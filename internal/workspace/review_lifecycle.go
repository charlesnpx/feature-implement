package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type ReviewRepositoryRequest struct {
	worktree string
	head     GitObjectID
}

func NewReviewRepositoryRequest(worktree string, head GitObjectID) (ReviewRepositoryRequest, error) {
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
	canonical, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Head          string `json:"head"`
		Tree          string `json:"tree"`
		Clean         bool   `json:"clean"`
	}{SchemaVersion: 2, Head: head.String(), Tree: tree.String(), Clean: clean})
	if err != nil {
		return ReviewRepositorySnapshot{}, err
	}
	return ReviewRepositorySnapshot{head: head, tree: tree, clean: clean, digest: DigestBytes(canonical)}, nil
}

func (snapshot ReviewRepositorySnapshot) Head() GitObjectID { return snapshot.head }
func (snapshot ReviewRepositorySnapshot) Tree() GitObjectID { return snapshot.tree }
func (snapshot ReviewRepositorySnapshot) Clean() bool       { return snapshot.clean }
func (snapshot ReviewRepositorySnapshot) Digest() Digest    { return snapshot.digest }

// ReviewRepositoryPort observes an exact attempt artifact and validates the
// separately configured final-history protocol. Gate adapters never receive
// this port or the attempt worktree it addresses.
type ReviewRepositoryPort interface {
	InspectReviewSnapshot(context.Context, ReviewRepositoryRequest) (ReviewRepositorySnapshot, error)
	VerifyFinalHistory(context.Context, CommitProtocol, string, GitObjectID, GitObjectID) error
}

type ReviewGateDispatchRequest struct {
	AttemptID  ID
	OccurredAt time.Time
}

type ReviewGateDispatchResult struct {
	dispatch   ReviewGateDispatch
	frozenCopy string
	policy     []byte
}

func (result ReviewGateDispatchResult) Dispatch() ReviewGateDispatch { return result.dispatch }
func (result ReviewGateDispatchResult) FrozenCopy() string           { return result.frozenCopy }
func (result ReviewGateDispatchResult) Policy() []byte {
	return append([]byte(nil), result.policy...)
}

// DispatchAttemptReviewGate records the request before materializing a fresh
// detached copy at its exact head and tree. The adapter gets only that copy.
// A materialization failure leaves the intent durable so a resumed caller can
// distinguish it from a request that was never made.
func DispatchAttemptReviewGate(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	materializer AttemptGitPort,
	request ReviewGateDispatchRequest,
) (ReviewGateDispatchResult, error) {
	if ctx == nil || journal == nil || repository == nil || materializer == nil ||
		request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return ReviewGateDispatchResult{}, fmt.Errorf("review gate dispatch requires context, journal, repository, materializer, attempt, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive {
		return ReviewGateDispatchResult{}, fmt.Errorf("attempt %s must be active for review gate dispatch", request.AttemptID)
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	config, configured := unit.ReviewGate()
	if !configured || !config.bound() {
		return ReviewGateDispatchResult{}, fmt.Errorf("attempt %s has no configured review gate", request.AttemptID)
	}
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.worktree, attempt.verifiedHead)
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	artifact, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	if !artifact.clean {
		return ReviewGateDispatchResult{}, fmt.Errorf("review gate dispatch requires an exact committed attempt artifact")
	}
	if err := verifyAttemptFinalHistory(ctx, repository, unit, attempt, artifact.head); err != nil {
		return ReviewGateDispatchResult{}, fmt.Errorf("verify final history before review gate dispatch: %w", err)
	}
	if ReviewGateCarriesDocumentContract(config.adapter) {
		if err := validateWitnessReviewInputTransport(ctx, repository, attempt, artifact.head); err != nil {
			return ReviewGateDispatchResult{}, err
		}
	}
	if artifact.head != attempt.verifiedHead {
		adoption, adoptionErr := NewReviewHeadAdoptedJournalEvent(
			definition.workspace.id, definition.generation, attempt.attemptID, attempt.mergeUnit,
			attempt.verifiedHead, artifact.head, artifact.tree, artifact.digest,
		)
		if adoptionErr != nil {
			return ReviewGateDispatchResult{}, adoptionErr
		}
		if _, appendErr := appendReviewJournalEvent(journal, snapshot, adoption, request.OccurredAt); appendErr != nil {
			return ReviewGateDispatchResult{}, appendErr
		}
		snapshot, projection, err = readReviewRuntime(journal, definition)
		if err != nil {
			return ReviewGateDispatchResult{}, err
		}
		attempt, exists = projection.core.Attempt(request.AttemptID)
		if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != artifact.head {
			return ReviewGateDispatchResult{}, fmt.Errorf("review gate head adoption did not rebuild to the inspected artifact")
		}
	}

	terminalOrdinal := uint64(0)
	if state, exists := projection.State(attempt.attemptID); exists {
		terminalOrdinal = uint64(len(state.records))
	}
	dispatch, err := NewReviewGateDispatch(ReviewGateDispatchOptions{
		WorkspaceID: definition.workspace.id, Generation: definition.generation,
		AttemptID: attempt.attemptID, MergeUnit: attempt.mergeUnit,
		Adapter: config.adapter, Recipe: config.recipe, PolicyDigest: config.policyDigest,
		Head: artifact.head, Tree: artifact.tree, TerminalOrdinal: terminalOrdinal,
	})
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	if state, exists := projection.State(attempt.attemptID); exists {
		if pending, pendingExists := state.Pending(); pendingExists {
			if pending != dispatch {
				return ReviewGateDispatchResult{}, fmt.Errorf("attempt %s has an unresolved review gate dispatch", attempt.attemptID)
			}
			return materializeReviewGateCopy(ctx, journal, materializer, attempt, pending, config.Policy())
		}
	}
	event, err := NewReviewGateDispatchedJournalEvent(dispatch)
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	if _, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt); err != nil {
		return ReviewGateDispatchResult{}, err
	}
	return materializeReviewGateCopy(ctx, journal, materializer, attempt, dispatch, config.Policy())
}

func validateWitnessReviewInputTransport(
	ctx context.Context,
	repository ReviewRepositoryPort,
	attempt RuntimeAttemptProjection,
	head GitObjectID,
) error {
	reader, ok := repository.(ReviewAdapterRepositoryPort)
	if !ok {
		return fmt.Errorf("Witness review gate dispatch requires a review input reader")
	}
	reviewInput, err := reader.ReadReviewInput(ctx, attempt.worktree, attempt.base, head)
	if err != nil {
		return fmt.Errorf("read review input before review gate dispatch: %w", err)
	}
	if !utf8.Valid(reviewInput) {
		return fmt.Errorf("review input is non-UTF-8")
	}
	return nil
}

func materializeReviewGateCopy(
	ctx context.Context,
	journal *WorkspaceJournal,
	materializer AttemptGitPort,
	attempt RuntimeAttemptProjection,
	dispatch ReviewGateDispatch,
	policy []byte,
) (ReviewGateDispatchResult, error) {
	worktreeRoot, err := derivedWorkspaceWorktreeRootForJournal(journal)
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	frozenCopy, err := reviewGateFrozenCopyPath(worktreeRoot, dispatch.digest)
	if err != nil {
		return ReviewGateDispatchResult{}, err
	}
	inspection, err := materializer.MaterializeAttemptTree(ctx, attempt.worktree, dispatch.head, frozenCopy)
	if err != nil {
		return ReviewGateDispatchResult{}, fmt.Errorf("materialize frozen review gate copy: %w", err)
	}
	if !inspection.WorktreeExists() || !inspection.Clean() || inspection.WorktreeHead() != dispatch.head ||
		inspection.WorktreeTree() != dispatch.tree {
		return ReviewGateDispatchResult{}, fmt.Errorf("frozen review gate copy is not the dispatched exact head and tree")
	}
	return ReviewGateDispatchResult{
		dispatch: dispatch, frozenCopy: frozenCopy, policy: append([]byte(nil), policy...),
	}, nil
}

func reviewGateFrozenCopyPath(root string, dispatchDigest Digest) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) || dispatchDigest.IsZero() {
		return "", fmt.Errorf("review gate frozen copy requires an absolute root and dispatch digest")
	}
	hex := strings.TrimPrefix(dispatchDigest.String(), "sha256:")
	if len(hex) != 64 {
		return "", fmt.Errorf("review gate dispatch digest has no content-addressed path")
	}
	return filepath.Join(root, "review-gate-"+hex), nil
}

type RecordAttemptReviewGateRequest struct {
	AttemptID      ID
	DispatchDigest Digest
	Verdict        ReviewGateVerdict
	EvidenceDigest Digest
	OccurredAt     time.Time
}

// RecordAttemptReviewGate stores an opaque terminal result. Dispatches with a
// document contract use this route only to record a failed-to-run outcome.
func RecordAttemptReviewGate(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordAttemptReviewGateRequest,
) (ReviewGateRecord, error) {
	if journal == nil || request.AttemptID.IsZero() || request.DispatchDigest.IsZero() ||
		!request.Verdict.valid() || request.EvidenceDigest.IsZero() || request.OccurredAt.IsZero() {
		return ReviewGateRecord{}, fmt.Errorf("review gate record requires journal, attempt, dispatch, verdict, evidence, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewGateRecord{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return ReviewGateRecord{}, fmt.Errorf("attempt %s has no review gate dispatch", request.AttemptID)
	}
	dispatch, exists := state.Dispatch(request.DispatchDigest)
	if !exists {
		return ReviewGateRecord{}, fmt.Errorf("review gate dispatch %s is unknown for attempt %s", request.DispatchDigest, request.AttemptID)
	}
	if ReviewGateCarriesDocumentContract(dispatch.adapter) && request.Verdict != ReviewGateFailedToRun {
		return ReviewGateRecord{}, fmt.Errorf("review gate dispatch %s requires a review document for %s", request.DispatchDigest, request.Verdict)
	}
	if existing, recorded := state.Record(request.DispatchDigest); recorded {
		if existing.verdict == request.Verdict && existing.evidenceDigest == request.EvidenceDigest {
			if err := discardReviewGateFrozenCopyForJournal(journal, dispatch); err != nil {
				return ReviewGateRecord{}, err
			}
			return existing, nil
		}
		return ReviewGateRecord{}, fmt.Errorf("review gate dispatch %s already has a different terminal record", request.DispatchDigest)
	}
	gateRecord, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: request.Verdict, EvidenceDigest: request.EvidenceDigest,
		OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return ReviewGateRecord{}, err
	}
	event, err := NewReviewGateRecordedJournalEvent(dispatch, gateRecord)
	if err != nil {
		return ReviewGateRecord{}, err
	}
	if _, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt); err != nil {
		return ReviewGateRecord{}, err
	}
	if err := discardReviewGateFrozenCopyForJournal(journal, dispatch); err != nil {
		return ReviewGateRecord{}, fmt.Errorf("discard terminal review gate frozen copy: %w", err)
	}
	return gateRecord, nil
}

// discardReviewGateFrozenCopy removes only the deterministic top-level copy
// reserved by a durable gate dispatch. The rooted deletion keeps symlinks and
// replaced directory identities from escaping the verified worktree root.
// A missing copy is normal after an earlier successful terminal-record retry.
func discardReviewGateFrozenCopy(
	worktreeRoot string,
	dispatch ReviewGateDispatch,
) error {
	worktreeRoot = filepath.Clean(strings.TrimSpace(worktreeRoot))
	if !filepath.IsAbs(worktreeRoot) {
		return fmt.Errorf("review gate frozen copy has no absolute derived worktree root")
	}
	root, err := OpenVerifiedRoot(RootRoleWorktree, worktreeRoot, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open review gate frozen-copy root: %w", err)
	}
	defer root.Close()
	if err := root.VerifyPath(); err != nil {
		return fmt.Errorf("verify review gate frozen-copy root: %w", err)
	}
	copyPath, err := reviewGateFrozenCopyPath(root.Path(), dispatch.digest)
	if err != nil {
		return err
	}
	if filepath.Dir(copyPath) != root.Path() {
		return fmt.Errorf("review gate frozen copy escapes its verified root")
	}
	relative := filepath.Base(copyPath)
	if !strings.HasPrefix(relative, "review-gate-") || len(relative) != len("review-gate-")+64 {
		return fmt.Errorf("review gate frozen copy path is not dispatch-derived")
	}
	info, exists, err := root.adapter.inspectExact(relative)
	if err != nil {
		return fmt.Errorf("inspect review gate frozen copy: %w", err)
	}
	if !exists {
		return root.VerifyPath()
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("review gate frozen copy is not an exact directory and will be preserved")
	}
	identity, err := platformFileIdentity(info)
	if err != nil {
		return fmt.Errorf("identify review gate frozen copy: %w", err)
	}
	if err := root.adapter.removeDirectoryTreeIdentityExact(relative, identity); err != nil {
		return fmt.Errorf("discard review gate frozen copy: %w", err)
	}
	return root.VerifyPath()
}

func discardReviewGateFrozenCopyForJournal(
	journal *WorkspaceJournal,
	dispatch ReviewGateDispatch,
) error {
	worktreeRoot, err := derivedWorkspaceWorktreeRootForJournal(journal)
	if err != nil {
		return err
	}
	return discardReviewGateFrozenCopy(worktreeRoot, dispatch)
}

// ConfirmReviewMergeReadiness is the direct readiness path. Integration also
// independently checks the same gate fact through the workspace gate view.
func ConfirmReviewMergeReadiness(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewRepositoryPort,
	attemptID ID,
) (ReviewGateReadiness, error) {
	if ctx == nil || journal == nil || repository == nil || attemptID.IsZero() {
		return ReviewGateReadiness{}, fmt.Errorf("confirm review gate readiness requires context, journal, repository, and attempt")
	}
	_, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return ReviewGateReadiness{}, err
	}
	attempt, exists := projection.core.Attempt(attemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead.IsZero() {
		return ReviewGateReadiness{}, fmt.Errorf("attempt %s has no active exact head for review gate readiness", attemptID)
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return ReviewGateReadiness{}, err
	}
	config, configured := unit.ReviewGate()
	if !configured || !config.bound() {
		return ReviewGateReadiness{}, fmt.Errorf("attempt %s has no configured review gate", attemptID)
	}
	state, exists := projection.State(attemptID)
	if !exists {
		return ReviewGateReadiness{}, fmt.Errorf("attempt %s has no review gate state", attemptID)
	}
	repositoryRequest, err := NewReviewRepositoryRequest(attempt.worktree, attempt.verifiedHead)
	if err != nil {
		return ReviewGateReadiness{}, err
	}
	artifact, err := repository.InspectReviewSnapshot(ctx, repositoryRequest)
	if err != nil || !artifact.clean || artifact.head != attempt.verifiedHead {
		return ReviewGateReadiness{}, fmt.Errorf("review gate readiness is stale against repository head and tree")
	}
	return newReviewGateReadiness(definition, attempt, state, config, artifact.head, artifact.tree)
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
