package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("context-column", claimContextColumn)
	registerTUIClaim("context-view", claimContextView)
	registerTUIClaim("context-edit", claimContextEdit)
}

// claimContextColumn: the CTX column marks exactly the threads whose REAL record
// carries a context, and follows `context set` / `context clear` on the daemon.
func claimContextColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	with := sb.newHeadlessThread(t, "pi", "with-context")
	sb.newHeadlessThread(t, "pi", "without-context")
	setContext(t, sb, with.ID, "Be terse.")

	cols, err := tui.ResolveColumns([]string{"name", "context", "tags"})
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols)
	m, view := renderUntilRowView(t, m, "without-context")
	if !strings.Contains(view, "CTX") {
		t.Fatalf("no CTX header:\n%s", view)
	}
	if l := lineWith(view, "with-context"); !strings.Contains(l, "◆") {
		t.Errorf("a thread with a context is not marked: %q", l)
	}
	if l := lineWith(view, "without-context"); strings.Contains(l, "◆") {
		t.Errorf("a thread without a context is marked: %q", l)
	}
	clearContext(t, sb, with.ID)
	if !waitUntil(30*time.Second, func() bool {
		m, view = renderUntilRowView(t, m, "with-context")
		return !strings.Contains(lineWith(view, "with-context"), "◆")
	}) {
		t.Fatalf("the mark did not go after context clear:\n%s", view)
	}
}

// claimContextView: `context-view` fetches the selected thread's context from the
// daemon and shows it in a scrollable takeover — a long context starts at its
// top with a ▼ indicator, scrolls to its end, and esc returns to the grid.
func claimContextView(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "long-context")
	var lines []string
	for i := 1; i <= 120; i++ {
		lines = append(lines, fmt.Sprintf("rule %03d: keep it real", i))
	}
	setContext(t, sb, th.ID, strings.Join(lines, "\n"))

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "long-context")
	m = sendMsg(t, m, tea.WindowSizeMsg{Width: 100, Height: 24}) // a real terminal's size
	m = selectRowByName(t, m, "long-context")
	m = runCommand(t, m, "context-view")
	if !m.ContextOpen() {
		t.Fatal("context-view did not open the viewer")
	}
	v := m.View()
	if !strings.Contains(v, "thread context · long-context") || !strings.Contains(v, "rule 001") || !strings.Contains(v, "▼") {
		t.Fatalf("viewer does not show the top of the REAL context with a ▼:\n%s", v)
	}
	if strings.Contains(v, "rule 120") {
		t.Fatalf("120 lines fit an unscrolled pane? the viewer is not paging:\n%s", v)
	}
	m = runKey(t, m, "G")
	if v = m.View(); !strings.Contains(v, "rule 120") || !strings.Contains(v, "▲") || strings.Contains(v, "rule 001") {
		t.Fatalf("G did not scroll to the end:\n%s", v)
	}
	m = runKey(t, m, "esc")
	if m.ContextOpen() {
		t.Fatal("esc did not close the viewer")
	}
}

// claimContextEdit runs the REAL `sesh tui` in its own tmux server with an
// editor script: `context-edit` suspends the TUI to the editor on a file holding
// the thread's current context, the saved file becomes the thread's context on
// the daemon, and the TUI comes back. Saving an emptied file removes the context.
func claimContextEdit(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "edit-me")
	setContext(t, sb, th.ID, "OLD CONTEXT")

	dir := t.TempDir()
	seen, next := filepath.Join(dir, "seen"), filepath.Join(dir, "next")
	editor := filepath.Join(dir, "editor.sh")
	// The "editor": record what it was given, then replace it with ./next.
	script := "#!/bin/sh\ncat \"$1\" > " + seen + "\ncat " + next + " > \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, []byte("NEW CONTEXT\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sock := fmt.Sprintf("sesh-test-tui-%d", time.Now().UnixNano())
	tm := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() { tm("kill-server") }) //nolint:errcheck — our own socket
	start := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-s", "tui", "-x", "160", "-y", "40",
		seshBin(t), "tui", "--editor", editor, "--columns", "name,context")
	start.Env = sandboxEnv(map[string]string{
		"SESH_HOME": sb.Home, "SESH_MACHINE": sb.Machine,
		"SESH_TMUX_SOCKET": sb.TmuxSocket, "SESH_MASTER_SOCKET": sb.MasterSocket, "TERM": "xterm-256color",
	})
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start the tui: %v\n%s", err, out)
	}
	capture := func() string { out, _ := tm("capture-pane", "-p", "-t", "tui"); return out }
	waitPane := func(what string, ok func(string) bool) {
		t.Helper()
		if !waitUntil(30*time.Second, func() bool { return ok(capture()) }) {
			t.Fatalf("%s; pane:\n%s", what, capture())
		}
	}
	keys := func(k ...string) {
		t.Helper()
		if out, err := tm(append([]string{"send-keys", "-t", "tui"}, k...)...); err != nil {
			t.Fatalf("send-keys: %v %s", err, out)
		}
	}
	runPalette := func(id string) {
		t.Helper()
		keys("p")
		waitPane("the palette never opened", func(s string) bool { return strings.Contains(s, "context") })
		keys("-l", id)
		time.Sleep(300 * time.Millisecond)
		keys("Enter")
	}
	waitPane("the grid never showed the thread", func(s string) bool { return strings.Contains(s, "edit-me") && strings.Contains(s, "◆") })

	runPalette("context-edit")
	if !waitUntil(30*time.Second, func() bool { return showContext(t, sb, th.ID).Text == "NEW CONTEXT\nsecond line" }) {
		t.Fatalf("the edited file never became the context (now %q); pane:\n%s", showContext(t, sb, th.ID).Text, capture())
	}
	if b, _ := os.ReadFile(seen); string(b) != "OLD CONTEXT" {
		t.Fatalf("the editor was given %q, want the current context", b)
	}
	waitPane("the TUI did not come back after the editor exited", func(s string) bool {
		return strings.Contains(s, "edit-me") && strings.Contains(s, "CTX") && strings.Contains(s, "context of edit-me set")
	})

	// An emptied file removes the context, and the mark goes.
	if err := os.WriteFile(next, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPalette("context-edit")
	if !waitUntil(30*time.Second, func() bool { return showContext(t, sb, th.ID).Hash == "" }) {
		t.Fatalf("an emptied file did not remove the context; pane:\n%s", capture())
	}
	waitPane("the mark did not go after the removal", func(s string) bool {
		return strings.Contains(lineWith(s, "edit-me"), "edit-me") && !strings.Contains(lineWith(s, "edit-me"), "◆")
	})
}
