package conformance

// thread.info cells (PARITY_ROADMAP F1): `sesh info` + the current-thread
// inference resolver, against REAL threads. Local exercises all four
// resolution sources (explicit prefix, $SESH_THREAD_ID, the calling pane's
// birth-stamp, and the loud no-context error), the PROVENANCE the answer
// carries, the no-pane refusal when an env-derived id is contradicted by the
// caller's directory (ticket d7be88ef), and a retrofitted verb (`thread status`
// with no --id). Remote = routed `info --id … --machine peer`.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.info", matrix.AgentAgnostic, matrix.Local, testInfoLocal)
	matrix.RegisterTest("thread.info", matrix.AgentAgnostic, matrix.Remote, testInfoRemote)
}

// runWithEnv runs the sesh binary against the sandbox with EXTRA env vars —
// the inference carriers ($SESH_THREAD_ID, $TMUX, $TMUX_PANE) a Runner can't
// inject.
func runWithEnv(t *testing.T, sb *Sandbox, extra map[string]string, args ...string) (string, string, error) {
	t.Helper()
	return runWithEnvDir(t, sb, "", extra, args...)
}

// runWithEnvDir is runWithEnv from a specific WORKING DIRECTORY. The caller's
// cwd is now an inference input — it corroborates an unverified
// ($SESH_THREAD_ID-derived) id — so a cell that exercises env inference has to
// stand where the real caller would stand. A headless turn's process cwd IS its
// thread's cwd; dir "" inherits the test process's, which is the repo.
func runWithEnvDir(t *testing.T, sb *Sandbox, dir string, extra map[string]string, args ...string) (string, string, error) {
	t.Helper()
	lr, ok := sb.Runner.(*localRunner)
	if !ok {
		t.Fatalf("runWithEnv needs a local sandbox")
	}
	env := map[string]string{}
	for k, v := range lr.env {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	cmd := exec.Command(lr.bin, args...)
	cmd.Env = sandboxEnv(env)
	cmd.Dir = dir
	return runCmd(cmd)
}

func testInfoLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "describeme", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")
	other := sb.newHeadlessThread(t, "pi", "otherthread")

	// 1. Explicit unique prefix (tid8) — full description of the right thread.
	out, stderr, err := sb.Runner.Run(t, "info", th.ID[:8])
	if err != nil {
		t.Fatalf("info <prefix>: %v\n%s", err, stderr)
	}
	for _, want := range []string{th.ID, "describeme", "pi", "headful", "tmux:"} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}

	// JSON form round-trips the same identity.
	out, _, err = sb.Runner.Run(t, "info", "--id", th.ID, "--json")
	if err != nil {
		t.Fatalf("info --json: %v", err)
	}
	var js struct {
		Thread struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"thread"`
		Head string `json:"head"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &js); err != nil {
		t.Fatalf("info --json decode: %v\n%s", err, out)
	}
	if js.Thread.ID != th.ID || js.Head != "headful" {
		t.Errorf("info --json = %+v, want id %s headful", js, th.ID)
	}

	// 2. Unknown + ambiguous prefixes are loud.
	if _, stderr, err := sb.Runner.Run(t, "info", "zzzzzzzz"); err == nil {
		t.Errorf("unknown prefix succeeded silently")
	} else if !strings.Contains(stderr, "zzzzzzzz") {
		t.Errorf("unknown-prefix error does not echo the ref: %s", stderr)
	}

	// 3. $SESH_THREAD_ID (the env every spawned pane/turn carries), from the
	// thread's own cwd — where a real headless turn runs. It resolves, but as
	// an UNVERIFIED answer: there is no pane here to confirm the id, so both
	// the source field and stderr must say so.
	out, stderr, err = runWithEnvDir(t, sb, other.Cwd, map[string]string{"SESH_THREAD_ID": other.ID}, "info")
	if err != nil {
		t.Fatalf("info via env: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "otherthread") {
		t.Errorf("env inference described the wrong thread:\n%s", out)
	}
	if !strings.Contains(out, "UNVERIFIED") {
		t.Errorf("env-derived info must declare itself unverified:\n%s", out)
	}
	if !strings.Contains(stderr, "unverified") {
		t.Errorf("env-derived info must announce the unverified answer on stderr: %q", stderr)
	}
	out, _, err = runWithEnvDir(t, sb, other.Cwd, map[string]string{"SESH_THREAD_ID": other.ID}, "info", "--json")
	if err != nil {
		t.Fatalf("info --json via env: %v", err)
	}
	var prov struct {
		Source   string `json:"source"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &prov); err != nil {
		t.Fatalf("info --json decode: %v\n%s", err, out)
	}
	if prov.Source != "env" || prov.Verified {
		t.Errorf("env inference reported source=%q verified=%v, want env/false — a caller doing "+
			"something destructive keys off exactly this", prov.Source, prov.Verified)
	}

	// 3b. A STALE env id falls through (here: to no-context, loud) — never a
	// silent wrong answer.
	if _, stderr, err := runWithEnv(t, sb, map[string]string{"SESH_THREAD_ID": "00000000-dead-beef-0000-000000000000"}, "info"); err == nil {
		t.Errorf("stale env id succeeded silently")
	} else if !strings.Contains(stderr, "not inside a sesh thread") {
		t.Errorf("stale-env failure not the loud no-context error: %s", stderr)
	}

	// 3c. THE INCIDENT (ticket d7be88ef), reproduced end to end against a real
	// daemon: NO tmux pane, and $SESH_THREAD_ID naming a thread whose cwd is
	// unrelated to where the caller stands. That is what a detached background
	// job looks like — its inherited env names a perfectly VALID thread that is
	// simply not this one. sesh answered confidently; a self-compact runner
	// built on that answer compacted the victim thread and injected a foreign
	// handover prompt into it.
	//
	// Both directories are siblings under the SAME temp root: t.TempDir() lives
	// under /tmp, so a thread parked at "/tmp" would CONTAIN the caller and read
	// as corroboration. The contradiction has to be between two unrelated trees.
	incidentBase := t.TempDir()
	victimCwd := filepath.Join(incidentBase, "mysetup", "sesh")
	callerCwd := filepath.Join(incidentBase, "dev", "20260822_tsl6xn__boxyard-go")
	for _, d := range []string{victimCwd, callerCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	victim := sb.newHeadlessThreadAt(t, "pi", "mysetup-sesh-victim", victimCwd)

	_, stderr, err = runWithEnvDir(t, sb, callerCwd, map[string]string{"SESH_THREAD_ID": victim.ID}, "info")
	if err == nil {
		t.Errorf("info reported an unrelated thread as the current one with no pane to verify it — " +
			"the reported bug (a self-compact then hijacked that thread)")
	} else {
		for _, want := range []string{victim.ID[:8], "--id", "--allow-unverified"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("the refusal must name %q so the caller can act on it; got: %s", want, stderr)
			}
		}
	}

	// A REFUSAL MUST NAME A FLAG THE COMMAND ACTUALLY HAS. `sesh subscribe`
	// takes --from and has no --id at all, so the generic "Pass --id" remedy
	// sends the caller into `flag provided but not defined: -id`. That happened
	// for real on 2026-08-27: a supervisor ran `sesh subscribe $ID` from a
	// pane-less claude Bash call with a stale $SESH_THREAD_ID, was correctly
	// refused, and its three subscriptions silently did not exist for an hour.
	if _, stderr, err := runWithEnvDir(t, sb, callerCwd,
		map[string]string{"SESH_THREAD_ID": victim.ID}, "subscribe", victim.ID); err == nil {
		t.Errorf("subscribe accepted a contradicted env id as the subscriber:\n%s", stderr)
	} else {
		if !strings.Contains(stderr, "--from") {
			t.Errorf("the subscribe refusal must name --from, the flag this command actually takes; got: %s", stderr)
		}
		if strings.Contains(stderr, "--id ") {
			t.Errorf("the subscribe refusal must NOT suggest --id — `sesh subscribe --id` does not parse; got: %s", stderr)
		}
	}

	// The same call must be refused for a MUTATING verb too — a contradicted id
	// must not be able to act on the victim either.
	if _, stderr, err := runWithEnvDir(t, sb, callerCwd,
		map[string]string{"SESH_THREAD_ID": victim.ID}, "thread", "tag", "--add", "hijacked"); err == nil {
		t.Errorf("a mutating verb acted on a contradicted env id:\n%s", stderr)
	}
	out, _, err = sb.Runner.Run(t, "info", "--id", victim.ID, "--json")
	if err != nil {
		t.Fatalf("victim info: %v", err)
	}
	if strings.Contains(out, "hijacked") {
		t.Errorf("the refused tag reached the victim thread anyway:\n%s", out)
	}

	// --allow-unverified is the deliberate override: same call, now resolves,
	// and still says out loud what it did.
	out, stderr, err = runWithEnvDir(t, sb, callerCwd,
		map[string]string{"SESH_THREAD_ID": victim.ID}, "info", "--allow-unverified")
	if err != nil {
		t.Fatalf("info --allow-unverified: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "mysetup-sesh-victim") {
		t.Errorf("--allow-unverified did not resolve the env thread:\n%s", out)
	}
	if !strings.Contains(stderr, "allow-unverified") {
		t.Errorf("the override must announce itself on stderr: %q", stderr)
	}

	// 4. The calling pane's birth-stamp: $TMUX (socket path) + $TMUX_PANE of
	// the REAL agent pane resolve to its thread.
	sockPath := tmuxSocketPath(sb.TmuxSocket)
	out, stderr, err = runWithEnv(t, sb,
		map[string]string{"TMUX": sockPath + ",1,0", "TMUX_PANE": pane}, "info")
	if err != nil {
		t.Fatalf("info via pane: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "describeme") {
		t.Errorf("pane inference described the wrong thread:\n%s", out)
	}
	// A pane-derived answer is VERIFIED — that is the provenance a destructive
	// caller (self-compact) must require before acting on "itself".
	out, _, err = runWithEnv(t, sb,
		map[string]string{"TMUX": sockPath + ",1,0", "TMUX_PANE": pane}, "info", "--json")
	if err != nil {
		t.Fatalf("info --json via pane: %v", err)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &prov); err != nil {
		t.Fatalf("info --json decode: %v\n%s", err, out)
	}
	if prov.Source != "pane" || !prov.Verified {
		t.Errorf("pane inference reported source=%q verified=%v, want pane/true", prov.Source, prov.Verified)
	}

	// 4b. DRIFT: the pane marker is thread B (describeme) while $SESH_THREAD_ID
	// names a DIFFERENT, still-VALID thread A (otherthread) — the exact
	// adopt/reparent drift bug. The live pane marker must WIN over the frozen
	// env, and a loud drift note must hit stderr (never a silent wrong answer).
	out, stderr, err = runWithEnv(t, sb,
		map[string]string{
			"SESH_THREAD_ID": other.ID,
			"TMUX":           sockPath + ",1,0",
			"TMUX_PANE":      pane,
		}, "info")
	if err != nil {
		t.Fatalf("info under env/marker drift: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "describeme") || strings.Contains(out, "otherthread") {
		t.Errorf("drift: resolved the stale env thread, not the live pane marker:\n%s", out)
	}
	if !strings.Contains(stderr, "stale") || !strings.Contains(stderr, other.ID[:8]) {
		t.Errorf("drift not surfaced loudly on stderr (want stale note naming %s): %q", other.ID[:8], stderr)
	}

	// 5. A retrofitted verb infers identically: `thread status` with no --id.
	out, stderr, err = runWithEnvDir(t, sb, th.Cwd, map[string]string{"SESH_THREAD_ID": th.ID}, "thread", "status")
	if err != nil {
		t.Fatalf("thread status inferred: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "head:") {
		t.Errorf("inferred thread status output wrong:\n%s", out)
	}

	// 6. No context at all: loud.
	if _, stderr, err := sb.Runner.Run(t, "info"); err == nil {
		t.Errorf("info with no context succeeded silently")
	} else if !strings.Contains(stderr, "not inside a sesh thread") {
		t.Errorf("no-context error wrong: %s", stderr)
	}
}

func testInfoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "remoteinfo")

	// Routed: the description comes from the PEER's daemon over the real hop.
	out, stderr, err := sb.Runner.Run(t, "info", "--id", th.ID)
	if err != nil {
		t.Fatalf("routed info: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "remoteinfo") || !strings.Contains(out, th.ID) {
		t.Errorf("routed info wrong:\n%s", out)
	}
}

// tmuxSocketPath is where `tmux -L name` puts its socket.
func tmuxSocketPath(name string) string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), name)
}
