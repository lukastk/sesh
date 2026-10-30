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
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/transfers"
)

const (
	liveInputPrivateProtocol      = "pi.input-image-read"
	liveInputPrivateVersion       = 1
	liveInputPrivateErrorMaxBytes = 16 << 10
	liveInputPrivateHeaderBytes   = 64 << 10
)

var liveInputMediaTypePattern = regexp.MustCompile(`^[A-Za-z0-9!#$&^_.+-]+/[A-Za-z0-9!#$&^_.+-]+$`)

type liveInputReadRequest struct {
	Protocol           string           `json:"protocol"`
	Version            int              `json:"version"`
	ThreadID           string           `json:"thread_id"`
	Target             transfers.Target `json:"target"`
	ListenerGeneration string           `json:"listener_generation"`
	InputImageID       string           `json:"input_image_id"`
}

type liveInputReadErrorEnvelope struct {
	Protocol string `json:"protocol"`
	Version  int    `json:"version"`
	Status   string `json:"status"`
	Error    struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type liveInputReadResult struct {
	Body         []byte
	MediaType    string
	Digest       string
	DigestStatus string
}

type liveInputPrivateErrorSpec struct {
	privateStatus int
	publicStatus  int
	publicCode    string
	message       string
}

var liveInputPrivateErrors = map[string]liveInputPrivateErrorSpec{
	"invalid_request":               {http.StatusBadRequest, http.StatusBadGateway, "invalid_native_response", "The live input-image reader rejected Sesh's private request"},
	"stale_target":                  {http.StatusConflict, http.StatusConflict, "stale_target", "The live Pi target changed during the input-image read"},
	"stale_branch":                  {http.StatusConflict, http.StatusConflict, "stale_branch", "The live Pi branch changed during the input-image read"},
	"stale_listener_generation":     {http.StatusConflict, http.StatusConflict, "stale_listener_generation", "The live input-image reader was replaced during the read"},
	"input_image_pending":           {http.StatusConflict, http.StatusConflict, "input_image_pending", "The input image is not durably associated"},
	"input_image_not_found":         {http.StatusNotFound, http.StatusNotFound, "input_image_not_found", "The input image is not on the current live branch"},
	"input_image_unavailable":       {http.StatusConflict, http.StatusConflict, "input_image_unavailable", "The input image bytes are unavailable"},
	"byte_limit":                    {http.StatusRequestEntityTooLarge, http.StatusRequestEntityTooLarge, "byte_limit", "The input image exceeds the live byte limit"},
	"invalid_native_metadata":       {http.StatusUnprocessableEntity, http.StatusBadGateway, "invalid_native_response", "The live Pi input-image metadata is invalid"},
	"byte_count_mismatch":           {http.StatusBadGateway, http.StatusBadGateway, "byte_count_mismatch", "The live input-image byte count did not validate"},
	"digest_mismatch":               {http.StatusBadGateway, http.StatusBadGateway, "digest_mismatch", "The live input-image digest did not validate"},
	"native_busy":                   {http.StatusServiceUnavailable, http.StatusServiceUnavailable, "native_busy", "The live input-image reader is at capacity"},
	"runtime_unavailable":           {http.StatusServiceUnavailable, http.StatusServiceUnavailable, "runtime_unavailable", "The live input-image runtime is unavailable"},
	"native_timeout":                {http.StatusGatewayTimeout, http.StatusGatewayTimeout, "native_timeout", "The live input-image operation timed out"},
	"unsupported_native_capability": {http.StatusNotImplemented, http.StatusNotImplemented, "unsupported_native_capability", "The owner Pi runtime does not support live input-image bytes"},
}

func (d *Daemon) inputMediaRuntimeActive(threadID, nativeSession string) (bool, string, string) {
	thread, err := d.store.GetThread(threadID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			return false, "thread_not_found", "thread not found"
		}
		return false, "owner_internal_error", "owner thread lookup failed"
	}
	if !piFamily(thread.AgentKind) {
		return false, "unsupported_agent", "input media is available only for pi/mypi threads"
	}
	if !validCanonicalUUID(thread.AgentSessionID) {
		return false, "session_association_missing", "Pi thread has no canonical associated session id"
	}
	if thread.AgentSessionID != nativeSession {
		return false, "stale_session", "requested native session is no longer associated with the thread"
	}
	stopped, err := d.storedArtifactThreadStopped(threadID)
	if err != nil {
		return false, "owner_internal_error", "could not verify the Pi runtime state"
	}
	return !stopped, "", ""
}

func mapLiveInputReaderAuthorityError(err error) (string, string) {
	switch transfers.ErrorCode(err) {
	case "unsupported_runtime_transfer":
		return "unsupported_native_capability", "The active Pi runtime does not advertise the live input-image reader"
	case "stale_target":
		return "stale_target", "The live Pi target does not match the thread session"
	case "storage_corrupt":
		return "invalid_native_response", "The live input-image reader marker is invalid"
	default:
		return "owner_internal_error", "The live input-image reader marker could not be read"
	}
}

func (d *Daemon) serveLocalLiveInputBytes(ctx context.Context, w http.ResponseWriter, method string, query storedInputBytesQuery, owner string) {
	if !acquireStoredInputSlot(storedInputProcessSlots) {
		writeStoredInputError(w, http.StatusServiceUnavailable, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "native_busy", "native input-image capacity is exhausted")
		return
	}
	defer releaseStoredInputSlot(storedInputProcessSlots)

	authority, err := d.transfers.ResolveInputImageReader(query.ThreadID, query.NativeSession)
	if err != nil {
		code, message := mapLiveInputReaderAuthorityError(err)
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	active, code, message := d.inputMediaRuntimeActive(query.ThreadID, query.NativeSession)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	if !active {
		writeStoredInputError(w, http.StatusConflict, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "stale_replacement", "The Pi runtime stopped before the live input-image read began")
		return
	}
	socketPath, ok := piRPCSocketPathWithSuffix(query.NativeSession, authority.Reader.SocketSuffix)
	if !ok {
		writeStoredInputError(w, http.StatusServiceUnavailable, storedInputBytesKind, query.ThreadID, owner, query.NativeSession, "runtime_unavailable", "The advertised live input-image reader socket is unavailable")
		return
	}
	result, code, message := readLiveInputImage(ctx, socketPath, authority, query)
	if code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	if code, message := d.revalidateLiveInputAuthority(query, owner, authority); code != "" {
		writeStoredInputError(w, storedInputHTTPStatus(code), storedInputBytesKind, query.ThreadID, owner, query.NativeSession, code, message)
		return
	}
	writeVerifiedStoredInputImage(w, method, result.Body, result.MediaType, result.Digest, result.DigestStatus)
}

func (d *Daemon) revalidateLiveInputAuthority(query storedInputBytesQuery, owner string, original transfers.InputImageReaderAuthority) (string, string) {
	currentOwner, err := d.browserThreadOwner(query.ThreadID)
	if err != nil || currentOwner != owner || owner != d.cfg.Machine {
		return "stale_replacement", "The input-image thread owner changed during the live read"
	}
	active, code, _ := d.inputMediaRuntimeActive(query.ThreadID, query.NativeSession)
	if code != "" || !active {
		return "stale_replacement", "The thread, session or runtime changed during the live input-image read"
	}
	current, err := d.transfers.ResolveInputImageReader(query.ThreadID, query.NativeSession)
	if err != nil {
		if transfers.ErrorCode(err) == "stale_target" {
			return "stale_target", "The live Pi target changed during the input-image read"
		}
		return "stale_listener_generation", "The live input-image reader marker changed during the read"
	}
	if current.Target != original.Target {
		return "stale_target", "The live Pi target changed during the input-image read"
	}
	if current.Reader != original.Reader {
		return "stale_listener_generation", "The live input-image reader generation changed during the read"
	}
	return "", ""
}

func readLiveInputImage(parent context.Context, socketPath string, authority transfers.InputImageReaderAuthority, query storedInputBytesQuery) (liveInputReadResult, string, string) {
	requestBody, err := json.Marshal(liveInputReadRequest{
		Protocol: liveInputPrivateProtocol, Version: liveInputPrivateVersion,
		ThreadID: query.ThreadID, Target: authority.Target,
		ListenerGeneration: authority.Reader.ListenerGeneration, InputImageID: query.InputImageID,
	})
	if err != nil || len(requestBody) == 0 || len(requestBody) > 8192 {
		return liveInputReadResult{}, "owner_internal_error", "The live input-image private request could not be encoded"
	}
	ctx, cancel := context.WithTimeout(parent, storedInputBytesTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://pi/v1/input-images/read", bytes.NewReader(requestBody))
	if err != nil {
		return liveInputReadResult{}, "owner_internal_error", "The live input-image private request could not be built"
	}
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{
		Proxy:                  nil,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		MaxResponseHeaderBytes: liveInputPrivateHeaderBytes,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return liveInputReadResult{}, "native_timeout", "The live input-image operation exceeded 12 seconds"
		}
		return liveInputReadResult{}, "runtime_unavailable", "The live input-image reader could not be reached"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return decodeLiveInputError(response)
	}
	return decodeLiveInputSuccess(response, authority, query)
}

func decodeLiveInputError(response *http.Response) (liveInputReadResult, string, string) {
	if response.ContentLength < 0 || response.ContentLength > liveInputPrivateErrorMaxBytes || len(response.TransferEncoding) != 0 {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned invalid error framing"
	}
	contentType, ok := firstExact(response.Header.Values("Content-Type"))
	if !ok || contentType != "application/json" || !exactResponseHeader(response, "Cache-Control", "no-store") || !exactResponseHeader(response, "X-Content-Type-Options", "nosniff") {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned invalid error headers"
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, liveInputPrivateErrorMaxBytes+1))
	if err != nil || int64(len(body)) != response.ContentLength || len(body) > liveInputPrivateErrorMaxBytes {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned an invalid bounded error"
	}
	var envelope liveInputReadErrorEnvelope
	if decodeStrictJSON(body, &envelope) != nil || envelope.Protocol != liveInputPrivateProtocol || envelope.Version != liveInputPrivateVersion || envelope.Status != "error" || !validNativeHistoryMessage(envelope.Error.Message) {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned a malformed error"
	}
	spec, ok := liveInputPrivateErrors[envelope.Error.Code]
	if !ok || response.StatusCode != spec.privateStatus {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned an unknown error"
	}
	return liveInputReadResult{}, spec.publicCode, spec.message
}

func decodeLiveInputSuccess(response *http.Response, authority transfers.InputImageReaderAuthority, query storedInputBytesQuery) (liveInputReadResult, string, string) {
	if response.ContentLength < 0 || response.ContentLength > storedInputMaxBytes || len(response.TransferEncoding) != 0 {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned invalid success framing"
	}
	allowedPrivate := map[string]bool{
		"x-pi-input-image-protocol": true, "x-pi-input-image-version": true,
		"x-pi-input-image-listener-generation": true, "x-pi-input-image-id": true,
		"x-pi-input-image-thread-id": true, "x-pi-input-image-stream-id": true,
		"x-pi-input-image-session-id": true, "x-pi-input-image-session-epoch": true,
		"x-pi-input-image-sha256": true, "x-pi-input-image-digest-status": true,
		"x-pi-input-image-canonical-sha256": true,
	}
	for name := range response.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-pi-input-image-") && !allowedPrivate[lower] {
			return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned an unknown private header"
		}
	}
	mediaType, ok := firstExact(response.Header.Values("Content-Type"))
	if !ok || len(mediaType) > 127 || !liveInputMediaTypePattern.MatchString(mediaType) || !exactResponseHeader(response, "Cache-Control", "no-store") || !exactResponseHeader(response, "X-Content-Type-Options", "nosniff") {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned invalid success headers"
	}
	required := map[string]string{
		"X-Pi-Input-Image-Protocol":            liveInputPrivateProtocol,
		"X-Pi-Input-Image-Version":             strconv.Itoa(liveInputPrivateVersion),
		"X-Pi-Input-Image-Listener-Generation": authority.Reader.ListenerGeneration,
		"X-Pi-Input-Image-ID":                  query.InputImageID,
		"X-Pi-Input-Image-Thread-ID":           query.ThreadID,
		"X-Pi-Input-Image-Stream-ID":           authority.Target.StreamID,
		"X-Pi-Input-Image-Session-ID":          authority.Target.SessionID,
		"X-Pi-Input-Image-Session-Epoch":       strconv.FormatInt(authority.Target.SessionEpoch, 10),
	}
	for name, want := range required {
		if !exactResponseHeader(response, name, want) {
			return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned mismatched identity headers"
		}
	}
	digest, ok := firstExact(response.Header.Values("X-Pi-Input-Image-SHA256"))
	if !ok || !validLowerSHA256(digest) {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned an invalid digest header"
	}
	digestStatus, ok := firstExact(response.Header.Values("X-Pi-Input-Image-Digest-Status"))
	if !ok || (digestStatus != "canonical-match" && digestStatus != "computed-only") {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader returned an invalid digest status"
	}
	canonicalValues := response.Header.Values("X-Pi-Input-Image-Canonical-SHA256")
	if digestStatus == "canonical-match" {
		canonical, ok := firstExact(canonicalValues)
		if !ok || !validLowerSHA256(canonical) || canonical != digest {
			return liveInputReadResult{}, "digest_mismatch", "The live input-image canonical digest did not validate"
		}
	} else if len(canonicalValues) != 0 {
		return liveInputReadResult{}, "invalid_native_response", "The live input-image reader mixed computed and canonical digest semantics"
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, storedInputMaxBytes+1))
	if err != nil || int64(len(body)) != response.ContentLength || len(body) > storedInputMaxBytes {
		return liveInputReadResult{}, "byte_count_mismatch", "The live input-image body did not match its bounded length"
	}
	sum := sha256.Sum256(body)
	computed := hex.EncodeToString(sum[:])
	if computed != digest {
		return liveInputReadResult{}, "digest_mismatch", "The live input-image body digest did not validate"
	}
	return liveInputReadResult{Body: body, MediaType: mediaType, Digest: computed, DigestStatus: digestStatus}, "", ""
}

func exactResponseHeader(response *http.Response, name, want string) bool {
	value, ok := firstExact(response.Header.Values(name))
	return ok && value == want
}

func init() {
	for code, spec := range liveInputPrivateErrors {
		if spec.publicCode == "" || spec.privateStatus < 400 || spec.publicStatus < 400 || storedInputHTTPStatus(spec.publicCode) != spec.publicStatus {
			panic(fmt.Sprintf("invalid live input-image error mapping for %s", code))
		}
	}
}
