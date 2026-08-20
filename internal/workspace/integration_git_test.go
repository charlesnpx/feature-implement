package workspace_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type realIntegrationScenario struct {
	attemptHarness
	attempt        workspace.RuntimeAttemptProjection
	repository     *reviewRepositoryStub
	repositoryRoot string
	acceptedHead   workspace.GitObjectID
	acceptedTree   workspace.GitObjectID
}

type localIntegrationReviewRepository struct {
	git workspace.LocalCommitGitAdapter
}

func (repository localIntegrationReviewRepository) InspectReviewSnapshot(
	ctx context.Context,
	request workspace.ReviewRepositoryRequest,
) (workspace.ReviewRepositorySnapshot, error) {
	inspection, err := repository.git.InspectCleanWorktreeHead(
		ctx, request.Worktree(), request.Head(),
	)
	if err != nil {
		return workspace.ReviewRepositorySnapshot{}, err
	}
	return workspace.NewReviewRepositorySnapshot(
		inspection.Commit(), inspection.Tree(), true,
	)
}

func TestLocalGitIntegrationCreatesExactTwoParentCommitForSHA1AndSHA256(
	t *testing.T,
) {
	t.Parallel()

	for _, algorithm := range []workspace.GitHashAlgorithm{
		workspace.GitHashSHA1,
		workspace.GitHashSHA256,
	} {
		t.Run(string(algorithm), func(t *testing.T) {
			requireFullSuiteCase(
				t,
				algorithm == workspace.GitHashSHA1,
				"Git object-format permutation",
			)

			scenario := newRealIntegrationScenario(
				t, algorithm, true, workspace.GitObjectID{},
			)
			result, err := workspace.IntegrateMergeUnit(
				context.Background(),
				scenario.journal,
				scenario.definition,
				scenario.repository,
				workspace.DefaultLocalIntegrationGitAdapter(),
				workspace.IntegrateMergeUnitRequest{
					AttemptID: scenario.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T15:00:00Z",
					),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			intent := result.Intent()
			if intent.ExpectedFeatureHead() != scenario.base ||
				intent.AcceptedHead() != scenario.acceptedHead ||
				intent.AcceptedTree() != scenario.acceptedTree ||
				intent.AcceptanceMode() !=
					workspace.IntegrationAcceptanceAdoptedHead {
				t.Fatalf("real integration intent = %#v", intent)
			}
			parents := intent.Parents()
			if len(parents) != 2 || parents[0] != scenario.base ||
				parents[1] != scenario.acceptedHead {
				t.Fatalf("real integration parents = %#v", parents)
			}
			raw := []byte(runTargetGitTest(
				t, scenario.repositoryRoot,
				"cat-file", "commit", rawGitObject(result.MergeCommit()),
			))
			if !bytes.Equal(raw, intent.CommitContent()) {
				t.Fatalf(
					"real integration commit differs from intent\nactual:\n%s\nexpected:\n%s",
					raw, intent.CommitContent(),
				)
			}
			featureHead := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot,
				"rev-parse", intent.FeatureRef(),
			))
			if featureHead != rawGitObject(result.MergeCommit()) {
				t.Fatalf(
					"feature ref = %s, want %s",
					featureHead, result.MergeCommit(),
				)
			}
			if got := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot,
				"cat-file", "-t", rawGitObject(scenario.acceptedHead),
			)); got != "commit" {
				t.Fatalf(
					"accepted detached attempt object type = %q, want commit",
					got,
				)
			}
			tree := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot,
				"rev-parse",
				rawGitObject(result.MergeCommit())+"^{tree}",
			))
			if tree != rawGitObject(scenario.acceptedTree) {
				t.Fatalf(
					"integration tree = %s, want %s",
					tree, scenario.acceptedTree,
				)
			}
			if !strings.Contains(
				intent.Message(),
				"Acceptance: adopted-head:"+
					intent.AdoptedHeadEventDigest().String(),
			) {
				t.Fatalf(
					"integration message has wrong acceptance:\n%s",
					intent.Message(),
				)
			}
			assertSingleIntegrationTransition(
				t, scenario.journal, scenario.attempt.AttemptID(),
			)
		})
	}
}

func TestLocalGitIntegrationUsesExactDetachedAttemptTree(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	runtimeRoot := t.TempDir()
	initialized, err := initializeWorkspaceV2(
		t, runtimeRoot, definition,
		mustTime(t, "2026-07-25T15:30:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		runtimeRoot, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("implementation-goal"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	attemptGit := workspace.DefaultLocalAttemptGitAdapter()
	attempt, err := workspace.ReserveAttempt(
		context.Background(), journal, definition, attemptGit,
		workspace.ReserveAttemptRequest{
			MergeUnit: mustMergeUnitReference(
				t, "alpha-plan", "unit-one",
			),
			AttemptNumber: 1,
			Goal:          goal,
			OccurredAt: mustTime(
				t, "2026-07-25T15:30:01Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = workspace.MaterializeAttempt(
		context.Background(), journal, definition, attemptGit,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T15:30:02Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(attempt.Worktree(), "integration.txt"),
		[]byte("accepted implementation\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(
		t, attempt.Worktree(), "add", "--", "integration.txt",
	)
	runTargetGitTest(
		t, attempt.Worktree(),
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "--quiet", "-m", "Accepted implementation",
	)
	acceptedHead := parseGitHead(t, attempt.Worktree())
	treeText := strings.TrimSpace(runTargetGitTest(
		t, attempt.Worktree(), "rev-parse", "HEAD^{tree}",
	))
	acceptedTree, err := workspace.ParseGitObjectID(
		string(acceptedHead.Algorithm()) + ":" + treeText,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := localIntegrationReviewRepository{
		git: workspace.DefaultLocalCommitGitAdapter(),
	}
	if _, err := workspace.AdoptAttemptHead(
		context.Background(), journal, definition, repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID: attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T15:30:03Z",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	result, err := workspace.IntegrateMergeUnit(
		context.Background(), journal, definition, repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID: attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T15:30:04Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := initialized.Runtime().LocalTarget()
	if !ok {
		t.Fatal("initialized local target is missing")
	}
	if result.Intent().ExpectedFeatureHead() != target.CreatedHead() ||
		result.Intent().AcceptedHead() != acceptedHead ||
		result.Intent().AcceptedTree() != acceptedTree {
		t.Fatalf(
			"detached-attempt integration intent = %#v",
			result.Intent(),
		)
	}
	if current := parseGitHead(t, attempt.Worktree()); current != acceptedHead {
		t.Fatalf(
			"attempt worktree moved to %s, want %s",
			current, acceptedHead,
		)
	}
	if err := os.RemoveAll(attempt.Worktree()); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reinitialized, err := initializeWorkspaceV2(
		t, runtimeRoot, definition,
		mustTime(t, "2026-07-25T15:30:05Z"),
	)
	if err != nil {
		t.Fatalf(
			"re-admit workspace at integrated feature head: %v",
			err,
		)
	}
	reinitializedTarget, ok := reinitialized.Runtime().LocalTarget()
	if !ok ||
		reinitializedTarget.CreatedHead() != result.MergeCommit() ||
		reinitializedTarget.HeadRecord() == 0 {
		t.Fatalf(
			"reinitialized integrated target = %#v exists=%t",
			reinitializedTarget, ok,
		)
	}
	replayJournal, err := workspace.OpenWorkspaceJournal(
		runtimeRoot, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer replayJournal.Close()
	beforeReplay := journalRecordCount(t, replayJournal)
	replayed, err := workspace.IntegrateMergeUnit(
		context.Background(), replayJournal, definition, repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID: attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T15:30:06Z",
			),
		},
	)
	if err != nil || replayed.MergeCommit() != result.MergeCommit() {
		t.Fatalf(
			"retry completed integration after worktree cleanup = %#v error=%v",
			replayed, err,
		)
	}
	if journalRecordCount(t, replayJournal) != beforeReplay {
		t.Fatal(
			"completed integration retry after worktree cleanup appended state",
		)
	}
}

func TestLocalGitIntegrationRejectsAncestorDescendantAndUnrelatedDrift(
	t *testing.T,
) {
	t.Parallel()

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:00:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	merge := result.MergeCommit()
	descendant := createIntegrationTestCommit(
		t, scenario.repositoryRoot, scenario.acceptedTree,
		[]workspace.GitObjectID{merge}, "descendant drift",
	)
	unrelated := createIntegrationTestCommit(
		t, scenario.repositoryRoot, scenario.acceptedTree,
		nil, "unrelated drift",
	)
	tests := []struct {
		name string
		head workspace.GitObjectID
		want workspace.IntegrationRefState
	}{
		{
			name: "ancestor",
			head: scenario.acceptedHead,
			want: workspace.IntegrationRefAncestorDrift,
		},
		{
			name: "descendant",
			head: descendant,
			want: workspace.IntegrationRefDescendantDrift,
		},
		{
			name: "unrelated",
			head: unrelated,
			want: workspace.IntegrationRefUnrelatedDrift,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFullSuiteCase(
				t,
				test.name == "unrelated",
				"completed integration drift permutation",
			)

			runTargetGitTest(
				t, scenario.repositoryRoot,
				"update-ref", result.Intent().FeatureRef(),
				rawGitObject(test.head), rawGitObject(merge),
			)
			_, err := workspace.IntegrateMergeUnit(
				context.Background(),
				scenario.journal,
				scenario.definition,
				scenario.repository,
				workspace.DefaultLocalIntegrationGitAdapter(),
				workspace.IntegrateMergeUnitRequest{
					AttemptID: scenario.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T16:00:01Z",
					),
				},
			)
			if err == nil ||
				!strings.Contains(err.Error(), string(test.want)) {
				t.Fatalf("%s drift error = %v", test.want, err)
			}
			current := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot,
				"rev-parse", result.Intent().FeatureRef(),
			))
			if current != rawGitObject(test.head) {
				t.Fatalf(
					"%s drift was reset to %s",
					test.want, current,
				)
			}
			runTargetGitTest(
				t, scenario.repositoryRoot,
				"update-ref", result.Intent().FeatureRef(),
				rawGitObject(merge), rawGitObject(test.head),
			)
		})
	}
}

func TestLocalGitIntegrationRejectsCompletedSameOIDFeatureRefRecreation(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "same-object ref recreation permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:10:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	featureRef := result.Intent().FeatureRef()
	merge := result.MergeCommit()
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-d", featureRef, rawGitObject(merge),
	)
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "--create-reflog", "-m",
		"external completed same-OID recreation",
		featureRef, rawGitObject(merge),
	)

	before := journalRecordCount(t, scenario.journal)
	_, err = workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:10:01Z"),
		},
	)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"does not retain its exact merge and marker",
		) {
		t.Fatalf("completed same-OID recreation error = %v", err)
	}
	if journalRecordCount(t, scenario.journal) != before {
		t.Fatal("rejected completed same-OID recreation appended state")
	}
	current := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot, "rev-parse", featureRef,
	))
	if current != rawGitObject(merge) {
		t.Fatalf("completed same-OID recreation was reset to %s", current)
	}
}

func TestLocalGitIntegrationCompletedRetryRejectsFeatureBranchCheckout(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "completed integration checkout permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:20:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "feature-checkout")
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"worktree", "add", "--quiet", checkout,
		strings.TrimPrefix(
			result.Intent().FeatureRef(), "refs/heads/",
		),
	)
	removed := false
	t.Cleanup(func() {
		if !removed {
			runTargetGitTest(
				t, scenario.repositoryRoot,
				"worktree", "remove", "--force", checkout,
			)
		}
	})

	before := journalRecordCount(t, scenario.journal)
	_, err = workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:20:01Z"),
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "already checked out") {
		t.Fatalf("completed retry with feature checkout error = %v", err)
	}
	if journalRecordCount(t, scenario.journal) != before {
		t.Fatal("rejected completed retry appended state")
	}
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"worktree", "remove", "--force", checkout,
	)
	removed = true
	retried, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:20:02Z"),
		},
	)
	if err != nil || retried.MergeCommit() != result.MergeCommit() {
		t.Fatalf(
			"completed retry after feature checkout removal = %#v error=%v",
			retried, err,
		)
	}
}

func TestLocalGitCompletedIntegrationRetryFollowsLaterDurableFrontier(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "multi-integration durable-frontier permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	_, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:25:00Z"),
			Fault: failIntegrationOnce(
				workspace.IntegrationFaultAfterCompletion,
			),
		},
	)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			string(workspace.IntegrationFaultAfterCompletion),
		) {
		t.Fatalf("first real integration completion fault = %v", err)
	}
	firstMerge := integrationMergeFromRuntime(
		t, scenario.journal, scenario.attempt.AttemptID(),
	)

	secondCore := scenario.attemptHarness
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	secondCore.goal, err = workspace.NewGoalBinding(
		workspace.MustID("second-real-integration-goal"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	second := secondCore.reserve(t, "2026-07-25T16:25:01Z")
	second, err = workspace.MaterializeAttempt(
		context.Background(),
		secondCore.journal,
		secondCore.definition,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.MaterializeAttemptRequest{
			AttemptID:  second.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:25:02Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.Base() != firstMerge {
		t.Fatalf(
			"second real integration base = %s, want %s",
			second.Base(), firstMerge,
		)
	}
	secondAccepted := createIntegrationTestCommit(
		t,
		scenario.repositoryRoot,
		scenario.acceptedTree,
		[]workspace.GitObjectID{firstMerge},
		"second accepted attempt",
	)
	runTargetGitTest(
		t, second.Worktree(), "update-ref", "--no-deref", "HEAD",
		rawGitObject(secondAccepted),
	)
	runTargetGitTest(
		t, second.Worktree(), "reset", "--hard",
		rawGitObject(secondAccepted),
	)
	secondSnapshot, err := workspace.NewReviewRepositorySnapshot(
		secondAccepted, scenario.acceptedTree, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondRepository := &reviewRepositoryStub{
		snapshot: secondSnapshot,
	}
	if _, err := workspace.AdoptAttemptHead(
		context.Background(),
		secondCore.journal,
		secondCore.definition,
		secondRepository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  second.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:25:03Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	secondResult, err := workspace.IntegrateMergeUnit(
		context.Background(),
		secondCore.journal,
		secondCore.definition,
		secondRepository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  second.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:25:04Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	beforeRetry := journalRecordCount(t, scenario.journal)
	retried, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:25:05Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if retried.MergeCommit() != firstMerge ||
		retried.Record().Sequence() != 0 ||
		journalRecordCount(t, scenario.journal) != beforeRetry {
		t.Fatalf(
			"historical real integration retry mutated state: result=%#v records=%d/%d",
			retried, beforeRetry,
			journalRecordCount(t, scenario.journal),
		)
	}
	featureHead := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot, "rev-parse",
		secondResult.Intent().FeatureRef(),
	))
	if featureHead != rawGitObject(secondResult.MergeCommit()) {
		t.Fatalf(
			"historical retry moved current feature head to %s, want %s",
			featureHead, secondResult.MergeCommit(),
		)
	}
}

func TestLocalGitIntegrationRecoversCreatedObjectAndPublishedRef(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name  string
		point workspace.IntegrationLifecycleFaultPoint
	}{
		{
			name:  "created object at old head",
			point: workspace.IntegrationFaultAfterCommitCreated,
		},
		{
			name:  "prepared publication aborted at old head",
			point: workspace.IntegrationFaultAfterRefPrepared,
		},
		{
			name:  "published expected merge",
			point: workspace.IntegrationFaultAfterRefCAS,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFullSuiteCase(
				t,
				test.name != "prepared publication aborted at old head",
				"intermediate integration publication boundary",
			)

			scenario := newRealIntegrationScenario(
				t, workspace.GitHashSHA1, true,
				workspace.GitObjectID{},
			)
			_, err := workspace.IntegrateMergeUnit(
				context.Background(),
				scenario.journal,
				scenario.definition,
				scenario.repository,
				workspace.DefaultLocalIntegrationGitAdapter(),
				workspace.IntegrateMergeUnitRequest{
					AttemptID: scenario.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T16:30:00Z",
					),
					Fault: failIntegrationOnce(test.point),
				},
			)
			if err == nil ||
				!strings.Contains(err.Error(), string(test.point)) {
				t.Fatalf("real integration fault error = %v", err)
			}
			expectedMerge := integrationMergeFromRuntime(
				t, scenario.journal, scenario.attempt.AttemptID(),
			)
			current := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot,
				"rev-parse",
				scenario.definition.Workspace().FeatureRef(),
			))
			expectedHead := scenario.base
			if test.point == workspace.IntegrationFaultAfterRefCAS {
				expectedHead = expectedMerge
			}
			if current != rawGitObject(expectedHead) {
				t.Fatalf(
					"feature head after %s = %s, want %s",
					test.point, current, expectedHead,
				)
			}
			if objectType := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot,
				"cat-file", "-t", rawGitObject(expectedMerge),
			)); objectType != "commit" {
				t.Fatalf(
					"expected merge object type = %q",
					objectType,
				)
			}
			result, err := workspace.IntegrateMergeUnit(
				context.Background(),
				scenario.journal,
				scenario.definition,
				scenario.repository,
				workspace.DefaultLocalIntegrationGitAdapter(),
				workspace.IntegrateMergeUnitRequest{
					AttemptID: scenario.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T16:30:01Z",
					),
				},
			)
			if err != nil {
				t.Fatalf(
					"recover real integration after %s: %v",
					test.point, err,
				)
			}
			if result.MergeCommit() != expectedMerge {
				t.Fatalf(
					"recovered real merge = %s, want %s",
					result.MergeCommit(), expectedMerge,
				)
			}
			assertSingleIntegrationTransition(
				t, scenario.journal,
				scenario.attempt.AttemptID(),
			)
			if test.point == workspace.IntegrationFaultAfterRefCAS {
				if err := os.RemoveAll(scenario.attempt.Worktree()); err != nil {
					t.Fatalf(
						"remove detached attempt worktree %s: %v",
						scenario.attempt.Worktree(), err,
					)
				}
				if err := scenario.journal.Close(); err != nil {
					t.Fatal(err)
				}
				reinitialized, err :=
					workspace.InitializeWorkspaceV2WithOptions(
						context.Background(),
						scenario.workspace,
						scenario.definition,
						mustTime(
							t, "2026-07-25T16:30:02Z",
						),
						workspace.WorkspaceInitializationOptions{
							WorktreeRoot: scenario.worktrees,
						},
					)
				if err != nil {
					t.Fatalf(
						"re-admit recovered published integration: %v",
						err,
					)
				}
				target, ok := reinitialized.Runtime().LocalTarget()
				if !ok ||
					target.CreatedHead() != expectedMerge {
					t.Fatalf(
						"re-admitted recovered target = %#v exists=%t",
						target, ok,
					)
				}
			}
		})
	}
}

func TestLocalGitIntegrationRejectsExternalMoveToExpectedMergeWithoutMarker(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "external ref-publication permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	expectedMerge := stopRealIntegrationAfterCommit(t, scenario)
	featureRef := scenario.definition.Workspace().FeatureRef()
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-m", "external exact expected merge",
		featureRef, rawGitObject(expectedMerge), rawGitObject(scenario.base),
	)

	before := journalRecordCount(t, scenario.journal)
	_, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:40:00Z"),
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "no exact integration marker") {
		t.Fatalf("externally published expected merge error = %v", err)
	}
	if journalRecordCount(t, scenario.journal) != before {
		t.Fatal("rejected external expected merge appended completion")
	}
	current := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot, "rev-parse", featureRef,
	))
	if current != rawGitObject(expectedMerge) {
		t.Fatalf("external expected merge was reset to %s", current)
	}
	marker := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot,
		"reflog", "show", "--format=%gs", "-n", "1", featureRef, "--",
	))
	if marker != "external exact expected merge" {
		t.Fatalf("external expected-merge reflog marker = %q", marker)
	}
}

func TestLocalGitIntegrationRejectsSameOIDFeatureRefRecreationBeforeIntent(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "same-object ref recreation permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	featureRef := scenario.definition.Workspace().FeatureRef()
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-d", featureRef, rawGitObject(scenario.base),
	)
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "--create-reflog", "-m",
		"external same-OID recreation",
		featureRef, rawGitObject(scenario.base),
	)
	assertRejectedRealIntegration(
		t, scenario, "no exact durable workspace marker",
	)
}

func TestLocalGitIntegrationRejectsSameOIDFeatureRefRecreationAfterIntent(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "same-object ref recreation permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	expectedMerge := stopRealIntegrationAfterCommit(t, scenario)
	featureRef := scenario.definition.Workspace().FeatureRef()
	priorMarker := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot,
		"reflog", "show", "--format=%gs", "-n", "1",
		featureRef, "--",
	))
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-d", featureRef, rawGitObject(scenario.base),
	)
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "--create-reflog", "-m",
		"external same-OID recreation after intent",
		featureRef, rawGitObject(scenario.base),
	)
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:42:00Z"),
		},
	); err == nil ||
		!strings.Contains(
			err.Error(), "no exact durable workspace marker",
		) {
		t.Fatalf("same-OID recreation after intent error = %v", err)
	}
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-d", featureRef, rawGitObject(scenario.base),
	)
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "--create-reflog", "-m", priorMarker,
		featureRef, rawGitObject(scenario.base),
	)
	recovered, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:42:01Z"),
		},
	)
	if err != nil || recovered.MergeCommit() != expectedMerge {
		t.Fatalf(
			"recover restored owned feature marker = %#v error=%v",
			recovered, err,
		)
	}
}

func TestLocalGitIntegrationLocksFeatureOwnershipMarkerThroughPublication(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "feature ownership publication-lock permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	featureRef := scenario.definition.Workspace().FeatureRef()
	fired := false
	fault := func(
		point workspace.IntegrationLifecycleFaultPoint,
	) error {
		if point != workspace.IntegrationFaultAfterRefPrepared ||
			fired {
			return nil
		}
		fired = true
		command := exec.Command(
			"git", "-C", scenario.repositoryRoot,
			"update-ref", "-d", featureRef,
			rawGitObject(scenario.base),
		)
		output, err := command.CombinedOutput()
		if err == nil {
			return fmt.Errorf(
				"same-OID feature-ref deletion succeeded while publication was prepared",
			)
		}
		if !strings.Contains(
			string(output), "cannot lock ref",
		) {
			return fmt.Errorf(
				"prepared feature-ref deletion error = %v: %s",
				err, output,
			)
		}
		return nil
	}
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:42:30Z"),
			Fault:      fault,
		},
	)
	if err != nil || !fired {
		t.Fatalf(
			"prepared feature ownership lock fired=%t result=%#v error=%v",
			fired, result, err,
		)
	}
	current := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot, "rev-parse", featureRef,
	))
	if current != rawGitObject(result.MergeCommit()) {
		t.Fatalf(
			"prepared publication feature head = %s, want %s",
			current, result.MergeCommit(),
		)
	}
	marker := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot,
		"reflog", "show", "--format=%gs", "-n", "1",
		featureRef, "--",
	))
	expectedMarker := "feature workspace integration " +
		result.Intent().Digest().String()
	if marker != expectedMarker {
		t.Fatalf(
			"prepared publication marker = %q, want %q",
			marker, expectedMarker,
		)
	}
}

func TestLocalGitIntegrationRevalidatesMergeMarkerBeforeCompletion(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "integration marker replacement permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	featureRef := scenario.definition.Workspace().FeatureRef()
	var expectedMerge workspace.GitObjectID
	var integrationMarker string
	fired := false
	fault := func(
		point workspace.IntegrationLifecycleFaultPoint,
	) error {
		if point != workspace.IntegrationFaultBeforeCompletion ||
			fired {
			return nil
		}
		fired = true
		expectedMerge = integrationMergeFromRuntime(
			t, scenario.journal, scenario.attempt.AttemptID(),
		)
		integrationMarker = strings.TrimSpace(runTargetGitTest(
			t, scenario.repositoryRoot,
			"reflog", "show", "--format=%gs", "-n", "1",
			featureRef, "--",
		))
		runTargetGitTest(
			t, scenario.repositoryRoot,
			"update-ref", "-d", featureRef,
			rawGitObject(expectedMerge),
		)
		runTargetGitTest(
			t, scenario.repositoryRoot,
			"update-ref", "--create-reflog", "-m",
			"external same-OID merge recreation before completion",
			featureRef, rawGitObject(expectedMerge),
		)
		return nil
	}
	_, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:43:00Z"),
			Fault:      fault,
		},
	)
	if !fired || err == nil ||
		!strings.Contains(err.Error(), "no exact integration marker") {
		t.Fatalf(
			"merge marker replacement fired=%t error=%v",
			fired, err,
		)
	}
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-d", featureRef,
		rawGitObject(expectedMerge),
	)
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "--create-reflog", "-m",
		integrationMarker, featureRef, rawGitObject(expectedMerge),
	)
	recovered, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:43:01Z"),
		},
	)
	if err != nil || recovered.MergeCommit() != expectedMerge {
		t.Fatalf(
			"recover restored integration marker = %#v error=%v",
			recovered, err,
		)
	}
}

func TestWorkspaceReadmissionRejectsRecreatedIntegratedFeatureRef(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "completed workspace readmission permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true,
		workspace.GitObjectID{},
	)
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:45:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	featureRef := result.Intent().FeatureRef()
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-m", "external rewind",
		featureRef, rawGitObject(scenario.acceptedHead),
		rawGitObject(result.MergeCommit()),
	)
	runTargetGitTest(
		t, scenario.repositoryRoot,
		"update-ref", "-m", "external restore",
		featureRef, rawGitObject(result.MergeCommit()),
		rawGitObject(scenario.acceptedHead),
	)
	if err := scenario.journal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		scenario.workspace,
		scenario.definition,
		mustTime(t, "2026-07-25T16:45:01Z"),
		workspace.WorkspaceInitializationOptions{
			WorktreeRoot: scenario.worktrees,
		},
	); err == nil ||
		!strings.Contains(err.Error(), "exact durable workspace marker") {
		t.Fatalf("recreated integrated feature-ref error = %v", err)
	}
}

func TestLocalGitIntegrationRejectsRefChangesAfterIntent(t *testing.T) {
	t.Parallel()

	t.Run("accepted attempt head", func(t *testing.T) {
		requireFullSuite(t, "post-intent ref drift permutation")

		scenario := newRealIntegrationScenario(
			t, workspace.GitHashSHA1, true,
			workspace.GitObjectID{},
		)
		expectedMerge := stopRealIntegrationAfterCommit(
			t, scenario,
		)
		attacker := createIntegrationTestCommit(
			t, scenario.repositoryRoot, scenario.acceptedTree,
			[]workspace.GitObjectID{scenario.acceptedHead},
			"accepted attempt head attacker",
		)
		runTargetGitTest(
			t, scenario.attempt.Worktree(), "reset", "--hard",
			rawGitObject(attacker),
		)
		if _, err := workspace.IntegrateMergeUnit(
			context.Background(),
			scenario.journal,
			scenario.definition,
			scenario.repository,
			workspace.DefaultLocalIntegrationGitAdapter(),
			workspace.IntegrateMergeUnitRequest{
				AttemptID: scenario.attempt.AttemptID(),
				OccurredAt: mustTime(
					t, "2026-07-25T16:55:01Z",
				),
			},
		); err == nil ||
			!strings.Contains(
				err.Error(),
				"exact clean detached attempt worktree",
			) {
			t.Fatalf("moved accepted attempt head error = %v", err)
		}
		featureHead := strings.TrimSpace(runTargetGitTest(
			t, scenario.repositoryRoot, "rev-parse",
			scenario.definition.Workspace().FeatureRef(),
		))
		if featureHead != rawGitObject(scenario.base) {
			t.Fatalf(
				"moved accepted attempt head advanced feature to %s",
				featureHead,
			)
		}
		runTargetGitTest(
			t, scenario.attempt.Worktree(), "reset", "--hard",
			rawGitObject(scenario.acceptedHead),
		)
		recovered, err := workspace.IntegrateMergeUnit(
			context.Background(),
			scenario.journal,
			scenario.definition,
			scenario.repository,
			workspace.DefaultLocalIntegrationGitAdapter(),
			workspace.IntegrateMergeUnitRequest{
				AttemptID: scenario.attempt.AttemptID(),
				OccurredAt: mustTime(
					t, "2026-07-25T16:55:02Z",
				),
			},
		)
		if err != nil || recovered.MergeCommit() != expectedMerge {
			t.Fatalf(
				"recover restored accepted attempt head = %#v error=%v",
				recovered, err,
			)
		}
	})

	t.Run("feature ref", func(t *testing.T) {
		scenario := newRealIntegrationScenario(
			t, workspace.GitHashSHA1, true,
			workspace.GitObjectID{},
		)
		expectedMerge := stopRealIntegrationAfterCommit(
			t, scenario,
		)
		drift := createIntegrationTestCommit(
			t, scenario.repositoryRoot, scenario.acceptedTree,
			[]workspace.GitObjectID{scenario.base},
			"feature ref attacker",
		)
		featureRef := scenario.definition.Workspace().FeatureRef()
		priorMarker := strings.TrimSpace(runTargetGitTest(
			t, scenario.repositoryRoot,
			"reflog", "show", "--format=%gs", "-n", "1",
			featureRef, "--",
		))
		runTargetGitTest(
			t, scenario.repositoryRoot,
			"update-ref", featureRef, rawGitObject(drift),
			rawGitObject(scenario.base),
		)
		if _, err := workspace.IntegrateMergeUnit(
			context.Background(),
			scenario.journal,
			scenario.definition,
			scenario.repository,
			workspace.DefaultLocalIntegrationGitAdapter(),
			workspace.IntegrateMergeUnitRequest{
				AttemptID: scenario.attempt.AttemptID(),
				OccurredAt: mustTime(
					t, "2026-07-25T16:55:01Z",
				),
			},
		); err == nil ||
			!strings.Contains(
				err.Error(),
				string(workspace.IntegrationRefUnrelatedDrift),
			) {
			t.Fatalf("moved feature ref error = %v", err)
		}
		current := strings.TrimSpace(runTargetGitTest(
			t, scenario.repositoryRoot,
			"rev-parse", featureRef,
		))
		if current != rawGitObject(drift) {
			t.Fatalf(
				"moved feature ref was reset to %s",
				current,
			)
		}
		runTargetGitTest(
			t, scenario.repositoryRoot,
			"update-ref", "-m", priorMarker, featureRef,
			rawGitObject(scenario.base), rawGitObject(drift),
		)
		recovered, err := workspace.IntegrateMergeUnit(
			context.Background(),
			scenario.journal,
			scenario.definition,
			scenario.repository,
			workspace.DefaultLocalIntegrationGitAdapter(),
			workspace.IntegrateMergeUnitRequest{
				AttemptID: scenario.attempt.AttemptID(),
				OccurredAt: mustTime(
					t, "2026-07-25T16:55:02Z",
				),
			},
		)
		if err != nil || recovered.MergeCommit() != expectedMerge {
			t.Fatalf(
				"recover restored feature ref = %#v error=%v",
				recovered, err,
			)
		}
	})
}

func TestLocalGitIntegrationRejectsAttemptPathChangesBeforeCAS(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name   string
		point  workspace.IntegrationLifecycleFaultPoint
		mutate func(*testing.T, *realIntegrationScenario) func()
	}{
		{
			name: "symlink substitution",
			mutate: func(
				t *testing.T,
				scenario *realIntegrationScenario,
			) func() {
				worktree := scenario.attempt.Worktree()
				displaced := worktree + "-displaced"
				if err := os.Rename(worktree, displaced); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(displaced, worktree); err != nil {
					t.Fatal(err)
				}
				return func() {
					if err := os.Remove(worktree); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(
						displaced, worktree,
					); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "attempt directory relocation",
			mutate: func(
				t *testing.T,
				scenario *realIntegrationScenario,
			) func() {
				worktree := scenario.attempt.Worktree()
				relocated := worktree + "-relocated"
				if err := os.Rename(worktree, relocated); err != nil {
					t.Fatal(err)
				}
				return func() {
					if err := os.Rename(relocated, worktree); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFullSuiteCase(
				t,
				test.name == "symlink substitution",
				"attempt worktree replacement permutation",
			)

			scenario := newRealIntegrationScenario(
				t, workspace.GitHashSHA1, true,
				workspace.GitObjectID{},
			)
			var restore func()
			fired := false
			fault := func(
				point workspace.IntegrationLifecycleFaultPoint,
			) error {
				mutationPoint := test.point
				if mutationPoint == "" {
					mutationPoint =
						workspace.IntegrationFaultBeforeRefCAS
				}
				if point == mutationPoint &&
					!fired {
					fired = true
					restore = test.mutate(t, scenario)
				}
				return nil
			}
			t.Cleanup(func() {
				if restore != nil {
					restore()
				}
			})
			_, err := workspace.IntegrateMergeUnit(
				context.Background(),
				scenario.journal,
				scenario.definition,
				scenario.repository,
				workspace.DefaultLocalIntegrationGitAdapter(),
				workspace.IntegrateMergeUnitRequest{
					AttemptID: scenario.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T16:50:00Z",
					),
					Fault: fault,
				},
			)
			if !fired || err == nil {
				t.Fatalf(
					"attempt path mutation fired=%t error=%v",
					fired, err,
				)
			}
			featureHead := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot, "rev-parse",
				scenario.definition.Workspace().FeatureRef(),
			))
			expectedFeatureHead := scenario.base
			if test.point ==
				workspace.IntegrationFaultBeforeCompletion {
				expectedFeatureHead = integrationMergeFromRuntime(
					t, scenario.journal,
					scenario.attempt.AttemptID(),
				)
			}
			if featureHead != rawGitObject(expectedFeatureHead) {
				t.Fatalf(
					"attempt path mutation left feature at %s, want %s",
					featureHead, expectedFeatureHead,
				)
			}
			restore()
			restore = nil
			recovered, err := workspace.IntegrateMergeUnit(
				context.Background(),
				scenario.journal,
				scenario.definition,
				scenario.repository,
				workspace.DefaultLocalIntegrationGitAdapter(),
				workspace.IntegrateMergeUnitRequest{
					AttemptID: scenario.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T16:50:01Z",
					),
				},
			)
			if err != nil ||
				recovered.MergeCommit().IsZero() {
				t.Fatalf(
					"recover restored exact attempt worktree = %#v error=%v",
					recovered, err,
				)
			}
		})
	}
}

func TestLocalGitIntegrationRejectsInvalidAcceptedAncestryTreeAndBase(
	t *testing.T,
) {
	t.Parallel()

	t.Run("non-descendant accepted head", func(t *testing.T) {
		scenario := newRealIntegrationScenario(
			t, workspace.GitHashSHA1, false,
			workspace.GitObjectID{},
		)
		assertRejectedRealIntegration(
			t, scenario, "is not an ancestor",
		)
	})

	t.Run("accepted tree mismatch", func(t *testing.T) {
		requireFullSuite(t, "accepted integration identity permutation")

		reportedTree := mustGitObject(t, 'd')
		scenario := newRealIntegrationScenario(
			t, workspace.GitHashSHA1, true, reportedTree,
		)
		assertRejectedRealIntegration(
			t, scenario,
			"exact clean detached attempt worktree",
		)
	})

	t.Run("feature head moved from attempt base", func(t *testing.T) {
		requireFullSuite(t, "accepted integration identity permutation")

		scenario := newRealIntegrationScenario(
			t, workspace.GitHashSHA1, true,
			workspace.GitObjectID{},
		)
		drift := createIntegrationTestCommit(
			t, scenario.repositoryRoot, scenario.acceptedTree,
			[]workspace.GitObjectID{scenario.base},
			"feature drift before intent",
		)
		featureRef := scenario.definition.Workspace().FeatureRef()
		runTargetGitTest(
			t, scenario.repositoryRoot,
			"update-ref", featureRef, rawGitObject(drift),
			rawGitObject(scenario.base),
		)
		assertRejectedRealIntegration(
			t, scenario, string(workspace.IntegrationRefDescendantDrift),
		)
		current := strings.TrimSpace(runTargetGitTest(
			t, scenario.repositoryRoot, "rev-parse", featureRef,
		))
		if current != rawGitObject(drift) {
			t.Fatalf("rejected base drift was reset to %s", current)
		}
	})
}

func TestCompletedIntegrationReverifiesAcceptedHeadAncestry(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "completed integration ancestry permutation")

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true,
		workspace.GitObjectID{},
	)
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID: scenario.attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T16:55:00Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := scenario.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, exists := runtime.LocalTarget()
	if !exists {
		t.Fatal("completed integration has no local target")
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
merge_base=false
is_ancestor=false
for argument in "$@"; do
  if [ "$argument" = "merge-base" ]; then merge_base=true; fi
  if [ "$argument" = "--is-ancestor" ]; then is_ancestor=true; fi
done
if [ "$merge_base" = "true" ] && [ "$is_ancestor" = "true" ]; then
  exit 1
fi
exec ` + shellSingleQuote(realGit) + ` "$@"
`
	if err := os.WriteFile(
		wrapper, []byte(script), 0o700,
	); err != nil {
		t.Fatal(err)
	}
	adapter, err := workspace.NewLocalIntegrationGitAdapter(
		wrapper, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.VerifyCompletedIntegration(
		context.Background(),
		target.Binding(),
		[]workspace.MergeUnitIntegrationIntent{
			result.Intent(),
		},
	); err == nil ||
		!strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf(
			"completed accepted-head ancestry error = %v", err,
		)
	}
}

func TestCompletedIntegrationRejectsMissingAcceptedTreeClosure(
	t *testing.T,
) {
	t.Parallel()

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true,
		workspace.GitObjectID{},
	)
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID: scenario.attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T16:56:00Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := scenario.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, exists := runtime.LocalTarget()
	if !exists {
		t.Fatal("completed integration has no local target")
	}

	listing := runTargetGitTest(
		t, scenario.repositoryRoot,
		"ls-tree", "-r", rawGitObject(scenario.acceptedTree),
	)
	var blob string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == "blob" {
			blob = fields[2]
			break
		}
	}
	if blob == "" {
		t.Fatal("accepted tree has no reachable blob to remove")
	}
	commonDirectory := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot,
		"rev-parse", "--git-common-dir",
	))
	if !filepath.IsAbs(commonDirectory) {
		commonDirectory = filepath.Join(
			scenario.repositoryRoot, commonDirectory,
		)
	}
	if err := os.Remove(filepath.Join(
		commonDirectory, "objects", blob[:2], blob[2:],
	)); err != nil {
		t.Fatalf("remove accepted-tree blob %s: %v", blob, err)
	}

	if err := workspace.DefaultLocalIntegrationGitAdapter().
		VerifyCompletedIntegration(
			context.Background(),
			target.Binding(),
			[]workspace.MergeUnitIntegrationIntent{
				result.Intent(),
			},
		); err == nil ||
		!strings.Contains(
			err.Error(), "integration tree object closure",
		) {
		t.Fatalf(
			"missing accepted-tree closure error = %v", err,
		)
	}
}

func stopRealIntegrationAfterCommit(
	t *testing.T,
	scenario *realIntegrationScenario,
) workspace.GitObjectID {
	t.Helper()
	_, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T16:55:00Z"),
			Fault: failIntegrationOnce(
				workspace.IntegrationFaultAfterCommitCreated,
			),
		},
	)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			string(workspace.IntegrationFaultAfterCommitCreated),
		) {
		t.Fatalf("stop real integration after commit error = %v", err)
	}
	return integrationMergeFromRuntime(
		t, scenario.journal, scenario.attempt.AttemptID(),
	)
}

func newRealIntegrationScenario(
	t *testing.T,
	algorithm workspace.GitHashAlgorithm,
	descendsFromBase bool,
	reportedTree workspace.GitObjectID,
) *realIntegrationScenario {
	t.Helper()
	fixture := newDefinitionFixtureForHash(t, algorithm)
	core := newAttemptHarnessFromFixture(t, fixture, "unit-one")
	attempt := core.reserve(t, "2026-07-25T14:30:00Z")
	attempt, err := workspace.MaterializeAttempt(
		context.Background(), core.journal, core.definition,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.MaterializeAttemptRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T14:30:01Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := core.definition.Workspace().RepositoryRoot()
	treeText := strings.TrimSpace(runTargetGitTest(
		t, repositoryRoot, "rev-parse",
		rawGitObject(core.base)+"^{tree}",
	))
	tree, err := workspace.ParseGitObjectID(
		string(algorithm) + ":" + treeText,
	)
	if err != nil {
		t.Fatal(err)
	}
	parents := []workspace.GitObjectID(nil)
	if descendsFromBase {
		parents = append(parents, core.base)
	}
	accepted := createIntegrationTestCommit(
		t, attempt.Worktree(), tree, parents, "accepted attempt",
	)
	runTargetGitTest(
		t, attempt.Worktree(), "reset", "--hard", rawGitObject(accepted),
	)
	if reportedTree.IsZero() {
		reportedTree = tree
	}
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(
		accepted, reportedTree, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{snapshot: repositorySnapshot}
	if _, err := workspace.AdoptAttemptHead(
		context.Background(),
		core.journal,
		core.definition,
		repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T14:30:02Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, exists := runtime.Attempt(attempt.AttemptID())
	if !exists || attempt.VerifiedHead() != accepted {
		t.Fatalf(
			"real accepted attempt = %#v exists=%t",
			attempt, exists,
		)
	}
	return &realIntegrationScenario{
		attemptHarness: core,
		attempt:        attempt,
		repository:     repository,
		repositoryRoot: repositoryRoot,
		acceptedHead:   accepted,
		acceptedTree:   tree,
	}
}

func createIntegrationTestCommit(
	t *testing.T,
	repositoryRoot string,
	tree workspace.GitObjectID,
	parents []workspace.GitObjectID,
	message string,
) workspace.GitObjectID {
	t.Helper()
	arguments := []string{
		"-c", "user.name=Integration Test",
		"-c", "user.email=integration@example.invalid",
		"commit-tree", rawGitObject(tree),
	}
	for _, parent := range parents {
		arguments = append(
			arguments, "-p", rawGitObject(parent),
		)
	}
	arguments = append(arguments, "-m", message)
	raw := strings.TrimSpace(runTargetGitTest(
		t, repositoryRoot, arguments...,
	))
	algorithm := strings.TrimSpace(runTargetGitTest(
		t, repositoryRoot, "rev-parse", "--show-object-format",
	))
	object, err := workspace.ParseGitObjectID(algorithm + ":" + raw)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func assertRejectedRealIntegration(
	t *testing.T,
	scenario *realIntegrationScenario,
	want string,
) {
	t.Helper()
	before := journalRecordCount(t, scenario.journal)
	_, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T14:45:00Z"),
		},
	)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("rejected real integration error = %v, want %q", err, want)
	}
	if journalRecordCount(t, scenario.journal) != before {
		t.Fatal("rejected real integration appended journal state")
	}
}
