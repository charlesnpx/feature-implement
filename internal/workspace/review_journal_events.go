package workspace

import (
	"fmt"
	"time"
)

const reviewJournalRecordSafetyBytes = 8 * 1024

type ReviewHeadAdoptedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	mergeUnit      MergeUnitReference
	priorHead      GitObjectID
	head           GitObjectID
	tree           GitObjectID
	snapshotDigest Digest
}

func NewReviewHeadAdoptedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	mergeUnit MergeUnitReference,
	priorHead, head, tree GitObjectID,
	snapshotDigest Digest,
) (ReviewHeadAdoptedJournalEvent, error) {
	event := ReviewHeadAdoptedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID, mergeUnit: mergeUnit,
		priorHead: priorHead, head: head, tree: tree, snapshotDigest: snapshotDigest,
	}
	if err := event.validate(); err != nil {
		return ReviewHeadAdoptedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewHeadAdoptedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewHeadAdoptedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewHeadAdoptedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewHeadAdopted
}
func (event ReviewHeadAdoptedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewHeadAdoptedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.mergeUnit.planID.IsZero() || event.mergeUnit.mergeUnitID.IsZero() || event.priorHead.IsZero() ||
		event.head.IsZero() || event.tree.IsZero() || event.snapshotDigest.IsZero() ||
		event.priorHead.Algorithm() != event.head.Algorithm() || event.head.Algorithm() != event.tree.Algorithm() {
		return fmt.Errorf("review head adoption requires exact workspace, attempt, Git, and snapshot bindings")
	}
	return nil
}

type ReviewRoundStartedJournalEvent struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	mergeUnit   MergeUnitReference
	loop        ReviewLoop
	ordinal     uint16
	head        GitObjectID
	tree        GitObjectID
}

func NewReviewRoundStartedJournalEvent(start StartReviewRound) (ReviewRoundStartedJournalEvent, error) {
	event := ReviewRoundStartedJournalEvent{
		workspaceID: start.workspaceID, generation: start.generation, attemptID: start.attemptID,
		mergeUnit: start.mergeUnit, loop: cloneReviewLoop(start.loop), ordinal: start.ordinal,
		head: start.head, tree: start.tree,
	}
	if err := event.validate(); err != nil {
		return ReviewRoundStartedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewRoundStartedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewRoundStartedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewRoundStartedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewRoundStarted
}
func (event ReviewRoundStartedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewRoundStartedJournalEvent) validate() error {
	start, err := NewStartReviewRound(
		event.workspaceID, event.generation, event.attemptID, event.mergeUnit,
		event.loop, event.ordinal, event.head, event.tree,
	)
	if err != nil {
		return err
	}
	canonical, err := canonicalReviewLoopBytes(start.loop)
	if err != nil || DigestBytes(canonical) != event.loop.digest {
		return fmt.Errorf("review round event loop digest does not match its configuration")
	}
	return nil
}
func (event ReviewRoundStartedJournalEvent) WorkspaceID() ID               { return event.workspaceID }
func (event ReviewRoundStartedJournalEvent) Generation() Digest            { return event.generation }
func (event ReviewRoundStartedJournalEvent) AttemptID() ID                 { return event.attemptID }
func (event ReviewRoundStartedJournalEvent) MergeUnit() MergeUnitReference { return event.mergeUnit }
func (event ReviewRoundStartedJournalEvent) Loop() ReviewLoop              { return cloneReviewLoop(event.loop) }
func (event ReviewRoundStartedJournalEvent) Ordinal() uint16               { return event.ordinal }
func (event ReviewRoundStartedJournalEvent) Head() GitObjectID             { return event.head }
func (event ReviewRoundStartedJournalEvent) Tree() GitObjectID             { return event.tree }

type ReviewInvocationReservedJournalEvent struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	loopDigest  Digest
	reservation ReviewInvocationReservation
}

func NewReviewInvocationReservedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	loopDigest Digest,
	reservation ReviewInvocationReservation,
) (ReviewInvocationReservedJournalEvent, error) {
	event := ReviewInvocationReservedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		loopDigest: loopDigest, reservation: reservation,
	}
	if err := event.validate(); err != nil {
		return ReviewInvocationReservedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewInvocationReservedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewInvocationReservedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewInvocationReservedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewInvocationReserved
}
func (event ReviewInvocationReservedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewInvocationReservedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() || event.loopDigest.IsZero() ||
		event.reservation.request.workspaceID != event.workspaceID ||
		event.reservation.request.generation != event.generation || event.reservation.request.attemptID != event.attemptID ||
		event.reservation.request.loopDigest != event.loopDigest {
		return fmt.Errorf("review invocation reservation event requires exact workspace, attempt, and loop bindings")
	}
	canonical, err := canonicalReviewInvocationReservation(event.reservation)
	if err != nil || event.reservation.digest != DigestBytes(canonical) {
		return fmt.Errorf("review invocation reservation event is not canonical")
	}
	return nil
}

func (event ReviewInvocationReservedJournalEvent) Reservation() ReviewInvocationReservation {
	return event.reservation
}

type ReviewInvocationFailedJournalEvent struct {
	workspaceID       ID
	generation        Digest
	attemptID         ID
	loopDigest        Digest
	reservationDigest Digest
	failureDigest     Digest
}

func NewReviewInvocationFailedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	loopDigest Digest,
	failure RecordReviewInvocationFailure,
) (ReviewInvocationFailedJournalEvent, error) {
	event := ReviewInvocationFailedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID, loopDigest: loopDigest,
		reservationDigest: failure.reservationDigest, failureDigest: failure.failureDigest,
	}
	if err := event.validate(); err != nil {
		return ReviewInvocationFailedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewInvocationFailedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewInvocationFailedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewInvocationFailedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewInvocationFailed
}
func (event ReviewInvocationFailedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewInvocationFailedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.loopDigest.IsZero() || event.reservationDigest.IsZero() || event.failureDigest.IsZero() {
		return fmt.Errorf("review invocation failure event requires exact review and failure bindings")
	}
	return nil
}

type ReviewResultRecordedJournalEvent struct {
	workspaceID       ID
	generation        Digest
	attemptID         ID
	loopDigest        Digest
	round             uint16
	profileOrdinal    uint16
	invocation        uint16
	reservationDigest Digest
	result            ReviewResultSubmission
}

func NewReviewResultRecordedJournalEvent(
	workspaceID ID, generation Digest, attemptID ID, loopDigest Digest,
	record RecordReviewResult,
) (ReviewResultRecordedJournalEvent, error) {
	event := ReviewResultRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID, loopDigest: loopDigest,
		round: record.round, profileOrdinal: record.profileOrdinal, invocation: record.invocation,
		reservationDigest: record.reservationDigest, result: cloneReviewResult(record.result),
	}
	if err := event.validate(); err != nil {
		return ReviewResultRecordedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewResultRecordedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewResultRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewResultRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewResultRecorded
}
func (event ReviewResultRecordedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewResultRecordedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() || event.loopDigest.IsZero() {
		return fmt.Errorf("review result event requires workspace, generation, attempt, and loop")
	}
	_, err := NewRecordReviewResult(
		event.round, event.profileOrdinal, event.invocation, event.reservationDigest,
		event.result,
	)
	return err
}
func (event ReviewResultRecordedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event ReviewResultRecordedJournalEvent) Generation() Digest     { return event.generation }
func (event ReviewResultRecordedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event ReviewResultRecordedJournalEvent) LoopDigest() Digest     { return event.loopDigest }
func (event ReviewResultRecordedJournalEvent) Round() uint16          { return event.round }
func (event ReviewResultRecordedJournalEvent) ProfileOrdinal() uint16 { return event.profileOrdinal }
func (event ReviewResultRecordedJournalEvent) Invocation() uint16     { return event.invocation }
func (event ReviewResultRecordedJournalEvent) ReservationDigest() Digest {
	return event.reservationDigest
}
func (event ReviewResultRecordedJournalEvent) Result() ReviewResultSubmission {
	return cloneReviewResult(event.result)
}

type ReviewFindingFixReservedJournalEvent struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	loopDigest  Digest
	reservation ReviewFixReservation
}

func NewReviewFindingFixReservedJournalEvent(
	reservation ReviewFixReservation,
) (ReviewFindingFixReservedJournalEvent, error) {
	event := ReviewFindingFixReservedJournalEvent{
		workspaceID: reservation.workspaceID, generation: reservation.generation,
		attemptID: reservation.attemptID, loopDigest: reservation.loopDigest,
		reservation: *cloneReviewFixReservation(&reservation),
	}
	if err := event.validate(); err != nil {
		return ReviewFindingFixReservedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewFindingFixReservedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewFindingFixReservedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewFindingFixReservedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewFindingFixReserved
}
func (event ReviewFindingFixReservedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewFindingFixReservedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() || event.loopDigest.IsZero() ||
		event.reservation.workspaceID != event.workspaceID || event.reservation.generation != event.generation ||
		event.reservation.attemptID != event.attemptID || event.reservation.loopDigest != event.loopDigest {
		return fmt.Errorf("review finding-fix reservation event requires exact review bindings")
	}
	canonical, err := canonicalReviewFixReservation(event.reservation)
	if err != nil || event.reservation.digest != DigestBytes(canonical) {
		return fmt.Errorf("review finding-fix reservation event is not canonical")
	}
	return nil
}

func (event ReviewFindingFixReservedJournalEvent) Reservation() ReviewFixReservation {
	return *cloneReviewFixReservation(&event.reservation)
}

type ReviewFixAppliedJournalEvent struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	loopDigest  Digest
	fix         ApplyReviewFix
}

func NewReviewFixAppliedJournalEvent(
	workspaceID ID, generation Digest, attemptID ID, loopDigest Digest, fix ApplyReviewFix,
) (ReviewFixAppliedJournalEvent, error) {
	event := ReviewFixAppliedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		loopDigest: loopDigest, fix: cloneApplyReviewFix(fix),
	}
	if err := event.validate(); err != nil {
		return ReviewFixAppliedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewFixAppliedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewFixAppliedJournalEvent) isWorkspaceJournalEvent()      {}
func (ReviewFixAppliedJournalEvent) eventType() JournalEventType   { return JournalEventReviewFixApplied }
func (event ReviewFixAppliedJournalEvent) boundGeneration() Digest { return event.generation }
func (event ReviewFixAppliedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() || event.loopDigest.IsZero() {
		return fmt.Errorf("review fix event requires workspace, generation, attempt, and loop")
	}
	_, err := NewApplyReviewFix(
		event.fix.ordinal, event.fix.reservationDigest, event.fix.priorHead, event.fix.priorTree,
		event.fix.head, event.fix.tree, event.fix.evidence, event.fix.findings,
	)
	return err
}
func (event ReviewFixAppliedJournalEvent) WorkspaceID() ID     { return event.workspaceID }
func (event ReviewFixAppliedJournalEvent) Generation() Digest  { return event.generation }
func (event ReviewFixAppliedJournalEvent) AttemptID() ID       { return event.attemptID }
func (event ReviewFixAppliedJournalEvent) LoopDigest() Digest  { return event.loopDigest }
func (event ReviewFixAppliedJournalEvent) Fix() ApplyReviewFix { return cloneApplyReviewFix(event.fix) }

func isReviewJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case ReviewHeadAdoptedJournalEvent, ReviewRoundStartedJournalEvent, ReviewInvocationReservedJournalEvent,
		ReviewInvocationFailedJournalEvent, ReviewResultRecordedJournalEvent,
		ReviewFindingFixReservedJournalEvent, ReviewFixAppliedJournalEvent:
		return true
	default:
		return false
	}
}

func cloneReviewJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case ReviewHeadAdoptedJournalEvent:
		return value
	case ReviewRoundStartedJournalEvent:
		value.loop = cloneReviewLoop(value.loop)
		return value
	case ReviewInvocationReservedJournalEvent:
		return value
	case ReviewInvocationFailedJournalEvent:
		return value
	case ReviewResultRecordedJournalEvent:
		value.result = cloneReviewResult(value.result)
		return value
	case ReviewFindingFixReservedJournalEvent:
		value.reservation = *cloneReviewFixReservation(&value.reservation)
		return value
	case ReviewFixAppliedJournalEvent:
		value.fix = cloneApplyReviewFix(value.fix)
		return value
	default:
		return nil
	}
}

func cloneApplyReviewFix(fix ApplyReviewFix) ApplyReviewFix {
	fix.findings = append([]Digest(nil), fix.findings...)
	return fix
}

func validateReviewJournalRecordFootprint(event WorkspaceJournalEvent) error {
	request, err := newWorkflowJournalAppend(
		event, time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC),
	)
	if err != nil {
		return err
	}
	record, err := buildJournalRecord(JournalSnapshot{}, request)
	if err != nil {
		return err
	}
	encoded, err := marshalJournalRecord(record)
	if err != nil {
		return err
	}
	if len(encoded) > MaxJournalRecordBytes-reviewJournalRecordSafetyBytes {
		return fmt.Errorf("review journal record footprint %d exceeds its safe bound", len(encoded))
	}
	return nil
}
