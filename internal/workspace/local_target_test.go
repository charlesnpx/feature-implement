package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestLocalTargetValidationAndInitializationBindPrimaryAndLinkedWorktrees(
	t *testing.T,
) {
	tests := []struct {
		name      string
		algorithm workspace.GitHashAlgorithm
		linked    bool
	}{
		{name: "primary-sha1", algorithm: workspace.GitHashSHA1},
		{name: "primary-sha256", algorithm: workspace.GitHashSHA256},
		{name: "linked-sha1", algorithm: workspace.GitHashSHA1, linked: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, base := initializeTargetRepository(t, test.algorithm)
			targetRoot := root
			if test.linked {
				targetRoot = filepath.Join(canonicalTestDirectory(t), "linked")
				runTargetGitTest(
					t, root,
					"worktree", "add", "--quiet", "--detach",
					targetRoot, baseObjectHex(base),
				)
			}
			definition := localTargetDefinition(
				t, targetRoot, base, "feature/local-target-binding",
			)
			binding, err := workspace.ValidateLocalTarget(
				context.Background(), definition.Workspace(),
			)
			if err != nil {
				t.Fatalf("validate local target: %v", err)
			}
			if binding.Root() != targetRoot ||
				binding.BaseCommit() != base ||
				binding.ObjectFormat() != test.algorithm ||
				binding.LinkedWorktree() != test.linked ||
				binding.RootIdentity().Inode == 0 ||
				binding.CommonIdentity().Inode == 0 ||
				binding.GitDirectoryIdentity().Inode == 0 {
				t.Fatalf("local target binding = %#v", binding)
			}

			runtimeRoot := canonicalTestDirectory(t)
			result, err := workspace.InitializeWorkspaceV2(
				runtimeRoot,
				definition,
				mustTime(t, "2026-07-24T12:00:00Z"),
			)
			if err != nil {
				t.Fatalf("initialize local target: %v", err)
			}
			assertLocalTargetInitializationJournal(t, result.Snapshot())
			target, ok := result.Runtime().LocalTarget()
			if !ok || !target.Created() ||
				target.Binding().Digest() != binding.Digest() ||
				target.CreatedHead() != base {
				t.Fatalf("runtime local target = %#v", target)
			}
			if got := strings.TrimSpace(runTargetGitTest(
				t, root, "rev-parse", "refs/heads/feature/local-target-binding",
			)); got != baseObjectHex(base) {
				t.Fatalf("created feature ref = %s, want %s", got, base)
			}
		})
	}
}

func TestLocalTargetInitializationRecoversExactFeatureRefBoundaries(t *testing.T) {
	for _, faultPoint := range []workspace.LocalTargetInitializationFaultPoint{
		workspace.LocalTargetFaultAfterIntentSynced,
		workspace.LocalTargetFaultAfterRefUpdate,
		workspace.LocalTargetFaultBeforeCompletion,
	} {
		t.Run(string(faultPoint), func(t *testing.T) {
			fixture := newDefinitionFixture(t)
			definition := mustDefinition(t, fixture.sources)
			runtimeRoot := canonicalTestDirectory(t)
			fired := false
			_, err := workspace.InitializeWorkspaceV2WithOptions(
				context.Background(),
				runtimeRoot,
				definition,
				mustTime(t, "2026-07-24T12:10:00Z"),
				workspace.WorkspaceInitializationOptions{
					TargetFault: func(
						point workspace.LocalTargetInitializationFaultPoint,
					) error {
						if !fired && point == faultPoint {
							fired = true
							return errors.New("injected local target crash")
						}
						return nil
					},
				},
			)
			if err == nil || !fired ||
				!strings.Contains(err.Error(), string(faultPoint)) {
				t.Fatalf("initialization fault = %v fired=%t", err, fired)
			}
			recovered, err := workspace.InitializeWorkspaceV2(
				runtimeRoot,
				definition,
				mustTime(t, "2026-07-24T12:11:00Z"),
			)
			if err != nil {
				t.Fatalf("recover local target initialization: %v", err)
			}
			assertLocalTargetInitializationJournal(t, recovered.Snapshot())
			target, ok := recovered.Runtime().LocalTarget()
			if !ok || !target.Created() ||
				target.CreatedHead() != definition.Workspace().BaseCommit() {
				t.Fatalf("recovered local target = %#v", target)
			}
		})
	}
}

func TestLocalTargetInitializationRefRaceIsNotAdopted(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	root := definition.Workspace().RepositoryRoot()
	runtimeRoot := canonicalTestDirectory(t)
	raced := false
	_, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:20:00Z"),
		workspace.WorkspaceInitializationOptions{
			TargetFault: func(
				point workspace.LocalTargetInitializationFaultPoint,
			) error {
				if point == workspace.LocalTargetFaultBeforeRefUpdate && !raced {
					raced = true
					runTargetGitTest(
						t, root,
						"update-ref",
						definition.Workspace().FeatureRef(),
						baseObjectHex(definition.Workspace().BaseCommit()),
					)
				}
				return nil
			},
		},
	)
	if err == nil || !raced ||
		!strings.Contains(err.Error(), "expected-absent CAS") {
		t.Fatalf("feature-ref race error = %v raced=%t", err, raced)
	}
	_, err = workspace.InitializeWorkspaceV2(
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:21:00Z"),
	)
	if err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
		t.Fatalf("unrelated exact ref recovery error = %v", err)
	}
}

func TestLocalTargetBaseMovementIsInformationalAfterInitialization(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	runtimeRoot := canonicalTestDirectory(t)
	first, err := workspace.InitializeWorkspaceV2(
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:30:00Z"),
	)
	if err != nil {
		t.Fatal(err)
	}
	root := definition.Workspace().RepositoryRoot()
	if err := os.WriteFile(
		filepath.Join(root, "later.txt"), []byte("later base movement\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "add", "--", "later.txt")
	runTargetGitTest(
		t, root,
		"-c", "user.name=Feature Implement Test",
		"-c", "user.email=feature-implement@localhost",
		"commit", "--quiet", "-m", "move base later",
	)
	if _, err := workspace.ValidateLocalTarget(
		context.Background(), definition.Workspace(),
	); err == nil || !strings.Contains(err.Error(), "not pinned base_commit") {
		t.Fatalf("moved base validation error = %v", err)
	}
	retried, err := workspace.InitializeWorkspaceV2(
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:31:00Z"),
	)
	if err != nil {
		t.Fatalf("base movement blocked initialized runtime: %v", err)
	}
	if retried.Snapshot().Head() != first.Snapshot().Head() ||
		len(retried.Snapshot().Records()) != 3 {
		t.Fatalf("base movement changed initialization history")
	}
}

func TestLocalTargetRejectsFeatureNamespaceAndCheckedOutOwnership(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, workspace.GitObjectID, string)
		want  string
	}{
		{
			name: "ancestor namespace",
			setup: func(
				t *testing.T,
				root string,
				base workspace.GitObjectID,
				_ string,
			) {
				runTargetGitTest(
					t, root, "update-ref", "refs/heads/feature",
					baseObjectHex(base),
				)
			},
			want: "ancestor",
		},
		{
			name: "unrelated exact ref",
			setup: func(
				t *testing.T,
				root string,
				base workspace.GitObjectID,
				branch string,
			) {
				runTargetGitTest(
					t, root, "update-ref", "refs/heads/"+branch,
					baseObjectHex(base),
				)
			},
			want: "without a durable creation intent",
		},
		{
			name: "checked out elsewhere",
			setup: func(
				t *testing.T,
				root string,
				base workspace.GitObjectID,
				branch string,
			) {
				runTargetGitTest(
					t, root, "branch", branch, baseObjectHex(base),
				)
				worktree := filepath.Join(
					canonicalTestDirectory(t), "checked-out",
				)
				runTargetGitTest(
					t, root, "worktree", "add", "--quiet",
					worktree, branch,
				)
			},
			want: "already checked out",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, base := initializeTargetRepository(
				t, workspace.GitHashSHA1,
			)
			branch := "feature/owned-target"
			test.setup(t, root, base, branch)
			definition := localTargetDefinition(t, root, base, branch)
			_, err := workspace.ValidateLocalTarget(
				context.Background(), definition.Workspace(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("feature ownership error = %v", err)
			}
		})
	}
}

func TestLocalTargetRejectsUnsupportedRepositoryProfiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, workspace.GitObjectID)
		want  string
	}{
		{
			name: "promisor",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "remote.origin.promisor", "true",
				)
			},
			want: "partial/promisor",
		},
		{
			name: "partial clone extension",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config",
					"core.repositoryFormatVersion", "1",
				)
				runTargetGitTest(
					t, root, "config",
					"extensions.partialClone", "origin",
				)
			},
			want: "partial-clone extension",
		},
		{
			name: "promisor pack marker",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				if err := os.WriteFile(
					filepath.Join(
						root,
						".git",
						"objects",
						"pack",
						"pack-deadbeef.promisor",
					),
					nil,
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "promisor pack metadata",
		},
		{
			name: "alternates",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				path := filepath.Join(root, ".git", "objects", "info", "alternates")
				if err := os.WriteFile(path, []byte("/tmp/objects\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "alternate object database",
		},
		{
			name: "replacement ref",
			setup: func(t *testing.T, root string, base workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "update-ref",
					"refs/replace/"+baseObjectHex(base),
					baseObjectHex(base),
				)
			},
			want: "replacement refs",
		},
		{
			name: "graft",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				if err := os.WriteFile(
					filepath.Join(root, ".git", "info", "grafts"),
					[]byte("malformed graft\n"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "Git graft",
		},
		{
			name: "sparse",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "core.sparseCheckout", "true",
				)
			},
			want: "sparse checkout",
		},
		{
			name: "submodule config",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "submodule.hostile.url",
					"https://example.invalid/repository",
				)
			},
			want: "submodule configuration",
		},
		{
			name: "active filter",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "filter.hostile.process",
					"false",
				)
			},
			want: "active Git filter",
		},
		{
			name: "external attributes",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "core.attributesFile",
					"/tmp/hostile-attributes",
				)
			},
			want: "external Git attributes",
		},
		{
			name: "fsmonitor",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "core.fsmonitor",
					"hostile-fsmonitor",
				)
			},
			want: "active fsmonitor",
		},
		{
			name: "external diff",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "diff.hostile.command",
					"false",
				)
			},
			want: "external diff/text conversion",
		},
		{
			name: "configuration include",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "include.path",
					"/tmp/hostile-git-config",
				)
			},
			want: "configuration includes",
		},
		{
			name: "shallow",
			setup: func(t *testing.T, root string, base workspace.GitObjectID) {
				if err := os.WriteFile(
					filepath.Join(root, ".git", "shallow"),
					[]byte(baseObjectHex(base)+"\n"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "shallow repository",
		},
		{
			name: "missing object",
			setup: func(t *testing.T, root string, base workspace.GitObjectID) {
				raw := baseObjectHex(base)
				if err := os.Remove(filepath.Join(
					root, ".git", "objects", raw[:2], raw[2:],
				)); err != nil {
					t.Fatal(err)
				}
			},
			want: "object database is incomplete or invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, base := initializeTargetRepository(
				t, workspace.GitHashSHA1,
			)
			test.setup(t, root, base)
			definition := localTargetDefinition(
				t, root, base, "feature/profile-test",
			)
			_, err := workspace.ValidateLocalTarget(
				context.Background(), definition.Workspace(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsupported profile error = %v", err)
			}
		})
	}
}

func TestLocalTargetRejectsBareRepository(t *testing.T) {
	source, base := initializeTargetRepository(t, workspace.GitHashSHA1)
	parent := canonicalTestDirectory(t)
	bare := filepath.Join(parent, "target.git")
	runTargetGitTest(
		t, parent,
		"clone", "--quiet", "--bare", "--no-local", source, bare,
	)
	definition := localTargetDefinition(
		t, bare, base, "feature/bare-target",
	)
	_, err := workspace.ValidateLocalTarget(
		context.Background(), definition.Workspace(),
	)
	if err == nil || !strings.Contains(err.Error(), "bare repositories") {
		t.Fatalf("bare repository error = %v", err)
	}
}

func TestLocalTargetRejectsUnsupportedPinnedBaseTreeProfiles(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		want    string
	}{
		{
			name: "gitmodules",
			path: ".gitmodules",
			content: `[submodule "hostile"]
	path = dependencies/hostile
	url = https://example.invalid/hostile.git
`,
			want: "submodules are not supported",
		},
		{
			name:    "nested nonempty gitattributes",
			path:    "config/.gitattributes",
			content: "*.txt filter=hostile\n",
			want:    "repository-defined Git attributes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, _ := initializeTargetRepository(
				t, workspace.GitHashSHA1,
			)
			if err := os.MkdirAll(
				filepath.Dir(filepath.Join(root, test.path)),
				0o700,
			); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(root, test.path),
				[]byte(test.content),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			runTargetGitTest(t, root, "add", "--", test.path)
			runTargetGitTest(
				t, root,
				"-c", "user.name=Feature Implement Test",
				"-c", "user.email=feature-implement@localhost",
				"commit", "--quiet", "-m", "add unsupported base metadata",
			)
			base := targetHead(t, root, workspace.GitHashSHA1)
			definition := localTargetDefinition(
				t, root, base, "feature/base-tree-profile",
			)
			_, err := workspace.ValidateLocalTarget(
				context.Background(), definition.Workspace(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsupported pinned-base tree error = %v", err)
			}
		})
	}
}

func TestLocalTargetRejectsEscapingSymlinkAndRepositoryReplacement(t *testing.T) {
	t.Run("escaping symlink", func(t *testing.T) {
		root, _ := initializeTargetRepository(t, workspace.GitHashSHA1)
		if err := os.Symlink("../../outside", filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
		runTargetGitTest(t, root, "add", "--", "escape")
		runTargetGitTest(
			t, root,
			"-c", "user.name=Feature Implement Test",
			"-c", "user.email=feature-implement@localhost",
			"commit", "--quiet", "-m", "add escaping symlink",
		)
		base := targetHead(t, root, workspace.GitHashSHA1)
		definition := localTargetDefinition(
			t, root, base, "feature/symlink-test",
		)
		_, err := workspace.ValidateLocalTarget(
			context.Background(), definition.Workspace(),
		)
		if err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("escaping symlink error = %v", err)
		}
	})

	t.Run("linked worktree administration symlink", func(t *testing.T) {
		root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		linked := filepath.Join(canonicalTestDirectory(t), "linked")
		runTargetGitTest(
			t, root,
			"worktree", "add", "--quiet", "--detach",
			linked, baseObjectHex(base),
		)
		administration := filepath.Join(linked, ".git")
		moved := filepath.Join(linked, ".git-administration")
		if err := os.Rename(administration, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Base(moved),
			administration,
		); err != nil {
			t.Fatal(err)
		}
		definition := localTargetDefinition(
			t, linked, base, "feature/symlinked-administration",
		)
		_, err := workspace.ValidateLocalTarget(
			context.Background(), definition.Workspace(),
		)
		if err == nil ||
			!strings.Contains(err.Error(), "symlink") {
			t.Fatalf("linked worktree administration symlink error = %v", err)
		}
	})

	t.Run("repository replacement", func(t *testing.T) {
		fixture := newDefinitionFixture(t)
		definition := mustDefinition(t, fixture.sources)
		runtimeRoot := canonicalTestDirectory(t)
		if _, err := workspace.InitializeWorkspaceV2(
			runtimeRoot,
			definition,
			mustTime(t, "2026-07-24T12:40:00Z"),
		); err != nil {
			t.Fatal(err)
		}
		root := definition.Workspace().RepositoryRoot()
		moved := root + "-moved"
		if err := os.Rename(root, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		runTargetGitTest(
			t, root,
			"init", "--quiet", "--initial-branch=main",
			"--object-format=sha1", ".",
		)
		if err := os.WriteFile(
			filepath.Join(root, "seed.txt"), []byte("replacement\n"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		runTargetGitTest(t, root, "add", "--", "seed.txt")
		runTargetGitTest(
			t, root,
			"-c", "user.name=Feature Implement Test",
			"-c", "user.email=feature-implement@localhost",
			"commit", "--quiet", "-m", "replacement",
		)
		_, err := workspace.InitializeWorkspaceV2(
			runtimeRoot,
			definition,
			mustTime(t, "2026-07-24T12:41:00Z"),
		)
		if err == nil {
			t.Fatalf("repository replacement error = %v", err)
		}
	})
}

func TestLocalTargetOperationsDoNotInvokeHooksCredentialsOrProtocols(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	root := definition.Workspace().RepositoryRoot()
	marker := filepath.Join(root, "hostile-marker")
	hook := filepath.Join(root, ".git", "hooks", "reference-transaction")
	script := "#!/bin/sh\n: > " + shellSingleQuote(marker) + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(
		t, root, "config", "credential.helper",
		"!f() { : > "+shellSingleQuote(marker)+"; }; f",
	)
	runTargetGitTest(
		t, root, "remote", "add", "hostile",
		"ext::sh -c ': > "+shellSingleQuote(marker)+"'",
	)
	if _, err := workspace.InitializeWorkspaceV2(
		canonicalTestDirectory(t),
		definition,
		mustTime(t, "2026-07-24T12:50:00Z"),
	); err != nil {
		t.Fatalf("initialize with inert hostile programs: %v", err)
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

func targetHead(
	t *testing.T,
	root string,
	algorithm workspace.GitHashAlgorithm,
) workspace.GitObjectID {
	t.Helper()
	raw := strings.TrimSpace(runTargetGitTest(t, root, "rev-parse", "HEAD"))
	object, err := workspace.ParseGitObjectID(string(algorithm) + ":" + raw)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func baseObjectHex(object workspace.GitObjectID) string {
	return strings.TrimPrefix(
		object.String(), string(object.Algorithm())+":",
	)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
