// Package fork branches an agent conversation into a new session: it copies a
// source transcript's prefix (up to a chosen turn) into a NEW session id and
// rewrites the embedded session id, so the agent resumes continuing from that
// point. The mechanic is uniform across claude/codex/pi; only transcript
// location, the session-id field, and turn detection differ (verified live in
// exp17). Addressing is a turn ordinal (`--message-id N` = after the Nth
// assistant turn), which works for codex too (no per-message ids) and snaps the
// cut to a turn boundary so tool-call/result pairs aren't split.
package fork

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents/claude"
	codex "github.com/lukastk/sesh/internal/agents/codexfs"
	"github.com/lukastk/sesh/internal/agents/pi"
)

type fmeta struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
	} `json:"payload"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func hasText(content json.RawMessage) bool {
	c := strings.TrimSpace(string(content))
	if c == "" {
		return false
	}
	if c[0] == '"' { // bare string
		return len(c) > 2
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// isTurnBoundary reports whether a line completes an assistant turn for the
// agent — the unit `--message-id` counts.
func isTurnBoundary(agent string, m fmeta) bool {
	switch agent {
	case "claude":
		return m.Type == "assistant" && m.Message != nil && hasText(m.Message.Content)
	case "pi":
		return m.Type == "message" && m.Message != nil && m.Message.Role == "assistant" && hasText(m.Message.Content)
	case "codex":
		return m.Payload.Type == "task_complete"
	}
	return false
}

// rewriteLine substitutes the session id on a line where it appears: claude
// carries sessionId on EVERY line; codex only on session_meta; pi only on the
// session line. Plain string replace preserves key order (agents re-parse).
func rewriteLine(agent, line, oldID, newID string, m fmeta) string {
	switch agent {
	case "claude":
		return strings.ReplaceAll(line, oldID, newID)
	case "codex":
		if m.Type == "session_meta" {
			return strings.ReplaceAll(line, oldID, newID)
		}
	case "pi":
		if m.Type == "session" {
			return strings.ReplaceAll(line, oldID, newID)
		}
	}
	return line
}

// Branch returns the forked transcript lines for newID. afterTurn<=0 forks the
// whole transcript ("from latest"); otherwise it keeps the prefix through the
// afterTurn-th assistant turn.
func Branch(agent string, lines []string, oldID, newID string, afterTurn int) ([]string, error) {
	if agent != "claude" && agent != "codex" && agent != "pi" {
		return nil, fmt.Errorf("fork unsupported for agent %q", agent)
	}
	out := make([]string, 0, len(lines))
	turns := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var m fmeta
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			return nil, fmt.Errorf("fork: parse transcript line: %w", err)
		}
		out = append(out, rewriteLine(agent, ln, oldID, newID, m))
		if isTurnBoundary(agent, m) {
			turns++
			if afterTurn > 0 && turns >= afterTurn {
				break
			}
		}
	}
	if afterTurn > 0 && turns < afterTurn {
		return nil, fmt.Errorf("fork: source has only %d assistant turn(s); cannot fork after turn %d", turns, afterTurn)
	}
	return out, nil
}

// DestPath returns where to write the forked transcript for newID on THIS
// machine, given the agent's home dirs and the (source) cwd. The new file is
// keyed by newID so the agent's resume locates it.
func DestPath(agent, claudeHome, codexHome, piHome, cwd, newID string) (string, error) {
	now := time.Now().UTC()
	switch agent {
	case "claude":
		return claude.TranscriptPath(claudeHome, cwd, newID), nil
	case "pi":
		ts := now.Format("2006-01-02T15-04-05-000Z")
		return filepath.Join(pi.SessionsRoot(piHome), pi.CwdDirName(cwd), ts+"_"+newID+".jsonl"), nil
	case "codex":
		ts := now.Format("2006-01-02T15-04-05")
		return filepath.Join(codex.SessionsRoot(codexHome),
			now.Format("2006"), now.Format("01"), now.Format("02"),
			"rollout-"+ts+"-"+newID+".jsonl"), nil
	}
	return "", fmt.Errorf("fork unsupported for agent %q", agent)
}
