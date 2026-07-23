package tui

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestBusyGlyphStates pins the busy-cell glyph precedence (schema 43):
// blocked ‼ beats everything (it implies busy); done ✔ renders only on idle;
// plain busy/idle keep their glyphs.
func TestBusyGlyphStates(t *testing.T) {
	row := func(busy api.Busy, blocked, done bool) api.ThreadRow {
		return api.ThreadRow{Busy: busy, Blocked: blocked, Done: done}
	}
	cases := []struct {
		name string
		row  api.ThreadRow
		want string
	}{
		{"busy", row(api.BusyBusy, false, false), "▶"},
		{"idle", row(api.BusyIdle, false, false), "·"},
		{"blocked wins over busy", row(api.BusyBusy, true, false), "‼"},
		{"done on idle", row(api.BusyIdle, false, true), "✔"},
		{"done never shows mid-turn", row(api.BusyBusy, false, true), "▶"},
		{"blocked wins over done", row(api.BusyIdle, true, true), "‼"},
	}
	for _, tc := range cases {
		if got := BusyGlyph(tc.row); got != tc.want {
			t.Errorf("%s: BusyGlyph = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPredicateBlockedDone pins the new filter keywords.
func TestPredicateBlockedDone(t *testing.T) {
	blocked := api.ThreadRow{Blocked: true, Busy: api.BusyBusy}
	done := api.ThreadRow{Done: true, Busy: api.BusyIdle}
	plain := api.ThreadRow{Busy: api.BusyIdle}

	for _, tc := range []struct {
		expr string
		row  api.ThreadRow
		want bool
	}{
		{"blocked", blocked, true}, {"blocked", plain, false},
		{"done", done, true}, {"done", plain, false},
		{"not blocked and not done", plain, true},
	} {
		p, err := CompilePredicate(tc.expr)
		if err != nil {
			t.Fatalf("CompilePredicate(%q): %v", tc.expr, err)
		}
		if got := p.Eval(tc.row); got != tc.want {
			t.Errorf("%q on %+v = %v, want %v", tc.expr, tc.row, got, tc.want)
		}
	}
}
