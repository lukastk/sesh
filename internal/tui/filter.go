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
	if res := fuzzyScore(q, m.cwdDisplay(row.Cwd)); res.ok {
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

// visibleMatches applies the active filter to the fetched rows: no query =
// every row in fetch order; a query = matching rows ranked best-first (stable
// by name on ties).
func (m *Model) visibleMatches() []rowMatch {
	out := make([]rowMatch, 0, len(m.rows))
	for _, r := range m.rows {
		if rm, ok := m.matchRow(r); ok {
			out = append(out, rm)
		}
	}
	if m.filter != "" {
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].score != out[j].score {
				return out[i].score > out[j].score // best first (v2 is top-down)
			}
			return out[i].row.Name < out[j].row.Name
		})
	}
	return out
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
		return m, m.navSelected()
	case "up", "ctrl+k":
		m.moveCursor(-1)
		return m, nil
	case "down", "ctrl+j":
		m.moveCursor(1)
		return m, nil
	case "left":
		if m.filterCaret > 0 {
			m.filterCaret--
		}
		return m, nil
	case "right":
		if m.filterCaret < len([]rune(m.filter)) {
			m.filterCaret++
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
		m.view = (m.view + 1) % View(m.viewCount())
		m.cursor = 0
		return m, m.fetch()
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

// renderFilterPrompt renders the filter line: `> query` with a caret block,
// and right-aligned the target label, its ^t hint, and the match count.
func (m *Model) renderFilterPrompt(matched, total int) string {
	r := []rune(m.filter)
	if m.filterCaret > len(r) {
		m.filterCaret = len(r)
	}
	q := string(r[:m.filterCaret]) + "█" + string(r[m.filterCaret:])
	left := "> " + q
	right := fmt.Sprintf("%s ^t→%s  %d/%d", m.target.label(), m.target.other(), matched, total)
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
