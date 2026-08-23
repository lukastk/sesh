package conformance

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

// claimShellsView: `S` opens the shells viewer, which lists the REAL tmux
// sessions on the work server — including ones sesh never created — classifies
// them by whether they host a thread-marked pane, and can really kill one.
//
// This is the gap the viewer exists to close: a session started by hand is
// invisible to the grid because it has no thread record, even though it is
// sitting right there in the cockpit.
//
// The agent session is staged by STAMPING a pane with @sesh-thread-id rather
// than spawning a real agent: classification is a question about the marker, not
// about agent liveness, so a real agent would slow the cell without making it
// prove more. The sessions and the kill are entirely real.
func claimShellsView(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// Two REAL sessions on the real work server: one sesh knows nothing about,
	// one holding a thread-marked pane.
	for _, name := range []string{"ghostsess", "agentsess"} {
		if out, err := sb.rawTmux(t, "new-session", "-d", "-s", name); err != nil {
			t.Fatalf("new-session %s: %v\n%s", name, err, out)
		}
	}
	pane := sb.paneOf(t, "agentsess")
	if out, err := sb.rawTmux(t, "set-option", "-p", "-t", pane, "@sesh-thread-id", "thr_claim_shell"); err != nil {
		t.Fatalf("stamp pane: %v\n%s", err, out)
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)

	// S opens the viewer and runs the real per-machine `sesh tmux info` fan-out.
	m = runKeyDeep(t, m, "S")
	if !m.ShellsViewOpen() {
		t.Fatalf("S did not open the shells viewer")
	}
	if errs := m.ShellErrs(); len(errs) != 0 {
		t.Fatalf("fan-out reported errors: %v", errs)
	}

	// The untracked session is a GHOST — the whole point. Assert this FIRST:
	// it is the thing the grid cannot show at all.
	if class, ok := m.ShellSessionClass(sb.Machine, "ghostsess"); !ok || class != "ghost" {
		t.Fatalf("hand-made session: class=%q listed=%v, want a listed ghost", class, ok)
	}
	// The thread-marked one is classified separately, so promoting it is never
	// the default suggestion.
	if class, ok := m.ShellSessionClass(sb.Machine, "agentsess"); !ok || class != "agent" {
		t.Fatalf("thread-marked session: class=%q listed=%v, want a listed agent session", class, ok)
	}

	// Kill the ghost THROUGH the viewer (x → y) and prove it is really gone from
	// the tmux server, not merely dropped from the list.
	if !m.ShellSelect(sb.Machine, "ghostsess") {
		t.Fatalf("ghostsess not selectable")
	}
	m = runKeyDeep(t, m, "x")
	// x alone must not kill: the confirmation is the whole guard on a
	// session-wide blast radius.
	if out, err := sb.rawTmux(t, "has-session", "-t", "ghostsess"); err != nil {
		t.Fatalf("x killed the session BEFORE the confirmation was answered: %v\n%s", err, out)
	}
	m = runKeyDeep(t, m, "y")
	if out, err := sb.rawTmux(t, "has-session", "-t", "ghostsess"); err == nil {
		t.Fatalf("ghostsess still exists on the tmux server after the viewer killed it\n%s", out)
	}
	// The agent session must be untouched — a kill is one session, never a sweep.
	if out, err := sb.rawTmux(t, "has-session", "-t", "agentsess"); err != nil {
		t.Fatalf("killing the ghost also took agentsess: %v\n%s", err, out)
	}
}

// runKeyDeep is runKey that drains the whole command CHAIN, not just the first
// link: the viewer's kill returns a reload command, so a single-level drain
// would assert against a pre-reload list.
func runKeyDeep(t *testing.T, m tui.Model, key string) tui.Model {
	t.Helper()
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	m = nm.(tui.Model)
	for i := 0; i < 8 && cmd != nil; i++ {
		next, c := m.Update(cmd())
		m, cmd = next.(tui.Model), c
	}
	return m
}
