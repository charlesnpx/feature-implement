package workspace_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type fakeAttemptGit struct {
	inspections map[string]workspace.AttemptGitInspection
	createCalls int
	validateErr error
	inspectErr  error
	createErr   error
}

func (git *fakeAttemptGit) ValidateAttemptWorktreeRoot(context.Context, string, string) error {
	return git.validateErr
}

func (git *fakeAttemptGit) InspectAttemptWorktree(
	_ context.Context,
	_ string,
	worktree string,
) (workspace.AttemptGitInspection, error) {
	if git.inspectErr != nil {
		return workspace.AttemptGitInspection{}, git.inspectErr
	}
	return git.inspections[worktree], nil
}

func (git *fakeAttemptGit) MaterializeAttemptTree(
	_ context.Context,
	_ string,
	base workspace.GitObjectID,
	worktree string,
) (workspace.AttemptGitInspection, error) {
	if inspection := git.inspections[worktree]; inspection.WorktreeExists() {
		return inspection, nil
	}
	if git.createErr != nil {
		return workspace.AttemptGitInspection{}, git.createErr
	}
	git.createCalls++
	inspection, err := workspace.NewScratchAttemptGitInspection(
		base, workspace.GitObjectID{}, workspace.AttemptWorktreeGitBinding{}, true,
	)
	if err != nil {
		return workspace.AttemptGitInspection{}, err
	}
	if git.inspections == nil {
		git.inspections = make(map[string]workspace.AttemptGitInspection)
	}
	git.inspections[worktree] = inspection
	return inspection, nil
}

func (git *fakeAttemptGit) setHead(
	t *testing.T,
	worktree string,
	head workspace.GitObjectID,
	clean bool,
) {
	t.Helper()
	inspection, err := workspace.NewScratchAttemptGitInspection(
		head, workspace.GitObjectID{}, workspace.AttemptWorktreeGitBinding{}, clean,
	)
	if err != nil {
		t.Fatal(err)
	}
	if git.inspections == nil {
		git.inspections = make(map[string]workspace.AttemptGitInspection)
	}
	git.inspections[worktree] = inspection
}

type attemptHarness struct {
	definition workspace.EffectiveWorkspaceDefinition
	journal    *workspace.WorkspaceJournal
	workspace  string
	git        *fakeAttemptGit
	base       workspace.GitObjectID
	unit       workspace.MergeUnitReference
	goal       workspace.GoalBinding
}

func newAttemptHarness(t *testing.T, unitID string) attemptHarness {
	t.Helper()
	fixture := newDefinitionFixture(t)
	return newAttemptHarnessFromFixture(t, fixture, unitID)
}

func newIndependentAttemptHarness(t *testing.T, unitID string) attemptHarness {
	t.Helper()
	fixture := newDefinitionFixture(t)
	fixture.sources.Plans[0].Bytes = []byte(strings.Replace(
		string(fixture.sources.Plans[0].Bytes),
		"    dependencies:\n      - story-one",
		"    dependencies: []",
		1,
	))
	return newAttemptHarnessFromFixture(t, fixture, unitID)
}

func newAttemptHarnessFromFixture(t *testing.T, fixture definitionFixture, unitID string) attemptHarness {
	t.Helper()
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	initialized, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), workspaceDir, definition,
		mustTime(t, "2026-07-21T10:00:00Z"),
		workspace.WorkspaceInitializationOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	target, ok := initialized.Runtime().LocalTarget()
	if !ok || target.CreatedHead().IsZero() {
		t.Fatal("initialized harness has no durable local target head")
	}
	base := target.CreatedHead()
	goal, err := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	if err != nil {
		t.Fatal(err)
	}
	return attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDir, git: &fakeAttemptGit{}, base: base,
		unit: mustMergeUnitReference(t, "alpha-plan", unitID), goal: goal,
	}
}

func (h attemptHarness) reserve(t *testing.T, at string) workspace.RuntimeAttemptProjection {
	t.Helper()
	return h.reserveWithGit(t, h.git, at)
}

func (h attemptHarness) reserveWithLocalGit(t *testing.T, at string) workspace.RuntimeAttemptProjection {
	t.Helper()
	return h.reserveWithGit(t, workspace.DefaultLocalAttemptGitAdapter(), at)
}

func (h attemptHarness) reserveWithGit(
	t *testing.T,
	git workspace.AttemptGitPort,
	at string,
) workspace.RuntimeAttemptProjection {
	t.Helper()
	attempt, err := workspace.StartAttempt(
		context.Background(), h.journal, h.definition, git,
		workspace.StartAttemptRequest{
			MergeUnit: h.unit, AttemptNumber: 1,
			Goal: h.goal, OccurredAt: mustTime(t, at),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func TestAttemptIdentityIsDigestBacked(t *testing.T) {
	t.Parallel()

	workspaceID := workspace.MustID("workspace-one")
	generation := workspace.DigestBytes([]byte("generation-one"))
	plan := workspace.MustID("p" + strings.Repeat("1", 90))
	unit := workspace.MustID("u" + strings.Repeat("2", 90))
	reference, err := workspace.NewMergeUnitReference(plan, unit)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	identity, err := workspace.DeriveAttemptIdentity(workspaceID, generation, reference, 7, base)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := workspace.DeriveAttemptIdentity(workspaceID, generation, reference, 7, base)
	if err != nil || repeated.AttemptID() != identity.AttemptID() {
		t.Fatalf("attempt identity is not stable: %#v, %v", repeated, err)
	}
	otherBase, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	changed, _ := workspace.DeriveAttemptIdentity(workspaceID, generation, reference, 7, otherBase)
	if changed.AttemptID() == identity.AttemptID() {
		t.Fatal("attempt identity does not bind the exact base")
	}
	changed, _ = workspace.DeriveAttemptIdentity(
		workspaceID, workspace.DigestBytes([]byte("generation-two")),
		reference, 7, base,
	)
	if changed.AttemptID() == identity.AttemptID() {
		t.Fatal("attempt identity does not bind the generation")
	}
	changed, _ = workspace.DeriveAttemptIdentity(
		workspace.MustID("workspace-two"), generation,
		reference, 7, base,
	)
	if changed.AttemptID() == identity.AttemptID() {
		t.Fatal("attempt identity does not bind the workspace")
	}
}

func TestAttemptReservationEnforcesSchedulerOrderAndEffectiveAttemptBudget(t *testing.T) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	blockedUnit := mustMergeUnitReference(t, "alpha-plan", "unit-two")
	if _, err := workspace.StartAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.StartAttemptRequest{
			MergeUnit: blockedUnit, AttemptNumber: 1, Goal: harness.goal,
			OccurredAt: mustTime(t, "2026-07-21T10:01:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "not scheduler-ready") ||
		!strings.Contains(err.Error(), "unsatisfied dependency sets: [alpha-plan/unit-one]") {
		t.Fatalf("dependency-bypassing reservation error = %v", err)
	}
	if _, err := workspace.StartAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.StartAttemptRequest{
			MergeUnit: harness.unit, AttemptNumber: 4, Goal: harness.goal,
			OccurredAt: mustTime(t, "2026-07-21T10:02:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "exceeds max_attempts 3") {
		t.Fatalf("over-budget reservation error = %v", err)
	}
	if _, err := workspace.StartAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.StartAttemptRequest{
			MergeUnit: harness.unit, AttemptNumber: 2, Goal: harness.goal,
			OccurredAt: mustTime(t, "2026-07-21T10:03:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "next attempt is 1") {
		t.Fatalf("out-of-sequence reservation error = %v", err)
	}
	if attempt := harness.reserve(t, "2026-07-21T10:04:00Z"); attempt.AttemptNumber() != 1 {
		t.Fatalf("valid scheduler-ready reservation = %#v", attempt)
	}
}

func TestExecutionConfigRequiresExplicitSupportedBoundaryPolicy(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	valid := string(fixture.sources.ExecutionConfig.Bytes)
	config, err := workspace.DecodeExecutionConfig([]byte(valid))
	if err != nil {
		t.Fatalf("two-field boundary config decode: %v", err)
	}
	boundary := config.MergeUnits()[0].Boundary()
	if boundary.Checkpoint() != workspace.AttemptCheckpointPauseOnly ||
		boundary.Escalation() != workspace.AttemptEscalationAllowed {
		t.Fatalf("decoded boundary = checkpoint %q escalation %q", boundary.Checkpoint(), boundary.Escalation())
	}

	withoutBoundary := cloneDefinitionSources(fixture.sources)
	withoutBoundary.ExecutionConfig.Bytes = []byte(strings.Replace(
		string(withoutBoundary.ExecutionConfig.Bytes),
		"    boundary:\n      checkpoint: pause_only\n      escalation: allowed\n      serial_segment: serial-alpha\n",
		"",
		1,
	))
	if _, err := workspace.ValidateDefinition(withoutBoundary); err == nil || !strings.Contains(err.Error(), "boundary policy must be explicit") {
		t.Fatalf("missing boundary policy = %v", err)
	}

	legacy := strings.Replace(
		valid, "checkpoint: pause_only\n      escalation: allowed", "mode: pause_only", 1,
	)
	if _, err := workspace.DecodeExecutionConfig([]byte(legacy)); err == nil ||
		!strings.Contains(err.Error(), "checkpoint") || !strings.Contains(err.Error(), "escalation") {
		t.Fatalf("legacy boundary mode error = %v", err)
	}

	for _, test := range []struct {
		name    string
		source  string
		wantErr string
	}{
		{
			name:    "missing checkpoint",
			source:  strings.Replace(valid, "      checkpoint: pause_only\n", "", 1),
			wantErr: "checkpoint and escalation",
		},
		{
			name:    "missing escalation",
			source:  strings.Replace(valid, "      escalation: allowed\n", "", 1),
			wantErr: "checkpoint and escalation",
		},
		{
			name:    "unknown checkpoint",
			source:  strings.Replace(valid, "checkpoint: pause_only", "checkpoint: unsupported", 1),
			wantErr: "boundary checkpoint",
		},
		{
			name:    "complete-goal checkpoint",
			source:  strings.Replace(valid, "checkpoint: pause_only", "checkpoint: complete_goal_and_wait", 1),
			wantErr: `boundary checkpoint "complete_goal_and_wait" is unsupported`,
		},
		{
			name:    "unknown escalation",
			source:  strings.Replace(valid, "escalation: allowed", "escalation: unsupported", 1),
			wantErr: "boundary escalation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := workspace.DecodeExecutionConfig([]byte(test.source)); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("boundary config error = %v", err)
			}
		})
	}
}

func TestExecutionConfigValidatesBoundaryAgainstOptionalProfileBoundary(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	if _, err := workspace.DecodeExecutionConfig(fixture.sources.ExecutionConfig.Bytes); err != nil {
		t.Fatalf("absent profile boundary decode: %v", err)
	}
	withProfileBoundary := strings.Replace(
		string(fixture.sources.ExecutionConfig.Bytes),
		"merge_units:\n",
		"    boundary:\n      escalation: forbidden\nmerge_units:\n",
		1,
	)
	if _, err := workspace.DecodeExecutionConfig([]byte(withProfileBoundary)); err == nil ||
		!strings.Contains(err.Error(), "merge unit alpha-plan/unit-one boundary weakens escalation") {
		t.Fatalf("profile boundary escalation weakening error = %v", err)
	}
	strengthened := strings.ReplaceAll(
		withProfileBoundary,
		"escalation: allowed", "escalation: forbidden",
	)
	if _, err := workspace.DecodeExecutionConfig([]byte(strengthened)); err != nil {
		t.Fatalf("profile boundary escalation strengthening decode: %v", err)
	}
}

func TestStartAttemptReconcilesEveryPostAppendCrashPoint(t *testing.T) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	crash := errors.New("simulated crash")
	start := workspace.StartAttemptRequest{
		MergeUnit: harness.unit, AttemptNumber: 1,
		Goal: harness.goal, OccurredAt: mustTime(t, "2026-07-21T10:01:00Z"),
		Fault: failAt(workspace.AttemptFaultAfterReservation, crash),
	}
	if _, err := workspace.StartAttempt(
		context.Background(), harness.journal, harness.definition, harness.git, start,
	); !errors.Is(err, crash) {
		t.Fatalf("post-append start crash = %v", err)
	}
	attempt := onlyRuntimeAttempt(t, harness.journal)
	if attempt.Phase() != workspace.AttemptActive ||
		attempt.StartRecord() == 0 || harness.git.createCalls != 0 {
		t.Fatalf("post-append scratch start = %#v, creates %d", attempt, harness.git.createCalls)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if records := snapshot.Records(); len(records) == 0 || records[len(records)-1].EventType() != workspace.JournalEventAttemptStart {
		t.Fatalf("post-append durable record = %#v", records)
	}
	recordCount := len(snapshot.Records())
	for _, point := range []workspace.AttemptLifecycleFaultPoint{
		workspace.AttemptFaultAfterWorktreeCreation,
		workspace.AttemptFaultAfterGitVerification,
		workspace.AttemptFaultAfterStart,
	} {
		start.Fault = failAt(point, crash)
		if _, err := workspace.StartAttempt(
			context.Background(), harness.journal, harness.definition, harness.git, start,
		); !errors.Is(err, crash) {
			t.Fatalf("start crash at %s = %v", point, err)
		}
		if got := onlyRuntimeAttempt(t, harness.journal); got.StartRecord() != attempt.StartRecord() ||
			got.Phase() != workspace.AttemptActive || harness.git.createCalls != 1 {
			t.Fatalf("retry after %s = %#v, creates %d", point, got, harness.git.createCalls)
		}
	}
	start.Fault = nil
	started, err := workspace.StartAttempt(
		context.Background(), harness.journal, harness.definition, harness.git, start,
	)
	if err != nil || started.StartRecord() != attempt.StartRecord() ||
		started.VerifiedHead() != harness.base || started.LeaseID().IsZero() ||
		harness.git.createCalls != 1 {
		t.Fatalf("reconciled start = %#v, creates %d, err=%v", started, harness.git.createCalls, err)
	}
	snapshot, err = harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records()) != recordCount {
		t.Fatalf("reconciled start appended %d records, want %d", len(snapshot.Records()), recordCount)
	}
	if _, err := workspace.VerifyWorkspaceRuntimeConformance(snapshot, harness.definition.Generation()); err != nil {
		t.Fatalf("attempt replay conformance: %v", err)
	}
}

func TestStartAttemptRollsBackAnInterruptedScratchDirectory(t *testing.T) {
	t.Parallel()

	definition := mustDefinition(t, newDefinitionFixture(t).sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), workspaceDir, definition,
		mustTime(t, "2026-07-21T10:10:00Z"),
		workspace.WorkspaceInitializationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	goal, err := workspace.NewGoalBinding(workspace.MustID("start-recovery-goal"), workspace.GoalScopeMergeUnit)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("interrupt after scratch directory creation")
	interrupted := workspace.DefaultLocalAttemptGitAdapter().WithAttemptWorktreeMaterializationFaultInjector(
		func(point workspace.AttemptWorktreeMaterializationFaultPoint) error {
			if point == workspace.AttemptMaterializationFaultAfterDirectoryBinding {
				return failure
			}
			return nil
		},
	)
	request := workspace.StartAttemptRequest{
		MergeUnit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), AttemptNumber: 1,
		Goal: goal, OccurredAt: mustTime(t, "2026-07-21T10:11:00Z"),
	}
	if _, err := workspace.StartAttempt(context.Background(), journal, definition, interrupted, request); !errors.Is(err, failure) {
		t.Fatalf("interrupted scratch start = %v", err)
	}
	attempt := onlyRuntimeAttempt(t, journal)
	if attempt.Phase() != workspace.AttemptActive {
		t.Fatalf("interrupted scratch projection = %#v", attempt)
	}
	if _, err := os.Lstat(attempt.Worktree()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted scratch directory remains: %v", err)
	}
	if _, err := os.Lstat(attempt.Worktree() + ".feature-attempt-claim"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch materialization wrote a claim marker: %v", err)
	}
	before := journalRecordCount(t, journal)
	request.OccurredAt = mustTime(t, "2026-07-21T10:12:00Z")
	recovered, err := workspace.StartAttempt(
		context.Background(), journal, definition, workspace.DefaultLocalAttemptGitAdapter(), request,
	)
	if err != nil {
		t.Fatalf("recovered interrupted scratch directory = %v", err)
	}
	if recovered.AttemptID() != attempt.AttemptID() || recovered.Phase() != workspace.AttemptActive {
		t.Fatalf("recovered interrupted scratch projection = %#v", recovered)
	}
	if _, err := os.Lstat(attempt.Worktree()); err != nil {
		t.Fatalf("recovered scratch directory = %v", err)
	}
	if after := journalRecordCount(t, journal); after != before {
		t.Fatalf("recovered interrupted scratch directory appended records: before=%d after=%d", before, after)
	}
}

func newRawAttemptTreeRepository(
	t *testing.T,
) (repositoryRoot string, base workspace.GitObjectID) {
	t.Helper()
	repositoryRoot = canonicalTestDirectory(t)
	runGitSetup(t, repositoryRoot, "init", "--initial-branch=main", ".")
	if err := os.MkdirAll(filepath.Join(repositoryRoot, "links"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, fixture := range map[string]struct {
		content string
		mode    os.FileMode
	}{
		"payload.txt": {content: "raw payload\n", mode: 0o644},
		"script.sh":   {content: "#!/bin/sh\nexit 0\n", mode: 0o755},
	} {
		if err := os.WriteFile(
			filepath.Join(repositoryRoot, path), []byte(fixture.content), fixture.mode,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(
		"../payload.txt", filepath.Join(repositoryRoot, "links", "payload"),
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "--", "links/payload", "payload.txt", "script.sh")
	runGitSetup(t, repositoryRoot, "update-index", "--chmod=+x", "--", "script.sh")
	runGitSetup(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "raw attempt tree",
	)
	baseText := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "rev-parse", "HEAD"),
	))
	var err error
	base, err = workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}
	return repositoryRoot, base
}

func newRealAttemptRepository(
	t *testing.T,
) (repositoryRoot string, linkedRoot string, base workspace.GitObjectID) {
	t.Helper()
	parent := t.TempDir()
	repositoryRoot = filepath.Join(parent, "repository")
	remoteRoot := filepath.Join(parent, "remote.git")
	linkedRoot = filepath.Join(parent, "linked")
	runGitSetup(t, "", "init", "--initial-branch=main", repositoryRoot)
	runGitSetup(t, "", "init", "--bare", remoteRoot)
	runGitSetup(t, repositoryRoot, "config", "user.name", "Attempt Test")
	runGitSetup(t, repositoryRoot, "config", "user.email", "attempt@example.invalid")
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "tracked.txt"),
		[]byte("committed\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "tracked.txt")
	runGitSetup(t, repositoryRoot, "commit", "-m", "initial")
	runGitSetup(t, repositoryRoot, "remote", "add", "origin", remoteRoot)
	runGitSetup(t, repositoryRoot, "push", "-u", "origin", "main")
	runGitSetup(t, repositoryRoot, "worktree", "add", "-b", "linked-fixture", linkedRoot, "main")
	baseText := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "rev-parse", "HEAD"),
	))
	var err error
	base, err = workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}
	return repositoryRoot, linkedRoot, base
}

func directoryEntryNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func journalRecordCount(t *testing.T, journal *workspace.WorkspaceJournal) int {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return len(snapshot.Records())
}

func TestPauseOnlyBoundaryAtomicallyPausesAndResumesSameGoal(t *testing.T) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T11:01:00Z")
	unconstrainedHead := mustGitObject(t, 'b')
	harness.git.setHead(
		t, attempt.Worktree(), unconstrainedHead, true,
	)
	lease := attempt.LeaseID()
	evidence := boundaryEvidence(t, "pause-only")
	result, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	paused := result.Attempt()
	if paused.Phase() != workspace.AttemptPaused || !paused.LeaseID().IsZero() ||
		paused.SerialSegmentHeld() {
		t.Fatalf("pause-only boundary did not atomically close and release: %#v", paused)
	}
	boundary := result.Boundary()
	if boundary.Kind() != workspace.AttemptBoundaryKindCheckpoint ||
		boundary.Checkpoint() != workspace.AttemptCheckpointPauseOnly ||
		boundary.LeaseID() != lease || boundary.EvidenceDigest().IsZero() ||
		boundary.Head() != unconstrainedHead || !boundary.LeaseFencedAndReleased() {
		t.Fatalf("boundary did not checkpoint closed bindings: %#v", boundary)
	}
	replayed := mustRuntimeAttempt(t, harness.journal, attempt.AttemptID())
	replayedBoundary, ok := replayed.CurrentBoundary()
	if !ok || replayedBoundary.Kind() != workspace.AttemptBoundaryKindCheckpoint {
		t.Fatalf("replayed planned checkpoint kind = %#v exists=%v", replayedBoundary, ok)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	records := snapshot.Records()
	last := records[len(records)-1]
	if last.EventType() != workspace.JournalEventAttemptBoundary {
		t.Fatalf("last record = %s, expected atomic boundary", last.EventType())
	}
	resumed, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ResumeAttemptRequest{AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:04:00Z")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Phase() != workspace.AttemptActive || resumed.Goal() != harness.goal ||
		resumed.LeaseID() == lease || !resumed.SerialSegmentHeld() {
		t.Fatalf("pause-only resume bindings = %#v", resumed)
	}
}

func TestAttemptBoundaryRejectsDisallowedKindsBeforeAppend(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		replacement string
		kind        workspace.AttemptBoundaryKind
		want        string
	}{
		{
			name:        "checkpoint when checkpoint policy is none",
			replacement: "checkpoint: none\n      escalation: allowed\n      serial_segment: serial-alpha",
			kind:        workspace.AttemptBoundaryKindCheckpoint,
			want:        "configured checkpoint policy \"none\"",
		},
		{
			name:        "escalation when escalation policy is forbidden",
			replacement: "checkpoint: pause_only\n      escalation: forbidden\n      serial_segment: serial-alpha",
			kind:        workspace.AttemptBoundaryKindEscalation,
			want:        "configured escalation policy \"forbidden\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDefinitionFixture(t)
			configuration := string(fixture.sources.ExecutionConfig.Bytes)
			updated := strings.Replace(
				configuration,
				"checkpoint: pause_only\n      escalation: allowed\n      serial_segment: serial-alpha",
				test.replacement,
				1,
			)
			if updated == configuration {
				t.Fatal("failed to install boundary policy fixture")
			}
			fixture.sources.ExecutionConfig.Bytes = []byte(updated)
			harness := newAttemptHarnessFromFixture(t, fixture, "unit-one")
			attempt := harness.reserve(t, "2026-07-21T11:01:00Z")
			before := journalRecordCount(t, harness.journal)
			_, err := workspace.RecordAttemptBoundary(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.RecordAttemptBoundaryRequest{
					AttemptID: attempt.AttemptID(), Kind: test.kind,
					Evidence: boundaryEvidence(t, test.name), OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
				},
			)
			if err == nil || !strings.Contains(err.Error(), "alpha-plan/unit-one") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("disallowed boundary error = %v", err)
			}
			if after := journalRecordCount(t, harness.journal); after != before {
				t.Fatalf("rejected boundary wrote journal records: before=%d after=%d", before, after)
			}
		})
	}
}

func TestAttemptBoundaryRejectsDifferentKindSameEvidenceOnPausedRetry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		replacement   string
		recordedKind  workspace.AttemptBoundaryKind
		requestedKind workspace.AttemptBoundaryKind
	}{
		{
			name:          "checkpoint-none retry cannot replace recorded escalation",
			replacement:   "checkpoint: none\n      escalation: allowed\n      serial_segment: serial-alpha",
			recordedKind:  workspace.AttemptBoundaryKindEscalation,
			requestedKind: workspace.AttemptBoundaryKindCheckpoint,
		},
		{
			name:          "escalation-forbidden retry cannot replace recorded checkpoint",
			replacement:   "checkpoint: pause_only\n      escalation: forbidden\n      serial_segment: serial-alpha",
			recordedKind:  workspace.AttemptBoundaryKindCheckpoint,
			requestedKind: workspace.AttemptBoundaryKindEscalation,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDefinitionFixture(t)
			configuration := string(fixture.sources.ExecutionConfig.Bytes)
			updated := strings.Replace(
				configuration,
				"checkpoint: pause_only\n      escalation: allowed\n      serial_segment: serial-alpha",
				test.replacement,
				1,
			)
			if updated == configuration {
				t.Fatal("failed to install boundary policy fixture")
			}
			fixture.sources.ExecutionConfig.Bytes = []byte(updated)
			harness := newAttemptHarnessFromFixture(t, fixture, "unit-one")
			attempt := harness.reserve(t, "2026-07-21T11:01:00Z")
			evidence := boundaryEvidence(t, test.name)
			if _, err := workspace.RecordAttemptBoundary(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.RecordAttemptBoundaryRequest{
					AttemptID: attempt.AttemptID(), Kind: test.recordedKind,
					Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
				},
			); err != nil {
				t.Fatalf("record %s boundary: %v", test.recordedKind, err)
			}
			before := journalRecordCount(t, harness.journal)
			_, err := workspace.RecordAttemptBoundary(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.RecordAttemptBoundaryRequest{
					AttemptID: attempt.AttemptID(), Kind: test.requestedKind,
					Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T11:04:00Z"),
				},
			)
			if err == nil || !strings.Contains(err.Error(), "different boundary kind") ||
				!strings.Contains(err.Error(), string(test.requestedKind)) ||
				!strings.Contains(err.Error(), string(test.recordedKind)) {
				t.Fatalf("different-kind paused retry error = %v", err)
			}
			if after := journalRecordCount(t, harness.journal); after != before {
				t.Fatalf("different-kind paused retry wrote journal records: before=%d after=%d", before, after)
			}
		})
	}
}

func TestAttemptBoundaryRequiresSupportedKind(t *testing.T) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T11:01:00Z")
	before := journalRecordCount(t, harness.journal)
	for _, kind := range []workspace.AttemptBoundaryKind{"", "unsupported"} {
		_, err := workspace.RecordAttemptBoundary(
			context.Background(), harness.journal, harness.definition, harness.git,
			workspace.RecordAttemptBoundaryRequest{
				AttemptID: attempt.AttemptID(), Kind: kind,
				Evidence:   boundaryEvidence(t, "invalid-kind-"+string(kind)),
				OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
			},
		)
		if err == nil || !strings.Contains(err.Error(), "kind") {
			t.Fatalf("boundary kind %q error = %v", kind, err)
		}
	}
	if after := journalRecordCount(t, harness.journal); after != before {
		t.Fatalf("unsupported boundary kinds wrote journal records: before=%d after=%d", before, after)
	}
}

func TestEscalationUsesPauseOnlyShape(t *testing.T) {
	t.Parallel()

	harness := newIndependentAttemptHarness(t, "unit-two")
	attempt := harness.reserve(t, "2026-07-21T12:01:00Z")
	result, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindEscalation,
			Evidence: boundaryEvidence(t, "raised-escalation"), OccurredAt: mustTime(t, "2026-07-21T12:03:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	boundary := result.Boundary()
	if boundary.Kind() != workspace.AttemptBoundaryKindEscalation ||
		boundary.Checkpoint() != workspace.AttemptCheckpointPauseOnly {
		t.Fatalf("escalation boundary shape = %#v", boundary)
	}
	replayed := mustRuntimeAttempt(t, harness.journal, attempt.AttemptID())
	replayedBoundary, ok := replayed.CurrentBoundary()
	if !ok || replayedBoundary.Kind() != workspace.AttemptBoundaryKindEscalation ||
		replayedBoundary.Checkpoint() != workspace.AttemptCheckpointPauseOnly {
		t.Fatalf("replayed escalation shape = %#v exists=%v", replayedBoundary, ok)
	}
	resumed, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ResumeAttemptRequest{AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:04:00Z")},
	)
	if err != nil || resumed.Goal() != attempt.Goal() {
		t.Fatalf("escalation resumed goal = %#v err=%v", resumed, err)
	}
}

func TestPauseAttemptPreservesPlannedAndExceptionStops(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		harness    func(*testing.T) attemptHarness
		kind       workspace.AttemptBoundaryKind
		checkpoint workspace.AttemptCheckpointMode
	}{
		{
			name:       "planned checkpoint",
			harness:    func(t *testing.T) attemptHarness { return newAttemptHarness(t, "unit-one") },
			kind:       workspace.AttemptBoundaryKindCheckpoint,
			checkpoint: workspace.AttemptCheckpointPauseOnly,
		},
		{
			name:       "agent-raised escalation",
			harness:    func(t *testing.T) attemptHarness { return newIndependentAttemptHarness(t, "unit-two") },
			kind:       workspace.AttemptBoundaryKindEscalation,
			checkpoint: workspace.AttemptCheckpointPauseOnly,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := test.harness(t)
			attempt := harness.reserve(t, "2026-07-21T12:10:00Z")
			harness.git.setHead(
				t, attempt.Worktree(), mustGitObject(t, 'c'), true,
			)

			paused, err := workspace.PauseAttempt(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.PauseAttemptRequest{
					AttemptID: attempt.AttemptID(), Kind: test.kind,
					Evidence:   boundaryEvidence(t, test.name),
					OccurredAt: mustTime(t, "2026-07-21T12:12:00Z"),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			boundary, ok := paused.CurrentBoundary()
			if !ok || paused.Phase() != workspace.AttemptPaused ||
				boundary.Kind() != test.kind || boundary.Checkpoint() != test.checkpoint {
				t.Fatalf("paused attempt lost stop kind: attempt=%#v boundary=%#v exists=%v", paused, boundary, ok)
			}
		})
	}
}

func TestLocalAttemptInspectionRejectsHiddenIndexFlags(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		flag string
	}{
		{name: "assume unchanged", flag: "--assume-unchanged"},
		{name: "skip worktree", flag: "--skip-worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, _, base := newProtocolRepository(t)
			worktree := filepath.Join(t.TempDir(), "attempt")
			adapter := workspace.DefaultLocalAttemptGitAdapter()
			if _, err := adapter.MaterializeAttemptTree(context.Background(), repository, base, worktree); err != nil {
				t.Fatal(err)
			}
			runGitSetup(t, worktree, "update-index", test.flag, "src/protocol.go")
			if err := os.WriteFile(
				filepath.Join(worktree, "src", "protocol.go"),
				[]byte("package protocol\n\nconst HiddenAtBoundary = true\n"), 0o644,
			); err != nil {
				t.Fatal(err)
			}

			if _, err := adapter.InspectAttemptWorktree(
				context.Background(), repository, worktree,
			); err == nil || !strings.Contains(err.Error(), "assume-unchanged and skip-worktree") {
				t.Fatalf("InspectAttemptWorktree hidden-index error = %v", err)
			}
		})
	}
}

func TestLocalAttemptInspectionRejectsIntentToAddIndexEntry(t *testing.T) {
	t.Parallel()

	repository, _, base := newProtocolRepository(t)
	worktree := filepath.Join(t.TempDir(), "attempt")
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if _, err := adapter.MaterializeAttemptTree(context.Background(), repository, base, worktree); err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(worktree, "intent.txt")
	if err := os.WriteFile(intentPath, []byte("intent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, worktree, "add", "-N", "--", "intent.txt")
	if err := os.Remove(intentPath); err != nil {
		t.Fatal(err)
	}
	if indexTree, headTree := strings.TrimSpace(string(
		runGitSetup(t, worktree, "write-tree"),
	)), strings.TrimSpace(string(
		runGitSetup(t, worktree, "rev-parse", "HEAD^{tree}"),
	)); indexTree != headTree {
		t.Fatalf(
			"intent-to-add unexpectedly changed write-tree: %s != %s",
			indexTree, headTree,
		)
	}
	inspection, err := adapter.
		InspectAttemptWorktree(
			context.Background(), repository, worktree,
		)
	if err != nil || inspection.Clean() {
		t.Fatalf("intent-to-add inspection = %#v, %v", inspection, err)
	}
}

func TestLocalAttemptInspectionRejectsRawModeDrift(t *testing.T) {
	t.Parallel()

	repository, _, base := newProtocolRepository(t)
	worktree := filepath.Join(t.TempDir(), "attempt")
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if _, err := adapter.MaterializeAttemptTree(context.Background(), repository, base, worktree); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, worktree, "config", "core.fileMode", "false")
	tracked := filepath.Join(worktree, "src", "protocol.go")
	if err := os.Chmod(tracked, 0o755); err != nil {
		t.Fatal(err)
	}
	if ordinary := runGitSetup(t, worktree, "status", "--porcelain=v1", "-z"); len(ordinary) != 0 {
		t.Fatalf("core.fileMode=false did not hide attempt mode drift: %q", ordinary)
	}
	inspection, err := adapter.InspectAttemptWorktree(
		context.Background(), repository, worktree,
	)
	if err != nil || inspection.Clean() {
		t.Fatalf("attempt raw-mode inspection = %#v, %v", inspection, err)
	}
}

func TestCheckpointBoundaryRecoversIdempotentlyAndRejectsStaleHead(t *testing.T) {
	t.Parallel()

	harness := newIndependentAttemptHarness(t, "unit-two")
	attempt := harness.reserve(t, "2026-07-21T12:01:00Z")
	crash := errors.New("simulated crash")
	evidence := boundaryEvidence(t, "checkpoint")
	_, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T12:03:00Z"),
			Fault: failAt(workspace.AttemptFaultAfterBoundary, crash),
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("boundary crash = %v", err)
	}
	result, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T12:04:00Z"),
		},
	)
	if err != nil {
		t.Fatalf("boundary retry = %#v, %v", result, err)
	}
	boundary := result.Boundary()
	if result.Attempt().Phase() != workspace.AttemptPaused ||
		boundary.Kind() != workspace.AttemptBoundaryKindCheckpoint ||
		boundary.Checkpoint() != workspace.AttemptCheckpointPauseOnly {
		t.Fatalf("checkpoint boundary shape = %#v %#v", result.Attempt(), boundary)
	}
	recordsAfterPause := journalRecordCount(t, harness.journal)
	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T12:05:00Z"),
		},
	); err != nil {
		t.Fatalf("idempotent boundary retry = %v", err)
	}
	if records := journalRecordCount(t, harness.journal); records != recordsAfterPause {
		t.Fatalf("idempotent boundary retry wrote records: before=%d after=%d", recordsAfterPause, records)
	}
	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "different-evidence"),
			OccurredAt: mustTime(t, "2026-07-21T12:06:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "different boundary evidence") {
		t.Fatalf("mismatched boundary evidence error = %v", err)
	}

	stale, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	harness.git.setHead(t, attempt.Worktree(), stale, true)
	if _, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ResumeAttemptRequest{AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:07:00Z")},
	); err == nil || !strings.Contains(err.Error(), "Git verification failed") {
		t.Fatalf("stale-head resume = %v", err)
	}
	harness.git.setHead(
		t, attempt.Worktree(), boundary.Head(), true,
	)
	resumeRequest := workspace.ResumeAttemptRequest{
		AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:08:00Z"),
		Fault: failAt(workspace.AttemptFaultBeforeLeaseBinding, crash),
	}
	if _, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git, resumeRequest,
	); !errors.Is(err, crash) {
		t.Fatalf("before lease binding crash = %v", err)
	}
	if got := mustRuntimeAttempt(t, harness.journal, attempt.AttemptID()).Phase(); got != workspace.AttemptPaused {
		t.Fatalf("pre-bind crash changed attempt phase to %s", got)
	}
	resumeRequest.Fault = failAt(workspace.AttemptFaultAfterResume, crash)
	if _, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git, resumeRequest,
	); !errors.Is(err, crash) {
		t.Fatalf("after resume crash = %v", err)
	}
	resumeRequest.Fault = nil
	resumed, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git, resumeRequest,
	)
	if err != nil || resumed.Phase() != workspace.AttemptActive || resumed.Goal() != harness.goal {
		t.Fatalf("resume retry = %#v, %v", resumed, err)
	}
}

func TestSerialSegmentsFenceOnlyMatchingSegments(t *testing.T) {
	t.Parallel()

	harness := newIndependentAttemptHarness(t, "unit-one")
	first := harness.reserve(t, "2026-07-21T13:01:00Z")
	otherUnit := mustMergeUnitReference(t, "alpha-plan", "unit-two")
	otherGoal, _ := workspace.NewGoalBinding(workspace.MustID("other-goal"), workspace.GoalScopeMergeUnit)
	other, err := workspace.StartAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.StartAttemptRequest{
			MergeUnit: otherUnit, AttemptNumber: 1,
			Goal: otherGoal, OccurredAt: mustTime(t, "2026-07-21T13:02:00Z"),
		},
	)
	if err != nil || other.AttemptID().IsZero() {
		t.Fatalf("unrelated segment was globally serialized: %#v, %v", other, err)
	}
	if !first.SerialSegmentHeld() || other.SerialSegmentHeld() {
		t.Fatalf("segment holdings = first %v, other %v", first.SerialSegmentHeld(), other.SerialSegmentHeld())
	}

	fixture := newDefinitionFixture(t)
	fixture.sources.Plans[0].Bytes = []byte(strings.Replace(
		string(fixture.sources.Plans[0].Bytes),
		"    dependencies:\n      - story-one",
		"    dependencies: []",
		1,
	))
	fixture.sources.ExecutionConfig.Bytes = []byte(strings.Replace(
		string(fixture.sources.ExecutionConfig.Bytes),
		"    boundary:\n      checkpoint: pause_only\n      escalation: allowed\n    policy:\n      require_passing_checks: true\n      allow_write_network: false\n      max_attempts: 2",
		"    boundary:\n      checkpoint: pause_only\n      escalation: allowed\n      serial_segment: serial-alpha\n    policy:\n      require_passing_checks: true\n      allow_write_network: false\n      max_attempts: 2",
		1,
	))
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), workspaceDir, definition,
		mustTime(t, "2026-07-21T13:10:00Z"),
		workspace.WorkspaceInitializationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	git := &fakeAttemptGit{}
	for index, unitID := range []string{"unit-one", "unit-two"} {
		_, err := workspace.StartAttempt(
			context.Background(), journal, definition, git,
			workspace.StartAttemptRequest{
				MergeUnit: mustMergeUnitReference(t, "alpha-plan", unitID), AttemptNumber: 1,
				Goal:       otherGoal,
				OccurredAt: mustTime(t, fmt.Sprintf("2026-07-21T13:1%d:00Z", index+1)),
			},
		)
		if index == 0 && err != nil {
			t.Fatal(err)
		}
		if index == 1 && (err == nil || !strings.Contains(err.Error(), "serial segment")) {
			t.Fatalf("matching segment was not fenced: %v", err)
		}
	}
}

func boundaryEvidence(t *testing.T, label string) []workspace.Evidence {
	t.Helper()
	item, err := workspace.NewEvidenceItem(workspace.MustID("result"), label)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := workspace.NewEvidence(
		workspace.MustID("checkpoint"), workspace.DigestBytes([]byte(label)), []workspace.EvidenceItem{item},
	)
	if err != nil {
		t.Fatal(err)
	}
	return []workspace.Evidence{evidence}
}

func failAt(point workspace.AttemptLifecycleFaultPoint, failure error) workspace.AttemptLifecycleFaultInjector {
	fired := false
	return func(actual workspace.AttemptLifecycleFaultPoint) error {
		if actual == point && !fired {
			fired = true
			return failure
		}
		return nil
	}
}

func mustRuntime(t *testing.T, journal *workspace.WorkspaceJournal) workspace.WorkspaceRuntimeProjection {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func onlyRuntimeAttempt(t *testing.T, journal *workspace.WorkspaceJournal) workspace.RuntimeAttemptProjection {
	t.Helper()
	attempts := mustRuntime(t, journal).Attempts()
	if len(attempts) != 1 {
		t.Fatalf("runtime attempts = %#v, want exactly one", attempts)
	}
	return attempts[0]
}

func mustRuntimeAttempt(
	t *testing.T,
	journal *workspace.WorkspaceJournal,
	attemptID workspace.ID,
) workspace.RuntimeAttemptProjection {
	t.Helper()
	attempt, exists := mustRuntime(t, journal).Attempt(attemptID)
	if !exists {
		t.Fatalf("attempt %s is missing", attemptID)
	}
	return attempt
}

func runGitSetup(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	argv := append([]string(nil), arguments...)
	if directory != "" {
		argv = append([]string{"-C", directory}, argv...)
	}
	command := exec.Command("git", argv...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git setup %v: %v: %s", arguments, err, output)
	}
	return output
}

var _ workspace.AttemptGitPort = (*fakeAttemptGit)(nil)
