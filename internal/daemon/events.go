package daemon

import (
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
	}
	if e.From != "" || e.To != "" {
		m["SESH_EVENT_FROM"] = e.From
		m["SESH_EVENT_TO"] = e.To
	}
	return m
}
