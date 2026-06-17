package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendEventWritesFirstJournalEvent(t *testing.T) {
	workspaceDir := t.TempDir()
	now := fixedJournalTime("2026-06-17T10:00:00Z")

	event, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.created",
		Payload:      map[string]any{"workspace_id": "workspace-a"},
		Now:          now,
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if event.ID != "evt-000000000001" {
		t.Fatalf("id = %q", event.ID)
	}
	if event.Timestamp != "2026-06-17T10:00:00Z" {
		t.Fatalf("timestamp = %q", event.Timestamp)
	}
	if event.PreviousHash != zeroEventHash {
		t.Fatalf("previous hash = %q", event.PreviousHash)
	}
	if event.EventHash == "" || event.EventHash != mustJournalHash(t, event) {
		t.Fatalf("event hash = %q", event.EventHash)
	}
	if _, err := os.Stat(StateDir(workspaceDir)); err != nil {
		t.Fatalf("state dir missing: %v", err)
	}
	if _, err := os.Stat(EventsPath(workspaceDir)); err != nil {
		t.Fatalf("events file missing: %v", err)
	}
	if _, err := os.Stat(JournalLockPath(workspaceDir)); !os.IsNotExist(err) {
		t.Fatalf("journal lock should be removed after append: %v", err)
	}
	events := readTestJournalEvents(t, workspaceDir)
	if len(events) != 1 || events[0].EventHash != event.EventHash {
		t.Fatalf("events = %+v", events)
	}
}

func TestAppendEventChainsJournalEvents(t *testing.T) {
	workspaceDir := t.TempDir()
	first, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.created",
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent first: %v", err)
	}
	second, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.validated",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent second: %v", err)
	}

	if second.ID != "evt-000000000002" {
		t.Fatalf("second id = %q", second.ID)
	}
	if second.PreviousHash != first.EventHash {
		t.Fatalf("second previous hash = %q, want %q", second.PreviousHash, first.EventHash)
	}
	events := readTestJournalEvents(t, workspaceDir)
	if len(events) != 2 || events[1].EventHash != second.EventHash {
		t.Fatalf("events = %+v", events)
	}
}

func TestAppendEventReplaysLargeIntegerPayload(t *testing.T) {
	workspaceDir := t.TempDir()
	first, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.counted",
		Payload:      map[string]any{"count": int64(9_007_199_254_740_993)},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent first: %v", err)
	}

	second, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.validated",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent second: %v", err)
	}
	if second.PreviousHash != first.EventHash {
		t.Fatalf("second previous hash = %q, want %q", second.PreviousHash, first.EventHash)
	}
}

func TestAppendEventReplaysLargeJournalLine(t *testing.T) {
	workspaceDir := t.TempDir()
	largePayload := strings.Repeat("x", 70*1024)
	first, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.large",
		Payload:      map[string]any{"blob": largePayload},
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent first: %v", err)
	}

	second, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.validated",
		Now:          fixedJournalTime("2026-06-17T10:01:00Z"),
	})
	if err != nil {
		t.Fatalf("AppendEvent second: %v", err)
	}
	if second.PreviousHash != first.EventHash {
		t.Fatalf("second previous hash = %q, want %q", second.PreviousHash, first.EventHash)
	}
}

func TestAppendEventRejectsCorruptJSONL(t *testing.T) {
	workspaceDir := t.TempDir()
	if err := os.MkdirAll(StateDir(workspaceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(EventsPath(workspaceDir), []byte("{not-json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.created",
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})

	if err == nil || !strings.Contains(err.Error(), "parse events.jsonl line 1") {
		t.Fatalf("AppendEvent error = %v", err)
	}
	b, readErr := os.ReadFile(EventsPath(workspaceDir))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(b) != "{not-json}\n" {
		t.Fatalf("corrupt journal should not be appended:\n%s", b)
	}
	if _, statErr := os.Stat(JournalLockPath(workspaceDir)); !os.IsNotExist(statErr) {
		t.Fatalf("journal lock should be removed after failed append: %v", statErr)
	}
}

func TestAppendEventRequiresAvailableJournalLock(t *testing.T) {
	workspaceDir := t.TempDir()
	if err := os.MkdirAll(StateDir(workspaceDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(JournalLockPath(workspaceDir), []byte("locked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         "workspace.created",
		Now:          fixedJournalTime("2026-06-17T10:00:00Z"),
	})

	if err == nil || !strings.Contains(err.Error(), "workspace journal lock is held") {
		t.Fatalf("AppendEvent error = %v", err)
	}
}

func readTestJournalEvents(t *testing.T, workspaceDir string) []JournalEvent {
	t.Helper()
	b, err := os.ReadFile(EventsPath(workspaceDir))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	events := make([]JournalEvent, 0, len(lines))
	for i, line := range lines {
		var event JournalEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		events = append(events, event)
	}
	return events
}

func fixedJournalTime(value string) func() time.Time {
	return func() time.Time {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			panic(err)
		}
		return parsed
	}
}

func mustJournalHash(t *testing.T, event JournalEvent) string {
	t.Helper()
	hash, err := hashJournalEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestJournalPathHelpers(t *testing.T) {
	workspaceDir := filepath.Join(t.TempDir(), "workspace")
	if StateDir(workspaceDir) != filepath.Join(workspaceDir, "state") {
		t.Fatalf("state dir = %q", StateDir(workspaceDir))
	}
	if EventsPath(workspaceDir) != filepath.Join(workspaceDir, "state", "events.jsonl") {
		t.Fatalf("events path = %q", EventsPath(workspaceDir))
	}
	if JournalLockPath(workspaceDir) != filepath.Join(workspaceDir, "state", "journal.lock") {
		t.Fatalf("lock path = %q", JournalLockPath(workspaceDir))
	}
}
