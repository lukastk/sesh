package transfers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxAttachmentBytes       int64 = 16 << 20
	MaxArtifactBytes         int64 = 32 << 20
	MaxChunkBytes            int64 = 256 << 10
	MaxPrepareAttachments          = 8
	MaxPrepareBytes          int64 = 32 << 20
	MaxIncompleteCount             = 32
	MaxIncompleteBytes       int64 = 128 << 20
	MaxDurableBytes          int64 = 512 << 20
	MaxManagedRecords              = 4096
	MaxActivePrepareRequests       = 128
	MaxActiveLeases                = 1024
	ChunkSize                      = int(MaxChunkBytes)
	SweepLimit                     = 128
)

const InputImageReadCapability = "input-image-read-v1"

var (
	IncompleteTTL = 24 * time.Hour
	ReadyTTL      = 7 * 24 * time.Hour
	PreparedTTL   = 24 * time.Hour
	LeaseTTL      = 15 * time.Minute
	ArtifactTTL   = 24 * time.Hour
)

type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func fail(code, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "storage_error"
}

type Target struct {
	StreamID     string `json:"stream_id"`
	SessionID    string `json:"session_id"`
	SessionEpoch int64  `json:"session_epoch"`
}

type Attachment struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	ClientID      string `json:"client_id"`
	AuthorityID   string `json:"authority_id"`
	ThreadID      string `json:"thread_id"`
	OwnerMachine  string `json:"owner_machine"`
	Name          string `json:"name"`
	MediaType     string `json:"media_type,omitempty"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	State         string `json:"state"`
	NextOffset    int64  `json:"next_offset"`
	LastOffset    int64  `json:"last_offset,omitempty"`
	LastSize      int64  `json:"last_size,omitempty"`
	LastSHA256    string `json:"last_sha256,omitempty"`
	CreatedUnixMS int64  `json:"created_at_unix_ms"`
	UpdatedUnixMS int64  `json:"updated_at_unix_ms"`
	ExpiresUnixMS int64  `json:"expires_at_unix_ms"`
}

type Artifact struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	ClientID      string `json:"client_id"`
	ThreadID      string `json:"thread_id"`
	OwnerMachine  string `json:"owner_machine"`
	Target        Target `json:"target"`
	Name          string `json:"name"`
	MediaType     string `json:"media_type"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	Disposition   string `json:"disposition"`
	State         string `json:"state"`
	CreatedUnixMS int64  `json:"created_at_unix_ms"`
	UpdatedUnixMS int64  `json:"updated_at_unix_ms"`
	ExpiresUnixMS int64  `json:"expires_at_unix_ms"`
}

type InputImageReaderDescriptor struct {
	Protocol           string `json:"protocol"`
	Version            int    `json:"version"`
	ListenerGeneration string `json:"listener_generation"`
	SocketSuffix       string `json:"socket_suffix"`
}

type Consumer struct {
	Protocol         string                      `json:"protocol"`
	Version          int                         `json:"version"`
	ThreadID         string                      `json:"thread_id"`
	Target           Target                      `json:"target"`
	Capabilities     []string                    `json:"capabilities"`
	InputImageReader *InputImageReaderDescriptor `json:"input_image_reader,omitempty"`
}

type InputImageReaderAuthority struct {
	ThreadID string
	Target   Target
	Reader   InputImageReaderDescriptor
}

type Lease struct {
	Protocol      string `json:"protocol"`
	Version       int    `json:"version"`
	Type          string `json:"type"`
	LeaseID       string `json:"lease_id"`
	ThreadID      string `json:"thread_id"`
	AttachmentID  string `json:"attachment_id"`
	Target        Target `json:"target"`
	RequestID     string `json:"request_id"`
	Name          string `json:"name"`
	MediaType     string `json:"media_type,omitempty"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	Payload       string `json:"payload"`
	ExpiresUnixMS int64  `json:"expires_at_unix_ms"`
}

type PrepareRecord struct {
	Version       int      `json:"version"`
	ThreadID      string   `json:"thread_id"`
	RequestID     string   `json:"request_id"`
	Target        Target   `json:"target"`
	AttachmentIDs []string `json:"attachment_ids"`
	LeaseIDs      []string `json:"lease_ids"`
	ExpiresUnixMS int64    `json:"expires_at_unix_ms"`
}

type StartAttachment struct {
	ThreadID    string
	ClientID    string
	AuthorityID string
	Name        string
	MediaType   string
	Size        int64
	SHA256      string
}

type StartArtifact struct {
	ThreadID    string
	ClientID    string
	Target      Target
	Name        string
	MediaType   string
	Size        int64
	SHA256      string
	Disposition string
}

type Store struct {
	root    string
	machine string
	mu      sync.Mutex
	now     func() time.Time
}

func New(home, machine string) *Store {
	return &Store{root: filepath.Join(home, "browser-transfers"), machine: machine, now: time.Now}
}

func (s *Store) Root() string { return s.root }

func (s *Store) Inbox(threadID string) (string, error) {
	if !canonicalUUID(threadID) {
		return "", fail("invalid_request", "thread_id must be a canonical UUID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "runtime-inboxes", threadID)
	if err := secureMkdir(path); err != nil {
		return "", err
	}
	for _, child := range []string{"leases", "requests"} {
		if err := secureMkdir(filepath.Join(path, child)); err != nil {
			return "", err
		}
	}
	return filepath.Abs(path)
}

func (s *Store) StartAttachment(in StartAttachment) (Attachment, error) {
	if err := validateAttachmentInput(in); err != nil {
		return Attachment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	_, _ = s.sweepLocked(now, SweepLimit)
	attachments, artifacts, mappings, err := s.recordsLocked()
	if err != nil {
		return Attachment{}, err
	}
	if len(attachments)+len(artifacts)+len(mappings) >= MaxManagedRecords {
		return Attachment{}, fail("quota_exceeded", "managed record count exceeds safety limit")
	}
	for _, a := range attachments {
		if a.ClientID == in.ClientID && a.ThreadID == in.ThreadID && a.AuthorityID == in.AuthorityID {
			if a.Name == in.Name && a.MediaType == in.MediaType && a.Size == in.Size && a.SHA256 == in.SHA256 {
				return a, nil
			}
			return Attachment{}, fail("upload_conflict", "client_id already names an attachment with different immutable metadata")
		}
	}
	var incompleteCount int
	var incompleteBytes, total int64
	for _, a := range attachments {
		total += a.Size
		if a.State == "uploading" {
			incompleteCount++
			incompleteBytes += a.Size
		}
	}
	for _, a := range artifacts {
		total += a.Size
	}
	if incompleteCount >= MaxIncompleteCount || incompleteBytes+in.Size > MaxIncompleteBytes {
		return Attachment{}, fail("quota_exceeded", "incomplete upload quota exceeded")
	}
	if total+in.Size > MaxDurableBytes {
		return Attachment{}, fail("quota_exceeded", "owner durable transfer storage quota exceeded")
	}
	id := uuid.NewString()
	dir := filepath.Join(s.root, "attachments", id)
	if err := secureMkdir(dir); err != nil {
		return Attachment{}, err
	}
	payload := filepath.Join(dir, "payload")
	f, err := os.OpenFile(payload, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Attachment{}, fail("storage_error", "create attachment payload: %v", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.RemoveAll(dir)
		return Attachment{}, fail("storage_error", "protect attachment payload: %v", err)
	}
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return Attachment{}, fail("storage_error", "close attachment payload: %v", err)
	}
	ms := now.UnixMilli()
	a := Attachment{Version: 1, ID: id, ClientID: in.ClientID, AuthorityID: in.AuthorityID, ThreadID: in.ThreadID, OwnerMachine: s.machine, Name: in.Name, MediaType: in.MediaType, Size: in.Size, SHA256: in.SHA256, State: "uploading", CreatedUnixMS: ms, UpdatedUnixMS: ms, ExpiresUnixMS: now.Add(IncompleteTTL).UnixMilli()}
	if err := writeJSONAtomic(filepath.Join(dir, "metadata.json"), a); err != nil {
		_ = os.RemoveAll(dir)
		return Attachment{}, err
	}
	return a, nil
}

func (s *Store) Attachment(threadID, id string) (Attachment, error) {
	if !canonicalUUID(threadID) || !canonicalUUID(id) {
		return Attachment{}, fail("invalid_request", "thread_id and id must be canonical UUIDs")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.attachmentLocked(threadID, id)
	if err != nil {
		return Attachment{}, err
	}
	if a.ExpiresUnixMS <= s.now().UnixMilli() {
		return Attachment{}, fail("upload_expired", "attachment has expired")
	}
	return a, nil
}

func (s *Store) PutChunk(threadID, id string, offset int64, digest string, body io.Reader, length int64) (Attachment, error) {
	if length < 1 || length > MaxChunkBytes || !lowerHexDigest(digest) {
		return Attachment{}, fail("invalid_chunk", "chunk length or SHA-256 is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.attachmentLocked(threadID, id)
	if err != nil {
		return Attachment{}, err
	}
	if a.ExpiresUnixMS <= s.now().UnixMilli() {
		return Attachment{}, fail("upload_expired", "attachment has expired")
	}
	if a.State != "uploading" {
		return Attachment{}, fail("upload_not_ready", "attachment is not accepting chunks")
	}
	if offset < 0 || offset+length > a.Size {
		return Attachment{}, fail("invalid_chunk", "chunk exceeds the declared attachment size")
	}
	data, err := io.ReadAll(io.LimitReader(body, length+1))
	if err != nil || int64(len(data)) != length {
		return Attachment{}, fail("invalid_chunk", "chunk body length does not match Content-Length")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != digest {
		return Attachment{}, fail("digest_mismatch", "chunk SHA-256 does not match")
	}
	payload := filepath.Join(s.root, "attachments", id, "payload")
	if offset < a.NextOffset {
		if offset != a.LastOffset || length != a.LastSize || digest != a.LastSHA256 {
			return Attachment{}, fail("upload_conflict", "only the identical immediately previous chunk may be retried")
		}
		stored := make([]byte, length)
		f, err := safeOpenRegular(payload)
		if err != nil {
			return Attachment{}, err
		}
		_, rerr := f.ReadAt(stored, offset)
		_ = f.Close()
		if rerr != nil && rerr != io.EOF {
			return Attachment{}, fail("storage_error", "read stored retry chunk: %v", rerr)
		}
		if !bytes.Equal(stored, data) {
			return Attachment{}, fail("upload_conflict", "stored bytes differ from retried chunk")
		}
		return a, nil
	}
	if offset != a.NextOffset {
		return Attachment{}, fail("upload_conflict", "chunk offset must equal next_offset")
	}
	f, err := safeOpenWriteRegular(payload)
	if err != nil {
		return Attachment{}, err
	}
	if _, err := f.WriteAt(data, offset); err != nil {
		_ = f.Close()
		return Attachment{}, fail("storage_error", "write attachment chunk: %v", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return Attachment{}, fail("storage_error", "sync attachment chunk: %v", err)
	}
	if err := f.Close(); err != nil {
		return Attachment{}, fail("storage_error", "close attachment chunk: %v", err)
	}
	now := s.now()
	a.NextOffset += length
	a.LastOffset = offset
	a.LastSize = length
	a.LastSHA256 = digest
	a.UpdatedUnixMS = now.UnixMilli()
	a.ExpiresUnixMS = now.Add(IncompleteTTL).UnixMilli()
	if err := s.writeAttachmentLocked(a); err != nil {
		return Attachment{}, err
	}
	return a, nil
}

func (s *Store) Complete(threadID, id string) (Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.attachmentLocked(threadID, id)
	if err != nil {
		return Attachment{}, err
	}
	if a.ExpiresUnixMS <= s.now().UnixMilli() {
		return Attachment{}, fail("upload_expired", "attachment has expired")
	}
	if a.State == "ready" {
		size, digest, err := fileDigest(filepath.Join(s.root, "attachments", id, "payload"), MaxAttachmentBytes)
		if err != nil {
			return Attachment{}, err
		}
		if size != a.Size || digest != a.SHA256 {
			return Attachment{}, fail("storage_corrupt", "ready attachment payload does not match metadata")
		}
		return a, nil
	}
	if a.State != "uploading" || a.NextOffset != a.Size {
		return Attachment{}, fail("upload_not_ready", "attachment has not received its declared size")
	}
	payload := filepath.Join(s.root, "attachments", id, "payload")
	size, digest, err := fileDigest(payload, MaxAttachmentBytes)
	if err != nil {
		return Attachment{}, err
	}
	if size != a.Size || digest != a.SHA256 {
		return Attachment{}, fail("digest_mismatch", "completed attachment size or SHA-256 does not match")
	}
	now := s.now()
	a.State = "ready"
	a.UpdatedUnixMS = now.UnixMilli()
	a.ExpiresUnixMS = now.Add(ReadyTTL).UnixMilli()
	if err := s.writeAttachmentLocked(a); err != nil {
		return Attachment{}, err
	}
	return a, nil
}

func (s *Store) DeleteAttachment(threadID, id string) (bool, error) {
	if !canonicalUUID(threadID) || !canonicalUUID(id) {
		return false, fail("invalid_request", "thread_id and id must be canonical UUIDs")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.attachmentLocked(threadID, id)
	if err != nil {
		if ErrorCode(err) == "attachment_not_found" {
			return false, nil
		}
		return false, err
	}
	active, err := s.activeLeaseLocked(a.ThreadID, a.ID, s.now())
	if err != nil {
		return false, err
	}
	if active {
		return false, fail("live_lease_conflict", "attachment has a live prepared lease")
	}
	if err := os.RemoveAll(filepath.Join(s.root, "attachments", id)); err != nil {
		return false, fail("storage_error", "delete attachment: %v", err)
	}
	return true, nil
}

func (s *Store) Prepare(threadID, requestID string, target Target, attachmentIDs []string) (PrepareRecord, error) {
	if !canonicalUUID(threadID) || !canonicalUUID(requestID) || !validTarget(target) {
		return PrepareRecord{}, fail("invalid_request", "prepare thread, request and target are invalid")
	}
	if len(attachmentIDs) < 1 || len(attachmentIDs) > MaxPrepareAttachments {
		return PrepareRecord{}, fail("invalid_request", "prepare requires 1-8 attachments")
	}
	seen := map[string]bool{}
	for _, id := range attachmentIDs {
		if !canonicalUUID(id) || seen[id] {
			return PrepareRecord{}, fail("invalid_request", "attachment_ids must be unique canonical UUIDs")
		}
		seen[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	inbox := filepath.Join(s.root, "runtime-inboxes", threadID)
	consumer, err := readConsumer(filepath.Join(inbox, "consumer.json"))
	if err != nil {
		return PrepareRecord{}, err
	}
	if consumer.ThreadID != threadID || consumer.Target != target || !hasCapability(consumer.Capabilities, "attachment-lease-v1") {
		return PrepareRecord{}, fail("stale_target", "runtime transfer consumer does not match the exact target or capability")
	}
	reqPath := filepath.Join(inbox, "requests", requestID+".json")
	if existing, err := readStrict[PrepareRecord](reqPath); err == nil {
		if existing.ThreadID != threadID || existing.RequestID != requestID || existing.Target != target || !equalStrings(existing.AttachmentIDs, attachmentIDs) {
			return PrepareRecord{}, fail("upload_conflict", "request_id is already bound to different immutable prepare metadata")
		}
		if existing.ExpiresUnixMS <= now.UnixMilli() {
			return PrepareRecord{}, fail("upload_expired", "prepared request has expired and cannot be rebound")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return PrepareRecord{}, err
	}
	requestCount, leaseCount, err := s.activePrepareCountsLocked(now)
	if err != nil {
		return PrepareRecord{}, err
	}
	if requestCount >= MaxActivePrepareRequests || leaseCount+len(attachmentIDs) > MaxActiveLeases {
		return PrepareRecord{}, fail("quota_exceeded", "active prepared request or lease quota exceeded")
	}
	var total int64
	attachments := make([]Attachment, 0, len(attachmentIDs))
	for _, id := range attachmentIDs {
		a, err := s.attachmentLocked(threadID, id)
		if err != nil {
			return PrepareRecord{}, err
		}
		if a.ExpiresUnixMS <= now.UnixMilli() {
			return PrepareRecord{}, fail("upload_expired", "attachment %s has expired", id)
		}
		if a.State != "ready" {
			return PrepareRecord{}, fail("upload_not_ready", "attachment %s is not ready", id)
		}
		total += a.Size
		if total > MaxPrepareBytes {
			return PrepareRecord{}, fail("quota_exceeded", "prepared attachment bytes exceed 32 MiB")
		}
		size, digest, err := fileDigest(filepath.Join(s.root, "attachments", a.ID, "payload"), MaxAttachmentBytes)
		if err != nil {
			return PrepareRecord{}, err
		}
		if size != a.Size || digest != a.SHA256 {
			return PrepareRecord{}, fail("storage_corrupt", "ready attachment payload does not match metadata")
		}
		attachments = append(attachments, a)
	}
	expires := now.Add(LeaseTTL).UnixMilli()
	leases := make([]string, 0, len(attachments))
	created := []string{}
	for _, a := range attachments {
		lid := uuid.NewString()
		ldir := filepath.Join(inbox, "leases", lid)
		if err := secureMkdir(ldir); err != nil {
			cleanupDirs(created)
			return PrepareRecord{}, err
		}
		created = append(created, ldir)
		if err := os.Link(filepath.Join(s.root, "attachments", a.ID, "payload"), filepath.Join(ldir, "payload")); err != nil {
			cleanupDirs(created)
			return PrepareRecord{}, fail("storage_error", "link lease payload: %v", err)
		}
		lease := Lease{Protocol: "sesh.pi-transfer", Version: 1, Type: "attachment-lease", LeaseID: lid, ThreadID: threadID, AttachmentID: a.ID, Target: target, RequestID: requestID, Name: a.Name, MediaType: a.MediaType, Size: a.Size, SHA256: a.SHA256, Payload: "payload", ExpiresUnixMS: expires}
		if err := writeJSONAtomic(filepath.Join(ldir, "lease.json"), lease); err != nil {
			cleanupDirs(created)
			return PrepareRecord{}, err
		}
		leases = append(leases, lid)
	}
	rec := PrepareRecord{Version: 1, ThreadID: threadID, RequestID: requestID, Target: target, AttachmentIDs: append([]string(nil), attachmentIDs...), LeaseIDs: leases, ExpiresUnixMS: expires}
	if err := writeJSONAtomic(reqPath, rec); err != nil {
		cleanupDirs(created)
		return PrepareRecord{}, err
	}
	for _, a := range attachments {
		a.ExpiresUnixMS = now.Add(PreparedTTL).UnixMilli()
		a.UpdatedUnixMS = now.UnixMilli()
		if err := s.writeAttachmentLocked(a); err != nil {
			return PrepareRecord{}, err
		}
	}
	return rec, nil
}

func (s *Store) StartArtifact(in StartArtifact) (Artifact, error) {
	if !canonicalUUID(in.ThreadID) || !canonicalUUID(in.ClientID) || !validTarget(in.Target) || in.Size < 0 || in.Size > MaxArtifactBytes || !lowerHexDigest(in.SHA256) || validateName(in.Name) != nil || in.MediaType == "" || validateMediaType(in.MediaType) != nil || (in.Disposition != "inline" && in.Disposition != "attachment") {
		return Artifact{}, fail("invalid_request", "artifact metadata is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	_, _ = s.sweepLocked(now, SweepLimit)
	attachments, artifacts, mappings, err := s.recordsLocked()
	if err != nil {
		return Artifact{}, err
	}
	for _, a := range artifacts {
		if a.ClientID == in.ClientID && a.ThreadID == in.ThreadID {
			if a.Target == in.Target && a.Name == in.Name && a.MediaType == in.MediaType && a.Size == in.Size && a.SHA256 == in.SHA256 && a.Disposition == in.Disposition {
				return a, nil
			}
			return Artifact{}, fail("upload_conflict", "artifact client_id has conflicting immutable metadata")
		}
	}
	for _, mapping := range mappings {
		if mapping.ClientID == in.ClientID {
			return Artifact{}, fail("upload_conflict", "artifact client_id is reserved by a durable native mapping")
		}
	}
	return s.createArtifactLocked(uuid.NewString(), in, attachments, artifacts, mappings)
}

func (s *Store) PutArtifact(threadID, id string, body io.Reader, length int64) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.artifactLocked(threadID, id)
	if err != nil {
		return Artifact{}, err
	}
	return s.putArtifactLocked(a, body, length)
}

func (s *Store) Artifact(threadID, id string) (Artifact, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.artifactLocked(threadID, id)
	if err != nil {
		return Artifact{}, "", err
	}
	if a.ExpiresUnixMS <= s.now().UnixMilli() {
		return Artifact{}, "", fail("upload_expired", "artifact has expired")
	}
	if a.State != "ready" {
		return Artifact{}, "", fail("artifact_not_ready", "artifact is not ready")
	}
	path := filepath.Join(s.root, "artifacts", id, "payload")
	size, digest, err := fileDigest(path, MaxArtifactBytes)
	if err != nil {
		return Artifact{}, "", err
	}
	if size != a.Size || digest != a.SHA256 {
		return Artifact{}, "", fail("storage_corrupt", "artifact payload does not match metadata")
	}
	return a, path, nil
}

func (s *Store) ArtifactRecord(threadID, id string) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.artifactLocked(threadID, id)
	if err != nil {
		return Artifact{}, err
	}
	if a.ExpiresUnixMS <= s.now().UnixMilli() {
		return Artifact{}, fail("upload_expired", "artifact publication has expired")
	}
	return a, nil
}

func (s *Store) ValidateConsumer(threadID string, target Target, capability string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validateConsumerLocked(threadID, target, capability)
}

func (s *Store) ResolveInputImageReader(threadID, nativeSessionID string) (InputImageReaderAuthority, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := readConsumer(filepath.Join(s.root, "runtime-inboxes", threadID, "consumer.json"))
	if err != nil {
		return InputImageReaderAuthority{}, err
	}
	if c.ThreadID != threadID || c.Target.SessionID != nativeSessionID {
		return InputImageReaderAuthority{}, fail("stale_target", "runtime input-image reader does not match the exact thread/session")
	}
	if !hasCapability(c.Capabilities, InputImageReadCapability) || c.InputImageReader == nil {
		return InputImageReaderAuthority{}, fail("unsupported_runtime_transfer", "runtime has no registered live input-image reader")
	}
	if !canonicalUUID(c.Target.SessionID) || !safeIdentityText(c.Target.StreamID, 128) || c.Target.SessionEpoch > 1<<53-1 {
		return InputImageReaderAuthority{}, fail("storage_corrupt", "runtime input-image reader target is invalid")
	}
	return InputImageReaderAuthority{ThreadID: c.ThreadID, Target: c.Target, Reader: *c.InputImageReader}, nil
}

func (s *Store) validateConsumerLocked(threadID string, target Target, capability string) error {
	c, err := readConsumer(filepath.Join(s.root, "runtime-inboxes", threadID, "consumer.json"))
	if err != nil {
		return err
	}
	if c.ThreadID != threadID || c.Target != target || !hasCapability(c.Capabilities, capability) {
		return fail("stale_target", "runtime transfer consumer does not match exact target/capability")
	}
	return nil
}

func (s *Store) Sweep(now time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepLocked(now, limit)
}

func (s *Store) sweepLocked(now time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	removed := 0
	for _, kind := range []string{"attachments", "artifacts"} {
		entries, err := os.ReadDir(filepath.Join(s.root, kind))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fail("storage_error", "scan %s: %v", kind, err)
		}
		for _, e := range entries {
			if removed >= limit {
				return removed, nil
			}
			if !e.IsDir() || !canonicalUUID(e.Name()) {
				continue
			}
			meta := filepath.Join(s.root, kind, e.Name(), "metadata.json")
			var expires int64
			threadID := ""
			if kind == "attachments" {
				a, err := readStrict[Attachment](meta)
				if err != nil {
					continue
				}
				expires = a.ExpiresUnixMS
				threadID = a.ThreadID
			} else {
				a, err := readStrict[Artifact](meta)
				if err != nil {
					continue
				}
				expires = a.ExpiresUnixMS
			}
			if expires > 0 && expires <= now.UnixMilli() {
				if kind == "attachments" {
					active, err := s.anyLeaseLocked(threadID, e.Name())
					if err != nil {
						return removed, err
					}
					if active {
						continue
					}
				}
				if kind == "artifacts" {
					if err := s.expireMappingForArtifactLocked(e.Name(), now.UnixMilli()); err != nil {
						return removed, err
					}
				}
				if err := os.RemoveAll(filepath.Dir(meta)); err != nil {
					return removed, fail("storage_error", "remove expired %s: %v", kind, err)
				}
				removed++
			}
		}
	}
	inboxes, err := os.ReadDir(filepath.Join(s.root, "runtime-inboxes"))
	if err != nil && !os.IsNotExist(err) {
		return removed, fail("storage_error", "scan runtime inboxes: %v", err)
	}
	for _, in := range inboxes {
		if removed >= limit {
			break
		}
		if !in.IsDir() || !canonicalUUID(in.Name()) {
			continue
		}
		base := filepath.Join(s.root, "runtime-inboxes", in.Name())
		leases, _ := os.ReadDir(filepath.Join(base, "leases"))
		for _, e := range leases {
			if removed >= limit {
				break
			}
			lease, err := readStrict[Lease](filepath.Join(base, "leases", e.Name(), "lease.json"))
			if err == nil && lease.ExpiresUnixMS <= now.UnixMilli() {
				_ = os.RemoveAll(filepath.Join(base, "leases", e.Name()))
				removed++
			}
		}
		reqs, _ := os.ReadDir(filepath.Join(base, "requests"))
		for _, e := range reqs {
			if removed >= limit {
				break
			}
			r, err := readStrict[PrepareRecord](filepath.Join(base, "requests", e.Name()))
			if err == nil && r.ExpiresUnixMS <= now.UnixMilli() {
				_ = os.Remove(filepath.Join(base, "requests", e.Name()))
				removed++
			}
		}
	}
	return removed, nil
}

func (s *Store) recordsLocked() ([]Attachment, []Artifact, []ArtifactMapping, error) {
	mappings, _, err := s.mappingRecordsLocked()
	if err != nil {
		return nil, nil, nil, err
	}
	var attachments []Attachment
	var artifacts []Artifact
	count := len(mappings)
	if count > MaxManagedRecords {
		return nil, nil, nil, fail("quota_exceeded", "managed record count exceeds safety limit")
	}
	for _, kind := range []string{"attachments", "artifacts"} {
		entries, err := os.ReadDir(filepath.Join(s.root, kind))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, nil, fail("storage_error", "scan managed records: %v", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			count++
			if count > MaxManagedRecords {
				return nil, nil, nil, fail("quota_exceeded", "managed record count exceeds safety limit")
			}
			if kind == "attachments" {
				attachment, err := readStrict[Attachment](filepath.Join(s.root, kind, entry.Name(), "metadata.json"))
				if err != nil || !validAttachmentRecord(attachment) || attachment.ID != entry.Name() || !managedPayloadSize(filepath.Join(s.root, kind, entry.Name(), "payload"), attachment.Size, attachment.State == "ready") {
					return nil, nil, nil, fail("storage_corrupt", "invalid attachment record or payload")
				}
				attachments = append(attachments, attachment)
				continue
			}
			artifact, err := readStrict[Artifact](filepath.Join(s.root, kind, entry.Name(), "metadata.json"))
			if err != nil || !validArtifactRecord(artifact) || artifact.ID != entry.Name() || !managedPayloadSize(filepath.Join(s.root, kind, entry.Name(), "payload"), artifact.Size, artifact.State == "ready") {
				return nil, nil, nil, fail("storage_corrupt", "invalid artifact record or payload")
			}
			artifacts = append(artifacts, artifact)
		}
	}
	return attachments, artifacts, mappings, nil
}

func (s *Store) attachmentLocked(threadID, id string) (Attachment, error) {
	a, err := readStrict[Attachment](filepath.Join(s.root, "attachments", id, "metadata.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Attachment{}, fail("attachment_not_found", "attachment not found")
	}
	if err != nil {
		return Attachment{}, fail("storage_corrupt", "attachment metadata is invalid")
	}
	if !validAttachmentRecord(a) {
		return Attachment{}, fail("storage_corrupt", "attachment metadata is invalid")
	}
	if a.ID != id || a.ThreadID != threadID || a.OwnerMachine != s.machine {
		return Attachment{}, fail("attachment_not_found", "attachment not found for thread")
	}
	return a, nil
}
func (s *Store) artifactLocked(threadID, id string) (Artifact, error) {
	if !canonicalUUID(threadID) || !canonicalUUID(id) {
		return Artifact{}, fail("invalid_request", "thread_id and id must be canonical UUIDs")
	}
	a, err := readStrict[Artifact](filepath.Join(s.root, "artifacts", id, "metadata.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Artifact{}, fail("artifact_not_found", "artifact not found")
	}
	if err != nil {
		return Artifact{}, fail("storage_corrupt", "artifact metadata is invalid")
	}
	if a.ID != id || a.ThreadID != threadID || a.OwnerMachine != s.machine {
		return Artifact{}, fail("artifact_not_found", "artifact not found for thread")
	}
	if !validArtifactRecord(a) {
		return Artifact{}, fail("storage_corrupt", "artifact metadata is invalid")
	}
	return a, nil
}
func (s *Store) writeAttachmentLocked(a Attachment) error {
	return writeJSONAtomic(filepath.Join(s.root, "attachments", a.ID, "metadata.json"), a)
}
func (s *Store) writeArtifactLocked(a Artifact) error {
	return writeJSONAtomic(filepath.Join(s.root, "artifacts", a.ID, "metadata.json"), a)
}
func (s *Store) anyLeaseLocked(threadID, attachmentID string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "runtime-inboxes", threadID, "leases"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		lease, err := readStrict[Lease](filepath.Join(s.root, "runtime-inboxes", threadID, "leases", entry.Name(), "lease.json"))
		if err == nil && lease.AttachmentID == attachmentID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) activeLeaseLocked(threadID, attachmentID string, now time.Time) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "runtime-inboxes", threadID, "leases"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, e := range entries {
		leaseDir := filepath.Join(s.root, "runtime-inboxes", threadID, "leases", e.Name())
		l, err := readStrict[Lease](filepath.Join(leaseDir, "lease.json"))
		if err != nil || l.AttachmentID != attachmentID {
			continue
		}
		if l.ExpiresUnixMS > now.UnixMilli() {
			return true, nil
		}
		if err := os.RemoveAll(leaseDir); err != nil {
			return false, fail("storage_error", "remove expired attachment lease: %v", err)
		}
	}
	return false, nil
}

func (s *Store) activePrepareCountsLocked(now time.Time) (int, int, error) {
	inboxes, err := os.ReadDir(filepath.Join(s.root, "runtime-inboxes"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fail("storage_error", "scan runtime inboxes: %v", err)
	}
	requests, leases, scanned := 0, 0, 0
	for _, inbox := range inboxes {
		if !inbox.IsDir() || !canonicalUUID(inbox.Name()) {
			continue
		}
		base := filepath.Join(s.root, "runtime-inboxes", inbox.Name())
		reqEntries, _ := os.ReadDir(filepath.Join(base, "requests"))
		for _, entry := range reqEntries {
			scanned++
			if scanned > MaxManagedRecords {
				return 0, 0, fail("quota_exceeded", "runtime transfer record count exceeds safety limit")
			}
			record, err := readStrict[PrepareRecord](filepath.Join(base, "requests", entry.Name()))
			if err == nil && record.ExpiresUnixMS > now.UnixMilli() {
				requests++
			}
		}
		leaseEntries, _ := os.ReadDir(filepath.Join(base, "leases"))
		for _, entry := range leaseEntries {
			scanned++
			if scanned > MaxManagedRecords {
				return 0, 0, fail("quota_exceeded", "runtime transfer record count exceeds safety limit")
			}
			lease, err := readStrict[Lease](filepath.Join(base, "leases", entry.Name(), "lease.json"))
			if err == nil && lease.ExpiresUnixMS > now.UnixMilli() {
				leases++
			}
		}
	}
	return requests, leases, nil
}

func validAttachmentRecord(a Attachment) bool {
	return a.Version == 1 && canonicalUUID(a.ID) && canonicalUUID(a.ClientID) && canonicalUUID(a.ThreadID) && a.AuthorityID != "" && len(a.AuthorityID) <= 128 && a.OwnerMachine != "" && validateAttachmentName(a.Name) == nil && validateMediaType(a.MediaType) == nil && a.Size >= 0 && a.Size <= MaxAttachmentBytes && lowerHexDigest(a.SHA256) && (a.State == "uploading" || a.State == "ready") && a.NextOffset >= 0 && a.NextOffset <= a.Size && a.ExpiresUnixMS > 0
}

func validArtifactRecord(a Artifact) bool {
	return a.Version == 1 && canonicalUUID(a.ID) && canonicalUUID(a.ClientID) && canonicalUUID(a.ThreadID) && a.OwnerMachine != "" && validTarget(a.Target) && validateName(a.Name) == nil && a.MediaType != "" && validateMediaType(a.MediaType) == nil && a.Size >= 0 && a.Size <= MaxArtifactBytes && lowerHexDigest(a.SHA256) && (a.Disposition == "inline" || a.Disposition == "attachment") && (a.State == "staging" || a.State == "ready") && a.ExpiresUnixMS > 0
}

func managedPayloadSize(path string, declared int64, exact bool) bool {
	f, err := safeOpenRegular(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() > declared {
		return false
	}
	return !exact || fi.Size() == declared
}

func validateAttachmentInput(in StartAttachment) error {
	if !canonicalUUID(in.ThreadID) || !canonicalUUID(in.ClientID) || in.AuthorityID == "" || len(in.AuthorityID) > 128 || in.Size < 0 || in.Size > MaxAttachmentBytes || !lowerHexDigest(in.SHA256) {
		return fail("invalid_request", "attachment IDs, size or SHA-256 are invalid")
	}
	if err := validateAttachmentName(in.Name); err != nil {
		return err
	}
	return validateMediaType(in.MediaType)
}
func validateAttachmentName(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if name == "." || name == ".." {
		return fail("invalid_request", "attachment name must be a base name other than . or ..")
	}
	return nil
}

func validateName(name string) error {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return fail("invalid_request", "name must be one valid UTF-8 base name of at most 255 bytes")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fail("invalid_request", "name contains a control character")
		}
	}
	return nil
}
func validateMediaType(v string) error {
	if len(v) > 127 {
		return fail("invalid_request", "media_type exceeds 127 bytes")
	}
	for _, c := range []byte(v) {
		if c < 0x20 || c > 0x7e {
			return fail("invalid_request", "media_type must be visible ASCII")
		}
	}
	if v != "" {
		if _, _, err := mime.ParseMediaType(v); err != nil {
			return fail("invalid_request", "media_type is not valid MIME syntax")
		}
	}
	return nil
}
func validTarget(t Target) bool {
	return t.StreamID != "" && len(t.StreamID) <= 128 && t.SessionID != "" && len(t.SessionID) <= 128 && t.SessionEpoch >= 0
}
func canonicalUUID(v string) bool { u, err := uuid.Parse(v); return err == nil && u.String() == v }
func safeIdentityText(value string, maxBytes int) bool {
	return len(value) > 0 && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	})
}
func lowerHexDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
func hasCapability(v []string, w string) bool {
	for _, x := range v {
		if x == w {
			return true
		}
	}
	return false
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func cleanupDirs(paths []string) {
	for _, p := range paths {
		_ = os.RemoveAll(p)
	}
}

func readConsumer(path string) (Consumer, error) {
	c, err := readStrict[Consumer](path)
	if errors.Is(err, os.ErrNotExist) {
		return Consumer{}, fail("unsupported_runtime_transfer", "runtime has no registered Sesh transfer consumer")
	}
	if err != nil {
		return Consumer{}, fail("storage_corrupt", "runtime transfer consumer marker is invalid")
	}
	if c.Protocol != "sesh.pi-transfer" || c.Version != 1 || !canonicalUUID(c.ThreadID) || !validTarget(c.Target) {
		return Consumer{}, fail("storage_corrupt", "runtime transfer consumer marker has invalid identity")
	}
	sort.Strings(c.Capabilities)
	for i, v := range c.Capabilities {
		if (v != "attachment-lease-v1" && v != "artifact-publish-v1" && v != InputImageReadCapability) || (i > 0 && c.Capabilities[i-1] == v) {
			return Consumer{}, fail("storage_corrupt", "runtime transfer consumer capabilities are invalid")
		}
	}
	hasInputReader := hasCapability(c.Capabilities, InputImageReadCapability)
	if hasInputReader != (c.InputImageReader != nil) {
		return Consumer{}, fail("storage_corrupt", "runtime input-image reader capability and descriptor must be paired")
	}
	if c.InputImageReader != nil {
		reader := c.InputImageReader
		if reader.Protocol != "pi.input-image-read" || reader.Version != 1 || !canonicalUUID(reader.ListenerGeneration) || reader.SocketSuffix != ".input.sock" {
			return Consumer{}, fail("storage_corrupt", "runtime input-image reader descriptor is invalid")
		}
	}
	return c, nil
}

func readStrict[T any](path string) (T, error) {
	var out T
	f, err := safeOpenRegular(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return out, fmt.Errorf("trailing metadata")
	}
	return out, nil
}
func writeJSONAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	if err := secureMkdir(dir); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fail("storage_error", "encode metadata: %v", err)
	}
	if len(data) > 64<<10 {
		return fail("storage_error", "metadata exceeds 64 KiB")
	}
	tmp := filepath.Join(dir, ".metadata-"+uuid.NewString()+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail("storage_error", "create metadata: %v", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fail("storage_error", "protect metadata: %v", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fail("storage_error", "write metadata: %v", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fail("storage_error", "sync metadata: %v", err)
	}
	if err := f.Close(); err != nil {
		return fail("storage_error", "close metadata: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail("storage_error", "replace metadata: %v", err)
	}
	df, err := os.Open(dir)
	if err == nil {
		_ = df.Sync()
		_ = df.Close()
	}
	ok = true
	return nil
}
func verifyManagedPath(path string) error {
	clean := filepath.Clean(path)
	marker := string(filepath.Separator) + "browser-transfers"
	idx := strings.LastIndex(clean, marker)
	if idx < 0 {
		return fail("storage_corrupt", "managed path is outside browser-transfers")
	}
	root := clean[:idx+len(marker)]
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fail("storage_corrupt", "managed path escapes browser-transfers")
	}
	parts := []string{"."}
	if rel != "." {
		parts = strings.Split(rel, string(filepath.Separator))
	}
	current := root
	all := append([]string{"."}, parts...)
	for i, part := range all {
		if i > 0 {
			current = filepath.Join(current, part)
		}
		fi, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fail("storage_error", "inspect managed path: %v", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fail("storage_corrupt", "managed path contains a symlink")
		}
		if current != clean && !fi.IsDir() {
			return fail("storage_corrupt", "managed path parent is not a directory")
		}
	}
	return nil
}

func secureMkdir(path string) error {
	if err := verifyManagedPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fail("storage_error", "create managed directory: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fail("storage_error", "protect managed directory: %v", err)
	}
	if err := verifyManagedPath(path); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fail("storage_corrupt", "managed directory is not a real directory")
	}
	return nil
}
func safeOpenRegular(path string) (*os.File, error) {
	if err := verifyManagedPath(path); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, fail("storage_corrupt", "managed file is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}
func safeOpenWriteRegular(path string) (*os.File, error) {
	if err := verifyManagedPath(path); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, fail("storage_corrupt", "managed file is not a regular file")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return f, nil
}
func fileDigest(path string, max int64) (int64, string, error) {
	f, err := safeOpenRegular(path)
	if err != nil {
		return 0, "", fail("storage_corrupt", "open managed payload: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	if err != nil {
		return 0, "", fail("storage_error", "hash managed payload: %v", err)
	}
	if n > max {
		return 0, "", fail("storage_corrupt", "managed payload exceeds its limit")
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
