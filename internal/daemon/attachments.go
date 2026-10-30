package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/transfers"
)

const transferMetadataBodyLimit = 64 << 10
const transferRequestTimeout = 15 * time.Second
const browserArtifactCacheControl = "private, no-store"

func transferTimed(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), transferRequestTimeout)
		defer cancel()
		stopClose := context.AfterFunc(ctx, func() { _ = r.Body.Close() })
		defer stopClose()
		handler(w, r.WithContext(ctx))
	}
}

func (d *Daemon) routesBrowserTransfers(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/browser/attachments/start", transferTimed(d.handleBrowserAttachmentStart))
	mux.HandleFunc("GET /v1/browser/attachments", transferTimed(d.handleBrowserAttachmentStatus))
	mux.HandleFunc("PUT /v1/browser/attachments/chunk", transferTimed(d.handleBrowserAttachmentChunk))
	mux.HandleFunc("POST /v1/browser/attachments/complete", transferTimed(d.handleBrowserAttachmentComplete))
	mux.HandleFunc("POST /v1/browser/attachments/prepare", transferTimed(d.handleBrowserAttachmentPrepare))
	mux.HandleFunc("DELETE /v1/browser/attachments", transferTimed(d.handleBrowserAttachmentDelete))
	mux.HandleFunc("GET /v1/browser/artifacts", transferTimed(d.handleBrowserArtifactDownload))
	mux.HandleFunc("HEAD /v1/browser/artifacts", transferTimed(d.handleBrowserArtifactDownload))
	d.routesArtifactMappings(mux)
}

func (d *Daemon) routesRuntimeTransfers(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/runtime/artifacts/start", transferTimed(d.handleRuntimeArtifactStart))
	mux.HandleFunc("PUT /v1/runtime/artifacts/content", transferTimed(d.handleRuntimeArtifactContent))
	d.routesRuntimeArtifactMappings(mux)
}

func decodeTransferJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, transferMetadataBodyLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return fmt.Errorf("decode transfer request: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode transfer request: trailing JSON data")
	}
	return nil
}

func writeTransferError(w http.ResponseWriter, status int, code, message string) {
	if len(message) > 512 {
		message = message[:512]
	}
	writeJSON(w, status, api.BrowserTransferErrorResponse{Schema: api.SchemaVersion, Status: "error", Error: api.BrowserTransferError{Code: code, Message: message}})
}

func transferStatus(err error) int {
	switch transfers.ErrorCode(err) {
	case "invalid_request", "invalid_chunk", "invalid_publication", "invalid_failure", "digest_mismatch", "byte_count_mismatch":
		return http.StatusBadRequest
	case "attachment_not_found", "artifact_not_found", "mapping_not_found":
		return http.StatusNotFound
	case "quota_exceeded", "mapping_capacity":
		return http.StatusInsufficientStorage
	case "unsupported_runtime_transfer":
		return http.StatusNotImplemented
	case "stale_target", "wrong_owner", "unsupported_agent", "mapping_conflict", "terminal_state_conflict", "upload_conflict", "upload_expired", "upload_not_ready", "artifact_not_ready", "live_lease_conflict":
		return http.StatusConflict
	case "storage_corrupt", "storage_error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeTransferStoreError(w http.ResponseWriter, err error) {
	code := transfers.ErrorCode(err)
	message := err.Error()
	if code == "storage_error" || code == "storage_corrupt" {
		message = "owner managed transfer storage is unavailable or corrupt"
	}
	writeTransferError(w, transferStatus(err), code, message)
}

func browserTransferAuthority(r *http.Request) string {
	if authority, ok := r.Context().Value(browserAuthorityContextKey{}).(string); ok && authority != "" {
		return authority
	}
	return "local-unix"
}

func (d *Daemon) attachmentForAuthority(r *http.Request, threadID, id string) (transfers.Attachment, error) {
	a, err := d.transfers.Attachment(threadID, id)
	if err != nil {
		return transfers.Attachment{}, err
	}
	if a.AuthorityID != browserTransferAuthority(r) {
		return transfers.Attachment{}, &transfers.Error{Code: "attachment_not_found", Msg: "attachment not found for authenticated authority"}
	}
	return a, nil
}

func attachmentResponse(a transfers.Attachment) api.BrowserAttachmentResponse {
	return api.BrowserAttachmentResponse{Schema: api.SchemaVersion, Attachment: api.BrowserAttachmentInfo{ID: a.ID, ThreadID: a.ThreadID, OwnerMachine: a.OwnerMachine, Name: a.Name, MediaType: a.MediaType, Size: a.Size, SHA256: a.SHA256, State: a.State, NextOffset: a.NextOffset, ChunkSize: transfers.ChunkSize, ExpiresAtUnixMS: a.ExpiresUnixMS}}
}

func targetFromAPI(t api.TransferTarget) transfers.Target {
	return transfers.Target{StreamID: t.StreamID, SessionID: t.SessionID, SessionEpoch: t.SessionEpoch}
}

func targetToAPI(t transfers.Target) api.TransferTarget {
	return api.TransferTarget{StreamID: t.StreamID, SessionID: t.SessionID, SessionEpoch: t.SessionEpoch}
}

func (d *Daemon) transferOwner(w http.ResponseWriter, threadID string) (string, bool) {
	if !validCanonicalUUID(threadID) {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", "thread_id must be a canonical UUID")
		return "", false
	}
	owner, err := d.browserThreadOwner(threadID)
	if err != nil {
		status, code := storedHistoryOwnerError(err)
		writeTransferError(w, status, code, err.Error())
		return "", false
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		status, code := storedHistoryOwnerError(err)
		writeTransferError(w, status, code, err.Error())
		return "", false
	}
	return owner, true
}

func (d *Daemon) handleBrowserAttachmentStart(w http.ResponseWriter, r *http.Request) {
	var req api.BrowserAttachmentStartRequest
	if err := decodeTransferJSON(w, r, &req); err != nil {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	owner, ok := d.transferOwner(w, req.ThreadID)
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		body, _ := json.Marshal(req)
		d.serveBrowserTransferJSON(w, r, owner, http.MethodPost, "/v1/browser/attachments/start", nil, body)
		return
	}
	a, err := d.transfers.StartAttachment(transfers.StartAttachment{ThreadID: req.ThreadID, ClientID: req.ClientID, AuthorityID: browserTransferAuthority(r), Name: req.Name, MediaType: req.MediaType, Size: req.Size, SHA256: req.SHA256})
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attachmentResponse(a))
}

func exactTransferQuery(r *http.Request, keys ...string) (url.Values, error) {
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	q := r.URL.Query()
	for k, v := range q {
		if !allowed[k] || len(v) != 1 || v[0] == "" {
			return nil, fmt.Errorf("query requires exactly one %s", strings.Join(keys, ", "))
		}
	}
	for _, k := range keys {
		if len(q[k]) != 1 || q.Get(k) == "" {
			return nil, fmt.Errorf("query requires exactly one %s", strings.Join(keys, ", "))
		}
	}
	return q, nil
}

func (d *Daemon) handleBrowserAttachmentStatus(w http.ResponseWriter, r *http.Request) {
	q, err := exactTransferQuery(r, "thread_id", "id")
	if err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	owner, ok := d.transferOwner(w, q.Get("thread_id"))
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		d.serveBrowserTransferJSON(w, r, owner, http.MethodGet, "/v1/browser/attachments", q, nil)
		return
	}
	a, err := d.attachmentForAuthority(r, q.Get("thread_id"), q.Get("id"))
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, 200, attachmentResponse(a))
}

func (d *Daemon) handleBrowserAttachmentChunk(w http.ResponseWriter, r *http.Request) {
	q, err := exactTransferQuery(r, "thread_id", "id", "offset")
	if err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeTransferError(w, 400, "invalid_chunk", "offset must be a non-negative decimal integer")
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength < 1 || r.ContentLength > transfers.MaxChunkBytes || len(r.TransferEncoding) != 0 {
		writeTransferError(w, 400, "invalid_chunk", "chunk requires exact application/octet-stream Content-Length within 256 KiB")
		return
	}
	digests := r.Header.Values("X-Sesh-Chunk-SHA256")
	if len(digests) != 1 {
		writeTransferError(w, 400, "invalid_chunk", "exactly one X-Sesh-Chunk-SHA256 is required")
		return
	}
	owner, ok := d.transferOwner(w, q.Get("thread_id"))
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		d.serveBrowserChunk(w, r, owner, q, digests[0])
		return
	}
	if _, err := d.attachmentForAuthority(r, q.Get("thread_id"), q.Get("id")); err != nil {
		writeTransferStoreError(w, err)
		return
	}
	a, err := d.transfers.PutChunk(q.Get("thread_id"), q.Get("id"), offset, digests[0], r.Body, r.ContentLength)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, 200, attachmentResponse(a))
}

func (d *Daemon) serveBrowserChunk(w http.ResponseWriter, r *http.Request, owner string, q url.Values, digest string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, transfers.MaxChunkBytes+1))
	if err != nil || int64(len(body)) != r.ContentLength {
		writeTransferError(w, 400, "invalid_chunk", "chunk body length mismatch")
		return
	}
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeTransferError(w, browserOwnerStatus(err), "owner_routing_failed", err.Error())
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeTransferError(w, 502, "owner_routing_failed", "browser hub peer authentication is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeProxyTimeout)
	defer cancel()
	target := "http://" + peer.ApiAddr + "/v1/browser/attachments/chunk?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		writeTransferError(w, 500, "owner_internal_error", "could not build peer chunk request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Sesh-Chunk-SHA256", digest)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeTransferError(w, 502, "owner_routing_failed", "browser hub peer chunk request failed")
		return
	}
	copyTransferJSONResponse(w, resp)
}

func (d *Daemon) handleBrowserAttachmentComplete(w http.ResponseWriter, r *http.Request) {
	var req api.BrowserAttachmentMutationRequest
	if err := decodeTransferJSON(w, r, &req); err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	owner, ok := d.transferOwner(w, req.ThreadID)
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		body, _ := json.Marshal(req)
		d.serveBrowserTransferJSON(w, r, owner, http.MethodPost, "/v1/browser/attachments/complete", nil, body)
		return
	}
	if _, err := d.attachmentForAuthority(r, req.ThreadID, req.ID); err != nil {
		writeTransferStoreError(w, err)
		return
	}
	a, err := d.transfers.Complete(req.ThreadID, req.ID)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, 200, attachmentResponse(a))
}

func (d *Daemon) handleBrowserAttachmentDelete(w http.ResponseWriter, r *http.Request) {
	q, err := exactTransferQuery(r, "thread_id", "id")
	if err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	owner, ok := d.transferOwner(w, q.Get("thread_id"))
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		d.serveBrowserTransferJSON(w, r, owner, http.MethodDelete, "/v1/browser/attachments", q, nil)
		return
	}
	if _, err := d.attachmentForAuthority(r, q.Get("thread_id"), q.Get("id")); err != nil {
		if transfers.ErrorCode(err) == "attachment_not_found" {
			writeJSON(w, 200, api.BrowserAttachmentDeleteResponse{Schema: api.SchemaVersion, ThreadID: q.Get("thread_id"), ID: q.Get("id"), Deleted: false})
			return
		}
		writeTransferStoreError(w, err)
		return
	}
	deleted, err := d.transfers.DeleteAttachment(q.Get("thread_id"), q.Get("id"))
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, 200, api.BrowserAttachmentDeleteResponse{Schema: api.SchemaVersion, ThreadID: q.Get("thread_id"), ID: q.Get("id"), Deleted: deleted})
}

func (d *Daemon) handleBrowserAttachmentPrepare(w http.ResponseWriter, r *http.Request) {
	var req api.BrowserAttachmentPrepareRequest
	if err := decodeTransferJSON(w, r, &req); err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	owner, ok := d.transferOwner(w, req.ThreadID)
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		body, _ := json.Marshal(req)
		d.serveBrowserTransferJSON(w, r, owner, http.MethodPost, "/v1/browser/attachments/prepare", nil, body)
		return
	}
	thread, err := d.store.GetThread(req.ThreadID)
	if err != nil {
		writeTransferError(w, 404, "thread_not_found", "thread not found")
		return
	}
	if thread.AgentKind != "pi" {
		writeTransferError(w, 409, "unsupported_agent", "attachments require a Pi thread")
		return
	}
	if thread.AgentSessionID == "" || thread.AgentSessionID != req.Target.SessionID {
		writeTransferError(w, 409, "stale_target", "thread session does not match prepare target")
		return
	}
	for _, id := range req.AttachmentIDs {
		if _, err := d.attachmentForAuthority(r, req.ThreadID, id); err != nil {
			writeTransferStoreError(w, err)
			return
		}
	}
	rec, err := d.transfers.Prepare(req.ThreadID, req.RequestID, targetFromAPI(req.Target), req.AttachmentIDs)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, 200, api.BrowserAttachmentPrepareResponse{Schema: api.SchemaVersion, ThreadID: req.ThreadID, OwnerMachine: d.cfg.Machine, RequestID: req.RequestID, LeaseIDs: rec.LeaseIDs, ExpiresAtUnixMS: rec.ExpiresUnixMS})
}

func (d *Daemon) handleRuntimeArtifactStart(w http.ResponseWriter, r *http.Request) {
	var req api.RuntimeArtifactStartRequest
	if err := decodeTransferJSON(w, r, &req); err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	thread, err := d.store.GetThread(req.ThreadID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeTransferError(w, 404, "thread_not_found", "thread not found")
		} else {
			writeTransferError(w, 500, "storage_error", "thread lookup failed")
		}
		return
	}
	if thread.AgentKind != "pi" {
		writeTransferError(w, 409, "unsupported_agent", "artifacts require a Pi thread")
		return
	}
	target := targetFromAPI(req.Target)
	if thread.AgentSessionID == "" || thread.AgentSessionID != target.SessionID {
		writeTransferError(w, 409, "stale_target", "thread session does not match artifact target")
		return
	}
	if err := d.transfers.ValidateConsumer(req.ThreadID, target, "artifact-publish-v1"); err != nil {
		writeTransferStoreError(w, err)
		return
	}
	a, err := d.transfers.StartArtifact(transfers.StartArtifact{ThreadID: req.ThreadID, ClientID: req.ClientID, Target: target, Name: req.Name, MediaType: req.MediaType, Size: req.Size, SHA256: req.SHA256, Disposition: req.Disposition})
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, 200, artifactResponse(a))
}

func artifactResponse(a transfers.Artifact) api.BrowserArtifactResponse {
	return api.BrowserArtifactResponse{Schema: api.SchemaVersion, Artifact: api.BrowserArtifactInfo{ID: a.ID, ThreadID: a.ThreadID, Target: targetToAPI(a.Target), Name: a.Name, MediaType: a.MediaType, Size: a.Size, SHA256: a.SHA256, Disposition: a.Disposition, ExpiresAtUnixMS: a.ExpiresUnixMS}}
}

func (d *Daemon) handleRuntimeArtifactContent(w http.ResponseWriter, r *http.Request) {
	q, err := exactTransferQuery(r, "thread_id", "id")
	if err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength < 0 || r.ContentLength > transfers.MaxArtifactBytes || len(r.TransferEncoding) != 0 {
		writeTransferError(w, 400, "invalid_request", "artifact content requires exact bounded application/octet-stream Content-Length")
		return
	}
	a, err := d.transfers.ArtifactRecord(q.Get("thread_id"), q.Get("id"))
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	thread, err := d.store.GetThread(a.ThreadID)
	if err != nil || thread.AgentSessionID != a.Target.SessionID {
		writeTransferError(w, 409, "stale_target", "artifact target is no longer current")
		return
	}
	if err := d.transfers.ValidateConsumer(a.ThreadID, a.Target, "artifact-publish-v1"); err != nil {
		writeTransferStoreError(w, err)
		return
	}
	a, err = d.transfers.PutArtifact(a.ThreadID, a.ID, r.Body, r.ContentLength)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, 200, artifactResponse(a))
}

func (d *Daemon) handleBrowserArtifactDownload(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Range"), ",") {
		writeTransferError(w, http.StatusRequestedRangeNotSatisfiable, "invalid_range", "only one byte range is supported")
		return
	}
	q, err := exactTransferQuery(r, "thread_id", "id")
	if err != nil {
		writeTransferError(w, 400, "invalid_request", err.Error())
		return
	}
	owner, ok := d.transferOwner(w, q.Get("thread_id"))
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		d.serveBrowserArtifactPeer(w, r, owner, q)
		return
	}
	d.serveLocalArtifact(w, r, q.Get("thread_id"), q.Get("id"))
}

func (d *Daemon) serveLocalArtifact(w http.ResponseWriter, r *http.Request, threadID, id string) {
	w.Header().Set("Cache-Control", browserArtifactCacheControl)
	a, path, err := d.transfers.Artifact(threadID, id)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	f, err := safeArtifactFile(path)
	if err != nil {
		writeTransferError(w, 500, "storage_corrupt", "artifact payload is unavailable")
		return
	}
	defer f.Close()
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		fi, _ := f.Stat()
		if fi == nil || !validSingleByteRange(rangeHeader, fi.Size()) {
			writeTransferError(w, http.StatusRequestedRangeNotSatisfiable, "invalid_range", "byte range is invalid or unsatisfiable")
			return
		}
	}
	w.Header().Set("Content-Type", a.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", "\"sha256-"+a.SHA256+"\"")
	w.Header().Set("Content-Disposition", contentDisposition(a.Disposition, a.Name))
	http.ServeContent(w, r, a.Name, time.UnixMilli(a.UpdatedUnixMS), f)
}

func safeArtifactFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > transfers.MaxArtifactBytes {
		_ = f.Close()
		return nil, fmt.Errorf("invalid artifact file")
	}
	return f, nil
}

func validSingleByteRange(value string, size int64) bool {
	if size < 0 || !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return false
	}
	raw := strings.TrimPrefix(value, "bytes=")
	left, right, ok := strings.Cut(raw, "-")
	if !ok || (left == "" && right == "") {
		return false
	}
	if left == "" {
		suffix, err := strconv.ParseInt(right, 10, 64)
		return err == nil && suffix > 0 && size > 0
	}
	start, err := strconv.ParseInt(left, 10, 64)
	if err != nil || start < 0 || start >= size {
		return false
	}
	if right == "" {
		return true
	}
	end, err := strconv.ParseInt(right, 10, 64)
	return err == nil && end >= start
}

func contentDisposition(disposition, name string) string {
	return disposition + `; filename="artifact"; filename*=UTF-8''` + url.PathEscape(name)
}

func (d *Daemon) serveBrowserArtifactPeer(w http.ResponseWriter, r *http.Request, owner string, q url.Values) {
	w.Header().Set("Cache-Control", browserArtifactCacheControl)
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeTransferError(w, browserOwnerStatus(err), "owner_routing_failed", err.Error())
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeTransferError(w, 502, "owner_routing_failed", "browser hub peer authentication is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeProxyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, "http://"+peer.ApiAddr+"/v1/browser/artifacts?"+q.Encode(), nil)
	if err != nil {
		writeTransferError(w, 500, "owner_internal_error", "could not build artifact request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if v := r.Header.Get("Range"); v != "" {
		req.Header.Set("Range", v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeTransferError(w, 502, "owner_routing_failed", "artifact peer request failed")
		return
	}
	if resp.StatusCode >= 400 {
		copyTransferJSONResponse(w, resp)
		return
	}
	defer resp.Body.Close()
	if resp.ContentLength > transfers.MaxArtifactBytes {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "artifact peer response exceeded its limit")
		return
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition", "ETag", "Last-Modified", "X-Content-Type-Options"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, io.LimitReader(resp.Body, transfers.MaxArtifactBytes))
}

func (d *Daemon) startTransferSweeper() {
	_, _ = d.transfers.Sweep(time.Now(), transfers.SweepLimit)
	d.transferWG.Add(1)
	go func() {
		defer d.transferWG.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				_, _ = d.transfers.Sweep(now, transfers.SweepLimit)
			case <-d.transferStop:
				return
			}
		}
	}()
}

func (d *Daemon) stopTransferSweeper() {
	select {
	case <-d.transferStop:
	default:
		close(d.transferStop)
	}
	d.transferWG.Wait()
}

func (d *Daemon) serveBrowserTransferJSON(w http.ResponseWriter, r *http.Request, owner, method, path string, query url.Values, body []byte) {
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeTransferError(w, browserOwnerStatus(err), "owner_routing_failed", err.Error())
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "browser hub peer authentication is unavailable")
		return
	}
	target := "http://" + peer.ApiAddr + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeProxyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		writeTransferError(w, http.StatusInternalServerError, "owner_internal_error", "could not build transfer peer request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "browser hub transfer peer request failed")
		return
	}
	copyTransferJSONResponse(w, resp)
}

func copyTransferJSONResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, browserMaxRequestBody+1))
	if err != nil || len(body) > browserMaxRequestBody {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "browser hub transfer peer response exceeded its limit")
		return
	}
	var envelope struct {
		Schema int                       `json:"schema"`
		Status string                    `json:"status,omitempty"`
		Error  *api.BrowserTransferError `json:"error,omitempty"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Schema != api.SchemaVersion || (resp.StatusCode >= 400 && (envelope.Status != "error" || envelope.Error == nil)) {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "browser hub peer did not return the transfer protocol")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
