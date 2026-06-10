package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// Resume works for all three agents: each persists its conversation
	// incrementally, so a hard-killed session is resumable by relaunching with
	// --resume. (claude only persists when launched as a clean, top-level session;
	// the daemon guarantees that via agents.ScrubHarnessEnv — see daemon.New. The
	// long-standing "claude can't resume" symptom was that scrub missing, which let
	// CLAUDECODE/CLAUDE_CODE_* leak into the spawned claude and suppress its
	// transcript persistence.)
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.resume", a, loc,
				func(t *testing.T) { testThreadResume(t, string(a), loc) })
		}
	}
}

// testThreadResume revives a DEAD headed thread and proves the CONVERSATION
// CONTINUES, not restarts: it teaches the agent a codeword, kills the session
// (the thread goes dead, record kept), resumes (recreates the session +
// relaunches with --resume), and asserts the revived agent still remembers the
// codeword. For codex (no pre-assignable id) the session id is recovered from its
// rollouts; a codex thread killed before its first turn is the justified N/A edge,
// covered separately by TestCodexResumeBeforeFirstTurnIsNA.
func testThreadResume(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "rs", "/tmp")
	sb.waitThreadReady(t, th.ID, agent)

	token := "RESUMEWORD_" + strings.ToUpper(agent)
	// Teach the agent the codeword (this also mints codex's session id). Confirm
	// the turn ACTUALLY ran — working then waiting — so the conversation is really
	// persisted (otherwise a no-op send would leave nothing to resume).
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text",
		"Remember this codeword: "+token+". Just reply: ok."); err != nil {
		t.Fatalf("send codeword: %v\n%s", err, stderr)
	}
	if !waitUntil(45*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("codeword turn never started")
	}
	if !waitUntil(90*time.Second, func() bool { st := sb.threadStatus(t, th.ID); return st.Head == api.Headful && st.Busy == api.BusyIdle }) {
		t.Fatalf("codeword turn never completed")
	}

	// Kill the tmux session: the thread is now DEAD (record kept).
	if out, err := sb.rawTmux(t, "kill-session", "-t", "=sesh_rs"); err != nil {
		t.Fatalf("kill-session: %v\n%s", err, out)
	}
	if !waitUntil(10*time.Second, func() bool { st := sb.threadStatus(t, th.ID); return st.Head == api.Headless && st.Busy == api.BusyIdle }) {
		t.Fatalf("thread never went idle after kill")
	}

	// Resume: recreate the session + relaunch the agent with --resume.
	if _, stderr, err := sb.Runner.Run(t, "thread", "resume", "--id", th.ID); err != nil {
		t.Fatalf("resume: %v\n%s", err, stderr)
	}
	pane := sb.waitThreadReady(t, th.ID, agent)
	// The revived thread is a real, running agent under a freshly resolved pane.
	if !agentRunningUnder(mustMarkedPID(t, sb, th.ID), agent) {
		t.Fatalf("resumed thread has no live %s agent", agent)
	}

	// Continuity: the revived agent still knows the codeword (the conversation
	// continued — it was not a fresh session).
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text",
		"What was the codeword I told you earlier? Reply with ONLY the codeword."); err != nil {
		t.Fatalf("send continuity question: %v\n%s", err, stderr)
	}
	if !waitUntil(90*time.Second, func() bool {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		return strings.Contains(cap, token)
	}) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		t.Fatalf("resumed agent did not recall the codeword %q — conversation did NOT continue.\npane:\n%s", token, cap)
	}
}

// mustMarkedPID returns the marked pane's pid, waiting for it.
func mustMarkedPID(t *testing.T, sb *Sandbox, id string) int {
	t.Helper()
	var pid int
	waitUntil(agentStartTimeout, func() bool {
		_, p, ok := sb.markedPane(t, id)
		pid = p
		return ok
	})
	return pid
}

// TestCodexResumeBeforeFirstTurnIsNA is the justified-N/A edge (outside the
// matrix): a codex thread killed BEFORE its first turn has no minted session id,
// so it legitimately cannot be resumed — sesh must say so explicitly, never fake
// a revival.
func TestCodexResumeBeforeFirstTurnIsNA(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newThread(t, "codex", "early", "/tmp")
	// Wait only for the session/marker — do NOT send a turn (no session id minted).
	if !waitUntil(agentStartTimeout, func() bool {
		_, _, ok := sb.markedPane(t, th.ID)
		return ok
	}) {
		t.Fatalf("codex session never appeared")
	}
	if out, err := sb.rawTmux(t, "kill-session", "-t", "=sesh_early"); err != nil {
		t.Fatalf("kill-session: %v\n%s", err, out)
	}

	_, stderr, err := sb.Runner.Run(t, "thread", "resume", "--id", th.ID)
	if err == nil {
		t.Fatalf("resume of a codex thread that died before its first turn should FAIL (N/A), but it succeeded")
	}
	if !strings.Contains(stderr, "N/A") && !strings.Contains(stderr, "before its first turn") {
		t.Errorf("expected an explicit N/A error, got: %s", stderr)
	}
}
