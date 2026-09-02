package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// GO TO A THREAD BY UUID (`goto-uuid`, palette-only): a line prompt takes a thread's
// uuid — the full 36-char one, or the short prefix the ID column and the CLI's
// --id use — and moves the CURSOR onto that thread.
//
// WHICH VIEW. The current one when it already shows the thread — the cursor just
// moves, no view churn while you are browsing (Lukas: "it should move the cursor to
// that thread"). Otherwise the FIRST view in display order (active → on hold →
// archived → all → the custom [[tui.views]]) that admits it. `all` admits
// everything, so a thread the grid can see always has a view to land in.
//
// IT LOCATES, IT DOES NOT ENTER: `enter` remains the command that navs into a
// thread; goto only puts the selection on it (and then Enter/`enter` is one key).
//
// EVERY FAILURE IS LOUD. A uuid that matches nothing, matches several threads, or
// matches a thread the grid deliberately hides (an OFFLINE machine's, with
// hide-offline on; a peer's, on a self-only grid; one the active filter drops) is
// refused with a message naming the reason and the way out. Deliberately no silent
// flipping of a display setting, and deliberately no pending preselect left behind:
// a preselect that cannot land would sit armed and jump the cursor minutes later
// when the machine reconnects, which is exactly the kind of surprise this TUI's
// selection anchoring exists to prevent.

// uuidMaxLen is the length of a canonical thread uuid — the longest a prefix can be.
const uuidMaxLen = 36

// gotoMatch is one thread whose uuid starts with the typed prefix, carrying the
// visibility facts a refusal has to explain (its machine's self/reachable state).
type gotoMatch struct {
	row       api.ThreadRow
	machine   string
	self      bool
	reachable bool
}

// visible reports whether the grid's MACHINE-level filters would show this match:
// a peer needs --all-machines, and an unreachable peer needs the offline toggle.
// (Self is never hidden by either.)
func (g gotoMatch) visible(allMachines, hideOffline bool) bool {
	if !allMachines && !g.self {
		return false
	}
	if hideOffline && !g.self && !g.reachable {
		return false
	}
	return true
}

// gotoMatches returns every thread in the LAST-FETCHED mesh whose uuid starts with
// prefix, across all machines — including ones the grid is currently hiding, so the
// caller can say WHY a match is unreachable instead of the useless "no such thread".
func (m Model) gotoMatches(prefix string) []gotoMatch {
	var out []gotoMatch
	for _, mv := range m.machines {
		for _, t := range mv.Threads {
			if strings.HasPrefix(strings.ToLower(t.ID), prefix) {
				out = append(out, gotoMatch{row: meshRow(t), machine: mv.Machine, self: mv.Self, reachable: mv.Reachable})
			}
		}
	}
	return out
}

// normalizeGotoUUID lowercases and trims a typed uuid, and refuses anything that
// cannot be a uuid prefix. Uuids are hex digits and dashes, so a name typed by
// mistake gets a message saying what the prompt wants rather than a confusing
// "no thread's uuid starts with "dagster"".
func normalizeGotoUUID(input string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(input))
	if s == "" {
		return "", nil // the caller treats an empty submit as cancel
	}
	if len(s) > uuidMaxLen {
		return "", fmt.Errorf("goto: %q is longer than a uuid (%d characters)", input, uuidMaxLen)
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-' {
			continue
		}
		return "", fmt.Errorf("goto: %q is not a uuid or uuid prefix — uuids are hex digits and dashes (the short form is the 8-character id the ID column shows)", input)
	}
	return s, nil
}

// viewAdmits reports whether view v shows row — the built-in rule, or the custom
// view's own predicate. It mirrors exactly what fetch() applies, so goto can only
// pick a view the grid will really render the thread in.
func (m Model) viewAdmits(v View, row api.ThreadRow) bool {
	if i := int(v - viewBuiltins); i >= 0 && i < len(m.customViews) {
		return m.customViews[i].pred.Eval(row)
	}
	return builtinViewAdmits(v, row)
}

// firstViewShowing picks the view to jump to: the CURRENT one when it already shows
// the row (nothing to switch — just move the cursor), else the first view in
// DISPLAY order that admits it. ViewAll admits everything and is always in the
// order, so ok is false only if the view set ever stops containing it — reported
// rather than papered over.
func (m Model) firstViewShowing(row api.ThreadRow) (View, bool) {
	if m.viewAdmits(m.view, row) {
		return m.view, true
	}
	for _, v := range m.orderedViews() {
		if m.viewAdmits(v, row) {
			return v, true
		}
	}
	return m.view, false
}

// filterHides reports why the ACTIVE filter would keep row off the grid (the query
// not matching it, or ^y's child exclusion dropping it), or "" when it wouldn't.
// A filter narrows EVERY view, so unlike a view mismatch this cannot be solved by
// switching views — goto refuses and names it.
func (m Model) filterHides(row api.ThreadRow) string {
	if m.filter == "" {
		return ""
	}
	mm := m // matchRow takes a pointer receiver but only READS the filter state
	if _, ok := mm.matchRow(row); !ok {
		return fmt.Sprintf("the active filter %q (target %s) hides it — clear the filter first", m.filter, m.target.label())
	}
	if m.filterExcludeChildren && row.Parent != "" {
		return "the filter's child exclusion (^y) hides it — toggle it off first"
	}
	return ""
}

// gotoUUID resolves the typed uuid/prefix and lands the cursor on that thread,
// switching view when the current one doesn't show it. Returns the model plus any
// refetch the jump needs.
func (m Model) gotoUUID(input string) (Model, tea.Cmd) {
	m.actionErr, m.note = nil, ""
	prefix, err := normalizeGotoUUID(input)
	if err != nil {
		m.actionErr = err
		return m, nil
	}
	if prefix == "" {
		return m, nil // empty submit = cancel
	}

	matches := m.gotoMatches(prefix)
	var machineVisible, shown []gotoMatch
	for _, g := range matches {
		if !g.visible(m.allMachines, m.hideOffline) {
			continue
		}
		machineVisible = append(machineVisible, g)
		if m.cwdScope == nil || m.cwdScope.admits(g.row) {
			shown = append(shown, g)
		}
	}
	switch {
	case len(shown) > 1:
		m.actionErr = fmt.Errorf("goto: %d threads' uuids start with %q (%s) — type more of it", len(shown), prefix, gotoMatchLabels(shown))
		return m, nil
	case len(shown) == 0 && len(machineVisible) == 1:
		g := machineVisible[0]
		m.actionErr = fmt.Errorf("goto: %s %q is outside this TUI's launch CWD scope (%s)", tid8(g.row.ID), rowDisplayName(g.row), m.cwdScope.describe())
		return m, nil
	case len(shown) == 0 && len(machineVisible) > 1:
		m.actionErr = fmt.Errorf("goto: %d threads' uuids start with %q, but all are outside this TUI's launch CWD scope (%s) — type more of it", len(machineVisible), prefix, m.cwdScope.describe())
		return m, nil
	case len(shown) == 0 && len(matches) > 0:
		m.actionErr = gotoHiddenErr(prefix, matches, m.machine)
		return m, nil
	case len(shown) == 0:
		m.actionErr = fmt.Errorf("goto: no thread's uuid starts with %q", prefix)
		return m, nil
	}

	row := shown[0].row
	if why := m.filterHides(row); why != "" {
		m.actionErr = fmt.Errorf("goto: %s (%s): %s", tid8(row.ID), rowDisplayName(row), why)
		return m, nil
	}
	v, ok := m.firstViewShowing(row)
	if !ok {
		m.actionErr = fmt.Errorf("goto: no view shows %s (%s)", tid8(row.ID), rowDisplayName(row))
		return m, nil
	}
	if v != m.view {
		// The target view's rows aren't fetched yet, so the cursor is placed by the
		// PRESELECT path in the meshMsg handler (which also expands a nested
		// thread's ancestors so a collapsed child is really visible). The note says
		// why the view changed under you.
		m.view, m.preselectID, m.cursor = v, row.ID, 0
		m.note = fmt.Sprintf("go to %s %q · switched to the %s view", tid8(row.ID), rowDisplayName(row), m.viewNameAt(int(v)))
		return m, m.fetch()
	}
	if !m.positionCursorOn(row.ID) {
		// Admitted by this view and present in the mesh, but not in the rendered
		// rows yet (a just-published thread, or a fetch in flight): keep it as a
		// preselect and refetch, the same path the master-cursor jump uses.
		m.preselectID = row.ID
		return m, m.fetch()
	}
	m.ensureCursorVisible()
	return m, nil
}

// gotoMatchLabels names the ambiguous matches (short id + name) so "type more of
// it" is actionable — you can see which threads you are between.
func gotoMatchLabels(ms []gotoMatch) string {
	const max = 4
	out := make([]string, 0, max+1)
	for i, g := range ms {
		if i == max {
			out = append(out, fmt.Sprintf("… +%d more", len(ms)-max))
			break
		}
		out = append(out, fmt.Sprintf("%s %q", tid8(g.row.ID), rowDisplayName(g.row)))
	}
	return strings.Join(out, ", ")
}

// gotoHiddenErr explains a match the grid is deliberately hiding, and how to
// reveal it. Both causes are machine-level display settings: a self-only grid
// (--all-machines is a startup flag) and the offline hide (the `toggle-offline`
// command). An offline machine's threads can't be entered or mutated anyway, so
// this refuses rather than revealing them behind the user's back.
func gotoHiddenErr(prefix string, matches []gotoMatch, localMachine string) error {
	g := matches[0]
	label := fmt.Sprintf("%s %q", tid8(g.row.ID), rowDisplayName(g.row))
	if len(matches) > 1 {
		// Several hidden threads share the prefix; name the first and say so, rather
		// than reporting one machine's reason as if it were the only match.
		label = fmt.Sprintf("%s (+%d more hidden thread(s) match %q)", label, len(matches)-1, prefix)
	}
	if !g.reachable && !g.self {
		return fmt.Errorf("goto: %s is on %s, which is OFFLINE — run the toggle-offline command to show offline machines' threads", label, g.machine)
	}
	local := localMachine
	if local == "" {
		local = "this machine"
	}
	return fmt.Errorf("goto: %s is on %s — this grid shows only %s (start it with --all-machines)", label, g.machine, local)
}
