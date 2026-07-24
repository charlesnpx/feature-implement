package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

type WorkspaceInitializationOptions struct {
	PlanCheckpoint *VerifiedPlanLockCheckpoint
	TargetGit      *LocalTargetGitAdapter
	TargetFault    LocalTargetInitializationFaultInjector
	WorktreeRoot   string
}

func InitializeWorkspaceV2WithOptions(
	ctx context.Context,
	workspaceDir string,
	definition EffectiveWorkspaceDefinition,
	occurredAt time.Time,
	options WorkspaceInitializationOptions,
) (result WorkspaceInitializationResult, resultErr error) {
	if ctx == nil {
		return WorkspaceInitializationResult{}, fmt.Errorf(
			"workspace initialization requires context",
		)
	}
	if definition.generation.IsZero() || occurredAt.IsZero() {
		return WorkspaceInitializationResult{}, fmt.Errorf("workspace initialization requires an effective definition and occurrence time")
	}
	worktreeRoot, err := resolveInitializationWorktreeRoot(
		options.WorktreeRoot,
	)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	checkpoint := VerifiedPlanLockCheckpoint{}
	checkpointID := GitObjectID{}
	hasPlanCheckpoint := options.PlanCheckpoint != nil
	if hasPlanCheckpoint {
		checkpoint = *options.PlanCheckpoint
		if checkpoint.root == "" ||
			checkpoint.commit.IsZero() ||
			checkpoint.tree.IsZero() ||
			checkpoint.sourceDigest.IsZero() ||
			checkpoint.semanticDigest.IsZero() ||
			checkpoint.generation.IsZero() ||
			checkpoint.lockDigest.IsZero() {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"workspace initialization requires a nonzero verified plan lock checkpoint",
			)
		}
		if checkpoint.lease == nil || !checkpoint.lease.active.Load() {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"workspace initialization requires an active plan lock verification lease",
			)
		}
		if checkpoint.generation != definition.generation {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"verified plan lock checkpoint generation %s does not match workspace generation %s",
				checkpoint.generation,
				definition.generation,
			)
		}
		checkpointID = checkpoint.commit
	}
	requiresCheckpoint := false
	for _, artifact := range definition.artifacts {
		if artifact.kind == ArtifactWorkspaceBundle {
			requiresCheckpoint = true
			break
		}
	}
	if requiresCheckpoint && !hasPlanCheckpoint {
		return WorkspaceInitializationResult{}, fmt.Errorf("workspace bundle initialization requires a verified plan lock checkpoint")
	}
	planRoot := ""
	if hasPlanCheckpoint {
		planRoot = checkpoint.root
	}
	roots, err := OpenWorkspaceInitializationRootGuard(
		planRoot, workspaceDir, definition.workspace.repositoryRoot,
		worktreeRoot,
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
	targetGit := DefaultLocalTargetGitAdapter()
	if options.TargetGit != nil {
		targetGit = *options.TargetGit
	}
	if targetGit.git.executable == "" {
		return WorkspaceInitializationResult{}, fmt.Errorf(
			"workspace initialization requires a local target Git adapter",
		)
	}
	preflightSnapshot := JournalSnapshot{}
	preflightSnapshotKnown := false
	runtimeInitialized := false
	if roots.runtime != nil {
		_, runtimeInitialized, err = roots.runtime.adapter.inspectExact(
			RuntimeFormatFileName,
		)
		if err != nil {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"inspect workspace runtime initialization marker: %w", err,
			)
		}
	}
	if runtimeInitialized {
		preflightSnapshot, err = ReadWorkspaceJournalSnapshot(workspaceDir)
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
		preflightSnapshotKnown = true
	}
	targetInspection, err := inspectLocalTargetForInitializationAdmission(
		ctx,
		targetGit,
		definition,
		preflightSnapshot,
	)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if err := roots.bindLocalTarget(ctx, targetGit, targetInspection); err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if err := roots.VerifyBeforeRuntimeCreation(); err != nil {
		return WorkspaceInitializationResult{}, err
	}
	store, err := OpenGenerationStore(workspaceDir)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	defer store.Close()
	if err := roots.VerifyAfterEffects(); err != nil {
		return WorkspaceInitializationResult{}, err
	}
	journal, err := OpenWorkspaceJournal(workspaceDir, JournalReadWrite)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	defer journal.Close()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if preflightSnapshotKnown &&
		(snapshot.head != preflightSnapshot.head ||
			snapshot.byteLength != preflightSnapshot.byteLength) {
		return WorkspaceInitializationResult{}, fmt.Errorf(
			"workspace journal changed during local target root admission",
		)
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
				"workspace is already initialized as %s at generation %s; workspace %s at generation %s requires a fresh runtime directory",
				existing.workspaceID,
				existing.activeGeneration,
				definition.workspace.id,
				definition.generation,
			)
		} else if requiresCheckpoint && existing.planCheckpoint != checkpointID {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"workspace is already initialized at plan checkpoint %s",
				existing.planCheckpoint,
			)
		} else {
			worktreeBinding, bindingErr := roots.WorktreeRootBinding()
			if bindingErr != nil {
				return WorkspaceInitializationResult{}, bindingErr
			}
			if existing.worktreeRoot != worktreeBinding {
				return WorkspaceInitializationResult{}, fmt.Errorf(
					"workspace is already initialized with worktree root %s",
					existing.worktreeRoot.Path(),
				)
			}
		}
	}
	stored, err := store.Store(definition)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if needsInitialization {
		worktreeBinding, err := roots.WorktreeRootBinding()
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
		eventCheckpoint := []GitObjectID(nil)
		if hasPlanCheckpoint {
			eventCheckpoint = append(eventCheckpoint, checkpointID)
		}
		event, err := NewWorkspaceInitializedJournalEvent(
			definition.workspace.id,
			definition.generation,
			stored.definitionDigest,
			worktreeBinding,
			eventCheckpoint...,
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
	targetFault := func(point LocalTargetInitializationFaultPoint) error {
		if options.TargetFault != nil {
			if err := options.TargetFault(point); err != nil {
				return err
			}
		}
		return roots.VerifyAfterEffects()
	}
	snapshot, err = initializeLocalTarget(
		ctx,
		journal,
		snapshot,
		definition,
		occurredAt,
		targetGit,
		targetFault,
	)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if runtime.workspaceID != definition.workspace.id || runtime.activeGeneration != definition.generation {
		return WorkspaceInitializationResult{}, fmt.Errorf("initialized runtime does not match the effective definition")
	}
	if requiresCheckpoint && runtime.planCheckpoint != checkpointID {
		return WorkspaceInitializationResult{}, fmt.Errorf("initialized runtime does not match the verified plan checkpoint")
	}
	worktreeBinding, err := roots.WorktreeRootBinding()
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if runtime.worktreeRoot != worktreeBinding {
		return WorkspaceInitializationResult{}, fmt.Errorf(
			"initialized runtime does not match the verified worktree root",
		)
	}
	targetRuntime, ok := runtime.LocalTarget()
	if !ok || !targetRuntime.Created() ||
		targetRuntime.binding.root != definition.workspace.target.root ||
		targetRuntime.binding.baseRef != definition.workspace.target.baseRef ||
		targetRuntime.binding.baseCommit != definition.workspace.target.baseCommit ||
		targetRuntime.binding.featureBranch != definition.workspace.target.featureBranch {
		return WorkspaceInitializationResult{}, fmt.Errorf(
			"initialized runtime does not match the verified local target",
		)
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

func resolveInitializationWorktreeRoot(
	configured string,
) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", fmt.Errorf(
			"workspace initialization requires an explicit worktree root",
		)
	}
	if !filepath.IsAbs(configured) {
		return "", fmt.Errorf("worktree root must be absolute")
	}
	return normalizeInitializationRootPath(
		RootRoleWorktree, configured,
	)
}

func inspectLocalTargetForInitializationAdmission(
	ctx context.Context,
	adapter LocalTargetGitAdapter,
	definition EffectiveWorkspaceDefinition,
	snapshot JournalSnapshot,
) (LocalTargetInspection, error) {
	if len(snapshot.records) == 0 {
		return adapter.inspectUncreatedTarget(ctx, definition.workspace.target)
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if runtime.workspaceID != definition.workspace.id {
		return LocalTargetInspection{}, fmt.Errorf(
			"workspace journal does not match local target admission workspace %s",
			definition.workspace.id,
		)
	}
	target, ok := runtime.LocalTarget()
	if !ok {
		return adapter.inspectUncreatedTarget(ctx, definition.workspace.target)
	}
	if target.binding.root != definition.workspace.target.root ||
		target.binding.baseRef != definition.workspace.target.baseRef ||
		target.binding.baseCommit != definition.workspace.target.baseCommit ||
		target.binding.featureBranch != definition.workspace.target.featureBranch {
		return LocalTargetInspection{}, fmt.Errorf(
			"durable local target binding does not match the active workspace definition",
		)
	}
	if target.Created() {
		return adapter.verifyOwnedFeatureRef(
			ctx, target.binding, target.intentDigest,
		)
	}
	return adapter.inspectIntendedTarget(
		ctx, target.binding, target.intentDigest,
	)
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
