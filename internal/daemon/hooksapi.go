package daemon

// The hooks management API: list (definitions + mute state), mute/unmute
// (persisted), and synchronous test — `sesh hooks list/enable/disable/test`.

import (
	"net/http"
	"time"

	"github.com/lukastk/sesh/internal/api"
)

func (d *Daemon) handleHooksList(w http.ResponseWriter, r *http.Request) {
	out := api.HooksListResponse{Schema: api.SchemaVersion, Hooks: []api.HookInfo{}}
	for _, h := range d.hooks.hooks {
		out.Hooks = append(out.Hooks, api.HookInfo{
			Name: h.Name, Event: h.Event, From: h.From, To: h.To,
			Agent: h.Agent, Machine: h.Machine, Tag: h.Tag,
			Command: h.Command, Muted: d.hooks.isMuted(h.Name),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Daemon) handleHooksMute(w http.ResponseWriter, r *http.Request) {
	var req api.HookMuteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	known := false
	for _, h := range d.hooks.hooks {
		if h.Name == req.Name {
			known = true
		}
	}
	if !known {
		writeError(w, http.StatusNotFound, "hooks: no hook named "+req.Name+" in config.toml")
		return
	}
	if err := d.store.SetHookMuted(req.Name, req.Muted); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d.hooks.setMuted(req.Name, req.Muted)
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "name": req.Name, "muted": req.Muted})
}

func (d *Daemon) handleHooksTest(w http.ResponseWriter, r *http.Request) {
	var req api.HookTestRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The test event: the named thread's CURRENT snapshot (or a synthetic one),
	// with the hook's own event type so it matches by construction. The age
	// fields default to UNKNOWN (-1, omitted from the env) — a zero-value Event
	// would read as "input 0s ago" and wrongly trip an activity-gating hook;
	// the activity age is then computed from the real snapshot below.
	ev := Event{Type: "test", From: "busy", To: "idle", AttachedActivityAgo: -1, AttachmentChangedAgo: -1}
	for _, h := range d.hooks.hooks {
		if h.Name == req.Name {
			ev.Type = h.Event
			if h.From != "" {
				ev.From = h.From
			}
			if h.To != "" {
				ev.To = h.To
			}
		}
	}
	if req.ThreadID != "" {
		if sn, ok := d.maint.stateOf(req.ThreadID); ok {
			ev.Snap = sn
			if sn.AttachedActivityUnix > 0 {
				ev.AttachedActivityAgo = max(time.Now().Unix()-sn.AttachedActivityUnix, 0)
			}
		} else if th, err := d.store.GetThread(req.ThreadID); err == nil {
			ev.Snap.Thread = th
		} else {
			writeError(w, http.StatusNotFound, "hooks test: thread "+req.ThreadID+" not found")
			return
		}
	} else {
		ev.Snap.Thread = api.Thread{ID: "test-thread-id", Name: "hook-test", Machine: d.cfg.Machine, AgentKind: "pi", Cwd: "/tmp"}
	}
	out, err, ok := d.hooks.test(req.Name, ev)
	if !ok {
		writeError(w, http.StatusNotFound, "hooks: no hook named "+req.Name+" in config.toml")
		return
	}
	resp := api.HookTestResponse{Schema: api.SchemaVersion, Name: req.Name, Output: out, OK: err == nil}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
