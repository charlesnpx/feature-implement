package workspace

import (
	"fmt"
	"time"
)

const (
	JournalSchemaVersion       = 2
	journalRecordSchemaVersion = 4
)

type JournalEventType string

const (
	JournalEventWorkspaceInitialized         JournalEventType = "workspace.initialized.v2"
	JournalEventFeatureRefCreationIntended   JournalEventType = "feature_ref_creation_intended"
	JournalEventFeatureRefCreated            JournalEventType = "feature_ref_created"
	JournalEventTailRecovered                JournalEventType = "journal.tail_recovered.v2"
	JournalEventAttemptStart                 JournalEventType = "attempt.start.v3"
	JournalEventAttemptBoundary              JournalEventType = "attempt.paused.v3"
	JournalEventAttemptResumed               JournalEventType = "attempt.resumed.v3"
	JournalEventAttemptAbandoned             JournalEventType = "attempt.abandoned.v3"
	JournalEventReviewHeadAdopted            JournalEventType = "review.head_adopted.v2"
	JournalEventReviewGateDispatched         JournalEventType = "review.gate_dispatched.v1"
	JournalEventReviewGateRecorded           JournalEventType = "review.gate_recorded.v1"
	JournalEventMergeUnitIntegrationIntended JournalEventType = "merge_unit_integration_intended"
	JournalEventMergeUnitIntegrated          JournalEventType = "merge_unit_integrated"
	JournalEventWorkspaceCompleted           JournalEventType = "workspace_completed"
)

type WorkspaceJournalEvent interface {
	isWorkspaceJournalEvent()
	eventType() JournalEventType
	boundGeneration() Digest
	validate() error
}

type WorkspaceInitializedJournalEvent struct {
	workspaceID                  ID
	generation                   Digest
	definitionDigest             Digest
	planCheckpoint               Digest
	planCheckpointArtifactDigest Digest
	worktreeRoot                 WorkspaceWorktreeRootBinding
}

type PlanCheckpointJournalBinding struct {
	CheckpointID   Digest
	ArtifactDigest Digest
}

func NewWorkspaceInitializedJournalEvent(
	workspaceID ID,
	generation, definitionDigest Digest,
	worktreeRoot WorkspaceWorktreeRootBinding,
	planCheckpoint ...PlanCheckpointJournalBinding,
) (WorkspaceInitializedJournalEvent, error) {
	if len(planCheckpoint) > 1 {
		return WorkspaceInitializedJournalEvent{}, fmt.Errorf("workspace initialization accepts one plan checkpoint")
	}
	event := WorkspaceInitializedJournalEvent{
		workspaceID: workspaceID, generation: generation,
		definitionDigest: definitionDigest, worktreeRoot: worktreeRoot,
	}
	if len(planCheckpoint) == 1 {
		event.planCheckpoint = planCheckpoint[0].CheckpointID
		event.planCheckpointArtifactDigest = planCheckpoint[0].ArtifactDigest
	}
	if err := event.validate(); err != nil {
		return WorkspaceInitializedJournalEvent{}, err
	}
	return event, nil
}

func (WorkspaceInitializedJournalEvent) isWorkspaceJournalEvent() {}
func (WorkspaceInitializedJournalEvent) eventType() JournalEventType {
	return JournalEventWorkspaceInitialized
}
func (event WorkspaceInitializedJournalEvent) boundGeneration() Digest { return event.generation }
func (event WorkspaceInitializedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.definitionDigest.IsZero() {
		return fmt.Errorf("workspace initialization requires workspace, generation, and definition bindings")
	}
	if event.worktreeRoot.IsZero() {
		return fmt.Errorf(
			"workspace initialization requires a verified worktree root",
		)
	}
	if event.planCheckpoint.IsZero() != event.planCheckpointArtifactDigest.IsZero() {
		return fmt.Errorf("workspace initialization plan checkpoint requires checkpoint and artifact digests")
	}
	return nil
}
func (event WorkspaceInitializedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event WorkspaceInitializedJournalEvent) Generation() Digest { return event.generation }
func (event WorkspaceInitializedJournalEvent) DefinitionDigest() Digest {
	return event.definitionDigest
}
func (event WorkspaceInitializedJournalEvent) PlanCheckpoint() Digest {
	return event.planCheckpoint
}
func (event WorkspaceInitializedJournalEvent) PlanCheckpointArtifactDigest() Digest {
	return event.planCheckpointArtifactDigest
}
func (event WorkspaceInitializedJournalEvent) WorktreeRoot() WorkspaceWorktreeRootBinding {
	return event.worktreeRoot
}

type JournalTailRecoveredEvent struct {
	workspaceID   ID
	generation    Digest
	discardOffset int64
	discardSize   int64
	discardDigest Digest
	resultingHead Digest
}

func NewJournalTailRecoveredEvent(
	workspaceID ID,
	generation Digest,
	offset, size int64,
	discardDigest, resultingHead Digest,
) (JournalTailRecoveredEvent, error) {
	event := JournalTailRecoveredEvent{
		workspaceID: workspaceID, generation: generation, discardOffset: offset,
		discardSize: size, discardDigest: discardDigest, resultingHead: resultingHead,
	}
	if err := event.validate(); err != nil {
		return JournalTailRecoveredEvent{}, err
	}
	return event, nil
}

func (JournalTailRecoveredEvent) isWorkspaceJournalEvent()      {}
func (JournalTailRecoveredEvent) eventType() JournalEventType   { return JournalEventTailRecovered }
func (event JournalTailRecoveredEvent) boundGeneration() Digest { return event.generation }
func (event JournalTailRecoveredEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.discardOffset < 0 ||
		event.discardSize <= 0 || event.discardDigest.IsZero() || event.resultingHead.IsZero() {
		return fmt.Errorf("journal recovery requires bounded discarded bytes and resulting head bindings")
	}
	return nil
}
func (event JournalTailRecoveredEvent) WorkspaceID() ID       { return event.workspaceID }
func (event JournalTailRecoveredEvent) Generation() Digest    { return event.generation }
func (event JournalTailRecoveredEvent) DiscardOffset() int64  { return event.discardOffset }
func (event JournalTailRecoveredEvent) DiscardSize() int64    { return event.discardSize }
func (event JournalTailRecoveredEvent) DiscardDigest() Digest { return event.discardDigest }
func (event JournalTailRecoveredEvent) ResultingHead() Digest { return event.resultingHead }

type JournalAppend struct {
	event      WorkspaceJournalEvent
	occurredAt time.Time
}

func NewJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalAppend, error) {
	return newJournalAppend(event, occurredAt, false)
}

// newWorkflowJournalAppend constructs append requests for events whose
// evidence must be established by their owning lifecycle before they can be
// durably recorded.
func newWorkflowJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalAppend, error) {
	return newJournalAppend(event, occurredAt, true)
}

func newJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
	workflow bool,
) (JournalAppend, error) {
	if event == nil {
		return JournalAppend{}, fmt.Errorf("journal append requires a typed event")
	}
	if !supportedWorkspaceJournalEvent(event) {
		return JournalAppend{}, fmt.Errorf("unsupported workspace journal event %T", event)
	}
	if !workflow {
		switch event.(type) {
		case JournalTailRecoveredEvent:
			return JournalAppend{}, fmt.Errorf("journal recovery events must use the explicit recovery workflow")
		case AttemptResumedJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt resume must use the verified resume workflow")
		case FeatureRefCreationIntendedJournalEvent, FeatureRefCreatedJournalEvent:
			return JournalAppend{}, fmt.Errorf(
				"feature-ref events must use the recoverable local target initialization workflow",
			)
		case AttemptStartJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt start must use the detached-worktree workflow")
		case AttemptBoundaryReachedJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt boundary must use the atomic boundary workflow")
		case AttemptAbandonedJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt abandonment must use the attempt lifecycle workflow")
		case ReviewHeadAdoptedJournalEvent, ReviewGateDispatchedJournalEvent,
			ReviewGateRecordedJournalEvent:
			return JournalAppend{}, fmt.Errorf("review gate events must use the exact-artifact workflow")
		case MergeUnitIntegrationIntendedJournalEvent, MergeUnitIntegratedJournalEvent:
			return JournalAppend{}, fmt.Errorf(
				"integration events must use the ancestry-checked CAS integration workflow",
			)
		case WorkspaceCompletedJournalEvent:
			return JournalAppend{}, fmt.Errorf(
				"workspace completion events must use the complete local verification workflow",
			)
		}
	}
	if occurredAt.IsZero() {
		return JournalAppend{}, fmt.Errorf("journal append occurrence time is required")
	}
	if err := event.validate(); err != nil {
		return JournalAppend{}, err
	}
	if event.boundGeneration().IsZero() {
		return JournalAppend{}, fmt.Errorf("journal event generation binding is required")
	}
	return JournalAppend{
		event: cloneWorkspaceJournalEvent(event), occurredAt: occurredAt.UTC(),
	}, nil
}

func supportedWorkspaceJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case WorkspaceInitializedJournalEvent, JournalTailRecoveredEvent,
		FeatureRefCreationIntendedJournalEvent, FeatureRefCreatedJournalEvent:
		return true
	default:
		return isAttemptJournalEvent(event) || isReviewJournalEvent(event) ||
			isIntegrationJournalEvent(event) ||
			isCompletionJournalEvent(event)
	}
}

func (appendRequest JournalAppend) Event() WorkspaceJournalEvent {
	return cloneWorkspaceJournalEvent(appendRequest.event)
}
func (appendRequest JournalAppend) OccurredAt() time.Time { return appendRequest.occurredAt }

type JournalRecord struct {
	sequence     uint64
	occurredAt   time.Time
	previousHash Digest
	eventHash    Digest
	generation   Digest
	event        WorkspaceJournalEvent
}

func (record JournalRecord) Sequence() uint64            { return record.sequence }
func (record JournalRecord) OccurredAt() time.Time       { return record.occurredAt }
func (record JournalRecord) PreviousHash() Digest        { return record.previousHash }
func (record JournalRecord) EventHash() Digest           { return record.eventHash }
func (record JournalRecord) Generation() Digest          { return record.generation }
func (record JournalRecord) EventType() JournalEventType { return record.event.eventType() }
func (record JournalRecord) Event() WorkspaceJournalEvent {
	return cloneWorkspaceJournalEvent(record.event)
}

func cloneWorkspaceJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case WorkspaceInitializedJournalEvent:
		return value
	case JournalTailRecoveredEvent:
		return value
	default:
		if cloned := cloneLocalTargetJournalEvent(event); cloned != nil {
			return cloned
		}
		if cloned := cloneAttemptJournalEvent(event); cloned != nil {
			return cloned
		}
		if cloned := cloneReviewJournalEvent(event); cloned != nil {
			return cloned
		}
		if cloned := cloneIntegrationJournalEvent(event); cloned != nil {
			return cloned
		}
		if cloned := cloneCompletionJournalEvent(event); cloned != nil {
			return cloned
		}
		return nil
	}
}
