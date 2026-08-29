package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// The state maintainer (L1 in _dev/MESH.md) keeps every LOCAL thread's full live
// state continuously fresh in the background, so a query is an O(1) read of the
// last-computed snapshot rather than an on-demand probe. It replaces the blocking
// content-diff probe on the read path with a CONTINUOUS rolling probe.
const (
	maintainerTick    = 300 * time.Millisecond // how often state is refreshed
	busyWindow        = 2 * time.Second        // changes within this window => working
	busyChangesNeeded = 2                      // >= this many changes in the window => working
	// maintainerConcurrency bounds how many threads are probed in parallel per tick.
	// The per-tick tmux enumeration + process table are resolved ONCE (see tick), so
	// the only remaining per-thread cost is a capture-pane; running those concurrently
	// keeps the whole sweep well under busyWindow even at ~100 threads, so each pane is
	// sampled often enough for the content-diff busy signal to latch.
	maintainerConcurrency = 8
	// authorityStaleBound: a REPORTED-busy (non-blocked) authority entry is dropped
	// when the pane has been byte-stable for this long AND the report itself is at
	// least this old. A real in-flight claude/pi turn animates its TUI every second
	// (spinner/elapsed timer), so a frozen pane contradicts the report — this is the
	// lost-turn_ended class: claude's Stop hook does NOT fire on a user interrupt
	// (Esc), which would otherwise pin busy until the thread's next prompt. Blocked
	// entries are exempt: a permission/question prompt is genuinely mid-turn with a
	// static pane. The report-age condition keeps a fresh turn_started on a
	// long-frozen pane alive until its first render catches up.
	authorityStaleBound = 2 * time.Minute
)

// liveState is the maintainer's per-thread working state. All fields except snap
// are touched ONLY by the single maintainer goroutine; snap is published under the
// maintainer's lock for readers.
type liveState struct {
	snap        api.ThreadSnapshot
	seeded      bool        // have we captured a baseline for this pane yet?
	lastContent string      // last captured pane content (for the diff)
	changes     []time.Time // timestamps of recent content changes (pruned to busyWindow)
	lastActive  int64       // unix time of the most recent change/turn
	// lastChange is when the pane content last ACTUALLY differed (the baseline
	// capture counts, so a freshly observed pane is never instantly "frozen").
	// Unlike lastActive it is never bumped by reported authority — it is the
	// evidence the authority staleness bound checks reports against.
	lastChange time.Time
	// heuristicBusySince is when the CURRENT content-diff busy streak began
	// (zero when the heuristic reads idle). It is the mirror of lastChange: the
	// evidence the reported-IDLE staleness bound checks against — a pane
	// animating continuously for the bound while the report still says idle
	// means the reporter is stale/dead.
	heuristicBusySince time.Time
	hasActiveTicket    bool // any bound ticket is `active` (set per tick from the digest)
	// stallFlagged latches "this stall episode already auto-flagged" so a
	// manual unflag is not re-flagged every tick while the SAME prompt sits
	// open; reset when the stall clears. Maintainer-goroutine only.
	stallFlagged bool
	// changedGen is the maintainer generation at which this thread's published
	// snapshot last CHANGED (delta sync, schema 41). Guarded by m.mu.
	changedGen int64
}

type maintainer struct {
	d       *Daemon
	mu      sync.RWMutex
	st      map[string]*liveState // threadID -> state (guarded by mu for structure + snap)
	started bool
	stop    chan struct{}
	done    chan struct{}
	// The rev-gated sweep (_dev/MESH_SCALE.md C3): record reads and the
	// per-thread refresh are O(live), not O(total). lastRev mirrors the store's
	// trigger-bumped change counter; while it is unchanged (and no hold
	// deadline has passed) the cached record list is reused and only UNSETTLED
	// threads — those with live runtime, which can change without a record
	// write — are re-derived. Any record write bumps the rev (schema triggers,
	// so no write path can dodge it) and forces one FULL sweep.
	lastRev        int64
	haveRev        bool
	cachedThreads  []api.Thread
	cachedTickets  map[string]store.TicketDigest
	cachedHolds    map[string]int64
	nextHoldExpiry int64 // earliest future effective-hold deadline (0 = none): its passing flips OnHold with NO record write, so it forces a full sweep
	// emitting gates change-pair emission to the eventer: false during the
	// FIRST sweep after daemon start (the baseline — existing state must not
	// re-announce), true after. Guarded by m.mu.
	emitting bool

	// fullSweeps / sweptThreads make the O(live) property observable to tests
	// (the probedPanes pattern): fullSweeps counts record re-reads, sweptThreads
	// accumulates per-tick refresh work.
	fullSweeps   int
	sweptThreads int

	// probedPanes / probedProcs count ticks that actually ran the (expensive)
	// per-tick tmux pane enumeration / ps process-table snapshot. They make the
	// idle early-out observable to tests — the per-tick fork/exec cost this guards
	// is invisible otherwise. Touched only by the maintainer goroutine.
	probedPanes int
	probedProcs int
	// home is THIS daemon's user home dir, used to stamp each thread's CwdRel
	// (~-relative cwd) so cross-machine viewers can label it. "" if undeterminable.
	home string

	// Delta sync (schema 41, guarded by mu). gen is a monotonic change counter:
	// bumped whenever a thread's PUBLISHED snapshot actually changes or a thread
	// disappears. epoch identifies this daemon boot — a peer's cursor from a
	// previous boot must full-resync, never alias into this run's counters.
	// tombstones records deleted ids (id -> the gen of the deletion) so a delta
	// can say "removed"; minReliableGen rises if tombstones are ever pruned, so a
	// cursor from before the prune degrades to a full resync — never to a cache
	// that silently keeps a deleted thread.
	gen            int64
	epoch          string
	tombstones     map[string]int64
	minReliableGen int64

	// staleBound is authorityStaleBound, injectable so a test need not freeze a
	// pane for two real minutes. Maintainer-goroutine reads only.
	staleBound time.Duration
}

// maxTombstones caps the deletion log. Deletions are rare (hundreds per boot at
// most); hitting the cap clears the log and raises minReliableGen — older cursors
// then full-resync, which is always correct.
const maxTombstones = 4096

// staleReportedBusy reports whether a reported-busy authority entry is
// contradicted by the pane: the content has not changed for at least bound AND
// the report itself is at least bound old (so a fresh turn_started on a
// long-frozen pane survives until its first render catches up). A zero
// lastChange (pane never captured) proves nothing.
func staleReportedBusy(lastChange time.Time, reportedAtUnix int64, now time.Time, bound time.Duration) bool {
	if lastChange.IsZero() {
		return false
	}
	return now.Sub(lastChange) >= bound && now.Unix()-reportedAtUnix >= int64(bound/time.Second)
}

// staleReportedIdle is the mirror of staleReportedBusy: a reported-IDLE authority
// entry is contradicted by the pane when the content-diff has read busy
// CONTINUOUSLY for at least bound (the pane is animating — a running turn) AND
// the report itself is at least bound old. A working reporter fires turn_started
// within a second or two of a turn's animation, so a pane animating for the whole
// bound while the report still says idle means the reporter is stale or dead (a
// session predating the hooks, or a reporter that died mid-session) — the daemon
// must then trust the heuristic instead of masking a live turn as idle. A zero
// heuristicBusySince (pane not currently animating) proves nothing.
func staleReportedIdle(heuristicBusySince time.Time, reportedAtUnix int64, now time.Time, bound time.Duration) bool {
	if heuristicBusySince.IsZero() {
		return false
	}
	return now.Sub(heuristicBusySince) >= bound && now.Unix()-reportedAtUnix >= int64(bound/time.Second)
}

func newMaintainer(d *Daemon) *maintainer {
	home, _ := os.UserHomeDir() // "" => CwdRel stays absolute (viewer shows the raw path)
	return &maintainer{
		d: d, st: map[string]*liveState{}, stop: make(chan struct{}), done: make(chan struct{}), home: home,
		epoch:      strconv.FormatInt(time.Now().UnixNano(), 36),
		tombstones: map[string]int64{},
		staleBound: authorityStaleBound,
	}
}

// start launches the maintainer loop.
func (m *maintainer) start() {
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	go m.run()
}

// stopAndWait stops the loop and blocks until it has exited. Safe if never started.
func (m *maintainer) stopAndWait() {
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started {
		return
	}
	close(m.stop)
	<-m.done
}

func (m *maintainer) run() {
	defer close(m.done)
	t := time.NewTicker(maintainerTick)
	defer t.Stop()
	m.tick()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.tick()
		}
	}
}

// tick refreshes local thread state. Record reads are REV-GATED
// (_dev/MESH_SCALE.md C3): the store's trigger-bumped change counter is one
// integer read; unchanged — and no hold deadline passed — means no record
// mutated, so the cached record list is reused and only UNSETTLED threads
// (live runtime, which changes without record writes) are re-derived. A
// settled archived thread costs nothing per tick.
func (m *maintainer) tick() {
	now := time.Now()
	rev, revErr := m.d.store.ThreadsRev()
	needFull := revErr != nil || !m.haveRev || rev != m.lastRev ||
		(m.nextHoldExpiry > 0 && now.Unix() >= m.nextHoldExpiry)
	if needFull {
		threads, err := m.d.store.ListThreads(true) // include archived — they still have live state
		if err != nil {
			return // transient store error: skip this tick, try again next (haveRev unchanged => still full)
		}
		tickets, err := m.d.store.OpenTicketDigests()
		if err != nil {
			tickets = map[string]store.TicketDigest{} // transient store error: refresh next full sweep
		}
		m.cachedThreads, m.cachedTickets = threads, tickets
		// Effective hold deadlines (own + inherited from same-machine ancestors),
		// computed on record changes so a held parent parks its whole subtree —
		// and the EARLIEST future deadline, whose passing must force the next
		// full sweep (OnHold auto-expiry has no record write to bump the rev).
		m.cachedHolds = effectiveHolds(threads)
		m.nextHoldExpiry = nextHoldDeadline(m.cachedHolds, now.Unix())
		if revErr == nil {
			m.lastRev, m.haveRev = rev, true
		}
		m.fullSweeps++
	}
	threads := m.cachedThreads
	// Idle early-out: with no local threads there is nothing whose live state needs
	// a pane/process probe, so skip the per-tick tmux + ps enumeration entirely.
	// Otherwise the daemon fork/exec's `tmux` + `ps` every ~300ms forever even on a
	// machine with zero threads — on a wake-locked battery leaf (e.g. termux) that is
	// a continuous ~10% CPU drain for no work. State is already empty, so just clear.
	if len(threads) == 0 {
		var deleted []snapChange
		m.mu.Lock()
		for id := range m.st {
			if sn := m.st[id].snap; sn.Thread.ID != "" && m.emitting {
				sn := sn
				deleted = append(deleted, snapChange{old: &sn, new: nil})
			}
			m.recordTombstoneLocked(id)
		}
		clear(m.st)
		m.emitting = true
		m.mu.Unlock()
		m.emit(deleted)
		return
	}
	attached, err := m.d.tmux.AttachedSessions()
	if err != nil {
		attached = map[string]int64{} // tmux unreachable => nothing attached
	}
	// Resolve EVERY thread's pane and the agent process table ONCE per tick — both
	// are tick-global. Doing them per-thread (FindPaneByThreadID re-enumerates all
	// panes; AgentUnderPane re-runs `ps`) made a ~100-thread sweep take seconds, so
	// each pane was sampled far less than twice per busyWindow and `busy` never
	// latched. If either enumeration fails, skip this tick and retry next.
	// One walk yields BOTH runtime indices: agent threads by pane marker, shell
	// threads by session marker (a shell thread's runtime is a whole session, so
	// it never appears in the pane index). The walk runs EVERY tick regardless of
	// the rev gate: a pane can gain/lose a thread marker with no record write
	// (that is exactly what makes a thread unsettled), so runtime discovery must
	// never be gated on record changes.
	panes, shellSessions, err := m.d.tmux.RuntimeIndex()
	if err != nil {
		return
	}
	m.probedPanes++
	// The process table is only consulted to resolve the agent under a MARKED pane
	// (refreshThread touches procs only on the found-pane path). If no local thread
	// currently occupies a pane — every thread is headless/idle — skip the `ps`
	// enumeration; those threads resolve to headless·idle (or turn-in-flight via the
	// registry) without it. procs stays nil and is never dereferenced.
	var procs *tmux.ProcSnapshot
	if len(panes) > 0 {
		procs, err = tmux.NewProcSnapshot()
		if err != nil {
			return
		}
		m.probedProcs++
	}

	sweep := threads
	if !needFull {
		sweep = m.unsettled(threads, panes, shellSessions)
	}
	m.sweptThreads += len(sweep)
	// Probe threads concurrently: each thread's liveState is independent and only the
	// m.st structure (guarded by m.mu) is shared, so a bounded worker pool collapses
	// the per-thread capture-pane cost without races.
	var wg sync.WaitGroup
	sem := make(chan struct{}, maintainerConcurrency)
	for _, th := range sweep {
		wg.Add(1)
		sem <- struct{}{}
		go func(th api.Thread) {
			defer wg.Done()
			defer func() { <-sem }()
			m.refreshThread(th, attached, m.cachedTickets, panes, shellSessions, procs, now, m.cachedHolds[th.ID])
		}(th)
	}
	wg.Wait()

	if needFull {
		// Drop state for threads that no longer exist (tombstoned for delta sync,
		// emitted as deletions). Deletions only happen via record writes, so the
		// full sweep is the only place this can fire.
		present := make(map[string]bool, len(threads))
		for _, th := range threads {
			present[th.ID] = true
		}
		var deleted []snapChange
		m.mu.Lock()
		for id := range m.st {
			if !present[id] {
				if sn := m.st[id].snap; sn.Thread.ID != "" && m.emitting {
					sn := sn
					deleted = append(deleted, snapChange{old: &sn, new: nil})
				}
				delete(m.st, id)
				m.recordTombstoneLocked(id)
			}
		}
		m.mu.Unlock()
		m.emit(deleted)
	}
	m.mu.Lock()
	m.emitting = true // the baseline sweep is over; changes announce from now on
	m.mu.Unlock()
}

// unsettled selects the threads whose state can change WITHOUT a record write:
// live runtime (a marked pane, a shell session, an in-flight headless turn, a
// reported authority entry) or a last-published state that claims liveness
// (a vanished pane/session must be observed flipping headless). Conservative
// by construction — when in doubt a thread is swept; a settled archived
// thread (the overwhelming majority) is not.
func (m *maintainer) unsettled(threads []api.Thread, panes map[string]api.PaneLocator, shells map[string]api.TmuxSession) []api.Thread {
	out := make([]api.Thread, 0, 16)
	for _, th := range threads {
		if _, ok := panes[th.ID]; ok {
			out = append(out, th)
			continue
		}
		if _, ok := shells[th.ID]; ok {
			out = append(out, th)
			continue
		}
		if m.d.turnInFlight(th.ID) {
			out = append(out, th)
			continue
		}
		if _, ok := m.d.reportedState(th.ID); ok {
			out = append(out, th)
			continue
		}
		m.mu.RLock()
		st := m.st[th.ID]
		live := st == nil || st.snap.Thread.ID == "" ||
			st.snap.Head == api.Headful || st.snap.Busy == api.BusyBusy || st.snap.AgentRunning
		m.mu.RUnlock()
		if live {
			out = append(out, th)
		}
	}
	return out
}

// nextHoldDeadline returns the earliest effective-hold deadline still in the
// future (0 = none): when it passes, OnHold flips with no record write, so the
// maintainer schedules a full sweep for it.
func nextHoldDeadline(effHolds map[string]int64, nowUnix int64) int64 {
	var next int64
	for _, until := range effHolds {
		if until > nowUnix && (next == 0 || until < next) {
			next = until
		}
	}
	return next
}

// emit forwards change pairs to the eventer (nil-safe for bare test daemons).
func (m *maintainer) emit(changes []snapChange) {
	if m.d.evt != nil && len(changes) > 0 {
		m.d.evt.observe(changes)
	}
}

// recordTombstoneLocked logs a deletion for delta sync (caller holds m.mu). At
// the cap the log resets and minReliableGen rises — pre-reset cursors then
// full-resync rather than ever missing a removal.
func (m *maintainer) recordTombstoneLocked(id string) {
	m.gen++
	if len(m.tombstones) >= maxTombstones {
		clear(m.tombstones)
		m.minReliableGen = m.gen
	}
	m.tombstones[id] = m.gen
}

// refreshThread recomputes one thread's live snapshot. The expensive bit
// (capture-pane) runs WITHOUT the lock; only publishing the snapshot takes it.
func (m *maintainer) refreshThread(th api.Thread, attached map[string]int64, tickets map[string]store.TicketDigest, panes map[string]api.PaneLocator, shellSessions map[string]api.TmuxSession, procs *tmux.ProcSnapshot, now time.Time, effHoldUntil int64) {
	m.mu.Lock()
	st := m.st[th.ID]
	if st == nil {
		st = &liveState{}
		m.st[th.ID] = st
	}
	m.mu.Unlock()

	dg := tickets[th.ID]
	st.hasActiveTicket = dg.HasActive // publish() turns this into TicketNeedsInput once head/busy are known
	snap := api.ThreadSnapshot{Thread: th, Attachment: api.Detached, TicketsOpen: dg.Count, TicketName: dg.NewestName,
		// Stamp the owner-relative cwd so cross-machine viewers (with a different home)
		// can apply their cwd_label rules — they cannot know this machine's home.
		CwdRel: config.TildeRelative(th.Cwd, m.home),
		// The effective (inherited) hold deadline; publish() turns it into OnHold.
		OnHoldEffectiveUnix: effHoldUntil}

	// SHELL THREAD: its runtime is a tmux SESSION, so head comes from the session
	// index, never the pane index — it has no marked pane of its own. Busy does
	// not apply at all (there is no turn to be executing), so it is pinned idle
	// rather than probed: a content-diff over a shell's panes would read a
	// `tail -f` as a permanent turn and a silent build as idle. Attachment works
	// exactly as for an agent thread.
	if th.AgentKind == api.ShellAgentKind {
		m.d.clearAuthority(th.ID)
		snap.Busy = api.BusyIdle
		if sess, live := shellSessions[th.ID]; live {
			snap.Head = api.Headful
			if act, ok := attached[sess.Name]; ok {
				snap.Attachment = api.Attached
				snap.AttachedActivityUnix = act
			}
			st.lastActive = now.Unix()
		} else {
			snap.Head = api.Headless
		}
		snap.LastActiveUnix = st.lastActive
		m.publish(st, snap)
		return
	}

	// The two axes are PROBED, never read from the record: head from pane
	// presence, busy from the pane content-diff (headful) or the turn registry
	// (headless).
	loc, found := panes[th.ID]
	if !found {
		// No pane ⇒ no live reporter either: reported authority is bounded by
		// pane liveness (a reporter that died with its pane must not pin busy).
		// StateAuthority stays unset — headless busy comes from the daemon-owned
		// turn registry, which needs no authority label.
		m.d.clearAuthority(th.ID)
		snap.Head = api.Headless
		snap.Busy = api.BusyIdle
		if m.d.turnInFlight(th.ID) {
			snap.Busy = api.BusyBusy
			st.lastActive = now.Unix()
		}
		snap.LastActiveUnix = st.lastActive
		m.publish(st, snap)
		return
	}
	agent, running := procs.AgentUnderPane(loc.PanePID)
	snap.AgentRunning = running && agent.Kind == th.AgentKind
	if !snap.AgentRunning {
		// A marked pane whose agent exited: no live runtime — headless·idle.
		m.d.clearAuthority(th.ID)
		snap.Head = api.Headless
		snap.Busy = api.BusyIdle
		snap.LastActiveUnix = st.lastActive
		m.publish(st, snap)
		return
	}
	snap.Head = api.Headful
	if act, ok := attached[loc.Session]; ok {
		snap.Attachment = api.Attached
		snap.AttachedActivityUnix = act
	}

	content, err := m.d.tmux.CapturePane(loc.Pane)
	if err != nil {
		// Pane vanished mid-tick: no runtime after all.
		m.d.clearAuthority(th.ID)
		snap.Head = api.Headless
		snap.Busy = api.BusyIdle
		snap.LastActiveUnix = st.lastActive
		m.publish(st, snap)
		return
	}
	// Rolling content-diff: a change is a tick where the pane bytes differ. "Working"
	// requires SUSTAINED change (>= busyChangesNeeded within busyWindow) so a one-off
	// blip (a rotating hint, MCP startup) reads as waiting, while a real turn's
	// animation reads as working. The first capture is a baseline, not a change.
	if !st.seeded {
		st.seeded = true
		st.lastContent = content
		st.lastChange = now
	} else if content != st.lastContent {
		st.changes = append(st.changes, now)
		st.lastActive = now.Unix()
		st.lastContent = content
		st.lastChange = now
	}
	cutoff := now.Add(-busyWindow)
	pruned := st.changes[:0]
	for _, c := range st.changes {
		if c.After(cutoff) {
			pruned = append(pruned, c)
		}
	}
	st.changes = pruned
	if len(st.changes) >= busyChangesNeeded {
		snap.Busy = api.BusyBusy
		if st.heuristicBusySince.IsZero() {
			st.heuristicBusySince = now // a new content-diff busy streak began
		}
	} else {
		snap.Busy = api.BusyIdle
		st.heuristicBusySince = time.Time{} // streak broken
	}
	// State authority (schema 43): a live in-agent reporter's turn state
	// OVERRIDES the content-diff for the busy axis — the report is exact where
	// the diff can only infer (keystroke echoes and redraws latch the diff just
	// like agent output). The content-diff above still ran: it keeps lastActive
	// and the rolling change window warm, so a released/cleared authority
	// degrades to an already-seeded heuristic, not a cold baseline. Which
	// mechanism decided is always stamped — degradation is visible, never silent.
	auth, hasAuth := m.d.reportedState(th.ID)
	if hasAuth && auth.busy && !auth.blocked &&
		staleReportedBusy(st.lastChange, auth.reportedAtUnix, now, m.staleBound) {
		// The pane has been byte-stable for the whole bound while the report
		// claims an in-flight turn — contradiction: a real turn animates the
		// TUI every second. This is the lost-turn_ended class (claude's Stop
		// hook does not fire on a user interrupt), which would otherwise pin
		// busy until the thread's next prompt. Drop the entry LOUDLY and
		// degrade to the heuristic — visible via state_authority. Blocked
		// entries are exempt (a permission/question prompt is genuinely
		// mid-turn with a static pane).
		log.Printf("maintainer: thread %s reported busy by %s %ds ago but its pane has been byte-stable for %ds — dropping stale authority (lost turn_ended, e.g. an interrupted turn)",
			th.ID, auth.source, now.Unix()-auth.reportedAtUnix, int(now.Sub(st.lastChange)/time.Second))
		m.d.clearAuthority(th.ID)
		hasAuth = false
	}
	if hasAuth && !auth.busy &&
		staleReportedIdle(st.heuristicBusySince, auth.reportedAtUnix, now, m.staleBound) {
		// The MIRROR of the reported-busy bound above: the pane has been
		// animating (content-diff busy) for the whole bound while the report
		// still claims idle. A live reporter fires turn_started within a second
		// or two of a turn's animation, so this means the reporter is stale or
		// dead — a session that predates the reporter hooks, or one whose hooks
		// stopped firing. Trust the heuristic instead of masking a running turn
		// as idle. Drop LOUDLY and degrade — visible via state_authority.
		log.Printf("maintainer: thread %s reported idle by %s %ds ago but its pane has been animating for %ds — dropping stale authority (reporter not tracking turns, e.g. a session predating the hooks)",
			th.ID, auth.source, now.Unix()-auth.reportedAtUnix, int(now.Sub(st.heuristicBusySince)/time.Second))
		m.d.clearAuthority(th.ID)
		hasAuth = false
	}
	if hasAuth {
		if auth.busy {
			snap.Busy = api.BusyBusy
			st.lastActive = now.Unix() // a reported in-flight turn is activity
		} else {
			snap.Busy = api.BusyIdle
		}
		snap.StateAuthority = api.AuthorityReported
	} else {
		snap.StateAuthority = api.AuthorityHeuristic
	}
	// Auto-flagging (autoflag.go): a turn end, or an unanswered question/
	// approval stall (the reporter's blocked state — daemon-internal since 44),
	// flags the thread — attended or not (the gate was removed 2026-07-25).
	// The store's AutoFlag respects flag_disabled + already-flagged atomically;
	// the record read next tick carries the flag into the published snapshot
	// (≤ one tick of lag).
	stalled := hasAuth && auth.blocked
	if reason, flag := autoFlagTrigger(st.snap.Busy, snap.Busy,
		snap.StateAuthority, m.d.heuristicFlagAllowed(th.AgentKind),
		stalled, auth.blockedReason, st.stallFlagged); flag {
		if _, err := m.d.store.AutoFlag(th.ID, reason); err == nil && stalled {
			st.stallFlagged = true
		}
	}
	if !stalled {
		st.stallFlagged = false // stall episode over; a new one may flag again
	}
	snap.LastActiveUnix = st.lastActive
	m.publish(st, snap)
}

// publish stores the computed snapshot for readers. It derives TicketNeedsInput
// here — the single choke point — so every early-return path gets it consistently:
// an active ticket on a headful·idle thread is the human-blocked state.
func (m *maintainer) publish(st *liveState, snap api.ThreadSnapshot) {
	snap.TicketNeedsInput = st.hasActiveTicket && snap.Head == api.Headful && snap.Busy == api.BusyIdle
	// "On hold right now" derives from the EFFECTIVE (own + inherited) deadline vs THIS
	// machine's clock (the owner is authoritative for its own threads); it auto-expires
	// once now passes, and a child stays held while a parent's hold is in the future.
	snap.OnHold = snap.OnHoldEffectiveUnix > time.Now().Unix()
	m.mu.Lock()
	// Delta sync: bump the generation only when the published value actually
	// changed — a byte-stable thread (archived, idle) keeps its changedGen and
	// therefore never re-transfers. A re-created id supersedes its tombstone.
	changed := !reflect.DeepEqual(st.snap, snap)
	if changed {
		m.gen++
		st.changedGen = m.gen
		if len(m.tombstones) != 0 {
			delete(m.tombstones, snap.Thread.ID)
		}
	}
	old := st.snap
	st.snap = snap
	emitting := m.emitting
	m.mu.Unlock()
	// The diff-fed eventer's LOCAL half (_dev/MESH_SCALE.md C2): the change is
	// observed here, at the moment it is published — never by re-diffing the
	// whole set. The baseline sweep (emitting=false) absorbs existing state
	// silently, exactly as the old eventer's first tick did.
	if changed && emitting {
		var oldp *api.ThreadSnapshot
		if old.Thread.ID != "" {
			o := old
			oldp = &o
		}
		n := snap
		m.emit([]snapChange{{old: oldp, new: &n}})
	}
}

// handleSnapshot serves GET /v1/snapshot: a pure read of the maintained live state
// (no on-demand probe). This is the PEER-FACING surface — what every peer's mesh
// sync (http directly, ssh via `thread snapshot --json`) re-downloads on change.
// The payload is always the FULL thread set semantically, archived included — an
// earlier revision slimmed archived-dead threads out of it and Lukas rejected
// that outright (an optimization must NEVER change what sesh shows); the savings
// live at the TRANSFER layer instead:
//   - `?since=<cursor>` (schema 41): a valid cursor gets only the rows changed
//     since it + removed ids + the next cursor — a steady round costs ~100 bytes,
//     a busy tick costs one row. An invalid/stale/other-boot cursor degrades to
//     the full payload, never to wrong data.
//   - If-None-Match/ETag (cursor-less clients): an unchanged full payload costs
//     a bodyless 304.
func (d *Daemon) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if since := r.URL.Query().Get("since"); since != "" {
		if changed, removed, cur, ok := d.maint.deltaSince(since); ok {
			writeJSON(w, http.StatusOK, api.MachineSnapshot{
				Schema:          api.SchemaVersion,
				Machine:         d.cfg.Machine,
				GeneratedAtUnix: time.Now().Unix(),
				Threads:         sortedSnapshotThreads(changed),
				Delta:           true,
				Removed:         removed,
				Generation:      d.maint.cursor(cur),
			})
			return
		}
	}
	snap, gen := d.maint.snapshotWithGen()
	snap.Threads = sortedSnapshotThreads(snap.Threads)
	snap.Generation = d.maint.cursor(gen)
	etag := snapshotETag(snap.Threads)
	if etag != "" {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeJSON(w, http.StatusOK, snap)
}

// sortedSnapshotThreads orders the wire payload by thread ID so it — and thus
// the ETag — is deterministic (the maintainer's map iteration is not).
func sortedSnapshotThreads(threads []api.ThreadSnapshot) []api.ThreadSnapshot {
	out := append([]api.ThreadSnapshot(nil), threads...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// snapshotETag is a strong validator over the exact peer-facing threads payload
// (already sorted by peerFacingThreads). Empty on a marshal failure — the caller
// then serves an unconditional 200, the safe direction (never a wrong 304).
func snapshotETag(threads []api.ThreadSnapshot) string {
	b, err := json.Marshal(threads)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// stateOf returns the maintained snapshot for one thread, if the maintainer has
// computed it yet (it ticks every ~300ms; a just-created thread may be absent for
// one tick, in which case callers fall back to an on-demand resolve).
func (m *maintainer) stateOf(id string) (api.ThreadSnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.st[id]
	if !ok {
		return api.ThreadSnapshot{}, false
	}
	return st.snap, true
}

// snapshot returns this machine's current maintained state (an O(1) read).
func (m *maintainer) snapshot() api.MachineSnapshot {
	snap, _ := m.snapshotWithGen()
	return snap
}

// snapshotWithGen returns the full maintained state PLUS the generation it
// corresponds to, read under one lock — the cursor stamped on a full response
// must match its payload exactly, or the next delta could skip a change that
// landed between two separate reads.
func (m *maintainer) snapshotWithGen() (api.MachineSnapshot, int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := api.MachineSnapshot{
		Schema:          api.SchemaVersion,
		Machine:         m.d.cfg.Machine,
		GeneratedAtUnix: time.Now().Unix(),
		Threads:         make([]api.ThreadSnapshot, 0, len(m.st)),
	}
	for _, st := range m.st {
		// A just-created thread has an entry but no published snapshot yet (its
		// first probe is in flight) — emitting the zero value would be a row with
		// an empty identity. It joins the snapshot on its first publish; until
		// then on-demand readers use the grid's resolveRow fallback.
		if st.snap.Thread.ID == "" {
			continue
		}
		out.Threads = append(out.Threads, st.snap)
	}
	return out, m.gen
}

// cursor renders an opaque delta-sync cursor for gen ("<boot-epoch>:<gen>").
func (m *maintainer) cursor(gen int64) string {
	return m.epoch + ":" + strconv.FormatInt(gen, 10)
}

// deltaSince resolves a client cursor. ok=false means the cursor cannot be
// served incrementally — another boot's epoch, a pruned/garbage/future gen —
// and the caller must answer with the FULL payload (degrading to full is always
// correct; serving a wrong delta never is). On ok, changed carries every
// published row with changedGen > the cursor's gen, removed every id
// tombstoned after it, and cur the generation the results correspond to.
func (m *maintainer) deltaSince(cursorStr string) (changed []api.ThreadSnapshot, removed []string, cur int64, ok bool) {
	epoch, genStr, found := strings.Cut(cursorStr, ":")
	if !found || epoch != m.epoch {
		return nil, nil, 0, false
	}
	gen, err := strconv.ParseInt(genStr, 10, 64)
	if err != nil {
		return nil, nil, 0, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if gen < m.minReliableGen || gen > m.gen {
		return nil, nil, 0, false
	}
	for _, st := range m.st {
		if st.snap.Thread.ID == "" {
			continue
		}
		if st.changedGen > gen {
			changed = append(changed, st.snap)
		}
	}
	for id, tombGen := range m.tombstones {
		if tombGen > gen {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	return changed, removed, m.gen, true
}
