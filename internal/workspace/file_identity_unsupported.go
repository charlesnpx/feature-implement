//go:build !darwin && !linux

package workspace

import (
	"fmt"
	"os"
)

type PlatformFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Owner  uint64 `json:"owner"`
}

func platformFileIdentity(os.FileInfo) (PlatformFileIdentity, error) {
	return PlatformFileIdentity{}, fmt.Errorf("filesystem identity is unsupported on this platform")
}

func currentFilesystemOwner() (uint64, error) {
	return 0, fmt.Errorf("filesystem ownership is unsupported on this platform")
}

func platformFileLinkCount(os.FileInfo) (uint64, error) {
	return 0, fmt.Errorf("filesystem link counts are unsupported on this platform")
}
