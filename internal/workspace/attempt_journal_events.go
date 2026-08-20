package workspace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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

type AttemptReservedJournalEvent struct {
	workspaceID   ID
	generation    Digest
	attemptID     ID
	mergeUnit     MergeUnitReference
	attemptNumber uint64
	base          GitObjectID
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
	worktree string,
	checkpoint AttemptCheckpointMode,
	escalation AttemptEscalationPolicy,
	serialSegment ID,
	goal GoalBinding,
) (AttemptReservedJournalEvent, error) {
	event := AttemptReservedJournalEvent{
		workspaceID: workspaceID, generation: generation,
		attemptID: attemptID, mergeUnit: mergeUnit, attemptNumber: attemptNumber,
		base: base, worktree: filepath.Clean(worktree),
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
	expectedID, err := deriveAttemptIdentity(
		event.workspaceID, event.generation,
		event.mergeUnit, event.attemptNumber, event.base,
	)
	if err != nil {
		return err
	}
	if event.attemptID != expectedID {
		return fmt.Errorf("attempt identity is invalid")
	}
	return nil
}
func (event AttemptReservedJournalEvent) WorkspaceID() ID                   { return event.workspaceID }
func (event AttemptReservedJournalEvent) Generation() Digest                { return event.generation }
func (event AttemptReservedJournalEvent) AttemptID() ID                     { return event.attemptID }
func (event AttemptReservedJournalEvent) MergeUnit() MergeUnitReference     { return event.mergeUnit }
func (event AttemptReservedJournalEvent) AttemptNumber() uint64             { return event.attemptNumber }
func (event AttemptReservedJournalEvent) Base() GitObjectID                 { return event.base }
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
	worktree    string
}

func NewAttemptMaterializationIntendedJournalEvent(
	workspaceID, attemptID ID,
	generation Digest,
	base GitObjectID,
	worktree string,
) (AttemptMaterializationIntendedJournalEvent, error) {
	event := AttemptMaterializationIntendedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		base: base, worktree: filepath.Clean(worktree),
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
		!filepath.IsAbs(event.worktree) || filepath.Clean(event.worktree) != event.worktree {
		return fmt.Errorf("materialization intent requires attempt, base, and absolute worktree")
	}
	return validateBoundedText("attempt worktree", event.worktree, 4096)
}
func (event AttemptMaterializationIntendedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AttemptMaterializationIntendedJournalEvent) Generation() Digest { return event.generation }
func (event AttemptMaterializationIntendedJournalEvent) AttemptID() ID      { return event.attemptID }
func (event AttemptMaterializationIntendedJournalEvent) Base() GitObjectID  { return event.base }
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

// AttemptStartJournalEvent is the single durable attempt-start boundary. It
// deliberately records the immutable attempt bindings before the detached
// worktree is materialized, so retrying start reconciles the same directory
// rather than reserving a branch or a separate in-progress state.
type AttemptStartJournalEvent struct {
	workspaceID   ID
	generation    Digest
	attemptID     ID
	mergeUnit     MergeUnitReference
	attemptNumber uint64
	base          GitObjectID
	worktree      string
	checkpoint    AttemptCheckpointMode
	escalation    AttemptEscalationPolicy
	serialSegment ID
	leaseID       ID
	goal          GoalBinding
}

func NewAttemptStartJournalEvent(
	workspaceID, attemptID ID,
	generation Digest,
	mergeUnit MergeUnitReference,
	attemptNumber uint64,
	base GitObjectID,
	worktree string,
	checkpoint AttemptCheckpointMode,
	escalation AttemptEscalationPolicy,
	serialSegment, leaseID ID,
	goal GoalBinding,
) (AttemptStartJournalEvent, error) {
	event := AttemptStartJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		mergeUnit: mergeUnit, attemptNumber: attemptNumber, base: base,
		worktree: filepath.Clean(worktree), checkpoint: checkpoint,
		escalation: escalation, serialSegment: serialSegment, leaseID: leaseID,
		goal: goal,
	}
	if err := event.validate(); err != nil {
		return AttemptStartJournalEvent{}, err
	}
	return event, nil
}

func (AttemptStartJournalEvent) isWorkspaceJournalEvent()    {}
func (AttemptStartJournalEvent) eventType() JournalEventType { return JournalEventAttemptStart }
func (event AttemptStartJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AttemptStartJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() ||
		event.attemptID.IsZero() || event.mergeUnit.planID.IsZero() ||
		event.mergeUnit.mergeUnitID.IsZero() || event.attemptNumber == 0 ||
		event.base.IsZero() || !event.checkpoint.valid() ||
		!event.escalation.valid() || event.leaseID.IsZero() || event.goal.IsZero() {
		return fmt.Errorf("attempt start requires immutable workspace, unit, base, lease, and boundary bindings")
	}
	if !filepath.IsAbs(event.worktree) || filepath.Clean(event.worktree) != event.worktree {
		return fmt.Errorf("attempt start requires a clean absolute worktree path")
	}
	if err := validateBoundedText("attempt worktree", event.worktree, 4096); err != nil {
		return err
	}
	expectedID, err := deriveAttemptIdentity(
		event.workspaceID, event.generation, event.mergeUnit, event.attemptNumber, event.base,
	)
	if err != nil || event.attemptID != expectedID {
		return fmt.Errorf("attempt start identity does not match its immutable bindings")
	}
	expectedLease, err := deriveAttemptEpochBinding(event.attemptID, 1)
	if err != nil || event.leaseID != expectedLease {
		return fmt.Errorf("attempt start lease does not match its immutable bindings")
	}
	return nil
}
func (event AttemptStartJournalEvent) WorkspaceID() ID                   { return event.workspaceID }
func (event AttemptStartJournalEvent) Generation() Digest                { return event.generation }
func (event AttemptStartJournalEvent) AttemptID() ID                     { return event.attemptID }
func (event AttemptStartJournalEvent) MergeUnit() MergeUnitReference     { return event.mergeUnit }
func (event AttemptStartJournalEvent) AttemptNumber() uint64             { return event.attemptNumber }
func (event AttemptStartJournalEvent) Base() GitObjectID                 { return event.base }
func (event AttemptStartJournalEvent) Worktree() string                  { return event.worktree }
func (event AttemptStartJournalEvent) Checkpoint() AttemptCheckpointMode { return event.checkpoint }
func (event AttemptStartJournalEvent) Escalation() AttemptEscalationPolicy {
	return event.escalation
}
func (event AttemptStartJournalEvent) SerialSegment() ID { return event.serialSegment }
func (event AttemptStartJournalEvent) LeaseID() ID       { return event.leaseID }
func (event AttemptStartJournalEvent) Goal() GoalBinding { return event.goal }

type AttemptBoundaryReachedJournalEvent struct {
	workspaceID    ID
	generation     Digest
	attemptID      ID
	boundaryID     ID
	ordinal        uint64
	kind           AttemptBoundaryKind
	checkpoint     AttemptCheckpointMode
	serialSegment  ID
	leaseID        ID
	goal           GoalBinding
	head           GitObjectID
	evidence       []Evidence
	evidenceDigest Digest
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
	event := AttemptBoundaryReachedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
		boundaryID: boundaryID, ordinal: ordinal, kind: kind, checkpoint: checkpoint, serialSegment: serialSegment,
		leaseID: leaseID, goal: goal, head: head,
		evidence: evidenceCopy, evidenceDigest: evidenceDigest,
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

type AttemptAbandonedJournalEvent struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
}

func NewAttemptAbandonedJournalEvent(
	workspaceID, attemptID ID,
	generation Digest,
) (AttemptAbandonedJournalEvent, error) {
	event := AttemptAbandonedJournalEvent{
		workspaceID: workspaceID, generation: generation, attemptID: attemptID,
	}
	if err := event.validate(); err != nil {
		return AttemptAbandonedJournalEvent{}, err
	}
	return event, nil
}

func (AttemptAbandonedJournalEvent) isWorkspaceJournalEvent()    {}
func (AttemptAbandonedJournalEvent) eventType() JournalEventType { return JournalEventAttemptAbandoned }
func (event AttemptAbandonedJournalEvent) boundGeneration() Digest {
	return event.generation
}
func (event AttemptAbandonedJournalEvent) validate() error {
	if event.workspaceID.IsZero() || event.generation.IsZero() || event.attemptID.IsZero() {
		return fmt.Errorf("attempt abandonment requires workspace, generation, and attempt bindings")
	}
	return nil
}
func (event AttemptAbandonedJournalEvent) WorkspaceID() ID    { return event.workspaceID }
func (event AttemptAbandonedJournalEvent) Generation() Digest { return event.generation }
func (event AttemptAbandonedJournalEvent) AttemptID() ID      { return event.attemptID }

func isAttemptJournalEvent(event WorkspaceJournalEvent) bool {
	switch event.(type) {
	case AttemptReservedJournalEvent, AttemptMaterializationIntendedJournalEvent,
		AttemptStartedJournalEvent, AttemptStartJournalEvent,
		AttemptBoundaryReachedJournalEvent,
		AttemptResumedJournalEvent, AttemptAbandonedJournalEvent:
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
	case AttemptStartJournalEvent:
		return value
	case AttemptBoundaryReachedJournalEvent:
		value.evidence = cloneEvidence(value.evidence)
		return value
	case AttemptResumedJournalEvent:
		return value
	case AttemptAbandonedJournalEvent:
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

func sortedEvidenceForProjection(values []Evidence) []Evidence {
	result := cloneEvidence(values)
	sort.SliceStable(result, func(i, j int) bool {
		left := result[i].kind.String() + "\x00" + result[i].digest.String()
		right := result[j].kind.String() + "\x00" + result[j].digest.String()
		return left < right
	})
	return result
}
