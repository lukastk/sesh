package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestFlagGlyphStates pins the flag gutter cell (schema 44): ⚑ flagged wins
// over the disabled marker; ⌁ marks flag-disabled; blank otherwise. BusyGlyph
// is back to the plain execution axis (the 43-era ‼/✔ overlays are gone).
//
// The disabled marker must NOT be a slashed circle — it neighbours the archived
// ⊘ cell, and the previous ⌀ was indistinguishable from it (see TestGutterGlyphsDistinct).
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
		{"disabled", row(false, true), "⌁"},
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

// TestGutterGlyphsDistinct is a VOCABULARY drift guard for the state gutter.
//
// The gutter packs seven 1-cell states side by side, so two glyphs that merely
// LOOK alike are as bad as a literal duplicate: ⌀ (flag-disabled) and ⊘
// (archived) are different codepoints but render as the same slashed circle in
// a terminal font, and they sit in ADJACENT cells — an archived flag-disabled
// row read as one doubled symbol. Distinct runes alone would not have caught it,
// so this test ALSO refuses two glyphs drawn from the same confusable family.
//
// SCOPE: the gutter only. The NAME column's fold markers (▸/▾) share a row with
// it and ▸ is a smaller ▶, but they sit in a different region with a distinct
// role and Lukas signed that overlap off (2026-08-02) — widening this test to
// cover them would be re-litigating a decision, not catching drift.
//
// The `?` unknown glyph is deliberately absent: HeadGlyph and BusyGlyph both use
// it, and that IS one semantic ("this axis is unknown") on two axes.
//
// Adding a gutter glyph? Add it here. If it collides, pick a different shape —
// do not delete the family or the entry.
func TestGutterGlyphsDistinct(t *testing.T) {
	headful := api.ThreadRow{}
	headful.Head = api.Headful
	headless := api.ThreadRow{}
	headless.Head = api.Headless
	virtual := api.ThreadRow{}
	virtual.AgentKind = api.VirtualAgentKind
	archived := api.ThreadRow{}
	archived.Archived = true
	flagged := api.ThreadRow{}
	flagged.Flagged = true
	disabled := api.ThreadRow{}
	disabled.FlagDisabled = true
	shellLive := api.ThreadRow{}
	shellLive.AgentKind = api.ShellAgentKind
	shellLive.Head = api.Headful
	shellDead := api.ThreadRow{}
	shellDead.AgentKind = api.ShellAgentKind
	shellDead.Head = api.Headless
	heldOwn := api.ThreadRow{OnHold: true, OnHoldEffectiveUnix: 100}
	heldOwn.OnHoldUntilUnix = 100
	heldInherited := api.ThreadRow{OnHold: true, OnHoldEffectiveUnix: 100} // own 0 < effective
	// Every non-blank glyph the gutter can render, by the state it means.
	glyphs := map[string]string{
		"head/headful":    HeadGlyph(headful),
		"head/headless":   HeadGlyph(headless),
		"head/virtual":    HeadGlyph(virtual),
		"head/shell-live": HeadGlyph(shellLive),
		"head/shell-dead": HeadGlyph(shellDead),
		"busy/busy":       BusyGlyph(api.ThreadRow{Busy: api.BusyBusy}),
		"busy/idle":       BusyGlyph(api.ThreadRow{Busy: api.BusyIdle}),
		"desc/running":    DescendantGlyph(true),
		"att/attached":    "*", // rendered inline in View
		"arch/archived":   ArchivedGlyph(archived),
		"flag/flagged":    FlagGlyph(flagged),
		"flag/disabled":   FlagGlyph(disabled),
		"mark/moving":     markGlyph(api.ThreadRow{}, true),
		"hold/own":        HoldGlyph(heldOwn),
		"hold/inherited":  HoldGlyph(heldInherited),
	}

	seen := map[string]string{}
	for state, g := range glyphs {
		if g == " " || g == "" {
			t.Errorf("%s: gutter glyph is blank — a meaningful state needs a visible marker", state)
			continue
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("glyph %q means BOTH %s and %s — one state must pick a different glyph", g, prev, state)
			continue
		}
		seen[g] = state
	}

	// Confusable FAMILIES: shapes a terminal font renders near-identically at one
	// cell. Two gutter states may never draw from the same family, distinct
	// codepoints or not. NB ● is NOT in the dot family — a filled circle beside a
	// mid dot is the documented `●·` signature and reads fine at any size.
	families := map[string]string{
		"slashed circle": "⊘⌀⊗∅Ø⦸",
		"dot":            "·•∙‧⋅",
		"right triangle": "▶▸►▹",
		"vertical arrow": "↕↑↓⇕",
		// The three head-axis KIND families, one per kind — this IS the head
		// vocabulary: agent = round, shell = chevron, virtual = stacked lines.
		// Reaching into one of these with a new gutter glyph is reaching into a
		// kind's identity, so it trips here. NB ● lives in "round" but NOT in the
		// "dot" family above: a filled circle beside a mid dot is the documented
		// `●·` signature and reads fine.
		"round":         "●○◯⬤◍◉◌",
		"chevron":       "❯❮›‹»«⟩⟨",
		"stacked lines": "≡☰≣⋮⁝",
		// Hollow quadrilaterals: a hollow square and a diamond render alike at one
		// cell. NO gutter glyph draws from this family any more — virtual moved off
		// ◇ to ≡ (2026-08-28), which is also what freed the shell pair from the
		// narrow rectangles ▮/▯ it had been squeezed into. Kept as a pure drift
		// guard so nobody walks a gutter glyph back into that look.
		"hollow quadrilateral": "◇▢▣□◻",
		// The hold pair's family. Nothing else in the gutter is an hourglass, and
		// the point of listing it is the same as every other entry: a THIRD glyph
		// reaching into this shape later trips the guard.
		"hourglass": "⧖⧗⌛⏳",
	}
	// familyExempt: a state may share ONE named family, with the reason. Keyed by
	// state+family deliberately — a blanket per-state exemption would also wave the
	// glyph past every OTHER family, which is how a real collision slips back in.
	// An exemption is a decision, not a shortcut — state it or don't take it.
	familyExempt := map[string]string{
		"mark/moving|vertical arrow": "↕ is a TRANSIENT move-mode marker on exactly one " +
			"row, three cells left of the persistent ↓; it never co-occurs as a steady state",
		// A live/not-live PAIR on ONE axis is MEANT to look related: the shared family
		// is the signal that says "same axis, two states of it", which is how ●/◌ has
		// always read. H78's failure was two states from DIFFERENT axes looking alike
		// (⌀ flag-disabled beside ⊘ archived) — that, not this, is what the guard is
		// for. So a pair counts as ONE occupant of its family: the not-live half is
		// exempt, the live half is not, which keeps the family protective — a THIRD
		// glyph reaching into it still trips.
		"head/headless|round":     "the not-live half of the agent pair ●/◌ — a pair counts as one occupant",
		"head/shell-dead|chevron": "the not-live half of the shell pair ❯/› — a pair counts as one occupant",
		"hold/inherited|hourglass": "the second state of the hold pair ⧗/⧖ (own vs inherited) — " +
			"one axis, two values, so the pair counts as one occupant",
	}
	for family, runes := range families {
		var hits []string
		for _, r := range runes {
			state, ok := seen[string(r)]
			if !ok {
				continue
			}
			if _, exempt := familyExempt[state+"|"+family]; exempt {
				continue
			}
			hits = append(hits, state+" "+string(r))
		}
		if len(hits) > 1 {
			t.Errorf("gutter draws %d glyphs from the confusable %q family (%v) — they render alike side by side; pick a distinct shape",
				len(hits), family, hits)
		}
	}
	for key := range familyExempt {
		state, family, ok := strings.Cut(key, "|")
		if !ok {
			t.Errorf("familyExempt key %q must be \"<state>|<family>\"", key)
			continue
		}
		if _, ok := glyphs[state]; !ok {
			t.Errorf("familyExempt names state %q, which is not a gutter glyph — drop the stale exemption", key)
		}
		if _, ok := families[family]; !ok {
			t.Errorf("familyExempt names family %q, which is not a confusable family — drop the stale exemption", key)
		}
	}
}

// TestShellRowNeverNeighboursBusyGlyph pins the STRUCTURAL reason the shell head
// glyph ❯ can share a gutter with the busy ▶ although both point right: they sit in
// DIFFERENT columns, and a shell thread's busy cell is BLANK (a shell has no turn
// that could be executing), so the two can only ever appear diagonally — never side
// by side on a row, never stacked in one column.
//
// If a shell thread ever grows a busy axis, that blank goes away, ❯ and ▶ become
// horizontally adjacent, and the head glyph has to be reconsidered. This test is
// where that decision lands.
func TestShellRowNeverNeighboursBusyGlyph(t *testing.T) {
	live := api.ThreadRow{}
	live.AgentKind = api.ShellAgentKind
	live.Head = api.Headful
	dead := api.ThreadRow{}
	dead.AgentKind = api.ShellAgentKind
	dead.Head = api.Headless

	for name, row := range map[string]api.ThreadRow{"live": live, "no-session": dead} {
		if got := BusyGlyph(row); got != " " {
			t.Errorf("shell (%s) busy glyph = %q, want blank — a non-blank cell would put "+
				"the head chevron ❯ directly beside the busy ▶", name, got)
		}
		if got := HeadGlyph(row); got == BusyGlyph(api.ThreadRow{Busy: api.BusyBusy}) {
			t.Errorf("shell (%s) head glyph is the busy glyph %q", name, got)
		}
	}
}
