package workspace

import (
	"fmt"
	"time"
)

const reviewJournalRecordSafetyBytes = 8 * 1024

// ReviewHeadAdoptedJournalEvent remains the non-gate acceptance record used by
// merge units that do not configure a gate. It carries no review assessment
// state; its historic event name is retained for existing runtime journals.
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
		return fmt.Errorf("head adoption requires exact workspace, attempt, Git, and snapshot bindings")
	}
	return nil
}

// ReviewGateDispatchedJournalEvent records a repeatable adapter request before
// any adapter is given a frozen copy.
type ReviewGateDispatchedJournalEvent struct {
	dispatch ReviewGateDispatch
}

func NewReviewGateDispatchedJournalEvent(dispatch ReviewGateDispatch) (ReviewGateDispatchedJournalEvent, error) {
	canonical, err := canonicalReviewGateDispatch(dispatch)
	if err != nil || dispatch.digest != DigestBytes(canonical) {
		return ReviewGateDispatchedJournalEvent{}, fmt.Errorf("review gate dispatch event is not canonical")
	}
	event := ReviewGateDispatchedJournalEvent{dispatch: dispatch}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewGateDispatchedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewGateDispatchedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewGateDispatchedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewGateDispatched
}
func (event ReviewGateDispatchedJournalEvent) boundGeneration() Digest {
	return event.dispatch.generation
}
func (event ReviewGateDispatchedJournalEvent) validate() error {
	canonical, err := canonicalReviewGateDispatch(event.dispatch)
	if err != nil || event.dispatch.digest != DigestBytes(canonical) {
		return fmt.Errorf("review gate dispatch event is not canonical")
	}
	return nil
}
func (event ReviewGateDispatchedJournalEvent) Dispatch() ReviewGateDispatch {
	return event.dispatch
}

// ReviewGateRecordedJournalEvent is the terminal half of a dispatch. A raw
// Witness report can be retained as the evidence artifact without exposing its
// assessment details through the local gate model.
type ReviewGateRecordedJournalEvent struct {
	dispatch ReviewGateDispatch
	record   ReviewGateRecord
	document *ReviewDocumentArtifact
}

func NewReviewGateRecordedJournalEvent(
	dispatch ReviewGateDispatch,
	record ReviewGateRecord,
) (ReviewGateRecordedJournalEvent, error) {
	return newReviewGateRecordedJournalEvent(dispatch, record, nil)
}

func NewReviewGateRecordedDocumentJournalEvent(
	dispatch ReviewGateDispatch,
	record ReviewGateRecord,
	document ReviewDocumentArtifact,
) (ReviewGateRecordedJournalEvent, error) {
	return newReviewGateRecordedJournalEvent(dispatch, record, &document)
}

func newReviewGateRecordedJournalEvent(
	dispatch ReviewGateDispatch,
	record ReviewGateRecord,
	document *ReviewDocumentArtifact,
) (ReviewGateRecordedJournalEvent, error) {
	event := ReviewGateRecordedJournalEvent{dispatch: dispatch, record: record}
	if document != nil {
		copyDocument := *document
		event.document = &copyDocument
	}
	if err := event.validate(); err != nil {
		return ReviewGateRecordedJournalEvent{}, err
	}
	if err := validateReviewJournalRecordFootprint(event); err != nil {
		return ReviewGateRecordedJournalEvent{}, err
	}
	return event, nil
}

func (ReviewGateRecordedJournalEvent) isWorkspaceJournalEvent() {}
func (ReviewGateRecordedJournalEvent) eventType() JournalEventType {
	return JournalEventReviewGateRecorded
}
func (event ReviewGateRecordedJournalEvent) boundGeneration() Digest {
	return event.dispatch.generation
}
func (event ReviewGateRecordedJournalEvent) validate() error {
	dispatchCanonical, dispatchErr := canonicalReviewGateDispatch(event.dispatch)
	recordCanonical, recordErr := canonicalReviewGateRecord(event.record)
	if dispatchErr != nil || event.dispatch.digest != DigestBytes(dispatchCanonical) ||
		recordErr != nil || event.record.digest != DigestBytes(recordCanonical) ||
		event.record.dispatchDigest != event.dispatch.digest ||
		event.record.adapter != event.dispatch.adapter || event.record.recipe != event.dispatch.recipe ||
		event.record.policyDigest != event.dispatch.policyDigest || event.record.head != event.dispatch.head ||
		event.record.tree != event.dispatch.tree {
		return fmt.Errorf("review gate record event does not match its exact dispatch")
	}
	if event.document != nil {
		if err := event.document.validate(); err != nil {
			return err
		}
		if event.document.rawDocumentDigest != event.record.evidenceDigest {
			return fmt.Errorf("review gate document evidence does not match the gate record")
		}
	}
	return nil
}
func (event ReviewGateRecordedJournalEvent) Dispatch() ReviewGateDispatch { return event.dispatch }
func (event ReviewGateRecordedJournalEvent) Record() ReviewGateRecord     { return event.record }
func (event ReviewGateRecordedJournalEvent) DocumentArtifact() (ReviewDocumentArtifact, bool) {
	if event.document == nil {
		return ReviewDocumentArtifact{}, false
	}
	return *event.document, true
}

func isReviewJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case ReviewHeadAdoptedJournalEvent, ReviewGateDispatchedJournalEvent, ReviewGateRecordedJournalEvent:
		return true
	default:
		return false
	}
}

func cloneReviewJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case ReviewHeadAdoptedJournalEvent:
		return value
	case ReviewGateDispatchedJournalEvent:
		return value
	case ReviewGateRecordedJournalEvent:
		if value.document != nil {
			document := *value.document
			value.document = &document
		}
		return value
	default:
		return nil
	}
}

func validateReviewJournalRecordFootprint(event WorkspaceJournalEvent) error {
	request, err := newWorkflowJournalAppend(
		event,
		time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC),
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
		return fmt.Errorf("review gate journal record footprint %d exceeds its safe bound", len(encoded))
	}
	return nil
}
