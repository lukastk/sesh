package conformance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

// thread.blocked (schema 43): the blocked overlay — mid-turn, stalled on the
// human — reported by the claude Notification hook (permission requests only;
// idle reminders are filtered) and cleared by PostToolUse/turn boundaries. The
// cell drives a REAL claude into a REAL permission prompt: the sandbox forces
// `--permission-mode default` via [spawn.claude] args (the user-level default
// is an auto mode that self-approves safe commands — found in the live smoke),
// asks for a Bash command, waits for blocked+reason, then APPROVES the prompt
// with a real keypress and watches the overlay clear while the turn finishes.
// pi and codex are justified N/A on the feature (pi never natively blocks;
// codex has no hook surface).
func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.blocked", matrix.Claude, loc,
			func(t *testing.T) { testBlocked(t, loc) })
	}
}

func testBlocked(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	// Force real permission prompts for spawned claudes: without this the
	// user-level auto mode self-approves the Bash call and nothing ever blocks.
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"),
		[]byte("[spawn.claude]\nargs = [\"--permission-mode\", \"default\"]\n"), 0o644); err != nil {
		t.Fatalf("write spawn config: %v", err)
	}
	sb.startDaemon(t)

	th := sb.newThread(t, "claude", "blk", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "claude")

	marker := filepath.Join(sb.Home, "blocked-cell-file")
	sb.sendKeys(t, pane, "Use the Bash tool to run exactly this command: touch "+marker+" . Do not ask questions, just run it.")

	// The permission prompt must surface as blocked (with a reason) — and a
	// blocked agent is by definition mid-turn, so busy + reported authority.
	if !waitUntil(60*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Blocked && s.BlockedReason != "" && s.Busy == api.BusyBusy && s.StateAuthority == api.AuthorityReported
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("claude never reported blocked at its permission prompt (busy=%s blocked=%v reason=%q authority=%q)",
			s.Busy, s.Blocked, s.BlockedReason, s.StateAuthority)
	}

	// APPROVE with a real keypress ("1. Yes") — the overlay must clear
	// (PostToolUse → unblocked) and the turn must then finish (Stop →
	// idle+reported), never sticking in blocked after the human answered.
	if out, err := sb.rawTmux(t, "send-keys", "-t", pane, "-l", "1"); err != nil {
		t.Fatalf("approve keypress: %v\n%s", err, out)
	}
	if !waitUntil(30*time.Second, func() bool { return !sb.threadSnapshot(t, th.ID).Blocked }) {
		t.Fatalf("blocked never cleared after approving the prompt")
	}
	if !waitUntil(120*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Busy == api.BusyIdle && !s.Blocked && s.StateAuthority == api.AuthorityReported
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("turn never settled post-approval (busy=%s blocked=%v authority=%q)", s.Busy, s.Blocked, s.StateAuthority)
	}
	// The approved command really ran (the observable external effect).
	if !waitUntil(10*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}) {
		t.Fatalf("approved Bash command never created %s", marker)
	}
}
