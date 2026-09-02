package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lukastk/sesh/internal/api"
)

// holdArgvModel builds a one-row model whose routed verbs run a REAL fake `sesh`
// on disk that logs its argv, so a test asserts the command actually issued rather
// than an internal flag. Which command an un-hold issues is the whole behaviour
// here: a clear cannot undercut an ancestor's hold, only a release can.
func holdArgvModel(t *testing.T, row api.ThreadRow) (Model, func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "sesh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s ' \"$@\" >> %q\nprintf '\\n' >> %q\n", log, log)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := Model{
		machine:    row.Machine,
		rows:       []api.ThreadRow{row},
		machines:   reachableMachines(row.Machine),
		binaryPath: bin,
	}
	m.machines[0].Self = true
	return m, func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
}

func runHoldCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command to be issued")
	}
	cmd()
}

// THE REPORTED BUG: `h` on a thread held only by an ANCESTOR used to decide on the
// thread's OWN deadline, which was 0 — so the "un-hold" key took the SET branch and
// parked it until tomorrow. Worse, that left behind an own hold that OUTLIVED the
// parent's, so releasing the parent no longer freed the child. The toggle now keys
// on the effective state and issues a RELEASE.
func TestHoldToggleReleasesInheritedHold(t *testing.T) {
	now := time.Now().Unix()
	row := api.ThreadRow{
		Thread:              api.Thread{ID: "child", Name: "child", Machine: "mymain"},
		OnHold:              true,
		OnHoldEffectiveUnix: now + 7200, // parked by an ancestor; own deadline is 0
	}
	m, argv := holdArgvModel(t, row)
	runHoldCmd(t, m.holdToggleSelected())

	got := argv()
	if len(got) != 1 {
		t.Fatalf("expected exactly one routed command, got %v", got)
	}
	if !strings.Contains(got[0], "--release") {
		t.Errorf("un-holding a thread held by its ANCESTOR must issue --release, got: %s", got[0])
	}
	if strings.Contains(got[0], "--clear") {
		t.Errorf("a clear cannot undercut an ancestor's hold: %s", got[0])
	}
	// The regression itself: the toggle must never issue a plain hold here.
	if strings.Contains(got[0], "--until-unix") && !strings.Contains(got[0], "--release") {
		t.Errorf("the un-hold key issued a HOLD — the reported bug: %s", got[0])
	}
}

// A thread held by its OWN deadline is un-held by clearing it: no release is
// needed (nothing dominates it), and a release would leave stale state behind.
func TestHoldToggleClearsOwnHold(t *testing.T) {
	now := time.Now().Unix()
	row := api.ThreadRow{
		Thread:              api.Thread{ID: "own", Name: "own", Machine: "mymain", OnHoldUntilUnix: now + 3600},
		OnHold:              true,
		OnHoldEffectiveUnix: now + 3600,
	}
	m, argv := holdArgvModel(t, row)
	runHoldCmd(t, m.holdToggleSelected())

	got := argv()
	if len(got) != 1 || !strings.Contains(got[0], "--clear") {
		t.Fatalf("un-holding an own-held thread should --clear, got %v", got)
	}
	if strings.Contains(got[0], "--release") {
		t.Errorf("no ancestor dominates this thread — a release is not needed: %s", got[0])
	}
}

// An un-held thread is parked until the start of tomorrow (the other half of the
// toggle, unchanged) — including a RELEASED one, since setting a hold zeroes the
// release, so `h` still means "held" / "not held".
func TestHoldToggleHoldsUnheldAndReleasedThreads(t *testing.T) {
	now := time.Now().Unix()
	for _, tc := range []struct {
		name string
		row  api.ThreadRow
	}{
		{"plain", api.ThreadRow{Thread: api.Thread{ID: "a", Name: "a", Machine: "mymain"}}},
		{"released", api.ThreadRow{Thread: api.Thread{ID: "b", Name: "b", Machine: "mymain", HoldReleaseUntilUnix: now + 3600}}},
	} {
		m, argv := holdArgvModel(t, tc.row)
		runHoldCmd(t, m.holdToggleSelected())
		got := argv()
		if len(got) != 1 || !strings.Contains(got[0], "--until-unix") {
			t.Fatalf("%s: an un-held thread should be parked, got %v", tc.name, got)
		}
		if strings.Contains(got[0], "--release") || strings.Contains(got[0], "--clear") {
			t.Errorf("%s: expected a plain hold, got: %s", tc.name, got[0])
		}
	}
}

// The `H` prompt's EMPTY input means "not held", so it follows the same rule as the
// toggle: a thread dominated by an ancestor needs a release, not a clear.
func TestHoldPromptEmptyReleasesInheritedHold(t *testing.T) {
	now := time.Now().Unix()
	row := api.ThreadRow{
		Thread:              api.Thread{ID: "child", Name: "child", Machine: "mymain"},
		OnHold:              true,
		OnHoldEffectiveUnix: now + 7200,
	}
	m, argv := holdArgvModel(t, row)
	mm, _ := m.runCommand("hold-until")
	m = mm.(Model)
	mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	runHoldCmd(t, cmd)

	got := argv()
	if len(got) != 1 || !strings.Contains(got[0], "--release") {
		t.Fatalf("an empty H prompt on an inherited hold should --release, got %v", got)
	}
}

// The HOLD column distinguishes the three states, because a released thread is
// otherwise indistinguishable from an ordinary un-held one — leaving no way to see
// why a thread sits in the active view while its parent is parked.
func TestHoldColumnRendersReleaseAndInheritance(t *testing.T) {
	now := time.Now().Unix()
	future := now + 48*3600
	cell := colSpecByName(t, ColHold)
	m := &Model{}

	own := api.ThreadRow{Thread: api.Thread{OnHoldUntilUnix: future}, OnHold: true, OnHoldEffectiveUnix: future}
	if got := cell(m, own); strings.HasPrefix(got, "↑") || strings.HasPrefix(got, "~") || got == "" {
		t.Errorf("own hold should render a bare date, got %q", got)
	}
	inherited := api.ThreadRow{Thread: api.Thread{}, OnHold: true, OnHoldEffectiveUnix: future}
	if got := cell(m, inherited); !strings.HasPrefix(got, "↑") {
		t.Errorf("an inherited hold should render the ↑ marker, got %q", got)
	}
	released := api.ThreadRow{Thread: api.Thread{HoldReleaseUntilUnix: future}}
	if got := cell(m, released); !strings.HasPrefix(got, "~") {
		t.Errorf("a live release should render the ~ marker, got %q", got)
	}
	lapsed := api.ThreadRow{Thread: api.Thread{HoldReleaseUntilUnix: now - 3600}}
	if got := cell(m, lapsed); got != "" {
		t.Errorf("a LAPSED release must render nothing, got %q", got)
	}
}

// colSpecByName finds a column's cell renderer so a test can drive it directly.
func colSpecByName(t *testing.T, name string) func(*Model, api.ThreadRow) string {
	t.Helper()
	for _, c := range colOrder {
		if c.name == name {
			return c.cell
		}
	}
	t.Fatalf("no column named %q", name)
	return nil
}

// The hold SIGIL (⧗ own / ⧖ inherited) shares the leading mark cell with move
// mode's ↕. The pair is the point: the two states need different actions — an own
// hold is cleared, an inherited one can only be released — so a single "held"
// marker would hide exactly the distinction that makes the `on hold` view
// actionable, and would say nothing there at all (every row in it is held).
func TestHoldGlyphPair(t *testing.T) {
	now := time.Now().Unix()
	for _, tc := range []struct {
		name string
		row  api.ThreadRow
		want string
	}{
		{"not held", api.ThreadRow{}, " "},
		{"own hold", api.ThreadRow{
			Thread: api.Thread{OnHoldUntilUnix: now + 3600}, OnHold: true, OnHoldEffectiveUnix: now + 3600}, "⧗"},
		{"inherited hold", api.ThreadRow{
			OnHold: true, OnHoldEffectiveUnix: now + 3600}, "⧖"},
		{"own hold LATER than the ancestor's still reads as its own", api.ThreadRow{
			Thread: api.Thread{OnHoldUntilUnix: now + 7200}, OnHold: true, OnHoldEffectiveUnix: now + 7200}, "⧗"},
		// A released thread is not held, so the cell is blank; the ~<date> in the
		// HOLD column is what reports a release in force.
		{"released", api.ThreadRow{Thread: api.Thread{HoldReleaseUntilUnix: now + 3600}}, " "},
		// A LAPSED hold reads not-held: the owner derives OnHold against its clock,
		// and a stale deadline lingering on the record must not draw a sigil.
		{"lapsed hold", api.ThreadRow{Thread: api.Thread{OnHoldUntilUnix: now - 3600}}, " "},
	} {
		if got := HoldGlyph(tc.row); got != tc.want {
			t.Errorf("%s: HoldGlyph = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Move mode owns the mark cell while it is active — exactly one row, transiently —
// and the hold sigil returns the moment it ends. Sharing the cell is what makes the
// sigil free; this pins that the sharing has a defined winner.
func TestMarkGlyph(t *testing.T) {
	now := time.Now().Unix()
	held := api.ThreadRow{Thread: api.Thread{OnHoldUntilUnix: now + 3600}, OnHold: true, OnHoldEffectiveUnix: now + 3600}
	if got := markGlyph(held, true); got != "↕" {
		t.Errorf("move mode must win the mark cell: got %q, want ↕", got)
	}
	if got := markGlyph(held, false); got != "⧗" {
		t.Errorf("a held row not being moved shows its hold sigil: got %q, want ⧗", got)
	}
	if got := markGlyph(api.ThreadRow{}, false); got != " " {
		t.Errorf("an ordinary row leaves the mark cell blank: got %q", got)
	}
}

// The sigil must actually reach the rendered row, in BOTH the ordinary and the
// selected (reverse-video) branches — the selected branch builds its gutter
// separately, which is where a new cell is easiest to drop on the floor.
func TestHoldGlyphRenders(t *testing.T) {
	now := time.Now().Unix()
	rows := []api.ThreadRow{
		{Thread: api.Thread{ID: "own", Name: "own-held", Machine: "m", OnHoldUntilUnix: now + 3600},
			OnHold: true, OnHoldEffectiveUnix: now + 3600},
		{Thread: api.Thread{ID: "inh", Name: "inherited-held", Machine: "m"},
			OnHold: true, OnHoldEffectiveUnix: now + 3600},
		{Thread: api.Thread{ID: "free", Name: "not-held", Machine: "m"}},
	}
	m := Model{rows: rows, machines: reachableMachines("m"), machine: "m", view: ViewAll, width: 80, height: 20}
	m.machines[0].Self = true
	out := m.View()
	for _, want := range []string{"⧗", "⧖"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered grid is missing the %q sigil:\n%s", want, out)
		}
	}
	// ...and with the cursor ON the held row, so the reverse-video branch renders it.
	m.cursor = 0
	if sel := m.View(); !strings.Contains(sel, "⧗") {
		t.Errorf("the SELECTED row lost its hold sigil:\n%s", sel)
	}
}
