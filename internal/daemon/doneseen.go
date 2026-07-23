package daemon

// The done/seen marker (schema 43, issue #6 — the herdr-inspired "finished
// while you weren't looking" state). A HEADFUL turn that ends while the
// session is not attended sets done; it stays set — across stop/headless —
// until the user SEES the thread: fresh input on an attached client, or an
// attachment flip onto the session (tmux switch-client does NOT bump
// client_activity, so the flip itself is the "just navigated here" signal —
// the H48 measurement). Runtime state: in-memory, restart clears it.

import "github.com/lukastk/sesh/internal/api"

// doneAttendedWindow is how fresh attached-client INPUT must be (seconds) for
// a finishing turn to count as watched — the same window the notify policy
// uses for "the user is driving this session". A parked cockpit client with
// stale input does NOT count (the H48 lesson: attachment alone over-counts).
const doneAttendedWindow = 60

// nextDoneSince computes the marker's transition for one maintainer tick.
// Pure — the truth table lives in doneseen_test.go; refreshThread supplies
// this tick's axes and last tick's published busy/attachment.
func nextDoneSince(doneSince int64, prevBusy, busy api.Busy, prevAttachment, attachment api.Attachment, attachedActivityUnix, nowUnix int64) int64 {
	// SET: a turn just finished (busy→idle edge) while not attended.
	attended := attachment == api.Attached && attachedActivityUnix > 0 &&
		nowUnix-attachedActivityUnix <= doneAttendedWindow
	if prevBusy == api.BusyBusy && busy == api.BusyIdle && !attended {
		doneSince = nowUnix
	}
	if doneSince == 0 {
		return 0
	}
	// CLEAR (seen): input since it finished, or the user navigated onto it.
	// A flip in the same tick as the edge clears immediately — the user
	// arrived exactly as it finished, so they are looking at the result.
	if attachedActivityUnix > 0 && attachedActivityUnix >= doneSince {
		return 0
	}
	if prevAttachment == api.Detached && attachment == api.Attached {
		return 0
	}
	return doneSince
}
