package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/plan"
	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestWorkspaceV2StrictnessDoesNotChangeUnrelatedV1Planning(t *testing.T) {
	t.Parallel()

	root := canonicalMaterializationTestTempDir(t)
	manifest := `schema_version: 1
id: legacy-planning
title: Legacy Planning Still Works
output_name: legacy-planning
base_ref: main
remote: origin
epics:
  - id: epic-one
    number: 1
    name: Foundation
    features:
      - id: feature-one
        number: 1
        name: Contracts
        stories:
          - id: story-one
            number: 1
            name: Preserve Planning
            summary: Preserve version-one planning behavior.
            acceptance:
              - Version-one planning remains valid.
            implementation:
              - Keep workspace-v2 decoding isolated.
            testing:
              - Validate this version-one plan.
merge_units:
  - id: unit-one
    name: Unit One
    story_ids:
      - story-one
`
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	materialized, err := plan.Materialize(plan.MaterializeOptions{ManifestPath: manifestPath, OutRoot: root})
	if err != nil {
		t.Fatalf("v1 materialize: %v", err)
	}
	result, err := plan.Validate(plan.ValidateOptions{PlanDir: materialized.PlanDir})
	if err != nil || result.Status != "valid" {
		t.Fatalf("v1 validate = %#v, %v", result, err)
	}
	if _, err := workspace.DecodePlan([]byte(manifest)); err == nil || !strings.Contains(err.Error(), "v2 is required") {
		t.Fatalf("workspace v2 decoder accepted v1 artifact: %v", err)
	}
}
