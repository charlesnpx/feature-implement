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
	plannedAt = state.ObservedAt
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
	if err := validateExternalProviderPlanApproval(state.Events, reserveOpts, plannedAt); err != nil {
		return ExternalProviderPlanResult{}, err
	}
	if err := validateExternalProviderPlanMergeEvidence(state.Events, reserveOpts); err != nil {
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
	plannedIntent := externalIntentView(reserveOpts, identity, target, affectedResources, nil)
	plannedIntent.Status = "planned"
	return ExternalProviderPlanResult{
		Status:       "planned",
		WorkspaceDir: reserveOpts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		Intent:       plannedIntent,
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

func validateExternalProviderPlanApproval(events []JournalEvent, opts ExternalIntentReserveOptions, plannedAt time.Time) error {
	approvals, err := approvalSnapshots(events)
	if err != nil {
		return err
	}
	approval, ok := approvals[opts.ApprovalID]
	if !ok {
		return fmt.Errorf("approval not found: %s", opts.ApprovalID)
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
		now:         plannedAt,
	}); err != nil {
		return err
	}
	if staleInputs := approvalStaleInputsFromEvents(events, approval); len(staleInputs) > 0 {
		return fmt.Errorf("approval %s is stale after refresh changed %s", approval.ApprovalID, strings.Join(staleInputs, ", "))
	}
	return nil
}

func validateExternalProviderPlanMergeEvidence(events []JournalEvent, opts ExternalIntentReserveOptions) error {
	if opts.Action != ExternalActionRemoteDelete {
		return nil
	}
	intents, err := externalIntentSnapshots(events)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if intent.MergeUnitID == opts.MergeUnitID &&
			intent.AttemptID == opts.AttemptID &&
			intent.Action == ExternalActionMerge &&
			intent.Result != nil &&
			intent.Result.Accepted {
			return nil
		}
	}
	return fmt.Errorf("remote-delete provider plan requires accepted merge external intent evidence for merge unit %s attempt %s", opts.MergeUnitID, opts.AttemptID)
}

func externalProviderPlanView(opts ExternalIntentReserveOptions, worktree string, remote string, baseRef string, title string, prBody string, marker ExternalProviderMarker) (ExternalProviderPlanView, error) {
	approvalCommand := approvalCheckCommand(opts)
	intentCommand := intentReserveCommand(opts)
	providerCommand, err := providerCommand(opts, worktree, remote, baseRef, title, prBody, marker.IntentID)
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
	return intentResultCommandWithStatus(opts, intentID, ExternalResultSucceeded, "provider command completed")
}

func intentResultCommandWithStatus(opts ExternalIntentReserveOptions, intentID string, status string, details string) string {
	return strings.Join([]string{
		"feature workspace external intent result",
		shellQuote(opts.WorkspaceDir),
		"--merge-unit", shellQuote(opts.MergeUnitID),
		"--attempt", shellQuote(opts.AttemptID),
		"--agent", shellQuote(opts.AgentID),
		"--lease", shellQuote(opts.LeaseID),
		"--intent", shellQuote(intentID),
		"--status", shellQuote(status),
		"--details", shellQuote(details),
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

func providerCommand(opts ExternalIntentReserveOptions, worktree string, remote string, baseRef string, title string, prBody string, intentID string) (string, error) {
	switch opts.Action {
	case ExternalActionPush:
		return strings.Join([]string{
			localHeadCheckCommand(worktree, opts.RequestedHeadSHA),
			"&&",
			"git", "-C", shellQuote(worktree), "push", "-u", shellQuote(remote), shellQuote(opts.RequestedHeadSHA + ":refs/heads/" + opts.Branch),
		}, " "), nil
	case ExternalActionOpenPR:
		parts := []string{
			remoteBranchHeadCheckCommand(worktree, remote, opts.Branch, opts.RequestedHeadSHA),
			"&&",
			"cd", shellQuote(worktree), "&&", "gh pr create",
			"--base", shellQuote(baseRef),
			"--head", shellQuote(opts.Branch),
		}
		if strings.TrimSpace(title) != "" {
			parts = append(parts, "--title", shellQuote(strings.TrimSpace(title)))
		}
		parts = append(parts, "--body", shellQuote(prBody))
		parts = append(parts, "&&", remoteBranchHeadCheckCommand(worktree, remote, opts.Branch, opts.RequestedHeadSHA))
		return strings.Join(parts, " "), nil
	case ExternalActionMerge:
		target := opts.PR
		if target == "" {
			target = opts.Branch
		}
		return strings.Join([]string{
			mergePRHeadBaseCheckCommand(target, opts.RequestedHeadSHA, opts.ExpectedBaseSHA),
			"||",
			providerFailureResultBlock(opts, intentID, ExternalResultFailedBeforeSideEffect, "merge preflight head/base mismatch"),
			"&&",
			"(", "cd", shellQuote(worktree), "&&", "gh pr merge", shellQuote(target), "--merge", "--match-head-commit", shellQuote(opts.RequestedHeadSHA), ")",
			"||",
			providerFailureResultBlock(opts, intentID, ExternalResultAmbiguous, "merge provider command failed after preflight"),
		}, " "), nil
	case ExternalActionRemoteDelete:
		return strings.Join([]string{
			remoteBranchHeadCheckCommand(worktree, remote, opts.Branch, opts.RequestedHeadSHA),
			"||",
			providerFailureResultBlock(opts, intentID, ExternalResultFailedBeforeSideEffect, "remote-delete preflight head mismatch"),
			"&&",
			"git", "-C", shellQuote(worktree), "push", shellQuote(remote), shellQuote("--force-with-lease=refs/heads/" + opts.Branch + ":" + opts.RequestedHeadSHA), shellQuote(":refs/heads/" + opts.Branch),
			"||",
			providerFailureResultBlock(opts, intentID, ExternalResultAmbiguous, "remote-delete provider command failed after preflight"),
		}, " "), nil
	default:
		return "", fmt.Errorf("unsupported external provider action: %s", opts.Action)
	}
}

func localHeadCheckCommand(worktree string, headSHA string) string {
	return "test \"$(git -C " + shellQuote(worktree) + " rev-parse HEAD)\" = " + shellQuote(headSHA)
}

func remoteBranchHeadCheckCommand(worktree string, remote string, branch string, headSHA string) string {
	return "test \"$(git -C " + shellQuote(worktree) + " ls-remote " + shellQuote(remote) + " " + shellQuote("refs/heads/"+branch) + " | awk '{print $1}')\" = " + shellQuote(headSHA)
}

func mergePRHeadBaseCheckCommand(target string, headSHA string, baseSHA string) string {
	jq := ".headRefOid + \" \" + .baseRefOid"
	return "test \"$(gh pr view " + shellQuote(target) + " --json headRefOid,baseRefOid --jq " + shellQuote(jq) + ")\" = " + shellQuote(headSHA+" "+baseSHA)
}

func providerFailureResultBlock(opts ExternalIntentReserveOptions, intentID string, status string, details string) string {
	return "{ " + intentResultCommandWithStatus(opts, intentID, status, details) + "; exit 1; }"
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
