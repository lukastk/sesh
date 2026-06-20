package daemon

import (
	"net/http"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

// /v1/peers CRUD over the local peer registry (peers.json). Additive — a client (the
// GUI) manages the mesh without needing local filesystem access. The registry is the
// SAME one the mesh fan-out reads, so an added peer is immediately reachable. ssh
// remains the bootstrap transport; a peer with an api_addr is reached over its TCP API.
func (d *Daemon) routesPeers(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/peers", d.handlePeersList)
	mux.HandleFunc("POST /v1/peers", d.handlePeerAdd)
	mux.HandleFunc("POST /v1/peers/remove", d.handlePeerRemove)
}

func (d *Daemon) handlePeersList(w http.ResponseWriter, r *http.Request) {
	reg, err := peers.Load(d.cfg.PeersPath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.PeersListResponse{Schema: api.SchemaVersion, Peers: reg.List()})
}

func (d *Daemon) handlePeerAdd(w http.ResponseWriter, r *http.Request) {
	var req api.AddPeerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Machine == "" {
		writeError(w, http.StatusBadRequest, "peer add: machine is required")
		return
	}
	if req.SSH == "" {
		// ssh is the bootstrap/admin transport every peer needs (even an http peer).
		writeError(w, http.StatusBadRequest, "peer add: ssh (user@host) is required")
		return
	}
	path := d.cfg.PeersPath()
	reg, err := peers.Load(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := reg.Add(req.Peer); err != nil { // insert or replace, keyed by machine
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := reg.Save(path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.PeersListResponse{Schema: api.SchemaVersion, Peers: reg.List()})
}

func (d *Daemon) handlePeerRemove(w http.ResponseWriter, r *http.Request) {
	var req api.RemovePeerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Machine == "" {
		writeError(w, http.StatusBadRequest, "peer remove: machine is required")
		return
	}
	path := d.cfg.PeersPath()
	reg, err := peers.Load(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := reg.Remove(req.Machine); err != nil { // loud on an unknown peer
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := reg.Save(path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.PeersListResponse{Schema: api.SchemaVersion, Peers: reg.List()})
}
