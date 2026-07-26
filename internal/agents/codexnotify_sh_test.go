package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCodexNotifyScriptPassesSessionID drives the REAL embedded reporter
// script (the exact bytes the daemon materializes) with a fake SESH_BIN that
// records its argv: the payload's "thread-id" must ride along as
// --agent-session (schema 46, ticket 49d4299b), an id-less payload must still
// report (without the flag), and a non-turn-complete payload must not report.
func TestCodexNotifyScriptPassesSessionID(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	script, err := WriteCodexNotifyScript(dir)
	if err != nil {
		t.Fatalf("WriteCodexNotifyScript: %v", err)
	}
	argvLog := filepath.Join(dir, "argv.log")
	fakeBin := filepath.Join(dir, "fake-sesh")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+argvLog+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(payload string) {
		t.Helper()
		cmd := exec.Command("sh", script, payload)
		cmd.Env = append(os.Environ(), "SESH_THREAD_ID=tid-notify", "SESH_BIN="+fakeBin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script: %v\n%s", err, out)
		}
	}
	readLog := func() string {
		b, _ := os.ReadFile(argvLog)
		return string(b)
	}

	// The real 0.145 payload shape: thread-id = codex's own session id.
	run(`{"type":"agent-turn-complete","thread-id":"019f-test-session","turn-id":"t1","cwd":"/tmp"}`)
	got := readLog()
	if !strings.Contains(got, "--event turn_ended_no_authority") ||
		!strings.Contains(got, "--agent-session 019f-test-session") {
		t.Fatalf("turn-complete report missing event or --agent-session:\n%s", got)
	}

	// An id-less payload (older codex) still reports the turn end, sans flag.
	os.Remove(argvLog)
	run(`{"type":"agent-turn-complete","last-assistant-message":"OK"}`)
	got = readLog()
	if !strings.Contains(got, "--event turn_ended_no_authority") {
		t.Fatalf("id-less payload did not report the turn end:\n%s", got)
	}
	if strings.Contains(got, "--agent-session") {
		t.Fatalf("id-less payload must not send an empty --agent-session:\n%s", got)
	}

	// A foreign payload type must not report at all.
	os.Remove(argvLog)
	run(`{"type":"something-else","thread-id":"019f-test-session"}`)
	if got = readLog(); got != "" {
		t.Fatalf("non-turn-complete payload reported anyway:\n%s", got)
	}
}
