package workspace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const maxAttemptGitOutputBytes = 8 * 1024 * 1024
const maxAttemptGitRecordBytes = 1024 * 1024

type AttemptGitInspection struct {
	worktreeExists  bool
	worktreeHead    GitObjectID
	worktreeTree    GitObjectID
	worktreeBinding AttemptWorktreeGitBinding
	clean           bool
	digest          Digest
}

func newAttemptGitInspection(
	inspection AttemptGitInspection,
) (AttemptGitInspection, error) {
	if inspection.clean && (!inspection.worktreeExists || inspection.worktreeHead.IsZero()) {
		return AttemptGitInspection{}, fmt.Errorf("clean state requires an existing worktree")
	}
	if !inspection.worktreeBinding.IsZero() &&
		(!inspection.worktreeExists || inspection.worktreeHead.IsZero() ||
			inspection.worktreeTree.IsZero()) {
		return AttemptGitInspection{}, fmt.Errorf(
			"bound attempt worktree inspection requires exact worktree Git state",
		)
	}
	if inspection.worktreeBinding.IsZero() !=
		inspection.worktreeTree.IsZero() {
		return AttemptGitInspection{}, fmt.Errorf(
			"attempt worktree tree and exact binding must be recorded together",
		)
	}
	if !inspection.worktreeBinding.IsZero() {
		if err := inspection.worktreeBinding.validate(); err != nil {
			return AttemptGitInspection{}, err
		}
	}
	digest, err := digestAttemptGitInspection(inspection)
	if err != nil {
		return AttemptGitInspection{}, err
	}
	inspection.digest = digest
	return inspection, nil
}

func (inspection AttemptGitInspection) WorktreeExists() bool      { return inspection.worktreeExists }
func (inspection AttemptGitInspection) WorktreeHead() GitObjectID { return inspection.worktreeHead }
func (inspection AttemptGitInspection) WorktreeTree() GitObjectID { return inspection.worktreeTree }
func (inspection AttemptGitInspection) WorktreeGitBinding() AttemptWorktreeGitBinding {
	return inspection.worktreeBinding
}
func (inspection AttemptGitInspection) Clean() bool    { return inspection.clean }
func (inspection AttemptGitInspection) Digest() Digest { return inspection.digest }

// NewScratchAttemptGitInspection records an independent, detached attempt
// repository.
func NewScratchAttemptGitInspection(
	worktreeHead, worktreeTree GitObjectID,
	worktreeBinding AttemptWorktreeGitBinding,
	clean bool,
) (AttemptGitInspection, error) {
	return newAttemptGitInspection(AttemptGitInspection{
		worktreeExists: true, worktreeHead: worktreeHead,
		worktreeTree: worktreeTree, worktreeBinding: worktreeBinding,
		clean: clean,
	})
}

type AttemptGitPort interface {
	ValidateAttemptWorktreeRoot(context.Context, string, string) error
	InspectAttemptWorktree(context.Context, string, string) (AttemptGitInspection, error)
	MaterializeAttemptTree(context.Context, string, GitObjectID, string) (AttemptGitInspection, error)
}

type AttemptWorktreeMaterializationFaultPoint string

const (
	AttemptMaterializationFaultAfterDirectoryBinding AttemptWorktreeMaterializationFaultPoint = "after_directory_binding"
	AttemptMaterializationFaultAfterPath             AttemptWorktreeMaterializationFaultPoint = "after_path"
)

type AttemptWorktreeMaterializationFaultInjector func(
	AttemptWorktreeMaterializationFaultPoint,
) error

type LocalAttemptGitAdapter struct {
	executable               string
	environment              []EnvironmentVariable
	worktreeMaterializeFault AttemptWorktreeMaterializationFaultInjector
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

func (adapter LocalAttemptGitAdapter) WithAttemptWorktreeMaterializationFaultInjector(
	injector AttemptWorktreeMaterializationFaultInjector,
) LocalAttemptGitAdapter {
	adapter.worktreeMaterializeFault = injector
	return adapter
}

func (adapter LocalAttemptGitAdapter) InspectAttemptWorktree(
	ctx context.Context,
	repositoryRoot, worktree string,
) (AttemptGitInspection, error) {
	commit := LocalCommitGitAdapter{git: adapter}
	source, err := commit.captureTrustedWorktreeBinding(ctx, repositoryRoot)
	if err != nil {
		return AttemptGitInspection{}, fmt.Errorf("inspect target Git binding: %w", err)
	}
	return adapter.inspectScratchAttemptWorktree(ctx, source, worktree)
}

func (adapter LocalAttemptGitAdapter) materializeAttemptBlob(
	ctx context.Context,
	sourceRoot string,
	root *RootedFilesystemAdapter,
	entry rawGitTreeEntry,
	algorithm GitHashAlgorithm,
	permission os.FileMode,
) error {
	size, err := adapter.gitBlobSize(ctx, sourceRoot, entry.object)
	if err != nil {
		return err
	}
	digest, err := newGitBlobHasher(algorithm, size)
	if err != nil {
		return err
	}
	return root.writeFileExclusiveWith(
		entry.path, permission,
		func(file *os.File) error {
			writer := exactGitBlobWriter{
				file: file, digest: digest,
				expected: size, remaining: size,
			}
			if err := adapter.streamGitBlob(
				ctx, sourceRoot, entry.object, &writer,
			); err != nil {
				return fmt.Errorf("read Git blob %s: %w", entry.object, err)
			}
			if writer.remaining != 0 {
				return fmt.Errorf(
					"read Git blob %s produced %d bytes, expected %d",
					entry.object, writer.expected-writer.remaining, writer.expected,
				)
			}
			object, err := gitObjectIDFromHash(algorithm, digest)
			if err != nil {
				return err
			}
			if object != entry.object {
				return fmt.Errorf(
					"attempt tree path did not resolve to its recorded blob",
				)
			}
			return nil
		},
	)
}

func (adapter LocalAttemptGitAdapter) gitBlobSize(
	ctx context.Context,
	sourceRoot string,
	object GitObjectID,
) (int64, error) {
	output, exitCode, err := adapter.run(
		ctx, sourceRoot, "cat-file", "-s", gitObjectHex(object),
	)
	if err != nil {
		return 0, fmt.Errorf("read Git blob %s size: %w", object, err)
	}
	if exitCode != 0 {
		return 0, fmt.Errorf(
			"read Git blob %s size: Git exited with status %d",
			object, exitCode,
		)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("read Git blob %s returned an invalid size", object)
	}
	return size, nil
}

type exactGitBlobWriter struct {
	file      *os.File
	digest    hash.Hash
	expected  int64
	remaining int64
}

func (writer *exactGitBlobWriter) Write(content []byte) (int, error) {
	if writer == nil || writer.file == nil || writer.digest == nil ||
		writer.remaining < 0 {
		return 0, fmt.Errorf("Git blob writer is not initialized")
	}
	if int64(len(content)) > writer.remaining {
		return 0, fmt.Errorf(
			"Git blob output exceeds its declared size of %d bytes",
			writer.expected,
		)
	}
	if err := writeAll(writer.file, content); err != nil {
		return 0, err
	}
	if err := writeAll(writer.digest, content); err != nil {
		return 0, err
	}
	writer.remaining -= int64(len(content))
	return len(content), nil
}

func validateAttemptTreeEntries(entries []rawGitTreeEntry) ([]string, error) {
	spellings := make(map[string]string, len(entries)*2)
	directories := make(map[string]struct{})
	for _, entry := range entries {
		switch entry.mode {
		case GitModeRegular, GitModeExecutable, GitModeSymlink:
		case GitModeSubmodule:
			if entry.kind != "commit" {
				return nil, fmt.Errorf(
					"attempt tree path %s has inconsistent gitlink type %s",
					entry.path, entry.kind,
				)
			}
		default:
			return nil, fmt.Errorf(
				"attempt tree path %s has unsupported mode %s",
				entry.path, entry.mode,
			)
		}
		components := strings.Split(entry.path, "/")
		for _, component := range components {
			if materializationCollisionKey(component) == materializationCollisionKey(".git") {
				return nil, fmt.Errorf(
					"attempt tree path %s collides with Git administration",
					entry.path,
				)
			}
		}
		if isRepositoryAttributesPath(entry.path) {
			return nil, fmt.Errorf(
				"attempt tree path %s contains repository-defined .gitattributes; exact raw materialization does not support Git attribute transformations",
				entry.path,
			)
		}
		paths := []string{entry.path}
		for directory := pathpkg.Dir(entry.path); directory != "."; directory = pathpkg.Dir(directory) {
			paths = append(paths, directory)
			directories[directory] = struct{}{}
		}
		for _, candidate := range paths {
			key := materializationCollisionKey(candidate)
			if prior, exists := spellings[key]; exists && prior != candidate {
				return nil, fmt.Errorf(
					"attempt tree paths %q and %q collide",
					prior, candidate,
				)
			}
			spellings[key] = candidate
		}
	}
	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	sort.Slice(result, func(left, right int) bool {
		leftDepth := strings.Count(result[left], "/")
		rightDepth := strings.Count(result[right], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return result[left] < result[right]
	})
	return result, nil
}

func isRepositoryAttributesPath(relative string) bool {
	for _, component := range strings.Split(relative, "/") {
		if materializationCollisionKey(component) ==
			materializationCollisionKey(".gitattributes") {
			return true
		}
	}
	return false
}

func injectAttemptWorktreeMaterializationFault(
	injector AttemptWorktreeMaterializationFaultInjector,
	point AttemptWorktreeMaterializationFaultPoint,
) error {
	if injector == nil {
		return nil
	}
	return injector(point)
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
	environment, err := BuildIsolatedProcessEnvironment(os.Environ(), adapter.environment)
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
			output := stdout.bytes()
			if len(output) == 0 {
				output = stderr.bytes()
			}
			return output, exitCode, nil
		}
		return nil, -1, err
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, -1, fmt.Errorf("Git output exceeded its bound")
	}
	return stdout.bytes(), exitCode, nil
}

func (adapter LocalAttemptGitAdapter) streamNULTerminatedRecords(
	ctx context.Context,
	repositoryRoot string,
	consume func([]byte) error,
	arguments ...string,
) (int, error) {
	repositoryRoot = filepath.Clean(strings.TrimSpace(repositoryRoot))
	if !filepath.IsAbs(repositoryRoot) {
		return -1, fmt.Errorf("Git repository root must be absolute")
	}
	if consume == nil {
		return -1, fmt.Errorf("Git record consumer is required")
	}
	command := exec.CommandContext(
		ctx, adapter.executable,
		trustedGitArguments(repositoryRoot, arguments...)...,
	)
	environment, err := BuildIsolatedProcessEnvironment(
		os.Environ(), adapter.environment,
	)
	if err != nil {
		return -1, err
	}
	command.Env = environment
	stdout, err := command.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("open Git record stream: %w", err)
	}
	var stderr boundedProcessBuffer
	stderr.maximum = 64 * 1024
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return -1, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxAttemptGitRecordBytes)
	scanner.Split(splitNULTerminatedGitRecord)
	var consumeErr error
	for scanner.Scan() {
		if consumeErr = consume(scanner.Bytes()); consumeErr != nil {
			break
		}
	}
	if consumeErr == nil {
		consumeErr = scanner.Err()
	}
	if consumeErr != nil {
		_ = stdout.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}
	waitErr := command.Wait()
	if stderr.exceeded {
		return -1, fmt.Errorf("Git output exceeded its bound")
	}
	if consumeErr != nil {
		return -1, consumeErr
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			return exitError.ExitCode(), nil
		}
		return -1, waitErr
	}
	return 0, nil
}

func splitNULTerminatedGitRecord(
	data []byte,
	atEOF bool,
) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, 0); index >= 0 {
		return index + 1, data[:index], nil
	}
	if atEOF && len(data) != 0 {
		return 0, nil, fmt.Errorf("Git record stream is not NUL terminated")
	}
	return 0, nil, nil
}

func (adapter LocalAttemptGitAdapter) streamGitBlob(
	ctx context.Context,
	repositoryRoot string,
	object GitObjectID,
	destination *exactGitBlobWriter,
) error {
	repositoryRoot = filepath.Clean(strings.TrimSpace(repositoryRoot))
	if !filepath.IsAbs(repositoryRoot) {
		return fmt.Errorf("Git repository root must be absolute")
	}
	if destination == nil {
		return fmt.Errorf("Git blob destination is required")
	}
	command := exec.CommandContext(
		ctx, adapter.executable,
		trustedGitArguments(
			repositoryRoot, "cat-file", "blob", gitObjectHex(object),
		)...,
	)
	environment, err := BuildIsolatedProcessEnvironment(
		os.Environ(), adapter.environment,
	)
	if err != nil {
		return err
	}
	command.Env = environment
	var stderr boundedProcessBuffer
	stderr.maximum = 64 * 1024
	command.Stdout = destination
	command.Stderr = &stderr
	err = command.Run()
	if stderr.exceeded {
		return fmt.Errorf("Git output exceeded its bound")
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return fmt.Errorf(
				"Git exited with status %d", exitError.ExitCode(),
			)
		}
		return err
	}
	return nil
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
		if found && allowedAmbientProcessEnvironment(name) {
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

func allowedAmbientProcessEnvironment(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "PATH", "TMPDIR", "TMP", "TEMP", "TZ", "LANG", "LC_ALL", "LC_CTYPE",
		"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT":
		return true
	default:
		return false
	}
}

// BuildIsolatedProcessEnvironment is the shared environment boundary for
// local Git, checks, implementation processes, and reviewers. It removes
// all ambient state except a small operational allowlist, then forces
// authentication helpers, prompts, SSH identities, and system/global Git
// configuration off. Network sandboxing remains a responsibility of the
// typed runner port.
func BuildIsolatedProcessEnvironment(
	base []string,
	additions []EnvironmentVariable,
) ([]string, error) {
	for _, variable := range additions {
		if variable.name == "" || unsafeAttemptGitEnvironment(variable.name) {
			return nil, fmt.Errorf("isolated environment variable %s is unsafe", variable.name)
		}
	}
	return mergeProcessEnvironment(base, additions), nil
}

func unsafeAttemptGitEnvironment(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if sensitiveEnvironmentVariable(upper) || upper == "GIT_CONFIG" || strings.HasPrefix(upper, "GIT_CONFIG_") {
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

func sensitiveEnvironmentVariable(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch upper {
	case "TOKEN", "PAT", "PASSWORD", "PASS", "PASSPHRASE", "SECRET", "API_KEY", "ACCESS_KEY",
		"PRIVATE_KEY", "CREDENTIAL", "CREDENTIALS", "AUTHORIZATION", "AUTH_HEADER",
		"SYSTEM_ACCESSTOKEN",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE",
		"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_API_KEY", "GCLOUD_ACCESS_TOKEN",
		"CLOUDSDK_CONFIG", "AZURE_CLIENT_SECRET", "AZURE_CONFIG_DIR", "ARM_CLIENT_SECRET",
		"DOCKER_CONFIG", "KUBECONFIG", "NETRC":
		return true
	}
	for _, suffix := range []string{
		"_TOKEN", "_PAT", "_PASSWORD", "_PASS", "_PASSPHRASE", "_SECRET",
		"_API_KEY", "_ACCESS_KEY", "_PRIVATE_KEY", "_CLIENT_SECRET", "_CREDENTIAL", "_CREDENTIALS",
		"_CONFIG_DIR",
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
		"-c", "core.sparseCheckout=false",
		"-c", "core.sparseCheckoutCone=false",
		"-c", "index.sparse=false",
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
		SchemaVersion   int                            `json:"schema_version"`
		WorktreeExists  bool                           `json:"worktree_exists"`
		WorktreeHead    string                         `json:"worktree_head,omitempty"`
		WorktreeTree    string                         `json:"worktree_tree,omitempty"`
		WorktreeBinding *attemptWorktreeGitBindingWire `json:"worktree_binding,omitempty"`
		Clean           bool                           `json:"clean"`
	}
	var worktreeBinding *attemptWorktreeGitBindingWire
	if !inspection.worktreeBinding.IsZero() {
		wire := attemptWorktreeGitBindingToWire(
			inspection.worktreeBinding,
		)
		worktreeBinding = &wire
	}
	content, err := json.Marshal(inspectionJSON{
		SchemaVersion:   JournalSchemaVersion,
		WorktreeExists:  inspection.worktreeExists,
		WorktreeHead:    inspection.worktreeHead.String(),
		WorktreeTree:    inspection.worktreeTree.String(),
		WorktreeBinding: worktreeBinding, Clean: inspection.clean,
	})
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(content), nil
}
