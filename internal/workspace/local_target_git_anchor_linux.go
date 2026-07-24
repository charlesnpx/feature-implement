//go:build linux

package workspace

import (
	"fmt"
	"os"
)

func localTargetGitCommandAnchor(
	handle *os.File,
	identity PlatformFileIdentity,
) (string, error) {
	if handle == nil || identity.Device == 0 || identity.Inode == 0 {
		return "", fmt.Errorf("local target directory identity is unavailable")
	}
	// The parent process retains the directory descriptor for the complete Git
	// transaction. Referring to that descriptor through the parent's proc entry
	// avoids rediscovering a replacement at the original pathname.
	anchor := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), handle.Fd())
	anchored, err := os.Stat(anchor)
	if err != nil {
		return "", fmt.Errorf("resolve local target directory descriptor path: %w", err)
	}
	retained, err := handle.Stat()
	if err != nil || !os.SameFile(retained, anchored) {
		if err == nil {
			err = fmt.Errorf("descriptor path does not resolve to the retained directory")
		}
		return "", fmt.Errorf("verify local target directory descriptor path: %w", err)
	}
	return anchor, nil
}
