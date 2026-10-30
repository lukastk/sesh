// Package convo is the normalized shape a harness's native conversation record
// is decoded INTO, and nothing else: no file layout, no bounds, no transport.
//
// It exists because each harness writes a different JSONL schema (claude's
// user/assistant/system lines, pi's message records, codex's rollout
// response_items) and the Sesh display projection needs one vocabulary over all
// of them. The per-format decoders live with their format — agents/claude,
// agents/pi, agents/codexfs — and this package is the only thing they share, so
// it deliberately has no dependencies and cannot import them back.
//
// A Record is a faithful normalization, not an interpretation: roles come from
// the native record's own role, text and thinking are the native blocks' own
// text, and nothing is summarized or rewritten. Bounds, stable ids, ordering and
// coverage are applied one layer up (agents.ReadDisplayWindow).
package convo

import (
	"strings"
	"time"
)

// The display roles. They mirror the native record's own role; Tool is used
// where the native record IS a tool interaction (pi's toolResult record,
// claude's tool_result-carrying user record, codex's function_call /
// function_call_output items) rather than a message with a tool call in it.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

// Tool is one tool interaction carried by a record. A call contributes Name and
// Input; a result contributes Output and IsError. CallID is the harness's own
// correlation id where it has one, so a client can pair them without Sesh
// inventing a pairing.
type Tool struct {
	CallID  string
	Name    string
	Input   string
	Output  string
	IsError bool
}

// Record is one native conversation record, normalized.
//
// ID is the harness's own stable record id ("" for formats that have none —
// codex rollout items and claude's queue-operation records; the projection
// falls back to the source line there). NativeType/NativeSubtype carry the
// record's own type words so a client can label a system record without Sesh
// synthesizing prose for it. AtUnixMS is 0 when the native record has no
// timestamp.
type Record struct {
	ID            string
	Role          string
	NativeType    string
	NativeSubtype string
	AtUnixMS      int64
	Text          string
	Thinking      string
	Tools         []Tool
	// Injected marks a record whose ROLE is user-ish but whose author is the
	// harness, not the human: codex's <environment_context>/<permissions>
	// context messages and claude's isMeta records. It is advisory presentation
	// metadata — nothing is dropped on its account.
	Injected bool
}

// Empty reports whether a record carries nothing displayable. Such records are
// counted in coverage rather than emitted, which is what keeps a claude
// transcript's thousands of attachment/api-request lines out of a chat view.
func (r Record) Empty() bool {
	return r.Text == "" && r.Thinking == "" && len(r.Tools) == 0
}

// TimeMS parses a native ISO-8601 timestamp into Unix milliseconds. An absent or
// unparseable stamp returns 0 — the projection reports a timestamp only where the
// native record actually has one, and never guesses a clock.
func TimeMS(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// JoinNonEmpty joins the non-empty parts with sep — the one shared helper the
// decoders need for multi-block text.
func JoinNonEmpty(parts []string, sep string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
