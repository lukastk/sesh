package conformance

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
)

func init() {
	matrix.RegisterTest("tmux.master-current", matrix.AgentAgnostic, matrix.Remote, testMasterCurrent)
}

// testMasterCurrent proves `tmux master-current` resolves the thread a master window
// is CURRENTLY showing — the data behind the TUI's async master prefix+s preselect.
// It reads the origin master's marker client on the target machine and returns that
// client's active-pane thread. Tested over the real master + a real ssh-localhost peer
// (routed), and it TRACKS the client: after nav switches the master window to another
// thread, master-current returns the new one (not a stale snapshot).
func testMasterCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t)
	// A second thread on the peer to switch the master window to.
	tgt := peer.newThread(t, "pi", "curtgt", "/tmp")
	peer.waitThreadReady(t, tgt.ID, "pi")

	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	// Wait for the peer window's supervisor to attach (over real ssh) and record its marker.
	marker := tmux.MasterClientMarker(peer.Home, self.Machine)
	if !waitUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(marker)
		return err == nil && len(strings.Fields(string(b))) == 2
	}) {
		t.Fatalf("the peer window's attach never recorded a marker at %s", marker)
	}

	// Switch the master window's client to curtgt — a deterministic known thread.
	if _, stderr, err := self.Runner.Run(t, "tmux", "nav", "--to", peer.Machine+":"+tgt.SessionName); err != nil {
		t.Fatalf("nav to curtgt: %v\n%s", err, stderr)
	}

	// master-current (routed to the peer) returns curtgt's thread id — what the master
	// window is now showing.
	if !waitUntil(10*time.Second, func() bool {
		out, _, err := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		return err == nil && strings.TrimSpace(out) == tgt.ID
	}) {
		out, _, _ := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		t.Errorf("master-current did not return the shown thread %s; got %q", tgt.ID, strings.TrimSpace(out))
	}
	// --session returns the SESSION name the window shows (the prefix+a picker preselect).
	if out, _, err := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine, "--session"); err != nil {
		t.Errorf("master-current --session: %v", err)
	} else if strings.TrimSpace(out) != tgt.SessionName {
		t.Errorf("master-current --session = %q, want the shown session %q", strings.TrimSpace(out), tgt.SessionName)
	}

	// Track the change: nav the window to the FIRST thread (sesh_peerw) and re-resolve.
	peerw := threadByName(t, peer, "peerw")
	if _, stderr, err := self.Runner.Run(t, "tmux", "nav", "--to", peer.Machine+":"+peerw.SessionName); err != nil {
		t.Fatalf("nav to peerw: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool {
		out, _, err := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		return err == nil && strings.TrimSpace(out) == peerw.ID
	}) {
		out, _, _ := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		t.Errorf("master-current did not TRACK the nav to peerw (%s); got %q", peerw.ID, strings.TrimSpace(out))
	}

	// DAEMON-SIDE routing (schema 36): the TUI's master-cursor preselect for a REMOTE
	// active window no longer forks `sesh … --machine`; it asks its OWN daemon, passing
	// machine=peer, and the daemon routes the resolve to the peer over its mesh transport.
	// Drive that exact path: a direct client to self's daemon with machine=peer must return
	// the same thread the routed CLI does (peerw) — proving self's daemon did the hop.
	selfClient := client.New(self.Home + "/daemon.sock")
	if !waitUntil(10*time.Second, func() bool {
		_, tid, _, err := selfClient.TmuxMasterCurrent(context.Background(), self.Machine, peer.Machine)
		return err == nil && tid == peerw.ID
	}) {
		_, tid, _, err := selfClient.TmuxMasterCurrent(context.Background(), self.Machine, peer.Machine)
		t.Errorf("daemon-routed master-current (machine=%s) = %q (err %v), want peerw %s", peer.Machine, tid, err, peerw.ID)
	}
}
