package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	EventExternalIntentReserved       = "external_intent.reserved"
	EventExternalIntentResultRecorded = "external_intent.result_recorded"
	EventExternalIntentReconciled     = "external_intent.reconciled"

	eventPayloadIntentIDKey          = "intent_id"
	eventPayloadIdempotencyKeyKey    = "idempotency_key"
	eventPayloadActionKey            = "action"
	eventPayloadTargetKey            = "target"
	eventPayloadOperatorKey          = "operator"
	eventPayloadApprovalIDRefKey     = "approval_id"
	eventPayloadRequestedHeadSHAKey  = "requested_head_sha"
	eventPayloadExpectedBaseSHAKey   = "expected_base_sha"
	eventPayloadAffectedResourcesKey = "affected_resources"
	eventPayloadPolicyAcceptedKey    = "policy_accepted"
	eventPayloadDetailsKey           = "details"

	ExternalActionPush         = "push"
	ExternalActionOpenPR       = "open-pr"
	ExternalActionMerge        = "merge"
	ExternalActionRemoteDelete = "remote-delete"

	ExternalResultSucceeded              = "succeeded"
	ExternalResultNotPerformed           = "not_performed"
	ExternalResultFailedBeforeSideEffect = "failed_before_side_effect"
	ExternalResultFailedAfterSideEffect  = "failed_after_side_effect"
	ExternalResultAmbiguous              = "ambiguous"
	ExternalResultReconciledByOperator   = "reconciled_by_operator"
)

type ExternalIntentReserveOptions struct {
	WorkspaceDir     string
	MergeUnitID      string
	AttemptID        string
	AgentID          string
	LeaseID          string
	ApprovalID       string
	Action           string
	Scope            string
	Branch           string
	PR               string
	RequestedHeadSHA string
	ExpectedBaseSHA  string
	Now              func() time.Time
}

type ExternalIntentResultRecordOptions struct {
	WorkspaceDir   string
	MergeUnitID    string
	AttemptID      string
	AgentID        string
	LeaseID        string
	IntentID       string
	Status         string
	PolicyAccepted bool
	Details        string
	Now            func() time.Time
}

type ExternalIntentReconcileOptions struct {
	WorkspaceDir string
	IntentID     string
	Operator     string
	Details      string
	Now          func() time.Time
}

type ExternalIntentResult struct {
	Status       string             `json:"status"`
	WorkspaceDir string             `json:"workspace_dir"`
	WorkspaceID  string             `json:"workspace_id"`
	BaseRef      string             `json:"base_ref"`
	Intent       ExternalIntentView `json:"intent"`
	EventID      string             `json:"event_id,omitempty"`
	EventHash    string             `json:"event_hash,omitempty"`
}

type ExternalIntentResultRecordResult struct {
	Status       string                           `json:"status"`
	WorkspaceDir string                           `json:"workspace_dir"`
	WorkspaceID  string                           `json:"workspace_id"`
	BaseRef      string                           `json:"base_ref"`
	Intent       ExternalIntentView               `json:"intent"`
	Result       ExternalIntentRecordedResultView `json:"result"`
	EventID      string                           `json:"event_id,omitempty"`
	EventHash    string                           `json:"event_hash,omitempty"`
}

type ExternalIntentReconcileResult struct {
	Status       string                           `json:"status"`
	WorkspaceDir string                           `json:"workspace_dir"`
	WorkspaceID  string                           `json:"workspace_id"`
	BaseRef      string                           `json:"base_ref"`
	Intent       ExternalIntentView               `json:"intent"`
	Result       ExternalIntentRecordedResultView `json:"result"`
	EventID      string                           `json:"event_id,omitempty"`
	EventHash    string                           `json:"event_hash,omitempty"`
}

type ExternalIntentView struct {
	IntentID          string                            `json:"intent_id"`
	IdempotencyKey    string                            `json:"idempotency_key"`
	MergeUnitID       string                            `json:"merge_unit_id"`
	AttemptID         string                            `json:"attempt_id"`
	AgentID           string                            `json:"agent_id,omitempty"`
	LeaseID           string                            `json:"lease_id,omitempty"`
	Action            string                            `json:"action"`
	Scope             string                            `json:"scope"`
	Target            string                            `json:"target"`
	ApprovalID        string                            `json:"approval_id"`
	Branch            string                            `json:"branch,omitempty"`
	PR                string                            `json:"pr,omitempty"`
	RequestedHeadSHA  string                            `json:"requested_head_sha"`
	ExpectedBaseSHA   string                            `json:"expected_base_sha"`
	QueueID           string                            `json:"queue_id,omitempty"`
	QueuePosition     int                               `json:"queue_position,omitempty"`
	AffectedResources []string                          `json:"affected_resources"`
	Status            string                            `json:"status"`
	Result            *ExternalIntentRecordedResultView `json:"result,omitempty"`
}

type mergeIntentQueueReservation struct {
	entry   mergeQueueSnapshot
	view    MergeQueueEntryView
	readSet map[string]int
}

type ExternalIntentRecordedResultView struct {
	Status         string `json:"status"`
	PolicyAccepted bool   `json:"policy_accepted"`
	Accepted       bool   `json:"accepted"`
	Details        string `json:"details,omitempty"`
	Operator       string `json:"operator,omitempty"`
	EventID        string `json:"event_id,omitempty"`
	EventHash      string `json:"event_hash,omitempty"`
}

func ExternalIntentResource(id string) string {
	return resourceKey("external_intent", id)
}

func ProviderTargetResource(target string) string {
	return resourceKey("provider_target", target)
}

func RemoteRefResource(branch string) string {
	return resourceKey("remote_ref", branch)
}

func QueueSlotResource(id string) string {
	return resourceKey("queue_slot", id)
}

func ReserveExternalIntent(opts ExternalIntentReserveOptions) (ExternalIntentResult, error) {
	opts, reservedAt, target, err := normalizeExternalIntentReserveOptions(opts)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, reservedAt)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	reservedAt = state.ObservedAt
	lease, unit, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return ExternalIntentResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, reservedAt)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return ExternalIntentResult{}, err
	}
	approvals, err := approvalSnapshots(state.Events)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	approval, ok := approvals[opts.ApprovalID]
	if !ok {
		return ExternalIntentResult{}, fmt.Errorf("approval not found: %s", opts.ApprovalID)
	}
	identity := deriveExternalIntentIdentity(state.View.WorkspaceID, opts, target)
	intentResource := ExternalIntentResource(identity.intentID)
	if observed := state.Revisions[intentResource]; observed != 0 {
		return ExternalIntentResult{}, StaleResourceError{Resource: intentResource, Expected: 0, Observed: observed}
	}
	if err := approvalMatches(approval, approvalMatchRequest{
		mergeUnitID: opts.MergeUnitID,
		attemptID:   opts.AttemptID,
		action:      opts.Action,
		scope:       opts.Scope,
		pr:          opts.PR,
		branch:      opts.Branch,
		headSHA:     opts.RequestedHeadSHA,
		baseSHA:     opts.ExpectedBaseSHA,
		now:         reservedAt,
	}); err != nil {
		return ExternalIntentResult{}, err
	}
	if staleInputs := approvalStaleInputsFromEvents(state.Events, approval); len(staleInputs) > 0 {
		return ExternalIntentResult{}, fmt.Errorf("approval %s is stale after refresh changed %s", approval.ApprovalID, strings.Join(staleInputs, ", "))
	}
	approvalResource := ApprovalResource(opts.ApprovalID)
	approvalAttemptResource := ApprovalAttemptResource(opts.MergeUnitID, opts.AttemptID)
	affectedResources := externalIntentAffectedResources(opts, target, state.View.BaseRef)
	if err := validateResourcesNotFrozen(state.Events, state.ActiveLeases, affectedResources, "external intent reserve"); err != nil {
		return ExternalIntentResult{}, err
	}
	queueReservation, err := validateMergeIntentQueueReservation(state, unit, current, opts)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	readSet := map[string]int{
		LeaseResource(opts.MergeUnitID):     state.Revisions[LeaseResource(opts.MergeUnitID)],
		MergeUnitResource(opts.MergeUnitID): state.Revisions[MergeUnitResource(opts.MergeUnitID)],
		approvalResource:                    state.Revisions[approvalResource],
		approvalAttemptResource:             state.Revisions[approvalAttemptResource],
		intentResource:                      0,
	}
	addApprovalRefreshInputReadSet(readSet, state.Revisions, approval)
	writeSet := []string{intentResource, approvalResource, approvalAttemptResource}
	for _, resource := range affectedResources {
		readSet[resource] = state.Revisions[resource]
		writeSet = append(writeSet, resource)
	}
	if queueReservation != nil {
		for resource, revision := range queueReservation.readSet {
			readSet[resource] = revision
		}
		writeSet = append(writeSet, MergeQueueResource(), QueueSlotResource(queueReservation.entry.QueueID))
	}
	payload := map[string]any{
		eventPayloadIntentIDKey:          identity.intentID,
		eventPayloadIdempotencyKeyKey:    identity.idempotencyKey,
		eventPayloadMergeUnitIDKey:       opts.MergeUnitID,
		eventPayloadAttemptIDKey:         opts.AttemptID,
		eventPayloadAgentIDKey:           opts.AgentID,
		eventPayloadLeaseIDKey:           opts.LeaseID,
		eventPayloadApprovalIDRefKey:     opts.ApprovalID,
		eventPayloadActionKey:            opts.Action,
		eventPayloadScopeKey:             opts.Scope,
		eventPayloadTargetKey:            target,
		eventPayloadBranchKey:            opts.Branch,
		eventPayloadPRKey:                opts.PR,
		eventPayloadRequestedHeadSHAKey:  opts.RequestedHeadSHA,
		eventPayloadExpectedBaseSHAKey:   opts.ExpectedBaseSHA,
		eventPayloadAffectedResourcesKey: affectedResources,
		eventPayloadUsedCountKey:         approval.UsedCount + 1,
	}
	if queueReservation != nil {
		payload[eventPayloadQueueIDKey] = queueReservation.entry.QueueID
		payload[eventPayloadQueuePositionKey] = queueReservation.view.Position
		payload[eventPayloadQueueReasonKey] = mergeQueueExitReasonReserved
		payload[eventPayloadGateInputHashKey] = queueReservation.entry.GateInputHash
		payload[eventPayloadGateOutputHashKey] = queueReservation.entry.GateOutputHash
	}
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventExternalIntentReserved,
		Payload:      payload,
		ReadSet:      readSet,
		WriteSet:     writeSet,
		Now:          func() time.Time { return reservedAt },
	})
	if err != nil {
		return ExternalIntentResult{}, err
	}
	return ExternalIntentResult{
		Status:       "reserved",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		Intent:       externalIntentView(opts, identity, target, affectedResources, queueReservation),
		EventID:      event.ID,
		EventHash:    event.EventHash,
	}, nil
}

func validateMergeIntentQueueReservation(state leaseOperationState, unit SchedulerMergeUnitView, attempt attemptSnapshot, opts ExternalIntentReserveOptions) (*mergeIntentQueueReservation, error) {
	if opts.Action != ExternalActionMerge {
		return nil, nil
	}
	var live *MergeQueueEntryView
	for i := range state.View.MergeQueue {
		entry := &state.View.MergeQueue[i]
		if entry.MergeUnitID == opts.MergeUnitID && entry.AttemptID == opts.AttemptID {
			live = entry
			break
		}
	}
	if live == nil {
		entry, ok, err := mergeQueueEntryForAttempt(state.Events, opts.MergeUnitID, opts.AttemptID)
		if err != nil {
			return nil, err
		}
		if ok {
			readSet := mergeQueueReadSet(state.Revisions, state.Events, attempt, unit, entry)
			if _, err := appendMergeQueueStaleEvent(opts.WorkspaceDir, entry, readSet, mergeQueueStaleReasonNotLive, state.ObservedAt); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("merge intent reserve requires merge unit %s attempt %s to be in the live merge queue", opts.MergeUnitID, opts.AttemptID)
	}
	if live.Position != 1 {
		return nil, fmt.Errorf("merge intent reserve requires merge unit %s attempt %s at queue position 1, observed position %d", opts.MergeUnitID, opts.AttemptID, live.Position)
	}
	if live.ApprovalID != opts.ApprovalID {
		return nil, fmt.Errorf("merge intent reserve approval %s does not match queued approval %s", opts.ApprovalID, live.ApprovalID)
	}
	if live.Scope != opts.Scope {
		return nil, fmt.Errorf("merge intent reserve scope %s does not match queued scope %s", opts.Scope, live.Scope)
	}
	if live.PR != opts.PR {
		return nil, fmt.Errorf("merge intent reserve PR %s does not match queued PR %s", opts.PR, live.PR)
	}
	if live.Branch != opts.Branch {
		return nil, fmt.Errorf("merge intent reserve branch %s does not match queued branch %s", opts.Branch, live.Branch)
	}
	if live.HeadSHA != opts.RequestedHeadSHA {
		return nil, fmt.Errorf("merge intent reserve head %s does not match queued head %s", opts.RequestedHeadSHA, live.HeadSHA)
	}
	if live.BaseSHA != opts.ExpectedBaseSHA {
		return nil, fmt.Errorf("merge intent reserve base %s does not match queued base %s", opts.ExpectedBaseSHA, live.BaseSHA)
	}
	entry, err := mergeQueueSnapshotFromView(*live)
	if err != nil {
		return nil, err
	}
	readSet := mergeQueueReadSet(state.Revisions, state.Events, attempt, unit, entry)
	return &mergeIntentQueueReservation{entry: entry, view: *live, readSet: readSet}, nil
}

func mergeQueueEntryForAttempt(events []JournalEvent, mergeUnitID string, attemptID string) (mergeQueueSnapshot, bool, error) {
	tracker := newMergeQueueTracker()
	for _, event := range events {
		if err := tracker.Apply(event); err != nil {
			return mergeQueueSnapshot{}, false, err
		}
	}
	entry, ok := tracker.entries[mergeQueueAttemptKey(mergeUnitID, attemptID)]
	return entry, ok, nil
}

func mergeQueueSnapshotFromView(view MergeQueueEntryView) (mergeQueueSnapshot, error) {
	queuedAt, err := time.Parse(time.RFC3339Nano, view.QueuedAt)
	if err != nil {
		return mergeQueueSnapshot{}, fmt.Errorf("merge queue entry %s queued_at must be RFC3339: %w", view.QueueID, err)
	}
	return mergeQueueSnapshot{
		QueueID:        view.QueueID,
		MergeUnitID:    view.MergeUnitID,
		AttemptID:      view.AttemptID,
		AgentID:        view.AgentID,
		LeaseID:        view.LeaseID,
		ApprovalID:     view.ApprovalID,
		Scope:          view.Scope,
		PR:             view.PR,
		Branch:         view.Branch,
		HeadSHA:        view.HeadSHA,
		BaseSHA:        view.BaseSHA,
		GateInputHash:  view.GateInputHash,
		GateOutputHash: view.GateOutputHash,
		Position:       view.Position,
		QueuedAt:       queuedAt,
	}, nil
}

func RecordExternalIntentResult(opts ExternalIntentResultRecordOptions) (ExternalIntentResultRecordResult, error) {
	opts, recordedAt, err := normalizeExternalIntentResultRecordOptions(opts)
	if err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, recordedAt)
	if err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	recordedAt = state.ObservedAt
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return ExternalIntentResultRecordResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, recordedAt)
	if err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	intents, err := externalIntentSnapshots(state.Events)
	if err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	intent, ok := intents[opts.IntentID]
	if !ok {
		return ExternalIntentResultRecordResult{}, fmt.Errorf("external intent not found: %s", opts.IntentID)
	}
	if intent.MergeUnitID != opts.MergeUnitID {
		return ExternalIntentResultRecordResult{}, fmt.Errorf("external intent %s is for merge unit %s, not %s", opts.IntentID, intent.MergeUnitID, opts.MergeUnitID)
	}
	if intent.AttemptID != opts.AttemptID {
		return ExternalIntentResultRecordResult{}, fmt.Errorf("external intent %s is for attempt %s, not %s", opts.IntentID, intent.AttemptID, opts.AttemptID)
	}
	if intent.Result != nil {
		return ExternalIntentResultRecordResult{}, fmt.Errorf("external intent %s already has result %s", opts.IntentID, intent.Result.Status)
	}
	intentResource := ExternalIntentResource(opts.IntentID)
	readSet := map[string]int{
		LeaseResource(opts.MergeUnitID):     state.Revisions[LeaseResource(opts.MergeUnitID)],
		MergeUnitResource(opts.MergeUnitID): state.Revisions[MergeUnitResource(opts.MergeUnitID)],
		intentResource:                      state.Revisions[intentResource],
	}
	writeSet := []string{intentResource}
	for _, resource := range intent.AffectedResources {
		readSet[resource] = state.Revisions[resource]
		writeSet = append(writeSet, resource)
	}
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventExternalIntentResultRecorded,
		Payload: map[string]any{
			eventPayloadIntentIDKey:       opts.IntentID,
			eventPayloadMergeUnitIDKey:    opts.MergeUnitID,
			eventPayloadAttemptIDKey:      opts.AttemptID,
			eventPayloadAgentIDKey:        opts.AgentID,
			eventPayloadLeaseIDKey:        opts.LeaseID,
			eventPayloadActionKey:         intent.Action,
			eventPayloadTargetKey:         intent.Target,
			eventPayloadStatusKey:         opts.Status,
			eventPayloadPolicyAcceptedKey: opts.PolicyAccepted,
			eventPayloadDetailsKey:        opts.Details,
		},
		ReadSet:  readSet,
		WriteSet: writeSet,
		Now:      func() time.Time { return recordedAt },
	})
	if err != nil {
		return ExternalIntentResultRecordResult{}, err
	}
	recorded := externalIntentRecordedResultView(opts.Status, opts.PolicyAccepted, opts.Details, "", event.ID, event.EventHash)
	intent.Result = &recorded
	intent.Status = recorded.Status
	return ExternalIntentResultRecordResult{
		Status:       "recorded",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		Intent:       intent.ExternalIntentView,
		Result:       recorded,
		EventID:      event.ID,
		EventHash:    event.EventHash,
	}, nil
}

func ReconcileExternalIntent(opts ExternalIntentReconcileOptions) (ExternalIntentReconcileResult, error) {
	opts, reconciledAt, err := normalizeExternalIntentReconcileOptions(opts)
	if err != nil {
		return ExternalIntentReconcileResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, reconciledAt)
	if err != nil {
		return ExternalIntentReconcileResult{}, err
	}
	reconciledAt = state.ObservedAt
	intents, err := externalIntentSnapshots(state.Events)
	if err != nil {
		return ExternalIntentReconcileResult{}, err
	}
	intent, ok := intents[opts.IntentID]
	if !ok {
		return ExternalIntentReconcileResult{}, fmt.Errorf("external intent not found: %s", opts.IntentID)
	}
	if intent.Result == nil {
		if lease, ok := state.ActiveLeases[intent.MergeUnitID]; ok && lease.LeaseID == intent.LeaseID {
			return ExternalIntentReconcileResult{}, fmt.Errorf("external intent %s has no recorded result while lease %s is active", opts.IntentID, intent.LeaseID)
		}
	} else if intent.Result.Status != ExternalResultAmbiguous {
		return ExternalIntentReconcileResult{}, fmt.Errorf("external intent %s result %s does not require reconciliation", opts.IntentID, intent.Result.Status)
	}
	intentResource := ExternalIntentResource(opts.IntentID)
	readSet := map[string]int{
		intentResource: state.Revisions[intentResource],
	}
	writeSet := []string{intentResource}
	for _, resource := range intent.AffectedResources {
		readSet[resource] = state.Revisions[resource]
		writeSet = append(writeSet, resource)
	}
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventExternalIntentReconciled,
		Payload: map[string]any{
			eventPayloadIntentIDKey:    opts.IntentID,
			eventPayloadMergeUnitIDKey: intent.MergeUnitID,
			eventPayloadAttemptIDKey:   intent.AttemptID,
			eventPayloadOperatorKey:    opts.Operator,
			eventPayloadActionKey:      intent.Action,
			eventPayloadTargetKey:      intent.Target,
			eventPayloadDetailsKey:     opts.Details,
		},
		ReadSet:  readSet,
		WriteSet: writeSet,
		Now:      func() time.Time { return reconciledAt },
	})
	if err != nil {
		return ExternalIntentReconcileResult{}, err
	}
	reconciled := externalIntentRecordedResultView(ExternalResultReconciledByOperator, true, opts.Details, opts.Operator, event.ID, event.EventHash)
	intent.Result = &reconciled
	intent.Status = reconciled.Status
	return ExternalIntentReconcileResult{
		Status:       "reconciled",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		Intent:       intent.ExternalIntentView,
		Result:       reconciled,
		EventID:      event.ID,
		EventHash:    event.EventHash,
	}, nil
}

func normalizeExternalIntentReserveOptions(opts ExternalIntentReserveOptions) (ExternalIntentReserveOptions, time.Time, string, error) {
	if opts.WorkspaceDir == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --lease")
	}
	opts.ApprovalID = strings.TrimSpace(opts.ApprovalID)
	if opts.ApprovalID == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --approval")
	}
	opts.Action = strings.TrimSpace(strings.ToLower(opts.Action))
	if opts.Action == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --action")
	}
	opts.Scope = normalizeApprovalScope(opts.Scope)
	opts.Branch = strings.TrimSpace(opts.Branch)
	opts.PR = normalizeApprovalPR(opts.PR)
	opts.RequestedHeadSHA = strings.TrimSpace(opts.RequestedHeadSHA)
	if opts.RequestedHeadSHA == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --head-sha")
	}
	opts.ExpectedBaseSHA = strings.TrimSpace(opts.ExpectedBaseSHA)
	if opts.ExpectedBaseSHA == "" {
		return ExternalIntentReserveOptions{}, time.Time{}, "", fmt.Errorf("workspace external intent reserve requires --base-sha")
	}
	target, err := externalIntentTarget(opts.Action, opts.Branch, opts.PR)
	if err != nil {
		return ExternalIntentReserveOptions{}, time.Time{}, "", err
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), target, nil
}

func normalizeExternalIntentResultRecordOptions(opts ExternalIntentResultRecordOptions) (ExternalIntentResultRecordOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ExternalIntentResultRecordOptions{}, time.Time{}, fmt.Errorf("workspace external intent result requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return ExternalIntentResultRecordOptions{}, time.Time{}, fmt.Errorf("workspace external intent result requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return ExternalIntentResultRecordOptions{}, time.Time{}, fmt.Errorf("workspace external intent result requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return ExternalIntentResultRecordOptions{}, time.Time{}, fmt.Errorf("workspace external intent result requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return ExternalIntentResultRecordOptions{}, time.Time{}, fmt.Errorf("workspace external intent result requires --lease")
	}
	opts.IntentID = strings.TrimSpace(opts.IntentID)
	if opts.IntentID == "" {
		return ExternalIntentResultRecordOptions{}, time.Time{}, fmt.Errorf("workspace external intent result requires --intent")
	}
	status, err := normalizeExternalIntentResultStatus(opts.Status)
	if err != nil {
		return ExternalIntentResultRecordOptions{}, time.Time{}, err
	}
	opts.Status = status
	opts.Details = strings.TrimSpace(opts.Details)
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func normalizeExternalIntentResultStatus(value string) (string, error) {
	status := strings.TrimSpace(strings.ToLower(value))
	switch status {
	case ExternalResultSucceeded,
		ExternalResultNotPerformed,
		ExternalResultFailedBeforeSideEffect,
		ExternalResultFailedAfterSideEffect,
		ExternalResultAmbiguous:
		return status, nil
	default:
		return "", fmt.Errorf("unsupported external intent result status: %s", value)
	}
}

func normalizeExternalIntentReconcileOptions(opts ExternalIntentReconcileOptions) (ExternalIntentReconcileOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ExternalIntentReconcileOptions{}, time.Time{}, fmt.Errorf("workspace external intent reconcile requires <workspace-dir>")
	}
	opts.IntentID = strings.TrimSpace(opts.IntentID)
	if opts.IntentID == "" {
		return ExternalIntentReconcileOptions{}, time.Time{}, fmt.Errorf("workspace external intent reconcile requires --intent")
	}
	opts.Operator = strings.TrimSpace(opts.Operator)
	if opts.Operator == "" {
		return ExternalIntentReconcileOptions{}, time.Time{}, fmt.Errorf("workspace external intent reconcile requires --operator")
	}
	opts.Details = strings.TrimSpace(opts.Details)
	if opts.Details == "" {
		return ExternalIntentReconcileOptions{}, time.Time{}, fmt.Errorf("workspace external intent reconcile requires --details")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func externalIntentTarget(action string, branch string, pr string) (string, error) {
	hasBranch := branch != ""
	hasPR := pr != ""
	if hasBranch && hasPR {
		return "", fmt.Errorf("workspace external intent reserve accepts only one target: --branch or --pr")
	}
	switch action {
	case ExternalActionPush, ExternalActionOpenPR, ExternalActionRemoteDelete:
		if !hasBranch {
			return "", fmt.Errorf("workspace external intent reserve action %s requires --branch", action)
		}
		return "branch:" + branch, nil
	case ExternalActionMerge:
		if hasPR {
			return "pr:" + pr, nil
		}
		if hasBranch {
			return "branch:" + branch, nil
		}
		return "", fmt.Errorf("workspace external intent reserve action merge requires --pr or --branch")
	default:
		return "", fmt.Errorf("unsupported external intent action: %s", action)
	}
}

type externalIntentIdentity struct {
	intentID       string
	idempotencyKey string
}

func deriveExternalIntentIdentity(workspaceID string, opts ExternalIntentReserveOptions, target string) externalIntentIdentity {
	parts := []string{
		"workspace", workspaceID,
		"merge_unit", opts.MergeUnitID,
		"attempt", opts.AttemptID,
		"action", opts.Action,
		"target", target,
		"branch", opts.Branch,
		"pr", opts.PR,
		"head", opts.RequestedHeadSHA,
		"base", opts.ExpectedBaseSHA,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	key := hex.EncodeToString(sum[:])
	return externalIntentIdentity{intentID: "intent-" + key[:24], idempotencyKey: key}
}

func externalIntentAffectedResources(opts ExternalIntentReserveOptions, target string, baseRef string) []string {
	resources := []string{
		MergeUnitResource(opts.MergeUnitID),
		ProviderTargetResource(opts.Action + ":" + target),
	}
	remoteRef := opts.Branch
	if opts.Action == ExternalActionMerge && opts.PR != "" {
		remoteRef = strings.TrimSpace(baseRef)
	}
	if remoteRef != "" {
		resources = append(resources, RemoteRefResource(remoteRef))
	}
	sort.Strings(resources)
	return resources
}

func externalIntentView(opts ExternalIntentReserveOptions, identity externalIntentIdentity, target string, affectedResources []string, queueReservation *mergeIntentQueueReservation) ExternalIntentView {
	view := ExternalIntentView{
		IntentID:          identity.intentID,
		IdempotencyKey:    identity.idempotencyKey,
		MergeUnitID:       opts.MergeUnitID,
		AttemptID:         opts.AttemptID,
		AgentID:           opts.AgentID,
		LeaseID:           opts.LeaseID,
		Action:            opts.Action,
		Scope:             opts.Scope,
		Target:            target,
		ApprovalID:        opts.ApprovalID,
		Branch:            opts.Branch,
		PR:                opts.PR,
		RequestedHeadSHA:  opts.RequestedHeadSHA,
		ExpectedBaseSHA:   opts.ExpectedBaseSHA,
		AffectedResources: append([]string{}, affectedResources...),
		Status:            "reserved",
	}
	if queueReservation != nil {
		view.QueueID = queueReservation.entry.QueueID
		view.QueuePosition = queueReservation.view.Position
	}
	return view
}

func externalIntentRecordedResultView(status string, policyAccepted bool, details string, operator string, eventID string, eventHash string) ExternalIntentRecordedResultView {
	return ExternalIntentRecordedResultView{
		Status:         status,
		PolicyAccepted: policyAccepted,
		Accepted:       externalIntentResultAccepted(status, policyAccepted),
		Details:        details,
		Operator:       operator,
		EventID:        eventID,
		EventHash:      eventHash,
	}
}

func externalIntentResultAccepted(status string, policyAccepted bool) bool {
	if status == ExternalResultAmbiguous {
		return false
	}
	return status == ExternalResultSucceeded || status == ExternalResultReconciledByOperator || policyAccepted
}
