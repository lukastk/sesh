package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("master.watchers", matrix.AgentAgnostic, matrix.Remote, testMasterWatchers)
}

// testMasterWatchers: `sesh master watchers` reports which origin masters have a LIVE
// window-attach into this machine's work server — by liveness-checking each
// master-client.<origin> marker against the real client list. Both directions (the
// codex-liveness lesson): the origin appears while the real (ssh-attached) master is
// up, and DISAPPEARS after `master down` even though the marker file remains on disk
// (a stale marker must never count).
func testMasterWatchers(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t)
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck

	watchersOf := func(sb *Sandbox) []string {
		out, _, err := sb.Runner.Run(t, "master", "watchers")
		if err != nil {
			return nil
		}
		var ws []string
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				ws = append(ws, l)
			}
		}
		return ws
	}

	// While the master is up: the PEER (attached over a real ssh hop) sees the
	// origin master, and so does self (its local window).
	if !waitUntil(20*time.Second, func() bool { return strSliceHas(watchersOf(peer), self.Machine) }) {
		t.Errorf("peer's watchers never listed %q while its master window was attached; got %v", self.Machine, watchersOf(peer))
	}
	if !waitUntil(20*time.Second, func() bool { return strSliceHas(watchersOf(self), self.Machine) }) {
		t.Errorf("self's watchers never listed %q (local window); got %v", self.Machine, watchersOf(self))
	}

	// Tear the master down: the markers remain on disk, but the clients are gone —
	// the origin must drop out (stale markers never count).
	if _, stderr, err := self.Runner.Run(t, "master", "down"); err != nil {
		t.Fatalf("master down: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool { return !strSliceHas(watchersOf(peer), self.Machine) }) {
		t.Errorf("peer's watchers still list %q after master down (stale marker counted); got %v", self.Machine, watchersOf(peer))
	}
	if !waitUntil(15*time.Second, func() bool { return !strSliceHas(watchersOf(self), self.Machine) }) {
		t.Errorf("self's watchers still list %q after master down; got %v", self.Machine, watchersOf(self))
	}
}
