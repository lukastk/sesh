package tui

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func rowsWith(ids ...string) []api.ThreadRow {
	out := make([]api.ThreadRow, len(ids))
	for i, id := range ids {
		out[i] = api.ThreadRow{Thread: api.Thread{ID: id, Name: id}}
	}
	return out
}

func hasRow(rows []api.ThreadRow, id string) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// A hide patch drops the row from the rendered set immediately (no GC on a bare
// apply — the server is still stale), and leaves siblings alone.
func TestApplyPendingHideRemovesRow(t *testing.T) {
	m := Model{rows: rowsWith("a", "b"), pending: map[string]*rowPatch{
		"a": {hide: true, ttl: optimisticTTL},
	}}
	m.applyPending(false)
	if hasRow(m.rows, "a") {
		t.Errorf("hide patch did not drop row a: %v", m.rows)
	}
	if !hasRow(m.rows, "b") {
		t.Errorf("hide patch wrongly dropped sibling b")
	}
	if m.pending["a"] == nil {
		t.Errorf("apply(false) must not GC the patch (server still stale)")
	}
}

// Once the read path catches up (the row is gone from the fetch), the patch is GC'd.
func TestApplyPendingHideGCWhenServerCaughtUp(t *testing.T) {
	m := Model{rows: rowsWith("b"), pending: map[string]*rowPatch{ // a absent: archived-out / deleted
		"a": {hide: true, ttl: optimisticTTL},
	}}
	m.applyPending(true)
	if m.pending["a"] != nil {
		t.Errorf("patch should be GC'd once the row is gone from the fetch")
	}
}

// While the read path is still stale (row present), the row stays hidden and the
// TTL counts down; at zero the patch is dropped and the row RESURFACES — surfacing
// a mutation that silently never took, never masking it forever.
func TestApplyPendingHideTTLResurrect(t *testing.T) {
	m := Model{rows: rowsWith("a", "b"), pending: map[string]*rowPatch{
		"a": {hide: true, ttl: 2},
	}}
	m.applyPending(true) // ttl 2->1, still >0: hidden
	if hasRow(m.rows, "a") {
		t.Errorf("row a should still be hidden at ttl 1")
	}
	if m.pending["a"] == nil || m.pending["a"].ttl != 1 {
		t.Errorf("ttl should have decremented to 1, got %v", m.pending["a"])
	}
	m.rows = rowsWith("a", "b") // next stale fetch still shows a
	m.applyPending(true)        // ttl 1->0: give up, surface the truth
	if !hasRow(m.rows, "a") {
		t.Errorf("row a should resurface once TTL is exhausted (silent-failure surfacing)")
	}
	if m.pending["a"] != nil {
		t.Errorf("exhausted patch should be dropped")
	}
}

// leavesCurrentView decides when an archive/unarchive should hide the row.
func TestLeavesCurrentView(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "x"}}
	cases := []struct {
		view        View
		newArchived bool
		want        bool
	}{
		{ViewActive, true, true},    // archiving in active -> leaves
		{ViewActive, false, false},  // unarchiving in active -> stays (it's active)
		{ViewArchived, false, true}, // unarchiving in archived -> leaves
		{ViewArchived, true, false}, // archiving in archived -> stays
		{ViewAll, true, false},      // all shows both
		{ViewAll, false, false},
	}
	for _, c := range cases {
		m := Model{view: c.view}
		if got := m.leavesCurrentView(row, c.newArchived); got != c.want {
			t.Errorf("view=%d newArchived=%v: leavesCurrentView=%v, want %v", c.view, c.newArchived, got, c.want)
		}
	}
}
