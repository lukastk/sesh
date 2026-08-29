package daemon

import (
	"path/filepath"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

// newTestView wires a real store + a meshView with a synchronous change
// capture (the view emits outside its lock, in the caller's goroutine — these
// tests drive it single-threaded, so plain append is race-free).
func newTestView(t *testing.T) (*meshView, *store.Store, *[]snapChange) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	v := newMeshView(st)
	var got []snapChange
	v.onChange = func(cs []snapChange) { got = append(got, cs...) }
	return v, st, &got
}

func vsnap(id, machine, name string, busy api.Busy) api.ThreadSnapshot {
	return api.ThreadSnapshot{Thread: api.Thread{ID: id, Machine: machine, Name: name}, Busy: busy}
}

func changeKinds(cs []snapChange) map[string]string {
	out := map[string]string{}
	for _, c := range cs {
		switch {
		case c.old == nil && c.new != nil:
			out[c.new.ID] = "created"
		case c.old != nil && c.new == nil:
			out[c.old.ID] = "deleted"
		default:
			out[c.new.ID] = "changed"
		}
	}
	return out
}

func TestViewReplaceAllDiffAndEmit(t *testing.T) {
	v, _, got := newTestView(t)

	if err := v.replaceAll("m1", 100, []api.ThreadSnapshot{
		vsnap("a", "m1", "alpha", api.BusyIdle), vsnap("b", "m1", "beta", api.BusyIdle)}); err != nil {
		t.Fatalf("replaceAll: %v", err)
	}
	if k := changeKinds(*got); len(k) != 2 || k["a"] != "created" || k["b"] != "created" {
		t.Fatalf("first replace emitted %v, want two creations", k)
	}

	// A second replace: a changes, b vanishes, c appears — exactly those pairs.
	*got = nil
	if err := v.replaceAll("m1", 200, []api.ThreadSnapshot{
		vsnap("a", "m1", "alpha", api.BusyBusy), vsnap("c", "m1", "gamma", api.BusyIdle)}); err != nil {
		t.Fatalf("re-replace: %v", err)
	}
	k := changeKinds(*got)
	if len(k) != 3 || k["a"] != "changed" || k["b"] != "deleted" || k["c"] != "created" {
		t.Fatalf("diff emitted %v, want a changed / b deleted / c created", k)
	}
	for _, c := range *got {
		if c.new != nil && c.new.ID == "a" && (c.old == nil || c.old.Busy != api.BusyIdle || c.new.Busy != api.BusyBusy) {
			t.Fatalf("a's pair must carry old=idle new=busy, got old=%+v new=%+v", c.old, c.new)
		}
	}

	// A byte-identical replace emits NOTHING (no spurious events).
	*got = nil
	if err := v.replaceAll("m1", 300, []api.ThreadSnapshot{
		vsnap("a", "m1", "alpha", api.BusyBusy), vsnap("c", "m1", "gamma", api.BusyIdle)}); err != nil {
		t.Fatalf("identical replace: %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("identical replace emitted %d pairs, want 0", len(*got))
	}

	mvs := v.machineViews()
	if len(mvs) != 1 || mvs[0].Machine != "m1" || !mvs[0].Reachable || mvs[0].SyncedAtUnix != 300 {
		t.Fatalf("machineViews = %+v", mvs)
	}
	if len(mvs[0].Threads) != 2 || mvs[0].Threads[0].ID != "a" || mvs[0].Threads[1].ID != "c" {
		t.Fatalf("threads = %+v, want [a c] sorted by id", mvs[0].Threads)
	}
}

func TestViewApplyDelta(t *testing.T) {
	v, st, got := newTestView(t)

	// No base: a cursor without its machine is a resync signal, not a write.
	if ok, err := v.applyDelta("m1", 100, []api.ThreadSnapshot{vsnap("a", "m1", "alpha", api.BusyIdle)}, nil); ok || err != nil {
		t.Fatalf("applyDelta without base = (%v, %v), want (false, nil)", ok, err)
	}

	if err := v.replaceAll("m1", 100, []api.ThreadSnapshot{
		vsnap("a", "m1", "alpha", api.BusyIdle), vsnap("b", "m1", "beta", api.BusyIdle)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	*got = nil
	rows0 := v.rowsWritten.Load()

	ok, err := v.applyDelta("m1", 150, []api.ThreadSnapshot{vsnap("a", "m1", "alpha", api.BusyBusy)}, []string{"b"})
	if !ok || err != nil {
		t.Fatalf("applyDelta = (%v, %v)", ok, err)
	}
	k := changeKinds(*got)
	if len(k) != 2 || k["a"] != "changed" || k["b"] != "deleted" {
		t.Fatalf("delta emitted %v", k)
	}
	if dr := v.rowsWritten.Load() - rows0; dr != 2 {
		t.Fatalf("delta wrote %d rows, want 2 (one upsert + one removal)", dr)
	}
	// The rows are PERSISTED (a fresh view seeded from this store sees them).
	cache, err := st.LoadPeerCache()
	if err != nil || len(cache) != 1 || len(cache[0].Threads) != 1 || cache[0].Threads[0].Busy != api.BusyBusy {
		t.Fatalf("persisted rows = %+v (%v)", cache, err)
	}

	// An equal-valued "changed" row (e.g. a formatting-only refetch): row still
	// written, but NO pair — the eventer must not see a phantom change.
	*got = nil
	if ok, err := v.applyDelta("m1", 160, []api.ThreadSnapshot{vsnap("a", "m1", "alpha", api.BusyBusy)}, nil); !ok || err != nil {
		t.Fatalf("equal delta: (%v, %v)", ok, err)
	}
	if len(*got) != 0 {
		t.Fatalf("equal-valued delta emitted %d pairs, want 0", len(*got))
	}
}

func TestViewTouchFlushAndUnreachable(t *testing.T) {
	v, st, _ := newTestView(t)
	if v.touch("ghost", 1) {
		t.Fatalf("touch of an unknown machine must be false (caller full-refetches)")
	}
	if err := v.replaceAll("m1", 100, []api.ThreadSnapshot{vsnap("a", "m1", "alpha", api.BusyIdle)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !v.touch("m1", 999) {
		t.Fatalf("touch of a cached machine must succeed")
	}
	// The touch is view-only until the flush: boot freshness may under-claim,
	// never over-claim.
	if metas, _ := st.LoadPeerMetas(); metas[0].SyncedAtUnix != 100 {
		t.Fatalf("touch persisted eagerly (synced_at=%d) — it must batch into flushMeta", metas[0].SyncedAtUnix)
	}
	v.flushMeta()
	if metas, _ := st.LoadPeerMetas(); metas[0].SyncedAtUnix != 999 || !metas[0].Reachable {
		t.Fatalf("flushMeta did not persist the touch: %+v", metas[0])
	}

	// Unreachable: eager persist, threads retained, no emission.
	v.markUnreachable("m1")
	if metas, _ := st.LoadPeerMetas(); metas[0].Reachable {
		t.Fatalf("markUnreachable must persist eagerly")
	}
	mvs := v.machineViews()
	if mvs[0].Reachable || len(mvs[0].Threads) != 1 {
		t.Fatalf("unreachable machine = %+v, want stale-but-listed (offline browsing)", mvs[0])
	}
	if off := v.knownOffline(); !off["m1"] {
		t.Fatalf("knownOffline = %v", off)
	}
}

func TestViewDeleteMachine(t *testing.T) {
	v, st, got := newTestView(t)
	if err := v.replaceAll("m1", 100, []api.ThreadSnapshot{
		vsnap("a", "m1", "alpha", api.BusyIdle), vsnap("b", "m1", "beta", api.BusyIdle)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	*got = nil
	if err := v.deleteMachine("m1"); err != nil {
		t.Fatalf("deleteMachine: %v", err)
	}
	k := changeKinds(*got)
	if len(k) != 2 || k["a"] != "deleted" || k["b"] != "deleted" {
		t.Fatalf("delete emitted %v, want two deletions", k)
	}
	if mvs := v.machineViews(); len(mvs) != 0 {
		t.Fatalf("machine still in view: %+v", mvs)
	}
	if cache, _ := st.LoadPeerCache(); len(cache) != 0 {
		t.Fatalf("machine still in store: %+v", cache)
	}
}

// TestViewSeedIsSilentBaseline: rows persisted by a previous daemon life are
// absorbed at boot with ZERO emission — a restart must never re-announce
// existing state (the old eventer's first-tick baseline, by construction).
func TestViewSeedIsSilentBaseline(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := st.ReplacePeerThreads("m1", 100, []api.ThreadSnapshot{vsnap("a", "m1", "alpha", api.BusyIdle)}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	v := newMeshView(st)
	var got []snapChange
	v.onChange = func(cs []snapChange) { got = append(got, cs...) }
	if err := v.seedFromStore(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("seed emitted %d pairs — a restart re-announced existing state", len(got))
	}
	if mvs := v.machineViews(); len(mvs) != 1 || len(mvs[0].Threads) != 1 || mvs[0].SyncedAtUnix != 100 {
		t.Fatalf("seeded view = %+v", mvs)
	}
	// And the FIRST change after the baseline emits normally (diffed against
	// the seeded content, not re-announced wholesale).
	if err := v.replaceAll("m1", 200, []api.ThreadSnapshot{vsnap("a", "m1", "alpha", api.BusyBusy)}); err != nil {
		t.Fatalf("post-seed replace: %v", err)
	}
	if k := changeKinds(got); len(k) != 1 || k["a"] != "changed" {
		t.Fatalf("post-seed diff = %v, want exactly a changed", k)
	}
	if v.ownerOf("a") != "m1" || v.ownerOf("nope") != "" {
		t.Fatalf("ownerOf lookup broken")
	}
}
