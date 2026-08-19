package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
)

type featureRefCreationIntendedPayloadWire struct {
	WorkspaceID  string                 `json:"workspace_id"`
	Generation   string                 `json:"generation"`
	Binding      localTargetBindingWire `json:"binding"`
	IntentDigest string                 `json:"intent_digest"`
}

type featureRefCreatedPayloadWire struct {
	WorkspaceID  string `json:"workspace_id"`
	Generation   string `json:"generation"`
	IntentDigest string `json:"intent_digest"`
	FeatureRef   string `json:"feature_ref"`
	Head         string `json:"head"`
}

type workspaceAbandonedPayloadWire struct {
	WorkspaceID string `json:"workspace_id"`
	Generation  string `json:"generation"`
	FeatureRef  string `json:"feature_ref"`
	FeatureHead string `json:"feature_head,omitempty"`
	Reason      string `json:"reason"`
}

func marshalLocalTargetJournalEvent(
	event WorkspaceJournalEvent,
) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case FeatureRefCreationIntendedJournalEvent:
		value = featureRefCreationIntendedPayloadWire{
			WorkspaceID:  event.workspaceID.String(),
			Generation:   event.generation.String(),
			Binding:      localTargetBindingToWire(event.binding),
			IntentDigest: event.intentDigest.String(),
		}
	case FeatureRefCreatedJournalEvent:
		value = featureRefCreatedPayloadWire{
			WorkspaceID:  event.workspaceID.String(),
			Generation:   event.generation.String(),
			IntentDigest: event.intentDigest.String(),
			FeatureRef:   event.featureRef, Head: event.head.String(),
		}
	case WorkspaceAbandonedJournalEvent:
		value = workspaceAbandonedPayloadWire{
			WorkspaceID: event.workspaceID.String(),
			Generation:  event.generation.String(),
			FeatureRef:  event.featureRef,
			FeatureHead: event.featureHead.String(),
			Reason:      event.reason,
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeLocalTargetJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventFeatureRefCreationIntended:
		var wire featureRefCreationIntendedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf(
				"decode feature-ref creation intent: %w", err,
			)
		}
		workspaceID, err := NewID(wire.WorkspaceID)
		if err != nil {
			return nil, true, err
		}
		generation, err := ParseDigest(wire.Generation)
		if err != nil {
			return nil, true, err
		}
		binding, err := localTargetBindingFromWire(wire.Binding)
		if err != nil {
			return nil, true, err
		}
		event, err := NewFeatureRefCreationIntendedJournalEvent(
			workspaceID, generation, binding,
		)
		if err != nil {
			return nil, true, err
		}
		intentDigest, err := ParseDigest(wire.IntentDigest)
		if err != nil {
			return nil, true, err
		}
		if event.intentDigest != intentDigest {
			return nil, true, fmt.Errorf(
				"feature-ref creation intent digest mismatch",
			)
		}
		return event, true, nil
	case JournalEventFeatureRefCreated:
		var wire featureRefCreatedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf(
				"decode feature-ref creation completion: %w", err,
			)
		}
		workspaceID, err := NewID(wire.WorkspaceID)
		if err != nil {
			return nil, true, err
		}
		generation, err := ParseDigest(wire.Generation)
		if err != nil {
			return nil, true, err
		}
		intentDigest, err := ParseDigest(wire.IntentDigest)
		if err != nil {
			return nil, true, err
		}
		head, err := ParseGitObjectID(wire.Head)
		if err != nil {
			return nil, true, err
		}
		event, err := NewFeatureRefCreatedJournalEvent(
			workspaceID, generation, intentDigest,
			wire.FeatureRef, head,
		)
		return event, true, err
	case JournalEventWorkspaceAbandoned:
		var wire workspaceAbandonedPayloadWire
		if err := decodeStrictJSON(payload, &wire); err != nil {
			return nil, true, fmt.Errorf("decode workspace abandonment: %w", err)
		}
		workspaceID, err := NewID(wire.WorkspaceID)
		if err != nil {
			return nil, true, err
		}
		generation, err := ParseDigest(wire.Generation)
		if err != nil {
			return nil, true, err
		}
		featureHead := GitObjectID{}
		if strings.TrimSpace(wire.FeatureHead) != "" {
			featureHead, err = ParseGitObjectID(wire.FeatureHead)
			if err != nil {
				return nil, true, err
			}
		}
		event, err := NewWorkspaceAbandonedJournalEvent(
			workspaceID, generation, wire.FeatureRef, featureHead, wire.Reason,
		)
		return event, true, err
	default:
		return nil, false, nil
	}
}
