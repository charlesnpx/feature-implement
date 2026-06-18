package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ExternalProviderPlanOptions struct {
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
	Remote           string
	Worktree         string
	Title            string
	Body             string
	Now              func() time.Time
}

type ExternalProviderPlanResult struct {
	Status       string                   `json:"status"`
	WorkspaceDir string                   `json:"workspace_dir"`
	WorkspaceID  string                   `json:"workspace_id"`
	BaseRef      string                   `json:"base_ref"`
	Intent       ExternalIntentView       `json:"intent"`
	Plan         ExternalProviderPlanView `json:"plan"`
}

type ExternalProviderPlanView struct {
	Action          string                 `json:"action"`
	ApprovalID      string                 `json:"approval_id"`
	ApprovalCommand string                 `json:"approval_command"`
	IntentCommand   string                 `json:"intent_command"`
	ProviderCommand string                 `json:"provider_command"`
	ResultCommand   string                 `json:"result_command"`
	Commands        []string               `json:"commands"`
	Marker          ExternalProviderMarker `json:"marker"`
	PRBody          string                 `json:"pr_body,omitempty"`
}

type ExternalProviderMarker struct {
	SchemaVersion  int    `json:"schema_version"`
	WorkspaceID    string `json:"workspace_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	AttemptID      string `json:"attempt_id"`
	IntentID       string `json:"intent_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Action         string `json:"action"`
	Target         string `json:"target"`
	HeadSHA        string `json:"head_sha"`
	BaseSHA        string `json:"base_sha"`
}

func PlanExternalProviderCommand(opts ExternalProviderPlanOptions) (ExternalProviderPlanResult, error) {
	reserveOpts, plannedAt, target, err := normalizeExternalProviderPlanOptions(opts)
	if err != nil {
		return ExternalProviderPlanResult{}, err
	}
	state, err := loadLeaseOperationState(reserveOpts.WorkspaceDir, plannedAt)
	if err != nil {
		return ExternalProviderPlanResult{}, err
	}
	lease, _, err := requireOwnedActiveLease(state, reserveOpts.LeaseID, reserveOpts.AgentID)
	if err != nil {
		return ExternalProviderPlanResult{}, err
	}
	if lease.MergeUnitID != reserveOpts.MergeUnitID {
		return ExternalProviderPlanResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", reserveOpts.LeaseID, lease.MergeUnitID, reserveOpts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return ExternalProviderPlanResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, reserveOpts.MergeUnitID, reserveOpts.AttemptID, plannedAt)
	if err != nil {
		return ExternalProviderPlanResult{}, err
	}
	if err := validateAttemptLeaseOwner(reserveOpts.AttemptID, current.AgentID, current.LeaseID, reserveOpts.AgentID, reserveOpts.LeaseID); err != nil {
		return ExternalProviderPlanResult{}, err
	}
	identity := deriveExternalIntentIdentity(state.View.WorkspaceID, reserveOpts, target)
	intentResource := ExternalIntentResource(identity.intentID)
	if observed := state.Revisions[intentResource]; observed != 0 {
		return ExternalProviderPlanResult{}, StaleResourceError{Resource: intentResource, Expected: 0, Observed: observed}
	}
	affectedResources := externalIntentAffectedResources(reserveOpts, target, state.View.BaseRef)
	if err := validateResourcesNotFrozen(state.Events, state.ActiveLeases, affectedResources, "external provider plan"); err != nil {
		return ExternalProviderPlanResult{}, err
	}
	worktree := strings.TrimSpace(opts.Worktree)
	if worktree == "" {
		worktree = current.Worktree
	}
	remote := strings.TrimSpace(opts.Remote)
	if remote == "" {
		remote = "origin"
	}
	marker := ExternalProviderMarker{
		SchemaVersion:  1,
		WorkspaceID:    state.View.WorkspaceID,
		MergeUnitID:    reserveOpts.MergeUnitID,
		AttemptID:      reserveOpts.AttemptID,
		IntentID:       identity.intentID,
		IdempotencyKey: identity.idempotencyKey,
		Action:         reserveOpts.Action,
		Target:         target,
		HeadSHA:        reserveOpts.RequestedHeadSHA,
		BaseSHA:        reserveOpts.ExpectedBaseSHA,
	}
	prBody, err := providerPRBody(opts.Body, marker)
	if err != nil {
		return ExternalProviderPlanResult{}, err
	}
	plan, err := externalProviderPlanView(reserveOpts, worktree, remote, state.View.BaseRef, opts.Title, prBody, marker)
	if err != nil {
		return ExternalProviderPlanResult{}, err
	}
	return ExternalProviderPlanResult{
		Status:       "planned",
		WorkspaceDir: reserveOpts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		Intent:       externalIntentView(reserveOpts, identity, target, affectedResources),
		Plan:         plan,
	}, nil
}

func normalizeExternalProviderPlanOptions(opts ExternalProviderPlanOptions) (ExternalIntentReserveOptions, time.Time, string, error) {
	reserveOpts := ExternalIntentReserveOptions{
		WorkspaceDir:     opts.WorkspaceDir,
		MergeUnitID:      opts.MergeUnitID,
		AttemptID:        opts.AttemptID,
		AgentID:          opts.AgentID,
		LeaseID:          opts.LeaseID,
		ApprovalID:       opts.ApprovalID,
		Action:           opts.Action,
		Scope:            opts.Scope,
		Branch:           opts.Branch,
		PR:               opts.PR,
		RequestedHeadSHA: opts.RequestedHeadSHA,
		ExpectedBaseSHA:  opts.ExpectedBaseSHA,
		Now:              opts.Now,
	}
	return normalizeExternalIntentReserveOptions(reserveOpts)
}

func externalProviderPlanView(opts ExternalIntentReserveOptions, worktree string, remote string, baseRef string, title string, prBody string, marker ExternalProviderMarker) (ExternalProviderPlanView, error) {
	approvalCommand := approvalCheckCommand(opts)
	intentCommand := intentReserveCommand(opts)
	providerCommand, err := providerCommand(opts, worktree, remote, baseRef, title, prBody)
	if err != nil {
		return ExternalProviderPlanView{}, err
	}
	resultCommand := intentResultCommand(opts, marker.IntentID)
	commands := []string{approvalCommand, intentCommand, providerCommand, resultCommand}
	view := ExternalProviderPlanView{
		Action:          opts.Action,
		ApprovalID:      opts.ApprovalID,
		ApprovalCommand: approvalCommand,
		IntentCommand:   intentCommand,
		ProviderCommand: providerCommand,
		ResultCommand:   resultCommand,
		Commands:        commands,
		Marker:          marker,
	}
	if opts.Action == ExternalActionOpenPR {
		view.PRBody = prBody
	}
	return view, nil
}

func approvalCheckCommand(opts ExternalIntentReserveOptions) string {
	parts := []string{
		"feature workspace approve check",
		shellQuote(opts.WorkspaceDir),
		"--merge-unit", shellQuote(opts.MergeUnitID),
		"--attempt", shellQuote(opts.AttemptID),
		"--action", shellQuote(opts.Action),
		"--scope", shellQuote(opts.Scope),
	}
	parts = appendTargetFlags(parts, opts)
	parts = append(parts, "--head-sha", shellQuote(opts.RequestedHeadSHA), "--base-sha", shellQuote(opts.ExpectedBaseSHA), "--json")
	return strings.Join(parts, " ")
}

func intentReserveCommand(opts ExternalIntentReserveOptions) string {
	parts := []string{
		"feature workspace external intent reserve",
		shellQuote(opts.WorkspaceDir),
		"--merge-unit", shellQuote(opts.MergeUnitID),
		"--attempt", shellQuote(opts.AttemptID),
		"--agent", shellQuote(opts.AgentID),
		"--lease", shellQuote(opts.LeaseID),
		"--approval", shellQuote(opts.ApprovalID),
		"--action", shellQuote(opts.Action),
		"--scope", shellQuote(opts.Scope),
	}
	parts = appendTargetFlags(parts, opts)
	parts = append(parts, "--head-sha", shellQuote(opts.RequestedHeadSHA), "--base-sha", shellQuote(opts.ExpectedBaseSHA), "--json")
	return strings.Join(parts, " ")
}

func intentResultCommand(opts ExternalIntentReserveOptions, intentID string) string {
	return strings.Join([]string{
		"feature workspace external intent result",
		shellQuote(opts.WorkspaceDir),
		"--merge-unit", shellQuote(opts.MergeUnitID),
		"--attempt", shellQuote(opts.AttemptID),
		"--agent", shellQuote(opts.AgentID),
		"--lease", shellQuote(opts.LeaseID),
		"--intent", shellQuote(intentID),
		"--status", ExternalResultSucceeded,
		"--details", shellQuote("provider command completed"),
		"--json",
	}, " ")
}

func appendTargetFlags(parts []string, opts ExternalIntentReserveOptions) []string {
	if opts.Branch != "" {
		parts = append(parts, "--branch", shellQuote(opts.Branch))
	}
	if opts.PR != "" {
		parts = append(parts, "--pr", shellQuote(opts.PR))
	}
	return parts
}

func providerCommand(opts ExternalIntentReserveOptions, worktree string, remote string, baseRef string, title string, prBody string) (string, error) {
	switch opts.Action {
	case ExternalActionPush:
		return fmt.Sprintf("git -C %s push -u %s %s", shellQuote(worktree), shellQuote(remote), shellQuote("HEAD:"+opts.Branch)), nil
	case ExternalActionOpenPR:
		parts := []string{
			"cd", shellQuote(worktree), "&&", "gh pr create",
			"--base", shellQuote(baseRef),
			"--head", shellQuote(opts.Branch),
		}
		if strings.TrimSpace(title) != "" {
			parts = append(parts, "--title", shellQuote(strings.TrimSpace(title)))
		}
		parts = append(parts, "--body", shellQuote(prBody))
		return strings.Join(parts, " "), nil
	case ExternalActionMerge:
		target := opts.PR
		if target == "" {
			target = opts.Branch
		}
		return fmt.Sprintf("gh pr merge %s --merge", shellQuote(target)), nil
	case ExternalActionRemoteDelete:
		return fmt.Sprintf("git push %s --delete %s", shellQuote(remote), shellQuote(opts.Branch)), nil
	default:
		return "", fmt.Errorf("unsupported external provider action: %s", opts.Action)
	}
}

func providerPRBody(body string, marker ExternalProviderMarker) (string, error) {
	markerJSON, err := json.Marshal(marker)
	if err != nil {
		return "", err
	}
	body = strings.TrimSpace(body)
	if body != "" {
		body += "\n\n"
	}
	return body + "<!-- feature-workspace " + string(markerJSON) + " -->", nil
}
