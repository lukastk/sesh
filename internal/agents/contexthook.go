package agents

// Thread-context hooks (schema 60, api/context.go). Claude and codex receive a
// thread's context from their SessionStart + UserPromptSubmit hooks, which sesh
// attaches to every agent it launches — never to the user's own sessions:
//
//   - claude: two mechanisms, because neither is enough alone. The thread's
//     context FILE (<SESH_HOME>/thread-context/<id>/CLAUDE.md, loaded with --add-dir
//     as an ordinary instruction file) is what claude reloads at launch, resume,
//     clear and compaction, and what a rewind cannot remove. The hooks (--settings,
//     run ALONGSIDE the user's settings hooks) carry a CHANGE to a running process:
//     claude surfaces only the first mid-session edit of an instruction file, and
//     none at all to a file that was empty when it loaded. A launch flag would not
//     do: claude freezes --append-system-prompt at creation, ignores it on resume,
//     and drops it at compaction.
//   - codex: `-c hooks.<Event>=[…]` adds them as "session flag" hooks, which codex
//     runs only once trusted; the daemon records the trust (codexcontext.go).
//   - pi / mypi: nothing here — the sesh-agent-state extension asks before every
//     turn.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed contexthook.sh
var contextHookScript []byte

// ContextHookScriptName is the hook script's file name under SESH_HOME.
const ContextHookScriptName = "context-hook.sh"

// WriteContextHookScript materializes the embedded hook at
// <seshHome>/context-hook.sh (0755) and returns its path. Rewritten whenever its
// content differs, so an upgrade propagates; the path never changes.
func WriteContextHookScript(seshHome string) (string, error) {
	if seshHome == "" {
		return "", fmt.Errorf("context hook: empty sesh home")
	}
	path := filepath.Join(seshHome, ContextHookScriptName)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(contextHookScript) {
		return path, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, contextHookScript, 0o755); err != nil {
		return "", fmt.Errorf("context hook: write script: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("context hook: install script: %w", err)
	}
	return path, nil
}

// ContextDirName is the directory under SESH_HOME holding one folder per thread
// with that thread's rendered claude context file.
const ContextDirName = "thread-context"

// contextFileName is what claude loads from an --add-dir directory.
const contextFileName = "CLAUDE.md"

// claudeAddDirMemoryEnv makes claude load CLAUDE.md from --add-dir directories.
const claudeAddDirMemoryEnv = "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD"

// ContextFileDir is thread id's folder. The id is checked because the path is
// created and removed recursively: an empty or path-like id must never resolve
// to the parent directory or outside it.
func ContextFileDir(seshHome, threadID string) (string, error) {
	if seshHome == "" {
		return "", fmt.Errorf("context file: empty sesh home")
	}
	if threadID == "" || threadID == "." || threadID == ".." || strings.ContainsAny(threadID, `/\`) {
		return "", fmt.Errorf("context file: invalid thread id %q", threadID)
	}
	return filepath.Join(seshHome, ContextDirName, threadID), nil
}

// WriteContextFile writes thread id's rendered context file (content "" = an
// empty file: the directory --add-dir names must exist either way) and returns
// its folder. An unchanged file is left alone, so a launch does not touch its
// mtime. The file is a copy: the owner's store is the source of truth.
func WriteContextFile(seshHome, threadID, content string) (string, error) {
	dir, err := ContextFileDir(seshHome, threadID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("context file: %w", err)
	}
	path := filepath.Join(dir, contextFileName)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return dir, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("context file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("context file: %w", err)
	}
	return dir, nil
}

// RemoveContextFile removes thread id's folder (a deleted thread's). A thread
// that never had one is not an error.
func RemoveContextFile(seshHome, threadID string) error {
	dir, err := ContextFileDir(seshHome, threadID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("context file: %w", err)
	}
	return nil
}

// contextHookCommand is the hook command line for one harness. It is a stable
// string (the codex trust hash covers it). Claude's carries "file": that process
// was launched with its context file, so the daemon need only send it changes.
func contextHookCommand(script string, k Kind) string {
	cmd := ShellEscape(script) + " " + string(Family(k))
	if Family(k) == Claude {
		cmd += " file"
	}
	return cmd
}

// claudeEnvHookCommand finds the launching daemon's sesh like context-hook.sh does.
const claudeEnvHookCommand = `"${` + EnvSeshBin + `:-sesh}" env hook`

// ClaudeContextSettings is the --settings JSON adding the context hooks and
// enabling the --add-dir context file. Setting the variable here rather than in
// the launch environment keeps every launch path (pane, respawn, headless) alike.
func ClaudeContextSettings(script string) string {
	h := []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": contextHookCommand(script, Claude)}}}}
	// The thread-env hook (threadenv.go): SessionStart beside the context hook, and
	// FileChanged, which claude runs for the file SessionStart names in watchPaths.
	envHook := map[string]any{"hooks": []map[string]any{{"type": "command", "command": claudeEnvHookCommand}}}
	envChanged := map[string]any{"matcher": threadEnvScript, "hooks": envHook["hooks"]}
	b, _ := json.Marshal(map[string]any{
		"env": map[string]string{claudeAddDirMemoryEnv: "1"},
		"hooks": map[string]any{
			"SessionStart":     append(h, envHook),
			"UserPromptSubmit": h,
			"FileChanged":      []map[string]any{envChanged},
		},
	})
	return string(b)
}

// CodexContextHookEvents are the codex hook events sesh registers, with the trust
// key codex gives a session-flag hook of each (index 0:0: sesh passes the only
// session-flag hook for each event).
var CodexContextHookEvents = []struct{ Event, TrustKey string }{
	{"SessionStart", "/<session-flags>/config.toml:session_start:0:0"},
	{"UserPromptSubmit", "/<session-flags>/config.toml:user_prompt_submit:0:0"},
}

// CodexContextHookArgs are the -c flags adding the context hooks to one codex
// invocation (top-level, `exec` and `resume` all accept -c).
func CodexContextHookArgs(script string) []string {
	cmd := contextHookCommand(script, Codex)
	var out []string
	for _, e := range CodexContextHookEvents {
		out = append(out, "-c", fmt.Sprintf("hooks.%s=[{hooks=[{type=%s,command=%s}]}]", e.Event, tomlString("command"), tomlString(cmd)))
	}
	return out
}

// tomlString renders s as a TOML basic string.
func tomlString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// ContextHookArgs are the launch arguments attaching thread context to an agent
// of kind k (nil for pi/mypi, whose extension handles it). contextDir is claude's
// context folder; --add-dir takes several values, so it must be followed by
// another flag, never by a positional prompt — here that is --settings.
func ContextHookArgs(k Kind, script, contextDir string) []string {
	switch Family(k) {
	case Claude:
		return []string{"--add-dir", contextDir, "--settings", ClaudeContextSettings(script)}
	case Codex:
		return CodexContextHookArgs(script)
	}
	return nil
}
