package claude

import (
	"bytes"
	"encoding/json"

	"github.com/lukastk/sesh/internal/agents/convo"
)

// Display decoding of claude's transcript JSONL (re-verified against a live
// 57,292-record Claude Code 2.1.x transcript). The conversation-bearing records
// are:
//
//	{type:"user",      uuid, timestamp, isMeta, message:{role, content: string |
//	                   [{type:"text",text}|{type:"tool_result",tool_use_id,content,is_error}]}}
//	{type:"assistant", uuid, timestamp, message:{content:[{type:"text",text}|
//	                   {type:"thinking",thinking}|{type:"tool_use",id,name,input}]}}
//	{type:"system",    uuid, timestamp, subtype, content:"…"}
//	{type:"queue-operation", operation:"enqueue", content:"…"}
//
// The queue-operation arm is load-bearing and is the behaviour the sesh-ui
// client parser documented: a message typed while claude is MID-TURN is recorded
// only as an enqueue, because claude consumes the queue entry when it processes
// the message and writes no separate user record. Without this arm a queued send
// is invisible in a transcript view even though the agent answered it.
//
// Everything else a live transcript contains in bulk — attachment,
// api-request/-blob/-shape, file-history-*, mode, permission-mode, pr-link,
// last-prompt, ai-title, atis-latch, cost-state, queue-operation values other
// than enqueue — carries no conversation content and is reported as a skipped
// record.
type displayLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	UUID      string          `json:"uuid"`
	Timestamp string          `json:"timestamp"`
	IsMeta    bool            `json:"isMeta"`
	Operation string          `json:"operation"`
	Content   json.RawMessage `json:"content"`
	Message   *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type displayBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// DisplayRecord decodes one claude transcript line into a normalized record.
// ok=false means a recognized-but-not-conversational record; a non-nil error
// means the line is not valid JSON.
func DisplayRecord(raw []byte) (convo.Record, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return convo.Record{}, false, nil
	}
	var l displayLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return convo.Record{}, false, err
	}
	rec := convo.Record{
		ID: l.UUID, NativeType: l.Type, NativeSubtype: l.Subtype,
		AtUnixMS: convo.TimeMS(l.Timestamp),
	}
	switch l.Type {
	case "queue-operation":
		if l.Operation != "enqueue" {
			return convo.Record{}, false, nil
		}
		text, ok := decodeDisplayString(l.Content)
		if !ok {
			return convo.Record{}, false, nil
		}
		rec.Role = convo.RoleUser
		rec.NativeSubtype = l.Operation
		rec.Text = text
		return rec, true, nil
	case "system":
		text, _ := decodeDisplayString(l.Content)
		rec.Role = convo.RoleSystem
		rec.Text = text
		rec.Injected = true
		return rec, true, nil
	case "user", "assistant":
		if l.Message == nil {
			return convo.Record{}, false, nil
		}
	default:
		return convo.Record{}, false, nil
	}
	if text, ok := decodeDisplayString(l.Message.Content); ok {
		rec.Role = convo.RoleUser
		if l.Type == "assistant" {
			rec.Role = convo.RoleAssistant
		}
		rec.Text = text
		rec.Injected = l.IsMeta
		return rec, true, nil
	}
	text, thinking, tools, hasResult := decodeDisplayBlocks(l.Message.Content)
	rec.Text, rec.Thinking, rec.Tools = text, thinking, tools
	switch {
	case l.Type == "assistant":
		rec.Role = convo.RoleAssistant
	case hasResult:
		// A user record whose content is tool_result blocks is claude DELIVERING
		// a result, not a human turn.
		rec.Role = convo.RoleTool
	default:
		rec.Role = convo.RoleUser
		rec.Injected = l.IsMeta
	}
	return rec, true, nil
}

// decodeDisplayString returns the value when it is a bare JSON string.
func decodeDisplayString(value json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(trimmed, &s) != nil {
		return "", false
	}
	return s, true
}

func decodeDisplayBlocks(content json.RawMessage) (text, thinking string, tools []convo.Tool, hasResult bool) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return "", "", nil, false
	}
	var blocks []displayBlock
	if json.Unmarshal(trimmed, &blocks) != nil {
		return "", "", nil, false
	}
	var texts, thoughts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "thinking":
			thoughts = append(thoughts, b.Thinking)
		case "tool_use":
			input := ""
			if in := bytes.TrimSpace(b.Input); len(in) > 0 && string(in) != "null" {
				input = string(in)
			}
			tools = append(tools, convo.Tool{CallID: b.ID, Name: b.Name, Input: input})
		case "tool_result":
			hasResult = true
			output, ok := decodeDisplayString(b.Content)
			if !ok {
				output = decodeResultBlocks(b.Content)
			}
			tools = append(tools, convo.Tool{CallID: b.ToolUseID, Output: output, IsError: b.IsError})
		}
	}
	return convo.JoinNonEmpty(texts, "\n"), convo.JoinNonEmpty(thoughts, "\n"), tools, hasResult
}

// decodeResultBlocks renders a tool_result whose content is itself a block array.
func decodeResultBlocks(content json.RawMessage) string {
	var blocks []displayBlock
	if json.Unmarshal(bytes.TrimSpace(content), &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return convo.JoinNonEmpty(parts, "\n")
}
