package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadManifestValidWorkspace(t *testing.T) {
	path := writeManifest(t, validWorkspaceManifest())

	manifest, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.ID != "example-project" {
		t.Fatalf("id = %q", manifest.ID)
	}
	if len(manifest.Plans) != 2 {
		t.Fatalf("plans = %+v", manifest.Plans)
	}
	if len(manifest.Dependencies) != 1 {
		t.Fatalf("dependencies = %+v", manifest.Dependencies)
	}
	if len(manifest.ContractGates) != 1 {
		t.Fatalf("contract gates = %+v", manifest.ContractGates)
	}
	gate := manifest.ContractGates[0]
	if gate.ID != "core-contracts" || len(gate.Producers) != 1 || len(gate.Consumers) != 1 {
		t.Fatalf("contract gate did not parse: %+v", gate)
	}
	if len(gate.Validation.Commands) != 1 || gate.Validation.Commands[0] != "go test ./..." {
		t.Fatalf("validation commands = %+v", gate.Validation.Commands)
	}
}

func TestValidateManifestRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name     string
		manifest WorkspaceManifest
		wantErr  string
	}{
		{
			name:     "schema version",
			manifest: WorkspaceManifest{},
			wantErr:  "schema_version must be 1",
		},
		{
			name: "id",
			manifest: WorkspaceManifest{
				SchemaVersion: 1,
			},
			wantErr: `workspace id ""`,
		},
		{
			name: "repo",
			manifest: WorkspaceManifest{
				SchemaVersion: 1,
				ID:            "workspace-a",
			},
			wantErr: "repo is required",
		},
		{
			name: "base ref",
			manifest: WorkspaceManifest{
				SchemaVersion: 1,
				ID:            "workspace-a",
				Repo:          ".",
			},
			wantErr: "base_ref is required",
		},
		{
			name: "remote",
			manifest: WorkspaceManifest{
				SchemaVersion: 1,
				ID:            "workspace-a",
				Repo:          ".",
				BaseRef:       "workspace-orchestration",
			},
			wantErr: "remote is required",
		},
		{
			name: "plans",
			manifest: WorkspaceManifest{
				SchemaVersion: 1,
				ID:            "workspace-a",
				Repo:          ".",
				BaseRef:       "workspace-orchestration",
				Remote:        "origin",
			},
			wantErr: "at least one plan is required",
		},
		{
			name: "plan path",
			manifest: WorkspaceManifest{
				SchemaVersion: 1,
				ID:            "workspace-a",
				Repo:          ".",
				BaseRef:       "workspace-orchestration",
				Remote:        "origin",
				Plans:         []WorkspacePlanRef{{ID: "foundation"}},
			},
			wantErr: "plan foundation path is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManifest(tt.manifest)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateManifest error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateManifestRejectsDuplicatePlanIDs(t *testing.T) {
	manifest := validWorkspace()
	manifest.Plans = append(manifest.Plans, WorkspacePlanRef{ID: "foundation", Path: "again"})

	err := ValidateManifest(manifest)

	if err == nil || !strings.Contains(err.Error(), "duplicate plan id foundation") {
		t.Fatalf("ValidateManifest error = %v", err)
	}
}

func TestValidateManifestRejectsMalformedMergeUnitReferences(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*WorkspaceManifest)
		wantErr string
	}{
		{
			name: "dependency before",
			mutate: func(manifest *WorkspaceManifest) {
				manifest.Dependencies[0].Before = "foundation"
			},
			wantErr: "dependency 1 before",
		},
		{
			name: "dependency after",
			mutate: func(manifest *WorkspaceManifest) {
				manifest.Dependencies[0].After = "sources:bad_id"
			},
			wantErr: "dependency 1 after",
		},
		{
			name: "contract producer",
			mutate: func(manifest *WorkspaceManifest) {
				manifest.ContractGates[0].Producers = []string{"foundation:"}
			},
			wantErr: "contract gate core-contracts producer 1",
		},
		{
			name: "contract consumer",
			mutate: func(manifest *WorkspaceManifest) {
				manifest.ContractGates[0].Consumers = []string{"sources:story-source-schema:extra"}
			},
			wantErr: "contract gate core-contracts consumer 1",
		},
		{
			name: "dependency unknown plan",
			mutate: func(manifest *WorkspaceManifest) {
				manifest.Dependencies[0].Before = "missing:story-a"
			},
			wantErr: "references unknown plan missing",
		},
		{
			name: "contract producer unknown plan",
			mutate: func(manifest *WorkspaceManifest) {
				manifest.ContractGates[0].Producers = []string{"missing:story-a"}
			},
			wantErr: "references unknown plan missing",
		},
		{
			name: "contract consumer unknown plan",
			mutate: func(manifest *WorkspaceManifest) {
				manifest.ContractGates[0].Consumers = []string{"missing:story-a"}
			},
			wantErr: "references unknown plan missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validWorkspace()
			tt.mutate(&manifest)

			err := ValidateManifest(manifest)

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateManifest error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseMergeUnitRef(t *testing.T) {
	ref, err := ParseMergeUnitRef("foundation:story-id-namespaces")
	if err != nil {
		t.Fatalf("ParseMergeUnitRef: %v", err)
	}
	if ref.PlanID != "foundation" || ref.MergeUnitID != "story-id-namespaces" {
		t.Fatalf("ref = %+v", ref)
	}
}

func validWorkspace() WorkspaceManifest {
	return WorkspaceManifest{
		SchemaVersion: 1,
		ID:            "example-project",
		Repo:          ".",
		BaseRef:       "workspace-orchestration",
		Remote:        "origin",
		Plans: []WorkspacePlanRef{
			{ID: "foundation", Path: "plans/foundation"},
			{ID: "sources", Path: "plans/sources"},
		},
		Dependencies: []WorkspaceDependency{
			{Before: "foundation:story-id-namespaces", After: "sources:story-source-schema"},
		},
		ContractGates: []WorkspaceContractGate{{
			ID:        "core-contracts",
			Producers: []string{"foundation:story-id-namespaces"},
			Consumers: []string{"sources:story-source-schema"},
			Validation: WorkspaceGateValidation{
				Commands: []string{"go test ./..."},
			},
		}},
	}
}

func validWorkspaceManifest() string {
	return `schema_version: 1
id: example-project
repo: .
base_ref: workspace-orchestration
remote: origin
plans:
  - id: foundation
    path: plans/foundation
  - id: sources
    path: plans/sources
dependencies:
  - before: foundation:story-id-namespaces
    after: sources:story-source-schema
contract_gates:
  - id: core-contracts
    producers:
      - foundation:story-id-namespaces
    consumers:
      - sources:story-source-schema
    validation:
      commands:
        - go test ./...
`
}

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ManifestFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
