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
	baseResource := RefreshInputResource(claim.MergeUnitID, attempt.AttemptID, refreshInputBase)
	headResource := RefreshInputResource(claim.MergeUnitID, attempt.AttemptID, refreshInputHead)
	if _, ok := last.ReadSet[baseResource]; !ok {
		t.Fatalf("reserve read set missing %s: %+v", baseResource, last.ReadSet)
	}
	if _, ok := last.ReadSet[headResource]; !ok {
		t.Fatalf("reserve read set missing %s: %+v", headResource, last.ReadSet)
	}

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

func TestApprovalReplayRejectsStaleExternalIntentApproval(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionMerge},
		PR:           "42",
		HeadSHA:      "pre-head-sha",
		BaseSHA:      attempt.BaseSHA,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	appendRefreshForStaleApprovalReplayTest(t, fixture, claim, attempt, "pre-head-sha", "post-head-sha")
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	intentResource := ExternalIntentResource("intent-stale-merge")
	approvalResource := ApprovalResource(granted.Approval.ApprovalID)
	readSet := staleApprovalConsumptionReadSet(claim.MergeUnitID, attempt.AttemptID, approvalResource, revisions)
	readSet[intentResource] = revisions[intentResource]
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventExternalIntentReserved,
		Payload: map[string]any{
			eventPayloadIntentIDKey:          "intent-stale-merge",
			eventPayloadIdempotencyKeyKey:    "intent-key-stale-merge",
			eventPayloadMergeUnitIDKey:       claim.MergeUnitID,
			eventPayloadAttemptIDKey:         attempt.AttemptID,
			eventPayloadAgentIDKey:           "worker-a",
			eventPayloadLeaseIDKey:           claim.LeaseID,
			eventPayloadApprovalIDRefKey:     granted.Approval.ApprovalID,
			eventPayloadActionKey:            ExternalActionMerge,
			eventPayloadScopeKey:             "merge-unit",
			eventPayloadTargetKey:            "pr:42",
			eventPayloadPRKey:                "42",
			eventPayloadRequestedHeadSHAKey:  "pre-head-sha",
			eventPayloadExpectedBaseSHAKey:   attempt.BaseSHA,
			eventPayloadAffectedResourcesKey: []string{ProviderTargetResource("merge:pr:42")},
			eventPayloadUsedCountKey:         1,
		},
		ReadSet:  readSet,
		WriteSet: []string{intentResource, approvalResource},
		Now:      fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent stale intent: %v", err)
	}

	_, err = approvalSnapshots(readTestJournalEvents(t, fixture.Dir))
	if err == nil || !strings.Contains(err.Error(), "consumes stale approval") || !strings.Contains(err.Error(), "base, head") {
		t.Fatalf("approvalSnapshots stale intent error = %v", err)
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

func TestExternalIntentCompletionAcceptsRequiredIntentList(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	pushApproval := grantExternalIntentApprovalForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, ExternalActionPush, ready.Attempt.Branch, "", ready.HeadSHA, ready.BaseSHA, "2026-01-02T15:09:00Z")
	openPRApproval := grantExternalIntentApprovalForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, ExternalActionOpenPR, ready.Attempt.Branch, "", ready.HeadSHA, ready.BaseSHA, "2026-01-02T15:09:10Z")
	pushIntent := reserveExternalIntentActionForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, pushApproval.Approval.ApprovalID, ExternalActionPush, ready.Attempt.Branch, "", ready.HeadSHA, ready.BaseSHA, "2026-01-02T15:09:20Z")
	recordExternalIntentResultForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, pushIntent.Intent.IntentID, ExternalResultSucceeded, false, "2026-01-02T15:09:30Z")
	openPRIntent := reserveExternalIntentActionForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, openPRApproval.Approval.ApprovalID, ExternalActionOpenPR, ready.Attempt.Branch, "", ready.HeadSHA, ready.BaseSHA, "2026-01-02T15:09:40Z")
	recordExternalIntentResultForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, openPRIntent.Intent.IntentID, ExternalResultSucceeded, false, "2026-01-02T15:09:50Z")
	startExternalIntentLifecycleAt(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, ready.Attempt.Worktree, "2026-01-02T15:10:10Z")

	completed, err := Transition(TransitionOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey: "commit-sha-1",
			evidenceExternalIntentIDsKey: []string{
				pushIntent.Intent.IntentID,
				openPRIntent.Intent.IntentID,
			},
		},
		Now: fixedWorkspaceTime("2026-01-02T15:10:40Z"),
	})
	if err != nil {
		t.Fatalf("Transition complete: %v", err)
	}
	intentIDs, ok := completed.Evidence[evidenceExternalIntentIDsKey].([]string)
	if completed.EventType != EventMergeUnitCompleted || !ok || len(intentIDs) != 2 {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestExternalIntentCompletionListAcceptsMergeIntent(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	if _, err := queueMergeReadyAttempt(t, ready, "", ready.Attempt.Branch, "2026-01-02T15:09:00Z"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	startExternalIntentLifecycleAt(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, ready.Attempt.Worktree, "2026-01-02T15:09:30Z")
	mergeIntent := reserveExternalIntentActionForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, ready.Approval.Approval.ApprovalID, ExternalActionMerge, ready.Attempt.Branch, "", ready.HeadSHA, ready.BaseSHA, "2026-01-02T15:10:00Z")
	recordExternalIntentResultForTest(t, ready.Fixture.Dir, ready.Claim.MergeUnitID, ready.Attempt.AttemptID, ready.Claim.AgentID, ready.Claim.LeaseID, mergeIntent.Intent.IntentID, ExternalResultSucceeded, false, "2026-01-02T15:11:00Z")

	completed, err := Transition(TransitionOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey:         "commit-sha-1",
			evidenceExternalIntentIDsKey: []string{mergeIntent.Intent.IntentID},
		},
		Now: fixedWorkspaceTime("2026-01-02T15:12:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition complete: %v", err)
	}
	if completed.EventType != EventMergeUnitCompleted {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestExternalIntentCompletionListRejectsMissingRequiredIntent(t *testing.T) {
	fixture, claim, attempt, pushApproval := newExternalIntentFixture(t, ExternalActionPush)
	openPRApproval := grantExternalIntentApprovalForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, ExternalActionOpenPR, "feature/test", "", "head-sha", "base-sha", "2026-06-17T10:02:10Z")
	pushIntent := reserveExternalIntentActionForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, pushApproval.Approval.ApprovalID, ExternalActionPush, "feature/test", "", "head-sha", "base-sha", "2026-06-17T10:04:00Z")
	recordExternalIntentResultForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, pushIntent.Intent.IntentID, ExternalResultSucceeded, false, "2026-06-17T10:04:10Z")
	openPRIntent := reserveExternalIntentActionForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, openPRApproval.Approval.ApprovalID, ExternalActionOpenPR, "feature/test", "", "head-sha", "base-sha", "2026-06-17T10:04:20Z")
	recordExternalIntentResultForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, openPRIntent.Intent.IntentID, ExternalResultSucceeded, false, "2026-06-17T10:04:30Z")
	startExternalIntentLifecycle(t, fixture, claim, attempt)

	_, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey:         "commit-sha-1",
			evidenceExternalIntentIDsKey: []string{pushIntent.Intent.IntentID},
		},
		Now: fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "missing required external intent "+openPRIntent.Intent.IntentID) {
		t.Fatalf("missing required intent error = %v", err)
	}
}

func TestExternalIntentCompletionListRejectsUnacceptedAndUnknownIntents(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		fixture, claim, attempt := newApprovalAttemptFixture(t)
		startExternalIntentLifecycle(t, fixture, claim, attempt)

		_, err := Transition(TransitionOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			From:         MergeUnitInProgress,
			To:           MergeUnitCompleted,
			Evidence: map[string]any{
				evidenceCommitSHAKey:         "commit-sha-1",
				evidenceExternalIntentIDsKey: []string{"intent-missing"},
			},
			Now: fixedJournalTime("2026-06-17T10:04:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "references unknown external intent intent-missing") {
			t.Fatalf("unknown intent error = %v", err)
		}
	})

	t.Run("not accepted", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
		intent := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:04:00Z")
		recordExternalIntentResultForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, intent.Intent.IntentID, ExternalResultFailedAfterSideEffect, false, "2026-06-17T10:04:10Z")
		startExternalIntentLifecycle(t, fixture, claim, attempt)

		_, err := Transition(TransitionOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			From:         MergeUnitInProgress,
			To:           MergeUnitCompleted,
			Evidence: map[string]any{
				evidenceCommitSHAKey:         "commit-sha-1",
				evidenceExternalIntentIDsKey: []string{intent.Intent.IntentID},
			},
			Now: fixedJournalTime("2026-06-17T10:05:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "result failed_after_side_effect is not accepted") {
			t.Fatalf("unaccepted intent error = %v", err)
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
		startExternalIntentLifecycle(t, fixture, claim, attempt)
		intent := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:04:00Z")
		recordExternalIntentResultForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, intent.Intent.IntentID, ExternalResultAmbiguous, true, "2026-06-17T10:04:10Z")

		_, err := Transition(TransitionOptions{
			WorkspaceDir: fixture.Dir,
			MergeUnitID:  claim.MergeUnitID,
			AttemptID:    attempt.AttemptID,
			AgentID:      "worker-a",
			LeaseID:      claim.LeaseID,
			From:         MergeUnitInProgress,
			To:           MergeUnitCompleted,
			Evidence: map[string]any{
				evidenceCommitSHAKey:         "commit-sha-1",
				evidenceExternalIntentIDsKey: []string{intent.Intent.IntentID},
			},
			Now: fixedJournalTime("2026-06-17T10:05:00Z"),
		})
		if err == nil || !strings.Contains(err.Error(), "blocked by frozen resource") || !strings.Contains(err.Error(), "requires operator_reconcile") {
			t.Fatalf("ambiguous intent error = %v", err)
		}
	})
}

func TestExternalIntentCompletionListAllowsReconciledRequiredIntent(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	intent := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:04:00Z")
	recordExternalIntentResultForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, intent.Intent.IntentID, ExternalResultAmbiguous, true, "2026-06-17T10:04:10Z")
	if _, err := ReconcileExternalIntent(ExternalIntentReconcileOptions{
		WorkspaceDir: fixture.Dir,
		IntentID:     intent.Intent.IntentID,
		Operator:     "operator-a",
		Details:      "provider side effect verified",
		Now:          fixedJournalTime("2026-06-17T10:04:20Z"),
	}); err != nil {
		t.Fatalf("ReconcileExternalIntent: %v", err)
	}
	startExternalIntentLifecycle(t, fixture, claim, attempt)

	completed, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey:         "commit-sha-1",
			evidenceExternalIntentIDsKey: []string{intent.Intent.IntentID},
		},
		Now: fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition complete: %v", err)
	}
	if completed.EventType != EventMergeUnitCompleted {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestRemoteDeleteCleanupIntentAfterCompletionDoesNotBlockDependentReadiness(t *testing.T) {
	fixture := newChainedDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	startExternalIntentLifecycle(t, fixture, claim, attempt)
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("Transition complete: %v", err)
	}
	cleanupApproval := grantExternalIntentApprovalForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, ExternalActionRemoteDelete, attempt.Branch, "", "head-sha", "base-sha", "2026-06-17T10:05:00Z")
	cleanup := reserveExternalIntentActionForTest(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, cleanupApproval.Approval.ApprovalID, ExternalActionRemoteDelete, attempt.Branch, "", "head-sha", "base-sha", "2026-06-17T10:05:10Z")

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	report := findExternalIntentReportByID(t, status.ExternalIntents, cleanup.Intent.IntentID)
	if report.Purpose != ExternalIntentPurposeCleanup {
		t.Fatalf("cleanup report = %+v", report)
	}
	if !containsString(status.Ready, "sources:story-b") {
		t.Fatalf("cleanup intent should not block dependent readiness: ready=%+v blockers=%+v", status.Ready, status.Blockers)
	}
	next, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:06:00Z"),
	})
	if err != nil {
		t.Fatalf("Next dependent: %v", err)
	}
	if next.MergeUnitID != "sources:story-b" {
		t.Fatalf("dependent claim = %+v", next)
	}
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

	view, err := rebuildSchedulerViewAt(fixture.Dir, fixedJournalTime("2026-06-17T10:03:30Z")())
	if err != nil {
		t.Fatalf("rebuildSchedulerViewAt active: %v", err)
	}
	assertFrozenResource(t, view.FrozenResources, MergeUnitResource(claim.MergeUnitID), reserved.Intent.IntentID, externalIntentFreezeActionRecordResult)
	assertFrozenResource(t, view.FrozenResources, ProviderTargetResource("push:branch:feature/test"), reserved.Intent.IntentID, externalIntentFreezeActionRecordResult)
	assertFrozenResource(t, view.FrozenResources, RemoteRefResource("feature/test"), reserved.Intent.IntentID, externalIntentFreezeActionRecordResult)

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
	view, err = rebuildSchedulerViewAt(fixture.Dir, fixedJournalTime("2026-06-17T10:05:30Z")())
	if err != nil {
		t.Fatalf("rebuildSchedulerViewAt after release: %v", err)
	}
	unit := findSchedulerUnit(t, view, claim.MergeUnitID)
	if len(view.Blocked) != 1 || view.Blocked[0] != claim.MergeUnitID || len(unit.BlockingConditions) != 1 {
		t.Fatalf("blocked status = blocked %+v unit %+v", view.Blocked, unit)
	}
	condition := unit.BlockingConditions[0]
	if condition.Type != "frozen_resource" || condition.IntentID != reserved.Intent.IntentID || condition.RequiredAction != externalIntentFreezeActionOperatorReconcile {
		t.Fatalf("freeze blocking condition = %+v", condition)
	}
}

func TestAmbiguousMergeFreezeRequiresOperatorReconciliation(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	ready := prepareMergeReadinessWithFreshApproval(t, fixture, claim, attempt, "35", "", "head-sha", "base-sha", "2026-06-17T10")
	if _, err := queueMergeReadyAttempt(t, ready, "35", "", "2026-06-17T10:17:00Z"); err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:17:30Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	reserved, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       ready.Approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		PR:               "35",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Now:              fixedJournalTime("2026-06-17T10:18:00Z"),
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
		Now:            fixedJournalTime("2026-06-17T10:19:00Z"),
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
		Now: fixedJournalTime("2026-06-17T10:20:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "requires operator_reconcile") {
		t.Fatalf("ambiguous transition error = %v", err)
	}

	reconciled, err := ReconcileExternalIntent(ExternalIntentReconcileOptions{
		WorkspaceDir: fixture.Dir,
		IntentID:     reserved.Intent.IntentID,
		Operator:     "operator-a",
		Details:      "confirmed merge completed remotely",
		Now:          fixedJournalTime("2026-06-17T10:21:00Z"),
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
		Now: fixedJournalTime("2026-06-17T10:22:00Z"),
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

func grantExternalIntentApprovalForTest(t *testing.T, workspaceDir string, mergeUnitID string, attemptID string, agentID string, leaseID string, action string, branch string, pr string, headSHA string, baseSHA string, at string) ApprovalResult {
	t.Helper()
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  mergeUnitID,
		AttemptID:    attemptID,
		AgentID:      agentID,
		LeaseID:      leaseID,
		Actions:      []string{action},
		Branch:       branch,
		PR:           pr,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		MaxUses:      1,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime(at),
	})
	if err != nil {
		t.Fatalf("GrantApproval %s: %v", action, err)
	}
	return approval
}

func reserveExternalIntentActionForTest(t *testing.T, workspaceDir string, mergeUnitID string, attemptID string, agentID string, leaseID string, approvalID string, action string, branch string, pr string, headSHA string, baseSHA string, at string) ExternalIntentResult {
	t.Helper()
	result, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     workspaceDir,
		MergeUnitID:      mergeUnitID,
		AttemptID:        attemptID,
		AgentID:          agentID,
		LeaseID:          leaseID,
		ApprovalID:       approvalID,
		Action:           action,
		Branch:           branch,
		PR:               pr,
		RequestedHeadSHA: headSHA,
		ExpectedBaseSHA:  baseSHA,
		Now:              fixedJournalTime(at),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent %s: %v", action, err)
	}
	return result
}

func recordExternalIntentResultForTest(t *testing.T, workspaceDir string, mergeUnitID string, attemptID string, agentID string, leaseID string, intentID string, status string, policyAccepted bool, at string) ExternalIntentResultRecordResult {
	t.Helper()
	result, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir:   workspaceDir,
		MergeUnitID:    mergeUnitID,
		AttemptID:      attemptID,
		AgentID:        agentID,
		LeaseID:        leaseID,
		IntentID:       intentID,
		Status:         status,
		PolicyAccepted: policyAccepted,
		Now:            fixedJournalTime(at),
	})
	if err != nil {
		t.Fatalf("RecordExternalIntentResult %s: %v", intentID, err)
	}
	return result
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
	startExternalIntentLifecycleAt(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID, "worker-a", claim.LeaseID, attempt.Worktree, "2026-06-17T10:03:00Z")
}

func startExternalIntentLifecycleAt(t *testing.T, workspaceDir string, mergeUnitID string, attemptID string, agentID string, leaseID string, worktree string, at string) {
	t.Helper()
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  mergeUnitID,
		AttemptID:    attemptID,
		AgentID:      agentID,
		LeaseID:      leaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: worktree},
		Now:          fixedJournalTime(at),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
}

func findExternalIntentReportByID(t *testing.T, reports []ExternalIntentReport, intentID string) ExternalIntentReport {
	t.Helper()
	for _, report := range reports {
		if report.IntentID == intentID {
			return report
		}
	}
	t.Fatalf("external intent report %s missing from %+v", intentID, reports)
	return ExternalIntentReport{}
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
