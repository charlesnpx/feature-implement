package workspace

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshVerificationChecksContribution(t *testing.T) {
	beforeFiles := []string{"M\tREADME.md"}
	afterFiles := []string{"M\tREADME.md"}
	beforePatches := []RefreshPatchID{{PatchID: "patch-a", Commit: "commit-a"}}
	afterPatches := []RefreshPatchID{{PatchID: "patch-a", Commit: "commit-b"}}

	verified := verifyRefreshContribution(beforeFiles, afterFiles, beforePatches, afterPatches, []ContractCommandResult{{Command: "go test ./...", Status: "passed"}})
	if verified.Status != RefreshStatusSucceeded || !verified.ChangedFilesPreserved || !verified.PatchIDsPreserved {
		t.Fatalf("verified = %+v", verified)
	}

	failed := verifyRefreshContribution(beforeFiles, []string{"M\tother.md"}, beforePatches, afterPatches, nil)
	if failed.Status != RefreshStatusVerificationFailed || failed.ChangedFilesPreserved || !strings.Contains(failed.FailureReason, "changed files") {
		t.Fatalf("failed changed files = %+v", failed)
	}

	failed = verifyRefreshContribution(beforeFiles, afterFiles, beforePatches, []RefreshPatchID{{PatchID: "patch-b", Commit: "commit-b"}}, nil)
	if failed.Status != RefreshStatusVerificationFailed || failed.PatchIDsPreserved || !strings.Contains(failed.FailureReason, "patch IDs") {
		t.Fatalf("failed patch ids = %+v", failed)
	}

	failed = verifyRefreshContribution(beforeFiles, afterFiles, beforePatches, afterPatches, []ContractCommandResult{{Command: "go test ./...", Status: "failed"}})
	if failed.Status != RefreshStatusVerificationFailed || !strings.Contains(failed.FailureReason, "validation command failed: go test ./...") {
		t.Fatalf("failed command result = %+v", failed)
	}
}

func TestValidateRefreshBackupRefRejectsOptionLikeRef(t *testing.T) {
	err := validateRefreshBackupRef("", "-delete-me")
	if err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
		t.Fatalf("validateRefreshBackupRef error = %v, want option-like ref rejection", err)
	}
}

func TestRefreshVerificationFailureBlocksCurrentAttempt(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	resource := RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	evidencePath := filepath.Join(StateDirName, "evidence", "refresh", "failed.json")
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventBranchRefreshRecorded,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:  claim.MergeUnitID,
			eventPayloadAttemptIDKey:    attempt.AttemptID,
			eventPayloadAgentIDKey:      "worker-a",
			eventPayloadLeaseIDKey:      claim.LeaseID,
			eventPayloadStatusKey:       RefreshStatusVerificationFailed,
			eventPayloadBranchKey:       attempt.Branch,
			eventPayloadWorktreeKey:     attempt.Worktree,
			eventPayloadOldBaseKey:      attempt.BaseSHA,
			eventPayloadNewBaseKey:      "new-base-sha",
			eventPayloadPreHeadKey:      "pre-head-sha",
			eventPayloadPostHeadKey:     "post-head-sha",
			eventPayloadBackupRefKey:    "backup-ref",
			eventPayloadEvidencePathKey: evidencePath,
		},
		ReadSet: map[string]int{
			LeaseResource(claim.MergeUnitID):     revisions[LeaseResource(claim.MergeUnitID)],
			MergeUnitResource(claim.MergeUnitID): revisions[MergeUnitResource(claim.MergeUnitID)],
			resource:                             revisions[resource],
		},
		WriteSet: []string{resource},
		Now:      fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent refresh failure: %v", err)
	}

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	unit := findSchedulerUnit(t, view, claim.MergeUnitID)
	if len(unit.BlockingConditions) != 1 {
		t.Fatalf("blocking conditions = %+v", unit.BlockingConditions)
	}
	condition := unit.BlockingConditions[0]
	if condition.Type != "refresh_verification_failed" ||
		condition.Resource != resource ||
		condition.AttemptID != attempt.AttemptID ||
		condition.EvidencePath != evidencePath ||
		condition.RequiredAction != "rerun_local_refresh" {
		t.Fatalf("refresh condition = %+v", condition)
	}

	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "retry after refresh verification failure",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	view, err = RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView after abandon: %v", err)
	}
	unit = findSchedulerUnit(t, view, claim.MergeUnitID)
	if len(unit.BlockingConditions) != 0 {
		t.Fatalf("abandoned attempt should clear refresh block, got %+v", unit.BlockingConditions)
	}
}

func TestRefreshVerificationFailureBlocksCurrentAttemptTransitions(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusVerificationFailed, attempt.BaseSHA, "base-sha-2", "2026-06-17T10:03:00Z")

	_, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "refresh_verification_failed") || !strings.Contains(err.Error(), "requires rerun_local_refresh") {
		t.Fatalf("blocked completion error = %v", err)
	}

	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, "base-sha-2", "base-sha-3", "2026-06-17T10:05:00Z")
	completed, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		Now:          fixedJournalTime("2026-06-17T10:06:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition after successful refresh: %v", err)
	}
	if completed.EventType != EventMergeUnitCompleted {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestRefreshVerificationFailureRejectsTransitionReplay(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusVerificationFailed, attempt.BaseSHA, "base-sha-2", "2026-06-17T10:03:00Z")

	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	if err := appendTransitionEvent(fixture.Dir, TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
	}, EventMergeUnitCompleted, map[string]any{evidenceCommitSHAKey: "commit-sha-1"}, revisions, fixedJournalTime("2026-06-17T10:04:00Z")()); err != nil {
		t.Fatalf("appendTransitionEvent: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)
	if err == nil || !strings.Contains(err.Error(), "refresh_verification_failed") || !strings.Contains(err.Error(), "requires rerun_local_refresh") {
		t.Fatalf("replay error = %v", err)
	}
}

func TestLatestRefreshAdvancesBaselineAfterSuccessOrFailure(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	first := appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, "base-sha-1", "base-sha-2", "2026-06-17T10:02:00Z")
	second := appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusVerificationFailed, "base-sha-2", "base-sha-3", "2026-06-17T10:03:00Z")

	events := readTestJournalEvents(t, fixture.Dir)
	latest, ok := latestRefresh(events, claim.MergeUnitID, attempt.AttemptID)
	if !ok {
		t.Fatalf("latest refresh not found")
	}
	if latest.Status != RefreshStatusVerificationFailed || latest.NewBase != "base-sha-3" || latest.OldBase != "base-sha-2" || latest.EvidencePath != second || latest.EvidencePath == first {
		t.Fatalf("latest refresh = %+v first=%s second=%s", latest, first, second)
	}
}

func TestAppendRefreshEventAfterMutationUsesFreshLeaseRevision(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	resource := RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	originalRefreshRevision := revisions[resource]

	if _, err := Heartbeat(LeaseOptions{
		WorkspaceDir:  fixture.Dir,
		AgentID:       "worker-a",
		LeaseID:       claim.LeaseID,
		LeaseDuration: 14 * 24 * time.Hour,
		Now:           fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	evidencePath := filepath.Join(StateDirName, "evidence", "refresh", "fresh-lease.json")
	evidence := RefreshEvidence{
		SchemaVersion:      1,
		WorkspaceID:        fixture.Manifest.ID,
		BaseRef:            fixtureWorkspaceBaseRef,
		MergeUnitID:        claim.MergeUnitID,
		AttemptID:          attempt.AttemptID,
		AgentID:            "worker-a",
		LeaseID:            claim.LeaseID,
		Local:              true,
		Branch:             attempt.Branch,
		Worktree:           attempt.Worktree,
		OldBase:            attempt.BaseSHA,
		NewBase:            "new-base-sha",
		PreHead:            "pre-head-sha",
		PostHead:           "post-head-sha",
		BackupRef:          "backup-ref",
		ChangedFilesBefore: []string{"M\tREADME.md"},
		ChangedFilesAfter:  []string{"M\tREADME.md"},
		PatchIDsBefore:     []RefreshPatchID{{PatchID: "patch-a", Commit: "commit-a"}},
		PatchIDsAfter:      []RefreshPatchID{{PatchID: "patch-a", Commit: "commit-b"}},
		Verification: RefreshVerification{
			Status:                RefreshStatusSucceeded,
			ChangedFilesPreserved: true,
			PatchIDsPreserved:     true,
		},
	}
	result, err := appendRefreshEventAfterMutation(fixture.Dir, evidence, evidencePath, fixedJournalTime("2026-06-17T10:02:30Z")(), originalRefreshRevision, fixedJournalTime("2026-06-17T10:02:30Z"))
	if err != nil {
		t.Fatalf("appendRefreshEventAfterMutation: %v", err)
	}
	if result.Status != RefreshStatusSucceeded || result.EvidencePath != evidencePath || result.EventID == "" {
		t.Fatalf("refresh result = %+v", result)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	latest, ok := latestRefresh(events, claim.MergeUnitID, attempt.AttemptID)
	if !ok || latest.Status != RefreshStatusSucceeded || latest.EvidencePath != evidencePath {
		t.Fatalf("latest refresh = %+v ok=%v", latest, ok)
	}
}

func TestAppendRefreshEventAfterMutationRejectsStaleAttempt(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	resource := RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	originalRefreshRevision := revisions[resource]

	if _, err := Heartbeat(LeaseOptions{
		WorkspaceDir:  fixture.Dir,
		AgentID:       "worker-a",
		LeaseID:       claim.LeaseID,
		LeaseDuration: 14 * 24 * time.Hour,
		Now:           fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "worker stopped",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}

	evidence := RefreshEvidence{
		SchemaVersion: 1,
		WorkspaceID:   fixture.Manifest.ID,
		BaseRef:       fixtureWorkspaceBaseRef,
		MergeUnitID:   claim.MergeUnitID,
		AttemptID:     attempt.AttemptID,
		AgentID:       "worker-a",
		LeaseID:       claim.LeaseID,
		Local:         true,
		Branch:        attempt.Branch,
		Worktree:      attempt.Worktree,
		OldBase:       attempt.BaseSHA,
		NewBase:       "new-base-sha",
		PreHead:       "pre-head-sha",
		PostHead:      "post-head-sha",
		BackupRef:     "backup-ref",
		Verification: RefreshVerification{
			Status: RefreshStatusVerificationFailed,
		},
	}
	_, err = appendRefreshEventAfterMutation(fixture.Dir, evidence, filepath.Join(StateDirName, "evidence", "refresh", "stale-attempt.json"), fixedJournalTime("2026-06-17T10:02:30Z")(), originalRefreshRevision, fixedJournalTime("2026-06-17T10:02:30Z"))
	if err == nil || !strings.Contains(err.Error(), "has no active attempt") {
		t.Fatalf("stale attempt error = %v", err)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	if latest, ok := latestRefresh(events, claim.MergeUnitID, attempt.AttemptID); ok {
		t.Fatalf("stale attempt should not record refresh, got %+v", latest)
	}
}

func TestAppendTransitionEventReadsRefreshRevision(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusVerificationFailed, attempt.BaseSHA, "base-sha-2", "2026-06-17T10:03:00Z")

	err = appendTransitionEvent(fixture.Dir, TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
	}, EventMergeUnitCompleted, map[string]any{evidenceCommitSHAKey: "commit-sha-1"}, revisions, fixedJournalTime("2026-06-17T10:04:00Z")())
	var stale StaleResourceError
	refreshResource := RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	if err == nil || !errors.As(err, &stale) || stale.Resource != refreshResource {
		t.Fatalf("appendTransitionEvent stale error = %v, stale=%+v want resource %s", err, stale, refreshResource)
	}
}

func TestRefreshBranchBlockedByExternalIntentFreeze(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")

	_, err := RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Local:        true,
		Worktree:     attempt.Worktree,
		NewBase:      "base-sha-2",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "workspace refresh-branch blocked by frozen resource") {
		t.Fatalf("RefreshBranch freeze error = %v", err)
	}
}

func TestAppendRefreshEventAfterMutationRejectsFreshExternalIntentFreeze(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	resource := RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	originalRefreshRevision := revisions[resource]
	if _, err := Heartbeat(LeaseOptions{
		WorkspaceDir:  fixture.Dir,
		AgentID:       "worker-a",
		LeaseID:       claim.LeaseID,
		LeaseDuration: 14 * 24 * time.Hour,
		Now:           fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")

	evidence := RefreshEvidence{
		SchemaVersion: 1,
		WorkspaceID:   fixture.Manifest.ID,
		BaseRef:       fixtureWorkspaceBaseRef,
		MergeUnitID:   claim.MergeUnitID,
		AttemptID:     attempt.AttemptID,
		AgentID:       "worker-a",
		LeaseID:       claim.LeaseID,
		Local:         true,
		Branch:        attempt.Branch,
		Worktree:      attempt.Worktree,
		OldBase:       attempt.BaseSHA,
		NewBase:       "new-base-sha",
		PreHead:       "pre-head-sha",
		PostHead:      "post-head-sha",
		BackupRef:     "backup-ref",
		Verification: RefreshVerification{
			Status: RefreshStatusSucceeded,
		},
	}
	_, err = appendRefreshEventAfterMutation(fixture.Dir, evidence, filepath.Join(StateDirName, "evidence", "refresh", "fresh-freeze.json"), fixedJournalTime("2026-06-17T10:04:00Z")(), originalRefreshRevision, fixedJournalTime("2026-06-17T10:04:00Z"))
	if err == nil || !strings.Contains(err.Error(), "workspace refresh-branch blocked by frozen resource") {
		t.Fatalf("fresh freeze error = %v", err)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	if latest, ok := latestRefresh(events, claim.MergeUnitID, attempt.AttemptID); ok {
		t.Fatalf("fresh freeze should not record refresh, got %+v", latest)
	}
}

func TestMatchingRemoteTrackingRefRequiresExactRemoteBranch(t *testing.T) {
	remotes := "origin\nupstream\n"
	refs := strings.Join([]string{
		"origin/archive/feature/story-a",
		"upstream/feature/story-b",
		"origin/feature/story-a",
	}, "\n")
	if got := matchingRemoteTrackingRef(remotes, refs, "feature/story-a"); got != "origin/feature/story-a" {
		t.Fatalf("matchingRemoteTrackingRef exact = %q", got)
	}

	refs = strings.Join([]string{
		"origin/archive/feature/story-a",
		"upstream/archive/feature/story-a",
	}, "\n")
	if got := matchingRemoteTrackingRef(remotes, refs, "feature/story-a"); got != "" {
		t.Fatalf("matchingRemoteTrackingRef unrelated suffix = %q", got)
	}
}

func TestPublishRefreshPlansForceWithLeaseCommand(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, attempt.BaseSHA, "base-sha-2", "2026-06-17T10:02:00Z")
	approval := grantPublishRefreshApprovalForTest(t, fixture, claim, attempt, "post-base-sha-2", "remote-sha-1", "2026-06-17T10:03:00Z")
	before := len(readTestJournalEvents(t, fixture.Dir))

	planned, err := PublishRefresh(PublishRefreshOptions{
		WorkspaceDir:      fixture.Dir,
		MergeUnitID:       claim.MergeUnitID,
		AttemptID:         attempt.AttemptID,
		AgentID:           "worker-a",
		LeaseID:           claim.LeaseID,
		ApprovalID:        approval.Approval.ApprovalID,
		Remote:            "upstream",
		ExpectedRemoteSHA: "remote-sha-1",
		Now:               fixedJournalTime("2026-06-17T10:04:00Z"),
		remoteHeadResolver: func(worktree string, remote string, branch string) (string, error) {
			if worktree != attempt.Worktree || remote != "upstream" || branch != attempt.Branch {
				t.Fatalf("remote resolver args = %q %q %q", worktree, remote, branch)
			}
			return "remote-sha-1", nil
		},
	})
	if err != nil {
		t.Fatalf("PublishRefresh: %v", err)
	}
	if planned.Status != "planned" || planned.HeadSHA != "post-base-sha-2" || planned.Intent.Action != ExternalActionPush || planned.Intent.ExpectedBaseSHA != "remote-sha-1" {
		t.Fatalf("planned = %+v", planned)
	}
	wantProvider := "git -C " + attempt.Worktree + " push " + shellQuote("--force-with-lease=refs/heads/"+attempt.Branch+":remote-sha-1") + " upstream HEAD:refs/heads/" + attempt.Branch
	if planned.Plan.ProviderCommand != wantProvider {
		t.Fatalf("provider command = %s, want %s", planned.Plan.ProviderCommand, wantProvider)
	}
	if !strings.Contains(planned.Plan.IntentCommand, "feature workspace external intent reserve") ||
		!strings.Contains(planned.Plan.IntentCommand, "--approval "+approval.Approval.ApprovalID) ||
		!strings.Contains(planned.Plan.IntentCommand, "--head-sha post-base-sha-2") ||
		!strings.Contains(planned.Plan.IntentCommand, "--base-sha remote-sha-1") {
		t.Fatalf("intent command = %s", planned.Plan.IntentCommand)
	}
	if got := len(readTestJournalEvents(t, fixture.Dir)); got != before {
		t.Fatalf("PublishRefresh should only plan when remote matches: got %d events want %d", got, before)
	}
}

func TestPublishRefreshRequiresApproval(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, attempt.BaseSHA, "base-sha-2", "2026-06-17T10:02:00Z")
	before := len(readTestJournalEvents(t, fixture.Dir))

	_, err := PublishRefresh(PublishRefreshOptions{
		WorkspaceDir:      fixture.Dir,
		MergeUnitID:       claim.MergeUnitID,
		AttemptID:         attempt.AttemptID,
		AgentID:           "worker-a",
		LeaseID:           claim.LeaseID,
		ApprovalID:        "approval-missing",
		ExpectedRemoteSHA: "remote-sha-1",
		Now:               fixedJournalTime("2026-06-17T10:03:00Z"),
		remoteHeadResolver: func(worktree string, remote string, branch string) (string, error) {
			t.Fatal("remote resolver should not run before approval validation")
			return "", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "approval not found") {
		t.Fatalf("PublishRefresh error = %v, want missing approval", err)
	}
	if got := len(readTestJournalEvents(t, fixture.Dir)); got != before {
		t.Fatalf("missing approval should not append events: got %d want %d", got, before)
	}
}

func TestPublishRefreshRemoteMovedRecordsBlockingCondition(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, attempt.BaseSHA, "base-sha-2", "2026-06-17T10:02:00Z")
	approval := grantPublishRefreshApprovalForTest(t, fixture, claim, attempt, "post-base-sha-2", "remote-sha-1", "2026-06-17T10:03:00Z")

	result, err := PublishRefresh(PublishRefreshOptions{
		WorkspaceDir:      fixture.Dir,
		MergeUnitID:       claim.MergeUnitID,
		AttemptID:         attempt.AttemptID,
		AgentID:           "worker-a",
		LeaseID:           claim.LeaseID,
		ApprovalID:        approval.Approval.ApprovalID,
		ExpectedRemoteSHA: "remote-sha-1",
		Now:               fixedJournalTime("2026-06-17T10:04:00Z"),
		remoteHeadResolver: func(worktree string, remote string, branch string) (string, error) {
			return "remote-sha-2", nil
		},
	})
	var moved RemoteBranchMovedError
	if err == nil || !errors.As(err, &moved) {
		t.Fatalf("PublishRefresh error = %v, want RemoteBranchMovedError", err)
	}
	if result.Status != RefreshStatusRemoteBranchMoved || moved.Result.EventID == "" || result.ObservedRemoteSHA != "remote-sha-2" {
		t.Fatalf("remote moved result = %+v moved=%+v", result, moved.Result)
	}
	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	unit := findSchedulerUnit(t, view, claim.MergeUnitID)
	if len(unit.BlockingConditions) != 1 {
		t.Fatalf("blocking conditions = %+v", unit.BlockingConditions)
	}
	condition := unit.BlockingConditions[0]
	if condition.Type != RefreshStatusRemoteBranchMoved ||
		condition.Status != RefreshStatusRemoteBranchMoved ||
		condition.RequiredAction != "rerun_local_refresh" ||
		condition.EvidencePath != result.EvidencePath {
		t.Fatalf("remote moved condition = %+v result=%+v", condition, result)
	}
}

func TestPublishRefreshRemoteMovedRecordsAfterLeaseHeartbeat(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, attempt.BaseSHA, "base-sha-2", "2026-06-17T10:02:00Z")
	approval := grantPublishRefreshApprovalForTest(t, fixture, claim, attempt, "post-base-sha-2", "remote-sha-1", "2026-06-17T10:03:00Z")

	result, err := PublishRefresh(PublishRefreshOptions{
		WorkspaceDir:      fixture.Dir,
		MergeUnitID:       claim.MergeUnitID,
		AttemptID:         attempt.AttemptID,
		AgentID:           "worker-a",
		LeaseID:           claim.LeaseID,
		ApprovalID:        approval.Approval.ApprovalID,
		ExpectedRemoteSHA: "remote-sha-1",
		Now:               fixedJournalTime("2026-06-17T10:04:00Z"),
		remoteHeadResolver: func(worktree string, remote string, branch string) (string, error) {
			if _, err := Heartbeat(LeaseOptions{
				WorkspaceDir:  fixture.Dir,
				AgentID:       "worker-a",
				LeaseID:       claim.LeaseID,
				LeaseDuration: 14 * 24 * time.Hour,
				Now:           fixedJournalTime("2026-06-17T10:05:00Z"),
			}); err != nil {
				t.Fatalf("Heartbeat during remote resolution: %v", err)
			}
			return "remote-sha-2", nil
		},
	})
	var moved RemoteBranchMovedError
	if err == nil || !errors.As(err, &moved) {
		t.Fatalf("PublishRefresh error = %v, want RemoteBranchMovedError", err)
	}
	if result.Status != RefreshStatusRemoteBranchMoved || result.EventID == "" {
		t.Fatalf("remote moved result after heartbeat = %+v", result)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventBranchRefreshRecorded || last.Timestamp != "2026-06-17T10:05:00Z" {
		t.Fatalf("last event = %+v", last)
	}
}

func appendRefreshEventForTest(t *testing.T, fixture workspaceFixture, claim NextResult, attempt AttemptResult, status string, oldBase string, newBase string, at string) string {
	t.Helper()
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	resource := RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	evidencePath := filepath.Join(StateDirName, "evidence", "refresh", newBase+".json")
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventBranchRefreshRecorded,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:  claim.MergeUnitID,
			eventPayloadAttemptIDKey:    attempt.AttemptID,
			eventPayloadAgentIDKey:      "worker-a",
			eventPayloadLeaseIDKey:      claim.LeaseID,
			eventPayloadStatusKey:       status,
			eventPayloadBranchKey:       attempt.Branch,
			eventPayloadWorktreeKey:     attempt.Worktree,
			eventPayloadOldBaseKey:      oldBase,
			eventPayloadNewBaseKey:      newBase,
			eventPayloadPreHeadKey:      "pre-" + newBase,
			eventPayloadPostHeadKey:     "post-" + newBase,
			eventPayloadBackupRefKey:    "backup-" + newBase,
			eventPayloadEvidencePathKey: evidencePath,
		},
		ReadSet: map[string]int{
			LeaseResource(claim.MergeUnitID):     revisions[LeaseResource(claim.MergeUnitID)],
			MergeUnitResource(claim.MergeUnitID): revisions[MergeUnitResource(claim.MergeUnitID)],
			resource:                             revisions[resource],
		},
		WriteSet: []string{resource},
		Now:      fixedJournalTime(at),
	}); err != nil {
		t.Fatalf("AppendEvent refresh %s: %v", newBase, err)
	}
	return evidencePath
}

func grantPublishRefreshApprovalForTest(t *testing.T, fixture workspaceFixture, claim NextResult, attempt AttemptResult, headSHA string, remoteSHA string, at string) ApprovalResult {
	t.Helper()
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionPush},
		Branch:       attempt.Branch,
		HeadSHA:      headSHA,
		BaseSHA:      remoteSHA,
		MaxUses:      1,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime(at),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	return approval
}
