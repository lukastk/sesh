package tui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestResizeClearsScreen: a real size change must return tea.ClearScreen.
//
// bubbletea's renderer repaints only the lines of the NEW frame on a resize; it
// cannot know the PREVIOUS frame was wrapped by the terminal into more physical
// lines than it counted, so a shrink strands the old wide output below the new
// render. In the sidebar (resized by the fullscreen toggle, by travelling
// between master windows, and by slot re-pinning) that stale paint is what
// makes clicking appear broken: rowAtY still maps correctly, but the SCREEN no
// longer matches, so clicks land on rows the user cannot see.
func TestResizeClearsScreen(t *testing.T) {
	want := tea.ClearScreen()
	clears := func(t *testing.T, cmd tea.Cmd) bool {
		t.Helper()
		if cmd == nil {
			return false
		}
		return reflect.DeepEqual(cmd(), want)
	}

	m := flatModel("a", "b", "c")
	// A shrink (the case that strands wrapped output) must wipe the screen.
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 38, Height: 20})
	if !clears(t, cmd) {
		t.Fatalf("shrinking the pane must clear the screen, got %v", cmd)
	}
	if m.width != 38 || m.height != 20 {
		t.Fatalf("resize did not land: %dx%d", m.width, m.height)
	}
	// A grow too (unzoom/zoom both matter).
	_, cmd = step(t, m, tea.WindowSizeMsg{Width: 188, Height: 50})
	if !clears(t, cmd) {
		t.Fatalf("growing the pane must clear the screen, got %v", cmd)
	}
	// A repeat of the SAME size is not a resize — no needless full repaint.
	if _, cmd := step(t, m, tea.WindowSizeMsg{Width: 38, Height: 20}); cmd != nil {
		t.Fatalf("an unchanged size must not clear the screen, got %v", cmd)
	}
}
