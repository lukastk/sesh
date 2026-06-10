package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
)

// TurnStatus is the busy/idle state derived from the transcript.
type TurnStatus string

const (
	Busy    TurnStatus = "busy"
	Idle    TurnStatus = "idle"
	Unknown TurnStatus = "unknown"
)

// Line is the subset of a transcript JSONL line we care about. The real
// lines have many more fields (re-verified against Claude Code 2.1.158);
// json.Unmarshal ignores the rest.
type Line struct {
	Type      string `json:"type"`    // user | assistant | system | ...
	Subtype   string `json:"subtype"` // turn_duration | compact_boundary | ...
	SessionID string `json:"sessionId"`
	Message   *struct {
		Role       string          `json:"role"`
		StopReason string          `json:"stop_reason"` // end_turn | tool_use | stop_sequence
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

// ParseLine parses one JSONL line. Returns ok=false for blank lines.
func ParseLine(b []byte) (Line, bool, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return Line{}, false, nil
	}
	var l Line
	if err := json.Unmarshal(b, &l); err != nil {
		return Line{}, false, err
	}
	return l, true, nil
}

// isRealUserPrompt reports whether a user line is a human turn-start
// rather than a tool_result delivery. Tool results arrive as user lines
// whose content is an array of tool_result blocks; a real prompt is a
// string or a text block. Heuristic: content that is a JSON string, or an
// array NOT containing "tool_result", is a real prompt.
func (l Line) isRealUserPrompt() bool {
	if l.Type != "user" || l.Message == nil {
		return false
	}
	c := bytes.TrimSpace(l.Message.Content)
	if len(c) == 0 {
		return false
	}
	if c[0] == '"' { // a string => typed prompt
		return true
	}
	// array/object: it's a tool_result if it mentions one.
	return !bytes.Contains(c, []byte(`"tool_result"`))
}

// DeriveTurnStatus computes busy/idle from a transcript's lines.
//
// Markers (re-verified against live data):
//   - system.subtype == "turn_duration"  -> a TUI turn just completed (idle)
//   - assistant message stop_reason == "end_turn"/"stop_sequence" -> done (idle)
//   - assistant message stop_reason == "tool_use" -> mid-turn (busy)
//   - a real user prompt with no following completion -> busy
//
// The status is whatever the LAST meaningful line implies. Meta/title/
// snapshot lines are ignored. Empty transcript -> Unknown.
func DeriveTurnStatus(lines []Line) TurnStatus {
	status := Unknown
	for _, l := range lines {
		switch {
		case l.Type == "system" && l.Subtype == "turn_duration":
			status = Idle
		case l.Type == "assistant" && l.Message != nil:
			switch l.Message.StopReason {
			case "end_turn", "stop_sequence":
				status = Idle
			case "tool_use":
				status = Busy
			}
		case l.isRealUserPrompt():
			status = Busy
		}
	}
	return status
}

// OffsetReader does incremental, offset-based reads of a growing
// transcript (the pattern paired with the exp04 directory watcher).
// It re-opens the path each call, so it survives the atomic-rename that
// replaces sessions transcripts: if the file shrank below the last
// offset (truncated or replaced), it resets and re-reads from the start.
type OffsetReader struct {
	Path   string
	offset int64
}

func NewOffsetReader(path string) *OffsetReader { return &OffsetReader{Path: path} }

// ReadNew returns the complete lines appended since the last call. A
// partial trailing line (no newline yet) is NOT returned and the offset
// stops before it, so it's re-read whole next time.
func (r *OffsetReader) ReadNew() ([]Line, error) {
	f, err := os.Open(r.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < r.offset {
		// File was truncated or atomically replaced — start over.
		r.offset = 0
	}
	if _, err := f.Seek(r.offset, 0); err != nil {
		return nil, err
	}

	var lines []Line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024) // large lines (attachments)
	var consumed int64
	for sc.Scan() {
		raw := sc.Bytes()
		// advance by line length + newline
		adv := int64(len(raw)) + 1
		l, ok, perr := ParseLine(raw)
		if perr != nil {
			// A malformed line is likely a partial write; stop here and
			// re-read from this offset next time.
			break
		}
		consumed += adv
		if ok {
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		return lines, err
	}
	// Only advance past complete lines. If the file doesn't end in a
	// newline, the scanner still returned the last (partial) line; guard
	// by not advancing past EOF beyond what we consumed.
	newOffset := r.offset + consumed
	if newOffset > fi.Size() {
		newOffset = fi.Size()
	}
	r.offset = newOffset
	return lines, nil
}
