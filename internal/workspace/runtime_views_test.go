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

	harness := newGatedReviewHarness(t)
	harness.dispatch(t, "2026-07-21T18:00:00Z")
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := workspace.RebuildWorkspaceView(
		snapshot, harness.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.RebuildWorkspaceView(
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
	coreDigest, err := workspace.VerifyWorkspaceRuntimeConformance(
		snapshot, harness.definition.Generation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	reviewDigest, err := workspace.VerifyReviewRuntimeConformance(
		snapshot, harness.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Workflow.ProjectionDigest != coreDigest.String() ||
		first.Workflow.ReviewProjectionDigest != reviewDigest.String() {
		t.Fatalf("workspace view projection digests = %+v", first.Workflow)
	}
	var consumer struct {
		Scheduler struct {
			Units []struct {
				MergeUnitID string   `json:"merge_unit_id"`
				Blockers    []string `json:"blockers"`
			} `json:"units"`
		} `json:"scheduler"`
	}
	if err := json.Unmarshal(firstJSON, &consumer); err != nil {
		t.Fatalf("consumer decoding string blockers: %v", err)
	}
	var blockedBlockers, unblockedBlockers []string
	foundUnblocked := false
	for _, unit := range consumer.Scheduler.Units {
		switch unit.MergeUnitID {
		case "unit-one":
			unblockedBlockers = unit.Blockers
			foundUnblocked = true
		case "unit-two":
			blockedBlockers = unit.Blockers
		}
	}
	if len(blockedBlockers) != 1 ||
		!strings.Contains(blockedBlockers[0], "[alpha-plan/unit-one]") ||
		!foundUnblocked ||
		len(unblockedBlockers) != 0 {
		t.Fatalf(
			"consumer string blockers = blocked=%+v unblocked=%+v",
			blockedBlockers, unblockedBlockers,
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
		attempt.BoundaryPending {
		t.Fatalf("attempt view = %+v", attempt)
	}
	if len(first.Reviews) != 1 {
		t.Fatalf("review views = %+v", first.Reviews)
	}
	review := first.Reviews[0]
	if review.AttemptID != harness.attempt.AttemptID().String() ||
		review.Head != harness.base.String() ||
		review.Tree != harness.tree.String() ||
		review.Status != "dispatched" ||
		review.DispatchDigest == "" || review.Adapter != "natural-language" ||
		review.Recipe != "default" || review.PolicyDigest == "" ||
		review.Verdict != "" || review.EvidenceDigest != "" {
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
	if len(unitTwo.Blockers) != 1 ||
		!strings.Contains(unitTwo.Blockers[0], "unsatisfied dependency sets") ||
		!strings.Contains(unitTwo.Blockers[0], "[alpha-plan/unit-one]") {
		t.Fatalf("blocked unit does not name its unsatisfied dependency set: %+v", unitTwo.Blockers)
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

func TestConfiguredCommitGateResolvesAtDurableIntegrationIntent(t *testing.T) {
	t.Parallel()

	core := newAttemptHarnessFromFixture(t, configuredCommitProtocolFixture(t), "unit-one")
	first := core.reserve(t, "2026-07-21T18:10:00Z")
	repository := adoptedIntegrationRepository(
		t, core, first, mustGitObject(t, 'c'), mustGitObject(t, 'd'), "2026-07-21T18:10:01Z",
	)
	snapshot, err := core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspace.RebuildWorkspaceView(snapshot, core.definition)
	if err != nil {
		t.Fatal(err)
	}
	if gate := gateCheckByName(t, gateUnitByID(t, before.Gates, "unit-one"), "commit"); gate.Status != workspace.GatePending || gate.Reason != "final_history_validated_at_integration" {
		t.Fatalf("pre-integration commit gate = %#v", gate)
	}
	git := &integrationGitStub{featureHead: core.base}
	if _, err := workspace.CompleteWorkspace(
		context.Background(), core.journal, core.definition, git,
		workspace.CompleteWorkspaceRequest{OccurredAt: mustTime(t, "2026-07-21T18:10:02Z")},
	); err == nil {
		t.Fatal("completion was claimable while the configured commit gate was pending")
	}
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(), core.journal, core.definition, repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: first.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T18:10:03Z"),
			Fault: failIntegrationOnce(workspace.IntegrationFaultAfterIntentSynced),
		},
	); err == nil || !strings.Contains(err.Error(), "after_intent_synced") {
		t.Fatalf("integration-intent fault = %v", err)
	}
	snapshot, err = core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	intentView, err := workspace.RebuildWorkspaceView(snapshot, core.definition)
	if err != nil {
		t.Fatal(err)
	}
	if gate := gateCheckByName(t, gateUnitByID(t, intentView.Gates, "unit-one"), "commit"); gate.Status != workspace.GatePassed || gate.Reason != "final_history_validated_for_integration" {
		t.Fatalf("durable-intent commit gate = %#v", gate)
	}
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(), core.journal, core.definition, repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: first.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T18:10:04Z"),
		},
	); err != nil {
		t.Fatal(err)
	}

	secondCore := core
	secondCore.unit = mustMergeUnitReference(t, "alpha-plan", "unit-two")
	secondCore.goal, err = workspace.NewGoalBinding(
		workspace.MustID("commit-gate-second-goal"), workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	second := secondCore.reserve(t, "2026-07-21T18:10:05Z")
	secondRepository := adoptedIntegrationRepository(
		t, secondCore, second, mustGitObject(t, 'e'), mustGitObject(t, 'f'), "2026-07-21T18:10:06Z",
	)
	git.expectedCommit = false
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(), secondCore.journal, secondCore.definition, secondRepository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: second.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T18:10:07Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err = secondCore.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	readyToComplete, err := workspace.RebuildWorkspaceView(snapshot, secondCore.definition)
	if err != nil {
		t.Fatal(err)
	}
	if gate := gateCheckByName(t, gateUnitByID(t, readyToComplete.Gates, "unit-one"), "commit"); gate.Status != workspace.GatePassed {
		t.Fatalf("completion-ready commit gate = %#v", gate)
	}
	if _, err := workspace.CompleteWorkspace(
		context.Background(), secondCore.journal, secondCore.definition, git,
		workspace.CompleteWorkspaceRequest{OccurredAt: mustTime(t, "2026-07-21T18:10:08Z")},
	); err != nil {
		t.Fatalf("completion with passed configured commit gate: %v", err)
	}
}

func TestWorkspaceReviewViewSerializationMatchesGateSchema(t *testing.T) {
	t.Parallel()

	harness := newGatedReviewHarness(t)
	harness.dispatch(t, "2026-07-21T18:00:00Z")
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	view, err := workspace.RebuildWorkspaceView(snapshot, harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Reviews) != 1 {
		t.Fatalf("review views = %#v", view.Reviews)
	}
	encoded, err := json.Marshal(view.Reviews[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{
		"attempt_id": {}, "plan_id": {}, "merge_unit_id": {}, "generation": {},
		"dispatch_digest": {}, "adapter": {}, "recipe": {}, "policy_digest": {},
		"head": {}, "tree": {}, "status": {},
	}
	if len(fields) != len(want) {
		t.Fatalf("gate review JSON fields = %#v, want %#v", fields, want)
	}
	for field := range want {
		if _, exists := fields[field]; !exists {
			t.Fatalf("gate review JSON omits %q: %#v", field, fields)
		}
	}
}

func TestWorkspaceViewRetainsZeroGenerationConformanceError(t *testing.T) {
	t.Parallel()

	_, err := workspace.RebuildWorkspaceView(
		workspace.JournalSnapshot{}, workspace.EffectiveWorkspaceDefinition{},
	)
	if err == nil || !strings.Contains(err.Error(), "replay conformance requires") {
		t.Fatalf("zero-generation workspace view error = %v", err)
	}
}

func TestWorkspaceRuntimeViewsProjectPausedBoundaryKinds(
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
			snapshot, err := harness.journal.ReadSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			active, err := workspace.RebuildWorkspaceView(snapshot, harness.definition)
			if err != nil {
				t.Fatal(err)
			}
			assertNoDirective := func(surface string, pending bool, directives []workspace.WorkspaceBoundaryDirective) {
				t.Helper()
				if pending || directives == nil || len(directives) != 0 {
					t.Fatalf("%s active boundary = pending=%v directives=%+v", surface, pending, directives)
				}
			}
			status := schedulerUnitByID(t, active.Scheduler, "unit-one")
			assertNoDirective("status", status.BoundaryPending, status.PendingDirectives)
			if len(active.Attempts) != 1 {
				t.Fatalf("active attempts = %+v", active.Attempts)
			}
			assertNoDirective("report", active.Attempts[0].BoundaryPending, active.Attempts[0].PendingDirectives)
			if _, err := workspace.PauseAttempt(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.PauseAttemptRequest{
					AttemptID: attempt.AttemptID(), Kind: test.boundaryKind,
					Evidence: boundaryEvidence(t, test.name), OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
				},
			); err != nil {
				t.Fatal(err)
			}
			snapshot, err = harness.journal.ReadSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			report, err := workspace.RebuildWorkspaceView(snapshot, harness.definition)
			if err != nil {
				t.Fatal(err)
			}

			assertDirective := func(
				surface string,
				pending bool,
				reason string,
				directives []workspace.WorkspaceBoundaryDirective,
			) {
				t.Helper()
				if !pending || reason != string(test.boundaryKind) || len(directives) != 1 {
					t.Fatalf(
						"%s paused boundary = pending=%v reason=%q directives=%+v",
						surface, pending, reason, directives,
					)
				}
				directive := directives[0]
				if directive.Kind != "boundary_pending" || directive.BoundaryKind != string(test.boundaryKind) {
					t.Fatalf("%s directive = %+v", surface, directive)
				}
			}

			status = schedulerUnitByID(t, report.Scheduler, "unit-one")
			if status.Status != workspace.SchedulerUnitPaused {
				t.Fatalf("status unit = %+v", status)
			}
			assertDirective("status", status.BoundaryPending, status.BoundaryReason, status.PendingDirectives)
			if len(report.Attempts) != 1 || report.Attempts[0].Phase != workspace.AttemptPaused {
				t.Fatalf("report attempts = %+v", report.Attempts)
			}
			assertDirective(
				"report",
				report.Attempts[0].BoundaryPending,
				report.Attempts[0].BoundaryReason,
				report.Attempts[0].PendingDirectives,
			)

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
	view workspace.WorkspaceSchedule,
	id string,
) workspace.WorkspaceUnitState {
	t.Helper()
	for _, unit := range view.Units {
		if unit.MergeUnitID == id {
			return unit
		}
	}
	t.Fatalf("scheduler has no merge unit %s: %+v", id, view.Units)
	return workspace.WorkspaceUnitState{}
}

func gateUnitByID(
	t *testing.T,
	view workspace.WorkspaceGates,
	id string,
) workspace.WorkspaceUnitGates {
	t.Helper()
	for _, unit := range view.Units {
		if unit.MergeUnitID == id {
			return unit
		}
	}
	t.Fatalf("gate view has no merge unit %s: %+v", id, view.Units)
	return workspace.WorkspaceUnitGates{}
}

func gateCheckByName(
	t *testing.T,
	unit workspace.WorkspaceUnitGates,
	name string,
) workspace.WorkspaceGate {
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
	return workspace.WorkspaceGate{}
}

func integrationUnitByID(
	t *testing.T,
	view workspace.WorkspaceIntegration,
	id string,
) workspace.WorkspaceIntegrationUnit {
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
	return workspace.WorkspaceIntegrationUnit{}
}
