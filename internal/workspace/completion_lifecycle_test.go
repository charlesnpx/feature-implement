package workspace_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type completedWorkspaceHarness struct {
	core       attemptHarness
	git        *integrationGitStub
	repository *reviewRepositoryStub
	first      workspace.MergeUnitIntegrationResult
	second     workspace.MergeUnitIntegrationResult
}

func TestWorkspaceCompletionAppendsOnceAndBindsCanonicalLocalReport(
	t *testing.T,
) {
	t.Parallel()

	harness := newCompletedWorkspaceHarness(t)
	snapshot, err := harness.core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspace.RebuildWorkspaceView(
		snapshot, harness.core.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	if before.Completion.Complete ||
		len(before.Completion.Blockers) != 1 ||
		before.Completion.Blockers[0] !=
			"workspace_completion_not_recorded" ||
		before.Gates.Completion.Status != workspace.GatePending ||
		!containsCompletionBlocker(
			before.Gates.CompletionBlockers,
			"workspace_completion_not_recorded",
		) {
		t.Fatalf(
			"pre-completion report = completion %#v gate %#v",
			before.Completion, before.Gates.Completion,
		)
	}
	beforeDigest, err := workspace.ParseDigest(before.ReportDigest)
	if err != nil {
		t.Fatal(err)
	}
	beforeRecords := len(snapshot.Records())

	result, err := workspace.CompleteWorkspace(
		context.Background(),
		harness.core.journal,
		harness.core.definition,
		harness.git,
		workspace.CompleteWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-07-25T20:00:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Record().Sequence() == 0 ||
		result.Record().EventType() !=
			workspace.JournalEventWorkspaceCompleted ||
		result.Completion().ReportDigest() != beforeDigest ||
		result.Completion().FeatureHead() !=
			harness.second.MergeCommit() {
		t.Fatalf("completion result = %#v", result)
	}

	completedSnapshot, err := harness.core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(completedSnapshot.Records()) != beforeRecords+1 {
		t.Fatalf(
			"completion records = %d, want %d",
			len(completedSnapshot.Records()), beforeRecords+1,
		)
	}
	report, err := workspace.RebuildWorkspaceView(
		completedSnapshot, harness.core.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Completion.Complete ||
		len(report.Completion.Blockers) != 0 ||
		report.Completion.ReportDigest != before.ReportDigest ||
		report.Gates.Completion.Status != workspace.GatePassed ||
		report.Gates.Completion.Reason != "workspace_completed" ||
		len(report.Gates.CompletionBlockers) != 0 {
		t.Fatalf(
			"completed report = completion %#v gate %#v",
			report.Completion, report.Gates.Completion,
		)
	}

	retried, err := workspace.CompleteWorkspace(
		context.Background(),
		harness.core.journal,
		harness.core.definition,
		harness.git,
		workspace.CompleteWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-07-25T20:00:01Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Record().Sequence() != 0 ||
		retried.Completion().EventDigest() !=
			result.Completion().EventDigest() ||
		journalRecordCount(t, harness.core.journal) !=
			beforeRecords+1 {
		t.Fatalf(
			"idempotent completion = %#v records=%d",
			retried,
			journalRecordCount(t, harness.core.journal),
		)
	}
}

func TestWorkspaceCompletionExposesEveryCurrentLocalBlocker(
	t *testing.T,
) {
	t.Parallel()

	harness := newNoReviewIntegrationHarness(t, false)
	_, err := workspace.CompleteWorkspace(
		context.Background(),
		harness.journal,
		harness.definition,
		harness.git,
		workspace.CompleteWorkspaceRequest{
			OccurredAt: mustTime(t, "2026-07-25T20:10:00Z"),
		},
	)
	var blocked workspace.WorkspaceCompletionBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("completion blocker error = %v", err)
	}
	for _, expected := range []string{
		"attempt:" + harness.attempt.AttemptID().String() + ":active",
		"attempt:" + harness.attempt.AttemptID().String() + ":lease_held",
		"merge_unit:alpha-plan/unit-one:not_integrated",
		"merge_unit:alpha-plan/unit-two:not_integrated",
	} {
		if !containsCompletionBlocker(blocked.Blockers(), expected) {
			t.Fatalf(
				"completion blockers %v do not contain %q",
				blocked.Blockers(), expected,
			)
		}
	}
	snapshot, readErr := harness.journal.ReadSnapshot()
	if readErr != nil {
		t.Fatal(readErr)
	}
	report, reportErr := workspace.RebuildWorkspaceView(
		snapshot, harness.definition,
	)
	if reportErr != nil {
		t.Fatal(reportErr)
	}
	if report.Completion.Complete ||
		report.Gates.Completion.Status != workspace.GatePending {
		t.Fatalf(
			"blocked completion report = %#v gate=%#v",
			report.Completion, report.Gates.Completion,
		)
	}
	for _, expected := range blocked.Blockers() {
		if !containsCompletionBlocker(
			report.Completion.Blockers, expected,
		) {
			t.Fatalf(
				"reported blockers %v do not contain %q",
				report.Completion.Blockers, expected,
			)
		}
	}
}

func TestWorkspaceCompletionSupportsConfiguredReviewAndAdoptHead(
	t *testing.T,
) {
	t.Parallel()

	harness := newReviewHarness(t)
	start, err := workspace.StartAttemptReviewRound(
		context.Background(),
		harness.journal,
		harness.definition,
		harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T20:20:00Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	security := reviewSubmission(
		t, start.Request(), workspace.MustID("security-completion"),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	harness.record(
		t, start.Request(), security,
		"2026-07-25T20:20:01Z",
	)
	state := mustReviewState(
		t, harness.journal, harness.definition,
		harness.attempt.AttemptID(),
	)
	correctnessRequest, ok, err := state.NextRequest()
	if err != nil || !ok {
		t.Fatalf(
			"configured-review next request = %#v ok=%t err=%v",
			correctnessRequest, ok, err,
		)
	}
	correctness := reviewSubmission(
		t, correctnessRequest,
		workspace.MustID("correctness-completion"),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	harness.record(
		t, correctnessRequest, correctness,
		"2026-07-25T20:20:02Z",
	)
	if _, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(),
		harness.journal,
		harness.definition,
		harness.repository,
		harness.attempt.AttemptID(),
	); err != nil {
		t.Fatal(err)
	}

	git := &integrationGitStub{featureHead: harness.base}
	first, err := workspace.IntegrateMergeUnit(
		context.Background(),
		harness.journal,
		harness.definition,
		harness.repository,
		git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: harness.attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T20:20:03Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Intent().AcceptanceMode() !=
		workspace.IntegrationAcceptanceReviewReady ||
		first.Intent().ReviewReadinessDigest().IsZero() {
		t.Fatalf(
			"configured-review integration = %#v",
			first.Intent(),
		)
	}

	secondCore := harness.attemptHarness
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	secondCore.goal, err = workspace.NewGoalBinding(
		workspace.MustID("completion-second-goal"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	second := secondCore.reserve(
		t, "2026-07-25T20:20:04Z",
	)
	secondCore.git.inspection, err =
		workspace.NewAttemptGitInspection(
			false, workspace.GitObjectID{},
			false, false, "",
			workspace.GitObjectID{}, false,
		)
	if err != nil {
		t.Fatal(err)
	}
	second = secondCore.materialize(
		t, second.AttemptID(),
		"2026-07-25T20:20:05Z",
	)
	secondRepository := adoptedIntegrationRepository(
		t, secondCore, second,
		mustGitObject(t, 'e'),
		mustGitObject(t, 'f'),
		"2026-07-25T20:20:06Z",
	)
	git.expectedCommit = false
	secondResult, err := workspace.IntegrateMergeUnit(
		context.Background(),
		secondCore.journal,
		secondCore.definition,
		secondRepository,
		git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: second.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T20:20:07Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Intent().AcceptanceMode() !=
		workspace.IntegrationAcceptanceAdoptedHead ||
		secondResult.Intent().AdoptedHeadEventDigest().IsZero() {
		t.Fatalf(
			"adopt-head integration = %#v",
			secondResult.Intent(),
		)
	}
	if _, err := workspace.CompleteWorkspace(
		context.Background(),
		secondCore.journal,
		secondCore.definition,
		git,
		workspace.CompleteWorkspaceRequest{
			OccurredAt: mustTime(
				t, "2026-07-25T20:20:08Z",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCompletionRetryRecoversBeforeAndAfterAppendFaults(
	t *testing.T,
) {
	t.Parallel()

	for _, point := range []workspace.CompletionLifecycleFaultPoint{
		workspace.CompletionFaultBeforeAppend,
		workspace.CompletionFaultAfterAppend,
	} {
		t.Run(string(point), func(t *testing.T) {
			harness := newCompletedWorkspaceHarness(t)
			before := journalRecordCount(
				t, harness.core.journal,
			)
			crash := errors.New("completion crash")
			fired := false
			_, err := workspace.CompleteWorkspace(
				context.Background(),
				harness.core.journal,
				harness.core.definition,
				harness.git,
				workspace.CompleteWorkspaceRequest{
					OccurredAt: mustTime(
						t, "2026-07-25T20:30:00Z",
					),
					Fault: func(
						observed workspace.CompletionLifecycleFaultPoint,
					) error {
						if observed == point && !fired {
							fired = true
							return crash
						}
						return nil
					},
				},
			)
			if !fired || !errors.Is(err, crash) {
				t.Fatalf(
					"completion fault fired=%t error=%v",
					fired, err,
				)
			}
			afterFault := journalRecordCount(
				t, harness.core.journal,
			)
			wantAfterFault := before
			if point == workspace.CompletionFaultAfterAppend {
				wantAfterFault++
			}
			if afterFault != wantAfterFault {
				t.Fatalf(
					"records after %s = %d, want %d",
					point, afterFault, wantAfterFault,
				)
			}
			retried, err := workspace.CompleteWorkspace(
				context.Background(),
				harness.core.journal,
				harness.core.definition,
				harness.git,
				workspace.CompleteWorkspaceRequest{
					OccurredAt: mustTime(
						t, "2026-07-25T20:30:01Z",
					),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if retried.Completion().EventDigest().IsZero() ||
				journalRecordCount(
					t, harness.core.journal,
				) != before+1 {
				t.Fatalf(
					"completion retry = %#v records=%d",
					retried,
					journalRecordCount(
						t, harness.core.journal,
					),
				)
			}
		})
	}
}

func TestLocalRecoveryResumesPendingMaterializationAndIntegration(
	t *testing.T,
) {
	t.Parallel()

	t.Run("materialization", func(t *testing.T) {
		core := newAttemptHarness(t, "unit-one")
		attempt := core.reserve(
			t, "2026-07-25T20:40:00Z",
		)
		crash := errors.New("materialization crash")
		if _, err := workspace.MaterializeAttempt(
			context.Background(),
			core.journal,
			core.definition,
			core.git,
			workspace.MaterializeAttemptRequest{
				AttemptID: attempt.AttemptID(),
				OccurredAt: mustTime(
					t, "2026-07-25T20:40:01Z",
				),
				Fault: func(
					point workspace.AttemptLifecycleFaultPoint,
				) error {
					if point ==
						workspace.AttemptFaultAfterMaterializationIntent {
						return crash
					}
					return nil
				},
			},
		); !errors.Is(err, crash) {
			t.Fatalf("materialization fault = %v", err)
		}
		result, err := workspace.RecoverWorkspaceLocalEffects(
			context.Background(),
			core.journal,
			core.definition,
			workspace.DefaultLocalTargetGitAdapter(),
			core.git,
			&reviewRepositoryStub{},
			&integrationGitStub{featureHead: core.base},
			workspace.RecoverWorkspaceLocalEffectsRequest{
				OccurredAt: mustTime(
					t, "2026-07-25T20:40:02Z",
				),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !containsRecoveryAction(
			result.Actions(),
			workspace.LocalRecoveryAttemptMaterialized,
		) {
			t.Fatalf("materialization recovery = %v", result.Actions())
		}
		recovered := mustRuntimeAttempt(
			t, core.journal, attempt.AttemptID(),
		)
		if recovered.Phase() != workspace.AttemptActive {
			t.Fatalf(
				"recovered materialization = %#v", recovered,
			)
		}
	})

	t.Run("integration", func(t *testing.T) {
		harness := newNoReviewIntegrationHarness(t, false)
		if _, err := workspace.IntegrateMergeUnit(
			context.Background(),
			harness.journal,
			harness.definition,
			harness.repository,
			harness.git,
			workspace.IntegrateMergeUnitRequest{
				AttemptID: harness.attempt.AttemptID(),
				OccurredAt: mustTime(
					t, "2026-07-25T20:41:00Z",
				),
				Fault: failIntegrationOnce(
					workspace.IntegrationFaultAfterRefCAS,
				),
			},
		); err == nil ||
			!strings.Contains(
				err.Error(), "after_ref_cas",
			) {
			t.Fatalf("integration fault = %v", err)
		}
		result, err := workspace.RecoverWorkspaceLocalEffects(
			context.Background(),
			harness.journal,
			harness.definition,
			workspace.DefaultLocalTargetGitAdapter(),
			harness.attemptHarness.git,
			harness.repository,
			harness.git,
			workspace.RecoverWorkspaceLocalEffectsRequest{
				OccurredAt: mustTime(
					t, "2026-07-25T20:41:01Z",
				),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !containsRecoveryAction(
			result.Actions(),
			workspace.LocalRecoveryIntegrationCompleted,
		) {
			t.Fatalf("integration recovery = %v", result.Actions())
		}
		recovered := mustRuntimeAttempt(
			t, harness.journal,
			harness.attempt.AttemptID(),
		)
		integration, exists := recovered.Integration()
		if recovered.Phase() != workspace.AttemptCompleted ||
			!exists || !integration.Integrated() {
			t.Fatalf(
				"recovered integration = %#v exists=%t",
				recovered, exists,
			)
		}
	})
}

func TestLocalRecoveryRepairsIncompleteCompletionTailWithoutDuplication(
	t *testing.T,
) {
	t.Parallel()

	harness := newCompletedWorkspaceHarness(t)
	before := journalRecordCount(t, harness.core.journal)
	if err := harness.core.journal.Close(); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("partial completion append")
	fired := false
	faulty, err := workspace.OpenWorkspaceJournalWithOptions(
		harness.core.workspace,
		workspace.JournalReadWrite,
		workspace.JournalOptions{
			FaultInjector: func(
				point workspace.JournalFaultPoint,
			) error {
				if point ==
					workspace.JournalFaultAfterAppendPrefix &&
					!fired {
					fired = true
					return crash
				}
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	harness.core.journal = faulty
	if _, err := workspace.CompleteWorkspace(
		context.Background(),
		faulty,
		harness.core.definition,
		harness.git,
		workspace.CompleteWorkspaceRequest{
			OccurredAt: mustTime(
				t, "2026-07-25T20:50:00Z",
			),
		},
	); !fired || err == nil {
		t.Fatalf(
			"partial completion append fired=%t error=%v",
			fired, err,
		)
	}
	if err := faulty.Close(); err != nil {
		t.Fatal(err)
	}
	recoveredJournal, err := workspace.OpenWorkspaceJournal(
		harness.core.workspace,
		workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recoveredJournal.Close() })
	harness.core.journal = recoveredJournal
	recovered, err := workspace.RecoverWorkspaceLocalEffects(
		context.Background(),
		recoveredJournal,
		harness.core.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		harness.core.git,
		harness.repository,
		harness.git,
		workspace.RecoverWorkspaceLocalEffectsRequest{
			OccurredAt: mustTime(
				t, "2026-07-25T20:50:01Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []workspace.LocalRecoveryAction{
		workspace.LocalRecoveryJournalTail,
		workspace.LocalRecoveryWorkspaceCompleted,
	} {
		if !containsRecoveryAction(
			recovered.Actions(), action,
		) {
			t.Fatalf(
				"completion recovery actions %v omit %s",
				recovered.Actions(), action,
			)
		}
	}
	snapshot, err := recoveredJournal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := runtime.Completion(); !exists ||
		len(snapshot.Records()) != before+2 {
		t.Fatalf(
			"recovered completion exists=%t records=%d want=%d",
			exists, len(snapshot.Records()), before+2,
		)
	}
}

func TestIncompleteFeatureRefCreationIsReportedAndRecovered(
	t *testing.T,
) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	worktreeRoot := t.TempDir()
	crash := errors.New("feature-ref intent crash")
	_, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		workspaceDir,
		definition,
		mustTime(t, "2026-07-25T21:00:00Z"),
		workspace.WorkspaceInitializationOptions{
			WorktreeRoot: worktreeRoot,
			TargetFault: func(
				point workspace.LocalTargetInitializationFaultPoint,
			) error {
				if point ==
					workspace.LocalTargetFaultAfterIntentSynced {
					return crash
				}
				return nil
			},
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("feature-ref initialization fault = %v", err)
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(
		workspaceDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	report, err := workspace.RebuildWorkspaceView(
		snapshot, definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Target.Ready ||
		!containsCompletionBlocker(
			report.Completion.Blockers,
			"local_effect:feature_ref_creation_pending",
		) {
		t.Fatalf(
			"pending feature-ref report target=%#v completion=%#v",
			report.Target, report.Completion,
		)
	}

	journal, err := workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	recovered, err := workspace.RecoverWorkspaceLocalEffects(
		context.Background(),
		journal,
		definition,
		workspace.DefaultLocalTargetGitAdapter(),
		&fakeAttemptGit{},
		&reviewRepositoryStub{},
		&integrationGitStub{featureHead: fixture.base},
		workspace.RecoverWorkspaceLocalEffectsRequest{
			OccurredAt: mustTime(
				t, "2026-07-25T21:00:01Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsRecoveryAction(
		recovered.Actions(),
		workspace.LocalRecoveryFeatureRef,
	) {
		t.Fatalf(
			"feature-ref recovery actions = %v",
			recovered.Actions(),
		)
	}
	recoveredSnapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	recoveredReport, err := workspace.RebuildWorkspaceView(
		recoveredSnapshot, definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !recoveredReport.Target.Ready {
		t.Fatalf(
			"recovered target = %#v",
			recoveredReport.Target,
		)
	}
}

func newCompletedWorkspaceHarness(
	t *testing.T,
) completedWorkspaceHarness {
	t.Helper()
	harness := newNoReviewIntegrationHarness(t, false)
	first, err := workspace.IntegrateMergeUnit(
		context.Background(),
		harness.journal,
		harness.definition,
		harness.repository,
		harness.git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: harness.attempt.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T19:00:00Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	secondCore := harness.attemptHarness
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	secondCore.goal, err = workspace.NewGoalBinding(
		workspace.MustID("second-completion-goal"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	second := secondCore.reserve(
		t, "2026-07-25T19:00:01Z",
	)
	secondCore.git.inspection, err =
		workspace.NewAttemptGitInspection(
			false, workspace.GitObjectID{},
			false, false, "",
			workspace.GitObjectID{}, false,
		)
	if err != nil {
		t.Fatal(err)
	}
	second = secondCore.materialize(
		t, second.AttemptID(),
		"2026-07-25T19:00:02Z",
	)
	secondRepository := adoptedIntegrationRepository(
		t, secondCore, second,
		mustGitObject(t, 'e'),
		mustGitObject(t, 'f'),
		"2026-07-25T19:00:03Z",
	)
	harness.git.expectedCommit = false
	secondResult, err := workspace.IntegrateMergeUnit(
		context.Background(),
		secondCore.journal,
		secondCore.definition,
		secondRepository,
		harness.git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: second.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T19:00:04Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return completedWorkspaceHarness{
		core:       secondCore,
		git:        harness.git,
		repository: secondRepository,
		first:      first,
		second:     secondResult,
	}
}

func containsCompletionBlocker(
	blockers []string,
	expected string,
) bool {
	for _, blocker := range blockers {
		if blocker == expected {
			return true
		}
	}
	return false
}

func containsRecoveryAction(
	actions []workspace.LocalRecoveryAction,
	expected workspace.LocalRecoveryAction,
) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}
	return false
}
