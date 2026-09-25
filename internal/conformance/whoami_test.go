package conformance

// thread.whoami cells: `sesh whoami`, the default-safe identity GATE, against a
// REAL daemon, a REAL agent pane and a REAL pane marker.
//
// The cell's job is to prove whoami DIFFERS from `sesh info` in the one place
// that matters. info resolves the same answers and exits 0 for every row here —
// so each refusal below is asserted alongside the corresponding `info` call
// SUCCEEDING on identical inputs. Without that pairing the cell would pass just
// as well against a whoami that was a thin alias of info, which is exactly the
// non-feature this exists to rule out.
//
// LOCAL-ONLY BY DESIGN (the thread.placement precedent): whoami reports who the
// CALLING process is, so there is no remote form of the question. Routing it
// would have a peer read its own pane and environment and answer confidently
// about a different machine — the very failure mode the command prevents — so
// `--machine` is a loud refusal, asserted below rather than left untested.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.whoami", matrix.AgentAgnostic, matrix.Local, testWhoamiLocal)
}

func testWhoamiLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "whoami-subject", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")
	sockPath := tmuxSocketPath(sb.TmuxSocket)
	paneEnv := map[string]string{"TMUX": sockPath + ",1,0", "TMUX_PANE": pane}

	// 1. THE ACCEPTING CASE. A real pane marker is the one source a process
	// living elsewhere cannot inherit. stdout must be the bare uuid and
	// NOTHING else: `TID=$(sesh whoami)` is the whole point of the verb, and a
	// stray banner would land inside TID.
	out, stderr, err := runWithEnv(t, sb, paneEnv, "whoami")
	if err != nil {
		t.Fatalf("whoami in a real marked pane must succeed: %v\n%s", err, stderr)
	}
	if got := strings.TrimSpace(out); got != th.ID {
		t.Fatalf("whoami stdout = %q, want exactly the uuid %q — command substitution captures all of it", got, th.ID)
	}

	// The alias resolves to the same verb.
	out, stderr, err = runWithEnv(t, sb, paneEnv, "thread", "whoami")
	if err != nil || strings.TrimSpace(out) != th.ID {
		t.Errorf("`thread whoami` alias = %q (%v)\n%s", strings.TrimSpace(out), err, stderr)
	}

	// JSON carries the provenance a caller may want to log.
	out, _, err = runWithEnv(t, sb, paneEnv, "whoami", "--json")
	if err != nil {
		t.Fatalf("whoami --json: %v", err)
	}
	var js struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Source   string `json:"source"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &js); err != nil {
		t.Fatalf("whoami --json decode: %v\n%s", err, out)
	}
	if js.ID != th.ID || js.Source != "pane" || !js.Verified || js.Name != "whoami-subject" {
		t.Errorf("whoami --json = %+v, want id %s source=pane verified=true", js, th.ID)
	}

	// 2. THE RESIDUAL — the reason this verb exists as more than a rename.
	// A detached job standing INSIDE its launching thread's tree: the cwd
	// neither contradicts the inherited id nor confirms it, so corroboration
	// stays silent. `info` therefore answers and exits 0; whoami must not.
	base := t.TempDir()
	threadCwd := filepath.Join(base, "mysetup", "sesh")
	insideCwd := filepath.Join(threadCwd, "cmd", "sesh")
	unrelatedCwd := filepath.Join(base, "dev", "20260917_4cr9iq__mosaic-v3", "courses", "finnish")
	for _, d := range []string{insideCwd, unrelatedCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	inherited := sb.newHeadlessThreadAt(t, "pi", "inherited-id-thread", threadCwd)
	envOnly := map[string]string{"SESH_THREAD_ID": inherited.ID}

	// The control: info succeeds here. If this ever starts failing, the cell
	// below stops proving anything and must be re-read, not re-baselined.
	if _, stderr, err := runWithEnvDir(t, sb, insideCwd, envOnly, "info"); err != nil {
		t.Fatalf("PRECONDITION: `info` is expected to resolve an uncontradicted env id (that tolerance "+
			"is what whoami exists to tighten); it failed instead: %v\n%s", err, stderr)
	}
	out, stderr, err = runWithEnvDir(t, sb, insideCwd, envOnly, "whoami")
	if err == nil {
		t.Errorf("whoami accepted an identity resting on an inherited $SESH_THREAD_ID with no pane to "+
			"confirm it — absence of contradiction is not evidence:\n%s", out)
	} else {
		if strings.TrimSpace(out) != "" {
			t.Errorf("a refusing whoami wrote %q to stdout; it must be EMPTY so `TID=$(sesh whoami)` "+
				"cannot capture a half-answer", strings.TrimSpace(out))
		}
		if !strings.Contains(stderr, "NOT a verified identity") {
			t.Errorf("refusal must say plainly it is not an identity: %s", stderr)
		}
	}

	// 3. THE REPORTED INCIDENT (2026-09-25): a claude background job in a
	// mosaic-v3 course box whose inherited id named a live thread in an
	// unrelated box. Contradicted, so the resolver refuses first — whoami must
	// re-phrase that refusal rather than offer --id, a flag it does not have.
	if _, stderr, err := runWithEnvDir(t, sb, unrelatedCwd, envOnly, "whoami"); err == nil {
		t.Errorf("whoami claimed an unrelated thread's identity — the reported bug")
	} else {
		if !strings.Contains(stderr, inherited.ID[:8]) {
			t.Errorf("the refusal must name the thread it nearly became: %s", stderr)
		}
		if strings.Contains(stderr, "--id") {
			t.Errorf("whoami has no --id; a refusal naming it sends the caller to a parse error: %s", stderr)
		}
	}

	// 4. NO IDENTITY AT ALL — the reporter's own position. That is an answer,
	// not a lookup failure, and there is nothing to "allow", so the override
	// must not be dangled.
	if out, stderr, err := runWithEnvDir(t, sb, unrelatedCwd, map[string]string{}, "whoami"); err == nil {
		t.Errorf("whoami invented an identity with no pane and no env:\n%s", out)
	} else {
		if !strings.Contains(stderr, "NO sesh thread identity") {
			t.Errorf("want the no-identity answer, got: %s", stderr)
		}
		if strings.Contains(stderr, "--allow-unverified") {
			t.Errorf("there is no unverified id to allow here; offering the override is misleading: %s", stderr)
		}
	}

	// 5. The deliberate override still works, and still prints a usable uuid.
	out, stderr, err = runWithEnvDir(t, sb, insideCwd, envOnly, "whoami", "--allow-unverified")
	if err != nil {
		t.Fatalf("--allow-unverified must downgrade the gate: %v\n%s", err, stderr)
	}
	if got := strings.TrimSpace(out); got != inherited.ID {
		t.Errorf("overridden whoami = %q, want %q", got, inherited.ID)
	}

	// 6. Not routable, and refused with the REASON — a peer would answer about
	// its own environment, which is the failure mode, not a limitation.
	if _, stderr, err := runWithEnv(t, sb, paneEnv, "whoami", "--machine", "somepeer"); err == nil {
		t.Errorf("whoami accepted --machine")
	} else if !strings.Contains(stderr, "cannot be routed") {
		t.Errorf("routing refusal must explain itself, got: %s", stderr)
	}

	// 7. No thread selector: naming one would make the answer trivially
	// "explicit" and defeat the question.
	if _, stderr, err := runWithEnv(t, sb, paneEnv, "whoami", th.ID); err == nil {
		t.Errorf("whoami accepted a thread argument")
	} else if !strings.Contains(stderr, "sesh info") {
		t.Errorf("the refusal must name the verb that does take one, got: %s", stderr)
	}
}
