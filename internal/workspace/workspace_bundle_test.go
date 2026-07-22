package workspace_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestWorkspaceBundleRejectsHiddenDuplicateAndAmbiguousSources(t *testing.T) {
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

	t.Run("duplicate authority", func(t *testing.T) {
		authority := map[string]any{
			"id": "owner-policy", "kind": "git_blob", "content_path": "authority/owner-policy.yaml",
			"repository_identity": "https://github.com/example/policy.git",
			"commit_object":       "sha1:" + strings.Repeat("1", 40), "blob_object": gitSHA1Blob(fixture.gitData),
		}
		root := writeDefinitionBundle(t, fixture, map[string]any{
			"authorities": []any{authority, authority},
		})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "duplicate authority") {
			t.Fatalf("duplicate authority error = %v", err)
		}
	})

	t.Run("duplicate authority content path", func(t *testing.T) {
		authority := map[string]any{
			"id": "owner-policy", "kind": "git_blob", "content_path": "authority/owner-policy.yaml",
			"repository_identity": "https://github.com/example/policy.git",
			"commit_object":       "sha1:" + strings.Repeat("1", 40), "blob_object": gitSHA1Blob(fixture.gitData),
		}
		other := map[string]any{
			"id": "organization-policy", "kind": "external_digest", "content_path": "authority/owner-policy.yaml",
			"expected_source_digest": workspace.DigestBytes(fixture.gitData).String(),
		}
		root := writeDefinitionBundle(t, fixture, map[string]any{"authorities": []any{authority, other}})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "claimed by both") {
			t.Fatalf("duplicate authority source error = %v", err)
		}
	})

	t.Run("authority aliases plan source", func(t *testing.T) {
		authority := map[string]any{
			"id": "organization-policy", "kind": "external_digest", "content_path": fixture.sources.Plans[0].Path,
			"expected_source_digest": workspace.DigestBytes(fixture.sources.Plans[0].Bytes).String(),
		}
		root := writeDefinitionBundle(t, fixture, map[string]any{"authorities": []any{authority}})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "claimed by both") {
			t.Fatalf("cross-role source alias error = %v", err)
		}
	})

	t.Run("descriptor self reference", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{"workspace": workspace.WorkspaceBundleFileName})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "cannot reference its descriptor") {
			t.Fatalf("descriptor self-reference error = %v", err)
		}
	})

	t.Run("generated source", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{"workspace": "generated/workspace.yaml"})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "tool-owned generated path") {
			t.Fatalf("generated source error = %v", err)
		}
	})

	t.Run("null required authorities", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, map[string]any{"authorities": nil})
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "required JSON field") {
			t.Fatalf("null authorities error = %v", err)
		}
	})

	t.Run("omitted required authorities", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, nil)
		descriptor := filepath.Join(root, workspace.WorkspaceBundleFileName)
		content, err := os.ReadFile(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(content, &document); err != nil {
			t.Fatal(err)
		}
		delete(document, "authorities")
		content, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(descriptor, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "required JSON field") {
			t.Fatalf("omitted authorities error = %v", err)
		}
	})

	t.Run("duplicate descriptor key", func(t *testing.T) {
		root := writeDefinitionBundle(t, fixture, nil)
		descriptor := filepath.Join(root, workspace.WorkspaceBundleFileName)
		content := `{"schema_version":2,"schema_version":2,"workspace":"feature.workspace.yaml","plans":["plans/alpha.yaml"],"execution_config":"config/execution.yaml","authorities":[]}`
		if err := os.WriteFile(descriptor, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.LoadWorkspaceBundle(root); err == nil || !strings.Contains(err.Error(), "duplicate key") {
			t.Fatalf("duplicate descriptor key error = %v", err)
		}
	})
}

func TestWorkspaceBundleGeneratedLocksAreImmutableOwnedProjections(t *testing.T) {
	fixture := newDefinitionFixture(t)
	root := writeDefinitionBundle(t, fixture, nil)
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := workspace.WorkspaceBundleLockArtifacts(bundle)
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(root, workspace.WorkspaceGeneratedDirectory)
	first, err := workspace.SynchronizeMaterialization(generated, "bundle-test/v2", artifacts, workspace.MaterializationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Created()) != 2 || len(first.Updated()) != 0 || len(first.Deleted()) != 0 {
		t.Fatalf("initial generated lock result = created %v updated %v deleted %v", first.Created(), first.Updated(), first.Deleted())
	}
	workspaceLock := filepath.Join(generated, workspace.WorkspaceLockFileName)
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
	second, err := workspace.SynchronizeMaterialization(generated, "bundle-test/v2", artifacts, workspace.MaterializationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Created()) != 0 || len(second.Updated()) != 0 || len(second.Deleted()) != 0 {
		t.Fatalf("idempotent generated lock result = created %v updated %v deleted %v", second.Created(), second.Updated(), second.Deleted())
	}

	modified := append(append([]byte(nil), content...), '\n')
	if err := os.WriteFile(workspaceLock, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = workspace.SynchronizeMaterialization(generated, "bundle-test/v2", artifacts, workspace.MaterializationOptions{})
	var conflicts workspace.MaterializationConflictError
	if !errors.As(err, &conflicts) {
		t.Fatalf("modified generated lock error = %T %v", err, err)
	}
	after, readErr := os.ReadFile(workspaceLock)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(modified) {
		t.Fatalf("modified generated lock was overwritten:\n%s", after)
	}
}

func TestWorkspaceBundleBindsDescriptorAndControlPlaneAuthorityIntoGeneration(t *testing.T) {
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
	second, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Definition().Generation() == second.Definition().Generation() {
		t.Fatal("control_plane_authority changed without changing the effective generation")
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
		"authorities": []any{
			map[string]any{
				"id": "owner-policy", "kind": "git_blob", "content_path": "authority/owner-policy.yaml",
				"repository_identity": "https://github.com/example/policy.git",
				"commit_object":       "sha1:" + strings.Repeat("1", 40), "blob_object": gitSHA1Blob(fixture.gitData),
			},
			map[string]any{
				"id": "organization-policy", "kind": "external_digest", "content_path": "authority/organization-policy.yaml",
				"expected_source_digest": workspace.DigestBytes(fixture.extData).String(),
			},
		},
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
		"authority/owner-policy.yaml":        fixture.gitData,
		"authority/organization-policy.yaml": fixture.extData,
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
