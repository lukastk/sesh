package agents

import "testing"

func TestParseClaudeInitModels(t *testing.T) {
	stream := `{"type":"system","subtype":"noise"}
{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{"models":[{"value":"wrong"}]}}}
{"type":"control_response","response":{"subtype":"success","request_id":"sesh-models","response":{"models":[` +
		`{"value":"default","resolvedModel":"claude-sonnet-5-5","displayName":"Default (recommended)"},` +
		`{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus","description":"Opus 5.5 · everyday"},` +
		`{"value":"claude-fable-5-1[1m]","resolvedModel":"claude-fable-5-1","displayName":"Fable"},` +
		`{"value":"claude-haiku-5-5","resolvedModel":"claude-haiku-5-5","displayName":"Haiku"}]}}}
`
	got, err := parseClaudeInitModels([]byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got.HarnessDefault != "claude-sonnet-5-5" {
		t.Errorf("harness default = %q", got.HarnessDefault)
	}
	want := []ModelOption{
		{ID: "opus", Label: "Opus", Description: "Opus 5.5 · everyday", Resolved: "claude-opus-5-5"},
		{ID: "claude-fable-5-1[1m]", Label: "Fable", Resolved: "claude-fable-5-1"},
		{ID: "claude-haiku-5-5", Label: "Haiku"},
	}
	if len(got.Models) != len(want) {
		t.Fatalf("models = %+v", got.Models)
	}
	for i := range want {
		if got.Models[i] != want[i] {
			t.Errorf("model %d = %+v, want %+v", i, got.Models[i], want[i])
		}
	}
	if _, err := parseClaudeInitModels([]byte(`{"type":"control_response","response":{"subtype":"error","request_id":"sesh-models","error":"nope"}}`)); err == nil {
		t.Error("a refused initialize parsed as success")
	}
	if _, err := parseClaudeInitModels([]byte(`{"type":"result"}`)); err == nil {
		t.Error("output without the control_response parsed as success")
	}
}

func TestParseCodexModels(t *testing.T) {
	got, err := parseCodexModels([]byte(`{"models":[
		{"slug":"gpt-6-astra","display_name":"GPT-6-Astra","description":"d","visibility":"list"},
		{"slug":"gpt-reserve","visibility":"hide"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0] != (ModelOption{ID: "gpt-6-astra", Label: "GPT-6-Astra", Description: "d"}) {
		t.Fatalf("models = %+v", got.Models)
	}
	if _, err := parseCodexModels([]byte(`{"catalog":[]}`)); err == nil {
		t.Error("a catalog without a models array parsed as success")
	}
}

func TestParsePiModels(t *testing.T) {
	raw := "provider      model                 context  max-out  thinking  images\n" +
		"anthropic     claude-haiku-4-5      200K     64K      yes       yes\n" +
		"openrouter    qwen/qwen3.6-plus     1M       64K      yes       no\n"
	got, err := parsePiModels([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 || got.Models[0].ID != "anthropic/claude-haiku-4-5" || got.Models[1].ID != "openrouter/qwen/qwen3.6-plus" {
		t.Fatalf("models = %+v", got.Models)
	}
	if _, err := parsePiModels([]byte("No models available.\n")); err == nil {
		t.Error("output without the table header parsed as success")
	}
}
