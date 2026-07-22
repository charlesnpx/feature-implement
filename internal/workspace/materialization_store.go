package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

var materializationProcessMutex sync.Mutex

type materializationStateWire struct {
	SchemaVersion       int    `json:"schema_version"`
	Phase               string `json:"phase"`
	ActiveInventoryHash string `json:"active_inventory_hash"`
}

const (
	materializationPhaseBootstrap = "bootstrap"
	materializationPhaseActive    = "active"
)

type pendingMaterializationWriteWire struct {
	ArtifactID   string `json:"artifact_id"`
	Path         string `json:"path"`
	Disposition  string `json:"disposition"`
	Expected     string `json:"expected"`
	ExpectedHash string `json:"expected_hash"`
	NewHash      string `json:"new_hash"`
	StagePath    string `json:"stage_path"`
}

type pendingMaterializationDeleteWire struct {
	ArtifactID   string `json:"artifact_id"`
	Path         string `json:"path"`
	ExpectedHash string `json:"expected_hash"`
}

type pendingMaterializationWire struct {
	SchemaVersion         int                                `json:"schema_version"`
	TransactionID         string                             `json:"transaction_id"`
	PreviousInventoryHash string                             `json:"previous_inventory_hash"`
	NextInventoryHash     string                             `json:"next_inventory_hash"`
	NextInventory         materializationInventoryWire       `json:"next_inventory"`
	CreateDirectories     []string                           `json:"create_directories"`
	Writes                []pendingMaterializationWriteWire  `json:"writes"`
	Deletes               []pendingMaterializationDeleteWire `json:"deletes"`
	RemoveDirectories     []string                           `json:"remove_directories"`
}

type materializationControlState struct {
	state           materializationStateWire
	inventory       MaterializationInventory
	inventoryHash   Digest
	inventoryExists bool
}

type materializationComparison struct {
	nextInventory     MaterializationInventory
	createDirectories []string
	writes            []pendingMaterializationWriteWire
	deletes           []pendingMaterializationDeleteWire
	removeDirectories []string
	conflicts         []MaterializationConflict
}

// SynchronizeMaterialization compares a deterministic desired artifact set to
// the last activated ownership inventory and applies it through a recoverable
// pending transaction. Files outside that inventory are never claimed merely
// because they happen to exist under rootPath.
func SynchronizeMaterialization(
	rootPath string,
	generatorVersion string,
	artifacts []MaterializationArtifact,
	options MaterializationOptions,
) (MaterializationResult, error) {
	desired, err := prepareDesiredMaterialization(generatorVersion, artifacts)
	if err != nil {
		return MaterializationResult{}, err
	}
	rootPath, err = prepareMaterializationRoot(rootPath)
	if err != nil {
		return MaterializationResult{}, err
	}

	materializationProcessMutex.Lock()
	defer materializationProcessMutex.Unlock()

	adapter, err := OpenRootedFilesystemAdapter(rootPath)
	if err != nil {
		return MaterializationResult{}, err
	}
	defer adapter.Close()

	control, recovered, err := loadMaterializationControl(adapter, options)
	if err != nil {
		return MaterializationResult{}, err
	}
	comparison, err := compareMaterialization(adapter, control.inventory, control.inventoryExists, desired)
	if err != nil {
		return MaterializationResult{}, err
	}
	if len(comparison.conflicts) != 0 {
		sortMaterializationConflicts(comparison.conflicts)
		return MaterializationResult{}, MaterializationConflictError{conflicts: comparison.conflicts}
	}

	nextBytes, nextHash, err := marshalMaterializationInventory(comparison.nextInventory)
	if err != nil {
		return MaterializationResult{}, err
	}
	if control.inventoryExists && control.inventoryHash == nextHash &&
		len(comparison.createDirectories) == 0 && len(comparison.writes) == 0 &&
		len(comparison.deletes) == 0 && len(comparison.removeDirectories) == 0 {
		return MaterializationResult{inventory: comparison.nextInventory, recovered: recovered}, nil
	}

	pending, err := buildPendingMaterialization(control, comparison, nextHash)
	if err != nil {
		return MaterializationResult{}, err
	}
	if err := stagePendingMaterialization(adapter, pending, desired); err != nil {
		return MaterializationResult{}, err
	}
	if err := injectMaterialization(options, MaterializationFaultAfterStaging); err != nil {
		return MaterializationResult{}, err
	}
	if err := writePendingMaterialization(adapter, pending); err != nil {
		return MaterializationResult{}, err
	}
	if err := injectMaterialization(options, MaterializationFaultAfterPending); err != nil {
		return MaterializationResult{}, err
	}
	if err := applyPendingMaterialization(adapter, pending, control, nextBytes, nextHash, options); err != nil {
		return MaterializationResult{}, err
	}
	if err := cleanupPendingMaterialization(adapter); err != nil {
		return MaterializationResult{}, err
	}

	return resultFromPending(pending, comparison.nextInventory, recovered), nil
}

func prepareMaterializationRoot(rootPath string) (string, error) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if !filepath.IsAbs(rootPath) {
		return "", fmt.Errorf("materialization requires an absolute destination root")
	}
	base := filepath.Base(rootPath)
	if base == "." || base == string(filepath.Separator) || strings.HasPrefix(base, ".") || !norm.NFC.IsNormalString(base) {
		return "", fmt.Errorf("materialization destination %s has an unsafe final component", rootPath)
	}
	info, err := os.Lstat(rootPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("materialization destination %s is a symlink", rootPath)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("materialization destination %s is not a directory", rootPath)
		}
		return rootPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect materialization destination: %w", err)
	}
	parent := filepath.Dir(rootPath)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("materialization destination parent must already exist: %w", err)
	}
	if !parentInfo.IsDir() {
		return "", fmt.Errorf("materialization destination parent %s is not a directory", parent)
	}
	if err := os.Mkdir(rootPath, 0o755); err != nil {
		return "", fmt.Errorf("create materialization destination: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return "", fmt.Errorf("synchronize materialization destination parent: %w", err)
	}
	return rootPath, nil
}

func loadMaterializationControl(
	adapter *RootedFilesystemAdapter,
	options MaterializationOptions,
) (materializationControlState, bool, error) {
	stateExists, err := rootedPathExists(adapter, MaterializationStateFileName)
	if err != nil {
		return materializationControlState{}, false, newMaterializationCorruption("inspect state file: %v", err)
	}
	if !stateExists {
		entries, err := adapter.readDirectory(".")
		if err != nil {
			return materializationControlState{}, false, err
		}
		if len(entries) != 0 {
			for _, entry := range entries {
				for _, control := range materializationControlPaths {
					if materializationCollisionKey(entry.name) == materializationCollisionKey(control) {
						return materializationControlState{}, false, newMaterializationCorruption(
							"reserved control path %s exists without %s", entry.name, MaterializationStateFileName,
						)
					}
				}
			}
			conflicts := make([]MaterializationConflict, 0, len(entries))
			for _, entry := range entries {
				conflicts = append(conflicts, MaterializationConflict{
					kind: MaterializationConflictUnownedDestination, path: entry.name,
					detail: "destination is nonempty and has no ownership inventory",
				})
			}
			sortMaterializationConflicts(conflicts)
			return materializationControlState{}, false, MaterializationConflictError{conflicts: conflicts}
		}
		state := materializationStateWire{
			SchemaVersion:       MaterializationInventorySchemaVersion,
			Phase:               materializationPhaseBootstrap,
			ActiveInventoryHash: "",
		}
		if err := writeMaterializationState(adapter, state); err != nil {
			return materializationControlState{}, false, err
		}
		if err := injectMaterialization(options, MaterializationFaultAfterBootstrapState); err != nil {
			return materializationControlState{}, false, err
		}
	}

	state, err := readMaterializationState(adapter)
	if err != nil {
		return materializationControlState{}, false, err
	}
	control, err := readActiveMaterializationControl(adapter, state)
	if err != nil {
		return materializationControlState{}, false, err
	}
	pendingExists, err := rootedPathExists(adapter, MaterializationPendingFileName)
	if err != nil {
		return materializationControlState{}, false, newMaterializationCorruption("inspect pending transaction: %v", err)
	}
	recovered := false
	if pendingExists {
		pending, err := readPendingMaterialization(adapter)
		if err != nil {
			return materializationControlState{}, false, err
		}
		nextInventory, err := normalizeMaterializationInventoryWire(pending.NextInventory)
		if err != nil {
			return materializationControlState{}, false, err
		}
		nextBytes, nextHash, err := marshalMaterializationInventory(nextInventory)
		if err != nil {
			return materializationControlState{}, false, err
		}
		if nextHash.String() != pending.NextInventoryHash {
			return materializationControlState{}, false, newMaterializationCorruption("pending next-inventory hash does not match its canonical bytes")
		}
		if err := applyPendingMaterialization(adapter, pending, control, nextBytes, nextHash, MaterializationOptions{}); err != nil {
			return materializationControlState{}, false, fmt.Errorf("recover pending materialization: %w", err)
		}
		if err := cleanupPendingMaterialization(adapter); err != nil {
			return materializationControlState{}, false, err
		}
		recovered = true
		state, err = readMaterializationState(adapter)
		if err != nil {
			return materializationControlState{}, false, err
		}
		control, err = readActiveMaterializationControl(adapter, state)
		if err != nil {
			return materializationControlState{}, false, err
		}
	} else if err := cleanupOrphanMaterializationStaging(adapter); err != nil {
		return materializationControlState{}, false, err
	}

	if control.state.Phase == materializationPhaseBootstrap {
		entries, err := adapter.readDirectory(".")
		if err != nil {
			return materializationControlState{}, false, err
		}
		for _, entry := range entries {
			if entry.name != MaterializationStateFileName {
				return materializationControlState{}, false, newMaterializationCorruption(
					"bootstrap state contains untracked path %s without a pending transaction", entry.name,
				)
			}
		}
	}
	return control, recovered, nil
}

func readActiveMaterializationControl(
	adapter *RootedFilesystemAdapter,
	state materializationStateWire,
) (materializationControlState, error) {
	inventoryExists, err := rootedPathExists(adapter, MaterializationInventoryFileName)
	if err != nil {
		return materializationControlState{}, newMaterializationCorruption("inspect inventory: %v", err)
	}
	control := materializationControlState{state: state, inventoryExists: inventoryExists}
	if inventoryExists {
		content, err := adapter.readBounded(MaterializationInventoryFileName, MaxMaterializationControlBytes)
		if err != nil {
			return materializationControlState{}, newMaterializationCorruption("read inventory: %v", err)
		}
		inventory, err := parseMaterializationInventory(content)
		if err != nil {
			return materializationControlState{}, err
		}
		control.inventory = inventory
		control.inventoryHash = DigestBytes(content)
	}
	switch state.Phase {
	case materializationPhaseBootstrap:
		if state.ActiveInventoryHash != "" {
			return materializationControlState{}, newMaterializationCorruption("bootstrap state carries an active inventory hash")
		}
		// An inventory may already be active if a pending transaction crashed
		// between inventory activation and state activation.
		if inventoryExists {
			pendingExists, pendingErr := rootedPathExists(adapter, MaterializationPendingFileName)
			if pendingErr != nil || !pendingExists {
				return materializationControlState{}, newMaterializationCorruption("bootstrap state has an inventory without a pending transaction")
			}
		}
	case materializationPhaseActive:
		if !inventoryExists {
			return materializationControlState{}, newMaterializationCorruption("active ownership inventory is missing")
		}
		expected, err := ParseDigest(state.ActiveInventoryHash)
		if err != nil || expected.IsZero() {
			return materializationControlState{}, newMaterializationCorruption("active inventory hash is invalid")
		}
		if expected != control.inventoryHash {
			pendingExists, pendingErr := rootedPathExists(adapter, MaterializationPendingFileName)
			if pendingErr != nil || !pendingExists {
				return materializationControlState{}, newMaterializationCorruption(
					"active inventory bytes do not match state hash %s", expected,
				)
			}
		}
	default:
		return materializationControlState{}, newMaterializationCorruption("unsupported state phase %q", state.Phase)
	}
	return control, nil
}

func readMaterializationState(adapter *RootedFilesystemAdapter) (materializationStateWire, error) {
	content, err := adapter.readBounded(MaterializationStateFileName, 64*1024)
	if err != nil {
		return materializationStateWire{}, newMaterializationCorruption("read state: %v", err)
	}
	if err := rejectDuplicateJSONObjectKeys(content); err != nil {
		return materializationStateWire{}, newMaterializationCorruption("state JSON: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return materializationStateWire{}, newMaterializationCorruption("decode state fields: %v", err)
	}
	for _, required := range []string{"schema_version", "phase", "active_inventory_hash"} {
		if _, exists := fields[required]; !exists {
			return materializationStateWire{}, newMaterializationCorruption("state field %s is required", required)
		}
	}
	var state materializationStateWire
	if err := decodeStrictJSON(content, &state); err != nil {
		return materializationStateWire{}, newMaterializationCorruption("decode state: %v", err)
	}
	if state.SchemaVersion != MaterializationInventorySchemaVersion {
		return materializationStateWire{}, newMaterializationCorruption("unsupported state schema_version %d", state.SchemaVersion)
	}
	if state.Phase != materializationPhaseBootstrap && state.Phase != materializationPhaseActive {
		return materializationStateWire{}, newMaterializationCorruption("unsupported state phase %q", state.Phase)
	}
	return state, nil
}

func writeMaterializationState(adapter *RootedFilesystemAdapter, state materializationStateWire) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := adapter.atomicWrite(MaterializationStateFileName, content, 0o644); err != nil {
		return fmt.Errorf("write materialization state: %w", err)
	}
	return nil
}

func compareMaterialization(
	adapter *RootedFilesystemAdapter,
	previous MaterializationInventory,
	previousExists bool,
	desired desiredMaterialization,
) (materializationComparison, error) {
	comparison := materializationComparison{}
	previousByID := make(map[string]OwnedMaterializationArtifact, len(previous.artifacts))
	previousByPath := make(map[string]OwnedMaterializationArtifact, len(previous.artifacts))
	previousDirectories := make(map[string]struct{}, len(previous.directories))
	if previousExists {
		for _, artifact := range previous.artifacts {
			previousByID[artifact.id] = artifact
			previousByPath[artifact.path] = artifact
		}
		for _, directory := range previous.directories {
			previousDirectories[directory] = struct{}{}
		}
	}

	desiredPaths := make(map[string]MaterializationArtifact, len(desired.artifacts))
	desiredDirectorySet := make(map[string]struct{}, len(desired.directories))
	nextOwnedDirectories := make([]string, 0, len(desired.directories))
	for _, directory := range desired.directories {
		desiredDirectorySet[directory] = struct{}{}
		info, exists, err := adapter.inspectExact(directory)
		if err != nil {
			comparison.addUnsafeConflict("", directory, err.Error())
			continue
		}
		if exists {
			if !info.IsDir() {
				comparison.addUnsafeConflict("", directory, "required path prefix exists and is not a directory")
				continue
			}
			if _, owned := previousDirectories[directory]; owned {
				nextOwnedDirectories = append(nextOwnedDirectories, directory)
			}
			continue
		}
		comparison.createDirectories = append(comparison.createDirectories, directory)
		nextOwnedDirectories = append(nextOwnedDirectories, directory)
	}

	for _, artifact := range desired.artifacts {
		desiredPaths[artifact.path] = artifact
		info, exists, err := adapter.inspectExact(artifact.path)
		if err != nil {
			comparison.addUnsafeConflict(artifact.id, artifact.path, err.Error())
			continue
		}
		previousAtPath, pathWasOwned := previousByPath[artifact.path]
		previousByIdentity, identityWasOwned := previousByID[artifact.id]
		if !exists {
			if pathWasOwned || (identityWasOwned && previousByIdentity.path == artifact.path) {
				comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
					kind: MaterializationConflictMissingOwnedPath, artifactID: artifact.id, path: artifact.path,
					detail: "owned artifact is missing and will not be recreated",
				})
				continue
			}
			comparison.writes = append(comparison.writes, pendingMaterializationWriteWire{
				ArtifactID: artifact.id, Path: artifact.path, Disposition: "created",
				Expected: "absent", ExpectedHash: "", NewHash: artifact.hash.String(),
			})
			continue
		}
		if !info.Mode().IsRegular() {
			comparison.addUnsafeConflict(artifact.id, artifact.path, "desired artifact path is not a regular file")
			continue
		}
		current, err := readMaterializationHash(adapter, artifact.path)
		if err != nil {
			return materializationComparison{}, err
		}
		if !pathWasOwned {
			comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
				kind: MaterializationConflictUnownedPath, artifactID: artifact.id, path: artifact.path,
				detail: "desired artifact path already exists without ownership proof",
			})
			continue
		}
		if current == artifact.hash {
			continue
		}
		if current == previousAtPath.lastGeneratedHash {
			comparison.writes = append(comparison.writes, pendingMaterializationWriteWire{
				ArtifactID: artifact.id, Path: artifact.path, Disposition: "updated",
				Expected: "hash", ExpectedHash: current.String(), NewHash: artifact.hash.String(),
			})
			continue
		}
		comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
			kind: MaterializationConflictModifiedOwnedPath, artifactID: previousAtPath.id, path: artifact.path,
			detail: fmt.Sprintf("current hash %s differs from last-generated hash %s", current, previousAtPath.lastGeneratedHash),
		})
	}

	for _, artifact := range previous.artifacts {
		if _, remains := desiredPaths[artifact.path]; remains {
			continue
		}
		info, exists, err := adapter.inspectExact(artifact.path)
		if err != nil {
			comparison.addUnsafeConflict(artifact.id, artifact.path, err.Error())
			continue
		}
		if !exists {
			continue
		}
		if !info.Mode().IsRegular() {
			comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
				kind: MaterializationConflictModifiedOwnedPath, artifactID: artifact.id, path: artifact.path,
				detail: "stale owned path is no longer a regular file",
			})
			continue
		}
		current, err := readMaterializationHash(adapter, artifact.path)
		if err != nil {
			return materializationComparison{}, err
		}
		if current != artifact.lastGeneratedHash {
			comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
				kind: MaterializationConflictModifiedOwnedPath, artifactID: artifact.id, path: artifact.path,
				detail: fmt.Sprintf("stale file hash %s differs from last-generated hash %s", current, artifact.lastGeneratedHash),
			})
			continue
		}
		comparison.deletes = append(comparison.deletes, pendingMaterializationDeleteWire{
			ArtifactID: artifact.id, Path: artifact.path, ExpectedHash: artifact.lastGeneratedHash.String(),
		})
	}

	for _, directory := range previous.directories {
		if _, remains := desiredDirectorySet[directory]; !remains {
			info, exists, err := adapter.inspectExact(directory)
			if err != nil {
				comparison.addUnsafeConflict("", directory, err.Error())
				continue
			}
			if exists && !info.IsDir() {
				comparison.addUnsafeConflict("", directory, "stale owned directory path is no longer a directory")
				continue
			}
			comparison.removeDirectories = append(comparison.removeDirectories, directory)
		}
	}
	sortMaterializationDirectories(comparison.createDirectories, false)
	sortMaterializationDirectories(comparison.removeDirectories, true)
	sort.Slice(comparison.writes, func(i, j int) bool { return comparison.writes[i].Path < comparison.writes[j].Path })
	sort.Slice(comparison.deletes, func(i, j int) bool { return comparison.deletes[i].Path < comparison.deletes[j].Path })
	sort.Strings(nextOwnedDirectories)

	nextArtifacts := make([]OwnedMaterializationArtifact, 0, len(desired.artifacts))
	for _, artifact := range desired.artifacts {
		nextArtifacts = append(nextArtifacts, OwnedMaterializationArtifact{
			id: artifact.id, path: artifact.path, lastGeneratedHash: artifact.hash,
		})
	}
	comparison.nextInventory = MaterializationInventory{
		schemaVersion:    MaterializationInventorySchemaVersion,
		generatorVersion: desired.generatorVersion,
		artifacts:        nextArtifacts,
		directories:      nextOwnedDirectories,
	}
	return comparison, nil
}

func (comparison *materializationComparison) addUnsafeConflict(artifactID, relativePath, detail string) {
	comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
		kind: MaterializationConflictUnsafePath, artifactID: artifactID, path: relativePath, detail: detail,
	})
}

func buildPendingMaterialization(
	control materializationControlState,
	comparison materializationComparison,
	nextHash Digest,
) (pendingMaterializationWire, error) {
	pending := pendingMaterializationWire{
		SchemaVersion:     MaterializationInventorySchemaVersion,
		NextInventoryHash: nextHash.String(), NextInventory: comparison.nextInventory.wire(),
		CreateDirectories: append([]string{}, comparison.createDirectories...),
		Writes:            append([]pendingMaterializationWriteWire{}, comparison.writes...),
		Deletes:           append([]pendingMaterializationDeleteWire{}, comparison.deletes...),
		RemoveDirectories: append([]string{}, comparison.removeDirectories...),
	}
	if control.inventoryExists {
		pending.PreviousInventoryHash = control.inventoryHash.String()
	}
	for index := range pending.Writes {
		pending.Writes[index].StagePath = path.Join(
			MaterializationStagingDirectoryName,
			fmt.Sprintf("artifact-%06d.data", index),
		)
	}
	transactionID, err := pendingMaterializationTransactionID(pending)
	if err != nil {
		return pendingMaterializationWire{}, err
	}
	pending.TransactionID = transactionID.String()
	return pending, nil
}

func stagePendingMaterialization(
	adapter *RootedFilesystemAdapter,
	pending pendingMaterializationWire,
	desired desiredMaterialization,
) error {
	if len(pending.Writes) == 0 {
		return nil
	}
	if _, exists, err := adapter.inspectExact(MaterializationStagingDirectoryName); err != nil {
		return err
	} else if exists {
		return newMaterializationCorruption("staging directory already exists before staging a transaction")
	}
	if _, err := adapter.makeDirectory(MaterializationStagingDirectoryName, 0o755); err != nil {
		return err
	}
	contentByPath := make(map[string][]byte, len(desired.artifacts))
	for _, artifact := range desired.artifacts {
		contentByPath[artifact.path] = artifact.content
	}
	for _, write := range pending.Writes {
		content, exists := contentByPath[write.Path]
		if !exists || DigestBytes(content).String() != write.NewHash {
			return newMaterializationCorruption("desired bytes for staged path %s do not match pending transaction", write.Path)
		}
		if err := adapter.writeFileExclusive(write.StagePath, content, 0o644); err != nil {
			return fmt.Errorf("stage materialization artifact %s: %w", write.Path, err)
		}
	}
	return nil
}

func writePendingMaterialization(adapter *RootedFilesystemAdapter, pending pendingMaterializationWire) error {
	content, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if int64(len(content)) > MaxMaterializationControlBytes {
		return fmt.Errorf("pending materialization exceeds %d bytes", MaxMaterializationControlBytes)
	}
	if err := adapter.atomicWrite(MaterializationPendingFileName, content, 0o644); err != nil {
		return fmt.Errorf("write pending materialization: %w", err)
	}
	return nil
}

func readPendingMaterialization(adapter *RootedFilesystemAdapter) (pendingMaterializationWire, error) {
	content, err := adapter.readBounded(MaterializationPendingFileName, MaxMaterializationControlBytes)
	if err != nil {
		return pendingMaterializationWire{}, newMaterializationCorruption("read pending transaction: %v", err)
	}
	if err := rejectDuplicateJSONObjectKeys(content); err != nil {
		return pendingMaterializationWire{}, newMaterializationCorruption("pending transaction JSON: %v", err)
	}
	var pending pendingMaterializationWire
	if err := decodeStrictJSON(content, &pending); err != nil {
		return pendingMaterializationWire{}, newMaterializationCorruption("decode pending transaction: %v", err)
	}
	if err := validatePendingMaterialization(pending); err != nil {
		return pendingMaterializationWire{}, err
	}
	return pending, nil
}

func validatePendingMaterialization(pending pendingMaterializationWire) error {
	if pending.SchemaVersion != MaterializationInventorySchemaVersion {
		return newMaterializationCorruption("unsupported pending schema_version %d", pending.SchemaVersion)
	}
	if pending.CreateDirectories == nil || pending.Writes == nil || pending.Deletes == nil || pending.RemoveDirectories == nil {
		return newMaterializationCorruption("pending operation lists are required")
	}
	transactionID, err := ParseDigest(pending.TransactionID)
	if err != nil || transactionID.IsZero() {
		return newMaterializationCorruption("pending transaction id is invalid")
	}
	expectedID, err := pendingMaterializationTransactionID(pending)
	if err != nil {
		return err
	}
	if transactionID != expectedID {
		return newMaterializationCorruption("pending transaction id does not match its operations")
	}
	nextHash, err := ParseDigest(pending.NextInventoryHash)
	if err != nil || nextHash.IsZero() {
		return newMaterializationCorruption("pending next-inventory hash is invalid")
	}
	if pending.PreviousInventoryHash != "" {
		previous, err := ParseDigest(pending.PreviousInventoryHash)
		if err != nil || previous.IsZero() {
			return newMaterializationCorruption("pending previous-inventory hash is invalid")
		}
	}
	next, err := normalizeMaterializationInventoryWire(pending.NextInventory)
	if err != nil {
		return err
	}
	_, canonicalHash, err := marshalMaterializationInventory(next)
	if err != nil {
		return err
	}
	if canonicalHash != nextHash {
		return newMaterializationCorruption("pending next-inventory hash does not match its inventory")
	}
	for index, directory := range append(append([]string(nil), pending.CreateDirectories...), pending.RemoveDirectories...) {
		if normalized, err := normalizeMaterializationPath(directory); err != nil || normalized != directory {
			return newMaterializationCorruption("pending directory operation %d has invalid path %q", index, directory)
		}
	}
	for index, write := range pending.Writes {
		if normalized, err := normalizeMaterializationPath(write.Path); err != nil || normalized != write.Path {
			return newMaterializationCorruption("pending write %d has invalid path", index)
		}
		if write.Disposition != "created" && write.Disposition != "updated" {
			return newMaterializationCorruption("pending write %s has invalid disposition", write.Path)
		}
		if write.Expected != "absent" && write.Expected != "hash" {
			return newMaterializationCorruption("pending write %s has invalid expected state", write.Path)
		}
		if write.Expected == "absent" && write.ExpectedHash != "" {
			return newMaterializationCorruption("pending absent write %s carries an expected hash", write.Path)
		}
		if write.Expected == "hash" {
			hash, err := ParseDigest(write.ExpectedHash)
			if err != nil || hash.IsZero() {
				return newMaterializationCorruption("pending write %s has invalid expected hash", write.Path)
			}
		}
		newHash, err := ParseDigest(write.NewHash)
		if err != nil || newHash.IsZero() {
			return newMaterializationCorruption("pending write %s has invalid new hash", write.Path)
		}
		expectedStage := path.Join(MaterializationStagingDirectoryName, fmt.Sprintf("artifact-%06d.data", index))
		if write.StagePath != expectedStage {
			return newMaterializationCorruption("pending write %s has invalid stage path", write.Path)
		}
	}
	for _, deletion := range pending.Deletes {
		if normalized, err := normalizeMaterializationPath(deletion.Path); err != nil || normalized != deletion.Path {
			return newMaterializationCorruption("pending delete has invalid path %q", deletion.Path)
		}
		hash, err := ParseDigest(deletion.ExpectedHash)
		if err != nil || hash.IsZero() {
			return newMaterializationCorruption("pending delete %s has invalid expected hash", deletion.Path)
		}
	}
	return nil
}

func pendingMaterializationTransactionID(pending pendingMaterializationWire) (Digest, error) {
	copyPending := pending
	copyPending.TransactionID = ""
	content, err := json.Marshal(copyPending)
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func applyPendingMaterialization(
	adapter *RootedFilesystemAdapter,
	pending pendingMaterializationWire,
	control materializationControlState,
	nextInventoryBytes []byte,
	nextInventoryHash Digest,
	options MaterializationOptions,
) error {
	currentMatchesNext := control.inventoryExists && control.inventoryHash == nextInventoryHash
	if !currentMatchesNext {
		if pending.PreviousInventoryHash == "" {
			if control.inventoryExists || control.state.Phase != materializationPhaseBootstrap {
				return newMaterializationCorruption("bootstrap transaction no longer matches active control state")
			}
		} else {
			previous, err := ParseDigest(pending.PreviousInventoryHash)
			if err != nil || !control.inventoryExists || control.inventoryHash != previous {
				return newMaterializationCorruption("pending transaction does not match active inventory")
			}
		}
		if err := validatePendingMaterializationAuthority(pending, control); err != nil {
			return err
		}

		if err := validatePendingStage(adapter, pending); err != nil {
			return err
		}
		for _, deletion := range pending.Deletes {
			if err := applyPendingDelete(adapter, deletion); err != nil {
				return err
			}
			if err := injectMaterialization(options, MaterializationFaultAfterStaleDelete); err != nil {
				return err
			}
		}
		for _, directory := range pending.RemoveDirectories {
			if _, err := adapter.removeEmptyDirectory(directory); err != nil {
				return err
			}
			if err := injectMaterialization(options, MaterializationFaultAfterDirectoryCleanup); err != nil {
				return err
			}
		}
		for _, directory := range pending.CreateDirectories {
			if _, err := adapter.makeDirectory(directory, 0o755); err != nil {
				return materializationApplyConflict("", directory, MaterializationConflictUnsafePath, err.Error())
			}
			if err := injectMaterialization(options, MaterializationFaultAfterDirectoryCreate); err != nil {
				return err
			}
		}
		for _, write := range pending.Writes {
			if err := applyPendingWrite(adapter, write); err != nil {
				return err
			}
			if err := injectMaterialization(options, MaterializationFaultAfterArtifactWrite); err != nil {
				return err
			}
		}
		if err := verifyPendingMaterialization(adapter, pending); err != nil {
			return err
		}
		if err := adapter.atomicWrite(MaterializationInventoryFileName, nextInventoryBytes, 0o644); err != nil {
			return fmt.Errorf("activate materialization inventory: %w", err)
		}
		if err := injectMaterialization(options, MaterializationFaultAfterInventoryActivation); err != nil {
			return err
		}
	}

	activeState := materializationStateWire{
		SchemaVersion:       MaterializationInventorySchemaVersion,
		Phase:               materializationPhaseActive,
		ActiveInventoryHash: nextInventoryHash.String(),
	}
	if err := writeMaterializationState(adapter, activeState); err != nil {
		return err
	}
	if err := injectMaterialization(options, MaterializationFaultAfterStateActivation); err != nil {
		return err
	}
	return nil
}

func validatePendingMaterializationAuthority(
	pending pendingMaterializationWire,
	control materializationControlState,
) error {
	next, err := normalizeMaterializationInventoryWire(pending.NextInventory)
	if err != nil {
		return err
	}
	previousByPath := make(map[string]OwnedMaterializationArtifact, len(control.inventory.artifacts))
	previousDirectories := make(map[string]struct{}, len(control.inventory.directories))
	if control.inventoryExists {
		for _, artifact := range control.inventory.artifacts {
			previousByPath[artifact.path] = artifact
		}
		for _, directory := range control.inventory.directories {
			previousDirectories[directory] = struct{}{}
		}
	}
	nextByPath := make(map[string]OwnedMaterializationArtifact, len(next.artifacts))
	nextDirectories := make(map[string]struct{}, len(next.directories))
	for _, artifact := range next.artifacts {
		nextByPath[artifact.path] = artifact
	}
	for _, directory := range next.directories {
		nextDirectories[directory] = struct{}{}
	}

	operationPaths := make(map[string]string)
	for _, write := range pending.Writes {
		nextArtifact, exists := nextByPath[write.Path]
		if !exists || nextArtifact.id != write.ArtifactID || nextArtifact.lastGeneratedHash.String() != write.NewHash {
			return newMaterializationCorruption("pending write %s is not authorized by the next inventory", write.Path)
		}
		previousArtifact, previouslyOwned := previousByPath[write.Path]
		switch write.Expected {
		case "absent":
			if previouslyOwned || write.Disposition != "created" {
				return newMaterializationCorruption("pending create %s conflicts with prior ownership", write.Path)
			}
		case "hash":
			if !previouslyOwned || previousArtifact.lastGeneratedHash.String() != write.ExpectedHash || write.Disposition != "updated" {
				return newMaterializationCorruption("pending update %s is not authorized by the active inventory", write.Path)
			}
		}
		if prior := operationPaths[write.Path]; prior != "" {
			return newMaterializationCorruption("pending path %s has both %s and write operations", write.Path, prior)
		}
		operationPaths[write.Path] = "write"
	}
	for _, deletion := range pending.Deletes {
		previousArtifact, exists := previousByPath[deletion.Path]
		if !exists || previousArtifact.id != deletion.ArtifactID ||
			previousArtifact.lastGeneratedHash.String() != deletion.ExpectedHash {
			return newMaterializationCorruption("pending delete %s is not authorized by the active inventory", deletion.Path)
		}
		if _, remains := nextByPath[deletion.Path]; remains {
			return newMaterializationCorruption("pending delete %s is still present in the next inventory", deletion.Path)
		}
		if prior := operationPaths[deletion.Path]; prior != "" {
			return newMaterializationCorruption("pending path %s has both %s and delete operations", deletion.Path, prior)
		}
		operationPaths[deletion.Path] = "delete"
	}
	seenDirectories := make(map[string]string)
	for _, directory := range pending.CreateDirectories {
		if _, exists := nextDirectories[directory]; !exists {
			return newMaterializationCorruption("pending directory create %s is not owned by the next inventory", directory)
		}
		if prior := seenDirectories[directory]; prior != "" {
			return newMaterializationCorruption("pending directory %s has duplicate %s and create operations", directory, prior)
		}
		seenDirectories[directory] = "create"
	}
	for _, directory := range pending.RemoveDirectories {
		if _, exists := previousDirectories[directory]; !exists {
			return newMaterializationCorruption("pending directory removal %s is not authorized by the active inventory", directory)
		}
		if _, remains := nextDirectories[directory]; remains {
			return newMaterializationCorruption("pending directory removal %s remains owned by the next inventory", directory)
		}
		if prior := seenDirectories[directory]; prior != "" {
			return newMaterializationCorruption("pending directory %s has both %s and remove operations", directory, prior)
		}
		seenDirectories[directory] = "remove"
	}
	return nil
}

func validatePendingStage(adapter *RootedFilesystemAdapter, pending pendingMaterializationWire) error {
	if len(pending.Writes) == 0 {
		return nil
	}
	info, exists, err := adapter.inspectExact(MaterializationStagingDirectoryName)
	if err != nil {
		return newMaterializationCorruption("inspect staging directory: %v", err)
	}
	if !exists || !info.IsDir() {
		return newMaterializationCorruption("pending transaction staging directory is missing")
	}
	entries, err := adapter.readDirectory(MaterializationStagingDirectoryName)
	if err != nil {
		return err
	}
	if len(entries) != len(pending.Writes) {
		return newMaterializationCorruption("pending staging directory contains %d files; expected %d", len(entries), len(pending.Writes))
	}
	expected := make(map[string]pendingMaterializationWriteWire, len(pending.Writes))
	for _, write := range pending.Writes {
		expected[path.Base(write.StagePath)] = write
	}
	for _, entry := range entries {
		write, exists := expected[entry.name]
		if !exists || !entry.info.Mode().IsRegular() {
			return newMaterializationCorruption("unexpected staging entry %s", entry.name)
		}
		hash, err := readMaterializationHash(adapter, write.StagePath)
		if err != nil {
			return err
		}
		if hash.String() != write.NewHash {
			return newMaterializationCorruption("staged bytes for %s do not match pending hash", write.Path)
		}
	}
	return nil
}

func applyPendingDelete(adapter *RootedFilesystemAdapter, deletion pendingMaterializationDeleteWire) error {
	info, exists, err := adapter.inspectExact(deletion.Path)
	if err != nil {
		return materializationApplyConflict(deletion.ArtifactID, deletion.Path, MaterializationConflictUnsafePath, err.Error())
	}
	if !exists {
		return nil
	}
	if !info.Mode().IsRegular() {
		return materializationApplyConflict(
			deletion.ArtifactID, deletion.Path, MaterializationConflictModifiedOwnedPath,
			"stale owned path is no longer a regular file",
		)
	}
	current, err := readMaterializationHash(adapter, deletion.Path)
	if err != nil {
		return err
	}
	if current.String() != deletion.ExpectedHash {
		return materializationApplyConflict(
			deletion.ArtifactID, deletion.Path, MaterializationConflictModifiedOwnedPath,
			"stale owned bytes changed after comparison",
		)
	}
	return adapter.removeFile(deletion.Path)
}

func applyPendingWrite(adapter *RootedFilesystemAdapter, write pendingMaterializationWriteWire) error {
	info, exists, err := adapter.inspectExact(write.Path)
	if err != nil {
		return materializationApplyConflict(write.ArtifactID, write.Path, MaterializationConflictUnsafePath, err.Error())
	}
	if exists {
		if !info.Mode().IsRegular() {
			return materializationApplyConflict(write.ArtifactID, write.Path, MaterializationConflictUnsafePath, "target is not a regular file")
		}
		current, err := readMaterializationHash(adapter, write.Path)
		if err != nil {
			return err
		}
		if current.String() == write.NewHash {
			return nil
		}
		if write.Expected != "hash" || current.String() != write.ExpectedHash {
			kind := MaterializationConflictUnownedPath
			if write.Expected == "hash" {
				kind = MaterializationConflictModifiedOwnedPath
			}
			return materializationApplyConflict(write.ArtifactID, write.Path, kind, "target bytes changed after comparison")
		}
	} else if write.Expected != "absent" {
		return materializationApplyConflict(
			write.ArtifactID, write.Path, MaterializationConflictMissingOwnedPath,
			"owned target disappeared after comparison",
		)
	}
	content, err := adapter.readBounded(write.StagePath, MaxMaterializationArtifactBytes)
	if err != nil {
		return newMaterializationCorruption("read staged bytes for %s: %v", write.Path, err)
	}
	if DigestBytes(content).String() != write.NewHash {
		return newMaterializationCorruption("staged bytes for %s changed", write.Path)
	}
	if err := adapter.atomicWrite(write.Path, content, 0o644); err != nil {
		return fmt.Errorf("write materialized artifact %s: %w", write.Path, err)
	}
	return nil
}

func verifyPendingMaterialization(adapter *RootedFilesystemAdapter, pending pendingMaterializationWire) error {
	for _, artifact := range pending.NextInventory.Artifacts {
		info, exists, err := adapter.inspectExact(artifact.Path)
		if err != nil || !exists || !info.Mode().IsRegular() {
			return newMaterializationCorruption("materialized artifact %s is missing after staged update", artifact.Path)
		}
		hash, err := readMaterializationHash(adapter, artifact.Path)
		if err != nil {
			return err
		}
		if hash.String() != artifact.LastGeneratedHash {
			return materializationApplyConflict(
				artifact.ArtifactID, artifact.Path, MaterializationConflictModifiedOwnedPath,
				"materialized bytes changed before inventory activation",
			)
		}
	}
	for _, deletion := range pending.Deletes {
		if _, exists, err := adapter.inspectExact(deletion.Path); err != nil {
			return err
		} else if exists {
			return materializationApplyConflict(
				deletion.ArtifactID, deletion.Path, MaterializationConflictModifiedOwnedPath,
				"stale owned path reappeared before inventory activation",
			)
		}
	}
	return nil
}

func cleanupPendingMaterialization(adapter *RootedFilesystemAdapter) error {
	if err := adapter.removeFile(MaterializationPendingFileName); err != nil {
		return fmt.Errorf("remove completed pending transaction: %w", err)
	}
	return cleanupOrphanMaterializationStaging(adapter)
}

func cleanupOrphanMaterializationStaging(adapter *RootedFilesystemAdapter) error {
	info, exists, err := adapter.inspectExact(MaterializationStagingDirectoryName)
	if err != nil {
		return newMaterializationCorruption("inspect staging directory: %v", err)
	}
	if !exists {
		return nil
	}
	if !info.IsDir() {
		return newMaterializationCorruption("staging control path is not a directory")
	}
	entries, err := adapter.readDirectory(MaterializationStagingDirectoryName)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.info.Mode().IsRegular() || !validMaterializationStageName(entry.name) {
			return newMaterializationCorruption("unexpected orphan staging entry %s", entry.name)
		}
		if err := adapter.removeFile(path.Join(MaterializationStagingDirectoryName, entry.name)); err != nil {
			return err
		}
	}
	removed, err := adapter.removeEmptyDirectory(MaterializationStagingDirectoryName)
	if err != nil {
		return err
	}
	if !removed {
		return newMaterializationCorruption("staging directory is not empty after controlled cleanup")
	}
	return nil
}

func validMaterializationStageName(name string) bool {
	if !strings.HasPrefix(name, "artifact-") || !strings.HasSuffix(name, ".data") || len(name) != len("artifact-000000.data") {
		return false
	}
	for _, character := range strings.TrimSuffix(strings.TrimPrefix(name, "artifact-"), ".data") {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func rootedPathExists(adapter *RootedFilesystemAdapter, relative string) (bool, error) {
	_, exists, err := adapter.inspectExact(relative)
	return exists, err
}

func readMaterializationHash(adapter *RootedFilesystemAdapter, relative string) (Digest, error) {
	content, err := adapter.readBounded(relative, MaxMaterializationArtifactBytes)
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}

func injectMaterialization(options MaterializationOptions, point MaterializationFaultPoint) error {
	if options.FaultInjector == nil {
		return nil
	}
	if err := options.FaultInjector(point); err != nil {
		return fmt.Errorf("materialization fault at %s: %w", point, err)
	}
	return nil
}

func materializationApplyConflict(
	artifactID, relativePath string,
	kind MaterializationConflictKind,
	detail string,
) error {
	return MaterializationConflictError{conflicts: []MaterializationConflict{{
		kind: kind, artifactID: artifactID, path: relativePath, detail: detail,
	}}}
}

func sortMaterializationConflicts(conflicts []MaterializationConflict) {
	sort.Slice(conflicts, func(i, j int) bool {
		left := conflicts[i].path + "\x00" + string(conflicts[i].kind) + "\x00" + conflicts[i].artifactID
		right := conflicts[j].path + "\x00" + string(conflicts[j].kind) + "\x00" + conflicts[j].artifactID
		return left < right
	})
}

func resultFromPending(
	pending pendingMaterializationWire,
	inventory MaterializationInventory,
	recovered bool,
) MaterializationResult {
	result := MaterializationResult{inventory: cloneMaterializationInventory(inventory), recovered: recovered}
	for _, write := range pending.Writes {
		if write.Disposition == "created" {
			result.created = append(result.created, write.Path)
		} else {
			result.updated = append(result.updated, write.Path)
		}
	}
	for _, deletion := range pending.Deletes {
		result.deleted = append(result.deleted, deletion.Path)
	}
	sort.Strings(result.created)
	sort.Strings(result.updated)
	sort.Strings(result.deleted)
	return result
}
