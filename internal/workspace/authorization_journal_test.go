package workspace_test

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type providerPullRequestVerifier struct {
	expected workspace.Digest
	calls    int
	err      error
}

func TestAuthorizationJournalProtectedWorkflowsUseRealEd25519(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T16:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	keyID := workspace.MustID("authorization-owner-key")
	adapterID := workspace.MustID("authorization-coordinator")
	privateKey := ed25519.NewKeyFromSeed(bytesOf(33, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	replay := &controlPlaneTestReplay{}
	frontier, _ := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(),
		SerialSegment: workspace.MustID("serial-crypto"), Frontier: frontier,
		Actions:   []workspace.StandingAuthorizationAction{workspace.StandingAuthorizationPush},
		ExpiresAt: mustTime(t, "2026-07-21T19:00:00Z"), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantBinding, _ := workspace.StandingGrantControlPlaneBinding(scope)
	grantEnvelope, _ := workspace.NewControlPlaneEnvelopeV2(
		grantBinding, keyID, "real-grant", mustTime(t, "2026-07-21T20:00:00Z"), adapterID,
	)
	grantReceipt := signedControlPlaneReceipt(t, grantEnvelope, privateKey)
	grantVerifier := realControlPlaneVerifier(
		t, adapterID, keyID, publicKey, workspace.ControlPlaneReceiptStandingGrant,
		mustTime(t, "2026-07-21T16:01:00Z"), replay,
	)
	grant, _, err := workspace.RecordStandingGrant(
		context.Background(), journal, definition, grantVerifier, scope, grantReceipt,
		mustTime(t, "2026-07-21T16:01:00Z"),
	)
	if err != nil {
		t.Fatalf("record real signed grant: %v", err)
	}
	revocation := workspace.AuthorizationRevocationOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(),
		TargetGrant: grant.GrantID(), NextEpoch: 2, Reason: workspace.MustID("real-owner-revocation"),
	}
	revocationBinding, _ := workspace.AuthorizationRevocationControlPlaneBinding(revocation)
	revocationEnvelope, _ := workspace.NewControlPlaneEnvelopeV2(
		revocationBinding, keyID, "real-revocation", mustTime(t, "2026-07-21T20:00:00Z"), adapterID,
	)
	revocationReceipt := signedControlPlaneReceipt(t, revocationEnvelope, privateKey)
	revocationVerifier := realControlPlaneVerifier(
		t, adapterID, keyID, publicKey, workspace.ControlPlaneReceiptRevocation,
		mustTime(t, "2026-07-21T16:02:00Z"), replay,
	)
	if _, _, err := workspace.RecordAuthorizationRevocation(
		context.Background(), journal, definition, revocationVerifier, revocation, revocationReceipt,
		mustTime(t, "2026-07-21T16:02:00Z"),
	); err != nil {
		t.Fatalf("record real signed revocation: %v", err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := workspace.RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil || projection.State().Epoch() != 2 || len(projection.ReceiptDigests()) != 2 {
		t.Fatalf("real signed authorization projection = %#v, %v", projection, err)
	}
}

func TestAuthorizationSafetyChangesRequireExactSignedDurableReceipt(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T16:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	frontier, _ := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	segment := workspace.MustID("serial-safety")
	scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(), SerialSegment: segment,
		Frontier: frontier, Actions: []workspace.StandingAuthorizationAction{workspace.StandingAuthorizationPush},
		ExpiresAt: mustTime(t, "2026-07-21T20:00:00Z"), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantBinding, _ := workspace.StandingGrantControlPlaneBinding(scope)
	if _, _, err := workspace.RecordStandingGrant(
		context.Background(), journal, definition, &boundaryVerifier{expectedRequest: scope.Digest()}, scope,
		controlPlaneReceipt(t, grantBinding, "safety-grant"), mustTime(t, "2026-07-21T16:01:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T18:00:00Z")}
	evaluator, _ := workspace.NewAuthorizationEvaluator(clock)
	request := authorizationJournalRequest(t, definition, segment, frontier, 1)
	queued := authorizationJournalQueue(t, journal, definition, evaluator, request)
	_, beforeSafety, err := workspace.ReadAuthorizationEvaluationSnapshot(journal, definition)
	if err != nil {
		t.Fatal(err)
	}

	keyID := workspace.MustID("authorization-safety-key")
	adapterID := workspace.MustID("authorization-safety-coordinator")
	privateKey := ed25519.NewKeyFromSeed(bytesOf(44, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	replay := &controlPlaneTestReplay{}
	signSafety := func(
		state workspace.AuthorizationState,
		target workspace.AuthorizationSafetyState,
		nonce string,
	) (workspace.ControlPlaneBinding, workspace.ControlPlaneReceiptV2) {
		t.Helper()
		binding, bindingErr := workspace.AuthorizationSafetyChangeControlPlaneBinding(state, target)
		if bindingErr != nil {
			t.Fatal(bindingErr)
		}
		envelope, envelopeErr := workspace.NewControlPlaneEnvelopeV2(
			binding, keyID, nonce, mustTime(t, "2026-07-21T20:00:00Z"), adapterID,
		)
		if envelopeErr != nil {
			t.Fatal(envelopeErr)
		}
		return binding, signedControlPlaneReceipt(t, envelope, privateKey)
	}
	verifier := func(now string) *workspace.Ed25519ControlPlaneVerifier {
		return realControlPlaneVerifier(
			t, adapterID, keyID, publicKey, workspace.ControlPlaneReceiptReconciliation,
			mustTime(t, now), replay,
		)
	}

	state, _, err := workspace.ReadAuthorizationEvaluationSnapshot(journal, definition)
	if err != nil {
		t.Fatal(err)
	}
	blocked := workspace.NewAuthorizationSafetyState(true, true, true, true)
	_, blockedReceipt := signSafety(state, blocked, "safety-blocked")
	blockedRecord, err := workspace.RecordAuthorizationSafetyChange(
		context.Background(), journal, definition, verifier("2026-07-21T16:02:00Z"), blocked,
		blockedReceipt, mustTime(t, "2026-07-21T16:02:00Z"),
	)
	if err != nil || blockedRecord.EventType() != workspace.JournalEventAuthorizationSafetyChanged {
		t.Fatalf("record signed blocked safety = %#v, %v", blockedRecord, err)
	}

	blockedState, _, err := workspace.ReadAuthorizationEvaluationSnapshot(journal, definition)
	if err != nil {
		t.Fatal(err)
	}
	clear := workspace.NewAuthorizationSafetyState(false, false, false, false)
	clearBinding, clearReceipt := signSafety(blockedState, clear, "safety-cleared")
	if _, err := workspace.RecordAuthorizationSafetyChange(
		context.Background(), journal, definition, nil, clear, clearReceipt,
		mustTime(t, "2026-07-21T16:02:30Z"),
	); err == nil || !strings.Contains(err.Error(), "verifier") {
		t.Fatalf("unsigned safety clearing error = %v", err)
	}
	wrongTarget := workspace.NewAuthorizationSafetyState(false, true, false, false)
	_, wrongReceipt := signSafety(blockedState, wrongTarget, "safety-wrong-request")
	if _, err := workspace.RecordAuthorizationSafetyChange(
		context.Background(), journal, definition, verifier("2026-07-21T16:02:45Z"), clear, wrongReceipt,
		mustTime(t, "2026-07-21T16:02:45Z"),
	); err == nil || !strings.Contains(err.Error(), "exact protected transition") {
		t.Fatalf("wrong safety request receipt error = %v", err)
	}

	clearRecord, err := workspace.RecordAuthorizationSafetyChange(
		context.Background(), journal, definition, verifier("2026-07-21T16:03:00Z"), clear,
		clearReceipt, mustTime(t, "2026-07-21T16:03:00Z"),
	)
	if err != nil || clearRecord.EventType() != workspace.JournalEventAuthorizationSafetyChanged {
		t.Fatalf("record signed clear safety = %#v, %v", clearRecord, err)
	}
	retryVerifier := &boundaryVerifier{expectedRequest: clearBinding.RequestDigest()}
	if retry, err := workspace.RecordAuthorizationSafetyChange(
		context.Background(), journal, definition, retryVerifier, clear, clearReceipt,
		mustTime(t, "2026-07-21T16:03:30Z"),
	); err != nil || retry.Sequence() != 0 || retryVerifier.calls != 0 {
		t.Fatalf("durable safety retry = %#v, verifier calls=%d, %v", retry, retryVerifier.calls, err)
	}
	if _, err := workspace.NewJournalAppend(
		clearRecord.Event(), mustTime(t, "2026-07-21T16:03:45Z"),
		clearRecord.ReadSet(), clearRecord.WriteSet(),
	); err == nil || !strings.Contains(err.Error(), "protected control-plane") {
		t.Fatalf("direct safety append error = %v", err)
	}

	afterState, afterSafety, err := workspace.ReadAuthorizationEvaluationSnapshot(journal, definition)
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Safety() != clear || afterSafety.JournalHead() == beforeSafety.JournalHead() ||
		afterSafety.AuthorizationRevision() <= beforeSafety.AuthorizationRevision() {
		t.Fatalf("safety transitions did not advance exact capability bindings: before=%#v after=%#v", beforeSafety, afterSafety)
	}
	if _, _, err := workspace.RecordAuthorizationEffectDispatched(
		journal, definition, evaluator, request, queued, workspace.MustID("stale-after-safety"),
	); err == nil || !strings.Contains(err.Error(), "fresh matching") {
		t.Fatalf("safety transition did not invalidate queued capability: %v", err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := workspace.RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil || projection.State().Safety() != clear || len(projection.ReceiptDigests()) != 3 {
		t.Fatalf("signed safety projection = %#v, %v", projection, err)
	}
	if snapshot.Revision(workspace.AuthorizationReceiptJournalResource(blockedReceipt.ReceiptDigest())) != 1 ||
		snapshot.Revision(workspace.AuthorizationReceiptJournalResource(clearReceipt.ReceiptDigest())) != 1 {
		t.Fatal("signed safety receipts did not receive exact CAS resources")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedSnapshot, err := reopened.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildAuthorizationRuntime(reopenedSnapshot, definition)
	if err != nil || replayed.State().Safety() != clear || len(replayed.ReceiptDigests()) != 3 {
		t.Fatalf("replayed signed safety state = %#v, %v", replayed, err)
	}
}

func (verifier *providerPullRequestVerifier) VerifyProviderPullRequest(
	_ context.Context,
	verification workspace.ProviderPullRequestVerification,
	observation workspace.ProviderPullRequestObservation,
) error {
	verifier.calls++
	if verifier.err != nil {
		return verifier.err
	}
	if verification.ObservationDigest() != verifier.expected || observation.Digest() != verifier.expected ||
		verification.Repository() != observation.Repository() || verification.Frontier().Head() != observation.Head() {
		return fmt.Errorf("provider observation does not match exact verification")
	}
	return nil
}

func TestAuthorizationJournalPersistsGrantsSegmentsRevocationsAndReceiptCAS(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T16:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	frontier, err := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	if err != nil {
		t.Fatal(err)
	}
	segment := workspace.MustID("serial-one")
	scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(), SerialSegment: segment,
		Frontier: frontier, Actions: []workspace.StandingAuthorizationAction{
			workspace.StandingAuthorizationPush, workspace.StandingAuthorizationOpenPullRequest,
		},
		ExpiresAt: mustTime(t, "2026-07-21T20:00:00Z"), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := workspace.StandingGrantControlPlaneBinding(scope)
	receipt := controlPlaneReceipt(t, binding, "journal-grant")
	verifier := &boundaryVerifier{expectedRequest: scope.Digest()}
	grant, record, err := workspace.RecordStandingGrant(
		context.Background(), journal, definition, verifier, scope, receipt, mustTime(t, "2026-07-21T16:01:00Z"),
	)
	if err != nil || record.EventType() != workspace.JournalEventAuthorizationGrantRecorded {
		t.Fatalf("record grant = %#v, %#v, %v", grant, record, err)
	}
	if retry, retryRecord, err := workspace.RecordStandingGrant(
		context.Background(), journal, definition, verifier, scope, receipt, mustTime(t, "2026-07-21T16:01:10Z"),
	); err != nil || retry.GrantID() != grant.GrantID() || retryRecord.Sequence() != 0 || verifier.calls != 1 {
		t.Fatalf("durable grant retry = %#v, %#v, verifier calls=%d, %v", retry, retryRecord, verifier.calls, err)
	}
	if _, err := workspace.NewJournalAppend(
		record.Event(), mustTime(t, "2026-07-21T16:01:30Z"), record.ReadSet(), record.WriteSet(),
	); err == nil || !strings.Contains(err.Error(), "protected control-plane") {
		t.Fatalf("direct authorization append error = %v", err)
	}

	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := workspace.RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil || projection.State().Epoch() != 1 || len(projection.State().Grants()) != 1 ||
		len(projection.ReceiptDigests()) != 1 || projection.ReceiptDigests()[0] != receipt.ReceiptDigest() {
		t.Fatalf("authorization projection = %#v, %v", projection, err)
	}
	if snapshot.Revision(workspace.AuthorizationReceiptJournalResource(receipt.ReceiptDigest())) != 1 {
		t.Fatal("control-plane receipt did not receive its own CAS resource")
	}
	if _, err := workspace.RecordAuthorizationSegmentCompletion(
		journal, definition, segment, mustTime(t, "2026-07-21T16:02:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	if retry, err := workspace.RecordAuthorizationSegmentCompletion(
		journal, definition, segment, mustTime(t, "2026-07-21T16:02:30Z"),
	); err != nil || retry.Sequence() != 0 {
		t.Fatalf("segment completion retry = %#v, %v", retry, err)
	}

	revocationOptions := workspace.AuthorizationRevocationOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(),
		TargetGrant: grant.GrantID(), NextEpoch: 2, Reason: workspace.MustID("owner-revoked"),
	}
	revocationBinding, _ := workspace.AuthorizationRevocationControlPlaneBinding(revocationOptions)
	revocationReceipt := controlPlaneReceipt(t, revocationBinding, "journal-revocation")
	revocationVerifier := &boundaryVerifier{expectedRequest: revocationBinding.RequestDigest()}
	if _, record, err := workspace.RecordAuthorizationRevocation(
		context.Background(), journal, definition, revocationVerifier,
		revocationOptions, revocationReceipt, mustTime(t, "2026-07-21T16:03:00Z"),
	); err != nil || record.EventType() != workspace.JournalEventAuthorizationRevoked {
		t.Fatalf("record revocation = %#v, %v", record, err)
	}
	if retry, retryRecord, err := workspace.RecordAuthorizationRevocation(
		context.Background(), journal, definition, revocationVerifier,
		revocationOptions, revocationReceipt, mustTime(t, "2026-07-21T16:03:10Z"),
	); err != nil || retry.Digest() != revocationBinding.RequestDigest() ||
		retryRecord.Sequence() != 0 || revocationVerifier.calls != 1 {
		t.Fatalf("durable revocation retry = %#v, %#v, verifier calls=%d, %v", retry, retryRecord, revocationVerifier.calls, err)
	}

	snapshot, err = journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	projection, err = workspace.RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil || projection.State().Epoch() != 2 || len(projection.State().Grants()) != 0 ||
		len(projection.State().CompletedSegments()) != 1 ||
		len(projection.ReceiptDigests()) != 2 {
		t.Fatalf("revoked authorization projection = %#v, %v", projection, err)
	}
	if _, err := workspace.RebuildWorkspaceRuntime(snapshot); err != nil {
		t.Fatalf("core runtime rejected authorization events: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedSnapshot, err := reopened.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildAuthorizationRuntime(reopenedSnapshot, definition)
	if err != nil || replayed.State().Epoch() != 2 || len(replayed.ReceiptDigests()) != 2 {
		t.Fatalf("reopened authorization projection = %#v, %v", replayed, err)
	}
}

func TestAuthorizationJournalRejectsStaleEpochAndGeneration(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T16:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	frontier, _ := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	segment := workspace.MustID("serial-one")
	staleScope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(), SerialSegment: segment,
		Frontier: frontier, Actions: []workspace.StandingAuthorizationAction{workspace.StandingAuthorizationPush},
		ExpiresAt: mustTime(t, "2026-07-21T20:00:00Z"), Epoch: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := workspace.StandingGrantControlPlaneBinding(staleScope)
	receipt := controlPlaneReceipt(t, binding, "stale-epoch")
	if _, _, err := workspace.RecordStandingGrant(
		context.Background(), journal, definition, &boundaryVerifier{expectedRequest: staleScope.Digest()},
		staleScope, receipt, mustTime(t, "2026-07-21T16:01:00Z"),
	); err == nil || !strings.Contains(err.Error(), "epoch") {
		t.Fatalf("stale grant epoch error = %v", err)
	}
}

func TestProviderDerivedStandingGrantIsVerifiedSingleUseAndDurable(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T16:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	frontier, _ := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(),
		SerialSegment: workspace.MustID("serial-provider"), Frontier: frontier,
		Actions: []workspace.StandingAuthorizationAction{
			workspace.StandingAuthorizationPush, workspace.StandingAuthorizationOpenPullRequest,
			workspace.StandingAuthorizationMerge,
		},
		ExpiresAt: mustTime(t, "2026-07-21T20:00:00Z"), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := workspace.StandingGrantControlPlaneBinding(scope)
	receipt := controlPlaneReceipt(t, binding, "provider-parent")
	parent, _, err := workspace.RecordStandingGrant(
		context.Background(), journal, definition, &boundaryVerifier{expectedRequest: scope.Digest()},
		scope, receipt, mustTime(t, "2026-07-21T16:01:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := workspace.NewProviderPullRequestObservation(
		workspace.MustID("github"), definition.Workspace().Repository(), 76, frontier.Head(),
		workspace.DigestBytes([]byte("provider-open-pr-result")),
	)
	if err != nil {
		t.Fatal(err)
	}
	providerVerifier := &providerPullRequestVerifier{expected: observation.Digest()}
	derived, record, err := workspace.RecordDerivedStandingGrantPullRequest(
		context.Background(), journal, definition, providerVerifier, parent.GrantID(), observation,
		mustTime(t, "2026-07-21T16:02:00Z"),
	)
	if err != nil || record.EventType() != workspace.JournalEventAuthorizationGrantRecorded {
		t.Fatalf("record provider-derived grant = %#v, %#v, %v", derived, record, err)
	}
	parentID, hasParent := derived.ParentGrantID()
	observationDigest, hasObservation := derived.ProviderObservationDigest()
	if !hasParent || parentID != parent.GrantID() || !hasObservation || observationDigest != observation.Digest() {
		t.Fatalf("provider-derived grant bindings = %#v", derived)
	}
	if retry, retryRecord, err := workspace.RecordDerivedStandingGrantPullRequest(
		context.Background(), journal, definition, providerVerifier, parent.GrantID(), observation,
		mustTime(t, "2026-07-21T16:02:30Z"),
	); err != nil || retry.GrantID() != derived.GrantID() || retryRecord.Sequence() != 0 || providerVerifier.calls != 1 {
		t.Fatalf("provider-derived retry = %#v, %#v, calls=%d, %v", retry, retryRecord, providerVerifier.calls, err)
	}
	conflict, _ := workspace.NewProviderPullRequestObservation(
		workspace.MustID("github"), definition.Workspace().Repository(), 77, frontier.Head(),
		workspace.DigestBytes([]byte("different-provider-result")),
	)
	if _, _, err := workspace.RecordDerivedStandingGrantPullRequest(
		context.Background(), journal, definition, providerVerifier, parent.GrantID(), conflict,
		mustTime(t, "2026-07-21T16:03:00Z"),
	); err == nil || !strings.Contains(err.Error(), "different provider-derived child") {
		t.Fatalf("conflicting provider-derived child error = %v", err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision(workspace.AuthorizationProviderObservationJournalResource(observation.Digest())) != 1 {
		t.Fatal("provider observation was not durably claimed")
	}
	projection, err := workspace.RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil || len(projection.State().Grants()) != 2 {
		t.Fatalf("provider-derived projection = %#v, %v", projection, err)
	}
	mergeRequest, err := workspace.NewAuthorizationRequest(workspace.AuthorizationRequestOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(),
		SerialSegment: scope.SerialSegment(), Frontier: frontier,
		Action: workspace.StandingAuthorizationMerge, PullRequest: observation.PullRequest(), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	mergeEvaluator, _ := workspace.NewAuthorizationEvaluator(
		&authorizationTestClock{now: mustTime(t, "2026-07-21T18:00:00Z")},
	)
	mergeQueue := authorizationJournalQueue(t, journal, definition, mergeEvaluator, mergeRequest)
	mergeState, mergeSnapshot, err := workspace.ReadAuthorizationEvaluationSnapshot(journal, definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mergeEvaluator.AuthorizeImmediatelyBeforeDispatch(
		mergeState, mergeRequest, mergeSnapshot, mergeQueue,
	); err != nil {
		t.Fatalf("provider-derived grant did not authorize exact merge: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedSnapshot, err := reopened.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildAuthorizationRuntime(reopenedSnapshot, definition)
	if err != nil || len(replayed.State().Grants()) != 2 {
		t.Fatalf("replayed provider-derived grants = %#v, %v", replayed, err)
	}
}

func TestAuthorizationDispatchUsesTrustedClockAndDurableRevocationCAS(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T16:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	frontier, _ := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	segment := workspace.MustID("serial-dispatch")
	recordGrant := func(epoch uint64, nonce string) workspace.StandingGrant {
		scope, scopeErr := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
			WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
			Remote: definition.Workspace().Remote(), Generation: definition.Generation(), SerialSegment: segment,
			Frontier: frontier, Actions: []workspace.StandingAuthorizationAction{workspace.StandingAuthorizationPush},
			ExpiresAt: mustTime(t, "2026-07-21T20:00:00Z"), Epoch: epoch,
		})
		if scopeErr != nil {
			t.Fatal(scopeErr)
		}
		binding, _ := workspace.StandingGrantControlPlaneBinding(scope)
		grant, _, recordErr := workspace.RecordStandingGrant(
			context.Background(), journal, definition, &boundaryVerifier{expectedRequest: scope.Digest()}, scope,
			controlPlaneReceipt(t, binding, nonce), mustTime(t, fmt.Sprintf("2026-07-21T16:%02d:00Z", epoch*3-2)),
		)
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		return grant
	}
	grantOne := recordGrant(1, "dispatch-grant-one")
	requestOne := authorizationJournalRequest(t, definition, segment, frontier, 1)
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T18:00:00Z")}
	evaluator, _ := workspace.NewAuthorizationEvaluator(clock)
	queueOne := authorizationJournalQueue(t, journal, definition, evaluator, requestOne)
	revokeOne := workspace.AuthorizationRevocationOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(),
		TargetGrant: grantOne.GrantID(), NextEpoch: 2, Reason: workspace.MustID("owner-revoked-before-dispatch"),
	}
	revokeOneBinding, _ := workspace.AuthorizationRevocationControlPlaneBinding(revokeOne)
	if _, _, err := workspace.RecordAuthorizationRevocation(
		context.Background(), journal, definition,
		&boundaryVerifier{expectedRequest: revokeOneBinding.RequestDigest()}, revokeOne,
		controlPlaneReceipt(t, revokeOneBinding, "revoke-before-dispatch"), mustTime(t, "2026-07-21T16:03:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspace.RecordAuthorizationEffectDispatched(
		journal, definition, evaluator, requestOne, queueOne, workspace.MustID("stale-effect"),
	); err == nil || !strings.Contains(err.Error(), "fresh matching") {
		t.Fatalf("revocation did not invalidate queued capability: %v", err)
	}

	grantTwo := recordGrant(2, "dispatch-grant-two")
	requestTwo := authorizationJournalRequest(t, definition, segment, frontier, 2)
	queueTwo := authorizationJournalQueue(t, journal, definition, evaluator, requestTwo)
	obligation, dispatchRecord, err := workspace.RecordAuthorizationEffectDispatched(
		journal, definition, evaluator, requestTwo, queueTwo, workspace.MustID("push-effect"),
	)
	if err != nil || dispatchRecord.EventType() != workspace.JournalEventAuthorizationEffectDispatched ||
		obligation.RequestDigest() != requestTwo.Digest() {
		t.Fatalf("durable dispatch = %#v, %#v, %v", obligation, dispatchRecord, err)
	}
	revokeTwo := workspace.AuthorizationRevocationOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(),
		TargetGrant: grantTwo.GrantID(), NextEpoch: 3, Reason: workspace.MustID("owner-revoked-after-dispatch"),
	}
	revokeTwoBinding, _ := workspace.AuthorizationRevocationControlPlaneBinding(revokeTwo)
	if _, _, err := workspace.RecordAuthorizationRevocation(
		context.Background(), journal, definition,
		&boundaryVerifier{expectedRequest: revokeTwoBinding.RequestDigest()}, revokeTwo,
		controlPlaneReceipt(t, revokeTwoBinding, "revoke-after-dispatch"), mustTime(t, "2026-07-21T18:06:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := workspace.RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil || projection.State().Epoch() != 3 ||
		len(projection.State().OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("post-dispatch revocation projection = %#v, %v", projection, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedSnapshot, err := reopened.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildAuthorizationRuntime(reopenedSnapshot, definition)
	if err != nil || len(replayed.State().OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("replayed dispatch obligation = %#v, %v", replayed, err)
	}
}

func authorizationJournalRequest(
	t *testing.T,
	definition workspace.EffectiveWorkspaceDefinition,
	segment workspace.ID,
	frontier workspace.AuthorizationFrontier,
	epoch uint64,
) workspace.AuthorizationRequest {
	t.Helper()
	request, err := workspace.NewAuthorizationRequest(workspace.AuthorizationRequestOptions{
		WorkspaceID: definition.Workspace().ID(), Repository: definition.Workspace().Repository(),
		Remote: definition.Workspace().Remote(), Generation: definition.Generation(), SerialSegment: segment,
		Frontier: frontier, Action: workspace.StandingAuthorizationPush, Epoch: epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func authorizationJournalQueue(
	t *testing.T,
	journal *workspace.WorkspaceJournal,
	definition workspace.EffectiveWorkspaceDefinition,
	evaluator *workspace.AuthorizationEvaluator,
	request workspace.AuthorizationRequest,
) workspace.AuthorizationCapability {
	t.Helper()
	state, snapshot, err := workspace.ReadAuthorizationEvaluationSnapshot(journal, definition)
	if err != nil {
		t.Fatal(err)
	}
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
	return queued
}
