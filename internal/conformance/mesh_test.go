package conformance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

// meshTransport is the daemon↔daemon transport a mesh cell exercises. The SAME test
// body runs over BOTH (ssh and http) as DISTINCT matrix cells (feature-name suffix),
// so the grid enforces SSH↔HTTP parity: it can only go all-green if the peer's
// snapshot replicates correctly over EACH transport — neither can silently rot.
type meshTransport struct {
	name   string // "ssh" | "http"
	suffix string // "" for ssh (the baseline cell), ".http" for http
}

var meshTransports = []meshTransport{
	{name: "ssh", suffix: ""},
	{name: "http", suffix: ".http"},
}

func init() {
	for _, tr := range meshTransports {
		tr := tr
		matrix.RegisterTest("mesh.snapshot"+tr.suffix, matrix.AgentAgnostic, matrix.Remote,
			func(t *testing.T) { testMeshSnapshot(t, tr) })
		matrix.RegisterTest("mesh.offline-listing"+tr.suffix, matrix.AgentAgnostic, matrix.Remote,
			func(t *testing.T) { testMeshOfflineListing(t, tr) })
	}
}

// setupMeshPair starts a local daemon + a peer daemon and registers the peer with
// `local` over tr's transport. For ssh it is a plain `peer add`; for http the peer
// daemon is started with its TCP API exposed and registered with --api-addr/--api-token,
// so `local`'s mesh sync pulls the peer's snapshot over HTTP, not ssh. Returns a
// client to the LOCAL daemon (which serves the merged /v1/mesh) and the peer sandbox.
func setupMeshPair(t *testing.T, tr meshTransport) (peer *Sandbox, local *client.Client) {
	t.Helper()
	ensureSSHLocalhost(t)
	localSB := newSandbox(t, matrix.Local)
	localSB.startDaemon(t)

	// HONESTY: for the http transport the peer's ssh destination is deliberately
	// BROKEN (an unresolvable .invalid host). The mesh sync's only network call is
	// fetchPeerSnapshot, which for an http peer must go over the TCP API — so if the
	// code ever silently fell back to ssh, ssh would fail and the cell would go red.
	// A green http cell therefore PROVES the snapshot was pulled over HTTP, not ssh.
	sshDest := "localhost"
	var extraAdd []string
	switch tr.name {
	case "http":
		addr := freePort(t)
		token := fmt.Sprintf("mesh-token-%d", time.Now().UnixNano())
		peer = newSandbox(t, matrix.Local, withAPI(addr, token))
		extraAdd = []string{"--api-addr", addr, "--api-token", token}
		sshDest = "http-only.invalid" // ssh here cannot connect — only HTTP can carry the sync
	case "ssh":
		peer = newSandbox(t, matrix.Local)
	default:
		t.Fatalf("unknown mesh transport %q", tr.name)
	}
	peer.startDaemon(t)

	bin := seshBin(t)
	add := []string{"peer", "add", "--machine", peer.Machine, "--ssh", sshDest, "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket}
	add = append(add, extraAdd...)
	if _, stderr, err := localSB.Runner.Run(t, add...); err != nil {
		t.Fatalf("peer add (%s): %v\n%s", tr.name, err, stderr)
	}
	return peer, client.New(localSB.Home + "/daemon.sock")
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
func testMeshOfflineListing(t *testing.T, tr meshTransport) {
	if testing.Short() {
		t.Skip("short mode")
	}
	peer, c := setupMeshPair(t, tr)
	there := peer.newHeadlessThread(t, "pi", "onpeer")

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
func testMeshSnapshot(t *testing.T, tr meshTransport) {
	if testing.Short() {
		t.Skip("short mode")
	}
	peer, c := setupMeshPair(t, tr)
	there := peer.newHeadlessThread(t, "pi", "onpeer")

	var pview api.MachineView
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
					pview, row = mv, th
					return true
				}
			}
		}
		return false
	}) {
		t.Fatalf("local mesh never synced the peer's thread %s from %s", there.ID, peer.Machine)
	}

	// The peer's data is attributed to the peer, fresh, and self=false.
	if pview.Self {
		t.Errorf("peer view marked self=true")
	}
	if !pview.Reachable {
		t.Errorf("freshly-synced peer marked unreachable")
	}
	if row.Machine != peer.Machine || row.Name != "onpeer" {
		t.Errorf("peer thread mis-attributed: machine=%q name=%q", row.Machine, row.Name)
	}

	// The local machine itself is in the merged view, marked self (and is NOT the
	// peer — the self entry is the querying daemon).
	mesh, _ := c.Mesh(context.Background())
	var haveSelf bool
	for _, mv := range mesh.Machines {
		if mv.Self && mv.Machine != peer.Machine {
			haveSelf = true
		}
	}
	if !haveSelf {
		t.Errorf("merged mesh missing the local machine (self)")
	}

	// PEER-FACING SLIM (issue #1): archiving the (headless => no live pane) peer
	// thread must drop it from the peer's served snapshot, and thus from the local
	// cached mesh view — over THIS real transport (http: the slimmed /v1/snapshot;
	// ssh: the slimmed `thread snapshot --json`). A second live thread proves the
	// sync itself keeps flowing, so the absence is the filter, not a dead sync.
	stays := peer.newHeadlessThread(t, "pi", "stays")
	if !waitUntil(15*time.Second, func() bool {
		mv, ok := peerView(t, c, peer.Machine)
		return ok && hasThreadID(mv, stays.ID)
	}) {
		t.Fatalf("second peer thread never synced (presence first — never settle on absence alone)")
	}
	if _, stderr, err := peer.daemonRunner.Run(t, "thread", "archive", "--id", there.ID); err != nil {
		t.Fatalf("archive peer thread: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool {
		mv, ok := peerView(t, c, peer.Machine)
		return ok && !hasThreadID(mv, there.ID) && hasThreadID(mv, stays.ID)
	}) {
		mv, _ := peerView(t, c, peer.Machine)
		t.Fatalf("archived+dead peer thread still in the cached mesh view (archived=%v stays=%v) — the peer-facing snapshot must slim it",
			hasThreadID(mv, there.ID), hasThreadID(mv, stays.ID))
	}

	// STEADY-STATE FRESHNESS: with the peer's state now byte-stable, the cache's
	// synced_at must keep advancing (on http that is exactly the 304/touch path —
	// the payload no longer transfers, but freshness must not go stale).
	mv1, _ := peerView(t, c, peer.Machine)
	if !waitUntil(10*time.Second, func() bool {
		mv2, ok := peerView(t, c, peer.Machine)
		return ok && mv2.SyncedAtUnix > mv1.SyncedAtUnix && mv2.Reachable
	}) {
		t.Fatalf("peer freshness stopped advancing once its snapshot went unchanged (the conditional-fetch path must still touch synced_at)")
	}
}
