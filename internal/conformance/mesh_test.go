package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("mesh.snapshot", matrix.AgentAgnostic, matrix.Remote, testMeshSnapshot)
	matrix.RegisterTest("mesh.offline-listing", matrix.AgentAgnostic, matrix.Remote, testMeshOfflineListing)
}

// peerView returns the merged-mesh view for machine, if present.
func peerView(t *testing.T, c *client.Client, machine string) (api.MachineView, bool) {
	t.Helper()
	mesh, err := c.Mesh(context.Background())
	if err != nil {
		return api.MachineView{}, false
	}
	for _, mv := range mesh.Machines {
		if mv.Machine == machine {
			return mv, true
		}
	}
	return api.MachineView{}, false
}

func hasThreadID(mv api.MachineView, id string) bool {
	for _, th := range mv.Threads {
		if th.ID == id {
			return true
		}
	}
	return false
}

// testMeshOfflineListing is the offline-browsing guarantee: when a peer's daemon
// goes down, the mesh KEEPS its last-known threads (marked reachable=false) rather
// than dropping them; when the peer returns, it refreshes to reachable. Asserted on
// the real daemons over a real ssh hop.
func testMeshOfflineListing(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	there := peer.newHeadlessThread(t, "pi", "onpeer")
	c := client.New(local.Home + "/daemon.sock")

	// First, the peer is reachable and its thread is synced.
	if !waitUntil(15*time.Second, func() bool {
		mv, ok := peerView(t, c, peer.Machine)
		return ok && mv.Reachable && hasThreadID(mv, there.ID)
	}) {
		t.Fatalf("peer thread never synced into the local mesh")
	}

	// Take the peer DOWN.
	if _, stderr, err := peer.daemonRunner.Run(t, "daemon", "stop"); err != nil {
		t.Fatalf("stop peer daemon: %v\n%s", err, stderr)
	}
	// The peer is now reported unreachable BUT its thread is STILL listed (offline
	// browsing — last-known state retained, not dropped).
	if !waitUntil(15*time.Second, func() bool {
		mv, ok := peerView(t, c, peer.Machine)
		return ok && !mv.Reachable
	}) {
		t.Fatalf("downed peer never marked unreachable")
	}
	mv, _ := peerView(t, c, peer.Machine)
	if !hasThreadID(mv, there.ID) {
		t.Errorf("downed peer's thread was DROPPED (offline browsing must retain it)")
	}

	// Bring the peer BACK — it refreshes to reachable.
	peer.startDaemon(t)
	if !waitUntil(15*time.Second, func() bool {
		mv, ok := peerView(t, c, peer.Machine)
		return ok && mv.Reachable && hasThreadID(mv, there.ID)
	}) {
		t.Fatalf("recovered peer never refreshed to reachable")
	}
}

// testMeshSnapshot asserts the L2 mesh sync (_dev/MESH.md): a local daemon's
// background sync REPLICATES a peer's snapshot into its cache over a real ssh hop,
// so the merged GET /v1/mesh — read LOCALLY, no per-query ssh — shows the peer's
// thread with its live state, attributed to the peer, marked reachable.
func testMeshSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	there := peer.newHeadlessThread(t, "pi", "onpeer")

	c := client.New(local.Home + "/daemon.sock")
	var peerView api.MachineView
	var row api.ThreadSnapshot
	if !waitUntil(15*time.Second, func() bool {
		mesh, err := c.Mesh(context.Background())
		if err != nil {
			return false
		}
		for _, mv := range mesh.Machines {
			if mv.Machine != peer.Machine {
				continue
			}
			for _, th := range mv.Threads {
				if th.ID == there.ID {
					peerView, row = mv, th
					return true
				}
			}
		}
		return false
	}) {
		t.Fatalf("local mesh never synced the peer's thread %s from %s", there.ID, peer.Machine)
	}

	// The peer's data is attributed to the peer, fresh, and self=false.
	if peerView.Self {
		t.Errorf("peer view marked self=true")
	}
	if !peerView.Reachable {
		t.Errorf("freshly-synced peer marked unreachable")
	}
	if row.Machine != peer.Machine || row.Name != "onpeer" {
		t.Errorf("peer thread mis-attributed: machine=%q name=%q", row.Machine, row.Name)
	}

	// The local machine itself is in the merged view, marked self.
	mesh, _ := c.Mesh(context.Background())
	var haveSelf bool
	for _, mv := range mesh.Machines {
		if mv.Machine == local.Machine && mv.Self {
			haveSelf = true
		}
	}
	if !haveSelf {
		t.Errorf("merged mesh missing the local machine (self)")
	}
}
