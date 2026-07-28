package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func initializeWorkspaceV2(
	t *testing.T,
	workspaceDir string,
	definition workspace.EffectiveWorkspaceDefinition,
	occurredAt time.Time,
	planCheckpoint ...workspace.VerifiedPlanLockCheckpoint,
) (workspace.WorkspaceInitializationResult, error) {
	t.Helper()
	if len(planCheckpoint) > 1 {
		t.Fatalf(
			"test workspace initialization accepts one plan checkpoint",
		)
	}
	worktreeRoot := workspaceTestWorktreeRoot(t, workspaceDir)
	options := workspace.WorkspaceInitializationOptions{
		WorktreeRoot: worktreeRoot,
	}
	if len(planCheckpoint) == 1 {
		options.PlanCheckpoint = &planCheckpoint[0]
	}
	return workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		workspaceDir,
		definition,
		occurredAt,
		options,
	)
}

func workspaceTestWorktreeRoot(t *testing.T, workspaceDir string) string {
	t.Helper()
	worktreeRoot := workspaceDir + "-attempt-worktrees"
	if err := os.MkdirAll(worktreeRoot, 0o700); err != nil {
		t.Fatalf("create test worktree root: %v", err)
	}
	worktreeRoot, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		t.Fatalf("canonicalize test worktree root: %v", err)
	}
	return worktreeRoot
}
