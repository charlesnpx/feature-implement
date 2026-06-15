package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type MaterializeOptions struct {
	ManifestPath string
	OutRoot      string
}

type MaterializeResult struct {
	Status  string     `json:"status"`
	PlanDir string     `json:"plan_dir"`
	Files   []PlanFile `json:"files"`
}

func Materialize(opts MaterializeOptions) (MaterializeResult, error) {
	manifest, err := readManifest(opts.ManifestPath)
	if err != nil {
		return MaterializeResult{}, err
	}
	if err := validateMaterializeShape(manifest); err != nil {
		return MaterializeResult{}, err
	}
	outRoot, err := defaultOutRoot(opts.OutRoot)
	if err != nil {
		return MaterializeResult{}, err
	}
	dirName := manifest.OutputName
	if strings.TrimSpace(dirName) == "" {
		dirName = manifest.ID
	}
	if strings.TrimSpace(dirName) == "" {
		dirName = slug(manifest.Title)
	}
	planDir := filepath.Join(outRoot, slug(dirName))
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return MaterializeResult{}, err
	}
	if err := writeYAML(filepath.Join(planDir, "feature.plan.yaml"), manifest); err != nil {
		return MaterializeResult{}, err
	}
	files := []PlanFile{{Kind: "manifest", ID: manifest.ID, Path: filepath.Join(planDir, "feature.plan.yaml")}}
	for _, epic := range manifest.Epics {
		epicSlug := fmt.Sprintf("%s-epic-%s", num(epic.Number), slug(epic.Name))
		epicDir := filepath.Join(planDir, epicSlug)
		if err := os.MkdirAll(epicDir, 0o755); err != nil {
			return MaterializeResult{}, err
		}
		epicFile := filepath.Join(epicDir, epicSlug+".md")
		if err := os.WriteFile(epicFile, []byte(epicMarkdown(epic)), 0o644); err != nil {
			return MaterializeResult{}, err
		}
		files = append(files, PlanFile{Kind: "epic", ID: epic.ID, Path: epicFile})
		for _, feature := range epic.Features {
			featureSlug := fmt.Sprintf("%s-feature-%s", num(feature.Number), slug(feature.Name))
			featureDir := filepath.Join(epicDir, featureSlug)
			if err := os.MkdirAll(featureDir, 0o755); err != nil {
				return MaterializeResult{}, err
			}
			featureFile := filepath.Join(featureDir, featureSlug+".md")
			if err := os.WriteFile(featureFile, []byte(featureMarkdown(epic, feature)), 0o644); err != nil {
				return MaterializeResult{}, err
			}
			files = append(files, PlanFile{Kind: "feature", ID: feature.ID, Path: featureFile})
			for _, story := range feature.Stories {
				storySlug := fmt.Sprintf("%s-story-%s", num(story.Number), slug(story.Name))
				storyFile := filepath.Join(featureDir, storySlug+".md")
				if err := os.WriteFile(storyFile, []byte(storyMarkdown(epic, feature, story)), 0o644); err != nil {
					return MaterializeResult{}, err
				}
				files = append(files, PlanFile{Kind: "story", ID: story.ID, Path: storyFile})
			}
		}
	}
	return MaterializeResult{Status: "materialized", PlanDir: planDir, Files: files}, nil
}

func defaultOutRoot(value string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		tmp := filepath.Join(home, "tmp")
		if info, statErr := os.Stat(tmp); statErr == nil && info.IsDir() {
			return tmp, nil
		}
	}
	return os.TempDir(), nil
}
