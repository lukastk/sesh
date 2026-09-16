package daemon

// The spawn action and its reaper (_dev/SCHEDULING.md §7). A run creates a
// thread through the daemon's own /v1/threads endpoint (no second spawn path),
// gives it the prompt — a headless turn for a headless run, a paste into the
// rendered pane for a headed one (the sendWhenReady criterion made synchronous
// and LOUD: an unattended run must not inherit --msg's fire-and-forget log
// line) — and records the run row with the thread id BEFORE the turn starts,
// so a daemon restart mid-run still knows what it made. The reaper applies
// --on-turn-end at the run's first turn end: the eventer's busy→idle edge for
// a headed run, the headless completion path for a headless one.
//
// The overlap guard keys on RUNTIME only — the previous run's thread is "still
// going" when it has a live pane or a turn in flight — never on the record:
// with --on-turn-end keep (the default) a finished run is an un-archived
// headless·idle record, and "record exists" would suppress every later run.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
)

// runWatch is one spawn run the reaper is watching.
type runWatch struct {
	scheduleID   string
	scheduleName string
	firedAt      int64
	threadID     string
	rules        api.ScheduleRules
	startedAt    time.Time
	// busySince is when the thread was last seen going busy after the prompt
	// landed; an idle edge is a turn end only after a real turn (a heuristic
	// pane flickers busy→idle on its own between the paste and the agent's
	// first render), so the reaper wants either reported authority or a few
	// seconds of busy.
	busySince time.Time
}

const runMinBusy = 3 * time.Second

// renderRunName fills the run's thread name: the template with {date}/{time}/
// {schedule} tokens, default "<schedule>-<YYYY-MM-DD-HHMM>".
func renderRunName(sc api.Schedule, firedAt time.Time) string {
	tpl := sc.NameTemplate
	if tpl == "" {
		tpl = "{schedule}-{date}-{time}"
	}
	r := strings.NewReplacer(
		"{schedule}", sc.Name,
		"{date}", firedAt.Format("2006-01-02"),
		"{time}", firedAt.Format("1504"),
		"{unix}", fmt.Sprint(firedAt.Unix()),
	)
	return r.Replace(tpl)
}

// runSpawn is the spawn action.
func (s *scheduler) runSpawn(ctx context.Context, sc api.Schedule, firedAt time.Time, force bool) (outcome, detail, threadID string) {
	c := client.New(s.d.cfg.SocketPath())
	// The overlap guard.
	if !force && sc.LastRunThreadID != "" && sc.Rules.IfPrevious != "spawn-anyway" {
		if prev, err := s.d.store.GetThread(sc.LastRunThreadID); err == nil {
			snap, serr := s.targetState(prev)
			if serr == nil && (snap.Head == api.Headful || snap.Busy == api.BusyBusy) {
				if sc.Rules.IfPrevious == "stop-previous" {
					if err := c.ThreadStopForce(ctx, prev.ID, true); err != nil {
						return api.OutcomeFailed, "stop previous run " + prev.ID[:8] + ": " + err.Error(), ""
					}
					s.endRun(prev.ID, "stopped by the next run", firedAt)
				} else {
					return api.OutcomeSkipped, "previous run still going (" + prev.ID[:8] + ")", ""
				}
			}
		}
	}
	name := renderRunName(sc, firedAt)
	req := api.NewThreadRequest{
		Agent: sc.Agent, Name: name, Cwd: sc.Cwd, Headless: sc.Headless,
		Parent: sc.ParentID, Model: sc.Model, Mode: sc.SpawnMode, IntoSession: sc.IntoSession,
	}
	resp, err := c.ThreadNew(ctx, req)
	if err != nil {
		return api.OutcomeFailed, "spawn: " + err.Error(), ""
	}
	th := resp.Thread
	threadID = th.ID
	// The run row goes in NOW, with the thread id, so a restart mid-turn still
	// knows this thread belongs to this run (the reaper's seed reads it back).
	if err := s.d.store.InsertScheduleRun(api.ScheduleRun{ScheduleID: sc.ID, FiredAtUnix: firedAt.Unix(), ThreadID: th.ID, Outcome: "running"}); err != nil {
		log.Printf("scheduler: %s: run row: %v", sc.ID, err)
	}
	if !sc.Rules.FlagOnEnd {
		// An unattended recurring turn would flag itself every time (H60 flags
		// every turn end); a scheduled run's thread is quiet unless asked.
		if err := c.ThreadFlag(ctx, th.ID, "disable"); err != nil {
			log.Printf("scheduler: %s: flag --disable on %s: %v", sc.ID, th.ID, err)
		}
	}
	prompt, err := s.d.expandPrompt(sc.Prompt)
	if err != nil {
		return api.OutcomeFailed, err.Error(), th.ID
	}
	if sc.Headless {
		s.watchRun(sc, th.ID, firedAt)
		if err := c.ThreadSendHeadlessMode(ctx, th.ID, prompt, sc.SpawnMode); err != nil {
			s.endRun(th.ID, "turn never started: "+err.Error(), time.Now())
			return api.OutcomeFailed, "headless turn: " + err.Error(), th.ID
		}
		return api.OutcomeFired, "headless " + th.ID[:8], th.ID
	}
	if _, err := s.d.waitPaneReady(th, 90*time.Second); err != nil {
		s.endRun(th.ID, "agent never became ready", time.Now())
		return api.OutcomeFailed, err.Error(), th.ID
	}
	preq := pasteRequest{
		thread: th, text: prompt, guard: pasteGuard{}, sender: "schedule " + sc.Name,
		resolve: func() (string, string, bool, error) {
			loc, found, ferr := s.d.tmux.FindPaneByThreadID(th.ID)
			if ferr != nil || !found {
				return "", "", false, ferr
			}
			return loc.Pane, loc.Session, true, nil
		},
	}
	s.watchRun(sc, th.ID, firedAt)
	if _, err := s.d.pasteNow(preq); err != nil {
		s.endRun(th.ID, "prompt never delivered: "+err.Error(), time.Now())
		return api.OutcomeFailed, "prompt: " + err.Error(), th.ID
	}
	return api.OutcomeFired, "pane " + th.ID[:8], th.ID
}

// --- the reaper ---

func (s *scheduler) watchRun(sc api.Schedule, threadID string, firedAt time.Time) {
	s.d.runsMu.Lock()
	defer s.d.runsMu.Unlock()
	if s.d.runWatch == nil {
		s.d.runWatch = map[string]*runWatch{}
	}
	s.d.runWatch[threadID] = &runWatch{scheduleID: sc.ID, scheduleName: sc.Name, firedAt: firedAt.Unix(), threadID: threadID, rules: sc.Rules, startedAt: time.Now()}
}

func (s *scheduler) takeWatch(threadID string) (*runWatch, bool) {
	s.d.runsMu.Lock()
	defer s.d.runsMu.Unlock()
	w, ok := s.d.runWatch[threadID]
	if ok {
		delete(s.d.runWatch, threadID)
	}
	return w, ok
}

// watchedRuns is a test observable.
func (s *scheduler) watchedRuns() int {
	s.d.runsMu.Lock()
	defer s.d.runsMu.Unlock()
	return len(s.d.runWatch)
}

// seedReaper restores watches from open run rows at daemon start. A headless
// run's turn was a goroutine of the dead daemon, so it died with it: recorded
// failed, reaped per policy. A headed run's pane outlives the daemon: watched.
func (s *scheduler) seedReaper() {
	open, err := s.d.store.OpenScheduleRuns()
	if err != nil {
		log.Printf("scheduler: seed reaper: %v", err)
		return
	}
	for _, r := range open {
		sc, err := s.d.store.GetSchedule(r.ScheduleID)
		if err != nil {
			s.markRunEnded(r, "schedule gone", time.Now())
			continue
		}
		th, err := s.d.store.GetThread(r.ThreadID)
		if err != nil {
			s.markRunEnded(r, "thread gone", time.Now())
			continue
		}
		s.watchRun(sc, th.ID, time.Unix(r.FiredAtUnix, 0))
		if _, found, _ := s.d.tmux.FindPaneByThreadID(th.ID); !found {
			log.Printf("scheduler: run %s of %s (%s): daemon restarted mid-turn (headless run) — reaping", th.ID[:8], sc.ID[:8], sc.Name)
			s.reap(th.ID, "daemon restarted mid-turn", false)
		}
	}
}

// onBusyEdge is fed by the eventer for LOCAL threads; a busy→idle edge after a
// real turn ends the run.
func (s *scheduler) onBusyEdge(snap api.ThreadSnapshot) {
	if snap.Machine != s.d.cfg.Machine {
		return
	}
	s.d.runsMu.Lock()
	w, ok := s.d.runWatch[snap.ID]
	if !ok {
		s.d.runsMu.Unlock()
		return
	}
	now := time.Now()
	if snap.Busy == api.BusyBusy {
		if w.busySince.IsZero() {
			w.busySince = now
		}
		s.d.runsMu.Unlock()
		return
	}
	realTurn := snap.StateAuthority == api.AuthorityReported || (!w.busySince.IsZero() && now.Sub(w.busySince) >= runMinBusy)
	s.d.runsMu.Unlock()
	if !realTurn {
		return
	}
	s.reap(snap.ID, "turn ended", true)
}

// onHeadlessDone is fed by the headless completion path (deterministic).
func (s *scheduler) onHeadlessDone(threadID string) {
	s.reap(threadID, "turn ended", true)
}

// reap applies the run's --on-turn-end policy and closes its row.
func (s *scheduler) reap(threadID, why string, ok bool) {
	w, found := s.takeWatch(threadID)
	if !found {
		return
	}
	c := client.New(s.d.cfg.SocketPath())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	policy := w.rules.OnTurnEnd
	if policy == "" {
		policy = "keep"
	}
	detail := why + " → " + policy
	var perr error
	switch policy {
	case "keep":
	case "stop":
		perr = c.ThreadStopForce(ctx, threadID, true)
	case "archive":
		perr = c.ThreadArchive(ctx, threadID, true)
	case "stop+archive":
		if perr = c.ThreadStopForce(ctx, threadID, true); perr == nil {
			perr = c.ThreadArchive(ctx, threadID, true)
		}
	case "delete":
		if perr = c.ThreadStopForce(ctx, threadID, true); perr == nil {
			perr = c.ThreadDelete(ctx, threadID, true)
		}
	}
	if perr != nil {
		detail += " FAILED: " + perr.Error()
		log.Printf("scheduler: run %s of %s (%s): %s", threadID[:8], w.scheduleID[:8], w.scheduleName, detail)
	} else {
		log.Printf("scheduler: run %s of %s (%s): %s", threadID[:8], w.scheduleID[:8], w.scheduleName, detail)
	}
	if !ok {
		detail = "FAILED: " + detail
	}
	s.markRunEnded(api.ScheduleRun{ScheduleID: w.scheduleID, FiredAtUnix: w.firedAt, ThreadID: threadID}, detail, time.Now())
}

// endRun closes a run the action itself ended (a failed turn start, a
// stop-previous), without applying the turn-end policy.
func (s *scheduler) endRun(threadID, why string, at time.Time) {
	w, found := s.takeWatch(threadID)
	if !found {
		return
	}
	s.markRunEnded(api.ScheduleRun{ScheduleID: w.scheduleID, FiredAtUnix: w.firedAt, ThreadID: threadID}, why, at)
}

func (s *scheduler) markRunEnded(r api.ScheduleRun, detail string, at time.Time) {
	rows, err := s.d.store.ListScheduleRuns(r.ScheduleID, 0)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.FiredAtUnix == r.FiredAtUnix {
			row.Detail, row.EndedAtUnix = detail, at.Unix()
			if err := s.d.store.UpdateScheduleRun(row); err != nil {
				log.Printf("scheduler: run row %s@%d: %v", r.ScheduleID, r.FiredAtUnix, err)
			}
			return
		}
	}
}

// reapOverruns stops runs past their --max-runtime, recording them failed.
func (s *scheduler) reapOverruns(now time.Time) {
	s.d.runsMu.Lock()
	var over []*runWatch
	for _, w := range s.d.runWatch {
		if w.rules.MaxRuntimeS > 0 && now.Sub(w.startedAt) > time.Duration(w.rules.MaxRuntimeS)*time.Second {
			over = append(over, w)
		}
	}
	s.d.runsMu.Unlock()
	for _, w := range over {
		log.Printf("scheduler: run %s of %s (%s): exceeded max-runtime %ds — stopping", w.threadID[:8], w.scheduleID[:8], w.scheduleName, w.rules.MaxRuntimeS)
		s.reap(w.threadID, fmt.Sprintf("exceeded max-runtime %ds", w.rules.MaxRuntimeS), false)
	}
}

// emitScheduleEvent fires schedule_fired / schedule_failed to the hooks
// (skips are not events). The thread fields describe the target or the
// spawned thread when there is one.
func (d *Daemon) emitScheduleEvent(sc api.Schedule, outcome string) {
	if d.hooks == nil || d.evt == nil {
		return
	}
	typ := ""
	switch {
	case strings.HasPrefix(outcome, api.OutcomeFired):
		typ = "schedule_fired"
	case strings.HasPrefix(outcome, api.OutcomeFailed):
		typ = "schedule_failed"
	default:
		return
	}
	ev := Event{Type: typ, ScheduleID: sc.ID, ScheduleName: sc.Name, ScheduleOutcome: outcome, AttachedActivityAgo: -1, AttachmentChangedAgo: -1}
	id := sc.ThreadID
	if sc.Action == api.ScheduleActionSpawn {
		id = sc.LastRunThreadID
	}
	if id != "" && d.maint != nil {
		if snap, ok := d.maint.stateOf(id); ok {
			ev.Snap = snap
		}
	}
	if ev.Snap.ID == "" {
		ev.Snap.Machine = d.cfg.Machine
	}
	d.hooks.handle(ev)
}
