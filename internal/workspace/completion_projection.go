package workspace

import "fmt"

func reduceCompletionRuntime(
	current WorkspaceRuntimeProjection,
	next *WorkspaceRuntimeProjection,
	record JournalRecord,
	event WorkspaceCompletedJournalEvent,
) error {
	if next == nil || current.workspaceID.IsZero() ||
		current.activeGeneration.IsZero() {
		return fmt.Errorf(
			"workspace completion requires an initialized runtime",
		)
	}
	if current.completion != nil {
		return fmt.Errorf("workspace completion is already recorded")
	}
	if record.generation != current.activeGeneration ||
		event.workspaceID != current.workspaceID ||
		event.generation != current.activeGeneration {
		return fmt.Errorf(
			"workspace completion does not match the active generation",
		)
	}
	target, exists := current.LocalTarget()
	if !exists ||
		target.binding.featureRef != event.featureRef ||
		target.createdHead != event.featureHead {
		return fmt.Errorf(
			"workspace completion does not match the durable feature frontier",
		)
	}
	for _, attempt := range current.attempts {
		if attempt.phase.nonterminal() {
			return fmt.Errorf(
				"workspace completion has nonterminal attempt %s",
				attempt.attemptID,
			)
		}
		if !attempt.leaseID.IsZero() || attempt.serialSegmentHeld {
			return fmt.Errorf(
				"workspace completion has held resources for attempt %s",
				attempt.attemptID,
			)
		}
		if attempt.integration != nil &&
			!attempt.integration.Integrated() {
			return fmt.Errorf(
				"workspace completion has pending integration %s",
				attempt.attemptID,
			)
		}
	}
	next.completion = &RuntimeWorkspaceCompletionProjection{
		featureRef:   event.featureRef,
		featureHead:  event.featureHead,
		reportDigest: event.reportDigest,
		record:       record.sequence,
		eventDigest:  record.eventHash,
	}
	return nil
}
