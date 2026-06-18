package workspace

import (
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
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, repoDir, "init")
	runGit(t, gitPath, repoDir, "checkout", "-b", fixtureWorkspaceBaseRef)
	runGit(t, gitPath, repoDir, "config", "user.email", "feature-smoke@example.test")
	runGit(t, gitPath, repoDir, "config", "user.name", "Feature Smoke")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("local git smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, repoDir, "add", "README.md")
	runGit(t, gitPath, repoDir, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitOutput(t, gitPath, repoDir, "rev-parse", "HEAD"))

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
	wantCommand := "git worktree add -b " + wantBranch + " " + wantWorktree + " " + fixtureWorkspaceBaseRef
	if len(attempt.Commands) != 1 || attempt.Commands[0] != wantCommand {
		t.Fatalf("planned commands = %+v, want %q", attempt.Commands, wantCommand)
	}

	if err := os.MkdirAll(filepath.Dir(attempt.Worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	worktreeAdded := false
	t.Cleanup(func() {
		if !worktreeAdded {
			return
		}
		_ = exec.Command(gitPath, "-C", repoDir, "worktree", "remove", "--force", attempt.Worktree).Run()
		_ = exec.Command(gitPath, "-C", repoDir, "branch", "-D", attempt.Branch).Run()
	})
	runGit(t, gitPath, repoDir, "worktree", "add", "-b", attempt.Branch, attempt.Worktree, attempt.BaseRef)
	worktreeAdded = true

	gotBranch := strings.TrimSpace(runGitOutput(t, gitPath, attempt.Worktree, "rev-parse", "--abbrev-ref", "HEAD"))
	if gotBranch != attempt.Branch {
		t.Fatalf("worktree branch = %q, want %q", gotBranch, attempt.Branch)
	}
	gotSHA := strings.TrimSpace(runGitOutput(t, gitPath, attempt.Worktree, "rev-parse", "HEAD"))
	if gotSHA != baseSHA {
		t.Fatalf("worktree HEAD = %q, want %q", gotSHA, baseSHA)
	}
}

func runGit(t *testing.T, gitPath string, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, gitPath, dir, args...)
}

func runGitOutput(t *testing.T, gitPath string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
	return string(output)
}
