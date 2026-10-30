package conformance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/peers"
)

func init() {
	matrix.RegisterTest("api.browser-cors", matrix.AgentAgnostic, matrix.Local, testBrowserCORS)
	matrix.RegisterTest("api.browser-ws", matrix.AgentAgnostic, matrix.Local, testBrowserWS)
	matrix.RegisterTest("api.browser-hub.http", matrix.AgentAgnostic, matrix.Remote, testBrowserHubHTTP)
	matrix.RegisterTest("api.browser-output-mappings", matrix.AgentAgnostic, matrix.Local, testBrowserOutputMappings)
	matrix.RegisterTest("api.browser-output-mappings", matrix.AgentAgnostic, matrix.Remote, testBrowserOutputMappings)
	matrix.RegisterTest("api.browser-stored-history", matrix.Pi, matrix.Local, testBrowserStoredHistoryNative)
	matrix.RegisterTest("api.browser-stored-history", matrix.Pi, matrix.Remote, testBrowserStoredHistoryNative)
	matrix.RegisterTest("api.browser-stored-artifacts", matrix.Pi, matrix.Local, testBrowserStoredArtifactsNative)
	matrix.RegisterTest("api.browser-stored-artifacts", matrix.Pi, matrix.Remote, testBrowserStoredArtifactsNative)
	matrix.RegisterTest("api.browser-stored-input-media", matrix.Pi, matrix.Local, testBrowserStoredInputMediaNative)
	matrix.RegisterTest("api.browser-stored-input-media", matrix.Pi, matrix.Remote, testBrowserStoredInputMediaNative)
	matrix.RegisterTest("api.browser-stored-media-presentation-v2", matrix.Pi, matrix.Local, testBrowserStoredMediaPresentationV2Native)
	matrix.RegisterTest("api.browser-stored-media-presentation-v2", matrix.Pi, matrix.Remote, testBrowserStoredMediaPresentationV2Native)
	matrix.RegisterTest("api.browser-live-input-media", matrix.Pi, matrix.Local, testBrowserLiveInputMediaNative)
	matrix.RegisterTest("api.browser-live-input-media", matrix.Pi, matrix.Remote, testBrowserLiveInputMediaNative)
	matrix.RegisterTest("api.browser-transfers", matrix.Pi, matrix.Local, testBrowserTransfersNative)
	matrix.RegisterTest("api.browser-transfers", matrix.Pi, matrix.Remote, testBrowserTransfersNative)
}

type strictAPIDaemon struct {
	addr, token, home, machine, socket, bin string
	env                                     []string
}

// strictAPIEnv is deliberately allowlisted for these browser tests: no inherited
// SESH values, harness markers or provider credentials reach the daemon.
func strictAPIEnv(home, machine, socket, master, addr, token string) []string {
	userHome := filepath.Join(home, "user-home")
	xdgConfig := filepath.Join(home, "xdg-config")
	xdgCache := filepath.Join(home, "xdg-cache")
	xdgData := filepath.Join(home, "xdg-data")
	xdgRuntime := filepath.Join(home, "xdg-runtime")
	codexHome := filepath.Join(home, "codex-home")
	tmpDir := filepath.Join(home, "tmp")
	for _, dir := range []string{userHome, xdgConfig, xdgCache, xdgData, xdgRuntime, codexHome, tmpDir} {
		_ = os.MkdirAll(dir, 0o700)
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + userHome,
		"XDG_CONFIG_HOME=" + xdgConfig,
		"XDG_CACHE_HOME=" + xdgCache,
		"XDG_DATA_HOME=" + xdgData,
		"XDG_RUNTIME_DIR=" + xdgRuntime,
		"TMPDIR=" + tmpDir,
		"LANG=C.UTF-8",
		"SHELL=/bin/sh",
		"SESH_HOME=" + home,
		"SESH_MACHINE=" + machine,
		"SESH_TMUX_SOCKET=" + socket,
		"SESH_MASTER_SOCKET=" + master,
		"SESH_CODEX_HOME=" + codexHome,
		"SESH_API_ADDR=" + addr,
		"SESH_API_TOKEN=" + token,
	}
}

func runStrict(bin string, env []string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func startStrictAPIDaemon(t *testing.T, prepare func(home, machine string) error) *strictAPIDaemon {
	t.Helper()
	bin := seshBin(t)
	home := shortSandboxHome(t)
	stamp := time.Now().UnixNano()
	machine := fmt.Sprintf("browser-api-%d", stamp)
	socket := fmt.Sprintf("sesh-test-browser-%d", stamp)
	master := fmt.Sprintf("sesh-test-browser-master-%d", stamp)
	addr := freePort(t)
	token := fmt.Sprintf("browser-token-%d", stamp)
	if prepare != nil {
		if err := prepare(home, machine); err != nil {
			t.Fatalf("prepare browser daemon: %v", err)
		}
	}
	env := strictAPIEnv(home, machine, socket, master, addr, token)
	if out, err := runStrict(bin, env, "daemon", "start"); err != nil {
		t.Fatalf("start strict browser daemon: %v\n%s", err, out)
	}
	d := &strictAPIDaemon{addr: addr, token: token, home: home, machine: machine, socket: socket, bin: bin, env: env}
	t.Cleanup(func() {
		_, _ = runStrict(bin, env, "daemon", "stop")
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	if !waitUntil(10*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}) {
		t.Fatalf("strict browser daemon never listened on %s", addr)
	}
	return d
}

func browserRequest(t *testing.T, d *strictAPIDaemon, method, path string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+d.addr+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func unixBrowserRequest(t *testing.T, d *strictAPIDaemon, method, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", d.home+"/daemon.sock")
	}}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequest(method, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func assertManagedArtifactCachePolicy(t *testing.T, d *strictAPIDaemon, threadID, artifactID string, expected []byte) {
	t.Helper()
	path := "/v1/browser/artifacts?thread_id=" + threadID + "&id=" + artifactID
	resp := browserRequest(t, d, http.MethodGet, path, nil)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, expected) || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("managed artifact GET cache policy = %d headers=%#v body=%q err=%v", resp.StatusCode, resp.Header, body, err)
	}
	resp = browserRequest(t, d, http.MethodHead, path, nil)
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || len(body) != 0 || resp.Header.Get("Cache-Control") != "private, no-store" || resp.Header.Get("Content-Length") != fmt.Sprint(len(expected)) {
		t.Fatalf("managed artifact HEAD cache policy = %d headers=%#v body=%q err=%v", resp.StatusCode, resp.Header, body, err)
	}
}

func testBrowserOutputMappings(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	owner := startStrictAPIDaemon(t, nil)
	threadID, sessionID, publicationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	thread := api.Thread{ID: threadID, Machine: owner.machine, SessionName: "headless-" + threadID, Cwd: "/tmp", AgentKind: "pi", Name: "mapping-conformance", Tags: []string{}, CreatedAtUnix: time.Now().Unix(), AgentSessionID: sessionID, HeadlessStarted: true}
	threadBody, _ := json.Marshal(thread)
	resp := browserRequest(t, owner, http.MethodPost, "/v1/threads/import", threadBody)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("import mapping thread: %d %s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	target := api.TransferTarget{StreamID: "mapping-stream", SessionID: sessionID, SessionEpoch: 1}
	inbox := filepath.Join(owner.home, "browser-transfers", "runtime-inboxes", threadID)
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	consumer, _ := json.Marshal(map[string]any{"protocol": "sesh.pi-transfer", "version": 1, "thread_id": threadID, "target": target, "capabilities": []string{"artifact-publish-v1"}})
	if err := os.WriteFile(filepath.Join(inbox, "consumer.json"), consumer, 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("real daemon durable mapping bytes")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	resolve := api.RuntimeArtifactMappingResolveRequest{
		ThreadID: threadID, ConsumerTarget: target, NativeSession: sessionID,
		Publication: api.NativeArtifactPublication{PublicationID: publicationID, Kind: "file", Name: "result.bin", MediaType: "application/octet-stream", Size: int64(len(data)), SHA256: &digest, Disposition: "attachment", Retention: "session_inline", Origin: "native", Availability: api.NativeArtifactAvailability{Status: "available"}},
	}
	resolveBody, _ := json.Marshal(resolve)
	resp = browserRequest(t, owner, http.MethodPost, "/v1/runtime/artifact-mappings/resolve", resolveBody)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("runtime mapping route exposed on TCP: %d %s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
	resp = unixBrowserRequest(t, owner, http.MethodPost, "/v1/runtime/artifact-mappings/resolve", resolveBody, map[string]string{"Content-Type": "application/json"})
	var resolved api.RuntimeArtifactMappingResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolved); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resolved.Mapping.Status != "staging" || resolved.Mapping.ArtifactID == "" {
		t.Fatalf("real daemon mapping resolve = %d %#v", resp.StatusCode, resolved)
	}
	contentPath := "/v1/runtime/artifact-mappings/content?thread_id=" + threadID + "&mapping_id=" + resolved.Mapping.MappingID
	resp = unixBrowserRequest(t, owner, http.MethodPut, contentPath, data, map[string]string{
		"Content-Type": "application/octet-stream", "X-Sesh-Consumer-Stream-ID": target.StreamID,
		"X-Sesh-Consumer-Session-ID": target.SessionID, "X-Sesh-Consumer-Session-Epoch": "1",
	})
	var ready api.RuntimeArtifactMappingResponse
	if err := json.NewDecoder(resp.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || ready.Mapping.Status != "ready" || ready.Mapping.Artifact == nil || ready.Mapping.Artifact.SHA256 != digest {
		t.Fatalf("real daemon mapping content = %d %#v", resp.StatusCode, ready)
	}

	query := api.BrowserArtifactMappingsQueryRequest{ThreadID: threadID, NativeSession: sessionID, PublicationIDs: []string{publicationID}}
	queryBody, _ := json.Marshal(query)
	resp = browserRequest(t, owner, http.MethodPost, "/v1/browser/artifact-mappings/query", queryBody)
	var local api.BrowserArtifactMappingsQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&local); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || local.OwnerMachine != owner.machine || len(local.Mappings) != 1 || local.Mappings[0].Status != "ready" || local.Mappings[0].Artifact == nil || local.Mappings[0].Artifact.SHA256 != digest {
		t.Fatalf("real daemon local mapping query = %d %#v", resp.StatusCode, local)
	}
	assertManagedArtifactCachePolicy(t, owner, threadID, local.Mappings[0].Artifact.ID, data)

	hub := startStrictAPIDaemon(t, func(home, _ string) error {
		configBody := "[browser_hub]\nenabled = true\ntrusted_peers = [\"" + owner.machine + "\"]\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configBody), 0o600); err != nil {
			return err
		}
		registry := peers.Registry{Peers: map[string]peers.Peer{owner.machine: {Machine: owner.machine, ApiAddr: owner.addr, ApiToken: owner.token}}}
		return registry.Save(filepath.Join(home, "peers.json"))
	})
	if !waitUntil(10*time.Second, func() bool {
		listed := browserRequest(t, hub, http.MethodGet, "/v1/browser/threads?archived=include", nil)
		defer listed.Body.Close()
		var page api.BrowserThreadPage
		if listed.StatusCode != http.StatusOK || json.NewDecoder(listed.Body).Decode(&page) != nil {
			return false
		}
		for _, row := range page.Threads {
			if row.ID == threadID && row.Machine == owner.machine {
				return true
			}
		}
		return false
	}) {
		t.Fatal("mapping owner never appeared in the browser hub's replicated view")
	}
	resp = browserRequest(t, hub, http.MethodPost, "/v1/browser/artifact-mappings/query", queryBody)
	var remote api.BrowserArtifactMappingsQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&remote); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || remote.OwnerMachine != owner.machine || len(remote.Mappings) != 1 || remote.Mappings[0].Status != "ready" || remote.Mappings[0].Artifact == nil || remote.Mappings[0].Artifact.SHA256 != digest {
		t.Fatalf("real daemon routed mapping query = %d %#v", resp.StatusCode, remote)
	}
	assertManagedArtifactCachePolicy(t, hub, threadID, remote.Mappings[0].Artifact.ID, data)
}

func testBrowserCORS(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	d := startStrictAPIDaemon(t, func(home, _ string) error {
		return os.WriteFile(filepath.Join(home, "config.toml"), []byte("[browser_api]\nallowed_origins = [\"app://obsidian.md\"]\n"), 0o600)
	})
	do := func(req *http.Request) *http.Response {
		t.Helper()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	preflight := func(path, origin, method, headers string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodOptions, "http://"+d.addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", method)
		req.Header.Set("Access-Control-Request-Headers", headers)
		return do(req)
	}

	resp := preflight("/v1/browser/threads", "app://obsidian.md", http.MethodGet, "Authorization")
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "app://obsidian.md" || resp.Header.Get("Access-Control-Allow-Credentials") != "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("real TCP browser preflight = %d %#v", resp.StatusCode, resp.Header)
	}
	resp = preflight("/v1/browser/threads", "app://obsidian.md", http.MethodGet, "Authorization, Range")
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("overbroad browser preflight = %d %#v", resp.StatusCode, resp.Header)
	}
	resp = preflight("/v1/browser/artifact-mappings/query", "app://obsidian.md", http.MethodPost, "Authorization, Content-Type")
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" {
		t.Fatalf("artifact mapping query preflight = %d %#v", resp.StatusCode, resp.Header)
	}
	resp = preflight("/v1/browser/threads/media-presentation", "app://obsidian.md", http.MethodGet, "Authorization")
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Methods") != http.MethodGet || resp.Header.Get("Access-Control-Allow-Headers") != "Authorization" {
		t.Fatalf("media-presentation preflight = %d %#v", resp.StatusCode, resp.Header)
	}
	resp = preflight("/v1/browser/artifact-mappings/query", "app://obsidian.md", http.MethodPost, "Authorization, Range")
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("artifact mapping query accepted overbroad header = %d %#v", resp.StatusCode, resp.Header)
	}
	resp = preflight("/v1/browser/threads", "https://evil.example", http.MethodGet, "Authorization")
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign-origin browser preflight = %d %#v", resp.StatusCode, resp.Header)
	}
	for _, path := range []string{"/v1/route/other/v1/status", "/v1/runtime/artifacts/start", "/v1/threads/rpc"} {
		resp = preflight(path, "app://obsidian.md", http.MethodPost, "Authorization, Content-Type")
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("non-browser route %s received CORS: %d %#v", path, resp.StatusCode, resp.Header)
		}
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+d.addr+"/v1/browser/threads", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "app://obsidian.md")
	resp = do(req)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Access-Control-Allow-Origin") != "app://obsidian.md" {
		t.Fatalf("actual request bypassed bearer auth or hid auth error: %d %#v", resp.StatusCode, resp.Header)
	}
	req, err = http.NewRequest(http.MethodGet, "http://"+d.addr+"/v1/browser/threads", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "app://obsidian.md")
	req.Header.Set("Authorization", "Bearer "+d.token)
	resp = do(req)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Access-Control-Allow-Origin") != "app://obsidian.md" {
		t.Fatalf("authenticated actual browser request = %d %#v", resp.StatusCode, resp.Header)
	}
	wantSchema := []byte(fmt.Sprintf(`"schema":%d`, api.SchemaVersion))
	if body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err != nil || !bytes.Contains(body, wantSchema) {
		t.Fatalf("authenticated browser response body = %q, want %s, err=%v", body, wantSchema, err)
	}
}

func testBrowserWS(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	d := startStrictAPIDaemon(t, nil)
	rc := client.NewRemote(d.addr, d.token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := rc.ThreadNew(ctx, api.NewThreadRequest{Agent: "pi", Name: "browser-ws", Cwd: "/tmp", Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := created.Thread.AgentSessionID
	dir := filepath.Join(d.home, "tmp", "pi-rpc-sockets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, sessionID+".live.sock")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = os.Remove(sock) })
	seen := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		seen <- line
		_, _ = conn.Write([]byte("{\"protocol\":\"live-v1\",\"type\":\"response\",\"id\":\"one\",\"ok\":true}\n"))
	}()

	mintTicket := func(request api.WSTicketRequest) (api.WSTicketResponse, int, []byte) {
		t.Helper()
		body, _ := json.Marshal(request)
		resp := browserRequest(t, d, http.MethodPost, "/v1/ws-ticket", body)
		defer resp.Body.Close()
		responseBody, _ := io.ReadAll(resp.Body)
		var ticket api.WSTicketResponse
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(responseBody, &ticket); err != nil {
				t.Fatal(err)
			}
		}
		return ticket, resp.StatusCode, responseBody
	}
	ticket, status, responseBody := mintTicket(api.WSTicketRequest{ThreadID: created.Thread.ID, Route: "rpc", Protocol: "live-v1"})
	if status != http.StatusOK {
		t.Fatalf("mint status %d: %s", status, responseBody)
	}
	wsURL := "ws://" + d.addr + ticket.Path
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := `{"protocol":"live-v1","id":"one","op":"snapshot"}`
	if err := ws.Write(ctx, websocket.MessageText, []byte(request)); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-seen:
		if strings.Contains(line, "subscribe") || strings.TrimSpace(line) != request {
			t.Fatalf("live-v1 Unix input = %q; want exact request and no auto-subscribe", line)
		}
	case <-ctx.Done():
		t.Fatal("live-v1 request never reached the Unix socket")
	}
	_, frame, err := ws.Read(ctx)
	if err != nil || !bytes.Contains(frame, []byte(`"ok":true`)) {
		t.Fatalf("live-v1 output = %s, err=%v", frame, err)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "done")
	_, reuse, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("single-use ticket was reusable")
	}
	if reuse == nil || reuse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ticket reuse response=%v err=%v", reuse, err)
	}
	b, _ := io.ReadAll(reuse.Body)
	reuse.Body.Close()
	if bytes.Contains(b, []byte(ticket.Ticket)) {
		t.Fatal("ticket credential was echoed in an error")
	}

	// Ticket URLs are an exact capability shape: an appended terminal dimension is
	// not silently ignored, and the malformed attempt consumes the grant.
	ambiguous, status, responseBody := mintTicket(api.WSTicketRequest{ThreadID: created.Thread.ID, Route: "rpc", Protocol: "live-v1"})
	if status != http.StatusOK {
		t.Fatalf("ambiguous ticket mint status %d: %s", status, responseBody)
	}
	ambiguousURL := "ws://" + d.addr + ambiguous.Path + "&extra=1"
	_, ambiguousResp, err := websocket.Dial(ctx, ambiguousURL, nil)
	if err == nil || ambiguousResp == nil || ambiguousResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("extra-query ticket response=%v err=%v", ambiguousResp, err)
	}
	ambiguousResp.Body.Close()
	_, consumedResp, err := websocket.Dial(ctx, "ws://"+d.addr+ambiguous.Path, nil)
	if err == nil || consumedResp == nil || consumedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("extra-query attempt did not consume ticket: response=%v err=%v", consumedResp, err)
	}
	consumedResp.Body.Close()

	nonCanonical := strings.ReplaceAll(created.Thread.ID, "-", "")
	_, status, _ = mintTicket(api.WSTicketRequest{ThreadID: nonCanonical, Route: "rpc", Protocol: "live-v1"})
	if status != http.StatusBadRequest {
		t.Fatalf("non-canonical ticket id status = %d, want 400", status)
	}
}

func testBrowserHubHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	peer := startStrictAPIDaemon(t, nil)
	peerClient := client.NewRemote(peer.addr, peer.token)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	created, err := peerClient.ThreadNew(ctx, api.NewThreadRequest{Agent: "pi", Name: "remote-old", Cwd: "/tmp", Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	history, err := peerClient.ThreadNew(ctx, api.NewThreadRequest{Agent: "claude", Name: "remote-stopped-history", Cwd: "/tmp", Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	historyPath := claude.TranscriptPath(filepath.Join(peer.home, "user-home", ".claude"), history.Thread.Cwd, history.Thread.AgentSessionID)
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	historyLines := []string{
		`{"type":"user","message":{"content":"one"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"two"}]}}`,
		`{"type":"user","message":{"content":"three"}}`,
	}
	if err := os.WriteFile(historyPath, []byte(strings.Join(historyLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Real owner-side terminal fixture without claiming a real Pi runtime: a real
	// isolated tmux pane runs plain `cat` and carries the canonical Sesh pane marker.
	terminalSession := "browser-terminal-" + created.Thread.ID[:8]
	newPane := exec.Command("tmux", "-L", peer.socket, "-f", "/dev/null", "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", terminalSession, "cat")
	newPane.Env = peer.env
	paneOut, err := newPane.CombinedOutput()
	if err != nil {
		t.Fatalf("create isolated terminal pane: %v\n%s", err, paneOut)
	}
	paneID := strings.TrimSpace(string(paneOut))
	markPane := exec.Command("tmux", "-L", peer.socket, "set-option", "-p", "-t", paneID, "@sesh-thread-id", created.Thread.ID)
	markPane.Env = peer.env
	if out, err := markPane.CombinedOutput(); err != nil {
		t.Fatalf("mark isolated terminal pane: %v\n%s", err, out)
	}
	hub := startStrictAPIDaemon(t, func(home, _ string) error {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[browser_hub]\nenabled = true\ntrusted_peers = [\""+peer.machine+"\"]\n"), 0o600); err != nil {
			return err
		}
		reg := peers.Registry{Peers: map[string]peers.Peer{peer.machine: {Machine: peer.machine, ApiAddr: peer.addr, ApiToken: peer.token}}}
		return reg.Save(filepath.Join(home, "peers.json"))
	})

	var page api.BrowserThreadPage
	if !waitUntil(10*time.Second, func() bool {
		resp := browserRequest(t, hub, http.MethodGet, "/v1/browser/threads?archived=include", nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&page) != nil {
			return false
		}
		for _, row := range page.Threads {
			if row.ID == created.Thread.ID && row.Machine == peer.machine {
				return true
			}
		}
		return false
	}) {
		meshResp := browserRequest(t, hub, http.MethodGet, "/v1/mesh", nil)
		meshBody, _ := io.ReadAll(meshResp.Body)
		meshResp.Body.Close()
		peersResp := browserRequest(t, hub, http.MethodGet, "/v1/peers", nil)
		peersBody, _ := io.ReadAll(peersResp.Body)
		peersResp.Body.Close()
		t.Fatalf("browser hub never listed replicated peer thread: %#v\nmesh: %s\npeers: %s", page, meshBody, peersBody)
	}
	transcriptResp := browserRequest(t, hub, http.MethodGet, "/v1/browser/threads/transcript?id="+url.QueryEscape(history.Thread.ID)+"&tail=2", nil)
	if transcriptResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(transcriptResp.Body)
		transcriptResp.Body.Close()
		t.Fatalf("remote stopped transcript status %d: %s", transcriptResp.StatusCode, b)
	}
	var transcript api.BrowserTranscriptResponse
	if err := json.NewDecoder(transcriptResp.Body).Decode(&transcript); err != nil {
		transcriptResp.Body.Close()
		t.Fatal(err)
	}
	transcriptResp.Body.Close()
	if transcript.Format != "native-jsonl" || len(transcript.Lines) != 2 || transcript.Lines[0] != historyLines[1] || transcript.Lines[1] != historyLines[2] {
		t.Fatalf("remote stopped transcript = %#v", transcript)
	}
	if transcript.Coverage.Source != "resolved-current-session-file" || transcript.Coverage.RequestedTail != 2 || transcript.Coverage.StartLine != 1 || transcript.Coverage.EndLine != 3 || transcript.Coverage.TotalLines != 3 || transcript.Coverage.Complete || transcript.Coverage.ByteLimited {
		t.Fatalf("remote stopped transcript coverage = %#v", transcript.Coverage)
	}

	action, _ := json.Marshal(api.BrowserThreadActionRequest{ThreadID: created.Thread.ID, Action: "rename", Name: ptr("remote-new")})
	resp := browserRequest(t, hub, http.MethodPost, "/v1/browser/threads/action", action)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("browser hub action status %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	list, err := peerClient.ThreadList(ctx, true, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, th := range list.Threads {
		if th.ID == created.Thread.ID && th.Name == "remote-new" {
			found = true
		}
	}
	if !found {
		t.Fatal("allowlisted browser rename did not land on the real peer daemon")
	}

	// A canonical owner-side refusal must pass back unchanged, without exposing the
	// peer credential or turning the browser seam into a generic route.
	selfParent, _ := json.Marshal(api.BrowserThreadActionRequest{ThreadID: created.Thread.ID, Action: "reparent", Parent: ptr(created.Thread.ID)})
	resp = browserRequest(t, hub, http.MethodPost, "/v1/browser/threads/action", selfParent)
	errorBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(errorBody, []byte("cannot be its own parent")) {
		t.Fatalf("remote canonical refusal = %d %s", resp.StatusCode, errorBody)
	}
	if bytes.Contains(errorBody, []byte(peer.token)) {
		t.Fatal("browser response exposed the peer API token")
	}

	// Mint at the hub, then traverse hub ticket auth -> owner resolution -> real HTTP
	// peer bearer auth -> peer terminal handler -> real isolated tmux/pty -> WebSocket.
	ticketBody, _ := json.Marshal(api.WSTicketRequest{ThreadID: created.Thread.ID, Route: "terminal", Protocol: "terminal-v1"})
	resp = browserRequest(t, hub, http.MethodPost, "/v1/ws-ticket", ticketBody)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("remote terminal ticket status %d: %s", resp.StatusCode, b)
	}
	var terminalTicket api.WSTicketResponse
	if err := json.NewDecoder(resp.Body).Decode(&terminalTicket); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	terminalCtx, terminalCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer terminalCancel()
	terminalWS, wsResp, err := websocket.Dial(terminalCtx, "ws://"+hub.addr+terminalTicket.Path, nil)
	if err != nil {
		if wsResp != nil {
			b, _ := io.ReadAll(wsResp.Body)
			wsResp.Body.Close()
			t.Fatalf("remote terminal websocket: %v: %s", err, b)
		}
		t.Fatal(err)
	}
	defer terminalWS.CloseNow() //nolint:errcheck
	resize := []byte(`{"type":"resize","cols":91,"rows":37}`)
	if err := terminalWS.Write(terminalCtx, websocket.MessageText, resize); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(5*time.Second, func() bool {
		cmd := exec.Command("tmux", "-L", peer.socket, "list-clients", "-F", "#{client_width}x#{client_height}")
		cmd.Env = peer.env
		out, err := cmd.Output()
		return err == nil && strings.Contains(string(out), "91x37")
	}) {
		cmd := exec.Command("tmux", "-L", peer.socket, "list-clients", "-F", "#{client_width}x#{client_height}")
		cmd.Env = peer.env
		out, _ := cmd.CombinedOutput()
		t.Fatalf("authenticated post-open resize did not reach owner pty; clients=%q", out)
	}
	marker := "REMOTE-TERMINAL-HTTP-OK"
	if err := terminalWS.Write(terminalCtx, websocket.MessageBinary, []byte(marker+"\n")); err != nil {
		t.Fatal(err)
	}
	var terminalOutput []byte
	for !bytes.Contains(terminalOutput, []byte(marker)) {
		_, frame, err := terminalWS.Read(terminalCtx)
		if err != nil {
			t.Fatalf("read remote terminal output: %v; output=%q", err, terminalOutput)
		}
		terminalOutput = append(terminalOutput, frame...)
		if len(terminalOutput) > 1<<20 {
			t.Fatalf("remote terminal emitted over 1 MiB without marker")
		}
	}

	// The generic route remains absent from the TCP surface even with browser-hub opt-in.
	resp = browserRequest(t, hub, http.MethodGet, "/v1/route/"+url.PathEscape(peer.machine)+"/v1/status", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("TCP /v1/route status = %d, want 404", resp.StatusCode)
	}
}

func ptr(s string) *string { return &s }

func testBrowserStoredHistoryNative(t *testing.T) {
	t.Skip("NOT IMPLEMENTED: real source Pi --session-history-json is not wired into the isolated Sesh conformance launcher; Sesh fake-executable/real-HTTP-peer contract tests are green, but a conformant matrix cell requires the real native metadata mode")
}

func testBrowserStoredArtifactsNative(t *testing.T) {
	t.Skip("NOT IMPLEMENTED: real source Pi stopped-artifact metadata/byte CLIs are not yet wired into the isolated Sesh conformance launcher; Sesh argv, buffering, validation, stale-replacement, and real HTTP-peer contract tests are green")
}

func testBrowserStoredInputMediaNative(t *testing.T) {
	t.Skip("NOT IMPLEMENTED: real source Pi stopped presentation/input-image metadata/byte CLIs are not yet wired into the isolated Sesh conformance launcher; Sesh exact-argv, no-side-effect, reassociation, whole-byte validation, CORS, and real HTTP-peer contract tests are green")
}

func testBrowserStoredMediaPresentationV2Native(t *testing.T) {
	t.Skip("NOT IMPLEMENTED: real source Pi stopped media-presentation version-2 CLI is not yet wired into the isolated Sesh conformance launcher; Sesh exact-v2 argv, v1 preservation, no-fallback, strict envelope, CORS, source revalidation, and authenticated owner/hub fixture tests are green")
}

func testBrowserLiveInputMediaNative(t *testing.T) {
	t.Skip("NOT IMPLEMENTED: the isolated Sesh conformance launcher does not yet start the final pi-rpc-socket .input.sock bridge and atomic input_image_reader marker; Sesh active-only routing, exact generation/target validation, strict whole-byte verification, and real authenticated owner/hub tests are green")
}

func testBrowserTransfersNative(t *testing.T) {
	t.Skip("NOT IMPLEMENTED: real Pi/socket attachment lease consumption and artifact publication are not yet wired into the isolated Sesh conformance launcher; Sesh durable-store and real HTTP-peer proofs are green")
}
