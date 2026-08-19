package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspacecmd"
)

func TestWorkspaceInputIsBoundedBeforeStrictDecode(t *testing.T) {
	exact := bytes.Repeat([]byte{'x'}, workspacecmd.MaxCommandInputBytes)
	content, err := readBoundedWorkspaceInput(bytes.NewReader(exact))
	if err != nil || len(content) != len(exact) {
		t.Fatalf("exact-size input = %d bytes, %v", len(content), err)
	}
	oversized := bytes.Repeat([]byte{'x'}, workspacecmd.MaxCommandInputBytes+1)
	if _, err := readBoundedWorkspaceInput(bytes.NewReader(oversized)); err == nil ||
		!strings.Contains(err.Error(), "input exceeds") {
		t.Fatalf("oversized stream error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkspaceInput(path); err == nil || !strings.Contains(err.Error(), "input exceeds") {
		t.Fatalf("oversized file error = %v", err)
	}
}

func TestHelpCommandsExitSuccessfully(t *testing.T) {
	tests := [][]string{
		{"--help"},
		{"plan", "--help"},
		{"plan", "materialize", "--help"},
		{"validate", "--help"},
		{"workspace", "--help"},
		{"workspace", "attempt", "--help"},
	}
	for _, args := range tests {
		stdout, stderr, err := runFeature(t, args...)
		if err != nil {
			t.Fatalf("feature %s failed: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, stdout, stderr)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Fatalf("feature %s missing usage:\n%s", strings.Join(args, " "), stdout)
		}
		if strings.Contains(stderr, "help requested") {
			t.Fatalf("feature %s leaked flag help error: %s", strings.Join(args, " "), stderr)
		}
	}
}

func TestWorkspaceAttemptBoundaryHelpStatesRequiredKinds(t *testing.T) {
	stdout, stderr, err := runFeature(t, "workspace", "attempt", "boundary", "--help")
	if err != nil {
		t.Fatalf("workspace attempt boundary help failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	for _, text := range []string{
		"attempt boundary request requires kind",
		"checkpoint",
		"escalation",
	} {
		if !strings.Contains(stdout, text) {
			t.Fatalf("workspace attempt boundary help missing %q:\n%s", text, stdout)
		}
	}
}

func TestRemovedMutablePlanLifecycleFailsClearly(t *testing.T) {
	for _, args := range [][]string{
		{"status", "plan", "--json"},
		{"implement", "next", "plan", "--json"},
		{"implement", "push", "plan", "--allow-push", "--write-state"},
	} {
		stdout, stderr, err := runFeature(t, args...)
		if err == nil {
			t.Fatalf("removed command feature %s unexpectedly succeeded: %s", strings.Join(args, " "), stdout)
		}
		if !strings.Contains(stderr, "was removed; use feature workspace") {
			t.Fatalf("removed command feature %s failed unclearly: %s", strings.Join(args, " "), stderr)
		}
	}
	for _, action := range []string{"scheduler", "gates", "report"} {
		_, stderr, err := runFeature(t, "workspace", action)
		if err == nil || !strings.Contains(stderr, "unsupported workspace command") {
			t.Fatalf("removed workspace read command %q error = %v, %s", action, err, stderr)
		}
	}
	stdout, stderr, err := runFeature(t, "--help")
	if err != nil {
		t.Fatalf("help: %v: %s", err, stderr)
	}
	for _, removed := range []string{"feature status <plan-dir>", "feature implement next|start"} {
		if strings.Contains(stdout, removed) {
			t.Fatalf("root help advertises removed command %q:\n%s", removed, stdout)
		}
	}
	validateHelp := runFeatureOutput(t, "validate", "--help")
	if strings.Contains(validateHelp, "before feature:implement") || !strings.Contains(validateHelp, "feature workspace validate") {
		t.Fatalf("standalone validation help implies a legacy execution bridge:\n%s", validateHelp)
	}
}

func TestRemovedWorkspaceCommandsAndWrongSubactionsFailClearly(
	t *testing.T,
) {
	for _, args := range [][]string{
		{"workspace", "queue"},
		{"workspace", "receipts"},
		{"workspace", "reconcile", "stage"},
		{"workspace", "control", "grant"},
		{"workspace", "provider", "dispatch"},
		{"workspace", "commit", "rebase"},
	} {
		stdout, stderr, err := runFeature(t, args...)
		if err == nil {
			t.Fatalf(
				"removed command feature %s unexpectedly succeeded: %s",
				strings.Join(args, " "), stdout,
			)
		}
		if !strings.Contains(stderr, "was removed") {
			t.Fatalf(
				"removed command feature %s failed unclearly: %s",
				strings.Join(args, " "), stderr,
			)
		}
	}
	for _, args := range [][]string{
		{"workspace", "attempt", "unknown"},
		{"workspace", "review", "unknown"},
		{"workspace", "integrate", "unknown"},
		{"workspace", "complete", "unknown"},
	} {
		_, stderr, err := runFeature(t, args...)
		if err == nil || !strings.Contains(stderr, "unsupported workspace") {
			t.Fatalf(
				"wrong subaction feature %s error = %v, %s",
				strings.Join(args, " "), err, stderr,
			)
		}
	}
	_, stderr, err := runFeature(
		t,
		"workspace", "status",
		"--candidate-bundle", canonicalFeatureTestTempDir(t),
	)
	if err == nil ||
		!strings.Contains(stderr, "candidate-bundle") {
		t.Fatalf("removed candidate-bundle flag error = %v, %s", err, stderr)
	}

	help := runFeatureOutput(t, "workspace", "--help")
	for _, removed := range []string{
		"queue", "receipts", "reconcile", "control",
		"provider", "commit next|rebase", "candidate-bundle",
		"feature workspace scheduler", "feature workspace gates",
		"feature workspace report",
	} {
		if strings.Contains(help, removed) {
			t.Fatalf(
				"workspace help advertises removed surface %q:\n%s",
				removed, help,
			)
		}
	}
}

func TestPlanExampleSchemaAndImmutableLock(t *testing.T) {
	stdout, stderr, err := runFeature(t, "plan", "example")
	if err != nil {
		t.Fatalf("feature plan example failed: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "schema_version: 1") || !strings.Contains(stdout, "merge_units:") || !strings.Contains(stdout, "testing:") {
		t.Fatalf("example missing manifest contract:\n%s", stdout)
	}

	stdout, stderr, err = runFeature(t, "plan", "schema", "--json")
	if err != nil {
		t.Fatalf("feature plan schema failed: %v\nstderr=%s", err, stderr)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil || schema["title"] != "feature.plan.yaml" {
		t.Fatalf("unexpected plan schema: err=%v schema=%+v", err, schema)
	}

	root := canonicalFeatureTestTempDir(t)
	manifest := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifest, []byte(runFeatureOutput(t, "plan", "example")), 0o644); err != nil {
		t.Fatal(err)
	}
	planDir := strings.TrimSpace(runFeatureOutput(t, "plan", "materialize", "--manifest", manifest, "--out-root", root))
	stdout = runFeatureOutput(t, "validate", planDir, "--write-lock", "--json")
	var validation struct {
		LockPath string `json:"lock_path"`
	}
	if err := json.Unmarshal([]byte(stdout), &validation); err != nil || validation.LockPath == "" {
		t.Fatalf("unexpected validation: err=%v output=%s", err, stdout)
	}
	lockBytes, err := os.ReadFile(validation.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	var lock map[string]json.RawMessage
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		t.Fatal(err)
	}
	if _, exists := lock["state"]; exists {
		t.Fatalf("plan lock contains mutable runtime state: %s", lockBytes)
	}
}

func TestWorkspaceSchemaExampleAndJournalBackedStatus(t *testing.T) {
	stdout := runFeatureOutput(t, "workspace", "schema", "bundle", "--json")
	var bundleSchema map[string]any
	if err := json.Unmarshal([]byte(stdout), &bundleSchema); err != nil {
		t.Fatalf("bundle schema is not JSON: %v\n%s", err, stdout)
	}
	properties := bundleSchema["properties"].(map[string]any)
	if _, exists := properties["control_plane_authority"]; exists {
		t.Fatalf("bundle schema exposes removed control-plane authority: %+v", bundleSchema)
	}
	stdout = runFeatureOutput(t, "workspace", "schema", "requests", "--json")
	var requestSchema struct {
		SchemaVersion int                        `json:"schema_version"`
		Requests      map[string]json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal([]byte(stdout), &requestSchema); err != nil || requestSchema.SchemaVersion != 2 {
		t.Fatalf("request schema is invalid: err=%v output=%s", err, stdout)
	}
	for _, name := range []string{
		"init", "attempt.reserve", "attempt.adopt-head",
		"review.record", "integrate.merge-unit", "complete.verify",
	} {
		if _, exists := requestSchema.Requests[name]; !exists {
			t.Fatalf("request schema omits %s", name)
		}
	}
	for _, removed := range []string{
		`"receipt"`, `"reconcile.`, `"control.`, `"provider.`,
		`"provider_broker"`, `"commit.rebase"`,
	} {
		if strings.Contains(stdout, removed) {
			t.Fatalf("request schema exposes removed surface %q", removed)
		}
	}
	viewSchema := runFeatureOutput(
		t, "workspace", "schema", "reports", "--json",
	)
	var viewSchemaObject map[string]any
	if err := json.Unmarshal([]byte(viewSchema), &viewSchemaObject); err != nil {
		t.Fatalf("workspace view schema is not JSON: %v\n%s", err, viewSchema)
	}
	if viewSchemaObject["additionalProperties"] != false {
		t.Fatalf("workspace view schema allows unknown fields: %+v", viewSchemaObject)
	}
	if _, exists := viewSchemaObject["reports"]; exists {
		t.Fatalf("workspace schema still publishes multiple report shapes: %+v", viewSchemaObject)
	}
	for _, required := range []string{
		`"scheduler"`, `"gates"`,
		`"workflow"`, `"target"`, `"attempts"`, `"reviews"`,
		`"integration"`, `"drift"`, `"completion"`,
	} {
		if !strings.Contains(viewSchema, required) {
			t.Fatalf("workspace view schema omits %s: %s", required, viewSchema)
		}
	}
	for _, removed := range []string{
		`provider`, `receipt`, `authorization`, `queue`, `reconciliation`,
	} {
		if strings.Contains(viewSchema, removed) {
			t.Fatalf("workspace view schema exposes removed term %q: %s", removed, viewSchema)
		}
	}
	if example := runFeatureOutput(t, "workspace", "example"); !strings.Contains(example, `"schema_version": 2`) ||
		!strings.Contains(example, `"workspace"`) ||
		!strings.Contains(example, `"execution_config"`) ||
		strings.Contains(example, "authorities") ||
		strings.Contains(example, "control_plane") {
		t.Fatalf("workspace example is incomplete:\n%s", example)
	}

	bundleDir := writeWorkspaceBundleFixture(t)
	stdout = runFeatureOutput(t, "workspace", "validate", "--bundle", bundleDir, "--write-locks", "--json")
	var validation struct {
		Status   string `json:"status"`
		LockRoot string `json:"lock_root"`
	}
	if err := json.Unmarshal([]byte(stdout), &validation); err != nil || validation.Status != "valid" {
		t.Fatalf("workspace validation failed: err=%v output=%s", err, stdout)
	}
	if validation.LockRoot != filepath.Join(bundleDir, "generated") {
		t.Fatalf("lock root = %q", validation.LockRoot)
	}
	for _, path := range []string{
		filepath.Join(validation.LockRoot, "feature.workspace.lock.json"),
		filepath.Join(validation.LockRoot, "plans", "alpha-plan.lock.json"),
		filepath.Join(validation.LockRoot, "feature.materialization.v2.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated projection %s: %v", path, err)
		}
	}
	commitWorkspaceBundleFixture(t, bundleDir)

	workspaceDir := filepath.Join(canonicalFeatureTestTempDir(t), "workspace-state")
	worktreeRoot := canonicalFeatureTestTempDir(t)
	input := writeJSONInput(t, map[string]any{
		"schema_version": 2,
		"occurred_at":    "2026-07-22T12:00:00Z",
		"worktree_root":  worktreeRoot,
	})
	stdout = runFeatureOutput(t, "workspace", "init", "--bundle", bundleDir, "--workspace", workspaceDir, "--input", input, "--json")
	var initialized struct {
		Status         string `json:"status"`
		PlanCheckpoint string `json:"plan_checkpoint"`
		Report         struct {
			Scheduler struct {
				Units []struct {
					Status string `json:"status"`
				} `json:"units"`
			} `json:"scheduler"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(stdout), &initialized); err != nil || initialized.Status != "initialized" {
		t.Fatalf("workspace init failed: err=%v output=%s", err, stdout)
	}
	if !strings.HasPrefix(initialized.PlanCheckpoint, "sha256:") {
		t.Fatalf("workspace plan checkpoint is not a digest: %q", initialized.PlanCheckpoint)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "state", "plan-checkpoint.v5.json")); err != nil {
		t.Fatalf("expected runtime checkpoint artifact: %v", err)
	}
	if len(initialized.Report.Scheduler.Units) != 1 || initialized.Report.Scheduler.Units[0].Status != "ready" {
		t.Fatalf("unexpected initialized scheduler: %+v", initialized.Report.Scheduler.Units)
	}
	status := runFeatureOutput(t, "workspace", "status", "--workspace", workspaceDir, "--bundle", bundleDir, "--json")
	var report map[string]any
	if err := json.Unmarshal([]byte(status), &report); err != nil || report["report_digest"] == "" {
		t.Fatalf("journal-backed status is invalid: err=%v output=%s", err, status)
	}
	for _, required := range []string{
		"workflow", "target", "attempts", "reviews",
		"scheduler", "gates", "integration", "drift", "completion",
	} {
		if _, exists := report[required]; !exists {
			t.Fatalf("journal-backed status omits %s: %s", required, status)
		}
	}
	for _, removed := range []string{
		"provider", "receipt", "authorization", "queue", "reconciliation",
	} {
		if strings.Contains(status, removed) {
			t.Fatalf("journal-backed status exposes %q: %s", removed, status)
		}
	}
}

func TestWorkspaceMutationInputIsStrictAndDoesNotCreateStateOnFailure(t *testing.T) {
	bundleDir := writeWorkspaceBundleFixture(t)
	workspaceDir := filepath.Join(canonicalFeatureTestTempDir(t), "invalid-workspace")
	input := filepath.Join(canonicalFeatureTestTempDir(t), "invalid.json")
	if err := os.WriteFile(input, []byte(`{"schema_version":2,"occurred_at":"2026-07-22T12:00:00Z","occurred_at":"2026-07-22T13:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runFeature(t, "workspace", "init", "--bundle", bundleDir, "--workspace", workspaceDir, "--input", input, "--json")
	if err == nil || !strings.Contains(stderr, "duplicate key") {
		t.Fatalf("duplicate request field was not rejected: err=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	if _, statErr := os.Stat(workspaceDir); !os.IsNotExist(statErr) {
		t.Fatalf("invalid request created workspace state: %v", statErr)
	}

	if err := os.WriteFile(input, []byte(`{"schema_version":2,"occurred_at":"2026-07-22T12:00:00Z","unexpected":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runFeature(t, "workspace", "init", "--bundle", bundleDir, "--workspace", workspaceDir, "--input", input)
	if err == nil || !strings.Contains(stderr, "unknown field") {
		t.Fatalf("unknown request field was not rejected: err=%v stderr=%s", err, stderr)
	}

	if err := os.WriteFile(input, []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T12:00:00Z",
  "worktree_root": "relative/attempts"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runFeature(
		t,
		"workspace", "init",
		"--bundle", bundleDir,
		"--workspace", workspaceDir,
		"--input", input,
	)
	if err == nil ||
		!strings.Contains(stderr, "worktree_root must be absolute") {
		t.Fatalf(
			"relative worktree root error = %v stderr=%s",
			err, stderr,
		)
	}
	if _, statErr := os.Stat(workspaceDir); !os.IsNotExist(statErr) {
		t.Fatalf(
			"relative worktree root created workspace state: %v",
			statErr,
		)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("FEATURE_HELPER_PROCESS") != "1" {
		return
	}
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	os.Args = append([]string{"feature"}, args...)
	main()
	os.Exit(0)
}

func writeWorkspaceBundleFixture(t *testing.T) string {
	t.Helper()
	root := canonicalFeatureTestTempDir(t)
	repository := canonicalFeatureTestTempDir(t)
	baseCommit := initializeWorkspaceTargetFixture(t, repository)
	files := map[string]string{
		"feature.workspace.bundle.json": `{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/alpha.yaml"],
  "execution_config": "config/execution.yaml"
}
`,
		"feature.workspace.yaml": fmt.Sprintf(`schema_version: 2
id: example-workspace
mode: local
repository:
  root: %s
base_ref: refs/heads/main
base_commit: %s
feature_branch: feature/example-workspace
execution_config: config/execution.yaml
plans:
  - id: alpha-plan
    source: plans/alpha.yaml
dependencies: []
`, repository, baseCommit),
		"plans/alpha.yaml": `schema_version: 2
id: alpha-plan
title: Alpha Plan
stories:
  - id: story-one
    summary: Establish the first contract.
    acceptance:
      - The first contract is explicit.
    implementation:
      - Implement the first contract.
    testing:
      - Test the first contract.
    dependencies: []
merge_units:
  - id: unit-one
    name: Unit One
    story_ids:
      - story-one
`,
		"config/execution.yaml": `schema_version: 2
policy:
  require_passing_checks: true
  allow_write_network: false
  max_attempts: 3
  max_review_rounds: 3
  max_review_fixes: 2
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      checkpoint: pause_only
      escalation: allowed
      serial_segment: serial-one
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
`,
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func initializeWorkspaceTargetFixture(t *testing.T, root string) string {
	t.Helper()
	run := func(arguments ...string) string {
		t.Helper()
		command := exec.Command(
			"git", append([]string{"-C", root}, arguments...)...,
		)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf(
				"git %s: %v\n%s",
				strings.Join(arguments, " "), err, output,
			)
		}
		return string(output)
	}
	run(
		"init", "--quiet", "--initial-branch=main",
		"--object-format=sha1", ".",
	)
	if err := os.WriteFile(
		filepath.Join(root, "seed.txt"),
		[]byte("local target seed\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	run("add", "--", "seed.txt")
	run(
		"-c", "user.name=Feature Implement Test",
		"-c", "user.email=feature-implement@localhost",
		"commit", "--quiet", "-m", "seed local target",
	)
	return "sha1:" + strings.TrimSpace(run("rev-parse", "HEAD"))
}

func commitWorkspaceBundleFixture(t *testing.T, root string) {
	t.Helper()
	run := func(arguments ...string) string {
		t.Helper()
		command := exec.Command(
			"git", append([]string{"-C", root}, arguments...)...,
		)
		command.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_TERMINAL_PROMPT=0",
		)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf(
				"git %s: %v\n%s",
				strings.Join(arguments, " "), err, output,
			)
		}
		return string(output)
	}
	run("init", "--quiet", "--initial-branch=main", "--object-format=sha1", ".")
	run("config", "user.name", "Feature Implement Test")
	run("config", "user.email", "feature-implement@localhost")
	run("add", "--", ".")
	run("commit", "--quiet", "-m", "commit workspace bundle locks")
}

func writeJSONInput(t *testing.T, value any) string {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonicalFeatureTestTempDir(t), "request.json")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runFeatureOutput(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := runFeature(t, args...)
	if err != nil {
		t.Fatalf("feature %s failed: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func canonicalFeatureTestTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("canonicalize temporary test directory: %v", err)
	}
	return canonical
}

func runFeature(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmdArgs := append([]string{"-test.run=TestHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "FEATURE_HELPER_PROCESS=1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
