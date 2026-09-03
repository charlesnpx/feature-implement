package workspacecmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

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
