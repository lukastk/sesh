package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

// browserOpsMesh is a hub that trusts "peer" (a real HTTP daemon) and also knows
// "stranger", a registered peer the browser hub does not trust.
func browserOpsMesh(t *testing.T) (hub, peer *Daemon) {
	t.Helper()
	peer = newBrowserTestDaemon(t, "peer", "")
	peerSrv := httptest.NewServer(peer.tokenAuth("peer-token", peer.routes()))
	t.Cleanup(peerSrv.Close)
	peerURL, _ := url.Parse(peerSrv.URL)

	hub = newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	reg := peers.Registry{Peers: map[string]peers.Peer{
		"peer":     {Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "peer-token"},
		"stranger": {Machine: "stranger", ApiAddr: "127.0.0.1:1", ApiToken: "x"},
	}}
	if err := reg.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := hub.view.replaceAll("peer", now, nil); err != nil {
		t.Fatal(err)
	}
	if err := hub.view.replaceAll("stranger", now, nil); err != nil {
		t.Fatal(err)
	}
	return hub, peer
}

func browserCall(t *testing.T, d *Daemon, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	rec := httptest.NewRecorder()
	d.routes().ServeHTTP(rec, httptest.NewRequest(method, target, reader))
	return rec
}

func routed(machine string, request any) api.BrowserRoutedRequest {
	raw, _ := json.Marshal(request)
	return api.BrowserRoutedRequest{Machine: machine, Request: raw}
}

func TestBrowserTicketsRouteToTheOwnerThroughTheHub(t *testing.T) {
	hub, peer := browserOpsMesh(t)

	rec := browserCall(t, hub, http.MethodPost, "/v1/browser/tickets", routed("peer", api.CreateTicketRequest{Name: "routed", Prompt: "do it"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	var created api.TicketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.store.GetTicket(created.Ticket.ID); err != nil {
		t.Fatalf("ticket did not land on the owner: %v", err)
	}
	if _, err := hub.store.GetTicket(created.Ticket.ID); err == nil {
		t.Fatal("ticket landed on the hub as well")
	}

	rec = browserCall(t, hub, http.MethodGet, "/v1/browser/tickets/get?machine=peer&id="+created.Ticket.ID, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"routed"`) {
		t.Fatalf("get status %d: %s", rec.Code, rec.Body.String())
	}

	name := "renamed"
	rec = browserCall(t, hub, http.MethodPost, "/v1/browser/tickets/set", routed("peer", api.SetTicketRequest{ID: created.Ticket.ID, Name: &name}))
	if rec.Code != http.StatusOK {
		t.Fatalf("set status %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := peer.store.GetTicket(created.Ticket.ID); got.Name != "renamed" {
		t.Fatalf("set did not reach the owner: %#v", got)
	}

	rec = browserCall(t, hub, http.MethodPost, "/v1/browser/tickets/delete", routed("peer", map[string]string{"id": created.Ticket.ID}))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := peer.store.GetTicket(created.Ticket.ID); err == nil {
		t.Fatal("delete did not reach the owner")
	}

	// No machine: the ticket lands on the daemon answering, like POST /v1/tickets.
	rec = browserCall(t, hub, http.MethodPost, "/v1/browser/tickets", routed("", api.CreateTicketRequest{Name: "local"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("local create status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBrowserOpsRefuseUntrustedOwnersBadBodiesAndUnknownQueries(t *testing.T) {
	hub, _ := browserOpsMesh(t)
	cases := []struct {
		name, method, target string
		body                 any
		status               int
	}{
		{"untrusted owner", http.MethodPost, "/v1/browser/tickets", routed("stranger", api.CreateTicketRequest{Name: "x"}), http.StatusForbidden},
		{"untrusted fs owner", http.MethodGet, "/v1/browser/fs/list?machine=stranger&path=~", nil, http.StatusForbidden},
		{"missing request", http.MethodPost, "/v1/browser/tickets/status", map[string]string{"machine": "peer"}, http.StatusBadRequest},
		{"unknown envelope field", http.MethodPost, "/v1/browser/blobs", map[string]any{"machine": "peer", "request": map[string]string{}, "extra": 1}, http.StatusBadRequest},
		{"unknown query", http.MethodGet, "/v1/browser/tickets/get?machine=peer&id=x&oops=1", nil, http.StatusBadRequest},
		{"move to untrusted", http.MethodPost, "/v1/browser/tickets/move", api.MoveTicketRequest{ID: "x", From: "peer", To: "stranger"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		rec := browserCall(t, hub, tc.method, tc.target, tc.body)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d: %s", tc.name, rec.Code, tc.status, rec.Body.String())
		}
	}
}

func TestBrowserBlobsAndFsListRouteToTheOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "dev", "box-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	hub, peer := browserOpsMesh(t)

	rec := browserCall(t, hub, http.MethodPost, "/v1/browser/blobs", routed("peer", api.AddBlobRequest{Name: "note.txt", Data: []byte("hello")}))
	if rec.Code != http.StatusOK {
		t.Fatalf("blob status %d: %s", rec.Code, rec.Body.String())
	}
	var blob api.BlobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &blob); err != nil {
		t.Fatal(err)
	}
	if path, err := peer.blobs.Path(blob.Blob.Hash); err != nil || path == "" {
		t.Fatalf("blob did not land on the owner: %v", err)
	}

	// The owner expands its own tokens; a browser send then delivers the result verbatim.
	rec = browserCall(t, hub, http.MethodPost, "/v1/browser/blobs/expand", routed("peer", api.ExpandBlobsRequest{Text: "see @blob(" + blob.Blob.Hash[:12] + ")"}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "note.txt") {
		t.Fatalf("expand status %d: %s", rec.Code, rec.Body.String())
	}

	// Past the 1 MiB every other browser body is held to: the blob route has its own bound.
	big := bytes.Repeat([]byte{7}, 2<<20)
	rec = browserCall(t, hub, http.MethodPost, "/v1/browser/blobs", routed("peer", api.AddBlobRequest{Name: "big.bin", Data: big}))
	if rec.Code != http.StatusOK {
		t.Fatalf("2 MiB blob status %d: %s", rec.Code, rec.Body.String())
	}
	rec = browserCall(t, hub, http.MethodPost, "/v1/browser/tickets", routed("peer", api.CreateTicketRequest{Name: "x", Prompt: string(bytes.Repeat([]byte("p"), 2<<20))}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("2 MiB ticket body status %d, want 400", rec.Code)
	}

	rec = browserCall(t, hub, http.MethodGet, "/v1/browser/fs/list?machine=peer&path="+url.QueryEscape("~/dev"), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "box-a") {
		t.Fatalf("fs list status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBrowserMachinesSaysWhichMachinesTheHubRoutesTo(t *testing.T) {
	hub, _ := browserOpsMesh(t)
	rec := browserCall(t, hub, http.MethodGet, "/v1/browser/machines", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("machines status %d: %s", rec.Code, rec.Body.String())
	}
	var got api.BrowserMachinesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	routable := map[string]bool{}
	for _, m := range got.Machines {
		routable[m.Machine] = m.Routable
	}
	if got.Machines[0].Machine != "hub" || !got.Machines[0].Self {
		t.Fatalf("self first: %#v", got.Machines)
	}
	if !routable["hub"] || !routable["peer"] || routable["stranger"] {
		t.Fatalf("routable = %#v", routable)
	}
}

func TestBrowserThreadRowsCarryCwdRelAndCreatedAt(t *testing.T) {
	hub := newBrowserTestDaemon(t, "hub", "")
	id := uuid.NewString()
	th := insertBrowserThread(t, hub, id, uuid.NewString(), "t", 42)
	rows, err := hub.browserThreadRows()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CreatedAtUnix != 42 || rows[0].CwdRel == "" {
		t.Fatalf("rows = %#v (cwd %s)", rows, th.Cwd)
	}
}

func TestBrowserCapabilitiesListTheOperations(t *testing.T) {
	tokens := strings.Join(browserCapabilityTokens(), ",")
	for _, want := range []string{"browser.tickets-v1", "browser.fs-list-v1", "browser.blobs-v1", "browser.machines-v1"} {
		if !strings.Contains(tokens, want) {
			t.Fatalf("capabilities %s lack %s", tokens, want)
		}
	}
	for path := range browserOwnerPosts {
		if _, ok := browserCORSRoutes[path]; !ok {
			t.Fatalf("%s has no CORS policy", path)
		}
	}
	for _, path := range []string{"/v1/browser/tickets/get", "/v1/browser/tickets/find", "/v1/browser/tickets/list-all", "/v1/browser/tickets/move", "/v1/browser/fs/list", "/v1/browser/machines"} {
		if _, ok := browserCORSRoutes[path]; !ok {
			t.Fatalf("%s has no CORS policy", path)
		}
	}
}

// A browser create on a peer followed at once by an action: the hub has not synced
// the new thread yet, so the action asks the trusted peers to sync and then routes.
func TestBrowserActionFindsAThreadAPeerCreatedSinceTheLastSync(t *testing.T) {
	hub, peer := browserOpsMesh(t)
	id := uuid.NewString()
	insertBrowserThread(t, peer, id, uuid.NewString(), "fresh", 30)
	peer.maint.tick() // the peer's own snapshot now has it, as its running maintainer would
	if owner, err := hub.browserThreadOwner(id); err == nil {
		t.Fatalf("the hub already knows the thread (owner %q); the test needs a stale view", owner)
	}
	action := map[string]string{"thread_id": id, "action": "rename", "name": "renamed"}
	rec := browserCall(t, hub, http.MethodPost, "/v1/browser/threads/action", action)
	if rec.Code != http.StatusOK {
		t.Fatalf("action on a just-created peer thread: status %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := peer.store.GetThread(id); got.Name != "renamed" {
		t.Fatalf("rename did not reach the owner: %#v", got)
	}

	// A thread nobody has still fails, after the bounded wait.
	rec = browserCall(t, hub, http.MethodPost, "/v1/browser/threads/action", map[string]string{"thread_id": uuid.NewString(), "action": "rename", "name": "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread status %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
