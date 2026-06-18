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
}
