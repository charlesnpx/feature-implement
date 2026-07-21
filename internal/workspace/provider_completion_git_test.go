package workspace_test

import (
	"context"
	"os"
	"os/exec"
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
	runGitSetup(t, repository, "push", "origin", baseRef, branch)
	merger := filepath.Join(root, "merger")
	runGitSetup(t, "", "clone", remote, merger)
	runGitSetup(t, merger, "config", "user.name", "Remote Merge Test")
	runGitSetup(t, merger, "config", "user.email", "remote-merge@example.test")
	runGitSetup(t, merger, "switch", "-c", baseRef, "--track", "origin/"+baseRef)
	runGitSetup(t, merger, "merge", "--no-ff", "origin/"+branch, "-m", "Merge reviewed head")
	merge := providerGitRevision(t, merger, "HEAD")
	runGitSetup(t, merger, "push", "origin", baseRef)
	if providerGitObjectExists(t, repository, merge) {
		t.Fatal("verifier repository unexpectedly contains the remotely created merge")
	}
	refsBefore := string(runGitSetup(t, repository, "show-ref"))

	adapter := workspace.DefaultLocalProviderCompletionGitAdapter()
	inspection, err := adapter.InspectRemoteTopology(
		context.Background(), repository, "origin", branch, baseRef, head, merge, base,
	)
	if err != nil || inspection.RemoteBranchHead() != head || inspection.FinalBaseHead() != merge {
		t.Fatalf("remote topology = %#v err=%v", inspection, err)
	}
	inspectedHead := inspection.HeadCommit()
	if inspectedHead.Commit() != head || inspectedHead.Tree() != headTree {
		t.Fatalf("head inspection = %#v", inspectedHead)
	}
	inspectedMerge := inspection.MergeCommit()
	parents := inspectedMerge.Parents()
	if inspectedMerge.Tree() != headTree || len(parents) != 2 || parents[0] != base || parents[1] != head {
		t.Fatalf("merge inspection = %#v parents=%#v", inspectedMerge, parents)
	}
	if !inspection.BaseAncestor() || !inspection.HeadAncestor() {
		t.Fatalf("isolated ancestry = base:%v head:%v", inspection.BaseAncestor(), inspection.HeadAncestor())
	}
	if refsAfter := string(runGitSetup(t, repository, "show-ref")); refsAfter != refsBefore {
		t.Fatalf("isolated inspection moved verifier refs\nbefore:\n%s\nafter:\n%s", refsBefore, refsAfter)
	}
	if providerGitObjectExists(t, repository, merge) {
		t.Fatal("isolated inspection leaked the merge object into the verifier repository")
	}
	runGitSetup(t, repository, "remote", "set-url", "origin", "https://token:secret@example.invalid/repository.git")
	if _, err := adapter.InspectRemoteTopology(
		context.Background(), repository, "origin", branch, baseRef, head, merge, base,
	); err == nil ||
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

func providerGitObjectExists(t *testing.T, repository string, object workspace.GitObjectID) bool {
	t.Helper()
	command := exec.Command(
		"git", "-C", repository, "cat-file", "-e",
		strings.TrimPrefix(object.String(), string(object.Algorithm())+":")+"^{commit}",
	)
	if err := command.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false
		}
		t.Fatal(err)
	}
	return true
}
