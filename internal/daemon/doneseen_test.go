package daemon

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestNextDoneSince pins the done/seen truth table (doneseen.go).
func TestNextDoneSince(t *testing.T) {
	const now = int64(10_000)
	att, det := api.Attached, api.Detached
	busy, idle := api.BusyBusy, api.BusyIdle

	cases := []struct {
		name              string
		doneSince         int64
		prevBusy, curBusy api.Busy
		prevAtt, curAtt   api.Attachment
		attachedActivity  int64
		want              int64
	}{
		// SET: turn finishes detached → done.
		{"edge while detached sets", 0, busy, idle, det, det, 0, now},
		// SET: turn finishes with a PARKED attached client (stale input) →
		// done — attachment alone is not watching (the H48 lesson).
		{"edge with parked client sets", 0, busy, idle, att, att, now - doneAttendedWindow - 10, now},
		// NO SET: turn finishes while the user is driving (fresh input).
		{"edge while attended does not set", 0, busy, idle, att, att, now - 5, 0},
		// NO SET: no edge (still busy / already idle).
		{"no edge busy", 0, busy, busy, det, det, 0, 0},
		{"no edge idle", 0, idle, idle, det, det, 0, 0},
		// NO SET: first tick (unknown prev busy).
		{"first tick no set", 0, "", idle, det, det, 0, 0},
		// RETAIN: done holds while unseen.
		{"retains unseen", 5000, idle, idle, det, det, 0, 5000},
		{"retains with stale parked input", 5000, idle, idle, att, att, 4000, 5000},
		// CLEAR: fresh input AFTER it finished = seen.
		{"input after finish clears", 5000, idle, idle, att, att, 6000, 0},
		// CLEAR: navigating onto it (flip detached→attached) = seen, even
		// though switch-client bumps no client_activity.
		{"attachment flip clears", 5000, idle, idle, det, att, 0, 0},
		// CLEAR in the same tick as the edge: user arrived exactly as it
		// finished — they are looking at the result.
		{"flip in edge tick clears immediately", 0, busy, idle, det, att, 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextDoneSince(tc.doneSince, tc.prevBusy, tc.curBusy, tc.prevAtt, tc.curAtt, tc.attachedActivity, now)
			if got != tc.want {
				t.Fatalf("nextDoneSince = %d, want %d", got, tc.want)
			}
		})
	}
}
