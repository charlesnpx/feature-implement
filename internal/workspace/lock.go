package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	planpkg "github.com/charlesnpx/feature-implement/internal/plan"
)

const (
	LockFileName      = "feature.workspace.lock.json"
	lockSchemaVersion = 1
	planLockFileName  = "feature.plan.lock.json"
)

type ValidateOptions struct {
	WorkspaceDir string
	WriteLock    bool
}

type InitOptions struct {
	ManifestPath string
	WriteLock    bool
}

type InitResult struct {
	Status           string `json:"status"`
	WorkspaceDir     string `json:"workspace_dir"`
	ManifestPath     string `json:"manifest_path"`
	SourceManifest   string `json:"source_manifest,omitempty"`
	LockPath         string `json:"lock_path,omitempty"`
	ViewPath         string `json:"view_path,omitempty"`
	WorkspaceID      string `json:"workspace_id"`
	PlanCount        int    `json:"plan_count"`
	MergeUnitCount   int    `json:"merge_unit_count"`
	ContractCount    int    `json:"contract_count"`
	SchedulerReady   int    `json:"scheduler_ready,omitempty"`
	SchedulerBlocked int    `json:"scheduler_blocked,omitempty"`
}

type ValidateResult struct {
	Status       string        `json:"status"`
	WorkspaceDir string        `json:"workspace_dir"`
	LockPath     string        `json:"lock_path,omitempty"`
	Lock         WorkspaceLock `json:"lock,omitempty"`
}

type WorkspaceLock struct {
	SchemaVersion int                         `json:"schema_version"`
	WorkspaceID   string                      `json:"workspace_id"`
	Repo          string                      `json:"repo"`
	BaseRef       string                      `json:"base_ref"`
	Remote        string                      `json:"remote"`
	GatePolicy    WorkspaceGatePolicyLock     `json:"gate_policy"`
	Plans         []WorkspacePlanLock         `json:"plans"`
	MergeUnits    []WorkspaceMergeUnitLock    `json:"merge_units"`
	ContractGates []WorkspaceContractGateLock `json:"contract_gates,omitempty"`
}

type WorkspacePlanLock struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	LockPath string `json:"lock_path"`
	LockHash string `json:"lock_hash"`
}

type WorkspaceMergeUnitLock struct {
	ID           string   `json:"id"`
	PlanID       string   `json:"plan_id"`
	MergeUnitID  string   `json:"merge_unit_id"`
	StoryIDs     []string `json:"story_ids"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type WorkspaceContractGateLock struct {
	ID                 string                          `json:"id"`
	Producers          []string                        `json:"producers"`
	Consumers          []string                        `json:"consumers"`
	Artifacts          []WorkspaceContractArtifactLock `json:"artifacts"`
	ValidationCommands []string                        `json:"validation_commands"`
}

type WorkspaceContractArtifactLock struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type WorkspaceGatePolicyLock struct {
	ID             string                                 `json:"id"`
	Version        string                                 `json:"version"`
	RetentionRules []WorkspaceGatePolicyRetentionRuleLock `json:"retention_rules"`
}

type WorkspaceGatePolicyRetentionRuleLock struct {
	Evidence     string `json:"evidence"`
	Scope        string `json:"scope"`
	CarryForward bool   `json:"carry_forward"`
}

type loadedPlanLock struct {
	Ref  WorkspacePlanRef
	Lock planpkg.Lock
}

func Init(opts InitOptions) (InitResult, error) {
	if opts.ManifestPath == "" {
		return InitResult{}, fmt.Errorf("workspace init requires --manifest")
	}
	sourceManifestPath := filepath.Clean(opts.ManifestPath)
	manifestPath := canonicalInitManifestPath(sourceManifestPath)
	workspaceDir := filepath.Dir(manifestPath)
	manifest, err := ReadManifest(sourceManifestPath)
	if err != nil {
		return InitResult{}, err
	}
	lock, err := BuildLock(workspaceDir, manifest)
	if err != nil {
		return InitResult{}, err
	}
	copied := false
	if sourceManifestPath != manifestPath {
		if err := copyManifestSameDir(sourceManifestPath, manifestPath); err != nil {
			return InitResult{}, err
		}
		copied = true
	}
	result := InitResult{
		Status:         "initialized",
		WorkspaceDir:   workspaceDir,
		ManifestPath:   manifestPath,
		WorkspaceID:    lock.WorkspaceID,
		PlanCount:      len(lock.Plans),
		MergeUnitCount: len(lock.MergeUnits),
		ContractCount:  len(lock.ContractGates),
	}
	if copied {
		result.SourceManifest = sourceManifestPath
	}
	if !opts.WriteLock {
		return result, nil
	}
	lockPath := filepath.Join(workspaceDir, LockFileName)
	if err := writeStableJSON(lockPath, lock); err != nil {
		return InitResult{}, err
	}
	view, err := RebuildSchedulerView(workspaceDir)
	if err != nil {
		return InitResult{}, err
	}
	result.LockPath = lockPath
	result.ViewPath = SchedulerViewPath(workspaceDir)
	result.SchedulerReady = len(view.Ready)
	result.SchedulerBlocked = len(view.Blocked)
	return result, nil
}

func canonicalInitManifestPath(sourcePath string) string {
	if filepath.Base(sourcePath) == ManifestFileName {
		return sourcePath
	}
	return filepath.Join(filepath.Dir(sourcePath), ManifestFileName)
}

func copyManifestSameDir(sourcePath string, targetPath string) error {
	sourceDir, err := filepath.Abs(filepath.Dir(sourcePath))
	if err != nil {
		return err
	}
	targetDir, err := filepath.Abs(filepath.Dir(targetPath))
	if err != nil {
		return err
	}
	if sourceDir != targetDir {
		return fmt.Errorf("workspace init can only canonicalize manifests within the same directory; move the manifest beside %s or use absolute plans[].path values before copying across directories", ManifestFileName)
	}
	b, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	info, err := os.Lstat(targetPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("workspace init refused to overwrite existing non-regular %s", ManifestFileName)
		}
		existing, err := os.ReadFile(targetPath)
		if err != nil {
			return err
		}
		if string(existing) != string(b) {
			return fmt.Errorf("workspace init refused to overwrite existing %s with different contents; move the manifest beside %s or remove the existing canonical manifest after verifying relative plans[].path values", ManifestFileName, ManifestFileName)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("workspace init refused to create %s because it appeared during canonicalization", ManifestFileName)
		}
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func Validate(opts ValidateOptions) (ValidateResult, error) {
	workspaceDir := opts.WorkspaceDir
	if workspaceDir == "" {
		return ValidateResult{}, fmt.Errorf("workspace validate requires <workspace-dir>")
	}
	manifest, err := ReadManifest(filepath.Join(workspaceDir, ManifestFileName))
	if err != nil {
		return ValidateResult{}, err
	}
	lock, err := BuildLock(workspaceDir, manifest)
	if err != nil {
		return ValidateResult{}, err
	}
	lockPath := filepath.Join(workspaceDir, LockFileName)
	if opts.WriteLock {
		if err := writeStableJSON(lockPath, lock); err != nil {
			return ValidateResult{}, err
		}
		return ValidateResult{Status: "valid", WorkspaceDir: workspaceDir, LockPath: lockPath, Lock: lock}, nil
	}
	return ValidateResult{Status: "valid", WorkspaceDir: workspaceDir, Lock: lock}, nil
}

func BuildLock(workspaceDir string, manifest WorkspaceManifest) (WorkspaceLock, error) {
	lock := WorkspaceLock{
		SchemaVersion: lockSchemaVersion,
		WorkspaceID:   manifest.ID,
		Repo:          manifest.Repo,
		BaseRef:       manifest.BaseRef,
		Remote:        manifest.Remote,
		GatePolicy:    defaultWorkspaceGatePolicyLock(),
	}
	loadedPlans := []loadedPlanLock{}
	for _, plan := range manifest.Plans {
		planDir := resolveWorkspacePath(workspaceDir, plan.Path)
		lockPath := filepath.Join(planDir, planLockFileName)
		planLock, hash, err := readPlanLockSnapshot(lockPath)
		if err != nil {
			return WorkspaceLock{}, fmt.Errorf("plan %s lock: %w", plan.ID, err)
		}
		lock.Plans = append(lock.Plans, WorkspacePlanLock{
			ID:       plan.ID,
			Path:     plan.Path,
			LockPath: lockPath,
			LockHash: hash,
		})
		loadedPlans = append(loadedPlans, loadedPlanLock{Ref: plan, Lock: planLock})
	}
	mergeUnits, err := buildMergeUnitIndex(manifest, loadedPlans)
	if err != nil {
		return WorkspaceLock{}, err
	}
	lock.MergeUnits = mergeUnits
	contractGates, err := buildContractGateLocks(manifest.ContractGates, knownMergeUnitSet(mergeUnits))
	if err != nil {
		return WorkspaceLock{}, err
	}
	lock.ContractGates = contractGates
	return lock, nil
}

func defaultWorkspaceGatePolicyLock() WorkspaceGatePolicyLock {
	return WorkspaceGatePolicyLock{
		ID:      "default-review-gates",
		Version: "1",
		RetentionRules: []WorkspaceGatePolicyRetentionRuleLock{
			{Evidence: "reviews", Scope: "attempt", CarryForward: false},
			{Evidence: "check_results", Scope: "attempt", CarryForward: false},
			{Evidence: "refresh_inputs", Scope: "attempt", CarryForward: false},
		},
	}
}

func resolveWorkspacePath(workspaceDir string, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workspaceDir, path))
}

func readPlanLockSnapshot(path string) (planpkg.Lock, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return planpkg.Lock{}, "", err
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return planpkg.Lock{}, "", fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return planpkg.Lock{}, "", err
	}
	var lock planpkg.Lock
	if err := json.Unmarshal(b, &lock); err != nil {
		return planpkg.Lock{}, "", fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	sum := sha256.Sum256(canonical)
	return lock, hex.EncodeToString(sum[:]), nil
}

func buildMergeUnitIndex(manifest WorkspaceManifest, plans []loadedPlanLock) ([]WorkspaceMergeUnitLock, error) {
	known := map[string]bool{}
	storyUnit := map[string]string{}
	dependencySets := map[string]map[string]bool{}
	units := []WorkspaceMergeUnitLock{}

	for _, loaded := range plans {
		for _, unit := range loaded.Lock.MergeUnits {
			globalID := globalMergeUnitID(loaded.Ref.ID, unit.ID)
			if known[globalID] {
				return nil, fmt.Errorf("duplicate global merge unit id %s", globalID)
			}
			known[globalID] = true
			dependencySets[globalID] = map[string]bool{}
			storyIDs := append([]string(nil), unit.StoryIDs...)
			units = append(units, WorkspaceMergeUnitLock{
				ID:          globalID,
				PlanID:      loaded.Ref.ID,
				MergeUnitID: unit.ID,
				StoryIDs:    storyIDs,
			})
			for _, storyID := range storyIDs {
				storyRef := globalMergeUnitID(loaded.Ref.ID, storyID)
				if prior := storyUnit[storyRef]; prior != "" {
					return nil, fmt.Errorf("story %s assigned to both %s and %s", storyRef, prior, globalID)
				}
				storyUnit[storyRef] = globalID
			}
		}
	}

	for _, loaded := range plans {
		for _, story := range planStories(loaded.Lock) {
			currentUnit := storyUnit[globalMergeUnitID(loaded.Ref.ID, story.ID)]
			if currentUnit == "" {
				return nil, fmt.Errorf("plan %s story %s is not assigned to a merge unit", loaded.Ref.ID, story.ID)
			}
			for _, dep := range story.Dependencies {
				dependencyUnit := storyUnit[globalMergeUnitID(loaded.Ref.ID, dep)]
				if dependencyUnit == "" {
					return nil, fmt.Errorf("plan %s story %s depends on unknown story %s", loaded.Ref.ID, story.ID, dep)
				}
				if dependencyUnit != currentUnit {
					dependencySets[currentUnit][dependencyUnit] = true
				}
			}
		}
	}

	for i, dependency := range manifest.Dependencies {
		before, err := requireKnownMergeUnitRef(dependency.Before, known)
		if err != nil {
			return nil, fmt.Errorf("dependency %d before: %w", i+1, err)
		}
		after, err := requireKnownMergeUnitRef(dependency.After, known)
		if err != nil {
			return nil, fmt.Errorf("dependency %d after: %w", i+1, err)
		}
		if before != after {
			dependencySets[after][before] = true
		}
	}

	for i := range units {
		units[i].Dependencies = sortedKeys(dependencySets[units[i].ID])
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	return units, nil
}

func knownMergeUnitSet(units []WorkspaceMergeUnitLock) map[string]bool {
	known := map[string]bool{}
	for _, unit := range units {
		known[unit.ID] = true
	}
	return known
}

func buildContractGateLocks(gates []WorkspaceContractGate, knownMergeUnits map[string]bool) ([]WorkspaceContractGateLock, error) {
	locked := make([]WorkspaceContractGateLock, 0, len(gates))
	for _, gate := range gates {
		producers, err := requireKnownMergeUnitRefs(gate.Producers, knownMergeUnits, "producer")
		if err != nil {
			return nil, fmt.Errorf("contract gate %s: %w", gate.ID, err)
		}
		consumers, err := requireKnownMergeUnitRefs(gate.Consumers, knownMergeUnits, "consumer")
		if err != nil {
			return nil, fmt.Errorf("contract gate %s: %w", gate.ID, err)
		}
		artifacts, err := lockContractArtifacts(gate.Artifacts)
		if err != nil {
			return nil, fmt.Errorf("contract gate %s: %w", gate.ID, err)
		}
		commands := make([]string, 0, len(gate.Validation.Commands))
		for _, command := range gate.Validation.Commands {
			commands = append(commands, command)
		}
		locked = append(locked, WorkspaceContractGateLock{
			ID:                 gate.ID,
			Producers:          producers,
			Consumers:          consumers,
			Artifacts:          artifacts,
			ValidationCommands: commands,
		})
	}
	sort.Slice(locked, func(i, j int) bool { return locked[i].ID < locked[j].ID })
	return locked, nil
}

func requireKnownMergeUnitRefs(values []string, known map[string]bool, kind string) ([]string, error) {
	refs := make([]string, 0, len(values))
	seen := map[string]bool{}
	for i, value := range values {
		ref, err := requireKnownMergeUnitRef(value, known)
		if err != nil {
			return nil, fmt.Errorf("%s %d: %w", kind, i+1, err)
		}
		if seen[ref] {
			return nil, fmt.Errorf("duplicate %s merge unit %s", kind, ref)
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func lockContractArtifacts(artifacts []WorkspaceArtifactSpec) ([]WorkspaceContractArtifactLock, error) {
	locked := make([]WorkspaceContractArtifactLock, 0, len(artifacts))
	for i, artifact := range artifacts {
		path, err := normalizeRepoArtifactPath(artifact.Path)
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", i+1, err)
		}
		locked = append(locked, WorkspaceContractArtifactLock{
			ID:   artifact.ID,
			Path: path,
		})
	}
	sort.Slice(locked, func(i, j int) bool { return locked[i].ID < locked[j].ID })
	return locked, nil
}

func planStories(lock planpkg.Lock) []planpkg.Story {
	stories := []planpkg.Story{}
	for _, epic := range lock.Epics {
		for _, feature := range epic.Features {
			stories = append(stories, feature.Stories...)
		}
	}
	return stories
}

func requireKnownMergeUnitRef(value string, known map[string]bool) (string, error) {
	ref, err := ParseMergeUnitRef(value)
	if err != nil {
		return "", err
	}
	globalID := globalMergeUnitID(ref.PlanID, ref.MergeUnitID)
	if !known[globalID] {
		return "", fmt.Errorf("unknown merge unit %s", globalID)
	}
	return globalID, nil
}

func globalMergeUnitID(planID string, mergeUnitID string) string {
	return planID + mergeUnitRefSeparator + mergeUnitID
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeStableJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
