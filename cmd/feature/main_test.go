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
	if _, exists := properties["control_plane_authority"]; !exists {
		t.Fatalf("bundle schema omits control-plane authority: %+v", bundleSchema)
	}
	stdout = runFeatureOutput(t, "workspace", "schema", "requests", "--json")
	var requestSchema struct {
		SchemaVersion int                        `json:"schema_version"`
		Requests      map[string]json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal([]byte(stdout), &requestSchema); err != nil || requestSchema.SchemaVersion != 2 {
		t.Fatalf("request schema is invalid: err=%v output=%s", err, stdout)
	}
	for _, name := range []string{"init", "attempt.reserve", "attempt.adopt-head", "review.record", "provider.dispatch", "complete.verify"} {
		if _, exists := requestSchema.Requests[name]; !exists {
			t.Fatalf("request schema omits %s", name)
		}
	}
	var providerReserve map[string]any
	if err := json.Unmarshal(requestSchema.Requests["provider.reserve"], &providerReserve); err != nil {
		t.Fatal(err)
	}
	providerProperties := providerReserve["properties"].(map[string]any)
	providerKinds := providerProperties["kind"].(map[string]any)["enum"].([]any)
	if fmt.Sprint(providerKinds) != "[push open_pull_request merge]" {
		t.Fatalf("provider intent schema is not closed: %v", providerKinds)
	}
	for _, removed := range []string{"provider_command", "remote_delete", "no_merge", "local_only"} {
		if strings.Contains(stdout, removed) {
			t.Fatalf("request schema exposes removed execution surface %q", removed)
		}
	}
	if example := runFeatureOutput(t, "workspace", "example"); !strings.Contains(example, `"schema_version": 2`) || !strings.Contains(example, `"workspace"`) {
		t.Fatalf("workspace example is incomplete:\n%s", example)
	}

	bundleDir := writeWorkspaceBundleFixture(t)
	initialCheckpointInput := writeJSONInput(t, map[string]any{
		"schema_version": 2,
		"occurred_at":    "2026-07-22T11:58:00Z",
	})
	initialCheckpoint := runFeatureOutput(
		t,
		"plan", "checkpoint",
		"--root", bundleDir,
		"--kind", "initial",
		"--input", initialCheckpointInput,
		"--json",
	)
	var initialCheckpointResult struct {
		Status string `json:"status"`
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal([]byte(initialCheckpoint), &initialCheckpointResult); err != nil ||
		initialCheckpointResult.Status != "checkpointed" ||
		initialCheckpointResult.Commit == "" {
		t.Fatalf("initial plan checkpoint failed: err=%v output=%s", err, initialCheckpoint)
	}
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
	lockCheckpointInput := writeJSONInput(t, map[string]any{
		"schema_version": 2,
		"occurred_at":    "2026-07-22T11:59:00Z",
	})
	lockCheckpoint := runFeatureOutput(
		t,
		"plan", "checkpoint",
		"--root", bundleDir,
		"--kind", "lock",
		"--input", lockCheckpointInput,
		"--json",
	)
	var lockCheckpointResult struct {
		Commit     string `json:"commit"`
		LockDigest string `json:"lock_digest"`
	}
	if err := json.Unmarshal([]byte(lockCheckpoint), &lockCheckpointResult); err != nil ||
		lockCheckpointResult.Commit == "" ||
		lockCheckpointResult.LockDigest == "" {
		t.Fatalf("lock plan checkpoint failed: err=%v output=%s", err, lockCheckpoint)
	}

	workspaceDir := filepath.Join(canonicalFeatureTestTempDir(t), "workspace-state")
	input := writeJSONInput(t, map[string]any{"schema_version": 2, "occurred_at": "2026-07-22T12:00:00Z"})
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
	if initialized.PlanCheckpoint != lockCheckpointResult.Commit {
		t.Fatalf("workspace plan checkpoint = %q, want %q", initialized.PlanCheckpoint, lockCheckpointResult.Commit)
	}
	if len(initialized.Report.Scheduler.Units) != 1 || initialized.Report.Scheduler.Units[0].Status != "ready" {
		t.Fatalf("unexpected initialized scheduler: %+v", initialized.Report.Scheduler.Units)
	}
	status := runFeatureOutput(t, "workspace", "status", "--workspace", workspaceDir, "--bundle", bundleDir, "--json")
	var report map[string]any
	if err := json.Unmarshal([]byte(status), &report); err != nil || report["report_digest"] == "" {
		t.Fatalf("journal-backed status is invalid: err=%v output=%s", err, status)
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
  "execution_config": "config/execution.yaml",
  "authorities": []
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
authority_sources: []
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
  require_signed_receipts: true
  allow_write_network: false
  max_attempts: 3
  max_review_rounds: 3
  max_review_fixes: 2
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      mode: pause_only
      serial_segment: serial-one
    policy:
      require_passing_checks: true
      require_signed_receipts: true
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
