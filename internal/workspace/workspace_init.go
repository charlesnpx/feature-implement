package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

const WorkspaceRuntimeProjectionFileName = "runtime-projection.json"

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
	workspaceDir, err := absoluteWorkspaceRuntimeDirectory(workspaceDir)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	checkpoint := VerifiedPlanLockCheckpoint{}
	checkpointID := Digest{}
	hasPlanCheckpoint := options.PlanCheckpoint != nil
	if hasPlanCheckpoint {
		checkpoint = *options.PlanCheckpoint
		if checkpoint.validate() != nil {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"workspace initialization requires a nonzero verified plan lock checkpoint",
			)
		}
		if checkpoint.Generation() != definition.generation {
			return WorkspaceInitializationResult{}, fmt.Errorf(
				"verified plan lock checkpoint generation %s does not match workspace generation %s",
				checkpoint.Generation(),
				definition.generation,
			)
		}
		checkpointID = checkpoint.CheckpointID()
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
	if err := verifyDerivedInitializationLayout(
		planRoot, workspaceDir, definition.workspace.target.root,
	); err != nil {
		return WorkspaceInitializationResult{}, err
	}
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
	runtimeInitialized, err := inspectRuntimeFormatForAdmission(workspaceDir)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if runtimeInitialized {
		preflightSnapshot, err = ReadWorkspaceJournalSnapshot(workspaceDir)
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
		preflightSnapshotKnown = true
	}
	targetBinding, err := inspectLocalTargetForInitializationAdmission(
		ctx,
		targetGit,
		definition,
		preflightSnapshot,
	)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
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
		}
	}
	stored, err := store.Store(definition)
	if err != nil {
		return WorkspaceInitializationResult{}, err
	}
	if needsInitialization {
		eventCheckpoint := []PlanCheckpointJournalBinding(nil)
		if hasPlanCheckpoint {
			eventCheckpoint = append(eventCheckpoint, PlanCheckpointJournalBinding{
				CheckpointID: checkpointID,
			})
		}
		event, err := NewWorkspaceInitializedJournalEventWithTarget(
			definition.workspace.id,
			definition.generation,
			stored.definitionDigest,
			targetBinding,
			eventCheckpoint...,
		)
		if err != nil {
			return WorkspaceInitializationResult{}, err
		}
		appendRequest, err := NewJournalAppend(
			event, occurredAt,
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
	if requiresCheckpoint && runtime.planCheckpoint != checkpointID {
		return WorkspaceInitializationResult{}, fmt.Errorf("initialized runtime does not match the verified plan checkpoint")
	}
	targetRuntime, ok := runtime.LocalTarget()
	if !ok ||
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

func absoluteWorkspaceRuntimeDirectory(value string) (string, error) {
	value = filepath.Clean(value)
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("workspace initialization requires an absolute runtime directory")
	}
	return canonicalizeTrustedRootPath(value)
}

func verifyDerivedInitializationLayout(planRoot, runtimeRoot, targetRoot string) error {
	runtimeRoot, err := canonicalizeTrustedRootPath(runtimeRoot)
	if err != nil {
		return err
	}
	if err := rejectAncestorWorkspaceBundleRoot(runtimeRoot); err != nil {
		return fmt.Errorf("reject derived runtime root: %w", err)
	}
	targetRoot, err = canonicalizeTrustedRootPath(targetRoot)
	if err != nil {
		return err
	}
	worktreeRoot, err := DerivedWorkspaceWorktreeRoot(runtimeRoot)
	if err != nil {
		return err
	}
	for _, protected := range []struct {
		name string
		path string
	}{
		{name: "target", path: targetRoot},
		{name: "plan", path: planRoot},
	} {
		if protected.path == "" {
			continue
		}
		protectedPath, pathErr := canonicalizeTrustedRootPath(protected.path)
		if pathErr != nil {
			return pathErr
		}
		if pathContains(protectedPath, runtimeRoot) || pathContains(runtimeRoot, protectedPath) ||
			pathContains(protectedPath, worktreeRoot) || pathContains(worktreeRoot, protectedPath) {
			return fmt.Errorf("derived runtime or worktree root overlaps the %s root", protected.name)
		}
	}
	return nil
}

func inspectLocalTargetForInitializationAdmission(
	ctx context.Context,
	adapter LocalTargetGitAdapter,
	definition EffectiveWorkspaceDefinition,
	snapshot JournalSnapshot,
) (LocalTargetBinding, error) {
	if len(snapshot.records) == 0 {
		return adapter.inspectUncreatedTarget(ctx, definition.workspace.target)
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if runtime.workspaceID != definition.workspace.id {
		return LocalTargetBinding{}, fmt.Errorf(
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
		return LocalTargetBinding{}, fmt.Errorf(
			"durable local target binding does not match the active workspace definition",
		)
	}
	pending, hasPending, err := pendingIntegrationIntent(runtime)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if hasPending {
		return adapter.verifyPendingIntegrationFeatureRef(
			ctx, target.binding, pending,
		)
	}
	if target.headRecord == target.createdRecord {
		return adapter.verifyFeatureRefAbsent(ctx, target.binding)
	}
	return adapter.verifyOwnedFeatureRefAt(
		ctx, target.binding, target.createdHead,
	)
}

func pendingIntegrationIntent(
	runtime WorkspaceRuntimeProjection,
) (MergeUnitIntegrationIntent, bool, error) {
	var pending MergeUnitIntegrationIntent
	hasPending := false
	for _, attempt := range runtime.Attempts() {
		integration, exists := attempt.Integration()
		if !exists || integration.Integrated() {
			continue
		}
		if hasPending {
			return MergeUnitIntegrationIntent{}, false, fmt.Errorf(
				"workspace runtime contains multiple pending integration intents",
			)
		}
		pending = integration.Intent()
		hasPending = true
	}
	return pending, hasPending, nil
}

// ValidateLocalTargetForWorkspaceRuntime selects the established admission
// path from the durable runtime when one exists. Before runtime initialization,
// it retains initial target admission.
func ValidateLocalTargetForWorkspaceRuntime(
	ctx context.Context,
	workspaceDir string,
	definition EffectiveWorkspaceDefinition,
) (LocalTargetBinding, error) {
	if ctx == nil {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target validation requires context",
		)
	}
	workspaceDir, err := absoluteWorkspaceRuntimeDirectory(workspaceDir)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	runtimeInitialized, err := inspectRuntimeFormatForAdmission(workspaceDir)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if !runtimeInitialized {
		return ValidateLocalTarget(ctx, definition.workspace)
	}
	snapshot, err := ReadWorkspaceJournalSnapshot(workspaceDir)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	return inspectLocalTargetForInitializationAdmission(
		ctx, DefaultLocalTargetGitAdapter(), definition, snapshot,
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
