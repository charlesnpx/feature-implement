package workspace_test

import (
	"bytes"
	"context"
	"os"
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
		ctx, request.Worktree(), request.Branch(), request.Head(),
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
	for _, algorithm := range []workspace.GitHashAlgorithm{
		workspace.GitHashSHA1,
		workspace.GitHashSHA256,
	} {
		t.Run(string(algorithm), func(t *testing.T) {
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
			attemptHead := strings.TrimSpace(runTargetGitTest(
				t, scenario.repositoryRoot,
				"rev-parse",
				"refs/heads/"+scenario.attempt.Branch(),
			))
			if attemptHead != rawGitObject(scenario.acceptedHead) {
				t.Fatalf(
					"attempt ref changed to %s, want %s",
					attemptHead, scenario.acceptedHead,
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

func TestLocalGitIntegrationUsesExactRegisteredAttemptWorktree(t *testing.T) {
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
			"registered-worktree integration intent = %#v",
			result.Intent(),
		)
	}
	if current := parseGitHead(t, attempt.Worktree()); current != acceptedHead {
		t.Fatalf(
			"attempt worktree moved to %s, want %s",
			current, acceptedHead,
		)
	}
	runTargetGitTest(
		t, definition.Workspace().RepositoryRoot(),
		"worktree", "remove", "--force", attempt.Worktree(),
	)
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
}

func TestLocalGitIntegrationRejectsAncestorDescendantAndUnrelatedDrift(
	t *testing.T,
) {
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

func TestLocalGitIntegrationRecoversCreatedObjectAndPublishedRef(
	t *testing.T,
) {
	tests := []struct {
		name  string
		point workspace.IntegrationLifecycleFaultPoint
	}{
		{
			name:  "created object at old head",
			point: workspace.IntegrationFaultAfterCommitCreated,
		},
		{
			name:  "published expected merge",
			point: workspace.IntegrationFaultAfterRefCAS,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
		})
	}
}

func TestLocalGitIntegrationRecoversExternalMoveToExpectedMerge(
	t *testing.T,
) {
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

	result, err := workspace.IntegrateMergeUnit(
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
	if err != nil {
		t.Fatalf("recover externally published expected merge: %v", err)
	}
	if result.MergeCommit() != expectedMerge {
		t.Fatalf(
			"externally published merge = %s, want %s",
			result.MergeCommit(), expectedMerge,
		)
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
	assertSingleIntegrationTransition(
		t, scenario.journal, scenario.attempt.AttemptID(),
	)
}

func TestWorkspaceReadmissionRejectsRecreatedIntegratedFeatureRef(
	t *testing.T,
) {
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
	t.Run("attempt ref", func(t *testing.T) {
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
			"attempt ref attacker",
		)
		attemptRef := "refs/heads/" + scenario.attempt.Branch()
		runTargetGitTest(
			t, scenario.repositoryRoot,
			"update-ref", attemptRef, rawGitObject(attacker),
			rawGitObject(scenario.acceptedHead),
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
			!strings.Contains(err.Error(), "accepted attempt ref") {
			t.Fatalf("moved attempt ref error = %v", err)
		}
		featureHead := strings.TrimSpace(runTargetGitTest(
			t, scenario.repositoryRoot, "rev-parse",
			scenario.definition.Workspace().FeatureRef(),
		))
		if featureHead != rawGitObject(scenario.base) {
			t.Fatalf(
				"moved attempt ref advanced feature to %s",
				featureHead,
			)
		}
		runTargetGitTest(
			t, scenario.repositoryRoot,
			"update-ref", attemptRef,
			rawGitObject(scenario.acceptedHead),
			rawGitObject(attacker),
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
				"recover restored attempt ref = %#v error=%v",
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
			"update-ref", featureRef,
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

func TestLocalGitIntegrationRejectsInvalidAcceptedAncestryTreeAndBase(
	t *testing.T,
) {
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
		reportedTree := mustGitObject(t, 'd')
		scenario := newRealIntegrationScenario(
			t, workspace.GitHashSHA1, true, reportedTree,
		)
		assertRejectedRealIntegration(
			t, scenario, "has tree",
		)
	})

	t.Run("feature head moved from attempt base", func(t *testing.T) {
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
	attempt = core.materialize(
		t, attempt.AttemptID(), "2026-07-25T14:30:01Z",
	)
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
		t, repositoryRoot, tree, parents, "accepted attempt",
	)
	runTargetGitTest(
		t, repositoryRoot, "update-ref",
		"refs/heads/"+attempt.Branch(), rawGitObject(accepted),
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
