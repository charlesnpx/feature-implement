package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

type reviewAdapterRepositoryStub struct {
	reviewInput  []byte
	inputErr     error
	lastWorktree string
	lastBase     workspace.GitObjectID
	lastHead     workspace.GitObjectID
}

func (repository *reviewAdapterRepositoryStub) ReadReviewInput(
	_ context.Context,
	worktree string,
	base, head workspace.GitObjectID,
) ([]byte, error) {
	repository.lastWorktree, repository.lastBase, repository.lastHead = worktree, base, head
	if repository.inputErr != nil {
		return nil, repository.inputErr
	}
	return append([]byte(nil), repository.reviewInput...), nil
}

func TestWitnessAdapterBuildsFromFrozenCopyAndRetainsRawGateEvidence(t *testing.T) {
	t.Parallel()

	harness := newWitnessReviewHarness(t)
	dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
	repository := &reviewAdapterRepositoryStub{
		reviewInput: []byte("diff --git a/src/adapter.go b/src/adapter.go\n+adapter contract\n"),
	}
	request := workspace.ReviewAdapterBuildRequest{
		AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest(),
	}
	first, err := workspace.BuildReviewAdapterRequest(
		context.Background(), harness.journal, harness.definition, repository, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.BuildReviewAdapterRequest(
		context.Background(), harness.journal, harness.definition, repository, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if repository.lastWorktree != dispatched.FrozenCopy() || repository.lastWorktree == harness.attempt.Worktree() {
		t.Fatalf("adapter input worktree = %q, frozen=%q, attempt=%q", repository.lastWorktree, dispatched.FrozenCopy(), harness.attempt.Worktree())
	}
	if first.CharterHash() != second.CharterHash() || first.RequestDocumentDigest() != second.RequestDocumentDigest() ||
		!bytes.Equal(first.CharterJSON(), second.CharterJSON()) || !bytes.Equal(first.RequestJSON(), second.RequestJSON()) {
		t.Fatalf("same gate dispatch generated non-deterministic documents: first=%#v second=%#v", first, second)
	}
	raw := validReviewAdapterReportBytes(t, first)
	recorded, record, err := workspace.RecordAttemptReviewDocument(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.RecordAttemptReviewDocumentRequest{
			AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest(),
			Verdict: workspace.ReviewGateSatisfied, Document: raw,
			OccurredAt: mustTime(t, "2026-09-03T12:00:02Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.GateRecord().EvidenceDigest() != workspace.DigestBytes(raw) ||
		recorded.GateRecord().Verdict() != workspace.ReviewGateSatisfied {
		t.Fatalf("witness gate record = %#v", recorded.GateRecord())
	}
	artifact := recorded.Artifact()
	path, err := workspace.ReviewDocumentArtifactPath(harness.workspace, artifact)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatalf("retained raw report = %q error=%v", stored, err)
	}
	event, ok := record.Event().(workspace.ReviewGateRecordedJournalEvent)
	if !ok || event.Record().EvidenceDigest() != artifact.RawDocumentDigest() {
		t.Fatalf("recorded event lost raw evidence binding: %#v", record.Event())
	}
	if document, exists := event.DocumentArtifact(); !exists ||
		document.RawDocumentDigest() != workspace.DigestBytes(raw) || document.Path() == "" {
		t.Fatalf("document evidence locator = %#v exists=%t", document, exists)
	}
}

func TestWitnessAdapterRejectsMismatchedReportBindingsBeforeRecording(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*witnessreview.ReviewReportDocument)
	}{
		{
			name: "charter hash",
			mutate: func(document *witnessreview.ReviewReportDocument) {
				document.CharterHash = workspace.DigestBytes([]byte("wrong-charter")).String()
			},
		},
		{
			name: "review input digest",
			mutate: func(document *witnessreview.ReviewReportDocument) {
				document.ReviewInputDigest = workspace.DigestBytes([]byte("wrong-input")).String()
			},
		},
		{
			name: "consumer identity",
			mutate: func(document *witnessreview.ReviewReportDocument) {
				document.ConsumerIdentity = witnessreview.Identity{Kind: "feature-implement", ID: "different-workspace"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newWitnessReviewHarness(t)
			dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
			repository := &reviewAdapterRepositoryStub{reviewInput: []byte("diff --git a/a b/a\n+change\n")}
			materialization, err := workspace.BuildReviewAdapterRequest(
				context.Background(), harness.journal, harness.definition, repository,
				workspace.ReviewAdapterBuildRequest{AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest()},
			)
			if err != nil {
				t.Fatal(err)
			}
			var document witnessreview.ReviewReportDocument
			if err := json.Unmarshal(validReviewAdapterReportBytes(t, materialization), &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(&document)
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			before := reviewAdapterJournalHead(t, harness.journal)
			if _, _, err := workspace.RecordAttemptReviewDocument(
				context.Background(), harness.journal, harness.definition, repository,
				workspace.RecordAttemptReviewDocumentRequest{
					AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest(),
					Verdict: workspace.ReviewGateNotSatisfied, Document: raw,
					OccurredAt: mustTime(t, "2026-09-03T12:00:02Z"),
				},
			); err == nil {
				t.Fatal("mismatched report was recorded")
			}
			if after := reviewAdapterJournalHead(t, harness.journal); after != before {
				t.Fatalf("rejected report changed journal head: before=%s after=%s", before, after)
			}
		})
	}
}

func validReviewAdapterReportBytes(t *testing.T, materialization workspace.ReviewAdapterMaterialization) []byte {
	t.Helper()
	goals := materialization.FrozenCharter().Charter.Goals
	if len(goals) == 0 {
		t.Fatal("adapter charter has no goals")
	}
	document := witnessreview.ReviewReportDocument{
		SchemaVersion:     witnessreview.ReviewReportV1,
		Role:              witnessreview.RoleDefect,
		CharterHash:       materialization.CharterHash().String(),
		ReviewInputDigest: materialization.ReviewInputDigest().String(),
		SourceIdentity:    witnessreview.Identity{Kind: "test-reviewer", ID: "adapter-test"},
		ConsumerIdentity: witnessreview.Identity{
			Kind: "feature-implement", ID: materialization.Dispatch().WorkspaceID().String(),
		},
		Findings: []witnessreview.ReportFinding{{
			ID: "adapter-finding", Title: "Adapter retains raw gate evidence", ClaimedSeverity: witnessreview.SeverityHigh,
			CharterGoalIDs: []string{goals[0].ID},
			Witness: witnessreview.ReportWitness{
				Kind: witnessreview.WitnessKindDefect, Strength: witnessreview.WitnessStrengthConstructed,
				Content: "The adapter retains the raw document without mapping a finding locally.",
			},
			Annotation: &witnessreview.FindingAnnotation{Path: "src/adapter.go", Line: 12, Category: "contract"},
		}},
		Evaluation: &witnessreview.ReportEvaluation{
			EvaluatedPaths: []string{"src/adapter.go"}, EvaluatedGoalIDs: []string{goals[0].ID},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func reviewAdapterJournalHead(t *testing.T, journal *workspace.WorkspaceJournal) workspace.Digest {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Head()
}

var _ workspace.ReviewAdapterRepositoryPort = (*reviewAdapterRepositoryStub)(nil)

func TestReviewAdapterRejectsTerminalDispatch(t *testing.T) {
	t.Parallel()

	harness := newWitnessReviewHarness(t)
	dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
	harness.record(t, dispatched.Dispatch(), workspace.ReviewGateFailedToRun, "2026-09-03T12:00:02Z")
	_, err := workspace.BuildReviewAdapterRequest(
		context.Background(), harness.journal, harness.definition,
		&reviewAdapterRepositoryStub{reviewInput: []byte("diff --git a/a b/a\n")},
		workspace.ReviewAdapterBuildRequest{AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest()},
	)
	if err == nil || !strings.Contains(err.Error(), "already terminal") {
		t.Fatalf("terminal dispatch build error = %v", err)
	}
}
