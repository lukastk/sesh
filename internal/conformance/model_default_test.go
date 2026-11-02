package conformance

// model.default cells (schema 58): a per-harness default model in the OWNER
// daemon's config.toml. `model set-default` edits only that key, keeps the
// file's comments and writes THROUGH a symlinked config.toml; a thread created
// without --model then records the default AND the real agent process carries it
// on argv; an explicit --model still wins; a hand edit applies to the next thread
// with no daemon restart; unset-default removes the key and the table it leaves
// empty. Remote = the same commands routed with --machine, so it is the PEER's
// config that changes.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/agents/pi"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("model.default", a, loc,
				func(t *testing.T) { testModelDefault(t, string(a), loc) })
		}
	}
}

// testModels are real, cheap models each harness accepts on this rig: one
// default and one explicit override, in the harness's own spelling. pi uses its
// openai-codex provider: Anthropic answers pi (a third-party app on a Claude
// subscription) with "Third-party apps now draw from your extra usage, not your
// plan limits", so an anthropic/* model cannot run a pi turn here.
var testModels = map[string][2]string{
	"claude": {"haiku", "sonnet"},
	"codex":  {"gpt-6-luna", "gpt-6-astra"},
	"pi":     {"openai-codex/gpt-6-luna", "openai-codex/gpt-6-astra"},
}

func testModelDefault(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	def, override := testModels[agent][0], testModels[agent][1]
	sb := newSandbox(t, loc)

	// The synced-dotfiles layout: config.toml is a link into another directory.
	shared := filepath.Join(t.TempDir(), "shared-config.toml")
	const preamble = "# a comment the writer must keep\n[spawn]\nmode = \"yolo\" # and this one\n"
	if err := os.WriteFile(shared, []byte(preamble), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sb.Home, "config.toml")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)

	out, stderr, err := sb.Runner.Run(t, "model", "set-default", "--agent", agent, "--model", def, "--json")
	if err != nil {
		t.Fatalf("model set-default: %v\n%s", err, stderr)
	}
	var resp api.ModelDefaultsResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if resp.Defaults[agent] != def {
		t.Fatalf("defaults[%s] = %q, want %q", agent, resp.Defaults[agent], def)
	}
	if wantPath, _ := filepath.EvalSymlinks(shared); resp.WrotePath != wantPath {
		t.Errorf("wrote %q, want the link target %q", resp.WrotePath, wantPath)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.toml is no longer a symlink (err=%v)", err)
	}
	text := readFile(t, shared)
	if !strings.HasPrefix(text, preamble) || !strings.Contains(text, "[spawn."+agent+"]\nmodel = \""+def+"\"\n") {
		t.Fatalf("the shared file was not edited in place:\n%s", text)
	}

	// No --model: the default is recorded and on the REAL agent's argv. The daemon
	// was running before the write — no restart.
	th := sb.newThread(t, agent, "defaulted", "/tmp")
	if th.Model != def {
		t.Errorf("new thread recorded model %q, want the default %q", th.Model, def)
	}
	sb.waitThreadReady(t, th.ID, agent)
	assertLiveLaunchModel(t, sb, th, agent, def)

	// An explicit --model still wins.
	out, stderr, err = sb.Runner.Run(t, "thread", "new", "--agent", agent, "--name", "explicit", "--cwd", "/tmp", "--headless", "--model", override, "--json")
	if err != nil {
		t.Fatalf("thread new --model: %v\n%s", err, stderr)
	}
	var explicit api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &explicit); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if explicit.Model != override {
		t.Errorf("explicit --model recorded %q, want %q", explicit.Model, override)
	}

	// A HAND edit of the shared file applies to the next thread, still no restart.
	if err := os.WriteFile(shared, []byte(strings.Replace(text, "\""+def+"\"", "\""+override+"\"", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sb.newHeadlessThread(t, agent, "hand-edited").Model; got != override {
		t.Errorf("after a hand edit the next thread recorded %q, want %q", got, override)
	}

	// Unset: the key and the table it leaves empty go; the comments stay.
	if _, stderr, err := sb.Runner.Run(t, "model", "unset-default", "--agent", agent); err != nil {
		t.Fatalf("model unset-default: %v\n%s", err, stderr)
	}
	if got := readFile(t, shared); got != preamble {
		t.Errorf("unset did not restore the original file:\n%q\nwant\n%q", got, preamble)
	}
	if got := sb.newHeadlessThread(t, agent, "undefaulted").Model; got != "" {
		t.Errorf("with no default a new thread recorded %q, want none", got)
	}
}

// assertLiveLaunchModel proves a running headed agent was launched on model.
// claude/codex: the flag is on the real process's argv. pi rewrites its own
// process title (argv reads just "pi"), so its evidence is the live session's
// model as the pi-rpc-socket extension reports it.
func assertLiveLaunchModel(t *testing.T, sb *Sandbox, th api.Thread, agent, model string) {
	t.Helper()
	if agent != "pi" {
		if argv := agentArgv(t, sb, th.ID); !strings.Contains(argv, "--model "+model) {
			t.Errorf("the %s process does not carry --model %s: %q", agent, model, argv)
		}
		return
	}
	if got := piLiveModel(t, th); got != model {
		t.Errorf("the live pi session runs %q, want %q", got, model)
	}
}

// piLiveModel is a live pi thread's current model (`provider/id`), read over its
// pi-rpc-socket once the extension has created the socket.
func piLiveModel(t *testing.T, th api.Thread) string {
	t.Helper()
	var sock string
	if !waitUntil(30*time.Second, func() bool {
		var ok bool
		sock, ok = findPiRPCSocket(th.AgentSessionID)
		return ok
	}) {
		t.Fatalf("no pi-rpc-socket for session %s", th.AgentSessionID)
	}
	c, err := pi.Dial(sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer c.Close()
	ref, err := c.CurrentModel()
	if err != nil {
		t.Fatalf("pi current model: %v", err)
	}
	return ref.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
