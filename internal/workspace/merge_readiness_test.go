package workspace

import (
	"strings"
	"testing"
)

func TestReserveMergeIntentRequiresFrontOfQueue(t *testing.T) {
	dag := newIndependentDAGFixture(t)
	fixture := dag.Workspace
	if _, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	firstClaim, firstAttempt := startForcedQueueAttempt(t, fixture.Dir, "foundation:story-a", "worker-a", "2026-01-02T15")
	firstReady := prepareQueueReadiness(t, fixture, firstClaim, firstAttempt, "2026-01-02T15")
	if _, err := queueMergeReadyAttempt(t, firstReady, "", firstAttempt.Branch, "2026-01-02T15:09:00Z"); err != nil {
		t.Fatalf("queue first: %v", err)
	}

	secondClaim, secondAttempt := startForcedQueueAttempt(t, fixture.Dir, "sources:story-b", "worker-b", "2026-01-02T15")
	secondReady := prepareQueueReadiness(t, fixture, secondClaim, secondAttempt, "2026-01-02T15")
	if _, err := queueMergeReadyAttempt(t, secondReady, "", secondAttempt.Branch, "2026-01-02T15:10:00Z"); err != nil {
		t.Fatalf("queue second: %v", err)
	}

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.MergeQueue) != 2 || status.MergeQueue[1].Position != 2 || !hasBlockingCondition(status.MergeQueue[1].BlockingConditions, "queue_position") {
		t.Fatalf("queued position blockers = %+v", status.MergeQueue)
	}

	_, err = ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      secondClaim.MergeUnitID,
		AttemptID:        secondAttempt.AttemptID,
		AgentID:          secondClaim.AgentID,
		LeaseID:          secondClaim.LeaseID,
		ApprovalID:       secondReady.Approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		Branch:           secondAttempt.Branch,
		RequestedHeadSHA: secondReady.HeadSHA,
		ExpectedBaseSHA:  secondReady.BaseSHA,
		Now:              fixedWorkspaceTime("2026-01-02T15:11:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "queue position 1") || !strings.Contains(err.Error(), "observed position 2") {
		t.Fatalf("queue position error = %v", err)
	}
}

func TestReserveMergeIntentRejectsChangedHead(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	if _, err := queueMergeReadyAttempt(t, ready, "", ready.Attempt.Branch, "2026-01-02T15:09:00Z"); err != nil {
		t.Fatalf("queue: %v", err)
	}

	_, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     ready.Fixture.Dir,
		MergeUnitID:      ready.Claim.MergeUnitID,
		AttemptID:        ready.Attempt.AttemptID,
		AgentID:          ready.Claim.AgentID,
		LeaseID:          ready.Claim.LeaseID,
		ApprovalID:       ready.Approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		Branch:           ready.Attempt.Branch,
		RequestedHeadSHA: "head-sha-second",
		ExpectedBaseSHA:  ready.BaseSHA,
		Now:              fixedWorkspaceTime("2026-01-02T15:10:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "is for head head-sha-first") {
		t.Fatalf("changed head error = %v", err)
	}
}

func TestReserveMergeIntentRejectsStaleQueuedContract(t *testing.T) {
	fixture := newContractWorkspaceFixture(t)
	publishFixtureContract(t, fixture, "v1", "producer-commit-1", "2026-06-17T10:00:00Z")
	claim, attempt := startFixtureConsumerAttempt(t, fixture, "2026-06-17T10")
	if _, err := BindContract(ContractBindOptions{
		WorkspaceDir: fixture.Dir,
		ContractID:   "api-contract",
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		CommandResults: []ContractCommandResult{{
			Command: "go test ./...",
			Status:  "passed",
		}},
		Now: fixedJournalTime("2026-06-17T10:09:00Z"),
	}); err != nil {
		t.Fatalf("BindContract: %v", err)
	}
	ready := prepareMergeReadinessWithFreshApproval(t, fixture, claim, attempt, "", attempt.Branch, "head-sha-first", attempt.BaseSHA, "2026-06-17T10")
	queued, err := queueMergeReadyAttempt(t, ready, "", attempt.Branch, "2026-06-17T10:17:00Z")
	if err != nil {
		t.Fatalf("queue: %v", err)
	}

	writeContractArtifact(t, fixture.Dir, "openapi: 3.1.0\ninfo:\n  title: changed\n")
	publishFixtureContract(t, fixture, "v2", "producer-commit-2", "2026-06-17T10:18:00Z")

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(status.MergeQueue) != 0 {
		t.Fatalf("stale contract queue should leave live queue: %+v", status.MergeQueue)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, claim.MergeUnitID)
	if !hasBlockingCondition(unit.BlockingConditions, "stale_contract") {
		t.Fatalf("stale contract blockers = %+v", unit.BlockingConditions)
	}

	_, err = ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     fixture.Dir,
		MergeUnitID:      claim.MergeUnitID,
		AttemptID:        attempt.AttemptID,
		AgentID:          claim.AgentID,
		LeaseID:          claim.LeaseID,
		ApprovalID:       ready.Approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		Branch:           attempt.Branch,
		RequestedHeadSHA: ready.HeadSHA,
		ExpectedBaseSHA:  ready.BaseSHA,
		Now:              fixedJournalTime("2026-06-17T10:19:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "live merge queue") {
		t.Fatalf("stale contract reserve error = %v", err)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventMergeQueueStale || last.Payload[eventPayloadQueueIDKey] != queued.Queue.QueueID {
		t.Fatalf("stale queue event = %+v", last)
	}
}

func TestReserveMergeIntentRecordsQueueExitAndAllowsCompletion(t *testing.T) {
	ready := newQueueReadyAttemptFixture(t)
	queued, err := queueMergeReadyAttempt(t, ready, "", ready.Attempt.Branch, "2026-01-02T15:09:00Z")
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: ready.Attempt.Worktree},
		Now:          fixedWorkspaceTime("2026-01-02T15:09:30Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}

	reserved, err := ReserveExternalIntent(ExternalIntentReserveOptions{
		WorkspaceDir:     ready.Fixture.Dir,
		MergeUnitID:      ready.Claim.MergeUnitID,
		AttemptID:        ready.Attempt.AttemptID,
		AgentID:          ready.Claim.AgentID,
		LeaseID:          ready.Claim.LeaseID,
		ApprovalID:       ready.Approval.Approval.ApprovalID,
		Action:           ExternalActionMerge,
		Branch:           ready.Attempt.Branch,
		RequestedHeadSHA: ready.HeadSHA,
		ExpectedBaseSHA:  ready.BaseSHA,
		Now:              fixedWorkspaceTime("2026-01-02T15:10:00Z"),
	})
	if err != nil {
		t.Fatalf("ReserveExternalIntent: %v", err)
	}
	if reserved.Intent.QueueID != queued.Queue.QueueID || reserved.Intent.QueuePosition != 1 {
		t.Fatalf("reserved queue metadata = %+v queued=%+v", reserved.Intent, queued.Queue)
	}
	events := readTestJournalEvents(t, ready.Fixture.Dir)
	last := events[len(events)-1]
	if _, ok := last.ReadSet[MergeQueueResource()]; !ok {
		t.Fatalf("reserve read set missing %s: %+v", MergeQueueResource(), last.ReadSet)
	}
	if last.Payload[eventPayloadQueueReasonKey] != mergeQueueExitReasonReserved {
		t.Fatalf("reserve queue reason = %+v", last.Payload)
	}
	assertContainsString(t, last.WriteSet, QueueSlotResource(queued.Queue.QueueID))

	status, err := Status(ready.Fixture.Dir)
	if err != nil {
		t.Fatalf("Status after reserve: %v", err)
	}
	if len(status.MergeQueue) != 0 {
		t.Fatalf("queue should exit after merge intent reservation: %+v", status.MergeQueue)
	}

	if _, err := RecordExternalIntentResult(ExternalIntentResultRecordOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		IntentID:     reserved.Intent.IntentID,
		Status:       ExternalResultSucceeded,
		Now:          fixedWorkspaceTime("2026-01-02T15:11:00Z"),
	}); err != nil {
		t.Fatalf("RecordExternalIntentResult: %v", err)
	}
	completed, err := Transition(TransitionOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence: map[string]any{
			evidenceCommitSHAKey:        "commit-sha-1",
			evidenceExternalIntentIDKey: reserved.Intent.IntentID,
		},
		Now: fixedWorkspaceTime("2026-01-02T15:12:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition complete: %v", err)
	}
	if completed.EventType != EventMergeUnitCompleted {
		t.Fatalf("completed = %+v", completed)
	}
}

func queueMergeReadyAttempt(t *testing.T, ready queueReadyAttemptFixture, pr string, branch string, at string) (MergeQueueResult, error) {
	t.Helper()
	return QueueMergeUnit(MergeQueueOptions{
		WorkspaceDir: ready.Fixture.Dir,
		MergeUnitID:  ready.Claim.MergeUnitID,
		AttemptID:    ready.Attempt.AttemptID,
		AgentID:      ready.Claim.AgentID,
		LeaseID:      ready.Claim.LeaseID,
		ApprovalID:   ready.Approval.Approval.ApprovalID,
		PR:           pr,
		Branch:       branch,
		HeadSHA:      ready.HeadSHA,
		BaseSHA:      ready.BaseSHA,
		Now:          fixedWorkspaceTime(at),
	})
}

func prepareMergeReadinessWithFreshApproval(t *testing.T, fixture workspaceFixture, claim NextResult, attempt AttemptResult, pr string, branch string, headSHA string, baseSHA string, prefix string) queueReadyAttemptFixture {
	t.Helper()
	gateClaim := queueClaimFromNext(claim)
	appendGateRefreshEvent(t, fixture.Dir, gateClaim, attempt, attempt.BaseSHA, baseSHA, headSHA, headSHA, prefix+":10:00Z")
	approval, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      claim.AgentID,
		LeaseID:      claim.LeaseID,
		Actions:      []string{ExternalActionMerge},
		PR:           pr,
		Branch:       branch,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		MaxUses:      1,
		ExpiresAt:    parseWorkspaceTestTime("2027-01-02T15:00:00Z"),
		Now:          fixedWorkspaceTime(prefix + ":11:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	evaluation := evaluateQueueGates(t, fixture.Dir, gateClaim, attempt, prefix+":12:00Z")
	overrideQueueGate(t, fixture.Dir, gateClaim, attempt, evaluation.InputHash, "review", headSHA, baseSHA, prefix+":13:00Z")
	overrideQueueGate(t, fixture.Dir, gateClaim, attempt, evaluation.InputHash, "security", headSHA, baseSHA, prefix+":14:00Z")
	overrideQueueGate(t, fixture.Dir, gateClaim, attempt, evaluation.InputHash, "test", headSHA, baseSHA, prefix+":15:00Z")
	evaluation = evaluateQueueGates(t, fixture.Dir, gateClaim, attempt, prefix+":16:00Z")
	return queueReadyAttemptFixture{
		Fixture:    fixture,
		Claim:      gateClaim,
		Attempt:    attempt,
		Approval:   approval,
		Evaluation: evaluation,
		HeadSHA:    headSHA,
		BaseSHA:    baseSHA,
	}
}
