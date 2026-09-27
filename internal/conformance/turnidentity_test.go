package conformance

// thread.turn-identity cells: a daemon-launched headless WORKER obtaining its
// own verified identity — driven through the REAL scheduled spawn path, with a
// REAL agent, against a REAL daemon.
//
// WHAT WAS BROKEN. `sesh schedule spawn --headless` creates a thread and runs its
// first turn as a child of the daemon. That worker has no tmux pane, so the only
// thing naming its thread was the inherited $SESH_THREAD_ID — which `sesh whoami`
// refuses, correctly and by design, because a detached background job carries a
// perfectly valid id belonging to unrelated work. Reported from a real pi probe
// on mymain (2026-09-26): `whoami --json` exit 1, `info --json` source=env
// verified=false. The one process on the machine whose identity the daemon knew
// for certain was the only one that could not prove it.
//
// WHY THE CELL IS SHAPED LIKE THIS. Two halves that have to hold TOGETHER, or
// the feature is either useless or a regression:
//
//	(a) the worker, from inside its own turn, gets its OWN uuid back — not the
//	    parent's, not a neighbour's;
//	(b) at the very same moment, with the very same live turn in flight, a
//	    process OUTSIDE that turn's process tree carrying that same id in its
//	    environment is still REFUSED.
//
// (b) is the anti-weakening proof and it is why the mechanism is an ancestry
// walk rather than a credential in the environment: a token would be inherited
// by exactly the detached processes the model distrusts. The cell also plants a
// DECOY headless thread in the SAME directory, so an implementation that
// "guessed from the cwd" (or from the newest schedule run) would resolve two
// candidates and cannot pass (a).
//
// PER-AGENT AXES, deliberately. The environment construction is shared (one
// agents.TurnEnv above the per-agent switch), but what each agent does to run a
// shell command is NOT: ancestry holds only if the agent executes its tools as
// descendants of the turn process rather than handing them to a machine-global
// helper. That is precisely the kind of per-agent difference the matrix exists to
// find (H93), so it is an axis and not an assumption.
//
// LOCAL-ONLY: the question is "who is THIS process", which has no remote form —
// the thread.whoami / thread.placement precedent.

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, a := range matrix.AllAgents {
		a := a
		matrix.RegisterTest("thread.turn-identity", a, matrix.Local,
			func(t *testing.T) { testTurnIdentity(t, string(a)) })
	}
}

// identityProbePrompt asks the worker to do one mechanical thing. $SESH_BIN is
// the launching daemon's own executable, injected into the turn env: a bare
// `sesh` from a login shell may be a shell FUNCTION that re-pins SESH_HOME, or an
// older binary from a profile directory — either would have the worker asking a
// different sesh than the one that launched it.
const identityProbePrompt = `You are a test probe. Do exactly this, nothing else.

1. Run this shell command: "$SESH_BIN" whoami
2. Reply with ONE line: IAM=<that command's stdout, whitespace trimmed>
   If it exits non-zero, reply with ONE line: IAM=FAILED <its stderr>

No explanation, no code fences, no other text.`

func testTurnIdentity(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	workDir := t.TempDir()

	// THE DECOY, created first so it is the OLDER thread in the same directory.
	// Its only job is to make a cwd/name/recency guess ambiguous: if the worker
	// comes back with this id, the mechanism is inferring rather than verifying.
	decoy := sb.newHeadlessThreadAt(t, agent, "turn-identity-decoy", workDir)

	// The real native scheduled path: a spawn schedule, headless, fired with
	// run-now (the sanctioned way to test one). --yolo so the worker may actually
	// run the one command the probe asks for.
	sc := sb.scheduleJSON(t, "spawn",
		"--name", "turn-identity-"+agent,
		"--agent", agent,
		"--cwd", workDir,
		"--yolo",        // headless is the DEFAULT (there is only --headed); yolo so the worker may run the one command the probe asks for
		"--every", "1h", // next fire is an hour out; run-now is what fires it
		"--prompt", identityProbePrompt,
	)
	out, err := sb.scheduleRunNow(t, sc.ID, false)
	if err != nil || !strings.HasPrefix(out.Outcome, api.OutcomeFired) {
		t.Fatalf("run-now on a headless spawn schedule: outcome %q detail %q err %v", out.Outcome, out.Detail, err)
	}
	worker := out.ThreadID
	if worker == "" {
		t.Fatalf("the spawn run reported no thread id: %+v", out)
	}
	if worker == decoy.ID {
		t.Fatalf("the run reused the decoy thread %s", decoy.ID[:8])
	}
	if st := sb.threadStatus(t, worker); st.Head != api.Headless {
		t.Fatalf("the run's thread is %s, want headless — this cell is about the PANE-LESS worker", st.Head)
	}

	// --- (b) WHILE THE TURN IS LIVE: an outside process is still refused. ---
	//
	// Asserted against a turn that is genuinely in flight, so it proves the live
	// credential does not leak to non-descendants. TMUX/TMUX_PANE are blanked
	// explicitly: the test process may itself be inside a real tmux pane, and a
	// cell whose outcome depends on that is not a measurement.
	if !waitUntil(60*time.Second, func() bool { return sb.threadStatus(t, worker).Busy == api.BusyBusy }) {
		t.Fatalf("the spawned worker never went busy, so there is no live turn to test against")
	}
	outsideEnv := map[string]string{"SESH_THREAD_ID": worker, "TMUX": "", "TMUX_PANE": ""}
	// The control, and it must keep working: `info` is a DIAGNOSTIC and is
	// expected to describe the thread and exit 0 on an uncontradicted env id.
	// If this ever starts failing, the refusal below stops proving anything.
	if _, stderr, ierr := runWithEnvDir(t, sb, workDir, outsideEnv, "info"); ierr != nil {
		t.Fatalf("PRECONDITION: `info` must still resolve an uncontradicted env id: %v\n%s", ierr, stderr)
	}
	stdout, stderr, werr := runWithEnvDir(t, sb, workDir, outsideEnv, "whoami")
	if werr == nil {
		t.Errorf("whoami accepted a process that merely INHERITED the worker's $SESH_THREAD_ID while the "+
			"worker's turn was live — the turn identity must not be claimable from outside the turn's "+
			"process tree:\n%s", stdout)
	} else if strings.TrimSpace(stdout) != "" {
		t.Errorf("a refusing whoami wrote %q to stdout; it must be EMPTY", strings.TrimSpace(stdout))
	} else if !strings.Contains(stderr, "NOT a verified identity") {
		t.Errorf("refusal must say plainly it is not an identity: %s", stderr)
	}

	// --- (a) THE WORKER ITSELF, from inside its own turn. ---
	if !waitUntil(240*time.Second, func() bool { return !sb.headlessReply(t, worker).Working }) {
		t.Fatalf("the worker's turn never completed")
	}
	reply := sb.headlessReply(t, worker)
	if !reply.HaveReply || strings.HasPrefix(reply.Reply, "ERROR:") {
		t.Fatalf("no valid reply from the worker: %+v", reply)
	}
	if !strings.Contains(reply.Reply, worker) {
		t.Fatalf("the worker could not obtain its OWN identity. It was asked to run `$SESH_BIN whoami` "+
			"inside its own daemon-launched turn and should have printed %s; it replied:\n%s",
			worker, reply.Reply)
	}
	if strings.Contains(reply.Reply, decoy.ID) {
		t.Errorf("the worker reported the DECOY thread %s — the identity is being inferred from the "+
			"directory or the schedule row, not verified:\n%s", decoy.ID[:8], reply.Reply)
	}

	if st := sb.threadStatus(t, worker); st.Busy != api.BusyIdle {
		t.Errorf("worker busy = %s after its turn, want idle", st.Busy)
	}

	// NOT ASSERTED HERE, and stated rather than faked: that the registry entry is
	// DROPPED when the turn ends. Every vantage point that survives the turn is
	// outside the turn's process tree, and such a process is refused before AND
	// after — so an assertion from here would pass without the removal and prove
	// nothing (the vacuous-negative trap). The property rests on two things that
	// are each really tested: the pid is dropped in the SAME critical section
	// that clears the in-flight flag (whose clearing thread.send.headless proves,
	// via the busy→idle edge asserted above), and turnOwnerOf with no live entry
	// refuses — TestTurnOwnerOfRealProcesses/"nothing in flight".
}
