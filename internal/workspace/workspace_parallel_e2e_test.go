package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceParallelEndToEndSmoke(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git executable is unavailable")
	}

	fixture := newWorkspaceFixture(t, workspaceFixtureSpec{
		ID:      "workspace-e2e",
		BaseRef: fixtureWorkspaceBaseRef,
		Plans: []workspaceFixturePlan{{
			ID: "e2e",
			Stories: []workspaceFixtureStory{
				{ID: "story-a"},
				{ID: "story-b"},
				{ID: "story-c", Dependencies: []string{"story-a", "story-b"}},
			},
		}},
	})
	if filepath.IsAbs(fixture.Manifest.Plans[0].Path) {
		t.Fatalf("fixture should initialize from a relative plan path: %+v", fixture.Manifest.Plans)
	}

	hooksDir := filepath.Join(fixture.Dir, "no-hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitEnv := isolatedGitEnv(hooksDir)
	repoDir := filepath.Join(fixture.Dir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "init")
	runGit(t, gitPath, gitEnv, repoDir, "checkout", "-b", fixtureWorkspaceBaseRef)
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-smoke@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Smoke")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("workspace parallel smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	fixture.Manifest.Repo = "repo"
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)
	initResult, err := Init(InitOptions{
		ManifestPath: filepath.Join(fixture.Dir, ManifestFileName),
		WriteLock:    true,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if initResult.LockPath == "" || initResult.ViewPath == "" {
		t.Fatalf("init result missing lock/view paths: %+v", initResult)
	}
	lock, err := readWorkspaceLock(initResult.LockPath)
	if err != nil {
		t.Fatalf("read initialized lock: %v", err)
	}
	if len(lock.Plans) != 1 || lock.Plans[0].Path != "plans/e2e" {
		t.Fatalf("init lock plan paths = %+v", lock.Plans)
	}

	assertReadyState(t, fixture.Dir, "initial", []string{"e2e:story-a", "e2e:story-b"}, []string{"e2e:story-c"})
	claimA := claimNextForSmoke(t, fixture.Dir, "worker-a", "e2e:story-a", "2026-07-01T10:00:00Z")
	claimB := claimNextForSmoke(t, fixture.Dir, "worker-b", "e2e:story-b", "2026-07-01T10:00:10Z")
	if next, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-c",
		Claim:        true,
		Now:          fixedWorkspaceTime("2026-07-01T10:00:20Z"),
	}); err != nil {
		t.Fatalf("Next third claim: %v", err)
	} else if next.Status != "none" {
		t.Fatalf("dependent should not be claimable while producers are leased: %+v", next)
	}

	attemptA := startAttemptForSmoke(t, fixture.Dir, claimA, baseSHA, "2026-07-01T10:01:00Z")
	attemptB := startAttemptForSmoke(t, fixture.Dir, claimB, baseSHA, "2026-07-01T10:01:10Z")
	attempts := []AttemptResult{attemptA, attemptB}
	backupRefs := []string{}
	t.Cleanup(func() {
		for _, backupRef := range backupRefs {
			runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", backupRef)
		}
		for _, attempt := range attempts {
			runGitCleanup(gitPath, gitEnv, repoDir, "worktree", "remove", "--force", attempt.Worktree)
			runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", attempt.Branch)
		}
	})

	commandDir := filepath.Join(fixture.Dir, "command-cwd")
	if err := os.Mkdir(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runAttemptWorktreeCommand(t, gitEnv, commandDir, attemptA)
	runAttemptWorktreeCommand(t, gitEnv, commandDir, attemptB)

	completedA := completeWorkspaceE2EAttempt(t, gitPath, gitEnv, fixture.Dir, claimA, attemptA, baseSHA, "2026-07-01T10")
	backupRefs = append(backupRefs, completedA.BackupRef)
	assertReadyState(t, fixture.Dir, "after story-a merge", nil, []string{"e2e:story-c"})

	completedB := completeWorkspaceE2EAttempt(t, gitPath, gitEnv, fixture.Dir, claimB, attemptB, baseSHA, "2026-07-01T11")
	backupRefs = append(backupRefs, completedB.BackupRef)
	assertReadyState(t, fixture.Dir, "after story-b merge", []string{"e2e:story-c"}, nil)

	cleanup := planCleanupIntentForSmoke(t, fixture.Dir, claimA, attemptA, completedA, "2026-07-01T12")
	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status after cleanup intent: %v", err)
	}
	report := findExternalIntentReportByID(t, status.ExternalIntents, cleanup.IntentID)
	if report.Purpose != ExternalIntentPurposeCleanup {
		t.Fatalf("cleanup report = %+v", report)
	}
	if !containsString(status.Ready, "e2e:story-c") {
		t.Fatalf("cleanup intent should not block dependent readiness: ready=%+v blockers=%+v", status.Ready, status.Blockers)
	}

	claimC := claimNextForSmoke(t, fixture.Dir, "worker-c", "e2e:story-c", "2026-07-01T12:30:00Z")
	if claimC.Status != "claimed" {
		t.Fatalf("claim C = %+v", claimC)
	}
}

type completedSmokeAttempt struct {
	HeadSHA       string
	BaseSHA       string
	MergeIntentID string
	BackupRef     string
}

type cleanupSmokeIntent struct {
	IntentID string
}

func claimNextForSmoke(t *testing.T, workspaceDir string, agentID string, wantMergeUnit string, at string) NextResult {
	t.Helper()
	claim, err := Next(NextOptions{
		WorkspaceDir:  workspaceDir,
		AgentID:       agentID,
		Claim:         true,
		LeaseDuration: 8 * time.Hour,
		Now:           fixedWorkspaceTime(at),
	})
	if err != nil {
		t.Fatalf("Next %s: %v", wantMergeUnit, err)
	}
	if claim.Status != "claimed" || claim.MergeUnitID != wantMergeUnit {
		t.Fatalf("claim %s = %+v", wantMergeUnit, claim)
	}
	return claim
}

func startAttemptForSmoke(t *testing.T, workspaceDir string, claim NextResult, baseSHA string, at string) AttemptResult {
	t.Helper()
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		BaseSHA:      baseSHA,
		Now:          fixedWorkspaceTime(at),
	})
	if err != nil {
		t.Fatalf("StartAttempt %s: %v", claim.MergeUnitID, err)
	}
	if attempt.BaseSHA != baseSHA || len(attempt.Commands) != 1 || !strings.HasPrefix(attempt.Commands[0], "git -C ") {
		t.Fatalf("attempt packet = %+v", attempt)
	}
	return attempt
}

func runAttemptWorktreeCommand(t *testing.T, gitEnv []string, commandDir string, attempt AttemptResult) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(attempt.Worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	runShellCommand(t, gitEnv, commandDir, attempt.Commands[0])
}

func completeWorkspaceE2EAttempt(t *testing.T, gitPath string, gitEnv []string, workspaceDir string, claim NextResult, attempt AttemptResult, baseSHA string, hourPrefix string) completedSmokeAttempt {
	t.Helper()
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedWorkspaceTime(hourPrefix + ":02:00Z"),
	}); err != nil {
		t.Fatalf("Transition start %s: %v", claim.MergeUnitID, err)
	}

	storyFile := filepath.Join(attempt.Worktree, attempt.PlanMergeUnitID+".txt")
	if err := os.WriteFile(storyFile, []byte("implemented "+attempt.PlanMergeUnitID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, attempt.Worktree, "add", filepath.Base(storyFile))
	runGit(t, gitPath, gitEnv, attempt.Worktree, "commit", "-m", "implement "+attempt.PlanMergeUnitID)

	refresh, err := RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Local:        true,
		Worktree:     attempt.Worktree,
		NewBase:      baseSHA,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./internal/workspace -run TestWorkspaceParallelEndToEndSmoke",
			Status:  "passed",
		}},
		Now: fixedWorkspaceTime(hourPrefix + ":03:00Z"),
	})
	if err != nil {
		t.Fatalf("RefreshBranch %s: %v", claim.MergeUnitID, err)
	}
	headSHA := refresh.Evidence.PostHead
	if refresh.Status != RefreshStatusSucceeded || headSHA == "" || refresh.Evidence.NewBase != baseSHA {
		t.Fatalf("refresh %s = %+v", claim.MergeUnitID, refresh)
	}

	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionMerge},
		Branch:       attempt.Branch,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		MaxUses:      1,
		ExpiresAt:    parseWorkspaceTestTime("2027-07-01T00:00:00Z"),
		Now:          fixedWorkspaceTime(hourPrefix + ":04:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval %s: %v", claim.MergeUnitID, err)
	}
	evaluation, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime(hourPrefix + ":05:00Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates before evidence %s: %v", claim.MergeUnitID, err)
	}
	recordSmokeGateEvidence(t, workspaceDir, claim, attempt, evaluation.InputHash, headSHA, baseSHA, "review", "", "reviewer-a", hourPrefix+":06:00Z")
	recordSmokeGateEvidence(t, workspaceDir, claim, attempt, evaluation.InputHash, headSHA, baseSHA, "security", "gosec ./...", "", hourPrefix+":07:00Z")
	recordSmokeGateEvidence(t, workspaceDir, claim, attempt, evaluation.InputHash, headSHA, baseSHA, "test", "go test ./internal/workspace", "", hourPrefix+":08:00Z")
	evaluation, err = EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime(hourPrefix + ":08:30Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates after evidence %s: %v", claim.MergeUnitID, err)
	}
	for _, gate := range []string{"review", "security", "test"} {
		if got := gateStatusByName(evaluation.Gates)[gate].Status; got != GateStatusPassed {
			t.Fatalf("gate %s for %s = %s, gates=%+v", gate, claim.MergeUnitID, got, evaluation.Gates)
		}
	}
	queued, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		ApprovalID:   approval.Approval.ApprovalID,
		Branch:       attempt.Branch,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		Now:          fixedWorkspaceTime(hourPrefix + ":09:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit %s: %v", claim.MergeUnitID, err)
	}
	if queued.Status != mergeQueueStatusQueued || queued.Queue == nil {
		t.Fatalf("queue %s = %+v", claim.MergeUnitID, queued)
	}

	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
		WorkspaceDir:     workspaceDir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          claim.AgentID,
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		Branch:           attempt.Branch,
		RequestedHeadSHA: headSHA,
		ExpectedBaseSHA:  baseSHA,
		Now:              fixedWorkspaceTime(hourPrefix + ":10:00Z"),
	})
	if err != nil {
		t.Fatalf("PlanExternalProviderCommand merge %s: %v", claim.MergeUnitID, err)
	}
	if len(planned.Plan.Commands) != 4 ||
		!strings.Contains(planned.Plan.ProviderCommand, "gh pr merge") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--match-head-commit "+headSHA) {
		t.Fatalf("merge provider plan = %+v", planned.Plan)
	}
	mergeIntent, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     workspaceDir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          claim.AgentID,
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		Branch:           attempt.Branch,
		RequestedHeadSHA: headSHA,
		ExpectedBaseSHA:  baseSHA,
		Now:              fixedWorkspaceTime(hourPrefix + ":10:30Z"),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent merge %s: %v", claim.MergeUnitID, err)
	}
	recorded, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		IntentID:     mergeIntent.Intent.IntentID,
		Status:       ExternalResultSucceeded,
		Details:      "provider-safe smoke recorded merge success",
		Now:          fixedWorkspaceTime(hourPrefix + ":11:00Z"),
	})
	if err != nil {
		t.Fatalf("RecordExternalIntentResult merge %s: %v", claim.MergeUnitID, err)
	}
	if !recorded.Result.Accepted {
		t.Fatalf("merge result should be accepted: %+v", recorded.Result)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey:         headSHA,
			evidenceExternalIntentIDsKey: []string{mergeIntent.Intent.IntentID},
		},
		Now: fixedWorkspaceTime(hourPrefix + ":12:00Z"),
	}); err != nil {
		t.Fatalf("Transition complete %s: %v", claim.MergeUnitID, err)
	}
	return completedSmokeAttempt{
		HeadSHA:       headSHA,
		BaseSHA:       baseSHA,
		MergeIntentID: mergeIntent.Intent.IntentID,
		BackupRef:     refresh.Evidence.BackupRef,
	}
}

func recordSmokeGateEvidence(t *testing.T, workspaceDir string, claim NextResult, attempt AttemptResult, inputHash string, headSHA string, baseSHA string, gate string, command string, reviewer string, at string) {
	t.Helper()
	if _, err := RecordGateEvidence(GateEvidenceOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Gate:         gate,
		Status:       GateStatusPassed,
		InputHash:    inputHash,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		Command:      command,
		Reviewer:     reviewer,
		Summary:      gate + " passed in workspace e2e smoke",
		Now:          fixedWorkspaceTime(at),
	}); err != nil {
		t.Fatalf("RecordGateEvidence %s: %v", gate, err)
	}
}

func planCleanupIntentForSmoke(t *testing.T, workspaceDir string, claim NextResult, attempt AttemptResult, completed completedSmokeAttempt, hourPrefix string) cleanupSmokeIntent {
	t.Helper()
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionRemoteDelete},
		Branch:       attempt.Branch,
		HeadSHA:      completed.HeadSHA,
		BaseSHA:      completed.BaseSHA,
		MaxUses:      1,
		ExpiresAt:    parseWorkspaceTestTime("2027-07-01T00:00:00Z"),
		Now:          fixedWorkspaceTime(hourPrefix + ":13:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval cleanup: %v", err)
	}
	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
		WorkspaceDir:     workspaceDir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          claim.AgentID,
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionRemoteDelete,
		Branch:           attempt.Branch,
		RequestedHeadSHA: completed.HeadSHA,
		ExpectedBaseSHA:  completed.BaseSHA,
		Now:              fixedWorkspaceTime(hourPrefix + ":13:30Z"),
	})
	if err != nil {
		t.Fatalf("PlanExternalProviderCommand cleanup: %v", err)
	}
	if !strings.Contains(planned.Plan.ProviderCommand, "--force-with-lease=refs/heads/"+attempt.Branch+":"+completed.HeadSHA) {
		t.Fatalf("cleanup provider plan = %s", planned.Plan.ProviderCommand)
	}
	cleanupIntent, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     workspaceDir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          claim.AgentID,
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionRemoteDelete,
		Branch:           attempt.Branch,
		RequestedHeadSHA: completed.HeadSHA,
		ExpectedBaseSHA:  completed.BaseSHA,
		Now:              fixedWorkspaceTime(hourPrefix + ":14:00Z"),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent cleanup: %v", err)
	}
	return cleanupSmokeIntent{IntentID: cleanupIntent.Intent.IntentID}
}

func assertReadyState(t *testing.T, workspaceDir string, stage string, wantReady []string, wantNotReady []string) {
	t.Helper()
	status, err := Status(workspaceDir)
	if err != nil {
		t.Fatalf("%s Status: %v", stage, err)
	}
	for _, mergeUnitID := range wantReady {
		if !containsString(status.Ready, mergeUnitID) {
			t.Fatalf("%s expected %s ready: ready=%+v blocked=%+v blockers=%+v", stage, mergeUnitID, status.Ready, status.Blocked, status.Blockers)
		}
	}
	for _, mergeUnitID := range wantNotReady {
		if containsString(status.Ready, mergeUnitID) {
			t.Fatalf("%s expected %s not ready: ready=%+v blocked=%+v blockers=%+v", stage, mergeUnitID, status.Ready, status.Blocked, status.Blockers)
		}
	}
}
