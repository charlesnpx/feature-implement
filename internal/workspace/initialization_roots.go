package workspace

import (
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
	plan        *VerifiedRoot
	runtime     *VerifiedRoot
	target      *VerifiedRoot
	runtimePath string
}

func OpenWorkspaceInitializationRootGuard(
	planPath string,
	runtimePath string,
	targetPath string,
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
	for _, root := range []*VerifiedRoot{guard.plan, guard.runtime, guard.target} {
		if root == nil {
			continue
		}
		if err := root.VerifyPath(); err != nil {
			return err
		}
	}
	return nil
}

func (guard *WorkspaceInitializationRootGuard) validateLayout() error {
	if guard.target == nil {
		return fmt.Errorf("workspace initialization requires a verified target root")
	}
	if guard.plan != nil && rootsOverlap(guard.plan, guard.target) {
		return unsafeInitializationRootOverlap(
			guard.plan.Role(), guard.plan.Path(),
			guard.target.Role(), guard.target.Path(),
		)
	}
	for _, root := range []*VerifiedRoot{guard.plan, guard.target} {
		if root == nil {
			continue
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
	return nil
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
