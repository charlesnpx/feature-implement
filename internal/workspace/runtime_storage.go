package workspace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
	RuntimeFormatSchemaVersion             = 3
	RuntimeFormatFileName                  = "feature.runtime.v3.json"
	RuntimeStateIdentityFileName           = "runtime-root.v3.json"
	RuntimeInitializationLockName          = "runtime-initialize.v3.lock"
	RuntimeCapabilityProbeIntentFileName   = "runtime-capability-probe.v3.intent.json"
	RuntimeCapabilityProbeDirectoryName    = "runtime-capability-probe.v3"
	MaxRuntimeIdentityMarkerBytes          = 16 * 1024
	maxRuntimeCapabilityProbeIntentBytes   = 16 * 1024
	runtimeCapabilityProbeIntentKind       = "runtime_capability_probe"
	runtimeInstanceIdentifierEncodedLength = 32
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
	SchemaVersion   int                  `json:"schema_version"`
	Kind            string               `json:"kind"`
	Boundary        string               `json:"boundary"`
	Role            RootRole             `json:"role"`
	Identity        PlatformFileIdentity `json:"identity"`
	RuntimeInstance string               `json:"runtime_instance"`
	RuntimeIdentity PlatformFileIdentity `json:"runtime_identity"`
	StateIdentity   PlatformFileIdentity `json:"state_identity"`
	Capabilities    []string             `json:"capabilities"`
}

type runtimeIdentityBinding struct {
	instanceID      string
	runtimeIdentity PlatformFileIdentity
	stateIdentity   PlatformFileIdentity
}

type runtimeCapabilityProbeIntentWire struct {
	SchemaVersion   int                  `json:"schema_version"`
	Kind            string               `json:"kind"`
	RuntimeInstance string               `json:"runtime_instance"`
	RuntimeIdentity PlatformFileIdentity `json:"runtime_identity"`
	StateIdentity   PlatformFileIdentity `json:"state_identity"`
	Directory       string               `json:"directory"`
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
		func(root *VerifiedRoot, directory string) error {
			return root.probeDurabilityAt(directory)
		},
	)
}

func openRuntimeStorageWithProbe(
	workspaceDir string,
	create bool,
	probe func(*VerifiedRoot, string) error,
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

	rootMarker, rootMarkerWire, markerExists, err := loadRuntimeIdentityMarker(
		runtimeRoot, RuntimeFormatFileName,
	)
	if err != nil {
		return nil, err
	}
	var initializationLock *os.File
	verifyInitializationLock := func(boundary string) error {
		if initializationLock == nil {
			return nil
		}
		if err := runtimeRoot.verifyOwnedRegularFile(
			RuntimeInitializationLockName, initializationLock,
		); err != nil {
			return fmt.Errorf(
				"verify local runtime initialization lock %s: %w",
				boundary, err,
			)
		}
		if err := runtimeRoot.VerifyPath(); err != nil {
			return fmt.Errorf(
				"verify local runtime root at initialization boundary %s: %w",
				boundary, err,
			)
		}
		return nil
	}
	runInitializationBoundary := func(boundary string, effect func() error) error {
		if err := verifyInitializationLock("before " + boundary); err != nil {
			return err
		}
		if err := effect(); err != nil {
			return err
		}
		return verifyInitializationLock("after " + boundary)
	}
	if !markerExists {
		if err := requireRuntimeInitializationCandidate(runtimeRoot, canonical, create); err != nil {
			return nil, err
		}
		initializationLock, _, err = runtimeRoot.openOwnedRegularFile(
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
		if err := verifyInitializationLock("after acquisition"); err != nil {
			return nil, err
		}
		rootMarker, rootMarkerWire, markerExists, err = loadRuntimeIdentityMarker(
			runtimeRoot, RuntimeFormatFileName,
		)
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
	}

	if create {
		if err := runInitializationBoundary("runtime state root creation", func() error {
			return runtimeRoot.EnsureDirectory(WorkspaceStateDirectoryName, 0o700)
		}); err != nil {
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

	stateMarker, stateMarkerWire, stateMarkerExists, err := loadRuntimeIdentityMarker(
		stateRoot, RuntimeStateIdentityFileName,
	)
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

	var binding runtimeIdentityBinding
	switch {
	case markerExists && stateMarkerExists:
		binding, err = validateRuntimeIdentityMarkerPair(
			runtimeRoot, rootMarkerWire, stateRoot, stateMarkerWire,
		)
		if err != nil {
			return nil, err
		}
	case markerExists:
		return nil, fmt.Errorf(
			"local v3 runtime state root %s has no identity marker; existing state was preserved",
			statePath,
		)
	case stateMarkerExists:
		binding = runtimeIdentityBindingFromWire(stateMarkerWire)
		if err := validateRuntimeIdentityBinding(runtimeRoot, stateRoot, binding); err != nil {
			return nil, err
		}
	default:
		if initializationLock == nil {
			return nil, fmt.Errorf("fresh local runtime initialization requires its held initialization lock")
		}
		binding, err = runRuntimeCapabilityProbe(
			runtimeRoot,
			stateRoot,
			probe,
			verifyInitializationLock,
		)
		if err != nil {
			return nil, fmt.Errorf("preflight runtime state capabilities: %w", err)
		}
	}

	if !stateMarkerExists {
		stateMarker, err = marshalRuntimeIdentityMarker(
			stateRoot, RuntimeStateIdentityFileName, binding,
		)
		if err != nil {
			return nil, err
		}
		if err := publishRuntimeIdentityMarker(
			stateRoot,
			RuntimeStateIdentityFileName,
			stateMarker,
			verifyInitializationLock,
		); err != nil {
			return nil, fmt.Errorf("publish runtime state identity marker: %w", err)
		}
		stateMarker, stateMarkerWire, stateMarkerExists, err = loadRuntimeIdentityMarker(
			stateRoot, RuntimeStateIdentityFileName,
		)
		if err != nil || !stateMarkerExists {
			if err == nil {
				err = fmt.Errorf("runtime state identity marker publication disappeared")
			}
			return nil, err
		}
	} else if err := publishRuntimeIdentityMarker(
		stateRoot,
		RuntimeStateIdentityFileName,
		stateMarker,
		verifyInitializationLock,
	); err != nil {
		return nil, err
	}
	if !markerExists {
		rootMarker, err = marshalRuntimeIdentityMarker(
			runtimeRoot, RuntimeFormatFileName, binding,
		)
		if err != nil {
			return nil, err
		}
		if publishErr := publishRuntimeIdentityMarker(
			runtimeRoot,
			RuntimeFormatFileName,
			rootMarker,
			verifyInitializationLock,
		); publishErr != nil {
			rootMarker, rootMarkerWire, markerExists, err = loadRuntimeIdentityMarker(
				runtimeRoot, RuntimeFormatFileName,
			)
			if err != nil {
				return nil, fmt.Errorf("publish local runtime format marker: %w", errors.Join(publishErr, err))
			}
			if !markerExists {
				return nil, fmt.Errorf("publish local runtime format marker: %w", publishErr)
			}
		}
		rootMarker, rootMarkerWire, markerExists, err = loadRuntimeIdentityMarker(
			runtimeRoot, RuntimeFormatFileName,
		)
		if err != nil || !markerExists {
			if err == nil {
				err = fmt.Errorf("local runtime format marker publication disappeared")
			}
			return nil, err
		}
	} else if err := publishRuntimeIdentityMarker(
		runtimeRoot,
		RuntimeFormatFileName,
		rootMarker,
		verifyInitializationLock,
	); err != nil {
		return nil, err
	}
	if _, err := validateRuntimeIdentityMarkerPair(
		runtimeRoot, rootMarkerWire, stateRoot, stateMarkerWire,
	); err != nil {
		return nil, err
	}
	if err := verifyInitializationLock("before successful initialization"); err != nil {
		return nil, err
	}
	storage := &RuntimeStorage{
		workspaceDir: canonical,
		root:         runtimeRoot,
		state:        stateRoot,
	}
	if err := storage.Verify(); err != nil {
		return nil, err
	}
	if err := verifyInitializationLock("at successful initialization return"); err != nil {
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
	_, rootMarker, rootMarkerExists, err := loadRuntimeIdentityMarker(
		storage.root, RuntimeFormatFileName,
	)
	if err != nil {
		return err
	}
	_, stateMarker, stateMarkerExists, err := loadRuntimeIdentityMarker(
		storage.state, RuntimeStateIdentityFileName,
	)
	if err != nil {
		return err
	}
	if !rootMarkerExists || !stateMarkerExists {
		return fmt.Errorf("runtime storage identity markers are incomplete")
	}
	if _, err := validateRuntimeIdentityMarkerPair(
		storage.root, rootMarker, storage.state, stateMarker,
	); err != nil {
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
			if err != nil {
				return false, err
			}
			if _, err := validateRuntimeIdentityMarker(
				state, RuntimeStateIdentityFileName, content,
			); err != nil {
				return false, nil
			}
		case RuntimeStateIdentityFileName + ".pending",
			RuntimeCapabilityProbeIntentFileName,
			RuntimeCapabilityProbeIntentFileName + ".pending":
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.Mode().IsRegular() {
				return false, nil
			}
		case RuntimeCapabilityProbeDirectoryName:
			if entry.info.Mode()&os.ModeSymlink != 0 || !entry.info.IsDir() {
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

func loadRuntimeIdentityMarker(
	root *VerifiedRoot,
	name string,
) ([]byte, runtimeRootIdentityWire, bool, error) {
	content, err := root.ReadBounded(name, MaxRuntimeIdentityMarkerBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, runtimeRootIdentityWire{}, false, nil
	}
	if err != nil {
		return nil, runtimeRootIdentityWire{}, false, fmt.Errorf(
			"read %s identity marker: %w", root.Role(), err,
		)
	}
	wire, err := validateRuntimeIdentityMarker(root, name, content)
	if err != nil {
		return nil, runtimeRootIdentityWire{}, false, err
	}
	return content, wire, true, nil
}

func marshalRuntimeIdentityMarker(
	root *VerifiedRoot,
	name string,
	binding runtimeIdentityBinding,
) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("runtime identity marker requires a verified root")
	}
	if err := validateRuntimeInstanceIdentifier(binding.instanceID); err != nil {
		return nil, err
	}
	expectedIdentity, err := runtimeMarkerBoundaryIdentity(name, binding)
	if err != nil {
		return nil, err
	}
	if root.Identity() != expectedIdentity {
		return nil, fmt.Errorf("runtime identity marker boundary does not match its verified root")
	}
	return json.Marshal(runtimeRootIdentityWire{
		SchemaVersion:   RuntimeFormatSchemaVersion,
		Kind:            localRuntimeFormatKind,
		Boundary:        name,
		Role:            root.Role(),
		Identity:        root.Identity(),
		RuntimeInstance: binding.instanceID,
		RuntimeIdentity: binding.runtimeIdentity,
		StateIdentity:   binding.stateIdentity,
		Capabilities:    append([]string(nil), requiredRuntimeCapabilities...),
	})
}

func validateRuntimeIdentityMarker(
	root *VerifiedRoot,
	name string,
	content []byte,
) (runtimeRootIdentityWire, error) {
	var wire runtimeRootIdentityWire
	if err := decodeStrictJSON(content, &wire); err != nil {
		return runtimeRootIdentityWire{}, fmt.Errorf(
			"decode %s root identity marker: %w", root.Role(), err,
		)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return runtimeRootIdentityWire{}, err
	}
	if !bytes.Equal(canonical, content) {
		return runtimeRootIdentityWire{}, fmt.Errorf(
			"%s root identity marker is not canonical JSON", root.Role(),
		)
	}
	if wire.SchemaVersion != RuntimeFormatSchemaVersion ||
		wire.Kind != localRuntimeFormatKind ||
		wire.Boundary != name ||
		wire.Role != root.Role() ||
		!slices.Equal(wire.Capabilities, requiredRuntimeCapabilities) {
		return runtimeRootIdentityWire{}, fmt.Errorf(
			"%s root identity marker is not local runtime format v%d",
			root.Role(), RuntimeFormatSchemaVersion,
		)
	}
	if err := validateRuntimeInstanceIdentifier(wire.RuntimeInstance); err != nil {
		return runtimeRootIdentityWire{}, fmt.Errorf(
			"%s root identity marker runtime instance: %w", root.Role(), err,
		)
	}
	binding := runtimeIdentityBindingFromWire(wire)
	expectedIdentity, err := runtimeMarkerBoundaryIdentity(name, binding)
	if err != nil {
		return runtimeRootIdentityWire{}, err
	}
	if wire.Identity != expectedIdentity {
		return runtimeRootIdentityWire{}, fmt.Errorf(
			"%s root identity marker does not match its declared runtime boundary",
			root.Role(),
		)
	}
	if wire.Identity != root.Identity() {
		return runtimeRootIdentityWire{}, fmt.Errorf(
			"%s root identity does not match its local v3 marker "+
				"(expected device=%d inode=%d owner=%d, observed device=%d inode=%d owner=%d)",
			root.Role(),
			wire.Identity.Device, wire.Identity.Inode, wire.Identity.Owner,
			root.Identity().Device, root.Identity().Inode, root.Identity().Owner,
		)
	}
	if wire.RuntimeIdentity == wire.StateIdentity ||
		wire.RuntimeIdentity.Owner != wire.StateIdentity.Owner {
		return runtimeRootIdentityWire{}, fmt.Errorf(
			"%s root identity marker has invalid runtime/state bindings", root.Role(),
		)
	}
	return wire, nil
}

func runtimeMarkerBoundaryIdentity(
	name string,
	binding runtimeIdentityBinding,
) (PlatformFileIdentity, error) {
	switch name {
	case RuntimeFormatFileName:
		return binding.runtimeIdentity, nil
	case RuntimeStateIdentityFileName:
		return binding.stateIdentity, nil
	default:
		return PlatformFileIdentity{}, fmt.Errorf(
			"unsupported runtime identity marker boundary %s", name,
		)
	}
}

func runtimeIdentityBindingFromWire(wire runtimeRootIdentityWire) runtimeIdentityBinding {
	return runtimeIdentityBinding{
		instanceID:      wire.RuntimeInstance,
		runtimeIdentity: wire.RuntimeIdentity,
		stateIdentity:   wire.StateIdentity,
	}
}

func validateRuntimeIdentityBinding(
	runtimeRoot *VerifiedRoot,
	stateRoot *VerifiedRoot,
	binding runtimeIdentityBinding,
) error {
	if runtimeRoot == nil || stateRoot == nil {
		return fmt.Errorf("runtime identity binding requires both verified roots")
	}
	if err := validateRuntimeInstanceIdentifier(binding.instanceID); err != nil {
		return err
	}
	if binding.runtimeIdentity != runtimeRoot.Identity() ||
		binding.stateIdentity != stateRoot.Identity() {
		return fmt.Errorf(
			"runtime and state roots do not match their immutable runtime binding",
		)
	}
	if binding.runtimeIdentity == binding.stateIdentity ||
		binding.runtimeIdentity.Owner != binding.stateIdentity.Owner {
		return fmt.Errorf("runtime identity binding has invalid root identities")
	}
	return nil
}

func validateRuntimeIdentityMarkerPair(
	runtimeRoot *VerifiedRoot,
	runtimeMarker runtimeRootIdentityWire,
	stateRoot *VerifiedRoot,
	stateMarker runtimeRootIdentityWire,
) (runtimeIdentityBinding, error) {
	runtimeBinding := runtimeIdentityBindingFromWire(runtimeMarker)
	stateBinding := runtimeIdentityBindingFromWire(stateMarker)
	if runtimeMarker.Boundary != RuntimeFormatFileName ||
		stateMarker.Boundary != RuntimeStateIdentityFileName ||
		runtimeBinding != stateBinding {
		return runtimeIdentityBinding{}, fmt.Errorf(
			"runtime and state identity markers do not share one immutable runtime binding",
		)
	}
	if err := validateRuntimeIdentityBinding(
		runtimeRoot, stateRoot, runtimeBinding,
	); err != nil {
		return runtimeIdentityBinding{}, err
	}
	return runtimeBinding, nil
}

func validateRuntimeInstanceIdentifier(value string) error {
	if len(value) != runtimeInstanceIdentifierEncodedLength ||
		value != strings.ToLower(value) {
		return fmt.Errorf("runtime instance identifier is invalid")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded)*2 != runtimeInstanceIdentifierEncodedLength {
		return fmt.Errorf("runtime instance identifier is invalid")
	}
	return nil
}

func newRuntimeIdentityBinding(
	runtimeRoot *VerifiedRoot,
	stateRoot *VerifiedRoot,
) (runtimeIdentityBinding, error) {
	random := make([]byte, runtimeInstanceIdentifierEncodedLength/2)
	if _, err := rand.Read(random); err != nil {
		return runtimeIdentityBinding{}, fmt.Errorf(
			"create runtime instance identifier: %w", err,
		)
	}
	binding := runtimeIdentityBinding{
		instanceID:      hex.EncodeToString(random),
		runtimeIdentity: runtimeRoot.Identity(),
		stateIdentity:   stateRoot.Identity(),
	}
	if err := validateRuntimeIdentityBinding(runtimeRoot, stateRoot, binding); err != nil {
		return runtimeIdentityBinding{}, err
	}
	return binding, nil
}

func runRuntimeCapabilityProbe(
	runtimeRoot *VerifiedRoot,
	stateRoot *VerifiedRoot,
	probe func(*VerifiedRoot, string) error,
	verifyInitializationLock func(string) error,
) (runtimeIdentityBinding, error) {
	binding, intent, err := prepareRuntimeCapabilityProbe(
		runtimeRoot, stateRoot, verifyInitializationLock,
	)
	if err != nil {
		return runtimeIdentityBinding{}, err
	}
	if _, exists, err := stateRoot.adapter.inspectExact(
		RuntimeCapabilityProbeDirectoryName,
	); err != nil {
		return runtimeIdentityBinding{}, err
	} else if exists {
		if err := runRuntimeStorageInitializationBoundary(
			verifyInitializationLock,
			"recovery of interrupted runtime capability probe",
			func() error {
				return stateRoot.adapter.removeDirectoryTreeExact(
					RuntimeCapabilityProbeDirectoryName,
				)
			},
		); err != nil {
			return runtimeIdentityBinding{}, err
		}
	}
	if verifyInitializationLock != nil {
		if err := verifyInitializationLock("before runtime capability probe"); err != nil {
			return runtimeIdentityBinding{}, err
		}
	}
	if probeErr := probe(stateRoot, RuntimeCapabilityProbeDirectoryName); probeErr != nil {
		cleanupErr := cleanupRuntimeCapabilityProbe(
			stateRoot, intent, verifyInitializationLock,
		)
		return runtimeIdentityBinding{}, errors.Join(probeErr, cleanupErr)
	}
	if verifyInitializationLock != nil {
		if err := verifyInitializationLock("after runtime capability probe"); err != nil {
			return runtimeIdentityBinding{}, err
		}
	}
	if err := cleanupRuntimeCapabilityProbe(
		stateRoot, intent, verifyInitializationLock,
	); err != nil {
		return runtimeIdentityBinding{}, err
	}
	return binding, nil
}

func prepareRuntimeCapabilityProbe(
	runtimeRoot *VerifiedRoot,
	stateRoot *VerifiedRoot,
	verifyInitializationLock func(string) error,
) (runtimeIdentityBinding, []byte, error) {
	if content, wire, exists, err := loadRuntimeCapabilityProbeIntent(
		runtimeRoot, stateRoot, RuntimeCapabilityProbeIntentFileName,
	); err != nil {
		return runtimeIdentityBinding{}, nil, err
	} else if exists {
		return runtimeIdentityBinding{
			instanceID:      wire.RuntimeInstance,
			runtimeIdentity: wire.RuntimeIdentity,
			stateIdentity:   wire.StateIdentity,
		}, content, nil
	}

	pending := RuntimeCapabilityProbeIntentFileName + ".pending"
	if _, exists, err := stateRoot.adapter.inspectExact(pending); err != nil {
		return runtimeIdentityBinding{}, nil, err
	} else if exists {
		content, wire, valid, readErr := loadRuntimeCapabilityProbeIntent(
			runtimeRoot, stateRoot, pending,
		)
		if readErr == nil && valid {
			if err := runRuntimeStorageInitializationBoundary(
				verifyInitializationLock,
				"recovery publication of runtime capability probe intent",
				func() error {
					return stateRoot.adapter.renameFileNoReplace(
						pending, RuntimeCapabilityProbeIntentFileName,
					)
				},
			); err != nil {
				return runtimeIdentityBinding{}, nil, err
			}
			return runtimeIdentityBinding{
				instanceID:      wire.RuntimeInstance,
				runtimeIdentity: wire.RuntimeIdentity,
				stateIdentity:   wire.StateIdentity,
			}, content, nil
		}
		if err := removePartialRuntimeCapabilityProbeIntent(
			stateRoot, pending, verifyInitializationLock,
		); err != nil {
			return runtimeIdentityBinding{}, nil, errors.Join(readErr, err)
		}
	}

	if _, exists, err := stateRoot.adapter.inspectExact(
		RuntimeCapabilityProbeDirectoryName,
	); err != nil {
		return runtimeIdentityBinding{}, nil, err
	} else if exists {
		return runtimeIdentityBinding{}, nil, fmt.Errorf(
			"runtime capability probe directory has no authenticated intent; existing state was preserved",
		)
	}

	binding, err := newRuntimeIdentityBinding(runtimeRoot, stateRoot)
	if err != nil {
		return runtimeIdentityBinding{}, nil, err
	}
	intent, err := json.Marshal(runtimeCapabilityProbeIntentWire{
		SchemaVersion:   RuntimeFormatSchemaVersion,
		Kind:            runtimeCapabilityProbeIntentKind,
		RuntimeInstance: binding.instanceID,
		RuntimeIdentity: binding.runtimeIdentity,
		StateIdentity:   binding.stateIdentity,
		Directory:       RuntimeCapabilityProbeDirectoryName,
	})
	if err != nil {
		return runtimeIdentityBinding{}, nil, err
	}
	if err := runRuntimeStorageInitializationBoundary(
		verifyInitializationLock,
		"write of runtime capability probe intent staging path",
		func() error {
			return stateRoot.adapter.writeFileExclusive(pending, intent, 0o600)
		},
	); err != nil {
		return runtimeIdentityBinding{}, nil, err
	}
	if err := runRuntimeStorageInitializationBoundary(
		verifyInitializationLock,
		"publication of runtime capability probe intent",
		func() error {
			return stateRoot.adapter.renameFileNoReplace(
				pending, RuntimeCapabilityProbeIntentFileName,
			)
		},
	); err != nil {
		return runtimeIdentityBinding{}, nil, err
	}
	published, _, exists, err := loadRuntimeCapabilityProbeIntent(
		runtimeRoot, stateRoot, RuntimeCapabilityProbeIntentFileName,
	)
	if err != nil {
		return runtimeIdentityBinding{}, nil, err
	}
	if !exists || !bytes.Equal(published, intent) {
		return runtimeIdentityBinding{}, nil, fmt.Errorf(
			"runtime capability probe intent publication did not match",
		)
	}
	return binding, intent, nil
}

func loadRuntimeCapabilityProbeIntent(
	runtimeRoot *VerifiedRoot,
	stateRoot *VerifiedRoot,
	name string,
) ([]byte, runtimeCapabilityProbeIntentWire, bool, error) {
	content, err := stateRoot.ReadBounded(name, maxRuntimeCapabilityProbeIntentBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, runtimeCapabilityProbeIntentWire{}, false, nil
	}
	if err != nil {
		return nil, runtimeCapabilityProbeIntentWire{}, false, fmt.Errorf(
			"read runtime capability probe intent: %w", err,
		)
	}
	var wire runtimeCapabilityProbeIntentWire
	if err := decodeStrictJSON(content, &wire); err != nil {
		return content, runtimeCapabilityProbeIntentWire{}, false, fmt.Errorf(
			"decode runtime capability probe intent: %w", err,
		)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return content, runtimeCapabilityProbeIntentWire{}, false, err
	}
	binding := runtimeIdentityBinding{
		instanceID:      wire.RuntimeInstance,
		runtimeIdentity: wire.RuntimeIdentity,
		stateIdentity:   wire.StateIdentity,
	}
	if !bytes.Equal(canonical, content) ||
		wire.SchemaVersion != RuntimeFormatSchemaVersion ||
		wire.Kind != runtimeCapabilityProbeIntentKind ||
		wire.Directory != RuntimeCapabilityProbeDirectoryName {
		return content, runtimeCapabilityProbeIntentWire{}, false, fmt.Errorf(
			"runtime capability probe intent is invalid",
		)
	}
	if err := validateRuntimeIdentityBinding(
		runtimeRoot, stateRoot, binding,
	); err != nil {
		return content, runtimeCapabilityProbeIntentWire{}, false, fmt.Errorf(
			"runtime capability probe intent: %w", err,
		)
	}
	return content, wire, true, nil
}

func removePartialRuntimeCapabilityProbeIntent(
	stateRoot *VerifiedRoot,
	name string,
	verifyInitializationLock func(string) error,
) error {
	file, _, err := stateRoot.openOwnedRegularFile(name, os.O_RDONLY, 0, false)
	if err != nil {
		return err
	}
	if err := stateRoot.verifyOwnedRegularFile(name, file); err != nil {
		_ = file.Close()
		return err
	}
	info, err := file.Stat()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	var removed bool
	if err := runRuntimeStorageInitializationBoundary(
		verifyInitializationLock,
		"cleanup of partial runtime capability probe intent",
		func() error {
			var removeErr error
			removed, removeErr = stateRoot.adapter.removeFileIdentityExact(
				name, info, stateRoot.VerifyPath,
			)
			return removeErr
		},
	); err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("partial runtime capability probe intent disappeared")
	}
	return nil
}

func cleanupRuntimeCapabilityProbe(
	stateRoot *VerifiedRoot,
	intent []byte,
	verifyInitializationLock func(string) error,
) error {
	if _, exists, err := stateRoot.adapter.inspectExact(
		RuntimeCapabilityProbeDirectoryName,
	); err != nil {
		return err
	} else if exists {
		if err := runRuntimeStorageInitializationBoundary(
			verifyInitializationLock,
			"cleanup of runtime capability probe directory",
			func() error {
				return stateRoot.adapter.removeDirectoryTreeExact(
					RuntimeCapabilityProbeDirectoryName,
				)
			},
		); err != nil {
			return err
		}
	}
	var removed bool
	if err := runRuntimeStorageInitializationBoundary(
		verifyInitializationLock,
		"cleanup of runtime capability probe intent",
		func() error {
			var removeErr error
			removed, removeErr = stateRoot.adapter.removeFileContentExact(
				RuntimeCapabilityProbeIntentFileName,
				intent,
				int64(len(intent)),
				stateRoot.VerifyPath,
			)
			return removeErr
		},
	); err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("runtime capability probe intent disappeared")
	}
	return nil
}

func publishRuntimeIdentityMarker(
	root *VerifiedRoot,
	name string,
	expected []byte,
	verifyInitializationLock func(string) error,
) error {
	if int64(len(expected)) > MaxRuntimeIdentityMarkerBytes {
		return fmt.Errorf("runtime identity marker %s exceeds its bound", name)
	}
	if current, _, exists, err := loadRuntimeIdentityMarker(root, name); err != nil {
		return err
	} else if exists {
		if !bytes.Equal(current, expected) {
			return fmt.Errorf("runtime identity marker %s has unexpected canonical bytes", name)
		}
		return runRuntimeStorageInitializationBoundary(
			verifyInitializationLock,
			"cleanup of "+name+" staging path",
			func() error { return removeRuntimeMarkerPending(root, name+".pending") },
		)
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
			if err := runRuntimeStorageInitializationBoundary(
				verifyInitializationLock,
				"recovery publication of "+name,
				func() error { return root.adapter.renameFileNoReplace(pending, name) },
			); err != nil {
				if published, _, publishedExists, loadErr := loadRuntimeIdentityMarker(root, name); loadErr == nil &&
					publishedExists && bytes.Equal(published, expected) {
					_ = runRuntimeStorageInitializationBoundary(
						verifyInitializationLock,
						"cleanup of concurrently published "+name,
						func() error {
							_, removeErr := root.adapter.removeFileIdentityExact(
								pending, info, root.VerifyPath,
							)
							return removeErr
						},
					)
					return root.VerifyPath()
				}
				return err
			}
			return root.VerifyPath()
		}
		var removed bool
		removeErr := runRuntimeStorageInitializationBoundary(
			verifyInitializationLock,
			"cleanup of partial "+name,
			func() error {
				var removeErr error
				removed, removeErr = root.adapter.removeFileIdentityExact(
					pending, info, root.VerifyPath,
				)
				return removeErr
			},
		)
		if removeErr != nil {
			return errors.Join(readErr, removeErr)
		}
		if !removed {
			return fmt.Errorf("partial runtime identity marker %s disappeared", pending)
		}
	}
	if err := runRuntimeStorageInitializationBoundary(
		verifyInitializationLock,
		"write of "+name+" staging path",
		func() error {
			return root.adapter.writeFileExclusive(pending, expected, 0o600)
		},
	); err != nil {
		return err
	}
	if err := runRuntimeStorageInitializationBoundary(
		verifyInitializationLock,
		"publication of "+name,
		func() error { return root.adapter.renameFileNoReplace(pending, name) },
	); err != nil {
		return err
	}
	published, _, exists, err := loadRuntimeIdentityMarker(root, name)
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(published, expected) {
		return fmt.Errorf("runtime identity marker %s publication did not match", name)
	}
	return root.VerifyPath()
}

func runRuntimeStorageInitializationBoundary(
	verifyInitializationLock func(string) error,
	boundary string,
	effect func() error,
) error {
	if verifyInitializationLock != nil {
		if err := verifyInitializationLock("before " + boundary); err != nil {
			return err
		}
	}
	if err := effect(); err != nil {
		return err
	}
	if verifyInitializationLock != nil {
		return verifyInitializationLock("after " + boundary)
	}
	return nil
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
