package install

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	name   = "feature-implement"
	binary = "feature"
	schema = 1
)

type Options struct {
	Operation   string
	Target      string
	InstallRoot string
	Version     string
}

type Result struct {
	Schema       int                `json:"schema"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	Operation    string             `json:"operation"`
	Kind         string             `json:"kind"`
	Capabilities []string           `json:"capabilities,omitempty"`
	Setup        []SetupRequirement `json:"setup,omitempty"`
	Targets      map[string]Files   `json:"targets"`
	Warnings     []string           `json:"warnings"`
}

type SetupRequirement struct {
	Kind        string   `json:"kind"`
	Executable  string   `json:"executable,omitempty"`
	RequiredFor []string `json:"required_for,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
}

type Files struct {
	Files []File `json:"files"`
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

type plannedFile struct {
	target string
	source string
	dest   string
	mode   os.FileMode
	tool   bool
}

func Run(opts Options) (Result, error) {
	if opts.Operation == "" {
		opts.Operation = "install"
	}
	if opts.Target == "" {
		opts.Target = "all"
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Operation != "plan" && opts.Operation != "install" && opts.Operation != "uninstall" {
		return Result{}, fmt.Errorf("unsupported operation: %s", opts.Operation)
	}
	if opts.Target != "all" && opts.Target != "tools" && opts.Target != "codex" && opts.Target != "claude" {
		return Result{}, fmt.Errorf("unsupported target: %s", opts.Target)
	}
	root, err := repoRoot()
	if err != nil {
		return Result{}, err
	}
	home, err := installHome(opts.InstallRoot)
	if err != nil {
		return Result{}, err
	}
	plan, err := buildPlan(root, home, opts.Target)
	if err != nil {
		return Result{}, err
	}
	if opts.Operation == "install" {
		if err := applyInstall(root, plan, opts.Version); err != nil {
			return Result{}, err
		}
	}
	if opts.Operation == "uninstall" {
		for _, file := range plan {
			if err := os.Remove(file.dest); err != nil && !os.IsNotExist(err) {
				return Result{}, err
			}
		}
	}
	return resultFromPlan(opts.Operation, opts.Version, plan)
}

func repoRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv("FEATURE_IMPLEMENT_REPO_ROOT")); root != "" {
		return filepath.Abs(root)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd, nil
		}
		next := filepath.Dir(wd)
		if next == wd {
			return "", fmt.Errorf("could not locate repo root; set FEATURE_IMPLEMENT_REPO_ROOT")
		}
		wd = next
	}
}

func installHome(root string) (string, error) {
	if strings.TrimSpace(root) != "" {
		return filepath.Abs(root)
	}
	return os.UserHomeDir()
}

func buildPlan(root, home, target string) ([]plannedFile, error) {
	var files []plannedFile
	includeTools := target == "all" || target == "tools" || target == "codex" || target == "claude"
	includeCodex := target == "all" || target == "codex"
	includeClaude := target == "all" || target == "claude"

	if includeTools {
		files = append(files, plannedFile{
			target: "tools",
			dest:   filepath.Join(home, ".local", "bin", binary),
			mode:   0o755,
			tool:   true,
		})
	}
	if includeCodex {
		files = append(files,
			plannedFile{target: "codex", source: filepath.Join(root, "skills", "codex", "feature", "SKILL.md"), dest: filepath.Join(home, ".codex", "skills", "feature", "SKILL.md"), mode: 0o644},
			plannedFile{target: "codex", source: filepath.Join(root, "skills", "codex", "feature", "agents", "openai.yaml"), dest: filepath.Join(home, ".codex", "skills", "feature", "agents", "openai.yaml"), mode: 0o644},
			plannedFile{target: "codex", source: filepath.Join(root, "skills", "codex", "feature__colon__implement", "SKILL.md"), dest: filepath.Join(home, ".codex", "skills", "feature:implement", "SKILL.md"), mode: 0o644},
			plannedFile{target: "codex", source: filepath.Join(root, "skills", "codex", "feature__colon__implement", "agents", "openai.yaml"), dest: filepath.Join(home, ".codex", "skills", "feature:implement", "agents", "openai.yaml"), mode: 0o644},
		)
	}
	if includeClaude {
		files = append(files,
			plannedFile{target: "claude", source: filepath.Join(root, "skills", "claude", "feature", "SKILL.md"), dest: filepath.Join(home, ".claude", "skills", "feature", "SKILL.md"), mode: 0o644},
			plannedFile{target: "claude", source: filepath.Join(root, "skills", "claude", "feature__colon__implement", "SKILL.md"), dest: filepath.Join(home, ".claude", "skills", "feature:implement", "SKILL.md"), mode: 0o644},
		)
	}
	return files, nil
}

func applyInstall(root string, plan []plannedFile, version string) error {
	for _, file := range plan {
		if err := os.MkdirAll(filepath.Dir(file.dest), 0o755); err != nil {
			return err
		}
		if file.tool {
			cmd := exec.Command("go", "build", "-ldflags", "-X main.Version="+version, "-o", file.dest, filepath.Join(root, "cmd", "feature"))
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("go build feature: %w\n%s", err, strings.TrimSpace(string(out)))
			}
			continue
		}
		if err := copyFile(file.source, file.dest, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func resultFromPlan(operation, version string, plan []plannedFile) (Result, error) {
	targets := map[string]Files{}
	for _, file := range plan {
		abs, err := filepath.Abs(file.dest)
		if err != nil {
			return Result{}, err
		}
		entry := File{Path: abs}
		if operation == "install" {
			sha, err := shaFile(abs)
			if err != nil {
				return Result{}, err
			}
			entry.SHA256 = sha
		}
		target := targets[file.target]
		target.Files = append(target.Files, entry)
		targets[file.target] = target
	}
	for targetName, target := range targets {
		sort.Slice(target.Files, func(i, j int) bool { return target.Files[i].Path < target.Files[j].Path })
		targets[targetName] = target
	}
	return Result{
		Schema:       schema,
		Name:         name,
		Version:      version,
		Operation:    operation,
		Kind:         "delegated",
		Capabilities: []string{"query", "write"},
		Setup: []SetupRequirement{
			{Kind: "executable", Executable: "git", RequiredFor: []string{"query", "write"}, Remediation: "Install Git, then verify with `git --version`."},
			{Kind: "executable", Executable: "gh", RequiredFor: []string{"write"}, Remediation: "Install GitHub CLI, then run `gh auth login`."},
			{Kind: "github-cli-auth", RequiredFor: []string{"write"}, Remediation: "Run `gh auth login`, then verify with `gh auth status`."},
		},
		Targets:  targets,
		Warnings: []string{},
	}, nil
}

func shaFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
