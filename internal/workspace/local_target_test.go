package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestLocalTargetInitializationLeavesFeatureRefAbsent(t *testing.T) {
	t.Parallel()

	for _, algorithm := range []workspace.GitHashAlgorithm{
		workspace.GitHashSHA1,
		workspace.GitHashSHA256,
	} {
		t.Run(string(algorithm), func(t *testing.T) {
			t.Parallel()

			root, base := initializeTargetRepository(t, algorithm)
			definition := localTargetDefinition(
				t, root, base, "feature/local-target-binding",
			)
			binding, err := workspace.ValidateLocalTarget(
				context.Background(), definition.Workspace(),
			)
			if err != nil {
				t.Fatalf("validate local target: %v", err)
			}
			if binding.Root() != root || binding.BaseCommit() != base ||
				binding.ObjectFormat() != algorithm {
				t.Fatalf("local target binding = %#v", binding)
			}

			runtimeRoot := canonicalTestDirectory(t)
			result, err := initializeWorkspaceV2(
				t, runtimeRoot, definition,
				mustTime(t, "2026-09-03T12:00:00Z"),
			)
			if err != nil {
				t.Fatalf("initialize local target: %v", err)
			}
			records := result.Snapshot().Records()
			if len(records) != 1 || records[0].EventType() != workspace.JournalEventWorkspaceInitialized {
				t.Fatalf("initialization journal = %#v", records)
			}
			target, ok := result.Runtime().LocalTarget()
			if !ok || target.Binding().Digest() != binding.Digest() ||
				target.CreatedHead() != base {
				t.Fatalf("runtime local target = %#v", target)
			}
			featureRef := definition.Workspace().FeatureRef()
			if err := exec.Command("git", "-C", root, "show-ref", "--verify", featureRef).Run(); err == nil {
				t.Fatalf("feature ref %s exists after initialization", featureRef)
			}
		})
	}

	t.Run("accepts deleted base ref after initialization", func(t *testing.T) {
		t.Parallel()

		root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		definition := localTargetDefinition(
			t, root, base, "feature/deleted-base-ref",
		)
		runtimeRoot := canonicalTestDirectory(t)
		if _, err := initializeWorkspaceV2(
			t, runtimeRoot, definition,
			mustTime(t, "2026-09-03T12:01:00Z"),
		); err != nil {
			t.Fatalf("initialize local target: %v", err)
		}
		runTargetGitTest(
			t, root, "update-ref", "-d", definition.Workspace().BaseRef(),
			baseObjectHex(base),
		)
		if err := exec.Command(
			"git", "-C", root, "show-ref", "--verify", "--quiet",
			definition.Workspace().BaseRef(),
		).Run(); err == nil {
			t.Fatalf("base ref %s still exists", definition.Workspace().BaseRef())
		}
		runTargetGitTest(
			t, root, "cat-file", "-e", baseObjectHex(base)+"^{commit}",
		)
		if _, err := workspace.ValidateLocalTargetForWorkspaceRuntime(
			context.Background(), runtimeRoot, definition,
		); err != nil {
			t.Fatalf("validate initialized target with deleted base ref: %v", err)
		}
	})
}

func TestLocalTargetInitializationRefusesPreexistingFeatureRef(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	root := definition.Workspace().RepositoryRoot()
	featureRef := definition.Workspace().FeatureRef()
	runTargetGitTest(t, root, "update-ref", featureRef, baseObjectHex(definition.Workspace().BaseCommit()))
	runtimeRoot := canonicalTestDirectory(t)
	_, err := initializeWorkspaceV2(
		t, runtimeRoot, definition, mustTime(t, "2026-09-03T12:01:00Z"),
	)
	if err == nil || !strings.Contains(err.Error(), "already exists before a recorded integration") {
		t.Fatalf("preexisting feature ref admission error = %v", err)
	}
	if _, statErr := os.Lstat(workspace.WorkspaceJournalPath(runtimeRoot)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected initialization created a journal: %v", statErr)
	}
}

func TestLocalTargetAdmissionUsesNarrowedProfile(t *testing.T) {
	t.Parallel()

	t.Run("allows sparse settings when the pinned object is available", func(t *testing.T) {
		t.Parallel()

		fixture := newDefinitionFixture(t)
		definition := mustDefinition(t, fixture.sources)
		root := definition.Workspace().RepositoryRoot()
		runTargetGitTest(t, root, "config", "core.sparseCheckout", "true")
		runTargetGitTest(t, root, "config", "index.sparse", "true")
		if _, err := workspace.ValidateLocalTarget(context.Background(), definition.Workspace()); err != nil {
			t.Fatalf("narrowed sparse admission: %v", err)
		}
	})

	t.Run("rejects bare repository", func(t *testing.T) {
		t.Parallel()

		source, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		bare := filepath.Join(canonicalTestDirectory(t), "target.git")
		runTargetGitTest(
			t, filepath.Dir(bare),
			"clone", "--quiet", "--bare", "--no-local", source, bare,
		)
		definition := localTargetDefinition(t, bare, base, "feature/bare-target")
		if _, err := workspace.ValidateLocalTarget(context.Background(), definition.Workspace()); err == nil ||
			!strings.Contains(err.Error(), "bare repositories") {
			t.Fatalf("bare repository admission error = %v", err)
		}
	})

	t.Run("rejects linked worktree", func(t *testing.T) {
		t.Parallel()

		root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		linked := filepath.Join(canonicalTestDirectory(t), "linked")
		runTargetGitTest(t, root, "worktree", "add", "--quiet", "--detach", linked, baseObjectHex(base))
		definition := localTargetDefinition(t, linked, base, "feature/linked-target")
		if _, err := workspace.ValidateLocalTarget(context.Background(), definition.Workspace()); err == nil ||
			!strings.Contains(err.Error(), "primary local worktree") {
			t.Fatalf("linked worktree admission error = %v", err)
		}
	})

	t.Run("rejects feature branch checked out elsewhere", func(t *testing.T) {
		t.Parallel()

		root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		branch := "feature/checked-out"
		runTargetGitTest(t, root, "branch", branch, baseObjectHex(base))
		checkedOut := filepath.Join(canonicalTestDirectory(t), "checked-out")
		runTargetGitTest(t, root, "worktree", "add", "--quiet", checkedOut, branch)
		definition := localTargetDefinition(t, root, base, branch)
		if _, err := workspace.ValidateLocalTarget(context.Background(), definition.Workspace()); err == nil ||
			!strings.Contains(err.Error(), "already checked out") {
			t.Fatalf("checked-out feature branch admission error = %v", err)
		}
	})

	t.Run("rejects external text conversion", func(t *testing.T) {
		t.Parallel()

		fixture := newDefinitionFixture(t)
		definition := mustDefinition(t, fixture.sources)
		root := definition.Workspace().RepositoryRoot()
		runTargetGitTest(t, root, "config", "diff.example.textconv", "/bin/false")
		if _, err := workspace.ValidateLocalTarget(context.Background(), definition.Workspace()); err == nil ||
			!strings.Contains(err.Error(), "external diff or text conversion") {
			t.Fatalf("text conversion admission error = %v", err)
		}
	})
}

func TestLocalTargetRejectsSymlinkedRoot(t *testing.T) {
	t.Parallel()

	root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
	link := filepath.Join(canonicalTestDirectory(t), "target-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	definition := localTargetDefinition(t, link, base, "feature/symlinked-target")
	if _, err := workspace.ValidateLocalTarget(context.Background(), definition.Workspace()); err == nil ||
		!strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("symlink target admission error = %v", err)
	}
}

func TestLocalTargetOperationsDoNotInvokeHooksOrConfiguredHelpers(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	root := definition.Workspace().RepositoryRoot()
	marker := filepath.Join(root, "hostile-marker")
	hook := filepath.Join(root, ".git", "hooks", "reference-transaction")
	script := "#!/bin/sh\n: > " + shellSingleQuote(marker) + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "config", "credential.helper", "!f() { : > "+shellSingleQuote(marker)+"; }; f")
	runTargetGitTest(t, root, "remote", "add", "hostile", "ext::sh -c ': > "+shellSingleQuote(marker)+"'")
	if _, err := initializeWorkspaceV2(
		t, canonicalTestDirectory(t), definition, mustTime(t, "2026-09-03T12:02:00Z"),
	); err != nil {
		t.Fatalf("initialize with inert hostile helpers: %v", err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local target operation invoked hostile program: %v", err)
	}
}

func localTargetDefinition(
	t *testing.T,
	root string,
	base workspace.GitObjectID,
	branch string,
) workspace.EffectiveWorkspaceDefinition {
	t.Helper()
	fixture := newDefinitionFixture(t)
	lines := strings.Split(string(fixture.sources.Workspace.Bytes), "\n")
	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "  root: "):
			lines[index] = "  root: " + root
		case strings.HasPrefix(line, "base_commit: "):
			lines[index] = "base_commit: " + base.String()
		case strings.HasPrefix(line, "feature_branch: "):
			lines[index] = "feature_branch: " + branch
		}
	}
	fixture.sources.Workspace.Bytes = []byte(strings.Join(lines, "\n"))
	return mustDefinition(t, fixture.sources)
}

func canonicalTestDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func baseObjectHex(object workspace.GitObjectID) string {
	return strings.TrimPrefix(object.String(), string(object.Algorithm())+":")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
