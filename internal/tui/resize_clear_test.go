package tui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestResizeClearsScreen: a real size change must return tea.ClearScreen.
//
// bubbletea repaints only the lines of the new frame on a resize; it has no way
// to know the previous, WIDER frame was wrapped by the terminal into more
// physical lines than it counted, so those lines are never erased and a shrink
// strands the old output around the new render. In the sidebar (resized by the
// fullscreen toggle and by travelling between master windows) that shows up as
// whole thread rows drawn as several wrapped lines each, below the correct list.
func TestResizeClearsScreen(t *testing.T) {
	want := tea.ClearScreen()
	clears := func(t *testing.T, cmd tea.Cmd) bool {
		t.Helper()
		return cmd != nil && reflect.DeepEqual(cmd(), want)
	}

	m := flatModel("a", "b", "c")
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 38, Height: 20}) // shrink
	if !clears(t, cmd) {
		t.Fatalf("shrinking the pane must clear the screen, got %v", cmd)
	}
	if m.width != 38 || m.height != 20 {
		t.Fatalf("resize did not land: %dx%d", m.width, m.height)
	}
	if _, cmd := step(t, m, tea.WindowSizeMsg{Width: 188, Height: 50}); !clears(t, cmd) {
		t.Fatalf("growing the pane must clear the screen, got %v", cmd)
	}
	if _, cmd := step(t, m, tea.WindowSizeMsg{Width: 38, Height: 20}); cmd != nil {
		t.Fatalf("an unchanged size must not clear the screen, got %v", cmd)
	}
}
