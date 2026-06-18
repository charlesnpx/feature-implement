package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
		{"workspace", "attempt", "--help"},
		{"workspace", "attempt", "start", "--help"},
		{"workspace", "attempt", "abandon", "--help"},
		{"workspace", "transition", "--help"},
		{"workspace", "contract", "--help"},
		{"workspace", "contract", "publish", "--help"},
		{"workspace", "contract", "verify", "--help"},
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
		"feature workspace attempt",
		"feature workspace transition",
		"feature workspace contract",
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

	stdout, stderr, err = runFeature(t, "workspace", "init", "--manifest", "feature.workspace.yaml")
	if err == nil {
		t.Fatalf("workspace init stub should fail until implemented")
	}
	if !strings.Contains(stderr, "workspace init is not implemented yet") {
		t.Fatalf("expected init stub error:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if strings.Contains(stdout, "Usage:") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("workspace init stub should not print usage:\nstdout=%s\nstderr=%s", stdout, stderr)
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
