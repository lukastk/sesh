package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// EVERY full-screen popup must render a frame that FITS the pane height. Each one
// sizes its list to fill the height exactly, so a stray trailing newline makes the
// frame height+1 — bubbletea then drops lines from the TOP, the title disappears
// and every row shifts up one. That is the H70 bug (found there as clicks landing
// one row below the target); the grid has guarded it since, but the popups each
// build their own frame and were NOT covered, so the `?` keymap really was losing
// its title on a full-height pane. This is the shared guard: a new popup added to
// View() without going through its trim fails here.
func TestPopupFramesFitPaneHeight(t *testing.T) {
	rows := make([]api.ThreadRow, 0, 40)
	for i := range 40 {
		rows = append(rows, api.ThreadRow{Thread: api.Thread{
			ID: string(rune('a'+i%26)) + "-id", Name: "thread-" + string(rune('a'+i%26)),
			Machine: "mymain", AgentKind: "pi", Parent: "",
		}})
	}
	base := Model{machine: "mymain", rows: rows, machines: selfMachines()}

	// Heights around the exact-fit boundary: each popup's list is sized from the
	// height, so an off-by-one shows up at every size, not just one.
	for _, h := range []int{8, 12, 20, 30, 47} {
		for _, c := range []struct {
			name  string
			setup func(Model) Model
		}{
			{"help", func(m Model) Model { m.helpPopup = true; return m }},
			{"palette", func(m Model) Model { m.openPalette(); return m }},
			{"parent-picker", func(m Model) Model {
				m.parentPick, m.parentPickRow = true, rows[0]
				return m
			}},
			{"view-picker", func(m Model) Model { m.viewPicker = true; return m }},
			{"shells", func(m Model) Model {
				m.shells, m.shellRows = true, manySessions(40)
				return m
			}},
			{"shells-with-chrome", func(m Model) Model {
				// The states that ADD lines: fan-out errors, a note, an applied
				// filter and the kill confirmation (whose warning WRAPS). Each one
				// has to come out of the row budget, or the frame outgrows the pane
				// and bubbletea drops the title and the top rows.
				m.shells, m.shellRows = true, manySessions(40)
				m.shellErrs = []string{"macbook: dial tcp 100.114.33.83:7878: connect: connection refused"}
				m.shellNote = "killed mymain:scratch"
				m.shellQuery = []rune("sess")
				m.shellConfirmKill = true
				return m
			}},
			{"grid", func(m Model) Model { return m }},
			// The `I` details popup, plain and with every line-adding field
			// present (meta pairs, a long cwd): it scrolls now, so its frame must
			// fit like every other popup's.
			{"details", func(m Model) Model {
				m.detailsPopup, m.detailsRow = true, rows[0]
				return m
			}},
			{"details-with-meta", func(m Model) Model {
				r := rows[0]
				r.Cwd = "/home/lukastk/dev/20260916_72fn54__sesh-scheduling-feature/a/very/deep/path/that/keeps/going"
				r.Tags = []string{"one", "two", "three"}
				r.Meta = map[string]string{"box": "20260916_72fn54", "note": "trunk/thread-notes/x.md", "origin": "cockpit", "ticket": "28f6e77b"}
				m.detailsPopup, m.detailsRow = true, r
				return m
			}},
			{"details-scrolled", func(m Model) Model {
				r := rows[0]
				r.Meta = map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5"}
				m.detailsPopup, m.detailsRow = true, r
				m.detailsOffset = 99 // past the end: the view must clamp, not overflow
				return m
			}},
		} {
			m := base
			m.width, m.height = 100, h
			m = c.setup(m)
			frame := m.View()
			lines := strings.Count(frame, "\n") + 1
			if lines > h {
				t.Errorf("%s at height %d rendered a %d-line frame — bubbletea drops the TOP lines to fit, so the title vanishes and every row shifts up one",
					c.name, h, lines)
			}
			if strings.HasSuffix(frame, "\n") {
				t.Errorf("%s frame ends with a trailing newline (a phantom final line)", c.name)
			}
		}
	}
}

// The popup titles must actually SURVIVE on a full-height pane — the observable
// the height arithmetic exists for. (Before the fix, the `?` and palette titles
// were the lines bubbletea dropped.)
func TestPopupTitlesSurviveFullHeight(t *testing.T) {
	m := Model{machine: "mymain", machines: selfMachines(), width: 100, height: 30}
	for _, c := range []struct {
		setup func(Model) Model
		title string
	}{
		{func(m Model) Model { m.helpPopup = true; return m }, "sesh — keys"},
		{func(m Model) Model { m.openPalette(); return m }, "sesh — commands"},
	} {
		got := c.setup(m).View()
		if !strings.Contains(strings.SplitN(got, "\n", 2)[0], c.title) {
			t.Errorf("popup title %q is not the first rendered line:\n%s", c.title, firstLines(got, 3))
		}
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}

// TestDetailsPopupScrolls: the `I` popup's field list scrolls on a pane too
// short to hold it — the fields move, the indicators say how many are off
// screen each way, the offset clamps at both ends, ^j/^k page, and closing
// resets it. Before this the view rendered every field unconditionally, so a
// short pane lost the TITLE and the first fields (bubbletea keeps the LAST
// `height` lines) — i.e. the id you opened the popup to read.
func TestDetailsPopupScrolls(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{
		ID: "c6627a0d-4789-4bf6-b7b3-ab48b32842de", Name: "deep-thread", Machine: "mymain",
		AgentKind: "claude", Cwd: "/home/lukastk/dev/box", SessionName: "sesh_deep",
		Meta: map[string]string{"box": "20260916_72fn54", "origin": "cockpit"},
	}}
	m := Model{machine: "mymain", width: 100, height: 12, detailsPopup: true, detailsRow: row}
	total := len(m.detailsFields())
	if total < 14 {
		t.Fatalf("fixture has only %d fields — too few to overflow a 12-row pane", total)
	}
	first := m.detailsView()
	if !strings.Contains(first, "thread details · deep-thread") {
		t.Fatalf("title missing:\n%s", first)
	}
	if !strings.Contains(first, "  id ") {
		t.Fatalf("the first field (id) is not on screen at offset 0:\n%s", first)
	}
	if !strings.Contains(first, "▼") {
		t.Fatalf("no ▼ indicator although %d fields cannot fit 12 rows:\n%s", total, first)
	}
	if strings.Contains(first, "▲") {
		t.Fatalf("▲ shown at offset 0:\n%s", first)
	}

	// ↓ scrolls: the id leaves, the indicators flip on.
	mm, _ := m.handleDetailsKey(tea.KeyMsg{Type: tea.KeyDown})
	m = mm.(Model)
	if m.detailsOffset != 1 {
		t.Fatalf("offset after ↓ = %d, want 1", m.detailsOffset)
	}
	scrolled := m.detailsView()
	if strings.Contains(scrolled, "  id ") {
		t.Fatalf("the id field is still rendered after scrolling past it:\n%s", scrolled)
	}
	if !strings.Contains(scrolled, "▲ 1 more") {
		t.Fatalf("▲ 1 more missing after one ↓:\n%s", scrolled)
	}
	if !strings.Contains(scrolled, "thread details · deep-thread") {
		t.Fatalf("title lost while scrolling:\n%s", scrolled)
	}

	// ↑ at the top clamps to 0; ↓ at the bottom clamps to the max.
	mm, _ = m.handleDetailsKey(tea.KeyMsg{Type: tea.KeyUp})
	m = mm.(Model)
	mm, _ = m.handleDetailsKey(tea.KeyMsg{Type: tea.KeyUp})
	m = mm.(Model)
	if m.detailsOffset != 0 {
		t.Fatalf("↑ past the top did not clamp: %d", m.detailsOffset)
	}
	maxOff := m.detailsMaxOffset()
	for range total + 5 {
		mm, _ = m.handleDetailsKey(tea.KeyMsg{Type: tea.KeyDown})
		m = mm.(Model)
	}
	if m.detailsOffset != maxOff {
		t.Fatalf("↓ past the end = %d, want the max %d", m.detailsOffset, maxOff)
	}
	bottom := m.detailsView()
	if strings.Contains(bottom, "▼") {
		t.Fatalf("▼ shown at the bottom:\n%s", bottom)
	}
	if !strings.Contains(bottom, "esc/q to close") {
		t.Fatalf("footer lost at the bottom:\n%s", bottom)
	}
	// The LAST field is reachable (it is what scrolling exists for).
	fields := m.detailsFields()
	if last := fields[len(fields)-1]; !strings.Contains(bottom, last.k) {
		t.Fatalf("the last field %q is not reachable by scrolling:\n%s", last.k, bottom)
	}

	// ^k pages back up, and closing resets the offset.
	mm, _ = m.handleDetailsKey(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = mm.(Model)
	if m.detailsOffset >= maxOff || m.detailsOffset < 0 {
		t.Fatalf("^k did not page up: %d (max %d)", m.detailsOffset, maxOff)
	}
	mm, _ = m.handleDetailsKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(Model)
	if m.detailsPopup || m.detailsOffset != 0 {
		t.Fatalf("esc must close AND reset the offset: popup=%v offset=%d", m.detailsPopup, m.detailsOffset)
	}

	// A pane tall enough for everything shows no indicators and every field.
	tall := Model{machine: "mymain", width: 100, height: 60, detailsPopup: true, detailsRow: row}
	full := tall.detailsView()
	if strings.Contains(full, "▲") || strings.Contains(full, "▼") {
		t.Fatalf("indicators shown although everything fits:\n%s", full)
	}
	for _, f := range tall.detailsFields() {
		if !strings.Contains(full, f.k) {
			t.Fatalf("field %q missing from a tall pane's view:\n%s", f.k, full)
		}
	}
}

// TestDetailsPopupWheel: the wheel scrolls the details popup (and does NOT move
// the grid's selection underneath it).
func TestDetailsPopupWheel(t *testing.T) {
	rows := []api.ThreadRow{
		{Thread: api.Thread{ID: "a-id", Name: "alpha", Machine: "mymain", AgentKind: "pi"}},
		{Thread: api.Thread{ID: "b-id", Name: "beta", Machine: "mymain", AgentKind: "pi"}},
	}
	m := Model{machine: "mymain", rows: rows, machines: selfMachines(), width: 100, height: 12,
		detailsPopup: true, detailsRow: rows[0]}
	wheel := func(m Model, b tea.MouseButton) Model {
		mm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: b})
		return mm.(Model)
	}
	m = wheel(m, tea.MouseButtonWheelDown)
	if m.detailsOffset != 1 {
		t.Fatalf("wheel down did not scroll the popup: offset=%d", m.detailsOffset)
	}
	if m.cursor != 0 {
		t.Fatalf("the wheel moved the grid cursor under the popup: cursor=%d", m.cursor)
	}
	m = wheel(m, tea.MouseButtonWheelUp)
	m = wheel(m, tea.MouseButtonWheelUp)
	if m.detailsOffset != 0 {
		t.Fatalf("wheel up past the top did not clamp: offset=%d", m.detailsOffset)
	}
}
