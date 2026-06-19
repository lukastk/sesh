package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lukastk/sesh/internal/matrix"
)

// TestThreadTerminalWebSocket is an honest end-to-end test of GET /v1/threads/terminal:
// it spawns a REAL headful pi agent, opens a REAL WebSocket over the daemon's TCP API,
// asserts the live tmux pane STREAMS back (attach + read bridge), asserts a typed marker
// REACHES the real pane (the write bridge), and checks the loud-error paths (404 unknown,
// 409 headless/no-pane). Outside the matrix (WS, not a (loc)×(agent) cell). Gated by -short.
func TestThreadTerminalWebSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skips spawning a real pi agent")
	}
	addr := freePort(t)
	token := fmt.Sprintf("termtok-%d", time.Now().UnixNano())
	sb := newSandbox(t, matrix.Local, withAPI(addr, token))
	sb.startDaemon(t)
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

	wsURL := "ws://" + addr + "/v1/threads/terminal"
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+token)
	dialStatus := func(url string) int {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
		if err == nil {
			c.CloseNow() //nolint:errcheck
			return http.StatusSwitchingProtocols
		}
		if resp == nil {
			t.Fatalf("dial %s: no response: %v", url, err)
		}
		return resp.StatusCode
	}

	// Loud errors (cheap).
	if got := dialStatus(wsURL + "?id=deadbeef-nope"); got != http.StatusNotFound {
		t.Errorf("unknown id: status %d, want 404", got)
	}
	hl, _, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "hl", "--cwd", "/tmp", "--headless", "--json")
	if err != nil {
		t.Fatalf("headless pi: %v", err)
	}
	var hlTh struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(hl), &hlTh) //nolint:errcheck
	if got := dialStatus(wsURL + "?id=" + hlTh.ID); got != http.StatusConflict {
		t.Errorf("headless (no pane): status %d, want 409", got)
	}

	// The real thing: attach a terminal to a live headful pi pane.
	th := sb.newThread(t, "pi", "termws", t.TempDir())
	pane := sb.waitThreadReady(t, th.ID, "pi")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL+"?id="+th.ID+"&cols=120&rows=40", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("ws dial (terminal): %v", err)
	}
	defer c.CloseNow() //nolint:errcheck

	// Read bridge: the live pi TUI must stream as terminal bytes (with ANSI control).
	got := make([]byte, 0, 8192)
	readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
	defer readCancel()
	for len(got) < 200 {
		_, data, rerr := c.Read(readCtx)
		if rerr != nil {
			t.Fatalf("terminal read: %v (got %d bytes so far)", rerr, len(got))
		}
		got = append(got, data...)
	}
	if !strings.ContainsRune(string(got), '\x1b') {
		t.Errorf("streamed terminal bytes contain no ANSI escape — not a real terminal? (%q)", string(got[:min(len(got), 120)]))
	}

	// Write bridge: type a unique marker; it must land in the REAL pane (pi echoes typed
	// input into its composer), observed via tmux capture-pane on the thread's own pane.
	const marker = "ZZTERMWRITEOK"
	if err := c.Write(ctx, websocket.MessageBinary, []byte(marker)); err != nil {
		t.Fatalf("terminal write: %v", err)
	}
	if !waitUntil(10*time.Second, func() bool {
		cap, err := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		return err == nil && strings.Contains(cap, marker)
	}) {
		t.Fatalf("typed marker %q never appeared in the live pane (write bridge broken)", marker)
	}
}
