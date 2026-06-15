package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeAndValidateWritesLock(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifestPath, []byte(sampleManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	materialized, err := Materialize(MaterializeOptions{ManifestPath: manifestPath, OutRoot: root})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	expected := filepath.Join(materialized.PlanDir, "001-epic-foundation", "001-feature-installer", "001-story-install-contract.md")
	b, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("expected story file: %v", err)
	}
	if !strings.Contains(string(b), "## Testing Criteria") {
		t.Fatalf("expected testing criteria section in story markdown:\n%s", string(b))
	}
	validated, err := Validate(ValidateOptions{PlanDir: materialized.PlanDir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if validated.LockPath == "" {
		t.Fatalf("expected lock path: %+v", validated)
	}
	status, err := Status(materialized.PlanDir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != "validated" {
		t.Fatalf("status = %s", status.Status)
	}
}

func TestValidateRejectsBrokenDependency(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	broken := `schema_version: 1
id: broken
title: Broken
epics:
  - id: epic-a
    number: 1
    name: Foundation
    features:
      - id: feature-a
        number: 1
        name: Installer
        stories:
          - id: story-a
            number: 1
            name: Install Contract
            summary: Implement install contract validation.
            acceptance:
              - Broken dependency is still represented for validation.
            implementation:
              - Keep this manifest valid except for the dependency reference.
            testing:
              - Confirm validation rejects the missing dependency.
            dependencies: [missing-story]
`
	if err := os.WriteFile(manifestPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	materialized, err := Materialize(MaterializeOptions{ManifestPath: manifestPath, OutRoot: root})
	if err != nil {
		t.Fatalf("Materialize should tolerate dependency validation until validate: %v", err)
	}
	if _, err := Validate(ValidateOptions{PlanDir: materialized.PlanDir}); err == nil {
		t.Fatalf("Validate should reject broken dependency")
	}
}

func TestMaterializeRejectsSparseStories(t *testing.T) {
	tests := []struct {
		name    string
		story   string
		wantErr string
	}{
		{
			name: "missing summary",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            acceptance:
              - Done behavior is defined.
            implementation:
              - Implement the behavior.
            testing:
              - Run the relevant checks.
`,
			wantErr: "story story-a requires summary",
		},
		{
			name: "blank summary",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            summary: "   "
            acceptance:
              - Done behavior is defined.
            implementation:
              - Implement the behavior.
            testing:
              - Run the relevant checks.
`,
			wantErr: "story story-a requires summary",
		},
		{
			name: "missing acceptance",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            summary: Implement the contract.
            implementation:
              - Implement the behavior.
            testing:
              - Run the relevant checks.
`,
			wantErr: "story story-a requires acceptance criteria",
		},
		{
			name: "blank acceptance",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            summary: Implement the contract.
            acceptance:
              - "   "
            implementation:
              - Implement the behavior.
            testing:
              - Run the relevant checks.
`,
			wantErr: "story story-a requires non-blank acceptance criteria item 1",
		},
		{
			name: "missing implementation",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            summary: Implement the contract.
            acceptance:
              - Done behavior is defined.
            testing:
              - Run the relevant checks.
`,
			wantErr: "story story-a requires implementation details",
		},
		{
			name: "blank implementation",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            summary: Implement the contract.
            acceptance:
              - Done behavior is defined.
            implementation:
              - "   "
            testing:
              - Run the relevant checks.
`,
			wantErr: "story story-a requires non-blank implementation details item 1",
		},
		{
			name: "missing testing",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            summary: Implement the contract.
            acceptance:
              - Done behavior is defined.
            implementation:
              - Implement the behavior.
`,
			wantErr: "story story-a requires testing criteria",
		},
		{
			name: "blank testing",
			story: `          - id: story-a
            number: 1
            name: Install Contract
            summary: Implement the contract.
            acceptance:
              - Done behavior is defined.
            implementation:
              - Implement the behavior.
            testing:
              - "   "
`,
			wantErr: "story story-a requires non-blank testing criteria item 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			manifestPath := filepath.Join(root, "feature.plan.yaml")
			manifest := `schema_version: 1
id: sparse
title: Sparse
output_name: sparse
epics:
  - id: epic-a
    number: 1
    name: Foundation
    features:
      - id: feature-a
        number: 1
        name: Installer
        stories:
` + tt.story
			if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Materialize(MaterializeOptions{ManifestPath: manifestPath, OutRoot: root})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Materialize error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestImplementRequiresLockAndExplicitWriteFlags(t *testing.T) {
	root := t.TempDir()
	if _, err := Implement(ImplementOptions{PlanDir: root, Action: "push"}); err == nil {
		t.Fatalf("implement should require lock")
	}
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifestPath, []byte(sampleManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	materialized, err := Materialize(MaterializeOptions{ManifestPath: manifestPath, OutRoot: root})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, err := Validate(ValidateOptions{PlanDir: materialized.PlanDir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	mergeUnitID := "story-install-contract"
	if _, err := Implement(ImplementOptions{PlanDir: materialized.PlanDir, Action: "start", MergeUnit: mergeUnitID, WriteState: true, BaseSHA: "base-sha"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: materialized.PlanDir, Action: "commit", MergeUnit: mergeUnitID, WriteState: true, CommitSHA: "commit-sha"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: materialized.PlanDir, Action: "push", MergeUnit: mergeUnitID}); err == nil {
		t.Fatalf("push should require explicit flag")
	}
	result, err := Implement(ImplementOptions{PlanDir: materialized.PlanDir, Action: "push", MergeUnit: mergeUnitID, AllowPush: true})
	if err != nil {
		t.Fatalf("push with flag: %v", err)
	}
	if result.Status != "planned" {
		t.Fatalf("result = %+v", result)
	}
}

func TestValidateRejectsDuplicateMergeUnitIDs(t *testing.T) {
	manifest := minimalValidManifest()
	manifest.MergeUnits = []MergeUnit{
		{ID: "story-a", Name: "Story A", StoryIDs: []string{"story-a"}},
		{ID: "story-a", Name: "Story A Again", StoryIDs: []string{"story-b"}},
	}

	err := validateManifestShape(manifest)

	if err == nil || !strings.Contains(err.Error(), "duplicate merge unit id story-a") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestValidateRejectsUnsafeMergeUnitIDs(t *testing.T) {
	manifest := minimalValidManifest()
	manifest.MergeUnits = []MergeUnit{
		{ID: "../outside", Name: "Unsafe", StoryIDs: []string{"story-a"}},
		{ID: "story-b", Name: "Story B", StoryIDs: []string{"story-b"}},
	}

	err := validateManifestShape(manifest)

	if err == nil || !strings.Contains(err.Error(), `merge unit id "../outside"`) {
		t.Fatalf("validate error = %v", err)
	}
}

func TestExampleManifestMaterializesAndValidates(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifestPath, []byte(ExampleManifestYAML()), 0o644); err != nil {
		t.Fatal(err)
	}
	materialized, err := Materialize(MaterializeOptions{ManifestPath: manifestPath, OutRoot: root})
	if err != nil {
		t.Fatalf("Materialize example: %v", err)
	}
	if _, err := Validate(ValidateOptions{PlanDir: materialized.PlanDir, WriteLock: true}); err != nil {
		t.Fatalf("Validate example: %v", err)
	}
}

func TestManifestSchemaExposesRequiredContract(t *testing.T) {
	b, err := json.Marshal(ManifestSchema())
	if err != nil {
		t.Fatalf("Marshal schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("Unmarshal schema: %v", err)
	}
	if schema["title"] != "feature.plan.yaml" {
		t.Fatalf("unexpected title: %+v", schema["title"])
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required missing: %+v", schema["required"])
	}
	for _, field := range []string{"schema_version", "id", "title", "epics"} {
		if !containsAny(required, field) {
			t.Fatalf("required field %s missing from %+v", field, required)
		}
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("$defs missing: %+v", schema["$defs"])
	}
	for _, def := range []string{"epic", "feature", "story", "merge_unit"} {
		if _, ok := defs[def]; !ok {
			t.Fatalf("definition %s missing from %+v", def, defs)
		}
	}
	mergeUnit := defs["merge_unit"].(map[string]any)
	mergeUnitProps := mergeUnit["properties"].(map[string]any)
	mergeUnitID := mergeUnitProps["id"].(map[string]any)
	if mergeUnitID["pattern"] != safeIDPattern {
		t.Fatalf("merge unit id pattern = %+v", mergeUnitID["pattern"])
	}
}

func containsAny(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sampleManifest() string {
	return `schema_version: 1
id: sample
title: Sample Feature Plan
output_name: sample-feature-plan
base_ref: main
remote: origin
merge_policy:
  require_passing_checks: true
epics:
  - id: epic-foundation
    number: 1
    name: Foundation
    summary: Build the install contract.
    features:
      - id: feature-installer
        number: 1
        name: Installer
        stories:
          - id: story-install-contract
            number: 1
            name: Install Contract
            summary: Implement delegated installer contract.
            acceptance:
              - install JSON includes hashes
            implementation:
              - Build delegated installer output from staged files.
            testing:
              - Validate staged install JSON includes hashes for installed files.
merge_units:
  - id: story-install-contract
    name: Install Contract
    story_ids:
      - story-install-contract
`
}

func minimalValidManifest() Manifest {
	return Manifest{
		SchemaVersion: 1,
		ID:            "sample",
		Title:         "Sample",
		Epics: []Epic{{
			ID:      "epic-a",
			Number:  1,
			Name:    "Epic A",
			Summary: "Epic summary.",
			Features: []Feature{{
				ID:      "feature-a",
				Number:  1,
				Name:    "Feature A",
				Summary: "Feature summary.",
				Stories: []Story{
					{
						ID:             "story-a",
						Number:         1,
						Name:           "Story A",
						Summary:        "Story summary.",
						Acceptance:     []string{"Acceptance."},
						Implementation: []string{"Implementation."},
						Testing:        []string{"Testing."},
					},
					{
						ID:             "story-b",
						Number:         2,
						Name:           "Story B",
						Summary:        "Story summary.",
						Acceptance:     []string{"Acceptance."},
						Implementation: []string{"Implementation."},
						Testing:        []string{"Testing."},
					},
				},
			}},
		}},
	}
}
