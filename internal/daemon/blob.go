package daemon

import (
	"net/http"

	"github.com/lukastk/sesh/internal/api"
)

func (d *Daemon) routesBlobs(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/blobs", d.handleBlobAdd)
	mux.HandleFunc("GET /v1/blobs", d.handleBlobList)
	mux.HandleFunc("GET /v1/blobs/get", d.handleBlobGet)
	mux.HandleFunc("POST /v1/blobs/delete", d.handleBlobDelete)
	mux.HandleFunc("GET /v1/blobs/path", d.handleBlobPath)
	mux.HandleFunc("POST /v1/blobs/expand", d.handleBlobExpand)
}

func (d *Daemon) handleBlobAdd(w http.ResponseWriter, r *http.Request) {
	var req api.AddBlobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := d.blobs.Add(req.Name, req.Data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.BlobResponse{Schema: api.SchemaVersion,
		Blob: api.BlobInfo{Hash: hash, Name: req.Name, Size: int64(len(req.Data))}})
}

func (d *Daemon) handleBlobList(w http.ResponseWriter, r *http.Request) {
	list, err := d.blobs.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]api.BlobInfo, 0, len(list))
	for _, b := range list {
		out = append(out, api.BlobInfo{Hash: b.Hash, Name: b.Name, Size: b.Size})
	}
	writeJSON(w, http.StatusOK, api.BlobListResponse{Schema: api.SchemaVersion, Blobs: out})
}

// handleBlobGet streams the RAW bytes (so `blob get` pipes cleanly) with the
// original filename in an X-Blob-Name header (so the move can recreate it by name).
func (d *Daemon) handleBlobGet(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeError(w, http.StatusBadRequest, "blob get: hash is required")
		return
	}
	name, data, err := d.blobs.Bytes(hash)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("X-Blob-Name", name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck — best effort stream
}

func (d *Daemon) handleBlobDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteBlobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Hash == "" {
		writeError(w, http.StatusBadRequest, "blob delete: hash is required")
		return
	}
	if err := d.blobs.Remove(req.Hash); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "deleted": req.Hash})
}

func (d *Daemon) handleBlobPath(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeError(w, http.StatusBadRequest, "blob path: hash is required")
		return
	}
	full, err := d.blobs.Resolve(hash)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	path, err := d.blobs.Path(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.BlobPathResponse{Schema: api.SchemaVersion, Hash: full, Path: path})
}

func (d *Daemon) handleBlobExpand(w http.ResponseWriter, r *http.Request) {
	var req api.ExpandBlobsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := d.blobs.Expand(req.Text)
	if err != nil {
		// A prompt referencing an unknown blob is a client-correctable 400, loud.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.ExpandBlobsResponse{Schema: api.SchemaVersion, Text: out})
}

// expandPrompt runs the local blob expansion over a prompt before it is delivered
// to an agent (send-prompt / thread send / headless). A missing blob is returned as
// an error so the send fails loudly rather than typing a dangling token at the agent.
func (d *Daemon) expandPrompt(text string) (string, error) {
	return d.blobs.Expand(text)
}
