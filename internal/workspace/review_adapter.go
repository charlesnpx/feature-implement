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
	"unicode/utf8"

	"github.com/charlesnpx/witness/contract/canonjson"
	witnesscharter "github.com/charlesnpx/witness/contract/charter"
	witnessdigest "github.com/charlesnpx/witness/contract/digest"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

const reviewDocumentDirectoryName = "review-documents"

// ReviewAdapterRepositoryPort is the narrow read-only Git boundary used by
// the document adapter. It deliberately extends the existing review snapshot
// port instead of introducing an independent shell path for patch collection.
type ReviewAdapterRepositoryPort interface {
	ReviewRepositoryPort
	ReadReviewInput(context.Context, string, GitObjectID, GitObjectID) ([]byte, error)
}

// ReviewAdapterBuildRequest identifies the already durable review invocation
// for which the external review documents are built.
type ReviewAdapterBuildRequest struct {
	AttemptID         ID
	ReservationDigest Digest
	RequestDigest     Digest
}

// ReviewAdapterMaterialization contains the deterministic request packet for
// one pending review reservation. The three serialized documents are kept as
// canonical bytes so callers can write exactly what was hashed and validated.
type ReviewAdapterMaterialization struct {
	frozen                  witnesscharter.FrozenCharter
	charterJSON             []byte
	requestJSON             []byte
	reviewInput             []byte
	charterHash             Digest
	requestDocumentDigest   Digest
	reviewInputDigest       Digest
	reservationDigest       Digest
	invocationRequestDigest Digest
	workspaceID             ID
	worktree                string
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

func (materialization ReviewAdapterMaterialization) ReservationDigest() Digest {
	return materialization.reservationDigest
}

func (materialization ReviewAdapterMaterialization) InvocationRequestDigest() Digest {
	return materialization.invocationRequestDigest
}

func (materialization ReviewAdapterMaterialization) WorkspaceID() ID {
	return materialization.workspaceID
}
func (materialization ReviewAdapterMaterialization) Worktree() string {
	return materialization.worktree
}

// BuildReviewAdapterRequest resolves the exact pending reservation and builds
// the Witness charter, review request, and Git patch it binds. Re-running this
// function against the same reservation produces the same charter and request
// digests.
//
// The patch is the raw diff of the reserved revisions, verbatim. No secret
// screening runs on it here or downstream: delegate's contract mode submits
// these bytes as supplied. The operator owns what the reserved commits
// contain.
func BuildReviewAdapterRequest(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewAdapterRepositoryPort,
	request ReviewAdapterBuildRequest,
) (ReviewAdapterMaterialization, error) {
	if ctx == nil || repository == nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("review adapter request requires context and repository adapter")
	}
	resolved, err := resolveReviewAdapterReservation(journal, definition, request)
	if err != nil {
		return ReviewAdapterMaterialization{}, err
	}
	reviewInput, err := repository.ReadReviewInput(
		ctx,
		resolved.attempt.worktree,
		resolved.attempt.base,
		resolved.reservation.request.head,
	)
	if err != nil {
		return ReviewAdapterMaterialization{}, fmt.Errorf("read exact review input: %w", err)
	}
	return buildReviewAdapterMaterialization(definition, resolved, reviewInput)
}

type reviewAdapterReservation struct {
	state       ReviewState
	attempt     RuntimeAttemptProjection
	reservation ReviewInvocationReservation
}

func resolveReviewAdapterReservation(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request ReviewAdapterBuildRequest,
) (reviewAdapterReservation, error) {
	if journal == nil || request.AttemptID.IsZero() || request.ReservationDigest.IsZero() || request.RequestDigest.IsZero() {
		return reviewAdapterReservation{}, fmt.Errorf("review adapter request requires attempt, reservation, and request digests")
	}
	_, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return reviewAdapterReservation{}, err
	}
	state, exists := projection.State(request.AttemptID)
	if !exists || len(state.rounds) == 0 {
		return reviewAdapterReservation{}, fmt.Errorf("attempt %s has no active review reservation", request.AttemptID)
	}
	attempt, exists := projection.core.Attempt(request.AttemptID)
	if !exists || attempt.phase != AttemptActive || attempt.verifiedHead != state.head {
		return reviewAdapterReservation{}, fmt.Errorf("review adapter attempt is stale or inactive")
	}
	latest := state.rounds[len(state.rounds)-1]
	reservation, pending := pendingReviewInvocation(latest)
	if !pending {
		return reviewAdapterReservation{}, fmt.Errorf("attempt %s has no pending review reservation", request.AttemptID)
	}
	if reservation.digest != request.ReservationDigest || reservation.request.digest != request.RequestDigest {
		return reviewAdapterReservation{}, fmt.Errorf("review adapter reservation does not match the exact pending request")
	}
	if reservation.request.attemptID != attempt.attemptID || reservation.request.head != attempt.verifiedHead ||
		reservation.request.head != state.head || reservation.request.tree != state.tree {
		return reviewAdapterReservation{}, fmt.Errorf("review adapter reservation is not bound to the active exact head/tree")
	}
	return reviewAdapterReservation{state: state, attempt: attempt, reservation: reservation}, nil
}

func buildReviewAdapterMaterialization(
	definition EffectiveWorkspaceDefinition,
	resolved reviewAdapterReservation,
	reviewInput []byte,
) (ReviewAdapterMaterialization, error) {
	goals, unitName, err := reviewAdapterGoals(definition, resolved.reservation.request.mergeUnit)
	if err != nil {
		return ReviewAdapterMaterialization{}, err
	}
	request := resolved.reservation.request
	charter := witnesscharter.Charter{
		SchemaVersion: witnesscharter.SchemaVersion,
		Goals:         goals,
		NonGoals:      []witnesscharter.Statement{},
		OwnerEvents: []witnesscharter.OwnerEvent{{
			ID:      reviewRequestOwnerEventID(request.digest),
			Type:    "review-request",
			Actor:   "feature-implement",
			Summary: unitName,
			Details: reviewAdapterRequestDetails(request),
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
		ConsumerIdentity: reviewAdapterConsumerIdentity(request.workspaceID),
		Subject: witnessreview.RequestSubject{
			Head: request.head.String(),
			Tree: request.tree.String(),
		},
		CharterHash:       frozen.CharterHash,
		ReviewInputDigest: reviewInputDigest.String(),
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
		frozen:                  frozen,
		charterJSON:             charterJSON,
		requestJSON:             requestJSON,
		reviewInput:             append([]byte(nil), reviewInput...),
		charterHash:             charterHash,
		requestDocumentDigest:   requestDocumentDigest,
		reviewInputDigest:       reviewInputDigest,
		reservationDigest:       resolved.reservation.digest,
		invocationRequestDigest: request.digest,
		workspaceID:             request.workspaceID,
		worktree:                resolved.attempt.worktree,
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
						ID:        fmt.Sprintf("%s-ac-%d", story.id, index+1),
						Statement: acceptance,
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
	return "review-request-" + hex
}

func reviewAdapterRequestDetails(request ReviewRequest) map[string]any {
	profile := request.profile
	return map[string]any{
		"workspace_id":     request.workspaceID.String(),
		"generation":       request.generation.String(),
		"attempt_id":       request.attemptID.String(),
		"plan_id":          request.mergeUnit.planID.String(),
		"merge_unit_id":    request.mergeUnit.mergeUnitID.String(),
		"round":            request.round,
		"profile_ordinal":  request.profileOrdinal,
		"invocation":       request.invocation,
		"profile_id":       profile.id.String(),
		"runner":           profile.runner.String(),
		"head":             request.head.String(),
		"tree":             request.tree.String(),
		"loop_digest":      request.loopDigest.String(),
		"request_digest":   request.digest.String(),
		"isolation_digest": request.isolationRequired.digest.String(),
	}
}

func reviewAdapterConsumerIdentity(workspaceID ID) witnessreview.Identity {
	return witnessreview.Identity{
		Kind: "feature-implement",
		ID:   workspaceID.String(),
	}
}

func requireMatchingReviewConsumerIdentity(
	reportIdentity, requestIdentity witnessreview.Identity,
) error {
	if reportIdentity != requestIdentity {
		return fmt.Errorf(
			"review report consumer identity {kind:%q id:%q} does not match review request consumer identity {kind:%q id:%q}",
			reportIdentity.Kind, reportIdentity.ID, requestIdentity.Kind, requestIdentity.ID,
		)
	}
	return nil
}

// ReviewDocumentArtifact identifies the raw report retained under the runtime
// state directory. ReportDigest is the Witness semantic report digest and
// RequestDigest is the semantic review-request-v1 digest. RawDocumentDigest
// preserves the exact submitted JSON bytes independently of
// formatting-equivalent report documents.
type ReviewDocumentArtifact struct {
	rawDocumentDigest Digest
	reportDigest      Digest
	requestDigest     Digest
	reviewInputDigest Digest
	charterHash       Digest
	path              string
}

func NewReviewDocumentArtifact(
	rawDocument []byte,
	reportDigest, requestDigest, reviewInputDigest, charterHash Digest,
) (ReviewDocumentArtifact, error) {
	artifact := ReviewDocumentArtifact{
		rawDocumentDigest: DigestBytes(rawDocument),
		reportDigest:      reportDigest,
		requestDigest:     requestDigest,
		reviewInputDigest: reviewInputDigest,
		charterHash:       charterHash,
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
	return filepath.ToSlash(filepath.Join(
		reviewDocumentDirectoryName,
		"report-"+strings.TrimPrefix(rawDocumentDigest.String(), "sha256:")+".json",
	))
}

// ReviewDocumentArtifactPath returns the artifact's absolute runtime path
// after validating that the artifact has its deterministic content address.
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

func validateReviewDocumentArtifactOperation(
	journal *WorkspaceJournal,
	artifact ReviewDocumentArtifact,
	rawDocument []byte,
) error {
	if journal == nil || len(rawDocument) == 0 || len(rawDocument) > MaxArtifactBytes ||
		DigestBytes(rawDocument) != artifact.rawDocumentDigest {
		return fmt.Errorf("review document artifact requires exact bounded raw bytes")
	}
	if err := artifact.validate(); err != nil {
		return err
	}
	return nil
}

func writeReviewDocumentArtifact(
	journal *WorkspaceJournal,
	artifact ReviewDocumentArtifact,
	rawDocument []byte,
) (bool, error) {
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
	created, err := state.writeExclusivePublished(
		artifact.path,
		rawDocument,
		0o600,
		func() error { return journal.inject(JournalFaultAfterReviewDocumentArtifactPublication) },
	)
	if err != nil {
		return false, rollbackPublishedReviewDocumentArtifact(
			journal,
			artifact,
			rawDocument,
			created,
			fmt.Errorf("retain raw review document: %w", err),
		)
	}
	if err := state.Sync(); err != nil {
		return false, rollbackPublishedReviewDocumentArtifact(
			journal,
			artifact,
			rawDocument,
			created,
			fmt.Errorf("synchronize raw review document: %w", err),
		)
	}
	stored, err = state.ReadBounded(artifact.path, MaxArtifactBytes)
	if err != nil {
		return false, rollbackPublishedReviewDocumentArtifact(
			journal,
			artifact,
			rawDocument,
			created,
			fmt.Errorf("verify retained raw review document: %w", err),
		)
	}
	if !bytes.Equal(stored, rawDocument) {
		return false, rollbackPublishedReviewDocumentArtifact(
			journal,
			artifact,
			rawDocument,
			created,
			fmt.Errorf("retained raw review document differs from the validated input"),
		)
	}
	return created, nil
}

func rollbackPublishedReviewDocumentArtifact(
	journal *WorkspaceJournal,
	artifact ReviewDocumentArtifact,
	rawDocument []byte,
	created bool,
	cause error,
) error {
	if !created {
		return cause
	}
	cleanupErr := removeReviewDocumentArtifactLocked(journal, artifact, rawDocument)
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("rollback published raw review document artifact: %w", cleanupErr)
	}
	return errors.Join(cause, cleanupErr)
}

func removeReviewDocumentArtifact(
	journal *WorkspaceJournal,
	artifact ReviewDocumentArtifact,
	rawDocument []byte,
) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.requireWriter(); err != nil {
		return err
	}
	return removeReviewDocumentArtifactLocked(journal, artifact, rawDocument)
}

func removeReviewDocumentArtifactLocked(
	journal *WorkspaceJournal,
	artifact ReviewDocumentArtifact,
	rawDocument []byte,
) error {
	removed, err := journal.runtime.state.adapter.removeFileContentExact(
		artifact.path, rawDocument, MaxArtifactBytes, journal.runtime.Verify,
	)
	if err != nil {
		return fmt.Errorf("remove raw review document artifact: %w", err)
	}
	if !removed {
		return fmt.Errorf("new raw review document artifact disappeared before cleanup")
	}
	return nil
}

// ReviewReportFindings builds the compatibility bridge into the legacy local
// review model. EvidenceDigest is sha256 of the Witness canonical JSON for
// the complete decoded ReportFinding. This keeps every bridge finding tied to
// all published finding fields without inventing a ReportWitness-only digest
// helper, which review-report-v1 does not expose.
func ReviewReportFindings(document witnessreview.ReviewReportDocument) ([]ReviewFinding, error) {
	findings := make([]ReviewFinding, 0, len(document.Findings))
	for index, reportFinding := range document.Findings {
		category, path, line, err := reviewReportFindingPresentation(reportFinding)
		if err != nil {
			return nil, fmt.Errorf("map review report finding %d presentation into local bridge: %w", index, err)
		}
		canonical, err := canonjson.Marshal(reportFinding)
		if err != nil {
			return nil, fmt.Errorf("canonicalize review report finding %d: %w", index, err)
		}
		finding, err := NewReviewFinding(ReviewFindingOptions{
			Severity:       ReviewSeverity(reportFinding.ClaimedSeverity),
			Category:       category,
			Path:           path,
			Line:           line,
			Summary:        truncateReviewBridgeSummary(reportFinding.Witness.Content),
			EvidenceDigest: DigestBytes(canonical),
		})
		if err != nil {
			return nil, fmt.Errorf("map review report finding %d into local bridge: %w", index, err)
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func reviewReportFindingPresentation(
	reportFinding witnessreview.ReportFinding,
) (ID, string, uint32, error) {
	category, err := NewID(reportFinding.Witness.Kind)
	if err != nil {
		return ID{}, "", 0, fmt.Errorf("local witness kind %q: %w", reportFinding.Witness.Kind, err)
	}
	path := ""
	line := uint32(0)
	if annotation := reportFinding.Annotation; annotation != nil {
		if annotation.Category != "" {
			if localCategory, categoryErr := NewID(annotation.Category); categoryErr == nil {
				category = localCategory
			}
		}
		path = annotation.Path
		line = annotation.Line
		if path != "" {
			localPath, pathErr := normalizeSourcePath(path)
			if pathErr == nil {
				path = localPath
			} else {
				path = ""
				line = 0
			}
		}
	}
	return category, path, line, nil
}

func truncateReviewBridgeSummary(value string) string {
	const maximum = 8192
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// NewReviewReportResultSubmission creates the existing completed-review
// result used by views and review-fix machinery after a report has already
// passed Witness's strict report validation.
func NewReviewReportResultSubmission(
	requestDigest Digest,
	reviewerInstance ID,
	isolation ReviewIsolationProof,
	document witnessreview.ReviewReportDocument,
) (ReviewResultSubmission, error) {
	findings, err := ReviewReportFindings(document)
	if err != nil {
		return ReviewResultSubmission{}, err
	}
	return NewReviewResultSubmission(ReviewResultSubmissionOptions{
		RequestDigest: requestDigest, ReviewerInstance: reviewerInstance,
		Status: ReviewResultCompleted, Findings: findings, Isolation: isolation,
	})
}

// RecordAttemptReviewDocumentRequest identifies an exact reserved review and
// carries the raw review-report-v1 output from its external reviewer.
type RecordAttemptReviewDocumentRequest struct {
	AttemptID         ID
	ReservationDigest Digest
	RequestDigest     Digest
	ReviewerInstance  ID
	Isolation         ReviewIsolationProof
	Document          []byte
	OccurredAt        time.Time
}

type RecordedReviewDocument struct {
	verified VerifiedReviewResult
	artifact ReviewDocumentArtifact
}

func (result RecordedReviewDocument) Verified() VerifiedReviewResult   { return result.verified }
func (result RecordedReviewDocument) Artifact() ReviewDocumentArtifact { return result.artifact }

func recordedReviewDocumentForRequest(
	snapshot JournalSnapshot,
	projection ReviewRuntimeProjection,
	request RecordAttemptReviewDocumentRequest,
) (RecordedReviewDocument, JournalRecord, bool, error) {
	for _, record := range snapshot.Records() {
		event, ok := record.Event().(ReviewResultRecordedJournalEvent)
		if !ok || event.AttemptID() != request.AttemptID || event.ReservationDigest() != request.ReservationDigest {
			continue
		}
		artifact, hasArtifact := event.DocumentArtifact()
		if !hasArtifact {
			return RecordedReviewDocument{}, JournalRecord{}, true,
				fmt.Errorf("review document cannot attach raw evidence to an existing result")
		}
		if artifact.RawDocumentDigest() != DigestBytes(request.Document) {
			return RecordedReviewDocument{}, JournalRecord{}, true, fmt.Errorf(
				"recorded review document raw report digest %s does not match request digest %s",
				artifact.RawDocumentDigest(),
				DigestBytes(request.Document),
			)
		}
		result := event.Result()
		if result.Status() != ReviewResultCompleted || result.RequestDigest() != request.RequestDigest ||
			result.ReviewerInstance() != request.ReviewerInstance ||
			result.Isolation().Digest() != request.Isolation.Digest() {
			return RecordedReviewDocument{}, JournalRecord{}, true,
				fmt.Errorf("recorded review document bindings do not match request")
		}
		state, exists := projection.State(request.AttemptID)
		if !exists {
			return RecordedReviewDocument{}, JournalRecord{}, true,
				fmt.Errorf("recorded review document has no rebuilt review state")
		}
		for _, round := range state.Rounds() {
			for _, verified := range round.Attempts() {
				if verified.ReservationDigest() != request.ReservationDigest {
					continue
				}
				if verified.Request().Digest() != request.RequestDigest ||
					verified.Submission().Digest() != result.Digest() {
					return RecordedReviewDocument{}, JournalRecord{}, true,
						fmt.Errorf("recorded review document result bindings do not match its journal projection")
				}
				return RecordedReviewDocument{verified: verified, artifact: artifact}, record, true, nil
			}
		}
		return RecordedReviewDocument{}, JournalRecord{}, true,
			fmt.Errorf("recorded review document result is missing from its rebuilt review state")
	}
	return RecordedReviewDocument{}, JournalRecord{}, false, nil
}

func reconcileAmbiguousReviewDocumentAppend(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	request RecordAttemptReviewDocumentRequest,
	eventHash Digest,
) (RecordedReviewDocument, JournalRecord, bool, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, false,
			fmt.Errorf("re-read journal after ambiguous review document append: %w", err)
	}
	var found bool
	for _, record := range snapshot.Records() {
		if record.EventHash() == eventHash {
			found = true
			break
		}
	}
	if !found {
		return RecordedReviewDocument{}, JournalRecord{}, false, nil
	}
	projection, err := RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, false,
			fmt.Errorf("rebuild review runtime after ambiguous review document append: %w", err)
	}
	recorded, record, exists, err := recordedReviewDocumentForRequest(snapshot, projection, request)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, false, err
	}
	if !exists || record.EventHash() != eventHash {
		return RecordedReviewDocument{}, JournalRecord{}, false, fmt.Errorf(
			"ambiguous review document event %s is not the recorded request result", eventHash,
		)
	}
	return recorded, record, true, nil
}

// RecordAttemptReviewDocument validates a raw review-report-v1 against the
// exact pending reservation's deterministic request packet, retains those
// exact raw bytes, and records the legacy finding bridge in the normal review
// result event. The retained artifact carries the semantic review-request-v1
// digest; the event's result still carries the established invocation request
// digest used by the existing review lifecycle.
func RecordAttemptReviewDocument(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	repository ReviewAdapterRepositoryPort,
	request RecordAttemptReviewDocumentRequest,
) (RecordedReviewDocument, JournalRecord, error) {
	if ctx == nil || journal == nil || repository == nil || request.AttemptID.IsZero() ||
		request.ReservationDigest.IsZero() || request.RequestDigest.IsZero() ||
		request.ReviewerInstance.IsZero() || len(request.Document) == 0 ||
		len(request.Document) > MaxArtifactBytes || request.OccurredAt.IsZero() {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf(
			"record review document requires exact reservation, reviewer, bounded document, and occurrence time",
		)
	}
	snapshot, projection, err := readReviewRuntime(journal, definition)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	if recorded, _, exists, err := recordedReviewDocumentForRequest(snapshot, projection, request); err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	} else if exists {
		return recorded, JournalRecord{}, nil
	}

	materialization, err := BuildReviewAdapterRequest(ctx, journal, definition, repository, ReviewAdapterBuildRequest{
		AttemptID: request.AttemptID, ReservationDigest: request.ReservationDigest, RequestDigest: request.RequestDigest,
	})
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	document, err := witnessreview.DecodeAndValidateReviewReport(
		request.Document, materialization.FrozenCharter(), materialization.ReviewInputDigest().String(),
	)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("validate review report document: %w", err)
	}
	if len(document.Findings) > maxReviewFindings {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf(
			"review report findings count %d exceeds legacy limit %d",
			len(document.Findings),
			maxReviewFindings,
		)
	}
	if err := requireMatchingReviewConsumerIdentity(
		document.ConsumerIdentity,
		reviewAdapterConsumerIdentity(materialization.WorkspaceID()),
	); err != nil {
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
	submission, err := NewReviewReportResultSubmission(
		materialization.InvocationRequestDigest(), request.ReviewerInstance, request.Isolation, document,
	)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	prepared, err := prepareAttemptReviewResult(ctx, journal, definition, repository, RecordAttemptReviewResultRequest{
		AttemptID: request.AttemptID, ReservationDigest: request.ReservationDigest,
		Submission: submission, OccurredAt: request.OccurredAt,
	})
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	if prepared.alreadyRecorded {
		snapshot, projection, err := readReviewRuntime(journal, definition)
		if err != nil {
			return RecordedReviewDocument{}, JournalRecord{}, err
		}
		recorded, _, exists, err := recordedReviewDocumentForRequest(snapshot, projection, request)
		if err != nil {
			return RecordedReviewDocument{}, JournalRecord{}, err
		}
		if exists {
			return recorded, JournalRecord{}, nil
		}
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf(
			"review document cannot attach raw evidence to an existing result",
		)
	}
	artifact, err := NewReviewDocumentArtifact(
		request.Document, reportDigest, materialization.RequestDocumentDigest(),
		materialization.ReviewInputDigest(), materialization.CharterHash(),
	)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	if err := validateReviewDocumentArtifactOperation(journal, artifact, request.Document); err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	event, err := NewReviewResultRecordedDocumentJournalEvent(
		definition.workspace.id, definition.generation, request.AttemptID, prepared.state.loop.digest,
		prepared.domain, artifact,
	)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	created, err := writeReviewDocumentArtifact(journal, artifact, request.Document)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	record, err := appendReviewJournalEvent(journal, prepared.snapshot, event, request.OccurredAt)
	if err != nil {
		var ambiguous JournalAppendAmbiguousError
		if errors.As(err, &ambiguous) {
			recorded, recovered, durable, reconcileErr := reconcileAmbiguousReviewDocumentAppend(
				journal,
				definition,
				request,
				ambiguous.EventHash,
			)
			if reconcileErr != nil {
				return RecordedReviewDocument{}, JournalRecord{}, errors.Join(
					err,
					fmt.Errorf("reconcile ambiguous review document append: %w", reconcileErr),
				)
			}
			if durable {
				return recorded, recovered, nil
			}
		}
		if created {
			if cleanupErr := removeReviewDocumentArtifact(journal, artifact, request.Document); cleanupErr != nil {
				return RecordedReviewDocument{}, JournalRecord{}, errors.Join(
					err,
					fmt.Errorf("remove new raw review document after journal append failure: %w", cleanupErr),
				)
			}
		}
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	return RecordedReviewDocument{verified: prepared.verified, artifact: artifact}, record, nil
}
