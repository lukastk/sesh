package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

// TestMarkerScopesDoNotCollide is the regression guard for the trap that shapes
// the whole shell-thread design.
//
// tmux resolves user options with INHERITANCE during format expansion — pane →
// window → session → global, starting from the deepest object in the format's
// context. So if the SESSION marker reused the pane key (@sesh-thread-id), every
// UNMARKED pane in a shell thread's session would report the shell thread's id,
// and FindPaneByThreadID / ThreadIDOfPane / `tmux current` / adopt's ownership
// guard / nav's window resolution would each return a plausible-but-wrong
// answer. The two keys are distinct precisely to prevent that.
//
// This test would fail loudly if anyone ever "simplified" them back into one.
func TestMarkerScopesDoNotCollide(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshshellid-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	tx := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := tx("-f", "/dev/null", "new-session", "-d", "-s", "boxsess", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	// A second window, so the session has an unmarked pane besides the one we
	// stamp — the inheritance bug shows up on exactly those.
	if _, err := tx("new-window", "-t", "boxsess"); err != nil {
		t.Fatalf("new-window: %v", err)
	}

	s := NewServer(sock)
	const shellThread = "shell-thread-id"
	const agentThread = "agent-thread-id"

	if err := s.StampSessionShellID("boxsess", shellThread); err != nil {
		t.Fatalf("stamp shell id: %v", err)
	}
	// Stamp exactly ONE pane as an agent thread's.
	panes, err := s.Info("")
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if len(panes) != 1 || len(panes[0].Windows) != 2 {
		t.Fatalf("fixture wrong: %+v", panes)
	}
	marked := panes[0].Windows[0].Panes[0].Pane
	if err := s.StampPaneThreadID(marked, agentThread); err != nil {
		t.Fatalf("stamp pane: %v", err)
	}

	sessions, err := s.Info("")
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	sess := sessions[0]

	// The session carries the SHELL id...
	if sess.ShellID != shellThread {
		t.Fatalf("session shell id = %q, want %q", sess.ShellID, shellThread)
	}
	// ...and NO pane inherits it as its thread id. This is the assertion that
	// would have caught the bug: with one shared key, every unmarked pane below
	// reports the session's value.
	marks := map[string]string{}
	unmarked := 0
	for _, w := range sess.Windows {
		for _, p := range w.Panes {
			marks[p.Pane] = p.ThreadID
			if p.ThreadID == "" {
				unmarked++
			}
		}
	}
	if marks[marked] != agentThread {
		t.Fatalf("the stamped pane reports %q, want %q", marks[marked], agentThread)
	}
	if unmarked == 0 {
		t.Fatalf("fixture wrong: no unmarked pane to test inheritance against (marks=%v)", marks)
	}
	for pane, id := range marks {
		if pane == marked {
			continue
		}
		if id != "" {
			t.Fatalf("pane %s reports thread id %q — it inherited the SESSION's marker; the session key must not be @sesh-thread-id", pane, id)
		}
	}

	// Resolution by each marker finds the right thing, independently.
	if got, ok, err := s.FindPaneByThreadID(agentThread); err != nil || !ok || got.Pane != marked {
		t.Fatalf("FindPaneByThreadID = (%+v, %v, %v), want the stamped pane %s", got, ok, err, marked)
	}
	if _, ok, err := s.FindPaneByThreadID(shellThread); err != nil || ok {
		t.Fatalf("FindPaneByThreadID resolved the SHELL id to a pane (ok=%v, err=%v) — the shell marker must be invisible to pane resolution", ok, err)
	}
	if got, ok, err := s.FindSessionByShellID(shellThread); err != nil || !ok || got.Name != "boxsess" {
		t.Fatalf("FindSessionByShellID = (%+v, %v, %v), want boxsess", got, ok, err)
	}
	if _, ok, err := s.FindSessionByShellID(agentThread); err != nil || ok {
		t.Fatalf("FindSessionByShellID resolved the AGENT id to a session (ok=%v, err=%v)", ok, err)
	}

	// Unstamping returns the session to an untracked ghost.
	if err := s.UnstampSessionShellID("boxsess"); err != nil {
		t.Fatalf("unstamp: %v", err)
	}
	if _, ok, err := s.FindSessionByShellID(shellThread); err != nil || ok {
		t.Fatalf("session still resolves after unstamp (ok=%v, err=%v)", ok, err)
	}
	// A rename must NOT lose the marker (identity is the marker, not the name).
	if err := s.StampSessionShellID("boxsess", shellThread); err != nil {
		t.Fatalf("re-stamp: %v", err)
	}
	if _, err := tx("rename-session", "-t", "boxsess", "renamed"); err != nil {
		t.Fatalf("rename-session: %v", err)
	}
	got, ok, err := s.FindSessionByShellID(shellThread)
	if err != nil || !ok || got.Name != "renamed" {
		t.Fatalf("after rename: FindSessionByShellID = (%+v, %v, %v), want the renamed session", got, ok, err)
	}
}
