package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

// ReviewCommandResult preserves the common command envelope while exposing
// only dispatch and terminal gate facts. This command never conducts review.
type ReviewCommandResult struct {
	SchemaVersion int                     `json:"schema_version"`
	Status        string                  `json:"status"`
	Action        string                  `json:"action"`
	Detail        any                     `json:"detail,omitempty"`
	Report        workspace.WorkspaceView `json:"report"`
}

type ReviewGateDispatchView struct {
	DispatchDigest string                           `json:"dispatch_digest"`
	Adapter        string                           `json:"adapter"`
	Recipe         string                           `json:"recipe"`
	PolicyDigest   string                           `json:"policy_digest"`
	Policy         string                           `json:"policy"`
	Head           string                           `json:"head"`
	Tree           string                           `json:"tree"`
	FrozenCopy     string                           `json:"frozen_copy"`
	WitnessPacket  *WitnessReviewDispatchPacketView `json:"witness_packet,omitempty"`
}

// WitnessReviewDispatchPacketView is the deterministic handoff used to build
// a review-report-v1 document from a witness dispatch alone.
type WitnessReviewDispatchPacketView struct {
	CharterDocument json.RawMessage `json:"charter_document"`
	RequestDocument json.RawMessage `json:"request_document"`
	ReviewInput     string          `json:"review_input"`
}

type ReviewGateRecordView struct {
	DispatchDigest string `json:"dispatch_digest"`
	GateRecord     string `json:"gate_record_digest"`
	Adapter        string `json:"adapter"`
	Recipe         string `json:"recipe"`
	PolicyDigest   string `json:"policy_digest"`
	Head           string `json:"head"`
	Tree           string `json:"tree"`
	Verdict        string `json:"verdict"`
	EvidenceDigest string `json:"evidence_digest"`
	OccurredAt     string `json:"occurred_at"`
}

type ReviewReadinessView struct {
	Digest           string `json:"digest"`
	WorkspaceID      string `json:"workspace_id"`
	Generation       string `json:"generation"`
	AttemptID        string `json:"attempt_id"`
	PlanID           string `json:"plan_id"`
	MergeUnitID      string `json:"merge_unit_id"`
	Head             string `json:"head"`
	Tree             string `json:"tree"`
	DispatchDigest   string `json:"dispatch_digest"`
	GateRecordDigest string `json:"gate_record_digest"`
}

type dispatchReviewGateInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	AttemptID     string `json:"attempt_id"`
}

type recordReviewGateInput struct {
	SchemaVersion  int    `json:"schema_version"`
	OccurredAt     string `json:"occurred_at"`
	AttemptID      string `json:"attempt_id"`
	DispatchDigest string `json:"dispatch_digest"`
	Verdict        string `json:"verdict"`
	EvidenceDigest string `json:"evidence_digest"`
}

type recordReviewDocumentInput struct {
	SchemaVersion  int             `json:"schema_version"`
	OccurredAt     string          `json:"occurred_at"`
	AttemptID      string          `json:"attempt_id"`
	DispatchDigest string          `json:"dispatch_digest"`
	Verdict        string          `json:"verdict"`
	Document       json.RawMessage `json:"document"`
}

type ReviewDocumentRecordDetail struct {
	GateRecord      ReviewGateRecordView `json:"gate_record"`
	RawDocumentPath string               `json:"raw_document_path"`
}

func executeReview(ctx context.Context, bundle workspace.WorkspaceBundle, options Options) (any, error) {
	definition := bundle.Definition()
	repository := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
	switch options.Subaction {
	case "dispatch":
		var input dispatchReviewGateInput
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
		journal, _, err := openWritableJournal(options)
		if err != nil {
			return nil, err
		}
		defer journal.Close()
		dispatched, err := workspace.DispatchAttemptReviewGate(
			ctx, journal, definition, repository, workspace.DefaultLocalAttemptGitAdapter(),
			workspace.ReviewGateDispatchRequest{AttemptID: attemptID, OccurredAt: occurredAt},
		)
		if err != nil {
			return nil, err
		}
		detail := reviewGateDispatchView(dispatched)
		if workspace.ReviewGateCarriesDocumentContract(dispatched.Dispatch().Adapter()) {
			materialization, err := workspace.BuildReviewAdapterRequest(
				ctx, journal, definition, repository, workspace.ReviewAdapterBuildRequest{
					AttemptID: attemptID, DispatchDigest: dispatched.Dispatch().Digest(),
				},
			)
			if err != nil {
				return nil, err
			}
			detail.WitnessPacket = witnessReviewDispatchPacketView(materialization)
		}
		return reviewCommandResult("review.dispatch", detail, journal, definition)
	case "record":
		var input recordReviewGateInput
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
		dispatch, err := parseDigest(input.DispatchDigest, "dispatch_digest")
		if err != nil {
			return nil, err
		}
		verdict, err := parseReviewGateVerdict(input.Verdict)
		if err != nil {
			return nil, err
		}
		evidence, err := parseDigest(input.EvidenceDigest, "evidence_digest")
		if err != nil {
			return nil, err
		}
		journal, _, err := openWritableJournal(options)
		if err != nil {
			return nil, err
		}
		defer journal.Close()
		recorded, err := workspace.RecordAttemptReviewGate(journal, definition, workspace.RecordAttemptReviewGateRequest{
			AttemptID: attemptID, DispatchDigest: dispatch, Verdict: verdict,
			EvidenceDigest: evidence, OccurredAt: occurredAt,
		})
		if err != nil {
			return nil, err
		}
		return reviewCommandResult("review.record", reviewGateRecordView(recorded), journal, definition)
	case "record-document":
		var input recordReviewDocumentInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		if len(input.Document) == 0 || len(input.Document) > workspace.MaxArtifactBytes {
			return nil, fmt.Errorf("review document must contain at most %d raw bytes", workspace.MaxArtifactBytes)
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return nil, err
		}
		dispatch, err := parseDigest(input.DispatchDigest, "dispatch_digest")
		if err != nil {
			return nil, err
		}
		verdict, err := parseReviewGateVerdict(input.Verdict)
		if err != nil {
			return nil, err
		}
		journal, workspaceDir, err := openWritableJournal(options)
		if err != nil {
			return nil, err
		}
		defer journal.Close()
		recorded, _, err := workspace.RecordAttemptReviewDocument(
			ctx, journal, definition, repository, workspace.RecordAttemptReviewDocumentRequest{
				AttemptID: attemptID, DispatchDigest: dispatch, Verdict: verdict,
				Document: input.Document, OccurredAt: occurredAt,
			},
		)
		if err != nil {
			return nil, err
		}
		artifactPath, err := workspace.ReviewDocumentArtifactPath(workspaceDir, recorded.Artifact())
		if err != nil {
			return nil, err
		}
		return reviewCommandResult("review.record-document", ReviewDocumentRecordDetail{
			GateRecord:      reviewGateRecordView(recorded.GateRecord()),
			RawDocumentPath: artifactPath,
		}, journal, definition)
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
		workspaceDir, err := absoluteDirectory(options.WorkspaceDir, "workspace")
		if err != nil {
			return nil, err
		}
		journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
		if err != nil {
			return nil, err
		}
		defer journal.Close()
		readiness, err := workspace.ConfirmReviewMergeReadiness(ctx, journal, definition, repository, attemptID)
		if err != nil {
			return nil, err
		}
		return reviewReadResult("review.ready", reviewReadinessView(readiness), journal, definition)
	default:
		return nil, fmt.Errorf("unsupported workspace review action %q", options.Subaction)
	}
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

func reviewReadResult(action string, detail any, journal *workspace.WorkspaceJournal, definition workspace.EffectiveWorkspaceDefinition) (ReviewCommandResult, error) {
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

func reviewGateDispatchView(result workspace.ReviewGateDispatchResult) ReviewGateDispatchView {
	dispatch := result.Dispatch()
	return ReviewGateDispatchView{
		DispatchDigest: dispatch.Digest().String(), Adapter: dispatch.Adapter().String(), Recipe: dispatch.Recipe().String(),
		PolicyDigest: dispatch.PolicyDigest().String(), Policy: string(result.Policy()),
		Head: dispatch.Head().String(), Tree: dispatch.Tree().String(), FrozenCopy: result.FrozenCopy(),
	}
}

func witnessReviewDispatchPacketView(materialization workspace.ReviewAdapterMaterialization) *WitnessReviewDispatchPacketView {
	return &WitnessReviewDispatchPacketView{
		CharterDocument: json.RawMessage(materialization.CharterJSON()),
		RequestDocument: json.RawMessage(materialization.RequestJSON()),
		ReviewInput:     string(materialization.ReviewInput()),
	}
}

func reviewGateRecordView(record workspace.ReviewGateRecord) ReviewGateRecordView {
	return ReviewGateRecordView{
		DispatchDigest: record.DispatchDigest().String(), GateRecord: record.Digest().String(),
		Adapter: record.Adapter().String(), Recipe: record.Recipe().String(), PolicyDigest: record.PolicyDigest().String(),
		Head: record.Head().String(), Tree: record.Tree().String(), Verdict: string(record.Verdict()),
		EvidenceDigest: record.EvidenceDigest().String(), OccurredAt: record.OccurredAt().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
}

func reviewReadinessView(readiness workspace.ReviewGateReadiness) ReviewReadinessView {
	return ReviewReadinessView{
		Digest: readiness.Digest().String(), WorkspaceID: readiness.WorkspaceID().String(), Generation: readiness.Generation().String(),
		AttemptID: readiness.AttemptID().String(), PlanID: readiness.MergeUnit().PlanID().String(),
		MergeUnitID: readiness.MergeUnit().MergeUnitID().String(), Head: readiness.Head().String(), Tree: readiness.Tree().String(),
		DispatchDigest: readiness.DispatchDigest().String(), GateRecordDigest: readiness.GateRecordDigest().String(),
	}
}

func parseReviewGateVerdict(raw string) (workspace.ReviewGateVerdict, error) {
	verdict := workspace.ReviewGateVerdict(raw)
	switch verdict {
	case workspace.ReviewGateSatisfied, workspace.ReviewGateNotSatisfied, workspace.ReviewGateFailedToRun:
		return verdict, nil
	default:
		return "", fmt.Errorf("verdict must be satisfied, not_satisfied, or failed_to_run")
	}
}
