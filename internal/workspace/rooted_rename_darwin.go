//go:build darwin

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameFileDescriptorNoReplace(directory *os.File, source, destination string) error {
	return unix.RenameatxNp(
		int(directory.Fd()), source,
		int(directory.Fd()), destination,
		unix.RENAME_EXCL,
	)
}
