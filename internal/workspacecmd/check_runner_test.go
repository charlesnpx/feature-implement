package workspacecmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestConfiguredCheckRunnerUsesExactCloneAndDeniesAmbientRepository(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			t.Skip("sandbox-exec is unavailable")
		}
	case "linux":
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap is unavailable")
		}
	default:
		t.Skip("no supported strict check sandbox")
	}

	repository := canonicalWorkspaceCommandTempDir(t)
	runGitTest(t, repository, "init", "-b", "main")
	runGitTest(t, repository, "config", "user.name", "Feature Test")
	runGitTest(t, repository, "config", "user.email", "feature@example.test")
	ambientConfig := filepath.Join(repository, ".git", "config")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	socketDirectory, err := os.MkdirTemp("", "fi-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	socketPath := filepath.Join(socketDirectory, "provider.sock")
	providerSocket, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer providerSocket.Close()
	hostEnvironment := fmt.Sprintf("/proc/%d/environ", os.Getpid())
	probeSource := filepath.Join(repository, "network_probe.go")
	if err := os.WriteFile(probeSource, []byte(`package main

import (
	"net"
	"os"
	"time"
)

func main() {
	connection, err := net.DialTimeout(os.Args[1], os.Args[2], time.Second)
	if err != nil {
		os.Exit(1)
	}
	_ = connection.Close()
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	probeBinary := filepath.Join(repository, "network-probe")
	build := exec.Command("go", "build", "-o", probeBinary, probeSource)
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build network probe: %v: %s", err, output)
	}
	checkScript := filepath.Join(repository, "check.sh")
	checkContent := fmt.Sprintf(`#!/bin/sh
if /bin/cat %s >/dev/null 2>&1; then
  printf '{"schema_version":1,"status":"failed","assertions":[{"id":"ambient-repository-readable","status":"failed"}]}\n'
  exit 1
fi
if /bin/cat %s >/dev/null 2>&1; then
  printf '{"schema_version":1,"status":"failed","assertions":[{"id":"host-process-environment-readable","status":"failed"}]}\n'
  exit 1
fi
if ./network-probe tcp %s >/dev/null 2>&1; then
  printf '{"schema_version":1,"status":"failed","assertions":[{"id":"network-write-allowed","status":"failed"}]}\n'
  exit 1
fi
if ./network-probe unix %s >/dev/null 2>&1; then
  printf '{"schema_version":1,"status":"failed","assertions":[{"id":"provider-socket-reachable","status":"failed"}]}\n'
  exit 1
fi
printf '{"schema_version":1,"status":"passed","assertions":[]}\n'
`, shellSingleQuote(ambientConfig), shellSingleQuote(hostEnvironment),
		shellSingleQuote(listener.Addr().String()), shellSingleQuote(socketPath))
	if err := os.WriteFile(checkScript, []byte(checkContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md", "check.sh", "network-probe", "network_probe.go")
	runGitTest(t, repository, "commit", "-m", "Base")
	base := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))

	if err := os.WriteFile(filepath.Join(repository, "change.txt"), []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "change.txt")
	message, err := workspace.NewCommitMessagePolicy("Implement isolated check", workspace.CommitBodyForbidden, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := workspace.NewCommitPathPolicy([]string{"change.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := workspace.NewCheckExpectation(workspace.CheckExpectationPass, nil)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := workspace.NewArgv("./check.sh")
	if err != nil {
		t.Fatal(err)
	}
	check, err := workspace.NewCommitCheck(
		workspace.MustID("isolated-check"), workspace.MustID("local-sandbox"),
		workspace.CheckParserAssertionJSON, argv, expectation,
	)
	if err != nil {
		t.Fatal(err)
	}
	step, err := workspace.NewCommitStep(workspace.MustID("implementation"), message, paths, []workspace.CommitCheck{check})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := workspace.NewCommitProtocol([]workspace.CommitStep{step})
	if err != nil {
		t.Fatal(err)
	}
	state, err := workspace.NewCommitProtocolState(workspace.DigestBytes([]byte("generation")), base, protocol)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := workspace.NewCommitProtocolShell(workspace.DefaultLocalCommitGitAdapter(), defaultIsolatedCheckRunner())
	if err != nil {
		t.Fatal(err)
	}
	state, err = shell.ExecuteNextCommitStep(context.Background(), state, "main", repository, "")
	if err != nil {
		t.Fatalf("ExecuteNextCommitStep with isolated runner: %v", err)
	}
	if state.Phase() != workspace.CommitProtocolComplete {
		t.Fatalf("configured protocol phase = %s", state.Phase())
	}
}

func TestLinuxCheckSandboxHasNoHostRootRuntimeOrSourceMount(t *testing.T) {
	scratch := canonicalWorkspaceCommandTempDir(t)
	repository := filepath.Join(scratch, "repository")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := filepath.EvalSymlinks("/usr/bin/true")
	if err != nil {
		t.Skip("standard absolute executable is unavailable")
	}
	source := canonicalWorkspaceCommandTempDir(t)
	arguments, err := linuxCheckSandboxArguments(scratch, repository, source, "", []string{executable})
	if err != nil {
		t.Fatal(err)
	}
	joined := "\x00" + strings.Join(arguments, "\x00") + "\x00"
	for _, required := range []string{
		"\x00--unshare-all\x00", "\x00--tmpfs\x00/\x00",
		"\x00--bind\x00" + scratch + "\x00" + scratch + "\x00",
		"\x00--proc\x00/proc\x00", "\x00--dev\x00/dev\x00",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("linux sandbox arguments omit %q: %#v", required, arguments)
		}
	}
	for _, forbidden := range []string{
		"\x00--ro-bind\x00/\x00/\x00", source, "\x00/run\x00", "\x00/var/run\x00",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("linux sandbox exposes forbidden host path %q: %#v", forbidden, arguments)
		}
	}
	if _, err := linuxCheckSandboxArguments(
		scratch, repository, filepath.Dir(executable), "", []string{executable},
	); err == nil || !strings.Contains(err.Error(), "overlaps readable host root") {
		t.Fatalf("source beneath readable system root was accepted: %v", err)
	}
}

func TestCheckIsolationRejectsCanonicalSourceOverlap(t *testing.T) {
	readableRoot := canonicalWorkspaceCommandTempDir(t)
	source := filepath.Join(readableRoot, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateIsolatedSourceWorktree(source, []string{readableRoot}); err == nil ||
		!strings.Contains(err.Error(), "overlaps readable host root") {
		t.Fatalf("readable source overlap error = %v", err)
	}
	isolated := canonicalWorkspaceCommandTempDir(t)
	if err := validateIsolatedSourceWorktree(isolated, []string{readableRoot}); err != nil {
		t.Fatalf("isolated source was rejected: %v", err)
	}
}

func canonicalWorkspaceCommandTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func runGitTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0="+os.DevNull,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func parseWorkspaceCommandGitObject(t *testing.T, value string) workspace.GitObjectID {
	t.Helper()
	object, err := workspace.ParseGitObjectID("sha1:" + value)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
