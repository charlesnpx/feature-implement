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
	request    ReviewRequest
	repository RepositoryIdentity
	worktree   string
	branch     string
}

func newReviewInvocation(
	request ReviewRequest, repository RepositoryIdentity, worktree, branch string,
) (ReviewInvocation, error) {
	repositoryRequest, err := NewReviewRepositoryRequest(repository, worktree, branch, request.head)
	if err != nil || request.digest.IsZero() || !request.isolationRequired.Strict() {
		return ReviewInvocation{}, fmt.Errorf("review invocation requires exact request and repository input")
	}
	return ReviewInvocation{
		request: request, repository: repositoryRequest.repository,
		worktree: repositoryRequest.worktree, branch: repositoryRequest.branch,
	}, nil
}

func (invocation ReviewInvocation) Request() ReviewRequest         { return invocation.request }
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
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.repository, attempt.worktree, attempt.branch, attempt.verifiedHead)
	if err != nil {
		return ReviewRoundStartResult{}, err
	}
	repositorySnapshot, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil {
		return ReviewRoundStartResult{}, err
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

type RecordAttemptReviewResultRequest struct {
	AttemptID  ID
	Submission ReviewResultSubmission
	Receipt    ControlPlaneReceiptV2
	OccurredAt time.Time
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
		request.Submission.digest.IsZero() || request.Receipt.ReceiptDigest().IsZero() || request.OccurredAt.IsZero() {
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
			if existing.submission.requestDigest != request.Submission.requestDigest {
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
	if pending.digest != request.Submission.requestDigest || !request.Submission.isolation.Strict() {
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
		request.Submission, request.Receipt.ReceiptDigest(),
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
		receiptDigest: request.Receipt.ReceiptDigest(),
	}, record, nil
}

func ExecuteNextReviewProfile(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	runner ReviewRunnerPort,
	verifier ControlPlaneVerifierPort,
	attemptID ID,
	occurredAt time.Time,
) (VerifiedReviewResult, JournalRecord, error) {
	if runner == nil {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("execute review profile requires isolated runner")
	}
	_, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	state, exists := projection.State(attemptID)
	if !exists {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("attempt %s has no review state", attemptID)
	}
	pending, ok, err := state.NextRequest()
	if err != nil || !ok {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("attempt %s has no pending review profile", attemptID)
	}
	attempt, exists := projection.core.Attempt(attemptID)
	if !exists {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("attempt %s is unavailable", attemptID)
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
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("review repository is not the exact clean requested snapshot")
	}
	invocation, err := newReviewInvocation(pending, attempt.repository, attempt.worktree, attempt.branch)
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	output, err := runner.RunReview(ctx, invocation)
	after, inspectionErr := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if inspectionErr != nil || after.digest != before.digest {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("reviewer mutated or changed repository input")
	}
	if err != nil {
		return VerifiedReviewResult{}, JournalRecord{}, err
	}
	if !output.submission.isolation.Strict() {
		return VerifiedReviewResult{}, JournalRecord{}, fmt.Errorf("review runner did not prove strict read-only isolation")
	}
	return RecordAttemptReviewResult(ctx, journal, definition, repository, verifier, RecordAttemptReviewResultRequest{
		AttemptID: attemptID, Submission: output.submission, Receipt: output.receipt, OccurredAt: occurredAt,
	})
}

type RecordReviewFixApplicationRequest struct {
	AttemptID          ID
	AcceptedFindingIDs []Digest
	OccurredAt         time.Time
}

func RecordReviewFixApplication(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordReviewFixApplicationRequest,
) (ReviewState, JournalRecord, error) {
	if journal == nil || request.AttemptID.IsZero() || len(request.AcceptedFindingIDs) == 0 || request.OccurredAt.IsZero() {
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
	expectedOrdinal := state.FixesUsed() + 1
	fixState := attempt.reviewFixes
	if fixState.Used() != expectedOrdinal || !fixState.Quiescent() || len(fixState.fixes) == 0 {
		return ReviewState{}, JournalRecord{}, fmt.Errorf("review-fix protocol has not completed the next durable ordinal")
	}
	commit := fixState.fixes[len(fixState.fixes)-1].commit
	fix, err := NewApplyReviewFix(
		expectedOrdinal, state.head, state.tree, commit.commit, commit.tree,
		commit.evidence, request.AcceptedFindingIDs,
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

func validateAttemptReviewProtocolState(
	definition EffectiveWorkspaceDefinition,
	unit UnitExecution,
	attempt RuntimeAttemptProjection,
	review ReviewState,
	hasReview bool,
	allowStaleReviewHead bool,
) error {
	expectedHead := attempt.base
	if protocol, configured := unit.CommitProtocol(); configured {
		if attempt.commitProtocol == nil || attempt.commitProtocol.protocol.digest != protocol.digest ||
			attempt.commitProtocol.phase != CommitProtocolComplete {
			return fmt.Errorf("attempt %s cannot review before its configured commit protocol completes", attempt.attemptID)
		}
		expectedHead = attempt.commitProtocol.Head()
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
