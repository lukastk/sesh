package conformance

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

// The TUI sits OUTSIDE the feature matrix, so it gets its own anti-rigging track,
// structurally identical to TestMatrixComplete: a fixed list of CLAIMS about the
// real TUI, each backed by a real test that drives the actual Model against a real
// daemon and asserts against INDEPENDENTLY-fetched truth (never golden blobs, never
// fixtures). TestTUIClaimsComplete fails the build if any declared claim lacks a
// test; an unimplemented claim is a loud Skip, never a blank.
var declaredTUIClaims = []string{
	"grid-render-real-state",    // a row's glyph tracks its REAL activity, both directions
	"grid-fanout-cross-machine", // the grid shows a peer's thread via the mesh
	"navigation-cursor",         // key nav moves the selection over real rows
	"action-kill",               // the kill key really removes the thread (daemon + runtime)
	"action-archive",            // the archive key really parks the thread
	"action-nav",                // the nav key really switches the tmux client
}

var boundTUIClaims = map[string]func(*testing.T){}

func registerTUIClaim(name string, fn func(*testing.T)) {
	if _, dup := boundTUIClaims[name]; dup {
		panic("duplicate TUI claim: " + name)
	}
	boundTUIClaims[name] = fn
}

// TestTUIClaims runs every bound claim as a subtest.
func TestTUIClaims(t *testing.T) {
	for _, name := range declaredTUIClaims {
		fn := boundTUIClaims[name]
		if fn == nil {
			continue // reported by TestTUIClaimsComplete
		}
		t.Run(name, fn)
	}
}

// TestTUIClaimsComplete fails the build if a declared claim has no test.
func TestTUIClaimsComplete(t *testing.T) {
	for _, c := range declaredTUIClaims {
		if _, ok := boundTUIClaims[c]; !ok {
			t.Errorf("TUI claim %q has no bound test", c)
		}
	}
}

func init() {
	registerTUIClaim("grid-render-real-state", claimGridRenderRealState)
	registerTUIClaim("grid-fanout-cross-machine", claimGridFanout)
	registerTUIClaim("navigation-cursor", claimNavigationCursor)
	// Action claims need the TUI's in-app key handlers, which land next; loud Skip.
	registerTUIClaim("action-kill", func(t *testing.T) { t.Skip("NOT IMPLEMENTED: TUI kill action") })
	registerTUIClaim("action-archive", func(t *testing.T) { t.Skip("NOT IMPLEMENTED: TUI archive action") })
	registerTUIClaim("action-nav", func(t *testing.T) { t.Skip("NOT IMPLEMENTED: TUI nav action") })
}

// render runs the model's REAL fetch command against the daemon and renders the
// result. No fixtures: the rows come from the live HTTP+JSON grid.
func render(t *testing.T, m tui.Model) (tui.Model, string) {
	t.Helper()
	msg := m.Init()() // execute the fetch cmd -> real grid response
	nm, _ := m.Update(msg)
	m2 := nm.(tui.Model)
	return m2, m2.View()
}

// rowLine returns the rendered line mentioning name.
func rowLine(view, name string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	return ""
}

// claimGridRenderRealState: the rendered glyph for a thread matches its REAL
// activity, and FLIPS when reality changes (idle ● -> working ◐ across a real
// turn). This is the matrix's both-directions honesty applied to rendering.
func claimGridRenderRealState(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "tuithread", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")

	m := tui.New(sb.Home+"/daemon.sock", false)

	// DATA is real (from the daemon, not a fixture): a row with our real machine,
	// name, and the independently-fetched activity. RENDER is faithful: the view
	// line shows the glyph for that activity.
	m, view := render(t, m)
	row, ok := rowByName(m, "tuithread")
	if !ok {
		t.Fatalf("TUI did not render the thread row; view:\n%s", view)
	}
	if row.Machine != sb.Machine {
		t.Errorf("row machine = %q, want the real %q (fixture?)", row.Machine, sb.Machine)
	}
	if got := sb.threadStatus(t, th.ID).Activity; row.Activity != got {
		t.Errorf("row activity = %q, but the daemon says %q", row.Activity, got)
	}
	if !strings.Contains(rowLine(view, "tuithread"), tui.Glyph(row)) {
		t.Errorf("rendered line does not show the glyph for the row's activity: %q", rowLine(view, "tuithread"))
	}

	// Change reality: a real turn -> both the row's activity AND its rendered glyph
	// must flip to working (the matrix's both-directions rule, applied to the TUI).
	sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how DNS works")
	if !waitUntil(30*time.Second, func() bool {
		mm, v := render(t, m)
		r, ok := rowByName(mm, "tuithread")
		return ok && r.Activity == api.ActivityWorking &&
			strings.Contains(rowLine(v, "tuithread"), tui.Glyph(r))
	}) {
		t.Fatalf("TUI grid never reflected the working state of a real turn")
	}
}

// rowByName returns the model row with the given thread name.
func rowByName(m tui.Model, name string) (api.ThreadRow, bool) {
	for _, r := range m.Rows() {
		if r.Name == name {
			return r, true
		}
	}
	return api.ThreadRow{}, false
}

// claimGridFanout: the grid (--all-machines) renders a PEER's thread, fetched via
// the mesh — proving cross-machine state is real, not local-only.
func claimGridFanout(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	there := peer.newHeadlessThread(t, "pi", "onpeer")

	m := tui.New(local.Home+"/daemon.sock", true) // all-machines
	m, view := render(t, m)
	row, ok := rowByName(m, "onpeer")
	if !ok {
		t.Fatalf("fan-out TUI did not render the peer thread %s; view:\n%s", there.ID, view)
	}
	// The row is the PEER's (real cross-machine data), and it is rendered.
	if row.Machine != peer.Machine {
		t.Errorf("peer row machine = %q, want %q", row.Machine, peer.Machine)
	}
	if rowLine(view, "onpeer") == "" {
		t.Errorf("peer thread not visible in the rendered view")
	}
}

// claimNavigationCursor: cursor keys move the selection over the REAL rows.
func claimNavigationCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	a := sb.newHeadlessThread(t, "pi", "alpha")
	b := sb.newHeadlessThread(t, "pi", "beta")

	m := tui.New(sb.Home+"/daemon.sock", false)
	m, _ = render(t, m)
	if len(m.Rows()) < 2 {
		t.Fatalf("expected >=2 rows, got %d", len(m.Rows()))
	}
	first, _ := m.Selected()
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(tui.Model)
	second, _ := m.Selected()
	if first.ID == second.ID {
		t.Errorf("cursor did not move on KeyDown")
	}
	// The two selections are real, distinct threads we created.
	ids := map[string]bool{a.ID: true, b.ID: true}
	if !ids[first.ID] || !ids[second.ID] {
		t.Errorf("selected rows are not the real threads: %s, %s", first.ID, second.ID)
	}
}
