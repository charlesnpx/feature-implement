//go:build darwin

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
	anchor := fmt.Sprintf("/.vol/%d/%d", identity.Device, identity.Inode)
	anchored, err := os.Stat(anchor)
	if err != nil {
		return "", fmt.Errorf("resolve local target directory identity path: %w", err)
	}
	retained, err := handle.Stat()
	if err != nil || !os.SameFile(retained, anchored) {
		if err == nil {
			err = fmt.Errorf("identity path does not resolve to the retained directory")
		}
		return "", fmt.Errorf("verify local target directory identity path: %w", err)
	}
	return anchor, nil
}
