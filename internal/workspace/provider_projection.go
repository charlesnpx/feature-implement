package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

type ProviderIntentProjection struct {
	intent               ProviderIntent
	status               ProviderIntentStatus
	reservationRecord    uint64
	dispatchRecord       uint64
	dispatchRecordHash   Digest
	dispatchEpoch        uint64
	grantID              Digest
	preflight            ProviderMergePreflight
	preflightRecord      uint64
	result               ProviderResult
	resultRecord         uint64
	reconciliation       ProviderReconciliation
	reconciliationRecord uint64
}

func (projection ProviderIntentProjection) Intent() ProviderIntent       { return projection.intent }
func (projection ProviderIntentProjection) Status() ProviderIntentStatus { return projection.status }
func (projection ProviderIntentProjection) ReservationRecord() uint64 {
	return projection.reservationRecord
}
func (projection ProviderIntentProjection) DispatchRecord() uint64 { return projection.dispatchRecord }
func (projection ProviderIntentProjection) DispatchRecordHash() Digest {
	return projection.dispatchRecordHash
}
func (projection ProviderIntentProjection) DispatchEpoch() uint64 { return projection.dispatchEpoch }
func (projection ProviderIntentProjection) GrantID() Digest       { return projection.grantID }
func (projection ProviderIntentProjection) MergePreflight() (ProviderMergePreflight, bool) {
	preflight := projection.preflight
	preflight.requiredChecks = append([]ProviderCheckState(nil), preflight.requiredChecks...)
	preflight.requiredReviews = append([]ProviderReviewState(nil), preflight.requiredReviews...)
	return preflight, !preflight.digest.IsZero()
}
func (projection ProviderIntentProjection) Result() (ProviderResult, bool) {
	return projection.result, !projection.result.digest.IsZero()
}
func (projection ProviderIntentProjection) Reconciliation() (ProviderReconciliation, bool) {
	return projection.reconciliation, !projection.reconciliation.digest.IsZero()
}
func (projection ProviderIntentProjection) Resolved() bool { return projection.status.terminal() }
func (projection ProviderIntentProjection) NeedsReconciliation() bool {
	return projection.status.needsReconciliation()
}

type ProviderRuntimeProjection struct {
	initialized        bool
	workspaceID        ID
	activeGeneration   Digest
	provider           ProviderIdentity
	intents            []ProviderIntentProjection
	completionReceipts []ProviderCompletionReceipt
}

func (projection ProviderRuntimeProjection) WorkspaceID() ID { return projection.workspaceID }
func (projection ProviderRuntimeProjection) ActiveGeneration() Digest {
	return projection.activeGeneration
}
func (projection ProviderRuntimeProjection) Provider() ProviderIdentity { return projection.provider }
func (projection ProviderRuntimeProjection) Intents() []ProviderIntentProjection {
	return append([]ProviderIntentProjection(nil), projection.intents...)
}
func (projection ProviderRuntimeProjection) Intent(intentID ID) (ProviderIntentProjection, bool) {
	for _, intent := range projection.intents {
		if intent.intent.intentID == intentID {
			return intent, true
		}
	}
	return ProviderIntentProjection{}, false
}
func (projection ProviderRuntimeProjection) PullRequestForAttempt(attemptID ID) (PullRequestIdentity, bool) {
	identity, exists, err := providerPullRequestForAttempt(projection, attemptID)
	return identity, exists && err == nil
}

// PullRequest is retained as a convenience for runtimes containing exactly
// one attempt with a provider-derived pull request. Callers that know the
// attempt must use PullRequestForAttempt so identities never bleed between
// serial merge units in the same generation.
func (projection ProviderRuntimeProjection) PullRequest() (PullRequestIdentity, bool) {
	var attemptID ID
	for _, intent := range projection.intents {
		if intent.intent.kind != ProviderIntentOpenPullRequest {
			continue
		}
		identity, applied := providerPullRequestIdentity(intent)
		if !applied {
			continue
		}
		if attemptID.IsZero() {
			attemptID = intent.intent.scope.attemptID
		} else if attemptID != intent.intent.scope.attemptID {
			return PullRequestIdentity{}, false
		}
		if identity.IsZero() {
			return PullRequestIdentity{}, false
		}
	}
	if attemptID.IsZero() {
		return PullRequestIdentity{}, false
	}
	return projection.PullRequestForAttempt(attemptID)
}
func (projection ProviderRuntimeProjection) CompletionReceipts() []ProviderCompletionReceipt {
	result := make([]ProviderCompletionReceipt, len(projection.completionReceipts))
	for index := range projection.completionReceipts {
		result[index] = cloneProviderCompletionReceipt(projection.completionReceipts[index])
	}
	return result
}

func RebuildProviderRuntime(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
) (ProviderRuntimeProjection, error) {
	projection, err := RebuildProjection(
		snapshot,
		ProviderRuntimeProjection{},
		func(current ProviderRuntimeProjection, record JournalRecord) (ProviderRuntimeProjection, error) {
			return reduceProviderRuntime(definition, current, record)
		},
	)
	if err != nil {
		return ProviderRuntimeProjection{}, err
	}
	if !projection.initialized || projection.workspaceID != definition.workspace.id ||
		projection.activeGeneration != definition.generation ||
		projection.provider != definition.workspace.provider {
		return ProviderRuntimeProjection{}, fmt.Errorf("provider projection does not match the active effective definition")
	}
	core, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return ProviderRuntimeProjection{}, err
	}
	for _, projected := range projection.intents {
		attempt, exists := core.Attempt(projected.intent.scope.attemptID)
		if !exists || attempt.mergeUnit != projected.intent.scope.mergeUnit ||
			attempt.generation != projected.intent.scope.generation ||
			attempt.repository != projected.intent.scope.repository ||
			attempt.serialSegment != projected.intent.scope.serialSegment {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider intent %s does not match its durable attempt", projected.intent.intentID)
		}
	}
	return projection, nil
}

func VerifyProviderRuntimeConformance(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
) (Digest, error) {
	return VerifyReplayConformance(
		snapshot,
		func() ProviderRuntimeProjection { return ProviderRuntimeProjection{} },
		func(current ProviderRuntimeProjection, record JournalRecord) (ProviderRuntimeProjection, error) {
			return reduceProviderRuntime(definition, current, record)
		},
		canonicalProviderRuntime,
		func(state ProviderRuntimeProjection) Digest { return state.activeGeneration },
		definition.generation,
	)
}

func reduceProviderRuntime(
	definition EffectiveWorkspaceDefinition,
	current ProviderRuntimeProjection,
	record JournalRecord,
) (ProviderRuntimeProjection, error) {
	next := cloneProviderRuntime(current)
	switch event := record.event.(type) {
	case WorkspaceInitializedJournalEvent:
		if current.initialized || event.workspaceID != definition.workspace.id {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider projection has invalid workspace initialization")
		}
		next.initialized, next.workspaceID, next.activeGeneration = true, event.workspaceID, event.generation
		next.provider = definition.workspace.provider
	case ProviderIntentReservedJournalEvent:
		if !current.initialized || event.workspaceID != current.workspaceID ||
			event.generation != current.activeGeneration || event.intent.scope.generation != current.activeGeneration {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider intent reservation has stale workspace generation")
		}
		if event.planning.snapshot.journalHead != record.previousHash ||
			event.reservation.snapshot.journalHead != record.previousHash {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider intent reservation authorization is bound to a stale journal head")
		}
		if err := validateProviderAuthorizationRevision(record, event.workspaceID, event.reservation); err != nil {
			return ProviderRuntimeProjection{}, err
		}
		if existing, exists := current.Intent(event.intent.intentID); exists {
			index := providerIntentIndex(current.intents, event.intent.intentID)
			retryable := existing.status == ProviderIntentFailedBeforeEffect ||
				existing.status == ProviderIntentReconciled && !existing.reconciliation.effectApplied
			if !retryable || existing.intent.digest != event.intent.digest {
				return ProviderRuntimeProjection{}, fmt.Errorf("provider intent %s cannot be re-reserved", event.intent.intentID)
			}
			next.intents[index] = ProviderIntentProjection{
				intent: event.intent, status: ProviderIntentReserved, reservationRecord: record.sequence,
				grantID: event.reservation.grantID,
			}
			break
		}
		next.intents = append(next.intents, ProviderIntentProjection{
			intent: event.intent, status: ProviderIntentReserved, reservationRecord: record.sequence,
			grantID: event.reservation.grantID,
		})
	case ProviderIntentAbandonedJournalEvent:
		index := providerIntentIndex(current.intents, event.intentID)
		if index < 0 || current.intents[index].status != ProviderIntentReserved ||
			current.intents[index].intent.digest != event.intentDigest {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider abandonment requires the exact reserved intent")
		}
		next.intents[index].status = ProviderIntentAbandoned
	case ProviderMergePreflightRecordedJournalEvent:
		preflight := event.preflight
		index := providerIntentIndex(current.intents, preflight.intentID)
		if index < 0 || current.intents[index].status != ProviderIntentReserved ||
			current.intents[index].intent.kind != ProviderIntentMerge ||
			current.intents[index].intent.digest != preflight.intentDigest {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider merge preflight requires the exact reserved merge intent")
		}
		next.intents[index].preflight = preflight
		next.intents[index].preflight.requiredChecks = append([]ProviderCheckState(nil), preflight.requiredChecks...)
		next.intents[index].preflight.requiredReviews = append([]ProviderReviewState(nil), preflight.requiredReviews...)
		next.intents[index].preflightRecord = record.sequence
	case ProviderIntentDispatchedJournalEvent:
		index := providerIntentIndex(current.intents, event.intentID)
		if index < 0 || current.intents[index].status != ProviderIntentReserved ||
			current.intents[index].intent.digest != event.intentDigest {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider dispatch requires matching reserved intent")
		}
		if current.intents[index].intent.kind == ProviderIntentMerge && current.intents[index].preflight.digest.IsZero() {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider merge dispatch requires durable preflight evidence")
		}
		if event.effect.capability.snapshot.journalHead != record.previousHash {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider dispatch authorization is bound to a stale journal head")
		}
		if err := validateProviderAuthorizationRevision(record, event.workspaceID, event.effect.capability); err != nil {
			return ProviderRuntimeProjection{}, err
		}
		if err := validateProviderDispatchEvent(event, current.intents[index].intent); err != nil {
			return ProviderRuntimeProjection{}, err
		}
		next.intents[index].status = ProviderIntentDispatched
		next.intents[index].dispatchRecord = record.sequence
		next.intents[index].dispatchRecordHash = record.eventHash
		next.intents[index].dispatchEpoch = event.effect.capability.epoch
		next.intents[index].grantID = event.effect.capability.grantID
	case ProviderResultRecordedJournalEvent:
		index := providerIntentIndex(current.intents, event.result.intentID)
		if index < 0 || current.intents[index].status != ProviderIntentDispatched ||
			current.intents[index].intent.digest != event.result.intentDigest ||
			current.intents[index].intent.authorization.digest != event.authorizationRequest ||
			current.intents[index].dispatchEpoch != event.dispatchEpoch {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider result requires the exact dispatched intent and epoch")
		}
		if err := validateProviderResultAgainstIntent(event.result, current.intents[index].intent, current.provider.kind); err != nil {
			return ProviderRuntimeProjection{}, err
		}
		if event.result.kind == ProviderIntentOpenPullRequest && event.result.status == ProviderIntentSucceeded {
			existing, found, err := providerPullRequestForAttempt(
				current, current.intents[index].intent.scope.attemptID,
			)
			if err != nil {
				return ProviderRuntimeProjection{}, err
			}
			if found && existing != event.result.pullRequest {
				return ProviderRuntimeProjection{}, fmt.Errorf("provider result attempted to replace the durable pull request identity")
			}
		}
		next.intents[index].status = event.result.status
		next.intents[index].result = event.result
		next.intents[index].resultRecord = record.sequence
	case ProviderIntentReconciledJournalEvent:
		result := event.reconciliation
		index := providerIntentIndex(current.intents, result.intentID)
		if index < 0 || !current.intents[index].status.needsReconciliation() ||
			current.intents[index].status != result.priorStatus ||
			current.intents[index].intent.digest != result.intentDigest ||
			current.intents[index].intent.authorization.digest != event.authorizationRequest ||
			current.intents[index].dispatchEpoch != event.dispatchEpoch ||
			current.intents[index].intent.idempotencyKey != result.idempotencyKey ||
			current.provider.kind != result.provider {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider reconciliation does not match its unresolved dispatch")
		}
		if !result.pullRequest.IsZero() {
			existing, found, err := providerPullRequestForAttempt(
				current, current.intents[index].intent.scope.attemptID,
			)
			if err != nil {
				return ProviderRuntimeProjection{}, err
			}
			if found && existing != result.pullRequest {
				return ProviderRuntimeProjection{}, fmt.Errorf("provider reconciliation attempted to replace the durable pull request identity")
			}
		}
		next.intents[index].status = ProviderIntentReconciled
		next.intents[index].reconciliation = result
		next.intents[index].reconciliationRecord = record.sequence
	case ProviderCompletionVerifiedJournalEvent:
		if !current.initialized || event.workspaceID != current.workspaceID || event.generation != current.activeGeneration {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider completion receipt has stale workspace generation")
		}
		for _, receipt := range current.completionReceipts {
			if receipt.attemptID == event.receipt.attemptID {
				return ProviderRuntimeProjection{}, fmt.Errorf("attempt %s already has a provider completion receipt", receipt.attemptID)
			}
			if receipt.digest == event.receipt.digest {
				return ProviderRuntimeProjection{}, fmt.Errorf("provider completion receipt %s was replayed", receipt.digest)
			}
		}
		next.completionReceipts = append(next.completionReceipts, cloneProviderCompletionReceipt(event.receipt))
	default:
		if !current.initialized && record.sequence != 0 {
			return ProviderRuntimeProjection{}, fmt.Errorf("provider projection encountered %s before workspace initialization", record.EventType())
		}
	}
	sort.Slice(next.intents, func(i, j int) bool {
		return next.intents[i].intent.intentID.String() < next.intents[j].intent.intentID.String()
	})
	return next, nil
}

func validateProviderAuthorizationRevision(
	record JournalRecord,
	workspaceID ID,
	capability AuthorizationCapability,
) error {
	resource := AuthorizationEpochJournalResource(workspaceID)
	for _, read := range record.readSet {
		if read.resource == resource {
			if read.revision != capability.snapshot.authorizationRevision {
				return fmt.Errorf("provider authorization capability has a stale resource revision")
			}
			return nil
		}
	}
	return fmt.Errorf("provider event does not read the authorization epoch resource")
}

func validateProviderResultAgainstIntent(result ProviderResult, intent ProviderIntent, provider ID) error {
	if result.intentID != intent.intentID || result.intentDigest != intent.digest || result.kind != intent.kind ||
		result.idempotencyKey != intent.idempotencyKey || result.provider != provider {
		return fmt.Errorf("provider result does not match the dispatched typed intent")
	}
	if result.status == ProviderIntentSucceeded {
		switch intent.kind {
		case ProviderIntentPush:
			if result.remoteHead != intent.head {
				return fmt.Errorf("provider push result does not establish the intended head")
			}
		case ProviderIntentOpenPullRequest:
			if result.pullRequest.IsZero() || result.pullRequest.repository != intent.scope.repository ||
				result.pullRequest.provider != provider || result.pullRequestHead != intent.head {
				return fmt.Errorf("provider open-pull-request result does not establish the intended repository and head")
			}
		case ProviderIntentMerge:
			if result.mergeCommit.IsZero() || result.finalBaseHead != result.mergeCommit {
				return fmt.Errorf("provider merge result does not establish merge commit as final base head")
			}
		}
	}
	return nil
}

func providerIntentIndex(values []ProviderIntentProjection, intentID ID) int {
	for index, value := range values {
		if value.intent.intentID == intentID {
			return index
		}
	}
	return -1
}

func providerPullRequestIdentity(projected ProviderIntentProjection) (PullRequestIdentity, bool) {
	if projected.intent.kind != ProviderIntentOpenPullRequest {
		return PullRequestIdentity{}, false
	}
	if projected.status == ProviderIntentSucceeded && !projected.result.pullRequest.IsZero() {
		return projected.result.pullRequest, true
	}
	if projected.status == ProviderIntentReconciled && projected.reconciliation.effectApplied &&
		!projected.reconciliation.pullRequest.IsZero() {
		return projected.reconciliation.pullRequest, true
	}
	return PullRequestIdentity{}, false
}

func providerPullRequestForAttempt(
	projection ProviderRuntimeProjection,
	attemptID ID,
) (PullRequestIdentity, bool, error) {
	if attemptID.IsZero() {
		return PullRequestIdentity{}, false, fmt.Errorf("provider pull request lookup requires an attempt")
	}
	var identity PullRequestIdentity
	for _, projected := range projection.intents {
		if projected.intent.scope.attemptID != attemptID {
			continue
		}
		candidate, applied := providerPullRequestIdentity(projected)
		if !applied {
			continue
		}
		if identity.IsZero() {
			identity = candidate
			continue
		}
		if identity != candidate {
			return PullRequestIdentity{}, false, fmt.Errorf("attempt %s has conflicting provider-derived pull request identities", attemptID)
		}
	}
	return identity, !identity.IsZero(), nil
}

func providerAppliedOpenIntentForAttempt(
	projection ProviderRuntimeProjection,
	attemptID ID,
) (ProviderIntent, bool, error) {
	var open ProviderIntent
	for _, projected := range projection.intents {
		if projected.intent.scope.attemptID != attemptID || projected.intent.kind != ProviderIntentOpenPullRequest {
			continue
		}
		if _, applied := providerPullRequestIdentity(projected); !applied {
			continue
		}
		if !open.intentID.IsZero() {
			return ProviderIntent{}, false, fmt.Errorf("attempt %s has multiple applied open-pull-request intents", attemptID)
		}
		open = projected.intent
	}
	return open, !open.intentID.IsZero(), nil
}

func cloneProviderRuntime(projection ProviderRuntimeProjection) ProviderRuntimeProjection {
	projection.intents = append([]ProviderIntentProjection(nil), projection.intents...)
	for index := range projection.intents {
		projection.intents[index].preflight.requiredChecks = append(
			[]ProviderCheckState(nil), projection.intents[index].preflight.requiredChecks...,
		)
		projection.intents[index].preflight.requiredReviews = append(
			[]ProviderReviewState(nil), projection.intents[index].preflight.requiredReviews...,
		)
	}
	projection.completionReceipts = append([]ProviderCompletionReceipt(nil), projection.completionReceipts...)
	for index := range projection.completionReceipts {
		projection.completionReceipts[index] = cloneProviderCompletionReceipt(projection.completionReceipts[index])
	}
	return projection
}

func canonicalProviderRuntime(projection ProviderRuntimeProjection) ([]byte, error) {
	type intentJSON struct {
		IntentID        string               `json:"intent_id"`
		IntentDigest    string               `json:"intent_digest"`
		Generation      string               `json:"generation"`
		Status          ProviderIntentStatus `json:"status"`
		Reservation     uint64               `json:"reservation_record"`
		Dispatch        uint64               `json:"dispatch_record,omitempty"`
		DispatchHash    string               `json:"dispatch_record_hash,omitempty"`
		DispatchEpoch   uint64               `json:"dispatch_epoch,omitempty"`
		GrantID         string               `json:"grant_id"`
		Preflight       string               `json:"merge_preflight_digest,omitempty"`
		PreflightRecord uint64               `json:"merge_preflight_record,omitempty"`
		Result          string               `json:"result_digest,omitempty"`
		Reconciliation  string               `json:"reconciliation_digest,omitempty"`
	}
	type attemptPullRequestJSON struct {
		AttemptID   string                      `json:"attempt_id"`
		PullRequest controlPlanePullRequestWire `json:"pull_request"`
	}
	type projectionJSON struct {
		SchemaVersion      int                      `json:"schema_version"`
		WorkspaceID        string                   `json:"workspace_id"`
		ActiveGeneration   string                   `json:"active_generation"`
		ProviderKind       string                   `json:"provider_kind"`
		ProviderRepository string                   `json:"provider_repository"`
		PullRequests       []attemptPullRequestJSON `json:"pull_requests"`
		Intents            []intentJSON             `json:"intents"`
		CompletionReceipts []string                 `json:"completion_receipts"`
	}
	wire := projectionJSON{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: projection.workspaceID.String(),
		ActiveGeneration: projection.activeGeneration.String(), ProviderKind: projection.provider.kind.String(),
		ProviderRepository: projection.provider.repository,
		PullRequests:       make([]attemptPullRequestJSON, 0),
		Intents:            make([]intentJSON, 0, len(projection.intents)),
		CompletionReceipts: make([]string, 0, len(projection.completionReceipts)),
	}
	seenAttempts := make(map[ID]struct{})
	for _, projected := range projection.intents {
		attemptID := projected.intent.scope.attemptID
		if _, seen := seenAttempts[attemptID]; seen {
			continue
		}
		seenAttempts[attemptID] = struct{}{}
		identity, exists, err := providerPullRequestForAttempt(projection, attemptID)
		if err != nil {
			return nil, err
		}
		if exists {
			wire.PullRequests = append(wire.PullRequests, attemptPullRequestJSON{
				AttemptID: attemptID.String(),
				PullRequest: controlPlanePullRequestWire{
					Provider: identity.provider.String(), Repository: identity.repository.String(), Number: identity.number,
				},
			})
		}
	}
	sort.Slice(wire.PullRequests, func(i, j int) bool {
		return wire.PullRequests[i].AttemptID < wire.PullRequests[j].AttemptID
	})
	for _, intent := range projection.intents {
		wire.Intents = append(wire.Intents, intentJSON{
			IntentID: intent.intent.intentID.String(), IntentDigest: intent.intent.digest.String(),
			Generation: intent.intent.scope.generation.String(), Status: intent.status,
			Reservation: intent.reservationRecord, Dispatch: intent.dispatchRecord,
			DispatchHash: intent.dispatchRecordHash.String(), DispatchEpoch: intent.dispatchEpoch,
			GrantID: intent.grantID.String(), Result: intent.result.digest.String(),
			Preflight: intent.preflight.digest.String(), PreflightRecord: intent.preflightRecord,
			Reconciliation: intent.reconciliation.digest.String(),
		})
	}
	for _, receipt := range projection.completionReceipts {
		wire.CompletionReceipts = append(wire.CompletionReceipts, receipt.digest.String())
	}
	return json.Marshal(wire)
}
