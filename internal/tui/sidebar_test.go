package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSidebarNavDoesNotQuit (issue #8): in SIDEBAR mode a successful nav keeps
// the TUI running (it is a persistent pane beside the thread, not a popup over
// it) and notes what was entered; the normal mode still quits so the popup gets
// out of the way.
func TestSidebarNavDoesNotQuit(t *testing.T) {
	t.Setenv("TMUX", "") // focusSiblingPane must no-op outside tmux (unit context)

	sb := Model{sidebar: true}
	nm, cmd := sb.Update(navDoneMsg{name: "worker"})
	got := nm.(Model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatalf("sidebar nav QUIT the TUI — a persistent pane must stay open")
		}
	}
	if !strings.Contains(got.note, `entered "worker"`) {
		t.Errorf("sidebar nav note %q missing the entered-thread feedback", got.note)
	}

	// Normal (popup) mode: unchanged — a successful nav quits.
	popup := Model{}
	_, cmd = popup.Update(navDoneMsg{})
	if cmd == nil {
		t.Fatalf("popup nav produced no command (expected quit)")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf("popup nav did not quit the TUI")
	}
}

// TestSidebarColumnsPreset pins the --sidebar column preset: NAME only, and it
// resolves cleanly through the normal column machinery.
func TestSidebarColumnsPreset(t *testing.T) {
	cols, err := ResolveColumns(SidebarColumns())
	if err != nil {
		t.Fatalf("sidebar preset does not resolve: %v", err)
	}
	if len(cols) != 1 || cols[0] != ColName {
		t.Fatalf("sidebar preset = %v, want [%s]", cols, ColName)
	}
}
