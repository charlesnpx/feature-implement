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
	EventExternalIntentReserved = "external_intent.reserved"

	eventPayloadIntentIDKey          = "intent_id"
	eventPayloadIdempotencyKeyKey    = "idempotency_key"
	eventPayloadActionKey            = "action"
	eventPayloadTargetKey            = "target"
	eventPayloadApprovalIDRefKey     = "approval_id"
	eventPayloadRequestedHeadSHAKey  = "requested_head_sha"
	eventPayloadExpectedBaseSHAKey   = "expected_base_sha"
	eventPayloadAffectedResourcesKey = "affected_resources"

	ExternalActionPush         = "push"
	ExternalActionOpenPR       = "open-pr"
	ExternalActionMerge        = "merge"
	ExternalActionRemoteDelete = "remote-delete"
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

type ExternalIntentResult struct {
	Status       string             `json:"status"`
	WorkspaceDir string             `json:"workspace_dir"`
	WorkspaceID  string             `json:"workspace_id"`
	BaseRef      string             `json:"base_ref"`
	Intent       ExternalIntentView `json:"intent"`
	EventID      string             `json:"event_id,omitempty"`
	EventHash    string             `json:"event_hash,omitempty"`
}

type ExternalIntentView struct {
	IntentID          string   `json:"intent_id"`
	IdempotencyKey    string   `json:"idempotency_key"`
	MergeUnitID       string   `json:"merge_unit_id"`
	AttemptID         string   `json:"attempt_id"`
	AgentID           string   `json:"agent_id,omitempty"`
	LeaseID           string   `json:"lease_id,omitempty"`
	Action            string   `json:"action"`
	Scope             string   `json:"scope"`
	Target            string   `json:"target"`
	ApprovalID        string   `json:"approval_id"`
	Branch            string   `json:"branch,omitempty"`
	PR                string   `json:"pr,omitempty"`
	RequestedHeadSHA  string   `json:"requested_head_sha"`
	ExpectedBaseSHA   string   `json:"expected_base_sha"`
	AffectedResources []string `json:"affected_resources"`
	Status            string   `json:"status"`
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

func ReserveExternalIntent(opts ExternalIntentReserveOptions) (ExternalIntentResult, error) {
	opts, reservedAt, target, err := normalizeExternalIntentReserveOptions(opts)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, reservedAt)
	if err != nil {
		return ExternalIntentResult{}, err
	}
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
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
	identity := deriveExternalIntentIdentity(state.View.WorkspaceID, opts, target)
	intentResource := ExternalIntentResource(identity.intentID)
	affectedResources := externalIntentAffectedResources(opts, target, state.View.BaseRef)
	readSet := map[string]int{
		LeaseResource(opts.MergeUnitID):     state.Revisions[LeaseResource(opts.MergeUnitID)],
		MergeUnitResource(opts.MergeUnitID): state.Revisions[MergeUnitResource(opts.MergeUnitID)],
		ApprovalResource(opts.ApprovalID):   state.Revisions[ApprovalResource(opts.ApprovalID)],
		intentResource:                      0,
	}
	writeSet := []string{intentResource}
	for _, resource := range affectedResources {
		readSet[resource] = state.Revisions[resource]
		writeSet = append(writeSet, resource)
	}
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventExternalIntentReserved,
		Payload: map[string]any{
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
		},
		ReadSet:  readSet,
		WriteSet: writeSet,
		Now:      func() time.Time { return reservedAt },
	})
	if err != nil {
		return ExternalIntentResult{}, err
	}
	return ExternalIntentResult{
		Status:       "reserved",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		Intent:       externalIntentView(opts, identity, target, affectedResources),
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

func externalIntentView(opts ExternalIntentReserveOptions, identity externalIntentIdentity, target string, affectedResources []string) ExternalIntentView {
	return ExternalIntentView{
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
}
