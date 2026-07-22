//go:build !darwin && !linux

package workspace

import (
	"fmt"
	"os"
)

func renameFileDescriptorNoReplace(_ *os.File, _, _ string) error {
	return fmt.Errorf("atomic no-replace rename is unsupported on this platform")
}
