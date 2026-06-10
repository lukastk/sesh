package conformance

// thread.meta cells (PARITY_ROADMAP D6): arbitrary per-thread KV — set/get/
// unset round-trip the wire, a missing key is LOUD, and the [[tui.views]]
// meta.<key> predicate sees real values. Remote = routed.

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	matrix.RegisterTest("thread.meta", matrix.AgentAgnostic, matrix.Local, testMetaLocal)
	matrix.RegisterTest("thread.meta", matrix.AgentAgnostic, matrix.Remote, testMetaRemote)
}

func testMetaLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "tagged")

	if _, stderr, err := sb.Runner.Run(t, "meta", "set", "stage", "review", "--id", th.ID); err != nil {
		t.Fatalf("meta set: %v\n%s", err, stderr)
	}
	out, _, err := sb.Runner.Run(t, "meta", "get", "stage", "--id", th.ID)
	if err != nil || strings.TrimSpace(out) != "review" {
		t.Fatalf("meta get = %q (%v), want review", out, err)
	}
	// The wire carries it.
	if got := threadByName(t, sb, "tagged").Meta["stage"]; got != "review" {
		t.Errorf("wire meta = %q", got)
	}
	// Predicates see it (both forms).
	for _, src := range []string{`meta.stage == review`, `meta.stage`} {
		p, err := tui.CompilePredicate(src)
		if err != nil {
			t.Fatal(err)
		}
		row := api.ThreadRow{Thread: threadByName(t, sb, "tagged")}
		if !p.Eval(row) {
			t.Errorf("predicate %q false against real meta", src)
		}
	}
	// Missing key: loud. Unset removes.
	if _, _, err := sb.Runner.Run(t, "meta", "get", "nope", "--id", th.ID); err == nil {
		t.Errorf("missing key read silently")
	}
	if _, stderr, err := sb.Runner.Run(t, "meta", "unset", "stage", "--id", th.ID); err != nil {
		t.Fatalf("meta unset: %v\n%s", err, stderr)
	}
	if _, _, err := sb.Runner.Run(t, "meta", "get", "stage", "--id", th.ID); err == nil {
		t.Errorf("unset key still readable")
	}
}

func testMetaRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "rtagged")
	if _, stderr, err := sb.Runner.Run(t, "meta", "set", "owner", "lukas", "--id", th.ID); err != nil {
		t.Fatalf("routed meta set: %v\n%s", err, stderr)
	}
	if got := threadByName(t, sb, "rtagged").Meta["owner"]; got != "lukas" {
		t.Errorf("routed meta = %q", got)
	}
}
