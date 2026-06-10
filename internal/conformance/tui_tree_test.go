package conformance

// A5 tree claims: the collapsible parent/child tree over REAL threads.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("tree-render-fold", claimTreeRenderFold)
	registerTUIClaim("tree-config-expand", claimTreeConfigExpand)
	registerTUIClaim("tree-orphan-promotes", claimTreeOrphanPromotes)
}

// treeSandbox: a real parent with two real children.
func treeSandbox(t *testing.T) (*Sandbox, tui.Model) {
	t.Helper()
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	p := sb.newHeadlessThread(t, "pi", "papa")
	for _, name := range []string{"kid-a", "kid-b"} {
		if _, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", name, "--cwd", "/tmp", "--headless", "--parent", p.ID); err != nil {
			t.Fatalf("child %s: %v\n%s", name, err, stderr)
		}
	}
	m := tui.New(sb.Home+"/daemon.sock", false)
	if !waitUntilT(t, func() bool { m, _ = render(t, m); return len(m.Rows()) == 3 }) {
		t.Fatalf("3 rows never appeared")
	}
	return sb, m
}

// claimTreeRenderFold: children are COLLAPSED under their parent by default
// (▸, hidden rows); → expands (▾ + ├/└ rails, real children visible); ←
// collapses again. The cursor walks the rendered order.
func claimTreeRenderFold(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	_, m := treeSandbox(t)
	view := m.View()
	if strings.Contains(view, "kid-a") || strings.Contains(view, "kid-b") {
		t.Fatalf("children visible while collapsed (default must be collapsed):\n%s", view)
	}
	if !strings.Contains(rowLine(view, "papa"), "▸") {
		t.Errorf("collapsed parent missing the ▸ marker: %q", rowLine(view, "papa"))
	}

	// Cursor is on papa (the only visible row) — expand.
	m = runSpecial(t, m, tea.KeyRight)
	view = m.View()
	if !strings.Contains(view, "kid-a") || !strings.Contains(view, "kid-b") {
		t.Fatalf("→ did not reveal the children:\n%s", view)
	}
	if !strings.Contains(rowLine(view, "papa"), "▾") {
		t.Errorf("expanded parent missing ▾: %q", rowLine(view, "papa"))
	}
	if !strings.Contains(rowLine(view, "kid-a"), "├") || !strings.Contains(rowLine(view, "kid-b"), "└") {
		t.Errorf("rails wrong:\n%s\n%s", rowLine(view, "kid-a"), rowLine(view, "kid-b"))
	}

	// Collapse again.
	m = runSpecial(t, m, tea.KeyLeft)
	if v := m.View(); strings.Contains(v, "kid-a") {
		t.Errorf("← did not collapse:\n%s", v)
	}
}

// claimTreeConfigExpand: WithExpand (the [tui] expand_children default /
// --expand flag) starts nodes expanded.
func claimTreeConfigExpand(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb, _ := treeSandbox(t)
	m := tui.New(sb.Home+"/daemon.sock", false).WithExpand(true)
	if !waitUntilT(t, func() bool { m, _ = render(t, m); return strings.Contains(m.View(), "kid-a") }) {
		t.Fatalf("expand_children did not start expanded:\n%s", m.View())
	}
}

// claimTreeOrphanPromotes: a child whose parent is NOT in the current view
// (here: archived away) renders as a top-level row, not dropped.
func claimTreeOrphanPromotes(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb, m := treeSandbox(t)
	papa := threadByName(t, sb, "papa")
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", papa.ID); err != nil {
		t.Fatalf("archive papa: %v\n%s", err, stderr)
	}
	if !waitUntilT(t, func() bool {
		m, _ = render(t, m)
		v := m.View()
		return !strings.Contains(v, "papa") && strings.Contains(v, "kid-a") && strings.Contains(v, "kid-b")
	}) {
		t.Fatalf("orphaned children did not promote to top level:\n%s", m.View())
	}
	if l := rowLine(m.View(), "kid-a"); strings.Contains(l, "├") || strings.Contains(l, "└") {
		t.Errorf("promoted orphan still shows rails: %q", l)
	}
}
