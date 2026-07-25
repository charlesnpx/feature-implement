package workspace_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type fakeAttemptGit struct {
	local                []string
	inspection           workspace.AttemptGitInspection
	createCalls          int
	prepareCalls         int
	recoveryCalls        int
	claim                *workspace.AttemptWorktreeClaim
	validateErr          error
	inspectErr           error
	createErr            error
	partialOnCreateError bool
}

func (git *fakeAttemptGit) ValidateAttemptBranch(context.Context, string, string) error {
	return git.validateErr
}

func (git *fakeAttemptGit) ValidateAttemptWorktreeRoot(context.Context, string, string) error {
	return git.validateErr
}

func (git *fakeAttemptGit) InspectAttemptRefs(context.Context, string) (workspace.AttemptRefInventory, error) {
	if git.inspectErr != nil {
		return workspace.AttemptRefInventory{}, git.inspectErr
	}
	return workspace.NewAttemptRefInventory(git.local)
}

func (git *fakeAttemptGit) InspectAttemptWorktree(context.Context, string, string, string) (workspace.AttemptGitInspection, error) {
	if git.inspectErr != nil {
		return workspace.AttemptGitInspection{}, git.inspectErr
	}
	return git.inspection, nil
}

func (git *fakeAttemptGit) PrepareAttemptWorktree(
	_ context.Context,
	_ string,
	claim workspace.AttemptWorktreeClaim,
	recoverUnregistered bool,
) error {
	git.prepareCalls++
	if git.claim != nil && (git.claim.AttemptID() != claim.AttemptID() ||
		git.claim.Generation() != claim.Generation() || git.claim.Base() != claim.Base() ||
		git.claim.Branch() != claim.Branch() || git.claim.Worktree() != claim.Worktree()) {
		return fmt.Errorf("attempt worktree claim changed immutable bindings")
	}
	copyClaim := claim
	git.claim = &copyClaim
	if recoverUnregistered {
		git.recoveryCalls++
		inspection, err := workspace.NewAttemptGitInspection(
			git.inspection.BranchExists(), git.inspection.BranchHead(), false, false, "", workspace.GitObjectID{}, false,
		)
		if err != nil {
			return err
		}
		git.inspection = inspection
	}
	return nil
}

func (git *fakeAttemptGit) ReleaseAttemptWorktreeClaim(
	_ context.Context,
	claim workspace.AttemptWorktreeClaim,
) error {
	if git.claim == nil {
		return nil
	}
	if git.claim.AttemptID() != claim.AttemptID() || git.claim.Generation() != claim.Generation() ||
		git.claim.Base() != claim.Base() || git.claim.Branch() != claim.Branch() ||
		git.claim.Worktree() != claim.Worktree() {
		return fmt.Errorf("released attempt worktree claim changed immutable bindings")
	}
	git.claim = nil
	return nil
}

func (git *fakeAttemptGit) CreateAttemptWorktree(
	_ context.Context,
	_ string,
	claim workspace.AttemptWorktreeClaim,
	createBranch, _ bool,
) error {
	branch := claim.Branch()
	base := claim.Base()
	if git.createErr != nil {
		if git.partialOnCreateError {
			git.local = append(git.local, branch)
			inspection, inspectionErr := workspace.NewAttemptGitInspection(
				true, base, true, false, "", workspace.GitObjectID{}, false,
			)
			if inspectionErr != nil {
				return inspectionErr
			}
			git.inspection = inspection
		}
		return git.createErr
	}
	git.createCalls++
	if createBranch {
		git.local = append(git.local, branch)
	}
	inspection, err := workspace.NewAttemptGitInspection(true, base, true, true, branch, base, true)
	if err != nil {
		return err
	}
	git.inspection = inspection
	return nil
}

func (git *fakeAttemptGit) setHead(t *testing.T, branch string, head workspace.GitObjectID, clean bool) {
	t.Helper()
	inspection, err := workspace.NewAttemptGitInspection(true, head, true, true, branch, head, clean)
	if err != nil {
		t.Fatal(err)
	}
	git.inspection = inspection
}

type attemptHarness struct {
	definition workspace.EffectiveWorkspaceDefinition
	journal    *workspace.WorkspaceJournal
	workspace  string
	git        *fakeAttemptGit
	base       workspace.GitObjectID
	unit       workspace.MergeUnitReference
	goal       workspace.GoalBinding
	worktrees  string
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
	worktrees := t.TempDir()
	initialized, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), workspaceDir, definition,
		mustTime(t, "2026-07-21T10:00:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktrees},
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
		unit: mustMergeUnitReference(t, "alpha-plan", unitID), goal: goal, worktrees: worktrees,
	}
}

func (h attemptHarness) reserve(t *testing.T, at string) workspace.RuntimeAttemptProjection {
	t.Helper()
	attempt, err := workspace.ReserveAttempt(
		context.Background(), h.journal, h.definition, h.git,
		workspace.ReserveAttemptRequest{
			MergeUnit: h.unit, AttemptNumber: 1,
			Goal: h.goal, OccurredAt: mustTime(t, at),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func (h attemptHarness) materialize(t *testing.T, attemptID workspace.ID, at string) workspace.RuntimeAttemptProjection {
	t.Helper()
	attempt, err := workspace.MaterializeAttempt(
		context.Background(), h.journal, h.definition, h.git,
		workspace.MaterializeAttemptRequest{AttemptID: attemptID, OccurredAt: mustTime(t, at)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func TestAttemptIdentityIsFlatBoundedDigestBackedAndRejectsRefConflicts(t *testing.T) {
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
	if len(identity.Branch()) > 240 || strings.Count(identity.Branch(), "/") != 1 ||
		!strings.HasPrefix(identity.Branch(), "mu/") || !strings.Contains(identity.Branch(), "-a7-") {
		t.Fatalf("attempt branch is not flat and bounded: %q (%d)", identity.Branch(), len(identity.Branch()))
	}
	repeated, err := workspace.DeriveAttemptIdentity(workspaceID, generation, reference, 7, base)
	if err != nil || repeated.Branch() != identity.Branch() || repeated.AttemptID() != identity.AttemptID() {
		t.Fatalf("attempt identity is not stable: %#v, %v", repeated, err)
	}
	otherBase, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	changed, _ := workspace.DeriveAttemptIdentity(workspaceID, generation, reference, 7, otherBase)
	if changed.Branch() == identity.Branch() || changed.AttemptID() == identity.AttemptID() {
		t.Fatal("attempt identity does not bind the exact base")
	}
	changed, _ = workspace.DeriveAttemptIdentity(
		workspaceID, workspace.DigestBytes([]byte("generation-two")),
		reference, 7, base,
	)
	if changed.Branch() == identity.Branch() ||
		changed.AttemptID() == identity.AttemptID() {
		t.Fatal("attempt identity does not bind the generation")
	}
	changed, _ = workspace.DeriveAttemptIdentity(
		workspace.MustID("workspace-two"), generation,
		reference, 7, base,
	)
	if changed.Branch() == identity.Branch() ||
		changed.AttemptID() == identity.AttemptID() {
		t.Fatal("attempt identity does not bind the workspace")
	}

	tests := []struct {
		name string
		refs []string
		kind workspace.AttemptRefConflictKind
	}{
		{"exact", []string{identity.Branch()}, workspace.AttemptRefExact},
		{"ancestor", []string{"mu"}, workspace.AttemptRefAncestor},
		{"descendant", []string{identity.Branch() + "/child"}, workspace.AttemptRefDescendant},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := workspace.CheckAttemptRefConflicts(identity.Branch(), test.refs, false)
			var conflict workspace.AttemptRefConflict
			if !errors.As(err, &conflict) || conflict.Kind() != test.kind {
				t.Fatalf("conflict = %#v, %v", conflict, err)
			}
		})
	}
}

func TestAttemptReservationEnforcesSchedulerOrderAndEffectiveAttemptBudget(t *testing.T) {
	harness := newAttemptHarness(t, "unit-one")
	blockedUnit := mustMergeUnitReference(t, "alpha-plan", "unit-two")
	if _, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ReserveAttemptRequest{
			MergeUnit: blockedUnit, AttemptNumber: 1, Goal: harness.goal,
			OccurredAt: mustTime(t, "2026-07-21T10:01:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "not scheduler-ready") || !strings.Contains(err.Error(), "dependency:alpha-plan/unit-one") {
		t.Fatalf("dependency-bypassing reservation error = %v", err)
	}
	if _, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ReserveAttemptRequest{
			MergeUnit: harness.unit, AttemptNumber: 4, Goal: harness.goal,
			OccurredAt: mustTime(t, "2026-07-21T10:02:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "exceeds max_attempts 3") {
		t.Fatalf("over-budget reservation error = %v", err)
	}
	if _, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ReserveAttemptRequest{
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
	fixture := newDefinitionFixture(t)
	withoutBoundary := cloneDefinitionSources(fixture.sources)
	withoutBoundary.ExecutionConfig.Bytes = []byte(strings.Replace(
		string(withoutBoundary.ExecutionConfig.Bytes),
		"    boundary:\n      mode: pause_only\n      serial_segment: serial-alpha\n",
		"",
		1,
	))
	if _, err := workspace.ValidateDefinition(withoutBoundary); err == nil || !strings.Contains(err.Error(), "boundary policy must be explicit") {
		t.Fatalf("missing boundary policy = %v", err)
	}
	unsupported := cloneDefinitionSources(fixture.sources)
	unsupported.ExecutionConfig.Bytes = []byte(strings.Replace(
		string(unsupported.ExecutionConfig.Bytes), "mode: pause_only", "mode: maybe_pause", 1,
	))
	if _, err := workspace.ValidateDefinition(unsupported); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported boundary policy = %v", err)
	}
}

func TestReserveAttemptRejectsLocalRefCollisions(t *testing.T) {
	for _, test := range []struct {
		name  string
		local func(string) []string
	}{
		{"exact", func(branch string) []string { return []string{branch} }},
		{"ancestor", func(string) []string { return []string{"mu"} }},
		{"descendant", func(branch string) []string { return []string{branch + "/child"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newAttemptHarness(t, "unit-one")
			identity, err := workspace.DeriveAttemptIdentity(
				harness.definition.Workspace().ID(), harness.definition.Generation(),
				harness.unit, 1, harness.base,
			)
			if err != nil {
				t.Fatal(err)
			}
			harness.git.local = test.local(identity.Branch())
			_, err = workspace.ReserveAttempt(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.ReserveAttemptRequest{
					MergeUnit: harness.unit, AttemptNumber: 1, Goal: harness.goal,
					OccurredAt: mustTime(t, "2026-07-21T09:00:00Z"),
				},
			)
			var conflict workspace.AttemptRefConflict
			if !errors.As(err, &conflict) {
				t.Fatalf("reservation collision = %v", err)
			}
		})
	}
}

func TestAttemptMaterializationRecoversAcrossEveryCrashPoint(t *testing.T) {
	harness := newAttemptHarness(t, "unit-one")
	crash := errors.New("simulated crash")
	identity, err := workspace.DeriveAttemptIdentity(
		harness.definition.Workspace().ID(), harness.definition.Generation(),
		harness.unit, 1, harness.base,
	)
	if err != nil {
		t.Fatal(err)
	}
	reservation := workspace.ReserveAttemptRequest{
		MergeUnit: harness.unit, AttemptNumber: 1,
		Goal: harness.goal, OccurredAt: mustTime(t, "2026-07-21T10:01:00Z"),
		Fault: failAt(workspace.AttemptFaultAfterReservation, crash),
	}
	if _, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git, reservation,
	); !errors.Is(err, crash) {
		t.Fatalf("reservation crash = %v", err)
	}
	attempt := mustRuntimeAttempt(t, harness.journal, identity.AttemptID())
	reservation.Fault = nil
	reservedAgain, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git, reservation,
	)
	if err != nil || reservedAgain.ReservationRecord() != attempt.ReservationRecord() {
		t.Fatalf("reservation retry = %#v, %v", reservedAgain, err)
	}
	_, err = workspace.MaterializeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:02:00Z"),
			Fault: failAt(workspace.AttemptFaultAfterMaterializationIntent, crash),
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("materialization intent crash = %v", err)
	}
	projected := mustRuntimeAttempt(t, harness.journal, attempt.AttemptID())
	if projected.Phase() != workspace.AttemptMaterializing || harness.git.createCalls != 0 {
		t.Fatalf("after intent = %s, creates %d", projected.Phase(), harness.git.createCalls)
	}

	_, err = workspace.MaterializeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:03:00Z"),
			Fault: failAt(workspace.AttemptFaultAfterWorktreeCreation, crash),
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("worktree creation crash = %v", err)
	}
	projected = mustRuntimeAttempt(t, harness.journal, attempt.AttemptID())
	if projected.Phase() != workspace.AttemptMaterializing || harness.git.createCalls != 1 {
		t.Fatalf("after creation = %s, creates %d", projected.Phase(), harness.git.createCalls)
	}

	_, err = workspace.MaterializeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:04:00Z"),
			Fault: failAt(workspace.AttemptFaultAfterGitVerification, crash),
		},
	)
	if !errors.Is(err, crash) || harness.git.createCalls != 1 {
		t.Fatalf("verification crash = %v, creates %d", err, harness.git.createCalls)
	}
	_, err = workspace.MaterializeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:05:00Z"),
			Fault: failAt(workspace.AttemptFaultAfterStart, crash),
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("start crash = %v", err)
	}
	started := mustRuntimeAttempt(t, harness.journal, attempt.AttemptID())
	if started.Phase() != workspace.AttemptActive || started.VerifiedHead() != harness.base ||
		started.LeaseID().IsZero() || harness.git.createCalls != 1 {
		t.Fatalf("started attempt = %#v, creates %d", started, harness.git.createCalls)
	}
	startedAgain := harness.materialize(t, attempt.AttemptID(), "2026-07-21T10:06:00Z")
	if startedAgain.StartRecord() != started.StartRecord() || harness.git.createCalls != 1 {
		t.Fatal("materialization retry duplicated the durable start or Git creation")
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.VerifyWorkspaceRuntimeConformance(snapshot, harness.definition.Generation()); err != nil {
		t.Fatalf("attempt replay conformance: %v", err)
	}
}

func TestAttemptMaterializationRecoversClaimedUnregisteredPartialWorktree(t *testing.T) {
	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T10:10:00Z")
	harness.git.createErr = errors.New("Git terminated after creating a partial checkout")
	harness.git.partialOnCreateError = true
	if _, err := workspace.MaterializeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:11:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "partial checkout") {
		t.Fatalf("partial materialization failure = %v", err)
	}
	partial := harness.git.inspection
	if !partial.WorktreeExists() || partial.WorktreeRegistered() || harness.git.claim == nil {
		t.Fatalf("partial worktree did not retain its durable ownership claim: %#v", partial)
	}

	harness.git.createErr = nil
	harness.git.partialOnCreateError = false
	started := harness.materialize(t, attempt.AttemptID(), "2026-07-21T10:12:00Z")
	if started.Phase() != workspace.AttemptActive || harness.git.recoveryCalls != 1 || harness.git.claim != nil {
		t.Fatalf("partial worktree recovery = %#v, recoveries %d, claim %#v", started, harness.git.recoveryCalls, harness.git.claim)
	}
}

func TestAttemptWorktreeClaimPublicationIsAtomicAcrossCrashPoints(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	runGitSetup(t, "", "init", "--initial-branch=main", repositoryRoot)
	base, err := workspace.ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("simulated claim publication crash")
	points := []workspace.AttemptWorktreeClaimFaultPoint{
		workspace.AttemptWorktreeClaimFaultAfterTemporaryCreated,
		workspace.AttemptWorktreeClaimFaultAfterTemporaryWritten,
		workspace.AttemptWorktreeClaimFaultAfterTemporarySynced,
		workspace.AttemptWorktreeClaimFaultAfterPublished,
	}
	for _, point := range points {
		t.Run(string(point), func(t *testing.T) {
			worktree := filepath.Join(t.TempDir(), "attempt-worktree")
			claim, err := workspace.NewAttemptWorktreeClaim(
				workspace.MustID("attempt-claim-test"), workspace.DigestBytes([]byte("generation")),
				base, "mu/claim-test-a1-123456789abc", worktree,
			)
			if err != nil {
				t.Fatal(err)
			}
			fired := false
			adapter := workspace.DefaultLocalAttemptGitAdapter().WithAttemptWorktreeClaimFaultInjector(
				func(actual workspace.AttemptWorktreeClaimFaultPoint) error {
					if actual == point && !fired {
						fired = true
						return crash
					}
					return nil
				},
			)
			if err := adapter.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, false,
			); !errors.Is(err, crash) {
				t.Fatalf("claim fault at %s = %v", point, err)
			}
			marker := worktree + ".feature-attempt-claim"
			_, markerErr := os.Lstat(marker)
			if point == workspace.AttemptWorktreeClaimFaultAfterPublished {
				if markerErr != nil {
					t.Fatalf("published complete claim is missing after fault: %v", markerErr)
				}
			} else if !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatalf("partial claim became visible at %s: %v", point, markerErr)
			}

			adapter = workspace.DefaultLocalAttemptGitAdapter()
			if err := adapter.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, false,
			); err != nil {
				t.Fatalf("claim retry after %s: %v", point, err)
			}
			if err := adapter.ReleaseAttemptWorktreeClaim(context.Background(), claim); err != nil {
				t.Fatalf("release claim after %s: %v", point, err)
			}
			if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("claim marker remained after retry at %s: %v", point, err)
			}
		})
	}
}

func TestAttemptWorktreeClaimRecoversCrashStrandedPendingFile(t *testing.T) {
	const (
		pendingPathEnvironment = "FEATURE_TEST_ATTEMPT_CLAIM_PENDING_PATH"
		pendingModeEnvironment = "FEATURE_TEST_ATTEMPT_CLAIM_PENDING_MODE"
	)
	if pending := os.Getenv(pendingPathEnvironment); pending != "" {
		file, err := os.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "create crash-stranded pending claim: %v\n", err)
			os.Exit(2)
		}
		if os.Getenv(pendingModeEnvironment) == "partial" {
			if _, err := file.Write([]byte(`{"schema_version":3,"kind":"attempt_`)); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "write partial pending claim: %v\n", err)
				os.Exit(2)
			}
		}
		if err := file.Sync(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "synchronize crash-stranded pending claim: %v\n", err)
			os.Exit(2)
		}
		os.Exit(0)
	}

	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	runGitSetup(t, "", "init", "--initial-branch=main", repositoryRoot)
	base, err := workspace.ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"empty", "partial"} {
		t.Run(mode, func(t *testing.T) {
			worktree := filepath.Join(t.TempDir(), "attempt-worktree")
			claim, err := workspace.NewAttemptWorktreeClaim(
				workspace.MustID("attempt-crash-pending-"+mode),
				workspace.DigestBytes([]byte("generation-"+mode)),
				base,
				"mu/claim-crash-pending-"+mode+"-a1-123456789abc",
				worktree,
			)
			if err != nil {
				t.Fatal(err)
			}
			pending := worktree + ".feature-attempt-claim.pending"
			command := exec.Command(
				os.Args[0],
				"-test.run=^TestAttemptWorktreeClaimRecoversCrashStrandedPendingFile$",
			)
			command.Env = append(
				os.Environ(),
				pendingPathEnvironment+"="+pending,
				pendingModeEnvironment+"="+mode,
			)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("crash helper: %v\n%s", err, output)
			}
			if _, err := os.Lstat(pending); err != nil {
				t.Fatalf("crash helper did not leave pending claim: %v", err)
			}

			adapter := workspace.DefaultLocalAttemptGitAdapter()
			if err := adapter.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, false,
			); err != nil {
				t.Fatalf("recover %s pending claim: %v", mode, err)
			}
			if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovered pending claim remains: %v", err)
			}
			marker := worktree + ".feature-attempt-claim"
			if _, err := os.Lstat(marker); err != nil {
				t.Fatalf("recovered claim was not published: %v", err)
			}
			if err := adapter.ReleaseAttemptWorktreeClaim(
				context.Background(), claim,
			); err != nil {
				t.Fatalf("release recovered claim: %v", err)
			}
		})
	}
}

func TestAttemptWorktreeClaimDoesNotReclaimActivePendingFile(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	runGitSetup(t, "", "init", "--initial-branch=main", repositoryRoot)
	base, err := workspace.ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "attempt-worktree")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("attempt-active-pending"),
		workspace.DigestBytes([]byte("active-pending-generation")),
		base,
		"mu/claim-active-pending-a1-123456789abc",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := worktree + ".feature-attempt-claim.pending"
	file, err := os.OpenFile(pending, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte(`{"schema_version":3`)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		}
	}()

	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err == nil || !strings.Contains(err.Error(), "pending attempt worktree claim") ||
		!strings.Contains(err.Error(), "is active") {
		t.Fatalf("active pending claim error = %v", err)
	}
	if _, err := os.Lstat(pending); err != nil {
		t.Fatalf("active pending claim was removed: %v", err)
	}
	if _, err := os.Lstat(worktree + ".feature-attempt-claim"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active pending claim was published: %v", err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	locked = false
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatalf("recover released pending claim: %v", err)
	}
	if err := adapter.ReleaseAttemptWorktreeClaim(
		context.Background(), claim,
	); err != nil {
		t.Fatalf("release recovered active claim: %v", err)
	}
}

func TestAttemptWorktreeCreationRejectsReplacedClaimParent(t *testing.T) {
	parent := t.TempDir()
	repositoryRoot := filepath.Join(parent, "repository")
	runGitSetup(t, "", "init", "--initial-branch=main", repositoryRoot)
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
	baseText := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD")))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}

	worktreeParent := filepath.Join(parent, "attempts")
	if err := os.Mkdir(worktreeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(worktreeParent, "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("attempt-parent-binding"),
		workspace.DigestBytes([]byte("generation")),
		base,
		"mu/parent-binding-a1-123456789abc",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	marker := worktree + ".feature-attempt-claim"
	markerContent, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	movedParent := worktreeParent + "-moved"
	if err := os.Rename(worktreeParent, movedParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktreeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, markerContent, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	); err == nil || !strings.Contains(err.Error(), "does not bind the verified parent root") {
		t.Fatalf("replaced claim parent creation error = %v", err)
	}
	if branches := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "branch", "--list", claim.Branch()),
	)); branches != "" {
		t.Fatalf("replaced claim parent created branch: %q", branches)
	}
}

func TestAttemptWorktreeAdmissionRejectsGitOwnedRootsBeforeMutation(t *testing.T) {
	repositoryRoot, linkedRoot, base := newRealAttemptRepository(t)
	fixture := newDefinitionFixture(t)
	workspaceSource := strings.Split(string(fixture.sources.Workspace.Bytes), "\n")
	for index, line := range workspaceSource {
		if strings.HasPrefix(line, "  root: ") {
			workspaceSource[index] = "  root: " + repositoryRoot
		}
		if strings.HasPrefix(line, "base_commit: ") {
			workspaceSource[index] = "base_commit: " + base.String()
		}
	}
	fixture.sources.Workspace.Bytes = []byte(strings.Join(workspaceSource, "\n"))
	definition := mustDefinition(t, fixture.sources)
	cases := map[string]string{
		"repository-root": repositoryRoot,
		"git-common-root": filepath.Join(repositoryRoot, ".git"),
		"linked-worktree": linkedRoot,
	}
	for name, worktreeRoot := range cases {
		t.Run(name, func(t *testing.T) {
			beforeEntries := directoryEntryNames(t, worktreeRoot)
			if _, err := workspace.InitializeWorkspaceV2WithOptions(
				context.Background(), t.TempDir(), definition,
				mustTime(t, "2026-07-21T10:15:00Z"),
				workspace.WorkspaceInitializationOptions{
					WorktreeRoot: worktreeRoot,
				},
			); err == nil || !strings.Contains(err.Error(), "unsafe workspace root overlap") {
				t.Fatalf("Git-owned initialization worktree root error = %v", err)
			}
			if afterEntries := directoryEntryNames(t, worktreeRoot); !slices.Equal(
				beforeEntries, afterEntries,
			) {
				t.Fatalf(
					"rejected initialization worktree root was mutated: before=%v after=%v",
					beforeEntries, afterEntries,
				)
			}

			originalInfo, err := os.Stat(worktreeRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(worktreeRoot, 0o555); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chmod(worktreeRoot, originalInfo.Mode().Perm()); err != nil {
					t.Errorf("restore worktree-root permissions: %v", err)
				}
			}()

			faultFired := false
			adapter := workspace.DefaultLocalAttemptGitAdapter().
				WithAttemptWorktreeClaimFaultInjector(
					func(workspace.AttemptWorktreeClaimFaultPoint) error {
						faultFired = true
						return errors.New("claim publication must not start")
					},
				)
			worktree := filepath.Join(worktreeRoot, "rejected-attempt")
			claim, err := workspace.NewAttemptWorktreeClaim(
				workspace.MustID("rejected-"+name),
				workspace.DigestBytes([]byte("root-admission-generation")),
				base,
				"mu/root-admission-"+name+"-a1",
				worktree,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, false,
			); err == nil || !strings.Contains(err.Error(), "unsafe attempt worktree overlap") {
				t.Fatalf("Git-owned worktree root preparation error = %v", err)
			}
			if faultFired {
				t.Fatal("rejected worktree root reached claim publication")
			}
			if afterEntries := directoryEntryNames(t, worktreeRoot); !slices.Equal(
				beforeEntries, afterEntries,
			) {
				t.Fatalf(
					"rejected worktree root was mutated: before=%v after=%v",
					beforeEntries, afterEntries,
				)
			}
			for _, suffix := range []string{
				".feature-attempt-claim",
				".feature-attempt-claim.pending",
			} {
				if _, err := os.Lstat(worktree + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected worktree root published %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestAttemptWorktreeAdmissionAllowsSafeExternalRoot(t *testing.T) {
	repositoryRoot, _, base := newRealAttemptRepository(t)
	fixture := newDefinitionFixture(t)
	workspaceSource := strings.Split(string(fixture.sources.Workspace.Bytes), "\n")
	for index, line := range workspaceSource {
		if strings.HasPrefix(line, "  root: ") {
			workspaceSource[index] = "  root: " + repositoryRoot
		}
		if strings.HasPrefix(line, "base_commit: ") {
			workspaceSource[index] = "base_commit: " + base.String()
		}
	}
	fixture.sources.Workspace.Bytes = []byte(strings.Join(workspaceSource, "\n"))
	definition := mustDefinition(t, fixture.sources)
	worktreeRoot := canonicalTestDirectory(t)
	runtimeRoot := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), runtimeRoot, definition,
		mustTime(t, "2026-07-21T10:17:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
	); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		runtimeRoot, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("safe-root-goal"), workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	attempt, err := workspace.ReserveAttempt(
		context.Background(), journal, definition, adapter,
		workspace.ReserveAttemptRequest{
			MergeUnit:     mustMergeUnitReference(t, "alpha-plan", "unit-one"),
			AttemptNumber: 1,
			Goal:          goal,
			OccurredAt:    mustTime(t, "2026-07-21T10:18:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := workspace.MaterializeAttempt(
		context.Background(), journal, definition, adapter,
		workspace.MaterializeAttemptRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-21T10:19:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if started.Phase() != workspace.AttemptActive ||
		filepath.Dir(started.Worktree()) != worktreeRoot {
		t.Fatalf("safe external worktree root materialization = %#v", started)
	}
}

func TestLocalGitAttemptMaterializationPreservesDirtyPrimaryCheckout(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	remoteRoot := filepath.Join(t.TempDir(), "remote.git")
	runGitSetup(t, "", "init", "--initial-branch=main", repositoryRoot)
	runGitSetup(t, "", "init", "--bare", remoteRoot)
	runGitSetup(t, repositoryRoot, "config", "user.name", "Attempt Test")
	runGitSetup(t, repositoryRoot, "config", "user.email", "attempt@example.invalid")
	tracked := filepath.Join(repositoryRoot, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "tracked.txt")
	runGitSetup(t, repositoryRoot, "commit", "-m", "initial")
	runGitSetup(t, repositoryRoot, "remote", "add", "origin", remoteRoot)
	runGitSetup(t, repositoryRoot, "push", "-u", "origin", "main")
	baseText := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD")))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("dirty primary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "untracked.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	primaryBefore := runGitSetup(t, repositoryRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if len(primaryBefore) == 0 {
		t.Fatal("primary checkout fixture is not dirty")
	}

	fixture := newDefinitionFixture(t)
	workspaceSource := strings.Split(string(fixture.sources.Workspace.Bytes), "\n")
	for index, line := range workspaceSource {
		if strings.HasPrefix(line, "  root: ") {
			workspaceSource[index] = "  root: " + repositoryRoot
		}
		if strings.HasPrefix(line, "base_commit: ") {
			workspaceSource[index] = "base_commit: " + base.String()
		}
	}
	fixture.sources.Workspace.Bytes = []byte(strings.Join(workspaceSource, "\n"))
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	worktreeRoot := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), workspaceDir, definition,
		mustTime(t, "2026-07-21T10:20:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
	); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	goal, _ := workspace.NewGoalBinding(workspace.MustID("local-git-goal"), workspace.GoalScopeMergeUnit)
	attempt, err := workspace.ReserveAttempt(
		context.Background(), journal, definition, adapter,
		workspace.ReserveAttemptRequest{
			MergeUnit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), AttemptNumber: 1,
			Goal: goal, OccurredAt: mustTime(t, "2026-07-21T10:21:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := workspace.NewAttemptWorktreeClaim(
		attempt.AttemptID(), attempt.Generation(), attempt.Base(), attempt.Branch(), attempt.Worktree(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(attempt.Worktree(), 0o755); err != nil {
		t.Fatal(err)
	}
	unowned := filepath.Join(attempt.Worktree(), "unowned.txt")
	if err := os.WriteFile(unowned, []byte("must survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, true,
	); err == nil ||
		!strings.Contains(err.Error(), "predates its ownership claim") {
		t.Fatalf("unowned worktree path was accepted for recovery: %v", err)
	}
	if content, err := os.ReadFile(unowned); err != nil || string(content) != "must survive\n" {
		t.Fatalf("unowned worktree content changed: %q, %v", content, err)
	}
	if err := os.RemoveAll(attempt.Worktree()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(attempt.Worktree(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, true,
	); err != nil {
		t.Fatalf("recover empty unbound worktree: %v", err)
	}
	if _, err := os.Lstat(attempt.Worktree()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty unbound worktree still exists: %v", err)
	}
	if err := os.MkdirAll(attempt.Worktree(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attempt.Worktree(), "partial.txt"), []byte("partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, true,
	); err == nil || !strings.Contains(err.Error(), "without a durable directory identity") {
		t.Fatalf("unbound partial worktree recovery error = %v", err)
	}
	if content, err := os.ReadFile(
		filepath.Join(attempt.Worktree(), "partial.txt"),
	); err != nil || string(content) != "partial\n" {
		t.Fatalf("unbound partial worktree was modified: %q, %v", content, err)
	}
	if err := os.RemoveAll(attempt.Worktree()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatalf("revalidate absent partial worktree: %v", err)
	}
	if err := adapter.ReleaseAttemptWorktreeClaim(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crash after real Git creation")
	if _, err := workspace.MaterializeAttempt(
		context.Background(), journal, definition, adapter,
		workspace.MaterializeAttemptRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:22:00Z"),
			Fault: failAt(workspace.AttemptFaultAfterWorktreeCreation, crash),
		},
	); !errors.Is(err, crash) {
		t.Fatalf("real Git creation crash = %v", err)
	}
	if err := os.Rename(attempt.Worktree(), attempt.Worktree()+"-interrupted"); err != nil {
		t.Fatalf("simulate interrupted registered checkout: %v", err)
	}
	started, err := workspace.MaterializeAttempt(
		context.Background(), journal, definition, adapter,
		workspace.MaterializeAttemptRequest{AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:23:00Z")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if started.Phase() != workspace.AttemptActive || started.VerifiedHead() != base {
		t.Fatalf("real Git attempt = %#v", started)
	}
	if _, err := os.Lstat(attempt.Worktree() + "-interrupted"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("displaced registered worktree was not restored exactly: %v", err)
	}
	if _, err := os.Lstat(attempt.Worktree() + ".feature-attempt-claim"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed materialization retained its ownership claim: %v", err)
	}
	primaryAfter := runGitSetup(t, repositoryRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if !bytes.Equal(primaryBefore, primaryAfter) {
		t.Fatalf("primary checkout changed:\nbefore %q\nafter  %q", primaryBefore, primaryAfter)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "--abbrev-ref", "HEAD"))); got != "main" {
		t.Fatalf("primary checkout switched to %q", got)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, started.Worktree(), "rev-parse", "--abbrev-ref", "HEAD"))); got != started.Branch() {
		t.Fatalf("attempt worktree branch = %q, expected %q", got, started.Branch())
	}
}

func TestAttemptWorktreeRejectsLateConfiguredSmudgeFilterWithoutInvocation(t *testing.T) {
	repositoryRoot := canonicalTestDirectory(t)
	runGitSetup(t, repositoryRoot, "init", "--initial-branch=main", ".")
	payload := filepath.Join(repositoryRoot, "payload.txt")
	if err := os.WriteFile(payload, []byte("raw payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "--", "payload.txt")
	runGitSetup(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "add later hostile attributes",
	)
	baseText := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "rev-parse", "HEAD"),
	))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}

	probeDirectory := canonicalTestDirectory(t)
	probe := filepath.Join(probeDirectory, "smudge-probe")
	marker := filepath.Join(probeDirectory, "smudge-filter-invoked")
	if err := os.WriteFile(
		probe,
		[]byte("#!/bin/sh\n: > "+shellSingleQuote(marker)+"\ncat\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(probeDirectory, "git-wrapper")
	wrapperScript := "#!/bin/sh\n" +
		"repository=\n" +
		"previous=\n" +
		"late_config=false\n" +
		"for argument in \"$@\"; do\n" +
		"  if [ \"$previous\" = \"-C\" ]; then repository=$argument; fi\n" +
		"  if [ \"$previous\" = \"worktree\" ] && [ \"$argument\" = \"add\" ]; then late_config=true; fi\n" +
		"  previous=$argument\n" +
		"done\n" +
		"if [ \"$late_config\" = \"true\" ]; then\n" +
		"  " + shellSingleQuote(realGit) + " -C \"$repository\" config filter.hostile.smudge " +
		shellSingleQuote(probe) + "\n" +
		"fi\n" +
		"exec " + shellSingleQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := workspace.NewLocalAttemptGitAdapter(wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("late-filter-attempt"),
		workspace.DigestBytes([]byte("late-filter-generation")),
		base,
		"mu/late-filter-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	err = adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "external Git filter") {
		t.Fatalf("late smudge-filter checkout error = %v", err)
	}
	if _, statErr := os.Lstat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("late configured smudge filter was invoked: %v", statErr)
	}
}

func TestAttemptWorktreeRevalidatesLateGitCommonAttributes(t *testing.T) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	attributes := filepath.Join(repositoryRoot, ".git", "info", "attributes")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	probeDirectory := canonicalTestDirectory(t)
	wrapper := filepath.Join(probeDirectory, "git-wrapper")
	wrapperScript := "#!/bin/sh\n" +
		"worktree_add=false\n" +
		"previous=\n" +
		"for argument in \"$@\"; do\n" +
		"  if [ \"$previous\" = \"worktree\" ] && [ \"$argument\" = \"add\" ]; then worktree_add=true; fi\n" +
		"  previous=$argument\n" +
		"done\n" +
		shellSingleQuote(realGit) + " \"$@\"\n" +
		"status=$?\n" +
		"if [ \"$status\" -eq 0 ] && [ \"$worktree_add\" = \"true\" ]; then\n" +
		"  printf '%s\\n' '*.txt text eol=crlf' > " + shellSingleQuote(attributes) + "\n" +
		"fi\n" +
		"exit \"$status\"\n"
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := workspace.NewLocalAttemptGitAdapter(wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("late-common-attributes-attempt"),
		workspace.DigestBytes([]byte("late-common-attributes-generation")),
		base,
		"mu/late-common-attributes-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	err = adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "external Git attributes metadata") {
		t.Fatalf("late common attributes creation error = %v", err)
	}
	if err := os.Remove(attributes); err != nil {
		t.Fatal(err)
	}
	adapter = workspace.DefaultLocalAttemptGitAdapter()
	if err := adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, false, true,
	); err != nil {
		t.Fatalf("recover after late common attributes: %v", err)
	}
	if err := os.WriteFile(
		attributes, []byte("*.txt text eol=crlf\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	); err == nil ||
		!strings.Contains(err.Error(), "external Git attributes metadata") {
		t.Fatalf("late common attributes inspection error = %v", err)
	}
}

func TestAttemptWorktreeRejectsTransformingRepositoryAttributesBeforePublication(
	t *testing.T,
) {
	repositoryRoot := canonicalTestDirectory(t)
	runGitSetup(t, repositoryRoot, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, ".gitattributes"),
		[]byte("payload.txt working-tree-encoding=UTF-16LE\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	for _, value := range []byte("raw payload\n") {
		encoded = append(encoded, value, 0)
	}
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "payload.txt"), encoded, 0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "--", ".gitattributes", "payload.txt")
	runGitSetup(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "add transforming attributes",
	)
	baseText := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "rev-parse", "HEAD"),
	))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("transforming-attributes-attempt"),
		workspace.DigestBytes([]byte("transforming-attributes-generation")),
		base,
		"mu/transforming-attributes-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	err = adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "repository-defined .gitattributes") {
		t.Fatalf("transforming attributes materialization error = %v", err)
	}
	for _, relative := range []string{".gitattributes", "payload.txt"} {
		if _, statErr := os.Lstat(
			filepath.Join(worktree, relative),
		); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("rejected attribute tree published %s: %v", relative, statErr)
		}
	}
	rejected, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	)
	if err != nil || rejected.Clean() {
		t.Fatalf("rejected transforming-attribute attempt = %#v, %v", rejected, err)
	}
	checkedWorktree := filepath.Join(canonicalTestDirectory(t), "checked-attempt")
	checkedBranch := "attribute-inspection"
	runGitSetup(
		t, repositoryRoot,
		"worktree", "add", "-b", checkedBranch, checkedWorktree, "HEAD",
	)
	if _, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, checkedBranch, checkedWorktree,
	); err == nil ||
		!strings.Contains(err.Error(), "repository-defined .gitattributes") {
		t.Fatalf("transforming attributes inspection error = %v", err)
	}
}

func TestAttemptWorktreeMaterializesExactRawTreeWithoutCheckoutPrograms(t *testing.T) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	probeDirectory := canonicalTestDirectory(t)
	hookMarker := filepath.Join(probeDirectory, "post-checkout-invoked")
	hook := filepath.Join(repositoryRoot, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(
		hook,
		[]byte("#!/bin/sh\n: > "+shellSingleQuote(hookMarker)+"\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	monitorMarker := filepath.Join(probeDirectory, "fsmonitor-invoked")
	monitor := filepath.Join(probeDirectory, "fsmonitor-probe")
	if err := os.WriteFile(
		monitor,
		[]byte("#!/bin/sh\n: > "+shellSingleQuote(monitorMarker)+"\nexit 1\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "config", "core.fsmonitor", monitor)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	checkoutMarker := filepath.Join(probeDirectory, "checkout-enabled")
	wrapper := filepath.Join(probeDirectory, "git-wrapper")
	wrapperScript := "#!/bin/sh\n" +
		"worktree_add=false\n" +
		"no_checkout=false\n" +
		"previous=\n" +
		"for argument in \"$@\"; do\n" +
		"  if [ \"$previous\" = \"worktree\" ] && [ \"$argument\" = \"add\" ]; then worktree_add=true; fi\n" +
		"  if [ \"$argument\" = \"--no-checkout\" ]; then no_checkout=true; fi\n" +
		"  previous=$argument\n" +
		"done\n" +
		"if [ \"$worktree_add\" = \"true\" ] && [ \"$no_checkout\" != \"true\" ]; then\n" +
		"  : > " + shellSingleQuote(checkoutMarker) + "\n" +
		"fi\n" +
		"exec " + shellSingleQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := workspace.NewLocalAttemptGitAdapter(wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("exact-raw-attempt"),
		workspace.DigestBytes([]byte("exact-raw-generation")),
		base,
		"mu/exact-raw-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	); err != nil {
		t.Fatal(err)
	}
	for name, marker := range map[string]string{
		"checkout-enabled worktree add": checkoutMarker,
		"configured fsmonitor":          monitorMarker,
		"post-checkout hook":            hookMarker,
	} {
		if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s ran during raw materialization: %v", name, err)
		}
	}
	if content, err := os.ReadFile(filepath.Join(worktree, "payload.txt")); err != nil ||
		string(content) != "raw payload\n" {
		t.Fatalf("raw payload = %q, %v", content, err)
	}
	executable, err := os.Stat(filepath.Join(worktree, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if executable.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable mode = %s", executable.Mode())
	}
	if target, err := os.Readlink(filepath.Join(worktree, "links", "payload")); err != nil ||
		target != "../payload.txt" {
		t.Fatalf("safe symlink target = %q, %v", target, err)
	}
	expectedTree := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "rev-parse", base.String()[len("sha1:"):]+"^{tree}"),
	))
	if indexTree := strings.TrimSpace(string(
		runGitSetup(t, worktree, "write-tree"),
	)); indexTree != expectedTree {
		t.Fatalf("attempt index tree = %s, expected %s", indexTree, expectedTree)
	}
	index := runGitSetup(t, worktree, "ls-files", "-z", "--")
	for _, expected := range [][]byte{
		[]byte("links/payload\x00"),
		[]byte("payload.txt\x00"),
		[]byte("script.sh\x00"),
	} {
		if !bytes.Contains(index, expected) {
			t.Fatalf("populated attempt index %q does not contain %q", index, expected)
		}
	}
	inspection, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	)
	if err != nil || !inspection.Clean() || inspection.WorktreeHead() != base {
		t.Fatalf("exact raw attempt inspection = %#v, %v", inspection, err)
	}
	if status := runGitSetup(
		t, worktree, "status", "--porcelain=v1", "-z",
	); len(status) != 0 {
		t.Fatalf(
			"ordinary Git status disagrees with clean attempt inspection: %q",
			status,
		)
	}
	if err := adapter.ReleaseAttemptWorktreeClaim(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "config", "--unset", "core.fsmonitor")
	if err := os.WriteFile(
		filepath.Join(worktree, "payload.txt"), []byte("ordinary edit\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	edited, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	)
	if err != nil || edited.Clean() {
		t.Fatalf("ordinary edit inspection = %#v, %v", edited, err)
	}
	if status := runGitSetup(t, worktree, "status", "--porcelain=v1", "-z"); len(status) == 0 {
		t.Fatal("ordinary Git status did not observe the edit")
	}
	runGitSetup(t, worktree, "add", "--", "payload.txt")
	runGitSetup(
		t, worktree,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "ordinary attempt edit",
	)
	committed, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	)
	if err != nil || !committed.Clean() || committed.WorktreeHead() == base {
		t.Fatalf("ordinary attempt commit inspection = %#v, %v", committed, err)
	}
}

func TestAttemptWorktreeStreamsBlobLargerThanBufferedGitOutputLimit(t *testing.T) {
	repositoryRoot, _ := newRawAttemptTreeRepository(t)
	content := bytes.Repeat([]byte{0xa5}, 8*1024*1024+1)
	largePath := filepath.Join(repositoryRoot, "large.bin")
	if err := os.WriteFile(largePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "--", "large.bin")
	runGitSetup(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "add large raw blob",
	)
	baseText := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "rev-parse", "HEAD"),
	))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("large-blob-attempt"),
		workspace.DigestBytes([]byte("large-blob-generation")),
		base,
		"mu/large-blob-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	); err != nil {
		t.Fatal(err)
	}
	materialized, err := os.ReadFile(filepath.Join(worktree, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(materialized, content) {
		t.Fatalf(
			"large raw blob differs: got %d bytes, want %d",
			len(materialized), len(content),
		)
	}
	inspection, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	)
	if err != nil || !inspection.Clean() || inspection.WorktreeHead() != base {
		t.Fatalf("large raw attempt inspection = %#v, %v", inspection, err)
	}
}

func TestAttemptWorktreeRecoversInterruptedRawPublication(t *testing.T) {
	crash := errors.New("simulated raw materialization interruption")
	for _, point := range []workspace.AttemptWorktreeMaterializationFaultPoint{
		workspace.AttemptMaterializationFaultAfterDirectoryBinding,
		workspace.AttemptMaterializationFaultAfterRegistration,
		workspace.AttemptMaterializationFaultAfterIndex,
		workspace.AttemptMaterializationFaultAfterPath,
	} {
		t.Run(string(point), func(t *testing.T) {
			repositoryRoot, base := newRawAttemptTreeRepository(t)
			worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
			pointID := strings.ReplaceAll(string(point), "_", "-")
			claim, err := workspace.NewAttemptWorktreeClaim(
				workspace.MustID("interrupted-"+pointID),
				workspace.DigestBytes([]byte("interrupted-raw-generation")),
				base,
				"mu/interrupted-"+pointID,
				worktree,
			)
			if err != nil {
				t.Fatal(err)
			}
			adapter := workspace.DefaultLocalAttemptGitAdapter().
				WithAttemptWorktreeMaterializationFaultInjector(
					func(observed workspace.AttemptWorktreeMaterializationFaultPoint) error {
						if observed == point {
							return crash
						}
						return nil
					},
				)
			if err := adapter.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, false,
			); err != nil {
				t.Fatal(err)
			}
			if err := adapter.CreateAttemptWorktree(
				context.Background(), repositoryRoot, claim, true, false,
			); !errors.Is(err, crash) {
				t.Fatalf("raw materialization interruption = %v", err)
			}
			if _, err := os.Lstat(worktree + ".feature-attempt-claim"); err != nil {
				t.Fatalf("interrupted materialization lost its ownership claim: %v", err)
			}
			recovery := workspace.DefaultLocalAttemptGitAdapter()
			if point == workspace.AttemptMaterializationFaultAfterDirectoryBinding {
				if err := recovery.PrepareAttemptWorktree(
					context.Background(), repositoryRoot, claim, true,
				); err != nil {
					t.Fatalf("recover interrupted directory binding: %v", err)
				}
				if err := recovery.CreateAttemptWorktree(
					context.Background(), repositoryRoot, claim, true, false,
				); err != nil {
					t.Fatalf("restart after interrupted directory binding: %v", err)
				}
			} else {
				if err := recovery.CreateAttemptWorktree(
					context.Background(), repositoryRoot, claim, false, true,
				); err != nil {
					t.Fatalf("recover interrupted raw materialization: %v", err)
				}
			}
			inspection, err := recovery.InspectAttemptWorktree(
				context.Background(), repositoryRoot, claim.Branch(), worktree,
			)
			if err != nil || !inspection.Clean() || inspection.WorktreeHead() != base {
				t.Fatalf("recovered raw materialization = %#v, %v", inspection, err)
			}
		})
	}
}

func TestAttemptWorktreePartialRecoveryIsRetryableAcrossOrderedCleanupCrashes(
	t *testing.T,
) {
	crash := errors.New("simulated ordered cleanup crash")
	for _, point := range []workspace.AttemptWorktreeCleanupFaultPoint{
		workspace.AttemptCleanupFaultAfterRecoveryContents,
		workspace.AttemptCleanupFaultAfterRecoveryBinding,
	} {
		t.Run(string(point), func(t *testing.T) {
			repositoryRoot, base := newRawAttemptTreeRepository(t)
			worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
			claim, err := workspace.NewAttemptWorktreeClaim(
				workspace.MustID("ordered-cleanup-"+strings.ReplaceAll(string(point), "_", "-")),
				workspace.DigestBytes([]byte("ordered-cleanup-generation")),
				base,
				"mu/ordered-cleanup-"+strings.ReplaceAll(string(point), "_", "-"),
				worktree,
			)
			if err != nil {
				t.Fatal(err)
			}
			interrupted := workspace.DefaultLocalAttemptGitAdapter().
				WithAttemptWorktreeMaterializationFaultInjector(
					func(observed workspace.AttemptWorktreeMaterializationFaultPoint) error {
						if observed == workspace.AttemptMaterializationFaultAfterDirectoryBinding {
							return crash
						}
						return nil
					},
				)
			if err := interrupted.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, false,
			); err != nil {
				t.Fatal(err)
			}
			if err := interrupted.CreateAttemptWorktree(
				context.Background(), repositoryRoot, claim, true, false,
			); !errors.Is(err, crash) {
				t.Fatalf("directory-binding interruption = %v", err)
			}
			partial := filepath.Join(worktree, "nested", "partial.txt")
			if err := os.MkdirAll(filepath.Dir(partial), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(partial, []byte("partial\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fired := false
			recovery := workspace.DefaultLocalAttemptGitAdapter().
				WithAttemptWorktreeCleanupFaultInjector(
					func(observed workspace.AttemptWorktreeCleanupFaultPoint) error {
						if observed == point && !fired {
							fired = true
							return crash
						}
						return nil
					},
				)
			if err := recovery.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, true,
			); !errors.Is(err, crash) {
				t.Fatalf("ordered cleanup interruption at %s = %v", point, err)
			}
			if !fired {
				t.Fatalf("ordered cleanup fault %s did not fire", point)
			}
			if _, err := os.Lstat(partial); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial content remains after %s: %v", point, err)
			}
			if info, err := os.Stat(worktree); err != nil || !info.IsDir() {
				t.Fatalf("bound directory missing after %s: %v", point, err)
			}
			binding := worktree + ".feature-attempt-claim.binding"
			_, bindingErr := os.Lstat(binding)
			if point == workspace.AttemptCleanupFaultAfterRecoveryContents {
				if bindingErr != nil {
					t.Fatalf("binding missing after content-only cleanup: %v", bindingErr)
				}
			} else if !errors.Is(bindingErr, os.ErrNotExist) {
				t.Fatalf("binding remains after binding cleanup: %v", bindingErr)
			}

			recovery = workspace.DefaultLocalAttemptGitAdapter()
			if err := recovery.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, true,
			); err != nil {
				t.Fatalf("retry ordered cleanup after %s: %v", point, err)
			}
			if _, err := os.Lstat(worktree); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cleared worktree remains after retry at %s: %v", point, err)
			}
			if _, err := os.Lstat(binding); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("binding remains after retry at %s: %v", point, err)
			}
			if err := recovery.CreateAttemptWorktree(
				context.Background(), repositoryRoot, claim, true, false,
			); err != nil {
				t.Fatalf("restart materialization after %s: %v", point, err)
			}
			inspection, err := recovery.InspectAttemptWorktree(
				context.Background(), repositoryRoot, claim.Branch(), worktree,
			)
			if err != nil || !inspection.Clean() {
				t.Fatalf(
					"restarted materialization after %s = %#v, %v",
					point, inspection, err,
				)
			}
		})
	}
}

func TestAttemptWorktreeRecoveryPreservesBoundDirectoryMovedOutsideParent(
	t *testing.T,
) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	displaced := filepath.Join(canonicalTestDirectory(t), "displaced-attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("moved-during-cleanup-attempt"),
		workspace.DigestBytes([]byte("moved-during-cleanup-generation")),
		base,
		"mu/moved-during-cleanup-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("stop after directory binding")
	interrupted := workspace.DefaultLocalAttemptGitAdapter().
		WithAttemptWorktreeMaterializationFaultInjector(
			func(observed workspace.AttemptWorktreeMaterializationFaultPoint) error {
				if observed == workspace.AttemptMaterializationFaultAfterDirectoryBinding {
					return crash
				}
				return nil
			},
		)
	if err := interrupted.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	if err := interrupted.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	); !errors.Is(err, crash) {
		t.Fatalf("directory-binding interruption = %v", err)
	}
	sentinel := filepath.Join(worktree, "must-survive.txt")
	if err := os.WriteFile(sentinel, []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := false
	recovery := workspace.DefaultLocalAttemptGitAdapter().
		WithAttemptWorktreeCleanupFaultInjector(
			func(point workspace.AttemptWorktreeCleanupFaultPoint) error {
				if point != workspace.AttemptCleanupFaultBeforeRecoveryEffect || moved {
					return nil
				}
				if err := os.Rename(worktree, displaced); err != nil {
					return err
				}
				moved = true
				return nil
			},
		)
	err = recovery.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, true,
	)
	if err == nil {
		t.Fatal("recovery accepted a bound directory moved outside its parent")
	}
	if !moved {
		t.Fatal("bound directory was not moved during cleanup")
	}
	content, readErr := os.ReadFile(
		filepath.Join(displaced, "must-survive.txt"),
	)
	if readErr != nil || string(content) != "preserve me\n" {
		t.Fatalf("moved bound directory sentinel changed: %q, %v", content, readErr)
	}
	if _, statErr := os.Lstat(
		worktree + ".feature-attempt-claim.binding",
	); statErr != nil {
		t.Fatalf("moved bound directory lost its identity binding: %v", statErr)
	}
}

func TestAttemptWorktreeClaimReleaseRecoversAfterBindingRemovalCrash(
	t *testing.T,
) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("release-binding-crash-attempt"),
		workspace.DigestBytes([]byte("release-binding-crash-generation")),
		base,
		"mu/release-binding-crash-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("simulated release crash")
	fired := false
	interrupted := adapter.WithAttemptWorktreeCleanupFaultInjector(
		func(point workspace.AttemptWorktreeCleanupFaultPoint) error {
			if point == workspace.AttemptCleanupFaultAfterReleaseBinding && !fired {
				fired = true
				return crash
			}
			return nil
		},
	)
	if err := interrupted.ReleaseAttemptWorktreeClaim(
		context.Background(), claim,
	); !errors.Is(err, crash) {
		t.Fatalf("release interruption = %v", err)
	}
	if !fired {
		t.Fatal("release fault did not fire")
	}
	if _, err := os.Lstat(
		worktree + ".feature-attempt-claim.binding",
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released binding remains after crash: %v", err)
	}
	if _, err := os.Lstat(
		worktree + ".feature-attempt-claim",
	); err != nil {
		t.Fatalf("claim missing after release crash: %v", err)
	}
	inspection, err := adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	)
	if err != nil || !inspection.Clean() {
		t.Fatalf("release-transition inspection = %#v, %v", inspection, err)
	}
	if err := adapter.ReleaseAttemptWorktreeClaim(
		context.Background(), claim,
	); err != nil {
		t.Fatalf("retry release after binding crash: %v", err)
	}
	if _, err := os.Lstat(
		worktree + ".feature-attempt-claim",
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim remains after release retry: %v", err)
	}
	inspection, err = adapter.InspectAttemptWorktree(
		context.Background(), repositoryRoot, claim.Branch(), worktree,
	)
	if err != nil || !inspection.Clean() {
		t.Fatalf("released worktree inspection = %#v, %v", inspection, err)
	}
}

func TestAttemptWorktreeRejectsMissingBlobWithoutLazyMaterialization(t *testing.T) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	blob := strings.TrimSpace(string(
		runGitSetup(t, repositoryRoot, "rev-parse", "HEAD:payload.txt"),
	))
	if len(blob) != 40 {
		t.Fatalf("payload blob = %q", blob)
	}
	if err := os.Remove(
		filepath.Join(repositoryRoot, ".git", "objects", blob[:2], blob[2:]),
	); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("missing-blob-attempt"),
		workspace.DigestBytes([]byte("missing-blob-generation")),
		base,
		"mu/missing-blob-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	err = adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	)
	if err == nil || !strings.Contains(err.Error(), "read Git blob") {
		t.Fatalf("missing raw blob error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(worktree, "payload.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing blob path was published: %v", err)
	}
}

func TestAttemptWorktreeRejectsUnsafeRawSymlink(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		want   string
	}{
		{name: "root escape", target: "../../outside", want: "escapes the repository root"},
		{name: "Git administration", target: ".git", want: "targets Git administration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositoryRoot := canonicalTestDirectory(t)
			runGitSetup(t, repositoryRoot, "init", "--initial-branch=main", ".")
			if err := os.Symlink(test.target, filepath.Join(repositoryRoot, "escape")); err != nil {
				t.Fatal(err)
			}
			runGitSetup(t, repositoryRoot, "add", "--", "escape")
			runGitSetup(
				t, repositoryRoot,
				"-c", "user.name=Attempt Test",
				"-c", "user.email=attempt@example.invalid",
				"commit", "-m", "unsafe symlink",
			)
			baseText := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD")))
			base, err := workspace.ParseGitObjectID("sha1:" + baseText)
			if err != nil {
				t.Fatal(err)
			}
			worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
			claim, err := workspace.NewAttemptWorktreeClaim(
				workspace.MustID("unsafe-symlink-attempt"),
				workspace.DigestBytes([]byte("unsafe-symlink-generation")),
				base,
				"mu/unsafe-symlink-attempt",
				worktree,
			)
			if err != nil {
				t.Fatal(err)
			}
			adapter := workspace.DefaultLocalAttemptGitAdapter()
			if err := adapter.PrepareAttemptWorktree(
				context.Background(), repositoryRoot, claim, false,
			); err != nil {
				t.Fatal(err)
			}
			err = adapter.CreateAttemptWorktree(
				context.Background(), repositoryRoot, claim, true, false,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe raw symlink error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(worktree, "escape")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe raw symlink was published: %v", err)
			}
		})
	}
}

func TestAttemptWorktreeRejectsConcurrentRegistrationChange(t *testing.T) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	probeDirectory := canonicalTestDirectory(t)
	racedWorktree := filepath.Join(probeDirectory, "raced-worktree")
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("registration-race-attempt"),
		workspace.DigestBytes([]byte("registration-race-generation")),
		base,
		"mu/registration-race-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	raced := false
	adapter := workspace.DefaultLocalAttemptGitAdapter().
		WithAttemptWorktreeMaterializationFaultInjector(
			func(point workspace.AttemptWorktreeMaterializationFaultPoint) error {
				if point == workspace.AttemptMaterializationFaultAfterPath && !raced {
					raced = true
					runGitSetup(
						t, repositoryRoot,
						"worktree", "add", "--no-checkout",
						"-b", "raced-registration", racedWorktree, "HEAD",
					)
				}
				return nil
			},
		)
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	err = adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	)
	if err == nil || !strings.Contains(err.Error(), "registered Git worktrees changed") {
		t.Fatalf("concurrent worktree registration error = %v", err)
	}
	if !raced {
		t.Fatal("registration race did not occur during raw path publication")
	}
}

func TestAttemptWorktreeRejectsExternalHardLinkDuringRawPublication(t *testing.T) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	probeDirectory := canonicalTestDirectory(t)
	outside := filepath.Join(probeDirectory, "outside-link")
	worktree := filepath.Join(canonicalTestDirectory(t), "attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("hard-link-race-attempt"),
		workspace.DigestBytes([]byte("hard-link-race-generation")),
		base,
		"mu/hard-link-race-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	adapter := workspace.DefaultLocalAttemptGitAdapter().
		WithAttemptWorktreeMaterializationFaultInjector(
			func(point workspace.AttemptWorktreeMaterializationFaultPoint) error {
				if point != workspace.AttemptMaterializationFaultAfterPath || linked {
					return nil
				}
				for _, relative := range []string{".gitattributes", "payload.txt", "script.sh"} {
					err := os.Link(filepath.Join(worktree, relative), outside)
					if err == nil {
						linked = true
						return nil
					}
					if !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
				return nil
			},
		)
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	err = adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	)
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("external hard-link publication error = %v", err)
	}
	if !linked {
		t.Fatal("external hard link was not created during raw publication")
	}
}

func TestAttemptWorktreeRejectsPostRegistrationSymlinkWithoutTouchingOutsideDirectory(
	t *testing.T,
) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	worktreeParent := canonicalTestDirectory(t)
	worktree := filepath.Join(worktreeParent, "attempt")
	displaced := filepath.Join(worktreeParent, "displaced-attempt")
	outside := canonicalTestDirectory(t)
	victim := filepath.Join(outside, "outside-victim.txt")
	if err := os.WriteFile(victim, []byte("must survive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("symlink-race-attempt"),
		workspace.DigestBytes([]byte("symlink-race-generation")),
		base,
		"mu/symlink-race-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	adapter := workspace.DefaultLocalAttemptGitAdapter().
		WithAttemptWorktreeMaterializationFaultInjector(
			func(point workspace.AttemptWorktreeMaterializationFaultPoint) error {
				if point != workspace.AttemptMaterializationFaultAfterRegistration || replaced {
					return nil
				}
				administration, err := os.ReadFile(filepath.Join(worktree, ".git"))
				if err != nil {
					return err
				}
				if err := os.Rename(worktree, displaced); err != nil {
					return err
				}
				if err := os.WriteFile(
					filepath.Join(outside, ".git"), administration, 0o600,
				); err != nil {
					return err
				}
				if err := os.Symlink(outside, worktree); err != nil {
					return err
				}
				replaced = true
				return nil
			},
		)
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	err = adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	)
	if err == nil {
		t.Fatal("post-registration worktree replacement was accepted")
	}
	if !replaced {
		t.Fatal("post-registration worktree replacement did not run")
	}
	content, readErr := os.ReadFile(victim)
	if readErr != nil || string(content) != "must survive\n" {
		t.Fatalf("outside victim was modified or removed: %q, %v", content, readErr)
	}
}

func TestAttemptWorktreeDoesNotRecreateMissingBoundDirectoryOutsideVerifiedParent(
	t *testing.T,
) {
	repositoryRoot, base := newRawAttemptTreeRepository(t)
	worktreeParent := canonicalTestDirectory(t)
	worktree := filepath.Join(worktreeParent, "attempt")
	displaced := filepath.Join(canonicalTestDirectory(t), "displaced-attempt")
	claim, err := workspace.NewAttemptWorktreeClaim(
		workspace.MustID("outside-move-attempt"),
		workspace.DigestBytes([]byte("outside-move-generation")),
		base,
		"mu/outside-move-attempt",
		worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("stop after durable registration")
	adapter := workspace.DefaultLocalAttemptGitAdapter().
		WithAttemptWorktreeMaterializationFaultInjector(
			func(point workspace.AttemptWorktreeMaterializationFaultPoint) error {
				if point == workspace.AttemptMaterializationFaultAfterRegistration {
					return crash
				}
				return nil
			},
		)
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, false,
	); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, true, false,
	); !errors.Is(err, crash) {
		t.Fatalf("registration interruption = %v", err)
	}
	if err := os.Rename(worktree, displaced); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(displaced, "must-survive.txt")
	if err := os.WriteFile(sentinel, []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = workspace.DefaultLocalAttemptGitAdapter().CreateAttemptWorktree(
		context.Background(), repositoryRoot, claim, false, true,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "identity is not present beneath the verified parent") {
		t.Fatalf("missing bound directory recovery error = %v", err)
	}
	if _, statErr := os.Lstat(worktree); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing bound worktree was recreated: %v", statErr)
	}
	content, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(content) != "preserve me\n" {
		t.Fatalf("displaced bound directory was modified: %q, %v", content, readErr)
	}
	if _, statErr := os.Lstat(worktree + ".feature-attempt-claim.binding"); statErr != nil {
		t.Fatalf("durable identity binding was discarded: %v", statErr)
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
	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-21T11:01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T11:02:00Z")
	unconstrainedHead := mustGitObject(t, 'b')
	harness.git.setHead(t, attempt.Branch(), unconstrainedHead, true)
	lease := attempt.LeaseID()
	evidence := boundaryEvidence(t, "pause-only")
	result, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	paused := result.Attempt()
	if paused.Phase() != workspace.AttemptPaused || !paused.LeaseID().IsZero() ||
		paused.SerialSegmentHeld() || len(result.Directives()) != 1 {
		t.Fatalf("pause-only boundary did not atomically close and release: %#v", paused)
	}
	boundary := result.Boundary()
	if boundary.LeaseID() != lease || boundary.EvidenceDigest().IsZero() ||
		boundary.Head() != unconstrainedHead || !boundary.LeaseFencedAndReleased() {
		t.Fatalf("boundary did not checkpoint closed bindings: %#v", boundary)
	}
	ownerDirective, ok := result.Directives()[0].(workspace.OwnerGateDirective)
	if !ok || ownerDirective.DirectiveDigest() != boundary.DirectiveDigest() ||
		len(ownerDirective.Choices()) != 1 || ownerDirective.Choices()[0] != workspace.OwnerBoundaryContinue {
		t.Fatalf("pause-only owner directive = %#v", result.Directives())
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
	writtenKinds := map[workspace.JournalResourceKind]bool{}
	for _, resource := range last.WriteSet() {
		writtenKinds[resource.Kind()] = true
	}
	for _, kind := range []workspace.JournalResourceKind{
		workspace.JournalResourceAttempt, workspace.JournalResourceLease,
		workspace.JournalResourceEvidence, workspace.JournalResourceSerialSegment,
	} {
		if !writtenKinds[kind] {
			t.Fatalf("atomic boundary did not write %s resource", kind)
		}
	}
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		harness.journal, harness.definition,
		workspace.RecordOrchestrationAcknowledgementRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementGoalCompleted,
			DirectiveDigest: boundary.DirectiveDigest(), Goal: harness.goal,
			IdempotencyKey: workspace.DigestBytes([]byte("false-completion")),
			OccurredAt:     mustTime(t, "2026-07-21T11:04:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "pause-only") {
		t.Fatalf("pause-only boundary claimed broader goal completion: %v", err)
	}
	if _, err := workspace.RecordOwnerBoundaryResponse(
		harness.journal, harness.definition,
		workspace.RecordOwnerBoundaryResponseRequest{
			AttemptID: attempt.AttemptID(), BoundaryID: boundary.BoundaryID(),
			DirectiveDigest: boundary.DirectiveDigest(), Goal: boundary.Goal(),
			ExpectedHead: boundary.Head(), Response: workspace.OwnerBoundaryContinue,
			OccurredAt: mustTime(t, "2026-07-21T11:05:00Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	resumed, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ResumeAttemptRequest{AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:06:00Z")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Phase() != workspace.AttemptActive || resumed.Goal() != harness.goal ||
		resumed.LeaseID() == lease || !resumed.SerialSegmentHeld() {
		t.Fatalf("pause-only resume bindings = %#v", resumed)
	}
}

func TestLocalAttemptInspectionRejectsHiddenIndexFlags(t *testing.T) {
	for _, test := range []struct {
		name string
		flag string
	}{
		{name: "assume unchanged", flag: "--assume-unchanged"},
		{name: "skip worktree", flag: "--skip-worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, _, _ := newProtocolRepository(t)
			worktree := filepath.Join(t.TempDir(), "attempt")
			branch := "attempt-hidden-index"
			runGitSetup(t, repository, "worktree", "add", "-b", branch, worktree, "HEAD")
			runGitSetup(t, worktree, "update-index", test.flag, "src/protocol.go")
			if err := os.WriteFile(
				filepath.Join(worktree, "src", "protocol.go"),
				[]byte("package protocol\n\nconst HiddenAtBoundary = true\n"), 0o644,
			); err != nil {
				t.Fatal(err)
			}

			if _, err := workspace.DefaultLocalAttemptGitAdapter().InspectAttemptWorktree(
				context.Background(), repository, branch, worktree,
			); err == nil || !strings.Contains(err.Error(), "assume-unchanged and skip-worktree") {
				t.Fatalf("InspectAttemptWorktree hidden-index error = %v", err)
			}
		})
	}
}

func TestLocalAttemptInspectionRejectsIntentToAddIndexEntry(t *testing.T) {
	repository, _, _ := newProtocolRepository(t)
	worktree := filepath.Join(t.TempDir(), "attempt")
	branch := "attempt-intent-to-add"
	runGitSetup(t, repository, "worktree", "add", "-b", branch, worktree, "HEAD")
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
	inspection, err := workspace.DefaultLocalAttemptGitAdapter().
		InspectAttemptWorktree(
			context.Background(), repository, branch, worktree,
		)
	if err != nil || inspection.Clean() {
		t.Fatalf("intent-to-add inspection = %#v, %v", inspection, err)
	}
}

func TestLocalAttemptInspectionRejectsRawModeDrift(t *testing.T) {
	repository, _, _ := newProtocolRepository(t)
	worktree := filepath.Join(t.TempDir(), "attempt")
	branch := "attempt-raw-mode"
	runGitSetup(t, repository, "worktree", "add", "-b", branch, worktree, "HEAD")
	runGitSetup(t, repository, "config", "core.fileMode", "false")
	tracked := filepath.Join(worktree, "src", "protocol.go")
	if err := os.Chmod(tracked, 0o755); err != nil {
		t.Fatal(err)
	}
	if ordinary := runGitSetup(t, worktree, "status", "--porcelain=v1", "-z"); len(ordinary) != 0 {
		t.Fatalf("core.fileMode=false did not hide attempt mode drift: %q", ordinary)
	}
	inspection, err := workspace.DefaultLocalAttemptGitAdapter().InspectAttemptWorktree(
		context.Background(), repository, branch, worktree,
	)
	if err != nil || inspection.Clean() {
		t.Fatalf("attempt raw-mode inspection = %#v, %v", inspection, err)
	}
}

func TestCompleteGoalBoundaryRecoversIdempotentlyThroughHandoffAndRejectsStaleHead(t *testing.T) {
	harness := newIndependentAttemptHarness(t, "unit-two")
	attempt := harness.reserve(t, "2026-07-21T12:01:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T12:02:00Z")
	crash := errors.New("simulated crash")
	evidence := boundaryEvidence(t, "complete-goal")
	_, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T12:03:00Z"),
			Fault: failAt(workspace.AttemptFaultAfterBoundary, crash),
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("boundary crash = %v", err)
	}
	result, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Evidence: evidence, OccurredAt: mustTime(t, "2026-07-21T12:04:00Z"),
		},
	)
	if err != nil || len(result.Directives()) != 1 {
		t.Fatalf("boundary retry = %#v, %v", result, err)
	}
	directive, ok := result.Directives()[0].(workspace.CompleteGoalAndWaitDirective)
	if !ok || directive.DirectiveDigest().IsZero() || directive.IdempotencyKey().IsZero() {
		t.Fatalf("complete-goal directive = %#v", result.Directives())
	}
	projection := mustRuntime(t, harness.journal)
	reemitted, ok := workspace.PendingAttemptBoundaryDirective(projection, attempt.AttemptID())
	if !ok || reemitted.DirectiveDigest() != directive.DirectiveDigest() || reemitted.IdempotencyKey() != directive.IdempotencyKey() {
		t.Fatal("complete-goal directive was not stably re-emitted")
	}
	if _, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ResumeAttemptRequest{AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:05:00Z")},
	); err == nil {
		t.Fatal("complete-goal attempt resumed without acknowledgements")
	}
	if _, err := workspace.OwnerBoundaryResponseRequestDigest(
		projection, attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	); err == nil || !strings.Contains(err.Error(), "goal-completion acknowledgement") {
		t.Fatalf("owner request was signable before goal completion: %v", err)
	}
	boundary := result.Boundary()
	if _, err := workspace.RecordOwnerBoundaryResponse(
		harness.journal, harness.definition,
		workspace.RecordOwnerBoundaryResponseRequest{
			AttemptID: attempt.AttemptID(), BoundaryID: boundary.BoundaryID(),
			DirectiveDigest: boundary.DirectiveDigest(), Goal: boundary.Goal(),
			ExpectedHead: boundary.Head(), Response: workspace.OwnerBoundaryContinue,
			OccurredAt: mustTime(t, "2026-07-21T12:06:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "goal-completion acknowledgement") {
		t.Fatalf("owner response bypassed goal completion: %v", err)
	}
	nextGoal, _ := workspace.NewGoalBinding(workspace.MustID("review-goal"), workspace.GoalScopeMergeUnit)
	differentAttempt := workspace.MustID("different-attempt")
	differentDirective := workspace.DigestBytes([]byte("different-directive"))
	differentIdempotency := workspace.DigestBytes([]byte("different-idempotency"))
	acknowledgement := workspace.RecordOrchestrationAcknowledgementRequest{
		AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementGoalCompleted,
		DirectiveDigest: directive.DirectiveDigest(), Goal: harness.goal,
		IdempotencyKey: directive.IdempotencyKey(),
		OccurredAt:     mustTime(t, "2026-07-21T12:06:30Z"),
	}
	recordsBeforeMismatches := journalRecordCount(t, harness.journal)
	for _, test := range []struct {
		name   string
		want   string
		mutate func(*workspace.RecordOrchestrationAcknowledgementRequest)
	}{
		{
			name: "attempt",
			want: "is not reserved",
			mutate: func(request *workspace.RecordOrchestrationAcknowledgementRequest) {
				request.AttemptID = differentAttempt
			},
		},
		{
			name: "directive",
			want: "directive does not match",
			mutate: func(request *workspace.RecordOrchestrationAcknowledgementRequest) {
				request.DirectiveDigest = differentDirective
			},
		},
		{
			name: "goal",
			want: "must bind the boundary goal",
			mutate: func(request *workspace.RecordOrchestrationAcknowledgementRequest) {
				request.Goal = nextGoal
			},
		},
		{
			name: "idempotency",
			want: "idempotency key does not match",
			mutate: func(request *workspace.RecordOrchestrationAcknowledgementRequest) {
				request.IdempotencyKey = differentIdempotency
			},
		},
	} {
		t.Run("rejects mismatched acknowledgement "+test.name, func(t *testing.T) {
			request := acknowledgement
			test.mutate(&request)
			if _, err := workspace.RecordOrchestrationAcknowledgement(
				harness.journal, harness.definition, request,
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mismatched %s acknowledgement error = %v", test.name, err)
			}
		})
	}
	if records := journalRecordCount(t, harness.journal); records != recordsBeforeMismatches {
		t.Fatalf(
			"mismatched acknowledgements changed journal records: before=%d after=%d",
			recordsBeforeMismatches, records,
		)
	}
	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Evidence: boundaryEvidence(t, "different-evidence"),
			OccurredAt: mustTime(t, "2026-07-21T12:06:45Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "different boundary evidence") {
		t.Fatalf("mismatched boundary evidence error = %v", err)
	}
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		harness.journal, harness.definition,
		workspace.RecordOrchestrationAcknowledgementRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementNextGoalCreated,
			DirectiveDigest: directive.DirectiveDigest(), Goal: nextGoal,
			IdempotencyKey: workspace.DigestBytes([]byte("next-too-early")),
			OccurredAt:     mustTime(t, "2026-07-21T12:07:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "durable creation intent") {
		t.Fatalf("next goal bypassed required ordering: %v", err)
	}
	goalAck := workspace.RecordOrchestrationAcknowledgementRequest{
		AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementGoalCompleted,
		DirectiveDigest: directive.DirectiveDigest(), Goal: harness.goal,
		IdempotencyKey: directive.IdempotencyKey(),
		OccurredAt:     mustTime(t, "2026-07-21T12:08:00Z"),
		Fault:          failAt(workspace.AttemptFaultAfterOrchestrationAck, crash),
	}
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		harness.journal, harness.definition, goalAck,
	); !errors.Is(err, crash) {
		t.Fatalf("goal ack crash = %v", err)
	}
	goalAck.Fault = nil
	ack, err := workspace.RecordOrchestrationAcknowledgement(
		harness.journal, harness.definition, goalAck,
	)
	if err != nil || ack.IdempotencyKey() != directive.IdempotencyKey() {
		t.Fatalf("goal ack retry = %#v, %v", ack, err)
	}
	projection = mustRuntime(t, harness.journal)
	_, err = workspace.OwnerBoundaryResponseRequestDigest(
		projection, attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	)
	if err != nil {
		t.Fatal(err)
	}
	differentBoundary := workspace.MustID("different-boundary")
	differentHead := mustGitObject(t, 'b')
	ownerResponse := workspace.RecordOwnerBoundaryResponseRequest{
		AttemptID: attempt.AttemptID(), BoundaryID: boundary.BoundaryID(),
		DirectiveDigest: boundary.DirectiveDigest(), Goal: boundary.Goal(),
		ExpectedHead: boundary.Head(), Response: workspace.OwnerBoundaryContinue,
		OccurredAt: mustTime(t, "2026-07-21T12:08:30Z"),
	}
	recordsBeforeMismatches = journalRecordCount(t, harness.journal)
	for _, test := range []struct {
		name   string
		want   string
		mutate func(*workspace.RecordOwnerBoundaryResponseRequest)
	}{
		{
			name: "attempt",
			want: "is not reserved",
			mutate: func(request *workspace.RecordOwnerBoundaryResponseRequest) {
				request.AttemptID = differentAttempt
			},
		},
		{
			name: "boundary",
			want: "does not match the exact current boundary",
			mutate: func(request *workspace.RecordOwnerBoundaryResponseRequest) {
				request.BoundaryID = differentBoundary
			},
		},
		{
			name: "directive",
			want: "does not match the exact current boundary",
			mutate: func(request *workspace.RecordOwnerBoundaryResponseRequest) {
				request.DirectiveDigest = differentDirective
			},
		},
		{
			name: "goal",
			want: "does not match the exact current boundary",
			mutate: func(request *workspace.RecordOwnerBoundaryResponseRequest) {
				request.Goal = nextGoal
			},
		},
		{
			name: "head",
			want: "does not match the exact current boundary",
			mutate: func(request *workspace.RecordOwnerBoundaryResponseRequest) {
				request.ExpectedHead = differentHead
			},
		},
	} {
		t.Run("rejects mismatched owner response "+test.name, func(t *testing.T) {
			request := ownerResponse
			test.mutate(&request)
			if _, err := workspace.RecordOwnerBoundaryResponse(
				harness.journal, harness.definition, request,
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mismatched %s owner response error = %v", test.name, err)
			}
		})
	}
	if records := journalRecordCount(t, harness.journal); records != recordsBeforeMismatches {
		t.Fatalf(
			"mismatched owner responses changed journal records: before=%d after=%d",
			recordsBeforeMismatches, records,
		)
	}
	ownerRequest := workspace.RecordOwnerBoundaryResponseRequest{
		AttemptID: attempt.AttemptID(), BoundaryID: boundary.BoundaryID(),
		DirectiveDigest: boundary.DirectiveDigest(), Goal: boundary.Goal(),
		ExpectedHead: boundary.Head(), Response: workspace.OwnerBoundaryContinue,
		OccurredAt: mustTime(t, "2026-07-21T12:09:00Z"),
		Fault:      failAt(workspace.AttemptFaultAfterOwnerResponse, crash),
	}
	if _, err := workspace.RecordOwnerBoundaryResponse(
		harness.journal, harness.definition, ownerRequest,
	); !errors.Is(err, crash) {
		t.Fatalf("owner response crash = %v", err)
	}
	ownerRequest.Fault = nil
	if _, err := workspace.RecordOwnerBoundaryResponse(
		harness.journal, harness.definition, ownerRequest,
	); err != nil {
		t.Fatalf("owner response retry: %v", err)
	}
	intentRequest := workspace.ReserveNextGoalCreationRequest{
		AttemptID: attempt.AttemptID(), Goal: nextGoal,
		OccurredAt: mustTime(t, "2026-07-21T12:09:30Z"),
		Fault:      failAt(workspace.AttemptFaultAfterNextGoalIntent, crash),
	}
	if _, err := workspace.ReserveNextGoalCreation(
		harness.journal, harness.definition, intentRequest,
	); !errors.Is(err, crash) {
		t.Fatalf("next-goal intent crash = %v", err)
	}
	intentRequest.Fault = nil
	intent, err := workspace.ReserveNextGoalCreation(harness.journal, harness.definition, intentRequest)
	if err != nil || intent.NextGoal() != nextGoal || intent.IdempotencyKey().IsZero() {
		t.Fatalf("next-goal intent retry = %#v, %v", intent, err)
	}
	pendingIntent, ok := workspace.PendingNextGoalCreationIntent(
		mustRuntime(t, harness.journal), attempt.AttemptID(),
	)
	if !ok || pendingIntent.NextGoal() != nextGoal || pendingIntent.IdempotencyKey() != intent.IdempotencyKey() {
		t.Fatalf("pending next-goal intent was not stably re-emitted: %#v", pendingIntent)
	}
	nextAck := workspace.RecordOrchestrationAcknowledgementRequest{
		AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementNextGoalCreated,
		DirectiveDigest: intent.DirectiveDigest(), Goal: nextGoal,
		IdempotencyKey: intent.IdempotencyKey(),
		OccurredAt:     mustTime(t, "2026-07-21T12:10:00Z"),
		Fault:          failAt(workspace.AttemptFaultAfterOrchestrationAck, crash),
	}
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		harness.journal, harness.definition, nextAck,
	); !errors.Is(err, crash) {
		t.Fatalf("next-goal ack crash = %v", err)
	}
	nextAck.Fault = nil
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		harness.journal, harness.definition, nextAck,
	); err != nil {
		t.Fatalf("next-goal ack retry: %v", err)
	}
	if _, ok := workspace.PendingNextGoalCreationIntent(mustRuntime(t, harness.journal), attempt.AttemptID()); ok {
		t.Fatal("acknowledged next-goal intent remained pending")
	}
	stale, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	harness.git.setHead(t, attempt.Branch(), stale, true)
	if _, err := workspace.ResumeAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ResumeAttemptRequest{AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:11:00Z")},
	); err == nil || !strings.Contains(err.Error(), "Git verification failed") {
		t.Fatalf("stale-head resume = %v", err)
	}
	harness.git.setHead(t, attempt.Branch(), harness.base, true)
	resumeRequest := workspace.ResumeAttemptRequest{
		AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:12:00Z"),
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
	if err != nil || resumed.Phase() != workspace.AttemptActive || resumed.Goal() != nextGoal {
		t.Fatalf("resume retry = %#v, %v", resumed, err)
	}
}

func TestSerialSegmentsFenceOnlyMatchingSegments(t *testing.T) {
	harness := newIndependentAttemptHarness(t, "unit-one")
	first := harness.reserve(t, "2026-07-21T13:01:00Z")
	otherUnit := mustMergeUnitReference(t, "alpha-plan", "unit-two")
	otherGoal, _ := workspace.NewGoalBinding(workspace.MustID("other-goal"), workspace.GoalScopeMergeUnit)
	other, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ReserveAttemptRequest{
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
		"    boundary:\n      mode: complete_goal_and_wait",
		"    boundary:\n      mode: complete_goal_and_wait\n      serial_segment: serial-alpha",
		1,
	))
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	worktreeRoot := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), workspaceDir, definition,
		mustTime(t, "2026-07-21T13:10:00Z"),
		workspace.WorkspaceInitializationOptions{WorktreeRoot: worktreeRoot},
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
		_, err := workspace.ReserveAttempt(
			context.Background(), journal, definition, git,
			workspace.ReserveAttemptRequest{
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
