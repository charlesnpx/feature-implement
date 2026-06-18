package workspace

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRebuildSchedulerViewEmptyJournalDefaultsFromLock(t *testing.T) {
	fixture := newChainedDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	if view.WorkspaceID != "workspace-chained" || view.Repo != fixture.Dir || view.BaseRef != fixtureWorkspaceBaseRef {
		t.Fatalf("view metadata = %+v", view)
	}
	if len(view.MergeUnits) != 2 {
		t.Fatalf("merge units = %+v", view.MergeUnits)
	}
	if view.Counts[MergeUnitPending] != 2 || view.Counts[MergeUnitCompleted] != 0 {
		t.Fatalf("counts = %+v", view.Counts)
	}
	if !reflect.DeepEqual(view.Ready, []string{"foundation:story-a"}) {
		t.Fatalf("ready = %+v", view.Ready)
	}
	if !reflect.DeepEqual(view.Blocked, []string{"sources:story-b"}) {
		t.Fatalf("blocked = %+v", view.Blocked)
	}
	if got := findSchedulerUnit(t, view, "sources:story-b").BlockedBy; !reflect.DeepEqual(got, []string{"foundation:story-a"}) {
		t.Fatalf("blocked by = %+v", got)
	}
	conditions := findSchedulerUnit(t, view, "sources:story-b").BlockingConditions
	if len(conditions) != 1 || conditions[0].Type != "dependency" || conditions[0].Resource != "foundation:story-a" {
		t.Fatalf("blocking conditions = %+v", conditions)
	}
	if _, err := os.Stat(SchedulerViewPath(fixture.Dir)); err != nil {
		t.Fatalf("scheduler view file missing: %v", err)
	}
}

func TestRebuildSchedulerViewReplaysMergeUnitTransition(t *testing.T) {
	fixture := newChainedDAGFixture(t).Workspace
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
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("Transition completed: %v", err)
	}

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	if got := findSchedulerUnit(t, view, "foundation:story-a").Status; got != MergeUnitCompleted {
		t.Fatalf("foundation status = %q", got)
	}
	if view.Counts[MergeUnitCompleted] != 1 || view.Counts[MergeUnitPending] != 1 {
		t.Fatalf("counts = %+v", view.Counts)
	}
	if !reflect.DeepEqual(view.Ready, []string{"sources:story-b"}) {
		t.Fatalf("ready = %+v", view.Ready)
	}
	if len(view.Blocked) != 0 {
		t.Fatalf("blocked = %+v", view.Blocked)
	}
}

func TestRebuildSchedulerViewRejectsUnknownEvent(t *testing.T) {
	fixture := newIndependentDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         "unexpected.event",
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	_, err := RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), `unknown scheduler event type "unexpected.event"`) {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestRebuildSchedulerViewRejectsAbandonedAttemptTransition(t *testing.T) {
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
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "tests failed",
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadFromKey:        MergeUnitPending,
			eventPayloadToKey:          MergeUnitInProgress,
			eventPayloadEvidenceKey:    map[string]any{evidenceWorktreeKey: attempt.Worktree},
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent transition: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), "without an active attempt") {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestRebuildSchedulerViewRejectsStaleAttemptTransition(t *testing.T) {
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
	first, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt first: %v", err)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    first.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "superseded",
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	second, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-2",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt second: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitCompleted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   first.AttemptID,
			eventPayloadFromKey:        MergeUnitPending,
			eventPayloadToKey:          MergeUnitCompleted,
			eventPayloadEvidenceKey:    map[string]any{evidenceCommitSHAKey: "commit-sha-old"},
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:04:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent transition: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), "not current active attempt "+second.AttemptID) {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestRebuildSchedulerViewRejectsUnsupportedAttemptTransition(t *testing.T) {
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
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitCompleted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadFromKey:        MergeUnitPending,
			eventPayloadToKey:          MergeUnitCompleted,
			eventPayloadEvidenceKey:    map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent transition: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), "unsupported workspace transition: pending -> completed") {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestRebuildSchedulerViewRejectsMismatchedTransitionLease(t *testing.T) {
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
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadAgentIDKey:     "worker-a",
			eventPayloadLeaseIDKey:     "wrong-lease",
			eventPayloadFromKey:        MergeUnitPending,
			eventPayloadToKey:          MergeUnitInProgress,
			eventPayloadEvidenceKey:    map[string]any{evidenceWorktreeKey: attempt.Worktree},
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent transition: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), "transition lease wrong-lease is not active for agent worker-a") {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestRebuildSchedulerViewRejectsExpiredTransitionLease(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir:  fixture.Dir,
		AgentID:       "worker-a",
		Claim:         true,
		LeaseDuration: time.Minute,
		Now:           fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:00:30Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadAgentIDKey:     "worker-a",
			eventPayloadLeaseIDKey:     claim.LeaseID,
			eventPayloadFromKey:        MergeUnitPending,
			eventPayloadToKey:          MergeUnitInProgress,
			eventPayloadEvidenceKey:    map[string]any{evidenceWorktreeKey: attempt.Worktree},
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent transition: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), "transition lease "+claim.LeaseID+" is not active for agent worker-a") {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestRebuildSchedulerViewRejectsTransitionBeforeAttemptStart(t *testing.T) {
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
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:10:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadAgentIDKey:     "worker-a",
			eventPayloadLeaseIDKey:     claim.LeaseID,
			eventPayloadFromKey:        MergeUnitPending,
			eventPayloadToKey:          MergeUnitInProgress,
			eventPayloadEvidenceKey:    map[string]any{evidenceWorktreeKey: attempt.Worktree},
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:05:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent transition: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), "attempt "+attempt.AttemptID+" has not started yet") {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestRebuildSchedulerViewRejectsFailedTransitionMissingReason(t *testing.T) {
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
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitFailed,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   attempt.AttemptID,
			eventPayloadAgentIDKey:     "worker-a",
			eventPayloadLeaseIDKey:     claim.LeaseID,
			eventPayloadFromKey:        MergeUnitPending,
			eventPayloadToKey:          MergeUnitFailed,
			eventPayloadEvidenceKey:    map[string]any{evidenceWorktreeKey: attempt.Worktree},
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent transition: %v", err)
	}

	_, err = RebuildSchedulerView(fixture.Dir)

	if err == nil || !strings.Contains(err.Error(), "transition evidence reason is required") {
		t.Fatalf("RebuildSchedulerView error = %v", err)
	}
}

func TestBuildSchedulerViewRejectsStaleReadSet(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	lock, err := BuildLock(fixture.Dir, fixture.Manifest)
	if err != nil {
		t.Fatalf("BuildLock: %v", err)
	}
	_, err = BuildSchedulerView(lock, []JournalEvent{
		{
			ID:       "evt-000000000001",
			Type:     EventMergeUnitStarted,
			Payload:  map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
			ReadSet:  map[string]int{MergeUnitResource("foundation:story-a"): 1},
			WriteSet: []string{MergeUnitResource("foundation:story-a")},
		},
	})

	var stale StaleResourceError
	if !errors.As(err, &stale) || stale.Resource != MergeUnitResource("foundation:story-a") {
		t.Fatalf("BuildSchedulerView stale error = %v", err)
	}
}

func TestRebuildSchedulerViewDiscardsLifecycleFromAbandonedAttempt(t *testing.T) {
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
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		Now:          fixedJournalTime("2026-06-17T10:02:30Z"),
	}); err != nil {
		t.Fatalf("Transition completed: %v", err)
	}
	if _, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "completion was invalid",
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	unit := findSchedulerUnit(t, view, "foundation:story-a")
	if unit.Status != MergeUnitPending || unit.CurrentAttempt != nil {
		t.Fatalf("abandoned attempt should not advance lifecycle: %+v", unit)
	}
	if view.Counts[MergeUnitCompleted] != 0 || view.Counts[MergeUnitPending] != 1 {
		t.Fatalf("counts = %+v", view.Counts)
	}
}

func TestRebuildSchedulerViewIsDeterministicAfterDelete(t *testing.T) {
	fixture := newIndependentDAGFixture(t).Workspace
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
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  claim.MergeUnitID,
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView first: %v", err)
	}
	if got := findSchedulerUnit(t, view, claim.MergeUnitID).Status; got != MergeUnitInProgress {
		t.Fatalf("%s status = %q", claim.MergeUnitID, got)
	}
	if view.Counts[MergeUnitInProgress] != 1 || view.Counts[MergeUnitPending] != 1 {
		t.Fatalf("counts = %+v", view.Counts)
	}
	firstBytes, err := os.ReadFile(SchedulerViewPath(fixture.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(SchedulerViewPath(fixture.Dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildSchedulerView(fixture.Dir); err != nil {
		t.Fatalf("RebuildSchedulerView second: %v", err)
	}
	secondBytes, err := os.ReadFile(SchedulerViewPath(fixture.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("scheduler view is not deterministic:\nfirst=%s\nsecond=%s", firstBytes, secondBytes)
	}
}

func writeWorkspaceLock(t *testing.T, workspaceDir string) {
	t.Helper()
	if _, err := Validate(ValidateOptions{WorkspaceDir: workspaceDir, WriteLock: true}); err != nil {
		t.Fatalf("Validate workspace lock: %v", err)
	}
}

func findSchedulerUnit(t *testing.T, view SchedulerView, id string) SchedulerMergeUnitView {
	t.Helper()
	for _, unit := range view.MergeUnits {
		if unit.ID == id {
			return unit
		}
	}
	t.Fatalf("scheduler unit %s not found in %+v", id, view.MergeUnits)
	return SchedulerMergeUnitView{}
}
