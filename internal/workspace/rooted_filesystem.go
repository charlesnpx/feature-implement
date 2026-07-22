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
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
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

func (adapter *RootedFilesystemAdapter) renameFileNoReplace(source, destination string) error {
	return adapter.renamePathNoReplace(source, destination, false)
}

func (adapter *RootedFilesystemAdapter) renameDirectoryNoReplace(source, destination string) error {
	return adapter.renamePathNoReplace(source, destination, true)
}

func (adapter *RootedFilesystemAdapter) renamePathNoReplace(source, destination string, directorySource bool) error {
	sourceDirectory, err := adapter.openDirectoryExact(path.Dir(source))
	if err != nil {
		return err
	}
	defer sourceDirectory.Close()
	destinationDirectory, err := adapter.openDirectoryExact(path.Dir(destination))
	if err != nil {
		return err
	}
	defer destinationDirectory.Close()
	sourceBase := path.Base(source)
	destinationBase := path.Base(destination)
	info, exists, err := inspectRootEntryExact(sourceDirectory, sourceBase)
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
	openedSourceDirectory, err := sourceDirectory.Open(".")
	if err != nil {
		return fmt.Errorf("open rooted quarantine parent: %w", err)
	}
	defer openedSourceDirectory.Close()
	openedSource, err := openFileDescriptorNoFollow(openedSourceDirectory, sourceBase, directorySource)
	if err != nil {
		return fmt.Errorf("open rooted quarantine source %s without following links: %w", source, err)
	}
	defer openedSource.Close()
	openedInfo, err := openedSource.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		if err == nil {
			err = fmt.Errorf("source identity changed")
		}
		return fmt.Errorf("verify rooted quarantine source %s: %w", source, err)
	}
	if _, exists, err := inspectRootEntryExact(destinationDirectory, destinationBase); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("rooted quarantine path %s already exists", destination)
	}
	openedDestinationDirectory, err := destinationDirectory.Open(".")
	if err != nil {
		return fmt.Errorf("open rooted quarantine destination parent: %w", err)
	}
	defer openedDestinationDirectory.Close()
	if err := renameFileDescriptorNoReplace(
		openedSourceDirectory, sourceBase,
		openedDestinationDirectory, destinationBase,
	); err != nil {
		return fmt.Errorf("quarantine rooted file %s: %w", source, err)
	}
	movedInfo, movedExists, verifyErr := inspectRootEntryExact(destinationDirectory, destinationBase)
	if verifyErr != nil || !movedExists || !os.SameFile(openedInfo, movedInfo) {
		if verifyErr == nil {
			verifyErr = fmt.Errorf("quarantined identity changed")
		}
		restoreErr := restoreRootedPathNoReplace(
			openedDestinationDirectory, destinationBase,
			openedSourceDirectory, sourceBase,
		)
		if restoreErr != nil {
			return fmt.Errorf("verify quarantined rooted path %s: %w; restore moved path: %v", destination, verifyErr, restoreErr)
		}
		return fmt.Errorf("verify quarantined rooted path %s: %w", destination, verifyErr)
	}
	if err := syncRootHandle(sourceDirectory); err != nil {
		return err
	}
	if path.Dir(source) != path.Dir(destination) {
		return syncRootHandle(destinationDirectory)
	}
	return nil
}

func (adapter *RootedFilesystemAdapter) linkFileNoReplace(source, destination string) error {
	return adapter.linkFileNoReplaceVerified(source, "", destination)
}

func (adapter *RootedFilesystemAdapter) linkFileNoReplaceVerified(source, proof, destination string) error {
	sourceDirectory, err := adapter.openDirectoryExact(path.Dir(source))
	if err != nil {
		return err
	}
	defer sourceDirectory.Close()
	destinationDirectory, err := adapter.openDirectoryExact(path.Dir(destination))
	if err != nil {
		return err
	}
	defer destinationDirectory.Close()
	sourceBase := path.Base(source)
	destinationBase := path.Base(destination)
	info, exists, err := inspectRootEntryExact(sourceDirectory, sourceBase)
	if err != nil || !exists || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("activation source is missing or invalid")
		}
		return err
	}
	openedSourceDirectory, err := sourceDirectory.Open(".")
	if err != nil {
		return fmt.Errorf("open rooted link source parent: %w", err)
	}
	defer openedSourceDirectory.Close()
	openedSource, err := openFileDescriptorNoFollow(openedSourceDirectory, sourceBase, false)
	if err != nil {
		return fmt.Errorf("open rooted link source %s without following links: %w", source, err)
	}
	defer openedSource.Close()
	openedInfo, err := openedSource.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		if err == nil {
			err = fmt.Errorf("source identity changed")
		}
		return fmt.Errorf("verify rooted link source %s: %w", source, err)
	}
	if proof != "" {
		proofDirectory, err := adapter.openDirectoryExact(path.Dir(proof))
		if err != nil {
			return err
		}
		defer proofDirectory.Close()
		proofInfo, proofExists, err := inspectRootEntryExact(proofDirectory, path.Base(proof))
		if err != nil || !proofExists || !proofInfo.Mode().IsRegular() {
			if err == nil {
				err = fmt.Errorf("identity proof is missing or invalid")
			}
			return fmt.Errorf("inspect rooted link proof %s: %w", proof, err)
		}
		openedProofDirectory, err := proofDirectory.Open(".")
		if err != nil {
			return fmt.Errorf("open rooted link proof parent: %w", err)
		}
		defer openedProofDirectory.Close()
		openedProof, err := openFileDescriptorNoFollow(openedProofDirectory, path.Base(proof), false)
		if err != nil {
			return fmt.Errorf("open rooted link proof %s without following links: %w", proof, err)
		}
		defer openedProof.Close()
		openedProofInfo, err := openedProof.Stat()
		if err != nil || !os.SameFile(proofInfo, openedProofInfo) || !os.SameFile(openedInfo, openedProofInfo) {
			if err == nil {
				err = fmt.Errorf("link source does not match its identity proof")
			}
			return fmt.Errorf("verify rooted link proof %s: %w", proof, err)
		}
	}
	if _, exists, err := inspectRootEntryExact(destinationDirectory, destinationBase); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("rooted activation target %s already exists", destination)
	}
	openedDestinationDirectory, err := destinationDirectory.Open(".")
	if err != nil {
		return fmt.Errorf("open rooted link destination parent: %w", err)
	}
	defer openedDestinationDirectory.Close()
	if err := linkFileDescriptorNoReplace(
		openedSourceDirectory, sourceBase,
		openedDestinationDirectory, destinationBase,
	); err != nil {
		return fmt.Errorf("link rooted activation %s: %w", destination, err)
	}
	linkedInfo, linkedExists, verifyErr := inspectRootEntryExact(destinationDirectory, destinationBase)
	if verifyErr != nil || !linkedExists || !os.SameFile(openedInfo, linkedInfo) {
		if verifyErr == nil {
			verifyErr = fmt.Errorf("linked identity changed")
		}
		return fmt.Errorf("verify rooted activation link %s and preserve the unmatched destination: %w", destination, verifyErr)
	}
	return syncRootHandle(destinationDirectory)
}

func (adapter *RootedFilesystemAdapter) sameFile(left, right string) (bool, error) {
	leftDirectory, err := adapter.openDirectoryExact(path.Dir(left))
	if err != nil {
		return false, err
	}
	defer leftDirectory.Close()
	rightDirectory, err := adapter.openDirectoryExact(path.Dir(right))
	if err != nil {
		return false, err
	}
	defer rightDirectory.Close()
	leftInfo, leftExists, err := inspectRootEntryExact(leftDirectory, path.Base(left))
	if err != nil || !leftExists {
		return false, err
	}
	rightInfo, rightExists, err := inspectRootEntryExact(rightDirectory, path.Base(right))
	if err != nil || !rightExists {
		return false, err
	}
	if leftInfo.Mode()&os.ModeSymlink != 0 || rightInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("rooted identity comparison rejects symlinks")
	}
	if (!leftInfo.Mode().IsRegular() && !leftInfo.IsDir()) ||
		(!rightInfo.Mode().IsRegular() && !rightInfo.IsDir()) {
		return false, fmt.Errorf("rooted identity comparison requires regular files or directories")
	}
	openedLeftDirectory, err := leftDirectory.Open(".")
	if err != nil {
		return false, err
	}
	defer openedLeftDirectory.Close()
	openedLeft, err := openFileDescriptorNoFollow(openedLeftDirectory, path.Base(left), leftInfo.IsDir())
	if err != nil {
		return false, err
	}
	defer openedLeft.Close()
	openedLeftInfo, err := openedLeft.Stat()
	if err != nil || !os.SameFile(leftInfo, openedLeftInfo) {
		if err == nil {
			err = fmt.Errorf("left identity changed")
		}
		return false, err
	}
	openedRightDirectory, err := rightDirectory.Open(".")
	if err != nil {
		return false, err
	}
	defer openedRightDirectory.Close()
	openedRight, err := openFileDescriptorNoFollow(openedRightDirectory, path.Base(right), rightInfo.IsDir())
	if err != nil {
		return false, err
	}
	defer openedRight.Close()
	openedRightInfo, err := openedRight.Stat()
	if err != nil || !os.SameFile(rightInfo, openedRightInfo) {
		if err == nil {
			err = fmt.Errorf("right identity changed")
		}
		return false, err
	}
	return os.SameFile(openedLeftInfo, openedRightInfo), nil
}

func restoreRootedPathNoReplace(
	sourceDirectory *os.File,
	source string,
	destinationDirectory *os.File,
	destination string,
) error {
	return renameFileDescriptorNoReplace(sourceDirectory, source, destinationDirectory, destination)
}

func (adapter *RootedFilesystemAdapter) removeFileExact(
	relative string,
	proof string,
	beforeUnlink func() error,
) (bool, error) {
	return adapter.removeFileBound(relative, func(opened *os.File) error {
		proofRoot, err := adapter.openDirectoryExact(path.Dir(proof))
		if err != nil {
			return err
		}
		defer proofRoot.Close()
		proofBase := path.Base(proof)
		proofInfo, proofExists, err := inspectRootEntryExact(proofRoot, proofBase)
		if err != nil || !proofExists || !proofInfo.Mode().IsRegular() {
			if err == nil {
				err = fmt.Errorf("identity proof is missing or invalid")
			}
			return fmt.Errorf("inspect rooted removal proof %s: %w", proof, err)
		}
		openedProofRoot, err := proofRoot.Open(".")
		if err != nil {
			return fmt.Errorf("open rooted removal proof parent: %w", err)
		}
		defer openedProofRoot.Close()
		openedProof, err := openFileDescriptorNoFollow(openedProofRoot, proofBase, false)
		if err != nil {
			return fmt.Errorf("open rooted removal proof %s without following links: %w", proof, err)
		}
		defer openedProof.Close()
		openedInfo, err := opened.Stat()
		if err != nil {
			return err
		}
		openedProofInfo, err := openedProof.Stat()
		if err != nil || !os.SameFile(proofInfo, openedProofInfo) || !os.SameFile(openedInfo, openedProofInfo) {
			if err == nil {
				err = fmt.Errorf("removal target does not match its identity proof")
			}
			return fmt.Errorf("verify rooted removal proof %s: %w", proof, err)
		}
		return nil
	}, beforeUnlink)
}

func (adapter *RootedFilesystemAdapter) removeFileContentExact(
	relative string,
	expected []byte,
	maximum int64,
	beforeUnlink func() error,
) (bool, error) {
	return adapter.removeFileBound(relative, func(opened *os.File) error {
		content, err := readOpenedFileBounded(opened, maximum)
		if err != nil {
			return err
		}
		if string(content) != string(expected) {
			return fmt.Errorf("rooted removal target %s has unexpected content", relative)
		}
		return nil
	}, beforeUnlink)
}

func (adapter *RootedFilesystemAdapter) removeFileHashExact(
	relative string,
	expectedHash string,
	maximum int64,
	beforeUnlink func() error,
) (bool, error) {
	return adapter.removeFileBound(relative, func(opened *os.File) error {
		content, err := readOpenedFileBounded(opened, maximum)
		if err != nil {
			return err
		}
		if DigestBytes(content).String() != expectedHash {
			return fmt.Errorf("rooted removal target %s has an unexpected hash", relative)
		}
		return nil
	}, beforeUnlink)
}

func readOpenedFileBounded(opened *os.File, maximum int64) ([]byte, error) {
	info, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("opened rooted file exceeds its removal bound")
	}
	if _, err := opened.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	content, err := io.ReadAll(io.LimitReader(opened, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("opened rooted file exceeds its removal bound")
	}
	return content, nil
}

func (adapter *RootedFilesystemAdapter) removeFileBound(
	relative string,
	verify func(*os.File) error,
	beforeUnlink func() error,
) (bool, error) {
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return false, err
	}
	directory, err := adapter.openDirectoryExact(path.Dir(rooted.Relative()))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
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
		return false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("rooted path %s cannot be removed because it is not a regular file", relative)
	}
	openedDirectory, err := directory.Open(".")
	if err != nil {
		return false, fmt.Errorf("open rooted removal parent: %w", err)
	}
	defer openedDirectory.Close()
	opened, err := openFileDescriptorNoFollow(openedDirectory, base, false)
	if err != nil {
		return false, fmt.Errorf("open rooted removal target %s without following links: %w", relative, err)
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		if err == nil {
			err = fmt.Errorf("removal target identity changed")
		}
		return false, fmt.Errorf("verify rooted removal target %s: %w", relative, err)
	}
	if verify != nil {
		if err := verify(opened); err != nil {
			return false, err
		}
	}
	if beforeUnlink != nil {
		if err := beforeUnlink(); err != nil {
			return false, err
		}
	}
	currentInfo, currentExists, err := inspectRootEntryExact(directory, base)
	if err != nil || !currentExists || !os.SameFile(openedInfo, currentInfo) {
		if err == nil {
			err = fmt.Errorf("removal target was replaced and will be preserved")
		}
		return false, fmt.Errorf("revalidate rooted removal target %s: %w", relative, err)
	}
	if err := directory.Remove(base); err != nil {
		return false, fmt.Errorf("remove rooted file %s: %w", relative, err)
	}
	if err := syncRootHandle(directory); err != nil {
		return false, err
	}
	return true, nil
}

func (adapter *RootedFilesystemAdapter) removeEmptyDirectoryExact(
	relative string,
	beforeRemove func() error,
) (bool, error) {
	rooted, err := NewRootedPath(adapter.rootPath, relative)
	if err != nil {
		return false, err
	}
	directory, err := adapter.openDirectoryExact(path.Dir(rooted.Relative()))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
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
		return false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("rooted path %s is not a directory", relative)
	}
	openedDirectory, err := directory.Open(".")
	if err != nil {
		return false, fmt.Errorf("open rooted directory removal parent: %w", err)
	}
	defer openedDirectory.Close()
	opened, err := openFileDescriptorNoFollow(openedDirectory, base, true)
	if err != nil {
		return false, fmt.Errorf("open rooted directory removal target %s without following links: %w", relative, err)
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		if err == nil {
			err = fmt.Errorf("directory removal target identity changed")
		}
		return false, fmt.Errorf("verify rooted directory removal target %s: %w", relative, err)
	}
	entries, readErr := opened.ReadDir(1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, fmt.Errorf("read rooted directory removal target %s: %w", relative, readErr)
	}
	if len(entries) != 0 {
		return false, nil
	}
	if beforeRemove != nil {
		if err := beforeRemove(); err != nil {
			return false, err
		}
	}
	currentInfo, currentExists, err := inspectRootEntryExact(directory, base)
	if err != nil || !currentExists || !os.SameFile(openedInfo, currentInfo) {
		if err == nil {
			err = fmt.Errorf("directory removal target was replaced and will be preserved")
		}
		return false, fmt.Errorf("revalidate rooted directory removal target %s: %w", relative, err)
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
