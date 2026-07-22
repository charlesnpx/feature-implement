package workspace

import (
	"encoding/json"
	"fmt"
)

type ProviderIntentReservedJournalEvent struct {
	workspaceID ID
	generation  Digest
	intent      ProviderIntent
	planning    AuthorizationCapability
	reservation AuthorizationCapability
}

func NewProviderIntentReservedJournalEvent(
	workspaceID ID,
	generation Digest,
	intent ProviderIntent,
	planning, reservation AuthorizationCapability,
) (ProviderIntentReservedJournalEvent, error) {
	event := ProviderIntentReservedJournalEvent{
		workspaceID: workspaceID, generation: generation, intent: intent,
		planning: planning, reservation: reservation,
	}
	if err := event.validate(); err != nil {
		return ProviderIntentReservedJournalEvent{}, err
	}
	return event, nil
}

func (ProviderIntentReservedJournalEvent) isWorkspaceJournalEvent() {}
func (ProviderIntentReservedJournalEvent) eventType() JournalEventType {
	return JournalEventProviderIntentReserved
}
func (event ProviderIntentReservedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ProviderIntentReservedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.intent.intentID.IsZero() ||
		event.intent.digest.IsZero() || event.intent.scope.workspaceID != event.workspaceID ||
		event.intent.scope.generation != event.generation {
		return fmt.Errorf("provider intent reservation requires exact workspace, generation, and typed intent")
	}
	if err := validateAuthorizationCapabilityStep(
		event.intent, event.planning, AuthorizationAtPlanning, AuthorizationCapability{},
	); err != nil {
		return err
	}
	return validateAuthorizationCapabilityStep(
		event.intent, event.reservation, AuthorizationAtIntentReservation, event.planning,
	)
}

func (event ProviderIntentReservedJournalEvent) Intent() ProviderIntent { return event.intent }

type ProviderIntentAbandonedJournalEvent struct {
	workspaceID  ID
	generation   Digest
	intentID     ID
	intentDigest Digest
}

func NewProviderIntentAbandonedJournalEvent(
	workspaceID ID,
	generation Digest,
	intent ProviderIntent,
) (ProviderIntentAbandonedJournalEvent, error) {
	event := ProviderIntentAbandonedJournalEvent{
		workspaceID: workspaceID, generation: generation,
		intentID: intent.intentID, intentDigest: intent.digest,
	}
	if err := event.validate(); err != nil {
		return ProviderIntentAbandonedJournalEvent{}, err
	}
	return event, nil
}

func (ProviderIntentAbandonedJournalEvent) isWorkspaceJournalEvent() {}
func (ProviderIntentAbandonedJournalEvent) eventType() JournalEventType {
	return JournalEventProviderIntentAbandoned
}
func (event ProviderIntentAbandonedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ProviderIntentAbandonedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.intentID.IsZero() || event.intentDigest.IsZero() {
		return fmt.Errorf("provider intent abandonment requires exact workspace, generation, and intent")
	}
	return nil
}

type ProviderMergePreflightRecordedJournalEvent struct {
	workspaceID ID
	generation  Digest
	preflight   ProviderMergePreflight
}

func NewProviderMergePreflightRecordedJournalEvent(
	workspaceID ID,
	generation Digest,
	preflight ProviderMergePreflight,
) (ProviderMergePreflightRecordedJournalEvent, error) {
	event := ProviderMergePreflightRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, preflight: preflight,
	}
	if err := event.validate(); err != nil {
		return ProviderMergePreflightRecordedJournalEvent{}, err
	}
	return event, nil
}

func (ProviderMergePreflightRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (ProviderMergePreflightRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventProviderMergePreflightRecorded
}
func (event ProviderMergePreflightRecordedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event ProviderMergePreflightRecordedJournalEvent) validate() error {
	canonical, err := canonicalProviderMergePreflight(event.preflight)
	if event.workspaceID.IsZero() || event.generation.IsZero() || err != nil ||
		event.preflight.digest.IsZero() || DigestBytes(canonical) != event.preflight.digest {
		return fmt.Errorf("provider merge preflight event requires canonical generation-bound evidence")
	}
	return nil
}

func (event ProviderMergePreflightRecordedJournalEvent) Preflight() ProviderMergePreflight {
	return event.preflight
}

type ProviderIntentDispatchedJournalEvent struct {
	workspaceID  ID
	generation   Digest
	intentID     ID
	intentDigest Digest
	planning     AuthorizationCapability
	reservation  AuthorizationCapability
	queue        AuthorizationCapability
	effect       AuthorizationEffectDispatched
}

func NewProviderIntentDispatchedJournalEvent(
	workspaceID ID,
	generation Digest,
	intent ProviderIntent,
	planning, reservation, queue AuthorizationCapability,
	effect AuthorizationEffectDispatched,
) (ProviderIntentDispatchedJournalEvent, error) {
	event := ProviderIntentDispatchedJournalEvent{
		workspaceID: workspaceID, generation: generation, intentID: intent.intentID,
		intentDigest: intent.digest, planning: planning, reservation: reservation,
		queue: queue, effect: effect,
	}
	if err := validateProviderDispatchEvent(event, intent); err != nil {
		return ProviderIntentDispatchedJournalEvent{}, err
	}
	return event, nil
}

func (ProviderIntentDispatchedJournalEvent) isWorkspaceJournalEvent() {}
func (ProviderIntentDispatchedJournalEvent) eventType() JournalEventType {
	return JournalEventProviderIntentDispatched
}
func (event ProviderIntentDispatchedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ProviderIntentDispatchedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.intentID.IsZero() ||
		event.intentDigest.IsZero() || event.effect.effectID != event.intentID ||
		event.effect.capability.checkpoint != AuthorizationBeforeDispatch ||
		event.effect.capability.requestDigest != event.queue.requestDigest ||
		event.effect.capability.priorDigest != event.queue.digest ||
		event.effect.capability.epoch != event.queue.epoch {
		return fmt.Errorf("provider dispatch event requires exact intent and chained authorization effect")
	}
	if event.planning.checkpoint != AuthorizationAtPlanning ||
		event.reservation.checkpoint != AuthorizationAtIntentReservation ||
		event.queue.checkpoint != AuthorizationAtQueueEntry ||
		event.reservation.priorDigest != event.planning.digest ||
		event.queue.priorDigest != event.reservation.digest ||
		event.planning.requestDigest != event.reservation.requestDigest ||
		event.planning.requestDigest != event.queue.requestDigest ||
		event.planning.snapshot != event.reservation.snapshot ||
		event.planning.snapshot != event.queue.snapshot ||
		event.planning.snapshot != event.effect.capability.snapshot {
		return fmt.Errorf("provider dispatch authorization checkpoint chain is invalid")
	}
	return nil
}

func validateProviderDispatchEvent(event ProviderIntentDispatchedJournalEvent, intent ProviderIntent) error {
	if event.workspaceID != intent.scope.workspaceID || event.generation != intent.scope.generation ||
		event.intentID != intent.intentID || event.intentDigest != intent.digest {
		return fmt.Errorf("provider dispatch event does not match typed intent")
	}
	if err := validateAuthorizationCapabilityStep(intent, event.planning, AuthorizationAtPlanning, AuthorizationCapability{}); err != nil {
		return err
	}
	if err := validateAuthorizationCapabilityStep(intent, event.reservation, AuthorizationAtIntentReservation, event.planning); err != nil {
		return err
	}
	if err := validateAuthorizationCapabilityStep(intent, event.queue, AuthorizationAtQueueEntry, event.reservation); err != nil {
		return err
	}
	if err := validateAuthorizationCapabilityStep(intent, event.effect.capability, AuthorizationBeforeDispatch, event.queue); err != nil {
		return err
	}
	return event.validate()
}

func validateAuthorizationCapabilityStep(
	intent ProviderIntent,
	capability AuthorizationCapability,
	checkpoint AuthorizationCheckpoint,
	prior AuthorizationCapability,
) error {
	if capability.checkpoint != checkpoint || capability.requestDigest != intent.authorization.digest ||
		capability.epoch != intent.scope.epoch || capability.grantID.IsZero() || capability.stateDigest.IsZero() ||
		capability.snapshot.journalHead.IsZero() || capability.digest.IsZero() ||
		capabilityDigest(capability) != capability.digest {
		return fmt.Errorf("provider intent has invalid %s authorization capability", checkpoint)
	}
	if checkpoint == AuthorizationAtPlanning {
		if !capability.priorDigest.IsZero() {
			return fmt.Errorf("provider planning capability cannot have a predecessor")
		}
		return nil
	}
	if prior.digest.IsZero() || capability.priorDigest != prior.digest || capability.snapshot != prior.snapshot ||
		capability.grantID != prior.grantID || capability.stateDigest != prior.stateDigest {
		return fmt.Errorf("provider %s capability is not chained to %s", checkpoint, prior.checkpoint)
	}
	return nil
}

type ProviderResultRecordedJournalEvent struct {
	workspaceID          ID
	generation           Digest
	result               ProviderResult
	authorizationRequest Digest
	dispatchEpoch        uint64
}

func NewProviderResultRecordedJournalEvent(
	workspaceID ID,
	generation Digest,
	result ProviderResult,
	authorizationRequest Digest,
	dispatchEpoch uint64,
) (ProviderResultRecordedJournalEvent, error) {
	event := ProviderResultRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, result: result,
		authorizationRequest: authorizationRequest, dispatchEpoch: dispatchEpoch,
	}
	if err := event.validate(); err != nil {
		return ProviderResultRecordedJournalEvent{}, err
	}
	return event, nil
}

func (ProviderResultRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (ProviderResultRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventProviderResultRecorded
}
func (event ProviderResultRecordedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ProviderResultRecordedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.result.intentID.IsZero() ||
		event.result.intentDigest.IsZero() || event.result.digest.IsZero() ||
		event.authorizationRequest.IsZero() ||
		event.result.status == ProviderIntentReserved || event.result.status == ProviderIntentDispatched ||
		event.result.status == ProviderIntentReconciled || event.result.status == ProviderIntentAbandoned ||
		event.dispatchEpoch == 0 {
		return fmt.Errorf("provider result event requires canonical dispatched outcome and epoch")
	}
	canonical, err := canonicalProviderResult(event.result)
	if err != nil || DigestBytes(canonical) != event.result.digest {
		return fmt.Errorf("provider result event digest mismatch")
	}
	return nil
}

func (event ProviderResultRecordedJournalEvent) Result() ProviderResult { return event.result }

type ProviderReconciliation struct {
	intentID        ID
	intentDigest    Digest
	priorStatus     ProviderIntentStatus
	idempotencyKey  Digest
	provider        ID
	observation     ProviderReconciliationObservation
	effectApplied   bool
	remoteHead      GitObjectID
	pullRequest     PullRequestIdentity
	pullRequestHead GitObjectID
	mergeCommit     GitObjectID
	finalBaseHead   GitObjectID
	digest          Digest
}

func newProviderReconciliation(
	intent ProviderIntent,
	priorStatus ProviderIntentStatus,
	provider ID,
	observation ProviderReconciliationObservation,
) (ProviderReconciliation, error) {
	if !priorStatus.needsReconciliation() || provider.IsZero() || observation.digest.IsZero() ||
		observation.disposition == ProviderEffectUnknown {
		return ProviderReconciliation{}, fmt.Errorf("provider reconciliation requires unresolved intent and definitive observation")
	}
	result := ProviderReconciliation{
		intentID: intent.intentID, intentDigest: intent.digest, priorStatus: priorStatus,
		idempotencyKey: intent.idempotencyKey, provider: provider, observation: observation,
		effectApplied: observation.disposition == ProviderEffectApplied,
	}
	if result.effectApplied {
		switch intent.kind {
		case ProviderIntentPush:
			if observation.remoteHead != intent.head {
				return ProviderReconciliation{}, fmt.Errorf("reconciled push does not establish the intended remote head")
			}
			result.remoteHead = observation.remoteHead
		case ProviderIntentOpenPullRequest:
			if observation.pullRequestNumber == 0 || observation.pullRequestHead != intent.head {
				return ProviderReconciliation{}, fmt.Errorf("reconciled pull request does not establish the intended head and identity")
			}
			identity, err := newPullRequestIdentity(provider, intent.scope.repository, observation.pullRequestNumber)
			if err != nil {
				return ProviderReconciliation{}, err
			}
			result.pullRequest, result.pullRequestHead = identity, observation.pullRequestHead
		case ProviderIntentMerge:
			if observation.mergeCommit.IsZero() || observation.finalBaseHead != observation.mergeCommit {
				return ProviderReconciliation{}, fmt.Errorf("reconciled merge does not establish merge commit as final base head")
			}
			result.mergeCommit, result.finalBaseHead = observation.mergeCommit, observation.finalBaseHead
		}
	}
	content, err := canonicalProviderReconciliation(result)
	if err != nil {
		return ProviderReconciliation{}, err
	}
	result.digest = DigestBytes(content)
	return result, nil
}

func canonicalProviderReconciliation(result ProviderReconciliation) ([]byte, error) {
	type canonical struct {
		SchemaVersion   int                          `json:"schema_version"`
		IntentID        string                       `json:"intent_id"`
		IntentDigest    string                       `json:"intent_digest"`
		PriorStatus     ProviderIntentStatus         `json:"prior_status"`
		IdempotencyKey  string                       `json:"idempotency_key"`
		Provider        string                       `json:"provider"`
		Observation     string                       `json:"observation_digest"`
		EffectApplied   bool                         `json:"effect_applied"`
		RemoteHead      string                       `json:"remote_head,omitempty"`
		PullRequest     *controlPlanePullRequestWire `json:"pull_request,omitempty"`
		PullRequestHead string                       `json:"pull_request_head,omitempty"`
		MergeCommit     string                       `json:"merge_commit,omitempty"`
		FinalBaseHead   string                       `json:"final_base_head,omitempty"`
	}
	wire := canonical{
		SchemaVersion: JournalSchemaVersion, IntentID: result.intentID.String(),
		IntentDigest: result.intentDigest.String(), PriorStatus: result.priorStatus,
		IdempotencyKey: result.idempotencyKey.String(), Provider: result.provider.String(),
		Observation: result.observation.digest.String(), EffectApplied: result.effectApplied,
		RemoteHead: result.remoteHead.String(), PullRequestHead: result.pullRequestHead.String(),
		MergeCommit: result.mergeCommit.String(), FinalBaseHead: result.finalBaseHead.String(),
	}
	if !result.pullRequest.IsZero() {
		wire.PullRequest = &controlPlanePullRequestWire{
			Provider: result.pullRequest.provider.String(), Repository: result.pullRequest.repository.String(),
			Number: result.pullRequest.number,
		}
	}
	return json.Marshal(wire)
}

func (result ProviderReconciliation) Digest() Digest      { return result.digest }
func (result ProviderReconciliation) EffectApplied() bool { return result.effectApplied }
func (result ProviderReconciliation) PullRequest() (PullRequestIdentity, bool) {
	return result.pullRequest, !result.pullRequest.IsZero()
}

type ProviderIntentReconciledJournalEvent struct {
	workspaceID          ID
	generation           Digest
	reconciliation       ProviderReconciliation
	authorizationRequest Digest
	dispatchEpoch        uint64
}

func NewProviderIntentReconciledJournalEvent(
	workspaceID ID,
	generation Digest,
	reconciliation ProviderReconciliation,
	authorizationRequest Digest,
	dispatchEpoch uint64,
) (ProviderIntentReconciledJournalEvent, error) {
	event := ProviderIntentReconciledJournalEvent{
		workspaceID: workspaceID, generation: generation,
		reconciliation: reconciliation, authorizationRequest: authorizationRequest,
		dispatchEpoch: dispatchEpoch,
	}
	if err := event.validate(); err != nil {
		return ProviderIntentReconciledJournalEvent{}, err
	}
	return event, nil
}

func (ProviderIntentReconciledJournalEvent) isWorkspaceJournalEvent() {}
func (ProviderIntentReconciledJournalEvent) eventType() JournalEventType {
	return JournalEventProviderIntentReconciled
}
func (event ProviderIntentReconciledJournalEvent) boundGeneration() Digest { return event.generation }
func (event ProviderIntentReconciledJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.reconciliation.intentID.IsZero() || event.reconciliation.intentDigest.IsZero() ||
		event.reconciliation.digest.IsZero() || event.authorizationRequest.IsZero() || event.dispatchEpoch == 0 {
		return fmt.Errorf("provider reconciliation event requires exact intent, result, and dispatch epoch")
	}
	canonical, err := canonicalProviderReconciliation(event.reconciliation)
	if err != nil || DigestBytes(canonical) != event.reconciliation.digest {
		return fmt.Errorf("provider reconciliation event digest mismatch")
	}
	return nil
}

type ProviderCompletionVerifiedJournalEvent struct {
	workspaceID ID
	generation  Digest
	receipt     ProviderCompletionReceipt
}

func NewProviderCompletionVerifiedJournalEvent(
	workspaceID ID,
	generation Digest,
	receipt ProviderCompletionReceipt,
) (ProviderCompletionVerifiedJournalEvent, error) {
	event := ProviderCompletionVerifiedJournalEvent{workspaceID: workspaceID, generation: generation, receipt: receipt}
	if err := event.validate(); err != nil {
		return ProviderCompletionVerifiedJournalEvent{}, err
	}
	return event, nil
}

func (ProviderCompletionVerifiedJournalEvent) isWorkspaceJournalEvent() {}
func (ProviderCompletionVerifiedJournalEvent) eventType() JournalEventType {
	return JournalEventProviderCompletionVerified
}
func (event ProviderCompletionVerifiedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ProviderCompletionVerifiedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.receipt.digest.IsZero() ||
		event.receipt.workspaceID != event.workspaceID || event.receipt.generation != event.generation {
		return fmt.Errorf("provider completion event requires canonical receipt for exact workspace generation")
	}
	canonical, err := canonicalProviderCompletionReceipt(event.receipt)
	if err != nil || DigestBytes(canonical) != event.receipt.digest {
		return fmt.Errorf("provider completion event receipt digest mismatch")
	}
	return nil
}

func isProviderJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case ProviderIntentReservedJournalEvent, ProviderIntentAbandonedJournalEvent,
		ProviderMergePreflightRecordedJournalEvent, ProviderIntentDispatchedJournalEvent,
		ProviderResultRecordedJournalEvent, ProviderIntentReconciledJournalEvent,
		ProviderCompletionVerifiedJournalEvent:
		return true
	default:
		return false
	}
}

func ProviderIntentJournalResource(intentID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceProviderIntent, "intent/"+intentID.String())
	return resource
}

func ProviderQueueJournalResource(intentID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceQueueEntry, "provider/"+intentID.String())
	return resource
}

func ProviderResultJournalResource(result Digest) JournalResource {
	resource, _ := NewJournalResource(JournalResourceEvidence, "provider-result/"+result.String())
	return resource
}

func ProviderCompletionReceiptJournalResource(receipt Digest) JournalResource {
	resource, _ := NewJournalResource(JournalResourceEvidence, "provider-completion/"+receipt.String())
	return resource
}

func providerJournalEventResources(event WorkspaceJournalEvent) ([]JournalResource, []JournalResource, bool) {
	switch event := event.(type) {
	case ProviderIntentReservedJournalEvent:
		reads := []JournalResource{
			WorkspaceJournalResource(event.workspaceID), GenerationJournalResource(event.generation),
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationGrantJournalResource(event.reservation.grantID),
			ProviderIntentJournalResource(event.intent.intentID),
		}
		return reads, []JournalResource{ProviderIntentJournalResource(event.intent.intentID)}, true
	case ProviderIntentAbandonedJournalEvent:
		resources := []JournalResource{
			WorkspaceJournalResource(event.workspaceID), GenerationJournalResource(event.generation),
			ProviderIntentJournalResource(event.intentID),
		}
		return resources, []JournalResource{ProviderIntentJournalResource(event.intentID)}, true
	case ProviderMergePreflightRecordedJournalEvent:
		resources := []JournalResource{
			WorkspaceJournalResource(event.workspaceID), GenerationJournalResource(event.generation),
			ProviderIntentJournalResource(event.preflight.intentID),
			ProviderResultJournalResource(event.preflight.digest),
		}
		return resources, []JournalResource{
			ProviderIntentJournalResource(event.preflight.intentID),
			ProviderResultJournalResource(event.preflight.digest),
		}, true
	case ProviderIntentDispatchedJournalEvent:
		reads := []JournalResource{
			WorkspaceJournalResource(event.workspaceID), GenerationJournalResource(event.generation),
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationGrantJournalResource(event.effect.capability.grantID),
			AuthorizationEffectJournalResource(event.intentID),
			ProviderIntentJournalResource(event.intentID), ProviderQueueJournalResource(event.intentID),
		}
		writes := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID), AuthorizationEffectJournalResource(event.intentID),
			ProviderIntentJournalResource(event.intentID), ProviderQueueJournalResource(event.intentID),
		}
		return reads, writes, true
	case ProviderResultRecordedJournalEvent:
		reads := []JournalResource{
			WorkspaceJournalResource(event.workspaceID), GenerationJournalResource(event.generation),
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationEffectJournalResource(event.result.intentID), ProviderIntentJournalResource(event.result.intentID),
			ProviderResultJournalResource(event.result.digest),
		}
		writes := []JournalResource{
			ProviderIntentJournalResource(event.result.intentID), ProviderQueueJournalResource(event.result.intentID),
			ProviderResultJournalResource(event.result.digest),
		}
		if event.result.status.terminal() {
			writes = append(writes, AuthorizationEpochJournalResource(event.workspaceID), AuthorizationEffectJournalResource(event.result.intentID))
		}
		return reads, writes, true
	case ProviderIntentReconciledJournalEvent:
		result := event.reconciliation
		reads := []JournalResource{
			WorkspaceJournalResource(event.workspaceID), GenerationJournalResource(event.generation),
			AuthorizationEpochJournalResource(event.workspaceID), AuthorizationEffectJournalResource(result.intentID),
			ProviderIntentJournalResource(result.intentID), ProviderResultJournalResource(result.digest),
		}
		writes := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID), AuthorizationEffectJournalResource(result.intentID),
			ProviderIntentJournalResource(result.intentID), ProviderQueueJournalResource(result.intentID),
			ProviderResultJournalResource(result.digest),
		}
		return reads, writes, true
	case ProviderCompletionVerifiedJournalEvent:
		resources := []JournalResource{
			WorkspaceJournalResource(event.workspaceID), GenerationJournalResource(event.generation),
			ProviderCompletionReceiptJournalResource(event.receipt.digest),
		}
		return resources, []JournalResource{ProviderCompletionReceiptJournalResource(event.receipt.digest)}, true
	default:
		return nil, nil, false
	}
}

func cloneProviderJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case ProviderIntentReservedJournalEvent:
		return value
	case ProviderIntentAbandonedJournalEvent:
		return value
	case ProviderMergePreflightRecordedJournalEvent:
		value.preflight.requiredChecks = append([]ProviderCheckState(nil), value.preflight.requiredChecks...)
		value.preflight.requiredReviews = append([]ProviderReviewState(nil), value.preflight.requiredReviews...)
		return value
	case ProviderIntentDispatchedJournalEvent:
		return value
	case ProviderResultRecordedJournalEvent:
		return value
	case ProviderIntentReconciledJournalEvent:
		return value
	case ProviderCompletionVerifiedJournalEvent:
		value.receipt = cloneProviderCompletionReceipt(value.receipt)
		return value
	default:
		return nil
	}
}
