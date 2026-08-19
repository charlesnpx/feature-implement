package workspacecmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type attemptBoundaryCommandFixture struct {
	bundleRoot   string
	workspaceDir string
	attemptID    workspace.ID
}

func TestExecuteAttemptBoundaryRequiresKindAndRecordsBoundary(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t)
	options := Options{
		Action:       "attempt",
		Subaction:    "boundary",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		Input: attemptBoundaryCommandInput(
			fixture.attemptID, "checkpoint", true,
		),
	}
	result, err := Execute(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	mutation, ok := result.(MutationResult)
	if !ok || mutation.Status != "recorded" || mutation.Action != "attempt.boundary" {
		t.Fatalf("boundary command result = %#v", result)
	}

	journal, err := workspace.OpenWorkspaceJournal(
		fixture.workspaceDir, workspace.JournalReadOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, exists := runtime.Attempt(fixture.attemptID)
	if !exists {
		t.Fatalf("recorded boundary has no attempt %s", fixture.attemptID)
	}
	boundary, exists := attempt.CurrentBoundary()
	if !exists || boundary.Kind() != workspace.AttemptBoundaryKindCheckpoint {
		t.Fatalf("recorded command boundary = %#v exists=%v", boundary, exists)
	}

	for _, test := range []struct {
		name  string
		input []byte
	}{
		{
			name:  "absent kind",
			input: attemptBoundaryCommandInput(fixture.attemptID, "", false),
		},
		{
			name:  "unknown kind",
			input: attemptBoundaryCommandInput(fixture.attemptID, "unsupported", true),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			options.Input = test.input
			_, err := Execute(context.Background(), options)
			if err == nil {
				t.Fatal("boundary command accepted an invalid kind")
			}
			for _, required := range []string{"kind", "checkpoint", "escalation"} {
				if !strings.Contains(err.Error(), required) {
					t.Fatalf("boundary kind error = %q, missing %q", err, required)
				}
			}
		})
	}
}

func newAttemptBoundaryCommandFixture(t *testing.T) attemptBoundaryCommandFixture {
	return newAttemptCommandFixture(t, false)
}

func newReviewRecordFailureCommandFixture(t *testing.T) attemptBoundaryCommandFixture {
	return newAttemptCommandFixture(t, true)
}

func newAttemptCommandFixture(
	t *testing.T,
	withReviewLoop bool,
) attemptBoundaryCommandFixture {
	t.Helper()
	repositoryRoot := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repositoryRoot, "init", "-b", "main")
	runGitTest(t, repositoryRoot, "config", "user.name", "Feature Test")
	runGitTest(t, repositoryRoot, "config", "user.email", "feature@example.test")
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "tracked.txt"),
		[]byte("base\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repositoryRoot, "add", "tracked.txt")
	runGitTest(t, repositoryRoot, "commit", "-m", "Base")
	base := parseWorkspaceCommandGitObject(
		t, strings.TrimSpace(runGitTest(t, repositoryRoot, "rev-parse", "HEAD")),
	)

	bundleRoot := canonicalWorkspaceCommandTempDir(t)
	for _, directory := range []string{"plans", "config"} {
		if err := os.MkdirAll(filepath.Join(bundleRoot, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(relative, content string) {
		t.Helper()
		if err := os.WriteFile(
			filepath.Join(bundleRoot, relative), []byte(content), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	write(workspace.WorkspaceBundleFileName, `{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/alpha.yaml"],
  "execution_config": "config/execution.yaml"
}`)
	write("feature.workspace.yaml", fmt.Sprintf(`schema_version: 2
id: boundary-command-workspace
mode: local
repository:
  root: %q
base_ref: refs/heads/main
base_commit: %s
feature_branch: feature/boundary-command-workspace
execution_config: config/execution.yaml
plans:
  - id: alpha-plan
    source: plans/alpha.yaml
dependencies: []
`, repositoryRoot, base))
	write("plans/alpha.yaml", `schema_version: 2
id: alpha-plan
title: Boundary Command Plan
stories:
  - id: story-one
    summary: Record an attempt boundary through the command.
    acceptance:
      - Boundary command records its kind.
    implementation:
      - Call the attempt boundary command.
    testing:
      - Reject missing and unknown kinds.
    dependencies: []
merge_units:
  - id: unit-one
    name: Unit One
    story_ids:
      - story-one
`)
	reviewProfiles := ""
	reviewLoop := ""
	if withReviewLoop {
		reviewProfiles = `review_profiles:
  - id: isolation
    runner: isolation-runner
    reviewer_policy: retain
`
		reviewLoop = `    review_fix_protocol:
      subject_prefix: Review fix
      body_policy: required
      allowed_paths:
        - src/**
      frozen_paths: []
      checks: []
    review_loop:
      profiles:
        - isolation
      max_infrastructure_retries: 2
`
	}
	write("config/execution.yaml", fmt.Sprintf(`schema_version: 2
policy:
  require_passing_checks: true
  allow_write_network: false
  max_attempts: 2
  max_review_rounds: 2
  max_review_fixes: 1
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 1
%smerge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      checkpoint: pause_only
      escalation: allowed
      serial_segment: boundary-command-segment
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 1
%s`, reviewProfiles, reviewLoop))
	if _, err := Execute(context.Background(), Options{
		Action:           "validate",
		BundleDir:        bundleRoot,
		WriteLocks:       true,
		GeneratorVersion: "test",
	}); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, bundleRoot, "init", "-b", "main")
	runGitTest(t, bundleRoot, "config", "user.name", "Feature Test")
	runGitTest(t, bundleRoot, "config", "user.email", "feature@example.test")
	runGitTest(t, bundleRoot, "add", ".")
	runGitTest(t, bundleRoot, "commit", "-m", "Committed plan locks")

	workspaceDir := canonicalWorkspaceCommandTempDir(t)
	worktreeRoot := canonicalWorkspaceCommandTempDir(t)
	if _, err := Execute(context.Background(), Options{
		Action:       "init",
		BundleDir:    bundleRoot,
		WorkspaceDir: workspaceDir,
		Input: []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:00Z",
  "worktree_root": %q
}`, worktreeRoot)),
	}); err != nil {
		t.Fatal(err)
	}
	bundle, err := workspace.LoadWorkspaceBundle(bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}()
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("boundary-command-goal"), workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	mergeUnit, err := workspace.NewMergeUnitReference(
		workspace.MustID("alpha-plan"), workspace.MustID("unit-one"),
	)
	if err != nil {
		t.Fatal(err)
	}
	attemptGit := workspace.DefaultLocalAttemptGitAdapter()
	attempt, err := workspace.ReserveAttempt(
		context.Background(), journal, bundle.Definition(), attemptGit,
		workspace.ReserveAttemptRequest{
			MergeUnit: mergeUnit, AttemptNumber: 1, Goal: goal,
			OccurredAt: time.Date(2026, time.July, 25, 18, 0, 1, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = workspace.MaterializeAttempt(
		context.Background(), journal, bundle.Definition(), attemptGit,
		workspace.MaterializeAttemptRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: time.Date(2026, time.July, 25, 18, 0, 2, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if withReviewLoop {
		if err := os.WriteFile(
			filepath.Join(attempt.Worktree(), "reviewable.txt"), []byte("reviewable\n"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, attempt.Worktree(), "add", "reviewable.txt")
		runGitTest(t, attempt.Worktree(), "commit", "-m", "Reviewable implementation")
	}
	return attemptBoundaryCommandFixture{
		bundleRoot: bundleRoot, workspaceDir: workspaceDir, attemptID: attempt.AttemptID(),
	}
}

func attemptBoundaryCommandInput(
	attemptID workspace.ID,
	kind string,
	includeKind bool,
) []byte {
	kindField := ""
	if includeKind {
		kindField = fmt.Sprintf("  \"kind\": %q,\n", kind)
	}
	return []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": "2026-07-25T18:00:03Z",
  "attempt_id": %q,
%s  "evidence": [
    {
      "kind": "boundary-command-evidence",
      "digest": %q,
      "items": [{"name": "summary", "value": "boundary recorded through Execute"}]
    }
  ]
}`,
		attemptID.String(), kindField,
		workspace.DigestBytes([]byte("boundary-command-evidence")).String(),
	))
}
