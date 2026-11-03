package agents

// Thread-context hooks (schema 60, api/context.go). Claude and codex receive a
// thread's context from their SessionStart + UserPromptSubmit hooks, which sesh
// attaches to every agent it launches — never to the user's own sessions:
//
//   - claude: `--settings <json>` adds the two hooks to that process only; they
//     run ALONGSIDE the user's settings hooks (sesh's own state hook included).
//     Launch-time instructions would not do: claude freezes --append-system-prompt
//     into the session at creation, ignores it on resume, and drops it at
//     compaction, whereas SessionStart fires again on resume, clear and compact.
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

// contextHookCommand is the hook command line for one harness. It is a stable
// string (the codex trust hash covers it).
func contextHookCommand(script string, k Kind) string {
	return ShellEscape(script) + " " + string(Family(k))
}

// ClaudeContextSettings is the --settings JSON adding the context hooks.
func ClaudeContextSettings(script string) string {
	h := []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": contextHookCommand(script, Claude)}}}}
	b, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": h, "UserPromptSubmit": h}})
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

// ContextHookArgs are the launch arguments attaching the context hooks to an
// agent of kind k (nil for pi/mypi, whose extension handles it).
func ContextHookArgs(k Kind, script string) []string {
	switch Family(k) {
	case Claude:
		return []string{"--settings", ClaudeContextSettings(script)}
	case Codex:
		return CodexContextHookArgs(script)
	}
	return nil
}
