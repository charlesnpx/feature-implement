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
	remote               []string
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

func (git *fakeAttemptGit) InspectAttemptRefs(context.Context, string, string) (workspace.AttemptRefInventory, error) {
	if git.inspectErr != nil {
		return workspace.AttemptRefInventory{}, git.inspectErr
	}
	return workspace.NewAttemptRefInventory(git.local, git.remote)
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
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	base, err := workspace.ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	goal, err := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	if err != nil {
		t.Fatal(err)
	}
	return attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDir, git: &fakeAttemptGit{}, base: base,
		unit: mustMergeUnitReference(t, "alpha-plan", unitID), goal: goal, worktrees: t.TempDir(),
	}
}

func (h attemptHarness) reserve(t *testing.T, at string) workspace.RuntimeAttemptProjection {
	t.Helper()
	attempt, err := workspace.ReserveAttempt(
		context.Background(), h.journal, h.definition, h.git,
		workspace.ReserveAttemptRequest{
			MergeUnit: h.unit, AttemptNumber: 1, Base: h.base,
			WorktreeRoot: h.worktrees, Goal: h.goal, OccurredAt: mustTime(t, at),
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
	repository, err := workspace.NewRepositoryIdentity("https://github.com/example/project.git")
	if err != nil {
		t.Fatal(err)
	}
	plan := workspace.MustID("p" + strings.Repeat("1", 90))
	unit := workspace.MustID("u" + strings.Repeat("2", 90))
	reference, err := workspace.NewMergeUnitReference(plan, unit)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	identity, err := workspace.DeriveAttemptIdentity(repository, reference, 7, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.Branch()) > 240 || strings.Count(identity.Branch(), "/") != 1 ||
		!strings.HasPrefix(identity.Branch(), "mu/") || !strings.Contains(identity.Branch(), "-a7-") {
		t.Fatalf("attempt branch is not flat and bounded: %q (%d)", identity.Branch(), len(identity.Branch()))
	}
	repeated, err := workspace.DeriveAttemptIdentity(repository, reference, 7, base)
	if err != nil || repeated.Branch() != identity.Branch() || repeated.AttemptID() != identity.AttemptID() {
		t.Fatalf("attempt identity is not stable: %#v, %v", repeated, err)
	}
	otherBase, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("b", 40))
	changed, _ := workspace.DeriveAttemptIdentity(repository, reference, 7, otherBase)
	if changed.Branch() == identity.Branch() || changed.AttemptID() == identity.AttemptID() {
		t.Fatal("attempt identity does not bind the exact base")
	}

	tests := []struct {
		name   string
		local  []string
		remote []string
		kind   workspace.AttemptRefConflictKind
		scope  workspace.AttemptRefScope
	}{
		{"local exact", []string{identity.Branch()}, nil, workspace.AttemptRefExact, workspace.AttemptRefLocal},
		{"remote exact", nil, []string{identity.Branch()}, workspace.AttemptRefExact, workspace.AttemptRefRemote},
		{"ancestor", []string{"mu"}, nil, workspace.AttemptRefAncestor, workspace.AttemptRefLocal},
		{"descendant", nil, []string{identity.Branch() + "/child"}, workspace.AttemptRefDescendant, workspace.AttemptRefRemote},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := workspace.CheckAttemptRefConflicts(identity.Branch(), test.local, test.remote, false)
			var conflict workspace.AttemptRefConflict
			if !errors.As(err, &conflict) || conflict.Kind() != test.kind || conflict.Scope() != test.scope {
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
			MergeUnit: blockedUnit, AttemptNumber: 1, Base: harness.base,
			WorktreeRoot: harness.worktrees, Goal: harness.goal,
			OccurredAt: mustTime(t, "2026-07-21T10:01:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "not scheduler-ready") || !strings.Contains(err.Error(), "dependency:alpha-plan/unit-one") {
		t.Fatalf("dependency-bypassing reservation error = %v", err)
	}
	if _, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ReserveAttemptRequest{
			MergeUnit: harness.unit, AttemptNumber: 4, Base: harness.base,
			WorktreeRoot: harness.worktrees, Goal: harness.goal,
			OccurredAt: mustTime(t, "2026-07-21T10:02:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "exceeds max_attempts 3") {
		t.Fatalf("over-budget reservation error = %v", err)
	}
	if _, err := workspace.ReserveAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.ReserveAttemptRequest{
			MergeUnit: harness.unit, AttemptNumber: 2, Base: harness.base,
			WorktreeRoot: harness.worktrees, Goal: harness.goal,
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

func TestReserveAttemptRejectsLocalAndRemoteRefCollisions(t *testing.T) {
	for _, test := range []struct {
		name   string
		local  func(string) []string
		remote func(string) []string
	}{
		{"local exact", func(branch string) []string { return []string{branch} }, nil},
		{"local ancestor", func(string) []string { return []string{"mu"} }, nil},
		{"remote exact", nil, func(branch string) []string { return []string{"refs/heads/" + branch} }},
		{"remote descendant", nil, func(branch string) []string { return []string{branch + "/child"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newAttemptHarness(t, "unit-one")
			identity, err := workspace.DeriveAttemptIdentity(
				harness.definition.Workspace().Repository(), harness.unit, 1, harness.base,
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.local != nil {
				harness.git.local = test.local(identity.Branch())
			}
			if test.remote != nil {
				harness.git.remote = test.remote(identity.Branch())
			}
			_, err = workspace.ReserveAttempt(
				context.Background(), harness.journal, harness.definition, harness.git,
				workspace.ReserveAttemptRequest{
					MergeUnit: harness.unit, AttemptNumber: 1, Base: harness.base,
					WorktreeRoot: harness.worktrees, Goal: harness.goal,
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
		harness.definition.Workspace().Repository(), harness.unit, 1, harness.base,
	)
	if err != nil {
		t.Fatal(err)
	}
	reservation := workspace.ReserveAttemptRequest{
		MergeUnit: harness.unit, AttemptNumber: 1, Base: harness.base,
		WorktreeRoot: harness.worktrees, Goal: harness.goal, OccurredAt: mustTime(t, "2026-07-21T10:01:00Z"),
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
		started.LeaseID().IsZero() || started.AuthorizationID().IsZero() || harness.git.createCalls != 1 {
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
	}
	fixture.sources.Workspace.Bytes = []byte(strings.Join(workspaceSource, "\n"))
	definition := mustDefinition(t, fixture.sources)
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("root-admission-goal"), workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"repository-root": repositoryRoot,
		"git-common-root": filepath.Join(repositoryRoot, ".git"),
		"linked-worktree": linkedRoot,
	}
	for name, worktreeRoot := range cases {
		t.Run(name, func(t *testing.T) {
			runtimeRoot := t.TempDir()
			if _, err := workspace.InitializeWorkspaceV2(
				runtimeRoot, definition, mustTime(t, "2026-07-21T10:15:00Z"),
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
			beforeRecords := journalRecordCount(t, journal)
			beforeEntries := directoryEntryNames(t, worktreeRoot)
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
			if _, err := workspace.ReserveAttempt(
				context.Background(), journal, definition, adapter,
				workspace.ReserveAttemptRequest{
					MergeUnit:     mustMergeUnitReference(t, "alpha-plan", "unit-one"),
					AttemptNumber: 1,
					Base:          base,
					WorktreeRoot:  worktreeRoot,
					Goal:          goal,
					OccurredAt:    mustTime(t, "2026-07-21T10:16:00Z"),
				},
			); err == nil || !strings.Contains(err.Error(), "unsafe attempt worktree overlap") {
				t.Fatalf("Git-owned worktree root reservation error = %v", err)
			}
			if afterRecords := journalRecordCount(t, journal); afterRecords != beforeRecords {
				t.Fatalf(
					"rejected worktree root journaled an attempt: before=%d after=%d",
					beforeRecords, afterRecords,
				)
			}

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
	}
	fixture.sources.Workspace.Bytes = []byte(strings.Join(workspaceSource, "\n"))
	definition := mustDefinition(t, fixture.sources)
	runtimeRoot := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(
		runtimeRoot, definition, mustTime(t, "2026-07-21T10:17:00Z"),
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
	worktreeRoot := t.TempDir()
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	attempt, err := workspace.ReserveAttempt(
		context.Background(), journal, definition, adapter,
		workspace.ReserveAttemptRequest{
			MergeUnit:     mustMergeUnitReference(t, "alpha-plan", "unit-one"),
			AttemptNumber: 1,
			Base:          base,
			WorktreeRoot:  worktreeRoot,
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
	}
	fixture.sources.Workspace.Bytes = []byte(strings.Join(workspaceSource, "\n"))
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T10:20:00Z")); err != nil {
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
			Base: base, WorktreeRoot: t.TempDir(), Goal: goal, OccurredAt: mustTime(t, "2026-07-21T10:21:00Z"),
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
	if err := os.WriteFile(filepath.Join(attempt.Worktree(), "partial.txt"), []byte("partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PrepareAttemptWorktree(
		context.Background(), repositoryRoot, claim, true,
	); err != nil {
		t.Fatalf("recover claimed partial worktree: %v", err)
	}
	if _, err := os.Lstat(attempt.Worktree()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claimed partial worktree still exists: %v", err)
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
	lease, authorization := attempt.LeaseID(), attempt.AuthorizationID()
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
	if paused.Phase() != workspace.AttemptPaused || !paused.LeaseID().IsZero() || !paused.AuthorizationID().IsZero() ||
		paused.SerialSegmentHeld() || len(result.Directives()) != 1 {
		t.Fatalf("pause-only boundary did not atomically close and release: %#v", paused)
	}
	boundary := result.Boundary()
	if boundary.LeaseID() != lease || boundary.AuthorizationID() != authorization || boundary.EvidenceDigest().IsZero() ||
		boundary.Head() != unconstrainedHead || !boundary.AuthorizationClosed() || !boundary.LeaseFencedAndReleased() {
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
		workspace.JournalResourceAuthorization, workspace.JournalResourceEvidence,
		workspace.JournalResourceSerialSegment,
	} {
		if !writtenKinds[kind] {
			t.Fatalf("atomic boundary did not write %s resource", kind)
		}
	}
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		context.Background(), harness.journal, harness.definition, &boundaryVerifier{},
		workspace.RecordOrchestrationAcknowledgementRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementGoalCompleted,
			Goal: harness.goal, Receipt: placeholderControlPlaneReceipt(t, workspace.DigestBytes([]byte("false-completion")), "false-completion"),
			OccurredAt: mustTime(t, "2026-07-21T11:04:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "pause-only") {
		t.Fatalf("pause-only boundary claimed broader goal completion: %v", err)
	}
	projection := mustRuntime(t, harness.journal)
	requestDigest, err := workspace.OwnerBoundaryResponseRequestDigest(
		projection, attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &boundaryVerifier{expectedRequest: requestDigest}
	binding, err := workspace.OwnerBoundaryResponseControlPlaneBinding(
		harness.definition, projection, attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt := controlPlaneReceipt(t, binding, "pause-nonce")
	if _, err := workspace.RecordOwnerBoundaryResponse(
		context.Background(), harness.journal, harness.definition, verifier,
		workspace.RecordOwnerBoundaryResponseRequest{
			AttemptID: attempt.AttemptID(), Response: workspace.OwnerBoundaryContinue,
			Receipt: receipt, OccurredAt: mustTime(t, "2026-07-21T11:05:00Z"),
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
		resumed.LeaseID() == lease || resumed.AuthorizationID() == authorization || !resumed.SerialSegmentHeld() {
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
	if _, err := workspace.DefaultLocalAttemptGitAdapter().InspectAttemptWorktree(
		context.Background(), repository, branch, worktree,
	); err == nil || !strings.Contains(err.Error(), "executable mode differs") {
		t.Fatalf("attempt raw-mode inspection error = %v", err)
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
	prematureDigest := workspace.DigestBytes([]byte("premature-owner-request"))
	verifier := &boundaryVerifier{expectedRequest: prematureDigest}
	receipt := placeholderControlPlaneReceipt(t, prematureDigest, "premature-complete-nonce")
	if _, err := workspace.RecordOwnerBoundaryResponse(
		context.Background(), harness.journal, harness.definition, verifier,
		workspace.RecordOwnerBoundaryResponseRequest{
			AttemptID: attempt.AttemptID(), Response: workspace.OwnerBoundaryContinue,
			Receipt: receipt, OccurredAt: mustTime(t, "2026-07-21T12:06:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "goal-completion acknowledgement") {
		t.Fatalf("owner response bypassed goal completion: %v", err)
	}
	nextGoal, _ := workspace.NewGoalBinding(workspace.MustID("review-goal"), workspace.GoalScopeMergeUnit)
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		context.Background(), harness.journal, harness.definition, &boundaryVerifier{},
		workspace.RecordOrchestrationAcknowledgementRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementNextGoalCreated,
			Goal: nextGoal, Receipt: placeholderControlPlaneReceipt(t, workspace.DigestBytes([]byte("next-too-early")), "next-too-early"),
			OccurredAt: mustTime(t, "2026-07-21T12:07:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "durable creation intent") {
		t.Fatalf("next goal bypassed required ordering: %v", err)
	}
	goalBinding, err := workspace.OrchestrationAcknowledgementControlPlaneBinding(
		harness.definition, mustRuntime(t, harness.journal), attempt.AttemptID(),
		workspace.AcknowledgementGoalCompleted, harness.goal,
	)
	if err != nil {
		t.Fatal(err)
	}
	goalVerifier := &boundaryVerifier{expectedRequest: goalBinding.RequestDigest()}
	goalAck := workspace.RecordOrchestrationAcknowledgementRequest{
		AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementGoalCompleted,
		Goal: harness.goal, Receipt: controlPlaneReceipt(t, goalBinding, "goal-completed"),
		OccurredAt: mustTime(t, "2026-07-21T12:08:00Z"),
		Fault:      failAt(workspace.AttemptFaultAfterOrchestrationAck, crash),
	}
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		context.Background(), harness.journal, harness.definition, goalVerifier, goalAck,
	); !errors.Is(err, crash) {
		t.Fatalf("goal ack crash = %v", err)
	}
	goalAck.Fault = nil
	ack, err := workspace.RecordOrchestrationAcknowledgement(
		context.Background(), harness.journal, harness.definition, goalVerifier, goalAck,
	)
	if err != nil || ack.IdempotencyKey() != directive.IdempotencyKey() {
		t.Fatalf("goal ack retry = %#v, %v", ack, err)
	}
	projection = mustRuntime(t, harness.journal)
	requestDigest, err := workspace.OwnerBoundaryResponseRequestDigest(
		projection, attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	)
	if err != nil {
		t.Fatal(err)
	}
	verifier = &boundaryVerifier{expectedRequest: requestDigest}
	binding, err := workspace.OwnerBoundaryResponseControlPlaneBinding(
		harness.definition, projection, attempt.AttemptID(), workspace.OwnerBoundaryContinue,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt = controlPlaneReceipt(t, binding, "complete-nonce")
	ownerRequest := workspace.RecordOwnerBoundaryResponseRequest{
		AttemptID: attempt.AttemptID(), Response: workspace.OwnerBoundaryContinue,
		Receipt: receipt, OccurredAt: mustTime(t, "2026-07-21T12:09:00Z"),
		Fault: failAt(workspace.AttemptFaultAfterOwnerResponse, crash),
	}
	if _, err := workspace.RecordOwnerBoundaryResponse(
		context.Background(), harness.journal, harness.definition, verifier, ownerRequest,
	); !errors.Is(err, crash) {
		t.Fatalf("owner response crash = %v", err)
	}
	ownerRequest.Fault = nil
	if _, err := workspace.RecordOwnerBoundaryResponse(
		context.Background(), harness.journal, harness.definition, verifier, ownerRequest,
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
	nextBinding, err := workspace.OrchestrationAcknowledgementControlPlaneBinding(
		harness.definition, mustRuntime(t, harness.journal), attempt.AttemptID(),
		workspace.AcknowledgementNextGoalCreated, nextGoal,
	)
	if err != nil {
		t.Fatal(err)
	}
	nextVerifier := &boundaryVerifier{expectedRequest: nextBinding.RequestDigest()}
	nextAck := workspace.RecordOrchestrationAcknowledgementRequest{
		AttemptID: attempt.AttemptID(), Kind: workspace.AcknowledgementNextGoalCreated,
		Goal: nextGoal, Receipt: controlPlaneReceipt(t, nextBinding, "next-goal-created"),
		OccurredAt: mustTime(t, "2026-07-21T12:10:00Z"),
		Fault:      failAt(workspace.AttemptFaultAfterOrchestrationAck, crash),
	}
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		context.Background(), harness.journal, harness.definition, nextVerifier, nextAck,
	); !errors.Is(err, crash) {
		t.Fatalf("next-goal ack crash = %v", err)
	}
	nextAck.Fault = nil
	if _, err := workspace.RecordOrchestrationAcknowledgement(
		context.Background(), harness.journal, harness.definition, nextVerifier, nextAck,
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
			MergeUnit: otherUnit, AttemptNumber: 1, Base: harness.base,
			WorktreeRoot: harness.worktrees, Goal: otherGoal, OccurredAt: mustTime(t, "2026-07-21T13:02:00Z"),
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
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, definition, mustTime(t, "2026-07-21T13:10:00Z")); err != nil {
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
				Base: harness.base, WorktreeRoot: t.TempDir(), Goal: otherGoal,
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

func TestReconciliationConsultsJournalProjectedAttempts(t *testing.T) {
	fixture := newDefinitionFixture(t)
	active := mustDefinition(t, fixture.sources)
	candidate := mustProspectiveCandidate(t, fixture)
	workspaceDir := t.TempDir()
	if _, err := workspace.InitializeWorkspaceV2(workspaceDir, active, mustTime(t, "2026-07-21T14:00:00Z")); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := workspace.OpenWorkspaceJournal(workspaceDir, workspace.JournalReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := store.StageCandidate(journal, candidate, mustTime(t, "2026-07-21T14:01:00Z")); err != nil {
		t.Fatal(err)
	}
	base, _ := workspace.ParseGitObjectID("sha1:" + strings.Repeat("a", 40))
	goal, _ := workspace.NewGoalBinding(workspace.MustID("reconcile-goal"), workspace.GoalScopeMergeUnit)
	if _, err := workspace.ReserveAttempt(
		context.Background(), journal, active, &fakeAttemptGit{},
		workspace.ReserveAttemptRequest{
			MergeUnit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), AttemptNumber: 1,
			Base: base, WorktreeRoot: t.TempDir(), Goal: goal, OccurredAt: mustTime(t, "2026-07-21T14:02:00Z"),
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	completed, _ := workspace.NewMergeUnitRuntimeState(
		mustMergeUnitReference(t, "alpha-plan", "unit-one"), workspace.MergeUnitCompleted, active.Generation(),
	)
	future, _ := workspace.NewMergeUnitRuntimeState(
		mustMergeUnitReference(t, "alpha-plan", "unit-two"), workspace.MergeUnitFuture, workspace.Digest{},
	)
	state, err := workspace.NewReconciliationState(
		snapshot, []workspace.MergeUnitRuntimeState{completed, future}, nil, nil, nil,
		workspace.EmptyRuntimeHistoryBinding(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.DryRunReconciliation(active, candidate, snapshot, state); err == nil ||
		!strings.Contains(err.Error(), "journal-projected nonterminal attempt") {
		t.Fatalf("reconciliation ignored journal attempt: %v", err)
	}
}

type boundaryVerifier struct {
	expectedRequest workspace.Digest
	calls           int
}

func (verifier *boundaryVerifier) Verify(
	_ context.Context,
	verification workspace.ControlPlaneVerification,
	receipt workspace.ControlPlaneReceiptV2,
) error {
	verifier.calls++
	if receipt.Binding() != verification.Binding() {
		return fmt.Errorf("receipt binding does not match verification")
	}
	if !verifier.expectedRequest.IsZero() && verification.RequestDigest() != verifier.expectedRequest {
		return fmt.Errorf("request = %s, expected %s", verification.RequestDigest(), verifier.expectedRequest)
	}
	return nil
}

func controlPlaneReceipt(t *testing.T, binding workspace.ControlPlaneBinding, nonce string) workspace.ControlPlaneReceiptV2 {
	t.Helper()
	envelope, err := workspace.NewControlPlaneEnvelopeV2(
		binding, workspace.MustID("owner-key"), nonce,
		mustTime(t, "2026-07-22T00:00:00Z"), workspace.MustID("test-coordinator"),
	)
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	signature[0] = 1
	receipt, err := workspace.NewControlPlaneReceiptV2(envelope, signature)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func placeholderControlPlaneReceipt(t *testing.T, request workspace.Digest, nonce string) workspace.ControlPlaneReceiptV2 {
	t.Helper()
	repository, err := workspace.NewRepositoryIdentity("https://example.invalid/repository.git")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.NewControlPlaneBinding(workspace.ControlPlaneBindingOptions{
		Kind: workspace.ControlPlaneReceiptReconciliation, WorkspaceID: workspace.MustID("placeholder-workspace"),
		Generation: workspace.DigestBytes([]byte("placeholder-generation")), RequestDigest: request,
		Repository: repository, Remote: "origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	return controlPlaneReceipt(t, binding, nonce)
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
var _ workspace.ControlPlaneVerifierPort = (*boundaryVerifier)(nil)
