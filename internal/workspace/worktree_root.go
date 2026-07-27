package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkspaceWorktreeRootBinding is the immutable local filesystem authority
// from which every attempt worktree path is derived. Replacement checks are
// made with opened roots at operation time; the durable binding stores the
// configured path only.
type WorkspaceWorktreeRootBinding struct {
	path string
}

func NewWorkspaceWorktreeRootBinding(
	path string,
) (WorkspaceWorktreeRootBinding, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return WorkspaceWorktreeRootBinding{}, fmt.Errorf(
			"workspace worktree root must be absolute",
		)
	}
	if err := validateBoundedText("workspace worktree root", path, 4096); err != nil {
		return WorkspaceWorktreeRootBinding{}, err
	}
	return WorkspaceWorktreeRootBinding{path: path}, nil
}

func (binding WorkspaceWorktreeRootBinding) Path() string {
	return binding.path
}

func (binding WorkspaceWorktreeRootBinding) IsZero() bool {
	return binding.path == ""
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
	return root.VerifyPath()
}
