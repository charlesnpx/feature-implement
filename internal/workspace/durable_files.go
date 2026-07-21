package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

func atomicWriteSynchronized(path string, content []byte, permission os.FileMode) error {
	directory := filepath.Dir(path)
	if err := ensureSynchronizedDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "pending-write-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(permission); err != nil {
		return err
	}
	if err := writeAll(temporary, content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("publish synchronized file %s: %w", filepath.Base(path), err)
	}
	return nil
}

func removeSynchronized(path string) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncFileAndDirectory(path, directory string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(directory)
}
