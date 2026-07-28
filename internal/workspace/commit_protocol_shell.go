package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

type CommitCheckInvocation struct {
	generation     Digest
	protocol       Digest
	stepID         ID
	stepOrdinal    uint16
	checkID        ID
	checkOrdinal   uint16
	commit         GitObjectID
	tree           GitObjectID
	diff           Digest
	runner         ID
	parser         CheckParserKind
	command        Argv
	worktree       string
	idempotencyKey Digest
}

func NewCommitCheckInvocation(effect RunConfiguredCheckEffect, worktree string) (CommitCheckInvocation, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	step, check, commit := effect.step, effect.check, effect.commit
	if !filepath.IsAbs(worktree) || effect.generation.IsZero() || effect.protocol.IsZero() ||
		step.id.IsZero() || effect.stepOrdinal == 0 || check.id.IsZero() || effect.checkOrdinal == 0 ||
		commit.commit.IsZero() || commit.tree.IsZero() || commit.diff.digest.IsZero() ||
		check.runner.IsZero() || !check.parser.valid() || len(check.command.values) == 0 || effect.idempotencyKey.IsZero() {
		return CommitCheckInvocation{}, fmt.Errorf("configured check invocation has incomplete bindings")
	}
	return CommitCheckInvocation{
		generation: effect.generation, protocol: effect.protocol,
		stepID: step.id, stepOrdinal: effect.stepOrdinal,
		checkID: check.id, checkOrdinal: effect.checkOrdinal,
		commit: commit.commit, tree: commit.tree, diff: commit.diff.digest,
		runner: check.runner, parser: check.parser, command: Argv{values: check.command.Values()},
		worktree: worktree, idempotencyKey: effect.idempotencyKey,
	}, nil
}

func (invocation CommitCheckInvocation) Generation() Digest      { return invocation.generation }
func (invocation CommitCheckInvocation) ProtocolDigest() Digest  { return invocation.protocol }
func (invocation CommitCheckInvocation) StepID() ID              { return invocation.stepID }
func (invocation CommitCheckInvocation) StepOrdinal() uint16     { return invocation.stepOrdinal }
func (invocation CommitCheckInvocation) CheckID() ID             { return invocation.checkID }
func (invocation CommitCheckInvocation) CheckOrdinal() uint16    { return invocation.checkOrdinal }
func (invocation CommitCheckInvocation) Commit() GitObjectID     { return invocation.commit }
func (invocation CommitCheckInvocation) Tree() GitObjectID       { return invocation.tree }
func (invocation CommitCheckInvocation) DiffDigest() Digest      { return invocation.diff }
func (invocation CommitCheckInvocation) Runner() ID              { return invocation.runner }
func (invocation CommitCheckInvocation) Parser() CheckParserKind { return invocation.parser }
func (invocation CommitCheckInvocation) Command() Argv {
	return Argv{values: invocation.command.Values()}
}
func (invocation CommitCheckInvocation) Worktree() string       { return invocation.worktree }
func (invocation CommitCheckInvocation) IdempotencyKey() Digest { return invocation.idempotencyKey }

// CommitCheckRunnerPort is a capability boundary, not a generic process port.
// An implementation must materialize the invocation's exact commit and tree
// in its isolated execution root rather than execute against the ambient
// worktree head. It must also deny repository hooks and write-capable network
// access, then return a proof describing the actual isolation used. The shell rejects any weaker
// proof. Worktree is repository input for that materialization, not authority
// to substitute its current checkout for the invocation bindings.
type CommitCheckRunnerPort interface {
	RunConfiguredCheck(context.Context, CommitCheckInvocation) (CheckProcessResult, error)
}

type CommitProtocolShell struct {
	git    CommitGitPort
	checks CommitCheckRunnerPort
}

func NewCommitProtocolShell(git CommitGitPort, checks CommitCheckRunnerPort) (CommitProtocolShell, error) {
	if git == nil {
		return CommitProtocolShell{}, fmt.Errorf("commit protocol shell requires a Git adapter")
	}
	return CommitProtocolShell{git: git, checks: checks}, nil
}

// CommitProtocolStateForUnit returns configured=false without imposing any
// commit constraints when the merge unit has no protocol. This is the explicit
// optionality boundary: an absent protocol is unconstrained, never an empty
// strict protocol.
func CommitProtocolStateForUnit(
	unit UnitExecution,
	generation Digest,
	base GitObjectID,
) (CommitProtocolState, bool, error) {
	protocol, configured := unit.CommitProtocol()
	if !configured {
		return CommitProtocolState{}, false, nil
	}
	state, err := NewCommitProtocolState(generation, base, protocol)
	if err != nil {
		return CommitProtocolState{}, true, err
	}
	return state, true, nil
}

// ReviewFixBudgetForAttempt derives usage from replayed attempt state. Budget
// consumption cannot be reset by constructing a new in-memory counter.
func ReviewFixBudgetForAttempt(
	unit UnitExecution,
	attempt RuntimeAttemptProjection,
) (ReviewFixBudget, bool, error) {
	protocol, configured := unit.ReviewFixProtocol()
	if !configured {
		if attempt.reviewFixes != nil {
			return ReviewFixBudget{}, false, fmt.Errorf("attempt has review-fix state without an effective protocol")
		}
		return ReviewFixBudget{}, false, nil
	}
	if attempt.attemptID.IsZero() || attempt.generation.IsZero() {
		return ReviewFixBudget{}, true, fmt.Errorf("review-fix budget requires a replayed attempt")
	}
	used := uint16(0)
	if attempt.reviewFixes != nil {
		state := attempt.reviewFixes
		if state.generation != attempt.generation || state.protocol.digest != protocol.digest ||
			state.maximum != unit.policy.maxReviewFixes {
			return ReviewFixBudget{}, true, fmt.Errorf("attempt review-fix state does not match its effective policy")
		}
		used = state.Used()
	}
	budget, err := newReviewFixBudget(protocol, unit.policy.maxReviewFixes, used)
	if err != nil {
		return ReviewFixBudget{}, true, err
	}
	return budget, true, nil
}

func (shell CommitProtocolShell) ExecuteNextCommitStep(
	ctx context.Context,
	state CommitProtocolState,
	branch, worktree, body string,
) (CommitProtocolState, error) {
	if shell.git == nil {
		return CommitProtocolState{}, fmt.Errorf("commit protocol shell has no Git adapter")
	}
	if state.phase != CommitProtocolReady {
		return CommitProtocolState{}, fmt.Errorf("next commit step requires ready protocol state")
	}
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return CommitProtocolState{}, err
	}
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if !filepath.IsAbs(worktree) {
		return CommitProtocolState{}, fmt.Errorf("commit protocol worktree must be absolute")
	}
	inspection, err := shell.git.InspectStaged(ctx, worktree, branch)
	if err != nil {
		return CommitProtocolState{}, err
	}
	stage, err := NewStageCommitStep(inspection, body)
	if err != nil {
		return CommitProtocolState{}, err
	}
	reduction, err := ReduceCommitProtocol(state, stage)
	if err != nil {
		return CommitProtocolState{}, err
	}
	return shell.drive(ctx, reduction.State(), branch, worktree)
}

func (shell CommitProtocolShell) Resume(
	ctx context.Context,
	state CommitProtocolState,
	branch, worktree string,
) (CommitProtocolState, error) {
	if shell.git == nil {
		return CommitProtocolState{}, fmt.Errorf("commit protocol shell has no Git adapter")
	}
	if state.phase != CommitProtocolAwaitingCommit && state.phase != CommitProtocolAwaitingChecks {
		return cloneCommitProtocolState(state), nil
	}
	return shell.drive(ctx, state, branch, filepath.Clean(strings.TrimSpace(worktree)))
}

func (shell CommitProtocolShell) RemapAfterRebase(
	ctx context.Context,
	state CommitProtocolState,
	branch, worktree string,
	newBase, newHead GitObjectID,
) (CommitProtocolState, error) {
	if shell.git == nil {
		return CommitProtocolState{}, fmt.Errorf("commit protocol shell has no Git adapter")
	}
	if state.phase != CommitProtocolReady && state.phase != CommitProtocolComplete {
		return CommitProtocolState{}, fmt.Errorf("rebase remapping requires quiescent commit protocol state")
	}
	if err := shell.git.VerifyCleanWorktree(ctx, worktree, branch, newHead); err != nil {
		return CommitProtocolState{}, fmt.Errorf("verify rebased worktree: %w", err)
	}
	var inspections []GitCommitInspection
	if len(state.steps) == 0 {
		if newHead != newBase {
			return CommitProtocolState{}, fmt.Errorf("base-only commit rebase must end at the new base")
		}
	} else {
		var err error
		inspections, err = shell.git.InspectFirstParentRange(ctx, worktree, newBase, newHead)
		if err != nil {
			return CommitProtocolState{}, err
		}
	}
	if len(inspections) != len(state.steps) {
		return CommitProtocolState{}, fmt.Errorf("rebased first-parent range has %d commits, expected %d", len(inspections), len(state.steps))
	}
	evidence := make([]CommitObjectEvidence, 0, len(inspections))
	for index, inspection := range inspections {
		item, err := inspection.Evidence(state.generation, state.protocol.steps[index], uint16(index+1))
		if err != nil {
			return CommitProtocolState{}, fmt.Errorf("re-prove rebased commit %d: %w", index+1, err)
		}
		evidence = append(evidence, item)
	}
	event, err := NewRemapRebasedCommits(newBase, evidence)
	if err != nil {
		return CommitProtocolState{}, err
	}
	reduction, err := ReduceCommitProtocol(state, event)
	if err != nil {
		return CommitProtocolState{}, err
	}
	if reduction.State().phase == CommitProtocolAwaitingChecks {
		return shell.drive(ctx, reduction.State(), branch, worktree)
	}
	return reduction.State(), nil
}

func (shell CommitProtocolShell) VerifySequence(
	ctx context.Context,
	state CommitProtocolState,
	repositoryRoot string,
	base, head GitObjectID,
) error {
	if shell.git == nil {
		return fmt.Errorf("commit protocol shell has no Git adapter")
	}
	if err := validateCommitProtocolState(state); err != nil {
		return err
	}
	if state.phase != CommitProtocolComplete {
		return fmt.Errorf("configured commit sequence is not complete")
	}
	inspections, err := shell.git.InspectFirstParentRange(ctx, repositoryRoot, base, head)
	if err != nil {
		return err
	}
	if len(inspections) != len(state.protocol.steps) || len(inspections) != len(state.steps) {
		return fmt.Errorf("configured first-parent sequence has an unexpected commit count")
	}
	parent := base
	for index, inspection := range inspections {
		evidence, err := inspection.Evidence(state.generation, state.protocol.steps[index], uint16(index+1))
		if err != nil {
			return err
		}
		if evidence.parent != parent || evidence.evidence != state.steps[index].commit.evidence {
			return fmt.Errorf("configured commit step %s no longer matches recorded evidence", state.protocol.steps[index].id)
		}
		for checkIndex, checkEvidence := range state.steps[index].checks {
			if err := checkEvidence.Validate(state.protocol.steps[index].checks[checkIndex], evidence); err != nil {
				return err
			}
		}
		parent = evidence.commit
	}
	if parent != head {
		return fmt.Errorf("configured commit sequence does not end at requested head")
	}
	return nil
}

func (shell CommitProtocolShell) drive(
	ctx context.Context,
	state CommitProtocolState,
	branch, worktree string,
) (CommitProtocolState, error) {
	fail := func(err error) (CommitProtocolState, error) {
		return cloneCommitProtocolState(state), err
	}
	for {
		effects, err := PendingCommitProtocolEffects(state)
		if err != nil {
			return fail(err)
		}
		if len(effects) == 0 || state.phase == CommitProtocolReady || state.phase == CommitProtocolComplete {
			return cloneCommitProtocolState(state), nil
		}
		switch effect := effects[0].(type) {
		case CreateConfiguredCommitEffect:
			request, err := NewCreateGitCommitRequest(
				branch, worktree, effect.parent, effect.step, effect.ordinal,
				effect.body, effect.inspection,
			)
			if err != nil {
				return fail(err)
			}
			inspection, err := shell.git.CreateConfiguredCommit(ctx, request)
			if err != nil {
				return fail(err)
			}
			evidence, err := inspection.Evidence(effect.generation, effect.step, effect.ordinal)
			if err != nil {
				return fail(err)
			}
			if err := shell.git.VerifyCleanWorktree(ctx, worktree, branch, evidence.commit); err != nil {
				return fail(err)
			}
			event, err := NewRecordCommitStep(evidence)
			if err != nil {
				return fail(err)
			}
			reduction, err := ReduceCommitProtocol(state, event)
			if err != nil {
				return fail(err)
			}
			state = reduction.State()
		case RunConfiguredCheckEffect:
			if shell.checks == nil {
				return fail(fmt.Errorf("configured commit check %s requires an isolated runner", effect.check.id))
			}
			if err := shell.git.VerifyCleanWorktree(ctx, worktree, branch, state.Head()); err != nil {
				return fail(fmt.Errorf("verify worktree before check %s: %w", effect.check.id, err))
			}
			invocation, err := NewCommitCheckInvocation(effect, worktree)
			if err != nil {
				return fail(err)
			}
			processResult, err := shell.checks.RunConfiguredCheck(ctx, invocation)
			if err != nil {
				return fail(fmt.Errorf("run configured check %s: %w", effect.check.id, err))
			}
			if !processResult.isolation.Strict() {
				return fail(fmt.Errorf("configured check %s did not prove hook and network isolation", effect.check.id))
			}
			outcome, err := ParseCheckOutcome(effect.check.parser, processResult)
			if err != nil {
				return fail(err)
			}
			if !effect.check.expectation.SatisfiedBy(outcome) {
				return fail(fmt.Errorf(
					"configured check %s produced %s (%v), which does not satisfy %s (%v)",
					effect.check.id, outcome.kind, outcome.identities,
					effect.check.expectation.kind, effect.check.expectation.failureIDs,
				))
			}
			if err := shell.git.VerifyCleanWorktree(ctx, worktree, branch, state.Head()); err != nil {
				return fail(fmt.Errorf("configured check %s changed Git state: %w", effect.check.id, err))
			}
			evidence, err := NewCommitCheckEvidence(
				effect.generation, effect.step, effect.check, effect.commit, processResult, outcome,
			)
			if err != nil {
				return fail(err)
			}
			event, err := NewRecordCommitCheck(evidence)
			if err != nil {
				return fail(err)
			}
			reduction, err := ReduceCommitProtocol(state, event)
			if err != nil {
				return fail(err)
			}
			state = reduction.State()
		default:
			return fail(fmt.Errorf("unsupported imperative commit effect %T", effects[0]))
		}
	}
}
