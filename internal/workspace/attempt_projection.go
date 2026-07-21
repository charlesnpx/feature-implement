package workspace

import (
	"encoding/json"
	"fmt"
)

type RuntimeOrchestrationAcknowledgement struct {
	record                uint64
	kind                  OrchestrationAcknowledgementKind
	goal                  GoalBinding
	idempotencyKey        Digest
	acknowledgementDigest Digest
}

func (acknowledgement RuntimeOrchestrationAcknowledgement) Record() uint64 {
	return acknowledgement.record
}
func (acknowledgement RuntimeOrchestrationAcknowledgement) Kind() OrchestrationAcknowledgementKind {
	return acknowledgement.kind
}
func (acknowledgement RuntimeOrchestrationAcknowledgement) Goal() GoalBinding {
	return acknowledgement.goal
}
func (acknowledgement RuntimeOrchestrationAcknowledgement) IdempotencyKey() Digest {
	return acknowledgement.idempotencyKey
}
func (acknowledgement RuntimeOrchestrationAcknowledgement) AcknowledgementDigest() Digest {
	return acknowledgement.acknowledgementDigest
}

type RuntimeOwnerBoundaryResponse struct {
	record        uint64
	response      OwnerBoundaryResponse
	requestDigest Digest
	receiptDigest Digest
}

func (response RuntimeOwnerBoundaryResponse) Record() uint64 { return response.record }
func (response RuntimeOwnerBoundaryResponse) Response() OwnerBoundaryResponse {
	return response.response
}
func (response RuntimeOwnerBoundaryResponse) RequestDigest() Digest { return response.requestDigest }
func (response RuntimeOwnerBoundaryResponse) ReceiptDigest() Digest { return response.receiptDigest }

type RuntimeNextGoalIntent struct {
	record         uint64
	goal           GoalBinding
	idempotencyKey Digest
}

func (intent RuntimeNextGoalIntent) Record() uint64         { return intent.record }
func (intent RuntimeNextGoalIntent) Goal() GoalBinding      { return intent.goal }
func (intent RuntimeNextGoalIntent) IdempotencyKey() Digest { return intent.idempotencyKey }

type RuntimeBoundaryProjection struct {
	boundaryID       ID
	ordinal          uint64
	record           uint64
	resumedRecord    uint64
	mode             AttemptBoundaryMode
	serialSegment    ID
	leaseID          ID
	authorizationID  ID
	goal             GoalBinding
	head             GitObjectID
	evidence         []Evidence
	evidenceDigest   Digest
	directiveDigest  Digest
	idempotencyKey   Digest
	goalCompleted    RuntimeOrchestrationAcknowledgement
	goalCompletedOK  bool
	ownerResponse    RuntimeOwnerBoundaryResponse
	ownerResponseOK  bool
	nextGoalIntent   RuntimeNextGoalIntent
	nextGoalIntentOK bool
	nextGoal         RuntimeOrchestrationAcknowledgement
	nextGoalOK       bool
}

func (boundary RuntimeBoundaryProjection) BoundaryID() ID            { return boundary.boundaryID }
func (boundary RuntimeBoundaryProjection) Ordinal() uint64           { return boundary.ordinal }
func (boundary RuntimeBoundaryProjection) Record() uint64            { return boundary.record }
func (boundary RuntimeBoundaryProjection) ResumedRecord() uint64     { return boundary.resumedRecord }
func (boundary RuntimeBoundaryProjection) Mode() AttemptBoundaryMode { return boundary.mode }
func (boundary RuntimeBoundaryProjection) SerialSegment() ID         { return boundary.serialSegment }
func (boundary RuntimeBoundaryProjection) LeaseID() ID               { return boundary.leaseID }
func (boundary RuntimeBoundaryProjection) AuthorizationID() ID       { return boundary.authorizationID }
func (boundary RuntimeBoundaryProjection) AuthorizationClosed() bool { return boundary.record != 0 }
func (boundary RuntimeBoundaryProjection) LeaseFencedAndReleased() bool {
	return boundary.record != 0
}
func (boundary RuntimeBoundaryProjection) Goal() GoalBinding { return boundary.goal }
func (boundary RuntimeBoundaryProjection) Head() GitObjectID { return boundary.head }
func (boundary RuntimeBoundaryProjection) Evidence() []Evidence {
	return cloneEvidence(boundary.evidence)
}
func (boundary RuntimeBoundaryProjection) EvidenceDigest() Digest  { return boundary.evidenceDigest }
func (boundary RuntimeBoundaryProjection) DirectiveDigest() Digest { return boundary.directiveDigest }
func (boundary RuntimeBoundaryProjection) IdempotencyKey() Digest  { return boundary.idempotencyKey }
func (boundary RuntimeBoundaryProjection) GoalCompletion() (RuntimeOrchestrationAcknowledgement, bool) {
	return boundary.goalCompleted, boundary.goalCompletedOK
}
func (boundary RuntimeBoundaryProjection) OwnerResponse() (RuntimeOwnerBoundaryResponse, bool) {
	return boundary.ownerResponse, boundary.ownerResponseOK
}
func (boundary RuntimeBoundaryProjection) NextGoalIntent() (RuntimeNextGoalIntent, bool) {
	return boundary.nextGoalIntent, boundary.nextGoalIntentOK
}
func (boundary RuntimeBoundaryProjection) NextGoalCreation() (RuntimeOrchestrationAcknowledgement, bool) {
	return boundary.nextGoal, boundary.nextGoalOK
}

type RuntimeAttemptProjection struct {
	attemptID             ID
	mergeUnit             MergeUnitReference
	generation            Digest
	repository            RepositoryIdentity
	attemptNumber         uint64
	base                  GitObjectID
	branch                string
	worktree              string
	boundaryMode          AttemptBoundaryMode
	serialSegment         ID
	serialSegmentHeld     bool
	phase                 AttemptRuntimePhase
	reservationRecord     uint64
	materializationRecord uint64
	startRecord           uint64
	verifiedHead          GitObjectID
	inspectionDigest      Digest
	leaseID               ID
	authorizationID       ID
	goal                  GoalBinding
	boundaries            []RuntimeBoundaryProjection
	commitProtocol        *CommitProtocolState
}

func (attempt RuntimeAttemptProjection) AttemptID() ID                  { return attempt.attemptID }
func (attempt RuntimeAttemptProjection) MergeUnit() MergeUnitReference  { return attempt.mergeUnit }
func (attempt RuntimeAttemptProjection) Generation() Digest             { return attempt.generation }
func (attempt RuntimeAttemptProjection) Repository() RepositoryIdentity { return attempt.repository }
func (attempt RuntimeAttemptProjection) AttemptNumber() uint64          { return attempt.attemptNumber }
func (attempt RuntimeAttemptProjection) Base() GitObjectID              { return attempt.base }
func (attempt RuntimeAttemptProjection) Branch() string                 { return attempt.branch }
func (attempt RuntimeAttemptProjection) Worktree() string               { return attempt.worktree }
func (attempt RuntimeAttemptProjection) BoundaryMode() AttemptBoundaryMode {
	return attempt.boundaryMode
}
func (attempt RuntimeAttemptProjection) SerialSegment() ID          { return attempt.serialSegment }
func (attempt RuntimeAttemptProjection) SerialSegmentHeld() bool    { return attempt.serialSegmentHeld }
func (attempt RuntimeAttemptProjection) Phase() AttemptRuntimePhase { return attempt.phase }
func (attempt RuntimeAttemptProjection) ReservationRecord() uint64  { return attempt.reservationRecord }
func (attempt RuntimeAttemptProjection) MaterializationRecord() uint64 {
	return attempt.materializationRecord
}
func (attempt RuntimeAttemptProjection) StartRecord() uint64       { return attempt.startRecord }
func (attempt RuntimeAttemptProjection) VerifiedHead() GitObjectID { return attempt.verifiedHead }
func (attempt RuntimeAttemptProjection) InspectionDigest() Digest  { return attempt.inspectionDigest }
func (attempt RuntimeAttemptProjection) LeaseID() ID               { return attempt.leaseID }
func (attempt RuntimeAttemptProjection) AuthorizationID() ID       { return attempt.authorizationID }
func (attempt RuntimeAttemptProjection) Goal() GoalBinding         { return attempt.goal }
func (attempt RuntimeAttemptProjection) Boundaries() []RuntimeBoundaryProjection {
	return cloneRuntimeBoundaries(attempt.boundaries)
}
func (attempt RuntimeAttemptProjection) CommitProtocol() (CommitProtocolState, bool) {
	if attempt.commitProtocol == nil {
		return CommitProtocolState{}, false
	}
	return cloneCommitProtocolState(*attempt.commitProtocol), true
}
func (attempt RuntimeAttemptProjection) CurrentBoundary() (RuntimeBoundaryProjection, bool) {
	if attempt.phase != AttemptPaused || len(attempt.boundaries) == 0 {
		return RuntimeBoundaryProjection{}, false
	}
	boundary := attempt.boundaries[len(attempt.boundaries)-1]
	if boundary.resumedRecord != 0 {
		return RuntimeBoundaryProjection{}, false
	}
	return cloneRuntimeBoundary(boundary), true
}

func (projection WorkspaceRuntimeProjection) Attempts() []RuntimeAttemptProjection {
	return cloneRuntimeAttempts(projection.attempts)
}

func (projection WorkspaceRuntimeProjection) Attempt(attemptID ID) (RuntimeAttemptProjection, bool) {
	for _, attempt := range projection.attempts {
		if attempt.attemptID == attemptID {
			return cloneRuntimeAttempt(attempt), true
		}
	}
	return RuntimeAttemptProjection{}, false
}

func (projection WorkspaceRuntimeProjection) AttemptGenerationBindings() []AttemptGenerationBinding {
	result := make([]AttemptGenerationBinding, 0, len(projection.attempts))
	for _, attempt := range projection.attempts {
		result = append(result, AttemptGenerationBinding{
			attemptID: attempt.attemptID, mergeUnit: attempt.mergeUnit,
			generation: attempt.generation, phase: attempt.phase,
		})
	}
	return result
}

func reduceAttemptRuntime(
	current WorkspaceRuntimeProjection,
	next *WorkspaceRuntimeProjection,
	record JournalRecord,
) error {
	if next == nil || current.workspaceID.IsZero() || current.activeGeneration.IsZero() {
		return fmt.Errorf("attempt events require an initialized workspace runtime")
	}
	if record.generation != current.activeGeneration {
		return fmt.Errorf("attempt event generation is not active")
	}
	switch event := record.event.(type) {
	case AttemptReservedJournalEvent:
		if event.workspaceID != current.workspaceID || event.generation != current.activeGeneration {
			return fmt.Errorf("attempt reservation does not match the active workspace generation")
		}
		if _, exists := findRuntimeAttempt(current.attempts, event.attemptID); exists {
			return fmt.Errorf("attempt %s is already reserved", event.attemptID)
		}
		for _, attempt := range current.attempts {
			if attempt.mergeUnit == event.mergeUnit && attempt.phase.nonterminal() {
				return fmt.Errorf("merge unit %s already has nonterminal attempt %s", event.mergeUnit, attempt.attemptID)
			}
			if !event.serialSegment.IsZero() && attempt.serialSegmentHeld && attempt.serialSegment == event.serialSegment {
				return fmt.Errorf("serial segment %s is held by attempt %s", event.serialSegment, attempt.attemptID)
			}
		}
		next.attempts = append(next.attempts, RuntimeAttemptProjection{
			attemptID: event.attemptID, mergeUnit: event.mergeUnit, generation: event.generation,
			repository: event.repository, attemptNumber: event.attemptNumber, base: event.base,
			branch: event.branch, worktree: event.worktree, boundaryMode: event.boundaryMode,
			serialSegment: event.serialSegment, serialSegmentHeld: !event.serialSegment.IsZero(),
			goal: event.goal, phase: AttemptReserved, reservationRecord: record.sequence,
		})
		return nil
	case AttemptMaterializationIntendedJournalEvent:
		index, attempt, err := requireRuntimeAttempt(current, event.attemptID, event.workspaceID, event.generation)
		if err != nil {
			return err
		}
		if attempt.phase != AttemptReserved || attempt.base != event.base || attempt.branch != event.branch || attempt.worktree != event.worktree {
			return fmt.Errorf("attempt materialization intent does not match a reserved attempt")
		}
		next.attempts[index].phase = AttemptMaterializing
		next.attempts[index].materializationRecord = record.sequence
		return nil
	case AttemptStartedJournalEvent:
		index, attempt, err := requireRuntimeAttempt(current, event.attemptID, event.workspaceID, event.generation)
		if err != nil {
			return err
		}
		if attempt.phase != AttemptMaterializing || attempt.base != event.verifiedHead || attempt.goal != event.goal {
			return fmt.Errorf("attempt start requires materializing state at the reserved base")
		}
		if err := ensureRuntimeBindingAvailable(current.attempts, attempt.attemptID, event.leaseID, event.authorizationID); err != nil {
			return err
		}
		updated := &next.attempts[index]
		updated.phase, updated.startRecord = AttemptActive, record.sequence
		updated.verifiedHead, updated.inspectionDigest = event.verifiedHead, event.inspectionDigest
		updated.leaseID, updated.authorizationID, updated.goal = event.leaseID, event.authorizationID, event.goal
		return nil
	case AttemptBoundaryReachedJournalEvent:
		index, attempt, err := requireRuntimeAttempt(current, event.attemptID, event.workspaceID, event.generation)
		if err != nil {
			return err
		}
		if attempt.phase != AttemptActive || attempt.leaseID != event.leaseID ||
			attempt.authorizationID != event.authorizationID || attempt.goal != event.goal ||
			attempt.boundaryMode != event.mode || attempt.serialSegment != event.serialSegment ||
			event.ordinal != uint64(len(attempt.boundaries)+1) {
			return fmt.Errorf("attempt boundary does not match the active lease, authorization, goal, policy, and ordinal")
		}
		updated := &next.attempts[index]
		updated.phase = AttemptPaused
		updated.serialSegmentHeld = false
		updated.verifiedHead = event.head
		updated.leaseID, updated.authorizationID = ID{}, ID{}
		updated.boundaries = append(updated.boundaries, RuntimeBoundaryProjection{
			boundaryID: event.boundaryID, ordinal: event.ordinal, record: record.sequence,
			mode: event.mode, serialSegment: event.serialSegment,
			leaseID: event.leaseID, authorizationID: event.authorizationID,
			goal: event.goal, head: event.head, evidence: cloneEvidence(event.evidence),
			evidenceDigest: event.evidenceDigest, directiveDigest: event.directiveDigest,
			idempotencyKey: event.idempotencyKey,
		})
		return nil
	case AttemptNextGoalIntendedJournalEvent:
		index, _, boundaryIndex, boundary, err := requireCurrentRuntimeBoundary(
			current, event.workspaceID, event.generation, event.attemptID, event.boundaryID,
		)
		if err != nil {
			return err
		}
		if boundary.mode != AttemptBoundaryCompleteGoalAndWait || !boundary.goalCompletedOK ||
			!boundary.ownerResponseOK || boundary.nextGoalIntentOK || event.goal == boundary.goal {
			return fmt.Errorf("next-goal intent is out of order, duplicate, or reuses the completed goal")
		}
		expected, err := deriveNextGoalIdempotencyKey(
			event.workspaceID, event.generation, event.attemptID, boundary.boundaryID,
			boundary.directiveDigest, event.goal,
		)
		if err != nil || expected != event.idempotencyKey {
			return fmt.Errorf("next-goal intent has an invalid idempotency key")
		}
		updated := &next.attempts[index].boundaries[boundaryIndex]
		updated.nextGoalIntent = RuntimeNextGoalIntent{
			record: record.sequence, goal: event.goal, idempotencyKey: event.idempotencyKey,
		}
		updated.nextGoalIntentOK = true
		return nil
	case AttemptOrchestrationAcknowledgedJournalEvent:
		index, attempt, boundaryIndex, boundary, err := requireCurrentRuntimeBoundary(
			current, event.workspaceID, event.generation, event.attemptID, event.boundaryID,
		)
		if err != nil {
			return err
		}
		if boundary.mode != AttemptBoundaryCompleteGoalAndWait {
			return fmt.Errorf("pause-only boundary %s cannot acknowledge goal completion or creation", boundary.boundaryID)
		}
		acknowledgement := RuntimeOrchestrationAcknowledgement{
			record: record.sequence, kind: event.kind, goal: event.goal,
			idempotencyKey: event.idempotencyKey, acknowledgementDigest: event.acknowledgementDigest,
		}
		updated := &next.attempts[index].boundaries[boundaryIndex]
		switch event.kind {
		case AcknowledgementGoalCompleted:
			if boundary.goalCompletedOK || event.goal != boundary.goal || event.idempotencyKey != boundary.idempotencyKey {
				return fmt.Errorf("goal-completion acknowledgement does not match the pending boundary directive")
			}
			updated.goalCompleted, updated.goalCompletedOK = acknowledgement, true
		case AcknowledgementNextGoalCreated:
			if boundary.nextGoalOK || !boundary.nextGoalIntentOK ||
				event.goal != boundary.nextGoalIntent.goal || event.idempotencyKey != boundary.nextGoalIntent.idempotencyKey {
				return fmt.Errorf("next-goal acknowledgement does not match the durable creation intent")
			}
			updated.nextGoal, updated.nextGoalOK = acknowledgement, true
		default:
			return fmt.Errorf("unsupported orchestration acknowledgement %q", event.kind)
		}
		_ = attempt
		return nil
	case AttemptOwnerResponseJournalEvent:
		index, _, boundaryIndex, boundary, err := requireCurrentRuntimeBoundary(
			current, event.workspaceID, event.generation, event.attemptID, event.boundaryID,
		)
		if err != nil {
			return err
		}
		if boundary.ownerResponseOK || boundary.mode == AttemptBoundaryCompleteGoalAndWait && !boundary.goalCompletedOK {
			return fmt.Errorf("owner response is duplicate or precedes goal-completion acknowledgement")
		}
		expected, err := deriveOwnerResponseRequestDigest(
			event.workspaceID, event.generation, event.attemptID, boundary, event.response,
		)
		if err != nil || expected != event.requestDigest {
			return fmt.Errorf("owner response request digest does not match its boundary")
		}
		updated := &next.attempts[index].boundaries[boundaryIndex]
		updated.ownerResponse = RuntimeOwnerBoundaryResponse{
			record: record.sequence, response: event.response,
			requestDigest: event.requestDigest, receiptDigest: event.receiptDigest,
		}
		updated.ownerResponseOK = true
		return nil
	case AttemptResumedJournalEvent:
		index, attempt, boundaryIndex, boundary, err := requireCurrentRuntimeBoundary(
			current, event.workspaceID, event.generation, event.attemptID, event.boundaryID,
		)
		if err != nil {
			return err
		}
		if !boundary.ownerResponseOK || event.verifiedHead != boundary.head || event.serialSegment != attempt.serialSegment {
			return fmt.Errorf("attempt resume requires owner response, unchanged head, and matching serial policy")
		}
		if boundary.mode == AttemptBoundaryCompleteGoalAndWait {
			if !boundary.nextGoalOK || event.goal != boundary.nextGoal.goal {
				return fmt.Errorf("complete-goal boundary resume requires acknowledged next goal")
			}
		} else if event.goal != boundary.goal {
			return fmt.Errorf("pause-only boundary must resume the same goal")
		}
		for _, other := range current.attempts {
			if !event.serialSegment.IsZero() && other.attemptID != attempt.attemptID &&
				other.serialSegmentHeld && other.serialSegment == event.serialSegment {
				return fmt.Errorf("serial segment %s is held by attempt %s", event.serialSegment, other.attemptID)
			}
		}
		if err := ensureRuntimeBindingAvailable(current.attempts, attempt.attemptID, event.leaseID, event.authorizationID); err != nil {
			return err
		}
		updated := &next.attempts[index]
		updated.phase = AttemptActive
		updated.serialSegmentHeld = !event.serialSegment.IsZero()
		updated.verifiedHead, updated.inspectionDigest = event.verifiedHead, event.inspectionDigest
		updated.leaseID, updated.authorizationID, updated.goal = event.leaseID, event.authorizationID, event.goal
		updated.boundaries[boundaryIndex].resumedRecord = record.sequence
		return nil
	default:
		return fmt.Errorf("unsupported attempt runtime event %T", record.event)
	}
}

func findRuntimeAttempt(values []RuntimeAttemptProjection, attemptID ID) (int, bool) {
	for index, attempt := range values {
		if attempt.attemptID == attemptID {
			return index, true
		}
	}
	return -1, false
}

func requireRuntimeAttempt(
	projection WorkspaceRuntimeProjection,
	attemptID, workspaceID ID,
	generation Digest,
) (int, RuntimeAttemptProjection, error) {
	if projection.workspaceID != workspaceID || projection.activeGeneration != generation {
		return -1, RuntimeAttemptProjection{}, fmt.Errorf("attempt event does not match the active workspace generation")
	}
	index, exists := findRuntimeAttempt(projection.attempts, attemptID)
	if !exists {
		return -1, RuntimeAttemptProjection{}, fmt.Errorf("attempt %s is not reserved", attemptID)
	}
	return index, projection.attempts[index], nil
}

func requireCurrentRuntimeBoundary(
	projection WorkspaceRuntimeProjection,
	workspaceID ID,
	generation Digest,
	attemptID, boundaryID ID,
) (int, RuntimeAttemptProjection, int, RuntimeBoundaryProjection, error) {
	index, attempt, err := requireRuntimeAttempt(projection, attemptID, workspaceID, generation)
	if err != nil {
		return -1, RuntimeAttemptProjection{}, -1, RuntimeBoundaryProjection{}, err
	}
	if attempt.phase != AttemptPaused || len(attempt.boundaries) == 0 {
		return -1, RuntimeAttemptProjection{}, -1, RuntimeBoundaryProjection{}, fmt.Errorf("attempt %s has no current paused boundary", attemptID)
	}
	boundaryIndex := len(attempt.boundaries) - 1
	boundary := attempt.boundaries[boundaryIndex]
	if boundary.boundaryID != boundaryID || boundary.resumedRecord != 0 {
		return -1, RuntimeAttemptProjection{}, -1, RuntimeBoundaryProjection{}, fmt.Errorf("boundary %s is not current for attempt %s", boundaryID, attemptID)
	}
	return index, attempt, boundaryIndex, boundary, nil
}

func ensureRuntimeBindingAvailable(
	attempts []RuntimeAttemptProjection,
	attemptID, leaseID, authorizationID ID,
) error {
	for _, attempt := range attempts {
		if attempt.attemptID == attemptID || attempt.phase != AttemptActive {
			continue
		}
		if attempt.leaseID == leaseID {
			return fmt.Errorf("lease %s is already bound to attempt %s", leaseID, attempt.attemptID)
		}
		if attempt.authorizationID == authorizationID {
			return fmt.Errorf("authorization %s is already bound to attempt %s", authorizationID, attempt.attemptID)
		}
	}
	return nil
}

func cloneRuntimeAttempts(values []RuntimeAttemptProjection) []RuntimeAttemptProjection {
	result := append([]RuntimeAttemptProjection(nil), values...)
	for index := range result {
		result[index] = cloneRuntimeAttempt(result[index])
	}
	return result
}

func cloneRuntimeAttempt(value RuntimeAttemptProjection) RuntimeAttemptProjection {
	value.boundaries = cloneRuntimeBoundaries(value.boundaries)
	if value.commitProtocol != nil {
		state := cloneCommitProtocolState(*value.commitProtocol)
		value.commitProtocol = &state
	}
	return value
}

func cloneRuntimeBoundaries(values []RuntimeBoundaryProjection) []RuntimeBoundaryProjection {
	result := append([]RuntimeBoundaryProjection(nil), values...)
	for index := range result {
		result[index] = cloneRuntimeBoundary(result[index])
	}
	return result
}

func cloneRuntimeBoundary(value RuntimeBoundaryProjection) RuntimeBoundaryProjection {
	value.evidence = cloneEvidence(value.evidence)
	return value
}

func canonicalAttemptRuntime(attempt RuntimeAttemptProjection) (json.RawMessage, error) {
	type acknowledgementJSON struct {
		Record                uint64                           `json:"record"`
		Kind                  OrchestrationAcknowledgementKind `json:"kind"`
		GoalID                string                           `json:"goal_id"`
		GoalScope             GoalScope                        `json:"goal_scope"`
		IdempotencyKey        string                           `json:"idempotency_key"`
		AcknowledgementDigest string                           `json:"acknowledgement_digest"`
	}
	type ownerJSON struct {
		Record        uint64                `json:"record"`
		Response      OwnerBoundaryResponse `json:"response"`
		RequestDigest string                `json:"request_digest"`
		ReceiptDigest string                `json:"receipt_digest"`
	}
	type nextGoalIntentJSON struct {
		Record         uint64    `json:"record"`
		GoalID         string    `json:"goal_id"`
		GoalScope      GoalScope `json:"goal_scope"`
		IdempotencyKey string    `json:"idempotency_key"`
	}
	type boundaryJSON struct {
		BoundaryID      string               `json:"boundary_id"`
		Ordinal         uint64               `json:"ordinal"`
		Record          uint64               `json:"record"`
		ResumedRecord   uint64               `json:"resumed_record"`
		Mode            AttemptBoundaryMode  `json:"mode"`
		SerialSegment   string               `json:"serial_segment,omitempty"`
		LeaseID         string               `json:"lease_id"`
		AuthorizationID string               `json:"authorization_id"`
		GoalID          string               `json:"goal_id"`
		GoalScope       GoalScope            `json:"goal_scope"`
		Head            string               `json:"head"`
		EvidenceDigest  string               `json:"evidence_digest"`
		DirectiveDigest string               `json:"directive_digest,omitempty"`
		IdempotencyKey  string               `json:"idempotency_key,omitempty"`
		GoalCompleted   *acknowledgementJSON `json:"goal_completed,omitempty"`
		OwnerResponse   *ownerJSON           `json:"owner_response,omitempty"`
		NextGoalIntent  *nextGoalIntentJSON  `json:"next_goal_intent,omitempty"`
		NextGoal        *acknowledgementJSON `json:"next_goal,omitempty"`
	}
	type attemptJSON struct {
		AttemptID             string              `json:"attempt_id"`
		PlanID                string              `json:"plan_id"`
		MergeUnitID           string              `json:"merge_unit_id"`
		Generation            string              `json:"generation"`
		Repository            string              `json:"repository"`
		AttemptNumber         uint64              `json:"attempt_number"`
		Base                  string              `json:"base"`
		Branch                string              `json:"branch"`
		Worktree              string              `json:"worktree"`
		BoundaryMode          AttemptBoundaryMode `json:"boundary_mode"`
		SerialSegment         string              `json:"serial_segment,omitempty"`
		SerialSegmentHeld     bool                `json:"serial_segment_held"`
		Phase                 AttemptRuntimePhase `json:"phase"`
		ReservationRecord     uint64              `json:"reservation_record"`
		MaterializationRecord uint64              `json:"materialization_record"`
		StartRecord           uint64              `json:"start_record"`
		VerifiedHead          string              `json:"verified_head,omitempty"`
		InspectionDigest      string              `json:"inspection_digest,omitempty"`
		LeaseID               string              `json:"lease_id,omitempty"`
		AuthorizationID       string              `json:"authorization_id,omitempty"`
		GoalID                string              `json:"goal_id,omitempty"`
		GoalScope             GoalScope           `json:"goal_scope,omitempty"`
		Boundaries            []boundaryJSON      `json:"boundaries"`
		CommitProtocol        json.RawMessage     `json:"commit_protocol,omitempty"`
	}
	ackJSON := func(value RuntimeOrchestrationAcknowledgement) *acknowledgementJSON {
		return &acknowledgementJSON{
			Record: value.record, Kind: value.kind, GoalID: value.goal.id.String(), GoalScope: value.goal.scope,
			IdempotencyKey: value.idempotencyKey.String(), AcknowledgementDigest: value.acknowledgementDigest.String(),
		}
	}
	value := attemptJSON{
		AttemptID: attempt.attemptID.String(), PlanID: attempt.mergeUnit.planID.String(),
		MergeUnitID: attempt.mergeUnit.mergeUnitID.String(), Generation: attempt.generation.String(),
		Repository: attempt.repository.String(), AttemptNumber: attempt.attemptNumber, Base: attempt.base.String(),
		Branch: attempt.branch, Worktree: attempt.worktree, BoundaryMode: attempt.boundaryMode,
		SerialSegment: attempt.serialSegment.String(), SerialSegmentHeld: attempt.serialSegmentHeld,
		Phase: attempt.phase, ReservationRecord: attempt.reservationRecord,
		MaterializationRecord: attempt.materializationRecord, StartRecord: attempt.startRecord,
		VerifiedHead: attempt.verifiedHead.String(), InspectionDigest: attempt.inspectionDigest.String(),
		LeaseID: attempt.leaseID.String(), AuthorizationID: attempt.authorizationID.String(),
		GoalID: attempt.goal.id.String(), GoalScope: attempt.goal.scope,
		Boundaries: make([]boundaryJSON, 0, len(attempt.boundaries)),
	}
	if attempt.commitProtocol != nil {
		protocol, err := canonicalCommitProtocolRuntime(*attempt.commitProtocol)
		if err != nil {
			return nil, err
		}
		value.CommitProtocol = protocol
	}
	for _, boundary := range attempt.boundaries {
		item := boundaryJSON{
			BoundaryID: boundary.boundaryID.String(), Ordinal: boundary.ordinal, Record: boundary.record,
			ResumedRecord: boundary.resumedRecord, Mode: boundary.mode, SerialSegment: boundary.serialSegment.String(),
			LeaseID: boundary.leaseID.String(), AuthorizationID: boundary.authorizationID.String(),
			GoalID: boundary.goal.id.String(), GoalScope: boundary.goal.scope, Head: boundary.head.String(),
			EvidenceDigest: boundary.evidenceDigest.String(), DirectiveDigest: boundary.directiveDigest.String(),
			IdempotencyKey: boundary.idempotencyKey.String(),
		}
		if boundary.goalCompletedOK {
			item.GoalCompleted = ackJSON(boundary.goalCompleted)
		}
		if boundary.ownerResponseOK {
			item.OwnerResponse = &ownerJSON{
				Record: boundary.ownerResponse.record, Response: boundary.ownerResponse.response,
				RequestDigest: boundary.ownerResponse.requestDigest.String(), ReceiptDigest: boundary.ownerResponse.receiptDigest.String(),
			}
		}
		if boundary.nextGoalIntentOK {
			item.NextGoalIntent = &nextGoalIntentJSON{
				Record: boundary.nextGoalIntent.record, GoalID: boundary.nextGoalIntent.goal.id.String(),
				GoalScope:      boundary.nextGoalIntent.goal.scope,
				IdempotencyKey: boundary.nextGoalIntent.idempotencyKey.String(),
			}
		}
		if boundary.nextGoalOK {
			item.NextGoal = ackJSON(boundary.nextGoal)
		}
		value.Boundaries = append(value.Boundaries, item)
	}
	content, err := json.Marshal(value)
	return json.RawMessage(content), err
}
