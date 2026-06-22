package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublishContractRecordsArtifactHashAndVerifyOK(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)

	published, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:        fixture.Dir,
		ContractID:          "api-contract",
		Version:             "v1",
		ProducerMergeUnitID: "foundation:story-a",
		ProducerCommit:      "producer-commit-1",
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("PublishContract: %v", err)
	}
	if published.Status != "published" || published.ContractID != "api-contract" || published.Version != "v1" {
		t.Fatalf("publish metadata = %+v", published)
	}
	if published.ProducerMergeUnitID != "foundation:story-a" || published.ProducerCommit != "producer-commit-1" {
		t.Fatalf("producer metadata = %+v", published)
	}
	if published.ArtifactID != "openapi" || published.ArtifactPath != "contracts/openapi.yaml" || published.ArtifactHash == "" {
		t.Fatalf("artifact metadata = %+v", published)
	}
	wantResults := []ContractCommandResult{{Command: "go test ./...", Status: "passed"}}
	if !reflect.DeepEqual(published.CommandResults, wantResults) {
		t.Fatalf("command results = %+v", published.CommandResults)
	}

	events := readTestJournalEvents(t, fixture.Dir)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	if event.Type != EventContractPublished {
		t.Fatalf("event type = %q", event.Type)
	}
	if event.Payload[eventPayloadContractIDKey] != "api-contract" ||
		event.Payload[eventPayloadVersionKey] != "v1" ||
		event.Payload[eventPayloadProducerMergeUnitIDKey] != "foundation:story-a" ||
		event.Payload[eventPayloadProducerCommitKey] != "producer-commit-1" ||
		event.Payload[eventPayloadArtifactPathKey] != "contracts/openapi.yaml" ||
		event.Payload[eventPayloadArtifactHashKey] != published.ArtifactHash {
		t.Fatalf("event payload = %+v", event.Payload)
	}
	if event.ReadSet[ContractResource("api-contract")] != 0 {
		t.Fatalf("event read set = %+v", event.ReadSet)
	}
	if len(event.WriteSet) != 1 || event.WriteSet[0] != ContractResource("api-contract") {
		t.Fatalf("event write set = %+v", event.WriteSet)
	}

	verified, err := VerifyContract(ContractVerifyOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
	})
	if err != nil {
		t.Fatalf("VerifyContract: %v", err)
	}
	if verified.Status != "ok" || !verified.ArtifactExists || !verified.HashMatches {
		t.Fatalf("verify result = %+v", verified)
	}
	if verified.PublishedHash != published.ArtifactHash || verified.CurrentHash != published.ArtifactHash {
		t.Fatalf("verify hashes = %+v", verified)
	}
	if _, err := Status(fixture.Dir); err != nil {
		t.Fatalf("Status should ignore contract events: %v", err)
	}
}

func TestPublishContractUsesCurrentProducerAttempt(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	claim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}

	published, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:   fixture.Dir,
		ContractID:     "api-contract",
		Version:        "v1",
		ProducerCommit: "producer-commit-1",
		AttemptID:      attempt.AttemptID,
		AgentID:        "worker-a",
		LeaseID:        claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("PublishContract: %v", err)
	}
	if published.ProducerMergeUnitID != "foundation:story-a" || published.AttemptID != attempt.AttemptID || published.LeaseID != claim.LeaseID {
		t.Fatalf("publish did not use current attempt metadata: %+v", published)
	}
	event := readTestJournalEvents(t, fixture.Dir)[2]
	if event.ReadSet[LeaseResource("foundation:story-a")] != 1 || event.ReadSet[MergeUnitResource("foundation:story-a")] != 2 {
		t.Fatalf("attempt-backed publish read set = %+v", event.ReadSet)
	}
}

func TestPublishContractRejectsMissingArtifact(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	if err := os.Remove(filepath.Join(fixture.Dir, "contracts", "openapi.yaml")); err != nil {
		t.Fatal(err)
	}

	_, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:        fixture.Dir,
		ContractID:          "api-contract",
		Version:             "v1",
		ProducerMergeUnitID: "foundation:story-a",
		ProducerCommit:      "producer-commit-1",
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
	})

	if err == nil || !strings.Contains(err.Error(), "contract artifact missing") {
		t.Fatalf("PublishContract error = %v", err)
	}
}

func TestVerifyContractReportsHashMismatch(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	published, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:        fixture.Dir,
		ContractID:          "api-contract",
		Version:             "v1",
		ProducerMergeUnitID: "foundation:story-a",
		ProducerCommit:      "producer-commit-1",
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
	})
	if err != nil {
		t.Fatalf("PublishContract: %v", err)
	}
	writeContractArtifact(t, fixture.Dir, "openapi: 3.1.0\ninfo:\n  title: changed\n")

	verified, err := VerifyContract(ContractVerifyOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
	})
	if err != nil {
		t.Fatalf("VerifyContract: %v", err)
	}
	if verified.Status != "mismatch" || !verified.ArtifactExists || verified.HashMatches {
		t.Fatalf("verify result = %+v", verified)
	}
	if verified.PublishedHash != published.ArtifactHash || verified.CurrentHash == "" || verified.CurrentHash == published.ArtifactHash {
		t.Fatalf("verify hashes = %+v, published=%+v", verified, published)
	}
}

func TestPublishContractRejectsInvalidProducer(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)

	_, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:        fixture.Dir,
		ContractID:          "api-contract",
		Version:             "v1",
		ProducerMergeUnitID: "sources:story-b",
		ProducerCommit:      "producer-commit-1",
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
	})

	if err == nil || !strings.Contains(err.Error(), "merge unit sources:story-b is not a producer for contract api-contract") {
		t.Fatalf("PublishContract error = %v", err)
	}
}

func TestPublishContractRequiresProducerMetadata(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)

	_, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:   fixture.Dir,
		ContractID:     "api-contract",
		Version:        "v1",
		ProducerCommit: "producer-commit-1",
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
	})

	if err == nil || !strings.Contains(err.Error(), "requires either --producer-merge-unit or --attempt, --agent, and --lease") {
		t.Fatalf("PublishContract error = %v", err)
	}
}

func newContractWorkspaceFixture(t *testing.T) workspaceFixture {
	t.Helper()
	fixture := newMultiPlanWorkspaceFixture(t)
	fixture.Manifest.ContractGates = []WorkspaceContractGate{validContractGateForFixture()}
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)
	writeContractArtifact(t, fixture.Dir, "openapi: 3.1.0\ninfo:\n  title: fixture\n")
	writeWorkspaceLock(t, fixture.Dir)
	return fixture
}

func writeContractArtifact(t *testing.T, workspaceDir string, content string) {
	t.Helper()
	contractDir := filepath.Join(workspaceDir, "contracts")
	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contractDir, "openapi.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
