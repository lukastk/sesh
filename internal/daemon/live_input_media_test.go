package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/transfers"
)

type liveInputBridgeRequest struct {
	Protocol           string           `json:"protocol"`
	Version            int              `json:"version"`
	ThreadID           string           `json:"thread_id"`
	Target             transfers.Target `json:"target"`
	ListenerGeneration string           `json:"listener_generation"`
	InputImageID       string           `json:"input_image_id"`
}

func setThreadRuntimeActive(t *testing.T, d *Daemon, threadID string) {
	t.Helper()
	d.hlMu.Lock()
	d.hlInFlight[threadID] = true
	d.hlMu.Unlock()
	t.Cleanup(func() {
		d.hlMu.Lock()
		delete(d.hlInFlight, threadID)
		d.hlMu.Unlock()
	})
}

func writeLiveInputConsumer(t *testing.T, d *Daemon, threadID string, target transfers.Target, generation string, withReader bool) {
	t.Helper()
	inbox, err := d.transfers.Inbox(threadID)
	if err != nil {
		t.Fatal(err)
	}
	consumer := transfers.Consumer{
		Protocol: "sesh.pi-transfer", Version: 1, ThreadID: threadID, Target: target,
		// Deliberately use the final contract's unsorted example order. Readers
		// canonicalize it so an incidental writer-order mismatch cannot disable
		// legacy attachment or output capabilities.
		Capabilities: []string{"attachment-lease-v1", "artifact-publish-v1"},
	}
	if withReader {
		consumer.Capabilities = append(consumer.Capabilities, transfers.InputImageReadCapability)
		consumer.InputImageReader = &transfers.InputImageReaderDescriptor{
			Protocol: "pi.input-image-read", Version: 1,
			ListenerGeneration: generation, SocketSuffix: ".input.sock",
		}
	}
	body, err := json.Marshal(consumer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "consumer.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func startLiveInputBridge(t *testing.T, sessionID string, handler http.Handler) string {
	t.Helper()
	root := shortBrowserTempDir(t)
	t.Setenv("TMPDIR", root)
	dir := filepath.Join(root, "pi-rpc-sockets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".input.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		_ = os.Remove(path)
	})
	return path
}

func writePrivateInputSuccess(t *testing.T, w http.ResponseWriter, req liveInputBridgeRequest, body []byte, canonical bool) {
	t.Helper()
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Pi-Input-Image-Protocol", "pi.input-image-read")
	w.Header().Set("X-Pi-Input-Image-Version", "1")
	w.Header().Set("X-Pi-Input-Image-Listener-Generation", req.ListenerGeneration)
	w.Header().Set("X-Pi-Input-Image-ID", req.InputImageID)
	w.Header().Set("X-Pi-Input-Image-Thread-ID", req.ThreadID)
	w.Header().Set("X-Pi-Input-Image-Stream-ID", req.Target.StreamID)
	w.Header().Set("X-Pi-Input-Image-Session-ID", req.Target.SessionID)
	w.Header().Set("X-Pi-Input-Image-Session-Epoch", strconv.FormatInt(req.Target.SessionEpoch, 10))
	w.Header().Set("X-Pi-Input-Image-SHA256", digest)
	if canonical {
		w.Header().Set("X-Pi-Input-Image-Digest-Status", "canonical-match")
		w.Header().Set("X-Pi-Input-Image-Canonical-SHA256", digest)
	} else {
		w.Header().Set("X-Pi-Input-Image-Digest-Status", "computed-only")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func decodePrivateInputRequest(t *testing.T, r *http.Request) liveInputBridgeRequest {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/input-images/read" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" || r.ContentLength < 1 || r.ContentLength > 8192 {
		t.Fatalf("private request = %s %s content-type=%q length=%d", r.Method, r.URL.String(), r.Header.Get("Content-Type"), r.ContentLength)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || int64(len(body)) != r.ContentLength {
		t.Fatalf("private request body = %d bytes, %v", len(body), err)
	}
	var request liveInputBridgeRequest
	if decodeStrictJSON(body, &request) != nil {
		t.Fatalf("private request JSON = %s", body)
	}
	return request
}

func writePrivateInputError(t *testing.T, w http.ResponseWriter, status int, code string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"protocol": "pi.input-image-read", "version": 1, "status": "error",
		"error": map[string]any{"code": code, "message": "bounded private failure"},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func TestBrowserLiveInputImageBytesUsesOnlyVerifiedBridge(t *testing.T) {
	argvLog := installFakeStoredInputPi(t)
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, t.TempDir(), t.TempDir())
	setThreadRuntimeActive(t, d, thread.ID)
	target := transfers.Target{StreamID: "live-stream", SessionID: thread.AgentSessionID, SessionEpoch: 7}
	generation := uuid.NewString()
	inputImageID := uuid.NewString()
	body := []byte("verified-live-png")
	requestPath := "/v1/browser/input-images?thread_id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&input_image_id=" + inputImageID

	writeLiveInputConsumer(t, d, thread.ID, target, generation, false)
	rec := httptest.NewRecorder()
	d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusNotImplemented || response.Error == nil || response.Error.Code != "unsupported_native_capability" {
		t.Fatalf("legacy active runtime = %d %#v", rec.Code, response)
	}
	if logged, _ := os.ReadFile(argvLog); len(logged) != 0 {
		t.Fatalf("active runtime invoked stopped CLI: %s", logged)
	}

	writeLiveInputConsumer(t, d, thread.ID, target, generation, true)
	rec = httptest.NewRecorder()
	d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
	response = decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusServiceUnavailable || response.Error == nil || response.Error.Code != "runtime_unavailable" {
		t.Fatalf("advertised missing endpoint = %d %#v", rec.Code, response)
	}
	if logged, _ := os.ReadFile(argvLog); len(logged) != 0 {
		t.Fatalf("missing live endpoint invoked stopped CLI: %s", logged)
	}

	var mu sync.Mutex
	requests := 0
	startLiveInputBridge(t, thread.AgentSessionID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodePrivateInputRequest(t, r)
		if request.Protocol != "pi.input-image-read" || request.Version != 1 || request.ThreadID != thread.ID || request.Target != target || request.ListenerGeneration != generation || request.InputImageID != inputImageID {
			t.Errorf("private request identity = %#v", request)
		}
		mu.Lock()
		requests++
		mu.Unlock()
		writePrivateInputSuccess(t, w, request, body, true)
	}))
	writeLiveInputConsumer(t, d, thread.ID, target, generation, true)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec = httptest.NewRecorder()
		d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(method, requestPath, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) || rec.Header().Get("X-Sesh-Input-Image-Digest-Status") != "canonical-match" || rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("Accept-Ranges") != "none" || rec.Header().Get("X-Pi-Input-Image-Listener-Generation") != "" {
			t.Fatalf("%s live bytes = %d headers=%#v body=%q", method, rec.Code, rec.Header(), rec.Body.String())
		}
		if method == http.MethodGet && !bytes.Equal(rec.Body.Bytes(), body) {
			t.Fatalf("GET live body = %q", rec.Body.Bytes())
		}
		if method == http.MethodHead && rec.Body.Len() != 0 {
			t.Fatalf("HEAD live body = %q", rec.Body.Bytes())
		}
	}
	mu.Lock()
	gotRequests := requests
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("private bridge requests = %d, want 2", gotRequests)
	}
	if logged, _ := os.ReadFile(argvLog); len(logged) != 0 {
		t.Fatalf("live byte success invoked stopped CLI: %s", logged)
	}
}

func TestBrowserLiveInputImageErrorsAndGenerationReplacement(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	thread, _ := insertPiStoredHistoryThread(t, d, t.TempDir(), t.TempDir())
	setThreadRuntimeActive(t, d, thread.ID)
	target := transfers.Target{StreamID: "live-stream", SessionID: thread.AgentSessionID, SessionEpoch: 1}
	generation := uuid.NewString()
	inputImageID := uuid.NewString()
	requestPath := "/v1/browser/input-images?thread_id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&input_image_id=" + inputImageID

	mode := "pending"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	startLiveInputBridge(t, thread.AgentSessionID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodePrivateInputRequest(t, r)
		switch mode {
		case "pending":
			writePrivateInputError(t, w, http.StatusConflict, "input_image_pending")
		case "branch":
			writePrivateInputError(t, w, http.StatusConflict, "stale_branch")
		case "replace":
			started <- struct{}{}
			<-release
			writePrivateInputSuccess(t, w, request, []byte("old-listener"), false)
		case "malformed":
			request.ListenerGeneration = uuid.NewString()
			writePrivateInputSuccess(t, w, request, []byte("bad-generation"), true)
		}
	}))
	writeLiveInputConsumer(t, d, thread.ID, target, generation, true)

	for _, tc := range []struct {
		mode   string
		status int
		code   string
	}{
		{mode: "pending", status: http.StatusConflict, code: "input_image_pending"},
		{mode: "branch", status: http.StatusConflict, code: "stale_branch"},
		{mode: "malformed", status: http.StatusBadGateway, code: "invalid_native_response"},
	} {
		mode = tc.mode
		rec := httptest.NewRecorder()
		d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
		response := decodeStoredInputResponse(t, rec)
		if rec.Code != tc.status || response.Error == nil || response.Error.Code != tc.code {
			t.Fatalf("%s = %d %#v", tc.mode, rec.Code, response)
		}
	}

	mode = "replace"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		d.handleBrowserStoredInputImageBytes(rec, httptest.NewRequest(http.MethodGet, requestPath, nil))
		done <- rec
	}()
	<-started
	writeLiveInputConsumer(t, d, thread.ID, target, uuid.NewString(), true)
	close(release)
	rec := <-done
	response := decodeStoredInputResponse(t, rec)
	if rec.Code != http.StatusConflict || response.Error == nil || response.Error.Code != "stale_listener_generation" || bytes.Contains(rec.Body.Bytes(), []byte("old-listener")) {
		t.Fatalf("generation replacement = %d %#v body=%q", rec.Code, response, rec.Body.String())
	}

	writeLiveInputConsumer(t, d, thread.ID, target, generation, true)
	authority, err := d.transfers.ResolveInputImageReader(thread.ID, thread.AgentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	rotated := target
	rotated.StreamID = "replacement-stream"
	rotated.SessionEpoch++
	writeLiveInputConsumer(t, d, thread.ID, rotated, generation, true)
	query := storedInputBytesQuery{ThreadID: thread.ID, NativeSession: thread.AgentSessionID, InputImageID: inputImageID}
	if code, _ := d.revalidateLiveInputAuthority(query, "self", authority); code != "stale_target" {
		t.Fatalf("target replacement revalidation code = %q", code)
	}
}

func TestBrowserLiveInputImageRealAuthenticatedPeer(t *testing.T) {
	peer := newBrowserTestDaemon(t, "peer", "")
	thread, _ := insertPiStoredHistoryThread(t, peer, t.TempDir(), t.TempDir())
	setThreadRuntimeActive(t, peer, thread.ID)
	target := transfers.Target{StreamID: "peer-live-stream", SessionID: thread.AgentSessionID, SessionEpoch: 2}
	generation := uuid.NewString()
	inputImageID := uuid.NewString()
	body := []byte("peer-live-image")
	var peerModeMu sync.Mutex
	peerPending := false
	startLiveInputBridge(t, thread.AgentSessionID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodePrivateInputRequest(t, r)
		peerModeMu.Lock()
		pending := peerPending
		peerModeMu.Unlock()
		if pending {
			writePrivateInputError(t, w, http.StatusConflict, "input_image_pending")
			return
		}
		writePrivateInputSuccess(t, w, request, body, false)
	}))
	writeLiveInputConsumer(t, peer, thread.ID, target, generation, true)

	peerServer := httptest.NewServer(peer.tokenAuth("peer-secret", peer.routes()))
	defer peerServer.Close()
	peerURL, err := url.Parse(peerServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	hub := newBrowserTestDaemon(t, "hub", "[browser_hub]\nenabled = true\ntrusted_peers = [\"peer\"]\n")
	registry := peers.Registry{Peers: map[string]peers.Peer{"peer": {Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "peer-secret"}}}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	if err := hub.view.replaceAll("peer", time.Now().Unix(), []api.ThreadSnapshot{{Thread: thread, Head: api.Headful, Busy: api.BusyIdle}}); err != nil {
		t.Fatal(err)
	}
	hubServer := httptest.NewServer(hub.tokenAuth("hub-secret", hub.routes()))
	defer hubServer.Close()

	requestPath := hubServer.URL + "/v1/browser/input-images?thread_id=" + thread.ID + "&native_session_id=" + thread.AgentSessionID + "&input_image_id=" + inputImageID
	request, err := http.NewRequest(http.MethodGet, requestPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer hub-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(got, body) || response.Header.Get("X-Sesh-Input-Image-Digest-Status") != "computed-only" || response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("X-Pi-Input-Image-Listener-Generation") != "" {
		t.Fatalf("peer live bytes = %d headers=%#v body=%q", response.StatusCode, response.Header, got)
	}

	peerModeMu.Lock()
	peerPending = true
	peerModeMu.Unlock()
	request, err = http.NewRequest(http.MethodGet, requestPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer hub-secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	errorBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var envelope api.BrowserStoredInputMediaResponse
	if json.Unmarshal(errorBody, &envelope) != nil || response.StatusCode != http.StatusConflict || envelope.Error == nil || envelope.Error.Code != "input_image_pending" || response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("peer live pending = %d %#v headers=%#v body=%s", response.StatusCode, envelope, response.Header, errorBody)
	}
}
