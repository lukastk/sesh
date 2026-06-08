package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// Phase 3a: local thread cells that are testable cheaply at idle (no API
	// turn needed) — for all three real agents. runtime-state, send.* and
	// new.headless need a real turn and land in Phase 3b. Remote needs the mesh
	// (Phase 4).
	for _, a := range matrix.AllAgents {
		a := a
		matrix.RegisterTest("thread.new.headed", a, matrix.Local,
			func(t *testing.T) { testThreadNewHeaded(t, string(a)) })
		matrix.RegisterTest("thread.kill", a, matrix.Local,
			func(t *testing.T) { testThreadKill(t, string(a)) })
		matrix.RegisterTest("thread.resolve-pane", a, matrix.Local,
			func(t *testing.T) { testThreadResolvePane(t, string(a)) })
	}
	matrix.RegisterTest("thread.list", matrix.AgentAgnostic, matrix.Local, testThreadList)
}

// agentStartTimeout is generous: a real agent (esp. node-wrapped codex) takes a
// moment for its process to appear under the pane.
const agentStartTimeout = 20 * time.Second

// testThreadNewHeaded spawns a real agent in a real tmux pane and asserts the
// OBSERVABLE facts: the session exists, the pane carries the thread marker,
// SESH_THREAD_ID is injected into the pane env, and a REAL agent process of the
// right kind is actually running under the pane (independent ps walk).
func testThreadNewHeaded(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "t1", "/tmp")
	if th.AgentKind != agent {
		t.Errorf("agent_kind = %q, want %q", th.AgentKind, agent)
	}
	if th.SessionName != "sesh_t1" {
		t.Errorf("session_name = %q, want sesh_t1", th.SessionName)
	}
	if th.Machine != sb.Machine {
		t.Errorf("machine = %q, want %q", th.Machine, sb.Machine)
	}

	if out, err := sb.rawTmux(t, "has-session", "-t", "=sesh_t1"); err != nil {
		t.Fatalf("tmux has no session sesh_t1: %v\n%s", err, out)
	}

	var panePID int
	if !waitUntil(agentStartTimeout, func() bool {
		_, pid, ok := sb.markedPane(t, th.ID)
		panePID = pid
		return ok
	}) {
		t.Fatalf("no pane carries the thread marker %s", th.ID)
	}

	env, err := sb.rawTmux(t, "show-environment", "-t", "=sesh_t1", "SESH_THREAD_ID")
	if err != nil {
		t.Fatalf("show-environment: %v\n%s", err, env)
	}
	if !strings.Contains(env, "SESH_THREAD_ID="+th.ID) {
		t.Errorf("SESH_THREAD_ID not injected; got %q", strings.TrimSpace(env))
	}

	if !waitUntil(agentStartTimeout, func() bool { return agentRunningUnder(panePID, agent) }) {
		t.Fatalf("no real %q process running under pane pid %d", agent, panePID)
	}
}

// testThreadKill asserts both directions: the agent is genuinely alive after
// spawn, and genuinely dead (process gone, session gone, record gone) after
// kill — the symmetry the v1 one-directional codex check lacked.
func testThreadKill(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "killme", "/tmp")

	var panePID int
	if !waitUntil(agentStartTimeout, func() bool {
		_, pid, ok := sb.markedPane(t, th.ID)
		panePID = pid
		return ok && agentRunningUnder(pid, agent)
	}) {
		t.Fatalf("agent never came up for kill test")
	}
	// Alive direction.
	if !pidAlive(panePID) || !agentRunningUnder(panePID, agent) {
		t.Fatalf("precondition: agent should be alive before kill")
	}

	if _, stderr, err := sb.Runner.Run(t, "thread", "kill", "--id", th.ID); err != nil {
		t.Fatalf("thread kill: %v\n%s", err, stderr)
	}

	// Dead direction: pane process actually exits and the session is gone.
	if !waitUntil(10*time.Second, func() bool { return !pidAlive(panePID) }) {
		t.Errorf("pane pid %d still alive after kill", panePID)
	}
	if !waitUntil(10*time.Second, func() bool {
		_, err := sb.rawTmux(t, "has-session", "-t", "=sesh_killme")
		return err != nil
	}) {
		t.Errorf("session sesh_killme still exists after kill")
	}

	// Record gone.
	if threadInList(t, sb, th.ID) {
		t.Errorf("killed thread %s still appears in list", th.ID)
	}
}

// testThreadResolvePane asserts pane resolution works via the marker and SURVIVES
// the pane being moved to another window (the whole point of marking the pane
// instead of storing its location), and reports dead once the session is gone.
func testThreadResolvePane(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "resolve", "/tmp")
	var markPane string
	if !waitUntil(agentStartTimeout, func() bool {
		p, _, ok := sb.markedPane(t, th.ID)
		markPane = p
		return ok
	}) {
		t.Fatalf("marker pane never appeared")
	}

	loc := sb.resolvePane(t, th.ID)
	if !loc.Found {
		t.Fatalf("resolve-pane reported not found for live thread")
	}
	if loc.Pane.Pane != markPane {
		t.Errorf("resolved pane = %q, want marked pane %q", loc.Pane.Pane, markPane)
	}

	// Move the agent pane to a new window; the @sesh-thread-id pane option
	// travels with it, so resolution must still find the SAME pane id. (A window
	// needs >1 pane before break-pane will split one off, so add one first.)
	if out, err := sb.rawTmux(t, "split-window", "-t", markPane); err != nil {
		t.Fatalf("split-window (to enable break-pane): %v\n%s", err, out)
	}
	if out, err := sb.rawTmux(t, "break-pane", "-d", "-s", markPane); err != nil {
		t.Fatalf("break-pane: %v\n%s", err, out)
	}
	moved := sb.resolvePane(t, th.ID)
	if !moved.Found || moved.Pane.Pane != markPane {
		t.Errorf("after move: resolve found=%v pane=%q, want same pane %q", moved.Found, moved.Pane.Pane, markPane)
	}

	// Kill the session: resolution must report dead (not found).
	if out, err := sb.rawTmux(t, "kill-session", "-t", "=sesh_resolve"); err != nil {
		t.Fatalf("kill-session: %v\n%s", err, out)
	}
	if !waitUntil(5*time.Second, func() bool { return !sb.resolvePane(t, th.ID).Found }) {
		t.Errorf("resolve-pane still finds a pane after the session was killed")
	}
}

// testThreadList asserts the thread list reflects creates and kills. Agent-
// agnostic, so it uses pi (cheapest) for the spawns.
func testThreadList(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	a := sb.newThread(t, "pi", "list1", "/tmp")
	b := sb.newThread(t, "pi", "list2", "/tmp")

	threads := sb.listThreads(t)
	if !hasThread(threads, a.ID) || !hasThread(threads, b.ID) {
		t.Fatalf("list missing one of the spawned threads: %+v", threadIDs(threads))
	}
	for _, th := range threads {
		if th.ID == a.ID && (th.Name != "list1" || th.SessionName != "sesh_list1") {
			t.Errorf("thread a fields wrong: %+v", th)
		}
	}

	if _, stderr, err := sb.Runner.Run(t, "thread", "kill", "--id", a.ID); err != nil {
		t.Fatalf("kill: %v\n%s", err, stderr)
	}
	after := sb.listThreads(t)
	if hasThread(after, a.ID) {
		t.Errorf("killed thread still listed")
	}
	if !hasThread(after, b.ID) {
		t.Errorf("surviving thread missing from list")
	}
}

// ---- helpers ----

func (sb *Sandbox) resolvePane(t *testing.T, id string) api.ResolvePaneResponse {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "pane", "--id", id, "--json")
	if err != nil {
		t.Fatalf("thread pane: %v\n%s", err, stderr)
	}
	var resp api.ResolvePaneResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode resolve-pane: %v\nraw: %s", err, stdout)
	}
	return resp
}

func (sb *Sandbox) listThreads(t *testing.T) []api.Thread {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "list", "--json")
	if err != nil {
		t.Fatalf("thread list: %v\n%s", err, stderr)
	}
	var out []api.Thread
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var th api.Thread
		if err := json.Unmarshal([]byte(line), &th); err != nil {
			t.Fatalf("decode thread line %q: %v", line, err)
		}
		out = append(out, th)
	}
	return out
}

func threadInList(t *testing.T, sb *Sandbox, id string) bool {
	return hasThread(sb.listThreads(t), id)
}

func hasThread(threads []api.Thread, id string) bool {
	for _, th := range threads {
		if th.ID == id {
			return true
		}
	}
	return false
}

func threadIDs(threads []api.Thread) []string {
	var ids []string
	for _, th := range threads {
		ids = append(ids, th.ID)
	}
	return ids
}
