package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type ValidateOptions struct {
	PlanDir   string
	WriteLock bool
}

type ValidateResult struct {
	Status   string   `json:"status"`
	PlanDir  string   `json:"plan_dir"`
	LockPath string   `json:"lock_path,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

func Validate(opts ValidateOptions) (ValidateResult, error) {
	manifest, err := readPlanManifest(opts.PlanDir)
	if err != nil {
		return ValidateResult{}, err
	}
	if err := validateManifestShape(manifest); err != nil {
		return ValidateResult{Status: "invalid", PlanDir: opts.PlanDir, Errors: []string{err.Error()}}, err
	}
	files := expectedFiles(opts.PlanDir, manifest)
	for _, file := range files {
		if _, err := os.Stat(file.Path); err != nil {
			return ValidateResult{Status: "invalid", PlanDir: opts.PlanDir, Errors: []string{fmt.Sprintf("missing %s %s: %s", file.Kind, file.ID, file.Path)}}, err
		}
	}
	lock := buildLock(manifest, files)
	lockPath := filepath.Join(opts.PlanDir, "feature.plan.lock.json")
	if opts.WriteLock {
		if err := writeJSON(lockPath, lock); err != nil {
			return ValidateResult{}, err
		}
		return ValidateResult{Status: "valid", PlanDir: opts.PlanDir, LockPath: lockPath}, nil
	}
	return ValidateResult{Status: "valid", PlanDir: opts.PlanDir}, nil
}

func validateManifestShape(manifest Manifest) error {
	if err := validateMaterializeShape(manifest); err != nil {
		return err
	}
	storyFeature := map[string]string{}
	for _, epic := range manifest.Epics {
		for _, feature := range epic.Features {
			for _, story := range feature.Stories {
				storyFeature[story.ID] = feature.ID
			}
		}
	}
	if len(manifest.MergeUnits) == 0 {
		for storyID := range storyFeature {
			manifest.MergeUnits = append(manifest.MergeUnits, MergeUnit{ID: storyID, StoryIDs: []string{storyID}})
		}
	}
	assigned := map[string]string{}
	for _, unit := range manifest.MergeUnits {
		if unit.ID == "" {
			return fmt.Errorf("merge unit id is required")
		}
		if len(unit.StoryIDs) == 0 {
			return fmt.Errorf("merge unit %s requires at least one story", unit.ID)
		}
		unitFeatures := map[string]bool{}
		for _, storyID := range unit.StoryIDs {
			featureID, ok := storyFeature[storyID]
			if !ok {
				return fmt.Errorf("merge unit %s references unknown story %s", unit.ID, storyID)
			}
			if prior := assigned[storyID]; prior != "" {
				return fmt.Errorf("story %s assigned to both merge unit %s and %s", storyID, prior, unit.ID)
			}
			assigned[storyID] = unit.ID
			unitFeatures[featureID] = true
		}
		if len(unit.StoryIDs) > 1 && !unit.AllowFeatureLevelPR {
			return fmt.Errorf("merge unit %s has multiple stories but allow_feature_level_pr is false", unit.ID)
		}
		if len(unit.StoryIDs) > 1 && len(unitFeatures) > 1 {
			return fmt.Errorf("merge unit %s spans multiple features", unit.ID)
		}
	}
	for storyID := range storyFeature {
		if assigned[storyID] == "" {
			return fmt.Errorf("story %s is not assigned to a merge unit", storyID)
		}
	}
	storyUnit := map[string]string{}
	for storyID, unitID := range assigned {
		storyUnit[storyID] = unitID
	}
	for _, epic := range manifest.Epics {
		for _, feature := range epic.Features {
			for _, story := range feature.Stories {
				for _, dep := range story.Dependencies {
					if _, ok := storyFeature[dep]; !ok {
						return fmt.Errorf("story %s depends on unknown story %s", story.ID, dep)
					}
					if storyUnit[story.ID] != storyUnit[dep] && len(unitByID(manifest.MergeUnits, storyUnit[story.ID]).StoryIDs) > 1 {
						return fmt.Errorf("feature-level merge unit %s has external dependency %s", storyUnit[story.ID], dep)
					}
				}
			}
		}
	}
	return nil
}

func validateMaterializeShape(manifest Manifest) error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1")
	}
	if manifest.ID == "" {
		return fmt.Errorf("id is required")
	}
	if manifest.Title == "" {
		return fmt.Errorf("title is required")
	}
	if len(manifest.Epics) == 0 {
		return fmt.Errorf("at least one epic is required")
	}
	ids := map[string]string{}
	for _, epic := range manifest.Epics {
		if err := addID(ids, epic.ID, "epic"); err != nil {
			return err
		}
		if epic.Number <= 0 || epic.Name == "" {
			return fmt.Errorf("epic %s requires positive number and name", epic.ID)
		}
		if len(epic.Features) == 0 {
			return fmt.Errorf("epic %s requires at least one feature", epic.ID)
		}
		for _, feature := range epic.Features {
			if err := addID(ids, feature.ID, "feature"); err != nil {
				return err
			}
			if feature.Number <= 0 || feature.Name == "" {
				return fmt.Errorf("feature %s requires positive number and name", feature.ID)
			}
			if len(feature.Stories) == 0 {
				return fmt.Errorf("feature %s requires at least one story", feature.ID)
			}
			for _, story := range feature.Stories {
				if err := addID(ids, story.ID, "story"); err != nil {
					return err
				}
				if story.Number <= 0 || story.Name == "" {
					return fmt.Errorf("story %s requires positive number and name", story.ID)
				}
				if story.Summary == "" {
					return fmt.Errorf("story %s requires summary", story.ID)
				}
				if len(story.Acceptance) == 0 {
					return fmt.Errorf("story %s requires acceptance criteria", story.ID)
				}
				if len(story.Implementation) == 0 {
					return fmt.Errorf("story %s requires implementation details", story.ID)
				}
				if len(story.Testing) == 0 {
					return fmt.Errorf("story %s requires testing criteria", story.ID)
				}
			}
		}
	}
	return nil
}

func addID(ids map[string]string, id string, kind string) error {
	if id == "" {
		return fmt.Errorf("%s id is required", kind)
	}
	if prior := ids[id]; prior != "" {
		return fmt.Errorf("duplicate id %s used by %s and %s", id, prior, kind)
	}
	ids[id] = kind
	return nil
}

func unitByID(units []MergeUnit, id string) MergeUnit {
	for _, unit := range units {
		if unit.ID == id {
			return unit
		}
	}
	return MergeUnit{}
}

func expectedFiles(planDir string, manifest Manifest) []PlanFile {
	files := []PlanFile{{Kind: "manifest", ID: manifest.ID, Path: filepath.Join(planDir, "feature.plan.yaml")}}
	for _, epic := range manifest.Epics {
		epicSlug := fmt.Sprintf("%s-epic-%s", num(epic.Number), slug(epic.Name))
		epicDir := filepath.Join(planDir, epicSlug)
		files = append(files, PlanFile{Kind: "epic", ID: epic.ID, Path: filepath.Join(epicDir, epicSlug+".md")})
		for _, feature := range epic.Features {
			featureSlug := fmt.Sprintf("%s-feature-%s", num(feature.Number), slug(feature.Name))
			featureDir := filepath.Join(epicDir, featureSlug)
			files = append(files, PlanFile{Kind: "feature", ID: feature.ID, Path: filepath.Join(featureDir, featureSlug+".md")})
			for _, story := range feature.Stories {
				storySlug := fmt.Sprintf("%s-story-%s", num(story.Number), slug(story.Name))
				files = append(files, PlanFile{Kind: "story", ID: story.ID, Path: filepath.Join(featureDir, storySlug+".md")})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func buildLock(manifest Manifest, files []PlanFile) Lock {
	units := manifest.MergeUnits
	if len(units) == 0 {
		for _, epic := range manifest.Epics {
			for _, feature := range epic.Features {
				for _, story := range feature.Stories {
					units = append(units, MergeUnit{ID: story.ID, Name: story.Name, StoryIDs: []string{story.ID}})
				}
			}
		}
	}
	state := RuntimeState{SchemaVersion: 1, MergeUnits: map[string]MergeUnitState{}}
	for _, unit := range units {
		state.MergeUnits[unit.ID] = MergeUnitState{Status: "pending"}
	}
	return Lock{
		SchemaVersion: 1,
		ManifestID:    manifest.ID,
		Title:         manifest.Title,
		BaseRef:       manifest.BaseRef,
		Remote:        manifest.Remote,
		MergePolicy:   manifest.MergePolicy,
		Epics:         manifest.Epics,
		MergeUnits:    units,
		Files:         files,
		State:         state,
	}
}
