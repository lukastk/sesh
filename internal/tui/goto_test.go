package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// Unit coverage for the `goto-uuid` command (goto.go): the uuid/prefix lookup, the
// view it lands in, and every refusal. The conformance claim `goto-uuid` drives the
// same thing against a real daemon.

// gsnap builds one mesh snapshot entry (the shape the TUI really fetches).
func gsnap(id, name string, mut func(*api.ThreadSnapshot)) api.ThreadSnapshot {
	t := api.ThreadSnapshot{
		Thread: api.Thread{ID: id, Name: name, Machine: "mymain"},
		Head:   api.Headless,
		Busy:   api.BusyIdle,
	}
	if mut != nil {
		mut(&t)
	}
	return t
}

// gotoModel builds a model whose ROWS are derived from the machines by the REAL
// fetch-path flatten, so the model's view filtering matches the shipped TUI (a
// hand-written row list could disagree with what goto's view lookup sees).
func gotoModel(view View, all, hideOffline bool, machines ...api.MachineView) Model {
	m := Model{machine: "mymain", view: view, allMachines: all, hideOffline: hideOffline,
		machines: machines, width: 120, height: 40}
	m.rows, _ = flattenMeshRows(machines, view, nil, all, hideOffline, "")
	return m
}

func selfMachine(threads ...api.ThreadSnapshot) api.MachineView {
	return api.MachineView{Machine: "mymain", Self: true, Reachable: true, Threads: threads}
}

// The headline: a uuid — full or short — moves the cursor onto that thread when the
// current view already shows it, with NO view switch (Lukas: "it should move the
// cursor to that thread").
func TestGotoUUIDMovesCursorInCurrentView(t *testing.T) {
	const target = "ef79e834-cffd-49d9-b9e7-8683d9916eae"
	m := gotoModel(ViewActive, false, true, selfMachine(
		gsnap("aaa11111-0000-0000-0000-000000000000", "alpha", nil),
		gsnap(target, "zulu", nil),
	))
	for _, typed := range []string{target, "ef79e834", "EF79E834", "  ef79  "} {
		got, cmd := m.gotoUUID(typed)
		if got.actionErr != nil {
			t.Fatalf("goto %q errored: %v", typed, got.actionErr)
		}
		if cmd != nil {
			t.Errorf("goto %q refetched even though the row is already in this view", typed)
		}
		if got.view != ViewActive {
			t.Errorf("goto %q switched away from the view that already shows the thread (view=%s)", typed, got.viewNameAt(int(got.view)))
		}
		if id := got.selectedID(); id != target {
			t.Errorf("goto %q left the cursor on %q, want %q", typed, id, target)
		}
		if got.preselectID != "" {
			t.Errorf("goto %q left a preselect pending (%q) after landing", typed, got.preselectID)
		}
	}
}

// A thread hidden by the current view sends the grid to the FIRST view in display
// order that shows it, with the cursor queued as a preselect for the refetch.
func TestGotoUUIDSwitchesToFirstViewShowing(t *testing.T) {
	archived := gsnap("a1111111-0000-0000-0000-000000000000", "parked-away", func(s *api.ThreadSnapshot) {
		s.Archived = true
	})
	held := gsnap("b2222222-0000-0000-0000-000000000000", "on-ice", func(s *api.ThreadSnapshot) {
		s.OnHold, s.OnHoldEffectiveUnix = true, 1<<40
	})
	live := gsnap("c3333333-0000-0000-0000-000000000000", "working", nil)

	for _, tc := range []struct {
		name, id string
		from     View
		want     View
	}{
		{"archived from active", archived.ID, ViewActive, ViewArchived},
		{"held from active", held.ID, ViewActive, ViewHold},
		{"active from archived", live.ID, ViewArchived, ViewActive},
		{"archived from all stays in all", archived.ID, ViewAll, ViewAll}, // already visible here
	} {
		m := gotoModel(tc.from, false, true, selfMachine(archived, held, live))
		got, cmd := m.gotoUUID(tc.id)
		if got.actionErr != nil {
			t.Fatalf("%s: goto errored: %v", tc.name, got.actionErr)
		}
		if got.view != tc.want {
			t.Errorf("%s: landed in view %q, want %q", tc.name, got.viewNameAt(int(got.view)), got.viewNameAt(int(tc.want)))
		}
		if tc.from == tc.want {
			if got.selectedID() != tc.id {
				t.Errorf("%s: cursor did not move to the thread (got %q)", tc.name, got.selectedID())
			}
			continue
		}
		// A view switch defers the cursor to the refetch: the preselect must be
		// armed AND a fetch returned, or the jump silently does nothing.
		if got.preselectID != tc.id {
			t.Errorf("%s: preselect = %q, want the target %q", tc.name, got.preselectID, tc.id)
		}
		if cmd == nil {
			t.Errorf("%s: view switch returned no refetch — the cursor would never land", tc.name)
		}
		if !strings.Contains(got.note, got.viewNameAt(int(tc.want))) {
			t.Errorf("%s: note %q does not say which view it switched to", tc.name, got.note)
		}
	}
}

// A custom [[tui.views]] view placed FIRST in the display order wins over the
// built-ins — the rule is display order, not built-in order.
func TestGotoUUIDHonoursCustomViewOrder(t *testing.T) {
	pred, err := CompilePredicate("archived")
	if err != nil {
		t.Fatalf("predicate: %v", err)
	}
	archived := gsnap("a1111111-0000-0000-0000-000000000000", "parked", func(s *api.ThreadSnapshot) {
		s.Archived = true
	})
	m := gotoModel(ViewActive, false, true, selfMachine(archived))
	m = m.WithViews([]customView{{name: "attic", pred: pred, position: 1}})
	custom := View(int(viewBuiltins)) // the only custom view
	got, _ := m.gotoUUID(archived.ID)
	if got.actionErr != nil {
		t.Fatalf("goto errored: %v", got.actionErr)
	}
	if got.view != custom {
		t.Errorf("landed in %q, want the custom view %q placed first in the display order", got.viewNameAt(int(got.view)), got.viewNameAt(int(custom)))
	}
}

// A collapsed CHILD is a legitimate target: the jump expands its ancestors so the
// cursor really lands on a visible row (the same rule preselect follows).
func TestGotoUUIDExpandsAncestors(t *testing.T) {
	const child = "cccccccc-0000-0000-0000-000000000000"
	m := gotoModel(ViewActive, false, true, selfMachine(
		gsnap("aaaaaaaa-0000-0000-0000-000000000000", "root", nil),
		gsnap("bbbbbbbb-0000-0000-0000-000000000000", "mid", func(s *api.ThreadSnapshot) {
			s.Parent = "aaaaaaaa-0000-0000-0000-000000000000"
		}),
		gsnap(child, "leaf", func(s *api.ThreadSnapshot) {
			s.Parent = "bbbbbbbb-0000-0000-0000-000000000000"
		}),
	))
	got, _ := m.gotoUUID("cccccccc")
	if got.actionErr != nil {
		t.Fatalf("goto errored: %v", got.actionErr)
	}
	if id := got.selectedID(); id != child {
		t.Errorf("cursor on %q, want the nested child %q (ancestors not expanded?)", id, child)
	}
}

// An ambiguous prefix is refused LOUDLY and changes nothing — never a silent pick.
func TestGotoUUIDAmbiguousPrefixRefuses(t *testing.T) {
	m := gotoModel(ViewActive, false, true, selfMachine(
		gsnap("abcd1111-0000-0000-0000-000000000000", "one", nil),
		gsnap("abcd2222-0000-0000-0000-000000000000", "two", nil),
	))
	before := m.selectedID()
	got, cmd := m.gotoUUID("abcd")
	if got.actionErr == nil {
		t.Fatalf("an ambiguous prefix must be refused loudly")
	}
	for _, want := range []string{"abcd", "one", "two"} {
		if !strings.Contains(got.actionErr.Error(), want) {
			t.Errorf("ambiguity error %q does not name %q", got.actionErr, want)
		}
	}
	if cmd != nil || got.selectedID() != before || got.preselectID != "" || got.view != ViewActive {
		t.Errorf("a refused goto must not move the cursor/view or arm a preselect")
	}
}

// Nothing matching, and input that cannot be a uuid at all, are both loud.
func TestGotoUUIDRefusesUnknownAndNonUUID(t *testing.T) {
	m := gotoModel(ViewActive, false, true, selfMachine(
		gsnap("abcd1111-0000-0000-0000-000000000000", "one", nil),
	))
	got, _ := m.gotoUUID("ffffffff")
	if got.actionErr == nil || !strings.Contains(got.actionErr.Error(), "ffffffff") {
		t.Errorf("unknown uuid: want a loud error naming it, got %v", got.actionErr)
	}
	got, _ = m.gotoUUID("dagster")
	if got.actionErr == nil || !strings.Contains(got.actionErr.Error(), "uuid") {
		t.Errorf("a non-uuid must be refused with a message about uuids, got %v", got.actionErr)
	}
	got, _ = m.gotoUUID(strings.Repeat("a", 40))
	if got.actionErr == nil {
		t.Errorf("an over-long input must be refused")
	}
	// An empty submit is a cancel, not an error.
	got, cmd := m.gotoUUID("   ")
	if got.actionErr != nil || cmd != nil {
		t.Errorf("an empty submit must cancel silently, got err=%v cmd=%v", got.actionErr, cmd != nil)
	}
}

// A thread the grid HIDES by a machine-level display setting is refused with the
// reason and the way out — and, critically, leaves NO preselect armed (one would
// jump the cursor later, when the machine reconnects).
func TestGotoUUIDHiddenByDisplaySettingRefuses(t *testing.T) {
	peerThread := gsnap("dddddddd-0000-0000-0000-000000000000", "far-away", func(s *api.ThreadSnapshot) {
		s.Machine = "macstudio"
	})
	offline := api.MachineView{Machine: "macstudio", Reachable: false, Threads: []api.ThreadSnapshot{peerThread}}
	online := api.MachineView{Machine: "macstudio", Reachable: true, Threads: []api.ThreadSnapshot{peerThread}}

	// hide-offline (the default) + an offline owner: names the machine and the toggle.
	m := gotoModel(ViewActive, true, true, selfMachine(), offline)
	got, cmd := m.gotoUUID("dddddddd")
	if got.actionErr == nil {
		t.Fatalf("a thread on a hidden OFFLINE machine must be refused")
	}
	for _, want := range []string{"macstudio", "OFFLINE", "toggle-offline"} {
		if !strings.Contains(got.actionErr.Error(), want) {
			t.Errorf("offline refusal %q does not mention %q", got.actionErr, want)
		}
	}
	if cmd != nil || got.preselectID != "" {
		t.Errorf("a refused goto must not arm a preselect or refetch (preselect=%q)", got.preselectID)
	}
	// Self-only grid + a peer's thread: names --all-machines.
	m = gotoModel(ViewActive, false, true, selfMachine(), online)
	got, _ = m.gotoUUID("dddddddd")
	if got.actionErr == nil || !strings.Contains(got.actionErr.Error(), "--all-machines") {
		t.Errorf("self-only grid: want a refusal naming --all-machines, got %v", got.actionErr)
	}
	// With the offline machines shown, the same jump works — the refusal really was
	// the display setting and nothing else.
	m = gotoModel(ViewActive, true, false, selfMachine(), offline)
	got, _ = m.gotoUUID("dddddddd")
	if got.actionErr != nil {
		t.Fatalf("with offline machines shown the jump must work, got %v", got.actionErr)
	}
	if got.selectedID() != peerThread.ID {
		t.Errorf("cursor on %q, want the peer thread", got.selectedID())
	}
}

// An active FILTER narrows every view, so it cannot be solved by switching views:
// goto refuses and names it (rather than leaving the cursor stuck where it was
// with no explanation).
func TestGotoUUIDRefusesWhenFilterHides(t *testing.T) {
	target := gsnap("eeeeeeee-0000-0000-0000-000000000000", "zulu", nil)
	base := gotoModel(ViewActive, false, true, selfMachine(
		gsnap("11111111-0000-0000-0000-000000000000", "dagster-run", nil), target))

	m := base
	m.filter = "dagster"
	got, _ := m.gotoUUID("eeeeeeee")
	if got.actionErr == nil || !strings.Contains(got.actionErr.Error(), "dagster") {
		t.Errorf("want a refusal naming the active filter, got %v", got.actionErr)
	}
	// A filter the target DOES match is no obstacle.
	m = base
	m.filter = "zulu"
	got, _ = m.gotoUUID("eeeeeeee")
	if got.actionErr != nil {
		t.Fatalf("a filter the thread matches must not block the jump: %v", got.actionErr)
	}
	if got.selectedID() != target.ID {
		t.Errorf("cursor on %q, want %q", got.selectedID(), target.ID)
	}
	// ^y's child exclusion hides a CHILD even when the query matches it.
	child := gsnap("ffffffff-0000-0000-0000-000000000000", "zulu-child", func(s *api.ThreadSnapshot) {
		s.Parent = target.ID
	})
	m = gotoModel(ViewActive, false, true, selfMachine(target, child))
	m.filter, m.filterExcludeChildren = "zulu", true
	got, _ = m.gotoUUID("ffffffff")
	if got.actionErr == nil || !strings.Contains(got.actionErr.Error(), "^y") {
		t.Errorf("want a refusal naming the ^y child exclusion, got %v", got.actionErr)
	}
}

// The command surface: `goto-uuid` opens a targetless line prompt; typing a uuid and
// pressing Enter performs the jump; Esc and an empty submit both cancel.
func TestGotoCommandPromptFlow(t *testing.T) {
	const target = "ef79e834-cffd-49d9-b9e7-8683d9916eae"
	m := gotoModel(ViewActive, false, true, selfMachine(
		gsnap("aaa11111-0000-0000-0000-000000000000", "alpha", nil),
		gsnap(target, "zulu", nil),
	))
	open := func() Model {
		mm, _ := m.runCommand("goto-uuid")
		got := mm.(Model)
		if got.prompting != promptGoto {
			t.Fatalf("goto-uuid did not open the uuid prompt (prompting=%v)", got.prompting)
		}
		if view := got.View(); !strings.Contains(view, "go to uuid") {
			t.Fatalf("the prompt does not render its label:\n%s", view)
		}
		return got
	}

	// Type the short id, Enter -> the cursor is on the thread.
	got := open()
	for _, r := range "ef79e834" {
		nm, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		got = nm.(Model)
	}
	nm, _ := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = nm.(Model)
	if got.prompting != promptNone {
		t.Errorf("Enter left the prompt open")
	}
	if got.selectedID() != target {
		t.Errorf("cursor on %q, want %q", got.selectedID(), target)
	}

	// Esc cancels: prompt closed, cursor untouched.
	got = open()
	nm, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ef79e834")})
	got = nm.(Model)
	nm, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = nm.(Model)
	if got.prompting != promptNone {
		t.Errorf("Esc did not close the prompt")
	}
	if got.selectedID() == target {
		t.Errorf("Esc performed the jump (must cancel)")
	}

	// An empty submit cancels too, without an error line.
	got = open()
	nm, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = nm.(Model)
	if got.prompting != promptNone || got.actionErr != nil {
		t.Errorf("an empty submit must close the prompt quietly (err=%v)", got.actionErr)
	}
}

// The `?` keymap popup and the palette both list the command (they are generated
// from the registry, so this pins that it really is registered — the surface a
// keyless command is discovered through).
func TestGotoCommandIsListed(t *testing.T) {
	m := gotoModel(ViewActive, false, true, selfMachine())
	m.helpPopup = true
	if v := m.View(); !strings.Contains(v, "go to a thread by UUID") {
		t.Errorf("the ? keymap popup does not list goto-uuid:\n%s", v)
	}
	m.helpPopup = false
	m.openPalette()
	found := false
	for _, c := range m.paletteCandidates() {
		if c.cmd.ID == "goto-uuid" {
			found = true
		}
	}
	if !found {
		t.Errorf("goto-uuid is not offered by the command palette")
	}
}

// The prompt takes NO target thread: it must work on an empty grid (nothing
// selected), where every other prompt has a row to carry.
func TestGotoCommandNeedsNoSelection(t *testing.T) {
	m := gotoModel(ViewActive, false, true, selfMachine())
	if _, ok := m.Selected(); ok {
		t.Fatalf("test setup: expected an empty grid")
	}
	mm, _ := m.runCommand("goto-uuid")
	got := mm.(Model)
	if got.prompting != promptGoto {
		t.Fatalf("goto-uuid must open its prompt with no selection")
	}
	if view := got.View(); !strings.Contains(view, "go to uuid") {
		t.Errorf("prompt not rendered on an empty grid:\n%s", view)
	}
}
