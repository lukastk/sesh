package tui

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestFlagGlyphStates pins the flag gutter cell (schema 44): ⚑ flagged wins
// over the disabled marker; ⌀ marks flag-disabled; blank otherwise. BusyGlyph
// is back to the plain execution axis (the 43-era ‼/✔ overlays are gone).
func TestFlagGlyphStates(t *testing.T) {
	row := func(flagged, disabled bool) api.ThreadRow {
		r := api.ThreadRow{}
		r.Flagged = flagged
		r.FlagDisabled = disabled
		return r
	}
	cases := []struct {
		name string
		row  api.ThreadRow
		want string
	}{
		{"plain", row(false, false), " "},
		{"flagged", row(true, false), "⚑"},
		{"disabled", row(false, true), "⌀"},
		{"flagged wins over disabled", row(true, true), "⚑"},
	}
	for _, tc := range cases {
		if got := FlagGlyph(tc.row); got != tc.want {
			t.Errorf("%s: FlagGlyph = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := BusyGlyph(api.ThreadRow{Busy: api.BusyBusy}); got != "▶" {
		t.Errorf("BusyGlyph(busy) = %q, want ▶", got)
	}
	if got := BusyGlyph(api.ThreadRow{Busy: api.BusyIdle}); got != "·" {
		t.Errorf("BusyGlyph(idle) = %q, want ·", got)
	}
}

// TestPredicateFlagged pins the flagged/flagdisabled filter keywords.
func TestPredicateFlagged(t *testing.T) {
	flagged := api.ThreadRow{}
	flagged.Flagged = true
	disabled := api.ThreadRow{}
	disabled.FlagDisabled = true
	plain := api.ThreadRow{}

	for _, tc := range []struct {
		expr string
		row  api.ThreadRow
		want bool
	}{
		{"flagged", flagged, true}, {"flagged", plain, false},
		{"flagdisabled", disabled, true}, {"flagdisabled", plain, false},
		{"not flagged and not flagdisabled", plain, true},
	} {
		p, err := CompilePredicate(tc.expr)
		if err != nil {
			t.Fatalf("CompilePredicate(%q): %v", tc.expr, err)
		}
		if got := p.Eval(tc.row); got != tc.want {
			t.Errorf("%q = %v, want %v", tc.expr, got, tc.want)
		}
	}
}
