//go:build darwin

package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// canonicalizeTrustedRootPath resolves only the immutable, root-owned aliases
// installed by macOS itself. Caller-controlled symlinks remain in the path and
// are rejected by OpenVerifiedRoot's component-by-component no-follow walk.
func canonicalizeTrustedRootPath(rootPath string) (string, error) {
	rootPath = filepath.Clean(rootPath)
	aliases := []struct {
		alias  string
		target string
	}{
		{alias: "/var", target: "/private/var"},
		{alias: "/tmp", target: "/private/tmp"},
		{alias: "/etc", target: "/private/etc"},
	}
	for _, candidate := range aliases {
		if rootPath != candidate.alias &&
			!strings.HasPrefix(rootPath, candidate.alias+string(filepath.Separator)) {
			continue
		}
		info, err := os.Lstat(candidate.alias)
		if err != nil {
			return "", fmt.Errorf("inspect trusted macOS path alias %s: %w", candidate.alias, err)
		}
		identity, err := platformFileIdentity(info)
		if err != nil {
			return "", err
		}
		destination, err := os.Readlink(candidate.alias)
		if err != nil {
			return "", fmt.Errorf("read trusted macOS path alias %s: %w", candidate.alias, err)
		}
		if !filepath.IsAbs(destination) {
			destination = filepath.Join(filepath.Dir(candidate.alias), destination)
		}
		if info.Mode()&os.ModeSymlink == 0 || identity.Owner != 0 ||
			filepath.Clean(destination) != candidate.target {
			return "", fmt.Errorf("macOS path alias %s is not the expected root-owned alias", candidate.alias)
		}
		suffix := strings.TrimPrefix(rootPath, candidate.alias)
		return filepath.Clean(candidate.target + suffix), nil
	}
	return rootPath, nil
}
