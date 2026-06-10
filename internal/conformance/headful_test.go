package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// thread.headful: promote a LIVE headless thread into a headed pane (resume the
	// conversation), for all three agents x both localities.
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.headful", a, loc,
				func(t *testing.T) { testThreadHeadful(t, string(a), loc) })
		}
	}
	// The busy-rejection is agent-agnostic.
	matrix.RegisterTest("thread.headful-busy", matrix.AgentAgnostic, matrix.Local, testThreadHeadfulBusy)
}

// headlessTurn sends one headless turn and waits for it to complete (mints the agent
// session, establishes a real conversation to resume).
func (sb *Sandbox) headlessTurn(t *testing.T, id, text string) {
	t.Helper()
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", id, "--text", text); err != nil {
		t.Fatalf("send-headless: %v\n%s", err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool { return !sb.headlessReply(t, id).Working }) {
		t.Fatalf("headless turn never completed")
	}
}

// testThreadHeadful: a headless thread that has had a real turn is promoted to headed
// — a REAL agent lands in a REAL marked tmux pane (the conversation resumed), and the
// record is no longer headless. Asserted on the observable pane, not internal state.
func testThreadHeadful(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newHeadlessThread(t, agent, "promo")
	// One real turn establishes the session (codex mints its id here).
	sb.headlessTurn(t, th.ID, "Reply with exactly: ok")

	// Promote to headed.
	if _, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID); err != nil {
		t.Fatalf("headful (%s/%s): %v\n%s", agent, loc, err, stderr)
	}

	// A real agent now lives in a real pane — a headless thread NEVER has a pane, so
	// this is the observable proof the promotion landed a live, headed agent.
	sb.waitThreadReady(t, th.ID, agent)
	if !agentRunningUnder(mustMarkedPID(t, sb, th.ID), agent) {
		t.Errorf("no real %s agent running in the promoted pane", agent)
	}
}

// testThreadHeadfulBusy: promoting while a turn is in flight is rejected with a
// conflict (never spawn a pane mid-turn — it would fork the conversation).
func testThreadHeadfulBusy(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newHeadlessThread(t, "pi", "busy")
	// Start a turn long enough to still be in flight when we try to promote; do NOT wait.
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID, "--text",
		"Write a detailed 300-word explanation of how TCP congestion control works."); err != nil {
		t.Fatalf("send-headless: %v\n%s", err, stderr)
	}
	if !waitUntil(45*time.Second, func() bool { return sb.headlessReply(t, th.ID).Working }) {
		t.Fatalf("turn never went in-flight")
	}
	// Promotion mid-turn must be rejected loudly.
	_, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID)
	if err == nil {
		t.Errorf("headful of a busy (turn-in-flight) thread should be rejected, but it succeeded")
	} else if !strings.Contains(stderr, "in flight") {
		t.Errorf("expected a turn-in-flight conflict, got: %s", stderr)
	}
}

// TestThreadHeadfulCodexBeforeFirstTurnIsNA is the justified-N/A edge (outside the
// matrix): a codex headless thread with no first turn has no minted session id, so it
// cannot be promoted — an explicit N/A, never faked.
func TestThreadHeadfulCodexBeforeFirstTurnIsNA(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newHeadlessThread(t, "codex", "early")
	// No turn => codex never minted a session id.
	_, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID)
	if err == nil {
		t.Fatalf("headful of a codex thread with no first turn should FAIL (N/A), but it succeeded")
	}
	if !strings.Contains(stderr, "N/A") && !strings.Contains(stderr, "no first turn") {
		t.Errorf("expected an explicit N/A error, got: %s", stderr)
	}
}
