package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	"github.com/charlesnpx/feature-implement/internal/workspacecmd"
)

func TestWorkspaceInitializationRequiresExactLockCheckpoint(t *testing.T) {
	t.Parallel()

	t.Run("initial checkpoint", func(t *testing.T) {
		root := initializedPlanRepository(t)
		err := initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "requires a lock checkpoint") {
			t.Fatalf("initial checkpoint initialization error = %v", err)
		}
	})

	t.Run("revision checkpoint", func(t *testing.T) {
		requireFullSuite(t, "plan checkpoint admission permutation")

		root := initializedPlanRepository(t)
		replaceFileText(
			t,
			filepath.Join(root, "plans", "alpha.yaml"),
			"Establish the first contract.",
			"Define the first contract.",
		)
		if _, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(t, "2026-07-23T14:01:00Z", "rev-init", workspace.DigestBytes([]byte("review")).String()),
		}); err != nil {
			t.Fatal(err)
		}
		err := initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "requires a lock checkpoint") {
			t.Fatalf("revision checkpoint initialization error = %v", err)
		}
	})

	t.Run("exact lock checkpoint", func(t *testing.T) {
		root, lock := lockedPlanRepository(t)
		result, runtimeRoot, err := executeCheckpointBundleInitializationWithRuntime(t, root)
		if err != nil {
			t.Fatalf("initialize exact lock checkpoint: %v", err)
		}
		initialized, ok := result.(workspacecmd.InitializationResult)
		if !ok {
			t.Fatalf("initialization result type = %T", result)
		}
		if initialized.PlanCheckpoint != lock.Commit {
			t.Fatalf("initialized checkpoint = %s, want %s", initialized.PlanCheckpoint, lock.Commit)
		}
		snapshot, err := workspace.ReadWorkspaceJournalSnapshot(runtimeRoot)
		if err != nil {
			t.Fatalf("read initialized journal: %v", err)
		}
		if len(snapshot.Records()) != 3 {
			t.Fatalf("initialized journal records = %d", len(snapshot.Records()))
		}
		event, ok := snapshot.Records()[0].Event().(workspace.WorkspaceInitializedJournalEvent)
		if !ok || event.PlanCheckpoint().String() != lock.Commit {
			t.Fatalf("initialization event checkpoint = %#v, want %s", event, lock.Commit)
		}
		runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
		if err != nil {
			t.Fatalf("rebuild initialized runtime: %v", err)
		}
		if runtime.PlanCheckpoint().String() != lock.Commit {
			t.Fatalf("runtime checkpoint = %s, want %s", runtime.PlanCheckpoint(), lock.Commit)
		}
	})

	t.Run("dirty source", func(t *testing.T) {
		root, _ := lockedPlanRepository(t)
		replaceFileText(
			t,
			filepath.Join(root, "plans", "alpha.yaml"),
			"Establish the first contract.",
			"Dirty source contract.",
		)
		err := initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "generation") {
			t.Fatalf("dirty source initialization error = %v", err)
		}
	})

	t.Run("dirty index", func(t *testing.T) {
		requireFullSuite(t, "plan checkpoint admission permutation")

		root, _ := lockedPlanRepository(t)
		relative := "plans/alpha.yaml"
		planPath := filepath.Join(root, filepath.FromSlash(relative))
		clean, err := os.ReadFile(planPath)
		if err != nil {
			t.Fatal(err)
		}
		replaceFileText(t, planPath, "Establish the first contract.", "Staged contract.")
		runGitSetup(t, root, "add", "--", relative)
		if err := os.WriteFile(planPath, clean, 0o600); err != nil {
			t.Fatal(err)
		}
		err = initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "clean index") {
			t.Fatalf("dirty index initialization error = %v", err)
		}
	})

	t.Run("unowned path", func(t *testing.T) {
		requireFullSuite(t, "plan checkpoint admission permutation")

		root, _ := lockedPlanRepository(t)
		if err := os.WriteFile(filepath.Join(root, "unowned.txt"), []byte("unowned\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "unowned path") {
			t.Fatalf("unowned path initialization error = %v", err)
		}
	})

	t.Run("wrong generated lock", func(t *testing.T) {
		requireFullSuite(t, "plan checkpoint identity permutation")

		root, _ := lockedPlanRepository(t)
		lockPath := filepath.Join(root, "generated", workspace.WorkspaceLockFileName)
		content, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockPath, append(content, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		err = initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("wrong lock initialization error = %v", err)
		}
	})

	t.Run("wrong checkpoint tree", func(t *testing.T) {
		requireFullSuite(t, "plan checkpoint identity permutation")

		root, _ := lockedPlanRepository(t)
		forgeCheckpointTreeFromParent(t, root)
		err := initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "HEAD tree") {
			t.Fatalf("wrong checkpoint tree initialization error = %v", err)
		}
	})

	t.Run("invalid checkpoint parent", func(t *testing.T) {
		requireFullSuite(t, "plan checkpoint identity permutation")

		root, _ := lockedPlanRepository(t)
		forgeCheckpointParent(t, root)
		err := initializeCheckpointBundle(t, root)
		if err == nil || !strings.Contains(err.Error(), "plan checkpoint commit") {
			t.Fatalf("invalid checkpoint parent initialization error = %v", err)
		}
	})
}

func TestWorkspaceInitializationRejectsWrongCheckpointIdentities(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive plan checkpoint identity matrix")

	for _, test := range []struct {
		name    string
		trailer string
		want    string
	}{
		{name: "generation", trailer: "Generation", want: "generation"},
		{name: "source", trailer: "Source-Digest", want: "source digest"},
		{name: "lock", trailer: "Lock-Digest", want: "lock digest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := lockedPlanRepository(t)
			forgeCheckpointTrailer(t, root, test.trailer, workspace.DigestBytes([]byte("wrong "+test.name)).String())
			err := initializeCheckpointBundle(t, root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wrong %s initialization error = %v", test.name, err)
			}
		})
	}
}

func TestWorkspaceValidationPreservesFinalLockCheckpoint(t *testing.T) {
	t.Parallel()

	root, lock := lockedPlanRepository(t)
	result, err := workspacecmd.Execute(context.Background(), workspacecmd.Options{
		Action:           "validate",
		BundleDir:        root,
		WriteLocks:       true,
		GeneratorVersion: "binary-version-must-not-own-plan-locks",
	})
	if err != nil {
		t.Fatalf("validate final lock checkpoint: %v", err)
	}
	validation, ok := result.(workspacecmd.ValidationResult)
	if !ok || validation.Status != "valid" {
		t.Fatalf("validation result = %#v", result)
	}
	verified, err := workspace.VerifyPlanLockCheckpoint(context.Background(), mustLoadBundle(t, root))
	if err != nil {
		t.Fatalf("verify lock after validation: %v", err)
	}
	if verified.Commit().String() != lock.Commit {
		t.Fatalf("verified checkpoint after validation = %s, want %s", verified.Commit(), lock.Commit)
	}
	retried, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointLock,
			Input: checkpointInput(t, "2026-07-23T14:02:00Z", "", ""),
		},
	)
	if err != nil {
		t.Fatalf("retry final lock after validation: %v", err)
	}
	if !retried.Recovered || retried.Commit != lock.Commit {
		t.Fatalf("lock retry after validation = %#v, want commit %s", retried, lock.Commit)
	}
}

func lockedPlanRepository(t *testing.T) (string, workspace.PlanCheckpointResult) {
	t.Helper()
	root := initializedPlanRepository(t)
	lock, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointLock,
		Input: checkpointInput(t, "2026-07-23T14:02:00Z", "", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, lock
}

func initializeCheckpointBundle(t *testing.T, root string) error {
	t.Helper()
	_, err := executeCheckpointBundleInitialization(t, root)
	return err
}

func executeCheckpointBundleInitialization(t *testing.T, root string) (any, error) {
	t.Helper()
	result, _, err := executeCheckpointBundleInitializationWithRuntime(t, root)
	return result, err
}

func executeCheckpointBundleInitializationWithRuntime(
	t *testing.T,
	root string,
) (any, string, error) {
	t.Helper()
	runtimeRoot := filepath.Join(
		canonicalMaterializationTestTempDir(t), "runtime",
	)
	request, err := json.Marshal(map[string]any{
		"schema_version": 2,
		"occurred_at":    "2026-07-23T14:03:00Z",
		"worktree_root": workspaceTestWorktreeRoot(
			t, runtimeRoot,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspacecmd.Execute(context.Background(), workspacecmd.Options{
		Action: "init", BundleDir: root, WorkspaceDir: runtimeRoot, Input: request,
	})
	return result, runtimeRoot, err
}

func forgeCheckpointTrailer(t *testing.T, root, trailer, replacement string) {
	t.Helper()
	message := string(runGitSetup(t, root, "log", "-1", "--format=%B"))
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	found := false
	for index, line := range lines {
		prefix := trailer + ": "
		if strings.HasPrefix(line, prefix) {
			lines[index] = prefix + replacement
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("checkpoint message has no %s trailer:\n%s", trailer, message)
	}
	message = strings.Join(lines, "\n") + "\n"
	tree := strings.TrimSpace(string(runGitSetup(t, root, "show", "-s", "--format=%T", "HEAD")))
	replaceCheckpointCommit(t, root, tree, message)
}

func forgeCheckpointTreeFromParent(t *testing.T, root string) {
	t.Helper()
	message := strings.TrimRight(
		string(runGitSetup(t, root, "log", "-1", "--format=%B")),
		"\n",
	) + "\n"
	tree := strings.TrimSpace(string(runGitSetup(t, root, "show", "-s", "--format=%T", "HEAD^")))
	replaceCheckpointCommit(t, root, tree, message)
}

func forgeCheckpointParent(t *testing.T, root string) {
	t.Helper()
	message := strings.TrimRight(
		string(runGitSetup(t, root, "log", "-1", "--format=%B")),
		"\n",
	) + "\n"
	tree := strings.TrimSpace(string(runGitSetup(t, root, "show", "-s", "--format=%T", "HEAD")))
	parent := strings.TrimSpace(string(runGitSetup(
		t,
		root,
		"-c", "user.name=Unstructured",
		"-c", "user.email=unstructured@localhost",
		"commit-tree", tree,
		"-m", "not a plan checkpoint",
	)))
	replaceCheckpointCommitWithParent(t, root, tree, parent, message)
}

func replaceCheckpointCommit(t *testing.T, root, tree, message string) {
	t.Helper()
	parent := strings.TrimSpace(string(runGitSetup(t, root, "show", "-s", "--format=%P", "HEAD")))
	replaceCheckpointCommitWithParent(t, root, tree, parent, message)
}

func replaceCheckpointCommitWithParent(t *testing.T, root, tree, parent, message string) {
	t.Helper()
	old := strings.TrimSpace(string(runGitSetup(t, root, "rev-parse", "HEAD")))
	command := exec.Command("git", "-C", root, "commit-tree", tree, "-p", parent, "-F", "-")
	command.Stdin = bytes.NewBufferString(message)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Feature Implement",
		"GIT_AUTHOR_EMAIL=feature-implement@localhost",
		"GIT_AUTHOR_DATE=@1784815320 +0000",
		"GIT_COMMITTER_NAME=Feature Implement",
		"GIT_COMMITTER_EMAIL=feature-implement@localhost",
		"GIT_COMMITTER_DATE=@1784815320 +0000",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("forge checkpoint commit: %v\n%s", err, output)
	}
	replacementCommit := strings.TrimSpace(string(output))
	runGitSetup(t, root, "update-ref", "refs/heads/main", replacementCommit, old)
	runGitSetup(t, root, "read-tree", replacementCommit)
}
