package conformance

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// daemon.lifecycle (agent-agnostic, local + remote) is implemented; bind a
	// real test per locality. Removed from skipAll in cells_test.go.
	matrix.RegisterTest("daemon.lifecycle", matrix.AgentAgnostic, matrix.Local,
		func(t *testing.T) { testDaemonLifecycle(t, matrix.Local) })
	matrix.RegisterTest("daemon.lifecycle", matrix.AgentAgnostic, matrix.Remote,
		func(t *testing.T) { testDaemonLifecycle(t, matrix.Remote) })
}

// testDaemonLifecycle exercises start/status/stop against the real sesh binary,
// asserting the *observable external effects*: a real daemon process appears and
// answers after start, and is gone (process dead, socket unreachable) after stop
// — both directions, because a one-directional check is exactly the v1 codex
// liveness bug.
func testDaemonLifecycle(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	r := sb.Runner

	// --- start ---
	if _, stderr, err := r.Run(t, "daemon", "start"); err != nil {
		t.Fatalf("[%s] daemon start failed: %v\n%s", loc, err, stderr)
	}

	// --- status: the daemon answers, reports our machine + schema, and its pid
	// is a real, live process on the box ---
	st := mustStatus(t, r)
	if st.Machine != sb.Machine {
		t.Errorf("[%s] status machine = %q, want %q", loc, st.Machine, sb.Machine)
	}
	if st.SchemaVersion != 1 {
		t.Errorf("[%s] schema_version = %d, want 1", loc, st.SchemaVersion)
	}
	if st.Schema != api.SchemaVersion {
		t.Errorf("[%s] api schema = %d, want %d", loc, st.Schema, api.SchemaVersion)
	}
	if !pidAlive(st.PID) {
		t.Fatalf("[%s] daemon reported pid %d but no such live process", loc, st.PID)
	}
	pid := st.PID

	// Starting again must fail loudly (single writer), not silently spawn a
	// second daemon on the same store.
	if _, _, err := r.Run(t, "daemon", "start"); err == nil {
		t.Errorf("[%s] second `daemon start` unexpectedly succeeded", loc)
	}

	// --- stop ---
	if _, stderr, err := r.Run(t, "daemon", "stop"); err != nil {
		t.Fatalf("[%s] daemon stop failed: %v\n%s", loc, err, stderr)
	}

	// --- observable effect of stop: the process actually dies and status no
	// longer answers (the other direction of the liveness check) ---
	if !waitUntil(5*time.Second, func() bool { return !pidAlive(pid) }) {
		t.Errorf("[%s] daemon pid %d still alive after stop", loc, pid)
	}
	if _, _, err := r.Run(t, "daemon", "status"); err == nil {
		t.Errorf("[%s] `daemon status` still succeeds after stop", loc)
	}
}

// mustStatus runs `daemon status --json` and decodes it, failing the test on any
// error.
func mustStatus(t *testing.T, r Runner) api.StatusResponse {
	t.Helper()
	stdout, stderr, err := r.Run(t, "daemon", "status", "--json")
	if err != nil {
		t.Fatalf("daemon status --json failed: %v\n%s", err, stderr)
	}
	var st api.StatusResponse
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatalf("decode status json: %v\nraw: %s", err, stdout)
	}
	return st
}
