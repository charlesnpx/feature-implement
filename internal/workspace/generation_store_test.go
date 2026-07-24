package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestGenerationStoreDetectsCanonicalTampering(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	workspaceDir := t.TempDir()
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stored, err := store.Store(definition)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Store(definition)
	if err != nil ||
		!strings.EqualFold(
			stored.Generation().String(),
			second.Generation().String(),
		) {
		t.Fatalf("idempotent store = %#v, %v", second, err)
	}
	entries, err := os.ReadDir(
		workspace.WorkspaceGenerationsDirectory(workspaceDir),
	)
	if err != nil || len(entries) != 1 {
		t.Fatalf("generation entries = %v, %v", entries, err)
	}
	path := filepath.Join(
		workspace.WorkspaceGenerationsDirectory(workspaceDir),
		entries[0].Name(),
	)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)/2] ^= 1
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(definition.Generation()); err == nil {
		t.Fatal("tampered generation loaded successfully")
	}
}

func TestGenerationStoreRequiresFreshRuntimeForAnotherGeneration(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	other := mustProspectiveCandidate(t, fixture)
	workspaceDir := t.TempDir()
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Store(definition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store(other); err == nil ||
		!strings.Contains(err.Error(), "requires a fresh runtime directory") {
		t.Fatalf("second generation store error = %v", err)
	}
	generations, err := store.List()
	if err != nil ||
		len(generations) != 1 ||
		generations[0] != definition.Generation() {
		t.Fatalf("stored generations = %v, %v", generations, err)
	}
}

func mustProspectiveCandidate(
	t *testing.T,
	fixture definitionFixture,
) workspace.EffectiveWorkspaceDefinition {
	t.Helper()
	sources := cloneDefinitionSources(fixture.sources)
	sources.Plans[0].Bytes = []byte(strings.Replace(
		string(sources.Plans[0].Bytes),
		"The dependent contract is explicit.",
		"The dependent contract is explicit and versioned.",
		1,
	))
	return mustDefinition(t, sources)
}

func mustMergeUnitReference(
	t *testing.T,
	planID string,
	mergeUnitID string,
) workspace.MergeUnitReference {
	t.Helper()
	reference, err := workspace.NewMergeUnitReference(
		workspace.MustID(planID),
		workspace.MustID(mergeUnitID),
	)
	if err != nil {
		t.Fatal(err)
	}
	return reference
}
