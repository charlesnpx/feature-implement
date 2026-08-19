package workspace

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type IntegrationLifecycleFaultPoint string

const (
	IntegrationFaultAfterIntentSynced  IntegrationLifecycleFaultPoint = "after_intent_synced"
	IntegrationFaultBeforeCommitCreate IntegrationLifecycleFaultPoint = "before_commit_create"
	IntegrationFaultAfterCommitCreated IntegrationLifecycleFaultPoint = "after_commit_created"
	IntegrationFaultBeforeRefCAS       IntegrationLifecycleFaultPoint = "before_ref_cas"
	IntegrationFaultAfterRefPrepared   IntegrationLifecycleFaultPoint = "after_ref_prepared"
	IntegrationFaultAfterRefCAS        IntegrationLifecycleFaultPoint = "after_ref_cas"
	IntegrationFaultAfterVerification  IntegrationLifecycleFaultPoint = "after_verification"
	IntegrationFaultBeforeCompletion   IntegrationLifecycleFaultPoint = "before_completion"
	IntegrationFaultAfterCompletion    IntegrationLifecycleFaultPoint = "after_completion"
)

type IntegrationLifecycleFaultInjector func(
	IntegrationLifecycleFaultPoint,
) error

type IntegrateMergeUnitRequest struct {
	AttemptID  ID
	OccurredAt time.Time
	Fault      IntegrationLifecycleFaultInjector
}

type MergeUnitIntegrationResult struct {
	attempt RuntimeAttemptProjection
	intent  MergeUnitIntegrationIntent
	record  JournalRecord
}

func (result MergeUnitIntegrationResult) Attempt() RuntimeAttemptProjection {
	return cloneRuntimeAttempt(result.attempt)
}
func (result MergeUnitIntegrationResult) Intent() MergeUnitIntegrationIntent {
	return result.intent
}
func (result MergeUnitIntegrationResult) MergeCommit() GitObjectID {
	return result.intent.expectedMerge
}
func (result MergeUnitIntegrationResult) Record() JournalRecord {
	return result.record
}

type integrationAcceptanceEvidence struct {
	head             GitObjectID
	tree             GitObjectID
	reviewReadiness  Digest
	adoptedHeadEvent Digest
}

func IntegrateMergeUnit(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	git IntegrationGitPort,
	request IntegrateMergeUnitRequest,
) (MergeUnitIntegrationResult, error) {
	if ctx == nil || journal == nil || repository == nil || git == nil ||
		request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"merge-unit integration requires context, journal, repository inspection, Git adapter, attempt, and occurrence time",
		)
	}
	snapshot, reviews, runtime, err := readIntegrationRuntime(
		journal, definition,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	attempt, exists := runtime.Attempt(request.AttemptID)
	if !exists {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"attempt %s is not reserved", request.AttemptID,
		)
	}
	target, ok := runtime.LocalTarget()
	if !ok || !target.Created() {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"integration requires a durable local target",
		)
	}
	binding := target.binding

	if attempt.integration == nil {
		evidence, err := confirmIntegrationAcceptance(
			ctx, snapshot, reviews, definition, repository,
			attempt, false,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		attemptInspection, err := git.InspectAttempt(
			ctx, binding, attempt.worktree, attempt.branch,
			evidence.head, evidence.tree,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		expectedFeatureMarker, err := expectedLocalTargetReflogMarker(
			runtime, target,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		intent, err := NewMergeUnitIntegrationIntent(
			MergeUnitIntegrationIntentOptions{
				WorkspaceID:            runtime.workspaceID,
				Generation:             runtime.activeGeneration,
				AttemptID:              attempt.attemptID,
				MergeUnit:              attempt.mergeUnit,
				FeatureRef:             binding.featureRef,
				ExpectedFeatureHead:    target.createdHead,
				ExpectedFeatureMarker:  expectedFeatureMarker,
				AttemptWorktreeBinding: attemptInspection.worktreeBinding,
				AcceptedHead:           evidence.head,
				AcceptedTree:           evidence.tree,
				ReviewReadinessDigest:  evidence.reviewReadiness,
				AdoptedHeadEventDigest: evidence.adoptedHeadEvent,
				OccurredAt:             request.OccurredAt,
			},
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		inspection, err := git.InspectIntegration(
			ctx, binding, attempt.branch, intent,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		if inspection.refState != IntegrationRefExpectedHead {
			return MergeUnitIntegrationResult{},
				integrationDriftError(inspection)
		}
		event, err := NewMergeUnitIntegrationIntendedJournalEvent(
			intent,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		if _, err := appendIntegrationJournalEvent(
			journal, snapshot, runtime, event,
			request.OccurredAt,
		); err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		if err := injectIntegrationLifecycleFault(
			request.Fault, IntegrationFaultAfterIntentSynced,
		); err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		snapshot, reviews, runtime, err = readIntegrationRuntime(
			journal, definition,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		attempt, exists = runtime.Attempt(request.AttemptID)
		if !exists || attempt.integration == nil {
			return MergeUnitIntegrationResult{}, fmt.Errorf(
				"durable integration intent is missing after append",
			)
		}
		target, _ = runtime.LocalTarget()
		binding = target.binding
	}

	integration := attempt.integration
	intent := integration.intent
	if intent.attemptID != request.AttemptID ||
		intent.workspaceID != runtime.workspaceID ||
		intent.generation != runtime.activeGeneration ||
		intent.mergeUnit != attempt.mergeUnit ||
		intent.featureRef != binding.featureRef {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"durable integration intent does not match the requested attempt and active target",
		)
	}

	if integration.Integrated() {
		if attempt.phase != AttemptCompleted {
			return MergeUnitIntegrationResult{}, fmt.Errorf(
				"completed integration projection is inconsistent",
			)
		}
		chain, err := completedIntegrationChain(
			runtime, target, attempt,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		if err := git.VerifyCompletedIntegration(
			ctx, binding, chain,
		); err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		return MergeUnitIntegrationResult{
			attempt: attempt, intent: intent,
		}, nil
	}

	if _, err := confirmIntegrationAcceptance(
		ctx, snapshot, reviews, definition, repository,
		attempt, true,
	); err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	inspection, err := git.InspectIntegration(
		ctx, binding, attempt.branch, intent,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	switch inspection.refState {
	case IntegrationRefExpectedHead:
		if !inspection.expectedCommit {
			if err := injectIntegrationLifecycleFault(
				request.Fault,
				IntegrationFaultBeforeCommitCreate,
			); err != nil {
				return MergeUnitIntegrationResult{}, err
			}
			if err := git.CreateIntegrationCommit(
				ctx, binding, attempt.branch, intent,
			); err != nil {
				return MergeUnitIntegrationResult{}, err
			}
			if err := injectIntegrationLifecycleFault(
				request.Fault,
				IntegrationFaultAfterCommitCreated,
			); err != nil {
				return MergeUnitIntegrationResult{}, err
			}
		}
		snapshot, reviews, runtime, err = readIntegrationRuntime(
			journal, definition,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		attempt, exists = runtime.Attempt(request.AttemptID)
		if !exists || attempt.integration == nil ||
			attempt.integration.intent.digest != intent.digest ||
			attempt.integration.Integrated() {
			return MergeUnitIntegrationResult{}, fmt.Errorf(
				"integration intent changed before feature-ref publication",
			)
		}
		if _, err := confirmIntegrationAcceptance(
			ctx, snapshot, reviews, definition, repository,
			attempt, true,
		); err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		if err := requireIntegrationCompletionReservation(
			snapshot, runtime,
		); err != nil {
			return MergeUnitIntegrationResult{}, fmt.Errorf(
				"reserve durable integration completion before feature-ref publication: %w",
				err,
			)
		}
		if err := injectIntegrationLifecycleFault(
			request.Fault, IntegrationFaultBeforeRefCAS,
		); err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		attemptInspection, err := git.InspectAttempt(
			ctx, binding, intent.attemptWorktreeBinding.worktree,
			attempt.branch, intent.acceptedHead, intent.acceptedTree,
		)
		if err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		if attemptInspection.worktreeBinding !=
			intent.attemptWorktreeBinding {
			return MergeUnitIntegrationResult{}, fmt.Errorf(
				"attempt worktree Git binding changed immediately before feature-ref publication",
			)
		}
		if err := git.PublishIntegration(
			ctx, binding, attempt.branch, intent, request.Fault,
		); err != nil {
			return MergeUnitIntegrationResult{}, err
		}
		if err := injectIntegrationLifecycleFault(
			request.Fault, IntegrationFaultAfterRefCAS,
		); err != nil {
			return MergeUnitIntegrationResult{}, err
		}
	case IntegrationRefExpectedMerge:
		if !inspection.expectedCommit {
			return MergeUnitIntegrationResult{}, fmt.Errorf(
				"feature ref points to a missing expected integration commit",
			)
		}
	default:
		return MergeUnitIntegrationResult{},
			integrationDriftError(inspection)
	}

	verified, err := git.InspectIntegration(
		ctx, binding, attempt.branch, intent,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	if verified.refState != IntegrationRefExpectedMerge ||
		!verified.expectedCommit {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"integration verification did not observe the exact expected merge",
		)
	}
	if err := injectIntegrationLifecycleFault(
		request.Fault, IntegrationFaultAfterVerification,
	); err != nil {
		return MergeUnitIntegrationResult{}, err
	}

	snapshot, reviews, runtime, err = readIntegrationRuntime(
		journal, definition,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	attempt, exists = runtime.Attempt(request.AttemptID)
	if !exists || attempt.integration == nil ||
		attempt.integration.intent.digest != intent.digest ||
		attempt.integration.Integrated() {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"integration intent changed before durable completion",
		)
	}
	if _, err := confirmIntegrationAcceptance(
		ctx, snapshot, reviews, definition, repository,
		attempt, true,
	); err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	if err := injectIntegrationLifecycleFault(
		request.Fault, IntegrationFaultBeforeCompletion,
	); err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	completionInspection, err := git.InspectIntegration(
		ctx, binding, attempt.branch, intent,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	if completionInspection.refState !=
		IntegrationRefExpectedMerge ||
		!completionInspection.expectedCommit {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"integration completion did not retain its exact published Git state",
		)
	}
	serialSegment := ID{}
	if attempt.serialSegmentHeld {
		serialSegment = attempt.serialSegment
	}
	supersededAttempts, err := integrationSupersededAttempts(
		runtime, attempt.attemptID,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	event, err := newMergeUnitIntegratedJournalEvent(
		intent, attempt.leaseID, serialSegment,
		supersededAttempts,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	record, err := appendIntegrationJournalEvent(
		journal, snapshot, runtime, event, request.OccurredAt,
	)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	if err := injectIntegrationLifecycleFault(
		request.Fault, IntegrationFaultAfterCompletion,
	); err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	_, _, runtime, err = readIntegrationRuntime(journal, definition)
	if err != nil {
		return MergeUnitIntegrationResult{}, err
	}
	completed, exists := runtime.Attempt(request.AttemptID)
	if !exists || completed.phase != AttemptCompleted ||
		completed.integration == nil ||
		!completed.integration.Integrated() ||
		completed.integration.intent.digest != intent.digest {
		return MergeUnitIntegrationResult{}, fmt.Errorf(
			"integration completion did not replay exactly",
		)
	}
	return MergeUnitIntegrationResult{
		attempt: completed, intent: intent, record: record,
	}, nil
}

func completedIntegrationChain(
	runtime WorkspaceRuntimeProjection,
	target RuntimeLocalTargetProjection,
	completed RuntimeAttemptProjection,
) ([]MergeUnitIntegrationIntent, error) {
	if completed.integration == nil ||
		!completed.integration.Integrated() ||
		completed.phase != AttemptCompleted {
		return nil, fmt.Errorf(
			"completed integration frontier requires one durable completed attempt",
		)
	}
	type transition struct {
		record uint64
		intent MergeUnitIntegrationIntent
	}
	transitions := make([]transition, 0)
	for _, attempt := range runtime.attempts {
		if attempt.integration == nil ||
			!attempt.integration.Integrated() ||
			attempt.integration.integratedRecord <
				completed.integration.integratedRecord {
			continue
		}
		transitions = append(
			transitions,
			transition{
				record: attempt.integration.integratedRecord,
				intent: attempt.integration.intent,
			},
		)
	}
	sort.Slice(transitions, func(i, j int) bool {
		return transitions[i].record < transitions[j].record
	})
	if len(transitions) == 0 ||
		transitions[0].record !=
			completed.integration.integratedRecord ||
		transitions[0].intent.digest !=
			completed.integration.intent.digest {
		return nil, fmt.Errorf(
			"completed integration is absent from the durable feature frontier",
		)
	}
	previousRecord := uint64(0)
	previousMerge := GitObjectID{}
	chain := make(
		[]MergeUnitIntegrationIntent, 0, len(transitions),
	)
	for index, transition := range transitions {
		if transition.record == 0 ||
			(previousRecord != 0 &&
				transition.record <= previousRecord) {
			return nil, fmt.Errorf(
				"completed integration frontier has duplicate or unordered transitions",
			)
		}
		if index != 0 &&
			transition.intent.expectedFeatureHead !=
				previousMerge {
			return nil, fmt.Errorf(
				"completed integration frontier is not one exact first-parent chain",
			)
		}
		previousRecord = transition.record
		previousMerge = transition.intent.expectedMerge
		chain = append(chain, transition.intent)
	}
	frontier := transitions[len(transitions)-1]
	if frontier.record != target.headRecord ||
		frontier.intent.expectedMerge != target.createdHead {
		return nil, fmt.Errorf(
			"completed integration frontier does not match the current durable feature head",
		)
	}
	return chain, nil
}

func readIntegrationRuntime(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (
	JournalSnapshot,
	ReviewRuntimeProjection,
	WorkspaceRuntimeProjection,
	error,
) {
	if journal == nil || definition.workspace.id.IsZero() ||
		definition.generation.IsZero() {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{},
			fmt.Errorf(
				"integration runtime requires journal and effective definition",
			)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{}, err
	}
	reviews, err := RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{}, err
	}
	runtime := reviews.core
	if runtime.workspaceID != definition.workspace.id ||
		runtime.activeGeneration != definition.generation {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{},
			fmt.Errorf(
				"integration definition does not match the active workspace generation",
			)
	}
	if err := requireReadyLocalTarget(runtime); err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{}, err
	}
	if err := verifyWorkspaceWorktreeRootBinding(
		runtime.worktreeRoot,
	); err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{},
			WorkspaceRuntimeProjection{}, err
	}
	return snapshot, reviews, runtime, nil
}

func confirmIntegrationAcceptance(
	ctx context.Context,
	snapshot JournalSnapshot,
	reviews ReviewRuntimeProjection,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	attempt RuntimeAttemptProjection,
	pendingIntent bool,
) (integrationAcceptanceEvidence, error) {
	if attempt.phase != AttemptActive || attempt.verifiedHead.IsZero() {
		return integrationAcceptanceEvidence{}, fmt.Errorf(
			"attempt %s must be active at an exact head for integration",
			attempt.attemptID,
		)
	}
	if pendingIntent != (attempt.integration != nil) {
		return integrationAcceptanceEvidence{}, fmt.Errorf(
			"attempt %s integration intent state changed",
			attempt.attemptID,
		)
	}
	if boundary, exists := attempt.CurrentBoundary(); exists {
		return integrationAcceptanceEvidence{}, fmt.Errorf(
			"attempt %s cannot integrate with pending boundary %s",
			attempt.attemptID, boundary.boundaryID,
		)
	}
	unit, err := executionForMergeUnit(
		definition.execution, attempt.mergeUnit,
	)
	if err != nil {
		return integrationAcceptanceEvidence{}, err
	}
	if err := requireIntegrationGateReady(
		snapshot, definition, attempt,
	); err != nil {
		return integrationAcceptanceEvidence{}, err
	}
	repositoryRequest, err := NewReviewRepositoryRequest(
		attempt.worktree, attempt.branch, attempt.verifiedHead,
	)
	if err != nil {
		return integrationAcceptanceEvidence{}, err
	}
	repositorySnapshot, err := repository.InspectReviewSnapshot(
		ctx, repositoryRequest,
	)
	if err != nil {
		return integrationAcceptanceEvidence{}, err
	}
	if !repositorySnapshot.clean ||
		repositorySnapshot.head != attempt.verifiedHead {
		return integrationAcceptanceEvidence{}, fmt.Errorf(
			"integration requires the exact clean accepted attempt head",
		)
	}
	evidence := integrationAcceptanceEvidence{
		head: repositorySnapshot.head,
		tree: repositorySnapshot.tree,
	}
	if loop, configured := unit.ReviewLoop(); configured {
		state, exists := reviews.State(attempt.attemptID)
		if !exists || !state.MergeReady() ||
			state.loop.digest != loop.digest ||
			state.head != repositorySnapshot.head ||
			state.tree != repositorySnapshot.tree {
			return integrationAcceptanceEvidence{}, fmt.Errorf(
				"attempt %s has no exact-head review readiness for integration",
				attempt.attemptID,
			)
		}
		if err := validateAttemptReviewProtocolState(
			definition, unit, attempt, state, true, false,
		); err != nil {
			return integrationAcceptanceEvidence{}, err
		}
		readiness, err := newReviewMergeReadiness(
			definition, attempt, state,
		)
		if err != nil {
			return integrationAcceptanceEvidence{}, err
		}
		evidence.reviewReadiness = readiness.digest
	} else {
		if _, exists := reviews.State(attempt.attemptID); exists {
			return integrationAcceptanceEvidence{}, fmt.Errorf(
				"unconfigured review evidence cannot authorize integration",
			)
		}
		if protocol, configured := unit.CommitProtocol(); configured {
			if attempt.commitProtocol == nil ||
				attempt.commitProtocol.protocol.digest != protocol.digest ||
				attempt.commitProtocol.phase != CommitProtocolComplete ||
				attempt.commitProtocol.Head() != attempt.verifiedHead {
				return integrationAcceptanceEvidence{}, fmt.Errorf(
					"attempt %s has not completed its configured commit protocol",
					attempt.attemptID,
				)
			}
		} else if attempt.commitProtocol != nil {
			return integrationAcceptanceEvidence{}, fmt.Errorf(
				"attempt %s has an unconfigured commit protocol",
				attempt.attemptID,
			)
		}
		record, exists := exactAdoptedHeadRecord(
			snapshot, attempt.attemptID, repositorySnapshot,
		)
		if !exists {
			return integrationAcceptanceEvidence{}, fmt.Errorf(
				"attempt %s requires durable adopt-head evidence for exact head and tree",
				attempt.attemptID,
			)
		}
		evidence.adoptedHeadEvent = record.eventHash
	}
	if pendingIntent {
		intent := attempt.integration.intent
		if evidence.head != intent.acceptedHead ||
			evidence.tree != intent.acceptedTree ||
			evidence.reviewReadiness !=
				intent.reviewReadinessDigest ||
			evidence.adoptedHeadEvent !=
				intent.adoptedHeadEventDigest {
			return integrationAcceptanceEvidence{}, fmt.Errorf(
				"integration acceptance evidence changed after durable intent",
			)
		}
	}
	return evidence, nil
}

func requireIntegrationGateReady(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	attempt RuntimeAttemptProjection,
) error {
	gates, err := RebuildGateView(snapshot, definition)
	if err != nil {
		return err
	}
	for _, unit := range gates.Units {
		if unit.PlanID == attempt.mergeUnit.planID.String() &&
			unit.MergeUnitID == attempt.mergeUnit.mergeUnitID.String() {
			if unit.AttemptID != attempt.attemptID.String() ||
				!unit.MergeReady {
				return fmt.Errorf(
					"merge unit %s is not ready for local integration",
					attempt.mergeUnit,
				)
			}
			return nil
		}
	}
	return fmt.Errorf(
		"merge unit %s is absent from integration gates",
		attempt.mergeUnit,
	)
}

func appendIntegrationJournalEvent(
	journal *WorkspaceJournal,
	snapshot JournalSnapshot,
	runtime WorkspaceRuntimeProjection,
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalRecord, error) {
	if !isIntegrationJournalEvent(event) {
		return JournalRecord{}, fmt.Errorf(
			"unsupported integration journal event %T", event,
		)
	}
	appendRequest, err := NewJournalAppend(event, occurredAt)
	if err != nil {
		return JournalRecord{}, err
	}
	prospective, err := buildJournalRecord(snapshot, appendRequest)
	if err != nil {
		return JournalRecord{}, err
	}
	if _, err := reduceWorkspaceRuntime(
		runtime, prospective,
	); err != nil {
		return JournalRecord{}, fmt.Errorf(
			"validate integration transition: %w", err,
		)
	}
	return journal.AppendIfHead(appendRequest, snapshot.head)
}

func injectIntegrationLifecycleFault(
	injector IntegrationLifecycleFaultInjector,
	point IntegrationLifecycleFaultPoint,
) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf(
			"integration lifecycle fault at %s: %w", point, err,
		)
	}
	return nil
}
