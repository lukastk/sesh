package daemon

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestAutoFlagTrigger pins the auto-flag truth table (autoflag.go). There is
// deliberately NO attended dimension (the gate was removed 2026-07-25 — Lukas:
// "Even attended threads should trigger notifications and flagging"): a turn
// end or a stall flags regardless of who is watching.
func TestAutoFlagTrigger(t *testing.T) {
	rep, heu := api.AuthorityReported, api.AuthorityHeuristic

	type in struct {
		name           string
		prevBusy, busy api.Busy
		authority      api.StateAuthority
		heuristicOK    bool
		stalled        bool
		stallReason    string
		stallLatched   bool
		wantFlag       bool
		wantReason     string
	}
	cases := []in{
		// Reported turn end → flag, regardless of attachment (no gate).
		{name: "reported edge", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: rep,
			wantFlag: true, wantReason: "turn ended"},
		// HEURISTIC edge: only when the agent is opted in ([flags]).
		{name: "heuristic edge default off", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: heu,
			wantFlag: false},
		{name: "heuristic edge opted in", prevBusy: api.BusyBusy, busy: api.BusyIdle, authority: heu,
			heuristicOK: true, wantFlag: true, wantReason: "turn ended"},
		// No edge → no flag.
		{name: "still busy", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep},
		{name: "still idle", prevBusy: api.BusyIdle, busy: api.BusyIdle, authority: rep},
		{name: "first tick", prevBusy: "", busy: api.BusyIdle, authority: rep},
		// A stall (question/approval prompt) flags immediately with its reason,
		// mid-turn — waiting for the edge would never come.
		{name: "stall", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep,
			stalled: true, stallReason: "Do you prefer red or blue?",
			wantFlag: true, wantReason: "Do you prefer red or blue?"},
		{name: "stall no reason gets default", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep,
			stalled: true, wantFlag: true, wantReason: "agent is waiting on you"},
		// One flag per stall episode: the latch stops per-tick re-flagging
		// (so a manual unflag is not fought while the same prompt sits open).
		{name: "stall latched", prevBusy: api.BusyBusy, busy: api.BusyBusy, authority: rep,
			stalled: true, stallLatched: true, wantFlag: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, flag := autoFlagTrigger(tc.prevBusy, tc.busy, tc.authority, tc.heuristicOK,
				tc.stalled, tc.stallReason, tc.stallLatched)
			if flag != tc.wantFlag || (flag && reason != tc.wantReason) {
				t.Fatalf("autoFlagTrigger = (%q, %v), want (%q, %v)", reason, flag, tc.wantReason, tc.wantFlag)
			}
		})
	}
}
