package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	"github.com/charlesnpx/witness/contract/canonjson"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

type reviewAdapterRepositoryStub struct {
	reviewRepositoryStub
	reviewInput    []byte
	inputErr       error
	beforeSnapshot func() error
}

func (repository *reviewAdapterRepositoryStub) InspectReviewSnapshot(
	ctx context.Context,
	request workspace.ReviewRepositoryRequest,
) (workspace.ReviewRepositorySnapshot, error) {
	if repository.beforeSnapshot != nil {
		hook := repository.beforeSnapshot
		repository.beforeSnapshot = nil
		if err := hook(); err != nil {
			return workspace.ReviewRepositorySnapshot{}, err
		}
	}
	return repository.reviewRepositoryStub.InspectReviewSnapshot(ctx, request)
}

func (repository *reviewAdapterRepositoryStub) ReadReviewInput(
	context.Context,
	string,
	workspace.GitObjectID,
	workspace.GitObjectID,
) ([]byte, error) {
	if repository.inputErr != nil {
		return nil, repository.inputErr
	}
	return append([]byte(nil), repository.reviewInput...), nil
}

func TestReviewAdapterRequestIsDeterministicForOneReservation(t *testing.T) {
	t.Parallel()
	harness, repository, reservation := newReviewAdapterReservation(t)
	request := workspace.ReviewAdapterBuildRequest{
		AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservation.Digest(),
		RequestDigest: reservation.Request().Digest(),
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
	if first.CharterHash() != second.CharterHash() ||
		first.RequestDocumentDigest() != second.RequestDocumentDigest() ||
		!bytes.Equal(first.CharterJSON(), second.CharterJSON()) ||
		!bytes.Equal(first.RequestJSON(), second.RequestJSON()) {
		t.Fatalf("same reservation generated non-deterministic request packets: first=%#v second=%#v", first, second)
	}
	if got := first.FrozenCharter().Charter.Goals; len(got) == 0 || got[0].ID != "story-one-ac-1" {
		t.Fatalf("adapter charter goals = %#v", got)
	}
}

func TestRecordAttemptReviewDocumentRetainsRawReportAndBridgesFindings(t *testing.T) {
	t.Parallel()
	harness, repository, reservation := newReviewAdapterReservation(t)
	materialization := mustReviewAdapterMaterialization(t, harness, repository, reservation)
	raw := validReviewAdapterReportBytes(t, materialization)

	recorded, record, err := workspace.RecordAttemptReviewDocument(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.RecordAttemptReviewDocumentRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservation.Digest(),
			RequestDigest: reservation.Request().Digest(), ReviewerInstance: reservation.ReviewerInstance(),
			Isolation: workspace.StrictReviewIsolationProof(), Document: raw,
			OccurredAt: mustTime(t, "2026-07-21T11:00:02Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	artifact := recorded.Artifact()
	if artifact.ReportDigest().IsZero() || artifact.RequestDigest() != materialization.RequestDocumentDigest() ||
		artifact.ReviewInputDigest() != materialization.ReviewInputDigest() || artifact.CharterHash() != materialization.CharterHash() {
		t.Fatalf("recorded review artifact bindings = %#v", artifact)
	}
	path, err := workspace.ReviewDocumentArtifactPath(harness.workspace, artifact)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, raw) {
		t.Fatalf("retained raw report differs from submitted bytes\nwant=%s\n got=%s", raw, stored)
	}
	event, ok := record.Event().(workspace.ReviewResultRecordedJournalEvent)
	if !ok {
		t.Fatalf("record event = %T", record.Event())
	}
	journalArtifact, ok := event.DocumentArtifact()
	if !ok || journalArtifact.RawDocumentDigest() != artifact.RawDocumentDigest() ||
		journalArtifact.ReportDigest() != artifact.ReportDigest() || journalArtifact.Path() != artifact.Path() {
		t.Fatalf("journal artifact reference = %#v exists=%v", journalArtifact, ok)
	}

	findings := recorded.Verified().Submission().Findings()
	if len(findings) != 1 {
		t.Fatalf("bridged review findings = %#v", findings)
	}
	bridged := findings[0]
	if bridged.Severity() != workspace.ReviewSeverityHigh || bridged.Category().String() != "contract" ||
		bridged.Path() != "src/adapter.go" || bridged.Line() != 12 || bridged.Summary() != "The adapter must retain the raw report." {
		t.Fatalf("bridged finding = %#v", bridged)
	}
	var document witnessreview.ReviewReportDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonjson.Marshal(document.Findings[0])
	if err != nil {
		t.Fatal(err)
	}
	if bridged.EvidenceDigest() != workspace.DigestBytes(canonical) {
		t.Fatalf("bridged evidence digest = %s, want %s", bridged.EvidenceDigest(), workspace.DigestBytes(canonical))
	}
}

func TestRecordAttemptReviewDocumentRejectsBeforeDurableWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		mutate              func(t *testing.T, document witnessreview.ReviewReportDocument) []byte
		mismatchReservation bool
	}{
		{
			name: "charter hash mismatch",
			mutate: func(t *testing.T, document witnessreview.ReviewReportDocument) []byte {
				document.CharterHash = workspace.DigestBytes([]byte("wrong-charter")).String()
				return marshalReviewAdapterReport(t, document)
			},
		},
		{
			name: "review input digest mismatch",
			mutate: func(t *testing.T, document witnessreview.ReviewReportDocument) []byte {
				document.ReviewInputDigest = workspace.DigestBytes([]byte("wrong-review-input")).String()
				return marshalReviewAdapterReport(t, document)
			},
		},
		{
			name: "semantic diagnostic",
			mutate: func(t *testing.T, document witnessreview.ReviewReportDocument) []byte {
				document.Findings[0].Witness.Strength = witnessreview.WitnessStrengthArgued
				return marshalReviewAdapterReport(t, document)
			},
		},
		{
			name: "strict diagnostic",
			mutate: func(t *testing.T, document witnessreview.ReviewReportDocument) []byte {
				raw := marshalReviewAdapterReport(t, document)
				var object map[string]json.RawMessage
				if err := json.Unmarshal(raw, &object); err != nil {
					t.Fatal(err)
				}
				object["unexpected"] = json.RawMessage(`true`)
				result, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				return result
			},
		},
		{
			name:                "reservation mismatch",
			mismatchReservation: true,
			mutate: func(t *testing.T, document witnessreview.ReviewReportDocument) []byte {
				return marshalReviewAdapterReport(t, document)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness, repository, reservation := newReviewAdapterReservation(t)
			materialization := mustReviewAdapterMaterialization(t, harness, repository, reservation)
			var document witnessreview.ReviewReportDocument
			if err := json.Unmarshal(validReviewAdapterReportBytes(t, materialization), &document); err != nil {
				t.Fatal(err)
			}
			before := reviewAdapterJournalHead(t, harness.journal)
			reservationDigest := reservation.Digest()
			if test.mismatchReservation {
				reservationDigest = workspace.DigestBytes([]byte("different-reservation"))
			}
			_, _, err := workspace.RecordAttemptReviewDocument(
				context.Background(), harness.journal, harness.definition, repository,
				workspace.RecordAttemptReviewDocumentRequest{
					AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservationDigest,
					RequestDigest: reservation.Request().Digest(), ReviewerInstance: reservation.ReviewerInstance(),
					Isolation: workspace.StrictReviewIsolationProof(), Document: test.mutate(t, document),
					OccurredAt: mustTime(t, "2026-07-21T11:00:02Z"),
				},
			)
			if err == nil {
				t.Fatal("invalid review document was recorded")
			}
			if after := reviewAdapterJournalHead(t, harness.journal); after != before {
				t.Fatalf("rejected document changed journal head: before=%s after=%s error=%v", before, after, err)
			}
			artifactDirectory := filepath.Join(harness.workspace, workspace.WorkspaceStateDirectoryName, "review-documents")
			if _, statErr := os.Stat(artifactDirectory); !os.IsNotExist(statErr) {
				t.Fatalf("rejected document retained an artifact directory: %v", statErr)
			}
		})
	}
}

func TestRecordAttemptReviewDocumentRemovesNewArtifactAfterStaleAppend(t *testing.T) {
	t.Parallel()
	harness, repository, reservation := newReviewAdapterReservation(t)
	materialization := mustReviewAdapterMaterialization(t, harness, repository, reservation)
	raw := validReviewAdapterReportBytes(t, materialization)
	appended := false
	repository.beforeSnapshot = func() error {
		appended = true
		_, _, err := workspace.RecordAttemptReviewInvocationFailure(
			harness.journal,
			harness.definition,
			workspace.RecordAttemptReviewInvocationFailureRequest{
				AttemptID:         harness.attempt.AttemptID(),
				ReservationDigest: reservation.Digest(),
				FailureDigest:     workspace.DigestBytes([]byte("concurrent-review-failure")),
				OccurredAt:        mustTime(t, "2026-07-21T11:00:03Z"),
			},
		)
		return err
	}

	_, _, err := workspace.RecordAttemptReviewDocument(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.RecordAttemptReviewDocumentRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservation.Digest(),
			RequestDigest: reservation.Request().Digest(), ReviewerInstance: reservation.ReviewerInstance(),
			Isolation: workspace.StrictReviewIsolationProof(), Document: raw,
			OccurredAt: mustTime(t, "2026-07-21T11:00:02Z"),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "stale journal head") {
		t.Fatalf("stale append error = %v", err)
	}
	if !appended {
		t.Fatal("concurrent journal append was not attempted")
	}
	artifactPath := filepath.Join(
		harness.workspace,
		workspace.WorkspaceStateDirectoryName,
		"review-documents",
		"report-"+strings.TrimPrefix(workspace.DigestBytes(raw).String(), "sha256:")+".json",
	)
	if _, statErr := os.Stat(artifactPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale append left raw report artifact %s: %v", artifactPath, statErr)
	}
}

func TestRecordAttemptReviewDocumentRejectsMismatchedConsumerIdentity(t *testing.T) {
	t.Parallel()
	harness, repository, reservation := newReviewAdapterReservation(t)
	materialization := mustReviewAdapterMaterialization(t, harness, repository, reservation)
	var document witnessreview.ReviewReportDocument
	if err := json.Unmarshal(validReviewAdapterReportBytes(t, materialization), &document); err != nil {
		t.Fatal(err)
	}
	document.ConsumerIdentity = map[string]any{"kind": "feature-implement", "id": "another-workspace"}
	before := reviewAdapterJournalHead(t, harness.journal)
	_, _, err := workspace.RecordAttemptReviewDocument(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.RecordAttemptReviewDocumentRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservation.Digest(),
			RequestDigest: reservation.Request().Digest(), ReviewerInstance: reservation.ReviewerInstance(),
			Isolation: workspace.StrictReviewIsolationProof(), Document: marshalReviewAdapterReport(t, document),
			OccurredAt: mustTime(t, "2026-07-21T11:00:02Z"),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "another-workspace") || !strings.Contains(err.Error(), "example-workspace") {
		t.Fatalf("consumer identity mismatch error = %v", err)
	}
	if after := reviewAdapterJournalHead(t, harness.journal); after != before {
		t.Fatalf("consumer identity rejection changed journal head: before=%s after=%s", before, after)
	}
}

func TestRecordAttemptReviewDocumentFallsBackForLocalAnnotationCategory(t *testing.T) {
	t.Parallel()
	harness, repository, reservation := newReviewAdapterReservation(t)
	materialization := mustReviewAdapterMaterialization(t, harness, repository, reservation)
	var document witnessreview.ReviewReportDocument
	if err := json.Unmarshal(validReviewAdapterReportBytes(t, materialization), &document); err != nil {
		t.Fatal(err)
	}
	document.Findings[0].Annotation.Category = "correctness.logic"
	raw := marshalReviewAdapterReport(t, document)

	recorded, _, err := workspace.RecordAttemptReviewDocument(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.RecordAttemptReviewDocumentRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservation.Digest(),
			RequestDigest: reservation.Request().Digest(), ReviewerInstance: reservation.ReviewerInstance(),
			Isolation: workspace.StrictReviewIsolationProof(), Document: raw,
			OccurredAt: mustTime(t, "2026-07-21T11:00:02Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	findings := recorded.Verified().Submission().Findings()
	if len(findings) != 1 || findings[0].Category().String() != witnessreview.WitnessKindDefect {
		t.Fatalf("fallback local finding = %#v", findings)
	}
	path, err := workspace.ReviewDocumentArtifactPath(harness.workspace, recorded.Artifact())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var retained witnessreview.ReviewReportDocument
	if err := json.Unmarshal(stored, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Findings[0].Annotation == nil || retained.Findings[0].Annotation.Category != "correctness.logic" {
		t.Fatalf("retained annotation = %#v", retained.Findings[0].Annotation)
	}
}

func newReviewAdapterReservation(t *testing.T) (*reviewHarness, *reviewAdapterRepositoryStub, workspace.ReviewInvocationReservation) {
	t.Helper()
	harness := newReviewHarness(t)
	repository := &reviewAdapterRepositoryStub{
		reviewRepositoryStub: *harness.repository,
		reviewInput:          []byte("diff --git a/src/adapter.go b/src/adapter.go\n+adapter contract\n"),
	}
	started, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:00:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reservation := harness.reserve(
		t, started.Request(), workspace.MustID("security-one"), "2026-07-21T11:00:01Z",
	)
	return harness, repository, reservation
}

func mustReviewAdapterMaterialization(
	t *testing.T,
	harness *reviewHarness,
	repository *reviewAdapterRepositoryStub,
	reservation workspace.ReviewInvocationReservation,
) workspace.ReviewAdapterMaterialization {
	t.Helper()
	materialization, err := workspace.BuildReviewAdapterRequest(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.ReviewAdapterBuildRequest{
			AttemptID: harness.attempt.AttemptID(), ReservationDigest: reservation.Digest(),
			RequestDigest: reservation.Request().Digest(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return materialization
}

func validReviewAdapterReportBytes(
	t *testing.T,
	materialization workspace.ReviewAdapterMaterialization,
) []byte {
	t.Helper()
	goals := materialization.FrozenCharter().Charter.Goals
	if len(goals) == 0 {
		t.Fatal("adapter charter has no goals")
	}
	return marshalReviewAdapterReport(t, witnessreview.ReviewReportDocument{
		SchemaVersion:     witnessreview.ReviewReportV1,
		Role:              witnessreview.RoleDefect,
		CharterHash:       materialization.CharterHash().String(),
		ReviewInputDigest: materialization.ReviewInputDigest().String(),
		SourceIdentity:    map[string]any{"kind": "test-reviewer", "id": "adapter-test"},
		ConsumerIdentity: map[string]any{
			"kind": "feature-implement",
			"id":   materialization.Reservation().Request().WorkspaceID().String(),
		},
		Findings: []witnessreview.ReportFinding{{
			ID: "adapter-finding", Title: "Adapter keeps review evidence", ClaimedSeverity: witnessreview.SeverityHigh,
			CharterGoalIDs: []string{goals[0].ID},
			Witness: witnessreview.ReportWitness{
				Kind: witnessreview.WitnessKindDefect, Strength: witnessreview.WitnessStrengthConstructed,
				Content: "The adapter must retain the raw report.",
			},
			Annotation: &witnessreview.FindingAnnotation{Path: "src/adapter.go", Line: 12, Category: "contract"},
		}},
	})
}

func marshalReviewAdapterReport(t *testing.T, document witnessreview.ReviewReportDocument) []byte {
	t.Helper()
	result, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func reviewAdapterJournalHead(t *testing.T, journal *workspace.WorkspaceJournal) workspace.Digest {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Head()
}
