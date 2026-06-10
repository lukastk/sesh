package pi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
)

// pi JSONL schema (version 3, re-verified against live pi 0.78.0):
//   - first line: {type:"session", id, cwd, timestamp, version}
//   - {type:"message", id, parentId, timestamp, message:{role, ...}}
//       role=user       : content
//       role=assistant  : stopReason, content, model, usage, ...
//       role=toolResult : toolCallId, toolName, content, isError
//   - {type:"model_change"|"thinking_level_change"|...}
//
// This is the SECOND busy/idle pathway: the pi-rpc socket only broadcasts
// events for socket-initiated turns (exp03 §2.2), so a turn the user TYPED
// in the TUI is invisible there. The JSONL watch covers it (v1 exp20 §Q3).

type TurnStatus string

const (
	Busy    TurnStatus = "busy"
	Idle    TurnStatus = "idle"
	Unknown TurnStatus = "unknown"
)

type Line struct {
	Type      string `json:"type"` // session | message | model_change | ...
	ID        string `json:"id"`
	Cwd       string `json:"cwd"` // only on the session line
	Message   *struct {
		Role       string `json:"role"`       // user | assistant | toolResult
		StopReason string `json:"stopReason"` // toolUse | stop | aborted | error
	} `json:"message"`
}

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

// FirstRecordCwd reads the authoritative cwd from the first ("session")
// record — the dir name is lossy, so this is the only reliable source.
func FirstRecordCwd(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if sc.Scan() {
		l, ok, err := ParseLine(sc.Bytes())
		if err != nil {
			return "", err
		}
		if ok {
			return l.Cwd, nil
		}
	}
	return "", sc.Err()
}

// DeriveTurnStatus computes busy/idle from pi JSONL lines.
//
//   - user message            -> turn start (busy)
//   - assistant stopReason "toolUse" -> mid-turn (busy)
//   - assistant stopReason "stop"/"aborted"/"error" -> turn end (idle)
//   - toolResult              -> mid-turn (leaves status unchanged)
//
// Status is whatever the last meaningful line implies. Empty -> Unknown.
func DeriveTurnStatus(lines []Line) TurnStatus {
	status := Unknown
	for _, l := range lines {
		if l.Type != "message" || l.Message == nil {
			continue
		}
		switch l.Message.Role {
		case "user":
			status = Busy
		case "assistant":
			switch l.Message.StopReason {
			case "toolUse":
				status = Busy
			case "stop", "aborted", "error":
				status = Idle
			}
		case "toolResult":
			// mid-turn; status stays as-is (still busy)
		}
	}
	return status
}

// OffsetReader: incremental offset-based reads (same shape as exp05's).
type OffsetReader struct {
	Path   string
	offset int64
}

func NewOffsetReader(path string) *OffsetReader { return &OffsetReader{Path: path} }

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
		r.offset = 0
	}
	if _, err := f.Seek(r.offset, 0); err != nil {
		return nil, err
	}
	var lines []Line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var consumed int64
	for sc.Scan() {
		raw := sc.Bytes()
		l, ok, perr := ParseLine(raw)
		if perr != nil {
			break // partial write; re-read next time
		}
		consumed += int64(len(raw)) + 1
		if ok {
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		return lines, err
	}
	if no := r.offset + consumed; no <= fi.Size() {
		r.offset = no
	} else {
		r.offset = fi.Size()
	}
	return lines, nil
}
