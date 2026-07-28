package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpCommandsExitSuccessfully(t *testing.T) {
	tests := [][]string{
		{"--help"},
		{"plan", "--help"},
		{"plan", "materialize", "--help"},
		{"validate", "--help"},
		{"implement", "push", "--help"},
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

func TestHelpTokenAsValueDoesNotTriggerUsage(t *testing.T) {
	stdout, stderr, err := runFeature(t, "validate", "help")
	if err == nil {
		t.Fatalf("feature validate help should try to validate path named help")
	}
	if strings.Contains(stdout, "Usage:") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("literal help positional was treated as usage:\nstdout=%s\nstderr=%s", stdout, stderr)
	}

	stdout, stderr, err = runFeature(t, "plan", "materialize", "--manifest", "help")
	if err == nil {
		t.Fatalf("feature plan materialize --manifest help should try to read manifest named help")
	}
	if strings.Contains(stdout, "Usage:") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("literal help manifest was treated as usage:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func TestInvalidImplementActionHelpFails(t *testing.T) {
	stdout, stderr, err := runFeature(t, "implement", "frobnicate", "--help")
	if err == nil {
		t.Fatalf("invalid implement action help should fail")
	}
	if !strings.Contains(stderr, "unsupported implement action: frobnicate") {
		t.Fatalf("expected unsupported action error:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func TestPlanExampleAndSchemaCommands(t *testing.T) {
	stdout, stderr, err := runFeature(t, "plan", "example")
	if err != nil {
		t.Fatalf("feature plan example failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "schema_version: 1") || !strings.Contains(stdout, "merge_units:") || !strings.Contains(stdout, "testing:") {
		t.Fatalf("example missing manifest contract:\n%s", stdout)
	}

	stdout, stderr, err = runFeature(t, "plan", "schema", "--json")
	if err != nil {
		t.Fatalf("feature plan schema failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v\n%s", err, stdout)
	}
	if schema["title"] != "feature.plan.yaml" {
		t.Fatalf("unexpected schema title: %+v", schema["title"])
	}
}

func TestDocumentedTrailingFlagsWork(t *testing.T) {
	root := t.TempDir()
	example, stderr, err := runFeature(t, "plan", "example")
	if err != nil {
		t.Fatalf("feature plan example failed: %v\nstderr=%s", err, stderr)
	}
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifestPath, []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runFeature(t, "plan", "materialize", "--manifest", manifestPath, "--out-root", root)
	if err != nil {
		t.Fatalf("feature plan materialize failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	planDir := strings.TrimSpace(stdout)

	stdout, stderr, err = runFeature(t, "validate", planDir, "--write-lock", "--json")
	if err != nil {
		t.Fatalf("feature validate with trailing flags failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status":"valid"`) {
		t.Fatalf("validate did not report valid status:\n%s", stdout)
	}

	if _, stderr, err := runFeature(t, "implement", "start", planDir, "--merge-unit", "story-current-state", "--base-sha", "base", "--write-state", "--json"); err != nil {
		t.Fatalf("feature implement start failed: %v\nstderr=%s", err, stderr)
	}
	if _, stderr, err := runFeature(t, "implement", "commit", planDir, "--merge-unit", "story-current-state", "--commit-sha", "commit", "--write-state", "--json"); err != nil {
		t.Fatalf("feature implement commit failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err = runFeature(t, "implement", "push", planDir, "--merge-unit", "story-current-state", "--allow-push", "--json")
	if err != nil {
		t.Fatalf("feature implement with trailing flags failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"action":"push"`) {
		t.Fatalf("implement did not report push action:\n%s", stdout)
	}
}

func TestImplementLifecycleWriteStateCommands(t *testing.T) {
	root := t.TempDir()
	example, stderr, err := runFeature(t, "plan", "example")
	if err != nil {
		t.Fatalf("feature plan example failed: %v\nstderr=%s", err, stderr)
	}
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifestPath, []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runFeature(t, "plan", "materialize", "--manifest", manifestPath, "--out-root", root)
	if err != nil {
		t.Fatalf("feature plan materialize failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	planDir := strings.TrimSpace(stdout)
	if _, stderr, err := runFeature(t, "validate", planDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature validate failed: %v\nstderr=%s", err, stderr)
	}

	commands := [][]string{
		{"implement", "start", planDir, "--merge-unit", "story-current-state", "--base-sha", "base", "--write-state", "--json"},
		{"implement", "commit", planDir, "--merge-unit", "story-current-state", "--commit-sha", "commit", "--write-state", "--json"},
		{"implement", "push", planDir, "--merge-unit", "story-current-state", "--allow-push", "--write-state", "--json"},
		{"implement", "open-pr", planDir, "--merge-unit", "story-current-state", "--allow-open-pr", "--pr", "7", "--pr-url", "https://example.test/pr/7", "--write-state", "--json"},
		{"implement", "review", planDir, "--merge-unit", "story-current-state", "--review-status", "passed", "--write-state", "--json"},
		{"implement", "merge", planDir, "--merge-unit", "story-current-state", "--allow-merge", "--merge-commit", "merge", "--write-state", "--json"},
		{"implement", "cleanup", planDir, "--merge-unit", "story-current-state", "--write-state", "--json"},
	}
	for _, args := range commands {
		stdout, stderr, err := runFeature(t, args...)
		if err != nil {
			t.Fatalf("feature %s failed: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, stdout, stderr)
		}
		if !strings.Contains(stdout, `"status":"recorded"`) {
			t.Fatalf("feature %s did not record state:\n%s", strings.Join(args, " "), stdout)
		}
	}

	stdout, stderr, err = runFeature(t, "implement", "next", planDir, "--json")
	if err != nil {
		t.Fatalf("feature implement next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"merge_unit":"story-target-plan"`) {
		t.Fatalf("next did not advance:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"story_progress_label":"(Story 2/2)"`) {
		t.Fatalf("next did not report story progress:\n%s", stdout)
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
