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
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesRPC(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/threads/rpc", d.handleThreadRPC)
}

// piRPCSocketPath preserves the protocol-2 lookup used by headed Pi delivery.
func piRPCSocketPath(sessionID string) (string, bool) {
	return piRPCSocketPathWithSuffix(sessionID, ".sock")
}

// piRPCSocketPathWithSuffix resolves a protocol-specific Pi socket. Both the
// thread's Pi session id and its filename are UUID-derived; no caller text is
// joined into a socket path without validation.
func piRPCSocketPathWithSuffix(sessionID, suffix string) (string, bool) {
	if !validCanonicalUUID(sessionID) {
		return "", false
	}
	var dirs []string
	if t := os.Getenv("TMPDIR"); t != "" {
		dirs = append(dirs, t)
	}
	dirs = append(dirs, "/tmp", "/var/tmp")
	for _, dir := range dirs {
		p := filepath.Join(dir, "pi-rpc-sockets", sessionID+suffix)
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return p, true
		}
	}
	return "", false
}

// handleThreadRPC preserves the legacy protocol-2 bridge and adds live-v1.
// Empty protocol (the historical URL) and protocol2 route to <session>.sock and
// auto-subscribe exactly as before. live-v1 routes to <session>.live.sock and
// writes nothing implicitly: the browser speaks the published JSONL protocol.
func (d *Daemon) handleThreadRPC(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !validCanonicalUUID(id) {
		writeError(w, http.StatusBadRequest, "rpc: id must be a canonical UUID")
		return
	}
	protocol := r.URL.Query().Get("protocol")
	legacy := protocol == "" || protocol == "protocol2"
	if !legacy && protocol != "live-v1" {
		writeError(w, http.StatusBadRequest, "rpc: unknown protocol "+protocol+" (want protocol2 or live-v1)")
		return
	}
	if owner := wsOwnerFromContext(r.Context()); owner != "" && owner != d.cfg.Machine {
		d.proxyBrowserWebSocket(w, r, owner)
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
	if !piFamily(th.AgentKind) {
		writeError(w, http.StatusBadRequest, "rpc: only pi/mypi threads expose an rpc socket (this thread is "+th.AgentKind+")")
		return
	}
	if !validCanonicalUUID(th.AgentSessionID) {
		writeError(w, http.StatusConflict, "rpc: thread has no valid canonical Pi session UUID")
		return
	}
	suffix := ".sock"
	if protocol == "live-v1" {
		suffix = ".live.sock"
	}
	sockPath, ok := piRPCSocketPathWithSuffix(th.AgentSessionID, suffix)
	if !ok {
		writeError(w, http.StatusConflict, "rpc: no live Pi "+protocolName(protocol)+" socket for thread "+id)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow() //nolint:errcheck
	c.SetReadLimit(liveRPCMaxRecord)
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		c.Close(websocket.StatusInternalError, "pi rpc socket dial failed") //nolint:errcheck
		return
	}
	defer conn.Close() //nolint:errcheck

	if protocol == "live-v1" {
		bridgeLiveJSONL(r.Context(), c, conn)
		return
	}
	bridgeLegacyPiRPC(r.Context(), c, conn)
}

func protocolName(protocol string) string {
	if protocol == "live-v1" {
		return "live-v1"
	}
	return "protocol2"
}

// bridgeLegacyPiRPC is the pre-schema-55 bridge kept byte-for-byte in semantics:
// subscribe once, one Unix JSONL line per WS text frame, one WS message per line.
func bridgeLegacyPiRPC(parent context.Context, c *websocket.Conn, conn net.Conn) {
	if _, err := conn.Write([]byte(`{"subscribe":true}` + "\n")); err != nil {
		c.Close(websocket.StatusInternalError, "pi rpc subscribe failed") //nolint:errcheck
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
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
