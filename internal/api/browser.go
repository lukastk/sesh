package api

import "encoding/json"

// WSTicketRequest mints one short-lived, single-use browser WebSocket ticket.
// Route is "rpc" or "terminal"; Protocol is "live-v1" or "terminal-v1".
type WSTicketRequest struct {
	ThreadID string `json:"thread_id"`
	Route    string `json:"route"`
	Protocol string `json:"protocol"`
}

type WSTicketResponse struct {
	Schema          int    `json:"schema"`
	Ticket          string `json:"ticket"`
	ExpiresAtUnixMs int64  `json:"expires_at_unix_ms"`
	Path            string `json:"path"`
}

// BrowserThreadSummary is the browser-facing, cacheable projection of a Sesh
// thread plus owner-stamped runtime/freshness state.
type BrowserThreadSummary struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Agent             string   `json:"agent"`
	Machine           string   `json:"machine"`
	Cwd               string   `json:"cwd"`
	State             string   `json:"state"`
	UpdatedAtUnix     int64    `json:"updated_at_unix"`
	ParentID          string   `json:"parent_id,omitempty"`
	Tags              []string `json:"tags"`
	Archived          bool     `json:"archived"`
	RuntimeAvailable  bool     `json:"runtime_available"`
	TerminalAvailable bool     `json:"terminal_available"`
	Reachable         bool     `json:"reachable"`
	SyncedAtUnix      int64    `json:"synced_at_unix"`
	AgentSessionID    string   `json:"agent_session_id,omitempty"`
}

type BrowserThreadPage struct {
	Schema       int                    `json:"schema"`
	Capabilities []string               `json:"capabilities"`
	Threads      []BrowserThreadSummary `json:"threads"`
	NextCursor   string                 `json:"next_cursor,omitempty"`
}

// BrowserTranscriptCoverage describes exactly which zero-based native JSONL
// source lines are present. EndLine is exclusive. Source-file coverage is not a
// claim that the file contains an agent's complete logical branch history.
type BrowserTranscriptCoverage struct {
	Source        string `json:"source"`
	RequestedTail int    `json:"requested_tail"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	TotalLines    int    `json:"total_lines"`
	Complete      bool   `json:"complete"`
	ByteLimited   bool   `json:"byte_limited"`
}

// BrowserTranscriptResponse is a bounded owner-side window over the native
// transcript file. Lines remain raw and are not interpreted as a second agent
// history model.
type BrowserTranscriptResponse struct {
	Schema     int                       `json:"schema"`
	ID         string                    `json:"id"`
	Path       string                    `json:"path"`
	Format     string                    `json:"format"`
	Lines      []string                  `json:"lines"`
	LastReply  string                    `json:"last_reply"`
	ReplyCount int                       `json:"reply_count"`
	Coverage   BrowserTranscriptCoverage `json:"coverage"`
}

type PiStoredHistorySession struct {
	ID     string  `json:"id"`
	Cwd    string  `json:"cwd"`
	LeafID *string `json:"leafId"`
}

type BrowserStoredHistoryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BrowserStoredHistoryResponse wraps Pi's unchanged stored-current-branch
// session/page projection with Sesh thread/owner identity. Exactly one of
// Session+Page or Error is present according to Status.
type BrowserStoredHistoryResponse struct {
	Schema       int                        `json:"schema"`
	ThreadID     string                     `json:"thread_id,omitempty"`
	OwnerMachine string                     `json:"owner_machine,omitempty"`
	Protocol     string                     `json:"protocol"`
	Version      int                        `json:"version"`
	Capability   string                     `json:"capability"`
	Status       string                     `json:"status"`
	Session      *PiStoredHistorySession    `json:"session,omitempty"`
	Page         json.RawMessage            `json:"page,omitempty"`
	Error        *BrowserStoredHistoryError `json:"error,omitempty"`
}

// BrowserStoredArtifactsResponse wraps Pi's separate stopped artifact-metadata
// protocol. It does not reuse or alter pi.session-history version 1.
type BrowserStoredArtifactsResponse struct {
	Schema       int                        `json:"schema"`
	ThreadID     string                     `json:"thread_id,omitempty"`
	OwnerMachine string                     `json:"owner_machine,omitempty"`
	Protocol     string                     `json:"protocol"`
	Version      int                        `json:"version"`
	Capability   string                     `json:"capability"`
	Status       string                     `json:"status"`
	Session      *PiStoredHistorySession    `json:"session,omitempty"`
	Page         json.RawMessage            `json:"page,omitempty"`
	Error        *BrowserStoredHistoryError `json:"error,omitempty"`
}

// BrowserStoredInputMediaResponse wraps Pi's separate stopped input-only or media
// presentation, input-image metadata, or input-image byte protocols. Native protocol
// Version and Capability select the exact operation; it never changes or aliases
// pi.session-history version 1.
type BrowserStoredInputMediaResponse struct {
	Schema          int                        `json:"schema"`
	ThreadID        string                     `json:"thread_id,omitempty"`
	OwnerMachine    string                     `json:"owner_machine,omitempty"`
	NativeSessionID string                     `json:"native_session_id,omitempty"`
	Protocol        string                     `json:"protocol"`
	Version         int                        `json:"version"`
	Capability      string                     `json:"capability"`
	Status          string                     `json:"status"`
	Session         *PiStoredHistorySession    `json:"session,omitempty"`
	Page            json.RawMessage            `json:"page,omitempty"`
	Error           *BrowserStoredHistoryError `json:"error,omitempty"`
}

type TransferTarget struct {
	StreamID     string `json:"stream_id"`
	SessionID    string `json:"session_id"`
	SessionEpoch int64  `json:"session_epoch"`
}

type BrowserAttachmentStartRequest struct {
	ThreadID  string `json:"thread_id"`
	ClientID  string `json:"client_id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type,omitempty"`
}

type BrowserAttachmentMutationRequest struct {
	ThreadID string `json:"thread_id"`
	ID       string `json:"id"`
}

type BrowserAttachmentPrepareRequest struct {
	ThreadID      string         `json:"thread_id"`
	RequestID     string         `json:"request_id"`
	Target        TransferTarget `json:"target"`
	AttachmentIDs []string       `json:"attachment_ids"`
}

type BrowserAttachmentInfo struct {
	ID              string `json:"id"`
	ThreadID        string `json:"thread_id"`
	OwnerMachine    string `json:"owner_machine"`
	Name            string `json:"name"`
	MediaType       string `json:"media_type,omitempty"`
	Size            int64  `json:"size"`
	SHA256          string `json:"sha256"`
	State           string `json:"state"`
	NextOffset      int64  `json:"next_offset"`
	ChunkSize       int    `json:"chunk_size"`
	ExpiresAtUnixMS int64  `json:"expires_at_unix_ms"`
}

type BrowserAttachmentResponse struct {
	Schema     int                   `json:"schema"`
	Attachment BrowserAttachmentInfo `json:"attachment"`
}

type BrowserAttachmentDeleteResponse struct {
	Schema   int    `json:"schema"`
	ThreadID string `json:"thread_id"`
	ID       string `json:"id"`
	Deleted  bool   `json:"deleted"`
}

type BrowserAttachmentPrepareResponse struct {
	Schema          int      `json:"schema"`
	ThreadID        string   `json:"thread_id"`
	OwnerMachine    string   `json:"owner_machine"`
	RequestID       string   `json:"request_id"`
	LeaseIDs        []string `json:"lease_ids"`
	ExpiresAtUnixMS int64    `json:"expires_at_unix_ms"`
}

type RuntimeArtifactStartRequest struct {
	ThreadID    string         `json:"thread_id"`
	ClientID    string         `json:"client_id"`
	Target      TransferTarget `json:"target"`
	Name        string         `json:"name"`
	MediaType   string         `json:"media_type"`
	Size        int64          `json:"size"`
	SHA256      string         `json:"sha256"`
	Disposition string         `json:"disposition"`
}

type BrowserArtifactInfo struct {
	ID              string         `json:"id"`
	ThreadID        string         `json:"thread_id"`
	Target          TransferTarget `json:"target"`
	Name            string         `json:"name"`
	MediaType       string         `json:"media_type"`
	Size            int64          `json:"size"`
	SHA256          string         `json:"sha256"`
	Disposition     string         `json:"disposition"`
	ExpiresAtUnixMS int64          `json:"expires_at_unix_ms"`
}

type BrowserArtifactResponse struct {
	Schema   int                 `json:"schema"`
	Artifact BrowserArtifactInfo `json:"artifact"`
}

type NativeArtifactAvailability struct {
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type NativeArtifactPublication struct {
	PublicationID string                     `json:"publication_id"`
	Kind          string                     `json:"kind"`
	Name          string                     `json:"name"`
	MediaType     string                     `json:"media_type"`
	Size          int64                      `json:"size"`
	SHA256        *string                    `json:"sha256"`
	Disposition   string                     `json:"disposition"`
	Retention     string                     `json:"retention"`
	Origin        string                     `json:"origin"`
	Availability  NativeArtifactAvailability `json:"availability"`
}

type RuntimeArtifactMappingResolveRequest struct {
	ThreadID       string                    `json:"thread_id"`
	ConsumerTarget TransferTarget            `json:"consumer_target"`
	NativeSession  string                    `json:"native_session_id"`
	Publication    NativeArtifactPublication `json:"publication"`
}

type ArtifactMappingUnavailable struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RuntimeArtifactMappingView struct {
	MappingID       string                      `json:"mapping_id"`
	PublicationID   string                      `json:"publication_id"`
	Status          string                      `json:"status"`
	ProducerTarget  *TransferTarget             `json:"producer_target,omitempty"`
	ArtifactID      string                      `json:"artifact_id,omitempty"`
	Artifact        *BrowserArtifactInfo        `json:"artifact,omitempty"`
	Unavailable     *ArtifactMappingUnavailable `json:"unavailable,omitempty"`
	ExpiredAtUnixMS int64                       `json:"expired_at_unix_ms,omitempty"`
	UpdatedAtUnixMS int64                       `json:"updated_at_unix_ms"`
}

type RuntimeArtifactMappingResponse struct {
	Schema   int                        `json:"schema"`
	Protocol string                     `json:"protocol"`
	Version  int                        `json:"version"`
	Mapping  RuntimeArtifactMappingView `json:"mapping"`
}

type RuntimeArtifactMappingFailure struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RuntimeArtifactMappingFailRequest struct {
	ThreadID       string                        `json:"thread_id"`
	ConsumerTarget TransferTarget                `json:"consumer_target"`
	NativeSession  string                        `json:"native_session_id"`
	PublicationID  string                        `json:"publication_id"`
	Failure        RuntimeArtifactMappingFailure `json:"failure"`
}

type BrowserArtifactMappingsQueryRequest struct {
	ThreadID       string   `json:"thread_id"`
	NativeSession  string   `json:"native_session_id"`
	PublicationIDs []string `json:"publication_ids"`
}

type BrowserArtifactMappingItem struct {
	PublicationID   string                      `json:"publication_id"`
	Status          string                      `json:"status"`
	Artifact        *BrowserArtifactInfo        `json:"artifact,omitempty"`
	Unavailable     *ArtifactMappingUnavailable `json:"unavailable,omitempty"`
	ArtifactID      string                      `json:"artifact_id,omitempty"`
	ExpiredAtUnixMS int64                       `json:"expired_at_unix_ms,omitempty"`
	UpdatedAtUnixMS int64                       `json:"updated_at_unix_ms,omitempty"`
}

type BrowserArtifactMappingsQueryResponse struct {
	Schema        int                          `json:"schema"`
	Protocol      string                       `json:"protocol"`
	Version       int                          `json:"version"`
	ThreadID      string                       `json:"thread_id"`
	OwnerMachine  string                       `json:"owner_machine"`
	NativeSession string                       `json:"native_session_id"`
	Mappings      []BrowserArtifactMappingItem `json:"mappings"`
}

type BrowserTransferError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type BrowserTransferErrorResponse struct {
	Schema int                  `json:"schema"`
	Status string               `json:"status"`
	Error  BrowserTransferError `json:"error"`
}

// BrowserThreadCreateRequest routes one canonical NewThreadRequest to an
// explicit owner machine. An empty Machine means this daemon.
type BrowserThreadCreateRequest struct {
	Machine string           `json:"machine"`
	Request NewThreadRequest `json:"request"`
}

// BrowserThreadActionRequest is a narrow tagged union over existing Sesh verbs.
// Fields not used by Action are rejected by the daemon.
//
// Text/RequestID/OnTyping belong to the `send` and `send_headless` actions:
// RequestID is a caller-minted UUID that makes a retry idempotent within the
// owner's bounded recovery window, and OnTyping selects the live-pane typing
// policy ("defer", the default, or "skip"). The guard is never bypassed.
type BrowserThreadActionRequest struct {
	ThreadID  string   `json:"thread_id"`
	Action    string   `json:"action"`
	Name      *string  `json:"name,omitempty"`
	Add       []string `json:"add,omitempty"`
	Remove    []string `json:"remove,omitempty"`
	Parent    *string  `json:"parent,omitempty"`
	Archived  *bool    `json:"archived,omitempty"`
	Force     *bool    `json:"force,omitempty"`
	Text      *string  `json:"text,omitempty"`
	RequestID string   `json:"request_id,omitempty"`
	OnTyping  string   `json:"on_typing,omitempty"`
	Model     string   `json:"model,omitempty"`
}

// --- Sesh-owned basic display projection (capability browser.display-v1) ---

// BrowserDisplayTool is one bounded tool interaction inside a display item. A
// call carries Name/Input, a result carries Output/IsError, and CallID is the
// harness's own correlation id where it has one.
type BrowserDisplayTool struct {
	CallID          string `json:"call_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Input           string `json:"input,omitempty"`
	InputTruncated  bool   `json:"input_truncated,omitempty"`
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	IsError         bool   `json:"is_error,omitempty"`
}

// BrowserDisplayItem is one projected native conversation record.
//
// ID is stable for an append-only session file: the harness's own record id
// where it has one, else "L<seq>". Seq is the zero-based source line index and
// the ordering key. NativeType/NativeSubtype are the record's own type words, so
// a client can label a system record without the daemon synthesizing prose.
// AtUnixMS is omitted when the native record carries no timestamp. Injected
// marks a user-role record the HARNESS authored (codex context messages, claude
// isMeta records); nothing is hidden on its account.
type BrowserDisplayItem struct {
	ID                string               `json:"id"`
	Seq               int                  `json:"seq"`
	Role              string               `json:"role"`
	NativeType        string               `json:"native_type,omitempty"`
	NativeSubtype     string               `json:"native_subtype,omitempty"`
	AtUnixMS          int64                `json:"at_unix_ms,omitempty"`
	Text              string               `json:"text,omitempty"`
	TextTruncated     bool                 `json:"text_truncated,omitempty"`
	Thinking          string               `json:"thinking,omitempty"`
	ThinkingTruncated bool                 `json:"thinking_truncated,omitempty"`
	Tools             []BrowserDisplayTool `json:"tools,omitempty"`
	ToolsDropped      int                  `json:"tools_dropped,omitempty"`
	Injected          bool                 `json:"injected,omitempty"`
}

// BrowserDisplayCoverage states exactly what produced a display page.
//
// Source is always one resolved native session file; Format names that file's
// native format. StartLine is the first item's source line and EndLine the last
// one's plus one (both 0 on an empty page). Complete means every projected item
// in the FILE is in this page — never a claim that the file holds an agent's
// complete logical branch history. ByteLimited means the retained-byte budget,
// not the item limit, bounded the page. SkippedRecords counts parsed records
// with no conversation content (claude attachment/api-request lines, codex's
// duplicate item_completed events, pi model_change); UnparsedLines counts lines
// the decoder could not read at all, including a partial final write.
type BrowserDisplayCoverage struct {
	Source         string `json:"source"`
	Format         string `json:"format"`
	Path           string `json:"path"`
	RequestedLimit int    `json:"requested_limit"`
	Direction      string `json:"direction"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	TotalLines     int    `json:"total_lines"`
	Complete       bool   `json:"complete"`
	ByteLimited    bool   `json:"byte_limited"`
	SkippedRecords int    `json:"skipped_records"`
	UnparsedLines  int    `json:"unparsed_lines"`
}

// BrowserDisplayResponse is one bounded page of the Sesh-owned basic display
// projection over a thread's own conversation file.
//
// Source is the literal "sesh-display-v1": this is Sesh's own projection, not a
// harness-owned history contract. For pi/mypi, Pi's pi.session-history remains
// the authority for rich history; this route is the common basic view that also
// covers claude and codex, live, headless-idle and stopped.
//
// NewerCursor is ALWAYS present and is the forward-poll token. OlderCursor is
// present only when projected items exist before the returned window. Cursors
// are opaque and bound to this thread's current resolved source file: a rotated
// native session (claude compaction) refuses the old cursor loudly rather than
// paging a different file.
type BrowserDisplayResponse struct {
	Schema       int                    `json:"schema"`
	ID           string                 `json:"id"`
	OwnerMachine string                 `json:"owner_machine"`
	Source       string                 `json:"source"`
	Agent        string                 `json:"agent"`
	TurnStatus   string                 `json:"turn_status"`
	Items        []BrowserDisplayItem   `json:"items"`
	Coverage     BrowserDisplayCoverage `json:"coverage"`
	NewerCursor  string                 `json:"newer_cursor"`
	OlderCursor  string                 `json:"older_cursor,omitempty"`
}

// --- Browser send actions (capability browser.send-v1) ---

// The explicit outcomes of a browser send. `sent` means the delivery left Sesh,
// NOT that the agent finished (or even started) a turn.
const (
	BrowserSendSent          = "sent"
	BrowserSendDeferred      = "deferred"
	BrowserSendSkippedTyping = "skipped_typing"
	BrowserSendRefusedBusy   = "refused_busy"
	BrowserSendRefusedDead   = "refused_dead"
	BrowserSendUncertain     = "uncertain"
)

// BrowserSendResponse is the outcome of a `send` or `send_headless` browser
// action. Replayed reports that RequestID was already recorded and this is the
// stored outcome rather than a second delivery. Detail carries the daemon's own
// reason, so an explicit refusal keeps its exact cause.
type BrowserSendResponse struct {
	Schema       int    `json:"schema"`
	ThreadID     string `json:"thread_id"`
	OwnerMachine string `json:"owner_machine"`
	Action       string `json:"action"`
	RequestID    string `json:"request_id"`
	Outcome      string `json:"outcome"`
	Detail       string `json:"detail,omitempty"`
	Replayed     bool   `json:"replayed"`
	InputAgoSec  int64  `json:"input_ago_sec,omitempty"`
	DeadlineUnix int64  `json:"deadline_unix,omitempty"`
}
