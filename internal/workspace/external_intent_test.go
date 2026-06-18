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
		last.Payload[eventPayloadTargetKey] != "branch:feature/test" {
		t.Fatalf("intent payload = %+v", last.Payload)
	}
	assertContainsString(t, last.WriteSet, ExternalIntentResource(result.Intent.IntentID))
	assertContainsString(t, last.WriteSet, ProviderTargetResource("push:branch:feature/test"))
	assertContainsString(t, last.WriteSet, RemoteRefResource("feature/test"))

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
