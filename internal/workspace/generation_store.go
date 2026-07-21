package workspace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	WorkspaceGenerationsDirectoryName = "generations"
	MaxStoredGenerationBytes          = 64 << 20
)

type storedArtifactDocumentWire struct {
	Kind      ArtifactKind    `json:"kind"`
	ID        string          `json:"id"`
	Path      string          `json:"path"`
	Canonical json.RawMessage `json:"canonical"`
}

type storedAuthorityDocumentWire struct {
	ID        string          `json:"id"`
	Kind      AuthorityKind   `json:"kind"`
	Canonical json.RawMessage `json:"canonical"`
}

type storedGenerationWire struct {
	SchemaVersion int                           `json:"schema_version"`
	Generation    string                        `json:"generation"`
	Identity      canonicalGeneration           `json:"identity"`
	Artifacts     []storedArtifactDocumentWire  `json:"artifacts"`
	Authorities   []storedAuthorityDocumentWire `json:"authorities"`
}

type StoredGeneration struct {
	workspaceID      ID
	generation       Digest
	definitionDigest Digest
	canonical        []byte
}

func (generation StoredGeneration) WorkspaceID() ID          { return generation.workspaceID }
func (generation StoredGeneration) Generation() Digest       { return generation.generation }
func (generation StoredGeneration) DefinitionDigest() Digest { return generation.definitionDigest }
func (generation StoredGeneration) CanonicalBytes() []byte {
	return append([]byte(nil), generation.canonical...)
}

type GenerationStore struct {
	workspaceDir   string
	generationsDir string
}

func WorkspaceGenerationsDirectory(workspaceDir string) string {
	return filepath.Join(WorkspaceStateDirectory(workspaceDir), WorkspaceGenerationsDirectoryName)
}

func OpenGenerationStore(workspaceDir string) (*GenerationStore, error) {
	workspaceDir = filepath.Clean(workspaceDir)
	if !filepath.IsAbs(workspaceDir) {
		return nil, fmt.Errorf("generation store requires an absolute workspace directory")
	}
	if err := ensureSynchronizedDirectory(WorkspaceStateDirectory(workspaceDir)); err != nil {
		return nil, err
	}
	generationsDir := WorkspaceGenerationsDirectory(workspaceDir)
	if err := ensureSynchronizedDirectory(generationsDir); err != nil {
		return nil, err
	}
	return &GenerationStore{workspaceDir: workspaceDir, generationsDir: generationsDir}, nil
}

func (store *GenerationStore) Store(definition EffectiveWorkspaceDefinition) (StoredGeneration, error) {
	if store == nil || definition.generation.IsZero() {
		return StoredGeneration{}, fmt.Errorf("generation store and effective definition are required")
	}
	canonical, err := marshalStoredGeneration(definition)
	if err != nil {
		return StoredGeneration{}, err
	}
	if len(canonical) > MaxStoredGenerationBytes {
		return StoredGeneration{}, fmt.Errorf("stored generation exceeds %d bytes", MaxStoredGenerationBytes)
	}
	path := store.path(definition.generation)
	if existing, err := readBoundedFile(path, MaxStoredGenerationBytes); err == nil {
		if !bytes.Equal(existing, canonical) {
			return StoredGeneration{}, fmt.Errorf("generation %s already exists with different canonical bytes", definition.generation)
		}
		if err := syncFileAndDirectory(path, store.generationsDir); err != nil {
			return StoredGeneration{}, fmt.Errorf("synchronize existing generation %s: %w", definition.generation, err)
		}
		return parseStoredGeneration(existing, definition.generation)
	} else if !os.IsNotExist(err) {
		return StoredGeneration{}, err
	}
	if err := atomicWriteSynchronized(path, canonical, 0o644); err != nil {
		return StoredGeneration{}, err
	}
	return parseStoredGeneration(canonical, definition.generation)
}

func (store *GenerationStore) Load(generation Digest) (StoredGeneration, error) {
	if store == nil || generation.IsZero() {
		return StoredGeneration{}, fmt.Errorf("generation store and generation are required")
	}
	content, err := readBoundedFile(store.path(generation), MaxStoredGenerationBytes)
	if err != nil {
		return StoredGeneration{}, err
	}
	return parseStoredGeneration(content, generation)
}

func (store *GenerationStore) Contains(generation Digest) bool {
	if store == nil || generation.IsZero() {
		return false
	}
	_, err := store.Load(generation)
	return err == nil
}

func (store *GenerationStore) List() ([]Digest, error) {
	if store == nil {
		return nil, fmt.Errorf("generation store is required")
	}
	entries, err := os.ReadDir(store.generationsDir)
	if err != nil {
		return nil, err
	}
	result := make([]Digest, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "generation-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(name, "generation-"), ".json")
		if len(raw) != 64 {
			continue
		}
		if _, err := hex.DecodeString(raw); err != nil {
			continue
		}
		digest, err := ParseDigest("sha256:" + raw)
		if err != nil {
			return nil, err
		}
		if _, err := store.Load(digest); err != nil {
			return nil, fmt.Errorf("validate stored generation %s: %w", digest, err)
		}
		result = append(result, digest)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, nil
}

func (store *GenerationStore) OrphanCandidates(snapshot JournalSnapshot) ([]Digest, error) {
	stored, err := store.List()
	if err != nil {
		return nil, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil && len(snapshot.records) != 0 {
		return nil, err
	}
	recorded := append([]Digest(nil), runtime.generationHistory...)
	recorded = append(recorded, sortedCandidateGenerations(runtime)...)
	orphans := make([]Digest, 0)
	for _, generation := range stored {
		if !containsDigest(recorded, generation) {
			orphans = append(orphans, generation)
		}
	}
	return orphans, nil
}

func (store *GenerationStore) StageCandidate(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	occurredAt time.Time,
) (JournalRecord, error) {
	if journal == nil || store == nil || journal.workspaceDir != store.workspaceDir {
		return JournalRecord{}, fmt.Errorf("candidate staging requires journal and generation store for the same workspace")
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalRecord{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalRecord{}, err
	}
	if runtime.workspaceID != definition.workspace.id {
		return JournalRecord{}, fmt.Errorf("candidate definition belongs to workspace %s, not %s", definition.workspace.id, runtime.workspaceID)
	}
	if definition.generation == runtime.activeGeneration || runtime.HasCandidate(definition.generation) {
		return JournalRecord{}, fmt.Errorf("generation %s is already active or staged", definition.generation)
	}
	if _, err := store.Store(definition); err != nil {
		return JournalRecord{}, err
	}
	event, err := NewCandidateGenerationStoredJournalEvent(
		runtime.workspaceID, runtime.activeGeneration, definition.generation, false,
	)
	if err != nil {
		return JournalRecord{}, err
	}
	workspaceResource := WorkspaceJournalResource(runtime.workspaceID)
	candidateResource := GenerationJournalResource(definition.generation)
	workspaceRevision, _ := NewJournalResourceRevision(workspaceResource, snapshot.Revision(workspaceResource))
	candidateRevision, _ := NewJournalResourceRevision(candidateResource, snapshot.Revision(candidateResource))
	appendRequest, err := NewJournalAppend(
		event, occurredAt,
		[]JournalResourceRevision{workspaceRevision, candidateRevision},
		[]JournalResource{workspaceResource, candidateResource},
	)
	if err != nil {
		return JournalRecord{}, err
	}
	return journal.Append(appendRequest)
}

func (store *GenerationStore) RecoverOrphanCandidate(
	journal *WorkspaceJournal,
	generation Digest,
	occurredAt time.Time,
) (JournalRecord, error) {
	if journal == nil || store == nil || journal.workspaceDir != store.workspaceDir {
		return JournalRecord{}, fmt.Errorf("orphan recovery requires journal and generation store for the same workspace")
	}
	stored, err := store.Load(generation)
	if err != nil {
		return JournalRecord{}, err
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalRecord{}, err
	}
	orphans, err := store.OrphanCandidates(snapshot)
	if err != nil {
		return JournalRecord{}, err
	}
	if !containsDigest(orphans, generation) {
		return JournalRecord{}, fmt.Errorf("generation %s is not an orphan candidate", generation)
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalRecord{}, err
	}
	if stored.workspaceID != runtime.workspaceID {
		return JournalRecord{}, fmt.Errorf(
			"orphan generation belongs to workspace %s, not %s",
			stored.workspaceID, runtime.workspaceID,
		)
	}
	event, err := NewCandidateGenerationStoredJournalEvent(runtime.workspaceID, runtime.activeGeneration, generation, true)
	if err != nil {
		return JournalRecord{}, err
	}
	workspaceResource := WorkspaceJournalResource(runtime.workspaceID)
	candidateResource := GenerationJournalResource(generation)
	workspaceRevision, _ := NewJournalResourceRevision(workspaceResource, snapshot.Revision(workspaceResource))
	candidateRevision, _ := NewJournalResourceRevision(candidateResource, snapshot.Revision(candidateResource))
	appendRequest, err := newPrivilegedJournalAppend(
		event, occurredAt,
		[]JournalResourceRevision{workspaceRevision, candidateRevision},
		[]JournalResource{workspaceResource, candidateResource},
	)
	if err != nil {
		return JournalRecord{}, err
	}
	return journal.Append(appendRequest)
}

func (store *GenerationStore) path(generation Digest) string {
	hexDigest := strings.TrimPrefix(generation.String(), "sha256:")
	return filepath.Join(store.generationsDir, "generation-"+hexDigest+".json")
}

func marshalStoredGeneration(definition EffectiveWorkspaceDefinition) ([]byte, error) {
	identity := canonicalGenerationIdentity(definition)
	identityBytes, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	if DigestBytes(identityBytes) != definition.generation {
		return nil, fmt.Errorf("effective definition generation does not match its canonical identity")
	}
	artifacts := make([]storedArtifactDocumentWire, 0, len(definition.artifacts))
	for _, artifact := range definition.artifacts {
		artifacts = append(artifacts, storedArtifactDocumentWire{
			Kind: artifact.kind, ID: artifact.id.String(), Path: artifact.path,
			Canonical: json.RawMessage(append([]byte(nil), artifact.canonical...)),
		})
	}
	authorities := make([]storedAuthorityDocumentWire, 0, len(definition.authorities))
	for _, authority := range definition.authorities {
		canonical, err := canonicalAuthorityBytes(authority)
		if err != nil {
			return nil, err
		}
		authorities = append(authorities, storedAuthorityDocumentWire{
			ID: authority.id.String(), Kind: authority.kind, Canonical: json.RawMessage(canonical),
		})
	}
	return json.Marshal(storedGenerationWire{
		SchemaVersion: JournalSchemaVersion, Generation: definition.generation.String(),
		Identity: identity, Artifacts: artifacts, Authorities: authorities,
	})
}

func canonicalGenerationIdentity(definition EffectiveWorkspaceDefinition) canonicalGeneration {
	identity := canonicalGeneration{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: definition.workspace.id.String(),
		Artifacts:   make([]canonicalArtifactIdentity, 0, len(definition.artifacts)),
		Authorities: make([]canonicalAuthorityIdentity, 0, len(definition.authorities)),
	}
	for _, artifact := range definition.artifacts {
		identity.Artifacts = append(identity.Artifacts, canonicalArtifactIdentity{
			Kind: artifact.kind, ID: artifact.id.String(), Path: artifact.path,
			SourceHash: artifact.sourceHash.String(), SemanticHash: artifact.semanticHash.String(),
		})
	}
	for _, authority := range definition.authorities {
		identity.Authorities = append(identity.Authorities, canonicalAuthorityIdentity{
			ID: authority.id.String(), Kind: authority.kind,
			SourceHash: authority.sourceHash.String(), SemanticHash: authority.semanticHash.String(),
		})
	}
	return identity
}

func parseStoredGeneration(content []byte, expected Digest) (StoredGeneration, error) {
	var wire storedGenerationWire
	if err := decodeStrictJSON(content, &wire); err != nil {
		return StoredGeneration{}, fmt.Errorf("decode stored generation: %w", err)
	}
	if wire.SchemaVersion != JournalSchemaVersion {
		return StoredGeneration{}, fmt.Errorf("stored generation schema_version must be %d", JournalSchemaVersion)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return StoredGeneration{}, err
	}
	if !bytes.Equal(canonical, content) {
		return StoredGeneration{}, fmt.Errorf("stored generation is not canonical JSON")
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return StoredGeneration{}, err
	}
	if generation != expected {
		return StoredGeneration{}, fmt.Errorf("stored generation identity %s does not match requested %s", generation, expected)
	}
	identityBytes, err := json.Marshal(wire.Identity)
	if err != nil {
		return StoredGeneration{}, err
	}
	if DigestBytes(identityBytes) != generation {
		return StoredGeneration{}, fmt.Errorf("stored generation canonical identity does not match %s", generation)
	}
	if len(wire.Artifacts) != len(wire.Identity.Artifacts) || len(wire.Authorities) != len(wire.Identity.Authorities) {
		return StoredGeneration{}, fmt.Errorf("stored generation documents do not match identity cardinality")
	}
	for index, artifact := range wire.Artifacts {
		identity := wire.Identity.Artifacts[index]
		if artifact.Kind != identity.Kind || artifact.ID != identity.ID || artifact.Path != identity.Path {
			return StoredGeneration{}, fmt.Errorf("stored artifact %d does not match its identity", index)
		}
		semanticHash, err := ParseDigest(identity.SemanticHash)
		if err != nil {
			return StoredGeneration{}, err
		}
		if _, err := ParseDigest(identity.SourceHash); err != nil {
			return StoredGeneration{}, err
		}
		if !json.Valid(artifact.Canonical) || DigestBytes(artifact.Canonical) != semanticHash {
			return StoredGeneration{}, fmt.Errorf("stored artifact %s canonical bytes do not match semantic hash", artifact.ID)
		}
	}
	for index, authority := range wire.Authorities {
		identity := wire.Identity.Authorities[index]
		if authority.ID != identity.ID || authority.Kind != identity.Kind {
			return StoredGeneration{}, fmt.Errorf("stored authority %d does not match its identity", index)
		}
		semanticHash, err := ParseDigest(identity.SemanticHash)
		if err != nil {
			return StoredGeneration{}, err
		}
		if !json.Valid(authority.Canonical) || DigestBytes(authority.Canonical) != semanticHash {
			return StoredGeneration{}, fmt.Errorf("stored authority %s canonical bytes do not match semantic hash", authority.ID)
		}
		if _, err := ParseDigest(identity.SourceHash); err != nil {
			return StoredGeneration{}, err
		}
	}
	workspaceID, err := NewID(wire.Identity.WorkspaceID)
	if err != nil {
		return StoredGeneration{}, err
	}
	return StoredGeneration{
		workspaceID:      workspaceID,
		generation:       generation,
		definitionDigest: DigestBytes(content),
		canonical:        append([]byte(nil), content...),
	}, nil
}
