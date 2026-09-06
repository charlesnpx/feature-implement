package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

const (
	RuntimeFormatSchemaVersion    = 9
	RuntimeFormatFileName         = "feature.runtime.v9.json"
	RuntimeInitializationLockName = "runtime-initialize.v9.lock"
	MaxRuntimeFormatMarkerBytes   = 16 * 1024
)

const localRuntimeFormatKind = "feature_workspace_local_runtime"

var requiredRuntimeCapabilities = []string{
	"advisory-locking",
	"directory-synchronization",
	"exclusive-creation",
	"no-follow-open",
	"rename-no-replace",
}

type runtimeFormatMarkerWire struct {
	SchemaVersion int      `json:"schema_version"`
	Kind          string   `json:"kind"`
	StateRoot     string   `json:"state_root"`
	Capabilities  []string `json:"capabilities"`
}

// RuntimeStorage retains independently verified handles for the selected
// runtime directory and its durable state directory. The handles are verified
// during each operation; the runtime marker records only the local format.
type RuntimeStorage struct {
	workspaceDir string
	root         *VerifiedRoot
	state        *VerifiedRoot
}

func OpenRuntimeStorage(workspaceDir string, create bool) (*RuntimeStorage, error) {
	workspaceDir = filepath.Clean(strings.TrimSpace(workspaceDir))
	if !filepath.IsAbs(workspaceDir) {
		return nil, fmt.Errorf("runtime storage requires an absolute runtime directory")
	}
	canonical, err := canonicalizeTrustedRootPath(workspaceDir)
	if err != nil {
		return nil, err
	}
	runtimeRoot, err := OpenVerifiedRoot(RootRoleRuntime, canonical, create)
	if err != nil {
		return nil, fmt.Errorf("open runtime root: %w", err)
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = runtimeRoot.Close()
		}
	}()

	if err := initializeRuntimeFormat(runtimeRoot, canonical, create); err != nil {
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

func initializeRuntimeFormat(root *VerifiedRoot, runtimePath string, create bool) error {
	if _, exists, err := loadRuntimeFormatMarker(root); err != nil {
		return incompatibleRuntimeFormatError(runtimePath)
	} else if exists {
		return nil
	}
	if !create {
		return incompatibleRuntimeFormatError(runtimePath)
	}
	initializable, err := runtimeRootInitializable(root)
	if err != nil || !initializable {
		if _, exists, markerErr := loadRuntimeFormatMarker(root); markerErr == nil && exists {
			return nil
		}
		return incompatibleRuntimeFormatError(runtimePath)
	}
	lock, _, err := root.openOwnedRegularFile(
		RuntimeInitializationLockName,
		os.O_RDWR,
		0o600,
		true,
	)
	if err != nil {
		return fmt.Errorf("open local runtime initialization lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire local runtime initialization lock: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if _, exists, err := loadRuntimeFormatMarker(root); err != nil {
		return incompatibleRuntimeFormatError(runtimePath)
	} else if exists {
		return nil
	}
	initializable, err = runtimeRootInitializable(root)
	if err != nil || !initializable {
		if _, exists, markerErr := loadRuntimeFormatMarker(root); markerErr == nil && exists {
			return nil
		}
		return incompatibleRuntimeFormatError(runtimePath)
	}
	if err := root.EnsureDirectory(WorkspaceStateDirectoryName, 0o700); err != nil {
		return fmt.Errorf("create runtime state root: %w", err)
	}
	marker, err := marshalRuntimeFormatMarker()
	if err != nil {
		return err
	}
	if err := root.WriteExclusive(RuntimeFormatFileName, marker, 0o600); err != nil {
		existing, exists, readErr := loadRuntimeFormatMarker(root)
		if readErr == nil && exists && bytes.Equal(existing, marker) {
			return nil
		}
		return fmt.Errorf("publish local runtime format marker: %w", errors.Join(err, readErr))
	}
	return nil
}

func (storage *RuntimeStorage) WorkspaceDir() string {
	if storage == nil {
		return ""
	}
	return storage.workspaceDir
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
	if _, exists, err := loadRuntimeFormatMarker(storage.root); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("runtime storage format marker is missing")
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

func runtimeRootInitializable(root *VerifiedRoot) (bool, error) {
	entries, err := root.adapter.readDirectory(".")
	if err != nil {
		return false, fmt.Errorf("inspect runtime directory: %w", err)
	}
	if len(entries) == 0 {
		return true, nil
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.name, runtimeStagePrefix) {
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.Mode().IsRegular() {
				return false, nil
			}
			continue
		}
		switch entry.name {
		case RuntimeInitializationLockName:
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.Mode().IsRegular() {
				return false, nil
			}
		case WorkspaceStateDirectoryName:
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.IsDir() {
				return false, nil
			}
			state, err := OpenVerifiedRoot(
				RootRoleRuntime,
				filepath.Join(root.Path(), WorkspaceStateDirectoryName),
				false,
			)
			if err != nil {
				return false, err
			}
			initializable, stateErr := runtimeStateRootInitializable(state)
			closeErr := state.Close()
			if stateErr != nil || closeErr != nil {
				return false, errors.Join(stateErr, closeErr)
			}
			if !initializable {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

func runtimeStateRootInitializable(state *VerifiedRoot) (bool, error) {
	entries, err := state.adapter.readDirectory(".")
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func incompatibleRuntimeFormatError(string) error {
	return fmt.Errorf(
		"runtime format is incompatible; regenerate from committed sources",
	)
}

// inspectRuntimeFormatForAdmission distinguishes a missing or empty runtime
// root from a fully readable current-format runtime without creating anything.
// A nonempty root that cannot be opened as the current format is always an
// incompatible prior runtime, not an uninitialized workspace.
func inspectRuntimeFormatForAdmission(runtimePath string) (bool, error) {
	runtimeRoot, err := OpenVerifiedRoot(RootRoleRuntime, runtimePath, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, incompatibleRuntimeFormatError(runtimePath)
	}
	defer runtimeRoot.Close()

	entries, err := runtimeRoot.adapter.readDirectory(".")
	if err != nil {
		return false, incompatibleRuntimeFormatError(runtimePath)
	}
	if len(entries) == 0 {
		return false, nil
	}
	if _, exists, markerErr := loadRuntimeFormatMarker(runtimeRoot); markerErr != nil || !exists {
		return false, incompatibleRuntimeFormatError(runtimePath)
	}
	storage, err := OpenRuntimeStorage(runtimeRoot.Path(), false)
	if err != nil {
		return false, incompatibleRuntimeFormatError(runtimePath)
	}
	if err := storage.Close(); err != nil {
		return false, incompatibleRuntimeFormatError(runtimePath)
	}
	return true, nil
}

func marshalRuntimeFormatMarker() ([]byte, error) {
	return json.Marshal(runtimeFormatMarkerWire{
		SchemaVersion: RuntimeFormatSchemaVersion,
		Kind:          localRuntimeFormatKind,
		StateRoot:     WorkspaceStateDirectoryName,
		Capabilities:  append([]string(nil), requiredRuntimeCapabilities...),
	})
}

func loadRuntimeFormatMarker(root *VerifiedRoot) ([]byte, bool, error) {
	content, err := root.ReadBounded(RuntimeFormatFileName, MaxRuntimeFormatMarkerBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read runtime format marker: %w", err)
	}
	var wire runtimeFormatMarkerWire
	if err := decodeStrictJSON(content, &wire); err != nil {
		return nil, false, fmt.Errorf("decode runtime format marker: %w", err)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return nil, false, err
	}
	if !bytes.Equal(canonical, content) {
		return nil, false, fmt.Errorf("runtime format marker is not canonical JSON")
	}
	if wire.SchemaVersion != RuntimeFormatSchemaVersion ||
		wire.Kind != localRuntimeFormatKind ||
		wire.StateRoot != WorkspaceStateDirectoryName ||
		!slices.Equal(wire.Capabilities, requiredRuntimeCapabilities) {
		return nil, false, fmt.Errorf(
			"runtime format marker is not local runtime format v%d",
			RuntimeFormatSchemaVersion,
		)
	}
	return content, true, nil
}
