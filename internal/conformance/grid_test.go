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
	// Local grid (no fan-out).
	matrix.RegisterTest("thread.grid", matrix.AgentAgnostic, matrix.Local, testThreadGridLocal)
	// Remote grid (mesh fan-out) over BOTH transports — thread.grid (ssh) and
	// thread.grid.http run the same body, enforcing SSH↔HTTP fan-out parity.
	for _, tr := range meshTransports {
		tr := tr
		matrix.RegisterTest("thread.grid"+tr.suffix, matrix.AgentAgnostic, matrix.Remote,
			func(t *testing.T) { testThreadGridRemote(t, tr) })
	}
}

// testThreadGridLocal asserts the local live-status grid reflects REAL thread state:
// a thread's row tracks waiting -> working across a real turn (the data the TUI renders).
func testThreadGridLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
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
}

// testThreadGridRemote asserts the mesh fan-out grid includes a PEER's thread with
// its real status and owning machine, fetched over the peer's transport (ssh or http).
func testThreadGridRemote(t *testing.T, tr meshTransport) {
	if testing.Short() {
		t.Skip("short mode")
	}
	local, peer := setupFanoutPair(t, tr)
	// A thread on the PEER, settled to waiting.
	th := peer.newThread(t, "pi", "g", "/tmp")
	peer.waitThreadReady(t, th.ID, "pi")
	// The local daemon's fan-out grid includes it, with its real status + machine.
	row := gridRow(t, local, true, th.ID)
	if row.ID == "" {
		t.Fatalf("fan-out grid missing the peer's thread over %s", tr.name)
	}
	if row.Machine != peer.Machine {
		t.Errorf("peer row machine = %q, want %q", row.Machine, peer.Machine)
	}
	if row.Activity != api.ActivityWaiting {
		t.Errorf("peer row activity = %s, want waiting", row.Activity)
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
