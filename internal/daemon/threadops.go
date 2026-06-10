package daemon

import (
	"strconv"

	"errors"
	"github.com/lukastk/sesh/internal/agents"
	"net/http"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesThreadOps(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/rename", d.handleThreadRename)
	mux.HandleFunc("POST /v1/threads/reparent", d.handleThreadReparent)
	mux.HandleFunc("POST /v1/threads/notify", d.handleThreadNotify)
	mux.HandleFunc("GET /v1/threads/transcript", d.handleThreadTranscript)
	mux.HandleFunc("POST /v1/threads/tag", d.handleThreadTag)
	mux.HandleFunc("POST /v1/threads/archive", d.handleThreadArchive)
	mux.HandleFunc("POST /v1/threads/stop", d.handleThreadStop)
	mux.HandleFunc("POST /v1/threads/delete", d.handleThreadDelete)
}

// handleThreadStop ends the thread's runtime — kills its tmux session (which
// kills the agent) — but KEEPS the record, which becomes a normal dead thread
// (resumable via `resume`). This is the runtime half of the old `kill`; dropping
// the record is a separate `delete`. Stopping a thread whose session is already
// gone is not an error (the runtime was already down) — it is idempotent.
func (d *Daemon) handleThreadStop(w http.ResponseWriter, r *http.Request) {
	var req api.StopThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "stop: id is required")
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	if d.tmux.HasSession(thread.SessionName) {
		if err := d.tmux.KillSession(thread.SessionName); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "stopped": req.ID})
}

func (d *Daemon) handleThreadRename(w http.ResponseWriter, r *http.Request) {
	var req api.RenameThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "rename: id and name are required")
		return
	}
	if err := d.store.RenameThread(req.ID, req.Name); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

// handleThreadTag adds and/or removes tags, preserving order and de-duplicating.
func (d *Daemon) handleThreadTag(w http.ResponseWriter, r *http.Request) {
	var req api.TagThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	remove := map[string]bool{}
	for _, t := range req.Remove {
		remove[t] = true
	}
	seen := map[string]bool{}
	var tags []string
	for _, t := range thread.Tags {
		if remove[t] || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	for _, t := range req.Add {
		if t == "" || remove[t] || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	if err := d.store.SetThreadTags(req.ID, tags); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

func (d *Daemon) handleThreadArchive(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "archive: id is required")
		return
	}
	if err := d.store.SetThreadArchived(req.ID, req.Archived); err != nil {
		d.threadOpErr(w, err)
		return
	}
	d.respondThread(w, req.ID)
}

// handleThreadDelete drops the record only — the runtime (agent + tmux session)
// is left untouched. It is for forgetting a record, usually an already-dead one.
// Deleting a thread whose runtime is still LIVE orphans its agent (record gone,
// process still running), so delete refuses a live thread unless Force is set —
// the natural order is `stop` then `delete` (which is what `kill` was).
func (d *Daemon) handleThreadDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "delete: id is required")
		return
	}
	thread, err := d.store.GetThread(req.ID)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	if !req.Force {
		if _, live, err := d.tmux.FindPaneByThreadID(req.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		} else if live {
			writeError(w, http.StatusConflict,
				"delete: thread "+thread.SessionName+" is live (agent running); run `stop` first, or delete --force to drop the record and orphan the agent")
			return
		}
	}
	if err := d.store.DeleteThread(req.ID); err != nil {
		d.threadOpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "deleted": req.ID})
}

func (d *Daemon) respondThread(w http.ResponseWriter, id string) {
	thread, err := d.store.GetThread(id)
	if err != nil {
		d.threadOpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: thread})
}

func (d *Daemon) threadOpErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrThreadNotFound) {
		writeError(w, http.StatusNotFound, "thread not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// handleThreadReparent re-parents a thread (” = root). The new parent must
// exist, and the chain from it must not loop back through the thread (a cycle
// would hang every tree walk) — both are loud refusals.
func (d *Daemon) handleThreadReparent(w http.ResponseWriter, r *http.Request) {
	var req api.ReparentThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "reparent: id is required")
		return
	}
	if _, err := d.store.GetThread(req.ID); err != nil {
		writeError(w, http.StatusNotFound, "reparent: thread "+req.ID+" not found")
		return
	}
	if req.Parent != "" {
		if req.Parent == req.ID {
			writeError(w, http.StatusBadRequest, "reparent: a thread cannot be its own parent")
			return
		}
		seen := map[string]bool{req.ID: true}
		for p := req.Parent; p != ""; {
			if seen[p] {
				writeError(w, http.StatusBadRequest, "reparent: would create a cycle through "+p)
				return
			}
			seen[p] = true
			th, err := d.store.GetThread(p)
			if err != nil {
				writeError(w, http.StatusBadRequest, "reparent: parent "+p+" does not exist")
				return
			}
			p = th.Parent
		}
	}
	if err := d.store.SetThreadParent(req.ID, req.Parent); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: th})
}

// handleThreadNotify toggles a thread's notification gate.
func (d *Daemon) handleThreadNotify(w http.ResponseWriter, r *http.Request) {
	var req api.NotifyThreadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "notify: id is required")
		return
	}
	if err := d.store.SetThreadNotify(req.ID, req.On); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ThreadResponse{Schema: api.SchemaVersion, Thread: th})
}

// handleThreadTranscript is the owner-side transcript read (D0): the raw
// lines (?tail=N for the last N) + the last assistant reply with its monotone
// count. Transcript content is NOT mesh-replicated — remote reads route here.
func (d *Daemon) handleThreadTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "transcript: id is required")
		return
	}
	th, err := d.store.GetThread(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	tail := -1
	if t := r.URL.Query().Get("tail"); t != "" {
		n, perr := strconv.Atoi(t)
		if perr != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "transcript: bad tail "+t)
			return
		}
		tail = n
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	kind := agents.Kind(th.AgentKind)
	lines, err := agents.ReadTranscript(kind, th.AgentSessionID, th.Cwd, homes, tail)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	path, _, _ := agents.TranscriptPath(kind, th.AgentSessionID, th.Cwd, homes)
	reply, count, err := agents.LastReply(kind, th.AgentSessionID, th.Cwd, homes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.TranscriptResponse{
		Schema: api.SchemaVersion, ID: id, Path: path,
		Lines: lines, LastReply: reply, ReplyCount: count,
	})
}
