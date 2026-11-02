package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// The OBSERVED model is the model a conversation's transcript says it last ran
// on — read from the harness's own records, never stored by sesh and never
// guessed. It differs from a thread's registered model whenever the agent was
// switched in-session (claude /model, codex /model, pi's model picker) or the
// thread never registered one.
//
//   - claude: `message.model` on assistant entries (the resolved id, e.g.
//     claude-opus-5-5; the context-window suffix such as [1m] is not recorded).
//     The `<synthetic>` placeholder claude writes for some local entries is not
//     a model and is skipped.
//   - codex: `payload.model` of `turn_context` entries, written once per turn.
//   - pi/mypi: a `model_change` entry (written at the moment of a switch) or an
//     assistant message's provider + model, rendered `provider/id`.
//
// A transcript with no model-bearing entry yet (no turn taken) has no observed
// model: found=false, which callers show as nothing.

// modelOf extracts the model a single transcript line records, if any.
func modelOf(k Kind, line []byte) (string, bool) {
	if !bytes.Contains(line, []byte(`"model`)) {
		return "", false // cheap pre-filter: most lines carry no model at all
	}
	switch k {
	case Claude:
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message.Model == "" || e.Message.Model == "<synthetic>" {
			return "", false
		}
		return e.Message.Model, true
	case Codex:
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "turn_context" || e.Payload.Model == "" {
			return "", false
		}
		return e.Payload.Model, true
	case Pi, MyPi:
		var e struct {
			Type     string `json:"type"`
			Provider string `json:"provider"`
			ModelID  string `json:"modelId"`
			Message  struct {
				Role     string `json:"role"`
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil {
			return "", false
		}
		if e.Type == "model_change" && e.Provider != "" && e.ModelID != "" {
			return e.Provider + "/" + e.ModelID, true
		}
		if e.Message.Role == "assistant" && e.Message.Provider != "" && e.Message.Model != "" {
			return e.Message.Provider + "/" + e.Message.Model, true
		}
	}
	return "", false
}

// ModelScan is the result of reading a transcript for its observed model.
type ModelScan struct {
	Model string
	Found bool
	// Scanned is the offset just past the last COMPLETE line read: pass it back
	// as `since` to read only what was appended after it.
	Scanned int64
}

// LastModel reads path for the newest model it records.
//
// since > 0 is an incremental read: only bytes from since onward are scanned
// (forward), for a transcript that has only GROWN since a previous scan; Found
// then reports whether the appended part names a model, and the caller keeps
// its previous answer when it does not. since == 0 is a full read, scanning
// backwards from the end so a large transcript stops at its newest model
// instead of being read whole.
//
// A trailing partial line (the agent mid-write) is left unread; Scanned stops
// before it so the next incremental read picks it up whole.
func LastModel(k Kind, path string, since int64) (ModelScan, error) {
	switch k {
	case Claude, Codex, Pi, MyPi:
	default:
		return ModelScan{}, fmt.Errorf("observed model: unknown agent %q", k)
	}
	f, err := os.Open(path)
	if err != nil {
		return ModelScan{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ModelScan{}, err
	}
	size := fi.Size()
	if since > size {
		return ModelScan{}, fmt.Errorf("observed model: %s shrank below the previous scan (%d < %d); rescan it whole", path, size, since)
	}
	if since > 0 {
		return scanForward(k, f, since, size)
	}
	return scanBackward(k, f, size)
}

func scanForward(k Kind, f *os.File, since, size int64) (ModelScan, error) {
	buf := make([]byte, size-since)
	if _, err := f.ReadAt(buf, since); err != nil && err != io.EOF {
		return ModelScan{}, err
	}
	end := bytes.LastIndexByte(buf, '\n')
	out := ModelScan{Scanned: since}
	if end < 0 {
		return out, nil // nothing complete appended yet
	}
	out.Scanned = since + int64(end) + 1
	for _, line := range bytes.Split(buf[:end], []byte{'\n'}) {
		if m, ok := modelOf(k, line); ok {
			out.Model, out.Found = m, true
		}
	}
	return out, nil
}

// backwardChunk is how much of the file is read per step from the end.
const backwardChunk = 256 << 10

func scanBackward(k Kind, f *os.File, size int64) (ModelScan, error) {
	out := ModelScan{}
	// The scan boundary: everything up to the last newline is complete.
	var carry []byte // the partial line straddling the previous chunk's start
	first := true
	for pos := size; pos > 0; {
		start := pos - backwardChunk
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, pos-start)
		if _, err := f.ReadAt(chunk, start); err != nil && err != io.EOF {
			return ModelScan{}, err
		}
		data := append(chunk, carry...)
		if first {
			// Drop a trailing partial line and fix the incremental boundary.
			nl := bytes.LastIndexByte(data, '\n')
			if nl < 0 {
				if start == 0 {
					return out, nil // no complete line in the whole file
				}
				carry, pos = data, start
				continue
			}
			out.Scanned = start + int64(nl) + 1
			data = data[:nl]
			first = false
		}
		// Lines in this window, newest first. The first piece may be a fragment of
		// a line that began in an earlier chunk — it is carried, not parsed, unless
		// this window reaches the file's start.
		lines := bytes.Split(data, []byte{'\n'})
		lo := 1
		if start == 0 {
			lo = 0
		}
		for i := len(lines) - 1; i >= lo; i-- {
			if m, ok := modelOf(k, lines[i]); ok {
				out.Model, out.Found = m, true
				return out, nil
			}
		}
		if start == 0 {
			break
		}
		carry = append([]byte(nil), lines[0]...)
		pos = start
	}
	return out, nil
}
