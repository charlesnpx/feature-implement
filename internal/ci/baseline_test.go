package ci_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

	type workflowStep struct {
		Name string         `yaml:"name"`
		Uses string         `yaml:"uses"`
		Run  string         `yaml:"run"`
		With map[string]any `yaml:"with"`
	}
	type workflowJob struct {
		RunsOn         string `yaml:"runs-on"`
		TimeoutMinutes int    `yaml:"timeout-minutes"`
		Strategy       struct {
			FailFast bool `yaml:"fail-fast"`
			Matrix   struct {
				OS []string `yaml:"os"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Env   map[string]string `yaml:"env"`
		Steps []workflowStep    `yaml:"steps"`
	}
	var contract struct {
		Permissions map[string]string      `yaml:"permissions"`
		Jobs        map[string]workflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &contract); err != nil {
		t.Fatalf("parse workflow YAML: %v", err)
	}

	if want := map[string]string{"contents": "read"}; !reflect.DeepEqual(contract.Permissions, want) {
		t.Fatalf("workflow permissions = %#v, want %#v", contract.Permissions, want)
	}
	if len(contract.Jobs) != 1 {
		t.Fatalf("workflow jobs = %d, want exactly one baseline job", len(contract.Jobs))
	}
	job, ok := contract.Jobs["baseline"]
	if !ok {
		t.Fatal("workflow is missing the baseline job")
	}
	if job.RunsOn != "${{ matrix.os }}" {
		t.Fatalf("baseline runs-on = %q", job.RunsOn)
	}
	if job.TimeoutMinutes != 45 {
		t.Fatalf("baseline timeout-minutes = %d, want 45", job.TimeoutMinutes)
	}
	if job.Strategy.FailFast {
		t.Fatal("baseline matrix must retain evidence from both platforms after one fails")
	}
	if want := []string{"ubuntu-24.04", "macos-15"}; !reflect.DeepEqual(job.Strategy.Matrix.OS, want) {
		t.Fatalf("baseline operating systems = %#v, want %#v", job.Strategy.Matrix.OS, want)
	}
	wantEnvironment := map[string]string{
		"EXPECTED_HEAD_SHA": "${{ github.event.pull_request.head.sha || inputs.head_sha }}",
		"GH_TOKEN":          "",
		"GITHUB_TOKEN":      "",
	}
	if !reflect.DeepEqual(job.Env, wantEnvironment) {
		t.Fatalf("baseline environment = %#v, want %#v", job.Env, wantEnvironment)
	}

	if len(job.Steps) != 11 {
		t.Fatalf("baseline steps = %d, want 11", len(job.Steps))
	}
	checkout := job.Steps[0]
	if checkout.Name != "Check out the exact requested head" ||
		checkout.Uses != "actions/checkout@11d5960a326750d5838078e36cf38b85af677262" {
		t.Fatalf("checkout step = %#v", checkout)
	}
	wantCheckoutInputs := map[string]any{
		"fetch-depth":         1,
		"lfs":                 false,
		"persist-credentials": false,
		"ref":                 "${{ github.event.pull_request.head.sha || inputs.head_sha }}",
		"submodules":          false,
	}
	if !reflect.DeepEqual(checkout.With, wantCheckoutInputs) {
		t.Fatalf("checkout inputs = %#v, want %#v", checkout.With, wantCheckoutInputs)
	}

	setupGo := job.Steps[1]
	if setupGo.Name != "Install Go 1.26.0 without dependency caching" ||
		setupGo.Uses != "actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff" {
		t.Fatalf("setup-go step = %#v", setupGo)
	}
	wantSetupInputs := map[string]any{
		"cache":        false,
		"check-latest": false,
		"go-version":   "1.26.0",
	}
	if !reflect.DeepEqual(setupGo.With, wantSetupInputs) {
		t.Fatalf("setup-go inputs = %#v, want %#v", setupGo.With, wantSetupInputs)
	}

	wantCommands := []string{
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
	for index, want := range wantCommands {
		step := job.Steps[index+2]
		if step.Run != want || step.Uses != "" {
			t.Errorf("baseline command step %d = %#v, want run %q", index+1, step, want)
		}
	}

	immutableAction := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	for _, step := range job.Steps {
		if step.Uses != "" && !strings.HasPrefix(step.Uses, "./") && !immutableAction.MatchString(step.Uses) {
			t.Errorf("external action is not pinned to an exact 40-hex commit: %q", step.Uses)
		}
	}
	for _, forbidden := range []string{"secrets.", "github.token"} {
		if bytes.Contains(bytes.ToLower(workflow), []byte(forbidden)) {
			t.Fatalf("workflow must not expose token context %q to executable steps", forbidden)
		}
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
