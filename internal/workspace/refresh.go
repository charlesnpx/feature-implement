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

	refreshAppendMaxAttempts = 3

	eventPayloadOldBaseKey      = "old_base"
	eventPayloadNewBaseKey      = "new_base"
	eventPayloadPreHeadKey      = "pre_head"
	eventPayloadPostHeadKey     = "post_head"
	eventPayloadBackupRefKey    = "backup_ref"
	eventPayloadEvidencePathKey = "evidence_path"
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
	CommandResults     []ContractCommandResult `json:"command_results,omitempty"`
	Verification       RefreshVerification     `json:"verification"`
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
	OldBase      string
	NewBase      string
	EvidencePath string
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
	if !ok || snapshot.Status != RefreshStatusVerificationFailed {
		return nil
	}
	if attemptID == "" || snapshot.AttemptID != attemptID {
		return nil
	}
	return []SchedulerBlockingCondition{{
		Type:           "refresh_verification_failed",
		Resource:       snapshot.Resource,
		AttemptID:      snapshot.AttemptID,
		Status:         snapshot.Status,
		RequiredAction: "rerun_local_refresh",
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
	evidence, err := runLocalRefresh(opts, state.View.WorkspaceID, state.View.BaseRef, state.Events, current, worktree, commandResults, refreshedAt)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	evidencePath, err := writeRefreshEvidence(opts.WorkspaceDir, evidence, refreshedAt)
	if err != nil {
		return RefreshBranchResult{}, err
	}
	result, err := appendRefreshEventAfterMutation(opts.WorkspaceDir, evidence, evidencePath, refreshedAt, originalRefreshRevision)
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
	case RefreshStatusSucceeded, RefreshStatusVerificationFailed:
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
	if _, err := eventStringPayload(event, eventPayloadBranchKey); err != nil {
		return refreshSnapshot{}, err
	}
	if _, err := eventStringPayload(event, eventPayloadWorktreeKey); err != nil {
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
	if _, err := eventStringPayload(event, eventPayloadPreHeadKey); err != nil {
		return refreshSnapshot{}, err
	}
	if _, err := eventStringPayload(event, eventPayloadPostHeadKey); err != nil {
		return refreshSnapshot{}, err
	}
	if _, err := eventStringPayload(event, eventPayloadBackupRefKey); err != nil {
		return refreshSnapshot{}, err
	}
	return refreshSnapshot{
		MergeUnitID:  mergeUnitID,
		AttemptID:    attemptID,
		Status:       status,
		Resource:     resource,
		OldBase:      oldBase,
		NewBase:      newBase,
		EvidencePath: evidencePath,
	}, nil
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
	output, err := gitOutput(worktree, "for-each-ref", "--format=%(refname:short)", "refs/remotes")
	if err != nil {
		return "", err
	}
	suffix := "/" + branch
	for _, ref := range nonEmptyLines(output) {
		if strings.HasSuffix(ref, suffix) {
			return ref, nil
		}
	}
	return "", nil
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

func appendRefreshEventAfterMutation(workspaceDir string, evidence RefreshEvidence, evidencePath string, refreshedAt time.Time, expectedRefreshRevision int) (RefreshBranchResult, error) {
	var lastErr error
	refreshResource := RefreshResource(evidence.MergeUnitID + ":" + evidence.AttemptID)
	for attempt := 0; attempt < refreshAppendMaxAttempts; attempt++ {
		recordedAt := time.Now()
		if recordedAt.Before(refreshedAt) {
			recordedAt = refreshedAt
		}
		state, err := loadLeaseOperationState(workspaceDir, recordedAt)
		if err != nil {
			return RefreshBranchResult{}, err
		}
		recordedAt, err = observedAtAfterEvents(state.Events, recordedAt)
		if err != nil {
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

func appendRefreshEvent(workspaceDir string, state leaseOperationState, evidence RefreshEvidence, evidencePath string, refreshedAt time.Time, expectedRefreshRevision int) (RefreshBranchResult, error) {
	resource := RefreshResource(evidence.MergeUnitID + ":" + evidence.AttemptID)
	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         EventBranchRefreshRecorded,
		Payload: map[string]any{
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
		},
		ReadSet: map[string]int{
			LeaseResource(evidence.MergeUnitID):     state.Revisions[LeaseResource(evidence.MergeUnitID)],
			MergeUnitResource(evidence.MergeUnitID): state.Revisions[MergeUnitResource(evidence.MergeUnitID)],
			resource:                                expectedRefreshRevision,
		},
		WriteSet: []string{resource},
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
