package daemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestConfirmAgentLaunched proves the spawn-verification safety net: a launch that exits
// immediately (the corkboard bug: `claude --resume` refusing a session another agent holds)
// is reported as a LOUD error carrying the pane's last output, while a launch that stays up
// passes. This is the mechanism that turns a silent bad revive into a visible failure.
func TestConfirmAgentLaunched(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	sock := "seshverify-" + strings.ReplaceAll(t.Name(), "/", "_")
	// An empty base session so the server exists; each case creates its own marked session.
	if _, err := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-s", "base", "-x", "80", "-y", "20").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	d := &Daemon{store: st, tmux: tmux.NewServer(sock)}
	window := 2 * time.Second

	// (1) Immediate exit: a lone-pane session whose command prints a reason then exits ⇒
	// the pane (and session) self-destructs. The marker is stamped while it is briefly alive.
	if err := d.tmux.CreateSessionCmd("dies", "", nil,
		`sh -c 'echo SESSION-HELD-BY-BG-AGENT; sleep 0.4; exit 1'`); err != nil {
		t.Fatalf("create dying session: %v", err)
	}
	pane, err := d.tmux.SessionFirstPane("dies")
	if err != nil {
		t.Fatalf("first pane: %v", err)
	}
	if err := d.tmux.SetPaneThreadID(pane, "tid-dies"); err != nil {
		t.Fatalf("stamp marker: %v", err)
	}
	err = d.confirmAgentLaunchedWithin("tid-dies", window)
	if err == nil {
		t.Fatal("confirmAgentLaunched on an immediately-exiting spawn returned nil — the silent-success bug")
	}
	if !strings.Contains(err.Error(), "exited immediately") {
		t.Errorf("error = %q, want it to mention the immediate exit", err.Error())
	}
	if !strings.Contains(err.Error(), "SESSION-HELD-BY-BG-AGENT") {
		t.Errorf("error = %q, want it to carry the pane's last output", err.Error())
	}

	// (2) Stays up: a long-lived command keeps its pane for the whole window ⇒ nil (up).
	if err := d.tmux.CreateSessionCmd("lives", "", nil, `sh -c 'echo UP; sleep 30'`); err != nil {
		t.Fatalf("create live session: %v", err)
	}
	livePane, err := d.tmux.SessionFirstPane("lives")
	if err != nil {
		t.Fatalf("first pane: %v", err)
	}
	if err := d.tmux.SetPaneThreadID(livePane, "tid-lives"); err != nil {
		t.Fatalf("stamp marker: %v", err)
	}
	if err := d.confirmAgentLaunchedWithin("tid-lives", window); err != nil {
		t.Errorf("confirmAgentLaunched on a healthy long-lived spawn = %v, want nil", err)
	}
	d.tmux.KillSession("lives") //nolint:errcheck
}
