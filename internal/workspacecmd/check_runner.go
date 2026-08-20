package workspacecmd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

const configuredCheckOutputLimit = 8 * 1024 * 1024

// isolatedCheckRunner materializes the exact recorded commit into a private
// clone and executes its typed argv under an OS network sandbox. Environments
// without a supported sandbox fail closed when a configured check is reached.
type isolatedCheckRunner struct {
	gitExecutable string
}

func defaultIsolatedCheckRunner() isolatedCheckRunner {
	return isolatedCheckRunner{gitExecutable: "git"}
}

func (runner isolatedCheckRunner) RunConfiguredCheck(
	ctx context.Context,
	invocation workspace.CommitCheckInvocation,
) (workspace.CheckProcessResult, error) {
	if ctx == nil {
		return workspace.CheckProcessResult{}, fmt.Errorf("configured check requires context")
	}
	scratch, err := os.MkdirTemp("", "feature-workspace-check-")
	if err != nil {
		return workspace.CheckProcessResult{}, err
	}
	canonicalScratch, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		_ = os.RemoveAll(scratch)
		return workspace.CheckProcessResult{}, err
	}
	scratch = canonicalScratch
	defer os.RemoveAll(scratch)
	repository := filepath.Join(scratch, "repository")
	if err := runner.materializeExactCommit(ctx, invocation, repository); err != nil {
		return workspace.CheckProcessResult{}, err
	}
	argv := invocation.Command().Values()
	resolved, err := resolveCheckExecutable(repository, argv[0])
	if err != nil {
		return workspace.NewCheckProcessResult(
			workspace.CheckMissingExecutable, -1, "", nil, []byte(err.Error()), workspace.StrictCheckIsolationProof(),
		)
	}
	argv[0] = resolved
	environment, moduleCache, err := configuredCheckEnvironment(scratch)
	if err != nil {
		return workspace.CheckProcessResult{}, err
	}
	command, err := sandboxedCheckCommand(ctx, scratch, repository, invocation.Worktree(), moduleCache, argv)
	if err != nil {
		return workspace.CheckProcessResult{}, err
	}
	command.Dir = repository
	command.Env = environment
	var stdout, stderr boundedCheckBuffer
	stdout.maximum, stderr.maximum = configuredCheckOutputLimit, configuredCheckOutputLimit
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return workspace.CheckProcessResult{}, fmt.Errorf("configured check output exceeded %d bytes", configuredCheckOutputLimit)
	}
	termination, exitCode, signal, err := classifyCheckTermination(ctx, runErr)
	if err != nil {
		return workspace.CheckProcessResult{}, err
	}
	return workspace.NewCheckProcessResult(
		termination, exitCode, signal, stdout.bytes(), stderr.bytes(), workspace.StrictCheckIsolationProof(),
	)
}

func (runner isolatedCheckRunner) materializeExactCommit(
	ctx context.Context,
	invocation workspace.CommitCheckInvocation,
	destination string,
) error {
	executable := strings.TrimSpace(runner.gitExecutable)
	if executable == "" {
		executable = "git"
	}
	commands := [][]string{
		{"clone", "--no-checkout", "--no-hardlinks", "--config", "core.hooksPath=" + os.DevNull, "--", invocation.Worktree(), destination},
		// clone installs the source's default branch as a symbolic HEAD even
		// when the attempt source is detached. Preserve the attempt shape
		// before checking out the exact recorded tree.
		{"-C", destination, "update-ref", "--no-deref", "HEAD", gitObjectHex(invocation.Commit())},
		{"-C", destination, "-c", "core.hooksPath=" + os.DevNull, "checkout", "--detach", gitObjectHex(invocation.Commit())},
		{"-C", destination, "remote", "remove", "origin"},
	}
	for _, arguments := range commands {
		if _, err := runIsolatedSetup(ctx, executable, arguments...); err != nil {
			return fmt.Errorf("materialize configured check commit: %w", err)
		}
	}
	output, err := runIsolatedSetup(ctx, executable, "-C", destination, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return fmt.Errorf("verify configured check tree: %w", err)
	}
	observed, err := parseRawGitObject(strings.TrimSpace(string(output)), invocation.Tree().Algorithm())
	if err != nil || observed != invocation.Tree() {
		return fmt.Errorf("configured check materialization tree %q (%v) does not match %s", strings.TrimSpace(string(output)), err, invocation.Tree())
	}
	return nil
}

func parseRawGitObject(value string, algorithm workspace.GitHashAlgorithm) (workspace.GitObjectID, error) {
	return workspace.ParseGitObjectID(string(algorithm) + ":" + strings.TrimSpace(value))
}

func gitObjectHex(object workspace.GitObjectID) string {
	return hex.EncodeToString(object.Bytes())
}

func runIsolatedSetup(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	environment, err := workspace.BuildIsolatedProcessEnvironment(os.Environ(), nil)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = environment
	var stdout, stderr boundedCheckBuffer
	stdout.maximum, stderr.maximum = configuredCheckOutputLimit, configuredCheckOutputLimit
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", executable, strings.Join(arguments, " "), err, strings.TrimSpace(string(stderr.bytes())))
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("setup output exceeded %d bytes", configuredCheckOutputLimit)
	}
	return stdout.bytes(), nil
}

func resolveCheckExecutable(repository, value string) (string, error) {
	if strings.ContainsRune(value, filepath.Separator) {
		candidate := value
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(repository, candidate)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", fmt.Errorf("configured check executable %q is unavailable", value)
		}
		if !pathWithin(repository, resolved) {
			return "", fmt.Errorf("configured check relative executable escapes the isolated repository")
		}
		return resolved, nil
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("configured check executable %q is unavailable", value)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && pathWithin(home, resolved) {
		return "", fmt.Errorf("configured check executable %q is inside the ambient home directory", value)
	}
	return resolved, nil
}

func configuredCheckEnvironment(scratch string) ([]string, string, error) {
	cache := filepath.Join(scratch, "cache")
	temporary := filepath.Join(scratch, "tmp")
	home := filepath.Join(scratch, "home")
	for _, directory := range []string{cache, temporary, home} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, "", err
		}
	}
	moduleCache := ""
	if ambientHome, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(ambientHome, "go", "pkg", "mod")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			moduleCache = candidate
		}
	}
	values := map[string]string{
		"GOCACHE": cache, "GOTOOLCHAIN": "local", "GONOSUMDB": "*", "GOPROXY": "off", "GOSUMDB": "off",
		"TMPDIR": temporary, "TMP": temporary, "TEMP": temporary,
	}
	if moduleCache != "" {
		values["GOMODCACHE"] = moduleCache
		if runtime.GOOS == "linux" {
			values["GOMODCACHE"] = "/feature-go-mod-cache"
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	// Stable ordering keeps the effective process boundary deterministic.
	sortStrings(names)
	additions := make([]workspace.EnvironmentVariable, 0, len(names))
	for _, name := range names {
		variable, err := workspace.NewEnvironmentVariable(name, values[name])
		if err != nil {
			return nil, "", err
		}
		additions = append(additions, variable)
	}
	environment, err := workspace.BuildIsolatedProcessEnvironment(os.Environ(), additions)
	return environment, moduleCache, err
}

func sandboxedCheckCommand(
	ctx context.Context,
	scratch, repository, sourceWorktree, moduleCache string,
	argv []string,
) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		sandbox, err := exec.LookPath("sandbox-exec")
		if err != nil {
			return nil, fmt.Errorf("configured checks require sandbox-exec on darwin")
		}
		readableRoots := darwinCheckReadableRoots(scratch, moduleCache)
		if err := validateIsolatedSourceWorktree(sourceWorktree, readableRoots); err != nil {
			return nil, err
		}
		profile := darwinCheckSandboxProfile(scratch, readableRoots)
		arguments := append([]string{"-p", profile, "--"}, argv...)
		return exec.CommandContext(ctx, sandbox, arguments...), nil
	case "linux":
		bubblewrap, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("configured checks require bubblewrap (bwrap) on linux")
		}
		arguments, err := linuxCheckSandboxArguments(scratch, repository, sourceWorktree, moduleCache, argv)
		if err != nil {
			return nil, err
		}
		return exec.CommandContext(ctx, bubblewrap, arguments...), nil
	default:
		return nil, fmt.Errorf("configured checks have no strict network sandbox on %s", runtime.GOOS)
	}
}

func linuxCheckSandboxArguments(
	scratch, repository, sourceWorktree, moduleCache string,
	argv []string,
) ([]string, error) {
	scratch = filepath.Clean(scratch)
	repository = filepath.Clean(repository)
	if !filepath.IsAbs(scratch) || !filepath.IsAbs(repository) || !pathWithin(scratch, repository) || len(argv) == 0 {
		return nil, fmt.Errorf("linux check sandbox requires absolute scratch, contained repository, and argv")
	}
	systemRoots := existingLinuxCheckSystemRoots()
	readableRoots := append(append([]string(nil), systemRoots...), scratch)
	if moduleCache != "" {
		readableRoots = append(readableRoots, moduleCache)
	}
	if err := validateIsolatedSourceWorktree(sourceWorktree, readableRoots); err != nil {
		return nil, err
	}
	resolvedExecutable := filepath.Clean(argv[0])
	if !filepath.IsAbs(resolvedExecutable) {
		return nil, fmt.Errorf("linux check sandbox requires a resolved absolute executable")
	}
	executableVisible := pathWithin(scratch, resolvedExecutable)
	for _, root := range systemRoots {
		if pathWithin(root, resolvedExecutable) {
			executableVisible = true
			break
		}
	}
	if !executableVisible {
		return nil, fmt.Errorf("configured check executable %q is outside minimal sandbox roots", resolvedExecutable)
	}
	arguments := []string{"--die-with-parent", "--new-session", "--unshare-all", "--tmpfs", "/"}
	for _, root := range systemRoots {
		arguments = append(arguments, "--dir", root, "--ro-bind", root, root)
	}
	arguments = append(arguments, "--dir", "/etc")
	for _, path := range []string{"/etc/group", "/etc/ld.so.cache", "/etc/localtime", "/etc/nsswitch.conf", "/etc/passwd"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			arguments = append(arguments, "--ro-bind", path, path)
		}
	}
	ancestors, err := isolatedMountAncestors(scratch, systemRoots)
	if err != nil {
		return nil, err
	}
	for _, ancestor := range ancestors {
		arguments = append(arguments, "--dir", ancestor)
	}
	arguments = append(arguments,
		"--bind", scratch, scratch,
		"--dir", "/proc", "--proc", "/proc",
		"--dir", "/dev", "--dev", "/dev",
	)
	if moduleCache != "" {
		moduleCache = filepath.Clean(moduleCache)
		if !filepath.IsAbs(moduleCache) {
			return nil, fmt.Errorf("linux check module cache must be absolute")
		}
		arguments = append(arguments,
			"--dir", "/feature-go-mod-cache", "--ro-bind", moduleCache, "/feature-go-mod-cache",
		)
	}
	arguments = append(arguments, "--chdir", repository, "--")
	arguments = append(arguments, argv...)
	return arguments, nil
}

func existingLinuxCheckSystemRoots() []string {
	roots := make([]string, 0, 6)
	for _, root := range []string{"/bin", "/lib", "/lib64", "/nix/store", "/sbin", "/usr"} {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			roots = append(roots, root)
		}
	}
	return roots
}

func isolatedMountAncestors(path string, mountedRoots []string) ([]string, error) {
	parents := make([]string, 0, 8)
	for parent := filepath.Dir(filepath.Clean(path)); parent != string(filepath.Separator); parent = filepath.Dir(parent) {
		for _, mounted := range mountedRoots {
			if pathWithin(mounted, parent) || pathWithin(parent, mounted) {
				return nil, fmt.Errorf("isolated scratch path overlaps read-only system root %s", mounted)
			}
		}
		parents = append(parents, parent)
	}
	for left, right := 0, len(parents)-1; left < right; left, right = left+1, right-1 {
		parents[left], parents[right] = parents[right], parents[left]
	}
	return parents, nil
}

func darwinCheckReadableRoots(scratch, moduleCache string) []string {
	readRoots := []string{"/System", "/usr", "/bin", "/sbin", "/Library", "/opt", "/private/etc", scratch}
	if moduleCache != "" {
		readRoots = append(readRoots, moduleCache)
	}
	return readRoots
}

func validateIsolatedSourceWorktree(sourceWorktree string, readableRoots []string) error {
	sourceWorktree = filepath.Clean(sourceWorktree)
	if !filepath.IsAbs(sourceWorktree) {
		return fmt.Errorf("configured check source worktree must be absolute")
	}
	canonicalSource, err := filepath.EvalSymlinks(sourceWorktree)
	if err != nil {
		return fmt.Errorf("resolve configured check source worktree: %w", err)
	}
	for _, rawRoot := range readableRoots {
		root := filepath.Clean(rawRoot)
		if !filepath.IsAbs(root) {
			return fmt.Errorf("configured check readable root must be absolute")
		}
		if canonical, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
			root = canonical
		}
		if pathWithin(root, canonicalSource) || pathWithin(canonicalSource, root) {
			return fmt.Errorf("configured check source worktree %s overlaps readable host root %s", canonicalSource, root)
		}
	}
	return nil
}

func darwinCheckSandboxProfile(scratch string, readRoots []string) string {
	var profile strings.Builder
	profile.WriteString("(version 1)\n(deny default)\n(import \"system.sb\")\n(deny network*)\n(allow process*)\n(allow sysctl-read)\n")
	profile.WriteString("(allow mach-lookup (global-name \"com.apple.system.opendirectoryd.libinfo\"))\n")
	profile.WriteString("(allow file-read-metadata)\n")
	for _, root := range readRoots {
		profile.WriteString("(allow file-read* (subpath ")
		profile.WriteString(strconv.Quote(root))
		profile.WriteString("))\n")
	}
	profile.WriteString("(allow file-read* (literal \"/dev/null\"))\n")
	profile.WriteString("(allow file-write* (subpath ")
	profile.WriteString(strconv.Quote(scratch))
	profile.WriteString(") (literal \"/dev/null\"))\n")
	return profile.String()
}

func classifyCheckTermination(ctx context.Context, runErr error) (workspace.CheckTerminationKind, int, string, error) {
	if runErr == nil {
		return workspace.CheckExited, 0, "", nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return workspace.CheckTimedOut, -1, "", nil
	}
	if ctx.Err() != nil {
		return "", 0, "", ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return workspace.CheckSignaled, -1, status.Signal().String(), nil
		}
		return workspace.CheckExited, exitError.ExitCode(), "", nil
	}
	return workspace.CheckInfrastructure, -1, "", nil
}

func pathWithin(root, candidate string) bool {
	root, candidate = filepath.Clean(root), filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type boundedCheckBuffer struct {
	content  bytes.Buffer
	maximum  int
	exceeded bool
}

func (buffer *boundedCheckBuffer) Write(value []byte) (int, error) {
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

func (buffer *boundedCheckBuffer) bytes() []byte {
	return append([]byte(nil), buffer.content.Bytes()...)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
