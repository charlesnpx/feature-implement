package workspace

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"
)

type AttemptLifecycleFaultPoint string

const (
	AttemptFaultAfterReservation      AttemptLifecycleFaultPoint = "after_reservation"
	AttemptFaultAfterWorktreeCreation AttemptLifecycleFaultPoint = "after_worktree_creation"
	AttemptFaultAfterGitVerification  AttemptLifecycleFaultPoint = "after_git_verification"
	AttemptFaultAfterStart            AttemptLifecycleFaultPoint = "after_start"
	AttemptFaultAfterBoundary         AttemptLifecycleFaultPoint = "after_boundary"
	AttemptFaultBeforeLeaseBinding    AttemptLifecycleFaultPoint = "before_lease_binding"
	AttemptFaultAfterResume           AttemptLifecycleFaultPoint = "after_resume"
	AttemptFaultAfterAbandon          AttemptLifecycleFaultPoint = "after_abandon"
)

type AttemptLifecycleFaultInjector func(AttemptLifecycleFaultPoint) error

// StartAttemptRequest contains the immutable inputs for a detached attempt.
// Start appends those bindings before it touches the attempt directory, so a
// retry reconciles the same scratch directory instead of reserving another
// resource.
type StartAttemptRequest struct {
	MergeUnit     MergeUnitReference
	AttemptNumber uint64
	Goal          GoalBinding
	OccurredAt    time.Time
	Fault         AttemptLifecycleFaultInjector
}

func StartAttempt(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	request StartAttemptRequest,
) (RuntimeAttemptProjection, error) {
	if journal == nil || git == nil || request.OccurredAt.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt start requires journal, Git adapter, and occurrence time")
	}
	if request.MergeUnit.planID.IsZero() || request.MergeUnit.mergeUnitID.IsZero() ||
		request.AttemptNumber == 0 || request.Goal.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt start requires merge unit, attempt number, and goal")
	}
	manifest := definition.workspace
	if manifest.id.IsZero() || definition.generation.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt start requires an effective workspace definition")
	}
	unitExecution, err := executionForMergeUnit(definition.execution, request.MergeUnit)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if request.AttemptNumber > uint64(unitExecution.policy.maxAttempts) {
		return RuntimeAttemptProjection{}, fmt.Errorf(
			"attempt number %d exceeds max_attempts %d for merge unit %s",
			request.AttemptNumber, unitExecution.policy.maxAttempts, request.MergeUnit,
		)
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	target, ok := runtime.LocalTarget()
	if !ok || !target.Created() || target.CreatedHead().IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt start requires a durable local feature head")
	}
	base := target.CreatedHead()
	identity, err := DeriveAttemptIdentity(
		manifest.id, definition.generation, request.MergeUnit, request.AttemptNumber, base,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	worktree, err := AttemptWorktreePath(
		runtime.worktreeRoot.Path(), identity, request.MergeUnit, request.AttemptNumber,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := git.ValidateAttemptWorktreeRoot(ctx, manifest.target.root, worktree); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if existing, exists := runtime.Attempt(identity.attemptID); exists {
		if existing.mergeUnit != request.MergeUnit || existing.attemptNumber != request.AttemptNumber ||
			existing.base != base || existing.worktree != worktree || existing.goal != request.Goal {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s was started with different immutable bindings", identity.attemptID)
		}
		return reconcileStartedAttempt(ctx, journal, definition, git, existing, request.Fault)
	}
	for _, attempt := range runtime.attempts {
		if attempt.integration != nil && !attempt.integration.Integrated() {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt start conflicts with pending integration attempt %s", attempt.attemptID)
		}
		if attempt.mergeUnit == request.MergeUnit && attempt.attemptNumber == request.AttemptNumber {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt number %d is already bound to %s", request.AttemptNumber, attempt.attemptID)
		}
	}
	nextAttemptNumber := uint64(1)
	for _, attempt := range runtime.attempts {
		if attempt.mergeUnit == request.MergeUnit && attempt.attemptNumber >= nextAttemptNumber {
			nextAttemptNumber = attempt.attemptNumber + 1
		}
	}
	if request.AttemptNumber != nextAttemptNumber {
		return RuntimeAttemptProjection{}, fmt.Errorf(
			"attempt number %d is out of sequence for merge unit %s; next attempt is %d",
			request.AttemptNumber, request.MergeUnit, nextAttemptNumber,
		)
	}
	view, err := RebuildWorkspaceView(snapshot, definition)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	scheduler := view.Scheduler
	ready := false
	status := SchedulerUnitStatus("")
	var blockers []string
	for _, unit := range scheduler.Units {
		if unit.PlanID == request.MergeUnit.planID.String() && unit.MergeUnitID == request.MergeUnit.mergeUnitID.String() {
			status, blockers = unit.Status, append([]string(nil), unit.Blockers...)
			ready = unit.Status == SchedulerUnitReady
			break
		}
	}
	if !ready {
		return RuntimeAttemptProjection{}, fmt.Errorf(
			"merge unit %s is not scheduler-ready (status=%s blockers=%v)", request.MergeUnit, status, blockers,
		)
	}
	leaseID, err := deriveAttemptEpochBinding(identity.attemptID, 1)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	event, err := NewAttemptStartJournalEvent(
		manifest.id, identity.attemptID, definition.generation, request.MergeUnit,
		request.AttemptNumber, base, worktree, unitExecution.boundary.checkpoint,
		unitExecution.boundary.escalation, unitExecution.boundary.serialSegment,
		leaseID, request.Goal,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterReservation); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	started, err := loadRuntimeAttempt(journal, identity.attemptID)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	return reconcileStartedAttempt(ctx, journal, definition, git, started, request.Fault)
}

func reconcileStartedAttempt(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	attempt RuntimeAttemptProjection,
	fault AttemptLifecycleFaultInjector,
) (RuntimeAttemptProjection, error) {
	if attempt.phase != AttemptActive {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s is not a detached started attempt", attempt.attemptID)
	}
	inspection, err := git.MaterializeAttemptTree(
		ctx, definition.workspace.target.root, attempt.base, attempt.worktree,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(fault, AttemptFaultAfterWorktreeCreation); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := verifyAttemptGitInspection(attempt, inspection, attempt.base); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(fault, AttemptFaultAfterGitVerification); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	confirmed, err := git.InspectAttemptWorktree(
		ctx, definition.workspace.target.root, attempt.worktree,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := verifyAttemptGitInspection(attempt, confirmed, attempt.base); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if confirmed.digest != inspection.digest {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt Git state changed during start verification")
	}
	if err := injectAttemptLifecycleFault(fault, AttemptFaultAfterStart); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	return loadRuntimeAttempt(journal, attempt.attemptID)
}

type AdoptAttemptHeadRequest struct {
	AttemptID  ID
	OccurredAt time.Time
}

type AttemptHeadAdoptionResult struct {
	head    GitObjectID
	tree    GitObjectID
	record  JournalRecord
	adopted bool
}

func (result AttemptHeadAdoptionResult) Head() GitObjectID     { return result.head }
func (result AttemptHeadAdoptionResult) Tree() GitObjectID     { return result.tree }
func (result AttemptHeadAdoptionResult) Record() JournalRecord { return result.record }
func (result AttemptHeadAdoptionResult) Adopted() bool         { return result.adopted }

// AdoptAttemptHead records exact clean-head acceptance for an active attempt
// without a review loop. Configured commit constraints are proved from the
// final history immediately before this durable adoption.
func AdoptAttemptHead(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	request AdoptAttemptHeadRequest,
) (AttemptHeadAdoptionResult, error) {
	if journal == nil || repository == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return AttemptHeadAdoptionResult{}, fmt.Errorf("adopt attempt head requires journal, repository inspector, attempt, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return AttemptHeadAdoptionResult{}, err
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive {
		return AttemptHeadAdoptionResult{}, fmt.Errorf("attempt %s must be active for head adoption", request.AttemptID)
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return AttemptHeadAdoptionResult{}, err
	}
	if _, configured := unit.ReviewLoop(); configured {
		return AttemptHeadAdoptionResult{}, fmt.Errorf(
			"ordinary head adoption is unavailable for a configured review loop",
		)
	}
	if _, hasState := projection.State(request.AttemptID); hasState {
		return AttemptHeadAdoptionResult{}, fmt.Errorf("ordinary head adoption is unavailable after durable review state")
	}
	repositoryRequest, err := NewReviewRepositoryRequest(
		attempt.worktree, attempt.verifiedHead,
	)
	if err != nil {
		return AttemptHeadAdoptionResult{}, err
	}
	repositorySnapshot, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil {
		return AttemptHeadAdoptionResult{}, err
	}
	if !repositorySnapshot.clean {
		return AttemptHeadAdoptionResult{}, fmt.Errorf("head adoption requires a clean attempt worktree")
	}
	result := AttemptHeadAdoptionResult{head: repositorySnapshot.head, tree: repositorySnapshot.tree}
	if _, exists := exactAdoptedHeadRecord(
		snapshot, attempt.attemptID, repositorySnapshot,
	); exists {
		return result, nil
	}
	if err := verifyAttemptFinalHistory(
		ctx, repository, unit, attempt, repositorySnapshot.head,
	); err != nil {
		return AttemptHeadAdoptionResult{}, fmt.Errorf("adopt final history: %w", err)
	}
	adoption, err := NewReviewHeadAdoptedJournalEvent(
		definition.workspace.id, definition.generation, attempt.attemptID, attempt.mergeUnit,
		attempt.verifiedHead, repositorySnapshot.head, repositorySnapshot.tree, repositorySnapshot.digest,
	)
	if err != nil {
		return AttemptHeadAdoptionResult{}, err
	}
	record, err := appendReviewJournalEvent(journal, snapshot, adoption, request.OccurredAt)
	if err != nil {
		return AttemptHeadAdoptionResult{}, err
	}
	_, rebuilt, err := readReviewRuntime(journal, definition)
	if err != nil {
		return AttemptHeadAdoptionResult{}, err
	}
	updated, exists := rebuilt.core.Attempt(request.AttemptID)
	if !exists || updated.phase != AttemptActive || updated.verifiedHead != repositorySnapshot.head {
		return AttemptHeadAdoptionResult{}, fmt.Errorf("attempt head adoption did not rebuild to the inspected head")
	}
	result.record, result.adopted = record, true
	return result, nil
}

func exactAdoptedHeadRecord(
	snapshot JournalSnapshot,
	attemptID ID,
	repository ReviewRepositorySnapshot,
) (JournalRecord, bool) {
	for index := len(snapshot.records) - 1; index >= 0; index-- {
		record := snapshot.records[index]
		event, ok := record.event.(ReviewHeadAdoptedJournalEvent)
		if !ok || event.attemptID != attemptID {
			continue
		}
		if event.head == repository.head &&
			event.tree == repository.tree &&
			event.snapshotDigest == repository.digest {
			return record, true
		}
		return JournalRecord{}, false
	}
	return JournalRecord{}, false
}

type AttemptBoundaryKind string

const (
	AttemptBoundaryKindCheckpoint AttemptBoundaryKind = "checkpoint"
	AttemptBoundaryKindEscalation AttemptBoundaryKind = "escalation"
)

func (kind AttemptBoundaryKind) valid() bool {
	return kind == AttemptBoundaryKindCheckpoint || kind == AttemptBoundaryKindEscalation
}

type RecordAttemptBoundaryRequest struct {
	AttemptID  ID
	Kind       AttemptBoundaryKind
	Evidence   []Evidence
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

type AttemptBoundaryResult struct {
	attempt  RuntimeAttemptProjection
	boundary RuntimeBoundaryProjection
}

func (result AttemptBoundaryResult) Attempt() RuntimeAttemptProjection {
	return cloneRuntimeAttempt(result.attempt)
}
func (result AttemptBoundaryResult) Boundary() RuntimeBoundaryProjection {
	return cloneRuntimeBoundary(result.boundary)
}

func RecordAttemptBoundary(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	request RecordAttemptBoundaryRequest,
) (AttemptBoundaryResult, error) {
	if journal == nil || git == nil || request.AttemptID.IsZero() || !request.Kind.valid() || request.OccurredAt.IsZero() {
		return AttemptBoundaryResult{}, fmt.Errorf("attempt boundary requires journal, Git adapter, attempt, kind, and occurrence time")
	}
	if len(request.Evidence) == 0 {
		return AttemptBoundaryResult{}, fmt.Errorf("attempt boundary requires typed evidence")
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	attempt, exists := runtime.Attempt(request.AttemptID)
	if !exists {
		return AttemptBoundaryResult{}, fmt.Errorf("attempt %s is not reserved", request.AttemptID)
	}
	if attempt.phase == AttemptPaused {
		boundary, ok := attempt.CurrentBoundary()
		if !ok {
			return AttemptBoundaryResult{}, fmt.Errorf("attempt %s has inconsistent paused state", attempt.attemptID)
		}
		if request.Kind != boundary.kind {
			return AttemptBoundaryResult{}, fmt.Errorf(
				"attempt %s is already paused with different boundary kind: requested %q, recorded %q",
				attempt.attemptID, request.Kind, boundary.kind,
			)
		}
		requestedDigest, digestErr := digestBoundaryEvidence(sortedEvidenceForProjection(request.Evidence))
		if digestErr != nil {
			return AttemptBoundaryResult{}, digestErr
		}
		if requestedDigest != boundary.evidenceDigest {
			return AttemptBoundaryResult{}, fmt.Errorf("attempt %s is already paused with different boundary evidence", attempt.attemptID)
		}
		return boundaryResult(attempt, boundary)
	}
	if attempt.phase != AttemptActive {
		return AttemptBoundaryResult{}, fmt.Errorf("attempt %s must be active to reach a boundary", attempt.attemptID)
	}
	resolvedCheckpoint := attempt.checkpoint
	switch request.Kind {
	case AttemptBoundaryKindCheckpoint:
		if attempt.checkpoint == AttemptCheckpointNone {
			return AttemptBoundaryResult{}, fmt.Errorf(
				"merge unit %s rejects checkpoint boundary: configured checkpoint policy %q",
				attempt.mergeUnit, attempt.checkpoint,
			)
		}
	case AttemptBoundaryKindEscalation:
		if attempt.escalation == AttemptEscalationForbidden {
			return AttemptBoundaryResult{}, fmt.Errorf(
				"merge unit %s rejects escalation boundary: configured escalation policy %q",
				attempt.mergeUnit, attempt.escalation,
			)
		}
		resolvedCheckpoint = AttemptCheckpointPauseOnly
	}
	unitExecution, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	if configuredLoop, required := unitExecution.ReviewLoop(); required {
		reviewRuntime, rebuildErr := RebuildReviewRuntime(snapshot, definition)
		if rebuildErr != nil {
			return AttemptBoundaryResult{}, rebuildErr
		}
		reviewState, reviewed := reviewRuntime.State(attempt.attemptID)
		if !reviewed || reviewState.loop.digest != configuredLoop.digest {
			return AttemptBoundaryResult{}, fmt.Errorf(
				"attempt %s cannot reach a boundary before its configured review loop starts",
				attempt.attemptID,
			)
		}
		if reviewErr := validateAttemptReviewProtocolState(
			definition, unitExecution, attempt, reviewState, true, false,
		); reviewErr != nil {
			return AttemptBoundaryResult{}, reviewErr
		}
		if exhaustion, exhausted := reviewState.Exhaustion(); exhausted {
			return AttemptBoundaryResult{}, fmt.Errorf(
				"attempt %s cannot reach a boundary because its configured review loop is exhausted (%s)",
				attempt.attemptID, exhaustion.reason,
			)
		}
		if !reviewState.MergeReady() || reviewState.head != attempt.verifiedHead {
			return AttemptBoundaryResult{}, fmt.Errorf(
				"attempt %s cannot reach a boundary before every configured review profile confirms its exact head and tree",
				attempt.attemptID,
			)
		}
	}
	inspection, err := git.InspectAttemptWorktree(
		ctx, definition.workspace.target.root, attempt.worktree,
	)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	if err := verifyAttemptGitInspection(attempt, inspection, inspection.worktreeHead); err != nil {
		return AttemptBoundaryResult{}, err
	}
	event, err := NewAttemptBoundaryReachedJournalEvent(
		runtime.workspaceID, attempt.attemptID, attempt.generation,
		uint64(len(attempt.boundaries)+1), request.Kind, resolvedCheckpoint, attempt.serialSegment,
		attempt.leaseID, attempt.goal, inspection.worktreeHead,
		sortedEvidenceForProjection(request.Evidence),
	)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt); err != nil {
		return AttemptBoundaryResult{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterBoundary); err != nil {
		return AttemptBoundaryResult{}, err
	}
	attempt, err = loadRuntimeAttempt(journal, attempt.attemptID)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	boundary, _ := attempt.CurrentBoundary()
	return boundaryResult(attempt, boundary)
}

// PauseAttempt records an intentional checkpoint or an agent-raised
// exception stop. It deliberately has no directive or acknowledgement phase:
// resume is the next lifecycle operation once the worktree is ready again.
type PauseAttemptRequest struct {
	AttemptID  ID
	Kind       AttemptBoundaryKind
	Evidence   []Evidence
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

func PauseAttempt(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	request PauseAttemptRequest,
) (RuntimeAttemptProjection, error) {
	result, err := RecordAttemptBoundary(ctx, journal, definition, git, RecordAttemptBoundaryRequest{
		AttemptID: request.AttemptID, Kind: request.Kind, Evidence: request.Evidence,
		OccurredAt: request.OccurredAt, Fault: request.Fault,
	})
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	return result.Attempt(), nil
}

type ResumeAttemptRequest struct {
	AttemptID  ID
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

func ResumeAttempt(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	request ResumeAttemptRequest,
) (RuntimeAttemptProjection, error) {
	if journal == nil || git == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt resume requires journal, Git adapter, attempt, and occurrence time")
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	attempt, exists := runtime.Attempt(request.AttemptID)
	if !exists {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s is not reserved", request.AttemptID)
	}
	if attempt.phase == AttemptActive && len(attempt.boundaries) > 0 && attempt.boundaries[len(attempt.boundaries)-1].resumedRecord != 0 {
		return attempt, nil
	}
	boundary, ok := attempt.CurrentBoundary()
	if !ok {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s has no current boundary to resume", attempt.attemptID)
	}
	goal := boundary.goal
	inspection, err := git.InspectAttemptWorktree(
		ctx, definition.workspace.target.root, attempt.worktree,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := verifyAttemptGitInspection(attempt, inspection, boundary.head); err != nil {
		return RuntimeAttemptProjection{}, fmt.Errorf("verify paused attempt before resume: %w", err)
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultBeforeLeaseBinding); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	epoch := uint64(len(attempt.boundaries) + 1)
	leaseID, err := deriveAttemptEpochBinding(attempt.attemptID, epoch)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	event, err := NewAttemptResumedJournalEvent(
		runtime.workspaceID, attempt.attemptID, boundary.boundaryID, attempt.generation,
		inspection.worktreeHead, inspection.digest, leaseID, goal, attempt.serialSegment,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterResume); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	return loadRuntimeAttempt(journal, attempt.attemptID)
}

type AbandonAttemptRequest struct {
	AttemptID  ID
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

// AbandonAttempt is the terminal local exit. It releases only durable runtime
// ownership; the scratch directory is intentionally left intact for human
// inspection and is never treated as a registered target worktree.
func AbandonAttempt(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request AbandonAttemptRequest,
) (RuntimeAttemptProjection, error) {
	if journal == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt abandonment requires journal, attempt, and occurrence time")
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	attempt, exists := runtime.Attempt(request.AttemptID)
	if !exists {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s is not started", request.AttemptID)
	}
	if attempt.phase == AttemptAbandoned {
		return attempt, nil
	}
	event, err := NewAttemptAbandonedJournalEvent(runtime.workspaceID, attempt.attemptID, attempt.generation)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterAbandon); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	return loadRuntimeAttempt(journal, attempt.attemptID)
}

func readAttemptRuntime(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (JournalSnapshot, WorkspaceRuntimeProjection, error) {
	if journal == nil || definition.workspace.id.IsZero() || definition.generation.IsZero() {
		return JournalSnapshot{}, WorkspaceRuntimeProjection{}, fmt.Errorf("attempt runtime requires journal and effective definition")
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalSnapshot{}, WorkspaceRuntimeProjection{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalSnapshot{}, WorkspaceRuntimeProjection{}, err
	}
	if runtime.workspaceID != definition.workspace.id || runtime.activeGeneration != definition.generation {
		return JournalSnapshot{}, WorkspaceRuntimeProjection{}, fmt.Errorf("attempt definition does not match the active workspace generation")
	}
	if err := requireReadyLocalTarget(runtime); err != nil {
		return JournalSnapshot{}, WorkspaceRuntimeProjection{}, err
	}
	if err := verifyWorkspaceWorktreeRootBinding(runtime.worktreeRoot); err != nil {
		return JournalSnapshot{}, WorkspaceRuntimeProjection{}, err
	}
	return snapshot, runtime, nil
}

func appendAttemptLifecycleEvent(
	journal *WorkspaceJournal,
	snapshot JournalSnapshot,
	runtime WorkspaceRuntimeProjection,
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalRecord, error) {
	request, err := newWorkflowJournalAppend(event, occurredAt)
	if err != nil {
		return JournalRecord{}, err
	}
	prospective, err := buildJournalRecord(snapshot, request)
	if err != nil {
		return JournalRecord{}, err
	}
	if _, err := reduceWorkspaceRuntime(runtime, prospective); err != nil {
		return JournalRecord{}, fmt.Errorf("validate attempt transition: %w", err)
	}
	return journal.AppendIfHead(request, snapshot.head)
}

func executionForMergeUnit(config ExecutionConfig, reference MergeUnitReference) (UnitExecution, error) {
	for _, unit := range config.mergeUnits {
		if unit.planID == reference.planID && unit.mergeUnitID == reference.mergeUnitID {
			return unit, nil
		}
	}
	return UnitExecution{}, fmt.Errorf("execution config does not contain merge unit %s", reference)
}

func verifyAttemptGitInspection(
	attempt RuntimeAttemptProjection,
	inspection AttemptGitInspection,
	expectedHead GitObjectID,
) error {
	if !inspection.worktreeExists || !inspection.clean || inspection.worktreeHead != expectedHead {
		return fmt.Errorf(
			"attempt Git verification failed for detached scratch worktree %s at %s",
			attempt.worktree, expectedHead,
		)
	}
	return nil
}

func deriveAttemptEpochBinding(attemptID ID, epoch uint64) (ID, error) {
	if attemptID.IsZero() || epoch == 0 {
		return ID{}, fmt.Errorf("attempt epoch binding requires attempt and positive epoch")
	}
	bindings := fmt.Sprintf("attempt_epoch_v2\nattempt_id=%s\nepoch=%d\n", attemptID, epoch)
	digest := hex.EncodeToString(DigestBytes([]byte(bindings)).Bytes())[:16]
	leaseID, err := NewID("lease-" + digest)
	if err != nil {
		return ID{}, err
	}
	return leaseID, nil
}

func loadRuntimeAttempt(journal *WorkspaceJournal, attemptID ID) (RuntimeAttemptProjection, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	attempt, exists := runtime.Attempt(attemptID)
	if !exists {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s is missing after durable transition", attemptID)
	}
	return attempt, nil
}

func boundaryResult(
	attempt RuntimeAttemptProjection,
	boundary RuntimeBoundaryProjection,
) (AttemptBoundaryResult, error) {
	return AttemptBoundaryResult{
		attempt:  cloneRuntimeAttempt(attempt),
		boundary: cloneRuntimeBoundary(boundary),
	}, nil
}

func injectAttemptLifecycleFault(
	injector AttemptLifecycleFaultInjector,
	point AttemptLifecycleFaultPoint,
) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf("attempt lifecycle fault at %s: %w", point, err)
	}
	return nil
}
