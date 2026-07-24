package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
