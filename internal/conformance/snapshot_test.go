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
	for _, a := range matrix.AllAgents {
		a := a
		matrix.RegisterTest("thread.snapshot", a, matrix.Local,
			func(t *testing.T) { testThreadSnapshot(t, string(a)) })
	}
}

// testThreadSnapshot asserts the background state maintainer (L1, _dev/MESH.md): the
// snapshot reflects REAL live state, is an O(1) read (NOT the old ~3s on-demand
// probe), and tracks waiting->working across a real turn.
func testThreadSnapshot(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, agent, "snap", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, agent)

	c := client.New(sb.Home + "/daemon.sock")
	row := func() (api.ThreadSnapshot, bool) {
		snap, err := c.Snapshot(context.Background())
		if err != nil {
			return api.ThreadSnapshot{}, false
		}
		for _, r := range snap.Threads {
			if r.ID == th.ID {
				return r, true
			}
		}
		return api.ThreadSnapshot{}, false
	}

	// The row carries real, self-contained identity.
	if !waitUntil(10*time.Second, func() bool { _, ok := row(); return ok }) {
		t.Fatalf("thread never appeared in the snapshot")
	}
	r, _ := row()
	if r.Machine != sb.Machine || r.AgentKind != agent {
		t.Errorf("snapshot row identity wrong: machine=%q agent=%q", r.Machine, r.AgentKind)
	}
	// Settles to waiting (the maintainer's rolling window may lag the daemon's
	// readiness probe by up to one busy-window, so poll rather than assert instantly).
	if !waitUntil(5*time.Second, func() bool { r, ok := row(); return ok && r.Activity == api.ActivityWaiting }) {
		r, _ := row()
		t.Errorf("idle thread snapshot activity = %q, want waiting", r.Activity)
	}

	// O(1) read: a snapshot must return fast — it serves the maintained state, it
	// does NOT run the old blocking ~3s content-diff probe.
	start := time.Now()
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot read: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("snapshot read took %v — should be an O(1) read, not an on-demand probe", elapsed)
	}

	// Tracks reality: a real turn flips the maintained activity to working.
	sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how DNS works")
	if !waitUntil(30*time.Second, func() bool {
		r, ok := row()
		return ok && r.Activity == api.ActivityWorking
	}) {
		t.Fatalf("snapshot never reflected the working state of a real turn")
	}
}
