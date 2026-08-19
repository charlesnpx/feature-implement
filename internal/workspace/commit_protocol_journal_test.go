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

type journalCommitGit struct {
	branch       string
	head         workspace.GitObjectID
	staged       workspace.StagedCommitInspection
	commit       workspace.GitCommitInspection
	rangeCommits []workspace.GitCommitInspection
	beforeRange  func() error
	rangeCalls   int
	createCalls  int
	publishes    int
}

func TestJournaledCommitProtocolStartsFromRealStagedWorktree(t *testing.T) {
	t.Parallel()

	harness := newConfiguredAttemptHarness(t)
	attempt := harness.reserve(t, "2026-07-21T10:55:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T10:56:00Z")

	runGitSetup(
		t, "", "clone", "--no-local",
		harness.definition.Workspace().RepositoryRoot(),
		attempt.Worktree(),
	)
	runGitSetup(t, attempt.Worktree(), "config", "user.name", "Protocol Test")
	runGitSetup(t, attempt.Worktree(), "config", "user.email", "protocol@example.test")
	runGitSetup(
		t, attempt.Worktree(), "switch", "-c", attempt.Branch(),
		rawGitObject(attempt.Base()),
	)
	if err := os.MkdirAll(
		filepath.Join(attempt.Worktree(), "src"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(attempt.Worktree(), "src", "protocol.go")
	if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Durable = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, attempt.Worktree(), "add", "src/protocol.go")

	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, err := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), harness.journal, harness.definition, shell,
		workspace.ExecuteAttemptCommitStepRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:57:00Z"),
		},
	)
	if err != nil {
		t.Fatalf("execute durable protocol from staged worktree: %v", err)
	}
	state, configured := result.Protocol()
	head := parseGitHead(t, attempt.Worktree())
	if !configured || state.Phase() != workspace.CommitProtocolComplete || state.Head() != head ||
		result.Attempt().VerifiedHead() != head || head == attempt.Base() ||
		runnerCallCount(runner) != 1 {
		t.Fatalf("result=%#v state=%#v configured=%v head=%s checks=%d", result, state, configured, head, runnerCallCount(runner))
	}
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatalf("replay durable protocol: %v", err)
	}
	replayedAttempt, exists := replayed.Attempt(attempt.AttemptID())
	replayedState, hasProtocol := replayedAttempt.CommitProtocol()
	if !exists || !hasProtocol || replayedState.Phase() != workspace.CommitProtocolComplete || replayedState.Head() != head {
		t.Fatalf("replayed attempt=%#v exists=%v protocol=%#v hasProtocol=%v", replayedAttempt, exists, replayedState, hasProtocol)
	}

	reviewPath := filepath.Join(attempt.Worktree(), "src", "review.go")
	if err := os.WriteFile(reviewPath, []byte("package protocol\n\nconst Reviewed = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, attempt.Worktree(), "add", "src/review.go")
	reviewResult, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), harness.journal, harness.definition, shell,
		workspace.ExecuteAttemptReviewFixRequest{
			AttemptID: attempt.AttemptID(), Ordinal: 1, Body: "address accepted review feedback",
			OccurredAt: mustTime(t, "2026-07-21T10:58:00Z"),
		},
	)
	if err != nil {
		t.Fatalf("execute durable review fix from staged worktree: %v", err)
	}
	reviewState, configured := reviewResult.State()
	reviewHead := parseGitHead(t, attempt.Worktree())
	if !configured || reviewState.Phase() != workspace.ReviewFixComplete || reviewState.Base() != head ||
		reviewState.Head() != reviewHead || reviewResult.Attempt().VerifiedHead() != reviewHead ||
		reviewHead == head || runnerCallCount(runner) != 2 {
		t.Fatalf(
			"review result=%#v state=%#v configured=%v head=%s checks=%d",
			reviewResult, reviewState, configured, reviewHead, runnerCallCount(runner),
		)
	}
}

func (git *journalCommitGit) InspectStaged(context.Context, string, string) (workspace.StagedCommitInspection, error) {
	return git.staged, nil
}

func (git *journalCommitGit) CreateConfiguredCommit(
	_ context.Context,
	request workspace.CreateGitCommitRequest,
) (workspace.GitCommitInspection, error) {
	git.createCalls++
	if git.head == request.Parent() {
		git.head = git.commit.Commit()
		git.publishes++
	} else if git.head != git.commit.Commit() {
		return workspace.GitCommitInspection{}, errors.New("unexpected fake head")
	}
	return git.commit, nil
}

func (git *journalCommitGit) InspectCommit(context.Context, string, workspace.GitObjectID) (workspace.GitCommitInspection, error) {
	return git.commit, nil
}

func (git *journalCommitGit) InspectFirstParentRange(
	context.Context, string, workspace.GitObjectID, workspace.GitObjectID,
) ([]workspace.GitCommitInspection, error) {
	git.rangeCalls++
	if git.beforeRange != nil {
		hook := git.beforeRange
		git.beforeRange = nil
		if err := hook(); err != nil {
			return nil, err
		}
	}
	if git.rangeCommits != nil {
		return append([]workspace.GitCommitInspection(nil), git.rangeCommits...), nil
	}
	return []workspace.GitCommitInspection{git.commit}, nil
}

func (git *journalCommitGit) VerifyCleanWorktree(
	_ context.Context,
	_, branch string,
	head workspace.GitObjectID,
) error {
	if branch != git.branch || head != git.head {
		return errors.New("fake commit worktree verification failed")
	}
	return nil
}

func TestJournaledCommitProtocolRecoversCommitAndCheckCrashWindows(t *testing.T) {
	t.Parallel()

	harness := newConfiguredAttemptHarness(t)
	attempt := harness.reserve(t, "2026-07-21T11:00:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T11:01:00Z")

	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "premature"),
			OccurredAt: mustTime(t, "2026-07-21T11:02:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "before its configured commit protocol completes") {
		t.Fatalf("premature boundary error = %v", err)
	}

	step := configuredProtocolStep(t, harness.definition)
	tree, commitObject, changedObject := mustGitObject(t, 'b'), mustGitObject(t, 'c'), mustGitObject(t, 'd')
	diff := addedDiff(t, "src/protocol.go", changedObject)
	staged, err := workspace.NewStagedCommitInspection(harness.base, tree, diff, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	commitEvidence, err := workspace.NewCommitObjectEvidence(
		harness.definition.Generation(), step.ID(), 1, commitObject, harness.base, tree,
		step.Message().Subject(), "", diff, step.Paths().Digest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	commitInspection, err := workspace.NewGitCommitInspection(
		commitObject, []workspace.GitObjectID{harness.base}, tree,
		step.Message().Subject(), "", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = commitEvidence
	git := &journalCommitGit{
		branch: attempt.Branch(), head: harness.base, staged: staged, commit: commitInspection,
	}
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, _ := workspace.NewCommitProtocolShell(git, runner)

	request := workspace.ExecuteAttemptCommitStepRequest{
		AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T11:03:00Z"),
		Fault: commitFailOnce(workspace.CommitFaultAfterGitCommit),
	}
	result, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), harness.journal, harness.definition, shell, request,
	)
	if err == nil || !strings.Contains(err.Error(), string(workspace.CommitFaultAfterGitCommit)) {
		t.Fatalf("Git commit fault error = %v", err)
	}
	state, ok := result.Protocol()
	if !ok || state.Phase() != workspace.CommitProtocolAwaitingCommit || git.publishes != 1 {
		t.Fatalf("after commit crash state=%#v publishes=%d", state, git.publishes)
	}

	request.OccurredAt = mustTime(t, "2026-07-21T11:04:00Z")
	request.Fault = commitFailOnce(workspace.CommitFaultAfterCheckRun)
	result, err = workspace.ExecuteAttemptCommitStep(
		context.Background(), harness.journal, harness.definition, shell, request,
	)
	if err == nil || !strings.Contains(err.Error(), string(workspace.CommitFaultAfterCheckRun)) {
		t.Fatalf("check fault error = %v", err)
	}
	state, _ = result.Protocol()
	if state.Phase() != workspace.CommitProtocolAwaitingChecks || git.publishes != 1 || runnerCallCount(runner) != 1 {
		t.Fatalf("after check crash state=%#v publishes=%d checks=%d", state, git.publishes, runnerCallCount(runner))
	}

	request.OccurredAt = mustTime(t, "2026-07-21T11:05:00Z")
	request.Fault = nil
	result, err = workspace.ExecuteAttemptCommitStep(
		context.Background(), harness.journal, harness.definition, shell, request,
	)
	if err != nil {
		t.Fatalf("retry journaled protocol: %v", err)
	}
	state, _ = result.Protocol()
	if state.Phase() != workspace.CommitProtocolComplete || git.publishes != 1 || git.createCalls != 2 || runnerCallCount(runner) != 2 {
		t.Fatalf("completed state=%#v publishes=%d creates=%d checks=%d", state, git.publishes, git.createCalls, runnerCallCount(runner))
	}
	if result.Attempt().VerifiedHead() != commitObject {
		t.Fatalf("attempt head = %s", result.Attempt().VerifiedHead())
	}

	// Journal codec/replay has to reconstruct the reducer state without any
	// in-memory shell state.
	snapshot, err := harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	replayedAttempt, exists := replayed.Attempt(attempt.AttemptID())
	if !exists {
		t.Fatal("replayed attempt is missing")
	}
	replayedState, exists := replayedAttempt.CommitProtocol()
	if !exists || replayedState.Phase() != workspace.CommitProtocolComplete ||
		replayedState.CompletedSteps()[0].Commit().Commit() != commitObject {
		t.Fatalf("replayed protocol = %#v exists=%v", replayedState, exists)
	}

	harness.git.setHead(t, attempt.Branch(), mustGitObject(t, 'e'), true)
	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "extra-commit"),
			OccurredAt: mustTime(t, "2026-07-21T11:06:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "Git verification failed") {
		t.Fatalf("extra commit boundary error = %v", err)
	}

	harness.git.setHead(t, attempt.Branch(), commitObject, true)
	boundary, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "complete"),
			OccurredAt: mustTime(t, "2026-07-21T11:07:00Z"),
		},
	)
	if err != nil || boundary.Attempt().Phase() != workspace.AttemptPaused {
		t.Fatalf("boundary after protocol = %#v err=%v", boundary, err)
	}
}

func TestJournaledCommitProtocolRecoversBaseOnlyRebaseAfterStartCrash(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "base-only rebase crash permutation")

	scenario := newJournalCommitScenario(t)
	request := scenario.request
	request.Fault = commitFailOnce(workspace.CommitFaultAfterProtocolStart)
	result, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, request,
	)
	if err == nil || !strings.Contains(err.Error(), string(workspace.CommitFaultAfterProtocolStart)) {
		t.Fatalf("protocol-start fault error = %v", err)
	}
	state, configured := result.Protocol()
	if !configured || state.Phase() != workspace.CommitProtocolReady || len(state.CompletedSteps()) != 0 ||
		state.Base() != scenario.harness.base || scenario.git.createCalls != 0 {
		t.Fatalf("protocol state after start crash=%#v configured=%v creates=%d", state, configured, scenario.git.createCalls)
	}

	step := configuredProtocolStep(t, scenario.harness.definition)
	newBase, newTree, newCommit := mustGitObject(t, '5'), mustGitObject(t, '6'), mustGitObject(t, '7')
	diff := scenario.git.staged.Diff()
	scenario.git.head = newBase
	scenario.git.staged, err = workspace.NewStagedCommitInspection(newBase, newTree, diff, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	scenario.git.commit, err = workspace.NewGitCommitInspection(
		newCommit, []workspace.GitObjectID{newBase}, newTree, step.Message().Subject(), "", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.git.head = newCommit
	if _, err := workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newCommit, mustTime(t, "2026-07-21T12:02:05Z"), nil,
	); err == nil || !strings.Contains(err.Error(), "base-only commit rebase must end at the new base") {
		t.Fatalf("base-only rebase with an unrecorded commit error = %v", err)
	}
	scenario.git.head = newBase

	result, err = workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newBase, mustTime(t, "2026-07-21T12:02:10Z"),
		commitFailOnce(workspace.CommitFaultAfterRebaseRecord),
	)
	if err == nil || !strings.Contains(err.Error(), string(workspace.CommitFaultAfterRebaseRecord)) {
		t.Fatalf("base-only rebase fault error = %v", err)
	}
	state, _ = result.Protocol()
	if state.Phase() != workspace.CommitProtocolReady || state.Base() != newBase || state.Head() != newBase ||
		state.RebaseEpoch() != 1 || result.Attempt().VerifiedHead() != newBase || scenario.git.rangeCalls != 0 {
		t.Fatalf(
			"base-only rebase state=%#v attempt=%#v range calls=%d",
			state, result.Attempt(), scenario.git.rangeCalls,
		)
	}

	result, err = workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newBase, mustTime(t, "2026-07-21T12:02:20Z"), nil,
	)
	if err != nil || scenario.git.rangeCalls != 0 {
		t.Fatalf("retry base-only rebase result=%#v err=%v range calls=%d", result, err, scenario.git.rangeCalls)
	}

	request.OccurredAt = mustTime(t, "2026-07-21T12:02:30Z")
	request.Fault = nil
	result, err = workspace.ExecuteAttemptCommitStep(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, request,
	)
	if err != nil {
		t.Fatalf("resume protocol after base-only rebase: %v", err)
	}
	state, _ = result.Protocol()
	if state.Phase() != workspace.CommitProtocolComplete || state.Base() != newBase || state.Head() != newCommit ||
		state.RebaseEpoch() != 1 || result.Attempt().VerifiedHead() != newCommit ||
		scenario.git.publishes != 1 || runnerCallCount(scenario.runner) != 1 {
		t.Fatalf(
			"completed rebased protocol state=%#v attempt=%#v publishes=%d checks=%d",
			state, result.Attempt(), scenario.git.publishes, runnerCallCount(scenario.runner),
		)
	}

	if err := scenario.harness.journal.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(scenario.harness.workspace)
	if err != nil {
		t.Fatalf("decode base-only rebase journal: %v", err)
	}
	replayed, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatalf("replay base-only rebase journal: %v", err)
	}
	replayedAttempt, exists := replayed.Attempt(scenario.attempt.AttemptID())
	replayedState, hasProtocol := replayedAttempt.CommitProtocol()
	if !exists || !hasProtocol || replayedState.Base() != newBase || replayedState.Head() != newCommit ||
		replayedState.RebaseEpoch() != 1 || replayedAttempt.VerifiedHead() != newCommit {
		t.Fatalf(
			"replayed base-only rebase attempt=%#v exists=%v state=%#v configured=%v",
			replayedAttempt, exists, replayedState, hasProtocol,
		)
	}
}

func TestJournaledCommitProtocolRecoversEveryDurableCrashWindow(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive commit-protocol crash matrix")

	tests := []struct {
		name           string
		point          workspace.CommitProtocolFaultPoint
		phase          workspace.CommitProtocolPhase
		crashCreates   int
		crashPublishes int
		crashChecks    int
		finalCreates   int
		finalChecks    int
	}{
		{"protocol start", workspace.CommitFaultAfterProtocolStart, workspace.CommitProtocolReady, 0, 0, 0, 1, 1},
		{"step intent", workspace.CommitFaultAfterStepIntent, workspace.CommitProtocolAwaitingCommit, 0, 0, 0, 1, 1},
		{"Git commit", workspace.CommitFaultAfterGitCommit, workspace.CommitProtocolAwaitingCommit, 1, 1, 0, 2, 1},
		{"step record", workspace.CommitFaultAfterStepRecord, workspace.CommitProtocolAwaitingChecks, 1, 1, 0, 1, 1},
		{"check run", workspace.CommitFaultAfterCheckRun, workspace.CommitProtocolAwaitingChecks, 1, 1, 1, 1, 2},
		{"check record", workspace.CommitFaultAfterCheckRecord, workspace.CommitProtocolComplete, 1, 1, 1, 1, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := newJournalCommitScenario(t)
			scenario.request.Fault = commitFailOnce(test.point)
			result, err := workspace.ExecuteAttemptCommitStep(
				context.Background(), scenario.harness.journal, scenario.harness.definition,
				scenario.shell, scenario.request,
			)
			if err == nil || !strings.Contains(err.Error(), string(test.point)) {
				t.Fatalf("fault error = %v", err)
			}
			state, ok := result.Protocol()
			if !ok || state.Phase() != test.phase {
				t.Fatalf("crash state = %#v configured=%v", state, ok)
			}
			if scenario.git.createCalls != test.crashCreates || scenario.git.publishes != test.crashPublishes ||
				runnerCallCount(scenario.runner) != test.crashChecks {
				t.Fatalf(
					"crash calls: creates=%d publishes=%d checks=%d",
					scenario.git.createCalls, scenario.git.publishes, runnerCallCount(scenario.runner),
				)
			}

			scenario.request.OccurredAt = mustTime(t, "2026-07-21T12:04:00Z")
			scenario.request.Fault = nil
			result, err = workspace.ExecuteAttemptCommitStep(
				context.Background(), scenario.harness.journal, scenario.harness.definition,
				scenario.shell, scenario.request,
			)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			state, _ = result.Protocol()
			if state.Phase() != workspace.CommitProtocolComplete || result.Attempt().VerifiedHead() != scenario.commit ||
				scenario.git.createCalls != test.finalCreates || scenario.git.publishes != 1 ||
				runnerCallCount(scenario.runner) != test.finalChecks {
				t.Fatalf(
					"final state=%#v head=%s creates=%d publishes=%d checks=%d",
					state, result.Attempt().VerifiedHead(), scenario.git.createCalls,
					scenario.git.publishes, runnerCallCount(scenario.runner),
				)
			}
		})
	}
}

func TestJournaledReviewFixRecoversEveryDurableCrashWindow(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive review-fix crash matrix")

	tests := []struct {
		name           string
		point          workspace.ReviewFixFaultPoint
		phase          workspace.ReviewFixPhase
		crashCreates   int
		crashPublishes int
		crashChecks    int
		finalCreates   int
		finalChecks    int
	}{
		{"reservation", workspace.ReviewFixFaultAfterReservation, workspace.ReviewFixReserved, 0, 0, 0, 1, 1},
		{"intent", workspace.ReviewFixFaultAfterIntent, workspace.ReviewFixAwaitingCommit, 0, 0, 0, 1, 1},
		{"Git commit", workspace.ReviewFixFaultAfterGitCommit, workspace.ReviewFixAwaitingCommit, 1, 1, 0, 2, 1},
		{"commit record", workspace.ReviewFixFaultAfterCommitRecord, workspace.ReviewFixAwaitingChecks, 1, 1, 0, 1, 1},
		{"check run", workspace.ReviewFixFaultAfterCheckRun, workspace.ReviewFixAwaitingChecks, 1, 1, 1, 1, 2},
		{"check record", workspace.ReviewFixFaultAfterCheckRecord, workspace.ReviewFixComplete, 1, 1, 1, 1, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := newJournalReviewFixScenario(t)
			scenario.request.Fault = reviewFixFailOnce(test.point)
			result, err := workspace.ExecuteAttemptReviewFix(
				context.Background(), scenario.harness.journal, scenario.harness.definition,
				scenario.shell, scenario.request,
			)
			if err == nil || !strings.Contains(err.Error(), string(test.point)) {
				t.Fatalf("fault error = %v", err)
			}
			state, ok := result.State()
			if !ok || state.Phase() != test.phase || state.Used() != 1 || state.Remaining() != 1 {
				t.Fatalf("crash state = %#v configured=%v", state, ok)
			}
			if scenario.git.createCalls != test.crashCreates || scenario.git.publishes != test.crashPublishes ||
				runnerCallCount(scenario.runner) != test.crashChecks {
				t.Fatalf(
					"crash calls: creates=%d publishes=%d checks=%d",
					scenario.git.createCalls, scenario.git.publishes, runnerCallCount(scenario.runner),
				)
			}

			scenario.request.OccurredAt = mustTime(t, "2026-07-21T12:14:00Z")
			scenario.request.Fault = nil
			result, err = workspace.ExecuteAttemptReviewFix(
				context.Background(), scenario.harness.journal, scenario.harness.definition,
				scenario.shell, scenario.request,
			)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			state, _ = result.State()
			if state.Phase() != workspace.ReviewFixComplete || state.Head() != scenario.commit ||
				result.Attempt().VerifiedHead() != scenario.commit ||
				scenario.git.createCalls != test.finalCreates || scenario.git.publishes != 1 ||
				runnerCallCount(scenario.runner) != test.finalChecks {
				t.Fatalf(
					"final state=%#v head=%s creates=%d publishes=%d checks=%d",
					state, result.Attempt().VerifiedHead(), scenario.git.createCalls,
					scenario.git.publishes, runnerCallCount(scenario.runner),
				)
			}
		})
	}
}

func TestJournaledReviewFixReplayBudgetExactHeadAndBoundary(t *testing.T) {
	t.Parallel()

	scenario := newJournalReviewFixScenario(t)
	result, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := result.State()
	if !ok || state.Phase() != workspace.ReviewFixComplete || state.Used() != 1 ||
		state.Remaining() != 1 || result.Attempt().VerifiedHead() != scenario.commit {
		t.Fatalf("first review fix state=%#v configured=%v attempt=%#v", state, ok, result.Attempt())
	}
	returnedFixes := state.Fixes()
	returnedChecks := returnedFixes[0].Checks()
	returnedAllowed := state.Protocol().Paths().Allowed()
	returnedFixes[0] = workspace.ReviewFixStepState{}
	returnedChecks[0] = workspace.CommitCheckEvidence{}
	returnedAllowed[0] = workspace.CommitPathPattern{}
	freshState, _ := result.State()
	freshFixes := freshState.Fixes()
	if len(freshFixes) != 1 || freshFixes[0].Ordinal() != 1 || len(freshFixes[0].Checks()) != 1 ||
		freshFixes[0].Checks()[0].EvidenceDigest().IsZero() ||
		len(freshState.Protocol().Paths().Allowed()) != 1 || freshState.Protocol().Paths().Allowed()[0].String() != "src/**" {
		t.Fatalf("mutating returned review-fix state changed durable result: %#v", freshState)
	}

	scenario.git.head = mustGitObject(t, '9')
	scenario.request.OccurredAt = mustTime(t, "2026-07-21T12:15:00Z")
	if _, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	); err == nil || !strings.Contains(err.Error(), "verify completed review-fix head") {
		t.Fatalf("completed retry with drifted head error = %v", err)
	}
	scenario.git.head = scenario.commit

	review := configuredReviewFixProtocol(t, scenario.harness.definition)
	secondStep, err := review.Step(2)
	if err != nil {
		t.Fatal(err)
	}
	secondTree, secondCommit, secondChanged := mustGitObject(t, '6'), mustGitObject(t, '7'), mustGitObject(t, '8')
	secondDiff := addedDiff(t, "src/review-two.go", secondChanged)
	scenario.git.staged, err = workspace.NewStagedCommitInspection(
		scenario.commit, secondTree, secondDiff, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.git.commit, err = workspace.NewGitCommitInspection(
		secondCommit, []workspace.GitObjectID{scenario.commit}, secondTree,
		secondStep.Message().Subject(), "second accepted fix", secondDiff,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := workspace.ExecuteAttemptReviewFixRequest{
		AttemptID: scenario.attempt.AttemptID(), Ordinal: 2, Body: "second accepted fix",
		OccurredAt: mustTime(t, "2026-07-21T12:16:00Z"),
	}
	result, err = workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, secondRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = result.State()
	if state.Used() != 2 || state.Remaining() != 0 || state.Head() != secondCommit ||
		result.Attempt().VerifiedHead() != secondCommit {
		t.Fatalf("second review fix state=%#v attempt=%#v", state, result.Attempt())
	}

	beforeExhaustionCreates := scenario.git.createCalls
	exhausted := secondRequest
	exhausted.Ordinal = 3
	exhausted.OccurredAt = mustTime(t, "2026-07-21T12:17:00Z")
	if _, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, exhausted,
	); err == nil || !strings.Contains(err.Error(), "budget is exhausted") {
		t.Fatalf("exhausted budget error = %v", err)
	}
	if scenario.git.createCalls != beforeExhaustionCreates {
		t.Fatal("budget exhaustion reached Git mutation")
	}

	snapshot, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	replayedAttempt, exists := replayed.Attempt(scenario.attempt.AttemptID())
	replayedState, hasState := replayedAttempt.ReviewFixes()
	if !exists || !hasState || replayedState.Used() != 2 || replayedState.Head() != secondCommit ||
		replayedAttempt.VerifiedHead() != secondCommit {
		t.Fatalf("replayed attempt=%#v exists=%v state=%#v hasState=%v", replayedAttempt, exists, replayedState, hasState)
	}
	unit := configuredUnitExecution(t, scenario.harness.definition)
	budget, configured, err := workspace.ReviewFixBudgetForAttempt(unit, replayedAttempt)
	if err != nil || !configured || budget.Maximum() != 2 || budget.Used() != 2 || budget.Remaining() != 0 {
		t.Fatalf("replayed budget=%#v configured=%v err=%v", budget, configured, err)
	}

	scenario.harness.git.setHead(t, scenario.attempt.Branch(), secondCommit, true)
	boundary, err := workspace.RecordAttemptBoundary(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: scenario.attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "review-fixes-complete"),
			OccurredAt: mustTime(t, "2026-07-21T12:18:00Z"),
		},
	)
	if err != nil || boundary.Attempt().Phase() != workspace.AttemptPaused || boundary.Boundary().Head() != secondCommit {
		t.Fatalf("boundary after review fixes = %#v err=%v", boundary, err)
	}
	if err := scenario.harness.journal.Close(); err != nil {
		t.Fatal(err)
	}
	diskSnapshot, err := workspace.ReadWorkspaceJournalSnapshot(scenario.harness.workspace)
	if err != nil {
		t.Fatalf("decode review-fix journal from disk: %v", err)
	}
	if _, err := workspace.RebuildWorkspaceRuntime(diskSnapshot); err != nil {
		t.Fatalf("replay decoded review-fix journal: %v", err)
	}
}

func TestAttemptBoundaryRejectsInFlightReviewFix(t *testing.T) {
	t.Parallel()

	scenario := newJournalReviewFixScenario(t)
	scenario.request.Fault = reviewFixFailOnce(workspace.ReviewFixFaultAfterReservation)
	if _, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	); err == nil {
		t.Fatal("review-fix reservation fault was not injected")
	}
	scenario.harness.git.setHead(t, scenario.attempt.Branch(), scenario.implementation, true)
	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: scenario.attempt.AttemptID(), Kind: workspace.AttemptBoundaryKindCheckpoint,
			Evidence:   boundaryEvidence(t, "review-fix-in-flight"),
			OccurredAt: mustTime(t, "2026-07-21T12:19:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "in-flight review fix") {
		t.Fatalf("in-flight review-fix boundary error = %v", err)
	}
}

func TestReviewFixCanFollowUnconstrainedImplementationHistory(t *testing.T) {
	t.Parallel()

	harness := newReviewOnlyAttemptHarness(t)
	attempt := harness.reserve(t, "2026-07-21T12:20:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T12:21:00Z")
	review := configuredReviewFixProtocol(t, harness.definition)
	step, err := review.Step(1)
	if err != nil {
		t.Fatal(err)
	}
	implementationHead := mustGitObject(t, 'c')
	tree, commitObject, changedObject := mustGitObject(t, 'e'), mustGitObject(t, 'f'), mustGitObject(t, '1')
	diff := addedDiff(t, "src/review.go", changedObject)
	staged, err := workspace.NewStagedCommitInspection(implementationHead, tree, diff, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := workspace.NewGitCommitInspection(
		commitObject, []workspace.GitObjectID{implementationHead}, tree,
		step.Message().Subject(), "accepted unconstrained fix", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	git := &journalCommitGit{
		branch: attempt.Branch(), head: implementationHead, staged: staged, commit: commit,
	}
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, err := workspace.NewCommitProtocolShell(git, runner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), harness.journal, harness.definition, shell,
		workspace.ExecuteAttemptReviewFixRequest{
			AttemptID: attempt.AttemptID(), Ordinal: 1, Body: "accepted unconstrained fix",
			OccurredAt: mustTime(t, "2026-07-21T12:22:00Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := result.State()
	if !ok || state.Base() != implementationHead || state.Head() != commitObject ||
		result.Attempt().VerifiedHead() != commitObject || runnerCallCount(runner) != 1 {
		t.Fatalf("unconstrained review-fix state=%#v configured=%v attempt=%#v", state, ok, result.Attempt())
	}

	newBase, newTree, newCommit := mustGitObject(t, '2'), mustGitObject(t, '3'), mustGitObject(t, '4')
	rebasedInspection, err := workspace.NewGitCommitInspection(
		newCommit, []workspace.GitObjectID{newBase}, newTree,
		step.Message().Subject(), "accepted unconstrained fix", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	git.head, git.commit = newCommit, rebasedInspection
	git.rangeCommits = []workspace.GitCommitInspection{rebasedInspection}
	rebased, err := workspace.RecordAttemptReviewFixRebase(
		context.Background(), harness.journal, harness.definition, shell, attempt.AttemptID(),
		newBase, newCommit, mustTime(t, "2026-07-21T12:23:00Z"), nil,
	)
	if err != nil {
		t.Fatalf("record review-only rebase: %v", err)
	}
	state, _ = rebased.State()
	if state.Base() != newBase || state.Head() != newCommit || state.RebaseEpoch() != 1 ||
		state.Phase() != workspace.ReviewFixAwaitingChecks || rebased.Attempt().VerifiedHead() != newCommit {
		t.Fatalf("rebased review-only state=%#v attempt=%#v", state, rebased.Attempt())
	}
	result, err = workspace.ExecuteAttemptReviewFix(
		context.Background(), harness.journal, harness.definition, shell,
		workspace.ExecuteAttemptReviewFixRequest{
			AttemptID: attempt.AttemptID(), Ordinal: 1, Body: "accepted unconstrained fix",
			OccurredAt: mustTime(t, "2026-07-21T12:24:00Z"),
		},
	)
	if err != nil {
		t.Fatalf("rerun review-only rebase check: %v", err)
	}
	state, _ = result.State()
	if state.Phase() != workspace.ReviewFixComplete || state.Head() != newCommit ||
		result.Attempt().VerifiedHead() != newCommit || runnerCallCount(runner) != 2 {
		t.Fatalf("completed review-only rebase state=%#v attempt=%#v checks=%d", state, result.Attempt(), runnerCallCount(runner))
	}
}

func TestJournaledCommitRebaseRetryIsIdempotentAndRerunsChecks(t *testing.T) {
	t.Parallel()

	scenario := newJournalCommitScenario(t)
	result, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := result.Protocol()
	step := state.Protocol().Steps()[0]
	newBase, newTree, newCommit := mustGitObject(t, '5'), mustGitObject(t, '6'), mustGitObject(t, '7')
	rebased, err := workspace.NewGitCommitInspection(
		newCommit, []workspace.GitObjectID{newBase}, newTree,
		step.Message().Subject(), "", scenario.git.commit.Diff(),
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.git.head, scenario.git.commit = newCommit, rebased

	result, err = workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newCommit, mustTime(t, "2026-07-21T12:05:00Z"),
		commitFailOnce(workspace.CommitFaultAfterRebaseRecord),
	)
	if err == nil || !strings.Contains(err.Error(), string(workspace.CommitFaultAfterRebaseRecord)) {
		t.Fatalf("rebase fault error = %v", err)
	}
	state, _ = result.Protocol()
	if state.Phase() != workspace.CommitProtocolAwaitingChecks || state.RebaseEpoch() != 1 {
		t.Fatalf("recorded rebase state = %#v", state)
	}
	afterRebase, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	records := afterRebase.Records()
	if len(records) == 0 ||
		records[len(records)-1].EventType() != workspace.JournalEventCommitProtocolRebased {
		t.Fatalf("rebase journal tail = %#v", records)
	}

	scenario.git.head = newBase
	if _, err := workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newCommit, mustTime(t, "2026-07-21T12:05:10Z"), nil,
	); err == nil || !strings.Contains(err.Error(), "verify recorded commit rebase worktree") {
		t.Fatalf("retry with drifted head error = %v", err)
	}
	scenario.git.head = newCommit
	drifted, err := workspace.NewGitCommitInspection(
		newCommit, []workspace.GitObjectID{newBase}, mustGitObject(t, '8'),
		step.Message().Subject(), "", scenario.git.commit.Diff(),
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.git.commit = drifted
	if _, err := workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newCommit, mustTime(t, "2026-07-21T12:05:20Z"), nil,
	); err == nil || !strings.Contains(err.Error(), "no longer matches Git evidence") {
		t.Fatalf("retry with drifted mapping error = %v", err)
	}
	scenario.git.commit = rebased

	result, err = workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newCommit, mustTime(t, "2026-07-21T12:06:00Z"), nil,
	)
	if err != nil {
		t.Fatalf("retry recorded rebase: %v", err)
	}
	state, _ = result.Protocol()
	if state.Phase() != workspace.CommitProtocolAwaitingChecks || state.RebaseEpoch() != 1 {
		t.Fatalf("retried rebase state = %#v", state)
	}

	scenario.request.OccurredAt = mustTime(t, "2026-07-21T12:07:00Z")
	result, err = workspace.ExecuteAttemptCommitStep(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	)
	if err != nil {
		t.Fatalf("rerun rebased checks: %v", err)
	}
	state, _ = result.Protocol()
	if state.Phase() != workspace.CommitProtocolComplete || state.Head() != newCommit ||
		runnerCallCount(scenario.runner) != 2 {
		t.Fatalf("completed rebased state=%#v checks=%d", state, runnerCallCount(scenario.runner))
	}
}

func TestJournaledRebaseAtomicallyRemapsImplementationAndReviewFixChecks(t *testing.T) {
	t.Parallel()

	scenario := newJournalReviewFixScenario(t)
	reviewResult, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	reviewProtocol := configuredReviewFixProtocol(t, scenario.harness.definition)
	secondStep, err := reviewProtocol.Step(2)
	if err != nil {
		t.Fatal(err)
	}
	secondTree, secondCommit, secondChanged := mustGitObject(t, '7'), mustGitObject(t, '8'), mustGitObject(t, '9')
	secondDiff := addedDiff(t, "src/review-two.go", secondChanged)
	scenario.git.staged, err = workspace.NewStagedCommitInspection(
		scenario.commit, secondTree, secondDiff, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.git.commit, err = workspace.NewGitCommitInspection(
		secondCommit, []workspace.GitObjectID{scenario.commit}, secondTree,
		secondStep.Message().Subject(), "second accepted fix", secondDiff,
	)
	if err != nil {
		t.Fatal(err)
	}
	reviewResult, err = workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, workspace.ExecuteAttemptReviewFixRequest{
			AttemptID: scenario.attempt.AttemptID(), Ordinal: 2, Body: "second accepted fix",
			OccurredAt: mustTime(t, "2026-07-21T12:13:30Z"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	implementationState, ok := reviewResult.Attempt().CommitProtocol()
	if !ok {
		t.Fatal("completed attempt has no implementation protocol")
	}
	reviewState, ok := reviewResult.State()
	if !ok {
		t.Fatal("completed attempt has no review-fix state")
	}
	implementationEvidence := implementationState.CompletedSteps()[0].Commit()
	reviewFixes := reviewState.Fixes()
	if len(reviewFixes) != 2 {
		t.Fatalf("completed review-fix count = %d", len(reviewFixes))
	}
	firstReviewEvidence, firstOK := reviewFixes[0].Commit()
	secondReviewEvidence, secondOK := reviewFixes[1].Commit()
	if !firstOK || !secondOK {
		t.Fatal("completed review fixes have missing commit evidence")
	}

	newBase := mustGitObject(t, '2')
	newImplementationTree, newImplementationCommit := mustGitObject(t, '3'), mustGitObject(t, '4')
	newImplementation, err := workspace.NewGitCommitInspection(
		newImplementationCommit, []workspace.GitObjectID{newBase}, newImplementationTree,
		implementationEvidence.Subject(), implementationEvidence.Body(), implementationEvidence.Diff(),
	)
	if err != nil {
		t.Fatal(err)
	}
	newReviewTree, newReviewCommit := mustGitObject(t, '5'), mustGitObject(t, '6')
	newReview, err := workspace.NewGitCommitInspection(
		newReviewCommit, []workspace.GitObjectID{newImplementationCommit}, newReviewTree,
		firstReviewEvidence.Subject(), firstReviewEvidence.Body(), firstReviewEvidence.Diff(),
	)
	if err != nil {
		t.Fatal(err)
	}
	newSecondReviewTree, newSecondReviewCommit := mustGitObject(t, 'a'), mustGitObject(t, 'b')
	newSecondReview, err := workspace.NewGitCommitInspection(
		newSecondReviewCommit, []workspace.GitObjectID{newReviewCommit}, newSecondReviewTree,
		secondReviewEvidence.Subject(), secondReviewEvidence.Body(), secondReviewEvidence.Diff(),
	)
	if err != nil {
		t.Fatal(err)
	}
	scenario.git.head = newSecondReviewCommit
	scenario.git.commit = newSecondReview
	scenario.git.rangeCommits = []workspace.GitCommitInspection{newImplementation, newReview, newSecondReview}

	rebased, err := workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newSecondReviewCommit, mustTime(t, "2026-07-21T12:14:00Z"),
		commitFailOnce(workspace.CommitFaultAfterRebaseRecord),
	)
	if err == nil || !strings.Contains(err.Error(), string(workspace.CommitFaultAfterRebaseRecord)) {
		t.Fatalf("chain rebase fault error = %v", err)
	}
	implementationState, _ = rebased.Protocol()
	reviewState, ok = rebased.Attempt().ReviewFixes()
	if !ok || implementationState.Phase() != workspace.CommitProtocolAwaitingChecks ||
		implementationState.Head() != newImplementationCommit || implementationState.RebaseEpoch() != 1 ||
		reviewState.Phase() != workspace.ReviewFixAwaitingChecks || reviewState.Head() != newSecondReviewCommit ||
		reviewState.Base() != newImplementationCommit || reviewState.RebaseEpoch() != 1 ||
		rebased.Attempt().VerifiedHead() != newSecondReviewCommit {
		t.Fatalf("durable chain rebase implementation=%#v review=%#v attempt=%#v", implementationState, reviewState, rebased.Attempt())
	}

	if _, err := workspace.RecordAttemptCommitRebase(
		context.Background(), scenario.harness.journal, scenario.harness.definition, scenario.shell,
		scenario.attempt.AttemptID(), newBase, newSecondReviewCommit, mustTime(t, "2026-07-21T12:14:10Z"), nil,
	); err != nil {
		t.Fatalf("retry recorded chain rebase: %v", err)
	}
	implementationRequest := workspace.ExecuteAttemptCommitStepRequest{
		AttemptID: scenario.attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:14:20Z"),
	}
	implementationResult, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, implementationRequest,
	)
	if err != nil {
		t.Fatalf("rerun rebased implementation checks: %v", err)
	}
	implementationState, _ = implementationResult.Protocol()
	if implementationState.Phase() != workspace.CommitProtocolComplete {
		t.Fatalf("implementation revalidation state = %#v", implementationState)
	}
	wrongReviewOrdinal := workspace.ExecuteAttemptReviewFixRequest{
		AttemptID: scenario.attempt.AttemptID(), Ordinal: 2, Body: "second accepted fix",
		OccurredAt: mustTime(t, "2026-07-21T12:14:25Z"),
	}
	if _, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, wrongReviewOrdinal,
	); err == nil || !strings.Contains(err.Error(), "pending revalidation ordinal 1") {
		t.Fatalf("wrong review-fix revalidation ordinal error = %v", err)
	}
	if runnerCallCount(scenario.runner) != 3 {
		t.Fatalf("wrong review-fix ordinal ran checks: %d", runnerCallCount(scenario.runner))
	}
	scenario.request.OccurredAt = mustTime(t, "2026-07-21T12:14:30Z")
	reviewResult, err = workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	)
	if err != nil {
		t.Fatalf("rerun rebased review-fix checks: %v", err)
	}
	reviewState, _ = reviewResult.State()
	fixes := reviewState.Fixes()
	if reviewState.Phase() != workspace.ReviewFixComplete || reviewState.Head() != newSecondReviewCommit ||
		len(fixes) != 2 || len(fixes[0].Checks()) != 1 || len(fixes[1].Checks()) != 1 ||
		fixes[0].Checks()[0].Commit() != newReviewCommit ||
		fixes[1].Checks()[0].Commit() != newSecondReviewCommit ||
		reviewResult.Attempt().VerifiedHead() != newSecondReviewCommit || runnerCallCount(scenario.runner) != 5 {
		t.Fatalf("revalidated review state=%#v attempt=%#v checks=%d", reviewState, reviewResult.Attempt(), runnerCallCount(scenario.runner))
	}

	snapshot, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatalf("replay remapped chain: %v", err)
	}
	replayedAttempt, exists := replayed.Attempt(scenario.attempt.AttemptID())
	replayedReview, hasReview := replayedAttempt.ReviewFixes()
	if !exists || !hasReview || replayedAttempt.VerifiedHead() != newSecondReviewCommit ||
		replayedReview.Phase() != workspace.ReviewFixComplete || replayedReview.Head() != newSecondReviewCommit {
		t.Fatalf("replayed chain attempt=%#v exists=%v review=%#v configured=%v", replayedAttempt, exists, replayedReview, hasReview)
	}
}

func TestCommitJournalCodecRejectsTamperedProtocolPayload(t *testing.T) {
	t.Parallel()

	scenario := newJournalCommitScenario(t)
	scenario.request.Fault = commitFailOnce(workspace.CommitFaultAfterProtocolStart)
	result, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	)
	if err == nil {
		t.Fatal("protocol-start fault was not injected")
	}
	state, _ := result.Protocol()
	if err := scenario.harness.journal.Close(); err != nil {
		t.Fatal(err)
	}
	journalPath := workspace.WorkspaceJournalPath(scenario.harness.workspace)
	content, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`"protocol_digest":"` + state.ProtocolDigest().String() + `"`)
	tampered := []byte(`"protocol_digest":"` + workspace.DigestBytes([]byte("tampered-protocol")).String() + `"`)
	if bytes.Count(content, original) != 1 {
		t.Fatalf("protocol digest occurrence count = %d", bytes.Count(content, original))
	}
	content = bytes.Replace(content, original, tampered, 1)
	if err := os.WriteFile(journalPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadWorkspaceJournalSnapshot(scenario.harness.workspace); err == nil ||
		!strings.Contains(err.Error(), "payload digest does not match rules") {
		t.Fatalf("tampered payload error = %v", err)
	}
}

func TestReviewFixJournalCodecRejectsUnknownPayloadFields(t *testing.T) {
	t.Parallel()

	scenario := newJournalReviewFixScenario(t)
	scenario.request.Fault = reviewFixFailOnce(workspace.ReviewFixFaultAfterReservation)
	if _, err := workspace.ExecuteAttemptReviewFix(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	); err == nil {
		t.Fatal("review-fix reservation fault was not injected")
	}
	if err := scenario.harness.journal.Close(); err != nil {
		t.Fatal(err)
	}
	journalPath := workspace.WorkspaceJournalPath(scenario.harness.workspace)
	content, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`"type":"review_fix.reserved.v2","payload":{"workspace_id":`)
	tampered := []byte(`"type":"review_fix.reserved.v2","payload":{"unknown":true,"workspace_id":`)
	if bytes.Count(content, original) != 1 {
		t.Fatalf("review-fix reservation payload occurrence count = %d", bytes.Count(content, original))
	}
	content = bytes.Replace(content, original, tampered, 1)
	if err := os.WriteFile(journalPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadWorkspaceJournalSnapshot(scenario.harness.workspace); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown review-fix payload field error = %v", err)
	}
}

func TestCommitEventsUseOrdinaryAppendConstruction(t *testing.T) {
	t.Parallel()

	scenario := newJournalCommitScenario(t)
	protocol := configuredProtocolStep(t, scenario.harness.definition)
	configured, err := workspace.NewCommitProtocol([]workspace.CommitStep{protocol})
	if err != nil {
		t.Fatal(err)
	}
	event, err := workspace.NewCommitProtocolStartedJournalEvent(
		scenario.harness.definition.Workspace().ID(), scenario.harness.definition.Generation(),
		scenario.attempt.AttemptID(), scenario.harness.base, configured,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewJournalAppend(
		event, mustTime(t, "2026-07-21T12:08:00Z"),
	); err != nil {
		t.Fatalf("construct direct commit append: %v", err)
	}
	review := configuredReviewFixProtocol(t, scenario.harness.definition)
	reviewEvent, err := workspace.NewReviewFixReservedJournalEvent(
		scenario.harness.definition.Workspace().ID(), scenario.harness.definition.Generation(),
		scenario.attempt.AttemptID(), review, 2, 1, scenario.harness.base,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewJournalAppend(
		reviewEvent, mustTime(t, "2026-07-21T12:08:01Z"),
	); err != nil {
		t.Fatalf("construct direct review-fix append: %v", err)
	}
}

func TestCommitProtocolResultsDefensivelyCopyNestedState(t *testing.T) {
	t.Parallel()

	scenario := newJournalCommitScenario(t)
	result, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), scenario.harness.journal, scenario.harness.definition,
		scenario.shell, scenario.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := result.Protocol()
	steps := state.Protocol().Steps()
	checks := steps[0].Checks()
	allowed := steps[0].Paths().Allowed()
	completed := state.CompletedSteps()
	checkEvidence := completed[0].Checks()
	steps[0] = workspace.CommitStep{}
	checks[0] = workspace.CommitCheck{}
	allowed[0] = workspace.CommitPathPattern{}
	completed[0] = workspace.CommitProtocolStepState{}
	checkEvidence[0] = workspace.CommitCheckEvidence{}

	fresh, _ := result.Protocol()
	freshSteps := fresh.Protocol().Steps()
	freshCompleted := fresh.CompletedSteps()
	if len(freshSteps) != 1 || freshSteps[0].ID().IsZero() || len(freshSteps[0].Checks()) != 1 ||
		len(freshSteps[0].Paths().Allowed()) != 1 || freshSteps[0].Paths().Allowed()[0].String() != "src/**" ||
		len(freshCompleted) != 1 || freshCompleted[0].Commit().Commit() != scenario.commit ||
		len(freshCompleted[0].Checks()) != 1 || freshCompleted[0].Checks()[0].EvidenceDigest().IsZero() {
		t.Fatalf("mutating returned state changed result: %#v", fresh)
	}
	attemptState, ok := result.Attempt().CommitProtocol()
	if !ok || attemptState.Head() != scenario.commit {
		t.Fatalf("attempt protocol copy = %#v ok=%v", attemptState, ok)
	}
}

type journalCommitScenario struct {
	harness attemptHarness
	attempt workspace.RuntimeAttemptProjection
	git     *journalCommitGit
	runner  *protocolCheckRunner
	shell   workspace.CommitProtocolShell
	request workspace.ExecuteAttemptCommitStepRequest
	commit  workspace.GitObjectID
}

type journalReviewFixScenario struct {
	harness        attemptHarness
	attempt        workspace.RuntimeAttemptProjection
	git            *journalCommitGit
	runner         *protocolCheckRunner
	shell          workspace.CommitProtocolShell
	request        workspace.ExecuteAttemptReviewFixRequest
	implementation workspace.GitObjectID
	commit         workspace.GitObjectID
}

func newJournalCommitScenario(t *testing.T) journalCommitScenario {
	t.Helper()
	harness := newConfiguredAttemptHarness(t)
	attempt := harness.reserve(t, "2026-07-21T12:00:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T12:01:00Z")
	step := configuredProtocolStep(t, harness.definition)
	tree, commitObject, changedObject := mustGitObject(t, 'b'), mustGitObject(t, 'c'), mustGitObject(t, 'd')
	diff := addedDiff(t, "src/protocol.go", changedObject)
	staged, err := workspace.NewStagedCommitInspection(harness.base, tree, diff, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := workspace.NewGitCommitInspection(
		commitObject, []workspace.GitObjectID{harness.base}, tree,
		step.Message().Subject(), "", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	git := &journalCommitGit{branch: attempt.Branch(), head: harness.base, staged: staged, commit: commit}
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, err := workspace.NewCommitProtocolShell(git, runner)
	if err != nil {
		t.Fatal(err)
	}
	return journalCommitScenario{
		harness: harness, attempt: attempt, git: git, runner: runner, shell: shell, commit: commitObject,
		request: workspace.ExecuteAttemptCommitStepRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T12:03:00Z"),
		},
	}
}

func newJournalReviewFixScenario(t *testing.T) journalReviewFixScenario {
	t.Helper()
	implementation := newJournalCommitScenario(t)
	result, err := workspace.ExecuteAttemptCommitStep(
		context.Background(), implementation.harness.journal, implementation.harness.definition,
		implementation.shell, implementation.request,
	)
	if err != nil {
		t.Fatalf("complete implementation protocol: %v", err)
	}
	if result.Attempt().VerifiedHead() != implementation.commit {
		t.Fatalf("implementation head = %s", result.Attempt().VerifiedHead())
	}
	implementation.git.createCalls, implementation.git.publishes = 0, 0
	implementation.runner.invocations = nil

	review := configuredReviewFixProtocol(t, implementation.harness.definition)
	step, err := review.Step(1)
	if err != nil {
		t.Fatal(err)
	}
	tree, commitObject, changedObject := mustGitObject(t, 'e'), mustGitObject(t, 'f'), mustGitObject(t, '1')
	diff := addedDiff(t, "src/review.go", changedObject)
	staged, err := workspace.NewStagedCommitInspection(implementation.commit, tree, diff, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := workspace.NewGitCommitInspection(
		commitObject, []workspace.GitObjectID{implementation.commit}, tree,
		step.Message().Subject(), "accepted fix", diff,
	)
	if err != nil {
		t.Fatal(err)
	}
	implementation.git.staged, implementation.git.commit = staged, commit
	return journalReviewFixScenario{
		harness: implementation.harness, attempt: implementation.attempt,
		git: implementation.git, runner: implementation.runner, shell: implementation.shell,
		implementation: implementation.commit, commit: commitObject,
		request: workspace.ExecuteAttemptReviewFixRequest{
			AttemptID: implementation.attempt.AttemptID(), Ordinal: 1, Body: "accepted fix",
			OccurredAt: mustTime(t, "2026-07-21T12:13:00Z"),
		},
	}
}

func newConfiguredAttemptHarness(t *testing.T) attemptHarness {
	t.Helper()
	fixture := newDefinitionFixture(t)
	configuration := string(fixture.sources.ExecutionConfig.Bytes)
	needle := "      max_review_fixes: 2\n  - plan_id: alpha-plan\n    merge_unit_id: unit-two"
	protocol := `      max_review_fixes: 2
    commit_protocol:
      steps:
        - id: implementation
          subject: Implement protocol
          body_policy: forbidden
          allowed_paths:
            - src/**
          frozen_paths: []
          checks:
            - id: protocol-check
              runner: codex
              parser: go-test-json
              command:
                - go
                - test
                - ./...
              expectation:
                kind: pass
                failure_ids: []
    review_fix_protocol:
      subject_prefix: Review fix
      body_policy: required
      allowed_paths:
        - src/**
      frozen_paths: []
      checks:
        - id: review-check
          runner: codex
          parser: go-test-json
          command:
            - go
            - test
            - ./...
          expectation:
            kind: pass
            failure_ids: []
  - plan_id: alpha-plan
    merge_unit_id: unit-two`
	configuration = strings.Replace(configuration, needle, protocol, 1)
	if configuration == string(fixture.sources.ExecutionConfig.Bytes) {
		t.Fatal("failed to install configured protocol fixture")
	}
	fixture.sources.ExecutionConfig.Bytes = []byte(configuration)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	initialized, err := initializeWorkspaceV2(
		t, workspaceDir, definition,
		mustTime(t, "2026-07-21T10:00:00Z"),
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
		t.Fatal("configured harness has no durable local target")
	}
	base := target.CreatedHead()
	goal, _ := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	return attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDir, git: &fakeAttemptGit{}, base: base,
		unit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), goal: goal,
		worktrees: initialized.Runtime().WorktreeRoot().Path(),
	}
}

func newReviewOnlyAttemptHarness(t *testing.T) attemptHarness {
	t.Helper()
	fixture := newDefinitionFixture(t)
	configuration := string(fixture.sources.ExecutionConfig.Bytes)
	needle := "      max_review_fixes: 2\n  - plan_id: alpha-plan\n    merge_unit_id: unit-two"
	protocol := `      max_review_fixes: 2
    review_fix_protocol:
      subject_prefix: Review fix
      body_policy: required
      allowed_paths:
        - src/**
      frozen_paths: []
      checks:
        - id: review-check
          runner: codex
          parser: go-test-json
          command:
            - go
            - test
            - ./...
          expectation:
            kind: pass
            failure_ids: []
  - plan_id: alpha-plan
    merge_unit_id: unit-two`
	configuration = strings.Replace(configuration, needle, protocol, 1)
	if configuration == string(fixture.sources.ExecutionConfig.Bytes) {
		t.Fatal("failed to install review-only protocol fixture")
	}
	fixture.sources.ExecutionConfig.Bytes = []byte(configuration)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	initialized, err := initializeWorkspaceV2(
		t, workspaceDir, definition,
		mustTime(t, "2026-07-21T10:00:00Z"),
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
		t.Fatal("review harness has no durable local target")
	}
	base := target.CreatedHead()
	goal, _ := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	return attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDir, git: &fakeAttemptGit{}, base: base,
		unit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), goal: goal,
		worktrees: initialized.Runtime().WorktreeRoot().Path(),
	}
}

func configuredProtocolStep(t *testing.T, definition workspace.EffectiveWorkspaceDefinition) workspace.CommitStep {
	t.Helper()
	for _, unit := range definition.ExecutionConfig().MergeUnits() {
		if unit.MergeUnitID().String() == "unit-one" {
			protocol, ok := unit.CommitProtocol()
			if !ok || len(protocol.Steps()) != 1 {
				t.Fatalf("configured unit protocol = %#v ok=%v", protocol, ok)
			}
			return protocol.Steps()[0]
		}
	}
	t.Fatal("configured unit is missing")
	return workspace.CommitStep{}
}

func configuredUnitExecution(t *testing.T, definition workspace.EffectiveWorkspaceDefinition) workspace.UnitExecution {
	t.Helper()
	for _, unit := range definition.ExecutionConfig().MergeUnits() {
		if unit.MergeUnitID().String() == "unit-one" {
			return unit
		}
	}
	t.Fatal("configured unit is missing")
	return workspace.UnitExecution{}
}

func configuredReviewFixProtocol(t *testing.T, definition workspace.EffectiveWorkspaceDefinition) workspace.ReviewFixProtocol {
	t.Helper()
	protocol, ok := configuredUnitExecution(t, definition).ReviewFixProtocol()
	if !ok {
		t.Fatal("configured unit review-fix protocol is missing")
	}
	return protocol
}

func commitFailOnce(point workspace.CommitProtocolFaultPoint) workspace.CommitProtocolFaultInjector {
	fired := false
	return func(actual workspace.CommitProtocolFaultPoint) error {
		if actual == point && !fired {
			fired = true
			return errors.New("simulated commit crash")
		}
		return nil
	}
}

func reviewFixFailOnce(point workspace.ReviewFixFaultPoint) workspace.ReviewFixFaultInjector {
	fired := false
	return func(actual workspace.ReviewFixFaultPoint) error {
		if actual == point && !fired {
			fired = true
			return errors.New("simulated review-fix crash")
		}
		return nil
	}
}

func runnerCallCount(runner *protocolCheckRunner) int { return len(runner.invocations) }

var _ workspace.CommitGitPort = (*journalCommitGit)(nil)
