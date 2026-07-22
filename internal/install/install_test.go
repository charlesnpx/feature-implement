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
		`short_description: "Create workspace-v2 bundles"`,
		`default_prompt: "Use $feature to create and validate an implementation-ready workspace-v2 bundle."`,
		"policy:",
		"allow_implicit_invocation: false",
	})
	codexImplementMetadata := readInstalledSkill(t, filepath.Join(stage, ".codex", "skills", "feature:implement", "agents", "openai.yaml"))
	assertContainsAll(t, "codex implement metadata", codexImplementMetadata, []string{
		`short_description: "Execute governed workspace bundles"`,
		`default_prompt: "Use $feature:implement to execute a validated workspace-v2 bundle through governed merge units."`,
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
			"Invocation guard",
			"explicitly",
			"workspace-v2 bundle",
			"~/tmp/feature-plans/<workspace-id>/",
			"feature.workspace.bundle.json",
			"feature.workspace.yaml",
			"plans/*.yaml",
			"config/execution.yaml",
			"Quote every YAML string scalar",
			"Keep integers and booleans typed",
			"Default to one merge unit per story",
			"pause_only",
			"require_signed_receipts",
			"operator-supplied, externally pinned public authority material",
			"at most three plan-review iterations",
			"evidence-backed Critical and High findings",
			"do not start another plan-review iteration",
			"feature workspace validate --bundle <bundle-dir> --write-locks --json",
			"tool-owned immutable projections",
			"feature workspace schema bundle --json",
			"feature workspace schema requests --json",
			"feature workspace example",
		})
		assertNotContainsAny(t, path, content, []string{
			"schema_version: 1",
			"feature validate <plan-dir>",
			"feature plan example",
			"feature plan schema",
			"feature status <plan-dir>",
			"story_progress_label",
			"existing `feature implement` lifecycle",
			"local-only",
			"remote-delete",
			"Repeat until a fresh review has no Critical or High findings",
		})
		assertInOrder(t, path, content, []string{
			"Invocation guard",
			"Create a strict schema-version-2 workspace bundle",
			"Create `feature.workspace.bundle.json`",
			"feature workspace validate --bundle <bundle-dir> --json",
			"to review the source bundle",
			"evidence-backed Critical and High findings",
			"no Critical or High findings",
			"write-locks",
			"Bundle contract",
		})
	}

	implementSkills := []string{
		filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md"),
		filepath.Join(stage, ".claude", "skills", "feature:implement", "SKILL.md"),
	}
	for _, path := range implementSkills {
		content := readInstalledSkill(t, path)
		assertContainsAll(t, path, content, []string{
			"Invocation guard",
			"explicitly",
			"validated workspace-v2 bundle",
			"feature.workspace.bundle.json",
			"feature workspace validate --bundle <bundle-dir> --write-locks --json",
			"dedicated `<runtime-dir>` and `<worktree-root>` outside the primary checkout",
			"primary checkout may be dirty",
			"feature workspace schema requests --json",
			"explicit operator approval immediately before each external provider write",
			"feature workspace recover",
			"feature workspace scheduler",
			"attempt reserve",
			"attempt materialize",
			"attempt adopt-head",
			"journal-derived report",
			"feature workspace commit next",
			"typed provider intents",
			"review start",
			"review reserve",
			"review record",
			"review ready",
			"at most three iterations for the merge unit",
			"do not start another broad review iteration",
			"control grant",
			"`push`, `open_pull_request`, and `merge`",
			"provider dispatch",
			"provider preflight",
			"merge commit",
			"complete verify",
			"Provider responses contain typed evidence and idempotency markers only",
			"attempt boundary",
			"complete_goal_and_wait",
			"owner_gate",
			"create_next_goal",
			"`status`, `gates`, `queue`, `receipts`, and `report`",
		})
		assertNotContainsAny(t, path, content, []string{
			"feature status <plan-dir>",
			"existing `feature implement` lifecycle",
			"feature implement next",
			"feature implement start",
			"--write-state",
			"story_progress_label",
			"review-status",
			"changes-applied",
			"local-only",
			"local_only",
			"remote-delete",
			"provider_command",
			"git -C <worktree>",
			"gh pr",
			"Continue until `review ready` returns exact-head readiness",
		})
		assertInOrder(t, path, content, []string{
			"Invocation guard",
			"Preconditions",
			"feature workspace recover",
			"attempt reserve",
			"attempt materialize",
			"Implement and commit",
			"Governed review",
			"review start",
			"review ready",
			"Protected authorization and provider effects",
			"control grant",
			"provider dispatch",
			"provider preflight",
			"complete verify",
			"Boundaries and continuation",
			"Finish",
		})
	}

	codexImplementSkill := filepath.Join(stage, ".codex", "skills", "feature:implement", "SKILL.md")
	codexImplementContent := readInstalledSkill(t, codexImplementSkill)
	assertContainsAll(t, codexImplementSkill, codexImplementContent, []string{
		"literal `$feature:implement` invocation",
		"fresh Codex subagent",
	})
	claudeImplementSkill := filepath.Join(stage, ".claude", "skills", "feature:implement", "SKILL.md")
	claudeImplementContent := readInstalledSkill(t, claudeImplementSkill)
	assertContainsAll(t, claudeImplementSkill, claudeImplementContent, []string{
		"literal `/feature:implement` invocation",
		"fresh Claude subagent",
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
