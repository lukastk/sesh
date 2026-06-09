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
// in a tmux pane, pinning the conversation to sessionID where the agent supports
// it (pi/claude). codex cannot pre-assign its id, so it launches bare and its id
// is discovered after the first turn (see DiscoverCodexSession) — which is what
// lets a dead thread be resumed later. Working/waiting is still detected agent-
// agnostically from pane content-diff.
func HeadedCommand(k Kind, sessionID string) string {
	switch k {
	case Pi:
		if sessionID != "" {
			return "pi --session-id " + sessionID
		}
	case Claude:
		if sessionID != "" {
			return "claude --session-id " + sessionID
		}
	}
	return string(k) // codex (bare), or no session id
}

// ResumeCommand returns the shell command that RELAUNCHES the agent on an
// existing conversation (for `resume`), continuing where it left off.
func ResumeCommand(k Kind, sessionID string) string {
	switch k {
	case Pi:
		return "pi --session-id " + sessionID // --session-id resumes if it exists
	case Claude:
		return "claude --resume " + sessionID
	case Codex:
		return "codex resume " + sessionID
	default:
		return string(k)
	}
}
