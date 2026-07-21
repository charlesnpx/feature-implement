package workspace

import (
	"fmt"
	"time"
)

const reviewJournalRecordSafetyBytes = 8 * 1024

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

type ReviewResultRecordedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	loopDigest     Digest
	round          uint16
	profileOrdinal uint16
	invocation     uint16
	result         ReviewResultSubmission
	receiptDigest  Digest
}

func NewReviewResultRecordedJournalEvent(
	workspaceID ID, generation Digest, attemptID ID, loopDigest Digest,
	record RecordReviewResult,
) (ReviewResultRecordedJournalEvent, error) {
	event := ReviewResultRecordedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID, loopDigest: loopDigest,
		round: record.round, profileOrdinal: record.profileOrdinal, invocation: record.invocation,
		result: cloneReviewResult(record.result), receiptDigest: record.receiptDigest,
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
		event.round, event.profileOrdinal, event.invocation, event.result, event.receiptDigest,
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
func (event ReviewResultRecordedJournalEvent) Result() ReviewResultSubmission {
	return cloneReviewResult(event.result)
}
func (event ReviewResultRecordedJournalEvent) ReceiptDigest() Digest { return event.receiptDigest }

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
		event.fix.ordinal, event.fix.priorHead, event.fix.priorTree,
		event.fix.head, event.fix.tree, event.fix.evidence, event.fix.findings,
	)
	return err
}
func (event ReviewFixAppliedJournalEvent) WorkspaceID() ID     { return event.workspaceID }
func (event ReviewFixAppliedJournalEvent) Generation() Digest  { return event.generation }
func (event ReviewFixAppliedJournalEvent) AttemptID() ID       { return event.attemptID }
func (event ReviewFixAppliedJournalEvent) LoopDigest() Digest  { return event.loopDigest }
func (event ReviewFixAppliedJournalEvent) Fix() ApplyReviewFix { return cloneApplyReviewFix(event.fix) }

func ReviewJournalResource(attemptID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceReview, attemptID.String()+"/loop")
	return resource
}

func ReviewBudgetJournalResource(attemptID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceBudget, attemptID.String()+"/review-loop")
	return resource
}

func ReviewProfileResultJournalResource(attemptID ID, round, profileOrdinal, invocation uint16) JournalResource {
	identity := fmt.Sprintf("%s/%d/%d/%d", attemptID, round, profileOrdinal, invocation)
	resource, _ := NewJournalResource(JournalResourceReviewProfile, identity)
	return resource
}

func isReviewJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case ReviewRoundStartedJournalEvent, ReviewResultRecordedJournalEvent, ReviewFixAppliedJournalEvent:
		return true
	default:
		return false
	}
}

func reviewJournalEventResources(event WorkspaceJournalEvent) ([]JournalResource, []JournalResource, bool) {
	var workspaceID, attemptID ID
	var generation Digest
	var reads []JournalResource
	switch event := event.(type) {
	case ReviewRoundStartedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		reads = []JournalResource{
			AttemptJournalResource(attemptID), ReviewJournalResource(attemptID), ReviewBudgetJournalResource(attemptID),
		}
	case ReviewResultRecordedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		reads = []JournalResource{
			AttemptJournalResource(attemptID), ReviewJournalResource(attemptID), ReviewBudgetJournalResource(attemptID),
			ReviewProfileResultJournalResource(attemptID, event.round, event.profileOrdinal, event.invocation),
			AuthorizationReceiptJournalResource(event.receiptDigest),
		}
	case ReviewFixAppliedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
		reads = []JournalResource{
			AttemptJournalResource(attemptID), ReviewJournalResource(attemptID), ReviewBudgetJournalResource(attemptID),
			ReviewFixJournalResource(attemptID), EvidenceJournalResource(event.fix.evidence),
		}
	default:
		return nil, nil, false
	}
	reads = append(reads, WorkspaceJournalResource(workspaceID), GenerationJournalResource(generation))
	return reads, append([]JournalResource(nil), reads...), true
}

func cloneReviewJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case ReviewRoundStartedJournalEvent:
		value.loop = cloneReviewLoop(value.loop)
		return value
	case ReviewResultRecordedJournalEvent:
		value.result = cloneReviewResult(value.result)
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
	reads, writes, ok := reviewJournalEventResources(event)
	if !ok {
		return fmt.Errorf("unsupported review journal footprint event %T", event)
	}
	reads, _ = normalizeJournalWriteSet(reads)
	writes, _ = normalizeJournalWriteSet(writes)
	readSet := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(resource, ^uint64(0))
		readSet = append(readSet, revision)
	}
	request, err := newPrivilegedJournalAppend(
		event, time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC), readSet, writes,
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
