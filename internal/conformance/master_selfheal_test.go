package conformance

import (
	"os/exec"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("master.selfheal", matrix.AgentAgnostic, matrix.Remote, testMasterSelfheal)
}

// testMasterSelfheal: the cockpit invariant — "any machine that is CONNECTED has a
// master window" — holds by itself: the daemon's convergence loop recreates a window
// lost to prefix+K/kill-window (with a REAL re-attach into that machine's work server,
// the peer over a real ssh hop), never forces a window for an UNREACHABLE machine,
// and never resurrects a master that was deliberately taken down.
func testMasterSelfheal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t) // self-heal ON (the default)

	// A registered-but-unreachable peer (dead-end ssh, no API): the mesh marks it
	// unreachable; the healer must never force a window for it.
	if _, stderr, err := self.Runner.Run(t, "peer", "add", "--machine", "ghost", "--ssh", "ghost.invalid", "--home", "/nonexistent", "--binary", "/nonexistent", "--tmux-socket", "ghost"); err != nil {
		t.Fatalf("peer add ghost: %v\n%s", err, stderr)
	}

	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck
	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) >= 1 }) {
		t.Fatalf("peer window never attached initially")
	}

	// Lose the peer window (prefix+K): the supervisor dies with it and the peer's
	// work server REALLY loses its client...
	if out, err := exec.Command("tmux", "-L", self.MasterSocket, "kill-window", "-t", masterSessionName+":"+peer.Machine).CombinedOutput(); err != nil {
		t.Fatalf("kill-window: %v: %s", err, out)
	}
	if !waitUntil(15*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) == 0 }) {
		t.Fatalf("peer work server kept a client after kill-window (drop not real)")
	}

	// ...and the healer brings it back ON ITS OWN: the window reappears and a real
	// client re-attaches into the peer's work server over the ssh hop. No ensure call.
	if !waitUntil(30*time.Second, func() bool {
		return strSliceHas(masterWindowNames(self.MasterSocket), peer.Machine) && tmuxClientCount(peer.TmuxSocket) >= 1
	}) {
		t.Errorf("self-heal never recreated the peer window (windows=%v, peer clients=%d)",
			masterWindowNames(self.MasterSocket), tmuxClientCount(peer.TmuxSocket))
	}

	// The unreachable peer never got a window forced on it.
	if strSliceHas(masterWindowNames(self.MasterSocket), "ghost") {
		t.Errorf("self-heal created a window for the UNREACHABLE peer 'ghost': %v", masterWindowNames(self.MasterSocket))
	}

	// Deliberate teardown stays down — the healer must not resurrect the master.
	if _, stderr, err := self.Runner.Run(t, "master", "down"); err != nil {
		t.Fatalf("master down: %v\n%s", err, stderr)
	}
	time.Sleep(2*masterMaintTickForTest + time.Second)
	if out, err := exec.Command("tmux", "-L", self.MasterSocket, "has-session", "-t", "="+masterSessionName).CombinedOutput(); err == nil {
		t.Errorf("self-heal RESURRECTED a deliberately downed master: %s", out)
	}
}

// masterMaintTickForTest mirrors the daemon's tick (5s) without importing daemon
// internals into the conformance package.
const masterMaintTickForTest = 5 * time.Second
