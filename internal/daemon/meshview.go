package daemon

// The mesh view (_dev/MESH_SCALE.md C2): the daemon's ONE in-memory decoded
// copy of every peer's replicated threads, plus per-machine freshness meta.
// It is the in-process authority for everything that used to re-read and
// re-decode the peer-cache blobs — the eventer (which decoded the WHOLE mesh
// every second: 147 ms/s on the phone), /v1/mesh, the master self-heal's
// reachable set, the fan-out's offline gate, the subscriptions owner lookup,
// doctor. The store's peer_threads/peer_meta tables are its durable backing:
// content transitions write rows FIRST (store ahead of view on a crash — boot
// reseeds from the store, always consistent), then patch the view, then emit
// (old, new) pairs to the eventer — which is how a change is observed exactly
// once, with zero work when nothing changed.

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// snapChange is one observed thread transition: old == nil is a creation,
// new == nil a deletion, both set a change. The eventer field-compares pairs
// to decide which events fire.
type snapChange struct {
	old *api.ThreadSnapshot
	new *api.ThreadSnapshot
}

type meshView struct {
	st *store.Store
	// onChange receives every observed transition (nil = no observer, e.g.
	// tests). Called OUTSIDE the view lock, after the store and view are
	// consistent. Wired to eventer.observe by daemon.New.
	onChange func([]snapChange)

	mu       sync.RWMutex
	machines map[string]*peerViewState

	// contentWrites / rowsWritten count persisted content transitions and the
	// rows they wrote — the observable that steady-state rounds (304 / empty
	// delta) write NOTHING and a change writes O(changed rows), never the
	// whole set (the property the old full-blob rewrite violated).
	contentWrites atomic.Int64
	rowsWritten   atomic.Int64
}

type peerViewState struct {
	threads   map[string]api.ThreadSnapshot
	syncedAt  int64
	reachable bool
	metaDirty bool // freshness changed since last persisted (flushMeta)
}

func newMeshView(st *store.Store) *meshView {
	return &meshView{st: st, machines: map[string]*peerViewState{}}
}

// seedFromStore loads the persisted peer cache into the view at daemon start —
// the ONE remaining full decode, paid once per boot. Emits nothing: rows
// present at boot are the baseline (exactly the old eventer's silent first
// tick), so a restart never re-announces existing state.
func (v *meshView) seedFromStore() error {
	cache, err := v.st.LoadPeerCache()
	if err != nil {
		return fmt.Errorf("mesh view: seed: %w", err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, pc := range cache {
		ps := &peerViewState{threads: make(map[string]api.ThreadSnapshot, len(pc.Threads)),
			syncedAt: pc.SyncedAtUnix, reachable: pc.Reachable}
		for _, th := range pc.Threads {
			ps.threads[th.ID] = th
		}
		v.machines[pc.Machine] = ps
	}
	return nil
}

// replaceAll records a FULL successful sync: the machine's cached set is
// replaced wholesale (store first, then view), and every real difference vs
// the previous view is emitted. Full fetches happen at boot, on cursor loss,
// and for ssh-transport peers every round — the diff keeps their event
// semantics identical to the delta path's.
func (v *meshView) replaceAll(machine string, syncedAt int64, threads []api.ThreadSnapshot) error {
	if err := v.st.ReplacePeerThreads(machine, syncedAt, threads); err != nil {
		return err
	}
	v.contentWrites.Add(1)
	v.rowsWritten.Add(int64(len(threads)))
	next := make(map[string]api.ThreadSnapshot, len(threads))
	for _, th := range threads {
		next[th.ID] = th
	}
	var changes []snapChange
	v.mu.Lock()
	prev := v.machines[machine]
	if prev == nil {
		prev = &peerViewState{threads: map[string]api.ThreadSnapshot{}}
		v.machines[machine] = prev
	}
	for id, now := range next {
		if was, ok := prev.threads[id]; !ok {
			now := now
			changes = append(changes, snapChange{old: nil, new: &now})
		} else if !reflect.DeepEqual(was, now) {
			was, now := was, now
			changes = append(changes, snapChange{old: &was, new: &now})
		}
	}
	for id, was := range prev.threads {
		if _, still := next[id]; !still {
			was := was
			changes = append(changes, snapChange{old: &was, new: nil})
		}
	}
	prev.threads = next
	prev.syncedAt = syncedAt
	prev.reachable = true
	prev.metaDirty = false // persisted in the same store tx
	v.mu.Unlock()
	v.emit(changes)
	return nil
}

// applyDelta patches one machine's cached set with a delta round: removals
// first (a re-created id appears in both lists), then changed rows — written
// as O(changed) row upserts, patched into the view, emitted as pairs. false
// (with no error) means the machine has no view base — a cursor without its
// base is a bug-adjacent state; the caller must full-resync.
func (v *meshView) applyDelta(machine string, syncedAt int64, changed []api.ThreadSnapshot, removed []string) (bool, error) {
	v.mu.RLock()
	_, haveBase := v.machines[machine]
	v.mu.RUnlock()
	if !haveBase {
		return false, nil
	}
	if err := v.st.UpsertPeerThreads(machine, syncedAt, changed, removed); err != nil {
		return false, err
	}
	v.contentWrites.Add(1)
	v.rowsWritten.Add(int64(len(changed) + len(removed)))
	var changes []snapChange
	v.mu.Lock()
	ps := v.machines[machine]
	if ps == nil { // deleted between the check and here (peer removal): resync
		v.mu.Unlock()
		return false, nil
	}
	for _, id := range removed {
		if was, ok := ps.threads[id]; ok {
			was := was
			changes = append(changes, snapChange{old: &was, new: nil})
			delete(ps.threads, id)
		}
	}
	for _, now := range changed {
		now := now
		if was, ok := ps.threads[now.ID]; !ok {
			changes = append(changes, snapChange{old: nil, new: &now})
		} else if !reflect.DeepEqual(was, now) {
			was := was
			changes = append(changes, snapChange{old: &was, new: &now})
		}
		ps.threads[now.ID] = now
	}
	ps.syncedAt = syncedAt
	ps.reachable = true
	ps.metaDirty = false
	v.mu.Unlock()
	v.emit(changes)
	return true, nil
}

// touch refreshes freshness on a content-unchanged round (304 / empty delta) —
// view-only; the periodic flushMeta persists it. false = the machine is not in
// the view (removed under a surviving cursor): the caller must full-refetch —
// a conditional response with no base behind it must never look synced.
func (v *meshView) touch(machine string, syncedAt int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	ps := v.machines[machine]
	if ps == nil {
		return false
	}
	ps.syncedAt = syncedAt
	ps.reachable = true
	ps.metaDirty = true
	return true
}

// markUnreachable flags a machine stale, keeping its last-known threads
// (offline browsing). Persisted eagerly — reachability transitions are rare
// and boot-time reachability honesty is worth one tiny row write.
func (v *meshView) markUnreachable(machine string) {
	v.mu.Lock()
	if ps := v.machines[machine]; ps != nil {
		ps.reachable = false
		ps.metaDirty = false
	}
	v.mu.Unlock()
	v.st.MarkPeerUnreachable(machine) //nolint:errcheck — next round retries
}

// deleteMachine drops a machine from the cache entirely (peer removal),
// emitting deletions for its threads — the same events the old poll-diff
// emitted when cached rows vanished.
func (v *meshView) deleteMachine(machine string) error {
	if err := v.st.DeletePeerMachine(machine); err != nil {
		return err
	}
	var changes []snapChange
	v.mu.Lock()
	if ps := v.machines[machine]; ps != nil {
		for _, was := range ps.threads {
			was := was
			changes = append(changes, snapChange{old: &was, new: nil})
		}
		delete(v.machines, machine)
	}
	v.mu.Unlock()
	v.emit(changes)
	return nil
}

// flushMeta persists any freshness the touch path accumulated (the periodic
// flush + clean shutdown). A crash between flushes can only UNDER-claim boot
// freshness — the safe direction; the first sync round corrects it.
func (v *meshView) flushMeta() {
	type m struct {
		machine   string
		syncedAt  int64
		reachable bool
	}
	var dirty []m
	v.mu.RLock()
	for name, ps := range v.machines {
		if ps.metaDirty {
			dirty = append(dirty, m{name, ps.syncedAt, ps.reachable})
		}
	}
	v.mu.RUnlock()
	for _, d := range dirty {
		if v.st.SetPeerMeta(d.machine, d.syncedAt, d.reachable) != nil {
			continue // next flush retries; the view stays authoritative
		}
		v.mu.Lock()
		if ps := v.machines[d.machine]; ps != nil && ps.syncedAt == d.syncedAt && ps.reachable == d.reachable {
			ps.metaDirty = false
		}
		v.mu.Unlock()
	}
}

func (v *meshView) emit(changes []snapChange) {
	if v.onChange != nil && len(changes) > 0 {
		v.onChange(changes)
	}
}

// machineViews renders every cached peer as an api.MachineView (sorted by
// machine, threads sorted by id — the deterministic order the stored payload
// always had). The /v1/mesh peer section.
func (v *meshView) machineViews() []api.MachineView {
	v.mu.RLock()
	defer v.mu.RUnlock()
	names := make([]string, 0, len(v.machines))
	for name := range v.machines {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]api.MachineView, 0, len(names))
	for _, name := range names {
		ps := v.machines[name]
		out = append(out, api.MachineView{
			Machine:      name,
			Reachable:    ps.reachable,
			SyncedAtUnix: ps.syncedAt,
			Threads:      sortedSnapshotThreads(threadsOf(ps)),
		})
	}
	return out
}

func threadsOf(ps *peerViewState) []api.ThreadSnapshot {
	ths := make([]api.ThreadSnapshot, 0, len(ps.threads))
	for _, th := range ps.threads {
		ths = append(ths, th)
	}
	return ths
}

// threadsSorted returns one machine's cached threads sorted by id (the
// reconcile pass hashes exactly this — the same shape the peer's ETag covers).
func (v *meshView) threadsSorted(machine string) ([]api.ThreadSnapshot, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	ps := v.machines[machine]
	if ps == nil {
		return nil, false
	}
	return sortedSnapshotThreads(threadsOf(ps)), true
}

// knownOffline is the fan-out's gate: machines the cache DEFINITIVELY knows
// are down (reachable=false). Absent machines are absent from the map — a
// never-synced peer is still attempted live (see fanout.go).
func (v *meshView) knownOffline() map[string]bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	down := make(map[string]bool, len(v.machines))
	for name, ps := range v.machines {
		if !ps.reachable {
			down[name] = true
		}
	}
	return down
}

// metas returns every machine's freshness meta, sorted (doctor, mastermaint).
func (v *meshView) metas() []store.PeerMeta {
	v.mu.RLock()
	defer v.mu.RUnlock()
	names := make([]string, 0, len(v.machines))
	for name := range v.machines {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]store.PeerMeta, 0, len(names))
	for _, name := range names {
		ps := v.machines[name]
		out = append(out, store.PeerMeta{Machine: name, SyncedAtUnix: ps.syncedAt, Reachable: ps.reachable})
	}
	return out
}

// ownerOf finds which cached peer machine owns a thread id (” = none) — the
// subscriptions delivery lookup, previously a full-cache decode per question.
func (v *meshView) ownerOf(id string) string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for name, ps := range v.machines {
		if _, ok := ps.threads[id]; ok {
			return name
		}
	}
	return ""
}
