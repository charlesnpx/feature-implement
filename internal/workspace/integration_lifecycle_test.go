package workspace_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type integrationGitStub struct {
	featureHead    workspace.GitObjectID
	expectedCommit bool
	inspectCalls   int
	createCalls    int
	publishCalls   int
	verifyCalls    int
	verifiedStart  workspace.GitObjectID
	verifiedEnd    workspace.GitObjectID
	forcedState    workspace.IntegrationRefState
}

type concurrentIntegrationGitStub struct {
	mu             sync.Mutex
	featureHead    workspace.GitObjectID
	initialSeen    map[string]bool
	initialCount   int
	initialRelease chan struct{}
	objects        map[string]bool
}

func (git *integrationGitStub) InspectAttempt(
	_ context.Context,
	target workspace.LocalTargetBinding,
	worktree, branch string,
	expectedHead, expectedTree workspace.GitObjectID,
) (workspace.AttemptGitInspection, error) {
	return stubIntegrationAttemptInspection(
		target, worktree, branch, expectedHead, expectedTree,
	)
}

func (git *integrationGitStub) InspectIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	_ string,
	intent workspace.MergeUnitIntegrationIntent,
) (workspace.IntegrationGitInspection, error) {
	git.inspectCalls++
	state := git.forcedState
	if state == "" {
		switch git.featureHead {
		case intent.ExpectedFeatureHead():
			state = workspace.IntegrationRefExpectedHead
		case intent.ExpectedMerge():
			state = workspace.IntegrationRefExpectedMerge
		default:
			state = workspace.IntegrationRefUnrelatedDrift
		}
	}
	return workspace.NewIntegrationGitInspection(
		git.featureHead, state, git.expectedCommit,
	)
}

func (git *integrationGitStub) CreateIntegrationCommit(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	_ string,
	intent workspace.MergeUnitIntegrationIntent,
) error {
	git.createCalls++
	if git.featureHead != intent.ExpectedFeatureHead() {
		return errors.New("stub feature head moved before commit creation")
	}
	git.expectedCommit = true
	return nil
}

func (git *integrationGitStub) PublishIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	_ string,
	intent workspace.MergeUnitIntegrationIntent,
	fault workspace.IntegrationLifecycleFaultInjector,
) error {
	git.publishCalls++
	if git.featureHead != intent.ExpectedFeatureHead() ||
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
	if git.featureHead != git.verifiedEnd ||
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
	worktree, branch string,
	expectedHead, expectedTree workspace.GitObjectID,
) (workspace.AttemptGitInspection, error) {
	return stubIntegrationAttemptInspection(
		target, worktree, branch, expectedHead, expectedTree,
	)
}

func (git *concurrentIntegrationGitStub) InspectIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	_ string,
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
		return workspace.NewIntegrationGitInspection(
			intent.ExpectedFeatureHead(),
			workspace.IntegrationRefExpectedHead,
			false,
		)
	}
	head := git.featureHead
	objectExists := git.objects[intent.Digest().String()]
	git.mu.Unlock()
	state := workspace.IntegrationRefUnrelatedDrift
	switch head {
	case intent.ExpectedFeatureHead():
		state = workspace.IntegrationRefExpectedHead
	case intent.ExpectedMerge():
		state = workspace.IntegrationRefExpectedMerge
	}
	return workspace.NewIntegrationGitInspection(
		head, state, objectExists,
	)
}

func stubIntegrationAttemptInspection(
	target workspace.LocalTargetBinding,
	worktree, branch string,
	expectedHead, expectedTree workspace.GitObjectID,
) (workspace.AttemptGitInspection, error) {
	identitySeed := uint64(len(worktree) + len(branch) + 1)
	binding, err := workspace.NewAttemptWorktreeGitBinding(
		workspace.AttemptWorktreeGitBindingOptions{
			Worktree: worktree,
			WorktreeIdentity: workspace.PlatformFileIdentity{
				Device: 1, Inode: identitySeed, Owner: 1,
			},
			GitDirectory: filepath.Join(
				target.CommonDirectory(), "worktrees", branch,
			),
			GitDirectoryIdentity: workspace.PlatformFileIdentity{
				Device: 1, Inode: identitySeed + 1, Owner: 1,
			},
			CommonDirectory:         target.CommonDirectory(),
			CommonDirectoryIdentity: target.CommonIdentity(),
			AdministrationDigest: workspace.DigestBytes(
				[]byte("stub administration " + worktree),
			),
			ConfigurationDigest: workspace.DigestBytes(
				[]byte("stub configuration " + worktree),
			),
		},
	)
	if err != nil {
		return workspace.AttemptGitInspection{}, err
	}
	return workspace.NewBoundAttemptGitInspection(
		expectedHead, branch, expectedHead, expectedTree,
		binding, true,
	)
}

func (git *concurrentIntegrationGitStub) CreateIntegrationCommit(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	_ string,
	intent workspace.MergeUnitIntegrationIntent,
) error {
	git.mu.Lock()
	defer git.mu.Unlock()
	if git.featureHead != intent.ExpectedFeatureHead() {
		return errors.New("concurrent feature head moved before commit")
	}
	git.objects[intent.Digest().String()] = true
	return nil
}

func (git *concurrentIntegrationGitStub) PublishIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	_ string,
	intent workspace.MergeUnitIntegrationIntent,
	fault workspace.IntegrationLifecycleFaultInjector,
) error {
	git.mu.Lock()
	defer git.mu.Unlock()
	if git.featureHead != intent.ExpectedFeatureHead() ||
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
	return nil
}

func (git *concurrentIntegrationGitStub) VerifyCompletedIntegration(
	_ context.Context,
	_ workspace.LocalTargetBinding,
	chain []workspace.MergeUnitIntegrationIntent,
) error {
	git.mu.Lock()
	defer git.mu.Unlock()
	if len(chain) == 0 ||
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
	attempt = harness.materialize(
		t, attempt.AttemptID(), "2026-07-25T10:00:01Z",
	)
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
		result.Intent().AdoptedHeadEventDigest() !=
			adoption.Record().EventHash() ||
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
		"    boundary:\n      mode: complete_goal_and_wait",
		"    boundary:\n      mode: complete_goal_and_wait\n      serial_segment: serial-alpha",
		1,
	))
	core := newAttemptHarnessFromFixture(t, fixture, "unit-one")
	attempt := core.reserve(t, "2026-07-25T10:10:00Z")
	attempt = core.materialize(
		t, attempt.AttemptID(), "2026-07-25T10:10:01Z",
	)
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
	leaseResource := workspace.LeaseJournalResource(attempt.LeaseID())
	segmentResource := workspace.SerialSegmentJournalResource(
		attempt.SerialSegment(),
	)
	leaseRevision := snapshot.Revision(leaseResource)
	segmentRevision := snapshot.Revision(segmentResource)
	result, err := workspace.IntegrateMergeUnit(
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
	assertResourceRevision := func(
		resource workspace.JournalResource,
		revision uint64,
	) {
		t.Helper()
		foundRead, foundWrite := false, false
		for _, binding := range result.Record().ReadSet() {
			if binding.Resource() == resource &&
				binding.Revision() == revision {
				foundRead = true
			}
		}
		for _, written := range result.Record().WriteSet() {
			if written == resource {
				foundWrite = true
			}
		}
		if !foundRead || !foundWrite {
			t.Fatalf(
				"integration completion resource %s/%s read=%t write=%t",
				resource.Kind(), resource.Identity(),
				foundRead, foundWrite,
			)
		}
	}
	assertResourceRevision(leaseResource, leaseRevision)
	assertResourceRevision(segmentResource, segmentRevision)
	completedSnapshot, err := core.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if completedSnapshot.Revision(leaseResource) != leaseRevision+1 ||
		completedSnapshot.Revision(segmentResource) !=
			segmentRevision+1 {
		t.Fatalf(
			"integration completion revisions: lease=%d segment=%d",
			completedSnapshot.Revision(leaseResource),
			completedSnapshot.Revision(segmentResource),
		)
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

func TestConfiguredCommitProtocolRequiresDurableSameHeadAdoption(
	t *testing.T,
) {
	t.Parallel()

	scenario := newJournalCommitScenario(t)
	committed, err := workspace.ExecuteAttemptCommitStep(
		context.Background(),
		scenario.harness.journal,
		scenario.harness.definition,
		scenario.shell,
		scenario.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt := committed.Attempt()
	protocol, configured := committed.Protocol()
	if !configured ||
		protocol.Phase() != workspace.CommitProtocolComplete ||
		attempt.VerifiedHead() != scenario.commit {
		t.Fatalf(
			"completed configured protocol = %#v configured=%t attempt=%#v",
			protocol, configured, attempt,
		)
	}
	repositorySnapshot, err := workspace.NewReviewRepositorySnapshot(
		scenario.commit, scenario.git.commit.Tree(), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reviewRepositoryStub{snapshot: repositorySnapshot}
	adoption, err := workspace.AdoptAttemptHead(
		context.Background(),
		scenario.harness.journal,
		scenario.harness.definition,
		repository,
		workspace.AdoptAttemptHeadRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T10:30:00Z"),
		},
	)
	if err != nil || !adoption.Adopted() ||
		adoption.Head() != scenario.commit ||
		adoption.Record().Sequence() == 0 {
		t.Fatalf(
			"configured-protocol same-head adoption = %#v error=%v",
			adoption, err,
		)
	}
	git := &integrationGitStub{featureHead: scenario.harness.base}
	result, err := workspace.IntegrateMergeUnit(
		context.Background(),
		scenario.harness.journal,
		scenario.harness.definition,
		repository,
		git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T10:30:01Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Intent().AdoptedHeadEventDigest() !=
		adoption.Record().EventHash() ||
		result.Intent().AcceptedHead() != scenario.commit {
		t.Fatalf(
			"configured-protocol integration acceptance = %#v",
			result.Intent(),
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
	first = firstCore.materialize(
		t, first.AttemptID(), "2026-07-25T12:30:01Z",
	)
	secondCore := firstCore
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	second := secondCore.reserve(t, "2026-07-25T12:30:02Z")
	secondCore.git.inspection, _ = workspace.NewAttemptGitInspection(
		false, workspace.GitObjectID{}, false, false, "",
		workspace.GitObjectID{}, false,
	)
	second = secondCore.materialize(
		t, second.AttemptID(), "2026-07-25T12:30:03Z",
	)
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
	replayedReservation, err := workspace.ReserveAttempt(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		secondCore.git,
		workspace.ReserveAttemptRequest{
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
	if _, err := workspace.ReserveAttempt(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		secondCore.git,
		workspace.ReserveAttemptRequest{
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
	first = firstCore.materialize(
		t, first.AttemptID(), "2026-07-25T12:45:01Z",
	)
	secondCore := firstCore
	secondCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	second := secondCore.reserve(t, "2026-07-25T12:45:02Z")
	secondCore.git.inspection, _ = workspace.NewAttemptGitInspection(
		false, workspace.GitObjectID{}, false, false, "",
		workspace.GitObjectID{}, false,
	)
	second = secondCore.materialize(
		t, second.AttemptID(), "2026-07-25T12:45:03Z",
	)
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
	if revision := snapshot.Revision(
		workspace.LeaseJournalResource(loser.LeaseID()),
	); revision == 0 {
		t.Fatal("concurrent loser lease resource was not released")
	}
	scheduler, err := workspace.RebuildSchedulerView(
		snapshot, firstCore.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
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
	replacement, err := workspace.ReserveAttempt(
		context.Background(),
		firstCore.journal,
		firstCore.definition,
		loserCore.git,
		workspace.ReserveAttemptRequest{
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

func TestIntegrationMakesReviewExhaustedLoserRetryableAtNewFrontier(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "multi-integration frontier permutation")

	fixture := configuredReviewFixture(t)
	fixture.sources.Plans[0].Bytes = []byte(strings.Replace(
		string(fixture.sources.Plans[0].Bytes),
		"    dependencies:\n      - story-one",
		"    dependencies: []",
		1,
	))
	loserCore := newAttemptHarnessFromFixture(
		t, fixture, "unit-one",
	)
	loser := loserCore.reserve(t, "2026-07-25T12:50:00Z")
	loser = loserCore.materialize(
		t, loser.AttemptID(), "2026-07-25T12:50:01Z",
	)
	loserTree := mustGitObject(t, 'b')
	loserSnapshot, err := workspace.NewReviewRepositorySnapshot(
		loser.VerifiedHead(), loserTree, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	loserReview := &reviewHarness{
		attemptHarness: loserCore,
		attempt:        loser,
		repository: &reviewRepositoryStub{
			snapshot: loserSnapshot,
		},
		tree: loserTree,
	}
	start, err := workspace.StartAttemptReviewRound(
		context.Background(),
		loserCore.journal,
		loserCore.definition,
		loserReview.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID:  loser.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:50:02Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := start.Request()
	for invocation := 1; invocation <= 3; invocation++ {
		submission := reviewSubmission(
			t,
			request,
			workspace.MustID("security-one"),
			workspace.ReviewResultInfrastructureFailure,
			nil,
			workspace.DigestBytes(
				[]byte(fmt.Sprintf(
					"loser-review-failure-%d", invocation,
				)),
			),
		)
		loserReview.record(
			t,
			request,
			submission,
			fmt.Sprintf(
				"2026-07-25T12:50:%02dZ", invocation+2,
			),
		)
		state := mustReviewState(
			t, loserCore.journal, loserCore.definition,
			loser.AttemptID(),
		)
		if invocation == 3 {
			if _, exhausted := state.Exhaustion(); !exhausted {
				t.Fatal("losing attempt did not exhaust its review budget")
			}
			break
		}
		var ok bool
		request, ok, err = state.NextRequest()
		if err != nil || !ok {
			t.Fatalf(
				"next losing review request = %#v ok=%t err=%v",
				request, ok, err,
			)
		}
	}

	winnerCore := loserCore
	winnerCore.unit = mustMergeUnitReference(
		t, "alpha-plan", "unit-two",
	)
	winnerCore.goal, _ = workspace.NewGoalBinding(
		workspace.MustID("winner-goal"),
		workspace.GoalScopeMergeUnit,
	)
	winner := winnerCore.reserve(t, "2026-07-25T12:50:06Z")
	winnerCore.git.inspection, _ = workspace.NewAttemptGitInspection(
		false, workspace.GitObjectID{}, false, false, "",
		workspace.GitObjectID{}, false,
	)
	winner = winnerCore.materialize(
		t, winner.AttemptID(), "2026-07-25T12:50:07Z",
	)
	winnerRepository := adoptedIntegrationRepository(
		t, winnerCore, winner, mustGitObject(t, 'c'),
		mustGitObject(t, 'd'), "2026-07-25T12:50:08Z",
	)
	git := &integrationGitStub{featureHead: loserCore.base}
	integrated, err := workspace.IntegrateMergeUnit(
		context.Background(),
		winnerCore.journal,
		winnerCore.definition,
		winnerRepository,
		git,
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  winner.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T12:50:09Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := loserCore.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := workspace.RebuildSchedulerView(
		snapshot, loserCore.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	loserUnit := schedulerUnitByID(
		t, scheduler, "unit-one",
	)
	if loserUnit.Status != workspace.SchedulerUnitReady ||
		loserUnit.AttemptID != "" {
		t.Fatalf(
			"superseded exhausted loser scheduler view = %#v",
			loserUnit,
		)
	}
	gates, err := workspace.RebuildGateView(
		snapshot, loserCore.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	loserGate := gateUnitByID(t, gates, "unit-one")
	if loserGate.AttemptID != "" ||
		gateCheckByName(
			t, loserGate, "commit",
		).Reason != "no_attempt" ||
		gateCheckByName(
			t, loserGate, "review",
		).Reason != "no_attempt" {
		t.Fatalf(
			"superseded exhausted loser gates = %#v",
			loserGate,
		)
	}
	replacementGoal, _ := workspace.NewGoalBinding(
		workspace.MustID("exhausted-loser-replacement"),
		workspace.GoalScopeMergeUnit,
	)
	replacement, err := workspace.ReserveAttempt(
		context.Background(),
		loserCore.journal,
		loserCore.definition,
		loserCore.git,
		workspace.ReserveAttemptRequest{
			MergeUnit:     loser.MergeUnit(),
			AttemptNumber: 2,
			Goal:          replacementGoal,
			OccurredAt: mustTime(
				t, "2026-07-25T12:50:10Z",
			),
		},
	)
	if err != nil ||
		replacement.Base() != integrated.MergeCommit() {
		t.Fatalf(
			"review-exhausted loser replacement = %#v error=%v",
			replacement, err,
		)
	}
}

func TestCompletedIntegrationRetryFollowsLaterDurableFrontier(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "multi-integration frontier permutation")

	firstCore := newIndependentAttemptHarness(t, "unit-one")
	first := firstCore.reserve(t, "2026-07-25T12:55:00Z")
	first = firstCore.materialize(
		t, first.AttemptID(), "2026-07-25T12:55:01Z",
	)
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
	secondCore.git.inspection, _ = workspace.NewAttemptGitInspection(
		false, workspace.GitObjectID{}, false, false, "",
		workspace.GitObjectID{}, false,
	)
	second = secondCore.materialize(
		t, second.AttemptID(), "2026-07-25T12:55:05Z",
	)
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

	harness := newReviewHarness(t)
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

	start, err := workspace.StartAttemptReviewRound(
		context.Background(), harness.journal, harness.definition,
		harness.repository,
		workspace.StartAttemptReviewRoundRequest{
			AttemptID:  harness.attempt.AttemptID(),
			OccurredAt: mustTime(t, "2026-07-25T13:00:01Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := reviewSubmission(
		t, start.Request(), workspace.MustID("security-one"),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	harness.record(
		t, start.Request(), first, "2026-07-25T13:00:02Z",
	)
	state := mustReviewState(
		t, harness.journal, harness.definition,
		harness.attempt.AttemptID(),
	)
	secondRequest, ok, err := state.NextRequest()
	if err != nil || !ok {
		t.Fatalf("second review request = %#v ok=%t err=%v", secondRequest, ok, err)
	}
	second := reviewSubmission(
		t, secondRequest, workspace.MustID("correctness-one"),
		workspace.ReviewResultCompleted, nil, workspace.Digest{},
	)
	harness.record(
		t, secondRequest, second, "2026-07-25T13:00:03Z",
	)
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
		!strings.Contains(err.Error(), "exact-head review readiness") {
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
		"    boundary:\n      mode: pause_only",
		`    post_integration_checks:
      - id: parent-sensitive
        command:
          - git
          - show
          - --first-parent
    boundary:
      mode: pause_only`,
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
	scheduler, err := workspace.RebuildSchedulerView(
		snapshot, harness.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	var first, second workspace.SchedulerUnitView
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
	report, err := workspace.RebuildWorkspaceReport(
		snapshot, harness.definition,
	)
	if err != nil {
		t.Fatal(err)
	}
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

func newNoReviewIntegrationHarness(
	t *testing.T,
	sameHead bool,
) *noReviewIntegrationHarness {
	t.Helper()
	core := newAttemptHarness(t, "unit-one")
	attempt := core.reserve(t, "2026-07-25T09:00:00Z")
	attempt = core.materialize(
		t, attempt.AttemptID(), "2026-07-25T09:00:01Z",
	)
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
