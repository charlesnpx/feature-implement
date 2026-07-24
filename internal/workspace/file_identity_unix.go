//go:build darwin || linux

package workspace

import (
	"fmt"
	"os"
	"syscall"
)

type PlatformFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Owner  uint64 `json:"owner"`
}

func platformFileIdentity(info os.FileInfo) (PlatformFileIdentity, error) {
	if info == nil {
		return PlatformFileIdentity{}, fmt.Errorf("filesystem identity requires file information")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return PlatformFileIdentity{}, fmt.Errorf("filesystem identity is unsupported for %T", info.Sys())
	}
	return PlatformFileIdentity{
		Device: uint64(stat.Dev),
		Inode:  uint64(stat.Ino),
		Owner:  uint64(stat.Uid),
	}, nil
}

func currentFilesystemOwner() (uint64, error) {
	owner := os.Geteuid()
	if owner < 0 {
		return 0, fmt.Errorf("effective filesystem owner is unavailable")
	}
	return uint64(owner), nil
}

func platformFileLinkCount(info os.FileInfo) (uint64, error) {
	if info == nil {
		return 0, fmt.Errorf("filesystem link count requires file information")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("filesystem link count is unsupported for %T", info.Sys())
	}
	return uint64(stat.Nlink), nil
}
