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

type ValidateResult struct {
	Status       string        `json:"status"`
	WorkspaceDir string        `json:"workspace_dir"`
	LockPath     string        `json:"lock_path,omitempty"`
	Lock         WorkspaceLock `json:"lock,omitempty"`
}

type WorkspaceLock struct {
	SchemaVersion int                      `json:"schema_version"`
	WorkspaceID   string                   `json:"workspace_id"`
	Repo          string                   `json:"repo"`
	BaseRef       string                   `json:"base_ref"`
	Remote        string                   `json:"remote"`
	Plans         []WorkspacePlanLock      `json:"plans"`
	MergeUnits    []WorkspaceMergeUnitLock `json:"merge_units"`
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

type loadedPlanLock struct {
	Ref  WorkspacePlanRef
	Lock planpkg.Lock
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
	}
	loadedPlans := []loadedPlanLock{}
	for _, plan := range manifest.Plans {
		planDir := resolveWorkspacePath(workspaceDir, plan.Path)
		lockPath := filepath.Join(planDir, planLockFileName)
		hash, err := hashCanonicalJSONFile(lockPath)
		if err != nil {
			return WorkspaceLock{}, fmt.Errorf("plan %s lock: %w", plan.ID, err)
		}
		planLock, err := readPlanLock(lockPath)
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
	return lock, nil
}

func resolveWorkspacePath(workspaceDir string, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workspaceDir, path))
}

func hashCanonicalJSONFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return "", fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func readPlanLock(path string) (planpkg.Lock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return planpkg.Lock{}, err
	}
	var lock planpkg.Lock
	if err := json.Unmarshal(b, &lock); err != nil {
		return planpkg.Lock{}, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return lock, nil
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
