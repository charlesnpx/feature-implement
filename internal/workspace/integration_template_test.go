package workspace_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

// The real-Git integration cases all start from the same initialized target.
// Build one immutable seed repository per object format and test process,
// then clone it into a test-owned root before normal scenario setup.
// Process-local sync.OnceValues functions share only immutable images, while
// every caller receives a clone it may mutate.
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

type seedRepositoryTemplate struct {
	root           string
	repositoryRoot string
	base           workspace.GitObjectID
}

var (
	targetRepositoryTemplateSHA1 = sync.OnceValues(
		func() (targetRepositoryTemplate, error) {
			return buildTargetRepositoryTemplate(workspace.GitHashSHA1)
		},
	)
	targetRepositoryTemplateSHA256 = sync.OnceValues(
		func() (targetRepositoryTemplate, error) {
			return buildTargetRepositoryTemplate(workspace.GitHashSHA256)
		},
	)
	realIntegrationRepositoryTemplateSHA1 = sync.OnceValues(
		func() (realIntegrationRepositoryTemplate, error) {
			return buildRealIntegrationRepositoryTemplate(workspace.GitHashSHA1)
		},
	)
	realIntegrationRepositoryTemplateSHA256 = sync.OnceValues(
		func() (realIntegrationRepositoryTemplate, error) {
			return buildRealIntegrationRepositoryTemplate(workspace.GitHashSHA256)
		},
	)
)

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
	template, err := targetRepositoryTemplateForAlgorithm(algorithm)
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

func targetRepositoryTemplateForAlgorithm(
	algorithm workspace.GitHashAlgorithm,
) (targetRepositoryTemplate, error) {
	switch algorithm {
	case workspace.GitHashSHA1:
		return targetRepositoryTemplateSHA1()
	case workspace.GitHashSHA256:
		return targetRepositoryTemplateSHA256()
	default:
		return targetRepositoryTemplate{}, fmt.Errorf(
			"unsupported target repository hash algorithm %q", algorithm,
		)
	}
}

func buildTargetRepositoryTemplate(
	algorithm workspace.GitHashAlgorithm,
) (targetRepositoryTemplate, error) {
	seed, err := buildSeedRepositoryTemplate(algorithm)
	if err != nil {
		return targetRepositoryTemplate{}, err
	}
	return targetRepositoryTemplate{
		repositoryRoot: seed.repositoryRoot,
		base:           seed.base,
	}, nil
}

func buildSeedRepositoryTemplate(
	algorithm workspace.GitHashAlgorithm,
) (template seedRepositoryTemplate, resultErr error) {
	templateRoot, err := os.MkdirTemp(
		"", "feature-implement-template-"+string(algorithm)+"-",
	)
	if err != nil {
		return template, fmt.Errorf("create seed template root: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(templateRoot)
		}
	}()
	repositoryRoot := filepath.Join(templateRoot, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		return template, fmt.Errorf(
			"create seed template repository directory: %w", err,
		)
	}
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return template, fmt.Errorf(
			"canonicalize seed template repository: %w", err,
		)
	}
	if _, err := runRealIntegrationTemplateGit(
		canonical,
		"init", "--quiet", "--initial-branch=main",
		"--object-format="+string(algorithm), ".",
	); err != nil {
		return template, err
	}
	if err := os.WriteFile(
		filepath.Join(canonical, "seed.txt"), []byte("local target seed\n"), 0o600,
	); err != nil {
		return template, fmt.Errorf(
			"write seed template seed: %w", err,
		)
	}
	if _, err := runRealIntegrationTemplateGit(canonical, "add", "--", "seed.txt"); err != nil {
		return template, err
	}
	if _, err := runRealIntegrationTemplateGit(
		canonical,
		"-c", "user.name=Feature Implement Test",
		"-c", "user.email=feature-implement@localhost",
		"commit", "--quiet", "-m", "seed local target",
	); err != nil {
		return template, err
	}
	raw, err := runRealIntegrationTemplateGit(canonical, "rev-parse", "HEAD")
	if err != nil {
		return template, err
	}
	base, err := workspace.ParseGitObjectID(
		string(algorithm) + ":" + strings.TrimSpace(raw),
	)
	if err != nil {
		return template, fmt.Errorf(
			"parse seed template base: %w", err,
		)
	}
	return seedRepositoryTemplate{
		root:           filepath.Dir(canonical),
		repositoryRoot: canonical,
		base:           base,
	}, nil
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
	template, err := realIntegrationRepositoryTemplateForAlgorithm(algorithm)
	if err != nil {
		t.Fatalf(
			"build %s real-Git integration repository template: %v",
			algorithm,
			err,
		)
	}
	return template
}

func realIntegrationRepositoryTemplateForAlgorithm(
	algorithm workspace.GitHashAlgorithm,
) (realIntegrationRepositoryTemplate, error) {
	switch algorithm {
	case workspace.GitHashSHA1:
		return realIntegrationRepositoryTemplateSHA1()
	case workspace.GitHashSHA256:
		return realIntegrationRepositoryTemplateSHA256()
	default:
		return realIntegrationRepositoryTemplate{}, fmt.Errorf(
			"unsupported real-Git integration hash algorithm %q", algorithm,
		)
	}
}

func buildRealIntegrationRepositoryTemplate(
	algorithm workspace.GitHashAlgorithm,
) (template realIntegrationRepositoryTemplate, resultErr error) {
	seed, err := buildSeedRepositoryTemplate(algorithm)
	if err != nil {
		return template, err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(seed.root)
		}
	}()
	repositoryRoot := seed.repositoryRoot
	base := seed.base
	attemptRoot := filepath.Join(seed.root, "attempt")
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
	return realIntegrationRepositoryTemplate{
		repositoryRoot: repositoryRoot,
		attemptRoot:    attemptRoot,
		base:           base,
		acceptedHead:   acceptedHead,
		acceptedTree:   acceptedTree,
	}, nil
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

// newRealIntegrationAttemptHarness constructs the initialized runtime for a
// copied scenario. Each test validates its copied definition and owns a fresh
// journal and generation store, leaving the real-Git integration operation as
// the unit under test.
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
