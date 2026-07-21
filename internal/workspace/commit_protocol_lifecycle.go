package workspace

import (
	"context"
	"fmt"
	"time"
)

type CommitProtocolFaultPoint string

const (
	CommitFaultAfterProtocolStart CommitProtocolFaultPoint = "after_protocol_start"
	CommitFaultAfterStepIntent    CommitProtocolFaultPoint = "after_step_intent"
	CommitFaultAfterGitCommit     CommitProtocolFaultPoint = "after_git_commit"
	CommitFaultAfterStepRecord    CommitProtocolFaultPoint = "after_step_record"
	CommitFaultAfterCheckRun      CommitProtocolFaultPoint = "after_check_run"
	CommitFaultAfterCheckRecord   CommitProtocolFaultPoint = "after_check_record"
	CommitFaultAfterRebaseRecord  CommitProtocolFaultPoint = "after_rebase_record"
)

type CommitProtocolFaultInjector func(CommitProtocolFaultPoint) error

type ExecuteAttemptCommitStepRequest struct {
	AttemptID  ID
	Body       string
	OccurredAt time.Time
	Fault      CommitProtocolFaultInjector
}

type AttemptCommitProtocolResult struct {
	configured bool
	attempt    RuntimeAttemptProjection
	protocol   CommitProtocolState
}

func (result AttemptCommitProtocolResult) Configured() bool { return result.configured }
func (result AttemptCommitProtocolResult) Attempt() RuntimeAttemptProjection {
	return cloneRuntimeAttempt(result.attempt)
}
func (result AttemptCommitProtocolResult) Protocol() (CommitProtocolState, bool) {
	if !result.configured {
		return CommitProtocolState{}, false
	}
	return cloneCommitProtocolState(result.protocol), true
}

// ExecuteAttemptCommitStep is the durable imperative shell. Every external
// mutation is preceded by a journaled intent, and every observation is
// journaled before advancing. Retrying after a crash either reuses the exact
// existing commit or safely reruns an isolated, read-only check.
func ExecuteAttemptCommitStep(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	shell CommitProtocolShell,
	request ExecuteAttemptCommitStepRequest,
) (AttemptCommitProtocolResult, error) {
	if journal == nil || shell.git == nil || request.AttemptID.IsZero() || request.OccurredAt.IsZero() {
		return AttemptCommitProtocolResult{}, fmt.Errorf("attempt commit step requires journal, Git shell, attempt, and occurrence time")
	}
	result, err := ensureAttemptCommitProtocolStarted(ctx, journal, definition, shell, request)
	if err != nil || !result.configured {
		return result, err
	}
	state := result.protocol
	if state.phase == CommitProtocolComplete {
		return result, nil
	}
	if state.phase == CommitProtocolReady {
		inspection, err := shell.git.InspectStaged(ctx, result.attempt.worktree, result.attempt.branch)
		if err != nil {
			return result, err
		}
		ordinal := uint16(len(state.steps) + 1)
		step := state.protocol.steps[ordinal-1]
		resolvedBody, err := step.message.ResolveBody(request.Body)
		if err != nil {
			return result, err
		}
		stage, err := NewStageCommitStep(inspection, resolvedBody)
		if err != nil {
			return result, err
		}
		if _, err := ReduceCommitProtocol(state, stage); err != nil {
			return result, err
		}
		event, err := NewCommitStepIntendedJournalEvent(
			definition.workspace.id, definition.generation, request.AttemptID,
			state.protocol.digest, step.id, ordinal, state.Head(), inspection,
			resolvedBody, state.rebaseEpoch,
		)
		if err != nil {
			return result, err
		}
		if _, err := appendCommitProtocolEvent(journal, event, request.OccurredAt); err != nil {
			return result, err
		}
		if err := injectCommitProtocolFault(request.Fault, CommitFaultAfterStepIntent); err != nil {
			return loadAttemptCommitProtocolResult(journal, request.AttemptID, true, err)
		}
		result, err = loadAttemptCommitProtocolResult(journal, request.AttemptID, true, nil)
		if err != nil {
			return result, err
		}
		state = result.protocol
	}
	for state.phase == CommitProtocolAwaitingCommit || state.phase == CommitProtocolAwaitingChecks {
		effects, err := PendingCommitProtocolEffects(state)
		if err != nil || len(effects) != 1 {
			if err == nil {
				err = fmt.Errorf("commit protocol has no single pending effect")
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
			if err := injectCommitProtocolFault(request.Fault, CommitFaultAfterGitCommit); err != nil {
				return result, err
			}
			event, err := NewCommitStepRecordedJournalEvent(
				definition.workspace.id, definition.generation, request.AttemptID,
				state.protocol.digest, effect.idempotencyKey, evidence,
			)
			if err != nil {
				return result, err
			}
			if _, err := appendCommitProtocolEvent(journal, event, request.OccurredAt); err != nil {
				return result, err
			}
			if err := injectCommitProtocolFault(request.Fault, CommitFaultAfterStepRecord); err != nil {
				return loadAttemptCommitProtocolResult(journal, request.AttemptID, true, err)
			}
		case RunConfiguredCheckEffect:
			if shell.checks == nil {
				return result, fmt.Errorf("configured check %s requires an isolated runner", effect.check.id)
			}
			if err := shell.git.VerifyCleanWorktree(
				ctx, result.attempt.worktree, result.attempt.branch, effect.commit.commit,
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
				return result, fmt.Errorf("configured check %s did not prove strict isolation", effect.check.id)
			}
			outcome, err := ParseCheckOutcome(effect.check.parser, processResult)
			if err != nil {
				return result, err
			}
			if !effect.check.expectation.SatisfiedBy(outcome) {
				return result, fmt.Errorf(
					"configured check %s produced %s (%v), which does not satisfy %s (%v)",
					effect.check.id, outcome.kind, outcome.identities,
					effect.check.expectation.kind, effect.check.expectation.failureIDs,
				)
			}
			if err := shell.git.VerifyCleanWorktree(
				ctx, result.attempt.worktree, result.attempt.branch, effect.commit.commit,
			); err != nil {
				return result, fmt.Errorf("configured check %s changed Git state: %w", effect.check.id, err)
			}
			evidence, err := NewCommitCheckEvidence(
				effect.generation, effect.step, effect.check, effect.commit, processResult, outcome,
			)
			if err != nil {
				return result, err
			}
			if err := injectCommitProtocolFault(request.Fault, CommitFaultAfterCheckRun); err != nil {
				return result, err
			}
			event, err := NewCommitCheckRecordedJournalEvent(
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
			if err := injectCommitProtocolFault(request.Fault, CommitFaultAfterCheckRecord); err != nil {
				return loadAttemptCommitProtocolResult(journal, request.AttemptID, true, err)
			}
		default:
			return result, fmt.Errorf("unsupported durable commit effect %T", effects[0])
		}
		result, err = loadAttemptCommitProtocolResult(journal, request.AttemptID, true, nil)
		if err != nil {
			return result, err
		}
		state = result.protocol
	}
	return result, nil
}

func RecordAttemptCommitRebase(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	shell CommitProtocolShell,
	attemptID ID,
	newBase, newHead GitObjectID,
	occurredAt time.Time,
	fault CommitProtocolFaultInjector,
) (AttemptCommitProtocolResult, error) {
	if journal == nil || shell.git == nil || attemptID.IsZero() || newBase.IsZero() || newHead.IsZero() || occurredAt.IsZero() {
		return AttemptCommitProtocolResult{}, fmt.Errorf("commit rebase requires journal, Git shell, attempt, objects, and occurrence time")
	}
	result, err := loadAttemptCommitProtocolResult(journal, attemptID, true, nil)
	if err != nil {
		return result, err
	}
	alreadyRecorded := result.protocol.base == newBase && result.protocol.Head() == newHead
	if result.protocol.phase == CommitProtocolAwaitingChecks && alreadyRecorded && result.protocol.rebaseEpoch > 0 {
		// A crash can occur after the rebase event is durable but before the
		// caller observes success. The remapped checks are intentionally still
		// pending; replaying the rebase itself is a successful no-op.
		return result, nil
	}
	if result.protocol.phase != CommitProtocolReady && result.protocol.phase != CommitProtocolComplete {
		return result, fmt.Errorf("commit rebase is blocked by an in-flight commit or check")
	}
	if alreadyRecorded {
		return result, nil
	}
	if err := shell.git.VerifyCleanWorktree(ctx, result.attempt.worktree, result.attempt.branch, newHead); err != nil {
		return result, err
	}
	inspections, err := shell.git.InspectFirstParentRange(ctx, result.attempt.worktree, newBase, newHead)
	if err != nil {
		return result, err
	}
	if len(inspections) != len(result.protocol.steps) {
		return result, fmt.Errorf("rebased commit count %d does not match recorded count %d", len(inspections), len(result.protocol.steps))
	}
	commits := make([]CommitObjectEvidence, 0, len(inspections))
	for index, inspection := range inspections {
		evidence, err := inspection.Evidence(
			definition.generation, result.protocol.protocol.steps[index], uint16(index+1),
		)
		if err != nil {
			return result, err
		}
		commits = append(commits, evidence)
	}
	transition, err := NewRemapRebasedCommits(newBase, commits)
	if err != nil {
		return result, err
	}
	if _, err := ReduceCommitProtocol(result.protocol, transition); err != nil {
		return result, err
	}
	event, err := NewCommitProtocolRebasedJournalEvent(
		definition.workspace.id, definition.generation, attemptID,
		result.protocol.protocol.digest, newBase, commits,
	)
	if err != nil {
		return result, err
	}
	if _, err := appendCommitProtocolEvent(journal, event, occurredAt); err != nil {
		return result, err
	}
	if err := injectCommitProtocolFault(fault, CommitFaultAfterRebaseRecord); err != nil {
		return loadAttemptCommitProtocolResult(journal, attemptID, true, err)
	}
	return loadAttemptCommitProtocolResult(journal, attemptID, true, nil)
}

func ensureAttemptCommitProtocolStarted(
	ctx context.Context,
	journal *WorkspaceJournal,
	definition EffectiveWorkspaceDefinition,
	shell CommitProtocolShell,
	request ExecuteAttemptCommitStepRequest,
) (AttemptCommitProtocolResult, error) {
	snapshot, runtime, err := readAttemptRuntime(journal, definition)
	_ = snapshot
	if err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	attempt, exists := runtime.Attempt(request.AttemptID)
	if !exists {
		return AttemptCommitProtocolResult{}, fmt.Errorf("attempt %s is not reserved", request.AttemptID)
	}
	if attempt.phase != AttemptActive {
		return AttemptCommitProtocolResult{}, fmt.Errorf("attempt %s must be active for commit protocol execution", request.AttemptID)
	}
	unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
	if err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	protocol, configured := unit.CommitProtocol()
	if !configured {
		return AttemptCommitProtocolResult{configured: false, attempt: attempt}, nil
	}
	if attempt.commitProtocol != nil {
		if attempt.commitProtocol.protocol.digest != protocol.digest {
			return AttemptCommitProtocolResult{}, fmt.Errorf("attempt commit protocol does not match active generation")
		}
		return AttemptCommitProtocolResult{
			configured: true, attempt: attempt, protocol: cloneCommitProtocolState(*attempt.commitProtocol),
		}, nil
	}
	inspection, err := shell.git.InspectStaged(ctx, attempt.worktree, attempt.branch)
	if err != nil {
		return AttemptCommitProtocolResult{}, fmt.Errorf("inspect attempt before commit protocol start: %w", err)
	}
	initial, err := NewCommitProtocolState(definition.generation, attempt.verifiedHead, protocol)
	if err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	stage, err := NewStageCommitStep(inspection, request.Body)
	if err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	if _, err := ReduceCommitProtocol(initial, stage); err != nil {
		return AttemptCommitProtocolResult{}, fmt.Errorf("preflight first configured commit: %w", err)
	}
	event, err := NewCommitProtocolStartedJournalEvent(
		definition.workspace.id, definition.generation, attempt.attemptID, attempt.verifiedHead, protocol,
	)
	if err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	if _, err := appendCommitProtocolEvent(journal, event, request.OccurredAt); err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	if err := injectCommitProtocolFault(request.Fault, CommitFaultAfterProtocolStart); err != nil {
		return loadAttemptCommitProtocolResult(journal, request.AttemptID, true, err)
	}
	return loadAttemptCommitProtocolResult(journal, request.AttemptID, true, nil)
}

func appendCommitProtocolEvent(
	journal *WorkspaceJournal,
	event WorkspaceJournalEvent,
	occurredAt time.Time,
) (JournalRecord, error) {
	reads, writes, ok := commitJournalEventResources(event)
	if !ok {
		return JournalRecord{}, fmt.Errorf("unsupported commit protocol event %T", event)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalRecord{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalRecord{}, err
	}
	reads, _ = normalizeJournalWriteSet(reads)
	writes, _ = normalizeJournalWriteSet(writes)
	readSet := make([]JournalResourceRevision, 0, len(reads))
	for _, resource := range reads {
		revision, _ := NewJournalResourceRevision(resource, snapshot.Revision(resource))
		readSet = append(readSet, revision)
	}
	appendRequest, err := newPrivilegedJournalAppend(event, occurredAt, readSet, writes)
	if err != nil {
		return JournalRecord{}, err
	}
	prospective, err := buildJournalRecord(snapshot, appendRequest)
	if err != nil {
		return JournalRecord{}, err
	}
	if _, err := reduceWorkspaceRuntime(runtime, prospective); err != nil {
		return JournalRecord{}, fmt.Errorf("validate commit protocol transition: %w", err)
	}
	return journal.AppendIfHead(appendRequest, snapshot.head)
}

func loadAttemptCommitProtocolResult(
	journal *WorkspaceJournal,
	attemptID ID,
	configured bool,
	returnedErr error,
) (AttemptCommitProtocolResult, error) {
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return AttemptCommitProtocolResult{}, err
	}
	attempt, exists := runtime.Attempt(attemptID)
	if !exists {
		return AttemptCommitProtocolResult{}, fmt.Errorf("attempt %s is missing", attemptID)
	}
	result := AttemptCommitProtocolResult{configured: configured, attempt: attempt}
	if configured {
		if attempt.commitProtocol == nil {
			return AttemptCommitProtocolResult{}, fmt.Errorf("attempt %s has no commit protocol state", attemptID)
		}
		result.protocol = cloneCommitProtocolState(*attempt.commitProtocol)
	}
	return result, returnedErr
}

func injectCommitProtocolFault(injector CommitProtocolFaultInjector, point CommitProtocolFaultPoint) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf("commit protocol fault at %s: %w", point, err)
	}
	return nil
}
