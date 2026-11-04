package conformance

// thread.mcp-arg cells: a thread's MCP arguments reach the REAL server process that
// the REAL harness starts. Each sandbox gives its harness a global `soliton` entry
// pointing at a small stdio MCP server that appends its own argv to a log, so every
// assertion reads what a process was actually started with, and a green cell cannot
// come from a flag that was merely built. Covered per agent and locality: a headless
// first turn and a resumed one, set and unset, a fork, a headed spawn and its resume.
// The arguments include shell metacharacters; a canary file proves none ran.
// Remote cells route every command over a real ssh hop.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range matrix.AllAgents {
		for _, loc := range matrix.AllLocalities {
			a, l := agent, loc
			matrix.RegisterTest("thread.mcp-arg", a, l,
				func(t *testing.T) { testThreadMcpArg(t, string(a), l) })
		}
	}
}

const mcpGlobalArg = "--global"

const fakeMcpServer = `#!/usr/bin/env python3
import json, sys
with open(sys.argv[1], "a") as log:
    log.write(json.dumps(sys.argv[2:]) + "\n")
for line in sys.stdin:
    message = json.loads(line)
    if "id" not in message:
        continue
    if message.get("method") == "initialize":
        result = {"protocolVersion": message["params"]["protocolVersion"], "capabilities": {"tools": {}},
                  "serverInfo": {"name": "fake", "version": "1"}}
    elif message.get("method") == "tools/list":
        result = {"tools": []}
    else:
        result = {}
    print(json.dumps({"jsonrpc": "2.0", "id": message["id"], "result": result}), flush=True)
`

// fakeMcp is one sandbox's server executable and the log of its starts.
type fakeMcp struct {
	script, log string
	seen        int
}

func newFakeMcp(t *testing.T) *fakeMcp {
	t.Helper()
	dir := t.TempDir()
	f := &fakeMcp{script: filepath.Join(dir, "fake-mcp.py"), log: filepath.Join(dir, "starts.log")}
	if err := os.WriteFile(f.script, []byte(fakeMcpServer), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeMcp) starts(t *testing.T) [][]string {
	t.Helper()
	file, err := os.Open(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out [][]string
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		var argv []string
		if err := json.Unmarshal(lines.Bytes(), &argv); err != nil {
			t.Fatalf("server log line %q: %v", lines.Text(), err)
		}
		out = append(out, argv)
	}
	return out
}

// expect waits for at least one new start and requires every new start to have
// exactly the global argument followed by threadArgs.
func (f *fakeMcp) expect(t *testing.T, when string, threadArgs ...string) {
	t.Helper()
	want := append([]string{mcpGlobalArg}, threadArgs...)
	if !waitUntil(60*time.Second, func() bool { return len(f.starts(t)) > f.seen }) {
		t.Fatalf("%s: the server was never started", when)
	}
	all := f.starts(t)
	for _, got := range all[f.seen:] {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: server argv = %q, want %q", when, got, want)
		}
	}
	f.seen = len(all)
}

// expectNoThreadArgs is what a turn without arguments must show. claude and codex
// start every global entry themselves, so the server runs with its global argument
// alone. pi's extension acts only on the flag and the entry is disabled for pi's own
// MCP support, as on the rig, so the server must not have started at all.
func (f *fakeMcp) expectNoThreadArgs(t *testing.T, agent, when string) {
	t.Helper()
	if agent != "pi" {
		f.expect(t, when)
		return
	}
	if all := f.starts(t); len(all) != f.seen {
		t.Errorf("%s: the server was started: %q", when, all[f.seen:])
	}
}

// mcpSandboxOpts returns the sandbox options giving agent a global `soliton`
// entry, except codex, whose home exists only once the sandbox does (see
// installCodexMcpEntry).
func mcpSandboxOpts(t *testing.T, agent string, f *fakeMcp) []sandboxOpt {
	t.Helper()
	switch agent {
	case "claude":
		dir := setupClaudeConfigDir(t)
		path := filepath.Join(dir, ".claude.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg["mcpServers"] = map[string]any{"soliton": map[string]any{
			"type": "stdio", "command": f.script, "args": []string{f.log, mcpGlobalArg},
		}}
		out, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatal(err)
		}
		return []sandboxOpt{withSandboxEnv("CLAUDE_CONFIG_DIR", dir)}
	case "pi":
		dir := piRPCSettings(t)
		entry, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"soliton": map[string]any{
			"command": f.script, "args": []string{f.log, mcpGlobalArg}, "disabled": true,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mcp.json"), entry, 0o644); err != nil {
			t.Fatal(err)
		}
		// The extension of THIS tree, not the one the machine has installed.
		extension, err := filepath.Abs("../../integrations/pi/sesh-agent-state/index.ts")
		if err != nil {
			t.Fatal(err)
		}
		settingsPath := filepath.Join(dir, "settings.json")
		raw, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		var settings map[string]any
		if err := json.Unmarshal(raw, &settings); err != nil {
			t.Fatal(err)
		}
		extensions, _ := settings["extensions"].([]any)
		if len(extensions) != 2 {
			t.Fatalf("piRPCSettings changed shape: extensions = %v", extensions)
		}
		// -builtin:mcp as on every machine of the rig: left on, pi's own MCP support
		// starts a second copy of the entry, without the thread's arguments.
		settings["extensions"] = []any{extensions[0], extension, "-builtin:mcp"}
		out, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settingsPath, out, 0o600); err != nil {
			t.Fatal(err)
		}
		return []sandboxOpt{withSandboxEnv("PI_CODING_AGENT_DIR", dir)}
	default:
		return nil
	}
}

func installCodexMcpEntry(t *testing.T, sb *Sandbox, f *fakeMcp) {
	t.Helper()
	config := fmt.Sprintf("[mcp_servers.soliton]\ncommand = %q\nargs = [%q, %q]\n", f.script, f.log, mcpGlobalArg)
	if err := os.WriteFile(filepath.Join(sb.codexHome(t), "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (sb *Sandbox) mcpArgList(t *testing.T, id string) map[string][]string {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, "thread", "mcp-arg", "list", "--id", id, "--json")
	if err != nil {
		t.Fatalf("mcp-arg list: %v\n%s", err, stderr)
	}
	var listed struct {
		McpArgs map[string][]string `json:"mcp_args"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("mcp-arg list: %v\n%s", err, out)
	}
	return listed.McpArgs
}

func testThreadMcpArg(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	server := newFakeMcp(t)
	sb := newSandbox(t, loc, mcpSandboxOpts(t, agent, server)...)
	if agent == "codex" {
		installCodexMcpEntry(t, sb, server)
	}
	sb.startDaemon(t)

	var model []string
	if agent == "claude" {
		model = []string{"--model", "haiku"}
	}
	canary := filepath.Join(t.TempDir(), "canary")
	hostile := "--agent-file=/tmp/it's a $(touch " + canary + "); `touch " + canary + "` \"x\" \\y"
	const plain = "--plain"
	const reply = "Reply with exactly the word OK and nothing else"

	// Headless: the first turn, then a resumed one.
	args := append([]string{"thread", "new", "--agent", agent, "--name", "mcp", "--cwd", "/tmp", "--headless", "--json",
		"--mcp-arg", "soliton=" + hostile, "--mcp-arg", "soliton=" + plain}, model...)
	out, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(out), &th); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	want := map[string][]string{"soliton": {hostile, plain}}
	if got := sb.mcpArgList(t, th.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %q, want %q", got, want)
	}
	sb.headlessTurn(t, th.ID, reply)
	server.expect(t, "first headless turn", hostile, plain)
	sb.headlessTurn(t, th.ID, reply)
	server.expect(t, "resumed headless turn", hostile, plain)

	if agent == "claude" {
		file := filepath.Join(sb.Home, agents.McpDirName, th.ID, "mcp.json")
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("generated file mode = %o, want 600", info.Mode().Perm())
		}
		dir, err := os.Stat(filepath.Dir(file))
		if err != nil {
			t.Fatal(err)
		}
		if dir.Mode().Perm() != 0o700 {
			t.Errorf("generated folder mode = %o, want 700", dir.Mode().Perm())
		}
	}

	// set replaces, and the next start has it.
	const replaced = "--replaced"
	if _, stderr, err := sb.Runner.Run(t, "thread", "mcp-arg", "set", "--id", th.ID, "--mcp-arg", "soliton="+replaced); err != nil {
		t.Fatalf("mcp-arg set: %v\n%s", err, stderr)
	}
	sb.headlessTurn(t, th.ID, reply)
	server.expect(t, "turn after set", replaced)

	// A fork inherits nothing, on the record or at the process.
	out, stderr, err = sb.Runner.Run(t, "thread", "new", "--fork-from", th.ID, "--name", "branch", "--json")
	if err != nil {
		t.Fatalf("fork: %v\n%s", err, stderr)
	}
	var fork api.Thread
	if err := json.Unmarshal([]byte(out), &fork); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got := sb.mcpArgList(t, fork.ID); len(got) != 0 {
		t.Errorf("the fork inherited %q", got)
	}
	sb.headlessTurn(t, fork.ID, reply)
	server.expectNoThreadArgs(t, agent, "fork's turn")

	// unset leaves the global entry alone.
	if _, stderr, err := sb.Runner.Run(t, "thread", "mcp-arg", "unset", "--id", th.ID, "--server", "soliton"); err != nil {
		t.Fatalf("mcp-arg unset: %v\n%s", err, stderr)
	}
	sb.headlessTurn(t, th.ID, reply)
	server.expectNoThreadArgs(t, agent, "turn after unset")
	if _, _, err := sb.Runner.Run(t, "thread", "mcp-arg", "unset", "--id", th.ID, "--server", "soliton"); err == nil {
		t.Error("unset of a server with no arguments succeeded")
	}

	// Headed: a spawn, then a resume.
	headed := sb.newThread2(t, agent, "mcp-headed", "/tmp", append([]string{"--mcp-arg", "soliton=" + hostile}, model...)...)
	sb.waitThreadReady(t, headed.ID, agent)
	server.expect(t, "headed spawn", hostile)
	// A real turn: resume needs a conversation, and codex mints its id on the first.
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", headed.ID, "--text", reply); err != nil {
		t.Fatalf("send: %v\n%s", err, stderr)
	}
	if !waitUntil(45*time.Second, func() bool { return sb.threadStatus(t, headed.ID).Busy == api.BusyBusy }) {
		t.Fatalf("headed turn never started")
	}
	if !waitUntil(120*time.Second, func() bool { return sb.threadStatus(t, headed.ID).Busy == api.BusyIdle }) {
		t.Fatalf("headed turn never completed")
	}
	if agent == "codex" {
		sb.waitStamped(t, headed.ID)
	}
	sb.stopAndWait(t, headed.ID, "before resume")
	if _, stderr, err := sb.Runner.Run(t, "thread", "resume", "--id", headed.ID); err != nil {
		t.Fatalf("resume: %v\n%s", err, stderr)
	}
	sb.waitThreadReady(t, headed.ID, agent)
	server.expect(t, "headed resume", hostile)

	if _, err := os.Stat(canary); err == nil {
		t.Error("a shell metacharacter in an argument ran")
	}

	// Deleting a thread removes what was generated for it.
	sb.stopAndWait(t, headed.ID, "before delete")
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", headed.ID, "--force"); err != nil {
		t.Fatalf("delete: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(sb.Home, agents.McpDirName, headed.ID)); !os.IsNotExist(err) {
		t.Errorf("the generated folder survived its thread: %v", err)
	}
}

// A global entry a generated file cannot carry refuses the launch, naming why, and
// a server the harness does not have refuses it too. Claude alone needs a daemon
// here; the same checks for codex are unit tests.
func TestMcpArgRefusals(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := setupClaudeConfigDir(t)
	path := filepath.Join(dir, ".claude.json")
	entry := `{"mcpServers":{"withenv":{"command":"/bin/true","env":{"KEY":"value"}}}}`
	if err := os.WriteFile(path, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := newSandbox(t, matrix.Local, withSandboxEnv("CLAUDE_CONFIG_DIR", dir))
	sb.startDaemon(t)
	for flag, want := range map[string]string{"withenv=x": "env block", "nosuch=x": "no global MCP server"} {
		_, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "claude", "--cwd", "/tmp", "--name", "refused", "--mcp-arg", flag)
		if err == nil || !strings.Contains(stderr, want) {
			t.Errorf("--mcp-arg %s: err=%v stderr=%q, want it to mention %q", flag, err, stderr, want)
		}
	}
	for _, bad := range []string{"noequals", "=arg", "bad.name=x", "soliton="} {
		if _, _, err := sb.Runner.Run(t, "thread", "new", "--agent", "claude", "--cwd", "/tmp", "--mcp-arg", bad); err == nil {
			t.Errorf("--mcp-arg %q was accepted", bad)
		}
	}
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--virtual", "--mcp-arg", "soliton=x"); err == nil {
		t.Error("a virtual thread accepted --mcp-arg")
	}
}

// --machine over HTTP keeps the command in this process and redirects its daemon
// client, unlike ssh, so the record path is proved on that transport too.
func TestMcpArgRoutedHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	for _, tr := range meshTransports {
		if tr.suffix == "" {
			continue
		}
		sb := setupRoutedPeer(t, tr)
		arg := "--agent-file=/peer/path with space"
		out, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--cwd", "/tmp", "--name", "routed",
			"--headless", "--json", "--mcp-arg", "soliton="+arg)
		if err != nil {
			t.Fatalf("thread new: %v\n%s", err, stderr)
		}
		var th api.Thread
		if err := json.Unmarshal([]byte(out), &th); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		if th.Machine != sb.Machine {
			t.Errorf("created on %q, want the peer %q", th.Machine, sb.Machine)
		}
		if got := sb.mcpArgList(t, th.ID); !reflect.DeepEqual(got["soliton"], []string{arg}) {
			t.Errorf("list = %q", got)
		}
		if _, stderr, err := sb.Runner.Run(t, "thread", "mcp-arg", "set", "--id", th.ID, "--mcp-arg", "other=y"); err != nil {
			t.Fatalf("mcp-arg set: %v\n%s", err, stderr)
		}
		if got := sb.mcpArgList(t, th.ID); !reflect.DeepEqual(got, map[string][]string{"soliton": {arg}, "other": {"y"}}) {
			t.Errorf("list after set = %q", got)
		}
	}
}
