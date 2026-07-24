package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const maxAttemptGitOutputBytes = 8 * 1024 * 1024
const maxAttemptWorktreeClaimBytes = 16 * 1024

type AttemptRefInventory struct {
	local  []string
	remote []string
}

func NewAttemptRefInventory(local, remote []string) (AttemptRefInventory, error) {
	inventory := AttemptRefInventory{
		local: append([]string(nil), local...), remote: append([]string(nil), remote...),
	}
	for _, values := range [][]string{inventory.local, inventory.remote} {
		for _, value := range values {
			if _, err := normalizeHeadRef(value); err != nil {
				return AttemptRefInventory{}, err
			}
		}
	}
	sort.Strings(inventory.local)
	sort.Strings(inventory.remote)
	return inventory, nil
}

func (inventory AttemptRefInventory) Local() []string {
	return append([]string(nil), inventory.local...)
}
func (inventory AttemptRefInventory) Remote() []string {
	return append([]string(nil), inventory.remote...)
}

type AttemptGitInspection struct {
	branchExists       bool
	branchHead         GitObjectID
	worktreeExists     bool
	worktreeRegistered bool
	worktreeBranch     string
	worktreeHead       GitObjectID
	clean              bool
	digest             Digest
}

func NewAttemptGitInspection(
	branchExists bool,
	branchHead GitObjectID,
	worktreeExists, worktreeRegistered bool,
	worktreeBranch string,
	worktreeHead GitObjectID,
	clean bool,
) (AttemptGitInspection, error) {
	inspection := AttemptGitInspection{
		branchExists: branchExists, branchHead: branchHead,
		worktreeExists: worktreeExists, worktreeRegistered: worktreeRegistered,
		worktreeBranch: worktreeBranch, worktreeHead: worktreeHead, clean: clean,
	}
	if branchExists != !branchHead.IsZero() {
		return AttemptGitInspection{}, fmt.Errorf("branch existence and object identity disagree")
	}
	if worktreeRegistered && strings.TrimSpace(worktreeBranch) == "" {
		return AttemptGitInspection{}, fmt.Errorf("registered worktree requires its branch")
	}
	if worktreeRegistered && worktreeExists && worktreeHead.IsZero() {
		return AttemptGitInspection{}, fmt.Errorf("existing registered worktree requires its head")
	}
	if clean && (!worktreeExists || !worktreeRegistered || worktreeHead.IsZero()) {
		return AttemptGitInspection{}, fmt.Errorf("clean state requires an existing registered worktree")
	}
	digest, err := digestAttemptGitInspection(inspection)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	inspection.digest = digest
	return inspection, nil
}

func (inspection AttemptGitInspection) BranchExists() bool      { return inspection.branchExists }
func (inspection AttemptGitInspection) BranchHead() GitObjectID { return inspection.branchHead }
func (inspection AttemptGitInspection) WorktreeExists() bool    { return inspection.worktreeExists }
func (inspection AttemptGitInspection) WorktreeRegistered() bool {
	return inspection.worktreeRegistered
}
func (inspection AttemptGitInspection) WorktreeBranch() string    { return inspection.worktreeBranch }
func (inspection AttemptGitInspection) WorktreeHead() GitObjectID { return inspection.worktreeHead }
func (inspection AttemptGitInspection) Clean() bool               { return inspection.clean }
func (inspection AttemptGitInspection) Digest() Digest            { return inspection.digest }

// AttemptWorktreeClaim is the durable ownership proof used while Git is
// materializing an attempt worktree. The claim is written beside the target
// before Git is invoked, so an unregistered partial checkout can be removed on
// retry without treating an unrelated pre-existing path as attempt-owned.
type AttemptWorktreeClaim struct {
	attemptID  ID
	generation Digest
	base       GitObjectID
	branch     string
	worktree   string
}

func NewAttemptWorktreeClaim(
	attemptID ID,
	generation Digest,
	base GitObjectID,
	branch, worktree string,
) (AttemptWorktreeClaim, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if attemptID.IsZero() || generation.IsZero() || base.IsZero() || !filepath.IsAbs(worktree) {
		return AttemptWorktreeClaim{}, fmt.Errorf("attempt worktree claim requires attempt, generation, base, and absolute path")
	}
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return AttemptWorktreeClaim{}, err
	}
	return AttemptWorktreeClaim{
		attemptID: attemptID, generation: generation, base: base,
		branch: branch, worktree: worktree,
	}, nil
}

func (claim AttemptWorktreeClaim) AttemptID() ID      { return claim.attemptID }
func (claim AttemptWorktreeClaim) Generation() Digest { return claim.generation }
func (claim AttemptWorktreeClaim) Base() GitObjectID  { return claim.base }
func (claim AttemptWorktreeClaim) Branch() string     { return claim.branch }
func (claim AttemptWorktreeClaim) Worktree() string   { return claim.worktree }

type AttemptGitPort interface {
	ValidateAttemptBranch(context.Context, string, string) error
	ValidateAttemptWorktreeRoot(context.Context, string, string) error
	InspectAttemptRefs(context.Context, string, string) (AttemptRefInventory, error)
	InspectAttemptWorktree(context.Context, string, string, string) (AttemptGitInspection, error)
	PrepareAttemptWorktree(context.Context, string, AttemptWorktreeClaim, bool) error
	ReleaseAttemptWorktreeClaim(context.Context, AttemptWorktreeClaim) error
	CreateAttemptWorktree(context.Context, string, AttemptWorktreeClaim, bool, bool) error
}

type AttemptWorktreeClaimFaultPoint string

const (
	AttemptWorktreeClaimFaultAfterTemporaryCreated AttemptWorktreeClaimFaultPoint = "after_temporary_created"
	AttemptWorktreeClaimFaultAfterTemporaryWritten AttemptWorktreeClaimFaultPoint = "after_temporary_written"
	AttemptWorktreeClaimFaultAfterTemporarySynced  AttemptWorktreeClaimFaultPoint = "after_temporary_synced"
	AttemptWorktreeClaimFaultAfterPublished        AttemptWorktreeClaimFaultPoint = "after_published"
)

type AttemptWorktreeClaimFaultInjector func(AttemptWorktreeClaimFaultPoint) error

type LocalAttemptGitAdapter struct {
	executable         string
	environment        []EnvironmentVariable
	worktreeClaimFault AttemptWorktreeClaimFaultInjector
}

func NewLocalAttemptGitAdapter(executable string, environment []EnvironmentVariable) (LocalAttemptGitAdapter, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" || strings.IndexByte(executable, 0) >= 0 {
		return LocalAttemptGitAdapter{}, fmt.Errorf("Git executable is required")
	}
	copyEnvironment := append([]EnvironmentVariable(nil), environment...)
	seen := make(map[string]struct{}, len(copyEnvironment))
	for _, variable := range copyEnvironment {
		if variable.name == "" {
			return LocalAttemptGitAdapter{}, fmt.Errorf("Git environment contains an invalid variable")
		}
		if unsafeAttemptGitEnvironment(variable.name) {
			return LocalAttemptGitAdapter{}, fmt.Errorf("Git environment variable %s can redirect operations or expose credentials", variable.name)
		}
		if _, exists := seen[variable.name]; exists {
			return LocalAttemptGitAdapter{}, fmt.Errorf("duplicate Git environment variable %s", variable.name)
		}
		seen[variable.name] = struct{}{}
	}
	return LocalAttemptGitAdapter{executable: executable, environment: copyEnvironment}, nil
}

func DefaultLocalAttemptGitAdapter() LocalAttemptGitAdapter {
	adapter, _ := NewLocalAttemptGitAdapter("git", nil)
	return adapter
}

func (adapter LocalAttemptGitAdapter) WithAttemptWorktreeClaimFaultInjector(
	injector AttemptWorktreeClaimFaultInjector,
) LocalAttemptGitAdapter {
	adapter.worktreeClaimFault = injector
	return adapter
}

func (adapter LocalAttemptGitAdapter) ValidateAttemptBranch(ctx context.Context, repositoryRoot, branch string) error {
	if err := validateAttemptBranchSyntax(branch); err != nil {
		return err
	}
	_, exitCode, err := adapter.run(ctx, repositoryRoot, "check-ref-format", "--branch", branch)
	if err != nil {
		return fmt.Errorf("validate attempt branch: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("Git rejected attempt branch %q", branch)
	}
	return nil
}

func (adapter LocalAttemptGitAdapter) InspectAttemptRefs(
	ctx context.Context,
	repositoryRoot, _ string,
) (AttemptRefInventory, error) {
	localOutput, exitCode, err := adapter.run(ctx, repositoryRoot, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return AttemptRefInventory{}, fmt.Errorf("inspect local branches: %w", err)
	}
	local, err := parseLocalHeadRefs(localOutput)
	if err != nil {
		return AttemptRefInventory{}, err
	}
	return NewAttemptRefInventory(local, nil)
}

func (adapter LocalAttemptGitAdapter) InspectAttemptWorktree(
	ctx context.Context,
	repositoryRoot, branch, worktree string,
) (AttemptGitInspection, error) {
	algorithm, err := adapter.objectFormat(ctx, repositoryRoot)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	branchOutput, branchExit, err := adapter.run(ctx, repositoryRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt branch: %w", err)
	}
	branchExists := branchExit == 0
	if branchExit != 0 && branchExit != 1 {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt branch: Git exited with status %d", branchExit)
	}
	var branchHead GitObjectID
	if branchExists {
		branchHead, err = qualifyGitObjectID(algorithm, strings.TrimSpace(string(branchOutput)))
		if err != nil {
			return AttemptGitInspection{}, fmt.Errorf("inspect attempt branch object: %w", err)
		}
	}
	listOutput, exitCode, err := adapter.run(ctx, repositoryRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect registered worktrees: %w", err)
	}
	registered, err := parseRegisteredWorktrees(listOutput)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	worktree = filepath.Clean(worktree)
	_, statErr := os.Lstat(worktree)
	worktreeExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree path: %w", statErr)
	}
	registeredPath := canonicalWorktreePath(worktree)
	record, worktreeRegistered := registered[registeredPath]
	if !worktreeRegistered || !worktreeExists {
		return NewAttemptGitInspection(
			branchExists, branchHead, worktreeExists, worktreeRegistered,
			record.branch, GitObjectID{}, false,
		)
	}
	if record.detached {
		return AttemptGitInspection{}, fmt.Errorf("attempt worktree %s is detached", worktree)
	}
	commitAdapter := LocalCommitGitAdapter{git: adapter}
	binding, err := commitAdapter.captureTrustedWorktreeBinding(ctx, worktree)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree binding: %w", err)
	}
	worktree = binding.root
	headOutput, headExit, err := adapter.run(ctx, worktree, "rev-parse", "--verify", "HEAD")
	if err != nil || headExit != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", headExit)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree head: %w", err)
	}
	head, err := qualifyGitObjectID(algorithm, strings.TrimSpace(string(headOutput)))
	if err != nil {
		return AttemptGitInspection{}, err
	}
	branchOutput, branchExit, err = adapter.run(ctx, worktree, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branchExit != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", branchExit)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree branch: %w", err)
	}
	worktreeBranch := strings.TrimSpace(string(branchOutput))
	indexTree, err := commitAdapter.writeTree(ctx, worktree, algorithm)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree index tree: %w", err)
	}
	indexOutput, indexExit, err := adapter.run(ctx, worktree, "ls-files", "-v", "-z", "--")
	if err != nil || indexExit != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", indexExit)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree index flags: %w", err)
	}
	if err := rejectHiddenIndexRecords(indexOutput); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree index flags: %w", err)
	}
	fsmonitorOutput, fsmonitorExit, err := adapter.run(ctx, worktree, "ls-files", "-f", "-z", "--")
	if err != nil || fsmonitorExit != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", fsmonitorExit)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree fsmonitor flags: %w", err)
	}
	if err := rejectFSMonitorIndexRecords(fsmonitorOutput); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree fsmonitor flags: %w", err)
	}
	statusOutput, statusExit, err := adapter.run(
		ctx, worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching",
		"--ignore-submodules=none",
	)
	if err != nil || statusExit != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", statusExit)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree status: %w", err)
	}
	clean := len(statusOutput) == 0
	if clean {
		tree, err := commitAdapter.resolveObject(ctx, worktree, algorithm, objectHex(head)+"^{tree}")
		if err != nil {
			return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree tree: %w", err)
		}
		if indexTree != tree {
			return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree index tree does not match head tree")
		}
		if err := commitAdapter.verifyRawTreeMaterialization(ctx, worktree, tree); err != nil {
			return AttemptGitInspection{}, fmt.Errorf("inspect attempt raw worktree: %w", err)
		}
	}
	if err := commitAdapter.confirmTrustedCommitState(ctx, binding, worktreeBranch, head, indexTree); err != nil {
		return AttemptGitInspection{}, fmt.Errorf("confirm attempt worktree Git state: %w", err)
	}
	confirmedBranch, confirmedExit, err := adapter.run(
		ctx, repositoryRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch,
	)
	if err != nil || confirmedExit != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", confirmedExit)
		}
		return AttemptGitInspection{}, fmt.Errorf("confirm attempt branch: %w", err)
	}
	confirmedBranchHead, err := qualifyGitObjectID(algorithm, strings.TrimSpace(string(confirmedBranch)))
	if err != nil {
		return AttemptGitInspection{}, err
	}
	if !branchExists || confirmedBranchHead != branchHead {
		return AttemptGitInspection{}, fmt.Errorf("attempt branch changed during worktree verification")
	}
	return NewAttemptGitInspection(
		branchExists, branchHead, true, true, worktreeBranch, head, clean,
	)
}

func (adapter LocalAttemptGitAdapter) CreateAttemptWorktree(
	ctx context.Context,
	repositoryRoot string,
	claim AttemptWorktreeClaim,
	createBranch, recoverRegistered bool,
) error {
	if claim.base.IsZero() || !filepath.IsAbs(claim.worktree) {
		return fmt.Errorf("attempt worktree creation requires an exact base and absolute path")
	}
	marker := attemptWorktreeClaimPath(claim.worktree)
	parentPath, err := canonicalizeTrustedRootPath(filepath.Dir(marker))
	if err != nil {
		return err
	}
	parent, err := OpenVerifiedRoot(RootRoleWorktree, parentPath, false)
	if err != nil {
		return fmt.Errorf("reopen claimed attempt worktree root: %w", err)
	}
	defer parent.Close()
	worktree := filepath.Join(
		parent.Path(), filepath.Base(filepath.Clean(claim.worktree)),
	)
	expectedClaim, err := canonicalAttemptWorktreeClaim(
		claim, worktree, parent.Identity(),
	)
	if err != nil {
		return err
	}
	markerName := filepath.Base(marker)
	durableClaim, err := readAttemptWorktreeClaim(parent, markerName)
	if err != nil {
		return fmt.Errorf("reopen durable attempt worktree claim: %w", err)
	}
	if !bytes.Equal(durableClaim, expectedClaim) {
		return fmt.Errorf(
			"attempt worktree claim %s does not bind the verified parent root",
			marker,
		)
	}
	if err := parent.VerifyPath(); err != nil {
		return fmt.Errorf("verify claimed attempt worktree root before Git creation: %w", err)
	}
	commitAdapter := LocalCommitGitAdapter{git: adapter}
	binding, err := commitAdapter.captureTrustedWorktreeBinding(ctx, repositoryRoot)
	if err != nil {
		return fmt.Errorf("inspect attempt materialization Git binding: %w", err)
	}
	baseText := strings.TrimPrefix(
		claim.base.String(), string(claim.base.algorithm)+":",
	)
	arguments := []string{"worktree", "add"}
	if createBranch {
		if recoverRegistered {
			return fmt.Errorf("new attempt branch cannot recover an existing worktree registration")
		}
		arguments = append(
			arguments, "--no-track", "-b", claim.branch, worktree, baseText,
		)
	} else {
		if recoverRegistered {
			arguments = append(arguments, "--force")
		}
		arguments = append(arguments, worktree, claim.branch)
	}
	if err := parent.VerifyPath(); err != nil {
		return fmt.Errorf("revalidate claimed attempt worktree root before Git effect: %w", err)
	}
	if err := adapter.validateAttemptCheckoutProfile(
		ctx, binding, claim.base,
	); err != nil {
		return err
	}
	if err := parent.VerifyPath(); err != nil {
		return fmt.Errorf("revalidate claimed attempt worktree root after checkout-profile inspection: %w", err)
	}
	preEffectClaim, err := readAttemptWorktreeClaim(parent, markerName)
	if err != nil {
		return fmt.Errorf("revalidate durable attempt worktree claim before Git effect: %w", err)
	}
	if !bytes.Equal(preEffectClaim, expectedClaim) {
		return fmt.Errorf("durable attempt worktree claim changed before Git creation")
	}
	_, exitCode, gitErr := adapter.run(ctx, repositoryRoot, arguments...)
	var effectErr error
	if gitErr != nil {
		effectErr = fmt.Errorf("create attempt worktree: %w", gitErr)
	} else if exitCode != 0 {
		effectErr = fmt.Errorf(
			"create attempt worktree: Git exited with status %d", exitCode,
		)
	}
	if err := parent.VerifyPath(); err != nil {
		return errors.Join(
			effectErr,
			fmt.Errorf("verify claimed attempt worktree root after Git creation: %w", err),
		)
	}
	confirmedClaim, err := readAttemptWorktreeClaim(parent, markerName)
	if err != nil {
		return errors.Join(
			effectErr,
			fmt.Errorf("confirm durable attempt worktree claim: %w", err),
		)
	}
	if !bytes.Equal(confirmedClaim, expectedClaim) {
		return errors.Join(
			effectErr,
			fmt.Errorf("durable attempt worktree claim changed during Git creation"),
		)
	}
	if effectErr != nil {
		return effectErr
	}
	confirmed, err := commitAdapter.captureTrustedWorktreeBinding(ctx, binding.root)
	if err != nil {
		return fmt.Errorf("confirm attempt materialization Git binding: %w", err)
	}
	if confirmed != binding {
		return fmt.Errorf("Git worktree administration changed during attempt materialization")
	}
	if err := parent.VerifyPath(); err != nil {
		return fmt.Errorf("finalize claimed attempt worktree root verification: %w", err)
	}
	finalClaim, err := readAttemptWorktreeClaim(parent, markerName)
	if err != nil {
		return fmt.Errorf("finalize durable attempt worktree claim verification: %w", err)
	}
	if !bytes.Equal(finalClaim, expectedClaim) {
		return fmt.Errorf("durable attempt worktree claim changed after Git creation")
	}
	return nil
}

func (adapter LocalAttemptGitAdapter) validateAttemptCheckoutProfile(
	ctx context.Context,
	binding trustedWorktreeBinding,
	base GitObjectID,
) (resultErr error) {
	if binding.root == "" || binding.commonDir == "" || base.IsZero() {
		return fmt.Errorf("attempt checkout profile requires bound Git administration and an exact base")
	}
	target := LocalTargetGitAdapter{git: adapter}
	if err := target.inspectBaseTree(ctx, binding.root, base); err != nil {
		return fmt.Errorf("inspect exact attempt checkout tree: %w", err)
	}
	commonRoot, err := OpenVerifiedRoot(
		RootRoleGitCommon, binding.commonDir, false,
	)
	if err != nil {
		return fmt.Errorf("open attempt checkout Git common directory: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, commonRoot.Close())
	}()
	_, exists, err := commonRoot.adapter.inspectExact("info/attributes")
	if err != nil {
		return fmt.Errorf("inspect attempt checkout Git attributes: %w", err)
	}
	if exists {
		return fmt.Errorf(
			"external Git attributes metadata %s is not supported during attempt checkout",
			filepath.Join(binding.commonDir, "info", "attributes"),
		)
	}
	if err := commonRoot.VerifyPath(); err != nil {
		return fmt.Errorf("verify attempt checkout Git common directory: %w", err)
	}
	return nil
}

func (adapter LocalAttemptGitAdapter) PrepareAttemptWorktree(
	ctx context.Context,
	repositoryRoot string,
	claim AttemptWorktreeClaim,
	recoverUnregistered bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	guard, err := adapter.openAttemptWorktreeRootGuard(
		ctx, repositoryRoot, claim.worktree, true,
	)
	if err != nil {
		return err
	}
	defer guard.Close()
	parent := guard.worktree
	if err := guard.Verify(ctx, adapter); err != nil {
		return fmt.Errorf("verify attempt worktree roots before capability probe: %w", err)
	}
	if effectErr := parent.ProbeDurability(); effectErr != nil {
		return errors.Join(
			fmt.Errorf("preflight attempt worktree root capabilities: %w", effectErr),
			guard.verifyAfterEffect(ctx, adapter, "capability probe"),
		)
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return fmt.Errorf("verify attempt worktree roots after capability probe: %w", err)
	}
	marker := attemptWorktreeClaimPath(claim.worktree)
	worktreeName := filepath.Base(filepath.Clean(claim.worktree))
	markerName := filepath.Base(marker)
	canonicalWorktree := filepath.Join(parent.Path(), worktreeName)
	payload, err := canonicalAttemptWorktreeClaim(claim, canonicalWorktree, parent.Identity())
	if err != nil {
		return err
	}
	created, effectErr := createOrVerifyAttemptWorktreeClaim(
		parent, markerName, worktreeName, payload, adapter.worktreeClaimFault,
	)
	if effectErr != nil {
		return errors.Join(
			effectErr,
			guard.verifyAfterEffect(ctx, adapter, "claim publication"),
		)
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return fmt.Errorf("verify attempt worktree roots after claim publication: %w", err)
	}
	info, worktreeExists, err := parent.adapter.inspectExact(worktreeName)
	if err != nil {
		return fmt.Errorf("inspect claimed attempt worktree: %w", err)
	}
	if created && worktreeExists {
		_, removeErr := parent.adapter.removeFileContentExact(
			markerName, payload, int64(len(payload)), parent.VerifyPath,
		)
		return errors.Join(
			fmt.Errorf(
				"attempt worktree path %s appeared while ownership was being claimed",
				canonicalWorktree,
			),
			removeErr,
			guard.verifyAfterEffect(ctx, adapter, "raced claim cleanup"),
		)
	}
	if !worktreeExists {
		return nil
	}
	if !recoverUnregistered {
		return fmt.Errorf("attempt worktree path %s exists but recovery was not requested", canonicalWorktree)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("claimed attempt worktree path %s is not a recoverable directory", canonicalWorktree)
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return fmt.Errorf("verify attempt worktree roots before partial recovery: %w", err)
	}
	if effectErr := parent.adapter.removeDirectoryTreeExact(worktreeName); effectErr != nil {
		return errors.Join(
			fmt.Errorf("remove partial attempt worktree %s: %w", canonicalWorktree, effectErr),
			guard.verifyAfterEffect(ctx, adapter, "partial recovery"),
		)
	}
	if err := guard.Verify(ctx, adapter); err != nil {
		return fmt.Errorf("synchronize recovered attempt worktree roots: %w", err)
	}
	return nil
}

func (adapter LocalAttemptGitAdapter) ReleaseAttemptWorktreeClaim(
	ctx context.Context,
	claim AttemptWorktreeClaim,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	marker := attemptWorktreeClaimPath(claim.worktree)
	parentPath, err := canonicalizeTrustedRootPath(filepath.Dir(marker))
	if err != nil {
		return err
	}
	parent, err := OpenVerifiedRoot(RootRoleWorktree, parentPath, false)
	if err != nil {
		return fmt.Errorf("open attempt worktree root: %w", err)
	}
	defer parent.Close()
	canonicalWorktree := filepath.Join(parent.Path(), filepath.Base(filepath.Clean(claim.worktree)))
	payload, err := canonicalAttemptWorktreeClaim(claim, canonicalWorktree, parent.Identity())
	if err != nil {
		return err
	}
	markerName := filepath.Base(marker)
	existing, err := readAttemptWorktreeClaim(parent, markerName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, payload) {
		return fmt.Errorf("attempt worktree claim %s belongs to different immutable bindings", marker)
	}
	removed, err := parent.adapter.removeFileContentExact(
		markerName, payload, int64(len(payload)), parent.VerifyPath,
	)
	if err != nil {
		return fmt.Errorf("release attempt worktree claim: %w", err)
	}
	if !removed {
		return fmt.Errorf("release attempt worktree claim: claim disappeared")
	}
	return nil
}

func canonicalAttemptWorktreeClaim(
	claim AttemptWorktreeClaim,
	canonicalWorktree string,
	rootIdentity PlatformFileIdentity,
) ([]byte, error) {
	if claim.attemptID.IsZero() || claim.generation.IsZero() || claim.base.IsZero() ||
		!filepath.IsAbs(canonicalWorktree) {
		return nil, fmt.Errorf("attempt worktree claim is incomplete")
	}
	if err := validateAttemptBranchSyntax(claim.branch); err != nil {
		return nil, err
	}
	type claimJSON struct {
		SchemaVersion int                  `json:"schema_version"`
		Kind          string               `json:"kind"`
		AttemptID     string               `json:"attempt_id"`
		Generation    string               `json:"generation"`
		Base          string               `json:"base"`
		Branch        string               `json:"branch"`
		Worktree      string               `json:"worktree"`
		RootIdentity  PlatformFileIdentity `json:"root_identity"`
	}
	return json.Marshal(claimJSON{
		SchemaVersion: RuntimeFormatSchemaVersion, Kind: "attempt_worktree_claim",
		AttemptID: claim.attemptID.String(), Generation: claim.generation.String(),
		Base: claim.base.String(), Branch: claim.branch, Worktree: canonicalWorktree,
		RootIdentity: rootIdentity,
	})
}

func attemptWorktreeClaimPath(worktree string) string {
	return filepath.Clean(worktree) + ".feature-attempt-claim"
}

func createOrVerifyAttemptWorktreeClaim(
	root *VerifiedRoot,
	marker, worktree string,
	payload []byte,
	fault AttemptWorktreeClaimFaultInjector,
) (bool, error) {
	for {
		published, retry, err := createOrVerifyAttemptWorktreeClaimOnce(
			root, marker, worktree, payload, fault,
		)
		if retry {
			continue
		}
		return published, err
	}
}

func createOrVerifyAttemptWorktreeClaimOnce(
	root *VerifiedRoot,
	marker, worktree string,
	payload []byte,
	fault AttemptWorktreeClaimFaultInjector,
) (bool, bool, error) {
	if root == nil {
		return false, false, fmt.Errorf("attempt worktree claim requires a verified root")
	}
	if _, exists, err := root.adapter.inspectExact(marker); err != nil {
		return false, false, fmt.Errorf("inspect attempt worktree claim: %w", err)
	} else if exists {
		existing, readErr := readAttemptWorktreeClaim(root, marker)
		if readErr != nil {
			return false, false, readErr
		}
		if !bytes.Equal(existing, payload) {
			return false, false, fmt.Errorf("attempt worktree claim %s belongs to different immutable bindings", marker)
		}
		if err := root.Sync(); err != nil {
			return false, false, fmt.Errorf("synchronize existing attempt worktree claim: %w", err)
		}
		return false, false, nil
	}
	if _, exists, err := root.adapter.inspectExact(worktree); err != nil {
		return false, false, fmt.Errorf("inspect attempt worktree before claim: %w", err)
	} else if exists {
		return false, false, fmt.Errorf("attempt worktree path %s predates its ownership claim", worktree)
	}

	pending := marker + ".pending"
	file, created, err := root.adapter.openRegularFileExact(
		pending, os.O_RDWR, 0o600, true,
	)
	if err != nil {
		return false, false, fmt.Errorf("open pending attempt worktree claim: %w", err)
	}
	fileOpen := true
	locked := false
	defer func() {
		if locked {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		}
		if fileOpen {
			_ = file.Close()
		}
	}()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return false, false, fmt.Errorf("pending attempt worktree claim %s is active", pending)
		}
		return false, false, fmt.Errorf("lock pending attempt worktree claim: %w", err)
	}
	locked = true
	if err := root.verifyOwnedRegularFile(pending, file); err != nil {
		return false, false, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return false, false, fmt.Errorf("inspect pending attempt worktree claim: %w", err)
	}
	if err := root.adapter.verifyOpenedFileExact(pending, file); err != nil {
		return false, false, err
	}
	if !created {
		existing, readErr := readOpenedFileBounded(file, maxAttemptWorktreeClaimBytes)
		if readErr != nil {
			return false, false, fmt.Errorf("read pending attempt worktree claim: %w", readErr)
		}
		if !bytes.Equal(existing, payload) {
			if json.Valid(existing) {
				return false, false, fmt.Errorf(
					"pending attempt worktree claim belongs to different immutable bindings",
				)
			}
			removed, removeErr := root.adapter.removeFileIdentityExact(
				pending,
				openedInfo,
				func() error {
					if err := root.VerifyPath(); err != nil {
						return err
					}
					if _, exists, err := root.adapter.inspectExact(marker); err != nil {
						return err
					} else if exists {
						return fmt.Errorf("attempt worktree claim appeared during pending recovery")
					}
					if _, exists, err := root.adapter.inspectExact(worktree); err != nil {
						return err
					} else if exists {
						return fmt.Errorf("attempt worktree appeared during pending recovery")
					}
					return root.adapter.verifyOpenedFileExact(pending, file)
				},
			)
			if removeErr != nil {
				return false, false, fmt.Errorf(
					"recover partial pending attempt worktree claim: %w", removeErr,
				)
			}
			if !removed {
				return false, false, fmt.Errorf(
					"recover partial pending attempt worktree claim: staging file disappeared",
				)
			}
			if err := root.Sync(); err != nil {
				return false, false, fmt.Errorf(
					"synchronize recovered pending attempt worktree claim: %w", err,
				)
			}
			return false, true, nil
		}
	}
	removeOnFailure := true
	defer func() {
		if removeOnFailure && created {
			_, _ = root.adapter.removeFileIdentityExact(pending, openedInfo, nil)
		}
	}()
	if created {
		if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterTemporaryCreated); err != nil {
			return false, false, err
		}
		if err := writeAll(file, payload); err != nil {
			return false, false, fmt.Errorf("write pending attempt worktree claim: %w", err)
		}
		if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterTemporaryWritten); err != nil {
			return false, false, err
		}
	}
	if err := file.Sync(); err != nil {
		return false, false, fmt.Errorf("synchronize pending attempt worktree claim: %w", err)
	}
	if err := root.adapter.verifyOpenedFileExact(pending, file); err != nil {
		return false, false, err
	}
	if created {
		if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterTemporarySynced); err != nil {
			return false, false, err
		}
	}
	if err := root.adapter.renameFileNoReplace(pending, marker); err != nil {
		if existing, readErr := readAttemptWorktreeClaim(root, marker); readErr == nil &&
			bytes.Equal(existing, payload) {
			_, _ = root.adapter.removeFileContentExact(
				pending, payload, int64(len(payload)), nil,
			)
			removeOnFailure = false
			return false, false, root.Sync()
		}
		return false, false, fmt.Errorf("publish attempt worktree claim without replacement: %w", err)
	}
	removeOnFailure = false
	if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterPublished); err != nil {
		return false, false, err
	}
	if err := root.Sync(); err != nil {
		return false, false, fmt.Errorf("publish attempt worktree claim: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return false, false, fmt.Errorf("unlock published attempt worktree claim: %w", err)
	}
	locked = false
	if err := file.Close(); err != nil {
		return false, false, fmt.Errorf("close published attempt worktree claim: %w", err)
	}
	fileOpen = false
	return true, false, nil
}

func injectAttemptWorktreeClaimFault(
	injector AttemptWorktreeClaimFaultInjector,
	point AttemptWorktreeClaimFaultPoint,
) error {
	if injector == nil {
		return nil
	}
	if err := injector(point); err != nil {
		return fmt.Errorf("attempt worktree claim fault at %s: %w", point, err)
	}
	return nil
}

func readAttemptWorktreeClaim(root *VerifiedRoot, marker string) ([]byte, error) {
	content, err := root.ReadBounded(marker, maxAttemptWorktreeClaimBytes)
	if err != nil {
		return nil, fmt.Errorf("read attempt worktree claim: %w", err)
	}
	return content, nil
}

type registeredWorktree struct {
	branch   string
	detached bool
}

func parseRegisteredWorktrees(content []byte) (map[string]registeredWorktree, error) {
	result := make(map[string]registeredWorktree)
	var path string
	var record registeredWorktree
	flush := func() error {
		if path == "" {
			return nil
		}
		path = canonicalWorktreePath(path)
		if _, exists := result[path]; exists {
			return fmt.Errorf("duplicate registered Git worktree %s", path)
		}
		result[path] = record
		path, record = "", registeredWorktree{}
		return nil
	}
	for _, raw := range bytes.Split(content, []byte{0}) {
		line := string(raw)
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, found := strings.Cut(line, " ")
		switch key {
		case "worktree":
			if path != "" {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			if !found || !filepath.IsAbs(value) {
				return nil, fmt.Errorf("Git returned an invalid registered worktree path")
			}
			path = value
		case "branch":
			if !found || !strings.HasPrefix(value, "refs/heads/") {
				return nil, fmt.Errorf("Git returned an invalid registered worktree branch")
			}
			record.branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			record.detached = true
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
}

func canonicalWorktreePath(path string) string {
	path = filepath.Clean(path)
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(canonical)
	}
	ancestor := path
	var suffix []string
	for {
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return path
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
		canonical, err := filepath.EvalSymlinks(ancestor)
		if err != nil {
			continue
		}
		for index := len(suffix) - 1; index >= 0; index-- {
			canonical = filepath.Join(canonical, suffix[index])
		}
		return filepath.Clean(canonical)
	}
}

func parseLocalHeadRefs(content []byte) ([]string, error) {
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "refs/heads/") {
			return nil, fmt.Errorf("Git returned non-head local ref %q", line)
		}
		result = append(result, strings.TrimPrefix(line, "refs/heads/"))
	}
	return result, nil
}

func parseRemoteHeadRefs(content []byte) ([]string, error) {
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/heads/") {
			return nil, fmt.Errorf("Git returned malformed remote head record")
		}
		result = append(result, strings.TrimPrefix(fields[1], "refs/heads/"))
	}
	return result, nil
}

func qualifyGitObjectID(algorithm GitHashAlgorithm, raw string) (GitObjectID, error) {
	return ParseGitObjectID(string(algorithm) + ":" + strings.TrimSpace(raw))
}

func (adapter LocalAttemptGitAdapter) objectFormat(ctx context.Context, repositoryRoot string) (GitHashAlgorithm, error) {
	output, exitCode, err := adapter.run(ctx, repositoryRoot, "rev-parse", "--show-object-format")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return "", fmt.Errorf("inspect Git object format: %w", err)
	}
	algorithm := GitHashAlgorithm(strings.TrimSpace(string(output)))
	if algorithm != GitHashSHA1 && algorithm != GitHashSHA256 {
		return "", fmt.Errorf("unsupported repository Git object format %q", algorithm)
	}
	return algorithm, nil
}

func (adapter LocalAttemptGitAdapter) run(
	ctx context.Context,
	repositoryRoot string,
	arguments ...string,
) ([]byte, int, error) {
	repositoryRoot = filepath.Clean(strings.TrimSpace(repositoryRoot))
	if !filepath.IsAbs(repositoryRoot) {
		return nil, -1, fmt.Errorf("Git repository root must be absolute")
	}
	argv := trustedGitArguments(repositoryRoot, arguments...)
	command := exec.CommandContext(ctx, adapter.executable, argv...)
	environment, err := BuildNonProviderProcessEnvironment(os.Environ(), adapter.environment)
	if err != nil {
		return nil, -1, err
	}
	command.Env = environment
	var stdout, stderr boundedProcessBuffer
	stdout.maximum = maxAttemptGitOutputBytes
	stderr.maximum = 64 * 1024
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
			return stdout.bytes(), exitCode, nil
		}
		return nil, -1, err
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, -1, fmt.Errorf("Git output exceeded its bound")
	}
	return stdout.bytes(), exitCode, nil
}

type boundedProcessBuffer struct {
	content  bytes.Buffer
	maximum  int
	exceeded bool
}

func (buffer *boundedProcessBuffer) Write(value []byte) (int, error) {
	if buffer.content.Len()+len(value) > buffer.maximum {
		remaining := buffer.maximum - buffer.content.Len()
		if remaining > 0 {
			_, _ = buffer.content.Write(value[:remaining])
		}
		buffer.exceeded = true
		return len(value), nil
	}
	return buffer.content.Write(value)
}

func (buffer *boundedProcessBuffer) bytes() []byte {
	return append([]byte(nil), buffer.content.Bytes()...)
}

func mergeProcessEnvironment(base []string, additions []EnvironmentVariable) []string {
	values := make(map[string]string, len(additions)+20)
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if found && allowedNonProviderAmbientEnvironment(name) {
			values[name] = value
		}
	}
	for _, variable := range additions {
		values[variable.name] = variable.value
	}
	values["GIT_NO_REPLACE_OBJECTS"] = "1"
	values["GIT_NO_LAZY_FETCH"] = "1"
	values["GIT_GRAFT_FILE"] = os.DevNull
	values["GIT_OPTIONAL_LOCKS"] = "0"
	values["GIT_ATTR_NOSYSTEM"] = "1"
	values["GIT_CONFIG_NOSYSTEM"] = "1"
	values["GIT_CONFIG_GLOBAL"] = os.DevNull
	values["GIT_CONFIG_SYSTEM"] = os.DevNull
	values["GIT_TERMINAL_PROMPT"] = "0"
	values["GCM_INTERACTIVE"] = "Never"
	values["GIT_ASKPASS"] = os.DevNull
	values["SSH_ASKPASS"] = os.DevNull
	values["GIT_SSH_COMMAND"] = os.DevNull
	values["GIT_SSH_VARIANT"] = "ssh"
	values["HOME"] = os.DevNull
	values["XDG_CONFIG_HOME"] = os.DevNull
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func allowedNonProviderAmbientEnvironment(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "PATH", "TMPDIR", "TMP", "TEMP", "TZ", "LANG", "LC_ALL", "LC_CTYPE",
		"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT":
		return true
	default:
		return false
	}
}

// BuildNonProviderProcessEnvironment is the shared environment boundary for
// local Git, checks, implementation processes, and reviewers. It removes
// all ambient state except a small operational allowlist, then forces
// credential helpers, prompts, SSH identities, and system/global Git config
// off. Network sandboxing and provider-broker exclusion remain mandatory
// responsibilities of the typed runner port.
func BuildNonProviderProcessEnvironment(
	base []string,
	additions []EnvironmentVariable,
) ([]string, error) {
	for _, variable := range additions {
		if variable.name == "" || unsafeAttemptGitEnvironment(variable.name) {
			return nil, fmt.Errorf("non-provider environment variable %s is unsafe", variable.name)
		}
	}
	return mergeProcessEnvironment(base, additions), nil
}

func unsafeAttemptGitEnvironment(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if providerCredentialEnvironment(upper) || upper == "GIT_CONFIG" || strings.HasPrefix(upper, "GIT_CONFIG_") {
		return true
	}
	switch upper {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_NO_REPLACE_OBJECTS", "GIT_GRAFT_FILE", "GIT_OPTIONAL_LOCKS",
		"GIT_ATTR_NOSYSTEM",
		"GIT_ASKPASS", "GIT_TERMINAL_PROMPT", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT",
		"SSH_ASKPASS", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GCM_INTERACTIVE",
		"GIT_PROXY_COMMAND", "GIT_EXEC_PATH", "GIT_TEMPLATE_DIR":
		return true
	default:
		return false
	}
}

func providerCredentialEnvironment(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch upper {
	case "TOKEN", "PAT", "PASSWORD", "PASS", "PASSPHRASE", "SECRET", "API_KEY", "ACCESS_KEY",
		"PRIVATE_KEY", "CREDENTIAL", "CREDENTIALS", "AUTHORIZATION", "AUTH_HEADER",
		"GH_TOKEN", "GITHUB_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GITLAB_TOKEN",
		"BITBUCKET_TOKEN", "AZURE_DEVOPS_EXT_PAT", "SYSTEM_ACCESSTOKEN", "ADO_PAT",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE",
		"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_API_KEY", "GCLOUD_ACCESS_TOKEN",
		"CLOUDSDK_CONFIG", "AZURE_CLIENT_SECRET", "AZURE_CONFIG_DIR", "ARM_CLIENT_SECRET",
		"DOCKER_CONFIG", "KUBECONFIG", "NETRC", "GH_CONFIG_DIR", "CI_JOB_TOKEN", "NPM_TOKEN":
		return true
	}
	for _, suffix := range []string{
		"_TOKEN", "_PAT", "_PASSWORD", "_PASS", "_PASSPHRASE", "_SECRET",
		"_API_KEY", "_ACCESS_KEY", "_PRIVATE_KEY", "_CLIENT_SECRET", "_CREDENTIAL", "_CREDENTIALS",
	} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	for _, segment := range strings.Split(upper, "_") {
		switch segment {
		case "TOKEN", "PAT", "PASSWORD", "PASS", "PASSPHRASE", "SECRET", "CREDENTIAL", "CREDENTIALS":
			return true
		}
	}
	return false
}

func trustedGitArguments(repositoryRoot string, arguments ...string) []string {
	prefix := []string{
		"--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "credential.helper=",
		"-c", "credential.interactive=false",
		"-c", "core.askPass=" + os.DevNull,
		"-c", "http.extraHeader=",
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=never",
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "log.showSignature=false",
		"-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
		"-C", repositoryRoot,
	}
	return append(prefix, arguments...)
}

func digestAttemptGitInspection(inspection AttemptGitInspection) (Digest, error) {
	type inspectionJSON struct {
		SchemaVersion      int    `json:"schema_version"`
		BranchExists       bool   `json:"branch_exists"`
		BranchHead         string `json:"branch_head,omitempty"`
		WorktreeExists     bool   `json:"worktree_exists"`
		WorktreeRegistered bool   `json:"worktree_registered"`
		WorktreeBranch     string `json:"worktree_branch,omitempty"`
		WorktreeHead       string `json:"worktree_head,omitempty"`
		Clean              bool   `json:"clean"`
	}
	content, err := json.Marshal(inspectionJSON{
		SchemaVersion: JournalSchemaVersion,
		BranchExists:  inspection.branchExists, BranchHead: inspection.branchHead.String(),
		WorktreeExists: inspection.worktreeExists, WorktreeRegistered: inspection.worktreeRegistered,
		WorktreeBranch: inspection.worktreeBranch, WorktreeHead: inspection.worktreeHead.String(),
		Clean: inspection.clean,
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}
