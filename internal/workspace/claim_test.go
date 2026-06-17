package workspace

import (
	"reflect"
	"testing"
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
	if !reflect.DeepEqual(event.WriteSet, []string{LeaseResource("foundation:story-a")}) {
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
