package claude

import (
	"bytes"
	"encoding/json"
	"strings"
)

// block is one content block of an assistant message ({type:"text",text:…},
// {type:"tool_use",…}, …). We only extract the text blocks.
type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// AssistantText returns the concatenated text blocks of an assistant line,
// or "" if the line isn't an assistant message or carries no text (e.g. a
// pure tool_use line). message.content is normally an array of blocks but
// can be a bare JSON string.
func (l Line) AssistantText() string {
	if l.Type != "assistant" || l.Message == nil {
		return ""
	}
	c := bytes.TrimSpace(l.Message.Content)
	if len(c) == 0 {
		return ""
	}
	if c[0] == '"' { // a bare string
		var s string
		if json.Unmarshal(c, &s) == nil {
			return s
		}
		return ""
	}
	var blocks []block
	if json.Unmarshal(c, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// LastReply returns the text of the last assistant message that carries text,
// plus the count of such assistant-text messages in the transcript. The count
// is a monotone marker the two-way send path uses to detect that a NEW reply
// landed after `tmux send-keys` (exp14, the attached path) — distinguishing a
// fresh turn's answer from the previous turn's last message.
func LastReply(path string) (text string, count int, err error) {
	r := NewOffsetReader(path)
	lines, err := r.ReadNew()
	if err != nil {
		return "", 0, err
	}
	for _, l := range lines {
		if t := l.AssistantText(); t != "" {
			count++
			text = t
		}
	}
	return text, count, nil
}
