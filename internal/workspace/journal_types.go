package workspace

import (
	"fmt"
	"time"
)

const (
	JournalSchemaVersion       = 2
	journalRecordSchemaVersion = 3
)

type JournalEventType string

const (
	JournalEventWorkspaceInitialized           JournalEventType = "workspace.initialized.v2"
	JournalEventFeatureRefCreationIntended     JournalEventType = "feature_ref_creation_intended"
	JournalEventFeatureRefCreated              JournalEventType = "feature_ref_created"
	JournalEventTailRecovered                  JournalEventType = "journal.tail_recovered.v2"
	JournalEventAttemptReserved                JournalEventType = "attempt.reserved.v2"
	JournalEventAttemptMaterializationIntended JournalEventType = "attempt.materialization_intended.v2"
	JournalEventAttemptStarted                 JournalEventType = "attempt.started.v2"
	JournalEventAttemptBoundary                JournalEventType = "attempt.boundary_reached.v2"
	JournalEventNextGoalIntended               JournalEventType = "attempt.next_goal_intended.v2"
	JournalEventOrchestrationAck               JournalEventType = "attempt.orchestration_acknowledged.v2"
	JournalEventOwnerResponse                  JournalEventType = "attempt.owner_response_recorded.v2"
	JournalEventAttemptResumed                 JournalEventType = "attempt.resumed.v2"
	JournalEventCommitProtocolStarted          JournalEventType = "commit.protocol_started.v2"
	JournalEventCommitStepIntended             JournalEventType = "commit.step_intended.v2"
	JournalEventCommitStepRecorded             JournalEventType = "commit.step_recorded.v2"
	JournalEventCommitCheckRecorded            JournalEventType = "commit.check_recorded.v2"
	JournalEventCommitProtocolRebased          JournalEventType = "commit.protocol_rebased.v2"
	JournalEventReviewFixReserved              JournalEventType = "review_fix.reserved.v2"
	JournalEventReviewFixIntended              JournalEventType = "review_fix.intended.v2"
	JournalEventReviewFixCommitRecorded        JournalEventType = "review_fix.commit_recorded.v2"
	JournalEventReviewFixCheckRecorded         JournalEventType = "review_fix.check_recorded.v2"
	JournalEventReviewRoundStarted             JournalEventType = "review.round_started.v2"
	JournalEventReviewHeadAdopted              JournalEventType = "review.head_adopted.v2"
	JournalEventReviewInvocationReserved       JournalEventType = "review.invocation_reserved.v2"
	JournalEventReviewInvocationFailed         JournalEventType = "review.invocation_failed.v2"
	JournalEventReviewResultRecorded           JournalEventType = "review.result_recorded.v2"
	JournalEventReviewFindingFixReserved       JournalEventType = "review.finding_fix_reserved.v2"
	JournalEventReviewFixApplied               JournalEventType = "review.fix_applied.v2"
	JournalEventMergeUnitIntegrationIntended   JournalEventType = "merge_unit_integration_intended"
	JournalEventMergeUnitIntegrated            JournalEventType = "merge_unit_integrated"
	JournalEventWorkspaceCompleted             JournalEventType = "workspace_completed"
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
	if event == nil {
		return JournalAppend{}, fmt.Errorf("journal append requires a typed event")
	}
	if !supportedWorkspaceJournalEvent(event) {
		return JournalAppend{}, fmt.Errorf("unsupported workspace journal event %T", event)
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
		return isAttemptJournalEvent(event) || isCommitJournalEvent(event) ||
			isReviewJournalEvent(event) || isIntegrationJournalEvent(event) ||
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
		if cloned := cloneCommitJournalEvent(event); cloned != nil {
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
