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
	parent    workspace.StandingGrant
	derived   workspace.StandingGrant
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
	harness := newAttemptHarnessFromFixture(
		t, newDefinitionFixtureForHash(t, algorithm), "unit-one",
	)
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
	attempt := harness.reserve(t, "2026-07-21T"+hour+":01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T"+hour+":02:00Z")
	return newProviderOpenScenarioForAttempt(t, harness, attempt, hour, algorithm, pullRequestNumber)
}

func newProviderOpenScenarioForAttempt(
	t *testing.T,
	harness attemptHarness,
	attempt workspace.RuntimeAttemptProjection,
	hour string,
	algorithm workspace.GitHashAlgorithm,
	pullRequestNumber uint64,
) providerOpenScenario {
	t.Helper()
	base, head := attempt.Base(), attempt.VerifiedHead()
	tree := mustProviderGitObject(t, algorithm, 'b')
	frontier, err := workspace.NewAuthorizationFrontier(base, head)
	if err != nil {
		t.Fatal(err)
	}
	parent := recordProviderLifecycleGrant(
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
	identitySeed, _ := workspace.NewProviderPullRequestObservation(
		harness.definition.Workspace().Provider().Kind(), harness.definition.Workspace().Repository(),
		pullRequestNumber, head, workspace.DigestBytes([]byte("open-postflight-identity")),
	)
	adapter := &providerLifecycleAdapter{
		openResult: openResult,
		pullRequestState: providerPRState(
			t, harness, attempt, identitySeed.PullRequest(), head, tree, base, workspace.GitObjectID{}, false,
		),
	}
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
	derived, _, err := workspace.RecordProviderPullRequestAuthorization(
		context.Background(), harness.journal, harness.definition, broker, open.IntentID(),
		mustTime(t, "2026-07-21T"+hour+":06:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return providerOpenScenario{
		harness: harness, attempt: attempt, frontier: frontier, clock: clock, evaluator: evaluator,
		adapter: adapter, broker: broker, open: open, pull: pull, parent: parent, derived: derived,
		base: base, head: head, tree: tree, hour: hour,
	}
}

func TestProviderOpenPullRequestRequiresExactTopologyBeforeSuccess(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *workspace.ProviderPullRequestStateOptions)
	}{
		{name: "wrong base ref", mutate: func(_ *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			options.BaseRef = "feature/other-base"
		}},
		{name: "wrong branch", mutate: func(_ *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			options.Branch = "mu/other-provider-a1-0123456789ab"
		}},
		{name: "wrong base head", mutate: func(t *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			options.BaseHeadBeforeMerge = mustGitObject(t, 'd')
		}},
		{name: "wrong head", mutate: func(t *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			options.Head = mustGitObject(t, 'd')
			options.RemoteBranchHead = options.Head
		}},
		{name: "wrong tree", mutate: func(t *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			options.HeadTree = mustGitObject(t, 'd')
		}},
		{name: "remote head drift", mutate: func(t *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			options.RemoteBranchHead = mustGitObject(t, 'd')
		}},
		{name: "already merged", mutate: func(t *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			merge := mustGitObject(t, 'd')
			options.Lifecycle = workspace.ProviderPullRequestClosed
			options.Merged = true
			options.MergeStrategy = workspace.ProviderMergeCommit
			options.MergeCommit = merge
			options.FinalBaseHead = merge
		}},
		{name: "closed without merge", mutate: func(_ *testing.T, options *workspace.ProviderPullRequestStateOptions) {
			options.Lifecycle = workspace.ProviderPullRequestClosed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newAttemptHarness(t, "unit-one")
			attempt := harness.reserve(t, "2026-07-21T12:01:00Z")
			attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T12:02:00Z")
			base, head, tree := attempt.Base(), attempt.VerifiedHead(), mustGitObject(t, 'b')
			frontier, _ := workspace.NewAuthorizationFrontier(base, head)
			recordProviderLifecycleGrant(
				t, harness, attempt, frontier,
				[]workspace.StandingAuthorizationAction{
					workspace.StandingAuthorizationOpenPullRequest, workspace.StandingAuthorizationMerge,
				},
				"2026-07-21T12:03:00Z",
			)
			clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T12:04:00Z")}
			evaluator, _ := workspace.NewAuthorizationEvaluator(clock)
			intent, err := workspace.NewProviderOpenPullRequestIntent(workspace.ProviderOpenPullRequestIntentOptions{
				Scope:  providerIntentScope(harness, attempt, frontier, workspace.PullRequestIdentity{}),
				Branch: attempt.Branch(), BaseRef: harness.definition.Workspace().BaseRef(),
				Head: head, Tree: tree, Title: "Topology postflight", Body: "Require exact provider state.",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := workspace.ReserveProviderIntent(
				harness.journal, harness.definition, evaluator,
				workspace.ReserveProviderIntentRequest{Intent: intent, OccurredAt: mustTime(t, "2026-07-21T12:04:01Z")},
			); err != nil {
				t.Fatal(err)
			}
			identitySeed, _ := workspace.NewProviderPullRequestObservation(
				harness.definition.Workspace().Provider().Kind(), harness.definition.Workspace().Repository(),
				171, head, workspace.DigestBytes([]byte("open-topology-test")),
			)
			options := workspace.ProviderPullRequestStateOptions{
				Repository: harness.definition.Workspace().Repository(), PullRequest: identitySeed.PullRequest(),
				BaseRef: harness.definition.Workspace().BaseRef(), Branch: attempt.Branch(),
				BaseHeadBeforeMerge: base, Head: head, HeadTree: tree, RemoteBranchHead: head,
				Lifecycle: workspace.ProviderPullRequestOpen, RequestMarker: "open-topology-postflight",
			}
			test.mutate(t, &options)
			state, err := workspace.NewProviderPullRequestState(options)
			if err != nil {
				t.Fatal(err)
			}
			openResult, _ := workspace.NewProviderOpenPullRequestAdapterResult("open-topology", 171, head)
			adapter := &providerLifecycleAdapter{openResult: openResult, pullRequestState: state}
			broker, _ := workspace.NewProviderBroker(harness.definition.Workspace().Provider(), adapter)
			clock.now = mustTime(t, "2026-07-21T12:05:00Z")
			ticket, err := workspace.AuthorizeProviderIntentDispatch(
				harness.journal, harness.definition, evaluator, broker, intent.IntentID(),
			)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := workspace.ExecuteProviderIntent(
				context.Background(), harness.journal, harness.definition, broker, ticket,
				mustTime(t, "2026-07-21T12:05:01Z"),
			)
			if err == nil || execution.Result().Status() != workspace.ProviderIntentAmbiguous ||
				adapter.openCalls != 1 || adapter.queryPRCalls != 1 {
				t.Fatalf("wrong open-PR topology execution = %#v open=%d query=%d err=%v", execution, adapter.openCalls, adapter.queryPRCalls, err)
			}
			if _, ok := execution.Result().PullRequest(); ok {
				t.Fatal("ambiguous open-PR postflight established authorization identity")
			}
		})
	}
}

func TestProviderMergeRequiresConfiguredExactHeadReviewReadiness(t *testing.T) {
	fixture := configuredReviewFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDirectory := t.TempDir()
	if _, err := initializeWorkspaceV2(t,
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
	if _, err := initializeWorkspaceV2(t,
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
		IntegrationBaseHead: scenario.attempt.Base(), Head: scenario.head, Tree: scenario.tree,
		Strategy: workspace.ProviderMergeCommit,
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
			name: "closed without merge",
			state: func(t *testing.T, scenario *providerOpenScenario) workspace.ProviderPullRequestState {
				return providerUnmergedStateWithLifecycle(
					t, scenario, scenario.pull, scenario.harness.definition.Workspace().BaseRef(),
					scenario.attempt.Branch(), scenario.head, scenario.tree, scenario.head, scenario.base,
					workspace.ProviderCheckPassed, workspace.ProviderReviewApproved, workspace.ProviderPullRequestClosed,
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
		other, err := workspace.NewProviderPullRequestObservation(
			scenario.harness.definition.Workspace().Provider().Kind(),
			scenario.harness.definition.Workspace().Repository(),
			172,
			scenario.head,
			workspace.DigestBytes([]byte("wrong-pull-request-identity")),
		)
		if err != nil {
			t.Fatal(err)
		}
		wrong := providerPRState(
			t, scenario.harness, scenario.attempt, other.PullRequest(), scenario.head, scenario.tree, scenario.base,
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
	failedDigest := first.Result().Digest()
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
	succeededDigest := second.Result().Digest()
	scenario.adapter.pullRequestState = providerPRState(
		t, scenario.harness, scenario.attempt, scenario.pull, scenario.head, scenario.tree, scenario.base,
		mergeCommit, true,
	)
	headCommit, _ := workspace.NewProviderGitCommit(scenario.head, scenario.tree, []workspace.GitObjectID{scenario.base})
	mergeObject, _ := workspace.NewProviderGitCommit(
		mergeCommit, scenario.tree, []workspace.GitObjectID{scenario.base, scenario.head},
	)
	git := &providerCompletionGit{
		remoteBranch: scenario.head, remoteBase: mergeCommit,
		commits: map[string]workspace.ProviderGitCommit{
			scenario.head.String(): headCommit, mergeCommit.String(): mergeObject,
		},
		ancestors: map[string]bool{
			scenario.base.String() + "\x00" + mergeCommit.String(): true,
			scenario.head.String() + "\x00" + mergeCommit.String(): true,
		},
	}
	receipt, _, err := workspace.VerifyAndRecordProviderCompletion(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker, git,
		workspace.ProviderCompletionRequest{
			AttemptID: scenario.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T13:11:00Z"),
		},
	)
	if err != nil {
		t.Fatalf("verify completion after retry: %v", err)
	}
	encoded, err := receipt.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		ProviderResults []string `json:"provider_result_digests"`
	}
	if err := json.Unmarshal(encoded, &evidence); err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool, len(evidence.ProviderResults))
	for _, digest := range evidence.ProviderResults {
		found[digest] = true
	}
	if !found[failedDigest.String()] || !found[succeededDigest.String()] {
		t.Fatalf("retry receipt evidence = %#v; want failed %s and succeeded %s", evidence.ProviderResults, failedDigest, succeededDigest)
	}
}

func TestProviderReviewFixPushAndMergeKeepIntegrationBaseIndependent(t *testing.T) {
	harness := newReviewHarness(t)
	initialHead := mustGitObject(t, 'c')
	initialTree := mustGitObject(t, 'b')
	harness.repository.snapshot, _ = workspace.NewReviewRepositorySnapshot(initialHead, initialTree, true)
	firstRound, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:20:00Z"),
		},
	)
	if err != nil || firstRound.Request().Head() != initialHead {
		t.Fatalf("start initial review = %#v err=%v", firstRound, err)
	}
	medium := mustReviewFinding(t, workspace.ReviewSeverityMedium, "provider review fix")
	security := reviewSubmission(
		t, firstRound.Request(), workspace.MustID("security-provider-one"), workspace.ReviewResultCompleted,
		[]workspace.ReviewFinding{medium}, workspace.Digest{},
	)
	harness.record(t, firstRound.Request(), security, "2026-07-21T11:20:01Z")
	reviewState := mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	correctnessRequest, _, _ := reviewState.NextRequest()
	correctness := reviewSubmission(
		t, correctnessRequest, workspace.MustID("correctness-provider-one"),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	harness.record(t, correctnessRequest, correctness, "2026-07-21T11:20:02Z")
	if _, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, harness.attempt.AttemptID(),
	); err != nil {
		t.Fatalf("confirm initial review readiness: %v", err)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	adopted, exists := runtime.Attempt(harness.attempt.AttemptID())
	if !exists || adopted.Base() == adopted.VerifiedHead() || adopted.VerifiedHead() != initialHead {
		t.Fatalf("adopted implementation attempt = %#v exists=%v", adopted, exists)
	}
	opened := newProviderOpenScenarioForAttempt(
		t, harness.attemptHarness, adopted, "13", workspace.GitHashSHA1, 171,
	)

	protocol := configuredReviewFixProtocol(t, harness.definition)
	step, err := protocol.Step(1)
	if err != nil {
		t.Fatal(err)
	}
	fixTree, fixHead, changed := mustGitObject(t, 'd'), mustGitObject(t, 'e'), mustGitObject(t, 'f')
	diff := addedDiff(t, "src/provider_fix.go", changed)
	staged, err := workspace.NewStagedCommitInspection(initialHead, fixTree, diff, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := workspace.NewGitCommitInspection(
		fixHead, []workspace.GitObjectID{initialHead}, fixTree,
		step.Message().Subject(), "apply provider review fix", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	commitGit := &journalCommitGit{
		branch: adopted.Branch(), head: initialHead, staged: staged, commit: commit,
	}
	checkRunner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, err := workspace.NewCommitProtocolShell(commitGit, checkRunner)
	if err != nil {
		t.Fatal(err)
	}
	fixResult, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), harness.journal, harness.definition, shell,
		workspace.ExecuteAttemptReviewFixRequest{
			AttemptID: adopted.AttemptID(), Ordinal: 1, Body: "apply provider review fix",
			AcceptedFindingIDs: []workspace.Digest{medium.ID()},
			OccurredAt:         mustTime(t, "2026-07-21T14:00:00Z"),
		},
	)
	if err != nil || fixResult.Attempt().VerifiedHead() != fixHead {
		t.Fatalf("execute provider review fix = %#v err=%v", fixResult, err)
	}
	harness.git.setHead(t, adopted.Branch(), fixHead, true)
	harness.repository.snapshot, _ = workspace.NewReviewRepositorySnapshot(fixHead, fixTree, true)
	if _, _, err := workspace.RecordReviewFixApplication(
		harness.journal, harness.definition, workspace.RecordReviewFixApplicationRequest{
			AttemptID: adopted.AttemptID(), Ordinal: 1, AcceptedFindingIDs: []workspace.Digest{medium.ID()},
			OccurredAt: mustTime(t, "2026-07-21T14:00:01Z"),
		},
	); err != nil {
		t.Fatalf("record provider review fix: %v", err)
	}
	secondRound, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: adopted.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T14:00:02Z"),
		},
	)
	if err != nil || secondRound.Request().Head() != fixHead || secondRound.Request().Tree() != fixTree {
		t.Fatalf("start post-fix review = %#v err=%v", secondRound, err)
	}
	cleanSecurity := reviewSubmission(
		t, secondRound.Request(), workspace.MustID("security-provider-one"),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	harness.record(t, secondRound.Request(), cleanSecurity, "2026-07-21T14:00:03Z")
	reviewState = mustReviewState(t, harness.journal, harness.definition, adopted.AttemptID())
	cleanCorrectnessRequest, _, _ := reviewState.NextRequest()
	cleanCorrectness := reviewSubmission(
		t, cleanCorrectnessRequest, workspace.MustID("correctness-provider-two"),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	harness.record(t, cleanCorrectnessRequest, cleanCorrectness, "2026-07-21T14:00:04Z")
	if readiness, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, adopted.AttemptID(),
	); err != nil || readiness.Head() != fixHead || readiness.Tree() != fixTree {
		t.Fatalf("confirm post-fix readiness = %#v err=%v", readiness, err)
	}
	updatedAttempt := fixResult.Attempt()
	fixFrontier, _ := workspace.NewAuthorizationFrontier(initialHead, fixHead)
	fixScope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: harness.definition.Workspace().ID(), Repository: harness.definition.Workspace().Repository(),
		Remote: harness.definition.Workspace().Remote(), Generation: harness.definition.Generation(),
		SerialSegment: updatedAttempt.SerialSegment(), Frontier: fixFrontier,
		Actions: []workspace.StandingAuthorizationAction{
			workspace.StandingAuthorizationPush, workspace.StandingAuthorizationMerge,
		},
		ExpiresAt: mustTime(t, "2026-07-21T20:00:00Z"), Epoch: 1, RequiresProviderPullRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixBinding, _ := workspace.StandingGrantControlPlaneBinding(fixScope)
	fixParent, _, err := workspace.RecordStandingGrant(
		context.Background(), harness.journal, harness.definition,
		&boundaryVerifier{expectedRequest: fixScope.Digest()}, fixScope,
		controlPlaneReceipt(t, fixBinding, "provider-review-fix-grant"),
		mustTime(t, "2026-07-21T15:00:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	query, _ := workspace.NewProviderPullRequestQuery(harness.definition.Workspace().Repository(), opened.pull)
	currentPR, err := opened.broker.ObservePullRequestForAuthorization(
		context.Background(), query, workspace.DigestBytes([]byte("provider-review-fix-observation")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspace.RecordPullRequestStandingGrantFrontierAdvance(
		context.Background(), harness.journal, harness.definition, opened.broker,
		fixParent.GrantID(), opened.derived.GrantID(), currentPR, mustTime(t, "2026-07-21T15:00:01Z"),
	); err != nil {
		t.Fatalf("record review-fix frontier advance: %v", err)
	}

	opened.attempt = updatedAttempt
	opened.frontier = fixFrontier
	opened.head = fixHead
	opened.tree = fixTree
	opened.adapter.pullRequestState = providerPRState(
		t, opened.harness, updatedAttempt, opened.pull, fixHead, fixTree, opened.base,
		workspace.GitObjectID{}, false,
	)
	opened.adapter.pushResult, _ = workspace.NewProviderPushAdapterResult("review-fix-push", fixHead)
	push, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
		Scope:  providerIntentScope(opened.harness, updatedAttempt, fixFrontier, opened.pull),
		Branch: updatedAttempt.Branch(), ExpectedRemoteHead: initialHead, Head: fixHead, Tree: fixTree,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened.clock.now = mustTime(t, "2026-07-21T16:00:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		opened.harness.journal, opened.harness.definition, opened.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: push, OccurredAt: mustTime(t, "2026-07-21T16:00:01Z")},
	); err != nil {
		t.Fatalf("reserve review-fix push: %v", err)
	}
	opened.clock.now = mustTime(t, "2026-07-21T16:00:02Z")
	pushTicket, err := workspace.AuthorizeProviderIntentDispatch(
		opened.harness.journal, opened.harness.definition, opened.evaluator, opened.broker, push.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if execution, err := workspace.ExecuteProviderIntent(
		context.Background(), opened.harness.journal, opened.harness.definition, opened.broker, pushTicket,
		mustTime(t, "2026-07-21T16:00:03Z"),
	); err != nil || execution.Result().Status() != workspace.ProviderIntentSucceeded {
		t.Fatalf("execute review-fix push = %#v err=%v", execution, err)
	}
	if expected, ok := opened.adapter.lastPush.ExpectedRemoteHead(); !ok || expected != initialHead {
		t.Fatalf("review-fix push lease = %s, %v; want prior PR head %s", expected, ok, initialHead)
	}

	merge := opened.mergeIntent(t, opened.pull)
	if merge.IntegrationBaseHead() != opened.base || merge.Frontier().Base() != initialHead {
		t.Fatalf("merge bases = integration %s authorization %s", merge.IntegrationBaseHead(), merge.Frontier().Base())
	}
	opened.clock.now = mustTime(t, "2026-07-21T16:01:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		opened.harness.journal, opened.harness.definition, opened.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: merge, OccurredAt: mustTime(t, "2026-07-21T16:01:01Z")},
	); err != nil {
		t.Fatalf("reserve review-fix merge: %v", err)
	}
	if _, _, err := workspace.RecordProviderMergePreflight(
		context.Background(), opened.harness.journal, opened.harness.definition, opened.broker,
		merge.IntentID(), mustTime(t, "2026-07-21T16:01:02Z"),
	); err != nil {
		t.Fatalf("record review-fix merge preflight: %v", err)
	}
	opened.clock.now = mustTime(t, "2026-07-21T16:02:00Z")
	mergeTicket, err := workspace.AuthorizeProviderIntentDispatch(
		opened.harness.journal, opened.harness.definition, opened.evaluator, opened.broker, merge.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	mergeCommit := mustGitObject(t, '1')
	opened.adapter.mergeResult, _ = workspace.NewProviderMergeAdapterResult("review-fix-merge", mergeCommit, mergeCommit)
	if execution, err := workspace.ExecuteProviderIntent(
		context.Background(), opened.harness.journal, opened.harness.definition, opened.broker, mergeTicket,
		mustTime(t, "2026-07-21T16:02:01Z"),
	); err != nil || execution.Result().Status() != workspace.ProviderIntentSucceeded {
		t.Fatalf("execute review-fix merge = %#v err=%v", execution, err)
	}
	if opened.adapter.lastMerge.ExpectedBaseHead() != opened.base {
		t.Fatalf("provider merge expected base = %s, want durable integration base %s", opened.adapter.lastMerge.ExpectedBaseHead(), opened.base)
	}

	opened.adapter.pullRequestState = providerPRState(
		t, opened.harness, updatedAttempt, opened.pull, fixHead, fixTree, opened.base, mergeCommit, true,
	)
	headCommit, _ := workspace.NewProviderGitCommit(fixHead, fixTree, []workspace.GitObjectID{initialHead})
	mergeObject, _ := workspace.NewProviderGitCommit(
		mergeCommit, fixTree, []workspace.GitObjectID{opened.base, fixHead},
	)
	git := &providerCompletionGit{
		remoteBranch: fixHead, remoteBase: mergeCommit,
		commits: map[string]workspace.ProviderGitCommit{
			fixHead.String(): headCommit, mergeCommit.String(): mergeObject,
		},
		ancestors: map[string]bool{
			opened.base.String() + "\x00" + mergeCommit.String(): true,
			fixHead.String() + "\x00" + mergeCommit.String():     true,
		},
	}
	receipt, _, err := workspace.VerifyAndRecordProviderCompletion(
		context.Background(), opened.harness.journal, opened.harness.definition, opened.broker, git,
		workspace.ProviderCompletionRequest{
			AttemptID: updatedAttempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T16:03:00Z"),
		},
	)
	if err != nil || receipt.BaseHead() != opened.base || receipt.Head() != fixHead {
		t.Fatalf("review-fix completion receipt = %#v err=%v", receipt, err)
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
	other, err := workspace.NewProviderPullRequestObservation(
		scenario.harness.definition.Workspace().Provider().Kind(),
		scenario.harness.definition.Workspace().Repository(),
		172,
		scenario.head,
		workspace.DigestBytes([]byte("foreign-pull-request-identity")),
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongMerge := scenario.mergeIntent(t, other.PullRequest())
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
				Reviews: []workspace.ProviderReviewState{review}, Lifecycle: workspace.ProviderPullRequestClosed, Merged: true,
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
	return providerUnmergedStateWithLifecycle(
		t, scenario, pull, baseRef, branch, head, tree, remoteHead, base,
		checkConclusion, reviewConclusion, workspace.ProviderPullRequestOpen,
	)
}

func providerUnmergedStateWithLifecycle(
	t *testing.T,
	scenario *providerOpenScenario,
	pull workspace.PullRequestIdentity,
	baseRef, branch string,
	head, tree, remoteHead, base workspace.GitObjectID,
	checkConclusion workspace.ProviderCheckConclusion,
	reviewConclusion workspace.ProviderReviewConclusion,
	lifecycle workspace.ProviderPullRequestLifecycle,
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
		Lifecycle: lifecycle, RequestMarker: "provider-merge-preflight",
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
