package workspace

import (
	"encoding/json"
	"fmt"
)

type workspaceCompletedPayloadWire struct {
	SchemaVersion int    `json:"schema_version"`
	WorkspaceID   string `json:"workspace_id"`
	Generation    string `json:"generation"`
	FeatureRef    string `json:"feature_ref"`
	FeatureHead   string `json:"feature_head"`
	ReportDigest  string `json:"report_digest"`
}

func marshalCompletionJournalEvent(
	event WorkspaceJournalEvent,
) (json.RawMessage, bool, error) {
	completed, ok := event.(WorkspaceCompletedJournalEvent)
	if !ok {
		return nil, false, nil
	}
	payload, err := json.Marshal(workspaceCompletedPayloadWire{
		SchemaVersion: JournalSchemaVersion,
		WorkspaceID:   completed.workspaceID.String(),
		Generation:    completed.generation.String(),
		FeatureRef:    completed.featureRef,
		FeatureHead:   completed.featureHead.String(),
		ReportDigest:  completed.reportDigest.String(),
	})
	return json.RawMessage(payload), true, err
}

func decodeCompletionJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	if eventType != JournalEventWorkspaceCompleted {
		return nil, false, nil
	}
	var wire workspaceCompletedPayloadWire
	if err := decodeStrictJSONRequired(payload, &wire); err != nil {
		return nil, true, fmt.Errorf(
			"decode workspace completion: %w", err,
		)
	}
	if wire.SchemaVersion != JournalSchemaVersion {
		return nil, true, fmt.Errorf(
			"workspace completion schema_version must be %d",
			JournalSchemaVersion,
		)
	}
	workspaceID, err := NewID(wire.WorkspaceID)
	if err != nil {
		return nil, true, fmt.Errorf(
			"workspace completion workspace_id: %w", err,
		)
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return nil, true, fmt.Errorf(
			"workspace completion generation: %w", err,
		)
	}
	featureHead, err := ParseGitObjectID(wire.FeatureHead)
	if err != nil {
		return nil, true, fmt.Errorf(
			"workspace completion feature_head: %w", err,
		)
	}
	reportDigest, err := ParseDigest(wire.ReportDigest)
	if err != nil {
		return nil, true, fmt.Errorf(
			"workspace completion report_digest: %w", err,
		)
	}
	event, err := NewWorkspaceCompletedJournalEvent(
		workspaceID,
		generation,
		wire.FeatureRef,
		featureHead,
		reportDigest,
	)
	return event, true, err
}
