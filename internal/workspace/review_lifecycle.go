package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type ReviewRepositoryRequest struct {
	worktree string
	head     GitObjectID
}

func NewReviewRepositoryRequest(
	worktree string, head GitObjectID,
) (ReviewRepositoryRequest, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if !filepath.IsAbs(worktree) || head.IsZero() {
		return ReviewRepositoryRequest{}, fmt.Errorf("review repository request requires absolute worktree and head")
	}
	return ReviewRepositoryRequest{worktree: worktree, head: head}, nil
}

func (request ReviewRepositoryRequest) Worktree() string  { return request.worktree }
func (request ReviewRepositoryRequest) Head() GitObjectID { return request.head }

type ReviewRepositorySnapshot struct {
	head   GitObjectID
	tree   GitObjectID
	clean  bool
	digest Digest
}

func NewReviewRepositorySnapshot(head, tree GitObjectID, clean bool) (ReviewRepositorySnapshot, error) {
	if head.IsZero() || tree.IsZero() || head.Algorithm() != tree.Algorithm() {
		return ReviewRepositorySnapshot{}, fmt.Errorf("review repository snapshot requires algorithm-matched head and tree")
	}
	type snapshotJSON struct {
		SchemaVersion int    `json:"schema_version"`
		Head          string `json:"head"`
		Tree          string `json:"tree"`
		Clean         bool   `json:"clean"`
	}
	canonical, _ := json.Marshal(snapshotJSON{SchemaVersion: 2, Head: head.String(), Tree: tree.String(), Clean: clean})
	return ReviewRepositorySnapshot{head: head, tree: tree, clean: clean, digest: DigestBytes(canonical)}, nil
}

func (snapshot ReviewRepositorySnapshot) Head() GitObjectID { return snapshot.head }
func (snapshot ReviewRepositorySnapshot) Tree() GitObjectID { return snapshot.tree }
func (snapshot ReviewRepositorySnapshot) Clean() bool       { return snapshot.clean }
func (snapshot ReviewRepositorySnapshot) Digest() Digest    { return snapshot.digest }

// ReviewRepositoryPort observes an exact clean head/tree and validates a
// configured final history without mutating the attempt worktree.
type ReviewRepositoryPort interface {
	InspectReviewSnapshot(context.Context, ReviewRepositoryRequest) (ReviewRepositorySnapshot, error)
	VerifyFinalHistory(context.Context, CommitProtocol, string, GitObjectID, GitObjectID) error
}

type ReviewInvocation struct {
	reservation ReviewInvocationReservation
	worktree    string
}

func newReviewInvocation(
	reservation ReviewInvocationReservation, worktree string,
) (ReviewInvocation, error) {
	request := reservation.request
	repositoryRequest, err := NewReviewRepositoryRequest(worktree, request.head)
	canonical, reservationErr := canonicalReviewInvocationReservation(reservation)
	if err != nil || reservationErr != nil || reservation.digest != DigestBytes(canonical) ||
		request.digest.IsZero() || !request.isolationRequired.Strict() {
		return ReviewInvocation{}, fmt.Errorf("review invocation requires exact request and repository input")
	}
	return ReviewInvocation{
		reservation: reservation, worktree: repositoryRequest.worktree,
	}, nil
}

func (invocation ReviewInvocation) Request() ReviewRequest { return invocation.reservation.request }
func (invocation ReviewInvocation) Reservation() ReviewInvocationReservation {
	return invocation.reservation
}
func (invocation ReviewInvocation) ReviewerInstance() ID {
	return invocation.reservation.reviewerInstance
}
func (invocation ReviewInvocation) Worktree() string { return invocation.worktree }

type ReviewRunnerOutput struct {
	submission ReviewResultSubmission
}

func NewReviewRunnerOutput(
	submission ReviewResultSubmission,
) (ReviewRunnerOutput, error) {
	if submission.digest.IsZero() {
		return ReviewRunnerOutput{}, fmt.Errorf(
			"review runner output requires a canonical result",
		)
	}
	return ReviewRunnerOutput{submission: cloneReviewResult(submission)}, nil
}

func (output ReviewRunnerOutput) Submission() ReviewResultSubmission {
	return cloneReviewResult(output.submission)
}

// ReviewRunnerPort is a capability boundary, not a generic process or agent
// port. Implementations must materialize request.Head/Tree as read-only input,
// provide fresh ephemeral writable scratch, deny hooks, write-capable network,
// and external-write tools, then attest
// the actual isolation in the returned result. The workflow verifies the
// repository again after the runner returns and rejects any weaker proof.
type ReviewRunnerPort interface {
	RunReview(context.Context, ReviewInvocation) (ReviewRunnerOutput, error)
}

type ReviewRoundStartResult struct {
	state   ReviewState
	request ReviewRequest
	record  JournalRecord
}

func (result ReviewRoundStartResult) State() ReviewState     { return cloneReviewState(result.state) }
func (result ReviewRoundStartResult) Request() ReviewRequest { return result.request }
func (result ReviewRoundStartResult) Record() JournalRecord  { return result.record }

type StartAttemptReviewRoundRequest struct {
	AttemptID  ID
	OccurredAt time.Time
}

func StartAttemptReviewRound(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	request StartAttemptReviewRoundRequest,
) (ReviewRoundStartResult, error) {
	if journal == nil || repository == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return ReviewRoundStartResult{}, fmt.Errorf("start review round requires journal, repository inspector, attempt, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive {
		return ReviewRoundStartResult{}, fmt.Errorf("attempt %s must be active for review", request.AttemptID)
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	loop, configured := unit.ReviewLoop()
	if !configured {
		return ReviewRoundStartResult{}, fmt.Errorf("attempt %s has no configured review loop", request.AttemptID)
	}
	state, hasState := projection.State(request.AttemptID)
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, hasState, true); err != nil {
		return ReviewRoundStartResult{}, err
	}
	repositoryRequest, err := NewReviewRepositoryRequest(
		attempt.worktree, attempt.verifiedHead,
	)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	repositorySnapshot, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	if !repositorySnapshot.clean {
		return ReviewRoundStartResult{}, fmt.Errorf("review requires a clean exact attempt head")
	}
	if (repositorySnapshot.head != attempt.verifiedHead || !hasState) &&
		projection.RoundsUsed(request.AttemptID) >= loop.MaxRounds() {
		return ReviewRoundStartResult{}, reviewRoundBudgetExhaustedError(loop.MaxRounds())
	}
	if err := verifyAttemptFinalHistory(
		ctx, repository, unit, attempt, repositorySnapshot.head,
	); err != nil {
		return ReviewRoundStartResult{}, fmt.Errorf("review final history: %w", err)
	}
	if repositorySnapshot.head != attempt.verifiedHead {
		adoption, err := NewReviewHeadAdoptedJournalEvent(
			definition.workspace.id, definition.generation, attempt.attemptID, attempt.mergeUnit,
			attempt.verifiedHead, repositorySnapshot.head, repositorySnapshot.tree, repositorySnapshot.digest,
		)
		if err != nil {
			return ReviewRoundStartResult{}, err
		}
		if _, err := appendReviewJournalEvent(journal, snapshot, adoption, request.OccurredAt); err != nil {
			return ReviewRoundStartResult{}, err
		}
		snapshot, projection, err = readReviewRuntime(journal, definition)
		if err != nil {
			return ReviewRoundStartResult{}, err
		}
		attempt, exists = projection.core.Attempt(request.AttemptID)
		if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != repositorySnapshot.head {
			return ReviewRoundStartResult{}, fmt.Errorf("review head adoption did not rebuild to the inspected head")
		}
		state, hasState = projection.State(request.AttemptID)
		if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, false, true); err != nil {
			return ReviewRoundStartResult{}, err
		}
	}
	ordinal := uint16(1)
	if hasState {
		if state.loop.digest != loop.digest || state.generation != definition.generation {
			return ReviewRoundStartResult{}, fmt.Errorf("review configuration cannot reset durable counters")
		}
		if directive, exhausted := state.Exhaustion(); exhausted {
			if directive.Reason() == ReviewExhaustedRounds {
				return ReviewRoundStartResult{}, reviewRoundBudgetExhaustedError(loop.MaxRounds())
			}
			return ReviewRoundStartResult{}, fmt.Errorf("review loop is exhausted")
		}
		if pending, ok, err := state.NextRequest(); err != nil {
			return ReviewRoundStartResult{}, err
		} else if ok {
			return ReviewRoundStartResult{state: state, request: pending}, nil
		}
		if state.MergeReady() && state.head == attempt.verifiedHead {
			return ReviewRoundStartResult{}, fmt.Errorf("review is already clean on exact head %s", state.head)
		}
		ordinal = state.RoundsUsed() + 1
	}
	if projection.RoundsUsed(request.AttemptID) >= loop.MaxRounds() {
		return ReviewRoundStartResult{}, reviewRoundBudgetExhaustedError(loop.MaxRounds())
	}
	if !repositorySnapshot.clean || repositorySnapshot.head != attempt.verifiedHead {
		return ReviewRoundStartResult{}, fmt.Errorf("review requires a clean exact attempt head")
	}
	start, err := NewStartReviewRound(
		definition.workspace.id, definition.generation, attempt.attemptID, attempt.mergeUnit,
		loop, ordinal, repositorySnapshot.head, repositorySnapshot.tree,
	)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	if _, err := ReduceReview(state, start); err != nil {
		return ReviewRoundStartResult{}, err
	}
	event, err := NewReviewRoundStartedJournalEvent(start)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	record, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	_, updated, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	state, _ = updated.State(request.AttemptID)
	pending, ok, err := state.NextRequest()
	if err != nil || !ok {
		return ReviewRoundStartResult{}, fmt.Errorf("started review round has no first profile request")
	}
	return ReviewRoundStartResult{state: state, request: pending, record: record}, nil
}

type ReserveAttemptReviewInvocationRequest struct {
	AttemptID        ID
	ReviewerInstance ID
	IdempotencyKey   Digest
	OccurredAt       time.Time
}

type ReviewInvocationReservationResult struct {
	state       ReviewState
	reservation ReviewInvocationReservation
	record      JournalRecord
	acquired    bool
}

func (result ReviewInvocationReservationResult) State() ReviewState {
	return cloneReviewState(result.state)
}
func (result ReviewInvocationReservationResult) Reservation() ReviewInvocationReservation {
	return result.reservation
}
func (result ReviewInvocationReservationResult) Record() JournalRecord { return result.record }
func (result ReviewInvocationReservationResult) Acquired() bool        { return result.acquired }

type reviewInvocationOutcome struct {
	reservation ReviewInvocationReservation
	result      VerifiedReviewResult
	failure     ReviewInvocationFailure
	hasResult   bool
	hasFailure  bool
}

func findReviewInvocationOutcome(state ReviewState, idempotencyKey Digest) (reviewInvocationOutcome, bool) {
	for _, round := range state.rounds {
		for _, reservation := range round.reservations {
			if reservation.idempotencyKey != idempotencyKey {
				continue
			}
			outcome := reviewInvocationOutcome{reservation: reservation}
			for _, result := range round.attempts {
				if result.reservationDigest == reservation.digest {
					outcome.result, outcome.hasResult = result, true
					return outcome, true
				}
			}
			for _, failure := range round.failures {
				if failure.reservationDigest == reservation.digest {
					outcome.failure, outcome.hasFailure = failure, true
					return outcome, true
				}
			}
			return outcome, true
		}
	}
	return reviewInvocationOutcome{}, false
}

func ReserveAttemptReviewInvocation(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request ReserveAttemptReviewInvocationRequest,
) (ReviewInvocationReservationResult, error) {
	if journal == nil || request.AttemptID.IsZero() || request.ReviewerInstance.IsZero() ||
		request.IdempotencyKey.IsZero() || request.OccurredAt.IsZero() {
		return ReviewInvocationReservationResult{}, fmt.Errorf("reserve review invocation requires journal, attempt, reviewer, idempotency, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return ReviewInvocationReservationResult{}, fmt.Errorf("attempt %s has no active review state", request.AttemptID)
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != state.head {
		return ReviewInvocationReservationResult{}, fmt.Errorf("review invocation attempt is stale or inactive")
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, false); err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	if outcome, exists := findReviewInvocationOutcome(state, request.IdempotencyKey); exists {
		if outcome.reservation.reviewerInstance != request.ReviewerInstance {
			return ReviewInvocationReservationResult{}, fmt.Errorf("review invocation idempotency key is already bound to another reviewer")
		}
		return ReviewInvocationReservationResult{
			state: state, reservation: outcome.reservation,
		}, nil
	}
	pending, ok, err := state.NextRequest()
	if err != nil || !ok {
		return ReviewInvocationReservationResult{}, fmt.Errorf("review round has no pending profile")
	}
	reservation, err := NewReviewInvocationReservation(
		pending, request.ReviewerInstance, request.IdempotencyKey,
	)
	if err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	latestRound := state.rounds[len(state.rounds)-1]
	if existing, reserved := pendingReviewInvocation(latestRound); reserved {
		if existing.digest != reservation.digest {
			return ReviewInvocationReservationResult{}, fmt.Errorf("review request is already reserved by a different invocation")
		}
		return ReviewInvocationReservationResult{state: state, reservation: existing}, nil
	}
	domain, err := NewReserveReviewInvocation(pending, request.ReviewerInstance, request.IdempotencyKey)
	if err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	if _, err := ReduceReview(state, domain); err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	event, err := NewReviewInvocationReservedJournalEvent(
		definition.workspace.id, definition.generation, request.AttemptID, state.loop.digest, reservation,
	)
	if err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	record, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	_, updated, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewInvocationReservationResult{}, err
	}
	state, _ = updated.State(request.AttemptID)
	return ReviewInvocationReservationResult{
		state: state, reservation: reservation, record: record, acquired: true,
	}, nil
}

type RecordAttemptReviewResultRequest struct {
	AttemptID         ID
	ReservationDigest Digest
	Submission        ReviewResultSubmission
	OccurredAt        time.Time
}

func RecordAttemptReviewResult(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	request RecordAttemptReviewResultRequest,
) (VerifiedReviewResult, JournalRecord, error) {
	prepared, err := prepareAttemptReviewResult(ctx, journal, definition, repository, request)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if prepared.alreadyRecorded {
		return prepared.verified, JournalRecord{}, nil
	}
	event, err := NewReviewResultRecordedJournalEvent(
		definition.workspace.id, definition.generation, request.AttemptID, prepared.state.loop.digest, prepared.domain,
	)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	record, err := appendReviewJournalEvent(journal, prepared.snapshot, event, request.OccurredAt)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	return prepared.verified, record, nil
}

// preparedReviewResult contains a fully validated result transition before it
// is made durable. Document-backed review uses this seam so its raw report is
// retained only after all legacy lifecycle checks have passed, and immediately
// before the journal event that references it is appended.
type preparedReviewResult struct {
	snapshot        JournalSnapshot
	state           ReviewState
	domain          RecordReviewResult
	verified        VerifiedReviewResult
	alreadyRecorded bool
}

func prepareAttemptReviewResult(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	request RecordAttemptReviewResultRequest,
) (preparedReviewResult, error) {
	if journal == nil || repository == nil || request.AttemptID.IsZero() ||
		request.ReservationDigest.IsZero() || request.Submission.digest.IsZero() ||
		request.OccurredAt.IsZero() {
		return preparedReviewResult{}, fmt.Errorf(
			"record review result requires journal, repository, attempt, reservation, result, and occurrence time",
		)
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return preparedReviewResult{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return preparedReviewResult{}, fmt.Errorf("attempt %s has no active review round", request.AttemptID)
	}
	for _, round := range state.rounds {
		for _, existing := range round.attempts {
			if existing.reservationDigest != request.ReservationDigest {
				continue
			}
			if existing.submission.digest == request.Submission.digest {
				return preparedReviewResult{verified: existing, alreadyRecorded: true}, nil
			}
			return preparedReviewResult{}, fmt.Errorf("review request already has different durable evidence")
		}
	}
	pending, ok, err := state.NextRequest()
	if err != nil || !ok {
		return preparedReviewResult{}, fmt.Errorf("review round has no pending profile")
	}
	latestRound := state.rounds[len(state.rounds)-1]
	reservation, reserved := pendingReviewInvocation(latestRound)
	if !reserved || reservation.digest != request.ReservationDigest || pending.digest != request.Submission.requestDigest ||
		request.Submission.reviewerInstance != reservation.reviewerInstance || !request.Submission.isolation.Strict() {
		return preparedReviewResult{}, fmt.Errorf("review result does not match pending request or strict isolation")
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != pending.head {
		return preparedReviewResult{}, fmt.Errorf("review result attempt is stale or inactive")
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return preparedReviewResult{}, err
	}
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, false); err != nil {
		return preparedReviewResult{}, err
	}
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.worktree, pending.head)
	if err != nil {
		return preparedReviewResult{}, err
	}
	repositorySnapshot, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil {
		return preparedReviewResult{}, err
	}
	if !repositorySnapshot.clean || repositorySnapshot.head != pending.head || repositorySnapshot.tree != pending.tree {
		return preparedReviewResult{}, fmt.Errorf("reviewer changed or no longer matches the exact clean head/tree")
	}
	domain, err := NewRecordReviewResult(
		pending.round, pending.profileOrdinal, pending.invocation,
		request.ReservationDigest, request.Submission,
	)
	if err != nil {
		return preparedReviewResult{}, err
	}
	if _, err := ReduceReview(state, domain); err != nil {
		return preparedReviewResult{}, err
	}
	return preparedReviewResult{
		snapshot: snapshot, state: state, domain: domain,
		verified: VerifiedReviewResult{
			request: pending, submission: cloneReviewResult(request.Submission),
			reservationDigest: request.ReservationDigest,
		},
	}, nil
}

type RecordAttemptReviewInvocationFailureRequest struct {
	AttemptID         ID
	ReservationDigest Digest
	FailureDigest     Digest
	OccurredAt        time.Time
}

func RecordAttemptReviewInvocationFailure(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordAttemptReviewInvocationFailureRequest,
) (ReviewState, JournalRecord, error) {
	if journal == nil || request.AttemptID.IsZero() || request.ReservationDigest.IsZero() ||
		request.FailureDigest.IsZero() || request.OccurredAt.IsZero() {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("record review invocation failure requires journal, attempt, reservation, failure, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("attempt %s has no active review state", request.AttemptID)
	}
	for _, round := range state.rounds {
		for _, failure := range round.failures {
			if failure.reservationDigest != request.ReservationDigest {
				continue
			}
			if failure.failureDigest == request.FailureDigest {
				return state, JournalRecord{}, nil
			}
			return ReviewState{}, JournalRecord{}, fmt.Errorf("review invocation already has a different durable failure")
		}
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != state.head {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("review invocation failure attempt is stale or inactive")
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, false); err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	domain, err := NewRecordReviewInvocationFailure(request.ReservationDigest, request.FailureDigest)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	updated, err := ReduceReview(state, domain)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	event, err := NewReviewInvocationFailedJournalEvent(
		definition.workspace.id, definition.generation, request.AttemptID, state.loop.digest, domain,
	)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	record, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	_, rebuilt, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	updated, _ = rebuilt.State(request.AttemptID)
	return updated, record, nil
}

type ExecuteNextReviewProfileRequest struct {
	AttemptID        ID
	ReviewerInstance ID
	IdempotencyKey   Digest
	OccurredAt       time.Time
}

func ExecuteNextReviewProfile(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	runner ReviewRunnerPort,
	request ExecuteNextReviewProfileRequest,
) (VerifiedReviewResult, JournalRecord, error) {
	if runner == nil || repository == nil || request.AttemptID.IsZero() ||
		request.ReviewerInstance.IsZero() || request.IdempotencyKey.IsZero() || request.OccurredAt.IsZero() {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf(
			"execute review profile requires isolated runner, repository, attempt, reviewer, idempotency, and occurrence time",
		)
	}
	reserved, err := ReserveAttemptReviewInvocation(journal, definition, ReserveAttemptReviewInvocationRequest{
		AttemptID: request.AttemptID, ReviewerInstance: request.ReviewerInstance,
		IdempotencyKey: request.IdempotencyKey, OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if !reserved.acquired {
		outcome, exists := findReviewInvocationOutcome(reserved.state, request.IdempotencyKey)
		if !exists || outcome.reservation.digest != reserved.reservation.digest {
			return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("durable review invocation outcome is unavailable")
		}
		if outcome.hasResult {
			return outcome.result, JournalRecord{}, nil
		}
		if outcome.hasFailure {
			return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf(
				"review invocation %s already failed with digest %s",
				outcome.reservation.digest, outcome.failure.failureDigest,
			)
		}
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf(
			"review invocation %s is already pending under its durable executor claim",
			outcome.reservation.digest,
		)
	}
	_, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("attempt %s has no review state", request.AttemptID)
	}
	pending, ok, err := state.NextRequest()
	if err != nil || !ok {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("attempt %s has no pending review profile", request.AttemptID)
	}
	if pending.digest != reserved.reservation.request.digest {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("durable review invocation no longer matches the pending request")
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("attempt %s is unavailable", request.AttemptID)
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, false); err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.worktree, pending.head)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	before, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil || !before.clean || before.head != pending.head || before.tree != pending.tree {
		failure := fmt.Errorf("review repository is not the exact clean requested snapshot")
		if recordErr := recordReviewRunnerFailure(journal, definition, request, reserved.reservation.digest, failure); recordErr != nil {
			return VerifiedReviewResult{}, JournalRecord{}, recordErr
		}
		return VerifiedReviewResult{}, JournalRecord{}, failure
	}
	invocation, err := newReviewInvocation(reserved.reservation, attempt.worktree)
	if err != nil {
		if recordErr := recordReviewRunnerFailure(journal, definition, request, reserved.reservation.digest, err); recordErr != nil {
			return VerifiedReviewResult{}, JournalRecord{}, recordErr
		}
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	output, err := runner.RunReview(ctx, invocation)
	after, inspectionErr := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if inspectionErr != nil || after.digest != before.digest {
		failure := fmt.Errorf("reviewer mutated or changed repository input")
		if recordErr := recordReviewRunnerFailure(journal, definition, request, reserved.reservation.digest, failure); recordErr != nil {
			return VerifiedReviewResult{}, JournalRecord{}, recordErr
		}
		return VerifiedReviewResult{}, JournalRecord{}, failure
	}
	if err != nil {
		if recordErr := recordReviewRunnerFailure(journal, definition, request, reserved.reservation.digest, err); recordErr != nil {
			return VerifiedReviewResult{}, JournalRecord{}, recordErr
		}
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if !output.submission.isolation.Strict() {
		failure := fmt.Errorf("review runner did not prove strict read-only isolation")
		if recordErr := recordReviewRunnerFailure(journal, definition, request, reserved.reservation.digest, failure); recordErr != nil {
			return VerifiedReviewResult{}, JournalRecord{}, recordErr
		}
		return VerifiedReviewResult{}, JournalRecord{}, failure
	}
	return RecordAttemptReviewResult(ctx, journal, definition, repository, RecordAttemptReviewResultRequest{
		AttemptID: request.AttemptID, ReservationDigest: reserved.reservation.digest,
		Submission: output.submission, OccurredAt: request.OccurredAt,
	})
}

func recordReviewRunnerFailure(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request ExecuteNextReviewProfileRequest,
	reservationDigest Digest,
	failure error,
) error {
	if failure == nil {
		return nil
	}
	failureDigest := DigestBytes([]byte("review-runner-failure-v2\x00" + failure.Error()))
	_, _, err := RecordAttemptReviewInvocationFailure(journal, definition, RecordAttemptReviewInvocationFailureRequest{
		AttemptID: request.AttemptID, ReservationDigest: reservationDigest,
		FailureDigest: failureDigest, OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("record durable review runner failure after %v: %w", failure, err)
	}
	return nil
}

const ReviewMergeReadinessPurpose = "review_merge_readiness"

type ReviewReadiness struct {
	purpose    string
	workspace  ID
	generation Digest
	attemptID  ID
	mergeUnit  MergeUnitReference
	round      uint16
	head       GitObjectID
	tree       GitObjectID
	digest     Digest
}

func (readiness ReviewReadiness) Purpose() string               { return readiness.purpose }
func (readiness ReviewReadiness) WorkspaceID() ID               { return readiness.workspace }
func (readiness ReviewReadiness) Generation() Digest            { return readiness.generation }
func (readiness ReviewReadiness) AttemptID() ID                 { return readiness.attemptID }
func (readiness ReviewReadiness) MergeUnit() MergeUnitReference { return readiness.mergeUnit }
func (readiness ReviewReadiness) Round() uint16                 { return readiness.round }
func (readiness ReviewReadiness) Head() GitObjectID             { return readiness.head }
func (readiness ReviewReadiness) Tree() GitObjectID             { return readiness.tree }
func (readiness ReviewReadiness) Digest() Digest                { return readiness.digest }

func ConfirmReviewMergeReadiness(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	attemptID ID,
) (ReviewReadiness, error) {
	if journal == nil || repository == nil || attemptID.IsZero() {
		return ReviewReadiness{}, fmt.Errorf("confirm review readiness requires journal, repository, and attempt")
	}
	_, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewReadiness{}, err
	}
	state, exists := projection.State(attemptID)
	if !exists || !state.MergeReady() {
		return ReviewReadiness{}, fmt.Errorf("attempt %s has no exact-head clean review confirmation", attemptID)
	}
	attempt, exists := projection.core.Attempt(attemptID)
	if !exists || attempt.verifiedHead != state.head {
		return ReviewReadiness{}, fmt.Errorf("review readiness is stale against attempt head")
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewReadiness{}, err
	}
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, false); err != nil {
		return ReviewReadiness{}, err
	}
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.worktree, state.head)
	if err != nil {
		return ReviewReadiness{}, err
	}
	snapshot, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil || !snapshot.clean || snapshot.head != state.head || snapshot.tree != state.tree {
		return ReviewReadiness{}, fmt.Errorf("review readiness is stale against repository head/tree")
	}
	return newReviewMergeReadiness(definition, attempt, state)
}

func newReviewMergeReadiness(
	definition EffectiveWorkspaceDefinition,
	attempt RuntimeAttemptProjection,
	state ReviewState,
) (ReviewReadiness, error) {
	if !state.MergeReady() || state.workspaceID != definition.workspace.id ||
		state.generation != definition.generation || state.attemptID != attempt.attemptID ||
		state.mergeUnit != attempt.mergeUnit || state.head != attempt.verifiedHead ||
		state.head.IsZero() || state.tree.IsZero() {
		return ReviewReadiness{}, fmt.Errorf("review readiness does not match durable exact-head review state")
	}
	readiness := ReviewReadiness{
		purpose: ReviewMergeReadinessPurpose, workspace: definition.workspace.id,
		generation: definition.generation, attemptID: attempt.attemptID, mergeUnit: attempt.mergeUnit,
		round: state.RoundsUsed(), head: state.head, tree: state.tree,
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
	canonical, _ := json.Marshal(readinessJSON{
		SchemaVersion: 2, Purpose: readiness.purpose, WorkspaceID: readiness.workspace.String(),
		Generation: readiness.generation.String(), PlanID: readiness.mergeUnit.planID.String(),
		MergeUnitID: readiness.mergeUnit.mergeUnitID.String(),
		AttemptID:   attempt.attemptID.String(), Round: readiness.round,
		Head: readiness.head.String(), Tree: readiness.tree.String(), Loop: state.loop.digest.String(),
	})
	readiness.digest = DigestBytes(canonical)
	return readiness, nil
}

func validateAttemptReviewProtocolState(
	_ EffectiveWorkspaceDefinition,
	unit UnitExecution,
	attempt RuntimeAttemptProjection,
	review ReviewState,
	hasReview bool,
	allowStaleReviewHead bool,
) error {
	loop, configured := unit.ReviewLoop()
	if !configured || attempt.verifiedHead.IsZero() {
		return fmt.Errorf("attempt %s has no configured review loop at an accepted head", attempt.attemptID)
	}
	if !hasReview {
		return nil
	}
	if review.loop.digest != loop.digest {
		return fmt.Errorf("attempt %s review state does not match its configured loop", attempt.attemptID)
	}
	if review.head != attempt.verifiedHead && !allowStaleReviewHead {
		return fmt.Errorf("attempt %s review state no longer matches the accepted head", attempt.attemptID)
	}
	return nil
}

func readReviewRuntime(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
) (JournalSnapshot, ReviewRuntimeProjection, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{}, err
	}
	projection, err := RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		return JournalSnapshot{}, ReviewRuntimeProjection{}, err
	}
	return snapshot, projection, nil
}

func appendReviewJournalEvent(
	journal *WorkspaceJournal,
	snapshot JournalSnapshot,
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalRecord, error) {
	appendRequest, err := newWorkflowJournalAppend(event, occurredAt)
	if err != nil {
		return JournalRecord{}, err
	}
	return journal.AppendIfHead(appendRequest, snapshot.head)
}
