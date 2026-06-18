package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	workspacepkg "github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestHelpCommandsExitSuccessfully(t *testing.T) {
	tests := [][]string{
		{"--help"},
		{"plan", "--help"},
		{"plan", "materialize", "--help"},
		{"validate", "--help"},
		{"implement", "push", "--help"},
		{"workspace", "--help"},
		{"workspace", "init", "--help"},
		{"workspace", "validate", "--help"},
		{"workspace", "status", "--help"},
		{"workspace", "next", "--help"},
		{"workspace", "heartbeat", "--help"},
		{"workspace", "release", "--help"},
		{"workspace", "recover", "--help"},
		{"workspace", "refresh-branch", "--help"},
		{"workspace", "publish-refresh", "--help"},
		{"workspace", "evaluate-gates", "--help"},
		{"workspace", "gate", "--help"},
		{"workspace", "gate", "override", "--help"},
		{"workspace", "queue", "--help"},
		{"workspace", "queue", "enter", "--help"},
		{"workspace", "attempt", "--help"},
		{"workspace", "attempt", "start", "--help"},
		{"workspace", "attempt", "abandon", "--help"},
		{"workspace", "transition", "--help"},
		{"workspace", "contract", "--help"},
		{"workspace", "contract", "publish", "--help"},
		{"workspace", "contract", "verify", "--help"},
		{"workspace", "contract", "bind", "--help"},
		{"workspace", "contract", "check-contracts", "--help"},
		{"workspace", "approve", "--help"},
		{"workspace", "approve", "grant", "--help"},
		{"workspace", "approve", "check", "--help"},
		{"workspace", "approve", "consume", "--help"},
		{"workspace", "external", "--help"},
		{"workspace", "external", "plan", "--help"},
		{"workspace", "external", "intent", "--help"},
		{"workspace", "external", "intent", "reserve", "--help"},
		{"workspace", "external", "intent", "result", "--help"},
		{"workspace", "external", "intent", "reconcile", "--help"},
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

func TestWorkspaceNamespaceDoesNotChangeImplementCommandContract(t *testing.T) {
	stdout, stderr, err := runFeature(t, "implement", "--help")
	if err != nil {
		t.Fatalf("feature implement --help failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	wantUsage := "feature implement next|start|commit|push|open-pr|review|merge|cleanup <plan-dir> [--merge-unit <id>] [--write-state] [metadata flags] [--json]"
	if !strings.Contains(stdout, wantUsage) {
		t.Fatalf("implement help changed command contract:\n%s", stdout)
	}
	for _, forbidden := range []string{"feature workspace", "workspace attempt", "feature.workspace"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("implement help leaked workspace contract %q:\n%s", forbidden, stdout)
		}
	}

	for _, action := range []string{"workspace", "attempt"} {
		stdout, stderr, err = runFeature(t, "implement", action)
		if err == nil {
			t.Fatalf("feature implement %s should fail", action)
		}
		if !strings.Contains(stderr, "unsupported implement action: "+action) {
			t.Fatalf("expected unsupported implement action error:\nstdout=%s\nstderr=%s", stdout, stderr)
		}
	}
}

func TestWorkspaceCommandShell(t *testing.T) {
	stdout, stderr, err := runFeature(t, "workspace", "--help")
	if err != nil {
		t.Fatalf("feature workspace --help failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	for _, want := range []string{
		"feature workspace init",
		"feature workspace validate",
		"feature workspace status",
		"feature workspace next",
		"feature workspace heartbeat",
		"feature workspace release",
		"feature workspace recover",
		"feature workspace evaluate-gates",
		"feature workspace gate",
		"feature workspace queue",
		"feature workspace attempt",
		"feature workspace transition",
		"feature workspace contract",
		"feature workspace approve",
		"feature workspace external",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("workspace help missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = runFeature(t, "workspace", "frobnicate")
	if err == nil {
		t.Fatalf("invalid workspace action should fail")
	}
	if !strings.Contains(stderr, "unsupported workspace action: frobnicate") {
		t.Fatalf("expected unsupported workspace action error:\nstdout=%s\nstderr=%s", stdout, stderr)
	}

}

func TestWorkspaceInitCommandWritesLockAndView(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	manifestPath := filepath.Join(workspaceDir, "feature.workspace.yaml")

	stdout, stderr, err := runFeature(t, "workspace", "init", "--manifest", manifestPath, "--write-lock", "--json")
	if err != nil {
		t.Fatalf("feature workspace init failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Status           string `json:"status"`
		WorkspaceDir     string `json:"workspace_dir"`
		ManifestPath     string `json:"manifest_path"`
		LockPath         string `json:"lock_path"`
		ViewPath         string `json:"view_path"`
		WorkspaceID      string `json:"workspace_id"`
		PlanCount        int    `json:"plan_count"`
		MergeUnitCount   int    `json:"merge_unit_count"`
		SchedulerReady   int    `json:"scheduler_ready"`
		SchedulerBlocked int    `json:"scheduler_blocked"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not workspace init JSON: %v\n%s", err, stdout)
	}
	if result.Status != "initialized" || result.WorkspaceDir != workspaceDir || result.ManifestPath != manifestPath {
		t.Fatalf("init paths = %+v", result)
	}
	if result.WorkspaceID != "workspace-a" || result.PlanCount != 1 || result.MergeUnitCount != 1 {
		t.Fatalf("init counts = %+v", result)
	}
	if result.SchedulerReady != 1 || result.SchedulerBlocked != 0 {
		t.Fatalf("scheduler counts = %+v", result)
	}
	for _, path := range []string{result.LockPath, result.ViewPath} {
		if path == "" {
			t.Fatalf("init omitted output path: %+v", result)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected init output %s: %v", path, err)
		}
	}
	if _, err := os.Stat(workspacepkg.EventsPath(workspaceDir)); !os.IsNotExist(err) {
		t.Fatalf("init should not create journal events, stat err=%v", err)
	}

	stdout, stderr, err = runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status after init failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var status struct {
		Ready []string `json:"ready"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("status stdout is not JSON: %v\n%s", err, stdout)
	}
	if strings.Join(status.Ready, ",") != "foundation:story-a" {
		t.Fatalf("status ready = %+v", status.Ready)
	}

	stdout, stderr, err = runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next after init failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		Status      string `json:"status"`
		MergeUnitID string `json:"merge_unit_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}
	if claim.Status != "claimed" || claim.MergeUnitID != "foundation:story-a" {
		t.Fatalf("claim = %+v", claim)
	}

	if _, stderr, err = runFeature(t, "workspace", "init", "--manifest", manifestPath, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace init rerun failed: %v\nstderr=%s", err, stderr)
	}
}

func TestWorkspaceInitCommandCanonicalizesSameDirectoryManifest(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	canonicalPath := filepath.Join(workspaceDir, "feature.workspace.yaml")
	manifestBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(canonicalPath); err != nil {
		t.Fatal(err)
	}
	nonCanonicalPath := filepath.Join(workspaceDir, "workspace.yaml")
	if err := os.WriteFile(nonCanonicalPath, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runFeature(t, "workspace", "init", "--manifest", nonCanonicalPath, "--write-lock", "--json")
	if err != nil {
		t.Fatalf("feature workspace init failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Status         string `json:"status"`
		WorkspaceDir   string `json:"workspace_dir"`
		ManifestPath   string `json:"manifest_path"`
		SourceManifest string `json:"source_manifest"`
		LockPath       string `json:"lock_path"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not workspace init JSON: %v\n%s", err, stdout)
	}
	if result.Status != "initialized" || result.WorkspaceDir != workspaceDir {
		t.Fatalf("init result = %+v", result)
	}
	if result.ManifestPath != canonicalPath || result.SourceManifest != nonCanonicalPath {
		t.Fatalf("manifest paths = %+v", result)
	}
	copiedBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("expected canonical manifest copy: %v", err)
	}
	if !bytes.Equal(copiedBytes, manifestBytes) {
		t.Fatalf("canonical manifest copy changed contents:\n%s", copiedBytes)
	}
	if result.LockPath == "" {
		t.Fatalf("init did not write lock: %+v", result)
	}
	var lock struct {
		Plans []struct {
			LockPath string `json:"lock_path"`
		} `json:"plans"`
	}
	lockBytes, err := os.ReadFile(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		t.Fatalf("lock is not JSON: %v\n%s", err, lockBytes)
	}
	if len(lock.Plans) != 1 || lock.Plans[0].LockPath != filepath.Join(workspaceDir, "plans", "foundation", "feature.plan.lock.json") {
		t.Fatalf("relative plan path was not resolved from manifest directory: %+v", lock.Plans)
	}

	stdout, stderr, err = runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status failed after canonicalization: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
}

func TestWorkspaceInitCommandDoesNotOverwriteCanonicalManifestOnFailure(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	canonicalPath := filepath.Join(workspaceDir, "feature.workspace.yaml")
	canonicalBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	nonCanonicalPath := filepath.Join(workspaceDir, "workspace.yaml")
	invalidManifest := strings.Replace(string(canonicalBytes), "path: plans/foundation", "path: plans/missing", 1)
	if err := os.WriteFile(nonCanonicalPath, []byte(invalidManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runFeature(t, "workspace", "init", "--manifest", nonCanonicalPath, "--write-lock", "--json")
	if err == nil {
		t.Fatalf("feature workspace init should fail for missing plan lock")
	}
	if !strings.Contains(stderr, "plan foundation lock:") {
		t.Fatalf("expected missing plan lock error:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	afterBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterBytes, canonicalBytes) {
		t.Fatalf("failed init overwrote canonical manifest:\n%s", afterBytes)
	}
}

func TestWorkspaceInitCommandRefusesDifferentCanonicalManifest(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	canonicalPath := filepath.Join(workspaceDir, "feature.workspace.yaml")
	canonicalBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	nonCanonicalPath := filepath.Join(workspaceDir, "workspace.yaml")
	alternateManifest := strings.Replace(string(canonicalBytes), "remote: origin", "remote: upstream", 1)
	if err := os.WriteFile(nonCanonicalPath, []byte(alternateManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runFeature(t, "workspace", "init", "--manifest", nonCanonicalPath, "--json")
	if err == nil {
		t.Fatalf("feature workspace init should refuse different canonical manifest")
	}
	if !strings.Contains(stderr, "refused to overwrite existing feature.workspace.yaml with different contents") {
		t.Fatalf("expected overwrite refusal:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	afterBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterBytes, canonicalBytes) {
		t.Fatalf("overwrite refusal changed canonical manifest:\n%s", afterBytes)
	}
}

func TestWorkspaceInitCommandRefusesSymlinkedCanonicalManifest(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	canonicalPath := filepath.Join(workspaceDir, "feature.workspace.yaml")
	canonicalBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(canonicalPath); err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(workspaceDir, "outside.yaml")
	linkTargetBytes := []byte("do not overwrite\n")
	if err := os.WriteFile(linkTarget, linkTargetBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkTarget, canonicalPath); err != nil {
		t.Fatal(err)
	}
	nonCanonicalPath := filepath.Join(workspaceDir, "workspace.yaml")
	if err := os.WriteFile(nonCanonicalPath, canonicalBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runFeature(t, "workspace", "init", "--manifest", nonCanonicalPath, "--json")
	if err == nil {
		t.Fatalf("feature workspace init should refuse symlinked canonical manifest")
	}
	if !strings.Contains(stderr, "refused to overwrite existing non-regular feature.workspace.yaml") {
		t.Fatalf("expected non-regular manifest error:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	afterTargetBytes, err := os.ReadFile(linkTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterTargetBytes, linkTargetBytes) {
		t.Fatalf("symlink target was overwritten:\n%s", afterTargetBytes)
	}
}

func TestWorkspaceValidateCommandWritesLock(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)

	stdout, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json")
	if err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Status   string `json:"status"`
		LockPath string `json:"lock_path"`
		Lock     struct {
			WorkspaceID string `json:"workspace_id"`
			BaseRef     string `json:"base_ref"`
			Plans       []struct {
				ID       string `json:"id"`
				LockHash string `json:"lock_hash"`
			} `json:"plans"`
			MergeUnits []struct {
				ID          string   `json:"id"`
				PlanID      string   `json:"plan_id"`
				MergeUnitID string   `json:"merge_unit_id"`
				StoryIDs    []string `json:"story_ids"`
			} `json:"merge_units"`
		} `json:"lock"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not workspace validate JSON: %v\n%s", err, stdout)
	}
	if result.Status != "valid" || result.LockPath == "" {
		t.Fatalf("result = %+v", result)
	}
	if result.Lock.WorkspaceID != "workspace-a" || result.Lock.BaseRef != "workspace-orchestration" {
		t.Fatalf("lock metadata = %+v", result.Lock)
	}
	if len(result.Lock.Plans) != 1 || result.Lock.Plans[0].ID != "foundation" || result.Lock.Plans[0].LockHash == "" {
		t.Fatalf("lock plans = %+v", result.Lock.Plans)
	}
	if len(result.Lock.MergeUnits) != 1 || result.Lock.MergeUnits[0].ID != "foundation:story-a" {
		t.Fatalf("lock merge units = %+v", result.Lock.MergeUnits)
	}
	if _, err := os.Stat(result.LockPath); err != nil {
		t.Fatalf("expected lock file: %v", err)
	}
}

func TestWorkspaceCommandsAcceptMatchingPlanLockBaseAndRemote(t *testing.T) {
	tests := []struct {
		name string
		args func(string) []string
	}{
		{
			name: "validate",
			args: func(workspaceDir string) []string {
				return []string{"workspace", "validate", workspaceDir, "--write-lock", "--json"}
			},
		},
		{
			name: "init",
			args: func(workspaceDir string) []string {
				return []string{"workspace", "init", "--manifest", filepath.Join(workspaceDir, "feature.workspace.yaml"), "--write-lock", "--json"}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceDir := workspaceWithPlanLocks(t)
			writeWorkspacePlanLockMetadata(t, workspaceDir, "workspace-orchestration", "origin")

			stdout, stderr, err := runFeature(t, tt.args(workspaceDir)...)

			if err != nil {
				t.Fatalf("feature %s failed for matching plan lock metadata: %v\nstdout=%s\nstderr=%s", tt.name, err, stdout, stderr)
			}
		})
	}
}

func TestWorkspaceCommandsRejectMismatchedPlanLockBaseAndRemote(t *testing.T) {
	commands := []struct {
		name string
		args func(string) []string
	}{
		{
			name: "validate",
			args: func(workspaceDir string) []string {
				return []string{"workspace", "validate", workspaceDir, "--write-lock", "--json"}
			},
		},
		{
			name: "init",
			args: func(workspaceDir string) []string {
				return []string{"workspace", "init", "--manifest", filepath.Join(workspaceDir, "feature.workspace.yaml"), "--write-lock", "--json"}
			},
		},
	}
	mismatches := []struct {
		name    string
		baseRef string
		remote  string
		want    string
	}{
		{
			name:    "base ref",
			baseRef: "main",
			remote:  "origin",
			want:    `plan foundation lock base_ref "main" does not match workspace base_ref "workspace-orchestration"`,
		},
		{
			name:    "remote",
			baseRef: "workspace-orchestration",
			remote:  "upstream",
			want:    `plan foundation lock remote "upstream" does not match workspace remote "origin"`,
		},
	}
	for _, command := range commands {
		for _, mismatch := range mismatches {
			t.Run(command.name+"/"+mismatch.name, func(t *testing.T) {
				workspaceDir := workspaceWithPlanLocks(t)
				writeWorkspacePlanLockMetadata(t, workspaceDir, mismatch.baseRef, mismatch.remote)

				stdout, stderr, err := runFeature(t, command.args(workspaceDir)...)

				if err == nil {
					t.Fatalf("feature %s should fail for mismatched plan lock metadata\nstdout=%s\nstderr=%s", command.name, stdout, stderr)
				}
				if !strings.Contains(stderr, mismatch.want) {
					t.Fatalf("expected mismatch error %q:\nstdout=%s\nstderr=%s", mismatch.want, stdout, stderr)
				}
			})
		}
	}
}

func TestWorkspaceCommandsUseResolvedRepoFromNonRepoWorkingDirectory(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	cwd := t.TempDir()

	stdout, stderr, err := runFeatureInDir(t, cwd, "workspace", "validate", workspaceDir, "--write-lock", "--json")
	if err != nil {
		t.Fatalf("feature workspace validate failed from non-repo cwd: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var validated struct {
		Lock struct {
			Repo string `json:"repo"`
		} `json:"lock"`
	}
	if err := json.Unmarshal([]byte(stdout), &validated); err != nil {
		t.Fatalf("validate stdout is not JSON: %v\n%s", err, stdout)
	}
	if validated.Lock.Repo != workspaceDir {
		t.Fatalf("lock repo = %q, want %q", validated.Lock.Repo, workspaceDir)
	}

	stdout, stderr, err = runFeatureInDir(t, cwd, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status failed from non-repo cwd: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var status struct {
		Ready []string `json:"ready"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("status stdout is not JSON: %v\n%s", err, stdout)
	}
	if len(status.Ready) != 1 || status.Ready[0] != "foundation:story-a" {
		t.Fatalf("status ready = %+v", status.Ready)
	}

	stdout, stderr, err = runFeatureInDir(t, cwd, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed from non-repo cwd: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("next stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeatureInDir(t, cwd,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed from non-repo cwd: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var attempt struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &attempt); err != nil {
		t.Fatalf("attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	if attempt.Status != "started" {
		t.Fatalf("attempt = %+v", attempt)
	}
}

func TestWorkspaceStatusCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err := runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Status          string         `json:"status"`
		WorkspaceID     string         `json:"workspace_id"`
		BaseRef         string         `json:"base_ref"`
		TotalMergeUnits int            `json:"total_merge_units"`
		Counts          map[string]int `json:"counts"`
		Ready           []string       `json:"ready"`
		Blocked         []string       `json:"blocked"`
		MergeUnits      []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"merge_units"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not workspace status JSON: %v\n%s", err, stdout)
	}
	if result.Status != "ok" || result.WorkspaceID != "workspace-a" || result.BaseRef != "workspace-orchestration" {
		t.Fatalf("status metadata = %+v", result)
	}
	if result.TotalMergeUnits != 1 || result.Counts["pending"] != 1 || result.Counts["completed"] != 0 {
		t.Fatalf("status counts = %+v", result)
	}
	if len(result.Ready) != 1 || result.Ready[0] != "foundation:story-a" {
		t.Fatalf("ready = %+v", result.Ready)
	}
	if len(result.Blocked) != 0 {
		t.Fatalf("blocked = %+v", result.Blocked)
	}
	if len(result.MergeUnits) != 1 || result.MergeUnits[0].ID != "foundation:story-a" || result.MergeUnits[0].Status != "pending" {
		t.Fatalf("merge units = %+v", result.MergeUnits)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "state", "scheduler.view.json")); err != nil {
		t.Fatalf("expected scheduler view file: %v", err)
	}
}

func TestWorkspaceStatusCommandText(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err := runFeature(t, "workspace", "status", workspaceDir)
	if err != nil {
		t.Fatalf("feature workspace status failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	for _, want := range []string{
		"workspace workspace-a",
		"base_ref workspace-orchestration",
		"merge_units total=1 pending=1 in_progress=0 completed=0 failed=0 ready=1 blocked=0",
		"ready foundation:story-a",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("workspace status text missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "{") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("workspace status text output is noisy:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func TestWorkspaceStatusCommandMissingLockFailsClearly(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)

	stdout, stderr, err := runFeature(t, "workspace", "status", workspaceDir)
	if err == nil {
		t.Fatalf("feature workspace status should fail without workspace lock")
	}
	if !strings.Contains(stderr, "workspace lock missing:") || !strings.Contains(stderr, "feature workspace validate <workspace-dir> --write-lock") {
		t.Fatalf("missing lock error was not clear:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if strings.Contains(stdout, "Usage:") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("missing lock should not print usage:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func TestWorkspaceStatusCommandInvalidLockFailsClearly(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if err := os.WriteFile(filepath.Join(workspaceDir, "feature.workspace.lock.json"), []byte("{not-json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runFeature(t, "workspace", "status", workspaceDir)
	if err == nil {
		t.Fatalf("feature workspace status should fail with invalid workspace lock")
	}
	if !strings.Contains(stderr, "workspace status: parse feature.workspace.lock.json") {
		t.Fatalf("invalid lock error was not clear:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if strings.Contains(stdout, "Usage:") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("invalid lock should not print usage:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func TestWorkspaceNextCommandClaimsReadyMergeUnit(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Status         string `json:"status"`
		WorkspaceID    string `json:"workspace_id"`
		BaseRef        string `json:"base_ref"`
		MergeUnitID    string `json:"merge_unit_id"`
		LeaseID        string `json:"lease_id"`
		AgentID        string `json:"agent_id"`
		LeaseExpiresAt string `json:"lease_expires_at"`
		Lifecycle      string `json:"lifecycle"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not workspace next JSON: %v\n%s", err, stdout)
	}
	if result.Status != "claimed" || result.WorkspaceID != "workspace-a" || result.BaseRef != "workspace-orchestration" {
		t.Fatalf("claim metadata = %+v", result)
	}
	if result.MergeUnitID != "foundation:story-a" || result.AgentID != "worker-a" || result.Lifecycle != "pending" {
		t.Fatalf("claim result = %+v", result)
	}
	if result.LeaseID == "" || result.LeaseExpiresAt == "" {
		t.Fatalf("claim should include lease details: %+v", result)
	}

	stdout, stderr, err = runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-b", "--claim", "--json")
	if err != nil {
		t.Fatalf("second feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var second struct {
		Status      string `json:"status"`
		MergeUnitID string `json:"merge_unit_id,omitempty"`
	}
	if err := json.Unmarshal([]byte(stdout), &second); err != nil {
		t.Fatalf("second stdout is not workspace next JSON: %v\n%s", err, stdout)
	}
	if second.Status != "none" || second.MergeUnitID != "" {
		t.Fatalf("active lease should prevent duplicate claim: %+v", second)
	}
}

func TestWorkspaceNextCommandRequiresClaimAgent(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--claim")
	if err == nil {
		t.Fatalf("feature workspace next --claim should require --agent")
	}
	if !strings.Contains(stderr, "workspace next --claim requires --agent") {
		t.Fatalf("expected missing agent error:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

func TestWorkspaceLeaseCommandsHeartbeatAndRelease(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeature(t, "workspace", "heartbeat", workspaceDir, "--agent", "worker-a", "--lease", claim.LeaseID, "--json")
	if err != nil {
		t.Fatalf("feature workspace heartbeat failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var heartbeat struct {
		Status      string `json:"status"`
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &heartbeat); err != nil {
		t.Fatalf("heartbeat stdout is not JSON: %v\n%s", err, stdout)
	}
	if heartbeat.Status != "extended" || heartbeat.MergeUnitID != claim.MergeUnitID || heartbeat.LeaseID != claim.LeaseID {
		t.Fatalf("heartbeat result = %+v", heartbeat)
	}

	stdout, stderr, err = runFeature(t, "workspace", "release", workspaceDir, "--agent", "worker-a", "--lease", claim.LeaseID, "--json")
	if err != nil {
		t.Fatalf("feature workspace release failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var released struct {
		Status      string `json:"status"`
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &released); err != nil {
		t.Fatalf("release stdout is not JSON: %v\n%s", err, stdout)
	}
	if released.Status != "released" || released.MergeUnitID != claim.MergeUnitID || released.LeaseID != claim.LeaseID {
		t.Fatalf("release result = %+v", released)
	}

	stdout, stderr, err = runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-b", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next after release failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var next struct {
		Status      string `json:"status"`
		MergeUnitID string `json:"merge_unit_id"`
		AgentID     string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &next); err != nil {
		t.Fatalf("next stdout is not JSON: %v\n%s", err, stdout)
	}
	if next.Status != "claimed" || next.MergeUnitID != claim.MergeUnitID || next.AgentID != "worker-b" {
		t.Fatalf("released merge unit was not claimable: %+v", next)
	}
}

func TestWorkspaceRecoverCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	if _, err := workspacepkg.AppendEvent(workspacepkg.AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         workspacepkg.EventLeaseGranted,
		Payload: map[string]any{
			"merge_unit_id":    "foundation:story-a",
			"lease_id":         "lease-expired",
			"agent_id":         "worker-a",
			"lease_expires_at": "2000-01-01T00:01:00Z",
		},
		WriteSet: []string{
			workspacepkg.LeaseResource("foundation:story-a"),
			workspacepkg.MergeUnitResource("foundation:story-a"),
		},
		Now: fixedFeatureTime("2000-01-01T00:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent expired lease: %v", err)
	}

	stdout, stderr, err := runFeature(t, "workspace", "recover", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace recover failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Status         string `json:"status"`
		RecoveredCount int    `json:"recovered_count"`
		Recovered      []struct {
			MergeUnitID string `json:"merge_unit_id"`
			LeaseID     string `json:"lease_id"`
			AgentID     string `json:"agent_id"`
		} `json:"recovered"`
		Actions []struct {
			Type        string `json:"type"`
			MergeUnitID string `json:"merge_unit_id"`
			LeaseID     string `json:"lease_id"`
			Status      string `json:"status"`
		} `json:"actions"`
		Ready []string `json:"ready"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("recover stdout is not JSON: %v\n%s", err, stdout)
	}
	if result.Status != "recovered" || result.RecoveredCount != 1 {
		t.Fatalf("recover result = %+v", result)
	}
	if len(result.Recovered) != 1 || result.Recovered[0].LeaseID != "lease-expired" || result.Recovered[0].AgentID != "worker-a" {
		t.Fatalf("recovered leases = %+v", result.Recovered)
	}
	if len(result.Actions) != 1 || result.Actions[0].Type != "recovered_lease" || result.Actions[0].LeaseID != "lease-expired" || result.Actions[0].Status != "recovered" {
		t.Fatalf("recovery actions = %+v", result.Actions)
	}
	if len(result.Ready) != 1 || result.Ready[0] != "foundation:story-a" {
		t.Fatalf("ready = %+v", result.Ready)
	}
}

func TestWorkspaceAttemptStartCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var attempt struct {
		Status        string   `json:"status"`
		MergeUnitID   string   `json:"merge_unit_id"`
		AttemptID     string   `json:"attempt_id"`
		AttemptNumber int      `json:"attempt_number"`
		Branch        string   `json:"branch"`
		Worktree      string   `json:"worktree"`
		BaseRef       string   `json:"base_ref"`
		BaseSHA       string   `json:"base_sha"`
		Mode          string   `json:"mode"`
		Commands      []string `json:"commands"`
	}
	if err := json.Unmarshal([]byte(stdout), &attempt); err != nil {
		t.Fatalf("attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	if attempt.Status != "started" || attempt.MergeUnitID != "foundation:story-a" || attempt.AttemptID != "foundation:story-a:attempt-1" {
		t.Fatalf("attempt result = %+v", attempt)
	}
	if attempt.AttemptNumber != 1 || attempt.BaseRef != "workspace-orchestration" || attempt.BaseSHA != "base-sha-cli" || attempt.Mode != "fresh-from-base" {
		t.Fatalf("attempt metadata = %+v", attempt)
	}
	if attempt.Branch != "feature/workspace-a/foundation/story-a/attempt-1" {
		t.Fatalf("branch = %q", attempt.Branch)
	}
	if !strings.Contains(attempt.Worktree, filepath.Join("state", "worktrees", "workspace-a", "foundation", "story-a", "attempt-1")) {
		t.Fatalf("worktree = %q", attempt.Worktree)
	}
	wantCommand := "git worktree add -b feature/workspace-a/foundation/story-a/attempt-1 " + attempt.Worktree + " workspace-orchestration"
	if len(attempt.Commands) != 1 || attempt.Commands[0] != wantCommand {
		t.Fatalf("commands = %+v, want %q", attempt.Commands, wantCommand)
	}
}

func TestWorkspaceAttemptAbandonCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var started struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &started); err != nil {
		t.Fatalf("start stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "abandon", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", started.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--reason", "review findings require restart",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt abandon failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var abandoned struct {
		Status      string `json:"status"`
		MergeUnitID string `json:"merge_unit_id"`
		AttemptID   string `json:"attempt_id"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stdout), &abandoned); err != nil {
		t.Fatalf("abandon stdout is not JSON: %v\n%s", err, stdout)
	}
	if abandoned.Status != "abandoned" || abandoned.MergeUnitID != claim.MergeUnitID || abandoned.AttemptID != started.AttemptID || abandoned.Reason != "review findings require restart" {
		t.Fatalf("abandon result = %+v", abandoned)
	}
}

func TestWorkspaceAttemptRestartEnforcesCurrentAttempt(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}

	type attemptJSON struct {
		Status        string `json:"status"`
		MergeUnitID   string `json:"merge_unit_id"`
		AttemptID     string `json:"attempt_id"`
		AttemptNumber int    `json:"attempt_number"`
		Branch        string `json:"branch"`
		Worktree      string `json:"worktree"`
		BaseSHA       string `json:"base_sha"`
		Mode          string `json:"mode"`
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "first-base-sha",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var first attemptJSON
	if err := json.Unmarshal([]byte(stdout), &first); err != nil {
		t.Fatalf("first attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	if first.AttemptID != "foundation:story-a:attempt-1" || first.AttemptNumber != 1 || first.BaseSHA != "first-base-sha" {
		t.Fatalf("first attempt = %+v", first)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "abandon", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", first.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--reason", "restart with clean state",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt abandon failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "second-base-sha",
		"--json",
	)
	if err != nil {
		t.Fatalf("second feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var second attemptJSON
	if err := json.Unmarshal([]byte(stdout), &second); err != nil {
		t.Fatalf("second attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	if second.Status != "started" || second.MergeUnitID != claim.MergeUnitID || second.AttemptID != "foundation:story-a:attempt-2" || second.AttemptNumber != 2 {
		t.Fatalf("second attempt result = %+v", second)
	}
	if second.BaseSHA != "second-base-sha" || second.Mode != "fresh-from-base" {
		t.Fatalf("second attempt metadata = %+v", second)
	}
	if second.Branch != "feature/workspace-a/foundation/story-a/attempt-2" {
		t.Fatalf("second branch = %q", second.Branch)
	}
	wantSecondWorktree := filepath.Join(workspaceDir, "state", "worktrees", "workspace-a", "foundation", "story-a", "attempt-2")
	if second.Worktree != wantSecondWorktree {
		t.Fatalf("second worktree = %q, want %q", second.Worktree, wantSecondWorktree)
	}
	if second.Branch == first.Branch || second.Worktree == first.Worktree || second.BaseSHA == first.BaseSHA {
		t.Fatalf("second attempt carried first metadata: first=%+v second=%+v", first, second)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "transition", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", first.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--from", "pending",
		"--to", "in_progress",
		"--evidence", "worktree="+first.Worktree,
		"--json",
	)
	if err == nil {
		t.Fatalf("non-current attempt transition should fail\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "attempt "+first.AttemptID+" is not current active attempt "+second.AttemptID) {
		t.Fatalf("non-current transition error = %q", stderr)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "transition", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", second.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--from", "pending",
		"--to", "in_progress",
		"--evidence", "worktree="+second.Worktree,
		"--json",
	)
	if err != nil {
		t.Fatalf("current attempt transition failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	viewPath := filepath.Join(workspaceDir, "state", "scheduler.view.json")
	if err := os.Remove(viewPath); err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("remove scheduler view: %v", err)
		}
	}
	stdout, stderr, err = runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status rebuild failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var status struct {
		MergeUnits []struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			CurrentAttempt *struct {
				AttemptID     string `json:"attempt_id"`
				AttemptNumber int    `json:"attempt_number"`
				Branch        string `json:"branch"`
				Worktree      string `json:"worktree"`
				BaseSHA       string `json:"base_sha"`
				Mode          string `json:"mode"`
				Status        string `json:"status"`
			} `json:"current_attempt,omitempty"`
		} `json:"merge_units"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("status stdout is not JSON: %v\n%s", err, stdout)
	}
	found := false
	for _, unit := range status.MergeUnits {
		if unit.ID != claim.MergeUnitID {
			continue
		}
		found = true
		if unit.Status != "in_progress" {
			t.Fatalf("merge unit status = %q", unit.Status)
		}
		if unit.CurrentAttempt == nil {
			t.Fatalf("current attempt missing for %+v", unit)
		}
		if unit.CurrentAttempt.AttemptID != second.AttemptID ||
			unit.CurrentAttempt.AttemptNumber != second.AttemptNumber ||
			unit.CurrentAttempt.Branch != second.Branch ||
			unit.CurrentAttempt.Worktree != second.Worktree ||
			unit.CurrentAttempt.BaseSHA != second.BaseSHA ||
			unit.CurrentAttempt.Mode != second.Mode ||
			unit.CurrentAttempt.Status != "active" {
			t.Fatalf("rebuilt current attempt = %+v, want second %+v", unit.CurrentAttempt, second)
		}
	}
	if !found {
		t.Fatalf("merge unit %s missing from status: %+v", claim.MergeUnitID, status.MergeUnits)
	}
}

func TestWorkspaceTransitionCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var attempt struct {
		AttemptID string `json:"attempt_id"`
		Worktree  string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(stdout), &attempt); err != nil {
		t.Fatalf("attempt stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "transition", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--from", "pending",
		"--to", "in_progress",
		"--evidence", "worktree="+attempt.Worktree,
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace transition failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var transitioned struct {
		Status      string         `json:"status"`
		MergeUnitID string         `json:"merge_unit_id"`
		AttemptID   string         `json:"attempt_id"`
		From        string         `json:"from"`
		To          string         `json:"to"`
		EventType   string         `json:"event_type"`
		Evidence    map[string]any `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(stdout), &transitioned); err != nil {
		t.Fatalf("transition stdout is not JSON: %v\n%s", err, stdout)
	}
	if transitioned.Status != "transitioned" || transitioned.MergeUnitID != claim.MergeUnitID || transitioned.AttemptID != attempt.AttemptID {
		t.Fatalf("transition result = %+v", transitioned)
	}
	if transitioned.From != "pending" || transitioned.To != "in_progress" || transitioned.EventType != "merge_unit.started" {
		t.Fatalf("transition metadata = %+v", transitioned)
	}
	if transitioned.Evidence["worktree"] != attempt.Worktree {
		t.Fatalf("transition evidence = %+v", transitioned.Evidence)
	}
}

func TestWorkspaceEvaluateGatesCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var attempt struct {
		AttemptID string `json:"attempt_id"`
		Branch    string `json:"branch"`
		Worktree  string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(stdout), &attempt); err != nil {
		t.Fatalf("attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	revisions, err := workspacepkg.ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	refreshResource := workspacepkg.RefreshResource(claim.MergeUnitID + ":" + attempt.AttemptID)
	if _, err := workspacepkg.AppendEvent(workspacepkg.AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         workspacepkg.EventBranchRefreshRecorded,
		Payload: map[string]any{
			"merge_unit_id": claim.MergeUnitID,
			"attempt_id":    attempt.AttemptID,
			"status":        workspacepkg.RefreshStatusSucceeded,
			"branch":        attempt.Branch,
			"worktree":      attempt.Worktree,
			"old_base":      "base-sha-cli",
			"new_base":      "base-sha-cli",
			"pre_head":      "head-sha-cli",
			"post_head":     "head-sha-cli",
			"backup_ref":    "backup/cli",
			"evidence_path": "state/refresh-evidence-cli.json",
		},
		ReadSet:  map[string]int{refreshResource: revisions[refreshResource]},
		WriteSet: []string{refreshResource},
		Now:      fixedFeatureTime("2026-01-02T15:03:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent refresh: %v", err)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "evaluate-gates", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace evaluate-gates failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Status           string `json:"status"`
		MergeUnitID      string `json:"merge_unit_id"`
		AttemptID        string `json:"attempt_id"`
		EvaluatorVersion string `json:"evaluator_version"`
		InputHash        string `json:"input_hash"`
		OutputHash       string `json:"output_hash"`
		Gates            []struct {
			Gate   string `json:"gate"`
			Status string `json:"status"`
		} `json:"gates"`
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("evaluate-gates stdout is not JSON: %v\n%s", err, stdout)
	}
	if result.Status != "recorded" || result.MergeUnitID != claim.MergeUnitID || result.AttemptID != attempt.AttemptID || result.EventID == "" {
		t.Fatalf("evaluate result = %+v", result)
	}
	if result.EvaluatorVersion != workspacepkg.GateEvaluatorVersion || result.InputHash == "" || result.OutputHash == "" {
		t.Fatalf("evaluate hashes = %+v", result)
	}
	if len(result.Gates) != 5 {
		t.Fatalf("evaluate gates = %+v", result.Gates)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "gate", "override", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--gate", "security",
		"--status", workspacepkg.GateStatusRetainedByOperator,
		"--reason", "operator accepted base-only rebase",
		"--input-hash", result.InputHash,
		"--head-sha", "head-sha-cli",
		"--base-sha", "base-sha-cli",
		"--operator", "operator-a",
		"--expires-in", "1h",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace gate override failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var override struct {
		Status   string `json:"status"`
		Override struct {
			OverrideID string `json:"override_id"`
			Gate       string `json:"gate"`
			Status     string `json:"status"`
			Reason     string `json:"reason"`
			InputHash  string `json:"input_hash"`
			Operator   string `json:"operator"`
		} `json:"override"`
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &override); err != nil {
		t.Fatalf("override stdout is not JSON: %v\n%s", err, stdout)
	}
	if override.Status != "recorded" ||
		override.Override.OverrideID == "" ||
		override.Override.Gate != "security" ||
		override.Override.Status != workspacepkg.GateStatusRetainedByOperator ||
		override.Override.InputHash != result.InputHash ||
		override.Override.Operator != "operator-a" ||
		override.EventID == "" {
		t.Fatalf("override result = %+v", override)
	}
}

func TestWorkspaceApproveCommandGrantCheckConsumeJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var attempt struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &attempt); err != nil {
		t.Fatalf("attempt stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "approve", "grant", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--action", "push",
		"--branch", "feature/test",
		"--expires-in", "1h",
		"--max-uses", "1",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace approve grant failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var granted struct {
		Status   string `json:"status"`
		Approval struct {
			ApprovalID string   `json:"approval_id"`
			Actions    []string `json:"actions"`
			Branch     string   `json:"branch"`
			MaxUses    int      `json:"max_uses"`
			UsedCount  int      `json:"used_count"`
			Status     string   `json:"status"`
		} `json:"approval"`
	}
	if err := json.Unmarshal([]byte(stdout), &granted); err != nil {
		t.Fatalf("grant stdout is not JSON: %v\n%s", err, stdout)
	}
	if granted.Status != "granted" || granted.Approval.ApprovalID == "" || granted.Approval.Branch != "feature/test" || granted.Approval.MaxUses != 1 || granted.Approval.Status != "active" {
		t.Fatalf("grant result = %+v", granted)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "approve", "grant", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--action", "push",
		"--expires-in", "1h",
		"--max-uses", "0",
		"--json",
	)
	if err == nil {
		t.Fatalf("feature workspace approve grant --max-uses 0 should fail\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "--max-uses must be greater than zero") {
		t.Fatalf("max uses error = %q", stderr)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "approve", "check", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--action", "push",
		"--branch", "feature/test",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace approve check failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var checked struct {
		Status    string `json:"status"`
		Action    string `json:"action"`
		Approvals []struct {
			ApprovalID string `json:"approval_id"`
		} `json:"approvals"`
	}
	if err := json.Unmarshal([]byte(stdout), &checked); err != nil {
		t.Fatalf("check stdout is not JSON: %v\n%s", err, stdout)
	}
	if checked.Status != "approved" || checked.Action != "push" || len(checked.Approvals) != 1 || checked.Approvals[0].ApprovalID != granted.Approval.ApprovalID {
		t.Fatalf("check result = %+v", checked)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "approve", "consume", workspaceDir,
		"--approval", granted.Approval.ApprovalID,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--action", "push",
		"--branch", "feature/test",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace approve consume failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var consumed struct {
		Status   string `json:"status"`
		Approval struct {
			ApprovalID string `json:"approval_id"`
			MaxUses    int    `json:"max_uses"`
			UsedCount  int    `json:"used_count"`
			Status     string `json:"status"`
		} `json:"approval"`
	}
	if err := json.Unmarshal([]byte(stdout), &consumed); err != nil {
		t.Fatalf("consume stdout is not JSON: %v\n%s", err, stdout)
	}
	if consumed.Status != "consumed" || consumed.Approval.ApprovalID != granted.Approval.ApprovalID || consumed.Approval.UsedCount != 1 || consumed.Approval.MaxUses != 1 || consumed.Approval.Status != "exhausted" {
		t.Fatalf("consume result = %+v", consumed)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "approve", "consume", workspaceDir,
		"--approval", granted.Approval.ApprovalID,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--action", "push",
		"--branch", "feature/test",
		"--json",
	)
	if err == nil {
		t.Fatalf("second feature workspace approve consume should fail\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "has no uses remaining") {
		t.Fatalf("second consume error = %q", stderr)
	}
}

func TestWorkspaceExternalIntentReserveCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var attempt struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &attempt); err != nil {
		t.Fatalf("attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "approve", "grant", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--action", "push",
		"--branch", "feature/test",
		"--head-sha", "head-sha-cli",
		"--base-sha", "base-sha-cli",
		"--expires-in", "1h",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace approve grant failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var granted struct {
		Approval struct {
			ApprovalID string `json:"approval_id"`
		} `json:"approval"`
	}
	if err := json.Unmarshal([]byte(stdout), &granted); err != nil {
		t.Fatalf("grant stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "external", "intent", "reserve", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--approval", granted.Approval.ApprovalID,
		"--action", "push",
		"--branch", "feature/test",
		"--head-sha", "head-sha-cli",
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace external intent reserve failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var reserved struct {
		Status string `json:"status"`
		Intent struct {
			IntentID          string   `json:"intent_id"`
			IdempotencyKey    string   `json:"idempotency_key"`
			Action            string   `json:"action"`
			Target            string   `json:"target"`
			ApprovalID        string   `json:"approval_id"`
			RequestedHeadSHA  string   `json:"requested_head_sha"`
			ExpectedBaseSHA   string   `json:"expected_base_sha"`
			AffectedResources []string `json:"affected_resources"`
			Status            string   `json:"status"`
		} `json:"intent"`
	}
	if err := json.Unmarshal([]byte(stdout), &reserved); err != nil {
		t.Fatalf("reserve stdout is not JSON: %v\n%s", err, stdout)
	}
	if reserved.Status != "reserved" || reserved.Intent.Status != "reserved" || reserved.Intent.IntentID == "" || reserved.Intent.IdempotencyKey == "" {
		t.Fatalf("reserve result = %+v", reserved)
	}
	if reserved.Intent.Action != "push" || reserved.Intent.Target != "branch:feature/test" || reserved.Intent.ApprovalID != granted.Approval.ApprovalID {
		t.Fatalf("reserve metadata = %+v", reserved.Intent)
	}
	if reserved.Intent.RequestedHeadSHA != "head-sha-cli" || reserved.Intent.ExpectedBaseSHA != "base-sha-cli" {
		t.Fatalf("reserve SHAs = %+v", reserved.Intent)
	}
	if !stringSliceContains(reserved.Intent.AffectedResources, "provider_target:push:branch:feature/test") ||
		!stringSliceContains(reserved.Intent.AffectedResources, "remote_ref:feature/test") {
		t.Fatalf("affected resources = %+v", reserved.Intent.AffectedResources)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "external", "intent", "result", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--intent", reserved.Intent.IntentID,
		"--status", "succeeded",
		"--details", "provider completed",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace external intent result failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var recorded struct {
		Status string `json:"status"`
		Intent struct {
			IntentID string `json:"intent_id"`
			Status   string `json:"status"`
			Result   struct {
				Status   string `json:"status"`
				Accepted bool   `json:"accepted"`
				Details  string `json:"details"`
			} `json:"result"`
		} `json:"intent"`
		Result struct {
			Status   string `json:"status"`
			Accepted bool   `json:"accepted"`
			Details  string `json:"details"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &recorded); err != nil {
		t.Fatalf("result stdout is not JSON: %v\n%s", err, stdout)
	}
	if recorded.Status != "recorded" || recorded.Intent.IntentID != reserved.Intent.IntentID || recorded.Intent.Status != "succeeded" {
		t.Fatalf("recorded result = %+v", recorded)
	}
	if recorded.Result.Status != "succeeded" || !recorded.Result.Accepted || recorded.Result.Details != "provider completed" ||
		recorded.Intent.Result.Status != recorded.Result.Status || !recorded.Intent.Result.Accepted {
		t.Fatalf("recorded result metadata = %+v", recorded)
	}

	stdout, stderr, err = runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status should ignore resolved external intent events: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status":"ok"`) {
		t.Fatalf("workspace status after intent reserve = %s", stdout)
	}
}

func TestWorkspaceContractPublishAndVerifyCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithContractPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err := runFeature(t,
		"workspace", "contract", "publish", workspaceDir,
		"--contract", "api-contract",
		"--version", "v1",
		"--producer-merge-unit", "foundation:story-a",
		"--producer-commit", "producer-commit-cli",
		"--command-result", "go test ./...=passed",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace contract publish failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var published struct {
		Status              string `json:"status"`
		ContractID          string `json:"contract_id"`
		Version             string `json:"version"`
		ProducerMergeUnitID string `json:"producer_merge_unit_id"`
		ProducerCommit      string `json:"producer_commit"`
		ArtifactID          string `json:"artifact_id"`
		ArtifactPath        string `json:"artifact_path"`
		ArtifactHash        string `json:"artifact_hash"`
		CommandResults      []struct {
			Command string `json:"command"`
			Status  string `json:"status"`
		} `json:"command_results"`
	}
	if err := json.Unmarshal([]byte(stdout), &published); err != nil {
		t.Fatalf("publish stdout is not JSON: %v\n%s", err, stdout)
	}
	if published.Status != "published" || published.ContractID != "api-contract" || published.Version != "v1" {
		t.Fatalf("published metadata = %+v", published)
	}
	if published.ProducerMergeUnitID != "foundation:story-a" || published.ProducerCommit != "producer-commit-cli" {
		t.Fatalf("published producer metadata = %+v", published)
	}
	if published.ArtifactID != "openapi" || published.ArtifactPath != "contracts/openapi.yaml" || published.ArtifactHash == "" {
		t.Fatalf("published artifact metadata = %+v", published)
	}
	if len(published.CommandResults) != 1 || published.CommandResults[0].Command != "go test ./..." || published.CommandResults[0].Status != "passed" {
		t.Fatalf("published command results = %+v", published.CommandResults)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "contract", "verify", workspaceDir,
		"--contract", "api-contract",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace contract verify failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var verified struct {
		Status         string `json:"status"`
		ContractID     string `json:"contract_id"`
		ArtifactExists bool   `json:"artifact_exists"`
		HashMatches    bool   `json:"hash_matches"`
		PublishedHash  string `json:"published_hash"`
		CurrentHash    string `json:"current_hash"`
	}
	if err := json.Unmarshal([]byte(stdout), &verified); err != nil {
		t.Fatalf("verify stdout is not JSON: %v\n%s", err, stdout)
	}
	if verified.Status != "ok" || verified.ContractID != "api-contract" || !verified.ArtifactExists || !verified.HashMatches {
		t.Fatalf("verify result = %+v", verified)
	}
	if verified.PublishedHash != published.ArtifactHash || verified.CurrentHash != published.ArtifactHash {
		t.Fatalf("verify hashes = %+v, published=%+v", verified, published)
	}

	stdout, stderr, err = runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status should ignore contract events: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status":"ok"`) {
		t.Fatalf("workspace status after contract publish = %s", stdout)
	}
}

func TestWorkspaceContractBindAndCheckContractsCommandJSON(t *testing.T) {
	workspaceDir := workspaceWithContractPlanLocks(t)
	if _, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json"); err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstderr=%s", err, stderr)
	}
	if stdout, stderr, err := runFeature(t,
		"workspace", "contract", "publish", workspaceDir,
		"--contract", "api-contract",
		"--version", "v1",
		"--producer-merge-unit", "foundation:story-a",
		"--producer-commit", "producer-commit-cli",
		"--command-result", "go test ./...=passed",
		"--json",
	); err != nil {
		t.Fatalf("feature workspace contract publish failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	stdout, stderr, err := runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-producer", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next producer failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var producerClaim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &producerClaim); err != nil {
		t.Fatalf("producer claim stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", producerClaim.MergeUnitID,
		"--agent", "worker-producer",
		"--lease", producerClaim.LeaseID,
		"--base-sha", "producer-base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace producer attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var producerAttempt struct {
		AttemptID string `json:"attempt_id"`
		Worktree  string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(stdout), &producerAttempt); err != nil {
		t.Fatalf("producer attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	if _, stderr, err = runFeature(t,
		"workspace", "transition", workspaceDir,
		"--merge-unit", producerClaim.MergeUnitID,
		"--attempt", producerAttempt.AttemptID,
		"--agent", "worker-producer",
		"--lease", producerClaim.LeaseID,
		"--from", "pending",
		"--to", "in_progress",
		"--evidence", "worktree="+producerAttempt.Worktree,
		"--json",
	); err != nil {
		t.Fatalf("feature workspace producer transition start failed: %v\nstderr=%s", err, stderr)
	}
	if _, stderr, err = runFeature(t,
		"workspace", "transition", workspaceDir,
		"--merge-unit", producerClaim.MergeUnitID,
		"--attempt", producerAttempt.AttemptID,
		"--agent", "worker-producer",
		"--lease", producerClaim.LeaseID,
		"--from", "in_progress",
		"--to", "completed",
		"--evidence", "commit_sha=producer-commit-cli",
		"--json",
	); err != nil {
		t.Fatalf("feature workspace producer transition complete failed: %v\nstderr=%s", err, stderr)
	}

	stdout, stderr, err = runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-consumer", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next consumer failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var consumerClaim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &consumerClaim); err != nil {
		t.Fatalf("consumer claim stdout is not JSON: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", consumerClaim.MergeUnitID,
		"--agent", "worker-consumer",
		"--lease", consumerClaim.LeaseID,
		"--base-sha", "consumer-base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace consumer attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var consumerAttempt struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &consumerAttempt); err != nil {
		t.Fatalf("consumer attempt stdout is not JSON: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "contract", "check-contracts", workspaceDir,
		"--merge-unit", consumerClaim.MergeUnitID,
		"--attempt", consumerAttempt.AttemptID,
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace contract check before bind failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var before struct {
		Status   string `json:"status"`
		Bindings []struct {
			ContractID string `json:"contract_id"`
			Status     string `json:"status"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal([]byte(stdout), &before); err != nil {
		t.Fatalf("check before stdout is not JSON: %v\n%s", err, stdout)
	}
	if before.Status != "missing" || len(before.Bindings) != 1 || before.Bindings[0].Status != "missing" {
		t.Fatalf("check before bind = %+v", before)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "contract", "bind", workspaceDir,
		"--contract", "api-contract",
		"--merge-unit", consumerClaim.MergeUnitID,
		"--attempt", consumerAttempt.AttemptID,
		"--agent", "worker-consumer",
		"--lease", consumerClaim.LeaseID,
		"--command-result", "go test ./...=passed",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace contract bind failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var bound struct {
		Status       string `json:"status"`
		ContractID   string `json:"contract_id"`
		MergeUnitID  string `json:"merge_unit_id"`
		AttemptID    string `json:"attempt_id"`
		ArtifactHash string `json:"artifact_hash"`
	}
	if err := json.Unmarshal([]byte(stdout), &bound); err != nil {
		t.Fatalf("bind stdout is not JSON: %v\n%s", err, stdout)
	}
	if bound.Status != "bound" || bound.ContractID != "api-contract" || bound.MergeUnitID != consumerClaim.MergeUnitID || bound.AttemptID != consumerAttempt.AttemptID || bound.ArtifactHash == "" {
		t.Fatalf("bind result = %+v", bound)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "contract", "check-contracts", workspaceDir,
		"--merge-unit", consumerClaim.MergeUnitID,
		"--attempt", consumerAttempt.AttemptID,
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace contract check after bind failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var after struct {
		Status   string `json:"status"`
		Bindings []struct {
			ContractID        string `json:"contract_id"`
			Status            string `json:"status"`
			BoundArtifactHash string `json:"bound_artifact_hash"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal([]byte(stdout), &after); err != nil {
		t.Fatalf("check after stdout is not JSON: %v\n%s", err, stdout)
	}
	if after.Status != "current" || len(after.Bindings) != 1 || after.Bindings[0].Status != "current" || after.Bindings[0].BoundArtifactHash != bound.ArtifactHash {
		t.Fatalf("check after bind = %+v, bound=%+v", after, bound)
	}
}

func TestWorkspaceContractGateQueueSmokeCommandJSON(t *testing.T) {
	t.Run("queues ready consumer", func(t *testing.T) {
		ready := prepareContractGateQueueSmoke(t)

		queued := queueContractGateSmoke(t, ready)
		if queued.Status != "queued" ||
			queued.Queue == nil ||
			queued.Queue.MergeUnitID != ready.ConsumerClaim.MergeUnitID ||
			queued.Queue.AttemptID != ready.ConsumerAttempt.AttemptID ||
			queued.Queue.Branch != ready.ConsumerAttempt.Branch ||
			queued.Queue.HeadSHA != ready.HeadSHA ||
			queued.Queue.BaseSHA != ready.BaseSHA ||
			queued.Queue.Position != 1 ||
			queued.Queue.ApprovalID != ready.ApprovalID {
			t.Fatalf("queue result = %+v", queued)
		}
		if queued.Queue.GateInputHash != ready.Evaluation.InputHash || queued.Queue.GateOutputHash != ready.Evaluation.OutputHash {
			t.Fatalf("queue gate hashes = %+v, evaluation=%+v", queued.Queue, ready.Evaluation)
		}

		var status struct {
			MergeQueue []struct {
				QueueID     string `json:"queue_id"`
				MergeUnitID string `json:"merge_unit_id"`
				Position    int    `json:"position"`
			} `json:"merge_queue"`
			MergeUnits []workspaceSmokeStatusUnit `json:"merge_units"`
		}
		runFeatureJSON(t, &status, "workspace", "status", ready.WorkspaceDir, "--json")
		if len(status.MergeQueue) != 1 ||
			status.MergeQueue[0].QueueID != queued.Queue.QueueID ||
			status.MergeQueue[0].MergeUnitID != ready.ConsumerClaim.MergeUnitID ||
			status.MergeQueue[0].Position != 1 {
			t.Fatalf("status merge queue = %+v", status.MergeQueue)
		}
		consumer := findWorkspaceStatusUnitForSmoke(t, status.MergeUnits, ready.ConsumerClaim.MergeUnitID)
		if consumer.MergeQueue == nil || consumer.MergeQueue.QueueID != queued.Queue.QueueID {
			t.Fatalf("consumer queue status = %+v", consumer.MergeQueue)
		}
		if len(consumer.ContractBindings) != 1 || consumer.ContractBindings[0].ContractID != "api-contract" || consumer.ContractBindings[0].Status != "current" {
			t.Fatalf("consumer contract bindings = %+v", consumer.ContractBindings)
		}
	})

	t.Run("stale contract blocks queue", func(t *testing.T) {
		ready := prepareContractGateQueueSmoke(t)
		if err := os.WriteFile(filepath.Join(ready.WorkspaceDir, "contracts", "openapi.yaml"), []byte("openapi: 3.1.0\ninfo:\n  title: v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var published struct {
			Status       string `json:"status"`
			ArtifactHash string `json:"artifact_hash"`
		}
		runFeatureJSON(t, &published,
			"workspace", "contract", "publish", ready.WorkspaceDir,
			"--contract", "api-contract",
			"--version", "v2",
			"--producer-merge-unit", "foundation:story-a",
			"--producer-commit", "producer-commit-v2",
			"--command-result", "go test ./...=passed",
			"--json",
		)
		if published.Status != "published" || published.ArtifactHash == "" {
			t.Fatalf("second publish = %+v", published)
		}

		blocked := queueContractGateSmoke(t, ready)
		if blocked.Status != "blocked" || !workspaceSmokeHasBlockingCondition(blocked.BlockingConditions, "stale_contract") {
			t.Fatalf("stale contract queue result = %+v", blocked)
		}
	})

	t.Run("stale gate inputs block queue", func(t *testing.T) {
		ready := prepareContractGateQueueSmoke(t)
		appendWorkspaceSmokeRefresh(t, ready.WorkspaceDir, ready.ConsumerClaim.MergeUnitID, ready.ConsumerAttempt.AttemptID, ready.ConsumerAttempt.Branch, ready.ConsumerAttempt.Worktree, ready.BaseSHA, "consumer-base-sha-v2", ready.HeadSHA, "head-sha-v2")

		blocked := queueContractGateSmokeWithSHAs(t, ready, "head-sha-v2", "consumer-base-sha-v2")
		if blocked.Status != "blocked" ||
			!workspaceSmokeHasBlockingCondition(blocked.BlockingConditions, "stale_gate_evaluation") ||
			!workspaceSmokeHasGateBlockingCondition(blocked.BlockingConditions, "test", workspacepkg.GateStatusRerunRequired) {
			t.Fatalf("stale gate queue result = %+v", blocked)
		}
	})
}

func TestWorkspaceExternalRefreshRecoverySmokeCommandJSON(t *testing.T) {
	t.Run("reports external intent result sources and recovery actions", func(t *testing.T) {
		workspaceDir := workspaceWithPlanLocks(t)
		var validated struct {
			Status string `json:"status"`
		}
		runFeatureJSON(t, &validated, "workspace", "validate", workspaceDir, "--write-lock", "--json")
		if validated.Status != "valid" {
			t.Fatalf("validate = %+v", validated)
		}

		claim := claimWorkspaceForSmoke(t, workspaceDir, "worker-a")
		attempt := startWorkspaceAttemptForSmoke(t, workspaceDir, claim, "base-sha-cli")
		approvalID := grantWorkspaceSmokeApprovalForAction(t, workspaceDir, claim, attempt, workspacepkg.ExternalActionPush, "feature/test", "head-sha-cli", "base-sha-cli", 2)

		var planned struct {
			Status string `json:"status"`
			Plan   struct {
				ProviderCommand string   `json:"provider_command"`
				IntentCommand   string   `json:"intent_command"`
				Commands        []string `json:"commands"`
			} `json:"plan"`
			Intent struct {
				Action string `json:"action"`
				Target string `json:"target"`
			} `json:"intent"`
		}
		runFeatureJSON(t, &planned,
			"workspace", "external", "plan", workspaceDir,
			"--merge-unit", claim.MergeUnitID,
			"--attempt", attempt.AttemptID,
			"--agent", claim.AgentID,
			"--lease", claim.LeaseID,
			"--approval", approvalID,
			"--action", workspacepkg.ExternalActionPush,
			"--branch", "feature/test",
			"--head-sha", "head-sha-cli",
			"--base-sha", "base-sha-cli",
			"--json",
		)
		if planned.Status != "planned" || planned.Plan.ProviderCommand == "" || !strings.Contains(planned.Plan.IntentCommand, "feature workspace external intent reserve") {
			t.Fatalf("provider plan = %+v", planned)
		}
		if planned.Intent.Action != workspacepkg.ExternalActionPush || planned.Intent.Target != "branch:feature/test" {
			t.Fatalf("planned intent = %+v", planned.Intent)
		}

		reserved := reserveWorkspaceSmokeExternalIntent(t, workspaceDir, claim, attempt, approvalID, workspacepkg.ExternalActionPush, "feature/test", "head-sha-cli", "base-sha-cli")
		status := workspaceSmokeExternalStatus(t, workspaceDir)
		report := findWorkspaceSmokeExternalIntentReport(t, status.ExternalIntents, reserved.Intent.IntentID)
		if report.ResultSource != workspacepkg.ExternalIntentResultSourceUnresolved ||
			report.Status != "unresolved" ||
			report.RequiredAction != "record_result" ||
			report.Accepted {
			t.Fatalf("unresolved report = %+v", report)
		}
		if !workspaceSmokeHasFrozenResource(status.FrozenResources, reserved.Intent.IntentID, "provider_target:push:branch:feature/test") ||
			!workspaceSmokeHasBlocker(status.Blockers, "frozen_resource", "record_result", reserved.Intent.IntentID) {
			t.Fatalf("unresolved freezes = frozen %+v blockers %+v", status.FrozenResources, status.Blockers)
		}

		recordWorkspaceSmokeExternalResult(t, workspaceDir, claim, attempt, reserved.Intent.IntentID, workspacepkg.ExternalResultSucceeded, "provider completed")
		status = workspaceSmokeExternalStatus(t, workspaceDir)
		report = findWorkspaceSmokeExternalIntentReport(t, status.ExternalIntents, reserved.Intent.IntentID)
		if report.ResultSource != workspacepkg.ExternalIntentResultSourceTool ||
			report.ResultStatus != workspacepkg.ExternalResultSucceeded ||
			!report.Accepted ||
			report.RequiredAction != "" ||
			workspaceSmokeHasFrozenResource(status.FrozenResources, reserved.Intent.IntentID, "provider_target:push:branch:feature/test") {
			t.Fatalf("tool-proven report = %+v frozen=%+v", report, status.FrozenResources)
		}

		operatorApprovalID := grantWorkspaceSmokeApprovalForAction(t, workspaceDir, claim, attempt, workspacepkg.ExternalActionOpenPR, "feature/operator", "head-sha-operator", "base-sha-cli", 1)
		operatorIntent := reserveWorkspaceSmokeExternalIntent(t, workspaceDir, claim, attempt, operatorApprovalID, workspacepkg.ExternalActionOpenPR, "feature/operator", "head-sha-operator", "base-sha-cli")
		var released struct {
			Status string `json:"status"`
		}
		runFeatureJSON(t, &released,
			"workspace", "release", workspaceDir,
			"--agent", claim.AgentID,
			"--lease", claim.LeaseID,
			"--json",
		)
		if released.Status != "released" {
			t.Fatalf("release = %+v", released)
		}
		status = workspaceSmokeExternalStatus(t, workspaceDir)
		report = findWorkspaceSmokeExternalIntentReport(t, status.ExternalIntents, operatorIntent.Intent.IntentID)
		if report.ResultSource != workspacepkg.ExternalIntentResultSourceUnresolved ||
			report.Status != "unresolved" ||
			report.RequiredAction != "operator_reconcile" ||
			report.Accepted {
			t.Fatalf("released unresolved report = %+v", report)
		}

		var reconciled struct {
			Status string `json:"status"`
			Result struct {
				Status   string `json:"status"`
				Accepted bool   `json:"accepted"`
				Operator string `json:"operator"`
			} `json:"result"`
		}
		runFeatureJSON(t, &reconciled,
			"workspace", "external", "intent", "reconcile", workspaceDir,
			"--intent", operatorIntent.Intent.IntentID,
			"--operator", "operator-a",
			"--details", "operator confirmed outcome",
			"--json",
		)
		if reconciled.Status != "reconciled" ||
			reconciled.Result.Status != workspacepkg.ExternalResultReconciledByOperator ||
			!reconciled.Result.Accepted ||
			reconciled.Result.Operator != "operator-a" {
			t.Fatalf("reconcile = %+v", reconciled)
		}
		status = workspaceSmokeExternalStatus(t, workspaceDir)
		report = findWorkspaceSmokeExternalIntentReport(t, status.ExternalIntents, operatorIntent.Intent.IntentID)
		if report.ResultSource != workspacepkg.ExternalIntentResultSourceOperator ||
			report.ResultStatus != workspacepkg.ExternalResultReconciledByOperator ||
			report.Operator != "operator-a" ||
			!report.Accepted {
			t.Fatalf("operator-reconciled report = %+v", report)
		}

		appendExpiredWorkspaceSmokeLease(t, workspaceDir, claim.MergeUnitID, "lease-expired-smoke", "worker-expired")
		var recovered workspaceSmokeRecoverResult
		runFeatureJSON(t, &recovered, "workspace", "recover", workspaceDir, "--json")
		if recovered.Status != "recovered" || recovered.RecoveredCount != 1 {
			t.Fatalf("recover = %+v", recovered)
		}
		if len(recovered.Actions) != 1 ||
			recovered.Actions[0].Type != workspacepkg.RecoveryActionRecoveredLease ||
			recovered.Actions[0].MergeUnitID != claim.MergeUnitID ||
			recovered.Actions[0].LeaseID != "lease-expired-smoke" ||
			recovered.Actions[0].Status != "recovered" {
			t.Fatalf("recover actions = %+v", recovered.Actions)
		}
		report = findWorkspaceSmokeExternalIntentReport(t, recovered.ExternalIntents, operatorIntent.Intent.IntentID)
		if report.ResultSource != workspacepkg.ExternalIntentResultSourceOperator {
			t.Fatalf("recover external report = %+v", report)
		}
	})

	t.Run("refresh invalidates approvals and queue state", func(t *testing.T) {
		ready := prepareContractGateQueueSmoke(t)
		queued := queueContractGateSmoke(t, ready)
		if queued.Status != "queued" || queued.Queue == nil {
			t.Fatalf("initial queue = %+v", queued)
		}

		before := workspaceSmokeExternalStatus(t, ready.WorkspaceDir)
		if len(before.MergeQueue) != 1 || before.MergeQueue[0].QueueID != queued.Queue.QueueID {
			t.Fatalf("pre-refresh queue status = %+v", before.MergeQueue)
		}
		beforeConsumer := findWorkspaceStatusUnitForSmoke(t, before.MergeUnits, ready.ConsumerClaim.MergeUnitID)
		if beforeConsumer.MergeQueue == nil || beforeConsumer.MergeQueue.QueueID != queued.Queue.QueueID {
			t.Fatalf("pre-refresh consumer queue = %+v", beforeConsumer.MergeQueue)
		}

		appendWorkspaceSmokeRefresh(t, ready.WorkspaceDir, ready.ConsumerClaim.MergeUnitID, ready.ConsumerAttempt.AttemptID, ready.ConsumerAttempt.Branch, ready.ConsumerAttempt.Worktree, ready.BaseSHA, "consumer-base-sha-v2", ready.HeadSHA, "head-sha-v2")

		after := workspaceSmokeExternalStatus(t, ready.WorkspaceDir)
		if len(after.MergeQueue) != 0 {
			t.Fatalf("post-refresh queue should be empty: %+v", after.MergeQueue)
		}
		afterConsumer := findWorkspaceStatusUnitForSmoke(t, after.MergeUnits, ready.ConsumerClaim.MergeUnitID)
		if afterConsumer.MergeQueue != nil {
			t.Fatalf("post-refresh consumer queue = %+v", afterConsumer.MergeQueue)
		}
		if len(afterConsumer.Approvals) != 1 ||
			afterConsumer.Approvals[0].ApprovalID != ready.ApprovalID ||
			afterConsumer.Approvals[0].Status != "stale" ||
			!stringSliceContains(afterConsumer.Approvals[0].StaleInputs, "base") ||
			!stringSliceContains(afterConsumer.Approvals[0].StaleInputs, "head") {
			t.Fatalf("post-refresh approvals = %+v", afterConsumer.Approvals)
		}

		var approvalCheck struct {
			Status    string `json:"status"`
			Approvals []struct {
				ApprovalID string `json:"approval_id"`
			} `json:"approvals"`
		}
		runFeatureJSON(t, &approvalCheck,
			"workspace", "approve", "check", ready.WorkspaceDir,
			"--merge-unit", ready.ConsumerClaim.MergeUnitID,
			"--attempt", ready.ConsumerAttempt.AttemptID,
			"--action", workspacepkg.ExternalActionMerge,
			"--branch", ready.ConsumerAttempt.Branch,
			"--head-sha", "head-sha-v2",
			"--base-sha", "consumer-base-sha-v2",
			"--json",
		)
		if approvalCheck.Status != "denied" || len(approvalCheck.Approvals) != 0 {
			t.Fatalf("stale approval check = %+v", approvalCheck)
		}

		blocked := queueContractGateSmokeWithSHAs(t, ready, "head-sha-v2", "consumer-base-sha-v2")
		if blocked.Status != "blocked" ||
			!workspaceSmokeHasBlockingCondition(blocked.BlockingConditions, "stale_approval") ||
			!workspaceSmokeHasBlockingCondition(blocked.BlockingConditions, "stale_gate_evaluation") ||
			!workspaceSmokeHasGateBlockingCondition(blocked.BlockingConditions, "test", workspacepkg.GateStatusRerunRequired) {
			t.Fatalf("post-refresh queue result = %+v", blocked)
		}
	})
}

type workspaceSmokeClaim struct {
	Status      string `json:"status"`
	MergeUnitID string `json:"merge_unit_id"`
	LeaseID     string `json:"lease_id"`
	AgentID     string `json:"agent_id"`
}

type workspaceSmokeAttempt struct {
	Status    string `json:"status"`
	AttemptID string `json:"attempt_id"`
	Branch    string `json:"branch"`
	Worktree  string `json:"worktree"`
	BaseSHA   string `json:"base_sha"`
}

type workspaceSmokeGateStatus struct {
	Gate   string `json:"gate"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type workspaceSmokeEvaluation struct {
	Status     string                     `json:"status"`
	InputHash  string                     `json:"input_hash"`
	OutputHash string                     `json:"output_hash"`
	Gates      []workspaceSmokeGateStatus `json:"gates"`
}

type workspaceSmokeReady struct {
	WorkspaceDir    string
	ConsumerClaim   workspaceSmokeClaim
	ConsumerAttempt workspaceSmokeAttempt
	ApprovalID      string
	Evaluation      workspaceSmokeEvaluation
	HeadSHA         string
	BaseSHA         string
}

type workspaceSmokeBlockingCondition struct {
	Type           string `json:"type"`
	Status         string `json:"status"`
	Gate           string `json:"gate"`
	RequiredAction string `json:"required_action"`
}

type workspaceSmokeQueueResult struct {
	Status string `json:"status"`
	Queue  *struct {
		QueueID        string `json:"queue_id"`
		MergeUnitID    string `json:"merge_unit_id"`
		AttemptID      string `json:"attempt_id"`
		ApprovalID     string `json:"approval_id"`
		Branch         string `json:"branch"`
		HeadSHA        string `json:"head_sha"`
		BaseSHA        string `json:"base_sha"`
		Position       int    `json:"position"`
		GateInputHash  string `json:"gate_input_hash"`
		GateOutputHash string `json:"gate_output_hash"`
	} `json:"queue"`
	BlockingConditions []workspaceSmokeBlockingCondition `json:"blocking_conditions"`
}

type workspaceSmokeExternalIntentResult struct {
	Status string `json:"status"`
	Intent struct {
		IntentID string `json:"intent_id"`
		Action   string `json:"action"`
		Target   string `json:"target"`
		Status   string `json:"status"`
	} `json:"intent"`
}

type workspaceSmokeStatusUnit struct {
	ID               string `json:"id"`
	ContractBindings []struct {
		ContractID string `json:"contract_id"`
		Status     string `json:"status"`
	} `json:"contract_bindings"`
	Approvals []struct {
		ApprovalID  string   `json:"approval_id"`
		Status      string   `json:"status"`
		StaleInputs []string `json:"stale_inputs"`
	} `json:"approvals"`
	MergeQueue *struct {
		QueueID  string `json:"queue_id"`
		Position int    `json:"position"`
	} `json:"merge_queue"`
}

type workspaceSmokeExternalIntentReport struct {
	IntentID       string `json:"intent_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	AttemptID      string `json:"attempt_id"`
	Action         string `json:"action"`
	Target         string `json:"target"`
	Status         string `json:"status"`
	ResultStatus   string `json:"result_status"`
	ResultSource   string `json:"result_source"`
	Accepted       bool   `json:"accepted"`
	PolicyAccepted bool   `json:"policy_accepted"`
	Operator       string `json:"operator"`
	RequiredAction string `json:"required_action"`
}

type workspaceSmokeFrozenResource struct {
	Resource       string `json:"resource"`
	IntentID       string `json:"intent_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	AttemptID      string `json:"attempt_id"`
	Action         string `json:"action"`
	Target         string `json:"target"`
	Status         string `json:"status"`
	RequiredAction string `json:"required_action"`
}

type workspaceSmokeBlockerGroup struct {
	Type           string `json:"type"`
	RequiredAction string `json:"required_action"`
	Conditions     []struct {
		Resource string `json:"resource"`
		IntentID string `json:"intent_id"`
	} `json:"conditions"`
}

type workspaceSmokeExternalStatusResult struct {
	Status          string                                `json:"status"`
	Blockers        []workspaceSmokeBlockerGroup          `json:"blockers"`
	FrozenResources []workspaceSmokeFrozenResource        `json:"frozen_resources"`
	ExternalIntents []workspaceSmokeExternalIntentReport  `json:"external_intents"`
	MergeQueue      []workspaceSmokeMergeQueueStatusEntry `json:"merge_queue"`
	MergeUnits      []workspaceSmokeStatusUnit            `json:"merge_units"`
}

type workspaceSmokeMergeQueueStatusEntry struct {
	QueueID     string `json:"queue_id"`
	MergeUnitID string `json:"merge_unit_id"`
	Position    int    `json:"position"`
}

type workspaceSmokeRecoverResult struct {
	Status          string                               `json:"status"`
	RecoveredCount  int                                  `json:"recovered_count"`
	Actions         []workspaceSmokeRecoveryAction       `json:"actions"`
	ExternalIntents []workspaceSmokeExternalIntentReport `json:"external_intents"`
}

type workspaceSmokeRecoveryAction struct {
	Type        string `json:"type"`
	MergeUnitID string `json:"merge_unit_id"`
	LeaseID     string `json:"lease_id"`
	AgentID     string `json:"agent_id"`
	Status      string `json:"status"`
}

func prepareContractGateQueueSmoke(t *testing.T) workspaceSmokeReady {
	t.Helper()
	workspaceDir := workspaceWithContractPlanLocks(t)
	var validated struct {
		Status string `json:"status"`
	}
	runFeatureJSON(t, &validated, "workspace", "validate", workspaceDir, "--write-lock", "--json")
	if validated.Status != "valid" {
		t.Fatalf("validate = %+v", validated)
	}

	producerClaim := claimWorkspaceForSmoke(t, workspaceDir, "worker-producer")
	if producerClaim.MergeUnitID != "foundation:story-a" {
		t.Fatalf("producer claim = %+v", producerClaim)
	}
	producerAttempt := startWorkspaceAttemptForSmoke(t, workspaceDir, producerClaim, "producer-base-sha-v1")
	transitionWorkspaceForSmoke(t, workspaceDir, producerClaim, producerAttempt, "pending", "in_progress", "worktree="+producerAttempt.Worktree)

	var published struct {
		Status              string `json:"status"`
		ProducerMergeUnitID string `json:"producer_merge_unit_id"`
		ProducerCommit      string `json:"producer_commit"`
		ArtifactHash        string `json:"artifact_hash"`
	}
	runFeatureJSON(t, &published,
		"workspace", "contract", "publish", workspaceDir,
		"--contract", "api-contract",
		"--version", "v1",
		"--attempt", producerAttempt.AttemptID,
		"--agent", producerClaim.AgentID,
		"--lease", producerClaim.LeaseID,
		"--producer-commit", "producer-commit-v1",
		"--command-result", "go test ./...=passed",
		"--json",
	)
	if published.Status != "published" || published.ProducerMergeUnitID != producerClaim.MergeUnitID || published.ProducerCommit != "producer-commit-v1" || published.ArtifactHash == "" {
		t.Fatalf("publish = %+v", published)
	}
	transitionWorkspaceForSmoke(t, workspaceDir, producerClaim, producerAttempt, "in_progress", "completed", "commit_sha=producer-commit-v1")

	consumerClaim := claimWorkspaceForSmoke(t, workspaceDir, "worker-consumer")
	if consumerClaim.MergeUnitID != "sources:story-b" {
		t.Fatalf("consumer claim = %+v", consumerClaim)
	}
	consumerAttempt := startWorkspaceAttemptForSmoke(t, workspaceDir, consumerClaim, "consumer-base-sha-v1")

	var beforeBind struct {
		Status   string `json:"status"`
		Bindings []struct {
			ContractID string `json:"contract_id"`
			Status     string `json:"status"`
		} `json:"bindings"`
	}
	runFeatureJSON(t, &beforeBind,
		"workspace", "contract", "check-contracts", workspaceDir,
		"--merge-unit", consumerClaim.MergeUnitID,
		"--attempt", consumerAttempt.AttemptID,
		"--json",
	)
	if beforeBind.Status != "missing" || len(beforeBind.Bindings) != 1 || beforeBind.Bindings[0].Status != "missing" {
		t.Fatalf("check before bind = %+v", beforeBind)
	}

	var bound struct {
		Status       string `json:"status"`
		ArtifactHash string `json:"artifact_hash"`
	}
	runFeatureJSON(t, &bound,
		"workspace", "contract", "bind", workspaceDir,
		"--contract", "api-contract",
		"--merge-unit", consumerClaim.MergeUnitID,
		"--attempt", consumerAttempt.AttemptID,
		"--agent", consumerClaim.AgentID,
		"--lease", consumerClaim.LeaseID,
		"--command-result", "go test ./...=passed",
		"--json",
	)
	if bound.Status != "bound" || bound.ArtifactHash == "" {
		t.Fatalf("bind = %+v", bound)
	}
	assertWorkspaceStatusContractCurrentForSmoke(t, workspaceDir, consumerClaim.MergeUnitID)

	headSHA := "head-sha-v1"
	baseSHA := consumerAttempt.BaseSHA
	appendWorkspaceSmokeRefresh(t, workspaceDir, consumerClaim.MergeUnitID, consumerAttempt.AttemptID, consumerAttempt.Branch, consumerAttempt.Worktree, baseSHA, baseSHA, headSHA, headSHA)
	approvalID := grantWorkspaceSmokeMergeApproval(t, workspaceDir, consumerClaim, consumerAttempt, headSHA, baseSHA)

	evaluation := evaluateWorkspaceSmokeGates(t, workspaceDir, consumerClaim, consumerAttempt)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "contract", workspacepkg.GateStatusPassed)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "merge_approval", workspacepkg.GateStatusPassed)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "review", workspacepkg.GateStatusPending)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "security", workspacepkg.GateStatusPending)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "test", workspacepkg.GateStatusPending)

	for _, gate := range []string{"review", "security", "test"} {
		overrideWorkspaceSmokeGate(t, workspaceDir, consumerClaim, consumerAttempt, evaluation.InputHash, gate, headSHA, baseSHA)
	}
	evaluation = evaluateWorkspaceSmokeGates(t, workspaceDir, consumerClaim, consumerAttempt)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "contract", workspacepkg.GateStatusPassed)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "merge_approval", workspacepkg.GateStatusPassed)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "review", workspacepkg.GateStatusRetainedByOperator)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "security", workspacepkg.GateStatusRetainedByOperator)
	assertWorkspaceSmokeGateStatus(t, evaluation.Gates, "test", workspacepkg.GateStatusRetainedByOperator)

	return workspaceSmokeReady{
		WorkspaceDir:    workspaceDir,
		ConsumerClaim:   consumerClaim,
		ConsumerAttempt: consumerAttempt,
		ApprovalID:      approvalID,
		Evaluation:      evaluation,
		HeadSHA:         headSHA,
		BaseSHA:         baseSHA,
	}
}

func claimWorkspaceForSmoke(t *testing.T, workspaceDir string, agentID string) workspaceSmokeClaim {
	t.Helper()
	var claim workspaceSmokeClaim
	runFeatureJSON(t, &claim, "workspace", "next", workspaceDir, "--agent", agentID, "--claim", "--json")
	if claim.Status != "claimed" || claim.AgentID != agentID || claim.MergeUnitID == "" || claim.LeaseID == "" {
		t.Fatalf("claim = %+v", claim)
	}
	return claim
}

func startWorkspaceAttemptForSmoke(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, baseSHA string) workspaceSmokeAttempt {
	t.Helper()
	var attempt workspaceSmokeAttempt
	runFeatureJSON(t, &attempt,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", claim.AgentID,
		"--lease", claim.LeaseID,
		"--base-sha", baseSHA,
		"--json",
	)
	if attempt.Status != "started" || attempt.AttemptID == "" || attempt.Branch == "" || attempt.Worktree == "" || attempt.BaseSHA != baseSHA {
		t.Fatalf("attempt = %+v", attempt)
	}
	return attempt
}

func transitionWorkspaceForSmoke(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, attempt workspaceSmokeAttempt, from string, to string, evidence string) {
	t.Helper()
	var transitioned struct {
		Status string `json:"status"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	runFeatureJSON(t, &transitioned,
		"workspace", "transition", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", claim.AgentID,
		"--lease", claim.LeaseID,
		"--from", from,
		"--to", to,
		"--evidence", evidence,
		"--json",
	)
	if transitioned.Status != "transitioned" || transitioned.From != from || transitioned.To != to {
		t.Fatalf("transition = %+v", transitioned)
	}
}

func grantWorkspaceSmokeMergeApproval(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, attempt workspaceSmokeAttempt, headSHA string, baseSHA string) string {
	t.Helper()
	return grantWorkspaceSmokeApprovalForAction(t, workspaceDir, claim, attempt, workspacepkg.ExternalActionMerge, attempt.Branch, headSHA, baseSHA, 1)
}

func grantWorkspaceSmokeApprovalForAction(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, attempt workspaceSmokeAttempt, action string, branch string, headSHA string, baseSHA string, maxUses int) string {
	t.Helper()
	var granted struct {
		Status   string `json:"status"`
		Approval struct {
			ApprovalID string `json:"approval_id"`
			Status     string `json:"status"`
		} `json:"approval"`
	}
	runFeatureJSON(t, &granted,
		"workspace", "approve", "grant", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", claim.AgentID,
		"--lease", claim.LeaseID,
		"--action", action,
		"--branch", branch,
		"--head-sha", headSHA,
		"--base-sha", baseSHA,
		"--expires-in", "1h",
		"--max-uses", strconv.Itoa(maxUses),
		"--json",
	)
	if granted.Status != "granted" || granted.Approval.Status != "active" || granted.Approval.ApprovalID == "" {
		t.Fatalf("approval = %+v", granted)
	}
	return granted.Approval.ApprovalID
}

func reserveWorkspaceSmokeExternalIntent(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, attempt workspaceSmokeAttempt, approvalID string, action string, branch string, headSHA string, baseSHA string) workspaceSmokeExternalIntentResult {
	t.Helper()
	var reserved workspaceSmokeExternalIntentResult
	runFeatureJSON(t, &reserved,
		"workspace", "external", "intent", "reserve", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", claim.AgentID,
		"--lease", claim.LeaseID,
		"--approval", approvalID,
		"--action", action,
		"--branch", branch,
		"--head-sha", headSHA,
		"--base-sha", baseSHA,
		"--json",
	)
	if reserved.Status != "reserved" ||
		reserved.Intent.Status != "reserved" ||
		reserved.Intent.IntentID == "" ||
		reserved.Intent.Action != action ||
		reserved.Intent.Target != "branch:"+branch {
		t.Fatalf("external intent reserve = %+v", reserved)
	}
	return reserved
}

func recordWorkspaceSmokeExternalResult(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, attempt workspaceSmokeAttempt, intentID string, status string, details string) {
	t.Helper()
	var recorded struct {
		Status string `json:"status"`
		Result struct {
			Status   string `json:"status"`
			Accepted bool   `json:"accepted"`
			Details  string `json:"details"`
		} `json:"result"`
	}
	runFeatureJSON(t, &recorded,
		"workspace", "external", "intent", "result", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", claim.AgentID,
		"--lease", claim.LeaseID,
		"--intent", intentID,
		"--status", status,
		"--details", details,
		"--json",
	)
	if recorded.Status != "recorded" || recorded.Result.Status != status || !recorded.Result.Accepted || recorded.Result.Details != details {
		t.Fatalf("external intent result = %+v", recorded)
	}
}

func evaluateWorkspaceSmokeGates(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, attempt workspaceSmokeAttempt) workspaceSmokeEvaluation {
	t.Helper()
	var evaluation workspaceSmokeEvaluation
	runFeatureJSON(t, &evaluation,
		"workspace", "evaluate-gates", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", claim.AgentID,
		"--lease", claim.LeaseID,
		"--json",
	)
	if evaluation.Status != "recorded" || evaluation.InputHash == "" || evaluation.OutputHash == "" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	return evaluation
}

func overrideWorkspaceSmokeGate(t *testing.T, workspaceDir string, claim workspaceSmokeClaim, attempt workspaceSmokeAttempt, inputHash string, gate string, headSHA string, baseSHA string) {
	t.Helper()
	var override struct {
		Status   string `json:"status"`
		Override struct {
			Gate   string `json:"gate"`
			Status string `json:"status"`
		} `json:"override"`
	}
	runFeatureJSON(t, &override,
		"workspace", "gate", "override", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--gate", gate,
		"--status", workspacepkg.GateStatusRetainedByOperator,
		"--reason", "operator accepted smoke readiness",
		"--input-hash", inputHash,
		"--head-sha", headSHA,
		"--base-sha", baseSHA,
		"--operator", "operator-a",
		"--expires-in", "1h",
		"--json",
	)
	if override.Status != "recorded" || override.Override.Gate != gate || override.Override.Status != workspacepkg.GateStatusRetainedByOperator {
		t.Fatalf("override = %+v", override)
	}
}

func queueContractGateSmoke(t *testing.T, ready workspaceSmokeReady) workspaceSmokeQueueResult {
	t.Helper()
	return queueContractGateSmokeWithSHAs(t, ready, ready.HeadSHA, ready.BaseSHA)
}

func queueContractGateSmokeWithSHAs(t *testing.T, ready workspaceSmokeReady, headSHA string, baseSHA string) workspaceSmokeQueueResult {
	t.Helper()
	var result workspaceSmokeQueueResult
	runFeatureJSON(t, &result,
		"workspace", "queue", "enter", ready.WorkspaceDir,
		"--merge-unit", ready.ConsumerClaim.MergeUnitID,
		"--attempt", ready.ConsumerAttempt.AttemptID,
		"--agent", ready.ConsumerClaim.AgentID,
		"--lease", ready.ConsumerClaim.LeaseID,
		"--approval", ready.ApprovalID,
		"--branch", ready.ConsumerAttempt.Branch,
		"--head-sha", headSHA,
		"--base-sha", baseSHA,
		"--json",
	)
	return result
}

func appendWorkspaceSmokeRefresh(t *testing.T, workspaceDir string, mergeUnitID string, attemptID string, branch string, worktree string, oldBase string, newBase string, preHead string, postHead string) {
	t.Helper()
	revisions, err := workspacepkg.ResourceRevisions(workspaceDir)
	if err != nil {
		t.Fatalf("ResourceRevisions: %v", err)
	}
	refreshResource := workspacepkg.RefreshResource(mergeUnitID + ":" + attemptID)
	readSet := map[string]int{refreshResource: revisions[refreshResource]}
	writeSet := []string{refreshResource}
	inputChanges := []any{}
	if oldBase != "" && newBase != "" && oldBase != newBase {
		resource := workspacepkg.RefreshInputResource(mergeUnitID, attemptID, "base")
		readSet[resource] = revisions[resource]
		writeSet = append(writeSet, resource)
		inputChanges = append(inputChanges, map[string]any{
			"input":     "base",
			"old_value": oldBase,
			"new_value": newBase,
			"resource":  resource,
		})
	}
	if preHead != "" && postHead != "" && preHead != postHead {
		resource := workspacepkg.RefreshInputResource(mergeUnitID, attemptID, "head")
		readSet[resource] = revisions[resource]
		writeSet = append(writeSet, resource)
		inputChanges = append(inputChanges, map[string]any{
			"input":     "head",
			"old_value": preHead,
			"new_value": postHead,
			"resource":  resource,
		})
	}
	payload := map[string]any{
		"merge_unit_id": mergeUnitID,
		"attempt_id":    attemptID,
		"status":        workspacepkg.RefreshStatusSucceeded,
		"evidence_path": "state/refresh-smoke.json",
		"branch":        branch,
		"worktree":      worktree,
		"old_base":      oldBase,
		"new_base":      newBase,
		"pre_head":      preHead,
		"post_head":     postHead,
		"backup_ref":    "backup/smoke",
	}
	if len(inputChanges) > 0 {
		payload["input_changes"] = inputChanges
	}
	if _, err := workspacepkg.AppendEvent(workspacepkg.AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         workspacepkg.EventBranchRefreshRecorded,
		Payload:      payload,
		ReadSet:      readSet,
		WriteSet:     writeSet,
		Now:          fixedFeatureTime("2026-06-17T10:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent refresh: %v", err)
	}
}

func appendExpiredWorkspaceSmokeLease(t *testing.T, workspaceDir string, mergeUnitID string, leaseID string, agentID string) {
	t.Helper()
	if _, err := workspacepkg.AppendEvent(workspacepkg.AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         workspacepkg.EventLeaseGranted,
		Payload: map[string]any{
			"merge_unit_id":    mergeUnitID,
			"lease_id":         leaseID,
			"agent_id":         agentID,
			"lease_expires_at": "2000-01-01T00:01:00Z",
		},
		WriteSet: []string{
			workspacepkg.LeaseResource(mergeUnitID),
			workspacepkg.MergeUnitResource(mergeUnitID),
		},
		Now: fixedFeatureTime("2000-01-01T00:00:00Z"),
	}); err != nil {
		t.Fatalf("AppendEvent expired lease: %v", err)
	}
}

func workspaceSmokeExternalStatus(t *testing.T, workspaceDir string) workspaceSmokeExternalStatusResult {
	t.Helper()
	var status workspaceSmokeExternalStatusResult
	runFeatureJSON(t, &status, "workspace", "status", workspaceDir, "--json")
	if status.Status != "ok" {
		t.Fatalf("workspace status = %+v", status)
	}
	return status
}

func assertWorkspaceStatusContractCurrentForSmoke(t *testing.T, workspaceDir string, mergeUnitID string) {
	t.Helper()
	var status struct {
		MergeUnits []workspaceSmokeStatusUnit `json:"merge_units"`
	}
	runFeatureJSON(t, &status, "workspace", "status", workspaceDir, "--json")
	unit := findWorkspaceStatusUnitForSmoke(t, status.MergeUnits, mergeUnitID)
	if len(unit.ContractBindings) != 1 || unit.ContractBindings[0].ContractID != "api-contract" || unit.ContractBindings[0].Status != "current" {
		t.Fatalf("status contract bindings = %+v", unit.ContractBindings)
	}
}

func findWorkspaceStatusUnitForSmoke(t *testing.T, units []workspaceSmokeStatusUnit, mergeUnitID string) workspaceSmokeStatusUnit {
	t.Helper()
	for _, unit := range units {
		if unit.ID == mergeUnitID {
			return unit
		}
	}
	t.Fatalf("merge unit %s missing from status: %+v", mergeUnitID, units)
	return workspaceSmokeStatusUnit{}
}

func assertWorkspaceSmokeGateStatus(t *testing.T, gates []workspaceSmokeGateStatus, gate string, status string) {
	t.Helper()
	for _, item := range gates {
		if item.Gate == gate {
			if item.Status != status {
				t.Fatalf("gate %s status = %s, want %s in %+v", gate, item.Status, status, gates)
			}
			return
		}
	}
	t.Fatalf("gate %s missing from %+v", gate, gates)
}

func findWorkspaceSmokeExternalIntentReport(t *testing.T, reports []workspaceSmokeExternalIntentReport, intentID string) workspaceSmokeExternalIntentReport {
	t.Helper()
	for _, report := range reports {
		if report.IntentID == intentID {
			return report
		}
	}
	t.Fatalf("external intent %s missing from %+v", intentID, reports)
	return workspaceSmokeExternalIntentReport{}
}

func workspaceSmokeHasFrozenResource(freezes []workspaceSmokeFrozenResource, intentID string, resource string) bool {
	for _, freeze := range freezes {
		if freeze.IntentID == intentID && freeze.Resource == resource {
			return true
		}
	}
	return false
}

func workspaceSmokeHasBlocker(groups []workspaceSmokeBlockerGroup, groupType string, requiredAction string, intentID string) bool {
	for _, group := range groups {
		if group.Type != groupType || group.RequiredAction != requiredAction {
			continue
		}
		for _, condition := range group.Conditions {
			if condition.IntentID == intentID {
				return true
			}
		}
	}
	return false
}

func workspaceSmokeHasBlockingCondition(conditions []workspaceSmokeBlockingCondition, conditionType string) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}

func workspaceSmokeHasGateBlockingCondition(conditions []workspaceSmokeBlockingCondition, gate string, status string) bool {
	for _, condition := range conditions {
		if condition.Type == "gate" && condition.Gate == gate && condition.Status == status {
			return true
		}
	}
	return false
}

func TestWorkspaceCLISmokeRebuildsStatusFromJournal(t *testing.T) {
	workspaceDir := workspaceWithTwoPlanLocks(t)
	stdout, stderr, err := runFeature(t, "workspace", "validate", workspaceDir, "--write-lock", "--json")
	if err != nil {
		t.Fatalf("feature workspace validate failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var validated struct {
		Status string `json:"status"`
		Lock   struct {
			Plans []struct {
				ID string `json:"id"`
			} `json:"plans"`
			MergeUnits []struct {
				ID           string   `json:"id"`
				Dependencies []string `json:"dependencies,omitempty"`
			} `json:"merge_units"`
		} `json:"lock"`
	}
	if err := json.Unmarshal([]byte(stdout), &validated); err != nil {
		t.Fatalf("validate stdout is not JSON: %v\n%s", err, stdout)
	}
	if validated.Status != "valid" || len(validated.Lock.Plans) != 2 || len(validated.Lock.MergeUnits) != 2 {
		t.Fatalf("validated workspace = %+v", validated)
	}

	stdout, stderr, err = runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var initialStatus struct {
		Ready      []string `json:"ready"`
		Blocked    []string `json:"blocked"`
		MergeUnits []struct {
			ID        string   `json:"id"`
			Status    string   `json:"status"`
			BlockedBy []string `json:"blocked_by,omitempty"`
		} `json:"merge_units"`
	}
	if err := json.Unmarshal([]byte(stdout), &initialStatus); err != nil {
		t.Fatalf("initial status stdout is not JSON: %v\n%s", err, stdout)
	}
	if strings.Join(initialStatus.Ready, ",") != "foundation:story-a" || strings.Join(initialStatus.Blocked, ",") != "sources:story-b" {
		t.Fatalf("initial ready/blocked = ready %+v blocked %+v", initialStatus.Ready, initialStatus.Blocked)
	}

	stdout, stderr, err = runFeature(t, "workspace", "next", workspaceDir, "--agent", "worker-a", "--claim", "--json")
	if err != nil {
		t.Fatalf("feature workspace next failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var claim struct {
		MergeUnitID string `json:"merge_unit_id"`
		LeaseID     string `json:"lease_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claim); err != nil {
		t.Fatalf("claim stdout is not JSON: %v\n%s", err, stdout)
	}
	if claim.MergeUnitID != "foundation:story-a" || claim.LeaseID == "" {
		t.Fatalf("claim = %+v", claim)
	}

	stdout, stderr, err = runFeature(t,
		"workspace", "attempt", "start", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--base-sha", "base-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace attempt start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var attempt struct {
		AttemptID string `json:"attempt_id"`
		Worktree  string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(stdout), &attempt); err != nil {
		t.Fatalf("attempt stdout is not JSON: %v\n%s", err, stdout)
	}
	if attempt.AttemptID == "" || attempt.Worktree == "" {
		t.Fatalf("attempt = %+v", attempt)
	}

	if stdout, stderr, err = runFeature(t,
		"workspace", "transition", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--from", "pending",
		"--to", "in_progress",
		"--evidence", "worktree="+attempt.Worktree,
		"--json",
	); err != nil {
		t.Fatalf("feature workspace transition start failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	stdout, stderr, err = runFeature(t,
		"workspace", "transition", workspaceDir,
		"--merge-unit", claim.MergeUnitID,
		"--attempt", attempt.AttemptID,
		"--agent", "worker-a",
		"--lease", claim.LeaseID,
		"--from", "in_progress",
		"--to", "completed",
		"--evidence", "commit_sha=commit-sha-cli",
		"--json",
	)
	if err != nil {
		t.Fatalf("feature workspace transition complete failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var completed struct {
		Status    string `json:"status"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal([]byte(stdout), &completed); err != nil {
		t.Fatalf("completed transition stdout is not JSON: %v\n%s", err, stdout)
	}
	if completed.Status != "transitioned" || completed.EventType != "merge_unit.completed" {
		t.Fatalf("completed transition = %+v", completed)
	}

	viewPath := filepath.Join(workspaceDir, "state", "scheduler.view.json")
	if err := os.Remove(viewPath); err != nil {
		t.Fatalf("remove scheduler view: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "state", "events.jsonl")); err != nil {
		t.Fatalf("expected workspace event journal: %v", err)
	}

	stdout, stderr, err = runFeature(t, "workspace", "status", workspaceDir, "--json")
	if err != nil {
		t.Fatalf("feature workspace status rebuild failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var rebuilt struct {
		Ready      []string       `json:"ready"`
		Counts     map[string]int `json:"counts"`
		MergeUnits []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"merge_units"`
	}
	if err := json.Unmarshal([]byte(stdout), &rebuilt); err != nil {
		t.Fatalf("rebuilt status stdout is not JSON: %v\n%s", err, stdout)
	}
	if _, err := os.Stat(viewPath); err != nil {
		t.Fatalf("expected rebuilt scheduler view: %v", err)
	}
	if rebuilt.Counts["completed"] != 1 || rebuilt.Counts["pending"] != 1 || strings.Join(rebuilt.Ready, ",") != "sources:story-b" {
		t.Fatalf("rebuilt status = %+v", rebuilt)
	}
	statusByID := map[string]string{}
	for _, unit := range rebuilt.MergeUnits {
		statusByID[unit.ID] = unit.Status
	}
	if statusByID["foundation:story-a"] != "completed" || statusByID["sources:story-b"] != "pending" {
		t.Fatalf("rebuilt merge unit statuses = %+v", statusByID)
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

func TestInstallSkillsPlanAllCommandJSONDoesNotWrite(t *testing.T) {
	stage := t.TempDir()
	stdout, stderr, err := runFeature(t, "install-skills", "--plan", "--target", "all", "--install-root", stage, "--json")
	if err != nil {
		t.Fatalf("feature install-skills plan all failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var result struct {
		Schema    int    `json:"schema"`
		Operation string `json:"operation"`
		Kind      string `json:"kind"`
		Targets   map[string]struct {
			Files []map[string]json.RawMessage `json:"files"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("install-skills stdout is not JSON: %v\n%s", err, stdout)
	}
	if result.Schema != 1 || result.Operation != "plan" || result.Kind != "delegated" {
		t.Fatalf("install-skills metadata = %+v", result)
	}
	for _, target := range []string{"tools", "codex", "claude"} {
		files, ok := result.Targets[target]
		if !ok || len(files.Files) == 0 {
			t.Fatalf("target %s missing from plan result: %+v", target, result.Targets)
		}
		for _, file := range files.Files {
			pathRaw, ok := file["path"]
			if !ok {
				t.Fatalf("target %s file missing path: %+v", target, file)
			}
			var path string
			if err := json.Unmarshal(pathRaw, &path); err != nil {
				t.Fatalf("target %s path is not a string: %v", target, err)
			}
			if !strings.HasPrefix(path, stage+string(os.PathSeparator)) {
				t.Fatalf("target %s planned path outside install root: %q", target, path)
			}
			if _, ok := file["sha256"]; ok {
				t.Fatalf("plan target %s should omit sha256: %+v", target, file)
			}
		}
	}
	if len(result.Targets) != 3 {
		t.Fatalf("unexpected target set: %+v", result.Targets)
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatalf("read install root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("plan should not write under install root: %+v", entries)
	}
}

func fixedFeatureTime(value string) func() time.Time {
	return func() time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			panic(err)
		}
		return parsed
	}
}

func workspaceWithPlanLocks(t *testing.T) string {
	t.Helper()
	workspaceDir := t.TempDir()
	planDir := filepath.Join(workspaceDir, "plans", "foundation")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := `{"schema_version":1,"manifest_id":"foundation","title":"Foundation","epics":[{"id":"epic-foundation","number":1,"name":"Epic","features":[{"id":"feature-foundation","number":1,"name":"Feature","stories":[{"id":"story-a","number":1,"name":"Story A"}]}]}],"merge_units":[{"id":"story-a","name":"Story A","story_ids":["story-a"]}]}`
	if err := os.WriteFile(filepath.Join(planDir, "feature.plan.lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: 1
id: workspace-a
repo: .
base_ref: workspace-orchestration
remote: origin
plans:
  - id: foundation
    path: plans/foundation
`
	if err := os.WriteFile(filepath.Join(workspaceDir, "feature.workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspaceDir
}

func writeWorkspacePlanLockMetadata(t *testing.T, workspaceDir string, baseRef string, remote string) {
	t.Helper()
	path := filepath.Join(workspaceDir, "plans", "foundation", "feature.plan.lock.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lock map[string]any
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if baseRef == "" {
		delete(lock, "base_ref")
	} else {
		lock["base_ref"] = baseRef
	}
	if remote == "" {
		delete(lock, "remote")
	} else {
		lock["remote"] = remote
	}
	b, err = json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func workspaceWithTwoPlanLocks(t *testing.T) string {
	t.Helper()
	workspaceDir := t.TempDir()
	plans := map[string]string{
		"foundation": `{"schema_version":1,"manifest_id":"foundation","title":"Foundation","epics":[{"id":"epic-foundation","number":1,"name":"Epic","features":[{"id":"feature-foundation","number":1,"name":"Feature","stories":[{"id":"story-a","number":1,"name":"Story A"}]}]}],"merge_units":[{"id":"story-a","name":"Story A","story_ids":["story-a"]}]}`,
		"sources":    `{"schema_version":1,"manifest_id":"sources","title":"Sources","epics":[{"id":"epic-sources","number":1,"name":"Epic","features":[{"id":"feature-sources","number":1,"name":"Feature","stories":[{"id":"story-b","number":1,"name":"Story B"}]}]}],"merge_units":[{"id":"story-b","name":"Story B","story_ids":["story-b"]}]}`,
	}
	for id, lock := range plans {
		planDir := filepath.Join(workspaceDir, "plans", id)
		if err := os.MkdirAll(planDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(planDir, "feature.plan.lock.json"), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `schema_version: 1
id: workspace-a
repo: .
base_ref: workspace-orchestration
remote: origin
plans:
  - id: foundation
    path: plans/foundation
  - id: sources
    path: plans/sources
dependencies:
  - before: foundation:story-a
    after: sources:story-b
`
	if err := os.WriteFile(filepath.Join(workspaceDir, "feature.workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspaceDir
}

func workspaceWithContractPlanLocks(t *testing.T) string {
	t.Helper()
	workspaceDir := workspaceWithTwoPlanLocks(t)
	manifest := `schema_version: 1
id: workspace-a
repo: .
base_ref: workspace-orchestration
remote: origin
plans:
  - id: foundation
    path: plans/foundation
  - id: sources
    path: plans/sources
dependencies:
  - before: foundation:story-a
    after: sources:story-b
contract_gates:
  - id: api-contract
    producers:
      - foundation:story-a
    consumers:
      - sources:story-b
    artifacts:
      - id: openapi
        path: contracts/openapi.yaml
    validation:
      commands:
        - go test ./...
`
	if err := os.WriteFile(filepath.Join(workspaceDir, "feature.workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	contractDir := filepath.Join(workspaceDir, "contracts")
	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contractDir, "openapi.yaml"), []byte("openapi: 3.1.0\ninfo:\n  title: cli fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspaceDir
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
	return runFeatureInDir(t, "", args...)
}

func runFeatureInDir(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	cmdArgs := append([]string{"-test.run=TestHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "FEATURE_HELPER_PROCESS=1")
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func runFeatureJSON(t *testing.T, result any, args ...string) {
	t.Helper()
	stdout, stderr, err := runFeature(t, args...)
	if err != nil {
		t.Fatalf("feature %s failed: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), result); err != nil {
		t.Fatalf("feature %s did not emit JSON: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, stdout, stderr)
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
