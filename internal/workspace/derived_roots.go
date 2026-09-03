package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	derivedRuntimeDirectorySuffix = ".feature-runtime"
	derivedAttemptRootSuffix      = "-attempt-worktrees"
)

// DerivedWorkspaceRuntimeDirectory returns the one runtime location for a
// bundle. The runtime is a sibling of the committed bundle so mutable state
// can never become a source artifact by accident.
func DerivedWorkspaceRuntimeDirectory(bundleRoot string) (string, error) {
	bundleRoot = filepath.Clean(strings.TrimSpace(bundleRoot))
	if !filepath.IsAbs(bundleRoot) {
		return "", fmt.Errorf("derived runtime requires an absolute bundle root")
	}
	canonical, err := canonicalizeTrustedRootPath(bundleRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(
		filepath.Dir(canonical),
		filepath.Base(canonical)+derivedRuntimeDirectorySuffix,
	), nil
}

// DerivedWorkspaceWorktreeRoot returns the one scratch-root location for a
// runtime. It is deliberately not stored in the journal: a runtime location
// already determines it.
func DerivedWorkspaceWorktreeRoot(runtimeRoot string) (string, error) {
	runtimeRoot = filepath.Clean(strings.TrimSpace(runtimeRoot))
	if !filepath.IsAbs(runtimeRoot) {
		return "", fmt.Errorf("derived worktree root requires an absolute runtime root")
	}
	canonical, err := canonicalizeTrustedRootPath(runtimeRoot)
	if err != nil {
		return "", err
	}
	return canonical + derivedAttemptRootSuffix, nil
}

func derivedWorkspaceWorktreeRootForJournal(journal *WorkspaceJournal) (string, error) {
	if journal == nil || journal.runtime == nil {
		return "", fmt.Errorf("workspace journal runtime is unavailable")
	}
	return DerivedWorkspaceWorktreeRoot(journal.workspaceDir)
}

// ValidateWorkspaceRuntimeRoot refuses a caller-selected runtime directory
// that would overwrite a source bundle or an unrelated Git repository.
func ValidateWorkspaceRuntimeRoot(runtimeRoot string) error {
	runtimeRoot = filepath.Clean(strings.TrimSpace(runtimeRoot))
	if !filepath.IsAbs(runtimeRoot) {
		return fmt.Errorf("workspace runtime root must be absolute")
	}
	if exists, err := rootedEntryExists(runtimeRoot, WorkspaceBundleFileName); err != nil {
		return fmt.Errorf("inspect configured workspace runtime root: %w", err)
	} else if exists {
		return fmt.Errorf("configured workspace runtime root is a workspace bundle root")
	}
	if exists, err := rootedEntryExists(runtimeRoot, ".git"); err != nil {
		return fmt.Errorf("inspect configured workspace runtime root: %w", err)
	} else if exists {
		return fmt.Errorf("configured workspace runtime root is a Git repository")
	}
	bareEntries := []string{"HEAD", "objects", "refs"}
	for _, entry := range bareEntries {
		exists, err := rootedEntryExists(runtimeRoot, entry)
		if err != nil {
			return fmt.Errorf("inspect configured workspace runtime root: %w", err)
		}
		if !exists {
			return nil
		}
	}
	return fmt.Errorf("configured workspace runtime root is a Git repository")
}

func rootedEntryExists(root, name string) (bool, error) {
	_, err := os.Lstat(filepath.Join(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
