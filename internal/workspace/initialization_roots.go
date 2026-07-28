package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceInitializationRootGuard retains the plan and target roots across
// command effects and binds the runtime root as soon as it exists. It permits
// a missing runtime candidate only so overlap can be rejected before the
// command creates any durable runtime state.
type WorkspaceInitializationRootGuard struct {
	plan                       *VerifiedRoot
	runtime                    *VerifiedRoot
	target                     *VerifiedRoot
	worktree                   *VerifiedRoot
	gitDirectory               *VerifiedRoot
	commonDirectory            *VerifiedRoot
	registered                 []*VerifiedRoot
	registeredPaths            []string
	registeredState            map[string]registeredWorktree
	inspectRegisteredWorktrees func() (map[string]registeredWorktree, error)
	targetSession              *localTargetGitSession
	runtimePath                string
}

func OpenWorkspaceInitializationRootGuard(
	planPath string,
	runtimePath string,
	targetPath string,
	worktreePath string,
) (*WorkspaceInitializationRootGuard, error) {
	runtimePath, err := normalizeInitializationRootPath(
		RootRoleRuntime, runtimePath,
	)
	if err != nil {
		return nil, err
	}
	targetPath, err = normalizeInitializationRootPath(RootRoleTarget, targetPath)
	if err != nil {
		return nil, err
	}
	worktreePath, err = normalizeInitializationRootPath(
		RootRoleWorktree, worktreePath,
	)
	if err != nil {
		return nil, err
	}
	target, err := OpenVerifiedRoot(RootRoleTarget, targetPath, false)
	if err != nil {
		return nil, fmt.Errorf("open workspace initialization target root: %w", err)
	}
	guard := &WorkspaceInitializationRootGuard{
		target: target, runtimePath: runtimePath,
	}
	closeGuard := true
	defer func() {
		if closeGuard {
			_ = guard.Close()
		}
	}()

	if strings.TrimSpace(planPath) != "" {
		planPath, err = normalizeInitializationRootPath(RootRolePlan, planPath)
		if err != nil {
			return nil, err
		}
		guard.plan, err = OpenVerifiedRoot(RootRolePlan, planPath, false)
		if err != nil {
			return nil, fmt.Errorf("open workspace initialization plan root: %w", err)
		}
	}
	guard.worktree, err = OpenVerifiedRoot(
		RootRoleWorktree, worktreePath, false,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"open workspace initialization worktree root: %w", err,
		)
	}
	guard.runtime, err = openOptionalInitializationRuntimeRoot(runtimePath)
	if err != nil {
		return nil, err
	}
	if err := guard.validateLayout(); err != nil {
		return nil, err
	}
	closeGuard = false
	return guard, nil
}

func (guard *WorkspaceInitializationRootGuard) WorktreeRootBinding() (
	WorkspaceWorktreeRootBinding,
	error,
) {
	if guard == nil || guard.worktree == nil {
		return WorkspaceWorktreeRootBinding{}, fmt.Errorf(
			"workspace initialization worktree root is unavailable",
		)
	}
	if err := guard.worktree.VerifyPath(); err != nil {
		return WorkspaceWorktreeRootBinding{}, err
	}
	return NewWorkspaceWorktreeRootBinding(guard.worktree.Path())
}

func (guard *WorkspaceInitializationRootGuard) bindLocalTarget(
	ctx context.Context,
	adapter LocalTargetGitAdapter,
	inspection LocalTargetInspection,
) (resultErr error) {
	if ctx == nil || guard == nil || guard.target == nil ||
		inspection.binding.IsZero() {
		return fmt.Errorf(
			"workspace initialization Git-root admission requires a target inspection",
		)
	}
	if guard.gitDirectory != nil || guard.commonDirectory != nil ||
		len(guard.registered) != 0 ||
		guard.inspectRegisteredWorktrees != nil ||
		guard.targetSession != nil {
		return fmt.Errorf("workspace initialization Git roots are already bound")
	}
	binding := inspection.binding
	if guard.target.Path() != binding.root {
		return fmt.Errorf(
			"workspace initialization target root does not match the inspected Git binding",
		)
	}
	gitDirectory, err := OpenVerifiedRoot(
		RootRoleGitDirectory, binding.gitDirectory, false,
	)
	if err != nil {
		return fmt.Errorf("open workspace initialization Git directory: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, gitDirectory.Close())
		}
	}()
	commonDirectory, err := OpenVerifiedRoot(
		RootRoleGitCommon, binding.commonDirectory, false,
	)
	if err != nil {
		return fmt.Errorf("open workspace initialization Git common directory: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, commonDirectory.Close())
		}
	}()
	if err := gitDirectory.VerifyPath(); err != nil {
		return fmt.Errorf("verify workspace initialization Git directory: %w", err)
	}
	if err := commonDirectory.VerifyPath(); err != nil {
		return fmt.Errorf("verify workspace initialization Git common directory: %w", err)
	}
	registeredPaths := sortedRegisteredWorktreePaths(
		inspection.registeredWorktrees,
	)
	registered := make([]*VerifiedRoot, 0, len(registeredPaths))
	defer func() {
		if resultErr == nil {
			return
		}
		for index := len(registered) - 1; index >= 0; index-- {
			resultErr = errors.Join(resultErr, registered[index].Close())
		}
	}()
	for _, registeredPath := range registeredPaths {
		info, err := os.Lstat(registeredPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf(
				"inspect registered worktree root %s: %w", registeredPath, err,
			)
		}
		if !info.IsDir() {
			return fmt.Errorf(
				"registered worktree root %s is not a directory", registeredPath,
			)
		}
		root, err := OpenVerifiedRoot(
			RootRoleRegisteredWorktree, registeredPath, false,
		)
		if err != nil {
			return fmt.Errorf(
				"open registered worktree root %s: %w", registeredPath, err,
			)
		}
		registered = append(registered, root)
	}
	session, err := adapter.openBoundSession(binding)
	if err != nil {
		return fmt.Errorf(
			"retain workspace initialization target Git session: %w", err,
		)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, session.Close())
		}
	}()
	guard.gitDirectory = gitDirectory
	guard.commonDirectory = commonDirectory
	guard.registered = registered
	guard.registeredPaths = registeredPaths
	guard.registeredState = cloneRegisteredWorktreeState(
		inspection.registeredWorktrees,
	)
	guard.inspectRegisteredWorktrees = func() (
		map[string]registeredWorktree,
		error,
	) {
		return session.inspectRegisteredWorktrees(ctx)
	}
	guard.targetSession = session
	if err := guard.validateLayout(); err != nil {
		guard.gitDirectory = nil
		guard.commonDirectory = nil
		guard.registered = nil
		guard.registeredPaths = nil
		guard.registeredState = nil
		guard.inspectRegisteredWorktrees = nil
		guard.targetSession = nil
		return err
	}
	return nil
}

func (guard *WorkspaceInitializationRootGuard) VerifyBeforeRuntimeCreation() error {
	if guard == nil || guard.target == nil || guard.runtimePath == "" {
		return fmt.Errorf("workspace initialization root guard is closed")
	}
	if err := guard.verifyHeldRoots(); err != nil {
		return err
	}
	if guard.runtime == nil {
		appeared, err := openOptionalInitializationRuntimeRoot(guard.runtimePath)
		if err != nil {
			return err
		}
		if appeared != nil {
			_ = appeared.Close()
			return fmt.Errorf(
				"runtime root %s appeared after workspace initialization admission",
				guard.runtimePath,
			)
		}
	}
	return guard.validateLayout()
}

func (guard *WorkspaceInitializationRootGuard) VerifyAfterRuntimeCreation() error {
	if err := guard.VerifyAfterEffects(); err != nil {
		return err
	}
	if guard.runtime == nil {
		return fmt.Errorf("initialized runtime root %s does not exist", guard.runtimePath)
	}
	return nil
}

// VerifyAfterEffects revalidates every held root and binds the runtime when an
// effect created it. Unlike VerifyAfterRuntimeCreation, it also accepts an
// absent runtime so callers can use it while returning from a failed effect.
func (guard *WorkspaceInitializationRootGuard) VerifyAfterEffects() error {
	if guard == nil || guard.target == nil || guard.runtimePath == "" {
		return fmt.Errorf("workspace initialization root guard is closed")
	}
	if err := guard.verifyHeldRoots(); err != nil {
		return err
	}
	observed, err := openOptionalInitializationRuntimeRoot(guard.runtimePath)
	if err != nil {
		return fmt.Errorf("open initialized runtime root: %w", err)
	}
	if observed == nil {
		return guard.validateLayout()
	}
	if guard.runtime != nil {
		if observed.Identity() != guard.runtime.Identity() {
			_ = observed.Close()
			return fmt.Errorf(
				"runtime root at %s was replaced during workspace initialization",
				guard.runtimePath,
			)
		}
		_ = observed.Close()
	} else {
		guard.runtime = observed
	}
	return guard.validateLayout()
}

func (guard *WorkspaceInitializationRootGuard) Close() error {
	if guard == nil {
		return nil
	}
	var closeErrors []error
	if guard.runtime != nil {
		closeErrors = append(closeErrors, guard.runtime.Close())
		guard.runtime = nil
	}
	if guard.worktree != nil {
		closeErrors = append(closeErrors, guard.worktree.Close())
		guard.worktree = nil
	}
	for index := len(guard.registered) - 1; index >= 0; index-- {
		closeErrors = append(closeErrors, guard.registered[index].Close())
	}
	guard.registered = nil
	guard.registeredPaths = nil
	guard.registeredState = nil
	guard.inspectRegisteredWorktrees = nil
	if guard.targetSession != nil {
		closeErrors = append(closeErrors, guard.targetSession.Close())
		guard.targetSession = nil
	}
	if guard.commonDirectory != nil {
		closeErrors = append(closeErrors, guard.commonDirectory.Close())
		guard.commonDirectory = nil
	}
	if guard.gitDirectory != nil {
		closeErrors = append(closeErrors, guard.gitDirectory.Close())
		guard.gitDirectory = nil
	}
	if guard.target != nil {
		closeErrors = append(closeErrors, guard.target.Close())
		guard.target = nil
	}
	if guard.plan != nil {
		closeErrors = append(closeErrors, guard.plan.Close())
		guard.plan = nil
	}
	return errors.Join(closeErrors...)
}

func (guard *WorkspaceInitializationRootGuard) verifyHeldRoots() error {
	roots := []*VerifiedRoot{
		guard.plan,
		guard.runtime,
		guard.target,
		guard.worktree,
		guard.gitDirectory,
		guard.commonDirectory,
	}
	roots = append(roots, guard.registered...)
	for _, root := range roots {
		if root == nil {
			continue
		}
		if err := root.VerifyPath(); err != nil {
			return err
		}
	}
	if guard.inspectRegisteredWorktrees != nil {
		observed, err := guard.inspectRegisteredWorktrees()
		if err != nil {
			return fmt.Errorf(
				"reinspect registered Git worktrees during workspace initialization: %w",
				err,
			)
		}
		if !sameRegisteredWorktrees(guard.registeredState, observed) {
			return fmt.Errorf(
				"registered Git worktree inventory changed during workspace initialization",
			)
		}
	}
	return nil
}

func (guard *WorkspaceInitializationRootGuard) validateLayout() error {
	if guard.target == nil || guard.worktree == nil {
		return fmt.Errorf(
			"workspace initialization requires verified target and worktree roots",
		)
	}
	protected := []*VerifiedRoot{
		guard.target,
		guard.gitDirectory,
		guard.commonDirectory,
	}
	protected = append(protected, guard.registered...)
	for _, root := range protected {
		if root == nil {
			continue
		}
		if rootsOverlap(root, guard.worktree) {
			return unsafeInitializationRootOverlap(
				root.Role(), root.Path(),
				guard.worktree.Role(), guard.worktree.Path(),
			)
		}
	}
	for _, registeredPath := range guard.registeredPaths {
		if guard.plan != nil &&
			initializationRootPathsOverlap(
				registeredPath, guard.plan.Path(),
			) {
			return unsafeInitializationRootOverlap(
				RootRoleRegisteredWorktree, registeredPath,
				guard.plan.Role(), guard.plan.Path(),
			)
		}
		if initializationRootPathsOverlap(
			registeredPath, guard.runtimePath,
		) {
			return unsafeInitializationRootOverlap(
				RootRoleRegisteredWorktree, registeredPath,
				RootRoleRuntime, guard.runtimePath,
			)
		}
		if initializationRootPathsOverlap(
			registeredPath, guard.worktree.Path(),
		) {
			return unsafeInitializationRootOverlap(
				RootRoleRegisteredWorktree, registeredPath,
				guard.worktree.Role(), guard.worktree.Path(),
			)
		}
	}
	for _, root := range protected {
		if root == nil {
			continue
		}
		if guard.plan != nil && rootsOverlap(root, guard.plan) {
			return unsafeInitializationRootOverlap(
				guard.plan.Role(), guard.plan.Path(),
				root.Role(), root.Path(),
			)
		}
		if guard.runtime != nil {
			if rootsOverlap(root, guard.runtime) {
				return unsafeInitializationRootOverlap(
					root.Role(), root.Path(),
					guard.runtime.Role(), guard.runtime.Path(),
				)
			}
			continue
		}
		if pathContains(root.Path(), guard.runtimePath) ||
			pathContains(guard.runtimePath, root.Path()) {
			return unsafeInitializationRootOverlap(
				root.Role(), root.Path(),
				RootRoleRuntime, guard.runtimePath,
			)
		}
	}
	if guard.plan != nil {
		if rootsOverlap(guard.plan, guard.worktree) {
			return unsafeInitializationRootOverlap(
				guard.plan.Role(), guard.plan.Path(),
				guard.worktree.Role(), guard.worktree.Path(),
			)
		}
		if guard.runtime != nil && rootsOverlap(guard.plan, guard.runtime) {
			return unsafeInitializationRootOverlap(
				guard.plan.Role(), guard.plan.Path(),
				guard.runtime.Role(), guard.runtime.Path(),
			)
		}
		if guard.runtime == nil &&
			(pathContains(guard.plan.Path(), guard.runtimePath) ||
				pathContains(guard.runtimePath, guard.plan.Path())) {
			return unsafeInitializationRootOverlap(
				guard.plan.Role(), guard.plan.Path(),
				RootRoleRuntime, guard.runtimePath,
			)
		}
	}
	if guard.runtime != nil {
		if rootsOverlap(guard.runtime, guard.worktree) {
			return unsafeInitializationRootOverlap(
				guard.runtime.Role(), guard.runtime.Path(),
				guard.worktree.Role(), guard.worktree.Path(),
			)
		}
	} else if initializationRootPathsOverlap(
		guard.runtimePath, guard.worktree.Path(),
	) {
		return unsafeInitializationRootOverlap(
			RootRoleRuntime, guard.runtimePath,
			guard.worktree.Role(), guard.worktree.Path(),
		)
	}
	return nil
}

func cloneRegisteredWorktreeState(
	source map[string]registeredWorktree,
) map[string]registeredWorktree {
	result := make(map[string]registeredWorktree, len(source))
	for worktreePath, worktree := range source {
		result[worktreePath] = worktree
	}
	return result
}

func initializationRootPathsOverlap(left string, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func unsafeInitializationRootOverlap(
	leftRole RootRole,
	leftPath string,
	rightRole RootRole,
	rightPath string,
) error {
	return fmt.Errorf(
		"unsafe workspace root overlap: %s root %s and %s root %s",
		leftRole, leftPath, rightRole, rightPath,
	)
}

func openOptionalInitializationRuntimeRoot(path string) (*VerifiedRoot, error) {
	root, err := OpenVerifiedRoot(RootRoleRuntime, path, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open workspace initialization runtime root: %w", err)
	}
	return root, nil
}

func normalizeInitializationRootPath(role RootRole, value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s root must be absolute", role)
	}
	canonical, err := canonicalizeTrustedRootPath(value)
	if err != nil {
		return "", err
	}
	volumeRoot := filepath.VolumeName(canonical) + string(filepath.Separator)
	if canonical == volumeRoot {
		return "", fmt.Errorf("%s root cannot be a filesystem volume root", role)
	}
	return canonical, nil
}
