package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/store"
)

func mappingResolveRequest(thread api.Thread, target api.TransferTarget, publicationID string, data []byte) api.RuntimeArtifactMappingResolveRequest {
	digest := transferDigest(data)
	return api.RuntimeArtifactMappingResolveRequest{
		ThreadID: thread.ID, ConsumerTarget: target, NativeSession: thread.AgentSessionID,
		Publication: api.NativeArtifactPublication{
			PublicationID: publicationID, Kind: "image", Name: "output.png", MediaType: "image/png",
			Size: int64(len(data)), SHA256: &digest, Disposition: "inline", Retention: "session_inline", Origin: "native",
			Availability: api.NativeArtifactAvailability{Status: "available"},
		},
	}
}

func artifactMappingJSONRequest(method, path string, body []byte) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func decodeRuntimeMapping(t *testing.T, recorder *httptest.ResponseRecorder) api.RuntimeArtifactMappingResponse {
	t.Helper()
	var response api.RuntimeArtifactMappingResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode runtime mapping: %v body=%s", err, recorder.Body.String())
	}
	return response
}

func TestRuntimeArtifactMappingResolveRotationContentFailAndBrowserQuery(t *testing.T) {
	d := newBrowserTestDaemon(t, "owner", "")
	thread, _ := insertPiStoredHistoryThread(t, d, t.TempDir(), t.TempDir())
	data := bytes.Repeat([]byte("output-media"), 30000)
	publicationID := uuid.NewString()
	target1 := api.TransferTarget{StreamID: "stream-one", SessionID: thread.AgentSessionID, SessionEpoch: 1}
	writeTransferConsumer(t, d, thread.ID, target1)
	resolve := mappingResolveRequest(thread, target1, publicationID, data)
	body, _ := json.Marshal(resolve)

	// Runtime mutation is Unix-only.
	recorder := httptest.NewRecorder()
	d.routes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", body))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("runtime mapping resolve exposed on TCP router: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", body))
	resolved := decodeRuntimeMapping(t, recorder)
	if recorder.Code != http.StatusOK || resolved.Protocol != "sesh.pi-artifact-map" || resolved.Mapping.Status != "staging" || resolved.Mapping.ArtifactID == "" || resolved.Mapping.ProducerTarget == nil || *resolved.Mapping.ProducerTarget != target1 {
		t.Fatalf("resolve = %d %#v body=%s", recorder.Code, resolved, recorder.Body.String())
	}

	// A current same-session target may finish the immutable original producer's artifact.
	target2 := api.TransferTarget{StreamID: "stream-two", SessionID: thread.AgentSessionID, SessionEpoch: 2}
	writeTransferConsumer(t, d, thread.ID, target2)
	contentURL := "/v1/runtime/artifact-mappings/content?thread_id=" + thread.ID + "&mapping_id=" + resolved.Mapping.MappingID
	request := httptest.NewRequest(http.MethodPut, contentURL, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Sesh-Consumer-Stream-ID", target2.StreamID)
	request.Header.Set("X-Sesh-Consumer-Session-ID", target2.SessionID)
	request.Header.Set("X-Sesh-Consumer-Session-Epoch", "2")
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, request)
	ready := decodeRuntimeMapping(t, recorder)
	if recorder.Code != http.StatusOK || ready.Mapping.Status != "ready" || ready.Mapping.Artifact == nil || ready.Mapping.Artifact.Target != target1 || ready.Mapping.ProducerTarget == nil || *ready.Mapping.ProducerTarget != target1 {
		t.Fatalf("rotated content = %d %#v body=%s", recorder.Code, ready, recorder.Body.String())
	}

	queryBody, _ := json.Marshal(api.BrowserArtifactMappingsQueryRequest{ThreadID: thread.ID, NativeSession: thread.AgentSessionID, PublicationIDs: []string{publicationID, uuid.NewString()}})
	recorder = httptest.NewRecorder()
	d.routes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/browser/artifact-mappings/query", queryBody))
	var queried api.BrowserArtifactMappingsQueryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &queried); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || queried.OwnerMachine != "owner" || len(queried.Mappings) != 2 || queried.Mappings[0].Status != "ready" || queried.Mappings[0].Artifact == nil || queried.Mappings[0].Artifact.Target != target1 || queried.Mappings[1].Status != "unmapped" || strings.Contains(recorder.Body.String(), resolved.Mapping.MappingID) {
		t.Fatalf("browser mapping query = %d %#v body=%s", recorder.Code, queried, recorder.Body.String())
	}

	failedPublication := uuid.NewString()
	failedResolve := mappingResolveRequest(thread, target2, failedPublication, []byte("will fail"))
	body, _ = json.Marshal(failedResolve)
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("failure mapping resolve = %d %s", recorder.Code, recorder.Body.String())
	}
	failBody, _ := json.Marshal(api.RuntimeArtifactMappingFailRequest{
		ThreadID: thread.ID, ConsumerTarget: target2, NativeSession: thread.AgentSessionID, PublicationID: failedPublication,
		Failure: api.RuntimeArtifactMappingFailure{Source: "native_copy", Code: "artifact_not_found", Message: "native bytes unavailable"},
	})
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/fail", failBody))
	failed := decodeRuntimeMapping(t, recorder)
	if recorder.Code != http.StatusOK || failed.Mapping.Status != "unavailable" || failed.Mapping.Unavailable == nil || failed.Mapping.Unavailable.Code != "artifact_not_found" {
		t.Fatalf("mapping fail = %d %#v body=%s", recorder.Code, failed, recorder.Body.String())
	}

	failBody = []byte(strings.Replace(string(failBody), `"artifact_not_found"`, `"timeout"`, 1))
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/fail", failBody))
	var transferError api.BrowserTransferErrorResponse
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusBadRequest || transferError.Error.Code != "invalid_failure" {
		t.Fatalf("retryable failure persisted = %d %#v", recorder.Code, transferError)
	}
}

func TestRuntimeArtifactMappingStrictIdentityAndWrongOwnerRefusal(t *testing.T) {
	d := newBrowserTestDaemon(t, "owner", "")
	thread, _ := insertPiStoredHistoryThread(t, d, t.TempDir(), t.TempDir())
	target := api.TransferTarget{StreamID: "stream", SessionID: thread.AgentSessionID, SessionEpoch: 1}
	writeTransferConsumer(t, d, thread.ID, target)
	resolve := mappingResolveRequest(thread, target, uuid.NewString(), []byte("data"))
	body, _ := json.Marshal(resolve)

	recorder := httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", bytes.NewReader(body)))
	var transferError api.BrowserTransferErrorResponse
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusBadRequest || transferError.Error.Code != "invalid_request" {
		t.Fatalf("missing JSON content type accepted = %d %#v", recorder.Code, transferError)
	}
	duplicateContentType := artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", body)
	duplicateContentType.Header.Add("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, duplicateContentType)
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusBadRequest || transferError.Error.Code != "invalid_request" {
		t.Fatalf("duplicate JSON content type accepted = %d %#v", recorder.Code, transferError)
	}

	// Local records that claim another machine are conflicts, not movement.
	thread.Machine = "other"
	if err := d.store.DeleteThread(thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.store.InsertThread(thread); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", body))
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusConflict || transferError.Error.Code != "wrong_owner" {
		t.Fatalf("wrong-owner runtime resolve = %d %#v", recorder.Code, transferError)
	}

	thread.Machine = d.cfg.Machine
	if err := d.store.DeleteThread(thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.store.InsertThread(thread); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", body))
	resolved := decodeRuntimeMapping(t, recorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("resolve = %d %s", recorder.Code, recorder.Body.String())
	}
	contentURL := "/v1/runtime/artifact-mappings/content?thread_id=" + thread.ID + "&mapping_id=" + resolved.Mapping.MappingID
	request := httptest.NewRequest(http.MethodPut, contentURL, strings.NewReader("data"))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Sesh-Consumer-Stream-ID", target.StreamID)
	request.Header.Set("X-Sesh-Consumer-Session-ID", target.SessionID)
	request.Header.Set("X-Sesh-Consumer-Session-Epoch", "1")
	request.Header.Add("X-Sesh-Consumer-Session-Epoch", "1")
	recorder = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, request)
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusBadRequest || transferError.Error.Code != "invalid_request" {
		t.Fatalf("duplicate target header accepted = %d %#v", recorder.Code, transferError)
	}
}

func TestBrowserArtifactMappingRealHTTPPeerRouting(t *testing.T) {
	peer := newBrowserTestDaemon(t, "peer", "")
	thread, _ := insertPiStoredHistoryThread(t, peer, t.TempDir(), t.TempDir())
	target := api.TransferTarget{StreamID: "peer-stream", SessionID: thread.AgentSessionID, SessionEpoch: 0}
	writeTransferConsumer(t, peer, thread.ID, target)
	data := []byte("peer mapped bytes")
	publicationID := uuid.NewString()
	resolveBody, _ := json.Marshal(mappingResolveRequest(thread, target, publicationID, data))
	recorder := httptest.NewRecorder()
	peer.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", resolveBody))
	resolved := decodeRuntimeMapping(t, recorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("peer resolve = %d %s", recorder.Code, recorder.Body.String())
	}
	contentURL := "/v1/runtime/artifact-mappings/content?thread_id=" + thread.ID + "&mapping_id=" + resolved.Mapping.MappingID
	request := httptest.NewRequest(http.MethodPut, contentURL, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Sesh-Consumer-Stream-ID", target.StreamID)
	request.Header.Set("X-Sesh-Consumer-Session-ID", target.SessionID)
	request.Header.Set("X-Sesh-Consumer-Session-Epoch", "0")
	recorder = httptest.NewRecorder()
	peer.unixRoutes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("peer content = %d %s", recorder.Code, recorder.Body.String())
	}

	peerServer := httptest.NewServer(peer.tokenAuth("peer-secret", peer.routes()))
	defer peerServer.Close()
	peerURL, _ := url.Parse(peerServer.URL)
	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	registry := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "peer-secret"}}}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	if err := hub.view.replaceAll("peer", time.Now().Unix(), []api.ThreadSnapshot{{Thread: thread, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}
	query := api.BrowserArtifactMappingsQueryRequest{ThreadID: thread.ID, NativeSession: thread.AgentSessionID, PublicationIDs: []string{publicationID}}
	queryBody, _ := json.Marshal(query)
	recorder = httptest.NewRecorder()
	hub.handleBrowserArtifactMappingsQuery(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/browser/artifact-mappings/query", queryBody))
	var response api.BrowserArtifactMappingsQueryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || response.OwnerMachine != "peer" || len(response.Mappings) != 1 || response.Mappings[0].Status != "ready" || response.Mappings[0].Artifact == nil || strings.Contains(recorder.Body.String(), "peer-secret") || strings.Contains(recorder.Body.String(), resolved.Mapping.MappingID) {
		t.Fatalf("peer mapping query = %d %#v body=%s", recorder.Code, response, recorder.Body.String())
	}

	registry.Peers["peer"] = peers.Peer{Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "wrong"}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	hub.handleBrowserArtifactMappingsQuery(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/browser/artifact-mappings/query", queryBody))
	var transferError api.BrowserTransferErrorResponse
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusBadGateway || transferError.Error.Code != "owner_routing_failed" || strings.Contains(recorder.Body.String(), "wrong") {
		t.Fatalf("peer auth failure = %d %#v body=%s", recorder.Code, transferError, recorder.Body.String())
	}
}

func TestBrowserArtifactMappingPeerEnvelopeAndErrorSanitization(t *testing.T) {
	threadID, sessionID, publicationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	query := api.BrowserArtifactMappingsQueryRequest{ThreadID: threadID, NativeSession: sessionID, PublicationIDs: []string{publicationID}}
	queryBody, _ := json.Marshal(query)
	malformed := api.BrowserArtifactMappingsQueryResponse{
		Schema: api.SchemaVersion, Protocol: "sesh.pi-artifact-map", Version: 1, ThreadID: threadID, OwnerMachine: "peer", NativeSession: sessionID,
		Mappings: []api.BrowserArtifactMappingItem{{PublicationID: publicationID, Status: "unavailable", UpdatedAtUnixMS: 1, Unavailable: &api.ArtifactMappingUnavailable{Source: "native", Code: "source_failed", Message: "/private/secret"}}},
	}
	malformedBody, _ := json.Marshal(malformed)
	malformedPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(malformedBody)
	}))
	defer malformedPeer.Close()
	malformedURL, _ := url.Parse(malformedPeer.URL)
	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	if err := (peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: malformedURL.Host, ApiToken: "server-secret"}}}).Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	thread := api.Thread{ID: threadID, Machine: "peer", AgentKind: "pi", AgentSessionID: sessionID}
	if err := hub.view.replaceAll("peer", time.Now().Unix(), []api.ThreadSnapshot{{Thread: thread, Head: api.Headless, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	hub.handleBrowserArtifactMappingsQuery(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/browser/artifact-mappings/query", queryBody))
	var transferError api.BrowserTransferErrorResponse
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusBadGateway || transferError.Error.Code != "owner_routing_failed" || strings.Contains(recorder.Body.String(), "private") || strings.Contains(recorder.Body.String(), "server-secret") {
		t.Fatalf("malformed peer envelope = %d %#v body=%s", recorder.Code, transferError, recorder.Body.String())
	}

	errorPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, api.BrowserTransferErrorResponse{Schema: api.SchemaVersion, Status: "error", Error: api.BrowserTransferError{Code: "mapping_conflict", Message: "credential=server-secret"}})
	}))
	defer errorPeer.Close()
	errorURL, _ := url.Parse(errorPeer.URL)
	if err := (peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: errorURL.Host, ApiToken: "server-secret"}}}).Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	hub.handleBrowserArtifactMappingsQuery(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/browser/artifact-mappings/query", queryBody))
	_ = json.Unmarshal(recorder.Body.Bytes(), &transferError)
	if recorder.Code != http.StatusConflict || transferError.Error.Code != "mapping_conflict" || transferError.Error.Message != "owner artifact mapping request failed: mapping_conflict" || strings.Contains(recorder.Body.String(), "server-secret") {
		t.Fatalf("peer error not normalized = %d %#v body=%s", recorder.Code, transferError, recorder.Body.String())
	}
}

func TestDaemonStartupRepairsMappingAfterInterruptedThreadDeletion(t *testing.T) {
	d := newBrowserTestDaemon(t, "owner", "")
	thread, _ := insertPiStoredHistoryThread(t, d, t.TempDir(), t.TempDir())
	target := api.TransferTarget{StreamID: "stream", SessionID: thread.AgentSessionID, SessionEpoch: 0}
	writeTransferConsumer(t, d, thread.ID, target)
	publicationID := uuid.NewString()
	body, _ := json.Marshal(mappingResolveRequest(thread, target, publicationID, []byte("pending bytes")))
	recorder := httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(recorder, artifactMappingJSONRequest(http.MethodPost, "/v1/runtime/artifact-mappings/resolve", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("resolve before interrupted deletion = %d %s", recorder.Code, recorder.Body.String())
	}
	cfg := d.cfg
	if err := d.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteThread(thread.ID); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.store.Close() })
	mappings, err := restarted.transfers.QueryArtifactMappings(thread.ID, thread.AgentSessionID, []string{publicationID})
	if err != nil || mappings[0] != nil {
		t.Fatalf("startup did not repair orphaned mapping: %#v err=%v", mappings, err)
	}
}
