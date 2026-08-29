package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/store"
)

// meshSyncFixture wires a meshSync against a real store, a real healthy HTTP peer
// (every /v1/snapshot hit returns a fresh, distinguishable payload) and a real
// "asleep" peer: a TCP listener that accepts and never responds — the exact failure
// mode of a powered-off tailscale host, where the dial/read hangs until the fetch
// timeout rather than being refused. Both peers carry a broken ssh dest so only the
// http transport can possibly serve them (per the .http-cell honesty convention).
type meshSyncFixture struct {
	st    *store.Store
	s     *meshSync
	hits  *atomic.Int64
	close func()
}

func newMeshSyncFixture(t *testing.T, fetchTimeout time.Duration) *meshSyncFixture {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	var hits atomic.Int64
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/snapshot" {
			http.NotFound(w, r)
			return
		}
		n := hits.Add(1)
		json.NewEncoder(w).Encode(api.MachineSnapshot{ //nolint:errcheck
			Machine: "healthy",
			Threads: []api.ThreadSnapshot{{Thread: api.Thread{ID: fmt.Sprintf("sync-%d", n), Machine: "healthy"}}},
		})
	}))

	hang, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hang listener: %v", err)
	}
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c) //nolint:errcheck — swallow the request, never respond
		}
	}()

	reg := peers.Registry{Peers: map[string]peers.Peer{
		"healthy": {Machine: "healthy", SSH: "http-only.invalid", Home: "/nonexistent", Binary: "sesh",
			ApiAddr: strings.TrimPrefix(healthy.URL, "http://"), ApiToken: "tok"},
		"dead": {Machine: "dead", SSH: "http-only.invalid", Home: "/nonexistent", Binary: "sesh",
			ApiAddr: hang.Addr().String(), ApiToken: "tok"},
	}}
	if err := reg.Save(filepath.Join(home, "peers.json")); err != nil {
		t.Fatalf("peers save: %v", err)
	}

	d := &Daemon{store: st, cfg: config.Config{Home: home, Machine: "self"}}
	d.view = newMeshView(st)
	s := newMeshSync(d)
	s.fetchTimeout = fetchTimeout

	return &meshSyncFixture{
		st:   st,
		s:    s,
		hits: &hits,
		close: func() {
			healthy.Close()
			hang.Close()
			st.Close()
		},
	}
}

// peerState reads one machine's cached state through the VIEW — the in-process
// authority every consumer reads (_dev/MESH_SCALE.md).
func (f *meshSyncFixture) peerState(t *testing.T, machine string) (api.MachineView, bool) {
	t.Helper()
	for _, mv := range f.s.d.view.machineViews() {
		if mv.Machine == machine {
			return mv, true
		}
	}
	return api.MachineView{}, false
}

func viewHasID(mv api.MachineView, id string) bool {
	for _, th := range mv.Threads {
		if th.ID == id {
			return true
		}
	}
	return false
}

// waitForID polls until the healthy peer's cached state carries the thread id
// (one sync landed), failing after deadline. The poll budget is the assertion:
// it is far SHORTER than the dead peer's fetch timeout, so a sync that only
// lands once the dead peer times out (the old wg.Wait barrier) fails loudly.
func (f *meshSyncFixture) waitForID(t *testing.T, marker string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if mv, ok := f.peerState(t, "healthy"); ok && viewHasID(mv, marker) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mv, _ := f.peerState(t, "healthy")
	t.Fatalf("healthy peer state never reached %q within %v (last: reachable=%v threads=%d)",
		marker, deadline, mv.Reachable, len(mv.Threads))
}

// TestMeshSyncDeadPeerDoesNotStallOthers is the regression test for the mesh-wide
// staleness bug: tick() used to fetch all peers concurrently but write ALL results
// only after wg.Wait(), so one asleep peer (fetch hanging until timeout) gated every
// other peer's cache update — with an 8s timeout the whole mesh view saw-toothed up
// to ~9s stale whenever any machine was down. Each fetch must write its own result
// as it completes, and a hanging peer must only skip its own ticks.
func TestMeshSyncDeadPeerDoesNotStallOthers(t *testing.T) {
	const fetchTimeout = 2 * time.Second
	f := newMeshSyncFixture(t, fetchTimeout)
	defer f.close()

	// Seed a previous successful sync of the dead peer, so the unreachable flip
	// (a no-op on a never-synced peer) is observable, and offline retention
	// (last-known threads survive) is asserted alongside.
	if err := f.s.d.view.replaceAll("dead", 1, []api.ThreadSnapshot{{Thread: api.Thread{ID: "last-known", Machine: "dead"}}}); err != nil {
		t.Fatalf("seed dead snapshot: %v", err)
	}

	// Tick 1: the dead peer's fetch starts hanging NOW. The healthy peer's result
	// must land while that hang is still in flight — well inside fetchTimeout.
	f.s.tick()
	f.waitForID(t, "sync-1", fetchTimeout/4)
	if mv, _ := f.peerState(t, "dead"); !mv.Reachable || !viewHasID(mv, "last-known") {
		t.Fatalf("dead peer flipped early (reachable=%v) — its fetch should still be in flight", mv.Reachable)
	}

	// Ticks 2 and 3: healthy keeps its cadence (a fresh payload lands per tick)
	// while the dead peer's single hanging fetch persists (in-flight guard: no
	// pile-up of hung fetches, and no stall of these writes).
	f.s.tick()
	f.waitForID(t, "sync-2", fetchTimeout/4)
	f.s.tick()
	f.waitForID(t, "sync-3", fetchTimeout/4)

	// Once fetchTimeout expires, the dead peer flips unreachable with its
	// last-known threads retained (offline browsing) — both directions proven.
	end := time.Now().Add(fetchTimeout + time.Second)
	for {
		mv, _ := f.peerState(t, "dead")
		if !mv.Reachable {
			if !viewHasID(mv, "last-known") {
				t.Fatalf("dead peer threads not retained: %d rows", len(mv.Threads))
			}
			break
		}
		if time.Now().After(end) {
			t.Fatalf("dead peer never marked unreachable after its fetch timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMeshSyncShutdownPrompt: stopAndWait must return promptly while a peer fetch
// is hanging (the cancel aborts it), and the aborted fetch must NOT mark the peer
// unreachable — shutdown says nothing about the peer's health.
func TestMeshSyncShutdownPrompt(t *testing.T) {
	f := newMeshSyncFixture(t, 30*time.Second) // far longer than the test: only cancel can end the hang
	defer f.close()

	if err := f.s.d.view.replaceAll("dead", 1, nil); err != nil {
		t.Fatalf("seed dead snapshot: %v", err)
	}

	f.s.start()
	f.waitForID(t, "sync-1", 2*time.Second) // the loop is running; dead's fetch is hanging

	done := make(chan struct{})
	go func() {
		f.s.stopAndWait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("stopAndWait did not return promptly with a hanging peer fetch in flight")
	}

	if mv, _ := f.peerState(t, "dead"); !mv.Reachable {
		t.Fatalf("shutdown-aborted fetch marked the peer unreachable — cancellation is not evidence of peer health")
	}
}

// TestMeshNudgeSyncsPeerNow drives the REAL POST /v1/mesh/nudge handler (schema
// 45) against the fixture's real store + real HTTP peer: a nudge alone — no
// tick, no kick, no run loop — must fetch the named peer's snapshot into the
// cache and bump mesh demand (so the cadence stays active over the follow-up
// window). Self / empty / unknown machine are loud 4xx, never silent no-ops.
func TestMeshNudgeSyncsPeerNow(t *testing.T) {
	f := newMeshSyncFixture(t, 2*time.Second)
	defer f.close()
	f.s.d.mesh = f.s // wire the daemon the way New() does, so the handler reaches the syncer

	nudge := func(machine string) *httptest.ResponseRecorder {
		body, err := json.Marshal(api.MeshNudgeRequest{Machine: machine})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rec := httptest.NewRecorder()
		f.s.d.handleMeshNudge(rec, httptest.NewRequest(http.MethodPost, "/v1/mesh/nudge", strings.NewReader(string(body))))
		return rec
	}

	if d := f.s.d.lastMeshDemand(); !d.IsZero() {
		t.Fatalf("pre-nudge demand should be zero, got %v", d)
	}
	if rec := nudge("healthy"); rec.Code != http.StatusOK {
		t.Fatalf("nudge(healthy) = %d: %s", rec.Code, rec.Body.String())
	}
	// The nudge ALONE lands the peer's payload — the deliberately-absent run
	// loop proves no tick was involved.
	f.waitForID(t, "sync-1", time.Second)
	if f.s.d.lastMeshDemand().IsZero() {
		t.Fatalf("a nudge must count as mesh demand (active cadence covers the in-flight race)")
	}

	// A second nudge fires a second immediate round.
	if rec := nudge("healthy"); rec.Code != http.StatusOK {
		t.Fatalf("second nudge = %d: %s", rec.Code, rec.Body.String())
	}
	f.waitForID(t, "sync-2", time.Second)

	// Loud validation: a typo'd machine, self, and an empty machine never no-op.
	if rec := nudge("nosuchmachine"); rec.Code != http.StatusNotFound {
		t.Fatalf("nudge(unknown) = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if rec := nudge("self"); rec.Code != http.StatusBadRequest {
		t.Fatalf("nudge(self) = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if rec := nudge(""); rec.Code != http.StatusBadRequest {
		t.Fatalf("nudge(empty) = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	f.s.wg.Wait() // let the nudge-launched fetch goroutines finish before the store closes
}
