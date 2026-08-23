package conformance

// Mouse-click claim: the left-click row interaction (select + fold-marker toggle) driven
// against a REAL daemon and its rendered grid — the mouse gesture maps to the same tree
// operations the keyboard does, proven by the live render, not internal state. (The
// double-click ENTER gesture composes navSelected, already covered by action-nav; the
// gesture→nav mapping itself is unit-tested in internal/tui/mouse_test.go.)

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("mouse-click", claimMouseClick)
}

// leftClick builds a left-button PRESS at (x, y).
func leftClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y}
}

func clickAt(t *testing.T, m tui.Model, x, y int) tui.Model {
	t.Helper()
	nm, _ := m.Update(leftClick(x, y))
	return nm.(tui.Model)
}

// screenRowY returns the 0-based screen row of the rendered DATA line for name (one that
// begins with the "> "/"  " row gutter, so chrome/legend lines never match), or -1.
func screenRowY(view, name string) int {
	for y, ln := range strings.Split(view, "\n") {
		p := stripANSI(ln)
		if (strings.HasPrefix(p, "> ") || strings.HasPrefix(p, "  ")) && strings.Contains(p, " "+name) {
			return y
		}
	}
	return -1
}

// foldMarkerXY returns the terminal column/row of the ▸/▾ fold glyph on name's rendered
// line. Every glyph in the gutter and tree is exactly one cell wide, so a rune index into
// the ANSI-stripped line equals its terminal column.
func foldMarkerXY(view, name string) (x, y int, ok bool) {
	y = screenRowY(view, name)
	if y < 0 {
		return 0, 0, false
	}
	line := stripANSI(strings.Split(view, "\n")[y])
	for i, r := range []rune(line) {
		if r == '▸' || r == '▾' {
			return i, y, true
		}
	}
	return 0, y, false
}

// claimMouseClick: over a REAL parent/child tree from the daemon, a left CLICK selects the
// row under the pointer, and a click on the ▸/▾ fold marker collapses/expands that node's
// subtree — asserted against the live grid + render.
func claimMouseClick(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	alpha := sb.newHeadlessThread(t, "pi", "alpha")
	beta := sb.newHeadlessThread(t, "pi", "beta")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	if !waitUntil(20*time.Second, func() bool { m, _ = render(t, m); return len(m.Rows()) == 2 }) {
		t.Fatalf("2 rows never appeared")
	}
	// Nest beta under alpha on the daemon (the P reparent path).
	m = selectRowByName(t, m, "beta")
	m = runCommand(t, m, "set-parent-uuid")
	m = typeText(t, m, alpha.ID)
	m = runSpecial(t, m, tea.KeyEnter)
	if !waitUntil(10*time.Second, func() bool { return threadParentOf(t, sb, beta.ID) == alpha.ID }) {
		t.Fatalf("daemon did not nest beta under alpha")
	}
	// Poll the grid until beta renders NESTED under alpha (the reparent must propagate
	// from the store through the maintainer to the fetched rows; the preselect that
	// lands on the moved node auto-expands its ancestors, so alpha shows a ▾ marker).
	if !waitUntil(10*time.Second, func() bool {
		m, _ = render(t, m)
		l := rowLine(m.View(), "beta")
		return strings.Contains(l, "├") || strings.Contains(l, "└")
	}) {
		t.Fatalf("beta never rendered nested under alpha:\n%s", m.View())
	}

	// CLICK-TO-SELECT: clicking beta's line moves the selection to it (was on alpha).
	m = selectRowByName(t, m, "alpha")
	betaY := screenRowY(m.View(), "beta")
	m = clickAt(t, m, 20, betaY)
	if row, ok := m.Selected(); !ok || row.Name != "beta" {
		t.Fatalf("click on beta's line did not select it (ok=%v name=%q)", ok, row.Name)
	}

	// CLICK-FOLD collapse: clicking alpha's ▾ marker hides its subtree (beta gone).
	x, y, ok := foldMarkerXY(m.View(), "alpha")
	if !ok {
		t.Fatalf("no fold marker on expanded alpha:\n%s", m.View())
	}
	m = clickAt(t, m, x, y)
	if screenRowY(m.View(), "beta") >= 0 {
		t.Errorf("clicking the fold marker did not collapse alpha (beta still shown):\n%s", m.View())
	}

	// CLICK-FOLD expand: clicking the ▸ marker again reveals the subtree.
	x, y, ok = foldMarkerXY(m.View(), "alpha")
	if !ok {
		t.Fatalf("no fold marker on collapsed alpha:\n%s", m.View())
	}
	m = clickAt(t, m, x, y)
	if screenRowY(m.View(), "beta") < 0 {
		t.Errorf("clicking the fold marker did not re-expand alpha (beta hidden):\n%s", m.View())
	}
}
