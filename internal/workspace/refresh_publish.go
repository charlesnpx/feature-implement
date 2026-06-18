package workspace

import (
	"fmt"
	"strings"
	"time"
)

type PublishRefreshOptions struct {
	WorkspaceDir       string
	MergeUnitID        string
	AttemptID          string
	AgentID            string
	LeaseID            string
	ApprovalID         string
	Branch             string
	Worktree           string
	Remote             string
	ExpectedRemoteSHA  string
	Scope              string
	Now                func() time.Time
	remoteHeadResolver func(worktree string, remote string, branch string) (string, error)
}

type PublishRefreshResult struct {
	Status            string                   `json:"status"`
	WorkspaceDir      string                   `json:"workspace_dir"`
	WorkspaceID       string                   `json:"workspace_id"`
	BaseRef           string                   `json:"base_ref"`
	MergeUnitID       string                   `json:"merge_unit_id"`
	AttemptID         string                   `json:"attempt_id"`
	Branch            string                   `json:"branch"`
	Worktree          string                   `json:"worktree"`
	Remote            string                   `json:"remote"`
	HeadSHA           string                   `json:"head_sha"`
	ExpectedRemoteSHA string                   `json:"expected_remote_sha"`
	ObservedRemoteSHA string                   `json:"observed_remote_sha"`
	Intent            ExternalIntentView       `json:"intent,omitempty"`
	Plan              ExternalProviderPlanView `json:"plan,omitempty"`
	EvidencePath      string                   `json:"evidence_path,omitempty"`
	EventID           string                   `json:"event_id,omitempty"`
	EventHash         string                   `json:"event_hash,omitempty"`
}

type RemoteBranchMovedError struct {
	Result PublishRefreshResult
}

func (e RemoteBranchMovedError) Error() string {
	observed := e.Result.ObservedRemoteSHA
	if observed == "" {
		observed = "<missing>"
	}
	return fmt.Sprintf("remote_branch_moved: %s expected %s observed %s", e.Result.Branch, e.Result.ExpectedRemoteSHA, observed)
}

func PublishRefresh(opts PublishRefreshOptions) (PublishRefreshResult, error) {
	opts, plannedAt, err := normalizePublishRefreshOptions(opts)
	if err != nil {
		return PublishRefreshResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, plannedAt)
	if err != nil {
		return PublishRefreshResult{}, err
	}
	plannedAt = state.ObservedAt
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return PublishRefreshResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return PublishRefreshResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return PublishRefreshResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, plannedAt)
	if err != nil {
		return PublishRefreshResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return PublishRefreshResult{}, err
	}
	worktree := opts.Worktree
	if worktree == "" {
		worktree = current.Worktree
	}
	if worktree != current.Worktree {
		return PublishRefreshResult{}, fmt.Errorf("publish-refresh worktree %s does not match current attempt worktree %s", worktree, current.Worktree)
	}
	refresh, ok := latestRefresh(state.Events, opts.MergeUnitID, opts.AttemptID)
	if !ok {
		return PublishRefreshResult{}, fmt.Errorf("publish-refresh requires a successful local refresh for attempt %s", opts.AttemptID)
	}
	if refresh.Status != RefreshStatusSucceeded {
		return PublishRefreshResult{}, fmt.Errorf("publish-refresh requires latest refresh status %s, got %s", RefreshStatusSucceeded, refresh.Status)
	}
	branch := opts.Branch
	if branch == "" {
		branch = refresh.Branch
	}
	if branch == "" {
		return PublishRefreshResult{}, fmt.Errorf("publish-refresh requires --branch")
	}
	if branch != refresh.Branch {
		return PublishRefreshResult{}, fmt.Errorf("publish-refresh branch %s does not match refreshed branch %s", branch, refresh.Branch)
	}
	if refresh.Worktree != "" && refresh.Worktree != worktree {
		return PublishRefreshResult{}, fmt.Errorf("publish-refresh worktree %s does not match refreshed worktree %s", worktree, refresh.Worktree)
	}
	remote := opts.Remote
	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
		WorkspaceDir:     opts.WorkspaceDir,
		MergeUnitID:      opts.MergeUnitID,
		AttemptID:        opts.AttemptID,
		AgentID:          opts.AgentID,
		LeaseID:          opts.LeaseID,
		ApprovalID:       opts.ApprovalID,
		Action:           ExternalActionPush,
		Scope:            opts.Scope,
		Branch:           branch,
		RequestedHeadSHA: refresh.PostHead,
		ExpectedBaseSHA:  opts.ExpectedRemoteSHA,
		Remote:           remote,
		Worktree:         worktree,
		Now:              func() time.Time { return plannedAt },
	})
	if err != nil {
		return PublishRefreshResult{}, err
	}
	observedRemoteSHA, err := opts.remoteHeadResolver(worktree, remote, branch)
	if err != nil {
		return PublishRefreshResult{}, err
	}
	if observedRemoteSHA != opts.ExpectedRemoteSHA {
		result, recordErr := appendRemoteBranchMovedRefreshEvent(opts, state, refresh, branch, worktree, remote, observedRemoteSHA, plannedAt)
		if recordErr != nil {
			return PublishRefreshResult{}, recordErr
		}
		return result, RemoteBranchMovedError{Result: result}
	}
	planned.Plan.ProviderCommand = forceWithLeasePushCommand(worktree, remote, branch, opts.ExpectedRemoteSHA)
	if len(planned.Plan.Commands) >= 3 {
		planned.Plan.Commands[2] = planned.Plan.ProviderCommand
	}
	return PublishRefreshResult{
		Status:            "planned",
		WorkspaceDir:      opts.WorkspaceDir,
		WorkspaceID:       state.View.WorkspaceID,
		BaseRef:           state.View.BaseRef,
		MergeUnitID:       opts.MergeUnitID,
		AttemptID:         opts.AttemptID,
		Branch:            branch,
		Worktree:          worktree,
		Remote:            remote,
		HeadSHA:           refresh.PostHead,
		ExpectedRemoteSHA: opts.ExpectedRemoteSHA,
		ObservedRemoteSHA: observedRemoteSHA,
		Intent:            planned.Intent,
		Plan:              planned.Plan,
	}, nil
}

func normalizePublishRefreshOptions(opts PublishRefreshOptions) (PublishRefreshOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return PublishRefreshOptions{}, time.Time{}, fmt.Errorf("workspace publish-refresh requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return PublishRefreshOptions{}, time.Time{}, fmt.Errorf("workspace publish-refresh requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return PublishRefreshOptions{}, time.Time{}, fmt.Errorf("workspace publish-refresh requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return PublishRefreshOptions{}, time.Time{}, fmt.Errorf("workspace publish-refresh requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return PublishRefreshOptions{}, time.Time{}, fmt.Errorf("workspace publish-refresh requires --lease")
	}
	opts.ApprovalID = strings.TrimSpace(opts.ApprovalID)
	if opts.ApprovalID == "" {
		return PublishRefreshOptions{}, time.Time{}, fmt.Errorf("workspace publish-refresh requires --approval")
	}
	opts.Branch = strings.TrimSpace(opts.Branch)
	opts.Worktree = strings.TrimSpace(opts.Worktree)
	opts.Remote = strings.TrimSpace(opts.Remote)
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	opts.ExpectedRemoteSHA = strings.TrimSpace(opts.ExpectedRemoteSHA)
	if opts.ExpectedRemoteSHA == "" {
		return PublishRefreshOptions{}, time.Time{}, fmt.Errorf("workspace publish-refresh requires --expected-remote-sha")
	}
	opts.Scope = normalizeApprovalScope(opts.Scope)
	if opts.remoteHeadResolver == nil {
		opts.remoteHeadResolver = resolveRemoteBranchSHA
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func appendRemoteBranchMovedRefreshEvent(opts PublishRefreshOptions, state leaseOperationState, refresh refreshSnapshot, branch string, worktree string, remote string, observedRemoteSHA string, recordedAt time.Time) (PublishRefreshResult, error) {
	observed := observedRemoteSHA
	if observed == "" {
		observed = "<missing>"
	}
	evidence := RefreshEvidence{
		SchemaVersion: 1,
		WorkspaceID:   state.View.WorkspaceID,
		BaseRef:       state.View.BaseRef,
		MergeUnitID:   opts.MergeUnitID,
		AttemptID:     opts.AttemptID,
		AgentID:       opts.AgentID,
		LeaseID:       opts.LeaseID,
		Local:         false,
		Branch:        branch,
		Worktree:      worktree,
		OldBase:       refresh.OldBase,
		NewBase:       refresh.NewBase,
		PreHead:       refresh.PreHead,
		PostHead:      refresh.PostHead,
		BackupRef:     refresh.BackupRef,
		Verification: RefreshVerification{
			Status:        RefreshStatusRemoteBranchMoved,
			FailureReason: fmt.Sprintf("remote branch %s/%s moved: expected %s observed %s", remote, branch, opts.ExpectedRemoteSHA, observed),
		},
	}
	evidencePath, err := writeRefreshEvidence(opts.WorkspaceDir, evidence, recordedAt)
	if err != nil {
		return PublishRefreshResult{}, err
	}
	result, err := appendRefreshEvent(opts.WorkspaceDir, state, evidence, evidencePath, recordedAt, state.Revisions[RefreshResource(opts.MergeUnitID+":"+opts.AttemptID)])
	if err != nil {
		return PublishRefreshResult{}, err
	}
	return PublishRefreshResult{
		Status:            RefreshStatusRemoteBranchMoved,
		WorkspaceDir:      opts.WorkspaceDir,
		WorkspaceID:       state.View.WorkspaceID,
		BaseRef:           state.View.BaseRef,
		MergeUnitID:       opts.MergeUnitID,
		AttemptID:         opts.AttemptID,
		Branch:            branch,
		Worktree:          worktree,
		Remote:            remote,
		HeadSHA:           refresh.PostHead,
		ExpectedRemoteSHA: opts.ExpectedRemoteSHA,
		ObservedRemoteSHA: observedRemoteSHA,
		EvidencePath:      evidencePath,
		EventID:           result.EventID,
		EventHash:         result.EventHash,
	}, nil
}

func forceWithLeasePushCommand(worktree string, remote string, branch string, expectedRemoteSHA string) string {
	return strings.Join([]string{
		"git",
		"-C", shellQuote(worktree),
		"push",
		shellQuote("--force-with-lease=refs/heads/" + branch + ":" + expectedRemoteSHA),
		shellQuote(remote),
		shellQuote("HEAD:refs/heads/" + branch),
	}, " ")
}

func resolveRemoteBranchSHA(worktree string, remote string, branch string) (string, error) {
	output, err := gitOutput(worktree, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		return "", nil
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}
