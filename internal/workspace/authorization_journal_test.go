package workspace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

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
	if _, record, err := workspace.RecordAuthorizationRevocation(
		context.Background(), journal, definition,
		&boundaryVerifier{expectedRequest: revocationBinding.RequestDigest()},
		revocationOptions, revocationReceipt, mustTime(t, "2026-07-21T16:03:00Z"),
	); err != nil || record.EventType() != workspace.JournalEventAuthorizationRevoked {
		t.Fatalf("record revocation = %#v, %v", record, err)
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
