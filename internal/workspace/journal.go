package workspace

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Now          func() time.Time
}

type JournalEvent struct {
	ID           string         `json:"id"`
	Timestamp    string         `json:"timestamp"`
	PreviousHash string         `json:"previous_hash"`
	EventHash    string         `json:"event_hash"`
	Type         string         `json:"type"`
	Payload      map[string]any `json:"payload,omitempty"`
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
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event JournalEvent
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", filepath.Base(path), lineNo, err)
		}
		eventHash, err := hashJournalEvent(event)
		if err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", filepath.Base(path), lineNo, err)
		}
		if event.EventHash != eventHash {
			return nil, fmt.Errorf("parse %s line %d: event hash mismatch", filepath.Base(path), lineNo)
		}
		if len(events) == 0 && event.PreviousHash != zeroEventHash {
			return nil, fmt.Errorf("parse %s line %d: previous hash mismatch", filepath.Base(path), lineNo)
		}
		if len(events) > 0 && event.PreviousHash != events[len(events)-1].EventHash {
			return nil, fmt.Errorf("parse %s line %d: previous hash mismatch", filepath.Base(path), lineNo)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
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
	}{
		ID:           event.ID,
		Timestamp:    event.Timestamp,
		PreviousHash: event.PreviousHash,
		Type:         event.Type,
		Payload:      event.Payload,
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
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
