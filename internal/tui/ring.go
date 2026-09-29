package tui

import "github.com/lukastk/sesh/internal/api"

// FlaggedRing is the cockpit's FLAGGED WORKING SET, in the order the TUI renders it:
// the flagged rows of the built-in `active` view across every machine, in the grid's
// own tree order. It is what `sesh tmux nav --cycle-flagged next|prev` (master
// prefix+, / prefix+.) rotates through (issue #11).
//
// It is computed by the TUI's OWN code, not a reimplementation: flattenMeshRows is the
// grid's fetch path (the active-view predicate, the (machine, name, id) sibling order,
// offline peers hidden) and visibleMatches is its render walk (children under their
// visible parent, an orphan promoted to a root, PINNED roots first by fractional key).
// Until issue #11 myrig carried a jq copy of all of that, which could silently drift
// from filter.go.
//
// FOLD STATE DOES NOT CHANGE THE RESULT, which is why the walk runs fully expanded: a
// collapsed parent hides its unflagged descendants but PIERCES its flagged ones, in walk
// order (flaggedUnder), so the flagged subsequence of the rendered rows is the same for
// every fold state — and the same as the fully expanded walk's.
//
// Offline peers are always hidden here (the grid's default), whatever [tui] show_offline
// says: an unreachable machine's thread cannot be entered, so it can never be a nav
// target. Self is never hidden (flattenMeshRows' rule).
func FlaggedRing(machines []api.MachineView) []api.ThreadRow {
	rows, _ := flattenMeshRows(machines, ViewActive, nil, nil, true, true, "")
	m := Model{rows: rows, defaultExpand: true}
	var ring []api.ThreadRow
	for _, tr := range m.visibleMatches() {
		if tr.row.Flagged {
			ring = append(ring, tr.row)
		}
	}
	return ring
}
