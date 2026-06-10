package conformance

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("master.ensure", matrix.AgentAgnostic, matrix.Remote, testMasterEnsure)
}

// testMasterEnsure: losing a master window (prefix+K / kill-window) used to strand the
// user — `master up` is loudly non-idempotent, so a single missing window had no
// recovery (hit live: every nav to that machine failed with "can't find window").
// `master ensure` recreates ONLY the missing windows, with a REAL re-attach into that
// machine's work server (the peer over a real ssh hop), leaves existing windows
// untouched, and no-ops when the cockpit is complete.
func testMasterEnsure(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self, peer := setupMasterPair(t)
	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck
	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) >= 1 }) {
		t.Fatalf("peer window never attached initially")
	}

	// Complete cockpit: ensure is a no-op (window set unchanged).
	before := masterWindowNames(self.MasterSocket)
	out, stderr, err := self.Runner.Run(t, "master", "ensure", "--machines", self.Machine+","+peer.Machine)
	if err != nil {
		t.Fatalf("master ensure (complete): %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "all windows present") {
		t.Errorf("ensure on a complete master should report all-present, got: %q", out)
	}
	if after := masterWindowNames(self.MasterSocket); len(after) != len(before) {
		t.Errorf("ensure on a complete master changed the windows: %v -> %v", before, after)
	}

	// Lose the PEER window (the prefix+K case) — its supervisor dies with it and the
	// peer's work server really loses its client.
	if out, err := exec.Command("tmux", "-L", self.MasterSocket, "kill-window", "-t", masterSessionName+":"+peer.Machine).CombinedOutput(); err != nil {
		t.Fatalf("kill-window: %v: %s", err, out)
	}
	if !waitUntil(15*time.Second, func() bool { return !strSliceHas(masterWindowNames(self.MasterSocket), peer.Machine) }) {
		t.Fatalf("peer window did not die")
	}
	if !waitUntil(15*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) == 0 }) {
		t.Fatalf("peer work server kept a client after kill-window (drop not real)")
	}

	// ensure: ONLY the missing window comes back, and it GENUINELY re-attaches into
	// the peer's work server over the real ssh hop.
	out, stderr, err = self.Runner.Run(t, "master", "ensure", "--machines", self.Machine+","+peer.Machine)
	if err != nil {
		t.Fatalf("master ensure (missing peer): %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "created "+peer.Machine) {
		t.Errorf("ensure should report creating %q, got: %q", peer.Machine, out)
	}
	wins := masterWindowNames(self.MasterSocket)
	if !strSliceHas(wins, peer.Machine) || !strSliceHas(wins, self.Machine) {
		t.Fatalf("windows after ensure = %v, want both machines", wins)
	}
	if !waitUntil(20*time.Second, func() bool { return tmuxClientCount(peer.TmuxSocket) >= 1 }) {
		t.Errorf("recreated peer window never re-attached into the peer's work server")
	}
}
