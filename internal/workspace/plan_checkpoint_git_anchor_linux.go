//go:build linux

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
	return planGitCommandAnchor{
		directory:  fmt.Sprintf("/proc/self/fd/%d", handle.Fd()),
		extraFiles: []*os.File{handle},
	}, nil
}
