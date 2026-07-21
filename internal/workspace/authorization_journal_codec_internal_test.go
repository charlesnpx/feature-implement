package workspace

import (
	"testing"
	"time"
)

func TestAuthorizationJournalCodecPreservesProviderDerivedGrantParent(t *testing.T) {
	repository, err := NewRepositoryIdentity("https://github.com/example/repository.git")
	if err != nil {
		t.Fatal(err)
	}
	base, err := ParseGitObjectID("sha1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	head, err := ParseGitObjectID("sha1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := NewAuthorizationFrontier(base, head)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := NewStandingGrantScope(StandingGrantScopeOptions{
		WorkspaceID: MustID("workspace-one"), Repository: repository, Remote: "origin",
		Generation: DigestBytes([]byte("generation-one")), SerialSegment: MustID("serial-one"),
		Frontier: frontier,
		Actions: []StandingAuthorizationAction{
			StandingAuthorizationPush, StandingAuthorizationOpenPullRequest, StandingAuthorizationMerge,
		},
		ExpiresAt: time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := StandingGrant{
		scope: scope, grantID: scope.Digest(), requestDigest: scope.Digest(),
		receiptDigest: DigestBytes([]byte("receipt-one")),
	}
	pullRequest, err := NewPullRequestIdentity(MustID("github"), repository, 71)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := DeriveStandingGrantPullRequest(parent, pullRequest, head)
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewAuthorizationGrantRecordedJournalEvent(scope.workspaceID, scope.generation, derived)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err := marshalAuthorizationJournalEvent(event)
	if err != nil || !supported {
		t.Fatalf("marshal derived grant = %s, %t, %v", payload, supported, err)
	}
	decoded, supported, err := decodeAuthorizationJournalEvent(JournalEventAuthorizationGrantRecorded, payload)
	if err != nil || !supported {
		t.Fatalf("decode derived grant = %#v, %t, %v", decoded, supported, err)
	}
	replayed := decoded.(AuthorizationGrantRecordedJournalEvent)
	if replayed.grant.parentGrantID != parent.grantID ||
		replayed.grant.grantID != derived.grantID || replayed.grant.scope.pullRequest != pullRequest {
		t.Fatalf("derived grant parent or identity was not preserved: %#v", replayed.grant)
	}

	state, err := NewAuthorizationState(scope.workspaceID, repository, scope.remote, scope.generation, 1)
	if err != nil {
		t.Fatal(err)
	}
	parentEvent, _ := NewAuthorizationGrantRecorded(parent)
	state, err = ReduceAuthorization(state, parentEvent)
	if err != nil {
		t.Fatal(err)
	}
	derivedEvent, _ := NewAuthorizationGrantRecorded(replayed.grant)
	if _, err := ReduceAuthorization(state, derivedEvent); err != nil {
		t.Fatalf("replayed derived grant did not match its durable parent: %v", err)
	}
}
