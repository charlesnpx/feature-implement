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
	EventGateOverrideRecorded   = "gate.override"

	GateEvaluatorVersion = "workspace-gate-evaluator/v1"

	GateStatusPassed             = "passed"
	GateStatusPending            = "pending"
	GateStatusBlocked            = "blocked"
	GateStatusRerunRequired      = "rerun_required"
	GateStatusRetainedByOperator = "retained_by_operator"

	eventPayloadOverrideIDKey       = "override_id"
	eventPayloadEvaluatorVersionKey = "evaluator_version"
	eventPayloadInputHashKey        = "input_hash"
	eventPayloadOutputHashKey       = "output_hash"
	eventPayloadPolicyIDKey         = "policy_id"
	eventPayloadPolicyVersionKey    = "policy_version"
	eventPayloadGatesKey            = "gates"
	eventPayloadGateKey             = "gate"
	eventPayloadFromStatusKey       = "from_status"
)

type GateEvaluateOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	AgentID      string
	LeaseID      string
	Now          func() time.Time
}

type GateOverrideOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	Gate         string
	Status       string
	Reason       string
	InputHash    string
	HeadSHA      string
	BaseSHA      string
	Operator     string
	ExpiresIn    time.Duration
	ExpiresAt    time.Time
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

type GateOverrideResult struct {
	Status       string           `json:"status"`
	WorkspaceDir string           `json:"workspace_dir"`
	WorkspaceID  string           `json:"workspace_id"`
	BaseRef      string           `json:"base_ref"`
	Override     GateOverrideView `json:"override"`
	EventID      string           `json:"event_id,omitempty"`
	EventHash    string           `json:"event_hash,omitempty"`
}

type GateStatusView struct {
	Gate           string `json:"gate"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	ComputedStatus string `json:"computed_status,omitempty"`
	OverrideID     string `json:"override_id,omitempty"`
	Operator       string `json:"operator,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
}

type GateOverrideView struct {
	OverrideID     string `json:"override_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	AttemptID      string `json:"attempt_id"`
	Gate           string `json:"gate"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	InputHash      string `json:"input_hash"`
	HeadSHA        string `json:"head_sha"`
	BaseSHA        string `json:"base_sha"`
	Operator       string `json:"operator"`
	ExpiresAt      string `json:"expires_at"`
	PolicyID       string `json:"policy_id"`
	PolicyVersion  string `json:"policy_version"`
	FromStatus     string `json:"from_status,omitempty"`
	OverrideStatus string `json:"override_status,omitempty"`
}

type gateOverrideSnapshot struct {
	OverrideID    string
	MergeUnitID   string
	AttemptID     string
	Gate          string
	Status        string
	Reason        string
	InputHash     string
	HeadSHA       string
	BaseSHA       string
	Operator      string
	ExpiresAt     time.Time
	PolicyID      string
	PolicyVersion string
	FromStatus    string
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

func GateOverrideResource(mergeUnitID string, attemptID string, gate string) string {
	return resourceKey("gate", mergeUnitID+":"+attemptID+":override:"+gate)
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
	gates, err := evaluateGateStatusesWithOverrides(state.Events, input, inputHash, evaluatedAt)
	if err != nil {
		return GateEvaluationResult{}, err
	}
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

func OverrideGate(opts GateOverrideOptions) (GateOverrideResult, error) {
	opts, overriddenAt, expiresAt, err := normalizeGateOverrideOptions(opts)
	if err != nil {
		return GateOverrideResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return GateOverrideResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, overriddenAt)
	if err != nil {
		return GateOverrideResult{}, err
	}
	overriddenAt = state.ObservedAt
	if opts.ExpiresAt.IsZero() {
		expiresAt = overriddenAt.Add(opts.ExpiresIn)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return GateOverrideResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, overriddenAt)
	if err != nil {
		return GateOverrideResult{}, err
	}
	input, err := buildGateEvaluationInput(lock, state.Events, current, overriddenAt)
	if err != nil {
		return GateOverrideResult{}, err
	}
	inputHash, err := stableHash(input)
	if err != nil {
		return GateOverrideResult{}, err
	}
	if inputHash != opts.InputHash {
		return GateOverrideResult{}, fmt.Errorf("gate override input hash %s does not match current evaluator input %s", opts.InputHash, inputHash)
	}
	baseSHA, headSHA := gateInputSHAs(input)
	if baseSHA == "" || headSHA == "" {
		return GateOverrideResult{}, fmt.Errorf("gate override requires current evaluator base and head SHA; record refresh evidence before overriding gates")
	}
	if opts.BaseSHA != baseSHA {
		return GateOverrideResult{}, fmt.Errorf("gate override base SHA %s does not match current base %s", opts.BaseSHA, baseSHA)
	}
	if opts.HeadSHA != headSHA {
		return GateOverrideResult{}, fmt.Errorf("gate override head SHA %s does not match current head %s", opts.HeadSHA, headSHA)
	}
	gates := evaluateGateStatuses(input)
	currentGate, ok := gateStatusByName(gates)[opts.Gate]
	if !ok {
		return GateOverrideResult{}, fmt.Errorf("unknown gate %s", opts.Gate)
	}
	overrideID := gateOverrideID(opts.MergeUnitID, opts.AttemptID, opts.Gate, opts.InputHash, opts.HeadSHA, opts.BaseSHA, overriddenAt)
	override := gateOverrideSnapshot{
		OverrideID:    overrideID,
		MergeUnitID:   opts.MergeUnitID,
		AttemptID:     opts.AttemptID,
		Gate:          opts.Gate,
		Status:        opts.Status,
		Reason:        opts.Reason,
		InputHash:     opts.InputHash,
		HeadSHA:       opts.HeadSHA,
		BaseSHA:       opts.BaseSHA,
		Operator:      opts.Operator,
		ExpiresAt:     expiresAt,
		PolicyID:      lock.GatePolicy.ID,
		PolicyVersion: lock.GatePolicy.Version,
		FromStatus:    currentGate.Status,
	}
	overrideResource := GateOverrideResource(opts.MergeUnitID, opts.AttemptID, opts.Gate)
	readSet := gateEvaluationReadSet(state.Revisions, input,
		GateEvaluationResource(opts.MergeUnitID, opts.AttemptID),
		overrideResource,
		MergeUnitResource(opts.MergeUnitID),
	)
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventGateOverrideRecorded,
		Payload: map[string]any{
			eventPayloadOverrideIDKey:    override.OverrideID,
			eventPayloadMergeUnitIDKey:   override.MergeUnitID,
			eventPayloadAttemptIDKey:     override.AttemptID,
			eventPayloadGateKey:          override.Gate,
			eventPayloadStatusKey:        override.Status,
			eventPayloadReasonKey:        override.Reason,
			eventPayloadInputHashKey:     override.InputHash,
			eventPayloadHeadSHAKey:       override.HeadSHA,
			eventPayloadBaseSHAKey:       override.BaseSHA,
			eventPayloadOperatorKey:      override.Operator,
			eventPayloadExpiresAtKey:     override.ExpiresAt.UTC().Format(time.RFC3339Nano),
			eventPayloadPolicyIDKey:      override.PolicyID,
			eventPayloadPolicyVersionKey: override.PolicyVersion,
			eventPayloadFromStatusKey:    override.FromStatus,
		},
		ReadSet:  readSet,
		WriteSet: []string{overrideResource},
		Now:      func() time.Time { return overriddenAt },
	})
	if err != nil {
		return GateOverrideResult{}, err
	}
	return GateOverrideResult{
		Status:       "recorded",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  lock.WorkspaceID,
		BaseRef:      lock.BaseRef,
		Override:     override.View(GateStatusRetainedByOperator),
		EventID:      event.ID,
		EventHash:    event.EventHash,
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

func normalizeGateOverrideOptions(opts GateOverrideOptions) (GateOverrideOptions, time.Time, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --attempt")
	}
	opts.Gate = strings.TrimSpace(opts.Gate)
	if opts.Gate == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --gate")
	}
	if !isOverridableGate(opts.Gate) {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("gate %s is not overridable", opts.Gate)
	}
	opts.Status = strings.TrimSpace(strings.ToLower(opts.Status))
	if opts.Status == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --status")
	}
	if opts.Status != GateStatusRetainedByOperator {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("unsupported gate override status %s; use %s", opts.Status, GateStatusRetainedByOperator)
	}
	opts.Reason = strings.TrimSpace(opts.Reason)
	if opts.Reason == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --reason")
	}
	opts.InputHash = strings.TrimSpace(opts.InputHash)
	if opts.InputHash == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --input-hash")
	}
	opts.HeadSHA = strings.TrimSpace(opts.HeadSHA)
	if opts.HeadSHA == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --head-sha")
	}
	opts.BaseSHA = strings.TrimSpace(opts.BaseSHA)
	if opts.BaseSHA == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --base-sha")
	}
	opts.Operator = strings.TrimSpace(opts.Operator)
	if opts.Operator == "" {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --operator")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	overriddenAt := now()
	expiresAt := opts.ExpiresAt
	if expiresAt.IsZero() {
		if opts.ExpiresIn <= 0 {
			return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace gate override requires --expires-in or --expires-at")
		}
		expiresAt = overriddenAt.Add(opts.ExpiresIn)
	}
	if !expiresAt.After(overriddenAt) {
		return GateOverrideOptions{}, time.Time{}, time.Time{}, fmt.Errorf("gate override expiry must be in the future")
	}
	return opts, overriddenAt, expiresAt, nil
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

func evaluateGateStatusesWithOverrides(events []JournalEvent, input gateEvaluationInput, inputHash string, evaluatedAt time.Time) ([]GateStatusView, error) {
	gates := evaluateGateStatuses(input)
	overrides, err := gateOverrideSnapshots(events)
	if err != nil {
		return nil, err
	}
	baseSHA, headSHA := gateInputSHAs(input)
	for i, gate := range gates {
		override, ok := overrides[gateOverrideKey(input.MergeUnitID, input.AttemptID, gate.Gate)]
		if !ok || !override.Applies(inputHash, baseSHA, headSHA, evaluatedAt) {
			continue
		}
		gates[i] = GateStatusView{
			Gate:           gate.Gate,
			Status:         GateStatusRetainedByOperator,
			Reason:         override.Reason,
			ComputedStatus: gate.Status,
			OverrideID:     override.OverrideID,
			Operator:       override.Operator,
			ExpiresAt:      override.ExpiresAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return gates, nil
}

func gateStatusByName(gates []GateStatusView) map[string]GateStatusView {
	byName := map[string]GateStatusView{}
	for _, gate := range gates {
		byName[gate.Gate] = gate
	}
	return byName
}

func gateInputSHAs(input gateEvaluationInput) (string, string) {
	if input.Refresh != nil {
		return input.Refresh.NewBase, input.Refresh.PostHead
	}
	return input.Attempt.BaseSHA, ""
}

func contractGateStatus(contracts []ContractBindingStatus) string {
	if failedContractCommand(contracts) != "" {
		return GateStatusBlocked
	}
	switch aggregateContractBindingStatus(contracts) {
	case "none", contractBindingStatusCurrent:
		return GateStatusPassed
	default:
		return GateStatusBlocked
	}
}

func contractGateReason(contracts []ContractBindingStatus) string {
	if failed := failedContractCommand(contracts); failed != "" {
		return "contract_command_failed:" + failed
	}
	status := aggregateContractBindingStatus(contracts)
	if status == "none" || status == contractBindingStatusCurrent {
		return ""
	}
	return "contract_bindings_" + status
}

func failedContractCommand(contracts []ContractBindingStatus) string {
	for _, binding := range contracts {
		if failed := firstFailedRefreshCommand(binding.CommandResults); failed != "" {
			return failed
		}
	}
	return ""
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
	addGateReadSetResources(readSet, revisions, allGateOverrideResources(input.MergeUnitID, input.AttemptID)...)
	addGateReadSetResources(readSet, revisions,
		RefreshResource(input.MergeUnitID+":"+input.AttemptID),
		ApprovalAttemptResource(input.MergeUnitID, input.AttemptID),
	)
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

func allGateOverrideResources(mergeUnitID string, attemptID string) []string {
	resources := []string{}
	for _, gate := range knownGateNames() {
		resources = append(resources, GateOverrideResource(mergeUnitID, attemptID, gate))
	}
	return resources
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

func gateOverrideID(mergeUnitID string, attemptID string, gate string, inputHash string, headSHA string, baseSHA string, at time.Time) string {
	parts := []string{mergeUnitID, attemptID, gate, inputHash, headSHA, baseSHA, fmt.Sprintf("%d", at.UTC().UnixNano())}
	replacer := strings.NewReplacer(":", "-", "/", "-", "|", "-", " ", "-")
	return "gate-override-" + replacer.Replace(strings.Join(parts, "|"))
}

func gateOverrideKey(mergeUnitID string, attemptID string, gate string) string {
	return mergeUnitID + "\x00" + attemptID + "\x00" + gate
}

func knownGateNames() []string {
	return []string{"contract", "merge_approval", "review", "security", "test"}
}

func isOverridableGate(gate string) bool {
	switch gate {
	case "contract", "review", "security", "test":
		return true
	default:
		return false
	}
}

func gateOverrideSnapshots(events []JournalEvent) (map[string]gateOverrideSnapshot, error) {
	overrides := map[string]gateOverrideSnapshot{}
	for _, event := range events {
		if event.Type != EventGateOverrideRecorded {
			continue
		}
		override, err := gateOverrideFromEvent(event)
		if err != nil {
			return nil, err
		}
		overrides[gateOverrideKey(override.MergeUnitID, override.AttemptID, override.Gate)] = override
	}
	return overrides, nil
}

func validateGateOverrideEvent(event JournalEvent) error {
	_, err := gateOverrideFromEvent(event)
	return err
}

func gateOverrideFromEvent(event JournalEvent) (gateOverrideSnapshot, error) {
	overrideID, err := eventStringPayload(event, eventPayloadOverrideIDKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	gate, err := eventStringPayload(event, eventPayloadGateKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	if !isOverridableGate(gate) {
		return gateOverrideSnapshot{}, fmt.Errorf("gate override event %s gate %s is not overridable", event.ID, gate)
	}
	status, err := eventStringPayload(event, eventPayloadStatusKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	if status != GateStatusRetainedByOperator {
		return gateOverrideSnapshot{}, fmt.Errorf("gate override event %s has unsupported status %s", event.ID, status)
	}
	reason, err := eventStringPayload(event, eventPayloadReasonKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	inputHash, err := eventStringPayload(event, eventPayloadInputHashKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	headSHA, err := eventStringPayload(event, eventPayloadHeadSHAKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	baseSHA, err := eventStringPayload(event, eventPayloadBaseSHAKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	operator, err := eventStringPayload(event, eventPayloadOperatorKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	expiresAtText, err := eventStringPayload(event, eventPayloadExpiresAtKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
	if err != nil {
		return gateOverrideSnapshot{}, fmt.Errorf("gate override event %s payload %s must be RFC3339: %w", event.ID, eventPayloadExpiresAtKey, err)
	}
	policyID, err := eventStringPayload(event, eventPayloadPolicyIDKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	policyVersion, err := eventStringPayload(event, eventPayloadPolicyVersionKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	fromStatus, err := eventStringPayload(event, eventPayloadFromStatusKey)
	if err != nil {
		return gateOverrideSnapshot{}, err
	}
	resource := GateOverrideResource(mergeUnitID, attemptID, gate)
	if !containsString(event.WriteSet, resource) {
		return gateOverrideSnapshot{}, fmt.Errorf("gate override event %s missing write_set resource %s", event.ID, resource)
	}
	return gateOverrideSnapshot{
		OverrideID:    overrideID,
		MergeUnitID:   mergeUnitID,
		AttemptID:     attemptID,
		Gate:          gate,
		Status:        status,
		Reason:        reason,
		InputHash:     inputHash,
		HeadSHA:       headSHA,
		BaseSHA:       baseSHA,
		Operator:      operator,
		ExpiresAt:     expiresAt,
		PolicyID:      policyID,
		PolicyVersion: policyVersion,
		FromStatus:    fromStatus,
	}, nil
}

func (o gateOverrideSnapshot) Applies(inputHash string, baseSHA string, headSHA string, at time.Time) bool {
	if !at.Before(o.ExpiresAt) || o.InputHash != inputHash {
		return false
	}
	if baseSHA == "" || o.BaseSHA != baseSHA {
		return false
	}
	if headSHA == "" || o.HeadSHA != headSHA {
		return false
	}
	return true
}

func (o gateOverrideSnapshot) View(status string) GateOverrideView {
	return GateOverrideView{
		OverrideID:     o.OverrideID,
		MergeUnitID:    o.MergeUnitID,
		AttemptID:      o.AttemptID,
		Gate:           o.Gate,
		Status:         status,
		Reason:         o.Reason,
		InputHash:      o.InputHash,
		HeadSHA:        o.HeadSHA,
		BaseSHA:        o.BaseSHA,
		Operator:       o.Operator,
		ExpiresAt:      o.ExpiresAt.UTC().Format(time.RFC3339Nano),
		PolicyID:       o.PolicyID,
		PolicyVersion:  o.PolicyVersion,
		FromStatus:     o.FromStatus,
		OverrideStatus: o.Status,
	}
}
