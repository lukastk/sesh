package conformance

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// Master-tmux infrastructure. Remote: the proof requires a real ssh hop (the peer
	// window ssh-attaches into the peer's work server). ssh-localhost stands in.
	matrix.RegisterTest("master.up", matrix.AgentAgnostic, matrix.Remote, testMasterUp)
	matrix.RegisterTest("master.reconnect", matrix.AgentAgnostic, matrix.Remote, testMasterReconnect)
}

// setupMasterPair starts a self daemon + a peer daemon (ssh-localhost), registers the
// peer (with its work socket, for the master attach), and puts a real headed thread on
// each so each work server has a session to attach into. Returns (self, peer).
func setupMasterPair(t *testing.T) (self, peer *Sandbox) {
	t.Helper()
	ensureSSHLocalhost(t)
	self = newSandbox(t, matrix.Local)
	self.startDaemon(t)
	peer = newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := self.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	// Headed threads => a real session exists on each work server (so attach succeeds).
	self.newThread(t, "pi", "selfw", "/tmp")
	peer.newThread(t, "pi", "peerw", "/tmp")
	if !waitUntil(agentStartTimeout, func() bool { return tmuxSessionCount(self.TmuxSocket) >= 1 }) {
		t.Fatalf("self work server never got a session")
	}
	if !waitUntil(agentStartTimeout, func() bool { return tmuxSessionCount(peer.TmuxSocket) >= 1 }) {
		t.Fatalf("peer work server never got a session")
	}
	return self, peer
}

// testMasterUp asserts `sesh master up` builds the cockpit: one window per machine,
// named after the machine, each GENUINELY attached into THAT machine's work server
// (a real tmux client appears — for the peer, created over a real ssh hop). Asserted
// via raw tmux, never internal state.
func testMasterUp(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t)
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	// One window per machine, named after the machine.
	wins := masterWindowNames(self.MasterSocket)
	if !strSliceHas(wins, self.Machine) || !strSliceHas(wins, peer.Machine) {
		t.Fatalf("master windows = %v, want both %q and %q", wins, self.Machine, peer.Machine)
	}

	// Each window's supervisor genuinely attaches into that machine's WORK server.
	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(self.TmuxSocket) >= 1 }) {
		t.Errorf("self window never attached into self's work server (%s)", self.TmuxSocket)
	}
	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) >= 1 }) {
		t.Errorf("peer window never attached into the peer's work server over ssh (%s)", peer.TmuxSocket)
	}
}

// testMasterReconnect asserts the per-window supervisor self-heals: after the attach
// is dropped, it re-establishes — for both the local window and the ssh-localhost peer
// window. The drop is observed (client count really hits 0) before the heal, so a
// no-op can't pass.
func testMasterReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t)
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	for _, m := range []struct {
		name   string
		socket string
	}{{"self", self.TmuxSocket}, {"peer(ssh)", peer.TmuxSocket}} {
		// initial attach
		if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(m.socket) >= 1 }) {
			t.Fatalf("%s window never attached initially", m.name)
		}
		// DROP it — detach the supervisor's client.
		exec.Command("tmux", "-L", m.socket, "detach-client").Run() //nolint:errcheck
		// The drop really happened (count hits 0) — so the heal below isn't a no-op.
		if !waitUntil(10*time.Second, func() bool { return tmuxClientCount(m.socket) == 0 }) {
			t.Errorf("%s: detach did not drop the client", m.name)
		}
		// HEAL — the supervisor re-establishes the attach.
		if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(m.socket) >= 1 }) {
			t.Errorf("%s window did not reconnect after the drop", m.name)
		}
	}
}

// --- raw tmux helpers (assert the observable effect, not sesh's own state) ---

func masterWindowNames(socket string) []string {
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", masterSessionName, "-F", "#{window_name}").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

func tmuxClientCount(socket string) int  { return tmuxLineCount(socket, "list-clients") }
func tmuxSessionCount(socket string) int { return tmuxLineCount(socket, "list-sessions") }

func tmuxLineCount(socket, sub string) int {
	out, err := exec.Command("tmux", "-L", socket, sub).Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func strSliceHas(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

const masterSessionName = "master"
