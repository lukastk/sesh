package tui

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestModelCell(t *testing.T) {
	row := func(agent, registered, observed string) api.ThreadRow {
		return api.ThreadRow{Thread: api.Thread{AgentKind: agent, Model: registered}, ObservedModel: observed}
	}
	cases := []struct {
		row  api.ThreadRow
		want string
	}{
		{row("claude", "", ""), ""},                                   // unpinned, no turn
		{row("claude", "opus", ""), "(opus)"},                         // registered, not yet seen
		{row("claude", "opus", "claude-opus-5-5"), "claude-opus-5-5"}, // an alias agrees with its family
		{row("claude", "claude-fable-5-1[1m]", "claude-fable-5-1"), "claude-fable-5-1"},
		{row("claude", "opus", "claude-sonnet-5-5"), "claude-sonnet-5-5*"}, // switched in-session
		{row("claude", "", "claude-sonnet-5-5"), "claude-sonnet-5-5"},      // nothing registered to contradict
		{row("codex", "gpt-6-luna", "gpt-6-luna"), "gpt-6-luna"},
		{row("codex", "gpt-6-luna", "gpt-6-astra"), "gpt-6-astra*"},
		{row("pi", "anthropic/claude-haiku-4-5", "anthropic/claude-haiku-4-5"), "claude-haiku-4-5"},
		{row("pi", "anthropic/claude-haiku-4-5:high", "anthropic/claude-haiku-4-5"), "claude-haiku-4-5"},
		{row("pi", "claude-haiku-4-5", "anthropic/claude-haiku-4-5"), "claude-haiku-4-5"},
		{row("pi", "anthropic/claude-haiku-4-5", "openai-codex/gpt-6-astra"), "gpt-6-astra*"},
		{row("mypi", "openrouter/qwen/qwen3.6-plus", ""), "(qwen/qwen3.6-plus)"},
	}
	for _, c := range cases {
		if got := modelCell(c.row); got != c.want {
			t.Errorf("%s registered=%q observed=%q: %q, want %q", c.row.AgentKind, c.row.Model, c.row.ObservedModel, got, c.want)
		}
	}
}
