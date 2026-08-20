package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// attemptWorktreeRootGuard binds every Git-owned filesystem boundary that
// could otherwise be substituted while an attempt claim is prepared.
type attemptWorktreeRootGuard struct {
	binding         trustedWorktreeBinding
	registered      map[string]registeredWorktree
	target          *VerifiedRoot
	common          *VerifiedRoot
	worktree        *VerifiedRoot
	registeredRoots []*VerifiedRoot
	worktreePath    string
}

func (adapter LocalAttemptGitAdapter) ValidateAttemptWorktreeRoot(
	ctx context.Context,
	repositoryRoot string,
	worktree string,
) error {
	guard, err := adapter.openAttemptWorktreeRootGuard(
		ctx, repositoryRoot, worktree, false,
	)
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
	canonicalParent := canonicalWorktreePath(parentPath)
	canonicalWorktree := canonicalWorktreePath(worktree)

	commitAdapter := LocalCommitGitAdapter{git: adapter}
	binding, err := commitAdapter.captureTrustedWorktreeBinding(ctx, repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect attempt worktree Git binding: %w", err)
	}
	registered, err := adapter.inspectRegisteredWorktrees(ctx, binding.root)
	if err != nil {
		return nil, err
	}
	if err := rejectAttemptWorktreeRootOverlap(
		canonicalParent, canonicalWorktree, binding, registered,
	); err != nil {
		return nil, err
	}

	guard := &attemptWorktreeRootGuard{
		binding: binding, registered: registered, worktreePath: canonicalWorktree,
	}
	closeGuard := true
	defer func() {
		if closeGuard {
			_ = guard.Close()
		}
	}()
	guard.target, err = OpenVerifiedRoot(RootRoleTarget, binding.root, false)
	if err != nil {
		return nil, fmt.Errorf("open attempt target root: %w", err)
	}
	guard.common, err = OpenVerifiedRoot(RootRoleGitCommon, binding.commonDir, false)
	if err != nil {
		return nil, fmt.Errorf("open attempt Git common root: %w", err)
	}
	for _, registeredPath := range sortedRegisteredWorktreePaths(registered) {
		info, statErr := os.Lstat(registeredPath)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf(
				"inspect registered worktree root %s: %w", registeredPath, statErr,
			)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf(
				"registered worktree root %s is not a directory", registeredPath,
			)
		}
		root, openErr := OpenVerifiedRoot(
			RootRoleRegisteredWorktree, registeredPath, false,
		)
		if openErr != nil {
			return nil, fmt.Errorf(
				"open registered worktree root %s: %w", registeredPath, openErr,
			)
		}
		guard.registeredRoots = append(guard.registeredRoots, root)
	}
	guard.worktree, err = OpenVerifiedRoot(RootRoleWorktree, parentPath, create)
	if errors.Is(err, os.ErrNotExist) && !create {
		guard.worktree = nil
	} else if err != nil {
		return nil, fmt.Errorf("open attempt worktree root: %w", err)
	}
	if guard.worktree != nil && guard.worktree.Path() != canonicalParent {
		return nil, fmt.Errorf(
			"attempt worktree root resolved from %s to unexpected path %s",
			canonicalParent, guard.worktree.Path(),
		)
	}
	if err := guard.validateLayout(); err != nil {
		return nil, err
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return nil, fmt.Errorf("verify opened attempt worktree roots: %w", err)
	}
	closeGuard = false
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
	if guard == nil || guard.target == nil || guard.common == nil {
		return fmt.Errorf("attempt worktree root guard is closed")
	}
	for _, root := range append(
		[]*VerifiedRoot{guard.target, guard.common, guard.worktree},
		guard.registeredRoots...,
	) {
		if root == nil {
			continue
		}
		if err := root.VerifyPath(); err != nil {
			return err
		}
	}
	commitAdapter := LocalCommitGitAdapter{git: adapter}
	confirmedBinding, err := commitAdapter.captureTrustedWorktreeBinding(
		ctx, guard.binding.root,
	)
	if err != nil {
		return fmt.Errorf("reinspect attempt worktree Git binding: %w", err)
	}
	if confirmedBinding != guard.binding {
		return fmt.Errorf("attempt worktree Git binding changed during root admission")
	}
	confirmedRegistered, err := adapter.inspectRegisteredWorktrees(
		ctx, guard.binding.root,
	)
	if err != nil {
		return err
	}
	if !sameRegisteredWorktrees(guard.registered, confirmedRegistered) {
		return fmt.Errorf("registered Git worktrees changed during root admission")
	}
	return guard.validateLayout()
}

func (guard *attemptWorktreeRootGuard) verifyAfterEffect(
	ctx context.Context,
	adapter LocalAttemptGitAdapter,
	effect string,
) error {
	if err := guard.Verify(ctx, adapter); err != nil {
		return fmt.Errorf("verify attempt worktree roots after %s: %w", effect, err)
	}
	return nil
}

func (guard *attemptWorktreeRootGuard) validateLayout() error {
	if guard == nil || guard.target == nil || guard.common == nil {
		return fmt.Errorf("attempt worktree root guard is closed")
	}
	if guard.worktree == nil {
		return nil
	}
	sensitive := append(
		[]*VerifiedRoot{guard.target, guard.common},
		guard.registeredRoots...,
	)
	for _, root := range sensitive {
		// The storage root may contain sibling attempt worktrees. It is unsafe
		// only when the storage root is itself inside a Git-owned root, or when
		// this exact candidate contains or is contained by one.
		if guard.worktree.Identity() == root.Identity() ||
			pathContains(root.Path(), guard.worktree.Path()) {
			return unsafeAttemptWorktreeRootOverlap(
				guard.worktree.Path(), guard.worktreePath, root.Role(), root.Path(),
			)
		}
		if pathContains(root.Path(), guard.worktreePath) ||
			pathContains(guard.worktreePath, root.Path()) {
			return unsafeAttemptWorktreeRootOverlap(
				guard.worktree.Path(), guard.worktreePath, root.Role(), root.Path(),
			)
		}
	}
	return nil
}

func (guard *attemptWorktreeRootGuard) Close() error {
	if guard == nil {
		return nil
	}
	var closeErrors []error
	for index := len(guard.registeredRoots) - 1; index >= 0; index-- {
		closeErrors = append(closeErrors, guard.registeredRoots[index].Close())
	}
	guard.registeredRoots = nil
	if guard.worktree != nil {
		closeErrors = append(closeErrors, guard.worktree.Close())
		guard.worktree = nil
	}
	if guard.common != nil {
		closeErrors = append(closeErrors, guard.common.Close())
		guard.common = nil
	}
	if guard.target != nil {
		closeErrors = append(closeErrors, guard.target.Close())
		guard.target = nil
	}
	return errors.Join(closeErrors...)
}

func rejectAttemptWorktreeRootOverlap(
	worktreeRoot string,
	worktree string,
	binding trustedWorktreeBinding,
	registered map[string]registeredWorktree,
) error {
	sensitive := []struct {
		role RootRole
		path string
	}{
		{role: RootRoleTarget, path: binding.root},
		{role: RootRoleGitCommon, path: binding.commonDir},
	}
	for _, registeredPath := range sortedRegisteredWorktreePaths(registered) {
		sensitive = append(sensitive, struct {
			role RootRole
			path string
		}{role: RootRoleRegisteredWorktree, path: registeredPath})
	}
	for _, root := range sensitive {
		if pathContains(root.path, worktreeRoot) ||
			pathContains(root.path, worktree) ||
			pathContains(worktree, root.path) {
			return unsafeAttemptWorktreeRootOverlap(
				worktreeRoot, worktree, root.role, root.path,
			)
		}
	}
	return nil
}

func unsafeAttemptWorktreeRootOverlap(
	worktreeRoot string,
	worktree string,
	role RootRole,
	path string,
) error {
	return fmt.Errorf(
		"unsafe attempt worktree overlap: worktree root %s (candidate %s) and %s root %s",
		worktreeRoot, worktree, role, path,
	)
}

func sortedRegisteredWorktreePaths(
	registered map[string]registeredWorktree,
) []string {
	paths := make([]string, 0, len(registered))
	for registeredPath := range registered {
		paths = append(paths, registeredPath)
	}
	sort.Strings(paths)
	return paths
}

func sameRegisteredWorktrees(
	left map[string]registeredWorktree,
	right map[string]registeredWorktree,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, record := range left {
		if confirmed, exists := right[path]; !exists || confirmed != record {
			return false
		}
	}
	return true
}
