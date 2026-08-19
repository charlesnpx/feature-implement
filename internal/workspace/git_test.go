package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestAttemptMaterializationLeavesDirtyPrimaryByteIdentical(t *testing.T) {
	t.Parallel()

	fixture := reserveSafetyNetMaterializationAttempt(
		t, mustDefinition(t, newDefinitionFixture(t).sources),
	)
	primary := fixture.definition.Workspace().RepositoryRoot()
	seed := filepath.Join(primary, "seed.txt")
	if err := os.WriteFile(seed, []byte("staged primary change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, primary, "add", "--", "seed.txt")
	if err := os.WriteFile(seed, []byte("unstaged primary change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(primary, "operator-note.txt")
	if err := os.WriteFile(untracked, []byte("leave this alone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := []byte(runTargetGitTest(
		t, primary, "status", "--porcelain=v2", "-z", "--untracked-files=all",
	))
	if len(statusBefore) == 0 {
		t.Fatal("primary checkout was not made dirty")
	}
	gitDir := strings.TrimSpace(runTargetGitTest(
		t, primary, "rev-parse", "--absolute-git-dir",
	))
	indexBefore, err := os.ReadFile(filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	seedBefore, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	untrackedBefore, err := os.ReadFile(untracked)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := workspace.MaterializeAttempt(
		context.Background(),
		fixture.journal,
		fixture.definition,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.MaterializeAttemptRequest{
			AttemptID:  fixture.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-08-18T13:00:00Z"),
		},
	); err != nil {
		t.Fatalf("materialize attempt with dirty primary: %v", err)
	}

	indexAfter, err := os.ReadFile(filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	seedAfter, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	untrackedAfter, err := os.ReadFile(untracked)
	if err != nil {
		t.Fatal(err)
	}
	statusAfter := []byte(runTargetGitTest(
		t, primary, "status", "--porcelain=v2", "-z", "--untracked-files=all",
	))
	if !bytes.Equal(indexBefore, indexAfter) ||
		!bytes.Equal(seedBefore, seedAfter) ||
		!bytes.Equal(untrackedBefore, untrackedAfter) ||
		!bytes.Equal(statusBefore, statusAfter) {
		t.Fatal("attempt materialization changed the primary checkout")
	}
}

func TestAttemptWorktreeMaterializesExactRequestedTree(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	original := mustDefinition(t, fixture.sources)
	primary := original.Workspace().RepositoryRoot()
	if err := os.MkdirAll(filepath.Join(primary, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(primary, "links"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(primary, "bin", "run")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\necho feature\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../bin/run", filepath.Join(primary, "links", "run")); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, primary, "add", "--", "bin/run", "links/run")
	runTargetGitTest(
		t,
		primary,
		"-c", "user.name=Safety Net Test",
		"-c", "user.email=safety-net@example.invalid",
		"commit", "--quiet", "-m", "Add executable and symlink fixture",
	)
	base := parseGitHead(t, primary)
	sources := cloneDefinitionSources(fixture.sources)
	updated := strings.Replace(
		string(sources.Workspace.Bytes), fixture.base.String(), base.String(), 1,
	)
	if updated == string(sources.Workspace.Bytes) {
		t.Fatal("test fixture did not update its pinned base commit")
	}
	sources.Workspace.Bytes = []byte(updated)
	definition := mustDefinition(t, sources)
	attemptFixture := reserveSafetyNetMaterializationAttempt(t, definition)
	attempt, err := workspace.MaterializeAttempt(
		context.Background(),
		attemptFixture.journal,
		attemptFixture.definition,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.MaterializeAttemptRequest{
			AttemptID:  attemptFixture.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-08-18T13:10:00Z"),
		},
	)
	if err != nil {
		t.Fatalf("materialize exact tree: %v", err)
	}

	wantTree := safetyNetGitObject(
		t,
		primary,
		"rev-parse",
		rawGitObject(definition.Workspace().BaseCommit())+"^{tree}",
	)
	gotTree := safetyNetGitObject(t, attempt.Worktree(), "write-tree")
	if gotTree != wantTree {
		t.Fatalf("materialized tree = %s, want %s", gotTree, wantTree)
	}
	runnerInfo, err := os.Lstat(filepath.Join(attempt.Worktree(), "bin", "run"))
	if err != nil || runnerInfo.Mode()&0o111 == 0 {
		t.Fatalf("materialized executable mode = %v, %v", runnerInfo.Mode(), err)
	}
	link := filepath.Join(attempt.Worktree(), "links", "run")
	linkInfo, err := os.Lstat(link)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("materialized symlink mode = %v, %v", linkInfo.Mode(), err)
	}
	if target, err := os.Readlink(link); err != nil || target != "../bin/run" {
		t.Fatalf("materialized symlink target = %q, %v", target, err)
	}
}

func TestFeatureRefCASRejectsDriftWithoutChangingTheRef(t *testing.T) {
	t.Parallel()

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	_ = stopRealIntegrationAfterCommit(t, scenario)
	drift := createIntegrationTestCommit(
		t,
		scenario.repositoryRoot,
		scenario.acceptedTree,
		[]workspace.GitObjectID{scenario.base},
		"unexpected feature head",
	)
	featureRef := scenario.definition.Workspace().FeatureRef()
	runTargetGitTest(
		t,
		scenario.repositoryRoot,
		"update-ref",
		featureRef,
		rawGitObject(drift),
		rawGitObject(scenario.base),
	)
	beforeRecords := journalRecordCount(t, scenario.journal)
	_, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-08-18T13:15:00Z"),
		},
	)
	if err == nil {
		t.Fatal("feature-ref publication accepted a drifted ref")
	}
	current := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot, "rev-parse", featureRef,
	))
	if current != rawGitObject(drift) {
		t.Fatalf("drifted feature ref changed to %s", current)
	}
	if journalRecordCount(t, scenario.journal) != beforeRecords {
		t.Fatal("rejected feature-ref publication changed the journal")
	}
}

func TestIntegrationRetryReturnsTheSameCommitAndTree(t *testing.T) {
	t.Parallel()

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	request := workspace.IntegrateMergeUnitRequest{
		AttemptID:  scenario.attempt.AttemptID(),
		OccurredAt: mustTime(t, "2026-08-18T13:20:00Z"),
	}
	first, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstTree := safetyNetGitObject(
		t,
		scenario.repositoryRoot,
		"rev-parse",
		rawGitObject(first.MergeCommit())+"^{tree}",
	)
	second, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondTree := safetyNetGitObject(
		t,
		scenario.repositoryRoot,
		"rev-parse",
		rawGitObject(second.MergeCommit())+"^{tree}",
	)
	if first.MergeCommit() != second.MergeCommit() ||
		firstTree != secondTree || firstTree != scenario.acceptedTree {
		t.Fatalf(
			"integration identities = first %s/%s second %s/%s accepted tree %s",
			first.MergeCommit(), firstTree, second.MergeCommit(), secondTree, scenario.acceptedTree,
		)
	}
}

func TestIntegrationRecoveryCompletesPublishedRefExactlyOnce(t *testing.T) {
	t.Parallel()

	scenario := newRealIntegrationScenario(
		t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
	)
	crash := errors.New("crash after feature-ref publication")
	_, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.journal,
		scenario.definition,
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  scenario.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-08-18T13:30:00Z"),
			Fault: func(point workspace.IntegrationLifecycleFaultPoint) error {
				if point == workspace.IntegrationFaultAfterRefCAS {
					return crash
				}
				return nil
			},
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("integration crash error = %v", err)
	}
	featureRef := scenario.definition.Workspace().FeatureRef()
	published := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot, "rev-parse", featureRef,
	))
	if published == rawGitObject(scenario.base) {
		t.Fatal("feature ref did not move before the simulated crash")
	}
	if journalEventCount(
		t, scenario.journal, workspace.JournalEventMergeUnitIntegrated,
	) != 0 {
		t.Fatal("completion record was appended before the simulated crash")
	}

	recovered, err := workspace.RecoverWorkspaceLocalEffects(
		context.Background(),
		scenario.journal,
		scenario.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.DefaultLocalAttemptGitAdapter(),
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.RecoverWorkspaceLocalEffectsRequest{
			OccurredAt: mustTime(t, "2026-08-18T13:30:01Z"),
		},
	)
	if err != nil {
		t.Fatalf("recover published feature ref: %v", err)
	}
	if !containsRecoveryAction(
		recovered.Actions(), workspace.LocalRecoveryIntegrationCompleted,
	) {
		t.Fatalf("recovery actions = %v", recovered.Actions())
	}
	if current := strings.TrimSpace(runTargetGitTest(
		t, scenario.repositoryRoot, "rev-parse", featureRef,
	)); current != published {
		t.Fatalf("recovery moved feature ref from %s to %s", published, current)
	}
	if journalEventCount(
		t, scenario.journal, workspace.JournalEventMergeUnitIntegrated,
	) != 1 {
		t.Fatal("recovery did not append exactly one completion record")
	}
	beforeRetry := journalRecordCount(t, scenario.journal)
	again, err := workspace.RecoverWorkspaceLocalEffects(
		context.Background(),
		scenario.journal,
		scenario.definition,
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.DefaultLocalAttemptGitAdapter(),
		scenario.repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.RecoverWorkspaceLocalEffectsRequest{
			OccurredAt: mustTime(t, "2026-08-18T13:30:02Z"),
		},
	)
	if err != nil || again.Recovered() ||
		journalRecordCount(t, scenario.journal) != beforeRetry {
		t.Fatalf("repeat recovery = %#v, %v", again, err)
	}
}

func TestInitializationRefusesUnownedFeatureRefWithoutMutation(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	primary := definition.Workspace().RepositoryRoot()
	featureRef := definition.Workspace().FeatureRef()
	runTargetGitTest(
		t,
		primary,
		"update-ref",
		featureRef,
		rawGitObject(definition.Workspace().BaseCommit()),
	)
	refBefore := strings.TrimSpace(runTargetGitTest(
		t, primary, "rev-parse", featureRef,
	))
	statusBefore := []byte(runTargetGitTest(
		t, primary, "status", "--porcelain=v2", "-z", "--untracked-files=all",
	))
	runtimeRoot := t.TempDir()
	worktreeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-08-18T13:40:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
	); err == nil {
		t.Fatal("initialization adopted an unowned feature ref")
	}
	refAfter := strings.TrimSpace(runTargetGitTest(
		t, primary, "rev-parse", featureRef,
	))
	statusAfter := []byte(runTargetGitTest(
		t, primary, "status", "--porcelain=v2", "-z", "--untracked-files=all",
	))
	if refAfter != refBefore || !bytes.Equal(statusBefore, statusAfter) {
		t.Fatal("unowned feature-ref rejection mutated the primary checkout")
	}
	for _, path := range []string{
		workspace.WorkspaceJournalPath(runtimeRoot),
		workspace.WorkspaceRuntimeProjectionPath(runtimeRoot),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unowned feature-ref rejection created %s: %v", path, err)
		}
	}
}

type safetyNetMaterializationFixture struct {
	definition workspace.EffectiveWorkspaceDefinition
	journal    *workspace.WorkspaceJournal
	attempt    workspace.RuntimeAttemptProjection
}

func reserveSafetyNetMaterializationAttempt(
	t *testing.T,
	definition workspace.EffectiveWorkspaceDefinition,
) safetyNetMaterializationFixture {
	t.Helper()

	runtimeRoot := t.TempDir()
	worktreeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-08-18T12:50:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
	); err != nil {
		t.Fatalf("initialize materialization fixture: %v", err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		runtimeRoot, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("safety-net-goal"), workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := workspace.ReserveAttempt(
		context.Background(),
		journal,
		definition,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.ReserveAttemptRequest{
			MergeUnit:     mustMergeUnitReference(t, "alpha-plan", "unit-one"),
			AttemptNumber: 1,
			Goal:          goal,
			OccurredAt:    mustTime(t, "2026-08-18T12:50:01Z"),
		},
	)
	if err != nil {
		t.Fatalf("reserve materialization attempt: %v", err)
	}
	return safetyNetMaterializationFixture{
		definition: definition,
		journal:    journal,
		attempt:    attempt,
	}
}

func safetyNetGitObject(
	t *testing.T,
	repository string,
	arguments ...string,
) workspace.GitObjectID {
	t.Helper()

	raw := strings.TrimSpace(runTargetGitTest(t, repository, arguments...))
	algorithm := strings.TrimSpace(runTargetGitTest(
		t, repository, "rev-parse", "--show-object-format",
	))
	object, err := workspace.ParseGitObjectID(algorithm + ":" + raw)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func journalEventCount(
	t *testing.T,
	journal *workspace.WorkspaceJournal,
	eventType workspace.JournalEventType,
) int {
	t.Helper()

	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range snapshot.Records() {
		if record.EventType() == eventType {
			count++
		}
	}
	return count
}
