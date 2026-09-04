package workspace

import (
	"fmt"
	"sort"
)

func reduceIntegrationRuntime(
	current WorkspaceRuntimeProjection,
	next *WorkspaceRuntimeProjection,
	record JournalRecord,
) error {
	if next == nil || current.workspaceID.IsZero() ||
		current.activeGeneration.IsZero() {
		return fmt.Errorf(
			"integration events require an initialized workspace runtime",
		)
	}
	if record.generation != current.activeGeneration {
		return fmt.Errorf("integration event generation is not active")
	}
	target, ready := current.LocalTarget()
	if !ready {
		return fmt.Errorf(
			"integration requires a durable local feature head",
		)
	}
	switch event := record.event.(type) {
	case MergeUnitIntegrationIntendedJournalEvent:
		intent := event.intent
		if err := intent.validate(); err != nil {
			return err
		}
		if intent.workspaceID != current.workspaceID ||
			intent.generation != current.activeGeneration ||
			intent.featureRef != target.binding.featureRef {
			return fmt.Errorf(
				"integration intent does not match the active workspace target",
			)
		}
		index, attempt, err := requireRuntimeAttempt(
			current, intent.attemptID, intent.workspaceID,
			intent.generation,
		)
		if err != nil {
			return err
		}
		if attempt.phase != AttemptActive ||
			attempt.mergeUnit != intent.mergeUnit ||
			attempt.base != intent.expectedFeatureHead ||
			attempt.verifiedHead != intent.acceptedHead ||
			target.createdHead != intent.expectedFeatureHead ||
			attempt.integration != nil {
			return fmt.Errorf(
				"integration intent does not match the active attempt and exact feature head",
			)
		}
		for _, other := range current.attempts {
			if other.attemptID != attempt.attemptID &&
				other.integration != nil &&
				!other.integration.Integrated() {
				return fmt.Errorf(
					"integration intent conflicts with pending attempt %s",
					other.attemptID,
				)
			}
		}
		next.attempts[index].integration =
			&RuntimeIntegrationProjection{
				intent:       intent,
				intentRecord: record.sequence,
			}
		return nil
	case MergeUnitIntegratedJournalEvent:
		index, attempt, err := requireRuntimeAttempt(
			current, event.attemptID, event.workspaceID,
			event.generation,
		)
		if err != nil {
			return err
		}
		if attempt.phase != AttemptActive ||
			attempt.integration == nil ||
			attempt.integration.integratedRecord != 0 {
			return fmt.Errorf(
				"integration completion requires one pending active intent",
			)
		}
		intent := attempt.integration.intent
		expectedSuperseded, err := integrationSupersededAttempts(
			current, attempt.attemptID,
		)
		if err != nil {
			return err
		}
		if !equalIntegrationSupersededAttempts(
			event.supersededAttempts, expectedSuperseded,
		) {
			return fmt.Errorf(
				"integration completion does not bind the exact superseded attempts",
			)
		}
		expectedSerialSegment := ID{}
		if attempt.serialSegmentHeld {
			expectedSerialSegment = attempt.serialSegment
		}
		if event.mergeUnit != attempt.mergeUnit ||
			event.intentDigest != intent.digest ||
			event.featureRef != intent.featureRef ||
			event.expectedFeatureHead != intent.expectedFeatureHead ||
			event.acceptedHead != intent.acceptedHead ||
			event.acceptedTree != intent.acceptedTree ||
			event.mergeCommit != intent.expectedMerge ||
			target.binding.featureRef != intent.featureRef ||
			target.createdHead != intent.expectedFeatureHead ||
			event.leaseID != attempt.leaseID ||
			event.serialSegment != expectedSerialSegment {
			return fmt.Errorf(
				"integration completion does not match its exact intent, feature frontier, lease, and serial segment",
			)
		}
		updated := &next.attempts[index]
		updated.phase = AttemptCompleted
		updated.serialSegmentHeld = false
		updated.leaseID = ID{}
		updated.integration.integratedRecord = record.sequence
		for _, superseded := range event.supersededAttempts {
			supersededIndex, exists := findRuntimeAttempt(
				next.attempts, superseded.attemptID,
			)
			if !exists {
				return fmt.Errorf(
					"integration completion superseded attempt %s is absent",
					superseded.attemptID,
				)
			}
			supersededAttempt := &next.attempts[supersededIndex]
			supersededAttempt.phase = AttemptSuperseded
			supersededAttempt.serialSegmentHeld = false
			supersededAttempt.leaseID = ID{}
		}
		next.localTarget.createdHead = intent.expectedMerge
		next.localTarget.headRecord = record.sequence
		return nil
	default:
		return fmt.Errorf(
			"unsupported integration runtime event %T", record.event,
		)
	}
}

func integrationSupersededAttempts(
	runtime WorkspaceRuntimeProjection,
	winner ID,
) ([]integrationSupersededAttempt, error) {
	result := make([]integrationSupersededAttempt, 0)
	for _, attempt := range runtime.attempts {
		if attempt.attemptID == winner ||
			!attempt.phase.nonterminal() {
			continue
		}
		if attempt.integration != nil {
			return nil, fmt.Errorf(
				"integration completion conflicts with pending attempt %s",
				attempt.attemptID,
			)
		}
		result = append(
			result,
			integrationSupersededAttempt{
				attemptID:         attempt.attemptID,
				mergeUnit:         attempt.mergeUnit,
				base:              attempt.base,
				phase:             attempt.phase,
				leaseID:           attempt.leaseID,
				serialSegment:     attempt.serialSegment,
				serialSegmentHeld: attempt.serialSegmentHeld,
			},
		)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].attemptID.String() <
			result[j].attemptID.String()
	})
	return result, nil
}

func equalIntegrationSupersededAttempts(
	left, right []integrationSupersededAttempt,
) bool {
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
