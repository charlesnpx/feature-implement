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

type abandonInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	Reason        string `json:"reason"`
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
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (MutationResult, error) {
	if options.Subaction != "verify" {
		return MutationResult{}, fmt.Errorf(
			"unsupported workspace complete action %q",
			options.Subaction,
		)
	}
	var input completeVerifyInput
	if err := decodeRequest(options.Input, &input); err != nil {
		return MutationResult{}, err
	}
	occurredAt, err := parseOccurredAt(
		input.SchemaVersion, input.OccurredAt,
	)
	if err != nil {
		return MutationResult{}, err
	}
	journal, _, err := openWritableJournal(options)
	if err != nil {
		return MutationResult{}, err
	}
	defer journal.Close()
	if _, err := workspace.CompleteWorkspace(
		ctx,
		journal,
		bundle.Definition(),
		workspace.DefaultLocalIntegrationGitAdapter(),
		workspace.CompleteWorkspaceRequest{
			OccurredAt: occurredAt,
		},
	); err != nil {
		return MutationResult{}, err
	}
	return mutationResult(
		"complete.verify",
		journal,
		bundle.Definition(),
		nil,
	)
}

func executeAbandon(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (MutationResult, error) {
	if options.Subaction != "" {
		return MutationResult{}, fmt.Errorf(
			"workspace abandon does not accept a subaction",
		)
	}
	var input abandonInput
	if err := decodeRequest(options.Input, &input); err != nil {
		return MutationResult{}, err
	}
	occurredAt, err := parseOccurredAt(
		input.SchemaVersion, input.OccurredAt,
	)
	if err != nil {
		return MutationResult{}, err
	}
	journal, _, err := openWritableJournal(options)
	if err != nil {
		return MutationResult{}, err
	}
	defer journal.Close()
	if _, err := workspace.AbandonWorkspace(
		ctx,
		journal,
		bundle.Definition(),
		workspace.DefaultLocalTargetGitAdapter(),
		workspace.AbandonWorkspaceRequest{
			OccurredAt: occurredAt,
			Reason:     input.Reason,
		},
	); err != nil {
		return MutationResult{}, err
	}
	return mutationResult("abandon", journal, bundle.Definition(), nil)
}
