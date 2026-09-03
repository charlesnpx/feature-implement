package workspace_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	"github.com/charlesnpx/feature-implement/internal/workspacecmd"
)

func TestWorkspaceInitializationUsesCommittedPlanHeadCheckpoint(t *testing.T) {
	t.Parallel()

	root := committedPlanBundleRepository(t)
	bundle := mustLoadCheckpointBundle(t, root)

	verified, err := workspace.VerifyPlanLockCheckpoint(context.Background(), bundle)
	if err != nil {
		t.Fatalf("verify committed checkpoint: %v", err)
	}
	if verified.CheckpointID().IsZero() ||
		verified.ArtifactDigest().IsZero() ||
		!strings.HasPrefix(verified.CheckpointID().String(), "sha256:") {
		t.Fatalf("verified checkpoint is incomplete: %#v", verified)
	}

	runtimeRoot := filepath.Join(canonicalMaterializationTestTempDir(t), "runtime")
	request, err := json.Marshal(map[string]any{
		"schema_version": 2,
		"occurred_at":    "2026-07-23T14:03:00Z",
		"worktree_root":  workspaceTestWorktreeRoot(t, runtimeRoot),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspacecmd.Execute(context.Background(), workspacecmd.Options{
		Action: "init", BundleDir: root, WorkspaceDir: runtimeRoot, Input: request,
	})
	if err != nil {
		t.Fatalf("initialize committed checkpoint bundle: %v", err)
	}
	initialized, ok := result.(workspacecmd.InitializationResult)
	if !ok || initialized.Status != "initialized" {
		t.Fatalf("initialization result = %#v", result)
	}
	if initialized.PlanCheckpoint != verified.CheckpointID().String() {
		t.Fatalf("initialized checkpoint = %s, want %s", initialized.PlanCheckpoint, verified.CheckpointID())
	}
	artifactPath := filepath.Join(runtimeRoot, workspace.WorkspaceStateDirectoryName, workspace.PlanCheckpointArtifactFileName)
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read runtime checkpoint artifact: %v", err)
	}
	if workspace.DigestBytes(artifact) != verified.ArtifactDigest() {
		t.Fatalf("runtime checkpoint artifact digest = %s, want %s", workspace.DigestBytes(artifact), verified.ArtifactDigest())
	}

	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(runtimeRoot)
	if err != nil {
		t.Fatalf("read initialized journal: %v", err)
	}
	event, ok := snapshot.Records()[0].Event().(workspace.WorkspaceInitializedJournalEvent)
	if !ok {
		t.Fatalf("first event = %T", snapshot.Records()[0].Event())
	}
	if event.PlanCheckpoint() != verified.CheckpointID() ||
		event.PlanCheckpointArtifactDigest() != verified.ArtifactDigest() {
		t.Fatalf("initialization event checkpoint = %s/%s, want %s/%s",
			event.PlanCheckpoint(), event.PlanCheckpointArtifactDigest(),
			verified.CheckpointID(), verified.ArtifactDigest())
	}
}

func TestPlanCheckpointVerificationRejectsDirtyPlanRepository(t *testing.T) {
	t.Parallel()

	root := committedPlanBundleRepository(t)
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.VerifyPlanLockCheckpoint(context.Background(), mustLoadCheckpointBundle(t, root)); err == nil ||
		!strings.Contains(err.Error(), "plan repository must be clean") {
		t.Fatalf("dirty plan repository error = %v", err)
	}
}

func committedPlanBundleRepository(t *testing.T) string {
	t.Helper()
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	root := filepath.Join(canonicalMaterializationTestTempDir(t), "bundle")
	if err := os.MkdirAll(filepath.Join(root, "plans"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCheckpointBundleFile(t, root, workspace.WorkspaceBundleFileName, `{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/alpha.yaml"],
  "execution_config": "config/execution.yaml"
}`)
	writeCheckpointBundleFile(t, root, "feature.workspace.yaml", `schema_version: 2
id: checkpoint-workspace
mode: local
repository:
  root: `+repositoryRoot+`
base_ref: refs/heads/main
base_commit: `+base.String()+`
feature_branch: feature/checkpoint-workspace
execution_config: config/execution.yaml
plans:
  - id: alpha-plan
    source: plans/alpha.yaml
dependencies: []
`)
	writeCheckpointBundleFile(t, root, "plans/alpha.yaml", `schema_version: 2
id: alpha-plan
title: Alpha Plan
stories:
  - id: story-one
    summary: Establish the first contract.
    acceptance:
      - The workspace initializes from a committed bundle.
    implementation:
      - Verify the committed lock projection.
    testing:
      - Run focused workspace initialization tests.
    dependencies: []
merge_units:
  - id: unit-one
    name: Unit One
    story_ids:
      - story-one
`)
	writeCheckpointBundleFile(t, root, "config/execution.yaml", `schema_version: 2
policy:
  require_passing_checks: true
  allow_write_network: false
  max_attempts: 1
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 1
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      checkpoint: pause_only
      escalation: allowed
      serial_segment: command-segment
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 1
`)
	if _, err := workspacecmd.Execute(context.Background(), workspacecmd.Options{
		Action:           "validate",
		BundleDir:        root,
		WriteLocks:       true,
		GeneratorVersion: "test",
	}); err != nil {
		t.Fatalf("write generated locks: %v", err)
	}
	runGitSetup(t, root, "init", "--initial-branch=main", ".")
	runGitSetup(t, root, "config", "user.name", "Checkpoint Test")
	runGitSetup(t, root, "config", "user.email", "checkpoint@example.invalid")
	runGitSetup(t, root, "add", "--", ".")
	runGitSetup(t, root, "commit", "-m", "committed plan locks")
	return root
}

func writeCheckpointBundleFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustLoadCheckpointBundle(t *testing.T, root string) workspace.WorkspaceBundle {
	t.Helper()
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}
