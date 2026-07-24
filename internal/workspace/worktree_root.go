package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkspaceWorktreeRootBinding is the immutable local filesystem authority
// from which every attempt worktree path is derived. The path is descriptive;
// the platform identity detects replacement of the opened directory.
type WorkspaceWorktreeRootBinding struct {
	path     string
	identity PlatformFileIdentity
}

func NewWorkspaceWorktreeRootBinding(
	path string,
	identity PlatformFileIdentity,
) (WorkspaceWorktreeRootBinding, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return WorkspaceWorktreeRootBinding{}, fmt.Errorf(
			"workspace worktree root must be absolute",
		)
	}
	if zeroPlatformFileIdentity(identity) {
		return WorkspaceWorktreeRootBinding{}, fmt.Errorf(
			"workspace worktree root requires a filesystem identity",
		)
	}
	if err := validateBoundedText("workspace worktree root", path, 4096); err != nil {
		return WorkspaceWorktreeRootBinding{}, err
	}
	return WorkspaceWorktreeRootBinding{path: path, identity: identity}, nil
}

func (binding WorkspaceWorktreeRootBinding) Path() string {
	return binding.path
}

func (binding WorkspaceWorktreeRootBinding) Identity() PlatformFileIdentity {
	return binding.identity
}

func (binding WorkspaceWorktreeRootBinding) IsZero() bool {
	return binding.path == "" || zeroPlatformFileIdentity(binding.identity)
}

func verifyWorkspaceWorktreeRootBinding(
	binding WorkspaceWorktreeRootBinding,
) error {
	if binding.IsZero() {
		return fmt.Errorf("workspace runtime has no verified worktree root")
	}
	root, err := OpenVerifiedRoot(RootRoleWorktree, binding.path, false)
	if err != nil {
		return fmt.Errorf("open workspace worktree root: %w", err)
	}
	defer root.Close()
	if root.Identity() != binding.identity {
		return fmt.Errorf(
			"workspace worktree root at %s was replaced",
			binding.path,
		)
	}
	return root.VerifyPath()
}
