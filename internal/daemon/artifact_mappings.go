package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/transfers"
)

const browserArtifactMappingsResponseLimit = 256 << 10

func (d *Daemon) routesArtifactMappings(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/browser/artifact-mappings/query", transferTimed(d.handleBrowserArtifactMappingsQuery))
}

func (d *Daemon) routesRuntimeArtifactMappings(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/runtime/artifact-mappings/resolve", transferTimed(d.handleRuntimeArtifactMappingResolve))
	mux.HandleFunc("PUT /v1/runtime/artifact-mappings/content", transferTimed(d.handleRuntimeArtifactMappingContent))
	mux.HandleFunc("POST /v1/runtime/artifact-mappings/fail", transferTimed(d.handleRuntimeArtifactMappingFail))
}

func (d *Daemon) handleRuntimeArtifactMappingResolve(w http.ResponseWriter, r *http.Request) {
	var req api.RuntimeArtifactMappingResolveRequest
	if err := decodeArtifactMappingJSON(w, r, &req); err != nil {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !d.validateRuntimeArtifactMappingThread(w, req.ThreadID, req.NativeSession, req.ConsumerTarget) {
		return
	}
	mapping, err := d.transfers.ResolveArtifactMapping(transfers.ResolveArtifactMapping{
		ThreadID: req.ThreadID, ConsumerTarget: targetFromAPI(req.ConsumerTarget), NativeSessionID: req.NativeSession,
		PublicationID: req.Publication.PublicationID,
		Metadata: transfers.ArtifactMetadata{
			Kind: req.Publication.Kind, Name: req.Publication.Name, MediaType: req.Publication.MediaType,
			Size: req.Publication.Size, SHA256: req.Publication.SHA256, Disposition: req.Publication.Disposition,
			Retention: req.Publication.Retention, Origin: req.Publication.Origin,
		},
		Availability: transfers.MappingAvailability{Status: req.Publication.Availability.Status, Reason: req.Publication.Availability.Reason, Message: req.Publication.Availability.Message},
	})
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	response, err := d.runtimeArtifactMappingResponse(mapping)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (d *Daemon) handleRuntimeArtifactMappingContent(w http.ResponseWriter, r *http.Request) {
	query, err := exactTransferQuery(r, "thread_id", "mapping_id")
	if err != nil {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if r.Header.Get("Range") != "" || r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength < 0 || r.ContentLength > transfers.MaxArtifactBytes || len(r.TransferEncoding) != 0 {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", "artifact mapping content requires exact bounded application/octet-stream Content-Length without Range or chunked encoding")
		return
	}
	streamID, ok := exactHeader(r, "X-Sesh-Consumer-Stream-ID")
	if !ok {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", "exactly one X-Sesh-Consumer-Stream-ID is required")
		return
	}
	sessionID, ok := exactHeader(r, "X-Sesh-Consumer-Session-ID")
	if !ok {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", "exactly one X-Sesh-Consumer-Session-ID is required")
		return
	}
	epochRaw, ok := exactHeader(r, "X-Sesh-Consumer-Session-Epoch")
	if !ok {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", "exactly one X-Sesh-Consumer-Session-Epoch is required")
		return
	}
	epoch, err := strconv.ParseInt(epochRaw, 10, 64)
	if err != nil || epoch < 0 || strconv.FormatInt(epoch, 10) != epochRaw {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", "consumer session epoch must be a canonical non-negative decimal")
		return
	}
	target := api.TransferTarget{StreamID: streamID, SessionID: sessionID, SessionEpoch: epoch}
	if !d.validateRuntimeArtifactMappingThread(w, query.Get("thread_id"), sessionID, target) {
		return
	}
	mapping, err := d.transfers.PutArtifactMappingContent(query.Get("thread_id"), query.Get("mapping_id"), targetFromAPI(target), r.Body, r.ContentLength)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	response, err := d.runtimeArtifactMappingResponse(mapping)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (d *Daemon) handleRuntimeArtifactMappingFail(w http.ResponseWriter, r *http.Request) {
	var req api.RuntimeArtifactMappingFailRequest
	if err := decodeArtifactMappingJSON(w, r, &req); err != nil {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !d.validateRuntimeArtifactMappingThread(w, req.ThreadID, req.NativeSession, req.ConsumerTarget) {
		return
	}
	mapping, err := d.transfers.FailArtifactMapping(transfers.FailArtifactMapping{
		ThreadID: req.ThreadID, ConsumerTarget: targetFromAPI(req.ConsumerTarget), NativeSessionID: req.NativeSession,
		PublicationID: req.PublicationID,
		Failure:       transfers.MappingUnavailable{Source: req.Failure.Source, Code: req.Failure.Code, Message: req.Failure.Message},
	})
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	response, err := d.runtimeArtifactMappingResponse(mapping)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (d *Daemon) validateRuntimeArtifactMappingThread(w http.ResponseWriter, threadID, nativeSession string, target api.TransferTarget) bool {
	thread, err := d.store.GetThread(threadID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeTransferError(w, http.StatusNotFound, "thread_not_found", "thread not found")
		} else {
			writeTransferError(w, http.StatusInternalServerError, "storage_error", "thread lookup failed")
		}
		return false
	}
	if thread.Machine != d.cfg.Machine {
		writeTransferError(w, http.StatusConflict, "wrong_owner", "thread is not owned by this daemon")
		return false
	}
	if !piFamily(thread.AgentKind) {
		writeTransferError(w, http.StatusConflict, "unsupported_agent", "artifact mappings require a pi/mypi thread")
		return false
	}
	if thread.AgentSessionID == "" || thread.AgentSessionID != nativeSession || target.SessionID != nativeSession {
		writeTransferError(w, http.StatusConflict, "stale_target", "thread session does not match artifact mapping target")
		return false
	}
	return true
}

func exactHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	return firstExact(values)
}

func firstExact(values []string) (string, bool) {
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func (d *Daemon) runtimeArtifactMappingResponse(mapping transfers.ArtifactMapping) (api.RuntimeArtifactMappingResponse, error) {
	view := api.RuntimeArtifactMappingView{
		MappingID: mapping.MappingID, PublicationID: mapping.PublicationID,
		Status: mapping.State, UpdatedAtUnixMS: mapping.UpdatedUnixMS,
	}
	switch mapping.State {
	case "pending", "staging":
		target := targetToAPI(mapping.ProducerTarget)
		view.ProducerTarget = &target
		if mapping.ArtifactID != nil {
			view.ArtifactID = *mapping.ArtifactID
		}
	case "ready":
		target := targetToAPI(mapping.ProducerTarget)
		view.ProducerTarget = &target
		artifact, err := d.transfers.ArtifactForMapping(mapping)
		if err != nil {
			return api.RuntimeArtifactMappingResponse{}, err
		}
		info := artifactInfo(artifact)
		view.Artifact = &info
	case "unavailable":
		if mapping.Unavailable == nil {
			return api.RuntimeArtifactMappingResponse{}, &transfers.Error{Code: "storage_corrupt", Msg: "artifact mapping terminal state is invalid"}
		}
		view.Unavailable = &api.ArtifactMappingUnavailable{Source: mapping.Unavailable.Source, Code: mapping.Unavailable.Code, Message: mapping.Unavailable.Message}
	case "expired":
		if mapping.ArtifactID == nil {
			return api.RuntimeArtifactMappingResponse{}, &transfers.Error{Code: "storage_corrupt", Msg: "expired artifact mapping has no artifact identity"}
		}
		view.ArtifactID = *mapping.ArtifactID
		view.ExpiredAtUnixMS = mapping.ExpiredAtUnixMS
	default:
		return api.RuntimeArtifactMappingResponse{}, &transfers.Error{Code: "storage_corrupt", Msg: "artifact mapping state is invalid"}
	}
	return api.RuntimeArtifactMappingResponse{Schema: api.SchemaVersion, Protocol: transfers.ArtifactMappingProtocol, Version: transfers.ArtifactMappingVersion, Mapping: view}, nil
}

func artifactInfo(artifact transfers.Artifact) api.BrowserArtifactInfo {
	return api.BrowserArtifactInfo{
		ID: artifact.ID, ThreadID: artifact.ThreadID, Target: targetToAPI(artifact.Target), Name: artifact.Name,
		MediaType: artifact.MediaType, Size: artifact.Size, SHA256: artifact.SHA256,
		Disposition: artifact.Disposition, ExpiresAtUnixMS: artifact.ExpiresUnixMS,
	}
}

func (d *Daemon) handleBrowserArtifactMappingsQuery(w http.ResponseWriter, r *http.Request) {
	var req api.BrowserArtifactMappingsQueryRequest
	if err := decodeArtifactMappingJSON(w, r, &req); err != nil {
		writeTransferError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	owner, ok := d.transferOwner(w, req.ThreadID)
	if !ok {
		return
	}
	if owner != d.cfg.Machine {
		d.forwardBrowserArtifactMappingsQuery(w, r, owner, req)
		return
	}
	thread, err := d.store.GetThread(req.ThreadID)
	if err != nil {
		writeTransferError(w, http.StatusNotFound, "thread_not_found", "thread not found")
		return
	}
	if thread.Machine != d.cfg.Machine {
		writeTransferError(w, http.StatusConflict, "wrong_owner", "thread is not owned by this daemon")
		return
	}
	if !piFamily(thread.AgentKind) {
		writeTransferError(w, http.StatusConflict, "unsupported_agent", "artifact mappings require a pi/mypi thread")
		return
	}
	mappings, err := d.transfers.QueryArtifactMappings(req.ThreadID, req.NativeSession, req.PublicationIDs)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	response, err := d.browserArtifactMappingsResponse(req, mappings)
	if err != nil {
		writeTransferStoreError(w, err)
		return
	}
	writeArtifactMappingsJSON(w, http.StatusOK, response)
}

func (d *Daemon) browserArtifactMappingsResponse(req api.BrowserArtifactMappingsQueryRequest, mappings []*transfers.ArtifactMapping) (api.BrowserArtifactMappingsQueryResponse, error) {
	items := make([]api.BrowserArtifactMappingItem, 0, len(req.PublicationIDs))
	for i, publicationID := range req.PublicationIDs {
		mapping := mappings[i]
		if mapping == nil {
			items = append(items, api.BrowserArtifactMappingItem{PublicationID: publicationID, Status: "unmapped"})
			continue
		}
		item := api.BrowserArtifactMappingItem{PublicationID: publicationID, UpdatedAtUnixMS: mapping.UpdatedUnixMS}
		switch mapping.State {
		case "pending", "staging":
			item.Status = "pending"
		case "ready":
			artifact, err := d.transfers.ArtifactForMapping(*mapping)
			if err != nil {
				return api.BrowserArtifactMappingsQueryResponse{}, err
			}
			info := artifactInfo(artifact)
			item.Status = "ready"
			item.Artifact = &info
		case "unavailable":
			if mapping.Unavailable == nil {
				return api.BrowserArtifactMappingsQueryResponse{}, &transfers.Error{Code: "storage_corrupt", Msg: "artifact mapping terminal state is invalid"}
			}
			item.Status = "unavailable"
			item.Unavailable = &api.ArtifactMappingUnavailable{Source: mapping.Unavailable.Source, Code: mapping.Unavailable.Code, Message: mapping.Unavailable.Message}
		case "expired":
			if mapping.ArtifactID == nil {
				return api.BrowserArtifactMappingsQueryResponse{}, &transfers.Error{Code: "storage_corrupt", Msg: "expired artifact mapping has no artifact identity"}
			}
			item.Status = "expired"
			item.ArtifactID = *mapping.ArtifactID
			item.ExpiredAtUnixMS = mapping.ExpiredAtUnixMS
		default:
			return api.BrowserArtifactMappingsQueryResponse{}, &transfers.Error{Code: "storage_corrupt", Msg: "artifact mapping state is invalid"}
		}
		items = append(items, item)
	}
	return api.BrowserArtifactMappingsQueryResponse{
		Schema: api.SchemaVersion, Protocol: transfers.ArtifactMappingProtocol, Version: transfers.ArtifactMappingVersion,
		ThreadID: req.ThreadID, OwnerMachine: d.cfg.Machine, NativeSession: req.NativeSession, Mappings: items,
	}, nil
}

func writeArtifactMappingsJSON(w http.ResponseWriter, status int, response api.BrowserArtifactMappingsQueryResponse) {
	body, err := json.Marshal(response)
	if err != nil || len(body) > browserArtifactMappingsResponseLimit {
		writeTransferError(w, http.StatusInternalServerError, "storage_error", "artifact mapping response exceeds its safety limit")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func (d *Daemon) forwardBrowserArtifactMappingsQuery(w http.ResponseWriter, r *http.Request, owner string, request api.BrowserArtifactMappingsQueryRequest) {
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
	body, err := json.Marshal(request)
	if err != nil {
		writeTransferError(w, http.StatusInternalServerError, "owner_internal_error", "could not encode artifact mapping peer request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), transferRequestTimeout)
	defer cancel()
	peerReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+peer.ApiAddr+"/v1/browser/artifact-mappings/query", bytes.NewReader(body))
	if err != nil {
		writeTransferError(w, http.StatusInternalServerError, "owner_internal_error", "could not build artifact mapping peer request")
		return
	}
	peerReq.Header.Set("Authorization", "Bearer "+token)
	peerReq.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(peerReq)
	if err != nil {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "artifact mapping peer request failed")
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, browserArtifactMappingsResponseLimit+1))
	if err != nil || len(responseBody) > browserArtifactMappingsResponseLimit || !utf8.Valid(responseBody) {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "artifact mapping peer response exceeded its limit")
		return
	}
	if response.StatusCode >= 400 {
		var peerError api.BrowserTransferErrorResponse
		if strictJSON(responseBody, &peerError) != nil || peerError.Schema != api.SchemaVersion || peerError.Status != "error" || !safePeerArtifactMappingError(response.StatusCode, peerError.Error.Code) {
			writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "artifact mapping peer returned an invalid error envelope")
			return
		}
		writeTransferError(w, response.StatusCode, peerError.Error.Code, "owner artifact mapping request failed: "+peerError.Error.Code)
		return
	}
	var peerResponse api.BrowserArtifactMappingsQueryResponse
	if strictJSON(responseBody, &peerResponse) != nil || !validPeerArtifactMappingsResponse(peerResponse, request, owner) {
		writeTransferError(w, http.StatusBadGateway, "owner_routing_failed", "artifact mapping peer returned an invalid response envelope")
		return
	}
	writeArtifactMappingsJSON(w, http.StatusOK, peerResponse)
}

func decodeArtifactMappingJSON(w http.ResponseWriter, r *http.Request, value any) error {
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 || contentTypes[0] != "application/json" {
		return fmt.Errorf("artifact mapping request requires exactly one application/json Content-Type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, transferMetadataBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("decode artifact mapping request: %w", err)
	}
	if !utf8.Valid(body) {
		return fmt.Errorf("decode artifact mapping request: body is not valid UTF-8")
	}
	if err := strictJSON(body, value); err != nil {
		return fmt.Errorf("decode artifact mapping request: %w", err)
	}
	return nil
}

func strictJSON(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func validPeerArtifactMappingsResponse(response api.BrowserArtifactMappingsQueryResponse, request api.BrowserArtifactMappingsQueryRequest, owner string) bool {
	if response.Schema != api.SchemaVersion || response.Protocol != transfers.ArtifactMappingProtocol || response.Version != transfers.ArtifactMappingVersion || response.ThreadID != request.ThreadID || response.OwnerMachine != owner || response.NativeSession != request.NativeSession || len(response.Mappings) != len(request.PublicationIDs) {
		return false
	}
	for i, item := range response.Mappings {
		if item.PublicationID != request.PublicationIDs[i] {
			return false
		}
		switch item.Status {
		case "unmapped":
			if item.Artifact != nil || item.Unavailable != nil || item.ArtifactID != "" || item.ExpiredAtUnixMS != 0 || item.UpdatedAtUnixMS != 0 {
				return false
			}
		case "pending":
			if item.UpdatedAtUnixMS <= 0 || item.Artifact != nil || item.Unavailable != nil || item.ArtifactID != "" || item.ExpiredAtUnixMS != 0 {
				return false
			}
		case "ready":
			if item.UpdatedAtUnixMS <= 0 || item.Artifact == nil || item.Unavailable != nil || item.ArtifactID != "" || item.ExpiredAtUnixMS != 0 || !validPeerArtifact(*item.Artifact, request.ThreadID, request.NativeSession) {
				return false
			}
		case "unavailable":
			if item.UpdatedAtUnixMS <= 0 || item.Artifact != nil || item.Unavailable == nil || !validPeerMappingUnavailable(*item.Unavailable) || item.ArtifactID != "" || item.ExpiredAtUnixMS != 0 {
				return false
			}
		case "expired":
			if item.UpdatedAtUnixMS <= 0 || item.Artifact != nil || item.Unavailable != nil || !validCanonicalUUID(item.ArtifactID) || item.ExpiredAtUnixMS <= 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validPeerArtifact(artifact api.BrowserArtifactInfo, threadID, nativeSession string) bool {
	if !validCanonicalUUID(artifact.ID) || artifact.ThreadID != threadID || artifact.Target.SessionID != nativeSession || !validCanonicalUUID(artifact.Target.SessionID) || artifact.Target.StreamID == "" || len(artifact.Target.StreamID) > 128 || artifact.Target.SessionEpoch < 0 || artifact.Name == "" || len(artifact.Name) > 255 || filepath.Base(artifact.Name) != artifact.Name || strings.ContainsAny(artifact.Name, "/\\") || artifact.MediaType == "" || len(artifact.MediaType) > 127 || artifact.Size < 0 || artifact.Size > transfers.MaxArtifactBytes || !lowerHex64(artifact.SHA256) || (artifact.Disposition != "inline" && artifact.Disposition != "attachment") || artifact.ExpiresAtUnixMS <= 0 {
		return false
	}
	if !validPeerArtifactName(artifact.Name) || !validPeerMediaType(artifact.MediaType) {
		return false
	}
	return true
}

func lowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validPeerMappingUnavailable(unavailable api.ArtifactMappingUnavailable) bool {
	if !validPeerMappingText(unavailable.Message) {
		return false
	}
	switch unavailable.Source {
	case "native":
		return unavailable.Code == "source_not_retained" || unavailable.Code == "source_failed" || unavailable.Code == "invalid_bytes" || unavailable.Code == "byte_limit" || unavailable.Code == "lifecycle_ended"
	case "native_copy":
		return terminalPeerMappingCode(unavailable.Code)
	case "sesh":
		return unavailable.Code == "digest_unavailable" || terminalPeerMappingCode(unavailable.Code)
	default:
		return false
	}
}

func terminalPeerMappingCode(code string) bool {
	switch code {
	case "artifact_not_found", "artifact_unavailable", "source_failed", "byte_count_mismatch", "digest_mismatch", "invalid_limits", "quota_exceeded":
		return true
	default:
		return false
	}
}

func validPeerMappingText(value string) bool {
	if len(value) > transfers.MaxArtifactMappingMessageBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, char := range value {
		if (char < 0x20 && char != '\t') || char == 0x7f {
			return false
		}
	}
	return true
}

func validPeerArtifactName(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validPeerMediaType(value string) bool {
	for _, char := range []byte(value) {
		if char < 0x20 || char > 0x7e {
			return false
		}
	}
	_, _, err := mime.ParseMediaType(value)
	return err == nil
}

func safePeerArtifactMappingError(status int, code string) bool {
	switch code {
	case "invalid_request", "invalid_publication", "invalid_failure", "digest_mismatch", "byte_count_mismatch":
		return status == http.StatusBadRequest
	case "thread_not_found", "mapping_not_found", "artifact_not_found":
		return status == http.StatusNotFound
	case "owner_not_allowed":
		return status == http.StatusForbidden
	case "wrong_owner", "unsupported_agent", "stale_target", "mapping_conflict", "terminal_state_conflict", "artifact_not_ready", "upload_expired":
		return status == http.StatusConflict
	case "unsupported_runtime_transfer":
		return status == http.StatusNotImplemented
	case "mapping_capacity", "quota_exceeded":
		return status == http.StatusInsufficientStorage
	default:
		return false
	}
}
