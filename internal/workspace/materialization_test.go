package workspace_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

const testGeneratorVersion = "materialization-test/v2"

func TestMaterializationRejectsSymlinkedDestinationRootsAndAncestors(t *testing.T) {
	t.Parallel()

	artifacts := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
	for _, test := range []struct {
		name string
		root func(t *testing.T, external string) string
	}{
		{
			name: "ancestor",
			root: func(t *testing.T, external string) string {
				t.Helper()
				alias := filepath.Join(canonicalMaterializationTestTempDir(t), "alias")
				if err := os.Symlink(external, alias); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(alias, "plan")
			},
		},
		{
			name: "final root",
			root: func(t *testing.T, external string) string {
				t.Helper()
				root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
				if err := os.Symlink(external, root); err != nil {
					t.Fatal(err)
				}
				return root
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			external := canonicalMaterializationTestTempDir(t)
			root := test.root(t, external)
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{},
			); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("symlinked destination error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(external, "feature.plan.yaml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("symlink target was materialized: %v", err)
			}
		})
	}
}

func TestMaterializationRejectsHiddenDestinationAncestors(t *testing.T) {
	t.Parallel()

	base := canonicalMaterializationTestTempDir(t)
	root := filepath.Join(base, ".hidden", "plan")
	artifacts := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{},
	); err == nil || !strings.Contains(err.Error(), "unsafe component") {
		t.Fatalf("hidden destination error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".hidden")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hidden destination ancestor was created: %v", err)
	}
}

func TestMaterializationBootstrapsAbsentOrEmptyDestination(t *testing.T) {
	t.Parallel()

	for _, existing := range []bool{false, true} {
		existing := existing
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
			for _, directory := range []string{"001-epic", "001-epic/001-feature"} {
				info, err := os.Stat(filepath.Join(root, filepath.FromSlash(directory)))
				if err != nil {
					t.Fatalf("stat generated directory %s: %v", directory, err)
				}
				if got := info.Mode().Perm(); got != 0o755 {
					t.Fatalf("generated directory %s mode = %#o, want 0755", directory, got)
				}
			}

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
	t.Parallel()

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
	t.Parallel()

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
	t.Parallel()

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
	t.Parallel()

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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
	t.Parallel()

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
		mutate := mutate
		t.Run(mutate.name, func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
	t.Parallel()

	t.Run("symlink traversal", func(t *testing.T) {
		t.Parallel()

		root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
		base := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
		if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, base, workspace.MaterializationOptions{}); err != nil {
			t.Fatal(err)
		}
		external := canonicalMaterializationTestTempDir(t)
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
		t.Parallel()

		requireFullSuite(t, "materialization path-alias permutation")

		root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
		t.Parallel()

		requireFullSuite(t, "materialization path-alias permutation")

		caseAliases := materializationArtifacts(t,
			artifactFixture{id: "story/a", path: "Docs/a.md", content: "a"},
			artifactFixture{id: "story/b", path: "docs/b.md", content: "b"},
		)
		root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
		if _, err := workspace.SynchronizeMaterialization(filepath.Join(canonicalMaterializationTestTempDir(t), "plan"), testGeneratorVersion, prefixes, workspace.MaterializationOptions{}); err == nil || !strings.Contains(err.Error(), "path-prefix") {
			t.Fatalf("prefix error = %v", err)
		}
	})

	t.Run("hidden and non-normalized Unicode", func(t *testing.T) {
		t.Parallel()

		requireFullSuite(t, "materialization path-alias permutation")

		if _, err := workspace.NewMaterializationArtifact("story/hidden", ".git/config", []byte("a")); err == nil {
			t.Fatal("hidden materialization path was accepted")
		}
		if _, err := workspace.NewMaterializationArtifact("story/a", "Cafe\u0301.md", []byte("a")); err == nil {
			t.Fatal("non-NFC materialization path was accepted")
		}
		if _, err := workspace.NewMaterializationArtifact(
			"story/reserved", "FEATURE.MATERIALIZATION.TXN-user.md", []byte("a"),
		); err == nil {
			t.Fatal("case alias of reserved transaction namespace was accepted")
		}
		fullFoldAliases := materializationArtifacts(t,
			artifactFixture{id: "story/strasse", path: "Straße/a.md", content: "a"},
			artifactFixture{id: "story/ss", path: "STRASSE/b.md", content: "b"},
		)
		if _, err := workspace.SynchronizeMaterialization(filepath.Join(canonicalMaterializationTestTempDir(t), "plan"), testGeneratorVersion, fullFoldAliases, workspace.MaterializationOptions{}); err == nil || !strings.Contains(err.Error(), "directory spellings") {
			t.Fatalf("full Unicode case-fold alias error = %v", err)
		}
	})
}

func TestMaterializationRecoversAcrossStagedUpdateFaults(t *testing.T) {
	t.Parallel()

	points := []workspace.MaterializationFaultPoint{
		workspace.MaterializationFaultAfterBootstrapState,
		workspace.MaterializationFaultAfterStaging,
		workspace.MaterializationFaultAfterPending,
		workspace.MaterializationFaultAfterTemporarySync,
		workspace.MaterializationFaultAfterDirectoryCreate,
		workspace.MaterializationFaultAfterArtifactWrite,
		workspace.MaterializationFaultAfterInventoryActivation,
		workspace.MaterializationFaultAfterStateActivation,
	}
	capturePoints := points
	if testing.Short() {
		capturePoints = []workspace.MaterializationFaultPoint{
			workspace.MaterializationFaultAfterPending,
			workspace.MaterializationFaultAfterInventoryActivation,
		}
	}
	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	artifacts := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/a", path: "docs/nested/story.md", content: "story\n"},
	)
	snapshots := captureMaterializationRecoverySnapshots(
		t, root, testGeneratorVersion, artifacts, capturePoints,
	)
	for _, point := range points {
		point := point
		t.Run(string(point), func(t *testing.T) {
			t.Parallel()

			requireFullSuiteCase(
				t,
				point == workspace.MaterializationFaultAfterPending ||
					point == workspace.MaterializationFaultAfterInventoryActivation,
				"intermediate materialization publication boundary",
			)

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			moveTestFilesystemTree(t, snapshots[point], root)
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

func TestMaterializationRecoveryNeverClaimsTargetsThatAppearAfterPending(t *testing.T) {
	t.Parallel()

	t.Run("matching file", func(t *testing.T) {
		t.Parallel()

		root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
		initial := materializationArtifacts(t, artifactFixture{
			id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
		})
		if _, err := workspace.SynchronizeMaterialization(
			root, testGeneratorVersion, initial, workspace.MaterializationOptions{},
		); err != nil {
			t.Fatal(err)
		}
		desired := materializationArtifacts(t,
			artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
			artifactFixture{id: "story/a", path: "story.md", content: "generated story\n"},
		)
		fault := func(point workspace.MaterializationFaultPoint) error {
			if point != workspace.MaterializationFaultAfterPending {
				return nil
			}
			if err := os.WriteFile(filepath.Join(root, "story.md"), []byte("generated story\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return errors.New("simulated crash")
		}
		if _, err := workspace.SynchronizeMaterialization(
			root, testGeneratorVersion, desired, workspace.MaterializationOptions{FaultInjector: fault},
		); err == nil {
			t.Fatal("pending fault did not interrupt materialization")
		}

		conflicts := requireMaterializationConflicts(t, synchronize(root, desired))
		if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictUnownedPath, "story.md") {
			t.Fatalf("conflicts = %#v", conflicts)
		}
		assertFileContent(t, filepath.Join(root, "story.md"), "generated story\n")
		if got := len(readInventoryFixture(t, root).Artifacts); got != 1 {
			t.Fatalf("inventory claimed matching user file: %d artifacts", got)
		}
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()

		requireFullSuite(t, "appearing materialization target permutation")

		root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
		initial := materializationArtifacts(t, artifactFixture{
			id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
		})
		if _, err := workspace.SynchronizeMaterialization(
			root, testGeneratorVersion, initial, workspace.MaterializationOptions{},
		); err != nil {
			t.Fatal(err)
		}
		desired := materializationArtifacts(t,
			artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
			artifactFixture{id: "story/a", path: "docs/story.md", content: "story\n"},
		)
		fault := func(point workspace.MaterializationFaultPoint) error {
			if point != workspace.MaterializationFaultAfterPending {
				return nil
			}
			if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "docs", "keep.txt"), []byte("user\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return errors.New("simulated crash")
		}
		if _, err := workspace.SynchronizeMaterialization(
			root, testGeneratorVersion, desired, workspace.MaterializationOptions{FaultInjector: fault},
		); err == nil {
			t.Fatal("pending fault did not interrupt materialization")
		}

		conflicts := requireMaterializationConflicts(t, synchronize(root, desired))
		if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictUnownedPath, "docs") {
			t.Fatalf("conflicts = %#v", conflicts)
		}
		assertFileContent(t, filepath.Join(root, "docs", "keep.txt"), "user\n")
		if _, err := os.Stat(filepath.Join(root, "docs", "story.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("story was written into unowned directory: %v", err)
		}
	})
}

func TestMaterializationRecoveryPreservesTransactionPathsWithoutExactIdentity(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive materialization identity-replacement matrix")

	sourceRoot := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
	if _, err := workspace.SynchronizeMaterialization(
		sourceRoot, testGeneratorVersion, initial, workspace.MaterializationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/a", path: "story.md", content: "generated story\n"},
	)
	var pending pendingMaterializationFixture
	if _, err := workspace.SynchronizeMaterialization(
		sourceRoot, testGeneratorVersion, desired,
		workspace.MaterializationOptions{FaultInjector: func(point workspace.MaterializationFaultPoint) error {
			if point != workspace.MaterializationFaultAfterPending {
				return nil
			}
			pending = readPendingMaterializationFixture(t, sourceRoot)
			return errors.New("simulated crash")
		}},
	); err == nil {
		t.Fatal("pending fault did not interrupt materialization")
	}
	pendingSnapshot := filepath.Join(canonicalMaterializationTestTempDir(t), "pending")
	copyTestFilesystemTree(t, sourceRoot, pendingSnapshot)

	for _, test := range []struct {
		name              string
		targetMayActivate bool
		mutate            func(t *testing.T, root string, pending pendingMaterializationFixture)
		assert            func(t *testing.T, root string, pending pendingMaterializationFixture)
	}{
		{
			name: "matching activation",
			mutate: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				activation := filepath.Join(root, filepath.FromSlash(pending.Writes[0].ActivationPath))
				if err := os.WriteFile(activation, []byte("generated story\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				assertFileContent(t, filepath.Join(root, filepath.FromSlash(pending.Writes[0].ActivationPath)), "generated story\n")
			},
		},
		{
			name: "wrong activation",
			mutate: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				activation := filepath.Join(root, filepath.FromSlash(pending.Writes[0].ActivationPath))
				if err := os.WriteFile(activation, []byte("user-owned\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				assertFileContent(t, filepath.Join(root, filepath.FromSlash(pending.Writes[0].ActivationPath)), "user-owned\n")
			},
		},
		{
			name: "replaced stage",
			mutate: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				stage := filepath.Join(root, filepath.FromSlash(pending.Writes[0].StagePath))
				if err := os.Remove(stage); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stage, []byte("generated story\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				assertFileContent(t, filepath.Join(root, filepath.FromSlash(pending.Writes[0].StagePath)), "generated story\n")
			},
		},
		{
			name: "replaced staging directory",
			mutate: func(t *testing.T, root string, _ pendingMaterializationFixture) {
				t.Helper()
				staging := filepath.Join(root, workspace.MaterializationStagingDirectoryName)
				if err := os.RemoveAll(staging); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(staging, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(staging, "user-owned.txt"), []byte("user-owned\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, root string, _ pendingMaterializationFixture) {
				t.Helper()
				assertFileContent(t, filepath.Join(root, workspace.MaterializationStagingDirectoryName, "user-owned.txt"), "user-owned\n")
			},
		},
		{
			name:              "replaced control temporary",
			targetMayActivate: true,
			mutate: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				temporary := filepath.Join(root, filepath.FromSlash(pending.InventoryControl.TemporaryPath))
				if err := os.Remove(temporary); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(temporary, []byte("user-owned control\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				assertFileContent(t, filepath.Join(root, filepath.FromSlash(pending.InventoryControl.TemporaryPath)), "user-owned control\n")
			},
		},
		{
			name:              "matching control temporary",
			targetMayActivate: true,
			mutate: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				temporary := filepath.Join(root, filepath.FromSlash(pending.InventoryControl.TemporaryPath))
				content, err := os.ReadFile(temporary)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(temporary); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(temporary, content, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, root string, pending pendingMaterializationFixture) {
				t.Helper()
				temporary := filepath.Join(root, filepath.FromSlash(pending.InventoryControl.TemporaryPath))
				if _, err := os.Stat(temporary); err != nil {
					t.Fatalf("matching unowned control temporary was removed: %v", err)
				}
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			copyTestFilesystemTree(t, pendingSnapshot, root)
			test.mutate(t, root, pending)

			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{}); err == nil {
				t.Fatal("recovery accepted a transaction path without exact identity")
			}
			test.assert(t, root, pending)
			if !test.targetMayActivate {
				if _, err := os.Stat(filepath.Join(root, "story.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unproved transaction bytes were activated: %v", err)
				}
			}
		})
	}
}

func TestMaterializationControlActivationNeverOverwritesAppearingTargets(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization control-target replacement matrix")

	for _, test := range []struct {
		name         string
		faultOrdinal int
		target       string
	}{
		{name: "inventory", faultOrdinal: 1, target: workspace.MaterializationInventoryFileName},
		{name: "state", faultOrdinal: 2, target: workspace.MaterializationStateFileName},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			artifacts := materializationArtifacts(t, artifactFixture{
				id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
			})
			if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{}); err != nil {
				t.Fatal(err)
			}
			observed := 0
			fault := func(point workspace.MaterializationFaultPoint) error {
				if point != workspace.MaterializationFaultAfterTemporarySync {
					return nil
				}
				observed++
				if observed != test.faultOrdinal {
					return nil
				}
				if err := os.WriteFile(filepath.Join(root, test.target), []byte("user-owned control\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return errors.New("simulated crash")
			}
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion+"-next", artifacts, workspace.MaterializationOptions{FaultInjector: fault},
			); err == nil {
				t.Fatal("control activation fault did not interrupt materialization")
			}

			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion+"-next", artifacts, workspace.MaterializationOptions{},
			); err == nil {
				t.Fatal("recovery overwrote a control target that appeared after quarantine")
			}
			assertFileContent(t, filepath.Join(root, test.target), "user-owned control\n")
		})
	}
}

func TestMaterializationRecoversMissingControlTargetsAfterQuarantine(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		faultOrdinal int
		target       string
	}{
		{name: "inventory", faultOrdinal: 1, target: workspace.MaterializationInventoryFileName},
		{name: "state", faultOrdinal: 2, target: workspace.MaterializationStateFileName},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			requireFullSuiteCase(
				t,
				test.name == "inventory",
				"materialization control-file permutation",
			)

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			artifacts := materializationArtifacts(t, artifactFixture{
				id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
			})
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{},
			); err != nil {
				t.Fatal(err)
			}
			observed := 0
			fault := func(point workspace.MaterializationFaultPoint) error {
				if point != workspace.MaterializationFaultAfterTemporarySync {
					return nil
				}
				observed++
				if observed == test.faultOrdinal {
					return errors.New("simulated control activation crash")
				}
				return nil
			}
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion+"-next", artifacts,
				workspace.MaterializationOptions{FaultInjector: fault},
			); err == nil {
				t.Fatal("control activation fault did not interrupt materialization")
			}
			if _, err := os.Stat(filepath.Join(root, test.target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("control target was not in the expected missing-target crash state: %v", err)
			}

			result, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion+"-next", artifacts, workspace.MaterializationOptions{},
			)
			if err != nil {
				t.Fatalf("recover missing %s control target: %v", test.name, err)
			}
			if !result.Recovered() {
				t.Fatal("control target recovery was not reported")
			}
			if got := result.Inventory().GeneratorVersion(); got != testGeneratorVersion+"-next" {
				t.Fatalf("generator version = %q", got)
			}
			assertNoMaterializationTransaction(t, root)
		})
	}
}

func TestMaterializationRecoveryPreservesAppearingDirectoryPreparation(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization directory-preparation replacement")

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/a", path: "docs/story.md", content: "story\n"},
	)
	var pending pendingMaterializationFixture
	fault := func(point workspace.MaterializationFaultPoint) error {
		if point != workspace.MaterializationFaultAfterPending {
			return nil
		}
		pending = readPendingMaterializationFixture(t, root)
		preparation := filepath.Join(root, filepath.FromSlash(pending.CreateDirectories[0].PreparationPath))
		if err := os.Mkdir(preparation, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(preparation, "user-owned.txt"), []byte("user-owned\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return errors.New("simulated crash")
	}
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, desired, workspace.MaterializationOptions{FaultInjector: fault},
	); err == nil {
		t.Fatal("pending fault did not interrupt materialization")
	}

	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{}); err == nil {
		t.Fatal("recovery claimed an appearing directory preparation")
	}
	preparation := filepath.Join(root, filepath.FromSlash(pending.CreateDirectories[0].PreparationPath))
	assertFileContent(t, filepath.Join(preparation, "user-owned.txt"), "user-owned\n")
	if _, err := os.Stat(filepath.Join(root, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unproved directory preparation was activated: %v", err)
	}
}

func TestMaterializationNeverDeletesRecreatedOwnedDirectoryInstance(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization directory-identity replacement")

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/stale", path: "old/nested/stale.md", content: "stale\n"},
	)
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
	replaced := filepath.Join(root, "old", "nested")
	fault := func(point workspace.MaterializationFaultPoint) error {
		if point != workspace.MaterializationFaultAfterStaleDelete {
			return nil
		}
		if err := os.RemoveAll(replaced); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(replaced, 0o755); err != nil {
			t.Fatal(err)
		}
		return errors.New("simulated crash")
	}
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, desired, workspace.MaterializationOptions{FaultInjector: fault},
	); err == nil {
		t.Fatal("stale deletion fault did not interrupt materialization")
	}

	conflicts := requireMaterializationConflicts(t, synchronize(root, desired))
	if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictModifiedOwnedPath, "old/nested") {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	info, err := os.Stat(replaced)
	if err != nil || !info.IsDir() {
		t.Fatalf("recreated directory was deleted: %v", err)
	}
}

func TestMaterializationQuarantinesBeforeHashingOwnedTargets(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization quarantine mutation matrix")

	for _, test := range []struct {
		name       string
		initial    []artifactFixture
		desired    []artifactFixture
		mutatePath string
	}{
		{
			name: "overwrite",
			initial: []artifactFixture{
				{id: "manifest/sample", path: "feature.plan.yaml", content: "v1\n"},
			},
			desired: []artifactFixture{
				{id: "manifest/sample", path: "feature.plan.yaml", content: "v2\n"},
			},
			mutatePath: "feature.plan.yaml",
		},
		{
			name: "delete",
			initial: []artifactFixture{
				{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
				{id: "story/stale", path: "stale.md", content: "stale\n"},
			},
			desired: []artifactFixture{
				{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
			},
			mutatePath: "stale.md",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, materializationArtifacts(t, test.initial...), workspace.MaterializationOptions{},
			); err != nil {
				t.Fatal(err)
			}
			mutated := false
			fault := func(point workspace.MaterializationFaultPoint) error {
				if point != workspace.MaterializationFaultAfterQuarantine || mutated {
					return nil
				}
				if err := filepath.WalkDir(root, func(current string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if strings.HasPrefix(entry.Name(), "feature.materialization.txn-") &&
						strings.HasSuffix(entry.Name(), ".quarantine") {
						if err := os.WriteFile(current, []byte("user edit during apply\n"), 0o644); err != nil {
							return err
						}
						mutated = true
						return filepath.SkipAll
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !mutated {
					t.Fatal("quarantine was not visible at the quarantine fault point")
				}
				return nil
			}
			_, err := workspace.SynchronizeMaterialization(
				root,
				testGeneratorVersion,
				materializationArtifacts(t, test.desired...),
				workspace.MaterializationOptions{FaultInjector: fault},
			)
			conflicts := requireMaterializationConflicts(t, err)
			if !hasMaterializationConflict(conflicts, workspace.MaterializationConflictModifiedOwnedPath, test.mutatePath) {
				t.Fatalf("conflicts = %#v", conflicts)
			}
			assertFileContent(t, filepath.Join(root, test.mutatePath), "user edit during apply\n")
		})
	}
}

func TestMaterializationRecoveryPreservesAReplacedQuarantineSource(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization quarantine replacement")

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/stale", path: "stale.md", content: "stale\n"},
	)
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
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
	external := filepath.Join(canonicalMaterializationTestTempDir(t), "user.txt")
	if err := os.WriteFile(external, []byte("user-owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "stale.md")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, target); err != nil {
		t.Fatal(err)
	}

	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, desired, workspace.MaterializationOptions{}); err == nil {
		t.Fatal("recovery accepted a replacement quarantine source")
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement symlink was moved or removed: %v", err)
	}
	assertFileContent(t, external, "user-owned\n")
}

func TestMaterializationPreservesUnownedReservedStagingPaths(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization reserved-path replacement")

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	artifacts := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, workspace.MaterializationStagingDirectoryName)
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(staging, "artifact-000000.data")
	if err := os.WriteFile(userFile, []byte("user-owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{},
	)
	var corruption workspace.MaterializationCorruptionError
	if !errors.As(err, &corruption) {
		t.Fatalf("error = %T %v, want MaterializationCorruptionError", err, err)
	}
	assertFileContent(t, userFile, "user-owned\n")
}

func TestMaterializationRecoversStaleDeletionAndDirectoryCleanup(t *testing.T) {
	t.Parallel()

	points := []workspace.MaterializationFaultPoint{
		workspace.MaterializationFaultAfterQuarantine,
		workspace.MaterializationFaultAfterStaleDelete,
		workspace.MaterializationFaultAfterDirectoryCleanup,
	}
	capturePoints := points
	if testing.Short() {
		capturePoints = []workspace.MaterializationFaultPoint{
			workspace.MaterializationFaultAfterQuarantine,
			workspace.MaterializationFaultAfterStaleDelete,
		}
	}
	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/stale", path: "old/nested/stale.md", content: "stale\n"},
	)
	if _, err := workspace.SynchronizeMaterialization(root, testGeneratorVersion, initial, workspace.MaterializationOptions{}); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t, artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"})
	snapshots := captureMaterializationRecoverySnapshots(
		t, root, testGeneratorVersion, desired, capturePoints,
	)
	for _, point := range points {
		point := point
		t.Run(string(point), func(t *testing.T) {
			t.Parallel()

			requireFullSuiteCase(
				t,
				point != workspace.MaterializationFaultAfterDirectoryCleanup,
				"intermediate stale-deletion cleanup boundary",
			)

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			moveTestFilesystemTree(t, snapshots[point], root)
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

func TestMaterializationRecoversOwnedUpdatesAcrossTransactionFaults(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive owned-update recovery matrix")

	points := []workspace.MaterializationFaultPoint{
		workspace.MaterializationFaultAfterStaging,
		workspace.MaterializationFaultAfterPending,
		workspace.MaterializationFaultAfterQuarantine,
		workspace.MaterializationFaultAfterTemporarySync,
		workspace.MaterializationFaultAfterArtifactWrite,
		workspace.MaterializationFaultAfterInventoryActivation,
		workspace.MaterializationFaultAfterStateActivation,
	}
	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "v1\n",
	})
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, initial, workspace.MaterializationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "v2\n",
	})
	snapshots := captureMaterializationRecoverySnapshots(
		t, root, testGeneratorVersion, desired, points,
	)
	for _, point := range points {
		point := point
		t.Run(string(point), func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			moveTestFilesystemTree(t, snapshots[point], root)
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion, desired, workspace.MaterializationOptions{},
			); err != nil {
				t.Fatalf("recover update after %s: %v", point, err)
			}
			assertFileContent(t, filepath.Join(root, "feature.plan.yaml"), "v2\n")
			assertNoMaterializationTransaction(t, root)
		})
	}
}

func TestMaterializationCleanupRecoversEveryPersistedPrefix(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive materialization cleanup-prefix matrix")

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "v1\n"},
		artifactFixture{id: "story/stale", path: "old/nested/stale.md", content: "stale\n"},
	)
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, initial, workspace.MaterializationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "v2\n"},
		artifactFixture{id: "story/new", path: "new/nested/story.md", content: "story\n"},
	)

	// Each snapshot is the exact durable state immediately after one cleanup
	// write. Replaying every snapshot retains the prior fault-prefix matrix
	// without rebuilding the same transaction once per prefix.
	var snapshots []string
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion+"-next", desired,
		workspace.MaterializationOptions{FaultInjector: func(point workspace.MaterializationFaultPoint) error {
			if point != workspace.MaterializationFaultAfterCleanupStep {
				return nil
			}
			snapshot := filepath.Join(
				canonicalMaterializationTestTempDir(t),
				fmt.Sprintf("step-%02d", len(snapshots)+1),
			)
			copyTestFilesystemTree(t, root, snapshot)
			snapshots = append(snapshots, snapshot)
			return nil
		}},
	); err != nil {
		t.Fatalf("capture cleanup prefixes: %v", err)
	}
	cleanupSteps := len(snapshots)
	if cleanupSteps < 10 {
		t.Fatalf("cleanup exercised only %d persisted steps", cleanupSteps)
	}
	for ordinal, snapshot := range snapshots {
		ordinal, snapshot := ordinal, snapshot
		t.Run(fmt.Sprintf("step-%02d", ordinal+1), func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
			moveTestFilesystemTree(t, snapshot, root)
			if _, err := workspace.SynchronizeMaterialization(
				root, testGeneratorVersion+"-next", desired, workspace.MaterializationOptions{},
			); err != nil {
				t.Fatalf("recover cleanup step %d: %v", ordinal+1, err)
			}
			assertFileContent(t, filepath.Join(root, "feature.plan.yaml"), "v2\n")
			assertFileContent(t, filepath.Join(root, "new", "nested", "story.md"), "story\n")
			if _, err := os.Stat(filepath.Join(root, "old")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale directory remains after cleanup recovery: %v", err)
			}
			assertNoMaterializationTransaction(t, root)
		})
	}
}

func TestMaterializationCleanupPreservesReplacementAtVerifiedUnlink(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization cleanup replacement boundary")

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
	initial := materializationArtifacts(t,
		artifactFixture{id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n"},
		artifactFixture{id: "story/stale", path: "stale.md", content: "stale\n"},
	)
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, initial, workspace.MaterializationOptions{},
	); err != nil {
		t.Fatal(err)
	}
	desired := materializationArtifacts(t, artifactFixture{
		id: "manifest/sample", path: "feature.plan.yaml", content: "manifest\n",
	})
	mutated := false
	backup := filepath.Join(canonicalMaterializationTestTempDir(t), "transaction-quarantine-backup")
	fault := func(point workspace.MaterializationFaultPoint) error {
		if point != workspace.MaterializationFaultBeforeCleanupUnlink || mutated {
			return nil
		}
		pending := readPendingMaterializationFixture(t, root)
		quarantine := filepath.Join(root, filepath.FromSlash(pending.Deletes[0].QuarantinePath))
		if err := os.Rename(quarantine, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(quarantine, []byte("user-owned replacement\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mutated = true
		return nil
	}
	if _, err := workspace.SynchronizeMaterialization(
		root, testGeneratorVersion, desired, workspace.MaterializationOptions{FaultInjector: fault},
	); err == nil || !mutated {
		t.Fatalf("cleanup accepted a concurrent replacement: mutated=%t err=%v", mutated, err)
	}
	pending := readPendingMaterializationFixture(t, root)
	quarantine := filepath.Join(root, filepath.FromSlash(pending.Deletes[0].QuarantinePath))
	assertFileContent(t, quarantine, "user-owned replacement\n")
	assertFileContent(t, backup, "stale\n")
	if _, err := os.Stat(filepath.Join(root, "stale.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed stale target unexpectedly reappeared: %v", err)
	}
}

func TestMaterializationRejectsCorruptStagedBytesDuringRecovery(t *testing.T) {
	t.Parallel()

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
	staging := filepath.Join(root, workspace.MaterializationStagingDirectoryName)
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) != 3 {
		t.Fatalf("staging entries = %v, err=%v", entries, err)
	}
	stage := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".stage") {
			stage = filepath.Join(staging, entry.Name())
			break
		}
	}
	if stage == "" {
		t.Fatal("staged artifact identity was not found")
	}
	if err := os.WriteFile(stage, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = workspace.SynchronizeMaterialization(root, testGeneratorVersion, artifacts, workspace.MaterializationOptions{})
	var corruption workspace.MaterializationCorruptionError
	if !errors.As(err, &corruption) {
		t.Fatalf("error = %T %v, want corruption", err, err)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.plan.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt stage was activated: %v", err)
	}
}

func TestMaterializationRecoveryRechecksOwnedBytesBeforeOverwriteOrDelete(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "materialization owned-byte mutation matrix")

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
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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
	t.Parallel()

	root := filepath.Join(canonicalMaterializationTestTempDir(t), "plan")
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

func canonicalMaterializationTestTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("canonicalize temporary test directory: %v", err)
	}
	return canonical
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

type pendingMaterializationFixture struct {
	CreateDirectories []struct {
		PreparationPath string `json:"preparation_path"`
	} `json:"create_directories"`
	InventoryControl struct {
		TemporaryPath string `json:"temporary_path"`
	} `json:"inventory_control"`
	Deletes []struct {
		QuarantinePath string `json:"quarantine_path"`
	} `json:"deletes"`
	Writes []struct {
		Path           string `json:"path"`
		StagePath      string `json:"stage_path"`
		ActivationPath string `json:"activation_path"`
	} `json:"writes"`
}

func readPendingMaterializationFixture(t *testing.T, root string) pendingMaterializationFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, workspace.MaterializationPendingFileName))
	if err != nil {
		t.Fatal(err)
	}
	var pending pendingMaterializationFixture
	if err := json.Unmarshal(content, &pending); err != nil {
		t.Fatal(err)
	}
	return pending
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
		workspace.MaterializationCleanupFileName,
		workspace.MaterializationStagingDirectoryName,
	} {
		if _, err := os.Stat(filepath.Join(root, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction path %s remains: %v", relative, err)
		}
	}
	if err := filepath.WalkDir(root, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), "feature.materialization.txn-") {
			t.Fatalf("transaction path remains after cleanup: %s", current)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect materialization transaction cleanup: %v", err)
	}
}

// captureMaterializationRecoverySnapshots observes the first occurrence of
// each requested durable fault boundary during one successful transaction.
// Its images are equivalent to crashing immediately after those writes, but
// avoid repeating identical setup and publication work for every boundary.
func captureMaterializationRecoverySnapshots(
	t *testing.T,
	root, generatorVersion string,
	artifacts []workspace.MaterializationArtifact,
	points []workspace.MaterializationFaultPoint,
) map[workspace.MaterializationFaultPoint]string {
	t.Helper()
	wanted := make(map[workspace.MaterializationFaultPoint]bool, len(points))
	for _, point := range points {
		wanted[point] = true
	}
	snapshotDirectory := canonicalMaterializationTestTempDir(t)
	snapshots := make(map[workspace.MaterializationFaultPoint]string, len(points))
	if _, err := workspace.SynchronizeMaterialization(
		root, generatorVersion, artifacts,
		workspace.MaterializationOptions{FaultInjector: func(point workspace.MaterializationFaultPoint) error {
			if !wanted[point] || snapshots[point] != "" {
				return nil
			}
			snapshot := filepath.Join(snapshotDirectory, fmt.Sprintf("%02d-%s", len(snapshots)+1, point))
			copyTestFilesystemTree(t, root, snapshot)
			snapshots[point] = snapshot
			return nil
		}},
	); err != nil {
		t.Fatalf("capture materialization recovery boundaries: %v", err)
	}
	for _, point := range points {
		if snapshots[point] == "" {
			t.Fatalf("materialization fault boundary %s was not observed", point)
		}
	}
	return snapshots
}

// copyTestFilesystemTree makes an independent filesystem image while
// preserving hard-link identity within that image. Some fixtures use hard
// links as durable proofs, so ordinary byte copies can change their state.
func copyTestFilesystemTree(t *testing.T, source, destination string) {
	t.Helper()
	rootInfo, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat materialization snapshot source: %v", err)
	}
	if !rootInfo.IsDir() {
		t.Fatalf("materialization snapshot source is not a directory: %s", source)
	}
	if runtime.GOOS == "darwin" {
		copyTestFilesystemTreeClone(t, source, destination)
		return
	}
	copyTestFilesystemTreePortable(t, source, destination, rootInfo)
}

// cloneGitTestFilesystemTree uses a copy-on-write clone for a Git fixture.
// Unlike materialization images, Git fixtures do not use hard-link identity as
// a durable proof, so walking both trees to reconstruct that topology would
// only add setup work to every integration partition.
func cloneGitTestFilesystemTree(t *testing.T, source, destination string) {
	t.Helper()
	rootInfo, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat Git fixture source: %v", err)
	}
	if !rootInfo.IsDir() {
		t.Fatalf("Git fixture source is not a directory: %s", source)
	}
	if runtime.GOOS != "darwin" {
		copyTestFilesystemTree(t, source, destination)
		return
	}
	output, err := exec.Command("cp", "-cR", source, destination).CombinedOutput()
	if err != nil {
		t.Fatalf("clone Git fixture: %v\n%s", err, output)
	}
}

// copyTestFilesystemTreeClone uses APFS copy-on-write clones for the large
// real-Git fixtures. cp does not retain hard-link topology within the copied
// tree, so repair it from the source after cloning before exposing the image
// to a recovery test.
func copyTestFilesystemTreeClone(t *testing.T, source, destination string) {
	t.Helper()
	output, err := exec.Command("cp", "-cR", source, destination).CombinedOutput()
	if err != nil {
		t.Fatalf("clone materialization snapshot: %v\n%s", err, output)
	}
	repairTestFilesystemHardLinks(t, source, destination)
}

func repairTestFilesystemHardLinks(t *testing.T, source, destination string) {
	t.Helper()
	type copiedFile struct {
		info        os.FileInfo
		destination string
	}
	var copied []copiedFile
	if err := filepath.WalkDir(source, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == source {
			return nil
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		target := filepath.Join(destination, relative)
		for _, prior := range copied {
			if !os.SameFile(info, prior.info) {
				continue
			}
			if err := os.Remove(target); err != nil {
				return err
			}
			return os.Link(prior.destination, target)
		}
		copied = append(copied, copiedFile{info: info, destination: target})
		return nil
	}); err != nil {
		t.Fatalf("repair materialization snapshot hard links: %v", err)
	}
}

func copyTestFilesystemTreePortable(
	t *testing.T,
	source, destination string,
	rootInfo os.FileInfo,
) {
	t.Helper()
	if err := os.Mkdir(destination, rootInfo.Mode().Perm()); err != nil {
		t.Fatalf("create materialization snapshot destination: %v", err)
	}
	if err := os.Chmod(destination, rootInfo.Mode().Perm()); err != nil {
		t.Fatalf("set materialization snapshot root mode: %v", err)
	}

	type copiedFile struct {
		info        os.FileInfo
		destination string
	}
	var copied []copiedFile
	if err := filepath.WalkDir(source, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == source {
			return nil
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(current)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			for _, prior := range copied {
				if os.SameFile(info, prior.info) {
					return os.Link(prior.destination, target)
				}
			}
			input, err := os.Open(current)
			if err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
			if err != nil {
				_ = input.Close()
				return err
			}
			if _, err := io.Copy(output, input); err != nil {
				_ = output.Close()
				_ = input.Close()
				return err
			}
			if err := output.Close(); err != nil {
				_ = input.Close()
				return err
			}
			if err := input.Close(); err != nil {
				return err
			}
			if err := os.Chmod(target, info.Mode().Perm()); err != nil {
				return err
			}
			copied = append(copied, copiedFile{info: info, destination: target})
			return nil
		default:
			return fmt.Errorf("unsupported materialization snapshot entry %s (%s)", current, info.Mode())
		}
	}); err != nil {
		t.Fatalf("copy materialization snapshot: %v", err)
	}
}

// moveTestFilesystemTree consumes a one-shot snapshot without copying its
// contents. The recovery matrices below use each captured durable image once,
// so renaming retains the exact inode identities while avoiding repeated
// fixture-copy work before the operation under test.
func moveTestFilesystemTree(t *testing.T, source, destination string) {
	t.Helper()
	if filepath.Clean(source) == filepath.Clean(destination) {
		t.Fatalf("test filesystem snapshot source and destination are the same path: %s", source)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat one-shot test filesystem snapshot: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("one-shot test filesystem snapshot is not a directory: %s", source)
	}
	if err := os.RemoveAll(destination); err != nil {
		t.Fatalf("remove one-shot test filesystem destination %s: %v", destination, err)
	}
	if err := os.Rename(source, destination); err != nil {
		t.Fatalf("move one-shot test filesystem snapshot into place: %v", err)
	}
}
