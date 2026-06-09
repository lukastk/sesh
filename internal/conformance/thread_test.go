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
	// Both localities: Remote = `--machine` routing into a peer daemon over a real
	// ssh hop (the exact path the v1 `--machine X` bug broke).
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.new.headed", a, loc,
				func(t *testing.T) { testThreadNewHeaded(t, string(a), loc) })
			matrix.RegisterTest("thread.kill", a, loc,
				func(t *testing.T) { testThreadKill(t, string(a), loc) })
			matrix.RegisterTest("thread.resolve-pane", a, loc,
				func(t *testing.T) { testThreadResolvePane(t, string(a), loc) })
		}
		matrix.RegisterTest("thread.list", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testThreadList(t, loc) })
	}

	// thread.runtime-state + thread.send.headful: the content-diff activity signal
	// is agent-agnostic. All three agents now spawn deterministically — codex's
	// per-directory trust prompt (which used to eat input) is pre-granted by sesh
	// at spawn (see agents.EnsureCodexTrust). Both localities.
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.runtime-state", a, loc,
				func(t *testing.T) { testRuntimeState(t, string(a), loc) })
			matrix.RegisterTest("thread.send.headful", a, loc,
				func(t *testing.T) { testSendHeadful(t, string(a), loc) })
		}
	}
}

// testSendHeadful asserts that `sesh thread send` actually delivers a message
// into the agent's live pane: the observable proof is that the agent begins a
// turn (activity flips to working) in response. A dead thread cannot be sent to.
func testSendHeadful(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "send", "/tmp")
	sb.waitThreadReady(t, th.ID, agent)

	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text",
		"Write a detailed 150-word explanation of how HTTP request/response works"); err != nil {
		t.Fatalf("thread send: %v\n%s", err, stderr)
	}

	// Observable effect: the message landed and the agent began a turn.
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Activity == api.ActivityWorking }) {
		t.Fatalf("agent never started working after send (message not delivered?)")
	}
}

// testRuntimeState exercises the two orthogonal runtime axes for a real agent
// thread, BOTH directions and their independence:
//   - activity waiting <-> working, via a real turn (content-diff);
//   - working is observed while DETACHED, proving activity is independent of
//     attachment;
//   - the attachment axis flips when a client attaches;
//   - activity dead once the session is killed.
//
// This is the flagship honesty cell — the region the v1 codex bug lived in — so
// it asserts the real, observable transitions, never internal state.
func testRuntimeState(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "rt", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, agent)

	// Idle and nobody attached: waiting + detached, and needs-input is true even
	// though detached (the orthogonality the design requires).
	st := sb.threadStatus(t, th.ID)
	if st.Attachment != api.Detached {
		t.Errorf("fresh thread attachment = %s, want detached", st.Attachment)
	}
	if !st.NeedsInput() {
		t.Errorf("idle detached thread should report needs-input")
	}

	// Send a real turn WHILE DETACHED; activity must flip to working (pane
	// animates with no client attached).
	sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how DNS resolution works")
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Activity == api.ActivityWorking }) {
		t.Fatalf("activity never became working after a turn (detached)")
	}
	if got := sb.threadStatus(t, th.ID); got.Attachment != api.Detached {
		t.Errorf("expected still detached while working, got %s", got.Attachment)
	}

	// Turn completes -> back to waiting (the other direction).
	if !waitUntil(60*time.Second, func() bool { return sb.threadStatus(t, th.ID).Activity == api.ActivityWaiting }) {
		t.Fatalf("activity never returned to waiting after the turn")
	}

	// Attachment axis: attach a client -> attached, activity unaffected (waiting).
	sb.attachViewer(t, "sesh_rt")
	if !waitUntil(10*time.Second, func() bool { return sb.threadStatus(t, th.ID).Attachment == api.Attached }) {
		t.Errorf("attachment never became attached after a client attached")
	}

	// Kill the session -> activity dead.
	if out, err := sb.rawTmux(t, "kill-session", "-t", "=sesh_rt"); err != nil {
		t.Fatalf("kill-session: %v\n%s", err, out)
	}
	if !waitUntil(10*time.Second, func() bool { return sb.threadStatus(t, th.ID).Activity == api.ActivityDead }) {
		t.Errorf("activity never became dead after kill")
	}
}

// agentStartTimeout is generous: a real agent (esp. node-wrapped codex) takes a
// moment for its process to appear under the pane.
const agentStartTimeout = 20 * time.Second

// testThreadNewHeaded spawns a real agent in a real tmux pane and asserts the
// OBSERVABLE facts: the session exists, the pane carries the thread marker,
// SESH_THREAD_ID is injected into the pane env, and a REAL agent process of the
// right kind is actually running under the pane (independent ps walk).
func testThreadNewHeaded(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
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
func testThreadKill(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "killme", "/tmp")

	// Alive direction: a real agent of the right kind is genuinely running under
	// the marked pane (this wait IS the alive assertion).
	var panePID int
	if !waitUntil(agentStartTimeout, func() bool {
		_, pid, ok := sb.markedPane(t, th.ID)
		panePID = pid
		return ok && agentRunningUnder(pid, agent)
	}) {
		t.Fatalf("agent never came up alive for kill test")
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
func testThreadResolvePane(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
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

	resolved := sb.resolvePane(t, th.ID)
	if !resolved.Found {
		t.Fatalf("resolve-pane reported not found for live thread")
	}
	if resolved.Pane.Pane != markPane {
		t.Errorf("resolved pane = %q, want marked pane %q", resolved.Pane.Pane, markPane)
	}
	// The resolved pane must be the CORRECT one — the pane actually running this
	// agent (not merely a pane that happens to carry the marker).
	if !waitUntil(agentStartTimeout, func() bool { return agentRunningUnder(resolved.Pane.PanePID, agent) }) {
		t.Errorf("resolved pane (pid %d) is not running a real %q process", resolved.Pane.PanePID, agent)
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
func testThreadList(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
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
