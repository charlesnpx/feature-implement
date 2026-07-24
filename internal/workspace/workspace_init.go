package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

const WorkspaceRuntimeProjectionFileName = "runtime-projection.v3.json"

type WorkspaceInitializationResult struct {
	storedGeneration StoredGeneration
	snapshot         JournalSnapshot
	runtime          WorkspaceRuntimeProjection
	projectionDigest Digest
}

func (result WorkspaceInitializationResult) StoredGeneration() StoredGeneration {
	return result.storedGeneration
}
func (result WorkspaceInitializationResult) Snapshot() JournalSnapshot { return result.snapshot }
func (result WorkspaceInitializationResult) Runtime() WorkspaceRuntimeProjection {
	return cloneWorkspaceRuntime(result.runtime)
}
func (result WorkspaceInitializationResult) ProjectionDigest() Digest { return result.projectionDigest }

func WorkspaceRuntimeProjectionPath(workspaceDir string) string {
	return filepath.Join(WorkspaceStateDirectory(workspaceDir), WorkspaceRuntimeProjectionFileName)
}

func InitializeWorkspaceV2(
	workspaceDir string,
	definition EffectiveWorkspaceDefinition,
	occurredAt time.Time,
	planCheckpoint ...GitObjectID,
) (result WorkspaceInitializationResult, resultErr error) {
	if definition.generation.IsZero() || occurredAt.IsZero() {
		return WorkspaceInitializationResult{}, fmt.Errorf("workspace initialization requires an effective definition and occurrence time")
	}
	if len(planCheckpoint) > 1 {
		return WorkspaceInitializationResult{}, fmt.Errorf("workspace initialization accepts one plan checkpoint")
	}
	checkpoint := GitObjectID{}
	if len(planCheckpoint) == 1 {
		checkpoint = planCheckpoint[0]
	}
	requiresCheckpoint := false
	for _, artifact := range definition.artifacts {
		if artifact.kind == ArtifactWorkspaceBundle {
			requiresCheckpoint = true
			break
		}
	}
	if requiresCheckpoint && checkpoint.IsZero() {
		return WorkspaceInitializationResult{}, fmt.Errorf("workspace bundle initialization requires a verified plan lock checkpoint")
	}
	roots, err := OpenWorkspaceInitializationRootGuard(
		"", workspaceDir, definition.workspace.repositoryRoot,
	)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	defer roots.Close()
	if err := roots.VerifyBeforeRuntimeCreation(); err != nil {
		return WorkspaceInitializationResult{}, err
	}
	defer func() {
		var verifyErr error
		if resultErr == nil {
			verifyErr = roots.VerifyAfterRuntimeCreation()
		} else {
			verifyErr = roots.VerifyAfterEffects()
		}
		if verifyErr != nil {
			result = WorkspaceInitializationResult{}
			resultErr = errors.Join(resultErr, verifyErr)
		}
	}()
	store, err := OpenGenerationStore(workspaceDir)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	defer store.Close()
	journal, err := OpenWorkspaceJournal(workspaceDir, JournalReadWrite)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	defer journal.Close()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	needsInitialization := len(snapshot.records) == 0
	if len(snapshot.records) != 0 {
		existing, err := RebuildWorkspaceRuntime(snapshot)
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
		if existing.activeGeneration.IsZero() {
			if existing.workspaceID != definition.workspace.id || len(existing.recoveries) == 0 {
				return WorkspaceInitializationResult{}, fmt.Errorf("pre-initialization journal does not match workspace %s", definition.workspace.id)
			}
			for _, recovery := range existing.recoveries {
				if recovery.generation != definition.generation {
					return WorkspaceInitializationResult{}, fmt.Errorf(
						"pre-initialization journal is bound to generation %s, not %s",
						recovery.generation, definition.generation,
					)
				}
			}
			needsInitialization = true
		} else if existing.workspaceID != definition.workspace.id || existing.activeGeneration != definition.generation {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"workspace is already initialized as %s at generation %s",
				existing.workspaceID, existing.activeGeneration,
			)
		} else if requiresCheckpoint && existing.planCheckpoint != checkpoint {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"workspace is already initialized at plan checkpoint %s",
				existing.planCheckpoint,
			)
		}
	}
	stored, err := store.Store(definition)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if needsInitialization {
		event, err := NewWorkspaceInitializedJournalEvent(
			definition.workspace.id,
			definition.generation,
			stored.definitionDigest,
			planCheckpoint...,
		)
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
		workspaceResource := WorkspaceJournalResource(definition.workspace.id)
		generationResource := GenerationJournalResource(definition.generation)
		workspaceRevision, _ := NewJournalResourceRevision(workspaceResource, snapshot.Revision(workspaceResource))
		generationRevision, _ := NewJournalResourceRevision(generationResource, snapshot.Revision(generationResource))
		appendRequest, err := NewJournalAppend(
			event, occurredAt,
			[]JournalResourceRevision{workspaceRevision, generationRevision},
			[]JournalResource{workspaceResource, generationResource},
		)
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
		if _, err := journal.Append(appendRequest); err != nil {
			return WorkspaceInitializationResult{}, err
		}
		snapshot, err = journal.ReadSnapshot()
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if runtime.workspaceID != definition.workspace.id || runtime.activeGeneration != definition.generation {
		return WorkspaceInitializationResult{}, fmt.Errorf("initialized runtime does not match the effective definition")
	}
	if requiresCheckpoint && runtime.planCheckpoint != checkpoint {
		return WorkspaceInitializationResult{}, fmt.Errorf("initialized runtime does not match the verified plan checkpoint")
	}
	projectionDigest, err := writeWorkspaceRuntimeProjectionAt(journal.runtime, snapshot, runtime)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	result = WorkspaceInitializationResult{
		storedGeneration: stored, snapshot: snapshot,
		runtime: runtime, projectionDigest: projectionDigest,
	}
	return result, nil
}

func RebuildWorkspaceRuntimeProjectionFile(journal *WorkspaceJournal) (Digest, error) {
	if journal == nil {
		return Digest{}, fmt.Errorf("workspace journal is required")
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return Digest{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return Digest{}, err
	}
	return writeWorkspaceRuntimeProjectionAt(journal.runtime, snapshot, runtime)
}

func writeWorkspaceRuntimeProjection(
	workspaceDir string,
	snapshot JournalSnapshot,
	runtime WorkspaceRuntimeProjection,
) (Digest, error) {
	storage, err := OpenRuntimeStorage(workspaceDir, true)
	if err != nil {
		return Digest{}, err
	}
	defer storage.Close()
	return writeWorkspaceRuntimeProjectionAt(storage, snapshot, runtime)
}

func writeWorkspaceRuntimeProjectionAt(
	storage *RuntimeStorage,
	snapshot JournalSnapshot,
	runtime WorkspaceRuntimeProjection,
) (Digest, error) {
	if storage == nil {
		return Digest{}, fmt.Errorf("runtime storage is required")
	}
	projectionBytes, err := canonicalWorkspaceRuntime(runtime)
	if err != nil {
		return Digest{}, err
	}
	conformanceDigest, err := VerifyWorkspaceRuntimeConformance(snapshot, runtime.activeGeneration)
	if err != nil {
		return Digest{}, err
	}
	if DigestBytes(projectionBytes) != conformanceDigest {
		return Digest{}, fmt.Errorf("runtime projection does not match replay conformance digest")
	}
	type projectionFile struct {
		SchemaVersion    int             `json:"schema_version"`
		JournalHead      string          `json:"journal_head"`
		ProjectionDigest string          `json:"projection_digest"`
		Projection       json.RawMessage `json:"projection"`
	}
	content, err := json.Marshal(projectionFile{
		SchemaVersion: JournalSchemaVersion, JournalHead: snapshot.head.String(),
		ProjectionDigest: conformanceDigest.String(), Projection: json.RawMessage(projectionBytes),
	})
	if err != nil {
		return Digest{}, err
	}
	if err := storage.state.PublishReplaceable(
		WorkspaceRuntimeProjectionFileName,
		content,
		0o600,
		MaxJournalBytes,
		PublicationOptions{},
	); err != nil {
		return Digest{}, err
	}
	return conformanceDigest, nil
}
