package conformance

// thread.spawn-mode cells (PARITY_ROADMAP E3): the [spawn] launch policy is
// OBSERVABLE on the real agent process's argv — config yolo puts the bypass
// flag there, a --sandbox override puts the restriction there (codex), pi
// refuses sandbox loudly, the args passthrough lands literally, and --msg
// reaches the real conversation once the agent is ready (never the blank-pane
// race). LOCAL-only: the mode comes from the OWNER's config; routed spawns
// exercise the identical daemon path.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
)

func init() {
	for _, agent := range []matrix.Agent{matrix.Claude, matrix.Codex, matrix.Pi} {
		a := agent
		matrix.RegisterTest("thread.spawn-mode", a, matrix.Local,
			func(t *testing.T) { testSpawnMode(t, string(a)) })
	}
}

// agentArgv returns the live agent process's command line for a thread.
func agentArgv(t *testing.T, sb *Sandbox, id string) string {
	t.Helper()
	_, pid, ok := sb.markedPane(t, id)
	if !ok {
		t.Fatalf("no marked pane for %s", id)
	}
	if agent, running := tmux.AgentUnderPane(pid); running {
		return agent.Command
	}
	t.Fatalf("agent process not found for %s", id)
	return ""
}

func testSpawnMode(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	// Config: global yolo + an args passthrough for this agent.
	conf := `
[spawn]
mode = "yolo"
`
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)

	// Config yolo is on the REAL process's argv (pi: yolo ≡ default — no flag,
	// and the spawn must still work).
	th := sb.newThread(t, agent, "yoloed", "/tmp")
	sb.waitThreadReady(t, th.ID, agent)
	argv := agentArgv(t, sb, th.ID)
	switch agent {
	case "claude":
		if !strings.Contains(argv, "--dangerously-skip-permissions") {
			t.Errorf("claude yolo missing the bypass flag: %q", argv)
		}
	case "codex":
		if !strings.Contains(argv, "--dangerously-bypass-approvals-and-sandbox") {
			t.Errorf("codex yolo missing the bypass flag: %q", argv)
		}
	case "pi":
		if strings.Contains(argv, "--dangerously") || strings.Contains(argv, "--sandbox") {
			t.Errorf("pi grew permission flags it doesn't have: %q", argv)
		}
	}

	switch agent {
	case "codex":
		// A --sandbox override beats the yolo config: the restriction is on argv.
		th2 := sb.newThread2(t, "codex", "boxed", "/tmp", "--sandbox")
		sb.waitThreadReady(t, th2.ID, "codex")
		argv2 := agentArgv(t, sb, th2.ID)
		if !strings.Contains(argv2, "--sandbox") || !strings.Contains(argv2, "read-only") {
			t.Errorf("codex --sandbox override missing: %q", argv2)
		}
		if strings.Contains(argv2, "--dangerously-bypass") {
			t.Errorf("codex --sandbox still carries the yolo flag: %q", argv2)
		}
	case "pi":
		// pi + sandbox: loud refusal at spawn.
		if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "nope", "--cwd", "/tmp", "--sandbox"); err == nil {
			t.Errorf("pi --sandbox spawn succeeded (must refuse)")
		} else if !strings.Contains(stderr, "cannot be sandboxed") {
			t.Errorf("pi sandbox refusal wrong: %s", stderr)
		}
		// --msg: the initial prompt reaches the REAL conversation (the daemon
		// waits for readiness — never the blank-pane race).
		th3 := sb.newThread2(t, "pi", "greeted", "/tmp", "--msg", "Reply with exactly the word LILAC and nothing else")
		if !waitUntil(120*time.Second, func() bool {
			out, _, err := sb.Runner.Run(t, "thread", "transcript", "--id", th3.ID, "--json")
			return err == nil && strings.Contains(out, "LILAC")
		}) {
			t.Errorf("--msg never reached the conversation")
		}
	case "claude":
		// args passthrough: a literal extra arg lands on argv.
		conf2 := "[spawn]\nmode = \"yolo\"\n[spawn.claude]\nargs = [\"--model\", \"sonnet\"]\n"
		if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte(conf2), 0o644); err != nil {
			t.Fatal(err)
		}
		// The daemon reads [spawn] at start: restart it for the new args.
		if _, stderr, err := sb.daemonRunner.Run(t, "daemon", "restart"); err != nil {
			t.Fatalf("daemon restart: %v\n%s", err, stderr)
		}
		th4 := sb.newThread(t, "claude", "argued", "/tmp")
		sb.waitThreadReady(t, th4.ID, "claude")
		argv4 := agentArgv(t, sb, th4.ID)
		if !strings.Contains(argv4, "--model sonnet") && !strings.Contains(argv4, "--model") {
			t.Errorf("args passthrough missing: %q", argv4)
		}
	}
}

// newThread2 is newThread with extra flags appended.
func (sb *Sandbox) newThread2(t *testing.T, agent, name, cwd string, extra ...string) api.Thread {
	t.Helper()
	args := append([]string{"thread", "new", "--agent", agent, "--name", name, "--cwd", cwd, "--json"}, extra...)
	out, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return th
}
