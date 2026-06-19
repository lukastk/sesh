package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/coder/websocket"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesRPC(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/threads/rpc", d.handleThreadRPC)
}

// piRPCSocketPath resolves the unix socket a pi thread's `rpc-socket` extension
// opened for its session. The extension creates it under the first of $TMPDIR,
// /tmp, /var/tmp whose path fits the sun_path limit; we look in the same order and
// return the first that actually exists as a socket. Empty sessionID (codex before
// its first turn, or a thread that never started) resolves to nothing — loudly.
func piRPCSocketPath(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	var dirs []string
	if t := os.Getenv("TMPDIR"); t != "" {
		dirs = append(dirs, t)
	}
	dirs = append(dirs, "/tmp", "/var/tmp")
	for _, dir := range dirs {
		p := filepath.Join(dir, "pi-rpc-sockets", sessionID+".sock")
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return p, true
		}
	}
	return "", false
}

// handleThreadRPC bridges a pi thread's live `rpc-socket` to a WebSocket: the client
// gets the structured, token-streaming pi protocol (subscribe -> text_delta/tool/
// agent_end; {"message":…} injects a steer into the LIVE conversation), so a GUI can
// render a streaming chat for a pi thread — headful OR headless — over the daemon API,
// cross-machine, with no tmux attach. This is the daemon-native version of the relay
// prototyped in _dev/experiments/05_rpc_relay.
//
// pi-only by construction (claude/codex expose no equivalent live interface); a non-pi
// thread, an unknown id, or a thread with no live socket is a LOUD HTTP error before any
// upgrade — never a silent empty stream. On the TCP API this route inherits the daemon's
// bearer-token auth (the wrapping handler); the unix socket is local-trust as everywhere.
func (d *Daemon) handleThreadRPC(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "rpc: id is required")
		return
	}
	th, err := d.store.GetThread(id)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if th.AgentKind != string(agents.Pi) {
		writeError(w, http.StatusBadRequest, "rpc: only pi threads expose an rpc socket (this thread is "+th.AgentKind+")")
		return
	}
	sockPath, ok := piRPCSocketPath(th.AgentSessionID)
	if !ok {
		writeError(w, http.StatusConflict, "rpc: no live pi rpc socket for thread "+id+" (not running, or the rpc-socket extension is not loaded)")
		return
	}

	// Auth (on TCP) is already enforced by the wrapping tokenAuth handler. Origin is
	// NOT a trust signal here — the bearer token is — so skip the origin check (a browser
	// connecting from the app's origin would otherwise be rejected on host mismatch).
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return // Accept already wrote the failure response.
	}
	defer c.CloseNow() //nolint:errcheck — best-effort
	c.SetReadLimit(1 << 20)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		c.Close(websocket.StatusInternalError, "pi rpc socket dial failed") //nolint:errcheck
		return
	}
	defer conn.Close() //nolint:errcheck

	// Auto-subscribe so the client receives the event stream without a round-trip.
	if _, err := conn.Write([]byte(`{"subscribe":true}` + "\n")); err != nil {
		c.Close(websocket.StatusInternalError, "pi rpc subscribe failed") //nolint:errcheck
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// pi socket -> WebSocket: one JSONL line per text frame.
	go func() {
		defer cancel()
		br := bufio.NewReader(conn)
		for {
			line, rerr := br.ReadBytes('\n')
			if trimmed := bytes.TrimRight(line, "\r\n"); len(trimmed) > 0 {
				if werr := c.Write(ctx, websocket.MessageText, trimmed); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// WebSocket -> pi socket: each client message ({"message":…}/{"abort":true}/…)
	// becomes one JSONL line. Returning here ends the handler and tears both sides down.
	for {
		_, data, rerr := c.Read(ctx)
		if rerr != nil {
			return
		}
		if _, werr := conn.Write(append(bytes.TrimRight(data, "\r\n"), '\n')); werr != nil {
			return
		}
	}
}
