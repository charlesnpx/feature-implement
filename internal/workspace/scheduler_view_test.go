package workspace

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRebuildSchedulerViewEmptyJournalDefaultsFromLock(t *testing.T) {
	fixture := newChainedDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView: %v", err)
	}
	if view.WorkspaceID != "workspace-chained" || view.BaseRef != fixtureWorkspaceBaseRef {
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
	if _, err := os.Stat(SchedulerViewPath(fixture.Dir)); err != nil {
		t.Fatalf("scheduler view file missing: %v", err)
	}
}

func TestRebuildSchedulerViewReplaysMergeUnitTransition(t *testing.T) {
	fixture := newChainedDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitCompleted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
		WriteSet:     []string{MergeUnitResource("foundation:story-a")},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
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
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitCompleted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: "foundation:story-a",
			eventPayloadAttemptIDKey:   attempt.AttemptID,
		},
		WriteSet: []string{MergeUnitResource("foundation:story-a")},
		Now:      fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent completed: %v", err)
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
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "sources:story-b"},
		WriteSet:     []string{MergeUnitResource("sources:story-b")},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	view, err := RebuildSchedulerView(fixture.Dir)
	if err != nil {
		t.Fatalf("RebuildSchedulerView first: %v", err)
	}
	if got := findSchedulerUnit(t, view, "sources:story-b").Status; got != MergeUnitInProgress {
		t.Fatalf("sources status = %q", got)
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
