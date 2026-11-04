package conformance

// thread.mcp-tool, thread.mcp-list-changed and thread.await-turn-failure cells. The
// thread.mcp-arg cells prove what a server was started with; these prove that it is
// usable and that its failure is seen. Every assertion reads what the fake server's own
// logs recorded or what a caller of the CLI gets back.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range matrix.AllAgents {
		for _, loc := range matrix.AllLocalities {
			a, l := agent, loc
			matrix.RegisterTest("thread.mcp-tool", a, l,
				func(t *testing.T) { testThreadMcpTool(t, string(a), l) })
			matrix.RegisterTest("thread.mcp-list-changed", a, l,
				func(t *testing.T) { testThreadMcpListChanged(t, string(a), l) })
			matrix.RegisterTest("thread.await-turn-failure", a, l,
				func(t *testing.T) { testThreadAwaitTurnFailure(t, string(a), l) })
		}
	}
}

const callSecretWord = "Call the MCP tool whose name ends in secret_word and reply with exactly what it returned, nothing else."

type mcpCall struct {
	Tool string   `json:"tool"`
	Argv []string `json:"argv"`
}

func (f *fakeMcp) calls(t *testing.T) []mcpCall {
	t.Helper()
	file, err := os.Open(f.log + ".calls")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []mcpCall
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		var call mcpCall
		if err := json.Unmarshal(lines.Bytes(), &call); err != nil {
			t.Fatalf("call log line %q: %v", lines.Text(), err)
		}
		out = append(out, call)
	}
	return out
}

// calledBy reports a call of the tool by a server process started with arg.
func (f *fakeMcp) calledBy(t *testing.T, arg string) bool {
	t.Helper()
	for _, call := range f.calls(t) {
		if call.Tool == "secret_word" && len(call.Argv) > 0 && call.Argv[len(call.Argv)-1] == arg {
			return true
		}
	}
	return false
}

func (f *fakeMcp) hold(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.log+".hold", nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeMcp) release(t *testing.T) {
	t.Helper()
	if err := os.Remove(f.log + ".hold"); err != nil {
		t.Fatal(err)
	}
}

// mcpSandbox is a daemon whose harness has the fake server as its global entry, run
// in yolo as production runs them: a non-interactive turn may not call a tool otherwise.
func mcpSandbox(t *testing.T, agent string, loc matrix.Locality, server *fakeMcp) *Sandbox {
	t.Helper()
	sb := newSandbox(t, loc, mcpSandboxOpts(t, agent, server)...)
	if agent == "codex" {
		installCodexMcpEntry(t, sb, server)
	}
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte("[spawn]\nmode = \"yolo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)
	return sb
}

func mcpModelArgs(agent string) []string {
	if agent == "claude" {
		return []string{"--model", "haiku"}
	}
	return nil
}

func (sb *Sandbox) newMcpHeadless(t *testing.T, agent, name string, mcpArgs ...string) api.Thread {
	t.Helper()
	args := []string{"thread", "new", "--agent", agent, "--name", name, "--cwd", "/tmp", "--headless", "--json"}
	for _, arg := range mcpArgs {
		args = append(args, "--mcp-arg", "soliton="+arg)
	}
	out, stderr, err := sb.Runner.Run(t, append(args, mcpModelArgs(agent)...)...)
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(out), &th); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return th
}

// sendInPane sends a turn and waits for the tool call it asks for.
func (sb *Sandbox) sendInPane(t *testing.T, server *fakeMcp, id, arg string) {
	t.Helper()
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", id, "--text", callSecretWord); err != nil {
		t.Fatalf("send: %v\n%s", err, stderr)
	}
	if !waitUntil(150*time.Second, func() bool { return server.calledBy(t, arg) }) {
		t.Fatalf("the agent in the pane never called the tool of the server started with %q; calls: %+v", arg, server.calls(t))
	}
}

func testThreadMcpTool(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	server := newFakeMcp(t)
	sb := mcpSandbox(t, agent, loc, server)

	const headlessArg, headedArg = "--identity-word-osprey", "--identity-word-marten"
	th := sb.newMcpHeadless(t, agent, "tool", headlessArg)
	sb.headlessTurnArgs(t, th.ID, callSecretWord)
	if !server.calledBy(t, headlessArg) {
		t.Fatalf("no call reached the server started with %q; calls: %+v", headlessArg, server.calls(t))
	}
	if reply := sb.headlessReply(t, th.ID).Reply; !strings.Contains(reply, headlessArg) {
		t.Errorf("the reply does not hold what the tool returned: %q", reply)
	}

	headed := sb.newThread2(t, agent, "tool-headed", "/tmp", append([]string{"--mcp-arg", "soliton=" + headedArg}, mcpModelArgs(agent)...)...)
	sb.waitThreadReady(t, headed.ID, agent)
	sb.sendInPane(t, server, headed.ID, headedArg)
}

func testThreadMcpListChanged(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if agent == "codex" {
		t.Skip("NOT IMPLEMENTED: codex 0.166.0 ignores tools/list_changed, so a long-lived codex cannot recover; the harness's own limit, measured in _dev/THREAD_MCP_ARGS.md")
	}
	server := newFakeMcp(t)
	server.hold(t)
	sb := mcpSandbox(t, agent, loc, server)

	const headedArg = "--identity-word-heron"
	headed := sb.newThread2(t, agent, "listed-late", "/tmp", append([]string{"--mcp-arg", "soliton=" + headedArg}, mcpModelArgs(agent)...)...)
	sb.waitThreadReady(t, headed.ID, agent)
	server.expect(t, "headed spawn", headedArg)
	if got := server.calls(t); len(got) != 0 {
		t.Fatalf("a call before there was a tool: %+v", got)
	}
	server.release(t)
	// The same process must take the call: no second start.
	sb.sendInPane(t, server, headed.ID, headedArg)
	if all := server.starts(t); len(all) != server.seen {
		t.Errorf("the server was started again: %q", all[server.seen:])
	}
	if agent != "pi" {
		return
	}

	// Headless, the turn's process is all there is, so it waits for the tools.
	const lateArg = "--identity-word-plover"
	server.hold(t)
	th := sb.newMcpHeadless(t, agent, "late-turn", lateArg)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID, "--text", callSecretWord); err != nil {
		t.Fatalf("send-headless: %v\n%s", err, stderr)
	}
	if !waitUntil(60*time.Second, func() bool { return len(server.starts(t)) > server.seen }) {
		t.Fatal("the turn never started the server")
	}
	server.seen = len(server.starts(t))
	time.Sleep(4 * time.Second)
	server.release(t)
	if out, stderr, err := sb.Runner.Run(t, "await", "--id", th.ID, "--timeout", "150s", "--json"); err != nil {
		t.Fatalf("await: %v\n%s\n%s", err, out, stderr)
	}
	if !server.calledBy(t, lateArg) {
		t.Fatalf("the turn did not call the tool that arrived during it; calls: %+v", server.calls(t))
	}

	// When they never come, the worker fails instead of answering without them.
	server.hold(t)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID, "--text", callSecretWord); err != nil {
		t.Fatalf("send-headless: %v\n%s", err, stderr)
	}
	out, stderr, err := sb.Runner.Run(t, "await", "--id", th.ID, "--timeout", "150s", "--json")
	if err == nil {
		t.Fatalf("await exited 0 on a worker that had no tools: %s", out)
	}
	if !strings.Contains(out, "lists no tools") {
		t.Errorf("await's reason: stdout %q stderr %q", out, stderr)
	}
	server.release(t)
}

// turnFailure is what makes one real harness fail one headless turn: a model that does
// not exist, or for pi, which warns about one and carries on, a server that cannot start.
type turnFailure struct {
	arm, disarm func(t *testing.T, sb *Sandbox, id string)
	sendFlags   []string
}

func failureFor(agent string) turnFailure {
	if agent != "pi" {
		return turnFailure{
			arm:       func(*testing.T, *Sandbox, string) {},
			disarm:    func(*testing.T, *Sandbox, string) {},
			sendFlags: []string{"--model", "no-such-model-sesh-conformance"},
		}
	}
	const absent = "no-such-global-server"
	return turnFailure{
		arm: func(t *testing.T, sb *Sandbox, id string) {
			if _, stderr, err := sb.Runner.Run(t, "thread", "mcp-arg", "set", "--id", id, "--mcp-arg", absent+"=--x"); err != nil {
				t.Fatalf("mcp-arg set: %v\n%s", err, stderr)
			}
		},
		disarm: func(t *testing.T, sb *Sandbox, id string) {
			if _, stderr, err := sb.Runner.Run(t, "thread", "mcp-arg", "unset", "--id", id, "--server", absent); err != nil {
				t.Fatalf("mcp-arg unset: %v\n%s", err, stderr)
			}
		},
	}
}

func testThreadAwaitTurnFailure(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	server := newFakeMcp(t)
	sb := mcpSandbox(t, agent, loc, server)
	th := sb.newMcpHeadless(t, agent, "fails")
	failure := failureFor(agent)

	// A good turn first. await straight after the send has to wait for it, or a caller
	// reads the idle from before the turn began.
	send := func(text string, flags ...string) {
		t.Helper()
		args := append([]string{"thread", "send-headless", "--id", th.ID, "--text", text}, flags...)
		if _, stderr, err := sb.Runner.Run(t, args...); err != nil {
			t.Fatalf("send-headless: %v\n%s", err, stderr)
		}
	}
	const reply = "Reply with exactly the word OK and nothing else"
	send(reply)
	if out, stderr, err := sb.Runner.Run(t, "await", "--id", th.ID, "--timeout", "150s", "--json"); err != nil {
		t.Fatalf("await on a good turn: %v\n%s\n%s", err, out, stderr)
	} else if strings.Contains(out, "turn_error") {
		t.Errorf("a good turn carries turn_error: %s", out)
	}
	if r := sb.headlessReply(t, th.ID); r.Working || !r.HaveReply {
		t.Fatalf("await returned before the turn finished: %+v", r)
	}

	failure.arm(t, sb, th.ID)
	send(reply, failure.sendFlags...)
	expectFailed := func(when string) {
		t.Helper()
		out, stderr, err := sb.Runner.Run(t, "await", "--id", th.ID, "--timeout", "150s", "--json")
		if err == nil {
			t.Fatalf("%s: await exited 0 on a failed turn: %s", when, out)
		}
		var got struct {
			Busy      api.Busy `json:"busy"`
			TurnError string   `json:"turn_error"`
		}
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("%s: await --json: %v\nstdout %q\nstderr %q", when, jerr, out, stderr)
		}
		if got.Busy != api.BusyIdle || got.TurnError == "" {
			t.Errorf("%s: await said %+v", when, got)
		}
		if !strings.Contains(stderr, got.TurnError) {
			t.Errorf("%s: stderr %q lacks %q", when, stderr, got.TurnError)
		}
		status := sb.threadStatus(t, th.ID)
		if status.TurnError == "" {
			t.Errorf("%s: thread status does not show it: %+v", when, status)
		}
		info, stderr, err := sb.Runner.Run(t, "thread", "info", "--id", th.ID)
		if err != nil {
			t.Fatalf("%s: info: %v\n%s", when, err, stderr)
		}
		if _, stderr, err := sb.Runner.Run(t, "thread", "wait", "--id", th.ID, "--until", "idle", "--timeout", "30s"); err == nil || !strings.Contains(stderr, "failed") {
			t.Errorf("%s: thread wait: err=%v stderr=%q", when, err, stderr)
		}
		if !strings.Contains(info, "FAILED") {
			t.Errorf("%s: thread info does not show it:\n%s", when, info)
		}
		if r := sb.headlessReply(t, th.ID); !strings.HasPrefix(r.Reply, "ERROR: ") {
			t.Errorf("%s: headless-reply: %+v", when, r)
		}
	}
	expectFailed("after the turn")

	if _, stderr, err := sb.daemonRunner.Run(t, "daemon", "stop"); err != nil {
		t.Fatalf("daemon stop: %v\n%s", err, stderr)
	}
	sb.startDaemon(t)
	expectFailed("after a daemon restart")

	failure.disarm(t, sb, th.ID)
	send(reply)
	if out, stderr, err := sb.Runner.Run(t, "await", "--id", th.ID, "--timeout", "150s", "--json"); err != nil {
		t.Fatalf("await on the turn after a failure: %v\n%s\n%s", err, out, stderr)
	} else if strings.Contains(out, "turn_error") {
		t.Errorf("the good turn did not clear it: %s", out)
	}
	if status := sb.threadStatus(t, th.ID); status.TurnError != "" {
		t.Errorf("status still shows %q", status.TurnError)
	}
}
