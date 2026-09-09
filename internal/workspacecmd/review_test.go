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
	"github.com/charlesnpx/witness/contract/charter"
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

func TestReviewRunRecordsObservedSatisfiedSubprocess(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t, true)
	request := reviewRunTestRequest(t, fixture)
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeCharterPath, err := filepath.Rel(workingDirectory, fixture.charterPath)
	if err != nil {
		t.Fatal(err)
	}
	resolvedCharterPath, err := filepath.EvalSymlinks(fixture.charterPath)
	if err != nil {
		t.Fatal(err)
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
	completion, err := witnessreview.NewReviewCompletionDocument(
		request,
		evidence,
		map[string]string{"reviewer-a": workspace.DigestBytes([]byte("fake reviewer report")).String()},
		witnessreview.CompletionVerdictSatisfied,
	)
	if err != nil {
		t.Fatal(err)
	}
	fakeDirectory := canonicalWorkspaceCommandTempDir(t)
	installReviewRunFake(t, fakeDirectory, request, completion, fixture.charterPath, 0, true)
	t.Setenv("FEATURE_TEST_REVIEW_EXPECTED_CHARTER", resolvedCharterPath)
	t.Setenv("PATH", fakeDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "run",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		CharterPath:  relativeCharterPath,
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
	if !ok || detail.Verdict != string(workspace.ReviewGateSatisfied) || detail.ExitCode != 0 {
		t.Fatalf("satisfied review run detail = %#v", run.Detail)
	}
}

func TestReviewRunRecordsObservedNotSatisfiedSubprocess(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t, true)
	request := reviewRunTestRequest(t, fixture)
	var err error
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
	completion, err := witnessreview.NewReviewCompletionDocument(
		request, evidence, map[string]string{}, witnessreview.CompletionVerdictNotSatisfied,
	)
	if err != nil {
		t.Fatal(err)
	}
	fakeDirectory := canonicalWorkspaceCommandTempDir(t)
	installReviewRunFake(t, fakeDirectory, request, completion, fixture.charterPath, 20, true)
	t.Setenv("PATH", fakeDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "run",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		CharterPath:  fixture.charterPath,
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
}

func TestReviewRunRecordsFailedToRunWhenRequestCharterDiffers(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t, true)
	request := reviewRunTestRequest(t, fixture)
	request.CharterHash = workspace.DigestBytes([]byte("different-test-charter")).String()
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
	completion, err := witnessreview.NewReviewCompletionDocument(
		request,
		evidence,
		map[string]string{"reviewer-a": workspace.DigestBytes([]byte("fake reviewer report")).String()},
		witnessreview.CompletionVerdictSatisfied,
	)
	if err != nil {
		t.Fatal(err)
	}
	fakeDirectory := canonicalWorkspaceCommandTempDir(t)
	installReviewRunFake(t, fakeDirectory, request, completion, fixture.charterPath, 0, true)
	t.Setenv("PATH", fakeDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "run",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		CharterPath:  fixture.charterPath,
		Input:        reviewCommandInput(fixture.attemptID, "2026-09-03T12:00:02Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, ok := result.(ReviewCommandResult)
	if !ok {
		t.Fatalf("review run result = %#v", result)
	}
	detail, ok := run.Detail.(ReviewRunView)
	if !ok || detail.Verdict != string(workspace.ReviewGateFailedToRun) || detail.ExitCode != 0 ||
		!strings.Contains(detail.Observation, "Charter") {
		t.Fatalf("Charter-mismatch detail = %#v", run.Detail)
	}
}

func TestReviewRunRecordsFailedToRunWhenAdapterHasNoCompletion(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t, true)
	request := reviewRunTestRequest(t, fixture)
	fakeDirectory := canonicalWorkspaceCommandTempDir(t)
	installReviewRunFake(
		t, fakeDirectory,
		request, witnessreview.ReviewCompletionDocument{}, fixture.charterPath, 21, false,
	)
	t.Setenv("PATH", fakeDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "run",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		CharterPath:  fixture.charterPath,
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
		!strings.Contains(detail.Observation, "review-completion.json") {
		t.Fatalf("failed-to-run detail = %#v", run.Detail)
	}
	expectedRequestDigest, err := witnessreview.ReviewRequestV2Digest(request)
	if err != nil {
		t.Fatal(err)
	}
	if detail.RequestDigest != expectedRequestDigest {
		t.Fatalf("failed-to-run request digest = %q, want adapter request %q", detail.RequestDigest, expectedRequestDigest)
	}
}

func reviewRunTestRequest(t *testing.T, fixture attemptBoundaryCommandFixture) witnessreview.ReviewRequestV2Document {
	t.Helper()
	writeReviewRunTestCharter(t, fixture.charterPath)
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
		RecipeID: "defect-and-economy", Instructions: "opaque review instructions",
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
	input, err := charter.ReadFile(fixture.charterPath)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := charter.Freeze(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	return witnessreview.ReviewRequestV2Document{
		SchemaVersion: witnessreview.ReviewRequestV2,
		ConsumerIdentity: witnessreview.Identity{
			Kind: "feature-implement", ID: bundle.Definition().Workspace().ID().String(),
		},
		Subject:           witnessreview.RequestSubject{Head: dispatch.Head, Tree: dispatch.Tree},
		CharterHash:       frozen.CharterHash,
		ReviewInputDigest: workspace.DigestBytes([]byte("test-review-input")).String(),
		FrozenRecipe:      recipeBytes, RecipeDigest: recipeDigest,
		Adapter: "simple", RequiredOutputs: []string{"reviewer-a"},
	}
}

func writeReviewRunTestCharter(t *testing.T, path string) {
	t.Helper()
	input := charter.InitSkeleton("test-owner", "test-charter", "Charter used by the review run tests.")
	input.Goals = []charter.Statement{{ID: "test-goal", Statement: "The review test binds the supplied Charter."}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reviewCommandInput(attemptID workspace.ID, occurredAt string) []byte {
	return []byte(fmt.Sprintf(`{
  "schema_version": 2,
  "occurred_at": %q,
  "attempt_id": %q
}`, occurredAt, attemptID.String()))
}

func installReviewRunFake(
	t *testing.T,
	directory string,
	request witnessreview.ReviewRequestV2Document,
	completion witnessreview.ReviewCompletionDocument,
	suppliedCharterPath string,
	exitCode int,
	writeCompletion bool,
) {
	t.Helper()
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var completionBytes []byte
	if writeCompletion {
		completionBytes, err = json.Marshal(completion)
		if err != nil {
			t.Fatal(err)
		}
	}
	charterBytes, err := os.ReadFile(suppliedCharterPath)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(directory, "request.json")
	completionPath := filepath.Join(directory, "completion.json")
	charterOutputPath := filepath.Join(directory, "charter.freeze.json")
	for path, content := range map[string][]byte{
		requestPath: requestBytes, completionPath: completionBytes, charterOutputPath: charterBytes,
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	verdict := witnessreview.CompletionVerdictSatisfied
	ok := "true"
	jobs := `[{"reviewer":"reviewer-a","job_id":"fake-job","state":"completed","state_exit_code":0,"report_status":"valid","result_artifact_available":true,"transcript_complete":true,"transcript_gap":false}]`
	switch exitCode {
	case 20:
		verdict = witnessreview.CompletionVerdictNotSatisfied
		ok = "false"
	case 21:
		verdict = witnessreview.CompletionVerdictFailedToRun
		ok = "false"
		jobs = `[]`
	}
	writeCompletionValue := "0"
	if writeCompletion {
		writeCompletionValue = "1"
	}
	script := `#!/bin/sh
set -eu
[ "$#" -ge 2 ] && [ "$1" = "review" ] && [ "$2" = "run" ] || exit 2
shift 2
source_dir=""
out_dir=""
config_path=""
subject_head=""
subject_tree=""
consumer_kind=""
consumer_id=""
charter_path=""
while [ "$#" -gt 0 ]; do
  flag="$1"
  case "$flag" in
    -source-dir|-out-dir|-config|-subject-head|-subject-tree|-consumer-kind|-consumer-id|-charter)
      [ "$#" -ge 2 ] || exit 2
      value="$2"
      case "$flag" in
        -source-dir) source_dir="$value" ;;
        -out-dir) out_dir="$value" ;;
        -config) config_path="$value" ;;
        -subject-head) subject_head="$value" ;;
        -subject-tree) subject_tree="$value" ;;
        -consumer-kind) consumer_kind="$value" ;;
        -consumer-id) consumer_id="$value" ;;
        -charter) charter_path="$value" ;;
      esac
      shift 2
      ;;
    *)
      echo "unknown adapter argument: $flag" >&2
      exit 2
      ;;
  esac
done
[ -n "$source_dir" ] && [ -d "$source_dir" ] || exit 2
[ -n "$out_dir" ] && [ -d "$out_dir" ] || exit 2
[ -n "$subject_head" ] && [ -n "$subject_tree" ] || exit 2
[ "$consumer_kind" = "feature-implement" ] || exit 2
[ -n "$consumer_id" ] || exit 2
[ -n "$charter_path" ] && [ -f "$charter_path" ] || exit 2
[ -z "${FEATURE_TEST_REVIEW_EXPECTED_CHARTER:-}" ] || [ "$charter_path" = "$FEATURE_TEST_REVIEW_EXPECTED_CHARTER" ] || exit 2
cp "$FEATURE_TEST_REVIEW_REQUEST" "$out_dir/review-request.json"
cp "$FEATURE_TEST_REVIEW_CHARTER" "$out_dir/charter.freeze.json"
if [ "$FEATURE_TEST_REVIEW_WRITE_COMPLETION" = "1" ]; then
  cp "$FEATURE_TEST_REVIEW_COMPLETION" "$out_dir/review-completion.json"
fi
printf '{"ok":%s,"verdict":"%s","request_path":"review-request.json","charter_freeze_path":"charter.freeze.json","completion_path":"review-completion.json","jobs":%s}\n' \
  "$FEATURE_TEST_REVIEW_OK" "$FEATURE_TEST_REVIEW_VERDICT" "$FEATURE_TEST_REVIEW_JOBS"
exit "$FEATURE_TEST_REVIEW_EXIT"
`
	if err := os.WriteFile(filepath.Join(directory, "witness"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATURE_TEST_REVIEW_REQUEST", requestPath)
	t.Setenv("FEATURE_TEST_REVIEW_COMPLETION", completionPath)
	t.Setenv("FEATURE_TEST_REVIEW_CHARTER", charterOutputPath)
	t.Setenv("FEATURE_TEST_REVIEW_WRITE_COMPLETION", writeCompletionValue)
	t.Setenv("FEATURE_TEST_REVIEW_OK", ok)
	t.Setenv("FEATURE_TEST_REVIEW_VERDICT", verdict)
	t.Setenv("FEATURE_TEST_REVIEW_JOBS", jobs)
	t.Setenv("FEATURE_TEST_REVIEW_EXIT", fmt.Sprint(exitCode))
}
