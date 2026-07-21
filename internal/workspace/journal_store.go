package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
)

const (
	WorkspaceStateDirectoryName = "state"
	WorkspaceJournalFileName    = "journal.v2.jsonl"
	WorkspaceJournalLockName    = "journal.v2.lock"
	MaxJournalRecordBytes       = 1 << 20
	MaxJournalBytes             = 64 << 20
)

var ErrWorkspaceJournalLocked = errors.New("workspace journal lock is held")

type JournalMode uint8

const (
	JournalReadOnly JournalMode = iota + 1
	JournalReadWrite
)

type JournalFaultPoint string

const (
	JournalFaultBeforeAppend        JournalFaultPoint = "before_append"
	JournalFaultAfterAppendPrefix   JournalFaultPoint = "after_append_prefix"
	JournalFaultAfterAppend         JournalFaultPoint = "after_append"
	JournalFaultBeforeFileSync      JournalFaultPoint = "before_file_sync"
	JournalFaultAfterFileSync       JournalFaultPoint = "after_file_sync"
	JournalFaultBeforeDirectorySync JournalFaultPoint = "before_directory_sync"
	JournalFaultAfterDirectorySync  JournalFaultPoint = "after_directory_sync"
)

type JournalFaultInjector func(JournalFaultPoint) error

type JournalOptions struct {
	FaultInjector JournalFaultInjector
}

type StaleJournalResourceError struct {
	Resource JournalResource
	Expected uint64
	Observed uint64
}

type JournalAppendAmbiguousError struct {
	EventHash Digest
	Cause     error
}

func (err JournalAppendAmbiguousError) Error() string {
	return fmt.Sprintf("journal append %s has an ambiguous durability outcome: %v", err.EventHash, err.Cause)
}

func (err JournalAppendAmbiguousError) Unwrap() error { return err.Cause }

func (err StaleJournalResourceError) Error() string {
	return fmt.Sprintf(
		"stale journal resource %s/%s: expected revision %d, observed %d",
		err.Resource.kind, err.Resource.identity, err.Expected, err.Observed,
	)
}

type IncompleteJournalTailError struct {
	offset        int64
	size          int64
	digest        Digest
	resultingHead Digest
}

func (err IncompleteJournalTailError) Error() string {
	return fmt.Sprintf("journal has an incomplete EOF record at offset %d (%d bytes, %s)", err.offset, err.size, err.digest)
}

func (err IncompleteJournalTailError) Offset() int64         { return err.offset }
func (err IncompleteJournalTailError) Size() int64           { return err.size }
func (err IncompleteJournalTailError) Digest() Digest        { return err.digest }
func (err IncompleteJournalTailError) ResultingHead() Digest { return err.resultingHead }

type JournalSnapshot struct {
	records    []JournalRecord
	head       Digest
	byteLength int64
	revisions  []JournalResourceRevision
}

func (snapshot JournalSnapshot) Records() []JournalRecord {
	return append([]JournalRecord(nil), snapshot.records...)
}
func (snapshot JournalSnapshot) Head() Digest      { return snapshot.head }
func (snapshot JournalSnapshot) ByteLength() int64 { return snapshot.byteLength }
func (snapshot JournalSnapshot) ResourceRevisions() []JournalResourceRevision {
	return append([]JournalResourceRevision(nil), snapshot.revisions...)
}
func (snapshot JournalSnapshot) Revision(resource JournalResource) uint64 {
	for _, revision := range snapshot.revisions {
		if revision.resource == resource {
			return revision.revision
		}
	}
	return 0
}

type WorkspaceJournal struct {
	workspaceDir string
	stateDir     string
	journalPath  string
	lockPath     string
	lockFile     *os.File
	mode         JournalMode
	fault        JournalFaultInjector
	mu           sync.Mutex
	closed       bool
}

func WorkspaceStateDirectory(workspaceDir string) string {
	return filepath.Join(workspaceDir, WorkspaceStateDirectoryName)
}

func WorkspaceJournalPath(workspaceDir string) string {
	return filepath.Join(WorkspaceStateDirectory(workspaceDir), WorkspaceJournalFileName)
}

func WorkspaceJournalLockPath(workspaceDir string) string {
	return filepath.Join(WorkspaceStateDirectory(workspaceDir), WorkspaceJournalLockName)
}

func OpenWorkspaceJournal(workspaceDir string, mode JournalMode) (*WorkspaceJournal, error) {
	return OpenWorkspaceJournalWithOptions(workspaceDir, mode, JournalOptions{})
}

func OpenWorkspaceJournalWithOptions(workspaceDir string, mode JournalMode, options JournalOptions) (*WorkspaceJournal, error) {
	workspaceDir = filepath.Clean(workspaceDir)
	if !filepath.IsAbs(workspaceDir) {
		return nil, fmt.Errorf("workspace journal requires an absolute workspace directory")
	}
	if mode != JournalReadOnly && mode != JournalReadWrite {
		return nil, fmt.Errorf("unsupported workspace journal mode %d", mode)
	}
	stateDir := WorkspaceStateDirectory(workspaceDir)
	if mode == JournalReadWrite {
		if err := ensureSynchronizedDirectory(stateDir); err != nil {
			return nil, err
		}
	} else if info, err := os.Stat(stateDir); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return nil, fmt.Errorf("open workspace state directory: %w", err)
	}
	lockPath := WorkspaceJournalLockPath(workspaceDir)
	lockExisted := pathExists(lockPath)
	flags := os.O_RDONLY
	if mode == JournalReadWrite {
		flags = os.O_RDWR | os.O_CREATE
	}
	lockFile, err := os.OpenFile(lockPath, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open workspace journal lock: %w", err)
	}
	operation := syscall.LOCK_SH | syscall.LOCK_NB
	if mode == JournalReadWrite {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err := syscall.Flock(int(lockFile.Fd()), operation); err != nil {
		_ = lockFile.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrWorkspaceJournalLocked, lockPath)
		}
		return nil, fmt.Errorf("acquire workspace journal lock: %w", err)
	}
	if !lockExisted && mode == JournalReadWrite {
		if err := syncDirectory(stateDir); err != nil {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
			return nil, err
		}
	}
	if mode == JournalReadWrite && pathExists(WorkspaceJournalPath(workspaceDir)) {
		if err := syncFileAndDirectory(WorkspaceJournalPath(workspaceDir), stateDir); err != nil {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
			return nil, fmt.Errorf("synchronize existing workspace journal: %w", err)
		}
	}
	return &WorkspaceJournal{
		workspaceDir: workspaceDir, stateDir: stateDir,
		journalPath: WorkspaceJournalPath(workspaceDir), lockPath: lockPath,
		lockFile: lockFile, mode: mode, fault: options.FaultInjector,
	}, nil
}

func (journal *WorkspaceJournal) Close() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.closed {
		return nil
	}
	journal.closed = true
	unlockErr := syscall.Flock(int(journal.lockFile.Fd()), syscall.LOCK_UN)
	closeErr := journal.lockFile.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock workspace journal: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close workspace journal lock: %w", closeErr)
	}
	return nil
}

func (journal *WorkspaceJournal) ReadSnapshot() (JournalSnapshot, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.requireOpen(); err != nil {
		return JournalSnapshot{}, err
	}
	recoveryPending, err := journalRecoveryPending(journal.workspaceDir)
	if err != nil {
		return JournalSnapshot{}, err
	}
	if recoveryPending {
		return JournalSnapshot{}, fmt.Errorf("journal recovery is pending; ordinary readers cannot repair state")
	}
	if journal.mode == JournalReadWrite && pathExists(journal.journalPath) {
		if err := syncFileAndDirectory(journal.journalPath, journal.stateDir); err != nil {
			return JournalSnapshot{}, fmt.Errorf("synchronize workspace journal before writer reconciliation: %w", err)
		}
	}
	snapshot, tail, err := journal.readSnapshotAllowTail()
	if err != nil {
		return JournalSnapshot{}, err
	}
	if tail != nil {
		return JournalSnapshot{}, *tail
	}
	return snapshot, nil
}

func ReadWorkspaceJournalSnapshot(workspaceDir string) (JournalSnapshot, error) {
	journal, err := OpenWorkspaceJournal(workspaceDir, JournalReadOnly)
	if err != nil {
		return JournalSnapshot{}, err
	}
	defer journal.Close()
	return journal.ReadSnapshot()
}

func (journal *WorkspaceJournal) Append(request JournalAppend) (JournalRecord, error) {
	return journal.appendWithHead(request, Digest{}, false)
}

func (journal *WorkspaceJournal) AppendIfHead(request JournalAppend, expectedHead Digest) (JournalRecord, error) {
	if expectedHead.IsZero() {
		return JournalRecord{}, fmt.Errorf("journal head CAS requires an expected head")
	}
	return journal.appendWithHead(request, expectedHead, true)
}

func (journal *WorkspaceJournal) appendWithHead(request JournalAppend, expectedHead Digest, enforceHead bool) (JournalRecord, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.requireWriter(); err != nil {
		return JournalRecord{}, err
	}
	recoveryPending, err := journalRecoveryPending(journal.workspaceDir)
	if err != nil {
		return JournalRecord{}, err
	}
	if recoveryPending {
		return JournalRecord{}, fmt.Errorf("journal recovery is pending")
	}
	snapshot, tail, err := journal.readSnapshotAllowTail()
	if err != nil {
		return JournalRecord{}, err
	}
	if tail != nil {
		return JournalRecord{}, *tail
	}
	if enforceHead && snapshot.head != expectedHead {
		return JournalRecord{}, fmt.Errorf("stale journal head: expected %s, observed %s", expectedHead, snapshot.head)
	}
	return journal.appendToSnapshot(snapshot, request)
}

func (journal *WorkspaceJournal) appendToSnapshot(snapshot JournalSnapshot, request JournalAppend) (JournalRecord, error) {
	if request.event == nil {
		return JournalRecord{}, fmt.Errorf("journal append requires a typed event")
	}
	if err := validateJournalCAS(snapshot, request.readSet); err != nil {
		return JournalRecord{}, err
	}
	record, err := buildJournalRecord(snapshot, request)
	if err != nil {
		return JournalRecord{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("validate current journal runtime: %w", err)
	}
	if _, err := reduceWorkspaceRuntime(runtime, record); err != nil {
		return JournalRecord{}, fmt.Errorf("validate journal event %s: %w", record.event.eventType(), err)
	}
	encoded, err := marshalJournalRecord(record)
	if err != nil {
		return JournalRecord{}, err
	}
	if len(encoded) == 0 || len(encoded) > MaxJournalRecordBytes {
		return JournalRecord{}, fmt.Errorf("journal record is empty or exceeds %d bytes", MaxJournalRecordBytes)
	}
	encoded = append(encoded, '\n')
	if snapshot.byteLength+int64(len(encoded)) > MaxJournalBytes {
		return JournalRecord{}, fmt.Errorf("journal exceeds %d bytes", MaxJournalBytes)
	}
	file, err := os.OpenFile(journal.journalPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return JournalRecord{}, err
	}
	closeWith := func(operationErr error) error {
		closeErr := file.Close()
		if operationErr != nil {
			return operationErr
		}
		return closeErr
	}
	if err := journal.inject(JournalFaultBeforeAppend); err != nil {
		return JournalRecord{}, closeWith(err)
	}
	prefix := len(encoded) / 2
	if prefix == 0 {
		prefix = 1
	}
	if err := writeAll(file, encoded[:prefix]); err != nil {
		return JournalRecord{}, closeWith(err)
	}
	if err := journal.inject(JournalFaultAfterAppendPrefix); err != nil {
		return JournalRecord{}, closeWith(err)
	}
	if err := writeAll(file, encoded[prefix:]); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: closeWith(err)}
	}
	if err := journal.inject(JournalFaultAfterAppend); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: closeWith(err)}
	}
	if err := journal.inject(JournalFaultBeforeFileSync); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: closeWith(err)}
	}
	if err := file.Sync(); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: closeWith(err)}
	}
	if err := journal.inject(JournalFaultAfterFileSync); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: closeWith(err)}
	}
	if err := closeWith(nil); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: err}
	}
	if err := journal.inject(JournalFaultBeforeDirectorySync); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: err}
	}
	if err := syncDirectory(journal.stateDir); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: err}
	}
	if err := journal.inject(JournalFaultAfterDirectorySync); err != nil {
		return JournalRecord{}, JournalAppendAmbiguousError{EventHash: record.eventHash, Cause: err}
	}
	return record, nil
}

func (journal *WorkspaceJournal) readSnapshotAllowTail() (JournalSnapshot, *IncompleteJournalTailError, error) {
	content, err := readBoundedFile(journal.journalPath, MaxJournalBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyJournalSnapshot(), nil, nil
		}
		return JournalSnapshot{}, nil, err
	}
	return parseJournalBytes(content)
}

func parseJournalBytes(content []byte) (JournalSnapshot, *IncompleteJournalTailError, error) {
	if len(content) > MaxJournalBytes {
		return JournalSnapshot{}, nil, fmt.Errorf("journal exceeds %d bytes", MaxJournalBytes)
	}
	snapshot := emptyJournalSnapshot()
	runtime := WorkspaceRuntimeProjection{}
	lineStart := 0
	for lineStart < len(content) {
		relativeEnd := bytes.IndexByte(content[lineStart:], '\n')
		if relativeEnd < 0 {
			tail := content[lineStart:]
			if len(tail) > MaxJournalRecordBytes {
				return JournalSnapshot{}, nil, fmt.Errorf("incomplete journal record exceeds %d bytes", MaxJournalRecordBytes)
			}
			diagnosis := &IncompleteJournalTailError{
				offset: int64(lineStart), size: int64(len(tail)), digest: DigestBytes(tail),
				resultingHead: snapshot.head,
			}
			return snapshot, diagnosis, nil
		}
		lineEnd := lineStart + relativeEnd
		line := content[lineStart:lineEnd]
		if len(line) == 0 {
			return JournalSnapshot{}, nil, fmt.Errorf("journal record %d is blank", len(snapshot.records)+1)
		}
		if len(line) > MaxJournalRecordBytes {
			return JournalSnapshot{}, nil, fmt.Errorf("journal record %d exceeds %d bytes", len(snapshot.records)+1, MaxJournalRecordBytes)
		}
		record, err := parseJournalRecord(line)
		if err != nil {
			return JournalSnapshot{}, nil, fmt.Errorf("parse journal record %d: %w", len(snapshot.records)+1, err)
		}
		if record.sequence != uint64(len(snapshot.records))+1 {
			return JournalSnapshot{}, nil, fmt.Errorf("journal sequence %d is not contiguous", record.sequence)
		}
		if record.previousHash != snapshot.head {
			return JournalSnapshot{}, nil, fmt.Errorf("journal record %d previous hash mismatch", record.sequence)
		}
		if err := validateJournalCAS(snapshot, record.readSet); err != nil {
			return JournalSnapshot{}, nil, fmt.Errorf("journal record %d: %w", record.sequence, err)
		}
		nextRuntime, err := reduceWorkspaceRuntime(runtime, record)
		if err != nil {
			return JournalSnapshot{}, nil, fmt.Errorf("journal record %d violates runtime invariants: %w", record.sequence, err)
		}
		runtime = nextRuntime
		snapshot.records = append(snapshot.records, record)
		snapshot.head = record.eventHash
		snapshot.revisions = applyJournalWrites(snapshot.revisions, record.writeSet)
		lineStart = lineEnd + 1
		snapshot.byteLength = int64(lineStart)
	}
	return snapshot, nil, nil
}

func emptyJournalSnapshot() JournalSnapshot {
	return JournalSnapshot{head: JournalGenesisHash(), revisions: []JournalResourceRevision{}}
}

func validateJournalCAS(snapshot JournalSnapshot, reads []JournalResourceRevision) error {
	for _, expected := range reads {
		observed := snapshot.Revision(expected.resource)
		if observed != expected.revision {
			return StaleJournalResourceError{Resource: expected.resource, Expected: expected.revision, Observed: observed}
		}
	}
	return nil
}

func applyJournalWrites(revisions []JournalResourceRevision, writes []JournalResource) []JournalResourceRevision {
	byKey := make(map[string]JournalResourceRevision, len(revisions)+len(writes))
	for _, revision := range revisions {
		byKey[revision.resource.key()] = revision
	}
	for _, resource := range writes {
		current := byKey[resource.key()]
		current.resource = resource
		current.revision++
		byKey[resource.key()] = current
	}
	result := make([]JournalResourceRevision, 0, len(byKey))
	for _, revision := range byKey {
		result = append(result, revision)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].resource.key() < result[j].resource.key() })
	return result
}

func (journal *WorkspaceJournal) requireOpen() error {
	if journal == nil || journal.closed || journal.lockFile == nil {
		return fmt.Errorf("workspace journal is closed")
	}
	return nil
}

func (journal *WorkspaceJournal) requireWriter() error {
	if err := journal.requireOpen(); err != nil {
		return err
	}
	if journal.mode != JournalReadWrite {
		return fmt.Errorf("workspace journal is read-only")
	}
	return nil
}

func (journal *WorkspaceJournal) inject(point JournalFaultPoint) error {
	if journal.fault == nil {
		return nil
	}
	if err := journal.fault(point); err != nil {
		return fmt.Errorf("journal fault at %s: %w", point, err)
	}
	return nil
}

func ensureSynchronizedDirectory(path string) error {
	path = filepath.Clean(path)
	missing := make([]string, 0, 2)
	for cursor := path; ; cursor = filepath.Dir(cursor) {
		info, err := os.Stat(cursor)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", cursor)
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return fmt.Errorf("cannot find an existing parent directory for %s", path)
		}
		missing = append(missing, cursor)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		created := missing[index]
		if err := os.Mkdir(created, 0o755); err != nil {
			if !os.IsExist(err) {
				return err
			}
			info, statErr := os.Stat(created)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", created)
			}
		}
		if err := syncDirectory(filepath.Dir(created)); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return nil
}

func readBoundedFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("file %s exceeds %d bytes", filepath.Base(path), maximum)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("file %s exceeds %d bytes", filepath.Base(path), maximum)
	}
	return content, nil
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
