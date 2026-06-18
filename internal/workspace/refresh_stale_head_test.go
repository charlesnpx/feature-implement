package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostRefreshCommitStalesReadinessEvidence(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git executable is unavailable")
	}

	root := t.TempDir()
	hooksDir := filepath.Join(root, "no-hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitEnv := isolatedGitEnv(hooksDir)

	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "init")
	runGit(t, gitPath, gitEnv, repoDir, "checkout", "-b", fixtureWorkspaceBaseRef)
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-test@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedWorkspaceTime("2026-01-02T15:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		BaseSHA:      baseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(attempt.Worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	worktreeAdded := false
	t.Cleanup(func() {
		if !worktreeAdded {
			return
		}
		runGitCleanup(gitPath, gitEnv, repoDir, "worktree", "remove", "--force", attempt.Worktree)
		runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", attempt.Branch)
	})
	runGit(t, gitPath, gitEnv, repoDir, "worktree", "add", "-b", attempt.Branch, attempt.Worktree, baseSHA)
	worktreeAdded = true

	headBeforeFix := commitWorktreeFile(t, gitPath, gitEnv, attempt.Worktree, "story.txt", "story work\n", "story work")
	firstRefresh, err := RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Local:        true,
		NewBase:      baseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:03:00Z"),
	})
	if err != nil {
		t.Fatalf("RefreshBranch first: %v", err)
	}
	if firstRefresh.Evidence.PostHead != headBeforeFix {
		t.Fatalf("first refresh post_head = %s, want %s", firstRefresh.Evidence.PostHead, headBeforeFix)
	}
	movedWorktree := attempt.Worktree + ".moved"
	if err := os.Rename(attempt.Worktree, movedWorktree); err != nil {
		t.Fatalf("move worktree aside: %v", err)
	}
	_, unavailableErr := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:03:30Z"),
	})
	unavailableStatus, statusErr := Status(fixture.Dir)
	if err := os.Rename(movedWorktree, attempt.Worktree); err != nil {
		t.Fatalf("restore worktree: %v", err)
	}
	if unavailableErr == nil || !strings.Contains(unavailableErr.Error(), "stale refresh head evidence") || !strings.Contains(unavailableErr.Error(), "<unavailable>") {
		t.Fatalf("EvaluateGates unavailable-worktree error = %v", unavailableErr)
	}
	if statusErr != nil {
		t.Fatalf("Status unavailable worktree: %v", statusErr)
	}
	unavailableUnit := findSchedulerUnit(t, SchedulerView{MergeUnits: unavailableStatus.MergeUnits}, claim.MergeUnitID)
	if !hasBlockingConditionWithAction(unavailableUnit.BlockingConditions, refreshConditionStaleHead, mergeQueueRequiredActionRefresh) {
		t.Fatalf("status unavailable-worktree blockers = %+v", unavailableUnit.BlockingConditions)
	}

	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionMerge},
		Branch:       attempt.Branch,
		HeadSHA:      headBeforeFix,
		BaseSHA:      baseSHA,
		MaxUses:      1,
		ExpiresAt:    parseWorkspaceTestTime("2027-01-02T15:00:00Z"),
		Now:          fixedWorkspaceTime("2026-01-02T15:04:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval first: %v", err)
	}
	evaluation := evaluateQueueGates(t, fixture.Dir, queueClaimFromNext(claim), attempt, "2026-01-02T15:05:00Z")
	overrideQueueGate(t, fixture.Dir, queueClaimFromNext(claim), attempt, evaluation.InputHash, "review", headBeforeFix, baseSHA, "2026-01-02T15:06:00Z")
	overrideQueueGate(t, fixture.Dir, queueClaimFromNext(claim), attempt, evaluation.InputHash, "security", headBeforeFix, baseSHA, "2026-01-02T15:07:00Z")
	overrideQueueGate(t, fixture.Dir, queueClaimFromNext(claim), attempt, evaluation.InputHash, "test", headBeforeFix, baseSHA, "2026-01-02T15:08:00Z")
	evaluateQueueGates(t, fixture.Dir, queueClaimFromNext(claim), attempt, "2026-01-02T15:08:30Z")

	headAfterFix := commitWorktreeFile(t, gitPath, gitEnv, attempt.Worktree, "fix.txt", "fix work\n", "fix work")
	if headAfterFix == headBeforeFix {
		t.Fatalf("fix commit did not advance HEAD")
	}

	if _, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	}); err == nil || !strings.Contains(err.Error(), "stale refresh head evidence") {
		t.Fatalf("EvaluateGates stale-head error = %v", err)
	}
	if _, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       ExternalActionMerge,
		Branch:       attempt.Branch,
		HeadSHA:      headBeforeFix,
		BaseSHA:      baseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:30Z"),
	}); err == nil || !strings.Contains(err.Error(), "stale refresh head evidence") {
		t.Fatalf("CheckApproval stale-head error = %v", err)
	}

	blocked, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		ApprovalID:   approval.Approval.ApprovalID,
		Branch:       attempt.Branch,
		HeadSHA:      headBeforeFix,
		BaseSHA:      baseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:10:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit stale head: %v", err)
	}
	if blocked.Status != "blocked" || !hasBlockingConditionWithAction(blocked.BlockingConditions, refreshConditionStaleHead, mergeQueueRequiredActionRefresh) {
		t.Fatalf("stale-head queue block = %+v", blocked)
	}
	if hasBlockingCondition(blocked.BlockingConditions, "missing_refresh") {
		t.Fatalf("stale head should not be reported as missing refresh: %+v", blocked.BlockingConditions)
	}

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, claim.MergeUnitID)
	if !hasBlockingConditionWithAction(unit.BlockingConditions, refreshConditionStaleHead, mergeQueueRequiredActionRefresh) {
		t.Fatalf("status stale-head blockers = %+v", unit.BlockingConditions)
	}

	secondRefresh, err := RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Local:        true,
		NewBase:      baseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:11:00Z"),
	})
	if err != nil {
		t.Fatalf("RefreshBranch second: %v", err)
	}
	if secondRefresh.Evidence.PostHead != headAfterFix {
		t.Fatalf("second refresh post_head = %s, want %s", secondRefresh.Evidence.PostHead, headAfterFix)
	}
	secondApproval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionMerge},
		Branch:       attempt.Branch,
		HeadSHA:      headAfterFix,
		BaseSHA:      baseSHA,
		MaxUses:      1,
		ExpiresAt:    parseWorkspaceTestTime("2027-01-02T15:00:00Z"),
		Now:          fixedWorkspaceTime("2026-01-02T15:12:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval second: %v", err)
	}
	secondEvaluation := evaluateQueueGates(t, fixture.Dir, queueClaimFromNext(claim), attempt, "2026-01-02T15:13:00Z")
	overrideQueueGate(t, fixture.Dir, queueClaimFromNext(claim), attempt, secondEvaluation.InputHash, "review", headAfterFix, baseSHA, "2026-01-02T15:14:00Z")
	overrideQueueGate(t, fixture.Dir, queueClaimFromNext(claim), attempt, secondEvaluation.InputHash, "security", headAfterFix, baseSHA, "2026-01-02T15:15:00Z")
	overrideQueueGate(t, fixture.Dir, queueClaimFromNext(claim), attempt, secondEvaluation.InputHash, "test", headAfterFix, baseSHA, "2026-01-02T15:16:00Z")
	secondEvaluation = evaluateQueueGates(t, fixture.Dir, queueClaimFromNext(claim), attempt, "2026-01-02T15:16:30Z")

	queued, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		ApprovalID:   secondApproval.Approval.ApprovalID,
		Branch:       attempt.Branch,
		HeadSHA:      headAfterFix,
		BaseSHA:      baseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:17:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit after second refresh: %v", err)
	}
	if queued.Status != mergeQueueStatusQueued || queued.Queue == nil {
		t.Fatalf("queue after second refresh = %+v evaluation=%+v", queued, secondEvaluation)
	}
}

func commitWorktreeFile(t *testing.T, gitPath string, gitEnv []string, worktree string, name string, content string, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, worktree, "add", name)
	runGit(t, gitPath, gitEnv, worktree, "commit", "-m", message)
	return strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, worktree, "rev-parse", "HEAD"))
}
