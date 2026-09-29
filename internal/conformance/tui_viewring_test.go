package conformance

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() { registerTUIClaim("view-ring", claimViewRing) }

// claimViewRing: ONE key flips the grid between the views in [tui] view_ring and
// back — the mechanism the cockpit's Shift+F12 drives by injecting that key into
// the traveling sidebar with `tmux send-keys`.
//
// Driven against a REAL daemon with a REAL flagged thread, because the thing that
// must be true is not "the view index changed" but "the rows on screen are now
// only the flagged ones" — which a stubbed row set cannot prove. The key is
// delivered as an F-KEY through a [[tui.key]]-resolved keymap, i.e. byte-for-byte
// the path the cockpit binding uses; a command reachable only from the palette
// would be useless to it.
func claimViewRing(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	plain := sb.newHeadlessThread(t, "pi", "ring-plain")
	flagged := sb.newHeadlessThread(t, "pi", "ring-flagged")
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--on", "--id", flagged.ID); err != nil {
		t.Fatalf("flag: %v\n%s", err, stderr)
	}

	// Lukas's real shape: a `flagged` custom view sitting right after `active`,
	// and a ring over exactly those two.
	views, err := tui.CompileViews([]tui.ViewSpec{
		{Name: "flagged", Filter: "flagged", Position: 2},
		{Name: "ticketed", Filter: "ticketed and not archived"},
	})
	if err != nil {
		t.Fatal(err)
	}
	km, err := tui.ResolveKeymap([]tui.KeySpec{{Command: "view-ring", Key: "f12"}})
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithViews(views).WithKeymap(km)
	m, err = m.WithViewRing([]string{"active", "flagged"})
	if err != nil {
		t.Fatal(err)
	}

	// BASELINE FIRST: both rows really are in `active`. Without this the "only the
	// flagged row" assertion below could pass on a grid that simply never showed
	// the other thread.
	m = renderUntilCount(t, m, 2)
	view := m.View()
	if !strings.Contains(view, "[active]") {
		t.Fatalf("baseline is not the active view:\n%s", view)
	}
	if names := modelRowNames(m); !containsString(names, plain.Name) || !containsString(names, flagged.Name) {
		t.Fatalf("baseline rows = %v, want both %q and %q", names, plain.Name, flagged.Name)
	}

	// ONE key: into the flagged view, showing ONLY the flagged thread.
	m, view = ringKey(t, m)
	if !strings.Contains(view, "[flagged]") {
		t.Fatalf("the ring key did not reach the flagged view:\n%s", view)
	}
	if names := modelRowNames(m); len(names) != 1 || names[0] != flagged.Name {
		t.Fatalf("flagged view rows = %v, want only %q — the ring changed the title but not the rows", names, flagged.Name)
	}

	// The SAME key again: back to active, with the unflagged thread present again.
	m, view = ringKey(t, m)
	if !strings.Contains(view, "[active]") {
		t.Fatalf("the second press did not come back to active (it must not walk on to the next view):\n%s", view)
	}
	if names := modelRowNames(m); !containsString(names, plain.Name) {
		t.Fatalf("back in active the unflagged thread is missing: rows = %v", names)
	}

	// From a view the ring does not mention — reached through the REAL view picker
	// — the key enters the ring at its first entry rather than wherever it feels
	// like. (Ordered active, flagged, on hold, archived, all, ticketed: `all` is
	// four steps down from active.)
	for i := 0; i < 4; i++ {
		m = runKey(t, m, "tab")
		if v := m.View(); !strings.Contains(v, "view · tab") {
			t.Fatalf("the view picker did not open:\n%s", v)
		}
		m = runKey(t, m, "j")
		m = runKey(t, m, "enter")
		m, _ = render(t, m)
	}
	if v := m.View(); !strings.Contains(v, "[all]") {
		t.Fatalf("could not reach a view outside the ring with the picker:\n%s", v)
	}
	m, view = ringKey(t, m)
	if !strings.Contains(view, "[active]") {
		t.Fatalf("from outside the ring the key must enter at its FIRST entry (active):\n%s", view)
	}
}

// ringKey presses the F-KEY bound to view-ring and settles the refetch, returning
// the rendered grid. The F-key form is the point: it is what `tmux send-keys F12`
// delivers into the sidebar pane.
func ringKey(t *testing.T, m tui.Model) (tui.Model, string) {
	t.Helper()
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF12})
	m = nm.(tui.Model)
	if cmd != nil {
		nm2, _ := m.Update(cmd())
		m = nm2.(tui.Model)
	}
	if err := m.ActionErr(); err != nil {
		t.Fatalf("the ring key errored: %v", err)
	}
	// The rows for the new view arrive with the refetch above, but give the grid a
	// beat to settle before reading it (the daemon is real).
	deadline := time.Now().Add(5 * time.Second)
	var view string
	for {
		m, view = render(t, m)
		if len(m.Rows()) > 0 || time.Now().After(deadline) {
			return m, view
		}
		time.Sleep(100 * time.Millisecond)
	}
}
