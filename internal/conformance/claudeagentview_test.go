package conformance

// thread.claude-no-agent-view (sesh#16): pressing ← twice on an empty claude prompt
// opens claude's AGENT VIEW, and opening it moves the conversation into a claude
// BACKGROUND SESSION — a `continued-in` record, the daemon logging the source as
// `(slash)`, the thread stranded behind a holder sesh does not own. That is how two
// of Lukas's threads became un-revivable with nothing typed. sesh now pins
// CLAUDE_CODE_DISABLE_AGENT_VIEW=1 into every claude pane it launches.
//
// The cell drives the REAL chain: a real claude in a real tmux pane against an
// ISOLATED claude config dir, a real turn, then a real ← ← delivered to that pane by
// tmux — the keystrokes themselves, not a call into anything of sesh's. It asserts on
// the observable effects: the variable is in the claude PROCESS's environment, claude's
// OWN registry holds no background session for the thread, no `continued-in` record
// was written, and the pane is still the thread's claude. Both headed paths are covered
// — the spawn and a revive — because each builds its env separately and a pin on one
// is no evidence about the other.
//
// There is no in-cell "without the pin" baseline, because sesh offers no way to launch
// a pane without it. The anti-gaming neuter is that baseline: drop the pin and this
// cell goes red with a real background session in claude's registry.
//
// Remote = the same through a real `--machine` hop; the pin happens on the OWNER.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.claude-no-agent-view", matrix.Claude, loc,
			func(t *testing.T) { testClaudeNoAgentView(t, loc) })
	}
}

// paneEnv reads one variable from the environment of the thread's pane PROCESS — the
// claude itself, which is what claude reads its switch from.
func (sb *Sandbox) paneEnv(t *testing.T, threadID, key, when string) (string, bool) {
	t.Helper()
	_, pid, ok := sb.markedPane(t, threadID)
	if !ok {
		t.Fatalf("%s: no marked pane for %s", when, threadID)
	}
	raw, err := os.ReadFile(filepath.Join("/proc", itoa(pid), "environ"))
	if err != nil {
		t.Fatalf("%s: read environ of pane pid %d: %v", when, pid, err)
	}
	for _, kv := range strings.Split(string(raw), "\x00") {
		if v, found := strings.CutPrefix(kv, key+"="); found {
			return v, true
		}
	}
	return "", false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// continuedInWritten reports whether any transcript under the isolated config dir
// carries a `continued-in` record — the mark claude leaves when a conversation is
// handed to a background session.
func continuedInWritten(t *testing.T, cfgDir string) bool {
	t.Helper()
	found := false
	filepath.WalkDir(filepath.Join(cfgDir, "projects"), func(p string, de os.DirEntry, err error) error { //nolint:errcheck
		if err != nil || de.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), `"type":"continued-in"`) {
			found = true
		}
		return nil
	})
	return found
}

// pressLeftTwiceAndCheck sends ← ← to the thread's pane and asserts nothing was
// backgrounded. The wait is generous: unpinned, the handoff appears within ~1 s.
func (sb *Sandbox) pressLeftTwiceAndCheck(t *testing.T, cfgDir, cwd, threadID, pane, when string) {
	t.Helper()
	if v, ok := sb.paneEnv(t, threadID, "CLAUDE_CODE_DISABLE_AGENT_VIEW", when); !ok || v != "1" {
		// Errorf, not Fatalf: carry on and press ← ← anyway, so a missing pin is ALSO
		// reported as its effect (a real background session), not just as a mechanism.
		t.Errorf("%s: the claude in the thread's pane does not carry CLAUDE_CODE_DISABLE_AGENT_VIEW=1 (got %q, present=%v) — ← ← would hand the conversation to a background session",
			when, v, ok)
	}
	leaf := sb.leafSession(t, threadID)
	if _, err := sb.rawTmux(t, "send-keys", "-t", pane, "Left"); err != nil {
		t.Fatalf("%s: send Left: %v", when, err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := sb.rawTmux(t, "send-keys", "-t", pane, "Left"); err != nil {
		t.Fatalf("%s: send Left: %v", when, err)
	}
	time.Sleep(8 * time.Second)

	if e := backgroundSessions(t, cfgDir, cwd)[leaf]; e.ID != "" {
		t.Errorf("%s: ← ← handed the conversation to background session %s (state %q) — the thread is now owned by claude, not sesh", when, e.ID, e.State)
	}
	if held := backgroundSessions(t, cfgDir, cwd); len(held) != 0 {
		t.Errorf("%s: claude's registry holds background session(s) after ← ←: %+v", when, held)
	}
	if continuedInWritten(t, cfgDir) {
		t.Errorf("%s: a `continued-in` record was written — the conversation was moved to a background session", when)
	}
	if _, _, ok := sb.markedPane(t, threadID); !ok {
		t.Errorf("%s: the thread's pane is gone after ← ←", when)
	}
	if capd, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p"); strings.Contains(capd, "describe a task for a new session") {
		t.Errorf("%s: the pane is showing claude's agents view after ← ←:\n%s", when, capd)
	}
}

func testClaudeNoAgentView(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	cfgDir := setupClaudeConfigDir(t)
	sb := newSandbox(t, loc, withSandboxEnv("CLAUDE_CONFIG_DIR", cfgDir))
	sb.startDaemon(t)

	cwd := t.TempDir()
	th := sb.newThread(t, "claude", "noagentview", cwd)
	pane := sb.waitThreadReady(t, th.ID, "claude")
	// A real turn first: ← ← on a conversation with no messages has nothing to hand off.
	sb.answeredSend(t, th.ID, pane, "first turn")
	sb.pressLeftTwiceAndCheck(t, cfgDir, cwd, th.ID, pane, "spawn")

	// The revive path builds its env separately; prove the pin there too.
	sb.stopAndWait(t, th.ID, "pre-revive")
	if _, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID); err != nil {
		t.Fatalf("revive: %v\n%s", err, stderr)
	}
	pane = sb.waitThreadReady(t, th.ID, "claude")
	sb.answeredSend(t, th.ID, pane, "after revive")
	sb.pressLeftTwiceAndCheck(t, cfgDir, cwd, th.ID, pane, "revive")
}
