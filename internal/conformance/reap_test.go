package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The reaper is what makes conformance teardown survive a run that DIES — Ctrl-C,
// a -timeout abort, a SIGKILL — where `t.Cleanup` never runs and a leaked sandbox
// keeps a real agent alive forever. These tests pin the two halves that matter:
// it must take the old ones, and it must NOT take a concurrent run's.

func TestReapTakesStaleSocketsAndLeavesFreshOnes(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-24 * time.Hour)
	fresh := time.Now()

	stale := fmt.Sprintf("sesh-test-local-%d", old.UnixNano())
	live := fmt.Sprintf("sesh-test-local-%d", fresh.UnixNano())
	for _, n := range []string{stale, live} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reapTestServersIn(dir, time.Now().Add(-staleTestServerAge))

	if _, err := os.Stat(filepath.Join(dir, stale)); !os.IsNotExist(err) {
		t.Errorf("a socket from a run 24h ago must be reaped, got err=%v", err)
	}
	// A run that started seconds ago may be executing RIGHT NOW in another
	// process; killing its server would break a passing test run.
	if _, err := os.Stat(filepath.Join(dir, live)); err != nil {
		t.Errorf("a socket from a concurrent run must be left alone: %v", err)
	}
}

func TestReapTouchesNothingItDoesNotOwn(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-24 * time.Hour).UnixNano()
	others := []string{
		"sesh",                                    // the real work server
		"sesh-master",                             // the cockpit
		fmt.Sprintf("other-tool-%d", old),         // someone else's socket
		"sesh-test-local-not-a-timestamp",         // ours by prefix, but unparseable
		fmt.Sprintf("sesh-testish-local-%d", old), // prefix lookalike
	}
	for _, n := range others {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reapTestServersIn(dir, time.Now().Add(-staleTestServerAge))

	for _, n := range others {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("reaper removed %q, which is not its to remove: %v", n, err)
		}
	}
}

// The file-level tests above pin the age arithmetic. This one pins the thing that
// actually matters: a REAL leaked tmux server, holding a REAL process the way a
// leaked sandbox holds an agent, is killed.
func TestReapKillsARealLeakedServer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Stamped a day ago so the reaper treats it as a previous run's leak, and
	// uniquely enough that a concurrent run cannot collide with it.
	socket := fmt.Sprintf("sesh-test-local-%d", time.Now().Add(-24*time.Hour).UnixNano())
	// `sleep` stands in for the agent: it is what keeps the server alive, so
	// tmux's own exit-empty can never reclaim it.
	if err := exec.Command("tmux", "-L", socket, "new-session", "-d", "sleep", "600").Run(); err != nil {
		t.Skipf("could not start a tmux server: %v", err)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", socket, "kill-server").Run() }) //nolint:errcheck
	if err := exec.Command("tmux", "-L", socket, "list-sessions").Run(); err != nil {
		t.Fatalf("test server did not come up: %v", err)
	}

	if killed := reapTestServersIn(tmuxSocketDir(), time.Now().Add(-staleTestServerAge)); killed < 1 {
		t.Fatalf("reaper killed %d servers, want at least the one we leaked", killed)
	}

	if err := exec.Command("tmux", "-L", socket, "list-sessions").Run(); err == nil {
		t.Error("the leaked server is still running after a reap")
	}
	if _, err := os.Stat(filepath.Join(tmuxSocketDir(), socket)); !os.IsNotExist(err) {
		t.Errorf("the dead socket file was left behind: err=%v", err)
	}
}

func TestReapSurvivesAMissingSocketDir(t *testing.T) {
	if got := reapTestServersIn(filepath.Join(t.TempDir(), "nope"), time.Now()); got != 0 {
		t.Errorf("a missing socket dir must be a no-op, killed=%d", got)
	}
}
