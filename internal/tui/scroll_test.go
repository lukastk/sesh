package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// TestWheelTick covers the sensitivity accumulator: div=1 steps every notch, a higher
// divisor needs that many notches per step, and reversing direction resets the carry.
func TestWheelTick(t *testing.T) {
	acc := 0
	if got := wheelTick(&acc, 1, 1); got != 1 {
		t.Errorf("div=1 should step on every notch, got %d", got)
	}
	// div=3: only the 3rd notch in one direction steps.
	acc = 0
	steps := 0
	for i := 0; i < 3; i++ {
		steps += wheelTick(&acc, 1, 3)
	}
	if steps != 1 {
		t.Errorf("div=3: three notches should yield exactly one step, got %d", steps)
	}
	// Two of three: no step yet.
	acc = 0
	if wheelTick(&acc, 1, 3)+wheelTick(&acc, 1, 3) != 0 {
		t.Errorf("two of three notches should not step")
	}
	// Direction flip resets the carry, then accumulates the other way.
	acc = 0
	wheelTick(&acc, 1, 3)
	wheelTick(&acc, 1, 3) // acc = 2
	if got := wheelTick(&acc, -1, 3); got != 0 || acc != -1 {
		t.Errorf("flip should reset to 0 then go -1 (got step %d, acc %d)", got, acc)
	}
}

// TestHorizontalViewPartialColumn: when a wide trailing column (NAME) can't fully
// fit, horizontalView still includes it at a CLIPPED width (the reported bug was it
// vanishing entirely), but only when the leftover space is worth rendering into.
func TestHorizontalViewPartialColumn(t *testing.T) {
	cols := make([]colSpec, 4) // only len(cols) matters to the windowing
	widths := []int{4, 9, 6, 30}

	// avail=30: TKT!/MACHINE/AGENT fully fit (used=21), leaving 8 for a partial NAME.
	s, e, w := horizontalView(cols, widths, 0, 30)
	if s != 0 || e != 4 {
		t.Fatalf("expected the 4th column partially included, got [%d:%d]", s, e)
	}
	if w[3] != 8 { // 30 - 21 - 1(separator)
		t.Errorf("partial trailing width = %d, want 8", w[3])
	}
	if total := sumWidths(w); total > 30 {
		t.Errorf("rendered total %d exceeds avail 30", total)
	}

	// avail=23: only 1 column-worth of leftover (< minPartialColW) — NOT worth a
	// sliver, so the column is dropped (the › indicator carries it instead).
	if _, e2, _ := horizontalView(cols, widths, 0, 23); e2 != 3 {
		t.Errorf("tiny leftover should drop the trailing column, end=%d want 3", e2)
	}

	// A single forced column wider than the whole screen is clipped to avail so it
	// never bleeds past the edge.
	if _, _, w3 := horizontalView(make([]colSpec, 2), []int{40, 5}, 0, 10); w3[0] != 10 {
		t.Errorf("forced overflow column should clip to avail, got %d want 10", w3[0])
	}
}

func sumWidths(w []int) int {
	total := 0
	for i, x := range w {
		if i > 0 {
			total++ // separator
		}
		total += x
	}
	return total
}

func mouse(button tea.MouseButton, shift bool) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: button, Shift: shift}
}

// TestMouseWheelSensitivityVertical: with mouse_scroll_v=2 the selection moves one row
// only every SECOND wheel notch.
func TestMouseWheelSensitivityVertical(t *testing.T) {
	m := Model{
		columns: []string{ColName}, width: 80, height: 20, scrollDivV: 2, scrollDivH: 1,
		rows: []api.ThreadRow{
			{Thread: api.Thread{ID: "1", Name: "a"}}, {Thread: api.Thread{ID: "2", Name: "b"}},
			{Thread: api.Thread{ID: "3", Name: "c"}},
		},
	}
	step := func(mm Model) Model { nm, _ := mm.Update(mouse(tea.MouseButtonWheelDown, false)); return nm.(Model) }
	m = step(m)
	if m.Cursor() != 0 {
		t.Errorf("div=2: first notch should NOT move the selection, cursor=%d", m.Cursor())
	}
	m = step(m)
	if m.Cursor() != 1 {
		t.Errorf("div=2: second notch should move the selection one row, cursor=%d", m.Cursor())
	}
}

// TestMouseWheelHorizontalPan: Shift+vertical-wheel pans columns (the reliable path) and
// native wheel-left/right pan too — both honor the horizontal divisor.
func TestMouseWheelHorizontalPan(t *testing.T) {
	// Many columns + a narrow window so the row is clipped (maxHOffset > 0).
	cols := []string{ColMachine, ColAgent, ColName, ColCwd, ColTags, ColCreated}
	m := Model{
		columns: cols, width: 30, height: 20, scrollDivV: 1, scrollDivH: 1,
		rows: []api.ThreadRow{{Thread: api.Thread{ID: "1", Name: "wide", Machine: "m", AgentKind: "pi", Cwd: "/x"}}},
	}
	if m.maxHOffset() == 0 {
		t.Fatalf("setup: expected a horizontally-clipped grid (maxHOffset>0)")
	}
	// Shift+wheel-down pans right.
	nm, _ := m.Update(mouse(tea.MouseButtonWheelDown, true))
	m = nm.(Model)
	if m.HOffset() != 1 {
		t.Errorf("Shift+wheel-down should pan one column right, hOffset=%d", m.HOffset())
	}
	// Shift+wheel-up pans back left.
	nm, _ = m.Update(mouse(tea.MouseButtonWheelUp, true))
	m = nm.(Model)
	if m.HOffset() != 0 {
		t.Errorf("Shift+wheel-up should pan back to 0, hOffset=%d", m.HOffset())
	}
	// Native wheel-right also pans (no shift).
	nm, _ = m.Update(mouse(tea.MouseButtonWheelRight, false))
	m = nm.(Model)
	if m.HOffset() != 1 {
		t.Errorf("wheel-right should pan one column right, hOffset=%d", m.HOffset())
	}
	// And a plain (no-shift) vertical wheel does NOT pan horizontally.
	hBefore := m.HOffset()
	nm, _ = m.Update(mouse(tea.MouseButtonWheelDown, false))
	m = nm.(Model)
	if m.HOffset() != hBefore {
		t.Errorf("plain wheel-down must not change hOffset (got %d, was %d)", m.HOffset(), hBefore)
	}
}
