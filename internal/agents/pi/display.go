package pi

import (
	"bytes"
	"encoding/json"

	"github.com/lukastk/sesh/internal/agents/convo"
)

// Display decoding of pi's session JSONL (v3, re-verified against a live
// 6,819-record pi 1.4.0 session). Every record carries `id`, `timestamp` and
// `parentId`; the conversation-bearing ones are:
//
//	{type:"message", message:{role:"user",      content:[{type:"text",text}]}}
//	{type:"message", message:{role:"assistant", content:[{type:"text",text}|
//	                                                     {type:"thinking",thinking}|
//	                                                     {type:"toolCall",id,name,arguments}]}}
//	{type:"message", message:{role:"toolResult", toolCallId, toolName, isError,
//	                                             content:[{type:"text",text}]}}
//	{type:"message", message:{role:"system", content, sections:{…}}}
//	{type:"compaction", summary:"…"}
//
// model_change, thinking_level_change, context_edit, custom and the session
// header carry no conversation content and are reported as skipped records
// rather than rendered as invented prose.
//
// Only the RECORD-level `timestamp` is read: a toolResult's inner
// message.timestamp is a monotonic number in live data, not an epoch.
type displayLine struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Summary   string `json:"summary"`
	Message   *struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		IsError    bool            `json:"isError"`
	} `json:"message"`
}

type displayBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// DisplayRecord decodes one pi JSONL line into a normalized record. ok=false
// means the line is a recognized-but-not-conversational record (or blank); a
// non-nil error means the line is not valid JSON.
func DisplayRecord(raw []byte) (convo.Record, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return convo.Record{}, false, nil
	}
	var l displayLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return convo.Record{}, false, err
	}
	rec := convo.Record{ID: l.ID, NativeType: l.Type, AtUnixMS: convo.TimeMS(l.Timestamp)}
	switch {
	case l.Type == "compaction":
		rec.Role = convo.RoleSystem
		rec.Text = l.Summary
		return rec, true, nil
	case l.Type != "message" || l.Message == nil:
		return convo.Record{}, false, nil
	}
	rec.NativeSubtype = l.Message.Role
	text, thinking, calls := decodeDisplayBlocks(l.Message.Content)
	switch l.Message.Role {
	case "user":
		rec.Role = convo.RoleUser
		rec.Text = text
	case "assistant":
		rec.Role = convo.RoleAssistant
		rec.Text, rec.Thinking, rec.Tools = text, thinking, calls
	case "toolResult":
		rec.Role = convo.RoleTool
		rec.Tools = []convo.Tool{{
			CallID: l.Message.ToolCallID, Name: l.Message.ToolName,
			Output: text, IsError: l.Message.IsError,
		}}
	case "system":
		// pi's system record holds the assembled system prompt. It is the
		// harness's own text, never a human turn.
		rec.Role = convo.RoleSystem
		rec.Text = text
		rec.Injected = true
	default:
		return convo.Record{}, false, nil
	}
	return rec, true, nil
}

// decodeDisplayBlocks splits a pi content value (a bare string, or an array of
// text/thinking/toolCall blocks) into its text, thinking and tool calls.
func decodeDisplayBlocks(content json.RawMessage) (text, thinking string, calls []convo.Tool) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return "", "", nil
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return s, "", nil
		}
		return "", "", nil
	}
	var blocks []displayBlock
	if json.Unmarshal(trimmed, &blocks) != nil {
		return "", "", nil
	}
	var texts, thoughts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "thinking":
			thoughts = append(thoughts, b.Thinking)
		case "toolCall":
			input := ""
			if args := bytes.TrimSpace(b.Arguments); len(args) > 0 && string(args) != "null" {
				input = string(args)
			}
			calls = append(calls, convo.Tool{CallID: b.ID, Name: b.Name, Input: input})
		}
	}
	return convo.JoinNonEmpty(texts, "\n"), convo.JoinNonEmpty(thoughts, "\n"), calls
}
