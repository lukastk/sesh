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
	ColID          = "id"
	ColMachine     = "machine"
	ColAgent       = "agent"
	ColHead        = "head"
	ColBusy        = "busy"
	ColName        = "name"
	ColCwd         = "cwd"
	ColTags        = "tags"
	ColTickets     = "tickets"
	ColTicketName  = "ticket_name"
	ColTicketInput = "ticket_input"
	ColNotify      = "notify"
	ColCreated     = "created"
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
		cell: func(m *Model, r api.ThreadRow) string { return m.cwdDisplay(r) }},
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
	{name: ColTicketName, header: "TKT-NAME", fullWidth: true,
		cell: func(_ *Model, r api.ThreadRow) string {
			// Newest open ticket's name; +N when the thread has more open tickets.
			if r.TicketName == "" {
				return ""
			}
			if r.TicketsOpen > 1 {
				return fmt.Sprintf("%s +%d", r.TicketName, r.TicketsOpen-1)
			}
			return r.TicketName
		}},
	{name: ColTicketInput, header: "TKT!", fixedW: 4,
		cell: func(_ *Model, r api.ThreadRow) string {
			if r.TicketNeedsInput {
				return "!" // a bound ticket is active on a headful·idle thread
			}
			return ""
		}},
	{name: ColNotify, header: "NTF", fixedW: 3,
		cell: func(_ *Model, r api.ThreadRow) string {
			if r.Notify {
				return "▪" // notifications on; blank = off
			}
			return ""
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

// ColumnMove repositions one column over the base set (see config.ColumnMove).
type ColumnMove struct {
	Name     string
	After    string
	Before   string
	Position int // 1-based; 0 = unset
}

// ApplyColumnMoves applies the moves to a base column list, in order. Each
// move removes its column (if present) and re-inserts it: relative to an
// anchor (after/before) or at an absolute 1-based position. A column not in
// the base is inserted. Validation is LOUD: an unknown column/anchor, more
// than one of after/before/position, or none, is an error the user must fix.
func ApplyColumnMoves(base []string, moves []ColumnMove) ([]string, error) {
	known := map[string]bool{}
	for _, c := range colOrder {
		known[c.name] = true
	}
	out := append([]string(nil), base...)
	indexOf := func(name string) int {
		for i, n := range out {
			if n == name {
				return i
			}
		}
		return -1
	}
	for i, mv := range moves {
		if mv.Name == "" {
			return nil, fmt.Errorf("[tui.column] %d: name is required", i+1)
		}
		if !known[mv.Name] {
			return nil, fmt.Errorf("[tui.column] %q: unknown column (valid: %s)", mv.Name, strings.Join(ValidColumnNames(), ", "))
		}
		set := 0
		if mv.After != "" {
			set++
		}
		if mv.Before != "" {
			set++
		}
		if mv.Position != 0 {
			set++
		}
		if set != 1 {
			return nil, fmt.Errorf("[tui.column] %q: set exactly one of after / before / position", mv.Name)
		}
		// Remove the column from its current spot (a move, not a duplicate).
		if cur := indexOf(mv.Name); cur >= 0 {
			out = append(out[:cur], out[cur+1:]...)
		}
		// Resolve the insertion index.
		var at int
		switch {
		case mv.Position != 0:
			if mv.Position < 1 {
				return nil, fmt.Errorf("[tui.column] %q: position must be >= 1 (1 = first)", mv.Name)
			}
			at = mv.Position - 1
		case mv.After != "":
			anchor := indexOf(mv.After)
			if anchor < 0 {
				return nil, fmt.Errorf("[tui.column] %q: anchor %q (after) is not in the column set", mv.Name, mv.After)
			}
			at = anchor + 1
		default: // before
			anchor := indexOf(mv.Before)
			if anchor < 0 {
				return nil, fmt.Errorf("[tui.column] %q: anchor %q (before) is not in the column set", mv.Name, mv.Before)
			}
			at = anchor
		}
		if at > len(out) {
			at = len(out)
		}
		out = append(out[:at], append([]string{mv.Name}, out[at:]...)...)
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
	spec := map[string]colSpec{}
	for _, c := range colOrder {
		spec[c.name] = c
	}
	// Render in the USER's configured order (--columns / [tui] columns), not a
	// fixed built-in order. The `i` ID toggle prepends ID when it isn't already
	// configured (so it appears without disturbing the chosen order).
	names := append([]string(nil), m.columns...)
	if m.showID && !m.columnsHasID() {
		names = append([]string{ColID}, names...)
	}
	out := make([]colSpec, 0, len(names))
	for _, n := range names {
		if c, ok := spec[n]; ok {
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
		parts[i] = pad(trunc(c.header, widths[i]), widths[i])
	}
	return strings.Join(parts, " ")
}

// renderCells renders one row's column cells (after the state gutter).
// Every cell is truncated to its render width then padded; full-width columns are
// normally sized to their longest cell so truncation is a no-op, EXCEPT for a
// clipped trailing column (horizontalView's partial NAME) whose width is reduced.
// hl, when non-nil, maps column names to matched rune positions (the filter's
// highlight); positions are styled AFTER padding so widths stay rune-true.
// colorize applies the per-column [[tui.column_color]] tint; the caller passes
// false for a selected row (its reverse-video wins) so colour never fights it. A
// filter-matched cell shows the match styling instead of the column tint (match
// wins over colour) — both keep widths rune-true (ANSI is zero-width).
func (m *Model) renderCells(cols []colSpec, widths []int, tr treeRow, hl map[string][]int, colorize bool) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		cell := c.cell(m, tr.row)
		var shift int
		if c.name == ColName && tr.prefix != "" {
			cell = tr.prefix + cell // tree rails (highlight positions shift past them)
			shift = len([]rune(tr.prefix))
		}
		// Truncate to the render width. Full-width columns are normally sized to
		// their longest cell (so this is a no-op), but a CLIPPED trailing column
		// (horizontalView's partial NAME) carries a reduced width and must truncate.
		cell = trunc(cell, widths[i])
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
		} else if colorize {
			if st, ok := m.colColors[c.name]; ok {
				cell = st.Render(cell)
			}
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

// cwdDisplay renders a thread's cwd for the CWD column. It works on the
// OWNER-relative path (row.CwdRel, stamped by the owning machine's daemon) so a
// cross-machine thread labels correctly — the viewer cannot know a peer's home.
// CwdRel is already ~-relative, so the labeler/fallback need no further home
// stripping. A pre-CwdRel peer (rolling deploy) falls back to viewer-home
// relativization of the absolute cwd.
func (m *Model) cwdDisplay(r api.ThreadRow) string {
	disp := r.CwdRel
	if disp == "" {
		disp = tildeRelative(r.Cwd, m.userHome) // pre-schema-8 peer, or owner home unknown
	}
	if m.cwdLabeler != nil {
		return m.cwdLabeler(disp)
	}
	return disp
}

// tildeRelative renders path ~-relative to home (the viewer-side fallback when an
// owner-relative path is unavailable).
func tildeRelative(path, home string) string {
	if home != "" {
		if rel, ok := strings.CutPrefix(path, strings.TrimRight(home, "/")); ok {
			return "~" + rel
		}
	}
	return path
}
