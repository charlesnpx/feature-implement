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
	branch      string
	head        workspace.GitObjectID
	staged      workspace.StagedCommitInspection
	commit      workspace.GitCommitInspection
	createCalls int
	publishes   int
}

func TestJournaledCommitProtocolStartsFromRealStagedWorktree(t *testing.T) {
	seed, _, base := newProtocolRepository(t)
	harness := newConfiguredAttemptHarness(t)
	harness.base = base
	attempt := harness.reserve(t, "2026-07-21T10:55:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T10:56:00Z")

	runGitSetup(t, "", "clone", "--no-local", seed, attempt.Worktree())
	runGitSetup(t, attempt.Worktree(), "config", "user.name", "Protocol Test")
	runGitSetup(t, attempt.Worktree(), "config", "user.email", "protocol@example.test")
	runGitSetup(t, attempt.Worktree(), "switch", "-c", attempt.Branch(), rawGitObject(base))
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
		result.Attempt().VerifiedHead() != head || head == base || runnerCallCount(runner) != 1 {
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
	harness := newConfiguredAttemptHarness(t)
	attempt := harness.reserve(t, "2026-07-21T11:00:00Z")
	attempt = harness.materialize(t, attempt.AttemptID(), "2026-07-21T11:01:00Z")

	if _, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Evidence: boundaryEvidence(t, "premature"),
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
			AttemptID: attempt.AttemptID(), Evidence: boundaryEvidence(t, "extra-commit"),
			OccurredAt: mustTime(t, "2026-07-21T11:06:00Z"),
		},
	); err == nil || !strings.Contains(err.Error(), "Git verification failed") {
		t.Fatalf("extra commit boundary error = %v", err)
	}

	harness.git.setHead(t, attempt.Branch(), commitObject, true)
	boundary, err := workspace.RecordAttemptBoundary(
		context.Background(), harness.journal, harness.definition, harness.git,
		workspace.RecordAttemptBoundaryRequest{
			AttemptID: attempt.AttemptID(), Evidence: boundaryEvidence(t, "complete"),
			OccurredAt: mustTime(t, "2026-07-21T11:07:00Z"),
		},
	)
	if err != nil || boundary.Attempt().Phase() != workspace.AttemptPaused {
		t.Fatalf("boundary after protocol = %#v err=%v", boundary, err)
	}
}

func TestJournaledCommitProtocolRecoversEveryDurableCrashWindow(t *testing.T) {
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

func TestJournaledCommitRebaseRetryIsIdempotentAndRerunsChecks(t *testing.T) {
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
	beforeRebase, err := scenario.harness.journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	stepResource := workspace.CommitStepJournalResource(scenario.attempt.AttemptID(), step.ID(), 1)
	stepRevision := beforeRebase.Revision(stepResource)
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
	newEvidence := state.CompletedSteps()[0].Commit().EvidenceDigest()
	if afterRebase.Revision(stepResource) != stepRevision+1 ||
		afterRebase.Revision(workspace.EvidenceJournalResource(newEvidence)) != 1 {
		t.Fatalf(
			"rebase resources: step=%d evidence=%d",
			afterRebase.Revision(stepResource), afterRebase.Revision(workspace.EvidenceJournalResource(newEvidence)),
		)
	}

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

func TestCommitJournalCodecRejectsTamperedProtocolPayload(t *testing.T) {
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

func TestCommitEventsRejectDirectNonPrivilegedAppend(t *testing.T) {
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
		event, mustTime(t, "2026-07-21T12:08:00Z"), nil, nil,
	); err == nil || !strings.Contains(err.Error(), "Git-verified commit workflow") {
		t.Fatalf("direct commit append error = %v", err)
	}
}

func TestCommitProtocolResultsDefensivelyCopyNestedState(t *testing.T) {
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
  - plan_id: alpha-plan
    merge_unit_id: unit-two`
	configuration = strings.Replace(configuration, needle, protocol, 1)
	if configuration == string(fixture.sources.ExecutionConfig.Bytes) {
		t.Fatal("failed to install configured protocol fixture")
	}
	fixture.sources.ExecutionConfig.Bytes = []byte(configuration)
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
	base := mustGitObject(t, 'a')
	goal, _ := workspace.NewGoalBinding(workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit)
	return attemptHarness{
		definition: definition, journal: journal, workspace: workspaceDir, git: &fakeAttemptGit{}, base: base,
		unit: mustMergeUnitReference(t, "alpha-plan", "unit-one"), goal: goal, worktrees: t.TempDir(),
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

func runnerCallCount(runner *protocolCheckRunner) int { return len(runner.invocations) }

var _ workspace.CommitGitPort = (*journalCommitGit)(nil)
