package workspace

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	StateDirName        = "state"
	EventsFileName      = "events.jsonl"
	JournalLockFileName = "journal.lock"
	zeroEventHash       = "0000000000000000000000000000000000000000000000000000000000000000"
)

type AppendEventOptions struct {
	WorkspaceDir string
	Type         string
	Payload      map[string]any
	ReadSet      map[string]int
	WriteSet     []string
	Now          func() time.Time
}

type JournalEvent struct {
	ID           string         `json:"id"`
	Timestamp    string         `json:"timestamp"`
	PreviousHash string         `json:"previous_hash"`
	EventHash    string         `json:"event_hash"`
	Type         string         `json:"type"`
	Payload      map[string]any `json:"payload,omitempty"`
	ReadSet      map[string]int `json:"read_set,omitempty"`
	WriteSet     []string       `json:"write_set,omitempty"`
}

type StaleResourceError struct {
	Resource string
	Expected int
	Observed int
}

func (e StaleResourceError) Error() string {
	return fmt.Sprintf("stale resource %s: expected revision %d, observed revision %d", e.Resource, e.Expected, e.Observed)
}

func WorkspaceResource(id string) string {
	return resourceKey("workspace", id)
}

func MergeUnitResource(id string) string {
	return resourceKey("merge_unit", id)
}

func LeaseResource(id string) string {
	return resourceKey("lease", id)
}

func BaseRefResource(id string) string {
	return resourceKey("base_ref", id)
}

func StateDir(workspaceDir string) string {
	return filepath.Join(workspaceDir, StateDirName)
}

func EventsPath(workspaceDir string) string {
	return filepath.Join(StateDir(workspaceDir), EventsFileName)
}

func JournalLockPath(workspaceDir string) string {
	return filepath.Join(StateDir(workspaceDir), JournalLockFileName)
}

func AppendEvent(opts AppendEventOptions) (JournalEvent, error) {
	if strings.TrimSpace(opts.WorkspaceDir) == "" {
		return JournalEvent{}, fmt.Errorf("workspace dir is required")
	}
	if strings.TrimSpace(opts.Type) == "" {
		return JournalEvent{}, fmt.Errorf("event type is required")
	}
	if err := os.MkdirAll(StateDir(opts.WorkspaceDir), 0o755); err != nil {
		return JournalEvent{}, err
	}
	release, err := acquireJournalLock(opts.WorkspaceDir)
	if err != nil {
		return JournalEvent{}, err
	}
	defer release()

	events, err := readJournalEvents(EventsPath(opts.WorkspaceDir))
	if err != nil {
		return JournalEvent{}, err
	}
	revisions, err := replayResourceRevisions(events)
	if err != nil {
		return JournalEvent{}, err
	}
	readSet, err := normalizeReadSet(opts.ReadSet)
	if err != nil {
		return JournalEvent{}, err
	}
	if err := validateReadSet(revisions, readSet); err != nil {
		return JournalEvent{}, err
	}
	writeSet, err := normalizeWriteSet(opts.WriteSet)
	if err != nil {
		return JournalEvent{}, err
	}
	previousHash := zeroEventHash
	if len(events) > 0 {
		previousHash = events[len(events)-1].EventHash
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	event := JournalEvent{
		ID:           fmt.Sprintf("evt-%012d", len(events)+1),
		Timestamp:    now().UTC().Format(time.RFC3339Nano),
		PreviousHash: previousHash,
		Type:         opts.Type,
		Payload:      clonePayload(opts.Payload),
		ReadSet:      readSet,
		WriteSet:     writeSet,
	}
	eventHash, err := hashJournalEvent(event)
	if err != nil {
		return JournalEvent{}, err
	}
	event.EventHash = eventHash
	if err := appendJournalEvent(EventsPath(opts.WorkspaceDir), event); err != nil {
		return JournalEvent{}, err
	}
	return event, nil
}

func ResourceRevisions(workspaceDir string) (map[string]int, error) {
	events, err := readJournalEvents(EventsPath(workspaceDir))
	if err != nil {
		return nil, err
	}
	return replayResourceRevisions(events)
}

func acquireJournalLock(workspaceDir string) (func(), error) {
	path := JournalLockPath(workspaceDir)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("workspace journal lock is held: %s", path)
		}
		return nil, err
	}
	_, writeErr := fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return nil, writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	return func() { _ = os.Remove(path) }, nil
}

func readJournalEvents(path string) ([]JournalEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	events := []JournalEvent{}
	reader := bufio.NewReader(f)
	lineNo := 0
	for {
		raw, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if errors.Is(readErr, io.EOF) && raw == "" {
			break
		}
		lineNo++
		event, err := parseJournalLine(path, lineNo, raw)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 && event.PreviousHash != zeroEventHash {
			return nil, fmt.Errorf("parse %s line %d: previous hash mismatch", filepath.Base(path), lineNo)
		}
		if len(events) > 0 && event.PreviousHash != events[len(events)-1].EventHash {
			return nil, fmt.Errorf("parse %s line %d: previous hash mismatch", filepath.Base(path), lineNo)
		}
		events = append(events, event)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return events, nil
}

func parseJournalLine(path string, lineNo int, raw string) (JournalEvent, error) {
	line := strings.TrimRight(raw, "\r\n")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return JournalEvent{}, fmt.Errorf("parse %s line %d: blank journal line", filepath.Base(path), lineNo)
	}
	if trimmed != line {
		return JournalEvent{}, fmt.Errorf("parse %s line %d: surrounding whitespace", filepath.Base(path), lineNo)
	}
	var event JournalEvent
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return JournalEvent{}, fmt.Errorf("parse %s line %d: %w", filepath.Base(path), lineNo, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return JournalEvent{}, fmt.Errorf("parse %s line %d: trailing data", filepath.Base(path), lineNo)
	}
	eventHash, err := hashJournalEvent(event)
	if err != nil {
		return JournalEvent{}, fmt.Errorf("parse %s line %d: %w", filepath.Base(path), lineNo, err)
	}
	if event.EventHash != eventHash {
		return JournalEvent{}, fmt.Errorf("parse %s line %d: event hash mismatch", filepath.Base(path), lineNo)
	}
	return event, nil
}

func appendJournalEvent(path string, event JournalEvent) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func hashJournalEvent(event JournalEvent) (string, error) {
	value := struct {
		ID           string         `json:"id"`
		Timestamp    string         `json:"timestamp"`
		PreviousHash string         `json:"previous_hash"`
		Type         string         `json:"type"`
		Payload      map[string]any `json:"payload,omitempty"`
		ReadSet      map[string]int `json:"read_set,omitempty"`
		WriteSet     []string       `json:"write_set,omitempty"`
	}{
		ID:           event.ID,
		Timestamp:    event.Timestamp,
		PreviousHash: event.PreviousHash,
		Type:         event.Type,
		Payload:      event.Payload,
		ReadSet:      event.ReadSet,
		WriteSet:     event.WriteSet,
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func replayResourceRevisions(events []JournalEvent) (map[string]int, error) {
	revisions := map[string]int{}
	for _, event := range events {
		readSet, err := normalizeReadSet(event.ReadSet)
		if err != nil {
			return nil, fmt.Errorf("event %s read_set: %w", event.ID, err)
		}
		if err := validateReadSet(revisions, readSet); err != nil {
			return nil, fmt.Errorf("event %s: %w", event.ID, err)
		}
		writeSet, err := normalizeWriteSet(event.WriteSet)
		if err != nil {
			return nil, fmt.Errorf("event %s write_set: %w", event.ID, err)
		}
		if len(writeSet) != len(event.WriteSet) {
			return nil, fmt.Errorf("event %s write_set contains duplicate resources", event.ID)
		}
		for _, resource := range writeSet {
			revisions[resource]++
		}
	}
	return revisions, nil
}

func validateReadSet(revisions map[string]int, readSet map[string]int) error {
	for resource, expected := range readSet {
		observed := revisions[resource]
		if expected != observed {
			return StaleResourceError{Resource: resource, Expected: expected, Observed: observed}
		}
	}
	return nil
}

func normalizeReadSet(readSet map[string]int) (map[string]int, error) {
	if len(readSet) == 0 {
		return nil, nil
	}
	normalized := make(map[string]int, len(readSet))
	for resource, revision := range readSet {
		if err := validateResourceKey(resource); err != nil {
			return nil, err
		}
		if revision < 0 {
			return nil, fmt.Errorf("resource %s revision must be non-negative", resource)
		}
		normalized[resource] = revision
	}
	return normalized, nil
}

func normalizeWriteSet(writeSet []string) ([]string, error) {
	if len(writeSet) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	normalized := make([]string, 0, len(writeSet))
	for _, resource := range writeSet {
		if err := validateResourceKey(resource); err != nil {
			return nil, err
		}
		if !seen[resource] {
			seen[resource] = true
			normalized = append(normalized, resource)
		}
	}
	sort.Strings(normalized)
	return normalized, nil
}

func validateResourceKey(value string) error {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("resource %q must use kind:id", value)
	}
	switch parts[0] {
	case "workspace", "merge_unit", "lease", "base_ref", "contract", "contract_binding", "approval":
		return nil
	default:
		return fmt.Errorf("unsupported resource kind %q", parts[0])
	}
}

func resourceKey(kind string, id string) string {
	return kind + ":" + id
}

func clonePayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	clone := make(map[string]any, len(payload))
	for key, value := range payload {
		clone[key] = value
	}
	return clone
}
