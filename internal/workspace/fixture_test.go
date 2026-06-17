package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/plan"
	"gopkg.in/yaml.v3"
)

const fixtureWorkspaceBaseRef = "workspace-orchestration"

type workspaceFixture struct {
	Dir      string
	Plans    map[string]string
	Manifest WorkspaceManifest
}

type workspaceFixtureSpec struct {
	ID           string
	BaseRef      string
	Plans        []workspaceFixturePlan
	Dependencies []WorkspaceDependency
}

type workspaceFixturePlan struct {
	ID           string
	StoryID      string
	Dependencies []string
}

func TestWorkspaceFixtureBuildsOnePlanWorkspace(t *testing.T) {
	fixture := newOnePlanWorkspaceFixture(t)

	if fixture.Manifest.BaseRef != fixtureWorkspaceBaseRef {
		t.Fatalf("base_ref = %q", fixture.Manifest.BaseRef)
	}
	if len(fixture.Manifest.Plans) != 1 || fixture.Plans["foundation"] == "" {
		t.Fatalf("plans = %+v, dirs = %+v", fixture.Manifest.Plans, fixture.Plans)
	}
	if _, err := os.Stat(filepath.Join(fixture.Plans["foundation"], planLockFileName)); err != nil {
		t.Fatalf("fixture plan lock missing: %v", err)
	}
	result, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate fixture workspace: %v", err)
	}
	if len(result.Lock.Plans) != 1 || len(result.Lock.MergeUnits) != 1 {
		t.Fatalf("workspace lock = %+v", result.Lock)
	}
	if result.Lock.MergeUnits[0].ID != "foundation:story-a" {
		t.Fatalf("merge unit = %+v", result.Lock.MergeUnits[0])
	}
}

func TestWorkspaceFixtureBuildsMultiPlanWorkspace(t *testing.T) {
	fixture := newMultiPlanWorkspaceFixture(t)

	result, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate fixture workspace: %v", err)
	}
	if len(result.Lock.Plans) != 2 || len(result.Lock.MergeUnits) != 2 {
		t.Fatalf("workspace lock = %+v", result.Lock)
	}
	if got := result.Lock.MergeUnits[1].Dependencies; len(got) != 1 || got[0] != "foundation:story-a" {
		t.Fatalf("multi-plan dependency = %+v", got)
	}
}

func newOnePlanWorkspaceFixture(t *testing.T) workspaceFixture {
	t.Helper()
	return newWorkspaceFixture(t, workspaceFixtureSpec{
		ID:      "workspace-a",
		BaseRef: fixtureWorkspaceBaseRef,
		Plans: []workspaceFixturePlan{{
			ID:      "foundation",
			StoryID: "story-a",
		}},
	})
}

func newMultiPlanWorkspaceFixture(t *testing.T) workspaceFixture {
	t.Helper()
	return newWorkspaceFixture(t, workspaceFixtureSpec{
		ID:      "workspace-a",
		BaseRef: fixtureWorkspaceBaseRef,
		Plans: []workspaceFixturePlan{
			{ID: "foundation", StoryID: "story-a"},
			{ID: "sources", StoryID: "story-b"},
		},
		Dependencies: []WorkspaceDependency{
			{Before: "foundation:story-a", After: "sources:story-b"},
		},
	})
}

func newWorkspaceFixture(t *testing.T, spec workspaceFixtureSpec) workspaceFixture {
	t.Helper()
	if spec.ID == "" {
		spec.ID = "workspace-a"
	}
	if spec.BaseRef == "" {
		spec.BaseRef = fixtureWorkspaceBaseRef
	}
	if len(spec.Plans) == 0 {
		spec.Plans = []workspaceFixturePlan{{ID: "foundation", StoryID: "story-a"}}
	}

	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	fixture := workspaceFixture{
		Dir:   workspaceDir,
		Plans: map[string]string{},
		Manifest: WorkspaceManifest{
			SchemaVersion: manifestSchemaVersion,
			ID:            spec.ID,
			Repo:          ".",
			BaseRef:       spec.BaseRef,
			Remote:        "origin",
			Dependencies:  spec.Dependencies,
		},
	}
	for _, planSpec := range spec.Plans {
		planDir := materializeFixturePlan(t, workspaceDir, spec.BaseRef, planSpec)
		fixture.Plans[planSpec.ID] = planDir
		fixture.Manifest.Plans = append(fixture.Manifest.Plans, WorkspacePlanRef{
			ID:   planSpec.ID,
			Path: filepath.ToSlash(filepath.Join("plans", planSpec.ID)),
		})
	}
	writeWorkspaceManifest(t, workspaceDir, fixture.Manifest)
	return fixture
}

func materializeFixturePlan(t *testing.T, workspaceDir string, baseRef string, spec workspaceFixturePlan) string {
	t.Helper()
	if spec.ID == "" {
		t.Fatal("fixture plan id is required")
	}
	if spec.StoryID == "" {
		spec.StoryID = "story-a"
	}
	manifest := plan.Manifest{
		SchemaVersion: 1,
		ID:            spec.ID,
		Title:         fixtureTitle(spec.ID),
		OutputName:    spec.ID,
		BaseRef:       baseRef,
		Remote:        "origin",
		Epics: []plan.Epic{{
			ID:      "epic-" + spec.ID,
			Number:  1,
			Name:    fixtureTitle(spec.ID) + " Epic",
			Summary: "Fixture epic.",
			Features: []plan.Feature{{
				ID:      "feature-" + spec.ID,
				Number:  1,
				Name:    fixtureTitle(spec.ID) + " Feature",
				Summary: "Fixture feature.",
				Stories: []plan.Story{{
					ID:             spec.StoryID,
					Number:         1,
					Name:           fixtureTitle(spec.StoryID),
					Summary:        "Fixture story.",
					Acceptance:     []string{"Acceptance."},
					Implementation: []string{"Implementation."},
					Testing:        []string{"Testing."},
					Dependencies:   spec.Dependencies,
				}},
			}},
		}},
		MergeUnits: []plan.MergeUnit{{
			ID:       spec.StoryID,
			Name:     fixtureTitle(spec.StoryID),
			StoryIDs: []string{spec.StoryID},
		}},
	}

	manifestPath := filepath.Join(workspaceDir, spec.ID+".feature.plan.yaml")
	writeYAMLFile(t, manifestPath, manifest)
	materialized, err := plan.Materialize(plan.MaterializeOptions{
		ManifestPath: manifestPath,
		OutRoot:      filepath.Join(workspaceDir, "plans"),
	})
	if err != nil {
		t.Fatalf("Materialize fixture plan %s: %v", spec.ID, err)
	}
	if _, err := plan.Validate(plan.ValidateOptions{PlanDir: materialized.PlanDir, WriteLock: true}); err != nil {
		t.Fatalf("Validate fixture plan %s: %v", spec.ID, err)
	}
	return materialized.PlanDir
}

func writeWorkspaceManifest(t *testing.T, workspaceDir string, manifest WorkspaceManifest) {
	t.Helper()
	writeYAMLFile(t, filepath.Join(workspaceDir, ManifestFileName), manifest)
}

func readFixturePlanLock(t *testing.T, planDir string) plan.Lock {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(planDir, planLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	var lock plan.Lock
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	return lock
}

func writeFixturePlanLock(t *testing.T, planDir string, lock plan.Lock) {
	t.Helper()
	if err := writeStableJSON(filepath.Join(planDir, planLockFileName), lock); err != nil {
		t.Fatal(err)
	}
}

func writeCompactFixturePlanLock(t *testing.T, planDir string, lock plan.Lock) {
	t.Helper()
	b, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, planLockFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeYAMLFile(t *testing.T, path string, value any) {
	t.Helper()
	b, err := yaml.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func fixtureTitle(id string) string {
	return fmt.Sprintf("Fixture %s", id)
}
