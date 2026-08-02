package conformance

// thread.await cells (PARITY_ROADMAP C1): `sesh await` really blocks until a
// REAL turn finishes — per agent, both localities. The remote cells run await
// LOCALLY with NO routing for a PEER's thread (v1's mesh-read design: turn
// state replicates, so awaiting needs no forwarding). The pi/local cell also
// covers: already-idle returns immediately, --timeout expiry is loud during a
// real in-flight turn, and unknown ids are loud.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range []matrix.Agent{matrix.Claude, matrix.Codex, matrix.Pi} {
		a := agent
		matrix.RegisterTest("thread.await", a, matrix.Local,
			func(t *testing.T) { testAwaitTurn(t, string(a), matrix.Local) })
		matrix.RegisterTest("thread.await", a, matrix.Remote,
			func(t *testing.T) { testAwaitTurn(t, string(a), matrix.Remote) })
	}
}

func testAwaitTurn(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}

	// Remote: the thread lives on a PEER; await runs on the LOCAL daemon with
	// no --machine — the mesh replicates the turn state (the design under test).
	var sb *Sandbox    // where the thread lives
	var awaiter Runner // where await runs
	var awaitMesh *client.Client
	if loc == matrix.Remote {
		ensureSSHLocalhost(t)
		local := newSandbox(t, matrix.Local)
		local.startDaemon(t)
		peer := newSandbox(t, matrix.Local)
		peer.startDaemon(t)
		if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost",
			"--home", peer.Home, "--binary", seshBin(t), "--tmux-socket", peer.TmuxSocket); err != nil {
			t.Fatalf("peer add: %v\n%s", err, stderr)
		}
		sb, awaiter = peer, local.Runner
		awaitMesh = client.New(local.Home + "/daemon.sock")
	} else {
		sb = newSandbox(t, matrix.Local)
		sb.startDaemon(t)
		awaiter = sb.Runner
		awaitMesh = client.New(sb.Home + "/daemon.sock")
	}

	th := sb.newHeadlessThread(t, agent, "awaitme")
	meshState := func(want api.Busy) bool {
		mesh, err := awaitMesh.Mesh(context.Background())
		if err != nil {
			return false
		}
		for _, mv := range mesh.Machines {
			for _, row := range mv.Threads {
				if row.ID == th.ID && (want == "" || row.Busy == want) {
					return true
				}
			}
		}
		return false
	}
	// A remote await reads the LOCAL replicated mesh. Establish that the thread
	// has actually published into that view before testing its turn transition;
	// otherwise an exact UUID can race the peer's first snapshot and the cell is
	// testing initial replication, not await. Local uses the same observable
	// condition so both localities follow one sequence.
	if !waitUntil(15*time.Second, func() bool { return meshState("") }) {
		t.Fatalf("[%s/%s] awaiter's mesh never observed thread %s", agent, loc, th.ID)
	}

	// Kick a REAL turn and await it CONCURRENTLY: await must return only after
	// the turn completes (the reply must already be available the moment it
	// does — that is the contract a script depends on).
	var wg sync.WaitGroup
	var awaitOut, awaitErr string
	var awaitFail error
	wg.Add(1)
	go func() {
		defer wg.Done()
		if !waitUntil(45*time.Second, func() bool { return meshState(api.BusyBusy) }) {
			awaitFail = fmt.Errorf("awaiter's mesh never observed the real turn become busy")
			return
		}
		awaitOut, awaitErr, awaitFail = awaiter.Run(t, "await", th.ID, "--timeout", "180s")
	}()
	sb.headlessTurn(t, th.ID, "Reply with exactly: AWAITED")
	wg.Wait()
	if awaitFail != nil {
		t.Fatalf("[%s/%s] await failed: %v\n%s", agent, loc, awaitFail, awaitErr)
	}
	if !strings.Contains(awaitOut, "idle") {
		t.Errorf("await output missing the settled state: %q", awaitOut)
	}
	reply := sb.headlessReply(t, th.ID)
	if reply.Working || !strings.Contains(reply.Reply, "AWAITED") {
		t.Errorf("await returned before the turn really finished (working=%v reply=%q)", reply.Working, reply.Reply)
	}

	if loc == matrix.Remote || agent != "pi" {
		return // the edge cases below are agent/locality-independent: pi/local owns them
	}

	// Already idle: returns immediately.
	start := time.Now()
	if _, stderr, err := awaiter.Run(t, "await", th.ID, "--timeout", "30s"); err != nil {
		t.Fatalf("await on idle: %v\n%s", err, stderr)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("await on an idle thread took %s (want immediate)", time.Since(start))
	}

	// Timeout during a real in-flight turn: loud non-zero.
	done := make(chan struct{})
	go func() {
		defer close(done)
		sb.headlessTurn(t, th.ID, "Write a detailed 300-word explanation of how DNS resolution works end to end")
	}()
	if !waitUntil(45*time.Second, func() bool { return meshState(api.BusyBusy) }) {
		t.Fatal("awaiter mesh never observed the timeout probe turn as busy")
	}
	if _, stderr, err := awaiter.Run(t, "await", th.ID, "--timeout", "1s"); err == nil {
		t.Errorf("await --timeout 1s during a real turn succeeded (must be loud)")
	} else if !strings.Contains(stderr, "still") {
		t.Errorf("timeout error wrong: %s", stderr)
	}
	<-done

	// Unknown id: loud.
	if _, stderr, err := awaiter.Run(t, "await", "ffffffff"); err == nil {
		t.Errorf("await on an unknown id succeeded")
	} else if !strings.Contains(stderr, "ffffffff") {
		t.Errorf("unknown-id error does not echo the ref: %s", stderr)
	}
}
