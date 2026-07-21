package workspace

import (
	"encoding/json"
	"fmt"
)

type attemptReservedPayloadWire struct {
	WorkspaceID   string              `json:"workspace_id"`
	Generation    string              `json:"generation"`
	Repository    string              `json:"repository"`
	AttemptID     string              `json:"attempt_id"`
	PlanID        string              `json:"plan_id"`
	MergeUnitID   string              `json:"merge_unit_id"`
	AttemptNumber uint64              `json:"attempt_number"`
	Base          string              `json:"base"`
	Branch        string              `json:"branch"`
	Worktree      string              `json:"worktree"`
	BoundaryMode  AttemptBoundaryMode `json:"boundary_mode"`
	SerialSegment string              `json:"serial_segment,omitempty"`
	GoalID        string              `json:"goal_id"`
	GoalScope     GoalScope           `json:"goal_scope"`
}

type attemptMaterializationPayloadWire struct {
	WorkspaceID string `json:"workspace_id"`
	Generation  string `json:"generation"`
	AttemptID   string `json:"attempt_id"`
	Base        string `json:"base"`
	Branch      string `json:"branch"`
	Worktree    string `json:"worktree"`
}

type attemptStartedPayloadWire struct {
	WorkspaceID      string    `json:"workspace_id"`
	Generation       string    `json:"generation"`
	AttemptID        string    `json:"attempt_id"`
	VerifiedHead     string    `json:"verified_head"`
	InspectionDigest string    `json:"inspection_digest"`
	LeaseID          string    `json:"lease_id"`
	AuthorizationID  string    `json:"authorization_id"`
	GoalID           string    `json:"goal_id"`
	GoalScope        GoalScope `json:"goal_scope"`
}

type evidenceItemPayloadWire struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type evidencePayloadWire struct {
	Kind   string                    `json:"kind"`
	Digest string                    `json:"digest"`
	Items  []evidenceItemPayloadWire `json:"items"`
}

type attemptBoundaryPayloadWire struct {
	WorkspaceID     string                `json:"workspace_id"`
	Generation      string                `json:"generation"`
	AttemptID       string                `json:"attempt_id"`
	BoundaryID      string                `json:"boundary_id"`
	Ordinal         uint64                `json:"ordinal"`
	Mode            AttemptBoundaryMode   `json:"mode"`
	SerialSegment   string                `json:"serial_segment,omitempty"`
	LeaseID         string                `json:"lease_id"`
	AuthorizationID string                `json:"authorization_id"`
	GoalID          string                `json:"goal_id"`
	GoalScope       GoalScope             `json:"goal_scope"`
	Head            string                `json:"head"`
	Evidence        []evidencePayloadWire `json:"evidence"`
	EvidenceDigest  string                `json:"evidence_digest"`
	DirectiveDigest string                `json:"directive_digest,omitempty"`
	IdempotencyKey  string                `json:"idempotency_key,omitempty"`
}

type attemptOrchestrationAckPayloadWire struct {
	WorkspaceID           string                           `json:"workspace_id"`
	Generation            string                           `json:"generation"`
	AttemptID             string                           `json:"attempt_id"`
	BoundaryID            string                           `json:"boundary_id"`
	Kind                  OrchestrationAcknowledgementKind `json:"kind"`
	GoalID                string                           `json:"goal_id"`
	GoalScope             GoalScope                        `json:"goal_scope"`
	IdempotencyKey        string                           `json:"idempotency_key"`
	AcknowledgementDigest string                           `json:"acknowledgement_digest"`
}

type attemptNextGoalIntentPayloadWire struct {
	WorkspaceID    string    `json:"workspace_id"`
	Generation     string    `json:"generation"`
	AttemptID      string    `json:"attempt_id"`
	BoundaryID     string    `json:"boundary_id"`
	GoalID         string    `json:"goal_id"`
	GoalScope      GoalScope `json:"goal_scope"`
	IdempotencyKey string    `json:"idempotency_key"`
}

type attemptOwnerResponsePayloadWire struct {
	WorkspaceID   string                `json:"workspace_id"`
	Generation    string                `json:"generation"`
	AttemptID     string                `json:"attempt_id"`
	BoundaryID    string                `json:"boundary_id"`
	Response      OwnerBoundaryResponse `json:"response"`
	RequestDigest string                `json:"request_digest"`
	ReceiptDigest string                `json:"receipt_digest"`
}

type attemptResumedPayloadWire struct {
	WorkspaceID      string    `json:"workspace_id"`
	Generation       string    `json:"generation"`
	AttemptID        string    `json:"attempt_id"`
	BoundaryID       string    `json:"boundary_id"`
	VerifiedHead     string    `json:"verified_head"`
	InspectionDigest string    `json:"inspection_digest"`
	LeaseID          string    `json:"lease_id"`
	AuthorizationID  string    `json:"authorization_id"`
	GoalID           string    `json:"goal_id"`
	GoalScope        GoalScope `json:"goal_scope"`
	SerialSegment    string    `json:"serial_segment,omitempty"`
}

func marshalAttemptJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case AttemptReservedJournalEvent:
		value = attemptReservedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			Repository: event.repository.String(), AttemptID: event.attemptID.String(),
			PlanID: event.mergeUnit.planID.String(), MergeUnitID: event.mergeUnit.mergeUnitID.String(),
			AttemptNumber: event.attemptNumber, Base: event.base.String(), Branch: event.branch,
			Worktree: event.worktree, BoundaryMode: event.boundaryMode, SerialSegment: event.serialSegment.String(),
			GoalID: event.goal.id.String(), GoalScope: event.goal.scope,
		}
	case AttemptMaterializationIntendedJournalEvent:
		value = attemptMaterializationPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), Base: event.base.String(), Branch: event.branch, Worktree: event.worktree,
		}
	case AttemptStartedJournalEvent:
		value = attemptStartedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			VerifiedHead: event.verifiedHead.String(), InspectionDigest: event.inspectionDigest.String(),
			LeaseID: event.leaseID.String(), AuthorizationID: event.authorizationID.String(),
			GoalID: event.goal.id.String(), GoalScope: event.goal.scope,
		}
	case AttemptBoundaryReachedJournalEvent:
		value = attemptBoundaryPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			BoundaryID: event.boundaryID.String(), Ordinal: event.ordinal, Mode: event.mode,
			SerialSegment: event.serialSegment.String(), LeaseID: event.leaseID.String(),
			AuthorizationID: event.authorizationID.String(), GoalID: event.goal.id.String(), GoalScope: event.goal.scope,
			Head: event.head.String(), Evidence: evidencePayloadFromDomain(event.evidence),
			EvidenceDigest: event.evidenceDigest.String(), DirectiveDigest: event.directiveDigest.String(),
			IdempotencyKey: event.idempotencyKey.String(),
		}
	case AttemptNextGoalIntendedJournalEvent:
		value = attemptNextGoalIntentPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), BoundaryID: event.boundaryID.String(),
			GoalID: event.goal.id.String(), GoalScope: event.goal.scope,
			IdempotencyKey: event.idempotencyKey.String(),
		}
	case AttemptOrchestrationAcknowledgedJournalEvent:
		value = attemptOrchestrationAckPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			BoundaryID: event.boundaryID.String(), Kind: event.kind,
			GoalID: event.goal.id.String(), GoalScope: event.goal.scope,
			IdempotencyKey: event.idempotencyKey.String(), AcknowledgementDigest: event.acknowledgementDigest.String(),
		}
	case AttemptOwnerResponseJournalEvent:
		value = attemptOwnerResponsePayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			BoundaryID: event.boundaryID.String(), Response: event.response,
			RequestDigest: event.requestDigest.String(), ReceiptDigest: event.receiptDigest.String(),
		}
	case AttemptResumedJournalEvent:
		value = attemptResumedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(), AttemptID: event.attemptID.String(),
			BoundaryID: event.boundaryID.String(), VerifiedHead: event.verifiedHead.String(),
			InspectionDigest: event.inspectionDigest.String(), LeaseID: event.leaseID.String(),
			AuthorizationID: event.authorizationID.String(), GoalID: event.goal.id.String(), GoalScope: event.goal.scope,
			SerialSegment: event.serialSegment.String(),
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeAttemptJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventAttemptReserved:
		var wire attemptReservedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode attempt reservation: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		repository, err := NewRepositoryIdentity(wire.Repository)
		if err != nil {
			return nil, true, err
		}
		mergeUnit, err := parseMergeUnitReference(wire.PlanID, wire.MergeUnitID)
		if err != nil {
			return nil, true, err
		}
		base, err := ParseGitObjectID(wire.Base)
		if err != nil {
			return nil, true, err
		}
		segment, err := parseOptionalID(wire.SerialSegment)
		if err != nil {
			return nil, true, err
		}
		goal, err := parseGoalBinding(wire.GoalID, wire.GoalScope)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptReservedJournalEvent(
			workspaceID, generation, repository, attemptID, mergeUnit, wire.AttemptNumber,
			base, wire.Branch, wire.Worktree, wire.BoundaryMode, segment, goal,
		)
		return event, true, err
	case JournalEventAttemptMaterializationIntended:
		var wire attemptMaterializationPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode materialization intent: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		base, err := ParseGitObjectID(wire.Base)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptMaterializationIntendedJournalEvent(
			workspaceID, attemptID, generation, base, wire.Branch, wire.Worktree,
		)
		return event, true, err
	case JournalEventAttemptStarted:
		var wire attemptStartedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode attempt start: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		head, inspection, leaseID, authorizationID, goal, err := parseAttemptActivationBindings(
			wire.VerifiedHead, wire.InspectionDigest, wire.LeaseID, wire.AuthorizationID, wire.GoalID, wire.GoalScope,
		)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptStartedJournalEvent(
			workspaceID, attemptID, generation, head, inspection, leaseID, authorizationID, goal,
		)
		return event, true, err
	case JournalEventAttemptBoundary:
		var wire attemptBoundaryPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode attempt boundary: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		boundaryID, err := NewID(wire.BoundaryID)
		if err != nil {
			return nil, true, err
		}
		segment, err := parseOptionalID(wire.SerialSegment)
		if err != nil {
			return nil, true, err
		}
		leaseID, err := NewID(wire.LeaseID)
		if err != nil {
			return nil, true, err
		}
		authorizationID, err := NewID(wire.AuthorizationID)
		if err != nil {
			return nil, true, err
		}
		goal, err := parseGoalBinding(wire.GoalID, wire.GoalScope)
		if err != nil {
			return nil, true, err
		}
		head, err := ParseGitObjectID(wire.Head)
		if err != nil {
			return nil, true, err
		}
		evidence, err := evidenceDomainFromPayload(wire.Evidence)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptBoundaryReachedJournalEvent(
			workspaceID, attemptID, generation, wire.Ordinal, wire.Mode, segment,
			leaseID, authorizationID, goal, head, evidence,
		)
		if err != nil {
			return nil, true, err
		}
		if event.boundaryID != boundaryID || event.evidenceDigest.String() != wire.EvidenceDigest ||
			event.directiveDigest.String() != wire.DirectiveDigest || event.idempotencyKey.String() != wire.IdempotencyKey {
			return nil, true, fmt.Errorf("attempt boundary derived bindings do not match its payload")
		}
		return event, true, nil
	case JournalEventNextGoalIntended:
		var wire attemptNextGoalIntentPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode next-goal intent: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		boundaryID, err := NewID(wire.BoundaryID)
		if err != nil {
			return nil, true, err
		}
		goal, err := parseGoalBinding(wire.GoalID, wire.GoalScope)
		if err != nil {
			return nil, true, err
		}
		key, err := ParseDigest(wire.IdempotencyKey)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptNextGoalIntendedJournalEvent(
			workspaceID, attemptID, boundaryID, generation, goal, key,
		)
		return event, true, err
	case JournalEventOrchestrationAck:
		var wire attemptOrchestrationAckPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode orchestration acknowledgement: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		boundaryID, err := NewID(wire.BoundaryID)
		if err != nil {
			return nil, true, err
		}
		goal, err := parseGoalBinding(wire.GoalID, wire.GoalScope)
		if err != nil {
			return nil, true, err
		}
		key, err := ParseDigest(wire.IdempotencyKey)
		if err != nil {
			return nil, true, err
		}
		acknowledgement, err := ParseDigest(wire.AcknowledgementDigest)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptOrchestrationAcknowledgedJournalEvent(
			workspaceID, attemptID, boundaryID, generation, wire.Kind, goal, key, acknowledgement,
		)
		return event, true, err
	case JournalEventOwnerResponse:
		var wire attemptOwnerResponsePayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode owner response: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		boundaryID, err := NewID(wire.BoundaryID)
		if err != nil {
			return nil, true, err
		}
		request, err := ParseDigest(wire.RequestDigest)
		if err != nil {
			return nil, true, err
		}
		receipt, err := ParseDigest(wire.ReceiptDigest)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptOwnerResponseJournalEvent(
			workspaceID, attemptID, boundaryID, generation, wire.Response, request, receipt,
		)
		return event, true, err
	case JournalEventAttemptResumed:
		var wire attemptResumedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode attempt resume: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		boundaryID, err := NewID(wire.BoundaryID)
		if err != nil {
			return nil, true, err
		}
		head, inspection, leaseID, authorizationID, goal, err := parseAttemptActivationBindings(
			wire.VerifiedHead, wire.InspectionDigest, wire.LeaseID, wire.AuthorizationID, wire.GoalID, wire.GoalScope,
		)
		if err != nil {
			return nil, true, err
		}
		segment, err := parseOptionalID(wire.SerialSegment)
		if err != nil {
			return nil, true, err
		}
		event, err := NewAttemptResumedJournalEvent(
			workspaceID, attemptID, boundaryID, generation, head, inspection,
			leaseID, authorizationID, goal, segment,
		)
		return event, true, err
	default:
		return nil, false, nil
	}
}

func evidencePayloadFromDomain(values []Evidence) []evidencePayloadWire {
	result := make([]evidencePayloadWire, 0, len(values))
	for _, value := range values {
		items := make([]evidenceItemPayloadWire, 0, len(value.items))
		for _, item := range value.items {
			items = append(items, evidenceItemPayloadWire{Name: item.name.String(), Value: item.value})
		}
		result = append(result, evidencePayloadWire{
			Kind: value.kind.String(), Digest: value.digest.String(), Items: items,
		})
	}
	return result
}

func evidenceDomainFromPayload(values []evidencePayloadWire) ([]Evidence, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("attempt boundary evidence is required")
	}
	result := make([]Evidence, 0, len(values))
	for _, value := range values {
		kind, err := NewID(value.Kind)
		if err != nil {
			return nil, err
		}
		digest, err := ParseDigest(value.Digest)
		if err != nil {
			return nil, err
		}
		items := make([]EvidenceItem, 0, len(value.Items))
		for _, wireItem := range value.Items {
			name, err := NewID(wireItem.Name)
			if err != nil {
				return nil, err
			}
			item, err := NewEvidenceItem(name, wireItem.Value)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		evidence, err := NewEvidence(kind, digest, items)
		if err != nil {
			return nil, err
		}
		result = append(result, evidence)
	}
	return result, nil
}

func parseAttemptEnvelope(workspace, generation, attempt string) (ID, Digest, ID, error) {
	workspaceID, err := NewID(workspace)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	generationDigest, err := ParseDigest(generation)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	attemptID, err := NewID(attempt)
	if err != nil {
		return ID{}, Digest{}, ID{}, err
	}
	return workspaceID, generationDigest, attemptID, nil
}

func parseMergeUnitReference(plan, unit string) (MergeUnitReference, error) {
	planID, err := NewID(plan)
	if err != nil {
		return MergeUnitReference{}, err
	}
	unitID, err := NewID(unit)
	if err != nil {
		return MergeUnitReference{}, err
	}
	return NewMergeUnitReference(planID, unitID)
}

func parseOptionalID(value string) (ID, error) {
	if value == "" {
		return ID{}, nil
	}
	return NewID(value)
}

func parseGoalBinding(id string, scope GoalScope) (GoalBinding, error) {
	goalID, err := NewID(id)
	if err != nil {
		return GoalBinding{}, err
	}
	return NewGoalBinding(goalID, scope)
}

func parseAttemptActivationBindings(
	headText, inspectionText, leaseText, authorizationText, goalText string,
	goalScope GoalScope,
) (GitObjectID, Digest, ID, ID, GoalBinding, error) {
	head, err := ParseGitObjectID(headText)
	if err != nil {
		return GitObjectID{}, Digest{}, ID{}, ID{}, GoalBinding{}, err
	}
	inspection, err := ParseDigest(inspectionText)
	if err != nil {
		return GitObjectID{}, Digest{}, ID{}, ID{}, GoalBinding{}, err
	}
	leaseID, err := NewID(leaseText)
	if err != nil {
		return GitObjectID{}, Digest{}, ID{}, ID{}, GoalBinding{}, err
	}
	authorizationID, err := NewID(authorizationText)
	if err != nil {
		return GitObjectID{}, Digest{}, ID{}, ID{}, GoalBinding{}, err
	}
	goal, err := parseGoalBinding(goalText, goalScope)
	if err != nil {
		return GitObjectID{}, Digest{}, ID{}, ID{}, GoalBinding{}, err
	}
	return head, inspection, leaseID, authorizationID, goal, nil
}
