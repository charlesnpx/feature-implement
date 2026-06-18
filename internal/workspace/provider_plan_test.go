package workspace

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanExternalProviderPushCommands(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	before := len(readTestJournalEvents(t, fixture.Dir))

	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
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
		t.Fatalf("PlanExternalProviderCommand: %v", err)
	}
	if planned.Status != "planned" || planned.Intent.IntentID == "" || planned.Intent.IdempotencyKey == "" {
		t.Fatalf("planned = %+v", planned)
	}
	if planned.Intent.Status != "planned" {
		t.Fatalf("intent status = %s, want planned", planned.Intent.Status)
	}
	if got := len(readTestJournalEvents(t, fixture.Dir)); got != before {
		t.Fatalf("planning appended events: got %d want %d", got, before)
	}
	if len(planned.Plan.Commands) != 4 {
		t.Fatalf("commands = %+v", planned.Plan.Commands)
	}
	if !strings.Contains(planned.Plan.ApprovalCommand, "feature workspace approve check") ||
		!strings.Contains(planned.Plan.ApprovalCommand, "--action push") {
		t.Fatalf("approval command = %s", planned.Plan.ApprovalCommand)
	}
	if !strings.Contains(planned.Plan.IntentCommand, "feature workspace external intent reserve") ||
		!strings.Contains(planned.Plan.IntentCommand, "--approval "+approval.Approval.ApprovalID) ||
		!strings.Contains(planned.Plan.IntentCommand, "--head-sha head-sha") {
		t.Fatalf("intent command = %s", planned.Plan.IntentCommand)
	}
	wantProvider := "git -C " + attempt.Worktree + " push -u origin HEAD:feature/test"
	if planned.Plan.ProviderCommand != wantProvider {
		t.Fatalf("provider command = %s, want %s", planned.Plan.ProviderCommand, wantProvider)
	}
	if !strings.Contains(planned.Plan.ResultCommand, "--intent "+planned.Intent.IntentID) ||
		!strings.Contains(planned.Plan.ResultCommand, "--status succeeded") {
		t.Fatalf("result command = %s", planned.Plan.ResultCommand)
	}
}

func TestPlanExternalProviderOpenPRMarkers(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionOpenPR)
	planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          "worker-a",
		LeaseID:          claim.LeaseID,
		ApprovalID:       approval.Approval.ApprovalID,
		Action:           ExternalActionOpenPR,
		Branch:           "feature/test",
		RequestedHeadSHA: "head-sha",
		ExpectedBaseSHA:  "base-sha",
		Title:            "Story implementation",
		Body:             "Summary body",
		Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("PlanExternalProviderCommand: %v", err)
	}
	if !strings.Contains(planned.Plan.ProviderCommand, "gh pr create") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--base workspace-orchestration") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--head feature/test") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--title 'Story implementation'") ||
		!strings.Contains(planned.Plan.ProviderCommand, "--body ") {
		t.Fatalf("provider command = %s", planned.Plan.ProviderCommand)
	}
	marker := parseProviderMarker(t, planned.Plan.PRBody)
	if marker.WorkspaceID != planned.WorkspaceID ||
		marker.MergeUnitID != claim.MergeUnitID ||
		marker.AttemptID != attempt.AttemptID ||
		marker.IntentID != planned.Intent.IntentID ||
		marker.HeadSHA != "head-sha" ||
		marker.BaseSHA != "base-sha" ||
		marker.Action != ExternalActionOpenPR ||
		marker.Target != "branch:feature/test" {
		t.Fatalf("marker = %+v planned = %+v", marker, planned)
	}
}

func TestPlanExternalProviderMergeAndRemoteDeleteCommands(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionMerge)
		planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
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
			Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
		})
		if err != nil {
			t.Fatalf("PlanExternalProviderCommand merge: %v", err)
		}
		wantProvider := "cd " + attempt.Worktree + " && gh pr merge 35 --merge"
		if planned.Plan.ProviderCommand != wantProvider {
			t.Fatalf("merge provider command = %s", planned.Plan.ProviderCommand)
		}
		if !strings.Contains(planned.Plan.ApprovalCommand, "--action merge") ||
			!strings.Contains(planned.Plan.IntentCommand, "--pr 35") {
			t.Fatalf("merge plan = %+v", planned.Plan)
		}
	})

	t.Run("remote delete", func(t *testing.T) {
		fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionRemoteDelete)
		planned, err := PlanExternalProviderCommand(ExternalProviderPlanOptions{
			WorkspaceDir:     fixture.Dir,
			MergeUnitID:      claim.MergeUnitID,
			AttemptID:        attempt.AttemptID,
			AgentID:          "worker-a",
			LeaseID:          claim.LeaseID,
			ApprovalID:       approval.Approval.ApprovalID,
			Action:           ExternalActionRemoteDelete,
			Branch:           "feature/test",
			RequestedHeadSHA: "head-sha",
			ExpectedBaseSHA:  "base-sha",
			Remote:           "upstream",
			Now:              fixedJournalTime("2026-06-17T10:03:00Z"),
		})
		if err != nil {
			t.Fatalf("PlanExternalProviderCommand remote delete: %v", err)
		}
		wantProvider := "git -C " + attempt.Worktree + " push upstream --delete feature/test"
		if planned.Plan.ProviderCommand != wantProvider {
			t.Fatalf("remote delete provider command = %s", planned.Plan.ProviderCommand)
		}
		if !strings.Contains(planned.Plan.ApprovalCommand, "--action remote-delete") ||
			!strings.Contains(planned.Plan.IntentCommand, "--action remote-delete") {
			t.Fatalf("remote delete plan must use separate approval/intent: %+v", planned.Plan)
		}
	})
}

func parseProviderMarker(t *testing.T, body string) ExternalProviderMarker {
	t.Helper()
	const prefix = "<!-- feature-workspace "
	const suffix = " -->"
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatalf("marker prefix missing from %q", body)
	}
	raw := body[start+len(prefix):]
	end := strings.Index(raw, suffix)
	if end < 0 {
		t.Fatalf("marker suffix missing from %q", body)
	}
	var marker ExternalProviderMarker
	if err := json.Unmarshal([]byte(raw[:end]), &marker); err != nil {
		t.Fatalf("parse marker: %v\n%s", err, raw[:end])
	}
	return marker
}
