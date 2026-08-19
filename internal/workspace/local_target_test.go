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

func TestLocalTargetValidationAndInitializationBindPrimaryAndLinkedWorktrees(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name      string
		algorithm workspace.GitHashAlgorithm
		linked    bool
	}{
		{name: "primary-sha1", algorithm: workspace.GitHashSHA1},
		{name: "primary-sha256", algorithm: workspace.GitHashSHA256},
		{name: "linked-sha1", algorithm: workspace.GitHashSHA1, linked: true},
		{name: "linked-sha256", algorithm: workspace.GitHashSHA256, linked: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireFullSuiteCase(
				t,
				test.name == "primary-sha1" || test.name == "linked-sha256",
				"Git object-format and worktree-shape cross-product",
			)

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
				binding.LinkedWorktree() != test.linked {
				t.Fatalf("local target binding = %#v", binding)
			}

			runtimeRoot := canonicalTestDirectory(t)
			result, err := initializeWorkspaceV2(t,
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
	t.Parallel()

	for _, faultPoint := range []workspace.LocalTargetInitializationFaultPoint{
		workspace.LocalTargetFaultAfterIntentSynced,
		workspace.LocalTargetFaultAfterRefUpdate,
		workspace.LocalTargetFaultBeforeCompletion,
	} {
		t.Run(string(faultPoint), func(t *testing.T) {
			requireFullSuiteCase(
				t,
				faultPoint == workspace.LocalTargetFaultAfterIntentSynced ||
					faultPoint == workspace.LocalTargetFaultAfterRefUpdate,
				"intermediate feature-ref creation boundary",
			)

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
					WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
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
			recovered, err := initializeWorkspaceV2(t,
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
	t.Parallel()

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
			WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
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
	_, err = initializeWorkspaceV2(t,
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:21:00Z"),
	)
	if err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
		t.Fatalf("unrelated exact ref recovery error = %v", err)
	}
}

func TestLocalTargetRefCreationDoesNotMutateReplacementGitDirectory(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "Git directory replacement permutation")

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	root := definition.Workspace().RepositoryRoot()
	replacement, _ := initializeTargetRepository(t, workspace.GitHashSHA1)
	runtimeRoot := canonicalTestDirectory(t)
	originalGit := filepath.Join(canonicalTestDirectory(t), "original.git")
	replaced := false
	_, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:25:00Z"),
		workspace.WorkspaceInitializationOptions{
			WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
			TargetFault: func(
				point workspace.LocalTargetInitializationFaultPoint,
			) error {
				if point != workspace.LocalTargetFaultBeforeRefUpdate || replaced {
					return nil
				}
				replaced = true
				if err := os.Rename(filepath.Join(root, ".git"), originalGit); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(
					filepath.Join(replacement, ".git"),
					filepath.Join(root, ".git"),
				); err != nil {
					t.Fatal(err)
				}
				return nil
			},
		},
	)
	if err == nil || !replaced {
		t.Fatalf("Git-directory replacement error = %v replaced=%t", err, replaced)
	}
	branch := definition.Workspace().FeatureBranch()
	if got := strings.TrimSpace(runTargetGitTest(
		t, root, "branch", "--list", branch,
	)); got != "" {
		t.Fatalf("replacement repository received feature branch %q", got)
	}
	if got := strings.TrimSpace(runTargetGitTest(
		t, root, "--git-dir="+originalGit, "branch", "--list", branch,
	)); got != "" {
		t.Fatalf("retained original repository was mutated after replacement %q", got)
	}
}

func TestLocalTargetRefCreationDoesNotFollowReplacedCommonDirectory(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "Git common-directory replacement permutation")

	root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
	linked := filepath.Join(canonicalTestDirectory(t), "linked")
	runTargetGitTest(
		t, root,
		"worktree", "add", "--quiet", "--detach",
		linked, baseObjectHex(base),
	)
	definition := localTargetDefinition(
		t, linked, base, "feature/replaced-common-directory",
	)
	replacement, _ := initializeTargetRepository(t, workspace.GitHashSHA1)
	gitDirectory := strings.TrimSpace(runTargetGitTest(
		t, linked, "rev-parse", "--absolute-git-dir",
	))
	runtimeRoot := canonicalTestDirectory(t)
	replaced := false
	_, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:25:30Z"),
		workspace.WorkspaceInitializationOptions{
			WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
			TargetFault: func(
				point workspace.LocalTargetInitializationFaultPoint,
			) error {
				if point != workspace.LocalTargetFaultBeforeRefUpdate || replaced {
					return nil
				}
				replaced = true
				return os.WriteFile(
					filepath.Join(gitDirectory, "commondir"),
					[]byte(filepath.Join(replacement, ".git")+"\n"),
					0o600,
				)
			},
		},
	)
	if err == nil || !replaced || !strings.Contains(
		err.Error(), "common-directory administration points to",
	) {
		t.Fatalf("common-directory replacement error = %v replaced=%t", err, replaced)
	}
	branch := definition.Workspace().FeatureBranch()
	if got := strings.TrimSpace(runTargetGitTest(
		t, replacement, "branch", "--list", branch,
	)); got != "" {
		t.Fatalf("replacement common repository received feature branch %q", got)
	}
	if got := strings.TrimSpace(runTargetGitTest(
		t, root, "branch", "--list", branch,
	)); got != "" {
		t.Fatalf("retained common repository was mutated after replacement %q", got)
	}
}

func TestLocalTargetRefCreationRejectsExternalRefAndReflogStorage(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "external Git ref-storage matrix")

	tests := []struct {
		name  string
		setup func(*testing.T, string, string, string) (string, []byte)
	}{
		{
			name: "ref parent symlink",
			setup: func(
				t *testing.T,
				commonDirectory string,
				_ string,
				externalDirectory string,
			) (string, []byte) {
				parent := filepath.Join(
					commonDirectory, "refs", "heads", "feature",
				)
				if err := os.Symlink(externalDirectory, parent); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(
					externalDirectory, "external-ref-storage",
				), nil
			},
		},
		{
			name: "reflog parent symlink",
			setup: func(
				t *testing.T,
				commonDirectory string,
				_ string,
				externalDirectory string,
			) (string, []byte) {
				parent := filepath.Join(
					commonDirectory, "logs", "refs", "heads", "feature",
				)
				if err := os.Symlink(externalDirectory, parent); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(
					externalDirectory, "external-ref-storage",
				), nil
			},
		},
		{
			name: "reflog hard link",
			setup: func(
				t *testing.T,
				commonDirectory string,
				featureRef string,
				externalDirectory string,
			) (string, []byte) {
				reflog := filepath.Join(
					commonDirectory, "logs", filepath.FromSlash(featureRef),
				)
				if err := os.MkdirAll(filepath.Dir(reflog), 0o700); err != nil {
					t.Fatal(err)
				}
				content := []byte("external victim must remain unchanged\n")
				victim := filepath.Join(externalDirectory, "victim")
				if err := os.WriteFile(victim, content, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(victim, reflog); err != nil {
					t.Fatal(err)
				}
				return victim, content
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
			definition := localTargetDefinition(
				t, root, base, "feature/external-ref-storage",
			)
			commonDirectory := filepath.Join(root, ".git")
			externalDirectory := canonicalTestDirectory(t)
			runtimeRoot := canonicalTestDirectory(t)
			var victim string
			var original []byte
			changed := false
			_, err := workspace.InitializeWorkspaceV2WithOptions(
				context.Background(),
				runtimeRoot,
				definition,
				mustTime(t, "2026-07-24T12:25:40Z"),
				workspace.WorkspaceInitializationOptions{
					WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
					TargetFault: func(
						point workspace.LocalTargetInitializationFaultPoint,
					) error {
						if point != workspace.LocalTargetFaultBeforeRefUpdate ||
							changed {
							return nil
						}
						changed = true
						victim, original = test.setup(
							t,
							commonDirectory,
							definition.Workspace().FeatureRef(),
							externalDirectory,
						)
						return nil
					},
				},
			)
			if err == nil || !changed ||
				!strings.Contains(err.Error(), "storage") {
				t.Fatalf(
					"external ref storage error = %v changed=%t",
					err, changed,
				)
			}
			content, readErr := os.ReadFile(victim)
			if len(original) == 0 {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf(
						"external ref storage was created: %v %q",
						readErr, content,
					)
				}
			} else if readErr != nil || string(content) != string(original) {
				t.Fatalf(
					"external reflog victim changed: %v %q",
					readErr, content,
				)
			}
			branch := definition.Workspace().FeatureBranch()
			if got := strings.TrimSpace(runTargetGitTest(
				t, root, "branch", "--list", branch,
			)); got != "" {
				t.Fatalf("repository received feature branch %q", got)
			}
		})
	}
}

func TestLocalTargetRefCreationRejectsUnsafeStorageAncestorPermissions(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "Git storage-permission matrix")

	tests := []struct {
		name     string
		ancestor func(string) string
	}{
		{
			name: "ref",
			ancestor: func(featureRef string) string {
				return filepath.Dir(filepath.FromSlash(featureRef))
			},
		},
		{
			name: "reflog",
			ancestor: func(featureRef string) string {
				return filepath.Dir(
					filepath.Join("logs", filepath.FromSlash(featureRef)),
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
			definition := localTargetDefinition(
				t, root, base, "feature/unsafe-storage-ancestor",
			)
			commonDirectory := filepath.Join(root, ".git")
			featureRef := definition.Workspace().FeatureRef()
			refPath := filepath.Join(
				commonDirectory, filepath.FromSlash(featureRef),
			)
			reflogPath := filepath.Join(
				commonDirectory, "logs", filepath.FromSlash(featureRef),
			)
			externalDirectory := canonicalTestDirectory(t)
			externalVictim := filepath.Join(externalDirectory, "victim")
			original := []byte("external victim must remain unchanged\n")
			if err := os.WriteFile(externalVictim, original, 0o600); err != nil {
				t.Fatal(err)
			}
			runtimeRoot := canonicalTestDirectory(t)
			changed := false
			_, err := workspace.InitializeWorkspaceV2WithOptions(
				context.Background(),
				runtimeRoot,
				definition,
				mustTime(t, "2026-07-24T12:25:50Z"),
				workspace.WorkspaceInitializationOptions{
					WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
					TargetFault: func(
						point workspace.LocalTargetInitializationFaultPoint,
					) error {
						if point != workspace.LocalTargetFaultBeforeRefUpdate ||
							changed {
							return nil
						}
						changed = true
						ancestor := filepath.Join(
							commonDirectory, test.ancestor(featureRef),
						)
						if err := os.MkdirAll(ancestor, 0o700); err != nil {
							return err
						}
						return os.Chmod(ancestor, 0o777)
					},
				},
			)
			if err == nil || !changed ||
				!strings.Contains(err.Error(), "permissions") ||
				!strings.Contains(err.Error(), "non-owner writes") {
				t.Fatalf(
					"unsafe %s ancestor error = %v changed=%t",
					test.name, err, changed,
				)
			}
			for _, candidate := range []string{refPath, reflogPath} {
				if _, statErr := os.Lstat(candidate); !errors.Is(
					statErr, os.ErrNotExist,
				) {
					t.Fatalf(
						"unsafe %s ancestor allowed target write %s: %v",
						test.name, candidate, statErr,
					)
				}
			}
			content, readErr := os.ReadFile(externalVictim)
			if readErr != nil || string(content) != string(original) {
				t.Fatalf(
					"unsafe %s ancestor allowed external write: %v %q",
					test.name, readErr, content,
				)
			}
		})
	}
}

func TestLocalTargetCreatesSecureFeatureStorageAncestors(t *testing.T) {
	t.Parallel()

	root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
	definition := localTargetDefinition(
		t, root, base, "feature/secure-storage-ancestors",
	)
	if _, err := initializeWorkspaceV2(t,
		canonicalTestDirectory(t),
		definition,
		mustTime(t, "2026-07-24T12:25:55Z"),
	); err != nil {
		t.Fatal(err)
	}
	commonDirectory := filepath.Join(root, ".git")
	featureRef := definition.Workspace().FeatureRef()
	for _, ancestor := range []string{
		filepath.Dir(filepath.Join(
			commonDirectory, filepath.FromSlash(featureRef),
		)),
		filepath.Dir(filepath.Join(
			commonDirectory, "logs", filepath.FromSlash(featureRef),
		)),
	} {
		info, err := os.Stat(ancestor)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf(
				"created feature storage ancestor %s permissions = %04o",
				ancestor, info.Mode().Perm(),
			)
		}
	}
}

func TestLocalTargetInitializationReadinessBarrierAtEveryFault(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive feature-ref readiness-boundary matrix")

	for _, faultPoint := range []workspace.LocalTargetInitializationFaultPoint{
		workspace.LocalTargetFaultAfterIntentSynced,
		workspace.LocalTargetFaultBeforeRefUpdate,
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
				mustTime(t, "2026-07-24T12:26:00Z"),
				workspace.WorkspaceInitializationOptions{
					WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
					TargetFault: func(
						point workspace.LocalTargetInitializationFaultPoint,
					) error {
						if !fired && point == faultPoint {
							fired = true
							return errors.New("injected readiness boundary")
						}
						return nil
					},
				},
			)
			if err == nil || !fired {
				t.Fatalf("initialization fault = %v fired=%t", err, fired)
			}
			journal, err := workspace.OpenWorkspaceJournal(
				runtimeRoot, workspace.JournalReadWrite,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			snapshot, err := journal.ReadSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			report, err := workspace.RebuildWorkspaceView(
				snapshot, definition,
			)
			if err != nil {
				t.Fatalf(
					"incomplete initialization report: %v", err,
				)
			}
			if report.Target.Ready ||
				!containsCompletionBlocker(
					report.Completion.Blockers,
					"local_effect:feature_ref_creation_pending",
				) ||
				!containsCompletionBlocker(
					report.Gates.CompletionBlockers,
					"local_effect:feature_ref_creation_pending",
				) {
				t.Fatalf(
					"incomplete initialization report target=%#v completion=%#v gates=%#v",
					report.Target,
					report.Completion,
					report.Gates,
				)
			}
			goal, err := workspace.NewGoalBinding(
				workspace.MustID("readiness-goal"),
				workspace.GoalScopeMergeUnit,
			)
			if err != nil {
				t.Fatal(err)
			}
			fakeGit := &fakeAttemptGit{}
			if _, err := workspace.ReserveAttempt(
				context.Background(),
				journal,
				definition,
				fakeGit,
				workspace.ReserveAttemptRequest{
					MergeUnit: mustMergeUnitReference(
						t, "alpha-plan", "unit-one",
					),
					AttemptNumber: 1,
					Goal:          goal,
					OccurredAt: mustTime(
						t, "2026-07-24T12:26:01Z",
					),
				},
			); err == nil || !strings.Contains(err.Error(), "feature_ref_created") {
				t.Fatalf("incomplete initialization attempt error = %v", err)
			}
			if fakeGit.prepareCalls != 0 || fakeGit.createCalls != 0 {
				t.Fatal("incomplete initialization reached attempt Git mutation")
			}
		})
	}
}

func TestLocalTargetBaseMustRemainPinnedAtEveryPreCompletionFault(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "exhaustive feature-ref base-drift matrix")

	for _, faultPoint := range []workspace.LocalTargetInitializationFaultPoint{
		workspace.LocalTargetFaultAfterIntentSynced,
		workspace.LocalTargetFaultBeforeRefUpdate,
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
				mustTime(t, "2026-07-24T12:27:00Z"),
				workspace.WorkspaceInitializationOptions{
					WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
					TargetFault: func(
						point workspace.LocalTargetInitializationFaultPoint,
					) error {
						if !fired && point == faultPoint {
							fired = true
							return errors.New("injected base-pin boundary")
						}
						return nil
					},
				},
			)
			if err == nil || !fired {
				t.Fatalf("initialization fault = %v fired=%t", err, fired)
			}
			root := definition.Workspace().RepositoryRoot()
			movedFile := filepath.Join(
				root, "base-moved-"+string(faultPoint)+".txt",
			)
			if err := os.WriteFile(
				movedFile, []byte("move base\n"), 0o600,
			); err != nil {
				t.Fatal(err)
			}
			runTargetGitTest(t, root, "add", "--", filepath.Base(movedFile))
			runTargetGitTest(
				t, root,
				"-c", "user.name=Feature Implement Test",
				"-c", "user.email=feature-implement@localhost",
				"commit", "--quiet", "-m", "move base before completion",
			)
			_, err = initializeWorkspaceV2(t,
				runtimeRoot,
				definition,
				mustTime(t, "2026-07-24T12:27:01Z"),
			)
			if err == nil || !strings.Contains(err.Error(), "not pinned base_commit") {
				t.Fatalf("moved pre-completion base error = %v", err)
			}
			snapshot, err := workspace.ReadWorkspaceJournalSnapshot(runtimeRoot)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			target, ok := runtime.LocalTarget()
			if !ok || target.Created() {
				t.Fatalf("moved base completed local target = %#v", target)
			}
		})
	}
}

func TestLocalTargetBaseMovementIsInformationalAfterInitialization(t *testing.T) {
	t.Parallel()

	fixture := newDefinitionFixture(t)
	definition := mustDefinition(t, fixture.sources)
	runtimeRoot := canonicalTestDirectory(t)
	first, err := initializeWorkspaceV2(t,
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
	retried, err := initializeWorkspaceV2(t,
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

func TestLocalTargetInitializationRejectsDiscoveredRootOverlap(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "repository root-overlap permutation")

	tests := []struct {
		name  string
		setup func(*testing.T) (workspace.EffectiveWorkspaceDefinition, string)
	}{
		{
			name: "linked target runtime inside common directory",
			setup: func(t *testing.T) (
				workspace.EffectiveWorkspaceDefinition,
				string,
			) {
				root, base := initializeTargetRepository(
					t, workspace.GitHashSHA1,
				)
				linked := filepath.Join(canonicalTestDirectory(t), "linked")
				runTargetGitTest(
					t, root,
					"worktree", "add", "--quiet", "--detach",
					linked, baseObjectHex(base),
				)
				return localTargetDefinition(
						t, linked, base, "feature/common-overlap",
					),
					filepath.Join(root, ".git", "unsafe-runtime")
			},
		},
		{
			name: "primary target runtime inside registered worktree",
			setup: func(t *testing.T) (
				workspace.EffectiveWorkspaceDefinition,
				string,
			) {
				root, base := initializeTargetRepository(
					t, workspace.GitHashSHA1,
				)
				linked := filepath.Join(canonicalTestDirectory(t), "linked")
				runTargetGitTest(
					t, root,
					"worktree", "add", "--quiet", "--detach",
					linked, baseObjectHex(base),
				)
				return localTargetDefinition(
						t, root, base, "feature/registered-overlap",
					),
					filepath.Join(linked, "unsafe-runtime")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition, runtimeRoot := test.setup(t)
			_, err := initializeWorkspaceV2(t,
				runtimeRoot,
				definition,
				mustTime(t, "2026-07-24T12:35:00Z"),
			)
			if err == nil || !strings.Contains(
				err.Error(), "unsafe workspace root overlap",
			) {
				t.Fatalf("discovered root overlap error = %v", err)
			}
			if _, err := os.Lstat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected runtime root was created: %v", err)
			}
		})
	}
}

func TestLocalTargetInitializationRejectsPrunableRegisteredRuntimePath(
	t *testing.T,
) {
	t.Parallel()

	root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
	parent := canonicalTestDirectory(t)
	runtimeRoot := filepath.Join(parent, "prunable-worktree")
	runTargetGitTest(
		t, root,
		"worktree", "add", "--quiet", "--detach",
		runtimeRoot, baseObjectHex(base),
	)
	if err := os.RemoveAll(runtimeRoot); err != nil {
		t.Fatal(err)
	}
	definition := localTargetDefinition(
		t, root, base, "feature/prunable-runtime-overlap",
	)
	_, err := initializeWorkspaceV2(t,
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:35:10Z"),
	)
	if err == nil || !strings.Contains(
		err.Error(), "unsafe workspace root overlap",
	) {
		t.Fatalf("prunable registered-worktree overlap error = %v", err)
	}
	if _, statErr := os.Lstat(runtimeRoot); !errors.Is(
		statErr, os.ErrNotExist,
	) {
		t.Fatalf("prunable registered path was recreated: %v", statErr)
	}
}

func TestLocalTargetInitializationRejectsConcurrentRegisteredWorktree(
	t *testing.T,
) {
	t.Parallel()

	root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
	definition := localTargetDefinition(
		t, root, base, "feature/concurrent-worktree-registration",
	)
	runtimeRoot := canonicalTestDirectory(t)
	lateWorktree := filepath.Join(runtimeRoot, "late-worktree")
	registered := false
	_, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(),
		runtimeRoot,
		definition,
		mustTime(t, "2026-07-24T12:35:20Z"),
		workspace.WorkspaceInitializationOptions{
			WorktreeRoot: workspaceTestWorktreeRoot(t, runtimeRoot),
			TargetFault: func(
				point workspace.LocalTargetInitializationFaultPoint,
			) error {
				if point != workspace.LocalTargetFaultBeforeRefUpdate ||
					registered {
					return nil
				}
				registered = true
				runTargetGitTest(
					t, root,
					"worktree", "add", "--quiet", "--detach",
					lateWorktree, baseObjectHex(base),
				)
				return nil
			},
		},
	)
	if err == nil || !registered || !strings.Contains(
		err.Error(),
		"registered Git worktree inventory changed during workspace initialization",
	) {
		t.Fatalf(
			"concurrent registered-worktree error = %v registered=%t",
			err, registered,
		)
	}
	branch := definition.Workspace().FeatureBranch()
	if got := strings.TrimSpace(runTargetGitTest(
		t, root, "branch", "--list", branch,
	)); got != "" {
		t.Fatalf("concurrent registration allowed feature ref %q", got)
	}
}

func TestLocalTargetRejectsFeatureNamespaceAndCheckedOutOwnership(t *testing.T) {
	t.Parallel()

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
			requireFullSuiteCase(
				t,
				test.name == "unrelated exact ref",
				"feature-ref namespace and checkout permutation",
			)

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

func TestLocalTargetRejectsExternalObjectLinks(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "external Git object-storage matrix")

	t.Run("packed objects", func(t *testing.T) {
		root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		runTargetGitTest(t, root, "gc", "--quiet", "--prune=now")
		packDirectory := filepath.Join(root, ".git", "objects", "pack")
		entries, err := os.ReadDir(packDirectory)
		if err != nil {
			t.Fatal(err)
		}
		external := canonicalTestDirectory(t)
		replaced := 0
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			extension := filepath.Ext(entry.Name())
			if extension != ".pack" && extension != ".idx" &&
				extension != ".rev" {
				continue
			}
			source := filepath.Join(packDirectory, entry.Name())
			outside := filepath.Join(external, entry.Name())
			if err := os.Rename(source, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, source); err != nil {
				t.Fatal(err)
			}
			replaced++
		}
		if replaced == 0 {
			t.Fatal("Git gc produced no pack object files")
		}
		definition := localTargetDefinition(
			t, root, base, "feature/external-pack",
		)
		_, err = workspace.ValidateLocalTarget(
			context.Background(), definition.Workspace(),
		)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("external pack symlink error = %v", err)
		}
	})

	t.Run("loose object", func(t *testing.T) {
		root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		raw := baseObjectHex(base)
		source := filepath.Join(
			root, ".git", "objects", raw[:2], raw[2:],
		)
		outside := filepath.Join(canonicalTestDirectory(t), raw)
		if err := os.Rename(source, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, source); err != nil {
			t.Fatal(err)
		}
		definition := localTargetDefinition(
			t, root, base, "feature/external-loose-object",
		)
		_, err := workspace.ValidateLocalTarget(
			context.Background(), definition.Workspace(),
		)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("external loose-object symlink error = %v", err)
		}
	})

	t.Run("loose object hard link", func(t *testing.T) {
		root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
		raw := baseObjectHex(base)
		source := filepath.Join(
			root, ".git", "objects", raw[:2], raw[2:],
		)
		outside := filepath.Join(canonicalTestDirectory(t), raw)
		if err := os.Link(source, outside); err != nil {
			t.Fatal(err)
		}
		definition := localTargetDefinition(
			t, root, base, "feature/hard-linked-loose-object",
		)
		_, err := workspace.ValidateLocalTarget(
			context.Background(), definition.Workspace(),
		)
		if err == nil || !strings.Contains(err.Error(), "hard links") {
			t.Fatalf("hard-linked loose-object error = %v", err)
		}
	})
}

func TestLocalTargetRejectsUnsupportedRepositoryProfiles(t *testing.T) {
	t.Parallel()

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
			name: "external attributes metadata",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				if err := os.WriteFile(
					filepath.Join(root, ".git", "info", "attributes"),
					[]byte("*.txt filter=hostile\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
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
			name: "signature display",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "log.showSignature", "true",
				)
			},
			want: "signature display",
		},
		{
			name: "generic signature program",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "gpg.program", "false",
				)
			},
			want: "external signature program",
		},
		{
			name: "format-specific signature program",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "gpg.openpgp.program", "false",
				)
			},
			want: "external signature program",
		},
		{
			name: "SSH default-key command",
			setup: func(t *testing.T, root string, _ workspace.GitObjectID) {
				runTargetGitTest(
					t, root, "config", "gpg.ssh.defaultKeyCommand", "false",
				)
			},
			want: "external signature program",
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
			requireFullSuiteCase(
				t,
				test.name == "promisor",
				"unsupported Git repository-profile matrix",
			)

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
	t.Parallel()
	requireFullSuite(t, "bare Git repository profile")

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

func TestLocalTargetRejectsSubmodulesInPinnedBaseTree(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "submodule Git repository profile")

	root, _ := initializeTargetRepository(
		t, workspace.GitHashSHA1,
	)
	content := `[submodule "hostile"]
	path = dependencies/hostile
	url = https://example.invalid/hostile.git
`
	if err := os.WriteFile(
		filepath.Join(root, ".gitmodules"),
		[]byte(content),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "add", "--", ".gitmodules")
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
	if err == nil || !strings.Contains(err.Error(), "submodules are not supported") {
		t.Fatalf("unsupported pinned-base tree error = %v", err)
	}
}

func TestLocalTargetRejectsRepositoryAttributesInPinnedBaseTree(t *testing.T) {
	t.Parallel()
	requireFullSuite(t, "repository attributes profile")

	root, _ := initializeTargetRepository(
		t, workspace.GitHashSHA1,
	)
	attributes := filepath.Join(root, "config", ".gitattributes")
	if err := os.MkdirAll(filepath.Dir(attributes), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		attributes,
		[]byte("*.txt filter=hostile text eol=crlf\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "add", "--", "config/.gitattributes")
	runTargetGitTest(
		t, root,
		"-c", "user.name=Feature Implement Test",
		"-c", "user.email=feature-implement@localhost",
		"commit", "--quiet", "-m", "add repository attributes",
	)
	base := targetHead(t, root, workspace.GitHashSHA1)
	definition := localTargetDefinition(
		t, root, base, "feature/repository-attributes",
	)
	_, err := workspace.ValidateLocalTarget(
		context.Background(), definition.Workspace(),
	)
	if err == nil ||
		!strings.Contains(err.Error(), "repository-defined .gitattributes") {
		t.Fatalf("repository attributes admission error = %v", err)
	}
}

func TestLocalTargetReadsGitCompatibleWorktreeConfigurationBooleans(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "worktree Git configuration encoding matrix")

	tests := []struct {
		name       string
		boolean    string
		valueless  bool
		hostileKey string
		want       string
	}{
		{
			name:       "positive numeric signature program",
			boolean:    "2",
			hostileKey: "gpg.ssh.program",
			want:       "external signature program",
		},
		{
			name:       "negative numeric filter process",
			boolean:    "-7",
			hostileKey: "filter.hostile.process",
			want:       "active Git filter",
		},
		{
			name:       "valueless signature program",
			valueless:  true,
			hostileKey: "gpg.ssh.program",
			want:       "external signature program",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, base := initializeTargetRepository(t, workspace.GitHashSHA1)
			probeDirectory := canonicalTestDirectory(t)
			probe := filepath.Join(probeDirectory, "hostile-program")
			marker := filepath.Join(probeDirectory, "hostile-program-invoked")
			script := "#!/bin/sh\nmkdir " + shellSingleQuote(marker) + "\nexit 1\n"
			if err := os.WriteFile(probe, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			runTargetGitTest(
				t, root, "config", "core.repositoryFormatVersion", "1",
			)
			runTargetGitTest(
				t, root, "config", "extensions.worktreeConfig", "true",
			)
			runTargetGitTest(
				t, root, "config", "--worktree", test.hostileKey, probe,
			)
			if test.valueless {
				runTargetGitTest(
					t, root, "config", "--unset-all",
					"extensions.worktreeConfig",
				)
				config, err := os.OpenFile(
					filepath.Join(root, ".git", "config"),
					os.O_WRONLY|os.O_APPEND,
					0,
				)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := config.WriteString(
					"\n[extensions]\n\tworktreeConfig\n",
				)
				syncErr := config.Sync()
				closeErr := config.Close()
				if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
					t.Fatal(err)
				}
			} else {
				runTargetGitTest(
					t, root, "config", "extensions.worktreeConfig",
					test.boolean,
				)
			}
			definition := localTargetDefinition(
				t, root, base, "feature/worktree-config-boolean",
			)
			_, err := workspace.ValidateLocalTarget(
				context.Background(), definition.Workspace(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("worktree configuration error = %v", err)
			}
			if _, statErr := os.Lstat(marker); !errors.Is(
				statErr, os.ErrNotExist,
			) {
				t.Fatalf("hostile worktree program was invoked: %v", statErr)
			}
		})
	}
}

func TestLocalTargetRejectsEscapingSymlinkAndRepositoryReplacement(t *testing.T) {
	t.Parallel()

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
		requireFullSuite(t, "repository symlink-placement permutation")

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
		requireFullSuite(t, "repository replacement permutation")

		fixture := newDefinitionFixture(t)
		definition := mustDefinition(t, fixture.sources)
		runtimeRoot := canonicalTestDirectory(t)
		if _, err := initializeWorkspaceV2(t,
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
		_, err := initializeWorkspaceV2(t,
			runtimeRoot,
			definition,
			mustTime(t, "2026-07-24T12:41:00Z"),
		)
		if err == nil {
			t.Fatalf("repository replacement error = %v", err)
		}
	})
}

func TestLocalTargetRejectsConfiguredSignatureVerifierWithoutInvocation(
	t *testing.T,
) {
	t.Parallel()
	requireFullSuite(t, "Git signature-verifier profile")

	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is required for signed-commit coverage")
	}
	root := canonicalTestDirectory(t)
	runTargetGitTest(
		t, root,
		"init", "--quiet", "--initial-branch=main",
		"--object-format=sha1", ".",
	)
	if err := os.WriteFile(
		filepath.Join(root, "seed.txt"),
		[]byte("signed local target seed\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "add", "--", "seed.txt")
	keyDirectory := canonicalTestDirectory(t)
	signingKey := filepath.Join(keyDirectory, "signing-key")
	command := exec.Command(
		sshKeygen,
		"-q", "-t", "ed25519", "-N", "", "-f", signingKey,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate SSH signing key: %v\n%s", err, output)
	}
	runTargetGitTest(
		t, root,
		"-c", "user.name=Feature Implement Test",
		"-c", "user.email=feature-implement@localhost",
		"-c", "gpg.format=ssh",
		"-c", "user.signingKey="+signingKey,
		"commit", "--quiet", "-S", "-m", "signed seed local target",
	)
	base := targetHead(t, root, workspace.GitHashSHA1)
	probeDirectory := canonicalTestDirectory(t)
	probe := filepath.Join(probeDirectory, "signature-probe")
	marker := filepath.Join(probeDirectory, "signature-program-invoked")
	script := "#!/bin/sh\nmkdir " + shellSingleQuote(marker) + "\nexit 1\n"
	if err := os.WriteFile(probe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runTargetGitTest(t, root, "config", "log.showSignature", "true")
	runTargetGitTest(t, root, "config", "gpg.format", "ssh")
	runTargetGitTest(t, root, "config", "gpg.ssh.program", probe)
	definition := localTargetDefinition(
		t, root, base, "feature/inert-signature-verifier",
	)
	_, err = initializeWorkspaceV2(t,
		canonicalTestDirectory(t),
		definition,
		mustTime(t, "2026-07-24T12:45:00Z"),
	)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("configured signature verifier error = %v", err)
	}
	if _, statErr := os.Lstat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("configured signature verifier was invoked: %v", statErr)
	}
}

func TestLocalTargetOperationsDoNotInvokeHooksCredentialsOrProtocols(t *testing.T) {
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
	runTargetGitTest(
		t, root, "config", "credential.helper",
		"!f() { : > "+shellSingleQuote(marker)+"; }; f",
	)
	runTargetGitTest(
		t, root, "remote", "add", "hostile",
		"ext::sh -c ': > "+shellSingleQuote(marker)+"'",
	)
	if _, err := initializeWorkspaceV2(t,
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
