package tui

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// anchorRow is a plain, view-admitted (non-archived, not on hold) top-level thread.
func anchorRow(id, name string) api.ThreadRow {
	return api.ThreadRow{
		Thread: api.Thread{ID: id, Name: name, Machine: "m", AgentKind: "pi"},
		Head:   api.Headless, Busy: api.BusyIdle,
	}
}

func newAnchorModel(cursor int, rows ...api.ThreadRow) Model {
	return Model{
		view:     ViewActive,
		machine:  "m",
		expanded: map[string]bool{},
		rows:     rows,
		cursor:   cursor,
	}
}

// The core ticket: when a poll makes a new row appear ABOVE the selected one, the
// cursor must stay on the SAME thread — not shift onto whatever slid into its slot
// (which is how you archive/delete the wrong row).
func TestSelectionAnchoredWhenRowAppearsAbove(t *testing.T) {
	m := newAnchorModel(0, anchorRow("b", "b"), anchorRow("c", "c")) // cursor on b
	updated, _ := m.Update(meshMsg{
		rows:      []api.ThreadRow{anchorRow("a", "a"), anchorRow("b", "b"), anchorRow("c", "c")},
		fetchedAt: 1,
	})
	mm := updated.(Model)
	if sel, ok := mm.Selected(); !ok || sel.ID != "b" {
		t.Fatalf("selection must stay on b after a row appears above; got %+v ok=%v cursor=%d", sel, ok, mm.Cursor())
	}
	if mm.Cursor() != 1 {
		t.Fatalf("cursor should track b to its new index 1, got %d", mm.Cursor())
	}
}

// A row appearing BELOW the selection must also leave the selection untouched.
func TestSelectionAnchoredWhenRowAppearsBelow(t *testing.T) {
	m := newAnchorModel(0, anchorRow("a", "a")) // cursor on a
	updated, _ := m.Update(meshMsg{
		rows:      []api.ThreadRow{anchorRow("a", "a"), anchorRow("b", "b")},
		fetchedAt: 1,
	})
	mm := updated.(Model)
	if sel, ok := mm.Selected(); !ok || sel.ID != "a" || mm.Cursor() != 0 {
		t.Fatalf("selection must stay on a (index 0); got %+v ok=%v cursor=%d", sel, ok, mm.Cursor())
	}
}

// A row disappearing ABOVE the selection (not the selected one itself) keeps the
// selection on the same thread — its index shifts up by one.
func TestSelectionAnchoredWhenRowRemovedAbove(t *testing.T) {
	m := newAnchorModel(2, anchorRow("a", "a"), anchorRow("b", "b"), anchorRow("c", "c")) // cursor on c
	updated, _ := m.Update(meshMsg{
		rows:      []api.ThreadRow{anchorRow("b", "b"), anchorRow("c", "c")}, // a vanished
		fetchedAt: 1,
	})
	mm := updated.(Model)
	if sel, ok := mm.Selected(); !ok || sel.ID != "c" || mm.Cursor() != 1 {
		t.Fatalf("selection must stay on c after a row above it vanished; got %+v ok=%v cursor=%d", sel, ok, mm.Cursor())
	}
}

// The ticket's EXCEPTION: when the SELECTED thread itself leaves the view (archived/
// held/reparented away by the owner), the cursor can't follow it — it holds its
// positional slot, landing on the neighbour rather than chasing the vanished row.
func TestSelectionFallsToNeighbourWhenSelectedRowLeavesView(t *testing.T) {
	m := newAnchorModel(1, anchorRow("a", "a"), anchorRow("b", "b"), anchorRow("c", "c")) // cursor on b
	updated, _ := m.Update(meshMsg{
		rows:      []api.ThreadRow{anchorRow("a", "a"), anchorRow("c", "c")}, // b left the view
		fetchedAt: 2,
	})
	mm := updated.(Model)
	if sel, ok := mm.Selected(); !ok || sel.ID != "c" {
		t.Fatalf("when the selected row leaves the view the cursor holds its slot (neighbour c); got %+v ok=%v cursor=%d", sel, ok, mm.Cursor())
	}
}

// When the selected thread is RENAMED (re-sorting it in the freshly-fetched rows), the
// cursor follows it to its new slot rather than staying at the old index.
func TestSelectionFollowsRenamedThreadAcrossPoll(t *testing.T) {
	m := newAnchorModel(1, anchorRow("a", "a"), anchorRow("b", "b"), anchorRow("c", "c")) // cursor on b
	// Next poll: b renamed to "z" and re-sorted to the end (mesh delivers sorted rows).
	updated, _ := m.Update(meshMsg{
		rows:      []api.ThreadRow{anchorRow("a", "a"), anchorRow("c", "c"), anchorRow("b", "z")},
		fetchedAt: 3,
	})
	mm := updated.(Model)
	if sel, ok := mm.Selected(); !ok || sel.ID != "b" {
		t.Fatalf("cursor should follow the renamed thread b to its new slot; got %+v ok=%v cursor=%d", sel, ok, mm.Cursor())
	}
	if mm.Cursor() != 2 {
		t.Fatalf("renamed b should be at index 2, got %d", mm.Cursor())
	}
}

// An explicit preselect (a jump the user/master path requested) still wins over
// anchoring — anchoring must not override a pending preselect.
func TestPreselectWinsOverAnchor(t *testing.T) {
	m := newAnchorModel(0, anchorRow("a", "a"), anchorRow("b", "b")) // anchored on a
	m.preselectID = "c"
	updated, _ := m.Update(meshMsg{
		rows:          []api.ThreadRow{anchorRow("a", "a"), anchorRow("b", "b"), anchorRow("c", "c")},
		preselectSeen: true,
		fetchedAt:     1,
	})
	mm := updated.(Model)
	if sel, ok := mm.Selected(); !ok || sel.ID != "c" {
		t.Fatalf("an active preselect must win over anchoring; got %+v ok=%v", sel, ok)
	}
	if mm.preselectID != "" {
		t.Fatalf("preselect should have landed and cleared, got %q", mm.preselectID)
	}
}

// reanchorCursor unit behaviour: found → move to the id's index; not found / empty →
// hold the slot, clamped into range.
func TestReanchorCursor(t *testing.T) {
	m := newAnchorModel(2, anchorRow("a", "a"), anchorRow("b", "b"), anchorRow("c", "c"))

	m.reanchorCursor("a")
	if m.cursor != 0 {
		t.Fatalf("reanchor to a should land index 0, got %d", m.cursor)
	}

	// Not found with cursor in range: hold the slot.
	m.cursor = 1
	m.reanchorCursor("missing")
	if m.cursor != 1 {
		t.Fatalf("reanchor to a missing id should hold the slot (1), got %d", m.cursor)
	}

	// Not found with cursor out of range: clamp to the last row.
	m.cursor = 9
	m.reanchorCursor("")
	if m.cursor != 2 {
		t.Fatalf("reanchor with an out-of-range cursor should clamp to 2, got %d", m.cursor)
	}
}
