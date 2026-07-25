package workspace

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const JournalSchemaVersion = 2

type JournalResourceKind string

const (
	JournalResourceWorkspace      JournalResourceKind = "workspace"
	JournalResourceGeneration     JournalResourceKind = "generation"
	JournalResourceRecovery       JournalResourceKind = "recovery"
	JournalResourceAttempt        JournalResourceKind = "attempt"
	JournalResourceMergeUnit      JournalResourceKind = "merge_unit"
	JournalResourceLease          JournalResourceKind = "lease"
	JournalResourceOrchestration  JournalResourceKind = "orchestration"
	JournalResourceGoal           JournalResourceKind = "goal"
	JournalResourceSerialSegment  JournalResourceKind = "serial_segment"
	JournalResourceBudget         JournalResourceKind = "budget"
	JournalResourceApproval       JournalResourceKind = "approval"
	JournalResourceEvidence       JournalResourceKind = "evidence"
	JournalResourceCommitProtocol JournalResourceKind = "commit_protocol"
	JournalResourceReviewFix      JournalResourceKind = "review_fix"
	JournalResourceReview         JournalResourceKind = "review"
	JournalResourceReviewProfile  JournalResourceKind = "review_profile"
	JournalResourceCommitStep     JournalResourceKind = "commit_step"
	JournalResourceCheck          JournalResourceKind = "check"
	JournalResourceFeatureRef     JournalResourceKind = "feature_ref"
	JournalResourceIntegration    JournalResourceKind = "integration"
)

func (kind JournalResourceKind) valid() bool {
	switch kind {
	case JournalResourceWorkspace, JournalResourceGeneration, JournalResourceRecovery,
		JournalResourceAttempt, JournalResourceMergeUnit, JournalResourceLease,
		JournalResourceOrchestration, JournalResourceGoal, JournalResourceSerialSegment,
		JournalResourceBudget, JournalResourceApproval, JournalResourceEvidence,
		JournalResourceCommitProtocol, JournalResourceReviewFix, JournalResourceCommitStep,
		JournalResourceReview, JournalResourceReviewProfile, JournalResourceCheck,
		JournalResourceFeatureRef, JournalResourceIntegration:
		return true
	default:
		return false
	}
}

// JournalResource is an immutable typed CAS resource. Identity is deliberately
// opaque to the journal; domain reducers own its meaning.
type JournalResource struct {
	kind     JournalResourceKind
	identity string
}

func NewJournalResource(kind JournalResourceKind, identity string) (JournalResource, error) {
	if !kind.valid() {
		return JournalResource{}, fmt.Errorf("unsupported journal resource kind %q", kind)
	}
	identity = strings.TrimSpace(identity)
	if err := validateBoundedText("journal resource identity", identity, 2048); err != nil {
		return JournalResource{}, err
	}
	return JournalResource{kind: kind, identity: identity}, nil
}

func WorkspaceJournalResource(workspaceID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceWorkspace, workspaceID.String())
	return resource
}

func GenerationJournalResource(generation Digest) JournalResource {
	resource, _ := NewJournalResource(JournalResourceGeneration, generation.String())
	return resource
}

func RecoveryJournalResource(workspaceID ID) JournalResource {
	resource, _ := NewJournalResource(JournalResourceRecovery, workspaceID.String())
	return resource
}

func (resource JournalResource) Kind() JournalResourceKind { return resource.kind }
func (resource JournalResource) Identity() string          { return resource.identity }
func (resource JournalResource) IsZero() bool {
	return !resource.kind.valid() || resource.identity == ""
}
func (resource JournalResource) key() string {
	return string(resource.kind) + "\x00" + resource.identity
}

type JournalResourceRevision struct {
	resource JournalResource
	revision uint64
}

func NewJournalResourceRevision(resource JournalResource, revision uint64) (JournalResourceRevision, error) {
	if resource.IsZero() {
		return JournalResourceRevision{}, fmt.Errorf("journal resource revision requires a resource")
	}
	return JournalResourceRevision{resource: resource, revision: revision}, nil
}

func (revision JournalResourceRevision) Resource() JournalResource { return revision.resource }
func (revision JournalResourceRevision) Revision() uint64          { return revision.revision }

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
)

type WorkspaceJournalEvent interface {
	isWorkspaceJournalEvent()
	eventType() JournalEventType
	boundGeneration() Digest
	validate() error
}

type WorkspaceInitializedJournalEvent struct {
	workspaceID      ID
	generation       Digest
	definitionDigest Digest
	planCheckpoint   GitObjectID
	worktreeRoot     WorkspaceWorktreeRootBinding
}

func NewWorkspaceInitializedJournalEvent(
	workspaceID ID,
	generation, definitionDigest Digest,
	worktreeRoot WorkspaceWorktreeRootBinding,
	planCheckpoint ...GitObjectID,
) (WorkspaceInitializedJournalEvent, error) {
	if len(planCheckpoint) > 1 {
		return WorkspaceInitializedJournalEvent{}, fmt.Errorf("workspace initialization accepts one plan checkpoint")
	}
	event := WorkspaceInitializedJournalEvent{
		workspaceID: workspaceID, generation: generation,
		definitionDigest: definitionDigest, worktreeRoot: worktreeRoot,
	}
	if len(planCheckpoint) == 1 {
		event.planCheckpoint = planCheckpoint[0]
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
	return nil
}
func (event WorkspaceInitializedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event WorkspaceInitializedJournalEvent) Generation() Digest { return event.generation }
func (event WorkspaceInitializedJournalEvent) DefinitionDigest() Digest {
	return event.definitionDigest
}
func (event WorkspaceInitializedJournalEvent) PlanCheckpoint() GitObjectID {
	return event.planCheckpoint
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
	readSet    []JournalResourceRevision
	writeSet   []JournalResource
}

func NewJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
	readSet []JournalResourceRevision,
	writeSet []JournalResource,
) (JournalAppend, error) {
	return newJournalAppend(event, occurredAt, readSet, writeSet, false)
}

func newPrivilegedJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
	readSet []JournalResourceRevision,
	writeSet []JournalResource,
) (JournalAppend, error) {
	return newJournalAppend(event, occurredAt, readSet, writeSet, true)
}

func newJournalAppend(
	event WorkspaceJournalEvent,
	occurredAt time.Time,
	readSet []JournalResourceRevision,
	writeSet []JournalResource,
	privileged bool,
) (JournalAppend, error) {
	if event == nil {
		return JournalAppend{}, fmt.Errorf("journal append requires a typed event")
	}
	if !supportedWorkspaceJournalEvent(event) {
		return JournalAppend{}, fmt.Errorf("unsupported workspace journal event %T", event)
	}
	if !privileged {
		switch event.(type) {
		case JournalTailRecoveredEvent:
			return JournalAppend{}, fmt.Errorf("journal recovery events must use the explicit recovery workflow")
		case AttemptOrchestrationAcknowledgedJournalEvent:
			return JournalAppend{}, fmt.Errorf("orchestration acknowledgements must use the idempotent acknowledgement workflow")
		case AttemptNextGoalIntendedJournalEvent:
			return JournalAppend{}, fmt.Errorf("next-goal intents must use the durable intent workflow")
		case AttemptOwnerResponseJournalEvent:
			return JournalAppend{}, fmt.Errorf("owner responses must use the exact-boundary response workflow")
		case AttemptResumedJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt resume must use the verified resume workflow")
		case FeatureRefCreationIntendedJournalEvent, FeatureRefCreatedJournalEvent:
			return JournalAppend{}, fmt.Errorf(
				"feature-ref events must use the recoverable local target initialization workflow",
			)
		case AttemptReservedJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt reservation must use the ref-verified reservation workflow")
		case AttemptMaterializationIntendedJournalEvent:
			return JournalAppend{}, fmt.Errorf("materialization intent must use the reconciled materialization workflow")
		case AttemptStartedJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt start must use the Git-verified materialization workflow")
		case AttemptBoundaryReachedJournalEvent:
			return JournalAppend{}, fmt.Errorf("attempt boundary must use the atomic boundary workflow")
		case CommitProtocolStartedJournalEvent, CommitStepIntendedJournalEvent,
			CommitStepRecordedJournalEvent, CommitCheckRecordedJournalEvent,
			CommitProtocolRebasedJournalEvent, ReviewFixReservedJournalEvent,
			ReviewFixIntendedJournalEvent, ReviewFixCommitRecordedJournalEvent,
			ReviewFixCheckRecordedJournalEvent:
			return JournalAppend{}, fmt.Errorf("commit protocol events must use the Git-verified commit workflow")
		case ReviewHeadAdoptedJournalEvent, ReviewRoundStartedJournalEvent, ReviewInvocationReservedJournalEvent,
			ReviewInvocationFailedJournalEvent, ReviewResultRecordedJournalEvent,
			ReviewFindingFixReservedJournalEvent, ReviewFixAppliedJournalEvent:
			return JournalAppend{}, fmt.Errorf("review events must use the exact-head review workflow")
		case MergeUnitIntegrationIntendedJournalEvent,
			MergeUnitIntegratedJournalEvent:
			return JournalAppend{}, fmt.Errorf(
				"integration events must use the ancestry-checked CAS integration workflow",
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
	reads, err := normalizeJournalReadSet(readSet)
	if err != nil {
		return JournalAppend{}, err
	}
	writes, err := normalizeJournalWriteSet(writeSet)
	if err != nil {
		return JournalAppend{}, err
	}
	if len(writes) == 0 {
		return JournalAppend{}, fmt.Errorf("journal append requires at least one written resource")
	}
	if err := validateJournalEventResources(event, reads, writes); err != nil {
		return JournalAppend{}, err
	}
	return JournalAppend{
		event: cloneWorkspaceJournalEvent(event), occurredAt: occurredAt.UTC(),
		readSet: reads, writeSet: writes,
	}, nil
}

func supportedWorkspaceJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case WorkspaceInitializedJournalEvent, JournalTailRecoveredEvent,
		FeatureRefCreationIntendedJournalEvent, FeatureRefCreatedJournalEvent:
		return true
	default:
		return isAttemptJournalEvent(event) || isCommitJournalEvent(event) ||
			isReviewJournalEvent(event) || isIntegrationJournalEvent(event)
	}
}

func validateJournalEventResources(
	event WorkspaceJournalEvent,
	reads []JournalResourceRevision,
	writes []JournalResource,
) error {
	var expectedReads, expectedWrites []JournalResource
	switch event := event.(type) {
	case WorkspaceInitializedJournalEvent:
		expectedReads = []JournalResource{
			WorkspaceJournalResource(event.workspaceID),
			GenerationJournalResource(event.generation),
		}
		expectedWrites = append([]JournalResource(nil), expectedReads...)
	case JournalTailRecoveredEvent:
		expectedReads = []JournalResource{
			WorkspaceJournalResource(event.workspaceID),
			RecoveryJournalResource(event.workspaceID),
		}
		expectedWrites = append([]JournalResource(nil), expectedReads...)
	default:
		var ok bool
		expectedReads, expectedWrites, ok = localTargetJournalEventResources(event)
		if !ok {
			expectedReads, expectedWrites, ok = attemptJournalEventResources(event)
		}
		if !ok {
			expectedReads, expectedWrites, ok = commitJournalEventResources(event)
		}
		if !ok {
			expectedReads, expectedWrites, ok = reviewJournalEventResources(event)
		}
		if !ok {
			expectedReads, expectedWrites, ok =
				integrationJournalEventResources(event)
		}
		if !ok {
			return fmt.Errorf("unsupported workspace journal event %T", event)
		}
	}
	expectedWrites, _ = normalizeJournalWriteSet(expectedWrites)
	if len(reads) != len(expectedReads) || len(writes) != len(expectedWrites) {
		return fmt.Errorf("journal event %s has an invalid CAS resource set", event.eventType())
	}
	expectedReads, _ = normalizeJournalWriteSet(expectedReads)
	for index := range expectedReads {
		if reads[index].resource != expectedReads[index] {
			return fmt.Errorf("journal event %s has an invalid CAS read set", event.eventType())
		}
	}
	if !equalJournalWriteSets(writes, expectedWrites) {
		return fmt.Errorf("journal event %s has an invalid CAS write set", event.eventType())
	}
	return nil
}

func (appendRequest JournalAppend) Event() WorkspaceJournalEvent {
	return cloneWorkspaceJournalEvent(appendRequest.event)
}
func (appendRequest JournalAppend) OccurredAt() time.Time { return appendRequest.occurredAt }
func (appendRequest JournalAppend) ReadSet() []JournalResourceRevision {
	return append([]JournalResourceRevision(nil), appendRequest.readSet...)
}
func (appendRequest JournalAppend) WriteSet() []JournalResource {
	return append([]JournalResource(nil), appendRequest.writeSet...)
}

type JournalRecord struct {
	sequence     uint64
	occurredAt   time.Time
	previousHash Digest
	eventHash    Digest
	generation   Digest
	readSet      []JournalResourceRevision
	writeSet     []JournalResource
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
func (record JournalRecord) ReadSet() []JournalResourceRevision {
	return append([]JournalResourceRevision(nil), record.readSet...)
}
func (record JournalRecord) WriteSet() []JournalResource {
	return append([]JournalResource(nil), record.writeSet...)
}

func normalizeJournalReadSet(values []JournalResourceRevision) ([]JournalResourceRevision, error) {
	result := append([]JournalResourceRevision(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for _, revision := range result {
		if revision.resource.IsZero() {
			return nil, fmt.Errorf("journal read set contains an invalid resource")
		}
		key := revision.resource.key()
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("journal read set contains duplicate resource %s/%s", revision.resource.kind, revision.resource.identity)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].resource.key() < result[j].resource.key() })
	return result, nil
}

func normalizeJournalWriteSet(values []JournalResource) ([]JournalResource, error) {
	result := append([]JournalResource(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for _, resource := range result {
		if resource.IsZero() {
			return nil, fmt.Errorf("journal write set contains an invalid resource")
		}
		key := resource.key()
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("journal write set contains duplicate resource %s/%s", resource.kind, resource.identity)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key() < result[j].key() })
	return result, nil
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
		return nil
	}
}
