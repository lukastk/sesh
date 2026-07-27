package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Mouse row interaction (in addition to the wheel scroll in model.go's MouseMsg case):
// a left click SELECTS the row under the cursor, a double click ENTERS it (same compose
// as the `enter` key), and a click on the ▾/▸ fold marker collapses/expands that node's
// subtree. Only the plain grid responds — a popup/prompt/ticket view owns the screen.

// doubleClickWindow is how close two left presses on the SAME row must be to count as a
// double click (enter) rather than two separate selects. 500ms is the conventional
// desktop default and comfortably covers a deliberate double click.
const doubleClickWindow = 500 * time.Millisecond

// nowTime returns the current time, overridable in tests via nowFn (nil = time.Now).
func (m Model) nowTime() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

func (m *Model) resetClickTracking() {
	m.lastClickID = ""
	m.lastClickAt = time.Time{}
}

// handleLeftClick maps a left mouse PRESS to a grid action. Modal states (ticket view,
// details, confirm/prompt/tag/uuid popups, move mode) own the screen, so a stray click
// there is ignored rather than reaching the grid underneath.
func (m Model) handleLeftClick(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.ticketMode != ticketNone || m.detailsPopup || m.helpPopup || m.viewPicker ||
		m.confirming != confirmNone || m.prompting != promptNone || m.tagPopup ||
		m.uuidPopup || m.reordering {
		return m, nil
	}
	idx, ok := m.rowAtY(msg.Y)
	if !ok {
		return m, nil
	}
	vis := m.visibleMatches()
	tr := vis[idx]
	m.cursor = idx
	m.ensureCursorVisible()

	// A click on the ▾/▸ marker toggles that node's fold state (no select-then-enter);
	// reset the double-click timer so a second toggle-click doesn't read as an enter.
	if m.clickOnFoldMarker(tr, msg.X) {
		m.foldSelected(!m.isExpanded(tr.row.ID))
		m.resetClickTracking()
		return m, nil
	}

	// SIDEBAR mode: a SINGLE click enters the thread (Lukas: select-then-double-click
	// is the popup's ergonomics; a persistent sidebar is a jump list — one click, one
	// nav, focus hands to the agent pane and the sidebar stays). Fold-marker clicks
	// (above) still just fold. Same offline-owner gate as the enter key.
	if m.sidebar {
		m.resetClickTracking()
		if !m.machineReachable(tr.row.Machine) {
			m.note = ""
			m.actionErr = fmt.Errorf("%s is offline — can't reach %q until it reconnects", tr.row.Machine, offlineRowLabel(tr.row))
			return m, nil
		}
		cmd := m.enterSelected()
		return m, cmd
	}

	// Double click on the SAME row within the window ENTERS it — the same compose as the
	// `enter` key, including the offline-owner gate (a routed nav would otherwise hang on
	// the timeout; refuse instantly and loudly instead).
	now := m.nowTime()
	if tr.row.ID == m.lastClickID && !m.lastClickAt.IsZero() && now.Sub(m.lastClickAt) <= doubleClickWindow {
		m.resetClickTracking()
		if !m.machineReachable(tr.row.Machine) {
			m.note = ""
			m.actionErr = fmt.Errorf("%s is offline — can't reach %q until it reconnects", tr.row.Machine, offlineRowLabel(tr.row))
			return m, nil
		}
		cmd := m.enterSelected()
		return m, cmd
	}
	// First click: select and arm the double-click timer.
	m.lastClickID = tr.row.ID
	m.lastClickAt = now
	return m, nil
}

// rowAtY maps a 0-based terminal row Y to the visible-row index it renders, mirroring
// View's vertical layout (chrome lines above the grid + the viewport window). ok is
// false when Y is above the first data row or below the last. Assumes the plain-grid
// state (no popups) — handleLeftClick guards that before calling.
func (m *Model) rowAtY(y int) (int, bool) {
	top := m.rowsTop()
	start, end := m.viewportRange()
	idx := start + (y - top)
	if y < top || idx < start || idx >= end {
		return 0, false
	}
	return idx, true
}

// rowsTop counts the lines View emits ABOVE the first data row in the plain-grid state:
// the title, any daemon/action/note lines, the column header, and the "▲ N more"
// indicator when the viewport is scrolled. Mirrors View's top section — keep in sync.
func (m *Model) rowsTop() int {
	n := 1 // title
	if m.lastErr != nil {
		n++
	}
	if m.actionErr != nil {
		n++
	}
	if m.note != "" {
		n++
	}
	n++ // column header
	if start, _ := m.viewportRange(); start > 0 {
		n++ // "▲ N more" indicator
	}
	return n
}

// viewportRange returns the [start, end) slice of visibleMatches currently rendered —
// the vertical window View draws. Unknown height (no WindowSizeMsg) shows every row.
func (m *Model) viewportRange() (int, int) {
	vis := m.visibleMatches()
	if m.height <= 0 {
		return 0, len(vis)
	}
	start := m.vOffset
	if start > len(vis) {
		start = len(vis)
	}
	end := start + m.bodyHeight()
	if end > len(vis) {
		end = len(vis)
	}
	return start, end
}

// clickOnFoldMarker reports whether x (0-based terminal column) falls on the ▾/▸ fold
// marker of tr's rendered NAME cell. Only a node WITH visible children has a marker (its
// tree prefix ends with "▾ "/"▸ "); it sits at the tail of that prefix, at the start of
// the NAME column. Returns false when NAME is panned out of view or the marker is clipped
// by a narrow column. Mirrors View's column layout (activeColumns + horizontalView).
func (m *Model) clickOnFoldMarker(tr treeRow, x int) bool {
	if !strings.HasSuffix(tr.prefix, "▾ ") && !strings.HasSuffix(tr.prefix, "▸ ") {
		return false
	}
	cols := m.activeColumns()
	vis := m.visibleMatches()
	widths := m.colWidths(cols, vis)
	hStart, hEnd := 0, len(cols)
	vwidths := widths
	if m.width > 0 {
		hStart, hEnd, vwidths = horizontalView(cols, widths, m.hOffset, m.width-gutterWidth)
	}
	vcols := cols[hStart:hEnd]

	// Find the NAME column's start X within the row: the fixed gutter plus every visible
	// column before it (each followed by a 1-col separator, as renderCells joins them).
	nameX, nameW := -1, 0
	cx := gutterWidth
	for i, c := range vcols {
		if c.name == ColName {
			nameX, nameW = cx, vwidths[i]
			break
		}
		cx += vwidths[i] + 1
	}
	if nameX < 0 {
		return false // NAME column not in the visible horizontal window
	}
	// The marker glyph is the penultimate rune of the prefix ("▾ "/"▸ " → glyph, space).
	off := len([]rune(tr.prefix)) - 2
	if off < 0 {
		return false
	}
	// Accept the glyph cell and its trailing space (the "▾ " target), each only when it's
	// actually within the (possibly clamped) NAME column width.
	if x == nameX+off && off < nameW {
		return true
	}
	if x == nameX+off+1 && off+1 < nameW {
		return true
	}
	return false
}
