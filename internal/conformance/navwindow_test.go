package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("tmux.nav-window", matrix.AgentAgnostic, matrix.Local, testNavWindow)
}

// testNavWindow proves nav lands on the WINDOW holding the thread's @sesh-thread-id
// pane, not the session's last-active window. A real headed thread's session gets a
// SECOND window made active (the bug condition: switch-client to a bare session would
// land there); nav --in-client --thread must switch the client back to the thread's
// own window.
func testNavWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newThread(t, "pi", "navwin", "/tmp")
	session := th.SessionName
	var pane string
	if !waitUntil(agentStartTimeout, func() bool {
		p, _, ok := sb.markedPane(t, th.ID)
		pane = p
		return ok
	}) {
		t.Fatalf("thread pane never came up")
	}
	wantWin := paneWindow(t, sb, pane) // the thread's window — the expected nav destination

	// Add a SECOND window to the thread's session.
	if out, err := sb.rawTmux(t, "new-window", "-t", "="+session); err != nil {
		t.Fatalf("new-window: %v\n%s", err, out)
	}
	// Attach exactly one client to the session (a detached holder whose pane attaches).
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", "navwinhold",
		"env -u TMUX tmux -L "+sb.TmuxSocket+" attach -t "+session); err != nil {
		t.Fatalf("attach holder: %v\n%s", err, out)
	}
	if !waitUntil(8*time.Second, func() bool { return navClientSession(t, sb) == session }) {
		t.Fatalf("holder never attached to %s", session)
	}
	// Make a NON-thread window active for the session — the condition the fix targets.
	otherWin := ""
	wins, _ := sb.rawTmux(t, "list-windows", "-t", "="+session, "-F", "#{window_index}")
	for _, w := range strings.Fields(wins) {
		if w != wantWin {
			otherWin = w
			break
		}
	}
	if otherWin == "" {
		t.Fatalf("no second window created (windows=%q)", wins)
	}
	if _, err := sb.rawTmux(t, "select-window", "-t", "="+session+":"+otherWin); err != nil {
		t.Fatalf("select non-thread window: %v", err)
	}
	if got := navClientWindow(t, sb); got != otherWin {
		t.Fatalf("setup: client active window = %s, want the non-thread window %s", got, otherWin)
	}

	// $TMUX on the work socket (basename guard) + the carried client.
	spOut, _ := sb.rawTmux(t, "display-message", "-t", "="+session, "-p", "#{socket_path}")
	tmuxVal := strings.TrimSpace(spOut) + ",1,1"
	clientName := navClientName(t, sb)

	// nav --in-client --thread: the client lands on the THREAD's window, not the
	// session's last-active (non-thread) window.
	if _, stderr, err := runNav(t, sb, tmuxVal, "--in-client", "--client", clientName,
		"--to", sb.Machine+":"+session, "--thread", th.ID); err != nil {
		t.Fatalf("nav --in-client --thread: %v\n%s", err, stderr)
	}
	if !waitUntil(8*time.Second, func() bool { return navClientWindow(t, sb) == wantWin }) {
		t.Errorf("nav landed on window %q, want the thread's window %q", navClientWindow(t, sb), wantWin)
	}

	// Sanity: WITHOUT --thread, nav is session-level and stays on the last-active
	// window — proving the window targeting is what moved it (not an incidental switch).
	if _, err := sb.rawTmux(t, "select-window", "-t", "="+session+":"+otherWin); err != nil {
		t.Fatalf("re-select non-thread window: %v", err)
	}
	if _, stderr, err := runNav(t, sb, tmuxVal, "--in-client", "--client", clientName,
		"--to", sb.Machine+":"+session); err != nil {
		t.Fatalf("nav --in-client (no --thread): %v\n%s", err, stderr)
	}
	// Give it a beat; it should NOT have moved off the non-thread window.
	time.Sleep(500 * time.Millisecond)
	if got := navClientWindow(t, sb); got != otherWin {
		t.Errorf("session-level nav (no --thread) moved off the active window to %q (want %q)", got, otherWin)
	}
}

// paneWindow returns the window index of a pane.
func paneWindow(t *testing.T, sb *Sandbox, pane string) string {
	t.Helper()
	out, err := sb.rawTmux(t, "display-message", "-t", pane, "-p", "#{window_index}")
	if err != nil {
		t.Fatalf("window of %s: %v", pane, err)
	}
	return strings.TrimSpace(out)
}

// navClientName returns the (single) work-socket client's name.
func navClientName(t *testing.T, sb *Sandbox) string {
	t.Helper()
	out, err := sb.rawTmux(t, "list-clients", "-F", "#{client_name}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
}

// navClientWindow returns the window index the (single) work-socket client is on.
func navClientWindow(t *testing.T, sb *Sandbox) string {
	t.Helper()
	name := navClientName(t, sb)
	if name == "" {
		return ""
	}
	out, err := sb.rawTmux(t, "display-message", "-c", name, "-p", "#{window_index}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
