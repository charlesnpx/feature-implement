package workspace_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

func TestReviewCompletionUsesContractIdentityInsteadOfAdapterName(t *testing.T) {
	t.Parallel()

	for _, adapter := range []string{"adapter-alpha", "unfamiliar-adapter"} {
		adapter := adapter
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			harness := newReviewGateHarness(t, adapter)
			dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
			request := validReviewRequest(t, harness, dispatched.Dispatch())
			completion := validReviewCompletion(t, request, witnessreview.CompletionVerdictSatisfied, validObservedExecution())

			recorded, journalRecord, err := workspace.RecordAttemptReviewCompletion(
				harness.journal, harness.definition, workspace.RecordAttemptReviewCompletionRequest{
					AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest(),
					Request: request, Completion: completion,
					OccurredAt: mustTime(t, "2026-09-03T12:00:02Z"),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if journalRecord.Event() == nil || recorded.GateRecord().Verdict() != workspace.ReviewGateSatisfied {
				t.Fatalf("completion record = %#v journal=%#v", recorded.GateRecord(), journalRecord.Event())
			}
			artifactPath, err := workspace.ReviewDocumentArtifactPath(harness.workspace, recorded.Artifact())
			if err != nil {
				t.Fatal(err)
			}
			stored, err := os.ReadFile(artifactPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) == 0 {
				t.Fatalf("retained completion evidence is empty")
			}
		})
	}
}

func TestReviewCompletionRecordsEachVerdictAndRejectsDecodedEvidence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		verdict  string
		observed witnessreview.ObservedReviewExecution
	}{
		{
			name: "satisfied", verdict: witnessreview.CompletionVerdictSatisfied,
			observed: validObservedExecution(),
		},
		{
			name: "not satisfied", verdict: witnessreview.CompletionVerdictNotSatisfied,
			observed: validObservedExecution(),
		},
		{
			name: "failed to run", verdict: witnessreview.CompletionVerdictFailedToRun,
			observed: witnessreview.ObservedReviewExecution{
				Complete: false, ResultArtifactAvailable: false,
				ReportOutcomes: map[string]witnessreview.ObservedReportOutcome{
					"reviewer-a": {Status: witnessreview.ExecutionReportMissing},
				},
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			harness := newReviewGateHarness(t, "adapter-"+strings.ReplaceAll(test.name, " ", "-"))
			dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
			request := validReviewRequest(t, harness, dispatched.Dispatch())
			completion := validReviewCompletion(t, request, test.verdict, test.observed)
			if _, _, err := workspace.RecordAttemptReviewCompletion(
				harness.journal, harness.definition, workspace.RecordAttemptReviewCompletionRequest{
					AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest(),
					Request: request, Completion: completion,
					OccurredAt: mustTime(t, "2026-09-03T12:00:02Z"),
				},
			); err != nil {
				t.Fatalf("record %s completion: %v", test.verdict, err)
			}

			raw, err := json.Marshal(completion)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := witnessreview.DecodeReviewCompletion(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := workspace.RecordAttemptReviewCompletion(
				harness.journal, harness.definition, workspace.RecordAttemptReviewCompletionRequest{
					AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest(),
					Request: request, Completion: decoded,
					OccurredAt: mustTime(t, "2026-09-03T12:00:02Z"),
				},
			); err == nil || !strings.Contains(err.Error(), "execution_evidence") {
				t.Fatalf("decoded completion error = %v", err)
			}
		})
	}
}

func TestReviewCompletionRejectsDifferentSourceRevision(t *testing.T) {
	t.Parallel()

	harness := newReviewGateHarness(t, "custom-reviewer")
	dispatched := harness.dispatch(t, "2026-09-03T12:00:01Z")
	request := validReviewRequest(t, harness, dispatched.Dispatch())
	request.Subject.Head = workspace.DigestBytes([]byte("different-head")).String()
	completion := validReviewCompletion(t, request, witnessreview.CompletionVerdictSatisfied, validObservedExecution())
	before := reviewAdapterJournalHead(t, harness.journal)
	if _, _, err := workspace.RecordAttemptReviewCompletion(
		harness.journal, harness.definition, workspace.RecordAttemptReviewCompletionRequest{
			AttemptID: harness.attempt.AttemptID(), DispatchDigest: dispatched.Dispatch().Digest(),
			Request: request, Completion: completion,
			OccurredAt: mustTime(t, "2026-09-03T12:00:02Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "subject head") {
		t.Fatalf("different source completion error = %v", err)
	}
	if after := reviewAdapterJournalHead(t, harness.journal); after != before {
		t.Fatalf("rejected completion changed journal head: before=%s after=%s", before, after)
	}
}

func validReviewRequest(t *testing.T, harness *gatedReviewHarness, dispatch workspace.ReviewGateDispatch) witnessreview.ReviewRequestV2Document {
	t.Helper()
	recipe := witnessreview.ReviewRecipe{
		RecipeID: dispatch.Recipe().String(), Instructions: "opaque review instructions",
		RequiredOutputs: []string{"reviewer-a"}, Policy: map[string]any{"opaque": true},
	}
	recipeBytes, err := witnessreview.ReviewRecipeCanonicalBytes(recipe)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := witnessreview.ReviewRecipeDigest(recipeBytes)
	if err != nil {
		t.Fatal(err)
	}
	request := witnessreview.ReviewRequestV2Document{
		SchemaVersion:     witnessreview.ReviewRequestV2,
		ConsumerIdentity:  witnessreview.Identity{Kind: "feature-implement", ID: harness.definition.Workspace().ID().String()},
		Subject:           witnessreview.RequestSubject{Head: dispatch.Head().String(), Tree: dispatch.Tree().String()},
		CharterHash:       workspace.DigestBytes([]byte("opaque-charter")).String(),
		ReviewInputDigest: workspace.DigestBytes([]byte("opaque-input")).String(),
		FrozenRecipe:      recipeBytes, RecipeDigest: recipeDigest,
		Adapter: dispatch.Adapter().String(), RequiredOutputs: []string{"reviewer-a"},
	}
	if err := witnessreview.RequireValidReviewRequestV2(request); err != nil {
		t.Fatal(err)
	}
	return request
}

func validObservedExecution() witnessreview.ObservedReviewExecution {
	return witnessreview.ObservedReviewExecution{
		Complete: true, ResultArtifactAvailable: true,
		ReportOutcomes: map[string]witnessreview.ObservedReportOutcome{
			"reviewer-a": {Status: witnessreview.ExecutionReportValid},
		},
	}
}

func validReviewCompletion(
	t *testing.T,
	request witnessreview.ReviewRequestV2Document,
	verdict string,
	observed witnessreview.ObservedReviewExecution,
) witnessreview.ReviewCompletionDocument {
	t.Helper()
	evidence, err := witnessreview.NewHostExecutionEvidence(observed)
	if err != nil {
		t.Fatal(err)
	}
	reportDigests := map[string]string{}
	if verdict != witnessreview.CompletionVerdictFailedToRun {
		reportDigests["reviewer-a"] = workspace.DigestBytes([]byte("reviewer-a-report")).String()
	}
	completion, err := witnessreview.NewReviewCompletionDocument(request, evidence, reportDigests, verdict)
	if err != nil {
		t.Fatal(err)
	}
	return completion
}

func reviewAdapterJournalHead(t *testing.T, journal *workspace.WorkspaceJournal) workspace.Digest {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Head()
}
