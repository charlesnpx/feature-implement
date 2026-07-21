package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type activationVerifier struct {
	workspaceID workspace.ID
	generation  workspace.Digest
	request     workspace.Digest
	calls       int
}

func (verifier *activationVerifier) Verify(
	_ context.Context,
	verification workspace.ControlPlaneVerification,
	receipt workspace.ControlPlaneReceiptV2,
) error {
	verifier.calls++
	if verification.WorkspaceID() != verifier.workspaceID || verification.Generation() != verifier.generation || verification.RequestDigest() != verifier.request {
		return errors.New("unexpected activation verification binding")
	}
	if verification.Binding() != receipt.Binding() {
		return errors.New("activation receipt binding mismatch")
	}
	return nil
}

func activationReceipt(
	t *testing.T,
	active workspace.EffectiveWorkspaceDefinition,
	candidate workspace.EffectiveWorkspaceDefinition,
	plan workspace.ReconciliationPlan,
	nonce string,
	expiresAt string,
) workspace.ControlPlaneReceiptV2 {
	t.Helper()
	binding, err := workspace.ReconciliationControlPlaneBinding(active, candidate, plan)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := workspace.NewControlPlaneEnvelopeV2(
		binding, workspace.MustID("owner-key"), nonce, mustTime(t, expiresAt), workspace.MustID("test-coordinator"),
	)
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	signature[0] = 1
	receipt, err := workspace.NewControlPlaneReceiptV2(envelope, signature)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestProspectiveReconciliationActivatesCandidateWithOwnerCAS(t *testing.T) {
	fixture := newDefinitionFixture(t)
	active := mustDefinition(t, fixture.sources)
	candidate := mustProspectiveCandidate(t, fixture)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, active, mustTime(t, "2026-07-21T02:00:00Z")); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := store.StageCandidate(journal, candidate, mustTime(t, "2026-07-21T02:01:00Z")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	unitOne := mustMergeUnitReference(t, "alpha-plan", "unit-one")
	unitTwo := mustMergeUnitReference(t, "alpha-plan", "unit-two")
	completedUnit, err := workspace.NewMergeUnitRuntimeState(unitOne, workspace.MergeUnitCompleted, active.Generation())
	if err != nil {
		t.Fatal(err)
	}
	futureUnit, err := workspace.NewMergeUnitRuntimeState(unitTwo, workspace.MergeUnitFuture, workspace.Digest{})
	if err != nil {
		t.Fatal(err)
	}
	terminalAttempt, err := workspace.NewAttemptGenerationBinding(
		workspace.MustID("attempt-one"), unitOne, active.Generation(), workspace.AttemptCompleted,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolvedIntent, _ := workspace.NewProviderIntentRuntimeState(workspace.MustID("intent-one"), active.Generation(), true)
	resolvedQueue, _ := workspace.NewQueueEntryRuntimeState(workspace.MustID("queue-one"), active.Generation(), true)
	history, err := workspace.NewRuntimeHistoryBinding(
		workspace.DigestBytes([]byte("budgets-consumed")),
		workspace.DigestBytes([]byte("approvals-recorded")),
		workspace.DigestBytes([]byte("historical-evidence")),
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewReconciliationState(
		snapshot,
		[]workspace.MergeUnitRuntimeState{futureUnit, completedUnit},
		[]workspace.AttemptGenerationBinding{terminalAttempt},
		[]workspace.ProviderIntentRuntimeState{resolvedIntent},
		[]workspace.QueueEntryRuntimeState{resolvedQueue},
		history,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workspace.DryRunReconciliation(active, candidate, snapshot, state)
	if err != nil {
		t.Fatal(err)
	}
	changed := plan.ChangedMergeUnits()
	if len(changed) != 1 || changed[0].String() != "alpha-plan/unit-two" || plan.ComparisonDigest().IsZero() || plan.StateDigest().IsZero() {
		t.Fatalf("reconciliation plan = %#v", plan)
	}
	if plan.JournalHead() != snapshot.Head() || plan.ActiveGeneration() != active.Generation() || plan.CandidateGeneration() != candidate.Generation() {
		t.Fatalf("reconciliation token bindings = %#v", plan)
	}
	token, err := plan.TokenBytes()
	if err != nil {
		t.Fatal(err)
	}
	parsedPlan, err := workspace.ParseReconciliationPlanToken(token)
	if err != nil || parsedPlan.ComparisonDigest() != plan.ComparisonDigest() ||
		len(parsedPlan.ChangedMergeUnits()) != 1 || parsedPlan.ChangedMergeUnits()[0] != changed[0] {
		t.Fatalf("reconciliation token round trip = %#v, %v", parsedPlan, err)
	}
	tamperedToken := append([]byte(nil), token...)
	tamperedToken[len(tamperedToken)/2] ^= 1
	if _, err := workspace.ParseReconciliationPlanToken(tamperedToken); err == nil {
		t.Fatal("tampered reconciliation token parsed successfully")
	}
	plan = parsedPlan

	changedHistory, _ := workspace.NewRuntimeHistoryBinding(
		workspace.DigestBytes([]byte("reset-budgets")), history.ApprovalDigest(), history.EvidenceDigest(),
	)
	changedState, err := workspace.NewReconciliationState(
		snapshot, state.MergeUnits(), state.Attempts(), state.ProviderIntents(), state.QueueEntries(), changedHistory,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt := activationReceipt(t, active, candidate, plan, "activation-nonce", "2026-07-21T03:00:00Z")
	verifier := &activationVerifier{
		workspaceID: active.Workspace().ID(), generation: candidate.Generation(), request: plan.ComparisonDigest(),
	}
	if _, err := workspace.ActivateCandidateGeneration(
		context.Background(), journal, store, active, candidate, plan, changedState, receipt, verifier,
		mustTime(t, "2026-07-21T02:02:00Z"),
	); err == nil || !strings.Contains(err.Error(), "runtime safety state changed") {
		t.Fatalf("history reset activation error = %v", err)
	}
	if verifier.calls != 0 {
		t.Fatal("stale history reached owner verifier")
	}

	record, err := workspace.ActivateCandidateGeneration(
		context.Background(), journal, store, active, candidate, plan, state, receipt, verifier,
		mustTime(t, "2026-07-21T02:02:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if verifier.calls != 1 || record.EventType() != workspace.JournalEventGenerationActivated || record.Generation() != candidate.Generation() {
		t.Fatalf("activation result = record %#v calls %d", record, verifier.calls)
	}
	activation, ok := record.Event().(workspace.GenerationActivatedJournalEvent)
	if !ok || activation.PriorGeneration() != active.Generation() || activation.ActiveGeneration() != candidate.Generation() ||
		activation.ComparisonDigest() != plan.ComparisonDigest() || activation.OwnerReceiptDigest() != receipt.ReceiptDigest() ||
		activation.History() != history {
		t.Fatalf("activation event = %#v", record.Event())
	}
	after, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(after)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ActiveGeneration() != candidate.Generation() || len(runtime.GenerationHistory()) != 2 || len(runtime.Activations()) != 1 {
		t.Fatalf("activated runtime = %#v", runtime)
	}
	if _, err := workspace.VerifyWorkspaceRuntimeConformance(after, candidate.Generation()); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ActivateCandidateGeneration(
		context.Background(), journal, store, active, candidate, plan, state, receipt, verifier,
		mustTime(t, "2026-07-21T02:03:00Z"),
	); err == nil || !strings.Contains(err.Error(), "journal head changed") {
		t.Fatalf("reused activation token error = %v", err)
	}

	beforeReinitialize := after.Head()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, active, mustTime(t, "2026-07-21T02:04:00Z")); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("old generation reinitialization error = %v", err)
	}
	unchanged, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir)
	if err != nil || unchanged.Head() != beforeReinitialize {
		t.Fatalf("reinitialization overwrote activated state: %v %#v", err, unchanged)
	}
}

func TestGenerationActivationRejectsOutstandingAuthorizationObligationsBeforeAppend(t *testing.T) {
	fixture := newDefinitionFixture(t)
	active := mustDefinition(t, fixture.sources)
	candidate := mustProspectiveCandidate(t, fixture)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, active, mustTime(t, "2026-07-21T02:00:00Z")); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	frontier, _ := workspace.NewAuthorizationFrontier(mustGitObject(t, 'a'), mustGitObject(t, 'b'))
	segment := workspace.MustID("serial-activation-obligation")
	scope, err := workspace.NewStandingGrantScope(workspace.StandingGrantScopeOptions{
		WorkspaceID: active.Workspace().ID(), Repository: active.Workspace().Repository(),
		Remote: active.Workspace().Remote(), Generation: active.Generation(), SerialSegment: segment,
		Frontier: frontier, Actions: []workspace.StandingAuthorizationAction{workspace.StandingAuthorizationPush},
		ExpiresAt: mustTime(t, "2026-07-21T05:00:00Z"), Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := workspace.StandingGrantControlPlaneBinding(scope)
	if _, _, err := workspace.RecordStandingGrant(
		context.Background(), journal, active, &boundaryVerifier{expectedRequest: scope.Digest()},
		scope, controlPlaneReceipt(t, binding, "activation-obligation-grant"),
		mustTime(t, "2026-07-21T02:01:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	evaluator, _ := workspace.NewAuthorizationEvaluator(
		&authorizationTestClock{now: mustTime(t, "2026-07-21T02:02:00Z")},
	)
	request := authorizationJournalRequest(t, active, segment, frontier, 1)
	queued := authorizationJournalQueue(t, journal, active, evaluator, request)
	if _, _, err := workspace.RecordAuthorizationEffectDispatched(
		journal, active, evaluator, request, queued, workspace.MustID("activation-blocking-effect"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageCandidate(journal, candidate, mustTime(t, "2026-07-21T02:03:00Z")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewReconciliationState(
		snapshot, nil, nil, nil, nil, workspace.EmptyRuntimeHistoryBinding(),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workspace.DryRunReconciliation(active, candidate, snapshot, state)
	if err != nil {
		t.Fatal(err)
	}
	receipt := activationReceipt(t, active, candidate, plan, "activation-with-obligation", "2026-07-21T04:00:00Z")
	verifier := &activationVerifier{
		workspaceID: active.Workspace().ID(), generation: candidate.Generation(), request: plan.ComparisonDigest(),
	}
	if _, err := workspace.ActivateCandidateGeneration(
		context.Background(), journal, store, active, candidate, plan, state, receipt, verifier,
		mustTime(t, "2026-07-21T02:04:00Z"),
	); err == nil || !strings.Contains(err.Error(), "dispatched effects awaiting reconciliation") {
		t.Fatalf("activation with authorization obligation error = %v", err)
	}
	if verifier.calls != 0 {
		t.Fatal("blocked activation reached owner verifier")
	}
	after, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Head() != snapshot.Head() {
		t.Fatal("blocked activation was durably appended")
	}
	authorization, err := workspace.RebuildAuthorizationRuntime(after, active)
	if err != nil || len(authorization.State().OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("authorization after blocked activation = %#v, %v", authorization, err)
	}
}

func TestActivatedCandidateCannotBeReactivated(t *testing.T) {
	fixture := newDefinitionFixture(t)
	first := mustDefinition(t, fixture.sources)
	second := mustProspectiveCandidate(t, fixture)
	thirdSources := cloneDefinitionSources(fixture.sources)
	thirdSources.Plans[0].Bytes = []byte(strings.Replace(
		string(thirdSources.Plans[0].Bytes),
		"The dependent contract is explicit.",
		"The dependent contract is explicit and independently versioned.",
		1,
	))
	third := mustDefinition(t, thirdSources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, first, mustTime(t, "2026-07-21T02:00:00Z")); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	activate := func(active, candidate workspace.EffectiveWorkspaceDefinition, stagedAt, activatedAt, nonce string) {
		t.Helper()
		if _, err := store.StageCandidate(journal, candidate, mustTime(t, stagedAt)); err != nil {
			t.Fatal(err)
		}
		snapshot, err := journal.ReadSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		state, err := workspace.NewReconciliationState(
			snapshot, nil, nil, nil, nil, workspace.EmptyRuntimeHistoryBinding(),
		)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := workspace.DryRunReconciliation(active, candidate, snapshot, state)
		if err != nil {
			t.Fatal(err)
		}
		receipt := activationReceipt(t, active, candidate, plan, nonce, activatedAt)
		verifier := &activationVerifier{
			workspaceID: active.Workspace().ID(), generation: candidate.Generation(), request: plan.ComparisonDigest(),
		}
		if _, err := workspace.ActivateCandidateGeneration(
			context.Background(), journal, store, active, candidate, plan, state, receipt, verifier,
			mustTime(t, activatedAt),
		); err != nil {
			t.Fatal(err)
		}
		if verifier.calls != 1 {
			t.Fatalf("activation verifier calls = %d", verifier.calls)
		}
	}

	activate(first, second, "2026-07-21T02:01:00Z", "2026-07-21T02:02:00Z", "second")
	activate(second, third, "2026-07-21T02:03:00Z", "2026-07-21T02:04:00Z", "third")

	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewReconciliationState(
		snapshot, nil, nil, nil, nil, workspace.EmptyRuntimeHistoryBinding(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.DryRunReconciliation(third, second, snapshot, state); err == nil ||
		!strings.Contains(err.Error(), "pending activation") {
		t.Fatalf("reactivated candidate dry-run error = %v", err)
	}
}

func TestReconciliationSafetyMatrixRejectsUnsafeRuntimeAndRetrospectiveChanges(t *testing.T) {
	fixture := newDefinitionFixture(t)
	active := mustDefinition(t, fixture.sources)
	candidate := mustProspectiveCandidate(t, fixture)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, active, mustTime(t, "2026-07-21T02:00:00Z")); err != nil {
		t.Fatal(err)
	}
	store, _ := workspace.OpenGenerationStore(workspaceDir)
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := store.StageCandidate(journal, candidate, mustTime(t, "2026-07-21T02:01:00Z")); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := journal.ReadSnapshot()
	unitOne := mustMergeUnitReference(t, "alpha-plan", "unit-one")
	unitTwo := mustMergeUnitReference(t, "alpha-plan", "unit-two")
	history := workspace.EmptyRuntimeHistoryBinding()

	for _, disposition := range []workspace.MergeUnitRuntimeDisposition{
		workspace.MergeUnitReserved, workspace.MergeUnitMaterializing, workspace.MergeUnitActive,
		workspace.MergeUnitPaused, workspace.MergeUnitReviewExhausted,
	} {
		t.Run("unit-"+string(disposition), func(t *testing.T) {
			unit, _ := workspace.NewMergeUnitRuntimeState(unitOne, disposition, active.Generation())
			state, err := workspace.NewReconciliationState(snapshot, []workspace.MergeUnitRuntimeState{unit}, nil, nil, nil, history)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil || !strings.Contains(err.Error(), "blocked") {
				t.Fatalf("unsafe unit state %s error = %v", disposition, err)
			}
		})
	}
	for _, phase := range []workspace.AttemptRuntimePhase{
		workspace.AttemptReserved, workspace.AttemptMaterializing, workspace.AttemptActive,
		workspace.AttemptPaused, workspace.AttemptReviewExhausted,
	} {
		t.Run("attempt-"+string(phase), func(t *testing.T) {
			attemptID := workspace.MustID("attempt-" + strings.ReplaceAll(string(phase), "_", "-"))
			attempt, _ := workspace.NewAttemptGenerationBinding(attemptID, unitOne, active.Generation(), phase)
			state, err := workspace.NewReconciliationState(snapshot, nil, []workspace.AttemptGenerationBinding{attempt}, nil, nil, history)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil || !strings.Contains(err.Error(), "nonterminal attempt") {
				t.Fatalf("unsafe attempt phase %s error = %v", phase, err)
			}
		})
	}
	t.Run("unresolved-provider-intent", func(t *testing.T) {
		intent, _ := workspace.NewProviderIntentRuntimeState(workspace.MustID("intent-one"), active.Generation(), false)
		state, _ := workspace.NewReconciliationState(snapshot, nil, nil, []workspace.ProviderIntentRuntimeState{intent}, nil, history)
		if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil || !strings.Contains(err.Error(), "unresolved provider intent") {
			t.Fatalf("unresolved intent error = %v", err)
		}
	})
	t.Run("unresolved-queue-entry", func(t *testing.T) {
		entry, _ := workspace.NewQueueEntryRuntimeState(workspace.MustID("queue-one"), active.Generation(), false)
		state, _ := workspace.NewReconciliationState(snapshot, nil, nil, nil, []workspace.QueueEntryRuntimeState{entry}, history)
		if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil || !strings.Contains(err.Error(), "unresolved queue entry") {
			t.Fatalf("unresolved queue error = %v", err)
		}
	})
	t.Run("retrospective-completed-unit", func(t *testing.T) {
		completed, _ := workspace.NewMergeUnitRuntimeState(unitTwo, workspace.MergeUnitCompleted, active.Generation())
		state, _ := workspace.NewReconciliationState(snapshot, []workspace.MergeUnitRuntimeState{completed}, nil, nil, nil, history)
		if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil || !strings.Contains(err.Error(), "retrospective") {
			t.Fatalf("retrospective change error = %v", err)
		}
	})
	t.Run("retrospective-terminal-attempt", func(t *testing.T) {
		attempt, _ := workspace.NewAttemptGenerationBinding(
			workspace.MustID("attempt-completed"), unitTwo, active.Generation(), workspace.AttemptCompleted,
		)
		future, _ := workspace.NewMergeUnitRuntimeState(unitTwo, workspace.MergeUnitFuture, workspace.Digest{})
		state, _ := workspace.NewReconciliationState(
			snapshot,
			[]workspace.MergeUnitRuntimeState{future},
			[]workspace.AttemptGenerationBinding{attempt},
			nil,
			nil,
			history,
		)
		if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil || !strings.Contains(err.Error(), "retrospective") {
			t.Fatalf("retrospective attempt change error = %v", err)
		}
	})
	t.Run("unknown-terminal-attempt", func(t *testing.T) {
		unknownUnit := mustMergeUnitReference(t, "alpha-plan", "unit-typo")
		attempt, err := workspace.NewAttemptGenerationBinding(
			workspace.MustID("attempt-unknown-unit"), unknownUnit, active.Generation(), workspace.AttemptCompleted,
		)
		if err != nil {
			t.Fatal(err)
		}
		state, err := workspace.NewReconciliationState(
			snapshot, nil, []workspace.AttemptGenerationBinding{attempt}, nil, nil, history,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil ||
			!strings.Contains(err.Error(), "references unknown merge unit") {
			t.Fatalf("unknown attempt merge unit error = %v", err)
		}
	})

	authoritySources := cloneDefinitionSources(fixture.sources)
	authoritySources.Workspace.Bytes = []byte(strings.Replace(
		string(authoritySources.Workspace.Bytes),
		"location: policy/owner.yaml",
		"location: policy/owner-v2.yaml",
		1,
	))
	authorityStructural := mustDefinition(t, authoritySources)
	if _, err := store.StageCandidate(journal, authorityStructural, mustTime(t, "2026-07-21T02:02:00Z")); err != nil {
		t.Fatal(err)
	}
	authoritySnapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	authorityState, err := workspace.NewReconciliationState(authoritySnapshot, nil, nil, nil, nil, history)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.DryRunReconciliation(active, authorityStructural, authoritySnapshot, authorityState); err == nil ||
		!strings.Contains(err.Error(), "structural changes") {
		t.Fatalf("authority location structural change error = %v", err)
	}

	structuralSources := cloneDefinitionSources(fixture.sources)
	structuralSources.Plans[0].Bytes = []byte(strings.Replace(
		string(structuralSources.Plans[0].Bytes),
		"    dependencies:\n      - story-one",
		"    dependencies: []",
		1,
	))
	structural := mustDefinition(t, structuralSources)
	if _, err := store.StageCandidate(journal, structural, mustTime(t, "2026-07-21T02:03:00Z")); err != nil {
		t.Fatal(err)
	}
	structuralSnapshot, _ := journal.ReadSnapshot()
	structuralState, _ := workspace.NewReconciliationState(structuralSnapshot, nil, nil, nil, nil, history)
	if _, err := workspace.DryRunReconciliation(active, structural, structuralSnapshot, structuralState); err == nil || !strings.Contains(err.Error(), "structural changes") {
		t.Fatalf("structural change error = %v", err)
	}
	safePlan, err := workspace.DryRunReconciliation(active, candidate, structuralSnapshot, structuralState)
	if err != nil {
		t.Fatal(err)
	}
	forgedPlan := mustForgeReconciliationPlan(
		t, structuralSnapshot, active, structural, safePlan.StateDigest(), safePlan.StructuralDigest(),
	)
	receipt := activationReceipt(t, active, structural, forgedPlan, "forged-nonce", "2026-07-21T03:00:00Z")
	verifier := &activationVerifier{
		workspaceID: active.Workspace().ID(), generation: structural.Generation(), request: forgedPlan.ComparisonDigest(),
	}
	if _, err := workspace.ActivateCandidateGeneration(
		context.Background(), journal, store, active, structural, forgedPlan, structuralState, receipt, verifier,
		mustTime(t, "2026-07-21T02:04:00Z"),
	); err == nil || !strings.Contains(err.Error(), "structural changes") {
		t.Fatalf("forged structural activation error = %v", err)
	}
	if verifier.calls != 0 {
		t.Fatal("forged structural plan reached owner verification")
	}
}

func TestCandidateOrphanRecoveryAndStaleComparisonToken(t *testing.T) {
	fixture := newDefinitionFixture(t)
	active := mustDefinition(t, fixture.sources)
	candidate := mustProspectiveCandidate(t, fixture)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, active, mustTime(t, "2026-07-21T02:00:00Z")); err != nil {
		t.Fatal(err)
	}
	store, _ := workspace.OpenGenerationStore(workspaceDir)
	if _, err := store.Store(candidate); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	snapshot, _ := journal.ReadSnapshot()
	orphans, err := store.OrphanCandidates(snapshot)
	if err != nil || len(orphans) != 1 || orphans[0] != candidate.Generation() {
		t.Fatalf("orphan candidates = %v, %v", orphans, err)
	}
	if _, err := store.RecoverOrphanCandidate(journal, candidate.Generation(), mustTime(t, "2026-07-21T02:01:00Z")); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = journal.ReadSnapshot()
	orphans, err = store.OrphanCandidates(snapshot)
	if err != nil || len(orphans) != 0 {
		t.Fatalf("orphans after recovery = %v, %v", orphans, err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil || len(runtime.Candidates()) != 1 || !runtime.Candidates()[0].Recovered() {
		t.Fatalf("recovered candidate projection = %#v, %v", runtime, err)
	}
	state, _ := workspace.NewReconciliationState(snapshot, nil, nil, nil, nil, workspace.EmptyRuntimeHistoryBinding())
	plan, err := workspace.DryRunReconciliation(active, candidate, snapshot, state)
	if err != nil {
		t.Fatal(err)
	}

	secondSources := cloneDefinitionSources(fixture.sources)
	secondSources.Plans[0].Bytes = []byte(strings.Replace(
		string(secondSources.Plans[0].Bytes), "The first contract is explicit.", "The first contract is exact.", 1,
	))
	secondCandidate := mustDefinition(t, secondSources)
	if _, err := store.StageCandidate(journal, secondCandidate, mustTime(t, "2026-07-21T02:02:00Z")); err != nil {
		t.Fatal(err)
	}
	receipt := activationReceipt(t, active, candidate, plan, "nonce", "2026-07-21T03:00:00Z")
	verifier := &activationVerifier{workspaceID: active.Workspace().ID(), generation: candidate.Generation(), request: plan.ComparisonDigest()}
	if _, err := workspace.ActivateCandidateGeneration(
		context.Background(), journal, store, active, candidate, plan, state, receipt, verifier, mustTime(t, "2026-07-21T02:03:00Z"),
	); err == nil || !strings.Contains(err.Error(), "journal head changed") {
		t.Fatalf("stale comparison token error = %v", err)
	}
	if verifier.calls != 0 {
		t.Fatal("stale head reached owner verifier")
	}
}

func TestGenerationStoreDetectsCanonicalTampering(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Store(definition)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Store(definition)
	if err != nil || !strings.EqualFold(stored.Generation().String(), second.Generation().String()) {
		t.Fatalf("idempotent store = %#v, %v", second, err)
	}
	entries, err := os.ReadDir(workspace.WorkspaceGenerationsDirectory(workspaceDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("generation entries = %v, %v", entries, err)
	}
	path := filepath.Join(workspace.WorkspaceGenerationsDirectory(workspaceDir), entries[0].Name())
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)/2] ^= 1
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(definition.Generation()); err == nil {
		t.Fatal("tampered generation loaded successfully")
	}
}

func TestOrphanRecoveryRejectsGenerationFromAnotherWorkspace(t *testing.T) {
	fixture := newDefinitionFixture(t)
	active := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(
		workspaceDir, active, mustTime(t, "2026-07-21T02:00:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	foreignSources := cloneDefinitionSources(fixture.sources)
	foreignSources.Workspace.Bytes = []byte(strings.Replace(
		string(foreignSources.Workspace.Bytes),
		"id: example-workspace",
		"id: foreign-workspace",
		1,
	))
	foreign := mustDefinition(t, foreignSources)
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store(foreign); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := store.RecoverOrphanCandidate(
		journal, foreign.Generation(), mustTime(t, "2026-07-21T02:01:00Z"),
	); err == nil || !strings.Contains(err.Error(), "belongs to workspace foreign-workspace") {
		t.Fatalf("foreign orphan recovery error = %v", err)
	}
}

func mustProspectiveCandidate(t *testing.T, fixture definitionFixture) workspace.EffectiveWorkspaceDefinition {
	t.Helper()
	sources := cloneDefinitionSources(fixture.sources)
	sources.Plans[0].Bytes = []byte(strings.Replace(
		string(sources.Plans[0].Bytes),
		"The dependent contract is explicit.",
		"The dependent contract is explicit and versioned.",
		1,
	))
	return mustDefinition(t, sources)
}

func mustMergeUnitReference(t *testing.T, planID, mergeUnitID string) workspace.MergeUnitReference {
	t.Helper()
	reference, err := workspace.NewMergeUnitReference(workspace.MustID(planID), workspace.MustID(mergeUnitID))
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func mustForgeReconciliationPlan(
	t *testing.T,
	snapshot workspace.JournalSnapshot,
	active, candidate workspace.EffectiveWorkspaceDefinition,
	stateDigest, structuralDigest workspace.Digest,
) workspace.ReconciliationPlan {
	t.Helper()
	type changedUnit struct {
		PlanID      string `json:"plan_id"`
		MergeUnitID string `json:"merge_unit_id"`
	}
	type comparison struct {
		SchemaVersion       int           `json:"schema_version"`
		WorkspaceID         string        `json:"workspace_id"`
		ActiveGeneration    string        `json:"active_generation"`
		CandidateGeneration string        `json:"candidate_generation"`
		JournalHead         string        `json:"journal_head"`
		StateDigest         string        `json:"state_digest"`
		StructuralDigest    string        `json:"structural_digest"`
		WorkspaceRevision   uint64        `json:"workspace_revision"`
		CandidateRevision   uint64        `json:"candidate_revision"`
		ChangedMergeUnits   []changedUnit `json:"changed_merge_units"`
	}
	value := comparison{
		SchemaVersion:       2,
		WorkspaceID:         active.Workspace().ID().String(),
		ActiveGeneration:    active.Generation().String(),
		CandidateGeneration: candidate.Generation().String(),
		JournalHead:         snapshot.Head().String(),
		StateDigest:         stateDigest.String(),
		StructuralDigest:    structuralDigest.String(),
		WorkspaceRevision:   snapshot.Revision(workspace.WorkspaceJournalResource(active.Workspace().ID())),
		CandidateRevision:   snapshot.Revision(workspace.GenerationJournalResource(candidate.Generation())),
		ChangedMergeUnits:   []changedUnit{},
	}
	comparisonBytes, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	tokenBytes, err := json.Marshal(struct {
		SchemaVersion    int             `json:"schema_version"`
		ComparisonDigest string          `json:"comparison_digest"`
		Comparison       json.RawMessage `json:"comparison"`
	}{
		SchemaVersion:    2,
		ComparisonDigest: workspace.DigestBytes(comparisonBytes).String(),
		Comparison:       json.RawMessage(comparisonBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workspace.ParseReconciliationPlanToken(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

var _ workspace.ControlPlaneVerifierPort = (*activationVerifier)(nil)
