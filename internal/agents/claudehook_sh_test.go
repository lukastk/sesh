package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// claudeHookScript locates the real Claude Code hook script in the repo (it is
// referenced by absolute path from claude settings, not embedded in the binary).
func claudeHookScript(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot resolve test file path")
	}
	// internal/agents/ -> repo root -> integrations/claude/…
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	p := filepath.Join(root, "integrations", "claude", "sesh-agent-state.sh")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("hook script not found at %s", p)
	}
	return p
}

// TestClaudeHookStampsSessionID drives the REAL Claude Code hook with a fake
// SESH_BIN that records its argv: UserPromptSubmit/Stop must report the mapped
// event AND carry the payload's session_id as --agent-session (schema 46 — so
// the daemon tracks the live session id across claude's compaction/rewind
// forks). A payload without a session_id still reports (no flag), a subagent
// invocation (agent_id) reports nothing, and AskUserQuestion reports blocked
// with the question AND the session id.
func TestClaudeHookStampsSessionID(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script := claudeHookScript(t)
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	fakeBin := filepath.Join(dir, "fake-sesh")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+argvLog+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(stdin string) {
		t.Helper()
		cmd := exec.Command("sh", script)
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), "SESH_THREAD_ID=tid-claude", "SESH_BIN="+fakeBin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook: %v\n%s", err, out)
		}
	}
	readLog := func() string { b, _ := os.ReadFile(argvLog); return string(b) }
	reset := func() { os.Remove(argvLog) }

	// UserPromptSubmit -> turn_started + --agent-session <session_id>.
	run(`{"hook_event_name":"UserPromptSubmit","session_id":"claude-sess-1"}`)
	got := readLog()
	if !strings.Contains(got, "--event turn_started") || !strings.Contains(got, "--agent-session claude-sess-1") {
		t.Fatalf("UserPromptSubmit missing event or --agent-session:\n%s", got)
	}

	// Stop -> turn_ended + the same stamping.
	reset()
	run(`{"hook_event_name":"Stop","session_id":"claude-sess-2"}`)
	got = readLog()
	if !strings.Contains(got, "--event turn_ended") || !strings.Contains(got, "--agent-session claude-sess-2") {
		t.Fatalf("Stop missing event or --agent-session:\n%s", got)
	}

	// No session_id -> still reports the event, no --agent-session.
	reset()
	run(`{"hook_event_name":"Stop"}`)
	got = readLog()
	if !strings.Contains(got, "--event turn_ended") {
		t.Fatalf("session-less payload did not report the turn end:\n%s", got)
	}
	if strings.Contains(got, "--agent-session") {
		t.Fatalf("session-less payload must not send an empty --agent-session:\n%s", got)
	}

	// Subagent invocation (agent_id) reports nothing.
	reset()
	run(`{"hook_event_name":"Stop","session_id":"claude-sess-3","agent_id":"sub-1"}`)
	if got = readLog(); got != "" {
		t.Fatalf("subagent invocation reported anyway:\n%s", got)
	}

	// AskUserQuestion -> blocked with the question reason AND the session id.
	reset()
	run(`{"hook_event_name":"PreToolUse","session_id":"claude-sess-4","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"red or blue?"}]}}`)
	got = readLog()
	if !strings.Contains(got, "--event blocked") || !strings.Contains(got, "red or blue?") || !strings.Contains(got, "--agent-session claude-sess-4") {
		t.Fatalf("AskUserQuestion missing blocked/reason/session:\n%s", got)
	}
}

// TestClaudeHookSkipsBackgroundSession pins the 2026-08-05 fix. A claude
// BACKGROUND agent (the `agents` feature) does not run in the thread's tmux
// pane — it runs under claude's machine-global daemon, from pre-forked spares
// that inherit SESH_THREAD_ID from whatever process started that daemon. Its
// thread id therefore names an UNRELATED thread, which makes every fact it
// reports wrong: live, one such agent stamped its conversation's session id
// onto a different thread's record, leaving the real thread pinned to a stale
// transcript and unresumable. The hook must report NOTHING from a bg session —
// for every event it would otherwise map, not merely the session stamp.
func TestClaudeHookSkipsBackgroundSession(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	script := claudeHookScript(t)
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	fakeBin := filepath.Join(dir, "fake-sesh")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+argvLog+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(stdin string, extraEnv ...string) string {
		t.Helper()
		os.Remove(argvLog)
		cmd := exec.Command("sh", script)
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), "SESH_THREAD_ID=tid-inherited", "SESH_BIN="+fakeBin)
		cmd.Env = append(cmd.Env, extraEnv...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook: %v\n%s", err, out)
		}
		b, _ := os.ReadFile(argvLog)
		return string(b)
	}

	for _, payload := range []string{
		`{"hook_event_name":"UserPromptSubmit","session_id":"bg-sess"}`,
		`{"hook_event_name":"Stop","session_id":"bg-sess"}`,
		`{"hook_event_name":"PostToolUse","session_id":"bg-sess"}`,
		`{"hook_event_name":"PreToolUse","session_id":"bg-sess","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"red or blue?"}]}}`,
		`{"hook_event_name":"Notification","session_id":"bg-sess","message":"Claude needs your permission to use Bash"}`,
	} {
		// Control FIRST: without the marker this payload really does report, so
		// an empty log below is the guard working and not a broken fixture.
		if got := run(payload); got == "" {
			t.Fatalf("fixture is vacuous — payload reported nothing even outside a bg session:\n%s", payload)
		}
		if got := run(payload, "CLAUDE_CODE_SESSION_KIND=bg"); got != "" {
			t.Fatalf("background session reported anyway (it would land on the inherited, WRONG thread id):\npayload: %s\nargv: %s", payload, got)
		}
	}

	// A normal foreground session is untouched by the guard.
	if got := run(`{"hook_event_name":"Stop","session_id":"fg-sess"}`, "CLAUDE_CODE_SESSION_KIND=interactive"); !strings.Contains(got, "--event turn_ended") || !strings.Contains(got, "--agent-session fg-sess") {
		t.Fatalf("foreground session stopped reporting:\n%s", got)
	}
}
