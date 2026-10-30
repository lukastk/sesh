package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

const (
	storedInputPresentationMaxStdout = 8 << 20
	storedInputMetadataMaxStdout     = 1 << 20
	storedInputMaxCursorBytes        = 8192
	storedInputMaxBytes              = 32 << 20
	storedInputChunkBytes            = 256 << 10
	storedInputNativeTimeoutMS       = 10000
	storedInputPageTimeout           = 10 * time.Second
	storedInputBytesTimeout          = 12 * time.Second
	storedInputPeerTimeout           = 15 * time.Second
	storedInputMetadataScanPages     = 64
)

var (
	storedInputProcessSlots = make(chan struct{}, 4)
	storedInputPeerSlots    = make(chan struct{}, 4)
	storedInputPeerClient   = func() *http.Client {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DisableCompression = true
		return &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}()
)

var nativeStoredInputPageErrorCodes = map[string]bool{
	"invalid_arguments": true, "session_not_found": true, "ambiguous_session": true,
	"invalid_cursor": true, "stale_cursor": true, "invalid_session": true,
	"unsupported_session_version": true, "read_failed": true, "internal_error": true,
	"input_image_not_found": true, "input_image_unavailable": true,
}

var nativeStoredInputByteErrorCodes = map[string]bool{
	"invalid_arguments": true, "session_not_found": true, "ambiguous_session": true,
	"invalid_session": true, "unsupported_session_version": true,
	"input_image_not_found": true, "input_image_unavailable": true,
	"invalid_limits": true, "invalid_bytes": true, "byte_count_mismatch": true,
	"digest_mismatch": true, "timeout": true, "aborted": true,
	"consumer_failed": true, "write_failed": true, "internal_error": true,
}

type storedInputPageKind struct {
	route       string
	browserPath string
	protocol    string
	version     int
	capability  string
	command     string
	fixedArgs   []string
	cursorFlag  string
	itemsFlag   string
	bytesFlag   string
	maxBytes    int
	maxStdout   int
}

var storedPresentationKind = storedInputPageKind{
	route: "presentation", browserPath: "presentation", protocol: "pi.session-presentation", version: 1,
	capability: "stored_current_branch_input_image_refs",
	command:    "--session-presentation-json", cursorFlag: "--session-presentation-cursor",
	itemsFlag: "--session-presentation-max-items", bytesFlag: "--session-presentation-max-bytes",
	maxBytes: storedInputPresentationMaxStdout, maxStdout: storedInputPresentationMaxStdout,
}

var storedMediaPresentationKind = storedInputPageKind{
	route: "media presentation", browserPath: "media-presentation", protocol: "pi.session-presentation", version: 2,
	capability: "stored_current_branch_media_refs",
	command:    "--session-presentation-json", fixedArgs: []string{"--session-presentation-version", "2"}, cursorFlag: "--session-presentation-cursor",
	itemsFlag: "--session-presentation-max-items", bytesFlag: "--session-presentation-max-bytes",
	maxBytes: storedInputPresentationMaxStdout, maxStdout: storedInputPresentationMaxStdout,
}

var storedInputMetadataKind = storedInputPageKind{
	route: "input-image metadata", browserPath: "input-images", protocol: "pi.session-input-images", version: 1,
	capability: "stored_user_input_image_metadata",
	command:    "--session-input-images-json", cursorFlag: "--session-input-image-cursor",
	itemsFlag: "--session-input-image-max-items", bytesFlag: "--session-input-image-max-bytes",
	maxBytes: 524288, maxStdout: storedInputMetadataMaxStdout,
}

type storedInputPageQuery struct {
	ThreadID      string
	NativeSession string
	Cursor        string
	MaxItems      int
	MaxBytes      int
}

type storedInputBytesQuery struct {
	ThreadID      string
	NativeSession string
	InputImageID  string
}

type nativeStoredInputEnvelope struct {
	Protocol      string                         `json:"protocol"`
	Version       int                            `json:"version"`
	Capability    string                         `json:"capability"`
	Status        string                         `json:"status"`
	Session       json.RawMessage                `json:"session,omitempty"`
	Page          json.RawMessage                `json:"page,omitempty"`
	Error         *api.BrowserStoredHistoryError `json:"error,omitempty"`
	parsedSession *api.PiStoredHistorySession
}

type nativeInputImagePageWire struct {
	Scope       string                           `json:"scope"`
	InputImages []nativeInputImageProjectionWire `json:"inputImages"`
	Coverage    nativeInputImageCoverageWire     `json:"coverage"`
	NextCursor  string                           `json:"nextCursor,omitempty"`
}

type nativeInputImageProjectionWire struct {
	InputImageID string          `json:"inputImageId"`
	MediaType    string          `json:"mediaType"`
	ByteLength   int64           `json:"byteLength"`
	SHA256       json.RawMessage `json:"sha256,omitempty"`
	Source       json.RawMessage `json:"source"`
	Origin       string          `json:"origin"`
	Availability json.RawMessage `json:"availability"`
}

type nativeInputImageCoverageWire struct {
	Scope               string                         `json:"scope"`
	LeafID              json.RawMessage                `json:"leafId"`
	StartIndex          int                            `json:"startIndex"`
	EndIndexExclusive   int                            `json:"endIndexExclusive"`
	TotalInputImages    int                            `json:"totalInputImages"`
	ByteLength          int                            `json:"byteLength"`
	HasOlder            bool                           `json:"hasOlder"`
	HasNewer            bool                           `json:"hasNewer"`
	OversizedInputImage *nativeInputImageOversizedWire `json:"oversizedInputImage,omitempty"`
}

type nativeInputImageOversizedWire struct {
	Index        int    `json:"index"`
	InputImageID string `json:"inputImageId"`
	ByteLength   int    `json:"byteLength"`
}

type storedInputImageMetadata struct {
	ID           string
	MediaType    string
	ByteLength   int64
	CanonicalSHA string
}

func decodeStoredInputPageQuery(r *http.Request, kind storedInputPageKind) (storedInputPageQuery, error) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "id" && key != "native_session_id" && key != "cursor" && key != "max_items" && key != "max_bytes") || len(values) != 1 || values[0] == "" {
			return storedInputPageQuery{}, fmt.Errorf("exactly one non-empty id and native_session_id plus optional cursor, max_items and max_bytes are allowed")
		}
	}
	out := storedInputPageQuery{ThreadID: q.Get("id"), NativeSession: q.Get("native_session_id"), Cursor: q.Get("cursor")}
	if !validCanonicalUUID(out.ThreadID) {
		return storedInputPageQuery{}, fmt.Errorf("id must be a canonical UUID")
	}
	if !validCanonicalUUID(out.NativeSession) {
		return storedInputPageQuery{}, fmt.Errorf("native_session_id must be a canonical UUID")
	}
	if len(out.Cursor) > storedInputMaxCursorBytes {
		return storedInputPageQuery{}, fmt.Errorf("cursor exceeds 8192 bytes")
	}
	if raw := q.Get("max_items"); raw != "" {
		n, err := parseStrictPositiveDecimal(raw)
		if err != nil || n < 1 || n > 500 {
			return storedInputPageQuery{}, fmt.Errorf("max_items must be 1-500")
		}
		out.MaxItems = n
	}
	if raw := q.Get("max_bytes"); raw != "" {
		n, err := parseStrictPositiveDecimal(raw)
		if err != nil || n < 2 || n > kind.maxBytes {
			return storedInputPageQuery{}, fmt.Errorf("max_bytes must be 2-%d", kind.maxBytes)
		}
		out.MaxBytes = n
	}
	return out, nil
}

func decodeStoredInputBytesQuery(r *http.Request) (storedInputBytesQuery, string, error) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "thread_id" && key != "native_session_id" && key != "input_image_id") || len(values) != 1 || values[0] == "" {
			return storedInputBytesQuery{}, "invalid_request", fmt.Errorf("exactly one non-empty thread_id, native_session_id and input_image_id are required")
		}
	}
	out := storedInputBytesQuery{ThreadID: q.Get("thread_id"), NativeSession: q.Get("native_session_id"), InputImageID: q.Get("input_image_id")}
	if !validCanonicalUUID(out.ThreadID) {
		return storedInputBytesQuery{}, "invalid_request", fmt.Errorf("thread_id must be a canonical UUID")
	}
	if !validCanonicalUUID(out.NativeSession) {
		return storedInputBytesQuery{}, "invalid_request", fmt.Errorf("native_session_id must be a canonical UUID")
	}
	if !validNativePublicationID(out.InputImageID) {
		return storedInputBytesQuery{}, "invalid_request", fmt.Errorf("input_image_id must be a canonical native input-image ID")
	}
	if len(r.Header.Values("Range")) != 0 {
		return out, "range_not_supported", fmt.Errorf("Range is not supported")
	}
	return out, "", nil
}

func parseStrictPositiveDecimal(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty decimal")
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("not decimal")
		}
	}
	return strconv.Atoi(raw)
}

func storedInputHTTPStatus(code string) int {
	switch code {
	case "invalid_request", "invalid_arguments", "invalid_cursor", "invalid_limits", "unsupported_agent":
		return http.StatusBadRequest
	case "thread_not_found", "session_not_found", "input_image_not_found":
		return http.StatusNotFound
	case "owner_conflict", "session_association_missing", "ambiguous_session", "stale_cursor", "stale_session", "input_image_pending", "input_image_unavailable", "runtime_active", "stale_replacement", "stale_target", "stale_branch", "stale_listener_generation":
		return http.StatusConflict
	case "range_not_supported":
		return http.StatusRequestedRangeNotSatisfiable
	case "invalid_session", "unsupported_session_version", "invalid_bytes":
		return http.StatusUnprocessableEntity
	case "owner_not_allowed":
		return http.StatusForbidden
	case "native_unavailable", "owner_unavailable", "native_busy", "runtime_unavailable":
		return http.StatusServiceUnavailable
	case "unsupported_native_capability":
		return http.StatusNotImplemented
	case "byte_limit":
		return http.StatusRequestEntityTooLarge
	case "native_timeout", "timeout":
		return http.StatusGatewayTimeout
	case "native_output_too_large", "invalid_native_response", "session_mismatch", "owner_routing_failed", "byte_count_mismatch", "digest_mismatch", "write_failed", "consumer_failed":
		return http.StatusBadGateway
	case "aborted":
		return http.StatusRequestTimeout
	case "read_failed", "owner_internal_error", "internal_error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func safeStoredInputMessage(code string) string {
	switch code {
	case "invalid_arguments":
		return "Pi rejected the stopped input-media arguments"
	case "session_not_found":
		return "The associated Pi session is not available on its owner"
	case "ambiguous_session":
		return "Pi reported an ambiguous session association"
	case "invalid_cursor":
		return "The native input-media cursor is invalid"
	case "stale_cursor":
		return "The native input-media cursor is stale"
	case "invalid_session":
		return "The associated Pi session is invalid"
	case "unsupported_session_version":
		return "The associated Pi session version is unsupported"
	case "read_failed":
		return "Pi could not read the associated session"
	case "input_image_not_found":
		return "The input image is not present on the current branch"
	case "input_image_unavailable":
		return "The input image bytes are unavailable"
	case "invalid_limits":
		return "Pi rejected the input-image byte limits"
	case "invalid_bytes":
		return "The input image contains invalid bytes"
	case "byte_count_mismatch":
		return "The input-image byte count does not match canonical metadata"
	case "digest_mismatch":
		return "The input-image digest does not match canonical metadata"
	case "timeout":
		return "The native input-image copy timed out"
	case "aborted":
		return "The native input-image copy was aborted"
	case "consumer_failed", "write_failed":
		return "Pi could not complete its input-image output"
	case "internal_error":
		return "Pi reported an internal stopped input-media error"
	default:
		return "The stopped input-media operation failed"
	}
}

func writeStoredInputError(w http.ResponseWriter, status int, kind storedInputPageKind, threadID, owner, nativeSession, code, message string) {
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r", " "), "\n", " ")
	if utf8.RuneCountInString(message) > 512 {
		message = string([]rune(message)[:512])
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if kind.protocol == "pi.session-input-image-bytes" {
		w.Header().Set("Accept-Ranges", "none")
	}
	writeJSON(w, status, api.BrowserStoredInputMediaResponse{
		Schema: api.SchemaVersion, ThreadID: threadID, OwnerMachine: owner, NativeSessionID: nativeSession,
		Protocol: kind.protocol, Version: kind.version, Capability: kind.capability, Status: "error",
		Error: &api.BrowserStoredHistoryError{Code: code, Message: message},
	})
}

var storedInputBytesKind = storedInputPageKind{protocol: "pi.session-input-image-bytes", version: 1, capability: "stored_user_input_image_bytes"}

func acquireStoredInputSlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseStoredInputSlot(slots chan struct{}) { <-slots }

func (d *Daemon) handleBrowserStoredPresentation(w http.ResponseWriter, r *http.Request) {
	d.handleBrowserStoredInputPage(w, r, storedPresentationKind)
}

func (d *Daemon) handleBrowserStoredMediaPresentation(w http.ResponseWriter, r *http.Request) {
	d.handleBrowserStoredInputPage(w, r, storedMediaPresentationKind)
}

func (d *Daemon) handleBrowserStoredMediaPresentationMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	writeStoredInputError(w, http.StatusMethodNotAllowed, storedMediaPresentationKind, "", "", "", "invalid_request", "browser stopped media presentation supports GET only")
}

func (d *Daemon) handleBrowserStoredInputImages(w http.ResponseWriter, r *http.Request) {
	d.handleBrowserStoredInputPage(w, r, storedInputMetadataKind)
}

func (d *Daemon) handleBrowserStoredInputPage(w http.ResponseWriter, r *http.Request, kind storedInputPageKind) {
	query, err := decodeStoredInputPageQuery(r, kind)
	if err != nil {
		writeStoredInputError(w, http.StatusBadRequest, kind, "", "", "", "invalid_request", "browser stopped "+kind.route+": "+err.Error())
		return
	}
	owner, err := d.browserThreadOwner(query.ThreadID)
	if err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredInputError(w, status, kind, query.ThreadID, "", query.NativeSession, code, "could not resolve the authoritative thread owner")
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredInputError(w, status, kind, query.ThreadID, owner, query.NativeSession, code, "the authoritative thread owner is not available to this browser hub")
		return
	}
	if owner != d.cfg.Machine {
		d.serveStoredInputPagePeer(w, r, owner, query, kind)
		return
	}
	d.serveLocalStoredInputPage(r.Context(), w, query, owner, kind)
}

func (d *Daemon) handleBrowserStoredInputImageBytes(w http.ResponseWriter, r *http.Request) {
	query, code, err := decodeStoredInputBytesQuery(r)
	if err != nil {
		status := storedInputHTTPStatus(code)
		writeStoredInputError(w, status, storedInputBytesKind, query.ThreadID, "", query.NativeSession, code, "browser stopped input-image bytes: "+err.Error())
		return
	}
	owner, err := d.browserThreadOwner(query.ThreadID)
	if err != nil {
		status, ownerCode := storedHistoryOwnerError(err)
		writeStoredInputError(w, status, storedInputBytesKind, query.ThreadID, "", query.NativeSession, ownerCode, "could not resolve the authoritative thread owner")
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		status, ownerCode := storedHistoryOwnerError(err)
		writeStoredInputError(w, status, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, ownerCode, "the authoritative thread owner is not available to this browser hub")
		return
	}
	if owner != d.cfg.Machine {
		d.serveStoredInputBytesPeer(w, r, owner, query)
		return
	}
	d.serveLocalStoredInputBytes(r.Context(), w, r.Method, query, owner)
}

func (d *Daemon) resolveStoredInputSource(threadID, nativeSession string) (storedArtifactSource, string, string) {
	thread, err := d.store.GetThread(threadID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			return storedArtifactSource{}, "thread_not_found", "thread not found"
		}
		return storedArtifactSource{}, "owner_internal_error", "owner thread lookup failed"
	}
	if !piFamily(thread.AgentKind) {
		return storedArtifactSource{}, "unsupported_agent", "stopped input media is available only for pi/mypi threads"
	}
	if !validCanonicalUUID(thread.AgentSessionID) {
		return storedArtifactSource{}, "session_association_missing", "Pi thread has no canonical associated session id"
	}
	if thread.AgentSessionID != nativeSession {
		return storedArtifactSource{}, "stale_session", "requested native session is no longer associated with the thread"
	}
	stopped, err := d.storedArtifactThreadStopped(threadID)
	if err != nil {
		return storedArtifactSource{}, "owner_internal_error", "could not verify the Pi runtime state"
	}
	if !stopped {
		return storedArtifactSource{}, "runtime_active", "stopped input-media access requires the Pi runtime to be stopped"
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	path, found, err := agents.TranscriptPath(agents.Kind(thread.AgentKind), thread.AgentSessionID, thread.Cwd, homes)
	if err != nil {
		return storedArtifactSource{}, "read_failed", "could not resolve the associated Pi session"
	}
	if !found {
		return storedArtifactSource{}, "session_not_found", "associated Pi session file is not present on its owner"
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return storedArtifactSource{}, "read_failed", "could not resolve the associated Pi session"
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return storedArtifactSource{}, "read_failed", "associated Pi session file is unavailable"
	}
	return storedArtifactSource{
		threadID: threadID, sessionID: thread.AgentSessionID,
		agentKind: thread.AgentKind, exe: agentExecutable(thread.AgentKind),
		path: path, info: info,
	}, "", ""
}

func (d *Daemon) serveLocalStoredInputPage(ctx context.Context, w http.ResponseWriter, query storedInputPageQuery, owner string, kind storedInputPageKind) {
	source, code, message := d.resolveStoredInputSource(query.ThreadID, query.NativeSession)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), kind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	if !acquireStoredInputSlot(storedInputProcessSlots) {
		writeStoredInputError(w, http.StatusServiceUnavailable, kind, query.ThreadID, owner, query.NativeSession, "native_busy", "native input-media process capacity is exhausted")
		return
	}
	defer releaseStoredInputSlot(storedInputProcessSlots)
	envelope, code, message := runPiStoredInputPage(ctx, source.exe, source.path, query, kind)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), kind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	if envelope.parsedSession.ID != source.sessionID {
		writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "session_mismatch", "Pi input-media session does not match the thread association")
		return
	}
	if !d.storedArtifactSourceUnchanged(source) {
		writeStoredInputError(w, http.StatusConflict, kind, query.ThreadID, owner, query.NativeSession, "stale_replacement", "thread, session, runtime or session file changed during input-media read")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, api.BrowserStoredInputMediaResponse{
		Schema: api.SchemaVersion, ThreadID: query.ThreadID, OwnerMachine: owner, NativeSessionID: query.NativeSession,
		Protocol: envelope.Protocol, Version: envelope.Version, Capability: envelope.Capability, Status: envelope.Status,
		Session: envelope.parsedSession, Page: envelope.Page,
	})
}

func (d *Daemon) serveLocalStoredInputBytes(ctx context.Context, w http.ResponseWriter, method string, query storedInputBytesQuery, owner string) {
	active, code, message := d.inputMediaRuntimeActive(query.ThreadID, query.NativeSession)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	if active {
		d.serveLocalLiveInputBytes(ctx, w, method, query, owner)
		return
	}
	source, code, message := d.resolveStoredInputSource(query.ThreadID, query.NativeSession)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	if !acquireStoredInputSlot(storedInputProcessSlots) {
		writeStoredInputError(w, http.StatusServiceUnavailable, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "native_busy", "native input-image process capacity is exhausted")
		return
	}
	defer releaseStoredInputSlot(storedInputProcessSlots)

	metadata, code, message := findStoredInputImageMetadata(ctx, source.exe, source.path, source.sessionID, query.InputImageID)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	body, digest, code, message := runPiStoredInputImageBytes(ctx, source.exe, source.path, query.InputImageID)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	if int64(len(body)) != metadata.ByteLength {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "byte_count_mismatch", "input-image bytes do not match canonical metadata")
		return
	}
	digestStatus := "computed-only"
	if metadata.CanonicalSHA != "" {
		if digest != metadata.CanonicalSHA {
			writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "digest_mismatch", "input-image digest does not match canonical metadata")
			return
		}
		digestStatus = "canonical-match"
	}
	if !d.storedArtifactSourceUnchanged(source) {
		writeStoredInputError(w, http.StatusConflict, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "stale_replacement", "thread, session, runtime or session file changed during input-image read")
		return
	}
	writeVerifiedStoredInputImage(w, method, body, metadata.MediaType, digest, digestStatus)
}

func runPiStoredInputPage(parent context.Context, exe, path string, query storedInputPageQuery, kind storedInputPageKind) (nativeStoredInputEnvelope, string, string) {
	args := []string{kind.command, path}
	args = append(args, kind.fixedArgs...)
	if query.Cursor != "" {
		args = append(args, kind.cursorFlag, query.Cursor)
	}
	if query.MaxItems != 0 {
		args = append(args, kind.itemsFlag, strconv.Itoa(query.MaxItems))
	}
	if query.MaxBytes != 0 {
		args = append(args, kind.bytesFlag, strconv.Itoa(query.MaxBytes))
	}
	ctx, cancel := context.WithTimeout(parent, storedInputPageTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	var stdout cappedBuffer
	stdout.limit = kind.maxStdout + 1
	var stderr cappedBuffer
	stderr.limit = storedArtifactMaxStderr + 1
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nativeStoredInputEnvelope{}, "native_timeout", "Pi stopped input-media read exceeded 10 seconds"
	}
	if stdout.exceeded || len(stdout.Bytes()) > kind.maxStdout || stderr.exceeded || len(stderr.Bytes()) > storedArtifactMaxStderr {
		return nativeStoredInputEnvelope{}, "native_output_too_large", "Pi stopped input-media process exceeded its bounded output"
	}
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return nativeStoredInputEnvelope{}, "native_unavailable", "Pi executable is unavailable on the owner"
	}
	envelope, parseErr := decodeNativeStoredInputEnvelope(stdout.Bytes(), kind)
	if parseErr != nil {
		if kind.version == 2 && claimsNativeStoredInputProtocolVersion(stdout.Bytes(), kind) {
			return nativeStoredInputEnvelope{}, "invalid_native_response", parseErr.Error()
		}
		if runErr != nil {
			return nativeStoredInputEnvelope{}, "unsupported_native_capability", "installed Pi does not provide the required stopped input-media protocol"
		}
		return nativeStoredInputEnvelope{}, "invalid_native_response", parseErr.Error()
	}
	if kind.version != 2 && len(stderr.Bytes()) != 0 {
		return nativeStoredInputEnvelope{}, "invalid_native_response", "Pi stopped input-media mode wrote unexpected stderr"
	}
	if envelope.Protocol != kind.protocol || envelope.Version != kind.version || envelope.Capability != kind.capability {
		if kind.version == 2 && envelope.Protocol == kind.protocol && envelope.Version == kind.version {
			return nativeStoredInputEnvelope{}, "invalid_native_response", "Pi claimed stopped media-presentation version 2 with an invalid capability"
		}
		return nativeStoredInputEnvelope{}, "unsupported_native_capability", "installed Pi does not provide the required stopped input-media protocol"
	}
	if len(stderr.Bytes()) != 0 {
		return nativeStoredInputEnvelope{}, "invalid_native_response", "Pi stopped input-media mode wrote unexpected stderr"
	}
	switch envelope.Status {
	case "ok":
		if runErr != nil || envelope.Error != nil {
			return nativeStoredInputEnvelope{}, "invalid_native_response", "Pi stopped input-media success has the wrong exit status or error field"
		}
		session, err := decodeNativeStoredHistorySession(envelope.Session)
		if err != nil || !validNativeStoredInputPage(envelope.Page, kind, session.LeafID) {
			return nativeStoredInputEnvelope{}, "invalid_native_response", "Pi stopped input-media success has an invalid session or page"
		}
		envelope.parsedSession = session
		return envelope, "", ""
	case "error":
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 || envelope.Error == nil || len(envelope.Session) != 0 || len(envelope.Page) != 0 || !nativeStoredInputPageErrorCodes[envelope.Error.Code] || !validNativeHistoryMessage(envelope.Error.Message) {
			return nativeStoredInputEnvelope{}, "invalid_native_response", "Pi stopped input-media error envelope is invalid"
		}
		return nativeStoredInputEnvelope{}, envelope.Error.Code, safeStoredInputMessage(envelope.Error.Code)
	default:
		return nativeStoredInputEnvelope{}, "invalid_native_response", "Pi stopped input-media response has an unknown status"
	}
}

func decodeNativeStoredInputEnvelope(raw []byte, kind storedInputPageKind) (nativeStoredInputEnvelope, error) {
	raw, err := strictNativeJSONLine(raw, "Pi stopped "+kind.route)
	if err != nil {
		return nativeStoredInputEnvelope{}, err
	}
	var envelope nativeStoredInputEnvelope
	if err := decodeStrictJSON(raw, &envelope); err != nil {
		return nativeStoredInputEnvelope{}, fmt.Errorf("invalid Pi stopped input-media JSON: %w", err)
	}
	return envelope, nil
}

func claimsNativeStoredInputProtocolVersion(raw []byte, kind storedInputPageKind) bool {
	var claim struct {
		Protocol string `json:"protocol"`
		Version  int    `json:"version"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	return dec.Decode(&claim) == nil && claim.Protocol == kind.protocol && claim.Version == kind.version
}

func validNativeStoredInputPage(raw json.RawMessage, kind storedInputPageKind, expectedLeaf *string) bool {
	if kind.protocol == storedPresentationKind.protocol {
		var page struct {
			Scope    string            `json:"scope"`
			Items    []json.RawMessage `json:"items"`
			Coverage struct {
				Scope             string          `json:"scope"`
				LeafID            json.RawMessage `json:"leafId"`
				StartIndex        int             `json:"startIndex"`
				EndIndexExclusive int             `json:"endIndexExclusive"`
				TotalItems        int             `json:"totalItems"`
				ByteLength        int             `json:"byteLength"`
				HasOlder          bool            `json:"hasOlder"`
				HasNewer          bool            `json:"hasNewer"`
				OversizedItem     json.RawMessage `json:"oversizedItem,omitempty"`
			} `json:"coverage"`
			NextCursor string `json:"nextCursor,omitempty"`
		}
		if len(raw) == 0 || decodeStrictJSON(raw, &page) != nil || page.Scope != "current_branch_presentation" || len(page.Items) > 500 || !validOpaqueCursor(page.NextCursor) {
			return false
		}
		c := page.Coverage
		if c.Scope != page.Scope || !validNullableString(c.LeafID) || c.StartIndex < 0 || c.EndIndexExclusive < c.StartIndex || c.TotalItems < c.EndIndexExclusive || c.ByteLength < 2 || c.ByteLength > storedInputPresentationMaxStdout {
			return false
		}
		if kind.version == 2 {
			if !nativeLeafMatches(c.LeafID, expectedLeaf) || c.HasOlder != (c.StartIndex > 0) || c.HasNewer != (c.EndIndexExclusive < c.TotalItems) || c.HasOlder != (page.NextCursor != "") {
				return false
			}
			if len(c.OversizedItem) != 0 {
				var oversized struct {
					Index      int    `json:"index"`
					ItemID     string `json:"itemId"`
					ByteLength int    `json:"byteLength"`
				}
				if decodeStrictJSON(c.OversizedItem, &oversized) != nil || oversized.Index < 0 || oversized.Index >= c.TotalItems || oversized.ItemID == "" || oversized.ByteLength < 1 {
					return false
				}
			}
		}
		for _, item := range page.Items {
			trimmed := bytes.TrimSpace(item)
			if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
				return false
			}
		}
		return true
	}
	var page nativeInputImagePageWire
	if len(raw) == 0 || decodeStrictJSON(raw, &page) != nil || !validNativeInputImagePage(&page) {
		return false
	}
	return true
}

func nativeLeafMatches(raw json.RawMessage, expected *string) bool {
	var actual *string
	if decodeStrictJSON(raw, &actual) != nil {
		return false
	}
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	return *actual == *expected
}

func validOpaqueCursor(value string) bool {
	if len(value) > storedInputMaxCursorBytes {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func validNativeInputImagePage(page *nativeInputImagePageWire) bool {
	if page.Scope != "current_branch_input_images" || len(page.InputImages) > 500 || !validOpaqueCursor(page.NextCursor) {
		return false
	}
	c := page.Coverage
	if c.Scope != page.Scope || !validNullableString(c.LeafID) || c.StartIndex < 0 || c.EndIndexExclusive < c.StartIndex || c.TotalInputImages < c.EndIndexExclusive || c.ByteLength < 2 || c.ByteLength > 524288 {
		return false
	}
	if c.OversizedInputImage != nil && (c.OversizedInputImage.Index < 0 || !validNativePublicationID(c.OversizedInputImage.InputImageID) || c.OversizedInputImage.ByteLength < 1) {
		return false
	}
	for i := range page.InputImages {
		if !validNativeInputImageProjection(&page.InputImages[i]) {
			return false
		}
	}
	return true
}

func validNativeInputImageProjection(image *nativeInputImageProjectionWire) bool {
	if !validNativePublicationID(image.InputImageID) || !validInputMediaType(image.MediaType) || image.ByteLength < 0 || image.ByteLength > storedInputMaxBytes || (image.Origin != "native" && image.Origin != "legacy_user_image") {
		return false
	}
	canonicalSHA := ""
	if len(image.SHA256) != 0 {
		if decodeStrictJSON(image.SHA256, &canonicalSHA) != nil || !validLowerSHA256(canonicalSHA) {
			return false
		}
	}
	if (image.Origin == "native" && canonicalSHA == "") || (image.Origin == "legacy_user_image" && canonicalSHA != "") {
		return false
	}
	var source struct {
		Type          string `json:"type"`
		EntryID       string `json:"entryId"`
		TargetEntryID string `json:"targetEntryId,omitempty"`
		ContentIndex  int    `json:"contentIndex"`
	}
	if decodeStrictJSON(image.Source, &source) != nil || source.EntryID == "" || source.ContentIndex < 0 {
		return false
	}
	switch source.Type {
	case "user_message_image":
		if source.TargetEntryID != "" {
			return false
		}
	case "user_context_edit_image":
		if source.TargetEntryID == "" {
			return false
		}
	default:
		return false
	}
	var availability struct {
		Status  string `json:"status"`
		Reason  string `json:"reason,omitempty"`
		Message string `json:"message,omitempty"`
	}
	if decodeStrictJSON(image.Availability, &availability) != nil {
		return false
	}
	if availability.Status == "available" {
		return availability.Reason == "" && availability.Message == ""
	}
	if availability.Status != "unavailable" || availability.Message == "" || !validNativeHistoryMessage(availability.Message) {
		return false
	}
	switch availability.Reason {
	case "invalid_bytes", "byte_limit", "source_missing", "not_persisted":
		return true
	default:
		return false
	}
}

func validInputMediaType(value string) bool {
	if len(value) < 1 || len(value) > 255 || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func findStoredInputImageMetadata(parent context.Context, exe, path, sessionID, inputImageID string) (storedInputImageMetadata, string, string) {
	ctx, cancel := context.WithTimeout(parent, storedInputPageTimeout)
	defer cancel()
	cursor := ""
	for pageNumber := 0; pageNumber < storedInputMetadataScanPages; pageNumber++ {
		query := storedInputPageQuery{Cursor: cursor, MaxItems: 500, MaxBytes: 524288}
		envelope, code, message := runPiStoredInputPage(ctx, exe, path, query, storedInputMetadataKind)
		if code != "" {
			return storedInputImageMetadata{}, code, message
		}
		if envelope.parsedSession.ID != sessionID {
			return storedInputImageMetadata{}, "session_mismatch", "Pi input-image metadata session does not match the thread association"
		}
		var page nativeInputImagePageWire
		if decodeStrictJSON(envelope.Page, &page) != nil || !validNativeInputImagePage(&page) {
			return storedInputImageMetadata{}, "invalid_native_response", "Pi input-image metadata page is invalid"
		}
		for i := range page.InputImages {
			image := &page.InputImages[i]
			if image.InputImageID != inputImageID {
				continue
			}
			var availability struct {
				Status string `json:"status"`
			}
			if decodeStrictJSON(image.Availability, &availability) != nil {
				return storedInputImageMetadata{}, "invalid_native_response", "Pi input-image availability is invalid"
			}
			if availability.Status != "available" {
				return storedInputImageMetadata{}, "input_image_unavailable", "input image is unavailable"
			}
			canonicalSHA := ""
			if len(image.SHA256) != 0 && decodeStrictJSON(image.SHA256, &canonicalSHA) != nil {
				return storedInputImageMetadata{}, "invalid_native_response", "Pi input-image digest is invalid"
			}
			return storedInputImageMetadata{ID: image.InputImageID, MediaType: image.MediaType, ByteLength: image.ByteLength, CanonicalSHA: canonicalSHA}, "", ""
		}
		if page.NextCursor == "" {
			return storedInputImageMetadata{}, "input_image_not_found", "input image is not on the stopped current branch"
		}
		cursor = page.NextCursor
	}
	return storedInputImageMetadata{}, "native_output_too_large", "input-image metadata scan exceeded its bounded page count"
}

func runPiStoredInputImageBytes(parent context.Context, exe, path, inputImageID string) ([]byte, string, string, string) {
	args := []string{
		"--session-input-image-bytes", path,
		"--input-image-id", inputImageID,
		"--input-image-max-bytes", strconv.Itoa(storedInputMaxBytes),
		"--input-image-chunk-bytes", strconv.Itoa(storedInputChunkBytes),
		"--input-image-timeout-ms", strconv.Itoa(storedInputNativeTimeoutMS),
	}
	ctx, cancel := context.WithTimeout(parent, storedInputBytesTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	var stdout cappedBuffer
	stdout.limit = storedInputMaxBytes + 1
	var stderr cappedBuffer
	stderr.limit = storedArtifactMaxStderr + 1
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, "", "native_timeout", "Pi stopped input-image byte read exceeded 12 seconds"
	}
	if stdout.exceeded || len(stdout.Bytes()) > storedInputMaxBytes || stderr.exceeded || len(stderr.Bytes()) > storedArtifactMaxStderr {
		return nil, "", "native_output_too_large", "Pi stopped input-image byte process exceeded its bounded output"
	}
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return nil, "", "native_unavailable", "Pi executable is unavailable on the owner"
	}
	if runErr != nil {
		nativeError, err := decodeNativeStoredInputByteError(stderr.Bytes())
		if err != nil {
			return nil, "", "unsupported_native_capability", "installed Pi does not provide pi.session-input-image-bytes version 1"
		}
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 || !nativeStoredInputByteErrorCodes[nativeError.Code] {
			return nil, "", "invalid_native_response", "Pi stopped input-image byte failure is invalid"
		}
		return nil, "", nativeError.Code, safeStoredInputMessage(nativeError.Code)
	}
	if len(stderr.Bytes()) != 0 {
		return nil, "", "invalid_native_response", "Pi stopped input-image byte success wrote unexpected stderr"
	}
	body := stdout.Bytes()
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), "", ""
}

func decodeNativeStoredInputByteError(raw []byte) (*api.BrowserStoredHistoryError, error) {
	raw, err := strictNativeJSONLine(raw, "Pi stopped input-image byte failure")
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Protocol   string                         `json:"protocol"`
		Version    int                            `json:"version"`
		Capability string                         `json:"capability"`
		Status     string                         `json:"status"`
		Error      *api.BrowserStoredHistoryError `json:"error"`
	}
	if decodeStrictJSON(raw, &envelope) != nil || envelope.Protocol != storedInputBytesKind.protocol || envelope.Version != 1 || envelope.Capability != storedInputBytesKind.capability || envelope.Status != "error" || envelope.Error == nil || !validNativeHistoryMessage(envelope.Error.Message) {
		return nil, fmt.Errorf("invalid Pi stopped input-image byte error envelope")
	}
	return envelope.Error, nil
}

func writeVerifiedStoredInputImage(w http.ResponseWriter, method string, body []byte, mediaType, digest, digestStatus string) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", `"sha256-`+digest+`"`)
	w.Header().Set("X-Sesh-Input-Image-SHA256", digest)
	w.Header().Set("X-Sesh-Input-Image-Digest-Status", digestStatus)
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (d *Daemon) serveStoredInputPagePeer(w http.ResponseWriter, r *http.Request, owner string, query storedInputPageQuery, kind storedInputPageKind) {
	if !acquireStoredInputSlot(storedInputPeerSlots) {
		writeStoredInputError(w, http.StatusServiceUnavailable, kind, query.ThreadID, owner, query.NativeSession, "native_busy", "peer input-media capacity is exhausted")
		return
	}
	defer releaseStoredInputSlot(storedInputPeerSlots)
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeStoredInputError(w, browserOwnerStatus(err), kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "the input-media owner peer is unavailable")
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "browser hub peer authentication is unavailable")
		return
	}
	q := url.Values{"id": []string{query.ThreadID}, "native_session_id": []string{query.NativeSession}}
	if query.Cursor != "" {
		q.Set("cursor", query.Cursor)
	}
	if query.MaxItems != 0 {
		q.Set("max_items", strconv.Itoa(query.MaxItems))
	}
	if query.MaxBytes != 0 {
		q.Set("max_bytes", strconv.Itoa(query.MaxBytes))
	}
	route := "/v1/browser/threads/" + kind.browserPath
	ctx, cancel := context.WithTimeout(r.Context(), storedInputPeerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+peer.ApiAddr+route+"?"+q.Encode(), nil)
	if err != nil {
		writeStoredInputError(w, http.StatusInternalServerError, kind, query.ThreadID, owner, query.NativeSession, "owner_internal_error", "could not build input-media peer request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := storedInputPeerClient.Do(req)
	if err != nil {
		writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "input-media peer request failed")
		return
	}
	d.copyStoredInputPagePeerResponse(w, resp, query, owner, kind)
}

func (d *Daemon) copyStoredInputPagePeerResponse(w http.ResponseWriter, resp *http.Response, query storedInputPageQuery, owner string, kind storedInputPageKind) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(kind.maxStdout)+1))
	if err != nil || len(body) > kind.maxStdout {
		writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "input-media peer response exceeded its limit")
		return
	}
	var envelope api.BrowserStoredInputMediaResponse
	if decodeStrictJSON(body, &envelope) != nil || envelope.Schema != api.SchemaVersion || envelope.ThreadID != query.ThreadID || envelope.OwnerMachine != owner || envelope.NativeSessionID != query.NativeSession || envelope.Protocol != kind.protocol || envelope.Version != kind.version || envelope.Capability != kind.capability {
		writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer did not return the stopped input-media protocol")
		return
	}
	switch envelope.Status {
	case "ok":
		if resp.StatusCode != http.StatusOK || envelope.Session == nil || envelope.Session.ID != query.NativeSession || envelope.Error != nil || !validNativeStoredInputPage(envelope.Page, kind, envelope.Session.LeafID) {
			writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer returned invalid stopped input-media success")
			return
		}
	case "error":
		if resp.StatusCode < 400 || resp.StatusCode > 599 || envelope.Error == nil || envelope.Session != nil || len(envelope.Page) != 0 || !validStoredInputPeerErrorCode(envelope.Error.Code) || !validNativeHistoryMessage(envelope.Error.Message) || resp.StatusCode != storedInputHTTPStatus(envelope.Error.Code) {
			writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer returned invalid stopped input-media error")
			return
		}
		writeStoredInputError(w, resp.StatusCode, kind, query.ThreadID, owner, query.NativeSession, envelope.Error.Code, safeStoredInputPeerMessage(envelope.Error.Code))
		return
	default:
		writeStoredInputError(w, http.StatusBadGateway, kind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer returned unknown stopped input-media status")
		return
	}
	currentOwner, err := d.browserThreadOwner(query.ThreadID)
	if err != nil || currentOwner != owner {
		writeStoredInputError(w, http.StatusConflict, kind, query.ThreadID, owner, query.NativeSession, "stale_replacement", "thread ownership changed during input-media peer read")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func validStoredInputPeerErrorCode(code string) bool {
	if nativeStoredInputPageErrorCodes[code] || nativeStoredInputByteErrorCodes[code] {
		return true
	}
	switch code {
	case "invalid_request", "thread_not_found", "owner_conflict", "unsupported_agent", "session_association_missing", "stale_session", "runtime_active", "stale_replacement", "stale_target", "stale_branch", "stale_listener_generation", "input_image_pending", "byte_limit", "runtime_unavailable", "session_mismatch", "owner_not_allowed", "owner_unavailable", "owner_routing_failed", "owner_internal_error", "native_unavailable", "native_busy", "unsupported_native_capability", "native_timeout", "native_output_too_large", "invalid_native_response", "range_not_supported":
		return true
	default:
		return false
	}
}

func safeStoredInputPeerMessage(code string) string {
	if nativeStoredInputPageErrorCodes[code] || nativeStoredInputByteErrorCodes[code] {
		return safeStoredInputMessage(code)
	}
	switch code {
	case "invalid_request":
		return "The stopped input-media request is invalid"
	case "thread_not_found":
		return "The input-media thread was not found"
	case "owner_conflict":
		return "The input-media thread has conflicting ownership"
	case "unsupported_agent":
		return "Stopped input media is available only for Pi threads"
	case "session_association_missing":
		return "The Pi thread has no canonical associated session"
	case "stale_session":
		return "The requested Pi session is no longer associated with the thread"
	case "runtime_active":
		return "Stopped input-media access requires the Pi runtime to be stopped"
	case "stale_replacement":
		return "The thread, session, runtime or session file changed during the input-media read"
	case "stale_target":
		return "The live Pi target changed during the input-image read"
	case "stale_branch":
		return "The live Pi branch changed during the input-image read"
	case "stale_listener_generation":
		return "The live input-image reader was replaced during the read"
	case "input_image_pending":
		return "The input image is not durably associated"
	case "byte_limit":
		return "The input image exceeds the live byte limit"
	case "session_mismatch":
		return "Pi returned a session that does not match the thread association"
	case "owner_not_allowed":
		return "The input-media owner is not allowed by this browser hub"
	case "owner_unavailable", "native_unavailable", "native_busy", "runtime_unavailable":
		return "The input-media owner is temporarily unavailable"
	case "unsupported_native_capability":
		return "The owner Pi does not support the required input-media protocol"
	case "native_timeout":
		return "The owner input-media operation timed out"
	case "native_output_too_large", "invalid_native_response":
		return "The owner Pi returned an invalid input-media response"
	case "owner_routing_failed":
		return "The browser hub could not route to the input-media owner"
	case "range_not_supported":
		return "Input-image byte ranges are not supported"
	default:
		return "The input-media owner could not complete the request"
	}
}

func validStoredInputPeerSuccessHeaders(resp *http.Response) (string, string, string, bool) {
	if resp.StatusCode != http.StatusOK || resp.ContentLength < 0 || resp.ContentLength > storedInputMaxBytes || len(resp.TransferEncoding) != 0 {
		return "", "", "", false
	}
	mediaType, ok := firstExact(resp.Header.Values("Content-Type"))
	if !ok || !validInputMediaType(mediaType) || !exactResponseHeader(resp, "Accept-Ranges", "none") || !exactResponseHeader(resp, "X-Content-Type-Options", "nosniff") {
		return "", "", "", false
	}
	digest, ok := firstExact(resp.Header.Values("X-Sesh-Input-Image-SHA256"))
	if !ok || !validLowerSHA256(digest) || !exactResponseHeader(resp, "ETag", `"sha256-`+digest+`"`) {
		return "", "", "", false
	}
	digestStatus, ok := firstExact(resp.Header.Values("X-Sesh-Input-Image-Digest-Status"))
	if !ok || (digestStatus != "canonical-match" && digestStatus != "computed-only") {
		return "", "", "", false
	}
	for name := range resp.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-pi-input-image-") {
			return "", "", "", false
		}
		if strings.HasPrefix(lower, "x-sesh-input-image-") && lower != "x-sesh-input-image-sha256" && lower != "x-sesh-input-image-digest-status" {
			return "", "", "", false
		}
	}
	return mediaType, digest, digestStatus, true
}

func (d *Daemon) serveStoredInputBytesPeer(w http.ResponseWriter, r *http.Request, owner string, query storedInputBytesQuery) {
	if !acquireStoredInputSlot(storedInputPeerSlots) {
		writeStoredInputError(w, http.StatusServiceUnavailable, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "native_busy", "peer input-image capacity is exhausted")
		return
	}
	defer releaseStoredInputSlot(storedInputPeerSlots)
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeStoredInputError(w, browserOwnerStatus(err), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "the input-image owner peer is unavailable")
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "browser hub peer authentication is unavailable")
		return
	}
	q := url.Values{"thread_id": []string{query.ThreadID}, "native_session_id": []string{query.NativeSession}, "input_image_id": []string{query.InputImageID}}
	ctx, cancel := context.WithTimeout(r.Context(), storedInputPeerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+peer.ApiAddr+"/v1/browser/input-images?"+q.Encode(), nil)
	if err != nil {
		writeStoredInputError(w, http.StatusInternalServerError, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_internal_error", "could not build input-image peer request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := storedInputPeerClient.Do(req)
	if err != nil {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "input-image peer request failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		copyStoredInputBytesPeerError(w, resp, query, owner)
		return
	}
	mediaType, expectedDigest, digestStatus, ok := validStoredInputPeerSuccessHeaders(resp)
	if !ok {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer returned invalid input-image byte headers")
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, storedInputMaxBytes+1))
	if err != nil || int64(len(body)) != resp.ContentLength || len(body) > storedInputMaxBytes {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer input-image body did not match its bounded length")
		return
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer input-image digest did not validate")
		return
	}
	currentOwner, err := d.browserThreadOwner(query.ThreadID)
	if err != nil || currentOwner != owner {
		writeStoredInputError(w, http.StatusConflict, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "stale_replacement", "thread ownership changed during input-image peer read")
		return
	}
	writeVerifiedStoredInputImage(w, r.Method, body, mediaType, digest, digestStatus)
}

func copyStoredInputBytesPeerError(w http.ResponseWriter, resp *http.Response, query storedInputBytesQuery, owner string) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, storedInputMetadataMaxStdout+1))
	if err != nil || len(body) > storedInputMetadataMaxStdout {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "input-image peer error exceeded its limit")
		return
	}
	var envelope api.BrowserStoredInputMediaResponse
	if decodeStrictJSON(body, &envelope) != nil || envelope.Schema != api.SchemaVersion || envelope.ThreadID != query.ThreadID || envelope.OwnerMachine != owner || envelope.NativeSessionID != query.NativeSession || envelope.Protocol != storedInputBytesKind.protocol || envelope.Version != 1 || envelope.Capability != storedInputBytesKind.capability || envelope.Status != "error" || envelope.Error == nil || envelope.Session != nil || len(envelope.Page) != 0 || !validStoredInputPeerErrorCode(envelope.Error.Code) || !validNativeHistoryMessage(envelope.Error.Message) || resp.StatusCode != storedInputHTTPStatus(envelope.Error.Code) {
		writeStoredInputError(w, http.StatusBadGateway, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "owner_routing_failed", "peer did not return the input-image byte error protocol")
		return
	}
	writeStoredInputError(w, resp.StatusCode, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, envelope.Error.Code, safeStoredInputPeerMessage(envelope.Error.Code))
}
