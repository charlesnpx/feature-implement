package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type ReviewCommandResult struct {
	SchemaVersion int                     `json:"schema_version"`
	Status        string                  `json:"status"`
	Action        string                  `json:"action"`
	Detail        any                     `json:"detail,omitempty"`
	Report        workspace.WorkspaceView `json:"report"`
}

type ReviewRequestView struct {
	WorkspaceID     string `json:"workspace_id"`
	Generation      string `json:"generation"`
	AttemptID       string `json:"attempt_id"`
	PlanID          string `json:"plan_id"`
	MergeUnitID     string `json:"merge_unit_id"`
	Round           uint16 `json:"round"`
	ProfileOrdinal  uint16 `json:"profile_ordinal"`
	Invocation      uint16 `json:"invocation"`
	ProfileID       string `json:"profile_id"`
	Runner          string `json:"runner"`
	Head            string `json:"head"`
	Tree            string `json:"tree"`
	LoopDigest      string `json:"loop_digest"`
	RequestDigest   string `json:"request_digest"`
	IsolationDigest string `json:"isolation_digest"`
}

type ReviewReservationView struct {
	ReservationDigest string            `json:"reservation_digest"`
	ReviewerInstance  string            `json:"reviewer_instance"`
	IdempotencyKey    string            `json:"idempotency_key"`
	Request           ReviewRequestView `json:"request"`
}

type ReviewFixReservationView struct {
	ReservationDigest string   `json:"reservation_digest"`
	AttemptID         string   `json:"attempt_id"`
	Generation        string   `json:"generation"`
	Ordinal           uint16   `json:"ordinal"`
	Round             uint16   `json:"round"`
	Head              string   `json:"head"`
	Tree              string   `json:"tree"`
	FindingIDs        []string `json:"finding_ids"`
}

type ReviewReadinessView struct {
	Digest      string `json:"digest"`
	WorkspaceID string `json:"workspace_id"`
	Generation  string `json:"generation"`
	AttemptID   string `json:"attempt_id"`
	PlanID      string `json:"plan_id"`
	MergeUnitID string `json:"merge_unit_id"`
	Head        string `json:"head"`
	Tree        string `json:"tree"`
	Round       uint16 `json:"round"`
	Purpose     string `json:"purpose"`
}

type reserveReviewInput struct {
	SchemaVersion    int    `json:"schema_version"`
	OccurredAt       string `json:"occurred_at"`
	AttemptID        string `json:"attempt_id"`
	ReviewerInstance string `json:"reviewer_instance"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type reviewFindingInput struct {
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Path           string `json:"path"`
	Line           uint32 `json:"line"`
	Summary        string `json:"summary"`
	EvidenceDigest string `json:"evidence_digest"`
}

type isolationInput struct {
	RepositoryReadOnly bool `json:"repository_read_only"`
	ScratchEphemeral   bool `json:"scratch_ephemeral"`
	RepositoryHooks    bool `json:"repository_hooks"`
	WriteNetwork       bool `json:"write_network"`
	ExternalWrite      bool `json:"external_write"`
}

type recordReviewInput struct {
	SchemaVersion         int                  `json:"schema_version"`
	OccurredAt            string               `json:"occurred_at"`
	AttemptID             string               `json:"attempt_id"`
	ReservationDigest     string               `json:"reservation_digest"`
	RequestDigest         string               `json:"request_digest"`
	ReviewerInstance      string               `json:"reviewer_instance"`
	Status                string               `json:"status"`
	Findings              []reviewFindingInput `json:"findings"`
	InfrastructureFailure string               `json:"infrastructure_failure,omitempty"`
	Isolation             isolationInput       `json:"isolation"`
}

type reviewRequestInput struct {
	SchemaVersion     int    `json:"schema_version"`
	AttemptID         string `json:"attempt_id"`
	ReservationDigest string `json:"reservation_digest"`
	RequestDigest     string `json:"request_digest"`
	OutputDir         string `json:"output_dir"`
}

type recordReviewDocumentInput struct {
	SchemaVersion     int             `json:"schema_version"`
	OccurredAt        string          `json:"occurred_at"`
	AttemptID         string          `json:"attempt_id"`
	ReservationDigest string          `json:"reservation_digest"`
	RequestDigest     string          `json:"request_digest"`
	ReviewerInstance  string          `json:"reviewer_instance"`
	Document          json.RawMessage `json:"document"`
	Isolation         isolationInput  `json:"isolation"`
}

type ReviewAdapterRequestDetail struct {
	ReviewInput             string `json:"review_input"`
	CharterPath             string `json:"charter_path"`
	RequestPath             string `json:"request_path"`
	CharterHash             string `json:"charter_hash"`
	ReviewRequestDigest     string `json:"review_request_digest"`
	ReviewInputDigest       string `json:"review_input_digest"`
	ReservationDigest       string `json:"reservation_digest"`
	InvocationRequestDigest string `json:"invocation_request_digest"`
}

type ReviewDocumentRecordDetail struct {
	ResultDigest        string `json:"result_digest"`
	RawDocumentDigest   string `json:"raw_document_digest"`
	ReportDigest        string `json:"report_digest"`
	ReviewRequestDigest string `json:"review_request_digest"`
	ReviewInputDigest   string `json:"review_input_digest"`
	CharterHash         string `json:"charter_hash"`
	RawDocumentPath     string `json:"raw_document_path"`
}

type reviewFixInput struct {
	SchemaVersion      int      `json:"schema_version"`
	OccurredAt         string   `json:"occurred_at"`
	AttemptID          string   `json:"attempt_id"`
	Ordinal            uint16   `json:"ordinal"`
	AcceptedFindingIDs []string `json:"accepted_finding_ids"`
}

type applyReviewFixInput struct {
	SchemaVersion      int      `json:"schema_version"`
	OccurredAt         string   `json:"occurred_at"`
	AttemptID          string   `json:"attempt_id"`
	Ordinal            uint16   `json:"ordinal"`
	AcceptedFindingIDs []string `json:"accepted_finding_ids"`
	Body               string   `json:"body,omitempty"`
}

func executeReview(ctx context.Context, bundle workspace.WorkspaceBundle, options Options) (any, error) {
	definition := bundle.Definition()
	repository := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
	if options.Subaction == "request" {
		return executeReviewRequest(ctx, definition, repository, options)
	}
	journal, workspaceDir, err := openWritableJournal(options)
	if err != nil {
		return nil, err
	}
	defer journal.Close()
	switch options.Subaction {
	case "start":
		_, occurredAt, attemptID, err := decodeAttemptIDInput(options.Input)
		if err != nil {
			return nil, err
		}
		started, err := workspace.StartAttemptReviewRound(ctx, journal, definition, repository, workspace.StartAttemptReviewRoundRequest{
			AttemptID: attemptID, OccurredAt: occurredAt,
		})
		if err != nil {
			return nil, err
		}
		return reviewCommandResult("review.start", reviewRequestView(started.Request()), journal, definition)
	case "reserve":
		var input reserveReviewInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return nil, err
		}
		reviewer, err := parseID(input.ReviewerInstance, "reviewer_instance")
		if err != nil {
			return nil, err
		}
		idempotency, err := parseDigest(input.IdempotencyKey, "idempotency_key")
		if err != nil {
			return nil, err
		}
		reserved, err := workspace.ReserveAttemptReviewInvocation(journal, definition, workspace.ReserveAttemptReviewInvocationRequest{
			AttemptID: attemptID, ReviewerInstance: reviewer, IdempotencyKey: idempotency, OccurredAt: occurredAt,
		})
		if err != nil {
			return nil, err
		}
		reservation := reserved.Reservation()
		detail := ReviewReservationView{
			ReservationDigest: reservation.Digest().String(), ReviewerInstance: reservation.ReviewerInstance().String(),
			IdempotencyKey: reservation.IdempotencyKey().String(), Request: reviewRequestView(reservation.Request()),
		}
		return reviewCommandResult("review.reserve", detail, journal, definition)
	case "record":
		var input recordReviewInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return nil, err
		}
		reservation, err := parseDigest(input.ReservationDigest, "reservation_digest")
		if err != nil {
			return nil, err
		}
		requestDigest, err := parseDigest(input.RequestDigest, "request_digest")
		if err != nil {
			return nil, err
		}
		reviewer, err := parseID(input.ReviewerInstance, "reviewer_instance")
		if err != nil {
			return nil, err
		}
		findings, err := parseReviewFindings(input.Findings)
		if err != nil {
			return nil, err
		}
		infrastructure := workspace.Digest{}
		if input.InfrastructureFailure != "" {
			infrastructure, err = parseDigest(input.InfrastructureFailure, "infrastructure_failure")
			if err != nil {
				return nil, err
			}
		}
		proof := workspace.NewReviewIsolationProof(
			input.Isolation.RepositoryReadOnly, input.Isolation.ScratchEphemeral,
			input.Isolation.RepositoryHooks, input.Isolation.WriteNetwork, input.Isolation.ExternalWrite,
		)
		submission, err := workspace.NewReviewResultSubmission(workspace.ReviewResultSubmissionOptions{
			RequestDigest: requestDigest, ReviewerInstance: reviewer, Status: workspace.ReviewResultStatus(input.Status),
			Findings: findings, InfrastructureFailure: infrastructure, Isolation: proof,
		})
		if err != nil {
			return nil, err
		}
		verified, _, err := workspace.RecordAttemptReviewResult(ctx, journal, definition, repository, workspace.RecordAttemptReviewResultRequest{
			AttemptID: attemptID, ReservationDigest: reservation, Submission: submission, OccurredAt: occurredAt,
		})
		if err != nil {
			return nil, err
		}
		detail := map[string]any{
			"result_digest": verified.Submission().Digest().String(),
		}
		return reviewCommandResult("review.record", detail, journal, definition)
	case "record-document":
		var input recordReviewDocumentInput
		if err := decodeReviewDocumentInput(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return nil, err
		}
		reservation, err := parseDigest(input.ReservationDigest, "reservation_digest")
		if err != nil {
			return nil, err
		}
		requestDigest, err := parseDigest(input.RequestDigest, "request_digest")
		if err != nil {
			return nil, err
		}
		reviewer, err := parseID(input.ReviewerInstance, "reviewer_instance")
		if err != nil {
			return nil, err
		}
		proof := workspace.NewReviewIsolationProof(
			input.Isolation.RepositoryReadOnly, input.Isolation.ScratchEphemeral,
			input.Isolation.RepositoryHooks, input.Isolation.WriteNetwork, input.Isolation.ExternalWrite,
		)
		recorded, _, err := workspace.RecordAttemptReviewDocument(
			ctx, journal, definition, repository, workspace.RecordAttemptReviewDocumentRequest{
				AttemptID: attemptID, ReservationDigest: reservation, RequestDigest: requestDigest,
				ReviewerInstance: reviewer, Isolation: proof, Document: input.Document, OccurredAt: occurredAt,
			},
		)
		if err != nil {
			return nil, err
		}
		artifact := recorded.Artifact()
		artifactPath, err := workspace.ReviewDocumentArtifactPath(workspaceDir, artifact)
		if err != nil {
			return nil, err
		}
		return reviewCommandResult("review.record-document", ReviewDocumentRecordDetail{
			ResultDigest:      recorded.Verified().Submission().Digest().String(),
			RawDocumentDigest: artifact.RawDocumentDigest().String(), ReportDigest: artifact.ReportDigest().String(),
			ReviewRequestDigest: artifact.RequestDigest().String(), ReviewInputDigest: artifact.ReviewInputDigest().String(),
			CharterHash: artifact.CharterHash().String(), RawDocumentPath: artifactPath,
		}, journal, definition)
	case "reserve-fix", "apply-fix", "record-fix":
		var input reviewFixInput
		body := ""
		if options.Subaction == "apply-fix" {
			var applyInput applyReviewFixInput
			if err := decodeRequest(options.Input, &applyInput); err != nil {
				return nil, err
			}
			input = reviewFixInput{
				SchemaVersion:      applyInput.SchemaVersion,
				OccurredAt:         applyInput.OccurredAt,
				AttemptID:          applyInput.AttemptID,
				Ordinal:            applyInput.Ordinal,
				AcceptedFindingIDs: applyInput.AcceptedFindingIDs,
			}
			body = applyInput.Body
		} else if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return nil, err
		}
		findingIDs, err := parseDigestList(input.AcceptedFindingIDs, "accepted_finding_ids")
		if err != nil {
			return nil, err
		}
		if options.Subaction == "reserve-fix" {
			reserved, err := workspace.ReserveAttemptReviewFix(journal, definition, workspace.ReserveAttemptReviewFixRequest{
				AttemptID: attemptID, Ordinal: input.Ordinal, AcceptedFindingIDs: findingIDs, OccurredAt: occurredAt,
			})
			if err != nil {
				return nil, err
			}
			return reviewCommandResult("review.reserve-fix", reviewFixReservationView(reserved.Reservation()), journal, definition)
		}
		if options.Subaction == "apply-fix" {
			shell, err := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), defaultIsolatedCheckRunner())
			if err != nil {
				return nil, err
			}
			if _, err := workspace.ExecuteAttemptReviewFix(ctx, journal, definition, shell, workspace.ExecuteAttemptReviewFixRequest{
				AttemptID: attemptID, Ordinal: input.Ordinal, Body: body,
				AcceptedFindingIDs: findingIDs, OccurredAt: occurredAt,
			}); err != nil {
				return nil, err
			}
			return reviewCommandResult("review.apply-fix", nil, journal, definition)
		}
		if _, _, err := workspace.RecordReviewFixApplication(journal, definition, workspace.RecordReviewFixApplicationRequest{
			AttemptID: attemptID, Ordinal: input.Ordinal, AcceptedFindingIDs: findingIDs, OccurredAt: occurredAt,
		}); err != nil {
			return nil, err
		}
		return reviewCommandResult("review.record-fix", nil, journal, definition)
	case "ready":
		var input struct {
			SchemaVersion int    `json:"schema_version"`
			AttemptID     string `json:"attempt_id"`
		}
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		if input.SchemaVersion != requestSchemaVersion {
			return nil, fmt.Errorf("workspace command schema_version must be %d", requestSchemaVersion)
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return nil, err
		}
		readiness, err := workspace.ConfirmReviewMergeReadiness(ctx, journal, definition, repository, attemptID)
		if err != nil {
			return nil, err
		}
		return reviewReadResult("review.ready", ReviewReadinessView{
			Digest: readiness.Digest().String(), WorkspaceID: readiness.WorkspaceID().String(), Generation: readiness.Generation().String(),
			AttemptID: readiness.AttemptID().String(), PlanID: readiness.MergeUnit().PlanID().String(),
			MergeUnitID: readiness.MergeUnit().MergeUnitID().String(), Head: readiness.Head().String(), Tree: readiness.Tree().String(),
			Round: readiness.Round(), Purpose: readiness.Purpose(),
		}, journal, definition)
	default:
		return nil, fmt.Errorf("unsupported workspace review action %q", options.Subaction)
	}
}

func executeReviewRequest(
	ctx context.Context,
	definition workspace.EffectiveWorkspaceDefinition,
	repository workspace.ReviewAdapterRepositoryPort,
	options Options,
) (any, error) {
	workspaceDir, err := absoluteDirectory(options.WorkspaceDir, "workspace")
	if err != nil {
		return nil, err
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
	if err != nil {
		return nil, err
	}
	defer journal.Close()

	var input reviewRequestInput
	if err := decodeRequest(options.Input, &input); err != nil {
		return nil, err
	}
	if input.SchemaVersion != requestSchemaVersion {
		return nil, fmt.Errorf("workspace command schema_version must be %d", requestSchemaVersion)
	}
	attemptID, err := parseID(input.AttemptID, "attempt_id")
	if err != nil {
		return nil, err
	}
	reservation, err := parseDigest(input.ReservationDigest, "reservation_digest")
	if err != nil {
		return nil, err
	}
	requestDigest, err := parseDigest(input.RequestDigest, "request_digest")
	if err != nil {
		return nil, err
	}
	materialization, err := workspace.BuildReviewAdapterRequest(
		ctx, journal, definition, repository, workspace.ReviewAdapterBuildRequest{
			AttemptID: attemptID, ReservationDigest: reservation, RequestDigest: requestDigest,
		},
	)
	if err != nil {
		return nil, err
	}
	paths, err := writeReviewRequestArtifacts(
		input.OutputDir, definition.Workspace().RepositoryRoot(), materialization.Worktree(),
		materialization.CharterJSON(), materialization.RequestJSON(), materialization.ReviewInput(),
	)
	if err != nil {
		return nil, err
	}
	return reviewReadResult("review.request", ReviewAdapterRequestDetail{
		ReviewInput: paths.reviewInput,
		CharterPath: paths.charter, RequestPath: paths.request,
		CharterHash:             materialization.CharterHash().String(),
		ReviewRequestDigest:     materialization.RequestDocumentDigest().String(),
		ReviewInputDigest:       materialization.ReviewInputDigest().String(),
		ReservationDigest:       materialization.ReservationDigest().String(),
		InvocationRequestDigest: materialization.InvocationRequestDigest().String(),
	}, journal, definition)
}

const maxReviewDocumentEnvelopeBytes = workspace.MaxArtifactBytes + 64*1024

func decodeReviewDocumentInput(source []byte, target *recordReviewDocumentInput) error {
	if len(source) == 0 {
		return fmt.Errorf("workspace command requires --input <json-file|->")
	}
	if len(source) > maxReviewDocumentEnvelopeBytes {
		return fmt.Errorf("review document command input exceeds %d bytes", maxReviewDocumentEnvelopeBytes)
	}
	if err := workspace.DecodeStrictJSON(source, target); err != nil {
		return fmt.Errorf("decode workspace command input: %w", err)
	}
	if len(target.Document) == 0 || len(target.Document) > workspace.MaxArtifactBytes {
		return fmt.Errorf("review document must contain at most %d raw bytes", workspace.MaxArtifactBytes)
	}
	return nil
}

type reviewRequestArtifactPaths struct {
	charter     string
	request     string
	reviewInput string
}

func writeReviewRequestArtifacts(
	rawOutputDir, repositoryRoot, reviewWorktree string,
	charter, request, reviewInput []byte,
) (reviewRequestArtifactPaths, error) {
	outputDir, err := canonicalExistingDirectory(rawOutputDir, "review request output")
	if err != nil {
		return reviewRequestArtifactPaths{}, err
	}
	for label, rawRoot := range map[string]string{
		"repository":      repositoryRoot,
		"review worktree": reviewWorktree,
	} {
		root, rootErr := canonicalExistingDirectory(rawRoot, label)
		if rootErr != nil {
			return reviewRequestArtifactPaths{}, rootErr
		}
		if pathWithin(root, outputDir) {
			return reviewRequestArtifactPaths{}, fmt.Errorf(
				"review request output directory must be outside the %s", label,
			)
		}
	}
	files := []struct {
		name    string
		content []byte
	}{
		{name: "charter.json", content: charter},
		{name: "review-request.json", content: request},
		{name: "review-input.patch", content: reviewInput},
	}
	for _, file := range files {
		path := filepath.Join(outputDir, file.name)
		if _, err := os.Lstat(path); err == nil {
			return reviewRequestArtifactPaths{}, fmt.Errorf("refuse to overwrite existing review request artifact %s", path)
		} else if !os.IsNotExist(err) {
			return reviewRequestArtifactPaths{}, fmt.Errorf("inspect review request artifact %s: %w", path, err)
		}
	}
	paths := reviewRequestArtifactPaths{
		charter:     filepath.Join(outputDir, files[0].name),
		request:     filepath.Join(outputDir, files[1].name),
		reviewInput: filepath.Join(outputDir, files[2].name),
	}
	for _, file := range files {
		if err := writeReviewRequestArtifact(filepath.Join(outputDir, file.name), file.content); err != nil {
			return reviewRequestArtifactPaths{}, err
		}
	}
	return paths, nil
}

func canonicalExistingDirectory(value, label string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s directory must be absolute", label)
	}
	info, err := os.Stat(value)
	if err != nil {
		return "", fmt.Errorf("inspect %s directory: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s directory must exist", label)
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s directory: %w", label, err)
	}
	return filepath.Clean(resolved), nil
}

func writeReviewRequestArtifact(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write review request artifact %s: %w", path, err)
	}
	for len(content) != 0 {
		written, writeErr := file.Write(content)
		if writeErr != nil {
			_ = file.Close()
			return fmt.Errorf("write review request artifact %s: %w", path, writeErr)
		}
		if written == 0 {
			_ = file.Close()
			return fmt.Errorf("write review request artifact %s: %w", path, io.ErrShortWrite)
		}
		content = content[written:]
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("synchronize review request artifact %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close review request artifact %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("verify review request artifact %s: %w", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("review request artifact %s permissions are %04o, want 0600", path, info.Mode().Perm())
	}
	return nil
}

func reviewCommandResult(action string, detail any, journal *workspace.WorkspaceJournal, definition workspace.EffectiveWorkspaceDefinition) (ReviewCommandResult, error) {
	base, err := mutationResult(action, journal, definition)
	if err != nil {
		return ReviewCommandResult{}, err
	}
	return ReviewCommandResult{
		SchemaVersion: requestSchemaVersion, Status: base.Status, Action: action, Detail: detail, Report: base.Report,
	}, nil
}

func reviewReadResult(
	action string,
	detail any,
	journal *workspace.WorkspaceJournal,
	definition workspace.EffectiveWorkspaceDefinition,
) (ReviewCommandResult, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return ReviewCommandResult{}, err
	}
	report, err := workspace.RebuildWorkspaceView(snapshot, definition)
	if err != nil {
		return ReviewCommandResult{}, err
	}
	return ReviewCommandResult{
		SchemaVersion: requestSchemaVersion, Status: "observed", Action: action, Detail: detail, Report: report,
	}, nil
}

func reviewRequestView(request workspace.ReviewRequest) ReviewRequestView {
	return ReviewRequestView{
		WorkspaceID: request.WorkspaceID().String(), Generation: request.Generation().String(), AttemptID: request.AttemptID().String(),
		PlanID: request.MergeUnit().PlanID().String(), MergeUnitID: request.MergeUnit().MergeUnitID().String(), Round: request.Round(),
		ProfileOrdinal: request.ProfileOrdinal(), Invocation: request.Invocation(), ProfileID: request.Profile().ID().String(),
		Runner: request.Profile().Runner().String(), Head: request.Head().String(), Tree: request.Tree().String(),
		LoopDigest: request.LoopDigest().String(), RequestDigest: request.Digest().String(),
		IsolationDigest: request.IsolationRequirements().Digest().String(),
	}
}

func parseReviewFindings(inputs []reviewFindingInput) ([]workspace.ReviewFinding, error) {
	result := make([]workspace.ReviewFinding, 0, len(inputs))
	for index, input := range inputs {
		category, err := parseID(input.Category, fmt.Sprintf("findings[%d].category", index))
		if err != nil {
			return nil, err
		}
		evidence, err := parseDigest(input.EvidenceDigest, fmt.Sprintf("findings[%d].evidence_digest", index))
		if err != nil {
			return nil, err
		}
		finding, err := workspace.NewReviewFinding(workspace.ReviewFindingOptions{
			Severity: workspace.ReviewSeverity(input.Severity), Category: category, Path: input.Path,
			Line: input.Line, Summary: input.Summary, EvidenceDigest: evidence,
		})
		if err != nil {
			return nil, err
		}
		result = append(result, finding)
	}
	return result, nil
}

func parseDigestList(values []string, label string) ([]workspace.Digest, error) {
	result := make([]workspace.Digest, 0, len(values))
	for index, value := range values {
		digest, err := parseDigest(value, fmt.Sprintf("%s[%d]", label, index))
		if err != nil {
			return nil, err
		}
		result = append(result, digest)
	}
	return result, nil
}

func reviewFixReservationView(reservation workspace.ReviewFixReservation) ReviewFixReservationView {
	findings := reservation.FindingIDs()
	ids := make([]string, 0, len(findings))
	for _, finding := range findings {
		ids = append(ids, finding.String())
	}
	return ReviewFixReservationView{
		ReservationDigest: reservation.Digest().String(), AttemptID: reservation.AttemptID().String(),
		Generation: reservation.Generation().String(), Ordinal: reservation.Ordinal(), Round: reservation.Round(),
		Head: reservation.Head().String(), Tree: reservation.Tree().String(), FindingIDs: ids,
	}
}
