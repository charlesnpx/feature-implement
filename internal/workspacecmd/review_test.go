package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

func TestLocalReviewRepositoryAdoptsActualCleanDescendantHead(t *testing.T) {
	t.Parallel()

	repository := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.name", "Feature Test")
	runGitTest(t, repository, "config", "user.email", "feature@example.test")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Base")
	base := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))

	if err := os.WriteFile(tracked, []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Implementation")
	head := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))
	tree := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD^{tree}")))
	request, err := workspace.NewReviewRepositoryRequest(repository, base)
	if err != nil {
		t.Fatal(err)
	}
	adapter := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
	if _, err := adapter.InspectReviewSnapshot(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "attempt worktree must keep HEAD detached") {
		t.Fatalf("branch-attached attempt inspection error = %v", err)
	}
	runGitTest(t, repository, "switch", "--detach", gitObjectHex(head))
	snapshot, err := adapter.InspectReviewSnapshot(context.Background(), request)
	if err != nil || !snapshot.Clean() || snapshot.Head() != head || snapshot.Tree() != tree {
		t.Fatalf("actual review snapshot = %#v error=%v", snapshot, err)
	}

	runGitTest(t, repository, "reset", "--hard", gitObjectHex(base))
	staleRequest, err := workspace.NewReviewRepositoryRequest(repository, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.InspectReviewSnapshot(context.Background(), staleRequest); err == nil ||
		!strings.Contains(err.Error(), "descend from durable head") {
		t.Fatalf("rewound ordinary head error = %v", err)
	}
}

func TestReviewDispatchExposesFrozenConfigurationWithoutAdapterSpecificPacket(t *testing.T) {
	t.Parallel()

	fixture := newAttemptBoundaryCommandFixture(t, true)
	dispatchResult, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "dispatch",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-09-03T12:00:01Z",
  "attempt_id": "` + fixture.attemptID.String() + `"
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatched, ok := dispatchResult.(ReviewCommandResult)
	if !ok || dispatched.Action != "review.dispatch" {
		t.Fatalf("dispatch command result = %#v", dispatchResult)
	}
	dispatch, ok := dispatched.Detail.(ReviewGateDispatchView)
	if dispatch.ReviewConfigurationSource != workspace.BundledDefaultReviewConfiguration ||
		dispatch.ReviewConfigurationDigest == "" || dispatch.FrozenCopy == "" {
		t.Fatalf("generic dispatch detail = %#v", dispatch)
	}
}

func TestReviewRunRecordsObservedNotSatisfiedSubprocess(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t, true)
	dispatchResult, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "dispatch",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		Input:        reviewCommandInput(fixture.attemptID, "2026-09-03T12:00:01Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatchCommand, ok := dispatchResult.(ReviewCommandResult)
	if !ok {
		t.Fatalf("dispatch result = %#v", dispatchResult)
	}
	dispatch, ok := dispatchCommand.Detail.(ReviewGateDispatchView)
	if !ok {
		t.Fatalf("dispatch detail = %#v", dispatchCommand.Detail)
	}
	bundle, err := workspace.LoadWorkspaceBundle(fixture.bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipe := witnessreview.ReviewRecipe{
		RecipeID: dispatch.Recipe, Instructions: "opaque review instructions",
		RequiredOutputs: []string{"reviewer-a"}, Policy: map[string]any{},
	}
	recipeBytes, err := witnessreview.ReviewRecipeCanonicalBytes(recipe)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := witnessreview.ReviewRecipeDigest(recipeBytes)
	if err != nil {
		t.Fatal(err)
	}
	request := witnessreview.ReviewRequestV2Document{
		SchemaVersion: witnessreview.ReviewRequestV2,
		ConsumerIdentity: witnessreview.Identity{
			Kind: "feature-implement", ID: bundle.Definition().Workspace().ID().String(),
		},
		Subject:           witnessreview.RequestSubject{Head: dispatch.Head, Tree: dispatch.Tree},
		CharterHash:       workspace.DigestBytes([]byte("test-charter")).String(),
		ReviewInputDigest: workspace.DigestBytes([]byte("test-review-input")).String(),
		FrozenRecipe:      recipeBytes, RecipeDigest: recipeDigest,
		Adapter: dispatch.Adapter, RequiredOutputs: []string{"reviewer-a"},
	}
	observed := witnessreview.ObservedReviewExecution{
		Complete: true, ResultArtifactAvailable: true,
		ReportOutcomes: map[string]witnessreview.ObservedReportOutcome{
			"reviewer-a": {Status: witnessreview.ExecutionReportValid},
		},
	}
	evidence, err := witnessreview.NewHostExecutionEvidence(observed)
	if err != nil {
		t.Fatal(err)
	}
	reportBytes := []byte("reviewer-a report")
	reportPath := filepath.Join(canonicalWorkspaceCommandTempDir(t), "reviewer-a.json")
	if err := os.WriteFile(reportPath, reportBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	reportDigest := workspace.DigestBytes(reportBytes).String()
	completion, err := witnessreview.NewReviewCompletionDocument(
		request, evidence, map[string]string{"reviewer-a": reportDigest}, witnessreview.CompletionVerdictNotSatisfied,
	)
	if err != nil {
		t.Fatal(err)
	}
	outputBytes, err := json.Marshal(map[string]any{
		"request": request, "completion": completion,
		"report_artifacts": map[string]any{
			"reviewer-a": map[string]string{"path": reportPath, "digest": reportDigest},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fakeDirectory := canonicalWorkspaceCommandTempDir(t)
	outputPath := filepath.Join(fakeDirectory, "adapter-output.json")
	if err := os.WriteFile(outputPath, outputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeDirectory, "witness"), []byte("#!/bin/sh\ncat \"$FEATURE_TEST_REVIEW_OUTPUT\"\nexit 20\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATURE_TEST_REVIEW_OUTPUT", outputPath)
	t.Setenv("PATH", fakeDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "run",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		Input:        reviewCommandInput(fixture.attemptID, "2026-09-03T12:00:02Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, ok := result.(ReviewCommandResult)
	if !ok || run.Action != "review.run" {
		t.Fatalf("review run result = %#v", result)
	}
	detail, ok := run.Detail.(ReviewRunView)
	if !ok || detail.Verdict != string(workspace.ReviewGateNotSatisfied) || detail.ExitCode != 20 {
		t.Fatalf("review run detail = %#v", run.Detail)
	}
	artifact, ok := detail.ReportArtifacts["reviewer-a"]
	if !ok || artifact.Digest != reportDigest {
		t.Fatalf("host-observed report artifact = %#v", detail.ReportArtifacts)
	}
}

func TestReviewRunRecordsFailedToRunWhenAdapterHasNoCompletion(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t, true)
	fakeDirectory := canonicalWorkspaceCommandTempDir(t)
	if err := os.WriteFile(filepath.Join(fakeDirectory, "witness"), []byte("#!/bin/sh\nexit 21\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "run",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		Input:        reviewCommandInput(fixture.attemptID, "2026-09-03T12:00:01Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, ok := result.(ReviewCommandResult)
	if !ok {
		t.Fatalf("review run result = %#v", result)
	}
	detail, ok := run.Detail.(ReviewRunView)
	if !ok || detail.Verdict != string(workspace.ReviewGateFailedToRun) || detail.ExitCode != 21 ||
		!strings.Contains(detail.Observation, "no parseable completion") {
		t.Fatalf("failed-to-run detail = %#v", run.Detail)
	}
}

func reviewCommandInput(attemptID workspace.ID, occurredAt string) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": %q,
  "attempt_id": %q
}`, occurredAt, attemptID.String()))
}
