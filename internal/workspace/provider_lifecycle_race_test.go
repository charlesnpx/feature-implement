package workspace_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type providerPushScenario struct {
	harness   attemptHarness
	attempt   workspace.RuntimeAttemptProjection
	frontier  workspace.AuthorizationFrontier
	grant     workspace.StandingGrant
	clock     *authorizationTestClock
	evaluator *workspace.AuthorizationEvaluator
	intent    workspace.ProviderIntent
	adapter   *providerLifecycleAdapter
	broker    *workspace.ProviderBroker
}

func newProviderPushScenario(t *testing.T, hour string) providerPushScenario {
	t.Helper()
	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T"+hour+":01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T"+hour+":02:00Z")
	frontier, err := workspace.NewAuthorizationFrontier(attempt.Base(), attempt.VerifiedHead())
	if err != nil {
		t.Fatal(err)
	}
	grant := recordProviderLifecycleGrant(
		t, harness, attempt, frontier,
		[]workspace.StandingAuthorizationAction{workspace.StandingAuthorizationPush},
		"2026-07-21T"+hour+":03:00Z",
	)
	clock := &authorizationTestClock{now: mustTime(t, "2026-07-21T"+hour+":04:00Z")}
	evaluator, err := workspace.NewAuthorizationEvaluator(clock)
	if err != nil {
		t.Fatal(err)
	}
	intent := providerPushIntent(t, harness, attempt, frontier)
	if _, _, err := workspace.ReserveProviderIntent(
		harness.journal, harness.definition, evaluator,
		workspace.ReserveProviderIntentRequest{
			Intent: intent, OccurredAt: mustTime(t, "2026-07-21T"+hour+":04:01Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	adapter := &providerLifecycleAdapter{}
	broker, err := workspace.NewProviderBroker(harness.definition.Workspace().Provider(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	return providerPushScenario{
		harness: harness, attempt: attempt, frontier: frontier, grant: grant,
		clock: clock, evaluator: evaluator, intent: intent, adapter: adapter, broker: broker,
	}
}

func (scenario *providerPushScenario) authorize(t *testing.T, hour string) workspace.ProviderDispatchTicket {
	t.Helper()
	scenario.clock.now = mustTime(t, "2026-07-21T"+hour+":05:00Z")
	ticket, err := workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, scenario.intent.IntentID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestProviderRevocationBetweenReservationAndDispatchPreventsBrokerCapability(t *testing.T) {
	scenario := newProviderPushScenario(t, "14")
	options := workspace.AuthorizationRevocationOptions{
		WorkspaceID: scenario.harness.definition.Workspace().ID(),
		Repository:  scenario.harness.definition.Workspace().Repository(),
		Remote:      scenario.harness.definition.Workspace().Remote(),
		Generation:  scenario.harness.definition.Generation(),
		TargetGrant: scenario.grant.GrantID(), NextEpoch: 2,
		Reason: workspace.MustID("owner-revoked-provider-dispatch"),
	}
	binding, err := workspace.AuthorizationRevocationControlPlaneBinding(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspace.RecordAuthorizationRevocation(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		&boundaryVerifier{expectedRequest: binding.RequestDigest()}, options,
		controlPlaneReceipt(t, binding, "provider-revocation-before-dispatch"),
		mustTime(t, "2026-07-21T14:04:30Z"),
	); err != nil {
		t.Fatal(err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T14:05:00Z")
	ticket, err := workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, scenario.intent.IntentID(),
	)
	if err == nil || !ticket.CapabilityDigest().IsZero() {
		t.Fatalf("revoked reservation produced broker capability %#v, err=%v", ticket, err)
	}
	if scenario.adapter.pushCalls != 0 {
		t.Fatalf("provider adapter called after pre-dispatch revocation: %d", scenario.adapter.pushCalls)
	}
	projection, err := workspace.RebuildProviderRuntime(mustJournalSnapshot(t, scenario.harness.journal), scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	reserved, _ := projection.Intent(scenario.intent.IntentID())
	if reserved.Status() != workspace.ProviderIntentReserved {
		t.Fatalf("revoked intent state = %s, want reserved", reserved.Status())
	}
}

func TestProviderDistinctReservedIntentsRaceAtDurableDispatchCAS(t *testing.T) {
	scenario := newProviderPushScenario(t, "15")
	second, err := workspace.NewProviderPushIntent(workspace.ProviderPushIntentOptions{
		Scope:  providerIntentScope(scenario.harness, scenario.attempt, scenario.frontier, workspace.PullRequestIdentity{}),
		Branch: scenario.attempt.Branch(), ExpectedRemoteHead: scenario.attempt.Base(),
		Head: scenario.attempt.VerifiedHead(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.IntentID() == scenario.intent.IntentID() {
		t.Fatal("distinct queue-race intent has the same identity")
	}
	if _, _, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: second, OccurredAt: mustTime(t, "2026-07-21T15:04:02Z")},
	); err != nil {
		t.Fatal(err)
	}
	scenario.clock.now = mustTime(t, "2026-07-21T15:05:00Z")
	type dispatchOutcome struct {
		intent workspace.ID
		ticket workspace.ProviderDispatchTicket
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan dispatchOutcome, 2)
	var wait sync.WaitGroup
	for _, intentID := range []workspace.ID{scenario.intent.IntentID(), second.IntentID()} {
		wait.Add(1)
		go func(id workspace.ID) {
			defer wait.Done()
			<-start
			ticket, dispatchErr := workspace.AuthorizeProviderIntentDispatch(
				scenario.harness.journal, scenario.harness.definition, scenario.evaluator, id,
			)
			outcomes <- dispatchOutcome{intent: id, ticket: ticket, err: dispatchErr}
		}(intentID)
	}
	close(start)
	wait.Wait()
	close(outcomes)
	var winner dispatchOutcome
	failed := 0
	for outcome := range outcomes {
		if outcome.err == nil {
			if !winner.intent.IsZero() {
				t.Fatalf("two provider intents crossed the serial dispatch CAS: %s and %s", winner.intent, outcome.intent)
			}
			winner = outcome
		} else {
			failed++
		}
	}
	if winner.intent.IsZero() || failed != 1 {
		t.Fatalf("dispatch race winner=%s failed=%d", winner.intent, failed)
	}
	state, _, err := workspace.ReadAuthorizationEvaluationSnapshot(scenario.harness.journal, scenario.harness.definition)
	if err != nil || len(state.OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("dispatch race obligations=%#v err=%v", state.OutstandingReconciliationObligations(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if execution, err := workspace.ExecuteProviderIntent(
		ctx, scenario.harness.journal, scenario.harness.definition, scenario.broker, winner.ticket,
		mustTime(t, "2026-07-21T15:05:01Z"),
	); err == nil || execution.Result().Status() != workspace.ProviderIntentFailedBeforeEffect {
		t.Fatalf("failed-before-effect race settlement = %#v err=%v", execution, err)
	}
	loser := scenario.intent.IntentID()
	if loser == winner.intent {
		loser = second.IntentID()
	}
	scenario.clock.now = mustTime(t, "2026-07-21T15:06:00Z")
	if ticket, err := workspace.AuthorizeProviderIntentDispatch(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator, loser,
	); err != nil || ticket.CapabilityDigest().IsZero() {
		t.Fatalf("settled queue did not release the losing reservation: %#v err=%v", ticket, err)
	}
}

func TestProviderFailedAfterEffectRequiresTypedQueryReconciliation(t *testing.T) {
	scenario := newProviderPushScenario(t, "16")
	ticket := scenario.authorize(t, "16")
	failure, err := workspace.NewProviderAdapterFailure(
		workspace.ProviderAdapterFailedAfterEffect,
		"push-timeout-after-provider-accepted",
		errors.New("timeout after provider accepted request"),
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.adapter.pushErr = failure
	execution, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker, ticket,
		mustTime(t, "2026-07-21T16:05:01Z"),
	)
	if err == nil || execution.Result().Status() != workspace.ProviderIntentFailedAfterEffect {
		t.Fatalf("failed-after-effect outcome = %#v err=%v", execution, err)
	}
	state, _, _ := workspace.ReadAuthorizationEvaluationSnapshot(scenario.harness.journal, scenario.harness.definition)
	if len(state.OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("failed-after-effect lost reconciliation obligation: %#v", state.OutstandingReconciliationObligations())
	}
	observation, _ := workspace.NewProviderReconciliationObservation(workspace.ProviderReconciliationObservationOptions{
		Disposition: workspace.ProviderEffectApplied, RequestMarker: "query-failed-after-effect",
		RemoteHead: scenario.attempt.VerifiedHead(),
	})
	scenario.adapter.reconciliation = observation
	reconciled, err := workspace.ReconcileProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker,
		scenario.intent.IntentID(), mustTime(t, "2026-07-21T16:06:00Z"),
	)
	if err != nil || reconciled.Projection().Status() != workspace.ProviderIntentReconciled {
		t.Fatalf("failed-after-effect reconciliation = %#v err=%v", reconciled, err)
	}
}

func TestProviderUnknownQueryKeepsObligationAndAllowsLaterDefinitiveRetry(t *testing.T) {
	scenario := newProviderPushScenario(t, "17")
	ticket := scenario.authorize(t, "17")
	scenario.adapter.pushErr = errors.New("connection lost with unknown provider outcome")
	if execution, err := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker, ticket,
		mustTime(t, "2026-07-21T17:05:01Z"),
	); err == nil || execution.Result().Status() != workspace.ProviderIntentAmbiguous {
		t.Fatalf("ambiguous dispatch = %#v err=%v", execution, err)
	}
	unknown, _ := workspace.NewProviderReconciliationObservation(workspace.ProviderReconciliationObservationOptions{
		Disposition: workspace.ProviderEffectUnknown, RequestMarker: "query-still-unknown",
	})
	scenario.adapter.reconciliation = unknown
	if _, err := workspace.ReconcileProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker,
		scenario.intent.IntentID(), mustTime(t, "2026-07-21T17:06:00Z"),
	); err == nil || !strings.Contains(err.Error(), "remains ambiguous") {
		t.Fatalf("unknown query did not remain blocked: %v", err)
	}
	state, _, _ := workspace.ReadAuthorizationEvaluationSnapshot(scenario.harness.journal, scenario.harness.definition)
	if len(state.OutstandingReconciliationObligations()) != 1 {
		t.Fatalf("unknown query cleared durable obligation: %#v", state.OutstandingReconciliationObligations())
	}
	applied, _ := workspace.NewProviderReconciliationObservation(workspace.ProviderReconciliationObservationOptions{
		Disposition: workspace.ProviderEffectApplied, RequestMarker: "query-later-applied",
		RemoteHead: scenario.attempt.VerifiedHead(),
	})
	scenario.adapter.reconciliation = applied
	if reconciled, err := workspace.ReconcileProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker,
		scenario.intent.IntentID(), mustTime(t, "2026-07-21T17:07:00Z"),
	); err != nil || reconciled.Projection().Status() != workspace.ProviderIntentReconciled ||
		scenario.adapter.queryIntentCalls != 2 {
		t.Fatalf("definitive retry = %#v calls=%d err=%v", reconciled, scenario.adapter.queryIntentCalls, err)
	}
}

func TestProviderCrashAfterDurableDispatchBeforeBrokerInvocationReconciles(t *testing.T) {
	scenario := newProviderPushScenario(t, "18")
	_ = scenario.authorize(t, "18") // Simulate process loss of the opaque in-memory ticket.
	if scenario.adapter.pushCalls != 0 {
		t.Fatalf("provider invoked before simulated crash: %d", scenario.adapter.pushCalls)
	}
	projection, err := workspace.RebuildProviderRuntime(mustJournalSnapshot(t, scenario.harness.journal), scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, _ := projection.Intent(scenario.intent.IntentID())
	if dispatched.Status() != workspace.ProviderIntentDispatched || !dispatched.NeedsReconciliation() {
		t.Fatalf("post-crash durable state = %#v", dispatched)
	}
	notApplied, _ := workspace.NewProviderReconciliationObservation(workspace.ProviderReconciliationObservationOptions{
		Disposition: workspace.ProviderEffectNotApplied, RequestMarker: "query-crash-before-broker",
	})
	scenario.adapter.reconciliation = notApplied
	if reconciled, err := workspace.ReconcileProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker,
		scenario.intent.IntentID(), mustTime(t, "2026-07-21T18:06:00Z"),
	); err != nil || reconciled.Projection().Status() != workspace.ProviderIntentReconciled {
		t.Fatalf("crash-before-broker reconciliation = %#v err=%v", reconciled, err)
	}
}

func TestProviderCrashAfterEffectBeforeResultRecordLeavesDurableReconciliation(t *testing.T) {
	scenario := newProviderPushScenario(t, "19")
	ticket := scenario.authorize(t, "19")
	push, _ := workspace.NewProviderPushAdapterResult("push-effect-before-crash", scenario.attempt.VerifiedHead())
	scenario.adapter.pushResult = push
	if err := scenario.harness.journal.Close(); err != nil {
		t.Fatal(err)
	}
	faulted := false
	faulty, err := workspace.OpenWorkspaceJournalWithOptions(
		scenario.harness.workspace, workspace.JournalReadWrite,
		workspace.JournalOptions{FaultInjector: func(point workspace.JournalFaultPoint) error {
			if point == workspace.JournalFaultBeforeAppend && !faulted {
				faulted = true
				return errors.New("simulated crash before provider result append")
			}
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ExecuteProviderIntent(
		context.Background(), faulty, scenario.harness.definition, scenario.broker, ticket,
		mustTime(t, "2026-07-21T19:05:01Z"),
	); err == nil || !strings.Contains(err.Error(), string(workspace.JournalFaultBeforeAppend)) {
		t.Fatalf("result append crash error = %v", err)
	}
	if err := faulty.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.OpenWorkspaceJournal(scenario.harness.workspace, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	scenario.harness.journal = reopened
	if scenario.adapter.pushCalls != 1 {
		t.Fatalf("provider effect calls = %d, want 1", scenario.adapter.pushCalls)
	}
	replay, replayErr := workspace.ExecuteProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker, ticket,
		mustTime(t, "2026-07-21T19:05:02Z"),
	)
	if replayErr == nil || replay.Result().Status() != workspace.ProviderIntentAmbiguous || scenario.adapter.pushCalls != 1 {
		t.Fatalf("consumed broker capability replay = %#v calls=%d err=%v", replay, scenario.adapter.pushCalls, replayErr)
	}
	projection, err := workspace.RebuildProviderRuntime(mustJournalSnapshot(t, scenario.harness.journal), scenario.harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, _ := projection.Intent(scenario.intent.IntentID())
	if dispatched.Status() != workspace.ProviderIntentAmbiguous || !dispatched.NeedsReconciliation() {
		t.Fatalf("crash-after-effect durable state = %#v", dispatched)
	}
	applied, _ := workspace.NewProviderReconciliationObservation(workspace.ProviderReconciliationObservationOptions{
		Disposition: workspace.ProviderEffectApplied, RequestMarker: "query-crash-after-effect",
		RemoteHead: scenario.attempt.VerifiedHead(),
	})
	scenario.adapter.reconciliation = applied
	if reconciled, err := workspace.ReconcileProviderIntent(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.broker,
		scenario.intent.IntentID(), mustTime(t, "2026-07-21T19:06:00Z"),
	); err != nil || reconciled.Projection().Status() != workspace.ProviderIntentReconciled {
		t.Fatalf("crash-after-effect reconciliation = %#v err=%v", reconciled, err)
	}
}

func TestProviderIntentIdentityAndReservationAreIdempotent(t *testing.T) {
	scenario := newProviderPushScenario(t, "13")
	identical := providerPushIntent(t, scenario.harness, scenario.attempt, scenario.frontier)
	if identical.IntentID() != scenario.intent.IntentID() ||
		identical.IdempotencyKey() != scenario.intent.IdempotencyKey() ||
		identical.Digest() != scenario.intent.Digest() {
		t.Fatalf("identical provider intents changed canonical identity: %#v %#v", scenario.intent, identical)
	}
	projection, record, err := workspace.ReserveProviderIntent(
		scenario.harness.journal, scenario.harness.definition, scenario.evaluator,
		workspace.ReserveProviderIntentRequest{Intent: identical, OccurredAt: mustTime(t, "2026-07-21T13:04:02Z")},
	)
	if err != nil || projection.Status() != workspace.ProviderIntentReserved || record.Sequence() != 0 {
		t.Fatalf("idempotent reservation = %#v record=%d err=%v", projection, record.Sequence(), err)
	}
}

func mustJournalSnapshot(t *testing.T, journal *workspace.WorkspaceJournal) workspace.JournalSnapshot {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
