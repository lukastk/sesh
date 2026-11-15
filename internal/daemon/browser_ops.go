package daemon

// Browser operations beyond threads (capabilities browser.tickets-v1,
// browser.fs-list-v1, browser.blobs-v1 and browser.machines-v1): tickets, a
// machine's directory listing, blob upload and the machine list, for a browser
// client that holds ONE endpoint and ONE credential. They follow the thread routes'
// pattern: the browser names the owning machine, the hub applies the same policy
// (authorizeBrowserOwner: the hub must be enabled and the peer trusted, reachable
// and on the HTTP transport) and forwards the canonical request UNCHANGED to the
// owner's canonical route, whose response is returned unchanged. Nothing here
// re-implements a ticket, filesystem or blob rule; the canonical handlers own them.
//
// Mesh-wide ticket reads (find, list-all) and the daemon-coordinated move run on
// the hub itself, which already fans out or coordinates through its own peers.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"

	"github.com/lukastk/sesh/internal/api"
)

// browserOwnerPosts maps each owner-routed browser POST to the canonical route it
// forwards to. The request body is api.BrowserRoutedRequest.
var browserOwnerPosts = map[string]string{
	"/v1/browser/tickets":             "/v1/tickets",
	"/v1/browser/tickets/status":      "/v1/tickets/status",
	"/v1/browser/tickets/set":         "/v1/tickets/set",
	"/v1/browser/tickets/delete":      "/v1/tickets/delete",
	"/v1/browser/tickets/send-prompt": "/v1/tickets/send-prompt",
	"/v1/browser/blobs":               "/v1/blobs",
	// Expands @blob() tokens on the owner, for a caller that sends the text through a
	// browser send (which delivers text verbatim and never expands tokens itself).
	"/v1/browser/blobs/expand": "/v1/blobs/expand",
}

// browserMaxBlobBody bounds POST /v1/browser/blobs, whose body carries the blob as
// base64: 48 MiB admits a 32 MiB blob (what sesh-ui sends at most) with room for the
// envelope. Every other browser body keeps browserMaxRequestBody (1 MiB).
const browserMaxBlobBody = 48 << 20

func (d *Daemon) routesBrowserOps(mux *http.ServeMux) {
	for browserPath, canonical := range browserOwnerPosts {
		limit := int64(browserMaxRequestBody)
		if browserPath == "/v1/browser/blobs" {
			limit = browserMaxBlobBody
		}
		mux.HandleFunc("POST "+browserPath, d.browserOwnerPost(canonical, limit))
	}
	mux.HandleFunc("GET /v1/browser/tickets/get", d.handleBrowserTicketGet)
	mux.HandleFunc("GET /v1/browser/tickets/find", d.handleBrowserHubGet("/v1/tickets/find", "id"))
	mux.HandleFunc("GET /v1/browser/tickets/list-all", d.handleBrowserHubGet("/v1/tickets/list-all", "summary"))
	mux.HandleFunc("POST /v1/browser/tickets/move", d.handleBrowserTicketMove)
	mux.HandleFunc("GET /v1/browser/fs/list", d.handleBrowserFsList)
	mux.HandleFunc("GET /v1/browser/machines", d.handleBrowserMachines)
}

// browserOwnerPost forwards Request to the owner's canonical route. An empty
// Machine means this daemon, so a ticket created without one lands here, as the
// canonical POST /v1/tickets does.
func (d *Daemon) browserOwnerPost(canonical string, limit int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req api.BrowserRoutedRequest
		if err := decodeBrowserJSONLimit(w, r, &req, limit); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(req.Request) == 0 || string(req.Request) == "null" {
			writeError(w, http.StatusBadRequest, "browser request: `request` is required")
			return
		}
		owner := req.Machine
		if owner == "" {
			owner = d.cfg.Machine
		}
		if err := d.authorizeBrowserOwner(owner); err != nil {
			writeError(w, browserOwnerStatus(err), err.Error())
			return
		}
		d.serveBrowserCanonical(w, r, owner, http.MethodPost, canonical, nil, req.Request)
	}
}

// browserOwnerQuery reads the `machine` query parameter (empty = this daemon) and
// authorizes it.
func (d *Daemon) browserOwnerQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	owner := r.URL.Query().Get("machine")
	if owner == "" {
		owner = d.cfg.Machine
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return "", false
	}
	return owner, true
}

// onlyQuery refuses any query parameter outside allowed, so a misspelt one fails
// loudly instead of being dropped on the way to the owner.
func onlyQuery(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	ok := map[string]bool{}
	for _, name := range allowed {
		ok[name] = true
	}
	for name := range r.URL.Query() {
		if !ok[name] {
			writeError(w, http.StatusBadRequest, "browser request: unknown query parameter "+name)
			return false
		}
	}
	return true
}

func (d *Daemon) handleBrowserTicketGet(w http.ResponseWriter, r *http.Request) {
	if !onlyQuery(w, r, "machine", "id") {
		return
	}
	owner, ok := d.browserOwnerQuery(w, r)
	if !ok {
		return
	}
	d.serveBrowserCanonical(w, r, owner, http.MethodGet, "/v1/tickets/get", url.Values{"id": {r.URL.Query().Get("id")}}, nil)
}

func (d *Daemon) handleBrowserFsList(w http.ResponseWriter, r *http.Request) {
	if !onlyQuery(w, r, "machine", "path") {
		return
	}
	owner, ok := d.browserOwnerQuery(w, r)
	if !ok {
		return
	}
	d.serveBrowserCanonical(w, r, owner, http.MethodGet, "/v1/fs/list", url.Values{"path": {r.URL.Query().Get("path")}}, nil)
}

// handleBrowserHubGet serves a mesh-wide read on this daemon, passing through only
// the named query parameters.
func (d *Daemon) handleBrowserHubGet(canonical string, params ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !onlyQuery(w, r, params...) {
			return
		}
		query := url.Values{}
		for _, name := range params {
			if value := r.URL.Query().Get(name); value != "" {
				query.Set(name, value)
			}
		}
		d.serveBrowserCanonical(w, r, d.cfg.Machine, http.MethodGet, canonical, query, nil)
	}
}

// handleBrowserTicketMove runs the canonical move on this daemon, which coordinates
// it with both machines. Both ends must be machines the browser hub may reach.
func (d *Daemon) handleBrowserTicketMove(w http.ResponseWriter, r *http.Request) {
	var req api.MoveTicketRequest
	if err := decodeBrowserJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, machine := range []string{req.From, req.To} {
		if machine == "" {
			continue
		}
		if err := d.authorizeBrowserOwner(machine); err != nil {
			writeError(w, browserOwnerStatus(err), err.Error())
			return
		}
	}
	body, _ := json.Marshal(req)
	d.serveBrowserCanonical(w, r, d.cfg.Machine, http.MethodPost, "/v1/tickets/move", nil, body)
}

// handleBrowserMachines lists this daemon and its peers, each with whether the
// browser hub will route to it (`routable`): a client offers only those as targets.
func (d *Daemon) handleBrowserMachines(w http.ResponseWriter, r *http.Request) {
	if !onlyQuery(w, r) {
		return
	}
	self := d.maint.snapshot()
	out := api.BrowserMachinesResponse{Schema: api.SchemaVersion, Machines: []api.BrowserMachine{{
		Machine: d.cfg.Machine, Self: true, Reachable: true, Routable: true, SyncedAtUnix: self.GeneratedAtUnix,
	}}}
	peers := d.view.machineViews()
	sort.Slice(peers, func(i, j int) bool { return peers[i].Machine < peers[j].Machine })
	for _, peer := range peers {
		out.Machines = append(out.Machines, api.BrowserMachine{
			Machine:      peer.Machine,
			Reachable:    peer.Reachable,
			Routable:     d.authorizeBrowserOwner(peer.Machine) == nil,
			SyncedAtUnix: peer.SyncedAtUnix,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
