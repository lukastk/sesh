package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLastModelPerHarness(t *testing.T) {
	cases := []struct {
		kind Kind
		text string
		want string
	}{
		{Claude, `{"type":"user","message":{"content":"set model"}}
{"type":"assistant","message":{"model":"claude-opus-5","content":[]}}
{"type":"assistant","message":{"model":"claude-opus-5-5","content":[]}}
{"type":"assistant","message":{"model":"<synthetic>","content":[]}}
{"type":"system","subtype":"turn_duration"}
`, "claude-opus-5-5"},
		{Codex, `{"type":"session_meta","payload":{"model_provider":"openai"}}
{"type":"turn_context","payload":{"model":"gpt-6-luna"}}
{"type":"turn_context","payload":{"model":"gpt-6-astra"}}
{"type":"event_msg","payload":{"type":"token_count"}}
`, "gpt-6-astra"},
		{Pi, `{"type":"session"}
{"type":"model_change","provider":"anthropic","modelId":"claude-haiku-4-5"}
{"type":"message","message":{"role":"assistant","provider":"anthropic","model":"claude-haiku-4-5"}}
{"type":"model_change","provider":"openai-codex","modelId":"gpt-6-astra"}
`, "openai-codex/gpt-6-astra"},
		{MyPi, `{"type":"message","message":{"role":"assistant","provider":"anthropic","model":"claude-haiku-5-5"}}
`, "anthropic/claude-haiku-5-5"},
	}
	for _, c := range cases {
		got, err := LastModel(c.kind, writeTranscript(t, c.text), 0)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Found || got.Model != c.want {
			t.Errorf("%s: %+v, want %q", c.kind, got, c.want)
		}
		if got.Scanned != int64(len(c.text)) {
			t.Errorf("%s: scanned %d of %d complete bytes", c.kind, got.Scanned, len(c.text))
		}
	}
}

func TestLastModelNoTurnAndPartialLine(t *testing.T) {
	got, err := LastModel(Claude, writeTranscript(t, `{"type":"user","message":{"content":"hi"}}`+"\n"), 0)
	if err != nil || got.Found {
		t.Fatalf("a turnless transcript observed a model: %+v %v", got, err)
	}
	// A trailing line still being written is not read, and Scanned stops before it.
	done := `{"type":"assistant","message":{"model":"claude-haiku-5-5"}}` + "\n"
	p := writeTranscript(t, done+`{"type":"assistant","message":{"model":"claude-opus-5-5"`)
	got, err = LastModel(Claude, p, 0)
	if err != nil || got.Model != "claude-haiku-5-5" || got.Scanned != int64(len(done)) {
		t.Fatalf("partial line: %+v %v", got, err)
	}
}

func TestLastModelIncremental(t *testing.T) {
	base := `{"type":"turn_context","payload":{"model":"gpt-6-luna"}}` + "\n"
	p := writeTranscript(t, base)
	first, err := LastModel(Codex, p, 0)
	if err != nil || first.Model != "gpt-6-luna" {
		t.Fatalf("%+v %v", first, err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"event_msg","payload":{"type":"token_count"}}` + "\n")
	f.Close()
	grown, err := LastModel(Codex, p, first.Scanned)
	if err != nil || grown.Found {
		t.Fatalf("appended lines without a model reported one: %+v %v", grown, err)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"turn_context","payload":{"model":"gpt-6-astra"}}` + "\n")
	f.Close()
	grown, err = LastModel(Codex, p, grown.Scanned)
	if err != nil || grown.Model != "gpt-6-astra" {
		t.Fatalf("appended switch not seen: %+v %v", grown, err)
	}
	if _, err := LastModel(Codex, p, grown.Scanned+1000); err == nil {
		t.Error("a since beyond the file's end was accepted")
	}
}

// The model line straddles the backward reader's chunk boundary and is followed
// by more than a chunk of model-less lines: it must be reassembled, not missed.
// The tail after it is sized EXACTLY so the boundary two chunks from the end
// falls in the middle of the model line.
func TestLastModelAcrossChunks(t *testing.T) {
	model := `{"type":"assistant","message":{"model":"claude-fable-5-1","content":[]}}` + "\n"
	line := func(n int) string { // a model-less JSONL line of exactly n bytes
		return `{"type":"user","message":{"content":"` + strings.Repeat("x", n-len(`{"type":"user","message":{"content":""}}`)-1) + `"}}` + "\n"
	}
	tailLen := 2*backwardChunk - len(model)/2
	var tail strings.Builder
	for tail.Len()+2000 < tailLen {
		tail.WriteString(line(1000))
	}
	tail.WriteString(line(tailLen - tail.Len()))
	if tail.Len() != tailLen {
		t.Fatalf("tail is %d bytes, want %d", tail.Len(), tailLen)
	}
	text := line(500) + model + tail.String()
	boundary := len(text) - 2*backwardChunk
	if start := len(line(500)); !(boundary > start && boundary < start+len(model)) {
		t.Fatalf("boundary %d does not split the model line at [%d,%d)", boundary, start, start+len(model))
	}
	got, err := LastModel(Claude, writeTranscript(t, text), 0)
	if err != nil || got.Model != "claude-fable-5-1" {
		t.Fatalf("%+v %v", got, err)
	}
}
