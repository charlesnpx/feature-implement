package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type retainedLocalTargetRoot struct {
	root   *VerifiedRoot
	handle *os.File
	anchor string
}

func retainLocalTargetRoot(root *VerifiedRoot) (*retainedLocalTargetRoot, error) {
	if root == nil || root.adapter == nil {
		return nil, fmt.Errorf("verified local target root is required")
	}
	handle, err := root.adapter.root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("retain %s root handle: %w", root.Role(), err)
	}
	info, err := handle.Stat()
	if err != nil || !os.SameFile(root.info, info) {
		_ = handle.Close()
		if err == nil {
			err = fmt.Errorf("directory identity changed")
		}
		return nil, fmt.Errorf("verify retained %s root: %w", root.Role(), err)
	}
	anchor, err := localTargetGitCommandAnchor(handle, root.Identity())
	if err != nil {
		_ = handle.Close()
		return nil, err
	}
	return &retainedLocalTargetRoot{
		root: root, handle: handle, anchor: anchor,
	}, nil
}

func (retained *retainedLocalTargetRoot) Verify() error {
	if retained == nil || retained.root == nil || retained.handle == nil {
		return fmt.Errorf("retained local target root is closed")
	}
	if err := retained.root.VerifyPath(); err != nil {
		return err
	}
	info, err := retained.handle.Stat()
	if err != nil {
		return fmt.Errorf("inspect retained %s root: %w", retained.root.Role(), err)
	}
	if !os.SameFile(retained.root.info, info) {
		return fmt.Errorf("retained %s root identity changed", retained.root.Role())
	}
	return nil
}

func (retained *retainedLocalTargetRoot) Close() error {
	if retained == nil || retained.handle == nil {
		return nil
	}
	err := retained.handle.Close()
	retained.handle = nil
	return err
}

type localTargetGitSession struct {
	adapter LocalTargetGitAdapter
	binding LocalTargetBinding
	target  *retainedLocalTargetRoot
	git     *retainedLocalTargetRoot
	common  *retainedLocalTargetRoot
}

func (adapter LocalTargetGitAdapter) openBoundSession(
	binding LocalTargetBinding,
) (session *localTargetGitSession, resultErr error) {
	if binding.IsZero() {
		return nil, fmt.Errorf("bound local target Git session requires a target binding")
	}
	targetRoot, err := OpenVerifiedRoot(RootRoleTarget, binding.root, false)
	if err != nil {
		return nil, fmt.Errorf("open bound local target root: %w", err)
	}
	gitRoot, err := OpenVerifiedRoot(
		RootRoleGitDirectory, binding.gitDirectory, false,
	)
	if err != nil {
		_ = targetRoot.Close()
		return nil, fmt.Errorf("open bound local target Git directory: %w", err)
	}
	commonRoot, err := OpenVerifiedRoot(
		RootRoleGitCommon, binding.commonDirectory, false,
	)
	if err != nil {
		_ = gitRoot.Close()
		_ = targetRoot.Close()
		return nil, fmt.Errorf("open bound local target Git common directory: %w", err)
	}
	closeRoots := true
	defer func() {
		if closeRoots {
			resultErr = errors.Join(
				resultErr,
				commonRoot.Close(),
				gitRoot.Close(),
				targetRoot.Close(),
			)
		}
	}()
	if targetRoot.Identity() != binding.rootIdentity ||
		gitRoot.Identity() != binding.gitDirectoryIdentity ||
		commonRoot.Identity() != binding.commonIdentity {
		return nil, fmt.Errorf(
			"local target filesystem identity changed after durable feature-ref intent",
		)
	}
	target, err := retainLocalTargetRoot(targetRoot)
	if err != nil {
		return nil, err
	}
	git, err := retainLocalTargetRoot(gitRoot)
	if err != nil {
		_ = target.Close()
		return nil, err
	}
	common, err := retainLocalTargetRoot(commonRoot)
	if err != nil {
		_ = git.Close()
		_ = target.Close()
		return nil, err
	}
	session = &localTargetGitSession{
		adapter: adapter, binding: binding,
		target: target, git: git, common: common,
	}
	if err := session.Verify(); err != nil {
		_ = session.Close()
		return nil, err
	}
	closeRoots = false
	return session, nil
}

func (session *localTargetGitSession) Verify() error {
	if session == nil || session.binding.IsZero() ||
		session.target == nil || session.git == nil || session.common == nil {
		return fmt.Errorf("bound local target Git session is closed")
	}
	for _, retained := range []*retainedLocalTargetRoot{
		session.target, session.git, session.common,
	} {
		if err := retained.Verify(); err != nil {
			return err
		}
	}
	if session.target.root.Identity() != session.binding.rootIdentity ||
		session.git.root.Identity() != session.binding.gitDirectoryIdentity ||
		session.common.root.Identity() != session.binding.commonIdentity {
		return fmt.Errorf("bound local target Git session identity changed")
	}
	if err := validateLocalTargetWorktreeAdministration(
		session.target.root,
		session.binding.gitDirectory,
		session.binding.linkedWorktree,
	); err != nil {
		return err
	}
	return nil
}

func (session *localTargetGitSession) Close() error {
	if session == nil {
		return nil
	}
	var closeErrors []error
	for _, retained := range []*retainedLocalTargetRoot{
		session.common, session.git, session.target,
	} {
		if retained != nil {
			closeErrors = append(closeErrors, retained.Close())
		}
	}
	for _, retained := range []*retainedLocalTargetRoot{
		session.common, session.git, session.target,
	} {
		if retained != nil && retained.root != nil {
			closeErrors = append(closeErrors, retained.root.Close())
			retained.root = nil
		}
	}
	session.common, session.git, session.target = nil, nil, nil
	return errors.Join(closeErrors...)
}

func (session *localTargetGitSession) run(
	ctx context.Context,
	input []byte,
	arguments ...string,
) ([]byte, int, error) {
	if ctx == nil {
		return nil, -1, fmt.Errorf("bound local target Git command requires context")
	}
	if err := session.Verify(); err != nil {
		return nil, -1, err
	}
	argv := trustedGitArguments(session.git.anchor, arguments...)
	command := exec.CommandContext(ctx, session.adapter.git.executable, argv...)
	command.Dir = string(os.PathSeparator)
	environment, err := BuildNonProviderProcessEnvironment(
		os.Environ(), session.adapter.git.environment,
	)
	if err != nil {
		return nil, -1, err
	}
	command.Env = append(
		environment,
		"GIT_DIR=.",
	)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr boundedProcessBuffer
	stdout.maximum = maxAttemptGitOutputBytes
	stderr.maximum = 64 * 1024
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	verifyErr := session.Verify()
	exitCode := 0
	if runErr != nil {
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			return nil, -1, errors.Join(runErr, verifyErr)
		}
	}
	if verifyErr != nil {
		return nil, -1, verifyErr
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, -1, fmt.Errorf("Git output exceeded its bound")
	}
	output := stdout.bytes()
	if exitCode != 0 && len(output) == 0 {
		output = stderr.bytes()
	}
	return output, exitCode, nil
}

func (session *localTargetGitSession) resolveBase(
	ctx context.Context,
) (GitObjectID, error) {
	output, exitCode, err := session.run(
		ctx, nil, "show-ref", "--verify", session.binding.baseRef,
	)
	if err != nil {
		return GitObjectID{}, err
	}
	if exitCode != 0 {
		return GitObjectID{}, fmt.Errorf(
			"base_ref %s does not resolve to a local ref (Git status %d: %s)",
			session.binding.baseRef,
			exitCode,
			strings.TrimSpace(string(output)),
		)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != session.binding.baseRef {
		return GitObjectID{}, fmt.Errorf("Git returned malformed base_ref data")
	}
	object, err := qualifyGitObjectID(session.binding.objectFormat, fields[0])
	if err != nil {
		return GitObjectID{}, fmt.Errorf("parse base_ref object: %w", err)
	}
	output, exitCode, err = session.run(
		ctx, nil, "cat-file", "-t", gitObjectHex(object),
	)
	if err != nil {
		return GitObjectID{}, err
	}
	if exitCode != 0 || strings.TrimSpace(string(output)) != "commit" {
		return GitObjectID{}, fmt.Errorf("Git object %s is not an available commit", object)
	}
	return object, nil
}

func (session *localTargetGitSession) inspectFeatureRef(
	ctx context.Context,
) (bool, GitObjectID, string, error) {
	output, exitCode, err := session.run(
		ctx, nil, "show-ref", "--verify", session.binding.featureRef,
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
			session.binding.featureRef, exitCode,
		)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != session.binding.featureRef {
		return false, GitObjectID{}, "", fmt.Errorf(
			"Git returned malformed feature-ref data",
		)
	}
	head, err := qualifyGitObjectID(session.binding.objectFormat, fields[0])
	if err != nil {
		return false, GitObjectID{}, "", err
	}
	reflog, reflogExit, err := session.run(
		ctx, nil,
		"reflog", "show", "--format=%gs", "-n", "1",
		session.binding.featureRef, "--",
	)
	if err != nil {
		return false, GitObjectID{}, "", err
	}
	marker := ""
	if reflogExit == 0 {
		marker = strings.TrimSpace(string(reflog))
	} else if reflogExit != 1 {
		return false, GitObjectID{}, "", fmt.Errorf(
			"inspect feature ref reflog: Git exited with status %d: %s",
			reflogExit, strings.TrimSpace(string(reflog)),
		)
	}
	return true, head, marker, nil
}

func (session *localTargetGitSession) inspectOwnedState(
	ctx context.Context,
	intentDigest Digest,
	requireBaseAtPin bool,
) (LocalTargetInspection, error) {
	if intentDigest.IsZero() {
		return LocalTargetInspection{}, fmt.Errorf(
			"bound local target inspection requires a durable intent digest",
		)
	}
	baseHead, err := session.resolveBase(ctx)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if requireBaseAtPin && baseHead != session.binding.baseCommit {
		return LocalTargetInspection{}, fmt.Errorf(
			"base_ref %s resolves to %s, not pinned base_commit %s",
			session.binding.baseRef, baseHead, session.binding.baseCommit,
		)
	}
	exists, head, marker, err := session.inspectFeatureRef(ctx)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if exists {
		if head != session.binding.baseCommit {
			return LocalTargetInspection{}, fmt.Errorf(
				"feature ref %s is %s, expected pinned base %s",
				session.binding.featureRef, head, session.binding.baseCommit,
			)
		}
		if marker != localTargetReflogMessage(intentDigest) {
			return LocalTargetInspection{}, fmt.Errorf(
				"feature ref %s has no exact workspace creation marker; refusing to adopt it",
				session.binding.featureRef,
			)
		}
	}
	refs, exitCode, err := session.run(
		ctx, nil, "for-each-ref", "--format=%(refname)", "refs/heads",
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if exitCode != 0 {
		return LocalTargetInspection{}, fmt.Errorf(
			"inspect local feature-ref namespace: Git exited with status %d",
			exitCode,
		)
	}
	localRefs, err := parseLocalHeadRefs(refs)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if err := CheckAttemptRefConflicts(
		session.binding.featureBranch, localRefs, nil, true,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	worktreeOutput, exitCode, err := session.run(
		ctx, nil, "worktree", "list", "--porcelain", "-z",
	)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if exitCode != 0 {
		return LocalTargetInspection{}, fmt.Errorf(
			"inspect target worktrees: Git exited with status %d", exitCode,
		)
	}
	worktrees, err := parseRegisteredWorktrees(worktreeOutput)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	for worktreePath, worktree := range worktrees {
		if worktree.branch == session.binding.featureBranch {
			return LocalTargetInspection{}, fmt.Errorf(
				"feature branch %s is already checked out at %s",
				session.binding.featureBranch, worktreePath,
			)
		}
	}
	if err := session.Verify(); err != nil {
		return LocalTargetInspection{}, err
	}
	return LocalTargetInspection{
		binding: session.binding, baseHead: baseHead,
		featureRefExists: exists, featureHead: head,
		featureReflogMarker: marker,
		registeredWorktrees: worktrees,
	}, nil
}

func (session *localTargetGitSession) createFeatureRef(
	ctx context.Context,
	intentDigest Digest,
) (LocalTargetInspection, error) {
	baseHead, err := session.resolveBase(ctx)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if baseHead != session.binding.baseCommit {
		return LocalTargetInspection{}, fmt.Errorf(
			"base_ref %s resolves to %s, not pinned base_commit %s",
			session.binding.baseRef, baseHead, session.binding.baseCommit,
		)
	}
	exists, _, _, err := session.inspectFeatureRef(ctx)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if exists {
		return LocalTargetInspection{}, fmt.Errorf(
			"create feature ref %s with expected-absent CAS: ref already exists",
			session.binding.featureRef,
		)
	}
	message := localTargetReflogMessage(intentDigest)
	transaction := []byte(fmt.Sprintf(
		"verify %s %s\ncreate %s %s\n",
		session.binding.baseRef,
		gitObjectHex(session.binding.baseCommit),
		session.binding.featureRef,
		gitObjectHex(session.binding.baseCommit),
	))
	_, exitCode, err := session.run(
		ctx,
		transaction,
		"update-ref", "--stdin", "--no-deref", "--create-reflog", "-m", message,
	)
	if err != nil {
		return LocalTargetInspection{}, fmt.Errorf(
			"create feature ref %s: %w", session.binding.featureRef, err,
		)
	}
	if exitCode != 0 {
		return LocalTargetInspection{}, fmt.Errorf(
			"create feature ref %s with pinned-base and expected-absent CAS: Git exited with status %d",
			session.binding.featureRef, exitCode,
		)
	}
	return session.inspectOwnedState(ctx, intentDigest, true)
}
