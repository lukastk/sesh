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
func HeadlessTurn(kind Kind, sessionID, cwd string, started bool, prompt, codexHome string) (reply, newSessionID string, err error) {
	switch kind {
	case Pi:
		// pi --session-id creates-if-missing and resumes uniformly.
		out, err := runHeadless(cwd, nil, "pi", "--print", "--session-id", sessionID, prompt)
		return strings.TrimSpace(out), sessionID, err

	case Claude:
		args := []string{"--print"}
		if started {
			args = append(args, "--resume", sessionID)
		} else {
			args = append(args, "--session-id", sessionID)
		}
		args = append(args, prompt)
		out, err := runHeadless(cwd, nil, "claude", args...)
		return strings.TrimSpace(out), sessionID, err

	case Codex:
		var env []string
		if codexHome != "" {
			env = append(env, "CODEX_HOME="+codexHome)
		}
		args := []string{"exec"}
		if started {
			args = append(args, "resume", sessionID)
		}
		args = append(args, "--json", "--skip-git-repo-check", prompt)
		out, err := runHeadless(cwd, env, "codex", args...)
		if err != nil {
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
		return "", fmt.Errorf("headless %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
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
