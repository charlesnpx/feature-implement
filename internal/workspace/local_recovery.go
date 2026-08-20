package workspace

import (
	"context"
	"fmt"
	"time"
)

type LocalRecoveryAction string

const (
	LocalRecoveryJournalTail          LocalRecoveryAction = "journal_tail"
	LocalRecoveryFeatureRef           LocalRecoveryAction = "feature_ref"
	LocalRecoveryAttemptMaterialized  LocalRecoveryAction = "attempt_materialized"
	LocalRecoveryIntegrationCompleted LocalRecoveryAction = "integration_completed"
	LocalRecoveryWorkspaceCompleted   LocalRecoveryAction = "workspace_completed"
)

type RecoverWorkspaceLocalEffectsRequest struct {
	OccurredAt       time.Time
	TargetFault      LocalTargetInitializationFaultInjector
	AttemptFault     AttemptLifecycleFaultInjector
	IntegrationFault IntegrationLifecycleFaultInjector
	CompletionFault  CompletionLifecycleFaultInjector
}

type WorkspaceLocalRecoveryResult struct {
	actions []LocalRecoveryAction
}

func (result WorkspaceLocalRecoveryResult) Actions() []LocalRecoveryAction {
	return append([]LocalRecoveryAction(nil), result.actions...)
}

func (result WorkspaceLocalRecoveryResult) Recovered() bool {
	return len(result.actions) != 0
}

func RecoverWorkspaceLocalEffects(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	targetGit LocalTargetGitAdapter,
	attemptGit AttemptGitPort,
	repository ReviewRepositoryPort,
	integrationGit IntegrationGitPort,
	request RecoverWorkspaceLocalEffectsRequest,
) (WorkspaceLocalRecoveryResult, error) {
	if ctx == nil || journal == nil || attemptGit == nil ||
		repository == nil || integrationGit == nil ||
		request.OccurredAt.IsZero() {
		return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
			"local workspace recovery requires context, journal, Git adapters, repository inspection, and occurrence time",
		)
	}
	result := WorkspaceLocalRecoveryResult{
		actions: []LocalRecoveryAction{},
	}
	tail, err := journal.RecoverIncompleteTail(
		definition.workspace.id, request.OccurredAt,
	)
	if err != nil {
		return WorkspaceLocalRecoveryResult{}, err
	}
	if tail.Recovered() {
		result.actions = append(
			result.actions, LocalRecoveryJournalTail,
		)
	}

	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return WorkspaceLocalRecoveryResult{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return WorkspaceLocalRecoveryResult{}, err
	}
	if runtime.workspaceID != definition.workspace.id ||
		runtime.activeGeneration != definition.generation {
		return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
			"local recovery definition does not match the active workspace generation",
		)
	}

	if _, completed := runtime.Completion(); !completed {
		target, exists := runtime.LocalTarget()
		if !exists || !target.Created() {
			if _, err := initializeLocalTarget(
				ctx, journal, snapshot, definition,
				request.OccurredAt, targetGit,
				request.TargetFault,
			); err != nil {
				return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
					"recover pending feature-ref creation: %w", err,
				)
			}
			result.actions = append(
				result.actions, LocalRecoveryFeatureRef,
			)
		}

		snapshot, runtime, err = readAttemptRuntime(
			journal, definition,
		)
		if err != nil {
			return WorkspaceLocalRecoveryResult{}, err
		}
		pending := make([]RuntimeAttemptProjection, 0)
		for _, attempt := range runtime.attempts {
			if attempt.phase != AttemptActive {
				continue
			}
			inspection, inspectErr := attemptGit.InspectAttemptWorktree(
				ctx, definition.workspace.target.root, attempt.worktree,
			)
			if inspectErr != nil {
				return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
					"inspect started scratch attempt %s: %w", attempt.attemptID, inspectErr,
				)
			}
			if !inspection.WorktreeExists() {
				pending = append(pending, attempt)
				continue
			}
			if inspection.WorktreeHead().IsZero() {
				return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
					"started scratch attempt %s has an incomplete worktree; rerun attempt start to inspect or repair it",
					attempt.attemptID,
				)
			}
		}
		for _, attempt := range pending {
			if _, err := reconcileStartedAttempt(
				ctx, journal, definition, attemptGit, attempt, request.AttemptFault,
			); err != nil {
				return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
					"recover attempt materialization %s: %w",
					attempt.attemptID, err,
				)
			}
			result.actions = append(
				result.actions,
				LocalRecoveryAttemptMaterialized,
			)
		}

		snapshot, reviews, runtime, err :=
			readIntegrationRuntime(journal, definition)
		if err != nil {
			return WorkspaceLocalRecoveryResult{}, err
		}
		pendingIntegrations := make([]ID, 0)
		for _, attempt := range runtime.attempts {
			if attempt.integration != nil &&
				!attempt.integration.Integrated() {
				pendingIntegrations = append(
					pendingIntegrations, attempt.attemptID,
				)
			}
		}
		for _, attemptID := range pendingIntegrations {
			if _, err := IntegrateMergeUnit(
				ctx, journal, definition,
				repository, integrationGit,
				IntegrateMergeUnitRequest{
					AttemptID:  attemptID,
					OccurredAt: request.OccurredAt,
					Fault:      request.IntegrationFault,
				},
			); err != nil {
				return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
					"recover merge-unit integration %s: %w",
					attemptID, err,
				)
			}
			result.actions = append(
				result.actions,
				LocalRecoveryIntegrationCompleted,
			)
		}

		snapshot, reviews, runtime, err =
			readCompletionRuntime(journal, definition)
		if err != nil {
			return WorkspaceLocalRecoveryResult{}, err
		}
		assessment := assessWorkspaceCompletion(
			snapshot, definition, reviews, runtime,
		)
		if len(assessment.blockers) == 0 {
			completed, err := CompleteWorkspace(
				ctx, journal, definition, integrationGit,
				CompleteWorkspaceRequest{
					OccurredAt: request.OccurredAt,
					Fault:      request.CompletionFault,
				},
			)
			if err != nil {
				return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
					"recover workspace completion publication: %w",
					err,
				)
			}
			if completed.Record().Sequence() != 0 {
				result.actions = append(
					result.actions,
					LocalRecoveryWorkspaceCompleted,
				)
			}
		}
		return result, nil
	}

	if _, err := CompleteWorkspace(
		ctx, journal, definition, integrationGit,
		CompleteWorkspaceRequest{
			OccurredAt: request.OccurredAt,
			Fault:      request.CompletionFault,
		},
	); err != nil {
		return WorkspaceLocalRecoveryResult{}, fmt.Errorf(
			"verify recovered workspace completion: %w", err,
		)
	}
	return result, nil
}
