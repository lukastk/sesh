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
	storedHistoryTimeout        = 10 * time.Second
	storedHistoryPeerTimeout    = 15 * time.Second
	storedHistoryMaxCursorBytes = 8192
	storedHistoryMaxStdout      = 8 << 20
	storedHistoryMaxStderr      = 64 << 10
)

var nativeStoredHistoryErrorCodes = map[string]bool{
	"invalid_arguments": true, "session_not_found": true, "ambiguous_session": true,
	"invalid_cursor": true, "stale_cursor": true, "invalid_session": true,
	"unsupported_session_version": true, "read_failed": true, "internal_error": true,
}

type storedHistoryQuery struct {
	ID       string
	Cursor   string
	MaxItems int
	MaxBytes int
}

type nativeStoredHistoryEnvelope struct {
	Protocol      string                         `json:"protocol"`
	Version       int                            `json:"version"`
	Capability    string                         `json:"capability"`
	Status        string                         `json:"status"`
	Session       json.RawMessage                `json:"session,omitempty"`
	Page          json.RawMessage                `json:"page,omitempty"`
	Error         *api.BrowserStoredHistoryError `json:"error,omitempty"`
	parsedSession *api.PiStoredHistorySession
}

type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < b.limit {
		remaining := b.limit - b.buf.Len()
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	if len(p) > 0 && b.buf.Len() >= b.limit {
		b.exceeded = b.exceeded || len(p) > b.limit || b.buf.Len() == b.limit
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

func decodeStoredHistoryQuery(r *http.Request) (storedHistoryQuery, error) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "id" && key != "cursor" && key != "max_items" && key != "max_bytes") || len(values) != 1 {
			return storedHistoryQuery{}, fmt.Errorf("only one id, cursor, max_items and max_bytes are allowed")
		}
	}
	out := storedHistoryQuery{ID: q.Get("id")}
	if !validCanonicalUUID(out.ID) {
		return storedHistoryQuery{}, fmt.Errorf("id must be a canonical UUID")
	}
	out.Cursor = q.Get("cursor")
	if len(out.Cursor) > storedHistoryMaxCursorBytes {
		return storedHistoryQuery{}, fmt.Errorf("cursor exceeds 8192 bytes")
	}
	if raw := q.Get("max_items"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			return storedHistoryQuery{}, fmt.Errorf("max_items must be 1-500")
		}
		out.MaxItems = n
	}
	if raw := q.Get("max_bytes"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 || n > 786432 {
			return storedHistoryQuery{}, fmt.Errorf("max_bytes must be 2-786432")
		}
		out.MaxBytes = n
	}
	return out, nil
}

func storedHistoryHTTPStatus(code string) int {
	switch code {
	case "invalid_request", "invalid_arguments", "invalid_cursor":
		return http.StatusBadRequest
	case "thread_not_found", "session_not_found":
		return http.StatusNotFound
	case "owner_conflict", "unsupported_agent", "session_association_missing", "ambiguous_session", "stale_cursor":
		return http.StatusConflict
	case "invalid_session", "unsupported_session_version":
		return http.StatusUnprocessableEntity
	case "owner_not_allowed":
		return http.StatusForbidden
	case "native_unavailable", "owner_unavailable":
		return http.StatusServiceUnavailable
	case "unsupported_native_capability":
		return http.StatusNotImplemented
	case "native_timeout":
		return http.StatusGatewayTimeout
	case "native_output_too_large", "invalid_native_response", "session_mismatch", "owner_routing_failed", "internal_error":
		return http.StatusBadGateway
	case "read_failed", "owner_internal_error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeStoredHistoryError(w http.ResponseWriter, status int, threadID, owner, code, message string) {
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r", " "), "\n", " ")
	if utf8.RuneCountInString(message) > 512 {
		runes := []rune(message)
		message = string(runes[:512])
	}
	writeJSON(w, status, api.BrowserStoredHistoryResponse{
		Schema: api.SchemaVersion, ThreadID: threadID, OwnerMachine: owner,
		Protocol: "pi.session-history", Version: 1, Capability: "stored_current_branch", Status: "error",
		Error: &api.BrowserStoredHistoryError{Code: code, Message: message},
	})
}

func storedHistoryOwnerError(err error) (int, string) {
	status := browserOwnerStatus(err)
	switch status {
	case http.StatusNotFound:
		return status, "thread_not_found"
	case http.StatusConflict:
		return status, "owner_conflict"
	case http.StatusForbidden:
		return status, "owner_not_allowed"
	case http.StatusServiceUnavailable:
		return status, "owner_unavailable"
	case http.StatusBadGateway:
		return status, "owner_routing_failed"
	default:
		return status, "owner_internal_error"
	}
}

func (d *Daemon) handleBrowserStoredHistory(w http.ResponseWriter, r *http.Request) {
	query, err := decodeStoredHistoryQuery(r)
	if err != nil {
		writeStoredHistoryError(w, http.StatusBadRequest, "", "", "invalid_request", "browser stored history: "+err.Error())
		return
	}
	owner, err := d.browserThreadOwner(query.ID)
	if err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredHistoryError(w, status, query.ID, "", code, err.Error())
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredHistoryError(w, status, query.ID, owner, code, err.Error())
		return
	}
	if owner != d.cfg.Machine {
		d.serveStoredHistoryPeer(w, r, owner, query)
		return
	}
	d.serveLocalStoredHistory(r.Context(), w, query, owner)
}

func (d *Daemon) serveLocalStoredHistory(ctx context.Context, w http.ResponseWriter, query storedHistoryQuery, owner string) {
	thread, err := d.store.GetThread(query.ID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeStoredHistoryError(w, http.StatusNotFound, query.ID, owner, "thread_not_found", err.Error())
			return
		}
		writeStoredHistoryError(w, http.StatusInternalServerError, query.ID, owner, "owner_internal_error", err.Error())
		return
	}
	if !piFamily(thread.AgentKind) {
		writeStoredHistoryError(w, http.StatusConflict, query.ID, owner, "unsupported_agent", "stored canonical history is available only for pi/mypi threads")
		return
	}
	if !validCanonicalUUID(thread.AgentSessionID) {
		writeStoredHistoryError(w, http.StatusConflict, query.ID, owner, "session_association_missing", "Pi thread has no canonical associated session id")
		return
	}
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	path, found, err := agents.TranscriptPath(agents.Kind(thread.AgentKind), thread.AgentSessionID, thread.Cwd, homes)
	if err != nil {
		writeStoredHistoryError(w, http.StatusInternalServerError, query.ID, owner, "read_failed", "resolve Pi session: "+err.Error())
		return
	}
	if !found {
		writeStoredHistoryError(w, http.StatusNotFound, query.ID, owner, "session_not_found", "associated Pi session file is not present on its owner")
		return
	}
	path, err = filepath.Abs(path)
	if err != nil {
		writeStoredHistoryError(w, http.StatusInternalServerError, query.ID, owner, "read_failed", "resolve absolute Pi session path: "+err.Error())
		return
	}
	envelope, code, message := runPiStoredHistory(ctx, agentExecutable(thread.AgentKind), path, query)
	if code != "" {
		writeStoredHistoryError(w, storedHistoryHTTPStatus(code), query.ID, owner, code, message)
		return
	}
	if envelope.parsedSession.ID != thread.AgentSessionID {
		writeStoredHistoryError(w, http.StatusBadGateway, query.ID, owner, "session_mismatch", "Pi stored-history response session does not match the thread's canonical association")
		return
	}
	writeJSON(w, http.StatusOK, api.BrowserStoredHistoryResponse{
		Schema: api.SchemaVersion, ThreadID: query.ID, OwnerMachine: owner,
		Protocol: envelope.Protocol, Version: envelope.Version, Capability: envelope.Capability, Status: envelope.Status,
		Session: envelope.parsedSession, Page: envelope.Page,
	})
}

func runPiStoredHistory(parent context.Context, exe, path string, query storedHistoryQuery) (nativeStoredHistoryEnvelope, string, string) {
	args := []string{"--session-history-json", path}
	if query.Cursor != "" {
		args = append(args, "--session-history-cursor", query.Cursor)
	}
	if query.MaxItems != 0 {
		args = append(args, "--session-history-max-items", strconv.Itoa(query.MaxItems))
	}
	if query.MaxBytes != 0 {
		args = append(args, "--session-history-max-bytes", strconv.Itoa(query.MaxBytes))
	}
	ctx, cancel := context.WithTimeout(parent, storedHistoryTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	var stdout cappedBuffer
	stdout.limit = storedHistoryMaxStdout + 1
	var stderr cappedBuffer
	stderr.limit = storedHistoryMaxStderr + 1
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nativeStoredHistoryEnvelope{}, "native_timeout", "Pi stored-history metadata read exceeded 10 seconds"
	}
	if stdout.exceeded || len(stdout.Bytes()) > storedHistoryMaxStdout || stderr.exceeded || len(stderr.Bytes()) > storedHistoryMaxStderr {
		return nativeStoredHistoryEnvelope{}, "native_output_too_large", "Pi stored-history process exceeded its bounded output"
	}
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return nativeStoredHistoryEnvelope{}, "native_unavailable", "Pi executable is unavailable on the owner"
	}
	envelope, parseErr := decodeNativeStoredHistory(stdout.Bytes())
	if parseErr != nil {
		if runErr != nil {
			return nativeStoredHistoryEnvelope{}, "unsupported_native_capability", "installed Pi does not provide pi.session-history version 1"
		}
		return nativeStoredHistoryEnvelope{}, "invalid_native_response", parseErr.Error()
	}
	if len(stderr.Bytes()) != 0 {
		return nativeStoredHistoryEnvelope{}, "invalid_native_response", "Pi stored-history mode wrote unexpected stderr"
	}
	if envelope.Protocol != "pi.session-history" || envelope.Version != 1 || envelope.Capability != "stored_current_branch" {
		return nativeStoredHistoryEnvelope{}, "unsupported_native_capability", "installed Pi does not provide pi.session-history version 1"
	}
	switch envelope.Status {
	case "ok":
		if runErr != nil || envelope.Error != nil {
			return nativeStoredHistoryEnvelope{}, "invalid_native_response", "Pi stored-history success envelope has the wrong exit status or error field"
		}
		session, sessionErr := decodeNativeStoredHistorySession(envelope.Session)
		if sessionErr != nil || !validNativeStoredHistoryPage(envelope.Page) {
			return nativeStoredHistoryEnvelope{}, "invalid_native_response", "Pi stored-history success envelope has an invalid session or page"
		}
		envelope.parsedSession = session
		return envelope, "", ""
	case "error":
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 || envelope.Error == nil || len(envelope.Session) != 0 || len(envelope.Page) != 0 || !nativeStoredHistoryErrorCodes[envelope.Error.Code] || !validNativeHistoryMessage(envelope.Error.Message) {
			return nativeStoredHistoryEnvelope{}, "invalid_native_response", "Pi stored-history error envelope is invalid"
		}
		return nativeStoredHistoryEnvelope{}, envelope.Error.Code, envelope.Error.Message
	default:
		return nativeStoredHistoryEnvelope{}, "invalid_native_response", "Pi stored-history response has an unknown status"
	}
}

func decodeNativeStoredHistory(raw []byte) (nativeStoredHistoryEnvelope, error) {
	if bytes.HasSuffix(raw, []byte("\n")) {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 || !utf8.Valid(raw) || bytes.ContainsAny(raw, "\r\n") || !bytes.Equal(raw, bytes.TrimSpace(raw)) {
		return nativeStoredHistoryEnvelope{}, fmt.Errorf("Pi stored-history mode did not return exactly one compact JSON line")
	}
	var envelope nativeStoredHistoryEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return nativeStoredHistoryEnvelope{}, fmt.Errorf("invalid Pi stored-history JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nativeStoredHistoryEnvelope{}, fmt.Errorf("Pi stored-history response has trailing data")
	}
	return envelope, nil
}

func decodeNativeStoredHistorySession(raw json.RawMessage) (*api.PiStoredHistorySession, error) {
	var wire struct {
		ID     string          `json:"id"`
		Cwd    string          `json:"cwd"`
		LeafID json.RawMessage `json:"leafId"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || dec.Decode(&wire) != nil || wire.ID == "" || wire.Cwd == "" || len(wire.LeafID) == 0 {
		return nil, fmt.Errorf("invalid stored-history session")
	}
	var leaf *string
	if !bytes.Equal(bytes.TrimSpace(wire.LeafID), []byte("null")) {
		var value string
		if json.Unmarshal(wire.LeafID, &value) != nil {
			return nil, fmt.Errorf("invalid stored-history leaf id")
		}
		leaf = &value
	}
	return &api.PiStoredHistorySession{ID: wire.ID, Cwd: wire.Cwd, LeafID: leaf}, nil
}

func validNativeStoredHistoryPage(raw json.RawMessage) bool {
	var page struct {
		Scope      string          `json:"scope"`
		Items      json.RawMessage `json:"items"`
		Coverage   json.RawMessage `json:"coverage"`
		NextCursor string          `json:"nextCursor,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || dec.Decode(&page) != nil || page.Scope != "current_branch" {
		return false
	}
	items := bytes.TrimSpace(page.Items)
	coverage := bytes.TrimSpace(page.Coverage)
	return len(items) >= 2 && items[0] == '[' && items[len(items)-1] == ']' && len(coverage) >= 2 && coverage[0] == '{' && coverage[len(coverage)-1] == '}'
}

func validNativeHistoryMessage(message string) bool {
	return message != "" && !strings.ContainsAny(message, "\r\n") && utf8.ValidString(message) && utf8.RuneCountInString(message) <= 512
}

func (d *Daemon) serveStoredHistoryPeer(w http.ResponseWriter, r *http.Request, owner string, query storedHistoryQuery) {
	peer, err := d.browserPeer(owner)
	if err != nil {
		status, code := storedHistoryOwnerError(err)
		writeStoredHistoryError(w, status, query.ID, owner, code, err.Error())
		return
	}
	token, err := peer.ResolveAPIToken()
	if err != nil {
		writeStoredHistoryError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "browser hub peer authentication is unavailable")
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
	target := "http://" + peer.ApiAddr + "/v1/browser/threads/history?" + q.Encode()
	ctx, cancel := context.WithTimeout(r.Context(), storedHistoryPeerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		writeStoredHistoryError(w, http.StatusInternalServerError, query.ID, owner, "owner_internal_error", "browser hub could not build the stored-history request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeStoredHistoryError(w, http.StatusBadGateway, query.ID, owner, "owner_routing_failed", "browser hub stored-history peer request failed")
		return
	}
	d.copyStoredHistoryPeerResponse(w, resp, query.ID, owner)
}

func (d *Daemon) copyStoredHistoryPeerResponse(w http.ResponseWriter, resp *http.Response, threadID, owner string) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, browserMaxForwardBody+1))
	if err != nil || len(body) > browserMaxForwardBody {
		writeStoredHistoryError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "browser hub stored-history peer response exceeded its limit")
		return
	}
	var envelope api.BrowserStoredHistoryResponse
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	decodeErr := dec.Decode(&envelope)
	if decodeErr == nil {
		decodeErr = dec.Decode(&struct{}{})
		if decodeErr == io.EOF {
			decodeErr = nil
		}
	}
	if decodeErr != nil || envelope.Schema != api.SchemaVersion || envelope.ThreadID != threadID || envelope.OwnerMachine != owner || envelope.Protocol != "pi.session-history" || envelope.Version != 1 || envelope.Capability != "stored_current_branch" {
		writeStoredHistoryError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "browser hub peer did not return the stored-history protocol")
		return
	}
	switch envelope.Status {
	case "ok":
		if resp.StatusCode != http.StatusOK || envelope.Session == nil || len(envelope.Page) == 0 || envelope.Error != nil {
			writeStoredHistoryError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "browser hub peer returned an invalid stored-history success")
			return
		}
	case "error":
		if resp.StatusCode < 400 || envelope.Error == nil || envelope.Session != nil || len(envelope.Page) != 0 {
			writeStoredHistoryError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "browser hub peer returned an invalid stored-history error")
			return
		}
	default:
		writeStoredHistoryError(w, http.StatusBadGateway, threadID, owner, "owner_routing_failed", "browser hub peer returned an unknown stored-history status")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
