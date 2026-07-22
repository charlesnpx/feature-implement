//go:build !darwin && !linux

package workspace

import (
	"fmt"
	"os"
)

func renameFileDescriptorNoReplace(_ *os.File, _ string, _ *os.File, _ string) error {
	return fmt.Errorf("atomic no-replace rename is unsupported on this platform")
}

func linkFileDescriptorNoReplace(_ *os.File, _ string, _ *os.File, _ string) error {
	return fmt.Errorf("atomic rooted hard links are unsupported on this platform")
}

func openFileDescriptorNoFollow(_ *os.File, _ string, _ bool) (*os.File, error) {
	return nil, fmt.Errorf("no-follow rooted descriptors are unsupported on this platform")
}
