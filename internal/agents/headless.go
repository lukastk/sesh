package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TurnSpec is one headless turn: which conversation to run, what to say, and how
// the caller wants to be told about the process that runs it.
//
// It replaced a nine-positional-parameter signature when the turn gained an
// identity contract (SeshBin, OnStart). Nine was already at the limit; adding a
// callback and a path to it positionally would have made the single call site
// unreadable and every future carrier a wider blast radius.
type TurnSpec struct {
	Kind      Kind
	ThreadID  string
	SessionID string
	Cwd       string
	// Started is false on the FIRST turn (create/assign the agent session) and
	// true afterwards (resume it).
	Started   bool
	Prompt    string
	CodexHome string
	Mode      string
	// Model pins the model for THIS turn ('' = the agent default).
	Model string
	// SeshBin is the LAUNCHING daemon's own executable, injected as $SESH_BIN so
	// the worker can call sesh unambiguously. See TurnEnv.
	SeshBin string
	// OnStart is called with the pid of the turn's ROOT process (the $SHELL -c
	// wrapper) once it has started, before the turn is waited on. The daemon
	// records it so it can later answer "is this process running inside a turn I
	// launched?" — the verified identity a headless worker otherwise has no way
	// to obtain (see internal/procs). Optional: nil means nobody is watching.
	OnStart func(pid int)
}

// HeadlessTurn runs ONE turn of a headless conversation (stateless-per-turn
// model): spawn the agent's non-interactive interface, deliver the prompt,
// capture the reply, and return the (possibly newly-created) agent session id.
// The conversation persists on disk between turns; there is no live process held
// between turns — "working" is simply "a HeadlessTurn process is in flight".
//
// codex cannot pre-assign its session id, so its first turn returns the id it
// generated (newSessionID); pi and claude use the SessionID sesh pre-assigned.
func HeadlessTurn(spec TurnSpec) (reply, newSessionID string, err error) {
	// ONE environment for all three agents, built once (see TurnEnv). The
	// per-agent branches below must never touch it: a carrier added to one
	// branch and forgotten in another is how a worker ends up with an identity
	// on two agents and none on the third.
	env := TurnEnv(os.Environ(), spec.ThreadID, spec.SeshBin, spec.CodexHome)

	switch spec.Kind {
	case Pi:
		// pi --session-id creates-if-missing and resumes uniformly.
		args := append([]string{"--print", "--session-id", spec.SessionID}, modelArgs(spec.Model)...)
		args = append(args, spec.Prompt)
		out, err := runHeadless(spec, env, "pi", args...)
		return strings.TrimSpace(out), spec.SessionID, err

	case Claude:
		args := append([]string{"--print"}, modeArgs(Claude, spec.Mode)...)
		if spec.Started {
			args = append(args, "--resume", spec.SessionID)
		} else {
			args = append(args, "--session-id", spec.SessionID)
		}
		args = append(args, modelArgs(spec.Model)...)
		args = append(args, spec.Prompt)
		out, err := runHeadless(spec, env, "claude", args...)
		return strings.TrimSpace(out), spec.SessionID, err

	case Codex:
		// --model goes right after `exec` (a parent-command flag, before the
		// optional `resume <id>` subcommand).
		args := append([]string{"exec"}, modelArgs(spec.Model)...)
		if spec.Started {
			args = append(args, "resume", spec.SessionID)
		}
		args = append(args, modeArgs(Codex, spec.Mode)...)
		args = append(args, "--json", "--skip-git-repo-check", spec.Prompt)
		out, err := runHeadless(spec, env, "codex", args...)
		if err != nil {
			// codex writes its error events to STDOUT (the --json stream), not
			// stderr, so the bare exit-status error is useless on its own. Surface
			// the codex-reported reason (e.g. a model rejected by the account) so
			// the failure is LOUD, not a cryptic "exit status 1".
			if ce := parseCodexError(out); ce != "" {
				return "", "", fmt.Errorf("%w: %s", err, ce)
			}
			return "", "", err
		}
		reply, id := parseCodexExec(out)
		if spec.Started {
			id = spec.SessionID
		}
		return reply, id, nil

	default:
		return "", "", fmt.Errorf("headless: unknown agent %q", spec.Kind)
	}
}

// TurnEnv builds the COMPLETE environment of a headless turn process: the
// launching daemon's own environment, minus the variables it must not pass on,
// plus the turn's identity carriers.
//
// WHAT IS REMOVED, and it is not cosmetic: $TMUX and $TMUX_PANE. A headless turn
// has no pane by definition, but the environment it inherits is the DAEMON's,
// and a daemon started from inside a tmux pane would hand every turn process a
// live pane reference belonging to whatever started the daemon. Current-thread
// inference reads exactly those two variables to find the pane marker it treats
// as GROUND TRUTH — the most trusted source in the model — so leaving them in
// place would let a headless worker resolve a stranger's thread through the one
// source that is meant to be unfakeable. Every supervised daemon on the fleet
// was checked and carries neither, so in production this removes nothing; a
// hand-started or test daemon carries both, and the property should not rest on
// how the daemon happened to be started.
//
// WHAT IS ADDED: $SESH_THREAD_ID (unchanged, and still only a hint — never an
// identity on its own) and $SESH_BIN, the launching daemon's own executable.
// Panes have carried SESH_BIN since schema 43; turns did not, so a worker that
// wanted to call sesh had to hope PATH resolved something compatible — and from
// a login shell it may not: myrig defines a `sesh` FUNCTION that re-pins
// SESH_HOME, and profile directories can shadow the daemon's binary with an
// older install. A worker asking who it is should not have to guess which sesh
// it is asking.
//
// CODEX_HOME is set only when the caller resolved one, and is left inherited
// otherwise — exactly the pre-existing behavior.
func TurnEnv(base []string, threadID, seshBin, codexHome string) []string {
	drop := map[string]bool{
		"TMUX":      true,
		"TMUX_PANE": true,
		// Dropped so the values below appear ONCE. Trailing duplicates win in
		// practice, but an environment with two SESH_THREAD_IDs is a thing no
		// reader of `tr '\0' '\n' < /proc/<pid>/environ` should have to reason
		// about.
		EnvThreadID: true,
		EnvSeshBin:  true,
	}
	if codexHome != "" {
		drop["CODEX_HOME"] = true
	}
	out := make([]string, 0, len(base)+3)
	for _, e := range base {
		name, _, _ := strings.Cut(e, "=")
		if drop[name] {
			continue
		}
		out = append(out, e)
	}
	out = append(out, EnvThreadID+"="+threadID)
	if seshBin != "" {
		out = append(out, EnvSeshBin+"="+seshBin)
	}
	if codexHome != "" {
		out = append(out, "CODEX_HOME="+codexHome)
	}
	return out
}

// runHeadless runs an agent command in spec.Cwd with no stdin (so codex/others
// don't block reading it), returning stdout. stderr is folded into the error.
func runHeadless(spec TurnSpec, env []string, name string, args ...string) (string, error) {
	// Run the agent THROUGH the user's shell ($SHELL -c), exactly as a tmux pane
	// would: a headless turn is the same conversation as a pane turn, so it must
	// see the same environment. zsh sources ~/.zshenv for every invocation, which
	// is where interactive setups (PATH additions, API keys) actually live — a
	// bare exec from the daemon would miss them all (observed live: pi turns
	// failed provider auth under the supervised daemon while panes worked).
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	quoted := make([]string, 0, len(args)+1)
	quoted = append(quoted, shellQuoteArg(name))
	for _, a := range args {
		quoted = append(quoted, shellQuoteArg(a))
	}
	cmd := exec.Command(shell, "-c", strings.Join(quoted, " "))
	cmd.Dir = spec.Cwd
	cmd.Stdin = nil
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Start/Wait rather than Run, so the turn's root pid can be published the
	// moment it exists: it is the identity of everything the turn goes on to run
	// (internal/procs), and it is only knowable between these two calls.
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("headless %s: %w", name, err)
	}
	if spec.OnStart != nil && cmd.Process != nil {
		spec.OnStart(cmd.Process.Pid)
	}
	if err := cmd.Wait(); err != nil {
		// Return stdout alongside the error: some agents (codex --json) report the
		// real failure reason on stdout, which the caller parses for a loud message.
		return stdout.String(), fmt.Errorf("headless %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// shellQuoteArg single-quotes a string for POSIX shells.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseCodexExec extracts the agent reply and the session (thread) id from
// `codex exec --json` JSONL output.
func parseCodexExec(out string) (reply, sessionID string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "thread.started" && ev.ThreadID != "":
			sessionID = ev.ThreadID
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			reply = ev.Item.Text
		}
	}
	return reply, sessionID
}

// parseCodexError extracts the human-readable failure reason from `codex exec
// --json` JSONL output (the `error` / `turn.failed` events codex writes to stdout
// on failure). Returns "" when no error event is present. The message is often
// itself a JSON-encoded string — returned verbatim, which is enough to surface
// the cause (e.g. an unsupported model name) loudly.
func parseCodexError(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "error":
			if ev.Message != "" {
				return ev.Message
			}
		case "turn.failed":
			if ev.Error.Message != "" {
				return ev.Error.Message
			}
		}
	}
	return ""
}
