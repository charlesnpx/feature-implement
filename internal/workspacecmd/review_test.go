package workspacecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	witnesscharter "github.com/charlesnpx/witness/contract/charter"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

func TestLocalReviewRepositoryAdoptsActualCleanDescendantHead(t *testing.T) {
	repository := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.name", "Feature Test")
	runGitTest(t, repository, "config", "user.email", "feature@example.test")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Base")
	base := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))

	if err := os.WriteFile(tracked, []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Implementation")
	head := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))
	tree := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD^{tree}")))
	request, err := workspace.NewReviewRepositoryRequest(repository, base)
	if err != nil {
		t.Fatal(err)
	}
	adapter := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
	if _, err := adapter.InspectReviewSnapshot(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "attempt worktree must keep HEAD detached") {
		t.Fatalf("branch-attached attempt inspection error = %v", err)
	}
	runGitTest(t, repository, "switch", "--detach", gitObjectHex(head))
	snapshot, err := adapter.InspectReviewSnapshot(context.Background(), request)
	if err != nil || !snapshot.Clean() || snapshot.Head() != head || snapshot.Tree() != tree {
		t.Fatalf("actual review snapshot = %#v error=%v", snapshot, err)
	}

	runGitTest(t, repository, "reset", "--hard", gitObjectHex(base))
	staleRequest, err := workspace.NewReviewRepositoryRequest(repository, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.InspectReviewSnapshot(context.Background(), staleRequest); err == nil ||
		!strings.Contains(err.Error(), "descend from durable head") {
		t.Fatalf("rewound ordinary head error = %v", err)
	}
}

func TestWitnessReviewDispatchPacketRoundTripsThroughRecordDocument(t *testing.T) {
	fixture := newAttemptBoundaryCommandFixture(t, true)
	dispatchResult, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "dispatch",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-09-03T12:00:01Z",
  "attempt_id": "` + fixture.attemptID.String() + `"
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatched, ok := dispatchResult.(ReviewCommandResult)
	if !ok || dispatched.Action != "review.dispatch" {
		t.Fatalf("dispatch command result = %#v", dispatchResult)
	}
	dispatch, ok := dispatched.Detail.(ReviewGateDispatchView)
	if !ok || dispatch.WitnessPacket == nil {
		t.Fatalf("witness dispatch detail = %#v", dispatched.Detail)
	}
	packet := dispatch.WitnessPacket
	packetJSON, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	var packetFields map[string]json.RawMessage
	if err := json.Unmarshal(packetJSON, &packetFields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"charter_document", "request_document", "review_input"} {
		if _, exists := packetFields[field]; !exists {
			t.Fatalf("witness packet omits %q: %s", field, packetJSON)
		}
	}
	if len(packetFields) != 3 {
		t.Fatalf("witness packet duplicates request bindings: %s", packetJSON)
	}
	var charter witnesscharter.Charter
	if err := json.Unmarshal(packet.CharterDocument, &charter); err != nil || len(charter.Goals) == 0 {
		t.Fatalf("dispatch charter = %#v error=%v", charter, err)
	}
	var request witnessreview.ReviewRequestDocument
	if err := json.Unmarshal(packet.RequestDocument, &request); err != nil {
		t.Fatal(err)
	}
	if request.ReviewInputDigest != workspace.DigestBytes([]byte(packet.ReviewInput)).String() {
		t.Fatalf("dispatch request input binding = %#v", request)
	}

	report := witnessreview.ReviewReportDocument{
		SchemaVersion:     witnessreview.ReviewReportV1,
		Role:              witnessreview.RoleDefect,
		CharterHash:       request.CharterHash,
		ReviewInputDigest: request.ReviewInputDigest,
		SourceIdentity:    witnessreview.Identity{Kind: "test-reviewer", ID: "cli-round-trip"},
		ConsumerIdentity:  request.ConsumerIdentity,
		Findings:          []witnessreview.ReportFinding{},
		Evaluation: &witnessreview.ReportEvaluation{
			EvaluatedPaths: []string{"src/review.go"}, EvaluatedGoalIDs: []string{charter.Goals[0].ID},
		},
	}
	rawReport, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	recordInput, err := json.Marshal(struct {
		SchemaVersion  int             `json:"schema_version"`
		OccurredAt     string          `json:"occurred_at"`
		AttemptID      string          `json:"attempt_id"`
		DispatchDigest string          `json:"dispatch_digest"`
		Verdict        string          `json:"verdict"`
		Document       json.RawMessage `json:"document"`
	}{
		SchemaVersion: 2, OccurredAt: "2026-09-03T12:00:02Z", AttemptID: fixture.attemptID.String(),
		DispatchDigest: dispatch.DispatchDigest, Verdict: "satisfied", Document: rawReport,
	})
	if err != nil {
		t.Fatal(err)
	}
	recordedResult, err := Execute(context.Background(), Options{
		Action: "review", Subaction: "record-document", BundleDir: fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir, Input: recordInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorded, ok := recordedResult.(ReviewCommandResult)
	if !ok || recorded.Action != "review.record-document" {
		t.Fatalf("record-document command result = %#v", recordedResult)
	}
	detail, ok := recorded.Detail.(ReviewDocumentRecordDetail)
	if !ok || detail.GateRecord.EvidenceDigest != workspace.DigestBytes(rawReport).String() || detail.RawDocumentPath == "" {
		t.Fatalf("record-document detail = %#v", recorded.Detail)
	}
	stored, err := os.ReadFile(detail.RawDocumentPath)
	if err != nil || !bytes.Equal(stored, rawReport) {
		t.Fatalf("retained raw report = %q error=%v", stored, err)
	}
}
