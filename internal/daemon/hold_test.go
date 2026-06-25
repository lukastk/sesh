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
	eff := effectiveHolds(threads)
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
	eff := effectiveHolds(threads)
	if eff["c"] != 500 {
		t.Errorf("child own-hold should win: got %d, want 500", eff["c"])
	}
	if eff["a"] != 20 || eff["b"] != 20 {
		t.Errorf("cycle should resolve to the max in the cycle (20,20), got a=%d b=%d", eff["a"], eff["b"])
	}
}
