package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	PlanRepositoryInventoryFileName    = "feature.plan.inventory.v1.json"
	PlanRepositoryInventorySchema      = 1
	PlanCheckpointRequestSchemaVersion = 2
	PlanCheckpointGeneratorVersion     = "feature-plan-checkpoint/v1"
	maxPlanRepositoryInventoryBytes    = 4 << 20
	maxPlanRepositoryFiles             = 100_000
)

type PlanCheckpointKind string

const (
	PlanCheckpointInitial  PlanCheckpointKind = "initial"
	PlanCheckpointRevision PlanCheckpointKind = "revision"
	PlanCheckpointLock     PlanCheckpointKind = "lock"
)

func (kind PlanCheckpointKind) valid() bool {
	switch kind {
	case PlanCheckpointInitial, PlanCheckpointRevision, PlanCheckpointLock:
		return true
	default:
		return false
	}
}

type PlanCheckpointFaultPoint string

const (
	PlanCheckpointFaultAfterRepositoryInitialization PlanCheckpointFaultPoint = "after_repository_initialization"
	PlanCheckpointFaultAfterLockGeneration           PlanCheckpointFaultPoint = "after_lock_generation"
	PlanCheckpointFaultAfterTreeCreation             PlanCheckpointFaultPoint = "after_tree_creation"
	PlanCheckpointFaultAfterCommitCreation           PlanCheckpointFaultPoint = "after_commit_creation"
	PlanCheckpointFaultBeforeRefCAS                  PlanCheckpointFaultPoint = "before_ref_cas"
	PlanCheckpointFaultAfterRefCAS                   PlanCheckpointFaultPoint = "after_ref_cas"
	PlanCheckpointFaultAfterIndexSynchronization     PlanCheckpointFaultPoint = "after_index_synchronization"
)

type PlanCheckpointFaultInjector func(PlanCheckpointFaultPoint) error

type PlanCheckpointOptions struct {
	Root          string
	Kind          PlanCheckpointKind
	Input         []byte
	GitExecutable string
	FaultInjector PlanCheckpointFaultInjector
}

type PlanCheckpointResult struct {
	SchemaVersion  int                `json:"schema_version"`
	Status         string             `json:"status"`
	Kind           PlanCheckpointKind `json:"kind"`
	Root           string             `json:"root"`
	Commit         string             `json:"commit"`
	Tree           string             `json:"tree"`
	SourceDigest   string             `json:"source_digest"`
	SemanticDigest string             `json:"semantic_digest"`
	Generation     string             `json:"generation"`
	LockDigest     string             `json:"lock_digest,omitempty"`
	RevisionID     string             `json:"revision_id,omitempty"`
	ReviewDigest   string             `json:"review_digest,omitempty"`
	Recovered      bool               `json:"recovered"`
}

type VerifiedPlanLockCheckpoint struct {
	root           string
	commit         GitObjectID
	tree           GitObjectID
	sourceDigest   Digest
	semanticDigest Digest
	generation     Digest
	lockDigest     Digest
}

func (checkpoint VerifiedPlanLockCheckpoint) Root() string         { return checkpoint.root }
func (checkpoint VerifiedPlanLockCheckpoint) Commit() GitObjectID  { return checkpoint.commit }
func (checkpoint VerifiedPlanLockCheckpoint) Tree() GitObjectID    { return checkpoint.tree }
func (checkpoint VerifiedPlanLockCheckpoint) SourceDigest() Digest { return checkpoint.sourceDigest }
func (checkpoint VerifiedPlanLockCheckpoint) SemanticDigest() Digest {
	return checkpoint.semanticDigest
}
func (checkpoint VerifiedPlanLockCheckpoint) Generation() Digest { return checkpoint.generation }
func (checkpoint VerifiedPlanLockCheckpoint) LockDigest() Digest { return checkpoint.lockDigest }

type planCheckpointRequest struct {
	occurredAt   time.Time
	revisionID   ID
	reviewDigest Digest
}

type planCheckpointSimpleRequestWire struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
}

type planCheckpointRevisionRequestWire struct {
	SchemaVersion int    `json:"schema_version"`
	OccurredAt    string `json:"occurred_at"`
	RevisionID    string `json:"revision_id"`
	ReviewDigest  string `json:"review_digest"`
}

type planRepositoryPathRole string

const (
	planRepositoryPathInventory      planRepositoryPathRole = "inventory"
	planRepositoryPathSource         planRepositoryPathRole = "source"
	planRepositoryPathLock           planRepositoryPathRole = "lock"
	planRepositoryPathAdministrative planRepositoryPathRole = "administrative"
)

type planRepositoryPathWire struct {
	Role    planRepositoryPathRole `json:"role"`
	Path    string                 `json:"path"`
	Tracked bool                   `json:"tracked"`
}

type planRepositoryInventoryWire struct {
	SchemaVersion int                      `json:"schema_version"`
	Paths         []planRepositoryPathWire `json:"paths"`
}

type planRepositoryInventory struct {
	paths []planRepositoryPathWire
}

func (inventory planRepositoryInventory) trackedPaths() []string {
	result := make([]string, 0, len(inventory.paths))
	for _, item := range inventory.paths {
		if item.Tracked {
			result = append(result, item.Path)
		}
	}
	return result
}

func (inventory planRepositoryInventory) administrativePaths() []string {
	result := make([]string, 0, len(inventory.paths))
	for _, item := range inventory.paths {
		if !item.Tracked {
			result = append(result, item.Path)
		}
	}
	return result
}

type planBundleIdentity struct {
	source     Digest
	semantic   Digest
	generation Digest
}

type planLockFiles struct {
	tracked map[string][]byte
	digest  Digest
}

var planCheckpointProcessMutex sync.Mutex

func CheckpointPlanRepository(
	ctx context.Context,
	options PlanCheckpointOptions,
) (PlanCheckpointResult, error) {
	if ctx == nil {
		return PlanCheckpointResult{}, fmt.Errorf("plan checkpoint requires context")
	}
	if !options.Kind.valid() {
		return PlanCheckpointResult{}, fmt.Errorf("plan checkpoint kind must be initial, revision, or lock")
	}
	request, err := decodePlanCheckpointRequest(options.Kind, options.Input)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	root, err := canonicalPlanCheckpointRoot(options.Root)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	bundle, err := LoadWorkspaceBundle(root)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if options.GitExecutable == "" {
		options.GitExecutable = "git"
	}
	adapter, err := newPlanCheckpointGitAdapter(options.GitExecutable, root)
	if err != nil {
		return PlanCheckpointResult{}, err
	}

	planCheckpointProcessMutex.Lock()
	defer planCheckpointProcessMutex.Unlock()

	return adapter.checkpoint(ctx, bundle, options.Kind, request, options.FaultInjector)
}

func VerifyPlanLockCheckpoint(
	ctx context.Context,
	bundle WorkspaceBundle,
) (VerifiedPlanLockCheckpoint, error) {
	if ctx == nil {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("plan lock verification requires context")
	}
	if bundle.root == "" || bundle.definition.generation.IsZero() {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("validated workspace bundle is required")
	}
	adapter, err := newPlanCheckpointGitAdapter("git", bundle.root)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	return adapter.verifyLockCheckpoint(ctx, bundle)
}

func decodePlanCheckpointRequest(kind PlanCheckpointKind, input []byte) (planCheckpointRequest, error) {
	if len(input) == 0 {
		return planCheckpointRequest{}, fmt.Errorf("plan checkpoint requires --input <strict-json>")
	}
	if len(input) > MaxArtifactBytes {
		return planCheckpointRequest{}, fmt.Errorf("plan checkpoint input exceeds %d bytes", MaxArtifactBytes)
	}
	var schema int
	var occurred string
	request := planCheckpointRequest{}
	switch kind {
	case PlanCheckpointInitial, PlanCheckpointLock:
		var wire planCheckpointSimpleRequestWire
		if err := decodeStrictJSONRequired(input, &wire); err != nil {
			return planCheckpointRequest{}, fmt.Errorf("decode %s checkpoint request: %w", kind, err)
		}
		schema, occurred = wire.SchemaVersion, wire.OccurredAt
	case PlanCheckpointRevision:
		var wire planCheckpointRevisionRequestWire
		if err := decodeStrictJSONRequired(input, &wire); err != nil {
			return planCheckpointRequest{}, fmt.Errorf("decode revision checkpoint request: %w", err)
		}
		schema, occurred = wire.SchemaVersion, wire.OccurredAt
		revisionID, err := NewID(wire.RevisionID)
		if err != nil {
			return planCheckpointRequest{}, fmt.Errorf("revision_id: %w", err)
		}
		reviewDigest, err := ParseDigest(wire.ReviewDigest)
		if err != nil {
			return planCheckpointRequest{}, fmt.Errorf("review_digest: %w", err)
		}
		request.revisionID = revisionID
		request.reviewDigest = reviewDigest
	default:
		return planCheckpointRequest{}, fmt.Errorf("unsupported plan checkpoint kind %q", kind)
	}
	if schema != PlanCheckpointRequestSchemaVersion {
		return planCheckpointRequest{}, fmt.Errorf(
			"plan checkpoint schema_version must be %d",
			PlanCheckpointRequestSchemaVersion,
		)
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(occurred))
	if err != nil || parsed.IsZero() {
		return planCheckpointRequest{}, fmt.Errorf("plan checkpoint occurred_at must be RFC3339Nano")
	}
	request.occurredAt = parsed.UTC()
	return request, nil
}

func canonicalPlanCheckpointRoot(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "" {
		return "", fmt.Errorf("plan checkpoint requires --root <bundle-root>")
	}
	if !filepath.IsAbs(value) {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		value = absolute
	}
	return canonicalizeTrustedRootPath(value)
}

func identityForPlanBundle(bundle WorkspaceBundle) (planBundleIdentity, error) {
	type item struct {
		Kind     string `json:"kind"`
		ID       string `json:"id"`
		Path     string `json:"path,omitempty"`
		Source   string `json:"source"`
		Semantic string `json:"semantic"`
	}
	sourceItems := make([]item, 0, len(bundle.definition.artifacts)+len(bundle.definition.authorities))
	semanticItems := make([]item, 0, cap(sourceItems))
	for _, artifact := range bundle.definition.artifacts {
		base := item{
			Kind: string(artifact.kind), ID: artifact.id.String(), Path: artifact.path,
			Source: artifact.sourceHash.String(), Semantic: artifact.semanticHash.String(),
		}
		sourceItems = append(sourceItems, item{
			Kind: base.Kind, ID: base.ID, Path: base.Path, Source: base.Source,
		})
		semanticItems = append(semanticItems, item{
			Kind: base.Kind, ID: base.ID, Path: base.Path, Semantic: base.Semantic,
		})
	}
	for _, authority := range bundle.definition.authorities {
		kind := "authority/" + string(authority.kind)
		sourceItems = append(sourceItems, item{
			Kind: kind, ID: authority.id.String(), Source: authority.sourceHash.String(),
		})
		semanticItems = append(semanticItems, item{
			Kind: kind, ID: authority.id.String(), Semantic: authority.semanticHash.String(),
		})
	}
	sourceBytes, err := json.Marshal(sourceItems)
	if err != nil {
		return planBundleIdentity{}, err
	}
	semanticBytes, err := json.Marshal(semanticItems)
	if err != nil {
		return planBundleIdentity{}, err
	}
	return planBundleIdentity{
		source: DigestBytes(sourceBytes), semantic: DigestBytes(semanticBytes),
		generation: bundle.definition.generation,
	}, nil
}

func expectedPlanLockFiles(bundle WorkspaceBundle) (planLockFiles, error) {
	artifacts, err := WorkspaceBundleLockArtifacts(bundle)
	if err != nil {
		return planLockFiles{}, err
	}
	type lockIdentity struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
	}
	identities := make([]lockIdentity, 0, len(artifacts))
	tracked := make(map[string][]byte, len(artifacts))
	for _, artifact := range artifacts {
		relative := path.Join(WorkspaceGeneratedDirectory, artifact.path)
		if _, exists := tracked[relative]; exists {
			return planLockFiles{}, fmt.Errorf("duplicate generated lock path %s", relative)
		}
		tracked[relative] = artifact.Bytes()
		identities = append(identities, lockIdentity{Path: relative, Digest: artifact.hash.String()})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].Path < identities[j].Path })
	canonical, err := json.Marshal(identities)
	if err != nil {
		return planLockFiles{}, err
	}
	return planLockFiles{tracked: tracked, digest: DigestBytes(canonical)}, nil
}

func buildPlanRepositoryInventory(
	bundle WorkspaceBundle,
	locks planLockFiles,
	administrative []string,
) (planRepositoryInventory, []byte, error) {
	items := []planRepositoryPathWire{{
		Role: planRepositoryPathInventory, Path: PlanRepositoryInventoryFileName, Tracked: true,
	}}
	for _, source := range bundle.sourcePaths {
		items = append(items, planRepositoryPathWire{
			Role: planRepositoryPathSource, Path: source, Tracked: true,
		})
	}
	lockPaths := make([]string, 0, len(locks.tracked))
	for lockPath := range locks.tracked {
		lockPaths = append(lockPaths, lockPath)
	}
	sort.Strings(lockPaths)
	for _, lockPath := range lockPaths {
		items = append(items, planRepositoryPathWire{
			Role: planRepositoryPathLock, Path: lockPath, Tracked: true,
		})
	}
	administrative = append([]string(nil), administrative...)
	sort.Strings(administrative)
	for _, administrativePath := range administrative {
		items = append(items, planRepositoryPathWire{
			Role: planRepositoryPathAdministrative, Path: administrativePath, Tracked: false,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Path != items[j].Path {
			return items[i].Path < items[j].Path
		}
		return items[i].Role < items[j].Role
	})
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		normalized, err := normalizePlanRepositoryPath(item.Path)
		if err != nil || normalized != item.Path {
			if err == nil {
				err = fmt.Errorf("path is not normalized")
			}
			return planRepositoryInventory{}, nil, fmt.Errorf("plan inventory path %q: %w", item.Path, err)
		}
		if _, exists := seen[item.Path]; exists {
			return planRepositoryInventory{}, nil, fmt.Errorf("duplicate plan inventory path %s", item.Path)
		}
		seen[item.Path] = struct{}{}
		if item.Role == planRepositoryPathAdministrative && item.Tracked {
			return planRepositoryInventory{}, nil, fmt.Errorf("administrative path %s cannot be tracked", item.Path)
		}
		if item.Role != planRepositoryPathAdministrative && !item.Tracked {
			return planRepositoryInventory{}, nil, fmt.Errorf("tracked plan path %s has an invalid role", item.Path)
		}
	}
	wire := planRepositoryInventoryWire{
		SchemaVersion: PlanRepositoryInventorySchema,
		Paths:         append([]planRepositoryPathWire(nil), items...),
	}
	content, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return planRepositoryInventory{}, nil, err
	}
	content = append(content, '\n')
	if len(content) > maxPlanRepositoryInventoryBytes {
		return planRepositoryInventory{}, nil, fmt.Errorf("plan repository inventory exceeds %d bytes", maxPlanRepositoryInventoryBytes)
	}
	return planRepositoryInventory{paths: items}, content, nil
}

func parsePlanRepositoryInventory(content []byte) (planRepositoryInventory, error) {
	if len(content) == 0 || len(content) > maxPlanRepositoryInventoryBytes {
		return planRepositoryInventory{}, fmt.Errorf("plan repository inventory has an invalid size")
	}
	if err := rejectDuplicateJSONObjectKeys(content); err != nil {
		return planRepositoryInventory{}, fmt.Errorf("plan repository inventory: %w", err)
	}
	var wire planRepositoryInventoryWire
	if err := decodeStrictJSONRequired(content, &wire); err != nil {
		return planRepositoryInventory{}, fmt.Errorf("decode plan repository inventory: %w", err)
	}
	if wire.SchemaVersion != PlanRepositoryInventorySchema {
		return planRepositoryInventory{}, fmt.Errorf(
			"plan repository inventory schema_version must be %d",
			PlanRepositoryInventorySchema,
		)
	}
	if len(wire.Paths) == 0 || len(wire.Paths) > maxPlanRepositoryFiles {
		return planRepositoryInventory{}, fmt.Errorf("plan repository inventory path count is invalid")
	}
	previous := ""
	for _, item := range wire.Paths {
		if item.Path <= previous {
			return planRepositoryInventory{}, fmt.Errorf("plan repository inventory paths are not strictly sorted")
		}
		previous = item.Path
		if _, err := normalizePlanRepositoryPath(item.Path); err != nil {
			return planRepositoryInventory{}, err
		}
		switch item.Role {
		case planRepositoryPathInventory, planRepositoryPathSource, planRepositoryPathLock:
			if !item.Tracked {
				return planRepositoryInventory{}, fmt.Errorf("plan repository path %s must be tracked", item.Path)
			}
		case planRepositoryPathAdministrative:
			if item.Tracked {
				return planRepositoryInventory{}, fmt.Errorf("administrative path %s cannot be tracked", item.Path)
			}
		default:
			return planRepositoryInventory{}, fmt.Errorf("plan repository path %s has invalid role %q", item.Path, item.Role)
		}
	}
	inventory := planRepositoryInventory{paths: append([]planRepositoryPathWire(nil), wire.Paths...)}
	canonical, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return planRepositoryInventory{}, err
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(content, canonical) {
		return planRepositoryInventory{}, fmt.Errorf("plan repository inventory is not canonical")
	}
	return inventory, nil
}

func normalizePlanRepositoryPath(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n\t") {
		return "", fmt.Errorf("plan repository path %q contains a control character", value)
	}
	normalized, err := normalizeSourcePath(value)
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(filepath.FromSlash(normalized)) || !isPortableRelativePath(normalized) {
		return "", fmt.Errorf("plan repository path %q is not portable", value)
	}
	for _, component := range strings.Split(normalized, "/") {
		if strings.HasPrefix(component, ".") {
			return "", fmt.Errorf("plan repository path %q contains a hidden component", value)
		}
	}
	return normalized, nil
}

func synchronizePlanLockMaterialization(
	bundle WorkspaceBundle,
	fault PlanCheckpointFaultInjector,
) (planLockFiles, []string, error) {
	locks, err := expectedPlanLockFiles(bundle)
	if err != nil {
		return planLockFiles{}, nil, err
	}
	artifacts, err := WorkspaceBundleLockArtifacts(bundle)
	if err != nil {
		return planLockFiles{}, nil, err
	}
	result, err := SynchronizeMaterialization(
		filepath.Join(bundle.root, WorkspaceGeneratedDirectory),
		PlanCheckpointGeneratorVersion,
		artifacts,
		MaterializationOptions{},
	)
	if err != nil {
		return planLockFiles{}, nil, err
	}
	if err := injectPlanCheckpointFault(fault, PlanCheckpointFaultAfterLockGeneration); err != nil {
		return planLockFiles{}, nil, err
	}
	administrative := planLockAdministrativePaths(result.Inventory())
	if err := verifyPlanLockMaterialization(bundle, locks, administrative); err != nil {
		return planLockFiles{}, nil, err
	}
	return locks, administrative, nil
}

func inspectPlanLockMaterialization(bundle WorkspaceBundle) (planLockFiles, []string, error) {
	locks, err := expectedPlanLockFiles(bundle)
	if err != nil {
		return planLockFiles{}, nil, err
	}
	root, err := OpenVerifiedRoot(RootRolePlan, bundle.root, false)
	if err != nil {
		return planLockFiles{}, nil, err
	}
	defer root.Close()
	inventoryBytes, err := root.adapter.readBounded(
		path.Join(WorkspaceGeneratedDirectory, MaterializationInventoryFileName),
		MaxMaterializationControlBytes,
	)
	if err != nil {
		return planLockFiles{}, nil, fmt.Errorf("read generated lock inventory: %w", err)
	}
	inventory, err := parseMaterializationInventory(inventoryBytes)
	if err != nil {
		return planLockFiles{}, nil, err
	}
	if inventory.generatorVersion != PlanCheckpointGeneratorVersion {
		return planLockFiles{}, nil, fmt.Errorf(
			"generated locks were not produced by %s",
			PlanCheckpointGeneratorVersion,
		)
	}
	administrative := planLockAdministrativePaths(inventory)
	if err := verifyPlanLockMaterialization(bundle, locks, administrative); err != nil {
		return planLockFiles{}, nil, err
	}
	return locks, administrative, nil
}

func planLockAdministrativePaths(inventory MaterializationInventory) []string {
	prefix := WorkspaceGeneratedDirectory
	result := []string{
		path.Join(prefix, MaterializationInventoryFileName),
		path.Join(prefix, MaterializationStateFileName),
		path.Join(prefix, MaterializationOwnershipProofFileName),
		path.Join(prefix, MaterializationOwnershipDirectoryName, materializationOwnershipRootClaimName),
	}
	for _, directory := range inventory.directories {
		result = append(result,
			path.Join(prefix, materializationDirectoryClaimPath(directory)),
			path.Join(prefix, materializationDirectoryAnchorPath(directory)),
		)
	}
	sort.Strings(result)
	return result
}

func verifyPlanLockMaterialization(
	bundle WorkspaceBundle,
	locks planLockFiles,
	administrative []string,
) error {
	root, err := OpenVerifiedRoot(RootRolePlan, bundle.root, false)
	if err != nil {
		return err
	}
	defer root.Close()
	for relative, expected := range locks.tracked {
		content, err := root.ReadBounded(relative, MaxMaterializationArtifactBytes)
		if err != nil {
			return fmt.Errorf("read generated lock %s: %w", relative, err)
		}
		if !bytes.Equal(content, expected) {
			return fmt.Errorf("generated lock %s does not match the current bundle", relative)
		}
	}
	inventoryPath := path.Join(WorkspaceGeneratedDirectory, MaterializationInventoryFileName)
	inventoryBytes, err := root.adapter.readBounded(inventoryPath, MaxMaterializationControlBytes)
	if err != nil {
		return fmt.Errorf("read generated lock inventory: %w", err)
	}
	inventory, err := parseMaterializationInventory(inventoryBytes)
	if err != nil {
		return err
	}
	if inventory.generatorVersion != PlanCheckpointGeneratorVersion {
		return fmt.Errorf("generated lock inventory has unexpected generator %q", inventory.generatorVersion)
	}
	expectedArtifacts, err := WorkspaceBundleLockArtifacts(bundle)
	if err != nil {
		return err
	}
	if len(inventory.artifacts) != len(expectedArtifacts) {
		return fmt.Errorf("generated lock inventory does not cover the current lock set")
	}
	expectedByID := make(map[string]MaterializationArtifact, len(expectedArtifacts))
	for _, artifact := range expectedArtifacts {
		expectedByID[artifact.id] = artifact
	}
	for _, owned := range inventory.artifacts {
		expected, exists := expectedByID[owned.id]
		if !exists || expected.path != owned.path || expected.hash != owned.lastGeneratedHash {
			return fmt.Errorf("generated lock inventory entry %s does not match the current lock set", owned.id)
		}
	}
	stateBytes, err := root.adapter.readBounded(
		path.Join(WorkspaceGeneratedDirectory, MaterializationStateFileName),
		MaxMaterializationControlBytes,
	)
	if err != nil {
		return fmt.Errorf("read generated lock state: %w", err)
	}
	if err := rejectDuplicateJSONObjectKeys(stateBytes); err != nil {
		return fmt.Errorf("generated lock state JSON: %w", err)
	}
	var state materializationStateWire
	if err := decodeStrictJSONRequired(stateBytes, &state); err != nil {
		return fmt.Errorf("decode generated lock state: %w", err)
	}
	if state.SchemaVersion != MaterializationInventorySchemaVersion {
		return fmt.Errorf("generated lock state has unsupported schema_version %d", state.SchemaVersion)
	}
	canonicalState, err := marshalMaterializationState(state)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonicalState, stateBytes) {
		return fmt.Errorf("generated lock state is not canonical")
	}
	_, inventoryHash, err := marshalMaterializationInventory(inventory)
	if err != nil {
		return err
	}
	if state.Phase != materializationPhaseActive || state.ActiveInventoryHash != inventoryHash.String() {
		return fmt.Errorf("generated lock materialization is not active at its exact inventory")
	}
	expectedAdministrative := planLockAdministrativePaths(inventory)
	if !equalStrings(expectedAdministrative, administrative) {
		return fmt.Errorf("generated lock administrative path inventory is inconsistent")
	}
	if err := verifyMaterializationOwnershipNamespaceAt(root.adapter, WorkspaceGeneratedDirectory); err != nil {
		return err
	}
	for _, directory := range inventory.directories {
		if err := verifyOwnedMaterializationDirectoryAt(root.adapter, WorkspaceGeneratedDirectory, directory); err != nil {
			return fmt.Errorf("verify generated lock directory %s: %w", directory, err)
		}
	}
	return nil
}

func verifyMaterializationOwnershipNamespaceAt(adapter *RootedFilesystemAdapter, prefix string) error {
	proof := path.Join(prefix, MaterializationOwnershipProofFileName)
	claim := path.Join(prefix, MaterializationOwnershipDirectoryName, materializationOwnershipRootClaimName)
	content, err := adapter.readBounded(proof, 1024)
	if err != nil || string(content) != "materialization-ownership-root:v2\n" {
		if err == nil {
			err = fmt.Errorf("ownership proof content changed")
		}
		return fmt.Errorf("verify generated lock ownership proof: %w", err)
	}
	same, err := adapter.sameFile(proof, claim)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("ownership proof identity changed")
		}
		return fmt.Errorf("verify generated lock ownership claim: %w", err)
	}
	return nil
}

func verifyOwnedMaterializationDirectoryAt(
	adapter *RootedFilesystemAdapter,
	prefix string,
	directory string,
) error {
	claim := path.Join(prefix, materializationDirectoryClaimPath(directory))
	anchor := path.Join(prefix, materializationDirectoryAnchorPath(directory))
	same, err := adapter.sameFile(claim, anchor)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("directory claim identity changed")
		}
		return err
	}
	content, err := adapter.readBounded(anchor, 8*1024)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, materializationDirectoryClaimContent(directory)) {
		return fmt.Errorf("directory claim content changed")
	}
	return nil
}

func injectPlanCheckpointFault(
	injector PlanCheckpointFaultInjector,
	point PlanCheckpointFaultPoint,
) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf("plan checkpoint fault at %s: %w", point, err)
	}
	return nil
}

func planCheckpointResult(
	bundle WorkspaceBundle,
	metadata planCheckpointMetadata,
	commit GitObjectID,
	tree GitObjectID,
	recovered bool,
) PlanCheckpointResult {
	result := PlanCheckpointResult{
		SchemaVersion: PlanCheckpointRequestSchemaVersion,
		Status:        "checkpointed", Kind: metadata.kind, Root: bundle.root,
		Commit: commit.String(), Tree: tree.String(),
		SourceDigest: metadata.source.String(), SemanticDigest: metadata.semantic.String(),
		Generation: metadata.generation.String(), Recovered: recovered,
	}
	if !metadata.lock.IsZero() {
		result.LockDigest = metadata.lock.String()
	}
	if !metadata.revisionID.IsZero() {
		result.RevisionID = metadata.revisionID.String()
	}
	if !metadata.reviewDigest.IsZero() {
		result.ReviewDigest = metadata.reviewDigest.String()
	}
	return result
}

func currentInventoryBytes(root *VerifiedRoot) ([]byte, bool, error) {
	content, err := root.ReadBounded(PlanRepositoryInventoryFileName, maxPlanRepositoryInventoryBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}
