package conformance

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("api.http-json", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testAPIHTTPJSON(t, loc) })
	}
	// daemon.mesh-read: a cross-machine read via the peer mesh (Remote only).
	matrix.RegisterTest("daemon.mesh-read", matrix.AgentAgnostic, matrix.Remote, testDaemonMeshRead)
}

// testAPIHTTPJSON asserts the daemon's client-facing surface is genuinely
// HTTP+JSON (for the CLI/TUI/Obsidian plugin) — exercised with a RAW HTTP client,
// not sesh's own client wrapper. Local hits the local daemon's socket directly;
// Remote reaches the PEER daemon's HTTP surface over a real ssh hop + curl.
func testAPIHTTPJSON(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	sockPath := filepath.Join(sb.Home, "daemon.sock")

	var body []byte
	switch loc {
	case matrix.Local:
		body = rawUnixGet(t, sockPath, "/v1/status")
	case matrix.Remote:
		// Real remote hop to the remote daemon's HTTP+JSON surface.
		out, stderr, err := runSSH(t, "curl", "-s", "--unix-socket", sockPath, "http://unix/v1/status")
		if err != nil {
			t.Fatalf("ssh+curl peer api: %v\n%s", err, stderr)
		}
		body = []byte(out)
	}

	var st api.StatusResponse
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, body)
	}
	if st.Schema != api.SchemaVersion {
		t.Errorf("schema = %d, want %d", st.Schema, api.SchemaVersion)
	}
	if st.Machine != sb.Machine {
		t.Errorf("machine = %q, want %q", st.Machine, sb.Machine)
	}
}

// testDaemonMeshRead asserts a cross-machine read: a local client reads the PEER
// machine's live tmux state via `--machine` routing (the mesh), and gets the
// PEER's data — not the local machine's. This is the read side of the path the
// v1 `--machine X` bug broke.
func testDaemonMeshRead(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	peer := newSandbox(t, matrix.Remote)
	peer.startDaemon(t)

	// Create a session that exists ONLY on the peer.
	if out, err := peer.rawTmux(t, "new-session", "-d", "-s", "onlyonpeer"); err != nil {
		t.Fatalf("new-session on peer: %v\n%s", err, out)
	}

	// Read it back through the mesh from the local client (peer.Runner routes
	// `--machine peer`).
	stdout, stderr, err := peer.Runner.Run(t, "tmux", "info")
	if err != nil {
		t.Fatalf("mesh read (tmux info --machine peer): %v\n%s", err, stderr)
	}
	sessions := parseJSONL(t, stdout)
	var found bool
	for _, s := range sessions {
		if s.Name == "onlyonpeer" {
			found = true
			if s.Machine != peer.Machine {
				t.Errorf("mesh-read session machine = %q, want peer %q", s.Machine, peer.Machine)
			}
		}
	}
	if !found {
		t.Fatalf("mesh read did not return the peer-only session; got %v", sessionNames(sessions))
	}
}

// rawUnixGet does a raw HTTP GET over a unix socket and returns the body, failing
// the test on any transport error or non-JSON content type.
func rawUnixGet(t *testing.T, socketPath, path string) []byte {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	resp, err := client.Get("http://unix" + path)
	if err != nil {
		t.Fatalf("raw GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", path, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}
