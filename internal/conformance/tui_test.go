package conformance

import (
	"os/exec"
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
	"mesh-render-offline",       // a downed peer renders OFFLINE, its threads still listed
	"action-stop",               // the stop key really ends the runtime, keeps the record
	"action-delete",             // the delete key really drops the record
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
	registerTUIClaim("mesh-render-offline", claimMeshRenderOffline)
	registerTUIClaim("action-stop", claimActionStop)
	registerTUIClaim("action-delete", claimActionDelete)
	registerTUIClaim("action-archive", claimActionArchive)
	registerTUIClaim("action-nav", claimActionNav)
}

// claimActionNav: the nav key really switches the tmux client to the selected
// thread's session — across the real master/inner tmux nesting, over a real ssh
// hop, via the nav primitive. Asserted on the real tmux servers.
func claimActionNav(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	// The peer machine: a real daemon. Its tmux socket is the inner mytmux.
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	th := peer.newThread(t, "pi", "navme", "/tmp") // session sesh_navme on the peer
	peer.waitThreadReady(t, th.ID, "pi")

	// A master tmux with a window for the peer machine, NOT currently focused.
	master := "sesh-tuinavmaster-" + th.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", peer.Machine)
	mustTmux(t, master, "select-window", "-t", "m:home")

	// The local client machine: knows the peer (incl. its tmux socket) and the
	// master socket. The TUI runs here.
	bin := seshBin(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_MASTER_SOCKET=" + master}

	m := tui.New(local.Home+"/daemon.sock", true).WithExec(bin, navEnv)
	m, _ = renderUntilRow(t, m, "navme") // wait for the mesh sync to replicate it

	// Enter -> nav. Asserts on the REAL servers:
	if m = runKey(t, m, "enter"); m.LastErr() != nil {
		t.Fatalf("nav action errored: %v", m.LastErr())
	}
	// outer: the master switched to the peer's window.
	if got := activeWindowOf(t, master); got != peer.Machine {
		t.Errorf("master active window = %q, want %q (outer switch failed)", got, peer.Machine)
	}
	// inner: the peer's tmux now has a client on the thread's session (bare-shell
	// kick, since nothing was attached).
	if !waitUntil(5*time.Second, func() bool { return innerClientSession(t, peer.TmuxSocket) == "sesh_navme" }) {
		t.Errorf("peer tmux client not on sesh_navme; got %q (inner switch failed)", innerClientSession(t, peer.TmuxSocket))
	}
}

func mustTmux(t *testing.T, socket string, args ...string) {
	t.Helper()
	if out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("tmux -L %s %v: %v\n%s", socket, args, err, out)
	}
}

func activeWindowOf(t *testing.T, socket string) string {
	t.Helper()
	out, _ := exec.Command("tmux", "-L", socket, "list-windows", "-F", "#{?window_active,#{window_name},}").Output()
	return strings.TrimSpace(string(out))
}

func innerClientSession(t *testing.T, socket string) string {
	t.Helper()
	out, _ := exec.Command("tmux", "-L", socket, "list-clients", "-F", "#{client_session}").Output()
	return strings.TrimSpace(string(out))
}

// runKey drives a keypress, runs the command it produces (the real action), and
// feeds the result back through Update — how a test exercises an in-app action end
// to end. The returned model's LastErr() carries any action error.
func runKey(t *testing.T, m tui.Model, key string) tui.Model {
	t.Helper()
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	m2 := nm.(tui.Model)
	if cmd == nil {
		return m2
	}
	nm2, _ := m2.Update(cmd())
	return nm2.(tui.Model)
}

// claimActionStop: the stop key really ends the runtime (real session + agent
// process dead) but KEEPS the record (still listed, now dead) — the stop/delete
// split, observed on the real daemon + tmux server.
func claimActionStop(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "stopme", "/tmp")
	var pid int
	if !waitUntil(agentStartTimeout, func() bool {
		_, p, ok := sb.markedPane(t, th.ID)
		pid = p
		return ok && agentRunningUnder(p, "pi")
	}) {
		t.Fatalf("agent never came up")
	}

	m := tui.New(sb.Home+"/daemon.sock", false)
	m, _ = renderUntilRow(t, m, "stopme") // single thread => cursor on it
	if m = runKey(t, m, "x"); m.LastErr() != nil {
		t.Fatalf("stop action errored: %v", m.LastErr())
	}
	// The REAL runtime is dead...
	if !waitUntil(10*time.Second, func() bool { return !pidAlive(pid) }) {
		t.Errorf("stop action did not kill the agent process")
	}
	if !waitUntil(10*time.Second, func() bool {
		_, err := sb.rawTmux(t, "has-session", "-t", "=sesh_stopme")
		return err != nil
	}) {
		t.Errorf("stop action did not kill the tmux session")
	}
	// ...but the record is KEPT (dead, resumable) — unlike delete.
	if !threadInList(t, sb, th.ID) {
		t.Errorf("stop dropped the record (should keep it)")
	}
}

// claimActionDelete: the delete key really drops the record. (The daemon's orphan
// guard refuses a live delete, so we stop first — the realistic flow.)
func claimActionDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "delme") // dead-by-construction (no runtime)

	m := tui.New(sb.Home+"/daemon.sock", false)
	m, _ = renderUntilRow(t, m, "delme")
	if m = runKey(t, m, "d"); m.LastErr() != nil {
		t.Fatalf("delete action errored: %v", m.LastErr())
	}
	// Observable effect: the record is gone from the daemon.
	if threadInList(t, sb, th.ID) {
		t.Errorf("delete did not drop the record")
	}
}

// claimActionArchive: the archive key really parks the thread (record kept, hidden
// from the active grid).
func claimActionArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "parkme")

	m := tui.New(sb.Home+"/daemon.sock", false)
	m, _ = renderUntilRow(t, m, "parkme")
	m = runKey(t, m, "a")

	// Record kept but hidden from the active list (the daemon truth)...
	if hasThread(sb.listThreads(t), th.ID) {
		t.Errorf("archived thread still in the active list")
	}
	if !hasThread(sb.listThreadsArchived(t), th.ID) {
		t.Errorf("archived thread missing from the archived list (record not kept)")
	}
	// ...and gone from the rendered active grid (after the maintainer re-reads the
	// archived flag and the TUI filters it).
	m = renderUntilGone(t, m, "parkme")
	_ = m
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
	m, row := renderUntilRow(t, m, "tuithread")
	_, view := render(t, m)
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

// The TUI now reads the mesh, which is eventually-consistent: a just-created thread
// appears within a maintainer tick (local) or a sync (peer), not instantly. So
// claims poll the render rather than rendering once.

func renderUntilRow(t *testing.T, m tui.Model, name string) (tui.Model, api.ThreadRow) {
	t.Helper()
	var got api.ThreadRow
	if !waitUntil(20*time.Second, func() bool {
		m, _ = render(t, m)
		r, ok := rowByName(m, name)
		if ok {
			got = r
		}
		return ok
	}) {
		_, view := render(t, m)
		t.Fatalf("row %q never appeared in the TUI; view:\n%s", name, view)
	}
	return m, got
}

func renderUntilCount(t *testing.T, m tui.Model, n int) tui.Model {
	t.Helper()
	if !waitUntil(20*time.Second, func() bool {
		m, _ = render(t, m)
		return len(m.Rows()) >= n
	}) {
		t.Fatalf("TUI never showed >= %d rows", n)
	}
	return m
}

func renderUntilGone(t *testing.T, m tui.Model, name string) tui.Model {
	t.Helper()
	if !waitUntil(20*time.Second, func() bool {
		m, _ = render(t, m)
		_, ok := rowByName(m, name)
		return !ok
	}) {
		t.Fatalf("row %q never disappeared from the TUI", name)
	}
	return m
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
	// The peer thread appears once the mesh sync replicates it (eventually consistent).
	m, row := renderUntilRow(t, m, "onpeer")
	_ = there
	// The row is the PEER's (real cross-machine data), and it is rendered.
	if row.Machine != peer.Machine {
		t.Errorf("peer row machine = %q, want %q", row.Machine, peer.Machine)
	}
	if _, view := render(t, m); rowLine(view, "onpeer") == "" {
		t.Errorf("peer thread not visible in the rendered view")
	}
}

// claimMeshRenderOffline: the rendered mesh view reflects offline browsing — a
// peer that goes down renders as OFFLINE, and its last-known threads stay listed
// (not dropped). The TUI's whole point for a multi-machine rig.
func claimMeshRenderOffline(t *testing.T) {
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
	peer.newHeadlessThread(t, "pi", "onpeer")

	m := tui.New(local.Home+"/daemon.sock", true) // all-machines
	m, _ = renderUntilRow(t, m, "onpeer")         // synced and rendered while up

	// Take the peer down: the render must show it OFFLINE while STILL listing onpeer.
	if _, stderr, err := peer.daemonRunner.Run(t, "daemon", "stop"); err != nil {
		t.Fatalf("stop peer daemon: %v\n%s", err, stderr)
	}
	if !waitUntil(20*time.Second, func() bool {
		mm, view := render(t, m)
		m = mm
		return strings.Contains(view, "OFFLINE") && rowLine(view, "onpeer") != ""
	}) {
		_, view := render(t, m)
		t.Fatalf("TUI never rendered the downed peer as OFFLINE-with-threads; view:\n%s", view)
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
	m = renderUntilCount(t, m, 2)
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
