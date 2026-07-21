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
		if event.grant.scope.Digest() != event.grant.requestDigest ||
			event.grant.grantID != event.grant.scope.Digest() {
			return fmt.Errorf("authorization grant event has invalid signed grant bindings")
		}
		return nil
	}
	derivedID, err := derivedStandingGrantID(
		event.grant.parentGrantID, event.grant.receiptDigest,
		event.grant.scope.pullRequest, event.grant.scope.frontier.head,
	)
	if err != nil || derivedID != event.grant.grantID || event.grant.scope.pullRequest.IsZero() {
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

func AuthorizationSegmentJournalResource(workspaceID, segment ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceSerialSegment, workspaceID.String()+"/"+segment.String())
	return resource
}

func isAuthorizationJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case AuthorizationGrantRecordedJournalEvent, AuthorizationRevokedJournalEvent,
		AuthorizationSegmentCompletedJournalEvent:
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
			)
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
	case CandidateGenerationStoredJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.activeGeneration != current.state.generation {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization projection candidate has stale bindings")
		}
		if !containsDigest(current.pendingCandidates, event.candidateGeneration) {
			next.pendingCandidates = append(next.pendingCandidates, event.candidateGeneration)
			sort.Slice(next.pendingCandidates, func(i, j int) bool {
				return next.pendingCandidates[i].String() < next.pendingCandidates[j].String()
			})
		}
		next.state.safety.reconciliationPending = true
	case GenerationActivatedJournalEvent:
		if !current.initialized || event.workspaceID != current.state.workspaceID ||
			event.priorGeneration != current.state.generation || len(current.state.obligations) != 0 {
			return AuthorizationRuntimeProjection{}, fmt.Errorf("authorization generation activation is stale or has reconciliation obligations")
		}
		next.state.generation = event.activeGeneration
		next.state.epoch++
		next.state.grants = nil
		next.state.revokedGrantIDs = nil
		next.state.completedSegments = nil
		next.state.safety = AuthorizationSafetyState{}
		next.pendingCandidates = removeDigest(next.pendingCandidates, event.activeGeneration)
		next.state.safety.reconciliationPending = len(next.pendingCandidates) != 0
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
	grant, err := VerifyStandingGrant(ctx, verifier, scope, receipt)
	if err != nil {
		return StandingGrant{}, JournalRecord{}, err
	}
	for _, existing := range projection.state.grants {
		if existing.grantID == grant.grantID {
			if existing.receiptDigest != grant.receiptDigest {
				return StandingGrant{}, JournalRecord{}, fmt.Errorf("standing grant conflicts with durable grant %s", grant.grantID)
			}
			return cloneStandingGrant(existing), JournalRecord{}, nil
		}
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

func removeDigest(values []Digest, target Digest) []Digest {
	result := make([]Digest, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
