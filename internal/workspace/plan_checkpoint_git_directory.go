package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

type planGitCommandAnchor struct {
	directory  string
	extraFiles []*os.File
}

type planCheckpointGitDirectory struct {
	root   *VerifiedRoot
	handle *os.File
	anchor planGitCommandAnchor
}

func newPlanCheckpointGitDirectory(
	root *VerifiedRoot,
) (*planCheckpointGitDirectory, error) {
	if root == nil || root.adapter == nil || root.Role() != RootRoleGitCommon {
		return nil, fmt.Errorf("verified plan Git directory is required")
	}
	handle, err := root.adapter.root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("retain plan Git directory handle: %w", err)
	}
	info, err := handle.Stat()
	if err != nil || !os.SameFile(root.info, info) {
		_ = handle.Close()
		if err == nil {
			err = fmt.Errorf("plan Git directory identity changed")
		}
		return nil, fmt.Errorf("verify retained plan Git directory: %w", err)
	}
	anchor, err := newPlanGitCommandAnchor(handle, root.identity)
	if err != nil {
		_ = handle.Close()
		return nil, err
	}
	directory := &planCheckpointGitDirectory{
		root: root, handle: handle, anchor: anchor,
	}
	if err := directory.Verify(); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func (directory *planCheckpointGitDirectory) Configure(command *exec.Cmd) error {
	if command == nil {
		return fmt.Errorf("plan Git command is required")
	}
	if err := directory.Verify(); err != nil {
		return err
	}
	command.Dir = directory.anchor.directory
	command.ExtraFiles = append([]*os.File(nil), directory.anchor.extraFiles...)
	return nil
}

func (directory *planCheckpointGitDirectory) Verify() error {
	if directory == nil || directory.root == nil || directory.handle == nil {
		return fmt.Errorf("plan Git directory is closed")
	}
	if err := directory.root.VerifyPath(); err != nil {
		return err
	}
	info, err := directory.handle.Stat()
	if err != nil {
		return fmt.Errorf("inspect retained plan Git directory: %w", err)
	}
	if !os.SameFile(directory.root.info, info) {
		return fmt.Errorf("retained plan Git directory identity changed")
	}
	return nil
}

func (directory *planCheckpointGitDirectory) Close() error {
	if directory == nil {
		return nil
	}
	var handleErr, rootErr error
	if directory.handle != nil {
		handleErr = directory.handle.Close()
		directory.handle = nil
	}
	if directory.root != nil {
		rootErr = directory.root.Close()
		directory.root = nil
	}
	return errors.Join(handleErr, rootErr)
}
