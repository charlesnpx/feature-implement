package workspacecmd

import (
	"context"
	"fmt"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type commitNextInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	AttemptID     string `json:"attempt_id"`
	Body          string `json:"body,omitempty"`
}

type commitRebaseInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	AttemptID     string `json:"attempt_id"`
	NewBase       string `json:"new_base"`
	NewHead       string `json:"new_head"`
}

func executeCommit(ctx context.Context, bundle workspace.WorkspaceBundle, options Options) (MutationResult, error) {
	journal, _, err := openWritableJournal(options)
	if err != nil {
		return MutationResult{}, err
	}
	defer journal.Close()
	definition := bundle.Definition()
	shell, err := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), defaultIsolatedCheckRunner())
	if err != nil {
		return MutationResult{}, err
	}
	switch options.Subaction {
	case "next":
		var input commitNextInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return MutationResult{}, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return MutationResult{}, err
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.ExecuteAttemptCommitStep(ctx, journal, definition, shell, workspace.ExecuteAttemptCommitStepRequest{
			AttemptID: attemptID, Body: input.Body, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("commit.next", journal, definition, nil)
	case "rebase":
		var input commitRebaseInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return MutationResult{}, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return MutationResult{}, err
		}
		attemptID, err := parseID(input.AttemptID, "attempt_id")
		if err != nil {
			return MutationResult{}, err
		}
		base, err := parseGitObject(input.NewBase, "new_base")
		if err != nil {
			return MutationResult{}, err
		}
		head, err := parseGitObject(input.NewHead, "new_head")
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.RecordAttemptCommitRebase(ctx, journal, definition, shell, attemptID, base, head, occurredAt, nil); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("commit.rebase", journal, definition, nil)
	default:
		return MutationResult{}, fmt.Errorf("unsupported workspace commit action %q", options.Subaction)
	}
}
