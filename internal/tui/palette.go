package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The COMMAND PALETTE (`p`): fuzzy-search every registered command and run it on
// the current selection. It exists so the grid needs only a handful of keys — a
// rarely-used action (fork, delete, add a tag, set a parent…) is two keystrokes
// and a word away instead of consuming a letter nobody remembers.
//
// The palette is a full-screen takeover, like the `?` popup and the view picker.
// It runs the SAME dispatch as a key press (Model.runCommand), so a command
// behaves identically however it is invoked — including the offline-owner gate.

// paletteRowsTop is the terminal row the first command line renders on. The
// layout above it is fixed (title, query, the ▲ indicator) so the mouse handler
// can map a click back to a row; paletteView must mirror this.
const paletteRowsTop = 3

// paletteChrome is how many lines the palette spends on everything that is not a
// command row: title, query, the two scroll indicators, the footer.
const paletteChrome = 5

// paletteDescWidth is the description column's width; keys render to its right,
// in one column for every row (selected or not).
const paletteDescWidth = 40

// clipPalette truncates a rendered line to the pane width (never wrap — a wrapped
// row would desync paletteRowAtY from what is on screen).
func (m Model) clipPalette(line string) string {
	if m.width > 1 {
		if r := []rune(line); len(r) > m.width {
			return string(r[:m.width-1]) + "…"
		}
	}
	return line
}

// paletteEntry is one scored candidate.
type paletteEntry struct {
	cmd   Command
	pos   []int // matched rune positions in Desc (highlighting); nil when matched on the id
	score int
}

// paletteCandidates fuzzy-matches the query against every command and returns the
// survivors best-first. An empty query keeps the registry's display order, so the
// palette opens as a readable menu rather than an arbitrary permutation.
//
// Both the description ("toggle the needs-attention flag") and the stable id
// ("flag") are matched, since the id is what config and muscle memory use; the
// better of the two scores wins, and only a description match highlights.
func (m Model) paletteCandidates() []paletteEntry {
	q := strings.TrimSpace(string(m.paletteQuery))
	out := make([]paletteEntry, 0, len(commands))
	for _, c := range commands {
		if q == "" {
			out = append(out, paletteEntry{cmd: c})
			continue
		}
		best := paletteEntry{cmd: c, score: -1 << 30}
		matched := false
		if r := fuzzyScore(q, c.Desc); r.ok {
			best, matched = paletteEntry{cmd: c, pos: r.pos, score: r.score}, true
		}
		if r := fuzzyScore(q, c.ID); r.ok && (!matched || r.score > best.score) {
			best, matched = paletteEntry{cmd: c, score: r.score}, true
		}
		if matched {
			out = append(out, best)
		}
	}
	if q != "" {
		// Stable so equal scores keep registry order (a shuffling list under a
		// steady query would make the cursor unpredictable).
		sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	}
	return out
}

// openPalette starts a fresh palette (query cleared — a stale query from last time
// would hide the command the user is reaching for).
func (m *Model) openPalette() {
	m.palette = true
	m.paletteQuery, m.paletteCursor, m.paletteOffset = nil, 0, 0
}

// closePalette dismisses it.
func (m *Model) closePalette() {
	m.palette = false
	m.paletteQuery, m.paletteCursor, m.paletteOffset = nil, 0, 0
}

// handlePaletteKey drives the palette: type to filter, ↑/↓ (or ^k/^j) move,
// Enter runs the highlighted command, Esc cancels.
func (m Model) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cands := m.paletteCandidates()
	switch msg.String() {
	case "esc", hardQuitKey:
		m.closePalette()
		return m, nil
	case "up", "ctrl+k":
		if len(cands) > 0 {
			m.paletteCursor = (m.paletteCursor - 1 + len(cands)) % len(cands)
			m.ensurePaletteVisible(len(cands))
		}
		return m, nil
	case "down", "ctrl+j":
		if len(cands) > 0 {
			m.paletteCursor = (m.paletteCursor + 1) % len(cands)
			m.ensurePaletteVisible(len(cands))
		}
		return m, nil
	case "backspace":
		if n := len(m.paletteQuery); n > 0 {
			m.paletteQuery = m.paletteQuery[:n-1]
			m.paletteCursor, m.paletteOffset = 0, 0
		}
		return m, nil
	case "enter":
		if m.paletteCursor < 0 || m.paletteCursor >= len(cands) {
			// Nothing matched — keep the palette open so the query can be fixed,
			// rather than silently closing on a keystroke that did nothing.
			return m, nil
		}
		id := cands[m.paletteCursor].cmd.ID
		m.closePalette()
		return m.runCommand(id)
	}
	// Type to filter — whole runes, so a paste isn't dropped.
	switch msg.Type {
	case tea.KeyRunes:
		m.paletteQuery = append(m.paletteQuery, msg.Runes...)
		m.paletteCursor, m.paletteOffset = 0, 0
	case tea.KeySpace:
		m.paletteQuery = append(m.paletteQuery, ' ')
		m.paletteCursor, m.paletteOffset = 0, 0
	}
	return m, nil
}

// paletteVisibleRows is how many command lines fit. Height unknown (tests, before
// the first WindowSizeMsg) = show everything.
func (m Model) paletteVisibleRows(n int) int {
	if m.height <= 0 {
		return n
	}
	avail := m.height - paletteChrome
	if avail < 1 {
		avail = 1
	}
	if avail > n {
		avail = n
	}
	return avail
}

// ensurePaletteVisible scrolls the window so the cursor stays on screen.
func (m *Model) ensurePaletteVisible(n int) {
	avail := m.paletteVisibleRows(n)
	if m.paletteCursor < m.paletteOffset {
		m.paletteOffset = m.paletteCursor
	}
	if m.paletteCursor >= m.paletteOffset+avail {
		m.paletteOffset = m.paletteCursor - avail + 1
	}
	if max := n - avail; m.paletteOffset > max {
		m.paletteOffset = max
	}
	if m.paletteOffset < 0 {
		m.paletteOffset = 0
	}
}

// paletteView renders the palette. The two indicator lines are ALWAYS present
// (blank when unneeded) so the row geometry is stable while scrolling — the same
// rule helpView follows, and what paletteRowAtY depends on.
func (m Model) paletteView() string {
	cands := m.paletteCandidates()
	avail := m.paletteVisibleRows(len(cands))
	off := m.paletteOffset
	if max := len(cands) - avail; off > max {
		off = max
	}
	if off < 0 {
		off = 0
	}

	var b strings.Builder
	b.WriteString(styleHeader.Render("sesh — commands") + "\n")
	b.WriteString(styleDim.Render("  > "+string(m.paletteQuery)) + "█\n")
	if off > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▲ %d more", off)) + "\n")
	} else {
		b.WriteString("\n")
	}
	km := m.km()
	for i := off; i < off+avail; i++ {
		e := cands[i]
		key := km.KeyLabel(e.cmd.ID)
		// The plain text is what decides the layout: desc may already carry
		// highlight escapes, whose bytes must not count toward the column width —
		// and the selected row must land its key in the SAME column as the rest.
		plain := fmt.Sprintf("  %-*s %s", paletteDescWidth, e.cmd.Desc, key)
		var line string
		if i == m.paletteCursor {
			// Reverse-video the whole row, rendered from the plain text: the
			// highlight's own colour would punch a hole in the band.
			line = styleSelected.Render(m.clipPalette(plain))
		} else {
			pad := paletteDescWidth - len([]rune(e.cmd.Desc))
			if pad < 0 {
				pad = 0
			}
			line = "  " + highlight(e.cmd.Desc, e.pos) +
				strings.Repeat(" ", pad) + " " + styleDim.Render(key)
			if m.width > 1 && len([]rune(plain)) > m.width {
				// Too narrow for the key column — fall back to the clipped plain
				// text rather than letting the row wrap (the H1 no-clipping rule).
				line = styleDim.Render(m.clipPalette(plain))
			}
		}
		b.WriteString(line + "\n")
	}
	if rest := len(cands) - off - avail; rest > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  ▼ %d more", rest)) + "\n")
	} else {
		b.WriteString("\n")
	}
	if len(cands) == 0 {
		b.WriteString(styleDim.Render("  no command matches") + "\n")
	} else {
		b.WriteString(styleDim.Render("↑/↓ move · enter run · esc cancel") + "\n")
	}
	return b.String()
}

// paletteRowAtY maps a click's terminal row to a candidate index (mirrors
// paletteView's fixed layout). ok=false outside the list.
func (m Model) paletteRowAtY(y int) (int, bool) {
	cands := m.paletteCandidates()
	avail := m.paletteVisibleRows(len(cands))
	i := y - paletteRowsTop + m.paletteOffset
	if i < m.paletteOffset || i >= m.paletteOffset+avail || i >= len(cands) {
		return 0, false
	}
	return i, true
}

// PaletteOpen reports whether the command palette is up (tests).
func (m Model) PaletteOpen() bool { return m.palette }

// PaletteQuery exposes the palette's query (tests).
func (m Model) PaletteQuery() string { return string(m.paletteQuery) }

// PaletteCommands lists the currently matching command ids, best-first (tests).
func (m Model) PaletteCommands() []string {
	cands := m.paletteCandidates()
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.cmd.ID)
	}
	return out
}
