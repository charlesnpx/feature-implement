package workspace

import (
	"encoding/json"
	"fmt"
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
	ArtifactID      string `json:"artifact_id"`
	Path            string `json:"path"`
	Disposition     string `json:"disposition"`
	Expected        string `json:"expected"`
	ExpectedHash    string `json:"expected_hash"`
	NewHash         string `json:"new_hash"`
	StagePath       string `json:"stage_path"`
	StageProofPath  string `json:"stage_proof_path"`
	SourceProofPath string `json:"source_proof_path"`
	ActivationPath  string `json:"activation_path"`
	QuarantinePath  string `json:"quarantine_path"`
}

type pendingMaterializationDeleteWire struct {
	ArtifactID      string `json:"artifact_id"`
	Path            string `json:"path"`
	ExpectedHash    string `json:"expected_hash"`
	SourceProofPath string `json:"source_proof_path"`
	QuarantinePath  string `json:"quarantine_path"`
}

type pendingMaterializationDirectoryWire struct {
	Path            string `json:"path"`
	PreparationPath string `json:"preparation_path"`
	ClaimPath       string `json:"claim_path"`
	ClaimAnchorPath string `json:"claim_anchor_path"`
	ClaimProofPath  string `json:"claim_proof_path"`
}

type pendingMaterializationDirectoryDeleteWire struct {
	Path                    string `json:"path"`
	ClaimPath               string `json:"claim_path"`
	ClaimAnchorPath         string `json:"claim_anchor_path"`
	ClaimQuarantinePath     string `json:"claim_quarantine_path"`
	DirectoryQuarantinePath string `json:"directory_quarantine_path"`
}

type pendingMaterializationControlWire struct {
	TargetPath         string `json:"target_path"`
	TemporaryPath      string `json:"temporary_path"`
	TemporaryProofPath string `json:"temporary_proof_path"`
	PreviousProofPath  string `json:"previous_proof_path"`
	QuarantinePath     string `json:"quarantine_path"`
	Expected           string `json:"expected"`
	ExpectedHash       string `json:"expected_hash"`
	NewHash            string `json:"new_hash"`
}

type pendingMaterializationWire struct {
	SchemaVersion         int                                         `json:"schema_version"`
	TransactionID         string                                      `json:"transaction_id"`
	PreviousInventoryHash string                                      `json:"previous_inventory_hash"`
	PreviousStateHash     string                                      `json:"previous_state_hash"`
	NextInventoryHash     string                                      `json:"next_inventory_hash"`
	NextInventory         materializationInventoryWire                `json:"next_inventory"`
	PendingTemporaryPath  string                                      `json:"pending_temporary_path"`
	PendingProofPath      string                                      `json:"pending_proof_path"`
	StagingClaimPath      string                                      `json:"staging_claim_path"`
	StagingProofPath      string                                      `json:"staging_proof_path"`
	InventoryControl      pendingMaterializationControlWire           `json:"inventory_control"`
	StateControl          pendingMaterializationControlWire           `json:"state_control"`
	CreateDirectories     []pendingMaterializationDirectoryWire       `json:"create_directories"`
	Writes                []pendingMaterializationWriteWire           `json:"writes"`
	Deletes               []pendingMaterializationDeleteWire          `json:"deletes"`
	RemoveDirectories     []pendingMaterializationDirectoryDeleteWire `json:"remove_directories"`
}

type materializationControlState struct {
	state           materializationStateWire
	stateHash       Digest
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
	rootPath, err = validateMaterializationRootPath(rootPath)
	if err != nil {
		return MaterializationResult{}, err
	}

	materializationProcessMutex.Lock()
	defer materializationProcessMutex.Unlock()

	adapter, err := openOrCreateRootedFilesystemAdapter(rootPath)
	if err != nil {
		return MaterializationResult{}, err
	}
	defer adapter.Close()

	control, recovered, err := loadMaterializationControl(adapter, desired, options)
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

	activeStateBytes, err := marshalMaterializationState(materializationStateWire{
		SchemaVersion:       MaterializationInventorySchemaVersion,
		Phase:               materializationPhaseActive,
		ActiveInventoryHash: nextHash.String(),
	})
	if err != nil {
		return MaterializationResult{}, err
	}
	pending, err := buildPendingMaterialization(control, comparison, nextHash, nextBytes, activeStateBytes)
	if err != nil {
		return MaterializationResult{}, err
	}
	pendingBytes, err := marshalPendingMaterialization(pending)
	if err != nil {
		return MaterializationResult{}, err
	}
	if err := preflightPendingMaterializationPaths(adapter, pending); err != nil {
		return MaterializationResult{}, err
	}
	if err := preparePendingMaterialization(adapter, pending, desired, nextBytes, activeStateBytes); err != nil {
		return MaterializationResult{}, err
	}
	if err := writePendingMaterialization(adapter, pending, pendingBytes); err != nil {
		return MaterializationResult{}, err
	}
	if err := injectMaterialization(options, MaterializationFaultAfterStaging); err != nil {
		return MaterializationResult{}, err
	}
	if err := injectMaterialization(options, MaterializationFaultAfterPending); err != nil {
		return MaterializationResult{}, err
	}
	if err := applyPendingMaterialization(adapter, pending, control, nextBytes, nextHash, options); err != nil {
		return MaterializationResult{}, err
	}
	if err := cleanupPendingMaterialization(adapter, pending); err != nil {
		return MaterializationResult{}, err
	}

	return resultFromPending(pending, comparison.nextInventory, recovered), nil
}

func validateMaterializationRootPath(rootPath string) (string, error) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if !filepath.IsAbs(rootPath) {
		return "", fmt.Errorf("materialization requires an absolute destination root")
	}
	volumeRoot := filepath.VolumeName(rootPath) + string(filepath.Separator)
	relative := strings.TrimPrefix(rootPath, volumeRoot)
	if relative == "" {
		return "", fmt.Errorf("materialization destination cannot be a filesystem root")
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == "" || strings.HasPrefix(component, ".") ||
			len(component) > maxMaterializationComponentBytes || !norm.NFC.IsNormalString(component) {
			return "", fmt.Errorf("materialization destination %s has unsafe component %q", rootPath, component)
		}
	}
	return rootPath, nil
}

func loadMaterializationControl(
	adapter *RootedFilesystemAdapter,
	desired desiredMaterialization,
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
		if err := createBootstrapMaterializationState(adapter, state); err != nil {
			return materializationControlState{}, false, err
		}
		if err := injectMaterialization(options, MaterializationFaultAfterBootstrapState); err != nil {
			return materializationControlState{}, false, err
		}
	}

	state, stateBytes, err := readMaterializationStateWithBytes(adapter)
	if err != nil {
		return materializationControlState{}, false, err
	}
	if err := verifyMaterializationOwnershipNamespace(adapter); err != nil {
		return materializationControlState{}, false, err
	}
	control, err := readActiveMaterializationControl(adapter, state, stateBytes)
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
		if err := validatePendingStage(adapter, pending); err != nil {
			return materializationControlState{}, false, fmt.Errorf("recover pending materialization staging: %w", err)
		}
		if err := applyPendingMaterialization(adapter, pending, control, nextBytes, nextHash, MaterializationOptions{}); err != nil {
			return materializationControlState{}, false, fmt.Errorf("recover pending materialization: %w", err)
		}
		if err := cleanupPendingMaterialization(adapter, pending); err != nil {
			return materializationControlState{}, false, err
		}
		recovered = true
		state, stateBytes, err = readMaterializationStateWithBytes(adapter)
		if err != nil {
			return materializationControlState{}, false, err
		}
		control, err = readActiveMaterializationControl(adapter, state, stateBytes)
		if err != nil {
			return materializationControlState{}, false, err
		}
	} else if err := rejectOrphanMaterializationControls(adapter, control); err != nil {
		return materializationControlState{}, false, err
	}

	if control.state.Phase == materializationPhaseBootstrap {
		entries, err := adapter.readDirectory(".")
		if err != nil {
			return materializationControlState{}, false, err
		}
		for _, entry := range entries {
			if entry.name != MaterializationStateFileName &&
				entry.name != MaterializationOwnershipDirectoryName &&
				entry.name != MaterializationOwnershipProofFileName {
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
	stateBytes []byte,
) (materializationControlState, error) {
	inventoryExists, err := rootedPathExists(adapter, MaterializationInventoryFileName)
	if err != nil {
		return materializationControlState{}, newMaterializationCorruption("inspect inventory: %v", err)
	}
	control := materializationControlState{
		state: state, stateHash: DigestBytes(stateBytes),
		inventoryExists: inventoryExists,
	}
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
	state, _, err := readMaterializationStateWithBytes(adapter)
	return state, err
}

func readMaterializationStateWithBytes(adapter *RootedFilesystemAdapter) (materializationStateWire, []byte, error) {
	content, err := adapter.readBounded(MaterializationStateFileName, 64*1024)
	if err != nil {
		return materializationStateWire{}, nil, newMaterializationCorruption("read state: %v", err)
	}
	if err := rejectDuplicateJSONObjectKeys(content); err != nil {
		return materializationStateWire{}, nil, newMaterializationCorruption("state JSON: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return materializationStateWire{}, nil, newMaterializationCorruption("decode state fields: %v", err)
	}
	for _, required := range []string{"schema_version", "phase", "active_inventory_hash"} {
		if _, exists := fields[required]; !exists {
			return materializationStateWire{}, nil, newMaterializationCorruption("state field %s is required", required)
		}
	}
	var state materializationStateWire
	if err := decodeStrictJSON(content, &state); err != nil {
		return materializationStateWire{}, nil, newMaterializationCorruption("decode state: %v", err)
	}
	if state.SchemaVersion != MaterializationInventorySchemaVersion {
		return materializationStateWire{}, nil, newMaterializationCorruption("unsupported state schema_version %d", state.SchemaVersion)
	}
	if state.Phase != materializationPhaseBootstrap && state.Phase != materializationPhaseActive {
		return materializationStateWire{}, nil, newMaterializationCorruption("unsupported state phase %q", state.Phase)
	}
	return state, content, nil
}

func marshalMaterializationState(state materializationStateWire) ([]byte, error) {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	content = append(content, '\n')
	return content, nil
}

func createBootstrapMaterializationState(
	adapter *RootedFilesystemAdapter,
	state materializationStateWire,
) error {
	if err := createMaterializationOwnershipNamespace(adapter); err != nil {
		return err
	}
	content, err := marshalMaterializationState(state)
	if err != nil {
		return err
	}
	if err := adapter.writeFileExclusive(MaterializationStateFileName, content, 0o644); err != nil {
		return fmt.Errorf("create bootstrap materialization state: %w", err)
	}
	return nil
}

func createMaterializationOwnershipNamespace(adapter *RootedFilesystemAdapter) error {
	created, err := adapter.makeDirectory(MaterializationOwnershipDirectoryName, 0o700)
	if err != nil {
		return fmt.Errorf("create materialization ownership directory: %w", err)
	}
	if !created {
		return newMaterializationCorruption("ownership directory appeared during bootstrap")
	}
	proofContent := []byte("materialization-ownership-root:v2\n")
	if err := adapter.writeFileExclusive(MaterializationOwnershipProofFileName, proofContent, 0o600); err != nil {
		return fmt.Errorf("create materialization ownership proof: %w", err)
	}
	claim := path.Join(MaterializationOwnershipDirectoryName, materializationOwnershipRootClaimName)
	if err := adapter.linkFileNoReplace(MaterializationOwnershipProofFileName, claim); err != nil {
		return fmt.Errorf("claim materialization ownership directory: %w", err)
	}
	return verifyMaterializationOwnershipNamespace(adapter)
}

func verifyMaterializationOwnershipNamespace(adapter *RootedFilesystemAdapter) error {
	directoryInfo, directoryExists, err := adapter.inspectExact(MaterializationOwnershipDirectoryName)
	if err != nil || !directoryExists || !directoryInfo.IsDir() {
		if err == nil {
			err = fmt.Errorf("ownership directory is missing or invalid")
		}
		return newMaterializationCorruption("verify ownership directory: %v", err)
	}
	claim := path.Join(MaterializationOwnershipDirectoryName, materializationOwnershipRootClaimName)
	proofContent, err := adapter.readBounded(MaterializationOwnershipProofFileName, 1024)
	if err != nil || string(proofContent) != "materialization-ownership-root:v2\n" {
		if err == nil {
			err = fmt.Errorf("ownership proof content changed")
		}
		return newMaterializationCorruption("verify ownership proof: %v", err)
	}
	same, err := adapter.sameFile(MaterializationOwnershipProofFileName, claim)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("ownership proof identity changed")
		}
		return newMaterializationCorruption("verify ownership directory claim: %v", err)
	}
	return nil
}

func materializationDirectoryAnchorPath(directory string) string {
	digest := strings.TrimPrefix(DigestBytes([]byte(directory)).String(), "sha256:")
	return path.Join(MaterializationOwnershipDirectoryName, "directory-"+digest+".anchor")
}

func materializationDirectoryClaimPath(directory string) string {
	return path.Join(directory, MaterializationDirectoryClaimFileName)
}

func materializationDirectoryClaimContent(directory string) []byte {
	return []byte("materialization-directory:v2:" + directory + "\n")
}

func verifyOwnedMaterializationDirectory(
	adapter *RootedFilesystemAdapter,
	directory string,
	claimPath string,
	anchorPath string,
) error {
	info, exists, err := adapter.inspectExact(directory)
	if err != nil || !exists || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("owned directory is missing or invalid")
		}
		return err
	}
	same, err := adapter.sameFile(claimPath, anchorPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("directory claim identity changed")
		}
		return err
	}
	content, err := adapter.readBounded(anchorPath, 8*1024)
	if err != nil {
		return err
	}
	if string(content) != string(materializationDirectoryClaimContent(directory)) {
		return fmt.Errorf("directory claim content changed")
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
		_, previouslyOwned := previousDirectories[directory]
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
			if previouslyOwned {
				if err := verifyOwnedMaterializationDirectory(
					adapter,
					directory,
					materializationDirectoryClaimPath(directory),
					materializationDirectoryAnchorPath(directory),
				); err != nil {
					comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
						kind: MaterializationConflictModifiedOwnedPath, path: directory,
						detail: "owned directory lost its durable identity: " + err.Error(),
					})
					continue
				}
				nextOwnedDirectories = append(nextOwnedDirectories, directory)
			}
			continue
		}
		if previouslyOwned {
			comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
				kind: MaterializationConflictMissingOwnedPath, path: directory,
				detail: "owned directory is missing and will not be recreated",
			})
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
			if !exists {
				comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
					kind: MaterializationConflictMissingOwnedPath, path: directory,
					detail: "stale owned directory is missing",
				})
				continue
			}
			if !info.IsDir() {
				comparison.addUnsafeConflict("", directory, "stale owned directory path is no longer a directory")
				continue
			}
			if err := verifyOwnedMaterializationDirectory(
				adapter,
				directory,
				materializationDirectoryClaimPath(directory),
				materializationDirectoryAnchorPath(directory),
			); err != nil {
				comparison.conflicts = append(comparison.conflicts, MaterializationConflict{
					kind: MaterializationConflictModifiedOwnedPath, path: directory,
					detail: "stale owned directory lost its durable identity: " + err.Error(),
				})
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
	nextInventoryBytes []byte,
	nextStateBytes []byte,
) (pendingMaterializationWire, error) {
	pending := pendingMaterializationWire{
		SchemaVersion:     MaterializationInventorySchemaVersion,
		PreviousStateHash: control.stateHash.String(),
		NextInventoryHash: nextHash.String(),
		NextInventory:     comparison.nextInventory.wire(),
		CreateDirectories: make([]pendingMaterializationDirectoryWire, len(comparison.createDirectories)),
		Writes:            append([]pendingMaterializationWriteWire{}, comparison.writes...),
		Deletes:           append([]pendingMaterializationDeleteWire{}, comparison.deletes...),
		RemoveDirectories: make([]pendingMaterializationDirectoryDeleteWire, len(comparison.removeDirectories)),
		InventoryControl: pendingMaterializationControlWire{
			TargetPath: MaterializationInventoryFileName,
			Expected:   "absent",
			NewHash:    DigestBytes(nextInventoryBytes).String(),
		},
		StateControl: pendingMaterializationControlWire{
			TargetPath:   MaterializationStateFileName,
			Expected:     "hash",
			ExpectedHash: control.stateHash.String(),
			NewHash:      DigestBytes(nextStateBytes).String(),
		},
	}
	for index, directory := range comparison.createDirectories {
		pending.CreateDirectories[index].Path = directory
	}
	if control.inventoryExists {
		pending.PreviousInventoryHash = control.inventoryHash.String()
		pending.InventoryControl.Expected = "hash"
		pending.InventoryControl.ExpectedHash = control.inventoryHash.String()
	}
	for index, directory := range comparison.removeDirectories {
		pending.RemoveDirectories[index].Path = directory
	}
	transactionID, err := pendingMaterializationTransactionID(pending)
	if err != nil {
		return pendingMaterializationWire{}, err
	}
	pending.TransactionID = transactionID.String()
	pending.PendingTemporaryPath = materializationTransactionControlPath(pending.TransactionID, "pending")
	pending.PendingProofPath = materializationTransactionControlPath(pending.TransactionID, "pending-proof")
	if len(pending.Writes) != 0 {
		pending.StagingProofPath = materializationTransactionControlPath(pending.TransactionID, "staging-proof")
		pending.StagingClaimPath = path.Join(
			MaterializationStagingDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "staging", 0, "claim"),
		)
	}
	populatePendingControlPaths(&pending.InventoryControl, pending.TransactionID, "inventory")
	populatePendingControlPaths(&pending.StateControl, pending.TransactionID, "state")
	for index := range pending.CreateDirectories {
		pending.CreateDirectories[index].PreparationPath = path.Join(
			path.Dir(pending.CreateDirectories[index].Path),
			materializationTransactionOperationName(pending.TransactionID, "directory", index, "preparation"),
		)
		pending.CreateDirectories[index].ClaimPath = materializationDirectoryClaimPath(pending.CreateDirectories[index].Path)
		pending.CreateDirectories[index].ClaimAnchorPath = materializationDirectoryAnchorPath(pending.CreateDirectories[index].Path)
		pending.CreateDirectories[index].ClaimProofPath = path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "directory", index, "claim-proof"),
		)
	}
	for index := range pending.Writes {
		pending.Writes[index].StagePath = path.Join(
			MaterializationStagingDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "write", index, "stage"),
		)
		pending.Writes[index].StageProofPath = path.Join(
			MaterializationStagingDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "write", index, "stage-proof"),
		)
		pending.Writes[index].ActivationPath = path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "write", index, "activation"),
		)
		if pending.Writes[index].Expected == "hash" {
			pending.Writes[index].SourceProofPath = path.Join(
				MaterializationOwnershipDirectoryName,
				materializationTransactionOperationName(pending.TransactionID, "write", index, "source-proof"),
			)
			pending.Writes[index].QuarantinePath = path.Join(
				MaterializationOwnershipDirectoryName,
				materializationTransactionOperationName(pending.TransactionID, "write", index, "quarantine"),
			)
		}
	}
	for index := range pending.Deletes {
		pending.Deletes[index].SourceProofPath = path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "delete", index, "source-proof"),
		)
		pending.Deletes[index].QuarantinePath = path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "delete", index, "quarantine"),
		)
	}
	for index := range pending.RemoveDirectories {
		directory := &pending.RemoveDirectories[index]
		directory.ClaimPath = materializationDirectoryClaimPath(directory.Path)
		directory.ClaimAnchorPath = materializationDirectoryAnchorPath(directory.Path)
		directory.ClaimQuarantinePath = path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "directory-remove", index, "claim-quarantine"),
		)
		directory.DirectoryQuarantinePath = path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "directory-remove", index, "directory-quarantine"),
		)
	}
	return pending, nil
}

func populatePendingControlPaths(control *pendingMaterializationControlWire, transactionID, kind string) {
	control.TemporaryPath = materializationTransactionControlPath(transactionID, kind)
	control.TemporaryProofPath = materializationTransactionControlPath(transactionID, kind+"-temporary-proof")
	control.QuarantinePath = materializationTransactionControlPath(transactionID, kind+"-quarantine")
	if control.Expected == "hash" {
		control.PreviousProofPath = materializationTransactionControlPath(transactionID, kind+"-previous-proof")
	}
}

func materializationTransactionControlPath(transactionID, kind string) string {
	return fmt.Sprintf(
		"%s%s.%s.tmp",
		materializationTransactionPathPrefix,
		strings.TrimPrefix(transactionID, "sha256:"),
		kind,
	)
}

func materializationTransactionOperationName(transactionID, kind string, index int, purpose string) string {
	return fmt.Sprintf(
		"%s%s.%s-%06d.%s",
		materializationTransactionPathPrefix, strings.TrimPrefix(transactionID, "sha256:"), kind, index, purpose,
	)
}

func preparePendingMaterialization(
	adapter *RootedFilesystemAdapter,
	pending pendingMaterializationWire,
	desired desiredMaterialization,
	nextInventoryBytes []byte,
	nextStateBytes []byte,
) error {
	if len(pending.Writes) != 0 {
		created, err := adapter.makeDirectory(MaterializationStagingDirectoryName, 0o755)
		if err != nil {
			return err
		}
		if !created {
			return newMaterializationCorruption("staging directory appeared while preparing the transaction")
		}
		proofContent := []byte("materialization-staging:" + pending.TransactionID + "\n")
		if err := adapter.writeFileExclusive(pending.StagingProofPath, proofContent, 0o600); err != nil {
			return fmt.Errorf("create staging identity proof: %w", err)
		}
		if err := adapter.linkFileNoReplace(pending.StagingProofPath, pending.StagingClaimPath); err != nil {
			return fmt.Errorf("claim staging directory: %w", err)
		}
	}
	contentByPath := make(map[string][]byte, len(desired.artifacts))
	for _, artifact := range desired.artifacts {
		contentByPath[artifact.path] = artifact.content
	}
	for _, write := range pending.Writes {
		content, desiredExists := contentByPath[write.Path]
		if !desiredExists || DigestBytes(content).String() != write.NewHash {
			return newMaterializationCorruption("original desired bytes for staged path %s are required to recover the pending transaction", write.Path)
		}
		if err := adapter.writeFileExclusive(write.StagePath, content, 0o644); err != nil {
			return fmt.Errorf("stage materialization artifact %s: %w", write.Path, err)
		}
		if err := adapter.linkFileNoReplace(write.StagePath, write.StageProofPath); err != nil {
			return fmt.Errorf("prove staged materialization artifact %s: %w", write.Path, err)
		}
	}
	for _, directory := range pending.CreateDirectories {
		claimContent := materializationDirectoryClaimContent(directory.Path)
		if err := adapter.writeFileExclusive(directory.ClaimAnchorPath, claimContent, 0o600); err != nil {
			return fmt.Errorf("create directory ownership anchor for %s: %w", directory.Path, err)
		}
		if err := adapter.linkFileNoReplace(directory.ClaimAnchorPath, directory.ClaimProofPath); err != nil {
			return fmt.Errorf("prove directory ownership anchor for %s: %w", directory.Path, err)
		}
	}
	for _, write := range pending.Writes {
		if write.SourceProofPath == "" {
			continue
		}
		if err := preparePendingSourceProof(adapter, write.Path, write.SourceProofPath, write.ExpectedHash); err != nil {
			return err
		}
	}
	for _, deletion := range pending.Deletes {
		if err := preparePendingSourceProof(adapter, deletion.Path, deletion.SourceProofPath, deletion.ExpectedHash); err != nil {
			return err
		}
	}
	for _, directory := range pending.RemoveDirectories {
		if err := verifyOwnedMaterializationDirectory(adapter, directory.Path, directory.ClaimPath, directory.ClaimAnchorPath); err != nil {
			return materializationApplyConflict("", directory.Path, MaterializationConflictModifiedOwnedPath, err.Error())
		}
	}
	if err := preparePendingControl(adapter, pending.InventoryControl, nextInventoryBytes); err != nil {
		return fmt.Errorf("prepare inventory control: %w", err)
	}
	if err := preparePendingControl(adapter, pending.StateControl, nextStateBytes); err != nil {
		return fmt.Errorf("prepare state control: %w", err)
	}
	return validatePendingStage(adapter, pending)
}

func preparePendingSourceProof(
	adapter *RootedFilesystemAdapter,
	target string,
	proof string,
	expectedHash string,
) error {
	if err := adapter.linkFileNoReplace(target, proof); err != nil {
		return materializationApplyConflict("", target, MaterializationConflictModifiedOwnedPath, "capture exact source identity: "+err.Error())
	}
	current, err := readMaterializationHash(adapter, proof)
	if err != nil || current.String() != expectedHash {
		if err == nil {
			err = fmt.Errorf("source bytes changed before identity capture")
		}
		return materializationApplyConflict("", target, MaterializationConflictModifiedOwnedPath, err.Error())
	}
	return nil
}

func preparePendingControl(
	adapter *RootedFilesystemAdapter,
	control pendingMaterializationControlWire,
	newContent []byte,
) error {
	if DigestBytes(newContent).String() != control.NewHash {
		return newMaterializationCorruption("control %s new hash does not match prepared bytes", control.TargetPath)
	}
	if err := adapter.writeFileExclusive(control.TemporaryPath, newContent, 0o644); err != nil {
		return err
	}
	if err := adapter.linkFileNoReplace(control.TemporaryPath, control.TemporaryProofPath); err != nil {
		return err
	}
	if control.Expected == "hash" {
		if err := adapter.linkFileNoReplace(control.TargetPath, control.PreviousProofPath); err != nil {
			return err
		}
		current, err := readMaterializationHash(adapter, control.PreviousProofPath)
		if err != nil || current.String() != control.ExpectedHash {
			if err == nil {
				err = fmt.Errorf("previous control bytes changed before identity capture")
			}
			return err
		}
	}
	return nil
}

func marshalPendingMaterialization(pending pendingMaterializationWire) ([]byte, error) {
	content, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return nil, err
	}
	content = append(content, '\n')
	if int64(len(content)) > MaxMaterializationControlBytes {
		return nil, fmt.Errorf("pending materialization exceeds %d bytes", MaxMaterializationControlBytes)
	}
	return content, nil
}

func writePendingMaterialization(
	adapter *RootedFilesystemAdapter,
	pending pendingMaterializationWire,
	content []byte,
) error {
	if err := adapter.writeFileExclusive(pending.PendingTemporaryPath, content, 0o644); err != nil {
		return fmt.Errorf("write pending materialization: %w", err)
	}
	if err := adapter.linkFileNoReplace(pending.PendingTemporaryPath, pending.PendingProofPath); err != nil {
		return fmt.Errorf("prove pending materialization identity: %w", err)
	}
	if err := adapter.renameFileNoReplace(pending.PendingTemporaryPath, MaterializationPendingFileName); err != nil {
		return fmt.Errorf("activate pending materialization: %w", err)
	}
	return nil
}

func preflightPendingMaterializationPaths(
	adapter *RootedFilesystemAdapter,
	pending pendingMaterializationWire,
) error {
	paths := []string{
		MaterializationPendingFileName,
		pending.PendingTemporaryPath,
		pending.PendingProofPath,
	}
	for _, control := range []pendingMaterializationControlWire{pending.InventoryControl, pending.StateControl} {
		paths = append(paths, control.TemporaryPath, control.TemporaryProofPath, control.QuarantinePath)
		if control.PreviousProofPath != "" {
			paths = append(paths, control.PreviousProofPath)
		}
	}
	if len(pending.Writes) != 0 {
		paths = append(paths, MaterializationStagingDirectoryName, pending.StagingClaimPath, pending.StagingProofPath)
	}
	for _, directory := range pending.CreateDirectories {
		paths = append(paths, directory.PreparationPath, directory.ClaimPath, directory.ClaimAnchorPath, directory.ClaimProofPath)
	}
	for _, write := range pending.Writes {
		paths = append(paths, write.StagePath, write.StageProofPath, write.ActivationPath)
		if write.SourceProofPath != "" {
			paths = append(paths, write.SourceProofPath)
		}
		if write.QuarantinePath != "" {
			paths = append(paths, write.QuarantinePath)
		}
	}
	for _, deletion := range pending.Deletes {
		paths = append(paths, deletion.SourceProofPath, deletion.QuarantinePath)
	}
	for _, directory := range pending.RemoveDirectories {
		paths = append(paths, directory.ClaimQuarantinePath, directory.DirectoryQuarantinePath)
	}
	conflicts := make([]MaterializationConflict, 0)
	for _, relative := range paths {
		_, exists, err := adapter.inspectExact(relative)
		if err != nil {
			conflicts = append(conflicts, MaterializationConflict{
				kind: MaterializationConflictUnsafePath, path: relative, detail: err.Error(),
			})
			continue
		}
		if exists {
			conflicts = append(conflicts, MaterializationConflict{
				kind: MaterializationConflictUnownedPath, path: relative,
				detail: "reserved transaction path already exists and will not be removed",
			})
		}
	}
	if len(conflicts) != 0 {
		sortMaterializationConflicts(conflicts)
		return MaterializationConflictError{conflicts: conflicts}
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
	same, err := adapter.sameFile(MaterializationPendingFileName, pending.PendingProofPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("pending transaction identity does not match its proof")
		}
		return pendingMaterializationWire{}, newMaterializationCorruption("verify pending transaction identity: %v", err)
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
	if pending.PendingTemporaryPath != materializationTransactionControlPath(pending.TransactionID, "pending") ||
		pending.PendingProofPath != materializationTransactionControlPath(pending.TransactionID, "pending-proof") {
		return newMaterializationCorruption("pending transaction identity paths do not match the transaction")
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
	previousStateHash, err := ParseDigest(pending.PreviousStateHash)
	if err != nil || previousStateHash.IsZero() {
		return newMaterializationCorruption("pending previous-state hash is invalid")
	}
	if err := validatePendingControl(pending.InventoryControl, pending.TransactionID, "inventory"); err != nil {
		return err
	}
	if err := validatePendingControl(pending.StateControl, pending.TransactionID, "state"); err != nil {
		return err
	}
	if pending.InventoryControl.TargetPath != MaterializationInventoryFileName ||
		pending.StateControl.TargetPath != MaterializationStateFileName ||
		pending.InventoryControl.NewHash != pending.NextInventoryHash ||
		pending.StateControl.ExpectedHash != pending.PreviousStateHash {
		return newMaterializationCorruption("pending control bindings do not match inventory and state authority")
	}
	if pending.PreviousInventoryHash == "" {
		if pending.InventoryControl.Expected != "absent" || pending.InventoryControl.ExpectedHash != "" {
			return newMaterializationCorruption("bootstrap inventory control must expect an absent target")
		}
	} else if pending.InventoryControl.Expected != "hash" ||
		pending.InventoryControl.ExpectedHash != pending.PreviousInventoryHash {
		return newMaterializationCorruption("pending inventory control does not bind the previous inventory")
	}
	if pending.StateControl.Expected != "hash" {
		return newMaterializationCorruption("pending state control must bind the previous state")
	}
	if len(pending.Writes) == 0 {
		if pending.StagingClaimPath != "" || pending.StagingProofPath != "" {
			return newMaterializationCorruption("pending transaction without writes carries staging identity paths")
		}
	} else {
		expectedStagingClaim := path.Join(
			MaterializationStagingDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "staging", 0, "claim"),
		)
		if pending.StagingClaimPath != expectedStagingClaim ||
			pending.StagingProofPath != materializationTransactionControlPath(pending.TransactionID, "staging-proof") {
			return newMaterializationCorruption("pending staging identity paths do not match the transaction")
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
	for index, directory := range pending.CreateDirectories {
		if normalized, err := normalizeMaterializationPath(directory.Path); err != nil || normalized != directory.Path {
			return newMaterializationCorruption("pending directory create %d has invalid path %q", index, directory.Path)
		}
		expectedPreparation := path.Join(
			path.Dir(directory.Path),
			materializationTransactionOperationName(pending.TransactionID, "directory", index, "preparation"),
		)
		if directory.PreparationPath != expectedPreparation {
			return newMaterializationCorruption("pending directory %s has an invalid preparation path", directory.Path)
		}
		expectedClaim := materializationDirectoryClaimPath(directory.Path)
		if directory.ClaimPath != expectedClaim {
			return newMaterializationCorruption(
				"pending directory %s has claim path %q; expected %q",
				directory.Path, directory.ClaimPath, expectedClaim,
			)
		}
		if directory.ClaimAnchorPath != materializationDirectoryAnchorPath(directory.Path) ||
			directory.ClaimProofPath != path.Join(
				MaterializationOwnershipDirectoryName,
				materializationTransactionOperationName(pending.TransactionID, "directory", index, "claim-proof"),
			) {
			return newMaterializationCorruption("pending directory %s has invalid ownership proof paths", directory.Path)
		}
	}
	for index, directory := range pending.RemoveDirectories {
		if normalized, err := normalizeMaterializationPath(directory.Path); err != nil || normalized != directory.Path {
			return newMaterializationCorruption("pending directory removal %d has invalid path %q", index, directory.Path)
		}
		if directory.ClaimPath != materializationDirectoryClaimPath(directory.Path) ||
			directory.ClaimAnchorPath != materializationDirectoryAnchorPath(directory.Path) ||
			directory.ClaimQuarantinePath != path.Join(
				MaterializationOwnershipDirectoryName,
				materializationTransactionOperationName(pending.TransactionID, "directory-remove", index, "claim-quarantine"),
			) ||
			directory.DirectoryQuarantinePath != path.Join(
				MaterializationOwnershipDirectoryName,
				materializationTransactionOperationName(pending.TransactionID, "directory-remove", index, "directory-quarantine"),
			) {
			return newMaterializationCorruption("pending directory removal %s has invalid recovery paths", directory.Path)
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
		expectedStage := path.Join(
			MaterializationStagingDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "write", index, "stage"),
		)
		if write.StagePath != expectedStage {
			return newMaterializationCorruption("pending write %s has invalid stage path", write.Path)
		}
		expectedStageProof := path.Join(
			MaterializationStagingDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "write", index, "stage-proof"),
		)
		if write.StageProofPath != expectedStageProof {
			return newMaterializationCorruption("pending write %s has invalid stage proof path", write.Path)
		}
		expectedActivation := path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "write", index, "activation"),
		)
		if write.ActivationPath != expectedActivation {
			return newMaterializationCorruption("pending write %s has invalid activation path", write.Path)
		}
		expectedQuarantine := ""
		expectedSourceProof := ""
		if write.Expected == "hash" {
			expectedSourceProof = path.Join(
				MaterializationOwnershipDirectoryName,
				materializationTransactionOperationName(pending.TransactionID, "write", index, "source-proof"),
			)
			expectedQuarantine = path.Join(
				MaterializationOwnershipDirectoryName,
				materializationTransactionOperationName(pending.TransactionID, "write", index, "quarantine"),
			)
		}
		if write.SourceProofPath != expectedSourceProof || write.QuarantinePath != expectedQuarantine {
			return newMaterializationCorruption("pending write %s has invalid quarantine path", write.Path)
		}
	}
	for index, deletion := range pending.Deletes {
		if normalized, err := normalizeMaterializationPath(deletion.Path); err != nil || normalized != deletion.Path {
			return newMaterializationCorruption("pending delete has invalid path %q", deletion.Path)
		}
		hash, err := ParseDigest(deletion.ExpectedHash)
		if err != nil || hash.IsZero() {
			return newMaterializationCorruption("pending delete %s has invalid expected hash", deletion.Path)
		}
		expectedQuarantine := path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "delete", index, "quarantine"),
		)
		expectedSourceProof := path.Join(
			MaterializationOwnershipDirectoryName,
			materializationTransactionOperationName(pending.TransactionID, "delete", index, "source-proof"),
		)
		if deletion.SourceProofPath != expectedSourceProof || deletion.QuarantinePath != expectedQuarantine {
			return newMaterializationCorruption("pending delete %s has invalid quarantine path", deletion.Path)
		}
	}
	return nil
}

func validatePendingControl(control pendingMaterializationControlWire, transactionID, kind string) error {
	if control.Expected != "absent" && control.Expected != "hash" {
		return newMaterializationCorruption("pending %s control has invalid expected state", kind)
	}
	if control.Expected == "absent" && (control.ExpectedHash != "" || control.PreviousProofPath != "") {
		return newMaterializationCorruption("pending %s absent control carries previous identity", kind)
	}
	if control.Expected == "hash" {
		expected, err := ParseDigest(control.ExpectedHash)
		if err != nil || expected.IsZero() {
			return newMaterializationCorruption("pending %s control has invalid expected hash", kind)
		}
	}
	newHash, err := ParseDigest(control.NewHash)
	if err != nil || newHash.IsZero() {
		return newMaterializationCorruption("pending %s control has invalid new hash", kind)
	}
	expected := pendingMaterializationControlWire{Expected: control.Expected}
	populatePendingControlPaths(&expected, transactionID, kind)
	if control.TemporaryPath != expected.TemporaryPath ||
		control.TemporaryProofPath != expected.TemporaryProofPath ||
		control.PreviousProofPath != expected.PreviousProofPath ||
		control.QuarantinePath != expected.QuarantinePath {
		return newMaterializationCorruption("pending %s control paths do not match the transaction", kind)
	}
	return nil
}

func pendingMaterializationTransactionID(pending pendingMaterializationWire) (Digest, error) {
	copyPending := pending
	copyPending.CreateDirectories = append([]pendingMaterializationDirectoryWire(nil), pending.CreateDirectories...)
	copyPending.Writes = append([]pendingMaterializationWriteWire(nil), pending.Writes...)
	copyPending.Deletes = append([]pendingMaterializationDeleteWire(nil), pending.Deletes...)
	copyPending.RemoveDirectories = append([]pendingMaterializationDirectoryDeleteWire(nil), pending.RemoveDirectories...)
	copyPending.TransactionID = ""
	copyPending.PendingTemporaryPath = ""
	copyPending.PendingProofPath = ""
	copyPending.StagingClaimPath = ""
	copyPending.StagingProofPath = ""
	clearPendingControlPaths := func(control *pendingMaterializationControlWire) {
		control.TemporaryPath = ""
		control.TemporaryProofPath = ""
		control.PreviousProofPath = ""
		control.QuarantinePath = ""
	}
	clearPendingControlPaths(&copyPending.InventoryControl)
	clearPendingControlPaths(&copyPending.StateControl)
	for index := range copyPending.CreateDirectories {
		copyPending.CreateDirectories[index].PreparationPath = ""
		copyPending.CreateDirectories[index].ClaimPath = ""
		copyPending.CreateDirectories[index].ClaimAnchorPath = ""
		copyPending.CreateDirectories[index].ClaimProofPath = ""
	}
	for index := range copyPending.Writes {
		copyPending.Writes[index].StagePath = ""
		copyPending.Writes[index].StageProofPath = ""
		copyPending.Writes[index].SourceProofPath = ""
		copyPending.Writes[index].ActivationPath = ""
		copyPending.Writes[index].QuarantinePath = ""
	}
	for index := range copyPending.Deletes {
		copyPending.Deletes[index].SourceProofPath = ""
		copyPending.Deletes[index].QuarantinePath = ""
	}
	for index := range copyPending.RemoveDirectories {
		copyPending.RemoveDirectories[index].ClaimPath = ""
		copyPending.RemoveDirectories[index].ClaimAnchorPath = ""
		copyPending.RemoveDirectories[index].ClaimQuarantinePath = ""
		copyPending.RemoveDirectories[index].DirectoryQuarantinePath = ""
	}
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
	if control.stateHash.String() != pending.PreviousStateHash &&
		control.stateHash.String() != pending.StateControl.NewHash {
		return newMaterializationCorruption("pending transaction does not match active state bytes")
	}
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
		for _, write := range pending.Writes {
			if write.Expected != "hash" {
				continue
			}
			if err := ensurePendingQuarantine(
				adapter, write.ArtifactID, write.Path, write.SourceProofPath, write.QuarantinePath, write.StageProofPath, write.ExpectedHash, options,
			); err != nil {
				return err
			}
		}
		for _, deletion := range pending.Deletes {
			if err := ensurePendingQuarantine(
				adapter, deletion.ArtifactID, deletion.Path, deletion.SourceProofPath, deletion.QuarantinePath, "", deletion.ExpectedHash, options,
			); err != nil {
				return err
			}
			if err := injectMaterialization(options, MaterializationFaultAfterStaleDelete); err != nil {
				return err
			}
		}
		for _, directory := range pending.RemoveDirectories {
			if err := ensurePendingDirectoryRemoval(adapter, directory); err != nil {
				return err
			}
			if err := injectMaterialization(options, MaterializationFaultAfterDirectoryCleanup); err != nil {
				return err
			}
		}
		for _, directory := range pending.CreateDirectories {
			if err := applyPendingDirectoryCreate(adapter, directory); err != nil {
				return err
			}
			if err := injectMaterialization(options, MaterializationFaultAfterDirectoryCreate); err != nil {
				return err
			}
		}
		for _, write := range pending.Writes {
			if err := applyPendingWrite(adapter, write, options); err != nil {
				return err
			}
			if err := injectMaterialization(options, MaterializationFaultAfterArtifactWrite); err != nil {
				return err
			}
		}
		if err := verifyPendingMaterialization(adapter, pending); err != nil {
			return err
		}
		if err := activatePendingControlFile(adapter, pending.InventoryControl, nextInventoryBytes, options); err != nil {
			return fmt.Errorf("activate materialization inventory: %w", err)
		}
		if err := injectMaterialization(options, MaterializationFaultAfterInventoryActivation); err != nil {
			return err
		}
	} else if err := verifyActivatedPendingControl(adapter, pending.InventoryControl, nextInventoryBytes); err != nil {
		return fmt.Errorf("verify activated materialization inventory: %w", err)
	}

	activeState := materializationStateWire{
		SchemaVersion:       MaterializationInventorySchemaVersion,
		Phase:               materializationPhaseActive,
		ActiveInventoryHash: nextInventoryHash.String(),
	}
	activeStateBytes, err := marshalMaterializationState(activeState)
	if err != nil {
		return err
	}
	if err := activatePendingControlFile(adapter, pending.StateControl, activeStateBytes, options); err != nil {
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
		if _, exists := nextDirectories[directory.Path]; !exists {
			return newMaterializationCorruption("pending directory create %s is not owned by the next inventory", directory.Path)
		}
		if prior := seenDirectories[directory.Path]; prior != "" {
			return newMaterializationCorruption("pending directory %s has duplicate %s and create operations", directory.Path, prior)
		}
		seenDirectories[directory.Path] = "create"
	}
	for _, directory := range pending.RemoveDirectories {
		if _, exists := previousDirectories[directory.Path]; !exists {
			return newMaterializationCorruption("pending directory removal %s is not authorized by the active inventory", directory.Path)
		}
		if _, remains := nextDirectories[directory.Path]; remains {
			return newMaterializationCorruption("pending directory removal %s remains owned by the next inventory", directory.Path)
		}
		if prior := seenDirectories[directory.Path]; prior != "" {
			return newMaterializationCorruption("pending directory %s has both %s and remove operations", directory.Path, prior)
		}
		seenDirectories[directory.Path] = "remove"
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
	if len(entries) != len(pending.Writes)*2+1 {
		return newMaterializationCorruption("pending staging directory contains %d entries; expected %d", len(entries), len(pending.Writes)*2+1)
	}
	proofContent, err := adapter.readBounded(pending.StagingProofPath, 8*1024)
	if err != nil || string(proofContent) != "materialization-staging:"+pending.TransactionID+"\n" {
		if err == nil {
			err = fmt.Errorf("staging proof content changed")
		}
		return newMaterializationCorruption("verify staging proof: %v", err)
	}
	same, err := adapter.sameFile(pending.StagingClaimPath, pending.StagingProofPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("staging directory identity changed")
		}
		return newMaterializationCorruption("verify staging directory identity: %v", err)
	}
	expected := make(map[string]struct{}, len(pending.Writes)*2+1)
	expected[path.Base(pending.StagingClaimPath)] = struct{}{}
	for _, write := range pending.Writes {
		expected[path.Base(write.StagePath)] = struct{}{}
		expected[path.Base(write.StageProofPath)] = struct{}{}
		same, err := adapter.sameFile(write.StagePath, write.StageProofPath)
		if err != nil || !same {
			if err == nil {
				err = fmt.Errorf("staged identity changed")
			}
			return newMaterializationCorruption("verify staged identity for %s: %v", write.Path, err)
		}
		hash, err := readMaterializationHash(adapter, write.StageProofPath)
		if err != nil {
			return err
		}
		if hash.String() != write.NewHash {
			return newMaterializationCorruption("staged bytes for %s do not match pending hash", write.Path)
		}
	}
	for _, entry := range entries {
		_, exists := expected[entry.name]
		if !exists || !entry.info.Mode().IsRegular() {
			return newMaterializationCorruption("unexpected staging entry %s", entry.name)
		}
	}
	return nil
}

func ensurePendingQuarantine(
	adapter *RootedFilesystemAdapter,
	artifactID string,
	target string,
	sourceProof string,
	quarantine string,
	activation string,
	expectedHash string,
	options MaterializationOptions,
) error {
	if info, exists, err := adapter.inspectExact(sourceProof); err != nil || !exists || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("transaction source proof is missing or invalid")
		}
		return materializationApplyConflict(artifactID, target, MaterializationConflictModifiedOwnedPath, err.Error())
	}
	quarantineInfo, quarantineExists, err := adapter.inspectExact(quarantine)
	if err != nil {
		return materializationApplyConflict(artifactID, target, MaterializationConflictUnsafePath, err.Error())
	}
	if quarantineExists {
		if !quarantineInfo.Mode().IsRegular() {
			return materializationApplyConflict(
				artifactID, target, MaterializationConflictUnsafePath,
				"transaction quarantine is not a regular file",
			)
		}
		same, err := adapter.sameFile(quarantine, sourceProof)
		if err != nil || !same {
			if err == nil {
				err = fmt.Errorf("quarantine identity does not match the transaction source proof")
			}
			return materializationApplyConflict(artifactID, target, MaterializationConflictModifiedOwnedPath, err.Error())
		}
		targetInfo, targetExists, err := adapter.inspectExact(target)
		if err != nil {
			return materializationApplyConflict(artifactID, target, MaterializationConflictUnsafePath, err.Error())
		}
		if targetExists {
			installedByTransaction := false
			if activation != "" && targetInfo.Mode().IsRegular() {
				if _, activationExists, inspectErr := adapter.inspectExact(activation); inspectErr != nil {
					return materializationApplyConflict(artifactID, target, MaterializationConflictUnsafePath, inspectErr.Error())
				} else if activationExists {
					installedByTransaction, err = adapter.sameFile(target, activation)
					if err != nil {
						return materializationApplyConflict(artifactID, target, MaterializationConflictUnsafePath, err.Error())
					}
				}
			}
			if !installedByTransaction {
				return materializationApplyConflict(
					artifactID, target, MaterializationConflictModifiedOwnedPath,
					"a quarantine exists but the owned target was independently recreated",
				)
			}
		}
		if err := verifyPendingQuarantineHash(adapter, artifactID, target, quarantine, expectedHash); err != nil {
			if !targetExists {
				if restoreErr := adapter.renameFileNoReplace(quarantine, target); restoreErr != nil {
					return fmt.Errorf("%w; restore changed quarantine: %v", err, restoreErr)
				}
			}
			return err
		}
		return nil
	}

	targetInfo, targetExists, err := adapter.inspectExact(target)
	if err != nil {
		return materializationApplyConflict(artifactID, target, MaterializationConflictUnsafePath, err.Error())
	}
	if !targetExists {
		return materializationApplyConflict(
			artifactID, target, MaterializationConflictMissingOwnedPath,
			"owned target disappeared before it could be quarantined",
		)
	}
	if !targetInfo.Mode().IsRegular() {
		return materializationApplyConflict(
			artifactID, target, MaterializationConflictModifiedOwnedPath,
			"owned target is no longer a regular file",
		)
	}
	if err := verifyPendingSourceProof(adapter, artifactID, target, sourceProof, expectedHash); err != nil {
		return err
	}
	same, err := adapter.sameFile(target, sourceProof)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("owned target identity changed after the transaction source proof was captured")
		}
		return materializationApplyConflict(artifactID, target, MaterializationConflictModifiedOwnedPath, err.Error())
	}
	if err := adapter.renameFileNoReplace(target, quarantine); err != nil {
		return materializationApplyConflict(
			artifactID, target, MaterializationConflictModifiedOwnedPath,
			fmt.Sprintf("quarantine owned target without replacement: %v", err),
		)
	}
	if err := injectMaterialization(options, MaterializationFaultAfterQuarantine); err != nil {
		return err
	}
	same, err = adapter.sameFile(quarantine, sourceProof)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("quarantined source identity changed")
		}
		if restoreErr := adapter.renameFileNoReplace(quarantine, target); restoreErr != nil {
			return fmt.Errorf("%v; restore quarantined target: %v", err, restoreErr)
		}
		return materializationApplyConflict(artifactID, target, MaterializationConflictModifiedOwnedPath, err.Error())
	}
	if err := verifyPendingQuarantineHash(adapter, artifactID, target, quarantine, expectedHash); err != nil {
		if restoreErr := adapter.renameFileNoReplace(quarantine, target); restoreErr != nil {
			return fmt.Errorf("%w; restore quarantined target: %v", err, restoreErr)
		}
		return err
	}
	return nil
}

func verifyPendingSourceProof(
	adapter *RootedFilesystemAdapter,
	artifactID string,
	target string,
	sourceProof string,
	expectedHash string,
) error {
	info, exists, err := adapter.inspectExact(sourceProof)
	if err != nil || !exists || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("transaction source proof is missing or invalid")
		}
		return materializationApplyConflict(artifactID, target, MaterializationConflictModifiedOwnedPath, err.Error())
	}
	current, err := readMaterializationHash(adapter, sourceProof)
	if err != nil || current.String() != expectedHash {
		if err == nil {
			err = fmt.Errorf("transaction source proof bytes changed")
		}
		return materializationApplyConflict(artifactID, target, MaterializationConflictModifiedOwnedPath, err.Error())
	}
	return nil
}

func verifyPendingQuarantineHash(
	adapter *RootedFilesystemAdapter,
	artifactID, target, quarantine, expectedHash string,
) error {
	current, err := readMaterializationHash(adapter, quarantine)
	if err != nil {
		return materializationApplyConflict(artifactID, target, MaterializationConflictUnsafePath, err.Error())
	}
	if current.String() != expectedHash {
		return materializationApplyConflict(
			artifactID, target, MaterializationConflictModifiedOwnedPath,
			"owned bytes changed before the stable quarantine was verified",
		)
	}
	return nil
}

func applyPendingDirectoryCreate(
	adapter *RootedFilesystemAdapter,
	directory pendingMaterializationDirectoryWire,
) error {
	same, err := adapter.sameFile(directory.ClaimAnchorPath, directory.ClaimProofPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("directory anchor identity changed")
		}
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnownedPath, err.Error())
	}
	claimContent, err := adapter.readBounded(directory.ClaimAnchorPath, 8*1024)
	if err != nil || string(claimContent) != string(materializationDirectoryClaimContent(directory.Path)) {
		if err == nil {
			err = fmt.Errorf("directory anchor content changed")
		}
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnownedPath, err.Error())
	}
	info, exists, err := adapter.inspectExact(directory.Path)
	if err != nil {
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, err.Error())
	}
	if exists {
		if !info.IsDir() {
			return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, "target is not a directory")
		}
		if err := verifyOwnedMaterializationDirectory(
			adapter, directory.Path, directory.ClaimPath, directory.ClaimAnchorPath,
		); err != nil {
			return materializationApplyConflict(
				"", directory.Path, MaterializationConflictUnownedPath,
				"directory exists without this transaction's ownership claim",
			)
		}
		if _, preparationExists, err := adapter.inspectExact(directory.PreparationPath); err != nil {
			return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, err.Error())
		} else if preparationExists {
			return materializationApplyConflict(
				"", directory.Path, MaterializationConflictUnsafePath,
				"both the activated directory and its preparation path exist",
			)
		}
		return nil
	}

	preparationInfo, preparationExists, err := adapter.inspectExact(directory.PreparationPath)
	if err != nil {
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, err.Error())
	}
	createdPreparation := false
	if !preparationExists {
		created, err := adapter.makeDirectory(directory.PreparationPath, 0o700)
		if err != nil {
			return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, err.Error())
		}
		if !created {
			return materializationApplyConflict(
				"", directory.Path, MaterializationConflictUnownedPath,
				"directory preparation path appeared after transaction preflight",
			)
		}
		createdPreparation = true
	} else if !preparationInfo.IsDir() {
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, "directory preparation path is not a directory")
	}

	preparationClaim := path.Join(directory.PreparationPath, path.Base(directory.ClaimPath))
	entries, err := adapter.readDirectory(directory.PreparationPath)
	if err != nil {
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, err.Error())
	}
	if createdPreparation {
		if len(entries) != 0 {
			return materializationApplyConflict(
				"", directory.Path, MaterializationConflictUnownedPath,
				"new directory preparation path unexpectedly contains entries",
			)
		}
		if err := adapter.linkFileNoReplaceVerified(
			directory.ClaimAnchorPath, directory.ClaimProofPath, preparationClaim,
		); err != nil {
			return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, err.Error())
		}
	} else if len(entries) != 1 || entries[0].name != path.Base(preparationClaim) {
		return materializationApplyConflict(
			"", directory.Path, MaterializationConflictUnownedPath,
			"directory preparation path contains unowned entries",
		)
	}
	same, err = adapter.sameFile(preparationClaim, directory.ClaimAnchorPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("directory preparation claim identity changed")
		}
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnownedPath, err.Error())
	}
	if err := adapter.renameDirectoryNoReplace(directory.PreparationPath, directory.Path); err != nil {
		if _, appeared, inspectErr := adapter.inspectExact(directory.Path); inspectErr == nil && appeared {
			return materializationApplyConflict(
				"", directory.Path, MaterializationConflictUnownedPath,
				"directory appeared before no-replace activation",
			)
		}
		return materializationApplyConflict("", directory.Path, MaterializationConflictUnsafePath, err.Error())
	}
	if err := verifyOwnedMaterializationDirectory(
		adapter, directory.Path, directory.ClaimPath, directory.ClaimAnchorPath,
	); err != nil {
		return newMaterializationCorruption("activated directory %s lost its transaction claim", directory.Path)
	}
	return nil
}

func ensurePendingDirectoryRemoval(
	adapter *RootedFilesystemAdapter,
	directory pendingMaterializationDirectoryDeleteWire,
) error {
	if info, exists, err := adapter.inspectExact(directory.DirectoryQuarantinePath); err != nil {
		return err
	} else if exists {
		if !info.IsDir() {
			return newMaterializationCorruption("directory quarantine for %s is not a directory", directory.Path)
		}
		if _, targetExists, err := adapter.inspectExact(directory.Path); err != nil {
			return err
		} else if targetExists {
			return materializationApplyConflict(
				"", directory.Path, MaterializationConflictUnownedPath,
				"directory path reappeared after the owned instance was quarantined",
			)
		}
		return verifyPendingDirectoryQuarantine(adapter, directory)
	}
	if info, exists, err := adapter.inspectExact(directory.ClaimQuarantinePath); err != nil {
		return err
	} else if exists {
		if !info.Mode().IsRegular() {
			return newMaterializationCorruption("directory claim quarantine for %s is not a regular file", directory.Path)
		}
		same, err := adapter.sameFile(directory.ClaimQuarantinePath, directory.ClaimAnchorPath)
		if err != nil || !same {
			if err == nil {
				err = fmt.Errorf("directory claim quarantine identity changed")
			}
			return newMaterializationCorruption("verify directory claim quarantine for %s: %v", directory.Path, err)
		}
		return nil
	}
	if err := verifyOwnedMaterializationDirectory(
		adapter, directory.Path, directory.ClaimPath, directory.ClaimAnchorPath,
	); err != nil {
		return materializationApplyConflict(
			"", directory.Path, MaterializationConflictModifiedOwnedPath,
			"owned directory instance changed before cleanup: "+err.Error(),
		)
	}
	entries, err := adapter.readDirectory(directory.Path)
	if err != nil {
		return err
	}
	if len(entries) == 1 && entries[0].name == MaterializationDirectoryClaimFileName {
		if err := adapter.renameDirectoryNoReplace(directory.Path, directory.DirectoryQuarantinePath); err != nil {
			return materializationApplyConflict(
				"", directory.Path, MaterializationConflictModifiedOwnedPath,
				"quarantine exact owned directory: "+err.Error(),
			)
		}
		if err := verifyPendingDirectoryQuarantine(adapter, directory); err != nil {
			if restoreErr := adapter.renameDirectoryNoReplace(directory.DirectoryQuarantinePath, directory.Path); restoreErr != nil {
				return fmt.Errorf("%w; restore directory quarantine: %v", err, restoreErr)
			}
			return err
		}
		return nil
	}
	if err := adapter.renameFileNoReplace(directory.ClaimPath, directory.ClaimQuarantinePath); err != nil {
		return materializationApplyConflict(
			"", directory.Path, MaterializationConflictModifiedOwnedPath,
			"quarantine exact directory claim: "+err.Error(),
		)
	}
	same, err := adapter.sameFile(directory.ClaimQuarantinePath, directory.ClaimAnchorPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("directory claim quarantine identity changed")
		}
		if restoreErr := adapter.renameFileNoReplace(directory.ClaimQuarantinePath, directory.ClaimPath); restoreErr != nil {
			return fmt.Errorf("%v; restore directory claim: %v", err, restoreErr)
		}
		return materializationApplyConflict("", directory.Path, MaterializationConflictModifiedOwnedPath, err.Error())
	}
	return nil
}

func verifyPendingDirectoryQuarantine(
	adapter *RootedFilesystemAdapter,
	directory pendingMaterializationDirectoryDeleteWire,
) error {
	claim := path.Join(directory.DirectoryQuarantinePath, MaterializationDirectoryClaimFileName)
	same, err := adapter.sameFile(claim, directory.ClaimAnchorPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("directory quarantine claim identity changed")
		}
		return newMaterializationCorruption("verify quarantined directory %s: %v", directory.Path, err)
	}
	content, err := adapter.readBounded(claim, 8*1024)
	if err != nil || string(content) != string(materializationDirectoryClaimContent(directory.Path)) {
		if err == nil {
			err = fmt.Errorf("directory quarantine claim content changed")
		}
		return newMaterializationCorruption("verify quarantined directory %s: %v", directory.Path, err)
	}
	entries, err := adapter.readDirectory(directory.DirectoryQuarantinePath)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].name != MaterializationDirectoryClaimFileName {
		return newMaterializationCorruption(
			"quarantined directory %s contains unowned entries and will be preserved",
			directory.Path,
		)
	}
	return nil
}

func applyPendingWrite(
	adapter *RootedFilesystemAdapter,
	write pendingMaterializationWriteWire,
	options MaterializationOptions,
) error {
	if err := ensurePendingActivation(adapter, write, options); err != nil {
		return err
	}
	info, exists, err := adapter.inspectExact(write.Path)
	if err != nil {
		return materializationApplyConflict(write.ArtifactID, write.Path, MaterializationConflictUnsafePath, err.Error())
	}
	if exists {
		if !info.Mode().IsRegular() {
			return materializationApplyConflict(write.ArtifactID, write.Path, MaterializationConflictUnsafePath, "target is not a regular file")
		}
		same, err := adapter.sameFile(write.Path, write.StageProofPath)
		if err != nil {
			return materializationApplyConflict(write.ArtifactID, write.Path, MaterializationConflictUnsafePath, err.Error())
		}
		if !same {
			kind := MaterializationConflictUnownedPath
			if write.Expected == "hash" {
				kind = MaterializationConflictModifiedOwnedPath
			}
			return materializationApplyConflict(
				write.ArtifactID, write.Path, kind,
				"target exists without this transaction's activation identity",
			)
		}
	} else {
		if err := adapter.linkFileNoReplaceVerified(write.StageProofPath, write.StagePath, write.Path); err != nil {
			if _, appeared, inspectErr := adapter.inspectExact(write.Path); inspectErr == nil && appeared {
				kind := MaterializationConflictUnownedPath
				if write.Expected == "hash" {
					kind = MaterializationConflictModifiedOwnedPath
				}
				return materializationApplyConflict(
					write.ArtifactID, write.Path, kind,
					"target appeared before no-replace activation",
				)
			}
			return fmt.Errorf("activate materialized artifact %s: %w", write.Path, err)
		}
	}
	current, err := readMaterializationHash(adapter, write.Path)
	if err != nil {
		return err
	}
	if current.String() != write.NewHash {
		return materializationApplyConflict(
			write.ArtifactID, write.Path, MaterializationConflictModifiedOwnedPath,
			"activated artifact bytes changed before inventory activation",
		)
	}
	return nil
}

func ensurePendingActivation(
	adapter *RootedFilesystemAdapter,
	write pendingMaterializationWriteWire,
	options MaterializationOptions,
) error {
	same, err := adapter.sameFile(write.StagePath, write.StageProofPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("staged identity changed")
		}
		return newMaterializationCorruption("verify staged identity for %s: %v", write.Path, err)
	}
	stagedHash, err := readMaterializationHash(adapter, write.StageProofPath)
	if err != nil || stagedHash.String() != write.NewHash {
		if err == nil {
			err = fmt.Errorf("staged bytes changed")
		}
		return newMaterializationCorruption("verify staged bytes for %s: %v", write.Path, err)
	}
	info, exists, err := adapter.inspectExact(write.ActivationPath)
	if err != nil {
		return materializationApplyConflict(write.ArtifactID, write.Path, MaterializationConflictUnsafePath, err.Error())
	}
	if exists {
		if !info.Mode().IsRegular() {
			return newMaterializationCorruption("activation path for %s is not a regular file", write.Path)
		}
		same, err := adapter.sameFile(write.ActivationPath, write.StageProofPath)
		if err != nil || !same {
			if err == nil {
				err = fmt.Errorf("activation identity does not match the staged transaction object")
			}
			return materializationApplyConflict(
				write.ArtifactID, write.Path, MaterializationConflictUnownedPath,
				"preserve unowned activation path: "+err.Error(),
			)
		}
	} else {
		if err := adapter.linkFileNoReplaceVerified(
			write.StageProofPath, write.StagePath, write.ActivationPath,
		); err != nil {
			return fmt.Errorf("create activation identity for %s: %w", write.Path, err)
		}
	}
	if err := injectMaterialization(options, MaterializationFaultAfterTemporarySync); err != nil {
		return err
	}
	activationHash, err := readMaterializationHash(adapter, write.ActivationPath)
	if err != nil || activationHash.String() != write.NewHash {
		if err == nil {
			err = fmt.Errorf("activation bytes changed")
		}
		return newMaterializationCorruption("verify activation for %s: %v", write.Path, err)
	}
	return nil
}

func activatePendingControlFile(
	adapter *RootedFilesystemAdapter,
	control pendingMaterializationControlWire,
	content []byte,
	options MaterializationOptions,
) error {
	if DigestBytes(content).String() != control.NewHash {
		return newMaterializationCorruption("new control bytes for %s do not match pending hash", control.TargetPath)
	}
	if activated, err := pendingControlIdentityMatches(
		adapter, control.TargetPath, control.TemporaryProofPath, control.NewHash,
	); err != nil {
		return err
	} else if activated {
		return nil
	}
	if matched, err := pendingControlIdentityMatches(
		adapter, control.TemporaryPath, control.TemporaryProofPath, control.NewHash,
	); err != nil {
		return err
	} else if !matched {
		return newMaterializationCorruption("control temporary %s is missing", control.TemporaryPath)
	}
	quarantineMatched, err := pendingControlIdentityMatches(
		adapter, control.QuarantinePath, control.PreviousProofPath, control.ExpectedHash,
	)
	if err != nil {
		return err
	}
	if quarantineMatched {
		if _, targetExists, err := adapter.inspectExact(control.TargetPath); err != nil {
			return err
		} else if targetExists {
			return newMaterializationCorruption(
				"control target %s appeared after its prior identity was quarantined and will be preserved",
				control.TargetPath,
			)
		}
	} else {
		_, targetExists, err := adapter.inspectExact(control.TargetPath)
		if err != nil {
			return err
		}
		switch control.Expected {
		case "absent":
			if targetExists {
				return newMaterializationCorruption(
					"control target %s appeared after an absent precondition and will be preserved",
					control.TargetPath,
				)
			}
		case "hash":
			if !targetExists {
				return newMaterializationCorruption("expected control target %s disappeared", control.TargetPath)
			}
			matched, err := pendingControlIdentityMatches(
				adapter, control.TargetPath, control.PreviousProofPath, control.ExpectedHash,
			)
			if err != nil {
				return err
			}
			if !matched {
				return newMaterializationCorruption(
					"control target %s no longer matches its transaction identity and will be preserved",
					control.TargetPath,
				)
			}
			if err := adapter.renameFileNoReplace(control.TargetPath, control.QuarantinePath); err != nil {
				return fmt.Errorf("quarantine prior control %s: %w", control.TargetPath, err)
			}
			matched, err = pendingControlIdentityMatches(
				adapter, control.QuarantinePath, control.PreviousProofPath, control.ExpectedHash,
			)
			if err != nil || !matched {
				if restoreErr := adapter.renameFileNoReplace(control.QuarantinePath, control.TargetPath); restoreErr != nil {
					return fmt.Errorf("verify quarantined control %s: %v; restore: %v", control.TargetPath, err, restoreErr)
				}
				return newMaterializationCorruption("quarantined control %s lost its transaction identity", control.TargetPath)
			}
		}
	}
	if err := injectMaterialization(options, MaterializationFaultAfterTemporarySync); err != nil {
		return err
	}
	if err := adapter.renameFileNoReplace(control.TemporaryPath, control.TargetPath); err != nil {
		return fmt.Errorf("activate control %s without replacement: %w", control.TargetPath, err)
	}
	if err := verifyActivatedPendingControl(adapter, control, content); err != nil {
		if restoreErr := adapter.renameFileNoReplace(control.TargetPath, control.TemporaryPath); restoreErr != nil {
			return fmt.Errorf("%w; restore unproved control temporary: %v", err, restoreErr)
		}
		return err
	}
	return nil
}

func verifyActivatedPendingControl(
	adapter *RootedFilesystemAdapter,
	control pendingMaterializationControlWire,
	content []byte,
) error {
	if DigestBytes(content).String() != control.NewHash {
		return newMaterializationCorruption("activated control %s has an invalid pending hash", control.TargetPath)
	}
	matched, err := pendingControlIdentityMatches(
		adapter, control.TargetPath, control.TemporaryProofPath, control.NewHash,
	)
	if err != nil {
		return err
	}
	if !matched {
		return newMaterializationCorruption("activated control %s lost its transaction identity", control.TargetPath)
	}
	return nil
}

func pendingControlIdentityMatches(
	adapter *RootedFilesystemAdapter,
	relative string,
	proof string,
	expectedHash string,
) (bool, error) {
	if relative == "" || proof == "" || expectedHash == "" {
		return false, nil
	}
	_, exists, err := adapter.inspectExact(relative)
	if err != nil || !exists {
		return false, err
	}
	same, err := adapter.sameFile(relative, proof)
	if err != nil {
		return false, err
	}
	if !same {
		return false, nil
	}
	content, err := adapter.readBounded(relative, MaxMaterializationControlBytes)
	if err != nil {
		return false, err
	}
	if DigestBytes(content).String() != expectedHash {
		return false, nil
	}
	return true, nil
}

func verifyPendingMaterialization(adapter *RootedFilesystemAdapter, pending pendingMaterializationWire) error {
	writesByPath := make(map[string]pendingMaterializationWriteWire, len(pending.Writes))
	for _, write := range pending.Writes {
		writesByPath[write.Path] = write
		if write.QuarantinePath != "" {
			same, err := adapter.sameFile(write.QuarantinePath, write.SourceProofPath)
			if err != nil || !same {
				return newMaterializationCorruption("updated artifact %s lost its quarantine identity", write.Path)
			}
			if err := verifyPendingQuarantineHash(
				adapter, write.ArtifactID, write.Path, write.QuarantinePath, write.ExpectedHash,
			); err != nil {
				return err
			}
		}
	}
	for _, directory := range pending.CreateDirectories {
		if err := verifyOwnedMaterializationDirectory(
			adapter, directory.Path, directory.ClaimPath, directory.ClaimAnchorPath,
		); err != nil {
			return newMaterializationCorruption("directory %s lost its transaction claim before inventory activation", directory.Path)
		}
		same, err := adapter.sameFile(directory.ClaimAnchorPath, directory.ClaimProofPath)
		if err != nil || !same {
			return newMaterializationCorruption("directory %s lost its prepared anchor identity", directory.Path)
		}
	}
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
		if write, changed := writesByPath[artifact.Path]; changed {
			same, err := adapter.sameFile(artifact.Path, write.StageProofPath)
			if err != nil || !same {
				return materializationApplyConflict(
					artifact.ArtifactID, artifact.Path, MaterializationConflictModifiedOwnedPath,
					"materialized target lost its transaction activation identity",
				)
			}
		}
	}
	for _, deletion := range pending.Deletes {
		same, err := adapter.sameFile(deletion.QuarantinePath, deletion.SourceProofPath)
		if err != nil || !same {
			return newMaterializationCorruption("deleted artifact %s lost its quarantine identity", deletion.Path)
		}
		if err := verifyPendingQuarantineHash(
			adapter, deletion.ArtifactID, deletion.Path, deletion.QuarantinePath, deletion.ExpectedHash,
		); err != nil {
			return err
		}
		if _, exists, err := adapter.inspectExact(deletion.Path); err != nil {
			return err
		} else if exists {
			return materializationApplyConflict(
				deletion.ArtifactID, deletion.Path, MaterializationConflictModifiedOwnedPath,
				"stale owned path reappeared before inventory activation",
			)
		}
	}
	for _, directory := range pending.RemoveDirectories {
		if _, exists, err := adapter.inspectExact(directory.DirectoryQuarantinePath); err != nil {
			return err
		} else if exists {
			if err := verifyPendingDirectoryQuarantine(adapter, directory); err != nil {
				return err
			}
			if _, targetExists, err := adapter.inspectExact(directory.Path); err != nil || targetExists {
				if err == nil {
					err = fmt.Errorf("directory path reappeared")
				}
				return newMaterializationCorruption("removed directory %s changed before inventory activation: %v", directory.Path, err)
			}
			continue
		}
		same, err := adapter.sameFile(directory.ClaimQuarantinePath, directory.ClaimAnchorPath)
		if err != nil || !same {
			return newMaterializationCorruption("removed directory %s lost its claim quarantine identity", directory.Path)
		}
	}
	return nil
}

func cleanupPendingMaterialization(
	adapter *RootedFilesystemAdapter,
	pending pendingMaterializationWire,
) error {
	state, err := readMaterializationState(adapter)
	if err != nil {
		return err
	}
	if state.Phase != materializationPhaseActive || state.ActiveInventoryHash != pending.NextInventoryHash {
		return newMaterializationCorruption("pending cleanup requires the next inventory to be active")
	}

	for _, write := range pending.Writes {
		if write.QuarantinePath == "" {
			continue
		}
		if err := cleanupPendingQuarantine(
			adapter, write.ArtifactID, write.Path, write.SourceProofPath, write.QuarantinePath, write.ExpectedHash, write.StageProofPath,
		); err != nil {
			return err
		}
	}
	for _, deletion := range pending.Deletes {
		if err := cleanupPendingQuarantine(
			adapter, deletion.ArtifactID, deletion.Path, deletion.SourceProofPath, deletion.QuarantinePath, deletion.ExpectedHash, "",
		); err != nil {
			return err
		}
	}
	for _, directory := range pending.RemoveDirectories {
		if err := cleanupPendingDirectoryRemoval(adapter, directory); err != nil {
			return err
		}
	}
	for _, write := range pending.Writes {
		if err := cleanupPendingActivation(adapter, write); err != nil {
			return err
		}
	}
	for _, directory := range pending.CreateDirectories {
		if err := verifyOwnedMaterializationDirectory(
			adapter, directory.Path, directory.ClaimPath, directory.ClaimAnchorPath,
		); err != nil {
			return newMaterializationCorruption("preserve directory proof for %s: %v", directory.Path, err)
		}
		same, err := adapter.sameFile(directory.ClaimAnchorPath, directory.ClaimProofPath)
		if err != nil || !same {
			return newMaterializationCorruption("preserve directory transaction proof for %s because its identity changed", directory.Path)
		}
		if err := adapter.removeFile(directory.ClaimProofPath); err != nil {
			return err
		}
	}

	nextInventory, err := normalizeMaterializationInventoryWire(pending.NextInventory)
	if err != nil {
		return err
	}
	nextInventoryBytes, _, err := marshalMaterializationInventory(nextInventory)
	if err != nil {
		return err
	}
	activeStateBytes, err := marshalMaterializationState(materializationStateWire{
		SchemaVersion:       MaterializationInventorySchemaVersion,
		Phase:               materializationPhaseActive,
		ActiveInventoryHash: pending.NextInventoryHash,
	})
	if err != nil {
		return err
	}
	if err := cleanupPendingControl(adapter, pending.InventoryControl, nextInventoryBytes); err != nil {
		return err
	}
	if err := cleanupPendingControl(adapter, pending.StateControl, activeStateBytes); err != nil {
		return err
	}
	if err := cleanupPendingStaging(adapter, pending); err != nil {
		return err
	}
	same, err := adapter.sameFile(MaterializationPendingFileName, pending.PendingProofPath)
	if err != nil || !same {
		return newMaterializationCorruption("preserve completed pending transaction because its identity changed")
	}
	pendingBytes, err := marshalPendingMaterialization(pending)
	if err != nil {
		return err
	}
	if err := removePendingFileWithContent(adapter, MaterializationPendingFileName, pendingBytes, MaxMaterializationControlBytes); err != nil {
		return fmt.Errorf("remove completed pending transaction: %w", err)
	}
	if err := adapter.removeFile(pending.PendingProofPath); err != nil {
		return fmt.Errorf("remove completed pending transaction proof: %w", err)
	}
	return nil
}

func cleanupPendingQuarantine(
	adapter *RootedFilesystemAdapter,
	artifactID, target, sourceProof, quarantine, expectedHash string,
	activation string,
) error {
	_, exists, err := adapter.inspectExact(quarantine)
	if err != nil || !exists {
		return err
	}
	if err := verifyPendingQuarantineHash(adapter, artifactID, target, quarantine, expectedHash); err != nil {
		return err
	}
	same, err := adapter.sameFile(quarantine, sourceProof)
	if err != nil || !same {
		return newMaterializationCorruption("quarantine for %s lost its source identity before cleanup", target)
	}
	_, targetExists, err := adapter.inspectExact(target)
	if err != nil {
		return err
	}
	if activation == "" && targetExists {
		return newMaterializationCorruption("target %s changed while its quarantine awaited cleanup", target)
	}
	if activation != "" {
		if !targetExists {
			return newMaterializationCorruption("updated target %s disappeared while its quarantine awaited cleanup", target)
		}
		same, err := adapter.sameFile(target, activation)
		if err != nil || !same {
			return newMaterializationCorruption("updated target %s lost its activation identity before quarantine cleanup", target)
		}
	}
	if err := adapter.removeFile(quarantine); err != nil {
		return err
	}
	return adapter.removeFile(sourceProof)
}

func cleanupPendingDirectoryRemoval(
	adapter *RootedFilesystemAdapter,
	directory pendingMaterializationDirectoryDeleteWire,
) error {
	if _, exists, err := adapter.inspectExact(directory.DirectoryQuarantinePath); err != nil {
		return err
	} else if exists {
		if err := verifyPendingDirectoryQuarantine(adapter, directory); err != nil {
			return err
		}
		claim := path.Join(directory.DirectoryQuarantinePath, MaterializationDirectoryClaimFileName)
		if err := adapter.renameFileNoReplace(claim, directory.ClaimQuarantinePath); err != nil {
			return fmt.Errorf("quarantine persistent claim for removed directory %s: %w", directory.Path, err)
		}
		same, err := adapter.sameFile(directory.ClaimQuarantinePath, directory.ClaimAnchorPath)
		if err != nil || !same {
			if restoreErr := adapter.renameFileNoReplace(directory.ClaimQuarantinePath, claim); restoreErr != nil {
				return fmt.Errorf("verify removed directory claim: %v; restore: %v", err, restoreErr)
			}
			return newMaterializationCorruption("removed directory %s claim identity changed", directory.Path)
		}
		removed, err := adapter.removeEmptyDirectory(directory.DirectoryQuarantinePath)
		if err != nil {
			return err
		}
		if !removed {
			return newMaterializationCorruption("removed directory quarantine %s is not empty", directory.Path)
		}
	}
	same, err := adapter.sameFile(directory.ClaimQuarantinePath, directory.ClaimAnchorPath)
	if err != nil || !same {
		return newMaterializationCorruption("removed directory %s lost its durable claim before cleanup", directory.Path)
	}
	content, err := adapter.readBounded(directory.ClaimQuarantinePath, 8*1024)
	if err != nil || string(content) != string(materializationDirectoryClaimContent(directory.Path)) {
		return newMaterializationCorruption("removed directory %s claim bytes changed before cleanup", directory.Path)
	}
	if err := adapter.removeFile(directory.ClaimQuarantinePath); err != nil {
		return err
	}
	return adapter.removeFile(directory.ClaimAnchorPath)
}

func cleanupPendingControl(
	adapter *RootedFilesystemAdapter,
	control pendingMaterializationControlWire,
	content []byte,
) error {
	if err := verifyActivatedPendingControl(adapter, control, content); err != nil {
		return err
	}
	if _, exists, err := adapter.inspectExact(control.TemporaryPath); err != nil {
		return err
	} else if exists {
		return newMaterializationCorruption("control temporary %s reappeared after activation", control.TemporaryPath)
	}
	if control.Expected == "hash" {
		matched, err := pendingControlIdentityMatches(
			adapter, control.QuarantinePath, control.PreviousProofPath, control.ExpectedHash,
		)
		if err != nil || !matched {
			return newMaterializationCorruption("prior control quarantine for %s lost its identity", control.TargetPath)
		}
		if err := adapter.removeFile(control.QuarantinePath); err != nil {
			return err
		}
		if err := adapter.removeFile(control.PreviousProofPath); err != nil {
			return err
		}
	} else if _, exists, err := adapter.inspectExact(control.QuarantinePath); err != nil || exists {
		if err == nil {
			err = fmt.Errorf("unexpected bootstrap control quarantine exists")
		}
		return err
	}
	return adapter.removeFile(control.TemporaryProofPath)
}

func cleanupPendingActivation(
	adapter *RootedFilesystemAdapter,
	write pendingMaterializationWriteWire,
) error {
	_, exists, err := adapter.inspectExact(write.ActivationPath)
	if err != nil || !exists {
		return err
	}
	same, err := adapter.sameFile(write.Path, write.ActivationPath)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("activation identity no longer matches its target")
		}
		return newMaterializationCorruption("preserve activation for %s: %v", write.Path, err)
	}
	same, err = adapter.sameFile(write.ActivationPath, write.StageProofPath)
	if err != nil || !same {
		return newMaterializationCorruption("preserve activation for %s because its staged identity changed", write.Path)
	}
	return adapter.removeFile(write.ActivationPath)
}

func removePendingFileWithContent(
	adapter *RootedFilesystemAdapter,
	relative string,
	expected []byte,
	maximum int64,
) error {
	_, exists, err := adapter.inspectExact(relative)
	if err != nil || !exists {
		return err
	}
	content, err := adapter.readBounded(relative, maximum)
	if err != nil {
		return err
	}
	if string(content) != string(expected) {
		return newMaterializationCorruption("preserve transaction file %s because its bytes changed", relative)
	}
	return adapter.removeFile(relative)
}

func cleanupPendingStaging(adapter *RootedFilesystemAdapter, pending pendingMaterializationWire) error {
	_, exists, err := adapter.inspectExact(MaterializationStagingDirectoryName)
	if err != nil || !exists {
		return err
	}
	if len(pending.Writes) == 0 {
		return newMaterializationCorruption("preserve staging directory not owned by this pending transaction")
	}
	if err := validatePendingStage(adapter, pending); err != nil {
		return err
	}
	for _, write := range pending.Writes {
		if err := adapter.removeFile(write.StagePath); err != nil {
			return err
		}
		if err := adapter.removeFile(write.StageProofPath); err != nil {
			return err
		}
	}
	same, err := adapter.sameFile(pending.StagingClaimPath, pending.StagingProofPath)
	if err != nil || !same {
		return newMaterializationCorruption("preserve staging identity because its proof changed")
	}
	if err := adapter.removeFile(pending.StagingClaimPath); err != nil {
		return err
	}
	if err := adapter.removeFile(pending.StagingProofPath); err != nil {
		return err
	}
	removed, err := adapter.removeEmptyDirectory(MaterializationStagingDirectoryName)
	if err != nil {
		return err
	}
	if !removed {
		return newMaterializationCorruption("staging directory is not empty after transaction cleanup")
	}
	return nil
}

func rejectOrphanMaterializationControls(
	adapter *RootedFilesystemAdapter,
	control materializationControlState,
) error {
	entries, err := adapter.readDirectory(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.name == MaterializationStagingDirectoryName ||
			strings.HasPrefix(entry.name, materializationTransactionPathPrefix) {
			return newMaterializationCorruption(
				"reserved transaction path %s exists without pending ownership evidence and will be preserved",
				entry.name,
			)
		}
	}
	ownershipEntries, err := adapter.readDirectory(MaterializationOwnershipDirectoryName)
	if err != nil {
		return err
	}
	expected := map[string]struct{}{materializationOwnershipRootClaimName: {}}
	if control.inventoryExists {
		for _, directory := range control.inventory.directories {
			expected[path.Base(materializationDirectoryAnchorPath(directory))] = struct{}{}
			if err := verifyOwnedMaterializationDirectory(
				adapter,
				directory,
				materializationDirectoryClaimPath(directory),
				materializationDirectoryAnchorPath(directory),
			); err != nil {
				return newMaterializationCorruption("owned directory %s lost its durable identity: %v", directory, err)
			}
		}
	}
	for _, entry := range ownershipEntries {
		if _, allowed := expected[entry.name]; !allowed {
			return newMaterializationCorruption(
				"reserved ownership path %s exists without pending ownership evidence and will be preserved",
				path.Join(MaterializationOwnershipDirectoryName, entry.name),
			)
		}
	}
	return nil
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
