package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func mustGitObject(t *testing.T, digit byte) workspace.GitObjectID {
	t.Helper()
	object, err := workspace.ParseGitObjectID("sha1:" + strings.Repeat(string(digit), 40))
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func newProtocolRepository(t *testing.T) (string, string, workspace.GitObjectID) {
	t.Helper()
	repository := t.TempDir()
	branch := "protocol"
	runGitSetup(t, "", "init", "-b", branch, repository)
	runGitSetup(t, repository, "config", "user.name", "Protocol Test")
	runGitSetup(t, repository, "config", "user.email", "protocol@example.test")
	if err := os.MkdirAll(filepath.Join(repository, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "src", "protocol.go"), []byte("package protocol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, repository, "add", "src/protocol.go")
	runGitSetup(t, repository, "commit", "-m", "Initial")
	base := parseGitHead(t, repository)
	runGitSetup(t, repository, "switch", "--detach", rawGitObject(base))
	return repository, branch, base
}

func parseGitHead(t *testing.T, repository string) workspace.GitObjectID {
	t.Helper()
	raw := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "HEAD")))
	algorithm := strings.TrimSpace(string(runGitSetup(t, repository, "rev-parse", "--show-object-format")))
	object, err := workspace.ParseGitObjectID(algorithm + ":" + raw)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func rawGitObject(object workspace.GitObjectID) string {
	return strings.TrimPrefix(object.String(), string(object.Algorithm())+":")
}
