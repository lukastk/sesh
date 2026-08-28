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

	// A DECOY: a second real client on the peer's work server, parked on a thread the
	// master window never shows. Without it this cell cannot discriminate, and for a
	// long time it did not: `display-message -p -c <client>` ignores -c for format
	// expansion, so master-current returned whatever client tmux ambiently picked —
	// which, with a single client attached, was the right one by luck. A busy machine
	// has several masters watching it, and there the answer was another master's
	// thread. The decoy makes the ambient pick observably wrong.
	decoy := peer.newThread(t, "pi", "curdecoy", "/tmp")
	peer.waitThreadReady(t, decoy.ID, "pi")
	peer.attachViewer(t, decoy.SessionName)
	decoyClient, _ := peer.workClientOn(t, decoy.SessionName)
	// MEASURED: tmux's ambient client pick follows the most recent switch-client, so the
	// decoy only makes this cell discriminating while IT moved last. That is not a
	// contrivance — it is the ordinary state of a shared work server, where another
	// master navigating its own window is the last switch-client to land, and it is
	// exactly when the bug bit.
	bumpDecoy := func() {
		if out, err := peer.rawTmux(t, "switch-client", "-c", decoyClient, "-t", decoy.SessionName); err != nil {
			t.Fatalf("bump decoy client: %v\n%s", err, out)
		}
	}

	// Switch the master window's client to curtgt — a deterministic known thread.
	if _, stderr, err := self.Runner.Run(t, "tmux", "nav", "--to", peer.Machine+":"+tgt.SessionName); err != nil {
		t.Fatalf("nav to curtgt: %v\n%s", err, stderr)
	}
	bumpDecoy() // another master moves last — the ambient pick is now WRONG

	// master-current (routed to the peer) returns curtgt's thread id — what the master
	// window is now showing.
	if !waitUntil(10*time.Second, func() bool {
		out, _, err := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		return err == nil && strings.TrimSpace(out) == tgt.ID
	}) {
		out, _, _ := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		got := strings.TrimSpace(out)
		if got == decoy.ID {
			t.Errorf("master-current returned the DECOY client's thread %s instead of the master window's %s — the resolve is not scoped to the recorded client", decoy.ID, tgt.ID)
		} else {
			t.Errorf("master-current did not return the shown thread %s; got %q", tgt.ID, got)
		}
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
	bumpDecoy() // another master moves last — the ambient pick is now WRONG
	if !waitUntil(10*time.Second, func() bool {
		out, _, err := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		return err == nil && strings.TrimSpace(out) == peerw.ID
	}) {
		out, _, _ := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		got := strings.TrimSpace(out)
		if got == decoy.ID {
			t.Errorf("master-current returned the DECOY client's thread %s instead of tracking the nav to %s", decoy.ID, peerw.ID)
		} else {
			t.Errorf("master-current did not TRACK the nav to peerw (%s); got %q", peerw.ID, got)
		}
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
