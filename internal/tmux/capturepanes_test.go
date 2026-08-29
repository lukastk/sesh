package tmux

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func waitPaneContains(t *testing.T, s *Server, pane, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		var err error
		last, err = s.CapturePane(pane)
		if err == nil && strings.Contains(last, needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pane %s never rendered %q; last capture %q", pane, needle, last)
}

// TestCapturePanesMatchesCapturePane: the batched capture must return, per
// pane, EXACTLY what the single-pane CapturePane returns — the content-diff
// busy heuristic compares successive captures, so any formatting drift between
// the two paths would read as pane activity.
func TestCapturePanesMatchesCapturePane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshcap-" + strings.ReplaceAll(t.Name(), "/", "_")
	s := NewServer(sock)
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	if _, err := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-s", "a", "-x", "60", "-y", "40").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	mk := func(cmd string) string {
		out, err := exec.Command("tmux", "-L", sock, "split-window", "-d", "-t", "a", "-P", "-F", "#{pane_id}", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("split: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	p1 := mk("printf 'alpha line one\\nalpha line two\\n'; sleep 60")
	p2 := mk("sleep 60") // a blank pane
	p3 := mk("printf 'gamma\\n'; sleep 60")
	waitPaneContains(t, s, p1, "alpha line two")
	waitPaneContains(t, s, p3, "gamma")
	panes := []string{p1, p2, p3}

	batched, err := s.CapturePanes(panes)
	if err != nil {
		t.Fatalf("CapturePanes: %v", err)
	}
	for _, p := range panes {
		single, err := s.CapturePane(p)
		if err != nil {
			t.Fatalf("CapturePane %s: %v", p, err)
		}
		got, ok := batched[p]
		if !ok {
			t.Fatalf("pane %s missing from the batch", p)
		}
		if got != single {
			t.Fatalf("pane %s: batched %q != single %q — the busy heuristic would read the drift as activity", p, got, single)
		}
	}
	if !strings.Contains(batched[p1], "alpha line two") || !strings.Contains(batched[p3], "gamma") {
		t.Fatalf("contents mis-assigned: %q / %q", batched[p1], batched[p3])
	}
}

// TestCapturePanesVanishedPane: a pane id that no longer exists makes tmux
// ABORT the rest of the command list (measured behavior) — the batch must
// recover the panes after it in a retry, and report the vanished one only by
// absence (the caller's pane-vanished path), never fail the whole tick.
func TestCapturePanesVanishedPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshcapv-" + strings.ReplaceAll(t.Name(), "/", "_")
	s := NewServer(sock)
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	if _, err := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-s", "a", "-x", "60", "-y", "40").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	mk := func(cmd string) string {
		out, err := exec.Command("tmux", "-L", sock, "split-window", "-d", "-t", "a", "-P", "-F", "#{pane_id}", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("split: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	p1 := mk("printf 'one\\n'; sleep 60")
	p2 := mk("printf 'two\\n'; sleep 60")
	waitPaneContains(t, s, p1, "one")
	waitPaneContains(t, s, p2, "two")
	got, err := s.CapturePanes([]string{p1, "%9999", p2})
	if err != nil {
		t.Fatalf("CapturePanes with a vanished pane: %v", err)
	}
	if _, ok := got["%9999"]; ok {
		t.Fatalf("vanished pane present in the result")
	}
	if !strings.Contains(got[p1], "one") || !strings.Contains(got[p2], "two") {
		t.Fatalf("live panes lost around the vanished one: %v", got)
	}
}

// TestProcSnapshotForMatchesPS (linux): the targeted /proc walk must resolve
// the agent under a pane pid exactly as the full `ps` snapshot does — it is
// the same question answered from /proc instead of a 70 ms process-table fork.
func TestProcSnapshotForMatchesPS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc walk is linux-only")
	}
	// A fake agent: argv0 "claude" via symlink (the authoritystale trick),
	// started as a child of this test process.
	dir := t.TempDir()
	sl, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	link := dir + "/claude"
	if err := exec.Command("ln", "-s", sl, link).Run(); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cmd := exec.Command(link, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cmd.Process.Kill() //nolint:errcheck

	self := currentPID()
	viaProc, err := snapshotProcsUnder([]int{self}, 4)
	if err != nil {
		t.Skipf("proc walk unavailable on this kernel: %v", err)
	}
	a1, ok1 := viaProc.findAgent(self, 4)
	full, err := snapshotProcs()
	if err != nil {
		t.Fatalf("ps snapshot: %v", err)
	}
	a2, ok2 := full.findAgent(self, 4)
	if !ok1 || !ok2 || a1.PID != a2.PID || a1.Kind != a2.Kind || a1.Kind != "claude" {
		t.Fatalf("proc walk (%+v, %v) != ps (%+v, %v)", a1, ok1, a2, ok2)
	}
}
