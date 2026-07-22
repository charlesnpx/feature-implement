package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// RootedFilesystemAdapter confines every materialization operation to one
// already-open directory. os.Root provides the containment boundary; the
// explicit component walk additionally rejects symlink traversal and
// case/Unicode aliases instead of following them inside that boundary.
type RootedFilesystemAdapter struct {
	rootPath string
	root     *os.Root
}

func OpenRootedFilesystemAdapter(rootPath string) (*RootedFilesystemAdapter, error) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if !filepath.IsAbs(rootPath) {
		return nil, fmt.Errorf("rooted filesystem requires an absolute root")
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, fmt.Errorf("inspect rooted filesystem root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("rooted filesystem root %s is a symlink", rootPath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("rooted filesystem root %s is not a directory", rootPath)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open rooted filesystem: %w", err)
	}
	return &RootedFilesystemAdapter{rootPath: rootPath, root: root}, nil
}

func (adapter *RootedFilesystemAdapter) Root() string {
	if adapter == nil {
		return ""
	}
	return adapter.rootPath
}

func (adapter *RootedFilesystemAdapter) Close() error {
	if adapter == nil || adapter.root == nil {
		return nil
	}
	err := adapter.root.Close()
	adapter.root = nil
	if err != nil {
		return fmt.Errorf("close rooted filesystem: %w", err)
	}
	return nil
}

func (adapter *RootedFilesystemAdapter) ReadFile(ctx context.Context, rooted RootedPath) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	relative, err := adapter.relative(rooted)
	if err != nil {
		return nil, err
	}
	info, exists, err := adapter.inspectExact(relative)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, &os.PathError{Op: "read", Path: relative, Err: os.ErrNotExist}
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rooted path %s is not a regular file", relative)
	}
	content, err := adapter.root.ReadFile(filepath.FromSlash(relative))
	if err != nil {
		return nil, fmt.Errorf("read rooted path %s: %w", relative, err)
	}
	return content, nil
}

func (adapter *RootedFilesystemAdapter) Inspect(ctx context.Context, rooted RootedPath) (FileInfo, error) {
	if err := contextError(ctx); err != nil {
		return FileInfo{}, err
	}
	relative, err := adapter.relative(rooted)
	if err != nil {
		return FileInfo{}, err
	}
	info, exists, err := adapter.inspectExact(relative)
	if err != nil {
		return FileInfo{}, err
	}
	if !exists {
		return FileInfo{}, &os.PathError{Op: "inspect", Path: relative, Err: os.ErrNotExist}
	}
	return NewFileInfo(
		info.Size(), info.Mode().IsRegular(), info.Mode()&os.ModeSymlink != 0,
		uint32(info.Mode().Perm()),
	)
}

func (adapter *RootedFilesystemAdapter) relative(rooted RootedPath) (string, error) {
	if adapter == nil || adapter.root == nil {
		return "", fmt.Errorf("rooted filesystem is closed")
	}
	if rooted.Root() == "" || filepath.Clean(rooted.Root()) != adapter.rootPath {
		return "", fmt.Errorf("rooted path belongs to a different filesystem root")
	}
	return rooted.Relative(), nil
}

func (adapter *RootedFilesystemAdapter) rooted(relative string) (RootedPath, error) {
	return NewRootedPath(adapter.rootPath, relative)
}

type rootedDirectoryEntry struct {
	name string
	info os.FileInfo
}

func (adapter *RootedFilesystemAdapter) readDirectory(relative string) ([]rootedDirectoryEntry, error) {
	if adapter == nil || adapter.root == nil {
		return nil, fmt.Errorf("rooted filesystem is closed")
	}
	if relative == "" {
		relative = "."
	}
	if relative != "." {
		info, exists, err := adapter.inspectExact(relative)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, &os.PathError{Op: "readdir", Path: relative, Err: os.ErrNotExist}
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("rooted path %s is not a directory", relative)
		}
	}
	return adapter.readDirectoryUnchecked(relative)
}

func (adapter *RootedFilesystemAdapter) readDirectoryUnchecked(relative string) ([]rootedDirectoryEntry, error) {
	directory, err := adapter.root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, fmt.Errorf("open rooted directory %s: %w", relative, err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read rooted directory %s: %w", relative, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close rooted directory %s: %w", relative, closeErr)
	}
	result := make([]rootedDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		entryPath := entry.Name()
		if relative != "." {
			entryPath = path.Join(relative, entry.Name())
		}
		info, err := adapter.root.Lstat(filepath.FromSlash(entryPath))
		if err != nil {
			return nil, fmt.Errorf("inspect rooted directory entry %s: %w", entryPath, err)
		}
		result = append(result, rootedDirectoryEntry{name: entry.Name(), info: info})
	}
	return result, nil
}

// inspectExact performs an exact-spelling component walk. A case-folded or
// canonically equivalent spelling is a collision, not an alias for the target.
func (adapter *RootedFilesystemAdapter) inspectExact(relative string) (os.FileInfo, bool, error) {
	if adapter == nil || adapter.root == nil {
		return nil, false, fmt.Errorf("rooted filesystem is closed")
	}
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return nil, false, err
	}
	components := strings.Split(rooted.Relative(), "/")
	parent := "."
	for index, component := range components {
		entries, err := adapter.readDirectoryUnchecked(parent)
		if err != nil {
			return nil, false, err
		}
		exact := false
		aliases := make([]string, 0, 1)
		key := materializationCollisionKey(component)
		for _, entry := range entries {
			if entry.name == component {
				exact = true
			}
			if materializationCollisionKey(entry.name) == key {
				aliases = append(aliases, entry.name)
			}
		}
		if len(aliases) > 1 || (!exact && len(aliases) == 1) {
			requested := component
			if parent != "." {
				requested = path.Join(parent, component)
			}
			return nil, false, fmt.Errorf(
				"rooted path collision at %s: requested %q conflicts with %q",
				requested, component, strings.Join(aliases, ", "),
			)
		}
		if !exact {
			return nil, false, nil
		}
		current := component
		if parent != "." {
			current = path.Join(parent, component)
		}
		info, err := adapter.root.Lstat(filepath.FromSlash(current))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("inspect rooted path %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("rooted path %s traverses a symlink", current)
		}
		if index != len(components)-1 && !info.IsDir() {
			return nil, false, fmt.Errorf("rooted path prefix %s is not a directory", current)
		}
		if index == len(components)-1 {
			return info, true, nil
		}
		parent = current
	}
	return nil, false, nil
}

func (adapter *RootedFilesystemAdapter) readBounded(relative string, maximum int64) ([]byte, error) {
	info, exists, err := adapter.inspectExact(relative)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, &os.PathError{Op: "read", Path: relative, Err: os.ErrNotExist}
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rooted path %s is not a regular file", relative)
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("rooted file %s exceeds %d bytes", relative, maximum)
	}
	file, err := adapter.root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, fmt.Errorf("open rooted file %s: %w", relative, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read rooted file %s: %w", relative, err)
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("rooted file %s exceeds %d bytes", relative, maximum)
	}
	return content, nil
}

func (adapter *RootedFilesystemAdapter) makeDirectory(relative string, permission os.FileMode) (bool, error) {
	if err := adapter.requireParentDirectory(relative); err != nil {
		return false, err
	}
	info, exists, err := adapter.inspectExact(relative)
	if err != nil {
		return false, err
	}
	if exists {
		if !info.IsDir() {
			return false, fmt.Errorf("rooted path %s already exists and is not a directory", relative)
		}
		return false, nil
	}
	if err := adapter.root.Mkdir(filepath.FromSlash(relative), permission.Perm()); err != nil {
		return false, fmt.Errorf("create rooted directory %s: %w", relative, err)
	}
	if err := adapter.syncDirectory(path.Dir(relative)); err != nil {
		return false, err
	}
	return true, nil
}

func (adapter *RootedFilesystemAdapter) writeFileExclusive(relative string, content []byte, permission os.FileMode) error {
	if err := adapter.requireParentDirectory(relative); err != nil {
		return err
	}
	if _, exists, err := adapter.inspectExact(relative); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("rooted path %s already exists", relative)
	}
	file, err := adapter.root.OpenFile(
		filepath.FromSlash(relative), os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission.Perm(),
	)
	if err != nil {
		return fmt.Errorf("create rooted file %s: %w", relative, err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = adapter.root.Remove(filepath.FromSlash(relative))
		}
	}()
	if err := writeAll(file, content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("synchronize rooted file %s: %w", relative, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close rooted file %s: %w", relative, err)
	}
	remove = false
	return adapter.syncDirectory(path.Dir(relative))
}

func (adapter *RootedFilesystemAdapter) atomicWrite(relative string, content []byte, permission os.FileMode) error {
	if err := adapter.requireParentDirectory(relative); err != nil {
		return err
	}
	if info, exists, err := adapter.inspectExact(relative); err != nil {
		return err
	} else if exists && !info.Mode().IsRegular() {
		return fmt.Errorf("rooted path %s cannot be replaced because it is not a regular file", relative)
	}
	parent := path.Dir(relative)
	base := path.Base(relative)
	temporary, err := randomRootedTemporaryName(parent, base)
	if err != nil {
		return err
	}
	if err := adapter.writeFileExclusive(temporary, content, permission); err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = adapter.root.Remove(filepath.FromSlash(temporary))
		}
	}()
	if err := adapter.root.Rename(filepath.FromSlash(temporary), filepath.FromSlash(relative)); err != nil {
		return fmt.Errorf("activate rooted file %s: %w", relative, err)
	}
	remove = false
	return adapter.syncDirectory(parent)
}

func (adapter *RootedFilesystemAdapter) removeFile(relative string) error {
	info, exists, err := adapter.inspectExact(relative)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("rooted path %s cannot be removed because it is not a regular file", relative)
	}
	if err := adapter.root.Remove(filepath.FromSlash(relative)); err != nil {
		return fmt.Errorf("remove rooted file %s: %w", relative, err)
	}
	return adapter.syncDirectory(path.Dir(relative))
}

func (adapter *RootedFilesystemAdapter) removeEmptyDirectory(relative string) (bool, error) {
	info, exists, err := adapter.inspectExact(relative)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	if !info.IsDir() {
		return false, fmt.Errorf("rooted path %s is not a directory", relative)
	}
	if err := adapter.root.Remove(filepath.FromSlash(relative)); err != nil {
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrInvalid) || errors.Is(err, syscall.ENOTEMPTY) {
			return false, nil
		}
		return false, fmt.Errorf("remove rooted directory %s: %w", relative, err)
	}
	if err := adapter.syncDirectory(path.Dir(relative)); err != nil {
		return false, err
	}
	return true, nil
}

func (adapter *RootedFilesystemAdapter) requireParentDirectory(relative string) error {
	parent := path.Dir(relative)
	if parent == "." {
		return nil
	}
	info, exists, err := adapter.inspectExact(parent)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("rooted parent directory %s does not exist", parent)
	}
	if !info.IsDir() {
		return fmt.Errorf("rooted path prefix %s is not a directory", parent)
	}
	return nil
}

func (adapter *RootedFilesystemAdapter) syncDirectory(relative string) error {
	if relative == "" || relative == "." {
		relative = "."
	} else if info, exists, err := adapter.inspectExact(relative); err != nil {
		return err
	} else if !exists || !info.IsDir() {
		return fmt.Errorf("rooted sync path %s is not a directory", relative)
	}
	directory, err := adapter.root.Open(filepath.FromSlash(relative))
	if err != nil {
		return fmt.Errorf("open rooted directory %s for synchronization: %w", relative, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("synchronize rooted directory %s: %w", relative, err)
	}
	return nil
}

func randomRootedTemporaryName(parent, base string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate rooted temporary name: %w", err)
	}
	name := "materialization-write-" + hex.EncodeToString(random) + "-" + base
	if parent == "." {
		return name, nil
	}
	return path.Join(parent, name), nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
