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
type BrowserThreadActionRequest struct {
	ThreadID string   `json:"thread_id"`
	Action   string   `json:"action"`
	Name     *string  `json:"name,omitempty"`
	Add      []string `json:"add,omitempty"`
	Remove   []string `json:"remove,omitempty"`
	Parent   *string  `json:"parent,omitempty"`
	Archived *bool    `json:"archived,omitempty"`
	Force    *bool    `json:"force,omitempty"`
}
