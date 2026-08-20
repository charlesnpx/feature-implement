package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
	defer func() { resultErr = errors.Join(resultErr, guard.Close()) }()
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
		if inspection.WorktreeRegistered() || !inspection.Clean() ||
			inspection.WorktreeHead() != base {
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
	canonicalWorktree, err := canonicalizeTrustedRootPath(worktree)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	canonicalParentWorktree, err := canonicalizeTrustedRootPath(
		filepath.Join(parent.Path(), name),
	)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if canonicalWorktree != canonicalParentWorktree {
		return AttemptGitInspection{}, fmt.Errorf("attempt worktree path does not match its verified parent")
	}
	if _, err := parent.adapter.makeDirectory(name, 0o700); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("create attempt worktree directory: %w", err)
	}
	if err := injectAttemptWorktreeMaterializationFault(
		adapter.worktreeMaterializeFault, AttemptMaterializationFaultAfterDirectoryBinding,
	); err != nil {
		return AttemptGitInspection{}, err
	}
	root, err := OpenVerifiedRoot(RootRoleWorktree, canonicalWorktree, false)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("open new attempt worktree: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	if err := materializeExactAttemptTree(ctx, adapter, source, root, base); err != nil {
		return AttemptGitInspection{}, err
	}
	if err := initializeDetachedAttemptRepository(ctx, adapter, source, root, base, algorithm); err != nil {
		return AttemptGitInspection{}, err
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("verify attempt roots after materialization: %w", err)
	}
	inspection, err = adapter.inspectScratchAttemptWorktree(ctx, source, worktree)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if !inspection.Clean() || inspection.WorktreeHead() != base || inspection.WorktreeRegistered() {
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
		return NewAttemptGitInspection(false, GitObjectID{}, false, false, "", GitObjectID{}, false)
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
		return NewAttemptGitInspection(false, GitObjectID{}, true, false, "", GitObjectID{}, false)
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
		if err := commit.verifyRawTreeMaterialization(ctx, worktree, tree); err != nil {
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
	algorithm, err := adapter.objectFormat(ctx, source.root)
	if err != nil {
		return err
	}
	for _, directory := range directories {
		if _, err := root.adapter.makeDirectory(directory, 0o755); err != nil {
			return fmt.Errorf("create attempt tree directory %s: %w", directory, err)
		}
	}
	target := LocalTargetGitAdapter{git: adapter}
	for _, entry := range entries {
		switch entry.mode {
		case GitModeRegular:
			err = adapter.materializeAttemptBlob(ctx, source.root, root.adapter, entry, algorithm, 0o644)
		case GitModeExecutable:
			err = adapter.materializeAttemptBlob(ctx, source.root, root.adapter, entry, algorithm, 0o755)
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
				err = root.adapter.writeSymlinkExclusive(entry.path, string(content))
			}
		default:
			err = fmt.Errorf("unsupported attempt tree mode %s", entry.mode)
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
) (resultErr error) {
	if root == nil {
		return fmt.Errorf("attempt tree destination is closed")
	}
	if _, exitCode, err := adapter.run(ctx, root.Path(), "init", "--quiet", "--object-format="+string(algorithm)); err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return fmt.Errorf("initialize detached attempt repository: %w", err)
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
	if err := commit.verifyRawTreeMaterialization(ctx, root.Path(), tree); err != nil {
		return fmt.Errorf("verify exact detached attempt tree: %w", err)
	}
	return nil
}
