package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshVerificationChecksContribution(t *testing.T) {
	beforeFiles := []string{"M\tREADME.md"}
	afterFiles := []string{"M\tREADME.md"}
	beforePatches := []RefreshPatchID{{PatchID: "patch-a", Commit: "commit-a"}}
	afterPatches := []RefreshPatchID{{PatchID: "patch-a", Commit: "commit-b"}}

	verified := verifyRefreshContribution(beforeFiles, afterFiles, beforePatches, afterPatches)
	if verified.Status != RefreshStatusSucceeded || !verified.ChangedFilesPreserved || !verified.PatchIDsPreserved {
		t.Fatalf("verified = %+v", verified)
	}

	failed := verifyRefreshContribution(beforeFiles, []string{"M\tother.md"}, beforePatches, afterPatches)
	if failed.Status != RefreshStatusVerificationFailed || failed.ChangedFilesPreserved || !strings.Contains(failed.FailureReason, "changed files") {
		t.Fatalf("failed changed files = %+v", failed)
	}

	failed = verifyRefreshContribution(beforeFiles, afterFiles, beforePatches, []RefreshPatchID{{PatchID: "patch-b", Commit: "commit-b"}})
	if failed.Status != RefreshStatusVerificationFailed || failed.PatchIDsPreserved || !strings.Contains(failed.FailureReason, "patch IDs") {
		t.Fatalf("failed patch ids = %+v", failed)
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

func TestLatestSuccessfulRefreshAdvancesBaseline(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	first := appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, "base-sha-1", "base-sha-2", "2026-06-17T10:02:00Z")
	second := appendRefreshEventForTest(t, fixture, claim, attempt, RefreshStatusSucceeded, "base-sha-2", "base-sha-3", "2026-06-17T10:03:00Z")

	events := readTestJournalEvents(t, fixture.Dir)
	latest, ok := latestSuccessfulRefresh(events, claim.MergeUnitID, attempt.AttemptID)
	if !ok {
		t.Fatalf("latest successful refresh not found")
	}
	if latest.NewBase != "base-sha-3" || latest.OldBase != "base-sha-2" || latest.EvidencePath != second || latest.EvidencePath == first {
		t.Fatalf("latest refresh = %+v first=%s second=%s", latest, first, second)
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
