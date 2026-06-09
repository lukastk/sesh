// Package agents knows how to launch and identify the three coding agents
// (claude, codex, pi). It deliberately holds only the per-agent specifics; the
// thread lifecycle (tmux, storage, routing) lives in the daemon.
package agents

import "fmt"

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

// HeadedCommand returns the shell command that launches the agent interactively
// in a tmux pane. The bare binary opens each agent's TUI; the binary is resolved
// from PATH on the target machine.
//
// (Working/waiting is detected agent-agnostically from pane content-diff, so no
// per-agent transcript wiring is needed at spawn. A transcript fallback is noted
// in _dev/SPEC.md §3 for any agent later found to have non-animating silent
// turns.)
func HeadedCommand(k Kind) string { return string(k) }
