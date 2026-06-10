package tui

// The column system (PARITY_ROADMAP A1, ported from v1's columns.go). A column
// is a named spec; the visible set comes from (in precedence order) the
// --columns flag, the [tui] columns config default, or the built-in default.
// Two sizing modes, v1's ergonomic heart: a FULL-WIDTH column sizes to its
// longest visible cell (no truncation — name/cwd read whole); a FIXED column
// truncates at its width. Unknown column names are a LOUD error (a typo must
// never silently drop a column). The leading state gutter (head/busy glyphs +
// attachment) is not a column — it is always shown.
//
// Deliberately arriving later (sequenced, not cut): predicate rule columns +
// meta columns ([[tui.columns]]) with A4; per-segment styling, match
// highlighting and horizontal scroll with A3's filter port.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

// Column names (flag- and config-facing).
const (
	ColID      = "id"
	ColMachine = "machine"
	ColAgent   = "agent"
	ColHead    = "head"
	ColBusy    = "busy"
	ColName    = "name"
	ColCwd     = "cwd"
	ColTags    = "tags"
	ColTickets = "tickets"
	ColNotify  = "notify"
	ColCreated = "created"
)

// colSpec is a column's static metadata. fullWidth columns size to the longest
// visible cell; fixed columns truncate at fixedW (header always fits).
type colSpec struct {
	name      string
	header    string
	fixedW    int
	fullWidth bool
	cell      func(m *Model, row api.ThreadRow) string
}

// colOrder is the fixed left-to-right render order; the visible set is the
// intersection of this order with the active names.
var colOrder = []colSpec{
	{name: ColID, header: "ID", fixedW: 8,
		cell: func(_ *Model, r api.ThreadRow) string { return tid8(r.ID) }},
	{name: ColName, header: "NAME", fullWidth: true,
		cell: func(_ *Model, r api.ThreadRow) string { return r.Name }},
	{name: ColCwd, header: "CWD", fullWidth: true,
		cell: func(m *Model, r api.ThreadRow) string { return m.cwdDisplay(r.Cwd) }},
	{name: ColAgent, header: "AGENT", fixedW: 7,
		cell: func(_ *Model, r api.ThreadRow) string { return r.AgentKind }},
	{name: ColMachine, header: "MACHINE", fixedW: 12,
		cell: func(_ *Model, r api.ThreadRow) string { return r.Machine }},
	{name: ColHead, header: "HEAD", fixedW: 8,
		cell: func(_ *Model, r api.ThreadRow) string { return string(r.Head) }},
	{name: ColBusy, header: "BUSY", fixedW: 4,
		cell: func(_ *Model, r api.ThreadRow) string { return string(r.Busy) }},
	{name: ColTickets, header: "TKT", fixedW: 3,
		cell: func(_ *Model, r api.ThreadRow) string {
			if r.TicketsOpen == 0 {
				return ""
			}
			return strconv.Itoa(r.TicketsOpen)
		}},
	{name: ColNotify, header: "NTF", fixedW: 3,
		cell: func(_ *Model, r api.ThreadRow) string {
			if r.Notify {
				return ""
			}
			return "off"
		}},
	{name: ColTags, header: "TAGS", fixedW: 16,
		cell: func(_ *Model, r api.ThreadRow) string { return strings.Join(r.Tags, ",") }},
	{name: ColCreated, header: "CREATED", fixedW: 10,
		cell: func(_ *Model, r api.ThreadRow) string { return createdLabel(r.CreatedAtUnix) }},
}

// DefaultColumns is the built-in visible set (HEAD/BUSY deliberately off — the
// glyph gutter carries the state; ID off — `i` toggles it).
var DefaultColumns = []string{ColMachine, ColAgent, ColName, ColCwd, ColTags, ColNotify}

// ValidColumnNames returns the known names (for error messages), in render order.
func ValidColumnNames() []string {
	names := make([]string, len(colOrder))
	for i, c := range colOrder {
		names[i] = c.name
	}
	return names
}

// ResolveColumns validates a requested column set. Unknown names are a LOUD
// error listing the valid names. Empty input = the built-in default.
func ResolveColumns(names []string) ([]string, error) {
	if len(names) == 0 {
		return append([]string(nil), DefaultColumns...), nil
	}
	known := map[string]bool{}
	for _, c := range colOrder {
		known[c.name] = true
	}
	var unknown []string
	out := make([]string, 0, len(names))
	for _, raw := range names {
		n := strings.ToLower(strings.TrimSpace(raw))
		if n == "" {
			continue
		}
		if !known[n] {
			unknown = append(unknown, n)
			continue
		}
		out = append(out, n)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown column(s) %s (valid: %s)",
			strings.Join(unknown, ", "), strings.Join(ValidColumnNames(), ", "))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty column set")
	}
	return out, nil
}

// WithColumns sets the visible column set (already validated via ResolveColumns).
func (m Model) WithColumns(names []string) Model {
	m.columns = names
	return m
}

// activeColumns returns the specs to render: the configured set in colOrder
// order, with the ID column joining when toggled on (`i`) even if not configured.
func (m *Model) activeColumns() []colSpec {
	want := map[string]bool{}
	for _, n := range m.columns {
		want[n] = true
	}
	if m.showID {
		want[ColID] = true
	} else if !m.columnsHasID() {
		delete(want, ColID)
	}
	out := make([]colSpec, 0, len(colOrder))
	for _, c := range colOrder {
		if want[c.name] {
			out = append(out, c)
		}
	}
	return out
}

func (m *Model) columnsHasID() bool {
	for _, n := range m.columns {
		if n == ColID {
			return true
		}
	}
	return false
}

// colWidths computes each active column's render width: fixed columns use
// fixedW; full-width columns size to their longest visible cell (min the
// header). Headers always fit.
func (m *Model) colWidths(cols []colSpec, vis []treeRow) []int {
	w := make([]int, len(cols))
	for i, c := range cols {
		w[i] = len([]rune(c.header))
		if !c.fullWidth {
			if c.fixedW > w[i] {
				w[i] = c.fixedW
			}
			continue
		}
		for _, tr := range vis {
			cell := c.cell(m, tr.row)
			if c.name == ColName {
				cell = tr.prefix + cell // tree rails live in the NAME cell
			}
			if n := len([]rune(cell)); n > w[i] {
				w[i] = n
			}
		}
	}
	return w
}

// renderHeader renders the column header line (after the state gutter).
func (m *Model) renderHeader(cols []colSpec, widths []int) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = pad(c.header, widths[i])
	}
	return strings.Join(parts, " ")
}

// renderCells renders one row's column cells (after the state gutter).
// Full-width cells are padded (they never truncate); fixed cells truncate.
// hl, when non-nil, maps column names to matched rune positions (the filter's
// highlight); positions are styled AFTER padding so widths stay rune-true.
func (m *Model) renderCells(cols []colSpec, widths []int, tr treeRow, hl map[string][]int) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		cell := c.cell(m, tr.row)
		var shift int
		if c.name == ColName && tr.prefix != "" {
			cell = tr.prefix + cell // tree rails (highlight positions shift past them)
			shift = len([]rune(tr.prefix))
		}
		if !c.fullWidth {
			cell = trunc(cell, widths[i])
		}
		cell = pad(cell, widths[i])
		if pos := hl[c.name]; len(pos) > 0 {
			if shift > 0 {
				shifted := make([]int, len(pos))
				for j, p := range pos {
					shifted[j] = p + shift
				}
				pos = shifted
			}
			cell = highlight(cell, pos)
		}
		parts[i] = cell
	}
	return strings.Join(parts, " ")
}

// pad right-pads to n columns' worth of runes.
func pad(s string, n int) string {
	if d := n - len([]rune(s)); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// tid8 is the short id form.
func tid8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// createdLabel renders a creation time compactly: time-of-day for today,
// date otherwise (v1's CREATED column behavior).
func createdLabel(unix int64) string {
	if unix == 0 {
		return ""
	}
	t := time.Unix(unix, 0)
	if t.Year() == time.Now().Year() && t.YearDay() == time.Now().YearDay() {
		return t.Format("15:04")
	}
	return t.Format("2006-01-02")
}

// cwdDisplay renders a thread's cwd for the CWD column: the [[cwd_label]]
// transform when configured, else ~-relative.
func (m *Model) cwdDisplay(cwd string) string {
	if m.cwdLabeler != nil {
		return m.cwdLabeler(cwd)
	}
	if m.userHome != "" {
		if rel, ok := strings.CutPrefix(cwd, strings.TrimRight(m.userHome, "/")); ok {
			return "~" + rel
		}
	}
	return cwd
}
