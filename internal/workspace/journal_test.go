package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestJournalAppendIsReadableAndRebuildable(t *testing.T) {
	t.Parallel()

	fixture := newJournalSafetyNetFixture(t)
	record, err := fixture.journal.AppendIfHead(
		fixture.append, workspace.JournalGenesisHash(),
	)
	if err != nil {
		t.Fatalf("append initial journal record: %v", err)
	}

	snapshot, err := fixture.journal.ReadSnapshot()
	if err != nil {
		t.Fatalf("read appended journal: %v", err)
	}
	if len(snapshot.Records()) != 1 || snapshot.Head() != record.EventHash() {
		t.Fatalf("readable append snapshot = %#v", snapshot.Records())
	}

	rebuilt, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatalf("rebuild runtime from appended record: %v", err)
	}
	if rebuilt.WorkspaceID() != fixture.workspaceID ||
		rebuilt.ActiveGeneration() != fixture.generation ||
		rebuilt.WorktreeRoot() != fixture.worktreeRoot {
		t.Fatalf("rebuilt runtime = %#v", rebuilt)
	}
	if _, err := workspace.VerifyWorkspaceRuntimeConformance(
		snapshot, fixture.generation,
	); err != nil {
		t.Fatalf("replay conformance: %v", err)
	}
}

func TestJournalMalformedRecordsAreRejectedOrRecovered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*testing.T) malformedJournalFixture
		wantErr func(error) bool
		recover bool
	}{
		{
			name:    "hash does not chain to predecessor",
			prepare: prepareUnchainedJournal,
			wantErr: func(err error) bool {
				return strings.Contains(err.Error(), "previous hash mismatch")
			},
		},
		{
			name:    "truncated final record",
			prepare: prepareTruncatedJournal,
			wantErr: func(err error) bool {
				var tail workspace.IncompleteJournalTailError
				return errors.As(err, &tail)
			},
			recover: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := test.prepare(t)
			journalPath := workspace.WorkspaceJournalPath(fixture.workspaceDir)
			beforeRead, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = workspace.ReadWorkspaceJournalSnapshot(fixture.workspaceDir)
			if err == nil || !test.wantErr(err) {
				t.Fatalf("malformed journal read error = %v", err)
			}
			afterRead, readErr := os.ReadFile(journalPath)
			if readErr != nil || !bytes.Equal(beforeRead, afterRead) {
				t.Fatalf("read changed malformed journal: err=%v", readErr)
			}
			if !test.recover {
				return
			}

			writer, err := workspace.OpenWorkspaceJournal(
				fixture.workspaceDir, workspace.JournalReadWrite,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			report, err := writer.RecoverIncompleteTail(
				fixture.workspaceID,
				mustTime(t, "2026-08-18T12:00:00Z"),
			)
			if err != nil {
				t.Fatalf("recover truncated journal: %v", err)
			}
			if !report.Recovered() ||
				report.DiscardOffset() != fixture.prefix.ByteLength() ||
				report.DiscardSize() != int64(len(fixture.partial)) ||
				report.TruncatedHead() != fixture.prefix.Head() {
				t.Fatalf("recovery report = %#v", report)
			}

			afterRecovery, err := writer.ReadSnapshot()
			if err != nil {
				t.Fatalf("read recovered journal: %v", err)
			}
			if len(afterRecovery.Records()) != len(fixture.prefix.Records())+1 ||
				afterRecovery.Head() != report.JournalHead() {
				t.Fatalf("recovered journal = %#v", afterRecovery.Records())
			}
			for index, record := range fixture.prefix.Records() {
				if afterRecovery.Records()[index].EventHash() != record.EventHash() {
					t.Fatalf("complete prefix record %d changed during recovery", index+1)
				}
			}
			again, err := writer.RecoverIncompleteTail(
				fixture.workspaceID,
				mustTime(t, "2026-08-18T12:01:00Z"),
			)
			if err != nil || again.Recovered() ||
				again.JournalHead() != afterRecovery.Head() {
				t.Fatalf("repeat recovery = %#v, %v", again, err)
			}
		})
	}
}

func TestJournalAppendIfHeadRejectsStaleHeadWithoutMutation(t *testing.T) {
	t.Parallel()

	fixture := newJournalSafetyNetFixture(t)
	if _, err := fixture.journal.AppendIfHead(
		fixture.append, workspace.JournalGenesisHash(),
	); err != nil {
		t.Fatalf("append initial record: %v", err)
	}
	before, err := fixture.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.journal.AppendIfHead(
		fixture.append, workspace.JournalGenesisHash(),
	)
	if err == nil || !strings.Contains(err.Error(), "stale journal head") {
		t.Fatalf("stale expected head error = %v", err)
	}
	after, err := fixture.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Head() != before.Head() ||
		len(after.Records()) != len(before.Records()) ||
		after.ByteLength() != before.ByteLength() {
		t.Fatalf("stale append changed journal: before=%#v after=%#v", before, after)
	}
}

func TestJournalWriterLockAllowsOneAppendWithoutPartialRecord(t *testing.T) {
	t.Parallel()

	fixture := newJournalSafetyNetFixture(t)
	if err := fixture.journal.Close(); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, 2)
	closeErrors := make(chan error, 2)
	var writers sync.WaitGroup
	writers.Add(2)
	for range 2 {
		go func() {
			defer writers.Done()
			<-start
			journal, err := workspace.OpenWorkspaceJournal(
				fixture.workspaceDir, workspace.JournalReadWrite,
			)
			if err != nil {
				results <- err
				return
			}
			_, err = journal.AppendIfHead(
				fixture.append, workspace.JournalGenesisHash(),
			)
			results <- err
			<-release
			if closeErr := journal.Close(); closeErr != nil {
				closeErrors <- closeErr
			}
		}()
	}

	released := false
	defer func() {
		if !released {
			close(release)
		}
		writers.Wait()
	}()
	close(start)

	successes, lockFailures := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, workspace.ErrWorkspaceJournalLocked):
			lockFailures++
		default:
			t.Fatalf("writer result = %v", err)
		}
	}
	if successes != 1 || lockFailures != 1 {
		t.Fatalf("writer outcomes: successes=%d lock failures=%d", successes, lockFailures)
	}
	close(release)
	released = true
	writers.Wait()
	select {
	case err := <-closeErrors:
		t.Fatalf("close competing writer: %v", err)
	default:
	}

	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(fixture.workspaceDir)
	if err != nil {
		t.Fatalf("read journal after competing writers: %v", err)
	}
	if len(snapshot.Records()) != 1 || snapshot.Head().IsZero() {
		t.Fatalf("writer competition journal = %#v", snapshot.Records())
	}
}

type journalSafetyNetFixture struct {
	workspaceDir string
	journal      *workspace.WorkspaceJournal
	append       workspace.JournalAppend
	workspaceID  workspace.ID
	generation   workspace.Digest
	worktreeRoot workspace.WorkspaceWorktreeRootBinding
}

func newJournalSafetyNetFixture(t *testing.T) journalSafetyNetFixture {
	t.Helper()

	workspaceDir := t.TempDir()
	worktreePath, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktreeRoot, err := workspace.NewWorkspaceWorktreeRootBinding(worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	workspaceID := workspace.MustID("journal-safety-net")
	generation := workspace.DigestBytes([]byte("journal-safety-net-generation"))
	definitionDigest := workspace.DigestBytes([]byte("journal-safety-net-definition"))
	event, err := workspace.NewWorkspaceInitializedJournalEvent(
		workspaceID, generation, definitionDigest, worktreeRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResource := workspace.WorkspaceJournalResource(workspaceID)
	generationResource := workspace.GenerationJournalResource(generation)
	workspaceRevision, err := workspace.NewJournalResourceRevision(
		workspaceResource, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	generationRevision, err := workspace.NewJournalResourceRevision(
		generationResource, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	appendRequest, err := workspace.NewJournalAppend(
		event,
		mustTime(t, "2026-08-18T11:00:00Z"),
		[]workspace.JournalResourceRevision{
			workspaceRevision,
			generationRevision,
		},
		[]workspace.JournalResource{workspaceResource, generationResource},
	)
	if err != nil {
		t.Fatal(err)
	}
	return journalSafetyNetFixture{
		workspaceDir: workspaceDir,
		journal:      journal,
		append:       appendRequest,
		workspaceID:  workspaceID,
		generation:   generation,
		worktreeRoot: worktreeRoot,
	}
}

type malformedJournalFixture struct {
	workspaceDir string
	workspaceID  workspace.ID
	prefix       workspace.JournalSnapshot
	partial      []byte
}

func prepareUnchainedJournal(t *testing.T) malformedJournalFixture {
	t.Helper()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	worktreeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstDir := t.TempDir()
	first, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		firstDir,
		definition,
		mustTime(t, "2026-08-18T11:10:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
	)
	if err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(
		t,
		definition.Workspace().RepositoryRoot(),
		"update-ref",
		"-d",
		definition.Workspace().FeatureRef(),
	)
	secondDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		secondDir,
		definition,
		mustTime(t, "2026-08-18T11:11:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
	); err != nil {
		t.Fatal(err)
	}
	firstLines := journalRecordLines(t, workspace.WorkspaceJournalPath(firstDir))
	secondLines := journalRecordLines(t, workspace.WorkspaceJournalPath(secondDir))
	if len(firstLines) < 1 || len(secondLines) < 2 {
		t.Fatalf("journal lines = first:%d second:%d", len(firstLines), len(secondLines))
	}
	corrupt := append([]byte(nil), firstLines[0]...)
	corrupt = append(corrupt, '\n')
	corrupt = append(corrupt, secondLines[1]...)
	corrupt = append(corrupt, '\n')
	if err := os.WriteFile(
		workspace.WorkspaceJournalPath(firstDir), corrupt, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	return malformedJournalFixture{
		workspaceDir: firstDir,
		workspaceID:  definition.Workspace().ID(),
		prefix:       first.Snapshot(),
	}
}

func prepareTruncatedJournal(t *testing.T) malformedJournalFixture {
	t.Helper()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	initialized, err := initializeWorkspaceV2(
		t,
		workspaceDir,
		definition,
		mustTime(t, "2026-08-18T11:20:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	lines := journalRecordLines(t, workspace.WorkspaceJournalPath(workspaceDir))
	last := lines[len(lines)-1]
	partial := append([]byte(nil), last[:len(last)/2]...)
	file, err := os.OpenFile(
		workspace.WorkspaceJournalPath(workspaceDir), os.O_WRONLY|os.O_APPEND, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(partial); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return malformedJournalFixture{
		workspaceDir: workspaceDir,
		workspaceID:  definition.Workspace().ID(),
		prefix:       initialized.Snapshot(),
		partial:      partial,
	}
}

func journalRecordLines(t *testing.T, path string) [][]byte {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(content, []byte("\n")) {
		t.Fatalf("journal %s has no final record delimiter", path)
	}
	lines := bytes.Split(bytes.TrimSuffix(content, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatalf("journal %s has no complete records", path)
	}
	return lines
}
