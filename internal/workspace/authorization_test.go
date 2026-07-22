package workspace_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type authorizationFixture struct {
	workspaceID workspace.ID
	repository  workspace.RepositoryIdentity
	remote      string
	generation  workspace.Digest
	segment     workspace.ID
	frontier    workspace.AuthorizationFrontier
	state       workspace.AuthorizationState
}

type authorizationTestClock struct{ now time.Time }

func (clock *authorizationTestClock) Now() time.Time { return clock.now }

func TestStandingAuthorizationCoversPrePRAndRejectsCallerManufacturedPRGrants(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	scope := authorizationScope(
		t, fixture, fixture.frontier, workspace.PullRequestIdentity{}, "2026-07-21T20:00:00Z", 1,
		workspace.StandingAuthorizationPush,
		workspace.StandingAuthorizationOpenPullRequest,
		workspace.StandingAuthorizationMerge,
	)
	grant := verifiedTestStandingGrant(t, scope, "initial-grant")
	event, err := workspace.NewAuthorizationGrantRecorded(grant)
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.ReduceAuthorization(fixture.state, event)
	if err != nil {
		t.Fatal(err)
	}

	push := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationPush, workspace.PullRequestIdentity{}, 1)
	now := mustTime(t, "2026-07-21T18:00:00Z")
	evaluator := authorizationEvaluator(t, now)
	snapshot := authorizationSnapshot(t, "pre-pr")
	planned, err := evaluator.PlanAuthorization(state, push, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.AuthorizeImmediatelyBeforeDispatch(state, push, snapshot, planned); err == nil {
		t.Fatal("planning capability skipped intent reservation and queue entry")
	}
	reserved, err := evaluator.ReserveAuthorizationIntent(state, push, snapshot, planned)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := evaluator.EnterAuthorizationQueue(state, push, snapshot, reserved)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := evaluator.AuthorizeImmediatelyBeforeDispatch(state, push, snapshot, queued)
	if err != nil {
		t.Fatal(err)
	}
	for checkpoint, capability := range map[workspace.AuthorizationCheckpoint]workspace.AuthorizationCapability{
		workspace.AuthorizationAtPlanning:          planned,
		workspace.AuthorizationAtIntentReservation: reserved,
		workspace.AuthorizationAtQueueEntry:        queued,
		workspace.AuthorizationBeforeDispatch:      dispatch,
	} {
		if capability.Checkpoint() != checkpoint || capability.GrantID() != grant.GrantID() {
			t.Fatalf("checkpoint %s capability = %#v", checkpoint, capability)
		}
	}
	openPR := authorizationRequest(
		t, fixture, fixture.frontier, workspace.StandingAuthorizationOpenPullRequest, workspace.PullRequestIdentity{}, 1,
	)
	if _, err := evaluator.AuthorizeImmediatelyBeforeDispatch(state, openPR, snapshot, queued); err == nil {
		t.Fatal("queue capability authorized a different request")
	}
	if _, err := authorizationBeforeDispatch(state, openPR, now); err != nil {
		t.Fatal(err)
	}
	mergeBeforePR := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationMerge, workspace.PullRequestIdentity{}, 1)
	if _, err := authorizationBeforeDispatch(state, mergeBeforePR, now); err == nil {
		t.Fatal("pre-PR grant authorized merge without derived PR identity")
	}

	observation, err := workspace.NewProviderPullRequestObservation(
		workspace.MustID("github"), fixture.repository, 71, fixture.frontier.Head(),
		workspace.DigestBytes([]byte("provider-result-71")),
	)
	if err != nil {
		t.Fatal(err)
	}
	pr := observation.PullRequest()
	callerManufacturedScope := authorizationScope(
		t, fixture, fixture.frontier, pr, "2026-07-21T20:00:00Z", 1,
		workspace.StandingAuthorizationPush, workspace.StandingAuthorizationMerge,
	)
	manufacturedBinding, _ := workspace.StandingGrantControlPlaneBinding(callerManufacturedScope)
	if _, err := workspace.VerifyStandingGrant(
		context.Background(), &boundaryVerifier{expectedRequest: callerManufacturedScope.Digest()},
		callerManufacturedScope, controlPlaneReceipt(t, manufacturedBinding, "caller-manufactured-pr"),
	); err == nil || !strings.Contains(err.Error(), "provider-derived") {
		t.Fatalf("caller-manufactured PR grant error = %v", err)
	}

	actions := scope.Actions()
	actions[0] = "invalid"
	if scope.Actions()[0] == "invalid" {
		t.Fatal("standing grant action slice aliases caller data")
	}
}

func TestAuthorizationBlocksGatesReconciliationDriftAmbiguitySegmentsAndStaleEpochs(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	scope := authorizationScope(
		t, fixture, fixture.frontier, workspace.PullRequestIdentity{}, "2026-07-21T20:00:00Z", 1,
		workspace.StandingAuthorizationPush,
	)
	grant := verifiedTestStandingGrant(t, scope, "grant-one")
	grantEvent, _ := workspace.NewAuthorizationGrantRecorded(grant)
	baseState, err := workspace.ReduceAuthorization(fixture.state, grantEvent)
	if err != nil {
		t.Fatal(err)
	}
	request := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationPush, workspace.PullRequestIdentity{}, 1)
	now := mustTime(t, "2026-07-21T18:00:00Z")
	queued, err := authorizationThroughQueue(baseState, request, now)
	if err != nil {
		t.Fatal(err)
	}

	blockers := []struct {
		name   string
		safety workspace.AuthorizationSafetyState
		want   string
	}{
		{name: "gate", safety: workspace.NewAuthorizationSafetyState(true, false, false, false), want: "gate"},
		{name: "reconciliation", safety: workspace.NewAuthorizationSafetyState(false, true, false, false), want: "reconciliation"},
		{name: "drift", safety: workspace.NewAuthorizationSafetyState(false, false, true, false), want: "drift"},
		{name: "ambiguity", safety: workspace.NewAuthorizationSafetyState(false, false, false, true), want: "ambiguous"},
	}
	for _, test := range blockers {
		t.Run(test.name, func(t *testing.T) {
			state, err := workspace.ReduceAuthorization(baseState, workspace.NewAuthorizationSafetyChanged(test.safety))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := authorizationEvaluatorAt(now).AuthorizeImmediatelyBeforeDispatch(
				state, request, authorizationSnapshotValue(), queued,
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s blocker error = %v", test.name, err)
			}
		})
	}

	segmentEvent, _ := workspace.NewAuthorizationSegmentCompleted(fixture.segment)
	segmentState, err := workspace.ReduceAuthorization(baseState, segmentEvent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizationEvaluatorAt(now).AuthorizeImmediatelyBeforeDispatch(
		segmentState, request, authorizationSnapshotValue(), queued,
	); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("segment completion error = %v", err)
	}

	secondScope := authorizationScope(
		t, fixture, fixture.frontier, workspace.PullRequestIdentity{}, "2026-07-21T19:30:00Z", 1,
		workspace.StandingAuthorizationPush,
	)
	secondGrant := verifiedTestStandingGrant(t, secondScope, "grant-two")
	secondEvent, _ := workspace.NewAuthorizationGrantRecorded(secondGrant)
	ambiguousState, err := workspace.ReduceAuthorization(baseState, secondEvent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizationEvaluatorAt(now).AuthorizeImmediatelyBeforeDispatch(
		ambiguousState, request, authorizationSnapshotValue(), queued,
	); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("overlapping grant ambiguity error = %v", err)
	}

	if _, err := authorizationEvaluatorAt(scope.ExpiresAt()).AuthorizeImmediatelyBeforeDispatch(
		baseState, request, authorizationSnapshotValue(), queued,
	); err == nil {
		t.Fatal("expired standing grant was authorized")
	}
	staleRequest := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationPush, workspace.PullRequestIdentity{}, 2)
	if _, err := authorizationEvaluatorAt(now).PlanAuthorization(
		baseState, staleRequest, authorizationSnapshotValue(),
	); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale epoch request error = %v", err)
	}
}

func TestAuthorizationEvaluatorUsesProtectedClockAtEveryCheckpoint(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	scope := authorizationScope(
		t, fixture, fixture.frontier, workspace.PullRequestIdentity{}, "2026-07-21T20:00:00Z", 1,
		workspace.StandingAuthorizationPush,
	)
	grant := verifiedTestStandingGrant(t, scope, "trusted-clock-grant")
	event, _ := workspace.NewAuthorizationGrantRecorded(grant)
	state, err := workspace.ReduceAuthorization(fixture.state, event)
	if err != nil {
		t.Fatal(err)
	}
	request := authorizationRequest(
		t, fixture, fixture.frontier, workspace.StandingAuthorizationPush, workspace.PullRequestIdentity{}, 1,
	)
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T18:00:00Z")}
	evaluator, err := workspace.NewAuthorizationEvaluator(clock)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := authorizationSnapshotValue()
	planned, err := evaluator.PlanAuthorization(state, request, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := evaluator.ReserveAuthorizationIntent(state, request, snapshot, planned)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := evaluator.EnterAuthorizationQueue(state, request, snapshot, reserved)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = scope.ExpiresAt()
	if _, err := evaluator.AuthorizeImmediatelyBeforeDispatch(state, request, snapshot, queued); err == nil ||
		!strings.Contains(err.Error(), "fresh matching") {
		t.Fatalf("expired grant passed protected-clock dispatch check: %v", err)
	}
}

func TestRevocationLinearizesBeforeDispatchAndPreservesPostDispatchObligation(t *testing.T) {
	fixture := newAuthorizationFixture(t)
	scope := authorizationScope(
		t, fixture, fixture.frontier, workspace.PullRequestIdentity{}, "2026-07-21T20:00:00Z", 1,
		workspace.StandingAuthorizationPush,
	)
	grant := verifiedTestStandingGrant(t, scope, "grant")
	grantEvent, _ := workspace.NewAuthorizationGrantRecorded(grant)
	state, err := workspace.ReduceAuthorization(fixture.state, grantEvent)
	if err != nil {
		t.Fatal(err)
	}
	request := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationPush, workspace.PullRequestIdentity{}, 1)
	capability, err := authorizationBeforeDispatch(state, request, mustTime(t, "2026-07-21T18:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	revocation := verifiedTestRevocation(t, fixture, grant.GrantID(), 2, "revoke")
	revokedEvent, _ := workspace.NewAuthorizationRevoked(revocation)
	revokedBeforeDispatch, err := workspace.ReduceAuthorization(state, revokedEvent)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, _ := workspace.NewAuthorizationEffectDispatched(workspace.MustID("push-effect"), capability)
	if _, err := workspace.ReduceAuthorization(revokedBeforeDispatch, dispatched); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("pre-dispatch revocation did not fence capability: %v", err)
	}

	postDispatch, err := workspace.ReduceAuthorization(state, dispatched)
	if err != nil {
		t.Fatal(err)
	}
	if len(postDispatch.OutstandingReconciliationObligations()) != 1 {
		t.Fatal("dispatched effect did not become a reconciliation obligation")
	}
	replayedDispatch, _ := workspace.NewAuthorizationEffectDispatched(workspace.MustID("second-push-effect"), capability)
	if _, err := workspace.ReduceAuthorization(postDispatch, replayedDispatch); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("pre-dispatch capability was reusable after dispatch: %v", err)
	}
	postDispatchRevoked, err := workspace.ReduceAuthorization(postDispatch, revokedEvent)
	if err != nil {
		t.Fatal(err)
	}
	if len(postDispatchRevoked.OutstandingReconciliationObligations()) != 1 || postDispatchRevoked.Epoch() != 2 {
		t.Fatal("post-dispatch revocation erased its reconciliation obligation")
	}
	if _, err := authorizationEvaluatorAt(mustTime(t, "2026-07-21T18:00:00Z")).PlanAuthorization(
		postDispatchRevoked, request, authorizationSnapshotValue(),
	); err == nil {
		t.Fatal("revoked state authorized another dispatch")
	}
}

func newAuthorizationFixture(t *testing.T) authorizationFixture {
	t.Helper()
	repository, err := workspace.NewRepositoryIdentity("https://github.com/example/repository.git")
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	if err != nil {
		t.Fatal(err)
	}
	fixture := authorizationFixture{
		workspaceID: workspace.MustID("workspace-one"), repository: repository, remote: "origin",
		generation: workspace.DigestBytes([]byte("generation-one")), segment: workspace.MustID("serial-one"),
		frontier: frontier,
	}
	fixture.state, err = workspace.NewAuthorizationState(
		fixture.workspaceID, fixture.repository, fixture.remote, fixture.generation, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func authorizationScope(
	t *testing.T,
	fixture authorizationFixture,
	frontier workspace.AuthorizationFrontier,
	pullRequest workspace.PullRequestIdentity,
	expiresAt string,
	epoch uint64,
	actions ...workspace.StandingAuthorizationAction,
) workspace.StandingGrantScope {
	t.Helper()
	scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: fixture.workspaceID, Repository: fixture.repository, Remote: fixture.remote,
		Generation: fixture.generation, SerialSegment: fixture.segment, Frontier: frontier,
		Actions: actions, ExpiresAt: mustTime(t, expiresAt), Epoch: epoch, PullRequest: pullRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func verifiedTestStandingGrant(t *testing.T, scope workspace.StandingGrantScope, nonce string) workspace.StandingGrant {
	t.Helper()
	binding, err := workspace.StandingGrantControlPlaneBinding(scope)
	if err != nil {
		t.Fatal(err)
	}
	receipt := controlPlaneReceipt(t, binding, nonce)
	grant, err := workspace.VerifyStandingGrant(
		context.Background(), &boundaryVerifier{expectedRequest: scope.Digest()}, scope, receipt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func authorizationRequest(
	t *testing.T,
	fixture authorizationFixture,
	frontier workspace.AuthorizationFrontier,
	action workspace.StandingAuthorizationAction,
	pullRequest workspace.PullRequestIdentity,
	epoch uint64,
) workspace.AuthorizationRequest {
	t.Helper()
	options := workspace.AuthorizationRequestOptions{
		WorkspaceID: fixture.workspaceID, Repository: fixture.repository, Remote: fixture.remote,
		Generation: fixture.generation, SerialSegment: fixture.segment, Frontier: frontier,
		Action: action, PullRequest: pullRequest, Epoch: epoch,
	}
	if action == workspace.StandingAuthorizationPush {
		if pullRequest.IsZero() {
			options.ExpectRemoteAbsent = true
		} else {
			options.ExpectedRemoteHead = frontier.Base()
		}
	}
	request, err := workspace.NewAuthorizationRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func authorizationThroughQueue(
	state workspace.AuthorizationState,
	request workspace.AuthorizationRequest,
	now time.Time,
) (workspace.AuthorizationCapability, error) {
	evaluator := authorizationEvaluatorAt(now)
	snapshot := authorizationSnapshotValue()
	planned, err := evaluator.PlanAuthorization(state, request, snapshot)
	if err != nil {
		return workspace.AuthorizationCapability{}, err
	}
	reserved, err := evaluator.ReserveAuthorizationIntent(state, request, snapshot, planned)
	if err != nil {
		return workspace.AuthorizationCapability{}, err
	}
	return evaluator.EnterAuthorizationQueue(state, request, snapshot, reserved)
}

func authorizationBeforeDispatch(
	state workspace.AuthorizationState,
	request workspace.AuthorizationRequest,
	now time.Time,
) (workspace.AuthorizationCapability, error) {
	queued, err := authorizationThroughQueue(state, request, now)
	if err != nil {
		return workspace.AuthorizationCapability{}, err
	}
	return authorizationEvaluatorAt(now).AuthorizeImmediatelyBeforeDispatch(
		state, request, authorizationSnapshotValue(), queued,
	)
}

func authorizationEvaluator(t *testing.T, now time.Time) *workspace.AuthorizationEvaluator {
	t.Helper()
	evaluator, err := workspace.NewAuthorizationEvaluator(&authorizationTestClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	return evaluator
}

func authorizationEvaluatorAt(now time.Time) *workspace.AuthorizationEvaluator {
	evaluator, _ := workspace.NewAuthorizationEvaluator(&authorizationTestClock{now: now})
	return evaluator
}

func authorizationSnapshot(t *testing.T, label string) workspace.AuthorizationSnapshotBinding {
	t.Helper()
	binding, err := workspace.NewAuthorizationSnapshotBinding(workspace.DigestBytes([]byte(label)), 1)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func authorizationSnapshotValue() workspace.AuthorizationSnapshotBinding {
	binding, _ := workspace.NewAuthorizationSnapshotBinding(workspace.DigestBytes([]byte("default")), 1)
	return binding
}

func verifiedTestRevocation(
	t *testing.T,
	fixture authorizationFixture,
	target workspace.Digest,
	nextEpoch uint64,
	nonce string,
) workspace.AuthorizationRevocation {
	t.Helper()
	options := workspace.AuthorizationRevocationOptions{
		WorkspaceID: fixture.workspaceID, Repository: fixture.repository, Remote: fixture.remote,
		Generation: fixture.generation, TargetGrant: target, NextEpoch: nextEpoch, Reason: workspace.MustID("owner-revoked"),
	}
	binding, err := workspace.AuthorizationRevocationControlPlaneBinding(options)
	if err != nil {
		t.Fatal(err)
	}
	receipt := controlPlaneReceipt(t, binding, nonce)
	revocation, err := workspace.VerifyAuthorizationRevocation(
		context.Background(), &boundaryVerifier{expectedRequest: binding.RequestDigest()}, options, receipt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return revocation
}

var _ = time.Time{}
