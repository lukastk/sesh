package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestActiveViewAdmitsArchivedHeadful proves the default `active` view predicate:
// (flagged OR not archived OR headful OR running) AND not on hold. An archived
// thread stays visible while it is still headful (a live pane) or RUNNING (a turn
// in flight — Lukas, 2026-07-27: live work belongs in the active view even on an
// archived thread, which notably covers HEADLESS turns, ◌▶) and drops out once it
// is quiet; a FLAGGED thread overrides archived-hiding (needs-attention) — but
// HOLD BEATS FLAG (Lukas, 2026-07-26) and hold beats RUNNING: an on-hold thread is
// excluded no matter what, so parking a thread actually parks it (its ⚑ shows in
// `on hold`).
func TestActiveViewAdmitsArchivedHeadful(t *testing.T) {
	cases := []struct {
		name     string
		archived bool
		head     api.Head
		busy     api.Busy
		onHold   bool
		flagged  bool
		want     bool
	}{
		{"live non-archived idle", false, api.Headless, api.BusyIdle, false, false, true},
		{"live non-archived headful", false, api.Headful, api.BusyIdle, false, false, true},
		{"archived + headful (live pane) -> shown", true, api.Headful, api.BusyIdle, false, false, true},
		{"archived + headless idle (parked) -> hidden", true, api.Headless, api.BusyIdle, false, false, false},
		{"archived + headful but on hold -> hidden", true, api.Headful, api.BusyIdle, true, false, false},
		{"non-archived headful on hold -> hidden", false, api.Headful, api.BusyIdle, true, false, false},
		{"non-archived headless on hold -> hidden", false, api.Headless, api.BusyIdle, true, false, false},
		{"FLAGGED archived headless -> SHOWN", true, api.Headless, api.BusyIdle, false, true, true},
		{"FLAGGED on hold -> HIDDEN (hold beats flag)", false, api.Headless, api.BusyIdle, true, true, false},
		{"FLAGGED archived + on hold -> HIDDEN (hold beats flag)", true, api.Headless, api.BusyIdle, true, true, false},
		{"FLAGGED headful on hold -> HIDDEN (hold beats flag)", false, api.Headful, api.BusyIdle, true, true, false},
		// RUNNING (2026-07-27). The load-bearing new case is the first: an
		// archived thread running a HEADLESS turn was hidden before.
		{"archived + headless RUNNING -> SHOWN", true, api.Headless, api.BusyBusy, false, false, true},
		{"archived + headful RUNNING -> shown", true, api.Headful, api.BusyBusy, false, false, true},
		{"non-archived headless RUNNING -> shown", false, api.Headless, api.BusyBusy, false, false, true},
		{"RUNNING on hold -> HIDDEN (hold beats running)", false, api.Headless, api.BusyBusy, true, false, false},
		{"archived RUNNING on hold -> HIDDEN (hold beats running)", true, api.Headless, api.BusyBusy, true, false, false},
		// An UNKNOWN busy axis (the zero value, "?") is not running: it must not
		// resurrect an archived thread.
		{"archived + headless busy-unknown -> hidden", true, api.Headless, api.Busy(""), false, false, false},
	}
	for _, c := range cases {
		// Archived/Flagged are on the embedded Thread; OnHold is derived on ThreadRow.
		row := api.ThreadRow{Thread: api.Thread{Archived: c.archived, Flagged: c.flagged}, Head: c.head, Busy: c.busy, OnHold: c.onHold}
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
