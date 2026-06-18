package workspace

import "fmt"

type externalIntentSnapshot struct {
	ExternalIntentView
	ReservedEventID   string
	ReservedEventHash string
}

type externalIntentTracker struct {
	intents map[string]externalIntentSnapshot
}

func newExternalIntentTracker() *externalIntentTracker {
	return &externalIntentTracker{intents: map[string]externalIntentSnapshot{}}
}

func externalIntentSnapshots(events []JournalEvent) (map[string]externalIntentSnapshot, error) {
	tracker := newExternalIntentTracker()
	for _, event := range events {
		if err := tracker.Apply(event); err != nil {
			return nil, err
		}
	}
	return tracker.intents, nil
}

func (t *externalIntentTracker) Apply(event JournalEvent) error {
	switch event.Type {
	case EventExternalIntentReserved:
		intent, err := externalIntentReservedFromEvent(event)
		if err != nil {
			return err
		}
		if _, exists := t.intents[intent.IntentID]; exists {
			return fmt.Errorf("external intent event %s duplicates intent %s", event.ID, intent.IntentID)
		}
		t.intents[intent.IntentID] = intent
	case EventExternalIntentResultRecorded:
		result, err := externalIntentRecordedResultFromEvent(event)
		if err != nil {
			return err
		}
		intent, ok := t.intents[result.intentID]
		if !ok {
			return fmt.Errorf("external intent result event %s references unknown intent %s", event.ID, result.intentID)
		}
		if intent.Result != nil {
			return fmt.Errorf("external intent result event %s duplicates result for intent %s", event.ID, result.intentID)
		}
		if result.mergeUnitID != intent.MergeUnitID {
			return fmt.Errorf("external intent result event %s merge unit %s does not match intent %s", event.ID, result.mergeUnitID, intent.MergeUnitID)
		}
		if result.attemptID != intent.AttemptID {
			return fmt.Errorf("external intent result event %s attempt %s does not match intent %s", event.ID, result.attemptID, intent.AttemptID)
		}
		if result.agentID != intent.AgentID {
			return fmt.Errorf("external intent result event %s agent %s does not match intent %s", event.ID, result.agentID, intent.AgentID)
		}
		if result.leaseID != intent.LeaseID {
			return fmt.Errorf("external intent result event %s lease %s does not match intent %s", event.ID, result.leaseID, intent.LeaseID)
		}
		if result.action != intent.Action {
			return fmt.Errorf("external intent result event %s action %s does not match intent %s", event.ID, result.action, intent.Action)
		}
		if result.target != intent.Target {
			return fmt.Errorf("external intent result event %s target %s does not match intent %s", event.ID, result.target, intent.Target)
		}
		for _, resource := range intent.AffectedResources {
			if !containsString(event.WriteSet, resource) {
				return fmt.Errorf("external intent result event %s must write affected resource %s", event.ID, resource)
			}
		}
		intent.Result = &result.view
		intent.Status = result.view.Status
		t.intents[result.intentID] = intent
	}
	return nil
}

func (t *externalIntentTracker) Intent(id string) (externalIntentSnapshot, bool) {
	intent, ok := t.intents[id]
	return intent, ok
}

func validateExternalIntentCompletionEvidence(eventID string, evidence map[string]any, mergeUnitID string, attemptID string, externalIntents *externalIntentTracker) error {
	value, ok := evidence[evidenceExternalIntentIDKey]
	if !ok {
		return nil
	}
	intentID, ok := value.(string)
	if !ok || intentID == "" {
		return fmt.Errorf("transition event %s evidence %s must be a string", eventID, evidenceExternalIntentIDKey)
	}
	if externalIntents == nil {
		return fmt.Errorf("transition event %s cannot validate external intent %s", eventID, intentID)
	}
	intent, ok := externalIntents.Intent(intentID)
	if !ok {
		return fmt.Errorf("transition event %s references unknown external intent %s", eventID, intentID)
	}
	if intent.MergeUnitID != mergeUnitID {
		return fmt.Errorf("transition event %s external intent %s is for merge unit %s, not %s", eventID, intentID, intent.MergeUnitID, mergeUnitID)
	}
	if intent.AttemptID != attemptID {
		return fmt.Errorf("transition event %s external intent %s is for attempt %s, not %s", eventID, intentID, intent.AttemptID, attemptID)
	}
	if intent.Result == nil {
		return fmt.Errorf("transition event %s external intent %s has no recorded result", eventID, intentID)
	}
	if !intent.Result.Accepted {
		return fmt.Errorf("transition event %s external intent %s result %s is not accepted", eventID, intentID, intent.Result.Status)
	}
	return nil
}

func externalIntentReservedFromEvent(event JournalEvent) (externalIntentSnapshot, error) {
	intentID, err := eventStringPayload(event, eventPayloadIntentIDKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	idempotencyKey, err := eventStringPayload(event, eventPayloadIdempotencyKeyKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	action, err := eventStringPayload(event, eventPayloadActionKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	scope, err := eventStringPayload(event, eventPayloadScopeKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	target, err := eventStringPayload(event, eventPayloadTargetKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	approvalID, err := eventStringPayload(event, eventPayloadApprovalIDRefKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	requestedHeadSHA, err := eventStringPayload(event, eventPayloadRequestedHeadSHAKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	expectedBaseSHA, err := eventStringPayload(event, eventPayloadExpectedBaseSHAKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	affectedResources, err := eventStringSlicePayload(event, eventPayloadAffectedResourcesKey)
	if err != nil {
		return externalIntentSnapshot{}, err
	}
	if !containsString(event.WriteSet, ExternalIntentResource(intentID)) {
		return externalIntentSnapshot{}, fmt.Errorf("external intent event %s must write %s", event.ID, ExternalIntentResource(intentID))
	}
	return externalIntentSnapshot{
		ExternalIntentView: ExternalIntentView{
			IntentID:          intentID,
			IdempotencyKey:    idempotencyKey,
			MergeUnitID:       mergeUnitID,
			AttemptID:         attemptID,
			AgentID:           optionalStringPayload(event, eventPayloadAgentIDKey),
			LeaseID:           optionalStringPayload(event, eventPayloadLeaseIDKey),
			Action:            action,
			Scope:             scope,
			Target:            target,
			ApprovalID:        approvalID,
			Branch:            optionalStringPayload(event, eventPayloadBranchKey),
			PR:                normalizeApprovalPR(optionalStringPayload(event, eventPayloadPRKey)),
			RequestedHeadSHA:  requestedHeadSHA,
			ExpectedBaseSHA:   expectedBaseSHA,
			AffectedResources: affectedResources,
			Status:            "reserved",
		},
		ReservedEventID:   event.ID,
		ReservedEventHash: event.EventHash,
	}, nil
}

type externalIntentRecordedResultSnapshot struct {
	intentID    string
	mergeUnitID string
	attemptID   string
	agentID     string
	leaseID     string
	action      string
	target      string
	view        ExternalIntentRecordedResultView
}

func externalIntentRecordedResultFromEvent(event JournalEvent) (externalIntentRecordedResultSnapshot, error) {
	intentID, err := eventStringPayload(event, eventPayloadIntentIDKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	if !containsString(event.WriteSet, ExternalIntentResource(intentID)) {
		return externalIntentRecordedResultSnapshot{}, fmt.Errorf("external intent result event %s must write %s", event.ID, ExternalIntentResource(intentID))
	}
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	agentID, err := eventStringPayload(event, eventPayloadAgentIDKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	leaseID, err := eventStringPayload(event, eventPayloadLeaseIDKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	action, err := eventStringPayload(event, eventPayloadActionKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	target, err := eventStringPayload(event, eventPayloadTargetKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	status, err := eventStringPayload(event, eventPayloadStatusKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	normalizedStatus, err := normalizeExternalIntentResultStatus(status)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	policyAccepted, err := optionalBoolPayload(event, eventPayloadPolicyAcceptedKey)
	if err != nil {
		return externalIntentRecordedResultSnapshot{}, err
	}
	return externalIntentRecordedResultSnapshot{
		intentID:    intentID,
		mergeUnitID: mergeUnitID,
		attemptID:   attemptID,
		agentID:     agentID,
		leaseID:     leaseID,
		action:      action,
		target:      target,
		view: externalIntentRecordedResultView(
			normalizedStatus,
			policyAccepted,
			optionalStringPayload(event, eventPayloadDetailsKey),
			event.ID,
			event.EventHash,
		),
	}, nil
}

func optionalBoolPayload(event JournalEvent, key string) (bool, error) {
	value, ok := event.Payload[key]
	if !ok {
		return false, nil
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("scheduler event %s payload %s must be a bool", event.ID, key)
	}
	return result, nil
}
