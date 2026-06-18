package workspace

import "testing"

func TestStaleContractBindingBlocksReadyConsumer(t *testing.T) {
	fixture, claim, attempt := staleContractFixture(t, true)

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	unit := findSchedulerUnit(t, view, claim.MergeUnitID)
	if unit.Status != MergeUnitPending {
		t.Fatalf("consumer status = %q", unit.Status)
	}
	if unit.ActiveLease != nil {
		t.Fatalf("consumer should be released in fixture: %+v", unit.ActiveLease)
	}
	if len(unit.BlockedBy) != 0 {
		t.Fatalf("dependency blocked_by should be empty after producer complete: %+v", unit.BlockedBy)
	}
	if len(unit.ContractBindings) != 1 || unit.ContractBindings[0].Status != contractBindingStatusStale {
		t.Fatalf("contract bindings = %+v", unit.ContractBindings)
	}
	if len(unit.BlockingConditions) != 1 {
		t.Fatalf("blocking conditions = %+v", unit.BlockingConditions)
	}
	condition := unit.BlockingConditions[0]
	if condition.Type != "stale_contract" || condition.ContractID != "api-contract" || condition.ArtifactID != "openapi" || condition.Resource != "api-contract:openapi" {
		t.Fatalf("stale condition = %+v", condition)
	}
	if len(view.Ready) != 0 || len(view.Blocked) != 1 || view.Blocked[0] != claim.MergeUnitID {
		t.Fatalf("ready/blocked = ready %+v blocked %+v", view.Ready, view.Blocked)
	}

	next, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-other",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:13:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next.Status != "none" {
		t.Fatalf("stale consumer should not be claimable: %+v", next)
	}
	if attempt.AttemptID == "" {
		t.Fatalf("fixture attempt missing")
	}
}

func TestRebindingClearsStaleContractBlock(t *testing.T) {
	fixture, claim, attempt := staleContractFixture(t, false)
	if _, err := BindContract(ContractBindOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:12:00Z"),
	}); err != nil {
		t.Fatalf("BindContract rebind: %v", err)
	}
	if _, err := Release(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:13:00Z"),
	}); err != nil {
		t.Fatalf("Release rebind lease: %v", err)
	}

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	unit := findSchedulerUnit(t, view, claim.MergeUnitID)
	if len(unit.ContractBindings) != 1 || unit.ContractBindings[0].Status != contractBindingStatusCurrent {
		t.Fatalf("contract bindings after rebind = %+v", unit.ContractBindings)
	}
	if len(unit.BlockingConditions) != 0 {
		t.Fatalf("blocking conditions after rebind = %+v", unit.BlockingConditions)
	}
	if len(view.Ready) != 1 || view.Ready[0] != claim.MergeUnitID || len(view.Blocked) != 0 {
		t.Fatalf("ready/blocked after rebind = ready %+v blocked %+v", view.Ready, view.Blocked)
	}

	next, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-other",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:14:00Z"),
	})
	if err != nil {
		t.Fatalf("Next after rebind: %v", err)
	}
	if next.Status != "claimed" || next.MergeUnitID != claim.MergeUnitID {
		t.Fatalf("consumer should be claimable after rebind and release: %+v", next)
	}
}

func staleContractFixture(t *testing.T, releaseConsumer bool) (workspaceFixture, NextResult, AttemptResult) {
	t.Helper()
	fixture := newContractWorkspaceFixture(t)
	publishFixtureContract(t, fixture, "v1", "producer-commit-1", "2026-06-17T10:00:00Z")
	claim, attempt := startFixtureConsumerAttempt(t, fixture, "2026-06-17T10")
	if _, err := BindContract(ContractBindOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:09:00Z"),
	}); err != nil {
		t.Fatalf("BindContract initial: %v", err)
	}
	if releaseConsumer {
		if _, err := Release(LeaseOptions{
			WorkspaceDir: fixture.Dir,
			AgentID:      "worker-consumer",
			LeaseID:      claim.LeaseID,
			Now:          fixedJournalTime("2026-06-17T10:10:00Z"),
		}); err != nil {
			t.Fatalf("Release initial consumer lease: %v", err)
		}
	}
	writeContractArtifact(t, fixture.Dir, "openapi: 3.1.0\ninfo:\n  title: changed\n")
	publishFixtureContract(t, fixture, "v2", "producer-commit-2", "2026-06-17T10:11:00Z")
	return fixture, claim, attempt
}
