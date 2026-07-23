package conformance

import (
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

// thread.done-seen (schema 43): "finished while you weren't looking" as
// first-class state. The derivation is agent-independent (a busy→idle edge +
// the attachment/activity axes), so the feature is agent-agnostic and the
// cells drive it with a real pi. Both directions are proven, per the honesty
// rules: an UNATTENDED turn end sets done, a real nested attach (the seen
// signal — tmux switch/attach bumps no client_activity, so the FLIP is the
// signal) clears it, and a turn ending while freshly attended never sets it.
func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.done-seen", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testDoneSeen(t, loc) })
	}
}

func testDoneSeen(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, "pi", "done", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")
	if s := sb.threadSnapshot(t, th.ID); s.Done {
		t.Fatalf("fresh thread already reads done")
	}

	// (1) A real turn run DETACHED: when it settles, done must be set (it
	// finished and nobody was watching).
	sb.sendKeys(t, pane, "Reply with exactly the word FINISHED and nothing else.")
	if !waitUntil(30*time.Second, func() bool { return sb.threadSnapshot(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("turn never started")
	}
	if !waitUntil(120*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return s.Busy == api.BusyIdle && s.Done && s.DoneSinceUnix > 0
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("detached turn end never set done (busy=%s done=%v since=%d)", s.Busy, s.Done, s.DoneSinceUnix)
	}

	// (2) SEEN: attach a real nested client to the thread's session — the
	// attachment flip is the "user navigated onto it" signal and must clear
	// done without any keystroke.
	sb.attachViewer(t, "sesh_done")
	if !waitUntil(15*time.Second, func() bool {
		s := sb.threadSnapshot(t, th.ID)
		return !s.Done && s.DoneSinceUnix == 0 && s.Attachment == api.Attached
	}) {
		s := sb.threadSnapshot(t, th.ID)
		t.Fatalf("attach never cleared done (done=%v attachment=%s)", s.Done, s.Attachment)
	}

	// (3) The negative direction: a turn ending while the session is freshly
	// attended (the viewer attached seconds ago — its client_activity is
	// within the attended window) must NOT set done.
	sb.sendKeys(t, pane, "Reply with exactly the word AGAIN and nothing else.")
	if !waitUntil(30*time.Second, func() bool { return sb.threadSnapshot(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("second turn never started")
	}
	if !waitUntil(120*time.Second, func() bool { return sb.threadSnapshot(t, th.ID).Busy == api.BusyIdle }) {
		t.Fatalf("second turn never settled")
	}
	// Settle a few maintainer ticks: done must STAY false, not flicker in.
	time.Sleep(1 * time.Second)
	if s := sb.threadSnapshot(t, th.ID); s.Done {
		t.Fatalf("attended turn end wrongly set done")
	}
}
