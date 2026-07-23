package daemon

import (
	"strconv"
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// Event is one observed thread change (see eventer.go for the observer loop
// and config.Hook for what subscribes). Snap is the post-change snapshot;
// From/To are set for busy_changed and head_changed.
type Event struct {
	Type string
	Snap api.ThreadSnapshot
	From string
	To   string
	// AttachedActivityAgo is seconds since the newest INPUT on a client attached
	// to the thread's session, computed at event time from the owner-stamped
	// snapshot field. -1 = unknown (detached, or a pre-42 owner).
	AttachedActivityAgo int64
	// AttachmentChangedAgo is seconds since THIS observer saw the thread's
	// attachment axis flip (either direction) — the "user just navigated onto
	// it" signal. -1 = no flip observed since daemon start.
	AttachmentChangedAgo int64
}

// Env is the environment a hook command receives.
func (e Event) Env() map[string]string {
	m := map[string]string{
		"SESH_EVENT":       e.Type,
		"SESH_THREAD_ID":   e.Snap.ID,
		"SESH_THREAD_NAME": e.Snap.Name,
		"SESH_AGENT":       e.Snap.AgentKind,
		"SESH_MACHINE":     e.Snap.Machine,
		"SESH_CWD":         e.Snap.Cwd,
		"SESH_SESSION":     e.Snap.SessionName,
		"SESH_TAGS":        strings.Join(e.Snap.Tags, ","),
		"SESH_HEAD":        string(e.Snap.Head),
		"SESH_BUSY":        string(e.Snap.Busy),
		// Attachment ("attached"/"detached") lets a hook tell user-driven busy
		// flips from background ones: typing into a pane or the redraw of
		// navigating onto it latches the content-diff busy probe exactly like
		// agent output, so a busy→idle edge alone can't distinguish "a turn
		// finished" from "the user stopped interacting". A notify hook can skip
		// attached threads (the user is looking at that session already).
		"SESH_ATTACHMENT": string(e.Snap.Attachment),
		// The per-thread gate: the user's notify hook respects it (the daemon
		// never decides what a notification is — policy stays in the hook).
		"SESH_NOTIFY": map[bool]string{true: "1", false: "0"}[e.Snap.Notify],
		// The flag (needs the user's attention; manual-clear) — schema 44.
		"SESH_FLAGGED": map[bool]string{true: "1", false: "0"}[e.Snap.Flagged],
	}
	if e.From != "" || e.To != "" {
		m["SESH_EVENT_FROM"] = e.From
		m["SESH_EVENT_TO"] = e.To
	}
	// The "is the user actually driving this session?" ages (integer seconds).
	// UNSET means unknown — a hook must fail open (notify), never read it as 0.
	if e.AttachedActivityAgo >= 0 {
		m["SESH_ATTACHED_ACTIVITY_AGO"] = strconv.FormatInt(e.AttachedActivityAgo, 10)
	}
	if e.AttachmentChangedAgo >= 0 {
		m["SESH_ATTACHMENT_CHANGED_AGO"] = strconv.FormatInt(e.AttachmentChangedAgo, 10)
	}
	// Present only when there is one (an auto-flag's trigger, e.g. the
	// question the agent asked) — hooks test presence.
	if e.Snap.FlagReason != "" {
		m["SESH_FLAG_REASON"] = e.Snap.FlagReason
	}
	// Which mechanism decided busy (reported|heuristic); ABSENT when unknown
	// (headless, a pre-43 owner) — a hook must not read absence as either value.
	if e.Snap.StateAuthority != "" {
		m["SESH_STATE_AUTHORITY"] = string(e.Snap.StateAuthority)
	}
	return m
}
