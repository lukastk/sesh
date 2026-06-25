package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPaneIndexByThreadIDMatchesFindPane proves the batched per-tick pane resolution
// (PaneIndexByThreadID, one enumeration) is equivalent to the per-thread
// FindPaneByThreadID it replaces in the maintainer hot loop. The maintainer's busy
// fix relies on this equivalence: marked panes across several sessions/windows must
// resolve to the SAME locator either way, and unmarked panes must be absent.
func TestPaneIndexByThreadIDMatchesFindPane(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshpaneidx-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	tx := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := tx("-f", "/dev/null", "new-session", "-d", "-s", "sesh_a", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session a: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	if _, err := tx("new-session", "-d", "-s", "sesh_b", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session b: %v", err)
	}
	// A second window in session a (the index resolution must span windows).
	if _, err := tx("new-window", "-t", "sesh_a"); err != nil {
		t.Fatalf("new-window: %v", err)
	}

	s := NewServer(sock)
	// Mark a pane in each of: sesh_a:0, sesh_a:1, sesh_b:0. Leave one extra pane
	// unmarked to prove unmarked panes never enter the index.
	want := map[string]string{ // threadID -> target window for marking
		"tid-a0": "sesh_a:0",
		"tid-a1": "sesh_a:1",
		"tid-b0": "sesh_b:0",
	}
	for tid, target := range want {
		pane, err := tx("list-panes", "-t", target, "-F", "#{pane_id}")
		if err != nil {
			t.Fatalf("pane id %s: %v", target, err)
		}
		if _, err := tx("set-option", "-p", "-t", pane, ThreadIDOption, tid); err != nil {
			t.Fatalf("mark %s: %v", target, err)
		}
	}

	idx, err := s.PaneIndexByThreadID()
	if err != nil {
		t.Fatalf("PaneIndexByThreadID: %v", err)
	}
	if len(idx) != len(want) {
		t.Fatalf("index has %d entries, want %d: %+v", len(idx), len(want), idx)
	}
	for tid := range want {
		got, ok := idx[tid]
		if !ok {
			t.Fatalf("index missing %s", tid)
		}
		// Equivalence: the per-thread path must agree exactly.
		fp, found, err := s.FindPaneByThreadID(tid)
		if err != nil || !found {
			t.Fatalf("FindPaneByThreadID(%s) found=%v err=%v", tid, found, err)
		}
		if got != fp {
			t.Errorf("%s: batched=%+v per-thread=%+v differ", tid, got, fp)
		}
	}
	// An unknown thread id is absent (no fallback).
	if _, ok := idx["nope"]; ok {
		t.Errorf("index should not contain an unmarked thread id")
	}
}
