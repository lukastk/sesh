package codexfs

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/lukastk/sesh/internal/agents/convo"
)

// Display decoding of codex's rollout JSONL (re-verified against a live
// codex-cli 0.147 rollout). Each line carries a record-level `timestamp` and an
// `ordinal`; the conversation-bearing payloads are all `response_item`s:
//
//	{type:"message", id, role:"user"|"assistant"|"developer",
//	                 content:[{type:"input_text"|"output_text",text}]}
//	{type:"reasoning", id, summary:[{text}]}
//	{type:"function_call", id, call_id, name, arguments:"<json string>"}
//	{type:"function_call_output", id, call_id, output:[{text}] | "…"}
//	{type:"custom_tool_call", id, call_id, name, input:"…"}
//	{type:"custom_tool_call_output", id, call_id, output:[{text}] | "…"}
//
// event_msg (task_started/task_complete/token_count/item_completed/…),
// session_meta, turn_context, world_state and compacted are not conversation
// records here: item_completed in particular DUPLICATES the response_item for
// the same message, so projecting both would show every codex turn twice. They
// are reported as skipped records. event_msg task markers are still what
// DeriveTurnStatus reads for the turn status.
//
// A `reasoning` item whose summary is empty (the common case — the content is
// encrypted) normalizes to an empty record and is skipped rather than rendered
// as a blank thinking bubble.
//
// Decoding is TWO-STAGE, and that is load-bearing rather than tidy: a rollout's
// non-response_item records reuse the same `payload` object for unrelated
// settings, and `turn_context.payload.summary` is a STRING where a reasoning
// item's summary is a LIST. Decoding the detailed payload for every line made
// 300 perfectly valid lines of a live 70,844-line rollout report as unreadable —
// and a mismatch on the FINAL line would freeze a forward poll's anchor there.
// So the record type is read first, and the typed payload only for the records
// this projection actually claims to understand.
type displayHead struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type string `json:"type"`
	} `json:"payload"`
}

type displayLine struct {
	Payload struct {
		ID        string          `json:"id"`
		Role      string          `json:"role"`
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		Content   json.RawMessage `json:"content"`
		Output    json.RawMessage `json:"output"`
		Summary   []struct {
			Text string `json:"text"`
		} `json:"summary"`
	} `json:"payload"`
}

type displayBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// DisplayRecord decodes one codex rollout line into a normalized record.
// ok=false means a recognized-but-not-conversational record; a non-nil error
// means the line is not valid JSON.
func DisplayRecord(raw []byte) (convo.Record, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return convo.Record{}, false, nil
	}
	var head displayHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return convo.Record{}, false, err
	}
	if head.Type != "response_item" {
		return convo.Record{}, false, nil
	}
	switch head.Payload.Type {
	case "message", "reasoning", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
	default:
		return convo.Record{}, false, nil
	}
	var l displayLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return convo.Record{}, false, err
	}
	rec := convo.Record{
		ID: l.Payload.ID, NativeType: head.Type, NativeSubtype: head.Payload.Type,
		AtUnixMS: convo.TimeMS(head.Timestamp),
	}
	switch head.Payload.Type {
	case "message":
		text := decodeDisplayText(l.Payload.Content)
		switch l.Payload.Role {
		case "user":
			rec.Role = convo.RoleUser
			rec.Text = text
			// codex injects its own environment/permissions context as user
			// messages. The marker is their exact leading tag, so the flag is
			// advisory metadata and nothing is hidden on its account.
			trimmed := strings.TrimLeft(text, " \t\r\n")
			rec.Injected = strings.HasPrefix(trimmed, "<environment_context") || strings.HasPrefix(trimmed, "<permissions")
		case "assistant":
			rec.Role = convo.RoleAssistant
			rec.Text = text
		case "developer":
			rec.Role = convo.RoleSystem
			rec.Text = text
			rec.Injected = true
		default:
			return convo.Record{}, false, nil
		}
		return rec, true, nil
	case "reasoning":
		parts := make([]string, 0, len(l.Payload.Summary))
		for _, s := range l.Payload.Summary {
			parts = append(parts, s.Text)
		}
		rec.Role = convo.RoleAssistant
		rec.Thinking = convo.JoinNonEmpty(parts, "\n")
		return rec, true, nil
	case "function_call", "custom_tool_call":
		input := l.Payload.Arguments
		if input == "" {
			input = l.Payload.Input
		}
		rec.Role = convo.RoleTool
		rec.Tools = []convo.Tool{{CallID: l.Payload.CallID, Name: l.Payload.Name, Input: input}}
		return rec, true, nil
	case "function_call_output", "custom_tool_call_output":
		rec.Role = convo.RoleTool
		rec.Tools = []convo.Tool{{CallID: l.Payload.CallID, Output: decodeDisplayText(l.Payload.Output)}}
		return rec, true, nil
	default:
		return convo.Record{}, false, nil
	}
}

// decodeDisplayText renders a codex content/output value: a bare string, or an
// array of text-bearing blocks (input_text / output_text / text).
func decodeDisplayText(value json.RawMessage) string {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return s
		}
		return ""
	}
	var blocks []displayBlock
	if json.Unmarshal(trimmed, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "input_text", "output_text", "text":
			parts = append(parts, b.Text)
		}
	}
	return convo.JoinNonEmpty(parts, "\n")
}
