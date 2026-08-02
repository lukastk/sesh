package tui

// The fzf-style filter (PARITY_ROADMAP A3, v1's apparatus ported whole):
// `/` enters filter mode; the query narrows the grid LIVE, ranked by fuzzy
// score (best first — v2 is top-down, v1 was fzf-reverse); matched runes are
// highlighted; Esc APPLIES the filter and returns to normal mode (the query
// stays active; `/` re-edits it); Enter navs to the selected match; ctrl+t
// toggles the search target between the match columns (name + cwd label) and
// the full uuid; the prompt shows the live `matched/total` count and the
// target with its `^t→` hint. Caret editing: ←/→/home/end/ctrl+a/ctrl+e,
// insert-at-caret, backspace-before-caret. (When A5's tree lands, ←/→ at the
// query boundaries fold/unfold the selected node, per v1.)

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// filterTarget selects what the query matches against.
type filterTarget int

const (
	targetCols filterTarget = iota // name + cwd label (the default)
	targetUUID                     // the full thread uuid
)

func (t filterTarget) label() string {
	if t == targetUUID {
		return "uuid"
	}
	return "name+cwd"
}

func (t filterTarget) other() string {
	if t == targetUUID {
		return "name+cwd"
	}
	return "uuid"
}

// rowMatch is one visible row with its match metadata (score for ranking,
// matched rune positions per column name for highlighting).
type rowMatch struct {
	row   api.ThreadRow
	score int
	pos   map[string][]int
}

// treeRow is one RENDERED row: a match plus its tree prefix (fold marker +
// rails). Prefixes are empty while a filter query is active (flat ranked list,
// per v1).
type treeRow struct {
	rowMatch
	prefix string
}

// matchRow scores a row against the active query. Cols target: each match
// column (name, cwd label) is scored independently — a row matches if ANY
// column matches; the best column score ranks it. UUID target: the full uuid
// (highlight clipped to the visible tid8 cell).
func (m *Model) matchRow(row api.ThreadRow) (rowMatch, bool) {
	q := m.filter
	if q == "" {
		return rowMatch{row: row}, true
	}
	if m.target == targetUUID {
		res := fuzzyScore(q, row.ID)
		if !res.ok {
			return rowMatch{}, false
		}
		var clipped []int
		for _, p := range res.pos {
			if p < 8 {
				clipped = append(clipped, p)
			}
		}
		return rowMatch{row: row, score: res.score, pos: map[string][]int{ColID: clipped}}, true
	}
	best, ok := 0, false
	pos := map[string][]int{}
	if res := fuzzyScore(q, row.Name); res.ok {
		ok = true
		pos[ColName] = res.pos
		best = res.score
	}
	if res := fuzzyScore(q, m.cwdDisplay(row)); res.ok {
		ok = true
		pos[ColCwd] = res.pos
		if res.score > best {
			best = res.score
		}
	}
	if !ok {
		return rowMatch{}, false
	}
	return rowMatch{row: row, score: best, pos: pos}, true
}

// visibleMatches applies the active filter + tree fold state to the fetched
// rows — THE render order; the cursor indexes into it. A filter query = a flat
// list ranked best-first (v1). No query = the parent/child TREE: children nest
// under their VISIBLE parent (a child whose parent is filtered out or on
// another view promotes to a root), collapsed parents hide their subtree,
// prefixes carry the ▾/▸ fold marker and ├/└/│ rails.
func (m *Model) visibleMatches() []treeRow {
	matched := make([]rowMatch, 0, len(m.rows))
	for _, r := range m.rows {
		if rm, ok := m.matchRow(r); ok {
			matched = append(matched, rm)
		}
	}
	if m.filter != "" {
		// By default a query searches EVERY thread, nested or not. ^y toggles the
		// exclusion on to drop child threads, for when a deep tree makes a name
		// search noisy.
		//
		// This defaulted the other way until 2026-08-02 and it was a trap: a uuid
		// search — an exact, unambiguous identifier — silently returned nothing for
		// any thread that happened to be a child, and supervisor/worker trees make
		// most interesting threads children. Search finding less than it was asked
		// for must be something the user opts INTO.
		if m.filterExcludeChildren {
			kept := matched[:0]
			for _, rm := range matched {
				if rm.row.Parent != "" {
					continue
				}
				kept = append(kept, rm)
			}
			matched = kept
		}
		sort.SliceStable(matched, func(i, j int) bool {
			if matched[i].score != matched[j].score {
				return matched[i].score > matched[j].score // best first (v2 is top-down)
			}
			return matched[i].row.Name < matched[j].row.Name
		})
		out := make([]treeRow, len(matched))
		for i, rm := range matched {
			out[i] = treeRow{rowMatch: rm}
		}
		return out
	}

	// Tree walk (v1's orderedRows). Sibling order = fetch order (machine, name, id).
	inSet := map[string]rowMatch{}
	order := make([]string, 0, len(matched))
	for _, rm := range matched {
		inSet[rm.row.ID] = rm
		order = append(order, rm.row.ID)
	}
	children := map[string][]string{}
	var roots []string
	for _, id := range order {
		if p := inSet[id].row.Parent; p != "" {
			if _, visible := inSet[p]; visible {
				children[p] = append(children[p], id)
				continue
			}
		}
		roots = append(roots, id)
	}
	// Manual ordering: PINNED roots render first, ordered by their fractional key
	// (then machine, id), as a block ABOVE the auto-sorted roots. Unpinned roots keep
	// their existing (machine, name, id) order — a stable sort preserves it. Only
	// top-level nodes are pinned, so this reorders the tree's ROOTS only; each pinned
	// root still carries its subtree. (Children are never pinned — pin_order is cleared
	// on reparent-to-child — so sibling order within a subtree is untouched.)
	sort.SliceStable(roots, func(i, j int) bool {
		ri, rj := inSet[roots[i]].row, inSet[roots[j]].row
		pi, pj := ri.PinOrder != nil, rj.PinOrder != nil
		if pi != pj {
			return pi // pinned before unpinned
		}
		if pi && pj {
			if *ri.PinOrder != *rj.PinOrder {
				return *ri.PinOrder < *rj.PinOrder
			}
			if ri.Machine != rj.Machine {
				return ri.Machine < rj.Machine
			}
			return ri.ID < rj.ID
		}
		return false // both unpinned: keep the stable (fetch) order
	})
	// flaggedUnder collects flagged descendants of id (ANY depth), in walk
	// order — the fold-piercing set (ticket df4fb07a).
	var flaggedUnder func(id string) []string
	flaggedUnder = func(id string) []string {
		var got []string
		for _, k := range children[id] {
			if inSet[k].row.Flagged {
				got = append(got, k)
			}
			got = append(got, flaggedUnder(k)...)
		}
		return got
	}
	var out []treeRow
	var walk func(id, indent string, isLast, isRoot bool)
	walk = func(id, indent string, isLast, isRoot bool) {
		prefix := indent
		if !isRoot {
			if isLast {
				prefix += "└ "
			} else {
				prefix += "├ "
			}
		}
		kids := children[id]
		if len(kids) > 0 {
			if m.isExpanded(id) {
				prefix += "▾ "
			} else {
				prefix += "▸ "
			}
		}
		out = append(out, treeRow{rowMatch: inSet[id], prefix: prefix})
		childIndent := indent
		if !isRoot {
			if isLast {
				childIndent += "  "
			} else {
				childIndent += "│ "
			}
		}
		if len(kids) > 0 && m.isExpanded(id) {
			for i, k := range kids {
				walk(k, childIndent, i == len(kids)-1, false)
			}
		} else if len(kids) > 0 {
			// FOLD-PIERCING: flagged descendants stay visible under a
			// collapsed parent — a flag must never hide inside a fold. They
			// render as direct rails of the collapsed node (intermediate
			// unflagged ancestry elided; the ▸ marker says more is hidden).
			pierced := flaggedUnder(id)
			for i, k := range pierced {
				rail := "├ "
				if i == len(pierced)-1 {
					rail = "└ "
				}
				out = append(out, treeRow{rowMatch: inSet[k], prefix: childIndent + rail})
			}
		}
	}
	for _, r := range roots {
		walk(r, "", true, true)
	}
	return out
}

// isExpanded reports a node's fold state (per-node override, else the
// configured default — children start collapsed unless [tui] expand_children
// or --expand).
func (m *Model) isExpanded(id string) bool {
	if v, ok := m.expanded[id]; ok {
		return v
	}
	return m.defaultExpand
}

// foldSelected sets the selected row's fold state (→ expand, ← collapse).
func (m *Model) foldSelected(expand bool) {
	if tr, ok := m.selectedTree(); ok {
		if m.expanded == nil {
			m.expanded = map[string]bool{}
		}
		m.expanded[tr.row.ID] = expand
	}
}

func (m *Model) selectedTree() (treeRow, bool) {
	vis := m.visibleMatches()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return treeRow{}, false
	}
	return vis[m.cursor], true
}

// WithExpand sets the default fold state (--expand / [tui] expand_children).
func (m Model) WithExpand(expand bool) Model {
	m.defaultExpand = expand
	return m
}

// handleFilterKey handles keys while the filter prompt is being edited.
func (m Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		// APPLY: keep the query active, return to normal navigation (v1).
		m.filtering = false
		return m, nil
	case "enter":
		// SIDEBAR mode: entering a thread from search must LEAVE search — the
		// TUI persists (unlike the popup, which quits on nav), and coming back
		// to a sidebar still narrowed to a stale query reads as broken (Lukas).
		// The filter is CLEARED (not applied-and-kept): the search was a jump,
		// the ambient list should be whole again. Order is load-bearing: the
		// nav cmd captures the FILTERED selection first, then the filter drops
		// and the cursor re-lands on that same thread in the full list.
		if m.sidebar {
			row, ok := m.Selected()
			cmd := m.navSelected()
			m.filtering, m.filter, m.filterCaret = false, "", 0
			if ok {
				m.positionCursorOn(row.ID)
			}
			m.clampCursor()
			return m, cmd
		}
		cmd := m.navSelected()
		return m, cmd
	case "ctrl+y":
		// Toggle whether child threads are excluded from the filter results.
		// (^y, not ^k — ^k stays the symmetric "move selection up" of ^j.)
		m.filterExcludeChildren = !m.filterExcludeChildren
		m.clampCursor()
		return m, nil
	case "up", "ctrl+k":
		m.moveCursor(-1)
		return m, nil
	case "down", "ctrl+j":
		m.moveCursor(1)
		return m, nil
	case "left":
		// Caret left; AT the start, collapse the selected node instead (v1 —
		// with an empty query the caret is at both ends, so ← just collapses).
		if m.filterCaret > 0 {
			m.filterCaret--
		} else {
			m.foldSelected(false)
		}
		return m, nil
	case "right":
		// Caret right; AT the end, expand instead.
		if m.filterCaret < len([]rune(m.filter)) {
			m.filterCaret++
		} else {
			m.foldSelected(true)
		}
		return m, nil
	case "home", "ctrl+a":
		m.filterCaret = 0
		return m, nil
	case "end", "ctrl+e":
		m.filterCaret = len([]rune(m.filter))
		return m, nil
	case "ctrl+t":
		if m.target == targetUUID {
			m.target = targetCols
		} else {
			m.target = targetUUID
		}
		m.cursor = 0
		return m, nil
	case "tab":
		// Same as normal mode: open the view PICKER (on the current view). It
		// dispatches above the filter, so its keys work mid-filter and the
		// filter state survives the switch.
		m.viewPicker = true
		m.viewPickerCursor = m.viewPos(m.view)
		return m, nil
	case "backspace":
		r := []rune(m.filter)
		if m.filterCaret > 0 && m.filterCaret <= len(r) {
			m.filter = string(r[:m.filterCaret-1]) + string(r[m.filterCaret:])
			m.filterCaret--
			m.cursor = 0
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		s := string(msg.Runes)
		if msg.Type == tea.KeySpace {
			s = " "
		}
		r := []rune(m.filter)
		if m.filterCaret > len(r) {
			m.filterCaret = len(r)
		}
		m.filter = string(r[:m.filterCaret]) + s + string(r[m.filterCaret:])
		m.filterCaret += len([]rune(s))
		m.cursor = 0
	}
	return m, nil
}

// moveCursor moves the selection over the VISIBLE rows, wrapping.
func (m *Model) moveCursor(delta int) {
	n := len(m.visibleMatches())
	if n == 0 {
		m.cursor = 0
		return
	}
	m.cursor = (m.cursor + delta + n) % n
}

// clampCursor keeps the cursor within the current visible range after the row set
// changes size (e.g. toggling filterExcludeChildren).
func (m *Model) clampCursor() {
	n := len(m.visibleMatches())
	switch {
	case n == 0:
		m.cursor = 0
	case m.cursor >= n:
		m.cursor = n - 1
	case m.cursor < 0:
		m.cursor = 0
	}
}

// renderFilterPrompt renders the filter line: `> query` with a caret block,
// and right-aligned the target label, its ^t hint, and the match count.
func (m *Model) renderFilterPrompt(matched, total int) string {
	r := []rune(m.filter)
	if m.filterCaret > len(r) {
		m.filterCaret = len(r)
	}
	q := string(r[:m.filterCaret]) + "█" + string(r[m.filterCaret:])
	left := "> " + q
	kids := "on"
	if m.filterExcludeChildren {
		kids = "off"
	}
	right := fmt.Sprintf("%s ^t→%s  ^y children:%s  %d/%d", m.target.label(), m.target.other(), kids, matched, total)
	gap := m.width - len([]rune(left)) - len([]rune(right))
	if gap < 1 {
		gap = 1
	}
	return styleHeader.Render(left) + strings.Repeat(" ", gap) + styleDim.Render(right)
}

// highlight styles the matched rune positions of cell (its plain text) bold.
// Positions beyond the cell (clipped by truncation) are ignored.
func highlight(cell string, pos []int) string {
	if len(pos) == 0 {
		return cell
	}
	want := map[int]bool{}
	for _, p := range pos {
		want[p] = true
	}
	var b strings.Builder
	for i, ru := range []rune(cell) {
		if want[i] {
			b.WriteString(styleMatch.Render(string(ru)))
		} else {
			b.WriteRune(ru)
		}
	}
	return b.String()
}

// WithFilterStart starts the TUI in filter mode (`tui --filter`).
func (m Model) WithFilterStart() Model {
	m.filtering = true
	return m
}

// Filtering reports whether the filter prompt is being edited (for tests).
func (m Model) Filtering() bool { return m.filtering }

// FilterQuery exposes the active query (for tests).
func (m Model) FilterQuery() string { return m.filter }
