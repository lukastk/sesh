package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

// drainCmd runs cmd and returns the messages it produces, flattening ONE batch level
// (deliberately not recursing: a tracking tick reschedules itself, so a recursive drain
// would never terminate).
func drainCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c != nil {
			out = append(out, c())
		}
	}
	return out
}

// workClientOn returns the tmux client name and pid attached to session, as tmux itself
// reports them — the pair a master window's attach records in its master-client marker.
func (sb *Sandbox) workClientOn(t *testing.T, session string) (name, pid string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err := sb.rawTmux(t, "list-clients", "-F", "#{client_name}\t#{client_pid}\t#{client_session}")
		if err == nil {
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				f := strings.Split(strings.TrimSpace(l), "\t")
				if len(f) == 3 && f[2] == session {
					return f[0], f[1]
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("no tmux client ever attached to session %q", session)
	return "", ""
}

// claimSidebarTracksCockpit (Lukas, 2026-08-28 — "It would be good if the selected row
// in the sidebar also moved as well. I don't think the current bindings do this which
// makes it a bit confusing"): a nav made from the COCKPIT side must move the persistent
// sidebar's cursor onto that thread.
//
// Everything here is real: a real daemon with two real pi threads, a real tmux client
// attached to the work server and recorded in a real master-client marker (the same
// pair `sesh master window`'s attach writes), a real `sesh tmux nav` subprocess doing
// the switch, the real nav bell it rings, and the real MarkerClientCurrent resolve the
// tracker spends when it sees the bell change. Nothing about the mechanism is stubbed.
func claimSidebarTracksCockpit(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	thA := local.newThread(t, "pi", "track-a", "/tmp")
	local.waitThreadReady(t, thA.ID, "pi")
	thB := local.newThread(t, "pi", "track-b", "/tmp")
	local.waitThreadReady(t, thB.ID, "pi")

	// A REAL master client: a nested tmux client on thread A's session, recorded in the
	// marker so MarkerClientCurrent has a live client to interrogate.
	local.attachViewer(t, thA.SessionName)
	name, pid := local.workClientOn(t, thA.SessionName)
	marker := filepath.Join(local.Home, "master-client."+local.Machine)
	if err := os.WriteFile(marker, []byte(name+" "+pid+"\n"), 0o644); err != nil {
		t.Fatalf("write master-client marker: %v", err)
	}

	// A master server with a window per machine, so the nav's OUTER select has a target.
	master := "sesh-tuitrack-" + thB.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", local.Machine)

	bin := seshBin(t)
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_TMUX_SOCKET=" + local.TmuxSocket, "SESH_MASTER_SOCKET=" + master}
	bell := filepath.Join(local.Home, "nav-bell")
	m := tui.New(local.Home+"/daemon.sock", false).
		WithExec(bin, navEnv).
		WithLocal(local.Machine, local.TmuxSocket).
		WithTmux("/tmp/notwork,1,1").
		WithSidebar().
		WithSidebarFollow(local.Machine)
	// Arm tracking AFTER the initial render: renderUntilRow drives Init()() expecting
	// the LONE fetch cmd a plain Init returns, and a tracked Init batches the ticker in
	// alongside it (which would hand the harness a BatchMsg instead of the mesh).
	m, _ = renderUntilRow(t, m, "track-b")
	m = m.WithMasterTracking(bell)
	t.Setenv("TMUX", "") // never let a focus handoff touch the developer's own tmux

	// The tracker is driven by the model's own resolve; run one round.
	track := func(m tui.Model) tui.Model {
		nm, cmd := m.Update(tui.MasterTrackTick())
		m = nm.(tui.Model)
		for _, msg := range drainCmd(cmd) {
			nm2, _ := m.Update(msg)
			m = nm2.(tui.Model)
		}
		return m
	}
	selected := func(m tui.Model) string {
		if row, ok := m.Selected(); ok {
			return row.Name
		}
		return ""
	}

	// BASELINE: the cockpit is on track-a, so the cursor settles there. Asserting this
	// FIRST is what stops the real assertion passing vacuously — without it a cursor
	// that merely happened to sit on track-b would look like successful tracking.
	if !waitUntil(20*time.Second, func() bool { m = track(m); return selected(m) == "track-a" }) {
		t.Fatalf("cursor never settled on track-a (the thread the cockpit is showing); got %q", selected(m))
	}
	bellBefore := readFileTrimmed(bell)

	// THE COCKPIT MOVES — a real `sesh tmux nav` subprocess, exactly what prefix+. runs.
	navCmd := exec.Command(bin, "tmux", "nav", "--to", local.Machine+":"+thB.SessionName, "--thread", thB.ID)
	navCmd.Env = sandboxEnv(map[string]string{
		"SESH_HOME": local.Home, "SESH_MACHINE": local.Machine,
		"SESH_TMUX_SOCKET": local.TmuxSocket, "SESH_MASTER_SOCKET": master,
	})
	if out, err := navCmd.CombinedOutput(); err != nil {
		t.Fatalf("cockpit nav: %v\n%s", err, out)
	}
	// The nav really rang the bell (the "forcible refresh" channel — without it the
	// cursor would only catch up on the slow backstop poll).
	if got := readFileTrimmed(bell); got == "" || got == bellBefore {
		t.Fatalf("nav did not ring the nav bell at %s (before=%q after=%q)", bell, bellBefore, got)
	}
	// The real client really moved (independent of sesh's own view of it).
	if got := clientSession(t, local, name); got != thB.SessionName {
		t.Fatalf("the master client is on session %q, want %q — the nav did not land", got, thB.SessionName)
	}

	// ...and the sidebar's cursor follows it.
	if !waitUntil(20*time.Second, func() bool { m = track(m); return selected(m) == "track-b" }) {
		t.Fatalf("the sidebar cursor never followed the cockpit onto track-b (still %q) — the reported bug", selected(m))
	}
}

// claimSidebarArrowNoRevert (Lukas, pocket4, 2026-09-14 — "As I go up and down it sort of
// janks a bit and pre-selects or reverts to selecting some of those sessions that I was
// previously on. It seems to jump back and forth"): arrowing the sidebar must never have
// the cockpit tracker drag the cursor back onto a thread the sidebar itself just navved
// through.
//
// The race, reproduced with real parts: ↓ fires a follow to b; a second ↓ to c is
// swallowed while it runs; b's nav really lands; a tracker resolve issued now really
// reports b — the thread the cursor has already left. Delivering it before b's follow
// reports is exactly the ordering bubbletea's concurrent commands produce. A tracker that
// applied it moved the cursor back to b, then forward to c once c landed.
//
// Real: a daemon with three real pi threads, a real tmux client recorded in a real
// master-client marker, the follows' real TmuxNav switches on the daemon, and the
// tracker's real MarkerClientCurrent resolve. Only the MESSAGE ORDER is chosen.
func claimSidebarArrowNoRevert(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	threads := map[string]api.Thread{}
	for _, name := range []string{"arrow-a", "arrow-b", "arrow-c"} {
		th := local.newThread(t, "pi", name, "/tmp")
		local.waitThreadReady(t, th.ID, "pi")
		threads[name] = th
	}
	thA := threads["arrow-a"]

	local.attachViewer(t, thA.SessionName)
	client, pid := local.workClientOn(t, thA.SessionName)
	marker := filepath.Join(local.Home, "master-client."+local.Machine)
	if err := os.WriteFile(marker, []byte(client+" "+pid+"\n"), 0o644); err != nil {
		t.Fatalf("write master-client marker: %v", err)
	}
	master := "sesh-tuiarrow-" + thA.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", local.Machine)

	bin := seshBin(t)
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_TMUX_SOCKET=" + local.TmuxSocket, "SESH_MASTER_SOCKET=" + master}
	m := tui.New(local.Home+"/daemon.sock", false).
		WithExec(bin, navEnv).
		WithLocal(local.Machine, local.TmuxSocket).
		WithTmux("/tmp/notwork,1,1").
		WithSidebar().
		WithSidebarFollow(local.Machine)
	m, _ = renderUntilRow(t, m, "arrow-c") // armed after the first render, as in sidebar-tracks-cockpit
	m = m.WithMasterTracking(filepath.Join(local.Home, "nav-bell"))
	t.Setenv("TMUX", "") // never let a focus handoff touch the developer's own tmux

	selected := func() string {
		if row, ok := m.Selected(); ok {
			return row.Name
		}
		return ""
	}
	update := func(msg tea.Msg) tea.Cmd {
		nm, cmd := m.Update(msg)
		m = nm.(tui.Model)
		return cmd
	}
	// resolveNow drives tracker ticks until the backstop expires, delivering the resolve
	// that spends — or, when the tracker deliberately issues none (a follow in flight),
	// giving up once more ticks have passed than the backstop could ever need. A tick
	// reschedules itself, so the rescheduled tick in each batch is not re-delivered.
	resolveNow := func() {
		for i := 0; i < 16; i++ {
			for _, msg := range drainCmd(update(tui.MasterTrackTick())) {
				if msg == tui.MasterTrackTick() {
					continue
				}
				update(msg)
				return
			}
		}
	}
	onSession := func(want string) {
		t.Helper()
		if !waitUntil(10*time.Second, func() bool { return clientSession(t, local, client) == want }) {
			t.Fatalf("the cockpit client is on %q, want %q — a follow nav did not land", clientSession(t, local, client), want)
		}
	}

	// BASELINE: the cockpit shows arrow-a, and tracking settles the cursor there.
	if !waitUntil(20*time.Second, func() bool { resolveNow(); return selected() == "arrow-a" }) {
		t.Fatalf("baseline: cursor never settled on arrow-a (the thread the cockpit shows); got %q", selected())
	}

	followB := update(tea.KeyMsg{Type: tea.KeyDown})
	if followB == nil || selected() != "arrow-b" {
		t.Fatalf("baseline: ↓ onto arrow-b must fire a follow (cmd=%v, cursor=%q)", followB != nil, selected())
	}
	if cmd := update(tea.KeyMsg{Type: tea.KeyDown}); cmd != nil || selected() != "arrow-c" {
		t.Fatalf("baseline: a ↓ during the follow must be swallowed onto arrow-c (cmd=%v, cursor=%q)", cmd != nil, selected())
	}

	doneB := followB() // b's nav really lands on the daemon...
	onSession(threads["arrow-b"].SessionName)
	resolveNow() // ...and the tracker resolves while b's follow has not yet reported
	if got := selected(); got != "arrow-c" {
		t.Fatalf("cursor = %q — the tracker dragged the selection back onto a thread the sidebar had just navved through (the reported bug)", got)
	}

	followC := update(doneB) // the coalesce fires the follow to arrow-c
	if followC == nil {
		t.Fatalf("b's completion did not coalesce-fire the follow to arrow-c")
	}
	resolveNow()
	if got := selected(); got != "arrow-c" {
		t.Fatalf("cursor = %q — a resolve during the follow to arrow-c reverted the selection", got)
	}
	update(followC())
	onSession(threads["arrow-c"].SessionName)
	resolveNow()
	if got := selected(); got != "arrow-c" {
		t.Fatalf("cursor = %q after arrow-c landed, want arrow-c", got)
	}
}

// clientSession reports which session a tmux client is currently on, read straight from
// tmux rather than through sesh.
func clientSession(t *testing.T, sb *Sandbox, client string) string {
	t.Helper()
	out, err := sb.rawTmux(t, "list-clients", "-F", "#{client_name}\t#{client_session}")
	if err != nil {
		t.Fatalf("list-clients: %v", err)
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(strings.TrimSpace(l), "\t")
		if len(f) == 2 && f[0] == client {
			return f[1]
		}
	}
	return ""
}

func readFileTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
