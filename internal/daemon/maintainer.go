package daemon

import (
	"net/http"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
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
}

type maintainer struct {
	d       *Daemon
	mu      sync.RWMutex
	st      map[string]*liveState // threadID -> state (guarded by mu for structure + snap)
	started bool
	stop    chan struct{}
	done    chan struct{}
}

func newMaintainer(d *Daemon) *maintainer {
	return &maintainer{d: d, st: map[string]*liveState{}, stop: make(chan struct{}), done: make(chan struct{})}
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

// tick refreshes the state of every local thread once.
func (m *maintainer) tick() {
	threads, err := m.d.store.ListThreads(true) // include archived — they still have live state
	if err != nil {
		return // transient store error: skip this tick, try again next
	}
	attached, err := m.d.tmux.AttachedSessions()
	if err != nil {
		attached = map[string]bool{} // tmux unreachable => nothing attached
	}
	now := time.Now()

	present := make(map[string]bool, len(threads))
	for _, th := range threads {
		present[th.ID] = true
		m.refreshThread(th, attached, now)
	}
	// Drop state for threads that no longer exist.
	m.mu.Lock()
	for id := range m.st {
		if !present[id] {
			delete(m.st, id)
		}
	}
	m.mu.Unlock()
}

// refreshThread recomputes one thread's live snapshot. The expensive bit
// (capture-pane) runs WITHOUT the lock; only publishing the snapshot takes it.
func (m *maintainer) refreshThread(th api.Thread, attached map[string]bool, now time.Time) {
	m.mu.Lock()
	st := m.st[th.ID]
	if st == nil {
		st = &liveState{}
		m.st[th.ID] = st
	}
	m.mu.Unlock()

	snap := api.ThreadSnapshot{Thread: th, Attachment: api.Detached}

	// The two axes are PROBED, never read from the record: head from pane
	// presence, busy from the pane content-diff (headful) or the turn registry
	// (headless).
	loc, found, err := m.d.tmux.FindPaneByThreadID(th.ID)
	if err != nil || !found {
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
	agent, running := tmux.AgentUnderPane(loc.PanePID)
	snap.AgentRunning = running && agent.Kind == th.AgentKind
	if !snap.AgentRunning {
		// A marked pane whose agent exited: no live runtime — headless·idle.
		snap.Head = api.Headless
		snap.Busy = api.BusyIdle
		snap.LastActiveUnix = st.lastActive
		m.publish(st, snap)
		return
	}
	snap.Head = api.Headful
	if attached[loc.Session] {
		snap.Attachment = api.Attached
	}

	content, err := m.d.tmux.CapturePane(loc.Pane)
	if err != nil {
		// Pane vanished mid-tick: no runtime after all.
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
	} else if content != st.lastContent {
		st.changes = append(st.changes, now)
		st.lastActive = now.Unix()
		st.lastContent = content
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
	} else {
		snap.Busy = api.BusyIdle
	}
	snap.LastActiveUnix = st.lastActive
	m.publish(st, snap)
}

// publish stores the computed snapshot for readers.
func (m *maintainer) publish(st *liveState, snap api.ThreadSnapshot) {
	m.mu.Lock()
	st.snap = snap
	m.mu.Unlock()
}

// handleSnapshot serves GET /v1/snapshot: a pure read of the maintained live state
// (no on-demand probe). This is what the local TUI and the mesh sync consume.
func (d *Daemon) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.maint.snapshot())
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
	return out
}
