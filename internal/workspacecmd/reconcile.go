package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type reconciliationStageInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
}

type reconciliationPlanInput struct {
	SchemaVersion int `json:"schema_version"`
}

type reconciliationActivateInput struct {
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	Token         json.RawMessage `json:"token"`
	Receipt       json.RawMessage `json:"receipt"`
}

type ReconciliationPlanResult struct {
	SchemaVersion       int             `json:"schema_version"`
	Status              string          `json:"status"`
	WorkspaceID         string          `json:"workspace_id"`
	ActiveGeneration    string          `json:"active_generation"`
	CandidateGeneration string          `json:"candidate_generation"`
	JournalHead         string          `json:"journal_head"`
	StateDigest         string          `json:"state_digest"`
	StructuralDigest    string          `json:"structural_digest"`
	ComparisonDigest    string          `json:"comparison_digest"`
	ChangedMergeUnits   []string        `json:"changed_merge_units"`
	Token               json.RawMessage `json:"token"`
}

func executeReconciliation(ctx context.Context, activeBundle workspace.WorkspaceBundle, options Options) (any, error) {
	if options.CandidateBundleDir == "" {
		return nil, fmt.Errorf("workspace reconcile requires --candidate-bundle <dir>")
	}
	candidateBundle, err := workspace.LoadWorkspaceBundle(options.CandidateBundleDir)
	if err != nil {
		return nil, err
	}
	journal, directory, err := openWritableJournal(options)
	if err != nil {
		return nil, err
	}
	defer journal.Close()
	store, err := workspace.OpenGenerationStore(directory)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	active := activeBundle.Definition()
	candidate := candidateBundle.Definition()
	switch options.Subaction {
	case "stage":
		var input reconciliationStageInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		if _, err := store.StageCandidate(journal, candidate, occurredAt); err != nil {
			return nil, err
		}
		return mutationResult("reconcile.stage", journal, active, nil)
	case "plan":
		var input reconciliationPlanInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		if input.SchemaVersion != requestSchemaVersion {
			return nil, fmt.Errorf("workspace command schema_version must be %d", requestSchemaVersion)
		}
		snapshot, err := journal.ReadSnapshot()
		if err != nil {
			return nil, err
		}
		state, err := workspace.RebuildReconciliationState(snapshot, active)
		if err != nil {
			return nil, err
		}
		plan, err := workspace.DryRunReconciliation(active, candidate, snapshot, state)
		if err != nil {
			return nil, err
		}
		token, err := plan.TokenBytes()
		if err != nil {
			return nil, err
		}
		changed := make([]string, 0, len(plan.ChangedMergeUnits()))
		for _, unit := range plan.ChangedMergeUnits() {
			changed = append(changed, unit.String())
		}
		return ReconciliationPlanResult{
			SchemaVersion: requestSchemaVersion, Status: "planned", WorkspaceID: plan.WorkspaceID().String(),
			ActiveGeneration: plan.ActiveGeneration().String(), CandidateGeneration: plan.CandidateGeneration().String(),
			JournalHead: plan.JournalHead().String(), StateDigest: plan.StateDigest().String(), StructuralDigest: plan.StructuralDigest().String(),
			ComparisonDigest: plan.ComparisonDigest().String(), ChangedMergeUnits: changed, Token: json.RawMessage(token),
		}, nil
	case "activate":
		var input reconciliationActivateInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return nil, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return nil, err
		}
		plan, err := workspace.ParseReconciliationPlanToken(input.Token)
		if err != nil {
			return nil, err
		}
		snapshot, err := journal.ReadSnapshot()
		if err != nil {
			return nil, err
		}
		state, err := workspace.RebuildReconciliationState(snapshot, active)
		if err != nil {
			return nil, err
		}
		receipt, verifier, err := controlPlaneInputs(activeBundle, options.WorkspaceDir, input.Receipt)
		if err != nil {
			return nil, err
		}
		if _, err := workspace.ActivateCandidateGeneration(
			ctx, journal, store, active, candidate, plan, state, receipt, verifier, occurredAt,
		); err != nil {
			return nil, err
		}
		return mutationResult("reconcile.activate", journal, candidate, nil)
	default:
		return nil, fmt.Errorf("unsupported workspace reconcile action %q", options.Subaction)
	}
}
