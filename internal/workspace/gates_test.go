package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvaluateGatesRecordsDefaultDeterministicEvaluation(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)

	first, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates first: %v", err)
	}
	second, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates second: %v", err)
	}
	if first.EvaluatorVersion != GateEvaluatorVersion || first.InputHash == "" || first.OutputHash == "" {
		t.Fatalf("evaluation metadata = %+v", first)
	}
	if first.InputHash != second.InputHash || first.OutputHash != second.OutputHash {
		t.Fatalf("evaluation hashes changed across identical inputs: first=%+v second=%+v", first, second)
	}
	statuses := gateStatusesByName(first.Gates)
	if statuses["contract"] != GateStatusPassed {
		t.Fatalf("contract gate = %q, gates=%+v", statuses["contract"], first.Gates)
	}
	if statuses["test"] != GateStatusPending {
		t.Fatalf("test gate = %q, gates=%+v", statuses["test"], first.Gates)
	}
	events, err := readJournalEvents(EventsPath(fixture.Dir))
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != EventGateEvaluationRecorded {
		t.Fatalf("last event type = %q", last.Type)
	}
	if got, _ := last.Payload[eventPayloadInputHashKey].(string); got != second.InputHash {
		t.Fatalf("event input hash = %q, want %q", got, second.InputHash)
	}
	if got, _ := last.Payload[eventPayloadOutputHashKey].(string); got != second.OutputHash {
		t.Fatalf("event output hash = %q, want %q", got, second.OutputHash)
	}
	if got := last.ReadSet[RefreshResource(claim.MergeUnitID+":"+attempt.AttemptID)]; got != 0 {
		t.Fatalf("refresh read-set revision = %d", got)
	}
	if got := last.ReadSet[ApprovalAttemptResource(claim.MergeUnitID, attempt.AttemptID)]; got != 0 {
		t.Fatalf("approval attempt read-set revision = %d", got)
	}
}

func TestEvaluateGatesMarksTestRerunRequiredAfterRefresh(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	appendRefreshInputChangeEvent(t, fixture.Dir, claim.MergeUnitID, attempt.AttemptID)

	result, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}
	statuses := gateStatusesByName(result.Gates)
	if statuses["test"] != GateStatusRerunRequired {
		t.Fatalf("test gate = %q, gates=%+v", statuses["test"], result.Gates)
	}
}

func TestEvaluateGatesDoesNotCarryRefreshOrResultsToFreshAttempt(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, firstAttempt := startGateEvaluationAttempt(t, fixture.Dir)
	appendRefreshInputChangeEvent(t, fixture.Dir, claim.MergeUnitID, firstAttempt.AttemptID)
	first, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    firstAttempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates first: %v", err)
	}
	if statuses := gateStatusesByName(first.Gates); statuses["test"] != GateStatusRerunRequired {
		t.Fatalf("first attempt test gate = %+v", first.Gates)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    firstAttempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Reason:       "fresh attempt",
		Now:          fixedWorkspaceTime("2026-01-02T15:06:05Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	secondAttempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-second",
		Now:          fixedWorkspaceTime("2026-01-02T15:07:05Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt second: %v", err)
	}

	second, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    secondAttempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:08:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates second: %v", err)
	}
	statuses := gateStatusesByName(second.Gates)
	if statuses["test"] != GateStatusPending {
		t.Fatalf("fresh attempt test gate carried prior refresh/result: %+v", second.Gates)
	}
}

func TestEvaluateGatesIgnoresPriorAttemptContractCheckResults(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	fixture.Manifest.ContractGates = []WorkspaceContractGate{{
		ID:        "api-contract",
		Producers: []string{"foundation:story-a"},
		Consumers: []string{"foundation:story-a"},
		Artifacts: []WorkspaceArtifactSpec{{
			ID:   "openapi",
			Path: "contracts/openapi.yaml",
		}},
		Validation: WorkspaceGateValidation{
			Commands: []string{"go test ./..."},
		},
	}}
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)
	contractPath := filepath.Join(fixture.Dir, "contracts", "openapi.yaml")
	if err := os.MkdirAll(filepath.Dir(contractPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, []byte("openapi: 3.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, firstAttempt := startGateEvaluationAttempt(t, fixture.Dir)
	if _, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:        fixture.Dir,
		ContractID:          "api-contract",
		Version:             "v1",
		ProducerMergeUnitID: claim.MergeUnitID,
		ProducerCommit:      "producer-sha",
		CommandResults:      []ContractCommandResult{{Command: "go test ./...", Status: "passed"}},
		Now:                 fixedWorkspaceTime("2026-01-02T15:03:05Z"),
	}); err != nil {
		t.Fatalf("PublishContract: %v", err)
	}
	if _, err := BindContract(ContractBindOptions{
		WorkspaceDir:   fixture.Dir,
		ContractID:     "api-contract",
		MergeUnitID:    claim.MergeUnitID,
		AttemptID:      firstAttempt.AttemptID,
		AgentID:        claim.AgentID,
		LeaseID:        claim.LeaseID,
		CommandResults: []ContractCommandResult{{Command: "go test ./...", Status: "passed"}},
		Now:            fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	}); err != nil {
		t.Fatalf("BindContract: %v", err)
	}
	first, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    firstAttempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates first: %v", err)
	}
	if statuses := gateStatusesByName(first.Gates); statuses["contract"] != GateStatusPassed {
		t.Fatalf("first contract gate = %+v", first.Gates)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    firstAttempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Reason:       "fresh attempt",
		Now:          fixedWorkspaceTime("2026-01-02T15:06:05Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	secondAttempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-second",
		Now:          fixedWorkspaceTime("2026-01-02T15:07:05Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt second: %v", err)
	}
	second, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    secondAttempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:08:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates second: %v", err)
	}
	if statuses := gateStatusesByName(second.Gates); statuses["contract"] != GateStatusBlocked {
		t.Fatalf("fresh attempt carried contract check result: %+v", second.Gates)
	}
}

func TestEvaluateGatesBlocksFailedContractCommandResult(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	fixture.Manifest.ContractGates = []WorkspaceContractGate{{
		ID:        "api-contract",
		Producers: []string{"foundation:story-a"},
		Consumers: []string{"foundation:story-a"},
		Artifacts: []WorkspaceArtifactSpec{{
			ID:   "openapi",
			Path: "contracts/openapi.yaml",
		}},
		Validation: WorkspaceGateValidation{
			Commands: []string{"go test ./..."},
		},
	}}
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)
	contractPath := filepath.Join(fixture.Dir, "contracts", "openapi.yaml")
	if err := os.MkdirAll(filepath.Dir(contractPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, []byte("openapi: 3.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	if _, err := PublishContract(ContractPublishOptions{
		WorkspaceDir:        fixture.Dir,
		ContractID:          "api-contract",
		Version:             "v1",
		ProducerMergeUnitID: claim.MergeUnitID,
		ProducerCommit:      "producer-sha",
		CommandResults:      []ContractCommandResult{{Command: "go test ./...", Status: "passed"}},
		Now:                 fixedWorkspaceTime("2026-01-02T15:03:05Z"),
	}); err != nil {
		t.Fatalf("PublishContract: %v", err)
	}
	if _, err := BindContract(ContractBindOptions{
		WorkspaceDir:   fixture.Dir,
		ContractID:     "api-contract",
		MergeUnitID:    claim.MergeUnitID,
		AttemptID:      attempt.AttemptID,
		AgentID:        claim.AgentID,
		LeaseID:        claim.LeaseID,
		CommandResults: []ContractCommandResult{{Command: "go test ./...", Status: "failed"}},
		Now:            fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	}); err != nil {
		t.Fatalf("BindContract: %v", err)
	}

	result, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}
	found := false
	for _, gate := range result.Gates {
		if gate.Gate != "contract" {
			continue
		}
		found = true
		if gate.Status != GateStatusBlocked || gate.Reason != "contract_command_failed:go test ./..." {
			t.Fatalf("contract gate = %+v", gate)
		}
	}
	if !found {
		t.Fatalf("contract gate missing: %+v", result.Gates)
	}
}

func TestOverrideGateAppliesRetainedByOperator(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	appendGateRefreshEvent(t, fixture.Dir, claim, attempt, attempt.BaseSHA, attempt.BaseSHA, "head-sha-first", "head-sha-first", "2026-01-02T15:03:00Z")
	evaluation, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}
	override, err := OverrideGate(GateOverrideOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Gate:         "security",
		Status:       GateStatusRetainedByOperator,
		Reason:       "operator accepted base-only rebase",
		InputHash:    evaluation.InputHash,
		HeadSHA:      "head-sha-first",
		BaseSHA:      attempt.BaseSHA,
		Operator:     "operator-a",
		ExpiresIn:    time.Hour,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	})
	if err != nil {
		t.Fatalf("OverrideGate: %v", err)
	}
	if override.Override.Status != GateStatusRetainedByOperator || override.Override.InputHash != evaluation.InputHash {
		t.Fatalf("override = %+v", override.Override)
	}

	after, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:06:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates after override: %v", err)
	}
	found := false
	for _, gate := range after.Gates {
		if gate.Gate != "security" {
			continue
		}
		found = true
		if gate.Status != GateStatusRetainedByOperator ||
			gate.ComputedStatus != GateStatusPending ||
			gate.OverrideID != override.Override.OverrideID ||
			gate.Operator != "operator-a" {
			t.Fatalf("security gate = %+v", gate)
		}
	}
	if !found {
		t.Fatalf("security gate missing: %+v", after.Gates)
	}
	events, err := readJournalEvents(EventsPath(fixture.Dir))
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	gatesPayload, ok := last.Payload[eventPayloadGatesKey].([]any)
	if !ok {
		t.Fatalf("gate payload missing from event: %+v", last.Payload)
	}
	foundPayload := false
	for _, raw := range gatesPayload {
		item, ok := raw.(map[string]any)
		if !ok || item[eventPayloadGateKey] != "security" {
			continue
		}
		foundPayload = true
		if item[eventPayloadComputedStatusKey] != GateStatusPending ||
			item[eventPayloadOverrideIDKey] != override.Override.OverrideID ||
			item[eventPayloadOperatorKey] != "operator-a" ||
			item[eventPayloadExpiresAtKey] != override.Override.ExpiresAt {
			t.Fatalf("security gate event payload = %+v", item)
		}
	}
	if !foundPayload {
		t.Fatalf("security gate missing from event payload: %+v", gatesPayload)
	}
}

func TestRecordGateEvidenceAppliesToolEvidence(t *testing.T) {
	cases := []struct {
		name     string
		gate     string
		command  string
		reviewer string
	}{
		{name: "review", gate: "review", reviewer: "reviewer-a"},
		{name: "security", gate: "security", command: "gosec ./..."},
		{name: "test", gate: "test", command: "go test ./..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newOnePlanWorkspaceFixture(t)
			if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
			appendGateRefreshEvent(t, fixture.Dir, claim, attempt, attempt.BaseSHA, attempt.BaseSHA, "head-sha-first", "head-sha-first", "2026-01-02T15:03:00Z")
			before, err := EvaluateGates(GateEvaluateOptions{
				WorkspaceDir: fixture.Dir,
				MergeUnitID:  claim.MergeUnitID,
				AttemptID:    attempt.AttemptID,
				AgentID:      claim.AgentID,
				LeaseID:      claim.LeaseID,
				Now:          fixedWorkspaceTime("2026-01-02T15:04:00Z"),
			})
			if err != nil {
				t.Fatalf("EvaluateGates before: %v", err)
			}

			evidence, err := RecordGateEvidence(GateEvidenceOptions{
				WorkspaceDir: fixture.Dir,
				MergeUnitID:  claim.MergeUnitID,
				AttemptID:    attempt.AttemptID,
				AgentID:      claim.AgentID,
				LeaseID:      claim.LeaseID,
				Gate:         tc.gate,
				Status:       GateStatusPassed,
				InputHash:    before.InputHash,
				HeadSHA:      "head-sha-first",
				BaseSHA:      attempt.BaseSHA,
				Command:      tc.command,
				Reviewer:     tc.reviewer,
				Summary:      tc.gate + " evidence passed",
				Now:          fixedWorkspaceTime("2026-01-02T15:05:00Z"),
			})
			if err != nil {
				t.Fatalf("RecordGateEvidence: %v", err)
			}
			if evidence.Evidence.Gate != tc.gate || evidence.Evidence.Status != GateStatusPassed || evidence.Evidence.InputHash != before.InputHash {
				t.Fatalf("evidence = %+v", evidence.Evidence)
			}
			after, err := EvaluateGates(GateEvaluateOptions{
				WorkspaceDir: fixture.Dir,
				MergeUnitID:  claim.MergeUnitID,
				AttemptID:    attempt.AttemptID,
				AgentID:      claim.AgentID,
				LeaseID:      claim.LeaseID,
				Now:          fixedWorkspaceTime("2026-01-02T15:06:00Z"),
			})
			if err != nil {
				t.Fatalf("EvaluateGates after: %v", err)
			}
			gate := gateStatusByName(after.Gates)[tc.gate]
			if gate.Status != GateStatusPassed ||
				gate.Reason != "tool_evidence_recorded" ||
				gate.EvidenceID != evidence.Evidence.EvidenceID ||
				gate.Command != tc.command ||
				gate.Reviewer != tc.reviewer ||
				gate.Summary != tc.gate+" evidence passed" {
				t.Fatalf("tool gate status = %+v", gate)
			}
			if after.OutputHash == before.OutputHash {
				t.Fatalf("tool evidence should change gate output hash")
			}
			events := readTestJournalEvents(t, fixture.Dir)
			last := events[len(events)-2]
			if last.Type != EventGateEvidenceRecorded {
				t.Fatalf("evidence event type = %s", last.Type)
			}
			resource := GateEvidenceResource(claim.MergeUnitID, attempt.AttemptID, tc.gate)
			if !containsString(last.WriteSet, resource) || last.ReadSet[resource] != 0 {
				t.Fatalf("evidence event resources read=%+v write=%+v", last.ReadSet, last.WriteSet)
			}
		})
	}
}

func TestRecordGateEvidenceRejectsStaleInputs(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	appendGateRefreshEvent(t, fixture.Dir, claim, attempt, attempt.BaseSHA, attempt.BaseSHA, "head-sha-first", "head-sha-first", "2026-01-02T15:03:00Z")
	evaluation, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:00Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}
	base := GateEvidenceOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Gate:         "test",
		Status:       GateStatusPassed,
		InputHash:    evaluation.InputHash,
		HeadSHA:      "head-sha-first",
		BaseSHA:      attempt.BaseSHA,
		Command:      "go test ./...",
		Summary:      "tests passed",
		Now:          fixedWorkspaceTime("2026-01-02T15:05:00Z"),
	}
	staleInput := base
	staleInput.InputHash = "wrong-input-hash"
	if _, err := RecordGateEvidence(staleInput); err == nil || !strings.Contains(err.Error(), "does not match current evaluator input") {
		t.Fatalf("stale input error = %v", err)
	}
	staleHead := base
	staleHead.HeadSHA = "wrong-head"
	if _, err := RecordGateEvidence(staleHead); err == nil || !strings.Contains(err.Error(), "does not match current head") {
		t.Fatalf("stale head error = %v", err)
	}
	staleBase := base
	staleBase.BaseSHA = "wrong-base"
	if _, err := RecordGateEvidence(staleBase); err == nil || !strings.Contains(err.Error(), "does not match current base") {
		t.Fatalf("stale base error = %v", err)
	}
}

func TestRecordGateEvidenceRejectsMissingFields(t *testing.T) {
	_, err := RecordGateEvidence(GateEvidenceOptions{
		WorkspaceDir: "workspace",
		MergeUnitID:  "foundation:story-a",
		AttemptID:    "foundation:story-a:attempt-1",
		AgentID:      "worker-a",
		LeaseID:      "lease-a",
		Gate:         "review",
		Status:       GateStatusPassed,
		InputHash:    "input-hash",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Summary:      "missing command or reviewer",
	})
	if err == nil || !strings.Contains(err.Error(), "requires --command or --reviewer") {
		t.Fatalf("missing source error = %v", err)
	}
	_, err = RecordGateEvidence(GateEvidenceOptions{
		WorkspaceDir: "workspace",
		MergeUnitID:  "foundation:story-a",
		AttemptID:    "foundation:story-a:attempt-1",
		AgentID:      "worker-a",
		LeaseID:      "lease-a",
		Gate:         "merge_approval",
		Status:       GateStatusPassed,
		InputHash:    "input-hash",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Reviewer:     "reviewer-a",
		Summary:      "unsupported gate",
	})
	if err == nil || !strings.Contains(err.Error(), "does not accept tool evidence") {
		t.Fatalf("unsupported gate error = %v", err)
	}
	_, err = RecordGateEvidence(GateEvidenceOptions{
		WorkspaceDir: "workspace",
		MergeUnitID:  "foundation:story-a",
		AttemptID:    "foundation:story-a:attempt-1",
		AgentID:      "worker-a",
		LeaseID:      "lease-a",
		Gate:         "review",
		Status:       GateStatusPending,
		InputHash:    "input-hash",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Reviewer:     "reviewer-a",
		Summary:      "unsupported status",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported gate evidence status") {
		t.Fatalf("unsupported status error = %v", err)
	}
}

func TestOverrideGateBecomesStaleWhenInputsChange(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	appendGateRefreshEvent(t, fixture.Dir, claim, attempt, attempt.BaseSHA, attempt.BaseSHA, "head-sha-first", "head-sha-first", "2026-01-02T15:03:00Z")
	evaluation, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}
	if _, err := OverrideGate(GateOverrideOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Gate:         "security",
		Status:       GateStatusRetainedByOperator,
		Reason:       "operator accepted base-only rebase",
		InputHash:    evaluation.InputHash,
		HeadSHA:      "head-sha-first",
		BaseSHA:      attempt.BaseSHA,
		Operator:     "operator-a",
		ExpiresIn:    time.Hour,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	}); err != nil {
		t.Fatalf("OverrideGate: %v", err)
	}
	appendGateRefreshEvent(t, fixture.Dir, claim, attempt, attempt.BaseSHA, "base-sha-second", "head-sha-first", "head-sha-second", "2026-01-02T15:06:00Z")

	after, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:06:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates after refresh: %v", err)
	}
	for _, gate := range after.Gates {
		if gate.Gate == "security" && gate.Status == GateStatusRetainedByOperator {
			t.Fatalf("stale override still applied: %+v", gate)
		}
	}
}

func TestOverrideGateRejectsHeadMismatch(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	appendGateRefreshEvent(t, fixture.Dir, claim, attempt, attempt.BaseSHA, attempt.BaseSHA, "head-sha-first", "head-sha-first", "2026-01-02T15:03:00Z")
	evaluation, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}

	_, err = OverrideGate(GateOverrideOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Gate:         "security",
		Status:       GateStatusRetainedByOperator,
		Reason:       "operator accepted base-only rebase",
		InputHash:    evaluation.InputHash,
		HeadSHA:      "wrong-head-sha",
		BaseSHA:      attempt.BaseSHA,
		Operator:     "operator-a",
		ExpiresIn:    time.Hour,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "does not match current head") {
		t.Fatalf("OverrideGate head mismatch error = %v", err)
	}
}

func TestOverrideGateRejectsExplicitExpiryBeforeObservedJournalTime(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	appendGateRefreshEvent(t, fixture.Dir, claim, attempt, attempt.BaseSHA, attempt.BaseSHA, "head-sha-first", "head-sha-first", "2026-01-02T15:03:00Z")
	evaluation, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}

	_, err = OverrideGate(GateOverrideOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Gate:         "security",
		Status:       GateStatusRetainedByOperator,
		Reason:       "operator accepted base-only rebase",
		InputHash:    evaluation.InputHash,
		HeadSHA:      "head-sha-first",
		BaseSHA:      attempt.BaseSHA,
		Operator:     "operator-a",
		ExpiresAt:    parseWorkspaceTestTime("2026-01-02T15:04:00Z"),
		Now:          fixedWorkspaceTime("2026-01-02T15:02:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "expiry must be in the future") {
		t.Fatalf("OverrideGate observed expiry error = %v", err)
	}
}

func TestOverrideGateRejectsMissingReason(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)

	_, err := OverrideGate(GateOverrideOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Gate:         "security",
		Status:       GateStatusRetainedByOperator,
		InputHash:    "input-hash",
		HeadSHA:      "head-sha",
		BaseSHA:      attempt.BaseSHA,
		Operator:     "operator-a",
		ExpiresIn:    time.Hour,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "requires --reason") {
		t.Fatalf("OverrideGate missing reason error = %v", err)
	}
}

func TestOverrideGateRejectsNonOverridableGate(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)

	_, err := OverrideGate(GateOverrideOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Gate:         "merge_approval",
		Status:       GateStatusRetainedByOperator,
		Reason:       "operator attempted approval override",
		InputHash:    "input-hash",
		HeadSHA:      "head-sha",
		BaseSHA:      attempt.BaseSHA,
		Operator:     "operator-a",
		ExpiresIn:    time.Hour,
		Now:          fixedWorkspaceTime("2026-01-02T15:05:05Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "gate merge_approval is not overridable") {
		t.Fatalf("OverrideGate non-overridable error = %v", err)
	}
}

func TestGateEvaluationEventDoesNotBreakSchedulerReplay(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	if _, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:04:05Z"),
	}); err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}
	if _, err := RebuildSchedulerView(fixture.Dir); err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
}

type gateClaimFixture struct {
	MergeUnitID string
	LeaseID     string
	AgentID     string
}

func startGateEvaluationAttempt(t *testing.T, workspaceDir string) (gateClaimFixture, AttemptResult) {
	t.Helper()
	claim, err := Next(NextOptions{
		WorkspaceDir: workspaceDir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedWorkspaceTime("2026-01-02T15:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-first",
		Now:          fixedWorkspaceTime("2026-01-02T15:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	return gateClaimFixture{MergeUnitID: claim.MergeUnitID, LeaseID: claim.LeaseID, AgentID: claim.AgentID}, attempt
}

func appendRefreshInputChangeEvent(t *testing.T, workspaceDir string, mergeUnitID string, attemptID string) {
	t.Helper()
	revisions, err := ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	refreshResource := RefreshResource(mergeUnitID + ":" + attemptID)
	inputResource := RefreshInputResource(mergeUnitID, attemptID, refreshInputBase)
	_, err = AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         EventBranchRefreshRecorded,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:  mergeUnitID,
			eventPayloadAttemptIDKey:    attemptID,
			eventPayloadStatusKey:       RefreshStatusSucceeded,
			eventPayloadEvidencePathKey: "state/refresh-evidence.json",
			eventPayloadBranchKey:       "feature/test",
			eventPayloadWorktreeKey:     filepath.Join(workspaceDir, "worktrees", "feature-test"),
			eventPayloadOldBaseKey:      "base-sha-first",
			eventPayloadNewBaseKey:      "base-sha-second",
			eventPayloadPreHeadKey:      "head-sha-first",
			eventPayloadPostHeadKey:     "head-sha-first",
			eventPayloadBackupRefKey:    "backup/test",
			eventPayloadInputChangesKey: []any{map[string]any{
				"input":     refreshInputBase,
				"old_value": "base-sha-first",
				"new_value": "base-sha-second",
				"resource":  inputResource,
			}},
		},
		ReadSet: map[string]int{
			refreshResource: revisions[refreshResource],
			inputResource:   revisions[inputResource],
		},
		WriteSet: []string{refreshResource, inputResource},
		Now:      fixedWorkspaceTime("2026-01-02T15:03:00Z"),
	})
	if err != nil {
		t.Fatalf("Append refresh event: %v", err)
	}
}

func appendGateRefreshEvent(t *testing.T, workspaceDir string, claim gateClaimFixture, attempt AttemptResult, oldBase string, newBase string, preHead string, postHead string, at string) {
	t.Helper()
	revisions, err := ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	refreshResource := RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	_, err = AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         EventBranchRefreshRecorded,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:  claim.MergeUnitID,
			eventPayloadAttemptIDKey:    attempt.AttemptID,
			eventPayloadStatusKey:       RefreshStatusSucceeded,
			eventPayloadEvidencePathKey: "state/refresh-evidence.json",
			eventPayloadBranchKey:       attempt.Branch,
			eventPayloadWorktreeKey:     attempt.Worktree,
			eventPayloadOldBaseKey:      oldBase,
			eventPayloadNewBaseKey:      newBase,
			eventPayloadPreHeadKey:      preHead,
			eventPayloadPostHeadKey:     postHead,
			eventPayloadBackupRefKey:    "backup/test",
		},
		ReadSet:  map[string]int{refreshResource: revisions[refreshResource]},
		WriteSet: []string{refreshResource},
		Now:      fixedWorkspaceTime(at),
	})
	if err != nil {
		t.Fatalf("Append refresh event: %v", err)
	}
}

func gateStatusesByName(gates []GateStatusView) map[string]string {
	statuses := map[string]string{}
	for _, gate := range gates {
		statuses[gate.Gate] = gate.Status
	}
	return statuses
}

func fixedWorkspaceTime(value string) func() time.Time {
	return func() time.Time {
		return parseWorkspaceTestTime(value)
	}
}

func parseWorkspaceTestTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
