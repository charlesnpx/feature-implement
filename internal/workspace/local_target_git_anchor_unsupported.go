//go:build !darwin && !linux

package workspace

import (
	"fmt"
	"os"
)

func localTargetGitCommandAnchor(
	*os.File,
	PlatformFileIdentity,
) (string, error) {
	return "", fmt.Errorf(
		"local target Git command anchoring is unsupported on this platform",
	)
}
