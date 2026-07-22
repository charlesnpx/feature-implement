package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type goalInput struct {
	ID    string `json:"id"`
	Scope string `json:"scope"`
}

type evidenceItemInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type evidenceInput struct {
	Kind   string              `json:"kind"`
	Digest string              `json:"digest"`
	Items  []evidenceItemInput `json:"items"`
}

type reserveAttemptInput struct {
	SchemaVersion int       `json:"schema_version"`
	OccurredAt    string    `json:"occurred_at"`
	PlanID        string    `json:"plan_id"`
	MergeUnitID   string    `json:"merge_unit_id"`
	AttemptNumber uint64    `json:"attempt_number"`
	Base          string    `json:"base"`
	WorktreeRoot  string    `json:"worktree_root"`
	Goal          goalInput `json:"goal"`
}

type attemptIDInput struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	AttemptID     string `json:"attempt_id"`
}

type boundaryInput struct {
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	AttemptID     string          `json:"attempt_id"`
	Evidence      []evidenceInput `json:"evidence"`
}

type nextGoalInput struct {
	SchemaVersion int       `json:"schema_version"`
	OccurredAt    string    `json:"occurred_at"`
	AttemptID     string    `json:"attempt_id"`
	Goal          goalInput `json:"goal"`
}

type acknowledgeInput struct {
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	AttemptID     string          `json:"attempt_id"`
	Kind          string          `json:"kind"`
	Goal          goalInput       `json:"goal"`
	Receipt       json.RawMessage `json:"receipt"`
}

type ownerResponseInput struct {
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	AttemptID     string          `json:"attempt_id"`
	Response      string          `json:"response"`
	Receipt       json.RawMessage `json:"receipt"`
}

func executeAttempt(ctx context.Context, bundle workspace.WorkspaceBundle, options Options) (MutationResult, error) {
	journal, _, err := openWritableJournal(options)
	if err != nil {
		return MutationResult{}, err
	}
	defer journal.Close()
	definition := bundle.Definition()
	git := workspace.DefaultLocalAttemptGitAdapter()
	switch options.Subaction {
	case "reserve":
		var input reserveAttemptInput
		if err := decodeRequest(options.Input, &input); err != nil {
			return MutationResult{}, err
		}
		occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
		if err != nil {
			return MutationResult{}, err
		}
		planID, err := parseID(input.PlanID, "plan_id")
		if err != nil {
			return MutationResult{}, err
		}
		mergeUnitID, err := parseID(input.MergeUnitID, "merge_unit_id")
		if err != nil {
			return MutationResult{}, err
		}
		reference, err := workspace.NewMergeUnitReference(planID, mergeUnitID)
		if err != nil {
			return MutationResult{}, err
		}
		base, err := parseGitObject(input.Base, "base")
		if err != nil {
			return MutationResult{}, err
		}
		worktreeRoot, err := absoluteDirectory(input.WorktreeRoot, "worktree root")
		if err != nil {
			return MutationResult{}, err
		}
		goal, err := parseGoal(input.Goal)
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.ReserveAttempt(ctx, journal, definition, git, workspace.ReserveAttemptRequest{
			MergeUnit: reference, AttemptNumber: input.AttemptNumber, Base: base,
			WorktreeRoot: worktreeRoot, Goal: goal, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.reserve", journal, definition, nil)
	case "materialize":
		input, occurredAt, attemptID, err := decodeAttemptIDInput(options.Input)
		_ = input
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.MaterializeAttempt(ctx, journal, definition, git, workspace.MaterializeAttemptRequest{
			AttemptID: attemptID, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.materialize", journal, definition, nil)
	case "boundary":
		var input boundaryInput
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
		evidence, err := parseEvidence(input.Evidence)
		if err != nil {
			return MutationResult{}, err
		}
		result, err := workspace.RecordAttemptBoundary(ctx, journal, definition, git, workspace.RecordAttemptBoundaryRequest{
			AttemptID: attemptID, Evidence: evidence, OccurredAt: occurredAt,
		})
		if err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.boundary", journal, definition, directiveViews(result.Directives()))
	case "next-goal":
		var input nextGoalInput
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
		goal, err := parseGoal(input.Goal)
		if err != nil {
			return MutationResult{}, err
		}
		intent, err := workspace.ReserveNextGoalCreation(journal, definition, workspace.ReserveNextGoalCreationRequest{
			AttemptID: attemptID, Goal: goal, OccurredAt: occurredAt,
		})
		if err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.next-goal", journal, definition, []BoundaryDirectiveView{nextGoalDirectiveView(intent)})
	case "acknowledge":
		var input acknowledgeInput
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
		goal, err := parseGoal(input.Goal)
		if err != nil {
			return MutationResult{}, err
		}
		receipt, verifier, err := controlPlaneInputs(bundle, options.WorkspaceDir, input.Receipt)
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.RecordOrchestrationAcknowledgement(ctx, journal, definition, verifier, workspace.RecordOrchestrationAcknowledgementRequest{
			AttemptID: attemptID, Kind: workspace.OrchestrationAcknowledgementKind(input.Kind),
			Goal: goal, Receipt: receipt, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.acknowledge", journal, definition, nil)
	case "owner-response":
		var input ownerResponseInput
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
		receipt, verifier, err := controlPlaneInputs(bundle, options.WorkspaceDir, input.Receipt)
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.RecordOwnerBoundaryResponse(ctx, journal, definition, verifier, workspace.RecordOwnerBoundaryResponseRequest{
			AttemptID: attemptID, Response: workspace.OwnerBoundaryResponse(input.Response),
			Receipt: receipt, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.owner-response", journal, definition, nil)
	case "resume":
		_, occurredAt, attemptID, err := decodeAttemptIDInput(options.Input)
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.ResumeAttempt(ctx, journal, definition, git, workspace.ResumeAttemptRequest{
			AttemptID: attemptID, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.resume", journal, definition, nil)
	default:
		return MutationResult{}, fmt.Errorf("unsupported workspace attempt action %q", options.Subaction)
	}
}

func decodeAttemptIDInput(source []byte) (attemptIDInput, time.Time, workspace.ID, error) {
	var input attemptIDInput
	if err := decodeRequest(source, &input); err != nil {
		return input, time.Time{}, workspace.ID{}, err
	}
	occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
	if err != nil {
		return input, time.Time{}, workspace.ID{}, err
	}
	attemptID, err := parseID(input.AttemptID, "attempt_id")
	return input, occurredAt, attemptID, err
}

func parseGoal(input goalInput) (workspace.GoalBinding, error) {
	id, err := parseID(input.ID, "goal.id")
	if err != nil {
		return workspace.GoalBinding{}, err
	}
	return workspace.NewGoalBinding(id, workspace.GoalScope(input.Scope))
}

func parseEvidence(inputs []evidenceInput) ([]workspace.Evidence, error) {
	result := make([]workspace.Evidence, 0, len(inputs))
	for index, input := range inputs {
		kind, err := parseID(input.Kind, fmt.Sprintf("evidence[%d].kind", index))
		if err != nil {
			return nil, err
		}
		digest, err := parseDigest(input.Digest, fmt.Sprintf("evidence[%d].digest", index))
		if err != nil {
			return nil, err
		}
		items := make([]workspace.EvidenceItem, 0, len(input.Items))
		for itemIndex, item := range input.Items {
			name, err := parseID(item.Name, fmt.Sprintf("evidence[%d].items[%d].name", index, itemIndex))
			if err != nil {
				return nil, err
			}
			value, err := workspace.NewEvidenceItem(name, item.Value)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		evidence, err := workspace.NewEvidence(kind, digest, items)
		if err != nil {
			return nil, err
		}
		result = append(result, evidence)
	}
	return result, nil
}

func directiveViews(directives []workspace.AttemptBoundaryDirective) []BoundaryDirectiveView {
	result := make([]BoundaryDirectiveView, 0, len(directives))
	for _, directive := range directives {
		switch value := directive.(type) {
		case workspace.CompleteGoalAndWaitDirective:
			result = append(result, BoundaryDirectiveView{
				Kind: "complete_goal_and_wait", WorkspaceID: value.WorkspaceID().String(), Generation: value.Generation().String(),
				AttemptID: value.AttemptID().String(), BoundaryID: value.BoundaryID().String(), GoalID: value.Goal().ID().String(),
				GoalScope: string(value.Goal().Scope()), Head: value.Head().String(), DirectiveDigest: value.DirectiveDigest().String(),
				IdempotencyKey: value.IdempotencyKey().String(),
			})
		case workspace.OwnerGateDirective:
			choices := make([]string, 0, len(value.Choices()))
			for _, choice := range value.Choices() {
				choices = append(choices, string(choice))
			}
			result = append(result, BoundaryDirectiveView{
				Kind: "owner_gate", WorkspaceID: value.WorkspaceID().String(), Generation: value.Generation().String(),
				AttemptID: value.AttemptID().String(), BoundaryID: value.BoundaryID().String(), GoalID: value.Goal().ID().String(),
				GoalScope: string(value.Goal().Scope()), Head: value.Head().String(), DirectiveDigest: value.DirectiveDigest().String(), Choices: choices,
			})
		}
	}
	return result
}

func nextGoalDirectiveView(intent workspace.NextGoalCreationIntent) BoundaryDirectiveView {
	return BoundaryDirectiveView{
		Kind: "create_next_goal", WorkspaceID: intent.WorkspaceID().String(), Generation: intent.Generation().String(),
		AttemptID: intent.AttemptID().String(), BoundaryID: intent.BoundaryID().String(), GoalID: intent.NextGoal().ID().String(),
		GoalScope: string(intent.NextGoal().Scope()), Head: intent.Head().String(), DirectiveDigest: intent.DirectiveDigest().String(),
		IdempotencyKey: intent.IdempotencyKey().String(),
	}
}
