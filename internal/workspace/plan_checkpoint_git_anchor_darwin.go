//go:build darwin

package workspace

import (
	"fmt"
	"os"
)

func newPlanGitCommandAnchor(
	handle *os.File,
	identity PlatformFileIdentity,
) (planGitCommandAnchor, error) {
	if handle == nil || identity.Device == 0 || identity.Inode == 0 {
		return planGitCommandAnchor{}, fmt.Errorf("plan Git directory identity is unavailable")
	}
	directory := fmt.Sprintf("/.vol/%d/%d", identity.Device, identity.Inode)
	anchored, err := os.Stat(directory)
	if err != nil {
		return planGitCommandAnchor{}, fmt.Errorf("resolve plan Git directory identity path: %w", err)
	}
	retained, err := handle.Stat()
	if err != nil || !os.SameFile(retained, anchored) {
		if err == nil {
			err = fmt.Errorf("identity path does not resolve to the retained directory")
		}
		return planGitCommandAnchor{}, fmt.Errorf("verify plan Git directory identity path: %w", err)
	}
	return planGitCommandAnchor{directory: directory}, nil
}
