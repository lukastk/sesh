package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents/pi"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

func installFakeStoredHistoryPi(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	script := `#!/bin/sh
: > "$FAKE_PI_ARGV"
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$FAKE_PI_ARGV"
done
if [ -n "$FAKE_PI_STDERR" ]; then
  printf '%s\n' "$FAKE_PI_STDERR" >&2
fi
if [ -n "$FAKE_PI_RESPONSE" ]; then
  printf '%s\n' "$FAKE_PI_RESPONSE"
fi
exit "${FAKE_PI_EXIT:-0}"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	argvLog := filepath.Join(dir, "argv")
	t.Setenv("FAKE_PI_ARGV", argvLog)
	return argvLog
}

func insertPiStoredHistoryThread(t *testing.T, d *Daemon, piHome, cwd string) (api.Thread, string) {
	t.Helper()
	id, sessionID := uuid.NewString(), uuid.NewString()
	thread := api.Thread{
		ID: id, Machine: d.cfg.Machine, SessionName: "headless-" + id, Cwd: cwd,
		AgentKind: "pi", Name: "stored-history", Tags: []string{}, CreatedAtUnix: time.Now().Unix(), AgentSessionID: sessionID,
	}
	if err := d.store.InsertThread(thread); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(pi.SessionsRoot(piHome), pi.CwdDirName(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-10-30T00-00-00_"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return thread, path
}

func nativeStoredHistorySuccess(t *testing.T, sessionID string) string {
	t.Helper()
	body := map[string]any{
		"protocol": "pi.session-history", "version": 1, "capability": "stored_current_branch", "status": "ok",
		"session": map[string]any{"id": sessionID, "cwd": "/owner/cwd", "leafId": nil},
		"page": map[string]any{
			"scope": "current_branch", "items": []any{map[string]any{"id": "entry-1", "type": "message"}},
			"coverage": map[string]any{"complete": true}, "nextCursor": "next-native-cursor",
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func decodeStoredHistoryResponse(t *testing.T, rec *httptest.ResponseRecorder) api.BrowserStoredHistoryResponse {
	t.Helper()
	var response api.BrowserStoredHistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode stored-history response: %v: %s", err, rec.Body.String())
	}
	return response
}

func TestBrowserStoredHistoryUsesOwnerResolvedPathAndExactArgv(t *testing.T) {
	argvLog := installFakeStoredHistoryPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	cwd := filepath.Join(t.TempDir(), "work $(not-shell)")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	thread, sessionPath := insertPiStoredHistoryThread(t, d, piHome, cwd)
	t.Setenv("FAKE_PI_RESPONSE", nativeStoredHistorySuccess(t, thread.AgentSessionID))
	t.Setenv("FAKE_PI_EXIT", "0")
	t.Setenv("FAKE_PI_STDERR", "")

	cursor := "eyJ2IjoxLCJiZWZvcmUiOjEyM30"
	rec := httptest.NewRecorder()
	requestPath := "/v1/browser/threads/history?id=" + thread.ID + "&cursor=" + url.QueryEscape(cursor) + "&max_items=17&max_bytes=4096"
	d.handleBrowserStoredHistory(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stored history status %d: %s", rec.Code, rec.Body.String())
	}
	response := decodeStoredHistoryResponse(t, rec)
	if response.Status != "ok" || response.Protocol != "pi.session-history" || response.Version != 1 || response.Capability != "stored_current_branch" || response.ThreadID != thread.ID || response.OwnerMachine != "self" || response.Session == nil || response.Session.ID != thread.AgentSessionID {
		t.Fatalf("stored history response = %#v", response)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(sessionPath)) {
		t.Fatal("browser response exposed the owner session path")
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n")
	wantArgs := []string{
		"--session-history-json", sessionPath,
		"--session-history-cursor", cursor,
		"--session-history-max-items", "17",
		"--session-history-max-bytes", "4096",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Pi metadata argv = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestBrowserStoredHistoryRejectsMismatchNativeFailureAndOlderPi(t *testing.T) {
	installFakeStoredHistoryPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		d.handleBrowserStoredHistory(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/history?id="+thread.ID, nil))
		return rec
	}

	t.Setenv("FAKE_PI_RESPONSE", nativeStoredHistorySuccess(t, uuid.NewString()))
	t.Setenv("FAKE_PI_EXIT", "0")
	t.Setenv("FAKE_PI_STDERR", "")
	rec := request()
	response := decodeStoredHistoryResponse(t, rec)
	if rec.Code != http.StatusBadGateway || response.Error == nil || response.Error.Code != "session_mismatch" || response.Session != nil || len(response.Page) != 0 {
		t.Fatalf("session mismatch response = %d %#v", rec.Code, response)
	}

	nativeFailure := `{"protocol":"pi.session-history","version":1,"capability":"stored_current_branch","status":"error","error":{"code":"stale_cursor","message":"cursor belongs to another leaf"}}`
	t.Setenv("FAKE_PI_RESPONSE", nativeFailure)
	t.Setenv("FAKE_PI_EXIT", "1")
	rec = request()
	response = decodeStoredHistoryResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "stale_cursor" || response.Error.Message != "cursor belongs to another leaf" {
		t.Fatalf("native failure response = %d %#v", rec.Code, response)
	}

	malformedSuccess := `{"protocol":"pi.session-history","version":1,"capability":"stored_current_branch","status":"ok","session":{"id":"` + thread.AgentSessionID + `","cwd":"/owner/cwd"},"page":{"scope":"current_branch","items":[],"coverage":{}}}`
	t.Setenv("FAKE_PI_RESPONSE", malformedSuccess)
	t.Setenv("FAKE_PI_EXIT", "0")
	t.Setenv("FAKE_PI_STDERR", "")
	rec = request()
	response = decodeStoredHistoryResponse(t, rec)
	if rec.Code != http.StatusBadGateway || response.Error == nil || response.Error.Code != "invalid_native_response" {
		t.Fatalf("malformed native success response = %d %#v", rec.Code, response)
	}

	t.Setenv("FAKE_PI_RESPONSE", "")
	t.Setenv("FAKE_PI_EXIT", "2")
	t.Setenv("FAKE_PI_STDERR", "unknown option --session-history-json")
	rec = request()
	response = decodeStoredHistoryResponse(t, rec)
	if rec.Code != http.StatusNotImplemented || response.Error == nil || response.Error.Code != "unsupported_native_capability" || strings.Contains(rec.Body.String(), "unknown option") {
		t.Fatalf("older Pi response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
}

func TestBrowserStoredHistoryRoutesThroughTrustedHTTPPeer(t *testing.T) {
	installFakeStoredHistoryPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	peer := newBrowserTestDaemon(t, "peer", "")
	thread, _ := insertPiStoredHistoryThread(t, peer, piHome, t.TempDir())
	t.Setenv("FAKE_PI_RESPONSE", nativeStoredHistorySuccess(t, thread.AgentSessionID))
	t.Setenv("FAKE_PI_EXIT", "0")
	t.Setenv("FAKE_PI_STDERR", "")
	peerSrv := httptest.NewServer(peer.tokenAuth("peer-secret", peer.routes()))
	defer peerSrv.Close()
	peerURL, _ := url.Parse(peerSrv.URL)

	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	registry := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "peer-secret"}}}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	if err := hub.view.replaceAll("peer", time.Now().Unix(), []api.ThreadSnapshot{{Thread: thread, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	hub.handleBrowserStoredHistory(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/history?id="+thread.ID+"&max_items=3", nil))
	response := decodeStoredHistoryResponse(t, rec)
	if rec.Code != http.StatusOK || response.Status != "ok" || response.OwnerMachine != "peer" || response.Session == nil || response.Session.ID != thread.AgentSessionID {
		t.Fatalf("remote stored history response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "peer-secret") {
		t.Fatal("remote stored-history response exposed peer token")
	}

	registry.Peers["peer"] = peers.Peer{Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "wrong-secret"}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	hub.handleBrowserStoredHistory(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/history?id="+thread.ID, nil))
	response = decodeStoredHistoryResponse(t, rec)
	if rec.Code != http.StatusBadGateway || response.Error == nil || response.Error.Code != "owner_routing_failed" || strings.Contains(rec.Body.String(), "wrong-secret") {
		t.Fatalf("remote auth failure response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
}

func TestStoredHistoryBoundsQueryTimeoutAndOutput(t *testing.T) {
	threadID := uuid.NewString()
	bad := []string{
		"?id=" + threadID + "&path=/tmp/evil",
		"?id=" + threadID + "&id=" + threadID,
		"?id=" + threadID + "&max_items=501",
		"?id=" + threadID + "&max_bytes=1",
		"?id=" + threadID + "&cursor=" + strings.Repeat("a", storedHistoryMaxCursorBytes+1),
	}
	for _, query := range bad {
		if _, err := decodeStoredHistoryQuery(httptest.NewRequest(http.MethodGet, "/v1/browser/threads/history"+query, nil)); err == nil {
			t.Fatalf("invalid stored-history query accepted: %s", query)
		}
	}

	var bounded cappedBuffer
	bounded.limit = 5
	_, _ = bounded.Write([]byte("123456789"))
	if !bounded.exceeded || string(bounded.Bytes()) != "12345" {
		t.Fatalf("bounded process output = exceeded:%v bytes:%q", bounded.exceeded, bounded.Bytes())
	}

	installFakeStoredHistoryPi(t)
	t.Setenv("FAKE_PI_RESPONSE", nativeStoredHistorySuccess(t, uuid.NewString()))
	t.Setenv("FAKE_PI_EXIT", "0")
	t.Setenv("FAKE_PI_STDERR", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	_, code, _ := runPiStoredHistory(ctx, "pi", "/owner/session.jsonl", storedHistoryQuery{})
	if code != "native_timeout" {
		t.Fatalf("cancelled native read code = %q", code)
	}
}
