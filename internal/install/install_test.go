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
	codexFeatureMetadata := readInstalledSkill(t, filepath.Join(stage, ".codex", "skills", "feature", "agents", "openai.yaml"))
	assertContainsAll(t, "codex feature metadata", codexFeatureMetadata, []string{
		`default_prompt: "Use $feature`,
		"policy:",
		"allow_implicit_invocation: false",
	})
	codexImplementMetadata := readInstalledSkill(t, filepath.Join(stage, ".codex", "skills", "feature:implement", "agents", "openai.yaml"))
	assertContainsAll(t, "codex implement metadata", codexImplementMetadata, []string{
		`default_prompt: "Use $feature:implement`,
		"policy:",
		"allow_implicit_invocation: false",
	})
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
	planningSkills := []string{
		filepath.Join(stage, ".codex", "skills", "feature", "SKILL.md"),
		filepath.Join(stage, ".claude", "skills", "feature", "SKILL.md"),
	}
	for _, path := range planningSkills {
		content := readInstalledSkill(t, path)
		assertContainsAll(t, path, content, []string{
			"Invocation Guard",
			"explicitly",
			"~/tmp/feature-plans/<plan-id>/",
			"Quote every YAML string scalar",
			"Leave integers and booleans typed",
			"summary: \"Inventory: document systems, owners, dependencies, and risks.\"",
			"schema_version: 1",
			"number: 1",
			"require_passing_checks: true",
			"Critical/High means normal-flow failure, data loss, approval bypass, unintended external writes, or direct CLI incompatibility",
			"Do not turn speculative edge cases into blockers",
			"no fixed iteration cap",
			"Medium or Low findings from that final review once",
			"feature validate <plan-dir> --write-lock --json",
			"feature plan example",
			"feature plan schema --json",
		})
		assertNotContainsAny(t, path, content, []string{
			"maximum of 10",
			"feature workspace",
			"execution_config",
			"audit chain",
			"fsync",
			"or asks",
		})
		assertInOrder(t, path, content, []string{
			"Invocation Guard",
			"fresh reviewer",
			"Apply only evidence-backed Critical or High findings",
			"After each accepted Critical/High finding",
			"no Critical or High findings",
			"Medium or Low findings from that final review once",
			"--write-lock",
		})
	}

	implementSkills := []string{
		filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md"),
		filepath.Join(stage, ".claude", "skills", "feature:implement", "SKILL.md"),
	}
	for _, path := range implementSkills {
		content := readInstalledSkill(t, path)
		assertContainsAll(t, path, content, []string{
			"Invocation Guard",
			"explicitly",
			"existing `feature implement` lifecycle",
			"feature status <plan-dir> --json",
			"feature implement next <plan-dir> --json",
			"`story_progress_label`",
			"Before every external write, obtain explicit approval",
			"Obtain hidden-path approval before creating or removing a worktree",
			"Run and verify each Git or GitHub operation first",
			"body containing `story_progress_label`",
			"only to record verified results",
			"Critical/High means normal-flow failure, data loss, approval bypass, unintended external writes, or direct CLI incompatibility",
			"fresh reviewer to inspect the updated PR",
			"no fixed iteration cap",
			"Medium or Low findings from that final review once",
			"review-status passed",
			"review-status changes-applied",
			"feature implement start <plan-dir>",
			"feature implement push <plan-dir>",
			"feature implement open-pr <plan-dir>",
			"feature implement review <plan-dir>",
			"feature implement merge <plan-dir>",
			"feature implement cleanup <plan-dir>",
			"git -C <worktree> push -u <remote> HEAD:<branch>",
			"gh pr create --base <base-ref> --head <branch> --title \"<clear title>\" --body \"<story_progress_label>: <summary>\"",
			"gh pr merge <pr-number-or-url> --merge",
			"--write-state",
		})
		assertNotContainsAny(t, path, content, []string{
			"maximum of 10",
			"feature workspace",
			"feature.workspace.yaml",
			"execution_config",
			"audit chain",
			"filesystem transaction",
			"or asks",
		})
		assertInOrder(t, path, content, []string{
			"Invocation Guard",
			"Run and verify each Git or GitHub operation first",
			"Ask a fresh",
			"Critical or High findings",
			"fresh reviewer to inspect the updated PR",
			"no Critical or High findings",
			"Medium or Low findings from that final review once",
			"review-status passed",
		})
	}

	codexImplementSkill := filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md")
	codexImplementContent := readInstalledSkill(t, codexImplementSkill)
	assertContainsAll(t, codexImplementSkill, codexImplementContent, []string{
		"`$pr:review:no-file <pr-number>`",
		"generic PR-review subagent",
	})
}

func readInstalledSkill(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged skill %s: %v", path, err)
	}
	return string(b)
}

func assertContainsAll(t *testing.T, path string, content string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Fatalf("staged skill %s missing %q", path, want)
		}
	}
}

func assertNotContainsAny(t *testing.T, path string, content string, forbidden []string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(content, value) {
			t.Fatalf("staged skill %s contains removed text %q", path, value)
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
