package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
)

func installFakeStoredInputPi(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	script := `#!/bin/sh
{
  printf '%s\n' '---'
  for arg in "$@"; do printf '%s\n' "$arg"; done
} >> "$FAKE_INPUT_ARGV"
case "$1" in
  --session-presentation-json)
    is_v2=
    for arg in "$@"; do
      [ "$arg" = "--session-presentation-version" ] && is_v2=1
    done
    [ -n "$FAKE_INPUT_SLEEP" ] && sleep "$FAKE_INPUT_SLEEP"
    if [ -n "$is_v2" ]; then
      [ -n "$FAKE_MEDIA_PRESENTATION_STDERR" ] && printf '%s\n' "$FAKE_MEDIA_PRESENTATION_STDERR" >&2
      [ -n "$FAKE_MEDIA_PRESENTATION" ] && printf '%s\n' "$FAKE_MEDIA_PRESENTATION"
      exit "${FAKE_MEDIA_PRESENTATION_EXIT:-0}"
    fi
    [ -n "$FAKE_PRESENTATION_STDERR" ] && printf '%s\n' "$FAKE_PRESENTATION_STDERR" >&2
    [ -n "$FAKE_PRESENTATION" ] && printf '%s\n' "$FAKE_PRESENTATION"
    exit "${FAKE_PRESENTATION_EXIT:-0}"
    ;;
  --session-input-images-json)
    [ -n "$FAKE_INPUT_SLEEP" ] && sleep "$FAKE_INPUT_SLEEP"
    [ -n "$FAKE_INPUT_METADATA_STDERR" ] && printf '%s\n' "$FAKE_INPUT_METADATA_STDERR" >&2
    [ -n "$FAKE_INPUT_METADATA" ] && printf '%s\n' "$FAKE_INPUT_METADATA"
    exit "${FAKE_INPUT_METADATA_EXIT:-0}"
    ;;
  --session-input-image-bytes)
    [ -n "$FAKE_INPUT_SLEEP" ] && sleep "$FAKE_INPUT_SLEEP"
    [ -n "$FAKE_INPUT_BYTES" ] && printf '%s' "$FAKE_INPUT_BYTES"
    [ -n "$FAKE_INPUT_BYTES_STDERR" ] && printf '%s\n' "$FAKE_INPUT_BYTES_STDERR" >&2
    exit "${FAKE_INPUT_BYTES_EXIT:-0}"
    ;;
esac
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	argvLog := filepath.Join(dir, "argv")
	t.Setenv("FAKE_INPUT_ARGV", argvLog)
	return argvLog
}

func nativeStoredPresentationSuccess(t *testing.T, sessionID string) string {
	t.Helper()
	body := map[string]any{
		"protocol": "pi.session-presentation", "version": 1,
		"capability": "stored_current_branch_input_image_refs", "status": "ok",
		"session": map[string]any{"id": sessionID, "cwd": "/owner/cwd", "leafId": "entry-1"},
		"page": map[string]any{
			"scope": "current_branch_presentation",
			"items": []any{map[string]any{
				"itemId": "entry-1", "entryId": "entry-1", "parentEntryId": nil,
				"timestamp": "2026-10-29T00:00:00.000Z", "kind": "message",
				"message": map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "input_image_reference", "inputImageId": uuid.NewString(), "mediaType": "image/png", "byteLength": 5, "availability": map[string]any{"status": "available"}},
				}},
			}},
			"coverage": map[string]any{
				"scope": "current_branch_presentation", "leafId": "entry-1", "startIndex": 0,
				"endIndexExclusive": 1, "totalItems": 1, "byteLength": 300,
				"hasOlder": false, "hasNewer": false,
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func nativeStoredMediaPresentationSuccess(t *testing.T, sessionID string) string {
	t.Helper()
	body := map[string]any{
		"protocol": "pi.session-presentation", "version": 2,
		"capability": "stored_current_branch_media_refs", "status": "ok",
		"session": map[string]any{"id": sessionID, "cwd": "/owner/cwd", "leafId": "entry-2"},
		"page": map[string]any{
			"scope": "current_branch_presentation",
			"items": []any{map[string]any{
				"itemId": "entry-2", "entryId": "entry-2", "parentEntryId": nil,
				"timestamp": "2026-10-30T00:00:00.000Z", "kind": "message",
				"message": map[string]any{"role": "toolResult", "content": []any{
					map[string]any{"type": "output_artifact_reference", "kind": "image", "state": "resolved", "artifact": map[string]any{"publicationId": uuid.NewString(), "kind": "image"}},
				}},
			}},
			"coverage": map[string]any{
				"scope": "current_branch_presentation", "leafId": "entry-2", "startIndex": 0,
				"endIndexExclusive": 1, "totalItems": 1, "byteLength": 320,
				"hasOlder": false, "hasNewer": false,
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func nativeStoredInputMetadataSuccess(t *testing.T, sessionID, inputImageID, mediaType, bytesValue string, canonical bool) string {
	t.Helper()
	sum := sha256.Sum256([]byte(bytesValue))
	projection := map[string]any{
		"inputImageId": inputImageID, "mediaType": mediaType, "byteLength": len(bytesValue),
		"source": map[string]any{"type": "user_message_image", "entryId": "entry-1", "contentIndex": 0},
		"origin": "legacy_user_image", "availability": map[string]any{"status": "available"},
	}
	if canonical {
		projection["origin"] = "native"
		projection["sha256"] = hex.EncodeToString(sum[:])
	}
	body := map[string]any{
		"protocol": "pi.session-input-images", "version": 1,
		"capability": "stored_user_input_image_metadata", "status": "ok",
		"session": map[string]any{"id": sessionID, "cwd": "/owner/cwd", "leafId": "entry-1"},
		"page": map[string]any{
			"scope": "current_branch_input_images", "inputImages": []any{projection},
			"coverage": map[string]any{
				"scope": "current_branch_input_images", "leafId": "entry-1", "startIndex": 0,
				"endIndexExclusive": 1, "totalInputImages": 1, "byteLength": 256,
				"hasOlder": false, "hasNewer": false,
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func decodeStoredInputResponse(t *testing.T, rec *httptest.ResponseRecorder) api.BrowserStoredInputMediaResponse {
	t.Helper()
	var response api.BrowserStoredInputMediaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode stored input-media response: %v: %s", err, rec.Body.String())
	}
	return response
}

func TestBrowserStoredInputPresentationAndMetadataUseExactAuthority(t *testing.T) {
	argvLog := installFakeStoredInputPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	cwd := filepath.Join(t.TempDir(), "work $(not-shell)")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	thread, sessionPath := insertPiStoredHistoryThread(t, d, piHome, cwd)
	before, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("FAKE_PRESENTATION", nativeStoredPresentationSuccess(t, thread.AgentSessionID))
	presentationCursor := "eyJ2IjoxLCJiZWZvcmUiOjF9"
	rec := httptest.NewRecorder()
	requestPath := "/v1/browser/threads/presentation?id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&cursor=" + url.QueryEscape(presentationCursor) + "&max_items=17&max_bytes=4096"
	d.handleBrowserStoredPresentation(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusOK || response.Status != "ok" || response.Protocol != "pi.session-presentation" || response.NativeSessionID != thread.AgentSessionID || response.Session == nil || response.Session.ID != thread.AgentSessionID || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("presentation response = %d %#v headers=%#v", rec.Code, response, rec.Header())
	}
	if strings.Contains(rec.Body.String(), sessionPath) || strings.Contains(rec.Body.String(), cwd) {
		t.Fatal("presentation response exposed owner paths")
	}

	t.Setenv("FAKE_MEDIA_PRESENTATION", nativeStoredMediaPresentationSuccess(t, thread.AgentSessionID))
	mediaCursor := "eyJ2IjoyLCJiZWZvcmUiOjF9"
	rec = httptest.NewRecorder()
	requestPath = "/v1/browser/threads/media-presentation?id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&cursor=" + url.QueryEscape(mediaCursor) + "&max_items=19&max_bytes=8192"
	d.handleBrowserStoredMediaPresentation(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusOK || response.Status != "ok" || response.Protocol != "pi.session-presentation" || response.Version != 2 || response.Capability != "stored_current_branch_media_refs" || response.NativeSessionID != thread.AgentSessionID || response.Session == nil || response.Session.ID != thread.AgentSessionID || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("media presentation response = %d %#v headers=%#v", rec.Code, response, rec.Header())
	}
	if strings.Contains(rec.Body.String(), sessionPath) || strings.Contains(rec.Body.String(), cwd) {
		t.Fatal("media presentation response exposed owner paths")
	}

	inputImageID := uuid.NewString()
	t.Setenv("FAKE_INPUT_METADATA", nativeStoredInputMetadataSuccess(t, thread.AgentSessionID, inputImageID, "image/png", "abcde", true))
	rec = httptest.NewRecorder()
	requestPath = "/v1/browser/threads/input-images?id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&max_items=11&max_bytes=2048"
	d.handleBrowserStoredInputImages(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusOK || response.Status != "ok" || response.Protocol != "pi.session-input-images" || response.NativeSessionID != thread.AgentSessionID {
		t.Fatalf("metadata response = %d %#v", rec.Code, response)
	}
	if strings.Contains(rec.Body.String(), sessionPath) || strings.Contains(rec.Body.String(), cwd) {
		t.Fatal("metadata response exposed owner paths")
	}

	after, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stopped input-media reads mutated the session file")
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logged)
	for _, exact := range []string{
		"--session-presentation-json\n" + sessionPath + "\n--session-presentation-cursor\n" + presentationCursor + "\n--session-presentation-max-items\n17\n--session-presentation-max-bytes\n4096\n",
		"--session-presentation-json\n" + sessionPath + "\n--session-presentation-version\n2\n--session-presentation-cursor\n" + mediaCursor + "\n--session-presentation-max-items\n19\n--session-presentation-max-bytes\n8192\n",
		"--session-input-images-json\n" + sessionPath + "\n--session-input-image-max-items\n11\n--session-input-image-max-bytes\n2048\n",
	} {
		if !strings.Contains(logText, exact) {
			t.Fatalf("argv log missing exact sequence %q in %q", exact, logText)
		}
	}
}

func TestBrowserStoredInputBytesVerifyDigestHEADRangeAndNoPartialSuccess(t *testing.T) {
	argvLog := installFakeStoredInputPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "[browser_api]\nallowed_origins = [\"app://obsidian.md\"]\n")
	thread, sessionPath := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	inputImageID := uuid.NewString()
	body := "verified-image-bytes"
	t.Setenv("FAKE_INPUT_METADATA", nativeStoredInputMetadataSuccess(t, thread.AgentSessionID, inputImageID, "image/png", body, true))
	t.Setenv("FAKE_INPUT_BYTES", body)
	requestPath := "/v1/browser/input-images?thread_id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&input_image_id=" + inputImageID

	rec := httptest.NewRecorder()
	d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	if rec.Code != http.StatusOK || rec.Body.String() != body || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) || rec.Header().Get("ETag") != `"sha256-`+digest+`"` || rec.Header().Get("X-Sesh-Input-Image-SHA256") != digest || rec.Header().Get("X-Sesh-Input-Image-Digest-Status") != "canonical-match" || rec.Header().Get("Accept-Ranges") != "none" || rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET bytes = %d %q headers=%#v", rec.Code, rec.Body.String(), rec.Header())
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	exactBytesArgv := strings.Join([]string{
		"--session-input-image-bytes", sessionPath,
		"--input-image-id", inputImageID,
		"--input-image-max-bytes", "33554432",
		"--input-image-chunk-bytes", "262144",
		"--input-image-timeout-ms", "10000",
	}, "\n") + "\n"
	if !strings.Contains(string(logged), exactBytesArgv) {
		t.Fatalf("byte argv log missing exact sequence %q in %q", exactBytesArgv, logged)
	}

	legacyID := strings.Repeat("a", 64)
	t.Setenv("FAKE_INPUT_METADATA", nativeStoredInputMetadataSuccess(t, thread.AgentSessionID, legacyID, "image/png", body, false))
	legacyPath := "/v1/browser/input-images?thread_id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&input_image_id=" + legacyID
	rec = httptest.NewRecorder()
	d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodHead, legacyPath, nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) || rec.Header().Get("X-Sesh-Input-Image-Digest-Status") != "computed-only" {
		t.Fatalf("HEAD legacy bytes = %d body=%q headers=%#v", rec.Code, rec.Body.String(), rec.Header())
	}

	actual := httptest.NewRequest(http.MethodGet, legacyPath, nil)
	actual.Header.Set("Origin", "app://obsidian.md")
	actual.Header.Set("Authorization", "Bearer browser-secret")
	rec = httptest.NewRecorder()
	d.browserCORS(d.tokenAuth("browser-secret", d.routes())).ServeHTTP(rec, actual)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Access-Control-Expose-Headers") != browserInputImageExposeHeaders || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("authenticated input-image CORS GET = %d headers=%#v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	ranged := httptest.NewRequest(http.MethodGet, requestPath, nil)
	ranged.Header.Set("Range", "bytes=0-1")
	rec = httptest.NewRecorder()
	d.handleBrowserStoredInputImageBytes(rec, ranged)
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable || response.Error == nil || response.Error.Code != "range_not_supported" || rec.Header().Get("Accept-Ranges") != "none" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("range response = %d %#v headers=%#v", rec.Code, response, rec.Header())
	}

	t.Setenv("FAKE_INPUT_METADATA", nativeStoredInputMetadataSuccess(t, thread.AgentSessionID, inputImageID, "image/png", "partial-secret", true))
	t.Setenv("FAKE_INPUT_BYTES", "partial-secret")
	t.Setenv("FAKE_INPUT_BYTES_EXIT", "1")
	t.Setenv("FAKE_INPUT_BYTES_STDERR", `{"protocol":"pi.session-input-image-bytes","version":1,"capability":"stored_user_input_image_bytes","status":"error","error":{"code":"input_image_unavailable","message":"/owner/private/session.jsonl"}}`)
	rec = httptest.NewRecorder()
	d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "input_image_unavailable" || strings.Contains(rec.Body.String(), "partial-secret") || strings.Contains(rec.Body.String(), "/owner/private") || rec.Header().Get("Content-Type") == "image/png" {
		t.Fatalf("partial failure leaked = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
}

func TestBrowserStoredInputRejectsReassociationAndOwnershipConflict(t *testing.T) {
	installFakeStoredInputPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	oldSession := thread.AgentSessionID
	if err := d.store.SetThreadAgentSession(thread.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.handleBrowserStoredPresentation(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/presentation?id="+thread.ID+"&native_session_id="+oldSession, nil))
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "stale_session" {
		t.Fatalf("reassociation response = %d %#v", rec.Code, response)
	}

	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer-a\", \"peer-b\"]\n")
	remote := thread
	remote.Machine = "peer-a"
	if err := hub.view.replaceAll("peer-a", time.Now().Unix(), []api.ThreadSnapshot{{Thread: remote, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}
	remote.Machine = "peer-b"
	if err := hub.view.replaceAll("peer-b", time.Now().Unix(), []api.ThreadSnapshot{{Thread: remote, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	hub.handleBrowserStoredInputImages(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/input-images?id="+thread.ID+"&native_session_id="+oldSession, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "owner_conflict" {
		t.Fatalf("ownership conflict response = %d %#v", rec.Code, response)
	}
}

func TestBrowserStoredMediaPresentationDistinguishesOldV1FromMalformedV2WithoutFallback(t *testing.T) {
	argvLog := installFakeStoredInputPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	requestPath := "/v1/browser/threads/media-presentation?id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID

	t.Setenv("FAKE_MEDIA_PRESENTATION", `{"protocol":"pi.session-presentation","version":1,"capability":"stored_current_branch_input_image_refs","status":"error","error":{"code":"invalid_arguments","message":"version 2 unsupported"}}`)
	t.Setenv("FAKE_MEDIA_PRESENTATION_STDERR", "unsupported presentation version")
	t.Setenv("FAKE_MEDIA_PRESENTATION_EXIT", "1")
	rec := httptest.NewRecorder()
	d.handleBrowserStoredMediaPresentation(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusNotImplemented || response.Version != 2 || response.Capability != "stored_current_branch_media_refs" || response.Error == nil || response.Error.Code != "unsupported_native_capability" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("old v1 response = %d %#v headers=%#v", rec.Code, response, rec.Header())
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(logged), "---\n") != 1 || !strings.Contains(string(logged), "--session-presentation-version\n2\n") {
		t.Fatalf("old v1 invocation retried or omitted fixed v2 selector: %q", logged)
	}

	if err := os.WriteFile(argvLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_MEDIA_PRESENTATION", `{"protocol":"pi.session-presentation","version":2,"capability":"stored_current_branch_media_refs","status":"error","error":{"code":"invalid_arguments","message":"malformed claimed v2"},"unexpected":true}`)
	t.Setenv("FAKE_MEDIA_PRESENTATION_STDERR", "")
	rec = httptest.NewRecorder()
	d.handleBrowserStoredMediaPresentation(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusBadGateway || response.Error == nil || response.Error.Code != "invalid_native_response" || strings.Contains(rec.Body.String(), "malformed claimed v2") {
		t.Fatalf("malformed claimed v2 response = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}
	logged, err = os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(logged), "---\n") != 1 || !strings.Contains(string(logged), "--session-presentation-version\n2\n") {
		t.Fatalf("malformed v2 invocation retried or omitted fixed selector: %q", logged)
	}

	if err := os.WriteFile(argvLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var leafMismatch map[string]any
	if err := json.Unmarshal([]byte(nativeStoredMediaPresentationSuccess(t, thread.AgentSessionID)), &leafMismatch); err != nil {
		t.Fatal(err)
	}
	page := leafMismatch["page"].(map[string]any)
	page["coverage"].(map[string]any)["leafId"] = "different-leaf"
	encoded, err := json.Marshal(leafMismatch)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_MEDIA_PRESENTATION", string(encoded))
	t.Setenv("FAKE_MEDIA_PRESENTATION_EXIT", "0")
	rec = httptest.NewRecorder()
	d.handleBrowserStoredMediaPresentation(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusBadGateway || response.Error == nil || response.Error.Code != "invalid_native_response" {
		t.Fatalf("leaf-mismatched claimed v2 response = %d %#v", rec.Code, response)
	}
	logged, err = os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(logged), "---\n") != 1 || !strings.Contains(string(logged), "--session-presentation-version\n2\n") {
		t.Fatalf("leaf-mismatched v2 invocation retried or omitted fixed selector: %q", logged)
	}
}

func TestBrowserStoredMediaPresentationRefusesActiveRuntimeWithoutNativeInvocation(t *testing.T) {
	argvLog := installFakeStoredInputPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, piHome, t.TempDir())
	setThreadRuntimeActive(t, d, thread.ID)
	t.Setenv("FAKE_MEDIA_PRESENTATION", nativeStoredMediaPresentationSuccess(t, thread.AgentSessionID))

	rec := httptest.NewRecorder()
	requestPath := "/v1/browser/threads/media-presentation?id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID
	d.handleBrowserStoredMediaPresentation(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "runtime_active" {
		t.Fatalf("active media presentation = %d %#v", rec.Code, response)
	}
	if _, err := os.Stat(argvLog); !os.IsNotExist(err) {
		t.Fatalf("active media presentation invoked stopped Pi: %v", err)
	}
}

func TestBrowserStoredMediaPresentationOlderPeerDoesNotFallback(t *testing.T) {
	requests := []string{}
	authorization := ""
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		authorization = r.Header.Get("Authorization")
		http.Redirect(w, r, "/v1/browser/threads/presentation", http.StatusTemporaryRedirect)
	}))
	defer peerSrv.Close()
	peerURL, err := url.Parse(peerSrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	registry := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "peer-secret"}}}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	thread := api.Thread{
		ID: uuid.NewString(), Machine: "peer", SessionName: "headless-old-peer", Cwd: "/work",
		AgentKind: "pi", Name: "old-peer", Tags: []string{}, CreatedAtUnix: time.Now().Unix(), AgentSessionID: uuid.NewString(),
	}
	if err := hub.view.replaceAll("peer", time.Now().Unix(), []api.ThreadSnapshot{{Thread: thread, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	requestPath := "/v1/browser/threads/media-presentation?id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID
	hub.handleBrowserStoredMediaPresentation(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusBadGateway || response.Version != 2 || response.Capability != "stored_current_branch_media_refs" || response.Error == nil || response.Error.Code != "owner_routing_failed" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("old peer response = %d %#v headers=%#v", rec.Code, response, rec.Header())
	}
	if authorization != "Bearer peer-secret" {
		t.Fatalf("peer Authorization = %q", authorization)
	}
	if !reflect.DeepEqual(requests, []string{"GET /v1/browser/threads/media-presentation"}) {
		t.Fatalf("old peer requests = %#v; Sesh must not probe version 1", requests)
	}
}

func TestBrowserStoredInputRoutesThroughTrustedPeerAndRevalidatesBytes(t *testing.T) {
	installFakeStoredInputPi(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	peer := newBrowserTestDaemon(t, "peer", "")
	thread, _ := insertPiStoredHistoryThread(t, peer, piHome, t.TempDir())
	inputImageID := uuid.NewString()
	body := "peer-image-bytes"
	t.Setenv("FAKE_PRESENTATION", nativeStoredPresentationSuccess(t, thread.AgentSessionID))
	t.Setenv("FAKE_INPUT_METADATA", nativeStoredInputMetadataSuccess(t, thread.AgentSessionID, inputImageID, "image/png", body, true))
	t.Setenv("FAKE_MEDIA_PRESENTATION", nativeStoredMediaPresentationSuccess(t, thread.AgentSessionID))
	t.Setenv("FAKE_INPUT_BYTES", body)
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
	hub.handleBrowserStoredPresentation(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/presentation?id="+thread.ID+"&native_session_id="+thread.AgentSessionID, nil))
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusOK || response.OwnerMachine != "peer" || response.NativeSessionID != thread.AgentSessionID || strings.Contains(rec.Body.String(), "peer-secret") {
		t.Fatalf("peer presentation = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	hub.handleBrowserStoredMediaPresentation(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/media-presentation?id="+thread.ID+"&native_session_id="+thread.AgentSessionID, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusOK || response.OwnerMachine != "peer" || response.Version != 2 || response.Capability != "stored_current_branch_media_refs" || response.NativeSessionID != thread.AgentSessionID || strings.Contains(rec.Body.String(), "peer-secret") {
		t.Fatalf("peer media presentation = %d %#v body=%s", rec.Code, response, rec.Body.String())
	}

	requestPath := "/v1/browser/input-images?thread_id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&input_image_id=" + inputImageID
	rec = httptest.NewRecorder()
	hub.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodHead, requestPath, nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) || rec.Header().Get("X-Sesh-Input-Image-Digest-Status") != "canonical-match" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("peer HEAD bytes = %d body=%q headers=%#v", rec.Code, rec.Body.String(), rec.Header())
	}

	t.Setenv("FAKE_INPUT_SLEEP", "0.2")
	changed := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := hub.view.replaceAll("peer", time.Now().Unix(), nil); err != nil {
			changed <- err
			return
		}
		moved := thread
		moved.Machine = "peer-2"
		changed <- hub.view.replaceAll("peer-2", time.Now().Unix(), []api.ThreadSnapshot{{Thread: moved, Head: api.Headless, Busy: api.BusyIdle}})
	}()
	rec = httptest.NewRecorder()
	hub.handleBrowserStoredPresentation(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/presentation?id="+thread.ID+"&native_session_id="+thread.AgentSessionID, nil))
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "stale_replacement" {
		t.Fatalf("stale peer ownership response = %d %#v", rec.Code, response)
	}
}

func TestStoredInputMediaCapabilityAndCORS(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "[browser_api]\nallowed_origins = [\"app://obsidian.md\"]\n")
	rec := httptest.NewRecorder()
	d.handleBrowserThreads(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads", nil))
	var page api.BrowserThreadPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page.Capabilities, []string{"browser.live-input-media-v1", "browser.stored-input-media-v1", "browser.stored-media-presentation-v2"}) {
		t.Fatalf("browser capabilities = %#v", page.Capabilities)
	}

	handler := d.browserCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	preflight := httptest.NewRequest(http.MethodOptions, "/v1/browser/input-images", nil)
	preflight.Header.Set("Origin", "app://obsidian.md")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflight.Header.Set("Access-Control-Request-Headers", "Authorization")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Access-Control-Allow-Headers") != "Authorization" {
		t.Fatalf("input-image CORS preflight = %d headers=%#v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	mediaPreflight := httptest.NewRequest(http.MethodOptions, "/v1/browser/threads/media-presentation", nil)
	mediaPreflight.Header.Set("Origin", "app://obsidian.md")
	mediaPreflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	mediaPreflight.Header.Set("Access-Control-Request-Headers", "Authorization")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, mediaPreflight)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Access-Control-Allow-Methods") != http.MethodGet || rec.Header().Get("Access-Control-Allow-Headers") != "Authorization" {
		t.Fatalf("media-presentation CORS preflight = %d headers=%#v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	actual := httptest.NewRequest(http.MethodGet, "/v1/browser/threads/media-presentation?id=invalid&native_session_id=invalid", nil)
	actual.Header.Set("Origin", "app://obsidian.md")
	actual.Header.Set("Authorization", "Bearer browser-secret")
	rec = httptest.NewRecorder()
	d.browserCORS(d.tokenAuth("browser-secret", d.routes())).ServeHTTP(rec, actual)
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusBadRequest || response.Version != 2 || response.Capability != "stored_current_branch_media_refs" || response.Error == nil || response.Error.Code != "invalid_request" || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("authenticated media-presentation CORS error = %d %#v headers=%#v", rec.Code, response, rec.Header())
	}

	head := httptest.NewRequest(http.MethodHead, "/v1/browser/threads/media-presentation?id=invalid&native_session_id=invalid", nil)
	head.Header.Set("Origin", "app://obsidian.md")
	head.Header.Set("Authorization", "Bearer browser-secret")
	rec = httptest.NewRecorder()
	d.browserCORS(d.tokenAuth("browser-secret", d.routes())).ServeHTTP(rec, head)
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusMethodNotAllowed || response.Version != 2 || response.Capability != "stored_current_branch_media_refs" || response.Error == nil || response.Error.Code != "invalid_request" || rec.Header().Get("Allow") != http.MethodGet || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("media-presentation HEAD refusal = %d %#v headers=%#v", rec.Code, response, rec.Header())
	}

	ranged := httptest.NewRequest(http.MethodOptions, "/v1/browser/input-images", nil)
	ranged.Header.Set("Origin", "app://obsidian.md")
	ranged.Header.Set("Access-Control-Request-Method", http.MethodGet)
	ranged.Header.Set("Access-Control-Request-Headers", "Authorization, Range")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, ranged)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Range CORS preflight status = %d, want 403", rec.Code)
	}
}
