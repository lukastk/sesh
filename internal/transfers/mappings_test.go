package transfers

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func mappingConsumer(t *testing.T, store *Store, threadID string, target Target) {
	t.Helper()
	inbox, err := store.Inbox(threadID)
	if err != nil {
		t.Fatal(err)
	}
	consumer := Consumer{Protocol: "sesh.pi-transfer", Version: 1, ThreadID: threadID, Target: target, Capabilities: []string{ArtifactPublishCapability}}
	if err := writeJSONAtomic(filepath.Join(inbox, "consumer.json"), consumer); err != nil {
		t.Fatal(err)
	}
}

func mappingResolveInput(threadID, sessionID, publicationID string, target Target, data []byte) ResolveArtifactMapping {
	digest := testDigest(data)
	return ResolveArtifactMapping{
		ThreadID: threadID, ConsumerTarget: target, NativeSessionID: sessionID, PublicationID: publicationID,
		Metadata:     ArtifactMetadata{Kind: "image", Name: "output.png", MediaType: "image/png", Size: int64(len(data)), SHA256: &digest, Disposition: "inline", Retention: "session_inline", Origin: "native"},
		Availability: MappingAvailability{Status: "available"},
	}
}

func TestDeriveArtifactMappingIDDeterministicUUIDv8(t *testing.T) {
	threadID := "11111111-1111-4111-8111-111111111111"
	sessionID := "22222222-2222-4222-8222-222222222222"
	publicationID := "33333333-3333-4333-8333-333333333333"
	got, err := DeriveArtifactMappingID(threadID, sessionID, publicationID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "9c82caf6-dea7-8830-b662-ad1fbaced108" {
		t.Fatalf("derived mapping id = %s", got)
	}
	parsed, err := uuid.Parse(got)
	if err != nil || parsed.Version() != 8 || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("derived mapping id is not canonical UUIDv8: %s err=%v", got, err)
	}
	other, err := DeriveArtifactMappingID(threadID, sessionID, uuid.NewString())
	if err != nil || other == got {
		t.Fatalf("different publication did not derive a different id: %s err=%v", other, err)
	}
}

func TestArtifactMappingSameSessionRotationAndReadyReplay(t *testing.T) {
	store := New(t.TempDir(), "owner")
	threadID, sessionID, publicationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	target1 := Target{StreamID: "stream-one", SessionID: sessionID, SessionEpoch: 1}
	target2 := Target{StreamID: "stream-two", SessionID: sessionID, SessionEpoch: 2}
	data := bytes.Repeat([]byte("managed-output"), 20000)
	input := mappingResolveInput(threadID, sessionID, publicationID, target1, data)
	mappingConsumer(t, store, threadID, target1)
	mapping, err := store.ResolveArtifactMapping(input)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.State != "staging" || mapping.ArtifactID == nil || mapping.MappingID != mapping.ClientID || mapping.ProducerTarget != target1 {
		t.Fatalf("initial mapping = %#v", mapping)
	}
	originalArtifactID := *mapping.ArtifactID

	mappingConsumer(t, store, threadID, target2)
	input.ConsumerTarget = target2
	rotated, err := store.ResolveArtifactMapping(input)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.MappingID != mapping.MappingID || rotated.ArtifactID == nil || *rotated.ArtifactID != originalArtifactID || rotated.ProducerTarget != target1 {
		t.Fatalf("rotation rebound immutable mapping: %#v", rotated)
	}
	ready, err := store.PutArtifactMappingContent(threadID, mapping.MappingID, target2, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != "ready" || ready.ProducerTarget != target1 {
		t.Fatalf("ready mapping = %#v", ready)
	}
	artifact, err := store.ArtifactForMapping(ready)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.ID != originalArtifactID || artifact.Target != target1 || artifact.SHA256 != testDigest(data) {
		t.Fatalf("ready artifact = %#v", artifact)
	}
	if _, err := store.PutArtifactMappingContent(threadID, mapping.MappingID, target2, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("exact ready replay failed: %v", err)
	}

	newSession := uuid.NewString()
	newTarget := Target{StreamID: "stream-new-session", SessionID: newSession, SessionEpoch: 0}
	mappingConsumer(t, store, threadID, newTarget)
	if _, err := store.PutArtifactMappingContent(threadID, mapping.MappingID, newTarget, bytes.NewReader(data), int64(len(data))); ErrorCode(err) != "stale_target" {
		t.Fatalf("different native session resumed mapping: %v", err)
	}
}

func TestArtifactMappingCrashRepairAndConflictRefusal(t *testing.T) {
	home := t.TempDir()
	store := New(home, "owner")
	threadID, sessionID, publicationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	target := Target{StreamID: "stream", SessionID: sessionID, SessionEpoch: 1}
	data := []byte("crash-window-bytes")
	input := mappingResolveInput(threadID, sessionID, publicationID, target, data)
	mappingConsumer(t, store, threadID, target)
	mapping, err := store.ResolveArtifactMapping(input)
	if err != nil {
		t.Fatal(err)
	}
	artifactID := *mapping.ArtifactID

	// Simulate a process death after the write-ahead mapping but before artifact start.
	mapping.State = "pending"
	mapping.UpdatedUnixMS++
	if err := writeJSONAtomic(filepath.Join(store.root, "artifact-mappings", mapping.MappingID, "mapping.json"), mapping); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(store.root, "artifacts", artifactID)); err != nil {
		t.Fatal(err)
	}
	restarted := New(home, "owner")
	repaired, err := restarted.ResolveArtifactMapping(input)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.State != "staging" || repaired.ArtifactID == nil || *repaired.ArtifactID != artifactID {
		t.Fatalf("pending repair minted/rebound identity: %#v", repaired)
	}

	ready, err := restarted.PutArtifactMappingContent(threadID, repaired.MappingID, target, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate content commit followed by death before the mapping-ready write.
	ready.State = "staging"
	ready.UpdatedUnixMS++
	if err := writeJSONAtomic(filepath.Join(restarted.root, "artifact-mappings", ready.MappingID, "mapping.json"), ready); err != nil {
		t.Fatal(err)
	}
	repairedReady, err := New(home, "owner").ResolveArtifactMapping(input)
	if err != nil {
		t.Fatal(err)
	}
	if repairedReady.State != "ready" || repairedReady.ArtifactID == nil || *repairedReady.ArtifactID != artifactID {
		t.Fatalf("ready repair failed: %#v", repairedReady)
	}

	artifactPath := filepath.Join(home, "browser-transfers", "artifacts", artifactID, "metadata.json")
	artifact, err := readStrict[Artifact](artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Target.StreamID = "conflicting-producer"
	if err := writeJSONAtomic(artifactPath, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := New(home, "owner").ResolveArtifactMapping(input); ErrorCode(err) != "storage_corrupt" {
		t.Fatalf("conflicting partial state was not refused: %v", err)
	}
}

func TestArtifactMappingUnavailableExpiryAndRestartTombstones(t *testing.T) {
	home := t.TempDir()
	store := New(home, "owner")
	base := time.Unix(1_900_000_000, 0)
	store.now = func() time.Time { return base }
	threadID, sessionID := uuid.NewString(), uuid.NewString()
	target := Target{StreamID: "stream", SessionID: sessionID, SessionEpoch: 0}
	mappingConsumer(t, store, threadID, target)

	legacyID := testDigest([]byte("legacy-id"))
	legacy := ResolveArtifactMapping{
		ThreadID: threadID, ConsumerTarget: target, NativeSessionID: sessionID, PublicationID: legacyID,
		Metadata:     ArtifactMetadata{Kind: "image", Name: "legacy.png", MediaType: "image/png", Size: 12, SHA256: nil, Disposition: "inline", Retention: "session_inline", Origin: "legacy_inline_image"},
		Availability: MappingAvailability{Status: "available"},
	}
	unavailable, err := store.ResolveArtifactMapping(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.State != "unavailable" || unavailable.Unavailable == nil || unavailable.Unavailable.Code != "digest_unavailable" {
		t.Fatalf("legacy digest tombstone = %#v", unavailable)
	}
	if again, err := New(home, "owner").QueryArtifactMappings(threadID, sessionID, []string{legacyID}); err != nil || again[0] == nil || again[0].State != "unavailable" {
		t.Fatalf("unavailable tombstone did not survive restart: %#v err=%v", again, err)
	}

	data := []byte("ready survives native source loss")
	publicationID := uuid.NewString()
	input := mappingResolveInput(threadID, sessionID, publicationID, target, data)
	mapping, err := store.ResolveArtifactMapping(input)
	if err != nil {
		t.Fatal(err)
	}
	mapping, err = store.PutArtifactMappingContent(threadID, mapping.MappingID, target, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	input.Availability = MappingAvailability{Status: "unavailable", Reason: "source_not_retained", Message: "source ended"}
	stillReady, err := store.ResolveArtifactMapping(input)
	if err != nil || stillReady.State != "ready" {
		t.Fatalf("native source loss invalidated ready bytes: %#v err=%v", stillReady, err)
	}

	store.now = func() time.Time { return base.Add(ArtifactTTL + time.Second) }
	if removed, err := store.Sweep(store.now(), SweepLimit); err != nil || removed < 1 {
		t.Fatalf("expiry sweep removed=%d err=%v", removed, err)
	}
	queried, err := store.QueryArtifactMappings(threadID, sessionID, []string{publicationID})
	if err != nil {
		t.Fatal(err)
	}
	if queried[0] == nil || queried[0].State != "expired" || queried[0].ArtifactID == nil || *queried[0].ArtifactID != *mapping.ArtifactID {
		t.Fatalf("expiry tombstone = %#v", queried[0])
	}
	if _, err := os.Stat(filepath.Join(home, "browser-transfers", "artifacts", *mapping.ArtifactID)); !os.IsNotExist(err) {
		t.Fatalf("expired artifact payload still exists: %v", err)
	}
	store.now = time.Now
	if resolved, err := store.ResolveArtifactMapping(mappingResolveInput(threadID, sessionID, publicationID, target, data)); err != nil || resolved.State != "expired" {
		t.Fatalf("expired mapping was republished: %#v err=%v", resolved, err)
	}
}

func TestArtifactMappingTerminalFailureAndThreadCleanup(t *testing.T) {
	home := t.TempDir()
	store := New(home, "owner")
	threadID, sessionID, publicationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	target := Target{StreamID: "stream", SessionID: sessionID, SessionEpoch: 0}
	data := []byte("failure")
	mappingConsumer(t, store, threadID, target)
	mapping, err := store.ResolveArtifactMapping(mappingResolveInput(threadID, sessionID, publicationID, target, data))
	if err != nil {
		t.Fatal(err)
	}
	failure := MappingUnavailable{Source: "native_copy", Code: "artifact_not_found", Message: "native bytes are unavailable"}
	failed, err := store.FailArtifactMapping(FailArtifactMapping{ThreadID: threadID, ConsumerTarget: target, NativeSessionID: sessionID, PublicationID: publicationID, Failure: failure})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "unavailable" || failed.Unavailable == nil || *failed.Unavailable != failure {
		t.Fatalf("failed mapping = %#v", failed)
	}
	if _, err := store.FailArtifactMapping(FailArtifactMapping{ThreadID: threadID, ConsumerTarget: target, NativeSessionID: sessionID, PublicationID: publicationID, Failure: failure}); err != nil {
		t.Fatalf("identical terminal failure was not idempotent: %v", err)
	}
	bad := failure
	bad.Code = "timeout"
	if _, err := store.FailArtifactMapping(FailArtifactMapping{ThreadID: threadID, ConsumerTarget: target, NativeSessionID: sessionID, PublicationID: publicationID, Failure: bad}); ErrorCode(err) != "invalid_failure" {
		t.Fatalf("retryable timeout became permanent: %v", err)
	}
	if removed, err := store.DeleteArtifactMappings(threadID); err != nil || removed != 1 {
		t.Fatalf("thread mapping cleanup removed=%d err=%v", removed, err)
	}
	if mappings, err := store.QueryArtifactMappings(threadID, sessionID, []string{publicationID}); err != nil || mappings[0] != nil {
		t.Fatalf("deleted thread mapping still exists: %#v err=%v", mappings, err)
	}
	if _, err := os.Stat(filepath.Join(home, "browser-transfers", "artifacts", *mapping.ArtifactID)); !os.IsNotExist(err) {
		t.Fatalf("mapped staging artifact survived thread cleanup: %v", err)
	}
}

func TestArtifactMappingLegacyIdentityValidationAndArtifactWithoutMappingRefusal(t *testing.T) {
	home := t.TempDir()
	store := New(home, "owner")
	threadID, sessionID := uuid.NewString(), uuid.NewString()
	legacyPublicationID := testDigest([]byte("legacy inline image identity"))
	target := Target{StreamID: "legacy-stream", SessionID: sessionID, SessionEpoch: 0}
	mappingConsumer(t, store, threadID, target)
	input := ResolveArtifactMapping{
		ThreadID: threadID, ConsumerTarget: target, NativeSessionID: sessionID, PublicationID: legacyPublicationID,
		Metadata:     ArtifactMetadata{Kind: "image", Name: "legacy.png", MediaType: "image/png", Size: 3, SHA256: nil, Disposition: "inline", Retention: "session_inline", Origin: "legacy_inline_image"},
		Availability: MappingAvailability{Status: "available"},
	}
	mapping, err := store.ResolveArtifactMapping(input)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.State != "unavailable" || mapping.Unavailable == nil || mapping.Unavailable.Source != "sesh" || mapping.Unavailable.Code != "digest_unavailable" || uuid.MustParse(mapping.MappingID).Version() != 8 {
		t.Fatalf("legacy unavailable mapping = %#v", mapping)
	}
	input.PublicationID = testDigest([]byte("other legacy image"))
	input.Availability = MappingAvailability{Status: "unavailable", Reason: "source_failed", Message: "/private/source"}
	if _, err := store.ResolveArtifactMapping(input); ErrorCode(err) != "invalid_publication" {
		t.Fatalf("path-bearing native message accepted: %v", err)
	}
	if _, err := store.QueryArtifactMappings(threadID, sessionID, []string{legacyPublicationID, legacyPublicationID}); ErrorCode(err) != "invalid_request" {
		t.Fatalf("duplicate publication query accepted: %v", err)
	}

	corruptHome := t.TempDir()
	corruptStore := New(corruptHome, "owner")
	publicationID := uuid.NewString()
	mappingID, err := DeriveArtifactMappingID(threadID, sessionID, publicationID)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("orphan")
	if _, err := corruptStore.StartArtifact(StartArtifact{ThreadID: threadID, ClientID: mappingID, Target: target, Name: "orphan.bin", MediaType: "application/octet-stream", Size: int64(len(data)), SHA256: testDigest(data), Disposition: "attachment"}); err != nil {
		t.Fatal(err)
	}
	mappingConsumer(t, corruptStore, threadID, target)
	if _, err := corruptStore.ResolveArtifactMapping(mappingResolveInput(threadID, sessionID, publicationID, target, data)); ErrorCode(err) != "storage_corrupt" {
		t.Fatalf("artifact without write-ahead mapping was not refused as corruption: %v", err)
	}
}

func TestArtifactMappingOrphanRepairAfterInterruptedThreadDeletion(t *testing.T) {
	store := New(t.TempDir(), "owner")
	keepThread, removeThread, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	target := Target{StreamID: "stream", SessionID: sessionID, SessionEpoch: 0}
	data := []byte("repair")
	keepPublication, removePublication := uuid.NewString(), uuid.NewString()
	mappingConsumer(t, store, keepThread, target)
	keep, err := store.ResolveArtifactMapping(mappingResolveInput(keepThread, sessionID, keepPublication, target, data))
	if err != nil {
		t.Fatal(err)
	}
	mappingConsumer(t, store, removeThread, target)
	remove, err := store.ResolveArtifactMapping(mappingResolveInput(removeThread, sessionID, removePublication, target, data))
	if err != nil {
		t.Fatal(err)
	}
	removed, err := store.DeleteOrphanArtifactMappings(map[string]bool{keepThread: true})
	if err != nil || removed != 1 {
		t.Fatalf("orphan repair removed=%d err=%v", removed, err)
	}
	if mappings, err := store.QueryArtifactMappings(keepThread, sessionID, []string{keepPublication}); err != nil || mappings[0] == nil {
		t.Fatalf("existing thread mapping was removed: %#v err=%v", mappings, err)
	}
	if mappings, err := store.QueryArtifactMappings(removeThread, sessionID, []string{removePublication}); err != nil || mappings[0] != nil {
		t.Fatalf("orphaned mapping survived repair: %#v err=%v", mappings, err)
	}
	if _, err := os.Stat(filepath.Join(store.root, "artifacts", *remove.ArtifactID)); !os.IsNotExist(err) {
		t.Fatalf("orphaned mapped artifact survived repair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.root, "artifacts", *keep.ArtifactID)); err != nil {
		t.Fatalf("existing mapped artifact was removed: %v", err)
	}
}
