package conformance

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
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("shell.lifecycle", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testShellLifecycle(t, loc) })
		matrix.RegisterTest("shell.promote", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testShellPromote(t, loc) })
		matrix.RegisterTest("shell.gates", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testShellGates(t, loc) })
	}
}

// shellThread runs a `sesh shell …` verb that returns a thread record.
func (sb *Sandbox) shellThread(t *testing.T, args ...string) api.Thread {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, append(append([]string{"shell"}, args...), "--json")...)
	if err != nil {
		t.Fatalf("sesh shell %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(stdout), &th); err != nil {
		t.Fatalf("decode shell thread json: %v\nraw: %s", err, stdout)
	}
	return th
}

// shellSessions runs `sesh shell sessions --json` and returns the classified set.
func (sb *Sandbox) shellSessions(t *testing.T) map[string]api.ShellSession {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "shell", "sessions", "--json")
	if err != nil {
		t.Fatalf("shell sessions: %v\n%s", err, stderr)
	}
	out := map[string]api.ShellSession{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ss api.ShellSession
		if err := json.Unmarshal([]byte(line), &ss); err != nil {
			t.Fatalf("decode session: %v\nraw: %s", err, line)
		}
		out[ss.Name] = ss
	}
	return out
}

// testShellLifecycle: a shell thread's runtime is a tmux SESSION and its durable
// content is its working directory. Create → the session is really there, rooted
// in that dir and carrying the marker; kill it → the thread goes headless; resume
// → the session comes back in the RECORDED cwd. Then the idempotency rule.
func testShellLifecycle(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	dir := filepath.Join(sb.Home, "boxdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	th := sb.shellThread(t, "new", "--cwd", dir, "--name", "boxwork")
	if th.AgentKind != api.ShellAgentKind {
		t.Fatalf("agent_kind = %q, want %q", th.AgentKind, api.ShellAgentKind)
	}
	if th.Cwd != dir {
		t.Fatalf("cwd = %q, want %q", th.Cwd, dir)
	}
	// A shell thread has NO conversation, so these must be empty — the record
	// must not silently acquire agent fields.
	if th.AgentSessionID != "" || th.Model != "" || th.HeadlessStarted {
		t.Fatalf("shell thread carries conversation fields: %+v", th)
	}

	// The REAL tmux session exists, is rooted in the dir, and carries the marker.
	sessions := sb.shellSessions(t)
	ss, ok := sessions[th.SessionName]
	if !ok {
		t.Fatalf("session %q not on the work server (have %v)", th.SessionName, sessionKeys(sessions))
	}
	if ss.Class != api.ShellClassShell || ss.ThreadID != th.ID {
		t.Fatalf("session class = %q id = %q, want shell/%s", ss.Class, ss.ThreadID, th.ID)
	}
	if ss.Path != dir {
		t.Fatalf("session start dir = %q, want %q", ss.Path, dir)
	}
	// Head follows the SESSION.
	if got := sb.threadStatus(t, th.ID).Head; got != api.Headful {
		t.Fatalf("head = %q with the session alive, want headful", got)
	}

	// Kill the session: a remembered place, not a live one.
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID); err != nil {
		t.Fatalf("stop: %v\n%s", err, stderr)
	}
	if !waitUntil(agentStartTimeout, func() bool { return string(sb.threadStatus(t, th.ID).Head) == string(api.Headless) }) {
		t.Fatalf("head never fell to headless after the session was killed")
	}

	// Revive: the session comes back IN THE RECORDED CWD. That is the whole
	// model — the cwd is to a shell thread what a transcript is to an agent one.
	if _, stderr, err := sb.Runner.Run(t, "thread", "resume", "--id", th.ID); err != nil {
		t.Fatalf("resume: %v\n%s", err, stderr)
	}
	if !waitUntil(agentStartTimeout, func() bool { return string(sb.threadStatus(t, th.ID).Head) == string(api.Headful) }) {
		t.Fatalf("head never returned to headful after resume")
	}
	revived := sb.shellSessions(t)
	var found bool
	for _, s := range revived {
		if s.ThreadID == th.ID {
			found = true
			if s.Path != dir {
				t.Fatalf("revived session start dir = %q, want the RECORDED cwd %q", s.Path, dir)
			}
		}
	}
	if !found {
		t.Fatalf("no session carries the marker after resume (have %v)", sessionKeys(revived))
	}

	// IDEMPOTENCY: `shell enter` on the same (cwd, name) returns the SAME thread.
	again := sb.shellThread(t, "enter", "--cwd", dir, "--name", "boxwork")
	if again.ID != th.ID {
		t.Fatalf("shell enter minted a SECOND thread %s for the same (cwd, name); want %s", again.ID, th.ID)
	}
	// ...while `shell new` refuses the duplicate name rather than making an
	// indistinguishable second row.
	if _, stderr, err := sb.Runner.Run(t, "shell", "new", "--cwd", dir, "--name", "boxwork", "--json"); err == nil {
		t.Fatalf("shell new accepted a duplicate (cwd, name); it must refuse and demand --name")
	} else if !strings.Contains(stderr, "distinct names") {
		t.Fatalf("duplicate refusal did not explain itself: %s", stderr)
	}
	// A DIFFERENT name in the same cwd is legal — several shells per box is fine.
	second := sb.shellThread(t, "new", "--cwd", dir, "--name", "boxtests")
	if second.ID == th.ID {
		t.Fatalf("a distinct name in the same cwd must make a NEW shell thread")
	}
}

// testShellPromote: an untracked session lists as a ghost, promotes in place, and
// then classifies as a shell thread. Also pins the other two classes.
func testShellPromote(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	dir := filepath.Join(sb.Home, "ghostdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A session sesh did NOT create — exactly what a hand-started shell is.
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", "handmade", "-c", dir); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	// ...and one holding an agent-thread pane, to pin the `agent` class.
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", "agentsess"); err != nil {
		t.Fatalf("new-session agentsess: %v\n%s", err, out)
	}
	pane := sb.paneOf(t, "agentsess")
	if out, err := sb.rawTmux(t, "set-option", "-p", "-t", pane, "@sesh-thread-id", "thr_conformance"); err != nil {
		t.Fatalf("stamp pane: %v\n%s", err, out)
	}

	before := sb.shellSessions(t)
	if got := before["handmade"].Class; got != api.ShellClassGhost {
		t.Fatalf("an untracked session classifies %q, want ghost", got)
	}
	if got := before["agentsess"].Class; got != api.ShellClassAgent {
		t.Fatalf("a session hosting an agent pane classifies %q, want agent", got)
	}

	th := sb.shellThread(t, "promote", "--session", "handmade")
	if th.AgentKind != api.ShellAgentKind {
		t.Fatalf("promoted record agent_kind = %q", th.AgentKind)
	}
	// The cwd comes from the session's START dir, which is the honest signal.
	if th.Cwd != dir {
		t.Fatalf("promoted cwd = %q, want the session's start dir %q", th.Cwd, dir)
	}
	after := sb.shellSessions(t)
	if got := after["handmade"].Class; got != api.ShellClassShell {
		t.Fatalf("after promote the session classifies %q, want shell", got)
	}
	if after["handmade"].ThreadID != th.ID {
		t.Fatalf("promoted session points at %q, want %s", after["handmade"].ThreadID, th.ID)
	}
	// Promoting twice is refused, naming the thread it already is.
	if _, stderr, err := sb.Runner.Run(t, "shell", "promote", "--session", "handmade", "--json"); err == nil {
		t.Fatalf("a second promote must refuse")
	} else if !strings.Contains(stderr, th.ID) {
		t.Fatalf("the refusal must name the existing thread: %s", stderr)
	}

	// STALE: delete the record while the session lives (--force), which UNSTAMPS,
	// so the session returns to being a ghost rather than carrying a dangling
	// marker. This is the delete contract for shells.
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", th.ID, "--force"); err != nil {
		t.Fatalf("force delete: %v\n%s", err, stderr)
	}
	back := sb.shellSessions(t)
	if got := back["handmade"].Class; got != api.ShellClassGhost {
		t.Fatalf("after a forced delete the session classifies %q, want ghost — delete must UNSTAMP, never leave a stale marker", got)
	}
	if out, err := sb.rawTmux(t, "has-session", "-t", "handmade"); err != nil {
		t.Fatalf("delete must leave the SESSION running: %v\n%s", err, out)
	}
}

// testShellGates: the taxonomy split, observed through the real CLI. A shell
// thread has a runtime (enter/send/capture/stop work) but no conversation
// (fork/transcript/send-headless refuse), and --pane addresses one pane exactly.
func testShellGates(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	dir := filepath.Join(sb.Home, "gatedir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	th := sb.shellThread(t, "new", "--cwd", dir, "--name", "gated")

	// CONVERSATION-shaped verbs refuse, and say why.
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"fork", []string{"thread", "new", "--fork-from", th.ID, "--name", "nope"}},
		{"transcript", []string{"thread", "transcript", "--id", th.ID}},
		{"send-headless", []string{"thread", "send-headless", "--id", th.ID, "--text", "hi"}},
	} {
		_, stderr, err := sb.Runner.Run(t, tc.args...)
		if err == nil {
			t.Fatalf("%s must refuse on a shell thread", tc.name)
		}
		if !strings.Contains(stderr, "shell thread") {
			t.Fatalf("%s refusal must name the kind: %s", tc.name, stderr)
		}
	}

	// RUNTIME-shaped verbs work. Capture proves the pane is really addressable.
	if _, stderr, err := sb.Runner.Run(t, "thread", "capture", "--id", th.ID); err != nil {
		t.Fatalf("capture must work on a shell thread: %v\n%s", err, stderr)
	}

	// --pane addresses ONE pane and lands nowhere else. Split the session so
	// there are two, write a marker file from a named pane, and check WHICH pane
	// ran it by the file it created.
	sess := th.SessionName
	if out, err := sb.rawTmux(t, "split-window", "-t", sess); err != nil {
		t.Fatalf("split-window: %v\n%s", err, out)
	}
	panes, err := sb.rawTmux(t, "list-panes", "-t", sess, "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	ids := strings.Fields(panes)
	if len(ids) != 2 {
		t.Fatalf("want 2 panes, got %v", ids)
	}
	target := ids[0]
	stamp := filepath.Join(dir, "from-pane.txt")
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID,
		"--pane", target, "--text", "printf %s "+target+" > "+stamp); err != nil {
		t.Fatalf("send --pane: %v\n%s", err, stderr)
	}
	if !waitUntil(agentStartTimeout, func() bool {
		b, rerr := os.ReadFile(stamp)
		return rerr == nil && strings.TrimSpace(string(b)) == target
	}) {
		got, _ := os.ReadFile(stamp)
		t.Fatalf("send --pane %s did not run in that pane (stamp=%q)", target, strings.TrimSpace(string(got)))
	}
	// A pane that is not in this session is LOUD, never a silent fallback to
	// some other pane.
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--pane", "%999", "--text", "x"); err == nil {
		t.Fatalf("send to a pane outside the session must refuse")
	} else if !strings.Contains(stderr, "%999") {
		t.Fatalf("the refusal must name the pane: %s", stderr)
	}

	// STOP GUARD: a shell whose session hosts an agent thread's pane refuses
	// without --force, because killing the session kills that agent too.
	if out, err := sb.rawTmux(t, "set-option", "-p", "-t", ids[1], "@sesh-thread-id", "thr_hosted"); err != nil {
		t.Fatalf("stamp hosted pane: %v\n%s", err, out)
	}
	_, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID)
	if err == nil {
		t.Fatalf("stopping a shell that hosts agent panes must refuse without --force")
	}
	if !strings.Contains(stderr, "thr_hosted") {
		t.Fatalf("the refusal must name the hosted agent thread: %s", stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID, "--force"); err != nil {
		t.Fatalf("--force must go through: %v\n%s", err, stderr)
	}
	if out, err := sb.rawTmux(t, "has-session", "-t", sess); err == nil {
		t.Fatalf("the session should be gone after a forced stop\n%s", out)
	}
}

func sessionKeys(m map[string]api.ShellSession) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
