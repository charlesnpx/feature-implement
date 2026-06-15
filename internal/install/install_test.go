package install

import (
	"os"
	"path/filepath"
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
