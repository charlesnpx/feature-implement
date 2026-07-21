package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type GitCommitInspection struct {
	commit  GitObjectID
	parents []GitObjectID
	tree    GitObjectID
	subject string
	body    string
	diff    CommitDiff
	digest  Digest
}

func NewGitCommitInspection(
	commit GitObjectID,
	parents []GitObjectID,
	tree GitObjectID,
	subject, body string,
	diff CommitDiff,
) (GitCommitInspection, error) {
	if commit.IsZero() || len(parents) == 0 || tree.IsZero() || diff.digest.IsZero() {
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
	type canonical struct {
		Commit  string   `json:"commit"`
		Parents []string `json:"parents"`
		Tree    string   `json:"tree"`
		Subject string   `json:"subject"`
		Body    string   `json:"body"`
		Diff    string   `json:"diff"`
	}
	parentStrings := make([]string, 0, len(copyParents))
	for _, parent := range copyParents {
		parentStrings = append(parentStrings, parent.String())
	}
	content, _ := json.Marshal(canonical{
		Commit: commit.String(), Parents: parentStrings, Tree: tree.String(), Subject: subject, Body: body,
		Diff: diff.digest.String(),
	})
	return GitCommitInspection{
		commit: commit, parents: copyParents, tree: tree, subject: subject, body: body,
		diff: cloneCommitDiff(diff), digest: DigestBytes(content),
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
func (inspection GitCommitInspection) Digest() Digest    { return inspection.digest }

func (inspection GitCommitInspection) Evidence(
	generation Digest,
	step CommitStep,
	ordinal uint16,
) (CommitObjectEvidence, error) {
	if len(inspection.parents) != 1 {
		return CommitObjectEvidence{}, fmt.Errorf("configured commit %s has %d parents; merge commits are forbidden", inspection.commit, len(inspection.parents))
	}
	evidence, err := NewCommitObjectEvidence(
		generation, step.id, ordinal, inspection.commit, inspection.parents[0], inspection.tree,
		inspection.subject, inspection.body, inspection.diff, step.paths.digest,
	)
	if err != nil {
		return CommitObjectEvidence{}, err
	}
	if err := evidence.ValidateStep(step, generation, ordinal, inspection.parents[0]); err != nil {
		return CommitObjectEvidence{}, err
	}
	return evidence, nil
}

type CreateGitCommitRequest struct {
	branch     string
	worktree   string
	parent     GitObjectID
	step       CommitStep
	ordinal    uint16
	body       string
	inspection StagedCommitInspection
}

func NewCreateGitCommitRequest(
	branch, worktree string,
	parent GitObjectID,
	step CommitStep,
	ordinal uint16,
	body string,
	inspection StagedCommitInspection,
) (CreateGitCommitRequest, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return CreateGitCommitRequest{}, err
	}
	if !filepath.IsAbs(worktree) || parent.IsZero() || step.id.IsZero() || ordinal == 0 {
		return CreateGitCommitRequest{}, fmt.Errorf("commit creation request requires branch, absolute worktree, parent, step, and ordinal")
	}
	if err := inspection.Validate(step, parent); err != nil {
		return CreateGitCommitRequest{}, err
	}
	resolvedBody, err := step.message.ResolveBody(body)
	if err != nil {
		return CreateGitCommitRequest{}, err
	}
	return CreateGitCommitRequest{
		branch: branch, worktree: worktree, parent: parent,
		step: cloneCommitSteps([]CommitStep{step})[0], ordinal: ordinal,
		body: resolvedBody, inspection: cloneStagedCommitInspection(inspection),
	}, nil
}

func (request CreateGitCommitRequest) Branch() string      { return request.branch }
func (request CreateGitCommitRequest) Worktree() string    { return request.worktree }
func (request CreateGitCommitRequest) Parent() GitObjectID { return request.parent }
func (request CreateGitCommitRequest) Step() CommitStep {
	return cloneCommitSteps([]CommitStep{request.step})[0]
}
func (request CreateGitCommitRequest) Ordinal() uint16 { return request.ordinal }
func (request CreateGitCommitRequest) Body() string    { return request.body }
func (request CreateGitCommitRequest) Inspection() StagedCommitInspection {
	return cloneStagedCommitInspection(request.inspection)
}

type CommitGitPort interface {
	InspectStaged(context.Context, string, string) (StagedCommitInspection, error)
	CreateConfiguredCommit(context.Context, CreateGitCommitRequest) (GitCommitInspection, error)
	InspectCommit(context.Context, string, GitObjectID) (GitCommitInspection, error)
	InspectFirstParentRange(context.Context, string, GitObjectID, GitObjectID) ([]GitCommitInspection, error)
	VerifyCleanWorktree(context.Context, string, string, GitObjectID) error
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

func (adapter LocalCommitGitAdapter) InspectStaged(
	ctx context.Context,
	worktree, branch string,
) (StagedCommitInspection, error) {
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return StagedCommitInspection{}, err
	}
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if !filepath.IsAbs(worktree) {
		return StagedCommitInspection{}, fmt.Errorf("commit worktree must be absolute")
	}
	first, err := adapter.inspectStagedOnce(ctx, worktree, branch)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	second, err := adapter.inspectStagedOnce(ctx, worktree, branch)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	if first.stateDigest != second.stateDigest {
		return StagedCommitInspection{}, fmt.Errorf("staged Git state changed during inspection")
	}
	return second, nil
}

func (adapter LocalCommitGitAdapter) inspectStagedOnce(
	ctx context.Context,
	worktree, branch string,
) (StagedCommitInspection, error) {
	binding, err := adapter.captureTrustedWorktreeBinding(ctx, worktree)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	worktree = binding.root
	algorithm, err := adapter.git.objectFormat(ctx, worktree)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	actualBranch, err := adapter.symbolicBranch(ctx, worktree)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	if actualBranch != branch {
		return StagedCommitInspection{}, fmt.Errorf("commit worktree branch %q does not match %q", actualBranch, branch)
	}
	head, err := adapter.resolveObject(ctx, worktree, algorithm, "HEAD")
	if err != nil {
		return StagedCommitInspection{}, err
	}
	if err := adapter.rejectHiddenIndexEntries(ctx, worktree); err != nil {
		return StagedCommitInspection{}, err
	}
	tree, err := adapter.writeTree(ctx, worktree, algorithm)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	raw, exitCode, err := adapter.git.run(
		ctx, worktree, "diff", "--cached", "--raw", "-z", "--no-abbrev", "--find-renames=50%",
		"--ignore-submodules=none", "--no-ext-diff", "--no-textconv", "HEAD", "--",
	)
	if err != nil || exitCode != 0 {
		return StagedCommitInspection{}, gitExitError("inspect staged diff", exitCode, err)
	}
	diff, err := parseRawGitDiff(raw, algorithm)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	status, exitCode, err := adapter.git.run(
		ctx, worktree, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none",
		"--ignored=matching",
	)
	if err != nil || exitCode != 0 {
		return StagedCommitInspection{}, gitExitError("inspect commit worktree status", exitCode, err)
	}
	unstaged, untracked, conflicted, err := parsePorcelainV2Status(status)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	inspection, err := NewStagedCommitInspection(head, tree, diff, unstaged, untracked, conflicted)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	if inspection.Eligible() {
		if err := adapter.verifyRawTreeMaterialization(ctx, worktree, tree); err != nil {
			return StagedCommitInspection{}, fmt.Errorf("verify staged raw worktree: %w", err)
		}
	}
	if err := adapter.confirmTrustedCommitState(ctx, binding, branch, head, tree); err != nil {
		return StagedCommitInspection{}, fmt.Errorf("confirm staged Git state: %w", err)
	}
	return inspection, nil
}

func (adapter LocalCommitGitAdapter) CreateConfiguredCommit(
	ctx context.Context,
	request CreateGitCommitRequest,
) (GitCommitInspection, error) {
	if err := validateAttemptBranchSyntax(request.branch); err != nil {
		return GitCommitInspection{}, err
	}
	if !filepath.IsAbs(request.worktree) || request.parent.IsZero() || request.step.id.IsZero() ||
		request.ordinal == 0 || request.inspection.stateDigest.IsZero() {
		return GitCommitInspection{}, fmt.Errorf("configured commit request is incomplete")
	}
	actualBranch, err := adapter.symbolicBranch(ctx, request.worktree)
	if err != nil {
		return GitCommitInspection{}, err
	}
	if actualBranch != request.branch {
		return GitCommitInspection{}, fmt.Errorf("configured commit branch changed from %q to %q", request.branch, actualBranch)
	}
	algorithm, err := adapter.git.objectFormat(ctx, request.worktree)
	if err != nil {
		return GitCommitInspection{}, err
	}
	head, err := adapter.resolveObject(ctx, request.worktree, algorithm, "HEAD")
	if err != nil {
		return GitCommitInspection{}, err
	}
	if head != request.parent {
		existing, inspectErr := adapter.InspectCommit(ctx, request.worktree, head)
		if inspectErr != nil {
			return GitCommitInspection{}, fmt.Errorf("inspect possible replayed commit: %w", inspectErr)
		}
		if err := existingMatchesCreateRequest(existing, request); err != nil {
			return GitCommitInspection{}, fmt.Errorf("configured commit head already advanced: %w", err)
		}
		if err := adapter.VerifyCleanWorktree(ctx, request.worktree, request.branch, existing.commit); err != nil {
			return GitCommitInspection{}, err
		}
		return existing, nil
	}
	confirmed, err := adapter.InspectStaged(ctx, request.worktree, request.branch)
	if err != nil {
		return GitCommitInspection{}, err
	}
	if confirmed.stateDigest != request.inspection.stateDigest {
		return GitCommitInspection{}, fmt.Errorf("staged Git state changed after commit intent was formed")
	}
	message := canonicalCommitMessage(request.step.message.subject, request.body)
	treeText := objectHex(request.inspection.indexTree)
	parentText := objectHex(request.parent)
	output, exitCode, err := adapter.runWithInput(
		ctx, request.worktree, message,
		"-c", "commit.gpgSign=false", "commit-tree", treeText, "-p", parentText,
	)
	if err != nil || exitCode != 0 {
		return GitCommitInspection{}, gitExitError("create configured commit object", exitCode, err)
	}
	commit, err := qualifyGitObjectID(algorithm, strings.TrimSpace(string(output)))
	if err != nil {
		return GitCommitInspection{}, fmt.Errorf("parse configured commit object: %w", err)
	}
	_, exitCode, err = adapter.git.run(
		ctx, request.worktree, "update-ref", "--no-deref", "-m", "feature commit protocol",
		"refs/heads/"+request.branch, objectHex(commit), objectHex(request.parent),
	)
	if err != nil || exitCode != 0 {
		return GitCommitInspection{}, gitExitError("atomically publish configured commit", exitCode, err)
	}
	inspection, err := adapter.InspectCommit(ctx, request.worktree, commit)
	if err != nil {
		return GitCommitInspection{}, err
	}
	if err := existingMatchesCreateRequest(inspection, request); err != nil {
		return GitCommitInspection{}, err
	}
	if err := adapter.VerifyCleanWorktree(ctx, request.worktree, request.branch, commit); err != nil {
		return GitCommitInspection{}, fmt.Errorf("configured commit left dirty state: %w", err)
	}
	return inspection, nil
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
	worktree, branch string,
	expectedHead GitObjectID,
) error {
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return err
	}
	binding, err := adapter.captureTrustedWorktreeBinding(ctx, worktree)
	if err != nil {
		return err
	}
	worktree = binding.root
	actualBranch, err := adapter.symbolicBranch(ctx, worktree)
	if err != nil {
		return err
	}
	if actualBranch != branch {
		return fmt.Errorf("worktree branch %q does not match %q", actualBranch, branch)
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
	if err := adapter.confirmTrustedCommitState(ctx, binding, branch, head, commitTree); err != nil {
		return fmt.Errorf("confirm committed Git state: %w", err)
	}
	return nil
}

func (adapter LocalCommitGitAdapter) confirmTrustedCommitState(
	ctx context.Context,
	binding trustedWorktreeBinding,
	branch string,
	head, tree GitObjectID,
) error {
	confirmed, err := adapter.captureTrustedWorktreeBinding(ctx, binding.root)
	if err != nil {
		return err
	}
	if confirmed != binding {
		return fmt.Errorf("Git worktree administration changed during verification")
	}
	actualBranch, err := adapter.symbolicBranch(ctx, binding.root)
	if err != nil {
		return err
	}
	if actualBranch != branch {
		return fmt.Errorf("worktree branch changed from %q to %q during verification", branch, actualBranch)
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
	output, exitCode, err := adapter.git.run(ctx, worktree, "ls-files", "-v", "-z", "--")
	if err != nil || exitCode != 0 {
		return gitExitError("inspect commit index flags", exitCode, err)
	}
	if err := rejectHiddenIndexRecords(output); err != nil {
		return err
	}
	output, exitCode, err = adapter.git.run(ctx, worktree, "ls-files", "-f", "-z", "--")
	if err != nil || exitCode != 0 {
		return gitExitError("inspect commit fsmonitor flags", exitCode, err)
	}
	return rejectFSMonitorIndexRecords(output)
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
		if len(record) < 3 || record[1] != ' ' {
			return fmt.Errorf("Git index flag record is malformed")
		}
		tag := record[0]
		if forbidden(tag) {
			return fmt.Errorf("%s", message)
		}
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

func (adapter LocalCommitGitAdapter) verifyRawTreeMaterialization(
	ctx context.Context,
	worktree string,
	tree GitObjectID,
) error {
	return adapter.verifyRawTreeMaterializationAtDepth(ctx, worktree, tree, 0, make(map[string]struct{}))
}

func (adapter LocalCommitGitAdapter) verifyRawTreeMaterializationAtDepth(
	ctx context.Context,
	worktree string,
	tree GitObjectID,
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
			return fmt.Errorf("raw worktree path %s: %w", entry.path, err)
		}
		switch entry.mode {
		case GitModeRegular, GitModeExecutable:
			if !info.Mode().IsRegular() {
				return fmt.Errorf("raw worktree path %s is not a regular file", entry.path)
			}
			executable := info.Mode().Perm()&0o111 != 0
			if executable != (entry.mode == GitModeExecutable) {
				return fmt.Errorf("raw worktree path %s executable mode differs from tree", entry.path)
			}
			object, err := adapter.hashRawWorktreeFile(ctx, binding.root, entry.path, algorithm, info)
			if err != nil {
				return err
			}
			if object != entry.object {
				return fmt.Errorf("raw worktree path %s bytes differ from tree", entry.path)
			}
		case GitModeSymlink:
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("raw worktree path %s is not a symbolic link", entry.path)
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
				return fmt.Errorf("raw worktree symlink %s changed during verification", entry.path)
			}
			if object != entry.object {
				return fmt.Errorf("raw worktree symlink %s target differs from tree", entry.path)
			}
		case GitModeSubmodule:
			if !info.IsDir() {
				return fmt.Errorf("raw worktree submodule %s is not a directory", entry.path)
			}
			submoduleRoot := absolute
			submoduleAlgorithm, err := adapter.git.objectFormat(ctx, submoduleRoot)
			if err != nil {
				return fmt.Errorf("inspect raw submodule %s object format: %w", entry.path, err)
			}
			if submoduleAlgorithm != entry.object.Algorithm() {
				return fmt.Errorf("raw worktree submodule %s object format differs from gitlink", entry.path)
			}
			head, err := adapter.resolveObject(ctx, submoduleRoot, submoduleAlgorithm, "HEAD")
			if err != nil {
				return fmt.Errorf("inspect raw submodule %s head: %w", entry.path, err)
			}
			if head != entry.object {
				return fmt.Errorf("raw worktree submodule %s head differs from gitlink", entry.path)
			}
			submoduleTree, err := adapter.resolveObject(
				ctx, submoduleRoot, submoduleAlgorithm, objectHex(head)+"^{tree}",
			)
			if err != nil {
				return fmt.Errorf("inspect raw submodule %s tree: %w", entry.path, err)
			}
			if err := adapter.verifyRawTreeMaterializationAtDepth(
				ctx, submoduleRoot, submoduleTree, depth+1, visited,
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
			return fmt.Errorf("raw worktree contains untracked directory %s", relative)
		}
		if !tracked || entry.mode == GitModeSubmodule {
			return fmt.Errorf("raw worktree contains untracked path %s", relative)
		}
		materialized[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	for expectedPath := range expected {
		if _, exists := materialized[expectedPath]; !exists {
			return fmt.Errorf("raw worktree tree path %s was not materialized as an exact filesystem pathname", expectedPath)
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
	output, exitCode, err := adapter.git.run(
		ctx, repositoryRoot, "ls-tree", "-r", "-z", "--full-tree", objectHex(tree),
	)
	if err != nil || exitCode != 0 {
		return nil, gitExitError("inspect raw Git tree", exitCode, err)
	}
	if len(output) == 0 {
		return nil, nil
	}
	tokens := bytes.Split(output, []byte{0})
	if len(tokens[len(tokens)-1]) != 0 {
		return nil, fmt.Errorf("raw Git tree is not NUL terminated")
	}
	entries := make([]rawGitTreeEntry, 0, len(tokens)-1)
	for _, token := range tokens[:len(tokens)-1] {
		metadata, rawPath, found := bytes.Cut(token, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !found || len(fields) != 3 {
			return nil, fmt.Errorf("raw Git tree entry is malformed")
		}
		normalized, err := normalizeCommitPath(string(rawPath))
		if err != nil {
			return nil, err
		}
		mode := GitFileMode(fields[0])
		if !mode.valid() || mode == GitModeAbsent {
			return nil, fmt.Errorf("raw Git tree path %s has unsupported mode %s", normalized, mode)
		}
		if (mode == GitModeSubmodule) != (fields[1] == "commit") ||
			(mode != GitModeSubmodule && fields[1] != "blob") {
			return nil, fmt.Errorf("raw Git tree path %s has inconsistent type %s", normalized, fields[1])
		}
		object, err := qualifyGitObjectID(tree.Algorithm(), fields[2])
		if err != nil {
			return nil, err
		}
		entries = append(entries, rawGitTreeEntry{
			path: normalized, mode: mode, kind: fields[1], object: object,
		})
	}
	return entries, nil
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
	return qualifyGitObjectID(algorithm, strings.TrimSpace(string(output)))
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
	return trustedWorktreeBinding{
		root: root, gitDir: gitDir, commonDir: commonDir,
		admin: DigestBytes(adminBytes), config: DigestBytes(config),
	}, nil
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

func existingMatchesCreateRequest(existing GitCommitInspection, request CreateGitCommitRequest) error {
	if len(existing.parents) != 1 || existing.parents[0] != request.parent ||
		existing.tree != request.inspection.indexTree || existing.diff.digest != request.inspection.diff.digest ||
		existing.subject != request.step.message.subject || existing.body != request.body {
		return fmt.Errorf("existing head does not match intended parent, tree, diff, and message")
	}
	for _, change := range existing.diff.changes {
		if err := request.step.paths.ValidateChange(change); err != nil {
			return err
		}
	}
	return nil
}

func (adapter LocalCommitGitAdapter) symbolicBranch(ctx context.Context, worktree string) (string, error) {
	output, exitCode, err := adapter.git.run(ctx, worktree, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || exitCode != 0 {
		return "", gitExitError("inspect commit worktree branch", exitCode, err)
	}
	branch := strings.TrimSpace(string(output))
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return "", err
	}
	return branch, nil
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

func (adapter LocalCommitGitAdapter) runWithInput(
	ctx context.Context,
	repositoryRoot string,
	input []byte,
	arguments ...string,
) ([]byte, int, error) {
	repositoryRoot = filepath.Clean(strings.TrimSpace(repositoryRoot))
	if !filepath.IsAbs(repositoryRoot) {
		return nil, -1, fmt.Errorf("Git repository root must be absolute")
	}
	argv := trustedGitArguments(repositoryRoot, arguments...)
	command := exec.CommandContext(ctx, adapter.git.executable, argv...)
	command.Env = mergeProcessEnvironment(os.Environ(), adapter.git.environment)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr boundedProcessBuffer
	stdout.maximum = maxAttemptGitOutputBytes
	stderr.maximum = 64 * 1024
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, -1, fmt.Errorf("Git output exceeded its bound")
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return stdout.bytes(), exitError.ExitCode(), nil
		}
		return nil, -1, err
	}
	return stdout.bytes(), 0, nil
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

func parsePorcelainV2Status(content []byte) (unstaged, untracked, conflicted []string, err error) {
	if len(content) == 0 {
		return nil, nil, nil, nil
	}
	tokens := bytes.Split(content, []byte{0})
	if len(tokens[len(tokens)-1]) != 0 {
		return nil, nil, nil, fmt.Errorf("Git status is not NUL terminated")
	}
	tokens = tokens[:len(tokens)-1]
	for index := 0; index < len(tokens); index++ {
		record := string(tokens[index])
		if record == "" {
			return nil, nil, nil, fmt.Errorf("Git status contains an empty record")
		}
		switch record[0] {
		case '1':
			fields := strings.SplitN(record, " ", 9)
			if len(fields) != 9 || len(fields[1]) != 2 {
				return nil, nil, nil, fmt.Errorf("ordinary Git status record is malformed")
			}
			dirtySubmodule, err := dirtySubmoduleStatus(fields[2])
			if err != nil {
				return nil, nil, nil, err
			}
			if fields[1][1] != '.' || dirtySubmodule {
				unstaged = append(unstaged, fields[8])
			}
		case '2':
			fields := strings.SplitN(record, " ", 10)
			if len(fields) != 10 || len(fields[1]) != 2 || index+1 >= len(tokens) {
				return nil, nil, nil, fmt.Errorf("renamed Git status record is malformed")
			}
			dirtySubmodule, err := dirtySubmoduleStatus(fields[2])
			if err != nil {
				return nil, nil, nil, err
			}
			if fields[1][1] != '.' || dirtySubmodule {
				unstaged = append(unstaged, fields[9])
			}
			index++ // porcelain v2 -z carries the original path as the next token.
		case 'u':
			fields := strings.SplitN(record, " ", 11)
			if len(fields) != 11 {
				return nil, nil, nil, fmt.Errorf("unmerged Git status record is malformed")
			}
			conflicted = append(conflicted, fields[10])
		case '?':
			if len(record) < 3 || record[1] != ' ' {
				return nil, nil, nil, fmt.Errorf("untracked Git status record is malformed")
			}
			untracked = append(untracked, record[2:])
		case '!':
			if len(record) < 3 || record[1] != ' ' {
				return nil, nil, nil, fmt.Errorf("ignored Git status record is malformed")
			}
			untracked = append(untracked, record[2:])
		case '#':
		default:
			return nil, nil, nil, fmt.Errorf("unsupported Git status record %q", record)
		}
	}
	return unstaged, untracked, conflicted, nil
}

func dirtySubmoduleStatus(value string) (bool, error) {
	if value == "N..." || value == "S..." {
		return false, nil
	}
	if len(value) != 4 || value[0] != 'S' ||
		(value[1] != '.' && value[1] != 'C') ||
		(value[2] != '.' && value[2] != 'M') ||
		(value[3] != '.' && value[3] != 'U') {
		return false, fmt.Errorf("Git submodule status %q is malformed", value)
	}
	return true, nil
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

func canonicalCommitMessage(subject, body string) []byte {
	if body == "" {
		return []byte(subject + "\n")
	}
	return []byte(subject + "\n\n" + body + "\n")
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
