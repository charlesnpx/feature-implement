package workspace

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanExternalProviderPushCommands(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	before := len(readTestJournalEvents(t, fixture.Dir))

	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("PlanExternalProviderCommand: %v", err)
	}
	if planned.Status != "planned" || planned.Intent.IntentID == "" || planned.Intent.IdempotencyKey == "" {
		t.Fatalf("planned = %+v", planned)
	}
	if planned.Intent.Status != "planned" {
		t.Fatalf("intent status = %s, want planned", planned.Intent.Status)
	}
	if got := len(readTestJournalEvents(t, fixture.Dir)); got != before {
		t.Fatalf("planning appended events: got %d want %d", got, before)
	}
	if len(planned.Plan.Commands) != 4 {
		t.Fatalf("commands = %+v", planned.Plan.Commands)
	}
	if !strings.Contains(planned.Plan.ApprovalCommand, "feature workspace approve check") ||
		!strings.Contains(planned.Plan.ApprovalCommand, "--action push") {
		t.Fatalf("approval command = %s", planned.Plan.ApprovalCommand)
	}
	if !strings.Contains(planned.Plan.IntentCommand, "feature workspace external intent reserve") ||
		!strings.Contains(planned.Plan.IntentCommand, "--approval "+approval.Approval.ApprovalID) ||
		!strings.Contains(planned.Plan.IntentCommand, "--head-sha head-sha") {
		t.Fatalf("intent command = %s", planned.Plan.IntentCommand)
	}
	wantProvider := "test \"$(git -C " + attempt.Worktree + " rev-parse HEAD)\" = head-sha && git -C " + attempt.Worktree + " push -u origin head-sha:refs/heads/feature/test"
	if planned.Plan.ProviderCommand != wantProvider {
		t.Fatalf("provider command = %s, want %s", planned.Plan.ProviderCommand, wantProvider)
	}
	if !strings.Contains(planned.Plan.ResultCommand, "--intent "+planned.Intent.IntentID) ||
		!strings.Contains(planned.Plan.ResultCommand, "--status succeeded") {
		t.Fatalf("result command = %s", planned.Plan.ResultCommand)
	}
}

func TestPlanExternalProviderOpenPRMarkers(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionOpenPR)
	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionOpenPR,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Title:            "Story implementation",
		Body:             "Summary body",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("PlanExternalProviderCommand: %v", err)
	}
	if !strings.Contains(planned.Plan.ProviderCommand, "gh pr create") ||
		!strings.HasPrefix(planned.Plan.ProviderCommand, "test \"$(git -C "+attempt.Worktree+" ls-remote origin refs/heads/feature/test | awk '{print $1}')\" = head-sha && ") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--base workspace-orchestration") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--head feature/test") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--title 'Story implementation'") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--body ") ||
		strings.Count(planned.Plan.ProviderCommand, "ls-remote origin refs/heads/feature/test") != 2 ||
		!strings.HasSuffix(planned.Plan.ProviderCommand, " && test \"$(git -C "+attempt.Worktree+" ls-remote origin refs/heads/feature/test | awk '{print $1}')\" = head-sha") {
		t.Fatalf("provider command = %s", planned.Plan.ProviderCommand)
	}
	marker := parseProviderMarker(t, planned.Plan.PRBody)
	if marker.WorkspaceID != planned.WorkspaceID ||
		marker.MergeUnitID != claim.MergeUnitID ||
		marker.AttemptID != attempt.AttemptID ||
		marker.IntentID != planned.Intent.IntentID ||
		marker.HeadSHA != "head-sha" ||
		marker.BaseSHA != "base-sha" ||
		marker.Action != ExternalActionOpenPR ||
		marker.Target != "branch:feature/test" {
		t.Fatalf("marker = %+v planned = %+v", marker, planned)
	}
}

func TestPlanExternalProviderPushCommandRejectsMovedLocalHead(t *testing.T) {
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
	remoteDir := filepath.Join(root, "remote.git")
	runGit(t, gitPath, gitEnv, root, "init", "--bare", remoteDir)
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitPath, gitEnv, repoDir, "init")
	runGit(t, gitPath, gitEnv, repoDir, "checkout", "-b", fixtureWorkspaceBaseRef)
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.email", "feature-test@example.test")
	runGit(t, gitPath, gitEnv, repoDir, "config", "user.name", "Feature Test")
	runGit(t, gitPath, gitEnv, repoDir, "remote", "add", "origin", remoteDir)
	headSHA := commitWorktreeFile(t, gitPath, gitEnv, repoDir, "story.txt", "planned\n", "planned head")

	fixture, claim, attempt := newApprovalAttemptFixture(t)
	approval := grantExternalIntentApprovalForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, claim.AgentID, claim.LeaseID, ExternalActionPush, "feature/test", "", headSHA, attempt.BaseSHA, "2026-06-17T10:02:00Z")
	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          claim.AgentID,
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/test",
		RequestedHeadSHA: headSHA,
		ExpectedBaseSHA:  attempt.BaseSHA,
		Worktree:         repoDir,
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("PlanExternalProviderCommand: %v", err)
	}
	if !strings.Contains(planned.Plan.ProviderCommand, headSHA+":refs/heads/feature/test") {
		t.Fatalf("provider command is not SHA-pinned: %s", planned.Plan.ProviderCommand)
	}

	commitWorktreeFile(t, gitPath, gitEnv, repoDir, "story.txt", "moved\n", "moved head")
	output, err := runShellCommandOutput(gitEnv, root, planned.Plan.ProviderCommand)
	if err == nil {
		t.Fatalf("moved HEAD command unexpectedly succeeded:\n%s", output)
	}
	refsOutput, refsErr := runGitCommandOutput(gitPath, gitEnv, root, "--git-dir", remoteDir, "show-ref", "--heads")
	if refsErr != nil && strings.TrimSpace(refsOutput) != "" {
		t.Fatalf("show remote refs failed: %v\n%s", refsErr, refsOutput)
	}
	if strings.TrimSpace(refsOutput) != "" {
		t.Fatalf("remote refs changed after failed command")
	}
}

func TestPlanExternalProviderMergeAndRemoteDeleteCommands(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionMerge)
		planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
			WorkspaceDir:     fixture.Dir,
			MergeUnitID:      claim.MergeUnitID,
			AttemptID:        attempt.AttemptID,
			AgentID:          "worker-a",
			LeaseID:          claim.LeaseID,
			ApprovalID:       approval.Approval.ApprovalID,
			Action:           ExternalActionMerge,
			PR:               "35",
			RequestedHeadSHA: "head-sha",
			ExpectedBaseSHA:  "base-sha",
			Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
		})
		if err != nil {
			t.Fatalf("PlanExternalProviderCommand merge: %v", err)
		}
		wantProvider := "cd " + attempt.Worktree + " && gh pr merge 35 --merge"
		if planned.Plan.ProviderCommand != wantProvider {
			t.Fatalf("merge provider command = %s", planned.Plan.ProviderCommand)
		}
		if !strings.Contains(planned.Plan.ApprovalCommand, "--action merge") ||
			!strings.Contains(planned.Plan.IntentCommand, "--pr 35") {
			t.Fatalf("merge plan = %+v", planned.Plan)
		}
	})

	t.Run("remote delete", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionRemoteDelete)
		planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
			WorkspaceDir:     fixture.Dir,
			MergeUnitID:      claim.MergeUnitID,
			AttemptID:        attempt.AttemptID,
			AgentID:          "worker-a",
			LeaseID:          claim.LeaseID,
			ApprovalID:       approval.Approval.ApprovalID,
			Action:           ExternalActionRemoteDelete,
			Branch:           "feature/test",
			RequestedHeadSHA: "head-sha",
			ExpectedBaseSHA:  "base-sha",
			Remote:           "upstream",
			Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
		})
		if err != nil {
			t.Fatalf("PlanExternalProviderCommand remote delete: %v", err)
		}
		wantProvider := "git -C " + attempt.Worktree + " push upstream --delete feature/test"
		if planned.Plan.ProviderCommand != wantProvider {
			t.Fatalf("remote delete provider command = %s", planned.Plan.ProviderCommand)
		}
		if !strings.Contains(planned.Plan.ApprovalCommand, "--action remote-delete") ||
			!strings.Contains(planned.Plan.IntentCommand, "--action remote-delete") {
			t.Fatalf("remote delete plan must use separate approval/intent: %+v", planned.Plan)
		}
	})
}

func TestPlanExternalProviderRequiresMatchingApproval(t *testing.T) {
	t.Run("missing approval", func(t *testing.T) {
		fixture, claim, attempt, _ := newExternalIntentFixture(t, ExternalActionPush)
		_, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
			WorkspaceDir:     fixture.Dir,
			MergeUnitID:      claim.MergeUnitID,
			AttemptID:        attempt.AttemptID,
			AgentID:          "worker-a",
			LeaseID:          claim.LeaseID,
			ApprovalID:       "approval-missing",
			Action:           ExternalActionPush,
			Branch:           "feature/test",
			RequestedHeadSHA: "head-sha",
			ExpectedBaseSHA:  "base-sha",
			Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "approval not found") {
			t.Fatalf("PlanExternalProviderCommand error = %v, want missing approval", err)
		}
	})

	t.Run("mismatched approval", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
		_, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
			WorkspaceDir:     fixture.Dir,
			MergeUnitID:      claim.MergeUnitID,
			AttemptID:        attempt.AttemptID,
			AgentID:          "worker-a",
			LeaseID:          claim.LeaseID,
			ApprovalID:       approval.Approval.ApprovalID,
			Action:           ExternalActionPush,
			Branch:           "feature/test",
			RequestedHeadSHA: "different-head",
			ExpectedBaseSHA:  "base-sha",
			Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "is for head head-sha, not different-head") {
			t.Fatalf("PlanExternalProviderCommand error = %v, want head mismatch", err)
		}
	})

	t.Run("exhausted approval", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
		for i := 0; i < approval.Approval.MaxUses; i++ {
			if _, err := ConsumeApproval(ApprovalConsumeOptions{
				WorkspaceDir: fixture.Dir,
				MergeUnitID:  claim.MergeUnitID,
				AttemptID:    attempt.AttemptID,
				ApprovalID:   approval.Approval.ApprovalID,
				Action:       ExternalActionPush,
				Branch:       "feature/test",
				HeadSHA:      "head-sha",
				BaseSHA:      "base-sha",
				Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
			}); err != nil {
				t.Fatalf("ConsumeApproval %d: %v", i, err)
			}
		}
		_, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
			WorkspaceDir:     fixture.Dir,
			MergeUnitID:      claim.MergeUnitID,
			AttemptID:        attempt.AttemptID,
			AgentID:          "worker-a",
			LeaseID:          claim.LeaseID,
			ApprovalID:       approval.Approval.ApprovalID,
			Action:           ExternalActionPush,
			Branch:           "feature/test",
			RequestedHeadSHA: "head-sha",
			ExpectedBaseSHA:  "base-sha",
			Now:              fixedJournalTime("2026-06-17T10:04:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "has no uses remaining") {
			t.Fatalf("PlanExternalProviderCommand error = %v, want exhausted approval", err)
		}
	})
}

func parseProviderMarker(t *testing.T, body string) ExternalProviderMarker {
	t.Helper()
	const prefix = "<!-- feature-workspace "
	const suffix = " -->"
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatalf("marker prefix missing from %q", body)
	}
	raw := body[start+len(prefix):]
	end := strings.Index(raw, suffix)
	if end < 0 {
		t.Fatalf("marker suffix missing from %q", body)
	}
	var marker ExternalProviderMarker
	if err := json.Unmarshal([]byte(raw[:end]), &marker); err != nil {
		t.Fatalf("parse marker: %v\n%s", err, raw[:end])
	}
	return marker
}

func runShellCommandOutput(env []string, dir string, command string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runGitCommandOutput(gitPath string, gitEnv []string, dir string, args ...string) (string, error) {
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv
	output, err := cmd.CombinedOutput()
	return string(output), err
}
