package conformance

// thread.parent cells (PARITY_ROADMAP A5): parent/child records against the
// REAL daemon — explicit --parent, the inside-a-thread inference default
// (F1's resolver), --no-parent, unknown-parent + cycle-guard loud refusals,
// and reparent. Remote = routed creation with --parent on the peer.

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	// The root thread gets its OWN directory rather than the shared /tmp: env
	// inference is corroborated against the caller's cwd, and a thread parked at
	// /tmp CONTAINS every t.TempDir(), so the contradiction case below could
	// never be staged against it (it would read as corroboration and pass
	// vacuously). rootCwd and strayCwd are unrelated siblings under one base.
	base := t.TempDir()
	rootCwd := filepath.Join(base, "rootthread")
	strayCwd := filepath.Join(base, "elsewhere")
	for _, d := range []string{rootCwd, strayCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	root := sb.newHeadlessThreadAt(t, "pi", "rootth", rootCwd)

	// Explicit --parent (prefix form exercises F1's resolver too).
	if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "child1", "--cwd", "/tmp", "--headless", "--parent", root.ID[:8]); err != nil {
		t.Fatalf("new --parent: %v\n%s", err, stderr)
	}
	c1 := threadByName(t, sb, "child1")
	if c1.Parent != root.ID {
		t.Fatalf("child1 parent = %q, want %s", c1.Parent, root.ID)
	}

	// Inference: `new` run "inside" the root (its env carrier) defaults to it.
	// Run it from the root's OWN cwd, which is where a real agent of that thread
	// stands — an env-derived id is corroborated against the caller's directory
	// (ticket d7be88ef), so standing anywhere else is a different scenario, tested
	// immediately below. The inferred parent must also be ANNOUNCED (6ea1f6eb):
	// a silent mis-parent once went unnoticed for ten hours.
	_, stderr, err := runWithEnvDir(t, sb, root.Cwd, map[string]string{"SESH_THREAD_ID": root.ID},
		"thread", "new", "--agent", "pi", "--name", "child2", "--cwd", "/tmp", "--headless")
	if err != nil {
		t.Fatalf("new inferred parent: %v\n%s", err, stderr)
	}
	if got := threadByName(t, sb, "child2").Parent; got != root.ID {
		t.Errorf("inferred parent = %q, want %s", got, root.ID)
	}
	if !strings.Contains(stderr, "parenting under") || !strings.Contains(stderr, root.ID[:8]) {
		t.Errorf("inferred parent not announced on stderr (want it to name %s): %q", root.ID[:8], stderr)
	}

	// A CONTRADICTED env id must NOT parent silently: standing in a directory
	// unrelated to the named thread's cwd is what a detached background job looks
	// like, and its inherited id names someone else's thread. Refuse to infer,
	// say so, and make a ROOT — never a silent child of a stranger.
	_, stderr, err = runWithEnvDir(t, sb, strayCwd, map[string]string{"SESH_THREAD_ID": root.ID},
		"thread", "new", "--agent", "pi", "--name", "notinferred", "--cwd", "/tmp", "--headless")
	if err != nil {
		t.Fatalf("new from a contradicted cwd should still create a ROOT thread: %v\n%s", err, stderr)
	}
	if got := threadByName(t, sb, "notinferred").Parent; got != "" {
		t.Errorf("a contradicted env id silently parented the new thread under %q — the mis-parent bug", got)
	}
	if !strings.Contains(stderr, "NOT inferring a parent") {
		t.Errorf("refusing to infer must be loud, not silent: %q", stderr)
	}

	// --no-parent forces a root even inside a thread.
	if _, stderr, err := runWithEnvDir(t, sb, root.Cwd, map[string]string{"SESH_THREAD_ID": root.ID},
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
