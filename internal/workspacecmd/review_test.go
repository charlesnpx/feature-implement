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

func TestReviewGateDispatchViewExposesOnlyAdapterContract(t *testing.T) {
	view := ReviewGateDispatchView{
		DispatchDigest: "sha256:dispatch", Adapter: "natural-language", Recipe: "default",
		PolicyDigest: "sha256:policy", Policy: "Review the exact artifact.",
		Head: "sha1:head", Tree: "sha1:tree", FrozenCopy: "/tmp/frozen-copy",
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"dispatch_digest": true, "adapter": true, "recipe": true, "policy_digest": true,
		"policy": true, "head": true, "tree": true, "frozen_copy": true,
	}
	if len(fields) != len(want) {
		t.Fatalf("dispatch view fields = %#v", fields)
	}
	for field := range want {
		if _, exists := fields[field]; !exists {
			t.Fatalf("dispatch view omits %q: %#v", field, fields)
		}
	}
}

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
	if packet.ReviewInputLocation != dispatch.FrozenCopy || !filepath.IsAbs(packet.ReviewInputLocation) ||
		packet.ReviewInputDigest != workspace.DigestBytes([]byte(packet.ReviewInput)).String() {
		t.Fatalf("witness packet input binding = %#v", packet)
	}
	var charter witnesscharter.Charter
	if err := json.Unmarshal(packet.CharterDocument, &charter); err != nil || len(charter.Goals) == 0 {
		t.Fatalf("dispatch charter = %#v error=%v", charter, err)
	}
	var request witnessreview.ReviewRequestDocument
	if err := json.Unmarshal(packet.RequestDocument, &request); err != nil {
		t.Fatal(err)
	}
	requestDigest, err := witnessreview.ReviewRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	if request.CharterHash != packet.CharterHash || request.ReviewInputDigest != packet.ReviewInputDigest ||
		requestDigest != packet.RequestDigest {
		t.Fatalf("dispatch request packet = %#v request_digest=%q", packet, requestDigest)
	}

	report := witnessreview.ReviewReportDocument{
		SchemaVersion:     witnessreview.ReviewReportV1,
		Role:              witnessreview.RoleDefect,
		CharterHash:       packet.CharterHash,
		ReviewInputDigest: packet.ReviewInputDigest,
		SourceIdentity:    witnessreview.Identity{Kind: "test-reviewer", ID: "cli-round-trip"},
		ConsumerIdentity:  request.ConsumerIdentity,
		Findings: []witnessreview.ReportFinding{{
			ID: "cli-finding", Title: "Dispatch packet supports a conforming report", ClaimedSeverity: witnessreview.SeverityHigh,
			CharterGoalIDs: []string{charter.Goals[0].ID},
			Witness: witnessreview.ReportWitness{
				Kind: witnessreview.WitnessKindDefect, Strength: witnessreview.WitnessStrengthConstructed,
				Content: "The command exposes all document bindings needed by the Witness report.",
			},
			Annotation: &witnessreview.FindingAnnotation{Path: "src/review.go", Line: 12, Category: "contract"},
		}},
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
