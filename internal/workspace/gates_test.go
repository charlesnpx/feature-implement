package workspace

import (
	"os"
	"path/filepath"
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

func gateStatusesByName(gates []GateStatusView) map[string]string {
	statuses := map[string]string{}
	for _, gate := range gates {
		statuses[gate.Gate] = gate.Status
	}
	return statuses
}

func fixedWorkspaceTime(value string) func() time.Time {
	return func() time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			panic(err)
		}
		return parsed
	}
}
