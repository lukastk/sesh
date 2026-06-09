package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.list-all", matrix.AgentAgnostic, matrix.Remote, testThreadListAll)
}

// testThreadListAll asserts the daemon-side mesh fan-out: a local daemon, asked
// for ?all-machines, aggregates its OWN threads with every reachable PEER's (each
// stamped with its real owning machine) over a real ssh hop. Both machines'
// threads must appear, attributed correctly — not just the local ones.
func testThreadListAll(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	local := newSandbox(t, matrix.Local) // the machine that fans out
	local.startDaemon(t)
	peer := newSandbox(t, matrix.Local) // a second real machine with its own daemon
	peer.startDaemon(t)

	// Register the peer with the local daemon's mesh registry.
	bin := seshBin(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}

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
