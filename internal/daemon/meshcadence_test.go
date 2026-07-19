package daemon

// Tests for the issue-#1 mesh data rationing: the peer-facing snapshot (slim +
// ETag/304 conditional fetch) and the demand-driven sync cadence. The protocol
// tests drive the REAL handleSnapshot and the REAL client — no re-implementation
// of either side — so a drift between server ETag semantics and client
// conditional semantics fails here.

import (
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
// snapshots (published state only — no tmux/store needed to serve /v1/snapshot).
func seedMaintDaemon(t *testing.T, snaps ...api.ThreadSnapshot) *Daemon {
	t.Helper()
	d := &Daemon{}
	d.maint = newMaintainer(d)
	for _, sn := range snaps {
		d.maint.st[sn.ID] = &liveState{snap: sn}
	}
	return d
}

func snap(id string, archived bool, head api.Head) api.ThreadSnapshot {
	return api.ThreadSnapshot{Thread: api.Thread{ID: id, Machine: "m", Archived: archived}, Head: head}
}

// TestSnapshotSlimAndConditional: the real /v1/snapshot handler must (a) slim the
// peer-facing payload — archived+dead threads dropped, archived-but-HEADFUL kept
// (the H40 contract) — and (b) answer a matching If-None-Match with a bodyless
// 304, then a NEW payload once the state changes. Driven end-to-end through the
// real client.SnapshotConditional.
func TestSnapshotSlimAndConditional(t *testing.T) {
	d := seedMaintDaemon(t,
		snap("live-1", false, api.Headful),
		snap("arch-live", true, api.Headful),
		snap("arch-dead", true, api.Headless),
	)
	srv := httptest.NewServer(http.HandlerFunc(d.handleSnapshot))
	defer srv.Close()
	c := client.NewRemote(strings.TrimPrefix(srv.URL, "http://"), "")

	ms, etag, notMod, err := c.SnapshotConditional(t.Context(), "")
	if err != nil || notMod {
		t.Fatalf("first fetch: err=%v notMod=%v", err, notMod)
	}
	if etag == "" {
		t.Fatalf("first fetch carried no ETag")
	}
	ids := map[string]bool{}
	for _, th := range ms.Threads {
		ids[th.ID] = true
	}
	if !ids["live-1"] || !ids["arch-live"] {
		t.Errorf("peer-facing snapshot missing live rows: %v (archived+HEADFUL must stay — H40)", ids)
	}
	if ids["arch-dead"] {
		t.Errorf("peer-facing snapshot still carries the archived+headless thread")
	}

	// Unchanged state + the ETag => 304, no payload.
	_, etag2, notMod, err := c.SnapshotConditional(t.Context(), etag)
	if err != nil || !notMod || etag2 != etag {
		t.Fatalf("conditional refetch: err=%v notMod=%v etag2=%q (want 304 with the same ETag)", err, notMod, etag2)
	}

	// A state change => full 200 with a NEW ETag.
	d.maint.st["live-2"] = &liveState{snap: snap("live-2", false, api.Headful)}
	ms3, etag3, notMod, err := c.SnapshotConditional(t.Context(), etag)
	if err != nil || notMod {
		t.Fatalf("post-change fetch: err=%v notMod=%v (a changed snapshot must be a full 200)", err, notMod)
	}
	if etag3 == etag {
		t.Errorf("ETag did not change with the payload")
	}
	found := false
	for _, th := range ms3.Threads {
		if th.ID == "live-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("post-change payload missing the new thread")
	}

	// A dead-archived-only change is INVISIBLE to peers: archiving the payload's
	// dead weight must not churn the ETag (that churn is the 124 KB-per-tick bug).
	d.maint.st["arch-dead-2"] = &liveState{snap: snap("arch-dead-2", true, api.Headless)}
	_, etag4, notMod, err := c.SnapshotConditional(t.Context(), etag3)
	if err != nil || !notMod || etag4 != etag3 {
		t.Errorf("adding an archived+dead thread changed the peer-facing payload: notMod=%v etag %q -> %q", notMod, etag3, etag4)
	}
}

// meshSync304Fixture: a meshSync against a REAL store and a peer served by the
// REAL handleSnapshot (real ETag semantics), so the syncer's conditional loop is
// proven against the actual server implementation.
type meshSync304Fixture struct {
	d      *Daemon // the syncing daemon
	peer   *Daemon // the peer daemon behind handleSnapshot
	s      *meshSync
	status *[]int // per-request response codes observed at the peer
	mu     *sync.Mutex
}

func newMeshSync304Fixture(t *testing.T) *meshSync304Fixture {
	t.Helper()
	f := newMeshSyncFixture(t, 2*time.Second) // reuse the real-store fixture scaffolding
	t.Cleanup(f.close)

	peerD := seedMaintDaemon(t, snap("p-1", false, api.Headful))
	var mu sync.Mutex
	var codes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		peerD.handleSnapshot(rec, r)
		mu.Lock()
		codes = append(codes, rec.Code)
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
	return &meshSync304Fixture{d: f.s.d, peer: peerD, s: f.s, status: &codes, mu: &mu}
}

func (f *meshSync304Fixture) codes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), *f.status...)
}

func (f *meshSync304Fixture) row(t *testing.T) (syncedAt int64, payload string) {
	t.Helper()
	rows, err := f.d.store.LoadPeerSnapshots()
	if err != nil {
		t.Fatalf("LoadPeerSnapshots: %v", err)
	}
	for _, r := range rows {
		if r.Machine == "etagpeer" {
			return r.SyncedAtUnix, r.Payload
		}
	}
	return 0, ""
}

// TestMeshSyncConditionalFetch: round 1 lands the full payload (200); round 2 is
// answered 304 by the real server and must refresh freshness WITHOUT rewriting
// the payload; a peer-side change makes round 3 a full 200 with the new thread.
func TestMeshSyncConditionalFetch(t *testing.T) {
	f := newMeshSync304Fixture(t)

	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, p := f.row(t); return strings.Contains(p, "p-1") })
	if codes := f.codes(); len(codes) == 0 || codes[len(codes)-1] != http.StatusOK {
		t.Fatalf("first sync codes = %v, want a 200", codes)
	}
	syncedAt1, payload1 := f.row(t)

	// Force a visibly-later clock second so the touch is observable.
	time.Sleep(1100 * time.Millisecond)
	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { sa, _ := f.row(t); return sa > syncedAt1 })
	if _, payload2 := f.row(t); payload2 != payload1 {
		t.Fatalf("payload rewritten on an unchanged snapshot (want the 304/touch path)")
	}
	if codes := f.codes(); codes[len(codes)-1] != http.StatusNotModified {
		t.Fatalf("second sync codes = %v, want a trailing 304", codes)
	}

	// Peer state changes => full 200, new payload.
	f.peer.maint.st["p-2"] = &liveState{snap: snap("p-2", false, api.Headful)}
	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, p := f.row(t); return strings.Contains(p, "p-2") })
	if codes := f.codes(); codes[len(codes)-1] != http.StatusOK {
		t.Fatalf("post-change sync codes = %v, want a trailing 200", codes)
	}
}

// TestMeshSync304MissingRowRefetches: a 304 whose cache row is GONE (peer removed
// + re-added while the in-memory ETag survived) must drop the ETag and refetch
// the full payload in the same round — never leave the peer payload-less while
// looking freshly synced.
func TestMeshSync304MissingRowRefetches(t *testing.T) {
	f := newMeshSync304Fixture(t)

	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, p := f.row(t); return strings.Contains(p, "p-1") })

	// Simulate the remove/re-add: the cache row vanishes, the ETag memory stays.
	if err := f.d.store.DeletePeerSnapshot("etagpeer"); err != nil {
		t.Fatalf("delete peer snapshot: %v", err)
	}
	f.s.tick()
	waitUntilDaemon(t, 2*time.Second, func() bool { _, p := f.row(t); return strings.Contains(p, "p-1") })
	if codes := f.codes(); codes[len(codes)-1] != http.StatusOK {
		t.Fatalf("recovery fetch codes = %v, want a trailing full 200", codes)
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
	f.waitForPayload(t, "sync-1", time.Second)
	time.Sleep(300 * time.Millisecond) // ~15 base ticks
	if n := f.hits.Load(); n != 1 {
		t.Fatalf("idle sync still fetched: %d hits, want just the boot round", n)
	}

	// A kick (demand arriving while idle) fires an immediate round.
	f.s.kick()
	f.waitForPayload(t, "sync-2", time.Second)

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
