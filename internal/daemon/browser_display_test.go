package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents/pi"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

// insertDisplayThread records a thread of the given kind and writes a
// pi-format conversation file for it (pi and mypi share one store and format).
func insertDisplayThread(t *testing.T, d *Daemon, kind, piHome string, lines []string) api.Thread {
	t.Helper()
	id, session := uuid.NewString(), uuid.NewString()
	cwd := filepath.Join(t.TempDir(), "proj")
	// A real cwd: a headless turn runs IN it, so a path that does not exist
	// fails the spawn before the agent is ever reached.
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	thread := api.Thread{
		ID: id, Machine: d.cfg.Machine, SessionName: "headless-" + id, Cwd: cwd,
		AgentKind: kind, Name: "display-" + kind, Tags: []string{},
		CreatedAtUnix: time.Now().Unix(), AgentSessionID: session,
	}
	if err := d.store.InsertThread(thread); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(pi.SessionsRoot(piHome), pi.CwdDirName(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-10-30T00-00-00-000Z_"+session+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return thread
}

var displayConversation = []string{
	`{"type":"session","version":3,"id":"s","timestamp":"2026-10-30T10:00:00.000Z","cwd":"/x"}`,
	`{"type":"message","id":"u1","timestamp":"2026-10-30T10:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"first"}]}}`,
	`{"type":"message","id":"a1","timestamp":"2026-10-30T10:00:02.000Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"second"}]}}`,
}

func decodeDisplayResponse(t *testing.T, rec *httptest.ResponseRecorder) api.BrowserDisplayResponse {
	t.Helper()
	var resp api.BrowserDisplayResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode display response: %v: %s", err, rec.Body.String())
	}
	return resp
}

func displayRequest(t *testing.T, d *Daemon, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	d.handleBrowserThreadDisplay(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/display?"+query, nil))
	return rec
}

// The projection answers for every harness kind sesh runs, including mypi,
// against a stopped thread with no pane and no process anywhere.
func TestBrowserDisplayServesEveryHarnessKindWithoutARuntime(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	for _, kind := range []string{"pi", "mypi"} {
		thread := insertDisplayThread(t, d, kind, piHome, displayConversation)
		rec := displayRequest(t, d, "id="+thread.ID)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s display = %d %s", kind, rec.Code, rec.Body.String())
		}
		resp := decodeDisplayResponse(t, rec)
		if resp.Source != "sesh-display-v1" || resp.Agent != kind || resp.OwnerMachine != "self" {
			t.Fatalf("%s display envelope = %+v", kind, resp)
		}
		if resp.Coverage.Format != "pi-session-jsonl" || resp.Coverage.Source != "resolved-current-session-file" {
			t.Fatalf("%s coverage = %+v", kind, resp.Coverage)
		}
		if len(resp.Items) != 2 || resp.Items[0].Text != "first" || resp.Items[1].Text != "second" {
			t.Fatalf("%s items = %+v", kind, resp.Items)
		}
		if resp.TurnStatus != "idle" || !resp.Coverage.Complete || resp.Coverage.SkippedRecords != 1 {
			t.Fatalf("%s status/coverage = %q %+v", kind, resp.TurnStatus, resp.Coverage)
		}
		// A forward-poll token is always present; nothing older exists here.
		if resp.NewerCursor == "" || resp.OlderCursor != "" {
			t.Fatalf("%s cursors = newer %q older %q", kind, resp.NewerCursor, resp.OlderCursor)
		}
	}
}

func TestBrowserDisplayValidatesItsQueryExactly(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread := insertDisplayThread(t, d, "pi", piHome, displayConversation)
	for _, query := range []string{
		"id=not-a-uuid",
		"id=" + strings.ToUpper(thread.ID),
		"id=" + thread.ID + "&id=" + thread.ID,
		"id=" + thread.ID + "&limit=0",
		"id=" + thread.ID + "&limit=501",
		"id=" + thread.ID + "&limit=2&limit=3",
		"id=" + thread.ID + "&tail=5",
		"id=" + thread.ID + "&cursor=" + strings.Repeat("x", 3000),
		"id=" + thread.ID + "&cursor=not-base64!!",
	} {
		if rec := displayRequest(t, d, query); rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q = %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if rec := displayRequest(t, d, "id="+uuid.NewString()); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown thread = %d", rec.Code)
	}
}

// A cursor is bound to the thread's CURRENT resolved source file, so a rotated
// native session refuses it loudly instead of paging another file's lines.
func TestBrowserDisplayCursorsPageAndRefuseAForeignSource(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	lines := []string{displayConversation[0]}
	for i := 0; i < 6; i++ {
		lines = append(lines, `{"type":"message","id":"m`+string(rune('a'+i))+`","timestamp":"2026-10-30T10:00:0`+string(rune('0'+i))+`.000Z","message":{"role":"user","content":[{"type":"text","text":"m`+string(rune('a'+i))+`"}]}}`)
	}
	thread := insertDisplayThread(t, d, "pi", piHome, lines)

	newest := decodeDisplayResponse(t, displayRequest(t, d, "id="+thread.ID+"&limit=2"))
	if len(newest.Items) != 2 || newest.Items[0].Text != "me" || newest.OlderCursor == "" {
		t.Fatalf("newest page = %+v (older cursor %q)", newest.Items, newest.OlderCursor)
	}
	older := decodeDisplayResponse(t, displayRequest(t, d, "id="+thread.ID+"&limit=2&cursor="+newest.OlderCursor))
	if len(older.Items) != 2 || older.Items[0].Text != "mc" || older.Coverage.Direction != "older" {
		t.Fatalf("older page = %+v (%+v)", older.Items, older.Coverage)
	}
	if older.Coverage.Complete {
		t.Fatal("an older page claimed complete file coverage")
	}

	// The same cursor against a DIFFERENT thread's source file is a conflict,
	// not a page of someone else's line numbers.
	other := insertDisplayThread(t, d, "pi", piHome, lines)
	rec := displayRequest(t, d, "id="+other.ID+"&cursor="+newest.OlderCursor)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "different native session file") {
		t.Fatalf("cross-thread cursor = %d %s", rec.Code, rec.Body.String())
	}
}

// The display read must work for a thread whose conversation file exists but
// whose runtime does not, and must refuse a node kind that has no conversation.
func TestBrowserDisplayRefusesNonConversationNodesAndMissingFiles(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	virtual := api.Thread{
		ID: uuid.NewString(), Machine: "self", SessionName: "virtual", AgentKind: api.VirtualAgentKind,
		Name: "group", Tags: []string{}, CreatedAtUnix: time.Now().Unix(),
	}
	if err := d.store.InsertThread(virtual); err != nil {
		t.Fatal(err)
	}
	if rec := displayRequest(t, d, "id="+virtual.ID); rec.Code != http.StatusConflict {
		t.Fatalf("virtual node display = %d %s", rec.Code, rec.Body.String())
	}
	// A real harness thread whose file is not on this owner: a loud 404 naming
	// the session, never an empty page that looks like an empty conversation.
	noFile := api.Thread{
		ID: uuid.NewString(), Machine: "self", SessionName: "headless", Cwd: t.TempDir(),
		AgentKind: "claude", Name: "nofile", Tags: []string{}, CreatedAtUnix: time.Now().Unix(),
		AgentSessionID: uuid.NewString(),
	}
	if err := d.store.InsertThread(noFile); err != nil {
		t.Fatal(err)
	}
	rec := displayRequest(t, d, "id="+noFile.ID)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), noFile.AgentSessionID) {
		t.Fatalf("missing-file display = %d %s", rec.Code, rec.Body.String())
	}
}

// The browser-hub policy governs the display read exactly as it governs the raw
// transcript: refused with the hub off, routed to the owner with it on.
func TestBrowserDisplayRoutesThroughATrustedHTTPPeer(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	owner := newBrowserTestDaemon(t, "peer", "")
	thread := insertDisplayThread(t, owner, "mypi", piHome, displayConversation)
	ownerSrv := httptest.NewServer(owner.tokenAuth("peer-secret", owner.routes()))
	defer ownerSrv.Close()

	snapshot := []api.ThreadSnapshot{{Thread: thread, Head: api.Headless, Busy: api.BusyIdle}}
	closed := newBrowserTestDaemon(t, "hub", "")
	if err := closed.view.replaceAll("peer", time.Now().Unix(), snapshot); err != nil {
		t.Fatal(err)
	}
	if rec := displayRequest(t, closed, "id="+thread.ID); rec.Code != http.StatusForbidden {
		t.Fatalf("display with the hub disabled = %d %s", rec.Code, rec.Body.String())
	}

	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	if err := hub.view.replaceAll("peer", time.Now().Unix(), snapshot); err != nil {
		t.Fatal(err)
	}
	reg := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: strings.TrimPrefix(ownerSrv.URL, "http://"), ApiToken: "peer-secret"}}}
	if err := reg.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	rec := displayRequest(t, hub, "id="+thread.ID+"&limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("routed display = %d %s", rec.Code, rec.Body.String())
	}
	resp := decodeDisplayResponse(t, rec)
	if resp.OwnerMachine != "peer" || resp.Agent != "mypi" || len(resp.Items) != 2 {
		t.Fatalf("routed display = %+v", resp)
	}
	// Paging works end to end through the peer with the owner's own cursor.
	if resp.NewerCursor == "" {
		t.Fatal("the routed page carried no forward-poll cursor")
	}
	poll := decodeDisplayResponse(t, displayRequest(t, hub, "id="+thread.ID+"&cursor="+resp.NewerCursor))
	if len(poll.Items) != 0 || poll.Coverage.Direction != "newer" {
		t.Fatalf("routed forward poll = %+v (%+v)", poll.Items, poll.Coverage)
	}
}
