package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// click builds a left-button PRESS at (x, y).
func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y}
}

func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(msg)
	return nm.(Model), cmd
}

// flatModel is a plain grid of N nameless-but-named top-level threads, sized so nothing
// scrolls. With columns=[NAME] the state gutter is 10 cols wide and NAME starts at X=10;
// the first data row renders at Y=2 (title + column header, no ▲ indicator).
func flatModel(names ...string) Model {
	rows := make([]api.ThreadRow, len(names))
	for i, n := range names {
		rows[i] = api.ThreadRow{Thread: api.Thread{ID: n, Name: n}}
	}
	return Model{columns: []string{ColName}, width: 80, height: 40, scrollDivV: 1, scrollDivH: 1, rows: rows}
}

const firstRowY = 2 // title + column header precede the rows in flatModel

// TestClickSelectsRow: a left click on a row's line moves the selection to it.
func TestClickSelectsRow(t *testing.T) {
	m := flatModel("a", "b", "c", "d")
	m, cmd := step(t, m, click(20, firstRowY+2)) // third row
	if m.Cursor() != 2 {
		t.Fatalf("click should select row 2, cursor=%d", m.Cursor())
	}
	if cmd != nil {
		t.Errorf("a single click should not enter (nil cmd), got %v", cmd)
	}
}

// TestClickOutsideRowsIgnored: clicks above the first row and below the last are no-ops.
func TestClickOutsideRowsIgnored(t *testing.T) {
	m := flatModel("a", "b")
	m.cursor = 1
	for _, y := range []int{0, 1, firstRowY + 5, 100} {
		nm, cmd := step(t, m, click(10, y))
		if nm.Cursor() != 1 {
			t.Errorf("click at y=%d moved the cursor (%d), want unchanged 1", y, nm.Cursor())
		}
		if cmd != nil {
			t.Errorf("click at y=%d returned a cmd, want nil", y)
		}
	}
}

// TestDoubleClickEnters: a second press on the SAME row within the window enters it
// (non-nil nav cmd); one outside the window is just another select (nil cmd).
func TestDoubleClickEnters(t *testing.T) {
	m := flatModel("a", "b", "c")
	clk := time.Unix(1000, 0)
	m.nowFn = func() time.Time { return clk }

	m, cmd := step(t, m, click(20, firstRowY+1)) // first click, row 1
	if m.Cursor() != 1 || cmd != nil {
		t.Fatalf("first click: cursor=%d cmd=%v, want cursor 1 / nil cmd", m.Cursor(), cmd)
	}
	clk = clk.Add(200 * time.Millisecond) // within the window
	m, cmd = step(t, m, click(20, firstRowY+1))
	if cmd == nil {
		t.Fatalf("double click within the window should enter (non-nil cmd)")
	}

	// A second click too LATE is a fresh select, not an enter.
	m2 := flatModel("a", "b", "c")
	clk2 := time.Unix(2000, 0)
	m2.nowFn = func() time.Time { return clk2 }
	m2, _ = step(t, m2, click(20, firstRowY+1))
	clk2 = clk2.Add(2 * time.Second)
	if _, cmd := step(t, m2, click(20, firstRowY+1)); cmd != nil {
		t.Errorf("click after the window should re-select, not enter (cmd=%v)", cmd)
	}
}

// TestDoubleClickDifferentRowsIsSelect: two fast clicks on DIFFERENT rows are two
// selects, never an enter.
func TestDoubleClickDifferentRowsIsSelect(t *testing.T) {
	m := flatModel("a", "b", "c")
	clk := time.Unix(1000, 0)
	m.nowFn = func() time.Time { return clk }
	m, _ = step(t, m, click(20, firstRowY)) // row 0
	clk = clk.Add(100 * time.Millisecond)
	m, cmd := step(t, m, click(20, firstRowY+2)) // row 2 — different row
	if m.Cursor() != 2 {
		t.Errorf("second click should select row 2, cursor=%d", m.Cursor())
	}
	if cmd != nil {
		t.Errorf("clicks on different rows must not enter, got cmd %v", cmd)
	}
}

// TestDoubleClickOfflineRefused: entering an OFFLINE machine's thread by double click is
// refused instantly (loud actionErr, no nav cmd) — the same gate the `enter` key has.
func TestDoubleClickOfflineRefused(t *testing.T) {
	m := flatModel()
	m.rows = []api.ThreadRow{{Thread: api.Thread{ID: "x", Name: "x", Machine: "box"}}}
	m.machines = []api.MachineView{{Machine: "box", Reachable: false}}
	clk := time.Unix(1000, 0)
	m.nowFn = func() time.Time { return clk }
	m, _ = step(t, m, click(20, firstRowY))
	clk = clk.Add(100 * time.Millisecond)
	m, cmd := step(t, m, click(20, firstRowY))
	if cmd != nil {
		t.Errorf("double click on an offline thread should not nav, got cmd %v", cmd)
	}
	if m.ActionErr() == nil {
		t.Errorf("expected a loud offline actionErr")
	}
}

// TestClickFoldMarkerToggles: clicking the ▸/▾ glyph collapses/expands that node's
// subtree; clicking the name (not the marker) selects without toggling.
func TestClickFoldMarkerToggles(t *testing.T) {
	m := Model{columns: []string{ColName}, width: 80, height: 40, scrollDivV: 1, scrollDivH: 1,
		rows: []api.ThreadRow{
			{Thread: api.Thread{ID: "p", Name: "parent"}},
			{Thread: api.Thread{ID: "c", Name: "child", Parent: "p"}},
		}}
	// Collapsed by default: only the parent shows, with a "▸ " marker at X=10.
	if got := len(m.visibleMatches()); got != 1 {
		t.Fatalf("collapsed tree should show 1 row, got %d", got)
	}
	m, _ = step(t, m, click(10, firstRowY)) // the ▸ glyph
	if !m.isExpanded("p") {
		t.Fatalf("clicking the fold marker should expand the node")
	}
	if got := len(m.visibleMatches()); got != 2 {
		t.Errorf("expanded tree should show 2 rows, got %d", got)
	}
	// Click it again → collapse.
	m, _ = step(t, m, click(10, firstRowY))
	if m.isExpanded("p") {
		t.Errorf("clicking the fold marker again should collapse the node")
	}

	// A click on the NAME (well past the marker) selects but does NOT toggle.
	before := m.isExpanded("p")
	m, cmd := step(t, m, click(15, firstRowY))
	if m.isExpanded("p") != before {
		t.Errorf("clicking the name should not toggle the fold")
	}
	if cmd != nil {
		t.Errorf("clicking the name is a select, want nil cmd")
	}
}

// TestClickReleaseAndMotionIgnored: only a PRESS acts; release/motion events (e.g. a
// drag) never move the selection or fire an action.
func TestClickReleaseAndMotionIgnored(t *testing.T) {
	m := flatModel("a", "b", "c")
	m.cursor = 0
	for _, act := range []tea.MouseAction{tea.MouseActionRelease, tea.MouseActionMotion} {
		msg := tea.MouseMsg{Action: act, Button: tea.MouseButtonLeft, X: 20, Y: firstRowY + 2}
		nm, cmd := step(t, m, msg)
		if nm.Cursor() != 0 || cmd != nil {
			t.Errorf("action %v should be ignored (cursor=%d cmd=%v)", act, nm.Cursor(), cmd)
		}
	}
}

// TestClickIgnoredInModal: a click while a popup/prompt owns the screen does not reach
// the grid underneath.
func TestClickIgnoredInModal(t *testing.T) {
	m := flatModel("a", "b", "c")
	m.cursor = 0
	m.prompting = promptRename
	if nm, cmd := step(t, m, click(20, firstRowY+2)); nm.Cursor() != 0 || cmd != nil {
		t.Errorf("click during a prompt should be ignored (cursor=%d cmd=%v)", nm.Cursor(), cmd)
	}
}

// TestRowAtYMatchesRender is the drift guard: rowAtY must map the terminal Y of a
// RENDERED row back to that row's visible index. It renders View(), locates each row's
// actual line, and round-trips the Y — so any change to View's top-chrome layout that
// isn't mirrored in rowsTop fails here (the layout-drift bug class). Also exercises the
// shift an extra chrome line (actionErr) introduces.
func TestRowAtYMatchesRender(t *testing.T) {
	strip := regexp.MustCompile("\x1b\\[[0-9;]*m")
	check := func(t *testing.T, m Model) {
		t.Helper()
		lines := strings.Split(m.View(), "\n")
		vis := m.visibleMatches()
		start, end := m.viewportRange()
		for i := start; i < end; i++ { // only rows actually in the rendered window
			tr := vis[i]
			name := tr.row.Name
			lineY := -1
			for y, ln := range lines {
				plain := strip.ReplaceAllString(ln, "")
				// Match the row's own line (skip the legend, which also lists names never
				// used here). Every data row begins with the "> "/"  " gutter prefix.
				if strings.Contains(plain, " "+name) && (strings.HasPrefix(plain, "> ") || strings.HasPrefix(plain, "  ")) {
					lineY = y
					break
				}
			}
			if lineY < 0 {
				t.Fatalf("row %q not found in render:\n%s", name, m.View())
			}
			got, ok := m.rowAtY(lineY)
			if !ok || got != i {
				t.Errorf("rowAtY(%d) = (%d,%v), want (%d,true) for row %q", lineY, got, ok, i, name)
			}
		}
	}
	// Unique names so the substring match is unambiguous.
	names := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	t.Run("clean", func(t *testing.T) { check(t, flatModel(names...)) })
	t.Run("with-action-error", func(t *testing.T) {
		m := flatModel(names...)
		m.actionErr = fmt.Errorf("boom")
		check(t, m)
	})
	t.Run("scrolled", func(t *testing.T) {
		// A short viewport forces a scroll: put the cursor at the bottom so vOffset > 0
		// and the "▲ N more" indicator appears above the first visible row.
		many := make([]string, 30)
		for i := range many {
			many[i] = fmt.Sprintf("row%02d", i)
		}
		m := flatModel(many...)
		m.height = 16
		m.cursor = 29
		m.ensureCursorVisible()
		if start, _ := m.viewportRange(); start == 0 {
			t.Fatalf("test setup: expected a scrolled viewport (start>0)")
		}
		check(t, m)
	})
}
