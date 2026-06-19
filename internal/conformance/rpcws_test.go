package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lukastk/sesh/internal/matrix"
)

// findPiRPCSocket mirrors the daemon's piRPCSocketPath: the pi rpc-socket extension
// creates <tmpdir>/pi-rpc-sockets/<sessionId>.sock under the first of $TMPDIR, /tmp,
// /var/tmp that fits. The test looks in the same order.
func findPiRPCSocket(sessionID string) (string, bool) {
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

// TestThreadRPCWebSocket is an honest end-to-end test of the daemon's
// GET /v1/threads/rpc WebSocket: it spawns a REAL pi agent, connects a REAL
// WebSocket over the daemon's TCP API, injects a prompt, and asserts the reply
// STREAMS back as text_delta events — plus the loud-error paths (bad token / unknown
// id / non-pi). Outside the matrix (pi-specific, WS — not a (loc)×(agent) cell), like
// the other focused regression tests. Gated by -short.
func TestThreadRPCWebSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skips spawning a real pi agent")
	}
	addr := freePort(t)
	token := fmt.Sprintf("rpctok-%d", time.Now().UnixNano())
	sb := newSandbox(t, matrix.Local, withAPI(addr, token))
	sb.startDaemon(t)

	// Wait for the TCP API to answer (it binds in the background).
	if !waitUntil(10*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}) {
		t.Fatalf("api daemon never listened on %s", addr)
	}

	wsURL := "ws://" + addr + "/v1/threads/rpc"
	authHdr := func(tok string) http.Header {
		h := http.Header{}
		if tok != "" {
			h.Set("Authorization", "Bearer "+tok)
		}
		return h
	}
	dialStatus := func(t *testing.T, url, tok string) int {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: authHdr(tok)})
		if err == nil {
			c.CloseNow() //nolint:errcheck
			return http.StatusSwitchingProtocols
		}
		if resp == nil {
			t.Fatalf("dial %s: no response: %v", url, err)
		}
		return resp.StatusCode
	}

	// --- Loud-error paths (cheap: no agent needs to be running) ---

	// No token -> 401 (inherited from the daemon's bearer auth on the TCP API).
	if got := dialStatus(t, wsURL+"?id=anything", ""); got != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", got)
	}
	// Unknown id -> 404.
	if got := dialStatus(t, wsURL+"?id=deadbeef-no-such-thread", token); got != http.StatusNotFound {
		t.Errorf("unknown id: status %d, want 404", got)
	}
	// Non-pi thread -> 400 (a headless claude RECORD; no agent is spawned).
	claudeOut, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "claude", "--name", "notpi", "--cwd", "/tmp", "--headless", "--json")
	if err != nil {
		t.Fatalf("create headless claude record: %v\n%s", err, stderr)
	}
	var claude struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(claudeOut), &claude); err != nil {
		t.Fatalf("decode claude json: %v", err)
	}
	if got := dialStatus(t, wsURL+"?id="+claude.ID, token); got != http.StatusBadRequest {
		t.Errorf("non-pi thread: status %d, want 400", got)
	}

	// --- The real thing: a streaming pi turn over the WebSocket ---

	th := sb.newThread(t, "pi", "rpcws", t.TempDir()) // headed pi (real agent)
	sb.waitThreadReady(t, th.ID, "pi")
	if !waitUntil(20*time.Second, func() bool {
		_, ok := findPiRPCSocket(th.AgentSessionID)
		return ok
	}) {
		t.Fatalf("pi rpc socket never appeared for session %q", th.AgentSessionID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL+"?id="+th.ID, &websocket.DialOptions{HTTPHeader: authHdr(token)})
	if err != nil {
		t.Fatalf("ws dial (pi): %v", err)
	}
	defer c.CloseNow() //nolint:errcheck

	const marker = "WS-PI-OK-4242"
	msg := `{"message":"Reply with exactly ` + marker + ` and nothing else."}`
	if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatalf("ws write: %v", err)
	}

	var got strings.Builder
	sawDelta := false
	for {
		_, data, rerr := c.Read(ctx)
		if rerr != nil {
			t.Fatalf("ws read: %v (assembled: %q)", rerr, got.String())
		}
		var ev struct {
			Event string `json:"event"`
			Delta string `json:"delta"`
			OK    bool   `json:"ok"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			continue // non-event line (e.g. an ack); ignore
		}
		if ev.Event == "text_delta" {
			sawDelta = true
			got.WriteString(ev.Delta)
		}
		if ev.Event == "agent_end" {
			break
		}
	}
	if !sawDelta {
		t.Errorf("no text_delta events streamed (the stream must be incremental, not one blob)")
	}
	if !strings.Contains(got.String(), marker) {
		t.Errorf("streamed reply %q did not contain %q", got.String(), marker)
	}
}
