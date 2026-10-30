package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

func installFakeStoredArtifactPi(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	script := `#!/bin/sh
: > "$FAKE_PI_ARGV"
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$FAKE_PI_ARGV"
done
if [ -n "$FAKE_PI_SLEEP" ]; then sleep "$FAKE_PI_SLEEP"; fi
if [ "$FAKE_PI_REPLACE_SESSION" = "1" ]; then
  printf '%s\n' '{"type":"session","id":"replacement"}' > "$2.replacement"
  mv "$2.replacement" "$2"
fi
case "$1" in
  --session-artifacts-json)
    if [ -n "$FAKE_PI_METADATA_STDERR" ]; then printf '%s\n' "$FAKE_PI_METADATA_STDERR" >&2; fi
    if [ -n "$FAKE_PI_METADATA" ]; then printf '%s\n' "$FAKE_PI_METADATA"; fi
    exit "${FAKE_PI_METADATA_EXIT:-0}"
    ;;
  --session-artifact-bytes)
    if [ -n "$FAKE_PI_BYTES" ]; then printf '%s' "$FAKE_PI_BYTES"; fi
    if [ -n "$FAKE_PI_BYTES_STDERR" ]; then printf '%s\n' "$FAKE_PI_BYTES_STDERR" >&2; fi
    exit "${FAKE_PI_BYTES_EXIT:-0}"
    ;;
  *)
    exit 2
    ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	argvLog := filepath.Join(dir, "argv")
	t.Setenv("FAKE_PI_ARGV", argvLog)
	t.Setenv("FAKE_PI_SLEEP", "")
	t.Setenv("FAKE_PI_REPLACE_SESSION", "")
	t.Setenv("FAKE_PI_METADATA", "")
	t.Setenv("FAKE_PI_METADATA_STDERR", "")
	t.Setenv("FAKE_PI_METADATA_EXIT", "0")
	t.Setenv("FAKE_PI_BYTES", "")
	t.Setenv("FAKE_PI_BYTES_STDERR", "")
	t.Setenv("FAKE_PI_BYTES_EXIT", "0")
	return argvLog
}

func nativeStoredArtifactsSuccess(t *testing.T, sessionID, publicationID string, includeSHA bool) string {
	t.Helper()
	artifact := map[string]any{
		"publicationId": publicationID,
		"kind":          "image",
		"displayName":   "image-1.png",
		"mediaType":     "image/png",
		"byteLength":    14,
		"disposition":   "inline",
		"retention":     "session_inline",
		"source": map[string]any{
			"type": "tool_result_image", "toolCallId": "tool-1", "contentIndex": 0, "entryId": "entry-1",
		},
		"origin":       "native",
		"availability": map[string]any{"status": "available"},
	}
	if includeSHA {
		sum := sha256.Sum256([]byte("artifact-bytes"))
		artifact["sha256"] = hex.EncodeToString(sum[:])
	} else {
		artifact["origin"] = "legacy_inline_image"
	}
	body := map[string]any{
		"protocol": "pi.session-artifacts", "version": 1, "capability": "stored_artifact_metadata", "status": "ok",
		"session": map[string]any{"id": sessionID, "cwd": "/owner/cwd", "leafId": nil},
		"page": map[string]any{
			"scope": "current_branch_artifacts", "artifacts": []any{artifact},
			"coverage": map[string]any{
				"scope": "current_branch_artifacts", "leafId": nil, "startIndex": 0, "endIndexExclusive": 1,
				"totalArtifacts": 1, "byteLength": 400, "hasOlder": false, "hasNewer": false,
			},
			"nextCursor": "eyJ2IjoxfQ",
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func decodeStoredArtifactsResponse(t *testing.T, rec *httptest.ResponseRecorder) api.BrowserStoredArtifactsResponse {
	t.Helper()
	var response api.BrowserStoredArtifactsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode stored-artifact response: %v: %s", err, rec.Body.String())
	}
	return response
}

func TestBrowserStoredArtifactsUsesOwnerPathExactArgvAndAllowsLegacyMissingSHA(t *testing.T) {
	argvLog := installFakeStoredArtifactPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	cwd := filepath.Join(t.TempDir(), "work $(not-shell)")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	thread, sessionPath := insertPiStoredHistoryThread(t, d, piHome, cwd)
	legacyID := strings.Repeat("a", 64)
	t.Setenv("FAKE_PI_METADATA", nativeStoredArtifactsSuccess(t, thread.AgentSessionID, legacyID, false))
	cursor := "eyJ2IjoxLCJiZWZvcmUiOjF9"
	rec := httptest.NewRecorder()
	requestPath := "/v1/browser/threads/artifacts?id=" + thread.ID + "&cursor=" + url.QueryEscape(cursor) + "&max_items=17&max_bytes=4096"
	d.handleBrowserStoredArtifacts(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response := decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusOK || response.Status != "ok" || response.Protocol != "pi.session-artifacts" || response.Session == nil || response.Session.ID != thread.AgentSessionID {
		t.Fatalf("stored artifact metadata = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(sessionPath)) {
		t.Fatal("browser metadata exposed owner session path")
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n")
	want := []string{
		"--session-artifacts-json", sessionPath,
		"--session-artifact-cursor", cursor,
		"--session-artifact-max-items", "17",
		"--session-artifact-max-bytes", "4096",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact metadata argv = %#v, want %#v", got, want)
	}
}

func TestBrowserStoredArtifactBytesBuffersValidatesAndUsesExactArgv(t *testing.T) {
	argvLog := installFakeStoredArtifactPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, sessionPath := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	publicationID := uuid.NewString()
	body := "artifact-bytes"
	t.Setenv("FAKE_PI_BYTES", body)
	rec := httptest.NewRecorder()
	d.handleBrowserStoredArtifactBytes(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifact-bytes?id="+thread.ID+"&publication_id="+publicationID, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Fatalf("stored artifact bytes = %d %q", rec.Code, rec.Body.String())
	}
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	if rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("Content-Length") != "14" || rec.Header().Get("ETag") != `"sha256-`+digest+`"` || rec.Header().Get("X-Sesh-Artifact-SHA256") != digest || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("stored artifact headers = %#v", rec.Header())
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n")
	want := []string{
		"--session-artifact-bytes", sessionPath,
		"--publication-id", publicationID,
		"--artifact-max-bytes", "33554432",
		"--artifact-chunk-bytes", "262144",
		"--artifact-timeout-ms", "10000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact bytes argv = %#v, want %#v", got, want)
	}
}

func TestStoredArtifactBytesDiscardsPartialStdoutOnFailureAndRejectsReplacement(t *testing.T) {
	installFakeStoredArtifactPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	publicationID := uuid.NewString()
	path := "/v1/browser/threads/artifact-bytes?id=" + thread.ID + "&publication_id=" + publicationID

	t.Setenv("FAKE_PI_BYTES", "partial-secret-bytes")
	t.Setenv("FAKE_PI_BYTES_EXIT", "1")
	t.Setenv("FAKE_PI_BYTES_STDERR", `{"protocol":"pi.session-artifact-bytes","version":1,"capability":"stored_inline_artifact_bytes","status":"error","error":{"code":"artifact_unavailable","message":"/owner/private/path"}}`)
	rec := httptest.NewRecorder()
	d.handleBrowserStoredArtifactBytes(rec, httptest.NewRequest(http.MethodGet, path, nil))
	response := decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "artifact_unavailable" || strings.Contains(rec.Body.String(), "partial-secret") || strings.Contains(rec.Body.String(), "/owner/private") {
		t.Fatalf("partial failure leaked = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}

	t.Setenv("FAKE_PI_BYTES_EXIT", "0")
	t.Setenv("FAKE_PI_BYTES_STDERR", "")
	t.Setenv("FAKE_PI_BYTES", "verified-before-replacement")
	t.Setenv("FAKE_PI_REPLACE_SESSION", "1")
	rec = httptest.NewRecorder()
	d.handleBrowserStoredArtifactBytes(rec, httptest.NewRequest(http.MethodGet, path, nil))
	response = decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "stale_replacement" || strings.Contains(rec.Body.String(), "verified-before-replacement") {
		t.Fatalf("replacement response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}

	t.Setenv("FAKE_PI_REPLACE_SESSION", "")
	t.Setenv("FAKE_PI_SLEEP", "0.2")
	replaced := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		replaced <- d.store.SetThreadAgentSession(thread.ID, uuid.NewString())
	}()
	rec = httptest.NewRecorder()
	d.handleBrowserStoredArtifactBytes(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	response = decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "stale_replacement" || rec.Header().Get("Content-Type") == "application/octet-stream" {
		t.Fatalf("session replacement response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
}

func TestStoredArtifactNativeProcessCancellationIsTimeout(t *testing.T) {
	installFakeStoredArtifactPi(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, code, _ := runPiStoredArtifacts(ctx, "/owner/session.jsonl", storedArtifactMetadataQuery{}); code != "native_timeout" {
		t.Fatalf("metadata cancellation code = %q", code)
	}
	if _, _, code, _ := runPiStoredArtifactBytes(ctx, "/owner/session.jsonl", uuid.NewString()); code != "native_timeout" {
		t.Fatalf("byte cancellation code = %q", code)
	}
}

func TestStoredArtifactsRejectRuntimeAndHostileQueriesAndBoundConcurrency(t *testing.T) {
	threadID := uuid.NewString()
	badMetadata := []string{
		"?id=" + threadID + "&path=/tmp/evil",
		"?id=" + threadID + "&id=" + threadID,
		"?id=" + threadID + "&max_items=501",
		"?id=" + threadID + "&max_bytes=524289",
		"?id=" + threadID + "&cursor=" + strings.Repeat("a", storedArtifactMaxCursorBytes+1),
	}
	for _, query := range badMetadata {
		if _, err := decodeStoredArtifactMetadataQuery(httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifacts"+query, nil)); err == nil {
			t.Fatalf("invalid metadata query accepted: %s", query)
		}
	}
	badBytes := []string{
		"?id=" + threadID + "&publication_id=../owner/path",
		"?id=" + threadID + "&publication_id=" + uuid.NewString() + "&path=x",
		"?id=" + threadID + "&publication_id=" + uuid.NewString() + "&publication_id=" + uuid.NewString(),
	}
	for _, query := range badBytes {
		if _, err := decodeStoredArtifactBytesQuery(httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifact-bytes"+query, nil)); err == nil {
			t.Fatalf("invalid byte query accepted: %s", query)
		}
	}
	rangeRequest := httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifact-bytes?id="+threadID+"&publication_id="+uuid.NewString(), nil)
	rangeRequest.Header.Set("Range", "bytes=0-1")
	if _, err := decodeStoredArtifactBytesQuery(rangeRequest); err == nil {
		t.Fatal("Range was accepted for whole-object verification")
	}

	slots := make(chan struct{}, 1)
	if !acquireStoredArtifactSlot(slots) || acquireStoredArtifactSlot(slots) {
		t.Fatal("artifact process slot did not fail closed at capacity")
	}
	releaseStoredArtifactSlot(slots)
	if !acquireStoredArtifactSlot(slots) {
		t.Fatal("artifact process slot was not released")
	}
	releaseStoredArtifactSlot(slots)

	installFakeStoredArtifactPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, sessionPath := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	d.hlMu.Lock()
	d.hlInFlight[thread.ID] = true
	d.hlMu.Unlock()
	rec := httptest.NewRecorder()
	d.handleBrowserStoredArtifacts(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifacts?id="+thread.ID, nil))
	response := decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "runtime_active" {
		t.Fatalf("active runtime response = %d %#v", rec.Code, response)
	}
	d.hlMu.Lock()
	delete(d.hlInFlight, thread.ID)
	d.hlMu.Unlock()
	target := filepath.Join(t.TempDir(), "target.jsonl")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sessionPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, sessionPath); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	d.handleBrowserStoredArtifacts(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifacts?id="+thread.ID, nil))
	response = decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusInternalServerError || response.Error == nil || response.Error.Code != "read_failed" || strings.Contains(rec.Body.String(), target) {
		t.Fatalf("symlink session response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
}

func TestStoredArtifactsRejectMalformedNativeAndOlderPiWithoutDetails(t *testing.T) {
	installFakeStoredArtifactPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	path := "/v1/browser/threads/artifacts?id=" + thread.ID

	t.Setenv("FAKE_PI_METADATA", `{"protocol":"pi.session-artifacts","version":1,"capability":"stored_artifact_metadata","status":"ok","session":{"id":"`+thread.AgentSessionID+`","cwd":"/owner"},"page":{"scope":"current_branch_artifacts","artifacts":[],"coverage":{}}}`)
	rec := httptest.NewRecorder()
	d.handleBrowserStoredArtifacts(rec, httptest.NewRequest(http.MethodGet, path, nil))
	response := decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusBadGateway || response.Error == nil || response.Error.Code != "invalid_native_response" {
		t.Fatalf("malformed native response = %d %#v", rec.Code, response)
	}

	t.Setenv("FAKE_PI_METADATA", "")
	t.Setenv("FAKE_PI_METADATA_EXIT", "2")
	t.Setenv("FAKE_PI_METADATA_STDERR", "unknown option --session-artifacts-json /private/session.jsonl")
	rec = httptest.NewRecorder()
	d.handleBrowserStoredArtifacts(rec, httptest.NewRequest(http.MethodGet, path, nil))
	response = decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusNotImplemented || response.Error == nil || response.Error.Code != "unsupported_native_capability" || strings.Contains(rec.Body.String(), "unknown option") || strings.Contains(rec.Body.String(), "/private") {
		t.Fatalf("older Pi response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
}

func TestStoredArtifactsRouteMetadataAndVerifiedBytesThroughTrustedPeer(t *testing.T) {
	installFakeStoredArtifactPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	peer := newBrowserTestDaemon(t, "peer", "")
	thread, _ := insertPiStoredHistoryThread(t, peer, piHome, t.TempDir())
	publicationID := uuid.NewString()
	t.Setenv("FAKE_PI_METADATA", nativeStoredArtifactsSuccess(t, thread.AgentSessionID, publicationID, true))
	t.Setenv("FAKE_PI_BYTES", "peer-artifact")
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
	hub.handleBrowserStoredArtifacts(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifacts?id="+thread.ID, nil))
	metadata := decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusOK || metadata.Status != "ok" || metadata.OwnerMachine != "peer" || metadata.Session == nil || metadata.Session.ID != thread.AgentSessionID {
		t.Fatalf("peer metadata = %d %#v body=%s", rec.Code, metadata, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	hub.handleBrowserStoredArtifactBytes(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifact-bytes?id="+thread.ID+"&publication_id="+publicationID, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "peer-artifact" || strings.Contains(rec.Body.String(), "peer-secret") {
		t.Fatalf("peer bytes = %d %q headers=%#v", rec.Code, rec.Body.String(), rec.Header())
	}

	registry.Peers["peer"] = peers.Peer{Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "wrong-secret"}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	hub.handleBrowserStoredArtifacts(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/artifacts?id="+thread.ID, nil))
	metadata = decodeStoredArtifactsResponse(t, rec)
	if rec.Code != http.StatusBadGateway || metadata.Error == nil || metadata.Error.Code != "owner_routing_failed" || strings.Contains(rec.Body.String(), "wrong-secret") {
		t.Fatalf("peer auth failure = %d %#v body=%s", rec.Code, metadata, rec.Body.String())
	}
}
