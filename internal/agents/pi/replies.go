package pi

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// LastReply returns the text of the last assistant message in a pi session
// plus the count of assistant-text messages (the monotone marker the two-way
// send / subscription-delivery paths use to detect a NEW reply). Mirrors
// claude.LastReply / codex.LastReply. pi assistant content is an array of
// blocks ([{type:"thinking",...},{type:"text","text":...}]); only text blocks
// count.
func LastReply(path string) (text string, count int, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		var l struct {
			Type    string `json:"type"`
			Message *struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.Type != "message" || l.Message == nil || l.Message.Role != "assistant" {
			continue
		}
		if t := replyText(l.Message.Content); t != "" {
			count++
			text = t
		}
	}
	return text, count, sc.Err()
}

// replyText extracts the concatenated text blocks from a pi assistant message's
// content (an array of blocks, or a bare string).
func replyText(content json.RawMessage) string {
	c := strings.TrimSpace(string(content))
	if c == "" {
		return ""
	}
	if c[0] == '"' {
		var s string
		if json.Unmarshal(content, &s) == nil {
			return s
		}
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
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
