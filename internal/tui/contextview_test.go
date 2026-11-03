package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

func TestWrapText(t *testing.T) {
	got := wrapText("alpha beta gamma delta\n\n  - indented item that wraps\nabcdefghijklmnop\tx\x1b[31mred", 12)
	want := []string{
		"alpha beta",
		"gamma delta",
		"",
		"  - indented",
		"  item that",
		"  wraps",
		"abcdefghijkl", // a word longer than the line is hard-broken
		"mnop",         // a tab is a space; the next word would overflow
		"x�[31mred",    // the ESC control character is made visible
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapText:\n got %q\nwant %q", got, want)
	}
	for _, l := range got {
		if strings.ContainsRune(l, 0x1b) {
			t.Fatalf("an escape sequence reached the terminal: %q", l)
		}
	}
}

func TestContextColumnCell(t *testing.T) {
	var spec colSpec
	for _, c := range colOrder {
		if c.name == ColContext {
			spec = c
		}
	}
	if spec.cell == nil {
		t.Fatal("no context column")
	}
	if got := spec.cell(nil, api.ThreadRow{Thread: api.Thread{ContextHash: "abc"}}); got != "◆" {
		t.Fatalf("with context: %q", got)
	}
	if got := spec.cell(nil, api.ThreadRow{}); got != "" {
		t.Fatalf("without context: %q", got)
	}
	if _, err := ResolveColumns([]string{"name", ColContext, "tags"}); err != nil {
		t.Fatalf("context must be a valid column: %v", err)
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestContextViewerScrollsAndCloses(t *testing.T) {
	m := New("/tmp/none.sock", false)
	m.width, m.height = 40, 12
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "brief", AgentKind: "claude", ContextHash: "h"}}
	m.contextPopup, m.contextRow, m.contextLoading = true, row, true

	// A fetch for ANOTHER thread (the viewer moved on) is ignored.
	mm, _ := m.applyContextLoaded(contextLoadedMsg{row: api.ThreadRow{Thread: api.Thread{ID: "other"}}, resp: api.ThreadContextResponse{Text: "x"}})
	m = mm.(Model)
	if !m.contextLoading {
		t.Fatal("a stale fetch filled the viewer")
	}
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "line "+string(rune('a'+i%26)))
	}
	mm, _ = m.applyContextLoaded(contextLoadedMsg{row: row, resp: api.ThreadContextResponse{Text: strings.Join(lines, "\n"), Hash: "h"}})
	m = mm.(Model)
	if m.contextLoading || !strings.Contains(m.contextView(), "line a") {
		t.Fatalf("viewer did not show the text:\n%s", m.contextView())
	}
	if m.contextMaxOffset() <= 0 {
		t.Fatal("30 lines in a 12-row pane must scroll")
	}
	// The frame never exceeds the pane (bubbletea would drop the title).
	if n := strings.Count(m.contextView(), "\n") + 1; n > m.height {
		t.Fatalf("frame is %d lines in a %d-line pane", n, m.height)
	}
	mm, _ = m.handleContextKey(key("down"))
	m = mm.(Model)
	if m.contextOffset != 1 {
		t.Fatalf("down: offset %d", m.contextOffset)
	}
	mm, _ = m.handleContextKey(key("G"))
	m = mm.(Model)
	if m.contextOffset != m.contextMaxOffset() || !strings.Contains(m.contextView(), "line d") {
		t.Fatalf("G: offset %d of %d", m.contextOffset, m.contextMaxOffset())
	}
	mm, _ = m.handleContextKey(key("down"))
	m = mm.(Model)
	if m.contextOffset != m.contextMaxOffset() {
		t.Fatal("scrolled past the end")
	}
	mm, _ = m.handleContextKey(key("g"))
	m = mm.(Model)
	if m.contextOffset != 0 {
		t.Fatal("g: not at the top")
	}
	mm, _ = m.handleContextKey(key("esc"))
	m = mm.(Model)
	if m.ContextOpen() {
		t.Fatal("esc did not close the viewer")
	}
}

func TestContextViewerStates(t *testing.T) {
	m := New("/tmp/none.sock", false)
	m.width, m.height = 60, 20
	m.contextPopup, m.contextRow = true, api.ThreadRow{Thread: api.Thread{ID: "t1", AgentKind: "pi"}}
	if !strings.Contains(m.contextView(), "no context") {
		t.Fatalf("empty context:\n%s", m.contextView())
	}
	m.contextErr = errors.New("peer unreachable")
	if !strings.Contains(m.contextView(), "peer unreachable") {
		t.Fatalf("error:\n%s", m.contextView())
	}
	// e is inert while the text could not be loaded (nothing to edit from).
	mm, cmd := m.handleContextKey(key("e"))
	if cmd != nil || !mm.(Model).ContextOpen() {
		t.Fatal("e must not start an edit without a loaded context")
	}
}

func TestContextCommandsRefuseNonAgentThreads(t *testing.T) {
	for _, kind := range []string{api.ShellAgentKind, api.VirtualAgentKind, api.DividerAgentKind} {
		if err := contextCapableRow(api.ThreadRow{Thread: api.Thread{AgentKind: kind}}); err == nil {
			t.Errorf("%s: want refused", kind)
		}
	}
	if err := contextCapableRow(api.ThreadRow{Thread: api.Thread{AgentKind: "codex"}}); err != nil {
		t.Fatal(err)
	}
}
