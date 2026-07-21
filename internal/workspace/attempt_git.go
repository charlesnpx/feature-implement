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
)

const maxAttemptGitOutputBytes = 8 * 1024 * 1024

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
	InspectAttemptRefs(context.Context, string, string) (AttemptRefInventory, error)
	InspectAttemptWorktree(context.Context, string, string, string) (AttemptGitInspection, error)
	PrepareAttemptWorktree(context.Context, AttemptWorktreeClaim, bool) error
	ReleaseAttemptWorktreeClaim(context.Context, AttemptWorktreeClaim) error
	CreateAttemptWorktree(context.Context, string, string, string, GitObjectID, bool, bool) error
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
			return LocalAttemptGitAdapter{}, fmt.Errorf("Git environment variable %s can redirect repository operations", variable.name)
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
	repositoryRoot, remote string,
) (AttemptRefInventory, error) {
	localOutput, exitCode, err := adapter.run(ctx, repositoryRoot, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return AttemptRefInventory{}, fmt.Errorf("inspect local branches: %w", err)
	}
	remote = strings.TrimSpace(remote)
	if remote == "" || strings.HasPrefix(remote, "-") || strings.IndexByte(remote, 0) >= 0 {
		return AttemptRefInventory{}, fmt.Errorf("attempt ref inspection requires a remote")
	}
	remoteOutput, exitCode, err := adapter.run(ctx, repositoryRoot, "ls-remote", "--heads", "--refs", "--", remote)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", exitCode)
		}
		return AttemptRefInventory{}, fmt.Errorf("inspect remote branches: %w", err)
	}
	local, err := parseLocalHeadRefs(localOutput)
	if err != nil {
		return AttemptRefInventory{}, err
	}
	remoteRefs, err := parseRemoteHeadRefs(remoteOutput)
	if err != nil {
		return AttemptRefInventory{}, err
	}
	return NewAttemptRefInventory(local, remoteRefs)
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
	statusOutput, statusExit, err := adapter.run(
		ctx, worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none",
	)
	if err != nil || statusExit != 0 {
		if err == nil {
			err = fmt.Errorf("Git exited with status %d", statusExit)
		}
		return AttemptGitInspection{}, fmt.Errorf("inspect attempt worktree status: %w", err)
	}
	return NewAttemptGitInspection(
		branchExists, branchHead, true, true, worktreeBranch, head, len(statusOutput) == 0,
	)
}

func (adapter LocalAttemptGitAdapter) CreateAttemptWorktree(
	ctx context.Context,
	repositoryRoot, branch, worktree string,
	base GitObjectID,
	createBranch, recoverRegistered bool,
) error {
	if base.IsZero() || !filepath.IsAbs(worktree) {
		return fmt.Errorf("attempt worktree creation requires an exact base and absolute path")
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(worktree)), 0o755); err != nil {
		return fmt.Errorf("create attempt worktree parent: %w", err)
	}
	baseText := strings.TrimPrefix(base.String(), string(base.algorithm)+":")
	arguments := []string{"worktree", "add"}
	if createBranch {
		if recoverRegistered {
			return fmt.Errorf("new attempt branch cannot recover an existing worktree registration")
		}
		arguments = append(arguments, "--no-track", "-b", branch, worktree, baseText)
	} else {
		if recoverRegistered {
			arguments = append(arguments, "--force")
		}
		arguments = append(arguments, worktree, branch)
	}
	_, exitCode, err := adapter.run(ctx, repositoryRoot, arguments...)
	if err != nil {
		return fmt.Errorf("create attempt worktree: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("create attempt worktree: Git exited with status %d", exitCode)
	}
	return nil
}

func (adapter LocalAttemptGitAdapter) PrepareAttemptWorktree(
	ctx context.Context,
	claim AttemptWorktreeClaim,
	recoverUnregistered bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := canonicalAttemptWorktreeClaim(claim)
	if err != nil {
		return err
	}
	marker := attemptWorktreeClaimPath(claim.worktree)
	if err := ensureSynchronizedDirectory(filepath.Dir(marker)); err != nil {
		return fmt.Errorf("prepare attempt worktree claim directory: %w", err)
	}
	created, err := createOrVerifyAttemptWorktreeClaim(
		marker, claim.worktree, payload, adapter.worktreeClaimFault,
	)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(claim.worktree)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect claimed attempt worktree: %w", statErr)
	}
	if created && statErr == nil {
		_ = removeSynchronized(marker)
		return fmt.Errorf("attempt worktree path %s appeared while ownership was being claimed", claim.worktree)
	}
	if statErr != nil {
		return nil
	}
	if !recoverUnregistered {
		return fmt.Errorf("attempt worktree path %s exists but recovery was not requested", claim.worktree)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("claimed attempt worktree path %s is not a recoverable directory", claim.worktree)
	}
	if err := os.RemoveAll(claim.worktree); err != nil {
		return fmt.Errorf("remove partial attempt worktree %s: %w", claim.worktree, err)
	}
	if err := syncDirectory(filepath.Dir(claim.worktree)); err != nil {
		return fmt.Errorf("synchronize recovered attempt worktree parent: %w", err)
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
	payload, err := canonicalAttemptWorktreeClaim(claim)
	if err != nil {
		return err
	}
	marker := attemptWorktreeClaimPath(claim.worktree)
	existing, err := readAttemptWorktreeClaim(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, payload) {
		return fmt.Errorf("attempt worktree claim %s belongs to different immutable bindings", marker)
	}
	if err := removeSynchronized(marker); err != nil {
		return fmt.Errorf("release attempt worktree claim: %w", err)
	}
	return nil
}

func canonicalAttemptWorktreeClaim(claim AttemptWorktreeClaim) ([]byte, error) {
	if claim.attemptID.IsZero() || claim.generation.IsZero() || claim.base.IsZero() ||
		!filepath.IsAbs(claim.worktree) {
		return nil, fmt.Errorf("attempt worktree claim is incomplete")
	}
	if err := validateAttemptBranchSyntax(claim.branch); err != nil {
		return nil, err
	}
	type claimJSON struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		AttemptID     string `json:"attempt_id"`
		Generation    string `json:"generation"`
		Base          string `json:"base"`
		Branch        string `json:"branch"`
		Worktree      string `json:"worktree"`
	}
	return json.Marshal(claimJSON{
		SchemaVersion: JournalSchemaVersion, Kind: "attempt_worktree_claim",
		AttemptID: claim.attemptID.String(), Generation: claim.generation.String(),
		Base: claim.base.String(), Branch: claim.branch, Worktree: claim.worktree,
	})
}

func attemptWorktreeClaimPath(worktree string) string {
	return filepath.Clean(worktree) + ".feature-attempt-claim"
}

func createOrVerifyAttemptWorktreeClaim(
	marker, worktree string,
	payload []byte,
	fault AttemptWorktreeClaimFaultInjector,
) (bool, error) {
	if _, err := os.Lstat(marker); err == nil {
		existing, readErr := readAttemptWorktreeClaim(marker)
		if readErr != nil {
			return false, readErr
		}
		if !bytes.Equal(existing, payload) {
			return false, fmt.Errorf("attempt worktree claim %s belongs to different immutable bindings", marker)
		}
		if err := syncDirectory(filepath.Dir(marker)); err != nil {
			return false, fmt.Errorf("synchronize existing attempt worktree claim: %w", err)
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect attempt worktree claim: %w", err)
	}
	if _, err := os.Lstat(worktree); err == nil {
		return false, fmt.Errorf("attempt worktree path %s predates its ownership claim", worktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect attempt worktree before claim: %w", err)
	}
	directory := filepath.Dir(marker)
	file, err := os.CreateTemp(directory, "pending-attempt-claim-")
	if err != nil {
		return false, fmt.Errorf("create pending attempt worktree claim: %w", err)
	}
	temporary := file.Name()
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = os.Remove(temporary)
		}
	}()
	if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterTemporaryCreated); err != nil {
		return false, err
	}
	if err := file.Chmod(0o600); err != nil {
		return false, fmt.Errorf("set pending attempt worktree claim permissions: %w", err)
	}
	if err := writeAll(file, payload); err != nil {
		return false, fmt.Errorf("write pending attempt worktree claim: %w", err)
	}
	if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterTemporaryWritten); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("synchronize pending attempt worktree claim: %w", err)
	}
	if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterTemporarySynced); err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close pending attempt worktree claim: %w", err)
	}
	if err := os.Link(temporary, marker); errors.Is(err, os.ErrExist) {
		existing, readErr := readAttemptWorktreeClaim(marker)
		if readErr != nil {
			return false, readErr
		}
		if !bytes.Equal(existing, payload) {
			return false, fmt.Errorf("attempt worktree claim %s belongs to different immutable bindings", marker)
		}
		if err := os.Remove(temporary); err != nil {
			return false, fmt.Errorf("remove superseded pending attempt worktree claim: %w", err)
		}
		removeOnFailure = false
		if err := syncDirectory(directory); err != nil {
			return false, fmt.Errorf("synchronize concurrently published attempt worktree claim: %w", err)
		}
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("publish attempt worktree claim without replacement: %w", err)
	}
	if err := injectAttemptWorktreeClaimFault(fault, AttemptWorktreeClaimFaultAfterPublished); err != nil {
		return false, err
	}
	if err := os.Remove(temporary); err != nil {
		return false, fmt.Errorf("remove published pending attempt worktree claim: %w", err)
	}
	removeOnFailure = false
	if err := syncDirectory(directory); err != nil {
		return false, fmt.Errorf("publish attempt worktree claim: %w", err)
	}
	return true, nil
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

func readAttemptWorktreeClaim(marker string) ([]byte, error) {
	info, err := os.Lstat(marker)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("attempt worktree claim %s is not a regular file", marker)
	}
	content, err := readBoundedFile(marker, 16*1024)
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
	argv := append([]string{"-C", repositoryRoot}, arguments...)
	command := exec.CommandContext(ctx, adapter.executable, argv...)
	command.Env = mergeProcessEnvironment(os.Environ(), adapter.environment)
	var stdout, stderr boundedProcessBuffer
	stdout.maximum = maxAttemptGitOutputBytes
	stderr.maximum = 64 * 1024
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
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
	values := make(map[string]string, len(base)+len(additions))
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if found && !unsafeAttemptGitEnvironment(name) {
			values[name] = value
		}
	}
	for _, variable := range additions {
		values[variable.name] = variable.value
	}
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

func unsafeAttemptGitEnvironment(name string) bool {
	switch name {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
		return true
	default:
		return false
	}
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
