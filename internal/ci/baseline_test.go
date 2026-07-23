package ci_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readRepositoryFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func TestExactHeadWorkflowContract(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/exact-head-baseline.yml")

	var parsed map[string]any
	if err := yaml.Unmarshal(workflow, &parsed); err != nil {
		t.Fatalf("parse workflow YAML: %v", err)
	}

	required := []string{
		"permissions:\n  contents: read",
		"ubuntu-24.04",
		"macos-15",
		"persist-credentials: false",
		"EXPECTED_HEAD_SHA: ${{ github.event.pull_request.head.sha || inputs.head_sha }}",
		"ref: ${{ github.event.pull_request.head.sha || inputs.head_sha }}",
		"go-version: \"1.26.0\"",
		"cache: false",
		"./scripts/ci-baseline.sh assert-head",
		"./scripts/ci-baseline.sh normal",
		"./scripts/ci-baseline.sh shuffle",
		"./scripts/ci-baseline.sh race",
		"./scripts/ci-baseline.sh vet",
		"./scripts/ci-baseline.sh build",
		"./scripts/ci-baseline.sh installer",
		"./scripts/ci-baseline.sh diff",
		"./scripts/ci-baseline.sh clean",
	}
	for _, contract := range required {
		if !bytes.Contains(workflow, []byte(contract)) {
			t.Errorf("workflow is missing %q", contract)
		}
	}

	unpinnedAction := regexp.MustCompile(`(?m)^\s*uses:\s+\S+@(v\d+|main|master)\s*$`)
	if unpinnedAction.Match(workflow) {
		t.Fatalf("workflow contains an unpinned action reference: %s", unpinnedAction.Find(workflow))
	}
	if bytes.Contains(workflow, []byte("secrets.")) {
		t.Fatal("workflow must not reference repository or organization secrets")
	}
}

func TestExactHeadGuard(t *testing.T) {
	root := repositoryRoot(t)
	headCommand := exec.Command("git", "rev-parse", "HEAD")
	headCommand.Dir = root
	headOutput, err := headCommand.Output()
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	head := strings.TrimSpace(string(headOutput))

	runGuard := func(expected string) ([]byte, error) {
		t.Helper()
		command := exec.Command(filepath.Join(root, "scripts", "ci-baseline.sh"), "assert-head")
		command.Dir = root
		command.Env = append(os.Environ(), "EXPECTED_HEAD_SHA="+expected)
		return command.CombinedOutput()
	}

	if output, err := runGuard(head); err != nil {
		t.Fatalf("guard rejected exact HEAD: %v\n%s", err, output)
	}

	wrongHead := strings.Repeat("0", len(head))
	output, err := runGuard(wrongHead)
	if err == nil {
		t.Fatal("guard accepted a different requested head")
	}
	if !bytes.Contains(output, []byte("does not match requested head")) {
		t.Fatalf("unexpected mismatch diagnostic: %s", output)
	}
}
