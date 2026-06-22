package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	EventBranchRefreshRecorded = "branch_refresh.recorded"

	RefreshStatusSucceeded          = "refreshed"
	RefreshStatusVerificationFailed = "verification_failed"
	RefreshStatusRemoteBranchMoved  = "remote_branch_moved"

	refreshAppendMaxAttempts = 3

	eventPayloadOldBaseKey      = "old_base"
	eventPayloadNewBaseKey      = "new_base"
	eventPayloadPreHeadKey      = "pre_head"
	eventPayloadPostHeadKey     = "post_head"
	eventPayloadBackupRefKey    = "backup_ref"
	eventPayloadEvidencePathKey = "evidence_path"
	eventPayloadInputChangesKey = "input_changes"

	refreshInputBase = "base"
	refreshInputHead = "head"

	refreshConditionStaleHead = "stale_refresh_head"
)

type RefreshBranchOptions struct {
	WorkspaceDir   string
	MergeUnitID    string
	AttemptID      string
	AgentID        string
	LeaseID        string
	Local          bool
	Worktree       string
	NewBase        string
	BackupRef      string
	CommandResults []ContractCommandResult
	Now            func() time.Time
}

type RefreshBranchResult struct {
	Status       string          `json:"status"`
	WorkspaceDir string          `json:"workspace_dir"`
	WorkspaceID  string          `json:"workspace_id"`
	BaseRef      string          `json:"base_ref"`
	MergeUnitID  string          `json:"merge_unit_id"`
	AttemptID    string          `json:"attempt_id"`
	Branch       string          `json:"branch"`
	Worktree     string          `json:"worktree"`
	EvidencePath string          `json:"evidence_path"`
	Evidence     RefreshEvidence `json:"evidence"`
	EventID      string          `json:"event_id,omitempty"`
	EventHash    string          `json:"event_hash,omitempty"`
}

type RefreshEvidence struct {
	SchemaVersion      int                     `json:"schema_version"`
	WorkspaceID        string                  `json:"workspace_id"`
	BaseRef            string                  `json:"base_ref"`
	MergeUnitID        string                  `json:"merge_unit_id"`
	AttemptID          string                  `json:"attempt_id"`
	AgentID            string                  `json:"agent_id"`
	LeaseID            string                  `json:"lease_id"`
	Local              bool                    `json:"local"`
	Branch             string                  `json:"branch"`
	Worktree           string                  `json:"worktree"`
	OldBase            string                  `json:"old_base"`
	NewBase            string                  `json:"new_base"`
	PreHead            string                  `json:"pre_head"`
	PostHead           string                  `json:"post_head"`
	BackupRef          string                  `json:"backup_ref"`
	ChangedFilesBefore []string                `json:"changed_files_before"`
	ChangedFilesAfter  []string                `json:"changed_files_after"`
	PatchIDsBefore     []RefreshPatchID        `json:"patch_ids_before"`
	PatchIDsAfter      []RefreshPatchID        `json:"patch_ids_after"`
	InputChanges       []RefreshInputChange    `json:"input_changes,omitempty"`
	CommandResults     []ContractCommandResult `json:"command_results,omitempty"`
	Verification       RefreshVerification     `json:"verification"`
}

type RefreshInputChange struct {
	Input    string `json:"input"`
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
	Resource string `json:"resource"`
}

type RefreshPatchID struct {
	PatchID string `json:"patch_id"`
	Commit  string `json:"commit"`
}

type RefreshVerification struct {
	Status                string `json:"status"`
	ChangedFilesPreserved bool   `json:"changed_files_preserved"`
	PatchIDsPreserved     bool   `json:"patch_ids_preserved"`
	FailureReason         string `json:"failure_reason,omitempty"`
}

type RefreshVerificationError struct {
	Result RefreshBranchResult
}

type refreshSnapshot struct {
	MergeUnitID  string
	AttemptID    string
	Status       string
	Resource     string
	Branch       string
	Worktree     string
	OldBase      string
	NewBase      string
	PreHead      string
	PostHead     string
	BackupRef    string
	EvidencePath string
	InputChanges []RefreshInputChange
}

type refreshTracker struct {
	latestByMergeUnit map[string]refreshSnapshot
}

func (e RefreshVerificationError) Error() string {
	reason := strings.TrimSpace(e.Result.Evidence.Verification.FailureReason)
	if reason == "" {
		reason = "contribution preservation checks failed"
	}
	return "refresh verification failed: " + reason
}

func RefreshResource(id string) string {
	return resourceKey("refresh", id)
}

func RefreshInputResource(mergeUnitID string, attemptID string, input string) string {
	return RefreshResource(mergeUnitID + ":" + attemptID + ":input:" + input)
}

func newRefreshTracker() *refreshTracker {
	return &refreshTracker{latestByMergeUnit: map[string]refreshSnapshot{}}
}

func refreshTrackerFromEvents(events []JournalEvent) (*refreshTracker, error) {
	tracker := newRefreshTracker()
	for _, event := range events {
		if event.Type != EventBranchRefreshRecorded {
			continue
		}
		if err := tracker.Apply(event); err != nil {
			return nil, err
		}
	}
	return tracker, nil
}

func (t *refreshTracker) Apply(event JournalEvent) error {
	snapshot, err := refreshSnapshotFromEvent(event)
	if err != nil {
		return err
	}
	t.latestByMergeUnit[snapshot.MergeUnitID] = snapshot
	return nil
}

func (t *refreshTracker) Conditions(mergeUnitID string, attemptID string) []SchedulerBlockingCondition {
	snapshot, ok := t.latestByMergeUnit[mergeUnitID]
	if !ok {
		return nil
	}
	if attemptID == "" || snapshot.AttemptID != attemptID {
		return nil
	}
	conditionType := ""
	requiredAction := ""
	switch snapshot.Status {
	case RefreshStatusVerificationFailed:
		conditionType = "refresh_verification_failed"
		requiredAction = "rerun_local_refresh"
	case RefreshStatusRemoteBranchMoved:
		conditionType = RefreshStatusRemoteBranchMoved
		requiredAction = "rerun_local_refresh"
	default:
		return nil
	}
	return []SchedulerBlockingCondition{{
		Type:           conditionType,
		Resource:       snapshot.Resource,
		AttemptID:      snapshot.AttemptID,
		Status:         snapshot.Status,
		RequiredAction: requiredAction,
		EvidencePath:   snapshot.EvidencePath,
	}}
}

func validateRefreshConditionsClear(operation string, mergeUnitID string, attemptID string, refreshes *refreshTracker) error {
	if refreshes == nil {
		return nil
	}
	blocked := refreshes.Conditions(mergeUnitID, attemptID)
	if len(blocked) == 0 {
		return nil
	}
	condition := blocked[0]
	return fmt.Errorf("%s blocked by %s %s; requires %s", operation, condition.Type, condition.Resource, condition.RequiredAction)
}

func RefreshBranch(opts RefreshBranchOptions) (RefreshBranchResult, error) {
	opts, refreshedAt, err := normalizeRefreshBranchOptions(opts)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, refreshedAt)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	refreshedAt = state.ObservedAt
	lease, _, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return RefreshBranchResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, refreshedAt)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return RefreshBranchResult{}, err
	}
	worktree := opts.Worktree
	if worktree == "" {
		worktree = current.Worktree
	}
	if worktree != current.Worktree {
		return RefreshBranchResult{}, fmt.Errorf("refresh worktree %s does not match current attempt worktree %s", worktree, current.Worktree)
	}
	commandResults, err := normalizeRefreshCommandResults(opts.CommandResults)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	refreshResource := RefreshResource(opts.MergeUnitID + ":" + opts.AttemptID)
	originalRefreshRevision := state.Revisions[refreshResource]
	if err := validateResourcesNotFrozen(state.Events, state.ActiveLeases, []string{MergeUnitResource(opts.MergeUnitID)}, "workspace refresh-branch"); err != nil {
		return RefreshBranchResult{}, err
	}
	evidence, err := runLocalRefresh(opts, state.View.WorkspaceID, state.View.BaseRef, state.Events, current, worktree, commandResults, refreshedAt)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	evidencePath, err := writeRefreshEvidence(opts.WorkspaceDir, evidence, refreshedAt)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	result, err := appendRefreshEventAfterMutation(opts.WorkspaceDir, evidence, evidencePath, refreshedAt, originalRefreshRevision, opts.Now)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	if evidence.Verification.Status == RefreshStatusVerificationFailed {
		return result, RefreshVerificationError{Result: result}
	}
	return result, nil
}

func normalizeRefreshBranchOptions(opts RefreshBranchOptions) (RefreshBranchOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return RefreshBranchOptions{}, time.Time{}, fmt.Errorf("workspace refresh-branch requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return RefreshBranchOptions{}, time.Time{}, fmt.Errorf("workspace refresh-branch requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return RefreshBranchOptions{}, time.Time{}, fmt.Errorf("workspace refresh-branch requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return RefreshBranchOptions{}, time.Time{}, fmt.Errorf("workspace refresh-branch requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return RefreshBranchOptions{}, time.Time{}, fmt.Errorf("workspace refresh-branch requires --lease")
	}
	if !opts.Local {
		return RefreshBranchOptions{}, time.Time{}, fmt.Errorf("workspace refresh-branch currently requires --local")
	}
	opts.Worktree = strings.TrimSpace(opts.Worktree)
	opts.NewBase = strings.TrimSpace(opts.NewBase)
	if opts.NewBase == "" {
		return RefreshBranchOptions{}, time.Time{}, fmt.Errorf("workspace refresh-branch requires --new-base")
	}
	opts.BackupRef = strings.TrimSpace(opts.BackupRef)
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func refreshSnapshotFromEvent(event JournalEvent) (refreshSnapshot, error) {
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	status, err := eventStringPayload(event, eventPayloadStatusKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	switch status {
	case RefreshStatusSucceeded, RefreshStatusVerificationFailed, RefreshStatusRemoteBranchMoved:
	default:
		return refreshSnapshot{}, fmt.Errorf("refresh event %s has unsupported status %s", event.ID, status)
	}
	evidencePath, err := eventStringPayload(event, eventPayloadEvidencePathKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	resource := RefreshResource(mergeUnitID + ":" + attemptID)
	if !containsString(event.WriteSet, resource) {
		return refreshSnapshot{}, fmt.Errorf("refresh event %s missing write_set resource %s", event.ID, resource)
	}
	branch, err := eventStringPayload(event, eventPayloadBranchKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	worktree, err := eventStringPayload(event, eventPayloadWorktreeKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	oldBase, err := eventStringPayload(event, eventPayloadOldBaseKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	newBase, err := eventStringPayload(event, eventPayloadNewBaseKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	preHead, err := eventStringPayload(event, eventPayloadPreHeadKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	postHead, err := eventStringPayload(event, eventPayloadPostHeadKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	backupRef, err := eventStringPayload(event, eventPayloadBackupRefKey)
	if err != nil {
		return refreshSnapshot{}, err
	}
	inputChanges, found, err := eventRefreshInputChangesPayload(event, mergeUnitID, attemptID)
	if err != nil {
		return refreshSnapshot{}, err
	}
	if !found {
		inputChanges = refreshInputChangesFromValues(status, mergeUnitID, attemptID, oldBase, newBase, preHead, postHead)
	}
	return refreshSnapshot{
		MergeUnitID:  mergeUnitID,
		AttemptID:    attemptID,
		Status:       status,
		Resource:     resource,
		Branch:       branch,
		Worktree:     worktree,
		OldBase:      oldBase,
		NewBase:      newBase,
		PreHead:      preHead,
		PostHead:     postHead,
		BackupRef:    backupRef,
		EvidencePath: evidencePath,
		InputChanges: inputChanges,
	}, nil
}

func eventRefreshInputChangesPayload(event JournalEvent, mergeUnitID string, attemptID string) ([]RefreshInputChange, bool, error) {
	value, ok := event.Payload[eventPayloadInputChangesKey]
	if !ok {
		return nil, false, nil
	}
	switch raw := value.(type) {
	case []RefreshInputChange:
		changes := append([]RefreshInputChange(nil), raw...)
		for i, change := range changes {
			if err := validateRefreshInputChangeForAttempt(change, mergeUnitID, attemptID); err != nil {
				return nil, true, fmt.Errorf("scheduler event %s payload %s item %d: %w", event.ID, eventPayloadInputChangesKey, i+1, err)
			}
		}
		sort.Slice(changes, func(i, j int) bool { return changes[i].Input < changes[j].Input })
		return changes, true, nil
	case []any:
		changes := make([]RefreshInputChange, 0, len(raw))
		for i, item := range raw {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, true, fmt.Errorf("scheduler event %s payload %s item %d must be an object", event.ID, eventPayloadInputChangesKey, i+1)
			}
			change := RefreshInputChange{
				Input:    stringMapValue(entry, "input"),
				OldValue: stringMapValue(entry, "old_value"),
				NewValue: stringMapValue(entry, "new_value"),
				Resource: stringMapValue(entry, "resource"),
			}
			if err := validateRefreshInputChangeForAttempt(change, mergeUnitID, attemptID); err != nil {
				return nil, true, fmt.Errorf("scheduler event %s payload %s item %d: %w", event.ID, eventPayloadInputChangesKey, i+1, err)
			}
			changes = append(changes, change)
		}
		sort.Slice(changes, func(i, j int) bool { return changes[i].Input < changes[j].Input })
		return changes, true, nil
	default:
		return nil, true, fmt.Errorf("scheduler event %s payload %s must be a list", event.ID, eventPayloadInputChangesKey)
	}
}

func stringMapValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func normalizeRefreshInputChanges(evidence RefreshEvidence) []RefreshInputChange {
	if len(evidence.InputChanges) == 0 {
		return refreshInputChangesFromValues(
			evidence.Verification.Status,
			evidence.MergeUnitID,
			evidence.AttemptID,
			evidence.OldBase,
			evidence.NewBase,
			evidence.PreHead,
			evidence.PostHead,
		)
	}
	changes := append([]RefreshInputChange(nil), evidence.InputChanges...)
	for i, change := range changes {
		change.Input = strings.TrimSpace(change.Input)
		change.OldValue = strings.TrimSpace(change.OldValue)
		change.NewValue = strings.TrimSpace(change.NewValue)
		change.Resource = strings.TrimSpace(change.Resource)
		if change.Resource == "" && change.Input != "" {
			change.Resource = RefreshInputResource(evidence.MergeUnitID, evidence.AttemptID, change.Input)
		}
		changes[i] = change
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Input < changes[j].Input })
	return changes
}

func refreshInputChangesFromValues(status string, mergeUnitID string, attemptID string, oldBase string, newBase string, preHead string, postHead string) []RefreshInputChange {
	switch status {
	case RefreshStatusSucceeded, RefreshStatusVerificationFailed:
	default:
		return nil
	}
	changes := []RefreshInputChange{}
	if oldBase != "" && newBase != "" && oldBase != newBase {
		changes = append(changes, RefreshInputChange{
			Input:    refreshInputBase,
			OldValue: oldBase,
			NewValue: newBase,
			Resource: RefreshInputResource(mergeUnitID, attemptID, refreshInputBase),
		})
	}
	if preHead != "" && postHead != "" && preHead != postHead {
		changes = append(changes, RefreshInputChange{
			Input:    refreshInputHead,
			OldValue: preHead,
			NewValue: postHead,
			Resource: RefreshInputResource(mergeUnitID, attemptID, refreshInputHead),
		})
	}
	return changes
}

func refreshInputChangeResources(changes []RefreshInputChange) []string {
	resources := make([]string, 0, len(changes))
	seen := map[string]bool{}
	for _, change := range changes {
		if change.Resource == "" || seen[change.Resource] {
			continue
		}
		seen[change.Resource] = true
		resources = append(resources, change.Resource)
	}
	sort.Strings(resources)
	return resources
}

func refreshInputChangesPayload(changes []RefreshInputChange) []any {
	payload := make([]any, 0, len(changes))
	for _, change := range changes {
		payload = append(payload, map[string]any{
			"input":     change.Input,
			"old_value": change.OldValue,
			"new_value": change.NewValue,
			"resource":  change.Resource,
		})
	}
	return payload
}

func validateRefreshInputChange(change RefreshInputChange) error {
	switch change.Input {
	case refreshInputBase, refreshInputHead:
	default:
		return fmt.Errorf("unsupported refresh input %q", change.Input)
	}
	if change.OldValue == "" || change.NewValue == "" {
		return fmt.Errorf("refresh input %s requires old_value and new_value", change.Input)
	}
	wantSuffix := ":input:" + change.Input
	if change.Resource == "" || !strings.HasPrefix(change.Resource, "refresh:") || !strings.HasSuffix(change.Resource, wantSuffix) {
		return fmt.Errorf("refresh input %s resource %q must be a refresh resource ending with %q", change.Input, change.Resource, wantSuffix)
	}
	if err := validateResourceKey(change.Resource); err != nil {
		return err
	}
	return nil
}

func validateRefreshInputChangeForAttempt(change RefreshInputChange, mergeUnitID string, attemptID string) error {
	if err := validateRefreshInputChange(change); err != nil {
		return err
	}
	wantResource := RefreshInputResource(mergeUnitID, attemptID, change.Input)
	if change.Resource != wantResource {
		return fmt.Errorf("refresh input %s resource %q must be %q", change.Input, change.Resource, wantResource)
	}
	return nil
}

func latestRefresh(events []JournalEvent, mergeUnitID string, attemptID string) (refreshSnapshot, bool) {
	var latest refreshSnapshot
	found := false
	for _, event := range events {
		if event.Type != EventBranchRefreshRecorded {
			continue
		}
		snapshot, err := refreshSnapshotFromEvent(event)
		if err != nil {
			continue
		}
		if snapshot.MergeUnitID != mergeUnitID || snapshot.AttemptID != attemptID {
			continue
		}
		latest = snapshot
		found = true
	}
	return latest, found
}

type refreshHeadState struct {
	Refresh      refreshSnapshot
	Worktree     string
	ExpectedHead string
	ObservedHead string
}

func currentRefreshHeadCondition(workspaceDir string, events []JournalEvent, attempt attemptSnapshot) (SchedulerBlockingCondition, bool, error) {
	state, stale, err := currentRefreshHeadState(workspaceDir, events, attempt)
	if err != nil || !stale {
		return SchedulerBlockingCondition{}, false, err
	}
	return SchedulerBlockingCondition{
		Type:           refreshConditionStaleHead,
		Resource:       state.Refresh.Resource,
		AttemptID:      attempt.AttemptID,
		Status:         "stale",
		RequiredAction: mergeQueueRequiredActionRefresh,
		EvidencePath:   state.Refresh.EvidencePath,
	}, true, nil
}

func validateCurrentRefreshHead(workspaceDir string, events []JournalEvent, attempt attemptSnapshot, operation string) error {
	state, stale, err := currentRefreshHeadState(workspaceDir, events, attempt)
	if err != nil {
		return fmt.Errorf("%s could not inspect current refresh head evidence: %w", operation, err)
	}
	if !stale {
		return nil
	}
	return fmt.Errorf("%s blocked by stale refresh head evidence %s: worktree %s HEAD is %s, refresh post_head is %s; requires %s", operation, state.Refresh.Resource, state.Worktree, state.ObservedHead, state.ExpectedHead, mergeQueueRequiredActionRefresh)
}

func currentRefreshHeadState(workspaceDir string, events []JournalEvent, attempt attemptSnapshot) (refreshHeadState, bool, error) {
	refresh, ok := latestRefresh(events, attempt.MergeUnitID, attempt.AttemptID)
	if !ok || refresh.Status != RefreshStatusSucceeded {
		return refreshHeadState{}, false, nil
	}
	worktree := strings.TrimSpace(refresh.Worktree)
	if worktree == "" {
		worktree = strings.TrimSpace(attempt.Worktree)
	}
	expectedHead := strings.TrimSpace(refresh.PostHead)
	if expectedHead == "" {
		return refreshHeadState{}, false, nil
	}
	observedHead, available, err := observedWorktreeHead(worktree)
	if err != nil {
		return refreshHeadState{}, false, err
	}
	if !available {
		recorded, err := refreshEvidenceFileExists(workspaceDir, refresh.EvidencePath)
		if err != nil {
			return refreshHeadState{}, false, err
		}
		if !recorded {
			return refreshHeadState{}, false, nil
		}
		return refreshHeadState{
			Refresh:      refresh,
			Worktree:     worktree,
			ExpectedHead: expectedHead,
			ObservedHead: "<unavailable>",
		}, true, nil
	}
	if observedHead == expectedHead {
		return refreshHeadState{}, false, nil
	}
	return refreshHeadState{
		Refresh:      refresh,
		Worktree:     worktree,
		ExpectedHead: expectedHead,
		ObservedHead: observedHead,
	}, true, nil
}

func refreshEvidenceFileExists(workspaceDir string, evidencePath string) (bool, error) {
	workspaceDir = strings.TrimSpace(workspaceDir)
	evidencePath = strings.TrimSpace(evidencePath)
	if workspaceDir == "" || evidencePath == "" {
		return false, nil
	}
	path := evidencePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceDir, path)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func observedWorktreeHead(worktree string) (string, bool, error) {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return "", false, nil
	}
	info, err := os.Stat(worktree)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("worktree path %s is not a directory", worktree)
	}
	inside, err := gitOutput(worktree, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return "", false, nil
	}
	head, err := gitOutput(worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", true, err
	}
	return strings.TrimSpace(head), true, nil
}

func normalizeRefreshCommandResults(values []ContractCommandResult) ([]ContractCommandResult, error) {
	results := make([]ContractCommandResult, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		command := strings.TrimSpace(value.Command)
		status := strings.TrimSpace(value.Status)
		if command == "" || status == "" {
			return nil, fmt.Errorf("refresh command results require non-empty command and status")
		}
		if seen[command] {
			return nil, fmt.Errorf("duplicate refresh command result for %q", command)
		}
		seen[command] = true
		results = append(results, ContractCommandResult{Command: command, Status: status})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Command < results[j].Command })
	return results, nil
}

func runLocalRefresh(opts RefreshBranchOptions, workspaceID string, baseRef string, events []JournalEvent, attempt attemptSnapshot, worktree string, commandResults []ContractCommandResult, refreshedAt time.Time) (RefreshEvidence, error) {
	if dirty, err := gitOutput(worktree, "status", "--porcelain"); err != nil {
		return RefreshEvidence{}, err
	} else if strings.TrimSpace(dirty) != "" {
		return RefreshEvidence{}, fmt.Errorf("refresh worktree is dirty")
	}
	branch, err := gitOutput(worktree, "branch", "--show-current")
	if err != nil {
		return RefreshEvidence{}, err
	}
	branch = strings.TrimSpace(branch)
	if branch != attempt.Branch {
		return RefreshEvidence{}, fmt.Errorf("refresh branch %s does not match current attempt branch %s", branch, attempt.Branch)
	}
	upstream, err := gitOutput(worktree, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err == nil && strings.TrimSpace(upstream) != "" {
		return RefreshEvidence{}, fmt.Errorf("local refresh requires an unpublished branch; %s tracks %s", branch, strings.TrimSpace(upstream))
	}
	if remoteRef, err := remoteTrackingRef(worktree, branch); err != nil {
		return RefreshEvidence{}, err
	} else if remoteRef != "" {
		return RefreshEvidence{}, fmt.Errorf("local refresh requires an unpublished branch; %s has remote ref %s", branch, remoteRef)
	}
	preHead, err := gitOutput(worktree, "rev-parse", "HEAD")
	if err != nil {
		return RefreshEvidence{}, err
	}
	preHead = strings.TrimSpace(preHead)
	oldBase := attempt.BaseSHA
	if latest, ok := latestRefresh(events, opts.MergeUnitID, opts.AttemptID); ok {
		oldBase = latest.NewBase
	}
	newBase, err := gitOutput(worktree, "rev-parse", opts.NewBase)
	if err != nil {
		return RefreshEvidence{}, err
	}
	newBase = strings.TrimSpace(newBase)
	backupRef := opts.BackupRef
	if backupRef == "" {
		backupRef = defaultRefreshBackupRef(branch, refreshedAt)
	}
	if err := validateRefreshBackupRef(worktree, backupRef); err != nil {
		return RefreshEvidence{}, err
	}
	beforeFiles, err := changedFiles(worktree, oldBase, preHead)
	if err != nil {
		return RefreshEvidence{}, err
	}
	beforePatchIDs, err := patchIDs(worktree, oldBase, preHead)
	if err != nil {
		return RefreshEvidence{}, err
	}
	if _, err := gitOutput(worktree, "branch", "--", backupRef, branch); err != nil {
		return RefreshEvidence{}, err
	}
	if _, err := gitOutput(worktree, "rebase", "--onto", newBase, oldBase, branch); err != nil {
		postHead := preHead
		if head, headErr := gitOutput(worktree, "rev-parse", "HEAD"); headErr == nil {
			postHead = strings.TrimSpace(head)
		}
		return failedRefreshEvidence(opts, workspaceID, baseRef, branch, worktree, oldBase, newBase, preHead, postHead, backupRef, beforeFiles, beforePatchIDs, commandResults, "rebase failed: "+err.Error()), nil
	}
	postHead, err := gitOutput(worktree, "rev-parse", "HEAD")
	if err != nil {
		return RefreshEvidence{}, err
	}
	postHead = strings.TrimSpace(postHead)
	afterFiles, err := changedFiles(worktree, newBase, postHead)
	if err != nil {
		return RefreshEvidence{}, err
	}
	afterPatchIDs, err := patchIDs(worktree, newBase, postHead)
	if err != nil {
		return RefreshEvidence{}, err
	}
	verification := verifyRefreshContribution(beforeFiles, afterFiles, beforePatchIDs, afterPatchIDs, commandResults)
	return RefreshEvidence{
		SchemaVersion:      1,
		WorkspaceID:        workspaceID,
		BaseRef:            baseRef,
		MergeUnitID:        opts.MergeUnitID,
		AttemptID:          opts.AttemptID,
		AgentID:            opts.AgentID,
		LeaseID:            opts.LeaseID,
		Local:              true,
		Branch:             branch,
		Worktree:           worktree,
		OldBase:            oldBase,
		NewBase:            newBase,
		PreHead:            preHead,
		PostHead:           postHead,
		BackupRef:          backupRef,
		ChangedFilesBefore: beforeFiles,
		ChangedFilesAfter:  afterFiles,
		PatchIDsBefore:     beforePatchIDs,
		PatchIDsAfter:      afterPatchIDs,
		InputChanges:       refreshInputChangesFromValues(RefreshStatusSucceeded, opts.MergeUnitID, opts.AttemptID, oldBase, newBase, preHead, postHead),
		CommandResults:     commandResults,
		Verification:       verification,
	}, nil
}

func failedRefreshEvidence(opts RefreshBranchOptions, workspaceID string, baseRef string, branch string, worktree string, oldBase string, newBase string, preHead string, postHead string, backupRef string, beforeFiles []string, beforePatchIDs []RefreshPatchID, commandResults []ContractCommandResult, reason string) RefreshEvidence {
	return RefreshEvidence{
		SchemaVersion:      1,
		WorkspaceID:        workspaceID,
		BaseRef:            baseRef,
		MergeUnitID:        opts.MergeUnitID,
		AttemptID:          opts.AttemptID,
		AgentID:            opts.AgentID,
		LeaseID:            opts.LeaseID,
		Local:              true,
		Branch:             branch,
		Worktree:           worktree,
		OldBase:            oldBase,
		NewBase:            newBase,
		PreHead:            preHead,
		PostHead:           postHead,
		BackupRef:          backupRef,
		ChangedFilesBefore: append([]string(nil), beforeFiles...),
		PatchIDsBefore:     append([]RefreshPatchID(nil), beforePatchIDs...),
		InputChanges:       refreshInputChangesFromValues(RefreshStatusVerificationFailed, opts.MergeUnitID, opts.AttemptID, oldBase, newBase, preHead, postHead),
		CommandResults:     commandResults,
		Verification: RefreshVerification{
			Status:        RefreshStatusVerificationFailed,
			FailureReason: strings.TrimSpace(reason),
		},
	}
}

func changedFiles(worktree string, base string, head string) ([]string, error) {
	output, err := gitOutput(worktree, "diff", "--name-status", base+"..."+head)
	if err != nil {
		return nil, err
	}
	lines := nonEmptyLines(output)
	sort.Strings(lines)
	return lines, nil
}

func patchIDs(worktree string, base string, head string) ([]RefreshPatchID, error) {
	output, err := gitOutput(worktree, "log", "--reverse", "--format=%H", base+".."+head)
	if err != nil {
		return nil, err
	}
	commits := nonEmptyLines(output)
	ids := make([]RefreshPatchID, 0, len(commits))
	for _, commit := range commits {
		diff, err := gitOutput(worktree, "show", "--format=medium", "--no-ext-diff", commit)
		if err != nil {
			return nil, err
		}
		raw, err := gitInputOutput(worktree, []byte(diff), "patch-id", "--stable")
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(string(raw))
		if len(fields) == 0 {
			return nil, fmt.Errorf("git patch-id produced no output for commit %s", commit)
		}
		ids = append(ids, RefreshPatchID{PatchID: fields[0], Commit: commit})
	}
	return ids, nil
}

func remoteTrackingRef(worktree string, branch string) (string, error) {
	remotes, err := gitOutput(worktree, "remote")
	if err != nil {
		return "", err
	}
	refs, err := gitOutput(worktree, "for-each-ref", "--format=%(refname:short)", "refs/remotes")
	if err != nil {
		return "", err
	}
	return matchingRemoteTrackingRef(remotes, refs, branch), nil
}

func matchingRemoteTrackingRef(remotes string, refs string, branch string) string {
	wanted := map[string]bool{}
	for _, remote := range nonEmptyLines(remotes) {
		wanted[remote+"/"+branch] = true
	}
	for _, ref := range nonEmptyLines(refs) {
		if wanted[ref] {
			return ref
		}
	}
	return ""
}

func validateRefreshBackupRef(worktree string, backupRef string) error {
	if strings.HasPrefix(backupRef, "-") {
		return fmt.Errorf("refresh backup ref %q must not start with '-'", backupRef)
	}
	if _, err := gitOutput(worktree, "check-ref-format", "--branch", backupRef); err != nil {
		return fmt.Errorf("invalid refresh backup ref %q: %w", backupRef, err)
	}
	return nil
}

func verifyRefreshContribution(beforeFiles []string, afterFiles []string, beforePatchIDs []RefreshPatchID, afterPatchIDs []RefreshPatchID, commandResults []ContractCommandResult) RefreshVerification {
	filesPreserved := stringSlicesEqual(beforeFiles, afterFiles)
	patchesPreserved := patchIDSetsEqual(beforePatchIDs, afterPatchIDs)
	status := RefreshStatusSucceeded
	reasons := []string{}
	if !filesPreserved || !patchesPreserved {
		status = RefreshStatusVerificationFailed
		switch {
		case !filesPreserved && !patchesPreserved:
			reasons = append(reasons, "changed files and patch IDs differ after refresh")
		case !filesPreserved:
			reasons = append(reasons, "changed files differ after refresh")
		default:
			reasons = append(reasons, "patch IDs differ after refresh")
		}
	}
	if failed := firstFailedRefreshCommand(commandResults); failed != "" {
		status = RefreshStatusVerificationFailed
		reasons = append(reasons, "validation command failed: "+failed)
	}
	return RefreshVerification{
		Status:                status,
		ChangedFilesPreserved: filesPreserved,
		PatchIDsPreserved:     patchesPreserved,
		FailureReason:         strings.Join(reasons, "; "),
	}
}

func firstFailedRefreshCommand(commandResults []ContractCommandResult) string {
	for _, result := range commandResults {
		status := strings.ToLower(strings.TrimSpace(result.Status))
		switch status {
		case "passed", "pass", "success", "succeeded", "ok":
			continue
		default:
			return strings.TrimSpace(result.Command)
		}
	}
	return ""
}

func appendRefreshEventAfterMutation(workspaceDir string, evidence RefreshEvidence, evidencePath string, refreshedAt time.Time, expectedRefreshRevision int, now func() time.Time) (RefreshBranchResult, error) {
	if now == nil {
		now = time.Now
	}
	var lastErr error
	refreshResource := RefreshResource(evidence.MergeUnitID + ":" + evidence.AttemptID)
	for attempt := 0; attempt < refreshAppendMaxAttempts; attempt++ {
		recordedAt := now()
		if recordedAt.Before(refreshedAt) {
			recordedAt = refreshedAt
		}
		state, err := loadLeaseOperationState(workspaceDir, recordedAt)
		if err != nil {
			return RefreshBranchResult{}, err
		}
		recordedAt = state.ObservedAt
		if err := validateFreshRefreshState(state, evidence, recordedAt); err != nil {
			return RefreshBranchResult{}, err
		}
		if err := validateResourcesNotFrozen(state.Events, state.ActiveLeases, []string{MergeUnitResource(evidence.MergeUnitID)}, "workspace refresh-branch"); err != nil {
			return RefreshBranchResult{}, err
		}
		result, err := appendRefreshEvent(workspaceDir, state, evidence, evidencePath, recordedAt, expectedRefreshRevision)
		if err == nil {
			return result, nil
		}
		lastErr = err
		var stale StaleResourceError
		if !errors.As(err, &stale) || stale.Resource == refreshResource {
			return RefreshBranchResult{}, err
		}
		if stale.Resource != LeaseResource(evidence.MergeUnitID) && stale.Resource != MergeUnitResource(evidence.MergeUnitID) {
			return RefreshBranchResult{}, err
		}
	}
	return RefreshBranchResult{}, lastErr
}

func validateFreshRefreshState(state leaseOperationState, evidence RefreshEvidence, observedAt time.Time) error {
	lease, unit, err := requireOwnedActiveLease(state, evidence.LeaseID, evidence.AgentID)
	if err != nil {
		return err
	}
	if lease.MergeUnitID != evidence.MergeUnitID {
		return fmt.Errorf("lease %s is for merge unit %s, not %s", evidence.LeaseID, lease.MergeUnitID, evidence.MergeUnitID)
	}
	switch unit.Status {
	case MergeUnitCompleted, MergeUnitFailed:
		return fmt.Errorf("merge unit %s lifecycle is %s", evidence.MergeUnitID, unit.Status)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return err
	}
	current, err := requireCurrentAttemptAt(attempts, evidence.MergeUnitID, evidence.AttemptID, observedAt)
	if err != nil {
		return err
	}
	if err := validateAttemptLeaseOwner(evidence.AttemptID, current.AgentID, current.LeaseID, evidence.AgentID, evidence.LeaseID); err != nil {
		return err
	}
	return nil
}

func appendRefreshEvent(workspaceDir string, state leaseOperationState, evidence RefreshEvidence, evidencePath string, refreshedAt time.Time, expectedRefreshRevision int) (RefreshBranchResult, error) {
	resource := RefreshResource(evidence.MergeUnitID + ":" + evidence.AttemptID)
	evidence.InputChanges = normalizeRefreshInputChanges(evidence)
	for _, change := range evidence.InputChanges {
		if err := validateRefreshInputChangeForAttempt(change, evidence.MergeUnitID, evidence.AttemptID); err != nil {
			return RefreshBranchResult{}, err
		}
	}
	payload := map[string]any{
		eventPayloadMergeUnitIDKey:  evidence.MergeUnitID,
		eventPayloadAttemptIDKey:    evidence.AttemptID,
		eventPayloadAgentIDKey:      evidence.AgentID,
		eventPayloadLeaseIDKey:      evidence.LeaseID,
		eventPayloadStatusKey:       evidence.Verification.Status,
		eventPayloadBranchKey:       evidence.Branch,
		eventPayloadWorktreeKey:     evidence.Worktree,
		eventPayloadOldBaseKey:      evidence.OldBase,
		eventPayloadNewBaseKey:      evidence.NewBase,
		eventPayloadPreHeadKey:      evidence.PreHead,
		eventPayloadPostHeadKey:     evidence.PostHead,
		eventPayloadBackupRefKey:    evidence.BackupRef,
		eventPayloadEvidencePathKey: evidencePath,
	}
	if len(evidence.InputChanges) > 0 {
		payload[eventPayloadInputChangesKey] = refreshInputChangesPayload(evidence.InputChanges)
	}
	writeSet := append([]string{resource}, refreshInputChangeResources(evidence.InputChanges)...)
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         EventBranchRefreshRecorded,
		Payload:      payload,
		ReadSet: map[string]int{
			LeaseResource(evidence.MergeUnitID):     state.Revisions[LeaseResource(evidence.MergeUnitID)],
			MergeUnitResource(evidence.MergeUnitID): state.Revisions[MergeUnitResource(evidence.MergeUnitID)],
			resource:                                expectedRefreshRevision,
		},
		WriteSet: writeSet,
		Now:      func() time.Time { return refreshedAt },
	})
	if err != nil {
		return RefreshBranchResult{}, err
	}
	return RefreshBranchResult{
		Status:       evidence.Verification.Status,
		WorkspaceDir: workspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		MergeUnitID:  evidence.MergeUnitID,
		AttemptID:    evidence.AttemptID,
		Branch:       evidence.Branch,
		Worktree:     evidence.Worktree,
		EvidencePath: evidencePath,
		Evidence:     evidence,
		EventID:      event.ID,
		EventHash:    event.EventHash,
	}, nil
}

func writeRefreshEvidence(workspaceDir string, evidence RefreshEvidence, refreshedAt time.Time) (string, error) {
	relative := refreshEvidenceRelativePath(evidence, refreshedAt)
	path := filepath.Join(workspaceDir, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := writeStableJSON(path, evidence); err != nil {
		return "", err
	}
	return relative, nil
}

func refreshEvidenceRelativePath(evidence RefreshEvidence, refreshedAt time.Time) string {
	name := refreshedAt.UTC().Format("20060102T150405Z") + "-" + shortHash(evidence.PreHead) + "-" + shortHash(evidence.PostHead) + ".json"
	return filepath.Join(StateDirName, "evidence", "refresh", safePathSegment(evidence.MergeUnitID), safePathSegment(evidence.AttemptID), name)
}

func defaultRefreshBackupRef(branch string, refreshedAt time.Time) string {
	return branch + "-backup-" + refreshedAt.UTC().Format("20060102T150405Z")
}

func gitOutput(worktree string, args ...string) (string, error) {
	output, err := gitInputOutput(worktree, nil, args...)
	return string(output), err
}

func gitInputOutput(worktree string, input []byte, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", worktree}, args...)
	cmd := exec.Command("git", commandArgs...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %w\n%s", strings.Join(commandArgs, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func nonEmptyLines(output string) []string {
	lines := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func patchIDSetsEqual(left []RefreshPatchID, right []RefreshPatchID) bool {
	leftIDs := sortedPatchIDs(left)
	rightIDs := sortedPatchIDs(right)
	return stringSlicesEqual(leftIDs, rightIDs)
}

func sortedPatchIDs(values []RefreshPatchID) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.PatchID)
	}
	sort.Strings(ids)
	return ids
}

func safePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

func shortHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 12 {
		return value[:12]
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
