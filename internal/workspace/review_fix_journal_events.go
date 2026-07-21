package workspace

import "fmt"

type ReviewFixReservedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	protocol       ReviewFixProtocol
	maximum        uint16
	ordinal        uint16
	parent         GitObjectID
	reservationKey Digest
}

func NewReviewFixReservedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocol ReviewFixProtocol,
	maximum, ordinal uint16,
	parent GitObjectID,
) (ReviewFixReservedJournalEvent, error) {
	key, err := reviewFixReservationKey(generation, protocol.digest, maximum, ordinal, parent)
	if err != nil {
		return ReviewFixReservedJournalEvent{}, err
	}
	event := ReviewFixReservedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocol: *cloneReviewFixProtocol(&protocol), maximum: maximum, ordinal: ordinal,
		parent: parent, reservationKey: key,
	}
	if err := event.validate(); err != nil {
		return ReviewFixReservedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return ReviewFixReservedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewFixReservedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewFixReservedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewFixReserved
}
func (event ReviewFixReservedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewFixReservedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.protocol.digest.IsZero() || event.maximum == 0 || event.ordinal == 0 ||
		event.ordinal > event.maximum || event.parent.IsZero() || event.reservationKey.IsZero() {
		return fmt.Errorf("review-fix reservation requires complete generation, budget, ordinal, and parent bindings")
	}
	if _, err := event.protocol.Step(event.ordinal); err != nil {
		return err
	}
	key, err := reviewFixReservationKey(
		event.generation, event.protocol.digest, event.maximum, event.ordinal, event.parent,
	)
	if err != nil || key != event.reservationKey {
		return fmt.Errorf("review-fix reservation key does not match its bindings")
	}
	return nil
}
func (event ReviewFixReservedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event ReviewFixReservedJournalEvent) Generation() Digest { return event.generation }
func (event ReviewFixReservedJournalEvent) AttemptID() ID      { return event.attemptID }
func (event ReviewFixReservedJournalEvent) Protocol() ReviewFixProtocol {
	return *cloneReviewFixProtocol(&event.protocol)
}
func (event ReviewFixReservedJournalEvent) ProtocolDigest() Digest { return event.protocol.digest }
func (event ReviewFixReservedJournalEvent) Maximum() uint16        { return event.maximum }
func (event ReviewFixReservedJournalEvent) Ordinal() uint16        { return event.ordinal }
func (event ReviewFixReservedJournalEvent) Parent() GitObjectID    { return event.parent }
func (event ReviewFixReservedJournalEvent) ReservationKey() Digest { return event.reservationKey }

type ReviewFixIntendedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	protocolDigest Digest
	stepID         ID
	ordinal        uint16
	parent         GitObjectID
	reservationKey Digest
	inspection     StagedCommitInspection
	body           string
	idempotencyKey Digest
}

func NewReviewFixIntendedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest Digest,
	step CommitStep,
	ordinal uint16,
	parent GitObjectID,
	reservationKey Digest,
	inspection StagedCommitInspection,
	body string,
) (ReviewFixIntendedJournalEvent, error) {
	resolvedBody, err := step.message.ResolveBody(body)
	if err != nil {
		return ReviewFixIntendedJournalEvent{}, err
	}
	key, err := commitEffectIdempotencyKey(
		generation, protocolDigest, step.id, ordinal, parent, inspection.stateDigest, resolvedBody, 0,
	)
	if err != nil {
		return ReviewFixIntendedJournalEvent{}, err
	}
	event := ReviewFixIntendedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocolDigest: protocolDigest, stepID: step.id, ordinal: ordinal, parent: parent,
		reservationKey: reservationKey, inspection: cloneStagedCommitInspection(inspection),
		body: resolvedBody, idempotencyKey: key,
	}
	if err := event.validate(); err != nil {
		return ReviewFixIntendedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return ReviewFixIntendedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewFixIntendedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewFixIntendedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewFixIntended
}
func (event ReviewFixIntendedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewFixIntendedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.protocolDigest.IsZero() || event.stepID.IsZero() || event.ordinal == 0 || event.parent.IsZero() ||
		event.reservationKey.IsZero() || event.inspection.stateDigest.IsZero() || event.idempotencyKey.IsZero() {
		return fmt.Errorf("review-fix intent requires complete reservation and staged-state bindings")
	}
	if err := validateCommitBody(event.body); err != nil {
		return err
	}
	key, err := commitEffectIdempotencyKey(
		event.generation, event.protocolDigest, event.stepID, event.ordinal, event.parent,
		event.inspection.stateDigest, event.body, 0,
	)
	if err != nil || key != event.idempotencyKey {
		return fmt.Errorf("review-fix intent key does not match its bindings")
	}
	return nil
}
func (event ReviewFixIntendedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event ReviewFixIntendedJournalEvent) Generation() Digest     { return event.generation }
func (event ReviewFixIntendedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event ReviewFixIntendedJournalEvent) ProtocolDigest() Digest { return event.protocolDigest }
func (event ReviewFixIntendedJournalEvent) StepID() ID             { return event.stepID }
func (event ReviewFixIntendedJournalEvent) Ordinal() uint16        { return event.ordinal }
func (event ReviewFixIntendedJournalEvent) Parent() GitObjectID    { return event.parent }
func (event ReviewFixIntendedJournalEvent) ReservationKey() Digest { return event.reservationKey }
func (event ReviewFixIntendedJournalEvent) Inspection() StagedCommitInspection {
	return cloneStagedCommitInspection(event.inspection)
}
func (event ReviewFixIntendedJournalEvent) Body() string           { return event.body }
func (event ReviewFixIntendedJournalEvent) IdempotencyKey() Digest { return event.idempotencyKey }

type ReviewFixCommitRecordedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	protocolDigest Digest
	ordinal        uint16
	intentKey      Digest
	evidence       CommitObjectEvidence
}

func NewReviewFixCommitRecordedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest Digest,
	ordinal uint16,
	intentKey Digest,
	evidence CommitObjectEvidence,
) (ReviewFixCommitRecordedJournalEvent, error) {
	event := ReviewFixCommitRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocolDigest: protocolDigest, ordinal: ordinal, intentKey: intentKey,
		evidence: cloneCommitObjectEvidence(evidence),
	}
	if err := event.validate(); err != nil {
		return ReviewFixCommitRecordedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return ReviewFixCommitRecordedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewFixCommitRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewFixCommitRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewFixCommitRecorded
}
func (event ReviewFixCommitRecordedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewFixCommitRecordedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.protocolDigest.IsZero() || event.ordinal == 0 || event.intentKey.IsZero() ||
		event.evidence.evidence.IsZero() || event.evidence.generation != event.generation ||
		event.evidence.ordinal != event.ordinal {
		return fmt.Errorf("review-fix commit record requires generation-bound intent and evidence")
	}
	content, err := canonicalCommitObjectEvidence(event.evidence)
	if err != nil || DigestBytes(content) != event.evidence.evidence {
		return fmt.Errorf("review-fix commit evidence digest does not match canonical bindings")
	}
	return nil
}
func (event ReviewFixCommitRecordedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event ReviewFixCommitRecordedJournalEvent) Generation() Digest     { return event.generation }
func (event ReviewFixCommitRecordedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event ReviewFixCommitRecordedJournalEvent) ProtocolDigest() Digest { return event.protocolDigest }
func (event ReviewFixCommitRecordedJournalEvent) Ordinal() uint16        { return event.ordinal }
func (event ReviewFixCommitRecordedJournalEvent) IntentKey() Digest      { return event.intentKey }
func (event ReviewFixCommitRecordedJournalEvent) Evidence() CommitObjectEvidence {
	return cloneCommitObjectEvidence(event.evidence)
}

type ReviewFixCheckRecordedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	protocolDigest Digest
	ordinal        uint16
	checkOrdinal   uint16
	idempotencyKey Digest
	evidence       CommitCheckEvidence
}

func NewReviewFixCheckRecordedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	protocolDigest Digest,
	ordinal, checkOrdinal uint16,
	idempotencyKey Digest,
	evidence CommitCheckEvidence,
) (ReviewFixCheckRecordedJournalEvent, error) {
	event := ReviewFixCheckRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		protocolDigest: protocolDigest, ordinal: ordinal, checkOrdinal: checkOrdinal,
		idempotencyKey: idempotencyKey, evidence: cloneOneCommitCheckEvidence(evidence),
	}
	if err := event.validate(); err != nil {
		return ReviewFixCheckRecordedJournalEvent{}, err
	}
	if err := validateCommitJournalRecordFootprint(event); err != nil {
		return ReviewFixCheckRecordedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewFixCheckRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewFixCheckRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewFixCheckRecorded
}
func (event ReviewFixCheckRecordedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewFixCheckRecordedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.protocolDigest.IsZero() || event.ordinal == 0 || event.checkOrdinal == 0 ||
		event.idempotencyKey.IsZero() || event.evidence.evidence.IsZero() ||
		event.evidence.generation != event.generation {
		return fmt.Errorf("review-fix check record requires complete protocol and evidence bindings")
	}
	content, err := canonicalCommitCheckEvidence(event.evidence)
	if err != nil || DigestBytes(content) != event.evidence.evidence {
		return fmt.Errorf("review-fix check evidence digest does not match canonical bindings")
	}
	return nil
}
func (event ReviewFixCheckRecordedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event ReviewFixCheckRecordedJournalEvent) Generation() Digest     { return event.generation }
func (event ReviewFixCheckRecordedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event ReviewFixCheckRecordedJournalEvent) ProtocolDigest() Digest { return event.protocolDigest }
func (event ReviewFixCheckRecordedJournalEvent) Ordinal() uint16        { return event.ordinal }
func (event ReviewFixCheckRecordedJournalEvent) CheckOrdinal() uint16   { return event.checkOrdinal }
func (event ReviewFixCheckRecordedJournalEvent) IdempotencyKey() Digest { return event.idempotencyKey }
func (event ReviewFixCheckRecordedJournalEvent) Evidence() CommitCheckEvidence {
	return cloneOneCommitCheckEvidence(event.evidence)
}

func ReviewFixJournalResource(attemptID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceReviewFix, attemptID.String()+"/protocol")
	return resource
}

func ReviewFixBudgetJournalResource(attemptID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceBudget, attemptID.String()+"/review-fixes")
	return resource
}

func ReviewFixStepJournalResource(attemptID, stepID ID, ordinal uint16) JournalResource {
	identity := fmt.Sprintf("%s/review-fix/%d/%s", attemptID, ordinal, stepID)
	resource, _ := NewJournalResource(JournalResourceCommitStep, identity)
	return resource
}

func ReviewFixCheckJournalResource(
	attemptID, stepID, checkID ID,
	ordinal, checkOrdinal uint16,
) JournalResource {
	identity := fmt.Sprintf("%s/review-fix/%d/%s/%d/%s", attemptID, ordinal, stepID, checkOrdinal, checkID)
	resource, _ := NewJournalResource(JournalResourceCheck, identity)
	return resource
}

func isReviewFixJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case ReviewFixReservedJournalEvent, ReviewFixIntendedJournalEvent,
		ReviewFixCommitRecordedJournalEvent, ReviewFixCheckRecordedJournalEvent:
		return true
	default:
		return false
	}
}

func reviewFixJournalEventResources(event WorkspaceJournalEvent) ([]JournalResource, []JournalResource, bool) {
	var workspaceID, attemptID, stepID ID
	var generation Digest
	var ordinal uint16
	var reads []JournalResource
	switch event := event.(type) {
	case ReviewFixReservedJournalEvent:
		workspaceID, generation, attemptID, ordinal = event.workspaceID, event.generation, event.attemptID, event.ordinal
		step, err := event.protocol.Step(event.ordinal)
		if err != nil {
			return nil, nil, false
		}
		stepID = step.id
		reads = []JournalResource{
			AttemptJournalResource(attemptID), ReviewFixJournalResource(attemptID),
			ReviewFixBudgetJournalResource(attemptID), ReviewFixStepJournalResource(attemptID, stepID, ordinal),
		}
	case ReviewFixIntendedJournalEvent:
		workspaceID, generation, attemptID, ordinal, stepID =
			event.workspaceID, event.generation, event.attemptID, event.ordinal, event.stepID
		reads = []JournalResource{
			AttemptJournalResource(attemptID), ReviewFixJournalResource(attemptID),
			ReviewFixStepJournalResource(attemptID, stepID, ordinal),
		}
	case ReviewFixCommitRecordedJournalEvent:
		workspaceID, generation, attemptID, ordinal, stepID =
			event.workspaceID, event.generation, event.attemptID, event.ordinal, event.evidence.stepID
		reads = []JournalResource{
			AttemptJournalResource(attemptID), ReviewFixJournalResource(attemptID),
			ReviewFixStepJournalResource(attemptID, stepID, ordinal),
			EvidenceJournalResource(event.evidence.evidence),
		}
	case ReviewFixCheckRecordedJournalEvent:
		workspaceID, generation, attemptID, ordinal, stepID =
			event.workspaceID, event.generation, event.attemptID, event.ordinal, event.evidence.stepID
		reads = []JournalResource{
			AttemptJournalResource(attemptID), ReviewFixJournalResource(attemptID),
			ReviewFixCheckJournalResource(
				attemptID, stepID, event.evidence.checkID, ordinal, event.checkOrdinal,
			),
			EvidenceJournalResource(event.evidence.evidence),
		}
	default:
		return nil, nil, false
	}
	reads = append(reads, WorkspaceJournalResource(workspaceID), GenerationJournalResource(generation))
	return reads, append([]JournalResource(nil), reads...), true
}

func cloneReviewFixJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case ReviewFixReservedJournalEvent:
		value.protocol = *cloneReviewFixProtocol(&value.protocol)
		return value
	case ReviewFixIntendedJournalEvent:
		value.inspection = cloneStagedCommitInspection(value.inspection)
		return value
	case ReviewFixCommitRecordedJournalEvent:
		value.evidence = cloneCommitObjectEvidence(value.evidence)
		return value
	case ReviewFixCheckRecordedJournalEvent:
		value.evidence = cloneOneCommitCheckEvidence(value.evidence)
		return value
	default:
		return nil
	}
}
