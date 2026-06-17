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
			"<plan-dir>/worktrees/<merge-unit-id>",
			"open a PR with a clear title and description",
			"review the opened PR",
			"branch-diff review only when PR creation is not approved",
			"maximum of 10 fresh-review iterations",
			"keep that reviewer agent alive",
			"changed file list",
			"do not commit yet",
			"spawn a fresh subagent to review the updated PR",
			"stop and report the remaining findings instead of merging",
			"only after the final reviewed branch has been pushed",
			"feature implement cleanup",
			"immutable and ordered",
			"--write-state",
		} {
			if !strings.Contains(content, want) {
				t.Fatalf("staged implement skill %s missing %q", path, want)
			}
		}
		if strings.Contains(content, "feature implement push <plan-dir> --merge-unit <id> --allow-push --json") {
			t.Fatalf("staged implement skill %s includes a non-state-recording push write step", path)
		}
	}
	codexImplementSkill := filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md")
	b, err := os.ReadFile(codexImplementSkill)
	if err != nil {
		t.Fatalf("read staged codex implement skill %s: %v", codexImplementSkill, err)
	}
	content := string(b)
	for _, want := range []string{
		"active Codex Skills list includes `pr:review:no-file`",
		"implementation worktree/repository path",
		"`$pr:review:no-file <pr-number>`",
		"generic Codex PR-review subagent",
		"When using `pr:review:no-file`, apply selected fixes locally",
		"fresh no-file review returns no findings worth addressing",
		"same skill-selection rule",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("staged codex implement skill missing %q", want)
		}
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
