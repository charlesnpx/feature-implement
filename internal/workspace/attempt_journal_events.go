package workspace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type GoalScope string

const (
	GoalScopeMergeUnit GoalScope = "merge_unit"
	GoalScopeWorkspace GoalScope = "workspace"
)

func (scope GoalScope) valid() bool {
	return scope == GoalScopeMergeUnit || scope == GoalScopeWorkspace
}

type GoalBinding struct {
	id    ID
	scope GoalScope
}

func NewGoalBinding(id ID, scope GoalScope) (GoalBinding, error) {
	if id.IsZero() || !scope.valid() {
		return GoalBinding{}, fmt.Errorf("goal binding requires an identity and supported scope")
	}
	return GoalBinding{id: id, scope: scope}, nil
}

func (goal GoalBinding) ID() ID           { return goal.id }
func (goal GoalBinding) Scope() GoalScope { return goal.scope }
func (goal GoalBinding) IsZero() bool     { return goal.id.IsZero() || !goal.scope.valid() }

type OrchestrationAcknowledgementKind string

const (
	AcknowledgementGoalCompleted   OrchestrationAcknowledgementKind = "goal_completed"
	AcknowledgementNextGoalCreated OrchestrationAcknowledgementKind = "next_goal_created"
)

func (kind OrchestrationAcknowledgementKind) valid() bool {
	return kind == AcknowledgementGoalCompleted || kind == AcknowledgementNextGoalCreated
}

type OwnerBoundaryResponse string

const OwnerBoundaryContinue OwnerBoundaryResponse = "continue"

func (response OwnerBoundaryResponse) valid() bool { return response == OwnerBoundaryContinue }

type AttemptReservedJournalEvent struct {
	workspaceID   ID
	generation    Digest
	attemptID     ID
	mergeUnit     MergeUnitReference
	attemptNumber uint64
	base          GitObjectID
	branch        string
	worktree      string
	checkpoint    AttemptCheckpointMode
	escalation    AttemptEscalationPolicy
	serialSegment ID
	goal          GoalBinding
}

func NewAttemptReservedJournalEvent(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	mergeUnit MergeUnitReference,
	attemptNumber uint64,
	base GitObjectID,
	branch, worktree string,
	checkpoint AttemptCheckpointMode,
	escalation AttemptEscalationPolicy,
	serialSegment ID,
	goal GoalBinding,
) (AttemptReservedJournalEvent, error) {
	event := AttemptReservedJournalEvent{
		workspaceID: workspaceID, generation: generation,
		attemptID: attemptID, mergeUnit: mergeUnit, attemptNumber: attemptNumber,
		base: base, branch: branch, worktree: filepath.Clean(worktree),
		checkpoint: checkpoint, escalation: escalation, serialSegment: serialSegment, goal: goal,
	}
	if err := event.validate(); err != nil {
		return AttemptReservedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptReservedJournalEvent) isWorkspaceJournalEvent()    {}
func (AttemptReservedJournalEvent) eventType() JournalEventType { return JournalEventAttemptReserved }
func (event AttemptReservedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AttemptReservedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.attemptID.IsZero() || event.mergeUnit.planID.IsZero() || event.mergeUnit.mergeUnitID.IsZero() ||
		event.attemptNumber == 0 || event.base.IsZero() || !event.checkpoint.valid() ||
		!event.escalation.valid() || event.goal.IsZero() {
		return fmt.Errorf("attempt reservation requires immutable workspace, generation, unit, attempt, base, and boundary bindings")
	}
	if !filepath.IsAbs(event.worktree) || filepath.Clean(event.worktree) != event.worktree {
		return fmt.Errorf("attempt worktree must be a clean absolute path")
	}
	if err := validateBoundedText("attempt worktree", event.worktree, 4096); err != nil {
		return err
	}
	expectedID, expectedBranch, err := deriveAttemptIdentity(
		event.workspaceID, event.generation,
		event.mergeUnit, event.attemptNumber, event.base,
	)
	if err != nil {
		return err
	}
	if event.attemptID != expectedID || event.branch != expectedBranch {
		return fmt.Errorf("attempt identity or branch does not match its immutable digest bindings")
	}
	return nil
}
func (event AttemptReservedJournalEvent) WorkspaceID() ID                   { return event.workspaceID }
func (event AttemptReservedJournalEvent) Generation() Digest                { return event.generation }
func (event AttemptReservedJournalEvent) AttemptID() ID                     { return event.attemptID }
func (event AttemptReservedJournalEvent) MergeUnit() MergeUnitReference     { return event.mergeUnit }
func (event AttemptReservedJournalEvent) AttemptNumber() uint64             { return event.attemptNumber }
func (event AttemptReservedJournalEvent) Base() GitObjectID                 { return event.base }
func (event AttemptReservedJournalEvent) Branch() string                    { return event.branch }
func (event AttemptReservedJournalEvent) Worktree() string                  { return event.worktree }
func (event AttemptReservedJournalEvent) Checkpoint() AttemptCheckpointMode { return event.checkpoint }
func (event AttemptReservedJournalEvent) Escalation() AttemptEscalationPolicy {
	return event.escalation
}
func (event AttemptReservedJournalEvent) SerialSegment() ID { return event.serialSegment }
func (event AttemptReservedJournalEvent) Goal() GoalBinding { return event.goal }

type AttemptMaterializationIntendedJournalEvent struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	base        GitObjectID
	branch      string
	worktree    string
}

func NewAttemptMaterializationIntendedJournalEvent(
	workspaceID, attemptID ID,
	generation Digest,
	base GitObjectID,
	branch, worktree string,
) (AttemptMaterializationIntendedJournalEvent, error) {
	event := AttemptMaterializationIntendedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		base: base, branch: branch, worktree: filepath.Clean(worktree),
	}
	if err := event.validate(); err != nil {
		return AttemptMaterializationIntendedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptMaterializationIntendedJournalEvent) isWorkspaceJournalEvent() {}
func (AttemptMaterializationIntendedJournalEvent) eventType() JournalEventType {
	return JournalEventAttemptMaterializationIntended
}
func (event AttemptMaterializationIntendedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AttemptMaterializationIntendedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() || event.base.IsZero() ||
		strings.TrimSpace(event.branch) == "" || !filepath.IsAbs(event.worktree) || filepath.Clean(event.worktree) != event.worktree {
		return fmt.Errorf("materialization intent requires attempt, base, branch, and absolute worktree bindings")
	}
	if err := validateBoundedText("attempt branch", event.branch, maxAttemptBranchBytes); err != nil {
		return err
	}
	return validateBoundedText("attempt worktree", event.worktree, 4096)
}
func (event AttemptMaterializationIntendedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AttemptMaterializationIntendedJournalEvent) Generation() Digest { return event.generation }
func (event AttemptMaterializationIntendedJournalEvent) AttemptID() ID      { return event.attemptID }
func (event AttemptMaterializationIntendedJournalEvent) Base() GitObjectID  { return event.base }
func (event AttemptMaterializationIntendedJournalEvent) Branch() string     { return event.branch }
func (event AttemptMaterializationIntendedJournalEvent) Worktree() string   { return event.worktree }

type AttemptStartedJournalEvent struct {
	workspaceID      ID
	generation       Digest
	attemptID        ID
	verifiedHead     GitObjectID
	inspectionDigest Digest
	leaseID          ID
	goal             GoalBinding
}

func NewAttemptStartedJournalEvent(
	workspaceID, attemptID ID,
	generation Digest,
	verifiedHead GitObjectID,
	inspectionDigest Digest,
	leaseID ID,
	goal GoalBinding,
) (AttemptStartedJournalEvent, error) {
	event := AttemptStartedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		verifiedHead: verifiedHead, inspectionDigest: inspectionDigest,
		leaseID: leaseID, goal: goal,
	}
	if err := event.validate(); err != nil {
		return AttemptStartedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptStartedJournalEvent) isWorkspaceJournalEvent()    {}
func (AttemptStartedJournalEvent) eventType() JournalEventType { return JournalEventAttemptStarted }
func (event AttemptStartedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AttemptStartedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.verifiedHead.IsZero() || event.inspectionDigest.IsZero() || event.leaseID.IsZero() ||
		event.goal.IsZero() {
		return fmt.Errorf("attempt start requires verified Git, lease, and goal bindings")
	}
	return nil
}
func (event AttemptStartedJournalEvent) WorkspaceID() ID           { return event.workspaceID }
func (event AttemptStartedJournalEvent) Generation() Digest        { return event.generation }
func (event AttemptStartedJournalEvent) AttemptID() ID             { return event.attemptID }
func (event AttemptStartedJournalEvent) VerifiedHead() GitObjectID { return event.verifiedHead }
func (event AttemptStartedJournalEvent) InspectionDigest() Digest  { return event.inspectionDigest }
func (event AttemptStartedJournalEvent) LeaseID() ID               { return event.leaseID }
func (event AttemptStartedJournalEvent) Goal() GoalBinding         { return event.goal }

type AttemptBoundaryReachedJournalEvent struct {
	workspaceID     ID
	generation      Digest
	attemptID       ID
	boundaryID      ID
	ordinal         uint64
	kind            AttemptBoundaryKind
	checkpoint      AttemptCheckpointMode
	serialSegment   ID
	leaseID         ID
	goal            GoalBinding
	head            GitObjectID
	evidence        []Evidence
	evidenceDigest  Digest
	directiveDigest Digest
	idempotencyKey  Digest
}

func NewAttemptBoundaryReachedJournalEvent(
	workspaceID, attemptID ID,
	generation Digest,
	ordinal uint64,
	kind AttemptBoundaryKind,
	checkpoint AttemptCheckpointMode,
	serialSegment, leaseID ID,
	goal GoalBinding,
	head GitObjectID,
	evidence []Evidence,
) (AttemptBoundaryReachedJournalEvent, error) {
	evidenceCopy := cloneEvidence(evidence)
	evidenceDigest, err := digestBoundaryEvidence(evidenceCopy)
	if err != nil {
		return AttemptBoundaryReachedJournalEvent{}, err
	}
	boundaryID, err := deriveBoundaryID(attemptID, ordinal, head)
	if err != nil {
		return AttemptBoundaryReachedJournalEvent{}, err
	}
	directiveDigest, idempotencyKey, err := deriveBoundaryDirectiveBindings(
		workspaceID, generation, attemptID, boundaryID, kind, checkpoint, goal, head, evidenceDigest,
	)
	if err != nil {
		return AttemptBoundaryReachedJournalEvent{}, err
	}
	event := AttemptBoundaryReachedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		boundaryID: boundaryID, ordinal: ordinal, kind: kind, checkpoint: checkpoint, serialSegment: serialSegment,
		leaseID: leaseID, goal: goal, head: head,
		evidence: evidenceCopy, evidenceDigest: evidenceDigest,
		directiveDigest: directiveDigest, idempotencyKey: idempotencyKey,
	}
	if err := event.validate(); err != nil {
		return AttemptBoundaryReachedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptBoundaryReachedJournalEvent) isWorkspaceJournalEvent() {}
func (AttemptBoundaryReachedJournalEvent) eventType() JournalEventType {
	return JournalEventAttemptBoundary
}
func (event AttemptBoundaryReachedJournalEvent) boundGeneration() Digest { return event.generation }
func (event AttemptBoundaryReachedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.boundaryID.IsZero() || event.ordinal == 0 || !event.kind.valid() || !event.checkpoint.valid() || event.leaseID.IsZero() ||
		event.goal.IsZero() || event.head.IsZero() ||
		len(event.evidence) == 0 || event.evidenceDigest.IsZero() {
		return fmt.Errorf("attempt boundary requires attempt, lease, goal, head, and evidence bindings")
	}
	if event.kind == AttemptBoundaryKindCheckpoint && event.checkpoint == AttemptCheckpointNone ||
		event.kind == AttemptBoundaryKindEscalation && event.checkpoint != AttemptCheckpointPauseOnly {
		return fmt.Errorf("attempt boundary kind does not match its resolved checkpoint shape")
	}
	for _, item := range event.evidence {
		if err := item.validate(); err != nil {
			return fmt.Errorf("boundary evidence: %w", err)
		}
	}
	expectedEvidence, err := digestBoundaryEvidence(event.evidence)
	if err != nil || expectedEvidence != event.evidenceDigest {
		return fmt.Errorf("boundary evidence digest mismatch")
	}
	expectedBoundary, err := deriveBoundaryID(event.attemptID, event.ordinal, event.head)
	if err != nil || expectedBoundary != event.boundaryID {
		return fmt.Errorf("boundary identity does not match its immutable bindings")
	}
	directive, key, err := deriveBoundaryDirectiveBindings(
		event.workspaceID, event.generation, event.attemptID, event.boundaryID, event.kind, event.checkpoint, event.goal, event.head,
		event.evidenceDigest,
	)
	if err != nil || directive != event.directiveDigest || key != event.idempotencyKey {
		return fmt.Errorf("boundary directive bindings are invalid")
	}
	return nil
}
func (event AttemptBoundaryReachedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AttemptBoundaryReachedJournalEvent) Generation() Digest { return event.generation }
func (event AttemptBoundaryReachedJournalEvent) AttemptID() ID      { return event.attemptID }
func (event AttemptBoundaryReachedJournalEvent) BoundaryID() ID     { return event.boundaryID }
func (event AttemptBoundaryReachedJournalEvent) Ordinal() uint64    { return event.ordinal }
func (event AttemptBoundaryReachedJournalEvent) Kind() AttemptBoundaryKind {
	return event.kind
}
func (event AttemptBoundaryReachedJournalEvent) Checkpoint() AttemptCheckpointMode {
	return event.checkpoint
}
func (event AttemptBoundaryReachedJournalEvent) SerialSegment() ID      { return event.serialSegment }
func (event AttemptBoundaryReachedJournalEvent) LeaseID() ID            { return event.leaseID }
func (AttemptBoundaryReachedJournalEvent) FencesAndReleasesLease() bool { return true }
func (event AttemptBoundaryReachedJournalEvent) Goal() GoalBinding      { return event.goal }
func (event AttemptBoundaryReachedJournalEvent) Head() GitObjectID      { return event.head }
func (event AttemptBoundaryReachedJournalEvent) Evidence() []Evidence {
	return cloneEvidence(event.evidence)
}
func (event AttemptBoundaryReachedJournalEvent) EvidenceDigest() Digest { return event.evidenceDigest }
func (event AttemptBoundaryReachedJournalEvent) DirectiveDigest() Digest {
	return event.directiveDigest
}
func (event AttemptBoundaryReachedJournalEvent) IdempotencyKey() Digest { return event.idempotencyKey }

type AttemptNextGoalIntendedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	boundaryID     ID
	goal           GoalBinding
	idempotencyKey Digest
}

func NewAttemptNextGoalIntendedJournalEvent(
	workspaceID, attemptID, boundaryID ID,
	generation Digest,
	goal GoalBinding,
	idempotencyKey Digest,
) (AttemptNextGoalIntendedJournalEvent, error) {
	event := AttemptNextGoalIntendedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		boundaryID: boundaryID, goal: goal, idempotencyKey: idempotencyKey,
	}
	if err := event.validate(); err != nil {
		return AttemptNextGoalIntendedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptNextGoalIntendedJournalEvent) isWorkspaceJournalEvent() {}
func (AttemptNextGoalIntendedJournalEvent) eventType() JournalEventType {
	return JournalEventNextGoalIntended
}
func (event AttemptNextGoalIntendedJournalEvent) boundGeneration() Digest { return event.generation }
func (event AttemptNextGoalIntendedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.boundaryID.IsZero() || event.goal.IsZero() || event.idempotencyKey.IsZero() {
		return fmt.Errorf("next-goal intent requires boundary, goal, and idempotency bindings")
	}
	return nil
}
func (event AttemptNextGoalIntendedJournalEvent) WorkspaceID() ID        { return event.workspaceID }
func (event AttemptNextGoalIntendedJournalEvent) Generation() Digest     { return event.generation }
func (event AttemptNextGoalIntendedJournalEvent) AttemptID() ID          { return event.attemptID }
func (event AttemptNextGoalIntendedJournalEvent) BoundaryID() ID         { return event.boundaryID }
func (event AttemptNextGoalIntendedJournalEvent) Goal() GoalBinding      { return event.goal }
func (event AttemptNextGoalIntendedJournalEvent) IdempotencyKey() Digest { return event.idempotencyKey }

type AttemptOrchestrationAcknowledgedJournalEvent struct {
	workspaceID     ID
	generation      Digest
	attemptID       ID
	boundaryID      ID
	kind            OrchestrationAcknowledgementKind
	directiveDigest Digest
	goal            GoalBinding
	idempotencyKey  Digest
	requestDigest   Digest
}

func NewAttemptOrchestrationAcknowledgedJournalEvent(
	workspaceID, attemptID, boundaryID ID,
	generation Digest,
	kind OrchestrationAcknowledgementKind,
	directiveDigest Digest,
	goal GoalBinding,
	idempotencyKey, requestDigest Digest,
) (AttemptOrchestrationAcknowledgedJournalEvent, error) {
	event := AttemptOrchestrationAcknowledgedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		boundaryID: boundaryID, kind: kind, directiveDigest: directiveDigest,
		goal: goal, idempotencyKey: idempotencyKey,
		requestDigest: requestDigest,
	}
	if err := event.validate(); err != nil {
		return AttemptOrchestrationAcknowledgedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptOrchestrationAcknowledgedJournalEvent) isWorkspaceJournalEvent() {}
func (AttemptOrchestrationAcknowledgedJournalEvent) eventType() JournalEventType {
	return JournalEventOrchestrationAck
}
func (event AttemptOrchestrationAcknowledgedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AttemptOrchestrationAcknowledgedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.boundaryID.IsZero() || !event.kind.valid() || event.goal.IsZero() ||
		event.directiveDigest.IsZero() || event.idempotencyKey.IsZero() ||
		event.requestDigest.IsZero() {
		return fmt.Errorf(
			"acknowledgement requires exact attempt, boundary, directive, goal, idempotency, and request bindings",
		)
	}
	return nil
}
func (event AttemptOrchestrationAcknowledgedJournalEvent) WorkspaceID() ID { return event.workspaceID }
func (event AttemptOrchestrationAcknowledgedJournalEvent) Generation() Digest {
	return event.generation
}
func (event AttemptOrchestrationAcknowledgedJournalEvent) AttemptID() ID  { return event.attemptID }
func (event AttemptOrchestrationAcknowledgedJournalEvent) BoundaryID() ID { return event.boundaryID }
func (event AttemptOrchestrationAcknowledgedJournalEvent) Kind() OrchestrationAcknowledgementKind {
	return event.kind
}
func (event AttemptOrchestrationAcknowledgedJournalEvent) DirectiveDigest() Digest {
	return event.directiveDigest
}
func (event AttemptOrchestrationAcknowledgedJournalEvent) Goal() GoalBinding { return event.goal }
func (event AttemptOrchestrationAcknowledgedJournalEvent) IdempotencyKey() Digest {
	return event.idempotencyKey
}
func (event AttemptOrchestrationAcknowledgedJournalEvent) RequestDigest() Digest {
	return event.requestDigest
}

type AttemptOwnerResponseJournalEvent struct {
	workspaceID     ID
	generation      Digest
	attemptID       ID
	boundaryID      ID
	directiveDigest Digest
	goal            GoalBinding
	expectedHead    GitObjectID
	response        OwnerBoundaryResponse
	requestDigest   Digest
}

func NewAttemptOwnerResponseJournalEvent(
	workspaceID, attemptID, boundaryID ID,
	generation, directiveDigest Digest,
	goal GoalBinding,
	expectedHead GitObjectID,
	response OwnerBoundaryResponse,
	requestDigest Digest,
) (AttemptOwnerResponseJournalEvent, error) {
	event := AttemptOwnerResponseJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		boundaryID: boundaryID, directiveDigest: directiveDigest, goal: goal,
		expectedHead: expectedHead, response: response,
		requestDigest: requestDigest,
	}
	if err := event.validate(); err != nil {
		return AttemptOwnerResponseJournalEvent{}, err
	}
	return event, nil
}

func (AttemptOwnerResponseJournalEvent) isWorkspaceJournalEvent() {}
func (AttemptOwnerResponseJournalEvent) eventType() JournalEventType {
	return JournalEventOwnerResponse
}
func (event AttemptOwnerResponseJournalEvent) boundGeneration() Digest { return event.generation }
func (event AttemptOwnerResponseJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.attemptID.IsZero() || event.boundaryID.IsZero() ||
		event.directiveDigest.IsZero() || event.goal.IsZero() ||
		event.expectedHead.IsZero() || !event.response.valid() ||
		event.requestDigest.IsZero() {
		return fmt.Errorf(
			"owner response requires exact attempt, boundary, directive, goal, head, response, and request bindings",
		)
	}
	return nil
}
func (event AttemptOwnerResponseJournalEvent) WorkspaceID() ID {
	return event.workspaceID
}
func (event AttemptOwnerResponseJournalEvent) Generation() Digest {
	return event.generation
}
func (event AttemptOwnerResponseJournalEvent) AttemptID() ID {
	return event.attemptID
}
func (event AttemptOwnerResponseJournalEvent) BoundaryID() ID {
	return event.boundaryID
}
func (event AttemptOwnerResponseJournalEvent) DirectiveDigest() Digest {
	return event.directiveDigest
}
func (event AttemptOwnerResponseJournalEvent) Goal() GoalBinding {
	return event.goal
}
func (event AttemptOwnerResponseJournalEvent) ExpectedHead() GitObjectID {
	return event.expectedHead
}
func (event AttemptOwnerResponseJournalEvent) Response() OwnerBoundaryResponse {
	return event.response
}
func (event AttemptOwnerResponseJournalEvent) RequestDigest() Digest {
	return event.requestDigest
}

type AttemptResumedJournalEvent struct {
	workspaceID      ID
	generation       Digest
	attemptID        ID
	boundaryID       ID
	verifiedHead     GitObjectID
	inspectionDigest Digest
	leaseID          ID
	goal             GoalBinding
	serialSegment    ID
}

func NewAttemptResumedJournalEvent(
	workspaceID, attemptID, boundaryID ID,
	generation Digest,
	verifiedHead GitObjectID,
	inspectionDigest Digest,
	leaseID ID,
	goal GoalBinding,
	serialSegment ID,
) (AttemptResumedJournalEvent, error) {
	event := AttemptResumedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		boundaryID: boundaryID, verifiedHead: verifiedHead, inspectionDigest: inspectionDigest,
		leaseID: leaseID, goal: goal, serialSegment: serialSegment,
	}
	if err := event.validate(); err != nil {
		return AttemptResumedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptResumedJournalEvent) isWorkspaceJournalEvent()    {}
func (AttemptResumedJournalEvent) eventType() JournalEventType { return JournalEventAttemptResumed }
func (event AttemptResumedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AttemptResumedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() ||
		event.boundaryID.IsZero() || event.verifiedHead.IsZero() || event.inspectionDigest.IsZero() ||
		event.leaseID.IsZero() || event.goal.IsZero() {
		return fmt.Errorf("attempt resume requires boundary, verified Git, lease, and goal bindings")
	}
	return nil
}
func (event AttemptResumedJournalEvent) WorkspaceID() ID           { return event.workspaceID }
func (event AttemptResumedJournalEvent) Generation() Digest        { return event.generation }
func (event AttemptResumedJournalEvent) AttemptID() ID             { return event.attemptID }
func (event AttemptResumedJournalEvent) BoundaryID() ID            { return event.boundaryID }
func (event AttemptResumedJournalEvent) VerifiedHead() GitObjectID { return event.verifiedHead }
func (event AttemptResumedJournalEvent) InspectionDigest() Digest  { return event.inspectionDigest }
func (event AttemptResumedJournalEvent) LeaseID() ID               { return event.leaseID }
func (event AttemptResumedJournalEvent) Goal() GoalBinding         { return event.goal }
func (event AttemptResumedJournalEvent) SerialSegment() ID         { return event.serialSegment }

func isAttemptJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case AttemptReservedJournalEvent, AttemptMaterializationIntendedJournalEvent,
		AttemptStartedJournalEvent, AttemptBoundaryReachedJournalEvent,
		AttemptNextGoalIntendedJournalEvent,
		AttemptOrchestrationAcknowledgedJournalEvent,
		AttemptOwnerResponseJournalEvent,
		AttemptResumedJournalEvent:
		return true
	default:
		return false
	}
}

func cloneAttemptJournalEvent(event WorkspaceJournalEvent) WorkspaceJournalEvent {
	switch value := event.(type) {
	case AttemptReservedJournalEvent:
		return value
	case AttemptMaterializationIntendedJournalEvent:
		return value
	case AttemptStartedJournalEvent:
		return value
	case AttemptBoundaryReachedJournalEvent:
		value.evidence = cloneEvidence(value.evidence)
		return value
	case AttemptNextGoalIntendedJournalEvent:
		return value
	case AttemptOrchestrationAcknowledgedJournalEvent:
		return value
	case AttemptOwnerResponseJournalEvent:
		return value
	case AttemptResumedJournalEvent:
		return value
	default:
		return nil
	}
}

func digestBoundaryEvidence(evidence []Evidence) (Digest, error) {
	if len(evidence) == 0 {
		return Digest{}, fmt.Errorf("attempt boundary requires typed evidence")
	}
	type itemJSON struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	type evidenceJSON struct {
		Kind   string     `json:"kind"`
		Digest string     `json:"digest"`
		Items  []itemJSON `json:"items"`
	}
	values := make([]evidenceJSON, 0, len(evidence))
	for _, value := range evidence {
		if err := value.validate(); err != nil {
			return Digest{}, err
		}
		items := make([]itemJSON, 0, len(value.items))
		for _, item := range value.items {
			items = append(items, itemJSON{Name: item.name.String(), Value: item.value})
		}
		values = append(values, evidenceJSON{Kind: value.kind.String(), Digest: value.digest.String(), Items: items})
	}
	content, err := json.Marshal(values)
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func deriveBoundaryID(attemptID ID, ordinal uint64, head GitObjectID) (ID, error) {
	if attemptID.IsZero() || ordinal == 0 || head.IsZero() {
		return ID{}, fmt.Errorf("boundary identity requires attempt, ordinal, and head")
	}
	digest := DigestBytes([]byte(fmt.Sprintf(
		"attempt_boundary_v2\nattempt_id=%s\nordinal=%d\nhead=%s\n",
		attemptID, ordinal, head.String(),
	)))
	return NewID("boundary-" + hex.EncodeToString(digest.Bytes()[:8]))
}

func deriveBoundaryDirectiveBindings(
	workspaceID ID,
	generation Digest,
	attemptID, boundaryID ID,
	kind AttemptBoundaryKind,
	checkpoint AttemptCheckpointMode,
	goal GoalBinding,
	head GitObjectID,
	evidenceDigest Digest,
) (Digest, Digest, error) {
	if workspaceID.IsZero() || generation.IsZero() || attemptID.IsZero() || boundaryID.IsZero() ||
		!kind.valid() || !checkpoint.valid() || goal.IsZero() || head.IsZero() || evidenceDigest.IsZero() {
		return Digest{}, Digest{}, fmt.Errorf("boundary directive requires immutable boundary bindings")
	}
	type directiveJSON struct {
		SchemaVersion int                   `json:"schema_version"`
		Kind          AttemptBoundaryKind   `json:"kind"`
		Checkpoint    AttemptCheckpointMode `json:"checkpoint"`
		WorkspaceID   string                `json:"workspace_id"`
		Generation    string                `json:"generation"`
		AttemptID     string                `json:"attempt_id"`
		BoundaryID    string                `json:"boundary_id"`
		GoalID        string                `json:"goal_id"`
		GoalScope     GoalScope             `json:"goal_scope"`
		Head          string                `json:"head"`
		Evidence      string                `json:"evidence_digest"`
	}
	content, err := json.Marshal(directiveJSON{
		SchemaVersion: JournalSchemaVersion, Kind: kind, Checkpoint: checkpoint, WorkspaceID: workspaceID.String(),
		Generation: generation.String(), AttemptID: attemptID.String(), BoundaryID: boundaryID.String(),
		GoalID: goal.id.String(), GoalScope: goal.scope, Head: head.String(), Evidence: evidenceDigest.String(),
	})
	if err != nil {
		return Digest{}, Digest{}, err
	}
	directive := DigestBytes(content)
	if checkpoint != AttemptCheckpointCompleteGoalAndWait {
		return directive, Digest{}, nil
	}
	key := DigestBytes([]byte("complete_goal_and_wait_v2\n" + directive.String() + "\n"))
	return directive, key, nil
}

func deriveNextGoalIdempotencyKey(
	workspaceID ID,
	generation Digest,
	attemptID, boundaryID ID,
	boundaryDigest Digest,
	goal GoalBinding,
) (Digest, error) {
	if workspaceID.IsZero() || generation.IsZero() || attemptID.IsZero() || boundaryID.IsZero() ||
		boundaryDigest.IsZero() || goal.IsZero() {
		return Digest{}, fmt.Errorf("next-goal idempotency requires workspace, generation, attempt, boundary, and goal")
	}
	return DigestBytes([]byte(fmt.Sprintf(
		"next_goal_created_v2\nworkspace_id=%s\ngeneration=%s\nattempt_id=%s\nboundary_id=%s\nboundary_digest=%s\ngoal_scope=%s\ngoal_id=%s\n",
		workspaceID, generation, attemptID, boundaryID, boundaryDigest, goal.scope, goal.id,
	))), nil
}

func deriveOwnerResponseRequestDigest(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	boundary RuntimeBoundaryProjection,
	response OwnerBoundaryResponse,
) (Digest, error) {
	if workspaceID.IsZero() || generation.IsZero() || attemptID.IsZero() ||
		boundary.boundaryID.IsZero() || boundary.directiveDigest.IsZero() ||
		boundary.goal.IsZero() || boundary.head.IsZero() ||
		boundary.evidenceDigest.IsZero() || !response.valid() {
		return Digest{}, fmt.Errorf(
			"owner response requires a complete exact boundary and closed response",
		)
	}
	if boundary.checkpoint == AttemptCheckpointCompleteGoalAndWait &&
		(!boundary.goalCompletedOK ||
			boundary.goalCompleted.requestDigest.IsZero()) {
		return Digest{}, fmt.Errorf(
			"owner response requires the durable goal-completion acknowledgement",
		)
	}
	type requestJSON struct {
		SchemaVersion int                   `json:"schema_version"`
		Kind          string                `json:"kind"`
		WorkspaceID   string                `json:"workspace_id"`
		Generation    string                `json:"generation"`
		AttemptID     string                `json:"attempt_id"`
		BoundaryID    string                `json:"boundary_id"`
		Checkpoint    AttemptCheckpointMode `json:"checkpoint"`
		Directive     string                `json:"directive_digest"`
		GoalID        string                `json:"goal_id"`
		GoalScope     GoalScope             `json:"goal_scope"`
		ExpectedHead  string                `json:"expected_head"`
		Evidence      string                `json:"evidence_digest"`
		CompletionAck string                `json:"completion_acknowledgement_digest,omitempty"`
		Response      OwnerBoundaryResponse `json:"response"`
	}
	content, err := json.Marshal(requestJSON{
		SchemaVersion: JournalSchemaVersion,
		Kind:          "attempt_owner_response",
		WorkspaceID:   workspaceID.String(),
		Generation:    generation.String(),
		AttemptID:     attemptID.String(),
		BoundaryID:    boundary.boundaryID.String(),
		Checkpoint:    boundary.checkpoint,
		Directive:     boundary.directiveDigest.String(),
		GoalID:        boundary.goal.id.String(),
		GoalScope:     boundary.goal.scope,
		ExpectedHead:  boundary.head.String(),
		Evidence:      boundary.evidenceDigest.String(),
		CompletionAck: boundary.goalCompleted.requestDigest.String(),
		Response:      response,
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func deriveOrchestrationAcknowledgementRequestDigest(
	workspaceID ID,
	generation Digest,
	attemptID ID,
	boundary RuntimeBoundaryProjection,
	kind OrchestrationAcknowledgementKind,
	goal GoalBinding,
	idempotencyKey Digest,
) (Digest, error) {
	if workspaceID.IsZero() || generation.IsZero() || attemptID.IsZero() || boundary.boundaryID.IsZero() ||
		!kind.valid() || goal.IsZero() || idempotencyKey.IsZero() || boundary.head.IsZero() ||
		boundary.directiveDigest.IsZero() {
		return Digest{}, fmt.Errorf("orchestration acknowledgement requires immutable directive bindings")
	}
	type requestJSON struct {
		SchemaVersion  int                              `json:"schema_version"`
		Kind           OrchestrationAcknowledgementKind `json:"kind"`
		WorkspaceID    string                           `json:"workspace_id"`
		Generation     string                           `json:"generation"`
		AttemptID      string                           `json:"attempt_id"`
		BoundaryID     string                           `json:"boundary_id"`
		GoalID         string                           `json:"goal_id"`
		GoalScope      GoalScope                        `json:"goal_scope"`
		Head           string                           `json:"head"`
		Directive      string                           `json:"directive_digest"`
		IdempotencyKey string                           `json:"idempotency_key"`
	}
	content, err := json.Marshal(requestJSON{
		SchemaVersion: JournalSchemaVersion, Kind: kind, WorkspaceID: workspaceID.String(),
		Generation: generation.String(), AttemptID: attemptID.String(), BoundaryID: boundary.boundaryID.String(),
		GoalID: goal.id.String(), GoalScope: goal.scope, Head: boundary.head.String(),
		Directive: boundary.directiveDigest.String(), IdempotencyKey: idempotencyKey.String(),
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func sortedEvidenceForProjection(values []Evidence) []Evidence {
	result := cloneEvidence(values)
	sort.SliceStable(result, func(i, j int) bool {
		left := result[i].kind.String() + "\x00" + result[i].digest.String()
		right := result[j].kind.String() + "\x00" + result[j].digest.String()
		return left < right
	})
	return result
}
