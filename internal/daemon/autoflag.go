package daemon

// Auto-flagging (api schema 44, ticket df4fb07a): the owning daemon FLAGS a
// thread — a stored, manually-cleared "needs the user's attention" marker —
// when the agent stops running for the user while nobody is watching:
//
//   - a HEADFUL turn ends (busy→idle edge) while the session is unattended.
//     Reported edges (in-agent harness hooks: claude Stop, pi agent_settled,
//     codex notify) always trigger; HEURISTIC edges only for agents opted in
//     via [flags] heuristic_agents (default: none — the content-diff can
//     mistake a user's own settle for a turn end, the H47 class).
//   - the agent is STALLED on the human (a question/approval prompt, per the
//     reporter's blocked state) while unattended — checked per tick, so
//     navigating away from an unanswered prompt still flags it. One flag per
//     stall episode (the liveState latch), so a manual unflag is not fought
//     while the same prompt sits open... but a NEW stall re-flags.
//
// Headless turn completion deliberately does NOT flag: delegate/await and
// subscriptions are the delivery path for headless work. Flags never
// auto-clear (Lukas 2026-07-23): the TUI key / `thread flag --off` clear them.

import "github.com/lukastk/sesh/internal/api"

// flagAttendedWindow: how fresh attached-client INPUT must be (seconds) for
// the session to count as watched at trigger time — a parked cockpit client
// with stale input does not count (the H48 lesson).
const flagAttendedWindow = 60

// attendedNow: is the user actively watching this session right now?
func attendedNow(attachment api.Attachment, attachedActivityUnix, nowUnix int64) bool {
	return attachment == api.Attached && attachedActivityUnix > 0 &&
		nowUnix-attachedActivityUnix <= flagAttendedWindow
}

// autoFlagTrigger decides whether this tick's state warrants an auto-flag and
// with what reason. Pure — the truth table lives in autoflag_test.go.
// prevBusy is LAST tick's published busy; stallLatched says this stall episode
// already flagged once (the caller resets it when the stall clears).
func autoFlagTrigger(
	prevBusy, busy api.Busy,
	authority api.StateAuthority, heuristicAllowed bool,
	stalled bool, stallReason string, stallLatched bool,
	attachment api.Attachment, attachedActivityUnix, nowUnix int64,
) (reason string, flag bool) {
	if attendedNow(attachment, attachedActivityUnix, nowUnix) {
		return "", false
	}
	// A stalled agent (question/approval prompt) flags immediately — waiting
	// for the turn edge would never come (the turn is suspended on the user).
	if stalled && !stallLatched {
		if stallReason == "" {
			stallReason = "agent is waiting on you"
		}
		return stallReason, true
	}
	// A turn just ended. Reported edges are exact; heuristic edges only for
	// agents explicitly opted in ([flags] heuristic_agents).
	if prevBusy == api.BusyBusy && busy == api.BusyIdle {
		if authority == api.AuthorityReported || heuristicAllowed {
			return "turn ended", true
		}
	}
	return "", false
}
