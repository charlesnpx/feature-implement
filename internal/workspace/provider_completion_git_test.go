package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestLocalProviderCompletionGitAdapterIndependentlyVerifiesRemoteAndMergeTopology(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	remote := filepath.Join(root, "remote.git")
	baseRef := "feature/base"
	branch := "mu/provider-git-a1-0123456789ab"
	runGitSetup(t, "", "init", "--bare", remote)
	runGitSetup(t, "", "init", "-b", baseRef, repository)
	runGitSetup(t, repository, "config", "user.name", "Provider Topology Test")
	runGitSetup(t, repository, "config", "user.email", "provider-topology@example.test")
	runGitSetup(t, repository, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(repository, "content.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "content.txt")
	runGitSetup(t, repository, "commit", "-m", "Base")
	base := providerGitRevision(t, repository, "HEAD")
	runGitSetup(t, repository, "switch", "-c", branch)
	if err := os.WriteFile(filepath.Join(repository, "content.txt"), []byte("reviewed head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "content.txt")
	runGitSetup(t, repository, "commit", "-m", "Reviewed head")
	head := providerGitRevision(t, repository, "HEAD")
	headTree := providerGitRevision(t, repository, "HEAD^{tree}")
	runGitSetup(t, repository, "switch", baseRef)
	runGitSetup(t, repository, "merge", "--no-ff", branch, "-m", "Merge reviewed head")
	merge := providerGitRevision(t, repository, "HEAD")
	runGitSetup(t, repository, "push", "origin", baseRef, branch)

	adapter := workspace.DefaultLocalProviderCompletionGitAdapter()
	remoteBranch, err := adapter.InspectRemoteBranch(context.Background(), repository, "origin", branch)
	if err != nil || remoteBranch != head {
		t.Fatalf("remote branch = %s err=%v, want %s", remoteBranch, err, head)
	}
	remoteBase, err := adapter.InspectRemoteBase(context.Background(), repository, "origin", baseRef)
	if err != nil || remoteBase != merge {
		t.Fatalf("remote base = %s err=%v, want %s", remoteBase, err, merge)
	}
	inspectedHead, err := adapter.InspectCommit(context.Background(), repository, head)
	if err != nil || inspectedHead.Commit() != head || inspectedHead.Tree() != headTree {
		t.Fatalf("head inspection = %#v err=%v", inspectedHead, err)
	}
	inspectedMerge, err := adapter.InspectCommit(context.Background(), repository, merge)
	parents := inspectedMerge.Parents()
	if err != nil || inspectedMerge.Tree() != headTree || len(parents) != 2 || parents[0] != base || parents[1] != head {
		t.Fatalf("merge inspection = %#v parents=%#v err=%v", inspectedMerge, parents, err)
	}
	for _, ancestor := range []workspace.GitObjectID{base, head} {
		isAncestor, err := adapter.IsAncestor(context.Background(), repository, ancestor, merge)
		if err != nil || !isAncestor {
			t.Fatalf("ancestor %s -> %s = %v err=%v", ancestor, merge, isAncestor, err)
		}
	}
	runGitSetup(t, repository, "remote", "set-url", "origin", "https://token:secret@example.invalid/repository.git")
	if _, err := adapter.InspectRemoteBranch(context.Background(), repository, "origin", branch); err == nil ||
		!strings.Contains(err.Error(), "embedded credentials") {
		t.Fatalf("local completion Git accepted credential-bearing remote URL: %v", err)
	}
}

func providerGitRevision(t *testing.T, repository, revision string) workspace.GitObjectID {
	t.Helper()
	raw := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "--verify", revision)))
	object, err := workspace.ParseGitObjectID("sha1:" + raw)
	if err != nil {
		t.Fatal(err)
	}
	return object
}
