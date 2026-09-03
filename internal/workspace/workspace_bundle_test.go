package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestWorkspaceBundleRejectsHiddenDuplicateAndAmbiguousSources(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)

	t.Run("hidden source", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{
			"plans": []string{".plans/alpha.yaml"},
		})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "hidden path") {
			t.Fatalf("hidden bundle source error = %v", err)
		}
	})

	t.Run("duplicate plan path", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{
			"plans": []string{"plans/alpha.yaml", "plans/alpha.yaml"},
		})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "duplicate plan path") {
			t.Fatalf("duplicate plan source error = %v", err)
		}
	})

	t.Run("descriptor self reference", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{"workspace": workspace.WorkspaceBundleFileName})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "cannot reference its descriptor") {
			t.Fatalf("descriptor self-reference error = %v", err)
		}
	})

	t.Run("ordinary generated-named source", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{"workspace": "generated/workspace.yaml"})
		path := filepath.Join(root, "generated", "workspace.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fixture.sources.Workspace.Bytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.LoadWorkspaceBundle(root); err != nil {
			t.Fatalf("ordinary generated-named source error = %v", err)
		}
	})

	t.Run("removed authorities", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{"authorities": []any{}})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("removed authorities error = %v", err)
		}
	})

	t.Run("removed control plane authority", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{"control_plane_authority": "owner-policy"})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("removed control-plane authority error = %v", err)
		}
	})

	t.Run("duplicate descriptor key", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, nil)
		descriptor := filepath.Join(root, workspace.WorkspaceBundleFileName)
		content := `{"schema_version":2,"schema_version":2,"workspace":"feature.workspace.yaml","plans":["plans/alpha.yaml"],"execution_config":"config/execution.yaml"}`
		if err := os.WriteFile(descriptor, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "duplicate key") {
			t.Fatalf("duplicate descriptor key error = %v", err)
		}
	})
}

func TestWorkspaceBundleRejectsReservedDerivedRootSuffix(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	for _, suffix := range []string{
		".feature-runtime",
		"-attempt-worktrees",
	} {
		suffix := suffix
		t.Run(suffix, func(t *testing.T) {
			root := writeDefinitionBundle(t, fixture, nil)
			reservedRoot := root + suffix
			if err := os.Rename(root, reservedRoot); err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.LoadWorkspaceBundle(reservedRoot); err == nil ||
				!strings.Contains(err.Error(), "reserved derived suffix") ||
				!strings.Contains(err.Error(), suffix) {
				t.Fatalf("reserved bundle root %q error = %v", reservedRoot, err)
			}
		})
	}
}

func TestConfiguredWorkspaceRuntimeRootRejectsBundleAndGitRepository(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	bundleRoot := writeDefinitionBundle(t, fixture, nil)
	bundle, err := workspace.LoadWorkspaceBundle(bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		root string
		want string
	}{
		{
			name: "bundle root",
			root: bundle.Root(),
			want: "workspace bundle root",
		},
		{
			name: "Git repository",
			root: bundle.Definition().Workspace().RepositoryRoot(),
			want: "Git repository",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if err := workspace.ValidateWorkspaceRuntimeRoot(test.root); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("configured runtime root %q error = %v", test.root, err)
			}
		})
	}
}

func TestWorkspaceBundleRejectsNestedBundleRuntimeAndAttemptRoots(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	outerRoot := writeDefinitionBundle(t, fixture, nil)
	outer, err := workspace.LoadWorkspaceBundle(outerRoot)
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, fixture.sources)
	stagedNestedRoot := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
	nestedRoot := filepath.Join(outer.Root(), "nested", "inner")
	if err := os.MkdirAll(filepath.Dir(nestedRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stagedNestedRoot, nestedRoot); err != nil {
		t.Fatal(err)
	}
	expectAncestorRejection := func(subject string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "ancestor workspace bundle root") ||
			!strings.Contains(err.Error(), outer.Root()) {
			t.Fatalf("%s nested-bundle error = %v", subject, err)
		}
	}

	_, err = workspace.LoadWorkspaceBundle(nestedRoot)
	expectAncestorRejection("nested bundle", err)
	_, err = workspace.DerivedWorkspaceRuntimeDirectory(nestedRoot)
	expectAncestorRejection("nested derived runtime", err)

	runtimeRoot := filepath.Join(
		filepath.Dir(nestedRoot), filepath.Base(nestedRoot)+".feature-runtime",
	)
	_, err = initializeWorkspaceV2(
		t, runtimeRoot, definition, mustTime(t, "2026-09-03T12:02:00Z"),
	)
	expectAncestorRejection("nested runtime initialization", err)
	if _, statErr := os.Lstat(runtimeRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("nested runtime was created: %v", statErr)
	}

	attemptRoot := runtimeRoot + "-attempt-worktrees"
	destination := filepath.Join(attemptRoot, "attempt")
	_, err = workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(),
		definition.Workspace().RepositoryRoot(),
		definition.Workspace().BaseCommit(),
		destination,
	)
	expectAncestorRejection("nested attempt root", err)
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("nested attempt was created: %v", statErr)
	}
}

func TestAttemptCannotMaterializeUnderAnotherWorkspaceBundleRoot(t *testing.T) {
	t.Parallel()

	firstFixture := newDefinitionFixture(t)
	firstRoot := writeDefinitionBundle(t, firstFixture, nil)
	first, err := workspace.LoadWorkspaceBundle(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot, err := workspace.DerivedWorkspaceRuntimeDirectory(first.Root())
	if err != nil {
		t.Fatal(err)
	}
	foreignRoot, err := workspace.DerivedWorkspaceWorktreeRoot(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}

	secondFixture := newDefinitionFixture(t)
	stagedForeignRoot := writeDefinitionBundle(t, secondFixture, nil)
	if err := os.Rename(stagedForeignRoot, foreignRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadWorkspaceBundle(foreignRoot); err == nil ||
		!strings.Contains(err.Error(), "reserved derived suffix") {
		t.Fatalf("reserved foreign bundle root error = %v", err)
	}
	descriptorPath := filepath.Join(foreignRoot, workspace.WorkspaceBundleFileName)
	descriptorBefore, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(foreignRoot, "attempt")
	_, err = workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(),
		first.Definition().Workspace().RepositoryRoot(),
		first.Definition().Workspace().BaseCommit(),
		destination,
	)
	if err == nil || !strings.Contains(err.Error(), "workspace bundle root") {
		t.Fatalf("materialize under foreign bundle error = %v", err)
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("materialization created a child under the foreign bundle: %v", statErr)
	}
	descriptorAfter, err := os.ReadFile(descriptorPath)
	if err != nil || !bytes.Equal(descriptorBefore, descriptorAfter) {
		t.Fatalf("foreign bundle descriptor changed: %q, %v", descriptorAfter, err)
	}
}

func TestWorkspaceBundleWritesOneAtomicCanonicalLock(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	root := writeDefinitionBundle(t, fixture, nil)
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := workspace.WriteWorkspaceBundleLock(
		bundle, workspace.WorkspaceLockWriteOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created() || first.Updated() {
		t.Fatalf("initial workspace lock result = %#v", first)
	}
	workspaceLock := filepath.Join(root, workspace.WorkspaceLockFileName)
	content, err := os.ReadFile(workspaceLock)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["state"]; exists {
		t.Fatalf("generated workspace lock contains mutable runtime state: %s", content)
	}
	second, err := workspace.WriteWorkspaceBundleLock(
		bundle, workspace.WorkspaceLockWriteOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created() || second.Updated() {
		t.Fatalf("idempotent workspace lock result = %#v", second)
	}

	modified := append(append([]byte(nil), content...), '\n')
	if err := os.WriteFile(workspaceLock, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = workspace.WriteWorkspaceBundleLock(
		bundle, workspace.WorkspaceLockWriteOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "committed workspace lock authority") {
		t.Fatalf("modified workspace lock error = %v", err)
	}
	after, readErr := os.ReadFile(workspaceLock)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(modified) {
		t.Fatalf("modified workspace lock was overwritten:\n%s", after)
	}
}

func TestWorkspaceBundleLockTornWriteRetainsPriorReadableLock(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	root := writeDefinitionBundle(t, fixture, nil)
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.WriteWorkspaceBundleLock(
		bundle, workspace.WorkspaceLockWriteOptions{},
	); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "init", "--quiet", "--initial-branch=main")
	runTargetGitTest(t, root, "config", "user.name", "Workspace Lock Test")
	runTargetGitTest(t, root, "config", "user.email", "workspace-lock@example.test")
	runTargetGitTest(t, root, "add", ".")
	runTargetGitTest(t, root, "commit", "--quiet", "-m", "Commit canonical workspace lock")
	before, err := workspace.ReadWorkspaceBundleLock(bundle)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(root, "plans", "alpha.yaml")
	plan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, append(plan, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err = workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("simulated torn write")
	_, err = workspace.WriteWorkspaceBundleLock(bundle, workspace.WorkspaceLockWriteOptions{
		FaultInjector: func(point workspace.WorkspaceLockWriteFaultPoint) error {
			if point == workspace.WorkspaceLockFaultAfterTemporarySync {
				return fault
			}
			return nil
		},
	})
	if !errors.Is(err, fault) {
		t.Fatalf("simulated workspace lock tear = %v", err)
	}
	after, err := workspace.ReadWorkspaceBundleLock(bundle)
	if err != nil || string(after) != string(before) {
		t.Fatalf("readable lock after simulated tear = %q, %v", after, err)
	}
}

func TestWorkspaceBundleLockSerializesInterleavedWriters(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	root := writeDefinitionBundle(t, fixture, nil)
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.WriteWorkspaceBundleLock(
		bundle, workspace.WorkspaceLockWriteOptions{},
	); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "init", "--quiet", "--initial-branch=main")
	runTargetGitTest(t, root, "config", "user.name", "Workspace Lock Test")
	runTargetGitTest(t, root, "config", "user.email", "workspace-lock@example.test")
	runTargetGitTest(t, root, "add", ".")
	runTargetGitTest(t, root, "commit", "--quiet", "-m", "Commit initial workspace lock")

	planPath := filepath.Join(root, "plans", "alpha.yaml")
	originalPlan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		planPath, append(originalPlan, []byte("\n# writer one\n")...), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	firstBundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}

	firstReady := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, writeErr := workspace.WriteWorkspaceBundleLock(
			firstBundle,
			workspace.WorkspaceLockWriteOptions{
				FaultInjector: func(point workspace.WorkspaceLockWriteFaultPoint) error {
					if point == workspace.WorkspaceLockFaultAfterTemporarySync {
						close(firstReady)
						<-releaseFirst
					}
					return nil
				},
			},
		)
		firstDone <- writeErr
	}()
	<-firstReady

	if err := os.WriteFile(
		planPath, append(originalPlan, []byte("\n# writer two\n")...), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	secondBundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	secondContended := make(chan struct{})
	secondReachedTemporarySync := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		_, writeErr := workspace.WriteWorkspaceBundleLock(secondBundle, workspace.WorkspaceLockWriteOptions{
			FaultInjector: func(point workspace.WorkspaceLockWriteFaultPoint) error {
				switch point {
				case workspace.WorkspaceLockFaultPublicationLockContended:
					close(secondContended)
				case workspace.WorkspaceLockFaultAfterTemporarySync:
					close(secondReachedTemporarySync)
					return errors.New("second writer reached temporary sync before first publication")
				}
				return nil
			},
		},
		)
		secondDone <- writeErr
	}()
	select {
	case <-secondContended:
	case <-secondReachedTemporarySync:
		close(releaseFirst)
		if err := <-firstDone; err != nil {
			t.Fatalf("first writer after missed serialization = %v", err)
		}
		t.Fatal("second writer passed authority check before first publication")
	case err := <-secondDone:
		close(releaseFirst)
		if firstErr := <-firstDone; firstErr != nil {
			t.Fatalf("first writer after second writer error = %v", firstErr)
		}
		t.Fatalf("second writer ended before publication-lock contention: %v", err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first writer error = %v", err)
	}
	if err := <-secondDone; err == nil ||
		!strings.Contains(err.Error(), "differs unexpectedly from its committed value") {
		t.Fatalf("second writer error = %v", err)
	}

	stored, err := workspace.ReadWorkspaceBundleLock(firstBundle)
	if err != nil {
		t.Fatal(err)
	}
	want, err := workspace.WorkspaceBundleLockBytes(firstBundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, want) {
		t.Fatalf("interleaved writers retained %q, want first writer %q", stored, want)
	}
}

func TestWorkspaceBundleVerifiesPlanRootPath(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	root := writeDefinitionBundle(t, fixture, nil)
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Root() != root {
		t.Fatalf("bundle root = %s, want %s", bundle.Root(), root)
	}
	if err := bundle.VerifyRoot(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceBundleBindsDescriptorAndRejectsProviderEraFields(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	root := writeDefinitionBundle(t, fixture, nil)
	first, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	bundleArtifact := artifactByKind(t, first.Definition(), workspace.ArtifactWorkspaceBundle)
	if bundleArtifact.Path() != workspace.WorkspaceBundleFileName || bundleArtifact.SourceHash() != first.DescriptorDigest() {
		t.Fatalf("bundle artifact does not bind descriptor: path=%s source=%s descriptor=%s", bundleArtifact.Path(), bundleArtifact.SourceHash(), first.DescriptorDigest())
	}

	descriptorPath := filepath.Join(root, workspace.WorkspaceBundleFileName)
	descriptorBytes, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]any
	if err := json.Unmarshal(descriptorBytes, &descriptor); err != nil {
		t.Fatal(err)
	}
	descriptor["control_plane_authority"] = "owner-policy"
	updatedBytes, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(descriptorPath, updatedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadWorkspaceBundle(root); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("provider-era descriptor field was accepted: %v", err)
	}
}

func TestWorkspaceBundleLoadsConfiguredReviewGatePolicyFiles(t *testing.T) {
	t.Parallel()

	fixture, rootPolicy, unitPolicy := configuredReviewGateFixture(t)
	root := writeDefinitionBundle(t, fixture, nil)
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]bool)
	for _, path := range bundle.SourcePaths() {
		paths[path] = true
	}
	for _, path := range []string{"policies/root-review.md", "policies/unit-two-review.md"} {
		if !paths[path] {
			t.Fatalf("bundle source paths omit configured policy %s: %#v", path, bundle.SourcePaths())
		}
	}
	sources := bundle.Sources()
	policyBytes := make(map[string][]byte)
	for _, policy := range sources.ReviewPolicies {
		policyBytes[policy.Path] = policy.Bytes
	}
	if string(policyBytes["policies/root-review.md"]) != string(rootPolicy) ||
		string(policyBytes["policies/unit-two-review.md"]) != string(unitPolicy) {
		t.Fatalf("bundle policy sources = %#v", policyBytes)
	}
}

func writeDefinitionBundle(t *testing.T, fixture definitionFixture, overrides map[string]any) string {
	t.Helper()
	root := canonicalMaterializationTestTempDir(t)
	descriptor := map[string]any{
		"schema_version":   2,
		"workspace":        "feature.workspace.yaml",
		"plans":            []string{"plans/alpha.yaml"},
		"execution_config": "config/execution.yaml",
	}
	for key, value := range overrides {
		descriptor[key] = value
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		workspace.WorkspaceBundleFileName:    encoded,
		fixture.sources.Workspace.Path:       fixture.sources.Workspace.Bytes,
		fixture.sources.Plans[0].Path:        fixture.sources.Plans[0].Bytes,
		fixture.sources.ExecutionConfig.Path: fixture.sources.ExecutionConfig.Bytes,
	}
	for _, policy := range fixture.sources.ReviewPolicies {
		files[policy.Path] = policy.Bytes
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
