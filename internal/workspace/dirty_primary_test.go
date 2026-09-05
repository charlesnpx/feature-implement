package workspace_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestInitializationAndIntegrationLeaveDirtyPrimaryCheckoutUntouched(t *testing.T) {
	t.Parallel()

	t.Run("initialization", func(t *testing.T) {
		t.Parallel()

		definition := mustDefinition(t, newDefinitionFixture(t).sources)
		primary := definition.Workspace().RepositoryRoot()
		before := dirtyPrimaryCheckout(t, primary, "initialization")

		if _, err := workspace.InitializeWorkspaceV2WithOptions(
			context.Background(),
			canonicalTestDirectory(t),
			definition,
			mustTime(t, "2026-09-03T12:03:00Z"),
			workspace.WorkspaceInitializationOptions{},
		); err != nil {
			t.Fatalf("initialize dirty primary checkout: %v", err)
		}
		before.assertUnchanged(t, primary)
	})

	t.Run("first integration", func(t *testing.T) {
		t.Parallel()

		scenario := newRealIntegrationScenario(
			t, workspace.GitHashSHA1, true, workspace.GitObjectID{},
		)
		before := dirtyPrimaryCheckout(
			t, scenario.repositoryRoot, "integration",
		)

		if _, err := workspace.IntegrateMergeUnit(
			context.Background(),
			scenario.journal,
			scenario.definition,
			scenario.repository,
			workspace.DefaultLocalIntegrationGitAdapter(),
			workspace.IntegrateMergeUnitRequest{
				AttemptID:  scenario.attempt.AttemptID(),
				OccurredAt: mustTime(t, "2026-09-03T12:03:01Z"),
			},
		); err != nil {
			t.Fatalf("integrate dirty primary checkout: %v", err)
		}
		before.assertUnchanged(t, scenario.repositoryRoot)
	})
}

type dirtyPrimaryCheckoutSnapshot struct {
	trackedPath   string
	untrackedPath string
	index         []byte
	tracked       []byte
	untracked     []byte
	status        []byte
}

func dirtyPrimaryCheckout(
	t *testing.T,
	primary, suffix string,
) dirtyPrimaryCheckoutSnapshot {
	t.Helper()

	trackedPath := filepath.Join(primary, "dirty-primary-"+suffix+".txt")
	if err := os.WriteFile(trackedPath, []byte("staged primary change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, primary, "add", "--", filepath.Base(trackedPath))
	if err := os.WriteFile(trackedPath, []byte("unstaged primary change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untrackedPath := filepath.Join(primary, "operator-note-"+suffix+".txt")
	if err := os.WriteFile(untrackedPath, []byte("leave this alone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := []byte(runTargetGitTest(
		t, primary, "status", "--porcelain=v2", "-z", "--untracked-files=all",
	))
	if len(status) == 0 {
		t.Fatal("primary checkout was not made dirty")
	}
	gitDir := strings.TrimSpace(runTargetGitTest(
		t, primary, "rev-parse", "--absolute-git-dir",
	))
	index, err := os.ReadFile(filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := os.ReadFile(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	untracked, err := os.ReadFile(untrackedPath)
	if err != nil {
		t.Fatal(err)
	}
	return dirtyPrimaryCheckoutSnapshot{
		trackedPath: trackedPath, untrackedPath: untrackedPath,
		index: index, tracked: tracked, untracked: untracked, status: status,
	}
}

func (before dirtyPrimaryCheckoutSnapshot) assertUnchanged(
	t *testing.T,
	primary string,
) {
	t.Helper()

	gitDir := strings.TrimSpace(runTargetGitTest(
		t, primary, "rev-parse", "--absolute-git-dir",
	))
	index, err := os.ReadFile(filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := os.ReadFile(before.trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	untracked, err := os.ReadFile(before.untrackedPath)
	if err != nil {
		t.Fatal(err)
	}
	status := []byte(runTargetGitTest(
		t, primary, "status", "--porcelain=v2", "-z", "--untracked-files=all",
	))
	if !bytes.Equal(before.index, index) ||
		!bytes.Equal(before.tracked, tracked) ||
		!bytes.Equal(before.untracked, untracked) ||
		!bytes.Equal(before.status, status) {
		t.Fatal("operation changed the dirty primary checkout")
	}
}
