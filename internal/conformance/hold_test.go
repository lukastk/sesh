package conformance

// thread.hold cells: parking a thread until a future instant. The HONEST proof is
// the derived `on_hold` flag flipping BOTH directions against the OWNING daemon's
// clock — a future deadline reads on-hold, a PAST deadline reads off-hold (the
// auto-expiry the feature rests on), and a clear zeroes the record. Remote = a
// routed hold over a real ssh hop, asserted on the peer's own snapshot.

import (
	"context"
	"strconv"
	"strings"
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

	// --- RELEASE (schema 48): the escape hatch from the inherited max. ---
	// Fresh three-generation tree, all held by the ROOT alone, so every assertion
	// below is about inheritance rather than about anything's own deadline.
	gp := sb.newHeadlessThread(t, "pi", "rel-root")
	kid := sb.newHeadlessThreadParented(t, "pi", "rel-kid", gp.ID)
	grandkid := sb.newHeadlessThreadParented(t, "pi", "rel-grandkid", kid.ID)
	rootHold := time.Now().Add(72 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", gp.ID, "--until-unix", strconv.FormatInt(rootHold, 10)); err != nil {
		t.Fatalf("hold rel-root: %v\n%s", err, stderr)
	}
	// BASELINE first: without it every "is free" assertion below could pass vacuously.
	if !waitUntil(5*time.Second, func() bool {
		k, ok1 := snapRowOnHold(t, c, kid.ID)
		g, ok2 := snapRowOnHold(t, c, grandkid.ID)
		return ok1 && ok2 && k.OnHold && g.OnHold
	}) {
		t.Fatalf("baseline: the whole subtree should be parked by the root's hold")
	}

	// A --clear on a thread parked by an ANCESTOR must FAIL LOUDLY, naming the
	// ancestor and the remedy. This is the reported defect: it used to exit 0 with
	// "hold cleared" while the thread stayed parked, so a caller could not tell that
	// nothing had happened. The record must also be unchanged in a way that matters.
	_, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", kid.ID, "--clear")
	if err == nil {
		t.Errorf("--clear on a thread held by its ancestor must fail loudly, not report success")
	} else {
		if !strings.Contains(stderr, "STILL on hold") || !strings.Contains(stderr, "--release") {
			t.Errorf("the refusal must say it is still held and name --release, got: %s", stderr)
		}
		if !strings.Contains(stderr, "rel-root") {
			t.Errorf("the refusal must NAME the ancestor responsible, got: %s", stderr)
		}
	}
	if r, _ := snapRowOnHold(t, c, kid.ID); !r.OnHold {
		t.Errorf("the failed clear should have left the thread held (it is the ancestor's hold)")
	}

	// --release frees the thread AND its subtree, while the ancestor stays parked —
	// the whole point: a child can now be worked on without un-parking its parent.
	relUntil := time.Now().Add(24 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", kid.ID, "--release", "--until-unix", strconv.FormatInt(relUntil, 10)); err != nil {
		t.Fatalf("release kid: %v\n%s", err, stderr)
	}
	if !waitUntil(5*time.Second, func() bool {
		k, ok1 := snapRowOnHold(t, c, kid.ID)
		g, ok2 := snapRowOnHold(t, c, grandkid.ID)
		return ok1 && ok2 && !k.OnHold && !g.OnHold
	}) {
		k, _ := snapRowOnHold(t, c, kid.ID)
		g, _ := snapRowOnHold(t, c, grandkid.ID)
		t.Errorf("release should free the thread and its subtree: kid.on_hold=%v grandkid.on_hold=%v", k.OnHold, g.OnHold)
	}
	if r, ok := snapRowOnHold(t, c, gp.ID); !ok || !r.OnHold {
		t.Errorf("releasing a child must NOT un-park its ancestor")
	}
	if got := threadByName(t, sb, "rel-kid").HoldReleaseUntilUnix; got != relUntil {
		t.Errorf("release deadline not persisted: %d, want %d", got, relUntil)
	}

	// Setting an own hold on a RELEASED thread clears the release: the two states are
	// mutually exclusive, so a thread is never both held and released.
	kidHold := time.Now().Add(12 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", kid.ID, "--until-unix", strconv.FormatInt(kidHold, 10)); err != nil {
		t.Fatalf("hold a released thread: %v\n%s", err, stderr)
	}
	if rec := threadByName(t, sb, "rel-kid"); rec.HoldReleaseUntilUnix != 0 || rec.OnHoldUntilUnix != kidHold {
		t.Errorf("a hold must replace the release: release=%d hold=%d", rec.HoldReleaseUntilUnix, rec.OnHoldUntilUnix)
	}

	// A release AUTO-EXPIRES, and the ancestor's hold snaps back on with NO further
	// write — the maintainer has to schedule a sweep for the release deadline itself.
	// Nothing is written during the wait, so a missing schedule leaves the row reading
	// un-held forever. (Short deadline on purpose; the flip must be observed live.)
	soon := time.Now().Add(2 * time.Second).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", kid.ID, "--release", "--until-unix", strconv.FormatInt(soon, 10)); err != nil {
		t.Fatalf("short release: %v\n%s", err, stderr)
	}
	if !waitUntil(5*time.Second, func() bool { r, ok := snapRowOnHold(t, c, kid.ID); return ok && !r.OnHold }) {
		t.Fatalf("the short release should free the thread first")
	}
	if !waitUntil(15*time.Second, func() bool { r, ok := snapRowOnHold(t, c, kid.ID); return ok && r.OnHold }) {
		t.Errorf("a LAPSED release must re-park the thread under its ancestor's hold, with no record write to prompt it")
	}

	// Held AND released at once has no meaning: the API refuses it rather than
	// silently picking one (which would be the plausible-but-wrong class).
	if err := postBothHoldFields(c, kid.ID); err == nil {
		t.Errorf("setting a hold and a release together must be refused")
	}

	// --- ARCHIVED detaches from the max, like a release. ---
	// A hold parks ACTIVE work temporarily; archiving is the permanent kind and
	// already hides the thread everywhere. Inheriting one into the other parked whole
	// archived subtrees invisibly, and un-archiving handed back a thread that was
	// still held with no own hold to clear.
	ah := sb.newHeadlessThread(t, "pi", "arch-root")
	ac := sb.newHeadlessThreadParented(t, "pi", "arch-child", ah.ID)
	ag := sb.newHeadlessThreadParented(t, "pi", "arch-grandchild", ac.ID)
	ahHold := time.Now().Add(72 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", ah.ID, "--until-unix", strconv.FormatInt(ahHold, 10)); err != nil {
		t.Fatalf("hold arch-root: %v\n%s", err, stderr)
	}
	// BASELINE: while nothing is archived the subtree inherits, so the assertions
	// below cannot pass vacuously.
	if !waitUntil(5*time.Second, func() bool {
		c1, ok1 := snapRowOnHold(t, c, ac.ID)
		g1, ok2 := snapRowOnHold(t, c, ag.ID)
		return ok1 && ok2 && c1.OnHold && g1.OnHold
	}) {
		t.Fatalf("baseline: the live subtree should inherit arch-root's hold")
	}
	// Archive the middle thread: it stops being held, AND the hold no longer reaches
	// its child THROUGH it, while the root keeps its own hold.
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", ac.ID); err != nil {
		t.Fatalf("archive arch-child: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool {
		c1, ok1 := snapRowOnHold(t, c, ac.ID)
		g1, ok2 := snapRowOnHold(t, c, ag.ID)
		return ok1 && ok2 && !c1.OnHold && !g1.OnHold
	}) {
		c1, _ := snapRowOnHold(t, c, ac.ID)
		g1, _ := snapRowOnHold(t, c, ag.ID)
		t.Errorf("archiving must detach from the inherited hold and stop it flowing through: archived=%v grandchild=%v (want both false)",
			c1.OnHold, g1.OnHold)
	}
	if r, ok := snapRowOnHold(t, c, ah.ID); !ok || !r.OnHold {
		t.Errorf("archiving a child must not un-park the ancestor")
	}
	// UN-archiving returns it to inheriting — the point is what is parked WHILE
	// archived, not a permanent exemption.
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", ac.ID, "--unarchive"); err != nil {
		t.Fatalf("unarchive: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool {
		c1, ok := snapRowOnHold(t, c, ac.ID)
		return ok && c1.OnHold
	}) {
		t.Errorf("un-archiving should return the thread to its ancestor's hold")
	}
}

// postBothHoldFields sends a deliberately contradictory hold request straight at the
// API — the CLI cannot express it, so this is the only way to prove the daemon (the
// thing every client shares) refuses it.
func postBothHoldFields(c *client.Client, id string) error {
	_, err := c.ThreadHold(context.Background(), id, time.Now().Add(time.Hour).Unix(), time.Now().Add(time.Hour).Unix())
	return err
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

	// A RELEASE routes over the same real hop, and the OWNER derives its effect: a
	// child parked by its parent reads off-hold once released, while the parent stays
	// parked. Asserted on the peer's own snapshot, which is where the derivation lives.
	parent := sb.newHeadlessThread(t, "pi", "rrel-parent")
	child := sb.newHeadlessThreadParented(t, "pi", "rrel-child", parent.ID)
	pHold := time.Now().Add(72 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", parent.ID, "--until-unix", strconv.FormatInt(pHold, 10)); err != nil {
		t.Fatalf("routed hold parent: %v\n%s", err, stderr)
	}
	if !waitUntil(5*time.Second, func() bool { r, ok := snapRowOnHold(t, c, child.ID); return ok && r.OnHold }) {
		t.Fatalf("baseline: the child should be parked by its parent on the peer")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", child.ID, "--release"); err != nil {
		t.Fatalf("routed release: %v\n%s", err, stderr)
	}
	if !waitUntil(5*time.Second, func() bool { r, ok := snapRowOnHold(t, c, child.ID); return ok && !r.OnHold }) {
		t.Errorf("a routed release did not free the child on the owning daemon")
	}
	if r, ok := snapRowOnHold(t, c, parent.ID); !ok || !r.OnHold {
		t.Errorf("a routed release must not un-park the parent")
	}
	// A bare --release defaults to the start of tomorrow, so it lapses with the
	// parking round it escapes rather than exempting the thread forever.
	if got := threadByName(t, sb, "rrel-child").HoldReleaseUntilUnix; got <= time.Now().Unix() || got > time.Now().Add(48*time.Hour).Unix() {
		t.Errorf("a bare --release should default to a deadline within the next day, got %d", got)
	}
	// A routed --clear on a thread its parent parks fails loudly through the hop too.
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", child.ID, "--clear"); err == nil {
		t.Errorf("a routed --clear on an inherited hold must fail loudly")
	} else if !strings.Contains(stderr, "STILL on hold") {
		t.Errorf("the routed refusal should say it is still held, got: %s", stderr)
	}
}
