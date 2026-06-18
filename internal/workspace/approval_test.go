package workspace

import (
	"strings"
	"testing"
	"time"
)

func TestApprovalGrantCheckConsumeAndMaxUses(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"push"},
		Branch:       "feature/test",
		MaxUses:      2,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	if granted.Status != "granted" || granted.Approval.Status != "active" || granted.Approval.MaxUses != 2 {
		t.Fatalf("grant result = %+v", granted)
	}

	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "push",
		Branch:       "feature/test",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval: %v", err)
	}
	if check.Status != "approved" || len(check.Approvals) != 1 || check.Approvals[0].ApprovalID != granted.Approval.ApprovalID {
		t.Fatalf("check result = %+v", check)
	}

	first, err := ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "push",
		Branch:       "feature/test",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("ConsumeApproval first: %v", err)
	}
	if first.Status != "consumed" || first.Approval.UsedCount != 1 || first.Approval.Status != "active" {
		t.Fatalf("first consume = %+v", first)
	}
	second, err := ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "push",
		Branch:       "feature/test",
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err != nil {
		t.Fatalf("ConsumeApproval second: %v", err)
	}
	if second.Approval.UsedCount != 2 || second.Approval.Status != "exhausted" {
		t.Fatalf("second consume = %+v", second)
	}
	_, err = ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "push",
		Branch:       "feature/test",
		Now:          fixedJournalTime("2026-06-17T10:06:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "has no uses remaining") {
		t.Fatalf("third consume error = %v", err)
	}
}

func TestApprovalExpiryPreventsConsumption(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"push"},
		ExpiresIn:    time.Minute,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "push",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval: %v", err)
	}
	if check.Status != "denied" || len(check.Approvals) != 0 {
		t.Fatalf("expired check = %+v", check)
	}
	_, err = ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "push",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "expired at") {
		t.Fatalf("expired consume error = %v", err)
	}
}

func TestApprovalScopeMismatchAndMergeTargetValidation(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	_, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"merge"},
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "merge approvals require") {
		t.Fatalf("untargeted merge grant error = %v", err)
	}
	_, err = GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"merge"},
		Branch:       "workspace-orchestration",
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:30Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "merge approvals require") {
		t.Fatalf("loose merge grant error = %v", err)
	}
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"merge"},
		Branch:       "workspace-orchestration",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval targeted merge: %v", err)
	}
	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval missing use target: %v", err)
	}
	if check.Status != "denied" || len(check.Approvals) != 0 {
		t.Fatalf("untargeted merge check = %+v", check)
	}
	_, err = ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "merge approval use requires") {
		t.Fatalf("untargeted merge consume error = %v", err)
	}
	check, err = CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		Branch:       "other-branch",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval mismatch: %v", err)
	}
	if check.Status != "denied" || len(check.Approvals) != 0 {
		t.Fatalf("mismatched check = %+v", check)
	}
	_, err = ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		Branch:       "other-branch",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "branch workspace-orchestration") {
		t.Fatalf("mismatched consume error = %v", err)
	}
}

func TestApprovalRejectsReplayedUntargetedMergeApproval(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	approvalID := "legacy-merge-approval"
	resource := ApprovalResource(approvalID)
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventApprovalGranted,
		Payload: map[string]any{
			eventPayloadApprovalIDKey:  approvalID,
			eventPayloadMergeUnitIDKey: claim.MergeUnitID,
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadAgentIDKey:     "worker-a",
			eventPayloadLeaseIDKey:     claim.LeaseID,
			eventPayloadActionsKey:     []string{"merge"},
			eventPayloadScopeKey:       "merge-unit",
			eventPayloadMaxUsesKey:     1,
			eventPayloadExpiresAtKey:   "2026-06-17T11:00:00Z",
		},
		ReadSet:  map[string]int{resource: revisions[resource]},
		WriteSet: []string{resource},
		Now:      fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent legacy approval: %v", err)
	}

	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval: %v", err)
	}
	if check.Status != "denied" || len(check.Approvals) != 0 {
		t.Fatalf("legacy merge check = %+v", check)
	}
	_, err = ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   approvalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "missing required merge target") {
		t.Fatalf("legacy merge consume error = %v", err)
	}
}

func TestApprovalPRURLMatchesNumber(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"merge"},
		PR:           "https://github.com/charlesnpx/feature-implement/pull/35",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	if granted.Approval.PR != "35" {
		t.Fatalf("grant PR = %q", granted.Approval.PR)
	}
	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		PR:           "35",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval: %v", err)
	}
	if check.Status != "approved" || len(check.Approvals) != 1 || check.Approvals[0].ApprovalID != granted.Approval.ApprovalID {
		t.Fatalf("check result = %+v", check)
	}
	consumed, err := ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		PR:           "35",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("ConsumeApproval: %v", err)
	}
	if consumed.Status != "consumed" || consumed.Approval.UsedCount != 1 {
		t.Fatalf("consume result = %+v", consumed)
	}
}

func TestApprovalConsumeReadsRefreshInputResources(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"merge"},
		PR:           "42",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	if _, err := ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		PR:           "42",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("ConsumeApproval: %v", err)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventApprovalConsumed {
		t.Fatalf("last event = %+v", last)
	}
	baseResource := RefreshInputResource(claim.MergeUnitID, attempt.AttemptID, refreshInputBase)
	headResource := RefreshInputResource(claim.MergeUnitID, attempt.AttemptID, refreshInputHead)
	if _, ok := last.ReadSet[baseResource]; !ok {
		t.Fatalf("consume read set missing %s: %+v", baseResource, last.ReadSet)
	}
	if _, ok := last.ReadSet[headResource]; !ok {
		t.Fatalf("consume read set missing %s: %+v", headResource, last.ReadSet)
	}
}

func TestApprovalReplayRejectsConsumedEventMissingTarget(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"merge"},
		Branch:       "workspace-orchestration",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	resource := ApprovalResource(granted.Approval.ApprovalID)
	revisions, err := ResourceRevisions(fixture.Dir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventApprovalConsumed,
		Payload: map[string]any{
			eventPayloadApprovalIDKey:  granted.Approval.ApprovalID,
			eventPayloadMergeUnitIDKey: claim.MergeUnitID,
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadActionsKey:     []string{"merge"},
			eventPayloadScopeKey:       "merge-unit",
			eventPayloadUsedCountKey:   1,
		},
		ReadSet:  map[string]int{resource: revisions[resource]},
		WriteSet: []string{resource},
		Now:      fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent invalid consume: %v", err)
	}

	_, err = CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		Action:       "merge",
		Branch:       "workspace-orchestration",
		HeadSHA:      "head-sha",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "merge approval use requires") {
		t.Fatalf("invalid replay check error = %v", err)
	}
}

func TestApprovalRejectsNegativeMaxUses(t *testing.T) {
	fixture, claim, attempt := newApprovalAttemptFixture(t)
	_, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"push"},
		MaxUses:      -1,
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "--max-uses must be greater than zero") {
		t.Fatalf("negative max uses error = %v", err)
	}
}

func TestApprovalDoesNotCarryForwardToFreshAttempt(t *testing.T) {
	fixture, claim, first := newApprovalAttemptFixture(t)
	granted, err := GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    first.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Actions:      []string{"push"},
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("GrantApproval: %v", err)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    first.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "restart",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	second, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-2",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt second: %v", err)
	}
	check, err := CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    second.AttemptID,
		Action:       "push",
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err != nil {
		t.Fatalf("CheckApproval second: %v", err)
	}
	if check.Status != "denied" || len(check.Approvals) != 0 {
		t.Fatalf("fresh attempt approval check = %+v", check)
	}
	_, err = CheckApproval(ApprovalCheckOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    first.AttemptID,
		Action:       "push",
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "not current active attempt") {
		t.Fatalf("old attempt check error = %v", err)
	}
	_, err = ConsumeApproval(ApprovalConsumeOptions{
		WorkspaceDir: fixture.Dir,
		ApprovalID:   granted.Approval.ApprovalID,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    first.AttemptID,
		Action:       "push",
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "not current active attempt") {
		t.Fatalf("old attempt consume error = %v", err)
	}
}

func TestApprovalGrantRequiresLeaseThatStartedAttempt(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)
	firstClaim, err := Next(NextOptions{
		WorkspaceDir:  fixture.Dir,
		AgentID:       "worker-a",
		Claim:         true,
		LeaseDuration: time.Minute,
		Now:           fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	firstAttempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  firstClaim.MergeUnitID,
		AgentID:      "worker-a",
		LeaseID:      firstClaim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:00:30Z"),
	})
	if err != nil {
		t.Fatalf("first StartAttempt: %v", err)
	}
	secondClaim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if secondClaim.MergeUnitID != firstClaim.MergeUnitID || secondClaim.LeaseID == firstClaim.LeaseID {
		t.Fatalf("second claim = %+v, first = %+v", secondClaim, firstClaim)
	}

	_, err = GrantApproval(ApprovalGrantOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  secondClaim.MergeUnitID,
		AttemptID:    firstAttempt.AttemptID,
		AgentID:      "worker-b",
		LeaseID:      secondClaim.LeaseID,
		Actions:      []string{"push"},
		ExpiresIn:    time.Hour,
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "was started under lease "+firstClaim.LeaseID) {
		t.Fatalf("grant with replacement lease error = %v", err)
	}
}

func newApprovalAttemptFixture(t *testing.T) (workspaceFixture, NextResult, AttemptResult) {
	t.Helper()
	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)
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
	return fixture, claim, attempt
}
