package tui

// ONE scrollable list window, shared by every list surface in the TUI: the grid
// (scroll.go), the `?` keymap, the command palette, the reparent picker and the
// shells viewer.
//
// Each of those had grown its OWN copy of this arithmetic — four transcriptions
// of the same six lines — and the shells viewer had NO copy at all, which is
// exactly why its cursor could walk past the bottom of the pane and the selected
// session simply stopped being on screen. A surface that renders a list must get
// its window from here rather than re-deriving it, so "the selection is always
// visible" is one rule with one implementation.
//
// All three are PURE: the caller owns the offset, so a surface can keep its own
// (grid vOffset, paletteOffset, shellOffset) without this file knowing about it.

// listVisibleRows is how many rows fit in a pane of `height` once `chrome`
// non-row lines (title, indicators, footer, …) are taken out, capped at the
// number of rows there actually are.
//
// height <= 0 means "size unknown" — no WindowSizeMsg has arrived yet, which is
// also every Model a unit test builds by hand — and renders EVERYTHING rather
// than clipping to a guessed height.
func listVisibleRows(total, height, chrome int) int {
	if height <= 0 {
		return total
	}
	avail := height - chrome
	if avail < 1 {
		avail = 1 // a pane too short for even one row still shows the cursor's row
	}
	if avail > total {
		avail = total
	}
	return avail
}

// listEnsureVisible scrolls `off` the MINIMUM distance that brings `cursor`
// inside the window [off, off+avail) — the whole "follow the selection" rule.
// Scrolling further would jump the list under the user for no reason.
func listEnsureVisible(off, cursor, avail int) int {
	if avail < 1 {
		return off
	}
	if cursor < off {
		off = cursor
	}
	if cursor >= off+avail {
		off = cursor - avail + 1
	}
	if off < 0 {
		off = 0
	}
	return off
}

// listClampOffset keeps `off` inside [0, total-avail], so the window can never
// scroll past the end of the list (which would render a screen of blank rows
// and read as "my sessions disappeared").
func listClampOffset(off, total, avail int) int {
	maxOff := total - avail
	if maxOff < 0 {
		maxOff = 0
	}
	if off > maxOff {
		off = maxOff
	}
	if off < 0 {
		off = 0
	}
	return off
}
