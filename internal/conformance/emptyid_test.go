package conformance

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/matrix"
)

// TestEmptyIDFlagIsLoud is the regression for the empty-selector footgun: an
// EXPLICITLY empty --id (`--id "$X"` with $X unset) must be a LOUD error, never
// silently inferred as the current thread. The reported case: `sesh thread archive
// --id ""` run INSIDE a thread (SESH_THREAD_ID set) archived the running thread.
// We prove against a real daemon that (1) the empty --id errors and leaves the
// current thread un-archived, and (2) inference still works when --id is OMITTED.
// Not a matrix cell: a focused regression for the guard (like TestEmptyThreadName).
func TestEmptyIDFlagIsLoud(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "footgun-target")

	// Drive the CLI with SESH_THREAD_ID set (and no inherited tmux pane), so the
	// command HAS a current thread it could wrongly infer from an empty --id.
	bin := seshBin(t)
	var env []string
	for _, e := range sandboxEnv(map[string]string{
		"SESH_HOME":          sb.Home,
		"SESH_MACHINE":       sb.Machine,
		"SESH_TMUX_SOCKET":   sb.TmuxSocket,
		"SESH_MASTER_SOCKET": sb.MasterSocket,
		agents.EnvThreadID:   th.ID,
	}) {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_PANE=") {
			continue // inference must come from SESH_THREAD_ID, not the user's real pane
		}
		env = append(env, e)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// archivedFlag reports the thread's persisted archived state (the daemon truth).
	archivedFlag := func() bool {
		for _, x := range sb.listThreadsArchived(t) { // includeArchived: active + archived
			if x.ID == th.ID {
				return x.Archived
			}
		}
		t.Fatalf("thread %s vanished from the listing", th.ID)
		return false
	}

	// (1) Explicit empty --id is loud, and the current thread is NOT archived.
	out, err := run("thread", "archive", "--id", "")
	if err == nil {
		t.Fatalf("`archive --id \"\"` should be a loud error (the footgun), got success:\n%s", out)
	}
	if !strings.Contains(out, "--id") {
		t.Errorf("the error should explain the empty --id:\n%s", out)
	}
	if archivedFlag() {
		t.Fatalf("`archive --id \"\"` archived the CURRENT thread — the footgun is NOT closed")
	}

	// (2) Inference is intact: OMITTING --id archives the current thread.
	if out, err := run("thread", "archive"); err != nil {
		t.Fatalf("`archive` with an omitted --id should infer the current thread: %v\n%s", err, out)
	}
	if !archivedFlag() {
		t.Errorf("omitted --id did not archive the current thread (inference broken)")
	}
}
