package plan

import (
	"os"
	"path/filepath"
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
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("expected story file: %v", err)
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
	if _, err := Implement(ImplementOptions{PlanDir: materialized.PlanDir, Action: "push"}); err == nil {
		t.Fatalf("push should require explicit flag")
	}
	result, err := Implement(ImplementOptions{PlanDir: materialized.PlanDir, Action: "push", AllowPush: true})
	if err != nil {
		t.Fatalf("push with flag: %v", err)
	}
	if result.Status != "planned" {
		t.Fatalf("result = %+v", result)
	}
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
merge_units:
  - id: story-install-contract
    name: Install Contract
    story_ids:
      - story-install-contract
`
}
