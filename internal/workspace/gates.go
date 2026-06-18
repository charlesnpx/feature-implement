package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	EventGateEvaluationRecorded = "gate_evaluation.recorded"

	GateEvaluatorVersion = "workspace-gate-evaluator/v1"

	GateStatusPassed        = "passed"
	GateStatusPending       = "pending"
	GateStatusBlocked       = "blocked"
	GateStatusRerunRequired = "rerun_required"

	eventPayloadEvaluatorVersionKey = "evaluator_version"
	eventPayloadInputHashKey        = "input_hash"
	eventPayloadOutputHashKey       = "output_hash"
	eventPayloadPolicyIDKey         = "policy_id"
	eventPayloadPolicyVersionKey    = "policy_version"
	eventPayloadGatesKey            = "gates"
)

type GateEvaluateOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	AgentID      string
	LeaseID      string
	Now          func() time.Time
}

type GateEvaluationResult struct {
	Status           string           `json:"status"`
	WorkspaceDir     string           `json:"workspace_dir"`
	WorkspaceID      string           `json:"workspace_id"`
	BaseRef          string           `json:"base_ref"`
	MergeUnitID      string           `json:"merge_unit_id"`
	AttemptID        string           `json:"attempt_id"`
	AgentID          string           `json:"agent_id"`
	LeaseID          string           `json:"lease_id"`
	PolicyID         string           `json:"policy_id"`
	PolicyVersion    string           `json:"policy_version"`
	EvaluatorVersion string           `json:"evaluator_version"`
	InputHash        string           `json:"input_hash"`
	OutputHash       string           `json:"output_hash"`
	Gates            []GateStatusView `json:"gates"`
	EventID          string           `json:"event_id,omitempty"`
	EventHash        string           `json:"event_hash,omitempty"`
}

type GateStatusView struct {
	Gate   string `json:"gate"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type gateEvaluationInput struct {
	SchemaVersion    int                     `json:"schema_version"`
	EvaluatorVersion string                  `json:"evaluator_version"`
	Policy           WorkspaceGatePolicyLock `json:"policy"`
	WorkspaceID      string                  `json:"workspace_id"`
	BaseRef          string                  `json:"base_ref"`
	MergeUnitID      string                  `json:"merge_unit_id"`
	AttemptID        string                  `json:"attempt_id"`
	Attempt          gateAttemptInput        `json:"attempt"`
	Refresh          *gateRefreshInput       `json:"refresh,omitempty"`
	Contracts        []ContractBindingStatus `json:"contracts"`
	Approvals        []ApprovalView          `json:"approvals"`
}

type gateAttemptInput struct {
	AttemptNumber int    `json:"attempt_number"`
	StartedAt     string `json:"started_at"`
	AgentID       string `json:"agent_id"`
	LeaseID       string `json:"lease_id"`
	Branch        string `json:"branch"`
	Worktree      string `json:"worktree"`
	BaseRef       string `json:"base_ref"`
	BaseSHA       string `json:"base_sha"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
}

type gateRefreshInput struct {
	Status       string               `json:"status"`
	Resource     string               `json:"resource"`
	Branch       string               `json:"branch"`
	Worktree     string               `json:"worktree"`
	OldBase      string               `json:"old_base"`
	NewBase      string               `json:"new_base"`
	PreHead      string               `json:"pre_head"`
	PostHead     string               `json:"post_head"`
	BackupRef    string               `json:"backup_ref"`
	EvidencePath string               `json:"evidence_path"`
	InputChanges []RefreshInputChange `json:"input_changes,omitempty"`
}

type gateEvaluationOutput struct {
	SchemaVersion    int              `json:"schema_version"`
	EvaluatorVersion string           `json:"evaluator_version"`
	InputHash        string           `json:"input_hash"`
	Gates            []GateStatusView `json:"gates"`
}

func GateEvaluationResource(mergeUnitID string, attemptID string) string {
	return resourceKey("gate", mergeUnitID+":"+attemptID)
}

func EvaluateGates(opts GateEvaluateOptions) (GateEvaluationResult, error) {
	opts, evaluatedAt, err := normalizeGateEvaluateOptions(opts)
	if err != nil {
		return GateEvaluationResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return GateEvaluationResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, evaluatedAt)
	if err != nil {
		return GateEvaluationResult{}, err
	}
	evaluatedAt = state.ObservedAt
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return GateEvaluationResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return GateEvaluationResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return GateEvaluationResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, evaluatedAt)
	if err != nil {
		return GateEvaluationResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return GateEvaluationResult{}, err
	}
	input, err := buildGateEvaluationInput(lock, state.Events, current, evaluatedAt)
	if err != nil {
		return GateEvaluationResult{}, err
	}
	inputHash, err := stableHash(input)
	if err != nil {
		return GateEvaluationResult{}, err
	}
	gates := evaluateGateStatuses(input)
	output := gateEvaluationOutput{
		SchemaVersion:    1,
		EvaluatorVersion: GateEvaluatorVersion,
		InputHash:        inputHash,
		Gates:            gates,
	}
	outputHash, err := stableHash(output)
	if err != nil {
		return GateEvaluationResult{}, err
	}

	gateResource := GateEvaluationResource(opts.MergeUnitID, opts.AttemptID)
	leaseResource := LeaseResource(opts.MergeUnitID)
	mergeUnitResource := MergeUnitResource(opts.MergeUnitID)
	readSet := gateEvaluationReadSet(state.Revisions, input, gateResource, leaseResource, mergeUnitResource)
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventGateEvaluationRecorded,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:      opts.MergeUnitID,
			eventPayloadAttemptIDKey:        opts.AttemptID,
			eventPayloadAgentIDKey:          opts.AgentID,
			eventPayloadLeaseIDKey:          opts.LeaseID,
			eventPayloadPolicyIDKey:         lock.GatePolicy.ID,
			eventPayloadPolicyVersionKey:    lock.GatePolicy.Version,
			eventPayloadEvaluatorVersionKey: GateEvaluatorVersion,
			eventPayloadInputHashKey:        inputHash,
			eventPayloadOutputHashKey:       outputHash,
			eventPayloadGatesKey:            gateStatusPayload(gates),
		},
		ReadSet:  readSet,
		WriteSet: []string{gateResource},
		Now:      func() time.Time { return evaluatedAt },
	})
	if err != nil {
		return GateEvaluationResult{}, err
	}
	return GateEvaluationResult{
		Status:           "recorded",
		WorkspaceDir:     opts.WorkspaceDir,
		WorkspaceID:      lock.WorkspaceID,
		BaseRef:          lock.BaseRef,
		MergeUnitID:      opts.MergeUnitID,
		AttemptID:        opts.AttemptID,
		AgentID:          opts.AgentID,
		LeaseID:          opts.LeaseID,
		PolicyID:         lock.GatePolicy.ID,
		PolicyVersion:    lock.GatePolicy.Version,
		EvaluatorVersion: GateEvaluatorVersion,
		InputHash:        inputHash,
		OutputHash:       outputHash,
		Gates:            gates,
		EventID:          event.ID,
		EventHash:        event.EventHash,
	}, nil
}

func normalizeGateEvaluateOptions(opts GateEvaluateOptions) (GateEvaluateOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return GateEvaluateOptions{}, time.Time{}, fmt.Errorf("workspace evaluate-gates requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return GateEvaluateOptions{}, time.Time{}, fmt.Errorf("workspace evaluate-gates requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return GateEvaluateOptions{}, time.Time{}, fmt.Errorf("workspace evaluate-gates requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return GateEvaluateOptions{}, time.Time{}, fmt.Errorf("workspace evaluate-gates requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return GateEvaluateOptions{}, time.Time{}, fmt.Errorf("workspace evaluate-gates requires --lease")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func buildGateEvaluationInput(lock WorkspaceLock, events []JournalEvent, attempt attemptSnapshot, evaluatedAt time.Time) (gateEvaluationInput, error) {
	contracts, err := contractBindingStatuses(lock, events, attempt.MergeUnitID, attempt.AttemptID)
	if err != nil {
		return gateEvaluationInput{}, err
	}
	approvals, err := approvalSnapshots(events)
	if err != nil {
		return gateEvaluationInput{}, err
	}
	input := gateEvaluationInput{
		SchemaVersion:    1,
		EvaluatorVersion: GateEvaluatorVersion,
		Policy:           lock.GatePolicy,
		WorkspaceID:      lock.WorkspaceID,
		BaseRef:          lock.BaseRef,
		MergeUnitID:      attempt.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		Attempt: gateAttemptInput{
			AttemptNumber: attempt.AttemptNumber,
			StartedAt:     attempt.StartedAt.UTC().Format(time.RFC3339Nano),
			AgentID:       attempt.AgentID,
			LeaseID:       attempt.LeaseID,
			Branch:        attempt.Branch,
			Worktree:      attempt.Worktree,
			BaseRef:       attempt.BaseRef,
			BaseSHA:       attempt.BaseSHA,
			Mode:          attempt.Mode,
			Status:        attempt.Status,
		},
		Contracts: contracts,
		Approvals: approvalViewsForStatus(approvals, events, attempt.MergeUnitID, attempt.AttemptID, evaluatedAt),
	}
	if refresh, ok := latestRefresh(events, attempt.MergeUnitID, attempt.AttemptID); ok {
		input.Refresh = &gateRefreshInput{
			Status:       refresh.Status,
			Resource:     refresh.Resource,
			Branch:       refresh.Branch,
			Worktree:     refresh.Worktree,
			OldBase:      refresh.OldBase,
			NewBase:      refresh.NewBase,
			PreHead:      refresh.PreHead,
			PostHead:     refresh.PostHead,
			BackupRef:    refresh.BackupRef,
			EvidencePath: refresh.EvidencePath,
			InputChanges: append([]RefreshInputChange(nil), refresh.InputChanges...),
		}
	}
	return input, nil
}

func evaluateGateStatuses(input gateEvaluationInput) []GateStatusView {
	gates := []GateStatusView{
		{Gate: "review", Status: GateStatusPending, Reason: "no_attempt_review_evidence"},
		{Gate: "contract", Status: contractGateStatus(input.Contracts), Reason: contractGateReason(input.Contracts)},
		{Gate: "security", Status: GateStatusPending, Reason: "no_attempt_security_evidence"},
		{Gate: "test", Status: testGateStatus(input.Refresh), Reason: testGateReason(input.Refresh)},
		{Gate: "merge_approval", Status: mergeApprovalGateStatus(input.Approvals), Reason: mergeApprovalGateReason(input.Approvals)},
	}
	for i := range gates {
		if gates[i].Reason == "" {
			gates[i].Reason = "satisfied"
		}
	}
	sortGateStatusViews(gates)
	return gates
}

func contractGateStatus(contracts []ContractBindingStatus) string {
	switch aggregateContractBindingStatus(contracts) {
	case "none", contractBindingStatusCurrent:
		return GateStatusPassed
	default:
		return GateStatusBlocked
	}
}

func contractGateReason(contracts []ContractBindingStatus) string {
	status := aggregateContractBindingStatus(contracts)
	if status == "none" || status == contractBindingStatusCurrent {
		return ""
	}
	return "contract_bindings_" + status
}

func testGateStatus(refresh *gateRefreshInput) string {
	if refresh != nil && len(refresh.InputChanges) > 0 {
		return GateStatusRerunRequired
	}
	return GateStatusPending
}

func testGateReason(refresh *gateRefreshInput) string {
	if refresh != nil && len(refresh.InputChanges) > 0 {
		return "refresh_changed_inputs"
	}
	return "no_attempt_test_evidence"
}

func mergeApprovalGateStatus(approvals []ApprovalView) string {
	for _, approval := range approvals {
		if approval.Status != "active" || len(approval.StaleInputs) > 0 {
			continue
		}
		if containsString(approval.Actions, "merge") {
			return GateStatusPassed
		}
	}
	return GateStatusPending
}

func mergeApprovalGateReason(approvals []ApprovalView) string {
	if mergeApprovalGateStatus(approvals) == GateStatusPassed {
		return ""
	}
	for _, approval := range approvals {
		if containsString(approval.Actions, "merge") && len(approval.StaleInputs) > 0 {
			return "merge_approval_stale"
		}
	}
	return "no_attempt_merge_approval"
}

func gateEvaluationReadSet(revisions map[string]int, input gateEvaluationInput, resources ...string) map[string]int {
	readSet := map[string]int{}
	addGateReadSetResources(readSet, revisions, resources...)
	if input.Refresh != nil {
		addGateReadSetResources(readSet, revisions, input.Refresh.Resource)
		for _, change := range input.Refresh.InputChanges {
			addGateReadSetResources(readSet, revisions, change.Resource)
		}
	}
	for _, binding := range input.Contracts {
		addGateReadSetResources(readSet, revisions,
			ContractResource(binding.ContractID),
			ContractBindingResource(input.MergeUnitID, binding.ContractID, binding.ArtifactID),
		)
	}
	for _, approval := range input.Approvals {
		addGateReadSetResources(readSet, revisions, ApprovalResource(approval.ApprovalID))
		if approval.HeadSHA != "" || approval.BaseSHA != "" {
			addGateReadSetResources(readSet, revisions,
				RefreshInputResource(approval.MergeUnitID, approval.AttemptID, refreshInputBase),
				RefreshInputResource(approval.MergeUnitID, approval.AttemptID, refreshInputHead),
			)
		}
	}
	return readSet
}

func addGateReadSetResources(readSet map[string]int, revisions map[string]int, resources ...string) {
	for _, resource := range resources {
		if strings.TrimSpace(resource) == "" {
			continue
		}
		readSet[resource] = revisions[resource]
	}
}

func gateStatusPayload(gates []GateStatusView) []any {
	payload := make([]any, 0, len(gates))
	for _, gate := range gates {
		item := map[string]any{
			"gate":   gate.Gate,
			"status": gate.Status,
		}
		if gate.Reason != "" {
			item["reason"] = gate.Reason
		}
		payload = append(payload, item)
	}
	return payload
}

func stableHash(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func sortGateStatusViews(gates []GateStatusView) {
	sort.Slice(gates, func(i, j int) bool { return gates[i].Gate < gates[j].Gate })
}
