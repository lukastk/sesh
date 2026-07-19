package conformance

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("mesh.delta-sync.http", matrix.AgentAgnostic, matrix.Remote, testMeshDeltaSync)
}

// wireExchange is one proxied snapshot request/response, as observed at the wire.
type wireExchange struct {
	at        time.Time
	respBytes int
}

// countingProxy forwards requests to the peer daemon's REAL TCP API and records
// each response's byte size. It is a measurement tap (like tcpdump), not a mock:
// both ends are real daemons speaking their real protocol through it.
func countingProxy(t *testing.T, target string, mu *sync.Mutex, log *[]wireExchange) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.RequestURI(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		mu.Lock()
		*log = append(*log, wireExchange{at: time.Now(), respBytes: len(body)})
		mu.Unlock()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(body) //nolint:errcheck
	})
}

// testMeshDeltaSync proves the schema-41 delta sync at the WIRE between two real
// daemons: after the cache converges on a ~two-dozen-thread peer, steady-state
// sync rounds transfer near-empty deltas (not the full payload), a single
// changed row transfers ~one row — and the replicated set stays COMPLETE the
// whole time (this optimization exists precisely because hiding rows was
// rejected; freshness keeps advancing via the touch path).
func testMeshDeltaSync(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	localSB := newSandbox(t, matrix.Local)
	localSB.startDaemon(t)

	addr := freePort(t)
	token := fmt.Sprintf("delta-token-%d", time.Now().UnixNano())
	peer := newSandbox(t, matrix.Local, withAPI(addr, token))
	peer.startDaemon(t)

	var mu sync.Mutex
	var wire []wireExchange
	proxy := httptest.NewServer(countingProxy(t, "http://"+addr, &mu, &wire))
	t.Cleanup(proxy.Close)

	// The peer is registered THROUGH the proxy, with a broken ssh dest — every
	// sync byte must cross the counted http path (the .http-cell honesty rule).
	bin := seshBin(t)
	add := []string{"peer", "add", "--machine", peer.Machine, "--ssh", "http-only.invalid", "--home", peer.Home,
		"--binary", bin, "--tmux-socket", peer.TmuxSocket,
		"--api-addr", strings.TrimPrefix(proxy.URL, "http://"), "--api-token", token}
	if _, stderr, err := localSB.Runner.Run(t, add...); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}

	// Seed a meaningfully-sized peer: two dozen virtual thread records (pure
	// grouping nodes — real records in the real store, no agent needed).
	const bulk = 24
	for i := 0; i < bulk; i++ {
		if _, stderr, err := peer.daemonRunner.Run(t, "thread", "new", "--virtual", "--name", fmt.Sprintf("bulk-%02d", i)); err != nil {
			t.Fatalf("seed virtual thread %d: %v\n%s", i, err, stderr)
		}
	}

	// Reference size of the peer's CURRENT full snapshot, measured directly
	// (bypassing the proxy so it doesn't pollute the wire log). The maintainer
	// publishes on its ~300ms tick, so wait for all seeds to be in the snapshot.
	direct := client.NewRemote(addr, token)
	snapNow, _, _, err := direct.SnapshotConditional(t.Context(), "", "")
	if err != nil {
		t.Fatalf("direct full snapshot: %v", err)
	}
	if !waitUntil(10*time.Second, func() bool {
		snapNow, _, _, err = direct.SnapshotConditional(t.Context(), "", "")
		return err == nil && len(snapNow.Threads) >= bulk
	}) {
		t.Fatalf("peer snapshot never published all %d seeds (has %d)", bulk, len(snapNow.Threads))
	}
	fullBytes := 0
	{
		resp, err := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/snapshot", nil)
		if err != nil {
			t.Fatalf("build direct request: %v", err)
		}
		resp.Header.Set("Authorization", "Bearer "+token)
		rr, err := http.DefaultClient.Do(resp)
		if err != nil {
			t.Fatalf("direct full snapshot fetch: %v", err)
		}
		body, _ := io.ReadAll(rr.Body)
		rr.Body.Close()
		fullBytes = len(body)
	}
	if len(snapNow.Threads) < bulk || fullBytes < 5000 {
		t.Fatalf("peer seeding too small to discriminate: threads=%d fullBytes=%d", len(snapNow.Threads), fullBytes)
	}

	// Converge: the local cache must hold the peer's COMPLETE set. The Mesh
	// polling here is also the demand signal that keeps the sync at full cadence.
	c := client.New(localSB.Home + "/daemon.sock")
	if !waitUntil(20*time.Second, func() bool {
		mv, ok := peerView(t, c, peer.Machine)
		return ok && mv.Reachable && len(mv.Threads) >= bulk
	}) {
		mv, _ := peerView(t, c, peer.Machine)
		t.Fatalf("cache never converged: %d/%d threads", len(mv.Threads), bulk)
	}

	// STEADY STATE: nothing changes on the peer — the next sync rounds must be
	// near-empty deltas, and freshness must keep advancing (the touch path).
	mu.Lock()
	seen := len(wire)
	mu.Unlock()
	mv1, _ := peerView(t, c, peer.Machine)
	if !waitUntil(15*time.Second, func() bool {
		mu.Lock()
		n := len(wire)
		mu.Unlock()
		mv, ok := peerView(t, c, peer.Machine)
		return n >= seen+3 && ok && mv.SyncedAtUnix > mv1.SyncedAtUnix
	}) {
		t.Fatalf("steady-state sync rounds never accumulated / freshness stalled")
	}
	mu.Lock()
	steady := append([]wireExchange(nil), wire[seen:]...)
	mu.Unlock()
	for i, ex := range steady {
		if ex.respBytes >= 1000 || ex.respBytes >= fullBytes/10 {
			t.Fatalf("steady-state round %d transferred %d bytes (full=%d) — must be a near-empty delta, not a re-download", i, ex.respBytes, fullBytes)
		}
	}

	// ONE ROW CHANGES: the wire must carry ~one row, and the replicated set must
	// still be COMPLETE with the change applied (renamed row present, count intact).
	mu.Lock()
	beforeChange := len(wire)
	mu.Unlock()
	if _, stderr, err := peer.daemonRunner.Run(t, "thread", "rename", "--id", snapNow.Threads[0].ID, "--name", "bulk-renamed"); err != nil {
		t.Fatalf("rename on peer: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool {
		mv, ok := peerView(t, c, peer.Machine)
		if !ok || len(mv.Threads) < bulk {
			return false
		}
		for _, th := range mv.Threads {
			if th.Name == "bulk-renamed" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("renamed row never reached the local cache with the set intact")
	}
	mu.Lock()
	changeRounds := append([]wireExchange(nil), wire[beforeChange:]...)
	mu.Unlock()
	max := 0
	for _, ex := range changeRounds {
		if ex.respBytes > max {
			max = ex.respBytes
		}
	}
	if max >= fullBytes/4 {
		t.Fatalf("the change crossed the wire as %d bytes (full=%d) — a one-row change must transfer ~one row, not the machine", max, fullBytes)
	}
}
