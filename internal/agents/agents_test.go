package agents

import "testing"

// These tests pin the per-agent --model flag PLACEMENT in the headed/resume
// command builders (the bug class: a model silently dropped or placed where the
// agent ignores it). An empty model must produce today's exact default command.

func TestHeadedCommandModelPlacement(t *testing.T) {
	cases := []struct {
		name      string
		kind      Kind
		sessionID string
		model     string
		want      string
	}{
		// Empty model = byte-identical to the pre-model default behavior.
		{"pi no model", Pi, "sid-1", "", "pi --session-id sid-1"},
		{"claude no model", Claude, "sid-2", "", "claude --session-id sid-2"},
		{"codex no model", Codex, "", "", "codex"},
		// claude/pi: --model goes AFTER the session flag.
		{"pi model", Pi, "sid-1", "anthropic/claude-haiku-4-5", "pi --session-id sid-1 --model 'anthropic/claude-haiku-4-5'"},
		{"claude model", Claude, "sid-2", "haiku", "claude --session-id sid-2 --model 'haiku'"},
		// codex: --model goes on the bare top-level command (no pre-assigned id).
		{"codex model", Codex, "", "gpt-5.5", "codex --model 'gpt-5.5'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HeadedCommand(tc.kind, tc.sessionID, tc.model, "default", nil)
			if got != tc.want {
				t.Errorf("HeadedCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResumeCommandModelPlacement(t *testing.T) {
	cases := []struct {
		name      string
		kind      Kind
		sessionID string
		model     string
		want      string
	}{
		{"pi no model", Pi, "sid-1", "", "pi --session-id sid-1"},
		{"claude no model", Claude, "sid-2", "", "claude --resume sid-2"},
		{"codex no model", Codex, "sid-3", "", "codex resume sid-3"},
		{"pi model", Pi, "sid-1", "anthropic/claude-opus-4-8", "pi --session-id sid-1 --model 'anthropic/claude-opus-4-8'"},
		{"claude model", Claude, "sid-2", "opus", "claude --resume sid-2 --model 'opus'"},
		// codex resume: --model goes after the session id (a `resume` flag).
		{"codex model", Codex, "sid-3", "gpt-5.5", "codex resume sid-3 --model 'gpt-5.5'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResumeCommand(tc.kind, tc.sessionID, tc.model, "default", nil)
			if got != tc.want {
				t.Errorf("ResumeCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

// The model flag sits BEFORE the mode/extra-args suffix, so a yolo spawn with a
// model keeps both (placement regression guard).
func TestModelAndModeCoexist(t *testing.T) {
	got := HeadedCommand(Claude, "sid", "haiku", "yolo", []string{"--foo"})
	want := "claude --session-id sid --model 'haiku' '--dangerously-skip-permissions' '--foo'"
	if got != want {
		t.Errorf("HeadedCommand = %q, want %q", got, want)
	}
}

func TestModelArgs(t *testing.T) {
	if got := modelArgs(""); got != nil {
		t.Errorf("modelArgs(\"\") = %v, want nil (the agent default)", got)
	}
	got := modelArgs("gpt-5.5")
	if len(got) != 2 || got[0] != "--model" || got[1] != "gpt-5.5" {
		t.Errorf("modelArgs = %v, want [--model gpt-5.5]", got)
	}
}

func TestParseCodexError(t *testing.T) {
	// codex writes its failure reason to STDOUT as a JSON error event; the model
	// name must survive into the surfaced message (loud, not a bare exit status).
	out := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"error","message":"The 'gpt-5.5-codex' model is not supported"}
{"type":"turn.failed","error":{"message":"The 'gpt-5.5-codex' model is not supported"}}`
	got := parseCodexError(out)
	if got != "The 'gpt-5.5-codex' model is not supported" {
		t.Errorf("parseCodexError = %q", got)
	}
	if parseCodexError(`{"type":"item.completed"}`) != "" {
		t.Errorf("parseCodexError on a clean stream should be empty")
	}
}
