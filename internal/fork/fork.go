// Package fork branches an agent conversation into a new session: it copies a
// source transcript's prefix (up to a chosen turn) into a NEW session id and
// rewrites the embedded session id, so the agent resumes continuing from that
// point. Claude branches also receive a fresh message-UUID graph so its native
// resume-lineage resolver cannot merge the intentional branch back into the
// source. The mechanic is otherwise uniform across claude/codex/pi; only
// transcript location, identity fields, and turn detection differ (verified
// live in exp17). Addressing is a turn ordinal (`--message-id N` = after the Nth
// assistant turn), which works for codex too (no per-message ids) and snaps the
// cut to a turn boundary so tool-call/result pairs aren't split.
package fork

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

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

// rewriteLine substitutes the session id on the format-specific metadata line.
// Claude is handled separately by rewriteClaudeBranch: an independent branch
// needs a fresh message-identity graph as well as a fresh session id.
func rewriteLine(agent, line, oldID, newID string, m fmeta) string {
	switch agent {
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

var claudeIdentityFields = [...]string{"uuid", "parentUuid", "logicalParentUuid"}

// rewriteClaudeBranch gives an intentional sesh branch its own conversation
// graph. Claude's native resume/rewind copies preserve message UUIDs, and the
// transcript resolver deliberately uses that shared root to follow session-id
// drift. Leaving those UUIDs unchanged here therefore makes the resolver merge
// an intentional branch back into its source. Re-keying only Claude's top-level
// graph identifiers keeps tool-use/result identifiers and message content
// untouched while making the two conversations unambiguously independent.
func rewriteClaudeBranch(lines []string, newSessionID string) ([]string, error) {
	ids := map[string]string{}
	parsed := make([]map[string]json.RawMessage, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &parsed[i]); err != nil {
			return nil, fmt.Errorf("fork: parse claude transcript line: %w", err)
		}
		for _, field := range claudeIdentityFields {
			raw, ok := parsed[i][field]
			if !ok || string(raw) == "null" {
				continue
			}
			var old string
			if err := json.Unmarshal(raw, &old); err != nil {
				return nil, fmt.Errorf("fork: parse claude %s: %w", field, err)
			}
			if old != "" {
				ids[old] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(newSessionID+"\x00"+old)).String()
			}
		}
	}

	out := make([]string, 0, len(lines))
	for _, obj := range parsed {
		if _, ok := obj["sessionId"]; ok {
			obj["sessionId"], _ = json.Marshal(newSessionID)
		}
		for _, field := range claudeIdentityFields {
			raw, ok := obj[field]
			if !ok || string(raw) == "null" {
				continue
			}
			var old string
			if err := json.Unmarshal(raw, &old); err != nil {
				return nil, fmt.Errorf("fork: parse claude %s: %w", field, err)
			}
			if replacement, ok := ids[old]; ok {
				obj[field], _ = json.Marshal(replacement)
			}
		}
		rewritten, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("fork: rewrite claude transcript line: %w", err)
		}
		out = append(out, string(rewritten))
	}
	return out, nil
}

// Branch returns the forked transcript lines for newID. afterTurn<=0 forks the
// whole transcript ("from latest"); otherwise it keeps the prefix through the
// afterTurn-th assistant turn.
func Branch(agent string, lines []string, oldID, newID string, afterTurn int) ([]string, error) {
	if agent != "claude" && agent != "codex" && agent != "pi" {
		return nil, fmt.Errorf("fork unsupported for agent %q", agent)
	}
	selected := make([]string, 0, len(lines))
	metas := make([]fmeta, 0, len(lines))
	turns := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var m fmeta
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			return nil, fmt.Errorf("fork: parse transcript line: %w", err)
		}
		selected = append(selected, ln)
		metas = append(metas, m)
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
	if agent == "claude" {
		return rewriteClaudeBranch(selected, newID)
	}
	out := make([]string, 0, len(selected))
	for i, line := range selected {
		out = append(out, rewriteLine(agent, line, oldID, newID, metas[i]))
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
