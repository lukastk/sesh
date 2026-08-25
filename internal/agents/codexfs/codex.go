// Package codex ports v1's codex integration to Go (CONCEPT.md Q5 /
// exp 07): rollout-file watching + send via `codex exec resume`.
// Re-verified against live codex-cli 0.134.0 on macOS.
//
// Rollout layout: ~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-<isoTs>-<uuid>.jsonl
// Lines are {type, timestamp, payload}. Busy/idle comes from
// event_msg.payload.type markers (v1 exp02 + 18).
package codexfs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// SessionsRoot is codex's rollout root.
func SessionsRoot(home string) string { return filepath.Join(home, "sessions") }

// TurnStatus is the derived busy/idle.
type TurnStatus string

const (
	Busy    TurnStatus = "busy"
	Idle    TurnStatus = "idle"
	Unknown TurnStatus = "unknown"
)

// Line is one rollout JSONL line. payload is decoded lazily by callers
// that need its fields; here we only need payload.type and (on
// session_meta) payload.{id,cwd}.
type Line struct {
	Type    string `json:"type"` // session_meta | event_msg | response_item | turn_context
	Payload struct {
		Type string `json:"type"` // (event_msg) task_started | task_complete | turn_aborted | ...; (response_item) message | reasoning | function_call | ...
		ID   string `json:"id"`   // (session_meta) the session uuid
		Cwd  string `json:"cwd"`  // (session_meta)
		Role string `json:"role"` // (response_item message) user | assistant | developer
		// (response_item message) the message parts. Always a JSON list or null
		// across all 513 live rollouts (2026-07-06 .. 2026-08-25), so a typed
		// slice is safe here — which matters more than usual because
		// OffsetReader.ReadNew BREAKS on the first parse error and silently
		// truncates the rest of the file.
		Content []struct {
			Type string `json:"type"` // output_text (assistant) | input_text (user)
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
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

// DeriveTurnStatus computes busy/idle from rollout lines.
//
// Markers (live-verified across 60 rollouts):
//   - event_msg payload.type "task_started"  -> busy
//   - event_msg payload.type "task_complete" -> idle
//   - event_msg payload.type "turn_aborted"  -> idle
//
// Consistency check observed: #task_started == #task_complete +
// #turn_aborted (every started turn ends in one of the two). All other
// payload types (agent_message, exec_command_end, token_count, …) are
// mid-turn activity and don't change the status.
func DeriveTurnStatus(lines []Line) TurnStatus {
	status := Unknown
	for _, l := range lines {
		if l.Type != "event_msg" {
			continue
		}
		switch l.Payload.Type {
		case "task_started":
			status = Busy
		case "task_complete", "turn_aborted":
			status = Idle
		}
	}
	return status
}

// LastReply returns the text of the conversation's last ASSISTANT message in the
// rollout, plus the number of them. The count is a monotone marker the two-way
// send path uses to detect a NEW reply after `tmux send-keys` (the attached
// path), and it is the subscribe engine's dedup contract.
//
// It reads the `response_item` conversation record — {type:"response_item",
// payload:{type:"message", role:"assistant", content:[{text:…}]}} — which is the
// same shape claude and pi are read through.
//
// IT USED TO READ {type:"event_msg", payload:{type:"agent_message", message:…}}
// AND THAT SHAPE IS GONE: codex stopped emitting it somewhere between 0.146 and
// 0.149.1, so this silently returned ("", 0) on every current rollout — no
// last_reply and no reply count for any codex thread, which is a live
// subscribe/delivery defect and not merely a test failure. (The `codex exec
// --json` STDOUT stream is a DIFFERENT format and still emits item.completed /
// agent_message; parseCodexExec in agents/headless.go reads that one and was
// never affected. Two formats, only one drifted.)
//
// DO NOT "also" match the legacy event_msg/agent_message line, or
// event_msg/item_completed with item.type "AgentMessage", as a compatibility
// measure. Measured against all 513 live rollouts: the 232 legacy-era files
// carry BOTH shapes for the SAME reply, in equal numbers and with identical
// text, so matching more than one DOUBLES the count on every one of them —
// corrupting the dedup marker rather than protecting it. The single
// response_item form covers 512/513 files; the one file it misses contains no
// agent reply at all (an aborted turn), where 0 is the right answer.
func LastReply(path string) (text string, count int, err error) {
	r := NewOffsetReader(path)
	lines, err := r.ReadNew()
	if err != nil {
		return "", 0, err
	}
	for _, l := range lines {
		if l.Type != "response_item" || l.Payload.Type != "message" || l.Payload.Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, c := range l.Payload.Content {
			b.WriteString(c.Text)
		}
		if b.Len() == 0 {
			continue // an assistant turn with no text (tool call only) is not a reply
		}
		count++
		text = b.String()
	}
	return text, count, nil
}

// SessionMeta is the {type:"session_meta"} payload subset.
type SessionMeta struct {
	ID  string
	Cwd string
}

// ReadSessionMeta reads the first line of a rollout and returns its
// session_meta (id + authoritative cwd).
func ReadSessionMeta(path string) (SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if sc.Scan() {
		l, ok, err := ParseLine(sc.Bytes())
		if err != nil {
			return SessionMeta{}, err
		}
		if ok && l.Type == "session_meta" {
			return SessionMeta{ID: l.Payload.ID, Cwd: l.Payload.Cwd}, nil
		}
	}
	return SessionMeta{}, sc.Err()
}

// FindRolloutByID walks the date-partitioned tree and returns the rollout
// path whose filename contains the uuid. The uuid is embedded in the
// filename (rollout-<ts>-<uuid>.jsonl), so this avoids opening files.
// Returns "" if not found.
func FindRolloutByID(home, sessionID string) (string, error) {
	root := SessionsRoot(home)
	var matches []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, sessionID+".jsonl") {
			matches = append(matches, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}

// ResumeArgs builds the argv for sending a prompt to an existing session:
// `codex exec resume <sessionID> <prompt>` (codex-cli 0.134.0).
func ResumeArgs(sessionID, prompt string) []string {
	return []string{"exec", "resume", sessionID, prompt}
}

// Send runs `codex exec resume`. Returns combined output. (Not used by
// the unit tests — they assert ResumeArgs — but provided for the daemon.)
func Send(codexBin, sessionID, prompt string) ([]byte, error) {
	if codexBin == "" {
		codexBin = "codex"
	}
	return exec.Command(codexBin, ResumeArgs(sessionID, prompt)...).CombinedOutput()
}

// OffsetReader: incremental offset-based reads (same shape as exp05/06).
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
			break
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
