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

type workflowStep struct {
	Name string         `yaml:"name"`
	ID   string         `yaml:"id"`
	If   string         `yaml:"if"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
}

type workflowMatrixEntry struct {
	Name     string `yaml:"name"`
	OS       string `yaml:"os"`
	Parallel int    `yaml:"parallel"`
	Seed     string `yaml:"seed"`
	Suite    string `yaml:"suite"`
}

type workflowJob struct {
	If             string `yaml:"if"`
	RunsOn         string `yaml:"runs-on"`
	TimeoutMinutes int    `yaml:"timeout-minutes"`
	Strategy       struct {
		FailFast bool `yaml:"fail-fast"`
		Matrix   struct {
			OS      []string              `yaml:"os"`
			Include []workflowMatrixEntry `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Env   map[string]string `yaml:"env"`
	Steps []workflowStep    `yaml:"steps"`
}

type workflowContract struct {
	On          map[string]yaml.Node   `yaml:"on"`
	Permissions map[string]string      `yaml:"permissions"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

func readWorkflowContract(t *testing.T, path string) ([]byte, workflowContract) {
	t.Helper()
	content := readRepositoryFile(t, path)
	var contract workflowContract
	if err := yaml.Unmarshal(content, &contract); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return content, contract
}

func assertWorkflowSafety(t *testing.T, content []byte, contract workflowContract) {
	t.Helper()
	if want := map[string]string{"contents": "read"}; !reflect.DeepEqual(contract.Permissions, want) {
		t.Errorf("workflow permissions = %#v, want %#v", contract.Permissions, want)
	}
	immutableAction := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	for jobName, job := range contract.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !strings.HasPrefix(step.Uses, "./") && !immutableAction.MatchString(step.Uses) {
				t.Errorf("%s external action is not pinned to an exact 40-hex commit: %q", jobName, step.Uses)
			}
		}
	}
	for _, forbidden := range []string{"secrets.", "github.token"} {
		if bytes.Contains(bytes.ToLower(content), []byte(forbidden)) {
			t.Errorf("workflow must not expose token context %q to executable steps", forbidden)
		}
	}
}

func assertWorkflowJobSteps(
	t *testing.T,
	name string,
	job workflowJob,
	checkoutRef string,
) {
	t.Helper()
	if len(job.Steps) != 5 {
		t.Fatalf("%s steps = %d, want 5", name, len(job.Steps))
	}
	wantCheckoutInputs := map[string]any{
		"fetch-depth":         1,
		"lfs":                 false,
		"persist-credentials": false,
		"ref":                 checkoutRef,
		"submodules":          false,
	}
	checkout := job.Steps[0]
	if checkout.Name != "Check out the exact requested head" || checkout.ID != "checkout" ||
		checkout.Uses != "actions/checkout@11d5960a326750d5838078e36cf38b85af677262" ||
		!reflect.DeepEqual(checkout.With, wantCheckoutInputs) {
		t.Errorf("%s checkout step = %#v", name, checkout)
	}
	wantSetupInputs := map[string]any{
		"cache":        false,
		"check-latest": false,
		"go-version":   "1.26.0",
	}
	setupGo := job.Steps[1]
	if setupGo.Name != "Install Go 1.26.0 without dependency caching" ||
		setupGo.Uses != "actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff" ||
		!reflect.DeepEqual(setupGo.With, wantSetupInputs) {
		t.Errorf("%s setup-go step = %#v", name, setupGo)
	}
	if job.Steps[2].Run != "./scripts/ci-baseline.sh assert-head" {
		t.Errorf("%s exact-head step = %#v", name, job.Steps[2])
	}
	if job.Steps[3].Run != `./scripts/ci-baseline.sh "${{ matrix.suite }}"` {
		t.Errorf("%s test step = %#v", name, job.Steps[3])
	}
	wantCleanCondition := "${{ always() && steps.checkout.outcome == 'success' }}"
	if clean := job.Steps[4]; clean.Run != "./scripts/ci-baseline.sh clean" || clean.If != wantCleanCondition {
		t.Errorf("%s clean step = %#v", name, clean)
	}
}

func TestExactHeadWorkflowContract(t *testing.T) {
	t.Run("fast pull request", func(t *testing.T) {
		content, contract := readWorkflowContract(t, ".github/workflows/exact-head-baseline.yml")
		assertWorkflowSafety(t, content, contract)
		if len(contract.On) != 1 {
			t.Fatalf("fast workflow triggers = %#v, want pull_request only", contract.On)
		}
		if _, ok := contract.On["pull_request"]; !ok {
			t.Fatal("fast workflow must run for every pull request")
		}
		if len(contract.Jobs) != 2 {
			t.Fatalf("fast workflow jobs = %d, want tests and static", len(contract.Jobs))
		}
		wantEnvironment := map[string]string{
			"EXPECTED_HEAD_SHA": "${{ github.event.pull_request.head.sha }}",
			"GH_TOKEN":          "",
			"GITHUB_TOKEN":      "",
		}
		tests := contract.Jobs["tests"]
		if tests.RunsOn != "${{ matrix.os }}" || tests.TimeoutMinutes != 40 || tests.Strategy.FailFast {
			t.Errorf("fast tests job = %#v", tests)
		}
		wantMatrix := []workflowMatrixEntry{
			{Name: "normal", OS: "ubuntu-24.04", Suite: "short-normal"},
			{Name: "normal", OS: "macos-15", Suite: "short-normal"},
			{Name: "shuffle", OS: "ubuntu-24.04", Suite: "short-shuffle"},
		}
		if !reflect.DeepEqual(tests.Strategy.Matrix.Include, wantMatrix) {
			t.Errorf("fast test matrix = %#v, want %#v", tests.Strategy.Matrix.Include, wantMatrix)
		}
		if !reflect.DeepEqual(tests.Env, wantEnvironment) {
			t.Errorf("fast test environment = %#v, want %#v", tests.Env, wantEnvironment)
		}
		assertWorkflowJobSteps(t, "fast tests", tests, "${{ github.event.pull_request.head.sha }}")

		static := contract.Jobs["static"]
		if static.RunsOn != "${{ matrix.os }}" || static.TimeoutMinutes != 15 || static.Strategy.FailFast {
			t.Errorf("static job = %#v", static)
		}
		if want := []string{"ubuntu-24.04", "macos-15"}; !reflect.DeepEqual(static.Strategy.Matrix.OS, want) {
			t.Errorf("static operating systems = %#v, want %#v", static.Strategy.Matrix.OS, want)
		}
		if !reflect.DeepEqual(static.Env, wantEnvironment) {
			t.Errorf("static environment = %#v, want %#v", static.Env, wantEnvironment)
		}
		wantStaticCommands := []string{
			"./scripts/ci-baseline.sh assert-head",
			"./scripts/ci-baseline.sh vet",
			"./scripts/ci-baseline.sh build",
			"./scripts/ci-baseline.sh installer",
			"./scripts/ci-baseline.sh diff",
			"./scripts/ci-baseline.sh clean",
		}
		if len(static.Steps) != len(wantStaticCommands)+2 {
			t.Fatalf("static steps = %d, want %d", len(static.Steps), len(wantStaticCommands)+2)
		}
		for index, want := range wantStaticCommands {
			step := static.Steps[index+2]
			if step.Run != want || step.Uses != "" {
				t.Errorf("static command step %d = %#v, want run %q", index+1, step, want)
			}
		}
		if clean := static.Steps[len(static.Steps)-1]; clean.If != "${{ always() && steps.checkout.outcome == 'success' }}" {
			t.Errorf("static clean step = %#v", clean)
		}
	})

	t.Run("full and stress exact head", func(t *testing.T) {
		content, contract := readWorkflowContract(t, ".github/workflows/full-exact-head.yml")
		assertWorkflowSafety(t, content, contract)
		if len(contract.On) != 2 {
			t.Fatalf("full workflow triggers = %#v, want pull_request and workflow_dispatch", contract.On)
		}
		pullRequest, ok := contract.On["pull_request"]
		if !ok {
			t.Fatal("full workflow is missing its critical-path pull_request trigger")
		}
		var pathFilter struct {
			Paths []string `yaml:"paths"`
		}
		if err := pullRequest.Decode(&pathFilter); err != nil {
			t.Fatal(err)
		}
		wantPaths := []string{
			".github/workflows/**",
			"go.mod",
			"go.sum",
			"internal/ci/**",
			"internal/workspace/**",
			"internal/workspacecmd/**",
			"scripts/ci-baseline.sh",
		}
		if !reflect.DeepEqual(pathFilter.Paths, wantPaths) {
			t.Errorf("critical path filter = %#v, want %#v", pathFilter.Paths, wantPaths)
		}
		dispatch, ok := contract.On["workflow_dispatch"]
		if !ok {
			t.Fatal("full workflow is missing exact-SHA dispatch")
		}
		var dispatchContract struct {
			Inputs map[string]struct {
				Required bool     `yaml:"required"`
				Default  string   `yaml:"default"`
				Type     string   `yaml:"type"`
				Options  []string `yaml:"options"`
			} `yaml:"inputs"`
		}
		if err := dispatch.Decode(&dispatchContract); err != nil {
			t.Fatal(err)
		}
		head := dispatchContract.Inputs["head_sha"]
		if !head.Required || head.Type != "string" {
			t.Errorf("head_sha dispatch input = %#v", head)
		}
		profile := dispatchContract.Inputs["profile"]
		if !profile.Required || profile.Type != "choice" || profile.Default != "full" ||
			!reflect.DeepEqual(profile.Options, []string{"full", "stress"}) {
			t.Errorf("profile dispatch input = %#v", profile)
		}
		if len(contract.Jobs) != 2 {
			t.Fatalf("full workflow jobs = %d, want full-tests and stress", len(contract.Jobs))
		}

		full := contract.Jobs["full-tests"]
		if full.If != "${{ github.event_name == 'pull_request' || inputs.profile == 'full' }}" ||
			full.RunsOn != "${{ matrix.os }}" || full.TimeoutMinutes != 40 || full.Strategy.FailFast {
			t.Errorf("full test job = %#v", full)
		}
		wantFullMatrix := []workflowMatrixEntry{
			{Name: "normal", OS: "ubuntu-24.04", Parallel: 4, Suite: "normal"},
			{Name: "normal", OS: "macos-15", Parallel: 4, Suite: "normal"},
			{Name: "shuffle", OS: "ubuntu-24.04", Parallel: 4, Suite: "shuffle"},
		}
		if !reflect.DeepEqual(full.Strategy.Matrix.Include, wantFullMatrix) {
			t.Errorf("full test matrix = %#v, want %#v", full.Strategy.Matrix.Include, wantFullMatrix)
		}
		wantFullEnvironment := map[string]string{
			"EXPECTED_HEAD_SHA":     "${{ github.event.pull_request.head.sha || inputs.head_sha }}",
			"FEATURE_TEST_PARALLEL": "${{ matrix.parallel }}",
			"GH_TOKEN":              "",
			"GITHUB_TOKEN":          "",
		}
		if !reflect.DeepEqual(full.Env, wantFullEnvironment) {
			t.Errorf("full environment = %#v, want %#v", full.Env, wantFullEnvironment)
		}
		assertWorkflowJobSteps(t, "full tests", full, "${{ github.event.pull_request.head.sha || inputs.head_sha }}")

		stress := contract.Jobs["stress"]
		if stress.If != "${{ github.event_name == 'workflow_dispatch' && inputs.profile == 'stress' }}" ||
			stress.RunsOn != "ubuntu-24.04" || stress.TimeoutMinutes != 40 || stress.Strategy.FailFast {
			t.Errorf("stress job = %#v", stress)
		}
		wantStressMatrix := []workflowMatrixEntry{
			{Name: "shuffle-1700000000", Seed: "1700000000", Suite: "shuffle"},
			{Name: "shuffle-1700000001", Seed: "1700000001", Suite: "shuffle"},
			{Name: "shuffle-1700000002", Seed: "1700000002", Suite: "shuffle"},
			{Name: "single-slot", Suite: "single-slot"},
			{Name: "repeated-concurrency", Suite: "stress-concurrency"},
		}
		if !reflect.DeepEqual(stress.Strategy.Matrix.Include, wantStressMatrix) {
			t.Errorf("stress matrix = %#v, want %#v", stress.Strategy.Matrix.Include, wantStressMatrix)
		}
		wantStressEnvironment := map[string]string{
			"EXPECTED_HEAD_SHA":     "${{ inputs.head_sha }}",
			"FEATURE_SHUFFLE_SEED":  "${{ matrix.seed }}",
			"FEATURE_TEST_PARALLEL": "4",
			"GH_TOKEN":              "",
			"GITHUB_TOKEN":          "",
		}
		if !reflect.DeepEqual(stress.Env, wantStressEnvironment) {
			t.Errorf("stress environment = %#v, want %#v", stress.Env, wantStressEnvironment)
		}
		assertWorkflowJobSteps(t, "stress", stress, "${{ inputs.head_sha }}")
	})
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

func TestBaselineScriptTierContract(t *testing.T) {
	script := readRepositoryFile(t, "scripts/ci-baseline.sh")
	for _, command := range []string{
		"short-normal)",
		"short-shuffle)",
		"short-race)",
		"single-slot)",
		"shuffle-race)",
		"stress-concurrency)",
	} {
		if !bytes.Contains(script, []byte(command)) {
			t.Errorf("baseline script is missing %s", command)
		}
	}
	for _, required := range []string{
		`go test -short -count=1 -p=1 -parallel="$test_parallel"`,
		`go test -short -count=1 -race -p=1 -parallel="$test_parallel"`,
		`go test -count=1 -p=1 -parallel=1`,
		`go test -count=1 -race -p=1 -shuffle="$seed" -parallel="$test_parallel"`,
		`go test -count=3 -p=1 -parallel="$test_parallel"`,
	} {
		if !bytes.Contains(script, []byte(required)) {
			t.Errorf("baseline script is missing bounded tier command %q", required)
		}
	}

	root := repositoryRoot(t)
	command := exec.Command(filepath.Join(root, "scripts", "ci-baseline.sh"), "short-normal")
	command.Dir = root
	command.Env = append(os.Environ(), "FEATURE_TEST_PARALLEL=5")
	output, err := command.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("must be an integer from 1 through 4")) {
		t.Fatalf("invalid parallel cap result: err=%v output=%s", err, output)
	}
}
