package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

const testGeneratorVersion = "materialization-test/v2"

var _ workspace.FilesystemPort = (*workspace.RootedFilesystemAdapter)(nil)

func TestRootedFilesystemAdapterRejectsCrossRootAndSymlinkTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "safe.txt"), []byte("safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "outside.txt"), []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	adapter, err := workspace.OpenRootedFilesystemAdapter(root)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	safe, err := workspace.NewRootedPath(root, "safe.txt")
	if err != nil {
		t.Fatal(err)
	}
	if content, err := adapter.ReadFile(context.Background(), safe); err != nil || string(content) != "safe\n" {
		t.Fatalf("safe read content=%q err=%v", content, err)
	}
	linked, err := workspace.NewRootedPath(root, "link/outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ReadFile(context.Background(), linked); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink traversal error = %v", err)
	}
	other, err := workspace.NewRootedPath(external, "outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ReadFile(context.Background(), other); err == nil || !strings.Contains(err.Error(), "different filesystem root") {
		t.Fatalf("cross-root read error = %v", err)
	}
}

func TestMaterializationBootstrapsAbsentOrEmptyDestinationWithV2Inventory(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plan")
			if existing {
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			artifacts := materializationArtifacts(t,
				artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
				artifactFixture{id: "story/story-a", path: "001-epic/001-feature/001-story-a.md", content: "story\n"},
			)

			result, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{})
			if err != nil {
				t.Fatalf("SynchronizeMaterialization: %v", err)
			}
			if got, want := result.Created(), []string{"001-epic/001-feature/001-story-a.md", "feature.plan.yaml"}; !slices.Equal(got, want) {
				t.Fatalf("created = %v, want %v", got, want)
			}
			if result.Inventory().SchemaVersion() != 2 || result.Inventory().GeneratorVersion() != testGeneratorVersion {
				t.Fatalf("inventory = %#v", result.Inventory())
			}
			assertFileContent(t, filepath.Join(root, "feature.plan.yaml"), "manifest\n")
			assertFileContent(t, filepath.Join(root, "001-epic", "001-feature", "001-story-a.md"), "story\n")

			inventory := readInventoryFixture(t, root)
			if inventory.SchemaVersion != 2 || inventory.GeneratorVersion != testGeneratorVersion {
				t.Fatalf("inventory wire = %#v", inventory)
			}
			if got, want := inventory.Artifacts[0].ArtifactID, "manifest/sample"; got != want {
				t.Fatalf("first artifact id = %q, want %q", got, want)
			}
			if inventory.Artifacts[0].Path != "feature.plan.yaml" ||
				inventory.Artifacts[0].LastGeneratedHash != workspace.DigestBytes([]byte("manifest\n")).String() {
				t.Fatalf("manifest inventory entry = %#v", inventory.Artifacts[0])
			}
			for _, control := range []string{
				workspace.MaterializationInventoryFileName,
				workspace.MaterializationStateFileName,
			} {
				if strings.HasPrefix(filepath.Base(control), ".") {
					t.Fatalf("control path %s is hidden", control)
				}
				if _, err := os.Stat(filepath.Join(root, control)); err != nil {
					t.Fatalf("expected control path %s: %v", control, err)
				}
			}
		})
	}
}

func TestMaterializationRejectsNonemptyUnownedDestinationWithoutWritingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plan")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(candidate, []byte("user-owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "generated\n"})

	_, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{})
	conflicts := requireMaterializationConflicts(t, err)
	if len(conflicts) != 1 || conflicts[0].Kind() != workspace.MaterializationConflictUnownedDestination {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	assertFileContent(t, candidate, "user-owned\n")
	for _, control := range []string{workspace.MaterializationStateFileName, workspace.MaterializationInventoryFileName} {
		if _, statErr := os.Stat(filepath.Join(root, control)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("control path %s was created: %v", control, statErr)
		}
	}
}

func TestMaterializationNeverClaimsAByteMatchingUnownedCandidate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plan")
	initial := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, "story.md")
	if err := os.WriteFile(candidate, []byte("generated story\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/a", path: "story.md", content: "generated story\n"},
	)

	conflicts := requireMaterializationConflicts(t, synchronize(root, desired))
	if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictUnownedPath, "story.md") {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	assertFileContent(t, candidate, "generated story\n")
	if got := len(readInventoryFixture(t, root).Artifacts); got != 1 {
		t.Fatalf("inventory claimed unowned candidate: %d artifacts", got)
	}
}

func TestMaterializationUpdatesAndDeletesOnlyHashMatchedOwnedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plan")
	initial := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "v1\n"},
		artifactFixture{id: "story/stale", path: "old/stale.md", content: "stale\n"},
	)
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "v2\n"})

	result, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !slices.Equal(result.Updated(), []string{"feature.plan.yaml"}) || !slices.Equal(result.Deleted(), []string{"old/stale.md"}) {
		t.Fatalf("result updated=%v deleted=%v", result.Updated(), result.Deleted())
	}
	assertFileContent(t, filepath.Join(root, "feature.plan.yaml"), "v2\n")
	if _, err := os.Stat(filepath.Join(root, "old", "stale.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty proven-owned directory remains: %v", err)
	}
}

func TestMaterializationPreservesModifiedOrMissingOwnedArtifacts(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(t *testing.T, root string)
		desired     []artifactFixture
		wantKind    workspace.MaterializationConflictKind
		wantPath    string
		wantContent string
	}{
		{
			name: "modified target",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "owned.md"), []byte("user edit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			desired:  []artifactFixture{{id: "story/owned", path: "owned.md", content: "generated v2\n"}},
			wantKind: workspace.MaterializationConflictModifiedOwnedPath,
			wantPath: "owned.md", wantContent: "user edit\n",
		},
		{
			name: "modified stale",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "owned.md"), []byte("user edit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			desired:  []artifactFixture{{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"}},
			wantKind: workspace.MaterializationConflictModifiedOwnedPath,
			wantPath: "owned.md", wantContent: "user edit\n",
		},
		{
			name: "missing target",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, "owned.md")); err != nil {
					t.Fatal(err)
				}
			},
			desired:  []artifactFixture{{id: "story/owned", path: "owned.md", content: "generated v1\n"}},
			wantKind: workspace.MaterializationConflictMissingOwnedPath,
			wantPath: "owned.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plan")
			initial := materializationArtifacts(t,
				artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
				artifactFixture{id: "story/owned", path: "owned.md", content: "generated v1\n"},
			)
			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
				t.Fatal(err)
			}
			inventoryBefore, err := os.ReadFile(filepath.Join(root, workspace.MaterializationInventoryFileName))
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, root)

			_, err = workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, materializationArtifacts(t, tt.desired...), workspace.MaterializationOptions{},
			)
			conflicts := requireMaterializationConflicts(t, err)
			if !hasMaterializationConflict(conflicts, tt.wantKind, tt.wantPath) {
				t.Fatalf("conflicts = %#v, want %s at %s", conflicts, tt.wantKind, tt.wantPath)
			}
			inventoryAfter, readErr := os.ReadFile(filepath.Join(root, workspace.MaterializationInventoryFileName))
			if readErr != nil || string(inventoryAfter) != string(inventoryBefore) {
				t.Fatalf("inventory changed after conflict: err=%v", readErr)
			}
			if tt.wantContent != "" {
				assertFileContent(t, filepath.Join(root, tt.wantPath), tt.wantContent)
			} else if _, statErr := os.Stat(filepath.Join(root, tt.wantPath)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("missing owned file was recreated: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, workspace.MaterializationPendingFileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("conflicting comparison wrote pending state: %v", statErr)
			}
		})
	}
}

func TestMaterializationTreatsLaterMissingOrCorruptInventoryAsCorruption(t *testing.T) {
	for _, mutate := range []struct {
		name  string
		apply func(t *testing.T, root string)
	}{
		{
			name: "missing",
			apply: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, workspace.MaterializationInventoryFileName)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt",
			apply: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, workspace.MaterializationInventoryFileName), []byte("{not-json\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plan")
			artifacts := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{}); err != nil {
				t.Fatal(err)
			}
			mutate.apply(t, root)

			_, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{})
			var corruption workspace.MaterializationCorruptionError
			if !errors.As(err, &corruption) {
				t.Fatalf("error = %T %v, want MaterializationCorruptionError", err, err)
			}
			assertFileContent(t, filepath.Join(root, "feature.plan.yaml"), "manifest\n")
		})
	}
}

func TestMaterializationRejectsSymlinkTraversalAndPathAliases(t *testing.T) {
	t.Run("symlink traversal", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "plan")
		base := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
		if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, base, workspace.MaterializationOptions{}); err != nil {
			t.Fatal(err)
		}
		external := t.TempDir()
		if err := os.Symlink(external, filepath.Join(root, "docs")); err != nil {
			t.Fatal(err)
		}
		desired := materializationArtifacts(t,
			artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
			artifactFixture{id: "story/a", path: "docs/story.md", content: "story\n"},
		)

		conflicts := requireMaterializationConflicts(t, synchronize(root, desired))
		if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictUnsafePath, "docs") {
			t.Fatalf("conflicts = %#v", conflicts)
		}
		if _, err := os.Stat(filepath.Join(external, "story.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("symlink target was written: %v", err)
		}
	})

	t.Run("existing case alias", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "plan")
		base := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
		if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, base, workspace.MaterializationOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "Docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		desired := materializationArtifacts(t,
			artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
			artifactFixture{id: "story/a", path: "docs/story.md", content: "story\n"},
		)

		conflicts := requireMaterializationConflicts(t, synchronize(root, desired))
		if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictUnsafePath, "docs") {
			t.Fatalf("conflicts = %#v", conflicts)
		}
	})

	t.Run("desired aliases and prefixes", func(t *testing.T) {
		caseAliases := materializationArtifacts(t,
			artifactFixture{id: "story/a", path: "Docs/a.md", content: "a"},
			artifactFixture{id: "story/b", path: "docs/b.md", content: "b"},
		)
		root := filepath.Join(t.TempDir(), "plan")
		if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, caseAliases, workspace.MaterializationOptions{}); err == nil || !strings.Contains(err.Error(), "directory spellings") {
			t.Fatalf("case alias error = %v", err)
		}
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid desired state created destination: %v", err)
		}

		prefixes := materializationArtifacts(t,
			artifactFixture{id: "story/a", path: "docs", content: "a"},
			artifactFixture{id: "story/middle", path: "docs-extra", content: "middle"},
			artifactFixture{id: "story/b", path: "docs/b.md", content: "b"},
		)
		if _, err := workspace.SynchronizeMaterialization(filepath.Join(t.TempDir(), "plan"), testGeneratorVersion, prefixes, workspace.MaterializationOptions{}); err == nil || !strings.Contains(err.Error(), "path-prefix") {
			t.Fatalf("prefix error = %v", err)
		}
	})

	t.Run("hidden and non-normalized Unicode", func(t *testing.T) {
		if _, err := workspace.NewMaterializationArtifact("story/hidden", ".git/config", []byte("a")); err == nil {
			t.Fatal("hidden materialization path was accepted")
		}
		if _, err := workspace.NewMaterializationArtifact("story/a", "Cafe\u0301.md", []byte("a")); err == nil {
			t.Fatal("non-NFC materialization path was accepted")
		}
		fullFoldAliases := materializationArtifacts(t,
			artifactFixture{id: "story/strasse", path: "Straße/a.md", content: "a"},
			artifactFixture{id: "story/ss", path: "STRASSE/b.md", content: "b"},
		)
		if _, err := workspace.SynchronizeMaterialization(filepath.Join(t.TempDir(), "plan"), testGeneratorVersion, fullFoldAliases, workspace.MaterializationOptions{}); err == nil || !strings.Contains(err.Error(), "directory spellings") {
			t.Fatalf("full Unicode case-fold alias error = %v", err)
		}
	})
}

func TestMaterializationRecoversAcrossStagedUpdateFaults(t *testing.T) {
	points := []workspace.MaterializationFaultPoint{
		workspace.MaterializationFaultAfterBootstrapState,
		workspace.MaterializationFaultAfterStaging,
		workspace.MaterializationFaultAfterPending,
		workspace.MaterializationFaultAfterDirectoryCreate,
		workspace.MaterializationFaultAfterArtifactWrite,
		workspace.MaterializationFaultAfterInventoryActivation,
		workspace.MaterializationFaultAfterStateActivation,
	}
	for _, point := range points {
		t.Run(string(point), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plan")
			artifacts := materializationArtifacts(t,
				artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
				artifactFixture{id: "story/a", path: "docs/nested/story.md", content: "story\n"},
			)
			failed := false
			fault := func(observed workspace.MaterializationFaultPoint) error {
				if !failed && observed == point {
					failed = true
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{FaultInjector: fault},
			); err == nil || !failed {
				t.Fatalf("fault %s did not interrupt materialization: %v", point, err)
			}

			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{}); err != nil {
				t.Fatalf("recover after %s: %v", point, err)
			}
			assertFileContent(t, filepath.Join(root, "feature.plan.yaml"), "manifest\n")
			assertFileContent(t, filepath.Join(root, "docs", "nested", "story.md"), "story\n")
			assertNoMaterializationTransaction(t, root)
			_ = readInventoryFixture(t, root)
		})
	}
}

func TestMaterializationRecoversStaleDeletionAndDirectoryCleanup(t *testing.T) {
	for _, point := range []workspace.MaterializationFaultPoint{
		workspace.MaterializationFaultAfterStaleDelete,
		workspace.MaterializationFaultAfterDirectoryCleanup,
	} {
		t.Run(string(point), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plan")
			initial := materializationArtifacts(t,
				artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
				artifactFixture{id: "story/stale", path: "old/nested/stale.md", content: "stale\n"},
			)
			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
				t.Fatal(err)
			}
			desired := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
			failed := false
			fault := func(observed workspace.MaterializationFaultPoint) error {
				if !failed && observed == point {
					failed = true
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, desired, workspace.MaterializationOptions{FaultInjector: fault},
			); err == nil || !failed {
				t.Fatalf("fault %s did not interrupt update: %v", point, err)
			}
			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{}); err != nil {
				t.Fatalf("recover after %s: %v", point, err)
			}
			if _, err := os.Stat(filepath.Join(root, "old")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale directory remains after recovery: %v", err)
			}
			assertNoMaterializationTransaction(t, root)
		})
	}
}

func TestMaterializationRejectsCorruptStagedBytesDuringRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plan")
	artifacts := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
	fault := func(point workspace.MaterializationFaultPoint) error {
		if point == workspace.MaterializationFaultAfterPending {
			return errors.New("simulated crash")
		}
		return nil
	}
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{FaultInjector: fault},
	); err == nil {
		t.Fatal("pending fault did not interrupt materialization")
	}
	stage := filepath.Join(root, workspace.MaterializationStagingDirectoryName, "artifact-000000.data")
	if err := os.WriteFile(stage, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{})
	var corruption workspace.MaterializationCorruptionError
	if !errors.As(err, &corruption) {
		t.Fatalf("error = %T %v, want corruption", err, err)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.plan.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt stage was activated: %v", err)
	}
}

func TestMaterializationRecoveryRechecksOwnedBytesBeforeOverwriteOrDelete(t *testing.T) {
	for _, test := range []struct {
		name       string
		desired    []artifactFixture
		mutatePath string
	}{
		{
			name: "overwrite",
			desired: []artifactFixture{
				{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest v2\n"},
				{id: "story/stale", path: "stale.md", content: "stale\n"},
			},
			mutatePath: "feature.plan.yaml",
		},
		{
			name: "delete",
			desired: []artifactFixture{
				{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest v1\n"},
			},
			mutatePath: "stale.md",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plan")
			initial := materializationArtifacts(t,
				artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest v1\n"},
				artifactFixture{id: "story/stale", path: "stale.md", content: "stale\n"},
			)
			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
				t.Fatal(err)
			}
			desired := materializationArtifacts(t, test.desired...)
			fault := func(point workspace.MaterializationFaultPoint) error {
				if point == workspace.MaterializationFaultAfterPending {
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, desired, workspace.MaterializationOptions{FaultInjector: fault},
			); err == nil {
				t.Fatal("pending fault did not interrupt materialization")
			}
			mutated := filepath.Join(root, filepath.FromSlash(test.mutatePath))
			if err := os.WriteFile(mutated, []byte("user edit after comparison\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{})
			conflicts := requireMaterializationConflicts(t, err)
			if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictModifiedOwnedPath, test.mutatePath) {
				t.Fatalf("conflicts = %#v", conflicts)
			}
			assertFileContent(t, mutated, "user edit after comparison\n")
			if _, err := os.Stat(filepath.Join(root, workspace.MaterializationPendingFileName)); err != nil {
				t.Fatalf("pending recovery evidence was removed after conflict: %v", err)
			}
		})
	}
}

func TestMaterializationRemovesOnlyEmptyProvenOwnedDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plan")
	initial := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/stale", path: "old/nested/stale.md", content: "stale\n"},
	)
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old", "keep.txt"), []byte("unowned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(root, "old", "keep.txt"), "unowned\n")
	if _, err := os.Stat(filepath.Join(root, "old", "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty owned nested directory remains: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "old", "keep.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "old"))
	if err != nil || !info.IsDir() {
		t.Fatalf("formerly owned directory was removed after ownership was dropped: %v", err)
	}
}

type artifactFixture struct {
	id      string
	path    string
	content string
}

func materializationArtifacts(t *testing.T, fixtures ...artifactFixture) []workspace.MaterializationArtifact {
	t.Helper()
	artifacts := make([]workspace.MaterializationArtifact, 0, len(fixtures))
	for _, fixture := range fixtures {
		artifact, err := workspace.NewMaterializationArtifact(fixture.id, fixture.path, []byte(fixture.content))
		if err != nil {
			t.Fatalf("NewMaterializationArtifact(%s): %v", fixture.path, err)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

func synchronize(root string, artifacts []workspace.MaterializationArtifact) error {
	_, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{})
	return err
}

func requireMaterializationConflicts(t *testing.T, err error) []workspace.MaterializationConflict {
	t.Helper()
	var conflictError workspace.MaterializationConflictError
	if !errors.As(err, &conflictError) {
		t.Fatalf("error = %T %v, want MaterializationConflictError", err, err)
	}
	return conflictError.Conflicts()
}

func hasMaterializationConflict(
	conflicts []workspace.MaterializationConflict,
	kind workspace.MaterializationConflictKind,
	path string,
) bool {
	for _, conflict := range conflicts {
		if conflict.Kind() == kind && conflict.Path() == path {
			return true
		}
	}
	return false
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("%s = %q, want %q", path, content, want)
	}
}

type inventoryFixture struct {
	SchemaVersion    int    `json:"schema_version"`
	GeneratorVersion string `json:"generator_version"`
	Artifacts        []struct {
		ArtifactID        string `json:"artifact_id"`
		Path              string `json:"path"`
		LastGeneratedHash string `json:"last_generated_hash"`
	} `json:"artifacts"`
	Directories []string `json:"directories"`
}

func readInventoryFixture(t *testing.T, root string) inventoryFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, workspace.MaterializationInventoryFileName))
	if err != nil {
		t.Fatal(err)
	}
	var inventory inventoryFixture
	if err := json.Unmarshal(content, &inventory); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func assertNoMaterializationTransaction(t *testing.T, root string) {
	t.Helper()
	for _, relative := range []string{
		workspace.MaterializationPendingFileName,
		workspace.MaterializationStagingDirectoryName,
	} {
		if _, err := os.Stat(filepath.Join(root, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction path %s remains: %v", relative, err)
		}
	}
}
