package workspace

import (
	"fmt"
	"sort"
	"strings"
)

const (
	externalIntentFreezeStatusUnresolved = "unresolved"
	externalIntentFreezeStatusAmbiguous  = "ambiguous"

	externalIntentFreezeActionRecordResult      = "record_result"
	externalIntentFreezeActionOperatorReconcile = "operator_reconcile"
)

type externalIntentSnapshot struct {
	ExternalIntentView
	ReservedEventID   string
	ReservedEventHash string
}

type externalIntentTracker struct {
	intents map[string]externalIntentSnapshot
}

type ExternalIntentFreezeView struct {
	Resource       string `json:"resource"`
	IntentID       string `json:"intent_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	AttemptID      string `json:"attempt_id"`
	Action         string `json:"action"`
	Target         string `json:"target"`
	Status         string `json:"status"`
	RequiredAction string `json:"required_action"`
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
	case EventExternalIntentReconciled:
		reconciliation, err := externalIntentReconciliationFromEvent(event)
		if err != nil {
			return err
		}
		intent, ok := t.intents[reconciliation.intentID]
		if !ok {
			return fmt.Errorf("external intent reconciliation event %s references unknown intent %s", event.ID, reconciliation.intentID)
		}
		if intent.Result != nil && intent.Result.Status != ExternalResultAmbiguous {
			return fmt.Errorf("external intent reconciliation event %s references non-ambiguous intent %s result %s", event.ID, reconciliation.intentID, intent.Result.Status)
		}
		if reconciliation.mergeUnitID != intent.MergeUnitID {
			return fmt.Errorf("external intent reconciliation event %s merge unit %s does not match intent %s", event.ID, reconciliation.mergeUnitID, intent.MergeUnitID)
		}
		if reconciliation.attemptID != intent.AttemptID {
			return fmt.Errorf("external intent reconciliation event %s attempt %s does not match intent %s", event.ID, reconciliation.attemptID, intent.AttemptID)
		}
		if reconciliation.action != intent.Action {
			return fmt.Errorf("external intent reconciliation event %s action %s does not match intent %s", event.ID, reconciliation.action, intent.Action)
		}
		if reconciliation.target != intent.Target {
			return fmt.Errorf("external intent reconciliation event %s target %s does not match intent %s", event.ID, reconciliation.target, intent.Target)
		}
		for _, resource := range intent.AffectedResources {
			if !containsString(event.WriteSet, resource) {
				return fmt.Errorf("external intent reconciliation event %s must write affected resource %s", event.ID, resource)
			}
		}
		intent.Result = &reconciliation.view
		intent.Status = reconciliation.view.Status
		t.intents[reconciliation.intentID] = intent
	}
	return nil
}

func (t *externalIntentTracker) Intent(id string) (externalIntentSnapshot, bool) {
	intent, ok := t.intents[id]
	return intent, ok
}

func (t *externalIntentTracker) Freezes(activeLeases map[string]activeLeaseSnapshot) []ExternalIntentFreezeView {
	freezes := []ExternalIntentFreezeView{}
	for _, intent := range t.intents {
		status, requiredAction, frozen := intent.freezeState(activeLeases)
		if !frozen {
			continue
		}
		for _, resource := range intent.AffectedResources {
			freezes = append(freezes, ExternalIntentFreezeView{
				Resource:       resource,
				IntentID:       intent.IntentID,
				MergeUnitID:    intent.MergeUnitID,
				AttemptID:      intent.AttemptID,
				Action:         intent.Action,
				Target:         intent.Target,
				Status:         status,
				RequiredAction: requiredAction,
			})
		}
	}
	sort.Slice(freezes, func(i, j int) bool {
		if freezes[i].Resource != freezes[j].Resource {
			return freezes[i].Resource < freezes[j].Resource
		}
		return freezes[i].IntentID < freezes[j].IntentID
	})
	return freezes
}

func (intent externalIntentSnapshot) freezeState(activeLeases map[string]activeLeaseSnapshot) (string, string, bool) {
	if intent.Result == nil {
		if lease, ok := activeLeases[intent.MergeUnitID]; ok && lease.LeaseID == intent.LeaseID {
			return externalIntentFreezeStatusUnresolved, externalIntentFreezeActionRecordResult, true
		}
		return externalIntentFreezeStatusUnresolved, externalIntentFreezeActionOperatorReconcile, true
	}
	if intent.Result.Status == ExternalResultAmbiguous {
		return externalIntentFreezeStatusAmbiguous, externalIntentFreezeActionOperatorReconcile, true
	}
	return "", "", false
}

func externalIntentFreezes(events []JournalEvent, activeLeases map[string]activeLeaseSnapshot) ([]ExternalIntentFreezeView, error) {
	tracker := newExternalIntentTracker()
	for _, event := range events {
		if err := tracker.Apply(event); err != nil {
			return nil, err
		}
	}
	return tracker.Freezes(activeLeases), nil
}

func validateResourcesNotFrozen(events []JournalEvent, activeLeases map[string]activeLeaseSnapshot, resources []string, operation string) error {
	freezes, err := externalIntentFreezes(events, activeLeases)
	if err != nil {
		return err
	}
	freezeByResource := externalIntentFreezesByResource(freezes)
	for _, resource := range resources {
		if frozen := freezeByResource[resource]; len(frozen) > 0 {
			first := frozen[0]
			return fmt.Errorf("%s blocked by frozen resource %s from external intent %s (%s; requires %s)", operation, first.Resource, first.IntentID, first.Status, first.RequiredAction)
		}
	}
	return nil
}

func externalIntentFreezesByResource(freezes []ExternalIntentFreezeView) map[string][]ExternalIntentFreezeView {
	byResource := map[string][]ExternalIntentFreezeView{}
	for _, freeze := range freezes {
		byResource[freeze.Resource] = append(byResource[freeze.Resource], freeze)
	}
	return byResource
}

func validateExternalIntentCompletionEvidence(eventID string, evidence map[string]any, mergeUnitID string, attemptID string, externalIntents *externalIntentTracker) error {
	intentIDs, listProvided, err := externalIntentCompletionEvidenceIDs(eventID, evidence)
	if err != nil {
		return err
	}
	if externalIntents == nil {
		return fmt.Errorf("transition event %s cannot validate external intents", eventID)
	}
	required := requiredCompletionExternalIntents(externalIntents, mergeUnitID, attemptID)
	if len(intentIDs) == 0 {
		if len(required) == 0 {
			return nil
		}
		return fmt.Errorf("transition event %s missing required external intent %s for action %s", eventID, required[0].IntentID, required[0].Action)
	}
	if !listProvided && len(required) > 1 {
		return fmt.Errorf("transition event %s evidence %s cannot cover %d required external intents; use %s", eventID, evidenceExternalIntentIDKey, len(required), evidenceExternalIntentIDsKey)
	}
	included := map[string]bool{}
	for _, intentID := range intentIDs {
		included[intentID] = true
		if err := validateExternalIntentCompletionID(eventID, intentID, mergeUnitID, attemptID, listProvided, externalIntents); err != nil {
			return err
		}
	}
	for _, intent := range required {
		if !included[intent.IntentID] {
			return fmt.Errorf("transition event %s missing required external intent %s for action %s", eventID, intent.IntentID, intent.Action)
		}
	}
	return nil
}

func requiredCompletionExternalIntents(externalIntents *externalIntentTracker, mergeUnitID string, attemptID string) []externalIntentSnapshot {
	required := []externalIntentSnapshot{}
	for _, intent := range externalIntents.intents {
		if intent.MergeUnitID != mergeUnitID || intent.AttemptID != attemptID || !isCompletionExternalIntentAction(intent.Action) {
			continue
		}
		required = append(required, intent)
	}
	sort.Slice(required, func(i, j int) bool {
		if required[i].Action != required[j].Action {
			return required[i].Action < required[j].Action
		}
		return required[i].IntentID < required[j].IntentID
	})
	return required
}

func externalIntentCompletionEvidenceIDs(eventID string, evidence map[string]any) ([]string, bool, error) {
	_, hasSingular := evidence[evidenceExternalIntentIDKey]
	_, hasList := evidence[evidenceExternalIntentIDsKey]
	if hasSingular && hasList {
		return nil, false, fmt.Errorf("transition event %s evidence must use %s or %s, not both", eventID, evidenceExternalIntentIDKey, evidenceExternalIntentIDsKey)
	}
	if hasSingular {
		value := evidence[evidenceExternalIntentIDKey]
		intentID, ok := value.(string)
		if !ok || strings.TrimSpace(intentID) == "" {
			return nil, false, fmt.Errorf("transition event %s evidence %s must be a string", eventID, evidenceExternalIntentIDKey)
		}
		return []string{strings.TrimSpace(intentID)}, false, nil
	}
	if !hasList {
		return nil, false, nil
	}
	intentIDs, err := normalizeExternalIntentIDList(evidence[evidenceExternalIntentIDsKey], evidenceExternalIntentIDsKey)
	if err != nil {
		return nil, false, fmt.Errorf("transition event %s %w", eventID, err)
	}
	return intentIDs, true, nil
}

func validateExternalIntentCompletionID(eventID string, intentID string, mergeUnitID string, attemptID string, listProvided bool, externalIntents *externalIntentTracker) error {
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
	if listProvided && !isCompletionExternalIntentAction(intent.Action) {
		return fmt.Errorf("transition event %s external intent %s action %s is cleanup-only, not required for completion", eventID, intentID, intent.Action)
	}
	if intent.Result == nil {
		return fmt.Errorf("transition event %s external intent %s has no recorded result", eventID, intentID)
	}
	if intent.Result.Status == ExternalResultAmbiguous {
		return fmt.Errorf("transition event %s external intent %s result is ambiguous", eventID, intentID)
	}
	if !intent.Result.Accepted {
		return fmt.Errorf("transition event %s external intent %s result %s is not accepted", eventID, intentID, intent.Result.Status)
	}
	return nil
}

func isCompletionExternalIntentAction(action string) bool {
	switch action {
	case ExternalActionPush, ExternalActionOpenPR, ExternalActionMerge:
		return true
	default:
		return false
	}
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
	queueID := optionalStringPayload(event, eventPayloadQueueIDKey)
	queuePosition := 0
	if queueID != "" {
		queuePosition, err = eventIntPayload(event, eventPayloadQueuePositionKey)
		if err != nil {
			return externalIntentSnapshot{}, err
		}
		if queuePosition <= 0 {
			return externalIntentSnapshot{}, fmt.Errorf("external intent event %s payload %s must be positive", event.ID, eventPayloadQueuePositionKey)
		}
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
			QueueID:           queueID,
			QueuePosition:     queuePosition,
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
	normalizedStatus, err := normalizeExternalIntentRecordedEventStatus(status)
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
			"",
			event.ID,
			event.EventHash,
		),
	}, nil
}

func normalizeExternalIntentRecordedEventStatus(value string) (string, error) {
	status := strings.TrimSpace(strings.ToLower(value))
	switch status {
	case ExternalResultSucceeded,
		ExternalResultNotPerformed,
		ExternalResultFailedBeforeSideEffect,
		ExternalResultFailedAfterSideEffect,
		ExternalResultAmbiguous,
		ExternalResultReconciledByOperator:
		return status, nil
	default:
		return "", fmt.Errorf("unsupported external intent result status: %s", value)
	}
}

type externalIntentReconciliationSnapshot struct {
	intentID    string
	mergeUnitID string
	attemptID   string
	operator    string
	action      string
	target      string
	view        ExternalIntentRecordedResultView
}

func externalIntentReconciliationFromEvent(event JournalEvent) (externalIntentReconciliationSnapshot, error) {
	intentID, err := eventStringPayload(event, eventPayloadIntentIDKey)
	if err != nil {
		return externalIntentReconciliationSnapshot{}, err
	}
	if !containsString(event.WriteSet, ExternalIntentResource(intentID)) {
		return externalIntentReconciliationSnapshot{}, fmt.Errorf("external intent reconciliation event %s must write %s", event.ID, ExternalIntentResource(intentID))
	}
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return externalIntentReconciliationSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return externalIntentReconciliationSnapshot{}, err
	}
	operator, err := eventStringPayload(event, eventPayloadOperatorKey)
	if err != nil {
		return externalIntentReconciliationSnapshot{}, err
	}
	action, err := eventStringPayload(event, eventPayloadActionKey)
	if err != nil {
		return externalIntentReconciliationSnapshot{}, err
	}
	target, err := eventStringPayload(event, eventPayloadTargetKey)
	if err != nil {
		return externalIntentReconciliationSnapshot{}, err
	}
	return externalIntentReconciliationSnapshot{
		intentID:    intentID,
		mergeUnitID: mergeUnitID,
		attemptID:   attemptID,
		operator:    operator,
		action:      action,
		target:      target,
		view: externalIntentRecordedResultView(
			ExternalResultReconciledByOperator,
			true,
			optionalStringPayload(event, eventPayloadDetailsKey),
			operator,
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
