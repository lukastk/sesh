package conformance

// thread.parent cells (PARITY_ROADMAP A5): parent/child records against the
// REAL daemon — explicit --parent, the inside-a-thread inference default
// (F1's resolver), --no-parent, unknown-parent + cycle-guard loud refusals,
// and reparent. Remote = routed creation with --parent on the peer.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.parent", matrix.AgentAgnostic, matrix.Local, testParentLocal)
	matrix.RegisterTest("thread.parent", matrix.AgentAgnostic, matrix.Remote, testParentRemote)
}

func threadByName(t *testing.T, sb *Sandbox, name string) api.Thread {
	t.Helper()
	for _, th := range sb.listThreads(t) {
		if th.Name == name {
			return th
		}
	}
	t.Fatalf("thread %q not found", name)
	return api.Thread{}
}

func testParentLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	root := sb.newHeadlessThread(t, "pi", "rootth")

	// Explicit --parent (prefix form exercises F1's resolver too).
	if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "child1", "--cwd", "/tmp", "--headless", "--parent", root.ID[:8]); err != nil {
		t.Fatalf("new --parent: %v\n%s", err, stderr)
	}
	c1 := threadByName(t, sb, "child1")
	if c1.Parent != root.ID {
		t.Fatalf("child1 parent = %q, want %s", c1.Parent, root.ID)
	}

	// Inference: `new` run "inside" the root (its env carrier) defaults to it.
	if _, stderr, err := runWithEnv(t, sb, map[string]string{"SESH_THREAD_ID": root.ID},
		"thread", "new", "--agent", "pi", "--name", "child2", "--cwd", "/tmp", "--headless"); err != nil {
		t.Fatalf("new inferred parent: %v\n%s", err, stderr)
	}
	if got := threadByName(t, sb, "child2").Parent; got != root.ID {
		t.Errorf("inferred parent = %q, want %s", got, root.ID)
	}

	// --no-parent forces a root even inside a thread.
	if _, stderr, err := runWithEnv(t, sb, map[string]string{"SESH_THREAD_ID": root.ID},
		"thread", "new", "--agent", "pi", "--name", "rooted", "--cwd", "/tmp", "--headless", "--no-parent"); err != nil {
		t.Fatalf("new --no-parent: %v\n%s", err, stderr)
	}
	if got := threadByName(t, sb, "rooted").Parent; got != "" {
		t.Errorf("--no-parent still got parent %q", got)
	}

	// Unknown parent: loud.
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "orphan", "--cwd", "/tmp", "--headless", "--parent", "ffffffff"); err == nil {
		t.Errorf("unknown --parent succeeded silently")
	}

	// Reparent: child1 -> child2; then a CYCLE attempt (root under child1's
	// subtree... root <- child2 <- child1: reparent root under child1 = cycle).
	c2 := threadByName(t, sb, "child2")
	if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", c1.ID, "--parent", c2.ID); err != nil {
		t.Fatalf("reparent: %v\n%s", err, stderr)
	}
	if got := threadByName(t, sb, "child1").Parent; got != c2.ID {
		t.Errorf("reparent did not persist: %q", got)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", root.ID, "--parent", c1.ID); err == nil {
		t.Errorf("cycle reparent succeeded silently")
	} else if !strings.Contains(stderr, "cycle") {
		t.Errorf("cycle error does not say so: %s", stderr)
	}
	// --root detaches.
	if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", c1.ID, "--root"); err != nil {
		t.Fatalf("reparent --root: %v\n%s", err, stderr)
	}
	if got := threadByName(t, sb, "child1").Parent; got != "" {
		t.Errorf("--root did not detach: %q", got)
	}
}

func testParentRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)
	root := sb.newHeadlessThread(t, "pi", "rroot")

	if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "rchild", "--cwd", "/tmp", "--headless", "--parent", root.ID); err != nil {
		t.Fatalf("routed new --parent: %v\n%s", err, stderr)
	}
	out, _, err := sb.Runner.Run(t, "thread", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		var th api.Thread
		if dec.Decode(&th) != nil {
			break
		}
		if th.Name == "rchild" && th.Parent == root.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("routed child's parent not set on the peer")
	}
}
