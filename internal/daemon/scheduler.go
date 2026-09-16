package daemon

// The scheduler (_dev/SCHEDULING.md §3.3, §5.4): the daemon's fourth loop, and
// its only clock-driven input. Every second it compares the wall clock against
// each enabled schedule's next_fire (a 1 s poll rather than a timer armed at
// the next instant, so suspend/resume, clock steps and edits underneath it
// cannot leave it wedged). A due schedule advances its next_fire FIRST (a slow
// run must not skew the cadence), then runs in its own goroutine through the
// five phases — due → guards → prepare → act → post — with EVERY ending
// recorded as an outcome: fired, skipped: <why>, or failed: <err>. Actions go
// through the daemon's own endpoints (the unix-socket client), so a scheduled
// revive/turn/spawn has exactly the CLI's gates and refusals; the pane paste
// goes through the typing guard in skip mode (a typing viewer skips the run —
// a periodic sender has a next occurrence). The engine never joins the
// maintainer's tick: it READS the published snapshot (stateOf, O(1)) and pays
// no capture.
//
// Missed occurrences (the laptop was asleep, the daemon was down): a schedule
// whose next_fire is more than scheduleMissGrace in the past is not "late", it
// was MISSED. catchup=skip rolls forward and counts the misses (loud in the
// log and on the record); catchup=once fires exactly one catch-up run. Never
// every missed occurrence — there is no use case and it is an unbounded storm.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/schedule"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

const (
	schedulerTick = 1 * time.Second
	// scheduleMissGrace separates "a little late" (a busy tick) from "missed
	// while down": past it, the catch-up policy applies instead of a fire.
	scheduleMissGrace = 90 * time.Second
	// scheduleRunsKept bounds the per-schedule run history.
	scheduleRunsKept = 50
	// scheduleMissedCap bounds the missed-occurrence count on a stale record.
	scheduleMissedCap = 100000
	// scheduleActionTimeout bounds one run's actions (revive + paste, spawn + turn start).
	scheduleActionTimeout = 3 * time.Minute
	// scheduleIdleForDefault is the dwell implied by `--if idle` when no
	// --idle-for is given (the stale-busy classes H58/H64).
	scheduleIdleForDefault = 60 * time.Second
)

// scheduler is the loop. tick is injectable for tests; the counters are the
// test observables (evaluate a fire without racing the wall clock).
type scheduler struct {
	d       *Daemon
	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
	tick    time.Duration

	// cache is the enabled schedules, refreshed on a store rev change.
	cacheMu sync.Mutex
	cache   []api.Schedule
	lastRev int64
	haveRev bool

	// inFlight holds schedule ids with a run in progress (never overlap).
	inFlight map[string]bool

	evaluated atomic.Int64
	fired     atomic.Int64
	skipped   atomic.Int64
	failed    atomic.Int64
	missed    atomic.Int64
}

func newScheduler(d *Daemon) *scheduler {
	return &scheduler{d: d, stop: make(chan struct{}), done: make(chan struct{}), tick: schedulerTick, inFlight: map[string]bool{}}
}

func (s *scheduler) start() {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	go s.run()
}

func (s *scheduler) stopAndWait() {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return
	}
	close(s.stop)
	<-s.done
}

func (s *scheduler) run() {
	defer close(s.done)
	if !s.d.schedules.Enabled {
		log.Printf("scheduler: [schedules] enabled = false — this daemon fires no schedules")
		return
	}
	s.seedReaper()
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.tickNow(time.Now())
		}
	}
}

// reload refreshes the cache when the store rev moved (or on first use).
func (s *scheduler) reload() error {
	rev, err := s.d.store.SchedulesRev()
	if err != nil {
		return err
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.haveRev && rev == s.lastRev {
		return nil
	}
	all, err := s.d.store.ListSchedules()
	if err != nil {
		return err
	}
	s.cache = s.cache[:0]
	for _, sc := range all {
		if sc.Enabled {
			s.cache = append(s.cache, sc)
		}
	}
	s.lastRev, s.haveRev = rev, true
	return nil
}

// tickNow is one evaluation pass at `now`: reconcile missed occurrences, fire
// the due ones, reap overrun spawn runs.
func (s *scheduler) tickNow(now time.Time) {
	if err := s.reload(); err != nil {
		log.Printf("scheduler: reload: %v", err)
		return
	}
	s.cacheMu.Lock()
	due := make([]api.Schedule, 0, len(s.cache))
	for _, sc := range s.cache {
		if sc.NextFireUnix > 0 && now.Unix() >= sc.NextFireUnix {
			due = append(due, sc)
		}
	}
	s.cacheMu.Unlock()
	for _, sc := range due {
		s.mu.Lock()
		if s.inFlight[sc.ID] {
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		s.dispatch(sc, now)
	}
	s.reapOverruns(now)
}

// dispatch decides what a due schedule does at `now`: a fire, or catch-up.
func (s *scheduler) dispatch(sc api.Schedule, now time.Time) {
	// Re-read the row: the cache may predate an edit or a run's state write.
	fresh, err := s.d.store.GetSchedule(sc.ID)
	if err != nil {
		log.Printf("scheduler: %s: %v", sc.ID, err)
		return
	}
	sc = fresh
	if !sc.Enabled || sc.NextFireUnix == 0 || now.Unix() < sc.NextFireUnix {
		return
	}
	spec, loc, err := s.parseSpec(sc)
	if err != nil {
		s.disable(sc, "spec no longer parses: "+err.Error())
		return
	}
	nowLoc := now.In(loc)
	// Life bounds.
	if sc.NotAfterUnix > 0 && now.Unix() > sc.NotAfterUnix {
		s.disable(sc, "not_after passed")
		return
	}
	// Missed occurrences: the daemon was not running when this was due.
	late := now.Sub(time.Unix(sc.NextFireUnix, 0))
	fireNow := true
	if late > scheduleMissGrace {
		missed := 1 + spec.Missed(time.Unix(sc.NextFireUnix, 0).In(loc), nowLoc, scheduleMissedCap)
		s.missed.Add(int64(missed))
		sc.Missed += missed
		switch sc.Catchup {
		case api.CatchupOnce:
			log.Printf("scheduler: %s (%s): %d occurrence(s) missed while the daemon was down — catchup=once, firing one catch-up run", sc.ID, sc.Name, missed)
		default:
			fireNow = false
			log.Printf("scheduler: %s (%s): %d occurrence(s) missed while the daemon was down — catchup=skip, rolling forward", sc.ID, sc.Name, missed)
		}
	}
	next := spec.Next(nowLoc)
	sc.NextFireUnix = 0
	if !next.IsZero() {
		sc.NextFireUnix = next.Unix()
	}
	if !fireNow {
		if sc.NextFireUnix == 0 {
			sc.Enabled, sc.DisabledReason = false, "no future occurrence"
		}
		if err := s.d.store.UpdateSchedule(sc); err != nil {
			log.Printf("scheduler: %s: roll forward: %v", sc.ID, err)
		}
		return
	}
	sc.LastFireUnix = now.Unix()
	if sc.NextFireUnix == 0 { // a one-shot: this is its only fire
		sc.Enabled, sc.DisabledReason = false, "one-shot fired"
	}
	if err := s.d.store.UpdateSchedule(sc); err != nil {
		log.Printf("scheduler: %s: advance: %v", sc.ID, err)
		return
	}
	s.mu.Lock()
	s.inFlight[sc.ID] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inFlight, sc.ID)
			s.mu.Unlock()
		}()
		s.runOnce(sc, now, false)
	}()
}

// parseSpec loads the record's zone and spec.
func (s *scheduler) parseSpec(sc api.Schedule) (schedule.Spec, *time.Location, error) {
	loc, err := time.LoadLocation(sc.TZ)
	if err != nil {
		return schedule.Spec{}, nil, fmt.Errorf("tz %q: %w", sc.TZ, err)
	}
	anchor := time.Unix(sc.CreatedAtUnix, 0)
	if sc.NotBeforeUnix > 0 {
		anchor = time.Unix(sc.NotBeforeUnix, 0)
	}
	spec, err := schedule.Parse(sc.Spec, loc, anchor.In(loc))
	return spec, loc, err
}

// disable turns a schedule off with a recorded, logged reason.
func (s *scheduler) disable(sc api.Schedule, why string) {
	sc.Enabled, sc.DisabledReason = false, why
	log.Printf("scheduler: %s (%s) DISABLED: %s", sc.ID, sc.Name, why)
	if err := s.d.store.UpdateSchedule(sc); err != nil {
		log.Printf("scheduler: %s: disable: %v", sc.ID, err)
	}
}

// runOnce executes one run (a fire, or run-now) and records its outcome.
// force bypasses the guards. Returns the outcome for run-now's response.
func (s *scheduler) runOnce(sc api.Schedule, firedAt time.Time, force bool) (outcome, detail, threadID string) {
	s.evaluated.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), scheduleActionTimeout)
	defer cancel()
	switch sc.Action {
	case api.ScheduleActionMessage:
		outcome, detail = s.runMessage(ctx, sc, firedAt, force)
	case api.ScheduleActionSpawn:
		outcome, detail, threadID = s.runSpawn(ctx, sc, firedAt, force)
	default:
		outcome, detail = api.OutcomeFailed, "unknown action "+sc.Action
	}
	s.record(sc.ID, firedAt, outcome, detail, threadID)
	return outcome, detail, threadID
}

// record writes the run row and the schedule's outcome counters, applies the
// failure breaker and max_fires, and prunes history.
func (s *scheduler) record(id string, firedAt time.Time, outcome, detail, threadID string) {
	full := outcome
	if detail != "" {
		full = outcome + ": " + detail
	}
	switch outcome {
	case api.OutcomeFired:
		s.fired.Add(1)
	case api.OutcomeSkipped:
		s.skipped.Add(1)
	default:
		s.failed.Add(1)
	}
	// A spawn run wrote its row before the turn (the reaper's seed); everything
	// else inserts one here. (schedule_id, fired_at) keys the row; run-now
	// inside the same second as a fire bumps the instant so neither is lost.
	if updated, err := s.d.store.SetScheduleRunOutcome(id, firedAt.Unix(), full, threadID); err != nil {
		log.Printf("scheduler: %s: run row: %v", id, err)
	} else if !updated {
		run := api.ScheduleRun{ScheduleID: id, FiredAtUnix: firedAt.Unix(), ThreadID: threadID, Outcome: full}
		for attempt := 0; attempt < 5; attempt++ {
			if err := s.d.store.InsertScheduleRun(run); err == nil {
				break
			} else if attempt == 4 {
				log.Printf("scheduler: %s: run row: %v", id, err)
			}
			run.FiredAtUnix++
		}
	}
	sc, err := s.d.store.GetSchedule(id)
	if err != nil {
		log.Printf("scheduler: %s: record: %v", id, err)
		return
	}
	sc.LastOutcome = full
	switch outcome {
	case api.OutcomeFired:
		sc.FireCount++
		sc.FailStreak = 0
		if threadID != "" {
			sc.LastRunThreadID = threadID
		}
		if sc.MaxFires > 0 && sc.FireCount >= sc.MaxFires {
			sc.Enabled, sc.DisabledReason = false, fmt.Sprintf("max_fires %d reached", sc.MaxFires)
			log.Printf("scheduler: %s (%s) DISABLED: max_fires %d reached", sc.ID, sc.Name, sc.MaxFires)
		}
	case api.OutcomeSkipped:
		sc.SkipCount++
	default:
		sc.FailStreak++
		log.Printf("scheduler: %s (%s) run FAILED (%d in a row): %s", sc.ID, sc.Name, sc.FailStreak, detail)
		if n := s.d.schedules.DisableAfterFailures; n > 0 && sc.FailStreak >= n {
			sc.Enabled, sc.DisabledReason = false, fmt.Sprintf("%d consecutive failures — last: %s", sc.FailStreak, detail)
			log.Printf("scheduler: %s (%s) DISABLED: %s", sc.ID, sc.Name, sc.DisabledReason)
		}
	}
	if err := s.d.store.UpdateSchedule(sc); err != nil {
		log.Printf("scheduler: %s: record: %v", id, err)
	}
	if err := s.d.store.PruneScheduleRuns(id, scheduleRunsKept); err != nil {
		log.Printf("scheduler: %s: prune: %v", id, err)
	}
	s.d.emitScheduleEvent(sc, full)
}

// --- the message action ---

// targetState reads the target's live state: the maintainer's published row
// when it has one (≤300 ms stale, O(1)), else an on-demand resolve.
func (s *scheduler) targetState(th api.Thread) (api.ThreadSnapshot, error) {
	if s.d.maint != nil {
		if snap, ok := s.d.maint.stateOf(th.ID); ok {
			return snap, nil
		}
	}
	head, busy, err := s.d.resolveState(th)
	if err != nil {
		return api.ThreadSnapshot{}, err
	}
	snap := api.ThreadSnapshot{Thread: th, Head: head, Busy: busy, Attachment: api.Detached}
	if head == api.Headful {
		if loc, found, ferr := s.d.tmux.FindPaneByThreadID(th.ID); ferr == nil && found {
			if n, _ := s.d.tmux.ClientCount(loc.Session); n > 0 {
				snap.Attachment = api.Attached
			}
		}
	}
	return snap, nil
}

// scheduleGuards evaluates the message guards against a snapshot. A non-empty
// reason is a skip. Pure over its inputs so it unit-tests as a truth table.
func scheduleGuards(rules api.ScheduleRules, snap api.ThreadSnapshot, blocked bool, now time.Time) (skip string) {
	if snap.Archived && !rules.AllowArchived {
		return "archived"
	}
	if snap.OnHold && !rules.IgnoreHold {
		return "on hold"
	}
	for _, cond := range rules.If {
		ok := true
		switch cond {
		case "idle":
			ok = snap.Busy == api.BusyIdle
		case "busy":
			ok = snap.Busy == api.BusyBusy
		case "headful":
			ok = snap.Head == api.Headful
		case "headless":
			ok = snap.Head == api.Headless
		case "attached":
			ok = snap.Attachment == api.Attached
		case "detached":
			ok = snap.Attachment != api.Attached
		case "flagged":
			ok = snap.Flagged
		case "not-flagged":
			ok = !snap.Flagged
		case "archived":
			ok = snap.Archived
		case "not-archived":
			ok = !snap.Archived
		case "blocked":
			ok = blocked
		case "not-blocked":
			ok = !blocked
		}
		if !ok {
			return "not " + cond
		}
	}
	if hasCond(rules.If, "idle") {
		dwell := scheduleIdleForDefault
		if rules.IdleForS != nil {
			dwell = time.Duration(*rules.IdleForS) * time.Second
		}
		if dwell > 0 && snap.LastActiveUnix > 0 {
			if quiet := now.Sub(time.Unix(snap.LastActiveUnix, 0)); quiet < dwell {
				return fmt.Sprintf("idle for only %s (need %s)", quiet.Round(time.Second), dwell)
			}
		}
	}
	if snap.Busy == api.BusyBusy && rules.WhenBusy != "send" {
		return "busy"
	}
	return ""
}

// validGuardWords is the closed --if vocabulary.
var validGuardWords = []string{"idle", "busy", "headful", "headless", "attached", "detached", "flagged", "not-flagged", "archived", "not-archived", "blocked", "not-blocked"}

func hasCond(conds []string, want string) bool {
	for _, c := range conds {
		if c == want {
			return true
		}
	}
	return false
}

// runMessage is the message action: guards → (revive) → deliver.
func (s *scheduler) runMessage(ctx context.Context, sc api.Schedule, firedAt time.Time, force bool) (outcome, detail string) {
	th, err := s.d.store.GetThread(sc.ThreadID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			s.disable(sc, "target thread "+sc.ThreadID[:min(8, len(sc.ThreadID))]+" no longer exists")
			return api.OutcomeFailed, "target thread no longer exists (schedule disabled)"
		}
		return api.OutcomeFailed, err.Error()
	}
	snap, err := s.targetState(th)
	if err != nil {
		return api.OutcomeFailed, "state: " + err.Error()
	}
	// The HEAD axis is read live: the delivery path (pane paste vs headless
	// turn vs revive) must follow the pane that exists NOW, not a snapshot up
	// to a maintainer tick old — a thread stopped a moment ago still reads
	// headful there, and a paste into a vanished pane is a loud failure. A
	// headless thread's busy is the daemon's own turn registry, exact and free,
	// so it is read live too (the snapshot lags a headless completion by a
	// tick); a headful thread's busy stays the maintainer's (content-diff or
	// reported authority — nothing cheaper is exact).
	if _, found, ferr := s.d.tmux.FindPaneByThreadID(th.ID); ferr == nil {
		if found {
			snap.Head = api.Headful
		} else {
			snap.Head = api.Headless
			snap.Busy = api.BusyIdle
			if s.d.turnInFlight(th.ID) {
				snap.Busy = api.BusyBusy
			}
		}
	}
	// Record fields come from the record just read (archived, flagged), not
	// the snapshot's copy of it. on_hold stays the maintainer's derivation
	// (own + inherited holds over the whole record set) — up to a tick old,
	// which for a hold set by a human is nothing.
	snap.Archived, snap.Flagged, snap.FlagDisabled = th.Archived, th.Flagged, th.FlagDisabled
	blocked := false
	if st, ok := s.d.reportedState(th.ID); ok {
		blocked = st.blocked
	}
	if !force {
		if why := scheduleGuards(sc.Rules, snap, blocked, firedAt); why != "" {
			return api.OutcomeSkipped, why
		}
	}
	text, err := s.d.expandPrompt(sc.Text)
	if err != nil {
		return api.OutcomeFailed, err.Error()
	}
	c := client.New(s.d.cfg.SocketPath())
	head := snap.Head
	if head == api.Headless {
		policy := sc.Rules.WhenHeadless
		if force && policy == "skip" {
			policy = "turn" // --force means "deliver now": a skip-only rule yields to a headless turn
		}
		switch policy {
		case "skip":
			return api.OutcomeSkipped, "headless"
		case "revive":
			if _, err := c.ThreadResume(ctx, th.ID); err != nil {
				return api.OutcomeFailed, "revive: " + err.Error()
			}
			if _, err := s.d.waitPaneReady(th, 90*time.Second); err != nil {
				return api.OutcomeFailed, "revived but " + err.Error()
			}
			head = api.Headful
		default: // turn
			if err := c.ThreadSendHeadless(ctx, th.ID, text); err != nil {
				return api.OutcomeFailed, "headless turn: " + err.Error()
			}
			return api.OutcomeFired, "headless turn"
		}
	}
	// A live pane: through the typing guard in SKIP mode (force = guard off).
	guard := s.d.pasteGuardFor(sc.Rules.RespectTypingMs, nil)
	if force {
		guard.Window = 0
	}
	preq := pasteRequest{
		thread: th, text: text, guard: guard, sender: "schedule " + sc.Name,
		resolve: func() (string, string, bool, error) {
			loc, found, ferr := s.d.tmux.FindPaneByThreadID(th.ID)
			if ferr != nil || !found {
				return "", "", false, ferr
			}
			return loc.Pane, loc.Session, true, nil
		},
	}
	if _, err := s.d.pasteNow(preq); err != nil {
		var typing errTyping
		if errors.As(err, &typing) {
			return api.OutcomeSkipped, "viewer typing (" + typing.ago.Round(time.Second).String() + " ago)"
		}
		return api.OutcomeFailed, err.Error()
	}
	if sc.Rules.WhenHeadless == "revive" && snap.Head == api.Headless {
		return api.OutcomeFired, "revived + pane"
	}
	return api.OutcomeFired, "pane"
}

// waitPaneReady waits until a thread's pane runs its agent with a rendered TUI
// (the sendWhenReady criterion, made synchronous and loud for unattended use).
func (d *Daemon) waitPaneReady(th api.Thread, timeout time.Duration) (api.PaneLocator, error) {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		loc, found, err := d.tmux.FindPaneByThreadID(th.ID)
		if err == nil && found {
			if agent, running := tmux.AgentUnderPane(loc.PanePID); running && agent.Kind == th.AgentKind {
				if cap, cerr := d.tmux.CapturePane(loc.Pane); cerr == nil {
					last = cap
					if nonBlank(cap) >= 3 {
						return loc, nil
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return api.PaneLocator{}, fmt.Errorf("agent never became ready within %s; last output: %s", timeout, strings.TrimSpace(tailLines(last, 5)))
}

// counters is the test observable.
func (s *scheduler) counters() (evaluated, fired, skipped, failed, missed int64) {
	return s.evaluated.Load(), s.fired.Load(), s.skipped.Load(), s.failed.Load(), s.missed.Load()
}
