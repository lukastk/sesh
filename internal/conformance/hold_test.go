package conformance

// thread.hold cells: parking a thread until a future instant. The HONEST proof is
// the derived `on_hold` flag flipping BOTH directions against the OWNING daemon's
// clock — a future deadline reads on-hold, a PAST deadline reads off-hold (the
// auto-expiry the feature rests on), and a clear zeroes the record. Remote = a
// routed hold over a real ssh hop, asserted on the peer's own snapshot.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.hold", matrix.AgentAgnostic, matrix.Local, testHoldLocal)
	matrix.RegisterTest("thread.hold", matrix.AgentAgnostic, matrix.Remote, testHoldRemote)
}

// snapRowOnHold reads the maintained snapshot from a daemon socket and returns one
// thread's live (OnHold, present) — the owner-derived flag the views filter on.
func snapRowOnHold(t *testing.T, c *client.Client, id string) (api.ThreadSnapshot, bool) {
	t.Helper()
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		return api.ThreadSnapshot{}, false
	}
	for _, r := range snap.Threads {
		if r.ID == id {
			return r, true
		}
	}
	return api.ThreadSnapshot{}, false
}

func testHoldLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "held")
	c := client.New(sb.Home + "/daemon.sock")

	// A fresh thread is not on hold (record 0, derived false once it appears).
	if !waitUntil(10*time.Second, func() bool { _, ok := snapRowOnHold(t, c, th.ID); return ok }) {
		t.Fatalf("thread never appeared in the snapshot")
	}
	if r, _ := snapRowOnHold(t, c, th.ID); r.OnHold || r.OnHoldUntilUnix != 0 {
		t.Fatalf("new thread should not be on hold: on_hold=%v until=%d", r.OnHold, r.OnHoldUntilUnix)
	}

	// Hold until a FUTURE instant → derived on_hold flips true, record persists it.
	future := time.Now().Add(48 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--until-unix", strconv.FormatInt(future, 10)); err != nil {
		t.Fatalf("hold --until-unix: %v\n%s", err, stderr)
	}
	if threadByName(t, sb, "held").OnHoldUntilUnix != future {
		t.Fatalf("hold deadline not persisted")
	}
	if !waitUntil(5*time.Second, func() bool { r, ok := snapRowOnHold(t, c, th.ID); return ok && r.OnHold }) {
		t.Errorf("future hold did not derive on_hold=true")
	}

	// Hold until a PAST instant → the deadline persists but on_hold derives FALSE
	// (auto-expiry: now > deadline). This is the OTHER direction — the bug-class the
	// codex one-directional check missed — proving the flag tracks the clock, not a
	// stored bit.
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--until-unix", "100"); err != nil {
		t.Fatalf("hold past: %v\n%s", err, stderr)
	}
	if !waitUntil(5*time.Second, func() bool { r, ok := snapRowOnHold(t, c, th.ID); return ok && !r.OnHold }) {
		r, _ := snapRowOnHold(t, c, th.ID)
		t.Errorf("a lapsed hold should read off-hold, got on_hold=%v until=%d", r.OnHold, r.OnHoldUntilUnix)
	}

	// --clear zeroes the record.
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--clear"); err != nil {
		t.Fatalf("hold --clear: %v\n%s", err, stderr)
	}
	if threadByName(t, sb, "held").OnHoldUntilUnix != 0 {
		t.Errorf("--clear did not zero the hold deadline")
	}

	// A bad date is a LOUD error, not a silent no-op.
	if _, _, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--until", "nope"); err == nil {
		t.Errorf("a bad --until date should fail loudly")
	}
	// Exactly one of the mutually exclusive flags is required.
	if _, _, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID); err == nil {
		t.Errorf("hold with no deadline flag should fail loudly")
	}

	// --- INHERITANCE: a held parent parks its children (effective = max(own, ancestor)). ---
	parent := sb.newHeadlessThread(t, "pi", "hparent")
	child := sb.newHeadlessThreadParented(t, "pi", "hchild", parent.ID)
	pHold := time.Now().Add(72 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", parent.ID, "--until-unix", strconv.FormatInt(pHold, 10)); err != nil {
		t.Fatalf("hold parent: %v\n%s", err, stderr)
	}
	// The child has NO own hold but inherits the parent's → on_hold true, effective = parent's.
	if !waitUntil(5*time.Second, func() bool {
		r, ok := snapRowOnHold(t, c, child.ID)
		return ok && r.OnHold && r.OnHoldEffectiveUnix == pHold && r.OnHoldUntilUnix == 0
	}) {
		r, _ := snapRowOnHold(t, c, child.ID)
		t.Errorf("child did not inherit parent's hold: on_hold=%v eff=%d own=%d (want on_hold=true eff=%d own=0)",
			r.OnHold, r.OnHoldEffectiveUnix, r.OnHoldUntilUnix, pHold)
	}
	// The child's OWN later hold wins (max): set it further out.
	cHold := time.Now().Add(240 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", child.ID, "--until-unix", strconv.FormatInt(cHold, 10)); err != nil {
		t.Fatalf("hold child: %v\n%s", err, stderr)
	}
	if !waitUntil(5*time.Second, func() bool {
		r, ok := snapRowOnHold(t, c, child.ID)
		return ok && r.OnHoldEffectiveUnix == cHold
	}) {
		r, _ := snapRowOnHold(t, c, child.ID)
		t.Errorf("child's own later hold should win: eff=%d, want %d", r.OnHoldEffectiveUnix, cHold)
	}
	// Releasing the PARENT leaves the child held by its OWN hold (inheritance only adds).
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", parent.ID, "--clear"); err != nil {
		t.Fatalf("clear parent: %v\n%s", err, stderr)
	}
	if !waitUntil(5*time.Second, func() bool {
		r, ok := snapRowOnHold(t, c, child.ID)
		return ok && r.OnHold && r.OnHoldEffectiveUnix == cHold
	}) {
		t.Errorf("child should stay held by its own hold after the parent is released")
	}
	// And the parent itself is no longer held.
	if !waitUntil(5*time.Second, func() bool { r, ok := snapRowOnHold(t, c, parent.ID); return ok && !r.OnHold }) {
		t.Errorf("parent should be off-hold after --clear")
	}
}

func testHoldRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "rheld")

	// The hold ROUTES over a real ssh hop to the owning daemon and persists there.
	future := time.Now().Add(48 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--until-unix", strconv.FormatInt(future, 10)); err != nil {
		t.Fatalf("routed hold: %v\n%s", err, stderr)
	}
	if threadByName(t, sb, "rheld").OnHoldUntilUnix != future {
		t.Fatalf("routed hold did not persist on the peer")
	}
	// The OWNING daemon (the peer, reached directly at its own socket) derives on_hold.
	c := client.New(sb.Home + "/daemon.sock")
	if !waitUntil(5*time.Second, func() bool { r, ok := snapRowOnHold(t, c, th.ID); return ok && r.OnHold }) {
		t.Errorf("routed future hold did not derive on_hold=true on the peer")
	}
	// Clearing routes too.
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--clear"); err != nil {
		t.Fatalf("routed clear: %v\n%s", err, stderr)
	}
	if threadByName(t, sb, "rheld").OnHoldUntilUnix != 0 {
		t.Errorf("routed --clear did not zero the hold on the peer")
	}
}
