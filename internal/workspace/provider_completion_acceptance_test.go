package workspace_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type providerOpenScenario struct {
	harness   attemptHarness
	attempt   workspace.RuntimeAttemptProjection
	frontier  workspace.AuthorizationFrontier
	clock     *authorizationTestClock
	evaluator *workspace.AuthorizationEvaluator
	adapter   *providerLifecycleAdapter
	broker    *workspace.ProviderBroker
	open      workspace.ProviderIntent
	pull      workspace.PullRequestIdentity
	base      workspace.GitObjectID
	head      workspace.GitObjectID
	tree      workspace.GitObjectID
	hour      string
}

func newProviderOpenScenario(
	t *testing.T,
	hour string,
	algorithm workspace.GitHashAlgorithm,
	pullRequestNumber uint64,
) providerOpenScenario {
	t.Helper()
	harness := newAttemptHarness(t, "unit-one")
	return newProviderOpenScenarioWithHarness(t, harness, hour, algorithm, pullRequestNumber)
}

func newProviderOpenScenarioWithHarness(
	t *testing.T,
	harness attemptHarness,
	hour string,
	algorithm workspace.GitHashAlgorithm,
	pullRequestNumber uint64,
) providerOpenScenario {
	t.Helper()
	if algorithm == workspace.GitHashSHA256 {
		harness.base = mustProviderGitObject(t, algorithm, 'a')
	}
	attempt := harness.reserve(t, "2026-07-21T"+hour+":01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T"+hour+":02:00Z")
	base, head := attempt.Base(), attempt.VerifiedHead()
	tree := mustProviderGitObject(t, algorithm, 'b')
	frontier, err := workspace.NewAuthorizationFrontier(base, head)
	if err != nil {
		t.Fatal(err)
	}
	recordProviderLifecycleGrant(
		t, harness, attempt, frontier,
		[]workspace.StandingAuthorizationAction{
			workspace.StandingAuthorizationPush,
			workspace.StandingAuthorizationOpenPullRequest,
			workspace.StandingAuthorizationMerge,
		},
		"2026-07-21T"+hour+":03:00Z",
	)
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T"+hour+":04:00Z")}
	evaluator, err := workspace.NewAuthorizationEvaluator(clock)
	if err != nil {
		t.Fatal(err)
	}
	open, err := workspace.NewProviderOpenPullRequestIntent(workspace.ProviderOpenPullRequestIntentOptions{
		Scope:  providerIntentScope(harness, attempt, frontier, workspace.PullRequestIdentity{}),
		Branch: attempt.Branch(), BaseRef: harness.definition.Workspace().BaseRef(),
		Head: head, Tree: tree, Title: "Provider topology", Body: "Verify exact provider topology.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspace.ReserveProviderIntent(
		harness.journal, harness.definition, evaluator,
		workspace.ReserveProviderIntentRequest{Intent: open, OccurredAt: mustTime(t, "2026-07-21T"+hour+":04:01Z")},
	); err != nil {
		t.Fatal(err)
	}
	openResult, _ := workspace.NewProviderOpenPullRequestAdapterResult(
		"open-provider-topology", pullRequestNumber, head,
	)
	adapter := &providerLifecycleAdapter{openResult: openResult}
	broker, err := workspace.NewProviderBroker(harness.definition.Workspace().Provider(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = mustTime(t, "2026-07-21T"+hour+":05:00Z")
	ticket, err := workspace.AuthorizeProviderIntentDispatch(
		harness.journal, harness.definition, evaluator, broker, open.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := workspace.ExecuteProviderIntent(
		context.Background(), harness.journal, harness.definition, broker, ticket,
		mustTime(t, "2026-07-21T"+hour+":05:01Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	pull, ok := execution.Result().PullRequest()
	if !ok {
		t.Fatal("provider open-PR result did not establish identity")
	}
	adapter.pullRequestState = providerPRState(
		t, harness, attempt, pull, head, tree, base, workspace.GitObjectID{}, false,
	)
	if _, _, err := workspace.RecordProviderPullRequestAuthorization(
		context.Background(), harness.journal, harness.definition, broker, open.IntentID(),
		mustTime(t, "2026-07-21T"+hour+":06:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	return providerOpenScenario{
		harness: harness, attempt: attempt, frontier: frontier, clock: clock, evaluator: evaluator,
		adapter: adapter, broker: broker, open: open, pull: pull, base: base, head: head, tree: tree, hour: hour,
	}
}

func TestProviderMergeRequiresConfiguredExactHeadReviewReadiness(t *testing.T) {
	fixture := configuredReviewFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDirectory := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(
		workspaceDirectory, definition, mustTime(t, "2026-07-21T10:00:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDirectory, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	goal, _ := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	harness := attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDirectory,
		git: &fakeAttemptGit{}, base: mustProviderGitObject(t, workspace.GitHashSHA1, 'a'),
		unit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), goal: goal, worktrees: t.TempDir(),
	}
	scenario := newProviderOpenScenarioWithHarness(t, harness, "13", workspace.GitHashSHA1, 171)
	merge := scenario.mergeIntent(t, scenario.pull)
	scenario.clock.now = mustTime(t, "2026-07-21T13:07:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: merge, OccurredAt: mustTime(t, "2026-07-21T13:07:01Z")},
	); err == nil || !strings.Contains(err.Error(), "exact-head clean review readiness") {
		t.Fatalf("merge without configured review readiness error = %v", err)
	}
	if scenario.adapter.mergeCalls != 0 {
		t.Fatalf("provider merge invoked without review readiness: %d", scenario.adapter.mergeCalls)
	}
}

func TestProviderMergeRequiresConfiguredCommitProtocolWithoutReviewToComplete(t *testing.T) {
	fixture := newDefinitionFixture(t)
	configuration := string(fixture.sources.ExecutionConfig.Bytes)
	configuration = strings.Replace(
		configuration,
		"      max_review_fixes: 2\n  - plan_id: alpha-plan\n    merge_unit_id: unit-two",
		`      max_review_fixes: 2
    commit_protocol:
      steps:
        - id: implementation
          subject: Implement provider lifecycle
          body_policy: required
          allowed_paths:
            - src/**
          frozen_paths: []
          checks: []
  - plan_id: alpha-plan
    merge_unit_id: unit-two`,
		1,
	)
	fixture.sources.ExecutionConfig.Bytes = []byte(configuration)
	definition := mustDefinition(t, fixture.sources)
	workspaceDirectory := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(
		workspaceDirectory, definition, mustTime(t, "2026-07-21T10:00:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDirectory, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	goal, _ := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	harness := attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDirectory,
		git: &fakeAttemptGit{}, base: mustProviderGitObject(t, workspace.GitHashSHA1, 'a'),
		unit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), goal: goal, worktrees: t.TempDir(),
	}
	scenario := newProviderOpenScenarioWithHarness(t, harness, "13", workspace.GitHashSHA1, 171)
	merge := scenario.mergeIntent(t, scenario.pull)
	scenario.clock.now = mustTime(t, "2026-07-21T13:07:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: merge, OccurredAt: mustTime(t, "2026-07-21T13:07:01Z")},
	); err == nil || !strings.Contains(err.Error(), "configured commit protocol") {
		t.Fatalf("merge before configured commit protocol completion error = %v", err)
	}
	if scenario.adapter.mergeCalls != 0 {
		t.Fatalf("provider merge invoked before commit protocol completion: %d", scenario.adapter.mergeCalls)
	}
}

func (scenario *providerOpenScenario) mergeIntent(t *testing.T, pull workspace.PullRequestIdentity) workspace.ProviderIntent {
	t.Helper()
	intent, err := workspace.NewProviderMergeIntent(workspace.ProviderMergeIntentOptions{
		Scope:  providerIntentScope(scenario.harness, scenario.attempt, scenario.frontier, pull),
		Branch: scenario.attempt.Branch(), BaseRef: scenario.harness.definition.Workspace().BaseRef(),
		Head: scenario.head, Tree: scenario.tree, Strategy: workspace.ProviderMergeCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func (scenario *providerOpenScenario) executeMerge(
	t *testing.T,
	providerState workspace.ProviderPullRequestState,
	mergeCommit workspace.GitObjectID,
) (workspace.ProviderIntent, workspace.ProviderExecutionResult, error) {
	t.Helper()
	scenario.adapter.pullRequestState = providerState
	intent := scenario.mergeIntent(t, scenario.pull)
	scenario.clock.now = mustTime(t, "2026-07-21T"+scenario.hour+":07:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{
			Intent: intent, OccurredAt: mustTime(t, "2026-07-21T"+scenario.hour+":07:01Z"),
		},
	); err != nil {
		return intent, workspace.ProviderExecutionResult{}, err
	}
	if _, _, err := workspace.RecordProviderMergePreflight(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.broker, intent.IntentID(), mustTime(t, "2026-07-21T"+scenario.hour+":07:02Z"),
	); err != nil {
		return intent, workspace.ProviderExecutionResult{}, err
	}
	scenario.clock.now = mustTime(t, "2026-07-21T"+scenario.hour+":08:00Z")
	ticket, err := workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, scenario.broker, intent.IntentID(),
	)
	if err != nil {
		return intent, workspace.ProviderExecutionResult{}, err
	}
	mergeResult, _ := workspace.NewProviderMergeAdapterResult("merge-provider-topology", mergeCommit, mergeCommit)
	scenario.adapter.mergeResult = mergeResult
	execution, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker, ticket,
		mustTime(t, "2026-07-21T"+scenario.hour+":08:01Z"),
	)
	return intent, execution, err
}

type providerCompletedScenario struct {
	providerOpenScenario
	mergeIntent   workspace.ProviderIntent
	mergeCommit   workspace.GitObjectID
	git           *providerCompletionGit
	request       workspace.ProviderCompletionRequest
	providerState workspace.ProviderPullRequestState
}

func newProviderCompletedScenario(
	t *testing.T,
	hour string,
	algorithm workspace.GitHashAlgorithm,
	pullRequestNumber uint64,
) providerCompletedScenario {
	t.Helper()
	opened := newProviderOpenScenario(t, hour, algorithm, pullRequestNumber)
	mergeCommit := mustProviderGitObject(t, algorithm, 'c')
	unmerged := providerPRState(
		t, opened.harness, opened.attempt, opened.pull, opened.head, opened.tree, opened.base,
		workspace.GitObjectID{}, false,
	)
	mergeIntent, execution, err := opened.executeMerge(t, unmerged, mergeCommit)
	if err != nil || execution.Result().Status() != workspace.ProviderIntentSucceeded {
		t.Fatalf("execute provider merge = %#v err=%v", execution, err)
	}
	providerState := providerPRState(
		t, opened.harness, opened.attempt, opened.pull, opened.head, opened.tree, opened.base,
		mergeCommit, true,
	)
	opened.adapter.pullRequestState = providerState
	headCommit, _ := workspace.NewProviderGitCommit(opened.head, opened.tree, []workspace.GitObjectID{opened.base})
	mergeObject, _ := workspace.NewProviderGitCommit(
		mergeCommit, opened.tree, []workspace.GitObjectID{opened.base, opened.head},
	)
	git := &providerCompletionGit{
		remoteBranch: opened.head, remoteBase: mergeCommit,
		commits: map[string]workspace.ProviderGitCommit{
			opened.head.String(): headCommit, mergeCommit.String(): mergeObject,
		},
		ancestors: map[string]bool{
			opened.base.String() + "\x00" + mergeCommit.String(): true,
			opened.head.String() + "\x00" + mergeCommit.String(): true,
		},
	}
	return providerCompletedScenario{
		providerOpenScenario: opened, mergeIntent: mergeIntent, mergeCommit: mergeCommit,
		git: git, providerState: providerState,
		request: workspace.ProviderCompletionRequest{
			AttemptID:  opened.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-21T"+hour+":09:00Z"),
		},
	}
}

func (scenario *providerCompletedScenario) verify() (workspace.ProviderCompletionReceipt, workspace.JournalRecord, error) {
	return workspace.VerifyAndRecordProviderCompletion(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.broker, scenario.git, scenario.request,
	)
}

func TestProviderMergePreflightRejectsWrongIdentityHeadAndTreeBeforeEffect(t *testing.T) {
	tests := []struct {
		name  string
		state func(*testing.T, *providerOpenScenario) workspace.ProviderPullRequestState
	}{
		{
			name: "wrong head",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				wrong := mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
				return providerPRState(t, scenario.harness, scenario.attempt, scenario.pull, wrong, scenario.tree, scenario.base, workspace.GitObjectID{}, false)
			},
		},
		{
			name: "wrong tree",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				wrong := mustProviderGitObject(t, scenario.tree.Algorithm(), 'd')
				return providerPRState(t, scenario.harness, scenario.attempt, scenario.pull, scenario.head, wrong, scenario.base, workspace.GitObjectID{}, false)
			},
		},
		{
			name: "remote branch drift",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				wrong := mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
				return providerUnmergedState(
					t, scenario, scenario.pull, scenario.harness.definition.Workspace().BaseRef(),
					scenario.attempt.Branch(), scenario.head, scenario.tree, wrong, scenario.base,
					workspace.ProviderCheckPassed, workspace.ProviderReviewApproved,
				)
			},
		},
		{
			name: "pre-merge base drift",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				wrong := mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
				return providerUnmergedState(
					t, scenario, scenario.pull, scenario.harness.definition.Workspace().BaseRef(),
					scenario.attempt.Branch(), scenario.head, scenario.tree, scenario.head, wrong,
					workspace.ProviderCheckPassed, workspace.ProviderReviewApproved,
				)
			},
		},
		{
			name: "wrong branch",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				return providerUnmergedState(
					t, scenario, scenario.pull, scenario.harness.definition.Workspace().BaseRef(),
					"mu/other-branch-a1-0123456789ab", scenario.head, scenario.tree, scenario.head, scenario.base,
					workspace.ProviderCheckPassed, workspace.ProviderReviewApproved,
				)
			},
		},
		{
			name: "wrong base ref",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				return providerUnmergedState(
					t, scenario, scenario.pull, "feature/other-base", scenario.attempt.Branch(),
					scenario.head, scenario.tree, scenario.head, scenario.base,
					workspace.ProviderCheckPassed, workspace.ProviderReviewApproved,
				)
			},
		},
		{
			name: "required check pending",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				return providerUnmergedState(
					t, scenario, scenario.pull, scenario.harness.definition.Workspace().BaseRef(),
					scenario.attempt.Branch(), scenario.head, scenario.tree, scenario.head, scenario.base,
					workspace.ProviderCheckPending, workspace.ProviderReviewApproved,
				)
			},
		},
		{
			name: "required review changes requested",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				return providerUnmergedState(
					t, scenario, scenario.pull, scenario.harness.definition.Workspace().BaseRef(),
					scenario.attempt.Branch(), scenario.head, scenario.tree, scenario.head, scenario.base,
					workspace.ProviderCheckPassed, workspace.ProviderReviewChangesRequested,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := newProviderOpenScenario(t, "13", workspace.GitHashSHA1, 171)
			_, execution, err := scenario.executeMerge(t, test.state(t, &scenario), mustProviderGitObject(t, workspace.GitHashSHA1, 'c'))
			if err == nil || !execution.Result().Digest().IsZero() || scenario.adapter.mergeCalls != 0 {
				t.Fatalf("merge preflight outcome = %#v calls=%d err=%v", execution, scenario.adapter.mergeCalls, err)
			}
		})
	}
	t.Run("wrong pull request identity", func(t *testing.T) {
		scenario := newProviderOpenScenario(t, "13", workspace.GitHashSHA1, 171)
		other := newProviderOpenScenario(t, "14", workspace.GitHashSHA1, 172)
		wrong := providerPRState(
			t, scenario.harness, scenario.attempt, other.pull, scenario.head, scenario.tree, scenario.base,
			workspace.GitObjectID{}, false,
		)
		_, execution, err := scenario.executeMerge(t, wrong, mustProviderGitObject(t, workspace.GitHashSHA1, 'c'))
		if err == nil || !execution.Result().Digest().IsZero() || scenario.adapter.mergeCalls != 0 {
			t.Fatalf("wrong-identity preflight = %#v calls=%d err=%v", execution, scenario.adapter.mergeCalls, err)
		}
	})
}

func TestProviderFailedBeforeEffectCanRetryExactMergeIntent(t *testing.T) {
	scenario := newProviderOpenScenario(t, "13", workspace.GitHashSHA1, 171)
	valid := providerPRState(
		t, scenario.harness, scenario.attempt, scenario.pull, scenario.head, scenario.tree, scenario.base,
		workspace.GitObjectID{}, false,
	)
	scenario.adapter.pullRequestState = valid
	intent := scenario.mergeIntent(t, scenario.pull)
	scenario.clock.now = mustTime(t, "2026-07-21T13:07:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: intent, OccurredAt: mustTime(t, "2026-07-21T13:07:01Z")},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspace.RecordProviderMergePreflight(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.broker, intent.IntentID(), mustTime(t, "2026-07-21T13:07:02Z"),
	); err != nil {
		t.Fatal(err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T13:08:00Z")
	ticket, err := workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, scenario.broker, intent.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongTree := mustProviderGitObject(t, workspace.GitHashSHA1, 'd')
	scenario.adapter.pullRequestState = providerUnmergedState(
		t, &scenario, scenario.pull, scenario.harness.definition.Workspace().BaseRef(), scenario.attempt.Branch(),
		scenario.head, wrongTree, scenario.head, scenario.base,
		workspace.ProviderCheckPassed, workspace.ProviderReviewApproved,
	)
	first, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker, ticket,
		mustTime(t, "2026-07-21T13:08:01Z"),
	)
	if err == nil || first.Result().Status() != workspace.ProviderIntentFailedBeforeEffect || scenario.adapter.mergeCalls != 0 {
		t.Fatalf("first merge dispatch = %#v calls=%d err=%v", first, scenario.adapter.mergeCalls, err)
	}
	scenario.adapter.pullRequestState = valid
	scenario.clock.now = mustTime(t, "2026-07-21T13:09:00Z")
	retried, record, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: intent, OccurredAt: mustTime(t, "2026-07-21T13:09:01Z")},
	)
	if err != nil || retried.Status() != workspace.ProviderIntentReserved ||
		record.EventType() != workspace.JournalEventProviderIntentReserved {
		t.Fatalf("retry reservation = %#v record=%s err=%v", retried, record.EventType(), err)
	}
	if _, exists := retried.MergePreflight(); exists {
		t.Fatal("retry reservation retained stale merge preflight")
	}
	if _, _, err := workspace.RecordProviderMergePreflight(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.broker, intent.IntentID(), mustTime(t, "2026-07-21T13:09:02Z"),
	); err != nil {
		t.Fatal(err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T13:10:00Z")
	ticket, err = workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, scenario.broker, intent.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	mergeCommit := mustProviderGitObject(t, workspace.GitHashSHA1, 'c')
	scenario.adapter.mergeResult, _ = workspace.NewProviderMergeAdapterResult("retry-merge", mergeCommit, mergeCommit)
	second, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker, ticket,
		mustTime(t, "2026-07-21T13:10:01Z"),
	)
	if err != nil || second.Result().Status() != workspace.ProviderIntentSucceeded || scenario.adapter.mergeCalls != 1 {
		t.Fatalf("retried merge dispatch = %#v calls=%d err=%v", second, scenario.adapter.mergeCalls, err)
	}
}

func TestProviderIntentAfterOpenPRMustBindSoleDerivedIdentity(t *testing.T) {
	scenario := newProviderOpenScenario(t, "13", workspace.GitHashSHA1, 171)
	pushWithoutIdentity, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
		Scope:  providerIntentScope(scenario.harness, scenario.attempt, scenario.frontier, workspace.PullRequestIdentity{}),
		Branch: scenario.attempt.Branch(), ExpectRemoteAbsent: true, Head: scenario.head,
	})
	if err != nil {
		t.Fatal(err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T13:07:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: pushWithoutIdentity, OccurredAt: mustTime(t, "2026-07-21T13:07:01Z")},
	); err == nil || !strings.Contains(err.Error(), "provider-derived pull request") {
		t.Fatalf("post-PR push without identity error = %v", err)
	}
	other := newProviderOpenScenario(t, "14", workspace.GitHashSHA1, 172)
	wrongMerge := scenario.mergeIntent(t, other.pull)
	if _, _, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: wrongMerge, OccurredAt: mustTime(t, "2026-07-21T13:07:02Z")},
	); err == nil || !strings.Contains(err.Error(), "sole provider-derived") {
		t.Fatalf("merge with foreign PR identity error = %v", err)
	}
}

func TestProviderPushRequiresExplicitAuthorizedLeaseBeforeAnyWrite(t *testing.T) {
	scenario := newProviderOpenScenario(t, "13", workspace.GitHashSHA1, 171)
	if _, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
		Scope:  providerIntentScope(scenario.harness, scenario.attempt, scenario.frontier, scenario.pull),
		Branch: scenario.attempt.Branch(), Head: scenario.head, Tree: scenario.tree,
	}); err == nil || !strings.Contains(err.Error(), "exactly one explicit") {
		t.Fatalf("missing post-PR push lease error = %v", err)
	}
	wrong := mustProviderGitObject(t, workspace.GitHashSHA1, 'd')
	if _, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
		Scope:  providerIntentScope(scenario.harness, scenario.attempt, scenario.frontier, scenario.pull),
		Branch: scenario.attempt.Branch(), ExpectedRemoteHead: wrong,
		Head: scenario.head, Tree: scenario.tree,
	}); err == nil || !strings.Contains(err.Error(), "frontier-base lease") {
		t.Fatalf("wrong post-PR push lease error = %v", err)
	}
	if _, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
		Scope:  providerIntentScope(scenario.harness, scenario.attempt, scenario.frontier, workspace.PullRequestIdentity{}),
		Branch: scenario.attempt.Branch(), Head: scenario.head,
	}); err == nil || !strings.Contains(err.Error(), "exactly one explicit") {
		t.Fatalf("missing initial push absence lease error = %v", err)
	}
	if scenario.adapter.pushCalls != 0 {
		t.Fatalf("provider push ran while validating rejected leases: %d", scenario.adapter.pushCalls)
	}
}

func TestProviderCompletionRejectsIndependentRemoteAndExactTopologyDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *providerCompletedScenario)
	}{
		{
			name: "remote branch drift",
			mutate: func(t *testing.T, scenario *providerCompletedScenario) {
				scenario.git.remoteBranch = mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
			},
		},
		{
			name: "final base drift",
			mutate: func(t *testing.T, scenario *providerCompletedScenario) {
				scenario.git.remoteBase = mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
			},
		},
		{
			name: "reviewed head tree drift",
			mutate: func(t *testing.T, scenario *providerCompletedScenario) {
				wrongTree := mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
				commit, _ := workspace.NewProviderGitCommit(scenario.head, wrongTree, []workspace.GitObjectID{scenario.base})
				scenario.git.commits[scenario.head.String()] = commit
			},
		},
		{
			name: "merge first parent drift",
			mutate: func(t *testing.T, scenario *providerCompletedScenario) {
				wrongParent := mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
				commit, _ := workspace.NewProviderGitCommit(
					scenario.mergeCommit, scenario.tree, []workspace.GitObjectID{wrongParent, scenario.head},
				)
				scenario.git.commits[scenario.mergeCommit.String()] = commit
			},
		},
		{
			name: "merge second parent drift",
			mutate: func(t *testing.T, scenario *providerCompletedScenario) {
				wrongParent := mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
				commit, _ := workspace.NewProviderGitCommit(
					scenario.mergeCommit, scenario.tree, []workspace.GitObjectID{scenario.base, wrongParent},
				)
				scenario.git.commits[scenario.mergeCommit.String()] = commit
			},
		},
		{
			name: "merge parent count drift",
			mutate: func(t *testing.T, scenario *providerCompletedScenario) {
				commit, _ := workspace.NewProviderGitCommit(
					scenario.mergeCommit, scenario.tree, []workspace.GitObjectID{scenario.base},
				)
				scenario.git.commits[scenario.mergeCommit.String()] = commit
			},
		},
		{
			name: "merge tree drift",
			mutate: func(t *testing.T, scenario *providerCompletedScenario) {
				wrongTree := mustProviderGitObject(t, scenario.head.Algorithm(), 'd')
				commit, _ := workspace.NewProviderGitCommit(
					scenario.mergeCommit, wrongTree, []workspace.GitObjectID{scenario.base, scenario.head},
				)
				scenario.git.commits[scenario.mergeCommit.String()] = commit
			},
		},
		{
			name: "base ancestry missing",
			mutate: func(_ *testing.T, scenario *providerCompletedScenario) {
				scenario.git.ancestors[scenario.base.String()+"\x00"+scenario.mergeCommit.String()] = false
			},
		},
		{
			name: "head ancestry missing",
			mutate: func(_ *testing.T, scenario *providerCompletedScenario) {
				scenario.git.ancestors[scenario.head.String()+"\x00"+scenario.mergeCommit.String()] = false
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := newProviderCompletedScenario(t, "13", workspace.GitHashSHA1, 171)
			test.mutate(t, &scenario)
			if receipt, _, err := scenario.verify(); err == nil || !receipt.Digest().IsZero() {
				t.Fatalf("drifted completion produced receipt %#v err=%v", receipt, err)
			}
		})
	}
}

func TestProviderCompletionRejectsRequiredCheckAndReviewFailures(t *testing.T) {
	tests := []struct {
		name   string
		check  workspace.ProviderCheckConclusion
		review workspace.ProviderReviewConclusion
	}{
		{name: "required check failed", check: workspace.ProviderCheckFailed, review: workspace.ProviderReviewApproved},
		{name: "required review pending", check: workspace.ProviderCheckPassed, review: workspace.ProviderReviewPending},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := newProviderCompletedScenario(t, "13", workspace.GitHashSHA1, 171)
			check, _ := workspace.NewProviderCheckState(
				workspace.MustID("ci"), true, test.check, workspace.DigestBytes([]byte("ci-evidence")),
			)
			review, _ := workspace.NewProviderReviewState(
				workspace.MustID("owners"), true, test.review, workspace.DigestBytes([]byte("review-evidence")),
			)
			state, err := workspace.NewProviderPullRequestState(workspace.ProviderPullRequestStateOptions{
				Repository: scenario.harness.definition.Workspace().Repository(), PullRequest: scenario.pull,
				BaseRef: scenario.harness.definition.Workspace().BaseRef(), Branch: scenario.attempt.Branch(),
				Head: scenario.head, HeadTree: scenario.tree, RemoteBranchHead: scenario.head,
				BaseHeadBeforeMerge: scenario.base, Checks: []workspace.ProviderCheckState{check},
				Reviews: []workspace.ProviderReviewState{review}, Merged: true,
				MergeStrategy: workspace.ProviderMergeCommit, MergeCommit: scenario.mergeCommit,
				FinalBaseHead: scenario.mergeCommit, RequestMarker: "provider-required-evidence",
			})
			if err != nil {
				t.Fatal(err)
			}
			scenario.adapter.pullRequestState = state
			if receipt, _, err := scenario.verify(); err == nil || !receipt.Digest().IsZero() {
				t.Fatalf("failed provider evidence produced receipt %#v err=%v", receipt, err)
			}
		})
	}
}

func TestProviderCompletionReceiptIsCanonicalTamperEvidentAndSupportsSHA256Git(t *testing.T) {
	scenario := newProviderCompletedScenario(t, "13", workspace.GitHashSHA256, 171)
	receipt, _, err := scenario.verify()
	if err != nil {
		t.Fatal(err)
	}
	for name, object := range map[string]workspace.GitObjectID{
		"base": receipt.BaseHead(), "head": receipt.Head(), "head tree": receipt.HeadTree(),
		"merge": receipt.MergeCommit(), "merge tree": receipt.MergeTree(), "final base": receipt.FinalBaseHead(),
	} {
		if object.Algorithm() != workspace.GitHashSHA256 {
			t.Fatalf("receipt %s algorithm = %s, want sha256", name, object.Algorithm())
		}
	}
	encoded, err := receipt.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := workspace.DecodeProviderCompletionReceipt(encoded)
	if err != nil || decoded.Digest() != receipt.Digest() {
		t.Fatalf("canonical SHA-256 receipt round trip = %#v err=%v", decoded, err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"definition_digest", "generation", "base_head", "head", "head_tree", "merge_commit",
		"merge_tree", "final_base_head", "provider_observation_digest", "topology_digest",
	} {
		if value, ok := wire[field].(string); !ok || value == "" {
			t.Fatalf("canonical completion receipt is missing %s: %#v", field, wire[field])
		}
	}
	for _, field := range []string{
		"check_evidence_digests", "review_evidence_digests", "owner_receipt_digests", "provider_result_digests",
	} {
		if values, ok := wire[field].([]any); !ok || len(values) == 0 {
			t.Fatalf("canonical completion receipt is missing bound %s: %#v", field, wire[field])
		}
	}
	wire["head"] = mustProviderGitObject(t, workspace.GitHashSHA256, 'd').String()
	tampered, _ := json.Marshal(wire)
	if _, err := workspace.DecodeProviderCompletionReceipt(tampered); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("completion receipt accepted head tampering: %v", err)
	}
	_ = json.Unmarshal(encoded, &wire)
	wire["unexpected"] = true
	tampered, _ = json.Marshal(wire)
	if _, err := workspace.DecodeProviderCompletionReceipt(tampered); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("completion receipt accepted unknown field: %v", err)
	}
}

func mustProviderGitObject(t *testing.T, algorithm workspace.GitHashAlgorithm, digit byte) workspace.GitObjectID {
	t.Helper()
	length := 40
	if algorithm == workspace.GitHashSHA256 {
		length = 64
	}
	object, err := workspace.ParseGitObjectID(string(algorithm) + ":" + strings.Repeat(string(digit), length))
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func providerUnmergedState(
	t *testing.T,
	scenario *providerOpenScenario,
	pull workspace.PullRequestIdentity,
	baseRef, branch string,
	head, tree, remoteHead, base workspace.GitObjectID,
	checkConclusion workspace.ProviderCheckConclusion,
	reviewConclusion workspace.ProviderReviewConclusion,
) workspace.ProviderPullRequestState {
	t.Helper()
	check, _ := workspace.NewProviderCheckState(
		workspace.MustID("ci"), true, checkConclusion, workspace.DigestBytes([]byte("ci-evidence")),
	)
	review, _ := workspace.NewProviderReviewState(
		workspace.MustID("owners"), true, reviewConclusion, workspace.DigestBytes([]byte("review-evidence")),
	)
	state, err := workspace.NewProviderPullRequestState(workspace.ProviderPullRequestStateOptions{
		Repository: scenario.harness.definition.Workspace().Repository(), PullRequest: pull,
		BaseRef: baseRef, Branch: branch, Head: head, HeadTree: tree,
		RemoteBranchHead: remoteHead, BaseHeadBeforeMerge: base,
		Checks: []workspace.ProviderCheckState{check}, Reviews: []workspace.ProviderReviewState{review},
		RequestMarker: "provider-merge-preflight",
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
