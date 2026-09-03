package workspace

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

type localTargetInspectionOptions struct {
	requireBaseAtPin     bool
	requireFeatureRef    bool
	expectedBinding      *LocalTargetBinding
	expectedFeatureHeads []GitObjectID
}

type LocalTargetGitAdapter struct {
	git LocalAttemptGitAdapter
}

func NewLocalTargetGitAdapter(
	executable string,
	environment []EnvironmentVariable,
) (LocalTargetGitAdapter, error) {
	git, err := NewLocalAttemptGitAdapter(executable, environment)
	if err != nil {
		return LocalTargetGitAdapter{}, err
	}
	return LocalTargetGitAdapter{git: git}, nil
}

func DefaultLocalTargetGitAdapter() LocalTargetGitAdapter {
	adapter, _ := NewLocalTargetGitAdapter("git", nil)
	return adapter
}

// ValidateLocalTarget admits a new target without changing it. The feature
// ref must be absent until the first integration publishes its merge commit.
func ValidateLocalTarget(
	ctx context.Context,
	manifest WorkspaceManifest,
) (LocalTargetBinding, error) {
	if ctx == nil {
		return LocalTargetBinding{}, fmt.Errorf("local target validation requires context")
	}
	if manifest.mode != WorkspaceModeLocal || manifest.target.IsZero() {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target validation requires a local workspace manifest",
		)
	}
	return DefaultLocalTargetGitAdapter().inspect(
		ctx, manifest.target,
		localTargetInspectionOptions{requireBaseAtPin: true},
	)
}

func (adapter LocalTargetGitAdapter) inspect(
	ctx context.Context,
	target LocalTarget,
	options localTargetInspectionOptions,
) (LocalTargetBinding, error) {
	if ctx == nil {
		return LocalTargetBinding{}, fmt.Errorf("local target inspection requires context")
	}
	if target.IsZero() {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target inspection requires complete target authority",
		)
	}
	root, err := requireCanonicalObservedTargetPath("target worktree", target.root)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if err := adapter.rejectBare(ctx, root); err != nil {
		return LocalTargetBinding{}, err
	}
	topLevel, err := adapter.runText(
		ctx, root,
		"rev-parse", "--path-format=absolute", "--show-toplevel",
	)
	if err != nil {
		return LocalTargetBinding{}, fmt.Errorf(
			"inspect local target worktree root: %w", err,
		)
	}
	topLevel, err = requireCanonicalObservedTargetPath("target worktree", topLevel)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if topLevel != root {
		return LocalTargetBinding{}, fmt.Errorf(
			"repository.root %s is not the selected primary Git worktree root %s",
			root, topLevel,
		)
	}

	gitDirectory, err := adapter.runText(
		ctx, root,
		"rev-parse", "--path-format=absolute", "--absolute-git-dir",
	)
	if err != nil {
		return LocalTargetBinding{}, fmt.Errorf("inspect local target Git directory: %w", err)
	}
	gitDirectory, err = requireCanonicalObservedTargetPath("Git directory", gitDirectory)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	commonDirectory, err := adapter.runText(
		ctx, root,
		"rev-parse", "--path-format=absolute", "--git-common-dir",
	)
	if err != nil {
		return LocalTargetBinding{}, fmt.Errorf("inspect local target Git common directory: %w", err)
	}
	commonDirectory, err = requireCanonicalObservedTargetPath("Git common directory", commonDirectory)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if gitDirectory != commonDirectory {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target must be a primary local worktree, not a linked worktree",
		)
	}

	configs, err := adapter.readConfigScope(ctx, root, "--local")
	if err != nil {
		return LocalTargetBinding{}, fmt.Errorf("inspect local target configuration: %w", err)
	}
	if err := rejectExecutingLocalTargetConfiguration(configs); err != nil {
		return LocalTargetBinding{}, err
	}
	objectFormat, err := adapter.git.objectFormat(ctx, root)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if target.baseCommit.Algorithm() != objectFormat {
		return LocalTargetBinding{}, fmt.Errorf(
			"base_commit uses %s but repository uses %s",
			target.baseCommit.Algorithm(), objectFormat,
		)
	}
	baseHead, err := adapter.resolveCommitRef(ctx, root, target.baseRef, objectFormat)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if options.requireBaseAtPin && baseHead != target.baseCommit {
		return LocalTargetBinding{}, fmt.Errorf(
			"base_ref %s resolves to %s, not pinned base_commit %s",
			target.baseRef, baseHead, target.baseCommit,
		)
	}
	if err := adapter.verifyExactCommit(ctx, root, target.baseCommit); err != nil {
		return LocalTargetBinding{}, err
	}
	if err := adapter.validateFeatureRefSyntax(ctx, root, target.featureBranch); err != nil {
		return LocalTargetBinding{}, err
	}
	featureExists, featureHead, err := adapter.inspectFeatureRef(
		ctx, root, target.FeatureRef(), objectFormat,
	)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if err := adapter.rejectCheckedOutFeatureBranch(ctx, root, target.FeatureRef()); err != nil {
		return LocalTargetBinding{}, err
	}
	if !featureExists {
		if options.requireFeatureRef {
			return LocalTargetBinding{}, fmt.Errorf(
				"feature ref %s is absent, expected recorded head %s",
				target.FeatureRef(), expectedFeatureRefHeads(options.expectedFeatureHeads),
			)
		}
	} else {
		if len(options.expectedFeatureHeads) == 0 {
			return LocalTargetBinding{}, fmt.Errorf(
				"feature ref %s already exists before a recorded integration",
				target.FeatureRef(),
			)
		}
		expected := false
		for _, candidate := range options.expectedFeatureHeads {
			if candidate.Algorithm() == objectFormat && featureHead == candidate {
				expected = true
				break
			}
		}
		if !expected {
			return LocalTargetBinding{}, fmt.Errorf(
				"feature ref %s is %s, expected recorded head %s",
				target.FeatureRef(), featureHead,
				expectedFeatureRefHeads(options.expectedFeatureHeads),
			)
		}
	}
	binding, err := NewLocalTargetBinding(LocalTargetBindingOptions{
		Root:          root,
		ObjectFormat:  objectFormat,
		BaseRef:       target.baseRef,
		BaseCommit:    target.baseCommit,
		FeatureBranch: target.featureBranch,
	})
	if err != nil {
		return LocalTargetBinding{}, err
	}
	if options.expectedBinding != nil && binding.digest != options.expectedBinding.digest {
		return LocalTargetBinding{}, fmt.Errorf(
			"local target binding differs from the recorded integration target",
		)
	}
	return binding, nil
}

func expectedFeatureRefHeads(heads []GitObjectID) string {
	values := make([]string, 0, len(heads))
	for _, head := range heads {
		values = append(values, head.String())
	}
	return strings.Join(values, " or ")
}

func (adapter LocalTargetGitAdapter) runText(
	ctx context.Context,
	root string,
	arguments ...string,
) (string, error) {
	output, exitCode, err := adapter.git.run(ctx, root, arguments...)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", fmt.Errorf("Git exited with status %d", exitCode)
	}
	if bytes.IndexByte(output, 0) >= 0 {
		return "", fmt.Errorf("Git returned an unexpected NUL byte")
	}
	return strings.TrimSpace(string(output)), nil
}

func requireCanonicalObservedTargetPath(role, value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("local target %s is not absolute", role)
	}
	canonical, err := canonicalizeTrustedRootPath(value)
	if err != nil {
		return "", err
	}
	evaluated, err := filepath.EvalSymlinks(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize local target %s: %w", role, err)
	}
	evaluated, err = canonicalizeTrustedRootPath(filepath.Clean(evaluated))
	if err != nil {
		return "", err
	}
	if canonical != evaluated {
		return "", fmt.Errorf(
			"local target %s %s contains a symbolic-link path component resolving to %s",
			role, canonical, evaluated,
		)
	}
	return canonical, nil
}

func (adapter LocalTargetGitAdapter) readConfigScope(
	ctx context.Context,
	root, scope string,
) (map[string][]string, error) {
	output, exitCode, err := adapter.git.run(
		ctx, root, "config", scope, "--no-includes", "--null", "--list",
	)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("Git exited with status %d", exitCode)
	}
	result := make(map[string][]string)
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		keyBytes, valueBytes, found := bytes.Cut(record, []byte{'\n'})
		if !found {
			keyBytes = record
			valueBytes = []byte("true")
		}
		key := strings.ToLower(strings.TrimSpace(string(keyBytes)))
		if key == "" {
			return nil, fmt.Errorf("Git returned an empty configuration key")
		}
		result[key] = append(result[key], string(valueBytes))
	}
	return result, nil
}

// rejectExecutingLocalTargetConfiguration is limited to settings that can
// bypass the command-line protections or execute a program for an operation
// used by this workflow. Hooks, signing, attributes, global configuration,
// and replacement refs are independently disabled by the Git invocation.
func rejectExecutingLocalTargetConfiguration(configs map[string][]string) error {
	for key, values := range configs {
		lower := strings.ToLower(key)
		switch {
		case strings.HasPrefix(lower, "diff.") &&
			(strings.HasSuffix(lower, ".command") || strings.HasSuffix(lower, ".textconv")):
			return fmt.Errorf("external diff or text conversion configuration %s is not supported", key)
		case lower == "core.alternaterefscommand":
			return fmt.Errorf("external alternate refs command is not supported")
		case strings.HasPrefix(lower, "filter.") &&
			(strings.HasSuffix(lower, ".clean") || strings.HasSuffix(lower, ".smudge") || strings.HasSuffix(lower, ".process")):
			for _, value := range values {
				if strings.TrimSpace(value) != "" {
					return fmt.Errorf("external Git filter configuration %s is not supported", key)
				}
			}
		}
	}
	return nil
}

func (adapter LocalTargetGitAdapter) rejectBare(ctx context.Context, root string) error {
	bare, err := adapter.runText(ctx, root, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("inspect bare repository state: %w", err)
	}
	if bare != "false" {
		return fmt.Errorf("bare repositories are not supported")
	}
	return nil
}

func (adapter LocalTargetGitAdapter) resolveCommitRef(
	ctx context.Context,
	root, reference string,
	algorithm GitHashAlgorithm,
) (GitObjectID, error) {
	output, exitCode, err := adapter.git.run(ctx, root, "show-ref", "--verify", reference)
	if err != nil {
		return GitObjectID{}, err
	}
	if exitCode != 0 {
		return GitObjectID{}, fmt.Errorf("base_ref %s does not resolve to a local ref", reference)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != reference {
		return GitObjectID{}, fmt.Errorf("Git returned malformed base_ref data")
	}
	object, err := qualifyGitObjectID(algorithm, fields[0])
	if err != nil {
		return GitObjectID{}, fmt.Errorf("parse base_ref object: %w", err)
	}
	if err := adapter.verifyExactCommit(ctx, root, object); err != nil {
		return GitObjectID{}, err
	}
	return object, nil
}

func (adapter LocalTargetGitAdapter) verifyExactCommit(
	ctx context.Context,
	root string,
	object GitObjectID,
) error {
	output, exitCode, err := adapter.git.run(ctx, root, "cat-file", "-t", gitObjectHex(object))
	if err != nil {
		return err
	}
	if exitCode != 0 || strings.TrimSpace(string(output)) != "commit" {
		return fmt.Errorf("Git object %s is not an available commit", object)
	}
	return nil
}

func (adapter LocalTargetGitAdapter) readBlob(
	ctx context.Context,
	root string,
	object GitObjectID,
) ([]byte, error) {
	output, exitCode, err := adapter.git.run(
		ctx, root, "cat-file", "blob", gitObjectHex(object),
	)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("read Git blob %s: status %d", object, exitCode)
	}
	return output, nil
}

func validateRepositorySymlink(entryPath string, rawTarget []byte) error {
	if bytes.IndexByte(rawTarget, 0) >= 0 {
		return fmt.Errorf("symlink %s contains a NUL target", entryPath)
	}
	target := string(rawTarget)
	if target == "" || path.IsAbs(target) ||
		strings.HasPrefix(target, `\`) || filepath.VolumeName(target) != "" {
		return fmt.Errorf("symlink %s has an absolute or empty target %q", entryPath, target)
	}
	resolved := path.Clean(path.Join(path.Dir(entryPath), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink %s escapes the repository root via %q", entryPath, target)
	}
	for _, component := range strings.Split(resolved, "/") {
		if materializationCollisionKey(component) == materializationCollisionKey(".git") {
			return fmt.Errorf("symlink %s targets Git administration via %q", entryPath, target)
		}
	}
	return nil
}

func (adapter LocalTargetGitAdapter) validateFeatureRefSyntax(
	ctx context.Context,
	root, branch string,
) error {
	_, exitCode, err := adapter.git.run(ctx, root, "check-ref-format", "--branch", branch)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("Git rejected feature branch %q", branch)
	}
	return nil
}

func (adapter LocalTargetGitAdapter) rejectCheckedOutFeatureBranch(
	ctx context.Context,
	root, featureRef string,
) error {
	output, exitCode, err := adapter.git.run(
		ctx, root, "worktree", "list", "--porcelain", "-z",
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("inspect target worktrees: Git exited with status %d", exitCode)
	}
	worktrees, err := parseRegisteredWorktrees(output)
	if err != nil {
		return err
	}
	branch := strings.TrimPrefix(featureRef, "refs/heads/")
	for worktreePath, worktree := range worktrees {
		if worktree.branch == branch {
			return fmt.Errorf("feature branch %s is already checked out at %s", branch, worktreePath)
		}
	}
	return nil
}

func (adapter LocalTargetGitAdapter) inspectFeatureRef(
	ctx context.Context,
	root, featureRef string,
	algorithm GitHashAlgorithm,
) (bool, GitObjectID, error) {
	output, exitCode, err := adapter.git.run(ctx, root, "show-ref", "--verify", featureRef)
	if err != nil {
		return false, GitObjectID{}, err
	}
	if exitCode == 1 || exitCode == 128 {
		return false, GitObjectID{}, nil
	}
	if exitCode != 0 {
		return false, GitObjectID{}, fmt.Errorf("inspect feature ref %s: Git exited with status %d", featureRef, exitCode)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != featureRef {
		return false, GitObjectID{}, fmt.Errorf("Git returned malformed feature-ref data")
	}
	head, err := qualifyGitObjectID(algorithm, fields[0])
	if err != nil {
		return false, GitObjectID{}, err
	}
	return true, head, nil
}

func gitObjectHex(object GitObjectID) string {
	return fmt.Sprintf("%x", object.Bytes())
}

func (adapter LocalTargetGitAdapter) verifyOwnedFeatureRefAt(
	ctx context.Context,
	binding LocalTargetBinding,
	expectedHead GitObjectID,
) (LocalTargetBinding, error) {
	target := LocalTarget{
		root: binding.root, baseRef: binding.baseRef,
		baseCommit: binding.baseCommit, featureBranch: binding.featureBranch,
	}
	return adapter.inspect(ctx, target, localTargetInspectionOptions{
		requireBaseAtPin: false, requireFeatureRef: true,
		expectedBinding: &binding, expectedFeatureHeads: []GitObjectID{expectedHead},
	})
}

func (adapter LocalTargetGitAdapter) verifyPendingIntegrationFeatureRef(
	ctx context.Context,
	binding LocalTargetBinding,
	intent MergeUnitIntegrationIntent,
) (LocalTargetBinding, error) {
	target := LocalTarget{
		root: binding.root, baseRef: binding.baseRef,
		baseCommit: binding.baseCommit, featureBranch: binding.featureBranch,
	}
	expectedHeads := []GitObjectID{intent.ExpectedMerge()}
	if !intent.ExpectedFeatureRefAbsent() {
		expectedHeads = append(
			[]GitObjectID{intent.ExpectedFeatureHead()}, expectedHeads...,
		)
	}
	return adapter.inspect(ctx, target, localTargetInspectionOptions{
		requireBaseAtPin:     false,
		requireFeatureRef:    !intent.ExpectedFeatureRefAbsent(),
		expectedBinding:      &binding,
		expectedFeatureHeads: expectedHeads,
	})
}

func (adapter LocalTargetGitAdapter) verifyFeatureRefAbsent(
	ctx context.Context,
	binding LocalTargetBinding,
) (LocalTargetBinding, error) {
	target := LocalTarget{
		root: binding.root, baseRef: binding.baseRef,
		baseCommit: binding.baseCommit, featureBranch: binding.featureBranch,
	}
	return adapter.inspect(ctx, target, localTargetInspectionOptions{
		requireBaseAtPin: false, expectedBinding: &binding,
	})
}

func (adapter LocalTargetGitAdapter) inspectUncreatedTarget(
	ctx context.Context,
	target LocalTarget,
) (LocalTargetBinding, error) {
	return adapter.inspect(ctx, target, localTargetInspectionOptions{requireBaseAtPin: true})
}
