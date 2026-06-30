package conformance

// thread.delegate cells (PARITY_ROADMAP C2): the ephemeral one-shot worker —
// a REAL agent answers a real task and the worker is ARCHIVED afterwards (the
// ephemeral contract, both directions: hidden from the active list yet RETAINED
// in the archived list after success AND after a turn failure — never deleted).
// --keep leaves a usable, active thread. Remote = the whole verb routed: the
// worker spawns, answers and is archived on the PEER.

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range []matrix.Agent{matrix.Claude, matrix.Codex, matrix.Pi} {
		a := agent
		matrix.RegisterTest("thread.delegate", a, matrix.Local,
			func(t *testing.T) { testDelegate(t, string(a), matrix.Local) })
		matrix.RegisterTest("thread.delegate", a, matrix.Remote,
			func(t *testing.T) { testDelegate(t, string(a), matrix.Remote) })
	}
}

func testDelegate(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	// One real one-shot task; the sentinel proves a REAL agent answered.
	out, stderr, err := sb.Runner.Run(t, "delegate", "--agent", agent, "--cwd", "/tmp",
		"Reply with exactly the word CHARTREUSE and nothing else")
	if err != nil {
		t.Fatalf("[%s/%s] delegate: %v\n%s", agent, loc, err, stderr)
	}
	if !strings.Contains(out, "CHARTREUSE") {
		t.Fatalf("[%s/%s] reply missing the sentinel: %q", agent, loc, out)
	}
	// Ephemeral, but RETAINED: the worker is gone from the active list yet
	// present (archived) in the archived list — never deleted.
	for _, th := range sb.listThreads(t) {
		if strings.HasPrefix(th.Name, "delegate-") {
			t.Errorf("[%s/%s] worker %s still active after a successful delegate (should be archived)", agent, loc, th.ID)
		}
	}
	if !delegateWorkerArchived(t, sb) {
		t.Errorf("[%s/%s] no archived delegate worker after success (it was deleted, not archived)", agent, loc)
	}

	if agent != "pi" {
		return // the keep + edge cases are agent-independent: pi owns them per locality
	}

	// --keep retains a REAL, follow-up-able thread.
	out, stderr, err = sb.Runner.Run(t, "delegate", "--agent", "pi", "--cwd", "/tmp",
		"--keep", "--name", "kept-worker", "Reply with exactly: KEPT")
	if err != nil {
		t.Fatalf("delegate --keep: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "KEPT") {
		t.Errorf("--keep reply wrong: %q", out)
	}
	kept := threadByName(t, sb, "kept-worker")
	// The kept worker is a normal headless thread: a SECOND turn remembers.
	sb.headlessTurn(t, kept.ID, "What word did I ask you to reply with? Answer with just that word.")
	if r := sb.headlessReply(t, kept.ID); !strings.Contains(r.Reply, "KEPT") {
		t.Errorf("kept worker lost the conversation: %q", r.Reply)
	}

	// Missing task/agent: loud. --sandbox: loud NOT IMPLEMENTED (roadmap E3).
	if _, _, err := sb.Runner.Run(t, "delegate", "--agent", "pi"); err == nil {
		t.Errorf("delegate without a task succeeded")
	}
	// E3: --sandbox on a pi worker is the loud pi-cannot-sandbox refusal (and
	// the worker is cleaned up, not leaked).
	if _, stderr, err := sb.Runner.Run(t, "delegate", "--agent", "pi", "--sandbox", "task"); err == nil {
		t.Errorf("--sandbox pi delegate succeeded (pi cannot be sandboxed)")
	} else if !strings.Contains(stderr, "cannot be sandboxed") {
		t.Errorf("--sandbox pi error wrong: %s", stderr)
	}
	for _, th := range sb.listThreads(t) {
		if strings.HasPrefix(th.Name, "delegate-") {
			t.Errorf("sandbox-refused worker left active: %s (should be archived)", th.ID)
		}
	}
}

// delegateWorkerArchived reports whether an archived worker thread (name
// delegate-*) exists — the retained-not-deleted half of the ephemeral contract.
func delegateWorkerArchived(t *testing.T, sb *Sandbox) bool {
	t.Helper()
	for _, th := range sb.listThreadsArchived(t) {
		if strings.HasPrefix(th.Name, "delegate-") && th.Archived {
			return true
		}
	}
	return false
}
