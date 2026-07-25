package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type integrationIntendedPayloadWire struct {
	Intent       integrationIntentDigestWire `json:"intent"`
	IntentDigest string                      `json:"intent_digest"`
}

type integrationCompletedPayloadWire struct {
	WorkspaceID         string                             `json:"workspace_id"`
	Generation          string                             `json:"generation"`
	AttemptID           string                             `json:"attempt_id"`
	PlanID              string                             `json:"plan_id"`
	MergeUnitID         string                             `json:"merge_unit_id"`
	IntentDigest        string                             `json:"intent_digest"`
	FeatureRef          string                             `json:"feature_ref"`
	ExpectedFeatureHead string                             `json:"expected_feature_head"`
	AcceptedHead        string                             `json:"accepted_head"`
	AcceptedTree        string                             `json:"accepted_tree"`
	MergeCommit         string                             `json:"merge_commit"`
	LeaseID             string                             `json:"lease_id"`
	SerialSegment       string                             `json:"serial_segment,omitempty"`
	SupersededAttempts  []integrationSupersededAttemptWire `json:"superseded_attempts,omitempty"`
}

type integrationSupersededAttemptWire struct {
	AttemptID         string              `json:"attempt_id"`
	PlanID            string              `json:"plan_id"`
	MergeUnitID       string              `json:"merge_unit_id"`
	Base              string              `json:"base"`
	Phase             AttemptRuntimePhase `json:"phase"`
	LeaseID           string              `json:"lease_id,omitempty"`
	SerialSegment     string              `json:"serial_segment,omitempty"`
	SerialSegmentHeld bool                `json:"serial_segment_held"`
}

func marshalIntegrationJournalEvent(
	event WorkspaceJournalEvent,
) (json.RawMessage, bool, error) {
	var value any
	switch event := event.(type) {
	case MergeUnitIntegrationIntendedJournalEvent:
		value = integrationIntendedPayloadWire{
			Intent:       integrationIntentDigestValue(event.intent),
			IntentDigest: event.intent.digest.String(),
		}
	case MergeUnitIntegratedJournalEvent:
		supersededAttempts := make(
			[]integrationSupersededAttemptWire,
			0, len(event.supersededAttempts),
		)
		for _, attempt := range event.supersededAttempts {
			supersededAttempts = append(
				supersededAttempts,
				integrationSupersededAttemptWire{
					AttemptID:         attempt.attemptID.String(),
					PlanID:            attempt.mergeUnit.planID.String(),
					MergeUnitID:       attempt.mergeUnit.mergeUnitID.String(),
					Base:              attempt.base.String(),
					Phase:             attempt.phase,
					LeaseID:           attempt.leaseID.String(),
					SerialSegment:     attempt.serialSegment.String(),
					SerialSegmentHeld: attempt.serialSegmentHeld,
				},
			)
		}
		value = integrationCompletedPayloadWire{
			WorkspaceID:         event.workspaceID.String(),
			Generation:          event.generation.String(),
			AttemptID:           event.attemptID.String(),
			PlanID:              event.mergeUnit.planID.String(),
			MergeUnitID:         event.mergeUnit.mergeUnitID.String(),
			IntentDigest:        event.intentDigest.String(),
			FeatureRef:          event.featureRef,
			ExpectedFeatureHead: event.expectedFeatureHead.String(),
			AcceptedHead:        event.acceptedHead.String(),
			AcceptedTree:        event.acceptedTree.String(),
			MergeCommit:         event.mergeCommit.String(),
			LeaseID:             event.leaseID.String(),
			SerialSegment:       event.serialSegment.String(),
			SupersededAttempts:  supersededAttempts,
		}
	default:
		return nil, false, nil
	}
	payload, err := json.Marshal(value)
	return json.RawMessage(payload), true, err
}

func decodeIntegrationJournalEvent(
	eventType JournalEventType,
	payload json.RawMessage,
) (WorkspaceJournalEvent, bool, error) {
	switch eventType {
	case JournalEventMergeUnitIntegrationIntended:
		var wire integrationIntendedPayloadWire
		if err := decodeStrictJSONRequired(payload, &wire); err != nil {
			return nil, true, fmt.Errorf(
				"decode merge-unit integration intent: %w", err,
			)
		}
		intent, err := integrationIntentFromWire(wire.Intent)
		if err != nil {
			return nil, true, err
		}
		intentDigest, err := ParseDigest(wire.IntentDigest)
		if err != nil {
			return nil, true, fmt.Errorf(
				"integration intent digest: %w", err,
			)
		}
		if intent.digest != intentDigest {
			return nil, true, fmt.Errorf(
				"integration intent payload digest mismatch",
			)
		}
		event, err := NewMergeUnitIntegrationIntendedJournalEvent(intent)
		return event, true, err
	case JournalEventMergeUnitIntegrated:
		var wire integrationCompletedPayloadWire
		if err := decodeStrictJSONRequired(payload, &wire); err != nil {
			return nil, true, fmt.Errorf(
				"decode merge-unit integration completion: %w", err,
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
		attemptID, err := NewID(wire.AttemptID)
		if err != nil {
			return nil, true, err
		}
		planID, err := NewID(wire.PlanID)
		if err != nil {
			return nil, true, err
		}
		mergeUnitID, err := NewID(wire.MergeUnitID)
		if err != nil {
			return nil, true, err
		}
		mergeUnit, err := NewMergeUnitReference(planID, mergeUnitID)
		if err != nil {
			return nil, true, err
		}
		intentDigest, err := ParseDigest(wire.IntentDigest)
		if err != nil {
			return nil, true, err
		}
		expectedFeatureHead, err := ParseGitObjectID(
			wire.ExpectedFeatureHead,
		)
		if err != nil {
			return nil, true, err
		}
		acceptedHead, err := ParseGitObjectID(wire.AcceptedHead)
		if err != nil {
			return nil, true, err
		}
		acceptedTree, err := ParseGitObjectID(wire.AcceptedTree)
		if err != nil {
			return nil, true, err
		}
		mergeCommit, err := ParseGitObjectID(wire.MergeCommit)
		if err != nil {
			return nil, true, err
		}
		leaseID, err := NewID(wire.LeaseID)
		if err != nil {
			return nil, true, err
		}
		serialSegment, err := parseOptionalID(wire.SerialSegment)
		if err != nil {
			return nil, true, err
		}
		supersededAttempts := make(
			[]integrationSupersededAttempt,
			0, len(wire.SupersededAttempts),
		)
		for _, supersededWire := range wire.SupersededAttempts {
			supersededAttemptID, err := NewID(
				supersededWire.AttemptID,
			)
			if err != nil {
				return nil, true, err
			}
			supersededPlanID, err := NewID(
				supersededWire.PlanID,
			)
			if err != nil {
				return nil, true, err
			}
			supersededMergeUnitID, err := NewID(
				supersededWire.MergeUnitID,
			)
			if err != nil {
				return nil, true, err
			}
			supersededMergeUnit, err := NewMergeUnitReference(
				supersededPlanID, supersededMergeUnitID,
			)
			if err != nil {
				return nil, true, err
			}
			supersededBase, err := ParseGitObjectID(
				supersededWire.Base,
			)
			if err != nil {
				return nil, true, err
			}
			supersededLease, err := parseOptionalID(
				supersededWire.LeaseID,
			)
			if err != nil {
				return nil, true, err
			}
			supersededSegment, err := parseOptionalID(
				supersededWire.SerialSegment,
			)
			if err != nil {
				return nil, true, err
			}
			supersededAttempts = append(
				supersededAttempts,
				integrationSupersededAttempt{
					attemptID:         supersededAttemptID,
					mergeUnit:         supersededMergeUnit,
					base:              supersededBase,
					phase:             supersededWire.Phase,
					leaseID:           supersededLease,
					serialSegment:     supersededSegment,
					serialSegmentHeld: supersededWire.SerialSegmentHeld,
				},
			)
		}
		event := MergeUnitIntegratedJournalEvent{
			workspaceID:         workspaceID,
			generation:          generation,
			attemptID:           attemptID,
			mergeUnit:           mergeUnit,
			intentDigest:        intentDigest,
			featureRef:          wire.FeatureRef,
			expectedFeatureHead: expectedFeatureHead,
			acceptedHead:        acceptedHead,
			acceptedTree:        acceptedTree,
			mergeCommit:         mergeCommit,
			leaseID:             leaseID,
			serialSegment:       serialSegment,
			supersededAttempts:  supersededAttempts,
		}
		if err := event.validate(); err != nil {
			return nil, true, err
		}
		return event, true, nil
	default:
		return nil, false, nil
	}
}

func integrationIntentFromWire(
	wire integrationIntentDigestWire,
) (MergeUnitIntegrationIntent, error) {
	if wire.SchemaVersion != JournalSchemaVersion {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration intent schema_version must be %d",
			JournalSchemaVersion,
		)
	}
	workspaceID, err := NewID(wire.WorkspaceID)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	generation, err := ParseDigest(wire.Generation)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	attemptID, err := NewID(wire.AttemptID)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	planID, err := NewID(wire.PlanID)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	mergeUnitID, err := NewID(wire.MergeUnitID)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	mergeUnit, err := NewMergeUnitReference(planID, mergeUnitID)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	expectedFeatureHead, err := ParseGitObjectID(
		wire.ExpectedFeatureHead,
	)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	attemptWorktreeBinding, err := attemptWorktreeGitBindingFromWire(
		wire.AttemptWorktreeBinding,
	)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	acceptedHead, err := ParseGitObjectID(wire.AcceptedHead)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	acceptedTree, err := ParseGitObjectID(wire.AcceptedTree)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	reviewReadiness, err := parseOptionalIntegrationDigest(
		wire.ReviewReadinessDigest,
	)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	adoptedHead, err := parseOptionalIntegrationDigest(
		wire.AdoptedHeadEventDigest,
	)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	authorAt, err := parseCanonicalIntegrationTime(wire.AuthorAt)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	committerAt, err := parseCanonicalIntegrationTime(wire.CommitterAt)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	if authorAt != committerAt {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration author and committer timestamps differ",
		)
	}
	intent, err := NewMergeUnitIntegrationIntent(
		MergeUnitIntegrationIntentOptions{
			WorkspaceID:            workspaceID,
			Generation:             generation,
			AttemptID:              attemptID,
			MergeUnit:              mergeUnit,
			FeatureRef:             wire.FeatureRef,
			ExpectedFeatureHead:    expectedFeatureHead,
			ExpectedFeatureMarker:  wire.ExpectedFeatureMarker,
			AttemptWorktreeBinding: attemptWorktreeBinding,
			AcceptedHead:           acceptedHead,
			AcceptedTree:           acceptedTree,
			ReviewReadinessDigest:  reviewReadiness,
			AdoptedHeadEventDigest: adoptedHead,
			OccurredAt:             authorAt,
		},
	)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	actual, err := json.Marshal(wire)
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	expected, err := json.Marshal(integrationIntentDigestValue(intent))
	if err != nil {
		return MergeUnitIntegrationIntent{}, err
	}
	if !bytes.Equal(actual, expected) {
		return MergeUnitIntegrationIntent{}, fmt.Errorf(
			"integration intent payload is not canonical",
		)
	}
	return intent, nil
}

func parseOptionalIntegrationDigest(value string) (Digest, error) {
	if strings.TrimSpace(value) == "" {
		return Digest{}, nil
	}
	return ParseDigest(value)
}

func parseCanonicalIntegrationTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() ||
		value != parsed.UTC().Format(time.RFC3339Nano) ||
		parsed.Location() != time.UTC || parsed.Nanosecond() != 0 {
		return time.Time{}, fmt.Errorf(
			"integration timestamp must be canonical whole-second UTC RFC3339Nano",
		)
	}
	return parsed, nil
}
