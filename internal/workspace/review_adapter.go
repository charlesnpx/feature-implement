package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charlesnpx/witness/contract/canonjson"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

const (
	reviewDocumentDirectoryName = "review-documents"
)

func reviewConsumerIdentity(workspaceID ID) witnessreview.Identity {
	return witnessreview.Identity{Kind: "feature-implement", ID: workspaceID.String()}
}

// ReviewDocumentArtifact identifies raw report bytes retained under the
// runtime state directory. Report bindings belong to the dispatch packet; the
// terminal record needs only the raw evidence locator.
type ReviewDocumentArtifact struct {
	rawDocumentDigest Digest
	path              string
}

func NewReviewDocumentArtifact(rawDocument []byte) (ReviewDocumentArtifact, error) {
	artifact := ReviewDocumentArtifact{rawDocumentDigest: DigestBytes(rawDocument)}
	if artifact.rawDocumentDigest.IsZero() || len(rawDocument) == 0 || len(rawDocument) > MaxArtifactBytes {
		return ReviewDocumentArtifact{}, fmt.Errorf("review document artifact requires bounded raw bytes")
	}
	artifact.path = reviewDocumentArtifactRelativePath(artifact.rawDocumentDigest)
	if err := artifact.validate(); err != nil {
		return ReviewDocumentArtifact{}, err
	}
	return artifact, nil
}

func (artifact ReviewDocumentArtifact) RawDocumentDigest() Digest { return artifact.rawDocumentDigest }
func (artifact ReviewDocumentArtifact) Path() string              { return artifact.path }

func (artifact ReviewDocumentArtifact) validate() error {
	if artifact.rawDocumentDigest.IsZero() || artifact.path != reviewDocumentArtifactRelativePath(artifact.rawDocumentDigest) {
		return fmt.Errorf("review document artifact bindings are incomplete")
	}
	return nil
}

func reviewDocumentArtifactRelativePath(rawDocumentDigest Digest) string {
	return filepath.ToSlash(filepath.Join(reviewDocumentDirectoryName, "report-"+strings.TrimPrefix(rawDocumentDigest.String(), "sha256:")+".json"))
}

func ReviewDocumentArtifactPath(workspaceDir string, artifact ReviewDocumentArtifact) (string, error) {
	if err := artifact.validate(); err != nil {
		return "", err
	}
	workspaceDir = filepath.Clean(strings.TrimSpace(workspaceDir))
	if !filepath.IsAbs(workspaceDir) {
		return "", fmt.Errorf("review document runtime directory must be absolute")
	}
	return filepath.Join(WorkspaceStateDirectory(workspaceDir), filepath.FromSlash(artifact.path)), nil
}

func writeReviewDocumentArtifact(journal *WorkspaceJournal, artifact ReviewDocumentArtifact, rawDocument []byte) (bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.requireWriter(); err != nil {
		return false, err
	}
	state := journal.runtime.state
	if err := state.EnsureDirectory(reviewDocumentDirectoryName, 0o700); err != nil {
		return false, fmt.Errorf("create review document artifact directory: %w", err)
	}
	stored, err := state.ReadBounded(artifact.path, MaxArtifactBytes)
	if err == nil {
		if !bytes.Equal(stored, rawDocument) {
			return false, fmt.Errorf("review document artifact path already retains different bytes")
		}
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect review document artifact: %w", err)
	}
	created, err := state.writeExclusivePublished(artifact.path, rawDocument, 0o600, func() error {
		return journal.inject(JournalFaultAfterReviewDocumentArtifactPublication)
	})
	if err != nil {
		return false, rollbackPublishedReviewDocumentArtifact(journal, artifact, rawDocument, created, fmt.Errorf("retain raw review document: %w", err))
	}
	if err := state.Sync(); err != nil {
		return false, rollbackPublishedReviewDocumentArtifact(journal, artifact, rawDocument, created, fmt.Errorf("synchronize raw review document: %w", err))
	}
	stored, err = state.ReadBounded(artifact.path, MaxArtifactBytes)
	if err != nil {
		return false, rollbackPublishedReviewDocumentArtifact(journal, artifact, rawDocument, created, fmt.Errorf("verify retained raw review document: %w", err))
	}
	if !bytes.Equal(stored, rawDocument) {
		return false, rollbackPublishedReviewDocumentArtifact(journal, artifact, rawDocument, created, fmt.Errorf("retained raw review document differs from the validated input"))
	}
	return created, nil
}

func rollbackPublishedReviewDocumentArtifact(journal *WorkspaceJournal, artifact ReviewDocumentArtifact, rawDocument []byte, created bool, cause error) error {
	if !created {
		return cause
	}
	cleanupErr := removeReviewDocumentArtifactLocked(journal, artifact, rawDocument)
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("rollback published raw review document artifact: %w", cleanupErr)
	}
	return errors.Join(cause, cleanupErr)
}

func removeReviewDocumentArtifact(journal *WorkspaceJournal, artifact ReviewDocumentArtifact, rawDocument []byte) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.requireWriter(); err != nil {
		return err
	}
	return removeReviewDocumentArtifactLocked(journal, artifact, rawDocument)
}

func removeReviewDocumentArtifactLocked(journal *WorkspaceJournal, artifact ReviewDocumentArtifact, rawDocument []byte) error {
	removed, err := journal.runtime.state.adapter.removeFileContentExact(artifact.path, rawDocument, MaxArtifactBytes, journal.runtime.Verify)
	if err != nil {
		return fmt.Errorf("remove raw review document artifact: %w", err)
	}
	if !removed {
		return fmt.Errorf("new raw review document artifact disappeared before cleanup")
	}
	return nil
}

// RecordAttemptReviewCompletionRequest is the in-process handoff from a
// review runner that observed execution. The request and completion are typed
// contract values rather than decoded persisted bytes: decoding a completion
// intentionally produces inert execution evidence and can never satisfy a
// gate.
type RecordAttemptReviewCompletionRequest struct {
	AttemptID      ID
	DispatchDigest Digest
	Request        witnessreview.ReviewRequestV2Document
	Completion     witnessreview.ReviewCompletionDocument
	OccurredAt     time.Time
}

// RecordedReviewCompletion contains the terminal gate fact and the retained
// completion document artifact. The completion is retained for inspection;
// readiness uses the in-process validation that happened before recording.
type RecordedReviewCompletion struct {
	gateRecord ReviewGateRecord
	artifact   ReviewDocumentArtifact
}

func (result RecordedReviewCompletion) GateRecord() ReviewGateRecord {
	return result.gateRecord
}

func (result RecordedReviewCompletion) Artifact() ReviewDocumentArtifact {
	return result.artifact
}

// RecordAttemptReviewCompletion validates a review-request-v2 /
// review-completion-v1 pair in the process that observed the review run, then
// records the completion as opaque evidence. It deliberately does not decode
// a completion from the runtime artifact: persisted completion documents are
// audit records, not re-validatable proof.
func RecordAttemptReviewCompletion(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordAttemptReviewCompletionRequest,
) (RecordedReviewCompletion, JournalRecord, error) {
	if journal == nil || request.AttemptID.IsZero() || request.DispatchDigest.IsZero() || request.OccurredAt.IsZero() {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("record review completion requires journal, attempt, dispatch, completion, and occurrence time")
	}
	if err := witnessreview.RequireValidReviewCompletion(request.Completion, request.Request); err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("validate review completion: %w", err)
	}

	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("attempt %s has no review gate dispatch", request.AttemptID)
	}
	dispatch, exists := state.Dispatch(request.DispatchDigest)
	if !exists {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("review gate dispatch %s is unknown for attempt %s", request.DispatchDigest, request.AttemptID)
	}
	if err := validateReviewCompletionDispatchBinding(dispatch, request.Request); err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, err
	}

	completionBytes, err := canonjson.Marshal(request.Completion)
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("canonicalize review completion: %w", err)
	}
	artifact, err := NewReviewDocumentArtifact(completionBytes)
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("retain review completion evidence: %w", err)
	}
	if existing, existingRecord, found, lookupErr := recordedReviewCompletionForDispatch(snapshot, request, artifact); lookupErr != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, lookupErr
	} else if found {
		if err := discardReviewGateFrozenCopyForJournal(journal, dispatch); err != nil {
			return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("discard terminal review gate frozen copy: %w", err)
		}
		return existing, existingRecord, nil
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != dispatch.head {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("review completion is stale against the active exact review-gate source")
	}

	verdict, err := reviewGateVerdictFromCompletion(request.Completion.Verdict)
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, err
	}
	gateRecord, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: verdict, EvidenceDigest: artifact.RawDocumentDigest(), OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, err
	}
	event, err := NewReviewGateRecordedDocumentJournalEvent(dispatch, gateRecord, artifact)
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, err
	}
	created, err := writeReviewDocumentArtifact(journal, artifact, completionBytes)
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, err
	}
	journalRecord, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		if recovered, recoveredRecord, found, lookupErr := recheckRecordedReviewCompletion(journal, request, artifact); lookupErr == nil && found {
			if cleanupErr := discardReviewGateFrozenCopyForJournal(journal, dispatch); cleanupErr != nil {
				return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("discard terminal review gate frozen copy: %w", cleanupErr)
			}
			return recovered, recoveredRecord, nil
		}
		if created {
			if cleanupErr := removeReviewDocumentArtifact(journal, artifact, completionBytes); cleanupErr != nil {
				return RecordedReviewCompletion{}, JournalRecord{}, errors.Join(err, fmt.Errorf("remove new review completion artifact after journal append failure: %w", cleanupErr))
			}
		}
		return RecordedReviewCompletion{}, JournalRecord{}, err
	}
	if err := discardReviewGateFrozenCopyForJournal(journal, dispatch); err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, fmt.Errorf("discard terminal review gate frozen copy: %w", err)
	}
	return RecordedReviewCompletion{gateRecord: gateRecord, artifact: artifact}, journalRecord, nil
}

func validateReviewCompletionDispatchBinding(
	dispatch ReviewGateDispatch,
	request witnessreview.ReviewRequestV2Document,
) error {
	if request.ConsumerIdentity != reviewConsumerIdentity(dispatch.workspaceID) {
		return fmt.Errorf("review request consumer identity {kind:%q id:%q} does not match workspace %s", request.ConsumerIdentity.Kind, request.ConsumerIdentity.ID, dispatch.workspaceID)
	}
	if request.Subject.Head != dispatch.head.String() {
		return fmt.Errorf("review request subject head %q does not match review gate dispatch head %q", request.Subject.Head, dispatch.head)
	}
	if strings.TrimSpace(request.Subject.Tree) == "" {
		return fmt.Errorf("review request subject tree is required for the exact review gate source")
	}
	if request.Subject.Tree != dispatch.tree.String() {
		return fmt.Errorf("review request subject tree %q does not match review gate dispatch tree %q", request.Subject.Tree, dispatch.tree)
	}
	return nil
}

func reviewGateVerdictFromCompletion(verdict string) (ReviewGateVerdict, error) {
	switch verdict {
	case witnessreview.CompletionVerdictSatisfied:
		return ReviewGateSatisfied, nil
	case witnessreview.CompletionVerdictNotSatisfied:
		return ReviewGateNotSatisfied, nil
	case witnessreview.CompletionVerdictFailedToRun:
		return ReviewGateFailedToRun, nil
	default:
		return "", fmt.Errorf("review completion verdict %q is not supported", verdict)
	}
}

func recordedReviewCompletionForDispatch(
	snapshot JournalSnapshot,
	request RecordAttemptReviewCompletionRequest,
	artifact ReviewDocumentArtifact,
) (RecordedReviewCompletion, JournalRecord, bool, error) {
	for _, journalRecord := range snapshot.Records() {
		event, ok := journalRecord.Event().(ReviewGateRecordedJournalEvent)
		if !ok || event.Dispatch().AttemptID() != request.AttemptID || event.Dispatch().Digest() != request.DispatchDigest {
			continue
		}
		recordedArtifact, hasArtifact := event.DocumentArtifact()
		if !hasArtifact {
			return RecordedReviewCompletion{}, JournalRecord{}, true, fmt.Errorf("review completion cannot attach to an existing gate record without retained evidence")
		}
		if recordedArtifact.RawDocumentDigest() != artifact.RawDocumentDigest() {
			return RecordedReviewCompletion{}, JournalRecord{}, true, fmt.Errorf("recorded review completion does not match the requested terminal record")
		}
		return RecordedReviewCompletion{gateRecord: event.Record(), artifact: recordedArtifact}, journalRecord, true, nil
	}
	return RecordedReviewCompletion{}, JournalRecord{}, false, nil
}

func recheckRecordedReviewCompletion(
	journal *WorkspaceJournal,
	request RecordAttemptReviewCompletionRequest,
	artifact ReviewDocumentArtifact,
) (RecordedReviewCompletion, JournalRecord, bool, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return RecordedReviewCompletion{}, JournalRecord{}, false, err
	}
	return recordedReviewCompletionForDispatch(snapshot, request, artifact)
}
