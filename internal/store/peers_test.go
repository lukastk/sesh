package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func snapT(id, machine, name string, busy api.Busy) api.ThreadSnapshot {
	return api.ThreadSnapshot{Thread: api.Thread{ID: id, Machine: machine, Name: name}, Busy: busy}
}

func cacheFor(t *testing.T, s *Store, machine string) (PeerCache, bool) {
	t.Helper()
	all, err := s.LoadPeerCache()
	if err != nil {
		t.Fatalf("LoadPeerCache: %v", err)
	}
	for _, pc := range all {
		if pc.Machine == machine {
			return pc, true
		}
	}
	return PeerCache{}, false
}

func TestReplaceAndLoadPeerCache(t *testing.T) {
	s := openTestStore(t)

	if err := s.ReplacePeerThreads("peer1", 100, []api.ThreadSnapshot{
		snapT("a", "peer1", "alpha", api.BusyIdle),
		snapT("b", "peer1", "beta", api.BusyBusy),
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	pc, ok := cacheFor(t, s, "peer1")
	if !ok || !pc.Reachable || pc.SyncedAtUnix != 100 {
		t.Fatalf("meta after replace = %+v ok=%v, want reachable @100", pc.PeerMeta, ok)
	}
	if len(pc.Threads) != 2 || pc.Threads[0].ID != "a" || pc.Threads[0].Name != "alpha" ||
		pc.Threads[1].ID != "b" || pc.Threads[1].Busy != api.BusyBusy {
		t.Fatalf("threads round-trip = %+v", pc.Threads)
	}

	// A second full replace REPLACES: stale rows must not survive.
	if err := s.ReplacePeerThreads("peer1", 200, []api.ThreadSnapshot{snapT("c", "peer1", "gamma", api.BusyIdle)}); err != nil {
		t.Fatalf("re-replace: %v", err)
	}
	pc, _ = cacheFor(t, s, "peer1")
	if len(pc.Threads) != 1 || pc.Threads[0].ID != "c" || pc.SyncedAtUnix != 200 {
		t.Fatalf("after re-replace = %+v (stale rows must be gone)", pc)
	}
}

func TestUpsertPeerThreadsDelta(t *testing.T) {
	s := openTestStore(t)
	if err := s.ReplacePeerThreads("peer1", 100, []api.ThreadSnapshot{
		snapT("a", "peer1", "alpha", api.BusyIdle),
		snapT("b", "peer1", "beta", api.BusyIdle),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Delta: a changes, b removed, c added.
	if err := s.UpsertPeerThreads("peer1", 150, []api.ThreadSnapshot{
		snapT("a", "peer1", "alpha", api.BusyBusy),
		snapT("c", "peer1", "gamma", api.BusyIdle),
	}, []string{"b"}); err != nil {
		t.Fatalf("delta: %v", err)
	}
	pc, _ := cacheFor(t, s, "peer1")
	if pc.SyncedAtUnix != 150 {
		t.Errorf("delta did not touch synced_at: %d", pc.SyncedAtUnix)
	}
	got := map[string]api.ThreadSnapshot{}
	for _, th := range pc.Threads {
		got[th.ID] = th
	}
	if len(got) != 2 || got["a"].Busy != api.BusyBusy || got["c"].Name != "gamma" {
		t.Fatalf("delta application = %+v", got)
	}
	if _, still := got["b"]; still {
		t.Fatalf("removed row survived the delta")
	}

	// A re-created id appears in BOTH lists: removal first, then the upsert —
	// the row must exist afterwards.
	if err := s.UpsertPeerThreads("peer1", 160, []api.ThreadSnapshot{snapT("b", "peer1", "beta-again", api.BusyIdle)}, []string{"b"}); err != nil {
		t.Fatalf("re-create delta: %v", err)
	}
	pc, _ = cacheFor(t, s, "peer1")
	found := false
	for _, th := range pc.Threads {
		if th.ID == "b" && th.Name == "beta-again" {
			found = true
		}
	}
	if !found {
		t.Fatalf("re-created id lost (removed+changed in one round must land as the new row)")
	}
}

func TestPeerMetaAndUnreachable(t *testing.T) {
	s := openTestStore(t)

	// MarkPeerUnreachable on a never-synced machine: silent no-op (no row minted).
	if err := s.MarkPeerUnreachable("ghost"); err != nil {
		t.Fatalf("mark unreachable (absent): %v", err)
	}
	if metas, _ := s.LoadPeerMetas(); len(metas) != 0 {
		t.Fatalf("no-op mark minted a row: %+v", metas)
	}

	if err := s.SetPeerMeta("peer1", 100, true); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	if err := s.MarkPeerUnreachable("peer1"); err != nil {
		t.Fatalf("mark unreachable: %v", err)
	}
	metas, err := s.LoadPeerMetas()
	if err != nil || len(metas) != 1 {
		t.Fatalf("load metas: %v (%d)", err, len(metas))
	}
	if metas[0].Reachable || metas[0].SyncedAtUnix != 100 {
		t.Fatalf("unreachable must flip the flag ONLY (synced_at retained): %+v", metas[0])
	}
	// A later meta flush refreshes both.
	if err := s.SetPeerMeta("peer1", 200, true); err != nil {
		t.Fatalf("re-set meta: %v", err)
	}
	metas, _ = s.LoadPeerMetas()
	if !metas[0].Reachable || metas[0].SyncedAtUnix != 200 {
		t.Fatalf("meta flush = %+v", metas[0])
	}
}

func TestDeletePeerMachine(t *testing.T) {
	s := openTestStore(t)
	for _, m := range []string{"peer1", "peer2"} {
		if err := s.ReplacePeerThreads(m, 100, []api.ThreadSnapshot{snapT("t-"+m, m, m, api.BusyIdle)}); err != nil {
			t.Fatalf("seed %s: %v", m, err)
		}
	}
	if err := s.DeletePeerMachine("peer1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := cacheFor(t, s, "peer1"); ok {
		t.Fatalf("deleted machine still cached")
	}
	if pc, ok := cacheFor(t, s, "peer2"); !ok || len(pc.Threads) != 1 {
		t.Fatalf("unrelated machine disturbed: %+v ok=%v", pc, ok)
	}
}

// TestThreadsRevTriggers: every record write path bumps the change counter via
// the migration-23 TRIGGERS (so a future write path can never silently dodge the
// maintainer's full-sweep signal), while peer-cache writes do NOT (they would
// force a full sweep on every mesh round).
func TestThreadsRevTriggers(t *testing.T) {
	s := openTestStore(t)
	rev := func() int64 {
		t.Helper()
		r, err := s.ThreadsRev()
		if err != nil {
			t.Fatalf("ThreadsRev: %v", err)
		}
		return r
	}
	r0 := rev()

	if err := s.InsertThread(api.Thread{ID: "t1", Machine: "m", SessionName: "s", AgentKind: "pi"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	r1 := rev()
	if r1 <= r0 {
		t.Fatalf("thread INSERT did not bump rev (%d -> %d)", r0, r1)
	}
	if err := s.RenameThread("t1", "renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	r2 := rev()
	if r2 <= r1 {
		t.Fatalf("thread UPDATE did not bump rev (%d -> %d)", r1, r2)
	}
	if err := s.InsertTicket(api.Ticket{ID: "k1", Name: "k", Status: "triage"}); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	r3 := rev()
	if r3 <= r2 {
		t.Fatalf("ticket INSERT did not bump rev (%d -> %d)", r2, r3)
	}
	if err := s.DeleteThread("t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	r4 := rev()
	if r4 <= r3 {
		t.Fatalf("thread DELETE did not bump rev (%d -> %d)", r3, r4)
	}

	// Peer-cache writes are NOT record writes: no bump.
	if err := s.ReplacePeerThreads("p", 1, []api.ThreadSnapshot{snapT("x", "p", "x", api.BusyIdle)}); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	if err := s.UpsertPeerThreads("p", 2, nil, []string{"x"}); err != nil {
		t.Fatalf("peer delta: %v", err)
	}
	if r5 := rev(); r5 != r4 {
		t.Fatalf("peer-cache writes bumped the record rev (%d -> %d) — every mesh round would force a full sweep", r4, r5)
	}
}

// TestMigrationBlobToRows drives migration 23 against a REAL pre-23 database:
// blobs convert to per-thread rows with meta preserved, a corrupt blob is
// SKIPPED (not fatal — the cache is derived data), and the old table is
// dropped so a rolled-back binary fails loudly rather than reading stale data.
func TestMigrationBlobToRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sesh.db")

	// Build the pre-23 schema by applying every migration BEFORE the blob->rows
	// one, exactly as migrate() would have on an old binary.
	pre := len(migrations) - 1
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)", path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta (id, version) VALUES (1, 0) ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	for i := 0; i < pre; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatalf("apply pre-migration %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(`UPDATE meta SET version = ? WHERE id = 1`, pre); err != nil {
		t.Fatalf("set version: %v", err)
	}

	// Seed era-22 blobs: two healthy machines + one corrupt payload.
	seed := func(machine string, syncedAt, reachable int, payload string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO peer_snapshots (machine, synced_at_unix, reachable, payload) VALUES (?, ?, ?, ?)`,
			machine, syncedAt, reachable, payload); err != nil {
			t.Fatalf("seed blob %s: %v", machine, err)
		}
	}
	seed("peerA", 111, 1, `[{"id":"a1","machine":"peerA","name":"one","busy":"busy"},{"id":"a2","machine":"peerA","name":"two","busy":"idle"}]`)
	seed("peerB", 222, 0, `{{{not json`)
	seed("peerC", 333, 1, `[{"id":"c1","machine":"peerC","name":"solo","busy":"idle"}]`)
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Open through the store: migration 23 must run.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("schema version = %d (%v), want %d", v, err, len(migrations))
	}

	pcA, ok := cacheFor(t, s, "peerA")
	if !ok || pcA.SyncedAtUnix != 111 || !pcA.Reachable {
		t.Fatalf("peerA meta = %+v ok=%v", pcA.PeerMeta, ok)
	}
	if len(pcA.Threads) != 2 || pcA.Threads[0].ID != "a1" || pcA.Threads[0].Busy != api.BusyBusy || pcA.Threads[1].Name != "two" {
		t.Fatalf("peerA rows = %+v (blob must convert row-for-row)", pcA.Threads)
	}
	pcB, ok := cacheFor(t, s, "peerB")
	if !ok || pcB.Reachable || pcB.SyncedAtUnix != 222 {
		t.Fatalf("peerB meta = %+v ok=%v (meta preserved even for a corrupt payload)", pcB.PeerMeta, ok)
	}
	if len(pcB.Threads) != 0 {
		t.Fatalf("peerB rows = %+v, want none (corrupt payload skipped, not fatal)", pcB.Threads)
	}
	if pcC, _ := cacheFor(t, s, "peerC"); len(pcC.Threads) != 1 || pcC.Threads[0].ID != "c1" {
		t.Fatalf("peerC rows = %+v", pcC)
	}

	// The blob table is GONE — a rollback reads loudly, never stale.
	if _, err := s.db.Query(`SELECT * FROM peer_snapshots`); err == nil {
		t.Fatalf("peer_snapshots still exists — a rolled-back binary would silently serve the frozen blob")
	}

	// The rev machinery arrived and is live.
	if r, err := s.ThreadsRev(); err != nil || r != 0 {
		t.Fatalf("fresh rev = %d (%v), want 0", r, err)
	}
	if err := s.InsertThread(api.Thread{ID: "t1", Machine: "m", SessionName: "s", AgentKind: "pi"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if r, _ := s.ThreadsRev(); r == 0 {
		t.Fatalf("triggers not live after migration")
	}
}
