package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type protocolCheckRunner struct {
	result      workspace.CheckProcessResult
	err         error
	mutate      func(workspace.CommitCheckInvocation) error
	invocations []workspace.CommitCheckInvocation
}

func (runner *protocolCheckRunner) RunConfiguredCheck(
	_ context.Context,
	invocation workspace.CommitCheckInvocation,
) (workspace.CheckProcessResult, error) {
	runner.invocations = append(runner.invocations, invocation)
	if runner.mutate != nil {
		if err := runner.mutate(invocation); err != nil {
			return workspace.CheckProcessResult{}, err
		}
	}
	return runner.result, runner.err
}

func TestLocalCommitShellCreatesExactCommitAndRevalidatesAfterRebase(t *testing.T) {
	repository, branch, base := newProtocolRepository(t)
	tracked := filepath.Join(repository, "src", "protocol.go")
	if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Enabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")

	step, _ := protocolTestStep(t, "implementation", "Implement protocol")
	protocol, _ := workspace.NewCommitProtocol([]workspace.CommitStep{step})
	state, _ := workspace.NewCommitProtocolState(workspace.DigestBytes([]byte("generation")), base, protocol)
	pass := mustCheckResult(
		t, workspace.CheckExited, 0, "",
		[]byte("{\"Action\":\"pass\",\"Package\":\"example/pkg\"}\n"), nil,
		workspace.StrictCheckIsolationProof(),
	)
	runner := &protocolCheckRunner{result: pass}
	adapter := workspace.DefaultLocalCommitGitAdapter()
	shell, err := workspace.NewCommitProtocolShell(adapter, runner)
	if err != nil {
		t.Fatal(err)
	}
	state, err = shell.ExecuteNextCommitStep(context.Background(), state, branch, repository, "")
	if err != nil {
		t.Fatalf("ExecuteNextCommitStep: %v", err)
	}
	if state.Phase() != workspace.CommitProtocolComplete || len(runner.invocations) != 1 {
		t.Fatalf("state=%#v runner calls=%d", state, len(runner.invocations))
	}
	head := parseGitHead(t, repository)
	completed := state.CompletedSteps()
	if len(completed) != 1 || completed[0].Commit().Commit() != head || len(completed[0].Checks()) != 1 {
		t.Fatalf("completed steps = %#v", completed)
	}
	inspection, err := adapter.InspectCommit(context.Background(), repository, head)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Subject() != "Implement protocol" || inspection.Body() != "" ||
		len(inspection.Parents()) != 1 || inspection.Parents()[0] != base {
		t.Fatalf("commit inspection = %#v", inspection)
	}
	if err := shell.VerifySequence(context.Background(), state, repository, base, head); err != nil {
		t.Fatalf("VerifySequence: %v", err)
	}

	// A new integration base changes the commit and tree identities. The exact
	// sequence and canonical diff remain re-provable, while SHA-bound checks are
	// invalidated and run again.
	runGitSetup(t, repository, "switch", "-c", "upstream", rawGitObject(base))
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "README.md")
	runGitSetup(t, repository, "commit", "-m", "Upstream change")
	newBase := parseGitHead(t, repository)
	runGitSetup(t, repository, "switch", branch)
	runGitSetup(t, repository, "rebase", "--onto", rawGitObject(newBase), rawGitObject(base), branch)
	newHead := parseGitHead(t, repository)
	if newHead == head {
		t.Fatal("rebase did not replace the configured commit object")
	}
	state, err = shell.RemapAfterRebase(context.Background(), state, branch, repository, newBase, newHead)
	if err != nil {
		t.Fatalf("RemapAfterRebase: %v", err)
	}
	if state.Phase() != workspace.CommitProtocolComplete || state.RebaseEpoch() != 1 || len(runner.invocations) != 2 {
		t.Fatalf("rebased state=%#v runner calls=%d", state, len(runner.invocations))
	}
	if err := shell.VerifySequence(context.Background(), state, repository, newBase, newHead); err != nil {
		t.Fatalf("VerifySequence after rebase: %v", err)
	}
}

func TestLocalCommitShellSupportsSHA256Repositories(t *testing.T) {
	repository := t.TempDir()
	branch := "protocol"
	runGitSetup(t, "", "init", "--object-format=sha256", "-b", branch, repository)
	runGitSetup(t, repository, "config", "user.name", "Protocol Test")
	runGitSetup(t, repository, "config", "user.email", "protocol@example.test")
	if err := os.MkdirAll(filepath.Join(repository, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(repository, "src", "protocol.go")
	if err := os.WriteFile(tracked, []byte("package protocol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")
	runGitSetup(t, repository, "commit", "-m", "Initial")
	base := parseGitHead(t, repository)
	if base.Algorithm() != workspace.GitHashSHA256 {
		t.Fatalf("repository object format = %s", base.Algorithm())
	}
	if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Enabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")

	state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
	state, err := shell.ExecuteNextCommitStep(context.Background(), state, branch, repository, "")
	if err != nil {
		t.Fatalf("SHA-256 commit protocol: %v", err)
	}
	head := parseGitHead(t, repository)
	if state.Phase() != workspace.CommitProtocolComplete || state.Head() != head ||
		head.Algorithm() != workspace.GitHashSHA256 {
		t.Fatalf("SHA-256 state=%#v head=%s", state, head)
	}
}

func TestLocalCommitAdapterParsesRenameModesSymlinksAndDeletion(t *testing.T) {
	repository, branch, _ := newProtocolRepository(t)
	newPath := filepath.Join(repository, "src", "renamed.go")
	runGitSetup(t, repository, "mv", "src/protocol.go", "src/renamed.go")
	if err := os.Chmod(newPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("renamed.go", filepath.Join(repository, "src", "protocol-link")); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "-A")
	adapter := workspace.DefaultLocalCommitGitAdapter()
	staged, err := adapter.InspectStaged(context.Background(), repository, branch)
	if err != nil {
		t.Fatalf("InspectStaged: %v", err)
	}
	if !staged.Eligible() {
		t.Fatalf("staged inspection is ineligible: %#v", staged)
	}
	changes := staged.Diff().Changes()
	var sawRename, sawSymlink bool
	for _, change := range changes {
		switch {
		case change.Kind() == workspace.CommitChangeRenamed && change.OldPath() == "src/protocol.go" && change.NewPath() == "src/renamed.go":
			sawRename = true
			if change.OldMode() != workspace.GitModeRegular || change.NewMode() != workspace.GitModeExecutable {
				t.Fatalf("rename modes = %s -> %s", change.OldMode(), change.NewMode())
			}
		case change.Kind() == workspace.CommitChangeAdded && change.NewPath() == "src/protocol-link":
			sawSymlink = true
			if change.NewMode() != workspace.GitModeSymlink {
				t.Fatalf("symlink mode = %s", change.NewMode())
			}
		}
	}
	if !sawRename || !sawSymlink {
		t.Fatalf("raw changes = %#v", changes)
	}
}

func TestLocalCommitAdapterRejectsDirtySubmoduleBeforeCommit(t *testing.T) {
	submodule := t.TempDir()
	runGitSetup(t, "", "init", "-b", "main", submodule)
	runGitSetup(t, submodule, "config", "user.name", "Protocol Test")
	runGitSetup(t, submodule, "config", "user.email", "protocol@example.test")
	if err := os.WriteFile(filepath.Join(submodule, "module.txt"), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, submodule, "add", "module.txt")
	runGitSetup(t, submodule, "commit", "-m", "Initial module")

	repository, branch, _ := newProtocolRepository(t)
	runGitSetup(t, repository, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "modules/tool")
	if err := os.WriteFile(filepath.Join(repository, "modules", "tool", "module.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inspection, err := workspace.DefaultLocalCommitGitAdapter().InspectStaged(context.Background(), repository, branch)
	if err != nil {
		t.Fatalf("InspectStaged: %v", err)
	}
	if inspection.Eligible() || len(inspection.Unstaged()) != 1 || inspection.Unstaged()[0] != "modules/tool" {
		t.Fatalf("dirty submodule inspection = %#v, unstaged=%v", inspection, inspection.Unstaged())
	}
}

func TestCommitShellRejectsDirtyStateWeakIsolationAndWrongFailure(t *testing.T) {
	t.Run("dirty before commit", func(t *testing.T) {
		repository, branch, base := newProtocolRepository(t)
		tracked := filepath.Join(repository, "src", "protocol.go")
		if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Staged = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", "src/protocol.go")
		if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Unstaged = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
		runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
		shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
		_, err := shell.ExecuteNextCommitStep(context.Background(), state, branch, repository, "")
		if err == nil || !strings.Contains(err.Error(), "no unstaged, untracked, or conflicted") {
			t.Fatalf("dirty pre-commit error = %v", err)
		}
	})

	t.Run("weak check isolation", func(t *testing.T) {
		repository, branch, base := stagedProtocolRepository(t)
		state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
		weak := workspace.NewCheckIsolationProof(true, false, false, false)
		runner := &protocolCheckRunner{result: passingCheckResult(t, weak)}
		shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
		state, err := shell.ExecuteNextCommitStep(context.Background(), state, branch, repository, "")
		if err == nil || !strings.Contains(err.Error(), "did not prove") {
			t.Fatalf("weak isolation error = %v", err)
		}
		if state.Phase() != workspace.CommitProtocolAwaitingChecks {
			t.Fatalf("in-flight state was not preserved: %#v", state)
		}
	})

	t.Run("wrong structured failure", func(t *testing.T) {
		repository, branch, base := stagedProtocolRepository(t)
		state := oneStepProtocolState(
			t, base, workspace.CheckExpectationExpectedTestFailure,
			[]string{"example/pkg::TestExpected"},
		)
		wrongOutput := []byte(strings.Join([]string{
			`{"Action":"fail","Package":"example/pkg","Test":"TestOther"}`,
			`{"Action":"fail","Package":"example/pkg"}`,
			"",
		}, "\n"))
		runner := &protocolCheckRunner{result: mustCheckResult(
			t, workspace.CheckExited, 1, "", wrongOutput, nil, workspace.StrictCheckIsolationProof(),
		)}
		shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
		_, err := shell.ExecuteNextCommitStep(context.Background(), state, branch, repository, "")
		if err == nil || !strings.Contains(err.Error(), "does not satisfy") {
			t.Fatalf("wrong failure error = %v", err)
		}
	})

	t.Run("dirty after check", func(t *testing.T) {
		repository, branch, base := stagedProtocolRepository(t)
		state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
		runner := &protocolCheckRunner{
			result: passingCheckResult(t, workspace.StrictCheckIsolationProof()),
			mutate: func(invocation workspace.CommitCheckInvocation) error {
				return os.WriteFile(filepath.Join(invocation.Worktree(), "src", "check-output.tmp"), []byte("dirty\n"), 0o644)
			},
		}
		shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
		_, err := shell.ExecuteNextCommitStep(context.Background(), state, branch, repository, "")
		if err == nil || !strings.Contains(err.Error(), "changed Git state") {
			t.Fatalf("dirty post-check error = %v", err)
		}
	})
}

func TestCommitShellAcceptsOnlyTheConfiguredExpectedFailure(t *testing.T) {
	repository, branch, base := stagedProtocolRepository(t)
	state := oneStepProtocolState(
		t, base, workspace.CheckExpectationExpectedTestFailure,
		[]string{"example/pkg::TestExpected"},
	)
	output := []byte(strings.Join([]string{
		`{"Action":"fail","Package":"example/pkg","Test":"TestExpected"}`,
		`{"Action":"fail","Package":"example/pkg"}`,
		"",
	}, "\n"))
	runner := &protocolCheckRunner{result: mustCheckResult(
		t, workspace.CheckExited, 1, "", output, nil, workspace.StrictCheckIsolationProof(),
	)}
	shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
	state, err := shell.ExecuteNextCommitStep(context.Background(), state, branch, repository, "")
	if err != nil || state.Phase() != workspace.CommitProtocolComplete {
		t.Fatalf("expected failure state=%#v err=%v", state, err)
	}
}

func newProtocolRepository(t *testing.T) (string, string, workspace.GitObjectID) {
	t.Helper()
	repository := t.TempDir()
	branch := "protocol"
	runGitSetup(t, "", "init", "-b", branch, repository)
	runGitSetup(t, repository, "config", "user.name", "Protocol Test")
	runGitSetup(t, repository, "config", "user.email", "protocol@example.test")
	if err := os.MkdirAll(filepath.Join(repository, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "src", "protocol.go"), []byte("package protocol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")
	runGitSetup(t, repository, "commit", "-m", "Initial")
	return repository, branch, parseGitHead(t, repository)
}

func stagedProtocolRepository(t *testing.T) (string, string, workspace.GitObjectID) {
	t.Helper()
	repository, branch, base := newProtocolRepository(t)
	if err := os.WriteFile(
		filepath.Join(repository, "src", "protocol.go"),
		[]byte("package protocol\n\nconst Enabled = true\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")
	return repository, branch, base
}

func parseGitHead(t *testing.T, repository string) workspace.GitObjectID {
	t.Helper()
	raw := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "HEAD")))
	algorithm := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "--show-object-format")))
	object, err := workspace.ParseGitObjectID(algorithm + ":" + raw)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func rawGitObject(object workspace.GitObjectID) string {
	return strings.TrimPrefix(object.String(), string(object.Algorithm())+":")
}

func oneStepProtocolState(
	t *testing.T,
	base workspace.GitObjectID,
	expectationKind workspace.CheckExpectationKind,
	failureIDs []string,
) workspace.CommitProtocolState {
	t.Helper()
	message, _ := workspace.NewCommitMessagePolicy("Implement protocol", workspace.CommitBodyForbidden, nil)
	paths, _ := workspace.NewCommitPathPolicy([]string{"src/**"}, []string{})
	expectation, err := workspace.NewCheckExpectation(expectationKind, failureIDs)
	if err != nil {
		t.Fatal(err)
	}
	command, _ := workspace.NewArgv("go", "test", "./...")
	check, _ := workspace.NewCommitCheck(
		workspace.MustID("protocol-check"), workspace.MustID("codex"),
		workspace.CheckParserGoTestJSON, command, expectation,
	)
	step, _ := workspace.NewCommitStep(workspace.MustID("implementation"), message, paths, []workspace.CommitCheck{check})
	protocol, _ := workspace.NewCommitProtocol([]workspace.CommitStep{step})
	state, err := workspace.NewCommitProtocolState(workspace.DigestBytes([]byte("generation")), base, protocol)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func passingCheckResult(t *testing.T, proof workspace.CheckIsolationProof) workspace.CheckProcessResult {
	t.Helper()
	return mustCheckResult(
		t, workspace.CheckExited, 0, "",
		[]byte("{\"Action\":\"pass\",\"Package\":\"example/pkg\"}\n"), nil, proof,
	)
}

var _ workspace.CommitCheckRunnerPort = (*protocolCheckRunner)(nil)
