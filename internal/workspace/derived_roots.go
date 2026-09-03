package workspace

import (
	"fmt"
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
