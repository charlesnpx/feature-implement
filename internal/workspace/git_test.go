package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestAttemptMaterializationLeavesDirtyPrimaryByteIdentical(t *testing.T) {
	t.Parallel()

	definition := mustDefinition(t, newDefinitionFixture(t).sources)
	primary := definition.Workspace().RepositoryRoot()
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
	reserveSafetyNetMaterializationAttempt(t, definition)

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
	attempt := attemptFixture.attempt

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
	runnerBytes, err := os.ReadFile(filepath.Join(
		attempt.Worktree(), "bin", "run",
	))
	if err != nil || !bytes.Equal(
		runnerBytes, []byte("#!/bin/sh\necho feature\n"),
	) {
		t.Fatalf("materialized executable bytes = %q, %v", runnerBytes, err)
	}
	runTargetGitTest(t, attempt.Worktree(), "update-index", "--refresh")
	runTargetGitTest(t, attempt.Worktree(), "diff-files", "--quiet")
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

func TestIndependentIntegrationConstructionReturnsTheSameCommitAndTree(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	repositoryRoot := definition.Workspace().RepositoryRoot()
	acceptedTree := safetyNetGitObject(
		t,
		repositoryRoot,
		"rev-parse",
		rawGitObject(definition.Workspace().BaseCommit())+"^{tree}",
	)
	acceptedHead := createIntegrationTestCommit(
		t,
		repositoryRoot,
		acceptedTree,
		[]workspace.GitObjectID{definition.Workspace().BaseCommit()},
		"accepted attempt",
	)
	runTargetGitTest(
		t,
		repositoryRoot,
		"update-ref",
		"refs/safety-net/accepted",
		rawGitObject(acceptedHead),
	)
	worktreeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	type identity struct {
		commit workspace.GitObjectID
		tree   workspace.GitObjectID
	}
	constructions := make([]identity, 0, 2)
	for range 2 {
		scenario := newIndependentIntegrationConstruction(
			t, definition, worktreeRoot, acceptedHead, acceptedTree,
		)
		commit := stopRealIntegrationAfterCommit(t, scenario)
		constructions = append(constructions, identity{
			commit: commit,
			tree: safetyNetGitObject(
				t,
				scenario.repositoryRoot,
				"rev-parse",
				rawGitObject(commit)+"^{tree}",
			),
		})
		discardIndependentIntegrationConstruction(t, scenario, commit)
	}
	if constructions[0].commit != constructions[1].commit ||
		constructions[0].tree != constructions[1].tree ||
		constructions[0].tree != acceptedTree {
		t.Fatalf(
			"integration identities = first %s/%s second %s/%s accepted tree %s",
			constructions[0].commit,
			constructions[0].tree,
			constructions[1].commit,
			constructions[1].tree,
			acceptedTree,
		)
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
	reflogBefore := []byte(runTargetGitTest(
		t, primary, "reflog", "show", "--format=%H %gD %gs", featureRef,
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
	reflogAfter := []byte(runTargetGitTest(
		t, primary, "reflog", "show", "--format=%H %gD %gs", featureRef,
	))
	statusAfter := []byte(runTargetGitTest(
		t, primary, "status", "--porcelain=v2", "-z", "--untracked-files=all",
	))
	if refAfter != refBefore ||
		!bytes.Equal(reflogBefore, reflogAfter) ||
		!bytes.Equal(statusBefore, statusAfter) {
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
	attempt, err := workspace.StartAttempt(
		context.Background(),
		journal,
		definition,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.StartAttemptRequest{
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

func newIndependentIntegrationConstruction(
	t *testing.T,
	definition workspace.EffectiveWorkspaceDefinition,
	worktreeRoot string,
	acceptedHead, acceptedTree workspace.GitObjectID,
) *realIntegrationScenario {
	t.Helper()

	runtimeRoot := t.TempDir()
	initialized, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-08-18T13:20:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
	)
	if err != nil {
		t.Fatalf("initialize independent integration construction: %v", err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		runtimeRoot, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	target, ok := initialized.Runtime().LocalTarget()
	if !ok || target.CreatedHead().IsZero() {
		t.Fatal("independent integration construction has no local target")
	}
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("integration-determinism-goal"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	mergeUnit := mustMergeUnitReference(t, "alpha-plan", "unit-one")
	attempt, err := workspace.StartAttempt(
		context.Background(),
		journal,
		definition,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.StartAttemptRequest{
			MergeUnit:     mergeUnit,
			AttemptNumber: 1,
			Goal:          goal,
			OccurredAt:    mustTime(t, "2026-08-18T13:20:01Z"),
		},
	)
	if err != nil {
		t.Fatalf("reserve independent integration attempt: %v", err)
	}
	repositoryRoot := definition.Workspace().RepositoryRoot()
	runTargetGitTest(
		t,
		attempt.Worktree(),
		"update-ref",
		"--no-deref",
		"HEAD",
		rawGitObject(acceptedHead),
	)
	runTargetGitTest(
		t, attempt.Worktree(), "reset", "--hard", rawGitObject(acceptedHead),
	)
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(
		acceptedHead, acceptedTree, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{snapshot: repositorySnapshot}
	if _, err := workspace.AdoptAttemptHead(
		context.Background(),
		journal,
		definition,
		repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-08-18T13:20:03Z"),
		},
	); err != nil {
		t.Fatalf("adopt independent integration head: %v", err)
	}
	attempt = mustRuntimeAttempt(t, journal, attempt.AttemptID())
	return &realIntegrationScenario{
		attemptHarness: attemptHarness{
			definition: definition,
			journal:    journal,
			workspace:  runtimeRoot,
			git:        &fakeAttemptGit{},
			base:       target.CreatedHead(),
			unit:       mergeUnit,
			goal:       goal,
			worktrees:  worktreeRoot,
		},
		attempt:        attempt,
		repository:     repository,
		repositoryRoot: repositoryRoot,
		acceptedHead:   acceptedHead,
		acceptedTree:   acceptedTree,
	}
}

func discardIndependentIntegrationConstruction(
	t *testing.T,
	scenario *realIntegrationScenario,
	expectedMerge workspace.GitObjectID,
) {
	t.Helper()

	if err := scenario.journal.Close(); err != nil {
		t.Fatalf("close independent integration journal: %v", err)
	}
	if err := os.RemoveAll(scenario.attempt.Worktree()); err != nil {
		t.Fatalf("remove detached independent integration worktree: %v", err)
	}
	runTargetGitTest(
		t,
		scenario.repositoryRoot,
		"update-ref",
		"-d",
		scenario.definition.Workspace().FeatureRef(),
	)
	runTargetGitTest(
		t, scenario.repositoryRoot, "prune", "--expire=now",
	)
	if err := exec.Command(
		"git",
		"-C",
		scenario.repositoryRoot,
		"cat-file",
		"-e",
		rawGitObject(expectedMerge)+"^{commit}",
	).Run(); err == nil {
		t.Fatalf("prune retained prior independent integration commit %s", expectedMerge)
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
