package tui

// The shells viewer as a LIST SURFACE: the viewport follows the cursor, the
// click mapping matches what was rendered, the `/` filter narrows it, and a
// re-fan-out keeps the cursor on the SAME session.
//
// The viewport tests are the reported bug: before this, shellsView rendered every
// row unconditionally with no window at all, so on a machine with forty sessions
// the cursor walked off the bottom of the pane and the selected row was no longer
// on screen (an over-tall frame keeps its LAST `height` lines, so the title and
// the top rows are what bubbletea drops).

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// manySessions builds n ghost rows with unique, greppable names.
func manySessions(n int) []sessionRow {
	rows := make([]sessionRow, 0, n)
	for i := range n {
		rows = append(rows, sessionRow{ShellSession: api.ShellSession{
			Machine: "mymain", Name: fmt.Sprintf("sess-%02d", i),
			Path: fmt.Sprintf("/dev/box-%02d", i), Class: api.ShellClassGhost,
			Windows: 1, Panes: 1,
		}})
	}
	return rows
}

func sizedShellModel(h int, rows ...sessionRow) Model {
	m := shellModel(rows...)
	m.width, m.height = 120, h
	return m
}

// press drives one key through the viewer's key handler.
func press(t *testing.T, m Model, key string) Model {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+j":
		msg = tea.KeyMsg{Type: tea.KeyCtrlJ}
	case "ctrl+k":
		msg = tea.KeyMsg{Type: tea.KeyCtrlK}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, _ := m.handleShellKey(msg)
	return next.(Model)
}

func plainLines(s string) []string {
	strip := regexp.MustCompile("\x1b\\[[0-9;]*m")
	out := strings.Split(s, "\n")
	for i, l := range out {
		out[i] = strip.ReplaceAllString(l, "")
	}
	return out
}

// renderedSelection returns the name on the "> " cursor line, "" if the selected
// row is not on screen at all — which is exactly the reported symptom.
func renderedSelection(m Model) string {
	for _, l := range plainLines(m.View()) {
		if strings.HasPrefix(l, "> ") {
			// fields: ">", machine, session, …
			return strings.Fields(l)[2]
		}
	}
	return ""
}

func TestShellsViewportFollowsCursor(t *testing.T) {
	rows := manySessions(40)
	m := sizedShellModel(20, rows...)

	// Walk the cursor well past the bottom of a 20-line pane.
	for range 30 {
		m = press(t, m, "j")
	}
	if m.shellCursor != 30 {
		t.Fatalf("cursor = %d, want 30", m.shellCursor)
	}
	want := "sess-30"
	if got := renderedSelection(m); got != want {
		t.Fatalf("selected row %q is not the one rendered under the cursor (got %q) — the viewport did not follow the selection:\n%s",
			want, got, m.View())
	}
	// The frame must FIT the pane: an over-tall frame loses its top lines.
	if n := len(plainLines(m.View())); n > 20 {
		t.Fatalf("frame is %d lines in a 20-row pane", n)
	}
	// Scrolling back up brings it back into view without stranding the cursor.
	for range 30 {
		m = press(t, m, "k")
	}
	if got := renderedSelection(m); got != "sess-00" {
		t.Fatalf("after scrolling back the cursor row is %q, want sess-00", got)
	}
	if m.shellOffset != 0 {
		t.Fatalf("offset = %d, want the window back at the top", m.shellOffset)
	}
}

func TestShellsHalfPageScroll(t *testing.T) {
	m := sizedShellModel(20, manySessions(40)...)
	before := m.shellOffset
	m = press(t, m, "ctrl+j")
	if m.shellOffset <= before {
		t.Fatalf("ctrl+j did not scroll the window (offset %d -> %d)", before, m.shellOffset)
	}
	// The cursor is pulled into the new window — never left off screen.
	if got := renderedSelection(m); got == "" {
		t.Fatalf("after a half-page scroll the cursor row is not rendered:\n%s", m.View())
	}
	m = press(t, m, "ctrl+k")
	if m.shellOffset != before {
		t.Fatalf("ctrl+k did not scroll back (offset %d, want %d)", m.shellOffset, before)
	}
}

// The click mapping must agree with what was RENDERED, at every scroll position
// and with every combination of chrome — the H41 drift class, which is why the
// geometry is resolved once in shellResolveLayout and read by both.
func TestShellRowAtYMatchesRender(t *testing.T) {
	check := func(t *testing.T, m Model) {
		t.Helper()
		l := m.shellResolveLayout()
		lines := plainLines(m.View())
		for i := l.off; i < l.off+l.avail; i++ {
			name := l.rows[i].Name
			lineY := -1
			for y, ln := range lines {
				if (strings.HasPrefix(ln, "> ") || strings.HasPrefix(ln, "  ")) &&
					strings.Contains(ln, " "+name+" ") {
					lineY = y
					break
				}
			}
			if lineY < 0 {
				t.Fatalf("row %q not found in the render:\n%s", name, m.View())
			}
			got, ok := m.shellRowAtY(lineY)
			if !ok || got != i {
				t.Errorf("shellRowAtY(%d) = (%d,%v), want (%d,true) for %q", lineY, got, ok, i, name)
			}
		}
	}
	t.Run("clean", func(t *testing.T) { check(t, sizedShellModel(24, manySessions(40)...)) })
	t.Run("with-errors-and-note", func(t *testing.T) {
		m := sizedShellModel(24, manySessions(40)...)
		m.shellErrs = []string{"macbook: dial tcp: connection refused", "pocket4: offline"}
		m.shellNote = "killed mymain:scratch"
		check(t, m)
	})
	t.Run("scrolled", func(t *testing.T) {
		m := sizedShellModel(24, manySessions(40)...)
		for range 25 {
			m = press(t, m, "j")
		}
		if m.shellOffset == 0 {
			t.Fatal("expected the window to have scrolled")
		}
		check(t, m)
	})
	t.Run("filtered", func(t *testing.T) {
		m := sizedShellModel(24, manySessions(40)...)
		m = press(t, m, "/")
		m = press(t, m, "1")
		check(t, m)
	})
	t.Run("narrow-pane", func(t *testing.T) {
		m := sizedShellModel(12, manySessions(40)...)
		m.width = 40
		m.shellErrs = []string{"macbook: a very long fan-out failure message that will certainly wrap at forty columns"}
		check(t, m)
	})
}

func TestShellsFilter(t *testing.T) {
	rows := append(manySessions(5),
		sessionRow{ShellSession: api.ShellSession{Machine: "macbook", Name: "appgarden", Path: "/dev/ag", Class: api.ShellClassGhost}})
	m := sizedShellModel(24, rows...)

	m = press(t, m, "/")
	if !m.shellFiltering {
		t.Fatal("/ must open the filter")
	}
	for _, r := range "appg" {
		m = press(t, m, string(r))
	}
	vis := m.visibleSessions()
	if len(vis) != 1 || vis[0].Name != "appgarden" {
		t.Fatalf("filter matched %d rows (%v), want just appgarden", len(vis), vis)
	}
	// A query matches the PATH and the MACHINE too, not only the session name.
	for _, q := range []string{"macbook", "dev/ag"} {
		m2 := sizedShellModel(24, rows...)
		m2.shellQuery = []rune(q)
		if got := m2.visibleSessions(); len(got) != 1 || got[0].Name != "appgarden" {
			t.Fatalf("query %q matched %d rows, want appgarden", q, len(got))
		}
	}

	// While typing, the command keys are QUERY TEXT — an `x` must not open the
	// kill confirmation under a filter.
	m3 := sizedShellModel(24, rows...)
	m3 = press(t, m3, "/")
	m3 = press(t, m3, "x")
	if m3.shellConfirmKill {
		t.Fatal("x while filtering must be typed into the query, not open the kill confirmation")
	}
	if string(m3.shellQuery) != "x" {
		t.Fatalf("query = %q, want %q", string(m3.shellQuery), "x")
	}

	// enter APPLIES (query stays, keys work again); esc clears it.
	m = press(t, m, "enter")
	if m.shellFiltering || string(m.shellQuery) != "appg" {
		t.Fatalf("enter must apply the filter and leave it in force (filtering=%v query=%q)", m.shellFiltering, string(m.shellQuery))
	}
	m = press(t, m, "esc")
	if len(m.shellQuery) != 0 {
		t.Fatalf("esc must clear the query, got %q", string(m.shellQuery))
	}
	if !m.shells {
		t.Fatal("the esc that clears a query must NOT also close the viewer")
	}
	// A second esc, with nothing left to clear, closes it.
	m = press(t, m, "esc")
	if m.shells {
		t.Fatal("esc with no query must close the viewer")
	}
}

// The cursor indexes the FILTERED list, so acting under a filter acts on the row
// the user is looking at.
func TestShellsFilterCursorActsOnVisibleRow(t *testing.T) {
	rows := append(manySessions(5),
		sessionRow{ShellSession: api.ShellSession{Machine: "macbook", Name: "appgarden", Class: api.ShellClassGhost}})
	m := sizedShellModel(24, rows...)
	m.shellQuery = []rune("appg")
	m.shellCursor = 0
	sel, ok := m.selectedSession()
	if !ok || sel.Name != "appgarden" {
		t.Fatalf("selected %v (ok=%v), want appgarden — the cursor must index the filtered list", sel, ok)
	}
}

// A re-fan-out must keep the cursor on the SAME session, not the same index.
func TestShellsCursorAnchoredAcrossReload(t *testing.T) {
	rows := []sessionRow{ghostRow("mymain", "beta"), ghostRow("mymain", "gamma")}
	m := sizedShellModel(24, rows...)
	m = press(t, m, "j") // on gamma
	if sel, _ := m.selectedSession(); sel.Name != "gamma" {
		t.Fatalf("setup: selected %q, want gamma", sel.Name)
	}

	// A session sorting ABOVE the selection appears (someone opened a shell).
	reloaded, _ := m.Update(shellsLoadedMsg{rows: []sessionRow{
		ghostRow("mymain", "alpha"), ghostRow("mymain", "beta"), ghostRow("mymain", "gamma"),
	}})
	m = reloaded.(Model)
	if sel, _ := m.selectedSession(); sel.Name != "gamma" {
		t.Fatalf("after the reload the cursor is on %q — a new row above the selection slid it onto a different session", sel.Name)
	}

	// The selected session is GONE (it was killed): the cursor holds its slot,
	// i.e. lands on the neighbour, rather than chasing a row that no longer exists.
	reloaded, _ = m.Update(shellsLoadedMsg{rows: []sessionRow{
		ghostRow("mymain", "alpha"), ghostRow("mymain", "beta"),
	}})
	m = reloaded.(Model)
	if sel, ok := m.selectedSession(); !ok || sel.Name != "beta" {
		t.Fatalf("after the selected session vanished the cursor is on %q (ok=%v), want beta", sel.Name, ok)
	}
}

func TestShellsMouse(t *testing.T) {
	m := sizedShellModel(24, manySessions(40)...)
	l := m.shellResolveLayout()

	click := func(m Model, y int) (Model, tea.Cmd) {
		next, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 4, Y: y})
		return next.(Model), cmd
	}

	// A click SELECTS the row under the pointer.
	targetY := l.rowsTop + 3
	m2, cmd := click(m, targetY)
	if cmd != nil {
		t.Fatal("a single click must select, not act")
	}
	if m2.shellCursor != l.off+3 {
		t.Fatalf("click at y=%d selected %d, want %d", targetY, m2.shellCursor, l.off+3)
	}

	// A DOUBLE click on the same row enters it (closes the viewer + navs).
	m3, cmd := click(m2, targetY)
	if cmd == nil {
		t.Fatal("a double click must enter the session")
	}
	if m3.shells {
		t.Fatal("entering must close the takeover")
	}

	// A click on the chrome (the title) selects nothing.
	m4, cmd := click(m, 0)
	if cmd != nil || m4.shellCursor != m.shellCursor {
		t.Fatalf("a click on the title must be ignored (cursor %d -> %d)", m.shellCursor, m4.shellCursor)
	}

	// The wheel moves the selection, with the viewport following.
	m5, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m5.(Model).shellCursor != 1 {
		t.Fatalf("wheel down moved the cursor to %d, want 1", m5.(Model).shellCursor)
	}
}

// Two clicks far apart in TIME are two selects, never an enter.
func TestShellsSlowDoubleClickDoesNotEnter(t *testing.T) {
	m := sizedShellModel(24, manySessions(40)...)
	l := m.shellResolveLayout()
	clk := time.Now()
	m.nowFn = func() time.Time { return clk }
	y := l.rowsTop + 2
	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: y})
	m = next.(Model)
	clk = clk.Add(doubleClickWindow * 2)
	m.nowFn = func() time.Time { return clk }
	next, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: y})
	if cmd != nil {
		t.Fatal("two clicks outside the double-click window must not enter")
	}
	if next.(Model).shells != true {
		t.Fatal("the viewer must stay open")
	}
}
