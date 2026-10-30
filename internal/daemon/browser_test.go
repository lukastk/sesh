package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/peers"
)

func newBrowserTestDaemon(t *testing.T, machine, configBody string) *Daemon {
	t.Helper()
	home := t.TempDir()
	if configBody != "" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := New(config.Config{
		Home: home, Machine: machine, MachineExplicit: true,
		TmuxSocket:   "sesh-browser-test-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		MasterSocket: "sesh-browser-master-test-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.store.Close() })
	return d
}

func insertBrowserThread(t *testing.T, d *Daemon, id, sessionID, name string, created int64) api.Thread {
	t.Helper()
	th := api.Thread{ID: id, Machine: d.cfg.Machine, SessionName: "headless-" + id, Cwd: t.TempDir(), AgentKind: "pi", Name: name, Tags: []string{}, CreatedAtUnix: created, AgentSessionID: sessionID}
	if err := d.store.InsertThread(th); err != nil {
		t.Fatal(err)
	}
	return th
}

func shortBrowserTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sesh-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDecodeBrowserJSONBoundsAndTrailingData(t *testing.T) {
	oversized := `{"x":"` + strings.Repeat("x", browserMaxRequestBody) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ws-ticket", strings.NewReader(oversized))
	var body struct {
		X string `json:"x"`
	}
	if err := decodeBrowserJSON(httptest.NewRecorder(), req, &body); err == nil {
		t.Fatal("oversized browser request body was accepted")
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/ws-ticket", strings.NewReader(`{"x":"ok"} {}`))
	if err := decodeBrowserJSON(httptest.NewRecorder(), req, &body); err == nil {
		t.Fatal("trailing browser request JSON was accepted")
	}
}

func TestWSTicketRegistrySingleUseBindingExpiryAndCapacity(t *testing.T) {
	r := newWSTicketRegistry()
	now := time.Unix(1000, 0)
	ticket, err := r.mint("/v1/threads/rpc", uuid.NewString(), "live-v1", "self", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.consume(ticket.value, ticket.path, ticket.threadID, ticket.protocol, now); !ok {
		t.Fatal("valid ticket was refused")
	}
	if _, ok := r.consume(ticket.value, ticket.path, ticket.threadID, ticket.protocol, now); ok {
		t.Fatal("ticket was reusable")
	}
	wrong, _ := r.mint("/v1/threads/rpc", uuid.NewString(), "live-v1", "self", now)
	if _, ok := r.consume(wrong.value, wrong.path, wrong.threadID, "protocol2", now); ok {
		t.Fatal("wrong protocol accepted")
	}
	if _, ok := r.consume(wrong.value, wrong.path, wrong.threadID, wrong.protocol, now); ok {
		t.Fatal("wrong-bound attempt did not consume ticket")
	}
	expired, _ := r.mint("/v1/threads/rpc", uuid.NewString(), "live-v1", "self", now)
	if _, ok := r.consume(expired.value, expired.path, expired.threadID, expired.protocol, now.Add(wsTicketTTL)); ok {
		t.Fatal("expired ticket accepted")
	}
	full := newWSTicketRegistry()
	for i := 0; i < wsTicketCapacity; i++ {
		if _, err := full.mint("/v1/threads/rpc", uuid.NewString(), "live-v1", "self", now); err != nil {
			t.Fatalf("fill ticket %d: %v", i, err)
		}
	}
	if _, err := full.mint("/v1/threads/rpc", uuid.NewString(), "live-v1", "self", now); err == nil {
		t.Fatal("ticket registry exceeded its capacity")
	}
	if _, err := full.mint("/v1/threads/rpc", uuid.NewString(), "live-v1", "self", now.Add(wsTicketTTL)); err != nil {
		t.Fatalf("expired capacity was not reclaimed: %v", err)
	}
}

func TestScopedWSTicketRejectsAmbiguousQueriesExpiryAndOwnerChange(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	threadID := uuid.NewString()
	insertBrowserThread(t, d, threadID, uuid.NewString(), "ticket-scope", time.Now().Unix())
	now := time.Now()
	request := func(rawQuery string) (*http.Request, bool) {
		req := httptest.NewRequest(http.MethodGet, "/v1/threads/terminal?"+rawQuery, nil)
		req.Header.Set("Upgrade", "websocket")
		_, ok := d.authenticateWSTicket(req, now)
		return req, ok
	}
	exact := func(ticket wsTicket) string {
		q := make(url.Values)
		q.Set("id", ticket.threadID)
		q.Set("protocol", ticket.protocol)
		q.Set("ticket", ticket.value)
		return q.Encode()
	}
	assertConsumedRefusal := func(name string, mutate func(url.Values)) {
		t.Helper()
		ticket, err := d.wsTickets.mint("/v1/threads/terminal", threadID, "terminal-v1", "self", now)
		if err != nil {
			t.Fatal(err)
		}
		q, _ := url.ParseQuery(exact(ticket))
		mutate(q)
		if _, ok := request(q.Encode()); ok {
			t.Fatalf("%s ticket query authenticated", name)
		}
		if _, ok := request(exact(ticket)); ok {
			t.Fatalf("%s malformed attempt did not consume the ticket", name)
		}
	}

	assertConsumedRefusal("duplicate id", func(q url.Values) { q.Add("id", threadID) })
	assertConsumedRefusal("duplicate protocol", func(q url.Values) { q.Add("protocol", "terminal-v1") })
	assertConsumedRefusal("duplicate ticket", func(q url.Values) { q.Add("ticket", q.Get("ticket")) })
	assertConsumedRefusal("extra query", func(q url.Values) { q.Set("cols", "80") })

	nonCanonical := strings.ReplaceAll(threadID, "-", "")
	invalid, err := d.wsTickets.mint("/v1/threads/terminal", nonCanonical, "terminal-v1", "self", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := request(exact(invalid)); ok {
		t.Fatal("non-canonical ticket thread id authenticated")
	}

	expired, err := d.wsTickets.mint("/v1/threads/terminal", threadID, "terminal-v1", "self", now.Add(-wsTicketTTL))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := request(exact(expired)); ok {
		t.Fatal("expired ticket authenticated")
	}

	moved, err := d.wsTickets.mint("/v1/threads/terminal", threadID, "terminal-v1", "self", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.DeleteThread(threadID); err != nil {
		t.Fatal(err)
	}
	remote := api.ThreadSnapshot{Thread: api.Thread{ID: threadID, Machine: "other", AgentKind: "pi", Name: "moved"}}
	if err := d.view.replaceAll("other", now.Unix(), []api.ThreadSnapshot{remote}); err != nil {
		t.Fatal(err)
	}
	if _, ok := request(exact(moved)); ok {
		t.Fatal("ticket followed a changed thread owner")
	}
}

func TestWSTicketMintRequiresCanonicalThreadID(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	threadID := uuid.NewString()
	insertBrowserThread(t, d, threadID, uuid.NewString(), "canonical", time.Now().Unix())
	body := []byte(`{"thread_id":"` + strings.ReplaceAll(threadID, "-", "") + `","route":"terminal","protocol":"terminal-v1"}`)
	rec := httptest.NewRecorder()
	d.handleWSTicket(rec, httptest.NewRequest(http.MethodPost, "/v1/ws-ticket", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-canonical ticket id status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLiveRPCWebSocketUsesLiveSocketWithoutAutoSubscribe(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	threadID, sessionID := uuid.NewString(), uuid.NewString()
	insertBrowserThread(t, d, threadID, sessionID, "live", time.Now().Unix())
	tmp := shortBrowserTempDir(t)
	t.Setenv("TMPDIR", tmp)
	dir := filepath.Join(tmp, "pi-rpc-sockets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, sessionID+".live.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotLine := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		gotLine <- line
		_, _ = conn.Write([]byte("{\"protocol\":\"live-v1\",\"type\":\"response\"}\n"))
	}()

	srv := httptest.NewServer(d.routes())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/threads/rpc?id=" + threadID + "&protocol=live-v1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"protocol":"live-v1","id":"one","op":"snapshot"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-gotLine:
		if strings.Contains(line, "subscribe") || !strings.Contains(line, `"op":"snapshot"`) {
			t.Fatalf("first live socket line = %q; live-v1 must not auto-subscribe", line)
		}
	case <-ctx.Done():
		t.Fatal("live socket did not receive browser request")
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"type":"response"`)) {
		t.Fatalf("ws output = %s", data)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "done")

	resp, err := http.Get(srv.URL + "/v1/threads/rpc?id=" + threadID + "&protocol=unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown protocol status = %d", resp.StatusCode)
	}
}

func TestHeaderOnlyMintAndSingleUseTicketAuthenticatesWebSocket(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	threadID, sessionID := uuid.NewString(), uuid.NewString()
	insertBrowserThread(t, d, threadID, sessionID, "ticket", time.Now().Unix())
	tmp := shortBrowserTempDir(t)
	t.Setenv("TMPDIR", tmp)
	dir := filepath.Join(tmp, "pi-rpc-sockets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, sessionID+".live.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadString('\n')
	}()

	srv := httptest.NewServer(d.tokenAuth("secret", d.routes()))
	defer srv.Close()
	body := []byte(`{"thread_id":"` + threadID + `","route":"rpc","protocol":"live-v1"}`)
	resp, err := http.Post(srv.URL+"/v1/ws-ticket?token=secret", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token minted ticket: %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/ws-ticket", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("mint status %d: %s", resp.StatusCode, b)
	}
	var ticket api.WSTicketResponse
	if err := json.NewDecoder(resp.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + ticket.Path
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "done")
	_, reuseResp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("single-use ticket was reusable")
	}
	if reuseResp == nil || reuseResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reuse response = %#v, err=%v", reuseResp, err)
	}
	b, _ := io.ReadAll(reuseResp.Body)
	reuseResp.Body.Close()
	if bytes.Contains(b, []byte(ticket.Ticket)) {
		t.Fatal("error body echoed ticket credential")
	}
}

func TestBrowserHubPaginationAndRemoteCanonicalAction(t *testing.T) {
	peer := newBrowserTestDaemon(t, "peer", "")
	remoteID, remoteSession := uuid.NewString(), uuid.NewString()
	remote := insertBrowserThread(t, peer, remoteID, remoteSession, "remote-old", 20)
	peerSrv := httptest.NewServer(peer.tokenAuth("peer-token", peer.routes()))
	defer peerSrv.Close()
	peerURL, _ := url.Parse(peerSrv.URL)

	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	localID := uuid.NewString()
	insertBrowserThread(t, hub, localID, uuid.NewString(), "local", 10)
	reg := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "peer-token"}}}
	if err := reg.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	if err := hub.view.replaceAll("peer", time.Now().Unix(), []api.ThreadSnapshot{{Thread: remote, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}

	list := func(raw string) api.BrowserThreadPage {
		rec := httptest.NewRecorder()
		hub.handleBrowserThreads(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads?archived=include&"+raw, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
		}
		var page api.BrowserThreadPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := list("limit=1")
	if len(first.Threads) != 1 || first.Threads[0].ID != remoteID || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second := list("limit=1&cursor=" + url.QueryEscape(first.NextCursor))
	if len(second.Threads) != 1 || second.Threads[0].ID != localID {
		t.Fatalf("second page = %#v", second)
	}

	action := []byte(`{"thread_id":"` + remoteID + `","action":"rename","name":"remote-new"}`)
	rec := httptest.NewRecorder()
	hub.handleBrowserThreadAction(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/threads/action", bytes.NewReader(action)))
	if rec.Code != http.StatusOK {
		t.Fatalf("remote action status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := peer.store.GetThread(remoteID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "remote-new" {
		t.Fatalf("remote canonical rename did not land: %#v", got)
	}

	hub.view.markUnreachable("peer")
	rec = httptest.NewRecorder()
	hub.handleBrowserThreadAction(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/threads/action", bytes.NewReader(action)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline peer action status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBrowserTerminalAvailabilityUsesFreshHeadfulState(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "[browser_hub]\nenabled = true\ntrusted_peers = [\"remote-fresh\", \"remote-stale\", \"remote-busy\"]\n")
	now := time.Now().Unix()
	localID := uuid.NewString()
	local := insertBrowserThread(t, d, localID, uuid.NewString(), "local-headful", now)
	d.maint.mu.Lock()
	d.maint.st[localID] = &liveState{snap: api.ThreadSnapshot{Thread: local, Head: api.Headful, Busy: api.BusyIdle}}
	d.maint.mu.Unlock()

	freshID := uuid.NewString()
	fresh := api.ThreadSnapshot{Thread: api.Thread{ID: freshID, Machine: "remote-fresh", AgentKind: "pi", Name: "fresh"}, Head: api.Headful, Busy: api.BusyBusy}
	if err := d.view.replaceAll("remote-fresh", now, []api.ThreadSnapshot{fresh}); err != nil {
		t.Fatal(err)
	}
	staleID := uuid.NewString()
	stale := api.ThreadSnapshot{Thread: api.Thread{ID: staleID, Machine: "remote-stale", AgentKind: "pi", Name: "stale"}, Head: api.Headful, Busy: api.BusyIdle}
	if err := d.view.replaceAll("remote-stale", now-3600, []api.ThreadSnapshot{stale}); err != nil {
		t.Fatal(err)
	}
	busyID := uuid.NewString()
	busy := api.ThreadSnapshot{Thread: api.Thread{ID: busyID, Machine: "remote-busy", AgentKind: "pi", Name: "headless-busy"}, Head: api.Headless, Busy: api.BusyBusy}
	if err := d.view.replaceAll("remote-busy", now, []api.ThreadSnapshot{busy}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	d.handleBrowserThreads(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads?archived=include", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("browser thread list status %d: %s", rec.Code, rec.Body.String())
	}
	var page api.BrowserThreadPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"terminal_available":true`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"terminal_available":false`)) {
		t.Fatalf("browser thread JSON omitted explicit terminal availability: %s", rec.Body.String())
	}
	byID := make(map[string]api.BrowserThreadSummary, len(page.Threads))
	for _, row := range page.Threads {
		byID[row.ID] = row
	}
	if row := byID[localID]; !row.TerminalAvailable || !row.RuntimeAvailable {
		t.Fatalf("local headful row = %#v", row)
	}
	if row := byID[freshID]; !row.TerminalAvailable || !row.RuntimeAvailable {
		t.Fatalf("fresh remote headful row = %#v", row)
	}
	if row := byID[staleID]; row.TerminalAvailable || row.RuntimeAvailable || row.State != "stale" {
		t.Fatalf("stale remote row = %#v", row)
	}
	if row := byID[busyID]; row.TerminalAvailable || !row.RuntimeAvailable || row.State != "running" {
		t.Fatalf("busy headless row = %#v", row)
	}
}

func TestBrowserTranscriptIsBoundedForLocalAndRemoteStoppedThreads(t *testing.T) {
	claudeHome := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	peer := newBrowserTestDaemon(t, "peer", "")
	id, sessionID := uuid.NewString(), uuid.NewString()
	cwd := t.TempDir()
	thread := api.Thread{ID: id, Machine: "peer", SessionName: "headless-" + id, Cwd: cwd, AgentKind: "claude", Name: "stopped", Tags: []string{}, CreatedAtUnix: time.Now().Unix(), AgentSessionID: sessionID}
	if err := peer.store.InsertThread(thread); err != nil {
		t.Fatal(err)
	}
	path := claude.TranscriptPath(claudeHome, cwd, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, browserDefaultTranscriptTail+101)
	for i := 0; i < browserDefaultTranscriptTail+100; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"content":"line-%04d"}}`, i))
	}
	lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"final reply"}]}}`)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	decode := func(rec *httptest.ResponseRecorder) api.BrowserTranscriptResponse {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("transcript status %d: %s", rec.Code, rec.Body.String())
		}
		var response api.BrowserTranscriptResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	localRec := httptest.NewRecorder()
	peer.handleBrowserThreadTranscript(localRec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/transcript?id="+id, nil))
	local := decode(localRec)
	if len(local.Lines) != browserDefaultTranscriptTail || local.Coverage.RequestedTail != browserDefaultTranscriptTail || local.Coverage.StartLine != 101 || local.Coverage.EndLine != 1101 || local.Coverage.TotalLines != 1101 || local.Coverage.Complete || local.Coverage.ByteLimited {
		t.Fatalf("default transcript coverage = %#v, lines=%d", local.Coverage, len(local.Lines))
	}
	if local.Format != "native-jsonl" || local.Coverage.Source != "resolved-current-session-file" || local.LastReply != "final reply" || local.ReplyCount != 1 {
		t.Fatalf("default transcript response = %#v", local)
	}

	peerSrv := httptest.NewServer(peer.tokenAuth("peer-token", peer.routes()))
	defer peerSrv.Close()
	peerURL, _ := url.Parse(peerSrv.URL)
	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	reg := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "peer-token"}}}
	if err := reg.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	if err := hub.view.replaceAll("peer", time.Now().Unix(), []api.ThreadSnapshot{{Thread: thread, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}
	remoteRec := httptest.NewRecorder()
	hub.handleBrowserThreadTranscript(remoteRec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/transcript?id="+id+"&tail=2", nil))
	remote := decode(remoteRec)
	if len(remote.Lines) != 2 || remote.Coverage.RequestedTail != 2 || remote.Coverage.StartLine != 1099 || remote.Coverage.EndLine != 1101 || remote.Coverage.Complete || remote.Coverage.ByteLimited {
		t.Fatalf("remote stopped transcript coverage = %#v, lines=%d", remote.Coverage, len(remote.Lines))
	}
	if remote.Lines[1] != lines[len(lines)-1] {
		t.Fatalf("remote stopped transcript tail = %#v", remote.Lines)
	}

	badRec := httptest.NewRecorder()
	peer.handleBrowserThreadTranscript(badRec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/transcript?id="+id+"&tail=10001", nil))
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("oversized transcript tail status = %d: %s", badRec.Code, badRec.Body.String())
	}
}
