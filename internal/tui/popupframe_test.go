package tui

import (
	"strings"
	"testing"

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
			{"grid", func(m Model) Model { return m }},
			// NOT covered: the `I` details popup. It renders a FIXED ~23-line field
			// list with no scrolling at all, so on a short pane it overflows by
			// ~15 lines and bubbletea drops its title and first fields. That is a
			// real pre-existing defect, not one this guard's trailing-newline fix
			// addresses — it needs the scroll treatment helpView/paletteView have.
			// Recorded rather than quietly excluded; see the H-entry.
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
