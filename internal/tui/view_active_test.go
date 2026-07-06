package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestActiveViewAdmitsArchivedHeadful proves the default `active` view predicate:
// (not archived OR headful) AND not on hold. An archived thread stays visible while
// it is still headful (a live pane) and drops out once it goes headless; on-hold
// threads are excluded regardless of head.
func TestActiveViewAdmitsArchivedHeadful(t *testing.T) {
	cases := []struct {
		name     string
		archived bool
		head     api.Head
		onHold   bool
		want     bool
	}{
		{"live non-archived idle", false, api.Headless, false, true},
		{"live non-archived headful", false, api.Headful, false, true},
		{"archived + headful (still running) -> shown", true, api.Headful, false, true},
		{"archived + headless (parked) -> hidden", true, api.Headless, false, false},
		{"archived + headful but on hold -> hidden", true, api.Headful, true, false},
		{"non-archived headful on hold -> hidden", false, api.Headful, true, false},
		{"non-archived headless on hold -> hidden", false, api.Headless, true, false},
	}
	for _, c := range cases {
		// Archived is on the embedded Thread; OnHold is the derived field on ThreadRow.
		row := api.ThreadRow{Thread: api.Thread{Archived: c.archived}, Head: c.head, OnHold: c.onHold}
		if got := builtinViewAdmits(ViewActive, row); got != c.want {
			t.Errorf("%s: builtinViewAdmits(active)=%v, want %v", c.name, got, c.want)
		}
	}
}

// The other built-in views are unchanged by the archived-but-headful default: `on
// hold` and `archived` still filter on their single axis.
func TestOtherBuiltinViewsUnchanged(t *testing.T) {
	archivedHeadful := api.ThreadRow{Thread: api.Thread{Archived: true}, Head: api.Headful}
	if builtinViewAdmits(ViewHold, archivedHeadful) {
		t.Error("on-hold view must not admit an archived-headful thread that isn't on hold")
	}
	if !builtinViewAdmits(ViewArchived, archivedHeadful) {
		t.Error("archived view must admit any archived thread (headful or not)")
	}
	if !builtinViewAdmits(ViewAll, archivedHeadful) {
		t.Error("all view must admit everything")
	}
	held := api.ThreadRow{Thread: api.Thread{}, Head: api.Headful, OnHold: true}
	if !builtinViewAdmits(ViewHold, held) {
		t.Error("on-hold view must admit a non-archived on-hold thread")
	}
}

// TestGutterHeaderWidth guards header↔row alignment: the header text over the state
// gutter must be exactly gutterWidth terminal columns wide, so "HBD" sits above the
// head/busy/descendant glyphs and the column headers line up with the data cells.
func TestGutterHeaderWidth(t *testing.T) {
	if w := len([]rune(gutterHeader)); w != gutterWidth {
		t.Errorf("gutterHeader is %d columns (%q), want gutterWidth=%d — header and rows will misalign", w, gutterHeader, gutterWidth)
	}
}

// ArchivedGlyph marks archived rows only.
func TestArchivedGlyph(t *testing.T) {
	if g := ArchivedGlyph(api.ThreadRow{Thread: api.Thread{Archived: true}}); g != "⊘" {
		t.Errorf("archived glyph = %q, want ⊘", g)
	}
	if g := ArchivedGlyph(api.ThreadRow{Thread: api.Thread{Archived: false}}); g != " " {
		t.Errorf("non-archived glyph = %q, want a blank", g)
	}
}

// The rendered grid shows the ⊘ glyph on an archived row and not on a live one, and
// the gutter header stays aligned (still reads HBD).
func TestViewRendersArchivedGlyph(t *testing.T) {
	m := Model{rows: []api.ThreadRow{
		{Thread: api.Thread{ID: "live", Name: "liveone"}, Head: api.Headful},
		{Thread: api.Thread{ID: "arch", Name: "archedone", Archived: true}, Head: api.Headful},
	}}
	m.columns = append([]string(nil), DefaultColumns...)
	m.view = ViewAll // so both rows render regardless of the active filter
	view := m.View()
	archLine := rowLineLocal(view, "archedone")
	if !strings.Contains(archLine, ArchivedGlyph(api.ThreadRow{Thread: api.Thread{Archived: true}})) {
		t.Errorf("archived row missing the ⊘ glyph: %q", archLine)
	}
	liveLine := rowLineLocal(view, "liveone")
	if strings.Contains(liveLine, "⊘") {
		t.Errorf("live (non-archived) row wrongly shows the ⊘ glyph: %q", liveLine)
	}
	if !strings.Contains(view, "HBD") {
		t.Errorf("gutter header should still read HBD: %q", view)
	}
}
