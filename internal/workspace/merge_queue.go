package workspace

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	EventMergeQueueEntered = "merge_queue.entered"

	eventPayloadQueueIDKey          = "queue_id"
	eventPayloadQueuePositionKey    = "queue_position"
	eventPayloadGateInputHashKey    = "gate_input_hash"
	eventPayloadGateOutputHashKey   = "gate_output_hash"
	eventPayloadQueuedAtKey         = "queued_at"
	mergeQueueStatusQueued          = "queued"
	mergeQueueRequiredActionRefresh = "refresh_branch"
)

type MergeQueueOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	AgentID      string
	LeaseID      string
	ApprovalID   string
	Scope        string
	PR           string
	Branch       string
	HeadSHA      string
	BaseSHA      string
	Now          func() time.Time
}

type MergeQueueResult struct {
	Status             string                       `json:"status"`
	WorkspaceDir       string                       `json:"workspace_dir"`
	WorkspaceID        string                       `json:"workspace_id"`
	BaseRef            string                       `json:"base_ref"`
	Queue              *MergeQueueEntryView         `json:"queue,omitempty"`
	BlockingConditions []SchedulerBlockingCondition `json:"blocking_conditions,omitempty"`
	EventID            string                       `json:"event_id,omitempty"`
	EventHash          string                       `json:"event_hash,omitempty"`
}

type MergeQueueEntryView struct {
	QueueID        string `json:"queue_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	AttemptID      string `json:"attempt_id"`
	AgentID        string `json:"agent_id,omitempty"`
	LeaseID        string `json:"lease_id,omitempty"`
	ApprovalID     string `json:"approval_id"`
	Scope          string `json:"scope"`
	PR             string `json:"pr,omitempty"`
	Branch         string `json:"branch,omitempty"`
	HeadSHA        string `json:"head_sha"`
	BaseSHA        string `json:"base_sha"`
	GateInputHash  string `json:"gate_input_hash"`
	GateOutputHash string `json:"gate_output_hash"`
	Position       int    `json:"position"`
	QueuedAt       string `json:"queued_at"`
	Status         string `json:"status"`
}

type mergeQueueSnapshot struct {
	QueueID        string
	MergeUnitID    string
	AttemptID      string
	AgentID        string
	LeaseID        string
	ApprovalID     string
	Scope          string
	PR             string
	Branch         string
	HeadSHA        string
	BaseSHA        string
	GateInputHash  string
	GateOutputHash string
	Position       int
	QueuedAt       time.Time
}

type mergeQueueEvaluation struct {
	ApprovalID     string
	GateInputHash  string
	GateOutputHash string
}

type mergeQueueTracker struct {
	entries map[string]mergeQueueSnapshot
}

func MergeQueueResource() string {
	return resourceKey("merge_queue", "global")
}

func QueueMergeUnit(opts MergeQueueOptions) (MergeQueueResult, error) {
	opts, queuedAt, err := normalizeMergeQueueOptions(opts)
	if err != nil {
		return MergeQueueResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return MergeQueueResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, queuedAt)
	if err != nil {
		return MergeQueueResult{}, err
	}
	queuedAt = state.ObservedAt
	lease, unit, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return MergeQueueResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return MergeQueueResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return MergeQueueResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, queuedAt)
	if err != nil {
		return MergeQueueResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return MergeQueueResult{}, err
	}
	if unit.MergeQueue != nil && unit.MergeQueue.AttemptID == opts.AttemptID {
		return MergeQueueResult{}, fmt.Errorf("merge unit %s attempt %s is already queued", opts.MergeUnitID, opts.AttemptID)
	}
	evaluation, blocking, err := evaluateMergeQueueReadiness(lock, state.Events, state.View, state.UnitByID, unit, current, mergeQueueCandidateFromOptions(opts), queuedAt)
	if err != nil {
		return MergeQueueResult{}, err
	}
	if len(blocking) > 0 {
		return MergeQueueResult{
			Status:             "blocked",
			WorkspaceDir:       opts.WorkspaceDir,
			WorkspaceID:        lock.WorkspaceID,
			BaseRef:            lock.BaseRef,
			BlockingConditions: blocking,
		}, nil
	}

	position := len(state.View.MergeQueue) + 1
	queueID := mergeQueueID(opts.MergeUnitID, opts.AttemptID, opts.HeadSHA, opts.BaseSHA, queuedAt)
	entry := mergeQueueSnapshot{
		QueueID:        queueID,
		MergeUnitID:    opts.MergeUnitID,
		AttemptID:      opts.AttemptID,
		AgentID:        opts.AgentID,
		LeaseID:        opts.LeaseID,
		ApprovalID:     evaluation.ApprovalID,
		Scope:          opts.Scope,
		PR:             opts.PR,
		Branch:         opts.Branch,
		HeadSHA:        opts.HeadSHA,
		BaseSHA:        opts.BaseSHA,
		GateInputHash:  evaluation.GateInputHash,
		GateOutputHash: evaluation.GateOutputHash,
		Position:       position,
		QueuedAt:       queuedAt,
	}
	readSet := mergeQueueReadSet(state.Revisions, state.Events, current, unit, entry)
	event, err := appendMergeQueueEvent(opts.WorkspaceDir, entry, readSet, queuedAt)
	if err != nil {
		return MergeQueueResult{}, err
	}
	view := entry.View(position)
	return MergeQueueResult{
		Status:       mergeQueueStatusQueued,
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  lock.WorkspaceID,
		BaseRef:      lock.BaseRef,
		Queue:        &view,
		EventID:      event.ID,
		EventHash:    event.EventHash,
	}, nil
}

func normalizeMergeQueueOptions(opts MergeQueueOptions) (MergeQueueOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires --lease")
	}
	opts.ApprovalID = strings.TrimSpace(opts.ApprovalID)
	opts.Scope = normalizeApprovalScope(opts.Scope)
	opts.PR = normalizeApprovalPR(opts.PR)
	opts.Branch = strings.TrimSpace(opts.Branch)
	if opts.PR == "" && opts.Branch == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires --pr or --branch")
	}
	opts.HeadSHA = strings.TrimSpace(opts.HeadSHA)
	if opts.HeadSHA == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires --head-sha")
	}
	opts.BaseSHA = strings.TrimSpace(opts.BaseSHA)
	if opts.BaseSHA == "" {
		return MergeQueueOptions{}, time.Time{}, fmt.Errorf("workspace queue enter requires --base-sha")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func appendMergeQueueEvent(workspaceDir string, entry mergeQueueSnapshot, readSet map[string]int, queuedAt time.Time) (JournalEvent, error) {
	return AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         EventMergeQueueEntered,
		Payload: map[string]any{
			eventPayloadQueueIDKey:        entry.QueueID,
			eventPayloadMergeUnitIDKey:    entry.MergeUnitID,
			eventPayloadAttemptIDKey:      entry.AttemptID,
			eventPayloadAgentIDKey:        entry.AgentID,
			eventPayloadLeaseIDKey:        entry.LeaseID,
			eventPayloadApprovalIDRefKey:  entry.ApprovalID,
			eventPayloadScopeKey:          entry.Scope,
			eventPayloadPRKey:             entry.PR,
			eventPayloadBranchKey:         entry.Branch,
			eventPayloadHeadSHAKey:        entry.HeadSHA,
			eventPayloadBaseSHAKey:        entry.BaseSHA,
			eventPayloadGateInputHashKey:  entry.GateInputHash,
			eventPayloadGateOutputHashKey: entry.GateOutputHash,
			eventPayloadQueuePositionKey:  entry.Position,
			eventPayloadQueuedAtKey:       entry.QueuedAt.UTC().Format(time.RFC3339Nano),
		},
		ReadSet:  readSet,
		WriteSet: []string{MergeQueueResource(), QueueSlotResource(entry.QueueID)},
		Now:      func() time.Time { return queuedAt },
	})
}

func mergeQueueReadSet(revisions map[string]int, events []JournalEvent, attempt attemptSnapshot, unit SchedulerMergeUnitView, entry mergeQueueSnapshot) map[string]int {
	readSet := map[string]int{
		MergeQueueResource():                                        revisions[MergeQueueResource()],
		QueueSlotResource(entry.QueueID):                            revisions[QueueSlotResource(entry.QueueID)],
		LeaseResource(entry.MergeUnitID):                            revisions[LeaseResource(entry.MergeUnitID)],
		MergeUnitResource(entry.MergeUnitID):                        revisions[MergeUnitResource(entry.MergeUnitID)],
		GateEvaluationResource(entry.MergeUnitID, entry.AttemptID):  revisions[GateEvaluationResource(entry.MergeUnitID, entry.AttemptID)],
		ApprovalResource(entry.ApprovalID):                          revisions[ApprovalResource(entry.ApprovalID)],
		ApprovalAttemptResource(entry.MergeUnitID, entry.AttemptID): revisions[ApprovalAttemptResource(entry.MergeUnitID, entry.AttemptID)],
		RefreshResource(entry.MergeUnitID + ":" + entry.AttemptID):  revisions[RefreshResource(entry.MergeUnitID+":"+entry.AttemptID)],
	}
	for _, dependencyID := range unit.Dependencies {
		resource := MergeUnitResource(dependencyID)
		readSet[resource] = revisions[resource]
	}
	for _, binding := range unit.ContractBindings {
		contractResource := ContractResource(binding.ContractID)
		bindingResource := ContractBindingResource(unit.ID, binding.ContractID, binding.ArtifactID)
		readSet[contractResource] = revisions[contractResource]
		readSet[bindingResource] = revisions[bindingResource]
	}
	for _, resource := range allGateOverrideResources(entry.MergeUnitID, entry.AttemptID) {
		readSet[resource] = revisions[resource]
	}
	if refresh, ok := latestRefresh(events, attempt.MergeUnitID, attempt.AttemptID); ok {
		readSet[refresh.Resource] = revisions[refresh.Resource]
		for _, change := range refresh.InputChanges {
			readSet[change.Resource] = revisions[change.Resource]
		}
	}
	for _, input := range []string{refreshInputBase, refreshInputHead} {
		resource := RefreshInputResource(entry.MergeUnitID, entry.AttemptID, input)
		readSet[resource] = revisions[resource]
	}
	return readSet
}

func newMergeQueueTracker() *mergeQueueTracker {
	return &mergeQueueTracker{entries: map[string]mergeQueueSnapshot{}}
}

func (t *mergeQueueTracker) Apply(event JournalEvent) error {
	if event.Type != EventMergeQueueEntered {
		return nil
	}
	entry, err := mergeQueueFromEvent(event)
	if err != nil {
		return err
	}
	t.entries[mergeQueueAttemptKey(entry.MergeUnitID, entry.AttemptID)] = entry
	return nil
}

func (t *mergeQueueTracker) Entries() []mergeQueueSnapshot {
	entries := make([]mergeQueueSnapshot, 0, len(t.entries))
	for _, entry := range t.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].QueuedAt.Equal(entries[j].QueuedAt) {
			return entries[i].QueuedAt.Before(entries[j].QueuedAt)
		}
		return entries[i].QueueID < entries[j].QueueID
	})
	return entries
}

func populateMergeQueue(view *SchedulerView, lock WorkspaceLock, events []JournalEvent, unitByID map[string]*SchedulerMergeUnitView, attempts *attemptTracker, approvals map[string]approvalSnapshot, queues *mergeQueueTracker, now time.Time) error {
	position := 1
	for _, entry := range queues.Entries() {
		unit := unitByID[entry.MergeUnitID]
		if unit == nil || unit.CurrentAttempt == nil || unit.CurrentAttempt.AttemptID != entry.AttemptID {
			continue
		}
		attempt := attempts.Current(entry.MergeUnitID)
		if attempt == nil || attempt.AttemptID != entry.AttemptID {
			continue
		}
		candidate := mergeQueueCandidateFromSnapshot(entry)
		_, blocking, err := evaluateMergeQueueReadiness(lock, events, *view, unitByID, *unit, *attempt, candidate, now)
		if err != nil {
			return err
		}
		if len(blocking) > 0 {
			continue
		}
		if _, ok := approvals[entry.ApprovalID]; !ok {
			continue
		}
		item := entry.View(position)
		view.MergeQueue = append(view.MergeQueue, item)
		unit.MergeQueue = &item
		position++
	}
	return nil
}

type mergeQueueCandidate struct {
	ApprovalID string
	Scope      string
	PR         string
	Branch     string
	HeadSHA    string
	BaseSHA    string
}

func mergeQueueCandidateFromOptions(opts MergeQueueOptions) mergeQueueCandidate {
	return mergeQueueCandidate{
		ApprovalID: opts.ApprovalID,
		Scope:      opts.Scope,
		PR:         opts.PR,
		Branch:     opts.Branch,
		HeadSHA:    opts.HeadSHA,
		BaseSHA:    opts.BaseSHA,
	}
}

func mergeQueueCandidateFromSnapshot(entry mergeQueueSnapshot) mergeQueueCandidate {
	return mergeQueueCandidate{
		ApprovalID: entry.ApprovalID,
		Scope:      entry.Scope,
		PR:         entry.PR,
		Branch:     entry.Branch,
		HeadSHA:    entry.HeadSHA,
		BaseSHA:    entry.BaseSHA,
	}
}

func evaluateMergeQueueReadiness(lock WorkspaceLock, events []JournalEvent, view SchedulerView, unitByID map[string]*SchedulerMergeUnitView, unit SchedulerMergeUnitView, attempt attemptSnapshot, candidate mergeQueueCandidate, now time.Time) (mergeQueueEvaluation, []SchedulerBlockingCondition, error) {
	conditions := mergeQueueStructuralConditions(view, unitByID, unit, attempt.AttemptID)
	approvals, err := approvalSnapshots(events)
	if err != nil {
		return mergeQueueEvaluation{}, nil, err
	}
	approval, approvalConditions := selectMergeQueueApproval(approvals, events, attempt, candidate, now)
	conditions = append(conditions, approvalConditions...)
	input, err := buildGateEvaluationInput(lock, events, attempt, now)
	if err != nil {
		return mergeQueueEvaluation{}, nil, err
	}
	inputHash, err := stableHash(input)
	if err != nil {
		return mergeQueueEvaluation{}, nil, err
	}
	gates, err := evaluateGateStatusesWithOverrides(events, input, inputHash, now)
	if err != nil {
		return mergeQueueEvaluation{}, nil, err
	}
	output := gateEvaluationOutput{
		SchemaVersion:    1,
		EvaluatorVersion: GateEvaluatorVersion,
		InputHash:        inputHash,
		Gates:            gates,
	}
	outputHash, err := stableHash(output)
	if err != nil {
		return mergeQueueEvaluation{}, nil, err
	}
	baseSHA, headSHA := gateInputSHAs(input)
	if baseSHA == "" || headSHA == "" {
		conditions = append(conditions, SchedulerBlockingCondition{
			Type:           "missing_refresh",
			Resource:       RefreshResource(attempt.MergeUnitID + ":" + attempt.AttemptID),
			AttemptID:      attempt.AttemptID,
			RequiredAction: mergeQueueRequiredActionRefresh,
		})
	} else if baseSHA != candidate.BaseSHA || headSHA != candidate.HeadSHA {
		conditions = append(conditions, SchedulerBlockingCondition{
			Type:           "stale_refresh_input",
			Resource:       RefreshResource(attempt.MergeUnitID + ":" + attempt.AttemptID),
			AttemptID:      attempt.AttemptID,
			Status:         "stale",
			RequiredAction: mergeQueueRequiredActionRefresh,
		})
	}
	latestGate, ok, err := latestGateEvaluation(events, attempt.MergeUnitID, attempt.AttemptID)
	if err != nil {
		return mergeQueueEvaluation{}, nil, err
	}
	if !ok {
		conditions = append(conditions, SchedulerBlockingCondition{
			Type:           "missing_gate_evaluation",
			Resource:       GateEvaluationResource(attempt.MergeUnitID, attempt.AttemptID),
			AttemptID:      attempt.AttemptID,
			RequiredAction: "evaluate_gates",
		})
	} else if latestGate.InputHash != inputHash || latestGate.OutputHash != outputHash {
		conditions = append(conditions, SchedulerBlockingCondition{
			Type:           "stale_gate_evaluation",
			Resource:       GateEvaluationResource(attempt.MergeUnitID, attempt.AttemptID),
			AttemptID:      attempt.AttemptID,
			Status:         "stale",
			RequiredAction: "evaluate_gates",
		})
	}
	conditions = append(conditions, mergeQueueGateConditions(attempt, gates)...)
	sortSchedulerBlockingConditions(conditions)
	return mergeQueueEvaluation{
		ApprovalID:     approval.ApprovalID,
		GateInputHash:  inputHash,
		GateOutputHash: outputHash,
	}, conditions, nil
}

func mergeQueueStructuralConditions(view SchedulerView, unitByID map[string]*SchedulerMergeUnitView, unit SchedulerMergeUnitView, attemptID string) []SchedulerBlockingCondition {
	conditions := []SchedulerBlockingCondition{}
	switch unit.Status {
	case MergeUnitCompleted, MergeUnitFailed:
		conditions = append(conditions, SchedulerBlockingCondition{
			Type:           "lifecycle",
			Resource:       MergeUnitResource(unit.ID),
			AttemptID:      attemptID,
			Status:         unit.Status,
			RequiredAction: "start_new_attempt",
		})
	}
	freezesByResource := externalIntentFreezesByResource(view.FrozenResources)
	conditions = append(conditions, schedulerBlockingConditions(
		incompleteDependencies(unit.Dependencies, unitByID),
		unit.ContractBindings,
		freezesByResource[MergeUnitResource(unit.ID)],
		filterRefreshBlockingConditions(unit.BlockingConditions, attemptID),
	)...)
	return conditions
}

func filterRefreshBlockingConditions(conditions []SchedulerBlockingCondition, attemptID string) []SchedulerBlockingCondition {
	filtered := []SchedulerBlockingCondition{}
	for _, condition := range conditions {
		if condition.AttemptID == attemptID {
			filtered = append(filtered, condition)
		}
	}
	return filtered
}

func selectMergeQueueApproval(approvals map[string]approvalSnapshot, events []JournalEvent, attempt attemptSnapshot, candidate mergeQueueCandidate, now time.Time) (approvalSnapshot, []SchedulerBlockingCondition) {
	if candidate.ApprovalID != "" {
		approval, ok := approvals[candidate.ApprovalID]
		if !ok {
			return approvalSnapshot{}, []SchedulerBlockingCondition{mergeQueueApprovalCondition(candidate.ApprovalID, attempt.MergeUnitID, attempt.AttemptID, "missing", "grant_merge_approval")}
		}
		if conditions := mergeQueueApprovalBlockingConditions(approval, events, attempt, candidate, now); len(conditions) > 0 {
			return approvalSnapshot{}, conditions
		}
		return approval, nil
	}
	ids := make([]string, 0, len(approvals))
	for id := range approvals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		approval := approvals[id]
		if len(mergeQueueApprovalBlockingConditions(approval, events, attempt, candidate, now)) == 0 {
			return approval, nil
		}
	}
	return approvalSnapshot{}, []SchedulerBlockingCondition{mergeQueueApprovalCondition("", attempt.MergeUnitID, attempt.AttemptID, "missing", "grant_merge_approval")}
}

func mergeQueueApprovalBlockingConditions(approval approvalSnapshot, events []JournalEvent, attempt attemptSnapshot, candidate mergeQueueCandidate, now time.Time) []SchedulerBlockingCondition {
	if staleInputs := approvalStaleInputsFromEvents(events, approval); len(staleInputs) > 0 {
		return []SchedulerBlockingCondition{{
			Type:           "stale_approval",
			Resource:       ApprovalResource(approval.ApprovalID),
			AttemptID:      attempt.AttemptID,
			Status:         strings.Join(staleInputs, ","),
			RequiredAction: "grant_merge_approval",
		}}
	}
	if err := approvalMatches(approval, approvalMatchRequest{
		mergeUnitID: attempt.MergeUnitID,
		attemptID:   attempt.AttemptID,
		action:      ExternalActionMerge,
		scope:       candidate.Scope,
		pr:          candidate.PR,
		branch:      candidate.Branch,
		headSHA:     candidate.HeadSHA,
		baseSHA:     candidate.BaseSHA,
		now:         now,
	}); err != nil {
		return []SchedulerBlockingCondition{mergeQueueApprovalCondition(approval.ApprovalID, attempt.MergeUnitID, attempt.AttemptID, "blocked", "grant_merge_approval")}
	}
	return nil
}

func mergeQueueApprovalCondition(approvalID string, mergeUnitID string, attemptID string, status string, action string) SchedulerBlockingCondition {
	resource := ApprovalAttemptResource(mergeUnitID, attemptID)
	if approvalID != "" {
		resource = ApprovalResource(approvalID)
	}
	return SchedulerBlockingCondition{
		Type:           "merge_approval",
		Resource:       resource,
		AttemptID:      attemptID,
		Status:         status,
		RequiredAction: action,
	}
}

func mergeQueueGateConditions(attempt attemptSnapshot, gates []GateStatusView) []SchedulerBlockingCondition {
	conditions := []SchedulerBlockingCondition{}
	for _, gate := range gates {
		if gate.Status == GateStatusPassed || gate.Status == GateStatusRetainedByOperator {
			continue
		}
		conditions = append(conditions, SchedulerBlockingCondition{
			Type:           "gate",
			Resource:       GateEvaluationResource(attempt.MergeUnitID, attempt.AttemptID),
			AttemptID:      attempt.AttemptID,
			Gate:           gate.Gate,
			Status:         gate.Status,
			RequiredAction: "satisfy_gate",
		})
	}
	return conditions
}

type gateEvaluationSnapshot struct {
	InputHash  string
	OutputHash string
}

func latestGateEvaluation(events []JournalEvent, mergeUnitID string, attemptID string) (gateEvaluationSnapshot, bool, error) {
	var latest gateEvaluationSnapshot
	found := false
	for _, event := range events {
		if event.Type != EventGateEvaluationRecorded {
			continue
		}
		eventMergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
		if err != nil {
			return gateEvaluationSnapshot{}, false, err
		}
		eventAttemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
		if err != nil {
			return gateEvaluationSnapshot{}, false, err
		}
		if eventMergeUnitID != mergeUnitID || eventAttemptID != attemptID {
			continue
		}
		inputHash, err := eventStringPayload(event, eventPayloadInputHashKey)
		if err != nil {
			return gateEvaluationSnapshot{}, false, err
		}
		outputHash, err := eventStringPayload(event, eventPayloadOutputHashKey)
		if err != nil {
			return gateEvaluationSnapshot{}, false, err
		}
		latest = gateEvaluationSnapshot{InputHash: inputHash, OutputHash: outputHash}
		found = true
	}
	return latest, found, nil
}

func validateMergeQueueEvent(event JournalEvent) error {
	_, err := mergeQueueFromEvent(event)
	return err
}

func mergeQueueFromEvent(event JournalEvent) (mergeQueueSnapshot, error) {
	queueID, err := eventStringPayload(event, eventPayloadQueueIDKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	agentID, err := eventStringPayload(event, eventPayloadAgentIDKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	leaseID, err := eventStringPayload(event, eventPayloadLeaseIDKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	approvalID, err := eventStringPayload(event, eventPayloadApprovalIDRefKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	scope, err := eventStringPayload(event, eventPayloadScopeKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	pr := optionalStringPayload(event, eventPayloadPRKey)
	branch := optionalStringPayload(event, eventPayloadBranchKey)
	if pr == "" && branch == "" {
		return mergeQueueSnapshot{}, fmt.Errorf("merge queue event %s requires PR or branch target", event.ID)
	}
	headSHA, err := eventStringPayload(event, eventPayloadHeadSHAKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	baseSHA, err := eventStringPayload(event, eventPayloadBaseSHAKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	gateInputHash, err := eventStringPayload(event, eventPayloadGateInputHashKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	gateOutputHash, err := eventStringPayload(event, eventPayloadGateOutputHashKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	position, err := eventIntPayload(event, eventPayloadQueuePositionKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	queuedAtText, err := eventStringPayload(event, eventPayloadQueuedAtKey)
	if err != nil {
		return mergeQueueSnapshot{}, err
	}
	queuedAt, err := time.Parse(time.RFC3339Nano, queuedAtText)
	if err != nil {
		return mergeQueueSnapshot{}, fmt.Errorf("merge queue event %s payload %s must be RFC3339: %w", event.ID, eventPayloadQueuedAtKey, err)
	}
	if !containsString(event.WriteSet, MergeQueueResource()) {
		return mergeQueueSnapshot{}, fmt.Errorf("merge queue event %s missing write_set resource %s", event.ID, MergeQueueResource())
	}
	slotResource := QueueSlotResource(queueID)
	if !containsString(event.WriteSet, slotResource) {
		return mergeQueueSnapshot{}, fmt.Errorf("merge queue event %s missing write_set resource %s", event.ID, slotResource)
	}
	return mergeQueueSnapshot{
		QueueID:        queueID,
		MergeUnitID:    mergeUnitID,
		AttemptID:      attemptID,
		AgentID:        agentID,
		LeaseID:        leaseID,
		ApprovalID:     approvalID,
		Scope:          scope,
		PR:             pr,
		Branch:         branch,
		HeadSHA:        headSHA,
		BaseSHA:        baseSHA,
		GateInputHash:  gateInputHash,
		GateOutputHash: gateOutputHash,
		Position:       position,
		QueuedAt:       queuedAt,
	}, nil
}

func mergeQueueAttemptKey(mergeUnitID string, attemptID string) string {
	return mergeUnitID + "\x00" + attemptID
}

func mergeQueueID(mergeUnitID string, attemptID string, headSHA string, baseSHA string, at time.Time) string {
	parts := []string{mergeUnitID, attemptID, headSHA, baseSHA, fmt.Sprintf("%d", at.UTC().UnixNano())}
	replacer := strings.NewReplacer(":", "-", "/", "-", "|", "-", " ", "-")
	return "merge-queue-" + replacer.Replace(strings.Join(parts, "|"))
}

func (e mergeQueueSnapshot) View(position int) MergeQueueEntryView {
	return MergeQueueEntryView{
		QueueID:        e.QueueID,
		MergeUnitID:    e.MergeUnitID,
		AttemptID:      e.AttemptID,
		AgentID:        e.AgentID,
		LeaseID:        e.LeaseID,
		ApprovalID:     e.ApprovalID,
		Scope:          e.Scope,
		PR:             e.PR,
		Branch:         e.Branch,
		HeadSHA:        e.HeadSHA,
		BaseSHA:        e.BaseSHA,
		GateInputHash:  e.GateInputHash,
		GateOutputHash: e.GateOutputHash,
		Position:       position,
		QueuedAt:       e.QueuedAt.UTC().Format(time.RFC3339Nano),
		Status:         mergeQueueStatusQueued,
	}
}

func sortSchedulerBlockingConditions(conditions []SchedulerBlockingCondition) {
	sort.Slice(conditions, func(i, j int) bool {
		if conditions[i].Type != conditions[j].Type {
			return conditions[i].Type < conditions[j].Type
		}
		if conditions[i].Resource != conditions[j].Resource {
			return conditions[i].Resource < conditions[j].Resource
		}
		if conditions[i].Gate != conditions[j].Gate {
			return conditions[i].Gate < conditions[j].Gate
		}
		return conditions[i].AttemptID < conditions[j].AttemptID
	})
}
