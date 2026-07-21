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

func TestStandingAuthorizationCoversPrePRDerivedPRAndReviewFixFrontiers(t *testing.T) {
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
	planned, err := workspace.PlanAuthorization(state, push, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.AuthorizeImmediatelyBeforeDispatch(state, push, planned, now); err == nil {
		t.Fatal("planning capability skipped intent reservation and queue entry")
	}
	reserved, err := workspace.ReserveAuthorizationIntent(state, push, planned, now)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := workspace.EnterAuthorizationQueue(state, push, reserved, now)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := workspace.AuthorizeImmediatelyBeforeDispatch(state, push, queued, now)
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
	if _, err := workspace.AuthorizeImmediatelyBeforeDispatch(state, openPR, queued, now); err == nil {
		t.Fatal("queue capability authorized a different request")
	}
	if _, err := authorizationBeforeDispatch(state, openPR, now); err != nil {
		t.Fatal(err)
	}
	mergeBeforePR := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationMerge, workspace.PullRequestIdentity{}, 1)
	if _, err := authorizationBeforeDispatch(state, mergeBeforePR, now); err == nil {
		t.Fatal("pre-PR grant authorized merge without derived PR identity")
	}

	pr, err := workspace.NewPullRequestIdentity(workspace.MustID("github"), fixture.repository, 71)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := workspace.DeriveStandingGrantPullRequest(grant, pr, fixture.frontier.Head())
	if err != nil {
		t.Fatal(err)
	}
	if derived.GrantID() == grant.GrantID() {
		t.Fatal("provider-derived PR did not create a distinct grant identity")
	}
	derivedEvent, _ := workspace.NewAuthorizationGrantRecorded(derived)
	state, err = workspace.ReduceAuthorization(state, derivedEvent)
	if err != nil {
		t.Fatal(err)
	}
	merge := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationMerge, pr, 1)
	if _, err := authorizationBeforeDispatch(state, merge, now); err != nil {
		t.Fatalf("derived PR merge authorization: %v", err)
	}

	reviewFixFrontier, err := workspace.NewAuthorizationFrontier(fixture.frontier.Head(), mustGitObject(t, 'd'))
	if err != nil {
		t.Fatal(err)
	}
	reviewFixScope := authorizationScope(
		t, fixture, reviewFixFrontier, pr, "2026-07-21T20:00:00Z", 1,
		workspace.StandingAuthorizationPush, workspace.StandingAuthorizationMerge,
	)
	reviewFixGrant := verifiedTestStandingGrant(t, reviewFixScope, "review-fix-grant")
	reviewFixEvent, _ := workspace.NewAuthorizationGrantRecorded(reviewFixGrant)
	state, err = workspace.ReduceAuthorization(state, reviewFixEvent)
	if err != nil {
		t.Fatal(err)
	}
	reviewFixPush := authorizationRequest(t, fixture, reviewFixFrontier, workspace.StandingAuthorizationPush, pr, 1)
	if _, err := authorizationBeforeDispatch(state, reviewFixPush, now); err != nil {
		t.Fatalf("review-fix push authorization: %v", err)
	}
	wrongFrontier, _ := workspace.NewAuthorizationFrontier(fixture.frontier.Head(), mustGitObject(t, 'e'))
	wrongHead := authorizationRequest(t, fixture, wrongFrontier, workspace.StandingAuthorizationPush, pr, 1)
	if _, err := authorizationBeforeDispatch(state, wrongHead, now); err == nil {
		t.Fatal("wrong review-fix head was authorized")
	}
	wrongRepository, _ := workspace.NewRepositoryIdentity("https://github.com/example/other.git")
	wrongPR, _ := workspace.NewPullRequestIdentity(workspace.MustID("github"), wrongRepository, 71)
	if _, err := workspace.DeriveStandingGrantPullRequest(grant, wrongPR, fixture.frontier.Head()); err == nil {
		t.Fatal("wrong-repository PR identity was derived")
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
			if _, err := workspace.AuthorizeImmediatelyBeforeDispatch(state, request, queued, now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s blocker error = %v", test.name, err)
			}
		})
	}

	segmentEvent, _ := workspace.NewAuthorizationSegmentCompleted(fixture.segment)
	segmentState, err := workspace.ReduceAuthorization(baseState, segmentEvent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.AuthorizeImmediatelyBeforeDispatch(segmentState, request, queued, now); err == nil || !strings.Contains(err.Error(), "complete") {
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
	if _, err := workspace.AuthorizeImmediatelyBeforeDispatch(ambiguousState, request, queued, now); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("overlapping grant ambiguity error = %v", err)
	}

	if _, err := workspace.AuthorizeImmediatelyBeforeDispatch(baseState, request, queued, scope.ExpiresAt()); err == nil {
		t.Fatal("expired standing grant was authorized")
	}
	staleRequest := authorizationRequest(t, fixture, fixture.frontier, workspace.StandingAuthorizationPush, workspace.PullRequestIdentity{}, 2)
	if _, err := workspace.PlanAuthorization(baseState, staleRequest, now); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale epoch request error = %v", err)
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
	if _, err := workspace.NewAuthorizationEffectDispatched(
		workspace.MustID("expired-push-effect"), capability, scope.ExpiresAt(),
	); err == nil {
		t.Fatal("expired pre-dispatch capability created a dispatched effect")
	}

	revokedBeforeDispatch, err := workspace.ReduceAuthorization(state, revokedEvent)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, _ := workspace.NewAuthorizationEffectDispatched(
		workspace.MustID("push-effect"), capability, mustTime(t, "2026-07-21T18:00:01Z"),
	)
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
	replayedDispatch, _ := workspace.NewAuthorizationEffectDispatched(
		workspace.MustID("second-push-effect"), capability, mustTime(t, "2026-07-21T18:00:02Z"),
	)
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
	if _, err := workspace.PlanAuthorization(postDispatchRevoked, request, mustTime(t, "2026-07-21T18:00:00Z")); err == nil {
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
	request, err := workspace.NewAuthorizationRequest(workspace.AuthorizationRequestOptions{
		WorkspaceID: fixture.workspaceID, Repository: fixture.repository, Remote: fixture.remote,
		Generation: fixture.generation, SerialSegment: fixture.segment, Frontier: frontier,
		Action: action, PullRequest: pullRequest, Epoch: epoch,
	})
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
	planned, err := workspace.PlanAuthorization(state, request, now)
	if err != nil {
		return workspace.AuthorizationCapability{}, err
	}
	reserved, err := workspace.ReserveAuthorizationIntent(state, request, planned, now)
	if err != nil {
		return workspace.AuthorizationCapability{}, err
	}
	return workspace.EnterAuthorizationQueue(state, request, reserved, now)
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
	return workspace.AuthorizeImmediatelyBeforeDispatch(state, request, queued, now)
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
