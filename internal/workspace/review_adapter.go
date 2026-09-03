package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charlesnpx/witness/contract/canonjson"
	witnesscharter "github.com/charlesnpx/witness/contract/charter"
	witnessdigest "github.com/charlesnpx/witness/contract/digest"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

const (
	reviewDocumentDirectoryName = "review-documents"
	WitnessReviewGateAdapter    = "witness"
)

// ReviewAdapterRepositoryPort reads an exact diff from the frozen copy. It has
// no method that addresses an attempt worktree.
type ReviewAdapterRepositoryPort interface {
	ReadReviewInput(context.Context, string, GitObjectID, GitObjectID) ([]byte, error)
}

type ReviewAdapterBuildRequest struct {
	AttemptID      ID
	DispatchDigest Digest
}

// ReviewAdapterMaterialization is the deterministic input handed to a
// document-based adapter. FrozenCopy is separate from the attempt worktree.
type ReviewAdapterMaterialization struct {
	frozen                witnesscharter.FrozenCharter
	charterJSON           []byte
	requestJSON           []byte
	reviewInput           []byte
	charterHash           Digest
	requestDocumentDigest Digest
	reviewInputDigest     Digest
	dispatch              ReviewGateDispatch
	frozenCopy            string
	policy                []byte
}

func (materialization ReviewAdapterMaterialization) FrozenCharter() witnesscharter.FrozenCharter {
	return materialization.frozen
}
func (materialization ReviewAdapterMaterialization) CharterJSON() []byte {
	return append([]byte(nil), materialization.charterJSON...)
}
func (materialization ReviewAdapterMaterialization) RequestJSON() []byte {
	return append([]byte(nil), materialization.requestJSON...)
}
func (materialization ReviewAdapterMaterialization) ReviewInput() []byte {
	return append([]byte(nil), materialization.reviewInput...)
}
func (materialization ReviewAdapterMaterialization) CharterHash() Digest {
	return materialization.charterHash
}
func (materialization ReviewAdapterMaterialization) RequestDocumentDigest() Digest {
	return materialization.requestDocumentDigest
}
func (materialization ReviewAdapterMaterialization) ReviewInputDigest() Digest {
	return materialization.reviewInputDigest
}
func (materialization ReviewAdapterMaterialization) Dispatch() ReviewGateDispatch {
	return materialization.dispatch
}
func (materialization ReviewAdapterMaterialization) FrozenCopy() string {
	return materialization.frozenCopy
}
func (materialization ReviewAdapterMaterialization) Policy() []byte {
	return append([]byte(nil), materialization.policy...)
}

// BuildReviewAdapterRequest builds input from an already durable dispatch and
// its frozen copy. A retained request can therefore be rebuilt without reading
// the mutable attempt directory.
func BuildReviewAdapterRequest(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewAdapterRepositoryPort,
	request ReviewAdapterBuildRequest,
) (ReviewAdapterMaterialization, error) {
	if ctx == nil || journal == nil || repository == nil || request.AttemptID.IsZero() || request.DispatchDigest.IsZero() {
		return ReviewAdapterMaterialization{}, fmt.Errorf("review adapter request requires context, journal, adapter repository, attempt, and dispatch")
	}
	resolved, err := resolveReviewAdapterDispatch(journal, definition, request)
	if err != nil {
		return ReviewAdapterMaterialization{}, err
	}
	reviewInput, err := repository.ReadReviewInput(
		ctx, resolved.frozenCopy, resolved.attempt.base, resolved.dispatch.head,
	)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("read exact review input from frozen copy: %w", err)
	}
	return buildReviewAdapterMaterialization(definition, resolved, reviewInput)
}

type reviewAdapterDispatch struct {
	attempt    RuntimeAttemptProjection
	dispatch   ReviewGateDispatch
	config     ReviewGateConfig
	frozenCopy string
}

func resolveReviewAdapterDispatch(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request ReviewAdapterBuildRequest,
) (reviewAdapterDispatch, error) {
	_, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return reviewAdapterDispatch{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return reviewAdapterDispatch{}, fmt.Errorf("attempt %s has no review gate dispatch", request.AttemptID)
	}
	dispatch, exists := state.Dispatch(request.DispatchDigest)
	if !exists {
		return reviewAdapterDispatch{}, fmt.Errorf("review gate dispatch %s is unknown", request.DispatchDigest)
	}
	if _, recorded := state.Record(dispatch.digest); recorded {
		return reviewAdapterDispatch{}, fmt.Errorf("review gate dispatch %s is already terminal", dispatch.digest)
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != dispatch.head {
		return reviewAdapterDispatch{}, fmt.Errorf("review gate dispatch is stale against the active attempt")
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return reviewAdapterDispatch{}, err
	}
	config, configured := unit.ReviewGate()
	if !configured || !config.bound() || !reviewGateDispatchMatchesConfig(dispatch, config) {
		return reviewAdapterDispatch{}, fmt.Errorf("review gate dispatch no longer matches configured adapter policy")
	}
	if projection.core.worktreeRoot.IsZero() {
		return reviewAdapterDispatch{}, fmt.Errorf("review gate dispatch has no verified frozen-copy root")
	}
	frozenCopy, err := reviewGateFrozenCopyPath(projection.core.worktreeRoot.Path(), dispatch.digest)
	if err != nil {
		return reviewAdapterDispatch{}, err
	}
	info, err := os.Stat(frozenCopy)
	if err != nil || !info.IsDir() {
		return reviewAdapterDispatch{}, fmt.Errorf("review gate frozen copy is unavailable: %w", err)
	}
	return reviewAdapterDispatch{attempt: attempt, dispatch: dispatch, config: config, frozenCopy: frozenCopy}, nil
}

func buildReviewAdapterMaterialization(
	definition EffectiveWorkspaceDefinition,
	resolved reviewAdapterDispatch,
	reviewInput []byte,
) (ReviewAdapterMaterialization, error) {
	goals, unitName, err := reviewAdapterGoals(definition, resolved.dispatch.mergeUnit)
	if err != nil {
		return ReviewAdapterMaterialization{}, err
	}
	charter := witnesscharter.Charter{
		SchemaVersion: witnesscharter.SchemaVersion,
		Goals:         goals,
		NonGoals:      []witnesscharter.Statement{},
		OwnerEvents: []witnesscharter.OwnerEvent{{
			ID:      reviewRequestOwnerEventID(resolved.dispatch.digest),
			Type:    "review-gate-dispatch",
			Actor:   "feature-implement",
			Summary: unitName,
			Details: reviewAdapterRequestDetails(resolved.dispatch),
		}},
	}
	frozen, err := witnesscharter.Freeze(charter, nil)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("freeze review charter: %w", err)
	}
	charterHash, err := ParseDigest(frozen.CharterHash)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("parse frozen review charter hash: %w", err)
	}
	reviewInputDigest, err := ParseDigest(witnessdigest.RawBytes(reviewInput))
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("parse review input digest: %w", err)
	}
	requestDocument := witnessreview.ReviewRequestDocument{
		SchemaVersion:    witnessreview.ReviewRequestV1,
		ConsumerIdentity: reviewAdapterConsumerIdentity(resolved.dispatch.workspaceID),
		Subject: witnessreview.RequestSubject{
			Head: resolved.dispatch.head.String(), Tree: resolved.dispatch.tree.String(),
		},
		CharterHash: frozen.CharterHash, ReviewInputDigest: reviewInputDigest.String(),
	}
	if err := witnessreview.RequireValidReviewRequest(requestDocument); err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("validate constructed review request: %w", err)
	}
	requestDocumentDigestText, err := witnessreview.ReviewRequestDigest(requestDocument)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("digest constructed review request: %w", err)
	}
	requestDocumentDigest, err := ParseDigest(requestDocumentDigestText)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("parse constructed review request digest: %w", err)
	}
	charterJSON, err := canonjson.Marshal(charter)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("canonicalize review charter: %w", err)
	}
	requestJSON, err := canonjson.Marshal(requestDocument)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("canonicalize review request: %w", err)
	}
	return ReviewAdapterMaterialization{
		frozen: frozen, charterJSON: charterJSON, requestJSON: requestJSON,
		reviewInput: append([]byte(nil), reviewInput...), charterHash: charterHash,
		requestDocumentDigest: requestDocumentDigest, reviewInputDigest: reviewInputDigest,
		dispatch: resolved.dispatch, frozenCopy: resolved.frozenCopy, policy: resolved.config.Policy(),
	}, nil
}

func reviewAdapterGoals(
	definition EffectiveWorkspaceDefinition,
	reference MergeUnitReference,
) ([]witnesscharter.Statement, string, error) {
	for _, plan := range definition.plans {
		if plan.id != reference.planID {
			continue
		}
		stories := make(map[string]Story, len(plan.stories))
		for _, story := range plan.stories {
			stories[story.id.String()] = story
		}
		for _, unit := range plan.mergeUnits {
			if unit.id != reference.mergeUnitID {
				continue
			}
			goals := make([]witnesscharter.Statement, 0)
			for _, storyID := range unit.storyIDs {
				story := stories[storyID.String()]
				for index, acceptance := range story.acceptance {
					goals = append(goals, witnesscharter.Statement{
						ID: fmt.Sprintf("%s-ac-%d", story.id, index+1), Statement: acceptance,
					})
				}
			}
			return goals, unit.name, nil
		}
		return nil, "", fmt.Errorf("review adapter plan %s has no merge unit %s", reference.planID, reference.mergeUnitID)
	}
	return nil, "", fmt.Errorf("review adapter has no plan %s", reference.planID)
}

func reviewRequestOwnerEventID(digest Digest) string {
	hex := strings.TrimPrefix(digest.String(), "sha256:")
	if len(hex) > 16 {
		hex = hex[:16]
	}
	return "review-gate-" + hex
}

func reviewAdapterRequestDetails(dispatch ReviewGateDispatch) map[string]any {
	return map[string]any{
		"workspace_id": dispatch.workspaceID.String(), "generation": dispatch.generation.String(),
		"attempt_id": dispatch.attemptID.String(), "plan_id": dispatch.mergeUnit.planID.String(),
		"merge_unit_id": dispatch.mergeUnit.mergeUnitID.String(), "adapter": dispatch.adapter.String(),
		"recipe": dispatch.recipe.String(), "policy_digest": dispatch.policyDigest.String(),
		"head": dispatch.head.String(), "tree": dispatch.tree.String(), "dispatch_digest": dispatch.digest.String(),
	}
}

func reviewAdapterConsumerIdentity(workspaceID ID) witnessreview.Identity {
	return witnessreview.Identity{Kind: "feature-implement", ID: workspaceID.String()}
}

func requireMatchingReviewConsumerIdentity(reportIdentity, requestIdentity witnessreview.Identity) error {
	if reportIdentity != requestIdentity {
		return fmt.Errorf("review report consumer identity {kind:%q id:%q} does not match review request consumer identity {kind:%q id:%q}", reportIdentity.Kind, reportIdentity.ID, requestIdentity.Kind, requestIdentity.ID)
	}
	return nil
}

// ReviewDocumentArtifact identifies raw report bytes retained under the
// runtime state directory. The gate record refers to RawDocumentDigest.
type ReviewDocumentArtifact struct {
	rawDocumentDigest Digest
	reportDigest      Digest
	requestDigest     Digest
	reviewInputDigest Digest
	charterHash       Digest
	path              string
}

func NewReviewDocumentArtifact(rawDocument []byte, reportDigest, requestDigest, reviewInputDigest, charterHash Digest) (ReviewDocumentArtifact, error) {
	artifact := ReviewDocumentArtifact{
		rawDocumentDigest: DigestBytes(rawDocument), reportDigest: reportDigest, requestDigest: requestDigest,
		reviewInputDigest: reviewInputDigest, charterHash: charterHash,
	}
	if artifact.rawDocumentDigest.IsZero() || len(rawDocument) == 0 || len(rawDocument) > MaxArtifactBytes ||
		reportDigest.IsZero() || requestDigest.IsZero() || reviewInputDigest.IsZero() || charterHash.IsZero() {
		return ReviewDocumentArtifact{}, fmt.Errorf("review document artifact requires bounded raw bytes and complete digests")
	}
	artifact.path = reviewDocumentArtifactRelativePath(artifact.rawDocumentDigest)
	if err := artifact.validate(); err != nil {
		return ReviewDocumentArtifact{}, err
	}
	return artifact, nil
}

func (artifact ReviewDocumentArtifact) RawDocumentDigest() Digest { return artifact.rawDocumentDigest }
func (artifact ReviewDocumentArtifact) ReportDigest() Digest      { return artifact.reportDigest }
func (artifact ReviewDocumentArtifact) RequestDigest() Digest     { return artifact.requestDigest }
func (artifact ReviewDocumentArtifact) ReviewInputDigest() Digest { return artifact.reviewInputDigest }
func (artifact ReviewDocumentArtifact) CharterHash() Digest       { return artifact.charterHash }
func (artifact ReviewDocumentArtifact) Path() string              { return artifact.path }

func (artifact ReviewDocumentArtifact) validate() error {
	if artifact.rawDocumentDigest.IsZero() || artifact.reportDigest.IsZero() || artifact.requestDigest.IsZero() ||
		artifact.reviewInputDigest.IsZero() || artifact.charterHash.IsZero() ||
		artifact.path != reviewDocumentArtifactRelativePath(artifact.rawDocumentDigest) {
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

func validateReviewDocumentArtifactOperation(journal *WorkspaceJournal, artifact ReviewDocumentArtifact, rawDocument []byte) error {
	if journal == nil || len(rawDocument) == 0 || len(rawDocument) > MaxArtifactBytes || DigestBytes(rawDocument) != artifact.rawDocumentDigest {
		return fmt.Errorf("review document artifact requires exact bounded raw bytes")
	}
	return artifact.validate()
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

type RecordAttemptReviewDocumentRequest struct {
	AttemptID      ID
	DispatchDigest Digest
	Verdict        ReviewGateVerdict
	Document       []byte
	OccurredAt     time.Time
}

type RecordedReviewDocument struct {
	gateRecord ReviewGateRecord
	artifact   ReviewDocumentArtifact
}

func (result RecordedReviewDocument) GateRecord() ReviewGateRecord     { return result.gateRecord }
func (result RecordedReviewDocument) Artifact() ReviewDocumentArtifact { return result.artifact }

// RecordAttemptReviewDocument keeps the strict Witness decode and every raw
// binding check, then uses the raw retained report as the gate evidence. The
// report's contents never enter the local assessment model.
func RecordAttemptReviewDocument(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewAdapterRepositoryPort,
	request RecordAttemptReviewDocumentRequest,
) (RecordedReviewDocument, JournalRecord, error) {
	if ctx == nil || journal == nil || repository == nil || request.AttemptID.IsZero() || request.DispatchDigest.IsZero() ||
		!request.Verdict.valid() || len(request.Document) == 0 || len(request.Document) > MaxArtifactBytes || request.OccurredAt.IsZero() {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("record review document requires exact dispatch, verdict, bounded document, and occurrence time")
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	if recorded, record, exists, lookupErr := recordedReviewDocumentForDispatch(snapshot, request); lookupErr != nil {
		return RecordedReviewDocument{}, JournalRecord{}, lookupErr
	} else if exists {
		state, stateExists := projection.State(request.AttemptID)
		if !stateExists {
			return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("attempt %s has no review gate state", request.AttemptID)
		}
		dispatch, dispatchExists := state.Dispatch(request.DispatchDigest)
		if !dispatchExists {
			return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("review gate dispatch %s is unknown", request.DispatchDigest)
		}
		if err := discardReviewGateFrozenCopy(projection.core.worktreeRoot, dispatch); err != nil {
			return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("discard terminal review gate frozen copy: %w", err)
		}
		return recorded, record, nil
	}
	state, exists := projection.State(request.AttemptID)
	if !exists {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("attempt %s has no review gate state", request.AttemptID)
	}
	dispatch, exists := state.Dispatch(request.DispatchDigest)
	if !exists || dispatch.adapter.String() != WitnessReviewGateAdapter {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("review document requires a Witness gate dispatch")
	}
	materialization, err := BuildReviewAdapterRequest(ctx, journal, definition, repository, ReviewAdapterBuildRequest{
		AttemptID: request.AttemptID, DispatchDigest: request.DispatchDigest,
	})
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	document, err := witnessreview.DecodeAndValidateReviewReport(request.Document, materialization.FrozenCharter(), materialization.ReviewInputDigest().String())
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("validate review report document: %w", err)
	}
	if err := requireMatchingReviewConsumerIdentity(document.ConsumerIdentity, reviewAdapterConsumerIdentity(materialization.Dispatch().WorkspaceID())); err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	reportDigestText, err := witnessreview.ReviewReportDigest(document)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("digest validated review report document: %w", err)
	}
	reportDigest, err := ParseDigest(reportDigestText)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("parse validated review report digest: %w", err)
	}
	artifact, err := NewReviewDocumentArtifact(request.Document, reportDigest, materialization.RequestDocumentDigest(), materialization.ReviewInputDigest(), materialization.CharterHash())
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	if err := validateReviewDocumentArtifactOperation(journal, artifact, request.Document); err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	gateRecord, err := NewReviewGateRecord(ReviewGateRecordOptions{
		Dispatch: dispatch, Verdict: request.Verdict, EvidenceDigest: artifact.RawDocumentDigest(), OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	event, err := NewReviewGateRecordedDocumentJournalEvent(dispatch, gateRecord, artifact)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	created, err := writeReviewDocumentArtifact(journal, artifact, request.Document)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	journalRecord, err := appendReviewJournalEvent(journal, snapshot, event, request.OccurredAt)
	if err != nil {
		if recovered, existingRecord, exists, lookupErr := recheckRecordedReviewDocument(journal, request); lookupErr == nil && exists {
			if cleanupErr := discardReviewGateFrozenCopy(projection.core.worktreeRoot, dispatch); cleanupErr != nil {
				return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("discard terminal review gate frozen copy: %w", cleanupErr)
			}
			return recovered, existingRecord, nil
		}
		if created {
			if cleanupErr := removeReviewDocumentArtifact(journal, artifact, request.Document); cleanupErr != nil {
				return RecordedReviewDocument{}, JournalRecord{}, errors.Join(err, fmt.Errorf("remove new raw review document after journal append failure: %w", cleanupErr))
			}
		}
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	if err := discardReviewGateFrozenCopy(projection.core.worktreeRoot, dispatch); err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("discard terminal review gate frozen copy: %w", err)
	}
	return RecordedReviewDocument{gateRecord: gateRecord, artifact: artifact}, journalRecord, nil
}

func recordedReviewDocumentForDispatch(snapshot JournalSnapshot, request RecordAttemptReviewDocumentRequest) (RecordedReviewDocument, JournalRecord, bool, error) {
	for _, journalRecord := range snapshot.Records() {
		event, ok := journalRecord.Event().(ReviewGateRecordedJournalEvent)
		if !ok || event.Dispatch().AttemptID() != request.AttemptID || event.Dispatch().Digest() != request.DispatchDigest {
			continue
		}
		artifact, hasArtifact := event.DocumentArtifact()
		if !hasArtifact {
			return RecordedReviewDocument{}, JournalRecord{}, true, fmt.Errorf("review document cannot attach raw evidence to an existing gate record")
		}
		if artifact.RawDocumentDigest() != DigestBytes(request.Document) || event.Record().Verdict() != request.Verdict {
			return RecordedReviewDocument{}, JournalRecord{}, true, fmt.Errorf("recorded review document does not match the requested terminal record")
		}
		return RecordedReviewDocument{gateRecord: event.Record(), artifact: artifact}, journalRecord, true, nil
	}
	return RecordedReviewDocument{}, JournalRecord{}, false, nil
}

func recheckRecordedReviewDocument(journal *WorkspaceJournal, request RecordAttemptReviewDocumentRequest) (RecordedReviewDocument, JournalRecord, bool, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, false, err
	}
	return recordedReviewDocumentForDispatch(snapshot, request)
}
