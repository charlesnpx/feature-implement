package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	planCheckpointAuthorName  = "Feature Implement"
	planCheckpointAuthorEmail = "feature-implement@localhost"
	planCheckpointMainRef     = "refs/heads/main"
)

type planCheckpointGitAdapter struct {
	git  LocalAttemptGitAdapter
	root string
}

type planCheckpointMetadata struct {
	kind         PlanCheckpointKind
	occurredAt   time.Time
	source       Digest
	semantic     Digest
	generation   Digest
	revisionID   ID
	reviewDigest Digest
	lock         Digest
}

type planCheckpointCommit struct {
	id       GitObjectID
	tree     GitObjectID
	parents  []GitObjectID
	metadata planCheckpointMetadata
}

type planGitRunOptions struct {
	input     []byte
	indexPath string
	identity  *time.Time
}

func newPlanCheckpointGitAdapter(executable, root string) (planCheckpointGitAdapter, error) {
	git, err := NewLocalAttemptGitAdapter(executable, nil)
	if err != nil {
		return planCheckpointGitAdapter{}, err
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return planCheckpointGitAdapter{}, fmt.Errorf("plan repository root must be absolute")
	}
	return planCheckpointGitAdapter{git: git, root: root}, nil
}

func (adapter planCheckpointGitAdapter) checkpoint(
	ctx context.Context,
	bundle WorkspaceBundle,
	kind PlanCheckpointKind,
	request planCheckpointRequest,
	fault PlanCheckpointFaultInjector,
) (PlanCheckpointResult, error) {
	root, err := OpenVerifiedRoot(RootRolePlan, bundle.root, false)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	defer root.Close()
	if root.Identity() != bundle.rootIdentity {
		return PlanCheckpointResult{}, fmt.Errorf("workspace bundle root changed before plan checkpoint")
	}

	head, err := adapter.prepareRepository(ctx, kind == PlanCheckpointInitial)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := injectPlanCheckpointFault(fault, PlanCheckpointFaultAfterRepositoryInitialization); err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := verifyCheckpointBundleRoot(root, bundle); err != nil {
		return PlanCheckpointResult{}, err
	}

	identity, err := identityForPlanBundle(bundle)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if !head.id.IsZero() {
		if err := validatePlanCheckpointTransition(head, kind); err != nil {
			if !adapter.mayBeExactRetry(head, kind, request, identity) {
				return PlanCheckpointResult{}, err
			}
		}
	}

	retryCandidate := adapter.mayBeExactRetry(head, kind, request, identity)
	if !retryCandidate {
		if err := adapter.requireCleanIndex(ctx, head); err != nil {
			return PlanCheckpointResult{}, err
		}
	}

	locks := planLockFiles{tracked: map[string][]byte{}}
	administrative := []string{}
	if kind == PlanCheckpointLock {
		if head.id.IsZero() {
			return PlanCheckpointResult{}, fmt.Errorf("lock checkpoint requires an initial or revision checkpoint")
		}
		if head.metadata.kind == PlanCheckpointLock {
			locks, administrative, err = inspectPlanLockMaterialization(bundle)
			if err != nil {
				return PlanCheckpointResult{}, err
			}
		} else {
			if err := adapter.verifyPreLockCheckpoint(ctx, root, bundle, head, identity); err != nil {
				return PlanCheckpointResult{}, err
			}
			locks, administrative, err = synchronizePlanLockMaterialization(bundle, fault)
			if err != nil {
				return PlanCheckpointResult{}, err
			}
			reloaded, err := LoadWorkspaceBundle(bundle.root)
			if err != nil {
				return PlanCheckpointResult{}, fmt.Errorf("reload bundle after lock generation: %w", err)
			}
			reloadedIdentity, err := identityForPlanBundle(reloaded)
			if err != nil {
				return PlanCheckpointResult{}, err
			}
			if reloadedIdentity != identity {
				return PlanCheckpointResult{}, fmt.Errorf("plan sources changed during lock generation")
			}
			bundle = reloaded
		}
	}

	inventory, inventoryBytes, err := buildPlanRepositoryInventory(bundle, locks, administrative)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := adapter.ensureInventoryPrecondition(ctx, root, head, inventoryBytes); err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := root.PublishReplaceable(
		PlanRepositoryInventoryFileName,
		inventoryBytes,
		0o600,
		maxPlanRepositoryInventoryBytes,
		PublicationOptions{},
	); err != nil {
		return PlanCheckpointResult{}, fmt.Errorf("publish plan repository inventory: %w", err)
	}

	reloaded, err := LoadWorkspaceBundle(bundle.root)
	if err != nil {
		return PlanCheckpointResult{}, fmt.Errorf("reload bundle before checkpoint: %w", err)
	}
	reloadedIdentity, err := identityForPlanBundle(reloaded)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if reloadedIdentity != identity {
		return PlanCheckpointResult{}, fmt.Errorf("plan sources changed while preparing checkpoint")
	}
	bundle = reloaded
	tracked, err := adapter.readExactCheckpointWorktree(
		root, bundle, locks, administrative, inventory, inventoryBytes,
	)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	tree, err := adapter.createTree(ctx, tracked)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := injectPlanCheckpointFault(fault, PlanCheckpointFaultAfterTreeCreation); err != nil {
		return PlanCheckpointResult{}, err
	}
	if _, err := adapter.readExactCheckpointWorktree(
		root, bundle, locks, administrative, inventory, inventoryBytes,
	); err != nil {
		return PlanCheckpointResult{}, err
	}

	metadata := planCheckpointMetadata{
		kind: kind, occurredAt: request.occurredAt,
		source: identity.source, semantic: identity.semantic, generation: identity.generation,
		revisionID: request.revisionID, reviewDigest: request.reviewDigest,
	}
	if kind == PlanCheckpointLock {
		metadata.lock = locks.digest
	}

	if !head.id.IsZero() && head.metadata.equal(metadata) && head.tree == tree {
		if err := adapter.synchronizeIndex(ctx, head); err != nil {
			return PlanCheckpointResult{}, err
		}
		if err := adapter.requireCleanIndex(ctx, head); err != nil {
			return PlanCheckpointResult{}, err
		}
		if _, err := adapter.readExactCheckpointWorktree(
			root, bundle, locks, administrative, inventory, inventoryBytes,
		); err != nil {
			return PlanCheckpointResult{}, err
		}
		return planCheckpointResult(bundle, metadata, head.id, tree, true), nil
	}
	if retryCandidate {
		return PlanCheckpointResult{}, fmt.Errorf("existing %s checkpoint does not match the exact retry", kind)
	}
	if err := validatePlanCheckpointTransition(head, kind); err != nil {
		return PlanCheckpointResult{}, err
	}
	if kind == PlanCheckpointRevision {
		if identity.semantic == head.metadata.semantic {
			return PlanCheckpointResult{}, fmt.Errorf("revision checkpoint is a canonical semantic no-op")
		}
		if err := adapter.requireUniqueRevisionID(ctx, head, request.revisionID); err != nil {
			return PlanCheckpointResult{}, err
		}
	}

	commit, err := adapter.createCommit(ctx, tree, head.id, metadata)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := injectPlanCheckpointFault(fault, PlanCheckpointFaultAfterCommitCreation); err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := injectPlanCheckpointFault(fault, PlanCheckpointFaultBeforeRefCAS); err != nil {
		return PlanCheckpointResult{}, err
	}
	if _, err := adapter.readExactCheckpointWorktree(
		root, bundle, locks, administrative, inventory, inventoryBytes,
	); err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := adapter.publishMainCAS(ctx, commit.id, head.id); err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := injectPlanCheckpointFault(fault, PlanCheckpointFaultAfterRefCAS); err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := adapter.synchronizeIndex(ctx, commit); err != nil {
		return PlanCheckpointResult{}, err
	}
	if err := injectPlanCheckpointFault(fault, PlanCheckpointFaultAfterIndexSynchronization); err != nil {
		return PlanCheckpointResult{}, err
	}
	published, err := adapter.inspectHead(ctx)
	if err != nil {
		return PlanCheckpointResult{}, err
	}
	if published.id != commit.id || published.tree != tree || !published.metadata.equal(metadata) {
		return PlanCheckpointResult{}, fmt.Errorf("published plan checkpoint does not match the expected commit")
	}
	if err := adapter.requireCleanIndex(ctx, published); err != nil {
		return PlanCheckpointResult{}, err
	}
	if _, err := adapter.readExactCheckpointWorktree(
		root, bundle, locks, administrative, inventory, inventoryBytes,
	); err != nil {
		return PlanCheckpointResult{}, err
	}
	return planCheckpointResult(bundle, metadata, commit.id, tree, false), nil
}

func (adapter planCheckpointGitAdapter) verifyLockCheckpoint(
	ctx context.Context,
	bundle WorkspaceBundle,
) (VerifiedPlanLockCheckpoint, error) {
	root, err := OpenVerifiedRoot(RootRolePlan, bundle.root, false)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	defer root.Close()
	if root.Identity() != bundle.rootIdentity {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("workspace bundle root changed before lock verification")
	}
	head, err := adapter.inspectPreparedRepository(ctx)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if head.id.IsZero() {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("plan repository has no checkpoint")
	}
	if head.metadata.kind != PlanCheckpointLock {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf(
			"workspace initialization requires a lock checkpoint; HEAD is %s",
			head.metadata.kind,
		)
	}
	identity, err := identityForPlanBundle(bundle)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if head.metadata.generation != identity.generation {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf(
			"lock checkpoint generation %s does not match bundle generation %s",
			head.metadata.generation,
			identity.generation,
		)
	}
	if head.metadata.source != identity.source {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf(
			"lock checkpoint source digest %s does not match bundle source digest %s",
			head.metadata.source,
			identity.source,
		)
	}
	if head.metadata.semantic != identity.semantic {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("lock checkpoint semantic digest does not match the bundle")
	}
	locks, administrative, err := inspectPlanLockMaterialization(bundle)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if head.metadata.lock != locks.digest {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf(
			"lock checkpoint lock digest %s does not match generated locks %s",
			head.metadata.lock,
			locks.digest,
		)
	}
	inventory, inventoryBytes, err := buildPlanRepositoryInventory(bundle, locks, administrative)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	currentInventory, exists, err := currentInventoryBytes(root)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if !exists || !bytes.Equal(currentInventory, inventoryBytes) {
		return VerifiedPlanLockCheckpoint{}, fmt.Errorf("plan repository inventory does not match the lock checkpoint")
	}
	if _, err := parsePlanRepositoryInventory(currentInventory); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if err := adapter.requireExactWorktree(root, inventory); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	tracked, err := adapter.readTrackedPlanFiles(root, bundle, locks, inventoryBytes)
	if err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if err := adapter.verifyTrackedTree(ctx, head.tree, tracked); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if err := adapter.requireCleanIndex(ctx, head); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	if _, err := adapter.readExactCheckpointWorktree(
		root, bundle, locks, administrative, inventory, inventoryBytes,
	); err != nil {
		return VerifiedPlanLockCheckpoint{}, err
	}
	return VerifiedPlanLockCheckpoint{
		root: bundle.root, commit: head.id, tree: head.tree,
		sourceDigest: identity.source, semanticDigest: identity.semantic,
		generation: identity.generation, lockDigest: locks.digest,
	}, nil
}

func (adapter planCheckpointGitAdapter) verifyTrackedTree(
	ctx context.Context,
	tree GitObjectID,
	files map[string][]byte,
) error {
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"ls-tree", "-r", "-z", "--full-tree", rawPlanGitObject(tree),
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("inspect plan checkpoint tree: %w", err)
	}
	observed := make(map[string]GitObjectID, len(files))
	for _, record := range bytes.Split(stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, relativeBytes, found := bytes.Cut(record, []byte{'\t'})
		if !found {
			return fmt.Errorf("plan checkpoint tree contains a malformed entry")
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
			return fmt.Errorf("plan checkpoint tree path %s has unsupported mode or type", relativeBytes)
		}
		relative := string(relativeBytes)
		if _, err := normalizePlanRepositoryPath(relative); err != nil {
			return err
		}
		object, err := ParseGitObjectID(string(tree.Algorithm()) + ":" + fields[2])
		if err != nil {
			return err
		}
		if _, exists := observed[relative]; exists {
			return fmt.Errorf("plan checkpoint tree contains duplicate path %s", relative)
		}
		observed[relative] = object
	}
	if len(observed) != len(files) {
		return fmt.Errorf("plan repository HEAD tree does not contain the exact inventory")
	}
	for relative, content := range files {
		expected, err := gitBlobObjectID(tree.Algorithm(), content)
		if err != nil {
			return err
		}
		if observed[relative] != expected {
			return fmt.Errorf("plan repository HEAD tree does not match tracked path %s", relative)
		}
	}
	return nil
}

func (adapter planCheckpointGitAdapter) prepareRepository(
	ctx context.Context,
	allowInitialize bool,
) (planCheckpointCommit, error) {
	repositoryExists := adapter.isRepository(ctx)
	if !repositoryExists {
		if !allowInitialize {
			return planCheckpointCommit{}, fmt.Errorf("plan repository must be initialized with an initial checkpoint")
		}
		if err := adapter.initializeRepository(ctx); err != nil {
			return planCheckpointCommit{}, err
		}
	}
	if err := adapter.verifyRepositoryLayout(ctx); err != nil {
		return planCheckpointCommit{}, err
	}
	head, err := adapter.inspectHead(ctx)
	if err != nil {
		return planCheckpointCommit{}, err
	}
	repairMetadata := allowInitialize && head.id.IsZero()
	if err := adapter.verifyOrConfigureRepository(ctx, repairMetadata); err != nil {
		return planCheckpointCommit{}, err
	}
	if err := adapter.verifyOrPublishExclude(ctx, repairMetadata); err != nil {
		return planCheckpointCommit{}, err
	}
	if err := adapter.verifyRepositoryRefs(ctx, head); err != nil {
		return planCheckpointCommit{}, err
	}
	if err := adapter.verifyCheckpointHistory(ctx, head); err != nil {
		return planCheckpointCommit{}, err
	}
	return head, nil
}

func (adapter planCheckpointGitAdapter) inspectPreparedRepository(
	ctx context.Context,
) (planCheckpointCommit, error) {
	if !adapter.isRepository(ctx) {
		return planCheckpointCommit{}, fmt.Errorf("bundle root is not a plan repository")
	}
	if err := adapter.verifyRepositoryLayout(ctx); err != nil {
		return planCheckpointCommit{}, err
	}
	head, err := adapter.inspectHead(ctx)
	if err != nil {
		return planCheckpointCommit{}, err
	}
	if err := adapter.verifyOrConfigureRepository(ctx, false); err != nil {
		return planCheckpointCommit{}, err
	}
	if err := adapter.verifyOrPublishExclude(ctx, false); err != nil {
		return planCheckpointCommit{}, err
	}
	if err := adapter.verifyRepositoryRefs(ctx, head); err != nil {
		return planCheckpointCommit{}, err
	}
	if err := adapter.verifyCheckpointHistory(ctx, head); err != nil {
		return planCheckpointCommit{}, err
	}
	return head, nil
}

func (adapter planCheckpointGitAdapter) isRepository(ctx context.Context) bool {
	stdout, _, exitCode, err := adapter.run(ctx, planGitRunOptions{}, "rev-parse", "--show-toplevel")
	if err != nil || exitCode != 0 {
		return false
	}
	top := filepath.Clean(strings.TrimSpace(string(stdout)))
	return top == adapter.root
}

func (adapter planCheckpointGitAdapter) verifyRepositoryLayout(ctx context.Context) error {
	stdout, stderr, exitCode, err := adapter.run(ctx, planGitRunOptions{}, "rev-parse", "--show-toplevel")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("resolve plan repository worktree: %w", err)
	}
	if filepath.Clean(strings.TrimSpace(string(stdout))) != adapter.root {
		return fmt.Errorf("plan repository must be rooted at the bundle root")
	}
	stdout, stderr, exitCode, err = adapter.run(ctx, planGitRunOptions{}, "rev-parse", "--absolute-git-dir")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("resolve plan repository Git directory: %w", err)
	}
	expected := filepath.Join(adapter.root, ".git")
	if filepath.Clean(strings.TrimSpace(string(stdout))) != expected {
		return fmt.Errorf("plan repository must use its own .git directory")
	}
	info, err := os.Lstat(expected)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err == nil {
			err = fmt.Errorf(".git is not a directory")
		}
		return fmt.Errorf("inspect plan repository Git directory: %w", err)
	}
	return nil
}

func (adapter planCheckpointGitAdapter) initializeRepository(ctx context.Context) error {
	_, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"init",
		"--initial-branch=main",
		"--template=",
	)
	if err != nil {
		return fmt.Errorf("initialize plan repository: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("initialize plan repository: Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (adapter planCheckpointGitAdapter) verifyOrConfigureRepository(
	ctx context.Context,
	repair bool,
) error {
	allowed := map[string]struct{}{
		"core.repositoryformatversion": {}, "core.filemode": {}, "core.bare": {},
		"core.logallrefupdates": {}, "core.ignorecase": {}, "core.precomposeunicode": {},
		"core.symlinks": {}, "extensions.objectformat": {},
		"user.name": {}, "user.email": {}, "core.hookspath": {},
		"commit.gpgsign": {}, "tag.gpgsign": {}, "protocol.allow": {},
		"core.fsmonitor": {}, "core.untrackedcache": {}, "core.attributesfile": {},
	}
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"config", "--local", "--no-includes", "--name-only", "--null", "--list",
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("inspect plan repository configuration: %w", err)
	}
	for _, raw := range bytes.Split(stdout, []byte{0}) {
		key := strings.ToLower(strings.TrimSpace(string(raw)))
		if key == "" {
			continue
		}
		if _, exists := allowed[key]; !exists {
			return fmt.Errorf("plan repository contains unsupported local Git configuration %s", key)
		}
	}
	desired := map[string]string{
		"user.name":           planCheckpointAuthorName,
		"user.email":          planCheckpointAuthorEmail,
		"core.hooksPath":      os.DevNull,
		"commit.gpgSign":      "false",
		"tag.gpgSign":         "false",
		"protocol.allow":      "never",
		"core.fsmonitor":      "false",
		"core.untrackedCache": "false",
		"core.attributesFile": os.DevNull,
	}
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values, valueErr := adapter.localConfigValues(ctx, key)
		if valueErr == nil && len(values) == 1 && values[0] == desired[key] {
			continue
		}
		if !repair {
			if valueErr != nil {
				return fmt.Errorf("plan repository configuration %s is unavailable: %w", key, valueErr)
			}
			return fmt.Errorf("plan repository configuration %s does not match the fixed tool configuration", key)
		}
		_, stderr, exitCode, err := adapter.run(
			ctx,
			planGitRunOptions{},
			"config", "--local", "--replace-all", key, desired[key],
		)
		if err != nil || exitCode != 0 {
			if err == nil {
				err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
			}
			return fmt.Errorf("configure plan repository %s: %w", key, err)
		}
	}
	remotes, stderr, exitCode, err := adapter.run(ctx, planGitRunOptions{}, "remote")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("inspect plan repository remotes: %w", err)
	}
	if strings.TrimSpace(string(remotes)) != "" {
		return fmt.Errorf("plan repository must not have a remote")
	}
	return nil
}

func (adapter planCheckpointGitAdapter) localConfigValues(ctx context.Context, key string) ([]string, error) {
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"config", "--local", "--no-includes", "--get-all", key,
	)
	if err != nil {
		return nil, err
	}
	if exitCode == 1 {
		return nil, fmt.Errorf("configuration is missing")
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
	}
	text := strings.TrimSuffix(string(stdout), "\n")
	if text == "" {
		return []string{""}, nil
	}
	return strings.Split(text, "\n"), nil
}

func (adapter planCheckpointGitAdapter) verifyOrPublishExclude(ctx context.Context, repair bool) error {
	stdout, stderr, exitCode, err := adapter.run(ctx, planGitRunOptions{}, "rev-parse", "--absolute-git-dir")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("resolve plan repository Git directory: %w", err)
	}
	gitDirectory := filepath.Clean(strings.TrimSpace(string(stdout)))
	gitRoot, err := OpenVerifiedRoot(RootRoleGitCommon, gitDirectory, false)
	if err != nil {
		return err
	}
	defer gitRoot.Close()
	expected := planRepositoryExcludeContent()
	current, readErr := gitRoot.ReadBounded("info/exclude", 64*1024)
	if readErr == nil && bytes.Equal(current, expected) {
		return nil
	}
	if !repair {
		if readErr != nil {
			return fmt.Errorf("read fixed plan repository excludes: %w", readErr)
		}
		return fmt.Errorf("plan repository excludes do not match the fixed tool configuration")
	}
	if err := gitRoot.EnsureDirectory("info", 0o700); err != nil {
		return err
	}
	if err := gitRoot.PublishReplaceable(
		"info/exclude", expected, 0o600, 64*1024, PublicationOptions{},
	); err != nil {
		return fmt.Errorf("publish plan repository excludes: %w", err)
	}
	return nil
}

func planRepositoryExcludeContent() []byte {
	return []byte(`# Managed by Feature Implement plan checkpoints.
/generated/feature.materialization.v2.json
/generated/feature.materialization.state.v2.json
/generated/feature.materialization.pending.v2.json
/generated/feature.materialization.cleanup.v2.json
/generated/feature.materialization.ownership.v2.proof
/generated/feature.materialization.ownership.v2/
/generated/feature.materialization.staging.v2/
/generated/**/feature.materialization.directory.v2.claim
/generated/**/feature.materialization.txn-*
/runtime-publication-*.intent.json
/runtime-publication-*.new
/runtime-publication-*.old
`)
}

func (adapter planCheckpointGitAdapter) verifyRepositoryRefs(
	ctx context.Context,
	head planCheckpointCommit,
) error {
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"symbolic-ref", "--quiet", "HEAD",
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("plan repository HEAD must be attached to main: %w", err)
	}
	if strings.TrimSpace(string(stdout)) != planCheckpointMainRef {
		return fmt.Errorf("plan repository HEAD must be %s", planCheckpointMainRef)
	}
	stdout, stderr, exitCode, err = adapter.run(
		ctx,
		planGitRunOptions{},
		"for-each-ref", "--format=%(refname)",
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("inspect plan repository refs: %w", err)
	}
	refs := strings.Fields(string(stdout))
	if head.id.IsZero() {
		if len(refs) != 0 {
			return fmt.Errorf("unborn plan repository contains unexpected refs")
		}
		return nil
	}
	if len(refs) != 1 || refs[0] != planCheckpointMainRef {
		return fmt.Errorf("plan repository may contain only %s", planCheckpointMainRef)
	}
	return nil
}

func (adapter planCheckpointGitAdapter) inspectHead(ctx context.Context) (planCheckpointCommit, error) {
	algorithm, err := adapter.git.objectFormat(ctx, adapter.root)
	if err != nil {
		return planCheckpointCommit{}, err
	}
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"rev-parse", "--verify", "--quiet", "HEAD",
	)
	if err != nil {
		return planCheckpointCommit{}, err
	}
	if exitCode == 1 {
		return planCheckpointCommit{}, nil
	}
	if exitCode != 0 {
		return planCheckpointCommit{}, fmt.Errorf(
			"inspect plan repository HEAD: Git exited with status %d: %s",
			exitCode,
			strings.TrimSpace(string(stderr)),
		)
	}
	id, err := parseRawPlanGitObject(algorithm, stdout)
	if err != nil {
		return planCheckpointCommit{}, err
	}
	return adapter.readCommit(ctx, id)
}

func (adapter planCheckpointGitAdapter) verifyCheckpointHistory(
	ctx context.Context,
	head planCheckpointCommit,
) error {
	if head.id.IsZero() {
		return nil
	}
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"rev-list", "--first-parent", rawPlanGitObject(head.id),
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("inspect plan checkpoint history: %w", err)
	}
	rawCommits := strings.Fields(string(stdout))
	if len(rawCommits) == 0 || len(rawCommits) > maxPlanRepositoryFiles {
		return fmt.Errorf("plan checkpoint history has an invalid length")
	}
	history := make([]planCheckpointCommit, 0, len(rawCommits))
	for index := len(rawCommits) - 1; index >= 0; index-- {
		id, err := ParseGitObjectID(string(head.id.Algorithm()) + ":" + rawCommits[index])
		if err != nil {
			return err
		}
		commit := head
		if id != head.id {
			commit, err = adapter.readCommit(ctx, id)
			if err != nil {
				return err
			}
		}
		history = append(history, commit)
	}
	if history[0].metadata.kind != PlanCheckpointInitial {
		return fmt.Errorf("plan checkpoint history must begin with an initial checkpoint")
	}
	revisions := make(map[ID]struct{}, len(history))
	for index, commit := range history {
		if index == 0 {
			continue
		}
		parent := history[index-1]
		if len(commit.parents) != 1 || commit.parents[0] != parent.id {
			return fmt.Errorf("plan checkpoint history is not a single first-parent chain")
		}
		if parent.metadata.kind == PlanCheckpointLock {
			return fmt.Errorf("plan checkpoint history continues after a lock checkpoint")
		}
		switch commit.metadata.kind {
		case PlanCheckpointRevision:
			if commit.metadata.semantic == parent.metadata.semantic {
				return fmt.Errorf("plan checkpoint history contains a semantic no-op revision")
			}
			if _, exists := revisions[commit.metadata.revisionID]; exists {
				return fmt.Errorf(
					"plan checkpoint history repeats revision_id %s",
					commit.metadata.revisionID,
				)
			}
			revisions[commit.metadata.revisionID] = struct{}{}
		case PlanCheckpointLock:
			if commit.metadata.source != parent.metadata.source {
				return fmt.Errorf("lock checkpoint source digest does not match its parent checkpoint")
			}
			if commit.metadata.semantic != parent.metadata.semantic {
				return fmt.Errorf("lock checkpoint semantic digest does not match its parent checkpoint")
			}
			if commit.metadata.generation != parent.metadata.generation {
				return fmt.Errorf("lock checkpoint generation does not match its parent checkpoint")
			}
		default:
			return fmt.Errorf(
				"plan checkpoint history contains unexpected %s checkpoint",
				commit.metadata.kind,
			)
		}
	}
	return nil
}

func (adapter planCheckpointGitAdapter) readCommit(
	ctx context.Context,
	id GitObjectID,
) (planCheckpointCommit, error) {
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"cat-file", "commit", rawPlanGitObject(id),
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return planCheckpointCommit{}, fmt.Errorf("read plan checkpoint commit %s: %w", id, err)
	}
	return parsePlanCheckpointCommit(id, stdout)
}

func parsePlanCheckpointCommit(id GitObjectID, content []byte) (planCheckpointCommit, error) {
	headerBytes, messageBytes, found := bytes.Cut(content, []byte("\n\n"))
	if !found {
		return planCheckpointCommit{}, fmt.Errorf("plan checkpoint commit %s has no message", id)
	}
	result := planCheckpointCommit{id: id}
	author := ""
	committer := ""
	for _, rawLine := range bytes.Split(headerBytes, []byte{'\n'}) {
		line := string(rawLine)
		name, value, ok := strings.Cut(line, " ")
		if !ok || value == "" {
			return planCheckpointCommit{}, fmt.Errorf("plan checkpoint commit %s has malformed headers", id)
		}
		switch name {
		case "tree":
			if !result.tree.IsZero() {
				return planCheckpointCommit{}, fmt.Errorf("plan checkpoint commit %s has duplicate tree", id)
			}
			tree, err := ParseGitObjectID(string(id.Algorithm()) + ":" + value)
			if err != nil {
				return planCheckpointCommit{}, err
			}
			result.tree = tree
		case "parent":
			parent, err := ParseGitObjectID(string(id.Algorithm()) + ":" + value)
			if err != nil {
				return planCheckpointCommit{}, err
			}
			result.parents = append(result.parents, parent)
		case "author":
			author = value
		case "committer":
			committer = value
		default:
			return planCheckpointCommit{}, fmt.Errorf("plan checkpoint commit %s has unsupported header %s", id, name)
		}
	}
	if result.tree.IsZero() || author == "" || committer == "" || author != committer {
		return planCheckpointCommit{}, fmt.Errorf("plan checkpoint commit %s has invalid identity headers", id)
	}
	metadata, err := parsePlanCheckpointMessage(string(messageBytes))
	if err != nil {
		return planCheckpointCommit{}, fmt.Errorf("plan checkpoint commit %s: %w", id, err)
	}
	expectedIdentity := fmt.Sprintf(
		"%s <%s> %d +0000",
		planCheckpointAuthorName,
		planCheckpointAuthorEmail,
		metadata.occurredAt.Unix(),
	)
	if author != expectedIdentity {
		return planCheckpointCommit{}, fmt.Errorf("plan checkpoint commit %s does not use the fixed tool identity", id)
	}
	switch metadata.kind {
	case PlanCheckpointInitial:
		if len(result.parents) != 0 {
			return planCheckpointCommit{}, fmt.Errorf("initial plan checkpoint must not have a parent")
		}
	case PlanCheckpointRevision, PlanCheckpointLock:
		if len(result.parents) != 1 {
			return planCheckpointCommit{}, fmt.Errorf("%s plan checkpoint must have one parent", metadata.kind)
		}
	}
	result.metadata = metadata
	return result, nil
}

func (metadata planCheckpointMetadata) message() string {
	lines := []string{
		"feature plan checkpoint: " + string(metadata.kind),
		"",
		"Plan-Checkpoint: " + string(metadata.kind),
		"Occurred-At: " + metadata.occurredAt.UTC().Format(time.RFC3339Nano),
		"Source-Digest: " + metadata.source.String(),
		"Semantic-Digest: " + metadata.semantic.String(),
		"Generation: " + metadata.generation.String(),
	}
	if metadata.kind == PlanCheckpointRevision {
		lines = append(lines,
			"Revision-ID: "+metadata.revisionID.String(),
			"Review-Digest: "+metadata.reviewDigest.String(),
		)
	}
	if metadata.kind == PlanCheckpointLock {
		lines = append(lines, "Lock-Digest: "+metadata.lock.String())
	}
	return strings.Join(lines, "\n") + "\n"
}

func parsePlanCheckpointMessage(message string) (planCheckpointMetadata, error) {
	lines := strings.Split(strings.TrimSuffix(message, "\n"), "\n")
	if len(lines) < 7 || lines[1] != "" {
		return planCheckpointMetadata{}, fmt.Errorf("plan checkpoint message is not structured")
	}
	kindText := strings.TrimPrefix(lines[0], "feature plan checkpoint: ")
	kind := PlanCheckpointKind(kindText)
	if !kind.valid() || lines[0] != "feature plan checkpoint: "+kindText {
		return planCheckpointMetadata{}, fmt.Errorf("plan checkpoint message has invalid kind")
	}
	expectedCount := 7
	if kind == PlanCheckpointRevision {
		expectedCount = 9
	}
	if kind == PlanCheckpointLock {
		expectedCount = 8
	}
	if len(lines) != expectedCount {
		return planCheckpointMetadata{}, fmt.Errorf("plan checkpoint message has unexpected trailers")
	}
	value := func(index int, prefix string) (string, error) {
		if !strings.HasPrefix(lines[index], prefix) || strings.TrimSpace(strings.TrimPrefix(lines[index], prefix)) == "" {
			return "", fmt.Errorf("plan checkpoint message is missing %s", strings.TrimSuffix(prefix, ": "))
		}
		return strings.TrimPrefix(lines[index], prefix), nil
	}
	checkpointKind, err := value(2, "Plan-Checkpoint: ")
	if err != nil || checkpointKind != string(kind) {
		return planCheckpointMetadata{}, fmt.Errorf("plan checkpoint kind trailer does not match the subject")
	}
	occurredText, err := value(3, "Occurred-At: ")
	if err != nil {
		return planCheckpointMetadata{}, err
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, occurredText)
	if err != nil || occurredAt.IsZero() || occurredText != occurredAt.UTC().Format(time.RFC3339Nano) {
		return planCheckpointMetadata{}, fmt.Errorf("plan checkpoint occurred_at is not canonical UTC")
	}
	parseDigestTrailer := func(index int, prefix string) (Digest, error) {
		text, err := value(index, prefix)
		if err != nil {
			return Digest{}, err
		}
		return ParseDigest(text)
	}
	source, err := parseDigestTrailer(4, "Source-Digest: ")
	if err != nil {
		return planCheckpointMetadata{}, err
	}
	semantic, err := parseDigestTrailer(5, "Semantic-Digest: ")
	if err != nil {
		return planCheckpointMetadata{}, err
	}
	generation, err := parseDigestTrailer(6, "Generation: ")
	if err != nil {
		return planCheckpointMetadata{}, err
	}
	metadata := planCheckpointMetadata{
		kind: kind, occurredAt: occurredAt.UTC(),
		source: source, semantic: semantic, generation: generation,
	}
	if kind == PlanCheckpointRevision {
		revisionText, err := value(7, "Revision-ID: ")
		if err != nil {
			return planCheckpointMetadata{}, err
		}
		revisionID, err := NewID(revisionText)
		if err != nil {
			return planCheckpointMetadata{}, err
		}
		review, err := parseDigestTrailer(8, "Review-Digest: ")
		if err != nil {
			return planCheckpointMetadata{}, err
		}
		metadata.revisionID = revisionID
		metadata.reviewDigest = review
	}
	if kind == PlanCheckpointLock {
		lock, err := parseDigestTrailer(7, "Lock-Digest: ")
		if err != nil {
			return planCheckpointMetadata{}, err
		}
		metadata.lock = lock
	}
	if metadata.message() != message {
		return planCheckpointMetadata{}, fmt.Errorf("plan checkpoint message is not canonical")
	}
	return metadata, nil
}

func (metadata planCheckpointMetadata) equal(other planCheckpointMetadata) bool {
	return metadata.kind == other.kind &&
		metadata.occurredAt.Equal(other.occurredAt) &&
		metadata.source == other.source &&
		metadata.semantic == other.semantic &&
		metadata.generation == other.generation &&
		metadata.revisionID == other.revisionID &&
		metadata.reviewDigest == other.reviewDigest &&
		metadata.lock == other.lock
}

func (adapter planCheckpointGitAdapter) mayBeExactRetry(
	head planCheckpointCommit,
	kind PlanCheckpointKind,
	request planCheckpointRequest,
	identity planBundleIdentity,
) bool {
	if head.id.IsZero() || head.metadata.kind != kind ||
		!head.metadata.occurredAt.Equal(request.occurredAt) ||
		head.metadata.source != identity.source ||
		head.metadata.semantic != identity.semantic ||
		head.metadata.generation != identity.generation {
		return false
	}
	if kind == PlanCheckpointRevision {
		return head.metadata.revisionID == request.revisionID &&
			head.metadata.reviewDigest == request.reviewDigest
	}
	return true
}

func validatePlanCheckpointTransition(head planCheckpointCommit, next PlanCheckpointKind) error {
	if head.id.IsZero() {
		if next != PlanCheckpointInitial {
			return fmt.Errorf("%s checkpoint requires an initialized plan repository", next)
		}
		return nil
	}
	switch next {
	case PlanCheckpointInitial:
		return fmt.Errorf("plan repository is already initialized")
	case PlanCheckpointRevision:
		if head.metadata.kind == PlanCheckpointLock {
			return fmt.Errorf("locked plan repository cannot accept a revision")
		}
	case PlanCheckpointLock:
		if head.metadata.kind == PlanCheckpointLock {
			return fmt.Errorf("plan repository already has a lock checkpoint")
		}
	default:
		return fmt.Errorf("unsupported plan checkpoint kind %q", next)
	}
	return nil
}

func (adapter planCheckpointGitAdapter) requireCleanIndex(
	ctx context.Context,
	head planCheckpointCommit,
) error {
	if head.id.IsZero() {
		stdout, stderr, exitCode, err := adapter.run(ctx, planGitRunOptions{}, "ls-files", "--cached", "-z")
		if err != nil || exitCode != 0 {
			if err == nil {
				err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
			}
			return fmt.Errorf("inspect initial plan index: %w", err)
		}
		if len(stdout) != 0 {
			return fmt.Errorf("plan checkpoint requires a clean index")
		}
		return nil
	}
	_, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"diff-index", "--cached", "--quiet", rawPlanGitObject(head.id), "--",
	)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("plan checkpoint requires a clean index: %s", strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (adapter planCheckpointGitAdapter) synchronizeIndex(
	ctx context.Context,
	commit planCheckpointCommit,
) error {
	current, err := adapter.inspectHead(ctx)
	if err != nil {
		return err
	}
	if current.id != commit.id {
		return fmt.Errorf("plan repository HEAD moved before index recovery")
	}
	_, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"read-tree", rawPlanGitObject(commit.id),
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("synchronize plan repository index: %w", err)
	}
	return nil
}

func (adapter planCheckpointGitAdapter) ensureInventoryPrecondition(
	ctx context.Context,
	root *VerifiedRoot,
	head planCheckpointCommit,
	desired []byte,
) error {
	current, exists, err := currentInventoryBytes(root)
	if err != nil {
		return err
	}
	if head.id.IsZero() {
		if exists && !bytes.Equal(current, desired) {
			return fmt.Errorf("unborn plan repository contains an unexpected inventory")
		}
		return nil
	}
	if !exists {
		return fmt.Errorf("initialized plan repository is missing its inventory")
	}
	parent, err := adapter.readCommitPath(ctx, head.id, PlanRepositoryInventoryFileName)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, parent) && !bytes.Equal(current, desired) {
		return fmt.Errorf("plan repository inventory has uncheckpointed changes")
	}
	return nil
}

func (adapter planCheckpointGitAdapter) readCommitPath(
	ctx context.Context,
	commit GitObjectID,
	relative string,
) ([]byte, error) {
	spec := rawPlanGitObject(commit) + ":" + relative
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"cat-file", "blob", spec,
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return nil, fmt.Errorf("read %s from checkpoint %s: %w", relative, commit, err)
	}
	return stdout, nil
}

func (adapter planCheckpointGitAdapter) requireExactWorktree(
	root *VerifiedRoot,
	inventory planRepositoryInventory,
) error {
	actual, err := walkPlanRepositoryFiles(root)
	if err != nil {
		return err
	}
	expected := make(map[string]struct{}, len(inventory.paths))
	for _, item := range inventory.paths {
		expected[item.Path] = struct{}{}
	}
	for _, relative := range actual {
		if _, exists := expected[relative]; !exists {
			return fmt.Errorf("plan repository contains unowned path %s", relative)
		}
		delete(expected, relative)
	}
	if len(expected) != 0 {
		missing := make([]string, 0, len(expected))
		for relative := range expected {
			missing = append(missing, relative)
		}
		sort.Strings(missing)
		return fmt.Errorf("plan repository is missing owned path %s", missing[0])
	}
	return nil
}

func walkPlanRepositoryFiles(root *VerifiedRoot) ([]string, error) {
	if root == nil || root.adapter == nil {
		return nil, fmt.Errorf("verified plan root is required")
	}
	result := make([]string, 0)
	var walk func(string) error
	walk = func(directory string) error {
		entries, err := root.adapter.readDirectory(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			relative := entry.name
			if directory != "." {
				relative = path.Join(directory, entry.name)
			}
			if directory == "." && entry.name == ".git" {
				if !entry.info.IsDir() {
					return fmt.Errorf("plan repository .git path is not a directory")
				}
				continue
			}
			if entry.info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("plan repository path %s is a symlink", relative)
			}
			if entry.info.IsDir() {
				if err := walk(relative); err != nil {
					return err
				}
				continue
			}
			if !entry.info.Mode().IsRegular() {
				return fmt.Errorf("plan repository path %s is not a regular file", relative)
			}
			if entry.info.Mode().Perm()&0o111 != 0 {
				return fmt.Errorf("plan repository path %s must not be executable", relative)
			}
			result = append(result, relative)
			if len(result) > maxPlanRepositoryFiles {
				return fmt.Errorf("plan repository exceeds %d files", maxPlanRepositoryFiles)
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	sort.Strings(result)
	if err := root.VerifyPath(); err != nil {
		return nil, err
	}
	return result, nil
}

func (adapter planCheckpointGitAdapter) readTrackedPlanFiles(
	root *VerifiedRoot,
	bundle WorkspaceBundle,
	locks planLockFiles,
	inventoryBytes []byte,
) (map[string][]byte, error) {
	result := make(map[string][]byte, len(bundle.sourcePaths)+len(locks.tracked)+1)
	currentInventory, err := root.ReadBounded(
		PlanRepositoryInventoryFileName,
		maxPlanRepositoryInventoryBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("read plan repository inventory: %w", err)
	}
	if !bytes.Equal(currentInventory, inventoryBytes) {
		return nil, fmt.Errorf("plan repository inventory changed while preparing checkpoint")
	}
	result[PlanRepositoryInventoryFileName] = currentInventory
	var total int64 = int64(len(inventoryBytes))
	for _, relative := range bundle.sourcePaths {
		expected, exists := bundle.sourceFiles[relative]
		if !exists {
			return nil, fmt.Errorf("validated bundle is missing source snapshot %s", relative)
		}
		content, err := root.ReadBounded(relative, MaxMaterializationArtifactBytes)
		if err != nil {
			return nil, fmt.Errorf("read plan source %s: %w", relative, err)
		}
		if !bytes.Equal(content, expected) {
			return nil, fmt.Errorf("plan source %s changed since the bundle was loaded", relative)
		}
		result[relative] = content
		total += int64(len(content))
	}
	for relative, expected := range locks.tracked {
		content, err := root.ReadBounded(relative, MaxMaterializationArtifactBytes)
		if err != nil {
			return nil, fmt.Errorf("read plan lock %s: %w", relative, err)
		}
		if !bytes.Equal(content, expected) {
			return nil, fmt.Errorf("plan lock %s changed after generation", relative)
		}
		result[relative] = content
		total += int64(len(content))
	}
	if total > MaxMaterializationTotalBytes {
		return nil, fmt.Errorf("plan checkpoint tracked content exceeds %d bytes", MaxMaterializationTotalBytes)
	}
	return result, nil
}

func (adapter planCheckpointGitAdapter) readExactCheckpointWorktree(
	root *VerifiedRoot,
	bundle WorkspaceBundle,
	locks planLockFiles,
	administrative []string,
	inventory planRepositoryInventory,
	inventoryBytes []byte,
) (map[string][]byte, error) {
	if err := verifyCheckpointBundleRoot(root, bundle); err != nil {
		return nil, err
	}
	if len(locks.tracked) != 0 {
		if err := verifyPlanLockMaterialization(bundle, locks, administrative); err != nil {
			return nil, err
		}
	}
	if err := adapter.requireExactWorktree(root, inventory); err != nil {
		return nil, err
	}
	tracked, err := adapter.readTrackedPlanFiles(root, bundle, locks, inventoryBytes)
	if err != nil {
		return nil, err
	}
	if err := verifyCheckpointBundleRoot(root, bundle); err != nil {
		return nil, err
	}
	return tracked, nil
}

func verifyCheckpointBundleRoot(root *VerifiedRoot, bundle WorkspaceBundle) error {
	if root == nil || root.Identity() != bundle.rootIdentity {
		return fmt.Errorf("workspace bundle root identity changed during plan checkpoint")
	}
	if err := root.VerifyPath(); err != nil {
		return err
	}
	return nil
}

func (adapter planCheckpointGitAdapter) createTree(
	ctx context.Context,
	files map[string][]byte,
) (GitObjectID, error) {
	indexFile, err := os.CreateTemp("", "feature-plan-index-*")
	if err != nil {
		return GitObjectID{}, err
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		_ = os.Remove(indexPath)
		return GitObjectID{}, err
	}
	if err := os.Remove(indexPath); err != nil {
		return GitObjectID{}, err
	}
	defer os.Remove(indexPath)
	options := planGitRunOptions{indexPath: indexPath}
	_, stderr, exitCode, err := adapter.run(ctx, options, "read-tree", "--empty")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return GitObjectID{}, fmt.Errorf("initialize plan checkpoint index: %w", err)
	}
	algorithm, err := adapter.git.objectFormat(ctx, adapter.root)
	if err != nil {
		return GitObjectID{}, err
	}
	paths := make([]string, 0, len(files))
	for relative := range files {
		if _, err := normalizePlanRepositoryPath(relative); err != nil {
			return GitObjectID{}, err
		}
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		hashOptions := options
		hashOptions.input = files[relative]
		stdout, stderr, exitCode, err := adapter.run(ctx, hashOptions, "hash-object", "-w", "--stdin")
		if err != nil || exitCode != 0 {
			if err == nil {
				err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
			}
			return GitObjectID{}, fmt.Errorf("hash plan checkpoint path %s: %w", relative, err)
		}
		blob, err := parseRawPlanGitObject(algorithm, stdout)
		if err != nil {
			return GitObjectID{}, err
		}
		_, stderr, exitCode, err = adapter.run(
			ctx,
			options,
			"update-index", "--add", "--cacheinfo",
			"100644", rawPlanGitObject(blob), relative,
		)
		if err != nil || exitCode != 0 {
			if err == nil {
				err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
			}
			return GitObjectID{}, fmt.Errorf("index plan checkpoint path %s: %w", relative, err)
		}
	}
	stdout, stderr, exitCode, err := adapter.run(ctx, options, "write-tree")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return GitObjectID{}, fmt.Errorf("write plan checkpoint tree: %w", err)
	}
	return parseRawPlanGitObject(algorithm, stdout)
}

func (adapter planCheckpointGitAdapter) createCommit(
	ctx context.Context,
	tree GitObjectID,
	parent GitObjectID,
	metadata planCheckpointMetadata,
) (planCheckpointCommit, error) {
	arguments := []string{
		"-c", "commit.gpgSign=false",
		"commit-tree", rawPlanGitObject(tree),
	}
	if !parent.IsZero() {
		arguments = append(arguments, "-p", rawPlanGitObject(parent))
	}
	arguments = append(arguments, "-F", "-")
	occurred := metadata.occurredAt
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{input: []byte(metadata.message()), identity: &occurred},
		arguments...,
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return planCheckpointCommit{}, fmt.Errorf("create plan checkpoint commit: %w", err)
	}
	id, err := parseRawPlanGitObject(tree.Algorithm(), stdout)
	if err != nil {
		return planCheckpointCommit{}, err
	}
	commit, err := adapter.readCommit(ctx, id)
	if err != nil {
		return planCheckpointCommit{}, err
	}
	if commit.tree != tree || !commit.metadata.equal(metadata) {
		return planCheckpointCommit{}, fmt.Errorf("created plan checkpoint commit does not match its request")
	}
	if parent.IsZero() {
		if len(commit.parents) != 0 {
			return planCheckpointCommit{}, fmt.Errorf("created initial checkpoint unexpectedly has a parent")
		}
	} else if len(commit.parents) != 1 || commit.parents[0] != parent {
		return planCheckpointCommit{}, fmt.Errorf("created plan checkpoint has the wrong parent")
	}
	return commit, nil
}

func (adapter planCheckpointGitAdapter) publishMainCAS(
	ctx context.Context,
	commit GitObjectID,
	expected GitObjectID,
) error {
	expectedRaw := rawPlanGitObject(expected)
	if expected.IsZero() {
		length := 40
		if commit.Algorithm() == GitHashSHA256 {
			length = 64
		}
		expectedRaw = strings.Repeat("0", length)
	}
	_, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"update-ref", "--no-deref", "-m", "feature plan checkpoint",
		planCheckpointMainRef, rawPlanGitObject(commit), expectedRaw,
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("publish plan checkpoint with compare-and-swap: %w", err)
	}
	return nil
}

func (adapter planCheckpointGitAdapter) requireUniqueRevisionID(
	ctx context.Context,
	head planCheckpointCommit,
	revisionID ID,
) error {
	if revisionID.IsZero() {
		return fmt.Errorf("revision checkpoint requires revision_id")
	}
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{},
		"rev-list", "--first-parent", rawPlanGitObject(head.id),
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d: %s", exitCode, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("inspect plan checkpoint history: %w", err)
	}
	commits := strings.Fields(string(stdout))
	if len(commits) > maxPlanRepositoryFiles {
		return fmt.Errorf("plan checkpoint history exceeds %d commits", maxPlanRepositoryFiles)
	}
	for _, raw := range commits {
		id, err := ParseGitObjectID(string(head.id.Algorithm()) + ":" + raw)
		if err != nil {
			return err
		}
		commit, err := adapter.readCommit(ctx, id)
		if err != nil {
			return err
		}
		if commit.metadata.revisionID == revisionID {
			return fmt.Errorf("revision_id %s is already present in plan history", revisionID)
		}
	}
	return nil
}

func (adapter planCheckpointGitAdapter) verifyPreLockCheckpoint(
	ctx context.Context,
	root *VerifiedRoot,
	bundle WorkspaceBundle,
	head planCheckpointCommit,
	identity planBundleIdentity,
) error {
	if head.metadata.kind != PlanCheckpointInitial && head.metadata.kind != PlanCheckpointRevision {
		return fmt.Errorf("lock checkpoint requires an initial or revision checkpoint")
	}
	if head.metadata.source != identity.source {
		return fmt.Errorf("plan sources changed after the latest checkpoint")
	}
	if head.metadata.semantic != identity.semantic {
		return fmt.Errorf("plan semantics changed after the latest checkpoint")
	}
	if head.metadata.generation != identity.generation {
		return fmt.Errorf("plan generation changed after the latest checkpoint")
	}
	emptyLocks := planLockFiles{tracked: map[string][]byte{}}
	parentInventory, parentInventoryBytes, err := buildPlanRepositoryInventory(bundle, emptyLocks, nil)
	if err != nil {
		return err
	}
	headInventory, err := adapter.readCommitPath(ctx, head.id, PlanRepositoryInventoryFileName)
	if err != nil {
		return err
	}
	if !bytes.Equal(headInventory, parentInventoryBytes) {
		return fmt.Errorf("latest plan checkpoint inventory does not match the current sources")
	}
	tracked, err := adapter.readTrackedPlanFiles(root, bundle, emptyLocks, parentInventoryBytes)
	if err != nil {
		return err
	}
	tree, err := adapter.createTree(ctx, tracked)
	if err != nil {
		return err
	}
	if tree != head.tree {
		return fmt.Errorf("plan sources are not at the exact latest checkpoint")
	}
	actual, err := walkPlanRepositoryFiles(root)
	if err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(parentInventory.paths))
	for _, item := range parentInventory.paths {
		allowed[item.Path] = struct{}{}
	}
	for _, relative := range actual {
		if _, exists := allowed[relative]; exists {
			continue
		}
		if relative == PlanRepositoryInventoryFileName ||
			strings.HasPrefix(relative, WorkspaceGeneratedDirectory+"/") {
			continue
		}
		return fmt.Errorf("plan repository contains unowned path %s", relative)
	}
	return nil
}

func parseRawPlanGitObject(algorithm GitHashAlgorithm, value []byte) (GitObjectID, error) {
	return ParseGitObjectID(string(algorithm) + ":" + strings.TrimSpace(string(value)))
}

func rawPlanGitObject(object GitObjectID) string {
	return strings.TrimPrefix(object.String(), string(object.Algorithm())+":")
}

func (adapter planCheckpointGitAdapter) run(
	ctx context.Context,
	options planGitRunOptions,
	arguments ...string,
) ([]byte, []byte, int, error) {
	argv := trustedGitArguments(adapter.root, arguments...)
	command := exec.CommandContext(ctx, adapter.git.executable, argv...)
	additions := make([]EnvironmentVariable, 0, 6)
	if options.identity != nil {
		timestamp := "@" + strconv.FormatInt(options.identity.UTC().Unix(), 10) + " +0000"
		for _, item := range [][2]string{
			{"GIT_AUTHOR_NAME", planCheckpointAuthorName},
			{"GIT_AUTHOR_EMAIL", planCheckpointAuthorEmail},
			{"GIT_AUTHOR_DATE", timestamp},
			{"GIT_COMMITTER_NAME", planCheckpointAuthorName},
			{"GIT_COMMITTER_EMAIL", planCheckpointAuthorEmail},
			{"GIT_COMMITTER_DATE", timestamp},
		} {
			variable, err := NewEnvironmentVariable(item[0], item[1])
			if err != nil {
				return nil, nil, -1, err
			}
			additions = append(additions, variable)
		}
	}
	environment, err := BuildNonProviderProcessEnvironment(os.Environ(), additions)
	if err != nil {
		return nil, nil, -1, err
	}
	if options.indexPath != "" {
		if !filepath.IsAbs(options.indexPath) || strings.IndexByte(options.indexPath, 0) >= 0 {
			return nil, nil, -1, fmt.Errorf("plan checkpoint index path is invalid")
		}
		environment = append(environment, "GIT_INDEX_FILE="+options.indexPath)
	}
	command.Env = environment
	if options.input != nil {
		command.Stdin = bytes.NewReader(options.input)
	}
	var stdout, stderr boundedProcessBuffer
	stdout.maximum = maxAttemptGitOutputBytes
	stderr.maximum = 128 * 1024
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	exitCode := 0
	if runErr != nil {
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			return nil, nil, -1, runErr
		}
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, nil, -1, fmt.Errorf("Git output exceeded its bound")
	}
	return stdout.bytes(), stderr.bytes(), exitCode, nil
}
