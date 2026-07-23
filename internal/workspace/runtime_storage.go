package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

const (
	RuntimeFormatSchemaVersion    = 3
	RuntimeFormatFileName         = "feature.runtime.v3.json"
	RuntimeStateIdentityFileName  = "runtime-root.v3.json"
	RuntimeInitializationLockName = "runtime-initialize.v3.lock"
	MaxRuntimeIdentityMarkerBytes = 16 * 1024
)

const localRuntimeFormatKind = "feature_workspace_local_runtime"

var requiredRuntimeCapabilities = []string{
	"advisory-locking",
	"directory-synchronization",
	"effective-owner",
	"exclusive-creation",
	"hard-link-no-replace",
	"no-follow-open",
	"rename-no-replace",
}

type runtimeRootIdentityWire struct {
	SchemaVersion int                  `json:"schema_version"`
	Kind          string               `json:"kind"`
	Role          RootRole             `json:"role"`
	Identity      PlatformFileIdentity `json:"identity"`
	Capabilities  []string             `json:"capabilities"`
}

// RuntimeStorage retains independently verified handles for the selected
// runtime directory and its durable state directory.
type RuntimeStorage struct {
	workspaceDir string
	root         *VerifiedRoot
	state        *VerifiedRoot
}

func OpenRuntimeStorage(workspaceDir string, create bool) (*RuntimeStorage, error) {
	return openRuntimeStorageWithProbe(
		workspaceDir,
		create,
		func(root *VerifiedRoot) error { return root.ProbeDurability() },
	)
}

func openRuntimeStorageWithProbe(
	workspaceDir string,
	create bool,
	probe func(*VerifiedRoot) error,
) (*RuntimeStorage, error) {
	if probe == nil {
		return nil, fmt.Errorf("runtime storage capability probe is required")
	}
	workspaceDir = filepath.Clean(workspaceDir)
	if !filepath.IsAbs(workspaceDir) {
		return nil, fmt.Errorf("runtime storage requires an absolute runtime directory")
	}
	canonical, err := canonicalizeTrustedRootPath(workspaceDir)
	if err != nil {
		return nil, err
	}
	runtimeRoot, err := OpenVerifiedRoot(RootRoleRuntime, canonical, create)
	if err != nil {
		return nil, err
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = runtimeRoot.Close()
		}
	}()

	rootMarker, markerExists, err := loadRuntimeIdentityMarker(runtimeRoot, RuntimeFormatFileName)
	if err != nil {
		return nil, err
	}
	if !markerExists {
		if err := requireRuntimeInitializationCandidate(runtimeRoot, canonical, create); err != nil {
			return nil, err
		}
		initializationLock, _, err := runtimeRoot.openOwnedRegularFile(
			RuntimeInitializationLockName, os.O_RDWR, 0o600, true,
		)
		if err != nil {
			return nil, fmt.Errorf("open local runtime initialization lock: %w", err)
		}
		defer initializationLock.Close()
		if err := syscall.Flock(int(initializationLock.Fd()), syscall.LOCK_EX); err != nil {
			return nil, fmt.Errorf("acquire local runtime initialization lock: %w", err)
		}
		defer syscall.Flock(int(initializationLock.Fd()), syscall.LOCK_UN)
		if err := runtimeRoot.verifyOwnedRegularFile(RuntimeInitializationLockName, initializationLock); err != nil {
			return nil, fmt.Errorf("verify local runtime initialization lock: %w", err)
		}
		rootMarker, markerExists, err = loadRuntimeIdentityMarker(runtimeRoot, RuntimeFormatFileName)
		if err != nil {
			return nil, err
		}
		if !markerExists {
			initializable, err := runtimeRootInitializable(runtimeRoot)
			if err != nil {
				return nil, err
			}
			if !initializable {
				return nil, incompatibleRuntimeFormatError(canonical)
			}
		}
	} else if err := validateRuntimeIdentityMarker(runtimeRoot, rootMarker); err != nil {
		return nil, err
	} else if err := publishRuntimeIdentityMarker(runtimeRoot, RuntimeFormatFileName, rootMarker); err != nil {
		return nil, err
	}

	if create {
		if err := runtimeRoot.EnsureDirectory(WorkspaceStateDirectoryName, 0o700); err != nil {
			return nil, fmt.Errorf("create runtime state root: %w", err)
		}
	}
	statePath := filepath.Join(canonical, WorkspaceStateDirectoryName)
	stateRoot, err := OpenVerifiedRoot(RootRoleRuntime, statePath, false)
	if err != nil {
		return nil, fmt.Errorf("open runtime state root: %w", err)
	}
	closeState := true
	defer func() {
		if closeState {
			_ = stateRoot.Close()
		}
	}()

	stateMarker, stateMarkerExists, err := loadRuntimeIdentityMarker(stateRoot, RuntimeStateIdentityFileName)
	if err != nil {
		return nil, err
	}
	if !stateMarkerExists {
		initializable, err := runtimeStateRootInitializable(stateRoot)
		if err != nil {
			return nil, fmt.Errorf("inspect runtime state root: %w", err)
		}
		if markerExists || !create || !initializable {
			return nil, fmt.Errorf(
				"local v3 runtime state root %s has no identity marker; existing state was preserved",
				statePath,
			)
		}
	}
	if !markerExists || !stateMarkerExists {
		if err := probe(stateRoot); err != nil {
			return nil, fmt.Errorf("preflight runtime state capabilities: %w", err)
		}
	}
	if !stateMarkerExists {
		stateMarker, err = marshalRuntimeIdentityMarker(stateRoot)
		if err != nil {
			return nil, err
		}
		if err := publishRuntimeIdentityMarker(
			stateRoot, RuntimeStateIdentityFileName, stateMarker,
		); err != nil {
			return nil, fmt.Errorf("publish runtime state identity marker: %w", err)
		}
	} else if err := validateRuntimeIdentityMarker(stateRoot, stateMarker); err != nil {
		return nil, err
	} else if err := publishRuntimeIdentityMarker(stateRoot, RuntimeStateIdentityFileName, stateMarker); err != nil {
		return nil, err
	}
	if !markerExists {
		rootMarker, err = marshalRuntimeIdentityMarker(runtimeRoot)
		if err != nil {
			return nil, err
		}
		if publishErr := publishRuntimeIdentityMarker(
			runtimeRoot, RuntimeFormatFileName, rootMarker,
		); publishErr != nil {
			rootMarker, markerExists, err = loadRuntimeIdentityMarker(runtimeRoot, RuntimeFormatFileName)
			if err != nil {
				return nil, fmt.Errorf("publish local runtime format marker: %w", errors.Join(publishErr, err))
			}
			if !markerExists {
				return nil, fmt.Errorf("publish local runtime format marker: %w", publishErr)
			}
		}
	}
	storage := &RuntimeStorage{
		workspaceDir: canonical,
		root:         runtimeRoot,
		state:        stateRoot,
	}
	if err := storage.Verify(); err != nil {
		return nil, err
	}
	closeRoot = false
	closeState = false
	return storage, nil
}

func (storage *RuntimeStorage) WorkspaceDir() string {
	if storage == nil {
		return ""
	}
	return storage.workspaceDir
}

func (storage *RuntimeStorage) OpenStateDirectory(relative string, create bool) (*VerifiedRoot, error) {
	if err := storage.Verify(); err != nil {
		return nil, err
	}
	rooted, err := NewRootedPath(storage.state.Path(), relative)
	if err != nil {
		return nil, err
	}
	if create {
		if err := storage.state.EnsureDirectory(rooted.Relative(), 0o700); err != nil {
			return nil, err
		}
	}
	directoryPath := filepath.Join(storage.state.Path(), filepath.FromSlash(rooted.Relative()))
	root, err := OpenVerifiedRoot(RootRoleRuntime, directoryPath, false)
	if err != nil {
		return nil, err
	}
	if err := storage.Verify(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func (storage *RuntimeStorage) Verify() error {
	if storage == nil || storage.root == nil || storage.state == nil {
		return fmt.Errorf("runtime storage is closed")
	}
	if err := storage.root.VerifyPath(); err != nil {
		return err
	}
	if err := storage.state.VerifyPath(); err != nil {
		return err
	}
	return nil
}

func (storage *RuntimeStorage) Close() error {
	if storage == nil {
		return nil
	}
	var errs []error
	if storage.state != nil {
		errs = append(errs, storage.state.Close())
		storage.state = nil
	}
	if storage.root != nil {
		errs = append(errs, storage.root.Close())
		storage.root = nil
	}
	return errors.Join(errs...)
}

func requireRuntimeInitializationCandidate(
	root *VerifiedRoot,
	runtimePath string,
	create bool,
) error {
	initializable, err := runtimeRootInitializable(root)
	if err != nil {
		return err
	}
	if !create {
		return incompatibleRuntimeFormatError(runtimePath)
	}
	if initializable {
		return nil
	}
	_, initializationInProgress, err := root.adapter.inspectExact(
		RuntimeInitializationLockName,
	)
	if err != nil {
		return err
	}
	if !initializationInProgress {
		return incompatibleRuntimeFormatError(runtimePath)
	}
	return nil
}

func runtimeRootInitializable(root *VerifiedRoot) (bool, error) {
	entries, err := root.adapter.readDirectory(".")
	if err != nil {
		return false, fmt.Errorf("inspect runtime directory: %w", err)
	}
	if len(entries) == 0 {
		return true, nil
	}
	var stateEntry *rootedDirectoryEntry
	for index := range entries {
		entry := &entries[index]
		switch entry.name {
		case RuntimeInitializationLockName:
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.Mode().IsRegular() {
				return false, nil
			}
		case WorkspaceStateDirectoryName:
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.IsDir() {
				return false, nil
			}
			stateEntry = entry
		case RuntimeFormatFileName + ".pending":
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.Mode().IsRegular() {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	if stateEntry == nil {
		return true, nil
	}
	state, err := OpenVerifiedRoot(
		RootRoleRuntime,
		filepath.Join(root.Path(), WorkspaceStateDirectoryName),
		false,
	)
	if err != nil {
		return false, err
	}
	defer state.Close()
	return runtimeStateRootInitializable(state)
}

func runtimeStateRootInitializable(state *VerifiedRoot) (bool, error) {
	entries, err := state.adapter.readDirectory(".")
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return true, nil
	}
	for _, entry := range entries {
		switch entry.name {
		case RuntimeStateIdentityFileName:
			content, err := state.ReadBounded(
				RuntimeStateIdentityFileName,
				MaxRuntimeIdentityMarkerBytes,
			)
			if err != nil || validateRuntimeIdentityMarker(state, content) != nil {
				return false, err
			}
		case RuntimeStateIdentityFileName + ".pending":
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.Mode().IsRegular() {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

func incompatibleRuntimeFormatError(runtimePath string) error {
	return fmt.Errorf(
		"runtime directory %s has no local v3 format marker and may contain provider-oriented draft-v2 state; "+
			"regenerate in a new runtime directory; existing state was preserved",
		runtimePath,
	)
}

func loadRuntimeIdentityMarker(root *VerifiedRoot, name string) ([]byte, bool, error) {
	content, err := root.ReadBounded(name, MaxRuntimeIdentityMarkerBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s identity marker: %w", root.Role(), err)
	}
	if err := validateRuntimeIdentityMarker(root, content); err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func marshalRuntimeIdentityMarker(root *VerifiedRoot) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("runtime identity marker requires a verified root")
	}
	return json.Marshal(runtimeRootIdentityWire{
		SchemaVersion: RuntimeFormatSchemaVersion,
		Kind:          localRuntimeFormatKind,
		Role:          root.Role(),
		Identity:      root.Identity(),
		Capabilities:  append([]string(nil), requiredRuntimeCapabilities...),
	})
}

func validateRuntimeIdentityMarker(root *VerifiedRoot, content []byte) error {
	var wire runtimeRootIdentityWire
	if err := decodeStrictJSON(content, &wire); err != nil {
		return fmt.Errorf("decode %s root identity marker: %w", root.Role(), err)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, content) {
		return fmt.Errorf("%s root identity marker is not canonical JSON", root.Role())
	}
	if wire.SchemaVersion != RuntimeFormatSchemaVersion ||
		wire.Kind != localRuntimeFormatKind ||
		wire.Role != root.Role() ||
		!slices.Equal(wire.Capabilities, requiredRuntimeCapabilities) {
		return fmt.Errorf(
			"%s root identity marker is not local runtime format v%d",
			root.Role(), RuntimeFormatSchemaVersion,
		)
	}
	if wire.Identity != root.Identity() {
		return fmt.Errorf(
			"%s root identity does not match its local v3 marker "+
				"(expected device=%d inode=%d owner=%d, observed device=%d inode=%d owner=%d)",
			root.Role(),
			wire.Identity.Device, wire.Identity.Inode, wire.Identity.Owner,
			root.Identity().Device, root.Identity().Inode, root.Identity().Owner,
		)
	}
	return nil
}

func publishRuntimeIdentityMarker(root *VerifiedRoot, name string, expected []byte) error {
	if int64(len(expected)) > MaxRuntimeIdentityMarkerBytes {
		return fmt.Errorf("runtime identity marker %s exceeds its bound", name)
	}
	if current, exists, err := loadRuntimeIdentityMarker(root, name); err != nil {
		return err
	} else if exists {
		if !bytes.Equal(current, expected) {
			return fmt.Errorf("runtime identity marker %s has unexpected canonical bytes", name)
		}
		return removeRuntimeMarkerPending(root, name+".pending")
	}

	pending := name + ".pending"
	if _, exists, err := root.adapter.inspectExact(pending); err != nil {
		return err
	} else if exists {
		file, _, err := root.openOwnedRegularFile(pending, os.O_RDONLY, 0, false)
		if err != nil {
			return err
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil {
			return statErr
		}
		if closeErr != nil {
			return closeErr
		}
		current, readErr := root.ReadBounded(pending, MaxRuntimeIdentityMarkerBytes)
		if readErr == nil && bytes.Equal(current, expected) {
			if err := root.adapter.renameFileNoReplace(pending, name); err != nil {
				if published, publishedExists, loadErr := loadRuntimeIdentityMarker(root, name); loadErr == nil &&
					publishedExists && bytes.Equal(published, expected) {
					_, _ = root.adapter.removeFileIdentityExact(pending, info, root.VerifyPath)
					return root.VerifyPath()
				}
				return err
			}
			return root.VerifyPath()
		}
		removed, removeErr := root.adapter.removeFileIdentityExact(pending, info, root.VerifyPath)
		if removeErr != nil {
			return errors.Join(readErr, removeErr)
		}
		if !removed {
			return fmt.Errorf("partial runtime identity marker %s disappeared", pending)
		}
	}
	if err := root.adapter.writeFileExclusive(pending, expected, 0o600); err != nil {
		return err
	}
	if err := root.adapter.renameFileNoReplace(pending, name); err != nil {
		return err
	}
	published, exists, err := loadRuntimeIdentityMarker(root, name)
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(published, expected) {
		return fmt.Errorf("runtime identity marker %s publication did not match", name)
	}
	return root.VerifyPath()
}

func removeRuntimeMarkerPending(root *VerifiedRoot, pending string) error {
	info, exists, err := root.adapter.inspectExact(pending)
	if err != nil || !exists {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("runtime identity marker staging path %s is not a regular file", pending)
	}
	removed, err := root.adapter.removeFileIdentityExact(pending, info, root.VerifyPath)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("runtime identity marker staging path %s disappeared", pending)
	}
	return root.VerifyPath()
}
