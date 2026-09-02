package ci_test

import (
	"bufio"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var removedRuntimeSurface = regexp.MustCompile(
	`(?i)(github-cli-auth|\bgh[[:space:]]+auth\b|\bgithub\b|\bprovider\b|` +
		`\bauthorization\b|\bcredentials?_available\b|\bstanding[-_ ]?grant\b|` +
		`\breceipts?\b|\breplay[-_ ]?claim\b|\bpull[-_ ]?request\b|` +
		`\bcontrol[-_ ]?plane|\bremote[-_ ]?completion|\bgit[-_ ]+authority\b)`,
)

func TestShippedRuntimeContainsNoRemovedExternalCapability(t *testing.T) {
	root := repositoryRoot(t)
	files := shippedRuntimeFiles(t, root)
	for _, relative := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", relative, err)
		}
		scanner := bufio.NewScanner(file)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if removedRuntimeSurface.MatchString(line) &&
				!allowedRemovedSurfaceReference(relative, line) {
				_ = file.Close()
				t.Fatalf(
					"shipped runtime %s:%d exposes removed capability text: %s",
					relative, lineNumber, strings.TrimSpace(line),
				)
			}
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			t.Fatalf("scan %s: %v", relative, err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close %s: %v", relative, err)
		}
	}
}

func TestRuntimeDependencyGraphContainsNoRemovedExternalAdapter(t *testing.T) {
	root := repositoryRoot(t)
	command := exec.Command("go", "list", "-deps", "./cmd/feature")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list feature dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		for _, forbidden := range []string{
			"github.com/cli/go-gh",
			"github.com/google/go-github",
		} {
			if dependency == forbidden ||
				strings.HasPrefix(dependency, forbidden+"/") {
				t.Fatalf(
					"feature dependency graph contains removed adapter %s",
					dependency,
				)
			}
		}
	}
}

func TestRemovedSurfaceScannerAllowsDevelopmentAndModuleMetadata(t *testing.T) {
	allowed := []struct {
		path string
		line string
	}{
		{
			path: "go.mod",
			line: "module github.com/charlesnpx/feature-implement",
		},
		{
			path: "internal/workspace/model.go",
			line: `import "github.com/charlesnpx/feature-implement/internal/workspace"`,
		},
		{
			path: "internal/workspace/review_adapter.go",
			line: `import "github.com/charlesnpx/witness/contract/review"`,
		},
		{
			path: "go.mod",
			line: "require github.com/charlesnpx/witness v0.6.1",
		},
		{
			path: ".github/workflows/check.yml",
			line: "pull_request:",
		},
		{
			path: "README.md",
			line: "The hosted pull-request workflow runs development checks.",
		},
		{
			path: "README.md",
			line: "### Deferred GitHub design",
		},
	}
	for _, fixture := range allowed {
		if removedRuntimeSurface.MatchString(fixture.line) &&
			!allowedRemovedSurfaceReference(fixture.path, fixture.line) {
			t.Fatalf(
				"allowed metadata was classified as runtime capability: %s: %s",
				fixture.path, fixture.line,
			)
		}
	}

	forbidden := []struct {
		path string
		line string
	}{
		{"skills/runtime/SKILL.md", "Install gh, then run gh auth login."},
		{"skills/runtime/SKILL.md", "Dispatch the provider request."},
		{
			"internal/workspace/runtime.go",
			`import "github.com/charlesnpx/feature-implement/internal/provider"`,
		},
		{"skills/runtime/SKILL.md", "credentials_available: true"},
		{"skills/runtime/SKILL.md", "control_plane_authority: owner"},
		{"skills/runtime/SKILL.md", "The Git authority is pinned remotely."},
		{
			"README.md",
			"The hosted pull-request workflow dispatches provider requests.",
		},
		{
			"internal/workspace/runtime_storage.go",
			"provider-oriented draft-v2 state enabled remote completion",
		},
		{
			"internal/workspace/attempt_git.go",
			`"AUTHORIZATION", "provider"`,
		},
		{
			"go.mod",
			"require github.com/cli/go-gh v1.0.0",
		},
	}
	for _, fixture := range forbidden {
		if !removedRuntimeSurface.MatchString(fixture.line) ||
			allowedRemovedSurfaceReference(fixture.path, fixture.line) {
			t.Fatalf(
				"removed capability escaped scanner: %s: %s",
				fixture.path, fixture.line,
			)
		}
	}
}

func shippedRuntimeFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{"README.md", "go.mod", "install-skill.sh"}
	for _, directory := range []string{
		"cmd",
		"internal",
		"skills",
	} {
		err := filepath.WalkDir(
			filepath.Join(root, filepath.FromSlash(directory)),
			func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				name := entry.Name()
				if strings.HasSuffix(name, "_test.go") {
					return nil
				}
				extension := filepath.Ext(name)
				if extension != ".go" &&
					extension != ".md" &&
					extension != ".yaml" &&
					extension != ".sh" {
					return nil
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				files = append(files, filepath.ToSlash(relative))
				return nil
			},
		)
		if err != nil {
			t.Fatalf("enumerate shipped runtime %s: %v", directory, err)
		}
	}
	return files
}

func allowedRemovedSurfaceReference(relative, line string) bool {
	normalized := filepath.ToSlash(relative)
	if strings.HasPrefix(normalized, ".github/") {
		return true
	}
	trimmed := strings.TrimSpace(line)
	modulePaths := []string{
		"github.com/charlesnpx/feature-implement",
		"github.com/charlesnpx/witness",
	}
	if normalized == "go.mod" && trimmed == "module "+modulePaths[0] {
		return true
	}
	for _, modulePath := range modulePaths {
		if strings.Contains(line, modulePath) &&
			!removedRuntimeSurface.MatchString(
				strings.ReplaceAll(line, modulePath, "local-module"),
			) {
			return true
		}
	}
	if normalized == "README.md" &&
		strings.Contains(strings.ToLower(line), "pull-request workflow") {
		remainder := strings.ReplaceAll(
			strings.ToLower(line), "pull-request workflow", "development workflow",
		)
		return !removedRuntimeSurface.MatchString(remainder)
	}
	if normalized == "README.md" &&
		strings.TrimSpace(line) == "### Deferred GitHub design" {
		return true
	}
	if normalized == "cmd/feature/main.go" &&
		strings.HasPrefix(trimmed, `case "queue", "receipts", "reconcile", "control", "provider":`) {
		return true
	}
	if normalized == "internal/workspacecmd/command.go" &&
		strings.HasPrefix(trimmed, `case "queue", "receipts", "reconcile", "control", "provider":`) {
		return true
	}
	if normalized == "internal/workspace/runtime_storage.go" &&
		strings.Contains(line, "provider-oriented draft-v2 state") {
		remainder := strings.ReplaceAll(
			line, "provider-oriented draft-v2 state", "incompatible runtime state",
		)
		return !removedRuntimeSurface.MatchString(remainder)
	}
	if normalized == "internal/workspace/attempt_git.go" {
		remainder := strings.ReplaceAll(line, `"AUTHORIZATION"`, `"SENSITIVE_HEADER"`)
		return remainder != line && !removedRuntimeSurface.MatchString(remainder)
	}
	return false
}
