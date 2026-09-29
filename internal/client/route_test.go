package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// serveUnix runs h on a real unix socket (the transport NewRouted dials) for the test.
func serveUnix(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sesh-rt-") // short: sun_path is 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return sock
}

// A local daemon that PREDATES schema 51 has no /v1/route: its mux answers the plain
// 404 net/http gives any unknown path. The routed client must refuse loudly, naming
// the fix — never pass it through as the peer's "not found", never dial the peer.
func TestRoutedClientPre51DaemonIsLoud(t *testing.T) {
	sock := serveUnix(t, http.NewServeMux()) // no routes at all: every path is a bare 404
	_, err := NewRouted(sock, "peerx").Status(context.Background())
	if err == nil {
		t.Fatal("routed call to a daemon without /v1/route succeeded")
	}
	for _, want := range []string{"predates schema", "restart it through its service manager", "peerx"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// The PEER's own 404 (e.g. an unknown thread on the peer) arrives with the routed-by
// header and must pass through as an ordinary daemon error, not be mistaken for a
// pre-51 local daemon.
func TestRoutedClientPassesPeer404Through(t *testing.T) {
	var gotPath string
	sock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set(api.RoutedByHeader, "peerx")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(api.ErrorResponse{Schema: api.SchemaVersion, Error: "thread not found"}) //nolint:errcheck
	}))
	_, _, _, err := NewRouted(sock, "peerx").TmuxMasterCurrent(context.Background(), "origin", "")
	if err == nil || !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("peer 404 not passed through: err = %v", err)
	}
	if strings.Contains(err.Error(), "predates") {
		t.Errorf("peer 404 misread as a pre-51 daemon: %v", err)
	}
	if want := "/v1/route/peerx/v1/tmux/master-current"; gotPath != want {
		t.Errorf("request path = %q, want %q (the route prefix + the ordinary API path)", gotPath, want)
	}
}

// The route handler's OWN refusal carries RouteRefusedHeader; the client reports its
// message even through a method (Status) that otherwise reports only a status code.
func TestRoutedClientReportsRefusalMessage(t *testing.T) {
	sock := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(api.RoutedByHeader, "peerx")
		w.Header().Set(api.RouteRefusedHeader, "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(api.ErrorResponse{Schema: api.SchemaVersion, Error: `machine "peerx" is offline per the local mesh cache`}) //nolint:errcheck
	}))
	_, err := NewRouted(sock, "peerx").Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "offline per the local mesh cache") {
		t.Fatalf("refusal message lost: err = %v", err)
	}
}

// With the local daemon DOWN a routed call fails naming the local daemon — the
// dependency is new (the direct dial never needed it), so it must be said plainly.
func TestRoutedClientLocalDaemonDown(t *testing.T) {
	_, err := NewRouted("/tmp/sesh-rt-nonexistent.sock", "peerx").Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "goes through the LOCAL sesh daemon") {
		t.Fatalf("err = %v, want it to name the local daemon", err)
	}
}
