package conformance

// thread.codex-session-capture (schema 46, ticket 49d4299b): codex mints its
// session id on its FIRST TURN, and before 46 a headed codex thread never
// captured it — forking a live headed source 409'd misleadingly, and reviving
// fell back to cwd+time rollout discovery, which silently resumed the WRONG
// conversation when two codex threads shared a cwd (both live-proven in the
// 2026-07-26 experiment). Since 46 the notify reporter sesh wires at spawn
// carries the payload's thread-id and the daemon stamps it at each turn end.
// This cell drives the REAL chain (real codex, real notify hook, real report)
// and pins the two observable fixes:
//   (1) fork a LIVE headed codex thread with turns — it works, and carries
//       the conversation (plus: the pre-turn fork refusal names the REAL
//       state, not "no turn yet");
//   (2) two headed codex threads in ONE cwd, kill both, revive both — each
//       lands on its OWN conversation (recall proves it; ids stay distinct).
// LOCAL-only by design: the reporter and the rollouts live on the owner.

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.codex-session-capture", matrix.Codex, matrix.Local,
		func(t *testing.T) { testCodexSessionCapture(t) })
}

// headedTurn sends one real turn into a headed thread's pane and waits for it
// to latch busy and settle back to idle.
func (sb *Sandbox) headedTurn(t *testing.T, id, pane, text string) {
	t.Helper()
	sb.sendKeys(t, pane, text)
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, id).Busy == api.BusyBusy }) {
		t.Fatalf("turn never started on %s", id)
	}
	if !waitUntil(150*time.Second, func() bool { return sb.threadStatus(t, id).Busy == api.BusyIdle }) {
		t.Fatalf("turn never settled on %s", id)
	}
}

// waitStamped waits for the notify->report-state chain to record the thread's
// codex session id and returns it.
//
// The bound MATCHES headedTurn's settle bound, and that is deliberate rather
// than generous. codex is the one agent with NO authoritative turn state (it is
// a justified N/A on thread.state-authority), so headedTurn's "the turn
// settled" is the CONTENT-DIFF HEURISTIC — and a pane that stops animating
// reads as idle even when the turn is merely stalled (H58's frozen-pane class;
// under a full-suite run there are many real agents competing for the same
// provider, so a stall on a slow API call is ordinary). When that happens
// headedTurn returns early and this wait is still, in truth, waiting for the
// turn to finish. Giving it less time than a turn is allowed to take makes the
// cell fail for a reason that has nothing to do with what it tests.
//
// The ASSERTION is unchanged: the id must be stamped, or the cell is red.
func (sb *Sandbox) waitStamped(t *testing.T, id string) string {
	t.Helper()
	var sid string
	if !waitUntil(150*time.Second, func() bool {
		sid = sb.getThread(t, id).AgentSessionID
		return sid != ""
	}) {
		// Report the busy axis too: "still busy" means the turn really was
		// mid-flight and the heuristic had declared it settled early; "idle"
		// means the turn ended and the notify->report-state chain itself failed.
		// Those are different bugs and the message should not conflate them.
		t.Fatalf("agent_session_id never stamped for %s after 150s (thread busy=%s) — "+
			"if busy, the turn was still running and headedTurn's heuristic settle fired early; "+
			"if idle, the codex notify -> report-state chain is broken",
			id, sb.threadStatus(t, id).Busy)
	}
	return sid
}

func testCodexSessionCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	cwd := t.TempDir() // ONE cwd shared by both threads — the ambiguity under test

	a := sb.newThread(t, "codex", "cxa", cwd)
	paneA := sb.waitThreadReady(t, a.ID, "codex")

	// Pre-turn fork refusal names the real state (no captured session id),
	// not the old misleading "no turn yet".
	if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--fork-from", a.ID, "--name", "early"); err == nil {
		t.Fatalf("fork of a turn-less codex thread succeeded silently")
	} else if !strings.Contains(stderr, "no captured session id") {
		t.Fatalf("pre-turn fork refusal should name the missing session id, got: %s", stderr)
	}

	// (1) A real turn must stamp the id (the notify->report-state chain)...
	sb.headedTurn(t, a.ID, paneA, "Reply with exactly the word CAPALFA and nothing else.")
	sidA := sb.waitStamped(t, a.ID)

	// ...and forking the LIVE HEADED thread now works and carries the turn.
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--fork-from", a.ID, "--name", "cxa-branch", "--json")
	if err != nil {
		t.Fatalf("fork of a live headed codex thread: %v\n%s", err, stderr)
	}
	_ = out
	// (The fork/branch content contract itself is thread.fork's cell; here the
	// point is that a live HEADED codex source forks at all.)

	// (2) A second codex thread in the SAME cwd gets its OWN id...
	b := sb.newThread(t, "codex", "cxb", cwd)
	paneB := sb.waitThreadReady(t, b.ID, "codex")
	sb.headedTurn(t, b.ID, paneB, "Reply with exactly the word CAPBRAVO and nothing else.")
	sidB := sb.waitStamped(t, b.ID)
	if sidB == sidA {
		t.Fatalf("both threads stamped the SAME codex session %s", sidA)
	}

	// ...then kill BOTH and revive BOTH: each must land on its OWN
	// conversation. Revive the OLDER first — the exact order that made the
	// pre-46 discovery fallback resume the newer thread's conversation.
	for _, id := range []string{a.ID, b.ID} {
		if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", id); err != nil {
			t.Fatalf("stop %s: %v\n%s", id, err, stderr)
		}
	}
	for _, re := range []struct{ id, sid, word, other string }{
		{a.ID, sidA, "CAPALFA", "CAPBRAVO"},
		{b.ID, sidB, "CAPBRAVO", "CAPALFA"},
	} {
		if _, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", re.id); err != nil {
			t.Fatalf("headful %s: %v\n%s", re.id, err, stderr)
		}
		if got := sb.getThread(t, re.id).AgentSessionID; got != re.sid {
			t.Fatalf("revive changed %s's session id %s -> %s (landed on another conversation)", re.id, re.sid, got)
		}
		pane := sb.waitThreadReady(t, re.id, "codex")
		sb.headedTurn(t, re.id, pane, "Which word did I ask you to reply with earlier? Answer with just that word.")
		tail := sb.transcriptText(t, re.id)
		if i := strings.LastIndex(tail, "Which word"); i >= 0 {
			tail = tail[i:]
		}
		if !strings.Contains(tail, re.word) {
			t.Fatalf("revived %s lost its conversation: recall answer lacks %s\n%s", re.id, re.word, tail)
		}
		if strings.Contains(tail, re.other) {
			t.Fatalf("revived %s answered with the OTHER thread's word %s (cross-landed)", re.id, re.other)
		}
	}
}
