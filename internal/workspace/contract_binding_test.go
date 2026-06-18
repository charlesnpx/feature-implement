package workspace

import (
	"strings"
	"testing"
)

func TestBindContractRecordsLatestPublicationForCurrentConsumerAttempt(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	published := publishFixtureContract(t, fixture, "v1", "producer-commit-1", "2026-06-17T10:00:00Z")
	claim, attempt := startFixtureConsumerAttempt(t, fixture, "2026-06-17T10")

	before, err := CheckContracts(ContractCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Now:          fixedJournalTime("2026-06-17T10:08:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckContracts before bind: %v", err)
	}
	if before.Status != contractBindingStatusMissing || len(before.Bindings) != 1 || before.Bindings[0].Status != contractBindingStatusMissing {
		t.Fatalf("before bindings = %+v", before)
	}

	bound, err := BindContract(ContractBindOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:09:00Z"),
	})
	if err != nil {
		t.Fatalf("BindContract: %v", err)
	}
	if bound.Status != "bound" || bound.ContractID != "api-contract" || bound.MergeUnitID != "sources:story-b" || bound.AttemptID != attempt.AttemptID {
		t.Fatalf("bind result = %+v", bound)
	}
	if bound.Version != "v1" || bound.ArtifactHash != published.ArtifactHash || bound.PublicationEventID != published.EventID {
		t.Fatalf("bound publication metadata = %+v, published=%+v", bound, published)
	}

	events := readTestJournalEvents(t, fixture.Dir)
	event := events[len(events)-1]
	if event.Type != EventContractBound {
		t.Fatalf("event type = %q", event.Type)
	}
	if event.Payload[eventPayloadMergeUnitIDKey] != "sources:story-b" ||
		event.Payload[eventPayloadAttemptIDKey] != attempt.AttemptID ||
		event.Payload[eventPayloadContractIDKey] != "api-contract" ||
		event.Payload[eventPayloadArtifactHashKey] != published.ArtifactHash ||
		event.Payload[eventPayloadPublicationEventIDKey] != published.EventID {
		t.Fatalf("event payload = %+v", event.Payload)
	}
	if event.WriteSet[0] != ContractBindingResource("sources:story-b", "api-contract", "openapi") {
		t.Fatalf("event write set = %+v", event.WriteSet)
	}

	after, err := CheckContracts(ContractCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Now:          fixedJournalTime("2026-06-17T10:10:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckContracts after bind: %v", err)
	}
	if after.Status != contractBindingStatusCurrent || len(after.Bindings) != 1 || after.Bindings[0].Status != contractBindingStatusCurrent {
		t.Fatalf("after bindings = %+v", after)
	}
	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, "sources:story-b")
	if len(unit.ContractBindings) != 1 || unit.ContractBindings[0].Status != contractBindingStatusCurrent {
		t.Fatalf("status contract bindings = %+v", unit.ContractBindings)
	}
}

func TestBindContractRejectsUnpublishedContract(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	claim, attempt := startFixtureConsumerAttempt(t, fixture, "2026-06-17T10")

	_, err := BindContract(ContractBindOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:09:00Z"),
	})

	if err == nil || !strings.Contains(err.Error(), "contract api-contract artifact openapi is unpublished") {
		t.Fatalf("BindContract error = %v", err)
	}
}

func TestContractBindingsAreAttemptScoped(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	publishFixtureContract(t, fixture, "v1", "producer-commit-1", "2026-06-17T10:00:00Z")
	claim, first := startFixtureConsumerAttempt(t, fixture, "2026-06-17T10")
	if _, err := BindContract(ContractBindOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    first.AttemptID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:09:00Z"),
	}); err != nil {
		t.Fatalf("BindContract first: %v", err)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    first.AttemptID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		Reason:       "restart after review",
		Now:          fixedJournalTime("2026-06-17T10:10:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	second, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "consumer-base-sha-2",
		Now:          fixedJournalTime("2026-06-17T10:11:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt second: %v", err)
	}

	result, err := CheckContracts(ContractCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    second.AttemptID,
		Now:          fixedJournalTime("2026-06-17T10:12:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckContracts second: %v", err)
	}
	if result.Status != contractBindingStatusMissing || len(result.Bindings) != 1 || result.Bindings[0].Status != contractBindingStatusMissing || result.Bindings[0].BoundArtifactHash != "" {
		t.Fatalf("fresh attempt should not carry binding forward: %+v", result)
	}
}

func TestCheckContractsReportsStaleBinding(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	publishFixtureContract(t, fixture, "v1", "producer-commit-1", "2026-06-17T10:00:00Z")
	claim, attempt := startFixtureConsumerAttempt(t, fixture, "2026-06-17T10")
	bound, err := BindContract(ContractBindOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-consumer",
		LeaseID:      claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:09:00Z"),
	})
	if err != nil {
		t.Fatalf("BindContract: %v", err)
	}
	writeContractArtifact(t, fixture.Dir, "openapi: 3.1.0\ninfo:\n  title: changed\n")
	published := publishFixtureContract(t, fixture, "v2", "producer-commit-2", "2026-06-17T10:10:00Z")

	result, err := CheckContracts(ContractCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Now:          fixedJournalTime("2026-06-17T10:11:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckContracts: %v", err)
	}
	if result.Status != contractBindingStatusStale || len(result.Bindings) != 1 {
		t.Fatalf("stale result = %+v", result)
	}
	binding := result.Bindings[0]
	if binding.Status != contractBindingStatusStale || binding.BoundVersion != "v1" || binding.Version != "v2" {
		t.Fatalf("stale binding = %+v", binding)
	}
	if binding.BoundArtifactHash != bound.ArtifactHash || binding.ArtifactHash != published.ArtifactHash {
		t.Fatalf("stale hashes = %+v, bound=%+v, published=%+v", binding, bound, published)
	}
}

func publishFixtureContract(t *testing.T, fixture workspaceFixture, version string, commit string, at string) ContractPublishResult {
	t.Helper()
	published, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:        fixture.Dir,
		ContractID:          "api-contract",
		Version:             version,
		ProducerMergeUnitID: "foundation:story-a",
		ProducerCommit:      commit,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime(at),
	})
	if err != nil {
		t.Fatalf("PublishContract %s: %v", version, err)
	}
	return published
}

func startFixtureConsumerAttempt(t *testing.T, fixture workspaceFixture, hourPrefix string) (NextResult, AttemptResult) {
	t.Helper()
	producer, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-producer",
		Claim:        true,
		Now:          fixedJournalTime(hourPrefix + ":01:00Z"),
	})
	if err != nil {
		t.Fatalf("Next producer: %v", err)
	}
	producerAttempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  producer.MergeUnitID,
		AgentID:      "worker-producer",
		LeaseID:      producer.LeaseID,
		BaseSHA:      "producer-base-sha",
		Now:          fixedJournalTime(hourPrefix + ":02:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt producer: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  producer.MergeUnitID,
		AttemptID:    producerAttempt.AttemptID,
		AgentID:      "worker-producer",
		LeaseID:      producer.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: producerAttempt.Worktree},
		Now:          fixedJournalTime(hourPrefix + ":03:00Z"),
	}); err != nil {
		t.Fatalf("Transition producer start: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  producer.MergeUnitID,
		AttemptID:    producerAttempt.AttemptID,
		AgentID:      "worker-producer",
		LeaseID:      producer.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "producer-commit-sha"},
		Now:          fixedJournalTime(hourPrefix + ":04:00Z"),
	}); err != nil {
		t.Fatalf("Transition producer complete: %v", err)
	}
	consumer, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-consumer",
		Claim:        true,
		Now:          fixedJournalTime(hourPrefix + ":05:00Z"),
	})
	if err != nil {
		t.Fatalf("Next consumer: %v", err)
	}
	if consumer.MergeUnitID != "sources:story-b" {
		t.Fatalf("consumer claim = %+v", consumer)
	}
	consumerAttempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  consumer.MergeUnitID,
		AgentID:      "worker-consumer",
		LeaseID:      consumer.LeaseID,
		BaseSHA:      "consumer-base-sha",
		Now:          fixedJournalTime(hourPrefix + ":06:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt consumer: %v", err)
	}
	return consumer, consumerAttempt
}
