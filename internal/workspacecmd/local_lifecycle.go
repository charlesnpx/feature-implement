package workspacecmd

import (
	"context"
	"fmt"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type integrateMergeUnitInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	AttemptID     string `json:"attempt_id"`
}

type completeVerifyInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
}

func executeIntegration(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (MutationResult, error) {
	if options.Subaction != "merge-unit" {
		return MutationResult{}, fmt.Errorf(
			"unsupported workspace integrate action %q",
			options.Subaction,
		)
	}
	var input integrateMergeUnitInput
	if err := decodeRequest(options.Input, &input); err != nil {
		return MutationResult{}, err
	}
	occurredAt, err := parseOccurredAt(
		input.SchemaVersion, input.OccurredAt,
	)
	if err != nil {
		return MutationResult{}, err
	}
	attemptID, err := parseID(input.AttemptID, "attempt_id")
	if err != nil {
		return MutationResult{}, err
	}
	journal, _, err := openWritableJournal(options)
	if err != nil {
		return MutationResult{}, err
	}
	defer journal.Close()
	repository := localReviewRepository{
		git: workspace.DefaultLocalCommitGitAdapter(),
	}
	if _, err := workspace.IntegrateMergeUnit(
		ctx,
		journal,
		bundle.Definition(),
		repository,
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.IntegrateMergeUnitRequest{
			AttemptID:  attemptID,
			OccurredAt: occurredAt,
		},
	); err != nil {
		return MutationResult{}, err
	}
	return mutationResult(
		"integrate.merge-unit",
		journal,
		bundle.Definition(),
		nil,
	)
}

func executeCompletion(
	_ workspace.WorkspaceBundle,
	options Options,
) (any, error) {
	if options.Subaction != "verify" {
		return nil, fmt.Errorf(
			"unsupported workspace complete action %q",
			options.Subaction,
		)
	}
	var input completeVerifyInput
	if err := decodeRequest(options.Input, &input); err != nil {
		return nil, err
	}
	if _, err := parseOccurredAt(
		input.SchemaVersion, input.OccurredAt,
	); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf(
		"workspace complete verify is not implemented until local completion verification",
	)
}
