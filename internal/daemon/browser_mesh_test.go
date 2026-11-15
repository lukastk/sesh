package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
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

// tui-config resolves the hub's [tui] section as `sesh tui` does: column moves
// applied, custom views placed by position with their filters, keys and colours
// (defaults overlaid). The mesh carries the hub's cwd labels.
func TestBrowserTUIConfigAndCwdLabelsFollowTheHubConfig(t *testing.T) {
	cfg := `
[[cwd_label]]
match = "^/tmp/(?P<rest>.+)$"
label = "tmp:{rest}"

[tui]
view_ring = ["active", "active flagged"]

[[tui.column]]
name = "machine"
position = 1

[[tui.views]]
name = "active flagged"
filter = "flagged and not onhold"
position = 2

[[tui.key]]
command = "fork"
key = "F"

[[tui.glyph_color]]
name = "busy"
color = "#00cc00"
`
	d := newBrowserTestDaemon(t, "hub", cfg)
	rec := browserCall(t, d, http.MethodGet, "/v1/browser/tui-config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tui-config status %d: %s", rec.Code, rec.Body.String())
	}
	var got api.BrowserTUIConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) == 0 || got.Columns[0] != "machine" {
		t.Fatalf("columns %v: want machine moved first", got.Columns)
	}
	names := []string{}
	for _, v := range got.Views {
		names = append(names, v.Name)
	}
	if want := []string{"active", "active flagged", "on hold", "archived", "all"}; strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("views %v, want %v", names, want)
	}
	if got.Views[1].Filter != "flagged and not onhold" || got.Views[0].Filter != "" {
		t.Fatalf("view filters %#v", got.Views)
	}
	if len(got.Keys) != 1 || got.Keys[0].Command != "fork" || got.Keys[0].Key != "F" {
		t.Fatalf("keys %#v", got.Keys)
	}
	if got.GlyphColors["busy"] != "#00cc00" || got.GlyphColors["flag"] == "" || got.ColumnColors["name"] != "blue" {
		t.Fatalf("colours glyph=%v column=%v", got.GlyphColors, got.ColumnColors)
	}
	if strings.Join(got.ViewRing, "|") != "active|active flagged" || !got.MaxColumnWidths {
		t.Fatalf("ring %v cap %v", got.ViewRing, got.MaxColumnWidths)
	}

	id := uuid.NewString()
	th := insertBrowserThread(t, d, id, uuid.NewString(), "labelled", 1)
	d.maint.tick()
	mesh := browserMesh(t, d, "/v1/browser/mesh")
	label := mesh.Machines[0].CwdLabels[id]
	if !strings.HasPrefix(label, "tmp:") || !strings.HasSuffix(th.Cwd, strings.TrimPrefix(label, "tmp:")) {
		t.Fatalf("cwd label %q for %s", label, th.Cwd)
	}

	broken := newBrowserTestDaemon(t, "hub2", "[[tui.views]]\nname = \"x\"\nfilter = \"nonsense ==\"\n")
	if rec := browserCall(t, broken, http.MethodGet, "/v1/browser/tui-config", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("a broken [tui] config: status %d, want 500", rec.Code)
	}
}

// Context and env are read and written on the thread's owner through the hub; the
// machine-scoped model defaults read is served by the named machine; bad ids and
// unknown query parameters are refused.
func TestBrowserThreadContextEnvAndMachineReadsRouteToTheOwner(t *testing.T) {
	hub, peer := browserOpsMesh(t)
	id := uuid.NewString()
	insertBrowserThread(t, peer, id, uuid.NewString(), "remote", 30)
	peer.maint.tick()
	op := func(name string, request any) int {
		raw, _ := json.Marshal(request)
		return browserCall(t, hub, http.MethodPost, "/v1/browser/threads/op", api.BrowserThreadOpRequest{Op: name, Request: raw}).Code
	}
	if code := op("context", api.SetThreadContextRequest{ID: id, Text: "be brief"}); code != http.StatusOK {
		t.Fatalf("context set status %d", code)
	}
	rec := browserCall(t, hub, http.MethodGet, "/v1/browser/threads/context?id="+id, nil)
	var ctx api.ThreadContextResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ctx) != nil || ctx.Text != "be brief" || ctx.Machine != "peer" {
		t.Fatalf("context get %d %s", rec.Code, rec.Body.String())
	}
	if code := op("env", api.SetThreadEnvRequest{ID: id, Set: map[string]string{"FOO": "bar"}}); code != http.StatusOK {
		t.Fatalf("env set status %d", code)
	}
	rec = browserCall(t, hub, http.MethodGet, "/v1/browser/threads/env?id="+id, nil)
	var env api.ThreadEnvResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &env) != nil || env.Vars["FOO"] != "bar" {
		t.Fatalf("env get %d %s", rec.Code, rec.Body.String())
	}
	rec = browserCall(t, hub, http.MethodGet, "/v1/browser/models/defaults?machine=peer", nil)
	var defaults api.ModelDefaultsResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &defaults) != nil || defaults.Machine != "peer" {
		t.Fatalf("model defaults %d %s", rec.Code, rec.Body.String())
	}
	for _, target := range []string{"/v1/browser/threads/env?id=nope", "/v1/browser/threads/context?id=" + id + "&x=1", "/v1/browser/models/defaults?machine=peer&agent=claude"} {
		if rec := browserCall(t, hub, http.MethodGet, target, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", target, rec.Code)
		}
	}
	if rec := browserCall(t, hub, http.MethodGet, "/v1/browser/models/defaults?machine=stranger", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("untrusted machine: status %d, want 403", rec.Code)
	}
}
