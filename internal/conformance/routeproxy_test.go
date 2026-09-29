package conformance

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

// TestRouteProxy exercises the local daemon's /v1/route proxy (schema 51, issue #12)
// end to end with REAL daemons: a client daemon A, an http peer B (a real daemon with
// its TCP API behind a token), an ssh-only peer C, and an http peer D whose address
// has nothing listening. Nothing is mocked — every request crosses A's real unix
// socket and, when it is forwarded, B's real token-guarded TCP API.
func TestRouteProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bin := seshBin(t)
	aAddr, aToken := freePort(t), "routeproxy-a"
	a := newSandbox(t, matrix.Local, withAPI(aAddr, aToken))
	a.startDaemon(t)
	bAddr, bToken := freePort(t), "routeproxy-b"
	b := newSandbox(t, matrix.Local, withAPI(bAddr, bToken))
	b.startDaemon(t)
	deadAddr := freePort(t) // nothing listens here

	add := func(args ...string) {
		t.Helper()
		if _, stderr, err := a.Runner.Run(t, append([]string{"peer", "add"}, args...)...); err != nil {
			t.Fatalf("peer add %v: %v\n%s", args, err, stderr)
		}
	}
	add("--machine", b.Machine, "--ssh", "http-only.invalid", "--home", b.Home, "--binary", bin,
		"--tmux-socket", b.TmuxSocket, "--api-addr", bAddr, "--api-token", bToken)
	add("--machine", "sshonly", "--ssh", "localhost", "--home", "/nonexistent", "--binary", bin, "--tmux-socket", "none")
	add("--machine", "deadhttp", "--ssh", "http-only.invalid", "--home", "/nonexistent", "--binary", bin,
		"--tmux-socket", "none", "--api-addr", deadAddr, "--api-token", "x")

	ctx := context.Background()
	sock := a.Home + "/daemon.sock"

	// 1. FORWARDING + TOKEN INJECTION: B's API rejects an unauthenticated request, so a
	// Status answered with B's own machine name proves A forwarded it AND authenticated.
	st, err := client.NewRouted(sock, b.Machine).Status(ctx)
	if err != nil {
		t.Fatalf("routed Status to %s: %v", b.Machine, err)
	}
	if st.Machine != b.Machine {
		t.Fatalf("routed Status answered by %q, want the peer %q", st.Machine, b.Machine)
	}

	// 2. REFUSALS — each loud, each stamped with the routed-by header (so the client
	// never mistakes one for "the local daemon has no /v1/route").
	unix := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	get := func(c *http.Client, url, token string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get(api.RoutedByHeader), string(body)
	}
	for _, tc := range []struct {
		name, path string
		code       int
		want       string
	}{
		{"self", "/v1/route/" + a.Machine + "/v1/health", 400, "is this machine"},
		{"unknown", "/v1/route/nosuchmachine/v1/health", 404, "no peer registered"},
		{"ssh peer", "/v1/route/sshonly/v1/health", 400, "only http peers are routed"},
		{"nested", "/v1/route/" + b.Machine + "/v1/route/" + b.Machine + "/v1/health", 400, "nested routing is refused"},
		{"not an api path", "/v1/route/" + b.Machine + "/health", 400, "not an API path"},
	} {
		code, hdr, body := get(unix, "http://unix"+tc.path, "")
		if code != tc.code || !strings.Contains(body, tc.want) {
			t.Errorf("%s: got %d %q, want %d containing %q", tc.name, code, body, tc.code, tc.want)
		}
		if hdr == "" {
			t.Errorf("%s: refusal lacks the %s header — the client would read it as a pre-51 daemon", tc.name, api.RoutedByHeader)
		}
	}

	// 3. UNIX SOCKET ONLY: A's TOKEN-authenticated TCP API must NOT relay — a token
	// holder must not be able to reach B through A.
	code, hdr, _ := get(&http.Client{Timeout: 10 * time.Second}, "http://"+aAddr+"/v1/route/"+b.Machine+"/v1/status", aToken)
	if code != http.StatusNotFound || hdr != "" {
		t.Errorf("A's TCP API served /v1/route (status %d, routed-by %q) — the proxy must be unix-socket only", code, hdr)
	}

	// 4. A NEVER-SYNCED peer is still ATTEMPTED (absent from the cache is not "known
	// offline" — the old CLI gate's conservative rule), so nothing listening there is a
	// loud 502 naming the peer, not a refusal and not a hang.
	code, hdr, body := get(unix, "http://unix/v1/route/deadhttp/v1/health", "")
	if code != http.StatusBadGateway || !strings.Contains(body, "route to deadhttp") || hdr == "" {
		t.Errorf("never-synced dead peer: got %d %q (routed-by %q), want a 502 naming the peer", code, body, hdr)
	}

	// 5. KNOWN-OFFLINE GATE, answered from A's memory: a peer A has SEEN reachable and
	// then lost is refused instantly and loudly (the message the CLI's old whole-mesh
	// pre-check gave).
	eAddr, eToken := freePort(t), "routeproxy-e"
	e := newSandbox(t, matrix.Local, withAPI(eAddr, eToken))
	e.startDaemon(t)
	add("--machine", e.Machine, "--ssh", "http-only.invalid", "--home", e.Home, "--binary", bin,
		"--tmux-socket", e.TmuxSocket, "--api-addr", eAddr, "--api-token", eToken)
	local := client.New(sock)
	reachable := func(machine string) (seen, up bool) {
		m, err := local.Mesh(ctx)
		if err != nil {
			return false, false
		}
		for _, mv := range m.Machines {
			if mv.Machine == machine {
				return true, mv.Reachable
			}
		}
		return false, false
	}
	if !waitUntil(30*time.Second, func() bool { seen, up := reachable(e.Machine); return seen && up }) {
		t.Fatalf("A's mesh never saw %s reachable", e.Machine)
	}
	if _, stderr, err := e.Runner.Run(t, "daemon", "stop"); err != nil {
		t.Fatalf("stop peer E: %v\n%s", err, stderr)
	}
	if !waitUntil(30*time.Second, func() bool { seen, up := reachable(e.Machine); return seen && !up }) {
		t.Fatalf("A's mesh never marked %s unreachable after its daemon stopped", e.Machine)
	}
	code, hdr, body = get(unix, "http://unix/v1/route/"+e.Machine+"/v1/health", "")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "offline per the local mesh cache") || hdr == "" {
		t.Errorf("known-offline peer: got %d %q (routed-by %q), want 503 'offline per the local mesh cache'", code, body, hdr)
	}
	if _, err := client.NewRouted(sock, e.Machine).Status(ctx); err == nil || !strings.Contains(err.Error(), "offline per the local mesh cache") {
		t.Errorf("routed client to a known-offline peer: err = %v, want the offline refusal", err)
	}
}
