package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.grid", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testThreadGrid(t, loc) })
	}
}

// testThreadGrid asserts the live-status grid reflects REAL thread state — the
// data the TUI renders. Local: a thread's row tracks waiting -> working across a
// real turn. Remote: the mesh fan-out includes a PEER's thread, with its real
// status and owning machine.
func testThreadGrid(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}

	switch loc {
	case matrix.Local:
		sb := newSandbox(t, matrix.Local)
		sb.startDaemon(t)
		th := sb.newThread(t, "pi", "g", "/tmp")
		pane := sb.waitThreadReady(t, th.ID, "pi")

		// Idle -> the row says waiting.
		if got := gridRow(t, sb, false, th.ID).Activity; got != api.ActivityWaiting {
			t.Errorf("idle thread grid activity = %s, want waiting", got)
		}
		// Real turn -> the row flips to working (the grid tracks reality).
		sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how DNS works")
		if !waitUntil(30*time.Second, func() bool { return gridRow(t, sb, false, th.ID).Activity == api.ActivityWorking }) {
			t.Errorf("grid row never became working during a real turn")
		}

	case matrix.Remote:
		ensureSSHLocalhost(t)
		local := newSandbox(t, matrix.Local)
		local.startDaemon(t)
		peer := newSandbox(t, matrix.Local)
		peer.startDaemon(t)
		bin := seshBin(t)
		if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin); err != nil {
			t.Fatalf("peer add: %v\n%s", err, stderr)
		}
		// A thread on the PEER, settled to waiting.
		th := peer.newThread(t, "pi", "g", "/tmp")
		peer.waitThreadReady(t, th.ID, "pi")
		// The local daemon's fan-out grid includes it, with its real status + machine.
		row := gridRow(t, local, true, th.ID)
		if row.ID == "" {
			t.Fatalf("fan-out grid missing the peer's thread")
		}
		if row.Machine != peer.Machine {
			t.Errorf("peer row machine = %q, want %q", row.Machine, peer.Machine)
		}
		if row.Activity != api.ActivityWaiting {
			t.Errorf("peer row activity = %s, want waiting", row.Activity)
		}
	}
}

// gridRow fetches the grid (optionally all-machines) and returns the row for id.
func gridRow(t *testing.T, sb *Sandbox, allMachines bool, id string) api.ThreadRow {
	t.Helper()
	args := []string{"thread", "grid", "--json"}
	if allMachines {
		args = append(args, "--all-machines")
	}
	stdout, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread grid: %v\n%s", err, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var row api.ThreadRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("decode grid row %q: %v", line, err)
		}
		if row.ID == id {
			return row
		}
	}
	return api.ThreadRow{}
}
