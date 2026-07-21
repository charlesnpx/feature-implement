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
	repository RepositoryIdentity
	worktree   string
	branch     string
	head       GitObjectID
}

func NewReviewRepositoryRequest(
	repository RepositoryIdentity, worktree, branch string, head GitObjectID,
) (ReviewRepositoryRequest, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if repository.String() == "" || !filepath.IsAbs(worktree) || head.IsZero() {
		return ReviewRepositoryRequest{}, fmt.Errorf("review repository request requires repository, absolute worktree, and head")
	}
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return ReviewRepositoryRequest{}, err
	}
	return ReviewRepositoryRequest{repository: repository, worktree: worktree, branch: branch, head: head}, nil
}

func (request ReviewRepositoryRequest) Repository() RepositoryIdentity { return request.repository }
func (request ReviewRepositoryRequest) Worktree() string               { return request.worktree }
func (request ReviewRepositoryRequest) Branch() string                 { return request.branch }
func (request ReviewRepositoryRequest) Head() GitObjectID              { return request.head }

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

// ReviewRepositoryPort is deliberately read-only. It can inspect and confirm
// an exact clean head/tree but has no mutation, commit, push, provider, or
// process-execution method.
type ReviewRepositoryPort interface {
	InspectReviewSnapshot(context.Context, ReviewRepositoryRequest) (ReviewRepositorySnapshot, error)
}

type ReviewInvocation struct {
	reservation ReviewInvocationReservation
	repository  RepositoryIdentity
	worktree    string
	branch      string
}

func newReviewInvocation(
	reservation ReviewInvocationReservation, repository RepositoryIdentity, worktree, branch string,
) (ReviewInvocation, error) {
	request := reservation.request
	repositoryRequest, err := NewReviewRepositoryRequest(repository, worktree, branch, request.head)
	canonical, reservationErr := canonicalReviewInvocationReservation(reservation)
	if err != nil || reservationErr != nil || reservation.digest != DigestBytes(canonical) ||
		request.digest.IsZero() || !request.isolationRequired.Strict() {
		return ReviewInvocation{}, fmt.Errorf("review invocation requires exact request and repository input")
	}
	return ReviewInvocation{
		reservation: reservation, repository: repositoryRequest.repository,
		worktree: repositoryRequest.worktree, branch: repositoryRequest.branch,
	}, nil
}

func (invocation ReviewInvocation) Request() ReviewRequest { return invocation.reservation.request }
func (invocation ReviewInvocation) Reservation() ReviewInvocationReservation {
	return invocation.reservation
}
func (invocation ReviewInvocation) ReviewerInstance() ID {
	return invocation.reservation.reviewerInstance
}
func (invocation ReviewInvocation) Repository() RepositoryIdentity { return invocation.repository }
func (invocation ReviewInvocation) Worktree() string               { return invocation.worktree }
func (invocation ReviewInvocation) Branch() string                 { return invocation.branch }

type ReviewRunnerOutput struct {
	submission ReviewResultSubmission
	receipt    ControlPlaneReceiptV2
}

func NewReviewRunnerOutput(
	submission ReviewResultSubmission, receipt ControlPlaneReceiptV2,
) (ReviewRunnerOutput, error) {
	if submission.digest.IsZero() || receipt.ReceiptDigest().IsZero() {
		return ReviewRunnerOutput{}, fmt.Errorf("review runner output requires result and signed receipt")
	}
	return ReviewRunnerOutput{submission: cloneReviewResult(submission), receipt: receipt}, nil
}

func (output ReviewRunnerOutput) Submission() ReviewResultSubmission {
	return cloneReviewResult(output.submission)
}
func (output ReviewRunnerOutput) Receipt() ControlPlaneReceiptV2 { return output.receipt }

// ReviewRunnerPort is a capability boundary, not a generic process or agent
// port. Implementations must materialize request.Head/Tree as read-only input,
// provide fresh ephemeral writable scratch, deny credentials, hooks,
// write-capable network, provider broker, and external-write tools, then attest
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
		attempt.repository, attempt.worktree, attempt.branch, attempt.verifiedHead,
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
	if repositorySnapshot.head != attempt.verifiedHead {
		if _, configured := unit.CommitProtocol(); configured || hasState || attempt.reviewFixes != nil {
			return ReviewRoundStartResult{}, fmt.Errorf("review cannot adopt a changed head after durable commit or review state")
		}
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
		if hasState {
			return ReviewRoundStartResult{}, fmt.Errorf("review head adoption unexpectedly found prior review state")
		}
		if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, false, true); err != nil {
			return ReviewRoundStartResult{}, err
		}
	}
	ordinal := uint16(1)
	if hasState {
		if state.loop.digest != loop.digest || state.generation != definition.generation {
			return ReviewRoundStartResult{}, fmt.Errorf("review configuration cannot reset durable counters")
		}
		if _, exhausted := state.Exhaustion(); exhausted {
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
}

func (result ReviewInvocationReservationResult) State() ReviewState {
	return cloneReviewState(result.state)
}
func (result ReviewInvocationReservationResult) Reservation() ReviewInvocationReservation {
	return result.reservation
}
func (result ReviewInvocationReservationResult) Record() JournalRecord { return result.record }

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
	return ReviewInvocationReservationResult{state: state, reservation: reservation, record: record}, nil
}

type RecordAttemptReviewResultRequest struct {
	AttemptID         ID
	ReservationDigest Digest
	Submission        ReviewResultSubmission
	Receipt           ControlPlaneReceiptV2
	OccurredAt        time.Time
}

func ReviewResultControlPlaneBinding(
	definition EffectiveWorkspaceDefinition,
	request ReviewRequest,
	submission ReviewResultSubmission,
) (ControlPlaneBinding, error) {
	if request.workspaceID != definition.workspace.id || request.generation != definition.generation ||
		submission.requestDigest != request.digest || submission.digest.IsZero() {
		return ControlPlaneBinding{}, fmt.Errorf("review evidence binding requires exact request and result")
	}
	return NewControlPlaneBinding(ControlPlaneBindingOptions{
		Kind: ControlPlaneReceiptReviewEvidence, WorkspaceID: request.workspaceID,
		Generation: request.generation, RequestDigest: submission.digest,
		Repository: definition.workspace.repository, Remote: definition.workspace.remote,
		Head: request.head, Tree: request.tree,
	})
}

func RecordAttemptReviewResult(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	verifier ControlPlaneVerifierPort,
	request RecordAttemptReviewResultRequest,
) (VerifiedReviewResult, JournalRecord, error) {
	if journal == nil || repository == nil || verifier == nil || request.AttemptID.IsZero() ||
		request.ReservationDigest.IsZero() || request.Submission.digest.IsZero() ||
		request.Receipt.ReceiptDigest().IsZero() || request.OccurredAt.IsZero() {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("record review result requires journal, repository, verifier, attempt, result, receipt, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("attempt %s has no active review round", request.AttemptID)
	}
	for _, round := range state.rounds {
		for _, existing := range round.attempts {
			if existing.reservationDigest != request.ReservationDigest {
				continue
			}
			if existing.submission.digest == request.Submission.digest &&
				existing.receiptDigest == request.Receipt.ReceiptDigest() {
				return existing, JournalRecord{}, nil
			}
			return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("review request already has different durable evidence")
		}
	}
	pending, ok, err := state.NextRequest()
	if err != nil || !ok {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("review round has no pending profile")
	}
	latestRound := state.rounds[len(state.rounds)-1]
	reservation, reserved := pendingReviewInvocation(latestRound)
	if !reserved || reservation.digest != request.ReservationDigest || pending.digest != request.Submission.requestDigest ||
		request.Submission.reviewerInstance != reservation.reviewerInstance || !request.Submission.isolation.Strict() {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("review result does not match pending request or strict isolation")
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != pending.head {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("review result attempt is stale or inactive")
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, false); err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.repository, attempt.worktree, attempt.branch, pending.head)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	repositorySnapshot, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if !repositorySnapshot.clean || repositorySnapshot.head != pending.head || repositorySnapshot.tree != pending.tree {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("reviewer changed or no longer matches the exact clean head/tree")
	}
	binding, err := ReviewResultControlPlaneBinding(definition, pending, request.Submission)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	verification, err := NewControlPlaneVerification(binding)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if err := verifier.Verify(ctx, verification, request.Receipt); err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("verify signed review result: %w", err)
	}
	domain, err := NewRecordReviewResult(
		pending.round, pending.profileOrdinal, pending.invocation,
		request.ReservationDigest, request.Submission, request.Receipt.ReceiptDigest(),
	)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if _, err := ReduceReview(state, domain); err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	event, err := NewReviewResultRecordedJournalEvent(
		definition.workspace.id, definition.generation, request.AttemptID, state.loop.digest, domain,
	)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	record, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	return VerifiedReviewResult{
		request: pending, submission: cloneReviewResult(request.Submission),
		reservationDigest: request.ReservationDigest, receiptDigest: request.Receipt.ReceiptDigest(),
	}, record, nil
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
	verifier ControlPlaneVerifierPort,
	request ExecuteNextReviewProfileRequest,
) (VerifiedReviewResult, JournalRecord, error) {
	if runner == nil || repository == nil || verifier == nil || request.AttemptID.IsZero() ||
		request.ReviewerInstance.IsZero() || request.IdempotencyKey.IsZero() || request.OccurredAt.IsZero() {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("execute review profile requires isolated runner, repository, verifier, attempt, reviewer, idempotency, and occurrence time")
	}
	reserved, err := ReserveAttemptReviewInvocation(journal, definition, ReserveAttemptReviewInvocationRequest{
		AttemptID: request.AttemptID, ReviewerInstance: request.ReviewerInstance,
		IdempotencyKey: request.IdempotencyKey, OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
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
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.repository, attempt.worktree, attempt.branch, pending.head)
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
	invocation, err := newReviewInvocation(reserved.reservation, attempt.repository, attempt.worktree, attempt.branch)
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
	return RecordAttemptReviewResult(ctx, journal, definition, repository, verifier, RecordAttemptReviewResultRequest{
		AttemptID: request.AttemptID, ReservationDigest: reserved.reservation.digest,
		Submission: output.submission, Receipt: output.receipt, OccurredAt: request.OccurredAt,
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

type ReserveAttemptReviewFixRequest struct {
	AttemptID          ID
	Ordinal            uint16
	AcceptedFindingIDs []Digest
	OccurredAt         time.Time
}

type ReviewFixReservationResult struct {
	state       ReviewState
	reservation ReviewFixReservation
	record      JournalRecord
}

func (result ReviewFixReservationResult) State() ReviewState { return cloneReviewState(result.state) }
func (result ReviewFixReservationResult) Reservation() ReviewFixReservation {
	return *cloneReviewFixReservation(&result.reservation)
}
func (result ReviewFixReservationResult) Record() JournalRecord { return result.record }

func ReserveAttemptReviewFix(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request ReserveAttemptReviewFixRequest,
) (ReviewFixReservationResult, error) {
	if journal == nil || request.AttemptID.IsZero() || request.Ordinal == 0 ||
		len(request.AcceptedFindingIDs) == 0 || request.OccurredAt.IsZero() {
		return ReviewFixReservationResult{}, fmt.Errorf("reserve review fix requires journal, attempt, ordinal, findings, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return ReviewFixReservationResult{}, fmt.Errorf("attempt %s cannot execute a review fix before a completed review round", request.AttemptID)
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive {
		return ReviewFixReservationResult{}, fmt.Errorf("review fix reservation attempt is stale or inactive")
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	loop, configured := unit.ReviewLoop()
	if !configured || loop.digest != state.loop.digest {
		return ReviewFixReservationResult{}, fmt.Errorf("attempt %s has no matching configured review loop", request.AttemptID)
	}
	normalizedFindings, err := normalizeReviewFindingIDs(request.AcceptedFindingIDs)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	if request.Ordinal <= state.FixesUsed() {
		existing := state.fixes[request.Ordinal-1]
		if !equalDigestSlices(existing.findings, normalizedFindings) {
			return ReviewFixReservationResult{}, fmt.Errorf("review fix reservation retry differs from durable application")
		}
		reservation, err := NewReviewFixReservation(
			state.workspaceID, state.generation, state.attemptID, state.loop.digest,
			existing.ordinal, existing.round, existing.priorHead, existing.priorTree, existing.findings,
		)
		if err != nil || reservation.digest != existing.reservationDigest {
			return ReviewFixReservationResult{}, fmt.Errorf("review fix application has an invalid durable reservation binding")
		}
		if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, true); err != nil {
			return ReviewFixReservationResult{}, err
		}
		return ReviewFixReservationResult{state: state, reservation: reservation}, nil
	}
	if attempt.verifiedHead != state.head {
		return ReviewFixReservationResult{}, fmt.Errorf("review fix reservation attempt is stale against the reviewed head")
	}
	reservation, err := NewReviewFixReservation(
		state.workspaceID, state.generation, state.attemptID, state.loop.digest,
		request.Ordinal, state.RoundsUsed(), state.head, state.tree, normalizedFindings,
	)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	if state.pendingFix != nil {
		if state.pendingFix.digest != reservation.digest {
			return ReviewFixReservationResult{}, fmt.Errorf("a different review fix is already reserved")
		}
		return ReviewFixReservationResult{state: state, reservation: reservation}, nil
	}
	if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, false); err != nil {
		return ReviewFixReservationResult{}, err
	}
	domain, err := NewReserveReviewFindingFix(reservation)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	if _, err := ReduceReview(state, domain); err != nil {
		return ReviewFixReservationResult{}, err
	}
	event, err := NewReviewFindingFixReservedJournalEvent(reservation)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	record, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	_, rebuilt, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewFixReservationResult{}, err
	}
	state, _ = rebuilt.State(request.AttemptID)
	return ReviewFixReservationResult{state: state, reservation: reservation, record: record}, nil
}

type RecordReviewFixApplicationRequest struct {
	AttemptID          ID
	Ordinal            uint16
	AcceptedFindingIDs []Digest
	OccurredAt         time.Time
}

func RecordReviewFixApplication(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordReviewFixApplicationRequest,
) (ReviewState, JournalRecord, error) {
	if journal == nil || request.AttemptID.IsZero() || request.Ordinal == 0 ||
		len(request.AcceptedFindingIDs) == 0 || request.OccurredAt.IsZero() {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("record review fix application requires journal, attempt, findings, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("attempt %s has no review state", request.AttemptID)
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.reviewFixes == nil {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("attempt %s has no completed review-fix protocol state", request.AttemptID)
	}
	normalizedFindings, err := normalizeReviewFindingIDs(request.AcceptedFindingIDs)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	fixState := attempt.reviewFixes
	if request.Ordinal <= state.FixesUsed() {
		existing := state.fixes[request.Ordinal-1]
		if !equalDigestSlices(existing.findings, normalizedFindings) {
			return ReviewState{}, JournalRecord{}, fmt.Errorf("review fix application retry differs from durable state")
		}
		unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
		if err != nil {
			return ReviewState{}, JournalRecord{}, err
		}
		if err := validateAttemptReviewProtocolState(definition, unit, attempt, state, true, true); err != nil {
			return ReviewState{}, JournalRecord{}, err
		}
		return state, JournalRecord{}, nil
	}
	expectedOrdinal := state.FixesUsed() + 1
	if request.Ordinal != expectedOrdinal || fixState.Used() != expectedOrdinal ||
		!fixState.Quiescent() || len(fixState.fixes) < int(expectedOrdinal) {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("review-fix protocol has not completed the next durable ordinal")
	}
	reservation := state.pendingFix
	if reservation == nil || reservation.ordinal != expectedOrdinal ||
		!equalDigestSlices(reservation.findings, normalizedFindings) {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("review-fix protocol has no matching accepted-finding reservation")
	}
	commit := fixState.fixes[expectedOrdinal-1].commit
	if commit.parent != reservation.head || commit.parent != state.head || commit.tree.IsZero() || commit.evidence.IsZero() {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("review-fix commit does not match its exact reserved parent and evidence")
	}
	fix, err := NewApplyReviewFix(
		expectedOrdinal, reservation.digest, state.head, state.tree, commit.commit, commit.tree,
		commit.evidence, normalizedFindings,
	)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	updated, err := ReduceReview(state, fix)
	if err != nil {
		return ReviewState{}, JournalRecord{}, err
	}
	event, err := NewReviewFixAppliedJournalEvent(
		definition.workspace.id, definition.generation, request.AttemptID, state.loop.digest, fix,
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
	updated, exists = rebuilt.State(request.AttemptID)
	if !exists || updated.FixesUsed() != expectedOrdinal || updated.pendingFix != nil {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("review fix application did not rebuild to the expected durable state")
	}
	return updated, record, nil
}

type ReviewReadiness struct {
	attemptID ID
	round     uint16
	head      GitObjectID
	tree      GitObjectID
	digest    Digest
}

func (readiness ReviewReadiness) AttemptID() ID     { return readiness.attemptID }
func (readiness ReviewReadiness) Round() uint16     { return readiness.round }
func (readiness ReviewReadiness) Head() GitObjectID { return readiness.head }
func (readiness ReviewReadiness) Tree() GitObjectID { return readiness.tree }
func (readiness ReviewReadiness) Digest() Digest    { return readiness.digest }

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
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.repository, attempt.worktree, attempt.branch, state.head)
	if err != nil {
		return ReviewReadiness{}, err
	}
	snapshot, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil || !snapshot.clean || snapshot.head != state.head || snapshot.tree != state.tree {
		return ReviewReadiness{}, fmt.Errorf("review readiness is stale against repository head/tree")
	}
	readiness := ReviewReadiness{attemptID: attemptID, round: state.RoundsUsed(), head: state.head, tree: state.tree}
	type readinessJSON struct {
		SchemaVersion int    `json:"schema_version"`
		AttemptID     string `json:"attempt_id"`
		Round         uint16 `json:"round"`
		Head          string `json:"head"`
		Tree          string `json:"tree"`
		Loop          string `json:"loop_digest"`
	}
	canonical, _ := json.Marshal(readinessJSON{
		SchemaVersion: 2, AttemptID: attemptID.String(), Round: readiness.round, Head: readiness.head.String(),
		Tree: readiness.tree.String(), Loop: state.loop.digest.String(),
	})
	readiness.digest = DigestBytes(canonical)
	return readiness, nil
}

func validateAttemptReviewRebase(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	unit UnitExecution,
	attempt RuntimeAttemptProjection,
	newHead GitObjectID,
) error {
	loop, configured := unit.ReviewLoop()
	if !configured || newHead == attempt.verifiedHead {
		return nil
	}
	projection, err := RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		return err
	}
	state, exists := projection.State(attempt.attemptID)
	if !exists {
		return nil
	}
	if state.loop.digest != loop.digest {
		return fmt.Errorf("attempt %s review state does not match the configured loop", attempt.attemptID)
	}
	if state.pendingFix != nil || state.FixesUsed() != func() uint16 {
		if attempt.reviewFixes == nil {
			return 0
		}
		return attempt.reviewFixes.Used()
	}() {
		return fmt.Errorf("attempt %s cannot rebase before its reserved review fix is applied", attempt.attemptID)
	}
	if len(state.rounds) == 0 {
		return nil
	}
	latest := state.rounds[len(state.rounds)-1]
	if _, pending := pendingReviewInvocation(latest); pending || !latest.Complete(len(state.loop.profiles)) {
		return fmt.Errorf("attempt %s cannot rebase during an incomplete review round", attempt.attemptID)
	}
	if state.RoundsUsed() >= state.loop.maxRounds {
		return fmt.Errorf("attempt %s cannot rebase without remaining review round budget", attempt.attemptID)
	}
	return nil
}

func validateAttemptReviewProtocolState(
	definition EffectiveWorkspaceDefinition,
	unit UnitExecution,
	attempt RuntimeAttemptProjection,
	review ReviewState,
	hasReview bool,
	allowStaleReviewHead bool,
) error {
	expectedHead := attempt.verifiedHead
	if protocol, configured := unit.CommitProtocol(); configured {
		if attempt.commitProtocol == nil || attempt.commitProtocol.protocol.digest != protocol.digest ||
			attempt.commitProtocol.phase != CommitProtocolComplete {
			return fmt.Errorf("attempt %s cannot review before its configured commit protocol completes", attempt.attemptID)
		}
		expectedHead = attempt.commitProtocol.Head()
	} else if attempt.commitProtocol != nil {
		return fmt.Errorf("attempt %s has an unconfigured implementation commit protocol", attempt.attemptID)
	} else if attempt.reviewFixes != nil {
		expectedHead = attempt.reviewFixes.base
	}

	reviewFixProtocol, configured := unit.ReviewFixProtocol()
	if !configured {
		return fmt.Errorf("attempt %s configured review loop has no review-fix protocol", attempt.attemptID)
	}
	usedFixes := uint16(0)
	if attempt.reviewFixes != nil {
		state := attempt.reviewFixes
		if err := validateReviewFixState(*state); err != nil {
			return fmt.Errorf("attempt %s review-fix state: %w", attempt.attemptID, err)
		}
		if state.generation != definition.generation || state.protocol.digest != reviewFixProtocol.digest ||
			state.maximum != unit.policy.maxReviewFixes || state.base != expectedHead {
			return fmt.Errorf("attempt %s review-fix state does not match effective review policy", attempt.attemptID)
		}
		if !state.Quiescent() {
			return fmt.Errorf("attempt %s cannot review with an in-flight review fix", attempt.attemptID)
		}
		usedFixes = state.Used()
		expectedHead = state.Head()
	}
	if attempt.verifiedHead != expectedHead {
		return fmt.Errorf("attempt %s review head does not match its durable commit protocols", attempt.attemptID)
	}
	if !hasReview {
		if usedFixes != 0 {
			return fmt.Errorf("attempt %s cannot start review after unrecorded review fixes", attempt.attemptID)
		}
		return nil
	}
	if review.loop.digest.IsZero() || review.FixesUsed() != usedFixes {
		return fmt.Errorf("attempt %s review state does not match durable review-fix counters", attempt.attemptID)
	}
	if review.head != expectedHead && !allowStaleReviewHead {
		return fmt.Errorf("attempt %s review state does not match its durable review-fix head", attempt.attemptID)
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
	reads, writes, ok := reviewJournalEventResources(event)
	if !ok {
		return JournalRecord{}, fmt.Errorf("unsupported review journal event %T", event)
	}
	readSet := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(resource, snapshot.Revision(resource))
		readSet = append(readSet, revision)
	}
	appendRequest, err := newPrivilegedJournalAppend(event, occurredAt, readSet, writes)
	if err != nil {
		return JournalRecord{}, err
	}
	return journal.AppendIfHead(appendRequest, snapshot.head)
}
