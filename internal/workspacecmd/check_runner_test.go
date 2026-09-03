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
		t.Skipf("local TCP listener is unavailable: %v", err)
	}
	defer listener.Close()
	socketDirectory, err := os.MkdirTemp("", "fi-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	socketPath := filepath.Join(socketDirectory, "ambient.sock")
	providerSocket, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("local Unix listener is unavailable: %v", err)
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
  exit 1
fi
if /bin/cat %s >/dev/null 2>&1; then
  exit 1
fi
if ./network-probe tcp %s >/dev/null 2>&1; then
  exit 1
fi
if ./network-probe unix %s >/dev/null 2>&1; then
  exit 1
fi
exit 0
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
	if err := os.WriteFile(filepath.Join(repository, "change.txt"), []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "change.txt")
	runGitTest(t, repository, "commit", "-m", "Implementation")
	head := parseWorkspaceCommandGitObject(t, strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")))
	adapter := workspace.DefaultLocalCommitGitAdapter()
	inspection, err := adapter.InspectCommit(context.Background(), repository, head)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := workspace.NewArgv("./check.sh")
	if err != nil {
		t.Fatal(err)
	}
	check, err := workspace.NewCommitCheck(
		workspace.MustID("isolated-check"), workspace.MustID("local-sandbox"), argv,
	)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := workspace.NewFinalCommitCheckInvocation(
		check, head, inspection.Tree(), repository,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := defaultIsolatedCheckRunner().RunConfiguredCheck(context.Background(), invocation)
	if err != nil {
		t.Fatalf("RunConfiguredCheck with isolated runner: %v", err)
	}
	if !result.Succeeded() {
		t.Fatalf("isolated check result = %#v", result)
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
	isolate := canonicalWorkspaceCommandTempDir(t)
	if err := validateIsolatedSourceWorktree(isolate, []string{readableRoot}); err != nil {
		t.Fatalf("isolated source was rejected: %v", err)
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
