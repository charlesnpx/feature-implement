package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func readManifest(path string) (Manifest, error) {
	if strings.TrimSpace(path) == "" {
		return Manifest{}, fmt.Errorf("--manifest is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(b, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return manifest, nil
}

func readPlanManifest(planDir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(planDir, "feature.plan.yaml"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(b, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse feature.plan.yaml: %w", err)
	}
	return manifest, nil
}

func writeYAML(path string, value any) error {
	b, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
