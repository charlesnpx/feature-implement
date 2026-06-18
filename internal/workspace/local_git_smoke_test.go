package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const localGitSmokeEnv = "FEATURE_WORKSPACE_LOCAL_GIT_SMOKE"

func TestLocalGitAttemptWorktreeSmoke(t *testing.T) {
	if os.Getenv(localGitSmokeEnv) != "1" {
		t.Skipf("set %s=1 to run the local git worktree smoke test", localGitSmokeEnv)
	}
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

	repoDir := filepath.Join(root, "repo with spaces")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "init")
	runGit(t, gitPath, gitEnv, repoDir, "checkout", "-b", fixtureWorkspaceBaseRef)
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-smoke@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Smoke")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("local git smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	fixture := newOnePlanWorkspaceFixture(t)
	fixture.Manifest.Repo = repoDir
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      baseSHA,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if attempt.BaseRef != fixtureWorkspaceBaseRef {
		t.Fatalf("attempt base_ref = %q", attempt.BaseRef)
	}
	wantBranch := "feature/workspace-a/foundation/story-a/attempt-1"
	if attempt.Branch != wantBranch {
		t.Fatalf("attempt branch = %q, want %q", attempt.Branch, wantBranch)
	}
	wantWorktree := filepath.Join(fixture.Dir, "state", "worktrees", "workspace-a", "foundation", "story-a", "attempt-1")
	if attempt.Worktree != wantWorktree {
		t.Fatalf("attempt worktree = %q, want %q", attempt.Worktree, wantWorktree)
	}
	wantCommand := "git -C " + shellQuote(repoDir) + " worktree add -b " + shellQuote(wantBranch) + " " + shellQuote(wantWorktree) + " " + shellQuote(baseSHA)
	if len(attempt.Commands) != 1 || attempt.Commands[0] != wantCommand {
		t.Fatalf("planned commands = %+v, want %q", attempt.Commands, wantCommand)
	}

	nonRepoDir := filepath.Join(root, "not-repo")
	if err := os.Mkdir(nonRepoDir, 0o755); err != nil {
		t.Fatal(err)
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
	runShellCommand(t, gitEnv, nonRepoDir, attempt.Commands[0])
	worktreeAdded = true

	gotBranch := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, attempt.Worktree, "rev-parse", "--abbrev-ref", "HEAD"))
	if gotBranch != attempt.Branch {
		t.Fatalf("worktree branch = %q, want %q", gotBranch, attempt.Branch)
	}
	gotSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, attempt.Worktree, "rev-parse", "HEAD"))
	if gotSHA != baseSHA {
		t.Fatalf("worktree HEAD = %q, want %q", gotSHA, baseSHA)
	}
}

func TestLocalGitRefreshBranchSmoke(t *testing.T) {
	if os.Getenv(localGitSmokeEnv) != "1" {
		t.Skipf("set %s=1 to run the local git refresh smoke test", localGitSmokeEnv)
	}
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
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-smoke@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Smoke")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      baseSHA,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(attempt.Worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	worktreeAdded := false
	backupRef := ""
	t.Cleanup(func() {
		if backupRef != "" {
			runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", backupRef)
		}
		if !worktreeAdded {
			return
		}
		runGitCleanup(gitPath, gitEnv, repoDir, "worktree", "remove", "--force", attempt.Worktree)
		runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", attempt.Branch)
	})
	runGit(t, gitPath, gitEnv, repoDir, "worktree", "add", "-b", attempt.Branch, attempt.Worktree, attempt.BaseRef)
	worktreeAdded = true

	if err := os.WriteFile(filepath.Join(attempt.Worktree, "story.txt"), []byte("story work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, attempt.Worktree, "add", "story.txt")
	runGit(t, gitPath, gitEnv, attempt.Worktree, "commit", "-m", "story work")
	preHead := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, attempt.Worktree, "rev-parse", "HEAD"))

	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("new base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "base.txt")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "new base")
	newBaseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	result, err := RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Local:        true,
		NewBase:      newBaseSHA,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("RefreshBranch: %v", err)
	}
	backupRef = result.Evidence.BackupRef
	if result.Status != RefreshStatusSucceeded || result.Evidence.OldBase != baseSHA || result.Evidence.NewBase != newBaseSHA || result.Evidence.PreHead != preHead {
		t.Fatalf("refresh result = %+v", result)
	}
	if result.Evidence.PostHead == preHead || result.Evidence.BackupRef == "" {
		t.Fatalf("post refresh evidence = %+v", result.Evidence)
	}
	if len(result.Evidence.PatchIDsBefore) != 1 || len(result.Evidence.PatchIDsAfter) != 1 ||
		result.Evidence.PatchIDsBefore[0].PatchID != result.Evidence.PatchIDsAfter[0].PatchID {
		t.Fatalf("patch ids before=%+v after=%+v", result.Evidence.PatchIDsBefore, result.Evidence.PatchIDsAfter)
	}
	if len(result.Evidence.CommandResults) != 1 || result.Evidence.CommandResults[0].Command != "go test ./..." {
		t.Fatalf("command results = %+v", result.Evidence.CommandResults)
	}
	if _, err := os.Stat(filepath.Join(fixture.Dir, result.EvidencePath)); err != nil {
		t.Fatalf("expected evidence file: %v", err)
	}
	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	if conditions := findSchedulerUnit(t, view, claim.MergeUnitID).BlockingConditions; len(conditions) != 0 {
		t.Fatalf("unexpected blocking conditions = %+v", conditions)
	}
}

func TestLocalGitRefreshRejectsRemoteRefSmoke(t *testing.T) {
	if os.Getenv(localGitSmokeEnv) != "1" {
		t.Skipf("set %s=1 to run the local git refresh remote-ref smoke test", localGitSmokeEnv)
	}
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
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-smoke@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Smoke")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      baseSHA,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
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
	runGit(t, gitPath, gitEnv, repoDir, "worktree", "add", "-b", attempt.Branch, attempt.Worktree, attempt.BaseRef)
	worktreeAdded = true
	runGit(t, gitPath, gitEnv, repoDir, "remote", "add", "origin", filepath.Join(root, "origin.git"))
	runGit(t, gitPath, gitEnv, repoDir, "update-ref", "refs/remotes/origin/"+attempt.Branch, baseSHA)

	_, err = RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Local:        true,
		NewBase:      baseSHA,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "has remote ref origin/"+attempt.Branch) {
		t.Fatalf("RefreshBranch remote ref error = %v", err)
	}
}

func TestLocalGitRefreshValidationFailureSmoke(t *testing.T) {
	if os.Getenv(localGitSmokeEnv) != "1" {
		t.Skipf("set %s=1 to run the local git refresh validation failure smoke test", localGitSmokeEnv)
	}
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
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-smoke@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Smoke")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      baseSHA,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(attempt.Worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	worktreeAdded := false
	backupRef := ""
	t.Cleanup(func() {
		if backupRef != "" {
			runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", backupRef)
		}
		if !worktreeAdded {
			return
		}
		runGitCleanup(gitPath, gitEnv, repoDir, "worktree", "remove", "--force", attempt.Worktree)
		runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", attempt.Branch)
	})
	runGit(t, gitPath, gitEnv, repoDir, "worktree", "add", "-b", attempt.Branch, attempt.Worktree, attempt.BaseRef)
	worktreeAdded = true

	if err := os.WriteFile(filepath.Join(attempt.Worktree, "story.txt"), []byte("story work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, attempt.Worktree, "add", "story.txt")
	runGit(t, gitPath, gitEnv, attempt.Worktree, "commit", "-m", "story work")

	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("new base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "base.txt")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "new base")
	newBaseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	result, err := RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Local:        true,
		NewBase:      newBaseSHA,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "failed",
		}},
		Now: fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	var verificationErr RefreshVerificationError
	if !errors.As(err, &verificationErr) {
		t.Fatalf("RefreshBranch error = %T %[1]v, want RefreshVerificationError", err)
	}
	result = verificationErr.Result
	backupRef = result.Evidence.BackupRef
	if result.Status != RefreshStatusVerificationFailed || result.EvidencePath == "" ||
		!strings.Contains(result.Evidence.Verification.FailureReason, "validation command failed") {
		t.Fatalf("refresh failure result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.Dir, result.EvidencePath)); err != nil {
		t.Fatalf("expected evidence file: %v", err)
	}
	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	conditions := findSchedulerUnit(t, view, claim.MergeUnitID).BlockingConditions
	if len(conditions) != 1 {
		t.Fatalf("blocking conditions = %+v", conditions)
	}
	condition := conditions[0]
	if condition.Type != "refresh_verification_failed" || condition.EvidencePath != result.EvidencePath {
		t.Fatalf("refresh condition = %+v", condition)
	}
}

func TestLocalGitRefreshRebaseConflictRecordsFailureSmoke(t *testing.T) {
	if os.Getenv(localGitSmokeEnv) != "1" {
		t.Skipf("set %s=1 to run the local git refresh rebase conflict smoke test", localGitSmokeEnv)
	}
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
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-smoke@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Smoke")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      baseSHA,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(attempt.Worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	worktreeAdded := false
	backupRef := ""
	t.Cleanup(func() {
		if worktreeAdded {
			runGitCleanup(gitPath, gitEnv, attempt.Worktree, "rebase", "--abort")
		}
		if backupRef != "" {
			runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", backupRef)
		}
		if !worktreeAdded {
			return
		}
		runGitCleanup(gitPath, gitEnv, repoDir, "worktree", "remove", "--force", attempt.Worktree)
		runGitCleanup(gitPath, gitEnv, repoDir, "branch", "-D", attempt.Branch)
	})
	runGit(t, gitPath, gitEnv, repoDir, "worktree", "add", "-b", attempt.Branch, attempt.Worktree, attempt.BaseRef)
	worktreeAdded = true

	if err := os.WriteFile(filepath.Join(attempt.Worktree, "README.md"), []byte("story branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, attempt.Worktree, "add", "README.md")
	runGit(t, gitPath, gitEnv, attempt.Worktree, "commit", "-m", "story conflict")

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("new base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "add", "README.md")
	runGit(t, gitPath, gitEnv, repoDir, "commit", "-m", "new base conflict")
	newBaseSHA := strings.TrimSpace(runGitOutput(t, gitPath, gitEnv, repoDir, "rev-parse", "HEAD"))

	_, err = RefreshBranch(RefreshBranchOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Local:        true,
		NewBase:      newBaseSHA,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	var verificationErr RefreshVerificationError
	if !errors.As(err, &verificationErr) {
		t.Fatalf("RefreshBranch error = %T %[1]v, want RefreshVerificationError", err)
	}
	result := verificationErr.Result
	backupRef = result.Evidence.BackupRef
	if result.Status != RefreshStatusVerificationFailed || result.EvidencePath == "" ||
		!strings.Contains(result.Evidence.Verification.FailureReason, "rebase failed") {
		t.Fatalf("refresh conflict result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.Dir, result.EvidencePath)); err != nil {
		t.Fatalf("expected evidence file: %v", err)
	}
	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	conditions := findSchedulerUnit(t, view, claim.MergeUnitID).BlockingConditions
	if len(conditions) != 1 || conditions[0].Type != "refresh_verification_failed" || conditions[0].EvidencePath != result.EvidencePath {
		t.Fatalf("blocking conditions = %+v", conditions)
	}
}

func runGit(t *testing.T, gitPath string, gitEnv []string, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, gitPath, gitEnv, dir, args...)
}

func runGitOutput(t *testing.T, gitPath string, gitEnv []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
	return string(output)
}

func runShellCommand(t *testing.T, env []string, dir string, command string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", command, err, string(output))
	}
}

func runGitCleanup(gitPath string, gitEnv []string, dir string, args ...string) {
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv
	_ = cmd.Run()
}

func isolatedGitEnv(hooksDir string) []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_TRACE") {
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_CONFIG_PARAMETERS":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+hooksDir,
	)
}
