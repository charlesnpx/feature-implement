package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// FinalHistoryVerifier proves a configured commit protocol from the attempt's
// final clean base-to-head history. It deliberately retains no protocol
// progress, check output, or intermediate commit identity in the journal.
type FinalHistoryVerifier struct {
	git    CommitGitPort
	checks CommitCheckRunnerPort
}

func NewFinalHistoryVerifier(
	git CommitGitPort,
	checks CommitCheckRunnerPort,
) (FinalHistoryVerifier, error) {
	if git == nil {
		return FinalHistoryVerifier{}, fmt.Errorf("final history verifier requires a Git adapter")
	}
	return FinalHistoryVerifier{git: git, checks: checks}, nil
}

// Verify validates every configured checkpoint in order, then runs each
// configured command at the exact accepted head. A command succeeds only when
// the isolated runner returns after an exit-zero command.
func (verifier FinalHistoryVerifier) Verify(
	ctx context.Context,
	protocol CommitProtocol,
	worktree string,
	base, head GitObjectID,
) error {
	if ctx == nil || verifier.git == nil || base.IsZero() || head.IsZero() ||
		base.Algorithm() != head.Algorithm() {
		return fmt.Errorf("final history verification requires context, Git adapter, and compatible base and head")
	}
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if !filepath.IsAbs(worktree) {
		return fmt.Errorf("final history worktree must be absolute")
	}
	if err := verifier.git.VerifyCleanWorktree(ctx, worktree, head); err != nil {
		return fmt.Errorf("verify final clean worktree: %w", err)
	}
	inspections, err := verifier.git.InspectFirstParentRange(ctx, worktree, base, head)
	if err != nil {
		return fmt.Errorf("inspect final commit history: %w", err)
	}
	steps := protocol.steps
	if len(inspections) != len(steps) {
		return fmt.Errorf(
			"configured commit protocol requires %d checkpoints, but final history has %d commits",
			len(steps), len(inspections),
		)
	}
	for index, inspection := range inspections {
		step := steps[index]
		if err := step.message.Validate(inspection.subject, inspection.body); err != nil {
			return fmt.Errorf("commit checkpoint %s message: %w", step.id, err)
		}
		for _, change := range inspection.diff.changes {
			if err := step.paths.ValidateChange(change); err != nil {
				return fmt.Errorf("commit checkpoint %s path policy: %w", step.id, err)
			}
		}
	}
	if verifier.checks == nil {
		for _, step := range steps {
			if len(step.checks) != 0 {
				return fmt.Errorf("configured check %s requires an isolated runner", step.checks[0].id)
			}
		}
	} else {
		finalTree := inspections[len(inspections)-1].tree
		for _, step := range steps {
			for _, check := range step.checks {
				invocation, err := NewFinalCommitCheckInvocation(check, head, finalTree, worktree)
				if err != nil {
					return err
				}
				if err := verifier.checks.RunConfiguredCheck(ctx, invocation); err != nil {
					return fmt.Errorf(
						"configured check %s did not exit zero: %w", check.id, err,
					)
				}
				if err := verifier.git.VerifyCleanWorktree(ctx, worktree, head); err != nil {
					return fmt.Errorf("configured check %s changed Git state: %w", check.id, err)
				}
			}
		}
	}
	if err := verifier.git.VerifyCleanWorktree(ctx, worktree, head); err != nil {
		return fmt.Errorf("verify final clean worktree: %w", err)
	}
	return nil
}

func verifyAttemptFinalHistory(
	ctx context.Context,
	repository ReviewRepositoryPort,
	unit UnitExecution,
	attempt RuntimeAttemptProjection,
	head GitObjectID,
) error {
	protocol, configured := unit.CommitProtocol()
	if !configured {
		return nil
	}
	if err := repository.VerifyFinalHistory(ctx, protocol, attempt.worktree, attempt.base, head); err != nil {
		return err
	}
	return nil
}

// FinalCommitCheckInvocation binds a configured command to the exact final
// commit and tree without carrying a protocol journal identity.
func NewFinalCommitCheckInvocation(
	check CommitCheck,
	commit, tree GitObjectID,
	worktree string,
) (CommitCheckInvocation, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if check.id.IsZero() || check.runner.IsZero() || len(check.command.values) == 0 ||
		commit.IsZero() || tree.IsZero() || commit.Algorithm() != tree.Algorithm() ||
		!filepath.IsAbs(worktree) {
		return CommitCheckInvocation{}, fmt.Errorf("final configured check invocation has incomplete bindings")
	}
	return CommitCheckInvocation{
		checkID: check.id, commit: commit, tree: tree, runner: check.runner,
		command: Argv{values: check.command.Values()}, worktree: worktree,
	}, nil
}
