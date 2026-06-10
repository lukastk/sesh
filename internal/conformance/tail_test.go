package conformance

// Free (non-matrix) test for D1's `sesh tail`/`sesh transcript` CLI forms —
// the underlying read is matrix-covered by thread.transcript; this proves the
// v1 ergonomics: positional id, -n bound (default 20), inference via the env
// carrier, and the whole-dump alias.

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func TestTailCLIForms(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "tailed")
	sb.headlessTurn(t, th.ID, "Reply with exactly the word SAFFRON and nothing else")

	out, stderr, err := sb.Runner.Run(t, "tail", th.ID[:8], "-n", "5")
	if err != nil {
		t.Fatalf("tail: %v\n%s", err, stderr)
	}
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n > 5 {
		t.Errorf("tail -n 5 printed %d lines", n)
	}

	out, stderr, err = sb.Runner.Run(t, "transcript", th.ID)
	if err != nil || !strings.Contains(out, "SAFFRON") {
		t.Errorf("transcript dump missing the real reply: %v\n%s", err, stderr)
	}

	// Inference: bare `sesh tail` inside the thread's env context.
	out, stderr, err = runWithEnv(t, sb, map[string]string{"SESH_THREAD_ID": th.ID}, "tail")
	if err != nil || !strings.Contains(out, "SAFFRON") {
		t.Errorf("inferred tail wrong: %v\n%s", err, stderr)
	}
}
