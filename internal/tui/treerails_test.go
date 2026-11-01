package tui

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestTreeRailsCarryAStem: a child row renders "├─ "/"└─ " rather than a bare
// "├ "/"└ " — the horizontal stem is what makes a nested row read as nested at
// a glance. The rail's width is a CONTRACT with the child indent below it: a
// rail one column wider than the indent walks a deep tree out of alignment, so
// this asserts whole prefixes, including a grandchild whose │ must land in the
// same column as its parent's ├.
func TestTreeRailsCarryAStem(t *testing.T) {
	th := func(id, parent string) api.ThreadRow {
		return api.ThreadRow{Thread: api.Thread{ID: id, Name: id, Parent: parent}}
	}
	m := Model{
		rows: []api.ThreadRow{
			th("root", ""),
			th("kid-a", "root"),
			th("kid-b", "root"),
			th("grandkid", "kid-a"),
		},
		expanded: map[string]bool{"root": true, "kid-a": true},
	}

	want := []struct{ id, prefix string }{
		{"root", "▾ "},         // a root: fold marker only, no rail
		{"kid-a", "├─ ▾ "},     // a child that is itself a parent
		{"grandkid", "│  └─ "}, // the rail continues under kid-a (kid-b follows)
		{"kid-b", "└─ "},       // the last child closes the run
	}
	got := m.visibleMatches()
	if len(got) != len(want) {
		t.Fatalf("visible rows = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].row.ID != w.id {
			t.Fatalf("row %d = %q, want %q", i, got[i].row.ID, w.id)
		}
		if got[i].prefix != w.prefix {
			t.Errorf("%s prefix = %q, want %q", w.id, got[i].prefix, w.prefix)
		}
	}

	// A collapsed parent's flagged descendant pierces the fold on the same rail
	// (see TestFoldPiercing for the visibility rule itself).
	rows := m.rows
	rows[3].Flagged = true // grandkid
	m.rows = rows
	m.expanded = nil
	pierced := m.visibleMatches()
	if len(pierced) != 2 || pierced[1].row.ID != "grandkid" {
		t.Fatalf("pierced rows = %+v, want root then grandkid", pierced)
	}
	if pierced[1].prefix != "└─ " {
		t.Errorf("pierced prefix = %q, want %q", pierced[1].prefix, "└─ ")
	}
}
