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
	t.Parallel()

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

func TestWorkspaceRuntimeViewsDistinguishPausedBoundaryKinds(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		boundaryKind workspace.AttemptBoundaryKind
	}{
		{name: "planned checkpoint", boundaryKind: workspace.AttemptBoundaryKindCheckpoint},
		{name: "raised escalation", boundaryKind: workspace.AttemptBoundaryKindEscalation},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newAttemptHarness(t, "unit-one")
			attempt := harness.reserve(t, "2026-07-21T11:01:00Z")
			attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T11:02:00Z")
			if _, err := workspace.RecordAttemptBoundary(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.RecordAttemptBoundaryRequest{
					AttemptID: attempt.AttemptID(), Kind: test.boundaryKind,
					Evidence: boundaryEvidence(t, test.name), OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
				},
			); err != nil {
				t.Fatal(err)
			}
			snapshot, err := harness.journal.ReadSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			report, err := workspace.RebuildWorkspaceReport(snapshot, harness.definition)
			if err != nil {
				t.Fatal(err)
			}

			assertDirective := func(surface string, pending bool, directives []workspace.BoundaryDirectiveView) {
				t.Helper()
				if !pending || len(directives) != 1 {
					t.Fatalf("%s paused boundary = pending=%v directives=%+v", surface, pending, directives)
				}
				directive := directives[0]
				if directive.Kind != "owner_gate" || directive.BoundaryKind != string(test.boundaryKind) {
					t.Fatalf("%s directive = %+v", surface, directive)
				}
			}

			status := schedulerUnitByID(t, report.Scheduler, "unit-one")
			if status.Status != workspace.SchedulerUnitPaused {
				t.Fatalf("status unit = %+v", status)
			}
			assertDirective("status", status.BoundaryPending, status.PendingDirectives)
			if len(report.Attempts) != 1 || report.Attempts[0].Phase != workspace.AttemptPaused {
				t.Fatalf("report attempts = %+v", report.Attempts)
			}
			assertDirective("report", report.Attempts[0].BoundaryPending, report.Attempts[0].PendingDirectives)

			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"boundary_kind":"`+string(test.boundaryKind)+`"`) {
				t.Fatalf("report JSON does not expose %q boundary kind: %s", test.boundaryKind, encoded)
			}
		})
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
