//go:build !darwin && !linux

package workspace

import (
	"fmt"
	"os"
)

func newPlanGitCommandAnchor(
	*os.File,
	PlatformFileIdentity,
) (planGitCommandAnchor, error) {
	return planGitCommandAnchor{}, fmt.Errorf(
		"plan Git directory command anchoring is unsupported on this platform",
	)
}
