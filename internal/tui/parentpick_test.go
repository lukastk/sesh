package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// pickRow builds a grid row for the picker tests.
func pickRow(id, name, machine, parent string) api.ThreadRow {
	return api.ThreadRow{Thread: api.Thread{ID: id, Name: name, Machine: machine, AgentKind: "pi", Parent: parent}}
}

// pickModel: a small mesh — a mymain tree (root → child → grandchild), a sibling,
// a divider, and a macbook thread — with `child` selected.
func pickModel() Model {
	rows := []api.ThreadRow{
		pickRow("root1", "corkboard", "mymain", ""),
		pickRow("child1", "worker", "mymain", "root1"),
		pickRow("grand1", "sub-worker", "mymain", "child1"),
		pickRow("other1", "unrelated", "mymain", ""),
		{Thread: api.Thread{ID: "div1", Name: "today", Machine: "mymain", AgentKind: api.DividerAgentKind}},
		pickRow("book1", "elsewhere", "macbook", ""),
	}
	return Model{
		machine: "mymain",
		rows:    rows,
		cursor:  1, // `worker`
		machines: []api.MachineView{
			{Machine: "mymain", Self: true, Reachable: true},
			{Machine: "macbook", Reachable: true},
		},
		defaultExpand: true,
	}
}

// The candidate list is exactly what the daemon would ACCEPT: no self, no
// descendants (a cycle), no other machine (parents are validated owner-locally),
// no dividers, not the current parent — plus detach-to-root, since the thread has
// one to detach from.
func TestParentPickCandidates(t *testing.T) {
	m := pickModel()
	mm, _ := m.runCommand("set-parent")
	m = mm.(Model)
	if !m.ParentPickOpen() {
		t.Fatalf("set-parent did not open the picker")
	}
	got := m.ParentPickCandidates()
	want := []string{rootParentID, "other1"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
	// Spell the exclusions out so a regression names the reason it broke.
	for _, bad := range []struct{ id, why string }{
		{"child1", "the thread itself"},
		{"grand1", "a descendant (would be a cycle)"},
		{"root1", "the CURRENT parent (a no-op write)"},
		{"div1", "a divider (a rule, not a node)"},
		{"book1", "another machine (parents are validated owner-locally)"},
	} {
		for _, c := range got {
			if c == bad.id {
				t.Errorf("candidate %q must be excluded: %s", bad.id, bad.why)
			}
		}
	}
}

// A ROOT thread has no "(root)" entry — there is nothing to detach from — and its
// own subtree is still excluded.
func TestParentPickRootThreadHasNoDetachEntry(t *testing.T) {
	m := pickModel()
	m.cursor = 0 // `corkboard`, a root with a subtree
	mm, _ := m.runCommand("set-parent")
	got := mm.(Model).ParentPickCandidates()
	for _, c := range got {
		if c == rootParentID {
			t.Errorf("a root thread should not offer detach-to-root: %v", got)
		}
	}
	if len(got) != 1 || got[0] != "other1" {
		t.Fatalf("candidates = %v, want just [other1] (self + descendants excluded)", got)
	}
}

// Typing narrows the list, and Enter reparents onto the highlighted thread —
// closing the picker and issuing the routed command.
func TestParentPickFilterAndApply(t *testing.T) {
	m := pickModel()
	m.binaryPath = "/nonexistent/sesh" // the cmd is never run, only its existence asserted
	mm, _ := m.runCommand("set-parent")
	m = mm.(Model)

	// "unrel" narrows past the root entry to the one real candidate.
	for _, r := range "unrel" {
		mm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm.(Model)
	}
	if got := m.ParentPickCandidates(); len(got) != 1 || got[0] != "other1" {
		t.Fatalf("filtered candidates = %v, want [other1]", got)
	}
	mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	if m.ParentPickOpen() {
		t.Errorf("enter should close the picker")
	}
	if cmd == nil {
		t.Fatalf("enter should issue the reparent command")
	}
}

// Esc cancels: the picker closes and nothing is reparented.
func TestParentPickEscCancels(t *testing.T) {
	m := pickModel()
	mm, _ := m.runCommand("set-parent")
	m = mm.(Model)
	mm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if got := mm.(Model); got.ParentPickOpen() || cmd != nil {
		t.Errorf("esc should close the picker and run nothing (open=%v cmd=%v)", got.ParentPickOpen(), cmd != nil)
	}
}

// A DIVIDER can't be reparented — refused loudly, with no picker opened.
func TestParentPickDividerRefused(t *testing.T) {
	m := pickModel()
	m.cursor = 4 // the divider
	mm, cmd := m.runCommand("set-parent")
	got := mm.(Model)
	if got.ParentPickOpen() || cmd != nil {
		t.Errorf("a divider must not open the picker")
	}
	if got.ActionErr() == nil {
		t.Errorf("reparenting a divider must refuse loudly")
	}
}

// A machine with nothing else on it yields an EMPTY list — and says why, rather
// than rendering a blank box that reads as a bug.
func TestParentPickEmptyExplains(t *testing.T) {
	m := Model{
		machine:  "mymain",
		rows:     []api.ThreadRow{pickRow("solo", "only-one", "mymain", "")},
		machines: selfMachines(),
	}
	mm, _ := m.runCommand("set-parent")
	m = mm.(Model)
	if got := m.ParentPickCandidates(); len(got) != 0 {
		t.Fatalf("a lone thread should have no candidates, got %v", got)
	}
	strip := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	out := strip.ReplaceAllString(m.View(), "")
	if !strings.Contains(out, "no candidate on mymain") {
		t.Errorf("the empty picker must explain itself:\n%s", out)
	}
}

// The picker takes over View(), names the CHILD it is acting on, and a click on a
// candidate row applies it.
func TestParentPickRenderAndClick(t *testing.T) {
	m := pickModel()
	mm, _ := m.runCommand("set-parent")
	m = mm.(Model)
	strip := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	out := strip.ReplaceAllString(m.View(), "")
	if !strings.Contains(out, `set parent of "worker"`) {
		t.Fatalf("the picker should name the child it acts on:\n%s", out)
	}
	if !strings.Contains(out, "(root — no parent)") || !strings.Contains(out, "unrelated") {
		t.Errorf("the picker should list its candidates:\n%s", out)
	}
	// Click the second candidate row (`unrelated`): applies and closes.
	mm, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 5, Y: parentPickRowsTop + 1})
	if got := mm.(Model); got.ParentPickOpen() || cmd == nil {
		t.Errorf("clicking a candidate should apply it and close the picker (open=%v cmd=%v)", got.ParentPickOpen(), cmd != nil)
	}
	// A click outside the list leaves it open.
	mm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 5, Y: 0})
	if !mm.(Model).ParentPickOpen() {
		t.Errorf("a click outside the list should leave the picker open")
	}
}

// While the picker is up it owns the keyboard: a grid key must not leak through
// to the rows underneath.
func TestParentPickSwallowsGridKeys(t *testing.T) {
	m := pickModel()
	mm, _ := m.runCommand("set-parent")
	m = mm.(Model)
	mm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	got := mm.(Model)
	if got.pendingFor("child1") != nil {
		t.Errorf("`a` typed into the picker query must not archive the thread")
	}
	if string(got.parentPickQuery) != "a" {
		t.Errorf("`a` should have gone into the picker query, got %q", string(got.parentPickQuery))
	}
}
