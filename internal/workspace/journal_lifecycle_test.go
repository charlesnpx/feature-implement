package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestJournalTailRecoveredEventsRejectGeneralAppend(t *testing.T) {
	t.Parallel()

	event, err := workspace.NewJournalTailRecoveredEvent(
		workspace.MustID("example-workspace"),
		workspace.DigestBytes([]byte("tail-recovery-generation")),
		0,
		1,
		workspace.DigestBytes([]byte("discarded-tail")),
		workspace.JournalGenesisHash(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewJournalAppend(
		event,
		mustTime(t, "2026-07-21T00:58:00Z"),
	); err == nil || !strings.Contains(
		err.Error(), "explicit recovery workflow",
	) {
		t.Fatalf("direct tail-recovery append error = %v", err)
	}
}

func TestInitializeWorkspaceV2AdmissionBeforeMutation(t *testing.T) {
	t.Parallel()

	t.Run("derives worktree root", func(t *testing.T) {
		definition := mustDefinition(t, newDefinitionFixture(t).sources)
		workspaceDir := t.TempDir()
		result, err := workspace.InitializeWorkspaceV2WithOptions(
			context.Background(),
			workspaceDir,
			definition,
			mustTime(t, "2026-07-21T00:59:00Z"),
			workspace.WorkspaceInitializationOptions{},
		)
		if err != nil {
			t.Fatalf("derived worktree root initialization: %v", err)
		}
		if result.Runtime().WorktreeRoot() != "" {
			t.Fatalf("runtime retained a worktree-root binding: %#v", result.Runtime())
		}
	})

	t.Run("rejects runtime target overlap", func(t *testing.T) {
		fixture := newDefinitionFixture(t)
		definition := mustDefinition(t, fixture.sources)
		target := definition.Workspace().RepositoryRoot()

		if _, err := initializeWorkspaceV2(
			t,
			target,
			definition,
			mustTime(t, "2026-07-21T01:00:00Z"),
		); err == nil || !strings.Contains(err.Error(), "overlaps the target root") {
			t.Fatalf("runtime/target overlap error = %v", err)
		}
		for _, candidate := range []string{
			filepath.Join(target, workspace.RuntimeFormatFileName),
			filepath.Join(target, workspace.RuntimeInitializationLockName),
			filepath.Join(target, workspace.WorkspaceStateDirectoryName),
		} {
			if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("overlap admission mutated target path %s: %v", candidate, err)
			}
		}
	})
}

func TestInitializationResumesAfterBootstrapTailRecovery(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Store(definition)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	faulty, err := workspace.OpenWorkspaceJournalWithOptions(
		workspaceDir,
		workspace.JournalReadWrite,
		workspace.JournalOptions{
			FaultInjector: func(point workspace.JournalFaultPoint) error {
				if point == workspace.JournalFaultAfterAppendPrefix {
					return errors.New("simulated initialization crash")
				}
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	event, err := workspace.NewWorkspaceInitializedJournalEventWithTarget(
		definition.Workspace().ID(),
		definition.Generation(),
		stored.DefinitionDigest(),
		workspace.LocalTargetBinding{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := workspace.NewJournalAppend(
		event,
		mustTime(t, "2026-07-21T01:00:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := faulty.Append(request); err == nil || !strings.Contains(
		err.Error(), string(workspace.JournalFaultAfterAppendPrefix),
	) {
		t.Fatalf("partial initialization append error = %v", err)
	}
	if err := faulty.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir); err == nil {
		t.Fatal("partial initialization record was not diagnosed")
	}

	recoveryJournal, err := workspace.OpenWorkspaceJournal(
		workspaceDir,
		workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	report, err := recoveryJournal.RecoverIncompleteTail(
		definition.Workspace().ID(),
		mustTime(t, "2026-07-21T01:01:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Recovered() || report.DiscardOffset() != 0 ||
		report.TruncatedHead() != workspace.JournalGenesisHash() {
		t.Fatalf("bootstrap recovery report = %#v", report)
	}
	if err := recoveryJournal.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := initializeWorkspaceV2(
		t,
		workspaceDir,
		definition,
		mustTime(t, "2026-07-21T01:02:00Z"),
	)
	if err != nil {
		t.Fatalf("resume initialization: %v", err)
	}
	records := result.Snapshot().Records()
	if len(records) != 2 ||
		records[0].EventType() != workspace.JournalEventTailRecovered ||
		records[1].EventType() != workspace.JournalEventWorkspaceInitialized {
		t.Fatalf("resumed initialization journal = %#v", records)
	}
	recovery, ok := records[0].Event().(workspace.JournalTailRecoveredEvent)
	if !ok || recovery.Generation() != definition.Generation() ||
		recovery.ResultingHead() != workspace.JournalGenesisHash() {
		t.Fatalf("bootstrap recovery event = %#v", records[0].Event())
	}
	if result.Runtime().WorkspaceID() != definition.Workspace().ID() ||
		result.Runtime().ActiveGeneration() != definition.Generation() ||
		len(result.Runtime().Recoveries()) != 1 {
		t.Fatalf("resumed initialization runtime = %#v", result.Runtime())
	}
}

func TestWorkspaceJournalReadOnlyLockDoesNotRequireWritePermission(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := initializeWorkspaceV2(
		t,
		workspaceDir,
		definition,
		mustTime(t, "2026-07-21T01:00:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	lockPath := workspace.WorkspaceJournalLockPath(workspaceDir)
	if err := os.Chmod(lockPath, 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(lockPath, 0o644) }()

	reader, err := workspace.OpenWorkspaceJournal(
		workspaceDir,
		workspace.JournalReadOnly,
	)
	if err != nil {
		t.Fatalf("open read-only journal with read-only lock file: %v", err)
	}
	snapshot, readErr := reader.ReadSnapshot()
	closeErr := reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	assertLocalTargetInitializationJournal(t, snapshot)
}

func TestWorkspaceJournalWriterContentionRejectsStaleHead(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Store(definition)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	gateDir := t.TempDir()
	newCommand := func(worker string, output *bytes.Buffer) *exec.Cmd {
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestWorkspaceJournalWriterContentionSubprocess$",
		)
		command.Env = append(
			os.Environ(),
			"WORKSPACE_JOURNAL_WRITER_HELPER="+workspaceDir,
			"WORKSPACE_JOURNAL_WRITER_GENERATION="+definition.Generation().String(),
			"WORKSPACE_JOURNAL_WRITER_DEFINITION="+stored.DefinitionDigest().String(),
			"WORKSPACE_JOURNAL_WRITER_GATE="+gateDir,
			"WORKSPACE_JOURNAL_WRITER_ID="+worker,
		)
		command.Stdout = output
		command.Stderr = output
		return command
	}
	var firstOutput, secondOutput bytes.Buffer
	first := newCommand("first", &firstOutput)
	second := newCommand("second", &secondOutput)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	waitForJournalWriterGate(
		t,
		filepath.Join(gateDir, "ready-first"),
		filepath.Join(gateDir, "ready-second"),
	)
	writeJournalWriterGate(t, filepath.Join(gateDir, "open"))
	waitForAnyJournalWriterGate(
		t,
		filepath.Join(gateDir, "opened-first"),
		filepath.Join(gateDir, "opened-second"),
	)

	secondOpenDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(secondOpenDeadline) {
		if journalWriterGateExists(
			filepath.Join(gateDir, "opened-first"),
		) && journalWriterGateExists(
			filepath.Join(gateDir, "opened-second"),
		) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeJournalWriterGate(t, filepath.Join(gateDir, "append"))

	if err := first.Wait(); err != nil {
		t.Fatalf("first writer subprocess: %v\n%s", err, firstOutput.String())
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second writer subprocess: %v\n%s", err, secondOutput.String())
	}
	combined := firstOutput.String() + secondOutput.String()
	if strings.Count(combined, "append-won") != 1 ||
		strings.Count(combined, "append-stale") != 1 {
		t.Fatalf(
			"writer contention outputs:\nfirst=%s\nsecond=%s",
			firstOutput.String(),
			secondOutput.String(),
		)
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir)
	if err != nil {
		t.Fatalf("read writer-contention journal: %v", err)
	}
	if len(snapshot.Records()) != 1 ||
		snapshot.Records()[0].EventType() != workspace.JournalEventWorkspaceInitialized ||
		snapshot.Head().IsZero() || snapshot.ByteLength() == 0 {
		t.Fatalf("writer contention journal = %#v", snapshot.Records())
	}
}

func TestWorkspaceJournalWriterContentionSubprocess(t *testing.T) {
	workspaceDir := os.Getenv("WORKSPACE_JOURNAL_WRITER_HELPER")
	if workspaceDir == "" {
		t.Parallel()
		t.Skip("writer-contention subprocess helper")
	}
	worker := os.Getenv("WORKSPACE_JOURNAL_WRITER_ID")
	gateDir := os.Getenv("WORKSPACE_JOURNAL_WRITER_GATE")
	if worker == "" || gateDir == "" {
		t.Fatal("writer-contention helper lacks its gate configuration")
	}
	writeJournalWriterGate(t, filepath.Join(gateDir, "ready-"+worker))
	waitForJournalWriterGate(t, filepath.Join(gateDir, "open"))

	var journal *workspace.WorkspaceJournal
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		journal, err = workspace.OpenWorkspaceJournal(
			workspaceDir,
			workspace.JournalReadWrite,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, workspace.ErrWorkspaceJournalLocked) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if journal == nil {
		t.Fatalf("timed out acquiring journal writer lock: %v", err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close writer journal: %v", err)
		}
	}()
	writeJournalWriterGate(t, filepath.Join(gateDir, "opened-"+worker))
	waitForJournalWriterGate(t, filepath.Join(gateDir, "append"))

	generation, err := workspace.ParseDigest(
		os.Getenv("WORKSPACE_JOURNAL_WRITER_GENERATION"),
	)
	if err != nil {
		t.Fatal(err)
	}
	definitionDigest, err := workspace.ParseDigest(
		os.Getenv("WORKSPACE_JOURNAL_WRITER_DEFINITION"),
	)
	if err != nil {
		t.Fatal(err)
	}
	event, err := workspace.NewWorkspaceInitializedJournalEventWithTarget(
		workspace.MustID("example-workspace"),
		generation,
		definitionDigest,
		workspace.LocalTargetBinding{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := workspace.NewJournalAppend(
		event,
		mustTime(t, "2026-07-21T01:01:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AppendIfHead(request, workspace.JournalGenesisHash()); err == nil {
		fmt.Print("append-won")
		return
	} else if strings.Contains(err.Error(), "stale journal head") {
		fmt.Print("append-stale")
		return
	} else {
		t.Fatal(err)
	}
}

func writeJournalWriterGate(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForJournalWriterGate(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ready := true
		for _, path := range paths {
			if journalWriterGateExists(path) {
				continue
			}
			ready = false
			break
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for journal writer gates %v", paths)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForAnyJournalWriterGate(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, path := range paths {
			if journalWriterGateExists(path) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for any journal writer gate %v", paths)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func journalWriterGateExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
