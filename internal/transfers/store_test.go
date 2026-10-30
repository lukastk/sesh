package transfers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAttachmentChunkResumeCompleteAndSymlinkRefusal(t *testing.T) {
	home := t.TempDir()
	s := New(home, "owner")
	threadID, clientID := uuid.NewString(), uuid.NewString()
	data := bytes.Repeat([]byte("attachment-data-"), 20000)
	if _, err := s.StartAttachment(StartAttachment{ThreadID: threadID, ClientID: uuid.NewString(), AuthorityID: "test-authority", Name: "../escape", Size: 1, SHA256: testDigest([]byte{0})}); ErrorCode(err) != "invalid_request" {
		t.Fatalf("path-like attachment name accepted: %v", err)
	}
	a, err := s.StartAttachment(StartAttachment{ThreadID: threadID, ClientID: clientID, AuthorityID: "test-authority", Name: "shot.png", MediaType: "image/png", Size: int64(len(data)), SHA256: testDigest(data)})
	if err != nil {
		t.Fatal(err)
	}
	first := data[:MaxChunkBytes]
	a, err = s.PutChunk(threadID, a.ID, 0, testDigest(first), bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if a.NextOffset != int64(len(first)) {
		t.Fatalf("next offset = %d", a.NextOffset)
	}

	// Process restart: metadata is the durable offset/hash authority.
	s = New(home, "owner")
	got, err := s.Attachment(threadID, a.ID)
	if err != nil || got.NextOffset != int64(len(first)) {
		t.Fatalf("restart status = %#v %v", got, err)
	}
	if _, err := s.PutChunk(threadID, a.ID, 0, testDigest(first), bytes.NewReader(first), int64(len(first))); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	conflict := append([]byte(nil), first...)
	conflict[0] ^= 1
	if _, err := s.PutChunk(threadID, a.ID, 0, testDigest(conflict), bytes.NewReader(conflict), int64(len(conflict))); ErrorCode(err) != "upload_conflict" {
		t.Fatalf("conflicting retry = %v", err)
	}
	rest := data[len(first):]
	if _, err := s.PutChunk(threadID, a.ID, int64(len(first)), testDigest(rest), bytes.NewReader(rest), int64(len(rest))); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Complete(threadID, a.ID)
	if err != nil || ready.State != "ready" {
		t.Fatalf("complete = %#v %v", ready, err)
	}
	if fi, err := os.Stat(filepath.Join(s.Root(), "attachments", a.ID, "payload")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("payload mode = %v %v", fi, err)
	}

	realDir := filepath.Join(s.Root(), "attachments", a.ID+"-real")
	managedDir := filepath.Join(s.Root(), "attachments", a.ID)
	if err := os.Rename(managedDir, realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, managedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attachment(threadID, a.ID); ErrorCode(err) != "storage_corrupt" {
		t.Fatalf("symlink attachment accepted: %v", err)
	}
}

func TestAttachmentBasenameAlignmentAndByteNeutralLease(t *testing.T) {
	s := New(t.TempDir(), "owner")
	threadID := uuid.NewString()
	authorityID := "test-authority"
	emptyDigest := testDigest(nil)

	for _, name := range []string{".", ".."} {
		_, err := s.StartAttachment(StartAttachment{
			ThreadID: threadID, ClientID: uuid.NewString(), AuthorityID: authorityID,
			Name: name, Size: 0, SHA256: emptyDigest,
		})
		if ErrorCode(err) != "invalid_request" {
			t.Fatalf("literal attachment basename %q accepted: %v", name, err)
		}
	}

	for _, name := range []string{"notes.txt", "猫.png"} {
		a, err := s.StartAttachment(StartAttachment{
			ThreadID: threadID, ClientID: uuid.NewString(), AuthorityID: authorityID,
			Name: name, Size: 0, SHA256: emptyDigest,
		})
		if err != nil || a.Name != name {
			t.Fatalf("valid attachment basename %q = %#v, %v", name, a, err)
		}
		if deleted, err := s.DeleteAttachment(threadID, a.ID); err != nil || !deleted {
			t.Fatalf("delete basename fixture %q = %v, %v", name, deleted, err)
		}
	}

	target := Target{StreamID: "stream-image-like", SessionID: uuid.NewString(), SessionEpoch: 7}
	inbox, err := s.Inbox(threadID)
	if err != nil {
		t.Fatal(err)
	}
	consumer := Consumer{Protocol: "sesh.pi-transfer", Version: 1, ThreadID: threadID, Target: target, Capabilities: []string{"attachment-lease-v1"}}
	if err := writeJSONAtomic(filepath.Join(inbox, "consumer.json"), consumer); err != nil {
		t.Fatal(err)
	}

	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	name := "猫-preview.dat"
	mediaType := "text/plain; charset=utf-8"
	a, err := s.StartAttachment(StartAttachment{
		ThreadID: threadID, ClientID: uuid.NewString(), AuthorityID: authorityID,
		Name: name, MediaType: mediaType, Size: int64(len(data)), SHA256: testDigest(data),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutChunk(threadID, a.ID, 0, testDigest(data), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Complete(threadID, a.ID)
	if err != nil || ready.Name != name || ready.MediaType != mediaType || ready.SHA256 != testDigest(data) {
		t.Fatalf("byte-neutral completion = %#v, %v", ready, err)
	}
	rec, err := s.Prepare(threadID, uuid.NewString(), target, []string{a.ID})
	if err != nil || len(rec.LeaseIDs) != 1 {
		t.Fatalf("byte-neutral prepare = %#v, %v", rec, err)
	}
	leaseDir := filepath.Join(inbox, "leases", rec.LeaseIDs[0])
	lease, err := readStrict[Lease](filepath.Join(leaseDir, "lease.json"))
	if err != nil || lease.Name != name || lease.MediaType != mediaType || lease.Size != int64(len(data)) || lease.SHA256 != testDigest(data) {
		t.Fatalf("byte-neutral lease = %#v, %v", lease, err)
	}
	leased, err := os.ReadFile(filepath.Join(leaseDir, "payload"))
	if err != nil || !bytes.Equal(leased, data) {
		t.Fatalf("byte-neutral lease payload = %x, %v", leased, err)
	}
}

func TestPrepareExactTargetAndImmutableRequest(t *testing.T) {
	s := New(t.TempDir(), "owner")
	threadID := uuid.NewString()
	target := Target{StreamID: "stream-a", SessionID: uuid.NewString(), SessionEpoch: 3}
	inbox, err := s.Inbox(threadID)
	if err != nil {
		t.Fatal(err)
	}
	consumer := Consumer{Protocol: "sesh.pi-transfer", Version: 1, ThreadID: threadID, Target: target, Capabilities: []string{"attachment-lease-v1", "artifact-publish-v1"}}
	if err := writeJSONAtomic(filepath.Join(inbox, "consumer.json"), consumer); err != nil {
		t.Fatal(err)
	}
	data := []byte("hello")
	a, err := s.StartAttachment(StartAttachment{ThreadID: threadID, ClientID: uuid.NewString(), AuthorityID: "test-authority", Name: "hello.txt", MediaType: "text/plain", Size: int64(len(data)), SHA256: testDigest(data)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutChunk(threadID, a.ID, 0, testDigest(data), bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(threadID, a.ID); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.NewString()
	rec, err := s.Prepare(threadID, requestID, target, []string{a.ID})
	if err != nil || len(rec.LeaseIDs) != 1 {
		t.Fatalf("prepare = %#v %v", rec, err)
	}
	repeat, err := s.Prepare(threadID, requestID, target, []string{a.ID})
	if err != nil || !equalStrings(repeat.LeaseIDs, rec.LeaseIDs) {
		t.Fatalf("repeat = %#v %v", repeat, err)
	}
	lease, err := readStrict[Lease](filepath.Join(inbox, "leases", rec.LeaseIDs[0], "lease.json"))
	if err != nil || lease.Target != target || lease.RequestID != requestID || lease.Payload != "payload" {
		t.Fatalf("lease = %#v %v", lease, err)
	}
	if _, err := s.Prepare(threadID, requestID, Target{StreamID: "other", SessionID: target.SessionID, SessionEpoch: 3}, []string{a.ID}); ErrorCode(err) != "stale_target" {
		t.Fatalf("changed target = %v", err)
	}
	if _, err := s.DeleteAttachment(threadID, a.ID); ErrorCode(err) != "live_lease_conflict" {
		t.Fatalf("delete with lease = %v", err)
	}
	future := time.UnixMilli(rec.ExpiresUnixMS).Add(PreparedTTL + time.Hour)
	removed, err := s.Sweep(future, SweepLimit)
	if err != nil {
		t.Fatal(err)
	}
	removedAgain, err := s.Sweep(future, SweepLimit)
	if err != nil || removed+removedAgain < 3 {
		t.Fatalf("expiry sweeps removed=%d+%d err=%v", removed, removedAgain, err)
	}
	if _, err := os.Stat(filepath.Join(inbox, "leases", rec.LeaseIDs[0])); !os.IsNotExist(err) {
		t.Fatalf("expired lease remains: %v", err)
	}
	if _, err := s.Attachment(threadID, a.ID); ErrorCode(err) != "attachment_not_found" {
		t.Fatalf("expired prepared attachment remains: %v", err)
	}

	missing := New(t.TempDir(), "owner")
	if _, err := missing.Prepare(threadID, uuid.NewString(), target, []string{uuid.NewString()}); ErrorCode(err) != "unsupported_runtime_transfer" {
		t.Fatalf("missing consumer = %v", err)
	}
}

func TestArtifactPublicationAndTotalDurableQuota(t *testing.T) {
	s := New(t.TempDir(), "owner")
	threadID := uuid.NewString()
	target := Target{StreamID: "stream", SessionID: uuid.NewString(), SessionEpoch: 0}
	data := []byte("artifact bytes")
	a, err := s.StartArtifact(StartArtifact{ThreadID: threadID, ClientID: uuid.NewString(), Target: target, Name: "result.bin", MediaType: "application/octet-stream", Size: int64(len(data)), SHA256: testDigest(data), Disposition: "attachment"})
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.PutArtifact(threadID, a.ID, bytes.NewReader(data), int64(len(data)))
	if err != nil || a.State != "ready" {
		t.Fatalf("publish = %#v %v", a, err)
	}
	got, path, err := s.Artifact(threadID, a.ID)
	if err != nil || got.SHA256 != testDigest(data) {
		t.Fatalf("artifact = %#v %s %v", got, path, err)
	}
	stored, _ := os.ReadFile(path)
	if !bytes.Equal(stored, data) {
		t.Fatal("artifact bytes changed")
	}

	quota := New(t.TempDir(), "owner")
	now := time.Now()
	for i := 0; i < 16; i++ {
		id := uuid.NewString()
		dir := filepath.Join(quota.Root(), "artifacts", id)
		if err := secureMkdir(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		meta := Artifact{Version: 1, ID: id, ClientID: uuid.NewString(), ThreadID: threadID, OwnerMachine: "owner", Target: target, Name: "reserved.bin", MediaType: "application/octet-stream", Size: MaxArtifactBytes, SHA256: strings.Repeat("0", 64), Disposition: "attachment", State: "staging", ExpiresUnixMS: now.Add(time.Hour).UnixMilli()}
		if err := writeJSONAtomic(filepath.Join(dir, "metadata.json"), meta); err != nil {
			t.Fatal(err)
		}
	}
	_, err = quota.StartAttachment(StartAttachment{ThreadID: threadID, ClientID: uuid.NewString(), AuthorityID: "test-authority", Name: "one", Size: 1, SHA256: testDigest([]byte{0})})
	if ErrorCode(err) != "quota_exceeded" {
		t.Fatalf("total quota not enforced: %v", err)
	}
}

func TestInputImageReaderConsumerStrictAdditiveCompatibility(t *testing.T) {
	s := New(t.TempDir(), "owner")
	threadID := uuid.NewString()
	sessionID := uuid.NewString()
	target := Target{StreamID: "stream-live", SessionID: sessionID, SessionEpoch: 4}
	inbox, err := s.Inbox(threadID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(inbox, "consumer.json")

	legacy := Consumer{Protocol: "sesh.pi-transfer", Version: 1, ThreadID: threadID, Target: target, Capabilities: []string{"artifact-publish-v1", "attachment-lease-v1"}}
	if err := writeJSONAtomic(path, legacy); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateConsumer(threadID, target, "attachment-lease-v1"); err != nil {
		t.Fatalf("unsorted legacy marker disabled attachment transfer: %v", err)
	}
	if _, err := s.ResolveInputImageReader(threadID, sessionID); ErrorCode(err) != "unsupported_runtime_transfer" {
		t.Fatalf("legacy marker resolved live reader: %v", err)
	}

	generation := uuid.NewString()
	reader := &InputImageReaderDescriptor{Protocol: "pi.input-image-read", Version: 1, ListenerGeneration: generation, SocketSuffix: ".input.sock"}
	consumer := Consumer{Protocol: "sesh.pi-transfer", Version: 1, ThreadID: threadID, Target: target, Capabilities: []string{InputImageReadCapability, "artifact-publish-v1", "attachment-lease-v1"}, InputImageReader: reader}
	if err := writeJSONAtomic(path, consumer); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.ResolveInputImageReader(threadID, sessionID)
	if err != nil || resolved.Target != target || resolved.Reader.ListenerGeneration != generation {
		t.Fatalf("live reader = %#v, %v", resolved, err)
	}
	if err := s.ValidateConsumer(threadID, target, "artifact-publish-v1"); err != nil {
		t.Fatalf("input reader marker disabled output publication: %v", err)
	}

	consumer.InputImageReader = nil
	if err := writeJSONAtomic(path, consumer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveInputImageReader(threadID, sessionID); ErrorCode(err) != "storage_corrupt" {
		t.Fatalf("unpaired reader capability was accepted: %v", err)
	}

	consumer.InputImageReader = reader
	consumer.Target.StreamID = "bad\nstream"
	if err := writeJSONAtomic(path, consumer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveInputImageReader(threadID, sessionID); ErrorCode(err) != "storage_corrupt" {
		t.Fatalf("unsafe live target was accepted: %v", err)
	}
}
