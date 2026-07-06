package daemon

// MANUAL THREAD ORDERING + DIVIDERS.
//
// A top-level (parentless) thread can be PINNED: it gets a fractional `pin_order`
// key and renders ABOVE the auto-sorted block, ordered by that key. A DIVIDER is a
// thread record with agent_kind "divider" — a purely visual horizontal rule (no
// agent/pane/transcript/children) that always lives in the pinned block. Both reuse
// the ordinary record machinery (tree, tags, archive, mesh sync, delete); agent
// verbs on a divider refuse via nonAgentGate, exactly like a virtual thread.
//
// The daemon is a PURE SETTER of pin_order: the caller supplies the absolute float,
// which it computes client-side from the merged cross-machine view (mirroring how
// `hold` puts date math in the client). This keeps every pin/reorder a SINGLE write
// to one owner — it never renumbers siblings, which may live on offline machines.

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
)

func (d *Daemon) routesPin(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/pin", d.handleThreadPin)
}

// handleThreadPin sets (PinOrder non-nil) or clears (nil) a thread's manual-ordering
// key. Refusals: pinning a thread that has a parent (only top-level threads can be
// pinned), and un-pinning a divider (a divider lives only in the pinned block — delete
// it to remove it).
func (d *Daemon) handleThreadPin(w http.ResponseWriter, r *http.Request) {
	var req api.PinThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "pin: id is required")
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	if req.PinOrder != nil {
		if thread.Parent != "" {
			writeError(w, http.StatusConflict,
				"pin: only top-level threads can be pinned — reparent "+req.ID+" to root first (sesh thread reparent --id "+req.ID+")")
			return
		}
	} else if thread.AgentKind == api.DividerAgentKind {
		writeError(w, http.StatusConflict,
			"pin: a divider can't be un-pinned — delete it instead (sesh thread delete --id "+req.ID+")")
		return
	}
	if err := d.store.SetThreadPin(req.ID, req.PinOrder); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

// newDividerThread records a DIVIDER (see the package comment). Reached from
// handleThreadNew BEFORE agent parsing: it has no agent, so every agent-shaped
// request field is a loud refusal; a cwd is meaningless and ignored. A divider is
// created straight into the manual order, so PinOrder is REQUIRED, and it is a
// top-level node, so a parent is refused.
func (d *Daemon) newDividerThread(w http.ResponseWriter, req api.NewThreadRequest) {
	switch {
	case req.Agent != "":
		writeError(w, http.StatusBadRequest, "thread: --divider takes no --agent (a divider has none)")
		return
	case req.Headless:
		writeError(w, http.StatusBadRequest, "thread: --divider and --headless are mutually exclusive (nothing runs)")
		return
	case req.ForkFrom != "":
		writeError(w, http.StatusBadRequest, "thread: --divider cannot fork (no conversation to branch)")
		return
	case req.IntoSession != "" || req.IntoWindow != "" || req.IntoPane != "":
		writeError(w, http.StatusBadRequest, "thread: --divider takes no placement (nothing is spawned)")
		return
	case req.Msg != "":
		writeError(w, http.StatusBadRequest, "thread: --divider takes no --msg (no agent to receive it)")
		return
	case req.Mode != "":
		writeError(w, http.StatusBadRequest, "thread: --divider takes no spawn mode (nothing is spawned)")
		return
	case req.Model != "":
		writeError(w, http.StatusBadRequest, "thread: --divider takes no --model (nothing runs)")
		return
	case req.Parent != "":
		writeError(w, http.StatusBadRequest, "thread: a divider is a top-level node — it takes no --parent")
		return
	case req.PinOrder == nil:
		writeError(w, http.StatusBadRequest, "thread: --divider needs a position in the manual order (internal: pin_order unset)")
		return
	}
	id := uuid.NewString()
	thread := api.Thread{
		ID:            id,
		Machine:       d.cfg.Machine,
		SessionName:   "divider-" + id, // logical name; no tmux session ever exists
		AgentKind:     api.DividerAgentKind,
		Name:          req.Name, // optional label
		Tags:          []string{},
		CreatedAtUnix: time.Now().Unix(),
		Notify:        d.defaults.NotifyDefault(),
		PinOrder:      req.PinOrder,
	}
	if err := d.store.InsertThread(thread); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}
