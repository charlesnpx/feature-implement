package workspace

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type AuthorizationGrantRecordedJournalEvent struct {
	workspaceID ID
	generation  Digest
	grant       StandingGrant
}

func NewAuthorizationGrantRecordedJournalEvent(
	workspaceID ID,
	generation Digest,
	grant StandingGrant,
) (AuthorizationGrantRecordedJournalEvent, error) {
	event := AuthorizationGrantRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, grant: cloneStandingGrant(grant),
	}
	if err := event.validate(); err != nil {
		return AuthorizationGrantRecordedJournalEvent{}, err
	}
	return event, nil
}

func (AuthorizationGrantRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (AuthorizationGrantRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventAuthorizationGrantRecorded
}
func (event AuthorizationGrantRecordedJournalEvent) boundGeneration() Digest { return event.generation }
func (event AuthorizationGrantRecordedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.grant.grantID.IsZero() ||
		event.grant.requestDigest.IsZero() || event.grant.receiptDigest.IsZero() ||
		event.grant.scope.workspaceID != event.workspaceID || event.grant.scope.generation != event.generation ||
		event.grant.scope.Digest().IsZero() {
		return fmt.Errorf("authorization grant event requires generation-bound grant evidence")
	}
	if event.grant.parentGrantID.IsZero() {
		if !event.grant.priorDerivedGrantID.IsZero() ||
			!event.grant.providerObservationDigest.IsZero() || !event.grant.providerObservedHead.IsZero() ||
			!event.grant.scope.pullRequest.IsZero() ||
			event.grant.scope.Digest() != event.grant.requestDigest ||
			event.grant.grantID != event.grant.scope.Digest() {
			return fmt.Errorf("authorization grant event has invalid signed grant bindings")
		}
		return nil
	}
	derivedID, err := derivedStandingGrantID(
		event.grant.parentGrantID, event.grant.priorDerivedGrantID, event.grant.receiptDigest,
		event.grant.providerObservationDigest, event.grant.scope.pullRequest, event.grant.providerObservedHead,
	)
	if err != nil || derivedID != event.grant.grantID || event.grant.scope.pullRequest.IsZero() ||
		event.grant.providerObservedHead.IsZero() {
		return fmt.Errorf("authorization grant event has invalid provider-derived grant bindings")
	}
	return nil
}
func (event AuthorizationGrantRecordedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AuthorizationGrantRecordedJournalEvent) Generation() Digest { return event.generation }
func (event AuthorizationGrantRecordedJournalEvent) Grant() StandingGrant {
	return cloneStandingGrant(event.grant)
}

type AuthorizationRevokedJournalEvent struct {
	workspaceID ID
	generation  Digest
	revocation  AuthorizationRevocation
}

func NewAuthorizationRevokedJournalEvent(
	workspaceID ID,
	generation Digest,
	revocation AuthorizationRevocation,
) (AuthorizationRevokedJournalEvent, error) {
	event := AuthorizationRevokedJournalEvent{
		workspaceID: workspaceID, generation: generation, revocation: revocation,
	}
	if err := event.validate(); err != nil {
		return AuthorizationRevokedJournalEvent{}, err
	}
	return event, nil
}

func (AuthorizationRevokedJournalEvent) isWorkspaceJournalEvent() {}
func (AuthorizationRevokedJournalEvent) eventType() JournalEventType {
	return JournalEventAuthorizationRevoked
}
func (event AuthorizationRevokedJournalEvent) boundGeneration() Digest { return event.generation }
func (event AuthorizationRevokedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.revocation.digest.IsZero() ||
		event.revocation.receipt.IsZero() || event.revocation.workspaceID != event.workspaceID ||
		event.revocation.generation != event.generation {
		return fmt.Errorf("authorization revocation event requires signed generation-bound evidence")
	}
	return nil
}
func (event AuthorizationRevokedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AuthorizationRevokedJournalEvent) Generation() Digest { return event.generation }
func (event AuthorizationRevokedJournalEvent) Revocation() AuthorizationRevocation {
	return event.revocation
}

type AuthorizationSegmentCompletedJournalEvent struct {
	workspaceID ID
	generation  Digest
	segment     ID
	epoch       uint64
}

func NewAuthorizationSegmentCompletedJournalEvent(
	workspaceID ID,
	generation Digest,
	segment ID,
	epoch uint64,
) (AuthorizationSegmentCompletedJournalEvent, error) {
	event := AuthorizationSegmentCompletedJournalEvent{
		workspaceID: workspaceID, generation: generation, segment: segment, epoch: epoch,
	}
	if err := event.validate(); err != nil {
		return AuthorizationSegmentCompletedJournalEvent{}, err
	}
	return event, nil
}

func (AuthorizationSegmentCompletedJournalEvent) isWorkspaceJournalEvent() {}
func (AuthorizationSegmentCompletedJournalEvent) eventType() JournalEventType {
	return JournalEventAuthorizationSegmentCompleted
}
func (event AuthorizationSegmentCompletedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AuthorizationSegmentCompletedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.segment.IsZero() || event.epoch == 0 {
		return fmt.Errorf("authorization segment completion requires workspace, generation, segment, and epoch")
	}
	return nil
}
func (event AuthorizationSegmentCompletedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AuthorizationSegmentCompletedJournalEvent) Generation() Digest { return event.generation }
func (event AuthorizationSegmentCompletedJournalEvent) Segment() ID        { return event.segment }
func (event AuthorizationSegmentCompletedJournalEvent) Epoch() uint64      { return event.epoch }

type AuthorizationSafetyChangedJournalEvent struct {
	workspaceID   ID
	generation    Digest
	epoch         uint64
	safety        AuthorizationSafetyState
	requestDigest Digest
	receiptDigest Digest
}

func NewAuthorizationSafetyChangedJournalEvent(
	workspaceID ID,
	generation Digest,
	epoch uint64,
	safety AuthorizationSafetyState,
	requestDigest, receiptDigest Digest,
) (AuthorizationSafetyChangedJournalEvent, error) {
	event := AuthorizationSafetyChangedJournalEvent{
		workspaceID: workspaceID, generation: generation, epoch: epoch, safety: safety,
		requestDigest: requestDigest, receiptDigest: receiptDigest,
	}
	if err := event.validate(); err != nil {
		return AuthorizationSafetyChangedJournalEvent{}, err
	}
	return event, nil
}

func (AuthorizationSafetyChangedJournalEvent) isWorkspaceJournalEvent() {}
func (AuthorizationSafetyChangedJournalEvent) eventType() JournalEventType {
	return JournalEventAuthorizationSafetyChanged
}
func (event AuthorizationSafetyChangedJournalEvent) boundGeneration() Digest { return event.generation }
func (event AuthorizationSafetyChangedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.epoch == 0 ||
		event.requestDigest.IsZero() || event.receiptDigest.IsZero() {
		return fmt.Errorf("authorization safety change requires workspace, generation, epoch, request, and receipt")
	}
	return nil
}
func (event AuthorizationSafetyChangedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AuthorizationSafetyChangedJournalEvent) Generation() Digest { return event.generation }
func (event AuthorizationSafetyChangedJournalEvent) Epoch() uint64      { return event.epoch }
func (event AuthorizationSafetyChangedJournalEvent) Safety() AuthorizationSafetyState {
	return event.safety
}
func (event AuthorizationSafetyChangedJournalEvent) RequestDigest() Digest {
	return event.requestDigest
}
func (event AuthorizationSafetyChangedJournalEvent) ReceiptDigest() Digest {
	return event.receiptDigest
}

type AuthorizationEffectDispatchedJournalEvent struct {
	workspaceID ID
	generation  Digest
	effect      AuthorizationEffectDispatched
}

func NewAuthorizationEffectDispatchedJournalEvent(
	workspaceID ID,
	generation Digest,
	effect AuthorizationEffectDispatched,
) (AuthorizationEffectDispatchedJournalEvent, error) {
	event := AuthorizationEffectDispatchedJournalEvent{
		workspaceID: workspaceID, generation: generation, effect: effect,
	}
	if err := event.validate(); err != nil {
		return AuthorizationEffectDispatchedJournalEvent{}, err
	}
	return event, nil
}

func (AuthorizationEffectDispatchedJournalEvent) isWorkspaceJournalEvent() {}
func (AuthorizationEffectDispatchedJournalEvent) eventType() JournalEventType {
	return JournalEventAuthorizationEffectDispatched
}
func (event AuthorizationEffectDispatchedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AuthorizationEffectDispatchedJournalEvent) validate() error {
	capability := event.effect.capability
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.effect.effectID.IsZero() ||
		capability.grantID.IsZero() || capability.requestDigest.IsZero() || capability.stateDigest.IsZero() ||
		capability.digest.IsZero() || capability.snapshot.journalHead.IsZero() ||
		capability.checkpoint != AuthorizationBeforeDispatch || capability.epoch == 0 ||
		event.effect.dispatchedAt.IsZero() || event.effect.dispatchedAt != capability.evaluatedAt ||
		capabilityDigest(capability) != capability.digest {
		return fmt.Errorf("authorization dispatch event requires an exact durable pre-dispatch capability")
	}
	return nil
}
func (event AuthorizationEffectDispatchedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AuthorizationEffectDispatchedJournalEvent) Generation() Digest { return event.generation }
func (event AuthorizationEffectDispatchedJournalEvent) EffectID() ID       { return event.effect.effectID }
func (event AuthorizationEffectDispatchedJournalEvent) Capability() AuthorizationCapability {
	return event.effect.capability
}
func (event AuthorizationEffectDispatchedJournalEvent) DispatchedAt() time.Time {
	return event.effect.dispatchedAt
}

func AuthorizationEpochJournalResource(workspaceID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceAuthorization, workspaceID.String()+"/epoch")
	return resource
}

func AuthorizationGrantJournalResource(grantID Digest) JournalResource {
	resource, _ := NewJournalResource(JournalResourceApproval, "standing-grant/"+grantID.String())
	return resource
}

func AuthorizationReceiptJournalResource(receiptDigest Digest) JournalResource {
	resource, _ := NewJournalResource(JournalResourceControlReceipt, receiptDigest.String())
	return resource
}

func AuthorizationProviderObservationJournalResource(observationDigest Digest) JournalResource {
	resource, _ := NewJournalResource(JournalResourceEvidence, "provider-pr/"+observationDigest.String())
	return resource
}

func AuthorizationSegmentJournalResource(workspaceID, segment ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceSerialSegment, workspaceID.String()+"/"+segment.String())
	return resource
}

func AuthorizationSafetyJournalResource(workspaceID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceAuthorization, workspaceID.String()+"/safety")
	return resource
}

func AuthorizationEffectJournalResource(effectID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceProviderIntent, "authorization-dispatch/"+effectID.String())
	return resource
}

func isAuthorizationJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case AuthorizationGrantRecordedJournalEvent, AuthorizationRevokedJournalEvent,
		AuthorizationSegmentCompletedJournalEvent, AuthorizationSafetyChangedJournalEvent,
		AuthorizationEffectDispatchedJournalEvent:
		return true
	default:
		return false
	}
}

func authorizationJournalEventResources(event WorkspaceJournalEvent) ([]JournalResource, []JournalResource, bool) {
	switch event := event.(type) {
	case AuthorizationGrantRecordedJournalEvent:
		reads := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationGrantJournalResource(event.grant.grantID),
		}
		writes := append([]JournalResource(nil), reads...)
		if event.grant.parentGrantID.IsZero() {
			receipt := AuthorizationReceiptJournalResource(event.grant.receiptDigest)
			reads = append(reads, receipt)
			writes = append(writes, receipt)
		} else {
			reads = append(reads,
				AuthorizationGrantJournalResource(event.grant.parentGrantID),
				AuthorizationReceiptJournalResource(event.grant.receiptDigest),
				AuthorizationProviderObservationJournalResource(event.grant.providerObservationDigest),
			)
			if !event.grant.priorDerivedGrantID.IsZero() {
				reads = append(reads, AuthorizationGrantJournalResource(event.grant.priorDerivedGrantID))
			}
			writes = append(writes, AuthorizationProviderObservationJournalResource(event.grant.providerObservationDigest))
		}
		return reads, writes, true
	case AuthorizationRevokedJournalEvent:
		resources := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationReceiptJournalResource(event.revocation.receipt),
		}
		if !event.revocation.targetGrant.IsZero() {
			resources = append(resources, AuthorizationGrantJournalResource(event.revocation.targetGrant))
		}
		return resources, resources, true
	case AuthorizationSegmentCompletedJournalEvent:
		resources := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationSegmentJournalResource(event.workspaceID, event.segment),
		}
		return resources, resources, true
	case AuthorizationSafetyChangedJournalEvent:
		resources := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationSafetyJournalResource(event.workspaceID),
			AuthorizationReceiptJournalResource(event.receiptDigest),
		}
		return resources, resources, true
	case AuthorizationEffectDispatchedJournalEvent:
		reads := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationGrantJournalResource(event.effect.capability.grantID),
			AuthorizationEffectJournalResource(event.effect.effectID),
		}
		writes := []JournalResource{
			AuthorizationEpochJournalResource(event.workspaceID),
			AuthorizationEffectJournalResource(event.effect.effectID),
		}
		return reads, writes, true
	default:
		return nil, nil, false
	}
}

func cloneAuthorizationJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case AuthorizationGrantRecordedJournalEvent:
		value.grant = cloneStandingGrant(value.grant)
		return value
	case AuthorizationRevokedJournalEvent:
		return value
	case AuthorizationSegmentCompletedJournalEvent:
		return value
	case AuthorizationSafetyChangedJournalEvent:
		return value
	case AuthorizationEffectDispatchedJournalEvent:
		return value
	default:
		return nil
	}
}

type AuthorizationRuntimeProjection struct {
	initialized       bool
	state             AuthorizationState
	receipts          []Digest
	pendingCandidates []Digest
}

func (projection AuthorizationRuntimeProjection) State() AuthorizationState {
	return cloneAuthorizationState(projection.state)
}
func (projection AuthorizationRuntimeProjection) ReceiptDigests() []Digest {
	return append([]Digest(nil), projection.receipts...)
}
func (projection AuthorizationRuntimeProjection) PendingCandidateGenerations() []Digest {
	return append([]Digest(nil), projection.pendingCandidates...)
}

func RebuildAuthorizationRuntime(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
) (AuthorizationRuntimeProjection, error) {
	projection, err := RebuildProjection(
		snapshot,
		AuthorizationRuntimeProjection{},
		func(current AuthorizationRuntimeProjection, record JournalRecord) (AuthorizationRuntimeProjection, error) {
			return reduceAuthorizationRuntime(definition, current, record)
		},
	)
	if err != nil {
		return AuthorizationRuntimeProjection{}, err
	}
	if !projection.initialized || projection.state.workspaceID != definition.workspace.id ||
		projection.state.repository != definition.workspace.repository ||
		projection.state.remote != definition.workspace.remote || projection.state.generation != definition.generation {
		return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization projection does not match the active effective definition")
	}
	return projection, nil
}

func reduceAuthorizationRuntime(
	definition EffectiveWorkspaceDefinition,
	current AuthorizationRuntimeProjection,
	record JournalRecord,
) (AuthorizationRuntimeProjection, error) {
	next := cloneAuthorizationRuntime(current)
	switch event := record.event.(type) {
	case WorkspaceInitializedJournalEvent:
		if current.initialized || event.workspaceID != definition.workspace.id {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization projection has invalid workspace initialization")
		}
		state, err := NewAuthorizationState(
			event.workspaceID, definition.workspace.repository, definition.workspace.remote, event.generation, 1,
		)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.initialized, next.state = true, state
	case AuthorizationGrantRecordedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID || event.generation != current.state.generation {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization grant journal event has stale bindings")
		}
		if event.grant.parentGrantID.IsZero() {
			if containsDigest(current.receipts, event.grant.receiptDigest) {
				return AuthorizationRuntimeProjection{}, fmt.Errorf("control-plane receipt %s was replayed", event.grant.receiptDigest)
			}
		} else if !containsDigest(current.receipts, event.grant.receiptDigest) {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("derived standing grant references unknown receipt %s", event.grant.receiptDigest)
		}
		domainEvent, _ := NewAuthorizationGrantRecorded(event.grant)
		state, err := ReduceAuthorization(current.state, domainEvent)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.state = state
		if event.grant.parentGrantID.IsZero() {
			next.receipts = append(next.receipts, event.grant.receiptDigest)
		}
	case AuthorizationSafetyChangedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.generation != current.state.generation || event.epoch != current.state.epoch {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization safety journal event has stale bindings")
		}
		expectedRequest, err := authorizationSafetyChangeRequestDigest(current.state, current.pendingCandidates, event.safety)
		if err != nil || expectedRequest != event.requestDigest {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization safety journal event has invalid prior-state bindings")
		}
		if containsDigest(current.receipts, event.receiptDigest) {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("control-plane receipt %s was replayed", event.receiptDigest)
		}
		domainEvent := NewAuthorizationSafetyChanged(event.safety)
		state, err := ReduceAuthorization(current.state, domainEvent)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.state = state
		next.receipts = append(next.receipts, event.receiptDigest)
	case AuthorizationEffectDispatchedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.generation != current.state.generation {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization dispatch journal event has stale bindings")
		}
		capability := event.effect.capability
		if capability.snapshot.journalHead != record.previousHash {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization dispatch capability is bound to a stale journal head")
		}
		epochResource := AuthorizationEpochJournalResource(event.workspaceID)
		var revision uint64
		var found bool
		for _, read := range record.readSet {
			if read.resource == epochResource {
				revision, found = read.revision, true
				break
			}
		}
		if !found || capability.snapshot.authorizationRevision != revision {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization dispatch capability has a stale resource revision")
		}
		state, err := ReduceAuthorization(current.state, event.effect)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.state = state
	case ProviderIntentDispatchedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.generation != current.state.generation {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("provider dispatch authorization has stale bindings")
		}
		capability := event.effect.capability
		if capability.snapshot.journalHead != record.previousHash {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("provider dispatch authorization is bound to a stale journal head")
		}
		epochResource := AuthorizationEpochJournalResource(event.workspaceID)
		var revision uint64
		var found bool
		for _, read := range record.readSet {
			if read.resource == epochResource {
				revision, found = read.revision, true
				break
			}
		}
		if !found || capability.snapshot.authorizationRevision != revision {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("provider dispatch authorization has a stale resource revision")
		}
		state, err := ReduceAuthorization(current.state, event.effect)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.state = state
	case ProviderResultRecordedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.generation != current.state.generation || event.dispatchEpoch == 0 {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("provider result authorization has stale bindings")
		}
		if event.result.status.terminal() {
			resolved, err := NewAuthorizationEffectResolved(
				event.result.intentID, event.authorizationRequest, event.result.digest, event.dispatchEpoch,
			)
			if err != nil {
				return AuthorizationRuntimeProjection{}, err
			}
			state, err := ReduceAuthorization(current.state, resolved)
			if err != nil {
				return AuthorizationRuntimeProjection{}, err
			}
			next.state = state
		}
	case ProviderIntentReconciledJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.generation != current.state.generation || event.dispatchEpoch == 0 {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("provider reconciliation authorization has stale bindings")
		}
		resolved, err := NewAuthorizationEffectResolved(
			event.reconciliation.intentID, event.authorizationRequest,
			event.reconciliation.digest, event.dispatchEpoch,
		)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		state, err := ReduceAuthorization(current.state, resolved)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.state = state
	case AuthorizationRevokedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID || event.generation != current.state.generation {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization revocation journal event has stale bindings")
		}
		if containsDigest(current.receipts, event.revocation.receipt) {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("control-plane receipt %s was replayed", event.revocation.receipt)
		}
		domainEvent, _ := NewAuthorizationRevoked(event.revocation)
		state, err := ReduceAuthorization(current.state, domainEvent)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.state = state
		next.receipts = append(next.receipts, event.revocation.receipt)
	case AuthorizationSegmentCompletedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.generation != current.state.generation || event.epoch != current.state.epoch {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization segment completion has stale bindings")
		}
		domainEvent, _ := NewAuthorizationSegmentCompleted(event.segment)
		state, err := ReduceAuthorization(current.state, domainEvent)
		if err != nil {
			return AuthorizationRuntimeProjection{}, err
		}
		next.state = state
	default:
		if !current.initialized && record.sequence != 0 {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization projection encountered %s before workspace initialization", record.EventType())
		}
	}
	sort.Slice(next.receipts, func(i, j int) bool { return next.receipts[i].String() < next.receipts[j].String() })
	return next, nil
}

func RecordStandingGrant(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	verifier ControlPlaneVerifierPort,
	scope StandingGrantScope,
	receipt ControlPlaneReceiptV2,
	occurredAt time.Time,
) (StandingGrant, JournalRecord, error) {
	if journal == nil || verifier == nil || occurredAt.IsZero() {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("record standing grant requires journal, verifier, and occurrence time")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	expectedGrantID := scope.Digest()
	for _, existing := range projection.state.grants {
		if existing.grantID == expectedGrantID {
			if existing.receiptDigest != receipt.ReceiptDigest() {
				return StandingGrant{}, JournalRecord{}, fmt.Errorf("standing grant conflicts with durable grant %s", expectedGrantID)
			}
			return cloneStandingGrant(existing), JournalRecord{}, nil
		}
	}
	grant, err := VerifyStandingGrant(ctx, verifier, scope, receipt)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	domainEvent, _ := NewAuthorizationGrantRecorded(grant)
	if _, err := ReduceAuthorization(projection.state, domainEvent); err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	event, err := NewAuthorizationGrantRecordedJournalEvent(definition.workspace.id, definition.generation, grant)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	record, err := appendAuthorizationJournalEvent(journal, snapshot, event, occurredAt)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	return grant, record, nil
}

// RecordDerivedStandingGrantPullRequest is the only exported path that turns
// a provider observation into a pull-request-bound grant. It verifies the
// exact provider result, enforces one durable child per parent, and records the
// child through the authorization epoch CAS.
func RecordDerivedStandingGrantPullRequest(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	verifier ProviderPullRequestVerifierPort,
	parentGrantID Digest,
	observation ProviderPullRequestObservation,
	occurredAt time.Time,
) (StandingGrant, JournalRecord, error) {
	if journal == nil || verifier == nil || parentGrantID.IsZero() ||
		observation.digest.IsZero() || occurredAt.IsZero() {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("record derived standing grant requires journal, provider verifier, parent, observation, and occurrence time")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	var parent StandingGrant
	for _, existing := range projection.state.grants {
		if existing.parentGrantID == parentGrantID {
			if existing.providerObservationDigest != observation.digest ||
				existing.scope.pullRequest != observation.identity || existing.scope.frontier.head != observation.head {
				return StandingGrant{}, JournalRecord{}, fmt.Errorf("standing grant %s already has a different provider-derived child", parentGrantID)
			}
			return cloneStandingGrant(existing), JournalRecord{}, nil
		}
		if existing.grantID == parentGrantID {
			parent = existing
		}
	}
	if parent.grantID.IsZero() {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("provider-derived standing grant parent %s is not active", parentGrantID)
	}
	verification, err := newProviderPullRequestVerification(
		parent.scope, parentGrantID, Digest{}, parent.scope.frontier.head, observation,
	)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	if err := verifier.VerifyProviderPullRequest(ctx, verification, observation); err != nil {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("verify provider pull request observation: %w", err)
	}
	derived, err := deriveStandingGrantPullRequest(parent, observation)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	domainEvent, err := NewAuthorizationGrantRecorded(derived)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	if _, err := ReduceAuthorization(projection.state, domainEvent); err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	event, err := NewAuthorizationGrantRecordedJournalEvent(
		definition.workspace.id, definition.generation, derived,
	)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	record, err := appendAuthorizationJournalEvent(journal, snapshot, event, occurredAt)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	return derived, record, nil
}

// RecordPullRequestStandingGrantFrontierAdvance binds a newly signed exact
// old/new frontier grant to an already durable provider-derived pull request.
// It is the protected authorization path for review-fix pushes after the pull
// request exists: the provider independently confirms the PR identity and old
// remote head before the new exact head becomes dispatchable.
func RecordPullRequestStandingGrantFrontierAdvance(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	verifier ProviderPullRequestVerifierPort,
	parentGrantID Digest,
	priorDerivedGrantID Digest,
	observation ProviderPullRequestObservation,
	occurredAt time.Time,
) (StandingGrant, JournalRecord, error) {
	if journal == nil || verifier == nil || parentGrantID.IsZero() || priorDerivedGrantID.IsZero() ||
		observation.digest.IsZero() || occurredAt.IsZero() {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("record pull-request frontier advance requires journal, provider verifier, signed parent, predecessor, observation, and occurrence time")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	var parent, prior StandingGrant
	for _, existing := range projection.state.grants {
		if existing.parentGrantID == parentGrantID {
			if existing.priorDerivedGrantID != priorDerivedGrantID ||
				existing.providerObservationDigest != observation.digest ||
				existing.providerObservedHead != observation.head ||
				existing.scope.pullRequest != observation.identity {
				return StandingGrant{}, JournalRecord{}, fmt.Errorf("standing grant %s already has a different provider-derived child", parentGrantID)
			}
			return cloneStandingGrant(existing), JournalRecord{}, nil
		}
		if existing.priorDerivedGrantID == priorDerivedGrantID {
			return StandingGrant{}, JournalRecord{}, fmt.Errorf("pull-request grant %s already has a different frontier successor", priorDerivedGrantID)
		}
		if existing.grantID == parentGrantID {
			parent = existing
		}
		if existing.grantID == priorDerivedGrantID {
			prior = existing
		}
	}
	if parent.grantID.IsZero() || prior.grantID.IsZero() {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("pull-request frontier advance requires active signed parent %s and predecessor %s", parentGrantID, priorDerivedGrantID)
	}
	derived, err := deriveStandingGrantPullRequestFrontierAdvance(parent, prior, observation)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	verification, err := newProviderPullRequestVerification(
		parent.scope, parentGrantID, priorDerivedGrantID, prior.scope.frontier.head, observation,
	)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	if err := verifier.VerifyProviderPullRequest(ctx, verification, observation); err != nil {
		return StandingGrant{}, JournalRecord{}, fmt.Errorf("verify provider pull request frontier: %w", err)
	}
	domainEvent, err := NewAuthorizationGrantRecorded(derived)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	if _, err := ReduceAuthorization(projection.state, domainEvent); err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	event, err := NewAuthorizationGrantRecordedJournalEvent(
		definition.workspace.id, definition.generation, derived,
	)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	record, err := appendAuthorizationJournalEvent(journal, snapshot, event, occurredAt)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	return derived, record, nil
}

func RecordAuthorizationRevocation(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	verifier ControlPlaneVerifierPort,
	options AuthorizationRevocationOptions,
	receipt ControlPlaneReceiptV2,
	occurredAt time.Time,
) (AuthorizationRevocation, JournalRecord, error) {
	if journal == nil || verifier == nil || occurredAt.IsZero() {
		return AuthorizationRevocation{}, JournalRecord{}, fmt.Errorf("record authorization revocation requires journal, verifier, and occurrence time")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return AuthorizationRevocation{}, JournalRecord{}, err
	}
	expected, err := newAuthorizationRevocation(options)
	if err != nil {
		return AuthorizationRevocation{}, JournalRecord{}, err
	}
	for _, record := range snapshot.records {
		existing, ok := record.event.(AuthorizationRevokedJournalEvent)
		if !ok || existing.revocation.digest != expected.digest {
			continue
		}
		if existing.revocation.receipt != receipt.ReceiptDigest() {
			return AuthorizationRevocation{}, JournalRecord{}, fmt.Errorf("authorization revocation conflicts with durable request %s", expected.digest)
		}
		return existing.revocation, JournalRecord{}, nil
	}
	revocation, err := VerifyAuthorizationRevocation(ctx, verifier, options, receipt)
	if err != nil {
		return AuthorizationRevocation{}, JournalRecord{}, err
	}
	domainEvent, _ := NewAuthorizationRevoked(revocation)
	if _, err := ReduceAuthorization(projection.state, domainEvent); err != nil {
		return AuthorizationRevocation{}, JournalRecord{}, err
	}
	event, err := NewAuthorizationRevokedJournalEvent(definition.workspace.id, definition.generation, revocation)
	if err != nil {
		return AuthorizationRevocation{}, JournalRecord{}, err
	}
	record, err := appendAuthorizationJournalEvent(journal, snapshot, event, occurredAt)
	if err != nil {
		return AuthorizationRevocation{}, JournalRecord{}, err
	}
	return revocation, record, nil
}

func RecordAuthorizationSegmentCompletion(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	segment ID,
	occurredAt time.Time,
) (JournalRecord, error) {
	if journal == nil || segment.IsZero() || occurredAt.IsZero() {
		return JournalRecord{}, fmt.Errorf("record segment completion requires journal, segment, and occurrence time")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return JournalRecord{}, err
	}
	for _, completed := range projection.state.completedSegments {
		if completed == segment {
			return JournalRecord{}, nil
		}
	}
	domainEvent, _ := NewAuthorizationSegmentCompleted(segment)
	if _, err := ReduceAuthorization(projection.state, domainEvent); err != nil {
		return JournalRecord{}, err
	}
	event, err := NewAuthorizationSegmentCompletedJournalEvent(
		definition.workspace.id, definition.generation, segment, projection.state.epoch,
	)
	if err != nil {
		return JournalRecord{}, err
	}
	return appendAuthorizationJournalEvent(journal, snapshot, event, occurredAt)
}

func RecordAuthorizationSafetyChange(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	verifier ControlPlaneVerifierPort,
	safety AuthorizationSafetyState,
	receipt ControlPlaneReceiptV2,
	occurredAt time.Time,
) (JournalRecord, error) {
	if journal == nil || verifier == nil || receipt.ReceiptDigest().IsZero() || occurredAt.IsZero() {
		return JournalRecord{}, fmt.Errorf("record authorization safety change requires journal, verifier, receipt, and occurrence time")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return JournalRecord{}, err
	}
	receiptRequest := receipt.Binding().RequestDigest()
	for _, record := range snapshot.records {
		existing, ok := record.event.(AuthorizationSafetyChangedJournalEvent)
		if !ok {
			continue
		}
		if existing.receiptDigest == receipt.ReceiptDigest() {
			if existing.requestDigest != receiptRequest || existing.safety != safety ||
				projection.state.safety != safety {
				return JournalRecord{}, fmt.Errorf("authorization safety receipt conflicts with its durable transition")
			}
			return JournalRecord{}, nil
		}
		if existing.requestDigest == receiptRequest {
			return JournalRecord{}, fmt.Errorf("authorization safety request conflicts with its durable receipt")
		}
	}
	if projection.state.safety == safety {
		return JournalRecord{}, fmt.Errorf("authorization safety already matches target without this durable receipt")
	}
	binding, err := AuthorizationSafetyChangeControlPlaneBinding(projection.state, projection.pendingCandidates, safety)
	if err != nil {
		return JournalRecord{}, err
	}
	verification, err := NewControlPlaneVerification(binding)
	if err != nil {
		return JournalRecord{}, err
	}
	if err := verifier.Verify(ctx, verification, receipt); err != nil {
		return JournalRecord{}, fmt.Errorf("verify authorization safety change: %w", err)
	}
	domainEvent := NewAuthorizationSafetyChanged(safety)
	if _, err := ReduceAuthorization(projection.state, domainEvent); err != nil {
		return JournalRecord{}, err
	}
	event, err := NewAuthorizationSafetyChangedJournalEvent(
		definition.workspace.id, definition.generation, projection.state.epoch, safety,
		binding.RequestDigest(), receipt.ReceiptDigest(),
	)
	if err != nil {
		return JournalRecord{}, err
	}
	return appendAuthorizationJournalEvent(journal, snapshot, event, occurredAt)
}

// ReadAuthorizationEvaluationSnapshot returns an immutable state and the
// exact journal head/resource revision that every checkpoint must retain.
func ReadAuthorizationEvaluationSnapshot(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (AuthorizationState, AuthorizationSnapshotBinding, error) {
	if journal == nil {
		return AuthorizationState{}, AuthorizationSnapshotBinding{}, fmt.Errorf("authorization evaluation snapshot requires journal")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return AuthorizationState{}, AuthorizationSnapshotBinding{}, err
	}
	binding, err := NewAuthorizationSnapshotBinding(
		snapshot.head, snapshot.Revision(AuthorizationEpochJournalResource(definition.workspace.id)),
	)
	if err != nil {
		return AuthorizationState{}, AuthorizationSnapshotBinding{}, err
	}
	return projection.State(), binding, nil
}

// RecordAuthorizationEffectDispatched is the durable revocation
// linearization point. It reloads the current journal state, re-evaluates with
// the protected clock, and atomically records the reconciliation obligation
// before any provider broker may be invoked.
func RecordAuthorizationEffectDispatched(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	evaluator *AuthorizationEvaluator,
	request AuthorizationRequest,
	queueEntry AuthorizationCapability,
	effectID ID,
) (AuthorizationDispatchObligation, JournalRecord, error) {
	if journal == nil || evaluator == nil || effectID.IsZero() || request.digest.IsZero() {
		return AuthorizationDispatchObligation{}, JournalRecord{}, fmt.Errorf("record authorization dispatch requires journal, evaluator, request, queue capability, and effect")
	}
	snapshot, projection, err := readAuthorizationRuntime(journal, definition)
	if err != nil {
		return AuthorizationDispatchObligation{}, JournalRecord{}, err
	}
	for _, obligation := range projection.state.obligations {
		if obligation.effectID != effectID {
			continue
		}
		if obligation.requestDigest != request.digest || obligation.grantID != queueEntry.grantID ||
			obligation.epoch != queueEntry.epoch {
			return AuthorizationDispatchObligation{}, JournalRecord{}, fmt.Errorf("authorization effect %s conflicts with its durable dispatch", effectID)
		}
		return obligation, JournalRecord{}, nil
	}
	binding, err := NewAuthorizationSnapshotBinding(
		snapshot.head, snapshot.Revision(AuthorizationEpochJournalResource(definition.workspace.id)),
	)
	if err != nil {
		return AuthorizationDispatchObligation{}, JournalRecord{}, err
	}
	capability, err := evaluator.AuthorizeImmediatelyBeforeDispatch(
		projection.state, request, binding, queueEntry,
	)
	if err != nil {
		return AuthorizationDispatchObligation{}, JournalRecord{}, err
	}
	effect, err := NewAuthorizationEffectDispatched(effectID, capability)
	if err != nil {
		return AuthorizationDispatchObligation{}, JournalRecord{}, err
	}
	if _, err := ReduceAuthorization(projection.state, effect); err != nil {
		return AuthorizationDispatchObligation{}, JournalRecord{}, err
	}
	event, err := NewAuthorizationEffectDispatchedJournalEvent(
		definition.workspace.id, definition.generation, effect,
	)
	if err != nil {
		return AuthorizationDispatchObligation{}, JournalRecord{}, err
	}
	record, err := appendAuthorizationJournalEvent(journal, snapshot, event, effect.dispatchedAt)
	if err != nil {
		return AuthorizationDispatchObligation{}, JournalRecord{}, err
	}
	return AuthorizationDispatchObligation{
		effectID: effectID, requestDigest: capability.requestDigest,
		grantID: capability.grantID, epoch: capability.epoch, dispatchedAt: effect.dispatchedAt,
	}, record, nil
}

func readAuthorizationRuntime(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (JournalSnapshot, AuthorizationRuntimeProjection, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalSnapshot{}, AuthorizationRuntimeProjection{}, err
	}
	projection, err := RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil {
		return JournalSnapshot{}, AuthorizationRuntimeProjection{}, err
	}
	return snapshot, projection, nil
}

func appendAuthorizationJournalEvent(
	journal *WorkspaceJournal,
	snapshot JournalSnapshot,
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalRecord, error) {
	reads, writes, ok := authorizationJournalEventResources(event)
	if !ok {
		return JournalRecord{}, fmt.Errorf("unsupported authorization journal event %T", event)
	}
	readSet := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(resource, snapshot.Revision(resource))
		readSet = append(readSet, revision)
	}
	appendRequest, err := newPrivilegedJournalAppend(event, occurredAt, readSet, writes)
	if err != nil {
		return JournalRecord{}, err
	}
	return journal.AppendIfHead(appendRequest, snapshot.head)
}

func cloneAuthorizationRuntime(source AuthorizationRuntimeProjection) AuthorizationRuntimeProjection {
	result := source
	result.state = cloneAuthorizationState(source.state)
	result.receipts = append([]Digest(nil), source.receipts...)
	result.pendingCandidates = append([]Digest(nil), source.pendingCandidates...)
	return result
}

func cloneStandingGrant(grant StandingGrant) StandingGrant {
	grant.scope = cloneStandingGrantScope(grant.scope)
	return grant
}
