package workspace

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAppendEventCASIncrementsWrittenResource(t *testing.T) {
	workspaceDir := t.TempDir()
	workspace := WorkspaceResource("workspace-a")
	mergeUnit := MergeUnitResource("foundation:story-a")

	first, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.created",
		WriteSet:     []string{workspace},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent first: %v", err)
	}
	if got := first.WriteSet; len(got) != 1 || got[0] != workspace {
		t.Fatalf("first write set = %+v", got)
	}

	second, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "merge_unit.ready",
		ReadSet:      map[string]int{workspace: 1},
		WriteSet:     []string{mergeUnit},
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent second: %v", err)
	}
	if got := second.ReadSet[workspace]; got != 1 {
		t.Fatalf("second read set = %+v", second.ReadSet)
	}

	revisions, err := ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	if revisions[workspace] != 1 || revisions[mergeUnit] != 1 {
		t.Fatalf("revisions = %+v", revisions)
	}
	if len(revisions) != 2 {
		t.Fatalf("unexpected revisions = %+v", revisions)
	}
}

func TestAppendEventCASRejectsStaleReadSetBeforeAppend(t *testing.T) {
	workspaceDir := t.TempDir()
	workspace := WorkspaceResource("workspace-a")
	lease := LeaseResource("lease-a")
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.created",
		WriteSet:     []string{workspace},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent first: %v", err)
	}

	_, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "lease.claimed",
		ReadSet:      map[string]int{workspace: 0},
		WriteSet:     []string{lease},
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})

	var stale StaleResourceError
	if !errors.As(err, &stale) {
		t.Fatalf("AppendEvent error = %v", err)
	}
	if stale.Resource != workspace || stale.Expected != 0 || stale.Observed != 1 {
		t.Fatalf("stale error = %+v", stale)
	}
	events := readTestJournalEvents(t, workspaceDir)
	if len(events) != 1 {
		t.Fatalf("stale append should not write event: %+v", events)
	}
	revisions, err := ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	if revisions[lease] != 0 || revisions[workspace] != 1 {
		t.Fatalf("revisions = %+v", revisions)
	}
}

func TestAppendEventCASMultiResourceWrite(t *testing.T) {
	workspaceDir := t.TempDir()
	workspace := WorkspaceResource("workspace-a")
	mergeUnit := MergeUnitResource("foundation:story-a")
	lease := LeaseResource("lease-a")
	baseRef := BaseRefResource("workspace-orchestration")

	first, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.initialized",
		WriteSet:     []string{workspace, lease, mergeUnit, baseRef},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent first: %v", err)
	}
	wantWriteSet := []string{baseRef, lease, mergeUnit, workspace}
	if !equalStringSlices(first.WriteSet, wantWriteSet) {
		t.Fatalf("first write set = %+v, want %+v", first.WriteSet, wantWriteSet)
	}

	_, err = AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.refreshed",
		ReadSet: map[string]int{
			workspace: 1,
			mergeUnit: 1,
			lease:     1,
			baseRef:   1,
		},
		WriteSet: []string{workspace, baseRef},
		Now:      fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent second: %v", err)
	}

	revisions, err := ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	want := map[string]int{workspace: 2, baseRef: 2, mergeUnit: 1, lease: 1}
	for resource, revision := range want {
		if revisions[resource] != revision {
			t.Fatalf("%s revision = %d, want %d; all=%+v", resource, revisions[resource], revision, revisions)
		}
	}
	if len(revisions) != len(want) {
		t.Fatalf("unexpected revisions = %+v", revisions)
	}
}

func TestAppendEventCASAcceptsContractResources(t *testing.T) {
	workspaceDir := t.TempDir()
	contract := ContractResource("api-contract")
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         EventContractPublished,
		WriteSet:     []string{contract},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent contract resource: %v", err)
	}
	revisions, err := ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	if revisions[contract] != 1 {
		t.Fatalf("contract revision = %+v", revisions)
	}
}

func TestAppendEventCASRejectsUnsupportedResources(t *testing.T) {
	_, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: t.TempDir(),
		Type:         "external.updated",
		WriteSet:     []string{"database:core"},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), `unsupported resource kind "database"`) {
		t.Fatalf("AppendEvent error = %v", err)
	}
}

func TestAppendEventRejectsJournalLineWhitespace(t *testing.T) {
	workspaceDir := t.TempDir()
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.created",
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent first: %v", err)
	}
	b, err := os.ReadFile(EventsPath(workspaceDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EventsPath(workspaceDir), append([]byte(" "), b...), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.validated",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err == nil || !strings.Contains(err.Error(), "surrounding whitespace") {
		t.Fatalf("AppendEvent error = %v", err)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
