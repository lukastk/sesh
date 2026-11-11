package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

// thread.background-busy (schema 65, issue #24): an agent that ends its turn
// with background work in flight is NOT finished, so the thread reads busy and
// carries the work as `background` until a later turn end reports none.
//
// claude: the REAL Stop hook (integrations/claude/sesh-agent-state.sh,
// registered in ~/.claude/settings.json from the live ~/mysetup/sesh checkout —
// the same environmental prerequisite as thread.state-authority) forwards the
// payload's background_tasks. claude wakes ITSELF when the task finishes
// (UserPromptSubmit with a <task-notification>), and that turn's Stop reports
// the empty set. The background sleep deliberately outlives the daemon's
// 2-minute stale-busy bound: a thread busy only on background work has a
// byte-stable pane, and the bound must not mistake it for a lost turn_ended.
//
// codex: deferred (its background terminals are only visible in the rollout;
// Lukas 2026-11-11). pi: N/A, declared on the feature.
func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.background-busy", matrix.Claude, loc,
			func(t *testing.T) { testBackgroundBusyClaude(t, loc) })
		matrix.RegisterTest("thread.background-busy", matrix.Codex, loc, func(t *testing.T) {
			t.Skip("NOT IMPLEMENTED: codex background terminals (rollout-based detection, deferred — issue #24)")
		})
	}
}

const backgroundSleepSeconds = 150 // > the daemon's 2-minute authorityStaleBound

func testBackgroundBusyClaude(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, "claude", "bgbusy", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "claude")

	sb.sendKeys(t, pane, "Use the Bash tool with run_in_background set to true to run exactly this command: sleep 150. "+
		"Do not wait for it and do not check on it. Reply with the single word STARTED and end your turn. "+
		"When you are later told it completed, reply with the single word FINISHED.")

	// (1) The turn ends with the shell still running: busy, reported, and the
	// shell visible as background work. Seeing background at all proves the
	// turn_ended arrived (it is the only event that carries it).
	hasShell := func(s api.ThreadSnapshot) bool {
		if !s.Background.HasTasks() {
			return false
		}
		for _, task := range s.Background.Tasks {
			if task.Type == "shell" && strings.Contains(task.Description, "sleep") {
				return true
			}
		}
		return false
	}
	if !waitUntil(90*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Busy == api.BusyBusy && s.StateAuthority == api.AuthorityReported && hasShell(s)
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("claude never reached busy+reported with a background shell (busy=%s authority=%q background=%s) — is the claude hook registered from a checkout with --background-json?",
			s.Busy, s.StateAuthority, s.Background.Summary())
	}
	reportedAt := time.Now()

	// (2) It STAYS busy past the stale-busy bound while the pane is still.
	for time.Since(reportedAt) < 135*time.Second {
		s := sb.threadSnapshot(t, th.ID)
		if s.Busy != api.BusyBusy || s.StateAuthority != api.AuthorityReported || !hasShell(s) {
			t.Fatalf("background-busy thread lost its state after %s (busy=%s authority=%q background=%s) — the stale-busy bound or a lost report dropped it",
				time.Since(reportedAt).Round(time.Second), s.Busy, s.StateAuthority, s.Background.Summary())
		}
		time.Sleep(5 * time.Second)
	}

	// The routed status read carries it too.
	stdout, stderr, err := sb.Runner.Run(t, "thread", "status", "--id", th.ID)
	if err != nil {
		t.Fatalf("thread status: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "busy:          busy") || !strings.Contains(stdout, "background:    1 shell") {
		t.Fatalf("thread status does not show the background shell as busy:\n%s", stdout)
	}

	// (3) The shell finishes, claude wakes itself, and the wake-up turn's Stop
	// reports nothing in flight: idle, with a POSITIVE empty report (not nil).
	if !waitUntil(150*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Busy == api.BusyIdle && s.StateAuthority == api.AuthorityReported &&
			s.Background != nil && !s.Background.HasTasks()
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("after the background shell finished: busy=%s authority=%q background=%s, want idle+reported with an empty report",
			s.Busy, s.StateAuthority, s.Background.Summary())
	}
}
