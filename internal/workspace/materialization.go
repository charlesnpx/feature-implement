package workspace

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	MaterializationInventoryFileName            = "feature.materialization.v2.json"
	MaterializationStateFileName                = "feature.materialization.state.v2.json"
	MaterializationPendingFileName              = "feature.materialization.pending.v2.json"
	MaterializationCleanupFileName              = "feature.materialization.cleanup.v2.json"
	MaterializationStagingDirectoryName         = "feature.materialization.staging.v2"
	MaterializationOwnershipDirectoryName       = "feature.materialization.ownership.v2"
	MaterializationOwnershipProofFileName       = "feature.materialization.ownership.v2.proof"
	MaterializationDirectoryClaimFileName       = "feature.materialization.directory.v2.claim"
	MaterializationInventorySchemaVersion       = 2
	MaxMaterializationArtifactBytes       int64 = 16 << 20
	MaxMaterializationTotalBytes          int64 = 64 << 20
	MaxMaterializationControlBytes        int64 = 8 << 20
	maxMaterializationEntries                   = 100_000
	maxMaterializationPathBytes                 = 4096
	maxMaterializationComponentBytes            = 200
	materializationTransactionPathPrefix        = "feature.materialization.txn-"
	materializationOwnershipRootClaimName       = "root.claim"
)

var materializationControlPaths = []string{
	MaterializationInventoryFileName,
	MaterializationStateFileName,
	MaterializationPendingFileName,
	MaterializationCleanupFileName,
	MaterializationStagingDirectoryName,
	MaterializationOwnershipDirectoryName,
	MaterializationOwnershipProofFileName,
}

type MaterializationArtifact struct {
	id      string
	path    string
	content []byte
	hash    Digest
}

func NewMaterializationArtifact(id, relativePath string, content []byte) (MaterializationArtifact, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 1024 || !utf8.ValidString(id) || strings.ContainsAny(id, "\x00\r\n") {
		return MaterializationArtifact{}, fmt.Errorf("materialization artifact id is invalid")
	}
	normalized, err := normalizeMaterializationPath(relativePath)
	if err != nil {
		return MaterializationArtifact{}, err
	}
	if materializationPathUsesTransactionNamespace(normalized) {
		return MaterializationArtifact{}, fmt.Errorf("materialization path %q uses the reserved transaction namespace", normalized)
	}
	if int64(len(content)) > MaxMaterializationArtifactBytes {
		return MaterializationArtifact{}, fmt.Errorf(
			"materialization artifact %s exceeds %d bytes", id, MaxMaterializationArtifactBytes,
		)
	}
	copyContent := append([]byte(nil), content...)
	return MaterializationArtifact{
		id: id, path: normalized, content: copyContent, hash: DigestBytes(copyContent),
	}, nil
}

func (artifact MaterializationArtifact) ID() string   { return artifact.id }
func (artifact MaterializationArtifact) Path() string { return artifact.path }
func (artifact MaterializationArtifact) Bytes() []byte {
	return append([]byte(nil), artifact.content...)
}
func (artifact MaterializationArtifact) isValid() bool {
	return artifact.id != "" && artifact.path != "" && !artifact.hash.IsZero()
}

type MaterializationFaultPoint string

const (
	MaterializationFaultAfterBootstrapState      MaterializationFaultPoint = "after_bootstrap_state"
	MaterializationFaultAfterStaging             MaterializationFaultPoint = "after_staging"
	MaterializationFaultAfterPending             MaterializationFaultPoint = "after_pending"
	MaterializationFaultAfterTemporarySync       MaterializationFaultPoint = "after_temporary_sync"
	MaterializationFaultAfterQuarantine          MaterializationFaultPoint = "after_quarantine"
	MaterializationFaultAfterDirectoryCreate     MaterializationFaultPoint = "after_directory_create"
	MaterializationFaultAfterArtifactWrite       MaterializationFaultPoint = "after_artifact_write"
	MaterializationFaultAfterStaleDelete         MaterializationFaultPoint = "after_stale_delete"
	MaterializationFaultAfterDirectoryCleanup    MaterializationFaultPoint = "after_directory_cleanup"
	MaterializationFaultAfterInventoryActivation MaterializationFaultPoint = "after_inventory_activation"
	MaterializationFaultAfterStateActivation     MaterializationFaultPoint = "after_state_activation"
	MaterializationFaultBeforeCleanupUnlink      MaterializationFaultPoint = "before_cleanup_unlink"
	MaterializationFaultAfterCleanupStep         MaterializationFaultPoint = "after_cleanup_step"
)

type MaterializationFaultInjector func(MaterializationFaultPoint) error

type MaterializationOptions struct {
	FaultInjector MaterializationFaultInjector
}

type OwnedMaterializationArtifact struct {
	id                string
	path              string
	lastGeneratedHash Digest
}

func (artifact OwnedMaterializationArtifact) ID() string   { return artifact.id }
func (artifact OwnedMaterializationArtifact) Path() string { return artifact.path }

type MaterializationInventory struct {
	schemaVersion    int
	generatorVersion string
	artifacts        []OwnedMaterializationArtifact
	directories      []string
}

func (inventory MaterializationInventory) SchemaVersion() int { return inventory.schemaVersion }
func (inventory MaterializationInventory) GeneratorVersion() string {
	return inventory.generatorVersion
}
func (inventory MaterializationInventory) Artifacts() []OwnedMaterializationArtifact {
	return append([]OwnedMaterializationArtifact(nil), inventory.artifacts...)
}

type MaterializationResult struct {
	inventory MaterializationInventory
	created   []string
	updated   []string
	deleted   []string
	recovered bool
}

func (result MaterializationResult) Inventory() MaterializationInventory {
	return cloneMaterializationInventory(result.inventory)
}
func (result MaterializationResult) Created() []string {
	return append([]string(nil), result.created...)
}
func (result MaterializationResult) Updated() []string {
	return append([]string(nil), result.updated...)
}
func (result MaterializationResult) Deleted() []string {
	return append([]string(nil), result.deleted...)
}
func (result MaterializationResult) Recovered() bool { return result.recovered }

type MaterializationConflictKind string

const (
	MaterializationConflictUnownedDestination MaterializationConflictKind = "unowned_destination"
	MaterializationConflictUnownedPath        MaterializationConflictKind = "unowned_path"
	MaterializationConflictModifiedOwnedPath  MaterializationConflictKind = "modified_owned_path"
	MaterializationConflictMissingOwnedPath   MaterializationConflictKind = "missing_owned_path"
	MaterializationConflictUnsafePath         MaterializationConflictKind = "unsafe_path"
)

type MaterializationConflict struct {
	kind       MaterializationConflictKind
	artifactID string
	path       string
	detail     string
}

func (conflict MaterializationConflict) Kind() MaterializationConflictKind { return conflict.kind }
func (conflict MaterializationConflict) Path() string                      { return conflict.path }

type MaterializationConflictError struct {
	conflicts []MaterializationConflict
}

func (err MaterializationConflictError) Error() string {
	if len(err.conflicts) == 0 {
		return "materialization has conflicts"
	}
	first := err.conflicts[0]
	return fmt.Sprintf("materialization conflict %s at %s: %s", first.kind, first.path, first.detail)
}

func (err MaterializationConflictError) Conflicts() []MaterializationConflict {
	return append([]MaterializationConflict(nil), err.conflicts...)
}

type MaterializationCorruptionError struct {
	detail string
}

func (err MaterializationCorruptionError) Error() string {
	return "materialization state is corrupt: " + err.detail
}

func newMaterializationCorruption(format string, arguments ...any) error {
	return MaterializationCorruptionError{detail: fmt.Sprintf(format, arguments...)}
}

type materializationArtifactWire struct {
	ArtifactID        string `json:"artifact_id"`
	Path              string `json:"path"`
	LastGeneratedHash string `json:"last_generated_hash"`
}

type materializationInventoryWire struct {
	SchemaVersion    int                           `json:"schema_version"`
	GeneratorVersion string                        `json:"generator_version"`
	Artifacts        []materializationArtifactWire `json:"artifacts"`
	Directories      []string                      `json:"directories"`
}

func (inventory MaterializationInventory) wire() materializationInventoryWire {
	wire := materializationInventoryWire{
		SchemaVersion: inventory.schemaVersion, GeneratorVersion: inventory.generatorVersion,
		Artifacts:   make([]materializationArtifactWire, 0, len(inventory.artifacts)),
		Directories: append([]string{}, inventory.directories...),
	}
	for _, artifact := range inventory.artifacts {
		wire.Artifacts = append(wire.Artifacts, materializationArtifactWire{
			ArtifactID: artifact.id, Path: artifact.path,
			LastGeneratedHash: artifact.lastGeneratedHash.String(),
		})
	}
	return wire
}

func marshalMaterializationInventory(inventory MaterializationInventory) ([]byte, Digest, error) {
	content, err := json.MarshalIndent(inventory.wire(), "", "  ")
	if err != nil {
		return nil, Digest{}, err
	}
	content = append(content, '\n')
	return content, DigestBytes(content), nil
}

func parseMaterializationInventory(content []byte) (MaterializationInventory, error) {
	if len(content) == 0 || int64(len(content)) > MaxMaterializationControlBytes {
		return MaterializationInventory{}, newMaterializationCorruption("inventory has an invalid size")
	}
	if err := rejectDuplicateJSONObjectKeys(content); err != nil {
		return MaterializationInventory{}, newMaterializationCorruption("inventory JSON: %v", err)
	}
	var wire materializationInventoryWire
	if err := decodeStrictJSON(content, &wire); err != nil {
		return MaterializationInventory{}, newMaterializationCorruption("decode inventory: %v", err)
	}
	return normalizeMaterializationInventoryWire(wire)
}

func normalizeMaterializationInventoryWire(wire materializationInventoryWire) (MaterializationInventory, error) {
	if wire.SchemaVersion != MaterializationInventorySchemaVersion {
		return MaterializationInventory{}, newMaterializationCorruption(
			"inventory schema_version %d is unsupported", wire.SchemaVersion,
		)
	}
	if err := validateGeneratorVersion(wire.GeneratorVersion); err != nil {
		return MaterializationInventory{}, newMaterializationCorruption("%v", err)
	}
	if wire.Artifacts == nil || wire.Directories == nil {
		return MaterializationInventory{}, newMaterializationCorruption("inventory artifacts and directories are required")
	}
	if len(wire.Artifacts) == 0 || len(wire.Artifacts) > maxMaterializationEntries || len(wire.Directories) > maxMaterializationEntries {
		return MaterializationInventory{}, newMaterializationCorruption("inventory entry count is invalid")
	}
	artifacts := make([]OwnedMaterializationArtifact, 0, len(wire.Artifacts))
	seenIDs := make(map[string]string, len(wire.Artifacts))
	seenPaths := make(map[string]string, len(wire.Artifacts))
	lastID := ""
	for index, item := range wire.Artifacts {
		id := strings.TrimSpace(item.ArtifactID)
		if id == "" || id != item.ArtifactID || len(id) > 1024 || !utf8.ValidString(id) || strings.ContainsAny(id, "\x00\r\n") {
			return MaterializationInventory{}, newMaterializationCorruption("artifact %d has an invalid id", index)
		}
		if lastID != "" && id <= lastID {
			return MaterializationInventory{}, newMaterializationCorruption("inventory artifacts are not strictly sorted by id")
		}
		lastID = id
		relative, err := normalizeMaterializationPath(item.Path)
		if err != nil || relative != item.Path {
			return MaterializationInventory{}, newMaterializationCorruption("artifact %s has invalid path %q", id, item.Path)
		}
		if materializationPathUsesTransactionNamespace(relative) {
			return MaterializationInventory{}, newMaterializationCorruption("artifact %s uses the reserved transaction namespace", id)
		}
		hash, err := ParseDigest(item.LastGeneratedHash)
		if err != nil || hash.IsZero() {
			return MaterializationInventory{}, newMaterializationCorruption("artifact %s has invalid last-generated hash", id)
		}
		idKey := materializationCollisionKey(id)
		if prior := seenIDs[idKey]; prior != "" {
			return MaterializationInventory{}, newMaterializationCorruption("artifact ids %q and %q collide", prior, id)
		}
		pathKey := materializationCollisionKey(relative)
		if prior := seenPaths[pathKey]; prior != "" {
			return MaterializationInventory{}, newMaterializationCorruption("artifact paths %q and %q collide", prior, relative)
		}
		seenIDs[idKey] = id
		seenPaths[pathKey] = relative
		artifacts = append(artifacts, OwnedMaterializationArtifact{id: id, path: relative, lastGeneratedHash: hash})
	}
	if err := validateMaterializationPathPrefixes(ownedArtifactPaths(artifacts)); err != nil {
		return MaterializationInventory{}, newMaterializationCorruption("%v", err)
	}
	directories := append([]string(nil), wire.Directories...)
	lastDirectory := ""
	seenDirectories := make(map[string]string, len(directories))
	expectedDirectories := make(map[string]string)
	for _, artifact := range artifacts {
		for directory := path.Dir(artifact.path); directory != "."; directory = path.Dir(directory) {
			key := materializationCollisionKey(directory)
			if prior := expectedDirectories[key]; prior != "" && prior != directory {
				return MaterializationInventory{}, newMaterializationCorruption(
					"artifact directory spellings %q and %q collide", prior, directory,
				)
			}
			expectedDirectories[key] = directory
		}
	}
	for index, directory := range directories {
		normalized, err := normalizeMaterializationPath(directory)
		if err != nil || normalized != directory {
			return MaterializationInventory{}, newMaterializationCorruption("directory %d has invalid path %q", index, directory)
		}
		if materializationPathUsesTransactionNamespace(normalized) {
			return MaterializationInventory{}, newMaterializationCorruption("directory %q uses the reserved transaction namespace", directory)
		}
		if lastDirectory != "" && directory <= lastDirectory {
			return MaterializationInventory{}, newMaterializationCorruption("inventory directories are not strictly sorted")
		}
		lastDirectory = directory
		key := materializationCollisionKey(directory)
		if prior := seenDirectories[key]; prior != "" {
			return MaterializationInventory{}, newMaterializationCorruption("directories %q and %q collide", prior, directory)
		}
		if artifact := seenPaths[key]; artifact != "" {
			return MaterializationInventory{}, newMaterializationCorruption("directory %q conflicts with artifact path %q", directory, artifact)
		}
		if expected := expectedDirectories[key]; expected == "" || expected != directory {
			return MaterializationInventory{}, newMaterializationCorruption("directory %q is not an exact artifact parent", directory)
		}
		seenDirectories[key] = directory
	}
	return MaterializationInventory{
		schemaVersion:    MaterializationInventorySchemaVersion,
		generatorVersion: wire.GeneratorVersion,
		artifacts:        artifacts,
		directories:      directories,
	}, nil
}

type desiredMaterialization struct {
	generatorVersion string
	artifacts        []MaterializationArtifact
	directories      []string
}

func prepareDesiredMaterialization(
	generatorVersion string,
	artifacts []MaterializationArtifact,
) (desiredMaterialization, error) {
	if err := validateGeneratorVersion(generatorVersion); err != nil {
		return desiredMaterialization{}, err
	}
	if len(artifacts) == 0 {
		return desiredMaterialization{}, fmt.Errorf("materialization requires at least one artifact")
	}
	if len(artifacts) > maxMaterializationEntries {
		return desiredMaterialization{}, fmt.Errorf("materialization exceeds %d artifacts", maxMaterializationEntries)
	}
	copyArtifacts := append([]MaterializationArtifact(nil), artifacts...)
	var total int64
	for index := range copyArtifacts {
		artifact := copyArtifacts[index]
		if !artifact.isValid() || DigestBytes(artifact.content) != artifact.hash {
			return desiredMaterialization{}, fmt.Errorf("materialization artifact %d is invalid", index)
		}
		artifact.content = append([]byte(nil), artifact.content...)
		copyArtifacts[index] = artifact
		total += int64(len(artifact.content))
		if total > MaxMaterializationTotalBytes {
			return desiredMaterialization{}, fmt.Errorf("materialization exceeds %d total bytes", MaxMaterializationTotalBytes)
		}
	}
	sort.Slice(copyArtifacts, func(i, j int) bool { return copyArtifacts[i].id < copyArtifacts[j].id })
	seenIDs := make(map[string]string, len(copyArtifacts))
	seenPaths := make(map[string]string, len(copyArtifacts))
	paths := make([]string, 0, len(copyArtifacts))
	for _, artifact := range copyArtifacts {
		idKey := materializationCollisionKey(artifact.id)
		if prior := seenIDs[idKey]; prior != "" {
			return desiredMaterialization{}, fmt.Errorf("materialization artifact ids %q and %q collide", prior, artifact.id)
		}
		pathKey := materializationCollisionKey(artifact.path)
		if prior := seenPaths[pathKey]; prior != "" {
			return desiredMaterialization{}, fmt.Errorf("materialization paths %q and %q collide", prior, artifact.path)
		}
		for _, control := range materializationControlPaths {
			controlKey := materializationCollisionKey(control)
			if pathKey == controlKey || strings.HasPrefix(pathKey, controlKey+"/") || strings.HasPrefix(controlKey, pathKey+"/") {
				return desiredMaterialization{}, fmt.Errorf("materialization path %q conflicts with reserved control path %q", artifact.path, control)
			}
		}
		seenIDs[idKey] = artifact.id
		seenPaths[pathKey] = artifact.path
		paths = append(paths, artifact.path)
	}
	if err := validateMaterializationPathPrefixes(paths); err != nil {
		return desiredMaterialization{}, err
	}
	directorySet := make(map[string]struct{})
	directorySpellings := make(map[string]string)
	for _, artifact := range copyArtifacts {
		for directory := path.Dir(artifact.path); directory != "."; directory = path.Dir(directory) {
			key := materializationCollisionKey(directory)
			if prior := directorySpellings[key]; prior != "" && prior != directory {
				return desiredMaterialization{}, fmt.Errorf(
					"materialization directory spellings %q and %q collide", prior, directory,
				)
			}
			directorySpellings[key] = directory
			directorySet[directory] = struct{}{}
		}
	}
	directories := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		directories = append(directories, directory)
	}
	sortMaterializationDirectories(directories, false)
	return desiredMaterialization{
		generatorVersion: generatorVersion,
		artifacts:        copyArtifacts,
		directories:      directories,
	}, nil
}

func normalizeMaterializationPath(value string) (string, error) {
	if value == "" || len(value) > maxMaterializationPathBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		strings.Contains(value, "\\") || strings.IndexByte(value, 0) >= 0 || !norm.NFC.IsNormalString(value) {
		return "", fmt.Errorf("materialization path %q must be non-empty normalized UTF-8 with forward slashes", value)
	}
	clean := path.Clean(value)
	if clean != value || !fs.ValidPath(clean) || !isPortableRelativePath(clean) {
		return "", fmt.Errorf("materialization path %q must be a normalized portable relative path", value)
	}
	for _, component := range strings.Split(clean, "/") {
		if strings.HasPrefix(component, ".") || len(component) > maxMaterializationComponentBytes {
			return "", fmt.Errorf("materialization path %q contains an unsafe component %q", value, component)
		}
	}
	return clean, nil
}

func materializationPathUsesTransactionNamespace(relative string) bool {
	prefixKey := materializationCollisionKey(materializationTransactionPathPrefix)
	directoryClaimKey := materializationCollisionKey(MaterializationDirectoryClaimFileName)
	for _, component := range strings.Split(relative, "/") {
		componentKey := materializationCollisionKey(component)
		if strings.HasPrefix(componentKey, prefixKey) || componentKey == directoryClaimKey {
			return true
		}
	}
	return false
}

func validateMaterializationPathPrefixes(paths []string) error {
	byKey := make(map[string]string, len(paths))
	for _, value := range paths {
		key := materializationCollisionKey(value)
		if prior := byKey[key]; prior != "" {
			return fmt.Errorf("materialization paths %q and %q collide", prior, value)
		}
		byKey[key] = value
	}
	for key, value := range byKey {
		for ancestor := path.Dir(key); ancestor != "."; ancestor = path.Dir(ancestor) {
			if prefix, exists := byKey[ancestor]; exists {
				return fmt.Errorf(
					"materialization path-prefix conflict between %q and %q",
					prefix, value,
				)
			}
		}
	}
	return nil
}

func validateGeneratorVersion(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 256 ||
		!utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("materialization generator version is invalid")
	}
	return nil
}

func materializationCollisionKey(value string) string {
	return cases.Fold().String(norm.NFC.String(value))
}

func ownedArtifactPaths(artifacts []OwnedMaterializationArtifact) []string {
	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, artifact.path)
	}
	return paths
}

func cloneMaterializationInventory(inventory MaterializationInventory) MaterializationInventory {
	return MaterializationInventory{
		schemaVersion:    inventory.schemaVersion,
		generatorVersion: inventory.generatorVersion,
		artifacts:        append([]OwnedMaterializationArtifact(nil), inventory.artifacts...),
		directories:      append([]string(nil), inventory.directories...),
	}
}

func sortMaterializationDirectories(directories []string, deepestFirst bool) {
	sort.Slice(directories, func(i, j int) bool {
		leftDepth := strings.Count(directories[i], "/")
		rightDepth := strings.Count(directories[j], "/")
		if leftDepth != rightDepth {
			if deepestFirst {
				return leftDepth > rightDepth
			}
			return leftDepth < rightDepth
		}
		return directories[i] < directories[j]
	})
}
