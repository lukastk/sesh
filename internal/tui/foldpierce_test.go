package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestFoldPiercing (ticket df4fb07a): flagged descendants stay visible under
// a COLLAPSED parent — a flag must never hide inside a fold — while unflagged
// siblings stay hidden; expanding shows everyone as before; a deep flagged
// grandchild pierces too (intermediate unflagged ancestry elided).
func TestFoldPiercing(t *testing.T) {
	th := func(id, parent string, flagged bool) api.ThreadRow {
		r := api.ThreadRow{Thread: api.Thread{ID: id, Name: id, Parent: parent}}
		r.Flagged = flagged
		return r
	}
	m := Model{rows: []api.ThreadRow{
		th("root", "", false),
		th("plain", "root", false),
		th("hot", "root", true),
		th("mid", "root", false),
		th("deep", "mid", true), // flagged grandchild under an unflagged child
	}}

	ids := func() []string {
		var out []string
		for _, tr := range m.visibleMatches() {
			out = append(out, tr.row.ID)
		}
		return out
	}

	// Collapsed (the default): the flagged child AND the flagged grandchild
	// pierce; the unflagged child + intermediate stay hidden.
	got := strings.Join(ids(), ",")
	if got != "root,hot,deep" {
		t.Fatalf("collapsed pierce = %s, want root,hot,deep", got)
	}
	// The collapsed parent still shows the ▸ marker (more is hidden).
	if pre := m.visibleMatches()[0].prefix; !strings.Contains(pre, "▸") {
		t.Fatalf("collapsed pierced parent prefix %q should keep ▸", pre)
	}

	// Expanded: the normal full tree (piercing only applies to folds).
	m.expanded = map[string]bool{"root": true, "mid": true}
	got = strings.Join(ids(), ",")
	if got != "root,plain,hot,mid,deep" {
		t.Fatalf("expanded tree = %s, want the full tree", got)
	}

	// No flags → a collapsed parent hides everything, as always.
	m.expanded = nil
	rows := m.rows
	for i := range rows {
		rows[i].Flagged = false
	}
	m.rows = rows
	if got = strings.Join(ids(), ","); got != "root" {
		t.Fatalf("collapsed unflagged tree = %s, want root only", got)
	}
}
