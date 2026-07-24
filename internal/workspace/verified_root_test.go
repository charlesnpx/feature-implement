package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestVerifiedRootRejectsSymlinksAndDetectsReplacement(t *testing.T) {
	parent := canonicalVerifiedRootTempDir(t)
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(parent, "link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.OpenVerifiedRoot(workspace.RootRoleRuntime, symlinkRoot, false); err == nil ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked root error = %v", err)
	}
	ancestorTarget := filepath.Join(parent, "ancestor-target")
	if err := os.Mkdir(ancestorTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ancestorTarget, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	ancestorLink := filepath.Join(parent, "ancestor-link")
	if err := os.Symlink(ancestorTarget, ancestorLink); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.OpenVerifiedRoot(
		workspace.RootRoleRuntime,
		filepath.Join(ancestorLink, "nested"),
		false,
	); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked ancestor error = %v", err)
	}
	insecureRoot := filepath.Join(parent, "insecure")
	if err := os.Mkdir(insecureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecureRoot, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.OpenVerifiedRoot(
		workspace.RootRoleRuntime, insecureRoot, false,
	); err == nil || !strings.Contains(err.Error(), "non-owner writes") {
		t.Fatalf("insecure root permissions error = %v", err)
	}

	root, err := workspace.OpenVerifiedRoot(workspace.RootRoleRuntime, realRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteExclusive("bounded.txt", []byte("bounded"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ReadBounded("bounded.txt", 6); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bounded read error = %v", err)
	}
	if content, err := root.ReadBounded("bounded.txt", 7); err != nil || string(content) != "bounded" {
		t.Fatalf("bounded read = %q, %v", content, err)
	}

	movedRoot := filepath.Join(parent, "moved")
	if err := os.Rename(realRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.VerifyPath(); err == nil || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("replacement error = %v", err)
	}
	if _, err := root.ReadBounded("bounded.txt", 7); err == nil || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("read through replaced path error = %v", err)
	}
}

func TestVerifiedRootDurabilityProbeCleansItsArtifacts(t *testing.T) {
	rootPath := filepath.Join(canonicalVerifiedRootTempDir(t), "runtime")
	root, err := workspace.OpenVerifiedRoot(workspace.RootRoleRuntime, rootPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.ProbeDurability(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("durability probe left artifacts: %v", entries)
	}
}

func TestWorkspaceInitializationAdmissionRejectsPlanRuntimeOverlap(t *testing.T) {
	parent := canonicalVerifiedRootTempDir(t)
	planPath := filepath.Join(parent, "plan")
	targetPath := filepath.Join(parent, "target")
	worktreePath := filepath.Join(parent, "worktrees")
	for _, candidate := range []string{planPath, targetPath, worktreePath} {
		if err := os.Mkdir(candidate, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runtimePath := filepath.Join(planPath, "runtime")
	if _, err := workspace.OpenWorkspaceInitializationRootGuard(
		planPath, runtimePath, targetPath, worktreePath,
	); err == nil || !strings.Contains(err.Error(), "unsafe workspace root overlap") {
		t.Fatalf("plan/runtime admission error = %v", err)
	}
	if _, err := os.Lstat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("plan/runtime overlap admission created runtime path: %v", err)
	}
}

func TestWorkspaceRootLayoutAllowsOnlyGitStructuralOverlap(t *testing.T) {
	parent := canonicalVerifiedRootTempDir(t)
	paths := map[workspace.RootRole]string{
		workspace.RootRolePlan:               filepath.Join(parent, "plan"),
		workspace.RootRoleRuntime:            filepath.Join(parent, "runtime"),
		workspace.RootRoleTarget:             filepath.Join(parent, "target"),
		workspace.RootRoleWorktree:           filepath.Join(parent, "attempts"),
		workspace.RootRoleRegisteredWorktree: filepath.Join(parent, "linked"),
	}
	for _, path := range paths {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	commonPath := filepath.Join(paths[workspace.RootRoleTarget], ".git")
	if err := os.Mkdir(commonPath, 0o700); err != nil {
		t.Fatal(err)
	}
	open := func(role workspace.RootRole, path string) *workspace.VerifiedRoot {
		t.Helper()
		root, err := workspace.OpenVerifiedRoot(role, path, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		return root
	}
	layout := workspace.WorkspaceRootLayout{
		Plan:      open(workspace.RootRolePlan, paths[workspace.RootRolePlan]),
		Runtime:   open(workspace.RootRoleRuntime, paths[workspace.RootRoleRuntime]),
		Target:    open(workspace.RootRoleTarget, paths[workspace.RootRoleTarget]),
		GitCommon: open(workspace.RootRoleGitCommon, commonPath),
		Worktree:  open(workspace.RootRoleWorktree, paths[workspace.RootRoleWorktree]),
		RegisteredWorktrees: []*workspace.VerifiedRoot{
			open(workspace.RootRoleRegisteredWorktree, paths[workspace.RootRoleTarget]),
			open(workspace.RootRoleRegisteredWorktree, paths[workspace.RootRoleRegisteredWorktree]),
		},
	}
	if err := workspace.ValidateWorkspaceRootLayout(layout); err != nil {
		t.Fatalf("valid primary-worktree layout: %v", err)
	}

	overlappingRuntime := filepath.Join(paths[workspace.RootRolePlan], "runtime")
	if err := os.Mkdir(overlappingRuntime, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeRuntime := open(workspace.RootRoleRuntime, overlappingRuntime)
	unsafe := layout
	unsafe.Runtime = unsafeRuntime
	if err := workspace.ValidateWorkspaceRootLayout(unsafe); err == nil ||
		!strings.Contains(err.Error(), "unsafe workspace root overlap") {
		t.Fatalf("plan/runtime overlap error = %v", err)
	}

	unsafeWorktree := open(workspace.RootRoleWorktree, paths[workspace.RootRoleRegisteredWorktree])
	unsafe = layout
	unsafe.Worktree = unsafeWorktree
	if err := workspace.ValidateWorkspaceRootLayout(unsafe); err == nil ||
		!strings.Contains(err.Error(), "unsafe workspace root overlap") {
		t.Fatalf("registered-worktree overlap error = %v", err)
	}
}

func TestWorkspaceRootLayoutSupportsLinkedWorktreeCommonDirectory(t *testing.T) {
	parent := canonicalVerifiedRootTempDir(t)
	planPath := filepath.Join(parent, "plan")
	runtimePath := filepath.Join(parent, "runtime")
	primaryPath := filepath.Join(parent, "primary")
	linkedPath := filepath.Join(parent, "linked")
	attemptPath := filepath.Join(parent, "attempts")
	for _, candidate := range []string{planPath, runtimePath, primaryPath, linkedPath, attemptPath} {
		if err := os.Mkdir(candidate, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	commonPath := filepath.Join(primaryPath, ".git")
	if err := os.Mkdir(commonPath, 0o700); err != nil {
		t.Fatal(err)
	}
	open := func(role workspace.RootRole, candidate string) *workspace.VerifiedRoot {
		t.Helper()
		root, err := workspace.OpenVerifiedRoot(role, candidate, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		return root
	}
	layout := workspace.WorkspaceRootLayout{
		Plan:      open(workspace.RootRolePlan, planPath),
		Runtime:   open(workspace.RootRoleRuntime, runtimePath),
		Target:    open(workspace.RootRoleTarget, linkedPath),
		GitCommon: open(workspace.RootRoleGitCommon, commonPath),
		Worktree:  open(workspace.RootRoleWorktree, attemptPath),
		RegisteredWorktrees: []*workspace.VerifiedRoot{
			open(workspace.RootRoleRegisteredWorktree, primaryPath),
			open(workspace.RootRoleRegisteredWorktree, linkedPath),
		},
	}
	if err := workspace.ValidateWorkspaceRootLayout(layout); err != nil {
		t.Fatalf("valid linked-worktree layout: %v", err)
	}
	unsafe := layout
	unsafe.Worktree = open(workspace.RootRoleWorktree, primaryPath)
	if err := workspace.ValidateWorkspaceRootLayout(unsafe); err == nil ||
		!strings.Contains(err.Error(), "unsafe workspace root overlap") {
		t.Fatalf("attempt/registered overlap error = %v", err)
	}
}

func canonicalVerifiedRootTempDir(t *testing.T) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
