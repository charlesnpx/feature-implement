package plan

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	"gopkg.in/yaml.v3"
)

const PlanMaterializationGeneratorVersion = "feature-plan-materializer/v2"

type MaterializeOptions struct {
	ManifestPath  string
	OutRoot       string
	FaultInjector workspace.MaterializationFaultInjector
}

type MaterializeResult struct {
	Status        string     `json:"status"`
	PlanDir       string     `json:"plan_dir"`
	InventoryPath string     `json:"inventory_path"`
	Files         []PlanFile `json:"files"`
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
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return MaterializeResult{}, fmt.Errorf("create materialization output root: %w", err)
	}
	planDir := filepath.Join(outRoot, slug(dirName))
	manifestBytes, err := yaml.Marshal(manifest)
	if err != nil {
		return MaterializeResult{}, err
	}
	artifacts := make([]workspace.MaterializationArtifact, 0)
	files := make([]PlanFile, 0)
	appendArtifact := func(kind, id, relative string, content []byte) error {
		artifact, err := workspace.NewMaterializationArtifact(kind+"/"+id, relative, content)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		files = append(files, PlanFile{Kind: kind, ID: id, Path: filepath.Join(planDir, filepath.FromSlash(relative))})
		return nil
	}
	if err := appendArtifact("manifest", manifest.ID, "feature.plan.yaml", manifestBytes); err != nil {
		return MaterializeResult{}, err
	}
	for _, epic := range manifest.Epics {
		epicSlug := fmt.Sprintf("%s-epic-%s", num(epic.Number), slug(epic.Name))
		epicFile := path.Join(epicSlug, epicSlug+".md")
		if err := appendArtifact("epic", epic.ID, epicFile, []byte(epicMarkdown(epic))); err != nil {
			return MaterializeResult{}, err
		}
		for _, feature := range epic.Features {
			featureSlug := fmt.Sprintf("%s-feature-%s", num(feature.Number), slug(feature.Name))
			featureDir := path.Join(epicSlug, featureSlug)
			featureFile := path.Join(featureDir, featureSlug+".md")
			if err := appendArtifact("feature", feature.ID, featureFile, []byte(featureMarkdown(epic, feature))); err != nil {
				return MaterializeResult{}, err
			}
			for _, story := range feature.Stories {
				storySlug := fmt.Sprintf("%s-story-%s", num(story.Number), slug(story.Name))
				storyFile := path.Join(featureDir, storySlug+".md")
				if err := appendArtifact("story", story.ID, storyFile, []byte(storyMarkdown(epic, feature, story))); err != nil {
					return MaterializeResult{}, err
				}
			}
		}
	}
	if _, err := workspace.SynchronizeMaterialization(
		planDir,
		PlanMaterializationGeneratorVersion,
		artifacts,
		workspace.MaterializationOptions{FaultInjector: opts.FaultInjector},
	); err != nil {
		return MaterializeResult{}, err
	}
	return MaterializeResult{
		Status: "materialized", PlanDir: planDir,
		InventoryPath: filepath.Join(planDir, workspace.MaterializationInventoryFileName),
		Files:         files,
	}, nil
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
