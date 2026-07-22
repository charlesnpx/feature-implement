package workspace_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type providerLifecycleAdapter struct {
	pushResult        workspace.ProviderPushAdapterResult
	pushErr           error
	openResult        workspace.ProviderOpenPullRequestAdapterResult
	openErr           error
	mergeResult       workspace.ProviderMergeAdapterResult
	mergeErr          error
	reconciliation    workspace.ProviderReconciliationObservation
	reconciliationErr error
	pullRequestState  workspace.ProviderPullRequestState
	pullRequestErr    error
	pushCalls         int
	openCalls         int
	mergeCalls        int
	queryIntentCalls  int
	queryPRCalls      int
	lastPush          workspace.ProviderPushRequest
	lastMerge         workspace.ProviderMergeRequest
	pushStarted       chan struct{}
	pushRelease       <-chan struct{}
}

type leaseEnforcingProviderAdapter struct {
	providerLifecycleAdapter
	remoteAbsent bool
	remoteHead   workspace.GitObjectID
	pushEffects  int
}

func (adapter *leaseEnforcingProviderAdapter) Push(
	_ context.Context,
	request workspace.ProviderPushRequest,
) (workspace.ProviderPushAdapterResult, error) {
	adapter.pushCalls++
	expected, hasExpected := request.ExpectedRemoteHead()
	leaseMatches := adapter.remoteAbsent && request.ExpectsRemoteAbsent() && !hasExpected
	if !adapter.remoteAbsent {
		leaseMatches = !request.ExpectsRemoteAbsent() && hasExpected && expected == adapter.remoteHead
	}
	if !leaseMatches {
		failure, _ := workspace.NewProviderAdapterFailure(
			workspace.ProviderAdapterFailedBeforeEffect,
			"atomic-push-lease-mismatch",
			errors.New("provider branch changed before push"),
		)
		return workspace.ProviderPushAdapterResult{}, failure
	}
	adapter.pushEffects++
	return adapter.pushResult, adapter.pushErr
}

func (adapter *providerLifecycleAdapter) Push(
	_ context.Context,
	request workspace.ProviderPushRequest,
) (workspace.ProviderPushAdapterResult, error) {
	adapter.pushCalls++
	adapter.lastPush = request
	if adapter.pushStarted != nil {
		close(adapter.pushStarted)
	}
	if adapter.pushRelease != nil {
		<-adapter.pushRelease
	}
	return adapter.pushResult, adapter.pushErr
}

func (adapter *providerLifecycleAdapter) OpenPullRequest(
	_ context.Context,
	_ workspace.ProviderOpenPullRequestRequest,
) (workspace.ProviderOpenPullRequestAdapterResult, error) {
	adapter.openCalls++
	return adapter.openResult, adapter.openErr
}

func (adapter *providerLifecycleAdapter) Merge(
	_ context.Context,
	request workspace.ProviderMergeRequest,
) (workspace.ProviderMergeAdapterResult, error) {
	adapter.mergeCalls++
	adapter.lastMerge = request
	return adapter.mergeResult, adapter.mergeErr
}

func (adapter *providerLifecycleAdapter) QueryIntent(
	_ context.Context,
	_ workspace.ProviderIntentQuery,
) (workspace.ProviderReconciliationObservation, error) {
	adapter.queryIntentCalls++
	return adapter.reconciliation, adapter.reconciliationErr
}

func (adapter *providerLifecycleAdapter) QueryPullRequest(
	_ context.Context,
	_ workspace.ProviderPullRequestQuery,
) (workspace.ProviderPullRequestState, error) {
	adapter.queryPRCalls++
	return adapter.pullRequestState, adapter.pullRequestErr
}

type providerCompletionGit struct {
	remoteBranch workspace.GitObjectID
	remoteBase   workspace.GitObjectID
	commits      map[string]workspace.ProviderGitCommit
	ancestors    map[string]bool
}

func (git *providerCompletionGit) InspectRemoteTopology(
	_ context.Context,
	_, _, _, _ string,
	head, merge, base workspace.GitObjectID,
) (workspace.ProviderCompletionGitInspection, error) {
	headCommit, exists := git.commits[head.String()]
	if !exists {
		return workspace.ProviderCompletionGitInspection{}, errors.New("head commit not found")
	}
	mergeCommit, exists := git.commits[merge.String()]
	if !exists {
		return workspace.ProviderCompletionGitInspection{}, errors.New("merge commit not found")
	}
	return workspace.NewProviderCompletionGitInspection(
		git.remoteBranch, git.remoteBase, headCommit, mergeCommit,
		git.ancestors[base.String()+"\x00"+merge.String()],
		git.ancestors[head.String()+"\x00"+merge.String()],
	)
}

func (git *providerCompletionGit) InspectRemoteBranch(
	context.Context,
	string,
	string,
	string,
) (workspace.GitObjectID, error) {
	return git.remoteBranch, nil
}

func (git *providerCompletionGit) InspectRemoteBase(
	context.Context,
	string,
	string,
	string,
) (workspace.GitObjectID, error) {
	return git.remoteBase, nil
}

func (git *providerCompletionGit) InspectCommit(
	_ context.Context,
	_ string,
	object workspace.GitObjectID,
) (workspace.ProviderGitCommit, error) {
	commit, exists := git.commits[object.String()]
	if !exists {
		return workspace.ProviderGitCommit{}, errors.New("commit not found")
	}
	return commit, nil
}

func (git *providerCompletionGit) IsAncestor(
	_ context.Context,
	_ string,
	ancestor, descendant workspace.GitObjectID,
) (bool, error) {
	return git.ancestors[ancestor.String()+"\x00"+descendant.String()], nil
}

func TestProviderLifecycleDispatchesThroughSingleUseBrokerAndRecordsCanonicalCompletion(t *testing.T) {
	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T10:01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T10:02:00Z")
	if attempt.Phase() != workspace.AttemptActive {
		t.Fatalf("attempt phase = %s", attempt.Phase())
	}
	base := attempt.Base()
	head := attempt.VerifiedHead()
	tree := mustGitObject(t, 'b')
	mergeCommit := mustGitObject(t, 'c')
	frontier, err := workspace.NewAuthorizationFrontier(base, head)
	if err != nil {
		t.Fatal(err)
	}
	grant := recordProviderLifecycleGrant(
		t, harness, attempt, frontier,
		[]workspace.StandingAuthorizationAction{
			workspace.StandingAuthorizationPush,
			workspace.StandingAuthorizationOpenPullRequest,
			workspace.StandingAuthorizationMerge,
		},
		"2026-07-21T10:03:00Z",
	)
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T10:04:00Z")}
	evaluator, _ := workspace.NewAuthorizationEvaluator(clock)
	openIntent, err := workspace.NewProviderOpenPullRequestIntent(workspace.ProviderOpenPullRequestIntentOptions{
		Scope:  providerIntentScope(harness, attempt, frontier, workspace.PullRequestIdentity{}),
		Branch: attempt.Branch(), BaseRef: harness.definition.Workspace().BaseRef(),
		Head: head, Tree: tree, Title: "Provider lifecycle", Body: "Typed provider dispatch.",
	})
	if err != nil {
		t.Fatal(err)
	}
	reserved, _, err := workspace.ReserveProviderIntent(
		harness.journal, harness.definition, evaluator,
		workspace.ReserveProviderIntentRequest{Intent: openIntent, OccurredAt: mustTime(t, "2026-07-21T10:04:01Z")},
	)
	if err != nil || reserved.Status() != workspace.ProviderIntentReserved {
		t.Fatalf("reserve open PR = %#v, %v", reserved, err)
	}
	openAdapterResult, _ := workspace.NewProviderOpenPullRequestAdapterResult("request-open-1", 71, head)
	identitySeed, _ := workspace.NewProviderPullRequestObservation(
		harness.definition.Workspace().Provider().Kind(), harness.definition.Workspace().Repository(),
		71, head, workspace.DigestBytes([]byte("open-postflight-identity")),
	)
	adapter := &providerLifecycleAdapter{
		openResult: openAdapterResult,
		pullRequestState: providerPRState(
			t, harness, attempt, identitySeed.PullRequest(), head, tree, base, workspace.GitObjectID{}, false,
		),
	}
	broker, err := workspace.NewProviderBroker(harness.definition.Workspace().Provider(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = mustTime(t, "2026-07-21T10:05:00Z")
	openTicket, err := workspace.AuthorizeProviderIntentDispatch(
		harness.journal, harness.definition, evaluator, broker, openIntent.IntentID(),
	)
	if err != nil || openTicket.CapabilityDigest().IsZero() {
		t.Fatalf("authorize open PR = %#v, %v", openTicket, err)
	}
	state, _, err := workspace.ReadAuthorizationEvaluationSnapshot(harness.journal, harness.definition)
	if err != nil || len(state.OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("dispatch obligation = %#v, %v", state.OutstandingReconciliationObligations(), err)
	}
	openExecution, err := workspace.ExecuteProviderIntent(
		context.Background(), harness.journal, harness.definition, broker, openTicket,
		mustTime(t, "2026-07-21T10:05:01Z"),
	)
	if err != nil || openExecution.Result().Status() != workspace.ProviderIntentSucceeded || adapter.openCalls != 1 {
		t.Fatalf("execute open PR = %#v calls=%d, %v", openExecution, adapter.openCalls, err)
	}
	if _, err := workspace.ExecuteProviderIntent(
		context.Background(), harness.journal, harness.definition, broker, openTicket,
		mustTime(t, "2026-07-21T10:05:02Z"),
	); err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("reused dispatch ticket error = %v", err)
	}
	state, _, err = workspace.ReadAuthorizationEvaluationSnapshot(harness.journal, harness.definition)
	if err != nil || len(state.OutstandingReconciliationObligations()) != 0 {
		t.Fatalf("settled open PR obligations = %#v, %v", state.OutstandingReconciliationObligations(), err)
	}
	pullRequest, ok := openExecution.Result().PullRequest()
	if !ok {
		t.Fatal("successful open PR did not return provider-derived identity")
	}
	wrongTopology, err := workspace.NewProviderPullRequestState(workspace.ProviderPullRequestStateOptions{
		Repository: harness.definition.Workspace().Repository(), PullRequest: pullRequest,
		BaseRef: "feature/wrong-base", Branch: attempt.Branch(), BaseHeadBeforeMerge: base,
		Head: head, HeadTree: tree, RemoteBranchHead: head, RequestMarker: "wrong-open-authorization-topology",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter.pullRequestState = wrongTopology
	if _, _, err := workspace.RecordProviderPullRequestAuthorization(
		context.Background(), harness.journal, harness.definition, broker, openIntent.IntentID(),
		mustTime(t, "2026-07-21T10:05:30Z"),
	); err == nil || !strings.Contains(err.Error(), "authorization topology") {
		t.Fatalf("wrong open-PR authorization topology error = %v", err)
	}
	adapter.pullRequestState = providerPRState(
		t, harness, attempt, pullRequest, head, tree, base,
		workspace.GitObjectID{}, false,
	)
	derived, _, err := workspace.RecordProviderPullRequestAuthorization(
		context.Background(), harness.journal, harness.definition, broker, openIntent.IntentID(),
		mustTime(t, "2026-07-21T10:06:00Z"),
	)
	if err != nil {
		t.Fatalf("record provider PR authorization: %v", err)
	}
	if parent, ok := derived.ParentGrantID(); !ok || parent != grant.GrantID() {
		t.Fatalf("derived parent = %s, %v; want %s", parent, ok, grant.GrantID())
	}
	mergeIntent, err := workspace.NewProviderMergeIntent(workspace.ProviderMergeIntentOptions{
		Scope:  providerIntentScope(harness, attempt, frontier, pullRequest),
		Branch: attempt.Branch(), BaseRef: harness.definition.Workspace().BaseRef(),
		IntegrationBaseHead: attempt.Base(), Head: head, Tree: tree, Strategy: workspace.ProviderMergeCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = mustTime(t, "2026-07-21T10:07:00Z")
	if _, _, err := workspace.ReserveProviderIntent(
		harness.journal, harness.definition, evaluator,
		workspace.ReserveProviderIntentRequest{Intent: mergeIntent, OccurredAt: mustTime(t, "2026-07-21T10:07:01Z")},
	); err != nil {
		t.Fatalf("reserve merge: %v", err)
	}
	if _, _, err := workspace.RecordProviderMergePreflight(
		context.Background(), harness.journal, harness.definition, broker, mergeIntent.IntentID(),
		mustTime(t, "2026-07-21T10:07:02Z"),
	); err != nil {
		t.Fatalf("record merge preflight: %v", err)
	}
	clock.now = mustTime(t, "2026-07-21T10:08:00Z")
	mergeTicket, err := workspace.AuthorizeProviderIntentDispatch(
		harness.journal, harness.definition, evaluator, broker, mergeIntent.IntentID(),
	)
	if err != nil {
		t.Fatalf("authorize merge: %v", err)
	}
	mergeAdapterResult, _ := workspace.NewProviderMergeAdapterResult("request-merge-1", mergeCommit, mergeCommit)
	adapter.mergeResult = mergeAdapterResult
	mergeExecution, err := workspace.ExecuteProviderIntent(
		context.Background(), harness.journal, harness.definition, broker, mergeTicket,
		mustTime(t, "2026-07-21T10:08:01Z"),
	)
	if err != nil || mergeExecution.Result().Status() != workspace.ProviderIntentSucceeded ||
		adapter.queryPRCalls < 2 || adapter.mergeCalls != 1 {
		t.Fatalf("execute merge = %#v query=%d merge=%d, %v", mergeExecution, adapter.queryPRCalls, adapter.mergeCalls, err)
	}
	adapter.pullRequestState = providerPRState(t, harness, attempt, pullRequest, head, tree, base, mergeCommit, true)
	headCommit, _ := workspace.NewProviderGitCommit(head, tree, []workspace.GitObjectID{base})
	mergeObject, _ := workspace.NewProviderGitCommit(mergeCommit, tree, []workspace.GitObjectID{base, head})
	git := &providerCompletionGit{
		remoteBranch: head, remoteBase: mergeCommit,
		commits: map[string]workspace.ProviderGitCommit{
			head.String(): headCommit, mergeCommit.String(): mergeObject,
		},
		ancestors: map[string]bool{
			base.String() + "\x00" + mergeCommit.String(): true,
			head.String() + "\x00" + mergeCommit.String(): true,
		},
	}
	receipt, record, err := workspace.VerifyAndRecordProviderCompletion(
		context.Background(), harness.journal, harness.definition, broker, git,
		workspace.ProviderCompletionRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:09:00Z"),
		},
	)
	if err != nil || receipt.Digest().IsZero() || record.EventType() != workspace.JournalEventProviderCompletionVerified {
		t.Fatalf("verify completion receipt = %#v record=%s, %v", receipt, record.EventType(), err)
	}
	encoded, err := receipt.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := workspace.DecodeProviderCompletionReceipt(encoded)
	if err != nil || decoded.Digest() != receipt.Digest() || decoded.MergeCommit() != mergeCommit {
		t.Fatalf("completion receipt round trip = %#v, %v", decoded, err)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	providerProjection, err := workspace.RebuildProviderRuntime(snapshot, harness.definition)
	if err != nil || len(providerProjection.CompletionReceipts()) != 1 {
		t.Fatalf("provider replay = %#v, %v", providerProjection, err)
	}
	if _, err := workspace.VerifyProviderRuntimeConformance(snapshot, harness.definition); err != nil {
		t.Fatalf("provider replay conformance: %v", err)
	}
}

func TestProviderAmbiguityBlocksDispatchUntilTypedQueryReconciles(t *testing.T) {
	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T11:01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T11:02:00Z")
	frontier, _ := workspace.NewAuthorizationFrontier(attempt.Base(), attempt.VerifiedHead())
	recordProviderLifecycleGrant(
		t, harness, attempt, frontier,
		[]workspace.StandingAuthorizationAction{
			workspace.StandingAuthorizationPush,
			workspace.StandingAuthorizationOpenPullRequest,
		},
		"2026-07-21T11:03:00Z",
	)
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T11:03:00Z")}
	evaluator, _ := workspace.NewAuthorizationEvaluator(clock)
	intent := providerPushIntent(t, harness, attempt, frontier)
	if _, _, err := workspace.ReserveProviderIntent(
		harness.journal, harness.definition, evaluator,
		workspace.ReserveProviderIntentRequest{Intent: intent, OccurredAt: mustTime(t, "2026-07-21T11:03:01Z")},
	); err != nil {
		t.Fatal(err)
	}
	clock.now = mustTime(t, "2026-07-21T11:04:00Z")
	adapter := &providerLifecycleAdapter{pushErr: errors.New("connection lost after request")}
	broker, _ := workspace.NewProviderBroker(harness.definition.Workspace().Provider(), adapter)
	ticket, err := workspace.AuthorizeProviderIntentDispatch(
		harness.journal, harness.definition, evaluator, broker, intent.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := workspace.ExecuteProviderIntent(
		context.Background(), harness.journal, harness.definition, broker, ticket,
		mustTime(t, "2026-07-21T11:04:01Z"),
	)
	if err == nil || execution.Result().Status() != workspace.ProviderIntentAmbiguous {
		t.Fatalf("ambiguous execution = %#v, %v", execution, err)
	}
	state, _, _ := workspace.ReadAuthorizationEvaluationSnapshot(harness.journal, harness.definition)
	if len(state.OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("ambiguous obligations = %#v", state.OutstandingReconciliationObligations())
	}
	second, err := workspace.NewProviderOpenPullRequestIntent(workspace.ProviderOpenPullRequestIntentOptions{
		Scope:  providerIntentScope(harness, attempt, frontier, workspace.PullRequestIdentity{}),
		Branch: attempt.Branch(), BaseRef: harness.definition.Workspace().BaseRef(),
		Head: attempt.VerifiedHead(), Tree: mustGitObject(t, 'b'),
		Title: "Blocked by reconciliation", Body: "Must not dispatch while push is ambiguous.",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = mustTime(t, "2026-07-21T11:05:00Z")
	if _, _, reserveErr := workspace.ReserveProviderIntent(
		harness.journal, harness.definition, evaluator,
		workspace.ReserveProviderIntentRequest{Intent: second, OccurredAt: mustTime(t, "2026-07-21T11:05:01Z")},
	); reserveErr == nil || !strings.Contains(reserveErr.Error(), "awaiting reconciliation") {
		t.Fatalf("dispatch while ambiguous reserve error = %v", reserveErr)
	}
	observation, _ := workspace.NewProviderReconciliationObservation(workspace.ProviderReconciliationObservationOptions{
		Disposition: workspace.ProviderEffectApplied, RequestMarker: "query-push-1",
		RemoteHead: attempt.VerifiedHead(),
	})
	adapter.reconciliation = observation
	reconciled, err := workspace.ReconcileProviderIntent(
		context.Background(), harness.journal, harness.definition, broker, intent.IntentID(),
		mustTime(t, "2026-07-21T11:06:00Z"),
	)
	if err != nil || reconciled.Projection().Status() != workspace.ProviderIntentReconciled || adapter.queryIntentCalls != 1 {
		t.Fatalf("reconcile = %#v calls=%d, %v", reconciled, adapter.queryIntentCalls, err)
	}
	state, _, _ = workspace.ReadAuthorizationEvaluationSnapshot(harness.journal, harness.definition)
	if len(state.OutstandingReconciliationObligations()) != 0 {
		t.Fatalf("reconciled obligations = %#v", state.OutstandingReconciliationObligations())
	}
}

func TestProviderReconciledNotAppliedIntentCanBeReservedAndDispatchedAgain(t *testing.T) {
	scenario := newProviderPushScenario(t, "11")
	ticket := scenario.authorize(t, "11")
	scenario.adapter.pushErr = errors.New("provider outcome unknown")
	first, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.broker, ticket, mustTime(t, "2026-07-21T11:05:01Z"),
	)
	if err == nil || first.Result().Status() != workspace.ProviderIntentAmbiguous {
		t.Fatalf("ambiguous first dispatch = %#v err=%v", first, err)
	}
	scenario.adapter.reconciliation, _ = workspace.NewProviderReconciliationObservation(
		workspace.ProviderReconciliationObservationOptions{
			Disposition: workspace.ProviderEffectNotApplied, RequestMarker: "query-not-applied",
		},
	)
	resolved, err := workspace.ReconcileProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.broker, scenario.intent.IntentID(), mustTime(t, "2026-07-21T11:06:00Z"),
	)
	if err != nil || resolved.Projection().Status() != workspace.ProviderIntentReconciled ||
		resolved.Reconciliation().EffectApplied() {
		t.Fatalf("not-applied reconciliation = %#v err=%v", resolved, err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T11:07:00Z")
	retried, record, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{
			Intent: scenario.intent, OccurredAt: mustTime(t, "2026-07-21T11:07:01Z"),
		},
	)
	if err != nil || retried.Status() != workspace.ProviderIntentReserved ||
		record.EventType() != workspace.JournalEventProviderIntentReserved {
		t.Fatalf("reconciled retry reservation = %#v record=%s err=%v", retried, record.EventType(), err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T11:08:00Z")
	ticket, err = workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, scenario.broker,
		scenario.intent.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.adapter.pushErr = nil
	scenario.adapter.pushResult, _ = workspace.NewProviderPushAdapterResult(
		"retry-push", scenario.attempt.VerifiedHead(),
	)
	second, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.broker, ticket, mustTime(t, "2026-07-21T11:08:01Z"),
	)
	if err != nil || second.Result().Status() != workspace.ProviderIntentSucceeded || scenario.adapter.pushCalls != 2 {
		t.Fatalf("retried provider push = %#v calls=%d err=%v", second, scenario.adapter.pushCalls, err)
	}
}

func providerIntentScope(
	harness attemptHarness,
	attempt workspace.RuntimeAttemptProjection,
	frontier workspace.AuthorizationFrontier,
	pullRequest workspace.PullRequestIdentity,
) workspace.ProviderIntentScopeOptions {
	return workspace.ProviderIntentScopeOptions{
		WorkspaceID: harness.definition.Workspace().ID(), Generation: harness.definition.Generation(),
		AttemptID: attempt.AttemptID(), MergeUnit: attempt.MergeUnit(),
		Repository: harness.definition.Workspace().Repository(), Remote: harness.definition.Workspace().Remote(),
		SerialSegment: attempt.SerialSegment(), Frontier: frontier, PullRequest: pullRequest, Epoch: 1,
	}
}

func recordProviderLifecycleGrant(
	t *testing.T,
	harness attemptHarness,
	attempt workspace.RuntimeAttemptProjection,
	frontier workspace.AuthorizationFrontier,
	actions []workspace.StandingAuthorizationAction,
	recordedAt string,
) workspace.StandingGrant {
	t.Helper()
	scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: harness.definition.Workspace().ID(), Repository: harness.definition.Workspace().Repository(),
		Remote: harness.definition.Workspace().Remote(), Generation: harness.definition.Generation(),
		SerialSegment: attempt.SerialSegment(), Frontier: frontier, Actions: actions,
		ExpiresAt: mustTime(t, "2026-07-21T20:00:00Z"), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := workspace.StandingGrantControlPlaneBinding(scope)
	grant, _, err := workspace.RecordStandingGrant(
		context.Background(), harness.journal, harness.definition,
		&boundaryVerifier{expectedRequest: scope.Digest()}, scope,
		controlPlaneReceipt(t, binding, "provider-lifecycle-grant"),
		mustTime(t, recordedAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func providerPushIntent(
	t *testing.T,
	harness attemptHarness,
	attempt workspace.RuntimeAttemptProjection,
	frontier workspace.AuthorizationFrontier,
) workspace.ProviderIntent {
	t.Helper()
	intent, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
		Scope:  providerIntentScope(harness, attempt, frontier, workspace.PullRequestIdentity{}),
		Branch: attempt.Branch(), ExpectRemoteAbsent: true, Head: attempt.VerifiedHead(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func providerPRState(
	t *testing.T,
	harness attemptHarness,
	attempt workspace.RuntimeAttemptProjection,
	pullRequest workspace.PullRequestIdentity,
	head, tree, base, mergeCommit workspace.GitObjectID,
	merged bool,
) workspace.ProviderPullRequestState {
	t.Helper()
	check, _ := workspace.NewProviderCheckState(
		workspace.MustID("ci"), true, workspace.ProviderCheckPassed, workspace.DigestBytes([]byte("ci-evidence")),
	)
	review, _ := workspace.NewProviderReviewState(
		workspace.MustID("owners"), true, workspace.ProviderReviewApproved,
		workspace.DigestBytes([]byte("owner-review-evidence")),
	)
	options := workspace.ProviderPullRequestStateOptions{
		Repository: harness.definition.Workspace().Repository(), PullRequest: pullRequest,
		BaseRef: harness.definition.Workspace().BaseRef(), Branch: attempt.Branch(),
		Head: head, HeadTree: tree, RemoteBranchHead: head, BaseHeadBeforeMerge: base,
		Checks: []workspace.ProviderCheckState{check}, Reviews: []workspace.ProviderReviewState{review},
		Merged: merged, RequestMarker: "query-pr-state",
	}
	if merged {
		options.MergeStrategy = workspace.ProviderMergeCommit
		options.MergeCommit = mergeCommit
		options.FinalBaseHead = mergeCommit
	}
	state, err := workspace.NewProviderPullRequestState(options)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestProviderIntentSurfaceHasNoDeleteOrExecutableCommandRepresentation(t *testing.T) {
	for _, kind := range []workspace.ProviderIntentKind{
		workspace.ProviderIntentPush, workspace.ProviderIntentOpenPullRequest, workspace.ProviderIntentMerge,
	} {
		if strings.Contains(string(kind), "delete") || strings.Contains(string(kind), "close") {
			t.Fatalf("unsupported provider action is reachable: %s", kind)
		}
	}
	if _, err := workspace.NewProviderAdapterFailure(
		workspace.ProviderAdapterFailureKind("remote_delete"), "forbidden", errors.New("forbidden"),
	); err == nil {
		t.Fatal("provider adapter accepted an open-ended remote-delete failure/action kind")
	}
}

func TestProviderBrokerCancellationBeforeInvocationRecordsFailedBeforeEffect(t *testing.T) {
	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T12:01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T12:02:00Z")
	frontier, _ := workspace.NewAuthorizationFrontier(attempt.Base(), attempt.VerifiedHead())
	recordProviderLifecycleGrant(
		t, harness, attempt, frontier,
		[]workspace.StandingAuthorizationAction{workspace.StandingAuthorizationPush},
		"2026-07-21T12:03:00Z",
	)
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T12:03:00Z")}
	evaluator, _ := workspace.NewAuthorizationEvaluator(clock)
	intent := providerPushIntent(t, harness, attempt, frontier)
	_, _, _ = workspace.ReserveProviderIntent(
		harness.journal, harness.definition, evaluator,
		workspace.ReserveProviderIntentRequest{Intent: intent, OccurredAt: mustTime(t, "2026-07-21T12:03:01Z")},
	)
	clock.now = mustTime(t, "2026-07-21T12:04:00Z")
	adapter := &providerLifecycleAdapter{}
	broker, _ := workspace.NewProviderBroker(harness.definition.Workspace().Provider(), adapter)
	ticket, _ := workspace.AuthorizeProviderIntentDispatch(
		harness.journal, harness.definition, evaluator, broker, intent.IntentID(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	execution, err := workspace.ExecuteProviderIntent(
		ctx, harness.journal, harness.definition, broker, ticket, mustTime(t, "2026-07-21T12:04:01Z"),
	)
	if err == nil || execution.Result().Status() != workspace.ProviderIntentFailedBeforeEffect || adapter.pushCalls != 0 {
		t.Fatalf("cancelled dispatch = %#v calls=%d, %v", execution, adapter.pushCalls, err)
	}
	state, _, _ := workspace.ReadAuthorizationEvaluationSnapshot(harness.journal, harness.definition)
	if len(state.OutstandingReconciliationObligations()) != 0 {
		t.Fatalf("failed-before-effect retained obligation: %#v", state.OutstandingReconciliationObligations())
	}
}

func TestProviderAtomicPushLeaseRejectsRemoteDriftBeforeWrite(t *testing.T) {
	scenario := newProviderPushScenario(t, "12")
	result, _ := workspace.NewProviderPushAdapterResult("unreachable-push", scenario.attempt.VerifiedHead())
	adapter := &leaseEnforcingProviderAdapter{
		providerLifecycleAdapter: providerLifecycleAdapter{pushResult: result},
		remoteHead:               mustGitObject(t, 'd'),
	}
	broker, err := workspace.NewProviderBroker(scenario.harness.definition.Workspace().Provider(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T12:05:00Z")
	ticket, err := workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, broker,
		scenario.intent.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, broker, ticket,
		mustTime(t, "2026-07-21T12:05:01Z"),
	)
	if err == nil || execution.Result().Status() != workspace.ProviderIntentFailedBeforeEffect ||
		adapter.pushCalls != 1 || adapter.pushEffects != 0 {
		t.Fatalf(
			"drifted atomic lease = %#v calls=%d effects=%d err=%v",
			execution, adapter.pushCalls, adapter.pushEffects, err,
		)
	}
}

func TestProviderAdapterTypedFailureClassification(t *testing.T) {
	failure, err := workspace.NewProviderAdapterFailure(
		workspace.ProviderAdapterFailedAfterEffect, "provider-timeout-after-send", errors.New("timeout"),
	)
	if err != nil || failure.Kind() != workspace.ProviderAdapterFailedAfterEffect || failure.RequestMarker() == "" {
		t.Fatalf("typed failure = %#v, %v", failure, err)
	}
	if !errors.Is(failure, failure.Unwrap()) {
		t.Fatal("typed provider failure does not retain cause")
	}
}

var _ interface {
	Now() time.Time
} = (*authorizationTestClock)(nil)
