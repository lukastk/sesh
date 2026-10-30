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
	// MyPi is the patched pi fork, shipped as its own executable. It is a
	// SEPARATE harness kind because the executable sesh must launch differs; see
	// Family for everything it shares with Pi and Executable for what it doesn't.
	MyPi Kind = "mypi"
)

// AllKinds is the agent axis in a stable order. internal/config duplicates these
// literals on purpose (agents imports config, so the dependency cannot be
// inverted); this slice is the authority they are kept in step with.
var AllKinds = []Kind{Claude, Codex, Pi, MyPi}

// Family is the kind whose RUNTIME SHAPE k shares: its native conversation
// format, its session store, and its RPC socket protocol. mypi → pi, everything
// else → itself.
//
// This is not a convenience alias, it is the measured truth about the fork —
// measured on BOTH packagings that exist, which differ in exactly one visible
// way:
//
//   - the plain source fork (the HQ pi/ worktree) leaves package.json's
//     `piConfig.name` unset, so pi's APP_NAME stays "pi" and the process sets
//     process.title = "pi";
//   - the frozen mypi copy SETS `piConfig.name = "mypi"` and its launcher sets
//     argv[0] = "mypi", so comm/argv0/title read "mypi".
//
// Either way the conversation shape is pi's: the same JSONL v3 session records
// in the same session store (the mypi launcher uses its own agent dir,
// ~/.mypi/agent, but pins the SESSION dir back to ~/.pi/agent/sessions), and the
// same <TMPDIR>/pi-rpc-sockets/<session>.sock protocol-2 and .live.sock sockets.
// Only the executable name differs.
//
// So: anything that reads a conversation, dials a socket, or compares a pane's
// live process goes through Family; anything that LAUNCHES the agent goes
// through Executable. Routing on the family rather than on the process title is
// what makes BOTH packagings work, including the one whose panes are
// indistinguishable from pi's (see PaneKindMatches).
//
// Sesh never SETS a PI_*/MYPI_* variable for a launch — it only reads
// $PI_CODING_AGENT_DIR for its own resolution (see ResolveHomes), so whatever
// the mypi launcher exports is passed through untouched.
func Family(k Kind) Kind {
	if k == MyPi {
		return Pi
	}
	return k
}

// Executable is the agent's executable name, resolved from the launching
// daemon's PATH exactly as `pi`/`claude`/`codex` always have been. It is
// deliberately a bare name and never a path: a missing mypi is a LOUD failure at
// spawn (the pane's shell reports command not found) or at a native read
// (exec.Error → native_unavailable), never a silent fall back to pi.
func Executable(k Kind) string { return string(k) }

// PaneKindMatches reports whether a pane's DETECTED agent process is the agent a
// thread record claims. It compares families, which is load-bearing rather than
// lax: a mypi pane reports process title "pi" under the plain fork packaging and
// "mypi" under the frozen one (see Family), so a literal string comparison would
// decide — on the first of those — that every live mypi thread had no running
// agent and publish it as headless·idle: a live thread rendered dead. Comparing
// families is correct for both packagings and needs no knowledge of which is
// installed. The store stays authoritative for which executable a later launch
// uses.
func PaneKindMatches(recorded, detected Kind) bool {
	return Family(recorded) == Family(detected)
}

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

// Pi transfer identity is deliberately separate from EnvThreadID: EnvThreadID is
// historically inheritable and remains only a general agent hint, while the transfer
// consumer must bind leases to the exact authoritative thread that owns its inbox.
const EnvPiTransferThreadID = "SESH_PI_TRANSFER_THREAD_ID"
const EnvPiTransferInbox = "SESH_PI_TRANSFER_INBOX"
const EnvPiTransferAPISocket = "SESH_PI_TRANSFER_API_SOCKET"

// Valid reports whether k is a known agent kind.
func Valid(k Kind) bool {
	for _, known := range AllKinds {
		if k == known {
			return true
		}
	}
	return false
}

// KindList renders the agent axis for a message or a flag description.
func KindList() string {
	parts := make([]string, 0, len(AllKinds))
	for _, k := range AllKinds {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, "|")
}

// ParseKind validates and returns a Kind, or a loud error.
func ParseKind(s string) (Kind, error) {
	k := Kind(s)
	if !Valid(k) {
		return "", fmt.Errorf("unknown agent %q (want %s)", s, KindList())
	}
	return k, nil
}

// modeArgs maps a spawn mode to an agent's permission flags (E3). pi/mypi have
// no permission system: yolo ≡ default (it IS full access); sandbox for the pi
// family is refused upstream (config load / request validation), never silently
// dropped.
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
// it (pi/mypi/claude). codex cannot pre-assign its id, so it launches bare and
// its id is discovered after the first turn (see DiscoverCodexSession) — which is
// what lets a dead thread be resumed later. Working/waiting is still detected
// agent-agnostically from pane content-diff. model (” = the agent default) pins
// the model: claude/pi take it after the session flag, codex on its top-level
// command. The executable is always Executable(k) — mypi launches mypi.
func HeadedCommand(k Kind, sessionID, model, mode string, extra []string) string {
	base := Executable(k)
	switch Family(k) {
	case Pi, Claude:
		if sessionID != "" {
			base = Executable(k) + " --session-id " + sessionID
		}
	}
	return base + modelFlag(model) + LaunchArgs(k, mode, extra)
}

// ResumeCommand returns the shell command that RELAUNCHES the agent on an
// existing conversation (for `resume`), continuing where it left off. model
// (” = the agent default) re-pins the model on the resumed conversation.
func ResumeCommand(k Kind, sessionID, model, mode string, extra []string) string {
	base := Executable(k)
	switch Family(k) {
	case Pi:
		base = Executable(k) + " --session-id " + sessionID // --session-id resumes if it exists
	case Claude:
		base = Executable(k) + " --resume " + sessionID
	case Codex:
		base = Executable(k) + " resume " + sessionID
	}
	return base + modelFlag(model) + LaunchArgs(k, mode, extra)
}
