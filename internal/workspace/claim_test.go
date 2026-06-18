package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNextClaimsFirstReadyMergeUnitDeterministically(t *testing.T) {
	fixture := newIndependentDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)

	result, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if result.Status != "claimed" || result.MergeUnitID != "foundation:story-a" {
		t.Fatalf("claim result = %+v", result)
	}
	if result.AgentID != "worker-a" || result.Lifecycle != MergeUnitPending {
		t.Fatalf("claim metadata = %+v", result)
	}
	if result.LeaseID != "foundation:story-a:worker-a:1781690400000000000" {
		t.Fatalf("lease id = %q", result.LeaseID)
	}
	if result.LeaseExpiresAt != "2026-06-17T10:30:00Z" {
		t.Fatalf("lease expiry = %q", result.LeaseExpiresAt)
	}

	events := readTestJournalEvents(t, fixture.Dir)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	if event.Type != EventLeaseGranted {
		t.Fatalf("event type = %q", event.Type)
	}
	if event.Payload[eventPayloadMergeUnitIDKey] != "foundation:story-a" ||
		event.Payload[eventPayloadLeaseIDKey] != result.LeaseID ||
		event.Payload[eventPayloadAgentIDKey] != "worker-a" ||
		event.Payload[eventPayloadLeaseExpiresAtKey] != result.LeaseExpiresAt {
		t.Fatalf("event payload = %+v", event.Payload)
	}
	wantReadSet := map[string]int{
		LeaseResource("foundation:story-a"):     0,
		MergeUnitResource("foundation:story-a"): 0,
	}
	if !reflect.DeepEqual(event.ReadSet, wantReadSet) {
		t.Fatalf("event read set = %+v", event.ReadSet)
	}
	wantWriteSet := []string{
		LeaseResource("foundation:story-a"),
		MergeUnitResource("foundation:story-a"),
	}
	if !reflect.DeepEqual(event.WriteSet, wantWriteSet) {
		t.Fatalf("event write set = %+v", event.WriteSet)
	}
}

func TestNextSkipsBlockedDependencies(t *testing.T) {
	fixture := newBlockedDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)

	first, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next first: %v", err)
	}
	if first.MergeUnitID != "foundation:story-a" {
		t.Fatalf("first claim = %+v", first)
	}

	second, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("Next second: %v", err)
	}
	if second.Status != "none" {
		t.Fatalf("blocked dependencies should produce no claim before completion: %+v", second)
	}

	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      first.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      first.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:30Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	if _, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      first.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		Now:          fixedJournalTime("2026-06-17T10:02:45Z"),
	}); err != nil {
		t.Fatalf("Transition complete: %v", err)
	}
	third, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("Next third: %v", err)
	}
	if third.Status != "claimed" || third.MergeUnitID != "foundation:story-c" {
		t.Fatalf("dependency-ready claim = %+v", third)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	thirdEvent := events[len(events)-1]
	dependencyResource := MergeUnitResource("foundation:story-a")
	if got := thirdEvent.ReadSet[dependencyResource]; got != 4 {
		t.Fatalf("dependency read set revision = %d, want 4; read_set=%+v", got, thirdEvent.ReadSet)
	}
}

func TestNextPreventsDuplicateActiveClaim(t *testing.T) {
	fixture := newIndependentDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)

	first, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next first: %v", err)
	}
	second, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("Next second: %v", err)
	}
	if first.MergeUnitID != "foundation:story-a" {
		t.Fatalf("first claim = %+v", first)
	}
	if second.Status != "claimed" || second.MergeUnitID != "sources:story-b" {
		t.Fatalf("second claim should skip active lease and claim next ready unit: %+v", second)
	}

	third, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-c",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("Next third: %v", err)
	}
	if third.Status != "none" {
		t.Fatalf("all ready units have active leases: %+v", third)
	}
}

func TestNextConcurrentClaimsAreUnique(t *testing.T) {
	for run := 0; run < 20; run++ {
		fixture := newIndependentDAGFixture(t).Workspace
		writeWorkspaceLock(t, fixture.Dir)

		const workers = 12
		var wg sync.WaitGroup
		results := make(chan NextResult, workers)
		errors := make(chan error, workers)
		for i := 0; i < workers; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := Next(NextOptions{
					WorkspaceDir: fixture.Dir,
					AgentID:      "worker-" + string(rune('a'+i)),
					Claim:        true,
				})
				if err != nil {
					errors <- err
					return
				}
				results <- result
			}()
		}
		wg.Wait()
		close(results)
		close(errors)
		for err := range errors {
			t.Fatalf("run %d concurrent Next error: %v", run, err)
		}
		claimed := map[string]string{}
		noneCount := 0
		for result := range results {
			if result.Status == "none" {
				noneCount++
				continue
			}
			if result.Status != "claimed" {
				t.Fatalf("run %d unexpected result = %+v", run, result)
			}
			if prior := claimed[result.MergeUnitID]; prior != "" {
				t.Fatalf("run %d duplicate claim for %s by %s and %s", run, result.MergeUnitID, prior, result.AgentID)
			}
			claimed[result.MergeUnitID] = result.AgentID
		}
		if len(claimed) != 2 || noneCount != workers-2 {
			t.Fatalf("run %d claimed=%+v none=%d", run, claimed, noneCount)
		}
		revisions, err := ResourceRevisions(fixture.Dir)
		if err != nil {
			t.Fatalf("run %d ResourceRevisions: %v", run, err)
		}
		for _, id := range []string{"foundation:story-a", "sources:story-b"} {
			if revisions[LeaseResource(id)] != 1 || revisions[MergeUnitResource(id)] != 1 {
				t.Fatalf("run %d revisions for %s = %+v", run, id, revisions)
			}
		}
		events := readTestJournalEvents(t, fixture.Dir)
		if len(events) != 2 {
			t.Fatalf("run %d events = %+v", run, events)
		}
	}
}

func TestNextDoesNotRetryPermanentJournalStaleReadSet(t *testing.T) {
	fixture := newIndependentDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)
	event := JournalEvent{
		ID:           "evt-000000000001",
		Timestamp:    "2026-06-17T10:00:00Z",
		PreviousHash: zeroEventHash,
		Type:         EventLeaseGranted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
		ReadSet:      map[string]int{MergeUnitResource("foundation:story-a"): 1},
		WriteSet:     []string{LeaseResource("foundation:story-a")},
	}
	eventHash, err := hashJournalEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	event.EventHash = eventHash
	if err := os.MkdirAll(StateDir(fixture.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := appendJournalEvent(EventsPath(fixture.Dir), event); err != nil {
		t.Fatal(err)
	}

	_, err = Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})

	var stale StaleResourceError
	if !errors.As(err, &stale) || stale.Resource != MergeUnitResource("foundation:story-a") {
		t.Fatalf("Next stale error = %v", err)
	}
	if strings.Contains(err.Error(), "did not stabilize") {
		t.Fatalf("permanent stale read set was retried as a claim race: %v", err)
	}
	if got := len(readTestJournalEvents(t, fixture.Dir)); got != 1 {
		t.Fatalf("permanent stale read set should not append journal event; got %d events", got)
	}
}

func TestClaimReadyMergeUnitRejectsStaleSnapshot(t *testing.T) {
	fixture := newIndependentDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)
	lock, err := readWorkspaceLock(filepath.Join(fixture.Dir, LockFileName))
	if err != nil {
		t.Fatalf("readWorkspaceLock: %v", err)
	}
	view, err := BuildSchedulerView(lock, nil)
	if err != nil {
		t.Fatalf("BuildSchedulerView: %v", err)
	}
	unit := findSchedulerUnit(t, view, "foundation:story-a")
	revisions := map[string]int{}

	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitCompleted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
		WriteSet:     []string{MergeUnitResource("foundation:story-a")},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent completion: %v", err)
	}

	_, err = claimReadyMergeUnit(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		Claim:        true,
	}, view, unit, fixedJournalTime("2026-06-17T10:01:00Z")(), DefaultLeaseDuration, revisions)

	var stale StaleResourceError
	if !errors.As(err, &stale) {
		t.Fatalf("claimReadyMergeUnit error = %v", err)
	}
	if stale.Resource != MergeUnitResource("foundation:story-a") || stale.Expected != 0 || stale.Observed != 1 {
		t.Fatalf("stale error = %+v", stale)
	}
}

func TestHeartbeatExtendsOwningActiveLease(t *testing.T) {
	fixture := newIndependentDAGFixture(t).Workspace
	writeWorkspaceLock(t, fixture.Dir)
	claim, err := Next(NextOptions{
		WorkspaceDir:  fixture.Dir,
		AgentID:       "worker-a",
		Claim:         true,
		LeaseDuration: 10 * time.Minute,
		Now:           fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	heartbeat, err := Heartbeat(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if heartbeat.Status != "extended" || heartbeat.LeaseID != claim.LeaseID || heartbeat.AgentID != "worker-a" {
		t.Fatalf("heartbeat result = %+v", heartbeat)
	}
	if heartbeat.LeaseExpiresAt != "2026-06-17T10:35:00Z" {
		t.Fatalf("heartbeat expiry = %q", heartbeat.LeaseExpiresAt)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	event := events[len(events)-1]
	if event.Type != EventLeaseHeartbeat {
		t.Fatalf("heartbeat event type = %q", event.Type)
	}
	if event.Payload[eventPayloadLeaseExpiresAtKey] != heartbeat.LeaseExpiresAt {
		t.Fatalf("heartbeat payload = %+v", event.Payload)
	}
}

func TestReleaseMakesPendingMergeUnitClaimableAgain(t *testing.T) {
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

	released, err := Release(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.Status != "released" || released.Lifecycle != MergeUnitPending {
		t.Fatalf("release result = %+v", released)
	}

	next, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("Next after release: %v", err)
	}
	if next.Status != "claimed" || next.MergeUnitID != "foundation:story-a" || next.AgentID != "worker-b" {
		t.Fatalf("released unit should be claimable again: %+v", next)
	}
}

func TestReleaseDoesNotMakeAdvancedLifecycleClaimable(t *testing.T) {
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
		Now:          fixedJournalTime("2026-06-17T10:01:30Z"),
	}); err != nil {
		t.Fatalf("Transition start: %v", err)
	}

	released, err := Release(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.Lifecycle != MergeUnitInProgress {
		t.Fatalf("release lifecycle = %+v", released)
	}

	next, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("Next after release: %v", err)
	}
	if next.Status != "none" {
		t.Fatalf("advanced lifecycle should not be claimable: %+v", next)
	}
}

func TestLeaseOperationsRejectWrongLeaseAndAgent(t *testing.T) {
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

	_, err = Heartbeat(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		LeaseID:      "wrong-lease",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "active lease not found: wrong-lease") {
		t.Fatalf("wrong lease error = %v", err)
	}

	_, err = Release(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "is owned by agent worker-a, not worker-b") {
		t.Fatalf("wrong agent error = %v", err)
	}
}

func TestLeaseOperationsRejectStaleSnapshotUnderCAS(t *testing.T) {
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

	state, err := loadLeaseOperationState(fixture.Dir, fixedJournalTime("2026-06-17T10:01:00Z")())
	if err != nil {
		t.Fatalf("loadLeaseOperationState: %v", err)
	}
	lease, _, err := requireOwnedActiveLease(state, claim.LeaseID, "worker-a")
	if err != nil {
		t.Fatalf("requireOwnedActiveLease: %v", err)
	}

	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
		WriteSet:     []string{MergeUnitResource("foundation:story-a")},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent start: %v", err)
	}

	err = appendLeaseEvent(fixture.Dir, EventLeaseReleased, lease, "", state.Revisions, fixedJournalTime("2026-06-17T10:03:00Z")())

	var stale StaleResourceError
	if !errors.As(err, &stale) || stale.Resource != MergeUnitResource("foundation:story-a") {
		t.Fatalf("release should reject stale CAS snapshot: %v", err)
	}
}

func TestHeartbeatRejectsExpiredLease(t *testing.T) {
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

	_, err = Heartbeat(LeaseOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "active lease not found") {
		t.Fatalf("expired heartbeat error = %v", err)
	}
}

func TestSchedulerViewReflectsActiveLeaseState(t *testing.T) {
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
	lock, err := readWorkspaceLock(filepath.Join(fixture.Dir, LockFileName))
	if err != nil {
		t.Fatalf("readWorkspaceLock: %v", err)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	view, err := buildSchedulerViewAt(lock, events, fixedJournalTime("2026-06-17T10:01:00Z")())
	if err != nil {
		t.Fatalf("buildSchedulerViewAt: %v", err)
	}
	if len(view.Ready) != 0 {
		t.Fatalf("leased unit should not be ready: %+v", view.Ready)
	}
	if !reflect.DeepEqual(view.Leased, []string{"foundation:story-a"}) {
		t.Fatalf("leased = %+v", view.Leased)
	}
	unit := findSchedulerUnit(t, view, "foundation:story-a")
	if unit.ActiveLease == nil || unit.ActiveLease.LeaseID != claim.LeaseID || unit.ActiveLease.AgentID != "worker-a" {
		t.Fatalf("active lease = %+v", unit.ActiveLease)
	}
}

func TestRecoverMarksExpiredLeaseAndMakesUnitClaimable(t *testing.T) {
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

	recovered, err := Recover(RecoverOptions{
		WorkspaceDir: fixture.Dir,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if recovered.Status != "recovered" || recovered.RecoveredCount != 1 {
		t.Fatalf("recover result = %+v", recovered)
	}
	if len(recovered.Recovered) != 1 || recovered.Recovered[0].LeaseID != claim.LeaseID {
		t.Fatalf("recovered leases = %+v", recovered.Recovered)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventLeaseRecovered || last.Payload[eventPayloadLeaseIDKey] != claim.LeaseID {
		t.Fatalf("recovery event = %+v", last)
	}

	next, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("Next after recover: %v", err)
	}
	if next.Status != "claimed" || next.MergeUnitID != "foundation:story-a" || next.AgentID != "worker-b" {
		t.Fatalf("recovered unit should be claimable: %+v", next)
	}
}

func TestRecoverPreservesActiveLease(t *testing.T) {
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

	recovered, err := Recover(RecoverOptions{
		WorkspaceDir: fixture.Dir,
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if recovered.Status != "unchanged" || recovered.RecoveredCount != 0 {
		t.Fatalf("active lease should not recover: %+v", recovered)
	}
	if !reflect.DeepEqual(recovered.Leased, []string{"foundation:story-a"}) || len(recovered.Ready) != 0 {
		t.Fatalf("view state = ready %+v leased %+v", recovered.Ready, recovered.Leased)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	if len(events) != 1 || events[0].Payload[eventPayloadLeaseIDKey] != claim.LeaseID {
		t.Fatalf("recover should not append history for active lease: %+v", events)
	}
}

func TestRecoverRebuildsSchedulerViewWithoutHistoryChange(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)

	recovered, err := Recover(RecoverOptions{
		WorkspaceDir: fixture.Dir,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if recovered.Status != "unchanged" || recovered.RecoveredCount != 0 {
		t.Fatalf("recover result = %+v", recovered)
	}
	if _, err := os.Stat(SchedulerViewPath(fixture.Dir)); err != nil {
		t.Fatalf("scheduler view was not rebuilt: %v", err)
	}
	if _, err := os.Stat(EventsPath(fixture.Dir)); err == nil {
		events := readTestJournalEvents(t, fixture.Dir)
		if len(events) != 0 {
			t.Fatalf("view-only recovery should not append events: %+v", events)
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat events: %v", err)
	}
}

func TestStartAttemptRequiresActiveLease(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	writeWorkspaceLock(t, fixture.Dir)

	_, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      "missing-lease",
		BaseSHA:      "base-sha",
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "active lease not found: missing-lease") {
		t.Fatalf("missing lease error = %v", err)
	}
}

func TestStartAttemptRecordsFirstAttempt(t *testing.T) {
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
	if attempt.Status != "started" || attempt.AttemptID != "foundation:story-a:attempt-1" || attempt.AttemptNumber != 1 {
		t.Fatalf("attempt result = %+v", attempt)
	}
	if attempt.Branch != "feature/workspace-a/foundation/story-a/attempt-1" {
		t.Fatalf("branch = %q", attempt.Branch)
	}
	wantWorktree := filepath.Join(fixture.Dir, "state", "worktrees", "workspace-a", "foundation", "story-a", "attempt-1")
	if attempt.Worktree != wantWorktree {
		t.Fatalf("worktree = %q, want %q", attempt.Worktree, wantWorktree)
	}
	if attempt.BaseRef != fixtureWorkspaceBaseRef || attempt.BaseSHA != "base-sha-1" || attempt.Mode != "fresh-from-base" {
		t.Fatalf("base/mode metadata = %+v", attempt)
	}
	wantCommand := "git worktree add -b feature/workspace-a/foundation/story-a/attempt-1 " + wantWorktree + " workspace-orchestration"
	if len(attempt.Commands) != 1 || attempt.Commands[0] != wantCommand {
		t.Fatalf("commands = %+v, want %q", attempt.Commands, wantCommand)
	}

	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventAttemptStarted {
		t.Fatalf("attempt event = %+v", last)
	}
	if last.Payload[eventPayloadAttemptIDKey] != attempt.AttemptID ||
		last.Payload[eventPayloadBranchKey] != attempt.Branch ||
		last.Payload[eventPayloadWorktreeKey] != attempt.Worktree ||
		last.Payload[eventPayloadBaseSHAKey] != attempt.BaseSHA ||
		last.Payload[eventPayloadModeKey] != attempt.Mode {
		t.Fatalf("attempt payload = %+v", last.Payload)
	}
	wantReadSet := map[string]int{
		LeaseResource("foundation:story-a"):     1,
		MergeUnitResource("foundation:story-a"): 1,
	}
	if !reflect.DeepEqual(last.ReadSet, wantReadSet) {
		t.Fatalf("attempt read set = %+v, want %+v", last.ReadSet, wantReadSet)
	}
	if len(last.WriteSet) != 1 || last.WriteSet[0] != MergeUnitResource("foundation:story-a") {
		t.Fatalf("attempt write set = %+v", last.WriteSet)
	}

	lock, err := readWorkspaceLock(filepath.Join(fixture.Dir, LockFileName))
	if err != nil {
		t.Fatalf("readWorkspaceLock: %v", err)
	}
	view, err := buildSchedulerViewAt(lock, events, fixedJournalTime("2026-06-17T10:02:00Z")())
	if err != nil {
		t.Fatalf("buildSchedulerViewAt: %v", err)
	}
	unit := findSchedulerUnit(t, view, "foundation:story-a")
	if unit.CurrentAttempt == nil || unit.CurrentAttempt.AttemptID != attempt.AttemptID || unit.CurrentAttempt.Branch != attempt.Branch {
		t.Fatalf("current attempt = %+v", unit.CurrentAttempt)
	}
}

func TestStartAttemptQuotesWorktreeCommandWithSpaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace root with spaces")
	workspaceDir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := WorkspaceManifest{
		SchemaVersion: manifestSchemaVersion,
		ID:            "workspace-a",
		Repo:          ".",
		BaseRef:       fixtureWorkspaceBaseRef,
		Remote:        "origin",
		Plans: []WorkspacePlanRef{{
			ID:   "foundation",
			Path: filepath.ToSlash(filepath.Join("plans", "foundation")),
		}},
	}
	materializeFixturePlan(t, workspaceDir, fixtureWorkspaceBaseRef, workspaceFixturePlan{ID: "foundation", StoryID: "story-a"})
	writeWorkspaceManifest(t, workspaceDir, manifest)
	writeWorkspaceLock(t, workspaceDir)
	claim, err := Next(NextOptions{
		WorkspaceDir: workspaceDir,
		AgentID:      "worker-a",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: workspaceDir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	wantWorktree := filepath.Join(workspaceDir, "state", "worktrees", "workspace-a", "foundation", "story-a", "attempt-1")
	wantCommand := "git worktree add -b feature/workspace-a/foundation/story-a/attempt-1 '" + wantWorktree + "' workspace-orchestration"
	if len(attempt.Commands) != 1 || attempt.Commands[0] != wantCommand {
		t.Fatalf("commands = %+v, want %q", attempt.Commands, wantCommand)
	}
}

func TestStartAttemptRejectsExistingActiveAttempt(t *testing.T) {
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

	_, err = StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		BaseSHA:      "base-sha-2",
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "already has active attempt "+first.AttemptID) {
		t.Fatalf("active attempt error = %v", err)
	}
}

func TestAbandonAttemptRecordsReasonAndClearsCurrentAttempt(t *testing.T) {
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

	abandoned, err := AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    first.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "tests failed",
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}
	if abandoned.Status != attemptStatusAbandoned || abandoned.AttemptID != first.AttemptID || abandoned.Reason != "tests failed" {
		t.Fatalf("abandon result = %+v", abandoned)
	}

	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventAttemptAbandoned {
		t.Fatalf("abandon event = %+v", last)
	}
	if last.Payload[eventPayloadStatusKey] != attemptStatusAbandoned ||
		last.Payload[eventPayloadReasonKey] != "tests failed" ||
		last.Payload[eventPayloadAttemptIDKey] != first.AttemptID ||
		last.Payload[eventPayloadLeaseIDKey] != claim.LeaseID {
		t.Fatalf("abandon payload = %+v", last.Payload)
	}
	wantReadSet := map[string]int{
		LeaseResource("foundation:story-a"):     1,
		MergeUnitResource("foundation:story-a"): 2,
	}
	if !reflect.DeepEqual(last.ReadSet, wantReadSet) {
		t.Fatalf("abandon read set = %+v, want %+v", last.ReadSet, wantReadSet)
	}
	if len(last.WriteSet) != 1 || last.WriteSet[0] != MergeUnitResource("foundation:story-a") {
		t.Fatalf("abandon write set = %+v", last.WriteSet)
	}

	lock, err := readWorkspaceLock(filepath.Join(fixture.Dir, LockFileName))
	if err != nil {
		t.Fatalf("readWorkspaceLock: %v", err)
	}
	view, err := buildSchedulerViewAt(lock, events, fixedJournalTime("2026-06-17T10:03:00Z")())
	if err != nil {
		t.Fatalf("buildSchedulerViewAt: %v", err)
	}
	if unit := findSchedulerUnit(t, view, "foundation:story-a"); unit.CurrentAttempt != nil {
		t.Fatalf("current attempt should be cleared after abandon: %+v", unit.CurrentAttempt)
	}
}

func TestStartAttemptIncrementsAfterAbandon(t *testing.T) {
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
		Reason:       "retry with a clean branch",
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
	if second.AttemptID != "foundation:story-a:attempt-2" || second.AttemptNumber != 2 {
		t.Fatalf("second attempt = %+v", second)
	}
	attempts, err := attemptSnapshots(readTestJournalEvents(t, fixture.Dir))
	if err != nil {
		t.Fatalf("attemptSnapshots: %v", err)
	}
	if len(attempts["foundation:story-a"]) != 2 || attempts["foundation:story-a"][0].Status != attemptStatusAbandoned || attempts["foundation:story-a"][1].Status != attemptStatusActive {
		t.Fatalf("attempt audit state = %+v", attempts["foundation:story-a"])
	}
}

func TestAbandonAttemptRejectsNonCurrentAttempt(t *testing.T) {
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
		t.Fatalf("AbandonAttempt first: %v", err)
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

	_, err = AbandonAttempt(AttemptAbandonOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    first.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		Reason:       "old attempt",
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "not current active attempt "+second.AttemptID) {
		t.Fatalf("non-current abandon error = %v", err)
	}
}

func TestTransitionRecordsLifecycleForCurrentAttempt(t *testing.T) {
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

	started, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence: map[string]any{
			evidenceWorktreeKey: attempt.Worktree,
		},
		Now: fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition start: %v", err)
	}
	if started.Status != "transitioned" || started.EventType != EventMergeUnitStarted || started.From != MergeUnitPending || started.To != MergeUnitInProgress {
		t.Fatalf("transition result = %+v", started)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventMergeUnitStarted {
		t.Fatalf("transition event = %+v", last)
	}
	if last.Payload[eventPayloadAttemptIDKey] != attempt.AttemptID ||
		last.Payload[eventPayloadLeaseIDKey] != claim.LeaseID ||
		last.Payload[eventPayloadFromKey] != MergeUnitPending ||
		last.Payload[eventPayloadToKey] != MergeUnitInProgress {
		t.Fatalf("transition payload = %+v", last.Payload)
	}
	evidence, ok := last.Payload[eventPayloadEvidenceKey].(map[string]any)
	if !ok || evidence[evidenceWorktreeKey] != attempt.Worktree {
		t.Fatalf("transition evidence = %+v", last.Payload[eventPayloadEvidenceKey])
	}
	wantReadSet := map[string]int{
		LeaseResource("foundation:story-a"):                        1,
		MergeUnitResource("foundation:story-a"):                    2,
		RefreshResource("foundation:story-a:" + attempt.AttemptID): 0,
	}
	if !reflect.DeepEqual(last.ReadSet, wantReadSet) {
		t.Fatalf("transition read set = %+v, want %+v", last.ReadSet, wantReadSet)
	}
	if len(last.WriteSet) != 1 || last.WriteSet[0] != MergeUnitResource("foundation:story-a") {
		t.Fatalf("transition write set = %+v", last.WriteSet)
	}

	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, "foundation:story-a")
	if unit.Status != MergeUnitInProgress || unit.CurrentAttempt == nil || unit.CurrentAttempt.AttemptID != attempt.AttemptID {
		t.Fatalf("scheduler unit = %+v", unit)
	}
}

func TestTransitionCompletesLocalLifecycleWithCommitEvidence(t *testing.T) {
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
	completed, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitInProgress,
		To:           MergeUnitCompleted,
		Evidence:     map[string]any{evidenceCommitSHAKey: "commit-sha-1"},
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition complete: %v", err)
	}
	if completed.EventType != EventMergeUnitCompleted || completed.Evidence[evidenceCommitSHAKey] != "commit-sha-1" {
		t.Fatalf("completed = %+v", completed)
	}
	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, "foundation:story-a")
	if unit.Status != MergeUnitCompleted {
		t.Fatalf("scheduler unit = %+v", unit)
	}
}

func TestTransitionFailsLocalLifecycleWithReasonEvidence(t *testing.T) {
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

	failed, err := Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitFailed,
		Evidence:     map[string]any{evidenceReasonKey: "tests failed"},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}
	if failed.EventType != EventMergeUnitFailed || failed.Evidence[evidenceReasonKey] != "tests failed" {
		t.Fatalf("failed = %+v", failed)
	}
	events := readTestJournalEvents(t, fixture.Dir)
	last := events[len(events)-1]
	if last.Type != EventMergeUnitFailed {
		t.Fatalf("transition event = %+v", last)
	}
	evidence, ok := last.Payload[eventPayloadEvidenceKey].(map[string]any)
	if !ok || evidence[evidenceReasonKey] != "tests failed" {
		t.Fatalf("transition evidence = %+v", last.Payload[eventPayloadEvidenceKey])
	}
	status, err := Status(fixture.Dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	unit := findSchedulerUnit(t, SchedulerView{MergeUnits: status.MergeUnits}, "foundation:story-a")
	if unit.Status != MergeUnitFailed {
		t.Fatalf("scheduler unit = %+v", unit)
	}
}

func TestWorkspaceOperationsDoNotMutatePlanLock(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)
	planLockPath := filepath.Join(fixture.Plans["foundation"], planLockFileName)
	before, err := os.ReadFile(planLockPath)
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("Transition: %v", err)
	}

	after, err := os.ReadFile(planLockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("workspace operations mutated plan lock:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestTransitionRejectsMissingLease(t *testing.T) {
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

	_, err = Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      "missing-lease",
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "active lease not found: missing-lease") {
		t.Fatalf("missing lease error = %v", err)
	}
}

func TestTransitionRejectsLeaseBeforeGrantTimestamp(t *testing.T) {
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

	_, err = Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T09:59:59Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "active lease not found: "+claim.LeaseID) {
		t.Fatalf("future-dated lease error = %v", err)
	}
}

func TestTransitionRejectsAttemptBeforeStartTimestamp(t *testing.T) {
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

	_, err = Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:05:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "attempt "+attempt.AttemptID+" has not started yet") {
		t.Fatalf("future-dated attempt error = %v", err)
	}
}

func TestTransitionRejectsLeaseThatDidNotStartAttempt(t *testing.T) {
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
		t.Fatalf("Next first: %v", err)
	}
	attempt, err := StartAttempt(AttemptStartOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AgentID:      "worker-a",
		LeaseID:      firstClaim.LeaseID,
		BaseSHA:      "base-sha-1",
		Now:          fixedJournalTime("2026-06-17T10:00:30Z"),
	})
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := Recover(RecoverOptions{
		WorkspaceDir: fixture.Dir,
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	secondClaim, err := Next(NextOptions{
		WorkspaceDir: fixture.Dir,
		AgentID:      "worker-b",
		Claim:        true,
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err != nil {
		t.Fatalf("Next second: %v", err)
	}

	_, err = Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-b",
		LeaseID:      secondClaim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:04:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "was started under lease "+firstClaim.LeaseID+", not "+secondClaim.LeaseID) {
		t.Fatalf("wrong lease error = %v", err)
	}
}

func TestTransitionRejectsAbandonedAttempt(t *testing.T) {
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
		Reason:       "retry",
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AbandonAttempt: %v", err)
	}

	_, err = Transition(TransitionOptions{
		WorkspaceDir: fixture.Dir,
		MergeUnitID:  "foundation:story-a",
		AttemptID:    attempt.AttemptID,
		AgentID:      "worker-a",
		LeaseID:      claim.LeaseID,
		From:         MergeUnitPending,
		To:           MergeUnitInProgress,
		Evidence:     map[string]any{evidenceWorktreeKey: attempt.Worktree},
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "has no active attempt") {
		t.Fatalf("abandoned attempt error = %v", err)
	}
}

func TestTransitionRejectsStaleCASSnapshot(t *testing.T) {
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
	state, err := loadLeaseOperationState(fixture.Dir, fixedJournalTime("2026-06-17T10:02:00Z")())
	if err != nil {
		t.Fatalf("loadLeaseOperationState: %v", err)
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
		WriteSet:     []string{MergeUnitResource("foundation:story-a")},
		Now:          fixedJournalTime("2026-06-17T10:03:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	err = appendTransitionEvent(fixture.Dir, TransitionOptions{
		MergeUnitID: "foundation:story-a",
		AttemptID:   attempt.AttemptID,
		AgentID:     "worker-a",
		LeaseID:     claim.LeaseID,
		From:        MergeUnitPending,
		To:          MergeUnitInProgress,
	}, EventMergeUnitStarted, map[string]any{evidenceWorktreeKey: attempt.Worktree}, state.Revisions, fixedJournalTime("2026-06-17T10:04:00Z")())

	var stale StaleResourceError
	if !errors.As(err, &stale) || stale.Resource != MergeUnitResource("foundation:story-a") {
		t.Fatalf("transition should reject stale CAS snapshot: %v", err)
	}
	if got := len(readTestJournalEvents(t, fixture.Dir)); got != 3 {
		t.Fatalf("stale transition should not append journal event; got %d events", got)
	}
}
