package tui

// Vertical viewport + horizontal column pan (G5). Active only when width/height
// are known (a WindowSizeMsg has arrived); with them unset the whole grid renders
// unclipped — so tests that never send a size still see every row/column.
//
// Keys: j/k move the cursor and the viewport follows; ctrl+j/ctrl+k scroll the
// viewport a half-page (the cursor is pulled into the new window); h/l pan the
// columns so a clipped column can be brought into view. Fold/unfold is on the
// arrow keys.

// gutterWidth is the fixed leading state gutter:
// "> "/"  " + head + busy + descendant + att + " ".
const gutterWidth = 7

// wheelTick applies one wheel notch in direction dir (-1/+1) to the accumulator acc,
// returning the net step (-1/0/+1) once `div` notches accumulate in one direction. A
// direction flip resets the accumulator so reversing is immediate; div<=1 always steps
// (the default, every notch). This is how mouse_scroll_v/h dampen fast scrolling.
func wheelTick(acc *int, dir, div int) int {
	if div <= 1 {
		return dir
	}
	if (*acc > 0) != (dir > 0) {
		*acc = 0 // reversed direction — start fresh
	}
	*acc += dir
	if *acc >= div {
		*acc = 0
		return 1
	}
	if *acc <= -div {
		*acc = 0
		return -1
	}
	return 0
}

// wheelMoveV moves the selection by one row in dir once the vertical divisor is met.
func (m *Model) wheelMoveV(dir int) {
	if s := wheelTick(&m.wheelAccV, dir, m.scrollDivV); s != 0 {
		m.moveCursor(s)
		m.ensureCursorVisible()
	}
}

// wheelPanH pans the columns by one in dir once the horizontal divisor is met
// (clamped to [0, maxHOffset]).
func (m *Model) wheelPanH(dir int) {
	s := wheelTick(&m.wheelAccH, dir, m.scrollDivH)
	if s < 0 && m.hOffset > 0 {
		m.hOffset--
	} else if s > 0 && m.hOffset < m.maxHOffset() {
		m.hOffset++
	}
}

// chromeLines counts the non-row lines View emits in the current state, so the row
// budget (bodyHeight) leaves room for the header, footers, popups, prompts and the
// scroll indicators. Mirrors View's structure — keep in sync.
func (m *Model) chromeLines() int {
	n := 1 // title
	if m.lastErr != nil {
		n++
	}
	if m.actionErr != nil {
		n++ // persistent loud action-error line
	}
	if m.note != "" {
		n++
	}
	if m.confirming != confirmNone {
		n += 2 // confirm header + help
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
	// The legend OVERFLOWS (wraps) to the width, so its height is variable — count
	// the wrapped lines, not a fixed 1, or the row budget drifts on narrow widths.
	legendH := m.legendLines()
	switch {
	case m.filtering:
		n += 2 // blank + filter prompt
	case m.filter != "":
		n += 2 + legendH // blank + filter-status + wrapped legend
	default:
		n += 1 + legendH // blank + wrapped legend
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

// minPartialColW is the smallest leftover width worth rendering a clipped trailing
// column into (below this the column would be just an ellipsis — not useful).
const minPartialColW = 4

// horizontalView returns the visible column range [start:end) AND the render width
// for each column in that range. It first takes every column that fully fits (via
// horizontalWindow); then, if space remains and a wider column was dropped, it
// appends that one overflow column at a CLIPPED width so a too-wide trailing column
// (e.g. NAME) renders PARTIALLY instead of vanishing entirely (the reported bug).
// The returned widths are a fresh slice — the trailing entry may differ from the
// column's natural width — and their total never exceeds avail (the last visible
// column is clamped so nothing bleeds past the screen edge).
func horizontalView(cols []colSpec, widths []int, start, avail int) (int, int, []int) {
	s, e := horizontalWindow(cols, widths, start, avail)
	out := make([]int, e-s)
	copy(out, widths[s:e])
	used := 0
	for i := s; i < e; i++ {
		if i > s {
			used++ // column separator
		}
		used += widths[i]
	}
	// A wider column was dropped and there's room for a useful partial slice of it.
	if e < len(cols) {
		if leftover := avail - used - 1; leftover >= minPartialColW { // -1 = separator
			out = append(out, leftover)
			e++
		}
	}
	// Clamp the last visible column so the total never overflows avail (only the
	// single forced column from horizontalWindow can be wider than the screen).
	total := 0
	for i, w := range out {
		if i > 0 {
			total++ // separator
		}
		total += w
	}
	if total > avail && len(out) > 0 {
		if out[len(out)-1] -= total - avail; out[len(out)-1] < 1 {
			out[len(out)-1] = 1
		}
	}
	return s, e, out
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
