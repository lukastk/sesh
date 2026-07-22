package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

// liveState is the maintainer's per-thread working state. All fields except snap
// are touched ONLY by the single maintainer goroutine; snap is published under the
// maintainer's lock for readers.
type liveState struct {
	snap            api.ThreadSnapshot
	seeded          bool        // have we captured a baseline for this pane yet?
	lastContent     string      // last captured pane content (for the diff)
	changes         []time.Time // timestamps of recent content changes (pruned to busyWindow)
	lastActive      int64       // unix time of the most recent change/turn
	hasActiveTicket bool        // any bound ticket is `active` (set per tick from the digest)
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
}

// maxTombstones caps the deletion log. Deletions are rare (hundreds per boot at
// most); hitting the cap clears the log and raises minReliableGen — older cursors
// then full-resync, which is always correct.
const maxTombstones = 4096

func newMaintainer(d *Daemon) *maintainer {
	home, _ := os.UserHomeDir() // "" => CwdRel stays absolute (viewer shows the raw path)
	return &maintainer{
		d: d, st: map[string]*liveState{}, stop: make(chan struct{}), done: make(chan struct{}), home: home,
		epoch:      strconv.FormatInt(time.Now().UnixNano(), 36),
		tombstones: map[string]int64{},
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

// tick refreshes the state of every local thread once.
func (m *maintainer) tick() {
	threads, err := m.d.store.ListThreads(true) // include archived — they still have live state
	if err != nil {
		return // transient store error: skip this tick, try again next
	}
	// Idle early-out: with no local threads there is nothing whose live state needs
	// a pane/process probe, so skip the per-tick tmux + ps enumeration entirely.
	// Otherwise the daemon fork/exec's `tmux` + `ps` every ~300ms forever even on a
	// machine with zero threads — on a wake-locked battery leaf (e.g. termux) that is
	// a continuous ~10% CPU drain for no work. State is already empty, so just clear.
	if len(threads) == 0 {
		m.mu.Lock()
		for id := range m.st {
			m.recordTombstoneLocked(id)
		}
		clear(m.st)
		m.mu.Unlock()
		return
	}
	attached, err := m.d.tmux.AttachedSessions()
	if err != nil {
		attached = map[string]int64{} // tmux unreachable => nothing attached
	}
	tickets, err := m.d.store.OpenTicketDigests()
	if err != nil {
		tickets = map[string]store.TicketDigest{} // transient store error: refresh next tick
	}
	// Resolve EVERY thread's pane and the agent process table ONCE per tick — both
	// are tick-global. Doing them per-thread (FindPaneByThreadID re-enumerates all
	// panes; AgentUnderPane re-runs `ps`) made a ~100-thread sweep take seconds, so
	// each pane was sampled far less than twice per busyWindow and `busy` never
	// latched. If either enumeration fails, skip this tick and retry next.
	panes, err := m.d.tmux.PaneIndexByThreadID()
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
	now := time.Now()

	// Effective hold deadlines (own + inherited from same-machine ancestors), computed
	// ONCE per tick over the full thread set so a held parent parks its whole subtree.
	effHolds := effectiveHolds(threads)

	present := make(map[string]bool, len(threads))
	for _, th := range threads {
		present[th.ID] = true
	}
	// Probe threads concurrently: each thread's liveState is independent and only the
	// m.st structure (guarded by m.mu) is shared, so a bounded worker pool collapses
	// the per-thread capture-pane cost without races.
	var wg sync.WaitGroup
	sem := make(chan struct{}, maintainerConcurrency)
	for _, th := range threads {
		wg.Add(1)
		sem <- struct{}{}
		go func(th api.Thread) {
			defer wg.Done()
			defer func() { <-sem }()
			m.refreshThread(th, attached, tickets, panes, procs, now, effHolds[th.ID])
		}(th)
	}
	wg.Wait()

	// Drop state for threads that no longer exist (tombstoned for delta sync).
	m.mu.Lock()
	for id := range m.st {
		if !present[id] {
			delete(m.st, id)
			m.recordTombstoneLocked(id)
		}
	}
	m.mu.Unlock()
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
func (m *maintainer) refreshThread(th api.Thread, attached map[string]int64, tickets map[string]store.TicketDigest, panes map[string]api.PaneLocator, procs *tmux.ProcSnapshot, now time.Time, effHoldUntil int64) {
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

	// The two axes are PROBED, never read from the record: head from pane
	// presence, busy from the pane content-diff (headful) or the turn registry
	// (headless).
	loc, found := panes[th.ID]
	if !found {
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
	if !reflect.DeepEqual(st.snap, snap) {
		m.gen++
		st.changedGen = m.gen
		if len(m.tombstones) != 0 {
			delete(m.tombstones, snap.Thread.ID)
		}
	}
	st.snap = snap
	m.mu.Unlock()
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
