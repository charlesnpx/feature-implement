package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWritesDeterministicWorkspaceLock(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	fixture.Manifest.ContractGates = []WorkspaceContractGate{{
		ID:        "api-contract",
		Producers: []string{"foundation:story-a"},
		Consumers: []string{"sources:story-b"},
		Artifacts: []WorkspaceArtifactSpec{{
			ID:   "openapi",
			Path: "./contracts/../contracts/openapi.yaml",
		}},
		Validation: WorkspaceGateValidation{
			Commands: []string{"go test ./...", "feature workspace contract verify api-contract"},
		},
	}}
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)

	first, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate first: %v", err)
	}
	firstBytes, err := os.ReadFile(first.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true})
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
	if len(first.Lock.MergeUnits) != 2 {
		t.Fatalf("merge units = %+v", first.Lock.MergeUnits)
	}
	if first.Lock.MergeUnits[0].ID != "foundation:story-a" || first.Lock.MergeUnits[1].ID != "sources:story-b" {
		t.Fatalf("merge units not sorted by global id: %+v", first.Lock.MergeUnits)
	}
	if got := first.Lock.MergeUnits[1].Dependencies; len(got) != 1 || got[0] != "foundation:story-a" {
		t.Fatalf("sources dependencies = %+v", got)
	}
	if len(first.Lock.ContractGates) != 1 {
		t.Fatalf("contract gates = %+v", first.Lock.ContractGates)
	}
	gate := first.Lock.ContractGates[0]
	if gate.ID != "api-contract" {
		t.Fatalf("contract gate id = %q", gate.ID)
	}
	if len(gate.Producers) != 1 || gate.Producers[0] != "foundation:story-a" {
		t.Fatalf("contract gate producers = %+v", gate.Producers)
	}
	if len(gate.Consumers) != 1 || gate.Consumers[0] != "sources:story-b" {
		t.Fatalf("contract gate consumers = %+v", gate.Consumers)
	}
	if len(gate.Artifacts) != 1 || gate.Artifacts[0].ID != "openapi" || gate.Artifacts[0].Path != "contracts/openapi.yaml" {
		t.Fatalf("contract gate artifacts = %+v", gate.Artifacts)
	}
	if len(gate.ValidationCommands) != 2 || gate.ValidationCommands[0] != "go test ./..." || gate.ValidationCommands[1] != "feature workspace contract verify api-contract" {
		t.Fatalf("contract gate validation commands = %+v", gate.ValidationCommands)
	}
	if second.LockPath != first.LockPath {
		t.Fatalf("lock path changed: %q != %q", second.LockPath, first.LockPath)
	}
}

func TestValidateDoesNotWriteLockWithoutFlag(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)

	result, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.LockPath != "" {
		t.Fatalf("lock path should be empty without write flag: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.Dir, LockFileName)); !os.IsNotExist(err) {
		t.Fatalf("lock should not be written without flag: %v", err)
	}
}

func TestValidateReportsMissingPlanLock(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	if err := os.Remove(filepath.Join(fixture.Plans["sources"], planLockFileName)); err != nil {
		t.Fatal(err)
	}

	_, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir})

	if err == nil || !strings.Contains(err.Error(), "plan sources lock") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestValidateRejectsUnknownWorkspaceDependencyEndpoint(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	fixture.Manifest.Dependencies = []WorkspaceDependency{
		{Before: "foundation:story-a", After: "sources:missing"},
	}
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)

	_, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir})

	if err == nil || !strings.Contains(err.Error(), "dependency 1 after: unknown merge unit sources:missing") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestValidateRejectsUnknownContractGateProducer(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	fixture.Manifest.ContractGates = []WorkspaceContractGate{validContractGateForFixture()}
	fixture.Manifest.ContractGates[0].Producers = []string{"foundation:missing"}
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)

	_, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir})

	if err == nil || !strings.Contains(err.Error(), "contract gate api-contract: producer 1: unknown merge unit foundation:missing") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestValidateRejectsUnknownContractGateConsumer(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	fixture.Manifest.ContractGates = []WorkspaceContractGate{validContractGateForFixture()}
	fixture.Manifest.ContractGates[0].Consumers = []string{"sources:missing"}
	writeWorkspaceManifest(t, fixture.Dir, fixture.Manifest)

	_, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir})

	if err == nil || !strings.Contains(err.Error(), "contract gate api-contract: consumer 1: unknown merge unit sources:missing") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestBuildLockRejectsDuplicateGlobalMergeUnitIDs(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	lock := readFixturePlanLock(t, fixture.Plans["foundation"])
	lock.MergeUnits = append(lock.MergeUnits, lock.MergeUnits[0])
	writeFixturePlanLock(t, fixture.Plans["foundation"], lock)

	_, err := BuildLock(fixture.Dir, mustReadWorkspaceManifest(t, fixture.Dir))

	if err == nil || !strings.Contains(err.Error(), "duplicate global merge unit id foundation:story-a") {
		t.Fatalf("BuildLock error = %v", err)
	}
}

func TestBuildLockHashesCanonicalPlanLockJSON(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)
	lock := readFixturePlanLock(t, fixture.Plans["foundation"])
	writeCompactFixturePlanLock(t, fixture.Plans["foundation"], lock)
	compact, err := BuildLock(fixture.Dir, mustReadWorkspaceManifest(t, fixture.Dir))
	if err != nil {
		t.Fatalf("BuildLock compact: %v", err)
	}
	writeFixturePlanLock(t, fixture.Plans["foundation"], lock)
	formatted, err := BuildLock(fixture.Dir, mustReadWorkspaceManifest(t, fixture.Dir))
	if err != nil {
		t.Fatalf("BuildLock formatted: %v", err)
	}
	if compact.Plans[0].LockHash != formatted.Plans[0].LockHash {
		t.Fatalf("canonical hash changed: %s != %s", compact.Plans[0].LockHash, formatted.Plans[0].LockHash)
	}
}

func mustReadWorkspaceManifest(t *testing.T, workspaceDir string) WorkspaceManifest {
	t.Helper()
	manifest, err := ReadManifest(filepath.Join(workspaceDir, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func validContractGateForFixture() WorkspaceContractGate {
	return WorkspaceContractGate{
		ID:        "api-contract",
		Producers: []string{"foundation:story-a"},
		Consumers: []string{"sources:story-b"},
		Artifacts: []WorkspaceArtifactSpec{{
			ID:   "openapi",
			Path: "contracts/openapi.yaml",
		}},
		Validation: WorkspaceGateValidation{
			Commands: []string{"go test ./..."},
		},
	}
}
