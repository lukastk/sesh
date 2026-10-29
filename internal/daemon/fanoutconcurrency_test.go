package daemon

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/peers"
)

// peerFanOut is the shared body of every all-machines read (threads, grid). These
// tests pin the three properties the callers depend on: every peer is asked at the
// same time (so one slow machine does not delay the rest), the output is in registry
// order whatever order the answers arrive in, and a peer the liveness cache knows is
// down is never dialled.

func testPeers(machines ...string) []peers.Peer {
	out := make([]peers.Peer, 0, len(machines))
	for _, m := range machines {
		out = append(out, peers.Peer{Machine: m, SSH: m, Home: "/home/" + m})
	}
	return out
}

func TestPeerFanOutAsksEveryPeerConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond
	list := testPeers("a", "b", "c", "d")

	start := time.Now()
	rows, unreachable := peerFanOut(list, nil, func(p peers.Peer) ([]string, error) {
		time.Sleep(delay) // every peer is equally slow
		return []string{p.Machine}, nil
	})
	elapsed := time.Since(start)

	if len(unreachable) != 0 {
		t.Errorf("unreachable = %v, want none", unreachable)
	}
	if got, want := fmt.Sprint(rows), "[a b c d]"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	// Serially this was 4*delay. Concurrently it is ~delay; allow a generous
	// margin so the assertion is about SUM vs MAX, not about scheduler jitter.
	if elapsed > 2*delay {
		t.Errorf("fan-out took %v for %d peers of %v each — not concurrent", elapsed, len(list), delay)
	}
}

func TestPeerFanOutKeepsRegistryOrderNotCompletionOrder(t *testing.T) {
	list := testPeers("first", "second", "third")
	// The LAST peer answers first and the first peer answers last.
	delays := map[string]time.Duration{"first": 120 * time.Millisecond, "second": 60 * time.Millisecond, "third": 0}

	rows, _ := peerFanOut(list, nil, func(p peers.Peer) ([]string, error) {
		time.Sleep(delays[p.Machine])
		return []string{p.Machine + "/1", p.Machine + "/2"}, nil
	})

	want := "[first/1 first/2 second/1 second/2 third/1 third/2]"
	if got := fmt.Sprint(rows); got != want {
		t.Errorf("rows = %s, want %s (registry order, not completion order)", got, want)
	}
}

func TestPeerFanOutReportsFailuresAndSkipsKnownOfflinePeers(t *testing.T) {
	list := testPeers("up", "broken", "offline")
	var dialled atomic.Int64

	rows, unreachable := peerFanOut(list, map[string]bool{"offline": true}, func(p peers.Peer) ([]string, error) {
		dialled.Add(1)
		if p.Machine == "broken" {
			return nil, fmt.Errorf("connection refused")
		}
		return []string{p.Machine}, nil
	})

	if got, want := fmt.Sprint(rows), "[up]"; got != want {
		t.Errorf("rows = %s, want %s (a failed peer contributes nothing)", got, want)
	}
	// Registry order again: "broken" (failed) before "offline" (skipped).
	if got, want := fmt.Sprint(unreachable), "[broken offline]"; got != want {
		t.Errorf("unreachable = %s, want %s", got, want)
	}
	if got := dialled.Load(); got != 2 {
		t.Errorf("fetch ran %d times, want 2 — a known-offline peer must not be dialled", got)
	}
}
