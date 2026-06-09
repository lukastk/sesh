package conformance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, tr := range meshTransports {
		tr := tr
		matrix.RegisterTest("thread.list-all"+tr.suffix, matrix.AgentAgnostic, matrix.Remote,
			func(t *testing.T) { testThreadListAll(t, tr) })
	}
}

// setupFanoutPair starts a local daemon (the one that fans out) and a peer daemon,
// and registers the peer with `local` over tr's transport. For http the peer's TCP
// API is exposed and it is registered with --api-addr/--api-token AND a deliberately
// BROKEN ssh dest, so a green http cell PROVES the live fan-out reached the peer over
// HTTP (a silent ssh attempt would fail). Returns (local, peer).
func setupFanoutPair(t *testing.T, tr meshTransport) (local, peer *Sandbox) {
	t.Helper()
	ensureSSHLocalhost(t)
	bin := seshBin(t)
	local = newSandbox(t, matrix.Local)
	local.startDaemon(t)

	sshDest := "localhost"
	var extraAdd []string
	switch tr.name {
	case "http":
		addr := freePort(t)
		token := fmt.Sprintf("fan-token-%d", time.Now().UnixNano())
		peer = newSandbox(t, matrix.Local, withAPI(addr, token))
		extraAdd = []string{"--api-addr", addr, "--api-token", token}
		sshDest = "http-only.invalid"
	case "ssh":
		peer = newSandbox(t, matrix.Local)
	default:
		t.Fatalf("unknown transport %q", tr.name)
	}
	peer.startDaemon(t)

	add := []string{"peer", "add", "--machine", peer.Machine, "--ssh", sshDest, "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket}
	add = append(add, extraAdd...)
	if _, stderr, err := local.Runner.Run(t, add...); err != nil {
		t.Fatalf("peer add (%s): %v\n%s", tr.name, err, stderr)
	}
	return local, peer
}

// testThreadListAll asserts the daemon-side mesh fan-out: a local daemon, asked for
// ?all-machines, aggregates its OWN threads with every reachable PEER's (each stamped
// with its real owning machine) over the peer's transport (ssh or http). Both
// machines' threads must appear, attributed correctly — not just the local ones.
func testThreadListAll(t *testing.T, tr meshTransport) {
	if testing.Short() {
		t.Skip("short mode")
	}
	local, peer := setupFanoutPair(t, tr)

	// One thread on each machine (cheap headless records).
	here := local.newHeadlessThread(t, "pi", "onlocal")
	there := peer.newHeadlessThread(t, "pi", "onpeer")

	// A plain local list sees only the local thread...
	if hasThread(local.listThreads(t), there.ID) {
		t.Fatalf("local-only list leaked the peer's thread")
	}
	// ...but the fan-out sees BOTH, each attributed to its owning machine.
	all := local.listThreadsAll(t)
	byID := map[string]api.Thread{}
	for _, th := range all {
		byID[th.ID] = th
	}
	if _, ok := byID[here.ID]; !ok {
		t.Errorf("fan-out missing the local thread")
	}
	pt, ok := byID[there.ID]
	if !ok {
		t.Fatalf("fan-out missing the PEER's thread (cross-machine read failed)")
	}
	if pt.Machine != peer.Machine {
		t.Errorf("peer thread machine = %q, want %q", pt.Machine, peer.Machine)
	}
	if byID[here.ID].Machine != local.Machine {
		t.Errorf("local thread machine = %q, want %q", byID[here.ID].Machine, local.Machine)
	}
}

// listThreadsAll runs `thread list --all-machines --json`.
func (sb *Sandbox) listThreadsAll(t *testing.T) []api.Thread {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "list", "--all-machines", "--json")
	if err != nil {
		t.Fatalf("thread list --all-machines: %v\n%s", err, stderr)
	}
	var out []api.Thread
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var th api.Thread
		if err := json.Unmarshal([]byte(line), &th); err != nil {
			t.Fatalf("decode thread line %q: %v", line, err)
		}
		out = append(out, th)
	}
	return out
}
