package workspacecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestLocalReviewRepositoryAdoptsActualCleanDescendantHead(t *testing.T) {
	t.Parallel()

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

func TestReviewDispatchExposesFrozenConfigurationWithoutAdapterSpecificPacket(t *testing.T) {
	t.Parallel()

	fixture := newAttemptBoundaryCommandFixture(t, true)
	dispatchResult, err := Execute(context.Background(), Options{
		Action:       "review",
		Subaction:    "dispatch",
		BundleDir:    fixture.bundleRoot,
		WorkspaceDir: fixture.workspaceDir,
		Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-09-03T12:00:01Z",
  "attempt_id": "` + fixture.attemptID.String() + `"
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatched, ok := dispatchResult.(ReviewCommandResult)
	if !ok || dispatched.Action != "review.dispatch" {
		t.Fatalf("dispatch command result = %#v", dispatchResult)
	}
	dispatch, ok := dispatched.Detail.(ReviewGateDispatchView)
	if dispatch.ReviewConfigurationSource != workspace.BundledDefaultReviewConfiguration ||
		dispatch.ReviewConfigurationDigest == "" || dispatch.FrozenCopy == "" {
		t.Fatalf("generic dispatch detail = %#v", dispatch)
	}
}
