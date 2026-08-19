package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const maxLocalTargetAdministrationBytes = 16 * 1024

type LocalTargetInspection struct {
	binding             LocalTargetBinding
	baseHead            GitObjectID
	featureRefExists    bool
	featureHead         GitObjectID
	featureReflogMarker string
	registeredWorktrees map[string]registeredWorktree
}

func (inspection LocalTargetInspection) Binding() LocalTargetBinding {
	return inspection.binding
}
func (inspection LocalTargetInspection) BaseHead() GitObjectID {
	return inspection.baseHead
}
func (inspection LocalTargetInspection) FeatureRefExists() bool {
	return inspection.featureRefExists
}
func (inspection LocalTargetInspection) FeatureHead() GitObjectID {
	return inspection.featureHead
}

type localTargetInspectionOptions struct {
	requireBaseAtPin      bool
	allowFeatureRef       bool
	expectedBinding       *LocalTargetBinding
	intentDigest          Digest
	expectedFeatureHead   GitObjectID
	expectedFeatureMarker string
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

// ValidateLocalTarget admits a new local target without creating any state.
// The feature ref must be absent because no durable workspace intent exists
// yet that could distinguish an owned ref from an unrelated one.
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
	inspection, err := DefaultLocalTargetGitAdapter().inspect(
		ctx,
		manifest.target,
		localTargetInspectionOptions{requireBaseAtPin: true},
	)
	if err != nil {
		return LocalTargetBinding{}, err
	}
	return inspection.binding, nil
}

func (adapter LocalTargetGitAdapter) inspect(
	ctx context.Context,
	target LocalTarget,
	options localTargetInspectionOptions,
) (result LocalTargetInspection, resultErr error) {
	if ctx == nil {
		return LocalTargetInspection{}, fmt.Errorf("local target inspection requires context")
	}
	if target.IsZero() {
		return LocalTargetInspection{}, fmt.Errorf(
			"local target inspection requires complete target authority",
		)
	}
	targetRoot, err := OpenVerifiedRoot(RootRoleTarget, target.root, false)
	if err != nil {
		return LocalTargetInspection{}, fmt.Errorf("open local target root: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, targetRoot.Close())
	}()
	if err := adapter.rejectBare(ctx, target.root); err != nil {
		return LocalTargetInspection{}, err
	}

	topLevel, err := adapter.runText(
		ctx, target.root,
		"rev-parse", "--path-format=absolute", "--show-toplevel",
	)
	if err != nil {
		return LocalTargetInspection{}, fmt.Errorf(
			"inspect local target worktree root: %w", err,
		)
	}
	topLevel, err = requireCanonicalObservedTargetPath(
		"target worktree", topLevel,
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if topLevel != target.root {
		return LocalTargetInspection{}, fmt.Errorf(
			"repository.root %s is not the selected Git worktree root %s",
			target.root, topLevel,
		)
	}

	gitDirectory, err := adapter.runText(
		ctx, target.root,
		"rev-parse", "--path-format=absolute", "--absolute-git-dir",
	)
	if err != nil {
		return LocalTargetInspection{}, fmt.Errorf(
			"inspect local target Git directory: %w", err,
		)
	}
	gitDirectory, err = requireCanonicalObservedTargetPath(
		"Git directory", gitDirectory,
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	commonDirectory, err := adapter.runText(
		ctx, target.root,
		"rev-parse", "--path-format=absolute", "--git-common-dir",
	)
	if err != nil {
		return LocalTargetInspection{}, fmt.Errorf(
			"inspect local target Git common directory: %w", err,
		)
	}
	commonDirectory, err = requireCanonicalObservedTargetPath(
		"Git common directory", commonDirectory,
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}

	gitRoot, err := OpenVerifiedRoot(
		RootRoleGitCommon, gitDirectory, false,
	)
	if err != nil {
		return LocalTargetInspection{}, fmt.Errorf(
			"open local target Git directory: %w", err,
		)
	}
	defer func() {
		resultErr = errors.Join(resultErr, gitRoot.Close())
	}()
	commonRoot := gitRoot
	if commonDirectory != gitDirectory {
		commonRoot, err = OpenVerifiedRoot(
			RootRoleGitCommon, commonDirectory, false,
		)
		if err != nil {
			return LocalTargetInspection{}, fmt.Errorf(
				"open local target Git common directory: %w", err,
			)
		}
		defer func() {
			resultErr = errors.Join(resultErr, commonRoot.Close())
		}()
	}
	linkedWorktree := gitDirectory != commonDirectory
	if err := validateLocalTargetWorktreeAdministration(
		targetRoot,
		gitDirectory,
		linkedWorktree,
	); err != nil {
		return LocalTargetInspection{}, err
	}

	repositoryFormat, configs, err := adapter.inspectRepositoryConfiguration(
		ctx, target.root,
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	objectFormat, err := adapter.git.objectFormat(ctx, target.root)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if err := validateLocalTargetRepositoryFormat(
		repositoryFormat, objectFormat, configs,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := rejectUnsupportedLocalTargetConfiguration(configs); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.rejectUnsupportedAdministrativeFiles(
		commonRoot, gitRoot, objectFormat,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.rejectShallow(ctx, target.root); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.rejectReplacementRefs(ctx, target.root); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.rejectSparseIndex(ctx, target.root); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.verifyObjectDatabase(ctx, target.root); err != nil {
		return LocalTargetInspection{}, err
	}

	baseHead, err := adapter.resolveCommitRef(
		ctx, target.root, target.baseRef, objectFormat,
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if options.requireBaseAtPin && baseHead != target.baseCommit {
		return LocalTargetInspection{}, fmt.Errorf(
			"base_ref %s resolves to %s, not pinned base_commit %s",
			target.baseRef, baseHead, target.baseCommit,
		)
	}
	if target.baseCommit.Algorithm() != objectFormat {
		return LocalTargetInspection{}, fmt.Errorf(
			"base_commit uses %s but repository uses %s",
			target.baseCommit.Algorithm(), objectFormat,
		)
	}
	if err := adapter.verifyExactCommit(
		ctx, target.root, target.baseCommit,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.inspectBaseTree(
		ctx, target.root, target.baseCommit,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.validateFeatureRefSyntax(
		ctx, target.root, target.featureBranch,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := validateBoundLocalTargetFeatureRefStorage(
		commonRoot, target.FeatureRef(),
	); err != nil {
		return LocalTargetInspection{}, err
	}

	featureExists, featureHead, marker, err := adapter.inspectFeatureRef(
		ctx, target.root, target.FeatureRef(), objectFormat,
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	releaseMarkerExists, _, err := adapter.inspectReleaseMarkerRef(
		ctx, target.root, target.featureBranch, objectFormat,
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if err := adapter.validateFeatureNamespace(
		ctx, target.root, target.featureBranch, true,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	registeredWorktrees, err := adapter.rejectCheckedOutFeatureBranch(
		ctx, target.root, target.FeatureRef(),
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if !options.allowFeatureRef && releaseMarkerExists {
		return LocalTargetInspection{}, releasedFeatureRefAdoptionError(
			target.FeatureRef(),
			localTargetReleaseMarkerRef(target.featureBranch),
		)
	}
	if featureExists {
		if !options.allowFeatureRef {
			return LocalTargetInspection{}, fmt.Errorf(
				"feature ref %s already exists without a durable creation intent",
				target.FeatureRef(),
			)
		}
		expectedFeatureHead := target.baseCommit
		if !options.expectedFeatureHead.IsZero() {
			expectedFeatureHead = options.expectedFeatureHead
		}
		if expectedFeatureHead.Algorithm() != objectFormat {
			return LocalTargetInspection{}, fmt.Errorf(
				"expected feature head does not use the repository object format",
			)
		}
		if featureHead != expectedFeatureHead {
			return LocalTargetInspection{}, fmt.Errorf(
				"feature ref %s is %s, expected durable head %s",
				target.FeatureRef(), featureHead, expectedFeatureHead,
			)
		}
		expectedMarker := options.expectedFeatureMarker
		if expectedMarker == "" && !options.intentDigest.IsZero() {
			expectedMarker = localTargetReflogMessage(
				options.intentDigest,
			)
		}
		if expectedMarker == "" || marker != expectedMarker {
			return LocalTargetInspection{}, fmt.Errorf(
				"feature ref %s has no exact durable workspace marker; refusing to adopt it",
				target.FeatureRef(),
			)
		}
	}

	binding, err := NewLocalTargetBinding(LocalTargetBindingOptions{
		Root:             target.root,
		GitDirectory:     gitDirectory,
		CommonDirectory:  commonDirectory,
		RepositoryFormat: repositoryFormat,
		ObjectFormat:     objectFormat,
		LinkedWorktree:   linkedWorktree,
		BaseRef:          target.baseRef, BaseCommit: target.baseCommit,
		FeatureBranch: target.featureBranch,
	})
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if options.expectedBinding != nil &&
		binding.digest != options.expectedBinding.digest {
		return LocalTargetInspection{}, fmt.Errorf(
			"local target binding changed after durable feature-ref intent (expected %s, observed %s)",
			options.expectedBinding.digest, binding.digest,
		)
	}
	if err := targetRoot.VerifyPath(); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := gitRoot.VerifyPath(); err != nil {
		return LocalTargetInspection{}, err
	}
	if err := commonRoot.VerifyPath(); err != nil {
		return LocalTargetInspection{}, err
	}
	return LocalTargetInspection{
		binding: binding, baseHead: baseHead,
		featureRefExists: featureExists, featureHead: featureHead,
		featureReflogMarker: marker,
		registeredWorktrees: registeredWorktrees,
	}, nil
}

func validateLocalTargetWorktreeAdministration(
	targetRoot *VerifiedRoot,
	gitDirectory string,
	linkedWorktree bool,
) error {
	if targetRoot == nil {
		return fmt.Errorf("local target worktree administration requires a verified target root")
	}
	info, exists, err := targetRoot.adapter.inspectExact(".git")
	if err != nil {
		return fmt.Errorf("inspect local target worktree administration: %w", err)
	}
	if !exists {
		return fmt.Errorf("local target worktree administration .git is missing")
	}
	if !linkedWorktree {
		if !info.IsDir() ||
			filepath.Join(targetRoot.Path(), ".git") != gitDirectory {
			return fmt.Errorf(
				"primary local target requires an exact .git directory",
			)
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf(
			"linked local target requires a regular .git administration file",
		)
	}
	content, err := targetRoot.ReadBounded(
		".git",
		maxLocalTargetAdministrationBytes,
	)
	if err != nil {
		return fmt.Errorf("read local target worktree administration: %w", err)
	}
	if bytes.IndexByte(content, 0) >= 0 ||
		bytes.IndexByte(content, '\r') >= 0 {
		return fmt.Errorf("local target .git administration file is malformed")
	}
	text := strings.TrimSuffix(string(content), "\n")
	if strings.Contains(text, "\n") ||
		!strings.HasPrefix(text, "gitdir: ") {
		return fmt.Errorf("local target .git administration file is malformed")
	}
	adminPath := strings.TrimPrefix(text, "gitdir: ")
	if adminPath == "" {
		return fmt.Errorf("local target .git administration file is malformed")
	}
	if !filepath.IsAbs(adminPath) {
		adminPath = filepath.Join(targetRoot.Path(), adminPath)
	}
	adminPath, err = requireCanonicalObservedTargetPath(
		"worktree administration", adminPath,
	)
	if err != nil {
		return err
	}
	if adminPath != gitDirectory {
		return fmt.Errorf(
			"local target .git administration points to %s, not %s",
			adminPath,
			gitDirectory,
		)
	}
	return nil
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

func (adapter LocalTargetGitAdapter) inspectRepositoryConfiguration(
	ctx context.Context,
	root string,
) (uint64, map[string][]string, error) {
	configs, err := adapter.readConfigScope(ctx, root, "--local")
	if err != nil {
		return 0, nil, fmt.Errorf("inspect local target configuration: %w", err)
	}
	format := uint64(0)
	if values := configs["core.repositoryformatversion"]; len(values) != 0 {
		if len(values) != 1 {
			return 0, nil, fmt.Errorf(
				"local target has duplicate core.repositoryformatversion values",
			)
		}
		parsed, parseErr := strconv.ParseUint(strings.TrimSpace(values[0]), 10, 64)
		if parseErr != nil {
			return 0, nil, fmt.Errorf(
				"invalid core.repositoryformatversion %q", values[0],
			)
		}
		format = parsed
	}
	if configTrue(configs["extensions.worktreeconfig"]) {
		worktree, worktreeErr := adapter.readConfigScope(
			ctx, root, "--worktree",
		)
		if worktreeErr != nil {
			return 0, nil, fmt.Errorf(
				"inspect local target worktree configuration: %w",
				worktreeErr,
			)
		}
		for key, values := range worktree {
			configs[key] = append(configs[key], values...)
		}
	}
	return format, configs, nil
}

func (adapter LocalTargetGitAdapter) readConfigScope(
	ctx context.Context,
	root, scope string,
) (map[string][]string, error) {
	output, exitCode, err := adapter.git.run(
		ctx, root,
		"config", scope, "--no-includes", "--null", "--list",
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

func validateLocalTargetRepositoryFormat(
	repositoryFormat uint64,
	objectFormat GitHashAlgorithm,
	configs map[string][]string,
) error {
	switch repositoryFormat {
	case 0:
		if objectFormat != GitHashSHA1 {
			return fmt.Errorf(
				"Git repository format 0 cannot use %s objects", objectFormat,
			)
		}
	case 1:
		// Format 1 is required by SHA-256 and is also supported for the
		// worktreeConfig extension in SHA-1 repositories.
	default:
		return fmt.Errorf(
			"unsupported Git repository format version %d",
			repositoryFormat,
		)
	}
	for key := range configs {
		if !strings.HasPrefix(key, "extensions.") {
			continue
		}
		switch key {
		case "extensions.objectformat", "extensions.worktreeconfig",
			"extensions.partialclone":
		default:
			return fmt.Errorf(
				"unsupported local target repository extension %s", key,
			)
		}
	}
	if values := configs["extensions.objectformat"]; len(values) != 0 {
		if len(values) != 1 ||
			GitHashAlgorithm(strings.ToLower(strings.TrimSpace(values[0]))) != objectFormat {
			return fmt.Errorf(
				"repository object format does not match extensions.objectFormat",
			)
		}
	}
	return nil
}

func rejectUnsupportedLocalTargetConfiguration(
	configs map[string][]string,
) error {
	for key, values := range configs {
		lower := strings.ToLower(key)
		switch {
		case lower == "core.bare" && configTrue(values):
			return fmt.Errorf("bare repositories are not supported")
		case lower == "extensions.partialclone":
			return fmt.Errorf("partial-clone extension is not supported")
		case strings.HasPrefix(lower, "remote.") &&
			(strings.HasSuffix(lower, ".partialclonefilter") ||
				strings.HasSuffix(lower, ".promisor")):
			if !strings.HasSuffix(lower, ".promisor") || configTrue(values) {
				return fmt.Errorf(
					"partial/promisor remote configuration %s is not supported",
					key,
				)
			}
		case lower == "core.sparsecheckout" ||
			lower == "core.sparsecheckoutcone" ||
			lower == "index.sparse":
			if configTrue(values) {
				return fmt.Errorf(
					"sparse checkout configuration %s is not supported", key,
				)
			}
		case strings.HasPrefix(lower, "submodule."):
			return fmt.Errorf("submodule configuration %s is not supported", key)
		case strings.HasPrefix(lower, "filter.") &&
			(strings.HasSuffix(lower, ".clean") ||
				strings.HasSuffix(lower, ".smudge") ||
				strings.HasSuffix(lower, ".process") ||
				strings.HasSuffix(lower, ".required")):
			if !strings.HasSuffix(lower, ".required") ||
				configTrue(values) {
				return fmt.Errorf(
					"active Git filter configuration %s is not supported", key,
				)
			}
		case lower == "core.attributesfile":
			return fmt.Errorf(
				"external Git attributes configuration is not supported",
			)
		case lower == "core.fsmonitor":
			if configTrue(values) || configHasCommand(values) {
				return fmt.Errorf(
					"active fsmonitor configuration is not supported",
				)
			}
		case lower == "core.fsmonitorhookversion":
			return fmt.Errorf("fsmonitor hook configuration is not supported")
		case strings.HasPrefix(lower, "diff.") &&
			(strings.HasSuffix(lower, ".command") ||
				strings.HasSuffix(lower, ".textconv")):
			return fmt.Errorf(
				"external diff/text conversion configuration %s is not supported",
				key,
			)
		case strings.HasPrefix(lower, "merge.") &&
			strings.HasSuffix(lower, ".driver"):
			return fmt.Errorf(
				"external merge driver configuration %s is not supported", key,
			)
		case lower == "log.showsignature" && configTrue(values):
			return fmt.Errorf(
				"signature display configuration is not supported",
			)
		case lower == "gpg.program" ||
			(strings.HasPrefix(lower, "gpg.") &&
				strings.HasSuffix(lower, ".program")) ||
			lower == "gpg.ssh.defaultkeycommand":
			return fmt.Errorf(
				"external signature program configuration %s is not supported",
				key,
			)
		case lower == "core.alternaterefscommand":
			return fmt.Errorf("alternate refs command is not supported")
		case lower == "include.path" ||
			strings.HasPrefix(lower, "includeif."):
			return fmt.Errorf(
				"repository configuration includes are not supported: %s", key,
			)
		}
	}
	return nil
}

func configTrue(values []string) bool {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch normalized {
		case "true", "yes", "on", "1":
			return true
		case "", "false", "no", "off", "0":
			continue
		}
		numeric := normalized
		if strings.HasSuffix(numeric, "k") ||
			strings.HasSuffix(numeric, "m") ||
			strings.HasSuffix(numeric, "g") {
			numeric = numeric[:len(numeric)-1]
		}
		parsed, err := strconv.ParseInt(numeric, 10, 64)
		if err != nil || parsed != 0 {
			// Git rejects malformed booleans. Treat them as active here so
			// repository admission remains fail-closed.
			return true
		}
	}
	return false
}

func configHasCommand(values []string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			switch strings.ToLower(value) {
			case "false", "no", "off", "0":
			default:
				return true
			}
		}
	}
	return false
}

func (adapter LocalTargetGitAdapter) rejectUnsupportedAdministrativeFiles(
	commonRoot, gitRoot *VerifiedRoot,
	objectFormat GitHashAlgorithm,
) error {
	checks := []struct {
		root  *VerifiedRoot
		path  string
		label string
	}{
		{commonRoot, "objects/info/alternates", "alternate object database"},
		{commonRoot, "objects/info/http-alternates", "HTTP alternate object database"},
		{commonRoot, "info/grafts", "Git graft"},
		{commonRoot, "shallow", "shallow repository"},
		{commonRoot, "info/attributes", "external Git attributes"},
		{gitRoot, "info/sparse-checkout", "sparse checkout"},
	}
	seen := make(map[string]struct{})
	for _, check := range checks {
		key := check.root.Path() + "\x00" + check.path
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		_, exists, err := check.root.adapter.inspectExact(check.path)
		if err != nil {
			return fmt.Errorf("inspect %s metadata: %w", check.label, err)
		}
		if exists {
			return fmt.Errorf(
				"%s metadata %s is not supported",
				check.label, filepath.Join(check.root.Path(), check.path),
			)
		}
	}
	packEntries, err := commonRoot.adapter.readDirectory("objects/pack")
	if err != nil {
		return fmt.Errorf("inspect local target pack metadata: %w", err)
	}
	for _, entry := range packEntries {
		if strings.HasSuffix(strings.ToLower(entry.name), ".promisor") {
			return fmt.Errorf(
				"promisor pack metadata %s is not supported",
				filepath.Join(commonRoot.Path(), "objects", "pack", entry.name),
			)
		}
		if !entry.info.Mode().IsRegular() {
			return fmt.Errorf(
				"local target pack entry %s is not a regular file",
				filepath.Join(commonRoot.Path(), "objects", "pack", entry.name),
			)
		}
		if err := verifyLocalTargetObjectFile(
			commonRoot,
			path.Join("objects/pack", entry.name),
			"pack",
		); err != nil {
			return err
		}
	}
	objectEntries, err := commonRoot.adapter.readDirectory("objects")
	if err != nil {
		return fmt.Errorf("inspect local target object database entries: %w", err)
	}
	looseNameLength := 38
	if objectFormat == GitHashSHA256 {
		looseNameLength = 62
	}
	for _, directory := range objectEntries {
		if directory.name == "info" || directory.name == "pack" {
			if !directory.info.IsDir() {
				return fmt.Errorf(
					"local target object database entry %s is not a directory",
					filepath.Join(commonRoot.Path(), "objects", directory.name),
				)
			}
			continue
		}
		if len(directory.name) != 2 || !lowerHex(directory.name) {
			return fmt.Errorf(
				"unexpected local target object database entry %s",
				filepath.Join(commonRoot.Path(), "objects", directory.name),
			)
		}
		if !directory.info.IsDir() {
			return fmt.Errorf(
				"loose object directory %s is not a directory",
				filepath.Join(commonRoot.Path(), "objects", directory.name),
			)
		}
		relativeDirectory := path.Join("objects", directory.name)
		looseEntries, err := commonRoot.adapter.readDirectory(relativeDirectory)
		if err != nil {
			return fmt.Errorf(
				"inspect loose object directory %s: %w",
				filepath.Join(commonRoot.Path(), filepath.FromSlash(relativeDirectory)),
				err,
			)
		}
		for _, entry := range looseEntries {
			if len(entry.name) != looseNameLength || !lowerHex(entry.name) {
				return fmt.Errorf(
					"unexpected loose object entry %s",
					filepath.Join(
						commonRoot.Path(),
						filepath.FromSlash(relativeDirectory),
						entry.name,
					),
				)
			}
			if !entry.info.Mode().IsRegular() {
				return fmt.Errorf(
					"loose object entry %s is not a regular file",
					filepath.Join(
						commonRoot.Path(),
						filepath.FromSlash(relativeDirectory),
						entry.name,
					),
				)
			}
			if err := verifyLocalTargetObjectFile(
				commonRoot,
				path.Join(relativeDirectory, entry.name),
				"loose object",
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyLocalTargetObjectFile(
	root *VerifiedRoot,
	relative string,
	label string,
) error {
	file, _, err := root.adapter.openRegularFileExact(
		relative, os.O_RDONLY, 0, false,
	)
	if err != nil {
		return fmt.Errorf("open local target %s %s: %w", label, relative, err)
	}
	defer file.Close()
	if err := root.verifyOwnedRegularFile(relative, file); err != nil {
		return fmt.Errorf("verify local target %s %s: %w", label, relative, err)
	}
	return nil
}

func lowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (adapter LocalTargetGitAdapter) rejectBare(
	ctx context.Context,
	root string,
) error {
	bare, err := adapter.runText(ctx, root, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("inspect bare repository state: %w", err)
	}
	if bare != "false" {
		return fmt.Errorf("bare repositories are not supported")
	}
	return nil
}

func (adapter LocalTargetGitAdapter) rejectShallow(
	ctx context.Context,
	root string,
) error {
	shallow, err := adapter.runText(
		ctx, root, "rev-parse", "--is-shallow-repository",
	)
	if err != nil {
		return fmt.Errorf("inspect shallow repository state: %w", err)
	}
	if shallow != "false" {
		return fmt.Errorf("shallow repositories are not supported")
	}
	return nil
}

func (adapter LocalTargetGitAdapter) rejectReplacementRefs(
	ctx context.Context,
	root string,
) error {
	output, exitCode, err := adapter.git.run(
		ctx, root, "for-each-ref", "--format=%(refname)", "refs/replace",
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf(
			"inspect replacement refs: Git exited with status %d", exitCode,
		)
	}
	if strings.TrimSpace(string(output)) != "" {
		return fmt.Errorf("replacement refs are not supported")
	}
	return nil
}

func (adapter LocalTargetGitAdapter) rejectSparseIndex(
	ctx context.Context,
	root string,
) error {
	output, exitCode, err := adapter.git.run(
		ctx, root, "ls-files", "--sparse", "--stage", "-z",
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf(
			"inspect sparse index: Git exited with status %d", exitCode,
		)
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if bytes.HasPrefix(record, []byte("040000 ")) {
			return fmt.Errorf("sparse indexes are not supported")
		}
	}
	debug, exitCode, err := adapter.git.run(ctx, root, "ls-files", "--debug")
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf(
			"inspect fsmonitor index state: Git exited with status %d",
			exitCode,
		)
	}
	if strings.Contains(strings.ToLower(string(debug)), "fsmonitor valid") {
		return fmt.Errorf("fsmonitor index state is not supported")
	}
	return nil
}

func (adapter LocalTargetGitAdapter) verifyObjectDatabase(
	ctx context.Context,
	root string,
) error {
	_, exitCode, err := adapter.git.run(
		ctx, root,
		"fsck", "--full", "--strict", "--no-dangling", "--no-reflogs",
	)
	if err != nil {
		return fmt.Errorf("verify local target object database: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf(
			"local target object database is incomplete or invalid (git fsck status %d)",
			exitCode,
		)
	}
	return nil
}

func (adapter LocalTargetGitAdapter) resolveCommitRef(
	ctx context.Context,
	root, reference string,
	algorithm GitHashAlgorithm,
) (GitObjectID, error) {
	output, exitCode, err := adapter.git.run(
		ctx, root, "show-ref", "--verify", reference,
	)
	if err != nil {
		return GitObjectID{}, err
	}
	if exitCode != 0 {
		return GitObjectID{}, fmt.Errorf(
			"base_ref %s does not resolve to a local ref", reference,
		)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != reference {
		return GitObjectID{}, fmt.Errorf(
			"Git returned malformed base_ref data",
		)
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
	raw := gitObjectHex(object)
	output, exitCode, err := adapter.git.run(
		ctx, root, "cat-file", "-t", raw,
	)
	if err != nil {
		return err
	}
	if exitCode != 0 || strings.TrimSpace(string(output)) != "commit" {
		return fmt.Errorf("Git object %s is not an available commit", object)
	}
	return nil
}

type localTargetTreeEntry struct {
	mode   string
	kind   string
	object GitObjectID
	path   string
}

func (adapter LocalTargetGitAdapter) inspectBaseTree(
	ctx context.Context,
	root string,
	base GitObjectID,
) error {
	exitCode, err := adapter.git.streamNULTerminatedRecords(
		ctx, root,
		func(record []byte) error {
			entry, parseErr := parseLocalTargetTreeEntry(record, base.Algorithm())
			if parseErr != nil {
				return parseErr
			}
			if isRepositoryAttributesPath(entry.path) {
				return fmt.Errorf(
					"repository-defined .gitattributes are not supported (tree entry %s)",
					entry.path,
				)
			}
			if entry.mode == "160000" || entry.kind == "commit" ||
				entry.path == ".gitmodules" {
				return fmt.Errorf(
					"submodules are not supported (tree entry %s)", entry.path,
				)
			}
			if entry.mode == "120000" {
				target, readErr := adapter.readBlob(
					ctx, root, entry.object,
				)
				if readErr != nil {
					return readErr
				}
				if err := validateRepositorySymlink(entry.path, target); err != nil {
					return err
				}
			}
			return nil
		},
		"ls-tree", "-r", "-z", "--full-tree", gitObjectHex(base),
	)
	if err != nil {
		return fmt.Errorf("inspect pinned base tree: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf(
			"inspect pinned base tree: Git exited with status %d", exitCode,
		)
	}
	return nil
}

func parseLocalTargetTreeEntry(
	record []byte,
	algorithm GitHashAlgorithm,
) (localTargetTreeEntry, error) {
	metadata, name, found := bytes.Cut(record, []byte{'\t'})
	fields := strings.Fields(string(metadata))
	if !found || len(fields) != 3 || len(name) == 0 {
		return localTargetTreeEntry{}, fmt.Errorf(
			"Git returned malformed pinned-base tree data",
		)
	}
	object, err := qualifyGitObjectID(algorithm, fields[2])
	if err != nil {
		return localTargetTreeEntry{}, err
	}
	entryPath := string(name)
	if strings.IndexByte(entryPath, 0) >= 0 ||
		path.IsAbs(entryPath) || path.Clean(entryPath) != entryPath ||
		entryPath == "." || entryPath == ".." ||
		strings.HasPrefix(entryPath, "../") {
		return localTargetTreeEntry{}, fmt.Errorf(
			"pinned base contains invalid path %q", entryPath,
		)
	}
	return localTargetTreeEntry{
		mode: fields[0], kind: fields[1],
		object: object, path: entryPath,
	}, nil
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
		strings.HasPrefix(target, `\`) ||
		filepath.VolumeName(target) != "" {
		return fmt.Errorf(
			"symlink %s has an absolute or empty target %q",
			entryPath, target,
		)
	}
	resolved := path.Clean(path.Join(path.Dir(entryPath), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf(
			"symlink %s escapes the repository root via %q",
			entryPath, target,
		)
	}
	for _, component := range strings.Split(resolved, "/") {
		if materializationCollisionKey(component) == materializationCollisionKey(".git") {
			return fmt.Errorf(
				"symlink %s targets Git administration via %q",
				entryPath, target,
			)
		}
	}
	return nil
}

func (adapter LocalTargetGitAdapter) validateFeatureRefSyntax(
	ctx context.Context,
	root, branch string,
) error {
	_, exitCode, err := adapter.git.run(
		ctx, root, "check-ref-format", "--branch", branch,
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("Git rejected feature branch %q", branch)
	}
	return nil
}

func (adapter LocalTargetGitAdapter) validateFeatureNamespace(
	ctx context.Context,
	root, branch string,
	allowExact bool,
) error {
	output, exitCode, err := adapter.git.run(
		ctx, root, "for-each-ref", "--format=%(refname)", "refs/heads",
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf(
			"inspect local feature-ref namespace: Git exited with status %d",
			exitCode,
		)
	}
	refs, err := parseLocalHeadRefs(output)
	if err != nil {
		return err
	}
	return CheckAttemptRefConflicts(branch, refs, allowExact)
}

func (adapter LocalTargetGitAdapter) rejectCheckedOutFeatureBranch(
	ctx context.Context,
	root, featureRef string,
) (map[string]registeredWorktree, error) {
	output, exitCode, err := adapter.git.run(
		ctx, root, "worktree", "list", "--porcelain", "-z",
	)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf(
			"inspect target worktrees: Git exited with status %d", exitCode,
		)
	}
	worktrees, err := parseRegisteredWorktrees(output)
	if err != nil {
		return nil, err
	}
	branch := strings.TrimPrefix(featureRef, "refs/heads/")
	for worktreePath, worktree := range worktrees {
		if worktree.branch == branch {
			return nil, fmt.Errorf(
				"feature branch %s is already checked out at %s",
				branch, worktreePath,
			)
		}
	}
	return worktrees, nil
}

func (adapter LocalTargetGitAdapter) inspectFeatureRef(
	ctx context.Context,
	root, featureRef string,
	algorithm GitHashAlgorithm,
) (bool, GitObjectID, string, error) {
	output, exitCode, err := adapter.git.run(
		ctx, root, "show-ref", "--verify", featureRef,
	)
	if err != nil {
		return false, GitObjectID{}, "", err
	}
	if exitCode == 1 || exitCode == 128 {
		return false, GitObjectID{}, "", nil
	}
	if exitCode != 0 {
		return false, GitObjectID{}, "", fmt.Errorf(
			"inspect feature ref %s: Git exited with status %d",
			featureRef, exitCode,
		)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != featureRef {
		return false, GitObjectID{}, "", fmt.Errorf(
			"Git returned malformed feature-ref data",
		)
	}
	head, err := qualifyGitObjectID(algorithm, fields[0])
	if err != nil {
		return false, GitObjectID{}, "", err
	}
	reflog, reflogExit, err := adapter.git.run(
		ctx, root,
		"reflog", "show", "--no-show-signature",
		"--format=%gs", "-n", "1", featureRef, "--",
	)
	if err != nil {
		return false, GitObjectID{}, "", err
	}
	marker := ""
	if reflogExit == 0 {
		marker = strings.TrimSpace(string(reflog))
	} else if reflogExit != 1 {
		return false, GitObjectID{}, "", fmt.Errorf(
			"inspect feature ref reflog: Git exited with status %d",
			reflogExit,
		)
	}
	return true, head, marker, nil
}

func (adapter LocalTargetGitAdapter) inspectReleaseMarkerRef(
	ctx context.Context,
	root, featureBranch string,
	algorithm GitHashAlgorithm,
) (bool, GitObjectID, error) {
	markerRef := localTargetReleaseMarkerRef(featureBranch)
	output, exitCode, err := adapter.git.run(
		ctx, root, "show-ref", "--verify", markerRef,
	)
	if err != nil {
		return false, GitObjectID{}, err
	}
	if exitCode == 1 || exitCode == 128 {
		return false, GitObjectID{}, nil
	}
	if exitCode != 0 {
		return false, GitObjectID{}, fmt.Errorf(
			"inspect feature-ref release marker %s: Git exited with status %d",
			markerRef, exitCode,
		)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != markerRef {
		return false, GitObjectID{}, fmt.Errorf(
			"Git returned malformed feature-ref release marker data",
		)
	}
	head, err := qualifyGitObjectID(algorithm, fields[0])
	if err != nil {
		return false, GitObjectID{}, err
	}
	return true, head, nil
}

func localTargetReflogMessage(intentDigest Digest) string {
	return "feature-implement feature-ref creation " + intentDigest.String()
}

const localTargetReleaseReflogPrefix = "feature-implement feature-ref released "

const localTargetReleaseMarkerRefPrefix = "refs/feature-implement/released/"

func localTargetReleaseReflogMessage(intentDigest Digest) string {
	return localTargetReleaseReflogPrefix + intentDigest.String()
}

func localTargetReleaseMarkerRef(featureBranch string) string {
	return localTargetReleaseMarkerRefPrefix + featureBranch
}

func releasedFeatureRefAdoptionError(featureRef, markerRef string) error {
	return fmt.Errorf(
		"feature ref %s was released by an abandoned workspace (marker %s); choose a new feature branch",
		featureRef, markerRef,
	)
}

func gitObjectHex(object GitObjectID) string {
	return fmt.Sprintf("%x", object.Bytes())
}

func (adapter LocalTargetGitAdapter) verifyOwnedFeatureRefAt(
	ctx context.Context,
	binding LocalTargetBinding,
	expectedHead GitObjectID,
	expectedMarker string,
) (LocalTargetInspection, error) {
	target := LocalTarget{
		root: binding.root, baseRef: binding.baseRef,
		baseCommit: binding.baseCommit, featureBranch: binding.featureBranch,
	}
	return adapter.inspect(ctx, target, localTargetInspectionOptions{
		requireBaseAtPin: false, allowFeatureRef: true,
		expectedBinding:       &binding,
		expectedFeatureHead:   expectedHead,
		expectedFeatureMarker: expectedMarker,
	})
}

func (adapter LocalTargetGitAdapter) EnsureReleasedFeatureRefMarker(
	ctx context.Context,
	binding LocalTargetBinding,
	ownedHead GitObjectID,
	intentDigest Digest,
) (resultErr error) {
	if ctx == nil || binding.IsZero() || ownedHead.IsZero() ||
		ownedHead.Algorithm() != binding.objectFormat || intentDigest.IsZero() {
		return fmt.Errorf(
			"feature-ref release marker requires context, binding, owned head, and intent digest",
		)
	}
	session, err := adapter.openBoundSession(binding)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.Close())
	}()
	return session.ensureReleasedFeatureRefMarker(
		ctx, ownedHead, intentDigest,
	)
}

func (adapter LocalTargetGitAdapter) VerifyReleasedFeatureRefAt(
	ctx context.Context,
	binding LocalTargetBinding,
	ownedHead GitObjectID,
	intentDigest Digest,
) (inspection LocalTargetInspection, resultErr error) {
	if ctx == nil || binding.IsZero() || ownedHead.IsZero() ||
		ownedHead.Algorithm() != binding.objectFormat || intentDigest.IsZero() {
		return LocalTargetInspection{}, fmt.Errorf(
			"released feature-ref verification requires context, binding, owned head, and intent digest",
		)
	}
	session, err := adapter.openBoundSession(binding)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.Close())
	}()
	if err := session.verifyReleasedFeatureRefMarker(
		ctx, ownedHead, intentDigest,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	return LocalTargetInspection{binding: binding}, nil
}

func (adapter LocalTargetGitAdapter) inspectUncreatedTarget(
	ctx context.Context,
	target LocalTarget,
) (LocalTargetInspection, error) {
	return adapter.inspect(ctx, target, localTargetInspectionOptions{
		requireBaseAtPin: true,
	})
}

func (adapter LocalTargetGitAdapter) inspectIntendedTarget(
	ctx context.Context,
	binding LocalTargetBinding,
	intentDigest Digest,
) (LocalTargetInspection, error) {
	target := LocalTarget{
		root: binding.root, baseRef: binding.baseRef,
		baseCommit: binding.baseCommit, featureBranch: binding.featureBranch,
	}
	return adapter.inspect(ctx, target, localTargetInspectionOptions{
		requireBaseAtPin: true, allowFeatureRef: true,
		expectedBinding: &binding, intentDigest: intentDigest,
	})
}

func (adapter LocalTargetGitAdapter) refuseConfiguredExternalPrograms(
	configs map[string][]string,
) error {
	return rejectUnsupportedLocalTargetConfiguration(configs)
}
