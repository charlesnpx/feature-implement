package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

// reviewRepositoryStub is shared by integration and completion tests. It
// observes only a supplied exact artifact and never gives an adapter access to
// an attempt worktree.
type reviewRepositoryStub struct {
	snapshot         workspace.ReviewRepositorySnapshot
	sequence         []workspace.ReviewRepositorySnapshot
	err              error
	calls            int
	finalHistoryErr  error
	finalHistory     func(int) error
	finalHistoryRuns int
}

func (repository *reviewRepositoryStub) InspectReviewSnapshot(
	context.Context,
	workspace.ReviewRepositoryRequest,
) (workspace.ReviewRepositorySnapshot, error) {
	repository.calls++
	if repository.err != nil {
		return workspace.ReviewRepositorySnapshot{}, repository.err
	}
	if len(repository.sequence) != 0 {
		next := repository.sequence[0]
		repository.sequence = repository.sequence[1:]
		return next, nil
	}
	return repository.snapshot, nil
}

func (repository *reviewRepositoryStub) VerifyFinalHistory(
	context.Context,
	workspace.CommitProtocol,
	string,
	workspace.GitObjectID,
	workspace.GitObjectID,
) error {
	repository.finalHistoryRuns++
	if repository.finalHistory != nil {
		return repository.finalHistory(repository.finalHistoryRuns)
	}
	return repository.finalHistoryErr
}

type gatedReviewHarness struct {
	attemptHarness
	attempt    workspace.RuntimeAttemptProjection
	repository *reviewRepositoryStub
	tree       workspace.GitObjectID
}

func newGatedReviewHarness(t *testing.T) *gatedReviewHarness {
	return newReviewGateHarness(t, "natural-language")
}

func newWitnessReviewHarness(t *testing.T) *gatedReviewHarness {
	return newReviewGateHarness(t, workspace.WitnessReviewGateAdapter)
}

func newReviewGateHarness(t *testing.T, adapter string) *gatedReviewHarness {
	t.Helper()
	fixture := newDefinitionFixture(t)
	manifest, err := workspace.DecodeWorkspaceManifest(fixture.sources.Workspace.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifest.RepositoryRoot(), ".gitignore"), []byte("ignored-build/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, manifest.RepositoryRoot(), "add", ".gitignore")
	runGitSetup(t, manifest.RepositoryRoot(), "-c", "user.name=Review Test", "-c", "user.email=review@example.test", "commit", "-m", "Ignore build output")
	base := parseGitHead(t, manifest.RepositoryRoot())
	fixture.sources.Workspace.Bytes = []byte(strings.Replace(
		string(fixture.sources.Workspace.Bytes), fixture.base.String(), base.String(), 1,
	))
	fixture.base = base
	fixture.sources.ExecutionConfig.Bytes = []byte(strings.Replace(
		string(fixture.sources.ExecutionConfig.Bytes),
		"    merge_unit_id: unit-one\n    profile: standard",
		"    merge_unit_id: unit-one\n    profile: standard\n    review_gate:\n      adapter: "+adapter+"\n      recipe: default\n      policy_file: policies/review.md",
		1,
	))
	fixture.sources.ReviewPolicies = []workspace.SourceArtifact{{
		Path:  "policies/review.md",
		Bytes: []byte("Review the exact artifact for the merge-unit acceptance criteria.\n"),
	}}
	harness := newAttemptHarnessFromFixture(t, fixture, "unit-one")
	attempt := harness.reserveWithLocalGit(t, "2026-09-03T12:00:00Z")
	rawTree := strings.TrimSpace(string(runGitSetup(t, attempt.Worktree(), "rev-parse", "HEAD^{tree}")))
	tree, err := workspace.ParseGitObjectID("sha1:" + rawTree)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.NewReviewRepositorySnapshot(attempt.VerifiedHead(), tree, true)
	if err != nil {
		t.Fatal(err)
	}
	return &gatedReviewHarness{
		attemptHarness: harness, attempt: attempt,
		repository: &reviewRepositoryStub{snapshot: snapshot}, tree: tree,
	}
}

func (harness *gatedReviewHarness) dispatch(t *testing.T, at string) workspace.ReviewGateDispatchResult {
	t.Helper()
	result, err := workspace.DispatchAttemptReviewGate(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.DefaultLocalAttemptGitAdapter(), workspace.ReviewGateDispatchRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, at),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (harness *gatedReviewHarness) record(
	t *testing.T,
	dispatch workspace.ReviewGateDispatch,
	verdict workspace.ReviewGateVerdict,
	at string,
) workspace.ReviewGateRecord {
	t.Helper()
	result, err := workspace.RecordAttemptReviewGate(
		harness.journal, harness.definition, workspace.RecordAttemptReviewGateRequest{
			AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatch.Digest(), Verdict: verdict,
			EvidenceDigest: workspace.DigestBytes([]byte("adapter-record-" + string(verdict))),
			OccurredAt:     mustTime(t, at),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result.GateRecord()
}

func TestReviewGateDispatchUsesFrozenCopyAndOpaquePolicy(t *testing.T) {
	t.Parallel()

	harness := newGatedReviewHarness(t)
	if err := os.Mkdir(filepath.Join(harness.attempt.Worktree(), "ignored-build"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness.attempt.Worktree(), "ignored-build", "output.bin"), []byte("build output"), 0o600); err != nil {
		t.Fatal(err)
	}
	dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
	dispatch := dispatched.Dispatch()
	if dispatched.FrozenCopy() == harness.attempt.Worktree() || !filepath.IsAbs(dispatched.FrozenCopy()) {
		t.Fatalf("adapter copy = %q, attempt worktree = %q", dispatched.FrozenCopy(), harness.attempt.Worktree())
	}
	if info, err := os.Stat(dispatched.FrozenCopy()); err != nil || !info.IsDir() {
		t.Fatalf("frozen copy is unavailable: info=%#v error=%v", info, err)
	}
	policy := []byte("Review the exact artifact for the merge-unit acceptance criteria.\n")
	if dispatch.Head() != harness.attempt.VerifiedHead() || dispatch.Tree() != harness.tree ||
		dispatch.PolicyDigest() != workspace.DigestBytes(policy) ||
		string(dispatched.Policy()) != string(policy) {
		t.Fatalf("dispatch did not bind exact artifact and policy: %#v", dispatch)
	}
	if _, err := os.Stat(filepath.Join(dispatched.FrozenCopy(), "ignored-build")); !os.IsNotExist(err) {
		t.Fatalf("frozen copy unexpectedly exposes attempt build output: %v", err)
	}
	if harness.repository.calls != 1 {
		t.Fatalf("repository snapshot calls = %d, want 1", harness.repository.calls)
	}
}

func TestSatisfiedGatePassesBothReadinessPathsOnlyForExactArtifact(t *testing.T) {
	t.Parallel()

	harness := newGatedReviewHarness(t)
	dispatch := harness.dispatch(t, "2026-09-03T12:00:01Z").Dispatch()
	record := harness.record(t, dispatch, workspace.ReviewGateSatisfied, "2026-09-03T12:00:02Z")
	readiness, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, harness.attempt.AttemptID(),
	)
	if err != nil || readiness.Head() != dispatch.Head() || readiness.Tree() != dispatch.Tree() ||
		readiness.GateRecordDigest() != record.Digest() {
		t.Fatalf("direct gate readiness = %#v error=%v", readiness, err)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	view, err := workspace.RebuildWorkspaceView(snapshot, harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	foundPassed := false
	for _, unit := range view.Gates.Units {
		if unit.AttemptID != harness.attempt.AttemptID().String() {
			continue
		}
		for _, gate := range unit.Checks {
			if gate.Name == "review" && gate.Status == workspace.GatePassed && gate.Reason == "satisfied_exact_artifact" {
				foundPassed = true
			}
		}
	}
	if !foundPassed {
		t.Fatalf("gate view did not resolve the satisfied exact artifact: %#v", view.Gates)
	}
	otherTree := mustGitObject(t, 'd')
	mismatch, err := workspace.NewReviewRepositorySnapshot(dispatch.Head(), otherTree, true)
	if err != nil {
		t.Fatal(err)
	}
	harness.repository.snapshot = mismatch
	if _, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, harness.attempt.AttemptID(),
	); err == nil || !strings.Contains(err.Error(), "no satisfied review gate") {
		t.Fatalf("tree-mismatched artifact was admitted: %v", err)
	}
}

func TestFailedToRunIsTerminalGateFactWithoutChangingAttemptLifecycle(t *testing.T) {
	t.Parallel()

	harness := newGatedReviewHarness(t)
	dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
	dispatch := dispatched.Dispatch()
	record := harness.record(t, dispatch, workspace.ReviewGateFailedToRun, "2026-09-03T12:00:02Z")
	if record.Verdict() != workspace.ReviewGateFailedToRun || record.PolicyDigest() != dispatch.PolicyDigest() {
		t.Fatalf("terminal record verdict = %q", record.Verdict())
	}
	if _, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, harness.attempt.AttemptID(),
	); err == nil {
		t.Fatal("failed-to-run gate fabricated readiness")
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, exists := runtime.Attempt(harness.attempt.AttemptID())
	if !exists || attempt.Phase() != workspace.AttemptActive {
		t.Fatalf("failed-to-run gate stranded or changed attempt state: %#v exists=%t", attempt, exists)
	}
	if _, err := os.Stat(dispatched.FrozenCopy()); !os.IsNotExist(err) {
		t.Fatalf("terminal gate left its frozen copy behind: %v", err)
	}
}

var _ workspace.ReviewRepositoryPort = (*reviewRepositoryStub)(nil)
