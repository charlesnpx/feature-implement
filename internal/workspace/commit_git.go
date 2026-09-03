package workspace

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func gitBlobObjectID(algorithm GitHashAlgorithm, content []byte) (GitObjectID, error) {
	digest, err := newGitBlobHasher(algorithm, int64(len(content)))
	if err != nil {
		return GitObjectID{}, err
	}
	_, _ = digest.Write(content)
	return gitObjectIDFromHash(algorithm, digest)
}

func newGitBlobHasher(algorithm GitHashAlgorithm, size int64) (hash.Hash, error) {
	if size < 0 {
		return nil, fmt.Errorf("Git blob size cannot be negative")
	}
	var digest hash.Hash
	switch algorithm {
	case GitHashSHA1:
		digest = sha1.New() // Git object-format compatibility, not a security decision.
	case GitHashSHA256:
		digest = sha256.New()
	default:
		return nil, fmt.Errorf("unsupported Git blob algorithm %q", algorithm)
	}
	_, _ = fmt.Fprintf(digest, "blob %d%c", size, byte(0))
	return digest, nil
}

func gitObjectIDFromHash(
	algorithm GitHashAlgorithm,
	digest hash.Hash,
) (GitObjectID, error) {
	if digest == nil {
		return GitObjectID{}, fmt.Errorf("Git object digest is required")
	}
	raw := digest.Sum(nil)
	expected := 0
	switch algorithm {
	case GitHashSHA1:
		expected = sha1.Size
	case GitHashSHA256:
		expected = sha256.Size
	}
	if expected == 0 || len(raw) != expected {
		return GitObjectID{}, fmt.Errorf(
			"Git object digest length %d does not match %s",
			len(raw), algorithm,
		)
	}
	var object GitObjectID
	object.algorithm = algorithm
	object.length = uint8(len(raw))
	copy(object.value[:], raw)
	return object, nil
}

type GitCommitInspection struct {
	commit  GitObjectID
	parents []GitObjectID
	tree    GitObjectID
	subject string
	body    string
	diff    CommitDiff
}

func NewGitCommitInspection(
	commit GitObjectID,
	parents []GitObjectID,
	tree GitObjectID,
	subject, body string,
	diff CommitDiff,
) (GitCommitInspection, error) {
	if commit.IsZero() || len(parents) == 0 || tree.IsZero() || len(diff.changes) == 0 {
		return GitCommitInspection{}, fmt.Errorf("Git commit inspection requires commit, parent, tree, and diff")
	}
	copyParents := append([]GitObjectID(nil), parents...)
	for _, parent := range copyParents {
		if parent.IsZero() || parent.Algorithm() != commit.Algorithm() {
			return GitCommitInspection{}, fmt.Errorf("Git commit inspection parent object format differs")
		}
	}
	if tree.Algorithm() != commit.Algorithm() {
		return GitCommitInspection{}, fmt.Errorf("Git commit inspection tree object format differs")
	}
	if commitDiffObjectAlgorithm(diff) != commit.Algorithm() {
		return GitCommitInspection{}, fmt.Errorf("Git commit inspection diff object format differs")
	}
	if err := validateCommitSubject(subject); err != nil {
		return GitCommitInspection{}, err
	}
	if err := validateCommitBody(body); err != nil {
		return GitCommitInspection{}, err
	}
	return GitCommitInspection{
		commit: commit, parents: copyParents, tree: tree, subject: subject, body: body,
		diff: cloneCommitDiff(diff),
	}, nil
}

func (inspection GitCommitInspection) Commit() GitObjectID { return inspection.commit }
func (inspection GitCommitInspection) Parents() []GitObjectID {
	return append([]GitObjectID(nil), inspection.parents...)
}
func (inspection GitCommitInspection) Tree() GitObjectID { return inspection.tree }
func (inspection GitCommitInspection) Subject() string   { return inspection.subject }
func (inspection GitCommitInspection) Body() string      { return inspection.body }
func (inspection GitCommitInspection) Diff() CommitDiff  { return cloneCommitDiff(inspection.diff) }

type CommitGitPort interface {
	InspectFirstParentRange(context.Context, string, GitObjectID, GitObjectID) ([]GitCommitInspection, error)
	VerifyCleanWorktree(context.Context, string, GitObjectID) error
}

type LocalCommitGitAdapter struct {
	git LocalAttemptGitAdapter
}

func NewLocalCommitGitAdapter(executable string, environment []EnvironmentVariable) (LocalCommitGitAdapter, error) {
	git, err := NewLocalAttemptGitAdapter(executable, environment)
	if err != nil {
		return LocalCommitGitAdapter{}, err
	}
	return LocalCommitGitAdapter{git: git}, nil
}

func DefaultLocalCommitGitAdapter() LocalCommitGitAdapter {
	adapter, _ := NewLocalCommitGitAdapter("git", nil)
	return adapter
}

func (adapter LocalCommitGitAdapter) verifyAttemptWorktreeBranch(
	ctx context.Context,
	worktree string,
) error {
	_, exitCode, err := adapter.git.run(ctx, worktree, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return err
	}
	if exitCode != 1 {
		return fmt.Errorf("attempt worktree must keep HEAD detached")
	}
	return nil
}

func (adapter LocalCommitGitAdapter) InspectCommit(
	ctx context.Context,
	repositoryRoot string,
	commit GitObjectID,
) (GitCommitInspection, error) {
	if !filepath.IsAbs(filepath.Clean(repositoryRoot)) || commit.IsZero() {
		return GitCommitInspection{}, fmt.Errorf("commit inspection requires absolute repository and object")
	}
	algorithm, err := adapter.git.objectFormat(ctx, repositoryRoot)
	if err != nil {
		return GitCommitInspection{}, err
	}
	if algorithm != commit.Algorithm() {
		return GitCommitInspection{}, fmt.Errorf("commit object format does not match repository")
	}
	raw, exitCode, err := adapter.git.run(ctx, repositoryRoot, "cat-file", "commit", objectHex(commit))
	if err != nil || exitCode != 0 {
		return GitCommitInspection{}, gitExitError("read configured commit", exitCode, err)
	}
	tree, parents, subject, body, err := parseRawCommitObject(raw, algorithm)
	if err != nil {
		return GitCommitInspection{}, err
	}
	if len(parents) == 0 {
		return GitCommitInspection{}, fmt.Errorf("configured commit cannot be a root commit")
	}
	diffRaw, exitCode, err := adapter.git.run(
		ctx, repositoryRoot, "diff-tree", "--raw", "-z", "--no-commit-id", "--no-abbrev", "-r",
		"--find-renames=50%", "--ignore-submodules=none", objectHex(parents[0]), objectHex(commit), "--",
	)
	if err != nil || exitCode != 0 {
		return GitCommitInspection{}, gitExitError("inspect configured commit diff", exitCode, err)
	}
	diff, err := parseRawGitDiff(diffRaw, algorithm)
	if err != nil {
		return GitCommitInspection{}, err
	}
	return NewGitCommitInspection(commit, parents, tree, subject, body, diff)
}

// InspectCleanWorktreeHead returns the worktree's actual clean branch head.
// When that head differs from priorHead, every intervening first-parent commit
// must be a single-parent descendant. This is the trusted bridge used to adopt
// ordinary local commits without allowing a reset, rebase, or merge commit to
// replace the durable attempt frontier.
func (adapter LocalCommitGitAdapter) InspectCleanWorktreeHead(
	ctx context.Context,
	worktree string,
	priorHead GitObjectID,
) (GitCommitInspection, error) {
	if priorHead.IsZero() {
		return GitCommitInspection{}, fmt.Errorf("clean worktree head inspection requires a prior head")
	}
	binding, err := adapter.captureTrustedWorktreeBinding(ctx, worktree)
	if err != nil {
		return GitCommitInspection{}, err
	}
	worktree = binding.root
	if err := adapter.verifyAttemptWorktreeBranch(ctx, worktree); err != nil {
		return GitCommitInspection{}, err
	}
	algorithm, err := adapter.git.objectFormat(ctx, worktree)
	if err != nil {
		return GitCommitInspection{}, err
	}
	if algorithm != priorHead.Algorithm() {
		return GitCommitInspection{}, fmt.Errorf("prior head object format does not match repository")
	}
	head, err := adapter.resolveObject(ctx, worktree, algorithm, "HEAD")
	if err != nil {
		return GitCommitInspection{}, err
	}
	if err := adapter.VerifyCleanWorktree(ctx, worktree, head); err != nil {
		return GitCommitInspection{}, err
	}
	if head != priorHead {
		if _, err := adapter.InspectFirstParentRange(ctx, worktree, priorHead, head); err != nil {
			return GitCommitInspection{}, fmt.Errorf("ordinary commit head must descend from durable head: %w", err)
		}
	}
	inspection, err := adapter.InspectCommit(ctx, worktree, head)
	if err != nil {
		return GitCommitInspection{}, err
	}
	if err := adapter.VerifyCleanWorktree(ctx, worktree, head); err != nil {
		return GitCommitInspection{}, fmt.Errorf("confirm clean worktree head: %w", err)
	}
	return inspection, nil
}

// ReadReviewInput returns the exact patch between an attempt's durable base
// and its reserved review head. It is intentionally part of the same trusted
// local Git adapter used by review snapshot inspection: callers must not
// independently shell out for reviewer input.
func (adapter LocalCommitGitAdapter) ReadReviewInput(
	ctx context.Context,
	worktree string,
	base, head GitObjectID,
) ([]byte, error) {
	if ctx == nil || base.IsZero() || head.IsZero() || base.Algorithm() != head.Algorithm() {
		return nil, fmt.Errorf("review input requires context and algorithm-matched base and head")
	}
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if !filepath.IsAbs(worktree) {
		return nil, fmt.Errorf("review input worktree must be absolute")
	}
	if err := adapter.VerifyCleanWorktree(ctx, worktree, head); err != nil {
		return nil, fmt.Errorf("verify exact review input before diff: %w", err)
	}
	algorithm, err := adapter.git.objectFormat(ctx, worktree)
	if err != nil {
		return nil, err
	}
	if algorithm != base.Algorithm() {
		return nil, fmt.Errorf("review input objects do not match the worktree Git object format")
	}
	output, exitCode, err := adapter.git.run(
		ctx,
		worktree,
		"diff", "--no-ext-diff", "--no-textconv",
		objectHex(base)+".."+objectHex(head), "--",
	)
	if err != nil || exitCode != 0 {
		return nil, gitExitError("read review input diff", exitCode, err)
	}
	return append([]byte(nil), output...), nil
}

func (adapter LocalCommitGitAdapter) InspectFirstParentRange(
	ctx context.Context,
	repositoryRoot string,
	base, head GitObjectID,
) ([]GitCommitInspection, error) {
	if base.IsZero() || head.IsZero() || base.Algorithm() != head.Algorithm() {
		return nil, fmt.Errorf("first-parent inspection requires compatible base and head")
	}
	output, exitCode, err := adapter.git.run(
		ctx, repositoryRoot, "rev-list", "--first-parent", "--reverse", objectHex(base)+".."+objectHex(head),
	)
	if err != nil || exitCode != 0 {
		return nil, gitExitError("inspect first-parent commit sequence", exitCode, err)
	}
	lines := strings.Fields(string(output))
	if len(lines) == 0 {
		return nil, fmt.Errorf("first-parent range is empty")
	}
	result := make([]GitCommitInspection, 0, len(lines))
	parent := base
	for index, line := range lines {
		commit, err := qualifyGitObjectID(base.Algorithm(), line)
		if err != nil {
			return nil, err
		}
		inspection, err := adapter.InspectCommit(ctx, repositoryRoot, commit)
		if err != nil {
			return nil, fmt.Errorf("inspect first-parent commit %d: %w", index+1, err)
		}
		if len(inspection.parents) != 1 || inspection.parents[0] != parent {
			return nil, fmt.Errorf("commit %s is not a single-parent child of %s", commit, parent)
		}
		result = append(result, inspection)
		parent = commit
	}
	if parent != head {
		return nil, fmt.Errorf("first-parent sequence ends at %s, expected %s", parent, head)
	}
	return result, nil
}

func (adapter LocalCommitGitAdapter) VerifyCleanWorktree(
	ctx context.Context,
	worktree string,
	expectedHead GitObjectID,
) error {
	binding, err := adapter.captureTrustedWorktreeBinding(ctx, worktree)
	if err != nil {
		return err
	}
	worktree = binding.root
	if err := adapter.verifyAttemptWorktreeBranch(ctx, worktree); err != nil {
		return err
	}
	algorithm, err := adapter.git.objectFormat(ctx, worktree)
	if err != nil {
		return err
	}
	head, err := adapter.resolveObject(ctx, worktree, algorithm, "HEAD")
	if err != nil {
		return err
	}
	if head != expectedHead {
		return fmt.Errorf("worktree head %s does not match %s", head, expectedHead)
	}
	if err := adapter.rejectHiddenIndexEntries(ctx, worktree); err != nil {
		return err
	}
	status, exitCode, err := adapter.git.run(
		ctx, worktree, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none",
		"--ignored=matching",
	)
	if err != nil || exitCode != 0 {
		return gitExitError("verify clean worktree", exitCode, err)
	}
	if len(status) != 0 {
		return fmt.Errorf("worktree is dirty after configured transaction")
	}
	indexTree, err := adapter.writeTree(ctx, worktree, algorithm)
	if err != nil {
		return err
	}
	commitTree, err := adapter.resolveObject(ctx, worktree, algorithm, "HEAD^{tree}")
	if err != nil {
		return err
	}
	if indexTree != commitTree {
		return fmt.Errorf("worktree index tree does not match committed tree")
	}
	if err := adapter.verifyRawTreeMaterialization(ctx, worktree, commitTree); err != nil {
		return fmt.Errorf("verify committed raw worktree: %w", err)
	}
	if err := adapter.confirmTrustedCommitState(ctx, binding, head, commitTree); err != nil {
		return fmt.Errorf("confirm committed Git state: %w", err)
	}
	return nil
}

func (adapter LocalCommitGitAdapter) confirmTrustedCommitState(
	ctx context.Context,
	binding trustedWorktreeBinding,
	head, tree GitObjectID,
) error {
	confirmed, err := adapter.captureTrustedWorktreeBinding(ctx, binding.root)
	if err != nil {
		return err
	}
	if confirmed != binding {
		return fmt.Errorf("Git worktree administration changed during verification")
	}
	if err := adapter.verifyAttemptWorktreeBranch(ctx, binding.root); err != nil {
		return fmt.Errorf("worktree branch changed during verification: %w", err)
	}
	algorithm, err := adapter.git.objectFormat(ctx, binding.root)
	if err != nil {
		return err
	}
	confirmedHead, err := adapter.resolveObject(ctx, binding.root, algorithm, "HEAD")
	if err != nil {
		return err
	}
	if confirmedHead != head {
		return fmt.Errorf("worktree head changed from %s to %s during verification", head, confirmedHead)
	}
	confirmedTree, err := adapter.writeTree(ctx, binding.root, algorithm)
	if err != nil {
		return err
	}
	if confirmedTree != tree {
		return fmt.Errorf("worktree index tree changed from %s to %s during verification", tree, confirmedTree)
	}
	return nil
}

func (adapter LocalCommitGitAdapter) rejectHiddenIndexEntries(ctx context.Context, worktree string) error {
	exitCode, err := adapter.git.streamNULTerminatedRecords(
		ctx, worktree,
		func(record []byte) error {
			return rejectIndexTagRecord(
				record,
				func(tag byte) bool {
					return tag == 'S' || (tag >= 'a' && tag <= 'z')
				},
				"configured execution forbids assume-unchanged and skip-worktree index entries",
			)
		},
		"ls-files", "-v", "-z", "--",
	)
	if err != nil || exitCode != 0 {
		return gitExitError("inspect commit index flags", exitCode, err)
	}
	exitCode, err = adapter.git.streamNULTerminatedRecords(
		ctx, worktree,
		func(record []byte) error {
			return rejectIndexTagRecord(
				record,
				func(tag byte) bool {
					return tag >= 'a' && tag <= 'z'
				},
				"configured execution forbids fsmonitor-valid index entries",
			)
		},
		"ls-files", "-f", "-z", "--",
	)
	if err != nil || exitCode != 0 {
		return gitExitError("inspect commit fsmonitor flags", exitCode, err)
	}
	return nil
}

func rejectHiddenIndexRecords(output []byte) error {
	return rejectIndexTagRecords(output, func(tag byte) bool {
		return tag == 'S' || (tag >= 'a' && tag <= 'z')
	}, "configured execution forbids assume-unchanged and skip-worktree index entries")
}

func rejectFSMonitorIndexRecords(output []byte) error {
	return rejectIndexTagRecords(output, func(tag byte) bool {
		return tag >= 'a' && tag <= 'z'
	}, "configured execution forbids fsmonitor-valid index entries")
}

func rejectIndexTagRecords(output []byte, forbidden func(byte) bool, message string) error {
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if err := rejectIndexTagRecord(record, forbidden, message); err != nil {
			return err
		}
	}
	return nil
}

func rejectIndexTagRecord(
	record []byte,
	forbidden func(byte) bool,
	message string,
) error {
	if len(record) < 3 || record[1] != ' ' {
		return fmt.Errorf("Git index flag record is malformed")
	}
	if forbidden(record[0]) {
		return fmt.Errorf("%s", message)
	}
	return nil
}

const (
	maxGitAdministrativeFileBytes = 64 * 1024
	maxRawSubmoduleDepth          = 16
)

type trustedWorktreeBinding struct {
	root      string
	gitDir    string
	commonDir string
	admin     Digest
	config    Digest
}

type rawGitTreeEntry struct {
	path   string
	mode   GitFileMode
	kind   string
	object GitObjectID
}

type rawTreeMaterializationMismatch struct {
	message string
}

func (mismatch rawTreeMaterializationMismatch) Error() string {
	return mismatch.message
}

func rawTreeMismatchf(format string, arguments ...any) error {
	return rawTreeMaterializationMismatch{
		message: fmt.Sprintf(format, arguments...),
	}
}

func isRawTreeMaterializationMismatch(err error) bool {
	var mismatch rawTreeMaterializationMismatch
	return errors.As(err, &mismatch)
}

func (adapter LocalCommitGitAdapter) verifyRawTreeMaterialization(
	ctx context.Context,
	worktree string,
	tree GitObjectID,
) error {
	return adapter.verifyRawTreeMaterializationAtDepth(
		ctx, worktree, tree, false, 0, make(map[string]struct{}),
	)
}

// verifyDetachedAttemptRawTreeMaterialization treats each gitlink as the
// empty directory checkout leaves behind when submodules are not initialized.
func (adapter LocalCommitGitAdapter) verifyDetachedAttemptRawTreeMaterialization(
	ctx context.Context,
	worktree string,
	tree GitObjectID,
) error {
	return adapter.verifyRawTreeMaterializationAtDepth(
		ctx, worktree, tree, true, 0, make(map[string]struct{}),
	)
}

func (adapter LocalCommitGitAdapter) verifyRawTreeMaterializationAtDepth(
	ctx context.Context,
	worktree string,
	tree GitObjectID,
	emptyGitlinks bool,
	depth int,
	visited map[string]struct{},
) error {
	if depth > maxRawSubmoduleDepth {
		return fmt.Errorf("raw worktree exceeds %d nested submodules", maxRawSubmoduleDepth)
	}
	binding, err := adapter.captureTrustedWorktreeBinding(ctx, worktree)
	if err != nil {
		return err
	}
	algorithm, err := adapter.git.objectFormat(ctx, binding.root)
	if err != nil {
		return err
	}
	if tree.IsZero() || tree.Algorithm() != algorithm {
		return fmt.Errorf("raw worktree tree does not match repository object format")
	}
	visitKey := binding.root + "\x00" + tree.String()
	if _, exists := visited[visitKey]; exists {
		return fmt.Errorf("raw worktree contains a recursive submodule materialization")
	}
	visited[visitKey] = struct{}{}
	defer delete(visited, visitKey)

	entries, err := adapter.inspectRawTreeEntries(ctx, binding.root, tree)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if isRepositoryAttributesPath(entry.path) {
			return fmt.Errorf(
				"raw Git tree path %s contains unsupported repository-defined .gitattributes",
				entry.path,
			)
		}
	}
	if err := adapter.verifyExactStageZeroIndex(
		ctx, binding.root, algorithm, entries,
	); err != nil {
		return err
	}
	expected := make(map[string]rawGitTreeEntry, len(entries))
	expectedDirectories := map[string]struct{}{"": {}}
	for _, entry := range entries {
		if _, duplicate := expected[entry.path]; duplicate {
			return fmt.Errorf("raw Git tree repeats path %q", entry.path)
		}
		expected[entry.path] = entry
		for directory := pathpkg.Dir(entry.path); directory != "."; directory = pathpkg.Dir(directory) {
			expectedDirectories[directory] = struct{}{}
		}
	}

	for _, entry := range entries {
		absolute := filepath.Join(binding.root, filepath.FromSlash(entry.path))
		info, err := os.Lstat(absolute)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return rawTreeMismatchf("raw worktree path %s: %v", entry.path, err)
			}
			return fmt.Errorf("raw worktree path %s: %w", entry.path, err)
		}
		switch entry.mode {
		case GitModeRegular, GitModeExecutable:
			if !info.Mode().IsRegular() {
				return rawTreeMismatchf("raw worktree path %s is not a regular file", entry.path)
			}
			if err := verifyRawWorktreeSingleLink(entry.path, info); err != nil {
				return err
			}
			executable := info.Mode().Perm()&0o111 != 0
			if executable != (entry.mode == GitModeExecutable) {
				return rawTreeMismatchf("raw worktree path %s executable mode differs from tree", entry.path)
			}
			object, err := adapter.hashRawWorktreeFile(ctx, binding.root, entry.path, algorithm, info)
			if err != nil {
				return err
			}
			if object != entry.object {
				return rawTreeMismatchf("raw worktree path %s bytes differ from tree", entry.path)
			}
		case GitModeSymlink:
			if info.Mode()&os.ModeSymlink == 0 {
				return rawTreeMismatchf("raw worktree path %s is not a symbolic link", entry.path)
			}
			target, err := os.Readlink(absolute)
			if err != nil {
				return fmt.Errorf("read raw worktree symlink %s: %w", entry.path, err)
			}
			object, err := gitBlobObjectID(algorithm, []byte(target))
			if err != nil {
				return err
			}
			confirmedTarget, err := os.Readlink(absolute)
			if err != nil || confirmedTarget != target {
				return rawTreeMismatchf("raw worktree symlink %s changed during verification", entry.path)
			}
			if object != entry.object {
				return rawTreeMismatchf("raw worktree symlink %s target differs from tree", entry.path)
			}
		case GitModeSubmodule:
			if !info.IsDir() {
				return rawTreeMismatchf("raw worktree submodule %s is not a directory", entry.path)
			}
			if emptyGitlinks {
				contents, err := os.ReadDir(absolute)
				if err != nil {
					return fmt.Errorf("inspect raw worktree gitlink %s: %w", entry.path, err)
				}
				if len(contents) != 0 {
					return rawTreeMismatchf("raw worktree gitlink %s is not an empty directory", entry.path)
				}
				break
			}
			submoduleRoot := absolute
			submoduleAlgorithm, err := adapter.git.objectFormat(ctx, submoduleRoot)
			if err != nil {
				return fmt.Errorf("inspect raw submodule %s object format: %w", entry.path, err)
			}
			if submoduleAlgorithm != entry.object.Algorithm() {
				return rawTreeMismatchf("raw worktree submodule %s object format differs from gitlink", entry.path)
			}
			head, err := adapter.resolveObject(ctx, submoduleRoot, submoduleAlgorithm, "HEAD")
			if err != nil {
				return fmt.Errorf("inspect raw submodule %s head: %w", entry.path, err)
			}
			if head != entry.object {
				return rawTreeMismatchf("raw worktree submodule %s head differs from gitlink", entry.path)
			}
			submoduleTree, err := adapter.resolveObject(
				ctx, submoduleRoot, submoduleAlgorithm, objectHex(head)+"^{tree}",
			)
			if err != nil {
				return fmt.Errorf("inspect raw submodule %s tree: %w", entry.path, err)
			}
			if err := adapter.verifyRawTreeMaterializationAtDepth(
				ctx, submoduleRoot, submoduleTree, false, depth+1, visited,
			); err != nil {
				return fmt.Errorf("verify raw submodule %s: %w", entry.path, err)
			}
		default:
			return fmt.Errorf("raw worktree path %s has unsupported mode %s", entry.path, entry.mode)
		}
	}

	materialized := make(map[string]struct{}, len(expected))
	err = filepath.WalkDir(binding.root, func(absolute string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if absolute == binding.root {
			return nil
		}
		relative, err := filepath.Rel(binding.root, absolute)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entry, tracked := expected[relative]
		if item.IsDir() {
			if tracked && entry.mode == GitModeSubmodule {
				materialized[relative] = struct{}{}
				return filepath.SkipDir
			}
			if _, expectedDirectory := expectedDirectories[relative]; expectedDirectory {
				return nil
			}
			return rawTreeMismatchf("raw worktree contains untracked directory %s", relative)
		}
		if !tracked || entry.mode == GitModeSubmodule {
			return rawTreeMismatchf("raw worktree contains untracked path %s", relative)
		}
		materialized[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	for expectedPath := range expected {
		if _, exists := materialized[expectedPath]; !exists {
			return rawTreeMismatchf(
				"raw worktree tree path %s was not materialized as an exact filesystem pathname",
				expectedPath,
			)
		}
	}
	confirmed, err := adapter.captureTrustedWorktreeBinding(ctx, binding.root)
	if err != nil {
		return err
	}
	if confirmed != binding {
		return fmt.Errorf("Git worktree administration changed during raw verification")
	}
	return nil
}

func (adapter LocalCommitGitAdapter) inspectRawTreeEntries(
	ctx context.Context,
	repositoryRoot string,
	tree GitObjectID,
) ([]rawGitTreeEntry, error) {
	var entries []rawGitTreeEntry
	exitCode, err := adapter.git.streamNULTerminatedRecords(
		ctx, repositoryRoot,
		func(token []byte) error {
			metadata, rawPath, found := bytes.Cut(token, []byte{'\t'})
			fields := strings.Fields(string(metadata))
			if !found || len(fields) != 3 {
				return fmt.Errorf("raw Git tree entry is malformed")
			}
			normalized, err := normalizeCommitPath(string(rawPath))
			if err != nil {
				return err
			}
			mode := GitFileMode(fields[0])
			if !mode.valid() || mode == GitModeAbsent {
				return fmt.Errorf(
					"raw Git tree path %s has unsupported mode %s",
					normalized, mode,
				)
			}
			if (mode == GitModeSubmodule) != (fields[1] == "commit") ||
				(mode != GitModeSubmodule && fields[1] != "blob") {
				return fmt.Errorf(
					"raw Git tree path %s has inconsistent type %s",
					normalized, fields[1],
				)
			}
			object, err := qualifyGitObjectID(tree.Algorithm(), fields[2])
			if err != nil {
				return err
			}
			entries = append(entries, rawGitTreeEntry{
				path: normalized, mode: mode, kind: fields[1], object: object,
			})
			return nil
		},
		"ls-tree", "-r", "-z", "--full-tree", objectHex(tree),
	)
	if err != nil || exitCode != 0 {
		return nil, gitExitError("inspect raw Git tree", exitCode, err)
	}
	return entries, nil
}

func (adapter LocalCommitGitAdapter) verifyExactStageZeroIndex(
	ctx context.Context,
	repositoryRoot string,
	algorithm GitHashAlgorithm,
	expectedEntries []rawGitTreeEntry,
) error {
	expected := make(map[string]rawGitTreeEntry, len(expectedEntries))
	for _, entry := range expectedEntries {
		if _, duplicate := expected[entry.path]; duplicate {
			return fmt.Errorf("raw Git tree repeats path %q", entry.path)
		}
		expected[entry.path] = entry
	}
	observed := make(map[string]struct{}, len(expectedEntries))
	exitCode, err := adapter.git.streamNULTerminatedRecords(
		ctx, repositoryRoot,
		func(record []byte) error {
			metadata, rawPath, found := bytes.Cut(record, []byte{'\t'})
			fields := strings.Fields(string(metadata))
			if !found || len(fields) != 3 {
				return fmt.Errorf("Git stage-zero index entry is malformed")
			}
			mode := GitFileMode(fields[0])
			if !mode.valid() || mode == GitModeAbsent {
				return fmt.Errorf(
					"Git stage-zero index entry has unsupported mode %s",
					mode,
				)
			}
			if fields[2] != "0" {
				return rawTreeMismatchf(
					"Git index contains non-stage-zero entry for %s",
					string(rawPath),
				)
			}
			normalized, err := normalizeCommitPath(string(rawPath))
			if err != nil {
				return err
			}
			if strings.Trim(fields[1], "0") == "" {
				return rawTreeMismatchf(
					"Git index contains intent-to-add entry %s",
					normalized,
				)
			}
			object, err := qualifyGitObjectID(algorithm, fields[1])
			if err != nil {
				return err
			}
			entry, exists := expected[normalized]
			if !exists {
				return rawTreeMismatchf(
					"Git index contains path %s absent from the recorded tree",
					normalized,
				)
			}
			if _, duplicate := observed[normalized]; duplicate {
				return rawTreeMismatchf(
					"Git index repeats stage-zero path %s", normalized,
				)
			}
			if mode != entry.mode || object != entry.object {
				return rawTreeMismatchf(
					"Git index path %s mode or object differs from the recorded tree",
					normalized,
				)
			}
			observed[normalized] = struct{}{}
			return nil
		},
		"ls-files", "--stage", "-z", "--",
	)
	if err != nil || exitCode != 0 {
		return gitExitError("inspect exact stage-zero Git index", exitCode, err)
	}
	if len(observed) != len(expected) {
		for _, entry := range expectedEntries {
			if _, exists := observed[entry.path]; !exists {
				return rawTreeMismatchf(
					"Git index is missing recorded tree path %s",
					entry.path,
				)
			}
		}
		return rawTreeMismatchf(
			"Git index inventory differs from the recorded tree",
		)
	}
	return nil
}

func (adapter LocalCommitGitAdapter) hashRawWorktreeFile(
	ctx context.Context,
	repositoryRoot, relative string,
	algorithm GitHashAlgorithm,
	before os.FileInfo,
) (GitObjectID, error) {
	output, exitCode, err := adapter.git.run(
		ctx, repositoryRoot, "hash-object", "--no-filters", "--", relative,
	)
	if err != nil || exitCode != 0 {
		return GitObjectID{}, gitExitError("hash raw worktree file "+relative, exitCode, err)
	}
	after, err := os.Lstat(filepath.Join(repositoryRoot, filepath.FromSlash(relative)))
	if err != nil {
		return GitObjectID{}, fmt.Errorf("reinspect raw worktree file %s: %w", relative, err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return GitObjectID{}, fmt.Errorf("raw worktree file %s changed during verification", relative)
	}
	if err := verifyRawWorktreeSingleLink(relative, after); err != nil {
		return GitObjectID{}, err
	}
	return qualifyGitObjectID(algorithm, strings.TrimSpace(string(output)))
}

func verifyRawWorktreeSingleLink(relative string, info os.FileInfo) error {
	links, err := platformFileLinkCount(info)
	if err != nil {
		return fmt.Errorf("inspect raw worktree file %s hard links: %w", relative, err)
	}
	if links != 1 {
		return rawTreeMismatchf(
			"raw worktree file %s has %d hard links; exactly one is required",
			relative, links,
		)
	}
	return nil
}

func (adapter LocalCommitGitAdapter) captureTrustedWorktreeBinding(
	ctx context.Context,
	worktree string,
) (trustedWorktreeBinding, error) {
	root, err := canonicalExistingDirectory(worktree)
	if err != nil {
		return trustedWorktreeBinding{}, err
	}
	resolvedRoot, err := adapter.readGitPath(ctx, root, "--show-toplevel")
	if err != nil {
		return trustedWorktreeBinding{}, err
	}
	resolvedRoot, err = canonicalExistingDirectory(resolvedRoot)
	if err != nil {
		return trustedWorktreeBinding{}, err
	}
	if resolvedRoot != root {
		return trustedWorktreeBinding{}, fmt.Errorf("Git top-level %s does not match worktree %s", resolvedRoot, root)
	}
	gitDir, err := adapter.readGitPath(ctx, root, "--absolute-git-dir")
	if err != nil {
		return trustedWorktreeBinding{}, err
	}
	gitDir, err = canonicalExistingDirectory(gitDir)
	if err != nil {
		return trustedWorktreeBinding{}, err
	}
	commonDir, err := adapter.readGitPath(ctx, root, "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return trustedWorktreeBinding{}, err
	}
	commonDir, err = canonicalExistingDirectory(commonDir)
	if err != nil {
		return trustedWorktreeBinding{}, err
	}
	if err := rejectGitCommonAttributes(commonDir); err != nil {
		return trustedWorktreeBinding{}, err
	}
	adminPath := filepath.Join(root, ".git")
	adminInfo, err := os.Lstat(adminPath)
	if err != nil {
		return trustedWorktreeBinding{}, fmt.Errorf("inspect Git worktree administration: %w", err)
	}
	adminBytes := []byte(adminInfo.Mode().String() + "\n")
	switch {
	case adminInfo.IsDir():
		canonicalAdmin, err := canonicalExistingDirectory(adminPath)
		if err != nil {
			return trustedWorktreeBinding{}, err
		}
		if canonicalAdmin != gitDir {
			return trustedWorktreeBinding{}, fmt.Errorf("Git directory does not match worktree administration")
		}
	case adminInfo.Mode().IsRegular():
		if adminInfo.Size() > maxGitAdministrativeFileBytes {
			return trustedWorktreeBinding{}, fmt.Errorf("Git worktree administration file exceeds its bound")
		}
		content, err := os.ReadFile(adminPath)
		if err != nil {
			return trustedWorktreeBinding{}, fmt.Errorf("read Git worktree administration: %w", err)
		}
		adminBytes = append(adminBytes, content...)
	default:
		return trustedWorktreeBinding{}, fmt.Errorf("Git worktree administration has an unsupported file type")
	}
	if err := adapter.rejectExternalGitDrivers(ctx, root); err != nil {
		return trustedWorktreeBinding{}, err
	}
	config, exitCode, err := adapter.git.run(
		ctx, root, "config", "--null", "--list", "--show-origin", "--show-scope",
	)
	if err != nil || exitCode != 0 {
		return trustedWorktreeBinding{}, gitExitError("inspect trusted Git configuration", exitCode, err)
	}
	if err := rejectGitCommonAttributes(commonDir); err != nil {
		return trustedWorktreeBinding{}, err
	}
	return trustedWorktreeBinding{
		root: root, gitDir: gitDir, commonDir: commonDir,
		admin: DigestBytes(adminBytes), config: DigestBytes(config),
	}, nil
}

func rejectGitCommonAttributes(commonDir string) error {
	commonRoot, err := OpenVerifiedRoot(
		RootRoleGitCommon, commonDir, false,
	)
	if err != nil {
		return fmt.Errorf("open trusted Git common directory: %w", err)
	}
	defer commonRoot.Close()
	_, exists, err := commonRoot.adapter.inspectExact("info/attributes")
	if err != nil {
		return fmt.Errorf("inspect trusted Git common attributes: %w", err)
	}
	if exists {
		return fmt.Errorf(
			"external Git attributes metadata %s is not supported",
			filepath.Join(commonRoot.Path(), "info", "attributes"),
		)
	}
	if err := commonRoot.VerifyPath(); err != nil {
		return fmt.Errorf("verify trusted Git common directory: %w", err)
	}
	return nil
}

func (adapter LocalCommitGitAdapter) rejectExternalGitDrivers(ctx context.Context, repositoryRoot string) error {
	output, exitCode, err := adapter.git.run(ctx, repositoryRoot, "config", "--null", "--name-only", "--list")
	if err != nil || exitCode != 0 {
		return gitExitError("inspect trusted Git driver configuration", exitCode, err)
	}
	if len(output) == 0 {
		return nil
	}
	items := bytes.Split(output, []byte{0})
	if len(items[len(items)-1]) != 0 {
		return fmt.Errorf("trusted Git configuration names are not NUL terminated")
	}
	for _, item := range items[:len(items)-1] {
		name := strings.ToLower(string(item))
		filterDriver := strings.HasPrefix(name, "filter.") &&
			(strings.HasSuffix(name, ".clean") || strings.HasSuffix(name, ".smudge") || strings.HasSuffix(name, ".process"))
		diffDriver := name == "diff.external" ||
			(strings.HasPrefix(name, "diff.") && strings.HasSuffix(name, ".command"))
		if filterDriver || diffDriver {
			return fmt.Errorf("configured execution forbids external Git filter and diff drivers (%s)", name)
		}
	}
	return nil
}

func (adapter LocalCommitGitAdapter) readGitPath(
	ctx context.Context,
	repositoryRoot string,
	arguments ...string,
) (string, error) {
	output, exitCode, err := adapter.git.run(ctx, repositoryRoot, append([]string{"rev-parse"}, arguments...)...)
	if err != nil || exitCode != 0 {
		return "", gitExitError("resolve trusted Git path", exitCode, err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("trusted Git path is malformed")
	}
	return value, nil
}

func canonicalExistingDirectory(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("Git worktree binding requires absolute paths")
	}
	canonical, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree binding path: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("path is not a directory")
		}
		return "", fmt.Errorf("inspect Git worktree binding path: %w", err)
	}
	return filepath.Clean(canonical), nil
}

func (adapter LocalCommitGitAdapter) resolveObject(
	ctx context.Context,
	repositoryRoot string,
	algorithm GitHashAlgorithm,
	revision string,
) (GitObjectID, error) {
	output, exitCode, err := adapter.git.run(ctx, repositoryRoot, "rev-parse", "--verify", revision)
	if err != nil || exitCode != 0 {
		return GitObjectID{}, gitExitError("resolve Git object", exitCode, err)
	}
	return qualifyGitObjectID(algorithm, strings.TrimSpace(string(output)))
}

func (adapter LocalCommitGitAdapter) writeTree(
	ctx context.Context,
	repositoryRoot string,
	algorithm GitHashAlgorithm,
) (GitObjectID, error) {
	output, exitCode, err := adapter.git.run(ctx, repositoryRoot, "write-tree")
	if err != nil || exitCode != 0 {
		return GitObjectID{}, gitExitError("write staged Git tree", exitCode, err)
	}
	return qualifyGitObjectID(algorithm, strings.TrimSpace(string(output)))
}

func parseRawGitDiff(content []byte, algorithm GitHashAlgorithm) (CommitDiff, error) {
	if len(content) == 0 {
		return CommitDiff{}, fmt.Errorf("configured commit requires a non-empty raw Git diff")
	}
	tokens := bytes.Split(content, []byte{0})
	if len(tokens) == 0 || len(tokens[len(tokens)-1]) != 0 {
		return CommitDiff{}, fmt.Errorf("raw Git diff is not NUL terminated")
	}
	tokens = tokens[:len(tokens)-1]
	changes := make([]CommitPathChange, 0)
	for index := 0; index < len(tokens); {
		header := string(tokens[index])
		index++
		fields := strings.Fields(header)
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || len(fields[4]) == 0 {
			return CommitDiff{}, fmt.Errorf("raw Git diff header is malformed")
		}
		oldMode := GitFileMode(strings.TrimPrefix(fields[0], ":"))
		newMode := GitFileMode(fields[1])
		oldObject, err := parseRawDiffObject(algorithm, fields[2])
		if err != nil {
			return CommitDiff{}, err
		}
		newObject, err := parseRawDiffObject(algorithm, fields[3])
		if err != nil {
			return CommitDiff{}, err
		}
		status := fields[4][0]
		if index >= len(tokens) {
			return CommitDiff{}, fmt.Errorf("raw Git diff path is missing")
		}
		firstPath := string(tokens[index])
		index++
		kind := CommitChangeKind("")
		oldPath, newPath := firstPath, firstPath
		switch status {
		case 'A':
			kind, oldPath = CommitChangeAdded, ""
		case 'D':
			kind, newPath = CommitChangeDeleted, ""
		case 'M':
			kind = CommitChangeModified
		case 'T':
			kind = CommitChangeTypeChanged
		case 'R', 'C':
			if index >= len(tokens) {
				return CommitDiff{}, fmt.Errorf("raw Git rename or copy target is missing")
			}
			newPath = string(tokens[index])
			index++
			if status == 'R' {
				kind = CommitChangeRenamed
			} else {
				kind = CommitChangeCopied
			}
		default:
			return CommitDiff{}, fmt.Errorf("unsupported raw Git diff status %q", fields[4])
		}
		change, err := NewCommitPathChange(kind, oldPath, newPath, oldMode, newMode, oldObject, newObject)
		if err != nil {
			return CommitDiff{}, fmt.Errorf("parse raw Git change: %w", err)
		}
		changes = append(changes, change)
	}
	return NewCommitDiff(changes)
}

func parseRawDiffObject(algorithm GitHashAlgorithm, value string) (GitObjectID, error) {
	want := 40
	if algorithm == GitHashSHA256 {
		want = 64
	}
	if len(value) != want {
		return GitObjectID{}, fmt.Errorf("raw Git diff object has the wrong length")
	}
	if strings.Trim(value, "0") == "" {
		return GitObjectID{}, nil
	}
	return qualifyGitObjectID(algorithm, value)
}

func parseRawCommitObject(content []byte, algorithm GitHashAlgorithm) (GitObjectID, []GitObjectID, string, string, error) {
	separator := bytes.Index(content, []byte("\n\n"))
	if separator < 0 {
		return GitObjectID{}, nil, "", "", fmt.Errorf("Git commit object has no message separator")
	}
	headers, message := string(content[:separator]), content[separator+2:]
	var tree GitObjectID
	var parents []GitObjectID
	for _, line := range strings.Split(headers, "\n") {
		if strings.HasPrefix(line, " ") {
			continue
		}
		name, value, found := strings.Cut(line, " ")
		if !found {
			return GitObjectID{}, nil, "", "", fmt.Errorf("Git commit header is malformed")
		}
		switch name {
		case "tree":
			if !tree.IsZero() {
				return GitObjectID{}, nil, "", "", fmt.Errorf("Git commit repeats its tree header")
			}
			parsed, err := qualifyGitObjectID(algorithm, value)
			if err != nil {
				return GitObjectID{}, nil, "", "", err
			}
			tree = parsed
		case "parent":
			parent, err := qualifyGitObjectID(algorithm, value)
			if err != nil {
				return GitObjectID{}, nil, "", "", err
			}
			parents = append(parents, parent)
		}
	}
	if tree.IsZero() {
		return GitObjectID{}, nil, "", "", fmt.Errorf("Git commit has no tree")
	}
	if len(message) == 0 || message[len(message)-1] != '\n' || !utf8.Valid(message) || bytes.IndexByte(message, 0) >= 0 {
		return GitObjectID{}, nil, "", "", fmt.Errorf("Git commit message is not canonical UTF-8")
	}
	message = message[:len(message)-1]
	if bytes.HasSuffix(message, []byte("\n")) {
		return GitObjectID{}, nil, "", "", fmt.Errorf("Git commit message has trailing blank framing")
	}
	messageText := string(message)
	subject, body := messageText, ""
	if firstNewline := strings.IndexByte(messageText, '\n'); firstNewline >= 0 {
		subject = messageText[:firstNewline]
		if !strings.HasPrefix(messageText[firstNewline:], "\n\n") {
			return GitObjectID{}, nil, "", "", fmt.Errorf("Git commit body is not separated by a blank line")
		}
		body = messageText[firstNewline+2:]
	}
	if err := validateCommitSubject(subject); err != nil {
		return GitObjectID{}, nil, "", "", err
	}
	if err := validateCommitBody(body); err != nil {
		return GitObjectID{}, nil, "", "", err
	}
	return tree, parents, subject, body, nil
}

func objectHex(object GitObjectID) string {
	return strings.TrimPrefix(object.String(), string(object.Algorithm())+":")
}

func gitExitError(operation string, exitCode int, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: Git exited with status %d", operation, exitCode)
}
