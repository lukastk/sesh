package daemon

// The browser's TUI-equivalent surfaces (capabilities browser.mesh-v1 and
// browser.thread-ops-v1): the same merged mesh snapshot `sesh tui` polls, and the
// same canonical thread mutations its commands drive, for a browser client that
// holds ONE endpoint and ONE credential. Nothing here re-implements a thread rule:
// the mesh is d.handleMesh's data, and every op is forwarded unchanged to the
// owning machine's canonical route, which validates it.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// browserThreadOps maps each op a browser may run on a thread to the canonical
// route it forwards to. Every canonical body names the thread in `id`, which is how
// the hub finds the owner. Ops are the TUI's commands; send/create stay on their
// own routes (they carry idempotency and owner choice).
var browserThreadOps = map[string]string{
	"flag":     "/v1/threads/flag",
	"hold":     "/v1/threads/hold",
	"pin":      "/v1/threads/pin",
	"notify":   "/v1/threads/notify",
	"tag":      "/v1/threads/tag",
	"rename":   "/v1/threads/rename",
	"reparent": "/v1/threads/reparent",
	"archive":  "/v1/threads/archive",
	"delete":   "/v1/threads/delete",
	"stop":     "/v1/threads/stop",
	"resume":   "/v1/threads/resume",
	"realize":  "/v1/threads/realize",
	"model":    "/v1/threads/model",
	// schema 73: the TUI's context-edit and env-edit.
	"context": "/v1/threads/context",
	"env":     "/v1/threads/env",
}

// browserThreadReads maps each per-thread read a browser may make to its canonical
// GET (?id=), served by the thread's owner (schema 73).
var browserThreadReads = map[string]string{
	"/v1/browser/threads/context": "/v1/threads/context",
	"/v1/browser/threads/env":     "/v1/threads/env",
}

func (d *Daemon) routesBrowserMesh(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/browser/mesh", d.handleBrowserMesh)
	mux.HandleFunc("POST /v1/browser/threads/op", d.handleBrowserThreadOp)
	mux.HandleFunc("GET /v1/browser/tui-config", d.handleBrowserTUIConfig)
	for browserPath, canonical := range browserThreadReads {
		mux.HandleFunc("GET "+browserPath, d.handleBrowserThreadRead(canonical))
	}
}

// handleBrowserMesh serves the merged mesh snapshot. By default it leaves out the
// archived threads `sesh tui`'s default view would not show either (it keeps an
// archived thread that is flagged, has a live pane or is running), because the
// archive is most of the snapshot (3,500 of 3,700 threads on mymain in 2026-11)
// and a browser polls this every few seconds. `?archived=1` returns everything,
// for the archived and all views. Each machine says whether the hub routes ops to
// it (`routable`), so a client offers actions only where they can land.
func (d *Daemon) handleBrowserMesh(w http.ResponseWriter, r *http.Request) {
	if !onlyQuery(w, r, "archived") {
		return
	}
	archived := r.URL.Query().Get("archived")
	if archived != "" && archived != "1" {
		writeError(w, http.StatusBadRequest, "browser mesh: archived must be 1 or absent")
		return
	}
	d.noteMeshDemand()
	self := d.maint.snapshot()
	d.overlayTurns(self.Threads)
	views := append([]api.MachineView{{
		Machine: d.cfg.Machine, Self: true, Reachable: true, SyncedAtUnix: self.GeneratedAtUnix, Threads: self.Threads,
	}}, d.view.machineViews()...)
	labels, err := config.LoadCwdLabels(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "browser mesh: "+err.Error())
		return
	}
	out := api.BrowserMeshResponse{Schema: api.SchemaVersion, Archived: archived == "1", Machines: make([]api.BrowserMeshMachine, 0, len(views))}
	for _, view := range views {
		threads := view.Threads
		if archived != "1" {
			threads = make([]api.ThreadSnapshot, 0, len(view.Threads))
			for _, t := range view.Threads {
				if browserMeshKeeps(t) {
					threads = append(threads, t)
				}
			}
		}
		if threads == nil {
			threads = []api.ThreadSnapshot{}
		}
		out.Machines = append(out.Machines, api.BrowserMeshMachine{
			Machine: view.Machine, Self: view.Self, Reachable: view.Reachable,
			Routable: view.Self || d.authorizeBrowserOwner(view.Machine) == nil, SyncedAtUnix: view.SyncedAtUnix, Threads: threads,
			CwdLabels: browserCwdLabels(labels, threads),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// browserCwdLabels applies the hub's [[cwd_label]] rules to each thread's cwd as
// `sesh tui` does (cwdDisplay: the owner-relative cwd_rel, else the absolute cwd,
// passed through LabelFor). A rule that fails to expand is shown as the TUI shows
// it, "!ERR <reason>", rather than dropped.
func browserCwdLabels(labels *config.Labeling, threads []api.ThreadSnapshot) map[string]string {
	out := map[string]string{}
	if labels == nil {
		return out
	}
	home, _ := os.UserHomeDir()
	for _, t := range threads {
		disp := t.CwdRel
		if disp == "" {
			disp = config.TildeRelative(t.Cwd, home)
		}
		label, err := labels.LabelFor(disp, home)
		if err != nil {
			label = "!ERR " + err.Error()
		}
		if label != disp {
			out[t.ID] = label
		}
	}
	return out
}

// browserMeshKeeps is the default (non-archived) filter: every live thread, and an
// archived one only while the TUI's active view would still show it.
func browserMeshKeeps(t api.ThreadSnapshot) bool {
	return !t.Archived || t.Flagged || t.Head == api.Headful || t.Busy == api.BusyBusy
}

// handleBrowserThreadOp forwards one allowlisted thread op to its owner. The hub
// reads only the canonical body's `id` (to find the owner); the body itself is
// forwarded unchanged and validated by the owner's canonical handler.
func (d *Daemon) handleBrowserThreadOp(w http.ResponseWriter, r *http.Request) {
	var req api.BrowserThreadOpRequest
	if err := decodeBrowserJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	canonical, ok := browserThreadOps[req.Op]
	if !ok {
		writeError(w, http.StatusBadRequest, "browser thread op: unknown op "+req.Op)
		return
	}
	var target struct {
		ID string `json:"id"`
	}
	if len(req.Request) == 0 || json.Unmarshal(req.Request, &target) != nil || !validCanonicalUUID(target.ID) {
		writeError(w, http.StatusBadRequest, "browser thread op: request must be the canonical body with a canonical UUID `id`")
		return
	}
	owner, err := d.browserThreadOwnerFresh(r.Context(), target.ID)
	if err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	d.serveBrowserCanonical(w, r, owner, http.MethodPost, canonical, nil, req.Request)
}

// handleBrowserThreadRead forwards a per-thread GET (?id=) to the thread's owner.
func (d *Daemon) handleBrowserThreadRead(canonical string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !onlyQuery(w, r, "id") {
			return
		}
		id := r.URL.Query().Get("id")
		if !validCanonicalUUID(id) {
			writeError(w, http.StatusBadRequest, "browser thread read: id must be a canonical UUID")
			return
		}
		owner, err := d.browserThreadOwnerFresh(r.Context(), id)
		if err != nil {
			writeError(w, browserOwnerStatus(err), err.Error())
			return
		}
		if err := d.authorizeBrowserOwner(owner); err != nil {
			writeError(w, browserOwnerStatus(err), err.Error())
			return
		}
		d.serveBrowserCanonical(w, r, owner, http.MethodGet, canonical, url.Values{"id": {id}}, nil)
	}
}
