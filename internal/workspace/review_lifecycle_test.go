package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type reviewRepositoryStub struct {
	snapshot         workspace.ReviewRepositorySnapshot
	sequence         []workspace.ReviewRepositorySnapshot
	err              error
	calls            int
	finalHistoryErr  error
	finalHistory     func(int) error
	finalHistoryRuns int
}

func (repository *reviewRepositoryStub) InspectReviewSnapshot(
	context.Context,
	workspace.ReviewRepositoryRequest,
) (workspace.ReviewRepositorySnapshot, error) {
	repository.calls++
	if repository.err != nil {
		return workspace.ReviewRepositorySnapshot{}, repository.err
	}
	if len(repository.sequence) != 0 {
		next := repository.sequence[0]
		repository.sequence = repository.sequence[1:]
		return next, nil
	}
	return repository.snapshot, nil
}

func (repository *reviewRepositoryStub) VerifyFinalHistory(
	context.Context,
	workspace.CommitProtocol,
	string,
	workspace.GitObjectID,
	workspace.GitObjectID,
) error {
	repository.finalHistoryRuns++
	if repository.finalHistory != nil {
		return repository.finalHistory(repository.finalHistoryRuns)
	}
	return repository.finalHistoryErr
}

type reviewRunnerStub struct {
	run func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error)
}

func (runner reviewRunnerStub) RunReview(
	_ context.Context,
	invocation workspace.ReviewInvocation,
) (workspace.ReviewRunnerOutput, error) {
	return runner.run(invocation)
}

type reviewHarness struct {
	attemptHarness
	attempt    workspace.RuntimeAttemptProjection
	repository *reviewRepositoryStub
	tree       workspace.GitObjectID
}

func TestReviewConfigurationPreservesLoopOrderAndRejectsUnsafeSchemas(t *testing.T) {
	t.Parallel()

	fixture := configuredReviewFixture(t)
	config, err := workspace.DecodeExecutionConfig(fixture.sources.ExecutionConfig.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	profiles := config.ReviewProfiles()
	if len(profiles) != 2 || profiles[0].ID().String() != "correctness" ||
		profiles[1].ID().String() != "security" {
		t.Fatalf("canonical review profiles = %#v", profiles)
	}
	loop, configured := configuredUnitExecution(t, mustDefinition(t, fixture.sources)).ReviewLoop()
	if !configured {
		t.Fatal("review loop is missing")
	}
	ordered := loop.Profiles()
	if len(ordered) != 2 || ordered[0].ID().String() != "security" ||
		ordered[1].ID().String() != "correctness" ||
		ordered[0].ReviewerPolicy() != workspace.ReviewReviewerRetain ||
		ordered[1].ReviewerPolicy() != workspace.ReviewReviewerFreshEachInvocation {
		t.Fatalf("configured review order = %#v", ordered)
	}
	if loop.MaxRounds() != 3 || loop.MaxInfrastructureRetries() != 2 ||
		loop.Digest().IsZero() {
		t.Fatalf("review loop budgets/digest = %#v", loop)
	}

	valid := string(fixture.sources.ExecutionConfig.Bytes)
	tests := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{
			name: "unknown profile",
			mutate: func(value string) string {
				return strings.Replace(value, "        - correctness\n", "        - unavailable\n", 1)
			},
			want: "unknown review profile",
		},
		{
			name: "duplicate ordered profile",
			mutate: func(value string) string {
				return strings.Replace(value, "        - correctness\n", "        - security\n", 1)
			},
			want: "duplicates review profile",
		},
		{
			name: "zero retry budget",
			mutate: func(value string) string {
				return strings.Replace(value, "max_infrastructure_retries: 2", "max_infrastructure_retries: 0", 1)
			},
			want: "positive infrastructure retry budget",
		},
		{
			name: "unsupported reviewer policy",
			mutate: func(value string) string {
				return strings.Replace(value, "reviewer_policy: retain", "reviewer_policy: rotate_sometimes", 1)
			},
			want: "unsupported",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := workspace.DecodeExecutionConfig([]byte(test.mutate(valid)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("configuration error = %v, want %q", err, test.want)
			}
		})
	}

}

func TestReviewResultRejectsAggregatePayloadThatCannotFitJournal(t *testing.T) {
	t.Parallel()

	findings := make([]workspace.ReviewFinding, 0, 128)
	for index := 0; index < 128; index++ {
		finding, err := workspace.NewReviewFinding(workspace.ReviewFindingOptions{
			Severity: workspace.ReviewSeverityLow, Category: workspace.MustID("journal-bound"),
			Summary:        fmt.Sprintf("%03d:%s", index, strings.Repeat("x", 8188)),
			EvidenceDigest: workspace.DigestBytes([]byte(fmt.Sprintf("oversized-finding-%d", index))),
		})
		if err != nil {
			t.Fatal(err)
		}
		findings = append(findings, finding)
	}
	if _, err := workspace.NewReviewResultSubmission(workspace.ReviewResultSubmissionOptions{
		RequestDigest:    workspace.DigestBytes([]byte("oversized-review-request")),
		ReviewerInstance: workspace.MustID("oversized-reviewer"), Status: workspace.ReviewResultCompleted,
		Findings: findings, Isolation: workspace.StrictReviewIsolationProof(),
	}); err == nil || !strings.Contains(err.Error(), "aggregate safe journal bound") {
		t.Fatalf("oversized aggregate review result error = %v", err)
	}
}

func TestReviewReducerEnforcesOrderedProfilesExactHeadAndReviewerIdentity(t *testing.T) {
	t.Parallel()

	definition := mustDefinition(t, configuredReviewFixture(t).sources)
	state := startDomainReview(t, reviewLoopForDefinition(t, definition), definition.Generation())

	first, ok, err := state.NextRequest()
	if err != nil || !ok || first.Profile().ID().String() != "security" ||
		first.ProfileOrdinal() != 1 || first.Invocation() != 1 {
		t.Fatalf("first request = %#v ok=%v err=%v", first, ok, err)
	}
	state = reduceReviewSubmission(
		t, state, first, workspace.MustID("security-one"), workspace.ReviewResultInfrastructureFailure,
		nil, workspace.DigestBytes([]byte("security unavailable")),
	)
	if state.RoundsUsed() != 1 || state.InfrastructureRetriesUsed() != 0 {
		t.Fatalf("initial infrastructure failure consumed substantive budget: %#v", state)
	}
	retry, ok, err := state.NextRequest()
	if err != nil || !ok || retry.Profile().ID() != first.Profile().ID() || retry.Invocation() != 2 ||
		retry.Round() != first.Round() || retry.Head() != first.Head() || retry.Tree() != first.Tree() {
		t.Fatalf("retained-profile retry = %#v ok=%v err=%v", retry, ok, err)
	}
	wrongReservation, err := workspace.NewReserveReviewInvocation(
		retry, workspace.MustID("security-two"), workspace.DigestBytes([]byte("wrong-retained-invocation")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReduceReview(state, wrongReservation); err == nil || !strings.Contains(err.Error(), "retain") {
		t.Fatalf("changed retained reviewer error = %v", err)
	}
	state = reduceReviewSubmission(
		t, state, retry, workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)

	second, ok, err := state.NextRequest()
	if err != nil || !ok || second.Profile().ID().String() != "correctness" ||
		second.ProfileOrdinal() != 2 || second.Head() != first.Head() || second.Tree() != first.Tree() {
		t.Fatalf("second ordered request = %#v ok=%v err=%v", second, ok, err)
	}
	state = reduceReviewSubmission(
		t, state, second, workspace.MustID("correctness-one"), workspace.ReviewResultInfrastructureFailure,
		nil, workspace.DigestBytes([]byte("correctness unavailable")),
	)
	freshRetry, _, _ := state.NextRequest()
	wrongFreshReservation, err := workspace.NewReserveReviewInvocation(
		freshRetry, workspace.MustID("correctness-one"), workspace.DigestBytes([]byte("wrong-fresh-invocation")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReduceReview(state, wrongFreshReservation); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("reused fresh reviewer error = %v", err)
	}
	state = reduceReviewSubmission(
		t, state, freshRetry, workspace.MustID("correctness-two"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	if !state.MergeReady() || state.InfrastructureRetriesUsed() != 2 {
		t.Fatalf("completed exact-head round = %#v", state)
	}

	changedTree := mustGitObject(t, 'c')
	wrongHeadStart, err := workspace.NewStartReviewRound(
		state.WorkspaceID(), state.Generation(), state.AttemptID(), state.MergeUnit(), state.Loop(),
		2, state.Head(), changedTree,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReduceReview(state, wrongHeadStart); err == nil || !strings.Contains(err.Error(), "exact current head/tree") {
		t.Fatalf("same-head violation error = %v", err)
	}

	alternate := configuredReviewFixture(t)
	alternate.sources.ExecutionConfig.Bytes = []byte(strings.Replace(
		string(alternate.sources.ExecutionConfig.Bytes),
		"max_infrastructure_retries: 2",
		"max_infrastructure_retries: 1",
		1,
	))
	alternateLoop := reviewLoopForDefinition(t, mustDefinition(t, alternate.sources))
	reset, err := workspace.NewStartReviewRound(
		state.WorkspaceID(), state.Generation(), state.AttemptID(), state.MergeUnit(), alternateLoop,
		2, state.Head(), state.Tree(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReduceReview(state, reset); err == nil || !strings.Contains(err.Error(), "cannot reset") {
		t.Fatalf("configuration counter reset error = %v", err)
	}
}

func TestReviewReducerSeparatesInfrastructureAndRoundBudgets(t *testing.T) {
	t.Parallel()

	definition := mustDefinition(t, configuredReviewFixture(t).sources)
	loop := reviewLoopForDefinition(t, definition)

	t.Run("infrastructure retries resume one profile", func(t *testing.T) {
		state := startDomainReview(t, loop, definition.Generation())
		for invocation := 1; invocation <= 3; invocation++ {
			request, ok, err := state.NextRequest()
			if err != nil || !ok || int(request.Invocation()) != invocation || request.Profile().ID().String() != "security" {
				t.Fatalf("request %d = %#v ok=%v err=%v", invocation, request, ok, err)
			}
			state = reduceReviewSubmission(
				t, state, request, workspace.MustID("security-one"), workspace.ReviewResultInfrastructureFailure,
				nil, workspace.DigestBytes([]byte(fmt.Sprintf("failure-%d", invocation))),
			)
			if invocation < 3 {
				if _, exhausted := state.Exhaustion(); exhausted {
					t.Fatalf("infrastructure budget exhausted after invocation %d", invocation)
				}
			}
		}
		reason, exhausted := state.Exhaustion()
		if !exhausted || reason != workspace.ReviewExhaustedInfrastructure ||
			state.InfrastructureRetriesUsed() != 2 || state.RoundsUsed() != 1 {
			t.Fatalf("infrastructure exhaustion = %q exhausted=%v state=%#v", reason, exhausted, state)
		}
	})

	t.Run("rounds are substantive and bounded", func(t *testing.T) {
		state := startDomainReview(t, loop, definition.Generation())
		for round := 1; round <= 3; round++ {
			state, _ = completeBlockingDomainRound(t, state, round)
			if round < 3 {
				if _, exhausted := state.Exhaustion(); exhausted {
					t.Fatalf("round budget exhausted at %d", round)
				}
				state = startNextDomainReviewRound(t, state, state.Head(), state.Tree())
			}
		}
		reason, exhausted := state.Exhaustion()
		if !exhausted || reason != workspace.ReviewExhaustedRounds || state.RoundsUsed() != 3 {
			t.Fatalf("round exhaustion = %q exhausted=%v state=%#v", reason, exhausted, state)
		}
	})

}

func TestReviewRoundBudgetRejectsFourthBlockingReviewAcrossHeadChanges(t *testing.T) {
	t.Parallel()

	harness := newReviewHarness(t)
	headTrees := []struct {
		head workspace.GitObjectID
		tree workspace.GitObjectID
	}{
		{head: harness.attempt.VerifiedHead(), tree: harness.tree},
		{head: mustGitObject(t, 'c'), tree: mustGitObject(t, 'd')},
		{head: mustGitObject(t, 'e'), tree: mustGitObject(t, 'f')},
	}
	for index, headTree := range headTrees {
		snapshot, err := workspace.NewReviewRepositorySnapshot(
			headTree.head, headTree.tree, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		harness.repository.snapshot = snapshot
		started, err := workspace.StartAttemptReviewRound(
			context.Background(), harness.journal, harness.definition, harness.repository,
			workspace.StartAttemptReviewRoundRequest{
				AttemptID:  harness.attempt.AttemptID(),
				OccurredAt: mustTime(t, fmt.Sprintf("2026-07-21T12:00:%02dZ", index*3+1)),
			},
		)
		if err != nil {
			t.Fatalf("start blocking review round %d: %v", index+1, err)
		}
		finding := mustReviewFinding(
			t, workspace.ReviewSeverityHigh,
			fmt.Sprintf("blocking review round %d", index+1),
		)
		first := reviewSubmission(
			t, started.Request(), workspace.MustID("security-one"),
			workspace.ReviewResultCompleted, []workspace.ReviewFinding{finding}, workspace.Digest{},
		)
		harness.record(
			t, started.Request(), first,
			fmt.Sprintf("2026-07-21T12:00:%02dZ", index*3+2),
		)
		state := mustReviewState(
			t, harness.journal, harness.definition, harness.attempt.AttemptID(),
		)
		second, ok, err := state.NextRequest()
		if err != nil || !ok || second.Profile().ID().String() != "correctness" {
			t.Fatalf("second request for review round %d = %#v ok=%v err=%v", index+1, second, ok, err)
		}
		secondSubmission := reviewSubmission(
			t, second, workspace.MustID(fmt.Sprintf("correctness-%d", index+1)),
			workspace.ReviewResultCompleted, nil, workspace.Digest{},
		)
		harness.record(
			t, second, secondSubmission,
			fmt.Sprintf("2026-07-21T12:00:%02dZ", index*3+3),
		)
	}

	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := workspace.RebuildReviewRuntime(snapshot, harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	if reviews.RoundsUsed(harness.attempt.AttemptID()) != 3 {
		t.Fatalf("aggregate review rounds = %d, want 3", reviews.RoundsUsed(harness.attempt.AttemptID()))
	}
	state, exists := reviews.State(harness.attempt.AttemptID())
	reason, exhausted := state.Exhaustion()
	if !exists || !exhausted || reason != workspace.ReviewExhaustedRounds {
		t.Fatalf("cross-head review exhaustion = %q exists=%v exhausted=%v", reason, exists, exhausted)
	}
	view, err := workspace.RebuildWorkspaceView(snapshot, harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Reviews) != 1 || view.Reviews[0].RoundsUsed != 3 || view.Reviews[0].Status != "exhausted" {
		t.Fatalf("operator review view = %#v", view.Reviews)
	}

	fourth, err := workspace.NewReviewRepositorySnapshot(
		mustGitObject(t, '1'), mustGitObject(t, '2'), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	harness.repository.snapshot = fourth
	_, err = workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:00:10Z"),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "review round budget exhausted") ||
		!strings.Contains(err.Error(), "max_review_rounds=3 per attempt") {
		t.Fatalf("fourth review round error = %v", err)
	}
	after, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Records()) != len(snapshot.Records()) {
		t.Fatalf("exhausted fourth review changed the journal: before=%d after=%d", len(snapshot.Records()), len(after.Records()))
	}
}

func TestReviewLifecycleVerifiesLocalExactHeadEvidenceAndBoundaryReadiness(t *testing.T) {
	t.Parallel()

	harness := newReviewHarness(t)
	start, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:00:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if start.Request().Head() != harness.attempt.VerifiedHead() || start.Request().Tree() != harness.tree ||
		start.Request().Profile().ID().String() != "security" {
		t.Fatalf("first lifecycle request = %#v", start.Request())
	}
	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: harness.attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "too-early"),
			OccurredAt: mustTime(t, "2026-07-21T11:00:01Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "every configured review profile") {
		t.Fatalf("premature boundary error = %v", err)
	}

	firstSubmission := reviewSubmission(
		t, start.Request(), workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	otherSubmission := reviewSubmission(
		t, start.Request(), workspace.MustID("security-two"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	firstReservation := harness.reserve(
		t, start.Request(), firstSubmission.ReviewerInstance(), "2026-07-21T11:00:02Z",
	)
	if _, _, err := workspace.RecordAttemptReviewResult(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.RecordAttemptReviewResultRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: firstReservation.Digest(),
			Submission: otherSubmission,
			OccurredAt: mustTime(t, "2026-07-21T11:00:02Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "does not match pending request") {
		t.Fatalf("misbound local result error = %v", err)
	}
	wrongRequestSubmission, err := workspace.NewReviewResultSubmission(
		workspace.ReviewResultSubmissionOptions{
			RequestDigest:    workspace.DigestBytes([]byte("different-review-invocation")),
			ReviewerInstance: firstSubmission.ReviewerInstance(),
			Status:           workspace.ReviewResultCompleted,
			Isolation:        workspace.StrictReviewIsolationProof(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := workspace.RecordAttemptReviewResult(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.RecordAttemptReviewResultRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: firstReservation.Digest(),
			Submission: wrongRequestSubmission,
			OccurredAt: mustTime(t, "2026-07-21T11:00:02Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "does not match pending request") {
		t.Fatalf("mismatched review invocation error = %v", err)
	}
	harness.record(t, start.Request(), firstSubmission, "2026-07-21T11:00:03Z")

	state := mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	second, ok, err := state.NextRequest()
	if err != nil || !ok || second.Profile().ID().String() != "correctness" ||
		second.Head() != start.Request().Head() || second.Tree() != start.Request().Tree() {
		t.Fatalf("second lifecycle request = %#v ok=%v err=%v", second, ok, err)
	}
	secondSubmission := reviewSubmission(
		t, second, workspace.MustID("correctness-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	secondReservation := harness.reserve(
		t, second, secondSubmission.ReviewerInstance(), "2026-07-21T11:00:04Z",
	)
	wrongHead, _ := workspace.NewReviewRepositorySnapshot(
		mustGitObject(t, 'c'), harness.tree, true,
	)
	harness.repository.snapshot = wrongHead
	if _, _, err := workspace.RecordAttemptReviewResult(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.RecordAttemptReviewResultRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: secondReservation.Digest(),
			Submission: secondSubmission,
			OccurredAt: mustTime(t, "2026-07-21T11:00:04Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "exact clean head/tree") {
		t.Fatalf("stale-head result error = %v", err)
	}
	wrongTree, _ := workspace.NewReviewRepositorySnapshot(
		harness.attempt.VerifiedHead(), mustGitObject(t, 'd'), true,
	)
	harness.repository.snapshot = wrongTree
	if _, _, err := workspace.RecordAttemptReviewResult(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.RecordAttemptReviewResultRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: secondReservation.Digest(),
			Submission: secondSubmission,
			OccurredAt: mustTime(t, "2026-07-21T11:00:04Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "exact clean head/tree") {
		t.Fatalf("stale-tree result error = %v", err)
	}
	harness.repository.snapshot, _ = workspace.NewReviewRepositorySnapshot(harness.attempt.VerifiedHead(), harness.tree, true)
	harness.record(t, second, secondSubmission, "2026-07-21T11:00:05Z")

	readiness, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, harness.attempt.AttemptID(),
	)
	if err != nil || readiness.Head() != harness.attempt.VerifiedHead() || readiness.Tree() != harness.tree ||
		readiness.Round() != 1 || readiness.Digest().IsZero() {
		t.Fatalf("review readiness = %#v error=%v", readiness, err)
	}
	manifest := harness.definition.Workspace()
	if readiness.Purpose() != workspace.ReviewMergeReadinessPurpose ||
		readiness.WorkspaceID() != manifest.ID() || readiness.Generation() != harness.definition.Generation() ||
		readiness.MergeUnit() != harness.attempt.MergeUnit() {
		t.Fatalf("review readiness scope = %#v", readiness)
	}
	type readinessJSON struct {
		SchemaVersion int    `json:"schema_version"`
		Purpose       string `json:"purpose"`
		WorkspaceID   string `json:"workspace_id"`
		Generation    string `json:"generation"`
		PlanID        string `json:"plan_id"`
		MergeUnitID   string `json:"merge_unit_id"`
		AttemptID     string `json:"attempt_id"`
		Round         uint16 `json:"round"`
		Head          string `json:"head"`
		Tree          string `json:"tree"`
		Loop          string `json:"loop_digest"`
	}
	canonicalReadiness, err := json.Marshal(readinessJSON{
		SchemaVersion: 2, Purpose: workspace.ReviewMergeReadinessPurpose,
		WorkspaceID: manifest.ID().String(), Generation: harness.definition.Generation().String(),
		PlanID:      harness.attempt.MergeUnit().PlanID().String(),
		MergeUnitID: harness.attempt.MergeUnit().MergeUnitID().String(),
		AttemptID:   harness.attempt.AttemptID().String(), Round: 1,
		Head: harness.attempt.VerifiedHead().String(), Tree: harness.tree.String(),
		Loop: mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID()).Loop().Digest().String(),
	})
	if err != nil || readiness.Digest() != workspace.DigestBytes(canonicalReadiness) {
		t.Fatalf("review readiness digest is not scoped canonically: %v", err)
	}
	result, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: harness.attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "reviewed"),
			OccurredAt: mustTime(t, "2026-07-21T11:00:06Z"),
		},
	)
	if err != nil || result.Attempt().Phase() != workspace.AttemptPaused {
		t.Fatalf("reviewed boundary = %#v error=%v", result, err)
	}

	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildReviewRuntime(snapshot, harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	replayedState, exists := replayed.State(harness.attempt.AttemptID())
	if !exists || !replayedState.MergeReady() || len(replayedState.Rounds()[0].Results()) != 2 {
		t.Fatalf("replayed review state = %#v exists=%v", replayedState, exists)
	}
	if digest, err := workspace.VerifyReviewRuntimeConformance(snapshot, harness.definition); err != nil || digest.IsZero() {
		t.Fatalf("review replay conformance digest=%s error=%v", digest, err)
	}

	changedEvidenceSubmission := reviewSubmission(
		t, start.Request(), firstSubmission.ReviewerInstance(),
		workspace.ReviewResultCompleted,
		[]workspace.ReviewFinding{
			mustReviewFinding(
				t, workspace.ReviewSeverityLow,
				"different durable review evidence",
			),
		},
		workspace.Digest{},
	)
	if _, _, err := workspace.RecordAttemptReviewResult(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.RecordAttemptReviewResultRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: firstReservation.Digest(),
			Submission: changedEvidenceSubmission,
			OccurredAt: mustTime(t, "2026-07-21T11:00:07Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "different durable evidence") {
		t.Fatalf("different result for durable request error = %v", err)
	}
}

func TestReviewRunnerRejectsRepositoryMutationAndWeakIsolation(t *testing.T) {
	t.Parallel()

	harness := newReviewHarness(t)
	start, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:10:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := start.Request()
	submission := reviewSubmission(
		t, request, workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	runner := reviewRunnerStub{run: func(invocation workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
		if invocation.Request().Digest() != request.Digest() ||
			invocation.Worktree() != harness.attempt.Worktree() {
			t.Fatalf("review invocation changed immutable inputs: %#v", invocation)
		}
		return workspace.NewReviewRunnerOutput(submission)
	}}
	exact := harness.repository.snapshot
	mutated, _ := workspace.NewReviewRepositorySnapshot(request.Head(), mustGitObject(t, 'c'), true)
	harness.repository.sequence = []workspace.ReviewRepositorySnapshot{exact, mutated}
	if _, _, err := workspace.ExecuteNextReviewProfile(
		context.Background(), harness.journal, harness.definition, harness.repository, runner,
		workspace.ExecuteNextReviewProfileRequest{
			AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
			IdempotencyKey: workspace.DigestBytes([]byte("mutating-reviewer-1")),
			OccurredAt:     mustTime(t, "2026-07-21T11:10:01Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "mutated or changed") {
		t.Fatalf("reviewer mutation error = %v", err)
	}
	state := mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	pending, ok, _ := state.NextRequest()
	if !ok || pending.Invocation() != 2 {
		t.Fatalf("mutating reviewer changed durable state: %#v", state)
	}
	runnerFailure := errors.New("review process crashed")
	harness.repository.sequence = []workspace.ReviewRepositorySnapshot{exact, mutated}
	if _, _, err := workspace.ExecuteNextReviewProfile(
		context.Background(), harness.journal, harness.definition, harness.repository,
		reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
			return workspace.ReviewRunnerOutput{}, runnerFailure
		}},
		workspace.ExecuteNextReviewProfileRequest{
			AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
			IdempotencyKey: workspace.DigestBytes([]byte("mutating-reviewer-2")),
			OccurredAt:     mustTime(t, "2026-07-21T11:10:01.5Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "mutated or changed") {
		t.Fatalf("runner-error mutation was not detected: %v", err)
	}

	state = mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	weakRequest, ok, err := state.NextRequest()
	if err != nil || !ok || weakRequest.Invocation() != 3 {
		t.Fatalf("weak-isolation retry request = %#v ok=%v err=%v", weakRequest, ok, err)
	}
	weakIsolation := workspace.NewReviewIsolationProof(true, true, false, true, false)
	weakSubmission, err := workspace.NewReviewResultSubmission(workspace.ReviewResultSubmissionOptions{
		RequestDigest: weakRequest.Digest(), ReviewerInstance: workspace.MustID("security-one"),
		Status: workspace.ReviewResultCompleted, Isolation: weakIsolation,
	})
	if err != nil {
		t.Fatal(err)
	}
	weakRunner := reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
		return workspace.NewReviewRunnerOutput(weakSubmission)
	}}
	harness.repository.sequence = nil
	if _, _, err := workspace.ExecuteNextReviewProfile(
		context.Background(), harness.journal, harness.definition, harness.repository, weakRunner,
		workspace.ExecuteNextReviewProfileRequest{
			AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
			IdempotencyKey: workspace.DigestBytes([]byte("weak-isolation-reviewer-3")),
			OccurredAt:     mustTime(t, "2026-07-21T11:10:02Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "strict read-only isolation") {
		t.Fatalf("weak review isolation error = %v", err)
	}
}

func TestReviewInvocationReservationSerializesRunnerAndCountsRawFailureIdentity(t *testing.T) {
	t.Parallel()

	t.Run("only the durable reservation may invoke the runner", func(t *testing.T) {
		harness := newReviewHarness(t)
		start, err := workspace.StartAttemptReviewRound(
			context.Background(), harness.journal, harness.definition, harness.repository,
			workspace.StartAttemptReviewRoundRequest{
				AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:15:00Z"),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		submission := reviewSubmission(
			t, start.Request(), workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
		)
		output, err := workspace.NewReviewRunnerOutput(submission)
		if err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		firstErr := make(chan error, 1)
		idempotencyKey := workspace.DigestBytes([]byte("serialized-review-one"))
		go func() {
			_, _, err := workspace.ExecuteNextReviewProfile(
				context.Background(), harness.journal, harness.definition, harness.repository,
				reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
					close(entered)
					<-release
					return output, nil
				}},
				workspace.ExecuteNextReviewProfileRequest{
					AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
					IdempotencyKey: idempotencyKey,
					OccurredAt:     mustTime(t, "2026-07-21T11:15:01Z"),
				},
			)
			firstErr <- err
		}()
		<-entered
		sameKeyRunnerCalled := false
		if _, _, err := workspace.ExecuteNextReviewProfile(
			context.Background(), harness.journal, harness.definition, harness.repository,
			reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
				sameKeyRunnerCalled = true
				return workspace.ReviewRunnerOutput{}, errors.New("same-key runner was invoked")
			}},
			workspace.ExecuteNextReviewProfileRequest{
				AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
				IdempotencyKey: idempotencyKey,
				OccurredAt:     mustTime(t, "2026-07-21T11:15:01.5Z"),
			},
		); err == nil || !strings.Contains(err.Error(), "durable executor claim") {
			t.Fatalf("same-key pending review invocation error = %v", err)
		}
		if sameKeyRunnerCalled {
			t.Fatal("same-key retry invoked a second reviewer")
		}
		if _, _, err := workspace.ExecuteNextReviewProfile(
			context.Background(), harness.journal, harness.definition, harness.repository,
			reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
				return workspace.ReviewRunnerOutput{}, errors.New("second runner was invoked")
			}},
			workspace.ExecuteNextReviewProfileRequest{
				AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
				IdempotencyKey: workspace.DigestBytes([]byte("serialized-review-two")),
				OccurredAt:     mustTime(t, "2026-07-21T11:15:02Z"),
			},
		); err == nil || !strings.Contains(err.Error(), "already reserved") {
			t.Fatalf("competing review invocation error = %v", err)
		}
		close(release)
		if err := <-firstErr; err != nil {
			t.Fatalf("durably reserved review invocation: %v", err)
		}
		retryRunnerCalled := false
		retried, retryRecord, err := workspace.ExecuteNextReviewProfile(
			context.Background(), harness.journal, harness.definition, harness.repository,
			reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
				retryRunnerCalled = true
				return workspace.ReviewRunnerOutput{}, errors.New("completed retry runner was invoked")
			}},
			workspace.ExecuteNextReviewProfileRequest{
				AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
				IdempotencyKey: idempotencyKey,
				OccurredAt:     mustTime(t, "2026-07-21T11:15:03Z"),
			},
		)
		if err != nil || retryRecord.Sequence() != 0 ||
			retried.Submission().Digest() != submission.Digest() || retryRunnerCalled {
			t.Fatalf("completed exact retry = %#v record=%#v runner=%v error=%v", retried, retryRecord, retryRunnerCalled, err)
		}
	})

	t.Run("raw failure consumes fresh reviewer identity", func(t *testing.T) {
		harness := newReviewHarness(t)
		start, err := workspace.StartAttemptReviewRound(
			context.Background(), harness.journal, harness.definition, harness.repository,
			workspace.StartAttemptReviewRoundRequest{
				AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:16:00Z"),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		security := reviewSubmission(
			t, start.Request(), workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
		)
		harness.record(t, start.Request(), security, "2026-07-21T11:16:01Z")
		state := mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
		correctness, ok, err := state.NextRequest()
		if err != nil || !ok || correctness.Profile().ReviewerPolicy() != workspace.ReviewReviewerFreshEachInvocation {
			t.Fatalf("fresh review request = %#v ok=%v err=%v", correctness, ok, err)
		}
		rawFailure := errors.New("reviewer process disappeared")
		rawFailureKey := workspace.DigestBytes([]byte("raw-failure-one"))
		if _, _, err := workspace.ExecuteNextReviewProfile(
			context.Background(), harness.journal, harness.definition, harness.repository,
			reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
				return workspace.ReviewRunnerOutput{}, rawFailure
			}},
			workspace.ExecuteNextReviewProfileRequest{
				AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("correctness-one"),
				IdempotencyKey: rawFailureKey,
				OccurredAt:     mustTime(t, "2026-07-21T11:16:02Z"),
			},
		); !errors.Is(err, rawFailure) {
			t.Fatalf("raw runner failure = %v", err)
		}
		failedRetryRunnerCalled := false
		if _, _, err := workspace.ExecuteNextReviewProfile(
			context.Background(), harness.journal, harness.definition, harness.repository,
			reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
				failedRetryRunnerCalled = true
				return workspace.ReviewRunnerOutput{}, errors.New("failed retry runner was invoked")
			}},
			workspace.ExecuteNextReviewProfileRequest{
				AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("correctness-one"),
				IdempotencyKey: rawFailureKey,
				OccurredAt:     mustTime(t, "2026-07-21T11:16:02.5Z"),
			},
		); err == nil || !strings.Contains(err.Error(), "already failed with digest") {
			t.Fatalf("failed exact retry error = %v", err)
		}
		if failedRetryRunnerCalled {
			t.Fatal("failed exact retry invoked a second reviewer")
		}
		state = mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
		retry, ok, err := state.NextRequest()
		if err != nil || !ok || retry.Invocation() != 2 || len(state.Rounds()[0].Failures()) != 1 {
			t.Fatalf("durable raw-failure retry = %#v state=%#v ok=%v err=%v", retry, state, ok, err)
		}
		if _, err := workspace.ReserveAttemptReviewInvocation(
			harness.journal, harness.definition, workspace.ReserveAttemptReviewInvocationRequest{
				AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("correctness-one"),
				IdempotencyKey: workspace.DigestBytes([]byte("raw-failure-two")),
				OccurredAt:     mustTime(t, "2026-07-21T11:16:03Z"),
			},
		); err == nil || !strings.Contains(err.Error(), "fresh reviewer") {
			t.Fatalf("reused crashed reviewer identity error = %v", err)
		}
	})
}

func TestReviewRunnerReturnsFailureJournalError(t *testing.T) {
	t.Parallel()

	harness := newReviewHarness(t)
	start, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:17:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = start
	rawFailure := errors.New("runner failed after journal became unavailable")
	if _, _, err := workspace.ExecuteNextReviewProfile(
		context.Background(), harness.journal, harness.definition, harness.repository,
		reviewRunnerStub{run: func(workspace.ReviewInvocation) (workspace.ReviewRunnerOutput, error) {
			if err := harness.journal.Close(); err != nil {
				return workspace.ReviewRunnerOutput{}, err
			}
			return workspace.ReviewRunnerOutput{}, rawFailure
		}},
		workspace.ExecuteNextReviewProfileRequest{
			AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("security-one"),
			IdempotencyKey: workspace.DigestBytes([]byte("failure-journal-error")),
			OccurredAt:     mustTime(t, "2026-07-21T11:17:01Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "record durable review runner failure") {
		t.Fatalf("failure-journaling error = %v", err)
	}
}

func TestOrdinaryCommitInvalidatesPriorReview(t *testing.T) {
	t.Parallel()

	harness := newReviewHarness(t)
	start, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:18:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	harness.record(t, start.Request(), reviewSubmission(
		t, start.Request(), workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	), "2026-07-21T11:18:01Z")
	state := mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	next, ok, err := state.NextRequest()
	if err != nil || !ok {
		t.Fatalf("second review request = %#v ok=%t err=%v", next, ok, err)
	}
	harness.record(t, next, reviewSubmission(
		t, next, workspace.MustID("correctness-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	), "2026-07-21T11:18:02Z")
	if _, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, harness.attempt.AttemptID(),
	); err != nil {
		t.Fatalf("initial review readiness: %v", err)
	}

	newHead, newTree := mustGitObject(t, 'c'), mustGitObject(t, 'd')
	harness.repository.snapshot, _ = workspace.NewReviewRepositorySnapshot(newHead, newTree, true)
	fresh, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:18:03Z"),
		},
	)
	if err != nil || fresh.Request().Head() != newHead || fresh.Request().Tree() != newTree || fresh.Request().Round() != 1 {
		t.Fatalf("fresh review after ordinary commit = %#v error=%v", fresh, err)
	}
	if _, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition, harness.repository, harness.attempt.AttemptID(),
	); err == nil {
		t.Fatal("prior review remained valid after an ordinary commit")
	}
}

func TestAttemptWideReviewConstraintsSurviveOrdinaryHeadAdoption(t *testing.T) {
	t.Parallel()

	harness := newReviewHarness(t)
	first, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:20:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	harness.record(t, first.Request(), reviewSubmission(
		t, first.Request(), workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	), "2026-07-21T11:20:01Z")

	state := mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	correctness, ok, err := state.NextRequest()
	if err != nil || !ok || correctness.Profile().ID().String() != "correctness" {
		t.Fatalf("H1 correctness request = %#v ok=%t err=%v", correctness, ok, err)
	}
	harness.record(t, correctness, reviewSubmission(
		t, correctness, workspace.MustID("correctness-one"), workspace.ReviewResultInfrastructureFailure,
		nil, workspace.DigestBytes([]byte("H1 correctness unavailable")),
	), "2026-07-21T11:20:02Z")
	state = mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	correctnessRetry, ok, err := state.NextRequest()
	if err != nil || !ok || correctnessRetry.Invocation() != 2 {
		t.Fatalf("H1 correctness retry = %#v ok=%t err=%v", correctnessRetry, ok, err)
	}
	harness.record(t, correctnessRetry, reviewSubmission(
		t, correctnessRetry, workspace.MustID("correctness-two"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	), "2026-07-21T11:20:03Z")

	secondHead, secondTree := mustGitObject(t, 'c'), mustGitObject(t, 'd')
	harness.repository.snapshot, err = workspace.NewReviewRepositorySnapshot(secondHead, secondTree, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:20:04Z"),
		},
	)
	if err != nil || second.Request().Head() != secondHead || second.Request().Round() != 1 {
		t.Fatalf("H2 review after ordinary commit = %#v error=%v", second, err)
	}
	harness.record(t, second.Request(), reviewSubmission(
		t, second.Request(), workspace.MustID("security-one"), workspace.ReviewResultCompleted, nil, workspace.Digest{},
	), "2026-07-21T11:20:05Z")
	state = mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	secondCorrectness, ok, err := state.NextRequest()
	if err != nil || !ok || secondCorrectness.Profile().ID().String() != "correctness" {
		t.Fatalf("H2 correctness request = %#v ok=%t err=%v", secondCorrectness, ok, err)
	}
	if _, err := workspace.ReserveAttemptReviewInvocation(
		harness.journal, harness.definition, workspace.ReserveAttemptReviewInvocationRequest{
			AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("correctness-three"),
			IdempotencyKey: workspace.DigestBytes([]byte("review-invocation:" + correctnessRetry.Digest().String())),
			OccurredAt:     mustTime(t, "2026-07-21T11:20:06Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "idempotency key") {
		t.Fatalf("H2 reused idempotency key error = %v", err)
	}
	if _, err := workspace.ReserveAttemptReviewInvocation(
		harness.journal, harness.definition, workspace.ReserveAttemptReviewInvocationRequest{
			AttemptID: harness.attempt.AttemptID(), ReviewerInstance: workspace.MustID("correctness-two"),
			IdempotencyKey: workspace.DigestBytes([]byte("H2 reused completed reviewer")),
			OccurredAt:     mustTime(t, "2026-07-21T11:20:06Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "fresh reviewer") {
		t.Fatalf("H2 reused completed reviewer error = %v", err)
	}

	harness.record(t, secondCorrectness, reviewSubmission(
		t, secondCorrectness, workspace.MustID("correctness-three"), workspace.ReviewResultInfrastructureFailure,
		nil, workspace.DigestBytes([]byte("H2 correctness unavailable")),
	), "2026-07-21T11:20:07Z")
	state = mustReviewState(t, harness.journal, harness.definition, harness.attempt.AttemptID())
	secondRetry, ok, err := state.NextRequest()
	if err != nil || !ok || secondRetry.Invocation() != 2 {
		t.Fatalf("H2 remaining retry = %#v ok=%t err=%v", secondRetry, ok, err)
	}
	harness.record(t, secondRetry, reviewSubmission(
		t, secondRetry, workspace.MustID("correctness-four"), workspace.ReviewResultInfrastructureFailure,
		nil, workspace.DigestBytes([]byte("H2 retry unavailable")),
	), "2026-07-21T11:20:08Z")

	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := workspace.RebuildReviewRuntime(snapshot, harness.definition)
	if err != nil {
		t.Fatal(err)
	}
	state, exists := reviews.State(harness.attempt.AttemptID())
	reason, exhausted := state.Exhaustion()
	if !exists || !exhausted || reason != workspace.ReviewExhaustedInfrastructure ||
		reviews.InfrastructureRetriesUsed(harness.attempt.AttemptID()) != 2 {
		t.Fatalf("attempt-wide review history = reason=%q exhausted=%t retries=%d", reason, exhausted, reviews.InfrastructureRetriesUsed(harness.attempt.AttemptID()))
	}
	if next, ok, err := state.NextRequest(); err != nil || ok {
		t.Fatalf("retry budget reset after head adoption: next=%#v ok=%t err=%v", next, ok, err)
	}
}

func TestReviewAdoptsCleanImplementationHeadWithoutCommitProtocol(t *testing.T) {
	t.Parallel()

	harness := newReviewHarness(t)
	implementationHead, implementationTree := mustGitObject(t, 'c'), mustGitObject(t, 'd')
	harness.repository.snapshot, _ = workspace.NewReviewRepositorySnapshot(implementationHead, implementationTree, true)
	start, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:19:00Z"),
		},
	)
	if err != nil || start.Request().Head() != implementationHead || start.Request().Tree() != implementationTree {
		t.Fatalf("adopted implementation review start = %#v error=%v", start, err)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	records := snapshot.Records()
	if len(records) < 2 || records[len(records)-2].EventType() != workspace.JournalEventReviewHeadAdopted ||
		records[len(records)-1].EventType() != workspace.JournalEventReviewRoundStarted {
		t.Fatalf("review adoption journal tail = %#v", records)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, exists := runtime.Attempt(harness.attempt.AttemptID())
	if !exists || attempt.VerifiedHead() != implementationHead {
		t.Fatalf("replayed adopted attempt = %#v exists=%v", attempt, exists)
	}
	retry, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:19:01Z"),
		},
	)
	if err != nil || retry.Record().Sequence() != 0 || retry.Request().Digest() != start.Request().Digest() {
		t.Fatalf("idempotent adopted review start = %#v error=%v", retry, err)
	}
}

func TestProtocolFreeAttemptAdoptsOrdinaryHeadWithoutReviewLoop(t *testing.T) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T11:18:00Z")
	implementationHead, implementationTree := mustGitObject(t, 'c'), mustGitObject(t, 'd')
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(implementationHead, implementationTree, true)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{snapshot: repositorySnapshot}
	result, err := workspace.AdoptAttemptHead(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:18:02Z"),
		},
	)
	if err != nil || !result.Adopted() || result.Head() != implementationHead ||
		result.Tree() != implementationTree || result.Record().Sequence() == 0 {
		t.Fatalf("protocol-free head adoption = %#v error=%v", result, err)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	records := snapshot.Records()
	if len(records) == 0 || records[len(records)-1].EventType() != workspace.JournalEventReviewHeadAdopted {
		t.Fatalf("protocol-free adoption journal tail = %#v", records)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	updated, exists := runtime.Attempt(attempt.AttemptID())
	if !exists || updated.VerifiedHead() != implementationHead {
		t.Fatalf("protocol-free adopted attempt = %#v exists=%v", updated, exists)
	}
	retry, err := workspace.AdoptAttemptHead(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:18:03Z"),
		},
	)
	if err != nil || retry.Adopted() || retry.Record().Sequence() != 0 || retry.Head() != implementationHead {
		t.Fatalf("idempotent protocol-free head adoption = %#v error=%v", retry, err)
	}
}

func configuredReviewFixture(t *testing.T) definitionFixture {
	t.Helper()
	fixture := newDefinitionFixture(t)
	configuration := string(fixture.sources.ExecutionConfig.Bytes)
	configuration = strings.Replace(configuration, "merge_units:\n", `review_profiles:
  - id: security
    runner: security-reviewer
    reviewer_policy: retain
  - id: correctness
    runner: correctness-reviewer
    reviewer_policy: fresh_each_invocation
merge_units:
`, 1)
	needle := "      max_review_rounds: 3\n  - plan_id: alpha-plan\n    merge_unit_id: unit-two"
	replacement := `      max_review_rounds: 3
    review_loop:
      profiles:
        - security
        - correctness
      max_infrastructure_retries: 2
  - plan_id: alpha-plan
    merge_unit_id: unit-two`
	configuration = strings.Replace(configuration, needle, replacement, 1)
	if !strings.Contains(configuration, "review_loop:") || configuration == string(fixture.sources.ExecutionConfig.Bytes) {
		t.Fatal("failed to install review configuration fixture")
	}
	fixture.sources.ExecutionConfig.Bytes = []byte(configuration)
	return fixture
}

func newReviewHarness(t *testing.T) *reviewHarness {
	t.Helper()
	fixture := configuredReviewFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	initialized, err := initializeWorkspaceV2(t,
		workspaceDir, definition, mustTime(t, "2026-07-21T10:00:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	target, ok := initialized.Runtime().LocalTarget()
	if !ok || target.CreatedHead().IsZero() {
		t.Fatal("initialized review harness has no durable local target head")
	}
	base := target.CreatedHead()
	goal, _ := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	core := attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDir, git: &fakeAttemptGit{}, base: base,
		unit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), goal: goal, worktrees: t.TempDir(),
	}
	attempt := core.reserve(t, "2026-07-21T10:01:00Z")
	tree := mustGitObject(t, 'b')
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(base, tree, true)
	if err != nil {
		t.Fatal(err)
	}
	return &reviewHarness{
		attemptHarness: core, attempt: attempt, repository: &reviewRepositoryStub{snapshot: repositorySnapshot}, tree: tree,
	}
}

func (harness *reviewHarness) record(
	t *testing.T,
	request workspace.ReviewRequest,
	submission workspace.ReviewResultSubmission,
	at string,
) workspace.VerifiedReviewResult {
	t.Helper()
	reservation := harness.reserve(t, request, submission.ReviewerInstance(), at)
	result, _, err := workspace.RecordAttemptReviewResult(
		context.Background(), harness.journal, harness.definition, harness.repository,
		workspace.RecordAttemptReviewResultRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservation.Digest(), Submission: submission,
			OccurredAt: mustTime(t, at),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (harness *reviewHarness) reserve(
	t *testing.T,
	request workspace.ReviewRequest,
	reviewer workspace.ID,
	at string,
) workspace.ReviewInvocationReservation {
	t.Helper()
	result, err := workspace.ReserveAttemptReviewInvocation(
		harness.journal, harness.definition, workspace.ReserveAttemptReviewInvocationRequest{
			AttemptID: harness.attempt.AttemptID(), ReviewerInstance: reviewer,
			IdempotencyKey: workspace.DigestBytes([]byte("review-invocation:" + request.Digest().String())),
			OccurredAt:     mustTime(t, at),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result.Reservation()
}

func reviewLoopForDefinition(t *testing.T, definition workspace.EffectiveWorkspaceDefinition) workspace.ReviewLoop {
	t.Helper()
	loop, configured := configuredUnitExecution(t, definition).ReviewLoop()
	if !configured {
		t.Fatal("configured review loop is missing")
	}
	return loop
}

func startDomainReview(
	t *testing.T,
	loop workspace.ReviewLoop,
	generation workspace.Digest,
) workspace.ReviewState {
	t.Helper()
	start, err := workspace.NewStartReviewRound(
		workspace.MustID("example-workspace"), generation, workspace.MustID("review-attempt"),
		mustMergeUnitReference(t, "alpha-plan", "unit-one"), loop, 1,
		mustGitObject(t, 'a'), mustGitObject(t, 'b'),
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.ReduceReview(workspace.ReviewState{}, start)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func startNextDomainReviewRound(
	t *testing.T,
	state workspace.ReviewState,
	head, tree workspace.GitObjectID,
) workspace.ReviewState {
	t.Helper()
	start, err := workspace.NewStartReviewRound(
		state.WorkspaceID(), state.Generation(), state.AttemptID(), state.MergeUnit(), state.Loop(),
		state.RoundsUsed()+1, head, tree,
	)
	if err != nil {
		t.Fatal(err)
	}
	next, err := workspace.ReduceReview(state, start)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func reviewSubmission(
	t *testing.T,
	request workspace.ReviewRequest,
	instance workspace.ID,
	status workspace.ReviewResultStatus,
	findings []workspace.ReviewFinding,
	infrastructure workspace.Digest,
) workspace.ReviewResultSubmission {
	t.Helper()
	result, err := workspace.NewReviewResultSubmission(workspace.ReviewResultSubmissionOptions{
		RequestDigest: request.Digest(), ReviewerInstance: instance, Status: status,
		Findings: findings, InfrastructureFailure: infrastructure, Isolation: workspace.StrictReviewIsolationProof(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func reduceReviewSubmission(
	t *testing.T,
	state workspace.ReviewState,
	request workspace.ReviewRequest,
	instance workspace.ID,
	status workspace.ReviewResultStatus,
	findings []workspace.ReviewFinding,
	infrastructure workspace.Digest,
) workspace.ReviewState {
	t.Helper()
	submission := reviewSubmission(t, request, instance, status, findings, infrastructure)
	idempotencyKey := workspace.DigestBytes([]byte(
		"domain-review-invocation:" + request.Digest().String() + ":" + instance.String(),
	))
	reservation, err := workspace.NewReviewInvocationReservation(request, instance, idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	reserve, err := workspace.NewReserveReviewInvocation(request, instance, idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	state, err = workspace.ReduceReview(state, reserve)
	if err != nil {
		t.Fatal(err)
	}
	record, err := workspace.NewRecordReviewResult(
		request.Round(), request.ProfileOrdinal(), request.Invocation(),
		reservation.Digest(), submission,
	)
	if err != nil {
		t.Fatal(err)
	}
	next, err := workspace.ReduceReview(state, record)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func completeBlockingDomainRound(
	t *testing.T,
	state workspace.ReviewState,
	round int,
) (workspace.ReviewState, workspace.ReviewFinding) {
	t.Helper()
	finding := mustReviewFinding(t, workspace.ReviewSeverityHigh, fmt.Sprintf("blocking round %d", round))
	security, ok, err := state.NextRequest()
	if err != nil || !ok || security.Profile().ID().String() != "security" {
		t.Fatalf("security request = %#v ok=%v err=%v", security, ok, err)
	}
	state = reduceReviewSubmission(
		t, state, security, workspace.MustID("security-one"), workspace.ReviewResultCompleted,
		[]workspace.ReviewFinding{finding}, workspace.Digest{},
	)
	correctness, ok, err := state.NextRequest()
	if err != nil || !ok || correctness.Profile().ID().String() != "correctness" {
		t.Fatalf("correctness request = %#v ok=%v err=%v", correctness, ok, err)
	}
	state = reduceReviewSubmission(
		t, state, correctness, workspace.MustID(fmt.Sprintf("correctness-%d", round)),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	return state, finding
}

func mustReviewFinding(t *testing.T, severity workspace.ReviewSeverity, summary string) workspace.ReviewFinding {
	t.Helper()
	finding, err := workspace.NewReviewFinding(workspace.ReviewFindingOptions{
		Severity: severity, Category: workspace.MustID("correctness"), Path: "internal/workspace/review.go",
		Line: 1, Summary: summary, EvidenceDigest: workspace.DigestBytes([]byte("evidence:" + summary)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return finding
}

func mustReviewState(
	t *testing.T,
	journal *workspace.WorkspaceJournal,
	definition workspace.EffectiveWorkspaceDefinition,
	attemptID workspace.ID,
) workspace.ReviewState {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := workspace.RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		t.Fatal(err)
	}
	state, exists := projection.State(attemptID)
	if !exists {
		t.Fatal("review state is missing")
	}
	return state
}

var _ workspace.ReviewRepositoryPort = (*reviewRepositoryStub)(nil)
var _ workspace.ReviewRunnerPort = reviewRunnerStub{}
