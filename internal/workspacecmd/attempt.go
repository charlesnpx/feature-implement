package workspacecmd

import (
	"context"
	"fmt"
	"strings"
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
	Kind          string          `json:"kind"`
	Evidence      []evidenceInput `json:"evidence"`
}

type localReviewRepository struct {
	git workspace.LocalCommitGitAdapter
}

func (adapter localReviewRepository) InspectReviewSnapshot(
	ctx context.Context,
	request workspace.ReviewRepositoryRequest,
) (workspace.ReviewRepositorySnapshot, error) {
	inspection, err := adapter.git.InspectCleanWorktreeHead(
		ctx, request.Worktree(), request.Head(),
	)
	if err != nil {
		return workspace.ReviewRepositorySnapshot{}, err
	}
	return workspace.NewReviewRepositorySnapshot(inspection.Commit(), inspection.Tree(), true)
}

func (adapter localReviewRepository) ReadReviewInput(
	ctx context.Context,
	worktree string,
	base, head workspace.GitObjectID,
) ([]byte, error) {
	return adapter.git.ReadReviewInput(ctx, worktree, base, head)
}

func (adapter localReviewRepository) VerifyFinalHistory(
	ctx context.Context,
	protocol workspace.CommitProtocol,
	worktree string,
	base, head workspace.GitObjectID,
) error {
	verifier, err := workspace.NewFinalHistoryVerifier(
		adapter.git,
		defaultIsolatedCheckRunner(),
	)
	if err != nil {
		return err
	}
	return verifier.Verify(ctx, protocol, worktree, base, head)
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
	case "start":
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
		goal, err := parseGoal(input.Goal)
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.StartAttempt(ctx, journal, definition, git, workspace.StartAttemptRequest{
			MergeUnit: reference, AttemptNumber: input.AttemptNumber,
			Goal: goal, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.start", journal, definition)
	case "adopt-head":
		_, occurredAt, attemptID, err := decodeAttemptIDInput(options.Input)
		if err != nil {
			return MutationResult{}, err
		}
		repository := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
		if _, err := workspace.AdoptAttemptHead(ctx, journal, definition, repository, workspace.AdoptAttemptHeadRequest{
			AttemptID: attemptID, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.adopt-head", journal, definition)
	case "pause":
		input, kind, err := decodeBoundaryInput(options.Input)
		if err != nil {
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
		if _, err := workspace.PauseAttempt(ctx, journal, definition, git, workspace.PauseAttemptRequest{
			AttemptID: attemptID, Kind: kind, Evidence: evidence, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.pause", journal, definition)
	case "abandon":
		_, occurredAt, attemptID, err := decodeAttemptIDInput(options.Input)
		if err != nil {
			return MutationResult{}, err
		}
		if _, err := workspace.AbandonAttempt(journal, definition, workspace.AbandonAttemptRequest{
			AttemptID: attemptID, OccurredAt: occurredAt,
		}); err != nil {
			return MutationResult{}, err
		}
		return mutationResult("attempt.abandon", journal, definition)
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
		return mutationResult("attempt.resume", journal, definition)
	default:
		return MutationResult{}, fmt.Errorf("unsupported workspace attempt action %q", options.Subaction)
	}
}

func decodeBoundaryInput(source []byte) (boundaryInput, workspace.AttemptBoundaryKind, error) {
	var input boundaryInput
	if err := decodeRequest(source, &input); err != nil {
		if strings.Contains(err.Error(), "required JSON field $.kind is missing") {
			return input, "", requiredAttemptPauseKindError()
		}
		return input, "", err
	}
	kind, err := parseAttemptBoundaryKind(input.Kind)
	return input, kind, err
}

func parseAttemptBoundaryKind(value string) (workspace.AttemptBoundaryKind, error) {
	kind := workspace.AttemptBoundaryKind(value)
	switch kind {
	case workspace.AttemptBoundaryKindCheckpoint, workspace.AttemptBoundaryKindEscalation:
		return kind, nil
	default:
		return "", invalidAttemptPauseKindError()
	}
}

func invalidAttemptPauseKindError() error {
	return fmt.Errorf(
		"attempt pause kind must be %q or %q",
		workspace.AttemptBoundaryKindCheckpoint,
		workspace.AttemptBoundaryKindEscalation,
	)
}

func requiredAttemptPauseKindError() error {
	return fmt.Errorf(
		"attempt pause kind is required; accepted values are %q or %q",
		workspace.AttemptBoundaryKindCheckpoint,
		workspace.AttemptBoundaryKindEscalation,
	)
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
