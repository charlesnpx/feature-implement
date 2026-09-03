package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

type journalRecordWire struct {
	SchemaVersion int              `json:"schema_version"`
	Sequence      uint64           `json:"sequence"`
	OccurredAt    string           `json:"occurred_at"`
	PreviousHash  string           `json:"previous_hash"`
	EventHash     string           `json:"event_hash"`
	Generation    string           `json:"generation"`
	Type          JournalEventType `json:"type"`
	Payload       json.RawMessage  `json:"payload"`
}

type journalRecordBodyWire struct {
	SchemaVersion int              `json:"schema_version"`
	Sequence      uint64           `json:"sequence"`
	OccurredAt    string           `json:"occurred_at"`
	PreviousHash  string           `json:"previous_hash"`
	Generation    string           `json:"generation"`
	Type          JournalEventType `json:"type"`
	Payload       json.RawMessage  `json:"payload"`
}

type workspaceInitializedPayloadWire struct {
	WorkspaceID                  string `json:"workspace_id"`
	Generation                   string `json:"generation"`
	DefinitionDigest             string `json:"definition_digest"`
	PlanCheckpoint               string `json:"plan_checkpoint,omitempty"`
	PlanCheckpointArtifactDigest string `json:"plan_checkpoint_artifact_digest,omitempty"`
	WorktreeRoot                 string `json:"worktree_root"`
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
		event: cloneWorkspaceJournalEvent(request.event),
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
		SchemaVersion: journalRecordSchemaVersion,
		Sequence:      record.sequence,
		OccurredAt:    record.occurredAt.UTC().Format(time.RFC3339Nano),
		PreviousHash:  record.previousHash.String(),
		EventHash:     record.eventHash.String(),
		Generation:    record.generation.String(),
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
		SchemaVersion: journalRecordSchemaVersion,
		Sequence:      record.sequence,
		OccurredAt:    record.occurredAt.UTC().Format(time.RFC3339Nano),
		PreviousHash:  record.previousHash.String(),
		Generation:    record.generation.String(),
		Type:          record.event.eventType(),
		Payload:       payload,
	}
	return json.Marshal(value)
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
			DefinitionDigest: event.definitionDigest.String(), PlanCheckpoint: event.planCheckpoint.String(),
			PlanCheckpointArtifactDigest: event.planCheckpointArtifactDigest.String(),
			WorktreeRoot:                 event.worktreeRoot.Path(),
		}
	case JournalTailRecoveredEvent:
		value = journalTailRecoveredPayloadWire{
			WorkspaceID: event.workspaceID.String(), Generation: event.generation.String(),
			DiscardOffset: event.discardOffset, DiscardSize: event.discardSize,
			DiscardDigest: event.discardDigest.String(), ResultingHead: event.resultingHead.String(),
		}
	default:
		payload, supported, err := marshalLocalTargetJournalEvent(event)
		if supported {
			return payload, err
		}
		payload, supported, err = marshalAttemptJournalEvent(event)
		if supported {
			return payload, err
		}
		payload, supported, err = marshalReviewJournalEvent(event)
		if supported {
			return payload, err
		}
		payload, supported, err = marshalIntegrationJournalEvent(event)
		if supported {
			return payload, err
		}
		payload, supported, err = marshalCompletionJournalEvent(event)
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
	if wire.SchemaVersion != journalRecordSchemaVersion {
		return JournalRecord{}, fmt.Errorf(
			"journal format is incompatible with this runtime; regenerate from committed sources",
		)
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
	event, err := decodeWorkspaceJournalEvent(wire.Type, wire.Payload)
	if err != nil {
		return JournalRecord{}, err
	}
	if event.boundGeneration() != generation {
		return JournalRecord{}, fmt.Errorf("journal event generation does not match its envelope")
	}
	record := JournalRecord{
		sequence: wire.Sequence, occurredAt: occurredAt.UTC(), previousHash: previousHash,
		eventHash: eventHash, generation: generation, event: event,
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
		worktreeRoot, err := NewWorkspaceWorktreeRootBinding(wire.WorktreeRoot)
		if err != nil {
			return nil, fmt.Errorf(
				"workspace initialization worktree root: %w", err,
			)
		}
		var checkpoint []PlanCheckpointJournalBinding
		if strings.TrimSpace(wire.PlanCheckpoint) != "" {
			parsed, err := ParseDigest(wire.PlanCheckpoint)
			if err != nil {
				return nil, fmt.Errorf("plan checkpoint: %w", err)
			}
			artifactDigest, err := ParseDigest(wire.PlanCheckpointArtifactDigest)
			if err != nil {
				return nil, fmt.Errorf("plan checkpoint artifact digest: %w", err)
			}
			checkpoint = append(checkpoint, PlanCheckpointJournalBinding{
				CheckpointID: parsed, ArtifactDigest: artifactDigest,
			})
		} else if strings.TrimSpace(wire.PlanCheckpointArtifactDigest) != "" {
			return nil, fmt.Errorf("plan checkpoint artifact digest requires plan checkpoint")
		}
		return NewWorkspaceInitializedJournalEvent(
			workspaceID, generation, definitionDigest,
			worktreeRoot, checkpoint...,
		)
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
		event, supported, err := decodeLocalTargetJournalEvent(eventType, payload)
		if supported {
			return event, err
		}
		event, supported, err = decodeAttemptJournalEvent(eventType, payload)
		if supported {
			return event, err
		}
		event, supported, err = decodeReviewJournalEvent(eventType, payload)
		if supported {
			return event, err
		}
		event, supported, err = decodeIntegrationJournalEvent(
			eventType, payload,
		)
		if supported {
			return event, err
		}
		event, supported, err = decodeCompletionJournalEvent(
			eventType, payload,
		)
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
	if err := rejectDuplicateJSONObjectKeys(source); err != nil {
		return err
	}
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

// rejectDuplicateJSONObjectKeys rejects ambiguous JSON before decoding it
// into a durable record or materialization document. The standard decoder
// accepts duplicate keys by choosing one, which is unsuitable for data whose
// bytes participate in integrity checks.
func rejectDuplicateJSONObjectKeys(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder, 0); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON contains trailing data")
		}
		return err
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder, depth uint8) error {
	if depth > 64 {
		return fmt.Errorf("JSON exceeds its nesting bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("JSON contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("JSON object is incomplete")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("JSON array is incomplete")
		}
	default:
		return fmt.Errorf("JSON contains an invalid delimiter")
	}
	return nil
}

func decodeStrictJSONRequired(source []byte, target any) error {
	if err := decodeStrictJSON(source, target); err != nil {
		return err
	}
	return validateRequiredJSONFields(source, target)
}

var jsonRawMessageType = reflect.TypeOf(json.RawMessage{})

// validateRequiredJSONFields makes the runtime decoder honor the same field
// presence contract used by the emitted schemas: every exported JSON field
// without omitempty is required, and an explicitly supplied field may not be
// null. Nested structs and arrays are checked recursively.
func validateRequiredJSONFields(source []byte, target any) error {
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer || reflect.ValueOf(target).IsNil() {
		return fmt.Errorf("strict JSON target must be a non-nil pointer")
	}
	return validateRequiredJSONValue(json.RawMessage(source), targetType.Elem(), "$", true)
}

func validateRequiredJSONValue(raw json.RawMessage, targetType reflect.Type, path string, required bool) error {
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if required {
			return fmt.Errorf("required JSON field %s must not be null", path)
		}
		return fmt.Errorf("JSON field %s must not be null", path)
	}
	if targetType == jsonRawMessageType {
		return nil
	}

	switch targetType.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		for index := 0; index < targetType.NumField(); index++ {
			field := targetType.Field(index)
			if field.PkgPath != "" {
				continue
			}
			name, options := parseJSONFieldTag(field)
			if name == "-" {
				continue
			}
			value, exists := object[name]
			fieldPath := path + "." + name
			fieldRequired := !options["omitempty"]
			if !exists {
				if fieldRequired {
					return fmt.Errorf("required JSON field %s is missing", fieldPath)
				}
				continue
			}
			if err := validateRequiredJSONValue(value, field.Type, fieldPath, fieldRequired); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if targetType.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for index, item := range items {
			if err := validateRequiredJSONValue(item, targetType.Elem(), fmt.Sprintf("%s[%d]", path, index), true); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseJSONFieldTag(field reflect.StructField) (string, map[string]bool) {
	name, rawOptions, hasOptions := strings.Cut(field.Tag.Get("json"), ",")
	if name == "" {
		name = field.Name
	}
	options := make(map[string]bool)
	if hasOptions {
		for _, option := range strings.Split(rawOptions, ",") {
			if option != "" {
				options[option] = true
			}
		}
	}
	return name, options
}

// DecodeStrictJSON exposes the shared bounded-command decoding contract to
// composition roots without exposing wire DTOs from the domain package.
func DecodeStrictJSON(source []byte, target any) error {
	return decodeStrictJSONRequired(source, target)
}
