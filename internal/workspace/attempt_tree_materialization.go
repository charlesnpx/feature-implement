package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type attemptWorktreeRollbackEntry struct {
	relative string
	info     os.FileInfo
}

type initializedAttemptRepositoryRollback struct {
	gitDirectory PlatformFileIdentity
}

type attemptWorktreeRollback struct {
	root                  *VerifiedRoot
	entries               []attemptWorktreeRollbackEntry
	initializedRepository *initializedAttemptRepositoryRollback
}

func (rollback *attemptWorktreeRollback) record(relative string) error {
	if rollback == nil || rollback.root == nil || rollback.root.adapter == nil {
		return fmt.Errorf("attempt worktree rollback root is unavailable")
	}
	info, exists, err := rollback.root.adapter.inspectEntryIncludingSymlinkExact(relative)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("created attempt tree path %s disappeared before rollback binding", relative)
	}
	rollback.entries = append(rollback.entries, attemptWorktreeRollbackEntry{
		relative: relative,
		info:     info,
	})
	return nil
}

func (rollback *attemptWorktreeRollback) recordInitializedRepository() (bool, error) {
	if rollback == nil || rollback.root == nil || rollback.root.adapter == nil {
		return false, fmt.Errorf("attempt worktree rollback root is unavailable")
	}
	if rollback.initializedRepository != nil {
		return false, fmt.Errorf("created detached attempt repository is already bound for rollback")
	}
	info, exists, err := rollback.root.adapter.inspectEntryIncludingSymlinkExact(".git")
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("created detached attempt Git directory changed before rollback binding")
	}
	identity, err := platformFileIdentity(info)
	if err != nil {
		return false, fmt.Errorf("identify created detached attempt Git directory: %w", err)
	}
	rollback.initializedRepository = &initializedAttemptRepositoryRollback{
		gitDirectory: identity,
	}
	return true, nil
}

func (rollback *attemptWorktreeRollback) removeCreated() error {
	if rollback == nil || rollback.root == nil || rollback.root.adapter == nil {
		return nil
	}
	var cleanupErrors []error
	if rollback.initializedRepository != nil {
		// Git init created this one subtree in the verified new worktree. All
		// materialized tree entries remain non-recursive, identity-checked removals.
		if err := rollback.root.adapter.removeDirectoryTreeIdentityExact(
			".git", rollback.initializedRepository.gitDirectory,
		); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf(
				"remove created detached attempt Git directory: %w", err,
			))
		}
	}
	for index := len(rollback.entries) - 1; index >= 0; index-- {
		entry := rollback.entries[index]
		if _, err := rollback.root.adapter.removeEntryIdentityExact(entry.relative, entry.info); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf(
				"remove created attempt tree path %s: %w", entry.relative, err,
			))
		}
	}
	return errors.Join(cleanupErrors...)
}

func rollbackCreatedAttemptWorktree(
	parent *VerifiedRoot,
	name string,
	identity PlatformFileIdentity,
) error {
	removed, err := parent.adapter.removeEmptyDirectoryIdentityExact(name, identity, nil)
	if err != nil {
		return err
	}
	if removed {
		return nil
	}
	_, exists, err := parent.adapter.inspectExact(name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return fmt.Errorf("new attempt worktree directory contains unowned entries and will be preserved")
}

// MaterializeAttemptTree materialises base into an independently administered,
// detached attempt directory. The directory is never passed to git worktree
// add: its Git administration is local to the attempt and its object lookup
// reads the target repository through an alternate object directory.
//
// A pre-existing target is accepted only when it is already the exact clean
// detached base. A partially created or otherwise foreign target is reported,
// never cleared or claimed by this operation.
func (adapter LocalAttemptGitAdapter) MaterializeAttemptTree(
	ctx context.Context,
	repositoryRoot string,
	base GitObjectID,
	worktree string,
) (result AttemptGitInspection, resultErr error) {
	if ctx == nil || base.IsZero() {
		return AttemptGitInspection{}, fmt.Errorf("attempt tree materialization requires context and base")
	}
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if !filepath.IsAbs(worktree) {
		return AttemptGitInspection{}, fmt.Errorf("attempt tree materialization requires an absolute worktree")
	}
	guard, err := adapter.openAttemptWorktreeRootGuard(ctx, repositoryRoot, worktree, true)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	var (
		root             *VerifiedRoot
		rollbackParent   *VerifiedRoot
		rollbackName     string
		rollbackIdentity PlatformFileIdentity
		rollbackCreated  bool
		rollback         *attemptWorktreeRollback
	)
	defer func() {
		rollbackNeeded := resultErr != nil
		if rollbackNeeded && rollback != nil {
			if err := rollback.removeCreated(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf(
					"rollback newly created attempt worktree entries: %w", err,
				))
			}
		}
		if root != nil {
			resultErr = errors.Join(resultErr, root.Close())
		}
		if rollbackNeeded && rollbackCreated {
			if err := rollbackCreatedAttemptWorktree(
				rollbackParent, rollbackName, rollbackIdentity,
			); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf(
					"rollback newly created attempt worktree directory: %w", err,
				))
			}
		}
		resultErr = errors.Join(resultErr, guard.Close())
	}()
	source := guard.binding
	algorithm, err := adapter.objectFormat(ctx, source.root)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if base.Algorithm() != algorithm {
		return AttemptGitInspection{}, fmt.Errorf("attempt base does not match the repository object format")
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("verify attempt roots before materialization: %w", err)
	}

	inspection, err := adapter.inspectScratchAttemptWorktree(ctx, source, worktree)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if inspection.WorktreeExists() {
		if !inspection.Clean() || inspection.WorktreeHead() != base {
			return AttemptGitInspection{}, fmt.Errorf(
				"attempt worktree %s already exists but is not the exact clean detached base",
				worktree,
			)
		}
		return inspection, nil
	}

	parent := guard.worktree
	if parent == nil {
		return AttemptGitInspection{}, fmt.Errorf("attempt worktree parent is unavailable")
	}
	name := filepath.Base(worktree)
	created, makeDirectoryErr := parent.adapter.makeDirectory(name, 0o700)
	if created {
		createdInfo, exists, inspectErr := parent.adapter.inspectExact(name)
		if inspectErr != nil {
			return AttemptGitInspection{}, fmt.Errorf("identify new attempt worktree directory: %w", inspectErr)
		}
		if !exists || createdInfo.Mode()&os.ModeSymlink != 0 || !createdInfo.IsDir() {
			return AttemptGitInspection{}, fmt.Errorf("new attempt worktree directory changed before materialization")
		}
		identity, identityErr := platformFileIdentity(createdInfo)
		if identityErr != nil {
			return AttemptGitInspection{}, fmt.Errorf("identify new attempt worktree directory: %w", identityErr)
		}
		rollbackIdentity = identity
		rollbackParent, rollbackName, rollbackCreated = parent, name, true
	}
	if makeDirectoryErr != nil {
		return AttemptGitInspection{}, fmt.Errorf("create attempt worktree directory: %w", makeDirectoryErr)
	}
	if !created {
		return AttemptGitInspection{}, fmt.Errorf(
			"attempt worktree %s already exists but is not the exact clean detached base",
			worktree,
		)
	}
	if err := injectAttemptWorktreeMaterializationFault(
		adapter.worktreeMaterializeFault, AttemptMaterializationFaultAfterDirectoryBinding,
	); err != nil {
		return AttemptGitInspection{}, err
	}
	root, err = OpenVerifiedRoot(RootRoleWorktree, filepath.Join(parent.Path(), name), false)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("open new attempt worktree: %w", err)
	}
	rollback = &attemptWorktreeRollback{root: root}
	if err := materializeExactAttemptTree(ctx, adapter, source, root, base, algorithm, rollback); err != nil {
		return AttemptGitInspection{}, err
	}
	if err := initializeDetachedAttemptRepository(ctx, adapter, source, root, base, algorithm, rollback); err != nil {
		return AttemptGitInspection{}, err
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("verify attempt roots after materialization: %w", err)
	}
	inspection, err = adapter.inspectScratchAttemptWorktree(ctx, source, worktree)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if !inspection.Clean() || inspection.WorktreeHead() != base {
		return AttemptGitInspection{}, fmt.Errorf("new attempt worktree is not the exact clean detached base")
	}
	return inspection, nil
}

func (adapter LocalAttemptGitAdapter) inspectScratchAttemptWorktree(
	ctx context.Context,
	source trustedWorktreeBinding,
	worktree string,
) (AttemptGitInspection, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	canonicalWorktree, err := canonicalizeTrustedRootPath(worktree)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("canonicalize attempt worktree path: %w", err)
	}
	info, err := os.Lstat(worktree)
	if errors.Is(err, os.ErrNotExist) {
		return newAttemptGitInspection(AttemptGitInspection{})
	}
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return AttemptGitInspection{}, fmt.Errorf("attempt worktree %s is not an exact directory", worktree)
	}
	registered, err := adapter.inspectRegisteredWorktrees(ctx, source.root)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if _, exists := registered[canonicalWorktreePath(worktree)]; exists {
		return AttemptGitInspection{}, fmt.Errorf("attempt worktree %s is registered with the target repository", worktree)
	}
	if _, err := os.Lstat(filepath.Join(worktree, ".git")); errors.Is(err, os.ErrNotExist) {
		return newAttemptGitInspection(AttemptGitInspection{worktreeExists: true})
	} else if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt Git directory: %w", err)
	}

	commit := LocalCommitGitAdapter{git: adapter}
	binding, err := commit.captureTrustedWorktreeBinding(ctx, worktree)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt Git binding: %w", err)
	}
	canonicalBindingRoot, err := canonicalizeTrustedRootPath(binding.root)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("canonicalize detached attempt Git top-level: %w", err)
	}
	if canonicalBindingRoot != canonicalWorktree {
		return AttemptGitInspection{}, fmt.Errorf("detached attempt Git top-level does not match its exact worktree path")
	}
	if binding.commonDir == source.commonDir {
		return AttemptGitInspection{}, fmt.Errorf("attempt worktree unexpectedly shares target Git administration")
	}
	algorithm, err := adapter.objectFormat(ctx, worktree)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	head, err := commit.resolveObject(ctx, worktree, algorithm, "HEAD")
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt head: %w", err)
	}
	if _, exitCode, err := adapter.run(ctx, worktree, "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil || exitCode != 1 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt HEAD: %w", err)
	}
	tree, err := commit.resolveObject(ctx, worktree, algorithm, objectHex(head)+"^{tree}")
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt tree: %w", err)
	}
	indexTree, err := commit.writeTree(ctx, worktree, algorithm)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt index tree: %w", err)
	}
	if err := commit.rejectHiddenIndexEntries(ctx, worktree); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt index flags: %w", err)
	}
	clean := indexTree == tree
	if clean {
		if err := commit.verifyDetachedAttemptRawTreeMaterialization(ctx, worktree, tree); err != nil {
			if isRawTreeMaterializationMismatch(err) {
				clean = false
			} else {
				return AttemptGitInspection{}, fmt.Errorf("inspect detached attempt raw tree: %w", err)
			}
		}
	}
	exactBinding, err := NewAttemptWorktreeGitBinding(AttemptWorktreeGitBindingOptions{
		Worktree:             binding.root,
		GitDirectory:         binding.gitDir,
		CommonDirectory:      binding.commonDir,
		AdministrationDigest: binding.admin,
		ConfigurationDigest:  binding.config,
	})
	if err != nil {
		return AttemptGitInspection{}, err
	}
	return NewScratchAttemptGitInspection(head, tree, exactBinding, clean)
}

func materializeExactAttemptTree(
	ctx context.Context,
	adapter LocalAttemptGitAdapter,
	source trustedWorktreeBinding,
	root *VerifiedRoot,
	base GitObjectID,
	algorithm GitHashAlgorithm,
	rollback *attemptWorktreeRollback,
) error {
	if root == nil || root.adapter == nil {
		return fmt.Errorf("attempt tree destination is closed")
	}
	commit := LocalCommitGitAdapter{git: adapter}
	tree, err := commit.resolveObject(
		ctx, source.root, base.Algorithm(), objectHex(base)+"^{tree}",
	)
	if err != nil {
		return fmt.Errorf("resolve exact attempt base tree: %w", err)
	}
	entries, err := commit.inspectRawTreeEntries(ctx, source.root, tree)
	if err != nil {
		return fmt.Errorf("inspect exact attempt base tree: %w", err)
	}
	directories, err := validateAttemptTreeEntries(entries)
	if err != nil {
		return err
	}
	items, err := root.adapter.readDirectory(".")
	if err != nil {
		return fmt.Errorf("inspect new attempt directory: %w", err)
	}
	if len(items) != 0 {
		return fmt.Errorf("new attempt directory is unexpectedly nonempty")
	}
	for _, directory := range directories {
		created, makeDirectoryErr := root.adapter.makeDirectory(directory, 0o755)
		if created {
			if err := rollback.record(directory); err != nil {
				return fmt.Errorf("record created attempt tree directory %s: %w", directory, err)
			}
		}
		if makeDirectoryErr != nil {
			return fmt.Errorf("create attempt tree directory %s: %w", directory, makeDirectoryErr)
		}
		if !created {
			return fmt.Errorf("attempt tree directory %s appeared during materialization", directory)
		}
	}
	target := LocalTargetGitAdapter{git: adapter}
	for _, entry := range entries {
		created := false
		switch entry.mode {
		case GitModeRegular:
			err = adapter.materializeAttemptBlob(ctx, source.root, root.adapter, entry, algorithm, 0o644)
			created = err == nil
		case GitModeExecutable:
			err = adapter.materializeAttemptBlob(ctx, source.root, root.adapter, entry, algorithm, 0o755)
			created = err == nil
		case GitModeSymlink:
			var content []byte
			content, err = target.readBlob(ctx, source.root, entry.object)
			if err == nil {
				object, hashErr := gitBlobObjectID(algorithm, content)
				if hashErr != nil {
					err = hashErr
				} else if object != entry.object {
					err = fmt.Errorf("attempt tree path did not resolve to its recorded blob")
				}
			}
			if err == nil {
				err = validateRepositorySymlink(entry.path, content)
			}
			if err == nil {
				created, err = root.adapter.writeSymlinkExclusive(entry.path, string(content))
			}
		case GitModeSubmodule:
			created, err = root.adapter.makeDirectory(entry.path, 0o755)
			if err == nil && !created {
				err = fmt.Errorf("attempt tree directory appeared during materialization")
			}
		default:
			err = fmt.Errorf("unsupported attempt tree mode %s", entry.mode)
		}
		if created {
			if err := rollback.record(entry.path); err != nil {
				return fmt.Errorf("record created attempt tree path %s: %w", entry.path, err)
			}
		}
		if err != nil {
			return fmt.Errorf("materialize attempt tree path %s: %w", entry.path, err)
		}
		if err := injectAttemptWorktreeMaterializationFault(
			adapter.worktreeMaterializeFault, AttemptMaterializationFaultAfterPath,
		); err != nil {
			return err
		}
	}
	if err := root.Sync(); err != nil {
		return fmt.Errorf("synchronize exact attempt tree: %w", err)
	}
	return root.VerifyPath()
}

func initializeDetachedAttemptRepository(
	ctx context.Context,
	adapter LocalAttemptGitAdapter,
	source trustedWorktreeBinding,
	root *VerifiedRoot,
	base GitObjectID,
	algorithm GitHashAlgorithm,
	rollback *attemptWorktreeRollback,
) (resultErr error) {
	if root == nil {
		return fmt.Errorf("attempt tree destination is closed")
	}
	if rollback == nil || rollback.root != root || rollback.root.adapter == nil {
		return fmt.Errorf("attempt tree rollback is unavailable")
	}
	if _, exists, err := root.adapter.inspectEntryIncludingSymlinkExact(".git"); err != nil {
		return fmt.Errorf("inspect detached attempt Git directory before initialization: %w", err)
	} else if exists {
		return fmt.Errorf("detached attempt Git directory appeared before initialization")
	}
	_, exitCode, initErr := adapter.run(
		ctx, root.Path(), "init", "--quiet", "--object-format="+string(algorithm),
	)
	if initErr == nil && exitCode != 0 {
		initErr = fmt.Errorf("Git exited with status %d", exitCode)
	}
	initialized, recordErr := rollback.recordInitializedRepository()
	if initErr != nil {
		if recordErr != nil {
			return errors.Join(
				fmt.Errorf("initialize detached attempt repository: %w", initErr),
				fmt.Errorf("record created detached attempt repository: %w", recordErr),
			)
		}
		return fmt.Errorf("initialize detached attempt repository: %w", initErr)
	}
	if recordErr != nil {
		return fmt.Errorf("record created detached attempt repository: %w", recordErr)
	}
	if !initialized {
		return fmt.Errorf("initialize detached attempt repository: Git did not create its Git directory")
	}
	if err := injectAttemptWorktreeMaterializationFault(
		adapter.worktreeMaterializeFault, AttemptMaterializationFaultAfterGitInit,
	); err != nil {
		return err
	}
	commit := LocalCommitGitAdapter{git: adapter}
	binding, err := commit.captureTrustedWorktreeBinding(ctx, root.Path())
	if err != nil {
		return fmt.Errorf("capture detached attempt Git binding: %w", err)
	}
	gitRoot, err := OpenVerifiedRoot(RootRoleGitDirectory, binding.gitDir, false)
	if err != nil {
		return fmt.Errorf("open detached attempt Git directory: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, gitRoot.Close()) }()
	if err := gitRoot.EnsureDirectory("objects/info", 0o700); err != nil {
		return fmt.Errorf("prepare detached attempt alternate objects: %w", err)
	}
	objects := filepath.Join(source.commonDir, "objects")
	if _, err := os.Stat(objects); err != nil {
		return fmt.Errorf("inspect source object directory: %w", err)
	}
	if err := gitRoot.WriteExclusive("objects/info/alternates", []byte(objects+"\n"), 0o600); err != nil {
		return fmt.Errorf("publish detached attempt alternate objects: %w", err)
	}
	if _, exitCode, err := adapter.run(ctx, root.Path(), "cat-file", "-e", objectHex(base)+"^{commit}"); err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return fmt.Errorf("verify detached attempt base through alternate objects: %w", err)
	}
	// The filesystem was populated by materializeExactAttemptTree before this
	// repository was initialized. Populate only the index and detach HEAD; a
	// checkout here would re-materialize the tree through Git and would make the
	// raw-tree path above incidental rather than authoritative.
	if _, exitCode, err := adapter.run(ctx, root.Path(), "read-tree", objectHex(base)); err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return fmt.Errorf("populate detached attempt index: %w", err)
	}
	if _, exitCode, err := adapter.run(ctx, root.Path(), "update-ref", "--no-deref", "HEAD", objectHex(base)); err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return fmt.Errorf("detach attempt at base: %w", err)
	}
	tree, err := commit.resolveObject(ctx, root.Path(), base.Algorithm(), objectHex(base)+"^{tree}")
	if err != nil {
		return err
	}
	if err := commit.verifyDetachedAttemptRawTreeMaterialization(ctx, root.Path(), tree); err != nil {
		return fmt.Errorf("verify exact detached attempt tree: %w", err)
	}
	return nil
}
