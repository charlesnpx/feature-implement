package workspacecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestReviewRequestArtifactsUseSafeExternalDirectoryAnd0600Files(t *testing.T) {
	repository := canonicalWorkspaceCommandTempDir(t)
	worktree := canonicalWorkspaceCommandTempDir(t)
	output := canonicalWorkspaceCommandTempDir(t)
	charter := []byte(`{"schema_version":"review-charter-v2"}`)
	request := []byte(`{"schema_version":"review-request-v1"}`)
	patch := []byte("diff --git a/a b/a\n")
	paths, err := writeReviewRequestArtifacts(output, repository, worktree, charter, request, patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		path string
		want []byte
	}{
		{paths.charter, charter},
		{paths.request, request},
		{paths.reviewInput, patch},
	} {
		content, readErr := os.ReadFile(file.path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(content, file.want) {
			t.Fatalf("artifact %s = %q, want %q", file.path, content, file.want)
		}
		info, statErr := os.Stat(file.path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact %s permissions = %04o", file.path, info.Mode().Perm())
		}
	}
	if _, err := writeReviewRequestArtifacts(output, repository, worktree, charter, request, patch); err == nil ||
		!strings.Contains(err.Error(), "refuse to overwrite") {
		t.Fatalf("existing artifact rewrite error = %v", err)
	}
	insideRepository := filepath.Join(repository, "review-output")
	if err := os.Mkdir(insideRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := writeReviewRequestArtifacts(insideRepository, repository, worktree, charter, request, patch); err == nil ||
		!strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("repository-contained output error = %v", err)
	}
}

func TestLocalReviewRepositoryAdoptsActualCleanDescendantHead(t *testing.T) {
	repository := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.name", "Feature Test")
	runGitTest(t, repository, "config", "user.email", "feature@example.test")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Base")
	base := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))

	if err := os.WriteFile(tracked, []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "tracked.txt")
	runGitTest(t, repository, "commit", "-m", "Implementation")
	head := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))
	tree := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD^{tree}")))
	request, err := workspace.NewReviewRepositoryRequest(repository, base)
	if err != nil {
		t.Fatal(err)
	}
	adapter := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
	if _, err := adapter.InspectReviewSnapshot(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "attempt worktree must keep HEAD detached") {
		t.Fatalf("branch-attached attempt inspection error = %v", err)
	}
	runGitTest(t, repository, "switch", "--detach", gitObjectHex(head))
	snapshot, err := adapter.InspectReviewSnapshot(context.Background(), request)
	if err != nil || !snapshot.Clean() || snapshot.Head() != head || snapshot.Tree() != tree {
		t.Fatalf("actual review snapshot = %#v error=%v", snapshot, err)
	}

	runGitTest(t, repository, "reset", "--hard", gitObjectHex(base))
	staleRequest, err := workspace.NewReviewRepositoryRequest(repository, head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.InspectReviewSnapshot(context.Background(), staleRequest); err == nil ||
		!strings.Contains(err.Error(), "descend from durable head") {
		t.Fatalf("rewound ordinary head error = %v", err)
	}
}
