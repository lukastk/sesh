package daemon

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestEffectiveHolds(t *testing.T) {
	// root(100) -> mid(0) -> leaf(50); sib(0) under root; orphanXmachine parent absent.
	threads := []api.Thread{
		{ID: "root", OnHoldUntilUnix: 100},
		{ID: "mid", Parent: "root", OnHoldUntilUnix: 0},
		{ID: "leaf", Parent: "mid", OnHoldUntilUnix: 50},
		{ID: "sib", Parent: "root", OnHoldUntilUnix: 0},
		{ID: "lone", OnHoldUntilUnix: 0},
		{ID: "xmachine", Parent: "not-in-this-store", OnHoldUntilUnix: 7},
	}
	eff := effectiveHolds(threads, 0)
	want := map[string]int64{
		"root":     100, // own
		"mid":      100, // inherits root
		"leaf":     100, // max(own 50, inherited 100)
		"sib":      100, // inherits root
		"lone":     0,   // no hold anywhere
		"xmachine": 7,   // cross-machine parent absent → only own (no inheritance across the boundary)
	}
	for id, w := range want {
		if eff[id] != w {
			t.Errorf("effectiveHolds[%s] = %d, want %d", id, eff[id], w)
		}
	}
}

// A child's own hold LATER than its parent's wins (max), and a cyclic parent graph
// (which reparent guards against, but the walk must survive) does not loop forever.
func TestEffectiveHoldsMaxAndCycle(t *testing.T) {
	threads := []api.Thread{
		{ID: "p", OnHoldUntilUnix: 100},
		{ID: "c", Parent: "p", OnHoldUntilUnix: 500}, // own later than parent
		{ID: "a", Parent: "b", OnHoldUntilUnix: 10},  // a<->b cycle
		{ID: "b", Parent: "a", OnHoldUntilUnix: 20},
	}
	eff := effectiveHolds(threads, 0)
	if eff["c"] != 500 {
		t.Errorf("child own-hold should win: got %d, want 500", eff["c"])
	}
	if eff["a"] != 20 || eff["b"] != 20 {
		t.Errorf("cycle should resolve to the max in the cycle (20,20), got a=%d b=%d", eff["a"], eff["b"])
	}
}

// A RELEASE (schema 48) is the escape hatch from the max: a released thread ignores
// every ancestor's hold, and the inheritance walk STOPS at it, so its own subtree is
// freed with it. This is the whole point of the feature — before it, a child could
// never be active while its parent was parked. The release is DATED, so a lapsed one
// changes nothing and the ancestor's hold applies again.
func TestEffectiveHoldsRelease(t *testing.T) {
	const now = 1000
	// root(held 5000) -> mid -> leaf, and root -> sib.
	base := func() []api.Thread {
		return []api.Thread{
			{ID: "root", OnHoldUntilUnix: 5000},
			{ID: "mid", Parent: "root"},
			{ID: "leaf", Parent: "mid"},
			{ID: "sib", Parent: "root"},
		}
	}
	// Baseline: everything under root inherits its hold. Without this the release
	// cases below could pass vacuously (nothing held in the first place).
	if eff := effectiveHolds(base(), now); eff["mid"] != 5000 || eff["leaf"] != 5000 || eff["sib"] != 5000 {
		t.Fatalf("baseline: subtree should inherit root's hold, got mid=%d leaf=%d sib=%d", eff["mid"], eff["leaf"], eff["sib"])
	}

	// Release mid → mid AND its descendant leaf are free; the untouched sib is not,
	// and root keeps its own hold (a release cuts only what flows from ABOVE).
	th := base()
	th[1].HoldReleaseUntilUnix = 2000 // mid, still in the future at now=1000
	eff := effectiveHolds(th, now)
	if eff["mid"] != 0 {
		t.Errorf("released thread should be free: mid=%d, want 0", eff["mid"])
	}
	if eff["leaf"] != 0 {
		t.Errorf("a released thread frees its SUBTREE: leaf=%d, want 0", eff["leaf"])
	}
	if eff["sib"] != 5000 {
		t.Errorf("an unrelated sibling must stay held: sib=%d, want 5000", eff["sib"])
	}
	if eff["root"] != 5000 {
		t.Errorf("releasing a child must not touch the ancestor: root=%d, want 5000", eff["root"])
	}

	// A LAPSED release (deadline in the past) is inert — the ancestor's hold applies
	// again. This is the auto-expiry that stops a release exempting a thread forever.
	th = base()
	th[1].HoldReleaseUntilUnix = 999 // already passed at now=1000
	if eff := effectiveHolds(th, now); eff["mid"] != 5000 || eff["leaf"] != 5000 {
		t.Errorf("a lapsed release should not free anything: mid=%d leaf=%d, want 5000/5000", eff["mid"], eff["leaf"])
	}

	// A released thread's OWN hold still parks it and its subtree: release detaches
	// from ancestors, it does not mean "not held".
	th = base()
	th[1].HoldReleaseUntilUnix = 2000
	th[1].OnHoldUntilUnix = 3000
	if eff := effectiveHolds(th, now); eff["mid"] != 3000 || eff["leaf"] != 3000 {
		t.Errorf("a released thread's own hold still applies: mid=%d leaf=%d, want 3000/3000", eff["mid"], eff["leaf"])
	}
}

// nextHoldFlip must schedule a sweep for a RELEASE deadline as well as a hold one:
// both flip OnHold with NO record write, and missing the release direction would
// leave a thread reading un-held forever after its release lapsed, with no write
// ever to correct it.
func TestNextHoldFlipIncludesReleaseExpiry(t *testing.T) {
	const now = 1000
	threads := []api.Thread{
		{ID: "root", OnHoldUntilUnix: 5000},
		{ID: "mid", Parent: "root", HoldReleaseUntilUnix: 2000},
	}
	eff := effectiveHolds(threads, now)
	if got := nextHoldFlip(eff, threads, now); got != 2000 {
		t.Errorf("next flip should be the RELEASE expiry at 2000, got %d", got)
	}
	// Past deadlines never schedule anything.
	if got := nextHoldFlip(map[string]int64{"a": 10}, []api.Thread{{ID: "a", HoldReleaseUntilUnix: 20}}, now); got != 0 {
		t.Errorf("lapsed deadlines should schedule nothing, got %d", got)
	}
}

// holdDominator names the ANCESTOR actually keeping a thread parked — what the hold
// endpoint reports so a caller can be told WHY its clear did not un-hold anything.
func TestHoldDominator(t *testing.T) {
	const now = 1000
	threads := []api.Thread{
		{ID: "root", OnHoldUntilUnix: 5000, Name: "root"},
		{ID: "mid", Parent: "root"},
		{ID: "leaf", Parent: "mid"},
		{ID: "own", OnHoldUntilUnix: 4000},
		{ID: "free"},
		{ID: "rel", Parent: "root", HoldReleaseUntilUnix: 2000},
	}
	if got := holdDominator(threads, "leaf", now); got != "root" {
		t.Errorf("leaf is parked by root, got %q", got)
	}
	if got := holdDominator(threads, "own", now); got != "" {
		t.Errorf("a thread held by its OWN deadline has no dominating ancestor, got %q", got)
	}
	if got := holdDominator(threads, "free", now); got != "" {
		t.Errorf("an unheld thread has no dominator, got %q", got)
	}
	if got := holdDominator(threads, "rel", now); got != "" {
		t.Errorf("a released thread is not dominated, got %q", got)
	}
	if got := holdDominator(threads, "missing", now); got != "" {
		t.Errorf("an unknown id must not invent a dominator, got %q", got)
	}
	// Archived cuts inheritance, so nothing above an archived thread parks it — and the
	// dominator must agree with effectiveHolds, or a refusal would name an ancestor that
	// is not in fact the reason.
	arch := append(threads,
		api.Thread{ID: "arch", Parent: "root", Archived: true},
		api.Thread{ID: "under-arch", Parent: "arch"})
	if got := holdDominator(arch, "arch", now); got != "" {
		t.Errorf("an archived thread is not dominated by its ancestor, got %q", got)
	}
	if got := holdDominator(arch, "under-arch", now); got != "" {
		t.Errorf("a hold must not reach through an archived node, got %q", got)
	}
}

// An ARCHIVED thread is detached from its ancestors' holds: a parking round must not
// mark archived subtrees "on hold" (they are already out of every active view), and
// un-archiving one must not hand back a thread that is silently parked with no own
// hold to clear. Its OWN hold still applies and still reaches its descendants —
// only what flows from ABOVE an archived node stops there.
func TestEffectiveHoldsArchived(t *testing.T) {
	const now = 1000
	// root(held 5000) -> arch(ARCHIVED) -> leaf(live)
	base := func() []api.Thread {
		return []api.Thread{
			{ID: "root", OnHoldUntilUnix: 5000},
			{ID: "arch", Parent: "root", Archived: true},
			{ID: "leaf", Parent: "arch"},
			{ID: "live", Parent: "root"},
		}
	}
	// Baseline: without the archived flag the whole subtree inherits, so the
	// assertions below cannot pass vacuously.
	plain := base()
	plain[1].Archived = false
	if eff := effectiveHolds(plain, now); eff["arch"] != 5000 || eff["leaf"] != 5000 {
		t.Fatalf("baseline: a non-archived subtree must inherit, got arch=%d leaf=%d", eff["arch"], eff["leaf"])
	}

	eff := effectiveHolds(base(), now)
	if eff["arch"] != 0 {
		t.Errorf("an archived thread must not inherit its ancestor's hold: got %d, want 0", eff["arch"])
	}
	if eff["leaf"] != 0 {
		t.Errorf("the hold must not flow THROUGH an archived node: leaf=%d, want 0", eff["leaf"])
	}
	if eff["live"] != 5000 {
		t.Errorf("a live sibling must still inherit: got %d, want 5000", eff["live"])
	}
	if eff["root"] != 5000 {
		t.Errorf("the ancestor keeps its own hold: got %d, want 5000", eff["root"])
	}

	// An archived thread's OWN hold is explicit and still applies — to it, and to its
	// descendants. Only inheritance from above it is cut.
	th := base()
	th[1].OnHoldUntilUnix = 3000
	if eff := effectiveHolds(th, now); eff["arch"] != 3000 || eff["leaf"] != 3000 {
		t.Errorf("an archived thread's own hold must still apply and transmit: arch=%d leaf=%d, want 3000/3000",
			eff["arch"], eff["leaf"])
	}

	// The un-archive path is the user-visible point: clearing the flag returns the
	// thread to inheriting, so this is about what is parked WHILE archived.
	th = base()
	th[1].Archived = false
	if eff := effectiveHolds(th, now); eff["arch"] != 5000 {
		t.Errorf("un-archiving returns the thread to its ancestors' hold: got %d, want 5000", eff["arch"])
	}
}
