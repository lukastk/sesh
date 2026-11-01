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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/peers"
	"github.com/lukastk/sesh/internal/transfers"
)

func transferDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func decodeAttachmentResponse(t *testing.T, rec *httptest.ResponseRecorder) api.BrowserAttachmentResponse {
	t.Helper()
	var out api.BrowserAttachmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode attachment response: %v body=%s", err, rec.Body.String())
	}
	return out
}

func assertBoundedAttachmentStartRejection(t *testing.T, rec *httptest.ResponseRecorder, forbidden ...string) {
	t.Helper()
	var out api.BrowserTransferErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode attachment rejection: %v body=%s", err, rec.Body.String())
	}
	if rec.Code != http.StatusBadRequest || out.Schema != api.SchemaVersion || out.Error.Code != "invalid_request" || out.Error.Message == "" || len(out.Error.Message) > 512 || strings.ContainsAny(out.Error.Message, "\r\n") {
		t.Fatalf("attachment rejection = %d %#v body=%s", rec.Code, out, rec.Body.String())
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(rec.Body.String(), value) {
			t.Fatalf("attachment rejection exposed %q: %s", value, rec.Body.String())
		}
	}
}

func writeTransferConsumer(t *testing.T, d *Daemon, threadID string, target api.TransferTarget) {
	t.Helper()
	inbox, err := d.transfers.Inbox(threadID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"protocol": "sesh.pi-transfer", "version": 1, "thread_id": threadID, "target": target, "capabilities": []string{"attachment-lease-v1", "artifact-publish-v1"}})
	if err := os.WriteFile(filepath.Join(inbox, "consumer.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserAttachmentLifecyclePrepareAndArtifactPublication(t *testing.T) {
	d := newBrowserTestDaemon(t, "owner", "[browser_api]\nallowed_origins = [\"app://obsidian.md\"]\n")
	thread, _ := insertPiStoredHistoryThread(t, d, t.TempDir(), t.TempDir())
	for _, name := range []string{".", ".."} {
		start := api.BrowserAttachmentStartRequest{ThreadID: thread.ID, ClientID: uuid.NewString(), Name: name, Size: 0, SHA256: transferDigest(nil)}
		body, _ := json.Marshal(start)
		rec := httptest.NewRecorder()
		d.handleBrowserAttachmentStart(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/start", bytes.NewReader(body)))
		assertBoundedAttachmentStartRejection(t, rec, d.cfg.Home)
	}

	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	attachmentName := "猫-preview.dat"
	attachmentMediaType := "text/plain; charset=utf-8"
	start := api.BrowserAttachmentStartRequest{ThreadID: thread.ID, ClientID: uuid.NewString(), Name: attachmentName, Size: int64(len(data)), SHA256: transferDigest(data), MediaType: attachmentMediaType}
	body, _ := json.Marshal(start)
	rec := httptest.NewRecorder()
	d.handleBrowserAttachmentStart(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/start", bytes.NewReader(body)))
	resp := decodeAttachmentResponse(t, rec)
	if rec.Code != 200 || resp.Schema != api.SchemaVersion || resp.Attachment.NextOffset != 0 || resp.Attachment.OwnerMachine != "owner" || resp.Attachment.Name != attachmentName || resp.Attachment.MediaType != attachmentMediaType {
		t.Fatalf("start = %d %#v %s", rec.Code, resp, rec.Body.String())
	}
	id := resp.Attachment.ID
	chunkURL := "/v1/browser/attachments/chunk?thread_id=" + thread.ID + "&id=" + id + "&offset=0"
	req := httptest.NewRequest(http.MethodPut, chunkURL, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Sesh-Chunk-SHA256", transferDigest(data))
	rec = httptest.NewRecorder()
	d.handleBrowserAttachmentChunk(rec, req)
	resp = decodeAttachmentResponse(t, rec)
	if rec.Code != 200 || resp.Attachment.NextOffset != int64(len(data)) || resp.Attachment.Name != attachmentName || resp.Attachment.MediaType != attachmentMediaType {
		t.Fatalf("chunk = %d %#v %s", rec.Code, resp, rec.Body.String())
	}
	mutation, _ := json.Marshal(api.BrowserAttachmentMutationRequest{ThreadID: thread.ID, ID: id})
	rec = httptest.NewRecorder()
	d.handleBrowserAttachmentComplete(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/complete", bytes.NewReader(mutation)))
	resp = decodeAttachmentResponse(t, rec)
	if rec.Code != 200 || resp.Attachment.State != "ready" || resp.Attachment.Name != attachmentName || resp.Attachment.MediaType != attachmentMediaType {
		t.Fatalf("complete = %d %#v", rec.Code, resp)
	}
	wrongAuthority := httptest.NewRequest(http.MethodGet, "/v1/browser/attachments?thread_id="+thread.ID+"&id="+id, nil)
	wrongAuthority = wrongAuthority.WithContext(context.WithValue(wrongAuthority.Context(), browserAuthorityContextKey{}, "bearer:other"))
	rec = httptest.NewRecorder()
	d.handleBrowserAttachmentStatus(rec, wrongAuthority)
	var wrong api.BrowserTransferErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &wrong)
	if rec.Code != http.StatusNotFound || wrong.Error.Code != "attachment_not_found" {
		t.Fatalf("cross-authority status = %d %#v", rec.Code, wrong)
	}

	target := api.TransferTarget{StreamID: "stream-1", SessionID: thread.AgentSessionID, SessionEpoch: 2}
	prepare := api.BrowserAttachmentPrepareRequest{ThreadID: thread.ID, RequestID: uuid.NewString(), Target: target, AttachmentIDs: []string{id}}
	body, _ = json.Marshal(prepare)
	rec = httptest.NewRecorder()
	d.handleBrowserAttachmentPrepare(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/prepare", bytes.NewReader(body)))
	var terr api.BrowserTransferErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &terr)
	if rec.Code != http.StatusNotImplemented || terr.Error.Code != "unsupported_runtime_transfer" {
		t.Fatalf("prepare without consumer = %d %#v", rec.Code, terr)
	}
	writeTransferConsumer(t, d, thread.ID, target)
	rec = httptest.NewRecorder()
	d.handleBrowserAttachmentPrepare(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/prepare", bytes.NewReader(body)))
	var prepared api.BrowserAttachmentPrepareResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(prepared.LeaseIDs) != 1 || strings.Contains(rec.Body.String(), d.cfg.Home) {
		t.Fatalf("prepare = %d %#v body=%s", rec.Code, prepared, rec.Body.String())
	}
	inbox, err := d.transfers.Inbox(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	leaseDir := filepath.Join(inbox, "leases", prepared.LeaseIDs[0])
	var lease transfers.Lease
	leaseJSON, err := os.ReadFile(filepath.Join(leaseDir, "lease.json"))
	if err != nil || json.Unmarshal(leaseJSON, &lease) != nil || lease.Name != attachmentName || lease.MediaType != attachmentMediaType || lease.Size != int64(len(data)) || lease.SHA256 != transferDigest(data) {
		t.Fatalf("owner byte-neutral lease = %#v, read=%v body=%s", lease, err, leaseJSON)
	}
	leased, err := os.ReadFile(filepath.Join(leaseDir, "payload"))
	if err != nil || !bytes.Equal(leased, data) {
		t.Fatalf("owner byte-neutral payload = %x, %v", leased, err)
	}

	artifactData := []byte("published artifact")
	artifactStart := api.RuntimeArtifactStartRequest{ThreadID: thread.ID, ClientID: uuid.NewString(), Target: target, Name: "result.bin", MediaType: "application/octet-stream", Size: int64(len(artifactData)), SHA256: transferDigest(artifactData), Disposition: "attachment"}
	body, _ = json.Marshal(artifactStart)
	// Runtime publication is Unix-only: the ordinary/TCP router cannot see it.
	rec = httptest.NewRecorder()
	d.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/runtime/artifacts/start", bytes.NewReader(body)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("runtime artifact route exposed on ordinary router: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/runtime/artifacts/start", bytes.NewReader(body)))
	var artifact api.BrowserArtifactResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &artifact); err != nil {
		t.Fatalf("decode artifact start: %v body=%s", err, rec.Body.String())
	}
	if rec.Code != 200 {
		t.Fatalf("artifact start = %d %s", rec.Code, rec.Body.String())
	}
	contentURL := "/v1/runtime/artifacts/content?thread_id=" + thread.ID + "&id=" + artifact.Artifact.ID
	req = httptest.NewRequest(http.MethodPut, contentURL, bytes.NewReader(artifactData))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec = httptest.NewRecorder()
	d.unixRoutes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("artifact content = %d %s", rec.Code, rec.Body.String())
	}
	downloadURL := "/v1/browser/artifacts?thread_id=" + thread.ID + "&id=" + artifact.Artifact.ID
	req = httptest.NewRequest(http.MethodGet, downloadURL, nil)
	req.Header.Set("Range", "bytes=0-8")
	rec = httptest.NewRecorder()
	d.handleBrowserArtifactDownload(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != string(artifactData[:9]) || rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != browserArtifactCacheControl || strings.Contains(rec.Body.String(), d.cfg.Home) {
		t.Fatalf("artifact range = %d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	head := httptest.NewRequest(http.MethodHead, downloadURL, nil)
	rec = httptest.NewRecorder()
	d.handleBrowserArtifactDownload(rec, head)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != browserArtifactCacheControl || rec.Header().Get("Content-Length") != strconv.Itoa(len(artifactData)) {
		t.Fatalf("artifact head cache policy = %d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	corsRequest := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	corsRequest.Header.Set("Authorization", "Bearer cache-token")
	corsRequest.Header.Set("Origin", "app://obsidian.md")
	rec = httptest.NewRecorder()
	d.browserCORS(d.tokenAuth("cache-token", d.routes())).ServeHTTP(rec, corsRequest)
	if rec.Code != http.StatusOK || rec.Body.String() != string(artifactData) || rec.Header().Get("Access-Control-Allow-Origin") != "app://obsidian.md" || rec.Header().Get("Cache-Control") != browserArtifactCacheControl {
		t.Fatalf("artifact CORS GET cache policy = %d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestBrowserAttachmentRealHTTPPeerRouting(t *testing.T) {
	peer := newBrowserTestDaemon(t, "peer", "")
	thread, _ := insertPiStoredHistoryThread(t, peer, t.TempDir(), t.TempDir())
	target := api.TransferTarget{StreamID: "peer-artifact-stream", SessionID: thread.AgentSessionID, SessionEpoch: 3}
	writeTransferConsumer(t, peer, thread.ID, target)
	artifactData := []byte("peer managed artifact bytes")
	artifactStart := api.RuntimeArtifactStartRequest{ThreadID: thread.ID, ClientID: uuid.NewString(), Target: target, Name: "peer-result.bin", MediaType: "application/octet-stream", Size: int64(len(artifactData)), SHA256: transferDigest(artifactData), Disposition: "attachment"}
	body, _ := json.Marshal(artifactStart)
	rec := httptest.NewRecorder()
	peer.unixRoutes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/runtime/artifacts/start", bytes.NewReader(body)))
	var artifact api.BrowserArtifactResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &artifact); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("peer artifact start = %d %#v err=%v body=%s", rec.Code, artifact, err, rec.Body.String())
	}
	contentURL := "/v1/runtime/artifacts/content?thread_id=" + thread.ID + "&id=" + artifact.Artifact.ID
	content := httptest.NewRequest(http.MethodPut, contentURL, bytes.NewReader(artifactData))
	content.Header.Set("Content-Type", "application/octet-stream")
	rec = httptest.NewRecorder()
	peer.unixRoutes().ServeHTTP(rec, content)
	if rec.Code != http.StatusOK {
		t.Fatalf("peer artifact content = %d %s", rec.Code, rec.Body.String())
	}
	ownerHandler := peer.tokenAuth("peer-secret", peer.routes())
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream := httptest.NewRecorder()
		ownerHandler.ServeHTTP(upstream, r)
		for name, values := range upstream.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		// A browser hub must impose its own private/no-store policy rather than
		// trusting or forwarding an unsafe owner response value.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(upstream.Code)
		if r.Method != http.MethodHead {
			_, _ = w.Write(upstream.Body.Bytes())
		}
	}))
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
	artifactURL := "/v1/browser/artifacts?thread_id=" + thread.ID + "&id=" + artifact.Artifact.ID
	artifactGet := httptest.NewRequest(http.MethodGet, artifactURL, nil)
	artifactGet.Header.Set("Range", "bytes=5-11")
	rec = httptest.NewRecorder()
	hub.handleBrowserArtifactDownload(rec, artifactGet)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != string(artifactData[5:12]) || rec.Header().Get("Cache-Control") != browserArtifactCacheControl || strings.Contains(rec.Header().Get("Cache-Control"), "public") {
		t.Fatalf("remote artifact GET cache policy = %d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	artifactHead := httptest.NewRequest(http.MethodHead, artifactURL, nil)
	rec = httptest.NewRecorder()
	hub.handleBrowserArtifactDownload(rec, artifactHead)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != browserArtifactCacheControl || rec.Header().Get("Content-Length") != strconv.Itoa(len(artifactData)) {
		t.Fatalf("remote artifact HEAD cache policy = %d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	for _, name := range []string{".", ".."} {
		start := api.BrowserAttachmentStartRequest{ThreadID: thread.ID, ClientID: uuid.NewString(), Name: name, Size: 0, SHA256: transferDigest(nil)}
		body, _ = json.Marshal(start)
		rec = httptest.NewRecorder()
		hub.handleBrowserAttachmentStart(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/start", bytes.NewReader(body)))
		assertBoundedAttachmentStartRejection(t, rec, "peer-secret", peer.cfg.Home, hub.cfg.Home)
	}

	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x10, 0x20, 0x30}
	attachmentName := "猫-peer-image.dat"
	attachmentMediaType := "text/plain; charset=utf-8"
	start := api.BrowserAttachmentStartRequest{ThreadID: thread.ID, ClientID: uuid.NewString(), Name: attachmentName, Size: int64(len(data)), SHA256: transferDigest(data), MediaType: attachmentMediaType}
	body, _ = json.Marshal(start)
	rec = httptest.NewRecorder()
	hub.handleBrowserAttachmentStart(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/start", bytes.NewReader(body)))
	resp := decodeAttachmentResponse(t, rec)
	if rec.Code != 200 || resp.Schema != api.SchemaVersion || resp.Attachment.OwnerMachine != "peer" || resp.Attachment.Name != attachmentName || resp.Attachment.MediaType != attachmentMediaType || strings.Contains(rec.Body.String(), "peer-secret") {
		t.Fatalf("remote start = %d %#v %s", rec.Code, resp, rec.Body.String())
	}
	chunkURL := "/v1/browser/attachments/chunk?thread_id=" + thread.ID + "&id=" + resp.Attachment.ID + "&offset=0"
	req := httptest.NewRequest(http.MethodPut, chunkURL, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Sesh-Chunk-SHA256", transferDigest(data))
	rec = httptest.NewRecorder()
	hub.handleBrowserAttachmentChunk(rec, req)
	resp = decodeAttachmentResponse(t, rec)
	if rec.Code != 200 || resp.Attachment.Name != attachmentName || resp.Attachment.MediaType != attachmentMediaType || resp.Attachment.NextOffset != int64(len(data)) || strings.Contains(rec.Body.String(), "peer-secret") {
		t.Fatalf("remote chunk = %d %#v %s", rec.Code, resp, rec.Body.String())
	}
	mutation, _ := json.Marshal(api.BrowserAttachmentMutationRequest{ThreadID: thread.ID, ID: resp.Attachment.ID})
	rec = httptest.NewRecorder()
	hub.handleBrowserAttachmentComplete(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/attachments/complete", bytes.NewReader(mutation)))
	resp = decodeAttachmentResponse(t, rec)
	if rec.Code != 200 || resp.Attachment.State != "ready" || resp.Attachment.Name != attachmentName || resp.Attachment.MediaType != attachmentMediaType || resp.Attachment.SHA256 != transferDigest(data) {
		t.Fatalf("remote complete = %d %#v %s", rec.Code, resp, rec.Body.String())
	}

	registry.Peers["peer"] = peers.Peer{Machine: "peer", ApiAddr: peerURL.Host, ApiToken: "wrong"}
	if err := registry.Save(hub.cfg.PeersPath()); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	hub.handleBrowserAttachmentStatus(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/attachments?thread_id="+thread.ID+"&id="+resp.Attachment.ID, nil))
	var er api.BrowserTransferErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &er)
	if rec.Code != http.StatusBadGateway || er.Error.Code != "owner_routing_failed" || strings.Contains(rec.Body.String(), "wrong") {
		t.Fatalf("remote auth failure = %d %#v %s", rec.Code, er, rec.Body.String())
	}
}

func TestPiLaunchEnvironmentEstablishesExactTransferIdentityTriple(t *testing.T) {
	d := newBrowserTestDaemon(t, "owner", "")
	threadID := uuid.NewString()
	env := map[string]string{
		agents.EnvThreadID:            "hostile-stale-general-thread",
		agents.EnvPiTransferThreadID:  uuid.NewString(),
		agents.EnvPiTransferInbox:     "/hostile/stale/inbox",
		agents.EnvPiTransferAPISocket: "/hostile/stale/socket",
	}
	if err := d.prepAgentEnv(agents.Pi, env, t.TempDir(), threadID); err != nil {
		t.Fatal(err)
	}
	inbox := env[agents.EnvPiTransferInbox]
	if env[agents.EnvThreadID] != threadID || env[agents.EnvPiTransferThreadID] != threadID {
		t.Fatalf("Pi transfer identity was inferred from stale environment: %#v", env)
	}
	expectedInbox, err := d.transfers.Inbox(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if inbox != expectedInbox || env[agents.EnvPiTransferAPISocket] != d.cfg.SocketPath() {
		t.Fatalf("Pi transfer env = %#v", env)
	}
	fi, err := os.Stat(inbox)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("transfer inbox = %q mode=%v err=%v", inbox, fi, err)
	}

	nonPi := map[string]string{
		agents.EnvPiTransferThreadID:  uuid.NewString(),
		agents.EnvPiTransferInbox:     "/hostile/stale/inbox",
		agents.EnvPiTransferAPISocket: "/hostile/stale/socket",
	}
	if err := d.prepAgentEnv(agents.Claude, nonPi, t.TempDir(), threadID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{agents.EnvPiTransferThreadID, agents.EnvPiTransferInbox, agents.EnvPiTransferAPISocket} {
		if _, ok := nonPi[key]; ok {
			t.Fatalf("non-Pi environment retained inherited %s: %#v", key, nonPi)
		}
	}
}
