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

func TestDetachedAttemptTreeMaterializationPreservesPrimaryAndExactTree(t *testing.T) {
	t.Parallel()

	repositoryRoot, base := newRawAttemptTreeRepository(t)
	payload := filepath.Join(repositoryRoot, "payload.txt")
	if err := os.WriteFile(payload, []byte("dirty primary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(repositoryRoot, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statusBefore := runGitSetup(t, repositoryRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	indexBefore := runGitSetup(t, repositoryRoot, "write-tree")
	payloadBefore, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}

	worktree := filepath.Join(t.TempDir(), "attempt")
	adapter := workspace.DefaultLocalAttemptGitAdapter()
	inspection, err := adapter.MaterializeAttemptTree(context.Background(), repositoryRoot, base, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Clean() || inspection.WorktreeHead() != base {
		t.Fatalf("detached attempt inspection = %#v", inspection)
	}
	if statusAfter := runGitSetup(t, repositoryRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all"); !bytes.Equal(statusBefore, statusAfter) {
		t.Fatalf("primary status changed: before %q after %q", statusBefore, statusAfter)
	}
	if indexAfter := runGitSetup(t, repositoryRoot, "write-tree"); !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("primary index changed: before %q after %q", indexBefore, indexAfter)
	}
	if payloadAfter, readErr := os.ReadFile(payload); readErr != nil || !bytes.Equal(payloadBefore, payloadAfter) {
		t.Fatalf("primary file changed: %q, %v", payloadAfter, readErr)
	}
	if content, readErr := os.ReadFile(untracked); readErr != nil || string(content) != "keep me\n" {
		t.Fatalf("untracked file changed: %q, %v", content, readErr)
	}
	if info, statErr := os.Stat(filepath.Join(worktree, "script.sh")); statErr != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable mode = %v, %v", info, statErr)
	}
	if target, readErr := os.Readlink(filepath.Join(worktree, "links", "payload")); readErr != nil || target != "../payload.txt" {
		t.Fatalf("symlink target = %q, %v", target, readErr)
	}
	registered := string(runGitSetup(t, repositoryRoot, "worktree", "list", "--porcelain"))
	if strings.Contains(registered, "worktree "+worktree) {
		t.Fatalf("detached attempt was registered: %s", registered)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, ".git")); statErr != nil {
		t.Fatalf("detached attempt Git directory = %v", statErr)
	}
	if _, inspectErr := adapter.InspectAttemptWorktree(context.Background(), repositoryRoot, worktree); inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
		t.Fatalf("reinspect detached attempt: %v", inspectErr)
	}
}

func TestDetachedAttemptTreeMaterializationCreatesEmptyGitlinkDirectory(t *testing.T) {
	t.Parallel()

	repositoryRoot, _ := newRawAttemptTreeRepository(t)
	gitlink := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD")))
	runGitSetup(
		t, repositoryRoot, "update-index", "--add", "--cacheinfo",
		"160000,"+gitlink+",modules/tool",
	)
	runGitSetup(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "add gitlink",
	)
	baseText := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD")))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "attempt")

	if _, err := workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, worktree,
	); err != nil {
		t.Fatal(err)
	}
	gitlinkDirectory := filepath.Join(worktree, "modules", "tool")
	info, err := os.Lstat(gitlinkDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("gitlink materialization mode = %s", info.Mode())
	}
	entries, err := os.ReadDir(gitlinkDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("gitlink materialization contains %d entries", len(entries))
	}
}

func TestDetachedAttemptTreeMaterializationUnderTemporaryAlias(t *testing.T) {
	t.Parallel()

	repositoryRoot, base := newRawAttemptTreeRepository(t)
	parent, err := os.MkdirTemp("/tmp", "feature-implement-attempt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(parent); err != nil {
			t.Error(err)
		}
	})

	inspection, err := workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, filepath.Join(parent, "attempt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Clean() || inspection.WorktreeHead() != base {
		t.Fatalf("detached attempt inspection = %#v", inspection)
	}
}

func TestDetachedAttemptTreeMaterializationRejectsUnsafeSymlink(t *testing.T) {
	t.Parallel()

	repositoryRoot := t.TempDir()
	runGitSetup(t, repositoryRoot, "init", "--initial-branch=main", ".")
	if err := os.Symlink("../../outside", filepath.Join(repositoryRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "--", "escape")
	runGitSetup(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "unsafe symlink",
	)
	baseText := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD")))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}

	_, err = workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, filepath.Join(t.TempDir(), "attempt"),
	)
	if err == nil || !strings.Contains(err.Error(), "escapes the repository root") {
		t.Fatalf("unsafe symlink materialization error = %v", err)
	}
}

func TestDetachedAttemptTreeMaterializationRejectsExternalHardLink(t *testing.T) {
	t.Parallel()

	repositoryRoot, base := newRawAttemptTreeRepository(t)
	worktree := filepath.Join(t.TempDir(), "attempt")
	outside := filepath.Join(t.TempDir(), "outside-link")
	linked := false
	adapter := workspace.DefaultLocalAttemptGitAdapter().
		WithAttemptWorktreeMaterializationFaultInjector(
			func(point workspace.AttemptWorktreeMaterializationFaultPoint) error {
				if point != workspace.AttemptMaterializationFaultAfterPath || linked {
					return nil
				}
				for _, relative := range []string{"payload.txt", "script.sh"} {
					err := os.Link(filepath.Join(worktree, relative), outside)
					if err == nil {
						linked = true
						return nil
					}
					if !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
				return nil
			},
		)

	_, err := adapter.MaterializeAttemptTree(context.Background(), repositoryRoot, base, worktree)
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("external hard-link materialization error = %v", err)
	}
	if !linked {
		t.Fatal("external hard link was not created during materialization")
	}
}

func TestDetachedAttemptTreeMaterializationRejectsMissingBlob(t *testing.T) {
	t.Parallel()

	repositoryRoot, base := newRawAttemptTreeRepository(t)
	blob := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD:payload.txt")))
	if len(blob) != 40 {
		t.Fatalf("payload blob = %q", blob)
	}
	if err := os.Remove(filepath.Join(repositoryRoot, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "attempt")

	_, err := workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, worktree,
	)
	if err == nil || !strings.Contains(err.Error(), "read Git blob") {
		t.Fatalf("missing blob materialization error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(worktree, "payload.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing blob path was published: %v", statErr)
	}
}

func TestDetachedAttemptTreeMaterializationStreamsLargeBlob(t *testing.T) {
	t.Parallel()

	repositoryRoot, _ := newRawAttemptTreeRepository(t)
	content := bytes.Repeat([]byte{0xa5}, 8*1024*1024+1)
	if err := os.WriteFile(filepath.Join(repositoryRoot, "large.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repositoryRoot, "add", "--", "large.bin")
	runGitSetup(
		t, repositoryRoot,
		"-c", "user.name=Attempt Test",
		"-c", "user.email=attempt@example.invalid",
		"commit", "-m", "add large blob",
	)
	baseText := strings.TrimSpace(string(runGitSetup(t, repositoryRoot, "rev-parse", "HEAD")))
	base, err := workspace.ParseGitObjectID("sha1:" + baseText)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "attempt")

	if _, err := workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, worktree,
	); err != nil {
		t.Fatal(err)
	}
	materialized, err := os.ReadFile(filepath.Join(worktree, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(materialized, content) {
		t.Fatalf("large blob differs: got %d bytes, want %d", len(materialized), len(content))
	}
}
