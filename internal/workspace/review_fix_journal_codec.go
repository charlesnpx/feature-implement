package workspace

import (
	"encoding/json"
	"fmt"
)

type reviewFixReservedPayloadWire struct {
	WorkspaceID    string                     `json:"workspace_id"`
	Generation     string                     `json:"generation"`
	AttemptID      string                     `json:"attempt_id"`
	Protocol       canonicalReviewFixProtocol `json:"protocol"`
	ProtocolDigest string                     `json:"protocol_digest"`
	Maximum        uint16                     `json:"maximum"`
	Ordinal        uint16                     `json:"ordinal"`
	Parent         string                     `json:"parent"`
	ReservationKey string                     `json:"reservation_key"`
}

type reviewFixIntendedPayloadWire struct {
	WorkspaceID    string                  `json:"workspace_id"`
	Generation     string                  `json:"generation"`
	AttemptID      string                  `json:"attempt_id"`
	ProtocolDigest string                  `json:"protocol_digest"`
	StepID         string                  `json:"step_id"`
	Ordinal        uint16                  `json:"ordinal"`
	Parent         string                  `json:"parent"`
	ReservationKey string                  `json:"reservation_key"`
	Inspection     stagedCommitPayloadWire `json:"inspection"`
	Body           string                  `json:"body"`
	IdempotencyKey string                  `json:"idempotency_key"`
}

type reviewFixCommitRecordedPayloadWire struct {
	WorkspaceID    string                          `json:"workspace_id"`
	Generation     string                          `json:"generation"`
	AttemptID      string                          `json:"attempt_id"`
	ProtocolDigest string                          `json:"protocol_digest"`
	Ordinal        uint16                          `json:"ordinal"`
	IntentKey      string                          `json:"intent_key"`
	Evidence       commitObjectEvidencePayloadWire `json:"evidence"`
}

type reviewFixCheckRecordedPayloadWire struct {
	WorkspaceID    string                         `json:"workspace_id"`
	Generation     string                         `json:"generation"`
	AttemptID      string                         `json:"attempt_id"`
	ProtocolDigest string                         `json:"protocol_digest"`
	Ordinal        uint16                         `json:"ordinal"`
	CheckOrdinal   uint16                         `json:"check_ordinal"`
	IdempotencyKey string                         `json:"idempotency_key"`
	Evidence       commitCheckEvidencePayloadWire `json:"evidence"`
}

func marshalReviewFixJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case ReviewFixReservedJournalEvent:
		value = reviewFixReservedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), Protocol: canonicalizeReviewFixProtocol(event.protocol),
			ProtocolDigest: event.protocol.digest.String(), Maximum: event.maximum,
			Ordinal: event.ordinal, Parent: event.parent.String(), ReservationKey: event.reservationKey.String(),
		}
	case ReviewFixIntendedJournalEvent:
		value = reviewFixIntendedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), ProtocolDigest: event.protocolDigest.String(),
			StepID: event.stepID.String(), Ordinal: event.ordinal, Parent: event.parent.String(),
			ReservationKey: event.reservationKey.String(), Inspection: stagedCommitToPayload(event.inspection),
			Body: event.body, IdempotencyKey: event.idempotencyKey.String(),
		}
	case ReviewFixCommitRecordedJournalEvent:
		value = reviewFixCommitRecordedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), ProtocolDigest: event.protocolDigest.String(),
			Ordinal: event.ordinal, IntentKey: event.intentKey.String(),
			Evidence: commitObjectEvidenceToPayload(event.evidence),
		}
	case ReviewFixCheckRecordedJournalEvent:
		value = reviewFixCheckRecordedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			AttemptID: event.attemptID.String(), ProtocolDigest: event.protocolDigest.String(),
			Ordinal: event.ordinal, CheckOrdinal: event.checkOrdinal,
			IdempotencyKey: event.idempotencyKey.String(), Evidence: commitCheckEvidenceToPayload(event.evidence),
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeReviewFixJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventReviewFixReserved:
		var wire reviewFixReservedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review-fix reservation: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := reviewFixProtocolFromCanonical(wire.Protocol)
		if err != nil {
			return nil, true, err
		}
		if protocol.digest.String() != wire.ProtocolDigest {
			return nil, true, fmt.Errorf("review-fix protocol payload digest does not match rules")
		}
		parent, err := ParseGitObjectID(wire.Parent)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewFixReservedJournalEvent(
			workspaceID, generation, attemptID, protocol, wire.Maximum, wire.Ordinal, parent,
		)
		if err != nil {
			return nil, true, err
		}
		if event.reservationKey.String() != wire.ReservationKey {
			return nil, true, fmt.Errorf("review-fix reservation payload key does not match")
		}
		return event, true, nil
	case JournalEventReviewFixIntended:
		var wire reviewFixIntendedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review-fix intent: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := ParseDigest(wire.ProtocolDigest)
		if err != nil {
			return nil, true, err
		}
		stepID, err := NewID(wire.StepID)
		if err != nil {
			return nil, true, err
		}
		parent, err := ParseGitObjectID(wire.Parent)
		if err != nil {
			return nil, true, err
		}
		reservation, err := ParseDigest(wire.ReservationKey)
		if err != nil {
			return nil, true, err
		}
		inspection, err := stagedCommitFromPayload(wire.Inspection)
		if err != nil {
			return nil, true, err
		}
		key, err := ParseDigest(wire.IdempotencyKey)
		if err != nil {
			return nil, true, err
		}
		event := ReviewFixIntendedJournalEvent{
			workspaceID: workspaceID, generation: generation, attemptID: attemptID,
			protocolDigest: protocol, stepID: stepID, ordinal: wire.Ordinal, parent: parent,
			reservationKey: reservation, inspection: inspection, body: wire.Body, idempotencyKey: key,
		}
		if err := event.validate(); err != nil {
			return nil, true, err
		}
		if err := validateCommitJournalRecordFootprint(event); err != nil {
			return nil, true, err
		}
		return event, true, nil
	case JournalEventReviewFixCommitRecorded:
		var wire reviewFixCommitRecordedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review-fix commit record: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := ParseDigest(wire.ProtocolDigest)
		if err != nil {
			return nil, true, err
		}
		intent, err := ParseDigest(wire.IntentKey)
		if err != nil {
			return nil, true, err
		}
		evidence, err := commitObjectEvidenceFromPayload(wire.Evidence)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewFixCommitRecordedJournalEvent(
			workspaceID, generation, attemptID, protocol, wire.Ordinal, intent, evidence,
		)
		return event, true, err
	case JournalEventReviewFixCheckRecorded:
		var wire reviewFixCheckRecordedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode review-fix check record: %w", err)
		}
		workspaceID, generation, attemptID, err := parseAttemptEnvelope(wire.WorkspaceID, wire.Generation, wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		protocol, err := ParseDigest(wire.ProtocolDigest)
		if err != nil {
			return nil, true, err
		}
		key, err := ParseDigest(wire.IdempotencyKey)
		if err != nil {
			return nil, true, err
		}
		evidence, err := commitCheckEvidenceFromPayload(wire.Evidence)
		if err != nil {
			return nil, true, err
		}
		event, err := NewReviewFixCheckRecordedJournalEvent(
			workspaceID, generation, attemptID, protocol, wire.Ordinal, wire.CheckOrdinal, key, evidence,
		)
		return event, true, err
	default:
		return nil, false, nil
	}
}

func reviewFixProtocolFromCanonical(wire canonicalReviewFixProtocol) (ReviewFixProtocol, error) {
	paths, err := NewCommitPathPolicy(wire.AllowedPaths, wire.FrozenPaths)
	if err != nil {
		return ReviewFixProtocol{}, err
	}
	checks := make([]CommitCheck, 0, len(wire.Checks))
	for _, checkWire := range wire.Checks {
		checkID, err := NewID(checkWire.ID)
		if err != nil {
			return ReviewFixProtocol{}, err
		}
		runner, err := NewID(checkWire.Runner)
		if err != nil {
			return ReviewFixProtocol{}, err
		}
		command, err := NewArgv(checkWire.Command...)
		if err != nil {
			return ReviewFixProtocol{}, err
		}
		expectation, err := NewCheckExpectation(checkWire.Expectation, checkWire.FailureIDs)
		if err != nil {
			return ReviewFixProtocol{}, err
		}
		check, err := NewCommitCheck(checkID, runner, checkWire.Parser, command, expectation)
		if err != nil {
			return ReviewFixProtocol{}, err
		}
		checks = append(checks, check)
	}
	return NewReviewFixProtocol(wire.SubjectPrefix, wire.BodyPolicy, paths, checks)
}
