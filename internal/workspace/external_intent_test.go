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

func assertContainsString(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q missing from %+v", want, values)
}
