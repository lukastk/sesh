package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// The INTERACTIVE REPARENT PICKER (`set-parent`): run it on the CHILD, then pick
// the thread it should hang under and press Enter. The uuid form
// (`set-parent-uuid`) stays — pasting an id is still the right move when you
// already have one — but picking is what you want when you don't.
//
// The candidate list is deliberately narrow, so the picker can never offer a
// choice the daemon will refuse:
//
//   - SAME MACHINE ONLY. A parent is validated against the OWNER's local store, so
//     cross-machine parenting does not exist (H37); offering another machine's
//     threads would just produce a routed failure a second later.
//   - NOT the child, and NOT any of its descendants — that would be a cycle.
//   - Plus a "(root)" entry when the child currently has a parent, which detaches
//     it — the same thing an empty uuid prompt does.

// parentPickRowsTop is the terminal row the first candidate renders on. The chrome
// above it is fixed (title, query, the ▲ indicator) so a click maps back to a row;
// parentPickView must mirror this.
const parentPickRowsTop = 3

// parentPickChrome is the number of non-candidate lines (title, query, the two
// scroll indicators, the footer).
const parentPickChrome = 5

// rootParentID is the sentinel candidate id meaning "no parent" (detach to root).
// A real thread id is a uuid, so it can never collide.
const rootParentID = ""

// parentCandidate is one pickable target: a thread, or the root sentinel.
type parentCandidate struct {
	id    string // "" = detach to root
	label string
	row   api.ThreadRow
	root  bool
	pos   []int // matched rune positions in label (highlighting)
	score int
}

// openParentPick opens the picker for the selected row. Refuses loudly when there
// is nothing to reparent, or when the thread is a divider (an inert rule that
// cannot own or be owned).
func (m Model) openParentPick() (tea.Model, tea.Cmd) {
	row, ok := m.Selected()
	if !ok {
		m.note = "no thread selected"
		return m, nil
	}
	if row.AgentKind == api.DividerAgentKind {
		m.actionErr = fmt.Errorf("a divider can't be reparented — it is a rule in the pinned block, not a thread")
		return m, nil
	}
	m.parentPick, m.parentPickRow = true, row
	m.parentPickQuery, m.parentPickCursor, m.parentPickOffset = nil, 0, 0
	return m, nil
}

func (m *Model) closeParentPick() {
	m.parentPick = false
	m.parentPickQuery, m.parentPickCursor, m.parentPickOffset = nil, 0, 0
}

// descendantsOf returns the set of thread ids at or below root, walking the
// parent links of the currently loaded rows. Used to keep the picker from
// offering a cycle.
func (m Model) descendantsOf(rootID string) map[string]bool {
	children := make(map[string][]string, len(m.rows))
	for _, r := range m.rows {
		if r.Parent != "" {
			children[r.Parent] = append(children[r.Parent], r.ID)
		}
	}
	seen := map[string]bool{rootID: true}
	stack := []string{rootID}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range children[id] {
			if !seen[c] {
				seen[c] = true
				stack = append(stack, c)
			}
		}
	}
	return seen
}

// parentCandidates lists the pickable parents for the captured child, filtered by
// the query and ranked best-first. An empty query keeps the grid's own order
// (machine/name sorted), so the picker opens as the list you were just looking at.
func (m Model) parentCandidates() []parentCandidate {
	child := m.parentPickRow
	excluded := m.descendantsOf(child.ID)
	q := strings.TrimSpace(string(m.parentPickQuery))

	var out []parentCandidate
	// Detach-to-root first, and only when it would change anything.
	if child.Parent != "" {
		root := parentCandidate{id: rootParentID, label: "(root — no parent)", root: true}
		if r := fuzzyScore(q, root.label); r.ok {
			root.pos, root.score = r.pos, r.score
			out = append(out, root)
		}
	}
	for _, r := range m.rows {
		if excluded[r.ID] || r.Machine != child.Machine {
			continue
		}
		if r.AgentKind == api.DividerAgentKind {
			continue // a divider is a rule, not a node anything can hang under
		}
		if r.ID == child.Parent {
			continue // already the parent — picking it would be a no-op write
		}
		c := parentCandidate{id: r.ID, label: rowDisplayName(r), row: r}
		if q == "" {
			out = append(out, c)
			continue
		}
		best, matched := c, false
		if res := fuzzyScore(q, c.label); res.ok {
			best.pos, best.score, matched = res.pos, res.score, true
		}
		if res := fuzzyScore(q, r.ID); res.ok && (!matched || res.score > best.score) {
			best.pos, best.score, matched = nil, res.score, true
		}
		if matched {
			out = append(out, best)
		}
	}
	if q != "" {
		sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	}
	return out
}

// handleParentPickKey drives the picker: type to filter, ↑/↓ (or ^k/^j) move,
// Enter reparents onto the highlighted thread, Esc cancels.
func (m Model) handleParentPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cands := m.parentCandidates()
	switch msg.String() {
	case "esc", hardQuitKey:
		m.closeParentPick()
		return m, nil
	case "up", "ctrl+k":
		if len(cands) > 0 {
			m.parentPickCursor = (m.parentPickCursor - 1 + len(cands)) % len(cands)
			m.ensureParentPickVisible(len(cands))
		}
		return m, nil
	case "down", "ctrl+j":
		if len(cands) > 0 {
			m.parentPickCursor = (m.parentPickCursor + 1) % len(cands)
			m.ensureParentPickVisible(len(cands))
		}
		return m, nil
	case "backspace":
		if n := len(m.parentPickQuery); n > 0 {
			m.parentPickQuery = m.parentPickQuery[:n-1]
			m.parentPickCursor, m.parentPickOffset = 0, 0
		}
		return m, nil
	case "enter":
		if m.parentPickCursor < 0 || m.parentPickCursor >= len(cands) {
			// Nothing matched — hold the picker open so the query can be fixed.
			return m, nil
		}
		target := cands[m.parentPickCursor]
		child := m.parentPickRow
		m.closeParentPick()
		// reparentRow routes to the owner and carries the optimistic expand of the
		// new parent, so the moved row is visible where it landed.
		cmd := m.reparentRow(child, target.id)
		return m, cmd
	}
	switch msg.Type {
	case tea.KeyRunes:
		m.parentPickQuery = append(m.parentPickQuery, msg.Runes...)
		m.parentPickCursor, m.parentPickOffset = 0, 0
	case tea.KeySpace:
		m.parentPickQuery = append(m.parentPickQuery, ' ')
		m.parentPickCursor, m.parentPickOffset = 0, 0
	}
	return m, nil
}

// parentPickVisibleRows is how many candidate lines fit (height unknown = all).
func (m Model) parentPickVisibleRows(n int) int {
	return listVisibleRows(n, m.height, parentPickChrome)
}

// ensureParentPickVisible scrolls the window so the cursor stays on screen.
func (m *Model) ensureParentPickVisible(n int) {
	avail := m.parentPickVisibleRows(n)
	m.parentPickOffset = listClampOffset(listEnsureVisible(m.parentPickOffset, m.parentPickCursor, avail), n, avail)
}

// parentPickView renders the picker. Like the palette and the `?` popup, both
// indicator lines are always present so the row geometry is stable while
// scrolling — parentPickRowAtY depends on it.
func (m Model) parentPickView() string {
	cands := m.parentCandidates()
	avail := m.parentPickVisibleRows(len(cands))
	off := m.parentPickOffset
	if max := len(cands) - avail; off > max {
		off = max
	}
	if off < 0 {
		off = 0
	}

	var b strings.Builder
	b.WriteString(styleHeader.Render(fmt.Sprintf("set parent of %q", rowDisplayName(m.parentPickRow))) + "\n")
	b.WriteString(styleDim.Render("  > "+string(m.parentPickQuery)) + "█\n")
	if off > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", off)) + "\n")
	} else {
		b.WriteString("\n")
	}
	for i := off; i < off+avail; i++ {
		c := cands[i]
		id := "        "
		if !c.root {
			id = tid8(c.row.ID)
		}
		if i == m.parentPickCursor {
			b.WriteString(styleSelected.Render(fmt.Sprintf("  %-8s %s", id, c.label)) + "\n")
		} else {
			b.WriteString("  " + styleDim.Render(id) + " " + highlight(c.label, c.pos) + "\n")
		}
	}
	if rest := len(cands) - off - avail; rest > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", rest)) + "\n")
	} else {
		b.WriteString("\n")
	}
	if len(cands) == 0 {
		// Loud about WHY: on a machine with one thread there is genuinely nothing to
		// hang it under, and an empty list with no explanation reads as a bug.
		b.WriteString(styleDim.Render("  no candidate on "+m.parentPickRow.Machine+" (a parent must be another thread on the same machine) · esc cancel") + "\n")
	} else {
		b.WriteString(styleDim.Render("type to filter · ↑/↓ move · enter set parent · esc cancel") + "\n")
	}
	return b.String()
}

// parentPickRowAtY maps a click's terminal row to a candidate index (mirrors
// parentPickView's fixed layout). ok=false outside the list.
func (m Model) parentPickRowAtY(y int) (int, bool) {
	cands := m.parentCandidates()
	avail := m.parentPickVisibleRows(len(cands))
	i := y - parentPickRowsTop + m.parentPickOffset
	if i < m.parentPickOffset || i >= m.parentPickOffset+avail || i >= len(cands) {
		return 0, false
	}
	return i, true
}

// ParentPickOpen reports whether the reparent picker is up (tests).
func (m Model) ParentPickOpen() bool { return m.parentPick }

// ParentPickCandidates lists the picker's current candidate ids, best-first, with
// "" for the detach-to-root entry (tests).
func (m Model) ParentPickCandidates() []string {
	cands := m.parentCandidates()
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.id)
	}
	return out
}
