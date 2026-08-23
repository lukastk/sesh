package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	raw("send-keys", "-t", pane, "Enter")                                      //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
	raw("send-keys", "-t", pane, "-l", "clear") //nolint:errcheck
	raw("send-keys", "-t", pane, "Enter")       //nolint:errcheck
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

// TestSendTextSingleLineUsesBracketedPaste pins the transport-level half of the
// long Codex report regression. The real pane requests bracketed paste, records
// its input bytes, and must receive one explicit paste event followed by Enter
// (the tty line discipline presents Enter as LF to the reader).
// A literal send-keys stream can look identical after rendering but leaves a TUI
// paste detector active when Enter arrives, so asserting the wire bytes matters.
func TestSendTextSingleLineUsesBracketedPaste(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	sock := "seshsend-single-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	const text = "SINGLE_LINE_BRACKETED_REPORT"
	want := []byte("\x1b[200~" + text + "\x1b[201~\n")
	inputPath := filepath.Join(t.TempDir(), "input.bin")
	command := fmt.Sprintf("printf '\\033[?2004hBRACKETED_PASTE_READY\\n'; dd bs=1 count=%d of=%q status=none; sleep 5", len(want), inputPath)
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "single", "-x", "100", "-y", "30", command); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	pane, err := raw("list-panes", "-t", "single", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		capture, _ := raw("capture-pane", "-t", pane, "-p")
		if strings.Contains(capture, "BRACKETED_PASTE_READY") {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("pane never acknowledged bracketed-paste mode")
	}

	if err := NewServer(sock).SendText(pane, text, true); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	var got []byte
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(inputPath)
		if len(got) == len(want) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != string(want) {
		t.Fatalf("pane input = %q, want one bracketed paste then Enter %q", got, want)
	}
}

// TestSendTextConcurrentMultilineDoesNotCrossTargets reproduces the production
// ticket-send race against a real tmux server. Every multiline send needs its own
// buffer: sharing one named buffer lets a concurrent set-buffer overwrite another
// prompt before paste-buffer consumes it, or delete the buffer out from under the
// other request. Both outcomes are externally visible here as an error or a marker
// arriving in the wrong pane.
func TestSendTextConcurrentMultilineDoesNotCrossTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	sock := "seshsend-concurrent-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	leftPath := t.TempDir() + "/left.txt"
	rightPath := t.TempDir() + "/right.txt"
	if _, err := raw(
		"-f", "/dev/null", "new-session", "-d", "-s", "sends", "-x", "120", "-y", "40",
		fmt.Sprintf("cat > %q", leftPath),
	); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	if _, err := raw("new-window", "-d", "-t", "sends", fmt.Sprintf("cat > %q", rightPath)); err != nil {
		t.Fatalf("new-window: %v", err)
	}
	leftPane, err := raw("list-panes", "-t", "sends:0", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("left pane id: %v", err)
	}
	rightPane, err := raw("list-panes", "-t", "sends:1", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("right pane id: %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	const sendsPerPane = 48
	server := NewServer(sock)
	start := make(chan struct{})
	errs := make(chan error, sendsPerPane*2)
	var group sync.WaitGroup
	for i := range sendsPerPane {
		for _, send := range []struct {
			pane   string
			prefix string
		}{
			{leftPane, "LEFT"},
			{rightPane, "RIGHT"},
		} {
			group.Add(1)
			go func(index int, pane, prefix string) {
				defer group.Done()
				<-start
				text := fmt.Sprintf("%s_%03d_BEGIN\n%s_%03d_END\n", prefix, index, prefix, index)
				if err := server.SendText(pane, text, false); err != nil {
					errs <- err
				}
			}(i, send.pane, send.prefix)
		}
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent SendText returned an error: %v", err)
	}

	var left, right string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		leftBytes, _ := os.ReadFile(leftPath)
		rightBytes, _ := os.ReadFile(rightPath)
		left, right = string(leftBytes), string(rightBytes)
		if strings.Count(left, "LEFT_") == sendsPerPane*2 &&
			strings.Count(right, "RIGHT_") == sendsPerPane*2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(left, "RIGHT_") || strings.Contains(right, "LEFT_") {
		t.Fatalf("concurrent multiline sends crossed targets: left=%q right=%q", left, right)
	}
	for i := range sendsPerPane {
		for _, want := range []string{
			fmt.Sprintf("LEFT_%03d_BEGIN", i),
			fmt.Sprintf("LEFT_%03d_END", i),
		} {
			if !strings.Contains(left, want) {
				t.Errorf("left pane is missing %q", want)
			}
		}
		for _, want := range []string{
			fmt.Sprintf("RIGHT_%03d_BEGIN", i),
			fmt.Sprintf("RIGHT_%03d_END", i),
		} {
			if !strings.Contains(right, want) {
				t.Errorf("right pane is missing %q", want)
			}
		}
	}
}

// TestKillPaneLastPaneTearsDownServerIsSuccess reproduces the `stop` 500 bug: killing
// the LAST pane on a server tears the whole server down, and tmux reports the kill-pane
// command itself as failed ("server exited unexpectedly") even though the pane is gone.
// KillPane must treat that as success — while still surfacing a genuine kill failure.
func TestKillPaneLastPaneTearsDownServerIsSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Phase 1 — the bug: exactly one session/window/pane. Killing it tears the server
	// down; tmux returns a non-zero "server exited" error, but the pane is gone → success.
	sockA := "seshkill-last-" + strings.ReplaceAll(t.Name(), "/", "_")
	rawA := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sockA}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := rawA("-f", "/dev/null", "new-session", "-d", "-s", "only", "-x", "80", "-y", "24"); err != nil {
		t.Fatalf("new-session A: %v", err)
	}
	defer exec.Command("tmux", "-L", sockA, "kill-server").Run() //nolint:errcheck
	paneA, err := rawA("list-panes", "-t", "only", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id A: %v", err)
	}
	if err := NewServer(sockA).KillPane(paneA); err != nil {
		t.Fatalf("KillPane on the last pane must succeed (server-exit is the intended teardown), got: %v", err)
	}
	if out, err := rawA("list-sessions"); err == nil {
		t.Errorf("server should be gone after killing the last pane; list-sessions returned: %q", out)
	}

	// Phase 2 — loudness preserved: on a LIVE server, killing a nonexistent pane id must
	// still error (the fix swallows ONLY server-is-gone messages, not real failures).
	sockB := "seshkill-bad-" + strings.ReplaceAll(t.Name(), "/", "_")
	rawB := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sockB}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := rawB("-f", "/dev/null", "new-session", "-d", "-s", "keep", "-x", "80", "-y", "24"); err != nil {
		t.Fatalf("new-session B: %v", err)
	}
	defer exec.Command("tmux", "-L", sockB, "kill-server").Run() //nolint:errcheck
	if err := NewServer(sockB).KillPane("%99999"); err == nil {
		t.Error("KillPane on a nonexistent pane (live server) must surface an error, not be swallowed")
	}
}

// TestSendTextLargePayloadExceedsArgvCap proves the load-buffer transport removes
// tmux's per-command argv cap (MAX_IMSGSIZE, 16384 bytes). The old set-buffer path
// passed the whole text as one argv and failed "command too long" past that size, so
// a real 34 KB ticket prompt could never be delivered to its pane. load-buffer streams
// the payload on stdin, which has no such cap. The pane requests bracketed paste and
// records its raw input bytes; the full multi-line payload must arrive byte-for-byte
// inside one paste event.
func TestSendTextLargePayloadExceedsArgvCap(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshsend-large-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	// A multi-line payload well past the 16384-byte argv cap (the incident was a 34 KB
	// ticket prompt), with sentinels so a truncated delivery is caught, not just a wrong
	// length.
	var sb strings.Builder
	sb.WriteString("BIGSTART\n")
	for sb.Len() < 34000 {
		sb.WriteString("the quick brown fox jumps over the lazy dog 0123456789\n")
	}
	sb.WriteString("BIGEND")
	text := sb.String()
	if len(text) <= 16384 {
		t.Fatalf("test payload must exceed the 16384-byte argv cap, got %d", len(text))
	}
	// paste-buffer -p wraps the payload in bracketed-paste markers; enter=false so no
	// trailing Enter. paste-buffer (no -r) also replaces embedded LF with CR inside the
	// paste (how a multi-line block reaches a TUI's line editor), so the delivered bytes
	// carry CR where the source had LF. Length is unchanged. Reader captures exactly this
	// many raw bytes.
	want := []byte("\x1b[200~" + strings.ReplaceAll(text, "\n", "\r") + "\x1b[201~")

	inputPath := filepath.Join(t.TempDir(), "input.bin")
	// Read the pty in RAW mode: canonical mode caps a line/input burst and would drop
	// bytes of a large paste at the tty discipline (a harness artifact — a real agent TUI
	// reads raw), making the reader, not SendText, the bottleneck.
	command := fmt.Sprintf("stty raw -echo 2>/dev/null; printf '\\033[?2004hLARGE_PASTE_READY\\n'; head -c %d > %q; sleep 5", len(want), inputPath)
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "large", "-x", "100", "-y", "30", command); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	pane, err := raw("list-panes", "-t", "large", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		capture, _ := raw("capture-pane", "-t", pane, "-p")
		if strings.Contains(capture, "LARGE_PASTE_READY") {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("pane never acknowledged bracketed-paste mode")
	}

	// The load-buffer transport must accept the oversized payload without error.
	if err := NewServer(sock).SendText(pane, text, false); err != nil {
		t.Fatalf("SendText of a %d-byte payload failed — the argv cap was not removed: %v", len(text), err)
	}
	var got []byte
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(inputPath)
		if len(got) == len(want) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != string(want) {
		t.Fatalf("large payload not delivered intact: got %d bytes, want %d", len(got), len(want))
	}
}
