package workspace

import "testing"

func TestStatusReportsBlockersByTypeAndRequiredAction(t *testing.T) {
	fixture, claim, _ := staleContractFixture(t, true)

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	group := findBlockerGroup(t, status.Blockers, "stale_contract", "bind_contract")
	if group.Count != 1 || len(group.MergeUnits) != 1 || group.MergeUnits[0] != claim.MergeUnitID {
		t.Fatalf("stale contract group = %+v", group)
	}
	condition := group.Conditions[0]
	if condition.ContractID != "api-contract" || condition.ArtifactID != "openapi" || condition.MergeUnitID != claim.MergeUnitID {
		t.Fatalf("stale contract condition = %+v", condition)
	}
}

func TestRecoverReportsActionsAndRemainingBlockers(t *testing.T) {
	fixture, claim, _ := staleContractFixture(t, true)
	recovered, err := Recover(RecoverOptions{
		WorkspaceDir: fixture.Dir,
		Now:          fixedJournalTime("2026-06-17T10:12:00Z"),
	})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if recovered.Status != "unchanged" || len(recovered.Actions) != 0 {
		t.Fatalf("recover actions = %+v", recovered)
	}
	group := findBlockerGroup(t, recovered.RemainingBlockers, "stale_contract", "bind_contract")
	if group.Count != 1 || len(recovered.Blocked) != 1 || recovered.Blocked[0] != claim.MergeUnitID {
		t.Fatalf("recover blockers = blocked %+v group %+v", recovered.Blocked, group)
	}

	leaseFixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, leaseFixture.Dir)
	claimResult, err := Next(NextOptions{
		WorkspaceDir:  leaseFixture.Dir,
		AgentID:       "worker-a",
		Claim:         true,
		LeaseDuration: 1,
		Now:           fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	recovered, err = Recover(RecoverOptions{
		WorkspaceDir: leaseFixture.Dir,
		Now:          fixedJournalTime("2026-06-17T10:00:02Z"),
	})
	if err != nil {
		t.Fatalf("Recover expired lease: %v", err)
	}
	if len(recovered.Actions) != 1 ||
		recovered.Actions[0].Type != RecoveryActionRecoveredLease ||
		recovered.Actions[0].LeaseID != claimResult.LeaseID ||
		recovered.Actions[0].Status != "recovered" {
		t.Fatalf("recovered actions = %+v", recovered.Actions)
	}
}

func TestStatusReportsOperatorReconciledExternalIntent(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")
	if _, err := Release(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := ReconcileExternalIntent(ExternalIntentReconcileOptions{
		WorkspaceDir: fixture.Dir,
		IntentID:     reserved.Intent.IntentID,
		Operator:     "operator-a",
		Details:      "operator confirmed outcome",
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	}); err != nil {
		t.Fatalf("ReconcileExternalIntent: %v", err)
	}

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	report := findExternalIntentReport(t, status.ExternalIntents, reserved.Intent.IntentID)
	if report.ResultSource != ExternalIntentResultSourceOperator ||
		report.ResultStatus != ExternalResultReconciledByOperator ||
		report.Operator != "operator-a" ||
		!report.Accepted {
		t.Fatalf("external intent report = %+v", report)
	}
}

func TestBlockerGroupsUseFrozenResourcesWithoutDuplicateSchedulerCondition(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")

	view, err := rebuildSchedulerViewAt(fixture.Dir, fixedJournalTime("2026-06-17T10:03:30Z")())
	if err != nil {
		t.Fatalf("rebuildSchedulerViewAt: %v", err)
	}
	unit := findSchedulerUnit(t, view, claim.MergeUnitID)
	if len(unit.BlockingConditions) != 1 || unit.BlockingConditions[0].Type != "frozen_resource" {
		t.Fatalf("scheduler blocking conditions = %+v", unit.BlockingConditions)
	}

	group := findBlockerGroup(t, workspaceBlockerGroups(view), "frozen_resource", externalIntentFreezeActionRecordResult)
	if group.Count != 3 || len(group.Conditions) != 3 {
		t.Fatalf("frozen resource group = %+v", group)
	}
	assertBlockerResources(t, group.Conditions,
		MergeUnitResource(claim.MergeUnitID),
		ProviderTargetResource("push:branch:feature/test"),
		RemoteRefResource("feature/test"),
	)
	for _, condition := range group.Conditions {
		if condition.IntentID != reserved.Intent.IntentID || condition.AttemptID != attempt.AttemptID {
			t.Fatalf("frozen resource condition = %+v", condition)
		}
	}
}

func TestAmbiguousPolicyAcceptedExternalIntentReportsProviderSource(t *testing.T) {
	fixture, claim, attempt, approval := newExternalIntentFixture(t, ExternalActionPush)
	reserved := reserveExternalIntentForTest(t, fixture, claim, attempt, approval, "feature/test", "head-sha", "2026-06-17T10:03:00Z")
	if _, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir:   fixture.Dir,
		MergeUnitID:    claim.MergeUnitID,
		AttemptID:      attempt.AttemptID,
		AgentID:        "worker-a",
		LeaseID:        claim.LeaseID,
		IntentID:       reserved.Intent.IntentID,
		Status:         ExternalResultAmbiguous,
		PolicyAccepted: true,
		Details:        "provider result",
		Now:            fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("RecordExternalIntentResult: %v", err)
	}

	events := readTestJournalEvents(t, fixture.Dir)
	activeLeases, err := activeLeaseSnapshots(events, fixedJournalTime("2026-06-17T10:04:30Z")())
	if err != nil {
		t.Fatalf("activeLeaseSnapshots: %v", err)
	}
	reports, err := externalIntentReports(events, activeLeases)
	if err != nil {
		t.Fatalf("externalIntentReports: %v", err)
	}
	report := findExternalIntentReport(t, reports, reserved.Intent.IntentID)
	if report.ResultSource != ExternalIntentResultSourceProvider ||
		report.ResultStatus != ExternalResultAmbiguous ||
		report.RequiredAction != externalIntentFreezeActionOperatorReconcile ||
		report.Accepted ||
		!report.PolicyAccepted {
		t.Fatalf("ambiguous external intent report = %+v", report)
	}
}

func findBlockerGroup(t *testing.T, groups []WorkspaceBlockerGroup, groupType string, requiredAction string) WorkspaceBlockerGroup {
	t.Helper()
	for _, group := range groups {
		if group.Type == groupType && group.RequiredAction == requiredAction {
			return group
		}
	}
	t.Fatalf("blocker group %s/%s missing from %+v", groupType, requiredAction, groups)
	return WorkspaceBlockerGroup{}
}

func findExternalIntentReport(t *testing.T, reports []ExternalIntentReport, intentID string) ExternalIntentReport {
	t.Helper()
	for _, report := range reports {
		if report.IntentID == intentID {
			return report
		}
	}
	t.Fatalf("external intent %s missing from %+v", intentID, reports)
	return ExternalIntentReport{}
}

func assertBlockerResources(t *testing.T, conditions []WorkspaceBlockerView, resources ...string) {
	t.Helper()
	seen := map[string]int{}
	for _, condition := range conditions {
		seen[condition.Resource]++
	}
	for _, resource := range resources {
		if seen[resource] != 1 {
			t.Fatalf("resource %s count = %d in %+v", resource, seen[resource], conditions)
		}
		delete(seen, resource)
	}
	if len(seen) > 0 {
		t.Fatalf("unexpected resources in blocker conditions: %+v", seen)
	}
}
