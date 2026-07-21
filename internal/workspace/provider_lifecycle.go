package workspace

import (
	"context"
	"fmt"
	"time"
)

type ReserveProviderIntentRequest struct {
	Intent     ProviderIntent
	OccurredAt time.Time
}

func ReserveProviderIntent(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	evaluator *AuthorizationEvaluator,
	request ReserveProviderIntentRequest,
) (ProviderIntentProjection, JournalRecord, error) {
	if journal == nil || evaluator == nil || request.Intent.intentID.IsZero() || request.OccurredAt.IsZero() {
		return ProviderIntentProjection{}, JournalRecord{}, fmt.Errorf("reserve provider intent requires journal, authorization evaluator, typed intent, and occurrence time")
	}
	snapshot, providerProjection, authorization, core, err := readProviderLifecycleRuntime(journal, definition)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	if existing, exists := providerProjection.Intent(request.Intent.intentID); exists {
		if existing.intent.digest != request.Intent.digest {
			return ProviderIntentProjection{}, JournalRecord{}, fmt.Errorf("provider intent identity conflicts with durable intent")
		}
		return existing, JournalRecord{}, nil
	}
	if err := validateProviderIntentAgainstRuntime(snapshot, definition, core, providerProjection, request.Intent); err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	binding, err := NewAuthorizationSnapshotBinding(
		snapshot.head, snapshot.Revision(AuthorizationEpochJournalResource(definition.workspace.id)),
	)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	planning, err := evaluator.PlanAuthorization(authorization.state, request.Intent.authorization, binding)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, fmt.Errorf("plan provider authorization: %w", err)
	}
	reservation, err := evaluator.ReserveAuthorizationIntent(
		authorization.state, request.Intent.authorization, binding, planning,
	)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, fmt.Errorf("reserve provider authorization: %w", err)
	}
	event, err := NewProviderIntentReservedJournalEvent(
		definition.workspace.id, definition.generation, request.Intent, planning, reservation,
	)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	record, err := appendProviderJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	updated, err := readProviderProjection(journal, definition)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	projected, exists := updated.Intent(request.Intent.intentID)
	if !exists || projected.status != ProviderIntentReserved {
		return ProviderIntentProjection{}, JournalRecord{}, fmt.Errorf("provider intent reservation did not replay")
	}
	return projected, record, nil
}

func validateProviderIntentAgainstRuntime(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	core WorkspaceRuntimeProjection,
	providerProjection ProviderRuntimeProjection,
	intent ProviderIntent,
) error {
	if intent.scope.workspaceID != definition.workspace.id || intent.scope.generation != definition.generation ||
		intent.scope.repository != definition.workspace.repository || intent.scope.remote != definition.workspace.remote ||
		intent.digest.IsZero() || intent.idempotencyKey.IsZero() {
		return fmt.Errorf("provider intent does not match the active workspace definition")
	}
	attempt, exists := core.Attempt(intent.scope.attemptID)
	if !exists || attempt.phase != AttemptActive || attempt.generation != definition.generation ||
		attempt.mergeUnit != intent.scope.mergeUnit || attempt.repository != intent.scope.repository ||
		attempt.serialSegment != intent.scope.serialSegment || !attempt.serialSegmentHeld {
		return fmt.Errorf("provider intent requires the exact active attempt and held serial segment")
	}
	if attempt.verifiedHead != intent.head || intent.scope.frontier.head != intent.head {
		return fmt.Errorf("provider intent head does not match the active attempt")
	}
	if (intent.kind == ProviderIntentPush || intent.kind == ProviderIntentOpenPullRequest || intent.kind == ProviderIntentMerge) &&
		attempt.branch != intent.branch {
		return fmt.Errorf("provider intent branch does not match the active attempt")
	}
	if (intent.kind == ProviderIntentOpenPullRequest || intent.kind == ProviderIntentMerge) &&
		intent.baseRef != definition.workspace.baseRef {
		return fmt.Errorf("provider pull request base does not match the workspace base")
	}
	durablePullRequest, hasPullRequest, err := providerPullRequestForAttempt(providerProjection, intent.scope.attemptID)
	if err != nil {
		return err
	}
	switch intent.kind {
	case ProviderIntentOpenPullRequest:
		if hasPullRequest {
			return fmt.Errorf("provider intent cannot open a second pull request for the attempt")
		}
	case ProviderIntentPush:
		if hasPullRequest && intent.pullRequest != durablePullRequest {
			return fmt.Errorf("provider push must bind the attempt's provider-derived pull request identity")
		}
		if !hasPullRequest && !intent.pullRequest.IsZero() {
			return fmt.Errorf("provider push cannot supply a pull request identity before the provider establishes it")
		}
		if hasPullRequest {
			open, found, openErr := providerAppliedOpenIntentForAttempt(providerProjection, intent.scope.attemptID)
			if openErr != nil {
				return openErr
			}
			if !found || open.branch != intent.branch {
				return fmt.Errorf("provider push does not match the durable pull request branch")
			}
		}
	case ProviderIntentMerge:
		if !hasPullRequest || intent.pullRequest != durablePullRequest {
			return fmt.Errorf("provider merge must bind the attempt's sole provider-derived pull request identity")
		}
		open, found, openErr := providerAppliedOpenIntentForAttempt(providerProjection, intent.scope.attemptID)
		if openErr != nil {
			return openErr
		}
		if !found || open.branch != intent.branch || open.baseRef != intent.baseRef {
			return fmt.Errorf("provider merge does not match the durable pull request branch and base")
		}
		unit, unitErr := executionForMergeUnit(definition.execution, attempt.mergeUnit)
		if unitErr != nil {
			return unitErr
		}
		if _, configured := unit.ReviewLoop(); configured {
			reviews, reviewErr := RebuildReviewRuntime(snapshot, definition)
			if reviewErr != nil {
				return reviewErr
			}
			review, exists := reviews.State(attempt.attemptID)
			if !exists || !review.MergeReady() || review.Head() != intent.head || review.Tree() != intent.tree {
				return fmt.Errorf("provider merge requires exact-head clean review readiness")
			}
		}
	}
	return nil
}

type ProviderDispatchTicket struct {
	intent     ProviderIntent
	record     JournalRecord
	capability providerBrokerCapability
}

func (ticket ProviderDispatchTicket) Intent() ProviderIntent        { return ticket.intent }
func (ticket ProviderDispatchTicket) DispatchRecord() JournalRecord { return ticket.record }
func (ticket ProviderDispatchTicket) CapabilityDigest() Digest      { return ticket.capability.digest }

func AuthorizeProviderIntentDispatch(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	evaluator *AuthorizationEvaluator,
	intentID ID,
) (ProviderDispatchTicket, error) {
	if journal == nil || evaluator == nil || intentID.IsZero() {
		return ProviderDispatchTicket{}, fmt.Errorf("authorize provider dispatch requires journal, evaluator, and intent")
	}
	snapshot, providerProjection, authorization, core, err := readProviderLifecycleRuntime(journal, definition)
	if err != nil {
		return ProviderDispatchTicket{}, err
	}
	projected, exists := providerProjection.Intent(intentID)
	if !exists || projected.status != ProviderIntentReserved {
		if exists && projected.status.needsReconciliation() {
			return ProviderDispatchTicket{}, fmt.Errorf("provider intent %s was already dispatched and requires reconciliation", intentID)
		}
		return ProviderDispatchTicket{}, fmt.Errorf("provider intent %s is not reserved for dispatch", intentID)
	}
	if err := validateProviderIntentAgainstRuntime(snapshot, definition, core, providerProjection, projected.intent); err != nil {
		return ProviderDispatchTicket{}, err
	}
	binding, err := NewAuthorizationSnapshotBinding(
		snapshot.head, snapshot.Revision(AuthorizationEpochJournalResource(definition.workspace.id)),
	)
	if err != nil {
		return ProviderDispatchTicket{}, err
	}
	planning, err := evaluator.PlanAuthorization(authorization.state, projected.intent.authorization, binding)
	if err != nil {
		return ProviderDispatchTicket{}, fmt.Errorf("recheck provider planning authorization: %w", err)
	}
	reservation, err := evaluator.ReserveAuthorizationIntent(
		authorization.state, projected.intent.authorization, binding, planning,
	)
	if err != nil {
		return ProviderDispatchTicket{}, fmt.Errorf("recheck provider reservation authorization: %w", err)
	}
	queue, err := evaluator.EnterAuthorizationQueue(
		authorization.state, projected.intent.authorization, binding, reservation,
	)
	if err != nil {
		return ProviderDispatchTicket{}, fmt.Errorf("enter provider authorization queue: %w", err)
	}
	before, err := evaluator.AuthorizeImmediatelyBeforeDispatch(
		authorization.state, projected.intent.authorization, binding, queue,
	)
	if err != nil {
		return ProviderDispatchTicket{}, fmt.Errorf("authorize provider immediately before dispatch: %w", err)
	}
	effect, err := NewAuthorizationEffectDispatched(projected.intent.intentID, before)
	if err != nil {
		return ProviderDispatchTicket{}, err
	}
	event, err := NewProviderIntentDispatchedJournalEvent(
		definition.workspace.id, definition.generation, projected.intent,
		planning, reservation, queue, effect,
	)
	if err != nil {
		return ProviderDispatchTicket{}, err
	}
	record, err := appendProviderJournalEvent(journal, snapshot, event, before.evaluatedAt)
	if err != nil {
		return ProviderDispatchTicket{}, err
	}
	capability, err := newProviderBrokerCapability(projected.intent, before.epoch, record.eventHash)
	if err != nil {
		return ProviderDispatchTicket{}, err
	}
	return ProviderDispatchTicket{intent: projected.intent, record: record, capability: capability}, nil
}

type ProviderExecutionResult struct {
	projection ProviderIntentProjection
	result     ProviderResult
	record     JournalRecord
}

func (result ProviderExecutionResult) Projection() ProviderIntentProjection { return result.projection }
func (result ProviderExecutionResult) Result() ProviderResult               { return result.result }
func (result ProviderExecutionResult) Record() JournalRecord                { return result.record }

func ExecuteProviderIntent(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	broker *ProviderBroker,
	ticket ProviderDispatchTicket,
	occurredAt time.Time,
) (ProviderExecutionResult, error) {
	if journal == nil || broker == nil || ticket.intent.intentID.IsZero() ||
		ticket.record.eventHash.IsZero() || ticket.capability.digest.IsZero() || occurredAt.IsZero() {
		return ProviderExecutionResult{}, fmt.Errorf("execute provider intent requires journal, broker, durable dispatch ticket, and occurrence time")
	}
	if broker.provider != definition.workspace.provider || ticket.intent.scope.workspaceID != definition.workspace.id ||
		ticket.intent.scope.generation != definition.generation ||
		ticket.capability.dispatchRecord != ticket.record.eventHash {
		return ProviderExecutionResult{}, fmt.Errorf("provider dispatch ticket does not match broker or active definition")
	}
	current, err := readProviderProjection(journal, definition)
	if err != nil {
		return ProviderExecutionResult{}, err
	}
	projected, exists := current.Intent(ticket.intent.intentID)
	if !exists || projected.status != ProviderIntentDispatched ||
		projected.dispatchRecordHash != ticket.record.eventHash || projected.intent.digest != ticket.intent.digest {
		return ProviderExecutionResult{}, fmt.Errorf("provider dispatch ticket is stale or already resolved")
	}
	providerResult, brokerErr := broker.dispatch(ctx, ticket.capability, ticket.intent)
	if providerResult.digest.IsZero() {
		if brokerErr == nil {
			brokerErr = fmt.Errorf("provider broker returned no canonical result")
		}
		marker := "broker-dispatch-error-" + DigestBytes([]byte(fmt.Sprint(brokerErr))).String()
		providerResult, err = broker.failureResult(ticket.intent, ProviderAdapterAmbiguous, marker, brokerErr)
		if providerResult.digest.IsZero() {
			return ProviderExecutionResult{}, fmt.Errorf("canonicalize provider broker failure: %w", err)
		}
	}
	recorded, record, recordErr := recordProviderResult(
		journal, definition, projected, providerResult, occurredAt,
	)
	if recordErr != nil {
		return ProviderExecutionResult{}, recordErr
	}
	result := ProviderExecutionResult{projection: recorded, result: providerResult, record: record}
	if brokerErr != nil {
		return result, fmt.Errorf("provider broker outcome %s recorded: %w", providerResult.status, brokerErr)
	}
	return result, nil
}

func recordProviderResult(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	dispatched ProviderIntentProjection,
	result ProviderResult,
	occurredAt time.Time,
) (ProviderIntentProjection, JournalRecord, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	providerProjection, err := RebuildProviderRuntime(snapshot, definition)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	current, exists := providerProjection.Intent(dispatched.intent.intentID)
	if !exists || current.status != ProviderIntentDispatched ||
		current.dispatchRecordHash != dispatched.dispatchRecordHash || current.dispatchEpoch != dispatched.dispatchEpoch {
		return ProviderIntentProjection{}, JournalRecord{}, fmt.Errorf("provider result target is no longer the exact dispatched intent")
	}
	if err := validateProviderResultAgainstIntent(result, current.intent, definition.workspace.provider.kind); err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	event, err := NewProviderResultRecordedJournalEvent(
		definition.workspace.id, definition.generation, result,
		current.intent.authorization.digest, current.dispatchEpoch,
	)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	record, err := appendProviderJournalEvent(journal, snapshot, event, occurredAt)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	updated, err := readProviderProjection(journal, definition)
	if err != nil {
		return ProviderIntentProjection{}, JournalRecord{}, err
	}
	projected, exists := updated.Intent(current.intent.intentID)
	if !exists || projected.status != result.status {
		return ProviderIntentProjection{}, JournalRecord{}, fmt.Errorf("provider result did not replay")
	}
	return projected, record, nil
}

type ProviderReconciliationResult struct {
	projection     ProviderIntentProjection
	reconciliation ProviderReconciliation
	record         JournalRecord
}

func (result ProviderReconciliationResult) Projection() ProviderIntentProjection {
	return result.projection
}
func (result ProviderReconciliationResult) Reconciliation() ProviderReconciliation {
	return result.reconciliation
}
func (result ProviderReconciliationResult) Record() JournalRecord { return result.record }

func ReconcileProviderIntent(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	broker *ProviderBroker,
	intentID ID,
	occurredAt time.Time,
) (ProviderReconciliationResult, error) {
	if journal == nil || broker == nil || intentID.IsZero() || occurredAt.IsZero() {
		return ProviderReconciliationResult{}, fmt.Errorf("reconcile provider intent requires journal, broker, intent, and occurrence time")
	}
	if broker.provider != definition.workspace.provider {
		return ProviderReconciliationResult{}, fmt.Errorf("provider reconciliation broker does not match active definition")
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return ProviderReconciliationResult{}, err
	}
	projection, err := RebuildProviderRuntime(snapshot, definition)
	if err != nil {
		return ProviderReconciliationResult{}, err
	}
	projected, exists := projection.Intent(intentID)
	if !exists || !projected.status.needsReconciliation() {
		return ProviderReconciliationResult{}, fmt.Errorf("provider intent %s has no reconciliation obligation", intentID)
	}
	capability, err := newProviderQueryCapability(projected.intent, snapshot.head, occurredAt)
	if err != nil {
		return ProviderReconciliationResult{}, err
	}
	observation, err := broker.reconcile(ctx, capability, projected.intent)
	if err != nil {
		return ProviderReconciliationResult{}, fmt.Errorf("query provider reconciliation: %w", err)
	}
	if observation.disposition == ProviderEffectUnknown {
		return ProviderReconciliationResult{}, fmt.Errorf("provider intent %s remains ambiguous after query %s", intentID, observation.digest)
	}
	reconciliation, err := newProviderReconciliation(
		projected.intent, projected.status, definition.workspace.provider.kind, observation,
	)
	if err != nil {
		return ProviderReconciliationResult{}, err
	}
	event, err := NewProviderIntentReconciledJournalEvent(
		definition.workspace.id, definition.generation, reconciliation,
		projected.intent.authorization.digest, projected.dispatchEpoch,
	)
	if err != nil {
		return ProviderReconciliationResult{}, err
	}
	record, err := appendProviderJournalEvent(journal, snapshot, event, occurredAt)
	if err != nil {
		return ProviderReconciliationResult{}, err
	}
	updated, err := readProviderProjection(journal, definition)
	if err != nil {
		return ProviderReconciliationResult{}, err
	}
	resolved, exists := updated.Intent(intentID)
	if !exists || resolved.status != ProviderIntentReconciled {
		return ProviderReconciliationResult{}, fmt.Errorf("provider reconciliation did not replay")
	}
	return ProviderReconciliationResult{
		projection: resolved, reconciliation: reconciliation, record: record,
	}, nil
}

func RecordProviderPullRequestAuthorization(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	broker *ProviderBroker,
	intentID ID,
	occurredAt time.Time,
) (StandingGrant, JournalRecord, error) {
	if journal == nil || broker == nil || intentID.IsZero() || occurredAt.IsZero() {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("record provider pull request authorization requires journal, broker, intent, and occurrence time")
	}
	projection, err := readProviderProjection(journal, definition)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	projected, exists := projection.Intent(intentID)
	if !exists || projected.intent.kind != ProviderIntentOpenPullRequest || !providerProjectionEffectApplied(projected) {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("provider intent %s has no applied pull request result", intentID)
	}
	var observation ProviderPullRequestObservation
	if projected.status == ProviderIntentSucceeded {
		observation, err = projected.result.pullRequestObservation()
	} else {
		reconciliation := projected.reconciliation
		observation, err = NewProviderPullRequestObservation(
			reconciliation.pullRequest.provider, reconciliation.pullRequest.repository,
			reconciliation.pullRequest.number, reconciliation.pullRequestHead, reconciliation.digest,
		)
	}
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	return RecordDerivedStandingGrantPullRequest(
		ctx, journal, definition, broker, projected.grantID, observation, occurredAt,
	)
}

func readProviderLifecycleRuntime(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (JournalSnapshot, ProviderRuntimeProjection, AuthorizationRuntimeProjection, WorkspaceRuntimeProjection, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalSnapshot{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, WorkspaceRuntimeProjection{}, err
	}
	providerProjection, err := RebuildProviderRuntime(snapshot, definition)
	if err != nil {
		return JournalSnapshot{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, WorkspaceRuntimeProjection{}, err
	}
	authorization, err := RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil {
		return JournalSnapshot{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, WorkspaceRuntimeProjection{}, err
	}
	core, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalSnapshot{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, WorkspaceRuntimeProjection{}, err
	}
	return snapshot, providerProjection, authorization, core, nil
}

func readProviderProjection(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (ProviderRuntimeProjection, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return ProviderRuntimeProjection{}, err
	}
	return RebuildProviderRuntime(snapshot, definition)
}

func appendProviderJournalEvent(
	journal *WorkspaceJournal,
	snapshot JournalSnapshot,
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalRecord, error) {
	reads, writes, ok := providerJournalEventResources(event)
	if !ok {
		return JournalRecord{}, fmt.Errorf("unsupported provider journal event %T", event)
	}
	readSet := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, err := NewJournalResourceRevision(resource, snapshot.Revision(resource))
		if err != nil {
			return JournalRecord{}, err
		}
		readSet = append(readSet, revision)
	}
	appendRequest, err := newPrivilegedJournalAppend(event, occurredAt, readSet, writes)
	if err != nil {
		return JournalRecord{}, err
	}
	return journal.AppendIfHead(appendRequest, snapshot.head)
}
