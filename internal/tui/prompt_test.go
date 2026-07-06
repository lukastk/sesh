package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// TestPromptInPlaceEditing drives the line-prompt with ←/→ + Home/End + insert +
// Backspace/Delete and asserts the value and cursor track an in-place edit — the
// rename-cursor affordance (you can fix a typo in the middle without retyping).
func TestPromptInPlaceEditing(t *testing.T) {
	key := func(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	// Start as `r` would: prefilled name, cursor at the end.
	m := Model{prompting: promptRename, promptInput: []rune("feature"), promptCursor: len("feature")}

	step := func(k tea.KeyMsg) { mm, _ := m.handlePromptKey(k); m = mm.(Model) }

	// Move left 3 (cursor between 'a' and 't' in "feature" -> index 4).
	step(key(tea.KeyLeft))
	step(key(tea.KeyLeft))
	step(key(tea.KeyLeft))
	if m.promptCursor != 4 {
		t.Fatalf("after 3×left cursor = %d, want 4", m.promptCursor)
	}
	// Insert "XY" at the cursor: "featXYure".
	step(runes("XY"))
	if string(m.promptInput) != "featXYure" || m.promptCursor != 6 {
		t.Fatalf("after insert: %q cursor=%d, want featXYure cursor=6", string(m.promptInput), m.promptCursor)
	}
	// Backspace removes 'Y' (before the cursor): "featXure".
	step(key(tea.KeyBackspace))
	if string(m.promptInput) != "featXure" || m.promptCursor != 5 {
		t.Fatalf("after backspace: %q cursor=%d, want featXure cursor=5", string(m.promptInput), m.promptCursor)
	}
	// Home, then Delete removes the leading 'f': "eatXure".
	step(key(tea.KeyHome))
	step(key(tea.KeyDelete))
	if string(m.promptInput) != "eatXure" || m.promptCursor != 0 {
		t.Fatalf("after home+delete: %q cursor=%d, want eatXure cursor=0", string(m.promptInput), m.promptCursor)
	}
	// End jumps past the last rune; right at the end is a no-op.
	step(key(tea.KeyEnd))
	step(key(tea.KeyRight))
	if m.promptCursor != len("eatXure") {
		t.Fatalf("after end: cursor = %d, want %d", m.promptCursor, len("eatXure"))
	}

	// The cursor renders at its position (a block before the char it sits on).
	if got := renderPromptInput([]rune("ab"), 1); got != "a█b" {
		t.Errorf("renderPromptInput mid = %q, want a█b", got)
	}
	if got := renderPromptInput([]rune("ab"), 2); got != "ab█" {
		t.Errorf("renderPromptInput end = %q, want ab█", got)
	}
}

// TestRenameToEmptyIsNotCancel: submitting the rename prompt EMPTY renames to empty
// (a nameless thread / a bare-rule divider) — it must run a rename command, not no-op
// like a cancel. (Esc is the cancel; regression: empty used to silently no-op, so you
// couldn't clear a divider's label.)
func TestRenameToEmptyIsNotCancel(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "d1", Name: "today", Machine: "mymain", AgentKind: api.DividerAgentKind, PinOrder: fptr(1)}}
	m := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{row},
		machines: selfMachines(),
		// As `r` sets it up, then cleared: prompting a rename with an EMPTY value.
		prompting: promptRename, promptRow: row, promptInput: nil, promptCursor: 0,
	}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := nm.(Model)
	if got.prompting != promptNone {
		t.Fatalf("submit did not close the prompt")
	}
	if cmd == nil {
		t.Fatalf("empty rename submit must run a rename command (clear the name), not no-op like a cancel")
	}
}
