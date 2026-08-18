package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
	if targetRoot.Path() != binding.root ||
		gitRoot.Path() != binding.gitDirectory ||
		commonRoot.Path() != binding.commonDirectory {
		return nil, fmt.Errorf(
			"local target paths changed after durable feature-ref intent",
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
	if session.target.root.Path() != session.binding.root ||
		session.git.root.Path() != session.binding.gitDirectory ||
		session.common.root.Path() != session.binding.commonDirectory {
		return fmt.Errorf("bound local target Git session path changed")
	}
	if err := validateLocalTargetWorktreeAdministration(
		session.target.root,
		session.binding.gitDirectory,
		session.binding.linkedWorktree,
	); err != nil {
		return err
	}
	if err := validateBoundLocalTargetCommonDirectory(
		session.git.root,
		session.binding,
	); err != nil {
		return err
	}
	if err := validateBoundLocalTargetFeatureRefStorage(
		session.common.root,
		session.binding.featureRef,
	); err != nil {
		return err
	}
	return nil
}

func validateBoundLocalTargetCommonDirectory(
	gitRoot *VerifiedRoot,
	binding LocalTargetBinding,
) error {
	if !binding.linkedWorktree {
		return nil
	}
	content, err := gitRoot.ReadBounded(
		"commondir",
		maxLocalTargetAdministrationBytes,
	)
	if err != nil {
		return fmt.Errorf("read linked-worktree common-directory administration: %w", err)
	}
	if bytes.IndexByte(content, 0) >= 0 ||
		bytes.IndexByte(content, '\r') >= 0 {
		return fmt.Errorf("linked-worktree common-directory administration is malformed")
	}
	value := strings.TrimSuffix(string(content), "\n")
	if value == "" || strings.Contains(value, "\n") {
		return fmt.Errorf("linked-worktree common-directory administration is malformed")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(binding.gitDirectory, value)
	}
	value, err = requireCanonicalObservedTargetPath(
		"Git common administration", value,
	)
	if err != nil {
		return err
	}
	if value != binding.commonDirectory {
		return fmt.Errorf(
			"linked-worktree common-directory administration points to %s, not %s",
			value, binding.commonDirectory,
		)
	}
	return nil
}

func validateBoundLocalTargetFeatureRefStorage(
	commonRoot *VerifiedRoot,
	featureRef string,
) error {
	return validateBoundLocalTargetRefStorage(
		commonRoot,
		featureRef,
		"refs/heads/feature/",
		"feature ref",
		"feature reflog",
	)
}

func validateBoundLocalTargetReleaseMarkerStorage(
	commonRoot *VerifiedRoot,
	markerRef string,
) error {
	return validateBoundLocalTargetRefStorage(
		commonRoot,
		markerRef,
		localTargetReleaseMarkerRefPrefix,
		"feature-ref release marker",
		"feature-ref release marker reflog",
	)
}

func validateBoundLocalTargetRefStorage(
	commonRoot *VerifiedRoot,
	reference string,
	allowedPrefix string,
	refLabel string,
	reflogLabel string,
) error {
	if commonRoot == nil || commonRoot.adapter == nil {
		return fmt.Errorf("bound local target Git common directory is closed")
	}
	if !strings.HasPrefix(reference, allowedPrefix) ||
		path.Clean(reference) != reference {
		return fmt.Errorf("bound local target %s is invalid", refLabel)
	}
	for _, candidate := range []struct {
		path  string
		label string
	}{
		{path: reference, label: refLabel},
		{path: path.Join("logs", reference), label: reflogLabel},
	} {
		if err := validateBoundLocalTargetStorageAncestors(
			commonRoot, candidate.path, candidate.label,
		); err != nil {
			return err
		}
		info, exists, err := commonRoot.adapter.inspectExact(candidate.path)
		if err != nil {
			return fmt.Errorf(
				"inspect bound local target %s storage: %w",
				candidate.label, err,
			)
		}
		if !exists {
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf(
				"bound local target %s storage is not a regular file",
				candidate.label,
			)
		}
		file, _, err := commonRoot.adapter.openRegularFileExact(
			candidate.path, os.O_RDONLY, 0, false,
		)
		if err != nil {
			return fmt.Errorf(
				"open bound local target %s storage: %w",
				candidate.label, err,
			)
		}
		verifyErr := commonRoot.verifyOwnedRegularFile(candidate.path, file)
		closeErr := file.Close()
		if verifyErr != nil || closeErr != nil {
			return fmt.Errorf(
				"verify bound local target %s storage: %w",
				candidate.label, errors.Join(verifyErr, closeErr),
			)
		}
	}
	return commonRoot.VerifyPath()
}

func validateBoundLocalTargetStorageAncestors(
	commonRoot *VerifiedRoot,
	candidate string,
	label string,
) error {
	if err := validateBoundLocalTargetStorageDirectory(
		commonRoot, ".", label,
	); err != nil {
		return err
	}
	ancestor := ""
	for _, component := range strings.Split(path.Dir(candidate), "/") {
		ancestor = path.Join(ancestor, component)
		info, exists, err := commonRoot.adapter.inspectExact(ancestor)
		if err != nil {
			return fmt.Errorf(
				"inspect bound local target %s storage ancestor %s: %w",
				label, ancestor, err,
			)
		}
		if !exists {
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf(
				"bound local target %s has an ancestor namespace conflict at %s",
				label, ancestor,
			)
		}
		if err := validateBoundLocalTargetStorageDirectory(
			commonRoot, ancestor, label,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateBoundLocalTargetStorageDirectory(
	commonRoot *VerifiedRoot,
	relative string,
	label string,
) error {
	directory, err := commonRoot.adapter.openDirectoryExact(relative)
	if err != nil {
		return fmt.Errorf(
			"open bound local target %s storage ancestor %s: %w",
			label, relative, err,
		)
	}
	info, statErr := directory.Stat(".")
	closeErr := directory.Close()
	if statErr != nil || closeErr != nil {
		return fmt.Errorf(
			"inspect bound local target %s storage ancestor %s: %w",
			label, relative, errors.Join(statErr, closeErr),
		)
	}
	identity, err := platformFileIdentity(info)
	if err != nil {
		return fmt.Errorf(
			"identify bound local target %s storage ancestor %s: %w",
			label, relative, err,
		)
	}
	if identity.Owner != commonRoot.identity.Owner {
		return fmt.Errorf(
			"bound local target %s storage ancestor %s owner %d does not match common-root owner %d",
			label, relative, identity.Owner, commonRoot.identity.Owner,
		)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf(
			"bound local target %s storage ancestor %s permissions %04o allow non-owner writes",
			label, relative, info.Mode().Perm(),
		)
	}
	return nil
}

func (session *localTargetGitSession) ensureFeatureRefStorageAncestors() error {
	if err := validateBoundLocalTargetFeatureRefStorage(
		session.common.root,
		session.binding.featureRef,
	); err != nil {
		return err
	}
	if err := session.ensureReferenceStorageAncestors(
		session.binding.featureRef,
		"feature ref",
	); err != nil {
		return err
	}
	return validateBoundLocalTargetFeatureRefStorage(
		session.common.root,
		session.binding.featureRef,
	)
}

func (session *localTargetGitSession) ensureReleaseMarkerRefStorageAncestors() error {
	markerRef := localTargetReleaseMarkerRef(session.binding.featureBranch)
	if err := validateBoundLocalTargetReleaseMarkerStorage(
		session.common.root,
		markerRef,
	); err != nil {
		return err
	}
	if err := session.ensureReferenceStorageAncestors(
		markerRef,
		"feature-ref release marker",
	); err != nil {
		return err
	}
	return validateBoundLocalTargetReleaseMarkerStorage(
		session.common.root,
		markerRef,
	)
}

func (session *localTargetGitSession) ensureReferenceStorageAncestors(
	reference string,
	label string,
) error {
	seen := make(map[string]struct{})
	for _, candidate := range []string{
		reference,
		path.Join("logs", reference),
	} {
		ancestor := ""
		for _, component := range strings.Split(path.Dir(candidate), "/") {
			ancestor = path.Join(ancestor, component)
			if _, ok := seen[ancestor]; ok {
				continue
			}
			seen[ancestor] = struct{}{}
			if _, err := session.common.root.adapter.makeDirectory(
				ancestor, 0o700,
			); err != nil {
				return fmt.Errorf(
					"prepare bound local target %s storage ancestor %s: %w",
					label, ancestor, err,
				)
			}
			if err := validateBoundLocalTargetStorageDirectory(
				session.common.root, ancestor, label,
			); err != nil {
				return err
			}
		}
	}
	return session.common.root.VerifyPath()
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
	command, err := session.command(ctx, arguments...)
	if err != nil {
		return nil, -1, err
	}
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

func (session *localTargetGitSession) command(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, error) {
	if ctx == nil {
		return nil, fmt.Errorf(
			"bound local target Git command requires context",
		)
	}
	argv := trustedGitArguments(session.git.anchor, arguments...)
	command := exec.CommandContext(ctx, session.adapter.git.executable, argv...)
	command.Dir = string(os.PathSeparator)
	environment, err := BuildIsolatedProcessEnvironment(
		os.Environ(), session.adapter.git.environment,
	)
	if err != nil {
		return nil, err
	}
	command.Env = append(environment, "GIT_DIR=.")
	if session.binding.linkedWorktree {
		commonRelative, err := filepath.Rel(
			session.binding.gitDirectory,
			session.binding.commonDirectory,
		)
		if err != nil || commonRelative == "." ||
			filepath.IsAbs(commonRelative) ||
			(!strings.HasPrefix(commonRelative, ".."+string(filepath.Separator)) &&
				commonRelative != "..") {
			if err == nil {
				err = fmt.Errorf("Git common directory is not an ancestor")
			}
			return nil, fmt.Errorf(
				"bind linked-worktree Git common directory: %w", err,
			)
		}
		command.Env = append(
			command.Env,
			"GIT_COMMON_DIR="+commonRelative,
		)
	}
	return command, nil
}

func (session *localTargetGitSession) runPreparedReferenceTransaction(
	ctx context.Context,
	message string,
	commands []byte,
	inspectPrepared func() error,
) (resultErr error) {
	if ctx == nil || message == "" || len(commands) == 0 ||
		inspectPrepared == nil {
		return fmt.Errorf(
			"prepared reference transaction requires context, message, commands, and inspection",
		)
	}
	if err := session.Verify(); err != nil {
		return err
	}
	command, err := session.command(
		ctx,
		"update-ref", "--stdin", "--no-deref",
		"--create-reflog", "-m", message,
	)
	if err != nil {
		return err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	var stderr boundedProcessBuffer
	stderr.maximum = 64 * 1024
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return err
	}

	response := bufio.NewReader(
		io.LimitReader(stdout, 64*1024+1),
	)
	waited := false
	prepared := false
	commitSent := false
	wait := func() error {
		if waited {
			return nil
		}
		waited = true
		waitErr := command.Wait()
		verifyErr := session.Verify()
		if stderr.exceeded {
			return errors.Join(
				fmt.Errorf("Git transaction error output exceeded its bound"),
				verifyErr,
			)
		}
		if waitErr != nil {
			return errors.Join(
				fmt.Errorf(
					"Git reference transaction failed: %w: %s",
					waitErr,
					strings.TrimSpace(string(stderr.bytes())),
				),
				verifyErr,
			)
		}
		return verifyErr
	}
	send := func(action, expected string) error {
		if _, err := io.WriteString(stdin, action+"\n"); err != nil {
			return fmt.Errorf(
				"send Git reference transaction %s: %w",
				action, err,
			)
		}
		line, err := response.ReadString('\n')
		if err != nil {
			return fmt.Errorf(
				"read Git reference transaction %s response: %w",
				action, err,
			)
		}
		if line != expected+"\n" {
			return fmt.Errorf(
				"Git reference transaction %s response is %q, expected %q",
				action, strings.TrimSuffix(line, "\n"), expected,
			)
		}
		return nil
	}
	finish := func(action, expected string) error {
		if action == "commit" {
			commitSent = true
		}
		sendErr := send(action, expected)
		if sendErr == nil {
			prepared = false
		}
		closeErr := stdin.Close()
		return errors.Join(sendErr, closeErr, wait())
	}
	defer func() {
		if waited {
			return
		}
		var cleanupErr error
		if prepared && !commitSent {
			cleanupErr = send("abort", "abort: ok")
			prepared = false
		}
		resultErr = errors.Join(
			resultErr, cleanupErr, stdin.Close(), wait(),
		)
	}()

	if err := send("start", "start: ok"); err != nil {
		return err
	}
	if len(commands) == 0 ||
		commands[len(commands)-1] != '\n' {
		commands = append(
			append([]byte(nil), commands...), '\n',
		)
	}
	if _, err := stdin.Write(commands); err != nil {
		return fmt.Errorf(
			"queue Git reference transaction commands: %w", err,
		)
	}
	if err := send("prepare", "prepare: ok"); err != nil {
		return err
	}
	prepared = true
	if err := inspectPrepared(); err != nil {
		return errors.Join(
			err, finish("abort", "abort: ok"),
		)
	}
	return finish("commit", "commit: ok")
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
		"reflog", "show", "--no-show-signature", "--format=%gs", "-n", "1",
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

func (session *localTargetGitSession) inspectReleaseMarkerRef(
	ctx context.Context,
) (bool, GitObjectID, error) {
	markerRef := localTargetReleaseMarkerRef(session.binding.featureBranch)
	output, exitCode, err := session.run(
		ctx, nil, "show-ref", "--verify", markerRef,
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
	head, err := qualifyGitObjectID(session.binding.objectFormat, fields[0])
	if err != nil {
		return false, GitObjectID{}, err
	}
	return true, head, nil
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
	releaseMarkerExists, _, err := session.inspectReleaseMarkerRef(ctx)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if releaseMarkerExists {
		return LocalTargetInspection{}, releasedFeatureRefAdoptionError(
			session.binding.featureRef,
			localTargetReleaseMarkerRef(session.binding.featureBranch),
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
		session.binding.featureBranch, localRefs, true,
	); err != nil {
		return LocalTargetInspection{}, err
	}
	worktrees, err := session.inspectRegisteredWorktrees(ctx)
	if err != nil {
		return LocalTargetInspection{}, err
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

func (session *localTargetGitSession) inspectRegisteredWorktrees(
	ctx context.Context,
) (map[string]registeredWorktree, error) {
	worktreeOutput, exitCode, err := session.run(
		ctx, nil, "worktree", "list", "--porcelain", "-z",
	)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, fmt.Errorf(
			"inspect target worktrees: Git exited with status %d", exitCode,
		)
	}
	worktrees, err := parseRegisteredWorktrees(worktreeOutput)
	if err != nil {
		return nil, err
	}
	for worktreePath, worktree := range worktrees {
		if worktree.branch == session.binding.featureBranch {
			return nil, fmt.Errorf(
				"feature branch %s is already checked out at %s",
				session.binding.featureBranch, worktreePath,
			)
		}
	}
	return worktrees, nil
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
	releaseMarkerExists, _, err := session.inspectReleaseMarkerRef(ctx)
	if err != nil {
		return LocalTargetInspection{}, err
	}
	if releaseMarkerExists {
		return LocalTargetInspection{}, releasedFeatureRefAdoptionError(
			session.binding.featureRef,
			localTargetReleaseMarkerRef(session.binding.featureBranch),
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
	if err := session.ensureFeatureRefStorageAncestors(); err != nil {
		return LocalTargetInspection{}, err
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

func (session *localTargetGitSession) releaseOwnedFeatureRef(
	ctx context.Context,
	ownedHead GitObjectID,
	expectedMarker string,
	intentDigest Digest,
) error {
	if ctx == nil || ownedHead.IsZero() ||
		ownedHead.Algorithm() != session.binding.objectFormat ||
		expectedMarker == "" || intentDigest.IsZero() {
		return fmt.Errorf(
			"feature ref release requires owned head, marker, and intent digest",
		)
	}
	if err := session.Verify(); err != nil {
		return err
	}
	exists, head, marker, err := session.inspectFeatureRef(ctx)
	if err != nil {
		return err
	}
	if !exists || head != ownedHead || marker != expectedMarker {
		return fmt.Errorf(
			"feature ref changed from its exact owned head and marker before release",
		)
	}
	markerRef := localTargetReleaseMarkerRef(session.binding.featureBranch)
	if err := session.ensureReleaseMarkerRefStorageAncestors(); err != nil {
		return err
	}
	transaction := []byte(fmt.Sprintf(
		"verify %s %s\ncreate %s %s\n",
		session.binding.featureRef,
		gitObjectHex(ownedHead),
		markerRef,
		gitObjectHex(ownedHead),
	))
	output, exitCode, err := session.run(
		ctx,
		transaction,
		"update-ref", "--stdin", "--no-deref", "--create-reflog", "-m",
		localTargetReleaseReflogMessage(intentDigest),
	)
	if err != nil {
		return fmt.Errorf("create feature-ref release marker %s: %w", markerRef, err)
	}
	if exitCode != 0 {
		if !strings.Contains(string(output), "reference already exists") {
			return fmt.Errorf(
				"create feature-ref release marker %s with expected-absent CAS: Git exited with status %d: %s",
				markerRef, exitCode, strings.TrimSpace(string(output)),
			)
		}
		markerExists, markerHead, inspectErr := session.inspectReleaseMarkerRef(ctx)
		if inspectErr != nil {
			return inspectErr
		}
		if !markerExists {
			return fmt.Errorf(
				"release marker ref %s was reported as existing but is absent",
				markerRef,
			)
		}
		if markerHead != ownedHead {
			return fmt.Errorf(
				"release marker ref %s points to %s, expected owned head %s",
				markerRef, markerHead, ownedHead,
			)
		}
	}
	exists, head, marker, err = session.inspectFeatureRef(ctx)
	if err != nil {
		return err
	}
	if !exists || head != ownedHead ||
		marker != expectedMarker {
		return fmt.Errorf(
			"feature ref changed from its exact owned head and marker during release",
		)
	}
	markerExists, markerHead, err := session.inspectReleaseMarkerRef(ctx)
	if err != nil {
		return err
	}
	if !markerExists || markerHead != ownedHead {
		return fmt.Errorf(
			"release marker ref %s does not point to owned head %s",
			markerRef, ownedHead,
		)
	}
	return session.Verify()
}
