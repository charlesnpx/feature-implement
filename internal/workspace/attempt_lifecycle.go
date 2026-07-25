package workspace

import (
	"context"
	"encoding/hex"
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
		request.AttemptNumber == 0 || request.Goal.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf(
			"attempt reservation requires merge unit, attempt number, and goal",
		)
	}
	manifest := definition.workspace
	if manifest.id.IsZero() || definition.generation.IsZero() {
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt reservation requires an effective workspace definition")
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
		return RuntimeAttemptProjection{}, fmt.Errorf(
			"attempt reservation requires a durable local feature head",
		)
	}
	base := target.CreatedHead()
	identity, err := DeriveAttemptIdentity(
		manifest.id, definition.generation,
		request.MergeUnit, request.AttemptNumber, base,
	)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	worktree, err := AttemptWorktreePath(
		runtime.worktreeRoot.Path(), identity,
		request.MergeUnit, request.AttemptNumber,
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
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s was reserved with different immutable bindings", identity.attemptID)
		}
		return existing, nil
	}
	for _, attempt := range runtime.attempts {
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
	scheduler, err := RebuildSchedulerView(snapshot, definition)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
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
	if err := git.ValidateAttemptBranch(ctx, manifest.target.root, identity.branch); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	inventory, err := git.InspectAttemptRefs(ctx, manifest.target.root)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	if err := CheckAttemptRefConflicts(identity.branch, inventory.local, false); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	event, err := NewAttemptReservedJournalEvent(
		manifest.id, definition.generation, identity.attemptID,
		request.MergeUnit, request.AttemptNumber, base, identity.branch, worktree,
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
	if err := git.ValidateAttemptBranch(ctx, manifest.target.root, attempt.branch); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	inventory, err := git.InspectAttemptRefs(ctx, manifest.target.root)
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
	if err := CheckAttemptRefConflicts(attempt.branch, localWithoutExact, false); err != nil {
		return RuntimeAttemptProjection{}, err
	}
	inspection, err := git.InspectAttemptWorktree(ctx, manifest.target.root, attempt.branch, attempt.worktree)
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
			ctx, manifest.target.root, claim, false, true,
		); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterWorktreeCreation); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		inspection, err = git.InspectAttemptWorktree(ctx, manifest.target.root, attempt.branch, attempt.worktree)
		if err != nil {
			return RuntimeAttemptProjection{}, err
		}
	} else if !inspection.worktreeRegistered {
		if err := git.PrepareAttemptWorktree(
			ctx, manifest.target.root, claim, inspection.worktreeExists,
		); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		inspection, err = git.InspectAttemptWorktree(ctx, manifest.target.root, attempt.branch, attempt.worktree)
		if err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if inspection.worktreeExists || inspection.worktreeRegistered {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt worktree recovery did not produce an absent unregistered target")
		}
		if err := git.CreateAttemptWorktree(
			ctx, manifest.target.root, claim, !inspection.branchExists, false,
		); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		if err := injectAttemptLifecycleFault(request.Fault, AttemptFaultAfterWorktreeCreation); err != nil {
			return RuntimeAttemptProjection{}, err
		}
		inspection, err = git.InspectAttemptWorktree(ctx, manifest.target.root, attempt.branch, attempt.worktree)
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
	confirmed, err := git.InspectAttemptWorktree(ctx, manifest.target.root, attempt.branch, attempt.worktree)
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
	leaseID, err := deriveAttemptEpochBinding(attempt.attemptID, 1)
	if err != nil {
		return RuntimeAttemptProjection{}, err
	}
	event, err := NewAttemptStartedJournalEvent(
		runtime.workspaceID, attempt.attemptID, attempt.generation,
		confirmed.worktreeHead, confirmed.digest, leaseID, attempt.goal,
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

// OwnerGateDirective is a non-executable request for one closed owner choice.
// It deliberately exposes no argv, command, callback, or implementing-agent
// identity field.
type OwnerGateDirective struct {
	workspaceID     ID
	generation      Digest
	attemptID       ID
	boundaryID      ID
	goal            GoalBinding
	head            GitObjectID
	directiveDigest Digest
}

func (OwnerGateDirective) isAttemptBoundaryDirective()       {}
func (directive OwnerGateDirective) WorkspaceID() ID         { return directive.workspaceID }
func (directive OwnerGateDirective) Generation() Digest      { return directive.generation }
func (directive OwnerGateDirective) AttemptID() ID           { return directive.attemptID }
func (directive OwnerGateDirective) BoundaryID() ID          { return directive.boundaryID }
func (directive OwnerGateDirective) Goal() GoalBinding       { return directive.goal }
func (directive OwnerGateDirective) Head() GitObjectID       { return directive.head }
func (directive OwnerGateDirective) DirectiveDigest() Digest { return directive.directiveDigest }
func (OwnerGateDirective) Choices() []OwnerBoundaryResponse {
	return []OwnerBoundaryResponse{OwnerBoundaryContinue}
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
	var configuredProtocolHead GitObjectID
	if configured, required := unitExecution.CommitProtocol(); required {
		if attempt.commitProtocol == nil || attempt.commitProtocol.protocol.digest != configured.digest ||
			attempt.commitProtocol.phase != CommitProtocolComplete {
			return AttemptBoundaryResult{}, fmt.Errorf("attempt %s cannot reach a boundary before its configured commit protocol completes", attempt.attemptID)
		}
		configuredProtocolHead = attempt.commitProtocol.Head()
	}
	expectedCommittedHead := configuredProtocolHead
	if reviewProtocol, configured := unitExecution.ReviewFixProtocol(); configured {
		if attempt.reviewFixes != nil {
			state := attempt.reviewFixes
			if err := validateReviewFixState(*state); err != nil {
				return AttemptBoundaryResult{}, fmt.Errorf("attempt %s review-fix state: %w", attempt.attemptID, err)
			}
			if state.generation != definition.generation || state.protocol.digest != reviewProtocol.digest ||
				state.maximum != unitExecution.policy.maxReviewFixes {
				return AttemptBoundaryResult{}, fmt.Errorf("attempt %s review-fix state does not match effective policy", attempt.attemptID)
			}
			if !state.Quiescent() {
				return AttemptBoundaryResult{}, fmt.Errorf("attempt %s cannot reach a boundary with an in-flight review fix", attempt.attemptID)
			}
			if !configuredProtocolHead.IsZero() && state.base != configuredProtocolHead {
				return AttemptBoundaryResult{}, fmt.Errorf("attempt %s review-fix chain does not follow its implementation protocol", attempt.attemptID)
			}
			expectedCommittedHead = state.Head()
		}
	} else if attempt.reviewFixes != nil {
		return AttemptBoundaryResult{}, fmt.Errorf("attempt %s has review-fix state without an effective protocol", attempt.attemptID)
	}
	if !expectedCommittedHead.IsZero() && attempt.verifiedHead != expectedCommittedHead {
		return AttemptBoundaryResult{}, fmt.Errorf(
			"attempt %s completed implementation-plus-review-fix head does not match its verified head",
			attempt.attemptID,
		)
	}
	inspection, err := git.InspectAttemptWorktree(
		ctx, definition.workspace.target.root, attempt.branch, attempt.worktree,
	)
	if err != nil {
		return AttemptBoundaryResult{}, err
	}
	expectedHead := inspection.worktreeHead
	if !expectedCommittedHead.IsZero() {
		expectedHead = expectedCommittedHead
	}
	if err := verifyAttemptGitInspection(attempt, inspection, expectedHead); err != nil {
		return AttemptBoundaryResult{}, err
	}
	event, err := NewAttemptBoundaryReachedJournalEvent(
		runtime.workspaceID, attempt.attemptID, attempt.generation,
		uint64(len(attempt.boundaries)+1), attempt.boundaryMode, attempt.serialSegment,
		attempt.leaseID, attempt.goal, inspection.worktreeHead,
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

func PendingOwnerGateDirective(
	projection WorkspaceRuntimeProjection,
	attemptID ID,
) (OwnerGateDirective, bool) {
	attempt, exists := projection.Attempt(attemptID)
	if !exists {
		return OwnerGateDirective{}, false
	}
	boundary, exists := attempt.CurrentBoundary()
	if !exists || boundary.ownerResponseOK ||
		boundary.mode == AttemptBoundaryCompleteGoalAndWait && !boundary.goalCompletedOK {
		return OwnerGateDirective{}, false
	}
	return ownerGateDirective(projection, attempt, boundary), true
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

func OrchestrationAcknowledgementRequestDigest(
	projection WorkspaceRuntimeProjection,
	attemptID ID,
	kind OrchestrationAcknowledgementKind,
	goal GoalBinding,
) (Digest, error) {
	attempt, boundary, err := currentAttemptBoundary(projection, attemptID)
	if err != nil {
		return Digest{}, err
	}
	idempotencyKey, err := orchestrationAcknowledgementIdempotencyKey(boundary, kind, goal)
	if err != nil {
		return Digest{}, err
	}
	return deriveOrchestrationAcknowledgementRequestDigest(
		projection.workspaceID, attempt.generation, attempt.attemptID,
		boundary, kind, goal, idempotencyKey,
	)
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
		return NextGoalCreationIntent{}, fmt.Errorf("next-goal intent requires completed-goal acknowledgement and the exact owner response")
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
	AttemptID       ID
	Kind            OrchestrationAcknowledgementKind
	DirectiveDigest Digest
	Goal            GoalBinding
	IdempotencyKey  Digest
	OccurredAt      time.Time
	Fault           AttemptLifecycleFaultInjector
}

func RecordOrchestrationAcknowledgement(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordOrchestrationAcknowledgementRequest,
) (RuntimeOrchestrationAcknowledgement, error) {
	if journal == nil || request.AttemptID.IsZero() ||
		!request.Kind.valid() || request.DirectiveDigest.IsZero() ||
		request.Goal.IsZero() || request.IdempotencyKey.IsZero() ||
		request.OccurredAt.IsZero() {
		return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf(
			"acknowledgement requires journal, attempt, kind, directive, goal, idempotency, and occurrence time",
		)
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
	if request.DirectiveDigest != boundary.directiveDigest {
		return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf(
			"acknowledgement directive does not match boundary %s",
			boundary.boundaryID,
		)
	}
	idempotencyKey, err := orchestrationAcknowledgementIdempotencyKey(boundary, request.Kind, request.Goal)
	if err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	if request.IdempotencyKey != idempotencyKey {
		return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf(
			"acknowledgement idempotency key does not match the exact directive and goal",
		)
	}
	requestDigest, err := deriveOrchestrationAcknowledgementRequestDigest(
		runtime.workspaceID, attempt.generation, attempt.attemptID,
		boundary, request.Kind, request.Goal, idempotencyKey,
	)
	if err != nil {
		return RuntimeOrchestrationAcknowledgement{}, err
	}
	if existing, ok := boundaryAcknowledgement(boundary, request.Kind); ok {
		if existing.goal != request.Goal ||
			existing.idempotencyKey != request.IdempotencyKey ||
			existing.requestDigest != requestDigest {
			return RuntimeOrchestrationAcknowledgement{}, fmt.Errorf("orchestration acknowledgement conflicts with durable acknowledgement at record %d", existing.record)
		}
		return existing, nil
	}
	event, err := NewAttemptOrchestrationAcknowledgedJournalEvent(
		runtime.workspaceID, attempt.attemptID, boundary.boundaryID, attempt.generation,
		request.Kind, request.DirectiveDigest, request.Goal,
		request.IdempotencyKey, requestDigest,
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
	AttemptID       ID
	BoundaryID      ID
	DirectiveDigest Digest
	Goal            GoalBinding
	ExpectedHead    GitObjectID
	Response        OwnerBoundaryResponse
	OccurredAt      time.Time
	Fault           AttemptLifecycleFaultInjector
}

func RecordOwnerBoundaryResponse(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordOwnerBoundaryResponseRequest,
) (RuntimeOwnerBoundaryResponse, error) {
	if journal == nil || request.AttemptID.IsZero() ||
		request.BoundaryID.IsZero() || request.DirectiveDigest.IsZero() ||
		request.Goal.IsZero() || request.ExpectedHead.IsZero() ||
		!request.Response.valid() || request.OccurredAt.IsZero() {
		return RuntimeOwnerBoundaryResponse{}, fmt.Errorf(
			"owner response requires journal, attempt, boundary, directive, goal, expected head, response, and occurrence time",
		)
	}
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	attempt, boundary, err := currentAttemptBoundary(runtime, request.AttemptID)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	if request.BoundaryID != boundary.boundaryID ||
		request.DirectiveDigest != boundary.directiveDigest ||
		request.Goal != boundary.goal ||
		request.ExpectedHead != boundary.head {
		return RuntimeOwnerBoundaryResponse{}, fmt.Errorf(
			"owner response does not match the exact current boundary, directive, goal, and head",
		)
	}
	requestDigest, err := deriveOwnerResponseRequestDigest(
		runtime.workspaceID, attempt.generation, attempt.attemptID,
		boundary, request.Response,
	)
	if err != nil {
		return RuntimeOwnerBoundaryResponse{}, err
	}
	if boundary.ownerResponseOK {
		if boundary.ownerResponse.response != request.Response ||
			boundary.ownerResponse.requestDigest != requestDigest {
			return RuntimeOwnerBoundaryResponse{}, fmt.Errorf("owner response conflicts with durable response at record %d", boundary.ownerResponse.record)
		}
		return boundary.ownerResponse, nil
	}
	event, err := NewAttemptOwnerResponseJournalEvent(
		runtime.workspaceID, attempt.attemptID, boundary.boundaryID, attempt.generation,
		request.DirectiveDigest, request.Goal, request.ExpectedHead,
		request.Response, requestDigest,
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
		return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s cannot resume before the exact owner response", attempt.attemptID)
	}
	goal := boundary.goal
	if boundary.mode == AttemptBoundaryCompleteGoalAndWait {
		if !boundary.nextGoalOK {
			return RuntimeAttemptProjection{}, fmt.Errorf("attempt %s cannot resume before next-goal acknowledgement", attempt.attemptID)
		}
		goal = boundary.nextGoal.goal
	}
	inspection, err := git.InspectAttemptWorktree(
		ctx, definition.workspace.target.root, attempt.branch, attempt.worktree,
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
	if boundary.mode == AttemptBoundaryPauseOnly && !boundary.ownerResponseOK {
		result.directives = []AttemptBoundaryDirective{ownerGateDirective(runtime, attempt, boundary)}
	} else if boundary.mode == AttemptBoundaryCompleteGoalAndWait && !boundary.goalCompletedOK {
		result.directives = []AttemptBoundaryDirective{completeGoalDirective(runtime, attempt, boundary)}
	}
	return result, nil
}

func ownerGateDirective(
	runtime WorkspaceRuntimeProjection,
	attempt RuntimeAttemptProjection,
	boundary RuntimeBoundaryProjection,
) OwnerGateDirective {
	return OwnerGateDirective{
		workspaceID: runtime.workspaceID, generation: attempt.generation,
		attemptID: attempt.attemptID, boundaryID: boundary.boundaryID,
		goal: boundary.goal, head: boundary.head, directiveDigest: boundary.directiveDigest,
	}
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

func orchestrationAcknowledgementIdempotencyKey(
	boundary RuntimeBoundaryProjection,
	kind OrchestrationAcknowledgementKind,
	goal GoalBinding,
) (Digest, error) {
	switch kind {
	case AcknowledgementGoalCompleted:
		if goal != boundary.goal {
			return Digest{}, fmt.Errorf("goal-completion acknowledgement must bind the boundary goal")
		}
		return boundary.idempotencyKey, nil
	case AcknowledgementNextGoalCreated:
		if !boundary.nextGoalIntentOK || goal != boundary.nextGoalIntent.goal {
			return Digest{}, fmt.Errorf("next-goal acknowledgement requires the matching durable creation intent")
		}
		return boundary.nextGoalIntent.idempotencyKey, nil
	default:
		return Digest{}, fmt.Errorf("unsupported orchestration acknowledgement kind %q", kind)
	}
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
