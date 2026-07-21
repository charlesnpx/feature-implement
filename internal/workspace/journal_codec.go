package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type journalResourceWire struct {
	Kind     JournalResourceKind `json:"kind"`
	Identity string              `json:"identity"`
}

type journalResourceRevisionWire struct {
	Resource journalResourceWire `json:"resource"`
	Revision uint64              `json:"revision"`
}

type journalRecordWire struct {
	SchemaVersion int                           `json:"schema_version"`
	Sequence      uint64                        `json:"sequence"`
	OccurredAt    string                        `json:"occurred_at"`
	PreviousHash  string                        `json:"previous_hash"`
	EventHash     string                        `json:"event_hash"`
	Generation    string                        `json:"generation"`
	ReadSet       []journalResourceRevisionWire `json:"read_set"`
	WriteSet      []journalResourceWire         `json:"write_set"`
	Type          JournalEventType              `json:"type"`
	Payload       json.RawMessage               `json:"payload"`
}

type journalRecordBodyWire struct {
	SchemaVersion int                           `json:"schema_version"`
	Sequence      uint64                        `json:"sequence"`
	OccurredAt    string                        `json:"occurred_at"`
	PreviousHash  string                        `json:"previous_hash"`
	Generation    string                        `json:"generation"`
	ReadSet       []journalResourceRevisionWire `json:"read_set"`
	WriteSet      []journalResourceWire         `json:"write_set"`
	Type          JournalEventType              `json:"type"`
	Payload       json.RawMessage               `json:"payload"`
}

type workspaceInitializedPayloadWire struct {
	WorkspaceID      string `json:"workspace_id"`
	Generation       string `json:"generation"`
	DefinitionDigest string `json:"definition_digest"`
}

type candidateStoredPayloadWire struct {
	WorkspaceID         string `json:"workspace_id"`
	ActiveGeneration    string `json:"active_generation"`
	CandidateGeneration string `json:"candidate_generation"`
	Recovered           bool   `json:"recovered"`
}

type mergeUnitReferenceJSON struct {
	PlanID      string `json:"plan_id"`
	MergeUnitID string `json:"merge_unit_id"`
}

type generationActivatedPayloadWire struct {
	WorkspaceID        string                   `json:"workspace_id"`
	PriorGeneration    string                   `json:"prior_generation"`
	ActiveGeneration   string                   `json:"active_generation"`
	ComparisonDigest   string                   `json:"comparison_digest"`
	OwnerReceiptDigest string                   `json:"owner_receipt_digest"`
	BudgetHistory      string                   `json:"budget_history_digest"`
	ApprovalHistory    string                   `json:"approval_history_digest"`
	EvidenceHistory    string                   `json:"evidence_history_digest"`
	ChangedMergeUnits  []mergeUnitReferenceJSON `json:"changed_merge_units"`
}

type journalTailRecoveredPayloadWire struct {
	WorkspaceID   string `json:"workspace_id"`
	Generation    string `json:"generation"`
	DiscardOffset int64  `json:"discard_offset"`
	DiscardSize   int64  `json:"discard_size"`
	DiscardDigest string `json:"discard_digest"`
	ResultingHead string `json:"resulting_head"`
}

func JournalGenesisHash() Digest {
	digest, _ := ParseDigest("sha256:" + strings.Repeat("0", 64))
	return digest
}

func buildJournalRecord(snapshot JournalSnapshot, request JournalAppend) (JournalRecord, error) {
	if request.event == nil {
		return JournalRecord{}, fmt.Errorf("journal append event is required")
	}
	record := JournalRecord{
		sequence: uint64(len(snapshot.records)) + 1, occurredAt: request.occurredAt.UTC(),
		previousHash: snapshot.head, generation: request.event.boundGeneration(),
		readSet:  append([]JournalResourceRevision(nil), request.readSet...),
		writeSet: append([]JournalResource(nil), request.writeSet...),
		event:    cloneWorkspaceJournalEvent(request.event),
	}
	body, err := marshalJournalRecordBody(record)
	if err != nil {
		return JournalRecord{}, err
	}
	record.eventHash = DigestBytes(body)
	return record, nil
}

func marshalJournalRecord(record JournalRecord) ([]byte, error) {
	payload, err := marshalWorkspaceJournalEvent(record.event)
	if err != nil {
		return nil, err
	}
	value := journalRecordWire{
		SchemaVersion: JournalSchemaVersion,
		Sequence:      record.sequence,
		OccurredAt:    record.occurredAt.UTC().Format(time.RFC3339Nano),
		PreviousHash:  record.previousHash.String(),
		EventHash:     record.eventHash.String(),
		Generation:    record.generation.String(),
		ReadSet:       journalReadSetWire(record.readSet),
		WriteSet:      journalWriteSetWire(record.writeSet),
		Type:          record.event.eventType(),
		Payload:       payload,
	}
	return json.Marshal(value)
}

func marshalJournalRecordBody(record JournalRecord) ([]byte, error) {
	payload, err := marshalWorkspaceJournalEvent(record.event)
	if err != nil {
		return nil, err
	}
	value := journalRecordBodyWire{
		SchemaVersion: JournalSchemaVersion,
		Sequence:      record.sequence,
		OccurredAt:    record.occurredAt.UTC().Format(time.RFC3339Nano),
		PreviousHash:  record.previousHash.String(),
		Generation:    record.generation.String(),
		ReadSet:       journalReadSetWire(record.readSet),
		WriteSet:      journalWriteSetWire(record.writeSet),
		Type:          record.event.eventType(),
		Payload:       payload,
	}
	return json.Marshal(value)
}

func journalReadSetWire(values []JournalResourceRevision) []journalResourceRevisionWire {
	result := make([]journalResourceRevisionWire, 0, len(values))
	for _, revision := range values {
		result = append(result, journalResourceRevisionWire{
			Resource: journalResourceWire{Kind: revision.resource.kind, Identity: revision.resource.identity},
			Revision: revision.revision,
		})
	}
	return result
}

func journalWriteSetWire(values []JournalResource) []journalResourceWire {
	result := make([]journalResourceWire, 0, len(values))
	for _, resource := range values {
		result = append(result, journalResourceWire{Kind: resource.kind, Identity: resource.identity})
	}
	return result
}

func marshalWorkspaceJournalEvent(event WorkspaceJournalEvent) (json.RawMessage, error) {
	if event == nil {
		return nil, fmt.Errorf("journal event is required")
	}
	if err := event.validate(); err != nil {
		return nil, err
	}
	var value any
	switch event := event.(type) {
	case WorkspaceInitializedJournalEvent:
		value = workspaceInitializedPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			DefinitionDigest: event.definitionDigest.String(),
		}
	case CandidateGenerationStoredJournalEvent:
		value = candidateStoredPayloadWire{
			WorkspaceID: event.workspaceID.String(), ActiveGeneration: event.activeGeneration.String(),
			CandidateGeneration: event.candidateGeneration.String(), Recovered: event.recovered,
		}
	case GenerationActivatedJournalEvent:
		changed := make([]mergeUnitReferenceJSON, 0, len(event.changedMergeUnits))
		for _, reference := range event.changedMergeUnits {
			changed = append(changed, mergeUnitReferenceJSON{PlanID: reference.planID.String(), MergeUnitID: reference.mergeUnitID.String()})
		}
		value = generationActivatedPayloadWire{
			WorkspaceID: event.workspaceID.String(), PriorGeneration: event.priorGeneration.String(),
			ActiveGeneration: event.activeGeneration.String(), ComparisonDigest: event.comparisonDigest.String(),
			OwnerReceiptDigest: event.ownerReceiptDigest.String(),
			BudgetHistory:      event.history.budgets.String(), ApprovalHistory: event.history.approvals.String(),
			EvidenceHistory: event.history.evidence.String(), ChangedMergeUnits: changed,
		}
	case JournalTailRecoveredEvent:
		value = journalTailRecoveredPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			DiscardOffset: event.discardOffset, DiscardSize: event.discardSize,
			DiscardDigest: event.discardDigest.String(), ResultingHead: event.resultingHead.String(),
		}
	default:
		payload, supported, err := marshalAttemptJournalEvent(event)
		if supported {
			return payload, err
		}
		payload, supported, err = marshalCommitJournalEvent(event)
		if supported {
			return payload, err
		}
		payload, supported, err = marshalAuthorizationJournalEvent(event)
		if supported {
			return payload, err
		}
		payload, supported, err = marshalReviewJournalEvent(event)
		if supported {
			return payload, err
		}
		return nil, fmt.Errorf("unsupported workspace journal event %T", event)
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), err
}

func parseJournalRecord(raw []byte) (JournalRecord, error) {
	var wire journalRecordWire
	if err := decodeStrictJSON(raw, &wire); err != nil {
		return JournalRecord{}, err
	}
	if wire.SchemaVersion != JournalSchemaVersion {
		return JournalRecord{}, fmt.Errorf("journal schema_version %d is not supported", wire.SchemaVersion)
	}
	if wire.Sequence == 0 {
		return JournalRecord{}, fmt.Errorf("journal sequence must be positive")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, wire.OccurredAt)
	if err != nil || wire.OccurredAt != occurredAt.UTC().Format(time.RFC3339Nano) {
		return JournalRecord{}, fmt.Errorf("journal occurred_at must be canonical UTC RFC3339Nano")
	}
	previousHash, err := ParseDigest(wire.PreviousHash)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("journal previous_hash: %w", err)
	}
	eventHash, err := ParseDigest(wire.EventHash)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("journal event_hash: %w", err)
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("journal generation: %w", err)
	}
	readSet, err := parseJournalReadSet(wire.ReadSet)
	if err != nil {
		return JournalRecord{}, err
	}
	writeSet, err := parseJournalWriteSet(wire.WriteSet)
	if err != nil {
		return JournalRecord{}, err
	}
	if len(writeSet) == 0 {
		return JournalRecord{}, fmt.Errorf("journal write_set must not be empty")
	}
	event, err := decodeWorkspaceJournalEvent(wire.Type, wire.Payload)
	if err != nil {
		return JournalRecord{}, err
	}
	if err := validateJournalEventResources(event, readSet, writeSet); err != nil {
		return JournalRecord{}, err
	}
	if event.boundGeneration() != generation {
		return JournalRecord{}, fmt.Errorf("journal event generation does not match its envelope")
	}
	record := JournalRecord{
		sequence: wire.Sequence, occurredAt: occurredAt.UTC(), previousHash: previousHash,
		eventHash: eventHash, generation: generation, readSet: readSet, writeSet: writeSet, event: event,
	}
	body, err := marshalJournalRecordBody(record)
	if err != nil {
		return JournalRecord{}, err
	}
	if DigestBytes(body) != eventHash {
		return JournalRecord{}, fmt.Errorf("journal event hash mismatch")
	}
	canonical, err := marshalJournalRecord(record)
	if err != nil {
		return JournalRecord{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return JournalRecord{}, fmt.Errorf("journal record is not in canonical JSON form")
	}
	return record, nil
}

func parseJournalReadSet(values []journalResourceRevisionWire) ([]JournalResourceRevision, error) {
	result := make([]JournalResourceRevision, 0, len(values))
	for _, value := range values {
		resource, err := NewJournalResource(value.Resource.Kind, value.Resource.Identity)
		if err != nil {
			return nil, fmt.Errorf("journal read_set: %w", err)
		}
		revision, _ := NewJournalResourceRevision(resource, value.Revision)
		result = append(result, revision)
	}
	normalized, err := normalizeJournalReadSet(result)
	if err != nil {
		return nil, err
	}
	if !equalJournalReadSets(result, normalized) {
		return nil, fmt.Errorf("journal read_set must be unique and sorted")
	}
	return result, nil
}

func parseJournalWriteSet(values []journalResourceWire) ([]JournalResource, error) {
	result := make([]JournalResource, 0, len(values))
	for _, value := range values {
		resource, err := NewJournalResource(value.Kind, value.Identity)
		if err != nil {
			return nil, fmt.Errorf("journal write_set: %w", err)
		}
		result = append(result, resource)
	}
	normalized, err := normalizeJournalWriteSet(result)
	if err != nil {
		return nil, err
	}
	if !equalJournalWriteSets(result, normalized) {
		return nil, fmt.Errorf("journal write_set must be unique and sorted")
	}
	return result, nil
}

func decodeWorkspaceJournalEvent(eventType JournalEventType, payload json.RawMessage) (WorkspaceJournalEvent, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("journal payload is required")
	}
	switch eventType {
	case JournalEventWorkspaceInitialized:
		var wire workspaceInitializedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, fmt.Errorf("decode workspace initialization: %w", err)
		}
		workspaceID, generation, definitionDigest, err := parseWorkspaceGenerationBindings(wire.WorkspaceID, wire.Generation, wire.DefinitionDigest)
		if err != nil {
			return nil, err
		}
		return NewWorkspaceInitializedJournalEvent(workspaceID, generation, definitionDigest)
	case JournalEventCandidateStored:
		var wire candidateStoredPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, fmt.Errorf("decode candidate storage: %w", err)
		}
		workspaceID, err := NewID(wire.WorkspaceID)
		if err != nil {
			return nil, err
		}
		active, err := ParseDigest(wire.ActiveGeneration)
		if err != nil {
			return nil, err
		}
		candidate, err := ParseDigest(wire.CandidateGeneration)
		if err != nil {
			return nil, err
		}
		return NewCandidateGenerationStoredJournalEvent(workspaceID, active, candidate, wire.Recovered)
	case JournalEventGenerationActivated:
		var wire generationActivatedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, fmt.Errorf("decode generation activation: %w", err)
		}
		workspaceID, err := NewID(wire.WorkspaceID)
		if err != nil {
			return nil, err
		}
		prior, err := ParseDigest(wire.PriorGeneration)
		if err != nil {
			return nil, err
		}
		active, err := ParseDigest(wire.ActiveGeneration)
		if err != nil {
			return nil, err
		}
		comparison, err := ParseDigest(wire.ComparisonDigest)
		if err != nil {
			return nil, err
		}
		receipt, err := ParseDigest(wire.OwnerReceiptDigest)
		if err != nil {
			return nil, err
		}
		budgetHistory, err := ParseDigest(wire.BudgetHistory)
		if err != nil {
			return nil, err
		}
		approvalHistory, err := ParseDigest(wire.ApprovalHistory)
		if err != nil {
			return nil, err
		}
		evidenceHistory, err := ParseDigest(wire.EvidenceHistory)
		if err != nil {
			return nil, err
		}
		history, err := NewRuntimeHistoryBinding(budgetHistory, approvalHistory, evidenceHistory)
		if err != nil {
			return nil, err
		}
		changed := make([]MergeUnitReference, 0, len(wire.ChangedMergeUnits))
		for _, value := range wire.ChangedMergeUnits {
			planID, err := NewID(value.PlanID)
			if err != nil {
				return nil, err
			}
			unitID, err := NewID(value.MergeUnitID)
			if err != nil {
				return nil, err
			}
			reference, _ := NewMergeUnitReference(planID, unitID)
			changed = append(changed, reference)
		}
		return NewGenerationActivatedJournalEvent(workspaceID, prior, active, comparison, receipt, history, changed)
	case JournalEventTailRecovered:
		var wire journalTailRecoveredPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, fmt.Errorf("decode journal recovery: %w", err)
		}
		workspaceID, err := NewID(wire.WorkspaceID)
		if err != nil {
			return nil, err
		}
		generation, err := ParseDigest(wire.Generation)
		if err != nil {
			return nil, err
		}
		discardDigest, err := ParseDigest(wire.DiscardDigest)
		if err != nil {
			return nil, err
		}
		resultingHead, err := ParseDigest(wire.ResultingHead)
		if err != nil {
			return nil, err
		}
		return NewJournalTailRecoveredEvent(workspaceID, generation, wire.DiscardOffset, wire.DiscardSize, discardDigest, resultingHead)
	default:
		event, supported, err := decodeAttemptJournalEvent(eventType, payload)
		if supported {
			return event, err
		}
		event, supported, err = decodeCommitJournalEvent(eventType, payload)
		if supported {
			return event, err
		}
		event, supported, err = decodeAuthorizationJournalEvent(eventType, payload)
		if supported {
			return event, err
		}
		event, supported, err = decodeReviewJournalEvent(eventType, payload)
		if supported {
			return event, err
		}
		return nil, fmt.Errorf("unsupported journal event type %q", eventType)
	}
}

func parseWorkspaceGenerationBindings(workspace, generation, definition string) (ID, Digest, Digest, error) {
	workspaceID, err := NewID(workspace)
	if err != nil {
		return ID{}, Digest{}, Digest{}, err
	}
	generationDigest, err := ParseDigest(generation)
	if err != nil {
		return ID{}, Digest{}, Digest{}, err
	}
	definitionDigest, err := ParseDigest(definition)
	if err != nil {
		return ID{}, Digest{}, Digest{}, err
	}
	return workspaceID, generationDigest, definitionDigest, nil
}

func decodeStrictJSON(source []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func equalJournalReadSets(left, right []JournalResourceRevision) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].resource != right[index].resource || left[index].revision != right[index].revision {
			return false
		}
	}
	return true
}

func equalJournalWriteSets(left, right []JournalResource) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
