package conformance

// thread.info cells (PARITY_ROADMAP F1): `sesh info` + the current-thread
// inference resolver, against REAL threads. Local exercises all four
// resolution sources (explicit prefix, $SESH_THREAD_ID, the calling pane's
// birth-stamp, and the loud no-context error) plus a retrofitted verb
// (`thread status` with no --id). Remote = routed `info --id … --machine peer`.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.info", matrix.AgentAgnostic, matrix.Local, testInfoLocal)
	matrix.RegisterTest("thread.info", matrix.AgentAgnostic, matrix.Remote, testInfoRemote)
}

// runWithEnv runs the sesh binary against the sandbox with EXTRA env vars —
// the inference carriers ($SESH_THREAD_ID, $TMUX, $TMUX_PANE) a Runner can't
// inject.
func runWithEnv(t *testing.T, sb *Sandbox, extra map[string]string, args ...string) (string, string, error) {
	t.Helper()
	lr, ok := sb.Runner.(*localRunner)
	if !ok {
		t.Fatalf("runWithEnv needs a local sandbox")
	}
	env := map[string]string{}
	for k, v := range lr.env {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	cmd := exec.Command(lr.bin, args...)
	cmd.Env = sandboxEnv(env)
	return runCmd(cmd)
}

func testInfoLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "describeme", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")
	other := sb.newHeadlessThread(t, "pi", "otherthread")

	// 1. Explicit unique prefix (tid8) — full description of the right thread.
	out, stderr, err := sb.Runner.Run(t, "info", th.ID[:8])
	if err != nil {
		t.Fatalf("info <prefix>: %v\n%s", err, stderr)
	}
	for _, want := range []string{th.ID, "describeme", "pi", "headful", "tmux:"} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}

	// JSON form round-trips the same identity.
	out, _, err = sb.Runner.Run(t, "info", "--id", th.ID, "--json")
	if err != nil {
		t.Fatalf("info --json: %v", err)
	}
	var js struct {
		Thread struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"thread"`
		Head string `json:"head"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &js); err != nil {
		t.Fatalf("info --json decode: %v\n%s", err, out)
	}
	if js.Thread.ID != th.ID || js.Head != "headful" {
		t.Errorf("info --json = %+v, want id %s headful", js, th.ID)
	}

	// 2. Unknown + ambiguous prefixes are loud.
	if _, stderr, err := sb.Runner.Run(t, "info", "zzzzzzzz"); err == nil {
		t.Errorf("unknown prefix succeeded silently")
	} else if !strings.Contains(stderr, "zzzzzzzz") {
		t.Errorf("unknown-prefix error does not echo the ref: %s", stderr)
	}

	// 3. $SESH_THREAD_ID (the env every spawned pane/turn carries).
	out, stderr, err = runWithEnv(t, sb, map[string]string{"SESH_THREAD_ID": other.ID}, "info")
	if err != nil {
		t.Fatalf("info via env: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "otherthread") {
		t.Errorf("env inference described the wrong thread:\n%s", out)
	}

	// 3b. A STALE env id falls through (here: to no-context, loud) — never a
	// silent wrong answer.
	if _, stderr, err := runWithEnv(t, sb, map[string]string{"SESH_THREAD_ID": "00000000-dead-beef-0000-000000000000"}, "info"); err == nil {
		t.Errorf("stale env id succeeded silently")
	} else if !strings.Contains(stderr, "not inside a sesh thread") {
		t.Errorf("stale-env failure not the loud no-context error: %s", stderr)
	}

	// 4. The calling pane's birth-stamp: $TMUX (socket path) + $TMUX_PANE of
	// the REAL agent pane resolve to its thread.
	sockPath := tmuxSocketPath(sb.TmuxSocket)
	out, stderr, err = runWithEnv(t, sb,
		map[string]string{"TMUX": sockPath + ",1,0", "TMUX_PANE": pane}, "info")
	if err != nil {
		t.Fatalf("info via pane: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "describeme") {
		t.Errorf("pane inference described the wrong thread:\n%s", out)
	}

	// 5. A retrofitted verb infers identically: `thread status` with no --id.
	out, stderr, err = runWithEnv(t, sb, map[string]string{"SESH_THREAD_ID": th.ID}, "thread", "status")
	if err != nil {
		t.Fatalf("thread status inferred: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "head:") {
		t.Errorf("inferred thread status output wrong:\n%s", out)
	}

	// 6. No context at all: loud.
	if _, stderr, err := sb.Runner.Run(t, "info"); err == nil {
		t.Errorf("info with no context succeeded silently")
	} else if !strings.Contains(stderr, "not inside a sesh thread") {
		t.Errorf("no-context error wrong: %s", stderr)
	}
}

func testInfoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "remoteinfo")

	// Routed: the description comes from the PEER's daemon over the real hop.
	out, stderr, err := sb.Runner.Run(t, "info", "--id", th.ID)
	if err != nil {
		t.Fatalf("routed info: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "remoteinfo") || !strings.Contains(out, th.ID) {
		t.Errorf("routed info wrong:\n%s", out)
	}
}

// tmuxSocketPath is where `tmux -L name` puts its socket.
func tmuxSocketPath(name string) string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), name)
}
