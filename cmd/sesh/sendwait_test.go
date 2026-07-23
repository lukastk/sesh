package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

const swTID = "11111111-1111-1111-1111-111111111111"

// scriptedDaemon serves the minimal endpoints threadSend --wait touches, with
// a controllable wait behavior. This is a UNIT test of the CLI's wait/stall
// composition (a scripted daemon is legitimate here — outside the matrix);
// the real-agent proof of the wait mechanism is the thread.send-wait cells,
// which cannot honestly stage a wedged pane (tmux SIGCONTs stopped children).
func scriptedDaemon(t *testing.T, wait func(until string) api.ThreadWaitResponse) (config.Config, *int32) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "swcli.")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	var sends int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/threads", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.ThreadListResponse{Schema: api.SchemaVersion, //nolint:errcheck
			Threads: []api.Thread{{ID: swTID, Machine: "m", SessionName: "s", AgentKind: "pi"}}})
	})
	mux.HandleFunc("POST /v1/threads/send", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sends, 1)
		json.NewEncoder(w).Encode(map[string]any{"schema": api.SchemaVersion}) //nolint:errcheck
	})
	mux.HandleFunc("GET /v1/threads/wait", func(w http.ResponseWriter, r *http.Request) {
		resp := wait(r.URL.Query().Get("until"))
		resp.Schema, resp.ID = api.SchemaVersion, swTID
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})

	ln, err := net.Listen("unix", filepath.Join(home, "daemon.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return config.Config{Home: home}, &sends
}

// TestSendWaitStallGuard: a send whose delivery produces NO observable change
// (never busy, LastActiveUnix frozen) must fail loudly in ~5s — not sleep out
// the full --timeout.
func TestSendWaitStallGuard(t *testing.T) {
	cfg, sends := scriptedDaemon(t, func(until string) api.ThreadWaitResponse {
		// Wedged: never reached, activity frozen at the pre-send value.
		return api.ThreadWaitResponse{Reached: false, Busy: api.BusyIdle, LastActiveUnix: 1000}
	})
	start := time.Now()
	err := threadSend(cfg, []string{"--id", swTID, "--text", "x", "--wait", "--timeout", "60s"})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "no state change within 5s") {
		t.Fatalf("want stall error, got %v", err)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("stall took %s — must fail fast, not wait out the 60s timeout", elapsed)
	}
	if atomic.LoadInt32(sends) != 1 {
		t.Fatalf("sends = %d, want 1", *sends)
	}
}

// TestSendWaitSettles: pane activity after delivery (LastActiveUnix advancing)
// defeats the stall guard even if busy is never observed (a turn can flash
// completely inside one poll gap), and the settled wait then releases.
func TestSendWaitSettles(t *testing.T) {
	var sawSend atomic.Bool
	cfg, sends := scriptedDaemon(t, func(until string) api.ThreadWaitResponse {
		if until == "settled" {
			return api.ThreadWaitResponse{Reached: true, Busy: api.BusyIdle, LastActiveUnix: 2000}
		}
		// The busy probes: pre-send shows la=1000; post-send shows progress.
		if sawSend.Load() {
			return api.ThreadWaitResponse{Reached: false, Busy: api.BusyIdle, LastActiveUnix: 2000}
		}
		sawSend.Store(true) // first call = the pre-send read
		return api.ThreadWaitResponse{Reached: false, Busy: api.BusyIdle, LastActiveUnix: 1000}
	})
	if err := threadSend(cfg, []string{"--id", swTID, "--text", "x", "--wait", "--timeout", "30s"}); err != nil {
		t.Fatalf("send --wait: %v", err)
	}
	if atomic.LoadInt32(sends) != 1 {
		t.Fatalf("sends = %d, want 1", *sends)
	}
}

// TestWaitTimeoutLoud: thread wait --until with an expired deadline errors,
// naming the target and last state.
func TestWaitTimeoutLoud(t *testing.T) {
	cfg, _ := scriptedDaemon(t, func(until string) api.ThreadWaitResponse {
		return api.ThreadWaitResponse{Reached: false, Busy: api.BusyIdle}
	})
	err := threadWait(cfg, []string{"--id", swTID, "--until", "busy", "--timeout", "1s"})
	if err == nil || !strings.Contains(err.Error(), "did not reach busy") || !strings.Contains(err.Error(), "idle") {
		t.Fatalf("want loud timeout naming target+state, got %v", err)
	}
}
