package daemon

import (
	"errors"
	"net/http"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesThreadOps(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/threads/rename", d.handleThreadRename)
	mux.HandleFunc("POST /v1/threads/tag", d.handleThreadTag)
	mux.HandleFunc("POST /v1/threads/archive", d.handleThreadArchive)
	mux.HandleFunc("POST /v1/threads/delete", d.handleThreadDelete)
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
// is deliberately left untouched (unlike kill). It is for forgetting a record,
// usually an already-dead one.
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
