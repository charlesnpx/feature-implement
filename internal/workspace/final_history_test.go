package workspace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type finalHistoryGitStub struct {
	inspections []workspace.GitCommitInspection
	inspectErr  error
	cleanErr    error
	cleanHeads  []workspace.GitObjectID
}

func (stub *finalHistoryGitStub) InspectFirstParentRange(
	context.Context,
	string,
	workspace.GitObjectID,
	workspace.GitObjectID,
) ([]workspace.GitCommitInspection, error) {
	if stub.inspectErr != nil {
		return nil, stub.inspectErr
	}
	return append([]workspace.GitCommitInspection(nil), stub.inspections...), nil
}

func (stub *finalHistoryGitStub) VerifyCleanWorktree(
	_ context.Context,
	_ string,
	head workspace.GitObjectID,
) error {
	stub.cleanHeads = append(stub.cleanHeads, head)
	return stub.cleanErr
}

type finalHistoryCheckRunnerStub struct {
	result      workspace.CheckProcessResult
	err         error
	invocations []workspace.CommitCheckInvocation
}

func (stub *finalHistoryCheckRunnerStub) RunConfiguredCheck(
	_ context.Context,
	invocation workspace.CommitCheckInvocation,
) (workspace.CheckProcessResult, error) {
	stub.invocations = append(stub.invocations, invocation)
	return stub.result, stub.err
}

type finalHistoryFixture struct {
	protocol    workspace.CommitProtocol
	base        workspace.GitObjectID
	head        workspace.GitObjectID
	first       workspace.GitObjectID
	firstTree   workspace.GitObjectID
	secondTree  workspace.GitObjectID
	firstDiff   workspace.CommitDiff
	inspections []workspace.GitCommitInspection
}

func newFinalHistoryFixture(t *testing.T) finalHistoryFixture {
	t.Helper()
	base := mustGitObject(t, 'a')
	first := mustGitObject(t, 'b')
	head := mustGitObject(t, 'c')
	firstTree := mustGitObject(t, 'd')
	secondTree := mustGitObject(t, 'e')
	firstDiff := finalHistoryAddedDiff(t, "src/first.go", mustGitObject(t, 'f'))
	secondDiff := finalHistoryAddedDiff(t, "src/second.go", mustGitObject(t, '1'))
	firstMessage, err := workspace.NewCommitMessagePolicy(
		"Add first checkpoint", workspace.CommitBodyForbidden, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondMessage, err := workspace.NewCommitMessagePolicy(
		"Add second checkpoint", workspace.CommitBodyForbidden, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstPaths, err := workspace.NewCommitPathPolicy([]string{"src/first.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondPaths, err := workspace.NewCommitPathPolicy([]string{"src/second.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	command, err := workspace.NewArgv("true")
	if err != nil {
		t.Fatal(err)
	}
	firstCheck, err := workspace.NewCommitCheck(
		workspace.MustID("first-check"), workspace.MustID("local-runner"), command,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondCheck, err := workspace.NewCommitCheck(
		workspace.MustID("second-check"), workspace.MustID("local-runner"), command,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstStep, err := workspace.NewCommitStep(
		workspace.MustID("first"), firstMessage, firstPaths, []workspace.CommitCheck{firstCheck},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondStep, err := workspace.NewCommitStep(
		workspace.MustID("second"), secondMessage, secondPaths, []workspace.CommitCheck{secondCheck},
	)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := workspace.NewCommitProtocol([]workspace.CommitStep{firstStep, secondStep})
	if err != nil {
		t.Fatal(err)
	}
	firstInspection, err := workspace.NewGitCommitInspection(
		first, []workspace.GitObjectID{base}, firstTree, "Add first checkpoint", "", firstDiff,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondInspection, err := workspace.NewGitCommitInspection(
		head, []workspace.GitObjectID{first}, secondTree, "Add second checkpoint", "", secondDiff,
	)
	if err != nil {
		t.Fatal(err)
	}
	return finalHistoryFixture{
		protocol: protocol, base: base, head: head, first: first, firstTree: firstTree,
		secondTree: secondTree, firstDiff: firstDiff,
		inspections: []workspace.GitCommitInspection{firstInspection, secondInspection},
	}
}

func finalHistoryAddedDiff(
	t *testing.T,
	path string,
	object workspace.GitObjectID,
) workspace.CommitDiff {
	t.Helper()
	change, err := workspace.NewCommitPathChange(
		workspace.CommitChangeAdded, "", path,
		workspace.GitModeAbsent, workspace.GitModeRegular,
		workspace.GitObjectID{}, object,
	)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := workspace.NewCommitDiff([]workspace.CommitPathChange{change})
	if err != nil {
		t.Fatal(err)
	}
	return diff
}

func passingFinalHistoryCheckResult(t *testing.T) workspace.CheckProcessResult {
	t.Helper()
	result, err := workspace.NewCheckProcessResult(
		workspace.CheckExited, 0, "", nil, nil, workspace.StrictCheckIsolationProof(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFinalHistoryVerifierAcceptsOnlyConfiguredFinalSequence(t *testing.T) {
	t.Parallel()

	fixture := newFinalHistoryFixture(t)
	git := &finalHistoryGitStub{inspections: fixture.inspections}
	runner := &finalHistoryCheckRunnerStub{result: passingFinalHistoryCheckResult(t)}
	verifier, err := workspace.NewFinalHistoryVerifier(git, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(
		context.Background(), fixture.protocol, "/private/tmp/final-history", fixture.base, fixture.head,
	); err != nil {
		t.Fatalf("Verify final history: %v", err)
	}
	if len(runner.invocations) != 2 {
		t.Fatalf("configured check invocations = %d, want 2", len(runner.invocations))
	}
	for _, invocation := range runner.invocations {
		if invocation.Commit() != fixture.head || invocation.Tree() != fixture.secondTree ||
			invocation.Worktree() != "/private/tmp/final-history" {
			t.Fatalf("configured check was not bound to final head/tree: %#v", invocation)
		}
	}
	if len(git.cleanHeads) != 3 {
		t.Fatalf("clean worktree checks = %d, want 3", len(git.cleanHeads))
	}
}

func TestFinalHistoryVerifierNamesViolations(t *testing.T) {
	t.Parallel()

	nonzero, err := workspace.NewCheckProcessResult(
		workspace.CheckExited, 1, "", nil, nil, workspace.StrictCheckIsolationProof(),
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		mutate  func(*testing.T, *finalHistoryFixture)
		result  workspace.CheckProcessResult
		wantErr string
	}{
		{
			name: "checkpoint ordering",
			mutate: func(t *testing.T, fixture *finalHistoryFixture) {
				t.Helper()
				inspection, err := workspace.NewGitCommitInspection(
					fixture.first, []workspace.GitObjectID{fixture.base}, fixture.firstTree,
					"Add second checkpoint", "", fixture.firstDiff,
				)
				if err != nil {
					t.Fatal(err)
				}
				fixture.inspections[0] = inspection
			},
			wantErr: "commit checkpoint first message",
		},
		{
			name: "exact subject",
			mutate: func(t *testing.T, fixture *finalHistoryFixture) {
				t.Helper()
				inspection, err := workspace.NewGitCommitInspection(
					fixture.first, []workspace.GitObjectID{fixture.base}, fixture.firstTree,
					"A different subject", "", fixture.firstDiff,
				)
				if err != nil {
					t.Fatal(err)
				}
				fixture.inspections[0] = inspection
			},
			wantErr: "commit checkpoint first message",
		},
		{
			name: "path restriction",
			mutate: func(t *testing.T, fixture *finalHistoryFixture) {
				t.Helper()
				inspection, err := workspace.NewGitCommitInspection(
					fixture.first, []workspace.GitObjectID{fixture.base}, fixture.firstTree,
					"Add first checkpoint", "", finalHistoryAddedDiff(t, "other.go", mustGitObject(t, '2')),
				)
				if err != nil {
					t.Fatal(err)
				}
				fixture.inspections[0] = inspection
			},
			wantErr: "commit checkpoint first path policy",
		},
		{
			name: "parentage",
			mutate: func(t *testing.T, fixture *finalHistoryFixture) {
				t.Helper()
				inspection, err := workspace.NewGitCommitInspection(
					fixture.first, []workspace.GitObjectID{mustGitObject(t, '3')}, fixture.firstTree,
					"Add first checkpoint", "", fixture.firstDiff,
				)
				if err != nil {
					t.Fatal(err)
				}
				fixture.inspections[0] = inspection
			},
			wantErr: "commit checkpoint first has invalid parentage",
		},
		{
			name:    "nonzero configured check",
			result:  nonzero,
			wantErr: "configured check first-check did not exit zero",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFinalHistoryFixture(t)
			if test.mutate != nil {
				test.mutate(t, &fixture)
			}
			result := passingFinalHistoryCheckResult(t)
			if test.result.Termination() != "" {
				result = test.result
			}
			verifier, err := workspace.NewFinalHistoryVerifier(
				&finalHistoryGitStub{inspections: fixture.inspections},
				&finalHistoryCheckRunnerStub{result: result},
			)
			if err != nil {
				t.Fatal(err)
			}
			err = verifier.Verify(
				context.Background(), fixture.protocol, "/private/tmp/final-history", fixture.base, fixture.head,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("final history error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCommitProtocolAllowsRepeatedSubjectsWhenOrderIsExplicit(t *testing.T) {
	t.Parallel()

	message, err := workspace.NewCommitMessagePolicy(
		"Checkpoint", workspace.CommitBodyForbidden, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstPaths, err := workspace.NewCommitPathPolicy([]string{"src/first.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondPaths, err := workspace.NewCommitPathPolicy([]string{"src/second.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := workspace.NewCommitStep(workspace.MustID("first"), message, firstPaths, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.NewCommitStep(workspace.MustID("second"), message, secondPaths, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewCommitProtocol([]workspace.CommitStep{first, second}); err != nil {
		t.Fatalf("ordered protocol rejected repeated exact subjects: %v", err)
	}
}
