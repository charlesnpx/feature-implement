package workspace

import (
	"fmt"
	"strings"
)

const maxGitBranchBytes = 240

// validateGitBranchSyntax validates a real named Git branch, such as the
// workspace feature branch. Attempts are detached and must not use it.
func validateGitBranchSyntax(branch string) error {
	if branch == "" || len(branch) > maxGitBranchBytes || strings.TrimSpace(branch) != branch ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\\x00") {
		return fmt.Errorf("invalid Git branch %q", branch)
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("invalid Git branch %q", branch)
		}
	}
	return nil
}
