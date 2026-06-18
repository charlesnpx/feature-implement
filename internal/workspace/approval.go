package workspace

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	EventApprovalGranted  = "approval.granted"
	EventApprovalConsumed = "approval.consumed"

	eventPayloadApprovalIDKey = "approval_id"
	eventPayloadActionsKey    = "actions"
	eventPayloadScopeKey      = "scope"
	eventPayloadPRKey         = "pr"
	eventPayloadHeadSHAKey    = "head_sha"
	eventPayloadMaxUsesKey    = "max_uses"
	eventPayloadExpiresAtKey  = "expires_at"
	eventPayloadUsedCountKey  = "used_count"
)

type ApprovalGrantOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	AgentID      string
	LeaseID      string
	Actions      []string
	Scope        string
	PR           string
	Branch       string
	HeadSHA      string
	BaseSHA      string
	MaxUses      int
	ExpiresIn    time.Duration
	ExpiresAt    time.Time
	Now          func() time.Time
}

type ApprovalCheckOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	Action       string
	Scope        string
	PR           string
	Branch       string
	HeadSHA      string
	BaseSHA      string
	Now          func() time.Time
}

type ApprovalConsumeOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	ApprovalID   string
	Action       string
	Scope        string
	PR           string
	Branch       string
	HeadSHA      string
	BaseSHA      string
	Now          func() time.Time
}

type ApprovalResult struct {
	Status       string       `json:"status"`
	WorkspaceDir string       `json:"workspace_dir"`
	WorkspaceID  string       `json:"workspace_id"`
	BaseRef      string       `json:"base_ref"`
	Approval     ApprovalView `json:"approval"`
	EventID      string       `json:"event_id,omitempty"`
	EventHash    string       `json:"event_hash,omitempty"`
}

type ApprovalCheckResult struct {
	Status       string         `json:"status"`
	WorkspaceDir string         `json:"workspace_dir"`
	WorkspaceID  string         `json:"workspace_id"`
	BaseRef      string         `json:"base_ref"`
	MergeUnitID  string         `json:"merge_unit_id"`
	AttemptID    string         `json:"attempt_id"`
	Action       string         `json:"action"`
	Approvals    []ApprovalView `json:"approvals"`
}

type ApprovalView struct {
	ApprovalID  string   `json:"approval_id"`
	MergeUnitID string   `json:"merge_unit_id"`
	AttemptID   string   `json:"attempt_id"`
	AgentID     string   `json:"agent_id,omitempty"`
	LeaseID     string   `json:"lease_id,omitempty"`
	Actions     []string `json:"actions"`
	Scope       string   `json:"scope"`
	PR          string   `json:"pr,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	HeadSHA     string   `json:"head_sha,omitempty"`
	BaseSHA     string   `json:"base_sha,omitempty"`
	MaxUses     int      `json:"max_uses"`
	UsedCount   int      `json:"used_count"`
	ExpiresAt   string   `json:"expires_at"`
	Status      string   `json:"status"`
	StaleInputs []string `json:"stale_inputs,omitempty"`
}

type approvalSnapshot struct {
	ApprovalID  string
	MergeUnitID string
	AttemptID   string
	AgentID     string
	LeaseID     string
	Actions     []string
	Scope       string
	PR          string
	Branch      string
	HeadSHA     string
	BaseSHA     string
	MaxUses     int
	UsedCount   int
	ExpiresAt   time.Time
}

func ApprovalResource(id string) string {
	return resourceKey("approval", id)
}

func ApprovalAttemptResource(mergeUnitID string, attemptID string) string {
	return resourceKey("approval", mergeUnitID+":"+attemptID+":index")
}

func GrantApproval(opts ApprovalGrantOptions) (ApprovalResult, error) {
	opts, grantedAt, expiresAt, err := normalizeApprovalGrantOptions(opts)
	if err != nil {
		return ApprovalResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, grantedAt)
	if err != nil {
		return ApprovalResult{}, err
	}
	grantedAt = state.ObservedAt
	if opts.ExpiresAt.IsZero() {
		expiresAt = grantedAt.Add(opts.ExpiresIn)
	}
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return ApprovalResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return ApprovalResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return ApprovalResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, grantedAt)
	if err != nil {
		return ApprovalResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return ApprovalResult{}, err
	}
	approvalID := approvalID(opts.MergeUnitID, opts.AttemptID, opts.Actions, opts.Scope, opts.PR, opts.Branch, opts.HeadSHA, opts.BaseSHA, grantedAt)
	resource := ApprovalResource(approvalID)
	attemptResource := ApprovalAttemptResource(opts.MergeUnitID, opts.AttemptID)
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventApprovalGranted,
		Payload: map[string]any{
			eventPayloadApprovalIDKey:  approvalID,
			eventPayloadMergeUnitIDKey: opts.MergeUnitID,
			eventPayloadAttemptIDKey:   opts.AttemptID,
			eventPayloadAgentIDKey:     opts.AgentID,
			eventPayloadLeaseIDKey:     opts.LeaseID,
			eventPayloadActionsKey:     opts.Actions,
			eventPayloadScopeKey:       opts.Scope,
			eventPayloadPRKey:          opts.PR,
			eventPayloadBranchKey:      opts.Branch,
			eventPayloadHeadSHAKey:     opts.HeadSHA,
			eventPayloadBaseSHAKey:     opts.BaseSHA,
			eventPayloadMaxUsesKey:     opts.MaxUses,
			eventPayloadExpiresAtKey:   expiresAt.UTC().Format(time.RFC3339Nano),
		},
		ReadSet: map[string]int{
			LeaseResource(opts.MergeUnitID):     state.Revisions[LeaseResource(opts.MergeUnitID)],
			MergeUnitResource(opts.MergeUnitID): state.Revisions[MergeUnitResource(opts.MergeUnitID)],
			resource:                            state.Revisions[resource],
			attemptResource:                     state.Revisions[attemptResource],
		},
		WriteSet: []string{resource, attemptResource},
		Now:      func() time.Time { return grantedAt },
	})
	if err != nil {
		return ApprovalResult{}, err
	}
	approval := approvalSnapshot{
		ApprovalID:  approvalID,
		MergeUnitID: opts.MergeUnitID,
		AttemptID:   opts.AttemptID,
		AgentID:     opts.AgentID,
		LeaseID:     opts.LeaseID,
		Actions:     opts.Actions,
		Scope:       opts.Scope,
		PR:          opts.PR,
		Branch:      opts.Branch,
		HeadSHA:     opts.HeadSHA,
		BaseSHA:     opts.BaseSHA,
		MaxUses:     opts.MaxUses,
		ExpiresAt:   expiresAt,
	}
	return ApprovalResult{
		Status:       "granted",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		Approval:     approval.View(grantedAt),
		EventID:      event.ID,
		EventHash:    event.EventHash,
	}, nil
}

func CheckApproval(opts ApprovalCheckOptions) (ApprovalCheckResult, error) {
	opts, checkedAt, err := normalizeApprovalCheckOptions(opts)
	if err != nil {
		return ApprovalCheckResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, checkedAt)
	if err != nil {
		return ApprovalCheckResult{}, err
	}
	checkedAt = state.ObservedAt
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return ApprovalCheckResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, checkedAt)
	if err != nil {
		return ApprovalCheckResult{}, err
	}
	if opts.Action == ExternalActionMerge {
		if err := validateCurrentRefreshHead(state.Events, current, "approval check"); err != nil {
			return ApprovalCheckResult{}, err
		}
	}
	approvals, err := approvalSnapshots(state.Events)
	if err != nil {
		return ApprovalCheckResult{}, err
	}
	matches := []ApprovalView{}
	for _, approval := range approvals {
		if err := approvalMatches(approval, approvalMatchRequest{
			mergeUnitID: opts.MergeUnitID,
			attemptID:   opts.AttemptID,
			action:      opts.Action,
			scope:       opts.Scope,
			pr:          opts.PR,
			branch:      opts.Branch,
			headSHA:     opts.HeadSHA,
			baseSHA:     opts.BaseSHA,
			now:         checkedAt,
		}); err == nil && len(approvalStaleInputsFromEvents(state.Events, approval)) == 0 {
			matches = append(matches, approval.View(checkedAt))
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ApprovalID < matches[j].ApprovalID })
	status := "denied"
	if len(matches) > 0 {
		status = "approved"
	}
	return ApprovalCheckResult{
		Status:       status,
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		MergeUnitID:  opts.MergeUnitID,
		AttemptID:    opts.AttemptID,
		Action:       opts.Action,
		Approvals:    matches,
	}, nil
}

func ConsumeApproval(opts ApprovalConsumeOptions) (ApprovalResult, error) {
	opts, consumedAt, err := normalizeApprovalConsumeOptions(opts)
	if err != nil {
		return ApprovalResult{}, err
	}
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return ApprovalResult{}, err
	}
	events, err := readJournalEvents(EventsPath(opts.WorkspaceDir))
	if err != nil {
		return ApprovalResult{}, err
	}
	consumedAt, err = observedAtAfterEvents(events, consumedAt)
	if err != nil {
		return ApprovalResult{}, err
	}
	revisions, err := replayResourceRevisions(events)
	if err != nil {
		return ApprovalResult{}, err
	}
	approvals, err := approvalSnapshots(events)
	if err != nil {
		return ApprovalResult{}, err
	}
	attempts, err := attemptSnapshots(events)
	if err != nil {
		return ApprovalResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, consumedAt)
	if err != nil {
		return ApprovalResult{}, err
	}
	if opts.Action == ExternalActionMerge {
		if err := validateCurrentRefreshHead(events, current, "approval consume"); err != nil {
			return ApprovalResult{}, err
		}
	}
	approval, ok := approvals[opts.ApprovalID]
	if !ok {
		return ApprovalResult{}, fmt.Errorf("approval not found: %s", opts.ApprovalID)
	}
	if err := approvalMatches(approval, approvalMatchRequest{
		mergeUnitID: opts.MergeUnitID,
		attemptID:   opts.AttemptID,
		action:      opts.Action,
		scope:       opts.Scope,
		pr:          opts.PR,
		branch:      opts.Branch,
		headSHA:     opts.HeadSHA,
		baseSHA:     opts.BaseSHA,
		now:         consumedAt,
	}); err != nil {
		return ApprovalResult{}, err
	}
	if staleInputs := approvalStaleInputsFromEvents(events, approval); len(staleInputs) > 0 {
		return ApprovalResult{}, fmt.Errorf("approval %s is stale after refresh changed %s", approval.ApprovalID, strings.Join(staleInputs, ", "))
	}
	resource := ApprovalResource(opts.ApprovalID)
	attemptResource := ApprovalAttemptResource(opts.MergeUnitID, opts.AttemptID)
	mergeUnitResource := MergeUnitResource(opts.MergeUnitID)
	readSet := map[string]int{
		resource:          revisions[resource],
		attemptResource:   revisions[attemptResource],
		mergeUnitResource: revisions[mergeUnitResource],
	}
	addApprovalRefreshInputReadSet(readSet, revisions, approval)
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventApprovalConsumed,
		Payload: map[string]any{
			eventPayloadApprovalIDKey:  opts.ApprovalID,
			eventPayloadMergeUnitIDKey: opts.MergeUnitID,
			eventPayloadAttemptIDKey:   opts.AttemptID,
			eventPayloadActionsKey:     []string{opts.Action},
			eventPayloadScopeKey:       opts.Scope,
			eventPayloadPRKey:          opts.PR,
			eventPayloadBranchKey:      opts.Branch,
			eventPayloadHeadSHAKey:     opts.HeadSHA,
			eventPayloadBaseSHAKey:     opts.BaseSHA,
			eventPayloadUsedCountKey:   approval.UsedCount + 1,
		},
		ReadSet:  readSet,
		WriteSet: []string{resource, attemptResource},
		Now:      func() time.Time { return consumedAt },
	})
	if err != nil {
		return ApprovalResult{}, err
	}
	approval.UsedCount++
	return ApprovalResult{
		Status:       "consumed",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  lock.WorkspaceID,
		BaseRef:      lock.BaseRef,
		Approval:     approval.View(consumedAt),
		EventID:      event.ID,
		EventHash:    event.EventHash,
	}, nil
}

func normalizeApprovalGrantOptions(opts ApprovalGrantOptions) (ApprovalGrantOptions, time.Time, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace approve grant requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace approve grant requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace approve grant requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace approve grant requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace approve grant requires --lease")
	}
	actions, err := normalizeApprovalActions(opts.Actions)
	if err != nil {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, err
	}
	opts.Actions = actions
	opts.Scope = normalizeApprovalScope(opts.Scope)
	opts.PR = normalizeApprovalPR(opts.PR)
	opts.Branch = strings.TrimSpace(opts.Branch)
	opts.HeadSHA = strings.TrimSpace(opts.HeadSHA)
	opts.BaseSHA = strings.TrimSpace(opts.BaseSHA)
	if opts.MaxUses == 0 {
		opts.MaxUses = 1
	}
	if opts.MaxUses < 0 {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("--max-uses must be greater than zero")
	}
	if containsString(opts.Actions, "merge") {
		hasTarget := opts.PR != "" || opts.Branch != ""
		if !hasTarget || opts.HeadSHA == "" || opts.BaseSHA == "" {
			return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("merge approvals require --pr or --branch plus --head-sha and --base-sha")
		}
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	grantedAt := now()
	expiresAt := opts.ExpiresAt
	if expiresAt.IsZero() {
		if opts.ExpiresIn <= 0 {
			return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("workspace approve grant requires --expires-in or --expires-at")
		}
		expiresAt = grantedAt.Add(opts.ExpiresIn)
	}
	if !expiresAt.After(grantedAt) {
		return ApprovalGrantOptions{}, time.Time{}, time.Time{}, fmt.Errorf("approval expiry must be in the future")
	}
	return opts, grantedAt, expiresAt, nil
}

func normalizeApprovalCheckOptions(opts ApprovalCheckOptions) (ApprovalCheckOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ApprovalCheckOptions{}, time.Time{}, fmt.Errorf("workspace approve check requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return ApprovalCheckOptions{}, time.Time{}, fmt.Errorf("workspace approve check requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return ApprovalCheckOptions{}, time.Time{}, fmt.Errorf("workspace approve check requires --attempt")
	}
	opts.Action = strings.TrimSpace(opts.Action)
	if opts.Action == "" {
		return ApprovalCheckOptions{}, time.Time{}, fmt.Errorf("workspace approve check requires --action")
	}
	opts.Scope = normalizeApprovalScope(opts.Scope)
	opts.PR = normalizeApprovalPR(opts.PR)
	opts.Branch = strings.TrimSpace(opts.Branch)
	opts.HeadSHA = strings.TrimSpace(opts.HeadSHA)
	opts.BaseSHA = strings.TrimSpace(opts.BaseSHA)
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func normalizeApprovalConsumeOptions(opts ApprovalConsumeOptions) (ApprovalConsumeOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return ApprovalConsumeOptions{}, time.Time{}, fmt.Errorf("workspace approve consume requires <workspace-dir>")
	}
	opts.ApprovalID = strings.TrimSpace(opts.ApprovalID)
	if opts.ApprovalID == "" {
		return ApprovalConsumeOptions{}, time.Time{}, fmt.Errorf("workspace approve consume requires --approval")
	}
	check, now, err := normalizeApprovalCheckOptions(ApprovalCheckOptions{
		WorkspaceDir: opts.WorkspaceDir,
		MergeUnitID:  opts.MergeUnitID,
		AttemptID:    opts.AttemptID,
		Action:       opts.Action,
		Scope:        opts.Scope,
		PR:           opts.PR,
		Branch:       opts.Branch,
		HeadSHA:      opts.HeadSHA,
		BaseSHA:      opts.BaseSHA,
		Now:          opts.Now,
	})
	if err != nil {
		return ApprovalConsumeOptions{}, time.Time{}, err
	}
	opts.MergeUnitID = check.MergeUnitID
	opts.AttemptID = check.AttemptID
	opts.Action = check.Action
	opts.Scope = check.Scope
	opts.PR = check.PR
	opts.Branch = check.Branch
	opts.HeadSHA = check.HeadSHA
	opts.BaseSHA = check.BaseSHA
	return opts, now, nil
}

func normalizeApprovalActions(values []string) ([]string, error) {
	seen := map[string]bool{}
	actions := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			action := strings.TrimSpace(part)
			if action == "" {
				continue
			}
			if !seen[action] {
				seen[action] = true
				actions = append(actions, action)
			}
		}
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("workspace approve grant requires --action")
	}
	sort.Strings(actions)
	return actions, nil
}

func normalizeApprovalScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "merge-unit"
	}
	return scope
}

func normalizeApprovalPR(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if before, number, ok := strings.Cut(value, "/pull/"); ok && strings.Contains(strings.ToLower(before), "github.com/") {
		number, _, _ = strings.Cut(number, "/")
		number, _, _ = strings.Cut(number, "?")
		number, _, _ = strings.Cut(number, "#")
		if number = strings.TrimSpace(number); number != "" {
			return number
		}
	}
	return value
}

func approvalID(mergeUnitID string, attemptID string, actions []string, scope string, pr string, branch string, headSHA string, baseSHA string, at time.Time) string {
	parts := []string{mergeUnitID, attemptID, strings.Join(actions, "+"), scope, pr, branch, headSHA, baseSHA, fmt.Sprintf("%d", at.UTC().UnixNano())}
	text := strings.Join(parts, "|")
	replacer := strings.NewReplacer(":", "-", "/", "-", "|", "-", " ", "-")
	return "approval-" + replacer.Replace(text)
}

type approvalMatchRequest struct {
	mergeUnitID string
	attemptID   string
	action      string
	scope       string
	pr          string
	branch      string
	headSHA     string
	baseSHA     string
	now         time.Time
}

func approvalMatches(approval approvalSnapshot, req approvalMatchRequest) error {
	if approval.MergeUnitID != req.mergeUnitID {
		return fmt.Errorf("approval %s is for merge unit %s, not %s", approval.ApprovalID, approval.MergeUnitID, req.mergeUnitID)
	}
	if approval.AttemptID != req.attemptID {
		return fmt.Errorf("approval %s is for attempt %s, not %s", approval.ApprovalID, approval.AttemptID, req.attemptID)
	}
	if !containsString(approval.Actions, req.action) {
		return fmt.Errorf("approval %s does not allow action %s", approval.ApprovalID, req.action)
	}
	if approval.Scope != req.scope {
		return fmt.Errorf("approval %s scope is %s, not %s", approval.ApprovalID, approval.Scope, req.scope)
	}
	if req.action == "merge" {
		approvalHasTarget := approval.PR != "" || approval.Branch != ""
		if !approvalHasTarget || approval.HeadSHA == "" || approval.BaseSHA == "" {
			return fmt.Errorf("approval %s is missing required merge target", approval.ApprovalID)
		}
		requestHasTarget := req.pr != "" || req.branch != ""
		if !requestHasTarget || req.headSHA == "" || req.baseSHA == "" {
			return fmt.Errorf("merge approval use requires --pr or --branch plus --head-sha and --base-sha")
		}
	}
	if approval.PR != "" && approval.PR != req.pr {
		return fmt.Errorf("approval %s is for PR %s, not %s", approval.ApprovalID, approval.PR, req.pr)
	}
	if approval.Branch != "" && approval.Branch != req.branch {
		return fmt.Errorf("approval %s is for branch %s, not %s", approval.ApprovalID, approval.Branch, req.branch)
	}
	if approval.HeadSHA != "" && approval.HeadSHA != req.headSHA {
		return fmt.Errorf("approval %s is for head %s, not %s", approval.ApprovalID, approval.HeadSHA, req.headSHA)
	}
	if approval.BaseSHA != "" && approval.BaseSHA != req.baseSHA {
		return fmt.Errorf("approval %s is for base %s, not %s", approval.ApprovalID, approval.BaseSHA, req.baseSHA)
	}
	if !req.now.Before(approval.ExpiresAt) {
		return fmt.Errorf("approval %s expired at %s", approval.ApprovalID, approval.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	if approval.UsedCount >= approval.MaxUses {
		return fmt.Errorf("approval %s has no uses remaining", approval.ApprovalID)
	}
	return nil
}

func isTightMergeApproval(approval approvalSnapshot) bool {
	return containsString(approval.Actions, "merge") &&
		(approval.PR != "" || approval.Branch != "") &&
		approval.HeadSHA != "" &&
		approval.BaseSHA != ""
}

func approvalReadsRefreshInputs(approval approvalSnapshot) bool {
	return approval.HeadSHA != "" || approval.BaseSHA != ""
}

func addApprovalRefreshInputReadSet(readSet map[string]int, revisions map[string]int, approval approvalSnapshot) {
	if !approvalReadsRefreshInputs(approval) {
		return
	}
	for _, input := range []string{refreshInputBase, refreshInputHead} {
		resource := RefreshInputResource(approval.MergeUnitID, approval.AttemptID, input)
		readSet[resource] = revisions[resource]
	}
}

func approvalStaleInputsFromEvents(events []JournalEvent, approval approvalSnapshot) []string {
	if !isTightMergeApproval(approval) {
		return nil
	}
	stale := approvalStaleInputsForValues(approval, latestRefreshInputValues(events, approval.MergeUnitID, approval.AttemptID))
	changedAfterGrant := refreshInputsChangedAfterApproval(events, approval)
	for _, input := range []string{refreshInputBase, refreshInputHead} {
		if changedAfterGrant[input] && !containsString(stale, input) {
			stale = append(stale, input)
		}
	}
	return stale
}

func latestRefreshInputValues(events []JournalEvent, mergeUnitID string, attemptID string) map[string]string {
	values := map[string]string{}
	for _, event := range events {
		if event.Type != EventBranchRefreshRecorded {
			continue
		}
		refresh, err := refreshSnapshotFromEvent(event)
		if err != nil {
			continue
		}
		if refresh.MergeUnitID != mergeUnitID || refresh.AttemptID != attemptID {
			continue
		}
		for _, change := range refresh.InputChanges {
			values[change.Input] = change.NewValue
		}
	}
	return values
}

func approvalStaleInputsForValues(approval approvalSnapshot, inputValues map[string]string) []string {
	if !isTightMergeApproval(approval) {
		return nil
	}
	if len(inputValues) == 0 {
		return nil
	}
	stale := []string{}
	if value, ok := inputValues[refreshInputBase]; ok && approval.BaseSHA != value {
		stale = append(stale, refreshInputBase)
	}
	if value, ok := inputValues[refreshInputHead]; ok && approval.HeadSHA != value {
		stale = append(stale, refreshInputHead)
	}
	return stale
}

func refreshInputsChangedAfterApproval(events []JournalEvent, approval approvalSnapshot) map[string]bool {
	changed := map[string]bool{}
	seenApprovalGrant := false
	for _, event := range events {
		if event.Type == EventApprovalGranted {
			approvalID, err := eventStringPayload(event, eventPayloadApprovalIDKey)
			if err == nil && approvalID == approval.ApprovalID {
				seenApprovalGrant = true
			}
			continue
		}
		if !seenApprovalGrant || event.Type != EventBranchRefreshRecorded {
			continue
		}
		refresh, err := refreshSnapshotFromEvent(event)
		if err != nil {
			continue
		}
		if refresh.MergeUnitID != approval.MergeUnitID || refresh.AttemptID != approval.AttemptID {
			continue
		}
		for _, change := range refresh.InputChanges {
			changed[change.Input] = true
		}
	}
	return changed
}

func approvalSnapshots(events []JournalEvent) (map[string]approvalSnapshot, error) {
	approvals := map[string]approvalSnapshot{}
	for i, event := range events {
		priorEvents := events[:i]
		switch event.Type {
		case EventApprovalGranted:
			approval, err := approvalGrantedFromEvent(event)
			if err != nil {
				return nil, err
			}
			approvals[approval.ApprovalID] = approval
		case EventApprovalConsumed:
			approvalID, err := eventStringPayload(event, eventPayloadApprovalIDKey)
			if err != nil {
				return nil, err
			}
			approval, ok := approvals[approvalID]
			if !ok {
				return nil, fmt.Errorf("approval event %s references unknown approval %s", event.ID, approvalID)
			}
			if err := validateApprovalConsumedEvent(event, approval); err != nil {
				return nil, err
			}
			if err := validateApprovalEventNotStale(event, priorEvents, approval); err != nil {
				return nil, err
			}
			usedCount, err := eventIntPayload(event, eventPayloadUsedCountKey)
			if err != nil {
				return nil, err
			}
			if usedCount != approval.UsedCount+1 {
				return nil, fmt.Errorf("approval event %s payload %s is %d, want %d", event.ID, eventPayloadUsedCountKey, usedCount, approval.UsedCount+1)
			}
			approval.UsedCount++
			approvals[approvalID] = approval
		case EventExternalIntentReserved:
			approvalID, err := eventStringPayload(event, eventPayloadApprovalIDRefKey)
			if err != nil {
				return nil, err
			}
			approvalResource := ApprovalResource(approvalID)
			if !containsString(event.WriteSet, approvalResource) {
				continue
			}
			approval, ok := approvals[approvalID]
			if !ok {
				return nil, fmt.Errorf("approval event %s references unknown approval %s", event.ID, approvalID)
			}
			if err := validateExternalIntentApprovalConsumptionEvent(event, approval); err != nil {
				return nil, err
			}
			if err := validateApprovalEventNotStale(event, priorEvents, approval); err != nil {
				return nil, err
			}
			usedCount, err := eventIntPayload(event, eventPayloadUsedCountKey)
			if err != nil {
				return nil, err
			}
			if usedCount != approval.UsedCount+1 {
				return nil, fmt.Errorf("approval event %s payload %s is %d, want %d", event.ID, eventPayloadUsedCountKey, usedCount, approval.UsedCount+1)
			}
			approval.UsedCount++
			approvals[approvalID] = approval
		}
	}
	return approvals, nil
}

func validateApprovalEventNotStale(event JournalEvent, priorEvents []JournalEvent, approval approvalSnapshot) error {
	staleInputs := approvalStaleInputsFromEvents(priorEvents, approval)
	if len(staleInputs) == 0 {
		return nil
	}
	return fmt.Errorf("approval event %s consumes stale approval %s after refresh changed %s", event.ID, approval.ApprovalID, strings.Join(staleInputs, ", "))
}

func validateApprovalConsumedEvent(event JournalEvent, approval approvalSnapshot) error {
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return err
	}
	actions, err := eventStringSlicePayload(event, eventPayloadActionsKey)
	if err != nil {
		return err
	}
	if len(actions) != 1 {
		return fmt.Errorf("approval event %s payload %s must contain exactly one action", event.ID, eventPayloadActionsKey)
	}
	scope, err := eventStringPayload(event, eventPayloadScopeKey)
	if err != nil {
		return err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return fmt.Errorf("approval event %s timestamp must be RFC3339: %w", event.ID, err)
	}
	return approvalMatches(approval, approvalMatchRequest{
		mergeUnitID: mergeUnitID,
		attemptID:   attemptID,
		action:      actions[0],
		scope:       scope,
		pr:          normalizeApprovalPR(optionalStringPayload(event, eventPayloadPRKey)),
		branch:      optionalStringPayload(event, eventPayloadBranchKey),
		headSHA:     optionalStringPayload(event, eventPayloadHeadSHAKey),
		baseSHA:     optionalStringPayload(event, eventPayloadBaseSHAKey),
		now:         occurredAt,
	})
}

func validateExternalIntentApprovalConsumptionEvent(event JournalEvent, approval approvalSnapshot) error {
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return err
	}
	action, err := eventStringPayload(event, eventPayloadActionKey)
	if err != nil {
		return err
	}
	scope, err := eventStringPayload(event, eventPayloadScopeKey)
	if err != nil {
		return err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return fmt.Errorf("approval event %s timestamp must be RFC3339: %w", event.ID, err)
	}
	return approvalMatches(approval, approvalMatchRequest{
		mergeUnitID: mergeUnitID,
		attemptID:   attemptID,
		action:      action,
		scope:       scope,
		pr:          normalizeApprovalPR(optionalStringPayload(event, eventPayloadPRKey)),
		branch:      optionalStringPayload(event, eventPayloadBranchKey),
		headSHA:     optionalStringPayload(event, eventPayloadRequestedHeadSHAKey),
		baseSHA:     optionalStringPayload(event, eventPayloadExpectedBaseSHAKey),
		now:         occurredAt,
	})
}

func approvalGrantedFromEvent(event JournalEvent) (approvalSnapshot, error) {
	approvalID, err := eventStringPayload(event, eventPayloadApprovalIDKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	agentID, err := eventStringPayload(event, eventPayloadAgentIDKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	leaseID, err := eventStringPayload(event, eventPayloadLeaseIDKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	actions, err := eventStringSlicePayload(event, eventPayloadActionsKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	scope, err := eventStringPayload(event, eventPayloadScopeKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	maxUses, err := eventIntPayload(event, eventPayloadMaxUsesKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	expiresAtText, err := eventStringPayload(event, eventPayloadExpiresAtKey)
	if err != nil {
		return approvalSnapshot{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
	if err != nil {
		return approvalSnapshot{}, fmt.Errorf("scheduler event %s payload %s must be RFC3339: %w", event.ID, eventPayloadExpiresAtKey, err)
	}
	return approvalSnapshot{
		ApprovalID:  approvalID,
		MergeUnitID: mergeUnitID,
		AttemptID:   attemptID,
		AgentID:     agentID,
		LeaseID:     leaseID,
		Actions:     actions,
		Scope:       scope,
		PR:          optionalStringPayload(event, eventPayloadPRKey),
		Branch:      optionalStringPayload(event, eventPayloadBranchKey),
		HeadSHA:     optionalStringPayload(event, eventPayloadHeadSHAKey),
		BaseSHA:     optionalStringPayload(event, eventPayloadBaseSHAKey),
		MaxUses:     maxUses,
		ExpiresAt:   expiresAt,
	}, nil
}

func eventStringSlicePayload(event JournalEvent, key string) ([]string, error) {
	value, ok := event.Payload[key]
	if !ok {
		return nil, fmt.Errorf("scheduler event %s missing payload %s", event.ID, key)
	}
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("scheduler event %s payload %s must be a non-empty list", event.ID, key)
	}
	values := make([]string, 0, len(raw))
	for i, item := range raw {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("scheduler event %s payload %s item %d must be a string", event.ID, key, i+1)
		}
		values = append(values, text)
	}
	sort.Strings(values)
	return values, nil
}

func optionalStringPayload(event JournalEvent, key string) string {
	value, ok := event.Payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

func (approval approvalSnapshot) View(now time.Time) ApprovalView {
	return approval.ViewWithStaleInputs(now, nil)
}

func (approval approvalSnapshot) ViewWithStaleInputs(now time.Time, staleInputs []string) ApprovalView {
	status := "active"
	if !now.Before(approval.ExpiresAt) {
		status = "expired"
	} else if approval.UsedCount >= approval.MaxUses {
		status = "exhausted"
	} else if len(staleInputs) > 0 {
		status = "stale"
	}
	return ApprovalView{
		ApprovalID:  approval.ApprovalID,
		MergeUnitID: approval.MergeUnitID,
		AttemptID:   approval.AttemptID,
		AgentID:     approval.AgentID,
		LeaseID:     approval.LeaseID,
		Actions:     append([]string{}, approval.Actions...),
		Scope:       approval.Scope,
		PR:          approval.PR,
		Branch:      approval.Branch,
		HeadSHA:     approval.HeadSHA,
		BaseSHA:     approval.BaseSHA,
		MaxUses:     approval.MaxUses,
		UsedCount:   approval.UsedCount,
		ExpiresAt:   approval.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Status:      status,
		StaleInputs: append([]string{}, staleInputs...),
	}
}
