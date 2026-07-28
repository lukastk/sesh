package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestViewFitsPaneHeight is the guard for the off-by-one that made sidebar
// clicks land on the wrong row (Lukas, 2026-07-28: "often I have to click below
// a row to select it").
//
// bubbletea renders one terminal line per "\n"-separated element and, when a
// frame is taller than the pane, keeps only the LAST height lines
// (newLines[len-height:]) — it drops from the TOP. So an over-tall frame
// silently deletes the title, every row renders one line higher than the model
// believes, and rowAtY (which is otherwise correct) maps every click one row
// off. The frame must therefore never exceed m.height.
//
// The overflow needed BOTH scroll indicators to be showing — i.e. a list
// scrolled to the middle — which is why it never appeared with a short thread
// list and why the earlier rowAtY drift guard, which only compares logical
// lines against each other, could not see it.
func TestViewFitsPaneHeight(t *testing.T) {
	names := make([]string, 40)
	for i := range names {
		names[i] = fmt.Sprintf("row%02d", i)
	}
	check := func(t *testing.T, m Model, what string) {
		t.Helper()
		lines := strings.Split(m.View(), "\n")
		if len(lines) > m.height {
			start, end := m.viewportRange()
			t.Errorf("%s: frame is %d lines in a %d-row pane (over by %d) — bubbletea drops the top %d and every click is off by that much [chrome=%d body=%d viewport=[%d,%d)]",
				what, len(lines), m.height, len(lines)-m.height, len(lines)-m.height,
				m.chromeLines(), m.bodyHeight(), start, end)
		}
	}

	// Scrolled to the MIDDLE: both ▲ and ▼ render, consuming the whole reserve.
	m := flatModel(names...)
	m.width, m.height = 38, 24
	m.cursor = 20
	m.ensureCursorVisible()
	if start, end := m.viewportRange(); start == 0 || end == len(m.visibleMatches()) {
		t.Fatalf("setup: wanted both scroll indicators showing, viewport=[%d,%d) of %d", start, end, len(m.visibleMatches()))
	}
	check(t, m, "scrolled to middle")

	// Top and bottom of the list (one indicator each), and a short list (none).
	top := flatModel(names...)
	top.width, top.height = 38, 24
	check(t, top, "at top")

	bot := flatModel(names...)
	bot.width, bot.height = 38, 24
	bot.cursor = len(names) - 1
	bot.ensureCursorVisible()
	check(t, bot, "at bottom")

	short := flatModel("a", "b", "c")
	short.width, short.height = 38, 24
	check(t, short, "short list")

	// With the chrome the sidebar actually carries (peer footers push the
	// budget), across a range of pane heights.
	for _, h := range []int{12, 18, 24, 30, 52} {
		w := flatModel(names...)
		w.width, w.height = 38, h
		w.machines = []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macstudio", Reachable: true},
			{Machine: "ideapad", Reachable: true},
		}
		w.cursor = 20
		w.ensureCursorVisible()
		check(t, w, fmt.Sprintf("height=%d with peer footers", h))
	}
}
