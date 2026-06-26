package daemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestMaintainerIdleEarlyOut proves the per-tick fork/exec cost (tmux pane
// enumeration + `ps` process snapshot) is skipped when there is nothing to probe.
// This is the fix for the termux battery drain: with zero local threads the daemon
// must NOT spawn tmux+ps every ~300ms, and with only headless/idle threads (no
// marked pane) it must skip the `ps` snapshot.
func TestMaintainerIdleEarlyOut(t *testing.T) {
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

	// An isolated, empty tmux server: it has only its own default (unmarked) session,
	// so PaneIndexByThreadID returns no thread-marked panes.
	sock := "seshmaint-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	if _, err := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-s", "idle", "-x", "80", "-y", "20").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	d := &Daemon{store: st, tmux: tmux.NewServer(sock)}
	m := newMaintainer(d)

	// (1) Zero threads: the early-out fires BEFORE any tmux/ps call.
	m.tick()
	if m.probedPanes != 0 || m.probedProcs != 0 {
		t.Fatalf("zero-thread tick probed panes=%d procs=%d, want 0/0 (early-out)", m.probedPanes, m.probedProcs)
	}

	// (2) A headless thread (no marked pane): panes are enumerated to discover it has
	// none, but the `ps` snapshot is skipped (nothing occupies a pane).
	if err := st.InsertThread(api.Thread{ID: "tid-headless", Machine: "test", SessionName: "headless-tid", AgentKind: "pi"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	m.tick()
	if m.probedPanes != 1 {
		t.Fatalf("headless tick probedPanes=%d, want 1 (panes enumerated)", m.probedPanes)
	}
	if m.probedProcs != 0 {
		t.Fatalf("headless tick probedProcs=%d, want 0 (no marked pane => ps skipped)", m.probedProcs)
	}
	// The thread resolves to headless·idle without the process table.
	snap, ok := m.stateOf("tid-headless")
	if !ok {
		t.Fatalf("no snapshot for headless thread")
	}
	if snap.Head != api.Headless || snap.Busy != api.BusyIdle {
		t.Fatalf("headless thread head=%v busy=%v, want headless/idle", snap.Head, snap.Busy)
	}
}
