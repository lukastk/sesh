package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

// thread.flagged (schema 44, ticket df4fb07a): the stored needs-attention
// flag. Every cell drives the REAL per-agent trigger chain end to end:
//   - claude: the Stop hook (turn end) + an AskUserQuestion stall (PreToolUse
//     → blocked report → the per-tick stall trigger) — the question text
//     becomes the flag reason;
//   - pi: the extension's agent_settled (turn end);
//   - codex: the notify hook sesh wires into the sandbox codex config at
//     spawn (turn_ended_no_authority — flag without claiming busy authority).
// Plus the manual surface on all three: --off clears (nothing auto-clears),
// --disable suppresses a REAL second turn's auto-flag, --on re-enables and
// flags (the one-rule semantic).
func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.flagged", a, loc,
				func(t *testing.T) { testFlagged(t, string(a), loc) })
		}
	}
}

func testFlagged(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "flg", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, agent)
	if sb.threadSnapshot(t, th.ID).Flagged {
		t.Fatalf("fresh thread already flagged")
	}

	// (1) A real turn, run DETACHED: the agent's own reporter chain must flag
	// the thread when the turn ends (nobody was watching).
	sb.sendKeys(t, pane, "Reply with exactly the word FLAGME and nothing else.")
	if !waitUntil(120*time.Second, func() bool { return sb.threadSnapshot(t, th.ID).Flagged }) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("%s turn end never flagged the thread (busy=%s authority=%q) — is the %s reporter wired?",
			agent, s.Busy, s.StateAuthority, agent)
	}
	if r := sb.threadSnapshot(t, th.ID).FlagReason; r != "turn ended" {
		t.Fatalf("auto-flag reason = %q, want \"turn ended\"", r)
	}

	// (2) Nothing auto-clears: the flag survives a stretch of quiet ticks.
	time.Sleep(2 * time.Second)
	if !sb.threadSnapshot(t, th.ID).Flagged {
		t.Fatalf("flag auto-cleared — flags are manual-clear only")
	}

	// (3) Manual clear; then DISABLE + a real second turn must NOT re-flag.
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--off", "--id", th.ID); err != nil {
		t.Fatalf("flag --off: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool { return !sb.threadSnapshot(t, th.ID).Flagged }) {
		t.Fatalf("flag --off never cleared")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--disable", "--id", th.ID); err != nil {
		t.Fatalf("flag --disable: %v\n%s", err, stderr)
	}
	sb.sendKeys(t, pane, "Reply with exactly the word AGAIN and nothing else.")
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("second turn never started")
	}
	if !waitUntil(120*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle }) {
		t.Fatalf("second turn never settled")
	}
	time.Sleep(1500 * time.Millisecond) // give a would-be wrong auto-flag time to land
	if s := sb.threadSnapshot(t, th.ID); s.Flagged {
		t.Fatalf("flag-disabled thread auto-flagged anyway")
	}

	// (4) Manual ON re-enables AND flags — the one-rule semantic.
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--on", "--id", th.ID); err != nil {
		t.Fatalf("flag --on: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Flagged && !s.FlagDisabled
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("flag --on must flag AND re-enable (flagged=%v disabled=%v)", s.Flagged, s.FlagDisabled)
	}

	// (5) The QUESTION contingency (claude only — pi/codex have no native ask
	// surface): an AskUserQuestion prompt must flag the thread MID-STALL with
	// the question as the reason — waiting for a turn edge would never come.
	if agent == "claude" {
		if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--off", "--id", th.ID); err != nil {
			t.Fatalf("flag --off: %v\n%s", err, stderr)
		}
		sb.sendKeys(t, pane, "Use the AskUserQuestion tool to ask me whether I prefer red or blue. You must use the AskUserQuestion tool, do not answer yourself.")
		if !waitUntil(120*time.Second, func() bool {
			s := sb.threadSnapshot(t, th.ID)
			return s.Flagged && strings.Contains(s.FlagReason, "red or blue")
		}) {
			s := sb.threadSnapshot(t, th.ID)
			t.Fatalf("AskUserQuestion never flagged with the question (flagged=%v reason=%q)", s.Flagged, s.FlagReason)
		}
	}
}
