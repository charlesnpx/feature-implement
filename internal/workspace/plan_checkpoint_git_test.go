package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestPlanCheckpointInitialRevisionLockAndExactRetries(t *testing.T) {
	root := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
	initialInput := checkpointInput(t, "2026-07-23T10:00:00Z", "", "")
	initial, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointInitial, Input: initialInput,
	})
	if err != nil {
		t.Fatalf("initial checkpoint: %v", err)
	}
	if initial.Recovered || initial.Commit == "" || initial.Tree == "" || initial.LockDigest != "" {
		t.Fatalf("initial result = %#v", initial)
	}
	retried, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointInitial, Input: initialInput,
	})
	if err != nil {
		t.Fatalf("retry initial checkpoint: %v", err)
	}
	if !retried.Recovered || retried.Commit != initial.Commit || retried.Tree != initial.Tree {
		t.Fatalf("initial retry = %#v, initial = %#v", retried, initial)
	}
	assertPlanRepositoryGitPolicy(t, root)

	planPath := filepath.Join(root, "plans", "alpha.yaml")
	replaceFileText(t, planPath, "Establish the first contract.", "Define the first contract.")
	revisionInput := checkpointInput(
		t,
		"2026-07-23T10:01:00Z",
		"rev-one",
		workspace.DigestBytes([]byte("review one")).String(),
	)
	revision, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointRevision, Input: revisionInput,
	})
	if err != nil {
		t.Fatalf("revision checkpoint: %v", err)
	}
	if revision.RevisionID != "rev-one" || revision.ReviewDigest == "" || revision.Commit == initial.Commit {
		t.Fatalf("revision result = %#v", revision)
	}
	revisionRetry, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointRevision, Input: revisionInput,
	})
	if err != nil {
		t.Fatalf("retry revision checkpoint: %v", err)
	}
	if !revisionRetry.Recovered || revisionRetry.Commit != revision.Commit {
		t.Fatalf("revision retry = %#v", revisionRetry)
	}

	lockInput := checkpointInput(t, "2026-07-23T10:02:00Z", "", "")
	lock, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointLock, Input: lockInput,
	})
	if err != nil {
		t.Fatalf("lock checkpoint: %v", err)
	}
	if lock.LockDigest == "" || lock.Commit == revision.Commit || lock.Recovered {
		t.Fatalf("lock result = %#v", lock)
	}
	verified, err := workspace.VerifyPlanLockCheckpoint(context.Background(), mustLoadBundle(t, root))
	if err != nil {
		t.Fatalf("verify lock checkpoint: %v", err)
	}
	if verified.Commit().String() != lock.Commit || verified.Tree().String() != lock.Tree ||
		verified.Generation().String() != lock.Generation || verified.LockDigest().String() != lock.LockDigest {
		t.Fatalf("verified lock = %#v, result = %#v", verified, lock)
	}
	lockRetry, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointLock, Input: lockInput,
	})
	if err != nil {
		t.Fatalf("retry lock checkpoint: %v", err)
	}
	if !lockRetry.Recovered || lockRetry.Commit != lock.Commit {
		t.Fatalf("lock retry = %#v", lockRetry)
	}
	assertPlanRepositoryGitPolicy(t, root)
}

func TestWithVerifiedPlanLockCheckpointExcludesGitMutationsThroughWorkspaceBinding(
	t *testing.T,
) {
	root, lock := lockedPlanRepository(t)
	bundle := mustLoadBundle(t, root)
	runtimeRoot := filepath.Join(
		canonicalMaterializationTestTempDir(t),
		"runtime",
	)
	var retained workspace.VerifiedPlanLockCheckpoint
	var initialized workspace.WorkspaceInitializationResult
	verified, err := workspace.WithVerifiedPlanLockCheckpoint(
		context.Background(),
		bundle,
		func(checkpoint workspace.VerifiedPlanLockCheckpoint) error {
			retained = checkpoint
			for _, command := range []struct {
				name string
				argv []string
				lock string
			}{
				{
					name: "HEAD update",
					argv: []string{
						"-C", root, "update-ref",
						"refs/heads/main", "HEAD^",
					},
					lock: "main.lock",
				},
				{
					name: "index update",
					argv: []string{
						"-C", root, "add", "--", "plans/alpha.yaml",
					},
					lock: "index.lock",
				},
			} {
				process := exec.Command("git", command.argv...)
				output, commandErr := process.CombinedOutput()
				if commandErr == nil ||
					!strings.Contains(string(output), command.lock) {
					return fmt.Errorf(
						"%s was not excluded: err=%v output=%s",
						command.name,
						commandErr,
						output,
					)
				}
			}
			var initializeErr error
			initialized, initializeErr = initializeWorkspaceV2(t,
				runtimeRoot,
				bundle.Definition(),
				mustTime(t, "2026-07-23T10:04:00Z"),
				checkpoint,
			)
			if initializeErr != nil {
				return initializeErr
			}
			for _, relative := range []string{
				filepath.Join("refs", "heads", "main.lock"),
				"index.lock",
			} {
				if _, statErr := os.Stat(
					filepath.Join(root, ".git", relative),
				); statErr != nil {
					return fmt.Errorf(
						"verification exclusion %s was released before binding: %w",
						relative,
						statErr,
					)
				}
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("verify and bind lock checkpoint: %v", err)
	}
	if verified.Commit().String() != lock.Commit ||
		initialized.Runtime().PlanCheckpoint().String() != lock.Commit {
		t.Fatalf(
			"bound checkpoint: verified=%s runtime=%s want=%s",
			verified.Commit(),
			initialized.Runtime().PlanCheckpoint(),
			lock.Commit,
		)
	}
	if got := strings.TrimSpace(string(runGitSetup(
		t,
		root,
		"rev-parse",
		"HEAD",
	))); !strings.HasSuffix(lock.Commit, ":"+got) {
		t.Fatalf("HEAD after excluded update = %s, want %s", got, lock.Commit)
	}
	if staged := runGitSetup(
		t,
		root,
		"diff",
		"--cached",
		"--name-only",
		"HEAD",
	); len(staged) != 0 {
		t.Fatalf("index after excluded add is dirty: %s", staged)
	}
	snapshot, err := workspace.ReadWorkspaceJournalSnapshot(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records()) != 3 {
		t.Fatalf("initialization journal records = %d", len(snapshot.Records()))
	}
	event, ok := snapshot.Records()[0].Event().(workspace.WorkspaceInitializedJournalEvent)
	if !ok || event.PlanCheckpoint().String() != lock.Commit {
		t.Fatalf(
			"initialization journal checkpoint = %#v, want %s",
			event,
			lock.Commit,
		)
	}
	_, err = initializeWorkspaceV2(t,
		filepath.Join(
			canonicalMaterializationTestTempDir(t),
			"stale-runtime",
		),
		bundle.Definition(),
		mustTime(t, "2026-07-23T10:05:00Z"),
		retained,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "active plan lock verification lease") {
		t.Fatalf("stale verification lease error = %v", err)
	}
}

func TestPlanCheckpointRequestDecodingIsStrict(t *testing.T) {
	root := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
	for _, test := range []struct {
		name  string
		kind  workspace.PlanCheckpointKind
		input string
		want  string
	}{
		{
			name:  "missing occurred_at",
			kind:  workspace.PlanCheckpointInitial,
			input: `{"schema_version":2}`,
			want:  "occurred_at",
		},
		{
			name:  "unknown initial field",
			kind:  workspace.PlanCheckpointInitial,
			input: `{"schema_version":2,"occurred_at":"2026-07-23T10:00:00Z","extra":true}`,
			want:  "unknown field",
		},
		{
			name:  "missing review digest",
			kind:  workspace.PlanCheckpointRevision,
			input: `{"schema_version":2,"occurred_at":"2026-07-23T10:00:00Z","revision_id":"rev-one"}`,
			want:  "review_digest",
		},
		{
			name:  "duplicate field",
			kind:  workspace.PlanCheckpointLock,
			input: `{"schema_version":2,"schema_version":2,"occurred_at":"2026-07-23T10:00:00Z"}`,
			want:  "duplicate",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := workspace.CheckpointPlanRepository(
				context.Background(),
				workspace.PlanCheckpointOptions{
					Root: root, Kind: test.kind, Input: []byte(test.input),
				},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("strict request error = %v, want %q", err, test.want)
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid requests created a plan repository: %v", err)
	}
}

func TestPlanCheckpointOwnsRepositoryInsideAncestorRepository(t *testing.T) {
	root := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
	ancestor := filepath.Dir(root)
	runGitSetup(t, ancestor, "init", "--initial-branch=ancestor")

	result, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointInitial,
			Input: checkpointInput(t, "2026-07-23T10:30:00Z", "", ""),
		},
	)
	if err != nil {
		t.Fatalf("initial checkpoint in ancestor repository: %v", err)
	}
	if result.Commit == "" {
		t.Fatal("initial checkpoint did not return a commit")
	}
	if got := strings.TrimSpace(string(runGitSetup(t, root, "rev-parse", "--show-toplevel"))); got != root {
		t.Fatalf("nested plan repository root = %q, want %q", got, root)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, root, "rev-parse", "--absolute-git-dir"))); got != filepath.Join(root, ".git") {
		t.Fatalf("nested plan Git directory = %q", got)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, ancestor, "symbolic-ref", "HEAD"))); got != "refs/heads/ancestor" {
		t.Fatalf("ancestor HEAD = %q", got)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, ancestor, "for-each-ref", "--format=%(refname)"))); got != "" {
		t.Fatalf("checkpoint mutated ancestor refs: %q", got)
	}
}

func TestPlanCheckpointRejectsGitSymlinkBeforeInitialization(t *testing.T) {
	root := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
	external := filepath.Join(canonicalMaterializationTestTempDir(t), "external-git")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(external, "marker.txt")
	if err := os.WriteFile(markerPath, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}

	_, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointInitial,
			Input: checkpointInput(t, "2026-07-23T10:31:00Z", "", ""),
		},
	)
	if err == nil || !strings.Contains(err.Error(), ".git") ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Git symlink error = %v", err)
	}
	content, readErr := os.ReadFile(markerPath)
	if readErr != nil || string(content) != "untouched\n" {
		t.Fatalf("external marker changed: content=%q err=%v", content, readErr)
	}
	entries, readErr := os.ReadDir(external)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "marker.txt" {
		t.Fatalf("external Git target was mutated: %+v", entries)
	}
}

func TestPlanCheckpointGitEffectsRemainBoundToRetainedDirectory(t *testing.T) {
	root := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
	external := filepath.Join(canonicalMaterializationTestTempDir(t), "external-git-race")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(external, "marker.txt")
	if err := os.WriteFile(markerPath, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperRoot := canonicalMaterializationTestTempDir(t)
	wrapper := filepath.Join(wrapperRoot, "git-wrapper")
	trigger := filepath.Join(wrapperRoot, "triggered")
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" hash-object "*)
    if [ ! -e %s ]; then
      mv %s %s
      ln -s %s %s
      : > %s
    fi
    ;;
esac
exec %s "$@"
`,
		planShellQuote(trigger),
		planShellQuote(filepath.Join(root, ".git")),
		planShellQuote(filepath.Join(root, ".git-retained")),
		planShellQuote(external),
		planShellQuote(filepath.Join(root, ".git")),
		planShellQuote(trigger),
		planShellQuote(realGit),
	)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointInitial,
			Input:         checkpointInput(t, "2026-07-23T10:32:00Z", "", ""),
			GitExecutable: wrapper,
		},
	)
	if err == nil ||
		(!strings.Contains(err.Error(), "replaced") &&
			!strings.Contains(err.Error(), "symlink")) {
		t.Fatalf("replaced Git directory error = %v", err)
	}
	content, readErr := os.ReadFile(markerPath)
	if readErr != nil || string(content) != "untouched\n" {
		t.Fatalf("external marker changed: content=%q err=%v", content, readErr)
	}
	entries, readErr := os.ReadDir(external)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "marker.txt" {
		t.Fatalf("external Git target was mutated: %+v", entries)
	}
	if _, statErr := os.Stat(trigger); statErr != nil {
		t.Fatalf("Git replacement wrapper did not trigger: %v", statErr)
	}
}

func TestPlanCheckpointRejectsNoOpUnownedStagedAndDuplicateRevisions(t *testing.T) {
	t.Run("semantic no-op", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		content, err := os.ReadFile(planPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(planPath, append(content, []byte("\n# formatting only\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(t, "2026-07-23T11:01:00Z", "rev-no-op", workspace.DigestBytes([]byte("review")).String()),
		})
		if err == nil || !strings.Contains(err.Error(), "semantic no-op") {
			t.Fatalf("no-op revision error = %v", err)
		}
	})

	t.Run("unowned path", func(t *testing.T) {
		root := initializedPlanRepository(t)
		if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not owned\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(t, "2026-07-23T11:02:00Z", "rev-unowned", workspace.DigestBytes([]byte("review")).String()),
		})
		if err == nil || !strings.Contains(err.Error(), "unowned path") {
			t.Fatalf("unowned revision error = %v", err)
		}
	})

	t.Run("staged path", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		replaceFileText(t, planPath, "Establish the first contract.", "Define the first contract.")
		runGitSetup(t, root, "add", "--", "plans/alpha.yaml")
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(t, "2026-07-23T11:03:00Z", "rev-staged", workspace.DigestBytes([]byte("review")).String()),
		})
		if err == nil || !strings.Contains(err.Error(), "clean index") {
			t.Fatalf("staged revision error = %v", err)
		}
	})

	t.Run("duplicate revision id", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		replaceFileText(t, planPath, "Establish the first contract.", "Define the first contract.")
		first := checkpointInput(t, "2026-07-23T11:04:00Z", "rev-duplicate", workspace.DigestBytes([]byte("first review")).String())
		if _, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision, Input: first,
		}); err != nil {
			t.Fatal(err)
		}
		replaceFileText(t, planPath, "Define the first contract.", "Refine the first contract.")
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(t, "2026-07-23T11:05:00Z", "rev-duplicate", workspace.DigestBytes([]byte("second review")).String()),
		})
		if err == nil || !strings.Contains(err.Error(), "already present") {
			t.Fatalf("duplicate revision error = %v", err)
		}
	})

	t.Run("worktree-specific Git configuration", func(t *testing.T) {
		root := initializedPlanRepository(t)
		runGitSetup(t, root, "config", "extensions.worktreeConfig", "true")
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(t, "2026-07-23T11:06:00Z", "rev-config", workspace.DigestBytes([]byte("review")).String()),
		})
		if err == nil || !strings.Contains(err.Error(), "unsupported local Git configuration") {
			t.Fatalf("worktree-specific Git configuration error = %v", err)
		}
	})
}

func TestPlanCheckpointIndexRecoveryPreservesUnexpectedStaging(t *testing.T) {
	t.Run("exact retry", func(t *testing.T) {
		root := initializedPlanRepository(t)
		relative := "plans/alpha.yaml"
		planPath := filepath.Join(root, filepath.FromSlash(relative))
		clean, err := os.ReadFile(planPath)
		if err != nil {
			t.Fatal(err)
		}
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Unexpected staged contract.",
		)
		runGitSetup(t, root, "add", "--", relative)
		if err := os.WriteFile(planPath, clean, 0o600); err != nil {
			t.Fatal(err)
		}
		before := runGitSetup(t, root, "diff", "--cached", "--binary", "--", relative)
		_, err = workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointInitial,
			Input: checkpointInput(t, "2026-07-23T11:00:00Z", "", ""),
		})
		if err == nil || !strings.Contains(err.Error(), "refusing destructive recovery") {
			t.Fatalf("exact retry staged-index error = %v", err)
		}
		after := runGitSetup(t, root, "diff", "--cached", "--binary", "--", relative)
		if !stringEqualBytes(before, after) {
			t.Fatalf("exact retry changed the staged diff\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})

	t.Run("post-CAS staging race", func(t *testing.T) {
		root := initializedPlanRepository(t)
		relative := "plans/alpha.yaml"
		planPath := filepath.Join(root, filepath.FromSlash(relative))
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Define the first contract.",
		)
		input := checkpointInput(
			t,
			"2026-07-23T11:07:00Z",
			"rev-post-cas-staging",
			workspace.DigestBytes([]byte("review")).String(),
		)
		var staged []byte
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point != workspace.PlanCheckpointFaultAfterRefCAS {
					return nil
				}
				replaceFileText(
					t,
					planPath,
					"Define the first contract.",
					"Unexpected post-CAS staging.",
				)
				runGitSetup(t, root, "add", "--", relative)
				replaceFileText(
					t,
					planPath,
					"Unexpected post-CAS staging.",
					"Define the first contract.",
				)
				staged = runGitSetup(t, root, "diff", "--cached", "--binary", "--", relative)
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "refusing destructive recovery") {
			t.Fatalf("post-CAS staged-index error = %v", err)
		}
		after := runGitSetup(t, root, "diff", "--cached", "--binary", "--", relative)
		if len(staged) == 0 || !stringEqualBytes(staged, after) {
			t.Fatalf("post-CAS recovery changed the staged diff\nbefore:\n%s\nafter:\n%s", staged, after)
		}
		_, retryErr := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			},
		)
		if retryErr == nil || !strings.Contains(retryErr.Error(), "refusing destructive recovery") {
			t.Fatalf("post-CAS exact retry error = %v", retryErr)
		}
		retried := runGitSetup(t, root, "diff", "--cached", "--binary", "--", relative)
		if !stringEqualBytes(staged, retried) {
			t.Fatalf("post-CAS exact retry changed the staged diff")
		}
	})
}

func TestPlanCheckpointIndexPublicationLocksAndRecovers(t *testing.T) {
	t.Run("concurrent Git add is blocked", func(t *testing.T) {
		root := initializedPlanRepository(t)
		relative := "plans/alpha.yaml"
		planPath := filepath.Join(root, filepath.FromSlash(relative))
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Define the first contract.",
		)
		input := checkpointInput(
			t,
			"2026-07-23T11:08:00Z",
			"rev-index-lock",
			workspace.DigestBytes([]byte("review")).String(),
		)
		injected := errors.New("stop after proving index exclusion")
		_, err := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
				FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
					if point != workspace.PlanCheckpointFaultAfterIndexLock {
						return nil
					}
					replaceFileText(
						t,
						planPath,
						"Define the first contract.",
						"Concurrent staged contract.",
					)
					command := exec.Command("git", "-C", root, "add", "--", relative)
					output, addErr := command.CombinedOutput()
					if addErr == nil || !strings.Contains(string(output), "index.lock") {
						t.Fatalf(
							"concurrent git add was not excluded: err=%v output=%s",
							addErr,
							output,
						)
					}
					replaceFileText(
						t,
						planPath,
						"Concurrent staged contract.",
						"Define the first contract.",
					)
					return injected
				},
			},
		)
		if !errors.Is(err, injected) {
			t.Fatalf("index lock interruption error = %v", err)
		}
		if staged := runGitSetup(
			t,
			root,
			"diff",
			"--cached",
			"--name-only",
			"HEAD^",
		); len(staged) != 0 {
			t.Fatalf("concurrent Git add changed the prior index: %s", staged)
		}
		retried, err := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			},
		)
		if err != nil || !retried.Recovered {
			t.Fatalf("retry after index exclusion: result=%#v err=%v", retried, err)
		}
	})

	t.Run("quarantined prior index is crash recoverable", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Define the first contract.",
		)
		input := checkpointInput(
			t,
			"2026-07-23T11:09:00Z",
			"rev-index-quarantine",
			workspace.DigestBytes([]byte("review")).String(),
		)
		injected := errors.New("stop after index quarantine")
		_, err := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
				FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
					if point == workspace.PlanCheckpointFaultAfterIndexQuarantine {
						return injected
					}
					return nil
				},
			},
		)
		if !errors.Is(err, injected) {
			t.Fatalf("index quarantine interruption error = %v", err)
		}
		for _, relative := range []string{
			"index.lock",
			"feature-plan-index-sync.v1.json",
			"feature-plan-index.previous.v1",
		} {
			if _, statErr := os.Stat(filepath.Join(root, ".git", relative)); statErr != nil {
				t.Fatalf("expected recoverable index file %s: %v", relative, statErr)
			}
		}
		if _, statErr := os.Stat(filepath.Join(root, ".git", "index")); !errors.Is(
			statErr,
			os.ErrNotExist,
		) {
			t.Fatalf("quarantined index unexpectedly exists: %v", statErr)
		}
		retried, err := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			},
		)
		if err != nil || !retried.Recovered {
			t.Fatalf("retry quarantined index: result=%#v err=%v", retried, err)
		}
		for _, relative := range []string{
			"index.lock",
			"feature-plan-index-sync.v1.json",
			"feature-plan-index.previous.v1",
		} {
			if _, statErr := os.Stat(filepath.Join(root, ".git", relative)); !errors.Is(
				statErr,
				os.ErrNotExist,
			) {
				t.Fatalf("recovered index left %s: %v", relative, statErr)
			}
		}
	})
}

func TestPlanCheckpointIndexRecoveryPreservesActiveStateAfterHeadMoves(
	t *testing.T,
) {
	t.Run("index matching current HEAD", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Define the first contract.",
		)
		input := checkpointInput(
			t,
			"2026-07-23T11:10:00Z",
			"rev-index-head-moved",
			workspace.DigestBytes([]byte("review")).String(),
		)
		interruptPlanIndexAfterPublication(t, root, input)
		runGitSetup(
			t,
			root,
			"commit",
			"--allow-empty",
			"-m",
			"external concurrent commit",
		)
		before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		_, retryErr := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			},
		)
		if retryErr == nil {
			t.Fatal("checkpoint unexpectedly accepted external commit history")
		}
		after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		if !stringEqualBytes(before, after) {
			t.Fatal("index matching the moved HEAD was replaced during recovery")
		}
		if staged := runGitSetup(
			t,
			root,
			"diff",
			"--cached",
			"--name-only",
			"HEAD",
		); len(staged) != 0 {
			t.Fatalf("recovery dirtied the moved HEAD index: %s", staged)
		}
		assertPlanIndexRecoveryFilesRemoved(t, root)
	})

	t.Run("newly staged active index", func(t *testing.T) {
		root := initializedPlanRepository(t)
		relative := "plans/alpha.yaml"
		planPath := filepath.Join(root, filepath.FromSlash(relative))
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Define the first contract.",
		)
		input := checkpointInput(
			t,
			"2026-07-23T11:11:00Z",
			"rev-index-staged-after-crash",
			workspace.DigestBytes([]byte("review")).String(),
		)
		interruptPlanIndexAfterPublication(t, root, input)
		runGitSetup(t, root, "reset", "--soft", "HEAD^")
		before := runGitSetup(
			t,
			root,
			"diff",
			"--cached",
			"--binary",
			"--",
			relative,
		)
		if len(before) == 0 {
			t.Fatal("soft reset did not create the expected staged state")
		}
		_, retryErr := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			},
		)
		if retryErr == nil ||
			!strings.Contains(retryErr.Error(), "clean index") {
			t.Fatalf("staged recovery error = %v", retryErr)
		}
		after := runGitSetup(
			t,
			root,
			"diff",
			"--cached",
			"--binary",
			"--",
			relative,
		)
		if !stringEqualBytes(before, after) {
			t.Fatal("target-moved recovery replaced the staged active index")
		}
		assertPlanIndexRecoveryFilesRemoved(t, root)
	})

	t.Run("concurrent add is excluded during recovery", func(t *testing.T) {
		root := initializedPlanRepository(t)
		relative := "plans/alpha.yaml"
		planPath := filepath.Join(root, filepath.FromSlash(relative))
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Define the first contract.",
		)
		input := checkpointInput(
			t,
			"2026-07-23T11:12:00Z",
			"rev-index-recovery-lock",
			workspace.DigestBytes([]byte("review")).String(),
		)
		interruptPlanIndexAfterPublication(t, root, input)
		before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("stop after recovery exclusion")
		_, retryErr := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
				FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
					if point != workspace.PlanCheckpointFaultAfterIndexRecoveryLock {
						return nil
					}
					replaceFileText(
						t,
						planPath,
						"Define the first contract.",
						"Concurrent recovery staging.",
					)
					command := exec.Command(
						"git",
						"-C",
						root,
						"add",
						"--",
						relative,
					)
					output, addErr := command.CombinedOutput()
					if addErr == nil ||
						!strings.Contains(string(output), "index.lock") {
						t.Fatalf(
							"concurrent recovery add was not excluded: err=%v output=%s",
							addErr,
							output,
						)
					}
					replaceFileText(
						t,
						planPath,
						"Concurrent recovery staging.",
						"Define the first contract.",
					)
					return injected
				},
			},
		)
		if !errors.Is(retryErr, injected) {
			t.Fatalf("recovery exclusion error = %v", retryErr)
		}
		after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		if !stringEqualBytes(before, after) {
			t.Fatal("blocked concurrent add changed the active index")
		}
		if _, statErr := os.Stat(
			filepath.Join(root, ".git", "index.lock"),
		); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("recovery exclusion was not released: %v", statErr)
		}
		retried, err := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			},
		)
		if err != nil || !retried.Recovered {
			t.Fatalf("retry after recovery exclusion: result=%#v err=%v", retried, err)
		}
		assertPlanIndexRecoveryFilesRemoved(t, root)
	})
}

func TestPlanCheckpointRecoversInterruptedInitializationAndPublication(t *testing.T) {
	for _, test := range []struct {
		name      string
		point     workspace.PlanCheckpointFaultPoint
		recovered bool
	}{
		{name: "repository initialization", point: workspace.PlanCheckpointFaultAfterRepositoryInitialization},
		{name: "commit object", point: workspace.PlanCheckpointFaultAfterCommitCreation},
		{name: "ref publication", point: workspace.PlanCheckpointFaultAfterRefCAS, recovered: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
			input := checkpointInput(t, "2026-07-23T12:00:00Z", "", "")
			injected := errors.New("injected")
			_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointInitial, Input: input,
				FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
					if point == test.point {
						return injected
					}
					return nil
				},
			})
			if !errors.Is(err, injected) {
				t.Fatalf("injected checkpoint error = %v", err)
			}
			result, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointInitial, Input: input,
			})
			if err != nil {
				t.Fatalf("retry checkpoint: %v", err)
			}
			if result.Recovered != test.recovered {
				t.Fatalf("recovered = %t, want %t", result.Recovered, test.recovered)
			}
			assertPlanRepositoryGitPolicy(t, root)
		})
	}
}

func TestPlanCheckpointDetectsSourceChangeDuringLockAndCASRace(t *testing.T) {
	t.Run("source changes during lock", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		input := checkpointInput(t, "2026-07-23T13:00:00Z", "", "")
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointLock, Input: input,
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point == workspace.PlanCheckpointFaultAfterLockGeneration {
					replaceFileText(t, planPath, "Establish the first contract.", "Changed during locking.")
				}
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "changed during lock generation") {
			t.Fatalf("source race error = %v", err)
		}
		if got := strings.TrimSpace(string(runGitSetup(t, root, "log", "-1", "--format=%s"))); got != "feature plan checkpoint: initial" {
			t.Fatalf("HEAD after source race = %q", got)
		}
		replaceFileText(t, planPath, "Changed during locking.", "Establish the first contract.")
		if _, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointLock, Input: input,
		}); err != nil {
			t.Fatalf("retry lock after restoring source: %v", err)
		}
	})

	t.Run("CAS loses", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		replaceFileText(t, planPath, "Establish the first contract.", "Define the first contract.")
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(t, "2026-07-23T13:01:00Z", "rev-cas", workspace.DigestBytes([]byte("review")).String()),
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point == workspace.PlanCheckpointFaultBeforeRefCAS {
					runGitSetup(t, root, "commit", "--allow-empty", "-m", "concurrent update")
				}
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "compare-and-swap") {
			t.Fatalf("CAS race error = %v", err)
		}
		if got := strings.TrimSpace(string(runGitSetup(t, root, "log", "-1", "--format=%s"))); got != "concurrent update" {
			t.Fatalf("CAS winner = %q", got)
		}
	})

	t.Run("source race retires generated locks for revision", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointLock,
			Input: checkpointInput(t, "2026-07-23T13:01:30Z", "", ""),
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point == workspace.PlanCheckpointFaultAfterLockGeneration {
					replaceFileText(
						t,
						planPath,
						"Establish the first contract.",
						"Changed for a recoverable revision.",
					)
				}
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "changed during lock generation") {
			t.Fatalf("source race error = %v", err)
		}
		revision, err := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointRevision,
				Input: checkpointInput(
					t,
					"2026-07-23T13:01:31Z",
					"rev-after-lock-race",
					workspace.DigestBytes([]byte("review after lock race")).String(),
				),
			},
		)
		if err != nil {
			t.Fatalf("revision after lock source race: %v", err)
		}
		if revision.RevisionID != "rev-after-lock-race" {
			t.Fatalf("revision after lock source race = %#v", revision)
		}
		if _, statErr := os.Stat(filepath.Join(root, "generated", workspace.WorkspaceLockFileName)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("stale workspace lock survived retirement: %v", statErr)
		}
		inventory, readErr := os.ReadFile(filepath.Join(root, workspace.PlanRepositoryInventoryFileName))
		if readErr != nil ||
			!strings.Contains(string(inventory), "feature.plan.lock.retired.v1.json") ||
			!strings.Contains(string(inventory), `"tracked": false`) {
			t.Fatalf("retired lock inventory is missing: err=%v\n%s", readErr, inventory)
		}
		lock, err := workspace.CheckpointPlanRepository(
			context.Background(),
			workspace.PlanCheckpointOptions{
				Root: root, Kind: workspace.PlanCheckpointLock,
				Input: checkpointInput(t, "2026-07-23T13:01:32Z", "", ""),
			},
		)
		if err != nil || lock.LockDigest == "" {
			t.Fatalf("lock after recovered revision: result=%#v err=%v", lock, err)
		}
	})

	t.Run("retired lock sentinel changes before CAS", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		replaceFileText(
			t,
			planPath,
			"Establish the first contract.",
			"Define the first contract.",
		)
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: checkpointInput(
				t,
				"2026-07-23T13:01:45Z",
				"rev-sentinel-race",
				workspace.DigestBytes([]byte("review")).String(),
			),
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point == workspace.PlanCheckpointFaultAfterTreeCreation {
					sentinel := filepath.Join(
						root,
						"generated",
						"feature.plan.lock.retired.v1.json",
					)
					if writeErr := os.WriteFile(sentinel, []byte("changed\n"), 0o600); writeErr != nil {
						t.Fatal(writeErr)
					}
				}
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "sentinel content changed") {
			t.Fatalf("retired sentinel race error = %v", err)
		}
		if got := strings.TrimSpace(string(runGitSetup(t, root, "log", "-1", "--format=%s"))); got != "feature plan checkpoint: initial" {
			t.Fatalf("HEAD after retired sentinel race = %q", got)
		}
	})

	t.Run("source changes after tree creation", func(t *testing.T) {
		root := initializedPlanRepository(t)
		planPath := filepath.Join(root, "plans", "alpha.yaml")
		replaceFileText(t, planPath, "Establish the first contract.", "Define the first contract.")
		input := checkpointInput(
			t,
			"2026-07-23T13:02:00Z",
			"rev-tree-race",
			workspace.DigestBytes([]byte("review")).String(),
		)
		_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point == workspace.PlanCheckpointFaultAfterTreeCreation {
					replaceFileText(t, planPath, "Define the first contract.", "Changed after tree creation.")
				}
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "changed since the bundle was loaded") {
			t.Fatalf("post-tree source race error = %v", err)
		}
		if got := strings.TrimSpace(string(runGitSetup(t, root, "log", "-1", "--format=%s"))); got != "feature plan checkpoint: initial" {
			t.Fatalf("HEAD after post-tree source race = %q", got)
		}
	})
}

func TestPlanCheckpointRecoversInterruptedLockGeneration(t *testing.T) {
	root := initializedPlanRepository(t)
	input := checkpointInput(t, "2026-07-23T13:03:00Z", "", "")
	injected := errors.New("injected lock interruption")
	_, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointLock, Input: input,
		FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
			if point == workspace.PlanCheckpointFaultAfterLockGeneration {
				return injected
			}
			return nil
		},
	})
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted lock generation error = %v", err)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, root, "log", "-1", "--format=%s"))); got != "feature plan checkpoint: initial" {
		t.Fatalf("HEAD after interrupted lock generation = %q", got)
	}
	result, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointLock, Input: input,
	})
	if err != nil {
		t.Fatalf("retry interrupted lock generation: %v", err)
	}
	if result.Recovered || result.Kind != workspace.PlanCheckpointLock || result.LockDigest == "" {
		t.Fatalf("retried lock result = %#v", result)
	}
	verified, err := workspace.VerifyPlanLockCheckpoint(context.Background(), mustLoadBundle(t, root))
	if err != nil {
		t.Fatalf("verify retried lock: %v", err)
	}
	if verified.Commit().String() != result.Commit {
		t.Fatalf("verified retry commit = %s, want %s", verified.Commit(), result.Commit)
	}
}

func TestPlanCheckpointRecoversLockAfterInventoryPublication(t *testing.T) {
	for _, point := range []workspace.PlanCheckpointFaultPoint{
		workspace.PlanCheckpointFaultAfterTreeCreation,
		workspace.PlanCheckpointFaultAfterCommitCreation,
		workspace.PlanCheckpointFaultBeforeRefCAS,
	} {
		t.Run(string(point), func(t *testing.T) {
			root := initializedPlanRepository(t)
			input := checkpointInput(t, "2026-07-23T13:04:00Z", "", "")
			injected := errors.New("interrupt prepared lock checkpoint")
			_, err := workspace.CheckpointPlanRepository(
				context.Background(),
				workspace.PlanCheckpointOptions{
					Root: root, Kind: workspace.PlanCheckpointLock, Input: input,
					FaultInjector: func(observed workspace.PlanCheckpointFaultPoint) error {
						if observed == point {
							return injected
						}
						return nil
					},
				},
			)
			if !errors.Is(err, injected) {
				t.Fatalf("prepared lock interruption error = %v", err)
			}
			if got := strings.TrimSpace(string(runGitSetup(
				t,
				root,
				"log",
				"-1",
				"--format=%s",
			))); got != "feature plan checkpoint: initial" {
				t.Fatalf("HEAD after prepared lock interruption = %q", got)
			}
			retried, err := workspace.CheckpointPlanRepository(
				context.Background(),
				workspace.PlanCheckpointOptions{
					Root: root, Kind: workspace.PlanCheckpointLock, Input: input,
				},
			)
			if err != nil || retried.Recovered || retried.LockDigest == "" {
				t.Fatalf("retry prepared lock: result=%#v err=%v", retried, err)
			}
			if _, err := workspace.VerifyPlanLockCheckpoint(
				context.Background(),
				mustLoadBundle(t, root),
			); err != nil {
				t.Fatalf("verify recovered lock: %v", err)
			}
		})
	}
}

func TestPlanCheckpointReconcilesPreparedLockBeforeChangedRequest(t *testing.T) {
	for _, point := range []workspace.PlanCheckpointFaultPoint{
		workspace.PlanCheckpointFaultAfterTreeCreation,
		workspace.PlanCheckpointFaultAfterCommitCreation,
		workspace.PlanCheckpointFaultBeforeRefCAS,
	} {
		t.Run(string(point), func(t *testing.T) {
			t.Run("changed lock then revision", func(t *testing.T) {
				root := initializedPlanRepository(t)
				lockInput := checkpointInput(
					t,
					"2026-07-23T13:05:00Z",
					"",
					"",
				)
				interruptPreparedPlanLock(t, root, lockInput, point)
				planPath := filepath.Join(root, "plans", "alpha.yaml")
				replaceFileText(
					t,
					planPath,
					"Establish the first contract.",
					"Define the first contract.",
				)
				_, lockErr := workspace.CheckpointPlanRepository(
					context.Background(),
					workspace.PlanCheckpointOptions{
						Root: root, Kind: workspace.PlanCheckpointLock,
						Input: lockInput,
					},
				)
				if lockErr == nil ||
					!strings.Contains(
						lockErr.Error(),
						"sources changed after the latest checkpoint",
					) {
					t.Fatalf("changed-source lock error = %v", lockErr)
				}
				assertInventoryMatchesHead(t, root)
				revision, err := workspace.CheckpointPlanRepository(
					context.Background(),
					workspace.PlanCheckpointOptions{
						Root: root, Kind: workspace.PlanCheckpointRevision,
						Input: checkpointInput(
							t,
							"2026-07-23T13:05:01Z",
							"rev-after-prepared-lock",
							workspace.DigestBytes(
								[]byte("review after prepared lock"),
							).String(),
						),
					},
				)
				if err != nil ||
					revision.RevisionID != "rev-after-prepared-lock" {
					t.Fatalf(
						"revision after prepared lock: result=%#v err=%v",
						revision,
						err,
					)
				}
			})

			t.Run("direct revision", func(t *testing.T) {
				root := initializedPlanRepository(t)
				lockInput := checkpointInput(
					t,
					"2026-07-23T13:06:00Z",
					"",
					"",
				)
				interruptPreparedPlanLock(t, root, lockInput, point)
				replaceFileText(
					t,
					filepath.Join(root, "plans", "alpha.yaml"),
					"Establish the first contract.",
					"Define the first contract.",
				)
				revision, err := workspace.CheckpointPlanRepository(
					context.Background(),
					workspace.PlanCheckpointOptions{
						Root: root, Kind: workspace.PlanCheckpointRevision,
						Input: checkpointInput(
							t,
							"2026-07-23T13:06:01Z",
							"rev-direct-after-prepared-lock",
							workspace.DigestBytes(
								[]byte("direct review"),
							).String(),
						),
					},
				)
				if err != nil ||
					revision.RevisionID !=
						"rev-direct-after-prepared-lock" {
					t.Fatalf(
						"direct revision after prepared lock: result=%#v err=%v",
						revision,
						err,
					)
				}
			})
		})
	}
}

func TestPlanCheckpointTransactionExcludesConcurrentPreparedLockRecovery(
	t *testing.T,
) {
	root := initializedPlanRepository(t)
	interruptPreparedPlanLock(
		t,
		root,
		checkpointInput(t, "2026-07-23T13:07:00Z", "", ""),
		workspace.PlanCheckpointFaultAfterTreeCreation,
	)
	replaceFileText(
		t,
		filepath.Join(root, "plans", "alpha.yaml"),
		"Establish the first contract.",
		"Define the first contract.",
	)
	revisionInput := checkpointInput(
		t,
		"2026-07-23T13:07:01Z",
		"rev-transaction-exclusion",
		workspace.DigestBytes([]byte("review transaction exclusion")).String(),
	)
	headBefore := strings.TrimSpace(string(runGitSetup(
		t,
		root,
		"rev-parse",
		"HEAD",
	)))
	injected := errors.New("stop before prepared lock recovery")
	_, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: revisionInput,
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point !=
					workspace.PlanCheckpointFaultBeforePreparedLockRecovery {
					return nil
				}
				command := exec.Command(
					os.Args[0],
					"-test.run=^TestPlanCheckpointTransactionSubprocess$",
					"-test.count=1",
				)
				command.Env = append(
					os.Environ(),
					"FEATURE_IMPLEMENT_PLAN_TRANSACTION_HELPER=1",
					"FEATURE_IMPLEMENT_PLAN_TRANSACTION_ROOT="+root,
					"FEATURE_IMPLEMENT_PLAN_TRANSACTION_INPUT="+
						string(revisionInput),
				)
				output, processErr := command.CombinedOutput()
				if processErr != nil {
					return fmt.Errorf(
						"concurrent checkpoint helper: %w\n%s",
						processErr,
						output,
					)
				}
				if observed := strings.TrimSpace(string(runGitSetup(
					t,
					root,
					"rev-parse",
					"HEAD",
				))); observed != headBefore {
					return fmt.Errorf(
						"concurrent checkpoint published %s, expected %s",
						observed,
						headBefore,
					)
				}
				return injected
			},
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("prepared lock recovery pause error = %v", err)
	}
	if observed := strings.TrimSpace(string(runGitSetup(
		t,
		root,
		"rev-parse",
		"HEAD",
	))); observed != headBefore {
		t.Fatalf(
			"HEAD after excluded checkpoint = %s, want %s",
			observed,
			headBefore,
		)
	}
	revision, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision,
			Input: revisionInput,
		},
	)
	if err != nil {
		t.Fatalf("revision after transaction exclusion: %v", err)
	}
	if strings.HasSuffix(revision.Commit, ":"+headBefore) ||
		revision.RevisionID != "rev-transaction-exclusion" {
		t.Fatalf("revision after transaction exclusion = %#v", revision)
	}
	assertInventoryMatchesHead(t, root)
}

func TestPlanCheckpointTransactionSubprocess(t *testing.T) {
	if os.Getenv("FEATURE_IMPLEMENT_PLAN_TRANSACTION_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	root := os.Getenv("FEATURE_IMPLEMENT_PLAN_TRANSACTION_ROOT")
	input := []byte(os.Getenv("FEATURE_IMPLEMENT_PLAN_TRANSACTION_INPUT"))
	if root == "" || len(input) == 0 {
		t.Fatal("subprocess checkpoint fixture is incomplete")
	}
	_, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "plan repository transaction is active") {
		t.Fatalf("concurrent checkpoint transaction error = %v", err)
	}
}

func TestPlanCheckpointRecoversOnlyAbandonedMainRefExclusions(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "abandoned tool exclusion",
			content: "feature-plan-main-ref-lock:v1\n",
		},
		{
			name:    "foreign Git lock",
			content: "external-git-lock\n",
			wantErr: "locked by another Git operation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := initializedPlanRepository(t)
			replaceFileText(
				t,
				filepath.Join(root, "plans", "alpha.yaml"),
				"Establish the first contract.",
				"Define the first contract.",
			)
			lockPath := filepath.Join(
				root,
				".git",
				"refs",
				"heads",
				"main.lock",
			)
			if err := os.WriteFile(
				lockPath,
				[]byte(test.content),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			revision, err := workspace.CheckpointPlanRepository(
				context.Background(),
				workspace.PlanCheckpointOptions{
					Root: root, Kind: workspace.PlanCheckpointRevision,
					Input: checkpointInput(
						t,
						"2026-07-23T13:08:00Z",
						"rev-main-ref-exclusion",
						workspace.DigestBytes(
							[]byte("review main ref exclusion"),
						).String(),
					),
				},
			)
			if test.wantErr == "" {
				if err != nil ||
					revision.RevisionID != "rev-main-ref-exclusion" {
					t.Fatalf(
						"revision after abandoned exclusion: result=%#v err=%v",
						revision,
						err,
					)
				}
				if _, statErr := os.Stat(lockPath); !errors.Is(
					statErr,
					os.ErrNotExist,
				) {
					t.Fatalf(
						"abandoned main ref exclusion remains: %v",
						statErr,
					)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("foreign main ref lock error = %v", err)
			}
			content, readErr := os.ReadFile(lockPath)
			if readErr != nil || string(content) != test.content {
				t.Fatalf(
					"foreign main ref lock changed: content=%q err=%v",
					content,
					readErr,
				)
			}
		})
	}
}

func initializedPlanRepository(t *testing.T) string {
	t.Helper()
	root := writeDefinitionBundle(t, newDefinitionFixture(t), nil)
	if _, err := workspace.CheckpointPlanRepository(context.Background(), workspace.PlanCheckpointOptions{
		Root: root, Kind: workspace.PlanCheckpointInitial,
		Input: checkpointInput(t, "2026-07-23T11:00:00Z", "", ""),
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

func interruptPreparedPlanLock(
	t *testing.T,
	root string,
	input []byte,
	point workspace.PlanCheckpointFaultPoint,
) {
	t.Helper()
	injected := errors.New("interrupt prepared lock")
	_, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointLock, Input: input,
			FaultInjector: func(observed workspace.PlanCheckpointFaultPoint) error {
				if observed == point {
					return injected
				}
				return nil
			},
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("prepared lock interruption at %s: %v", point, err)
	}
}

func interruptPlanIndexAfterPublication(
	t *testing.T,
	root string,
	input []byte,
) {
	t.Helper()
	injected := errors.New("interrupt after index publication")
	_, err := workspace.CheckpointPlanRepository(
		context.Background(),
		workspace.PlanCheckpointOptions{
			Root: root, Kind: workspace.PlanCheckpointRevision, Input: input,
			FaultInjector: func(point workspace.PlanCheckpointFaultPoint) error {
				if point == workspace.PlanCheckpointFaultAfterIndexPublication {
					return injected
				}
				return nil
			},
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("index publication interruption: %v", err)
	}
}

func assertPlanIndexRecoveryFilesRemoved(t *testing.T, root string) {
	t.Helper()
	for _, relative := range []string{
		"index.lock",
		"feature-plan-index-sync.v1.json",
		"feature-plan-index.previous.v1",
	} {
		if _, statErr := os.Stat(
			filepath.Join(root, ".git", relative),
		); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("index recovery left %s: %v", relative, statErr)
		}
	}
}

func assertInventoryMatchesHead(t *testing.T, root string) {
	t.Helper()
	current, err := os.ReadFile(
		filepath.Join(root, workspace.PlanRepositoryInventoryFileName),
	)
	if err != nil {
		t.Fatal(err)
	}
	committed := runGitSetup(
		t,
		root,
		"show",
		"HEAD:"+workspace.PlanRepositoryInventoryFileName,
	)
	if !stringEqualBytes(current, committed) {
		t.Fatalf(
			"worktree inventory does not match HEAD\nworktree:\n%s\nHEAD:\n%s",
			current,
			committed,
		)
	}
}

func checkpointInput(t *testing.T, occurredAt, revisionID, reviewDigest string) []byte {
	t.Helper()
	value := map[string]any{
		"schema_version": workspace.PlanCheckpointRequestSchemaVersion,
		"occurred_at":    occurredAt,
	}
	if revisionID != "" {
		value["revision_id"] = revisionID
		value["review_digest"] = reviewDigest
	}
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func replaceFileText(t *testing.T, file, old, replacement string) {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), old) {
		t.Fatalf("%s does not contain %q", file, old)
	}
	updated := strings.Replace(string(content), old, replacement, 1)
	if err := os.WriteFile(file, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stringEqualBytes(left, right []byte) bool {
	return string(left) == string(right)
}

func planShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func mustLoadBundle(t *testing.T, root string) workspace.WorkspaceBundle {
	t.Helper()
	bundle, err := workspace.LoadWorkspaceBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func assertPlanRepositoryGitPolicy(t *testing.T, root string) {
	t.Helper()
	if got := strings.TrimSpace(string(runGitSetup(t, root, "symbolic-ref", "HEAD"))); got != "refs/heads/main" {
		t.Fatalf("HEAD = %q", got)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, root, "remote"))); got != "" {
		t.Fatalf("remotes = %q", got)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, root, "log", "-1", "--format=%an <%ae>"))); got != "Feature Implement <feature-implement@localhost>" {
		t.Fatalf("commit identity = %q", got)
	}
	if got := strings.TrimSpace(string(runGitSetup(t, root, "status", "--porcelain=v1", "--untracked-files=all"))); got != "" {
		t.Fatalf("plan repository is dirty:\n%s", got)
	}
}
