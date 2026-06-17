package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWritesDeterministicWorkspaceLock(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)

	first, err := Validate(ValidateOptions{WorkspaceDir: workspaceDir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate first: %v", err)
	}
	firstBytes, err := os.ReadFile(first.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Validate(ValidateOptions{WorkspaceDir: workspaceDir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate second: %v", err)
	}
	secondBytes, err := os.ReadFile(second.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("lock output is not deterministic:\nfirst=%s\nsecond=%s", firstBytes, secondBytes)
	}
	if first.Lock.WorkspaceID != "workspace-a" || first.Lock.BaseRef != "workspace-orchestration" || first.Lock.Remote != "origin" {
		t.Fatalf("lock metadata = %+v", first.Lock)
	}
	if len(first.Lock.Plans) != 2 {
		t.Fatalf("plans = %+v", first.Lock.Plans)
	}
	if first.Lock.Plans[0].ID != "foundation" || first.Lock.Plans[0].LockHash == "" {
		t.Fatalf("first plan lock = %+v", first.Lock.Plans[0])
	}
	if second.LockPath != first.LockPath {
		t.Fatalf("lock path changed: %q != %q", second.LockPath, first.LockPath)
	}
}

func TestValidateDoesNotWriteLockWithoutFlag(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)

	result, err := Validate(ValidateOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.LockPath != "" {
		t.Fatalf("lock path should be empty without write flag: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, LockFileName)); !os.IsNotExist(err) {
		t.Fatalf("lock should not be written without flag: %v", err)
	}
}

func TestValidateReportsMissingPlanLock(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if err := os.Remove(filepath.Join(workspaceDir, "plans", "sources", planLockFileName)); err != nil {
		t.Fatal(err)
	}

	_, err := Validate(ValidateOptions{WorkspaceDir: workspaceDir})

	if err == nil || !strings.Contains(err.Error(), "plan sources lock") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestBuildLockHashesCanonicalPlanLockJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	compact, err := BuildLock(workspaceDir, mustReadWorkspaceManifest(t, workspaceDir))
	if err != nil {
		t.Fatalf("BuildLock compact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "plans", "foundation", planLockFileName), []byte(`{
  "manifest_id": "foundation",
  "schema_version": 1
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	formatted, err := BuildLock(workspaceDir, mustReadWorkspaceManifest(t, workspaceDir))
	if err != nil {
		t.Fatalf("BuildLock formatted: %v", err)
	}
	if compact.Plans[0].LockHash != formatted.Plans[0].LockHash {
		t.Fatalf("canonical hash changed: %s != %s", compact.Plans[0].LockHash, formatted.Plans[0].LockHash)
	}
}

func workspaceWithPlanLocks(t *testing.T) string {
	t.Helper()
	workspaceDir := t.TempDir()
	for _, planID := range []string{"foundation", "sources"} {
		planDir := filepath.Join(workspaceDir, "plans", planID)
		if err := os.MkdirAll(planDir, 0o755); err != nil {
			t.Fatal(err)
		}
		lock := `{"schema_version":1,"manifest_id":"` + planID + `"}`
		if err := os.WriteFile(filepath.Join(planDir, planLockFileName), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `schema_version: 1
id: workspace-a
repo: .
base_ref: workspace-orchestration
remote: origin
plans:
  - id: foundation
    path: plans/foundation
  - id: sources
    path: plans/sources
dependencies:
  - before: foundation:story-a
    after: sources:story-b
`
	if err := os.WriteFile(filepath.Join(workspaceDir, ManifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspaceDir
}

func mustReadWorkspaceManifest(t *testing.T, workspaceDir string) WorkspaceManifest {
	t.Helper()
	manifest, err := ReadManifest(filepath.Join(workspaceDir, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}
