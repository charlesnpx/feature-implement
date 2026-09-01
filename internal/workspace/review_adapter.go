package workspace

import (
	"bytes"
	"context"
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
	charter               witnesscharter.Charter
	frozen                witnesscharter.FrozenCharter
	request               witnessreview.ReviewRequestDocument
	charterJSON           []byte
	requestJSON           []byte
	reviewInput           []byte
	charterHash           Digest
	requestDocumentDigest Digest
	reviewInputDigest     Digest
	reservation           ReviewInvocationReservation
	attempt               RuntimeAttemptProjection
}

func (materialization ReviewAdapterMaterialization) Charter() witnesscharter.Charter {
	return materialization.charter
}

func (materialization ReviewAdapterMaterialization) FrozenCharter() witnesscharter.FrozenCharter {
	return materialization.frozen
}

func (materialization ReviewAdapterMaterialization) Request() witnessreview.ReviewRequestDocument {
	return materialization.request
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

func (materialization ReviewAdapterMaterialization) Reservation() ReviewInvocationReservation {
	return materialization.reservation
}

func (materialization ReviewAdapterMaterialization) Attempt() RuntimeAttemptProjection {
	return materialization.attempt
}

// BuildReviewAdapterRequest resolves the exact pending reservation and builds
// the Witness charter, review request, and Git patch it binds. Re-running this
// function against the same reservation produces the same charter and request
// digests.
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
		ConsumerIdentity: map[string]any{"kind": "feature-implement", "id": request.workspaceID.String()},
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
		charter:               charter,
		frozen:                frozen,
		request:               requestDocument,
		charterJSON:           charterJSON,
		requestJSON:           requestJSON,
		reviewInput:           append([]byte(nil), reviewInput...),
		charterHash:           charterHash,
		requestDocumentDigest: requestDocumentDigest,
		reviewInputDigest:     reviewInputDigest,
		reservation:           resolved.reservation,
		attempt:               resolved.attempt,
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
				story, exists := stories[storyID.String()]
				if !exists {
					return nil, "", fmt.Errorf("review adapter merge unit %s references unavailable story %s", reference, storyID)
				}
				for index, acceptance := range story.acceptance {
					goals = append(goals, witnesscharter.Statement{
						ID:        fmt.Sprintf("%s-ac-%d", story.id, index+1),
						Statement: acceptance,
					})
				}
			}
			if len(goals) == 0 {
				fallback := "merge-unit-" + unit.id.String()
				goals = append(goals, witnesscharter.Statement{ID: fallback, Statement: fallback})
			}
			name := strings.TrimSpace(unit.name)
			if name == "" {
				name = unit.id.String()
			}
			return goals, name, nil
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

func writeReviewDocumentArtifact(
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

	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.requireWriter(); err != nil {
		return err
	}
	state := journal.runtime.state
	if err := state.EnsureDirectory(reviewDocumentDirectoryName, 0o700); err != nil {
		return fmt.Errorf("create review document artifact directory: %w", err)
	}
	stored, err := state.ReadBounded(artifact.path, MaxArtifactBytes)
	if err == nil {
		if !bytes.Equal(stored, rawDocument) {
			return fmt.Errorf("review document artifact path already retains different bytes")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect review document artifact: %w", err)
	}
	if err := state.WriteExclusive(artifact.path, rawDocument, 0o600); err != nil {
		return fmt.Errorf("retain raw review document: %w", err)
	}
	if err := state.Sync(); err != nil {
		return fmt.Errorf("synchronize raw review document: %w", err)
	}
	stored, err = state.ReadBounded(artifact.path, MaxArtifactBytes)
	if err != nil {
		return fmt.Errorf("verify retained raw review document: %w", err)
	}
	if !bytes.Equal(stored, rawDocument) {
		return fmt.Errorf("retained raw review document differs from the validated input")
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
		categoryText := reportFinding.Witness.Kind
		path := ""
		line := uint32(0)
		if reportFinding.Annotation != nil {
			if reportFinding.Annotation.Category != "" {
				categoryText = reportFinding.Annotation.Category
			}
			path = reportFinding.Annotation.Path
			line = reportFinding.Annotation.Line
		}
		category, err := NewID(categoryText)
		if err != nil {
			return nil, fmt.Errorf("review report finding %d category %q is incompatible with the local review bridge: %w", index, categoryText, err)
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
	reportDigestText, err := witnessreview.ReviewReportDigest(document)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("digest validated review report document: %w", err)
	}
	reportDigest, err := ParseDigest(reportDigestText)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("parse validated review report digest: %w", err)
	}
	submission, err := NewReviewReportResultSubmission(
		materialization.Reservation().Request().Digest(), request.ReviewerInstance, request.Isolation, document,
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
		return RecordedReviewDocument{}, JournalRecord{}, fmt.Errorf("review document cannot attach raw evidence to an existing result")
	}
	artifact, err := NewReviewDocumentArtifact(
		request.Document, reportDigest, materialization.RequestDocumentDigest(),
		materialization.ReviewInputDigest(), materialization.CharterHash(),
	)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	if err := writeReviewDocumentArtifact(journal, artifact, request.Document); err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	event, err := NewReviewResultRecordedDocumentJournalEvent(
		definition.workspace.id, definition.generation, request.AttemptID, prepared.state.loop.digest,
		prepared.domain, artifact,
	)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	record, err := appendReviewJournalEvent(journal, prepared.snapshot, event, request.OccurredAt)
	if err != nil {
		return RecordedReviewDocument{}, JournalRecord{}, err
	}
	return RecordedReviewDocument{verified: prepared.verified, artifact: artifact}, record, nil
}
