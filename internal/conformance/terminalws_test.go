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

	// Query-token auth (the WebView/browser path; a WS handshake can't set headers).
	// Dial with NO Authorization header — auth must come from ?token= alone.
	noHdrDial := func(url, tok string) int {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h := http.Header{}
		if tok != "" {
			h.Set("Authorization", "Bearer "+tok)
		}
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
		if err == nil {
			c.CloseNow() //nolint:errcheck
			return http.StatusSwitchingProtocols
		}
		if resp == nil {
			t.Fatalf("dial %s: no response: %v", url, err)
		}
		return resp.StatusCode
	}
	if got := noHdrDial(wsURL+"?id=deadbeef-nope&token="+token, ""); got != http.StatusNotFound {
		t.Errorf("query token (good): status %d, want 404 (authenticated, unknown id)", got)
	}
	if got := noHdrDial(wsURL+"?id=anything&token=wrong-query-token", ""); got != http.StatusUnauthorized {
		t.Errorf("query token (bad): status %d, want 401", got)
	}
	if got := noHdrDial(wsURL+"?id=anything", ""); got != http.StatusUnauthorized {
		t.Errorf("no token at all: status %d, want 401", got)
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
	cwd := t.TempDir()
	th := sb.newThread(t, "pi", "termws", cwd)
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

	// Working-dir fix (uiterm): the grouped viewer session is created with
	// `-c <thread cwd>`, so the work-conf `t` binding (`display-popup -E "zsh
	// -l"`, which without -d opens in the popup CLIENT's session cwd) lands in
	// the THREAD cwd — matching the real cockpit. Without the fix the viewer's
	// session cwd is the DAEMON's cwd (~). The WS pty already attached a real
	// client to the viewer session; replicate the binding honestly by running
	// `display-popup -c <that client> -E 'pwd>file'` and asserting the popup
	// shell's cwd is the thread cwd. (A command-line popup would read the CLI
	// client's cwd, not the session's — it must be driven through the attached
	// client, exactly as the `t` keybind is.)
	var viewer, vclient string
	if !waitUntil(10*time.Second, func() bool {
		out, err := sb.rawTmux(t, "list-clients", "-F", "#{client_name}\t#{client_session}")
		if err != nil {
			return false
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			name, sess, ok := strings.Cut(line, "\t")
			if ok && strings.HasPrefix(sess, "uiterm-"+th.ID[:8]) {
				vclient, viewer = name, sess
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no client attached to a uiterm-%s viewer session", th.ID[:8])
	}
	pwdFile := filepath.Join(t.TempDir(), "popup-pwd.txt")
	if out, err := sb.rawTmux(t, "display-popup", "-c", vclient, "-t", viewer, "-E", "pwd > "+pwdFile+"; sleep 0.2"); err != nil {
		t.Fatalf("display-popup on viewer: %v\n%s", err, out)
	}
	var gotPath string
	if !waitUntil(5*time.Second, func() bool {
		b, err := os.ReadFile(pwdFile)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 {
			return false
		}
		gotPath, _ = filepath.EvalSymlinks(strings.TrimSpace(string(b)))
		return true
	}) {
		t.Fatalf("popup never wrote its pwd to %s", pwdFile)
	}
	wantPath, _ := filepath.EvalSymlinks(cwd)
	if gotPath != wantPath {
		t.Errorf("prefix+t popup cwd = %q, want thread cwd %q (uiterm `-c <cwd>` fix missing → opens in daemon's ~)", gotPath, wantPath)
	}
}
