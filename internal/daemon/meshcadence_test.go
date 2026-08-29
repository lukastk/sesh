package daemon

// Tests for the issue-#1 mesh data rationing: the peer-facing snapshot (slim +
// ETag/304 conditional fetch) and the demand-driven sync cadence. The protocol
// tests drive the REAL handleSnapshot and the REAL client — no re-implementation
// of either side — so a drift between server ETag semantics and client
// conditional semantics fails here.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/peers"
)

// seedMaintDaemon builds a Daemon whose maintainer state holds the given
// snapshots (published through the REAL publish path, so delta-sync generations
// are assigned exactly as in production — no tmux/store needed to serve
// /v1/snapshot).
func seedMaintDaemon(t *testing.T, snaps ...api.ThreadSnapshot) *Daemon {
	t.Helper()
	d := &Daemon{}
	d.maint = newMaintainer(d)
	for _, sn := range snaps {
		pubThread(d, sn)
	}
	return d
}

// pubThread publishes (or re-publishes) one thread through the real publish path.
func pubThread(d *Daemon, sn api.ThreadSnapshot) {
	d.maint.mu.Lock()
	st := d.maint.st[sn.ID]
	if st == nil {
		st = &liveState{}
		d.maint.st[sn.ID] = st
	}
	d.maint.mu.Unlock()
	d.maint.publish(st, sn)
}

// dropThread removes one thread the way the maintainer's tick does (tombstoned).
func dropThread(d *Daemon, id string) {
	d.maint.mu.Lock()
	delete(d.maint.st, id)
	d.maint.recordTombstoneLocked(id)
	d.maint.mu.Unlock()
}

func snap(id string, archived bool, head api.Head) api.ThreadSnapshot {
	return api.ThreadSnapshot{Thread: api.Thread{ID: id, Machine: "m", Archived: archived}, Head: head}
}

// TestSnapshotFullAndConditional: the real /v1/snapshot handler must (a) serve
// the FULL thread set — archived included, headless or not; an earlier revision
// slimmed archived-dead rows out and Lukas rejected it (an optimization must
// never change what sesh shows) — and (b) answer a matching If-None-Match with a
// bodyless 304, then a NEW payload once the state changes. Driven end-to-end
// through the real client.SnapshotConditional.
func TestSnapshotFullAndConditional(t *testing.T) {
	d := seedMaintDaemon(t,
		snap("live-1", false, api.Headful),
		snap("arch-live", true, api.Headful),
		snap("arch-dead", true, api.Headless),
	)
	srv := httptest.NewServer(http.HandlerFunc(d.handleSnapshot))
	defer srv.Close()
	c := client.NewRemote(strings.TrimPrefix(srv.URL, "http://"), "")

	ms, etag, notMod, err := c.SnapshotConditional(t.Context(), "", "")
	if err != nil || notMod {
		t.Fatalf("first fetch: err=%v notMod=%v", err, notMod)
	}
	if etag == "" {
		t.Fatalf("first fetch carried no ETag")
	}
	if ms.Generation == "" {
		t.Fatalf("first fetch carried no delta-sync cursor")
	}
	ids := map[string]bool{}
	for _, th := range ms.Threads {
		ids[th.ID] = true
	}
	if !ids["live-1"] || !ids["arch-live"] || !ids["arch-dead"] {
		t.Errorf("snapshot must carry the FULL thread set (archived-dead included): %v", ids)
	}

	// Unchanged state + the ETag => 304, no payload (the pre-41 client flow).
	_, etag2, notMod, err := c.SnapshotConditional(t.Context(), etag, "")
	if err != nil || !notMod || etag2 != etag {
		t.Fatalf("conditional refetch: err=%v notMod=%v etag2=%q (want 304 with the same ETag)", err, notMod, etag2)
	}

	// A state change => full 200 with a NEW ETag — including an archived-dead
	// addition: it is DATA, so it must transfer and be visible.
	pubThread(d, snap("arch-dead-2", true, api.Headless))
	ms3, etag3, notMod, err := c.SnapshotConditional(t.Context(), etag, "")
	if err != nil || notMod {
		t.Fatalf("post-change fetch: err=%v notMod=%v (a changed snapshot must be a full 200)", err, notMod)
	}
	if etag3 == etag {
		t.Errorf("ETag did not change with the payload")
	}
	found := false
	for _, th := range ms3.Threads {
		if th.ID == "arch-dead-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("post-change payload missing the new archived thread")
	}
}

// TestSnapshotDelta drives the delta protocol end-to-end through the REAL
// handler and REAL client: full fetch → cursor; a one-row change → a delta with
// EXACTLY that row; no change → an empty delta; a deletion → removed; a
// garbage/other-boot cursor → full resync. Archived rows stay visible
// throughout (the no-UX-tradeoffs rule).
func TestSnapshotDelta(t *testing.T) {
	d := seedMaintDaemon(t,
		snap("live-1", false, api.Headful),
		snap("arch-dead", true, api.Headless),
	)
	srv := httptest.NewServer(http.HandlerFunc(d.handleSnapshot))
	defer srv.Close()
	c := client.NewRemote(strings.TrimPrefix(srv.URL, "http://"), "")

	full, _, _, err := c.SnapshotConditional(t.Context(), "", "")
	if err != nil || full.Delta || full.Generation == "" || len(full.Threads) != 2 {
		t.Fatalf("full fetch: err=%v delta=%v gen=%q threads=%d", err, full.Delta, full.Generation, len(full.Threads))
	}

	// No change since the cursor => an EMPTY delta (the ~100 B steady state).
	d1, _, notMod, err := c.SnapshotConditional(t.Context(), "", full.Generation)
	if err != nil || notMod || !d1.Delta || len(d1.Threads) != 0 || len(d1.Removed) != 0 {
		t.Fatalf("empty delta: err=%v notMod=%v delta=%v threads=%d removed=%d", err, notMod, d1.Delta, len(d1.Threads), len(d1.Removed))
	}
	if d1.Generation != full.Generation {
		t.Errorf("no-change delta advanced the cursor: %q -> %q", full.Generation, d1.Generation)
	}

	// One row changes => the delta carries EXACTLY that row (the archived row
	// must NOT re-transfer — it is byte-stable).
	changed := snap("live-1", false, api.Headful)
	changed.Busy = api.BusyBusy
	pubThread(d, changed)
	d2, _, _, err := c.SnapshotConditional(t.Context(), "", full.Generation)
	if err != nil || !d2.Delta {
		t.Fatalf("changed delta: err=%v delta=%v", err, d2.Delta)
	}
	if len(d2.Threads) != 1 || d2.Threads[0].ID != "live-1" || d2.Threads[0].Busy != api.BusyBusy {
		t.Fatalf("changed delta must carry exactly the changed row, got %+v", d2.Threads)
	}
	if d2.Generation == full.Generation {
		t.Errorf("change did not advance the cursor")
	}

	// A deletion => removed id; the archived row is still untouched.
	dropThread(d, "arch-dead")
	d3, _, _, err := c.SnapshotConditional(t.Context(), "", d2.Generation)
	if err != nil || !d3.Delta || len(d3.Threads) != 0 || len(d3.Removed) != 1 || d3.Removed[0] != "arch-dead" {
		t.Fatalf("removal delta: err=%v delta=%v threads=%v removed=%v", err, d3.Delta, d3.Threads, d3.Removed)
	}

	// Garbage and other-boot cursors DEGRADE TO FULL — never a wrong delta.
	for _, bad := range []string{"nonsense", "xx:12", "999zzz:artifact", d.maint.epoch + ":999999"} {
		f, _, _, err := c.SnapshotConditional(t.Context(), "", bad)
		if err != nil || f.Delta {
			t.Fatalf("cursor %q: err=%v delta=%v — an unservable cursor must yield the FULL payload", bad, err, f.Delta)
		}
		if f.Generation == "" {
			t.Errorf("cursor %q: full resync carried no fresh cursor", bad)
		}
	}

	// A "daemon restart" (fresh epoch, gens restart) must reject the old cursor
	// into a full resync — cursors never alias across boots.
	d2nd := seedMaintDaemon(t, snap("live-1", false, api.Headful))
	srv2 := httptest.NewServer(http.HandlerFunc(d2nd.handleSnapshot))
	defer srv2.Close()
	c2 := client.NewRemote(strings.TrimPrefix(srv2.URL, "http://"), "")
	f2, _, _, err := c2.SnapshotConditional(t.Context(), "", d2.Generation)
	if err != nil || f2.Delta {
		t.Fatalf("post-restart fetch with an old-boot cursor: err=%v delta=%v (must be full)", err, f2.Delta)
	}
}

// syncResp is one observed wire exchange at the fixture peer: the status code
// and the DECODED body (zero value for a bodyless 304).
type syncResp struct {
	code int
	body api.MachineSnapshot
}

// meshSync304Fixture: a meshSync against a REAL store and a peer served by the
// REAL handleSnapshot, so the syncer's conditional loop — delta cursors AND the
// legacy ETag flow — is proven against the actual server implementation, with
// every wire exchange recorded for delta-ness assertions.
type meshSync304Fixture struct {
	d     *Daemon // the syncing daemon
	peer  *Daemon // the peer daemon behind handleSnapshot
	s     *meshSync
	resps *[]syncResp
	mu    *sync.Mutex
}

func newMeshSync304Fixture(t *testing.T) *meshSync304Fixture {
	t.Helper()
	f := newMeshSyncFixture(t, 2*time.Second) // reuse the real-store fixture scaffolding
	t.Cleanup(f.close)

	peerD := seedMaintDaemon(t, snap("p-1", false, api.Headful))
	var mu sync.Mutex
	var resps []syncResp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		peerD.handleSnapshot(rec, r)
		var body api.MachineSnapshot
		json.Unmarshal(rec.Body.Bytes(), &body) //nolint:errcheck — zero body on 304
		mu.Lock()
		resps = append(resps, syncResp{code: rec.Code, body: body})
		mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes()) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	home := f.s.d.cfg.Home
	reg := peers.Registry{Peers: map[string]peers.Peer{
		"etagpeer": {Machine: "etagpeer", SSH: "http-only.invalid", Home: "/nonexistent", Binary: "sesh",
			ApiAddr: strings.TrimPrefix(srv.URL, "http://"), ApiToken: "tok"},
	}}
	if err := reg.Save(home + "/peers.json"); err != nil {
		t.Fatalf("peers save: %v", err)
	}
	return &meshSync304Fixture{d: f.s.d, peer: peerD, s: f.s, resps: &resps, mu: &mu}
}

func (f *meshSync304Fixture) last(t *testing.T) syncResp {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(*f.resps) == 0 {
		t.Fatalf("no wire exchanges recorded")
	}
	return (*f.resps)[len(*f.resps)-1]
}

// state reads the synced peer's cached view: freshness + the cached ids.
func (f *meshSync304Fixture) state(t *testing.T) (syncedAt int64, ids map[string]bool) {
	t.Helper()
	ids = map[string]bool{}
	for _, mv := range f.d.view.machineViews() {
		if mv.Machine != "etagpeer" {
			continue
		}
		for _, th := range mv.Threads {
			ids[th.ID] = true
		}
		return mv.SyncedAtUnix, ids
	}
	return 0, ids
}

// TestMeshSyncDeltaFetch drives the syncer's delta loop against the real
// server: round 1 = full payload (cursor established); round 2 = EMPTY delta
// (freshness touched, payload byte-untouched); a one-row peer change = a delta
// carrying EXACTLY that row, applied onto the cached set; a peer deletion = a
// removal applied. The recorded wire exchanges prove delta-ness — this is the
// transfer-layer replacement for the rejected archived-slim, so the cached SET
// must stay complete throughout.
func TestMeshSyncDeltaFetch(t *testing.T) {
	f := newMeshSync304Fixture(t)

	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, ids := f.state(t); return ids["p-1"] })
	if last := f.last(t); last.code != http.StatusOK || last.body.Delta {
		t.Fatalf("first sync: code=%d delta=%v, want a full 200", last.code, last.body.Delta)
	}
	syncedAt1, _ := f.state(t)
	writes1 := f.d.view.contentWrites.Load()

	// Force a visibly-later clock second so the touch is observable.
	time.Sleep(1100 * time.Millisecond)
	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { sa, _ := f.state(t); return sa > syncedAt1 })
	if w := f.d.view.contentWrites.Load(); w != writes1 {
		t.Fatalf("content persisted on an unchanged snapshot (%d -> %d writes) — the empty-delta/touch path must write NOTHING", writes1, w)
	}
	if last := f.last(t); !last.body.Delta || len(last.body.Threads) != 0 || len(last.body.Removed) != 0 {
		t.Fatalf("steady-state exchange = %+v, want an EMPTY delta", last.body)
	}

	// One peer row changes => the wire carries EXACTLY that row; the cache ends
	// up with BOTH threads (delta applied onto the existing set), and the
	// persisted write is O(changed rows), never the whole set.
	rows1 := f.d.view.rowsWritten.Load()
	pubThread(f.peer, snap("p-2", false, api.Headful))
	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, ids := f.state(t); return ids["p-2"] })
	last := f.last(t)
	if !last.body.Delta || len(last.body.Threads) != 1 || last.body.Threads[0].ID != "p-2" {
		t.Fatalf("change exchange must be a one-row delta, got %+v", last.body)
	}
	if _, ids := f.state(t); !ids["p-1"] {
		t.Fatalf("delta application lost the unchanged row p-1")
	}
	if dr := f.d.view.rowsWritten.Load() - rows1; dr != 1 {
		t.Fatalf("one-row delta wrote %d rows, want 1 (the full-set rewrite is the bug this replaced)", dr)
	}

	// A peer-side deletion arrives as a removal and is applied.
	dropThread(f.peer, "p-1")
	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, ids := f.state(t); return !ids["p-1"] })
	if last := f.last(t); !last.body.Delta || len(last.body.Removed) != 1 || last.body.Removed[0] != "p-1" {
		t.Fatalf("deletion exchange must be a removal delta, got %+v", last.body)
	}
	if _, ids := f.state(t); !ids["p-2"] {
		t.Fatalf("removal application lost the surviving row p-2")
	}
}

// TestMeshSyncMissingRowRefetches: a conditional response (empty delta) whose
// cache BASE is GONE (the machine was dropped from the cache — peer removal —
// while the in-memory cursor survived) must refetch the full payload in the
// same round — a cursor without its base must never look freshly synced.
func TestMeshSyncMissingRowRefetches(t *testing.T) {
	f := newMeshSync304Fixture(t)

	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, ids := f.state(t); return ids["p-1"] })

	// Simulate the remove/re-add: the cached machine vanishes (view + rows,
	// the peer-removal path), cursor memory stays.
	if err := f.d.view.deleteMachine("etagpeer"); err != nil {
		t.Fatalf("delete peer machine: %v", err)
	}
	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, ids := f.state(t); return ids["p-1"] })
	if last := f.last(t); last.code != http.StatusOK || last.body.Delta {
		t.Fatalf("recovery fetch = code=%d delta=%v, want a trailing full 200", last.code, last.body.Delta)
	}
}

// TestShouldSyncAndCadence is the cadence decision truth table.
func TestShouldSyncAndCadence(t *testing.T) {
	d := &Daemon{}
	s := newMeshSync(d)

	// Zero value: never idle (a hand-built daemon behaves as before the knob).
	if !s.shouldSync(0) || s.cadence() != "always" {
		t.Errorf("idleInterval=0: shouldSync=%v cadence=%q, want always-sync", s.shouldSync(0), s.cadence())
	}

	s.idleInterval = 60 * time.Second
	// No demand ever: idle — sync only once sinceLast reaches the interval.
	if s.shouldSync(1 * time.Second) {
		t.Errorf("idle with 1s since last round: must not sync")
	}
	if !s.shouldSync(61 * time.Second) {
		t.Errorf("idle with 61s since last round: must sync")
	}
	if s.cadence() != "idle" {
		t.Errorf("cadence = %q, want idle", s.cadence())
	}

	// Fresh demand: full cadence.
	d.noteMeshDemand()
	if !s.shouldSync(1*time.Second) || s.cadence() != "active" {
		t.Errorf("in-demand: shouldSync=%v cadence=%q, want full cadence", s.shouldSync(1*time.Second), s.cadence())
	}

	// Hooks pin: full cadence regardless of demand.
	d.meshDemand.Store(0)
	s.hooksPinned = true
	if !s.shouldSync(1*time.Second) || s.cadence() != "hooks-pinned" {
		t.Errorf("hooks-pinned: shouldSync=%v cadence=%q", s.shouldSync(1*time.Second), s.cadence())
	}
}

// TestMeshSyncIdleAndKick drives the REAL run loop: with no demand the sync
// idles (only the boot round fires), a kick triggers an immediate round, and
// demand restores the base cadence.
func TestMeshSyncIdleAndKick(t *testing.T) {
	f := newMeshSyncFixture(t, time.Second)
	defer f.close()
	f.s.tickInterval = 20 * time.Millisecond
	f.s.idleInterval = time.Hour // idle = effectively never, absent demand/kick

	f.s.start()
	defer f.s.stopAndWait()

	// Boot round only.
	f.waitForID(t, "sync-1", time.Second)
	time.Sleep(300 * time.Millisecond) // ~15 base ticks
	if n := f.hits.Load(); n != 1 {
		t.Fatalf("idle sync still fetched: %d hits, want just the boot round", n)
	}

	// A kick (demand arriving while idle) fires an immediate round.
	f.s.kick()
	f.waitForID(t, "sync-2", time.Second)

	// Demand pins full cadence: several rounds arrive over the next ticks.
	f.s.d.noteMeshDemand()
	end := time.Now().Add(2 * time.Second)
	for f.hits.Load() < 5 && time.Now().Before(end) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := f.hits.Load(); n < 5 {
		t.Fatalf("in-demand sync did not resume the base cadence: %d hits", n)
	}
}

// TestDNSCheckRetry: a resolver that is not ready at boot must be retried (no
// loud failure once it recovers), and only exhausting every attempt yields the
// final failure verdict.
func TestDNSCheckRetry(t *testing.T) {
	peerList := []peers.Peer{{Machine: "p", SSH: "x", ApiAddr: "p:7878"}}

	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }

	attempts := 0
	recoverAfter := 2
	lookup := func(host string) error {
		attempts++
		if attempts <= recoverAfter {
			return errors.New("no such host (resolver not ready)")
		}
		return nil
	}
	if failed := runPeerDNSCheck(peerList, lookup, sleep); failed {
		t.Fatalf("check reported failure although the resolver recovered on attempt %d", recoverAfter+1)
	}
	if len(slept) != recoverAfter {
		t.Fatalf("slept %d times, want %d (one backoff per failed attempt)", len(slept), recoverAfter)
	}

	// Persistent failure: every backoff consumed, final verdict failed.
	slept = nil
	alwaysFail := func(host string) error { return errors.New("no such host") }
	if failed := runPeerDNSCheck(peerList, alwaysFail, sleep); !failed {
		t.Fatalf("persistently-broken resolver must yield the loud final failure")
	}
	if len(slept) != len(dnsCheckBackoff) {
		t.Fatalf("persistent failure slept %d times, want the full backoff ladder %d", len(slept), len(dnsCheckBackoff))
	}

	// A non-http peer has nothing to resolve: no lookups, no failure.
	called := false
	if failed := runPeerDNSCheck([]peers.Peer{{Machine: "sshpeer", SSH: "host"}}, func(string) error { called = true; return errors.New("x") }, sleep); failed || called {
		t.Fatalf("ssh-only peer triggered a DNS lookup (failed=%v called=%v)", failed, called)
	}
}

// waitUntilDaemon is a local poll helper (the conformance package has its own).
func waitUntilDaemon(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", d)
}
