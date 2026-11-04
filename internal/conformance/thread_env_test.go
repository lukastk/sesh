package conformance

// thread.env* and env.* cells (schema 62).
//
// The evidence is what the agent's OWN shell tool sees: each probe asks the real
// agent to run printenv into a file, and the test reads that file. So a model cannot
// report a value it never had, and the tests read the environment the harness
// really gave its shell.
//
// Remote = the thread lives on the peer; every command is routed with --machine.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.env", a, loc, func(t *testing.T) { testThreadEnv(t, string(a), loc) })
			matrix.RegisterTest("thread.env-restart", a, loc, func(t *testing.T) { testThreadEnvRestart(t, string(a), loc) })
			matrix.RegisterTest("thread.env-headless", a, loc, func(t *testing.T) { testThreadEnvHeadless(t, string(a), loc) })
		}
		matrix.RegisterTest("env.inherit", matrix.AgentAgnostic, loc, func(t *testing.T) { testEnvInherit(t, loc) })
		matrix.RegisterTest("env.guards", matrix.AgentAgnostic, loc, func(t *testing.T) { testEnvGuards(t, loc) })
	}
	matrix.RegisterTest("env.edit", matrix.AgentAgnostic, matrix.Local, testEnvEditCLI)
}

const envProbePrefix = "SESHT_"

// envProbe is one round of asking the agent's shell what it sees.
type envProbe struct {
	dir string
	n   int
}

func newEnvProbe(t *testing.T) *envProbe {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sesh-env-probe-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return &envProbe{dir: dir}
}

// next returns the prompt for one probe and a reader of its result.
func (p *envProbe) next(t *testing.T) (string, func() map[string]string) {
	p.n++
	file := filepath.Join(p.dir, fmt.Sprintf("round-%d.txt", p.n))
	prompt := "Use your shell tool to run exactly this one command and nothing else, then reply with only the word DONE: printenv | grep '^" + envProbePrefix + "' | sort > " + file
	return prompt, func() map[string]string {
		t.Helper()
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("the agent's shell left no probe file: %v", err)
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				got[k] = v
			}
		}
		return got
	}
}

func (p *envProbe) pane(t *testing.T, sb *Sandbox, id string) map[string]string {
	t.Helper()
	prompt, read := p.next(t)
	headfulTurn(t, sb, id, prompt)
	return read()
}

func (p *envProbe) headless(t *testing.T, sb *Sandbox, id string) map[string]string {
	t.Helper()
	prompt, read := p.next(t)
	sb.headlessTurn(t, id, prompt)
	return read()
}

func wantEnv(t *testing.T, what string, got map[string]string, want map[string]string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: the agent's shell saw %v, want %v", what, got, want)
	}
}

// newThreadWithEnv is newThreadOnModel with --env flags.
func newThreadWithEnv(t *testing.T, sb *Sandbox, agent, name string, headless bool, env ...string) api.Thread {
	t.Helper()
	args := []string{"thread", "new", "--agent", agent, "--name", name, "--cwd", "/tmp", "--model", contextModels[agent], "--json"}
	if headless {
		args = append(args, "--headless")
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	out, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatal(err)
	}
	return th
}

func envSetCmd(t *testing.T, sb *Sandbox, id string, pairs ...string) api.SetThreadEnvResponse {
	t.Helper()
	return runEnvChange(t, sb, append([]string{"env", "set", "--id", id, "--json"}, pairs...)...)
}

func envUnsetCmd(t *testing.T, sb *Sandbox, id string, keys ...string) api.SetThreadEnvResponse {
	t.Helper()
	return runEnvChange(t, sb, append([]string{"env", "unset", "--id", id, "--json"}, keys...)...)
}

func runEnvChange(t *testing.T, sb *Sandbox, args ...string) api.SetThreadEnvResponse {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, stderr)
	}
	var resp api.SetThreadEnvResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("decode %v: %v\n%s", args, err, out)
	}
	return resp
}

func showEnv(t *testing.T, sb *Sandbox, id string) map[string]string {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, "env", "show", "--id", id, "--reveal", "--json")
	if err != nil {
		t.Fatalf("env show: %v\n%s", err, stderr)
	}
	var resp api.ThreadEnvResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Vars
}

func testThreadEnv(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	probe := newEnvProbe(t)

	th := newThreadWithEnv(t, sb, agent, "env", false, "SESHT_SEED=launch value")
	sb.waitThreadReady(t, th.ID, agent)
	_, pidBefore := liveAgentPID(t, sb, th.ID)
	wantEnv(t, "set at spawn", probe.pane(t, sb, th.ID), map[string]string{"SESHT_SEED": "launch value"})
	if row := gridRow(t, sb, false, th.ID); row.EnvCount != 1 {
		t.Fatalf("grid row env_count = %d, want 1", row.EnvCount)
	}

	follows := agent != "codex"
	wantOutcome := api.EnvLive
	if !follows {
		wantOutcome = api.EnvRestartRequired
	}
	if got := envSetCmd(t, sb, th.ID, "SESHT_LIVE=one"); got.Outcome != wantOutcome || got.Thread.EnvCount != 2 {
		t.Fatalf("env set answered %+v, want outcome %s", got, wantOutcome)
	}
	if follows {
		wantEnv(t, "added while running", probe.pane(t, sb, th.ID), map[string]string{"SESHT_SEED": "launch value", "SESHT_LIVE": "one"})
	} else {
		// A restart-required answer must be the truth: nothing changed.
		wantEnv(t, "codex is unchanged until a restart", probe.pane(t, sb, th.ID), map[string]string{"SESHT_SEED": "launch value"})
	}

	envSetCmd(t, sb, th.ID, "SESHT_LIVE=two")
	envUnsetCmd(t, sb, th.ID, "SESHT_SEED") // set at launch: the running process still holds it
	if follows {
		wantEnv(t, "changed and removed while running", probe.pane(t, sb, th.ID), map[string]string{"SESHT_LIVE": "two"})
	}
	if _, pid := liveAgentPID(t, sb, th.ID); pid != pidBefore {
		t.Fatalf("the agent process changed (%d -> %d): nothing here may restart it", pidBefore, pid)
	}
}

func testThreadEnvRestart(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	probe := newEnvProbe(t)

	th := newThreadWithEnv(t, sb, agent, "restart", false, "SESHT_GONE=old")
	sb.waitThreadReady(t, th.ID, agent)
	envSetCmd(t, sb, th.ID, "SESHT_EARLY=1")

	restart := func(what string) {
		t.Helper()
		paneBefore, pidBefore := liveAgentPID(t, sb, th.ID)
		if _, stderr, err := sb.Runner.Run(t, "thread", "restart", "--id", th.ID); err != nil {
			t.Fatalf("%s: thread restart: %v\n%s", what, err, stderr)
		}
		sb.waitThreadReady(t, th.ID, agent)
		pane, pid := liveAgentPID(t, sb, th.ID)
		if pane != paneBefore || pid == pidBefore {
			t.Fatalf("%s: pane %s -> %s, pid %d -> %d: want the same pane, a new process", what, paneBefore, pane, pidBefore, pid)
		}
	}

	// No prompt was ever sent: nothing to resume, and it must still work.
	restart("before the first prompt")
	wantEnv(t, "restarted before any prompt", probe.pane(t, sb, th.ID), map[string]string{"SESHT_GONE": "old", "SESHT_EARLY": "1"})

	word := randomTag("TAPIR")
	headfulTurn(t, sb, th.ID, "Remember this word for later: "+word+". Reply with only: OK")
	envUnsetCmd(t, sb, th.ID, "SESHT_GONE")
	envSetCmd(t, sb, th.ID, "SESHT_LATE=2")

	restart("after turns")
	wantEnv(t, "restarted after turns", probe.pane(t, sb, th.ID), map[string]string{"SESHT_EARLY": "1", "SESHT_LATE": "2"})
	assertReplyHas(t, "the conversation survives a restart", headfulTurn(t, sb, th.ID, "What was the word I asked you to remember? Reply with only the word."), word)
	// A restart starts clean, so the removal no longer needs to be remembered.
	home := sb.Home
	e, err := agents.ReadThreadEnv(home, th.ID)
	if err != nil || len(e.Removed) != 0 {
		t.Fatalf("after a restart env.json = %+v, %v; want no pending removals", e, err)
	}

	if _, _, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "restart", "--id", th.ID); err == nil || !strings.Contains(stderr, "no running agent") {
		t.Fatalf("restarting a stopped thread: err=%v stderr=%q", err, stderr)
	}
}

func testThreadEnvHeadless(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	// A non-interactive turn may only use a shell tool with permissions bypassed, as
	// production runs them; the probe IS a shell command.
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte("[spawn]\nmode = \"yolo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)
	probe := newEnvProbe(t)

	th := newThreadWithEnv(t, sb, agent, "env-headless", true, "SESHT_A=first")
	wantEnv(t, "first turn", probe.headless(t, sb, th.ID), map[string]string{"SESHT_A": "first"})
	if got := envSetCmd(t, sb, th.ID, "SESHT_B=second"); got.Outcome != api.EnvNextLaunch {
		t.Fatalf("env set on an idle thread answered %+v", got)
	}
	wantEnv(t, "after a change", probe.headless(t, sb, th.ID), map[string]string{"SESHT_A": "first", "SESHT_B": "second"})
	envUnsetCmd(t, sb, th.ID, "SESHT_A")
	wantEnv(t, "after a removal", probe.headless(t, sb, th.ID), map[string]string{"SESHT_B": "second"})
}

func testEnvInherit(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	parent := newThreadWithEnv(t, sb, "pi", "parent", true, "SESHT_A=parent", "SESHT_B=parent")

	child := func(name string, extra ...string) api.Thread {
		t.Helper()
		args := append([]string{"thread", "new", "--agent", "pi", "--name", name, "--cwd", "/tmp", "--headless", "--json"}, extra...)
		out, stderr, err := sb.Runner.Run(t, args...)
		if err != nil {
			t.Fatalf("thread new %s: %v\n%s", name, err, stderr)
		}
		var th api.Thread
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
			t.Fatal(err)
		}
		return th
	}
	inherited := child("inherited", "--parent", parent.ID)
	wantEnv(t, "a child", showEnv(t, sb, inherited.ID), map[string]string{"SESHT_A": "parent", "SESHT_B": "parent"})
	if inherited.EnvCount != 2 {
		t.Fatalf("the child's record says env_count %d", inherited.EnvCount)
	}
	over := child("overridden", "--parent", parent.ID, "--env", "SESHT_B=child")
	wantEnv(t, "--env over an inherited value", showEnv(t, sb, over.ID), map[string]string{"SESHT_A": "parent", "SESHT_B": "child"})
	none := child("opted-out", "--parent", parent.ID, "--no-inherit-env")
	wantEnv(t, "--no-inherit-env", showEnv(t, sb, none.ID), map[string]string{})

	// A copy: the two change independently.
	envSetCmd(t, sb, parent.ID, "SESHT_A=changed")
	wantEnv(t, "a child after its parent changed", showEnv(t, sb, inherited.ID), map[string]string{"SESHT_A": "parent", "SESHT_B": "parent"})

	// A fork continues its source (it needs a conversation to fork).
	sb.headlessTurn(t, parent.ID, "Reply with only: OK")
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--fork-from", parent.ID, "--name", "fork", "--json")
	if err != nil {
		t.Fatalf("fork: %v\n%s", err, stderr)
	}
	var fork api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &fork); err != nil {
		t.Fatal(err)
	}
	wantEnv(t, "a fork", showEnv(t, sb, fork.ID), map[string]string{"SESHT_A": "changed", "SESHT_B": "parent"})
	out, stderr, err = sb.Runner.Run(t, "thread", "new", "--fork-from", parent.ID, "--name", "fork2", "--no-inherit-env", "--json")
	if err != nil {
		t.Fatalf("fork: %v\n%s", err, stderr)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &fork); err != nil {
		t.Fatal(err)
	}
	wantEnv(t, "a fork with --no-inherit-env", showEnv(t, sb, fork.ID), map[string]string{})

	// A delegate's worker is a root thread, so it inherits from its CALLER. The
	// caller's identity is the client's, so this runs the client in the thread's place.
	if loc != matrix.Local {
		return
	}
	delegate := func(name string, extra ...string) string {
		t.Helper()
		cmd := exec.Command(seshBin(t), append([]string{"delegate", "--agent", "pi", "--cwd", "/tmp", "--name", name, "--json"}, append(extra, "Reply with only: OK")...)...)
		cmd.Dir = "/tmp"
		cmd.Env = sandboxEnv(map[string]string{"SESH_HOME": sb.Home, "SESH_MACHINE": sb.Machine, "SESH_THREAD_ID": parent.ID,
			"SESH_TMUX_SOCKET": sb.TmuxSocket, "SESH_MASTER_SOCKET": sb.MasterSocket})
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("delegate: %v\n%s", err, out)
		}
		var resp struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(out[bytes.IndexByte(out, '{'):], &resp); err != nil || resp.ID == "" {
			t.Fatalf("delegate answered %s (%v)", out, err)
		}
		return resp.ID
	}
	wantEnv(t, "a delegate's worker", showEnv(t, sb, delegate("worker")), map[string]string{"SESHT_A": "changed", "SESHT_B": "parent"})
	wantEnv(t, "a delegate with --no-inherit-env", showEnv(t, sb, delegate("worker2", "--no-inherit-env")), map[string]string{})
}

func testEnvGuards(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "guarded")

	refused := func(what, wantErr string, args ...string) {
		t.Helper()
		_, stderr, err := sb.Runner.Run(t, args...)
		if err == nil || !strings.Contains(stderr, wantErr) {
			t.Fatalf("%s: err=%v stderr=%q, want a refusal containing %q", what, err, stderr, wantErr)
		}
	}
	set := []string{"env", "set", "--id", th.ID}
	refused("a name that is no variable", "not a valid variable name", append(set, "A-B=1")...)
	refused("an empty name", "not a valid variable name", append(set, "=1")...)
	refused("no equals sign", "expected KEY=VALUE", append(set, "JUSTAKEY")...)
	refused("a flag after the variables", "flags go before", "env", "set", "A=1", "--id", th.ID)
	refused("sesh's identity", "reserved", append(set, "SESH_THREAD_ID=other")...)
	refused("tmux's", "reserved", append(set, "TMUX=/tmp/x")...)
	refused("a zsh special", "special zsh parameter", append(set, "LINES=a b")...)
	refused("zsh's path", "special zsh parameter", append(set, "path=/x")...)
	refused("oversize", "the limit is", append(set, "BIG="+strings.Repeat("x", api.MaxThreadEnvBytes+1))...)
	refused("nothing to set", "nothing to set", set...)
	refused("unset of a name it lacks", "not set on this thread", "env", "unset", "--id", th.ID, "NOPE")
	if got := showEnv(t, sb, th.ID); len(got) != 0 {
		t.Fatalf("a refused write stored %v", got)
	}

	shell, stderr, err := sb.Runner.Run(t, "thread", "new", "--virtual", "--name", "group", "--json")
	if err != nil {
		t.Fatalf("thread new --virtual: %v\n%s", err, stderr)
	}
	var virtual api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(shell)), &virtual); err != nil {
		t.Fatal(err)
	}
	refused("a thread with no agent", "runs no agent", "env", "set", "--id", virtual.ID, "A=1")
	refused("a thread with no agent at birth", "runs none", "thread", "new", "--virtual", "--name", "group2", "--env", "A=1")

	// Values with everything a shell could trip on come back byte-identical.
	const secret = `s3cret-'"$(touch pwned)` + "`x`"
	got := envSetCmd(t, sb, th.ID, "SESHT_SECRET="+secret)
	if !got.Changed || got.Hash == "" || got.Thread.EnvCount != 1 {
		t.Fatalf("env set answered %+v", got)
	}
	wantEnv(t, "show --reveal", showEnv(t, sb, th.ID), map[string]string{"SESHT_SECRET": secret})

	// Masked unless asked, in both forms.
	for _, args := range [][]string{{"env", "show", "--id", th.ID}, {"env", "show", "--id", th.ID, "--json"}} {
		out, _, err := sb.Runner.Run(t, args...)
		if err != nil || strings.Contains(out, "s3cret") || !strings.Contains(out, "SESHT_SECRET") {
			t.Fatalf("%v printed %q (%v)", args, out, err)
		}
	}
	// The same value again changes nothing.
	if again := envSetCmd(t, sb, th.ID, "SESHT_SECRET="+secret); again.Changed {
		t.Fatalf("the same value reported a change: %+v", again)
	}

	// The value is on the owner's disk, privately, and nowhere that replicates.
	for _, name := range []string{"sesh.db", "sesh.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(sb.Home, name))
		if err != nil && name == "sesh.db" {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("s3cret")) {
			t.Fatalf("the value reached %s", name)
		}
	}
	row := gridRow(t, sb, false, th.ID)
	if b, _ := json.Marshal(row); bytes.Contains(b, []byte("s3cret")) {
		t.Fatalf("the value is in the replicated row: %s", b)
	}
	dir, err := agents.ThreadEnvDir(sb.Home, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env.json", "env.sh"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s is %o, want 600", name, fi.Mode().Perm())
		}
	}

	// `env clear`, then the files go with the thread.
	if cleared := runEnvChange(t, sb, "env", "clear", "--id", th.ID, "--json"); cleared.Thread.EnvCount != 0 || cleared.Hash != "" {
		t.Fatalf("env clear answered %+v", cleared)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", th.ID); err != nil {
		t.Fatalf("thread delete: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the env files outlived the thread: %v", err)
	}
}

func testEnvEditCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := newThreadWithEnv(t, sb, "pi", "edit", true, "SESHT_A=1", "SESHT_DROP=x")

	dir := t.TempDir()
	seen, next := filepath.Join(dir, "seen"), filepath.Join(dir, "next")
	editor := filepath.Join(dir, "editor.sh")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ncat \"$1\" > "+seen+"\ncat "+next+" > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	edit := func(content string) string {
		t.Helper()
		if err := os.WriteFile(next, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(seshBin(t), "env", "edit", "--id", th.ID)
		cmd.Env = sandboxEnv(map[string]string{"SESH_HOME": sb.Home, "SESH_MACHINE": sb.Machine,
			"SESH_TMUX_SOCKET": sb.TmuxSocket, "SESH_MASTER_SOCKET": sb.MasterSocket, "EDITOR": editor, "VISUAL": ""})
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("env edit: %v\n%s", err, out)
		}
		return string(out)
	}
	out := edit("SESHT_A=2\n# a comment\nSESHT_NEW=\"two\\nlines\"\n")
	if b, _ := os.ReadFile(seen); string(b) != "SESHT_A=1\nSESHT_DROP=x\n" {
		t.Fatalf("the editor was given %q", b)
	}
	wantEnv(t, "after edit ("+out+")", showEnv(t, sb, th.ID), map[string]string{"SESHT_A": "2", "SESHT_NEW": "two\nlines"})
	if out = edit("SESHT_A=2\nSESHT_NEW=\"two\\nlines\"\n"); !strings.Contains(out, "unchanged") {
		t.Fatalf("an unchanged save reported %q", out)
	}
	edit("\n")
	wantEnv(t, "an emptied file", showEnv(t, sb, th.ID), map[string]string{})
}
