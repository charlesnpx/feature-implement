//go:build linux

package workspace

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openFileDescriptorNoFollow(directory *os.File, name string, directorySource bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directorySource {
		flags |= unix.O_DIRECTORY
	}
	descriptor, err := unix.Openat(int(directory.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), name)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("wrap no-follow descriptor")
	}
	return file, nil
}

func renameFileDescriptorNoReplace(
	sourceDirectory *os.File,
	source string,
	destinationDirectory *os.File,
	destination string,
) error {
	return unix.Renameat2(
		int(sourceDirectory.Fd()), source,
		int(destinationDirectory.Fd()), destination,
		unix.RENAME_NOREPLACE,
	)
}

func linkFileDescriptorNoReplace(
	sourceDirectory *os.File,
	source string,
	destinationDirectory *os.File,
	destination string,
) error {
	return unix.Linkat(
		int(sourceDirectory.Fd()), source,
		int(destinationDirectory.Fd()), destination,
		0,
	)
}
