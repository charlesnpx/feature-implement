package workspace

import (
	"context"
	"fmt"
	"time"
)

type ReviewFixFaultPoint string

const (
	ReviewFixFaultAfterReservation  ReviewFixFaultPoint = "after_reservation"
	ReviewFixFaultAfterIntent       ReviewFixFaultPoint = "after_intent"
	ReviewFixFaultAfterGitCommit    ReviewFixFaultPoint = "after_git_commit"
	ReviewFixFaultAfterCommitRecord ReviewFixFaultPoint = "after_commit_record"
	ReviewFixFaultAfterCheckRun     ReviewFixFaultPoint = "after_check_run"
	ReviewFixFaultAfterCheckRecord  ReviewFixFaultPoint = "after_check_record"
	ReviewFixFaultAfterRebaseRecord ReviewFixFaultPoint = "after_rebase_record"
)

type ReviewFixFaultInjector func(ReviewFixFaultPoint) error

type ExecuteAttemptReviewFixRequest struct {
	AttemptID  ID
	Ordinal    uint16
	Body       string
	OccurredAt time.Time
	Fault      ReviewFixFaultInjector
}

type AttemptReviewFixResult struct {
	configured bool
	attempt    RuntimeAttemptProjection
	state      ReviewFixState
}

func (result AttemptReviewFixResult) Configured() bool { return result.configured }
func (result AttemptReviewFixResult) Attempt() RuntimeAttemptProjection {
	return cloneRuntimeAttempt(result.attempt)
}
func (result AttemptReviewFixResult) State() (ReviewFixState, bool) {
	if !result.configured || result.state.generation.IsZero() {
		return ReviewFixState{}, false
	}
	return cloneReviewFixState(result.state), true
}

// ExecuteAttemptReviewFix durably consumes a caller-selected ordinal before
// forming a Git intent. Reusing the same ordinal is idempotent across every
// crash window; advancing requires the next ordinal and available replayed
// budget.
func ExecuteAttemptReviewFix(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	shell CommitProtocolShell,
	request ExecuteAttemptReviewFixRequest,
) (AttemptReviewFixResult, error) {
	if journal == nil || shell.git == nil || request.AttemptID.IsZero() || request.Ordinal == 0 || request.OccurredAt.IsZero() {
		return AttemptReviewFixResult{}, fmt.Errorf("attempt review fix requires journal, Git shell, attempt, ordinal, and occurrence time")
	}
	result, unit, protocol, err := loadReviewFixExecution(journal, definition, request.AttemptID)
	if err != nil || !result.configured {
		return result, err
	}
	if request.Ordinal > unit.policy.maxReviewFixes {
		return result, fmt.Errorf("review-fix budget is exhausted at ordinal %d", request.Ordinal)
	}
	if result.state.generation.IsZero() {
		if request.Ordinal != 1 {
			return result, fmt.Errorf("first review-fix ordinal must be 1")
		}
	} else if request.Ordinal <= result.state.Used() {
		fix := result.state.fixes[request.Ordinal-1]
		if fix.phase == ReviewFixReserved && request.Ordinal != result.state.Used() {
			return result, fmt.Errorf("review-fix %d is not complete", request.Ordinal)
		}
		if !fix.inspection.stateDigest.IsZero() {
			step, stepErr := protocol.Step(request.Ordinal)
			if stepErr != nil {
				return result, stepErr
			}
			body, bodyErr := step.message.ResolveBody(request.Body)
			if bodyErr != nil || body != fix.body {
				return result, fmt.Errorf("review-fix %d retry body differs from durable intent", request.Ordinal)
			}
		}
		if request.Ordinal < result.state.Used() || fix.phase == ReviewFixComplete && result.state.Quiescent() {
			if err := shell.git.VerifyCleanWorktree(
				ctx, result.attempt.worktree, result.attempt.branch, result.state.Head(),
			); err != nil {
				return result, fmt.Errorf("verify completed review-fix head: %w", err)
			}
			return result, nil
		}
	} else if request.Ordinal != result.state.Used()+1 {
		return result, fmt.Errorf("review-fix ordinal %d is not the next durable ordinal", request.Ordinal)
	}

	var reservationInspection StagedCommitInspection
	if result.state.generation.IsZero() || request.Ordinal > result.state.Used() {
		reservationInspection, err = shell.git.InspectStaged(
			ctx, result.attempt.worktree, result.attempt.branch,
		)
		if err != nil {
			return result, fmt.Errorf("inspect staged review fix before reservation: %w", err)
		}
		event, err := NewReviewFixReservedJournalEvent(
			definition.workspace.id, definition.generation, request.AttemptID,
			protocol, unit.policy.maxReviewFixes, request.Ordinal, reservationInspection.head,
		)
		if err != nil {
			return result, err
		}
		if _, err := appendCommitProtocolEvent(journal, event, request.OccurredAt); err != nil {
			return result, err
		}
		if err := injectReviewFixFault(request.Fault, ReviewFixFaultAfterReservation); err != nil {
			return loadAttemptReviewFixResult(journal, request.AttemptID, true, err)
		}
		result, err = loadAttemptReviewFixResult(journal, request.AttemptID, true, nil)
		if err != nil {
			return result, err
		}
	}

	state := result.state
	if state.Phase() == ReviewFixReserved {
		inspection := reservationInspection
		if inspection.stateDigest.IsZero() {
			inspection, err = shell.git.InspectStaged(ctx, result.attempt.worktree, result.attempt.branch)
			if err != nil {
				return result, err
			}
		}
		step, err := protocol.Step(request.Ordinal)
		if err != nil {
			return result, err
		}
		body, err := step.message.ResolveBody(request.Body)
		if err != nil {
			return result, err
		}
		transition, err := NewStageReviewFix(request.Ordinal, inspection, body)
		if err != nil {
			return result, err
		}
		if _, err := ReduceReviewFix(state, transition); err != nil {
			return result, err
		}
		fix := state.fixes[len(state.fixes)-1]
		event, err := NewReviewFixIntendedJournalEvent(
			definition.workspace.id, definition.generation, request.AttemptID,
			protocol.digest, step, request.Ordinal, fix.parent, fix.reservationKey,
			inspection, body,
		)
		if err != nil {
			return result, err
		}
		if _, err := appendCommitProtocolEvent(journal, event, request.OccurredAt); err != nil {
			return result, err
		}
		if err := injectReviewFixFault(request.Fault, ReviewFixFaultAfterIntent); err != nil {
			return loadAttemptReviewFixResult(journal, request.AttemptID, true, err)
		}
		result, err = loadAttemptReviewFixResult(journal, request.AttemptID, true, nil)
		if err != nil {
			return result, err
		}
		state = result.state
	}

	for state.Phase() == ReviewFixAwaitingCommit || state.Phase() == ReviewFixAwaitingChecks {
		effects, err := PendingReviewFixEffects(state)
		if err != nil || len(effects) != 1 {
			if err == nil {
				err = fmt.Errorf("review-fix protocol has no single pending effect")
			}
			return result, err
		}
		switch effect := effects[0].(type) {
		case CreateConfiguredCommitEffect:
			create, err := NewCreateGitCommitRequest(
				result.attempt.branch, result.attempt.worktree, effect.parent,
				effect.step, effect.ordinal, effect.body, effect.inspection,
			)
			if err != nil {
				return result, err
			}
			inspection, err := shell.git.CreateConfiguredCommit(ctx, create)
			if err != nil {
				return result, err
			}
			evidence, err := inspection.Evidence(effect.generation, effect.step, effect.ordinal)
			if err != nil {
				return result, err
			}
			if err := shell.git.VerifyCleanWorktree(
				ctx, result.attempt.worktree, result.attempt.branch, evidence.commit,
			); err != nil {
				return result, err
			}
			if err := injectReviewFixFault(request.Fault, ReviewFixFaultAfterGitCommit); err != nil {
				return result, err
			}
			event, err := NewReviewFixCommitRecordedJournalEvent(
				definition.workspace.id, definition.generation, request.AttemptID,
				state.protocol.digest, request.Ordinal, effect.idempotencyKey, evidence,
			)
			if err != nil {
				return result, err
			}
			if _, err := appendCommitProtocolEvent(journal, event, request.OccurredAt); err != nil {
				return result, err
			}
			if err := injectReviewFixFault(request.Fault, ReviewFixFaultAfterCommitRecord); err != nil {
				return loadAttemptReviewFixResult(journal, request.AttemptID, true, err)
			}
		case RunConfiguredCheckEffect:
			if shell.checks == nil {
				return result, fmt.Errorf("review-fix check %s requires an isolated runner", effect.check.id)
			}
			if err := shell.git.VerifyCleanWorktree(
				ctx, result.attempt.worktree, result.attempt.branch, result.attempt.verifiedHead,
			); err != nil {
				return result, err
			}
			invocation, err := NewCommitCheckInvocation(effect, result.attempt.worktree)
			if err != nil {
				return result, err
			}
			processResult, err := shell.checks.RunConfiguredCheck(ctx, invocation)
			if err != nil {
				return result, err
			}
			if !processResult.isolation.Strict() {
				return result, fmt.Errorf("review-fix check %s did not prove strict isolation", effect.check.id)
			}
			outcome, err := ParseCheckOutcome(effect.check.parser, processResult)
			if err != nil {
				return result, err
			}
			if !effect.check.expectation.SatisfiedBy(outcome) {
				return result, fmt.Errorf(
					"review-fix check %s produced %s (%v), which does not satisfy %s (%v)",
					effect.check.id, outcome.kind, outcome.identities,
					effect.check.expectation.kind, effect.check.expectation.failureIDs,
				)
			}
			if err := shell.git.VerifyCleanWorktree(
				ctx, result.attempt.worktree, result.attempt.branch, result.attempt.verifiedHead,
			); err != nil {
				return result, fmt.Errorf("review-fix check %s changed Git state: %w", effect.check.id, err)
			}
			evidence, err := NewCommitCheckEvidence(
				effect.generation, effect.step, effect.check, effect.commit, processResult, outcome,
			)
			if err != nil {
				return result, err
			}
			if err := injectReviewFixFault(request.Fault, ReviewFixFaultAfterCheckRun); err != nil {
				return result, err
			}
			event, err := NewReviewFixCheckRecordedJournalEvent(
				definition.workspace.id, definition.generation, request.AttemptID,
				state.protocol.digest, effect.stepOrdinal, effect.checkOrdinal,
				effect.idempotencyKey, evidence,
			)
			if err != nil {
				return result, err
			}
			if _, err := appendCommitProtocolEvent(journal, event, request.OccurredAt); err != nil {
				return result, err
			}
			if err := injectReviewFixFault(request.Fault, ReviewFixFaultAfterCheckRecord); err != nil {
				return loadAttemptReviewFixResult(journal, request.AttemptID, true, err)
			}
		default:
			return result, fmt.Errorf("unsupported durable review-fix effect %T", effects[0])
		}
		result, err = loadAttemptReviewFixResult(journal, request.AttemptID, true, nil)
		if err != nil {
			return result, err
		}
		state = result.state
	}
	if state.Phase() != ReviewFixComplete {
		return result, fmt.Errorf("review-fix %d did not reach completion", request.Ordinal)
	}
	if err := shell.git.VerifyCleanWorktree(
		ctx, result.attempt.worktree, result.attempt.branch, state.Head(),
	); err != nil {
		return result, fmt.Errorf("verify completed review-fix head: %w", err)
	}
	return result, nil
}

func RecordAttemptReviewFixRebase(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	shell CommitProtocolShell,
	attemptID ID,
	newBase, newHead GitObjectID,
	occurredAt time.Time,
	fault ReviewFixFaultInjector,
) (AttemptReviewFixResult, error) {
	if journal == nil || shell.git == nil || attemptID.IsZero() || newBase.IsZero() || newHead.IsZero() || occurredAt.IsZero() {
		return AttemptReviewFixResult{}, fmt.Errorf("review-fix rebase requires journal, Git shell, attempt, objects, and occurrence time")
	}
	attempt, err := recordAttemptProtocolChainRebase(
		ctx, journal, definition, shell, attemptID, newBase, newHead, occurredAt,
		false, true,
		func() error { return injectReviewFixFault(fault, ReviewFixFaultAfterRebaseRecord) },
	)
	result := AttemptReviewFixResult{attempt: cloneRuntimeAttempt(attempt)}
	if attempt.reviewFixes != nil {
		result.configured = true
		result.state = cloneReviewFixState(*attempt.reviewFixes)
	}
	if err != nil {
		return result, err
	}
	if !result.configured {
		return result, fmt.Errorf("attempt %s has no review-fix chain", attemptID)
	}
	return result, nil
}

func loadReviewFixExecution(
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	attemptID ID,
) (AttemptReviewFixResult, UnitExecution, ReviewFixProtocol, error) {
	_, runtime, err := readAttemptRuntime(journal, definition)
	if err != nil {
		return AttemptReviewFixResult{}, UnitExecution{}, ReviewFixProtocol{}, err
	}
	attempt, exists := runtime.Attempt(attemptID)
	if !exists {
		return AttemptReviewFixResult{}, UnitExecution{}, ReviewFixProtocol{}, fmt.Errorf("attempt %s is not reserved", attemptID)
	}
	if attempt.phase != AttemptActive {
		return AttemptReviewFixResult{}, UnitExecution{}, ReviewFixProtocol{}, fmt.Errorf("attempt %s must be active for review-fix execution", attemptID)
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return AttemptReviewFixResult{}, UnitExecution{}, ReviewFixProtocol{}, err
	}
	protocol, configured := unit.ReviewFixProtocol()
	if !configured {
		return AttemptReviewFixResult{configured: false, attempt: attempt}, unit, ReviewFixProtocol{}, nil
	}
	if implementation, required := unit.CommitProtocol(); required {
		if attempt.commitProtocol == nil || attempt.commitProtocol.protocol.digest != implementation.digest ||
			attempt.commitProtocol.phase != CommitProtocolComplete {
			return AttemptReviewFixResult{}, UnitExecution{}, ReviewFixProtocol{}, fmt.Errorf(
				"attempt %s requires a completed implementation protocol before review fixes", attemptID,
			)
		}
	}
	result := AttemptReviewFixResult{configured: true, attempt: attempt}
	if attempt.reviewFixes != nil {
		state := cloneReviewFixState(*attempt.reviewFixes)
		if state.generation != definition.generation || state.protocol.digest != protocol.digest ||
			state.maximum != unit.policy.maxReviewFixes {
			return AttemptReviewFixResult{}, UnitExecution{}, ReviewFixProtocol{}, fmt.Errorf(
				"attempt review-fix state does not match the active generation and effective policy",
			)
		}
		if attempt.commitProtocol != nil && state.base != attempt.commitProtocol.Head() {
			return AttemptReviewFixResult{}, UnitExecution{}, ReviewFixProtocol{}, fmt.Errorf(
				"review-fix chain does not start at the implementation protocol head",
			)
		}
		result.state = state
	}
	return result, unit, protocol, nil
}

func loadAttemptReviewFixResult(
	journal *WorkspaceJournal,
	attemptID ID,
	configured bool,
	returnedErr error,
) (AttemptReviewFixResult, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return AttemptReviewFixResult{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return AttemptReviewFixResult{}, err
	}
	attempt, exists := runtime.Attempt(attemptID)
	if !exists {
		return AttemptReviewFixResult{}, fmt.Errorf("attempt %s is missing", attemptID)
	}
	result := AttemptReviewFixResult{configured: configured, attempt: attempt}
	if configured {
		if attempt.reviewFixes == nil {
			return AttemptReviewFixResult{}, fmt.Errorf("attempt %s has no review-fix state", attemptID)
		}
		result.state = cloneReviewFixState(*attempt.reviewFixes)
	}
	return result, returnedErr
}

func injectReviewFixFault(injector ReviewFixFaultInjector, point ReviewFixFaultPoint) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf("review-fix fault at %s: %w", point, err)
	}
	return nil
}
