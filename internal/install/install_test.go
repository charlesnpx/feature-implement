package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPlanDoesNotWrite(t *testing.T) {
	stage := t.TempDir()
	result, err := Run(Options{Operation: "plan", Target: "codex", InstallRoot: stage, Version: "test"})
	if err != nil {
		t.Fatalf("Run plan: %v", err)
	}
	if result.Schema != 1 || result.Name != "feature-implement" || result.Kind != "delegated" {
		t.Fatalf("bad result metadata: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(stage, ".codex", "skills", "feature", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("plan should not write files, stat err=%v", err)
	}
	for _, files := range result.Targets {
		for _, file := range files.Files {
			if file.SHA256 != "" {
				t.Fatalf("plan should not include sha256: %+v", file)
			}
		}
	}
}

func TestRunInstallStagedAllTargets(t *testing.T) {
	stage := t.TempDir()
	result, err := Run(Options{Operation: "install", Target: "all", InstallRoot: stage, Version: "test"})
	if err != nil {
		t.Fatalf("Run install: %v", err)
	}
	expected := []string{
		filepath.Join(stage, ".local", "bin", "feature"),
		filepath.Join(stage, ".codex", "skills", "feature", "SKILL.md"),
		filepath.Join(stage, ".codex", "skills", "feature", "agents", "openai.yaml"),
		filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md"),
		filepath.Join(stage, ".codex", "skills", "feature:implement", "agents", "openai.yaml"),
		filepath.Join(stage, ".claude", "skills", "feature", "SKILL.md"),
		filepath.Join(stage, ".claude", "skills", "feature:implement", "SKILL.md"),
	}
	for _, path := range expected {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected installed file %s: %v", path, err)
		}
	}
	for target, files := range result.Targets {
		if len(files.Files) == 0 {
			t.Fatalf("target %s has no files", target)
		}
		for _, file := range files.Files {
			if len(file.SHA256) != 64 {
				t.Fatalf("target %s file %s missing sha256: %+v", target, file.Path, file)
			}
		}
	}
	for _, path := range []string{
		filepath.Join(stage, ".codex", "skills", "feature", "SKILL.md"),
		filepath.Join(stage, ".claude", "skills", "feature", "SKILL.md"),
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read staged skill %s: %v", path, err)
		}
		content := string(b)
		for _, want := range []string{
			"## Manifest Contract",
			"schema_version: 1",
			"feature plan example",
			"feature plan schema --json",
			"For migration or phased-planning prompts",
			"Every story must be implementation-ready",
			"`testing`: explicit test criteria",
			"Testing Criteria",
			"Never write the draft manifest in the current repo root",
			"~/tmp",
			"system temp directory",
		} {
			if !strings.Contains(content, want) {
				t.Fatalf("staged skill %s missing %q", path, want)
			}
		}
	}
	codexFeatureSkill := filepath.Join(stage, ".codex", "skills", "feature", "SKILL.md")
	b, err := os.ReadFile(codexFeatureSkill)
	if err != nil {
		t.Fatalf("read staged codex feature skill %s: %v", codexFeatureSkill, err)
	}
	codexFeatureContent := string(b)
	for _, want := range []string{
		"Do not use `pr:review:local:no-file`",
		"maximum of 10 fresh-review iterations",
		"re-run `feature plan materialize`",
		"Stop only when a fresh reviewer returns no findings worth addressing",
	} {
		if !strings.Contains(codexFeatureContent, want) {
			t.Fatalf("staged codex feature skill missing %q", want)
		}
	}
	for _, path := range []string{
		filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md"),
		filepath.Join(stage, ".claude", "skills", "feature:implement", "SKILL.md"),
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read staged implement skill %s: %v", path, err)
		}
		content := string(b)
		for _, want := range []string{
			"single-plan feature workspace",
			"feature validate <plan-dir> --write-lock --json",
			"feature.workspace.yaml",
			"base_ref: <base_ref from feature.plan.lock.json>",
			"remote: <remote from feature.plan.lock.json>",
			"feature workspace init --manifest <workspace-dir>/feature.workspace.yaml --write-lock --json",
			"feature workspace status <workspace-dir> --json",
			"feature workspace recover <workspace-dir> --json",
			"feature workspace next <workspace-dir> --agent <id> --claim --json",
			"feature workspace attempt start <workspace-dir>",
			"worker packet",
			"`workspace_id`, `repo`, `base_ref`",
			"`plan_id`, `plan_merge_unit_id`, `story_ids`",
			"`attempt_id`, `lease_id`, `branch`, `worktree`, `base_sha`, and `commands`",
			"`commands[]` worktree command",
			"feature workspace transition ... --from pending --to in_progress --evidence worktree=<worktree> --json",
			"feature workspace heartbeat <workspace-dir> --agent <id> --lease <id> --json",
			"feature workspace refresh-branch --local",
			"feature workspace refresh-branch <workspace-dir> --local",
			"feature workspace evaluate-gates <workspace-dir>",
			"feature workspace gate record <workspace-dir>",
			"feature workspace queue enter <workspace-dir>",
			"feature workspace approve grant",
			"feature workspace approve grant <workspace-dir>",
			"--action merge",
			"--approval <merge-approval-id>",
			"feature workspace external plan <workspace-dir>",
			"approval capability",
			"`approval_command`, `intent_command`, `provider_command`, then `result_command`",
			"`push`, `open-pr`, `merge`, and `remote-delete`",
			"accepted merge external intent evidence",
			"merge-intent-id",
			"--from in_progress --to completed",
			"external_intent_ids",
			"--from in_progress --to failed",
			"`feature.plan.lock.json` as read-only input",
			"direct plan lifecycle write-state commands",
			"workspace state commands",
			"branch-diff review only when PR creation is not approved",
			"maximum of 10 fresh-review iterations",
			"External writes remain explicitly approval-gated",
		} {
			if !strings.Contains(content, want) {
				t.Fatalf("staged implement skill %s missing %q", path, want)
			}
		}
		for _, forbidden := range []string{
			"feature implement next",
			"feature implement start",
			"feature implement commit",
			"feature implement push",
			"feature implement open-pr",
			"feature implement review",
			"feature implement merge",
			"feature implement cleanup",
			"feature status <plan-dir>",
			"<plan-dir>/worktrees/<merge-unit-id>",
			"always record lifecycle changes through `feature implement",
		} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("staged implement skill %s still contains stale serial workflow text %q", path, forbidden)
			}
		}
		assertInOrder(t, path, content, []string{
			"feature validate <plan-dir> --write-lock --json",
			"feature workspace init --manifest <workspace-dir>/feature.workspace.yaml --write-lock --json",
			"feature workspace status <workspace-dir> --json",
			"feature workspace recover <workspace-dir> --json",
			"feature workspace next <workspace-dir> --agent <id> --claim --json",
			"feature workspace attempt start <workspace-dir>",
			"feature workspace transition <workspace-dir>",
			"feature workspace heartbeat <workspace-dir>",
			"feature workspace refresh-branch <workspace-dir> --local",
			"feature workspace evaluate-gates <workspace-dir>",
			"feature workspace gate record <workspace-dir>",
			"feature workspace evaluate-gates <workspace-dir>",
			"feature workspace approve grant <workspace-dir>",
			"feature workspace evaluate-gates <workspace-dir>",
			"feature workspace queue enter <workspace-dir>",
			"feature workspace external plan <workspace-dir>",
			"feature workspace external intent result <workspace-dir>",
			"feature workspace transition <workspace-dir>",
		})
	}
	codexImplementSkill := filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md")
	b, err = os.ReadFile(codexImplementSkill)
	if err != nil {
		t.Fatalf("read staged codex implement skill %s: %v", codexImplementSkill, err)
	}
	codexImplementContent := string(b)
	for _, want := range []string{
		"active Codex Skills list includes `pr:review:no-file`",
		"implementation worktree/repository path",
		"`$pr:review:no-file <pr-number>`",
		"generic Codex PR-review subagent",
	} {
		if !strings.Contains(codexImplementContent, want) {
			t.Fatalf("staged codex implement skill missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"keep that reviewer agent alive",
		"changed file list, and relevant local diff",
		"ask whether the patch resolves its specific concerns",
	} {
		if strings.Contains(codexImplementContent, forbidden) {
			t.Fatalf("staged codex implement skill still contains removed reviewer-confirmation wording %q", forbidden)
		}
	}
}

func assertInOrder(t *testing.T, path string, content string, wants []string) {
	t.Helper()
	offset := 0
	for _, want := range wants {
		index := strings.Index(content[offset:], want)
		if index < 0 {
			t.Fatalf("staged skill %s missing %q after byte offset %d", path, want, offset)
		}
		offset += index + len(want)
	}
}

func TestRunTargetFiltering(t *testing.T) {
	stage := t.TempDir()
	result, err := Run(Options{Operation: "plan", Target: "tools", InstallRoot: stage, Version: "test"})
	if err != nil {
		t.Fatalf("Run tools plan: %v", err)
	}
	if len(result.Targets) != 1 || len(result.Targets["tools"].Files) != 1 {
		t.Fatalf("tools target filtering failed: %+v", result.Targets)
	}
	result, err = Run(Options{Operation: "plan", Target: "claude", InstallRoot: stage, Version: "test"})
	if err != nil {
		t.Fatalf("Run claude plan: %v", err)
	}
	if _, ok := result.Targets["tools"]; !ok {
		t.Fatalf("claude target should include tools: %+v", result.Targets)
	}
	if _, ok := result.Targets["claude"]; !ok {
		t.Fatalf("claude target missing claude files: %+v", result.Targets)
	}
	if _, ok := result.Targets["codex"]; ok {
		t.Fatalf("claude target should not include codex files: %+v", result.Targets)
	}
}
