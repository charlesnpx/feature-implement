package workspace

import (
	"encoding/json"
	"fmt"
)

type AttemptRuntimePhase string

const (
	AttemptActive     AttemptRuntimePhase = "active"
	AttemptPaused     AttemptRuntimePhase = "paused"
	AttemptSuperseded AttemptRuntimePhase = "superseded"
	AttemptCompleted  AttemptRuntimePhase = "completed"
	AttemptFailed     AttemptRuntimePhase = "failed"
	AttemptAbandoned  AttemptRuntimePhase = "abandoned"
)

func (phase AttemptRuntimePhase) valid() bool {
	switch phase {
	case AttemptActive, AttemptPaused,
		AttemptSuperseded, AttemptCompleted,
		AttemptFailed, AttemptAbandoned:
		return true
	default:
		return false
	}
}

func (phase AttemptRuntimePhase) nonterminal() bool {
	return phase == AttemptActive || phase == AttemptPaused
}

func (phase AttemptRuntimePhase) retryableTerminal() bool {
	return phase == AttemptSuperseded ||
		phase == AttemptFailed ||
		phase == AttemptAbandoned
}

type RuntimeBoundaryProjection struct {
	boundaryID     ID
	ordinal        uint64
	record         uint64
	resumedRecord  uint64
	kind           AttemptBoundaryKind
	checkpoint     AttemptCheckpointMode
	serialSegment  ID
	leaseID        ID
	goal           GoalBinding
	head           GitObjectID
	evidence       []Evidence
	evidenceDigest Digest
}

func (boundary RuntimeBoundaryProjection) Record() uint64 { return boundary.record }
func (boundary RuntimeBoundaryProjection) Kind() AttemptBoundaryKind {
	return boundary.kind
}
func (boundary RuntimeBoundaryProjection) Checkpoint() AttemptCheckpointMode {
	return boundary.checkpoint
}
func (boundary RuntimeBoundaryProjection) SerialSegment() ID { return boundary.serialSegment }
func (boundary RuntimeBoundaryProjection) LeaseID() ID       { return boundary.leaseID }
func (boundary RuntimeBoundaryProjection) LeaseFencedAndReleased() bool {
	return boundary.record != 0
}
func (boundary RuntimeBoundaryProjection) Goal() GoalBinding      { return boundary.goal }
func (boundary RuntimeBoundaryProjection) Head() GitObjectID      { return boundary.head }
func (boundary RuntimeBoundaryProjection) EvidenceDigest() Digest { return boundary.evidenceDigest }

type RuntimeAttemptProjection struct {
	attemptID         ID
	mergeUnit         MergeUnitReference
	generation        Digest
	attemptNumber     uint64
	base              GitObjectID
	worktree          string
	checkpoint        AttemptCheckpointMode
	escalation        AttemptEscalationPolicy
	serialSegment     ID
	serialSegmentHeld bool
	phase             AttemptRuntimePhase
	startRecord       uint64
	verifiedHead      GitObjectID
	inspectionDigest  Digest
	leaseID           ID
	goal              GoalBinding
	boundaries        []RuntimeBoundaryProjection
	integration       *RuntimeIntegrationProjection
}

func (attempt RuntimeAttemptProjection) AttemptID() ID                 { return attempt.attemptID }
func (attempt RuntimeAttemptProjection) MergeUnit() MergeUnitReference { return attempt.mergeUnit }
func (attempt RuntimeAttemptProjection) Generation() Digest            { return attempt.generation }
func (attempt RuntimeAttemptProjection) AttemptNumber() uint64         { return attempt.attemptNumber }
func (attempt RuntimeAttemptProjection) Base() GitObjectID             { return attempt.base }

func (attempt RuntimeAttemptProjection) Worktree() string                  { return attempt.worktree }
func (attempt RuntimeAttemptProjection) Checkpoint() AttemptCheckpointMode { return attempt.checkpoint }
func (attempt RuntimeAttemptProjection) Escalation() AttemptEscalationPolicy {
	return attempt.escalation
}
func (attempt RuntimeAttemptProjection) SerialSegment() ID          { return attempt.serialSegment }
func (attempt RuntimeAttemptProjection) SerialSegmentHeld() bool    { return attempt.serialSegmentHeld }
func (attempt RuntimeAttemptProjection) Phase() AttemptRuntimePhase { return attempt.phase }
func (attempt RuntimeAttemptProjection) StartRecord() uint64        { return attempt.startRecord }
func (attempt RuntimeAttemptProjection) VerifiedHead() GitObjectID  { return attempt.verifiedHead }
func (attempt RuntimeAttemptProjection) LeaseID() ID                { return attempt.leaseID }
func (attempt RuntimeAttemptProjection) Goal() GoalBinding          { return attempt.goal }
func (attempt RuntimeAttemptProjection) Integration() (
	RuntimeIntegrationProjection,
	bool,
) {
	if attempt.integration == nil {
		return RuntimeIntegrationProjection{}, false
	}
	return *attempt.integration, true
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
	case AttemptStartJournalEvent:
		if event.workspaceID != current.workspaceID || event.generation != current.activeGeneration {
			return fmt.Errorf("attempt start does not match the active workspace generation")
		}
		for _, attempt := range current.attempts {
			if attempt.integration != nil && !attempt.integration.Integrated() {
				return fmt.Errorf(
					"attempt start conflicts with pending integration attempt %s", attempt.attemptID,
				)
			}
		}
		if _, exists := findRuntimeAttempt(current.attempts, event.attemptID); exists {
			return fmt.Errorf("attempt %s is already started", event.attemptID)
		}
		for _, attempt := range current.attempts {
			if attempt.mergeUnit == event.mergeUnit && attempt.phase.nonterminal() {
				return fmt.Errorf("merge unit %s already has nonterminal attempt %s", event.mergeUnit, attempt.attemptID)
			}
			if !event.serialSegment.IsZero() && attempt.serialSegmentHeld && attempt.serialSegment == event.serialSegment {
				return fmt.Errorf("serial segment %s is held by attempt %s", event.serialSegment, attempt.attemptID)
			}
		}
		if err := ensureRuntimeLeaseAvailable(current.attempts, event.attemptID, event.leaseID); err != nil {
			return err
		}
		next.attempts = append(next.attempts, RuntimeAttemptProjection{
			attemptID: event.attemptID, mergeUnit: event.mergeUnit, generation: event.generation,
			attemptNumber: event.attemptNumber, base: event.base, worktree: event.worktree,
			checkpoint: event.checkpoint, escalation: event.escalation,
			serialSegment: event.serialSegment, serialSegmentHeld: !event.serialSegment.IsZero(),
			phase: AttemptActive, startRecord: record.sequence, verifiedHead: event.base,
			leaseID: event.leaseID, goal: event.goal,
		})
		return nil
	case AttemptBoundaryReachedJournalEvent:
		index, attempt, err := requireRuntimeAttempt(current, event.attemptID, event.workspaceID, event.generation)
		if err != nil {
			return err
		}
		if attempt.phase != AttemptActive || attempt.leaseID != event.leaseID || attempt.goal != event.goal ||
			attempt.serialSegment != event.serialSegment ||
			event.ordinal != uint64(len(attempt.boundaries)+1) {
			return fmt.Errorf("attempt boundary does not match the active lease, goal, policy, and ordinal")
		}
		switch event.kind {
		case AttemptBoundaryKindCheckpoint:
			if attempt.checkpoint == AttemptCheckpointNone || event.checkpoint != attempt.checkpoint {
				return fmt.Errorf("checkpoint boundary does not match the reserved merge-unit checkpoint policy")
			}
		case AttemptBoundaryKindEscalation:
			if attempt.escalation != AttemptEscalationAllowed || event.checkpoint != AttemptCheckpointPauseOnly {
				return fmt.Errorf("escalation boundary does not match the reserved merge-unit escalation policy")
			}
		default:
			return fmt.Errorf("attempt boundary has unsupported kind %q", event.kind)
		}
		updated := &next.attempts[index]
		updated.phase = AttemptPaused
		updated.serialSegmentHeld = false
		updated.verifiedHead = event.head
		updated.leaseID = ID{}
		updated.boundaries = append(updated.boundaries, RuntimeBoundaryProjection{
			boundaryID: event.boundaryID, ordinal: event.ordinal, record: record.sequence,
			kind: event.kind, checkpoint: event.checkpoint, serialSegment: event.serialSegment,
			leaseID: event.leaseID,
			goal:    event.goal, head: event.head, evidence: cloneEvidence(event.evidence),
			evidenceDigest: event.evidenceDigest,
		})
		return nil
	case AttemptResumedJournalEvent:
		index, attempt, boundaryIndex, boundary, err := requireCurrentRuntimeBoundary(
			current, event.workspaceID, event.generation, event.attemptID, event.boundaryID,
		)
		if err != nil {
			return err
		}
		if event.verifiedHead != boundary.head || event.serialSegment != attempt.serialSegment || event.goal != boundary.goal {
			return fmt.Errorf("attempt resume requires an unchanged paused head, goal, and serial policy")
		}
		for _, other := range current.attempts {
			if !event.serialSegment.IsZero() && other.attemptID != attempt.attemptID &&
				other.serialSegmentHeld && other.serialSegment == event.serialSegment {
				return fmt.Errorf("serial segment %s is held by attempt %s", event.serialSegment, other.attemptID)
			}
		}
		if err := ensureRuntimeLeaseAvailable(current.attempts, attempt.attemptID, event.leaseID); err != nil {
			return err
		}
		updated := &next.attempts[index]
		updated.phase = AttemptActive
		updated.serialSegmentHeld = !event.serialSegment.IsZero()
		updated.verifiedHead, updated.inspectionDigest = event.verifiedHead, event.inspectionDigest
		updated.leaseID, updated.goal = event.leaseID, event.goal
		updated.boundaries[boundaryIndex].resumedRecord = record.sequence
		return nil
	case AttemptAbandonedJournalEvent:
		index, attempt, err := requireRuntimeAttempt(current, event.attemptID, event.workspaceID, event.generation)
		if err != nil {
			return err
		}
		if !attempt.phase.nonterminal() || attempt.integration != nil {
			return fmt.Errorf("attempt abandonment requires a nonterminal attempt without integration")
		}
		updated := &next.attempts[index]
		updated.phase = AttemptAbandoned
		updated.leaseID = ID{}
		updated.serialSegmentHeld = false
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

func ensureRuntimeLeaseAvailable(
	attempts []RuntimeAttemptProjection,
	attemptID, leaseID ID,
) error {
	for _, attempt := range attempts {
		if attempt.attemptID == attemptID || attempt.phase != AttemptActive {
			continue
		}
		if attempt.leaseID == leaseID {
			return fmt.Errorf("lease %s is already bound to attempt %s", leaseID, attempt.attemptID)
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
	if value.integration != nil {
		integration := *value.integration
		value.integration = &integration
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
	type boundaryJSON struct {
		BoundaryID     string                `json:"boundary_id"`
		Ordinal        uint64                `json:"ordinal"`
		Record         uint64                `json:"record"`
		ResumedRecord  uint64                `json:"resumed_record"`
		Kind           AttemptBoundaryKind   `json:"kind"`
		Checkpoint     AttemptCheckpointMode `json:"checkpoint"`
		SerialSegment  string                `json:"serial_segment,omitempty"`
		LeaseID        string                `json:"lease_id"`
		GoalID         string                `json:"goal_id"`
		GoalScope      GoalScope             `json:"goal_scope"`
		Head           string                `json:"head"`
		EvidenceDigest string                `json:"evidence_digest"`
	}
	type integrationJSON struct {
		IntentDigest     string `json:"intent_digest"`
		IntentRecord     uint64 `json:"intent_record"`
		Integrated       bool   `json:"integrated"`
		IntegratedRecord uint64 `json:"integrated_record,omitempty"`
		ExpectedHead     string `json:"expected_feature_head"`
		AcceptedHead     string `json:"accepted_head"`
		AcceptedTree     string `json:"accepted_tree"`
		MergeCommit      string `json:"merge_commit"`
		AcceptanceMode   string `json:"acceptance_mode"`
	}
	type attemptJSON struct {
		AttemptID         string                  `json:"attempt_id"`
		PlanID            string                  `json:"plan_id"`
		MergeUnitID       string                  `json:"merge_unit_id"`
		Generation        string                  `json:"generation"`
		AttemptNumber     uint64                  `json:"attempt_number"`
		Base              string                  `json:"base"`
		Worktree          string                  `json:"worktree"`
		Checkpoint        AttemptCheckpointMode   `json:"checkpoint"`
		Escalation        AttemptEscalationPolicy `json:"escalation"`
		SerialSegment     string                  `json:"serial_segment,omitempty"`
		SerialSegmentHeld bool                    `json:"serial_segment_held"`
		Phase             AttemptRuntimePhase     `json:"phase"`
		StartRecord       uint64                  `json:"start_record"`
		VerifiedHead      string                  `json:"verified_head,omitempty"`
		InspectionDigest  string                  `json:"inspection_digest,omitempty"`
		LeaseID           string                  `json:"lease_id,omitempty"`
		GoalID            string                  `json:"goal_id,omitempty"`
		GoalScope         GoalScope               `json:"goal_scope,omitempty"`
		Boundaries        []boundaryJSON          `json:"boundaries"`
		Integration       *integrationJSON        `json:"integration,omitempty"`
	}
	value := attemptJSON{
		AttemptID: attempt.attemptID.String(), PlanID: attempt.mergeUnit.planID.String(),
		MergeUnitID: attempt.mergeUnit.mergeUnitID.String(), Generation: attempt.generation.String(),
		AttemptNumber: attempt.attemptNumber, Base: attempt.base.String(),
		Worktree:   attempt.worktree,
		Checkpoint: attempt.checkpoint, Escalation: attempt.escalation,
		SerialSegment: attempt.serialSegment.String(), SerialSegmentHeld: attempt.serialSegmentHeld,
		Phase: attempt.phase, StartRecord: attempt.startRecord,
		VerifiedHead: attempt.verifiedHead.String(), InspectionDigest: attempt.inspectionDigest.String(),
		LeaseID: attempt.leaseID.String(),
		GoalID:  attempt.goal.id.String(), GoalScope: attempt.goal.scope,
		Boundaries: make([]boundaryJSON, 0, len(attempt.boundaries)),
	}
	if attempt.integration != nil {
		intent := attempt.integration.intent
		value.Integration = &integrationJSON{
			IntentDigest:     intent.digest.String(),
			IntentRecord:     attempt.integration.intentRecord,
			Integrated:       attempt.integration.integratedRecord != 0,
			IntegratedRecord: attempt.integration.integratedRecord,
			ExpectedHead:     intent.expectedFeatureHead.String(),
			AcceptedHead:     intent.acceptedHead.String(),
			AcceptedTree:     intent.acceptedTree.String(),
			MergeCommit:      intent.expectedMerge.String(),
			AcceptanceMode:   string(intent.acceptanceMode),
		}
	}
	for _, boundary := range attempt.boundaries {
		item := boundaryJSON{
			BoundaryID: boundary.boundaryID.String(), Ordinal: boundary.ordinal, Record: boundary.record,
			ResumedRecord: boundary.resumedRecord, Kind: boundary.kind, Checkpoint: boundary.checkpoint,
			SerialSegment: boundary.serialSegment.String(),
			LeaseID:       boundary.leaseID.String(),
			GoalID:        boundary.goal.id.String(), GoalScope: boundary.goal.scope, Head: boundary.head.String(),
			EvidenceDigest: boundary.evidenceDigest.String(),
		}
		value.Boundaries = append(value.Boundaries, item)
	}
	content, err := json.Marshal(value)
	return json.RawMessage(content), err
}
