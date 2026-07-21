package workspace_test

import (
	"bytes"
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

func TestInitializeWorkspaceV2CreatesDurableJournalGenerationAndProjection(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := filepath.Join(t.TempDir(), "workspace")
	initializedAt := mustTime(t, "2026-07-21T01:00:00Z")

	result, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, initializedAt)
	if err != nil {
		t.Fatalf("InitializeWorkspaceV2: %v", err)
	}
	if result.StoredGeneration().Generation() != definition.Generation() || result.StoredGeneration().DefinitionDigest().IsZero() {
		t.Fatalf("stored generation = %#v", result.StoredGeneration())
	}
	snapshot := result.Snapshot()
	if len(snapshot.Records()) != 1 || snapshot.Records()[0].EventType() != workspace.JournalEventWorkspaceInitialized {
		t.Fatalf("initial journal = %#v", snapshot.Records())
	}
	if snapshot.Head() != snapshot.Records()[0].EventHash() || snapshot.Head().IsZero() {
		t.Fatalf("initial journal head = %s", snapshot.Head())
	}
	runtime := result.Runtime()
	if runtime.WorkspaceID().String() != "example-workspace" || runtime.ActiveGeneration() != definition.Generation() {
		t.Fatalf("initial runtime = %#v", runtime)
	}
	if result.ProjectionDigest().IsZero() {
		t.Fatal("projection conformance digest is required")
	}
	for _, path := range []string{
		workspace.WorkspaceJournalPath(workspaceDir),
		workspace.WorkspaceJournalLockPath(workspaceDir),
		workspace.WorkspaceRuntimeProjectionPath(workspaceDir),
		workspace.WorkspaceGenerationsDirectory(workspaceDir),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing initialized path %s: %v", path, err)
		}
	}

	readSnapshot, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if readSnapshot.Head() != snapshot.Head() || len(readSnapshot.Records()) != 1 {
		t.Fatalf("reader snapshot = %#v", readSnapshot)
	}
	conformance, err := workspace.VerifyWorkspaceRuntimeConformance(readSnapshot, definition.Generation())
	if err != nil || conformance != result.ProjectionDigest() {
		t.Fatalf("replay conformance = %s, %v", conformance, err)
	}

	if err := os.WriteFile(workspace.WorkspaceRuntimeProjectionPath(workspaceDir), []byte("disposable-corruption"), 0o644); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := workspace.RebuildWorkspaceRuntimeProjectionFile(journal)
	if closeErr := journal.Close(); err == nil {
		err = closeErr
	}
	if err != nil || rebuilt != result.ProjectionDigest() {
		t.Fatalf("rebuild disposable projection = %s, %v", rebuilt, err)
	}

	second, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, initializedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("idempotent initialization: %v", err)
	}
	if len(second.Snapshot().Records()) != 1 || second.Snapshot().Head() != snapshot.Head() {
		t.Fatalf("idempotent initialization appended history: %#v", second.Snapshot().Records())
	}
	candidate := mustProspectiveCandidate(t, fixture)
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, candidate, initializedAt.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("different generation initialization error = %v", err)
	}
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	generations, err := store.List()
	if err != nil || len(generations) != 1 || generations[0] != definition.Generation() {
		t.Fatalf("initialization materialized a runtime candidate: %v, %v", generations, err)
	}
}

func TestInitializationResumesAfterBootstrapTailRecovery(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Store(definition)
	if err != nil {
		t.Fatal(err)
	}

	faulty, err := workspace.OpenWorkspaceJournalWithOptions(workspaceDir, workspace.JournalReadWrite, workspace.JournalOptions{
		FaultInjector: func(point workspace.JournalFaultPoint) error {
			if point == workspace.JournalFaultAfterAppendPrefix {
				return errors.New("simulated initialization crash")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := workspace.NewWorkspaceInitializedJournalEvent(
		definition.Workspace().ID(), definition.Generation(), stored.DefinitionDigest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResource := workspace.WorkspaceJournalResource(definition.Workspace().ID())
	generationResource := workspace.GenerationJournalResource(definition.Generation())
	workspaceRevision, _ := workspace.NewJournalResourceRevision(workspaceResource, 0)
	generationRevision, _ := workspace.NewJournalResourceRevision(generationResource, 0)
	request, err := workspace.NewJournalAppend(
		event,
		mustTime(t, "2026-07-21T01:00:00Z"),
		[]workspace.JournalResourceRevision{workspaceRevision, generationRevision},
		[]workspace.JournalResource{workspaceResource, generationResource},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := faulty.Append(request); err == nil || !strings.Contains(err.Error(), string(workspace.JournalFaultAfterAppendPrefix)) {
		t.Fatalf("partial initialization append error = %v", err)
	}
	if err := faulty.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir); err == nil {
		t.Fatal("partial initialization record was not diagnosed")
	}

	recoveryJournal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	report, err := recoveryJournal.RecoverIncompleteTail(
		definition.Workspace().ID(), mustTime(t, "2026-07-21T01:01:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Recovered() || report.DiscardOffset() != 0 || report.TruncatedHead() != workspace.JournalGenesisHash() {
		t.Fatalf("bootstrap recovery report = %#v", report)
	}
	if err := recoveryJournal.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := workspace.InitializeWorkspaceV2(
		workspaceDir, definition, mustTime(t, "2026-07-21T01:02:00Z"),
	)
	if err != nil {
		t.Fatalf("resume initialization: %v", err)
	}
	records := result.Snapshot().Records()
	if len(records) != 2 || records[0].EventType() != workspace.JournalEventTailRecovered ||
		records[1].EventType() != workspace.JournalEventWorkspaceInitialized {
		t.Fatalf("resumed initialization journal = %#v", records)
	}
	recovery, ok := records[0].Event().(workspace.JournalTailRecoveredEvent)
	if !ok || recovery.Generation() != definition.Generation() || recovery.ResultingHead() != workspace.JournalGenesisHash() {
		t.Fatalf("bootstrap recovery event = %#v", records[0].Event())
	}
	if result.Runtime().WorkspaceID() != definition.Workspace().ID() ||
		result.Runtime().ActiveGeneration() != definition.Generation() ||
		len(result.Runtime().Recoveries()) != 1 {
		t.Fatalf("resumed initialization runtime = %#v", result.Runtime())
	}
}

func TestWorkspaceJournalUsesProcessLifetimeAdvisoryLocks(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z")); err != nil {
		t.Fatal(err)
	}

	writer, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite); !errors.Is(err, workspace.ErrWorkspaceJournalLocked) {
		t.Fatalf("second writer lock error = %v", err)
	}
	if _, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly); !errors.Is(err, workspace.ErrWorkspaceJournalLocked) {
		t.Fatalf("reader during writer lock error = %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceJournalLockSubprocess$")
	command.Env = append(os.Environ(), "WORKSPACE_JOURNAL_LOCK_HELPER="+workspaceDir)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "lock-observed") {
		t.Fatalf("subprocess lock result: %v\n%s", err, output)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	firstReader, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer firstReader.Close()
	secondReader, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
	if err != nil {
		t.Fatalf("shared reader lock: %v", err)
	}
	defer secondReader.Close()
	if _, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite); !errors.Is(err, workspace.ErrWorkspaceJournalLocked) {
		t.Fatalf("writer during reader snapshots error = %v", err)
	}
	first, err := firstReader.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondReader.ReadSnapshot()
	if err != nil || first.Head() != second.Head() || first.ByteLength() != second.ByteLength() {
		t.Fatalf("consistent reader snapshots = %#v %#v, %v", first, second, err)
	}
}

func TestWorkspaceJournalReadOnlyLockDoesNotRequireWritePermission(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(
		workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z"),
	); err != nil {
		t.Fatal(err)
	}
	lockPath := workspace.WorkspaceJournalLockPath(workspaceDir)
	if err := os.Chmod(lockPath, 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(lockPath, 0o644) }()

	reader, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadOnly)
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
	if len(snapshot.Records()) != 1 || snapshot.Records()[0].EventType() != workspace.JournalEventWorkspaceInitialized {
		t.Fatalf("read-only snapshot = %#v", snapshot.Records())
	}
}

func TestWorkspaceJournalLockSubprocess(t *testing.T) {
	workspaceDir := os.Getenv("WORKSPACE_JOURNAL_LOCK_HELPER")
	if workspaceDir == "" {
		t.Skip("subprocess helper")
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if errors.Is(err, workspace.ErrWorkspaceJournalLocked) {
		fmt.Print("lock-observed")
		return
	}
	if err == nil {
		_ = journal.Close()
		t.Fatal("subprocess unexpectedly acquired writer lock")
	}
	t.Fatalf("unexpected subprocess lock error: %v", err)
}

func TestWorkspaceJournalMultiProcessCASAllowsOneWinner(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z")); err != nil {
		t.Fatal(err)
	}
	newCommand := func(label string, output *bytes.Buffer) *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceJournalCASSubprocess$")
		command.Env = append(
			os.Environ(),
			"WORKSPACE_JOURNAL_CAS_HELPER="+workspaceDir,
			"WORKSPACE_JOURNAL_CAS_LABEL="+label,
			"WORKSPACE_JOURNAL_CAS_ACTIVE="+definition.Generation().String(),
		)
		command.Stdout = output
		command.Stderr = output
		return command
	}
	var firstOutput, secondOutput bytes.Buffer
	first := newCommand("candidate-one", &firstOutput)
	second := newCommand("candidate-two", &secondOutput)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("first CAS subprocess: %v\n%s", err, firstOutput.String())
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second CAS subprocess: %v\n%s", err, secondOutput.String())
	}
	combined := firstOutput.String() + secondOutput.String()
	if strings.Count(combined, "cas-appended") != 1 || strings.Count(combined, "cas-stale") != 1 {
		t.Fatalf("multi-process CAS outputs:\nfirst=%s\nsecond=%s", firstOutput.String(), secondOutput.String())
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records()) != 2 || snapshot.Revision(workspace.WorkspaceJournalResource(definition.Workspace().ID())) != 2 {
		t.Fatalf("multi-process CAS journal = %#v", snapshot.Records())
	}
}

func TestWorkspaceJournalCASSubprocess(t *testing.T) {
	workspaceDir := os.Getenv("WORKSPACE_JOURNAL_CAS_HELPER")
	if workspaceDir == "" {
		t.Skip("subprocess helper")
	}
	active, err := workspace.ParseDigest(os.Getenv("WORKSPACE_JOURNAL_CAS_ACTIVE"))
	if err != nil {
		t.Fatal(err)
	}
	label := os.Getenv("WORKSPACE_JOURNAL_CAS_LABEL")
	var journal *workspace.WorkspaceJournal
	for attempt := 0; attempt < 200; attempt++ {
		journal, err = workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
		if err == nil {
			break
		}
		if !errors.Is(err, workspace.ErrWorkspaceJournalLocked) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if journal == nil {
		t.Fatal("timed out acquiring journal lock")
	}
	defer journal.Close()
	workspaceID := workspace.MustID("example-workspace")
	candidate := workspace.DigestBytes([]byte(label))
	event, err := workspace.NewCandidateGenerationStoredJournalEvent(workspaceID, active, candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResource := workspace.WorkspaceJournalResource(workspaceID)
	candidateResource := workspace.GenerationJournalResource(candidate)
	workspaceRevision, _ := workspace.NewJournalResourceRevision(workspaceResource, 1)
	candidateRevision, _ := workspace.NewJournalResourceRevision(candidateResource, 0)
	request, err := workspace.NewJournalAppend(
		event, mustTime(t, "2026-07-21T01:01:00Z"),
		[]workspace.JournalResourceRevision{workspaceRevision, candidateRevision},
		[]workspace.JournalResource{workspaceResource, candidateResource},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(request); err != nil {
		var stale workspace.StaleJournalResourceError
		if errors.As(err, &stale) {
			fmt.Print("cas-stale")
			return
		}
		t.Fatal(err)
	}
	fmt.Print("cas-appended")
}

func TestJournalCASRejectsStaleResourceWithoutAppending(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	result, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	candidate := workspace.DigestBytes([]byte("candidate"))
	event, err := workspace.NewCandidateGenerationStoredJournalEvent(definition.Workspace().ID(), definition.Generation(), candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResource := workspace.WorkspaceJournalResource(definition.Workspace().ID())
	candidateResource := workspace.GenerationJournalResource(candidate)
	stale, _ := workspace.NewJournalResourceRevision(workspaceResource, 0)
	candidateRevision, _ := workspace.NewJournalResourceRevision(candidateResource, 0)
	request, err := workspace.NewJournalAppend(
		event, mustTime(t, "2026-07-21T01:01:00Z"),
		[]workspace.JournalResourceRevision{stale, candidateRevision},
		[]workspace.JournalResource{workspaceResource, candidateResource},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = journal.Append(request)
	var staleError workspace.StaleJournalResourceError
	if !errors.As(err, &staleError) || staleError.Observed != 1 || staleError.Expected != 0 {
		t.Fatalf("stale CAS error = %v", err)
	}
	after, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Head() != result.Snapshot().Head() || len(after.Records()) != 1 {
		t.Fatalf("stale append changed journal: %#v", after.Records())
	}
}

func TestJournalRejectsInvalidEventResourceSetsAndRuntimeTransitions(t *testing.T) {
	var nilEvent *workspace.WorkspaceInitializedJournalEvent
	if _, err := workspace.NewJournalAppend(
		nilEvent,
		mustTime(t, "2026-07-21T01:00:00Z"),
		nil,
		[]workspace.JournalResource{workspace.WorkspaceJournalResource(workspace.MustID("example-workspace"))},
	); err == nil || !strings.Contains(err.Error(), "unsupported workspace journal event") {
		t.Fatalf("typed nil event error = %v", err)
	}

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	initialized, err := workspace.InitializeWorkspaceV2(
		workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	candidate := workspace.DigestBytes([]byte("candidate"))
	event, err := workspace.NewCandidateGenerationStoredJournalEvent(
		definition.Workspace().ID(), definition.Generation(), candidate, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResource := workspace.WorkspaceJournalResource(definition.Workspace().ID())
	candidateResource := workspace.GenerationJournalResource(candidate)
	workspaceRevision, _ := workspace.NewJournalResourceRevision(workspaceResource, snapshot.Revision(workspaceResource))
	candidateRevision, _ := workspace.NewJournalResourceRevision(candidateResource, snapshot.Revision(candidateResource))
	if _, err := workspace.NewJournalAppend(
		event,
		mustTime(t, "2026-07-21T01:01:00Z"),
		[]workspace.JournalResourceRevision{workspaceRevision, candidateRevision},
		[]workspace.JournalResource{candidateResource},
	); err == nil || !strings.Contains(err.Error(), "invalid CAS resource set") {
		t.Fatalf("incomplete event resource set error = %v", err)
	}
	activation, err := workspace.NewGenerationActivatedJournalEvent(
		definition.Workspace().ID(),
		definition.Generation(),
		candidate,
		workspace.DigestBytes([]byte("comparison")),
		workspace.DigestBytes([]byte("owner-receipt")),
		workspace.EmptyRuntimeHistoryBinding(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewJournalAppend(
		activation,
		mustTime(t, "2026-07-21T01:01:00Z"),
		[]workspace.JournalResourceRevision{workspaceRevision, candidateRevision},
		[]workspace.JournalResource{workspaceResource, candidateResource},
	); err == nil || !strings.Contains(err.Error(), "owner-authorized activation workflow") {
		t.Fatalf("direct activation append error = %v", err)
	}

	staleEvent, err := workspace.NewCandidateGenerationStoredJournalEvent(
		definition.Workspace().ID(), workspace.DigestBytes([]byte("stale-active")), candidate, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := workspace.NewJournalAppend(
		staleEvent,
		mustTime(t, "2026-07-21T01:01:00Z"),
		[]workspace.JournalResourceRevision{workspaceRevision, candidateRevision},
		[]workspace.JournalResource{workspaceResource, candidateResource},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(request); err == nil || !strings.Contains(err.Error(), "active workspace generation") {
		t.Fatalf("invalid runtime transition error = %v", err)
	}
	after, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Head() != initialized.Snapshot().Head() || len(after.Records()) != 1 {
		t.Fatalf("invalid runtime transition changed journal: %#v", after.Records())
	}
}

func TestIncompleteTailRequiresExplicitRecoveryAndRecordsDiscardedBytes(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	initialized, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte(`{"schema_version":2,"sequence":2`)
	journalPath := workspace.WorkspaceJournalPath(workspaceDir)
	file, err := os.OpenFile(journalPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(partial); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	beforeRead, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = workspace.ReadWorkspaceJournalSnapshot(workspaceDir)
	var tail workspace.IncompleteJournalTailError
	if !errors.As(err, &tail) {
		t.Fatalf("ordinary reader tail error = %v", err)
	}
	if tail.Offset() != initialized.Snapshot().ByteLength() || tail.Size() != int64(len(partial)) || tail.Digest() != workspace.DigestBytes(partial) || tail.ResultingHead() != initialized.Snapshot().Head() {
		t.Fatalf("tail diagnosis = offset %d size %d digest %s head %s", tail.Offset(), tail.Size(), tail.Digest(), tail.ResultingHead())
	}
	afterRead, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(beforeRead, afterRead) {
		t.Fatalf("ordinary reader repaired journal: %v", err)
	}

	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	report, err := journal.RecoverIncompleteTail(definition.Workspace().ID(), mustTime(t, "2026-07-21T01:02:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Recovered() || report.DiscardOffset() != tail.Offset() || report.DiscardSize() != tail.Size() ||
		report.DiscardDigest() != tail.Digest() || report.TruncatedHead() != initialized.Snapshot().Head() || report.JournalHead() == report.TruncatedHead() {
		t.Fatalf("recovery report = %#v", report)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records()) != 2 || snapshot.Records()[1].EventType() != workspace.JournalEventTailRecovered || snapshot.Head() != report.JournalHead() {
		t.Fatalf("recovered journal = %#v", snapshot.Records())
	}
	recovery, ok := snapshot.Records()[1].Event().(workspace.JournalTailRecoveredEvent)
	if !ok || recovery.DiscardOffset() != tail.Offset() || recovery.DiscardSize() != tail.Size() || recovery.DiscardDigest() != tail.Digest() || recovery.ResultingHead() != initialized.Snapshot().Head() {
		t.Fatalf("recovery event = %#v", snapshot.Records()[1].Event())
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil || len(runtime.Recoveries()) != 1 || runtime.ActiveGeneration() != definition.Generation() {
		t.Fatalf("recovery projection = %#v, %v", runtime, err)
	}
	second, err := journal.RecoverIncompleteTail(definition.Workspace().ID(), mustTime(t, "2026-07-21T01:03:00Z"))
	if err != nil || second.Recovered() || second.JournalHead() != snapshot.Head() {
		t.Fatalf("idempotent recovery = %#v, %v", second, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRejectsCorruptCompleteRecord(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journalPath := workspace.WorkspaceJournalPath(workspaceDir)
	file, err := os.OpenFile(journalPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{corrupt-complete-record}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(journalPath)
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := journal.RecoverIncompleteTail(definition.Workspace().ID(), mustTime(t, "2026-07-21T01:01:00Z")); err == nil || !strings.Contains(err.Error(), "parse journal record 2") {
		t.Fatalf("complete corruption recovery error = %v", err)
	}
	after, _ := os.ReadFile(journalPath)
	if !bytes.Equal(before, after) {
		t.Fatal("complete corruption was modified")
	}
}

func TestJournalFailpointsDistinguishIncompleteAndCompleteAppends(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z")); err != nil {
		t.Fatal(err)
	}

	candidateOne := workspace.DigestBytes([]byte("candidate-one"))
	faulty, err := workspace.OpenWorkspaceJournalWithOptions(workspaceDir, workspace.JournalReadWrite, workspace.JournalOptions{
		FaultInjector: func(point workspace.JournalFaultPoint) error {
			if point == workspace.JournalFaultAfterAppendPrefix {
				return errors.New("simulated process crash")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := faulty.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	request := candidateJournalAppend(t, snapshot, definition.Workspace().ID(), definition.Generation(), candidateOne, mustTime(t, "2026-07-21T01:01:00Z"))
	if _, err := faulty.Append(request); err == nil || !strings.Contains(err.Error(), string(workspace.JournalFaultAfterAppendPrefix)) {
		t.Fatalf("partial append failpoint error = %v", err)
	}
	if err := faulty.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir); err == nil {
		t.Fatal("partial failpoint did not leave a diagnosed tail")
	}
	recoveryJournal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoveryJournal.RecoverIncompleteTail(definition.Workspace().ID(), mustTime(t, "2026-07-21T01:02:00Z")); err != nil {
		t.Fatal(err)
	}
	if err := recoveryJournal.Close(); err != nil {
		t.Fatal(err)
	}

	candidateTwo := workspace.DigestBytes([]byte("candidate-two"))
	beforeSync, err := workspace.OpenWorkspaceJournalWithOptions(workspaceDir, workspace.JournalReadWrite, workspace.JournalOptions{
		FaultInjector: func(point workspace.JournalFaultPoint) error {
			if point == workspace.JournalFaultBeforeFileSync {
				return errors.New("simulated exit before fsync")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = beforeSync.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	request = candidateJournalAppend(t, snapshot, definition.Workspace().ID(), definition.Generation(), candidateTwo, mustTime(t, "2026-07-21T01:03:00Z"))
	if _, err := beforeSync.Append(request); err == nil || !strings.Contains(err.Error(), string(workspace.JournalFaultBeforeFileSync)) {
		t.Fatalf("before-sync failpoint error = %v", err)
	} else {
		var ambiguous workspace.JournalAppendAmbiguousError
		if !errors.As(err, &ambiguous) || ambiguous.EventHash.IsZero() {
			t.Fatalf("before-sync outcome was not marked ambiguous: %v", err)
		}
	}
	if err := beforeSync.Close(); err != nil {
		t.Fatal(err)
	}
	complete, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir)
	if err != nil {
		t.Fatalf("complete but unsynced append should be reconciled as a complete record: %v", err)
	}
	last := complete.Records()[len(complete.Records())-1]
	if last.EventType() != workspace.JournalEventCandidateStored {
		t.Fatalf("complete failpoint record = %#v", last)
	}
}

func TestJournalRecoveryResumesAcrossCrashBoundaries(t *testing.T) {
	for _, faultPoint := range []workspace.JournalFaultPoint{
		workspace.JournalFaultBeforeAppend,
		workspace.JournalFaultAfterAppendPrefix,
		workspace.JournalFaultBeforeFileSync,
		workspace.JournalFaultAfterFileSync,
		workspace.JournalFaultBeforeDirectorySync,
		workspace.JournalFaultAfterDirectorySync,
	} {
		t.Run(string(faultPoint), func(t *testing.T) {
			fixture := newDefinitionFixture(t)
			definition := mustDefinition(t, fixture.sources)
			workspaceDir := t.TempDir()
			initialized, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z"))
			if err != nil {
				t.Fatal(err)
			}
			partial := []byte("partial-original-record")
			file, err := os.OpenFile(workspace.WorkspaceJournalPath(workspaceDir), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write(partial); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}

			faulted := false
			journal, err := workspace.OpenWorkspaceJournalWithOptions(workspaceDir, workspace.JournalReadWrite, workspace.JournalOptions{
				FaultInjector: func(point workspace.JournalFaultPoint) error {
					if point == faultPoint && !faulted {
						faulted = true
						return errors.New("simulated recovery crash")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := journal.RecoverIncompleteTail(definition.Workspace().ID(), mustTime(t, "2026-07-21T01:01:00Z")); err == nil || !strings.Contains(err.Error(), string(faultPoint)) {
				t.Fatalf("recovery crash error = %v", err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir); err == nil || !strings.Contains(err.Error(), "ordinary readers cannot repair") {
				t.Fatalf("reader during pending recovery error = %v", err)
			}

			resumer, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
			if err != nil {
				t.Fatal(err)
			}
			report, err := resumer.RecoverIncompleteTail(definition.Workspace().ID(), mustTime(t, "2026-07-21T01:02:00Z"))
			if err != nil {
				t.Fatal(err)
			}
			if !report.Recovered() || report.DiscardOffset() != initialized.Snapshot().ByteLength() ||
				report.DiscardSize() != int64(len(partial)) || report.DiscardDigest() != workspace.DigestBytes(partial) {
				t.Fatalf("resumed recovery report = %#v", report)
			}
			snapshot, err := resumer.ReadSnapshot()
			if err != nil || len(snapshot.Records()) != 2 || snapshot.Records()[1].EventType() != workspace.JournalEventTailRecovered {
				t.Fatalf("resumed recovery journal = %#v, %v", snapshot.Records(), err)
			}
			if err := resumer.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJournalSubprocessCrashesAroundAppendAndFsync(t *testing.T) {
	for _, faultPoint := range []workspace.JournalFaultPoint{
		workspace.JournalFaultAfterAppendPrefix,
		workspace.JournalFaultBeforeFileSync,
		workspace.JournalFaultAfterFileSync,
		workspace.JournalFaultBeforeDirectorySync,
		workspace.JournalFaultAfterDirectorySync,
	} {
		t.Run(string(faultPoint), func(t *testing.T) {
			fixture := newDefinitionFixture(t)
			definition := mustDefinition(t, fixture.sources)
			workspaceDir := t.TempDir()
			if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T01:00:00Z")); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceJournalCrashSubprocess$")
			command.Env = append(
				os.Environ(),
				"WORKSPACE_JOURNAL_CRASH_HELPER="+workspaceDir,
				"WORKSPACE_JOURNAL_CRASH_POINT="+string(faultPoint),
				"WORKSPACE_JOURNAL_CRASH_ACTIVE="+definition.Generation().String(),
			)
			err := command.Run()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 73 {
				t.Fatalf("crash subprocess exit = %v", err)
			}
			if faultPoint == workspace.JournalFaultAfterAppendPrefix {
				if _, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir); err == nil {
					t.Fatal("subprocess partial append was not diagnosed")
				}
				journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := journal.RecoverIncompleteTail(definition.Workspace().ID(), mustTime(t, "2026-07-21T01:02:00Z")); err != nil {
					t.Fatal(err)
				}
				if err := journal.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			snapshot, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir)
			if err != nil || len(snapshot.Records()) != 2 || snapshot.Records()[1].EventType() != workspace.JournalEventCandidateStored {
				t.Fatalf("complete subprocess append = %#v, %v", snapshot.Records(), err)
			}
		})
	}
}

func TestJournalSubprocessCrashesAroundInitialAppendDirectorySync(t *testing.T) {
	for _, faultPoint := range []workspace.JournalFaultPoint{
		workspace.JournalFaultBeforeDirectorySync,
		workspace.JournalFaultAfterDirectorySync,
	} {
		t.Run(string(faultPoint), func(t *testing.T) {
			workspaceDir := t.TempDir()
			generation := workspace.DigestBytes([]byte("initial-generation"))
			definitionDigest := workspace.DigestBytes([]byte("initial-definition"))
			command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceJournalInitialAppendCrashSubprocess$")
			command.Env = append(
				os.Environ(),
				"WORKSPACE_JOURNAL_INITIAL_CRASH_HELPER="+workspaceDir,
				"WORKSPACE_JOURNAL_CRASH_POINT="+string(faultPoint),
				"WORKSPACE_JOURNAL_CRASH_ACTIVE="+generation.String(),
				"WORKSPACE_JOURNAL_CRASH_DEFINITION="+definitionDigest.String(),
			)
			err := command.Run()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 73 {
				t.Fatalf("initial append crash subprocess exit = %v", err)
			}

			writer, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
			if err != nil {
				t.Fatalf("reopen after ambiguous initial append: %v", err)
			}
			snapshot, readErr := writer.ReadSnapshot()
			closeErr := writer.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if len(snapshot.Records()) != 1 || snapshot.Records()[0].EventType() != workspace.JournalEventWorkspaceInitialized {
				t.Fatalf("reconciled initial append = %#v", snapshot.Records())
			}
		})
	}
}

func TestWorkspaceJournalInitialAppendCrashSubprocess(t *testing.T) {
	workspaceDir := os.Getenv("WORKSPACE_JOURNAL_INITIAL_CRASH_HELPER")
	if workspaceDir == "" {
		t.Skip("subprocess helper")
	}
	generation, err := workspace.ParseDigest(os.Getenv("WORKSPACE_JOURNAL_CRASH_ACTIVE"))
	if err != nil {
		t.Fatal(err)
	}
	definitionDigest, err := workspace.ParseDigest(os.Getenv("WORKSPACE_JOURNAL_CRASH_DEFINITION"))
	if err != nil {
		t.Fatal(err)
	}
	faultPoint := workspace.JournalFaultPoint(os.Getenv("WORKSPACE_JOURNAL_CRASH_POINT"))
	journal, err := workspace.OpenWorkspaceJournalWithOptions(workspaceDir, workspace.JournalReadWrite, workspace.JournalOptions{
		FaultInjector: func(point workspace.JournalFaultPoint) error {
			if point == faultPoint {
				os.Exit(73)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := workspace.NewWorkspaceInitializedJournalEvent(
		workspace.MustID("example-workspace"), generation, definitionDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResource := workspace.WorkspaceJournalResource(workspace.MustID("example-workspace"))
	generationResource := workspace.GenerationJournalResource(generation)
	workspaceRevision, _ := workspace.NewJournalResourceRevision(workspaceResource, 0)
	generationRevision, _ := workspace.NewJournalResourceRevision(generationResource, 0)
	request, err := workspace.NewJournalAppend(
		event,
		mustTime(t, "2026-07-21T01:00:00Z"),
		[]workspace.JournalResourceRevision{workspaceRevision, generationRevision},
		[]workspace.JournalResource{workspaceResource, generationResource},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = journal.Append(request)
	t.Fatal("initial append crash failpoint was not reached")
}

func TestWorkspaceJournalCrashSubprocess(t *testing.T) {
	workspaceDir := os.Getenv("WORKSPACE_JOURNAL_CRASH_HELPER")
	if workspaceDir == "" {
		t.Skip("subprocess helper")
	}
	active, err := workspace.ParseDigest(os.Getenv("WORKSPACE_JOURNAL_CRASH_ACTIVE"))
	if err != nil {
		t.Fatal(err)
	}
	faultPoint := workspace.JournalFaultPoint(os.Getenv("WORKSPACE_JOURNAL_CRASH_POINT"))
	journal, err := workspace.OpenWorkspaceJournalWithOptions(workspaceDir, workspace.JournalReadWrite, workspace.JournalOptions{
		FaultInjector: func(point workspace.JournalFaultPoint) error {
			if point == faultPoint {
				os.Exit(73)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	request := candidateJournalAppend(
		t, snapshot, workspace.MustID("example-workspace"), active,
		workspace.DigestBytes([]byte("subprocess-candidate")), mustTime(t, "2026-07-21T01:01:00Z"),
	)
	_, _ = journal.Append(request)
	t.Fatal("crash failpoint was not reached")
}

func TestJournalRejectsOversizedAndNonCanonicalCompleteRecords(t *testing.T) {
	workspaceDir := t.TempDir()
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	oversized := append(bytes.Repeat([]byte{'x'}, workspace.MaxJournalRecordBytes+1), '\n')
	if err := os.WriteFile(workspace.WorkspaceJournalPath(workspaceDir), oversized, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadWorkspaceJournalSnapshot(workspaceDir); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized journal error = %v", err)
	}

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	canonicalDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(canonicalDir, definition, mustTime(t, "2026-07-21T01:00:00Z")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(workspace.WorkspaceJournalPath(canonicalDir))
	if err != nil {
		t.Fatal(err)
	}
	noncanonical := append([]byte(" "), content...)
	if err := os.WriteFile(workspace.WorkspaceJournalPath(canonicalDir), noncanonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadWorkspaceJournalSnapshot(canonicalDir); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
		t.Fatalf("noncanonical journal error = %v", err)
	}
}

func candidateJournalAppend(
	t *testing.T,
	snapshot workspace.JournalSnapshot,
	workspaceID workspace.ID,
	active, candidate workspace.Digest,
	occurredAt time.Time,
) workspace.JournalAppend {
	t.Helper()
	event, err := workspace.NewCandidateGenerationStoredJournalEvent(workspaceID, active, candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResource := workspace.WorkspaceJournalResource(workspaceID)
	candidateResource := workspace.GenerationJournalResource(candidate)
	workspaceRevision, _ := workspace.NewJournalResourceRevision(workspaceResource, snapshot.Revision(workspaceResource))
	candidateRevision, _ := workspace.NewJournalResourceRevision(candidateResource, snapshot.Revision(candidateResource))
	request, err := workspace.NewJournalAppend(
		event, occurredAt,
		[]workspace.JournalResourceRevision{workspaceRevision, candidateRevision},
		[]workspace.JournalResource{workspaceResource, candidateResource},
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustDefinition(t *testing.T, sources workspace.DefinitionSources) workspace.EffectiveWorkspaceDefinition {
	t.Helper()
	definition, err := workspace.ValidateDefinition(sources)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
