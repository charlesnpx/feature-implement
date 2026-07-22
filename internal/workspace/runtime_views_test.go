package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestWorkspaceRuntimeViewsReplayDeterministicallyThroughCompletion(t *testing.T) {
	scenario := newProviderCompletedScenario(t, "18", workspace.GitHashSHA1, 901)
	before, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	queued, err := workspace.RebuildWorkspaceReport(before, scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	unitOne := schedulerUnitByID(t, queued.Scheduler, "unit-one")
	unitTwo := schedulerUnitByID(t, queued.Scheduler, "unit-two")
	if unitOne.Status != workspace.SchedulerUnitActive || unitTwo.Status != workspace.SchedulerUnitBlocked {
		t.Fatalf("pre-completion scheduler = unit-one %s, unit-two %s", unitOne.Status, unitTwo.Status)
	}
	if len(queued.Queue.Ready) != 1 || queued.Queue.Ready[0].MergeUnitID != "unit-one" {
		t.Fatalf("pre-completion merge queue = %+v", queued.Queue.Ready)
	}

	receipt, _, err := scenario.verify()
	if err != nil {
		t.Fatal(err)
	}
	afterProvider, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	providerReport, err := workspace.RebuildWorkspaceReport(afterProvider, scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	unitOne = schedulerUnitByID(t, providerReport.Scheduler, "unit-one")
	unitTwo = schedulerUnitByID(t, providerReport.Scheduler, "unit-two")
	if unitOne.Status != workspace.SchedulerUnitActive || !unitOne.BoundaryPending ||
		unitOne.BoundaryReason != "completion_boundary_not_recorded" || unitOne.CompletionReceipt != receipt.Digest().String() {
		t.Fatalf("provider-completed unit hid its pending boundary: %+v", unitOne)
	}
	if unitTwo.Status != workspace.SchedulerUnitBlocked {
		t.Fatalf("dependent unit advanced before completion boundary: %+v", unitTwo)
	}

	boundary, err := workspace.RecordAttemptBoundary(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: scenario.attempt.AttemptID(), Evidence: boundaryEvidence(t, "provider-completion"),
			OccurredAt: mustTime(t, "2026-07-21T18:10:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(boundary.Directives()) != 1 {
		t.Fatalf("completion boundary directives = %#v", boundary.Directives())
	}
	pendingSnapshot, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	pendingReport, err := workspace.RebuildWorkspaceReport(pendingSnapshot, scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	unitOne = schedulerUnitByID(t, pendingReport.Scheduler, "unit-one")
	if unitOne.Status != workspace.SchedulerUnitPaused || !unitOne.BoundaryPending ||
		unitOne.BoundaryReason != "owner_gate" || len(unitOne.PendingDirectives) != 1 ||
		unitOne.PendingDirectives[0].Kind != "owner_gate" || unitOne.PendingDirectives[0].DirectiveDigest == "" {
		t.Fatalf("pending boundary directive was not replayed in report: %+v", unitOne)
	}

	projection := mustRuntime(t, scenario.harness.journal)
	requestDigest, err := workspace.OwnerBoundaryResponseRequestDigest(
		projection, scenario.attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.OwnerBoundaryResponseControlPlaneBinding(
		scenario.harness.definition, projection, scenario.attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.RecordOwnerBoundaryResponse(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		&boundaryVerifier{expectedRequest: requestDigest},
		workspace.RecordOwnerBoundaryResponseRequest{
			AttemptID: scenario.attempt.AttemptID(), Response: workspace.OwnerBoundaryContinue,
			Receipt:    controlPlaneReceipt(t, binding, "completion-boundary-nonce"),
			OccurredAt: mustTime(t, "2026-07-21T18:11:00Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ResumeAttempt(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.harness.git,
		workspace.ResumeAttemptRequest{
			AttemptID: scenario.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T18:12:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "completed attempt") {
		t.Fatalf("completed attempt resume error = %v", err)
	}

	after, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := workspace.RebuildWorkspaceReport(after, scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.RebuildWorkspaceReport(after, scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) || first.ReportDigest == "" || first.ReportDigest != second.ReportDigest {
		t.Fatalf("journal replay is not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	if first.JournalHead != after.Head().String() || first.ActiveGeneration != scenario.harness.definition.Generation().String() {
		t.Fatalf("report journal/generation binding = %+v", first)
	}

	unitOne = schedulerUnitByID(t, first.Scheduler, "unit-one")
	unitTwo = schedulerUnitByID(t, first.Scheduler, "unit-two")
	if unitOne.Status != workspace.SchedulerUnitCompleted || unitOne.LifecycleGeneration != scenario.harness.definition.Generation().String() ||
		unitOne.CompletionReceipt != receipt.Digest().String() {
		t.Fatalf("completed scheduler unit = %+v", unitOne)
	}
	if unitTwo.Status != workspace.SchedulerUnitReady || len(unitTwo.Blockers) != 0 {
		t.Fatalf("dependent scheduler unit = %+v", unitTwo)
	}
	if len(first.Completion.Completed) != 1 || first.Completion.Completed[0].ReceiptDigest != receipt.Digest().String() ||
		first.Completion.Completed[0].Generation != scenario.harness.definition.Generation().String() {
		t.Fatalf("completion view = %+v", first.Completion.Completed)
	}
	if len(first.Receipts.Receipts) == 0 {
		t.Fatal("receipt view did not index journal-backed receipts")
	}
	for _, indexed := range first.Receipts.Receipts {
		if indexed.Sequence == 0 || indexed.Digest == "" || indexed.Generation != scenario.harness.definition.Generation().String() {
			t.Fatalf("receipt is not bound to its journal generation: %+v", indexed)
		}
	}
	if len(first.Queue.Ready) != 0 {
		t.Fatalf("completed unit remained in merge queue: %+v", first.Queue.Ready)
	}
	reconciliation, err := workspace.RebuildReconciliationState(after, scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	attempts := reconciliation.Attempts()
	if len(attempts) != 1 || attempts[0].AttemptID() != scenario.attempt.AttemptID() ||
		attempts[0].Phase() != workspace.AttemptCompleted {
		t.Fatalf("completion was not reflected in reconciliation attempts: %+v", attempts)
	}
	completedGate := gateUnitByID(t, first.Gates, "unit-one")
	if completedGate.MergeReady || gateCheckByName(t, completedGate, "provider_completion").Status != workspace.GatePassed {
		t.Fatalf("completed unit gates = %+v", completedGate)
	}
}

func schedulerUnitByID(t *testing.T, view workspace.SchedulerView, id string) workspace.SchedulerUnitView {
	t.Helper()
	for _, unit := range view.Units {
		if unit.MergeUnitID == id {
			return unit
		}
	}
	t.Fatalf("scheduler has no merge unit %s: %+v", id, view.Units)
	return workspace.SchedulerUnitView{}
}

func gateUnitByID(t *testing.T, view workspace.GateView, id string) workspace.UnitGateView {
	t.Helper()
	for _, unit := range view.Units {
		if unit.MergeUnitID == id {
			return unit
		}
	}
	t.Fatalf("gate view has no merge unit %s: %+v", id, view.Units)
	return workspace.UnitGateView{}
}

func gateCheckByName(t *testing.T, unit workspace.UnitGateView, name string) workspace.GateCheckView {
	t.Helper()
	for _, check := range unit.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("gate view for %s has no check %s: %+v", unit.MergeUnitID, name, unit.Checks)
	return workspace.GateCheckView{}
}
