package conformance

// thread.claude-trust (ticket 4b069b88): Claude Code shows a per-workspace
// "Quick safety check … Yes, I trust this folder" dialog for any cwd it has not
// been told to trust, and since 2.1.27x the trust lookup stops at the enclosing
// GIT ROOT, so every freshly cloned box is a new workspace and prompts — with
// --dangerously-skip-permissions set. The dialog eats the first keystrokes
// (Enter picks "No, exit"), which is exactly what breaks "spawn a handful of
// threads and send to them". sesh now pre-trusts the spawn cwd in claude's
// global config (claude.EnsureTrust) on every HEADED launch path.
//
// The cell drives the REAL chain — a real claude, in a real git repo, against
// an ISOLATED claude config dir that trusts nothing — and pins:
//   (1) a fresh spawn comes up at the input prompt, not the dialog, and a
//       `thread send` issued straight after readiness is answered (the
//       reported workflow);
//   (2) the revive path seeds too: with the trust entry deleted behind
//       claude's back (a record created before this fix), `thread headful`
//       still comes up clean.
// waitThreadReady CANNOT catch the dialog by itself — a rendered, byte-stable
// dialog reads as "TUI up, idle" — so the pane text is asserted explicitly.
// Remote = the same via a real `--machine` hop; the seeding happens on the
// OWNER, whose daemon carries the isolated CLAUDE_CONFIG_DIR.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.claude-trust", matrix.Claude, loc,
			func(t *testing.T) { testClaudeTrust(t, loc) })
	}
}

// trustDialogMarkers are the dialog's own words (Claude Code 2.1.273); any of
// them in a "ready" pane means the agent is parked on the safety check.
var trustDialogMarkers = []string{"Quick safety check", "Accessing workspace", "I trust this folder"}

// setupClaudeConfigDir makes an isolated CLAUDE_CONFIG_DIR that is AUTHENTICATED
// but trusts NO workspace: the user's credentials are symlinked in (the
// setupCodexHome precedent), the global config is the user's own minus its
// `projects` map (onboarding/account state kept, every trust entry dropped),
// and the bypass-permissions disclaimer is pre-accepted in settings.json so the
// only dialog left standing is the one under test. Fails loudly when the user
// has no claude config — a claude cell needs a logged-in claude.
//
// Its own dir with a retrying cleanup, like setupCodexHome: claude keeps
// writing (transcripts, stats) as the pane is killed.
func setupClaudeConfigDir(t *testing.T) string {
	t.Helper()
	uh, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	realCfg := filepath.Join(uh, ".claude.json")
	raw, err := os.ReadFile(realCfg)
	if err != nil {
		t.Fatalf("thread.claude-trust needs a logged-in claude (%s): %v", realCfg, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("%s is not a JSON object: %v", realCfg, err)
	}
	delete(top, "projects")
	seed, err := json.Marshal(top)
	if err != nil {
		t.Fatal(err)
	}

	dir, err := os.MkdirTemp("", "sesh-claude-cfg-")
	if err != nil {
		t.Fatalf("setup claude config dir: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"skipDangerousModePermissionPrompt": true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realCreds := filepath.Join(uh, ".claude", ".credentials.json")
	if _, err := os.Stat(realCreds); err != nil {
		t.Fatalf("thread.claude-trust needs claude credentials at %s: %v", realCreds, err)
	}
	if err := os.Symlink(realCreds, filepath.Join(dir, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// trustEntry reads projects[key].hasTrustDialogAccepted from the isolated config.
func trustEntry(t *testing.T, cfgDir, key string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cfgDir, ".claude.json"))
	if err != nil {
		t.Fatalf("read isolated claude config: %v", err)
	}
	var top struct {
		Projects map[string]map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("isolated claude config is not valid JSON: %v\n%s", err, raw)
	}
	v, ok := top.Projects[key]["hasTrustDialogAccepted"]
	return string(v), ok
}

// dropTrustEntry deletes projects[key] from the isolated config — the shape of
// a thread whose record predates the fix (or a config claude rewrote).
func dropTrustEntry(t *testing.T, cfgDir, key string) {
	t.Helper()
	p := filepath.Join(cfgDir, ".claude.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	projects := map[string]json.RawMessage{}
	if p, ok := top["projects"]; ok {
		if err := json.Unmarshal(p, &projects); err != nil {
			t.Fatal(err)
		}
	}
	delete(projects, key)
	enc, _ := json.Marshal(projects)
	top["projects"] = enc
	out, _ := json.Marshal(top)
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertNoTrustDialog fails, naming the bug, when the pane shows the dialog.
func (sb *Sandbox) assertNoTrustDialog(t *testing.T, pane, when string) {
	t.Helper()
	cap, err := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	for _, m := range trustDialogMarkers {
		if strings.Contains(cap, m) {
			t.Fatalf("%s: claude is parked on its workspace-trust dialog (%q) — sesh did not pre-trust the cwd, so the next `thread send` would land in the dialog.\npane:\n%s", when, m, cap)
		}
	}
}

// answeredSend sends a computed-sentinel prompt through sesh's own delivery and
// waits for the REPLY (the prompt text is echoed in the pane, so the sentinel
// is a sum the model must compute — it never appears in the prompt).
func (sb *Sandbox) answeredSend(t *testing.T, id, pane, when string) {
	t.Helper()
	a, b := 10+rand.Intn(40), 10+rand.Intn(40)
	want := fmt.Sprintf("SESHOK-%d", a+b)
	prompt := fmt.Sprintf("Reply with exactly the string SESHOK-N where N is the number %d+%d written as digits, and nothing else.", a, b)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", id, "--text", prompt); err != nil {
		t.Fatalf("%s: thread send: %v\n%s", when, err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		return strings.Contains(cap, want)
	}) {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		t.Fatalf("%s: the send was never answered (want %q in the pane) — input did not reach the agent.\npane:\n%s", when, want, cap)
	}
}

func testClaudeTrust(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	cfgDir := setupClaudeConfigDir(t)
	sb := newSandbox(t, loc, withSandboxEnv("CLAUDE_CONFIG_DIR", cfgDir))
	sb.startDaemon(t)

	// The reported shape: a fresh GIT REPO (a box). Claude's trust walk stops
	// at the repo root, so no ancestor could vouch for it even if one were
	// trusted; here nothing is trusted at all, which is stricter still.
	cwd := filepath.Join(t.TempDir(), "box")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", cwd).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if got, ok := trustEntry(t, cfgDir, cwd); ok {
		t.Fatalf("precondition: %q is already trusted (%s) in the isolated config", cwd, got)
	}

	// (1) Spawn, then send straight after readiness — the workflow from the
	// ticket. The trust entry must be on disk (the mechanism) and the pane must
	// not be the dialog (the effect); the answered send is the proof that input
	// flowed into the agent.
	th := sb.newThread(t, "claude", "trustme", cwd)
	if got, ok := trustEntry(t, cfgDir, cwd); !ok || got != "true" {
		t.Fatalf("spawn did not seed projects[%q].hasTrustDialogAccepted in %s (got %q, present=%v)", cwd, cfgDir, got, ok)
	}
	pane := sb.waitThreadReady(t, th.ID, "claude")
	sb.assertNoTrustDialog(t, pane, "fresh spawn")
	sb.answeredSend(t, th.ID, pane, "fresh spawn")

	// (2) Revive path: kill the runtime, delete the trust entry behind claude's
	// back (the record predates the fix), headful again — must still seed.
	// The entry is dropped only once the old claude PROCESS is gone: a dying
	// claude rewrites its config on the way out, and a write landing after the
	// drop would put the entry back and make the revive assertion vacuous.
	_, pid, ok := sb.markedPane(t, th.ID)
	if !ok {
		t.Fatalf("no marked pane for %s before stop", th.ID)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID); err != nil {
		t.Fatalf("stop: %v\n%s", err, stderr)
	}
	if !waitUntil(20*time.Second, func() bool { return !processAlive(pid) }) {
		t.Fatalf("claude pid %d still alive 20s after thread stop", pid)
	}
	dropTrustEntry(t, cfgDir, cwd)
	if _, ok := trustEntry(t, cfgDir, cwd); ok {
		t.Fatalf("precondition: trust entry for %q still present after dropping it", cwd)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID); err != nil {
		t.Fatalf("headful: %v\n%s", err, stderr)
	}
	if got, ok := trustEntry(t, cfgDir, cwd); !ok || got != "true" {
		t.Fatalf("revive did not re-seed projects[%q].hasTrustDialogAccepted (got %q, present=%v)", cwd, got, ok)
	}
	pane = sb.waitThreadReady(t, th.ID, "claude")
	sb.assertNoTrustDialog(t, pane, "revive")
	sb.answeredSend(t, th.ID, pane, "revive")
}

// processAlive reports whether pid still exists (signal 0 probes without sending).
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
