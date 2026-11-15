package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
)

func browserMesh(t *testing.T, d *Daemon, target string) api.BrowserMeshResponse {
	t.Helper()
	rec := browserCall(t, d, http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", target, rec.Code, rec.Body.String())
	}
	var out api.BrowserMeshResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func meshThreadIDs(resp api.BrowserMeshResponse, machine string) map[string]bool {
	ids := map[string]bool{}
	for _, m := range resp.Machines {
		if m.Machine == machine {
			for _, th := range m.Threads {
				ids[th.ID] = true
			}
		}
	}
	return ids
}

// The default mesh drops quiet archived threads but keeps a flagged archived one
// (the TUI's active view shows it); archived=1 returns every thread. Every machine
// says whether ops route to it.
func TestBrowserMeshFiltersQuietArchivedThreadsAndReportsRoutability(t *testing.T) {
	hub, _ := browserOpsMesh(t)
	live, quiet, flagged := uuid.NewString(), uuid.NewString(), uuid.NewString()
	insertBrowserThread(t, hub, live, uuid.NewString(), "live", 10)
	insertBrowserThread(t, hub, quiet, uuid.NewString(), "quiet", 11)
	insertBrowserThread(t, hub, flagged, uuid.NewString(), "flagged", 12)
	for _, id := range []string{quiet, flagged} {
		if err := hub.store.SetThreadArchived(id, true, 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := hub.store.SetThreadFlagAction(flagged, api.FlagOn, "needs you"); err != nil {
		t.Fatal(err)
	}
	hub.maint.tick()

	def := browserMesh(t, hub, "/v1/browser/mesh")
	if def.Archived {
		t.Fatal("default mesh says archived")
	}
	ids := meshThreadIDs(def, "hub")
	if !ids[live] || !ids[flagged] || ids[quiet] {
		t.Fatalf("default mesh ids %v: want live and flagged, not the quiet archived one", ids)
	}
	all := browserMesh(t, hub, "/v1/browser/mesh?archived=1")
	if ids := meshThreadIDs(all, "hub"); !all.Archived || !ids[live] || !ids[flagged] || !ids[quiet] {
		t.Fatalf("archived=1 mesh ids %v archived=%v: want all three", ids, all.Archived)
	}
	routable := map[string]bool{}
	for _, m := range def.Machines {
		routable[m.Machine] = m.Routable
	}
	if !routable["hub"] || !routable["peer"] || routable["stranger"] {
		t.Fatalf("routable = %v, want hub and peer, not the untrusted stranger", routable)
	}
	for _, target := range []string{"/v1/browser/mesh?archived=yes", "/v1/browser/mesh?view=all"} {
		if rec := browserCall(t, hub, http.MethodGet, target, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", target, rec.Code)
		}
	}
}

// An op reaches the thread's owner unchanged; an unknown op, a body without a
// canonical id and an untrusted owner are refused.
func TestBrowserThreadOpForwardsToTheOwnerAndRefusesTheRest(t *testing.T) {
	hub, peer := browserOpsMesh(t)
	id := uuid.NewString()
	insertBrowserThread(t, peer, id, uuid.NewString(), "remote", 20)
	peer.maint.tick()
	op := func(name string, request any) int {
		raw, _ := json.Marshal(request)
		rec := browserCall(t, hub, http.MethodPost, "/v1/browser/threads/op", api.BrowserThreadOpRequest{Op: name, Request: raw})
		return rec.Code
	}
	if code := op("flag", api.FlagThreadRequest{ID: id, Action: api.FlagOn}); code != http.StatusOK {
		t.Fatalf("flag status %d", code)
	}
	if got, _ := peer.store.GetThread(id); !got.Flagged {
		t.Fatalf("flag did not reach the owner: %#v", got)
	}
	if code := op("notify", api.NotifyThreadRequest{ID: id, On: true}); code != http.StatusOK {
		t.Fatalf("notify status %d", code)
	}
	if got, _ := peer.store.GetThread(id); !got.Notify {
		t.Fatalf("notify did not reach the owner: %#v", got)
	}
	if code := op("send", map[string]string{"id": id}); code != http.StatusBadRequest {
		t.Fatalf("unknown op status %d, want 400", code)
	}
	if code := op("flag", map[string]string{"id": "not-a-uuid"}); code != http.StatusBadRequest {
		t.Fatalf("bad id status %d, want 400", code)
	}
	if code := op("flag", api.FlagThreadRequest{ID: uuid.NewString(), Action: api.FlagOn}); code != http.StatusNotFound {
		t.Fatalf("unknown thread status %d, want 404", code)
	}
}
