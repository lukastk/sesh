package tui

// Vertical viewport + horizontal column pan (G5). Active only when width/height
// are known (a WindowSizeMsg has arrived); with them unset the whole grid renders
// unclipped — so tests that never send a size still see every row/column.
//
// Keys: j/k move the cursor and the viewport follows; ctrl+j/ctrl+k scroll the
// viewport a half-page (the cursor is pulled into the new window); h/l pan the
// columns so a clipped column can be brought into view. Fold/unfold is on the
// arrow keys.

// gutterWidth is the fixed leading state gutter: "> "/"  " + head + busy + att + " ".
const gutterWidth = 6

// chromeLines counts the non-row lines View emits in the current state, so the row
// budget (bodyHeight) leaves room for the header, footers, popups, prompts and the
// scroll indicators. Mirrors View's structure — keep in sync.
func (m *Model) chromeLines() int {
	n := 1 // title
	if m.lastErr != nil {
		n++
	}
	if m.note != "" {
		n++
	}
	if m.tagPopup {
		n += 2 + len(m.tagPopupRow.Tags) // header + tags + help
	}
	if m.uuidPopup {
		n += 2
	}
	if m.prompting != promptNone {
		n++
	}
	n++ // column header
	for _, mv := range m.machines {
		if !mv.Self {
			n++ // per-peer freshness line
		}
	}
	switch {
	case m.filtering:
		n += 2
	case m.filter != "":
		n += 3
	default:
		n += 2
	}
	n += 2 // reserve for the ▲/▼ scroll indicators (always, to keep budgeting simple)
	return n
}

// bodyHeight is how many rows fit in the viewport. 0 height (no WindowSizeMsg yet)
// means "no clipping": render every row.
func (m *Model) bodyHeight() int {
	if m.height <= 0 {
		return len(m.visibleMatches())
	}
	h := m.height - m.chromeLines()
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) halfPage() int {
	if h := m.bodyHeight() / 2; h >= 1 {
		return h
	}
	return 1
}

// ensureCursorVisible scrolls the viewport just enough to keep the cursor in view.
func (m *Model) ensureCursorVisible() {
	if m.height <= 0 {
		return
	}
	h := m.bodyHeight()
	if m.cursor < m.vOffset {
		m.vOffset = m.cursor
	}
	if m.cursor >= m.vOffset+h {
		m.vOffset = m.cursor - h + 1
	}
	m.clampVOffset()
}

// scrollRows moves the viewport by delta rows (ctrl+j/k), pulling the cursor into
// the new window so the selection stays visible.
func (m *Model) scrollRows(delta int) {
	if m.height <= 0 {
		return
	}
	m.vOffset += delta
	m.clampVOffset()
	h := m.bodyHeight()
	if m.cursor < m.vOffset {
		m.cursor = m.vOffset
	}
	if m.cursor >= m.vOffset+h {
		m.cursor = m.vOffset + h - 1
	}
	n := len(m.visibleMatches())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) clampVOffset() {
	maxOff := len(m.visibleMatches()) - m.bodyHeight()
	if maxOff < 0 {
		maxOff = 0
	}
	if m.vOffset > maxOff {
		m.vOffset = maxOff
	}
	if m.vOffset < 0 {
		m.vOffset = 0
	}
}

// horizontalWindow returns [start:end) of cols that fits in avail columns, always
// showing at least the start column.
func horizontalWindow(cols []colSpec, widths []int, start, avail int) (int, int) {
	if start < 0 {
		start = 0
	}
	if start >= len(cols) && len(cols) > 0 {
		start = len(cols) - 1
	}
	used, end := 0, start
	for end < len(cols) {
		w := widths[end]
		if end > start {
			w++ // column separator
		}
		if used+w > avail && end > start {
			break
		}
		used += w
		end++
	}
	if end == start && start < len(cols) {
		end = start + 1 // always show at least one column
	}
	return start, end
}

// maxHOffset is the largest useful horizontal offset (0 when every column fits).
func (m *Model) maxHOffset() int {
	if m.width <= 0 {
		return 0
	}
	cols := m.activeColumns()
	widths := m.colWidths(cols, m.visibleMatches())
	avail := m.width - gutterWidth
	for start := 0; start < len(cols); start++ {
		if _, end := horizontalWindow(cols, widths, start, avail); end >= len(cols) {
			return start
		}
	}
	if len(cols) == 0 {
		return 0
	}
	return len(cols) - 1
}
