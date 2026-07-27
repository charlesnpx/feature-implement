//go:build !darwin

package workspace

import "path/filepath"

func canonicalizeTrustedRootPath(rootPath string) (string, error) {
	return filepath.Clean(rootPath), nil
}
