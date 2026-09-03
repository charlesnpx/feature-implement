package workspace_test

import (
	"context"
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
	options := workspace.WorkspaceInitializationOptions{}
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
