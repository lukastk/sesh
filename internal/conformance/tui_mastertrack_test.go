package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
