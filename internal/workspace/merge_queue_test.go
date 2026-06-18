package workspace

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueMergeUnitRecordsReadyAttempt(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)

	result, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       ready.Attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	if result.Status != mergeQueueStatusQueued || result.Queue == nil || result.Queue.Position != 1 {
		t.Fatalf("queue result = %+v", result)
	}
	if result.Queue.GateInputHash != ready.Evaluation.InputHash || result.Queue.GateOutputHash != ready.Evaluation.OutputHash {
		t.Fatalf("queue gate hashes = %+v, evaluation=%+v", result.Queue, ready.Evaluation)
	}
	events := readTestJournalEvents(t, ready.Fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventMergeQueueEntered {
		t.Fatalf("last event type = %q", last.Type)
	}
	if last.ReadSet[MergeQueueResource()] != 0 || !containsString(last.WriteSet, MergeQueueResource()) {
		t.Fatalf("queue event resources read=%+v write=%+v", last.ReadSet, last.WriteSet)
	}

	status, err := Status(ready.Fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.MergeQueue) != 1 || status.MergeQueue[0].QueueID != result.Queue.QueueID {
		t.Fatalf("status merge queue = %+v", status.MergeQueue)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, ready.Claim.MergeUnitID)
	if unit.MergeQueue == nil || unit.MergeQueue.QueueID != result.Queue.QueueID {
		t.Fatalf("unit queue status = %+v", unit.MergeQueue)
	}
}

func TestQueuedMergeUnitIsNotReadyAfterLeaseRelease(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	if _, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       ready.Attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	}); err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	if _, err := Release(LeaseOptions{
		WorkspaceDir: ready.Fixture.Dir,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		Now:          fixedWorkspaceTime("2026-01-02T15:10:00Z"),
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	status, err := Status(ready.Fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.Ready) != 0 || len(status.MergeQueue) != 1 {
		t.Fatalf("queued unit should not be ready: ready=%+v queue=%+v", status.Ready, status.MergeQueue)
	}
	next, err := Next(NextOptions{
		WorkspaceDir: ready.Fixture.Dir,
		AgentID:      "worker-other",
		Claim:        true,
		Now:          fixedWorkspaceTime("2026-01-02T15:11:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next.Status != "none" {
		t.Fatalf("queued unit should not be claimable: %+v", next)
	}
}

func TestQueueMergeUnitRequiresRefreshEvidence(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)

	blocked, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Branch:       attempt.Branch,
		HeadSHA:      "head-sha-first",
		BaseSHA:      attempt.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:02:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit before refresh: %v", err)
	}
	if blocked.Status != "blocked" || !hasBlockingConditionWithAction(blocked.BlockingConditions, "missing_refresh", mergeQueueRequiredActionRefresh) {
		t.Fatalf("missing refresh block result = %+v", blocked)
	}

	lock, err := readWorkspaceLock(filepath.Join(fixture.Dir, LockFileName))
	if err != nil {
		t.Fatalf("readWorkspaceLock: %v", err)
	}
	view, err := buildSchedulerViewAt(fixture.Dir, lock, readTestJournalEvents(t, fixture.Dir), fixedWorkspaceTime("2026-01-02T15:02:30Z")())
	if err != nil {
		t.Fatalf("buildSchedulerViewAt: %v", err)
	}
	unit := findSchedulerUnit(t, view, claim.MergeUnitID)
	if !hasBlockingConditionWithAction(unit.BlockingConditions, "missing_refresh", mergeQueueRequiredActionRefresh) {
		t.Fatalf("status missing refresh blockers = %+v", unit.BlockingConditions)
	}

	ready := prepareQueueReadiness(t, fixture, claim, attempt, "2026-01-02T15")
	queued, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit after refresh: %v", err)
	}
	if queued.Status != mergeQueueStatusQueued || queued.Queue == nil {
		t.Fatalf("queue after refresh = %+v", queued)
	}
}

func TestMergeQueueLeavesWhenGateHashesChange(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	if _, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       ready.Attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	}); err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	overrideQueueGate(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, ready.Evaluation.InputHash, "security", ready.HeadSHA, ready.BaseSHA, "2026-01-02T15:10:00Z")
	evaluateQueueGates(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, "2026-01-02T15:11:00Z")

	status, err := Status(ready.Fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.MergeQueue) != 0 {
		t.Fatalf("queue should be empty after gate hash change: %+v", status.MergeQueue)
	}
}

func TestQueueMergeUnitRejectsAmbiguousTarget(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	_, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		PR:           "47",
		Branch:       ready.Attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "accepts only one target") {
		t.Fatalf("ambiguous target error = %v", err)
	}
}

func TestQueueMergeUnitBlocksUnsatisfiedDependency(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startForcedQueueAttempt(t, fixture.Dir, "sources:story-b", "worker-b", "2026-01-02T15")
	ready := prepareQueueReadiness(t, fixture, claim, attempt, "2026-01-02T15")

	result, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	if result.Status != "blocked" || !hasBlockingCondition(result.BlockingConditions, "dependency") {
		t.Fatalf("dependency block result = %+v", result)
	}
}

func TestQueueMergeUnitBlocksStaleContract(t *testing.T) {
	fixture, claim, attempt := staleContractFixture(t, false)
	ready := prepareQueueReadiness(t, fixture, queueClaimFromNext(claim), attempt, "2026-06-17T10")

	result, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedJournalTime("2026-06-17T10:19:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	if result.Status != "blocked" || !hasBlockingCondition(result.BlockingConditions, "stale_contract") {
		t.Fatalf("stale contract block result = %+v", result)
	}
}

func TestQueueMergeUnitBlocksStaleApproval(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	appendGateRefreshEvent(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, ready.BaseSHA, "base-sha-second", ready.HeadSHA, "head-sha-second", "2026-01-02T15:09:00Z")
	evaluation := evaluateQueueGates(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, "2026-01-02T15:10:00Z")
	overrideQueueGate(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, evaluation.InputHash, "review", "head-sha-second", "base-sha-second", "2026-01-02T15:11:00Z")
	overrideQueueGate(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, evaluation.InputHash, "security", "head-sha-second", "base-sha-second", "2026-01-02T15:12:00Z")
	overrideQueueGate(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, evaluation.InputHash, "test", "head-sha-second", "base-sha-second", "2026-01-02T15:13:00Z")
	evaluateQueueGates(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, "2026-01-02T15:14:00Z")

	result, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       ready.Attempt.Branch,
		HeadSHA:      "head-sha-second",
		BaseSHA:      "base-sha-second",
		Now:          fixedWorkspaceTime("2026-01-02T15:15:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	if result.Status != "blocked" || !hasBlockingCondition(result.BlockingConditions, "stale_approval") {
		t.Fatalf("stale approval block result = %+v", result)
	}
}

func TestMergeQueueLeavesWhenRefreshInputsChange(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	result, err := QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		Branch:       ready.Attempt.Branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime("2026-01-02T15:09:00Z"),
	})
	if err != nil {
		t.Fatalf("QueueMergeUnit: %v", err)
	}
	if result.Status != mergeQueueStatusQueued {
		t.Fatalf("queue result = %+v", result)
	}

	appendGateRefreshEvent(t, ready.Fixture.Dir, ready.Claim, ready.Attempt, ready.BaseSHA, "base-sha-second", ready.HeadSHA, "head-sha-second", "2026-01-02T15:10:00Z")
	status, err := Status(ready.Fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.MergeQueue) != 0 {
		t.Fatalf("queue should be empty after refresh input change: %+v", status.MergeQueue)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, ready.Claim.MergeUnitID)
	if unit.MergeQueue != nil {
		t.Fatalf("unit should leave queue after refresh input change: %+v", unit.MergeQueue)
	}
}

func TestAppendMergeQueueEventRejectsStaleQueueResource(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	entry := mergeQueueSnapshot{
		QueueID:        "queue-a",
		MergeUnitID:    "foundation:story-a",
		AttemptID:      "foundation:story-a:attempt-1",
		AgentID:        "worker-a",
		LeaseID:        "lease-a",
		ApprovalID:     "approval-a",
		Scope:          "merge-unit",
		Branch:         "feature/test",
		HeadSHA:        "head-sha",
		BaseSHA:        "base-sha",
		GateInputHash:  "input-hash",
		GateOutputHash: "output-hash",
		Position:       1,
		QueuedAt:       parseWorkspaceTestTime("2026-01-02T15:00:00Z"),
	}
	readSet := map[string]int{
		MergeQueueResource():             revisions[MergeQueueResource()],
		QueueSlotResource(entry.QueueID): revisions[QueueSlotResource(entry.QueueID)],
	}
	if _, err := appendMergeQueueEvent(fixture.Dir, entry, readSet, parseWorkspaceTestTime("2026-01-02T15:00:00Z")); err != nil {
		t.Fatalf("appendMergeQueueEvent first: %v", err)
	}
	entry.QueueID = "queue-b"
	entry.QueuedAt = parseWorkspaceTestTime("2026-01-02T15:01:00Z")
	delete(readSet, QueueSlotResource("queue-a"))
	readSet[QueueSlotResource(entry.QueueID)] = revisions[QueueSlotResource(entry.QueueID)]
	_, err = appendMergeQueueEvent(fixture.Dir, entry, readSet, parseWorkspaceTestTime("2026-01-02T15:01:00Z"))
	var stale StaleResourceError
	if !errors.As(err, &stale) || stale.Resource != MergeQueueResource() {
		t.Fatalf("appendMergeQueueEvent stale error = %v stale=%+v", err, stale)
	}
}

type queueReadyAttemptFixture struct {
	Fixture    workspaceFixture
	Claim      gateClaimFixture
	Attempt    AttemptResult
	Approval   ApprovalResult
	Evaluation GateEvaluationResult
	HeadSHA    string
	BaseSHA    string
}

func newQueueReadyAttemptFixture(t *testing.T) queueReadyAttemptFixture {
	t.Helper()
	fixture := newOnePlanWorkspaceFixture(t)
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claim, attempt := startGateEvaluationAttempt(t, fixture.Dir)
	return prepareQueueReadiness(t, fixture, claim, attempt, "2026-01-02T15")
}

func prepareQueueReadiness(t *testing.T, fixture workspaceFixture, claim gateClaimFixture, attempt AttemptResult, prefix string) queueReadyAttemptFixture {
	t.Helper()
	baseSHA := attempt.BaseSHA
	headSHA := "head-sha-first"
	appendGateRefreshEvent(t, fixture.Dir, claim, attempt, baseSHA, baseSHA, headSHA, headSHA, prefix+":03:00Z")
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionMerge},
		Branch:       attempt.Branch,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		MaxUses:      1,
		ExpiresAt:    parseWorkspaceTestTime("2027-01-02T15:00:00Z"),
		Now:          fixedWorkspaceTime(prefix + ":04:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	evaluation := evaluateQueueGates(t, fixture.Dir, claim, attempt, prefix+":05:00Z")
	overrideQueueGate(t, fixture.Dir, claim, attempt, evaluation.InputHash, "review", headSHA, baseSHA, prefix+":06:00Z")
	overrideQueueGate(t, fixture.Dir, claim, attempt, evaluation.InputHash, "security", headSHA, baseSHA, prefix+":07:00Z")
	overrideQueueGate(t, fixture.Dir, claim, attempt, evaluation.InputHash, "test", headSHA, baseSHA, prefix+":08:00Z")
	evaluation = evaluateQueueGates(t, fixture.Dir, claim, attempt, prefix+":08:30Z")
	return queueReadyAttemptFixture{
		Fixture:    fixture,
		Claim:      claim,
		Attempt:    attempt,
		Approval:   approval,
		Evaluation: evaluation,
		HeadSHA:    headSHA,
		BaseSHA:    baseSHA,
	}
}

func startForcedQueueAttempt(t *testing.T, workspaceDir string, mergeUnitID string, agentID string, prefix string) (gateClaimFixture, AttemptResult) {
	t.Helper()
	leaseID := mergeUnitID + ":" + agentID + ":lease"
	expiresAt := parseWorkspaceTestTime(prefix + ":30:00Z").UTC().Format(time.RFC3339Nano)
	revisions, err := ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	leaseResource := LeaseResource(mergeUnitID)
	mergeUnitResource := MergeUnitResource(mergeUnitID)
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         EventLeaseGranted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:    mergeUnitID,
			eventPayloadLeaseIDKey:        leaseID,
			eventPayloadAgentIDKey:        agentID,
			eventPayloadLeaseExpiresAtKey: expiresAt,
		},
		ReadSet: map[string]int{
			leaseResource:     revisions[leaseResource],
			mergeUnitResource: revisions[mergeUnitResource],
		},
		WriteSet: []string{leaseResource, mergeUnitResource},
		Now:      fixedWorkspaceTime(prefix + ":00:00Z"),
	}); err != nil {
		t.Fatalf("Append lease: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  mergeUnitID,
		AgentID:      agentID,
		LeaseID:      leaseID,
		BaseSHA:      "base-sha-first",
		Now:          fixedWorkspaceTime(prefix + ":01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	return gateClaimFixture{MergeUnitID: mergeUnitID, LeaseID: leaseID, AgentID: agentID}, attempt
}

func evaluateQueueGates(t *testing.T, workspaceDir string, claim gateClaimFixture, attempt AttemptResult, at string) GateEvaluationResult {
	t.Helper()
	evaluation, err := EvaluateGates(GateEvaluateOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Now:          fixedWorkspaceTime(at),
	})
	if err != nil {
		t.Fatalf("EvaluateGates: %v", err)
	}
	return evaluation
}

func overrideQueueGate(t *testing.T, workspaceDir string, claim gateClaimFixture, attempt AttemptResult, inputHash string, gate string, headSHA string, baseSHA string, at string) {
	t.Helper()
	if _, err := OverrideGate(GateOverrideOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Gate:         gate,
		Status:       GateStatusRetainedByOperator,
		Reason:       "operator accepted queue readiness",
		InputHash:    inputHash,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		Operator:     "operator-a",
		ExpiresAt:    parseWorkspaceTestTime("2027-01-02T15:00:00Z"),
		Now:          fixedWorkspaceTime(at),
	}); err != nil {
		t.Fatalf("OverrideGate %s: %v", gate, err)
	}
}

func queueClaimFromNext(claim NextResult) gateClaimFixture {
	return gateClaimFixture{MergeUnitID: claim.MergeUnitID, LeaseID: claim.LeaseID, AgentID: claim.AgentID}
}

func hasBlockingCondition(conditions []SchedulerBlockingCondition, conditionType string) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}

func hasBlockingConditionWithAction(conditions []SchedulerBlockingCondition, conditionType string, requiredAction string) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType && condition.RequiredAction == requiredAction {
			return true
		}
	}
	return false
}
