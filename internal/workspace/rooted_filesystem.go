package workspace

import (
	"context"
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
// directory handle. Absolute roots and relative parents are opened one exact
// component at a time; every opened handle is compared with the no-follow
// observation that authorized it. Operations therefore neither follow static
// symlinks nor race a path walk against a replaced directory component.
type RootedFilesystemAdapter struct {
	rootPath string
	root     *os.Root
}

func OpenRootedFilesystemAdapter(rootPath string) (*RootedFilesystemAdapter, error) {
	return openRootedFilesystemAdapter(rootPath, false)
}

func openOrCreateRootedFilesystemAdapter(rootPath string) (*RootedFilesystemAdapter, error) {
	return openRootedFilesystemAdapter(rootPath, true)
}

func openRootedFilesystemAdapter(rootPath string, create bool) (*RootedFilesystemAdapter, error) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if !filepath.IsAbs(rootPath) {
		return nil, fmt.Errorf("rooted filesystem requires an absolute root")
	}
	volumeRoot := filepath.VolumeName(rootPath) + string(filepath.Separator)
	current, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return nil, fmt.Errorf("open filesystem volume root %s: %w", volumeRoot, err)
	}
	closeCurrent := true
	defer func() {
		if closeCurrent {
			_ = current.Close()
		}
	}()
	relative := strings.TrimPrefix(rootPath, volumeRoot)
	if relative == "" {
		closeCurrent = false
		return &RootedFilesystemAdapter{rootPath: rootPath, root: current}, nil
	}
	components := strings.Split(filepath.ToSlash(relative), "/")
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("rooted filesystem root %s has an invalid component", rootPath)
		}
		info, exists, err := inspectRootEntryExact(current, component)
		if err != nil {
			return nil, fmt.Errorf("inspect rooted filesystem component %s: %w", component, err)
		}
		if !exists {
			if !create {
				return nil, &os.PathError{Op: "open-root", Path: rootPath, Err: os.ErrNotExist}
			}
			if err := current.Mkdir(component, 0o755); err != nil {
				return nil, fmt.Errorf("create rooted filesystem component %s: %w", component, err)
			}
			if err := syncRootHandle(current); err != nil {
				return nil, err
			}
			info, exists, err = inspectRootEntryExact(current, component)
			if err != nil || !exists {
				if err == nil {
					err = os.ErrNotExist
				}
				return nil, fmt.Errorf("verify created rooted filesystem component %s: %w", component, err)
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("rooted filesystem root %s traverses symlink component %s", rootPath, component)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("rooted filesystem component %s is not a directory", component)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return nil, fmt.Errorf("open rooted filesystem component %s: %w", component, err)
		}
		openedInfo, err := next.Stat(".")
		if err != nil || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			if err == nil {
				err = fmt.Errorf("directory identity changed")
			}
			return nil, fmt.Errorf("verify rooted filesystem component %s: %w", component, err)
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, fmt.Errorf("close rooted filesystem ancestor: %w", err)
		}
		current = next
		if index == len(components)-1 {
			closeCurrent = false
			return &RootedFilesystemAdapter{rootPath: rootPath, root: current}, nil
		}
	}
	return nil, fmt.Errorf("rooted filesystem root %s could not be opened", rootPath)
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
	return adapter.readBounded(relative, int64(^uint64(0)>>1)-1)
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

type rootedDirectoryEntry struct {
	name string
	info os.FileInfo
}

func (adapter *RootedFilesystemAdapter) readDirectory(relative string) ([]rootedDirectoryEntry, error) {
	directory, err := adapter.openDirectoryExact(relative)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return readRootDirectoryEntries(directory)
}

func readRootDirectoryEntries(directory *os.Root) ([]rootedDirectoryEntry, error) {
	opened, err := directory.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open rooted directory: %w", err)
	}
	entries, readErr := opened.ReadDir(-1)
	closeErr := opened.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read rooted directory: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close rooted directory: %w", closeErr)
	}
	result := make([]rootedDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := directory.Lstat(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("inspect rooted directory entry %s: %w", entry.Name(), err)
		}
		result = append(result, rootedDirectoryEntry{name: entry.Name(), info: info})
	}
	return result, nil
}

func inspectRootEntryExact(directory *os.Root, name string) (os.FileInfo, bool, error) {
	entries, err := readRootDirectoryEntries(directory)
	if err != nil {
		return nil, false, err
	}
	exact := false
	aliases := make([]string, 0, 1)
	key := materializationCollisionKey(name)
	for _, entry := range entries {
		if entry.name == name {
			exact = true
		}
		if materializationCollisionKey(entry.name) == key {
			aliases = append(aliases, entry.name)
		}
	}
	if len(aliases) > 1 || (!exact && len(aliases) == 1) {
		return nil, false, fmt.Errorf(
			"rooted path collision: requested %q conflicts with %q",
			name, strings.Join(aliases, ", "),
		)
	}
	if !exact {
		return nil, false, nil
	}
	info, err := directory.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return info, true, nil
}

func (adapter *RootedFilesystemAdapter) openDirectoryExact(relative string) (*os.Root, error) {
	if adapter == nil || adapter.root == nil {
		return nil, fmt.Errorf("rooted filesystem is closed")
	}
	if relative == "" || relative == "." {
		return adapter.root.OpenRoot(".")
	}
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return nil, err
	}
	current, err := adapter.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(rooted.Relative(), "/") {
		info, exists, err := inspectRootEntryExact(current, component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if !exists {
			_ = current.Close()
			return nil, &os.PathError{Op: "open-directory", Path: relative, Err: os.ErrNotExist}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = current.Close()
			return nil, fmt.Errorf("rooted path %s traverses a symlink", relative)
		}
		if !info.IsDir() {
			_ = current.Close()
			return nil, fmt.Errorf("rooted path prefix %s is not a directory", relative)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		openedInfo, err := next.Stat(".")
		if err != nil || !os.SameFile(info, openedInfo) {
			_ = current.Close()
			_ = next.Close()
			if err == nil {
				err = fmt.Errorf("directory identity changed")
			}
			return nil, fmt.Errorf("verify rooted directory %s: %w", relative, err)
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, err
		}
		current = next
	}
	return current, nil
}

func (adapter *RootedFilesystemAdapter) inspectExact(relative string) (os.FileInfo, bool, error) {
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return nil, false, err
	}
	parent := path.Dir(rooted.Relative())
	directory, err := adapter.openDirectoryExact(parent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer directory.Close()
	info, exists, err := inspectRootEntryExact(directory, path.Base(rooted.Relative()))
	if err != nil || !exists {
		return info, exists, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("rooted path %s is a symlink", relative)
	}
	return info, true, nil
}

func (adapter *RootedFilesystemAdapter) readBounded(relative string, maximum int64) ([]byte, error) {
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return nil, err
	}
	parent := path.Dir(rooted.Relative())
	base := path.Base(rooted.Relative())
	directory, err := adapter.openDirectoryExact(parent)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	info, exists, err := inspectRootEntryExact(directory, base)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, &os.PathError{Op: "read", Path: relative, Err: os.ErrNotExist}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rooted path %s is not a regular file", relative)
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("rooted file %s exceeds %d bytes", relative, maximum)
	}
	file, err := directory.Open(base)
	if err != nil {
		return nil, fmt.Errorf("open rooted file %s: %w", relative, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		if err == nil {
			err = fmt.Errorf("file identity changed")
		}
		return nil, fmt.Errorf("verify rooted file %s: %w", relative, err)
	}
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
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return false, err
	}
	parent := path.Dir(rooted.Relative())
	base := path.Base(rooted.Relative())
	directory, err := adapter.openDirectoryExact(parent)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	info, exists, err := inspectRootEntryExact(directory, base)
	if err != nil {
		return false, err
	}
	if exists {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, fmt.Errorf("rooted path %s already exists and is not a directory", relative)
		}
		return false, nil
	}
	if err := directory.Mkdir(base, permission.Perm()); err != nil {
		return false, fmt.Errorf("create rooted directory %s: %w", relative, err)
	}
	if err := syncRootHandle(directory); err != nil {
		return false, err
	}
	return true, nil
}

func (adapter *RootedFilesystemAdapter) writeFileExclusive(relative string, content []byte, permission os.FileMode) error {
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return err
	}
	parent := path.Dir(rooted.Relative())
	base := path.Base(rooted.Relative())
	directory, err := adapter.openDirectoryExact(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	if _, exists, err := inspectRootEntryExact(directory, base); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("rooted path %s already exists", relative)
	}
	file, err := directory.OpenFile(base, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission.Perm())
	if err != nil {
		return fmt.Errorf("create rooted file %s: %w", relative, err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = directory.Remove(base)
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
	return syncRootHandle(directory)
}

// replaceFileFromTemporary atomically activates a deterministic sibling
// temporary that was durably recorded by the materialization transaction.
func (adapter *RootedFilesystemAdapter) replaceFileFromTemporary(relative, temporary string) error {
	if path.Dir(relative) != path.Dir(temporary) {
		return fmt.Errorf("rooted replacement requires sibling paths")
	}
	directory, err := adapter.openDirectoryExact(path.Dir(relative))
	if err != nil {
		return err
	}
	defer directory.Close()
	temporaryBase := path.Base(temporary)
	targetBase := path.Base(relative)
	temporaryInfo, exists, err := inspectRootEntryExact(directory, temporaryBase)
	if err != nil || !exists || !temporaryInfo.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("temporary file is missing or invalid")
		}
		return fmt.Errorf("activate rooted file %s: %w", relative, err)
	}
	if targetInfo, exists, err := inspectRootEntryExact(directory, targetBase); err != nil {
		return err
	} else if exists && (targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular()) {
		return fmt.Errorf("rooted path %s cannot be replaced because it is not a regular file", relative)
	}
	if err := directory.Rename(temporaryBase, targetBase); err != nil {
		return fmt.Errorf("activate rooted file %s: %w", relative, err)
	}
	return syncRootHandle(directory)
}

func (adapter *RootedFilesystemAdapter) renameFileNoReplace(source, destination string) error {
	return adapter.renamePathNoReplace(source, destination, false)
}

func (adapter *RootedFilesystemAdapter) renameDirectoryNoReplace(source, destination string) error {
	return adapter.renamePathNoReplace(source, destination, true)
}

func (adapter *RootedFilesystemAdapter) renamePathNoReplace(source, destination string, directorySource bool) error {
	if path.Dir(source) != path.Dir(destination) {
		return fmt.Errorf("rooted quarantine rename requires sibling paths")
	}
	directory, err := adapter.openDirectoryExact(path.Dir(source))
	if err != nil {
		return err
	}
	defer directory.Close()
	sourceBase := path.Base(source)
	destinationBase := path.Base(destination)
	info, exists, err := inspectRootEntryExact(directory, sourceBase)
	validSource := exists && info.Mode()&os.ModeSymlink == 0
	if directorySource {
		validSource = validSource && info.IsDir()
	} else {
		validSource = validSource && info.Mode().IsRegular()
	}
	if err != nil || !validSource {
		if err == nil {
			err = fmt.Errorf("source is missing or has the wrong type")
		}
		return fmt.Errorf("quarantine rooted file %s: %w", source, err)
	}
	if _, exists, err := inspectRootEntryExact(directory, destinationBase); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("rooted quarantine path %s already exists", destination)
	}
	opened, err := directory.Open(".")
	if err != nil {
		return fmt.Errorf("open rooted quarantine parent: %w", err)
	}
	defer opened.Close()
	if err := renameFileDescriptorNoReplace(opened, sourceBase, destinationBase); err != nil {
		return fmt.Errorf("quarantine rooted file %s: %w", source, err)
	}
	return syncRootHandle(directory)
}

func (adapter *RootedFilesystemAdapter) linkFileNoReplace(source, destination string) error {
	if path.Dir(source) != path.Dir(destination) {
		return fmt.Errorf("rooted activation link requires sibling paths")
	}
	directory, err := adapter.openDirectoryExact(path.Dir(source))
	if err != nil {
		return err
	}
	defer directory.Close()
	sourceBase := path.Base(source)
	destinationBase := path.Base(destination)
	info, exists, err := inspectRootEntryExact(directory, sourceBase)
	if err != nil || !exists || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("activation source is missing or invalid")
		}
		return err
	}
	if _, exists, err := inspectRootEntryExact(directory, destinationBase); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("rooted activation target %s already exists", destination)
	}
	if err := directory.Link(sourceBase, destinationBase); err != nil {
		return fmt.Errorf("link rooted activation %s: %w", destination, err)
	}
	return syncRootHandle(directory)
}

func (adapter *RootedFilesystemAdapter) sameFile(left, right string) (bool, error) {
	if path.Dir(left) != path.Dir(right) {
		return false, fmt.Errorf("rooted identity comparison requires sibling paths")
	}
	directory, err := adapter.openDirectoryExact(path.Dir(left))
	if err != nil {
		return false, err
	}
	defer directory.Close()
	leftInfo, leftExists, err := inspectRootEntryExact(directory, path.Base(left))
	if err != nil || !leftExists {
		return false, err
	}
	rightInfo, rightExists, err := inspectRootEntryExact(directory, path.Base(right))
	if err != nil || !rightExists {
		return false, err
	}
	if leftInfo.Mode()&os.ModeSymlink != 0 || rightInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("rooted identity comparison rejects symlinks")
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func (adapter *RootedFilesystemAdapter) removeFile(relative string) error {
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return err
	}
	directory, err := adapter.openDirectoryExact(path.Dir(rooted.Relative()))
	if err != nil {
		return err
	}
	defer directory.Close()
	base := path.Base(rooted.Relative())
	info, exists, err := inspectRootEntryExact(directory, base)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("rooted path %s cannot be removed because it is not a regular file", relative)
	}
	if err := directory.Remove(base); err != nil {
		return fmt.Errorf("remove rooted file %s: %w", relative, err)
	}
	return syncRootHandle(directory)
}

func (adapter *RootedFilesystemAdapter) removeEmptyDirectory(relative string) (bool, error) {
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return false, err
	}
	directory, err := adapter.openDirectoryExact(path.Dir(rooted.Relative()))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	defer directory.Close()
	base := path.Base(rooted.Relative())
	info, exists, err := inspectRootEntryExact(directory, base)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("rooted path %s is not a directory", relative)
	}
	if err := directory.Remove(base); err != nil {
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrInvalid) || errors.Is(err, syscall.ENOTEMPTY) {
			return false, nil
		}
		return false, fmt.Errorf("remove rooted directory %s: %w", relative, err)
	}
	if err := syncRootHandle(directory); err != nil {
		return false, err
	}
	return true, nil
}

func (adapter *RootedFilesystemAdapter) syncDirectory(relative string) error {
	directory, err := adapter.openDirectoryExact(relative)
	if err != nil {
		return err
	}
	defer directory.Close()
	return syncRootHandle(directory)
}

func syncRootHandle(directory *os.Root) error {
	opened, err := directory.Open(".")
	if err != nil {
		return fmt.Errorf("open rooted directory for synchronization: %w", err)
	}
	defer opened.Close()
	if err := opened.Sync(); err != nil {
		return fmt.Errorf("synchronize rooted directory: %w", err)
	}
	return nil
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
