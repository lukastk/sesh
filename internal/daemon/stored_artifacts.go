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

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

const (
	storedArtifactMetadataTimeout   = 10 * time.Second
	storedArtifactBytesTimeout      = 15 * time.Second
	storedArtifactPeerTimeout       = 15 * time.Second
	storedArtifactMaxCursorBytes    = 8192
	storedArtifactMetadataMaxStdout = 1 << 20
	storedArtifactMaxStderr         = 64 << 10
	storedArtifactMaxBytes          = 32 << 20
	storedArtifactChunkBytes        = 256 << 10
	storedArtifactNativeTimeoutMS   = 10000
)

var (
	storedArtifactProcessSlots = make(chan struct{}, 4)
	storedArtifactPeerSlots    = make(chan struct{}, 4)
)

var nativeStoredArtifactMetadataErrorCodes = map[string]bool{
	"invalid_arguments": true, "session_not_found": true, "ambiguous_session": true,
	"invalid_cursor": true, "stale_cursor": true, "invalid_session": true,
	"unsupported_session_version": true, "read_failed": true, "internal_error": true,
}

var nativeStoredArtifactByteErrorCodes = map[string]bool{
	"invalid_arguments": true, "session_not_found": true, "ambiguous_session": true,
	"invalid_session": true, "unsupported_session_version": true, "artifact_not_found": true,
	"artifact_unavailable": true, "invalid_limits": true, "invalid_bytes": true,
	"byte_count_mismatch": true, "digest_mismatch": true, "timeout": true,
	"aborted": true, "write_failed": true, "internal_error": true,
}

type storedArtifactMetadataQuery struct {
	ID       string
	Cursor   string
	MaxItems int
	MaxBytes int
}

type storedArtifactBytesQuery struct {
	ID            string
	PublicationID string
}

type nativeStoredArtifactEnvelope struct {
	Protocol      string                         `json:"protocol"`
	Version       int                            `json:"version"`
	Capability    string                         `json:"capability"`
	Status        string                         `json:"status"`
	Session       json.RawMessage                `json:"session,omitempty"`
	Page          json.RawMessage                `json:"page,omitempty"`
	Error         *api.BrowserStoredHistoryError `json:"error,omitempty"`
	parsedSession *api.PiStoredHistorySession
}

type storedArtifactSource struct {
	threadID  string
	sessionID string
	path      string
	info      os.FileInfo
}

func decodeStoredArtifactMetadataQuery(r *http.Request) (storedArtifactMetadataQuery, error) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "id" && key != "cursor" && key != "max_items" && key != "max_bytes") || len(values) != 1 || values[0] == "" {
			return storedArtifactMetadataQuery{}, fmt.Errorf("only one non-empty id, cursor, max_items and max_bytes are allowed")
		}
	}
	out := storedArtifactMetadataQuery{ID: q.Get("id"), Cursor: q.Get("cursor")}
	if !validCanonicalUUID(out.ID) {
		return storedArtifactMetadataQuery{}, fmt.Errorf("id must be a canonical UUID")
	}
	if len(out.Cursor) > storedArtifactMaxCursorBytes {
		return storedArtifactMetadataQuery{}, fmt.Errorf("cursor exceeds 8192 bytes")
	}
	if raw := q.Get("max_items"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			return storedArtifactMetadataQuery{}, fmt.Errorf("max_items must be 1-500")
		}
		out.MaxItems = n
	}
	if raw := q.Get("max_bytes"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 || n > 524288 {
			return storedArtifactMetadataQuery{}, fmt.Errorf("max_bytes must be 2-524288")
		}
		out.MaxBytes = n
	}
	return out, nil
}

func decodeStoredArtifactBytesQuery(r *http.Request) (storedArtifactBytesQuery, error) {
	if r.Header.Get("Range") != "" {
		return storedArtifactBytesQuery{}, fmt.Errorf("Range is not supported for verified whole-artifact reads")
	}
	q := r.URL.Query()
	for key, values := range q {
		if (key != "id" && key != "publication_id") || len(values) != 1 || values[0] == "" {
			return storedArtifactBytesQuery{}, fmt.Errorf("exactly one non-empty id and publication_id are required")
		}
	}
	out := storedArtifactBytesQuery{ID: q.Get("id"), PublicationID: q.Get("publication_id")}
	if !validCanonicalUUID(out.ID) {
		return storedArtifactBytesQuery{}, fmt.Errorf("id must be a canonical UUID")
	}
	if !validNativePublicationID(out.PublicationID) {
		return storedArtifactBytesQuery{}, fmt.Errorf("publication_id must be a canonical native publication ID")
	}
	return out, nil
}

func validNativePublicationID(value string) bool {
	if len(value) == 64 {
		_, err := hex.DecodeString(value)
		return err == nil && value == strings.ToLower(value)
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func storedArtifactHTTPStatus(code string) int {
	switch code {
	case "invalid_request", "invalid_arguments", "invalid_cursor", "invalid_limits":
		return http.StatusBadRequest
	case "thread_not_found", "session_not_found", "artifact_not_found":
		return http.StatusNotFound
	case "owner_conflict", "unsupported_agent", "session_association_missing", "ambiguous_session", "stale_cursor", "artifact_unavailable", "runtime_active", "stale_replacement":
		return http.StatusConflict
	case "invalid_session", "unsupported_session_version", "invalid_bytes", "byte_count_mismatch", "digest_mismatch":
		return http.StatusUnprocessableEntity
	case "owner_not_allowed":
		return http.StatusForbidden
	case "native_unavailable", "owner_unavailable", "native_busy":
		return http.StatusServiceUnavailable
	case "unsupported_native_capability":
		return http.StatusNotImplemented
	case "native_timeout", "timeout":
		return http.StatusGatewayTimeout
	case "native_output_too_large", "invalid_native_response", "session_mismatch", "owner_routing_failed", "write_failed":
		return http.StatusBadGateway
	case "aborted":
		return http.StatusRequestTimeout
	case "read_failed", "owner_internal_error", "internal_error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func safeStoredArtifactMessage(code string) string {
	switch code {
	case "invalid_arguments":
		return "Pi rejected the stopped-artifact arguments"
	case "session_not_found":
		return "The associated Pi session is not available on its owner"
	case "ambiguous_session":
		return "Pi reported an ambiguous session association"
	case "invalid_cursor":
		return "The native artifact cursor is invalid"
	case "stale_cursor":
		return "The native artifact cursor is stale"
	case "invalid_session":
		return "The associated Pi session is invalid"
	case "unsupported_session_version":
		return "The associated Pi session version is unsupported"
	case "read_failed":
		return "Pi could not read the associated session"
	case "artifact_not_found":
		return "The native artifact publication is not on the current branch"
	case "artifact_unavailable":
		return "The native artifact bytes are not retained in the stopped session"
	case "invalid_limits":
		return "Pi rejected the stopped-artifact byte limits"
	case "invalid_bytes":
		return "The stopped artifact contains invalid bytes"
	case "byte_count_mismatch":
		return "The stopped artifact byte count does not match native metadata"
	case "digest_mismatch":
		return "The stopped artifact digest does not match native metadata"
	case "timeout":
		return "The native stopped-artifact copy timed out"
	case "aborted":
		return "The native stopped-artifact copy was aborted"
	case "write_failed":
		return "Pi could not complete its stopped-artifact output"
	case "internal_error":
		return "Pi reported an internal stopped-artifact error"
	default:
		return "The stopped-artifact operation failed"
	}
}

func writeStoredArtifactError(w http.ResponseWriter, status int, protocol, capability, threadID, owner, code, message string) {
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r", " "), "\n", " ")
	if utf8.RuneCountInString(message) > 512 {
		message = string([]rune(message)[:512])
	}
	writeJSON(w, status, api.BrowserStoredArtifactsResponse{
		Schema: api.SchemaVersion, ThreadID: threadID, OwnerMachine: owner,
		Protocol: protocol, Version: 1, Capability: capability, Status: "error",
		Error: &api.BrowserStoredHistoryError{Code: code, Message: message},
	})
}

func writeStoredArtifactMetadataError(w http.ResponseWriter, status int, threadID, owner, code, message string) {
	writeStoredArtifactError(w, status, "pi.session-artifacts", "stored_artifact_metadata", threadID, owner, code, message)
}

func writeStoredArtifactBytesError(w http.ResponseWriter, status int, threadID, owner, code, message string) {
	writeStoredArtifactError(w, status, "pi.session-artifact-bytes", "stored_inline_artifact_bytes", threadID, owner, code, message)
}

func acquireStoredArtifactSlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseStoredArtifactSlot(slots chan struct{}) { <-slots }

func (d *Daemon) handleBrowserStoredArtifacts(w http.ResponseWriter, r *http.Request) {
	query, err := decodeStoredArtifactMetadataQuery(r)
	if err != nil {
		writeStoredArtifactMetadataError(w, http.StatusBadRequest, "", "", "invalid_request", "browser stored artifacts: "+err.Error())
		return
	}
	owner, err := d.browserThreadOwner(query.ID)
	if err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredArtifactMetadataError(w, status, query.ID, "", code, "could not resolve the authoritative thread owner")
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredArtifactMetadataError(w, status, query.ID, owner, code, "the authoritative thread owner is not available to this browser hub")
		return
	}
	if owner != d.cfg.Machine {
		d.serveStoredArtifactsPeer(w, r, owner, query)
		return
	}
	d.serveLocalStoredArtifacts(r.Context(), w, query, owner)
}

func (d *Daemon) handleBrowserStoredArtifactBytes(w http.ResponseWriter, r *http.Request) {
	query, err := decodeStoredArtifactBytesQuery(r)
	if err != nil {
		writeStoredArtifactBytesError(w, http.StatusBadRequest, "", "", "invalid_request", "browser stored artifact bytes: "+err.Error())
		return
	}
	owner, err := d.browserThreadOwner(query.ID)
	if err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredArtifactBytesError(w, status, query.ID, "", code, "could not resolve the authoritative thread owner")
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredArtifactBytesError(w, status, query.ID, owner, code, "the authoritative thread owner is not available to this browser hub")
		return
	}
	if owner != d.cfg.Machine {
		d.serveStoredArtifactBytesPeer(w, r, owner, query)
		return
	}
	d.serveLocalStoredArtifactBytes(r.Context(), w, query, owner)
}

func (d *Daemon) resolveStoredArtifactSource(threadID string) (storedArtifactSource, string, string) {
	thread, err := d.store.GetThread(threadID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			return storedArtifactSource{}, "thread_not_found", "thread not found"
		}
		return storedArtifactSource{}, "owner_internal_error", "owner thread lookup failed"
	}
	if thread.AgentKind != string(agents.Pi) {
		return storedArtifactSource{}, "unsupported_agent", "stored artifacts are available only for Pi threads"
	}
	if !validCanonicalUUID(thread.AgentSessionID) {
		return storedArtifactSource{}, "session_association_missing", "Pi thread has no canonical associated session id"
	}
	stopped, err := d.storedArtifactThreadStopped(threadID)
	if err != nil {
		return storedArtifactSource{}, "owner_internal_error", "could not verify the Pi runtime state"
	}
	if !stopped {
		return storedArtifactSource{}, "runtime_active", "stopped artifact access requires the Pi runtime to be stopped"
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	path, found, err := agents.TranscriptPath(agents.Pi, thread.AgentSessionID, thread.Cwd, homes)
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
	return storedArtifactSource{threadID: threadID, sessionID: thread.AgentSessionID, path: path, info: info}, "", ""
}

func (d *Daemon) storedArtifactThreadStopped(threadID string) (bool, error) {
	if d.turnInFlight(threadID) {
		return false, nil
	}
	if d.tmux == nil {
		return false, fmt.Errorf("tmux runtime resolver is unavailable")
	}
	_, found, err := d.tmux.FindPaneByThreadID(threadID)
	if err != nil {
		return false, err
	}
	return !found, nil
}

func (d *Daemon) storedArtifactSourceUnchanged(source storedArtifactSource) bool {
	thread, err := d.store.GetThread(source.threadID)
	if err != nil || thread.AgentKind != string(agents.Pi) || thread.AgentSessionID != source.sessionID {
		return false
	}
	stopped, err := d.storedArtifactThreadStopped(source.threadID)
	if err != nil || !stopped {
		return false
	}
	info, err := os.Lstat(source.path)
	return err == nil && info.Mode().IsRegular() && os.SameFile(source.info, info) && info.Size() == source.info.Size() && info.ModTime().Equal(source.info.ModTime())
}

func (d *Daemon) serveLocalStoredArtifacts(ctx context.Context, w http.ResponseWriter, query storedArtifactMetadataQuery, owner string) {
	source, code, message := d.resolveStoredArtifactSource(query.ID)
	if code != "" {
		writeStoredArtifactMetadataError(w, storedArtifactHTTPStatus(code), query.ID, owner, code, message)
		return
	}
	if !acquireStoredArtifactSlot(storedArtifactProcessSlots) {
		writeStoredArtifactMetadataError(w, http.StatusServiceUnavailable, query.ID, owner, "native_busy", "native artifact process capacity is exhausted")
		return
	}
	defer releaseStoredArtifactSlot(storedArtifactProcessSlots)
	envelope, code, message := runPiStoredArtifacts(ctx, source.path, query)
	if code != "" {
		writeStoredArtifactMetadataError(w, storedArtifactHTTPStatus(code), query.ID, owner, code, message)
		return
	}
	if envelope.parsedSession.ID != source.sessionID {
		writeStoredArtifactMetadataError(w, http.StatusBadGateway, query.ID, owner, "session_mismatch", "Pi artifact metadata session does not match the thread association")
		return
	}
	if !d.storedArtifactSourceUnchanged(source) {
		writeStoredArtifactMetadataError(w, http.StatusConflict, query.ID, owner, "stale_replacement", "thread, session, runtime or session file changed during artifact metadata read")
		return
	}
	writeJSON(w, http.StatusOK, api.BrowserStoredArtifactsResponse{
		Schema: api.SchemaVersion, ThreadID: query.ID, OwnerMachine: owner,
		Protocol: envelope.Protocol, Version: envelope.Version, Capability: envelope.Capability, Status: envelope.Status,
		Session: envelope.parsedSession, Page: envelope.Page,
	})
}

func (d *Daemon) serveLocalStoredArtifactBytes(ctx context.Context, w http.ResponseWriter, query storedArtifactBytesQuery, owner string) {
	source, code, message := d.resolveStoredArtifactSource(query.ID)
	if code != "" {
		writeStoredArtifactBytesError(w, storedArtifactHTTPStatus(code), query.ID, owner, code, message)
		return
	}
	if !acquireStoredArtifactSlot(storedArtifactProcessSlots) {
		writeStoredArtifactBytesError(w, http.StatusServiceUnavailable, query.ID, owner, "native_busy", "native artifact process capacity is exhausted")
		return
	}
	defer releaseStoredArtifactSlot(storedArtifactProcessSlots)
	body, digest, code, message := runPiStoredArtifactBytes(ctx, source.path, query.PublicationID)
	if code != "" {
		writeStoredArtifactBytesError(w, storedArtifactHTTPStatus(code), query.ID, owner, code, message)
		return
	}
	if !d.storedArtifactSourceUnchanged(source) {
		writeStoredArtifactBytesError(w, http.StatusConflict, query.ID, owner, "stale_replacement", "thread, session, runtime or session file changed during artifact byte read")
		return
	}
	writeVerifiedStoredArtifactBytes(w, body, digest)
}

func runPiStoredArtifacts(parent context.Context, path string, query storedArtifactMetadataQuery) (nativeStoredArtifactEnvelope, string, string) {
	args := []string{"--session-artifacts-json", path}
	if query.Cursor != "" {
		args = append(args, "--session-artifact-cursor", query.Cursor)
	}
	if query.MaxItems != 0 {
		args = append(args, "--session-artifact-max-items", strconv.Itoa(query.MaxItems))
	}
	if query.MaxBytes != 0 {
		args = append(args, "--session-artifact-max-bytes", strconv.Itoa(query.MaxBytes))
	}
	ctx, cancel := context.WithTimeout(parent, storedArtifactMetadataTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pi", args...)
	var stdout cappedBuffer
	stdout.limit = storedArtifactMetadataMaxStdout + 1
	var stderr cappedBuffer
	stderr.limit = storedArtifactMaxStderr + 1
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nativeStoredArtifactEnvelope{}, "native_timeout", "Pi artifact metadata read exceeded 10 seconds"
	}
	if stdout.exceeded || len(stdout.Bytes()) > storedArtifactMetadataMaxStdout || stderr.exceeded || len(stderr.Bytes()) > storedArtifactMaxStderr {
		return nativeStoredArtifactEnvelope{}, "native_output_too_large", "Pi artifact metadata process exceeded its bounded output"
	}
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return nativeStoredArtifactEnvelope{}, "native_unavailable", "Pi executable is unavailable on the owner"
	}
	envelope, parseErr := decodeNativeStoredArtifactEnvelope(stdout.Bytes())
	if parseErr != nil {
		if runErr != nil {
			return nativeStoredArtifactEnvelope{}, "unsupported_native_capability", "installed Pi does not provide pi.session-artifacts version 1"
		}
		return nativeStoredArtifactEnvelope{}, "invalid_native_response", parseErr.Error()
	}
	if len(stderr.Bytes()) != 0 {
		return nativeStoredArtifactEnvelope{}, "invalid_native_response", "Pi artifact metadata mode wrote unexpected stderr"
	}
	if envelope.Protocol != "pi.session-artifacts" || envelope.Version != 1 || envelope.Capability != "stored_artifact_metadata" {
		return nativeStoredArtifactEnvelope{}, "unsupported_native_capability", "installed Pi does not provide pi.session-artifacts version 1"
	}
	switch envelope.Status {
	case "ok":
		if runErr != nil || envelope.Error != nil {
			return nativeStoredArtifactEnvelope{}, "invalid_native_response", "Pi artifact metadata success has the wrong exit status or error field"
		}
		session, err := decodeNativeStoredHistorySession(envelope.Session)
		if err != nil || !validNativeStoredArtifactPage(envelope.Page) {
			return nativeStoredArtifactEnvelope{}, "invalid_native_response", "Pi artifact metadata success has an invalid session or page"
		}
		envelope.parsedSession = session
		return envelope, "", ""
	case "error":
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 || envelope.Error == nil || len(envelope.Session) != 0 || len(envelope.Page) != 0 || !nativeStoredArtifactMetadataErrorCodes[envelope.Error.Code] || !validNativeHistoryMessage(envelope.Error.Message) {
			return nativeStoredArtifactEnvelope{}, "invalid_native_response", "Pi artifact metadata error envelope is invalid"
		}
		return nativeStoredArtifactEnvelope{}, envelope.Error.Code, safeStoredArtifactMessage(envelope.Error.Code)
	default:
		return nativeStoredArtifactEnvelope{}, "invalid_native_response", "Pi artifact metadata response has an unknown status"
	}
}

func runPiStoredArtifactBytes(parent context.Context, path, publicationID string) ([]byte, string, string, string) {
	args := []string{
		"--session-artifact-bytes", path,
		"--publication-id", publicationID,
		"--artifact-max-bytes", strconv.Itoa(storedArtifactMaxBytes),
		"--artifact-chunk-bytes", strconv.Itoa(storedArtifactChunkBytes),
		"--artifact-timeout-ms", strconv.Itoa(storedArtifactNativeTimeoutMS),
	}
	ctx, cancel := context.WithTimeout(parent, storedArtifactBytesTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pi", args...)
	var stdout cappedBuffer
	stdout.limit = storedArtifactMaxBytes + 1
	var stderr cappedBuffer
	stderr.limit = storedArtifactMaxStderr + 1
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, "", "native_timeout", "Pi stopped-artifact byte read exceeded 15 seconds"
	}
	if stdout.exceeded || len(stdout.Bytes()) > storedArtifactMaxBytes || stderr.exceeded || len(stderr.Bytes()) > storedArtifactMaxStderr {
		return nil, "", "native_output_too_large", "Pi stopped-artifact byte process exceeded its bounded output"
	}
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return nil, "", "native_unavailable", "Pi executable is unavailable on the owner"
	}
	if runErr != nil {
		// stdout may already contain bytes if an operating-system write failed after a
		// partial write. Never expose them: nonzero exit is failure regardless of stdout.
		nativeError, err := decodeNativeStoredArtifactByteError(stderr.Bytes())
		if err != nil {
			return nil, "", "unsupported_native_capability", "installed Pi does not provide pi.session-artifact-bytes version 1"
		}
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 || !nativeStoredArtifactByteErrorCodes[nativeError.Code] {
			return nil, "", "invalid_native_response", "Pi stopped-artifact byte failure is invalid"
		}
		return nil, "", nativeError.Code, safeStoredArtifactMessage(nativeError.Code)
	}
	if len(stderr.Bytes()) != 0 {
		return nil, "", "invalid_native_response", "Pi stopped-artifact byte success wrote unexpected stderr"
	}
	body := stdout.Bytes()
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), "", ""
}

func decodeNativeStoredArtifactEnvelope(raw []byte) (nativeStoredArtifactEnvelope, error) {
	raw, err := strictNativeJSONLine(raw, "Pi artifact metadata")
	if err != nil {
		return nativeStoredArtifactEnvelope{}, err
	}
	var envelope nativeStoredArtifactEnvelope
	if err := decodeStrictJSON(raw, &envelope); err != nil {
		return nativeStoredArtifactEnvelope{}, fmt.Errorf("invalid Pi artifact metadata JSON: %w", err)
	}
	return envelope, nil
}

func decodeNativeStoredArtifactByteError(raw []byte) (*api.BrowserStoredHistoryError, error) {
	raw, err := strictNativeJSONLine(raw, "Pi stopped-artifact byte failure")
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
	if err := decodeStrictJSON(raw, &envelope); err != nil || envelope.Protocol != "pi.session-artifact-bytes" || envelope.Version != 1 || envelope.Capability != "stored_inline_artifact_bytes" || envelope.Status != "error" || envelope.Error == nil || !validNativeHistoryMessage(envelope.Error.Message) {
		return nil, fmt.Errorf("invalid Pi stopped-artifact byte error envelope")
	}
	return envelope.Error, nil
}

func strictNativeJSONLine(raw []byte, name string) ([]byte, error) {
	if bytes.HasSuffix(raw, []byte("\n")) {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 || !utf8.Valid(raw) || bytes.ContainsAny(raw, "\r\n") || !bytes.Equal(raw, bytes.TrimSpace(raw)) {
		return nil, fmt.Errorf("%s did not return exactly one compact JSON line", name)
	}
	return raw, nil
}

func decodeStrictJSON(raw []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

type nativeArtifactPageWire struct {
	Scope      string                         `json:"scope"`
	Artifacts  []nativeArtifactProjectionWire `json:"artifacts"`
	Coverage   nativeArtifactCoverageWire     `json:"coverage"`
	NextCursor string                         `json:"nextCursor,omitempty"`
}

type nativeArtifactProjectionWire struct {
	PublicationID string          `json:"publicationId"`
	Kind          string          `json:"kind"`
	DisplayName   string          `json:"displayName"`
	MediaType     string          `json:"mediaType"`
	ByteLength    int64           `json:"byteLength"`
	SHA256        json.RawMessage `json:"sha256,omitempty"`
	Disposition   string          `json:"disposition"`
	Retention     string          `json:"retention"`
	Source        json.RawMessage `json:"source"`
	Origin        string          `json:"origin"`
	Availability  json.RawMessage `json:"availability"`
}

type nativeArtifactCoverageWire struct {
	Scope             string                       `json:"scope"`
	LeafID            json.RawMessage              `json:"leafId"`
	StartIndex        int                          `json:"startIndex"`
	EndIndexExclusive int                          `json:"endIndexExclusive"`
	TotalArtifacts    int                          `json:"totalArtifacts"`
	ByteLength        int                          `json:"byteLength"`
	HasOlder          bool                         `json:"hasOlder"`
	HasNewer          bool                         `json:"hasNewer"`
	OversizedArtifact *nativeArtifactOversizedWire `json:"oversizedArtifact,omitempty"`
}

type nativeArtifactOversizedWire struct {
	Index         int    `json:"index"`
	PublicationID string `json:"publicationId"`
	ByteLength    int    `json:"byteLength"`
}

func validNativeStoredArtifactPage(raw json.RawMessage) bool {
	var page nativeArtifactPageWire
	if len(raw) == 0 || decodeStrictJSON(raw, &page) != nil || page.Scope != "current_branch_artifacts" || len(page.Artifacts) > 500 || len(page.NextCursor) > storedArtifactMaxCursorBytes {
		return false
	}
	if page.NextCursor != "" {
		for _, r := range page.NextCursor {
			if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
				return false
			}
		}
	}
	c := page.Coverage
	if c.Scope != "current_branch_artifacts" || !validNullableString(c.LeafID) || c.StartIndex < 0 || c.EndIndexExclusive < c.StartIndex || c.TotalArtifacts < c.EndIndexExclusive || c.ByteLength < 2 || c.ByteLength > 524288 {
		return false
	}
	if c.OversizedArtifact != nil && (c.OversizedArtifact.Index < 0 || !validNativePublicationID(c.OversizedArtifact.PublicationID) || c.OversizedArtifact.ByteLength < 1) {
		return false
	}
	for i := range page.Artifacts {
		if !validNativeArtifactProjection(&page.Artifacts[i]) {
			return false
		}
	}
	return true
}

func validNativeArtifactProjection(a *nativeArtifactProjectionWire) bool {
	if !validNativePublicationID(a.PublicationID) || (a.Kind != "image" && a.Kind != "file") || !validArtifactDisplayName(a.DisplayName) || !validArtifactMediaType(a.MediaType) || a.ByteLength < 0 || a.ByteLength > storedArtifactMaxBytes || (a.Disposition != "inline" && a.Disposition != "attachment") || (a.Retention != "session_inline" && a.Retention != "runtime_source") || (a.Origin != "native" && a.Origin != "legacy_inline_image") {
		return false
	}
	hasSHA := len(a.SHA256) != 0
	if hasSHA {
		var digest string
		if decodeStrictJSON(a.SHA256, &digest) != nil || len(digest) != 64 {
			return false
		}
		if _, err := hex.DecodeString(digest); err != nil || digest != strings.ToLower(digest) {
			return false
		}
	}
	if a.Origin == "native" {
		if !hasSHA {
			return false
		}
		parsed, err := uuid.Parse(a.PublicationID)
		if err != nil || parsed.String() != a.PublicationID {
			return false
		}
	} else if len(a.PublicationID) != 64 || hasSHA {
		// Legacy deterministic inline-image projections intentionally omit SHA-256
		// until an explicit bounded byte copy computes it.
		return false
	}
	var source struct {
		Type             string `json:"type"`
		ToolCallID       string `json:"toolCallId"`
		ParentToolCallID string `json:"parentToolCallId,omitempty"`
		ContentIndex     *int   `json:"contentIndex,omitempty"`
		PublicationIndex *int   `json:"publicationIndex,omitempty"`
		EntryID          string `json:"entryId,omitempty"`
	}
	if decodeStrictJSON(a.Source, &source) != nil || source.ToolCallID == "" || source.EntryID == "" {
		return false
	}
	switch source.Type {
	case "tool_result_image":
		if a.Kind != "image" || a.Retention != "session_inline" || source.ContentIndex == nil || *source.ContentIndex < 0 || source.PublicationIndex != nil {
			return false
		}
	case "published_file":
		if a.Kind != "file" || a.Retention != "runtime_source" || source.PublicationIndex == nil || *source.PublicationIndex < 0 || source.ContentIndex != nil {
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
	if decodeStrictJSON(a.Availability, &availability) != nil {
		return false
	}
	if availability.Status == "available" {
		return availability.Reason == "" && availability.Message == ""
	}
	if availability.Status != "unavailable" || availability.Message == "" || strings.ContainsAny(availability.Message, "\r\n") || utf8.RuneCountInString(availability.Message) > 512 {
		return false
	}
	switch availability.Reason {
	case "source_not_retained", "source_failed", "invalid_bytes", "byte_limit", "lifecycle_ended":
		return true
	default:
		return false
	}
}

func validNullableString(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var value string
	return decodeStrictJSON(raw, &value) == nil
}

func validArtifactDisplayName(value string) bool {
	return value != "" && len([]byte(value)) <= 255 && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\") && !strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func validArtifactMediaType(value string) bool {
	if len(value) < 1 || len(value) > 127 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

func writeVerifiedStoredArtifactBytes(w http.ResponseWriter, body []byte, digest string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Content-Disposition", `attachment; filename="artifact"`)
	w.Header().Set("ETag", `"sha256-`+digest+`"`)
	w.Header().Set("X-Sesh-Artifact-SHA256", digest)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (d *Daemon) serveStoredArtifactsPeer(w http.ResponseWriter, r *http.Request, owner string, query storedArtifactMetadataQuery) {
	if !acquireStoredArtifactSlot(storedArtifactPeerSlots) {
		writeStoredArtifactMetadataError(w, http.StatusServiceUnavailable, query.ID, owner, "native_busy", "peer artifact copy capacity is exhausted")
		return
	}
	defer releaseStoredArtifactSlot(storedArtifactPeerSlots)
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeStoredArtifactMetadataError(w, browserOwnerStatus(err), query.ID, owner, "owner_routing_failed", "the artifact owner peer is unavailable")
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeStoredArtifactMetadataError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "browser hub peer authentication is unavailable")
		return
	}
	q := url.Values{"id": []string{query.ID}}
	if query.Cursor != "" {
		q.Set("cursor", query.Cursor)
	}
	if query.MaxItems != 0 {
		q.Set("max_items", strconv.Itoa(query.MaxItems))
	}
	if query.MaxBytes != 0 {
		q.Set("max_bytes", strconv.Itoa(query.MaxBytes))
	}
	ctx, cancel := context.WithTimeout(r.Context(), storedArtifactPeerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+peer.ApiAddr+"/v1/browser/threads/artifacts?"+q.Encode(), nil)
	if err != nil {
		writeStoredArtifactMetadataError(w, http.StatusInternalServerError, query.ID, owner, "owner_internal_error", "could not build artifact peer request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeStoredArtifactMetadataError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "artifact metadata peer request failed")
		return
	}
	copyStoredArtifactsPeerResponse(w, resp, query.ID, owner)
}

func validStoredArtifactPeerErrorCode(code string) bool {
	if nativeStoredArtifactMetadataErrorCodes[code] || nativeStoredArtifactByteErrorCodes[code] {
		return true
	}
	switch code {
	case "invalid_request", "thread_not_found", "owner_conflict", "unsupported_agent", "session_association_missing", "runtime_active", "stale_replacement", "session_mismatch", "owner_not_allowed", "owner_unavailable", "owner_routing_failed", "owner_internal_error", "native_unavailable", "native_busy", "unsupported_native_capability", "native_timeout", "native_output_too_large", "invalid_native_response":
		return true
	default:
		return false
	}
}

func safeStoredArtifactPeerMessage(code string) string {
	if nativeStoredArtifactMetadataErrorCodes[code] || nativeStoredArtifactByteErrorCodes[code] {
		return safeStoredArtifactMessage(code)
	}
	switch code {
	case "invalid_request":
		return "The stopped-artifact request is invalid"
	case "thread_not_found":
		return "The artifact thread was not found"
	case "owner_conflict":
		return "The artifact thread has conflicting ownership"
	case "unsupported_agent":
		return "Stopped artifacts are available only for Pi threads"
	case "session_association_missing":
		return "The Pi thread has no canonical associated session"
	case "runtime_active":
		return "Stopped artifact access requires the Pi runtime to be stopped"
	case "stale_replacement":
		return "The thread, session, runtime or session file changed during the artifact read"
	case "session_mismatch":
		return "Pi returned a session that does not match the thread association"
	case "owner_not_allowed":
		return "The artifact owner is not allowed by this browser hub"
	case "owner_unavailable", "native_unavailable", "native_busy":
		return "The artifact owner is temporarily unavailable"
	case "unsupported_native_capability":
		return "The owner Pi does not support the required stopped-artifact protocol"
	case "native_timeout":
		return "The owner stopped-artifact operation timed out"
	case "native_output_too_large", "invalid_native_response":
		return "The owner Pi returned an invalid stopped-artifact response"
	case "owner_routing_failed":
		return "The browser hub could not route to the artifact owner"
	default:
		return "The artifact owner could not complete the request"
	}
}

func copyStoredArtifactsPeerResponse(w http.ResponseWriter, resp *http.Response, threadID, owner string) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, storedArtifactMetadataMaxStdout+1))
	if err != nil || len(body) > storedArtifactMetadataMaxStdout {
		writeStoredArtifactMetadataError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "artifact metadata peer response exceeded its limit")
		return
	}
	var envelope api.BrowserStoredArtifactsResponse
	if decodeStrictJSON(body, &envelope) != nil || envelope.Schema != api.SchemaVersion || envelope.ThreadID != threadID || envelope.OwnerMachine != owner || envelope.Protocol != "pi.session-artifacts" || envelope.Version != 1 || envelope.Capability != "stored_artifact_metadata" {
		writeStoredArtifactMetadataError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "peer did not return the artifact metadata protocol")
		return
	}
	switch envelope.Status {
	case "ok":
		if resp.StatusCode != http.StatusOK || envelope.Session == nil || envelope.Error != nil || !validNativeStoredArtifactPage(envelope.Page) {
			writeStoredArtifactMetadataError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "peer returned invalid artifact metadata success")
			return
		}
	case "error":
		if resp.StatusCode < 400 || resp.StatusCode > 599 || envelope.Error == nil || envelope.Session != nil || len(envelope.Page) != 0 || !validNativeHistoryMessage(envelope.Error.Message) || !validStoredArtifactPeerErrorCode(envelope.Error.Code) {
			writeStoredArtifactMetadataError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "peer returned invalid artifact metadata error")
			return
		}
		writeStoredArtifactMetadataError(w, resp.StatusCode, threadID, owner, envelope.Error.Code, safeStoredArtifactPeerMessage(envelope.Error.Code))
		return
	default:
		writeStoredArtifactMetadataError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "peer returned unknown artifact metadata status")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (d *Daemon) serveStoredArtifactBytesPeer(w http.ResponseWriter, r *http.Request, owner string, query storedArtifactBytesQuery) {
	if !acquireStoredArtifactSlot(storedArtifactPeerSlots) {
		writeStoredArtifactBytesError(w, http.StatusServiceUnavailable, query.ID, owner, "native_busy", "peer artifact copy capacity is exhausted")
		return
	}
	defer releaseStoredArtifactSlot(storedArtifactPeerSlots)
	peer, err := d.browserPeer(owner)
	if err != nil {
		writeStoredArtifactBytesError(w, browserOwnerStatus(err), query.ID, owner, "owner_routing_failed", "the artifact owner peer is unavailable")
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeStoredArtifactBytesError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "browser hub peer authentication is unavailable")
		return
	}
	q := url.Values{"id": []string{query.ID}, "publication_id": []string{query.PublicationID}}
	ctx, cancel := context.WithTimeout(r.Context(), storedArtifactPeerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+peer.ApiAddr+"/v1/browser/threads/artifact-bytes?"+q.Encode(), nil)
	if err != nil {
		writeStoredArtifactBytesError(w, http.StatusInternalServerError, query.ID, owner, "owner_internal_error", "could not build artifact byte peer request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeStoredArtifactBytesError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "artifact byte peer request failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		copyStoredArtifactBytesPeerError(w, resp, query.ID, owner)
		return
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength < 0 || resp.ContentLength > storedArtifactMaxBytes || resp.Header.Get("Content-Type") != "application/octet-stream" {
		writeStoredArtifactBytesError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "peer returned invalid artifact byte headers")
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, storedArtifactMaxBytes+1))
	if err != nil || int64(len(body)) != resp.ContentLength || len(body) > storedArtifactMaxBytes {
		writeStoredArtifactBytesError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "peer artifact byte body did not match its bounded length")
		return
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if resp.Header.Get("X-Sesh-Artifact-SHA256") != digest || resp.Header.Get("ETag") != `"sha256-`+digest+`"` {
		writeStoredArtifactBytesError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "peer artifact byte digest did not validate")
		return
	}
	writeVerifiedStoredArtifactBytes(w, body, digest)
}

func copyStoredArtifactBytesPeerError(w http.ResponseWriter, resp *http.Response, threadID, owner string) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, storedArtifactMetadataMaxStdout+1))
	if err != nil || len(body) > storedArtifactMetadataMaxStdout {
		writeStoredArtifactBytesError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "artifact byte peer error exceeded its limit")
		return
	}
	var envelope api.BrowserStoredArtifactsResponse
	if decodeStrictJSON(body, &envelope) != nil || envelope.Schema != api.SchemaVersion || envelope.ThreadID != threadID || envelope.OwnerMachine != owner || envelope.Protocol != "pi.session-artifact-bytes" || envelope.Version != 1 || envelope.Capability != "stored_inline_artifact_bytes" || envelope.Status != "error" || envelope.Error == nil || envelope.Session != nil || len(envelope.Page) != 0 || !validNativeHistoryMessage(envelope.Error.Message) || !validStoredArtifactPeerErrorCode(envelope.Error.Code) || resp.StatusCode < 400 || resp.StatusCode > 599 {
		writeStoredArtifactBytesError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "peer did not return the artifact byte error protocol")
		return
	}
	writeStoredArtifactBytesError(w, resp.StatusCode, threadID, owner, envelope.Error.Code, safeStoredArtifactPeerMessage(envelope.Error.Code))
}
