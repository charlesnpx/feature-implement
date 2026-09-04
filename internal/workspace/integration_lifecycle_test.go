package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	"github.com/charlesnpx/feature-implement/internal/workspacecmd"
)

type integrationGitStub struct {
	featureHead       workspace.GitObjectID
	featureRefPresent bool
	expectedCommit    bool
	inspectCalls      int
	createCalls       int
	publishCalls      int
	verifyCalls       int
	verifiedStart     workspace.GitObjectID
	verifiedEnd       workspace.GitObjectID
	forcedState       workspace.IntegrationRefState
}

type concurrentIntegrationGitStub struct {
	mu                sync.Mutex
	featureHead       workspace.GitObjectID
	featureRefPresent bool
	initialSeen       map[string]bool
	initialCount      int
	initialRelease    chan struct{}
	objects           map[string]bool
}

func (git *integrationGitStub) InspectAttempt(
	_ context.Context,
	target workspace.LocalTargetBinding,
	worktree string,
	expectedHead, expectedTree workspace.GitObjectID,
) (workspace.AttemptGitInspection, error) {
	return stubIntegrationAttemptInspection(
		target, worktree, expectedHead, expectedTree,
	)
}

func (git *integrationGitStub) InspectIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	intent workspace.MergeUnitIntegrationIntent,
) (workspace.IntegrationGitInspection, error) {
	git.inspectCalls++
	state := git.forcedState
	if state == "" {
		if !git.featureRefPresent {
			if intent.ExpectedFeatureRefAbsent() {
				state = workspace.IntegrationRefExpectedAbsent
			} else {
				state = workspace.IntegrationRefUnexpectedDrift
			}
		} else {
			switch git.featureHead {
			case intent.ExpectedFeatureHead():
				state = workspace.IntegrationRefExpectedHead
			case intent.ExpectedMerge():
				state = workspace.IntegrationRefExpectedMerge
			default:
				state = workspace.IntegrationRefUnexpectedDrift
			}
		}
	}
	featureHead := git.featureHead
	if !git.featureRefPresent {
		featureHead = workspace.GitObjectID{}
	}
	return workspace.NewIntegrationGitInspection(
		featureHead, state, git.expectedCommit,
	)
}

func (git *integrationGitStub) CreateIntegrationCommit(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	intent workspace.MergeUnitIntegrationIntent,
) error {
	git.createCalls++
	if (intent.ExpectedFeatureRefAbsent() && git.featureRefPresent) ||
		(!intent.ExpectedFeatureRefAbsent() && !git.featureRefPresent) ||
		git.featureHead != intent.ExpectedFeatureHead() {
		return errors.New("stub feature head moved before commit creation")
	}
	git.expectedCommit = true
	return nil
}

func (git *integrationGitStub) PublishIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	intent workspace.MergeUnitIntegrationIntent,
	fault workspace.IntegrationLifecycleFaultInjector,
) error {
	git.publishCalls++
	if (intent.ExpectedFeatureRefAbsent() && git.featureRefPresent) ||
		(!intent.ExpectedFeatureRefAbsent() && !git.featureRefPresent) ||
		git.featureHead != intent.ExpectedFeatureHead() ||
		!git.expectedCommit {
		return errors.New("stub integration publication precondition failed")
	}
	if fault != nil {
		if err := fault(
			workspace.IntegrationFaultAfterRefPrepared,
		); err != nil {
			return fmt.Errorf(
				"integration lifecycle fault at %s: %w",
				workspace.IntegrationFaultAfterRefPrepared,
				err,
			)
		}
	}
	git.featureHead = intent.ExpectedMerge()
	git.featureRefPresent = true
	return nil
}

func (git *integrationGitStub) VerifyCompletedIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	chain []workspace.MergeUnitIntegrationIntent,
) error {
	if len(chain) == 0 {
		return errors.New(
			"stub completed integration verification has no chain",
		)
	}
	git.verifyCalls++
	git.verifiedStart = chain[0].ExpectedMerge()
	git.verifiedEnd = chain[len(chain)-1].ExpectedMerge()
	if !git.featureRefPresent || git.featureHead != git.verifiedEnd ||
		!git.expectedCommit {
		return errors.New(
			"stub completed integration verification failed",
		)
	}
	return nil
}

func (git *concurrentIntegrationGitStub) InspectAttempt(
	_ context.Context,
	target workspace.LocalTargetBinding,
	worktree string,
	expectedHead, expectedTree workspace.GitObjectID,
) (workspace.AttemptGitInspection, error) {
	return stubIntegrationAttemptInspection(
		target, worktree, expectedHead, expectedTree,
	)
}

func (git *concurrentIntegrationGitStub) InspectIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	intent workspace.MergeUnitIntegrationIntent,
) (workspace.IntegrationGitInspection, error) {
	key := intent.AttemptID().String()
	git.mu.Lock()
	if !git.initialSeen[key] {
		git.initialSeen[key] = true
		git.initialCount++
		if git.initialCount == 2 {
			close(git.initialRelease)
		}
		release := git.initialRelease
		git.mu.Unlock()
		<-release
		if intent.ExpectedFeatureRefAbsent() {
			return workspace.NewIntegrationGitInspection(
				workspace.GitObjectID{},
				workspace.IntegrationRefExpectedAbsent,
				false,
			)
		}
		return workspace.NewIntegrationGitInspection(
			intent.ExpectedFeatureHead(),
			workspace.IntegrationRefExpectedHead,
			false,
		)
	}
	head := git.featureHead
	present := git.featureRefPresent
	objectExists := git.objects[intent.Digest().String()]
	git.mu.Unlock()
	state := workspace.IntegrationRefUnexpectedDrift
	if !present {
		if intent.ExpectedFeatureRefAbsent() {
			state = workspace.IntegrationRefExpectedAbsent
		}
		head = workspace.GitObjectID{}
	} else {
		switch head {
		case intent.ExpectedFeatureHead():
			state = workspace.IntegrationRefExpectedHead
		case intent.ExpectedMerge():
			state = workspace.IntegrationRefExpectedMerge
		}
	}
	return workspace.NewIntegrationGitInspection(
		head, state, objectExists,
	)
}

func stubIntegrationAttemptInspection(
	target workspace.LocalTargetBinding,
	worktree string,
	expectedHead, expectedTree workspace.GitObjectID,
) (workspace.AttemptGitInspection, error) {
	_ = target
	binding, err := workspace.NewAttemptWorktreeGitBinding(
		workspace.AttemptWorktreeGitBindingOptions{
			Worktree:        worktree,
			GitDirectory:    filepath.Join(worktree, ".git"),
			CommonDirectory: filepath.Join(worktree, ".git"),
			AdministrationDigest: workspace.DigestBytes(
				[]byte("scratch administration " + worktree),
			),
			ConfigurationDigest: workspace.DigestBytes(
				[]byte("scratch configuration " + worktree),
			),
		},
	)
	if err != nil {
		return workspace.AttemptGitInspection{}, err
	}
	return workspace.NewScratchAttemptGitInspection(
		expectedHead, expectedTree,
		binding, true,
	)
}

func (git *concurrentIntegrationGitStub) CreateIntegrationCommit(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	intent workspace.MergeUnitIntegrationIntent,
) error {
	git.mu.Lock()
	defer git.mu.Unlock()
	if (intent.ExpectedFeatureRefAbsent() && git.featureRefPresent) ||
		(!intent.ExpectedFeatureRefAbsent() && !git.featureRefPresent) ||
		git.featureHead != intent.ExpectedFeatureHead() {
		return errors.New("concurrent feature head moved before commit")
	}
	git.objects[intent.Digest().String()] = true
	return nil
}

func (git *concurrentIntegrationGitStub) PublishIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	intent workspace.MergeUnitIntegrationIntent,
	fault workspace.IntegrationLifecycleFaultInjector,
) error {
	git.mu.Lock()
	defer git.mu.Unlock()
	if (intent.ExpectedFeatureRefAbsent() && git.featureRefPresent) ||
		(!intent.ExpectedFeatureRefAbsent() && !git.featureRefPresent) ||
		git.featureHead != intent.ExpectedFeatureHead() ||
		!git.objects[intent.Digest().String()] {
		return errors.New("concurrent publication precondition failed")
	}
	if fault != nil {
		if err := fault(
			workspace.IntegrationFaultAfterRefPrepared,
		); err != nil {
			return fmt.Errorf(
				"integration lifecycle fault at %s: %w",
				workspace.IntegrationFaultAfterRefPrepared,
				err,
			)
		}
	}
	git.featureHead = intent.ExpectedMerge()
	git.featureRefPresent = true
	return nil
}

func (git *concurrentIntegrationGitStub) VerifyCompletedIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	chain []workspace.MergeUnitIntegrationIntent,
) error {
	git.mu.Lock()
	defer git.mu.Unlock()
	if len(chain) == 0 || !git.featureRefPresent ||
		git.featureHead != chain[len(chain)-1].ExpectedMerge() {
		return errors.New(
			"concurrent completed integration verification failed",
		)
	}
	for _, intent := range chain {
		if !git.objects[intent.Digest().String()] {
			return errors.New(
				"concurrent completed integration chain is missing an object",
			)
		}
	}
	return nil
}

type noReviewIntegrationHarness struct {
	attemptHarness
	attempt    workspace.RuntimeAttemptProjection
	repository *reviewRepositoryStub
	git        *integrationGitStub
	adoption   workspace.AttemptHeadAdoptionResult
}

func TestNoReviewIntegrationRequiresDurableSameHeadAdoption(t *testing.T) {
	t.Parallel()

	harness := newAttemptHarness(t, "unit-one")
	attempt := harness.reserve(t, "2026-07-25T10:00:00Z")
	tree := mustGitObject(t, 'b')
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(
		attempt.VerifiedHead(), tree, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{snapshot: repositorySnapshot}
	git := &integrationGitStub{featureHead: harness.base}
	_, err = workspace.IntegrateMergeUnit(
		context.Background(), harness.journal, harness.definition,
		repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T10:00:02Z"),
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "durable adopt-head evidence") {
		t.Fatalf("integration without adopt-head error = %v", err)
	}
	if git.inspectCalls != 0 {
		t.Fatalf(
			"integration inspected Git before acceptance: calls=%d",
			git.inspectCalls,
		)
	}

	adoption, err := workspace.AdoptAttemptHead(
		context.Background(), harness.journal, harness.definition, repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T10:00:03Z"),
		},
	)
	if err != nil || !adoption.Adopted() ||
		adoption.Record().Sequence() == 0 ||
		adoption.Head() != attempt.VerifiedHead() {
		t.Fatalf("same-head adoption = %#v error=%v", adoption, err)
	}
	result, err := workspace.IntegrateMergeUnit(
		context.Background(), harness.journal, harness.definition,
		repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T10:00:04Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Intent().AcceptanceMode() !=
		workspace.IntegrationAcceptanceAdoptedHead ||
		result.Intent().AdoptedHeadEventDigest().IsZero() ||
		!result.Intent().ReviewReadinessDigest().IsZero() {
		t.Fatalf(
			"same-head integration acceptance = %#v",
			result.Intent(),
		)
	}
}

func TestIntegrationCompletionAccountsForAndReleasesLeaseAndSerialSegment(
	t *testing.T,
) {
	t.Parallel()

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
	core := newAttemptHarnessFromFixture(t, fixture, "unit-one")
	attempt := core.reserve(t, "2026-07-25T10:10:00Z")
	head, tree := mustGitObject(t, 'c'), mustGitObject(t, 'd')
	repository := adoptedIntegrationRepository(
		t, core, attempt, head, tree, "2026-07-25T10:10:02Z",
	)
	snapshot, err := core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ = runtime.Attempt(attempt.AttemptID())
	_, err = workspace.IntegrateMergeUnit(
		context.Background(), core.journal, core.definition,
		repository, &integrationGitStub{featureHead: core.base},
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T10:10:03Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	completedSnapshot, err := core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	completedRuntime, err := workspace.RebuildWorkspaceRuntime(
		completedSnapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	completed, _ := completedRuntime.Attempt(attempt.AttemptID())
	if !completed.LeaseID().IsZero() ||
		completed.SerialSegmentHeld() {
		t.Fatalf(
			"integration did not release active resources: %#v",
			completed,
		)
	}
	core.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	next := core.reserve(t, "2026-07-25T10:10:04Z")
	if !next.SerialSegmentHeld() ||
		next.SerialSegment() != attempt.SerialSegment() {
		t.Fatalf(
			"released serial segment was not reusable: %#v",
			next,
		)
	}
}

func TestIntegrationDoesNotRunConfiguredChecksAfterIntent(t *testing.T) {
	t.Parallel()

	core := newAttemptHarnessFromFixture(t, configuredCommitProtocolFixture(t), "unit-one")
	attempt := core.reserve(t, "2026-07-25T10:30:00Z")
	tree := mustGitObject(t, 'b')
	snapshot, err := workspace.NewReviewRepositorySnapshot(attempt.VerifiedHead(), tree, true)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{
		snapshot: snapshot,
		finalHistory: func(run int) error {
			if run > 2 {
				return errors.New("configured check full-suite did not exit zero")
			}
			return nil
		},
	}
	if _, err := workspace.AdoptAttemptHead(
		context.Background(), core.journal, core.definition, repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-25T10:30:01Z"),
		},
	); err != nil {
		t.Fatalf("adopt configured final history: %v", err)
	}
	if repository.finalHistoryRuns != 1 {
		t.Fatalf("head adoption final-history runs = %d, want 1", repository.finalHistoryRuns)
	}

	git := &integrationGitStub{featureHead: core.base}
	result, err := workspace.IntegrateMergeUnit(
		context.Background(), core.journal, core.definition, repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-25T10:30:02Z"),
		},
	)
	if err != nil {
		t.Fatalf("integration with post-intent check failure = %v", err)
	}
	if result.Attempt().Phase() != workspace.AttemptCompleted {
		t.Fatalf("integration did not complete after pre-intent checks: %#v", result.Attempt())
	}
	if repository.finalHistoryRuns != 2 || git.createCalls != 1 || git.publishCalls != 1 {
		t.Fatalf(
			"integration ran configured checks=%d creates=%d publishes=%d",
			repository.finalHistoryRuns, git.createCalls, git.publishCalls,
		)
	}
}

func TestIntegrationRecoversEveryDurableEffectBoundaryDeterministically(
	t *testing.T,
) {
	t.Parallel()

	points := []workspace.IntegrationLifecycleFaultPoint{
		workspace.IntegrationFaultAfterIntentSynced,
		workspace.IntegrationFaultBeforeCommitCreate,
		workspace.IntegrationFaultAfterCommitCreated,
		workspace.IntegrationFaultBeforeRefCAS,
		workspace.IntegrationFaultAfterRefPrepared,
		workspace.IntegrationFaultAfterRefCAS,
		workspace.IntegrationFaultAfterVerification,
		workspace.IntegrationFaultBeforeCompletion,
		workspace.IntegrationFaultAfterCompletion,
	}
	for _, point := range points {
		t.Run(string(point), func(t *testing.T) {
			requireFullSuiteCase(
				t,
				point == workspace.IntegrationFaultBeforeCommitCreate ||
					point == workspace.IntegrationFaultAfterRefCAS,
				"intermediate integration durability boundary",
			)

			harness := newNoReviewIntegrationHarness(t, false)
			_, err := workspace.IntegrateMergeUnit(
				context.Background(),
				harness.journal,
				harness.definition,
				harness.repository,
				harness.git,
				workspace.IntegrateMergeUnitRequest{
					AttemptID:  harness.attempt.AttemptID(),
					OccurredAt: mustTime(t, "2026-07-25T11:00:00Z"),
					Fault:      failIntegrationOnce(point),
				},
			)
			if err == nil ||
				!strings.Contains(err.Error(), string(point)) {
				t.Fatalf("injected integration fault error = %v", err)
			}
			pendingMerge := integrationMergeFromRuntime(
				t, harness.journal, harness.attempt.AttemptID(),
			)
			result, err := workspace.IntegrateMergeUnit(
				context.Background(),
				harness.journal,
				harness.definition,
				harness.repository,
				harness.git,
				workspace.IntegrateMergeUnitRequest{
					AttemptID: harness.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T11:00:15Z",
					),
				},
			)
			if err != nil {
				t.Fatalf("recover integration after %s: %v", point, err)
			}
			if result.MergeCommit() != pendingMerge ||
				harness.git.featureHead != pendingMerge {
				t.Fatalf(
					"recovered merge after %s = %s, feature=%s, expected=%s",
					point, result.MergeCommit(),
					harness.git.featureHead, pendingMerge,
				)
			}
			assertSingleIntegrationTransition(
				t, harness.journal, harness.attempt.AttemptID(),
			)
			before := journalRecordCount(t, harness.journal)
			retry, err := workspace.IntegrateMergeUnit(
				context.Background(),
				harness.journal,
				harness.definition,
				harness.repository,
				harness.git,
				workspace.IntegrateMergeUnitRequest{
					AttemptID: harness.attempt.AttemptID(),
					OccurredAt: mustTime(
						t, "2026-07-25T11:00:30Z",
					),
				},
			)
			if err != nil || retry.MergeCommit() != pendingMerge ||
				journalRecordCount(t, harness.journal) != before {
				t.Fatalf(
					"idempotent integration retry after %s = %#v error=%v",
					point, retry, err,
				)
			}
		})
	}
}

func TestPendingIntegrationIntentFreezesAttemptAcceptance(t *testing.T) {
	t.Parallel()

	harness := newNoReviewIntegrationHarness(t, false)
	_, err := workspace.IntegrateMergeUnit(
		context.Background(), harness.journal, harness.definition,
		harness.repository, harness.git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:00:00Z"),
			Fault: failIntegrationOnce(
				workspace.IntegrationFaultAfterIntentSynced,
			),
		},
	)
	if err == nil {
		t.Fatal("integration unexpectedly passed the intent fault")
	}
	before := journalRecordCount(t, harness.journal)
	changedSnapshot, err := workspace.NewReviewRepositorySnapshot(
		mustGitObject(t, 'e'), mustGitObject(t, 'f'), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	harness.repository.snapshot = changedSnapshot
	if _, err := workspace.AdoptAttemptHead(
		context.Background(), harness.journal, harness.definition,
		harness.repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:00:01Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("post-intent head adoption error = %v", err)
	}
	if journalRecordCount(t, harness.journal) != before {
		t.Fatal("rejected post-intent mutation changed the journal")
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.RebuildWorkspaceRuntime(snapshot); err != nil {
		t.Fatalf("rejected post-intent mutation corrupted replay: %v", err)
	}
}

func TestPendingIntegrationIntentSerializesOtherMergeUnits(t *testing.T) {
	t.Parallel()

	firstCore := newIndependentAttemptHarness(t, "unit-one")
	first := firstCore.reserve(t, "2026-07-25T12:30:00Z")
	secondCore := firstCore
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	second := secondCore.reserve(t, "2026-07-25T12:30:02Z")
	firstRepository := adoptedIntegrationRepository(
		t, firstCore, first, mustGitObject(t, 'c'),
		mustGitObject(t, 'd'), "2026-07-25T12:30:04Z",
	)
	secondRepository := adoptedIntegrationRepository(
		t, secondCore, second, mustGitObject(t, 'e'),
		mustGitObject(t, 'f'), "2026-07-25T12:30:05Z",
	)
	git := &integrationGitStub{featureHead: firstCore.base}
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(), firstCore.journal, firstCore.definition,
		firstRepository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  first.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:30:06Z"),
			Fault: failIntegrationOnce(
				workspace.IntegrationFaultAfterIntentSynced,
			),
		},
	); err == nil {
		t.Fatal("first integration unexpectedly passed its intent fault")
	}
	before := journalRecordCount(t, firstCore.journal)
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(), firstCore.journal, firstCore.definition,
		secondRepository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID: second.AttemptID(),
			OccurredAt: mustTime(
				t, "2026-07-25T12:30:07Z",
			),
		},
	); err == nil ||
		!strings.Contains(err.Error(), "conflicts with pending attempt") {
		t.Fatalf("concurrent integration serialization error = %v", err)
	}
	if journalRecordCount(t, firstCore.journal) != before {
		t.Fatal("rejected concurrent integration appended journal state")
	}
	replayedReservation, err := workspace.StartAttempt(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		secondCore.git,
		workspace.StartAttemptRequest{
			MergeUnit:     second.MergeUnit(),
			AttemptNumber: second.AttemptNumber(),
			Goal:          second.Goal(),
			OccurredAt: mustTime(
				t, "2026-07-25T12:30:08Z",
			),
		},
	)
	if err != nil ||
		replayedReservation.AttemptID() != second.AttemptID() {
		t.Fatalf(
			"retry existing reservation during pending integration = %#v error=%v",
			replayedReservation, err,
		)
	}
	replacementGoal, err := workspace.NewGoalBinding(
		workspace.MustID("pending-intent-replacement"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.StartAttempt(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		secondCore.git,
		workspace.StartAttemptRequest{
			MergeUnit:     second.MergeUnit(),
			AttemptNumber: 2,
			Goal:          replacementGoal,
			OccurredAt: mustTime(
				t, "2026-07-25T12:30:09Z",
			),
		},
	); err == nil ||
		!strings.Contains(
			err.Error(), "conflicts with pending integration",
		) {
		t.Fatalf("reservation during pending integration error = %v", err)
	}
	snapshot, err := firstCore.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	first, _ = runtime.Attempt(first.AttemptID())
	second, _ = runtime.Attempt(second.AttemptID())
	if _, exists := first.Integration(); !exists {
		t.Fatal("first pending integration intent disappeared")
	}
	if _, exists := second.Integration(); exists {
		t.Fatal("second attempt acquired a concurrent integration intent")
	}
}

func TestConcurrentIntegrationsPublishExactlyOneIntent(t *testing.T) {
	t.Parallel()

	firstCore := newIndependentAttemptHarness(t, "unit-one")
	first := firstCore.reserve(t, "2026-07-25T12:45:00Z")
	secondCore := firstCore
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	second := secondCore.reserve(t, "2026-07-25T12:45:02Z")
	firstRepository := adoptedIntegrationRepository(
		t, firstCore, first, mustGitObject(t, 'c'),
		mustGitObject(t, 'd'), "2026-07-25T12:45:04Z",
	)
	secondRepository := adoptedIntegrationRepository(
		t, secondCore, second, mustGitObject(t, 'e'),
		mustGitObject(t, 'f'), "2026-07-25T12:45:05Z",
	)
	git := &concurrentIntegrationGitStub{
		featureHead:    firstCore.base,
		initialSeen:    make(map[string]bool),
		initialRelease: make(chan struct{}),
		objects:        make(map[string]bool),
	}
	type outcome struct {
		result workspace.MergeUnitIntegrationResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	occurredAt := mustTime(t, "2026-07-25T12:45:06Z")
	integrate := func(
		attempt workspace.RuntimeAttemptProjection,
		repository *reviewRepositoryStub,
	) {
		result, err := workspace.IntegrateMergeUnit(
			context.Background(),
			firstCore.journal,
			firstCore.definition,
			repository,
			git,
			workspace.IntegrateMergeUnitRequest{
				AttemptID:  attempt.AttemptID(),
				OccurredAt: occurredAt,
			},
		)
		outcomes <- outcome{result: result, err: err}
	}
	go integrate(first, firstRepository)
	go integrate(second, secondRepository)
	firstOutcome, secondOutcome := <-outcomes, <-outcomes
	successes, failures := 0, 0
	var winner workspace.MergeUnitIntegrationResult
	for _, candidate := range []outcome{
		firstOutcome, secondOutcome,
	} {
		if candidate.err == nil {
			successes++
			winner = candidate.result
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf(
			"concurrent integration outcomes: first=%v second=%v",
			firstOutcome.err, secondOutcome.err,
		)
	}
	if git.featureHead != winner.MergeCommit() {
		t.Fatalf(
			"concurrent feature head = %s, winner %s",
			git.featureHead, winner.MergeCommit(),
		)
	}
	snapshot, err := firstCore.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	intended, completed := 0, 0
	for _, record := range snapshot.Records() {
		switch record.EventType() {
		case workspace.JournalEventMergeUnitIntegrationIntended:
			intended++
		case workspace.JournalEventMergeUnitIntegrated:
			completed++
		}
	}
	if intended != 1 || completed != 1 {
		t.Fatalf(
			"concurrent integration journal: intended=%d completed=%d",
			intended, completed,
		)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatalf("concurrent integration replay: %v", err)
	}
	loser := first
	loserCore := firstCore
	if winner.Attempt().AttemptID() == first.AttemptID() {
		loser = second
		loserCore = secondCore
	}
	superseded, exists := runtime.Attempt(loser.AttemptID())
	if !exists ||
		superseded.Phase() != workspace.AttemptSuperseded ||
		!superseded.LeaseID().IsZero() ||
		superseded.SerialSegmentHeld() {
		t.Fatalf(
			"concurrent loser was not terminalized exactly: %#v exists=%t",
			superseded, exists,
		)
	}
	view, err := workspace.RebuildWorkspaceView(
		snapshot, firstCore.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	viewHasSupersededAttempt := false
	for _, candidate := range view.Attempts {
		if candidate.AttemptID == loser.AttemptID().String() {
			viewHasSupersededAttempt = candidate.Phase == workspace.AttemptSuperseded
		}
	}
	if !viewHasSupersededAttempt {
		t.Fatalf("workspace view omits superseded concurrent loser: %#v", view.Attempts)
	}
	if err := validatePublishedWorkspaceAttemptSchema(view, loser.AttemptID()); err != nil {
		t.Fatalf("published workspace schema rejects reachable superseded view: %v", err)
	}
	scheduler := view.Scheduler
	loserReady := false
	for _, unit := range scheduler.Units {
		if unit.PlanID == loser.MergeUnit().PlanID().String() &&
			unit.MergeUnitID ==
				loser.MergeUnit().MergeUnitID().String() {
			loserReady = unit.Status == workspace.SchedulerUnitReady
		}
	}
	if !loserReady {
		t.Fatal("superseded concurrent loser is not scheduler-ready")
	}
	replacementGoal, err := workspace.NewGoalBinding(
		workspace.MustID("replacement-goal"),
		workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := workspace.StartAttempt(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		loserCore.git,
		workspace.StartAttemptRequest{
			MergeUnit:     loser.MergeUnit(),
			AttemptNumber: 2,
			Goal:          replacementGoal,
			OccurredAt: mustTime(
				t, "2026-07-25T12:45:07Z",
			),
		},
	)
	if err != nil ||
		replacement.Base() != winner.MergeCommit() {
		t.Fatalf(
			"reserve replacement for superseded loser = %#v error=%v",
			replacement, err,
		)
	}
}

func validatePublishedWorkspaceAttemptSchema(view workspace.WorkspaceView, attemptID workspace.ID) error {
	var selected *workspace.WorkspaceAttempt
	for index := range view.Attempts {
		if view.Attempts[index].AttemptID == attemptID.String() {
			selected = &view.Attempts[index]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("workspace view has no attempt %s", attemptID)
	}
	viewSchema := workspacecmd.WorkspaceViewSchema()
	properties := viewSchema["properties"].(map[string]any)
	attemptSchema := properties["attempts"].(map[string]any)["items"].(map[string]any)
	encoded, err := json.Marshal(selected)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return err
	}
	return validatePublishedSchemaValue(attemptSchema, value, "attempt")
}

func validatePublishedSchemaValue(schema map[string]any, value any, path string) error {
	if enum, exists := schema["enum"].([]string); exists {
		actual, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s enum value is %T", path, value)
		}
		for _, allowed := range enum {
			if actual == allowed {
				break
			}
			if allowed == enum[len(enum)-1] {
				return fmt.Errorf("%s value %q is outside enum %q", path, actual, enum)
			}
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is %T, want object", path, value)
		}
		for _, required := range schema["required"].([]string) {
			if _, exists := object[required]; !exists {
				return fmt.Errorf("%s omits required field %q", path, required)
			}
		}
		properties := schema["properties"].(map[string]any)
		for name, field := range object {
			fieldSchema, known := properties[name]
			if !known {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("%s has unknown field %q", path, name)
				}
				continue
			}
			if err := validatePublishedSchemaValue(fieldSchema.(map[string]any), field, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s is %T, want array", path, value)
		}
		itemSchema := schema["items"].(map[string]any)
		for index, item := range array {
			if err := validatePublishedSchemaValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s is %T, want string", path, value)
		}
		if minimum, exists := schema["minLength"].(int); exists && len(text) < minimum {
			return fmt.Errorf("%s length %d is below %d", path, len(text), minimum)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) {
			return fmt.Errorf("%s is %v, want integer", path, value)
		}
		if minimum, exists := schema["minimum"].(int); exists && number < float64(minimum) {
			return fmt.Errorf("%s value %v is below %d", path, number, minimum)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s is %T, want boolean", path, value)
		}
	}
	return nil
}

func TestIntegrationMakesTerminalGateAttemptRetryableThroughGenericAbandonment(
	t *testing.T,
) {
	t.Parallel()
	harness := newGatedReviewHarness(t)
	dispatched := harness.dispatch(t, "2026-07-25T12:50:01Z")
	harness.record(t, dispatched.Dispatch(), workspace.ReviewGateFailedToRun, "2026-07-25T12:50:02Z")
	if _, err := workspace.AbandonAttempt(
		harness.journal, harness.definition,
		workspace.AbandonAttemptRequest{AttemptID: harness.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-25T12:50:03Z")},
	); err != nil {
		t.Fatalf("generic abandonment after terminal gate: %v", err)
	}
	replacementGoal, err := workspace.NewGoalBinding(workspace.MustID("terminal-gate-replacement"), workspace.GoalScopeMergeUnit)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := workspace.StartAttempt(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.StartAttemptRequest{
			MergeUnit: harness.attempt.MergeUnit(), AttemptNumber: 2, Goal: replacementGoal,
			OccurredAt: mustTime(t, "2026-07-25T12:50:04Z"),
		},
	)
	if err != nil || replacement.AttemptNumber() != 2 {
		t.Fatalf("replacement after terminal gate = %#v error=%v", replacement, err)
	}
}

func TestCompletedIntegrationRetryFollowsLaterDurableFrontier(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "multi-integration frontier permutation")

	firstCore := newIndependentAttemptHarness(t, "unit-one")
	first := firstCore.reserve(t, "2026-07-25T12:55:00Z")
	firstRepository := adoptedIntegrationRepository(
		t, firstCore, first, mustGitObject(t, 'c'),
		mustGitObject(t, 'd'), "2026-07-25T12:55:02Z",
	)
	git := &integrationGitStub{featureHead: firstCore.base}
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		firstRepository,
		git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  first.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:55:03Z"),
			Fault: failIntegrationOnce(
				workspace.IntegrationFaultAfterCompletion,
			),
		},
	); err == nil {
		t.Fatal("first integration unexpectedly passed its completion fault")
	}
	firstMerge := integrationMergeFromRuntime(
		t, firstCore.journal, first.AttemptID(),
	)

	secondCore := firstCore
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	secondCore.goal, _ = workspace.NewGoalBinding(
		workspace.MustID("second-integration-goal"),
		workspace.GoalScopeMergeUnit,
	)
	second := secondCore.reserve(t, "2026-07-25T12:55:04Z")
	secondRepository := adoptedIntegrationRepository(
		t, secondCore, second, mustGitObject(t, 'e'),
		mustGitObject(t, 'f'), "2026-07-25T12:55:06Z",
	)
	secondResult, err := workspace.IntegrateMergeUnit(
		context.Background(),
		secondCore.journal,
		secondCore.definition,
		secondRepository,
		git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  second.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:55:07Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	beforeRetry := journalRecordCount(t, firstCore.journal)
	retry, err := workspace.IntegrateMergeUnit(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		firstRepository,
		git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  first.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:55:08Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if retry.MergeCommit() != firstMerge ||
		retry.Record().Sequence() != 0 ||
		journalRecordCount(t, firstCore.journal) != beforeRetry {
		t.Fatalf(
			"historical completed retry mutated state: result=%#v records=%d/%d",
			retry, beforeRetry,
			journalRecordCount(t, firstCore.journal),
		)
	}
	if git.verifyCalls != 1 ||
		git.verifiedStart != firstMerge ||
		git.verifiedEnd != secondResult.MergeCommit() {
		t.Fatalf(
			"historical completed verification = calls %d, start %s, end %s",
			git.verifyCalls, git.verifiedStart, git.verifiedEnd,
		)
	}
}

func TestReviewReadyIntegrationBindsExactHeadTreeAndReadiness(t *testing.T) {
	t.Parallel()

	harness := newGatedReviewHarness(t)
	git := &integrationGitStub{featureHead: harness.base}
	_, err := workspace.IntegrateMergeUnit(
		context.Background(), harness.journal, harness.definition,
		harness.repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T13:00:00Z"),
		},
	)
	if err == nil {
		t.Fatal("integration without review readiness succeeded")
	}

	dispatched := harness.dispatch(t, "2026-07-25T13:00:01Z")
	harness.record(t, dispatched.Dispatch(), workspace.ReviewGateSatisfied, "2026-07-25T13:00:02Z")
	readiness, err := workspace.ConfirmReviewMergeReadiness(
		context.Background(), harness.journal, harness.definition,
		harness.repository, harness.attempt.AttemptID(),
	)
	if err != nil {
		t.Fatal(err)
	}

	stale, err := workspace.NewReviewRepositorySnapshot(
		harness.attempt.VerifiedHead(), mustGitObject(t, 'c'), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	exact := harness.repository.snapshot
	harness.repository.snapshot = stale
	if _, err := workspace.IntegrateMergeUnit(
		context.Background(), harness.journal, harness.definition,
		harness.repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T13:00:04Z"),
		},
	); err == nil ||
		!strings.Contains(err.Error(), "exact head and tree") {
		t.Fatalf("stale review readiness error = %v", err)
	}
	harness.repository.snapshot = exact

	result, err := workspace.IntegrateMergeUnit(
		context.Background(), harness.journal, harness.definition,
		harness.repository, git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T13:00:05Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Intent().AcceptanceMode() !=
		workspace.IntegrationAcceptanceReviewReady ||
		result.Intent().ReviewReadinessDigest() != readiness.Digest() ||
		!result.Intent().AdoptedHeadEventDigest().IsZero() ||
		result.Intent().AcceptedHead() != readiness.Head() ||
		result.Intent().AcceptedTree() != readiness.Tree() {
		t.Fatalf("review-ready integration intent = %#v", result.Intent())
	}
}

func TestExecutionConfigRejectsPostIntegrationChecks(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	configuration := string(fixture.sources.ExecutionConfig.Bytes)
	configuration = strings.Replace(
		configuration,
		"    boundary:\n      checkpoint: pause_only\n      escalation: allowed",
		`    post_integration_checks:
      - id: parent-sensitive
        command:
          - git
          - show
          - --first-parent
    boundary:
      checkpoint: pause_only
      escalation: allowed`,
		1,
	)
	if configuration == string(fixture.sources.ExecutionConfig.Bytes) {
		t.Fatal("failed to install post-integration check fixture")
	}
	if _, err := workspace.DecodeExecutionConfig(
		[]byte(configuration),
	); err == nil ||
		!strings.Contains(err.Error(), "post_integration_checks") {
		t.Fatalf("post-integration check configuration error = %v", err)
	}
}

func TestCompletedIntegrationUnblocksDependentSchedulerUnit(t *testing.T) {
	t.Parallel()

	harness := newNoReviewIntegrationHarness(t, false)
	result, err := workspace.IntegrateMergeUnit(
		context.Background(), harness.journal, harness.definition,
		harness.repository, harness.git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T14:00:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	view, err := workspace.RebuildWorkspaceView(
		snapshot, harness.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := view.Scheduler
	var first, second workspace.WorkspaceUnitState
	for _, unit := range scheduler.Units {
		switch unit.MergeUnitID {
		case "unit-one":
			first = unit
		case "unit-two":
			second = unit
		}
	}
	if first.Status != workspace.SchedulerUnitCompleted ||
		second.Status != workspace.SchedulerUnitReady ||
		len(second.Blockers) != 0 {
		t.Fatalf(
			"scheduler after integration: first=%#v second=%#v",
			first, second,
		)
	}
	report := view
	if report.Target.FeatureHead != result.MergeCommit().String() {
		t.Fatalf(
			"reported feature frontier = %s, want %s",
			report.Target.FeatureHead, result.MergeCommit(),
		)
	}
	integrated := false
	for _, unit := range report.Integration.Units {
		if unit.MergeUnitID == "unit-one" {
			integrated = unit.Status == "integrated" &&
				unit.AttemptID == harness.attempt.AttemptID().String()
		}
	}
	if !integrated {
		t.Fatalf(
			"workspace integration report = %#v",
			report.Integration,
		)
	}
}

func adoptedIntegrationRepository(
	t *testing.T,
	core attemptHarness,
	attempt workspace.RuntimeAttemptProjection,
	head, tree workspace.GitObjectID,
	at string,
) *reviewRepositoryStub {
	t.Helper()
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(
		head, tree, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{snapshot: repositorySnapshot}
	if _, err := workspace.AdoptAttemptHead(
		context.Background(), core.journal, core.definition, repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, at),
		},
	); err != nil {
		t.Fatal(err)
	}
	return repository
}

func configuredCommitProtocolFixture(t *testing.T) definitionFixture {
	t.Helper()
	fixture := newDefinitionFixture(t)
	configuration := string(fixture.sources.ExecutionConfig.Bytes)
	needle := "      max_attempts: 3\n  - plan_id: alpha-plan\n    merge_unit_id: unit-two"
	replacement := `      max_attempts: 3
    commit_protocol:
      steps:
        - id: implementation
          subject: Implement protocol
          body_policy: forbidden
          allowed_paths:
            - src/**
          frozen_paths: []
          checks:
            - id: full-suite
              runner: codex
              command:
                - go
                - test
                - ./...
  - plan_id: alpha-plan
    merge_unit_id: unit-two`
	updated := strings.Replace(configuration, needle, replacement, 1)
	if updated == configuration {
		t.Fatal("failed to configure final-history fixture")
	}
	fixture.sources.ExecutionConfig.Bytes = []byte(updated)
	return fixture
}

func newNoReviewIntegrationHarness(
	t *testing.T,
	sameHead bool,
) *noReviewIntegrationHarness {
	t.Helper()
	core := newAttemptHarness(t, "unit-one")
	attempt := core.reserve(t, "2026-07-25T09:00:00Z")
	head := mustGitObject(t, 'c')
	if sameHead {
		head = attempt.VerifiedHead()
	}
	tree := mustGitObject(t, 'd')
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(
		head, tree, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{snapshot: repositorySnapshot}
	adoption, err := workspace.AdoptAttemptHead(
		context.Background(), core.journal, core.definition, repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T09:00:02Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, exists := runtime.Attempt(attempt.AttemptID())
	if !exists || attempt.VerifiedHead() != head {
		t.Fatalf("adopted integration attempt = %#v exists=%t", attempt, exists)
	}
	return &noReviewIntegrationHarness{
		attemptHarness: core,
		attempt:        attempt,
		repository:     repository,
		git: &integrationGitStub{
			featureHead: core.base,
		},
		adoption: adoption,
	}
}

func integrationMergeFromRuntime(
	t *testing.T,
	journal *workspace.WorkspaceJournal,
	attemptID workspace.ID,
) workspace.GitObjectID {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, exists := runtime.Attempt(attemptID)
	if !exists {
		t.Fatalf("attempt %s is absent", attemptID)
	}
	integration, exists := attempt.Integration()
	if !exists {
		t.Fatalf("attempt %s has no durable integration intent", attemptID)
	}
	return integration.MergeCommit()
}

func assertSingleIntegrationTransition(
	t *testing.T,
	journal *workspace.WorkspaceJournal,
	attemptID workspace.ID,
) {
	t.Helper()
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	intended, completed := 0, 0
	for _, record := range snapshot.Records() {
		switch record.EventType() {
		case workspace.JournalEventMergeUnitIntegrationIntended:
			intended++
		case workspace.JournalEventMergeUnitIntegrated:
			completed++
		}
	}
	if intended != 1 || completed != 1 {
		t.Fatalf(
			"attempt %s integration records: intended=%d completed=%d",
			attemptID, intended, completed,
		)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	attempt, exists := runtime.Attempt(attemptID)
	integration, integrated := attempt.Integration()
	if !exists || attempt.Phase() != workspace.AttemptCompleted ||
		!integrated || !integration.Integrated() {
		t.Fatalf(
			"completed integration projection = %#v exists=%t integration=%#v integrated=%t",
			attempt, exists, integration, integrated,
		)
	}
}

func failIntegrationOnce(
	wanted workspace.IntegrationLifecycleFaultPoint,
) workspace.IntegrationLifecycleFaultInjector {
	fired := false
	return func(actual workspace.IntegrationLifecycleFaultPoint) error {
		if actual == wanted && !fired {
			fired = true
			return errors.New("simulated integration crash")
		}
		return nil
	}
}
