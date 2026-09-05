package workspace_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

const (
	realIntegrationTemplateReadyFile  = "template-ready"
	realIntegrationTemplateWaitLimit  = 30 * time.Second
	realIntegrationTemplatePollPeriod = 25 * time.Millisecond
	targetRepositoryTemplateReadyFile = "target-ready"
)

// The real-Git integration cases all start from the same initialized target.
// Build one immutable seed repository per object format and test process,
// then clone it into a test-owned root before the normal admission and
// workspace setup. The filesystem rendezvous intentionally avoids package
// globals: this package enforces that every external test can run in parallel.
type realIntegrationRepositoryTemplate struct {
	repositoryRoot string
	attemptRoot    string
	base           workspace.GitObjectID
	acceptedHead   workspace.GitObjectID
	acceptedTree   workspace.GitObjectID
}

type targetRepositoryTemplate struct {
	repositoryRoot string
	base           workspace.GitObjectID
}

// copyTargetRepositoryTemplate supplies the identical clean target used by
// definition fixtures. Each caller receives an independent clone, while one
// process-local image absorbs the repeated init/add/commit subprocesses.
func copyTargetRepositoryTemplate(
	t *testing.T,
	algorithm workspace.GitHashAlgorithm,
) (string, workspace.GitObjectID) {
	t.Helper()
	if algorithm != workspace.GitHashSHA1 &&
		algorithm != workspace.GitHashSHA256 {
		t.Fatalf("unsupported target repository hash algorithm %q", algorithm)
	}
	cacheRoot := filepath.Join(
		os.TempDir(),
		fmt.Sprintf(
			"feature-implement-target-repository-%d-%s",
			os.Getpid(), algorithm,
		),
	)
	template, err := acquireTargetRepositoryTemplate(cacheRoot, algorithm)
	if err != nil {
		t.Fatalf("build %s target repository template: %v", algorithm, err)
	}
	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	cloneGitTestFilesystemTree(t, template.repositoryRoot, repositoryRoot)
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatalf("canonicalize copied target repository: %v", err)
	}
	return canonical, template.base
}

func acquireTargetRepositoryTemplate(
	cacheRoot string,
	algorithm workspace.GitHashAlgorithm,
) (targetRepositoryTemplate, error) {
	deadline := time.Now().Add(realIntegrationTemplateWaitLimit)
	for {
		template, ready, err := readTargetRepositoryTemplate(cacheRoot)
		if err != nil {
			return targetRepositoryTemplate{}, err
		}
		if ready {
			return template, nil
		}
		if err := os.Mkdir(cacheRoot, 0o700); err == nil {
			template, buildErr := buildTargetRepositoryTemplate(cacheRoot, algorithm)
			if buildErr != nil {
				_ = os.RemoveAll(cacheRoot)
				return targetRepositoryTemplate{}, buildErr
			}
			if writeErr := os.WriteFile(
				filepath.Join(cacheRoot, targetRepositoryTemplateReadyFile),
				[]byte(template.base.String()+"\n"),
				0o600,
			); writeErr != nil {
				_ = os.RemoveAll(cacheRoot)
				return targetRepositoryTemplate{}, fmt.Errorf(
					"publish target repository template: %w", writeErr,
				)
			}
			return template, nil
		} else if !errors.Is(err, os.ErrExist) {
			return targetRepositoryTemplate{}, fmt.Errorf(
				"create target repository template root: %w", err,
			)
		}
		if time.Now().After(deadline) {
			return targetRepositoryTemplate{}, fmt.Errorf(
				"wait for concurrent target repository template at %s", cacheRoot,
			)
		}
		time.Sleep(realIntegrationTemplatePollPeriod)
	}
}

func buildTargetRepositoryTemplate(
	cacheRoot string,
	algorithm workspace.GitHashAlgorithm,
) (targetRepositoryTemplate, error) {
	repositoryRoot := filepath.Join(cacheRoot, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		return targetRepositoryTemplate{}, fmt.Errorf(
			"create target repository template directory: %w", err,
		)
	}
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return targetRepositoryTemplate{}, fmt.Errorf(
			"canonicalize target repository template: %w", err,
		)
	}
	if _, err := runRealIntegrationTemplateGit(
		canonical,
		"init", "--quiet", "--initial-branch=main",
		"--object-format="+string(algorithm), ".",
	); err != nil {
		return targetRepositoryTemplate{}, err
	}
	if err := os.WriteFile(
		filepath.Join(canonical, "seed.txt"), []byte("local target seed\n"), 0o600,
	); err != nil {
		return targetRepositoryTemplate{}, fmt.Errorf(
			"write target repository template seed: %w", err,
		)
	}
	if _, err := runRealIntegrationTemplateGit(canonical, "add", "--", "seed.txt"); err != nil {
		return targetRepositoryTemplate{}, err
	}
	if _, err := runRealIntegrationTemplateGit(
		canonical,
		"-c", "user.name=Feature Implement Test",
		"-c", "user.email=feature-implement@localhost",
		"commit", "--quiet", "-m", "seed local target",
	); err != nil {
		return targetRepositoryTemplate{}, err
	}
	raw, err := runRealIntegrationTemplateGit(canonical, "rev-parse", "HEAD")
	if err != nil {
		return targetRepositoryTemplate{}, err
	}
	base, err := workspace.ParseGitObjectID(
		string(algorithm) + ":" + strings.TrimSpace(raw),
	)
	if err != nil {
		return targetRepositoryTemplate{}, fmt.Errorf(
			"parse target repository template base: %w", err,
		)
	}
	return targetRepositoryTemplate{repositoryRoot: canonical, base: base}, nil
}

func readTargetRepositoryTemplate(
	cacheRoot string,
) (targetRepositoryTemplate, bool, error) {
	content, err := os.ReadFile(
		filepath.Join(cacheRoot, targetRepositoryTemplateReadyFile),
	)
	if errors.Is(err, os.ErrNotExist) {
		return targetRepositoryTemplate{}, false, nil
	}
	if err != nil {
		return targetRepositoryTemplate{}, false, fmt.Errorf(
			"read target repository template readiness: %w", err,
		)
	}
	base, err := workspace.ParseGitObjectID(strings.TrimSpace(string(content)))
	if err != nil {
		return targetRepositoryTemplate{}, false, fmt.Errorf(
			"parse target repository template base: %w", err,
		)
	}
	repositoryRoot := filepath.Join(cacheRoot, "repository")
	info, err := os.Stat(repositoryRoot)
	if err != nil {
		return targetRepositoryTemplate{}, false, fmt.Errorf(
			"inspect target repository template: %w", err,
		)
	}
	if !info.IsDir() {
		return targetRepositoryTemplate{}, false, fmt.Errorf(
			"target repository template is not a directory: %s", repositoryRoot,
		)
	}
	return targetRepositoryTemplate{
		repositoryRoot: repositoryRoot,
		base:           base,
	}, true, nil
}

func newRealIntegrationScenarioFixture(
	t *testing.T,
	algorithm workspace.GitHashAlgorithm,
) (definitionFixture, realIntegrationRepositoryTemplate, string) {
	t.Helper()
	template := realIntegrationRepositoryTemplateFor(t, algorithm)
	templateRoot := filepath.Dir(template.repositoryRoot)
	templateAttempt := filepath.Join(templateRoot, "attempt")
	if info, err := os.Stat(templateAttempt); err != nil || !info.IsDir() {
		t.Fatalf("real-Git template attempt = %s, err=%v", templateAttempt, err)
	}
	scenarioRoot := filepath.Join(t.TempDir(), "scenario")
	cloneGitTestFilesystemTree(t, templateRoot, scenarioRoot)
	canonical, err := filepath.EvalSymlinks(scenarioRoot)
	if err != nil {
		t.Fatalf("canonicalize copied real-Git integration scenario: %v", err)
	}
	repositoryRoot := filepath.Join(canonical, "repository")
	if info, err := os.Stat(repositoryRoot); err != nil || !info.IsDir() {
		t.Fatalf("copied real-Git integration repository = %s, err=%v", repositoryRoot, err)
	}
	workspaceRoot := filepath.Join(canonical, "workspace")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatalf("create copied real-Git integration workspace: %v", err)
	}
	template.repositoryRoot = repositoryRoot
	template.attemptRoot = filepath.Join(canonical, "attempt")
	return newDefinitionFixtureForRepository(repositoryRoot, template.base), template, workspaceRoot
}

func realIntegrationRepositoryTemplateFor(
	t *testing.T,
	algorithm workspace.GitHashAlgorithm,
) realIntegrationRepositoryTemplate {
	t.Helper()
	if algorithm != workspace.GitHashSHA1 &&
		algorithm != workspace.GitHashSHA256 {
		t.Fatalf("unsupported real-Git integration hash algorithm %q", algorithm)
	}
	cacheRoot := filepath.Join(
		os.TempDir(),
		fmt.Sprintf(
			"feature-implement-real-integration-%d-%s",
			os.Getpid(), algorithm,
		),
	)
	template, err := acquireRealIntegrationRepositoryTemplate(
		cacheRoot, algorithm,
	)
	if err != nil {
		t.Fatalf(
			"build %s real-Git integration repository template: %v",
			algorithm,
			err,
		)
	}
	return template
}

func acquireRealIntegrationRepositoryTemplate(
	cacheRoot string,
	algorithm workspace.GitHashAlgorithm,
) (realIntegrationRepositoryTemplate, error) {
	deadline := time.Now().Add(realIntegrationTemplateWaitLimit)
	for {
		template, ready, err := readRealIntegrationRepositoryTemplate(cacheRoot)
		if err != nil {
			return realIntegrationRepositoryTemplate{}, err
		}
		if ready {
			return template, nil
		}
		if err := os.Mkdir(cacheRoot, 0o700); err == nil {
			template, buildErr := buildRealIntegrationRepositoryTemplate(
				cacheRoot, algorithm,
			)
			if buildErr != nil {
				_ = os.RemoveAll(cacheRoot)
				return realIntegrationRepositoryTemplate{}, buildErr
			}
			if writeErr := os.WriteFile(
				filepath.Join(cacheRoot, realIntegrationTemplateReadyFile),
				[]byte(strings.Join([]string{
					template.base.String(),
					template.acceptedHead.String(),
					template.acceptedTree.String(),
				}, "\n")+"\n"),
				0o600,
			); writeErr != nil {
				_ = os.RemoveAll(cacheRoot)
				return realIntegrationRepositoryTemplate{}, fmt.Errorf(
					"publish real-Git integration template: %w", writeErr,
				)
			}
			return template, nil
		} else if !errors.Is(err, os.ErrExist) {
			return realIntegrationRepositoryTemplate{}, fmt.Errorf(
				"create real-Git integration template root: %w", err,
			)
		}
		if time.Now().After(deadline) {
			return realIntegrationRepositoryTemplate{}, fmt.Errorf(
				"wait for concurrent real-Git integration template at %s",
				cacheRoot,
			)
		}
		time.Sleep(realIntegrationTemplatePollPeriod)
	}
}

func buildRealIntegrationRepositoryTemplate(
	cacheRoot string,
	algorithm workspace.GitHashAlgorithm,
) (template realIntegrationRepositoryTemplate, resultErr error) {
	repositoryRoot := filepath.Join(cacheRoot, "repository")
	err := os.Mkdir(repositoryRoot, 0o700)
	if err != nil {
		return template, fmt.Errorf("create template repository directory: %w", err)
	}
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return template, fmt.Errorf("canonicalize template directory: %w", err)
	}
	if _, err := runRealIntegrationTemplateGit(
		repositoryRoot,
		"init", "--quiet", "--initial-branch=main",
		"--object-format="+string(algorithm), ".",
	); err != nil {
		return template, err
	}
	if err := os.WriteFile(
		filepath.Join(repositoryRoot, "seed.txt"),
		[]byte("local target seed\n"),
		0o600,
	); err != nil {
		return template, fmt.Errorf("write template seed: %w", err)
	}
	if _, err := runRealIntegrationTemplateGit(
		repositoryRoot, "add", "--", "seed.txt",
	); err != nil {
		return template, err
	}
	if _, err := runRealIntegrationTemplateGit(
		repositoryRoot,
		"-c", "user.name=Feature Implement Test",
		"-c", "user.email=feature-implement@localhost",
		"commit", "--quiet", "-m", "seed local target",
	); err != nil {
		return template, err
	}
	raw, err := runRealIntegrationTemplateGit(
		repositoryRoot, "rev-parse", "HEAD",
	)
	if err != nil {
		return template, err
	}
	base, err := workspace.ParseGitObjectID(
		string(algorithm) + ":" + strings.TrimSpace(raw),
	)
	if err != nil {
		return template, fmt.Errorf("parse template base commit: %w", err)
	}
	attemptRoot := filepath.Join(cacheRoot, "attempt")
	if _, err := workspace.DefaultLocalAttemptGitAdapter().MaterializeAttemptTree(
		context.Background(), repositoryRoot, base, attemptRoot,
	); err != nil {
		return template, fmt.Errorf("materialize template detached attempt: %w", err)
	}
	treeRaw, err := runRealIntegrationTemplateGit(
		repositoryRoot, "rev-parse", rawGitObject(base)+"^{tree}",
	)
	if err != nil {
		return template, err
	}
	acceptedTree, err := workspace.ParseGitObjectID(
		string(algorithm) + ":" + strings.TrimSpace(treeRaw),
	)
	if err != nil {
		return template, fmt.Errorf("parse template accepted tree: %w", err)
	}
	acceptedRaw, err := runRealIntegrationTemplateGit(
		attemptRoot,
		"-c", "user.name=Integration Test",
		"-c", "user.email=integration@example.invalid",
		"commit-tree", rawGitObject(acceptedTree),
		"-p", rawGitObject(base),
		"-m", "accepted attempt",
	)
	if err != nil {
		return template, err
	}
	acceptedHead, err := workspace.ParseGitObjectID(
		string(algorithm) + ":" + strings.TrimSpace(acceptedRaw),
	)
	if err != nil {
		return template, fmt.Errorf("parse template accepted head: %w", err)
	}
	if _, err := runRealIntegrationTemplateGit(
		attemptRoot, "reset", "--hard", rawGitObject(acceptedHead),
	); err != nil {
		return template, err
	}
	inspection, err := workspace.DefaultLocalAttemptGitAdapter().InspectAttemptWorktree(
		context.Background(), repositoryRoot, attemptRoot,
	)
	if err != nil {
		return template, fmt.Errorf("inspect template detached attempt: %w", err)
	}
	if !inspection.Clean() || inspection.WorktreeHead() != acceptedHead ||
		inspection.WorktreeTree() != acceptedTree {
		return template, fmt.Errorf(
			"template detached attempt = head %s tree %s clean=%t",
			inspection.WorktreeHead(), inspection.WorktreeTree(), inspection.Clean(),
		)
	}
	fixture := newDefinitionFixtureForRepository(repositoryRoot, base)
	definition, err := workspace.ValidateDefinition(fixture.sources)
	if err != nil {
		return template, fmt.Errorf("validate template definition: %w", err)
	}
	admissionRoot := filepath.Join(cacheRoot, "admission")
	if _, err := workspace.InitializeWorkspaceV2WithOptions(
		context.Background(), admissionRoot, definition,
		time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC),
		workspace.WorkspaceInitializationOptions{},
	); err != nil {
		return template, fmt.Errorf("admit template local target: %w", err)
	}
	if err := os.RemoveAll(admissionRoot); err != nil {
		return template, fmt.Errorf("remove verified template admission: %w", err)
	}
	return realIntegrationRepositoryTemplate{
		repositoryRoot: repositoryRoot,
		attemptRoot:    attemptRoot,
		base:           base,
		acceptedHead:   acceptedHead,
		acceptedTree:   acceptedTree,
	}, nil
}

func readRealIntegrationRepositoryTemplate(
	cacheRoot string,
) (realIntegrationRepositoryTemplate, bool, error) {
	content, err := os.ReadFile(
		filepath.Join(cacheRoot, realIntegrationTemplateReadyFile),
	)
	if errors.Is(err, os.ErrNotExist) {
		return realIntegrationRepositoryTemplate{}, false, nil
	}
	if err != nil {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"read real-Git integration template readiness: %w", err,
		)
	}
	objects := strings.Fields(string(content))
	if len(objects) != 3 {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"real-Git integration template readiness has %d objects, want 3",
			len(objects),
		)
	}
	base, err := workspace.ParseGitObjectID(objects[0])
	if err != nil {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"parse real-Git integration template base: %w", err,
		)
	}
	acceptedHead, err := workspace.ParseGitObjectID(objects[1])
	if err != nil {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"parse real-Git integration template accepted head: %w", err,
		)
	}
	acceptedTree, err := workspace.ParseGitObjectID(objects[2])
	if err != nil {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"parse real-Git integration template accepted tree: %w", err,
		)
	}
	if base.Algorithm() != acceptedHead.Algorithm() ||
		base.Algorithm() != acceptedTree.Algorithm() {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"real-Git integration template objects use different formats",
		)
	}
	repositoryRoot := filepath.Join(cacheRoot, "repository")
	info, err := os.Stat(repositoryRoot)
	if err != nil {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"inspect real-Git integration template repository: %w", err,
		)
	}
	if !info.IsDir() {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"real-Git integration template repository is not a directory: %s",
			repositoryRoot,
		)
	}
	attemptRoot := filepath.Join(cacheRoot, "attempt")
	attemptInfo, err := os.Stat(attemptRoot)
	if err != nil {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"inspect real-Git integration template attempt: %w", err,
		)
	}
	if !attemptInfo.IsDir() {
		return realIntegrationRepositoryTemplate{}, false, fmt.Errorf(
			"real-Git integration template attempt is not a directory: %s",
			attemptRoot,
		)
	}
	return realIntegrationRepositoryTemplate{
		repositoryRoot: repositoryRoot,
		attemptRoot:    attemptRoot,
		base:           base,
		acceptedHead:   acceptedHead,
		acceptedTree:   acceptedTree,
	}, true, nil
}

func installRealIntegrationTemplateAttempt(
	t *testing.T,
	template realIntegrationRepositoryTemplate,
	core attemptHarness,
) string {
	t.Helper()
	identity, err := workspace.DeriveAttemptIdentity(
		core.definition.Workspace().ID(), core.definition.Generation(),
		core.unit, 1, core.base,
	)
	if err != nil {
		t.Fatal(err)
	}
	worktreeRoot, err := workspace.DerivedWorkspaceWorktreeRoot(core.workspace)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := workspace.AttemptWorktreePath(
		worktreeRoot, identity, core.unit, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreeRoot, 0o700); err != nil {
		t.Fatalf("create copied attempt worktree root: %v", err)
	}
	if err := os.Rename(template.attemptRoot, worktree); err != nil {
		t.Fatalf("move copied template attempt into worktree: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(worktree, ".git", "objects", "info", "alternates"),
		[]byte(filepath.Join(template.repositoryRoot, ".git", "objects")+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("rebind copied attempt alternate objects: %v", err)
	}
	core.git.setHead(t, worktree, core.base, true)
	return worktree
}

// newRealIntegrationAttemptHarness restores the exact initialized runtime
// state that the template admission has already proved. Each test still owns
// a fresh journal and generation store; avoiding another target-admission
// subprocess fan-out leaves the real-Git integration operation as the unit
// under test.
func newRealIntegrationAttemptHarness(
	t *testing.T,
	fixture definitionFixture,
	unitID string,
	workspaceDir string,
) attemptHarness {
	t.Helper()
	definition := mustDefinition(t, fixture.sources)
	manifest := definition.Workspace()
	binding, err := workspace.NewLocalTargetBinding(
		workspace.LocalTargetBindingOptions{
			Root:          manifest.RepositoryRoot(),
			ObjectFormat:  manifest.BaseCommit().Algorithm(),
			BaseRef:       manifest.BaseRef(),
			BaseCommit:    manifest.BaseCommit(),
			FeatureBranch: manifest.FeatureBranch(),
		},
	)
	if err != nil {
		t.Fatalf("bind copied real-Git integration target: %v", err)
	}
	store, err := workspace.OpenGenerationStore(workspaceDir)
	if err != nil {
		t.Fatalf("open copied real-Git integration generation store: %v", err)
	}
	stored, err := store.Store(definition)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("store copied real-Git integration generation: %v", err)
	}
	journal, err := workspace.OpenWorkspaceJournal(
		workspaceDir, workspace.JournalReadWrite,
	)
	if err != nil {
		t.Fatalf("open copied real-Git integration journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	event, err := workspace.NewWorkspaceInitializedJournalEventWithTarget(
		manifest.ID(), definition.Generation(), stored.DefinitionDigest(), binding,
	)
	if err != nil {
		t.Fatalf("construct copied real-Git integration initialization: %v", err)
	}
	appendRequest, err := workspace.NewJournalAppend(
		event, mustTime(t, "2026-07-21T10:00:00Z"),
	)
	if err != nil {
		t.Fatalf("append copied real-Git integration initialization: %v", err)
	}
	if _, err := journal.Append(appendRequest); err != nil {
		t.Fatalf("record copied real-Git integration initialization: %v", err)
	}
	if _, err := workspace.RebuildWorkspaceRuntimeProjectionFile(journal); err != nil {
		t.Fatalf("project copied real-Git integration runtime: %v", err)
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspace.RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := runtime.LocalTarget()
	if !ok || target.Binding() != binding ||
		target.CreatedHead() != binding.BaseCommit() {
		t.Fatalf("copied real-Git integration target = %#v exists=%t", target, ok)
	}
	goal, err := workspace.NewGoalBinding(
		workspace.MustID("implementation-goal"), workspace.GoalScopeMergeUnit,
	)
	if err != nil {
		t.Fatal(err)
	}
	return attemptHarness{
		definition: definition,
		journal:    journal,
		workspace:  workspaceDir,
		git:        &fakeAttemptGit{},
		base:       target.CreatedHead(),
		unit:       mustMergeUnitReference(t, "alpha-plan", unitID),
		goal:       goal,
	}
}

func runRealIntegrationTemplateGit(
	repositoryRoot string,
	arguments ...string,
) (string, error) {
	command := exec.Command(
		"git", append([]string{"-C", repositoryRoot}, arguments...)...,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"template git %s: %w\n%s",
			strings.Join(arguments, " "), err, output,
		)
	}
	return string(output), nil
}
