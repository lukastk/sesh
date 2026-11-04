package agents

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeContextSettingsShape(t *testing.T) {
	var s struct {
		Env   map[string]string `json:"env"`
		Hooks map[string][]struct {
			Matcher string                           `json:"matcher"`
			Hooks   []struct{ Type, Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(ClaudeContextSettings("/h o/context-hook.sh")), &s); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		g := s.Hooks[ev]
		if len(g) == 0 || len(g[0].Hooks) != 1 || g[0].Hooks[0].Type != "command" || g[0].Hooks[0].Command != `'/h o/context-hook.sh' claude file` {
			t.Fatalf("%s: %+v", ev, g)
		}
	}
	// The thread-env hook: beside the context hook at SessionStart, and alone on
	// FileChanged, matching the file SessionStart watches.
	start, changed := s.Hooks["SessionStart"], s.Hooks["FileChanged"]
	if len(start) != 2 || start[1].Hooks[0].Command != claudeEnvHookCommand {
		t.Fatalf("SessionStart: %+v", start)
	}
	if len(changed) != 1 || changed[0].Matcher != threadEnvScript || changed[0].Hooks[0].Command != claudeEnvHookCommand {
		t.Fatalf("FileChanged: %+v", changed)
	}
	if len(s.Hooks) != 3 {
		t.Fatalf("unexpected events: %v", s.Hooks)
	}
	if s.Env[claudeAddDirMemoryEnv] != "1" {
		t.Fatalf("settings do not enable the --add-dir context file: %v", s.Env)
	}
	// --add-dir is variadic: what follows it must be a flag, or it swallows a prompt.
	args := ContextHookArgs(Claude, "/x", "/ctx")
	if len(args) != 4 || args[0] != "--add-dir" || args[1] != "/ctx" || args[2] != "--settings" {
		t.Fatalf("claude args %v", args)
	}
	if ContextHookArgs(Pi, "/x", "") != nil || ContextHookArgs(MyPi, "/x", "") != nil {
		t.Fatal("pi family gets its context from the extension, not launch args")
	}
}

func TestCodexContextHookArgs(t *testing.T) {
	args := CodexContextHookArgs(`/a "b"/context-hook.sh`)
	if len(args) != 4 || args[0] != "-c" || args[2] != "-c" {
		t.Fatalf("args %v", args)
	}
	if !strings.HasPrefix(args[1], "hooks.SessionStart=[{hooks=[{type=\"command\",command=") ||
		!strings.HasPrefix(args[3], "hooks.UserPromptSubmit=[") {
		t.Fatalf("args %v", args)
	}
	if !strings.Contains(args[1], `\"b\"`) {
		t.Fatalf("quote not escaped: %s", args[1])
	}
}

func TestWriteContextHookScriptAndRun(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	home := t.TempDir()
	script, err := WriteContextHookScript(home)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(script); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("not executable: %v", fi.Mode())
	}
	// Idempotent and stable.
	if again, err := WriteContextHookScript(home); err != nil || again != script {
		t.Fatalf("rewrite: %v %v", again, err)
	}
	// A fake SESH_BIN records its argv.
	rec := filepath.Join(home, "argv")
	fake := filepath.Join(home, "fake-sesh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+rec+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) {
		cmd := exec.Command("sh", "-c", ShellEscape(script)+" codex")
		cmd.Env = append([]string{"PATH=/usr/bin:/bin", "SESH_BIN=" + fake}, env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook: %v %s", err, out)
		}
	}
	run() // no SESH_THREAD_ID: must not exec sesh at all
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Fatal("the hook ran sesh outside a sesh thread")
	}
	run("SESH_THREAD_ID=t1")
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(b)); strings.Join(got, " ") != "context hook --agent codex" {
		t.Fatalf("argv %q", got)
	}
	// claude's command adds "file", which must reach the CLI as its flag.
	cmd := exec.Command("sh", "-c", ShellEscape(script)+" claude file")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "SESH_BIN=" + fake, "SESH_THREAD_ID=t1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v %s", err, out)
	}
	if b, _ = os.ReadFile(rec); strings.Join(strings.Fields(string(b)), " ") != "context hook --agent claude --context-file" {
		t.Fatalf("argv %q", b)
	}
}

func TestSetCodexHookTrustTextEdit(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, "config.toml")
	key := CodexContextHookEvents[0].TrustKey
	orig := "notify = [\"x\"]\n\n[projects.\"/a\"]\ntrust_level = \"trusted\" # keep me\n"
	if err := os.WriteFile(cfg, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetCodexHookTrust(home, key, "sha256:aaa"); err != nil {
		t.Fatal(err)
	}
	want := orig + "\n[hooks.state.\"" + key + "\"]\ntrusted_hash = \"sha256:aaa\"\n"
	if got, _ := os.ReadFile(cfg); string(got) != want {
		t.Fatalf("append:\n%s\nwant:\n%s", got, want)
	}
	// Replace in place; nothing else moves.
	if err := SetCodexHookTrust(home, key, "sha256:bbb"); err != nil {
		t.Fatal(err)
	}
	want = strings.Replace(want, "sha256:aaa", "sha256:bbb", 1)
	if got, _ := os.ReadFile(cfg); string(got) != want {
		t.Fatalf("replace:\n%s", got)
	}
	// A header without a trusted_hash gets one inserted under it.
	bare := "[hooks.state.\"" + key + "\"]\nenabled = true\n\n[other]\nx = 1\n"
	if err := os.WriteFile(cfg, []byte(bare), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetCodexHookTrust(home, key, "sha256:ccc"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cfg); string(got) != "[hooks.state.\""+key+"\"]\ntrusted_hash = \"sha256:ccc\"\nenabled = true\n\n[other]\nx = 1\n" {
		t.Fatalf("insert:\n%s", got)
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed: %v", fi.Mode())
	}
	if err := SetCodexHookTrust(home, `bad"key`, "h"); err == nil {
		t.Fatal("a key with a quote must be refused")
	}
}

// TestCodexTrustsContextHooks drives the REAL codex: in a fresh CODEX_HOME sesh's
// hooks start untrusted, EnsureCodexContextHooksTrusted records codex's own hashes,
// and codex then reports both trusted. Proves sesh never has to compute codex's
// hash itself.
func TestCodexTrustsContextHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}
	codexHome := t.TempDir()
	args := CodexContextHookArgs(filepath.Join(t.TempDir(), "context-hook.sh"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*CodexProbeTimeout)
	defer cancel()
	before, err := ProbeCodexHooks(ctx, codexHome, args)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range before {
		if h.Source == "sessionFlags" && h.TrustStatus == "trusted" {
			t.Fatalf("a fresh home already trusts %s", h.Key)
		}
	}
	if CodexContextHooksRecorded(codexHome) {
		t.Fatal("recorded before anything was written")
	}
	if err := EnsureCodexContextHooksTrusted(ctx, codexHome, args); err != nil {
		t.Fatal(err)
	}
	after, err := ProbeCodexHooks(ctx, codexHome, args)
	if err != nil {
		t.Fatal(err)
	}
	trusted := 0
	for _, h := range after {
		if h.Source == "sessionFlags" && h.TrustStatus == "trusted" && h.Enabled {
			trusted++
		}
	}
	if trusted != len(CodexContextHookEvents) {
		t.Fatalf("trusted %d of %d hooks: %+v", trusted, len(CodexContextHookEvents), after)
	}
	if !CodexContextHooksRecorded(codexHome) {
		t.Fatal("trust not visible to the cheap re-check")
	}
	// A DIFFERENT hook command is a different declaration: codex must not trust it
	// on the strength of sesh's record (the hash is per declaration).
	other := CodexContextHookArgs(filepath.Join(t.TempDir(), "elsewhere.sh"))
	hooks, err := ProbeCodexHooks(ctx, codexHome, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hooks {
		if h.Source == "sessionFlags" && h.TrustStatus == "trusted" {
			t.Fatalf("codex trusted a different command under sesh's record: %+v", h)
		}
	}
}

// TestShellEscapeRoundTrips runs escaped strings through a real sh and requires
// them back byte for byte — the pane launch line depends on it (a codex hook whose
// command lost its quotes hashes differently and is no longer trusted).
func TestShellEscapeRoundTrips(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	for _, s := range []string{"plain", "it's", "'", "''", `a'b"c\d`, "$(echo pwned)`id`", "sp ace", "new\nline", ""} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+ShellEscape(s)).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("ShellEscape(%q) read back as %q", s, out)
		}
	}
	args := CodexContextHookArgs("/home/x/.sesh/context-hook.sh")
	line := "printf '%s\\n'" + LaunchArgs(Codex, "", args)
	got, err := exec.Command("sh", "-c", line).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, "\n")+"\n" != string(got) {
		t.Fatalf("launch args did not survive the shell:\n%s", got)
	}
}

func TestContextFileLifecycle(t *testing.T) {
	home := t.TempDir()
	const id = "11111111-2222"
	dir, err := WriteContextFile(home, id, "first")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ContextDirName, id); dir != want {
		t.Fatalf("dir = %s, want %s", dir, want)
	}
	path := filepath.Join(dir, contextFileName)
	if b, _ := os.ReadFile(path); string(b) != "first" {
		t.Fatalf("file = %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// A clear leaves an empty file: --add-dir needs the folder to exist.
	if _, err := WriteContextFile(home, id, ""); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 0 {
		t.Fatalf("after clear: %q %v", b, err)
	}
	other, _ := WriteContextFile(home, "other", "x")
	if err := RemoveContextFile(home, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("folder survived: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("removing one thread touched another: %v", err)
	}
	if err := RemoveContextFile(home, "never-had-one"); err != nil {
		t.Fatalf("no folder is not an error: %v", err)
	}
}

// The folder is removed recursively, so an id must not be able to name its parent
// or anything outside it.
func TestContextFileRefusesPathLikeIDs(t *testing.T) {
	home := t.TempDir()
	if _, err := WriteContextFile(home, "keep", "x"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", ".", "..", "../keep", "a/b", `a\\b`, "/etc"} {
		if err := RemoveContextFile(home, id); err == nil {
			t.Errorf("id %q was accepted", id)
		}
		if _, err := WriteContextFile(home, id, "x"); err == nil {
			t.Errorf("id %q was accepted for a write", id)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ContextDirName, "keep")); err != nil {
		t.Fatalf("a refused id removed something: %v", err)
	}
}
