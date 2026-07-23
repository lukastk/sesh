package daemon

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestAutoFlagTrigger pins the auto-flag truth table (autoflag.go).
func TestAutoFlagTrigger(t *testing.T) {
	const now = int64(10_000)
	att, det := api.Attached, api.Detached
	rep, heu := api.AuthorityReported, api.AuthorityHeuristic

	type in struct {
		name                 string
		prevBusy, busy       api.Busy
		authority            api.StateAuthority
		heuristicOK          bool
		stalled              bool
		stallReason          string
		stallLatched         bool
		attachment           api.Attachment
		activity             int64
		wantFlag             bool
		wantReason           string
	}
	cases := []in{
		// Reported turn end while detached → flag.
		{name: "reported edge detached", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: rep,
			attachment: det, wantFlag: true, wantReason: "turn ended"},
		// Parked attached client (stale input) is NOT attended → flag.
		{name: "reported edge parked client", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: rep,
			attachment: att, activity: now - flagAttendedWindow - 5, wantFlag: true, wantReason: "turn ended"},
		// Actively attended at the edge → no flag (you watched it finish).
		{name: "attended edge", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: rep,
			attachment: att, activity: now - 5, wantFlag: false},
		// HEURISTIC edge: only when the agent is opted in ([flags]).
		{name: "heuristic edge default off", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: heu,
			attachment: det, wantFlag: false},
		{name: "heuristic edge opted in", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: heu,
			heuristicOK: true, attachment: det, wantFlag: true, wantReason: "turn ended"},
		// No edge → no flag.
		{name: "still busy", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep, attachment: det},
		{name: "still idle", prevBusy: api.BusyIdle, busy: api.BusyIdle, authority: rep, attachment: det},
		{name: "first tick", prevBusy: "", busy: api.BusyIdle, authority: rep, attachment: det},
		// A stall (question/approval prompt) flags immediately with its reason,
		// mid-turn — waiting for the edge would never come.
		{name: "stall detached", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep,
			stalled: true, stallReason: "Do you prefer red or blue?",
			attachment: det, wantFlag: true, wantReason: "Do you prefer red or blue?"},
		{name: "stall no reason gets default", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep,
			stalled: true, attachment: det, wantFlag: true, wantReason: "agent is waiting on you"},
		// One flag per stall episode: the latch stops per-tick re-flagging
		// (so a manual unflag is not fought while the same prompt sits open).
		{name: "stall latched", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep,
			stalled: true, stallLatched: true, attachment: det, wantFlag: false},
		// An attended stall does not flag (the prompt is on your screen).
		{name: "stall attended", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep,
			stalled: true, attachment: att, activity: now - 3, wantFlag: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, flag := autoFlagTrigger(tc.prevBusy, tc.busy, tc.authority, tc.heuristicOK,
				tc.stalled, tc.stallReason, tc.stallLatched, tc.attachment, tc.activity, now)
			if flag != tc.wantFlag || (flag && reason != tc.wantReason) {
				t.Fatalf("autoFlagTrigger = (%q, %v), want (%q, %v)", reason, flag, tc.wantReason, tc.wantFlag)
			}
		})
	}
}
