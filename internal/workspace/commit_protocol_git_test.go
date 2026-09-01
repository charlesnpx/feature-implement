package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

const commitProtocolGitTestChildEnvironment = "FEATURE_IMPLEMENT_COMMIT_PROTOCOL_GIT_TEST_CHILD"

func runCommitProtocolGitTestInEnvironment(t *testing.T, environment ...string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$")
	command.Env = append(os.Environ(), environment...)
	command.Env = append(command.Env, commitProtocolGitTestChildEnvironment+"="+t.Name())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s in isolated environment: %v: %s", t.Name(), err, output)
	}
}

func TestLocalCommitUsesTargetRepositoryIdentity(t *testing.T) {
	t.Parallel()

	if os.Getenv(commitProtocolGitTestChildEnvironment) != t.Name() {
		globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
		if err := os.WriteFile(globalConfig, []byte(
			"[user]\n\tname = Ambient Identity\n\temail = ambient@example.test\n",
		), 0o600); err != nil {
			t.Fatal(err)
		}
		runCommitProtocolGitTestInEnvironment(
			t,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL="+globalConfig,
			"GIT_CONFIG_SYSTEM="+os.DevNull,
		)
		return
	}

	harness := newConfiguredAttemptHarness(t)
	target := harness.definition.Workspace().RepositoryRoot()
	runGitSetup(t, target, "config", "user.name", "Target Identity")
	runGitSetup(t, target, "config", "user.email", "target@example.test")
	attempt := harness.reserveWithLocalGit(t, "2026-07-21T10:55:00Z")

	if err := os.MkdirAll(filepath.Join(attempt.Worktree(), "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(attempt.Worktree(), "src", "identity.go"),
		[]byte("package protocol\n\nconst Identity = true\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, attempt.Worktree(), "add", "src/identity.go")

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
		t.Fatalf("execute configured commit: %v", err)
	}
	state, configured := result.Protocol()
	if !configured || state.Phase() != workspace.CommitProtocolComplete {
		t.Fatalf("configured commit result = %#v configured=%v", state, configured)
	}
	identity := strings.Split(strings.TrimSuffix(string(runGitSetup(
		t, attempt.Worktree(), "show", "-s", "--format=%an%x00%ae%x00%cn%x00%ce", rawGitObject(state.Head()),
	)), "\n"), "\x00")
	if len(identity) != 4 || identity[0] != "Target Identity" || identity[1] != "target@example.test" ||
		identity[2] != "Target Identity" || identity[3] != "target@example.test" {
		t.Fatalf("configured commit identity = %#v", identity)
	}
	for _, key := range []string{"user.name", "user.email"} {
		command := exec.Command("git", "-C", attempt.Worktree(), "config", "--local", "--get", key)
		if output, err := command.Output(); err == nil {
			t.Fatalf("attempt repository persisted %s=%q", key, output)
		} else {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
				t.Fatalf("inspect attempt-local %s: %v", key, err)
			}
		}
	}
}

func TestLocalCommitRejectsMissingTargetRepositoryIdentity(t *testing.T) {
	t.Parallel()

	if os.Getenv(commitProtocolGitTestChildEnvironment) != t.Name() {
		home := t.TempDir()
		runCommitProtocolGitTestInEnvironment(
			t,
			"HOME="+home,
			"XDG_CONFIG_HOME="+home,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_SYSTEM="+os.DevNull,
		)
		return
	}

	harness := newConfiguredAttemptHarness(t)
	attempt := harness.reserveWithLocalGit(t, "2026-07-21T10:55:00Z")
	if err := os.MkdirAll(filepath.Join(attempt.Worktree(), "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(attempt.Worktree(), "src", "identity.go"),
		[]byte("package protocol\n\nconst Identity = false\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, attempt.Worktree(), "add", "src/identity.go")
	before := parseGitHead(t, attempt.Worktree())

	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, err := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = workspace.ExecuteAttemptCommitStep(
		context.Background(), harness.journal, harness.definition, shell,
		workspace.ExecuteAttemptCommitStepRequest{
			AttemptID: attempt.AttemptID(), OccurredAt: mustTime(t, "2026-07-21T10:57:00Z"),
		},
	)
	if err == nil || !strings.Contains(
		err.Error(), "target repository identity is incomplete; set user.name and user.email",
	) {
		t.Fatalf("missing target identity error = %v", err)
	}
	if after := parseGitHead(t, attempt.Worktree()); after != before {
		t.Fatalf("missing target identity created commit %s, want head %s", after, before)
	}
}

func TestLocalCommitShellCreatesExactCommitAndRevalidatesAfterRebase(t *testing.T) {
	t.Parallel()

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
	state, err = shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
	if err != nil {
		t.Fatalf("ExecuteNextCommitStep: %v", err)
	}
	if state.Phase() != workspace.CommitProtocolComplete || len(runner.invocations) != 1 {
		t.Fatalf("state=%#v runner calls=%d", state, len(runner.invocations))
	}
	var zeroShell workspace.CommitProtocolShell
	if err := zeroShell.VerifySequence(context.Background(), state, repository, base, state.Head()); err == nil ||
		!strings.Contains(err.Error(), "no Git adapter") {
		t.Fatalf("zero-value VerifySequence error = %v", err)
	}
	head := parseGitHead(t, repository)
	runGitSetup(t, repository, "branch", "-f", branch, rawGitObject(head))
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
	runGitSetup(t, repository, "switch", "--detach", rawGitObject(newHead))
	if newHead == head {
		t.Fatal("rebase did not replace the configured commit object")
	}
	state, err = shell.RemapAfterRebase(context.Background(), state, repository, newBase, newHead)
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

func TestLocalCommitShellRerunsEachRebasedStepCheckFromFinalHead(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "multi-step Git rebase permutation")

	repository, branch, base := newProtocolRepository(t)
	firstStep, _ := protocolTestStep(t, "first", "Implement first step")
	secondStep, _ := protocolTestStep(t, "second", "Implement second step")
	protocol, err := workspace.NewCommitProtocol([]workspace.CommitStep{firstStep, secondStep})
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewCommitProtocolState(workspace.DigestBytes([]byte("generation")), base, protocol)
	if err != nil {
		t.Fatal(err)
	}
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)

	for index, name := range []string{"first.go", "second.go"} {
		if err := os.WriteFile(
			filepath.Join(repository, "src", name),
			[]byte("package protocol\n"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", filepath.Join("src", name))
		state, err = shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
		if err != nil {
			t.Fatalf("execute step %d: %v", index+1, err)
		}
	}
	if state.Phase() != workspace.CommitProtocolComplete || len(runner.invocations) != 2 {
		t.Fatalf("initial state=%#v runner calls=%d", state, len(runner.invocations))
	}
	runGitSetup(t, repository, "branch", "-f", branch, rawGitObject(state.Head()))

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
	runGitSetup(t, repository, "switch", "--detach", rawGitObject(newHead))

	state, err = shell.RemapAfterRebase(context.Background(), state, repository, newBase, newHead)
	if err != nil {
		t.Fatalf("remap two-step rebase: %v", err)
	}
	completed := state.CompletedSteps()
	if state.Phase() != workspace.CommitProtocolComplete || state.Head() != newHead ||
		len(completed) != 2 || len(runner.invocations) != 4 ||
		runner.invocations[2].Commit() != completed[0].Commit().Commit() ||
		runner.invocations[3].Commit() != completed[1].Commit().Commit() {
		t.Fatalf("rebased state=%#v completed=%#v invocations=%#v", state, completed, runner.invocations)
	}
}

func TestLocalCommitShellRemapsBaseOnlyRebaseBeforeFirstStep(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "base-only Git rebase permutation")

	repository, branch, base := newProtocolRepository(t)
	step, _ := protocolTestStep(t, "implementation", "Implement protocol")
	protocol, _ := workspace.NewCommitProtocol([]workspace.CommitStep{step})
	state, _ := workspace.NewCommitProtocolState(workspace.DigestBytes([]byte("generation")), base, protocol)
	shell, _ := workspace.NewCommitProtocolShell(
		workspace.DefaultLocalCommitGitAdapter(),
		&protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())},
	)

	runGitSetup(t, repository, "switch", "-c", "upstream", rawGitObject(base))
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "README.md")
	runGitSetup(t, repository, "commit", "-m", "Upstream change")
	newBase := parseGitHead(t, repository)
	runGitSetup(t, repository, "switch", branch)
	runGitSetup(t, repository, "reset", "--hard", rawGitObject(newBase))
	if err := os.WriteFile(filepath.Join(repository, "unexpected.txt"), []byte("unexpected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "unexpected.txt")
	runGitSetup(t, repository, "commit", "-m", "Unexpected commit")
	unrecordedHead := parseGitHead(t, repository)
	runGitSetup(t, repository, "switch", "--detach", rawGitObject(unrecordedHead))
	if _, err := shell.RemapAfterRebase(
		context.Background(), state, repository, newBase, unrecordedHead,
	); err == nil || !strings.Contains(err.Error(), "base-only commit rebase must end at the new base") {
		t.Fatalf("base-only remap with an unrecorded commit error=%v", err)
	}
	runGitSetup(t, repository, "switch", branch)
	runGitSetup(t, repository, "reset", "--hard", rawGitObject(newBase))
	runGitSetup(t, repository, "switch", "--detach", rawGitObject(newBase))

	state, err := shell.RemapAfterRebase(context.Background(), state, repository, newBase, newBase)
	if err != nil {
		t.Fatalf("base-only RemapAfterRebase: %v", err)
	}
	if state.Phase() != workspace.CommitProtocolReady || state.Base() != newBase || state.Head() != newBase ||
		state.RebaseEpoch() != 1 || len(state.CompletedSteps()) != 0 {
		t.Fatalf("base-only remapped state=%#v", state)
	}
}

func TestLocalCommitShellSupportsSHA256Repositories(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "Git object-format permutation")

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
	runGitSetup(t, repository, "switch", "--detach", rawGitObject(base))
	if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Enabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")

	state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
	state, err := shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
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
	t.Parallel()
	requireFullSuite(t, "Git diff-shape permutation")

	repository, _, _ := newProtocolRepository(t)
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
	staged, err := adapter.InspectStaged(context.Background(), repository)
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
	t.Parallel()

	submodule := t.TempDir()
	runGitSetup(t, "", "init", "-b", "main", submodule)
	runGitSetup(t, submodule, "config", "user.name", "Protocol Test")
	runGitSetup(t, submodule, "config", "user.email", "protocol@example.test")
	if err := os.WriteFile(filepath.Join(submodule, "module.txt"), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, submodule, "add", "module.txt")
	runGitSetup(t, submodule, "commit", "-m", "Initial module")

	repository, _, _ := newProtocolRepository(t)
	runGitSetup(t, repository, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "modules/tool")
	runGitSetup(t, repository, "config", "diff.ignoreSubmodules", "all")
	runGitSetup(t, repository, "config", "submodule.modules/tool.ignore", "all")
	if err := os.WriteFile(filepath.Join(repository, "modules", "tool", "module.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inspection, err := workspace.DefaultLocalCommitGitAdapter().InspectStaged(context.Background(), repository)
	if err != nil {
		t.Fatalf("InspectStaged: %v", err)
	}
	if inspection.Eligible() || len(inspection.Unstaged()) != 1 || inspection.Unstaged()[0] != "modules/tool" {
		t.Fatalf("dirty submodule inspection = %#v, unstaged=%v", inspection, inspection.Unstaged())
	}
}

func TestLocalCommitAdapterDoesNotHideStagedGitlinkFromPathPolicy(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "Git submodule-index permutation")

	submodule := t.TempDir()
	runGitSetup(t, "", "init", "-b", "main", submodule)
	runGitSetup(t, submodule, "config", "user.name", "Protocol Test")
	runGitSetup(t, submodule, "config", "user.email", "protocol@example.test")
	moduleFile := filepath.Join(submodule, "module.txt")
	if err := os.WriteFile(moduleFile, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, submodule, "add", "module.txt")
	runGitSetup(t, submodule, "commit", "-m", "Initial module")

	repository, _, _ := newProtocolRepository(t)
	runGitSetup(t, repository, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "modules/tool")
	runGitSetup(t, repository, "commit", "-m", "Add module")
	base := parseGitHead(t, repository)
	moduleWorktree := filepath.Join(repository, "modules", "tool")
	runGitSetup(t, moduleWorktree, "config", "user.name", "Protocol Test")
	runGitSetup(t, moduleWorktree, "config", "user.email", "protocol@example.test")
	if err := os.WriteFile(filepath.Join(moduleWorktree, "module.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, moduleWorktree, "add", "module.txt")
	runGitSetup(t, moduleWorktree, "commit", "-m", "Update module")
	runGitSetup(t, repository, "add", "modules/tool")
	if err := os.WriteFile(
		filepath.Join(repository, "src", "protocol.go"),
		[]byte("package protocol\n\nconst Allowed = true\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")
	runGitSetup(t, repository, "config", "diff.ignoreSubmodules", "all")
	runGitSetup(t, repository, "config", "submodule.modules/tool.ignore", "all")

	state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, err := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shell.ExecuteNextCommitStep(context.Background(), state, repository, ""); err == nil ||
		!strings.Contains(err.Error(), "outside configured allowed_paths") {
		t.Fatalf("hidden gitlink path-policy error = %v", err)
	}
	if head := parseGitHead(t, repository); head != base {
		t.Fatalf("path-policy rejection advanced head from %s to %s", base, head)
	}
}

func TestLocalCommitAdapterRejectsHiddenIndexFlags(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		flag string
	}{
		{"assume unchanged", "--assume-unchanged"},
		{"skip worktree", "--skip-worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, _, head := newProtocolRepository(t)
			runGitSetup(t, repository, "update-index", test.flag, "src/protocol.go")
			if err := os.WriteFile(
				filepath.Join(repository, "src", "protocol.go"),
				[]byte("package protocol\n\nconst Hidden = true\n"), 0o644,
			); err != nil {
				t.Fatal(err)
			}
			adapter := workspace.DefaultLocalCommitGitAdapter()
			if _, err := adapter.InspectStaged(context.Background(), repository); err == nil ||
				!strings.Contains(err.Error(), "assume-unchanged and skip-worktree") {
				t.Fatalf("InspectStaged hidden-index error = %v", err)
			}
			if err := adapter.VerifyCleanWorktree(context.Background(), repository, head); err == nil ||
				!strings.Contains(err.Error(), "assume-unchanged and skip-worktree") {
				t.Fatalf("VerifyCleanWorktree hidden-index error = %v", err)
			}
		})
	}
}

func TestLocalCommitAdapterIgnoresReplacementRefsAndLegacyGrafts(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "Git history-replacement profile matrix")

	t.Run("replacement ref", func(t *testing.T) {
		repository, _, base := newProtocolRepository(t)
		tracked := filepath.Join(repository, "src", "protocol.go")
		if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Real = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", "src/protocol.go")
		runGitSetup(t, repository, "commit", "-m", "Real commit")
		realCommit := parseGitHead(t, repository)
		tree := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", rawGitObject(realCommit)+"^{tree}")))
		replacement := strings.TrimSpace(string(runGitSetup(
			t, repository, "commit-tree", tree, "-p", rawGitObject(base), "-m", "Replacement commit",
		)))
		runGitSetup(t, repository, "replace", rawGitObject(realCommit), replacement)
		if raw := string(runGitSetup(t, repository, "cat-file", "commit", rawGitObject(realCommit))); !strings.Contains(raw, "Replacement commit") {
			t.Fatalf("replacement ref did not affect ordinary Git inspection: %q", raw)
		}

		adapter := workspace.DefaultLocalCommitGitAdapter()
		inspection, err := adapter.InspectCommit(context.Background(), repository, realCommit)
		if err != nil {
			t.Fatalf("InspectCommit with replacement ref: %v", err)
		}
		if inspection.Subject() != "Real commit" || len(inspection.Parents()) != 1 || inspection.Parents()[0] != base {
			t.Fatalf("replacement ref changed trusted inspection: %#v", inspection)
		}
		rangeInspection, err := adapter.InspectFirstParentRange(
			context.Background(), repository, base, realCommit,
		)
		if err != nil || len(rangeInspection) != 1 || rangeInspection[0].Subject() != "Real commit" {
			t.Fatalf("replacement ref changed trusted range: %#v err=%v", rangeInspection, err)
		}
	})

	t.Run("legacy graft", func(t *testing.T) {
		repository, _, base := newProtocolRepository(t)
		tracked := filepath.Join(repository, "src", "protocol.go")
		if err := os.WriteFile(tracked, []byte("package protocol\n\nconst Real = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", "src/protocol.go")
		runGitSetup(t, repository, "commit", "-m", "Real graft target")
		realCommit := parseGitHead(t, repository)
		gitDir := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "--absolute-git-dir")))
		runGitSetup(t, repository, "config", "advice.graftFileDeprecated", "false")
		if err := os.WriteFile(
			filepath.Join(gitDir, "info", "grafts"), []byte(rawGitObject(realCommit)+"\n"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		ordinaryParents := strings.Fields(string(runGitSetup(
			t, repository, "rev-list", "--parents", "-n", "1", rawGitObject(realCommit),
		)))
		if len(ordinaryParents) != 1 {
			t.Fatalf("legacy graft did not affect ordinary Git history: %v", ordinaryParents)
		}

		inspection, err := workspace.DefaultLocalCommitGitAdapter().InspectCommit(
			context.Background(), repository, realCommit,
		)
		if err != nil {
			t.Fatalf("InspectCommit with legacy graft: %v", err)
		}
		if len(inspection.Parents()) != 1 || inspection.Parents()[0] != base {
			t.Fatalf("legacy graft changed trusted inspection: %#v", inspection)
		}
	})
}

func TestLocalCommitAdapterDoesNotTrustFsmonitorOrIgnoredInputs(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "Git fsmonitor and ignore profile matrix")

	t.Run("lying fsmonitor", func(t *testing.T) {
		repository, _, head := newProtocolRepository(t)
		hook := filepath.Join(t.TempDir(), "lying-fsmonitor")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'token\\000'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "config", "core.fsmonitor", hook)
		runGitSetup(t, repository, "config", "core.fsmonitorHookVersion", "2")
		_ = runGitSetup(t, repository, "status", "--porcelain=v2", "-z", "--untracked-files=all")
		if err := os.WriteFile(
			filepath.Join(repository, "src", "protocol.go"),
			[]byte("package protocol\n\nconst HiddenByMonitor = true\n"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
		if ordinary := runGitSetup(t, repository, "status", "--porcelain=v2", "-z", "--untracked-files=all"); len(ordinary) != 0 {
			t.Fatalf("lying fsmonitor did not hide the change from ordinary Git: %q", ordinary)
		}
		if err := workspace.DefaultLocalCommitGitAdapter().VerifyCleanWorktree(
			context.Background(), repository, head,
		); err == nil {
			t.Fatal("trusted clean-worktree verification accepted a change hidden by fsmonitor")
		}
	})

	t.Run("ignored input", func(t *testing.T) {
		repository, _, _ := newProtocolRepository(t)
		if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("generated/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", ".gitignore")
		runGitSetup(t, repository, "commit", "-m", "Ignore generated inputs")
		head := parseGitHead(t, repository)
		if err := os.MkdirAll(filepath.Join(repository, "generated"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(repository, "generated", "input.go"), []byte("package generated\n"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
		if ordinary := runGitSetup(t, repository, "status", "--porcelain=v2", "-z", "--untracked-files=all"); len(ordinary) != 0 {
			t.Fatalf("ignored input was not hidden from ordinary Git status: %q", ordinary)
		}
		if err := workspace.DefaultLocalCommitGitAdapter().VerifyCleanWorktree(
			context.Background(), repository, head,
		); err == nil || !strings.Contains(err.Error(), "dirty") {
			t.Fatalf("ignored-input clean-worktree error = %v", err)
		}
	})
}

func TestLocalCommitAdapterVerifiesRawBytesTypesAndModes(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "Git byte-type-mode matrix")

	t.Run("external clean filter", func(t *testing.T) {
		repository, branch, _ := newProtocolRepository(t)
		if err := os.WriteFile(
			filepath.Join(repository, ".gitattributes"), []byte("src/protocol.go filter=constant\n"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", ".gitattributes")
		runGitSetup(t, repository, "commit", "-m", "Configure clean filter")
		head := parseGitHead(t, repository)
		tree := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "HEAD^{tree}")))
		alternate := strings.TrimSpace(string(runGitSetup(
			t, repository, "commit-tree", tree, "-p", rawGitObject(head), "-m", "Same tree attacker commit",
		)))
		filter := filepath.Join(t.TempDir(), "moving-clean-filter")
		sentinel := filepath.Join(t.TempDir(), "filter-ran")
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
		script := "#!/bin/sh\ngit -C " + quote(repository) + " update-ref refs/heads/" + branch + " " + alternate +
			"\n: > " + quote(sentinel) + "\ncat\n"
		if err := os.WriteFile(filter, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "config", "filter.constant.clean", filter)
		runGitSetup(t, repository, "config", "filter.constant.required", "true")
		if err := os.WriteFile(
			filepath.Join(repository, "src", "protocol.go"),
			[]byte("malicious content\n"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
		adapter := workspace.DefaultLocalCommitGitAdapter()
		if err := adapter.VerifyCleanWorktree(
			context.Background(), repository, head,
		); err == nil || !strings.Contains(err.Error(), "external Git filter") {
			t.Fatalf("clean-filter rejection error = %v", err)
		}
		if _, err := adapter.InspectStaged(context.Background(), repository); err == nil ||
			!strings.Contains(err.Error(), "external Git filter") {
			t.Fatalf("staged clean-filter rejection error = %v", err)
		}
		if moved := parseGitHead(t, repository); moved != head {
			t.Fatalf("trusted verification executed filter and moved head from %s to %s", head, moved)
		}
		if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("trusted verification executed external clean filter: %v", err)
		}
	})

	t.Run("file mode", func(t *testing.T) {
		repository, _, head := newProtocolRepository(t)
		tracked := filepath.Join(repository, "src", "protocol.go")
		runGitSetup(t, repository, "config", "core.fileMode", "false")
		if err := os.Chmod(tracked, 0o755); err != nil {
			t.Fatal(err)
		}
		if ordinary := runGitSetup(t, repository, "status", "--porcelain=v2", "-z"); len(ordinary) != 0 {
			t.Fatalf("core.fileMode=false did not hide mode change: %q", ordinary)
		}
		if err := workspace.DefaultLocalCommitGitAdapter().VerifyCleanWorktree(
			context.Background(), repository, head,
		); err == nil || !strings.Contains(err.Error(), "executable mode differs") {
			t.Fatalf("raw file-mode verification error = %v", err)
		}
	})

	t.Run("stat cache", func(t *testing.T) {
		repository, _, head := newProtocolRepository(t)
		tracked := filepath.Join(repository, "src", "protocol.go")
		before, err := os.Stat(tracked)
		if err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "config", "core.trustctime", "false")
		runGitSetup(t, repository, "config", "core.checkStat", "minimal")
		oldTime := time.Now().Add(-10 * time.Second).Truncate(time.Second)
		if err := os.Chtimes(tracked, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "update-index", "--really-refresh")
		before, err = os.Stat(tracked)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tracked, []byte("package protocom\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(tracked, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
		if ordinary := runGitSetup(t, repository, "status", "--porcelain=v2", "-z"); len(ordinary) != 0 {
			t.Fatalf("stat-cache configuration did not hide same-size change: %q", ordinary)
		}
		if err := workspace.DefaultLocalCommitGitAdapter().VerifyCleanWorktree(
			context.Background(), repository, head,
		); err == nil || !strings.Contains(err.Error(), "bytes differ from tree") {
			t.Fatalf("stat-cache raw verification error = %v", err)
		}
	})

	t.Run("symlink type", func(t *testing.T) {
		repository, _, _ := newProtocolRepository(t)
		link := filepath.Join(repository, "src", "protocol.go")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, "src", "target.go"), []byte("package protocol\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target.go", link); err != nil {
			t.Fatal(err)
		}
		runGitSetup(t, repository, "add", "-A")
		runGitSetup(t, repository, "commit", "-m", "Track protocol symlink")
		head := parseGitHead(t, repository)
		runGitSetup(t, repository, "config", "core.symlinks", "false")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(link, []byte("target.go"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ordinary := runGitSetup(t, repository, "status", "--porcelain=v2", "-z"); len(ordinary) != 0 {
			t.Fatalf("core.symlinks=false did not hide type change: %q", ordinary)
		}
		if err := workspace.DefaultLocalCommitGitAdapter().VerifyCleanWorktree(
			context.Background(), repository, head,
		); err == nil || !strings.Contains(err.Error(), "not a symbolic link") {
			t.Fatalf("raw symlink verification error = %v", err)
		}
	})
}

func TestLocalCommitAdapterDisablesReferenceTransactionHooks(t *testing.T) {
	t.Parallel()

	repository, _, base := stagedProtocolRepository(t)
	gitDir := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "--absolute-git-dir")))
	hook := filepath.Join(gitDir, "hooks", "reference-transaction")
	sentinel := filepath.Join(gitDir, "hook-ran")
	hookScript := "#!/bin/sh\n: > '" + strings.ReplaceAll(sentinel, "'", "'\"'\"'") + "'\n"
	if err := os.WriteFile(hook, []byte(hookScript), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "update-ref", "refs/probe/hook", rawGitObject(base))
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("reference-transaction hook did not run during control update: %v", err)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "update-ref", "-d", "refs/probe/hook")
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}

	state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
	runner := &protocolCheckRunner{result: passingCheckResult(t, workspace.StrictCheckIsolationProof())}
	shell, err := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
	if err != nil {
		t.Fatal(err)
	}
	state, err = shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
	if err != nil || state.Phase() != workspace.CommitProtocolComplete {
		t.Fatalf("configured commit with disabled hooks state=%#v err=%v", state, err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("privileged commit executed reference-transaction hook: %v", err)
	}
}

func TestCommitShellRejectsDirtyStateWeakIsolationAndWrongFailure(t *testing.T) {
	t.Parallel()

	t.Run("dirty before commit", func(t *testing.T) {
		repository, _, base := newProtocolRepository(t)
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
		_, err := shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
		if err == nil || !strings.Contains(err.Error(), "no unstaged, untracked, or conflicted") {
			t.Fatalf("dirty pre-commit error = %v", err)
		}
	})

	t.Run("weak check isolation", func(t *testing.T) {
		repository, _, base := stagedProtocolRepository(t)
		state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
		weak := workspace.NewCheckIsolationProof(true, false)
		runner := &protocolCheckRunner{result: passingCheckResult(t, weak)}
		shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
		state, err := shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
		if err == nil || !strings.Contains(err.Error(), "did not prove") {
			t.Fatalf("weak isolation error = %v", err)
		}
		if state.Phase() != workspace.CommitProtocolAwaitingChecks {
			t.Fatalf("in-flight state was not preserved: %#v", state)
		}
	})

	t.Run("wrong structured failure", func(t *testing.T) {
		repository, _, base := stagedProtocolRepository(t)
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
		_, err := shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
		if err == nil || !strings.Contains(err.Error(), "does not satisfy") {
			t.Fatalf("wrong failure error = %v", err)
		}
	})

	t.Run("dirty after check", func(t *testing.T) {
		repository, _, base := stagedProtocolRepository(t)
		state := oneStepProtocolState(t, base, workspace.CheckExpectationPass, nil)
		runner := &protocolCheckRunner{
			result: passingCheckResult(t, workspace.StrictCheckIsolationProof()),
			mutate: func(invocation workspace.CommitCheckInvocation) error {
				return os.WriteFile(filepath.Join(invocation.Worktree(), "src", "check-output.tmp"), []byte("dirty\n"), 0o644)
			},
		}
		shell, _ := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), runner)
		_, err := shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
		if err == nil || !strings.Contains(err.Error(), "changed Git state") {
			t.Fatalf("dirty post-check error = %v", err)
		}
	})
}

func TestCommitShellAcceptsOnlyTheConfiguredExpectedFailure(t *testing.T) {
	t.Parallel()

	repository, _, base := stagedProtocolRepository(t)
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
	state, err := shell.ExecuteNextCommitStep(context.Background(), state, repository, "")
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
	base := parseGitHead(t, repository)
	runGitSetup(t, repository, "switch", "--detach", rawGitObject(base))
	return repository, branch, base
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
