package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ringModel is a grid with Lukas's real view set: the four built-ins plus a
// `flagged` custom view placed at display position 2 (right after `active`).
func ringModel(t *testing.T) Model {
	t.Helper()
	compiled, err := CompileViews([]ViewSpec{
		{Name: "flagged", Filter: "flagged", Position: 2},
		{Name: "ticketed", Filter: "ticketed and not archived"},
	})
	if err != nil {
		t.Fatalf("CompileViews: %v", err)
	}
	return Model{width: 80, height: 20}.WithViews(compiled)
}

func withRing(t *testing.T, m Model, names ...string) Model {
	t.Helper()
	m, err := m.WithViewRing(names)
	if err != nil {
		t.Fatalf("WithViewRing(%v): %v", names, err)
	}
	return m
}

// ring returns the view after pressing view-ring once, without running the
// refetch cmd (no daemon in a unit test).
func ringOnce(t *testing.T, m Model) Model {
	t.Helper()
	nm, _ := m.cycleViewRing()
	return nm.(Model)
}

// THE CONTRACT: one key, two views, back and forth — the cockpit's Shift+F12.
func TestViewRingFlipsBetweenItsViews(t *testing.T) {
	m := withRing(t, ringModel(t), "active", "flagged")
	flagged, err := m.viewByName("flagged")
	if err != nil {
		t.Fatalf("viewByName: %v", err)
	}
	m.view = ViewActive

	m = ringOnce(t, m)
	if m.view != flagged {
		t.Fatalf("first press: view = %q, want flagged", m.viewNameAt(int(m.view)))
	}
	m = ringOnce(t, m)
	if m.view != ViewActive {
		t.Fatalf("second press: view = %q, want active — the ring must come BACK, not walk on", m.viewNameAt(int(m.view)))
	}
	if m.ActionErr() != nil {
		t.Errorf("a working ring must not set an error: %v", m.ActionErr())
	}
}

// From a view the ring does not mention, the only answer that does not depend on
// where you came from is the ring's FIRST entry.
func TestViewRingEntersAtTheFirstEntryFromOutside(t *testing.T) {
	m := withRing(t, ringModel(t), "active", "flagged")
	for _, start := range []View{ViewArchived, ViewHold, ViewAll} {
		mm := m
		mm.view = start
		mm = ringOnce(t, mm)
		if mm.view != ViewActive {
			t.Errorf("from %q the ring entered at %q, want active (its first entry)",
				m.viewNameAt(int(start)), mm.viewNameAt(int(mm.view)))
		}
	}
}

// A ring of three keeps stepping round, so the mechanism is not secretly a
// two-state toggle that happens to be configured with two names.
func TestViewRingOfThreeCycles(t *testing.T) {
	m := withRing(t, ringModel(t), "active", "flagged", "archived")
	m.view = ViewActive
	want := []string{"flagged", "archived", "active", "flagged"}
	for i, w := range want {
		m = ringOnce(t, m)
		if got := m.viewNameAt(int(m.view)); got != w {
			t.Fatalf("press %d landed on %q, want %q", i+1, got, w)
		}
	}
}

// An unconfigured ring REFUSES LOUDLY. The command is palette-reachable on every
// machine, and a key that silently does nothing cannot be told from a broken one.
func TestViewRingUnconfiguredIsLoud(t *testing.T) {
	m := ringModel(t)
	m.view = ViewActive
	m = ringOnce(t, m)
	if m.ActionErr() == nil {
		t.Fatal("an unconfigured ring must set a loud error, not do nothing")
	}
	if !strings.Contains(m.ActionErr().Error(), "view_ring") {
		t.Errorf("the refusal must name the setting to fix; got %q", m.ActionErr())
	}
	if m.view != ViewActive {
		t.Errorf("a refused flip must not change the view")
	}
}

// Name resolution is loud on every shape that could otherwise flip between the
// WRONG views: unknown, ambiguous (two configured views share a name) and a
// repeat (which would make "next" depend on which match was found first).
func TestViewRingNameErrors(t *testing.T) {
	m := ringModel(t)

	if _, err := m.WithViewRing([]string{"active", "flagge"}); err == nil {
		t.Error("an unknown view name must refuse")
	} else if !strings.Contains(err.Error(), "flagge") || !strings.Contains(err.Error(), "flagged") {
		t.Errorf("the refusal must name the typo AND list the valid views; got %q", err)
	}
	if _, err := m.WithViewRing([]string{"active", "flagged", "active"}); err == nil {
		t.Error("a repeated view must refuse — a ring visits each view once")
	}

	dup, err := CompileViews([]ViewSpec{{Name: "dup", Filter: "flagged"}, {Name: "dup", Filter: "archived"}})
	if err != nil {
		t.Fatalf("CompileViews: %v", err)
	}
	if _, err := (Model{}).WithViews(dup).WithViewRing([]string{"dup"}); err == nil {
		t.Error("an ambiguous view name must refuse rather than pick one")
	}

	mm, err := m.WithViewRing(nil)
	if err != nil || mm.viewRing != nil {
		t.Errorf("an empty ring is not an error, it is simply unconfigured (got %v, ring %v)", err, mm.viewRing)
	}
}

// THE PATH THE COCKPIT ACTUALLY USES: Shift+F12 injects a key into the sidebar
// pane with `tmux send-keys`, and that key is bound to view-ring through
// [[tui.key]]. So the binding must resolve AND the F-key must dispatch — a
// command that only works from the palette would be useless to the binding.
func TestViewRingIsReachableFromAConfiguredFKey(t *testing.T) {
	km, err := ResolveKeymap([]KeySpec{{Command: "view-ring", Key: "f12"}})
	if err != nil {
		t.Fatalf("[[tui.key]] view-ring = f12 must resolve: %v", err)
	}
	if got := km.Command("f12"); got != "view-ring" {
		t.Fatalf("f12 runs %q, want view-ring", got)
	}
	m := withRing(t, ringModel(t), "active", "flagged").WithKeymap(km)
	m.view = ViewActive
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyF12})
	if got := nm.(Model).viewNameAt(int(nm.(Model).view)); got != "flagged" {
		t.Fatalf("the injected f12 landed on %q, want flagged", got)
	}
}
