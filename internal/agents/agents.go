// Package agents knows how to launch and identify the three coding agents
// (claude, codex, pi). It deliberately holds only the per-agent specifics; the
// thread lifecycle (tmux, storage, routing) lives in the daemon.
package agents

import (
	"fmt"
	"os"
	"strings"
)

// harnessEnvMarkers are the exact env vars a coding-agent harness sets on ITS
// OWN process to mark "I am a running agent session". CLAUDE_CODE_*-prefixed
// vars are scrubbed too (see ScrubHarnessEnv).
var harnessEnvMarkers = map[string]bool{
	"CLAUDECODE":     true,
	"IN_CLAUDE_CODE": true,
	"AI_AGENT":       true,
	"CLAUDE_EFFORT":  true,
}

// ScrubHarnessEnv removes coding-agent-harness session markers (CLAUDECODE=1,
// IN_CLAUDE_CODE=1, CLAUDE_CODE_SESSION_ID=…, CLAUDE_CODE_ENTRYPOINT=…, AI_AGENT=…
// and every CLAUDE_CODE_* var) from THIS process's environment.
//
// Why this matters and why it is loud-bug territory: the sesh daemon may be
// started from inside an agent session (e.g. an autonomous build, or the
// conformance suite running under Claude Code). Those markers are inherited by
// every process the daemon spawns. A claude launched while CLAUDECODE=1 /
// CLAUDE_CODE_SESSION_ID=… are set behaves as a NESTED session and STOPS
// persisting its transcript to ~/.claude/projects — which silently breaks
// `resume` (claude --resume reports "No conversation found"). The daemon must
// present a clean, top-level environment to the agents it spawns, regardless of
// what started it. Auth/config vars (ANTHROPIC_*, CLAUDE_CONFIG_DIR, CODEX_HOME,
// SESH_*) are deliberately left untouched.
func ScrubHarnessEnv() {
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if harnessEnvMarkers[name] || strings.HasPrefix(name, "CLAUDE_CODE_") {
			os.Unsetenv(name)
		}
	}
}

// Kind is a coding-agent kind.
type Kind string

const (
	Claude Kind = "claude"
	Codex  Kind = "codex"
	Pi     Kind = "pi"
)

// EnvThreadID is injected into every spawned agent's pane environment so the
// agent can identify itself (e.g. `sesh ticket list --thread $SESH_THREAD_ID`).
const EnvThreadID = "SESH_THREAD_ID"

// EnvSeshBin is injected alongside EnvThreadID: the spawning daemon's own
// executable path. In-agent state reporters (the pi extension, the claude
// hook) exec `$SESH_BIN thread report-state ...` — resolving a bare `sesh`
// from the pane's PATH would find an older installed binary (pane login
// shells re-prepend their profile dirs ahead of anything the daemon set),
// which is exactly how a conformance daemon's reporters would silently talk
// an older schema. Absent (pre-43 spawner, adopted agent) the reporters fall
// back to PATH resolution and their failure is VISIBLE as the thread staying
// state_authority=heuristic.
const EnvSeshBin = "SESH_BIN"

// Valid reports whether k is a known agent kind.
func Valid(k Kind) bool {
	switch k {
	case Claude, Codex, Pi:
		return true
	}
	return false
}

// ParseKind validates and returns a Kind, or a loud error.
func ParseKind(s string) (Kind, error) {
	k := Kind(s)
	if !Valid(k) {
		return "", fmt.Errorf("unknown agent %q (want claude|codex|pi)", s)
	}
	return k, nil
}

// modeArgs maps a spawn mode to an agent's permission flags (E3). pi has no
// permission system: yolo ≡ default (it IS full access); sandbox for pi is
// refused upstream (config load / request validation), never silently dropped.
func modeArgs(k Kind, mode string) []string {
	switch k {
	case Claude:
		if mode == "yolo" {
			return []string{"--dangerously-skip-permissions"}
		}
	case Codex:
		switch mode {
		case "yolo":
			return []string{"--dangerously-bypass-approvals-and-sandbox"}
		case "sandbox":
			return []string{"--sandbox", "read-only"}
		}
	}
	return nil
}

// modelArgs is the per-agent model flag for an opaque model string. All three
// agents spell it `--model <m>` (claude/pi take the bare alias or provider/id;
// codex takes -m/--model on its `exec`/`resume`/top-level commands). An empty
// model selects the agent's own default — no flag. The model string is NOT
// validated here: a bad model fails LOUDLY at the agent, never a silent default.
func modelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", model}
}

// modelFlag renders the model flag as a command suffix (leading space, or "" when
// no model). The `--model` literal is unquoted (like `--session-id` in the base
// command); only the model value is shell-escaped.
func modelFlag(model string) string {
	if model == "" {
		return ""
	}
	return " --model " + ShellEscape(model)
}

// LaunchArgs joins mode flags + the config's extra args into a command suffix.
func LaunchArgs(k Kind, mode string, extra []string) string {
	parts := append(modeArgs(k, mode), extra...)
	out := ""
	for _, p := range parts {
		out += " " + ShellEscape(p)
	}
	return out
}

// ShellEscape single-quotes s for safe embedding in a shell command line.
func ShellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'''`) + "'"
}

// HeadedCommand returns the shell command that launches the agent interactively
// in a tmux pane, pinning the conversation to sessionID where the agent supports
// it (pi/claude). codex cannot pre-assign its id, so it launches bare and its id
// is discovered after the first turn (see DiscoverCodexSession) — which is what
// lets a dead thread be resumed later. Working/waiting is still detected agent-
// agnostically from pane content-diff. model ('' = the agent default) pins the
// model: claude/pi take it after the session flag, codex on its top-level command.
func HeadedCommand(k Kind, sessionID, model, mode string, extra []string) string {
	base := string(k)
	switch k {
	case Pi:
		if sessionID != "" {
			base = "pi --session-id " + sessionID
		}
	case Claude:
		if sessionID != "" {
			base = "claude --session-id " + sessionID
		}
	}
	return base + modelFlag(model) + LaunchArgs(k, mode, extra)
}

// ResumeCommand returns the shell command that RELAUNCHES the agent on an
// existing conversation (for `resume`), continuing where it left off. model
// ('' = the agent default) re-pins the model on the resumed conversation.
func ResumeCommand(k Kind, sessionID, model, mode string, extra []string) string {
	base := string(k)
	switch k {
	case Pi:
		base = "pi --session-id " + sessionID // --session-id resumes if it exists
	case Claude:
		base = "claude --resume " + sessionID
	case Codex:
		base = "codex resume " + sessionID
	}
	return base + modelFlag(model) + LaunchArgs(k, mode, extra)
}
