package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileExclusiveWithPreservesReplacementAfterPopulationFailure(
	t *testing.T,
) {
	rootPath, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := OpenRootedFilesystemAdapter(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	crash := errors.New("simulated population failure")
	target := filepath.Join(rootPath, "published.txt")
	original := filepath.Join(rootPath, "original.txt")
	_, err = adapter.writeFileExclusiveWith(
		"published.txt", 0o600,
		func(file *os.File) error {
			if _, err := file.Write([]byte("original\n")); err != nil {
				return err
			}
			if err := os.Rename(target, original); err != nil {
				return err
			}
			if err := os.WriteFile(
				target, []byte("replacement\n"), 0o600,
			); err != nil {
				return err
			}
			return crash
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("population failure = %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "replacement\n" {
		t.Fatalf("replacement file changed or was removed: %q, %v", content, err)
	}
	content, err = os.ReadFile(original)
	if err != nil || string(content) != "original\n" {
		t.Fatalf("created file identity changed unexpectedly: %q, %v", content, err)
	}
}

func TestDetachedAttemptTreeMaterializationRollsBackFileWhenParentSyncFails(
	t *testing.T,
) {
	repositoryRoot := t.TempDir()
	runRootedFilesystemTestGit(t, repositoryRoot, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "payload.txt"), []byte("payload\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	runRootedFilesystemTestGit(t, repositoryRoot, "add", "--", "payload.txt")
	runRootedFilesystemTestGit(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "payload",
	)
	algorithm := strings.TrimSpace(string(runRootedFilesystemTestGit(
		t, repositoryRoot, "rev-parse", "--show-object-format",
	)))
	head := strings.TrimSpace(string(runRootedFilesystemTestGit(
		t, repositoryRoot, "rev-parse", "HEAD",
	)))
	base, err := ParseGitObjectID(algorithm + ":" + head)
	if err != nil {
		t.Fatal(err)
	}

	syncFailure := errors.New("injected attempt-parent synchronization failure")
	originalSyncRootHandle := syncRootHandle
	failed := false
	syncRootHandle = func(directory *os.Root) error {
		if !failed {
			if _, payloadErr := directory.Lstat("payload.txt"); payloadErr == nil {
				if _, gitErr := directory.Lstat(".git"); errors.Is(gitErr, os.ErrNotExist) {
					failed = true
					return syncFailure
				}
			}
		}
		return originalSyncRootHandle(directory)
	}
	t.Cleanup(func() { syncRootHandle = originalSyncRootHandle })

	worktree := filepath.Join(t.TempDir(), "attempt")
	_, err = DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, worktree,
	)
	if !errors.Is(err, syncFailure) {
		t.Fatalf("parent synchronization failure = %v", err)
	}
	if !failed {
		t.Fatal("parent synchronization was not injected")
	}
	if _, statErr := os.Lstat(worktree); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed file materialization left its destination behind: %v", statErr)
	}
	inspection, retryErr := DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, worktree,
	)
	if retryErr != nil {
		t.Fatalf("retry after parent synchronization failure = %v", retryErr)
	}
	if !inspection.Clean() || inspection.WorktreeHead() != base {
		t.Fatalf("retry inspection = %#v", inspection)
	}
}

func runRootedFilesystemTestGit(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return output
}
