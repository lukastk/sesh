package transfers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	ArtifactMappingProtocol         = "sesh.pi-artifact-map"
	ArtifactMappingVersion          = 1
	MaxArtifactMappingBytes         = 8 << 10
	MaxArtifactMappingMetadataBytes = 32 << 20
	MaxArtifactMappingMessageBytes  = 512
	ArtifactPublishCapability       = "artifact-publish-v1"
)

type ArtifactMetadata struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	MediaType   string  `json:"media_type"`
	Size        int64   `json:"size"`
	SHA256      *string `json:"sha256"`
	Disposition string  `json:"disposition"`
	Retention   string  `json:"retention"`
	Origin      string  `json:"origin"`
}

type MappingUnavailable struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ArtifactMapping struct {
	Protocol              string              `json:"protocol"`
	Version               int                 `json:"version"`
	MappingID             string              `json:"mapping_id"`
	ThreadID              string              `json:"thread_id"`
	OwnerMachine          string              `json:"owner_machine"`
	NativeSessionID       string              `json:"native_session_id"`
	PublicationID         string              `json:"publication_id"`
	ClientID              string              `json:"client_id"`
	ProducerTarget        Target              `json:"producer_target"`
	Metadata              ArtifactMetadata    `json:"metadata"`
	State                 string              `json:"state"`
	ArtifactID            *string             `json:"artifact_id"`
	Unavailable           *MappingUnavailable `json:"unavailable"`
	ArtifactExpiresUnixMS int64               `json:"artifact_expires_at_unix_ms"`
	ExpiredAtUnixMS       int64               `json:"expired_at_unix_ms"`
	CreatedUnixMS         int64               `json:"created_at_unix_ms"`
	UpdatedUnixMS         int64               `json:"updated_at_unix_ms"`
}

type MappingAvailability struct {
	Status  string
	Reason  string
	Message string
}

type ResolveArtifactMapping struct {
	ThreadID        string
	ConsumerTarget  Target
	NativeSessionID string
	PublicationID   string
	Metadata        ArtifactMetadata
	Availability    MappingAvailability
}

type FailArtifactMapping struct {
	ThreadID        string
	ConsumerTarget  Target
	NativeSessionID string
	PublicationID   string
	Failure         MappingUnavailable
}

func DeriveArtifactMappingID(threadID, nativeSessionID, publicationID string) (string, error) {
	if !canonicalUUID(threadID) || !canonicalUUID(nativeSessionID) || !validPublicationID(publicationID) {
		return "", fail("invalid_request", "artifact mapping key is invalid")
	}
	input := "sesh.pi-artifact-map\x00v1\x00" + threadID + "\x00" + nativeSessionID + "\x00" + publicationID
	sum := sha256.Sum256([]byte(input))
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x80
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String(), nil
}

func (s *Store) ResolveArtifactMapping(in ResolveArtifactMapping) (ArtifactMapping, error) {
	if err := validateResolveArtifactMapping(in); err != nil {
		return ArtifactMapping{}, err
	}
	mappingID, _ := DeriveArtifactMappingID(in.ThreadID, in.NativeSessionID, in.PublicationID)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	_, _ = s.sweepLocked(now, SweepLimit)
	if err := s.validateConsumerLocked(in.ThreadID, in.ConsumerTarget, ArtifactPublishCapability); err != nil {
		return ArtifactMapping{}, err
	}
	attachments, artifacts, mappings, err := s.recordsLocked()
	if err != nil {
		return ArtifactMapping{}, err
	}

	mapping, found, err := findMapping(mappingID, in.ThreadID, in.NativeSessionID, in.PublicationID, mappings)
	if err != nil {
		return ArtifactMapping{}, err
	}
	if found {
		if !equalArtifactMetadata(mapping.Metadata, in.Metadata) {
			return ArtifactMapping{}, fail("mapping_conflict", "artifact mapping immutable metadata conflicts")
		}
		mapping, err = s.reconcileMappingLocked(mapping, artifacts, now.UnixMilli())
		if err != nil {
			return ArtifactMapping{}, err
		}
		if mapping.State == "ready" || mapping.State == "unavailable" || mapping.State == "expired" {
			return mapping, nil
		}
		if in.Availability.Status == "unavailable" {
			return s.markMappingUnavailableLocked(mapping, MappingUnavailable{Source: "native", Code: in.Availability.Reason, Message: in.Availability.Message})
		}
		if in.Metadata.SHA256 == nil {
			return s.markMappingUnavailableLocked(mapping, MappingUnavailable{Source: "sesh", Code: "digest_unavailable", Message: "native publication has no authoritative SHA-256"})
		}
		if mapping.State == "pending" {
			mapping, err = s.ensurePendingArtifactLocked(mapping, attachments, artifacts, mappings, now.UnixMilli())
			if err != nil {
				return ArtifactMapping{}, err
			}
		}
		return mapping, nil
	}

	for _, artifact := range artifacts {
		if artifact.ClientID == mappingID {
			return ArtifactMapping{}, fail("storage_corrupt", "managed artifact exists without its write-ahead mapping")
		}
	}
	if len(attachments)+len(artifacts)+len(mappings) >= MaxManagedRecords {
		return ArtifactMapping{}, fail("mapping_capacity", "managed mapping record capacity is exhausted")
	}
	ms := now.UnixMilli()
	mapping = ArtifactMapping{
		Protocol: ArtifactMappingProtocol, Version: ArtifactMappingVersion,
		MappingID: mappingID, ClientID: mappingID, ThreadID: in.ThreadID, OwnerMachine: s.machine,
		NativeSessionID: in.NativeSessionID, PublicationID: in.PublicationID,
		ProducerTarget: in.ConsumerTarget, Metadata: in.Metadata,
		State: "unavailable", CreatedUnixMS: ms, UpdatedUnixMS: ms,
	}
	if in.Availability.Status == "unavailable" {
		mapping.Unavailable = &MappingUnavailable{Source: "native", Code: in.Availability.Reason, Message: in.Availability.Message}
		if err := s.writeMappingLocked(mapping); err != nil {
			return ArtifactMapping{}, err
		}
		return mapping, nil
	}
	if in.Metadata.SHA256 == nil {
		mapping.Unavailable = &MappingUnavailable{Source: "sesh", Code: "digest_unavailable", Message: "native publication has no authoritative SHA-256"}
		if err := s.writeMappingLocked(mapping); err != nil {
			return ArtifactMapping{}, err
		}
		return mapping, nil
	}
	if len(attachments)+len(artifacts)+len(mappings)+2 > MaxManagedRecords {
		return ArtifactMapping{}, fail("mapping_capacity", "managed mapping and artifact capacity is exhausted")
	}
	artifactID := uuid.NewString()
	mapping.State = "pending"
	mapping.ArtifactID = &artifactID
	mapping.ArtifactExpiresUnixMS = now.Add(ArtifactTTL).UnixMilli()
	if err := s.writeMappingLocked(mapping); err != nil {
		return ArtifactMapping{}, err
	}
	mappings = append(mappings, mapping)
	return s.ensurePendingArtifactLocked(mapping, attachments, artifacts, mappings, ms)
}

func (s *Store) PutArtifactMappingContent(threadID, mappingID string, current Target, body io.Reader, length int64) (ArtifactMapping, error) {
	if !canonicalUUID(threadID) || !canonicalUUID(mappingID) || !validMappingTarget(current) {
		return ArtifactMapping{}, fail("invalid_request", "artifact mapping content identity is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateConsumerLocked(threadID, current, ArtifactPublishCapability); err != nil {
		return ArtifactMapping{}, err
	}
	_, artifacts, mappings, err := s.recordsLocked()
	if err != nil {
		return ArtifactMapping{}, err
	}
	mapping, found, err := findMappingByID(mappingID, mappings)
	if err != nil {
		return ArtifactMapping{}, err
	}
	if !found || mapping.ThreadID != threadID {
		return ArtifactMapping{}, fail("mapping_not_found", "artifact mapping not found for thread")
	}
	if current.SessionID != mapping.NativeSessionID || current.SessionID != mapping.ProducerTarget.SessionID {
		return ArtifactMapping{}, fail("stale_target", "current consumer session does not match artifact mapping")
	}
	mapping, err = s.reconcileMappingLocked(mapping, artifacts, s.now().UnixMilli())
	if err != nil {
		return ArtifactMapping{}, err
	}
	if mapping.State != "staging" && mapping.State != "ready" {
		return ArtifactMapping{}, fail("terminal_state_conflict", "artifact mapping does not accept content in its current state")
	}
	if mapping.ArtifactID == nil {
		return ArtifactMapping{}, fail("storage_corrupt", "artifact mapping has no reserved artifact")
	}
	artifact, err := s.artifactLocked(mapping.ThreadID, *mapping.ArtifactID)
	if err != nil {
		return ArtifactMapping{}, err
	}
	if err := exactArtifactForMapping(artifact, mapping); err != nil {
		return ArtifactMapping{}, err
	}
	artifact, err = s.putArtifactLocked(artifact, body, length)
	if err != nil {
		return ArtifactMapping{}, err
	}
	mapping.State = "ready"
	mapping.ArtifactExpiresUnixMS = artifact.ExpiresUnixMS
	mapping.UpdatedUnixMS = s.now().UnixMilli()
	if err := s.writeMappingLocked(mapping); err != nil {
		return ArtifactMapping{}, err
	}
	return mapping, nil
}

func (s *Store) FailArtifactMapping(in FailArtifactMapping) (ArtifactMapping, error) {
	if !canonicalUUID(in.ThreadID) || !canonicalUUID(in.NativeSessionID) || !validMappingTarget(in.ConsumerTarget) || in.ConsumerTarget.SessionID != in.NativeSessionID || !validPublicationID(in.PublicationID) || !validTerminalFailure(in.Failure) {
		return ArtifactMapping{}, fail("invalid_failure", "terminal artifact mapping failure is invalid")
	}
	mappingID, _ := DeriveArtifactMappingID(in.ThreadID, in.NativeSessionID, in.PublicationID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateConsumerLocked(in.ThreadID, in.ConsumerTarget, ArtifactPublishCapability); err != nil {
		return ArtifactMapping{}, err
	}
	_, artifacts, mappings, err := s.recordsLocked()
	if err != nil {
		return ArtifactMapping{}, err
	}
	mapping, found, err := findMapping(mappingID, in.ThreadID, in.NativeSessionID, in.PublicationID, mappings)
	if err != nil {
		return ArtifactMapping{}, err
	}
	if !found {
		return ArtifactMapping{}, fail("mapping_not_found", "artifact mapping not found")
	}
	mapping, err = s.reconcileMappingLocked(mapping, artifacts, s.now().UnixMilli())
	if err != nil {
		return ArtifactMapping{}, err
	}
	if mapping.State == "unavailable" {
		if mapping.Unavailable != nil && *mapping.Unavailable == in.Failure {
			return mapping, nil
		}
		return ArtifactMapping{}, fail("terminal_state_conflict", "artifact mapping already has a different terminal failure")
	}
	if mapping.State != "pending" && mapping.State != "staging" {
		return ArtifactMapping{}, fail("terminal_state_conflict", "ready or expired artifact mapping cannot be failed")
	}
	return s.markMappingUnavailableLocked(mapping, in.Failure)
}

func (s *Store) QueryArtifactMappings(threadID, nativeSessionID string, publicationIDs []string) ([]*ArtifactMapping, error) {
	if !canonicalUUID(threadID) || !canonicalUUID(nativeSessionID) || len(publicationIDs) < 1 || len(publicationIDs) > 64 {
		return nil, fail("invalid_request", "artifact mapping query identity or count is invalid")
	}
	seen := map[string]bool{}
	for _, publicationID := range publicationIDs {
		if !validPublicationID(publicationID) || seen[publicationID] {
			return nil, fail("invalid_request", "publication_ids must be unique valid Pi publication IDs")
		}
		seen[publicationID] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, artifacts, mappings, err := s.recordsLocked()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ArtifactMapping, len(mappings))
	for _, mapping := range mappings {
		byID[mapping.MappingID] = mapping
	}
	out := make([]*ArtifactMapping, 0, len(publicationIDs))
	for _, publicationID := range publicationIDs {
		mappingID, _ := DeriveArtifactMappingID(threadID, nativeSessionID, publicationID)
		mapping, ok := byID[mappingID]
		if !ok {
			out = append(out, nil)
			continue
		}
		if mapping.ThreadID != threadID || mapping.NativeSessionID != nativeSessionID || mapping.PublicationID != publicationID || mapping.ClientID != mappingID {
			return nil, fail("mapping_conflict", "artifact mapping identity collision")
		}
		mapping, err = s.reconcileMappingLocked(mapping, artifacts, s.now().UnixMilli())
		if err != nil {
			return nil, err
		}
		copy := mapping
		out = append(out, &copy)
	}
	return out, nil
}

func (s *Store) ArtifactForMapping(mapping ArtifactMapping) (Artifact, error) {
	if mapping.State != "ready" || mapping.ArtifactID == nil {
		return Artifact{}, fail("artifact_not_ready", "artifact mapping is not ready")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := readStrict[ArtifactMapping](filepath.Join(s.root, "artifact-mappings", mapping.MappingID, "mapping.json"))
	if err != nil || !validArtifactMapping(current) || current.ThreadID != mapping.ThreadID || current.NativeSessionID != mapping.NativeSessionID || current.PublicationID != mapping.PublicationID || current.State != "ready" || current.ArtifactID == nil {
		return Artifact{}, fail("storage_corrupt", "artifact mapping changed or is invalid")
	}
	artifact, err := s.artifactLocked(current.ThreadID, *current.ArtifactID)
	if err != nil {
		return Artifact{}, err
	}
	if artifact.ExpiresUnixMS <= s.now().UnixMilli() {
		return Artifact{}, fail("upload_expired", "artifact has expired")
	}
	if err := exactArtifactForMapping(artifact, current); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) DeleteArtifactMappings(threadID string) (int, error) {
	if !canonicalUUID(threadID) {
		return 0, fail("invalid_request", "thread_id must be a canonical UUID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mappings, _, err := s.mappingRecordsLocked()
	if err != nil {
		return 0, err
	}
	return s.deleteArtifactMappingsLocked(mappings, func(mapping ArtifactMapping) bool { return mapping.ThreadID == threadID })
}

// DeleteOrphanArtifactMappings repairs a crash after authoritative thread deletion
// but before its mapped payloads and tombstones were removed.
func (s *Store) DeleteOrphanArtifactMappings(existingThreadIDs map[string]bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mappings, _, err := s.mappingRecordsLocked()
	if err != nil {
		return 0, err
	}
	return s.deleteArtifactMappingsLocked(mappings, func(mapping ArtifactMapping) bool { return !existingThreadIDs[mapping.ThreadID] })
}

func (s *Store) deleteArtifactMappingsLocked(mappings []ArtifactMapping, remove func(ArtifactMapping) bool) (int, error) {
	removed := 0
	for _, mapping := range mappings {
		if !remove(mapping) {
			continue
		}
		if mapping.ArtifactID != nil {
			if err := os.RemoveAll(filepath.Join(s.root, "artifacts", *mapping.ArtifactID)); err != nil {
				return removed, fail("storage_error", "remove mapped artifact: %v", err)
			}
		}
		if err := os.RemoveAll(filepath.Join(s.root, "artifact-mappings", mapping.MappingID)); err != nil {
			return removed, fail("storage_error", "remove artifact mapping: %v", err)
		}
		removed++
	}
	return removed, nil
}

func (s *Store) ensurePendingArtifactLocked(mapping ArtifactMapping, attachments []Attachment, artifacts []Artifact, mappings []ArtifactMapping, nowMS int64) (ArtifactMapping, error) {
	if mapping.State != "pending" || mapping.ArtifactID == nil || mapping.Metadata.SHA256 == nil {
		return ArtifactMapping{}, fail("storage_corrupt", "pending artifact mapping is invalid")
	}
	for _, artifact := range artifacts {
		if artifact.ID == *mapping.ArtifactID {
			if err := exactArtifactForMapping(artifact, mapping); err != nil {
				return ArtifactMapping{}, err
			}
			if artifact.State == "ready" {
				mapping.State = "ready"
			} else {
				mapping.State = "staging"
			}
			mapping.ArtifactExpiresUnixMS = artifact.ExpiresUnixMS
			mapping.UpdatedUnixMS = nowMS
			if err := s.writeMappingLocked(mapping); err != nil {
				return ArtifactMapping{}, err
			}
			return mapping, nil
		}
	}
	artifact, err := s.createArtifactLocked(*mapping.ArtifactID, StartArtifact{
		ThreadID: mapping.ThreadID, ClientID: mapping.ClientID, Target: mapping.ProducerTarget,
		Name: mapping.Metadata.Name, MediaType: mapping.Metadata.MediaType, Size: mapping.Metadata.Size,
		SHA256: *mapping.Metadata.SHA256, Disposition: mapping.Metadata.Disposition,
	}, attachments, artifacts, mappings)
	if err != nil {
		return ArtifactMapping{}, err
	}
	mapping.State = "staging"
	mapping.ArtifactExpiresUnixMS = artifact.ExpiresUnixMS
	mapping.UpdatedUnixMS = nowMS
	if err := s.writeMappingLocked(mapping); err != nil {
		return ArtifactMapping{}, err
	}
	return mapping, nil
}

func (s *Store) reconcileMappingLocked(mapping ArtifactMapping, artifacts []Artifact, nowMS int64) (ArtifactMapping, error) {
	if mapping.State == "unavailable" || mapping.State == "expired" {
		return mapping, nil
	}
	if mapping.ArtifactID == nil {
		if mapping.State == "pending" {
			return mapping, nil
		}
		return ArtifactMapping{}, fail("storage_corrupt", "nonterminal artifact mapping has no artifact identity")
	}
	var artifact *Artifact
	for i := range artifacts {
		if artifacts[i].ID == *mapping.ArtifactID {
			artifact = &artifacts[i]
			break
		}
	}
	if artifact == nil {
		if mapping.State == "pending" {
			return mapping, nil
		}
		if mapping.ArtifactExpiresUnixMS > 0 && mapping.ArtifactExpiresUnixMS <= nowMS {
			return s.markMappingExpiredLocked(mapping, nowMS)
		}
		return ArtifactMapping{}, fail("storage_corrupt", "mapped artifact is missing before expiry")
	}
	if err := exactArtifactForMapping(*artifact, mapping); err != nil {
		return ArtifactMapping{}, err
	}
	if artifact.ExpiresUnixMS <= nowMS {
		return s.markMappingExpiredLocked(mapping, nowMS)
	}
	if artifact.State == "ready" {
		if mapping.State != "ready" {
			mapping.State = "ready"
			mapping.ArtifactExpiresUnixMS = artifact.ExpiresUnixMS
			mapping.UpdatedUnixMS = nowMS
			if err := s.writeMappingLocked(mapping); err != nil {
				return ArtifactMapping{}, err
			}
		}
		return mapping, nil
	}
	if mapping.State == "ready" {
		return ArtifactMapping{}, fail("storage_corrupt", "ready mapping points to staging artifact")
	}
	if mapping.State == "pending" {
		mapping.State = "staging"
		mapping.ArtifactExpiresUnixMS = artifact.ExpiresUnixMS
		mapping.UpdatedUnixMS = nowMS
		if err := s.writeMappingLocked(mapping); err != nil {
			return ArtifactMapping{}, err
		}
	}
	return mapping, nil
}

func (s *Store) markMappingUnavailableLocked(mapping ArtifactMapping, unavailable MappingUnavailable) (ArtifactMapping, error) {
	if !validUnavailable(unavailable) {
		return ArtifactMapping{}, fail("invalid_failure", "artifact mapping unavailable reason is invalid")
	}
	mapping.State = "unavailable"
	mapping.Unavailable = &unavailable
	mapping.ExpiredAtUnixMS = 0
	mapping.UpdatedUnixMS = s.now().UnixMilli()
	if err := s.writeMappingLocked(mapping); err != nil {
		return ArtifactMapping{}, err
	}
	return mapping, nil
}

func (s *Store) markMappingExpiredLocked(mapping ArtifactMapping, nowMS int64) (ArtifactMapping, error) {
	if mapping.ArtifactID == nil {
		return ArtifactMapping{}, fail("storage_corrupt", "expired mapping has no former artifact identity")
	}
	mapping.State = "expired"
	mapping.Unavailable = nil
	mapping.ExpiredAtUnixMS = nowMS
	mapping.UpdatedUnixMS = nowMS
	if err := s.writeMappingLocked(mapping); err != nil {
		return ArtifactMapping{}, err
	}
	return mapping, nil
}

func (s *Store) expireMappingForArtifactLocked(artifactID string, nowMS int64) error {
	mappings, _, err := s.mappingRecordsLocked()
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		if mapping.ArtifactID != nil && *mapping.ArtifactID == artifactID && mapping.State != "expired" && mapping.State != "unavailable" {
			if _, err := s.markMappingExpiredLocked(mapping, nowMS); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) mappingRecordsLocked() ([]ArtifactMapping, int64, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "artifact-mappings"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fail("storage_error", "scan artifact mappings: %v", err)
	}
	mappings := make([]ArtifactMapping, 0, len(entries))
	var totalBytes int64
	keys := map[string]bool{}
	clients := map[string]bool{}
	artifacts := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() || !canonicalUUID(entry.Name()) {
			return nil, 0, fail("storage_corrupt", "artifact mapping directory is invalid")
		}
		path := filepath.Join(s.root, "artifact-mappings", entry.Name(), "mapping.json")
		fi, statErr := os.Lstat(path)
		if statErr != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 || fi.Size() > MaxArtifactMappingBytes {
			return nil, 0, fail("storage_corrupt", "artifact mapping metadata is invalid")
		}
		totalBytes += fi.Size()
		if totalBytes > MaxArtifactMappingMetadataBytes {
			return nil, 0, fail("mapping_capacity", "artifact mapping metadata exceeds owner limit")
		}
		mapping, readErr := readStrict[ArtifactMapping](path)
		if readErr != nil || !validArtifactMapping(mapping) || mapping.MappingID != entry.Name() || mapping.OwnerMachine != s.machine {
			return nil, 0, fail("storage_corrupt", "artifact mapping metadata is invalid")
		}
		key := mapping.ThreadID + "\x00" + mapping.NativeSessionID + "\x00" + mapping.PublicationID
		if keys[key] || clients[mapping.ClientID] || (mapping.ArtifactID != nil && artifacts[*mapping.ArtifactID]) {
			return nil, 0, fail("storage_corrupt", "artifact mapping identity is duplicated")
		}
		keys[key] = true
		clients[mapping.ClientID] = true
		if mapping.ArtifactID != nil {
			artifacts[*mapping.ArtifactID] = true
		}
		mappings = append(mappings, mapping)
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].MappingID < mappings[j].MappingID })
	return mappings, totalBytes, nil
}

func (s *Store) writeMappingLocked(mapping ArtifactMapping) error {
	if !validArtifactMapping(mapping) || mapping.OwnerMachine != s.machine {
		return fail("storage_corrupt", "refusing to write invalid artifact mapping")
	}
	data, err := json.Marshal(mapping)
	if err != nil {
		return fail("storage_error", "encode artifact mapping: %v", err)
	}
	if len(data) > MaxArtifactMappingBytes {
		return fail("mapping_capacity", "artifact mapping metadata exceeds 8 KiB")
	}
	mappings, total, err := s.mappingRecordsLocked()
	if err != nil {
		return err
	}
	for _, existing := range mappings {
		if existing.MappingID == mapping.MappingID {
			if fi, statErr := os.Stat(filepath.Join(s.root, "artifact-mappings", existing.MappingID, "mapping.json")); statErr == nil {
				total -= fi.Size()
			}
			break
		}
	}
	if total+int64(len(data)) > MaxArtifactMappingMetadataBytes {
		return fail("mapping_capacity", "artifact mapping metadata exceeds owner limit")
	}
	path := filepath.Join(s.root, "artifact-mappings", mapping.MappingID, "mapping.json")
	return writeJSONAtomic(path, mapping)
}

func findMapping(mappingID, threadID, nativeSessionID, publicationID string, mappings []ArtifactMapping) (ArtifactMapping, bool, error) {
	mapping, found, err := findMappingByID(mappingID, mappings)
	if err != nil || !found {
		return mapping, found, err
	}
	if mapping.ThreadID != threadID || mapping.NativeSessionID != nativeSessionID || mapping.PublicationID != publicationID || mapping.ClientID != mappingID {
		return ArtifactMapping{}, false, fail("mapping_conflict", "artifact mapping hash collision or identity conflict")
	}
	return mapping, true, nil
}

func findMappingByID(mappingID string, mappings []ArtifactMapping) (ArtifactMapping, bool, error) {
	for _, mapping := range mappings {
		if mapping.MappingID == mappingID {
			return mapping, true, nil
		}
	}
	return ArtifactMapping{}, false, nil
}

func exactArtifactForMapping(artifact Artifact, mapping ArtifactMapping) error {
	if mapping.ArtifactID == nil || artifact.ID != *mapping.ArtifactID || artifact.ClientID != mapping.ClientID || artifact.ThreadID != mapping.ThreadID || artifact.OwnerMachine != mapping.OwnerMachine || artifact.Target != mapping.ProducerTarget || mapping.Metadata.SHA256 == nil || artifact.Name != mapping.Metadata.Name || artifact.MediaType != mapping.Metadata.MediaType || artifact.Size != mapping.Metadata.Size || artifact.SHA256 != *mapping.Metadata.SHA256 || artifact.Disposition != mapping.Metadata.Disposition {
		return fail("storage_corrupt", "managed artifact conflicts with its durable mapping")
	}
	return nil
}

func validateResolveArtifactMapping(in ResolveArtifactMapping) error {
	if !canonicalUUID(in.ThreadID) || !canonicalUUID(in.NativeSessionID) || !validMappingTarget(in.ConsumerTarget) || in.ConsumerTarget.SessionID != in.NativeSessionID || !validPublicationID(in.PublicationID) || !validPublicationOrigin(in.PublicationID, in.Metadata.Origin) || !validArtifactMetadata(in.Metadata) || !validMappingAvailability(in.Availability) {
		return fail("invalid_publication", "artifact mapping publication is invalid")
	}
	return nil
}

func validArtifactMapping(mapping ArtifactMapping) bool {
	derived, err := DeriveArtifactMappingID(mapping.ThreadID, mapping.NativeSessionID, mapping.PublicationID)
	if err != nil || mapping.Protocol != ArtifactMappingProtocol || mapping.Version != ArtifactMappingVersion || mapping.MappingID != derived || mapping.ClientID != derived || mapping.OwnerMachine == "" || !validMappingTarget(mapping.ProducerTarget) || mapping.ProducerTarget.SessionID != mapping.NativeSessionID || !validPublicationOrigin(mapping.PublicationID, mapping.Metadata.Origin) || !validArtifactMetadata(mapping.Metadata) || mapping.CreatedUnixMS <= 0 || mapping.UpdatedUnixMS < mapping.CreatedUnixMS {
		return false
	}
	switch mapping.State {
	case "pending", "staging", "ready":
		return mapping.Metadata.SHA256 != nil && mapping.ArtifactID != nil && canonicalUUID(*mapping.ArtifactID) && mapping.Unavailable == nil && mapping.ExpiredAtUnixMS == 0 && mapping.ArtifactExpiresUnixMS > 0
	case "unavailable":
		return mapping.Unavailable != nil && validUnavailable(*mapping.Unavailable) && mapping.ExpiredAtUnixMS == 0
	case "expired":
		return mapping.ArtifactID != nil && canonicalUUID(*mapping.ArtifactID) && mapping.Unavailable == nil && mapping.ExpiredAtUnixMS > 0 && mapping.ArtifactExpiresUnixMS > 0
	default:
		return false
	}
}

func validArtifactMetadata(metadata ArtifactMetadata) bool {
	if metadata.Kind != "image" && metadata.Kind != "file" {
		return false
	}
	if validateName(metadata.Name) != nil || metadata.MediaType == "" || validateMediaType(metadata.MediaType) != nil || metadata.Size < 0 || metadata.Size > MaxArtifactBytes || (metadata.Disposition != "inline" && metadata.Disposition != "attachment") || (metadata.Retention != "session_inline" && metadata.Retention != "runtime_source") || (metadata.Origin != "native" && metadata.Origin != "legacy_inline_image") {
		return false
	}
	return metadata.SHA256 == nil || lowerHexDigest(*metadata.SHA256)
}

func equalArtifactMetadata(a, b ArtifactMetadata) bool {
	if a.Kind != b.Kind || a.Name != b.Name || a.MediaType != b.MediaType || a.Size != b.Size || a.Disposition != b.Disposition || a.Retention != b.Retention || a.Origin != b.Origin {
		return false
	}
	if a.SHA256 == nil || b.SHA256 == nil {
		return a.SHA256 == nil && b.SHA256 == nil
	}
	return *a.SHA256 == *b.SHA256
}

func validMappingTarget(target Target) bool {
	return target.StreamID != "" && len(target.StreamID) <= 128 && canonicalUUID(target.SessionID) && target.SessionEpoch >= 0
}

func validPublicationID(id string) bool {
	if canonicalUUID(id) {
		return true
	}
	return len(id) == 64 && lowerHexDigest(id)
}

func validPublicationOrigin(publicationID, origin string) bool {
	if origin == "native" {
		return canonicalUUID(publicationID)
	}
	return origin == "legacy_inline_image" && len(publicationID) == 64 && lowerHexDigest(publicationID)
}

func validMappingAvailability(availability MappingAvailability) bool {
	switch availability.Status {
	case "available":
		return availability.Reason == "" && availability.Message == ""
	case "unavailable":
		return nativeUnavailableCodes[availability.Reason] && validMappingMessage(availability.Message)
	default:
		return false
	}
}

var nativeUnavailableCodes = map[string]bool{
	"source_not_retained": true,
	"source_failed":       true,
	"invalid_bytes":       true,
	"byte_limit":          true,
	"lifecycle_ended":     true,
}

var terminalFailureCodes = map[string]bool{
	"artifact_not_found":   true,
	"artifact_unavailable": true,
	"source_failed":        true,
	"byte_count_mismatch":  true,
	"digest_mismatch":      true,
	"invalid_limits":       true,
	"quota_exceeded":       true,
}

func validTerminalFailure(unavailable MappingUnavailable) bool {
	return (unavailable.Source == "native_copy" || unavailable.Source == "sesh") && terminalFailureCodes[unavailable.Code] && validMappingMessage(unavailable.Message)
}

func validUnavailable(unavailable MappingUnavailable) bool {
	if !validMappingMessage(unavailable.Message) {
		return false
	}
	switch unavailable.Source {
	case "native":
		return nativeUnavailableCodes[unavailable.Code]
	case "native_copy":
		return terminalFailureCodes[unavailable.Code]
	case "sesh":
		return terminalFailureCodes[unavailable.Code] || unavailable.Code == "digest_unavailable"
	default:
		return false
	}
}

func validMappingMessage(message string) bool {
	if len(message) > MaxArtifactMappingMessageBytes || !utf8.ValidString(message) || strings.ContainsAny(message, "/\\") {
		return false
	}
	for _, r := range message {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Store) createArtifactLocked(id string, in StartArtifact, attachments []Attachment, artifacts []Artifact, mappings []ArtifactMapping) (Artifact, error) {
	if !canonicalUUID(id) {
		return Artifact{}, fail("invalid_request", "reserved artifact id is invalid")
	}
	if len(attachments)+len(artifacts)+len(mappings) >= MaxManagedRecords {
		return Artifact{}, fail("mapping_capacity", "managed record capacity is exhausted")
	}
	var total int64
	for _, attachment := range attachments {
		total += attachment.Size
	}
	for _, artifact := range artifacts {
		total += artifact.Size
	}
	if total+in.Size > MaxDurableBytes {
		return Artifact{}, fail("quota_exceeded", "owner durable transfer storage quota exceeded")
	}
	dir := filepath.Join(s.root, "artifacts", id)
	if _, err := os.Lstat(dir); err == nil {
		return Artifact{}, fail("storage_corrupt", "reserved artifact path already exists")
	} else if !os.IsNotExist(err) {
		return Artifact{}, fail("storage_error", "inspect reserved artifact path: %v", err)
	}
	if err := secureMkdir(dir); err != nil {
		return Artifact{}, err
	}
	payload := filepath.Join(dir, "payload")
	f, err := os.OpenFile(payload, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Artifact{}, fail("storage_error", "create artifact payload: %v", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.RemoveAll(dir)
		return Artifact{}, fail("storage_error", "protect artifact payload: %v", err)
	}
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return Artifact{}, fail("storage_error", "close artifact payload: %v", err)
	}
	now := s.now()
	ms := now.UnixMilli()
	artifact := Artifact{Version: 1, ID: id, ClientID: in.ClientID, ThreadID: in.ThreadID, OwnerMachine: s.machine, Target: in.Target, Name: in.Name, MediaType: in.MediaType, Size: in.Size, SHA256: in.SHA256, Disposition: in.Disposition, State: "staging", CreatedUnixMS: ms, UpdatedUnixMS: ms, ExpiresUnixMS: now.Add(ArtifactTTL).UnixMilli()}
	if err := writeJSONAtomic(filepath.Join(dir, "metadata.json"), artifact); err != nil {
		_ = os.RemoveAll(dir)
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) putArtifactLocked(artifact Artifact, body io.Reader, length int64) (Artifact, error) {
	if artifact.ExpiresUnixMS <= s.now().UnixMilli() {
		return Artifact{}, fail("upload_expired", "artifact publication has expired")
	}
	if length != artifact.Size {
		return Artifact{}, fail("byte_count_mismatch", "artifact Content-Length does not match declared size")
	}
	dataReader := io.LimitReader(body, length+1)
	tmp := filepath.Join(s.root, "artifacts", artifact.ID, "payload.tmp")
	if fi, err := os.Lstat(tmp); err == nil {
		if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
			return Artifact{}, fail("storage_corrupt", "artifact temporary payload is not a regular file")
		}
		if err := os.Remove(tmp); err != nil {
			return Artifact{}, fail("storage_error", "remove stale artifact temporary payload: %v", err)
		}
	} else if !os.IsNotExist(err) {
		return Artifact{}, fail("storage_error", "inspect artifact temporary payload: %v", err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Artifact{}, fail("storage_error", "create artifact temporary payload: %v", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return Artifact{}, fail("storage_error", "protect artifact temporary payload: %v", err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), dataReader)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != length {
		_ = os.Remove(tmp)
		return Artifact{}, fail("byte_count_mismatch", "artifact body did not match declared length")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if digest != artifact.SHA256 {
		_ = os.Remove(tmp)
		return Artifact{}, fail("digest_mismatch", "artifact SHA-256 does not match")
	}
	payload := filepath.Join(s.root, "artifacts", artifact.ID, "payload")
	if artifact.State == "ready" {
		_ = os.Remove(tmp)
		size, stored, err := fileDigest(payload, MaxArtifactBytes)
		if err != nil {
			return Artifact{}, err
		}
		if size == length && stored == digest {
			return artifact, nil
		}
		return Artifact{}, fail("mapping_conflict", "ready artifact bytes conflict")
	}
	if artifact.State != "staging" {
		_ = os.Remove(tmp)
		return Artifact{}, fail("mapping_conflict", "artifact is not staging")
	}
	if err := os.Rename(tmp, payload); err != nil {
		_ = os.Remove(tmp)
		return Artifact{}, fail("storage_error", "install artifact payload: %v", err)
	}
	now := s.now()
	artifact.State = "ready"
	artifact.UpdatedUnixMS = now.UnixMilli()
	artifact.ExpiresUnixMS = now.Add(ArtifactTTL).UnixMilli()
	if err := s.writeArtifactLocked(artifact); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}
