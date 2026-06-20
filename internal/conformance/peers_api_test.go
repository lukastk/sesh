package conformance

// The /v1/peers HTTP CRUD exercised over a real daemon's unix socket against the
// real peers.json: add → list shows it → remove → list drops it; removing an unknown
// peer is a LOUD error (not a silent success). The HTTP surface the GUI uses to manage
// the mesh, kept honest the same way peer_remove_test keeps the CLI honest.

import (
	"context"
	"testing"

	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/peers"
)

func hasPeerNamed(ps []peers.Peer, machine string) bool {
	for _, p := range ps {
		if p.Machine == machine {
			return true
		}
	}
	return false
}

func TestPeersAPI(t *testing.T) {
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	c := client.New(sb.Home + "/daemon.sock")
	ctx := context.Background()

	// Add an http-transport peer.
	added, err := c.PeerAdd(ctx, peers.Peer{
		Machine: "ghost", SSH: "nobody@ghost.invalid", Home: "/tmp/ghost-home",
		ApiAddr: "ghost.invalid:7878", ApiToken: "tok",
	})
	if err != nil {
		t.Fatalf("PeerAdd: %v", err)
	}
	if !hasPeerNamed(added.Peers, "ghost") {
		t.Fatalf("add response missing ghost: %+v", added.Peers)
	}

	// It shows up in a fresh list (persisted to peers.json).
	list, err := c.Peers(ctx)
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if !hasPeerNamed(list.Peers, "ghost") {
		t.Fatalf("list missing ghost after add: %+v", list.Peers)
	}

	// Remove it.
	after, err := c.PeerRemove(ctx, "ghost")
	if err != nil {
		t.Fatalf("PeerRemove: %v", err)
	}
	if hasPeerNamed(after.Peers, "ghost") {
		t.Errorf("ghost still present after remove: %+v", after.Peers)
	}

	// Removing an unknown peer is loud, not a silent success.
	if _, err := c.PeerRemove(ctx, "nosuch"); err == nil {
		t.Errorf("removing an unknown peer succeeded silently")
	}

	// Add requires a machine name and an ssh destination.
	if _, err := c.PeerAdd(ctx, peers.Peer{Machine: "nossh"}); err == nil {
		t.Errorf("peer add without ssh succeeded")
	}
}
