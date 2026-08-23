package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// typePalette feeds a query into an open palette one rune at a time, the way a
// terminal delivers it.
func typePalette(t *testing.T, m Model, q string) Model {
	t.Helper()
	for _, r := range q {
		mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm.(Model)
	}
	return m
}

func paletteRow(id, name, machine string) api.ThreadRow {
	return api.ThreadRow{Thread: api.Thread{ID: id, Name: name, Machine: machine, AgentKind: "pi"}}
}

// `p` opens the palette, typing narrows it, and Enter runs the highlighted command.
// The point of the feature: a command with NO key is reachable in a few keystrokes.
func TestPaletteOpensFiltersAndRuns(t *testing.T) {
	m := Model{machine: "mymain", rows: []api.ThreadRow{paletteRow("t1", "one", "mymain")}, machines: selfMachines()}
	mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m = mm.(Model)
	if !m.PaletteOpen() {
		t.Fatalf("p did not open the palette")
	}
	// Unfiltered, the palette lists EVERY command in registry order.
	if got := m.PaletteCommands(); len(got) != len(Commands()) {
		t.Fatalf("unfiltered palette listed %d of %d commands", len(got), len(Commands()))
	}

	// "divider" reaches new-divider — a command with no key at all.
	m = typePalette(t, m, "divider")
	if got := m.PaletteCommands(); len(got) == 0 || got[0] != "new-divider" {
		t.Fatalf("query %q should rank new-divider first, got %v", m.PaletteQuery(), got)
	}
	mm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	if m.PaletteOpen() {
		t.Errorf("enter should close the palette")
	}
	if m.prompting != promptNewDivider {
		t.Errorf("running new-divider from the palette should open its prompt, got %v", m.prompting)
	}
}

// Backspace edits the query, ↑/↓ move the selection, and esc cancels without
// running anything.
func TestPaletteEditMoveCancel(t *testing.T) {
	m := Model{machine: "mymain", rows: []api.ThreadRow{paletteRow("t1", "one", "mymain")}, machines: selfMachines()}
	m.openPalette()
	m = typePalette(t, m, "forkx")
	if got := m.PaletteCommands(); len(got) != 0 {
		t.Fatalf("query %q should match nothing, got %v", m.PaletteQuery(), got)
	}
	// A no-match Enter must NOT close the palette — silently dismissing on a
	// keystroke that did nothing is exactly the confusing behaviour to avoid.
	mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	if !m.PaletteOpen() || cmd != nil {
		t.Errorf("enter with no match should keep the palette open and run nothing")
	}
	mm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = mm.(Model)
	if m.PaletteQuery() != "fork" {
		t.Fatalf("backspace should trim the query, got %q", m.PaletteQuery())
	}
	if got := m.PaletteCommands(); len(got) == 0 || got[0] != "fork" {
		t.Fatalf("query %q should rank fork first, got %v", m.PaletteQuery(), got)
	}

	// ↓ then ↑ returns to the top entry; esc cancels with nothing run.
	for _, k := range []tea.KeyType{tea.KeyDown, tea.KeyUp} {
		mm, _ = m.handleKey(tea.KeyMsg{Type: k})
		m = mm.(Model)
	}
	if m.paletteCursor != 0 {
		t.Errorf("↓ then ↑ should return to the first entry, got %d", m.paletteCursor)
	}
	mm, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(Model)
	if m.PaletteOpen() || cmd != nil {
		t.Errorf("esc should close the palette and run nothing")
	}
	if m.prompting != promptNone || m.pendingFor("t1") != nil {
		t.Errorf("esc must not have run the highlighted command")
	}
}

// A palette-invoked command goes through the SAME offline-owner gate as a key
// press — the palette must not be a way around it.
func TestPaletteRespectsOfflineGate(t *testing.T) {
	m := Model{
		machine: "mymain",
		rows:    []api.ThreadRow{paletteRow("t1", "stuck", "macstudio")},
		machines: []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macstudio", Reachable: false},
		},
	}
	m.openPalette()
	m = typePalette(t, m, "fork")
	mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := mm.(Model)
	if cmd != nil {
		t.Errorf("fork from the palette on an offline owner returned a cmd (would hang)")
	}
	if got.ActionErr() == nil {
		t.Errorf("fork from the palette on an offline owner must refuse loudly")
	}
}

// The palette renders each command with its CURRENT key, scrolls when the list
// overflows, and takes over View().
func TestPaletteRendersKeysAndScrolls(t *testing.T) {
	km, err := ResolveKeymap([]KeySpec{{Command: "fork", Key: "F"}})
	if err != nil {
		t.Fatalf("ResolveKeymap: %v", err)
	}
	m := Model{keymap: km}
	m.openPalette()
	strip := regexp.MustCompile(`\x1b\[[0-9;]*m`)

	out := strip.ReplaceAllString(m.View(), "")
	if !strings.Contains(out, "sesh — commands") {
		t.Fatalf("View() did not take over for the palette:\n%s", out)
	}
	if !strings.Contains(out, "fork the thread (headless copy)") || !strings.Contains(out, "F") {
		t.Errorf("palette should show fork with its configured key:\n%s", out)
	}

	// Small height: only a window shows, and ▼ counts the rest.
	m.height = 10
	out = strip.ReplaceAllString(m.paletteView(), "")
	if !strings.Contains(out, "▼") {
		t.Fatalf("overflowing palette should show the ▼ indicator:\n%s", out)
	}
	// Walking to the LAST entry scrolls the window (▲ appears, ▼ goes) and never
	// runs anything. One more ↓ wraps back to the top (the grid's fzf --cycle feel).
	for range len(Commands()) - 1 {
		mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
		m = mm.(Model)
	}
	out = strip.ReplaceAllString(m.paletteView(), "")
	if !strings.Contains(out, "▲") || strings.Contains(out, "▼") {
		t.Errorf("palette scrolled to the end should show ▲ and no ▼:\n%s", out)
	}
	if !strings.Contains(out, Commands()[len(Commands())-1].Desc) {
		t.Errorf("the last command should be visible at the end of the scroll:\n%s", out)
	}
	mm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if m2 := mm.(Model); m2.paletteCursor != 0 || m2.paletteOffset != 0 {
		t.Errorf("↓ past the last entry should wrap to the top, got cursor=%d off=%d", m2.paletteCursor, m2.paletteOffset)
	}

	// Every row lands its key in the SAME column, selected or not (the selected row
	// renders through a different path, so this is a real drift risk).
	m.height, m.paletteCursor, m.paletteOffset = 0, 1, 0
	lines := strings.Split(strip.ReplaceAllString(m.paletteView(), ""), "\n")
	first := strings.Index(lines[paletteRowsTop], "↑/k")
	second := strings.Index(lines[paletteRowsTop+1], "↓/j")
	if first < 0 || second < 0 || first != second {
		t.Errorf("key column drifted between the plain row (%d) and the SELECTED row (%d):\n%s",
			first, second, strings.Join(lines[:5], "\n"))
	}
}

// A left click on a palette row RUNS that command; the wheel moves the selection.
// Clicking outside the list is a no-op (the palette stays; esc dismisses).
func TestPaletteMouse(t *testing.T) {
	m := Model{machine: "mymain", rows: []api.ThreadRow{paletteRow("t1", "one", "mymain")}, machines: selfMachines()}
	m.openPalette()
	m = typePalette(t, m, "divider")

	// Wheel down then up returns to the first row.
	for _, b := range []tea.MouseButton{tea.MouseButtonWheelDown, tea.MouseButtonWheelUp} {
		mm, _ := m.Update(tea.MouseMsg{Button: b, Action: tea.MouseActionPress})
		m = mm.(Model)
	}
	if m.paletteCursor != 0 {
		t.Errorf("wheel down+up should return to the first entry, got %d", m.paletteCursor)
	}

	// A click above the list (the title row) does nothing.
	mm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: 0})
	if !mm.(Model).PaletteOpen() {
		t.Errorf("a click outside the list should leave the palette open")
	}

	// A click on the first command row runs it.
	mm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 5, Y: paletteRowsTop})
	got := mm.(Model)
	if got.PaletteOpen() {
		t.Errorf("clicking a palette row should close the palette")
	}
	if got.prompting != promptNewDivider {
		t.Errorf("clicking the new-divider row should open its prompt, got %v", got.prompting)
	}
}

// While the palette is up it OWNS the keyboard and the mouse: a grid key must not
// leak through to the rows underneath (typing "a" into the query must not archive).
func TestPaletteSwallowsGridKeys(t *testing.T) {
	m := Model{machine: "mymain", rows: []api.ThreadRow{paletteRow("t1", "one", "mymain")}, machines: selfMachines()}
	m.openPalette()
	m = typePalette(t, m, "a")
	if m.pendingFor("t1") != nil {
		t.Errorf("`a` typed into the palette query must not archive the selected thread")
	}
	if m.PaletteQuery() != "a" {
		t.Errorf("`a` should have gone into the query, got %q", m.PaletteQuery())
	}
	// A grid click must not select a row underneath either.
	mm, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 5, Y: 40})
	if got := mm.(Model); got.cursor != 0 || !got.PaletteOpen() {
		t.Errorf("a click below the palette list must not reach the grid")
	}
}
