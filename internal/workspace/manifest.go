package workspace

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ManifestFileName      = "feature.workspace.yaml"
	manifestSchemaVersion = 1
	safeIDPattern         = `^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`
	mergeUnitRefSeparator = ":"
)

var safeID = regexp.MustCompile(safeIDPattern)
var windowsVolumePath = regexp.MustCompile(`^[A-Za-z]:`)

type WorkspaceManifest struct {
	SchemaVersion int                     `yaml:"schema_version" json:"schema_version"`
	ID            string                  `yaml:"id" json:"id"`
	Repo          string                  `yaml:"repo" json:"repo"`
	BaseRef       string                  `yaml:"base_ref" json:"base_ref"`
	Remote        string                  `yaml:"remote" json:"remote"`
	Plans         []WorkspacePlanRef      `yaml:"plans" json:"plans"`
	Dependencies  []WorkspaceDependency   `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
	ContractGates []WorkspaceContractGate `yaml:"contract_gates,omitempty" json:"contract_gates,omitempty"`
}

type WorkspacePlanRef struct {
	ID   string `yaml:"id" json:"id"`
	Path string `yaml:"path" json:"path"`
}

type WorkspaceDependency struct {
	Before string `yaml:"before" json:"before"`
	After  string `yaml:"after" json:"after"`
}

type WorkspaceContractGate struct {
	ID         string                  `yaml:"id" json:"id"`
	Producers  []string                `yaml:"producers" json:"producers"`
	Consumers  []string                `yaml:"consumers" json:"consumers"`
	Artifacts  []WorkspaceArtifactSpec `yaml:"artifacts" json:"artifacts"`
	Validation WorkspaceGateValidation `yaml:"validation,omitempty" json:"validation,omitempty"`
}

type WorkspaceArtifactSpec struct {
	ID   string `yaml:"id" json:"id"`
	Path string `yaml:"path" json:"path"`
}

type WorkspaceGateValidation struct {
	Commands []string `yaml:"commands,omitempty" json:"commands,omitempty"`
}

type MergeUnitRef struct {
	PlanID      string
	MergeUnitID string
}

func ReadManifest(path string) (WorkspaceManifest, error) {
	if strings.TrimSpace(path) == "" {
		return WorkspaceManifest{}, fmt.Errorf("--manifest is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	var manifest WorkspaceManifest
	if err := yaml.Unmarshal(b, &manifest); err != nil {
		return WorkspaceManifest{}, fmt.Errorf("parse %s: %w", ManifestFileName, err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return WorkspaceManifest{}, err
	}
	return manifest, nil
}

func ValidateManifest(manifest WorkspaceManifest) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("schema_version must be %d", manifestSchemaVersion)
	}
	if err := requireSafeID(manifest.ID, "workspace"); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.Repo) == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(manifest.BaseRef) == "" {
		return fmt.Errorf("base_ref is required")
	}
	if strings.TrimSpace(manifest.Remote) == "" {
		return fmt.Errorf("remote is required")
	}
	if len(manifest.Plans) == 0 {
		return fmt.Errorf("at least one plan is required")
	}
	planIDs := map[string]bool{}
	for _, plan := range manifest.Plans {
		if err := requireSafeID(plan.ID, "plan"); err != nil {
			return err
		}
		if planIDs[plan.ID] {
			return fmt.Errorf("duplicate plan id %s", plan.ID)
		}
		planIDs[plan.ID] = true
		if strings.TrimSpace(plan.Path) == "" {
			return fmt.Errorf("plan %s path is required", plan.ID)
		}
	}
	for i, dep := range manifest.Dependencies {
		if err := validateMergeUnitRef(dep.Before, planIDs); err != nil {
			return fmt.Errorf("dependency %d before: %w", i+1, err)
		}
		if err := validateMergeUnitRef(dep.After, planIDs); err != nil {
			return fmt.Errorf("dependency %d after: %w", i+1, err)
		}
	}
	gateIDs := map[string]bool{}
	for _, gate := range manifest.ContractGates {
		if err := requireSafeID(gate.ID, "contract gate"); err != nil {
			return err
		}
		if gateIDs[gate.ID] {
			return fmt.Errorf("duplicate contract gate id %s", gate.ID)
		}
		gateIDs[gate.ID] = true
		if len(gate.Producers) == 0 {
			return fmt.Errorf("contract gate %s requires at least one producer", gate.ID)
		}
		if len(gate.Consumers) == 0 {
			return fmt.Errorf("contract gate %s requires at least one consumer", gate.ID)
		}
		if len(gate.Artifacts) == 0 {
			return fmt.Errorf("contract gate %s requires at least one artifact", gate.ID)
		}
		if len(gate.Validation.Commands) == 0 {
			return fmt.Errorf("contract gate %s requires at least one validation command", gate.ID)
		}
		for i, producer := range gate.Producers {
			if err := validateMergeUnitRef(producer, planIDs); err != nil {
				return fmt.Errorf("contract gate %s producer %d: %w", gate.ID, i+1, err)
			}
		}
		for i, consumer := range gate.Consumers {
			if err := validateMergeUnitRef(consumer, planIDs); err != nil {
				return fmt.Errorf("contract gate %s consumer %d: %w", gate.ID, i+1, err)
			}
		}
		artifactIDs := map[string]bool{}
		for i, artifact := range gate.Artifacts {
			if err := requireSafeID(artifact.ID, "contract artifact"); err != nil {
				return err
			}
			if artifactIDs[artifact.ID] {
				return fmt.Errorf("contract gate %s duplicate artifact id %s", gate.ID, artifact.ID)
			}
			artifactIDs[artifact.ID] = true
			if _, err := normalizeRepoArtifactPath(artifact.Path); err != nil {
				return fmt.Errorf("contract gate %s artifact %d: %w", gate.ID, i+1, err)
			}
		}
		for i, command := range gate.Validation.Commands {
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("contract gate %s validation command %d is blank", gate.ID, i+1)
			}
		}
	}
	return nil
}

func normalizeRepoArtifactPath(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", fmt.Errorf("artifact path is required")
	}
	slashPath := strings.ReplaceAll(raw, "\\", "/")
	if path.IsAbs(slashPath) || windowsVolumePath.MatchString(slashPath) {
		return "", fmt.Errorf("artifact path %q must be repository-relative", value)
	}
	cleaned := path.Clean(slashPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("artifact path %q must stay within the repository", value)
	}
	return cleaned, nil
}

func ParseMergeUnitRef(value string) (MergeUnitRef, error) {
	parts := strings.Split(value, mergeUnitRefSeparator)
	if len(parts) != 2 {
		return MergeUnitRef{}, fmt.Errorf("merge-unit ref %q must use plan-id:merge-unit-id", value)
	}
	ref := MergeUnitRef{PlanID: parts[0], MergeUnitID: parts[1]}
	if err := requireSafeID(ref.PlanID, "merge-unit ref plan"); err != nil {
		return MergeUnitRef{}, err
	}
	if err := requireSafeID(ref.MergeUnitID, "merge-unit ref merge unit"); err != nil {
		return MergeUnitRef{}, err
	}
	return ref, nil
}

func validateMergeUnitRef(value string, planIDs map[string]bool) error {
	ref, err := ParseMergeUnitRef(value)
	if err != nil {
		return err
	}
	if !planIDs[ref.PlanID] {
		return fmt.Errorf("merge-unit ref %q references unknown plan %s", value, ref.PlanID)
	}
	return nil
}

func requireSafeID(id string, kind string) error {
	if !safeID.MatchString(id) {
		return fmt.Errorf("%s id %q must contain only lowercase letters, numbers, and hyphen separators", kind, id)
	}
	return nil
}
