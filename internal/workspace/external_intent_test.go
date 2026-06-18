package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReserveExternalIntentRecordsReservationAndIdempotency(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	result, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent: %v", err)
	}
	if result.Status != "reserved" || result.Intent.Status != "reserved" || result.Intent.IntentID == "" || result.Intent.IdempotencyKey == "" {
		t.Fatalf("result = %+v", result)
	}
	if result.Intent.Action != ExternalActionPush || result.Intent.Target != "branch:feature/test" || result.Intent.ApprovalID != approval.Approval.ApprovalID {
		t.Fatalf("intent metadata = %+v", result.Intent)
	}
	if result.Intent.RequestedHeadSHA != "head-sha" || result.Intent.ExpectedBaseSHA != "base-sha" {
		t.Fatalf("intent SHAs = %+v", result.Intent)
	}
	assertContainsString(t, result.Intent.AffectedResources, MergeUnitResource(claim.MergeUnitID))
	assertContainsString(t, result.Intent.AffectedResources, ProviderTargetResource("push:branch:feature/test"))
	assertContainsString(t, result.Intent.AffectedResources, RemoteRefResource("feature/test"))

	events, err := readJournalEvents(EventsPath(fixture.Dir))
	if err != nil {
		t.Fatalf("readJournalEvents: %v", err)
	}
	last := events[len(events)-1]
	if last.Type != EventExternalIntentReserved {
		t.Fatalf("last event type = %s", last.Type)
	}
	if last.Payload[eventPayloadIntentIDKey] != result.Intent.IntentID ||
		last.Payload[eventPayloadIdempotencyKeyKey] != result.Intent.IdempotencyKey ||
		last.Payload[eventPayloadTargetKey] != "branch:feature/test" ||
		last.Payload[eventPayloadRequestedHeadSHAKey] != "head-sha" ||
		last.Payload[eventPayloadExpectedBaseSHAKey] != "base-sha" {
		t.Fatalf("intent payload = %+v", last.Payload)
	}
	usedCount, err := eventIntPayload(last, eventPayloadUsedCountKey)
	if err != nil || usedCount != 1 {
		t.Fatalf("intent used count = %d err=%v payload=%+v", usedCount, err, last.Payload)
	}
	assertContainsString(t, last.WriteSet, ExternalIntentResource(result.Intent.IntentID))
	assertContainsString(t, last.WriteSet, ApprovalResource(approval.Approval.ApprovalID))
	assertContainsString(t, last.WriteSet, ProviderTargetResource("push:branch:feature/test"))
	assertContainsString(t, last.WriteSet, RemoteRefResource("feature/test"))

	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       ExternalActionPush,
		Branch:       "feature/test",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:03:30Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval after reserve: %v", err)
	}
	if check.Status != "approved" || len(check.Approvals) != 1 || check.Approvals[0].UsedCount != 1 {
		t.Fatalf("approval after reserve = %+v", check)
	}

	duplicate, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	var stale StaleResourceError
	if err == nil || !errors.As(err, &stale) || stale.Resource != ExternalIntentResource(result.Intent.IntentID) {
		t.Fatalf("duplicate reserve = %+v err=%v", duplicate, err)
	}
	check, err = CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       ExternalActionPush,
		Branch:       "feature/test",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval after failed duplicate: %v", err)
	}
	if check.Status != "approved" || len(check.Approvals) != 1 || check.Approvals[0].UsedCount != 1 {
		t.Fatalf("failed reservation consumed approval again: %+v", check)
	}
}

func TestReserveExternalIntentSupportsStoryActions(t *testing.T) {
	tests := []struct {
		name   string
		action string
		branch string
		pr     string
		target string
		ref    string
	}{
		{name: "push", action: ExternalActionPush, branch: "feature/test", target: "branch:feature/test", ref: "feature/test"},
		{name: "open-pr", action: ExternalActionOpenPR, branch: "feature/test", target: "branch:feature/test", ref: "feature/test"},
		{name: "remote-delete", action: ExternalActionRemoteDelete, branch: "feature/test", target: "branch:feature/test", ref: "feature/test"},
		{name: "merge", action: ExternalActionMerge, pr: "35", target: "pr:35", ref: "workspace-orchestration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, claim, attempt, approval := newExternalIntentFixture(t, tt.action)
			result, err := ReserveExternalIntent(ExternalIntentReserveOptions{
				WorkspaceDir:     fixture.Dir,
				MergeUnitID:      claim.MergeUnitID,
				AttemptID:        attempt.AttemptID,
				AgentID:          "worker-a",
				LeaseID:          claim.LeaseID,
				ApprovalID:       approval.Approval.ApprovalID,
				Action:           tt.action,
				Branch:           tt.branch,
				PR:               tt.pr,
				RequestedHeadSHA: "head-sha",
				ExpectedBaseSHA:  "base-sha",
				Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
			})
			if err != nil {
				t.Fatalf("ReserveExternalIntent: %v", err)
			}
			if result.Intent.Action != tt.action || result.Intent.Target != tt.target {
				t.Fatalf("intent = %+v", result.Intent)
			}
			assertContainsString(t, result.Intent.AffectedResources, ProviderTargetResource(tt.action+":"+tt.target))
			if tt.ref != "" {
				assertContainsString(t, result.Intent.AffectedResources, RemoteRefResource(tt.ref))
			}
		})
	}
}

func TestReserveExternalIntentRequiresApproval(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	_, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       "missing-approval",
		Action:           ExternalActionPush,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "approval not found: missing-approval") {
		t.Fatalf("missing approval error = %v", err)
	}
}

func TestReserveExternalIntentConsumesOneUseApproval(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionPush},
		MaxUses:      1,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	if _, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/one",
		RequestedHeadSHA: "head-sha-1",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("ReserveExternalIntent first: %v", err)
	}

	_, err = ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/two",
		RequestedHeadSHA: "head-sha-2",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "has no uses remaining") {
		t.Fatalf("second reserve error = %v", err)
	}
}

func TestReserveExternalIntentDuplicateOneUseReturnsStaleIntent(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionPush},
		MaxUses:      1,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	first, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/one",
		RequestedHeadSHA: "head-sha-1",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent first: %v", err)
	}

	_, err = ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/one",
		RequestedHeadSHA: "head-sha-1",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	var stale StaleResourceError
	if err == nil || !errors.As(err, &stale) || stale.Resource != ExternalIntentResource(first.Intent.IntentID) {
		t.Fatalf("duplicate one-use reserve error = %v", err)
	}
}

func TestApprovalReplayIgnoresLegacyIntentReservationWithoutApprovalWrite(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	intentID := "legacy-intent"
	intentResource := ExternalIntentResource(intentID)
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventExternalIntentReserved,
		Payload: map[string]any{
			eventPayloadIntentIDKey:          intentID,
			eventPayloadIdempotencyKeyKey:    "legacy-key",
			eventPayloadMergeUnitIDKey:       claim.MergeUnitID,
			eventPayloadAttemptIDKey:         attempt.AttemptID,
			eventPayloadAgentIDKey:           "worker-a",
			eventPayloadLeaseIDKey:           claim.LeaseID,
			eventPayloadApprovalIDRefKey:     approval.Approval.ApprovalID,
			eventPayloadActionKey:            ExternalActionPush,
			eventPayloadScopeKey:             "merge-unit",
			eventPayloadTargetKey:            "branch:feature/test",
			eventPayloadBranchKey:            "feature/test",
			eventPayloadRequestedHeadSHAKey:  "head-sha",
			eventPayloadExpectedBaseSHAKey:   "base-sha",
			eventPayloadAffectedResourcesKey: []string{ProviderTargetResource("push:branch:feature/test")},
		},
		ReadSet:  map[string]int{intentResource: revisions[intentResource]},
		WriteSet: []string{intentResource},
		Now:      fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent legacy intent: %v", err)
	}
	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       ExternalActionPush,
		Branch:       "feature/test",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval: %v", err)
	}
	if check.Status != "approved" || len(check.Approvals) != 1 || check.Approvals[0].UsedCount != 0 {
		t.Fatalf("legacy intent should not consume approval: %+v", check)
	}
}

func TestReserveExternalIntentValidatesApprovalScopeAndTarget(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionMerge)
	_, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		PR:               "99",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "is for PR 35") {
		t.Fatalf("mismatched approval error = %v", err)
	}
	_, err = ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "action merge requires --pr or --branch") {
		t.Fatalf("missing target error = %v", err)
	}
}

func TestRecordExternalIntentResultRecordsStatuses(t *testing.T) {
	tests := []struct {
		status         string
		policyAccepted bool
		accepted       bool
	}{
		{status: ExternalResultSucceeded, accepted: true},
		{status: ExternalResultNotPerformed},
		{status: ExternalResultFailedBeforeSideEffect},
		{status: ExternalResultFailedAfterSideEffect},
		{status: ExternalResultAmbiguous},
		{status: ExternalResultFailedBeforeSideEffect, policyAccepted: true, accepted: true},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
			reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")

			recorded, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
				WorkspaceDir:   fixture.Dir,
				MergeUnitID:    claim.MergeUnitID,
				AttemptID:      attempt.AttemptID,
				AgentID:        "worker-a",
				LeaseID:        claim.LeaseID,
				IntentID:       reserved.Intent.IntentID,
				Status:         tt.status,
				PolicyAccepted: tt.policyAccepted,
				Details:        "provider result",
				Now:            fixedJournalTime("2026-06-17T10:04:00Z"),
			})
			if err != nil {
				t.Fatalf("RecordExternalIntentResult: %v", err)
			}
			if recorded.Status != "recorded" || recorded.Intent.Status != tt.status || recorded.Result.Status != tt.status {
				t.Fatalf("recorded result = %+v", recorded)
			}
			if recorded.Result.PolicyAccepted != tt.policyAccepted || recorded.Result.Accepted != tt.accepted || recorded.Result.Details != "provider result" {
				t.Fatalf("recorded result policy = %+v", recorded.Result)
			}
			if recorded.Intent.Result == nil || recorded.Intent.Result.Status != tt.status {
				t.Fatalf("intent result view = %+v", recorded.Intent.Result)
			}
			events := readTestJournalEvents(t, fixture.Dir)
			last := events[len(events)-1]
			if last.Type != EventExternalIntentResultRecorded {
				t.Fatalf("last event type = %s", last.Type)
			}
			if last.Payload[eventPayloadIntentIDKey] != reserved.Intent.IntentID ||
				last.Payload[eventPayloadStatusKey] != tt.status ||
				last.Payload[eventPayloadPolicyAcceptedKey] != tt.policyAccepted ||
				last.Payload[eventPayloadDetailsKey] != "provider result" {
				t.Fatalf("result payload = %+v", last.Payload)
			}
			assertContainsString(t, last.WriteSet, ExternalIntentResource(reserved.Intent.IntentID))
			assertContainsString(t, last.WriteSet, ProviderTargetResource("push:branch:feature/test"))
			assertContainsString(t, last.WriteSet, RemoteRefResource("feature/test"))
			if _, err := Status(fixture.Dir); err != nil {
				t.Fatalf("Status after result: %v", err)
			}
		})
	}
}

func TestRecordExternalIntentResultRejectsUnknownStatusAndDuplicate(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")

	for _, status := range []string{"sideways", ExternalResultReconciledByOperator} {
		t.Run(status, func(t *testing.T) {
			_, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
				WorkspaceDir: fixture.Dir,
				MergeUnitID:  claim.MergeUnitID,
				AttemptID:    attempt.AttemptID,
				AgentID:      "worker-a",
				LeaseID:      claim.LeaseID,
				IntentID:     reserved.Intent.IntentID,
				Status:       status,
				Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
			})
			if err == nil || !strings.Contains(err.Error(), "unsupported external intent result status") {
				t.Fatalf("unsupported status %s error = %v", status, err)
			}
		})
	}

	if _, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		IntentID:     reserved.Intent.IntentID,
		Status:       ExternalResultSucceeded,
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("RecordExternalIntentResult first: %v", err)
	}
	_, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		IntentID:     reserved.Intent.IntentID,
		Status:       ExternalResultSucceeded,
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "already has result succeeded") {
		t.Fatalf("duplicate result error = %v", err)
	}
}

func TestReconcileExternalIntentClearsUnresolvedFreezeAfterLeaseRelease(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")
	_, err := ReconcileExternalIntent(ExternalIntentReconcileOptions{
		WorkspaceDir: fixture.Dir,
		IntentID:     reserved.Intent.IntentID,
		Operator:     "operator-a",
		Details:      "operator confirmed no provider side effect",
		Now:          fixedJournalTime("2026-06-17T10:03:30Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "has no recorded result while lease") {
		t.Fatalf("reconcile while active lease error = %v", err)
	}
	if _, err := Release(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		IntentID:     reserved.Intent.IntentID,
		Status:       ExternalResultSucceeded,
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	}); err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("record after release should require active lease: %v", err)
	}

	reconciled, err := ReconcileExternalIntent(ExternalIntentReconcileOptions{
		WorkspaceDir: fixture.Dir,
		IntentID:     reserved.Intent.IntentID,
		Operator:     "operator-a",
		Details:      "operator confirmed no provider side effect",
		Now:          fixedJournalTime("2026-06-17T10:06:00Z"),
	})
	if err != nil {
		t.Fatalf("ReconcileExternalIntent unresolved: %v", err)
	}
	if reconciled.Result.Status != ExternalResultReconciledByOperator || !reconciled.Result.Accepted || reconciled.Result.Operator != "operator-a" {
		t.Fatalf("reconciled unresolved = %+v", reconciled)
	}
	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.FrozenResources) != 0 {
		t.Fatalf("unresolved freeze after reconcile = %+v", status.FrozenResources)
	}
}

func TestLegacyReconciledByOperatorResultEventStillReplays(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	intentResource := ExternalIntentResource(reserved.Intent.IntentID)
	affectedResources := append([]string{}, reserved.Intent.AffectedResources...)
	readSet := map[string]int{
		intentResource: revisions[intentResource],
	}
	writeSet := []string{intentResource}
	for _, resource := range affectedResources {
		readSet[resource] = revisions[resource]
		writeSet = append(writeSet, resource)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventExternalIntentResultRecorded,
		Payload: map[string]any{
			eventPayloadIntentIDKey:       reserved.Intent.IntentID,
			eventPayloadMergeUnitIDKey:    claim.MergeUnitID,
			eventPayloadAttemptIDKey:      attempt.AttemptID,
			eventPayloadAgentIDKey:        "worker-a",
			eventPayloadLeaseIDKey:        claim.LeaseID,
			eventPayloadActionKey:         reserved.Intent.Action,
			eventPayloadTargetKey:         reserved.Intent.Target,
			eventPayloadStatusKey:         ExternalResultReconciledByOperator,
			eventPayloadPolicyAcceptedKey: true,
			eventPayloadDetailsKey:        "legacy operator result",
		},
		ReadSet:  readSet,
		WriteSet: writeSet,
		Now:      fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent legacy reconciled result: %v", err)
	}

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status with legacy reconciled result: %v", err)
	}
	if len(status.FrozenResources) != 0 {
		t.Fatalf("legacy reconciled result should clear freezes: %+v", status.FrozenResources)
	}
}

func TestExternalIntentCompletionRequiresAcceptedResult(t *testing.T) {
	t.Run("missing result blocks completion", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
		startExternalIntentLifecycle(t, fixture, claim, attempt)
		reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:04:00Z")

		_, err := Transition(TransitionOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			From:         MergeUnitInProgress,
			To:           MergeUnitCompleted,
			Evidence: map[string]any{
				evidenceCommitSHAKey:        "commit-sha-1",
				evidenceExternalIntentIDKey: reserved.Intent.IntentID,
			},
			Now: fixedJournalTime("2026-06-17T10:05:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "blocked by frozen resource") || !strings.Contains(err.Error(), "requires record_result") {
			t.Fatalf("missing result error = %v", err)
		}
	})

	t.Run("unaccepted failure blocks completion", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
		startExternalIntentLifecycle(t, fixture, claim, attempt)
		reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:04:00Z")
		if _, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			IntentID:     reserved.Intent.IntentID,
			Status:       ExternalResultFailedAfterSideEffect,
			Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
		}); err != nil {
			t.Fatalf("RecordExternalIntentResult: %v", err)
		}

		_, err := Transition(TransitionOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			From:         MergeUnitInProgress,
			To:           MergeUnitCompleted,
			Evidence: map[string]any{
				evidenceCommitSHAKey:        "commit-sha-1",
				evidenceExternalIntentIDKey: reserved.Intent.IntentID,
			},
			Now: fixedJournalTime("2026-06-17T10:06:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "result failed_after_side_effect is not accepted") {
			t.Fatalf("unaccepted result error = %v", err)
		}
	})

	t.Run("accepted result allows completion", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
		startExternalIntentLifecycle(t, fixture, claim, attempt)
		reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:04:00Z")
		if _, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			IntentID:     reserved.Intent.IntentID,
			Status:       ExternalResultSucceeded,
			Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
		}); err != nil {
			t.Fatalf("RecordExternalIntentResult: %v", err)
		}

		completed, err := Transition(TransitionOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			From:         MergeUnitInProgress,
			To:           MergeUnitCompleted,
			Evidence: map[string]any{
				evidenceCommitSHAKey:        "commit-sha-1",
				evidenceExternalIntentIDKey: reserved.Intent.IntentID,
			},
			Now: fixedJournalTime("2026-06-17T10:06:00Z"),
		})
		if err != nil {
			t.Fatalf("Transition complete: %v", err)
		}
		if completed.EventType != EventMergeUnitCompleted || completed.Evidence[evidenceExternalIntentIDKey] != reserved.Intent.IntentID {
			t.Fatalf("completed = %+v", completed)
		}
		status, err := Status(fixture.Dir)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, claim.MergeUnitID)
		if unit.Status != MergeUnitCompleted {
			t.Fatalf("scheduler unit = %+v", unit)
		}
	})
}

func TestUnresolvedExternalIntentFreezesStatusAndBlocksOverlappingIntent(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionPush},
		MaxUses:      2,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	reserved, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha-1",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent: %v", err)
	}

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	assertFrozenResource(t, status.FrozenResources, MergeUnitResource(claim.MergeUnitID), reserved.Intent.IntentID, externalIntentFreezeActionRecordResult)
	assertFrozenResource(t, status.FrozenResources, ProviderTargetResource("push:branch:feature/test"), reserved.Intent.IntentID, externalIntentFreezeActionRecordResult)
	assertFrozenResource(t, status.FrozenResources, RemoteRefResource("feature/test"), reserved.Intent.IntentID, externalIntentFreezeActionRecordResult)

	_, err = ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha-2",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "external intent reserve blocked by frozen resource") {
		t.Fatalf("overlapping reserve error = %v", err)
	}

	if _, err := Release(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	status, err = Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status after release: %v", err)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, claim.MergeUnitID)
	if len(status.Blocked) != 1 || status.Blocked[0] != claim.MergeUnitID || len(unit.BlockingConditions) != 1 {
		t.Fatalf("blocked status = blocked %+v unit %+v", status.Blocked, unit)
	}
	condition := unit.BlockingConditions[0]
	if condition.Type != "frozen_resource" || condition.IntentID != reserved.Intent.IntentID || condition.RequiredAction != externalIntentFreezeActionRecordResult {
		t.Fatalf("freeze blocking condition = %+v", condition)
	}
}

func TestAmbiguousMergeFreezeRequiresOperatorReconciliation(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionMerge)
	startExternalIntentLifecycle(t, fixture, claim, attempt)
	reserved, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		PR:               "35",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent: %v", err)
	}
	recorded, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir:   fixture.Dir,
		MergeUnitID:    claim.MergeUnitID,
		AttemptID:      attempt.AttemptID,
		AgentID:        "worker-a",
		LeaseID:        claim.LeaseID,
		IntentID:       reserved.Intent.IntentID,
		Status:         ExternalResultAmbiguous,
		PolicyAccepted: true,
		Details:        "provider timeout after merge request",
		Now:            fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err != nil {
		t.Fatalf("RecordExternalIntentResult: %v", err)
	}
	if recorded.Result.Accepted {
		t.Fatalf("ambiguous result must not be accepted by policy override: %+v", recorded.Result)
	}

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status ambiguous: %v", err)
	}
	assertFrozenResource(t, status.FrozenResources, MergeUnitResource(claim.MergeUnitID), reserved.Intent.IntentID, externalIntentFreezeActionOperatorReconcile)
	assertFrozenResource(t, status.FrozenResources, ProviderTargetResource("merge:pr:35"), reserved.Intent.IntentID, externalIntentFreezeActionOperatorReconcile)
	assertFrozenResource(t, status.FrozenResources, RemoteRefResource("workspace-orchestration"), reserved.Intent.IntentID, externalIntentFreezeActionOperatorReconcile)

	_, err = Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey:        "commit-sha-1",
			evidenceExternalIntentIDKey: reserved.Intent.IntentID,
		},
		Now: fixedJournalTime("2026-06-17T10:06:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "requires operator_reconcile") {
		t.Fatalf("ambiguous transition error = %v", err)
	}

	reconciled, err := ReconcileExternalIntent(ExternalIntentReconcileOptions{
		WorkspaceDir: fixture.Dir,
		IntentID:     reserved.Intent.IntentID,
		Operator:     "operator-a",
		Details:      "confirmed merge completed remotely",
		Now:          fixedJournalTime("2026-06-17T10:07:00Z"),
	})
	if err != nil {
		t.Fatalf("ReconcileExternalIntent: %v", err)
	}
	if reconciled.Result.Status != ExternalResultReconciledByOperator || !reconciled.Result.Accepted || reconciled.Result.Operator != "operator-a" {
		t.Fatalf("reconciled result = %+v", reconciled)
	}
	status, err = Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status reconciled: %v", err)
	}
	if len(status.FrozenResources) != 0 {
		t.Fatalf("frozen resources after reconcile = %+v", status.FrozenResources)
	}

	completed, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey:        "commit-sha-1",
			evidenceExternalIntentIDKey: reserved.Intent.IntentID,
		},
		Now: fixedJournalTime("2026-06-17T10:08:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition after reconcile: %v", err)
	}
	if completed.EventType != EventMergeUnitCompleted {
		t.Fatalf("completed = %+v", completed)
	}
}

func newExternalIntentFixture(t *testing.T, action string) (workspaceFixture, NextResult, AttemptResult, ApprovalResult) {
	t.Helper()
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	grant := ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{action},
		Branch:       "feature/test",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		MaxUses:      2,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}
	if action == ExternalActionMerge {
		grant.Branch = ""
		grant.PR = "35"
	}
	approval, err := GrantApproval(grant)
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	return fixture, claim, attempt, approval
}

func reserveExternalIntentForTest(t *testing.T, fixture workspaceFixture, claim NextResult, attempt AttemptResult, approval ApprovalResult, branch string, headSHA string, at string) ExternalIntentResult {
	t.Helper()
	result, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionPush,
		Branch:           branch,
		RequestedHeadSHA: headSHA,
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime(at),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent: %v", err)
	}
	return result
}

func startExternalIntentLifecycle(t *testing.T, fixture workspaceFixture, claim NextResult, attempt AttemptResult) {
	t.Helper()
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
}

func assertContainsString(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q missing from %+v", want, values)
}

func assertFrozenResource(t *testing.T, freezes []ExternalIntentFreezeView, resource string, intentID string, requiredAction string) {
	t.Helper()
	for _, freeze := range freezes {
		if freeze.Resource == resource && freeze.IntentID == intentID && freeze.RequiredAction == requiredAction {
			return
		}
	}
	t.Fatalf("freeze %s intent=%s action=%s missing from %+v", resource, intentID, requiredAction, freezes)
}
