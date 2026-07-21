package workspace

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type AttemptLifecycleFaultPoint string

const (
	AttemptFaultAfterReservation           AttemptLifecycleFaultPoint = "after_reservation"
	AttemptFaultAfterMaterializationIntent AttemptLifecycleFaultPoint = "after_materialization_intent"
	AttemptFaultAfterWorktreeCreation      AttemptLifecycleFaultPoint = "after_worktree_creation"
	AttemptFaultAfterGitVerification       AttemptLifecycleFaultPoint = "after_git_verification"
	AttemptFaultAfterStart                 AttemptLifecycleFaultPoint = "after_start"
	AttemptFaultAfterBoundary              AttemptLifecycleFaultPoint = "after_boundary"
	AttemptFaultAfterOrchestrationAck      AttemptLifecycleFaultPoint = "after_orchestration_ack"
	AttemptFaultAfterOwnerResponse         AttemptLifecycleFaultPoint = "after_owner_response"
	AttemptFaultAfterNextGoalIntent        AttemptLifecycleFaultPoint = "after_next_goal_intent"
	AttemptFaultBeforeLeaseBinding         AttemptLifecycleFaultPoint = "before_lease_binding"
	AttemptFaultAfterResume                AttemptLifecycleFaultPoint = "after_resume"
)

type AttemptLifecycleFaultInjector func(AttemptLifecycleFaultPoint) error

type ReserveAttemptRequest struct {
	MergeUnit     MergeUnitReference
	AttemptNumber uint64
	Base          GitObjectID
	WorktreeRoot  string
	Goal          GoalBinding
	OccurredAt    time.Time
	Fault         AttemptLifecycleFaultInjector
}

func ReserveAttempt(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	request ReserveAttemptRequest,
) (RuntimeAttemptProjection, error) {
	if journal == nil || git == nil || request.OccurredAt.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt reservation requires journal, Git adapter, and occurrence time")
	}
	if request.MergeUnit.planID.IsZero() || request.MergeUnit.mergeUnitID.IsZero() ||
		request.AttemptNumber == 0 || request.Base.IsZero() || request.Goal.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt reservation requires merge unit, attempt number, base, and goal")
	}
	manifest := definition.workspace
	if manifest.id.IsZero() || definition.generation.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt reservation requires an effective workspace definition")
	}
	unitExecution, err := executionForMergeUnit(definition.execution, request.MergeUnit)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	identity, err := DeriveAttemptIdentity(manifest.repository, request.MergeUnit, request.AttemptNumber, request.Base)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	worktree, err := AttemptWorktreePath(request.WorktreeRoot, identity, request.MergeUnit, request.AttemptNumber)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if existing, exists := runtime.Attempt(identity.attemptID); exists {
		if existing.mergeUnit != request.MergeUnit || existing.attemptNumber != request.AttemptNumber ||
			existing.base != request.Base || existing.worktree != worktree || existing.goal != request.Goal {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s was reserved with different immutable bindings", identity.attemptID)
		}
		return existing, nil
	}
	for _, attempt := range runtime.attempts {
		if attempt.mergeUnit == request.MergeUnit && attempt.attemptNumber == request.AttemptNumber {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt number %d is already bound to %s", request.AttemptNumber, attempt.attemptID)
		}
	}
	if err := git.ValidateAttemptBranch(ctx, manifest.repositoryRoot, identity.branch); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	inventory, err := git.InspectAttemptRefs(ctx, manifest.repositoryRoot, manifest.remote)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := CheckAttemptRefConflicts(identity.branch, inventory.local, inventory.remote, false); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	event, err := NewAttemptReservedJournalEvent(
		manifest.id, definition.generation, manifest.repository, identity.attemptID,
		request.MergeUnit, request.AttemptNumber, request.Base, identity.branch, worktree,
		unitExecution.boundary.mode, unitExecution.boundary.serialSegment, request.Goal,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterReservation); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	return loadRuntimeAttempt(journal, identity.attemptID)
}

type MaterializeAttemptRequest struct {
	AttemptID  ID
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

func MaterializeAttempt(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	request MaterializeAttemptRequest,
) (RuntimeAttemptProjection, error) {
	if journal == nil || git == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt materialization requires journal, Git adapter, attempt, and occurrence time")
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	attempt, exists := runtime.Attempt(request.AttemptID)
	if !exists {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s is not reserved", request.AttemptID)
	}
	if attempt.phase != AttemptReserved && attempt.phase != AttemptMaterializing {
		return attempt, nil
	}
	if attempt.phase == AttemptReserved {
		event, err := NewAttemptMaterializationIntendedJournalEvent(
			runtime.workspaceID, attempt.attemptID, attempt.generation,
			attempt.base, attempt.branch, attempt.worktree,
		)
		if err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterMaterializationIntent); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		snapshot, runtime, err = readAttemptRuntime(journal, definition)
		if err != nil {
			return RuntimeAttemptProjection{}, err
		}
		attempt, _ = runtime.Attempt(request.AttemptID)
	}
	manifest := definition.workspace
	claim, err := NewAttemptWorktreeClaim(
		attempt.attemptID, attempt.generation, attempt.base, attempt.branch, attempt.worktree,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := git.ValidateAttemptBranch(ctx, manifest.repositoryRoot, attempt.branch); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	inventory, err := git.InspectAttemptRefs(ctx, manifest.repositoryRoot, manifest.remote)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	localWithoutExact := make([]string, 0, len(inventory.local))
	for _, branch := range inventory.local {
		normalized, normalizeErr := normalizeHeadRef(branch)
		if normalizeErr != nil {
			return RuntimeAttemptProjection{}, normalizeErr
		}
		if normalized != attempt.branch {
			localWithoutExact = append(localWithoutExact, normalized)
		}
	}
	if err := CheckAttemptRefConflicts(attempt.branch, localWithoutExact, inventory.remote, false); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	inspection, err := git.InspectAttemptWorktree(ctx, manifest.repositoryRoot, attempt.branch, attempt.worktree)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if inspection.branchExists && inspection.branchHead != attempt.base {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt branch %s resolves to %s, expected %s", attempt.branch, inspection.branchHead, attempt.base)
	}
	if inspection.worktreeRegistered && !inspection.worktreeExists {
		if !inspection.branchExists || inspection.branchHead != attempt.base || inspection.worktreeBranch != attempt.branch {
			return RuntimeAttemptProjection{}, fmt.Errorf("interrupted worktree registration does not match the attempt intent")
		}
		if err := git.CreateAttemptWorktree(
			ctx, manifest.repositoryRoot, attempt.branch, attempt.worktree, attempt.base, false, true,
		); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterWorktreeCreation); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		inspection, err = git.InspectAttemptWorktree(ctx, manifest.repositoryRoot, attempt.branch, attempt.worktree)
		if err != nil {
			return RuntimeAttemptProjection{}, err
		}
	} else if !inspection.worktreeRegistered {
		if err := git.PrepareAttemptWorktree(ctx, claim, inspection.worktreeExists); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		inspection, err = git.InspectAttemptWorktree(ctx, manifest.repositoryRoot, attempt.branch, attempt.worktree)
		if err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if inspection.worktreeExists || inspection.worktreeRegistered {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt worktree recovery did not produce an absent unregistered target")
		}
		if err := git.CreateAttemptWorktree(
			ctx, manifest.repositoryRoot, attempt.branch, attempt.worktree, attempt.base, !inspection.branchExists, false,
		); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterWorktreeCreation); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		inspection, err = git.InspectAttemptWorktree(ctx, manifest.repositoryRoot, attempt.branch, attempt.worktree)
		if err != nil {
			return RuntimeAttemptProjection{}, err
		}
	}
	if err := verifyAttemptGitInspection(attempt, inspection, attempt.base); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterGitVerification); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	confirmed, err := git.InspectAttemptWorktree(ctx, manifest.repositoryRoot, attempt.branch, attempt.worktree)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := verifyAttemptGitInspection(attempt, confirmed, attempt.base); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if confirmed.digest != inspection.digest {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt Git state changed during start verification")
	}
	if err := git.ReleaseAttemptWorktreeClaim(ctx, claim); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	leaseID, authorizationID, err := deriveAttemptEpochBindings(attempt.attemptID, 1)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	event, err := NewAttemptStartedJournalEvent(
		runtime.workspaceID, attempt.attemptID, attempt.generation,
		confirmed.worktreeHead, confirmed.digest, leaseID, authorizationID, attempt.goal,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterStart); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	return loadRuntimeAttempt(journal, attempt.attemptID)
}

type RecordAttemptBoundaryRequest struct {
	AttemptID  ID
	Evidence   []Evidence
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

type CompleteGoalAndWaitDirective struct {
	workspaceID     ID
	generation      Digest
	attemptID       ID
	boundaryID      ID
	goal            GoalBinding
	head            GitObjectID
	directiveDigest Digest
	idempotencyKey  Digest
}

func (CompleteGoalAndWaitDirective) isAttemptBoundaryDirective()  {}
func (directive CompleteGoalAndWaitDirective) WorkspaceID() ID    { return directive.workspaceID }
func (directive CompleteGoalAndWaitDirective) Generation() Digest { return directive.generation }
func (directive CompleteGoalAndWaitDirective) AttemptID() ID      { return directive.attemptID }
func (directive CompleteGoalAndWaitDirective) BoundaryID() ID     { return directive.boundaryID }
func (directive CompleteGoalAndWaitDirective) Goal() GoalBinding  { return directive.goal }
func (directive CompleteGoalAndWaitDirective) Head() GitObjectID  { return directive.head }
func (directive CompleteGoalAndWaitDirective) DirectiveDigest() Digest {
	return directive.directiveDigest
}
func (directive CompleteGoalAndWaitDirective) IdempotencyKey() Digest {
	return directive.idempotencyKey
}

type AttemptBoundaryDirective interface {
	isAttemptBoundaryDirective()
}

type AttemptBoundaryResult struct {
	attempt    RuntimeAttemptProjection
	boundary   RuntimeBoundaryProjection
	directives []AttemptBoundaryDirective
}

func (result AttemptBoundaryResult) Attempt() RuntimeAttemptProjection {
	return cloneRuntimeAttempt(result.attempt)
}
func (result AttemptBoundaryResult) Boundary() RuntimeBoundaryProjection {
	return cloneRuntimeBoundary(result.boundary)
}
func (result AttemptBoundaryResult) Directives() []AttemptBoundaryDirective {
	return append([]AttemptBoundaryDirective(nil), result.directives...)
}

func RecordAttemptBoundary(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	git AttemptGitPort,
	request RecordAttemptBoundaryRequest,
) (AttemptBoundaryResult, error) {
	if journal == nil || git == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return AttemptBoundaryResult{}, fmt.Errorf("attempt boundary requires journal, Git adapter, attempt, and occurrence time")
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
		requestedDigest, digestErr := digestBoundaryEvidence(sortedEvidenceForProjection(request.Evidence))
		if digestErr != nil {
			return AttemptBoundaryResult{}, digestErr
		}
		if requestedDigest != boundary.evidenceDigest {
			return AttemptBoundaryResult{}, fmt.Errorf("attempt %s is already paused with different boundary evidence", attempt.attemptID)
		}
		return boundaryResult(runtime, attempt, boundary)
	}
	if attempt.phase != AttemptActive {
		return AttemptBoundaryResult{}, fmt.Errorf("attempt %s must be active to reach a boundary", attempt.attemptID)
	}
	inspection, err := git.InspectAttemptWorktree(
		ctx, definition.workspace.repositoryRoot, attempt.branch, attempt.worktree,
	)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	if err := verifyAttemptGitInspection(attempt, inspection, inspection.worktreeHead); err != nil {
		return AttemptBoundaryResult{}, err
	}
	event, err := NewAttemptBoundaryReachedJournalEvent(
		runtime.workspaceID, attempt.attemptID, attempt.generation,
		uint64(len(attempt.boundaries)+1), attempt.boundaryMode, attempt.serialSegment,
		attempt.leaseID, attempt.authorizationID, attempt.goal, inspection.worktreeHead,
		sortedEvidenceForProjection(request.Evidence),
	)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
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
	return boundaryResult(runtime, attempt, boundary)
}

func PendingAttemptBoundaryDirective(
	projection WorkspaceRuntimeProjection,
	attemptID ID,
) (CompleteGoalAndWaitDirective, bool) {
	attempt, exists := projection.Attempt(attemptID)
	if !exists {
		return CompleteGoalAndWaitDirective{}, false
	}
	boundary, exists := attempt.CurrentBoundary()
	if !exists || boundary.mode != AttemptBoundaryCompleteGoalAndWait || boundary.goalCompletedOK {
		return CompleteGoalAndWaitDirective{}, false
	}
	return completeGoalDirective(projection, attempt, boundary), true
}

func OwnerBoundaryResponseRequestDigest(
	projection WorkspaceRuntimeProjection,
	attemptID ID,
	response OwnerBoundaryResponse,
) (Digest, error) {
	attempt, boundary, err := currentAttemptBoundary(projection, attemptID)
	if err != nil {
		return Digest{}, err
	}
	return deriveOwnerResponseRequestDigest(projection.workspaceID, attempt.generation, attempt.attemptID, boundary, response)
}

type NextGoalCreationIntent struct {
	workspaceID     ID
	generation      Digest
	attemptID       ID
	boundaryID      ID
	completedGoal   GoalBinding
	nextGoal        GoalBinding
	head            GitObjectID
	directiveDigest Digest
	idempotencyKey  Digest
}

func (intent NextGoalCreationIntent) WorkspaceID() ID            { return intent.workspaceID }
func (intent NextGoalCreationIntent) Generation() Digest         { return intent.generation }
func (intent NextGoalCreationIntent) AttemptID() ID              { return intent.attemptID }
func (intent NextGoalCreationIntent) BoundaryID() ID             { return intent.boundaryID }
func (intent NextGoalCreationIntent) CompletedGoal() GoalBinding { return intent.completedGoal }
func (intent NextGoalCreationIntent) NextGoal() GoalBinding      { return intent.nextGoal }
func (intent NextGoalCreationIntent) Head() GitObjectID          { return intent.head }
func (intent NextGoalCreationIntent) DirectiveDigest() Digest    { return intent.directiveDigest }
func (intent NextGoalCreationIntent) IdempotencyKey() Digest     { return intent.idempotencyKey }

type ReserveNextGoalCreationRequest struct {
	AttemptID  ID
	Goal       GoalBinding
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

func ReserveNextGoalCreation(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request ReserveNextGoalCreationRequest,
) (NextGoalCreationIntent, error) {
	if journal == nil || request.AttemptID.IsZero() || request.Goal.IsZero() || request.OccurredAt.IsZero() {
		return NextGoalCreationIntent{}, fmt.Errorf("next-goal intent requires journal, attempt, goal, and occurrence time")
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return NextGoalCreationIntent{}, err
	}
	attempt, boundary, err := currentAttemptBoundary(runtime, request.AttemptID)
	if err != nil {
		return NextGoalCreationIntent{}, err
	}
	if boundary.mode != AttemptBoundaryCompleteGoalAndWait || !boundary.goalCompletedOK || !boundary.ownerResponseOK {
		return NextGoalCreationIntent{}, fmt.Errorf("next-goal intent requires completed-goal acknowledgement and verified owner response")
	}
	if boundary.nextGoalIntentOK {
		if boundary.nextGoalIntent.goal != request.Goal {
			return NextGoalCreationIntent{}, fmt.Errorf("next-goal intent conflicts with durable intent at record %d", boundary.nextGoalIntent.record)
		}
		return nextGoalCreationIntent(runtime, attempt, boundary), nil
	}
	if request.Goal == boundary.goal {
		return NextGoalCreationIntent{}, fmt.Errorf("next-goal intent cannot reuse the completed goal")
	}
	idempotencyKey, err := deriveNextGoalIdempotencyKey(
		runtime.workspaceID, attempt.generation, attempt.attemptID, boundary.boundaryID,
		boundary.directiveDigest, request.Goal,
	)
	if err != nil {
		return NextGoalCreationIntent{}, err
	}
	event, err := NewAttemptNextGoalIntendedJournalEvent(
		runtime.workspaceID, attempt.attemptID, boundary.boundaryID, attempt.generation,
		request.Goal, idempotencyKey,
	)
	if err != nil {
		return NextGoalCreationIntent{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
		return NextGoalCreationIntent{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterNextGoalIntent); err != nil {
		return NextGoalCreationIntent{}, err
	}
	updatedRuntime, updatedAttempt, updatedBoundary, err := loadAttemptBoundaryRuntime(journal, attempt.attemptID)
	if err != nil {
		return NextGoalCreationIntent{}, err
	}
	return nextGoalCreationIntent(updatedRuntime, updatedAttempt, updatedBoundary), nil
}

func PendingNextGoalCreationIntent(
	projection WorkspaceRuntimeProjection,
	attemptID ID,
) (NextGoalCreationIntent, bool) {
	attempt, exists := projection.Attempt(attemptID)
	if !exists {
		return NextGoalCreationIntent{}, false
	}
	boundary, exists := attempt.CurrentBoundary()
	if !exists || !boundary.nextGoalIntentOK || boundary.nextGoalOK {
		return NextGoalCreationIntent{}, false
	}
	return nextGoalCreationIntent(projection, attempt, boundary), true
}

type RecordOrchestrationAcknowledgementRequest struct {
	AttemptID             ID
	Kind                  OrchestrationAcknowledgementKind
	Goal                  GoalBinding
	AcknowledgementDigest Digest
	OccurredAt            time.Time
	Fault                 AttemptLifecycleFaultInjector
}

func RecordOrchestrationAcknowledgement(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordOrchestrationAcknowledgementRequest,
) (RuntimeOrchestrationAcknowledgement, error) {
	if journal == nil || request.AttemptID.IsZero() || !request.Kind.valid() ||
		request.Goal.IsZero() || request.AcknowledgementDigest.IsZero() || request.OccurredAt.IsZero() {
		return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf("orchestration acknowledgement requires attempt, kind, goal, digest, and occurrence time")
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	attempt, boundary, err := currentAttemptBoundary(runtime, request.AttemptID)
	if err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	if boundary.mode == AttemptBoundaryPauseOnly {
		return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf("pause-only boundary %s cannot acknowledge broader goal lifecycle", boundary.boundaryID)
	}
	if existing, ok := boundaryAcknowledgement(boundary, request.Kind); ok {
		if existing.goal != request.Goal || existing.acknowledgementDigest != request.AcknowledgementDigest {
			return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf("orchestration acknowledgement conflicts with durable acknowledgement at record %d", existing.record)
		}
		return existing, nil
	}
	var idempotencyKey Digest
	switch request.Kind {
	case AcknowledgementGoalCompleted:
		if request.Goal != boundary.goal {
			return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf("goal-completion acknowledgement must bind the boundary goal")
		}
		idempotencyKey = boundary.idempotencyKey
	case AcknowledgementNextGoalCreated:
		if !boundary.nextGoalIntentOK || request.Goal != boundary.nextGoalIntent.goal {
			return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf("next-goal acknowledgement requires the matching durable creation intent")
		}
		idempotencyKey = boundary.nextGoalIntent.idempotencyKey
	}
	event, err := NewAttemptOrchestrationAcknowledgedJournalEvent(
		runtime.workspaceID, attempt.attemptID, boundary.boundaryID, attempt.generation,
		request.Kind, request.Goal, idempotencyKey, request.AcknowledgementDigest,
	)
	if err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterOrchestrationAck); err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	_, updatedBoundary, err := loadCurrentAttemptBoundary(journal, attempt.attemptID)
	if err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	acknowledgement, _ := boundaryAcknowledgement(updatedBoundary, request.Kind)
	return acknowledgement, nil
}

type RecordOwnerBoundaryResponseRequest struct {
	AttemptID  ID
	Response   OwnerBoundaryResponse
	Receipt    Receipt
	OccurredAt time.Time
	Fault      AttemptLifecycleFaultInjector
}

func RecordOwnerBoundaryResponse(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	verifier ControlPlaneVerifierPort,
	request RecordOwnerBoundaryResponseRequest,
) (RuntimeOwnerBoundaryResponse, error) {
	if journal == nil || verifier == nil || request.AttemptID.IsZero() ||
		!request.Response.valid() || request.OccurredAt.IsZero() {
		return RuntimeOwnerBoundaryResponse{}, fmt.Errorf("owner response requires journal, verifier, attempt, response, and occurrence time")
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	attempt, boundary, err := currentAttemptBoundary(runtime, request.AttemptID)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	if boundary.ownerResponseOK {
		receiptDigest, digestErr := digestReceipt(request.Receipt)
		if digestErr != nil {
			return RuntimeOwnerBoundaryResponse{}, digestErr
		}
		if boundary.ownerResponse.response != request.Response || boundary.ownerResponse.receiptDigest != receiptDigest {
			return RuntimeOwnerBoundaryResponse{}, fmt.Errorf("owner response conflicts with durable response at record %d", boundary.ownerResponse.record)
		}
		return boundary.ownerResponse, nil
	}
	requestDigest, err := deriveOwnerResponseRequestDigest(
		runtime.workspaceID, attempt.generation, attempt.attemptID, boundary, request.Response,
	)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	if request.Receipt.payloadDigest != requestDigest {
		return RuntimeOwnerBoundaryResponse{}, fmt.Errorf("owner receipt payload does not match the boundary response request")
	}
	verification, err := NewReceiptVerification(runtime.workspaceID, attempt.generation, requestDigest)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	if err := verifier.Verify(ctx, verification, request.Receipt); err != nil {
		return RuntimeOwnerBoundaryResponse{}, fmt.Errorf("verify owner boundary response: %w", err)
	}
	receiptDigest, err := digestReceipt(request.Receipt)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	event, err := NewAttemptOwnerResponseJournalEvent(
		runtime.workspaceID, attempt.attemptID, boundary.boundaryID, attempt.generation,
		request.Response, requestDigest, receiptDigest,
	)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterOwnerResponse); err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	_, updatedBoundary, err := loadCurrentAttemptBoundary(journal, attempt.attemptID)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	return updatedBoundary.ownerResponse, nil
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
	if !boundary.ownerResponseOK {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s cannot resume before verified owner response", attempt.attemptID)
	}
	goal := boundary.goal
	if boundary.mode == AttemptBoundaryCompleteGoalAndWait {
		if !boundary.nextGoalOK {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s cannot resume before next-goal acknowledgement", attempt.attemptID)
		}
		goal = boundary.nextGoal.goal
	}
	inspection, err := git.InspectAttemptWorktree(
		ctx, definition.workspace.repositoryRoot, attempt.branch, attempt.worktree,
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
	leaseID, authorizationID, err := deriveAttemptEpochBindings(attempt.attemptID, epoch)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	event, err := NewAttemptResumedJournalEvent(
		runtime.workspaceID, attempt.attemptID, boundary.boundaryID, attempt.generation,
		inspection.worktreeHead, inspection.digest, leaseID, authorizationID, goal, attempt.serialSegment,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if _, err := appendAttemptLifecycleEvent(journal, snapshot, runtime, event, request.OccurredAt, true); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterResume); err != nil {
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
	return snapshot, runtime, nil
}

func appendAttemptLifecycleEvent(
	journal *WorkspaceJournal,
	snapshot JournalSnapshot,
	runtime WorkspaceRuntimeProjection,
	event WorkspaceJournalEvent,
	occurredAt time.Time,
	privileged bool,
) (JournalRecord, error) {
	reads, writes, ok := attemptJournalEventResources(event)
	if !ok {
		return JournalRecord{}, fmt.Errorf("unsupported attempt lifecycle event %T", event)
	}
	reads = normalizedAttemptEventResources(reads)
	writes = normalizedAttemptEventResources(writes)
	readSet := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(resource, snapshot.Revision(resource))
		readSet = append(readSet, revision)
	}
	var request JournalAppend
	var err error
	if privileged {
		request, err = newPrivilegedJournalAppend(event, occurredAt, readSet, writes)
	} else {
		request, err = NewJournalAppend(event, occurredAt, readSet, writes)
	}
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
	if !inspection.branchExists || !inspection.worktreeExists || !inspection.worktreeRegistered || !inspection.clean ||
		inspection.worktreeBranch != attempt.branch || inspection.branchHead != expectedHead || inspection.worktreeHead != expectedHead {
		return fmt.Errorf(
			"attempt Git verification failed for branch %s and worktree %s at %s",
			attempt.branch, attempt.worktree, expectedHead,
		)
	}
	return nil
}

func deriveAttemptEpochBindings(attemptID ID, epoch uint64) (ID, ID, error) {
	if attemptID.IsZero() || epoch == 0 {
		return ID{}, ID{}, fmt.Errorf("attempt epoch bindings require attempt and positive epoch")
	}
	bindings := fmt.Sprintf("attempt_epoch_v2\nattempt_id=%s\nepoch=%d\n", attemptID, epoch)
	digest := hex.EncodeToString(DigestBytes([]byte(bindings)).Bytes())[:16]
	leaseID, err := NewID("lease-" + digest)
	if err != nil {
		return ID{}, ID{}, err
	}
	authorizationID, err := NewID("authorization-" + digest)
	if err != nil {
		return ID{}, ID{}, err
	}
	return leaseID, authorizationID, nil
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

func currentAttemptBoundary(
	runtime WorkspaceRuntimeProjection,
	attemptID ID,
) (RuntimeAttemptProjection, RuntimeBoundaryProjection, error) {
	attempt, exists := runtime.Attempt(attemptID)
	if !exists {
		return RuntimeAttemptProjection{}, RuntimeBoundaryProjection{}, fmt.Errorf("attempt %s is not reserved", attemptID)
	}
	boundary, exists := attempt.CurrentBoundary()
	if !exists {
		return RuntimeAttemptProjection{}, RuntimeBoundaryProjection{}, fmt.Errorf("attempt %s has no current paused boundary", attemptID)
	}
	return attempt, boundary, nil
}

func loadCurrentAttemptBoundary(
	journal *WorkspaceJournal,
	attemptID ID,
) (RuntimeAttemptProjection, RuntimeBoundaryProjection, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return RuntimeAttemptProjection{}, RuntimeBoundaryProjection{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return RuntimeAttemptProjection{}, RuntimeBoundaryProjection{}, err
	}
	return currentAttemptBoundary(runtime, attemptID)
}

func loadAttemptBoundaryRuntime(
	journal *WorkspaceJournal,
	attemptID ID,
) (WorkspaceRuntimeProjection, RuntimeAttemptProjection, RuntimeBoundaryProjection, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return WorkspaceRuntimeProjection{}, RuntimeAttemptProjection{}, RuntimeBoundaryProjection{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return WorkspaceRuntimeProjection{}, RuntimeAttemptProjection{}, RuntimeBoundaryProjection{}, err
	}
	attempt, boundary, err := currentAttemptBoundary(runtime, attemptID)
	if err != nil {
		return WorkspaceRuntimeProjection{}, RuntimeAttemptProjection{}, RuntimeBoundaryProjection{}, err
	}
	return runtime, attempt, boundary, nil
}

func boundaryAcknowledgement(
	boundary RuntimeBoundaryProjection,
	kind OrchestrationAcknowledgementKind,
) (RuntimeOrchestrationAcknowledgement, bool) {
	if kind == AcknowledgementGoalCompleted {
		return boundary.goalCompleted, boundary.goalCompletedOK
	}
	return boundary.nextGoal, boundary.nextGoalOK
}

func boundaryResult(
	runtime WorkspaceRuntimeProjection,
	attempt RuntimeAttemptProjection,
	boundary RuntimeBoundaryProjection,
) (AttemptBoundaryResult, error) {
	result := AttemptBoundaryResult{attempt: cloneRuntimeAttempt(attempt), boundary: cloneRuntimeBoundary(boundary)}
	if boundary.mode == AttemptBoundaryCompleteGoalAndWait && !boundary.goalCompletedOK {
		result.directives = []AttemptBoundaryDirective{completeGoalDirective(runtime, attempt, boundary)}
	}
	return result, nil
}

func completeGoalDirective(
	runtime WorkspaceRuntimeProjection,
	attempt RuntimeAttemptProjection,
	boundary RuntimeBoundaryProjection,
) CompleteGoalAndWaitDirective {
	return CompleteGoalAndWaitDirective{
		workspaceID: runtime.workspaceID, generation: attempt.generation,
		attemptID: attempt.attemptID, boundaryID: boundary.boundaryID,
		goal: boundary.goal, head: boundary.head,
		directiveDigest: boundary.directiveDigest, idempotencyKey: boundary.idempotencyKey,
	}
}

func nextGoalCreationIntent(
	runtime WorkspaceRuntimeProjection,
	attempt RuntimeAttemptProjection,
	boundary RuntimeBoundaryProjection,
) NextGoalCreationIntent {
	return NextGoalCreationIntent{
		workspaceID: runtime.workspaceID, generation: attempt.generation,
		attemptID: attempt.attemptID, boundaryID: boundary.boundaryID,
		completedGoal: boundary.goal, nextGoal: boundary.nextGoalIntent.goal,
		head: boundary.head, directiveDigest: boundary.directiveDigest,
		idempotencyKey: boundary.nextGoalIntent.idempotencyKey,
	}
}

func digestReceipt(receipt Receipt) (Digest, error) {
	if receipt.keyID.IsZero() || receipt.payloadDigest.IsZero() || receipt.expiresAt.IsZero() || len(receipt.signature) == 0 {
		return Digest{}, fmt.Errorf("receipt is incomplete")
	}
	type receiptJSON struct {
		KeyID         string `json:"key_id"`
		PayloadDigest string `json:"payload_digest"`
		Nonce         string `json:"nonce"`
		ExpiresAt     string `json:"expires_at"`
		Signature     string `json:"signature"`
	}
	content, err := json.Marshal(receiptJSON{
		KeyID: receipt.keyID.String(), PayloadDigest: receipt.payloadDigest.String(),
		Nonce: receipt.nonce, ExpiresAt: receipt.expiresAt.UTC().Format(time.RFC3339Nano),
		Signature: base64.StdEncoding.EncodeToString(receipt.signature),
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
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
