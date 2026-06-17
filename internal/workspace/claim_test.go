package workspace

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
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

	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitCompleted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
		WriteSet:     []string{MergeUnitResource("foundation:story-a")},
		Now:          fixedJournalTime("2026-06-17T10:02:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent completion: %v", err)
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
	if got := thirdEvent.ReadSet[dependencyResource]; got != 2 {
		t.Fatalf("dependency read set revision = %d, want 2; read_set=%+v", got, thirdEvent.ReadSet)
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
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: fixture.Dir,
		Type:         EventMergeUnitStarted,
		Payload:      map[string]any{eventPayloadMergeUnitIDKey: "foundation:story-a"},
		WriteSet:     []string{MergeUnitResource("foundation:story-a")},
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent start: %v", err)
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
