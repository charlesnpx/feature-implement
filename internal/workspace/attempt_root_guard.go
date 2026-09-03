package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// attemptWorktreeRootGuard owns only the derived scratch parent. The target
// worktree and Git administration remain read-only inputs to Git; retaining
// their filesystem identities neither protects the primary checkout nor is
// required for exact attempt materialization.
type attemptWorktreeRootGuard struct {
	binding      trustedWorktreeBinding
	worktree     *VerifiedRoot
	worktreePath string
}

func (adapter LocalAttemptGitAdapter) ValidateAttemptWorktreeRoot(
	ctx context.Context,
	repositoryRoot string,
	worktree string,
) error {
	guard, err := adapter.openAttemptWorktreeRootGuard(ctx, repositoryRoot, worktree, false)
	if err != nil {
		return err
	}
	defer guard.Close()
	return nil
}

func (adapter LocalAttemptGitAdapter) openAttemptWorktreeRootGuard(
	ctx context.Context,
	repositoryRoot string,
	worktree string,
	create bool,
) (*attemptWorktreeRootGuard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if !filepath.IsAbs(worktree) {
		return nil, fmt.Errorf("attempt worktree root admission requires an absolute worktree path")
	}
	parentPath, err := canonicalizeTrustedRootPath(filepath.Dir(worktree))
	if err != nil {
		return nil, err
	}
	canonicalWorktree := canonicalWorktreePath(worktree)
	commitAdapter := LocalCommitGitAdapter{git: adapter}
	binding, err := commitAdapter.captureTrustedWorktreeBinding(ctx, repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect attempt worktree Git binding: %w", err)
	}
	if err := rejectAttemptWorktreeRootOverlap(parentPath, canonicalWorktree, binding); err != nil {
		return nil, err
	}
	root, err := OpenVerifiedRoot(RootRoleWorktree, parentPath, create)
	if errors.Is(err, os.ErrNotExist) && !create {
		root = nil
	} else if err != nil {
		return nil, fmt.Errorf("open derived attempt worktree root: %w", err)
	}
	guard := &attemptWorktreeRootGuard{
		binding: binding, worktree: root, worktreePath: canonicalWorktree,
	}
	if err := guard.validateLayout(); err != nil {
		_ = guard.Close()
		return nil, err
	}
	return guard, nil
}

func (adapter LocalAttemptGitAdapter) inspectRegisteredWorktrees(
	ctx context.Context,
	repositoryRoot string,
) (map[string]registeredWorktree, error) {
	output, exitCode, err := adapter.run(
		ctx, repositoryRoot, "worktree", "list", "--porcelain", "-z",
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return nil, fmt.Errorf("inspect registered worktrees: %w", err)
	}
	return parseRegisteredWorktrees(output)
}

func (guard *attemptWorktreeRootGuard) Verify(
	ctx context.Context,
	adapter LocalAttemptGitAdapter,
) error {
	if guard == nil {
		return fmt.Errorf("attempt worktree root guard is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard.worktree != nil {
		if err := guard.worktree.VerifyPath(); err != nil {
			return err
		}
	}
	return guard.validateLayout()
}

func (guard *attemptWorktreeRootGuard) validateLayout() error {
	if guard == nil {
		return fmt.Errorf("attempt worktree root guard is closed")
	}
	if guard.worktree == nil {
		return nil
	}
	if guard.worktree.Path() != filepath.Dir(guard.worktreePath) {
		return fmt.Errorf("derived attempt worktree parent changed during admission")
	}
	for _, protected := range []struct {
		label string
		path  string
	}{
		{label: "target", path: guard.binding.root},
		{label: "Git administration", path: guard.binding.commonDir},
	} {
		if pathContains(protected.path, guard.worktree.Path()) ||
			pathContains(protected.path, guard.worktreePath) ||
			pathContains(guard.worktreePath, protected.path) {
			return fmt.Errorf(
				"unsafe attempt worktree overlap: derived root %s (candidate %s) and %s path %s",
				guard.worktree.Path(), guard.worktreePath, protected.label, protected.path,
			)
		}
	}
	return nil
}

func (guard *attemptWorktreeRootGuard) Close() error {
	if guard == nil || guard.worktree == nil {
		return nil
	}
	err := guard.worktree.Close()
	guard.worktree = nil
	return err
}

func rejectAttemptWorktreeRootOverlap(
	worktreeRoot string,
	worktree string,
	binding trustedWorktreeBinding,
) error {
	for _, protected := range []struct {
		label string
		path  string
	}{
		{label: "target", path: binding.root},
		{label: "Git administration", path: binding.commonDir},
	} {
		if pathContains(protected.path, worktreeRoot) ||
			pathContains(protected.path, worktree) ||
			pathContains(worktree, protected.path) {
			return fmt.Errorf(
				"unsafe attempt worktree overlap: derived root %s (candidate %s) and %s path %s",
				worktreeRoot, worktree, protected.label, protected.path,
			)
		}
	}
	return nil
}
