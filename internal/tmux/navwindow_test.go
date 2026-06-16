package tmux

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestInnerSwitchScriptExplicitWindow checks that an EXPLICIT window index (prefix+L
// replaying a recorded location) overrides the thread-window resolution: the switch
// target becomes =session:<window> directly, skipping the list-panes marker lookup.
func TestInnerSwitchScriptExplicitWindow(t *testing.T) {
	script := InnerSwitchScript("sock", "sesh_x", "some-thread", "2", "/marker")
	if !strings.Contains(script, `w='2'; tgt="=sesh_x:$w"`) {
		t.Errorf("explicit window 2 not wired into the switch target:\n%s", script)
	}
	if strings.Contains(script, "list-panes -s") {
		t.Errorf("explicit window should SKIP the thread marker resolution:\n%s", script)
	}
}

// TestInnerSwitchScriptLandsOnThreadWindow runs the REAL generated switch script
// against a REAL tmux server and proves it lands the client on the WINDOW holding
// the thread's @sesh-thread-id pane — not the session's last-active window. This
// exercises the shell quoting of the window-resolution branch (the in-client Go path
// is covered by the conformance tmux.nav-window cell; this covers the master path's
// shell snippet). Single-client server, so the no-marker single-client branch runs.
func TestInnerSwitchScriptLandsOnThreadWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshnavwin-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	tx := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := tx("-f", "/dev/null", "new-session", "-d", "-s", "sesh_x", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	const tid = "thread-window-test-id"
	p0, err := tx("list-panes", "-t", "sesh_x:0", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	if _, err := tx("set-option", "-p", "-t", p0, ThreadIDOption, tid); err != nil {
		t.Fatalf("mark pane: %v", err)
	}
	// A second window, made active — the condition the window targeting must override.
	if _, err := tx("new-window", "-t", "sesh_x"); err != nil {
		t.Fatalf("new-window: %v", err)
	}
	if _, err := tx("select-window", "-t", "sesh_x:1"); err != nil {
		t.Fatalf("select-window: %v", err)
	}
	// Attach ONE client (a detached holder whose pane attaches to sesh_x).
	if _, err := tx("new-session", "-d", "-s", "hold", "env -u TMUX tmux -L "+sock+" attach -t sesh_x"); err != nil {
		t.Fatalf("holder: %v", err)
	}
	clientWindow := func() string {
		cl, err := tx("list-clients", "-t", "=sesh_x", "-F", "#{client_name}")
		if err != nil || cl == "" {
			return ""
		}
		name := strings.SplitN(cl, "\n", 2)[0]
		w, _ := tx("display-message", "-c", name, "-p", "#{window_index}")
		return w
	}
	deadline := time.Now().Add(8 * time.Second)
	for clientWindow() == "" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := clientWindow(); got != "1" {
		t.Fatalf("setup: client should start on the active window 1, got %q", got)
	}

	// Run the real generated script (no live marker → single-client branch switches).
	script := InnerSwitchScript(sock, "sesh_x", tid, "", "/nonexistent-marker")
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	deadline = time.Now().Add(8 * time.Second)
	for clientWindow() != "0" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := clientWindow(); got != "0" {
		t.Errorf("script did not land the client on the thread's window 0 (got %q)", got)
	}
}

// TestSendTextMultilinePreservesNewlines proves the bracketed-paste path: a multi-line
// send arrives as ONE multi-line input (newlines intact, NOT submitted line-by-line). It
// runs an interactive bash with bracketed paste enabled (the same readline mechanism a
// modern agent TUI uses) and asserts (a) every line survives in the pane and (b) nothing
// was executed (no "command not found") — i.e. the embedded newlines did not submit.
func TestSendTextMultilinePreservesNewlines(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	sock := "seshsend-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "s", "-x", "120", "-y", "40", "bash --norc --noprofile -i"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	pane, err := raw("list-panes", "-t", "s", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	// Enable bracketed paste in the proxy shell (deterministic regardless of bash default).
	raw("send-keys", "-t", pane, "-l", "bind 'set enable-bracketed-paste on'") //nolint:errcheck
	raw("send-keys", "-t", pane, "Enter")                                       //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
	raw("send-keys", "-t", pane, "-l", "clear")  //nolint:errcheck
	raw("send-keys", "-t", pane, "Enter")        //nolint:errcheck
	time.Sleep(300 * time.Millisecond)

	srv := NewServer(sock)
	text := "SENTINELA first line\nSENTINELB second line\nSENTINELC third line"
	if err := srv.SendText(pane, text, false); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	capt, err := raw("capture-pane", "-p", "-t", pane)
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	for _, s := range []string{"SENTINELA", "SENTINELB", "SENTINELC"} {
		if !strings.Contains(capt, s) {
			t.Errorf("multi-line paste lost %q; pane:\n%s", s, capt)
		}
	}
	// enter=false, so nothing should have executed — a submitted "SENTINELA first line"
	// would run as a command and bash would print "not found".
	if strings.Contains(capt, "not found") {
		t.Errorf("multi-line text was EXECUTED (newlines submitted as Enter); pane:\n%s", capt)
	}
}
