package conformance

// Placement / shared-session cells (H13): a tmux session may host MANY threads —
// runtime identity is the pane's @sesh-thread-id marker, not the session. These
// prove the four consequences with REAL agents in REAL panes:
//   thread.placement       — --into-session (own window) + --into-window (split)
//   thread.placement-pane  — --into-pane register-then-exec into an existing shell
//   thread.stop-shared     — stop kills only the thread's PANE; siblings survive
//   thread.adopt-shared    — adopting a 2nd agent into a session that already
//                            hosts a thread (the UNIQUE-constraint bug this fixes)
//
// LOCAL-only by design: like thread.adopt, these key off per-server pane ids /
// session topology — the documented "pane ids are per-server" rule. The
// cross-machine `--machine` routing path is the same mechanism proven by
// route.parity + thread.stop/remote. Agent-agnostic: the behaviour under test is
// tmux topology, identical across agents; a real `pi` exercises the live agent.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.placement", matrix.AgentAgnostic, matrix.Local, testPlacement)
	matrix.RegisterTest("thread.placement-pane", matrix.AgentAgnostic, matrix.Local, testPlacementPane)
	matrix.RegisterTest("thread.stop-shared", matrix.AgentAgnostic, matrix.Local, testStopShared)
	matrix.RegisterTest("thread.adopt-shared", matrix.AgentAgnostic, matrix.Local, testAdoptShared)
}

const placementAgent = "pi" // real agent; placement is tmux topology, agent-independent

// placedThread spawns a thread with extra placement flags and returns its record.
func placedThread(t *testing.T, sb *Sandbox, name, cwd string, extra ...string) api.Thread {
	t.Helper()
	args := append([]string{"thread", "new", "--agent", placementAgent, "--name", name, "--cwd", cwd, "--json"}, extra...)
	stdout, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread new %v: %v\n%s", extra, err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &th); err != nil {
		t.Fatalf("decode placed thread: %v\nraw: %s", err, stdout)
	}
	return th
}

// paneLoc returns the (session, window) a pane lives in, off the work server.
func paneLoc(t *testing.T, sb *Sandbox, pane string) (session, window string) {
	t.Helper()
	out, err := sb.rawTmux(t, "list-panes", "-a", "-F", "#{pane_id}\t#{session_name}\t#{window_index}")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && f[0] == pane {
			return f[1], f[2]
		}
	}
	t.Fatalf("pane %s not found on the work server", pane)
	return "", ""
}

// waitAgentPane waits until a thread's marked pane holds a live agent of the
// expected kind, returning the pane id and its pid.
func waitAgentPane(t *testing.T, sb *Sandbox, threadID string) (pane string, pid int) {
	t.Helper()
	if !waitUntil(agentStartTimeout, func() bool {
		p, pp, ok := sb.markedPane(t, threadID)
		if ok && agentRunningUnder(pp, placementAgent) {
			pane, pid = p, pp
			return true
		}
		return false
	}) {
		t.Fatalf("thread %s never came up alive", threadID)
	}
	return pane, pid
}

func testPlacement(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// A: an ordinary thread in its own session.
	a := sb.newThread(t, placementAgent, "host", "/tmp")
	aPane, _ := waitAgentPane(t, sb, a.ID)
	aSess, aWin := paneLoc(t, sb, aPane)
	if aSess != a.SessionName {
		t.Fatalf("host pane session %q != record %q", aSess, a.SessionName)
	}

	// B: --into-session A's session => a NEW WINDOW of A's session, same session.
	b := placedThread(t, sb, "win", "/tmp", "--into-session", a.SessionName)
	if b.SessionName != a.SessionName {
		t.Fatalf("--into-session: record session %q != host %q", b.SessionName, a.SessionName)
	}
	bPane, _ := waitAgentPane(t, sb, b.ID)
	bSess, bWin := paneLoc(t, sb, bPane)
	if bSess != a.SessionName {
		t.Errorf("--into-session: B landed in session %q, want %q", bSess, a.SessionName)
	}
	if bWin == aWin {
		t.Errorf("--into-session: B must be a NEW window, but shares window %s with host", aWin)
	}

	// C: --into-window A's pane => a SPLIT — same window as A, a different pane.
	c := placedThread(t, sb, "split", "/tmp", "--into-window", aPane)
	cPane, _ := waitAgentPane(t, sb, c.ID)
	cSess, cWin := paneLoc(t, sb, cPane)
	if cSess != a.SessionName || cWin != aWin {
		t.Errorf("--into-window: C at %s:%s, want a split of host window %s:%s", cSess, cWin, aSess, aWin)
	}
	if cPane == aPane {
		t.Errorf("--into-window: split must be a NEW pane, got the host pane %s", aPane)
	}
	if c.SessionName != a.SessionName {
		t.Errorf("--into-window: record session %q != host %q", c.SessionName, a.SessionName)
	}

	// Three distinct live threads now share ONE session — identity is the marker.
	for _, th := range []string{a.ID, b.ID, c.ID} {
		if !threadInList(t, sb, th) {
			t.Errorf("thread %s missing from list", th)
		}
	}
}

func testPlacementPane(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// A plain shell pane on the work server (no agent) — the register-then-exec target.
	if _, err := sb.rawTmux(t, "new-session", "-d", "-s", "shellhost", "-x", "200", "-y", "50"); err != nil {
		t.Fatalf("shell session: %v", err)
	}
	t.Cleanup(func() { sb.rawTmux(t, "kill-session", "-t", "=shellhost") }) //nolint:errcheck
	out, err := sb.rawTmux(t, "list-panes", "-t", "=shellhost", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("shell pane: %v", err)
	}
	shellPane := strings.TrimSpace(out)

	// register-then-exec: the daemon registers + marks + returns the command, and
	// does NOT spawn. (--into-pane --json carries the full response.)
	stdout, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", placementAgent,
		"--name", "here", "--cwd", "/tmp", "--into-pane", shellPane, "--json")
	if err != nil {
		t.Fatalf("thread new --into-pane: %v\n%s", err, stderr)
	}
	var resp api.ThreadResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("decode into-pane response: %v\nraw: %s", err, stdout)
	}
	if resp.LaunchCommand == "" {
		t.Fatal("--into-pane returned no launch_command (register-then-exec contract)")
	}
	if resp.LaunchEnv["SESH_THREAD_ID"] != resp.Thread.ID {
		t.Errorf("launch_env missing SESH_THREAD_ID=%s", resp.Thread.ID)
	}
	// The pane is marked with the new thread, and the daemon did NOT launch an
	// agent (the pane is still the bare shell).
	if p, pid, ok := sb.markedPane(t, resp.Thread.ID); !ok || p != shellPane {
		t.Fatalf("pane %s not marked with thread %s (got %s ok=%v)", shellPane, resp.Thread.ID, p, ok)
	} else if agentRunningUnder(pid, placementAgent) {
		t.Fatal("--into-pane must NOT spawn the agent — the daemon launched one anyway")
	}

	// Now run the returned command in the pane (what --exec does in-process), and
	// the real agent comes up under the marked pane — the full contract.
	prefix := ""
	for k, v := range resp.LaunchEnv {
		prefix += k + "=" + v + " "
	}
	sb.sendKeys(t, shellPane, prefix+resp.LaunchCommand)
	pane, _ := waitAgentPane(t, sb, resp.Thread.ID)
	if pane != shellPane {
		t.Errorf("agent came up on %s, want the registered pane %s", pane, shellPane)
	}
	if !waitUntil(agentStartTimeout, func() bool { return sb.threadStatus(t, resp.Thread.ID).Head == api.Headful }) {
		t.Errorf("register-then-exec thread never reported headful")
	}
}

func testStopShared(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	a := sb.newThread(t, placementAgent, "host", "/tmp")
	_, aPID := waitAgentPane(t, sb, a.ID)
	b := placedThread(t, sb, "sib", "/tmp", "--into-session", a.SessionName)
	_, bPID := waitAgentPane(t, sb, b.ID)
	if aPID == bPID {
		t.Fatal("host and sibling resolved to the same pane pid")
	}

	// Stop the sibling: ONLY its pane dies. The host stays alive and the shared
	// session survives — the whole point (KillSession would take both).
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", b.ID); err != nil {
		t.Fatalf("stop sibling: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool { return !pidAlive(bPID) }) {
		t.Errorf("sibling pid %d still alive after stop", bPID)
	}
	if !pidAlive(aPID) {
		t.Fatalf("stopping the sibling killed the host pid %d — stop is not per-pane", aPID)
	}
	if _, err := sb.rawTmux(t, "has-session", "-t", "="+a.SessionName); err != nil {
		t.Fatalf("shared session %s gone after stopping ONE of its threads", a.SessionName)
	}
	if _, _, ok := sb.markedPane(t, a.ID); !ok {
		t.Errorf("host pane vanished after sibling stop")
	}

	// Stopping the last thread tears the now-empty session down.
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", a.ID); err != nil {
		t.Fatalf("stop host: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool {
		_, err := sb.rawTmux(t, "has-session", "-t", "="+a.SessionName)
		return err != nil
	}) {
		t.Errorf("session %s survived after its last thread stopped", a.SessionName)
	}
}

func testAdoptShared(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// A: a managed thread in its own session.
	a := sb.newThread(t, placementAgent, "host", "/tmp")
	_, aPID := waitAgentPane(t, sb, a.ID)

	// Launch a 2nd real agent in a NEW WINDOW of A's session WITHOUT sesh, then
	// adopt it. Before H13 this failed with a raw UNIQUE(session_name) 500.
	sid := uuid.NewString()
	out, err := sb.rawTmux(t, "new-window", "-t", "="+a.SessionName, "-P", "-F", "#{pane_id}", "pi --session-id "+sid)
	if err != nil {
		t.Fatalf("manual window in shared session: %v", err)
	}
	foreign := strings.TrimSpace(out)

	var adoptedID string
	if !waitUntil(60*time.Second, func() bool {
		o, _, err := sb.Runner.Run(t, "thread", "adopt", "--pane", foreign, "--name", "adopted", "--json")
		if err != nil {
			return false
		}
		var th struct {
			ID          string `json:"id"`
			SessionName string `json:"session_name"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(o)), &th) != nil {
			return false
		}
		adoptedID = th.ID
		if th.SessionName != a.SessionName {
			t.Errorf("adopted into session %q, want the shared %q", th.SessionName, a.SessionName)
		}
		return true
	}) {
		_, stderr, _ := sb.Runner.Run(t, "thread", "adopt", "--pane", foreign, "--name", "adopted")
		t.Fatalf("adopt into a shared session never succeeded; last: %s", stderr)
	}

	// Both threads coexist, both live, sharing one session.
	if !pidAlive(aPID) {
		t.Errorf("host died during adoption of a sibling")
	}
	for _, id := range []string{a.ID, adoptedID} {
		if !threadInList(t, sb, id) {
			t.Errorf("thread %s missing after shared-session adopt", id)
		}
	}
	// Re-adopting the SAME pane is a clean 409 (never a raw DB error).
	if _, stderr, err := sb.Runner.Run(t, "thread", "adopt", "--pane", foreign, "--name", "again"); err == nil {
		t.Errorf("re-adopt of an already-managed pane succeeded silently")
	} else if !strings.Contains(stderr, "already belongs") {
		t.Errorf("re-adopt error should be a clean 'already belongs', got: %s", stderr)
	}
}
