package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestWorkspaceRuntimeViewsExposeOnlyLocalStateAndReplayDeterministically(
	t *testing.T,
) {
	harness := newReviewHarness(t)
	if _, err := workspace.StartAttemptReviewRound(
		context.Background(),
		harness.journal,
		harness.definition,
		harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-21T18:00:00Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := workspace.RebuildWorkspaceReport(
		snapshot, harness.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.RebuildWorkspaceReport(
		snapshot, harness.definition,
	)
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
	if !bytes.Equal(firstJSON, secondJSON) ||
		first.ReportDigest == "" ||
		first.ReportDigest != second.ReportDigest {
		t.Fatalf(
			"journal replay is not deterministic:\n%s\n%s",
			firstJSON, secondJSON,
		)
	}
	for _, forbidden := range []string{
		`"provider`, `"receipt`, `"authorization`,
		`"queue`, `"reconciliation`, `"remote`,
	} {
		if strings.Contains(string(firstJSON), forbidden) {
			t.Fatalf(
				"local report exposes removed field %q:\n%s",
				forbidden, firstJSON,
			)
		}
	}

	if first.Workflow.WorkspaceID !=
		harness.definition.Workspace().ID().String() ||
		first.Workflow.Generation !=
			harness.definition.Generation().String() ||
		first.Workflow.JournalHead != snapshot.Head().String() ||
		first.Workflow.WorktreeRoot == "" ||
		first.Workflow.ProjectionDigest == "" ||
		first.Workflow.ReviewProjectionDigest == "" {
		t.Fatalf("workflow view = %+v", first.Workflow)
	}
	if !first.Target.Ready ||
		first.Target.Root !=
			harness.definition.Workspace().RepositoryRoot() ||
		first.Target.BaseCommit != harness.base.String() ||
		first.Target.FeatureHead != harness.base.String() ||
		first.Target.BindingDigest == "" {
		t.Fatalf("target view = %+v", first.Target)
	}
	if len(first.Attempts) != 1 {
		t.Fatalf("attempt views = %+v", first.Attempts)
	}
	attempt := first.Attempts[0]
	if attempt.AttemptID != harness.attempt.AttemptID().String() ||
		attempt.PlanID != "alpha-plan" ||
		attempt.MergeUnitID != "unit-one" ||
		attempt.Base != harness.base.String() ||
		attempt.Phase != workspace.AttemptActive ||
		attempt.BoundaryPending ||
		len(attempt.PendingDirectives) != 0 {
		t.Fatalf("attempt view = %+v", attempt)
	}
	if len(first.Reviews) != 1 {
		t.Fatalf("review views = %+v", first.Reviews)
	}
	review := first.Reviews[0]
	if review.AttemptID != harness.attempt.AttemptID().String() ||
		review.Head != harness.base.String() ||
		review.Tree != harness.tree.String() ||
		review.Status != "active" ||
		review.RoundsUsed != 1 ||
		review.MergeReady {
		t.Fatalf("review view = %+v", review)
	}

	unitOne := schedulerUnitByID(t, first.Scheduler, "unit-one")
	unitTwo := schedulerUnitByID(t, first.Scheduler, "unit-two")
	if unitOne.Status != workspace.SchedulerUnitActive ||
		unitOne.AttemptID != harness.attempt.AttemptID().String() ||
		unitTwo.Status != workspace.SchedulerUnitBlocked {
		t.Fatalf(
			"scheduler = unit-one %+v, unit-two %+v",
			unitOne, unitTwo,
		)
	}
	gate := gateUnitByID(t, first.Gates, "unit-one")
	if gateCheckByName(t, gate, "integration").Status !=
		workspace.GatePending {
		t.Fatalf("integration gate = %+v", gate)
	}
	for _, check := range gate.Checks {
		if check.Name == "provider_completion" ||
			check.Name == "authorization_safety" {
			t.Fatalf("gate exposes removed state: %+v", check)
		}
	}
	if unit := integrationUnitByID(
		t, first.Integration, "unit-one",
	); unit.Status != "pending" ||
		unit.AttemptID != harness.attempt.AttemptID().String() {
		t.Fatalf("integration view = %+v", unit)
	}
	if first.Drift.Detected ||
		len(first.Drift.Reasons) != 0 ||
		first.Completion.Complete ||
		len(first.Completion.Blockers) == 0 {
		t.Fatalf(
			"drift/completion views = %+v / %+v",
			first.Drift, first.Completion,
		)
	}
}

func TestWorkspaceRuntimeViewsDoNotTreatProviderCompletionAsLocalIntegration(
	t *testing.T,
) {
	scenario := newProviderCompletedScenario(
		t, "19", workspace.GitHashSHA1, 101,
	)
	receipt, _, err := scenario.verify()
	if err != nil || receipt.Digest().IsZero() {
		t.Fatalf("record provider completion = %#v, %v", receipt, err)
	}
	boundaryResult, err := workspace.RecordAttemptBoundary(
		context.Background(),
		scenario.harness.journal,
		scenario.harness.definition,
		scenario.harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: scenario.attempt.AttemptID(),
			Evidence:  boundaryEvidence(t, "provider-completion-is-not-local-integration"),
			OccurredAt: mustTime(
				t, "2026-07-21T19:10:00Z",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	boundary := boundaryResult.Boundary()
	if _, err := workspace.RecordOwnerBoundaryResponse(
		scenario.harness.journal,
		scenario.harness.definition,
		workspace.RecordOwnerBoundaryResponseRequest{
			AttemptID:       scenario.attempt.AttemptID(),
			BoundaryID:      boundary.BoundaryID(),
			DirectiveDigest: boundary.DirectiveDigest(),
			Goal:            boundary.Goal(),
			ExpectedHead:    boundary.Head(),
			Response:        workspace.OwnerBoundaryContinue,
			OccurredAt:      mustTime(t, "2026-07-21T19:11:00Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	report, err := workspace.RebuildWorkspaceReport(
		snapshot, scenario.harness.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	unitOne := schedulerUnitByID(t, report.Scheduler, "unit-one")
	unitTwo := schedulerUnitByID(t, report.Scheduler, "unit-two")
	if unitOne.Status != workspace.SchedulerUnitPaused ||
		unitTwo.Status != workspace.SchedulerUnitBlocked ||
		len(unitTwo.Blockers) == 0 {
		t.Fatalf(
			"provider completion changed local scheduler state: unit-one=%+v unit-two=%+v",
			unitOne, unitTwo,
		)
	}
	integrationGate := gateCheckByName(
		t, gateUnitByID(t, report.Gates, "unit-one"), "integration",
	)
	if integrationGate.Status != workspace.GatePending ||
		integrationGate.Reason != "not_integrated" {
		t.Fatalf(
			"provider completion passed local integration gate: %+v",
			integrationGate,
		)
	}
	if integrationUnitByID(
		t, report.Integration, "unit-one",
	).Status != "pending" ||
		report.Completion.Complete {
		t.Fatalf(
			"provider completion changed local integration/completion: %+v / %+v",
			report.Integration, report.Completion,
		)
	}
	resumed, err := workspace.ResumeAttempt(
		context.Background(),
		scenario.harness.journal,
		scenario.harness.definition,
		scenario.harness.git,
		workspace.ResumeAttemptRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-21T19:12:00Z"),
		},
	)
	if err != nil || resumed.Phase() != workspace.AttemptActive {
		t.Fatalf(
			"provider completion changed local resume: phase=%s err=%v",
			resumed.Phase(),
			err,
		)
	}
}

func schedulerUnitByID(
	t *testing.T,
	view workspace.SchedulerView,
	id string,
) workspace.SchedulerUnitView {
	t.Helper()
	for _, unit := range view.Units {
		if unit.MergeUnitID == id {
			return unit
		}
	}
	t.Fatalf("scheduler has no merge unit %s: %+v", id, view.Units)
	return workspace.SchedulerUnitView{}
}

func gateUnitByID(
	t *testing.T,
	view workspace.GateView,
	id string,
) workspace.UnitGateView {
	t.Helper()
	for _, unit := range view.Units {
		if unit.MergeUnitID == id {
			return unit
		}
	}
	t.Fatalf("gate view has no merge unit %s: %+v", id, view.Units)
	return workspace.UnitGateView{}
}

func gateCheckByName(
	t *testing.T,
	unit workspace.UnitGateView,
	name string,
) workspace.GateCheckView {
	t.Helper()
	for _, check := range unit.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf(
		"gate view for %s has no check %s: %+v",
		unit.MergeUnitID, name, unit.Checks,
	)
	return workspace.GateCheckView{}
}

func integrationUnitByID(
	t *testing.T,
	view workspace.IntegrationView,
	id string,
) workspace.IntegrationUnitView {
	t.Helper()
	for _, unit := range view.Units {
		if unit.MergeUnitID == id {
			return unit
		}
	}
	t.Fatalf(
		"integration view has no merge unit %s: %+v",
		id, view.Units,
	)
	return workspace.IntegrationUnitView{}
}
