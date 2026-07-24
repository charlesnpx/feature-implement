package workspacecmd

import (
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
	_ workspace.WorkspaceBundle,
	options Options,
) (any, error) {
	if options.Subaction != "merge-unit" {
		return nil, fmt.Errorf(
			"unsupported workspace integrate action %q",
			options.Subaction,
		)
	}
	var input integrateMergeUnitInput
	if err := decodeRequest(options.Input, &input); err != nil {
		return nil, err
	}
	if _, err := parseOccurredAt(
		input.SchemaVersion, input.OccurredAt,
	); err != nil {
		return nil, err
	}
	if _, err := parseID(input.AttemptID, "attempt_id"); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf(
		"workspace integrate merge-unit is not implemented until local CAS integration",
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
