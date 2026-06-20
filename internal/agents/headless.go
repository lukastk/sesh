package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HeadlessTurn runs ONE turn of a headless conversation (stateless-per-turn
// model): spawn the agent's non-interactive interface, deliver the prompt,
// capture the reply, and return the (possibly newly-created) agent session id.
// The conversation persists on disk between turns; there is no live process held
// between turns — "working" is simply "a HeadlessTurn process is in flight".
//
//	started=false -> first turn (create/assign the session)
//	started=true  -> resume the existing session
//
// codex cannot pre-assign its session id, so its first turn returns the id it
// generated (newSessionID); pi and claude use the sessionID sesh pre-assigned.
//
// model ('' = the agent default) pins the model for THIS turn: claude/pi take
// `--model <m>` after the session flag, codex takes it right after `exec`.
func HeadlessTurn(kind Kind, threadID, sessionID, cwd string, started bool, prompt, codexHome, mode, model string) (reply, newSessionID string, err error) {
	// The turn process carries the thread identity, exactly like a pane does:
	// the agent (or anything it runs) can `sesh info` itself without being
	// told who it is.
	tidEnv := []string{EnvThreadID + "=" + threadID}
	switch kind {
	case Pi:
		// pi --session-id creates-if-missing and resumes uniformly.
		args := append([]string{"--print", "--session-id", sessionID}, modelArgs(model)...)
		args = append(args, prompt)
		out, err := runHeadless(cwd, tidEnv, "pi", args...)
		return strings.TrimSpace(out), sessionID, err

	case Claude:
		args := append([]string{"--print"}, modeArgs(Claude, mode)...)
		if started {
			args = append(args, "--resume", sessionID)
		} else {
			args = append(args, "--session-id", sessionID)
		}
		args = append(args, modelArgs(model)...)
		args = append(args, prompt)
		out, err := runHeadless(cwd, tidEnv, "claude", args...)
		return strings.TrimSpace(out), sessionID, err

	case Codex:
		env := append([]string{}, tidEnv...)
		if codexHome != "" {
			env = append(env, "CODEX_HOME="+codexHome)
		}
		// --model goes right after `exec` (a parent-command flag, before the
		// optional `resume <id>` subcommand).
		args := append([]string{"exec"}, modelArgs(model)...)
		if started {
			args = append(args, "resume", sessionID)
		}
		args = append(args, modeArgs(Codex, mode)...)
		args = append(args, "--json", "--skip-git-repo-check", prompt)
		out, err := runHeadless(cwd, env, "codex", args...)
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
		if started {
			id = sessionID
		}
		return reply, id, nil

	default:
		return "", "", fmt.Errorf("headless: unknown agent %q", kind)
	}
}

// runHeadless runs an agent command in cwd with no stdin (so codex/others don't
// block reading it), returning stdout. stderr is folded into the error.
func runHeadless(cwd string, extraEnv []string, name string, args ...string) (string, error) {
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
	cmd.Dir = cwd
	cmd.Stdin = nil
	if extraEnv != nil {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
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
