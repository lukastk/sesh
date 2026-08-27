package tui

import "testing"

// The shared list-window arithmetic. Every list surface in the TUI reads its
// window from these three functions, so their edges are worth pinning directly:
// a wrong answer here scrolls the selection off screen on ALL of them at once.
func TestListWindow(t *testing.T) {
	t.Run("visible rows", func(t *testing.T) {
		for _, c := range []struct{ total, height, chrome, want int }{
			{40, 20, 6, 14},
			{40, 0, 6, 40},  // size unknown (no WindowSizeMsg yet) = no clipping
			{40, -1, 6, 40}, // same
			{3, 20, 6, 3},   // never more rows than there are
			{40, 6, 6, 1},   // chrome fills the pane: still show the cursor's row
			{40, 2, 6, 1},   // chrome overflows it: same
			{0, 20, 6, 0},   // empty list
		} {
			if got := listVisibleRows(c.total, c.height, c.chrome); got != c.want {
				t.Errorf("listVisibleRows(%d,%d,%d) = %d, want %d", c.total, c.height, c.chrome, got, c.want)
			}
		}
	})

	t.Run("ensure visible scrolls the MINIMUM distance", func(t *testing.T) {
		for _, c := range []struct{ off, cursor, avail, want int }{
			{0, 5, 10, 0},   // already inside the window: do not move
			{0, 10, 10, 1},  // one past the bottom: scroll exactly one
			{0, 30, 10, 21}, // a long jump lands the cursor on the last line
			{20, 5, 10, 5},  // above the window: the cursor becomes the top line
			{5, 5, 10, 5},   // exactly the top line
			{3, 0, 0, 3},    // no room to render: leave the offset alone
			{-4, 2, 10, 0},  // never negative
		} {
			if got := listEnsureVisible(c.off, c.cursor, c.avail); got != c.want {
				t.Errorf("listEnsureVisible(%d,%d,%d) = %d, want %d", c.off, c.cursor, c.avail, got, c.want)
			}
		}
	})

	t.Run("clamp offset", func(t *testing.T) {
		for _, c := range []struct{ off, total, avail, want int }{
			{0, 40, 10, 0},
			{35, 40, 10, 30}, // cannot scroll past the end
			{30, 40, 10, 30},
			{5, 3, 10, 0}, // fewer rows than the window: pinned to the top
			{-2, 40, 10, 0},
			{7, 0, 0, 0}, // empty list
		} {
			if got := listClampOffset(c.off, c.total, c.avail); got != c.want {
				t.Errorf("listClampOffset(%d,%d,%d) = %d, want %d", c.off, c.total, c.avail, got, c.want)
			}
		}
	})
}
