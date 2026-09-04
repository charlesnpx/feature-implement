package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

type RootRole string

const runtimeStagePrefix = "runtime-stage-"

const (
	RootRolePlan               RootRole = "plan"
	RootRoleRuntime            RootRole = "runtime"
	RootRoleTarget             RootRole = "target"
	RootRoleGitDirectory       RootRole = "git-directory"
	RootRoleGitCommon          RootRole = "git-common"
	RootRoleWorktree           RootRole = "worktree"
	RootRoleRegisteredWorktree RootRole = "registered-worktree"
)

func (role RootRole) valid() bool {
	switch role {
	case RootRolePlan, RootRoleRuntime, RootRoleTarget, RootRoleGitDirectory, RootRoleGitCommon,
		RootRoleWorktree, RootRoleRegisteredWorktree:
		return true
	default:
		return false
	}
}

type VerifiedRoot struct {
	role     RootRole
	path     string
	identity PlatformFileIdentity
	info     os.FileInfo
	adapter  *RootedFilesystemAdapter
}

func OpenVerifiedRoot(role RootRole, rootPath string, create bool) (*VerifiedRoot, error) {
	if !role.valid() {
		return nil, fmt.Errorf("verified root role %q is unsupported", role)
	}
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if !filepath.IsAbs(rootPath) {
		return nil, fmt.Errorf("%s root must be absolute", role)
	}
	volumeRoot := filepath.VolumeName(rootPath) + string(filepath.Separator)
	if rootPath == volumeRoot {
		return nil, fmt.Errorf("%s root cannot be a filesystem volume root", role)
	}

	var adapter *RootedFilesystemAdapter
	var err error
	if create {
		adapter, err = openOrCreateRootedFilesystemAdapter(rootPath)
	} else {
		adapter, err = OpenRootedFilesystemAdapter(rootPath)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s root: %w", role, err)
	}
	info, err := adapter.root.Stat(".")
	if err != nil {
		_ = adapter.Close()
		return nil, fmt.Errorf("inspect %s root: %w", role, err)
	}
	identity, err := platformFileIdentity(info)
	if err != nil {
		_ = adapter.Close()
		return nil, fmt.Errorf("identify %s root: %w", role, err)
	}
	currentOwner, err := currentFilesystemOwner()
	if err != nil {
		_ = adapter.Close()
		return nil, fmt.Errorf("verify %s root ownership: %w", role, err)
	}
	if identity.Owner != currentOwner {
		_ = adapter.Close()
		return nil, fmt.Errorf(
			"%s root owner %d does not match effective owner %d",
			role, identity.Owner, currentOwner,
		)
	}
	if info.Mode().Perm()&0o022 != 0 {
		_ = adapter.Close()
		return nil, fmt.Errorf(
			"%s root permissions %04o allow non-owner writes",
			role, info.Mode().Perm(),
		)
	}
	return &VerifiedRoot{
		role: role, path: rootPath, identity: identity, info: info, adapter: adapter,
	}, nil
}

func (root *VerifiedRoot) Role() RootRole {
	if root == nil {
		return ""
	}
	return root.role
}

func (root *VerifiedRoot) Path() string {
	if root == nil {
		return ""
	}
	return root.path
}

func (root *VerifiedRoot) Identity() PlatformFileIdentity {
	if root == nil {
		return PlatformFileIdentity{}
	}
	return root.identity
}

func (root *VerifiedRoot) Close() error {
	if root == nil || root.adapter == nil {
		return nil
	}
	err := root.adapter.Close()
	root.adapter = nil
	if err != nil {
		return fmt.Errorf("close %s root: %w", root.role, err)
	}
	return nil
}

func (root *VerifiedRoot) VerifyPath() error {
	if root == nil || root.adapter == nil {
		return fmt.Errorf("verified root is closed")
	}
	reopened, err := reopenVerifiedRootedFilesystemAdapter(root.path)
	if err != nil {
		return fmt.Errorf("reopen %s root: %w", root.role, err)
	}
	defer reopened.Close()
	info, err := reopened.root.Stat(".")
	if err != nil {
		return fmt.Errorf("reinspect %s root: %w", root.role, err)
	}
	identity, err := platformFileIdentity(info)
	if err != nil {
		return fmt.Errorf("reidentify %s root: %w", root.role, err)
	}
	if identity != root.identity || !os.SameFile(root.info, info) {
		return fmt.Errorf(
			"%s root at %s was replaced (expected device=%d inode=%d, observed device=%d inode=%d)",
			root.role, root.path, root.identity.Device, root.identity.Inode,
			identity.Device, identity.Inode,
		)
	}
	return nil
}

func (root *VerifiedRoot) ReadBounded(relative string, maximum int64) ([]byte, error) {
	if maximum < 0 {
		return nil, fmt.Errorf("bounded rooted read requires a non-negative maximum")
	}
	if err := root.VerifyPath(); err != nil {
		return nil, err
	}
	file, _, err := root.adapter.openRegularFileExact(relative, os.O_RDONLY, 0, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := root.verifyOwnedRegularFile(relative, file); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect %s root file %s: %w", root.role, relative, err)
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("rooted file %s exceeds %d bytes", relative, maximum)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read %s root file %s: %w", root.role, relative, err)
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("rooted file %s exceeds %d bytes", relative, maximum)
	}
	if err := root.verifyOwnedRegularFile(relative, file); err != nil {
		return nil, err
	}
	if err := root.VerifyPath(); err != nil {
		return nil, err
	}
	return content, nil
}

func (root *VerifiedRoot) WriteExclusive(relative string, content []byte, permission os.FileMode) error {
	_, err := root.writeExclusivePublished(relative, content, permission, nil)
	return err
}

// writeExclusivePublished reports whether its no-replace rename made the
// target visible, including when a later check fails. Callers that need an
// all-or-nothing higher-level operation can then remove that exact target.
func (root *VerifiedRoot) writeExclusivePublished(
	relative string,
	content []byte,
	permission os.FileMode,
	afterPublication func() error,
) (bool, error) {
	if err := root.VerifyPath(); err != nil {
		return false, err
	}
	rooted, err := NewRootedPath(root.path, relative)
	if err != nil {
		return false, err
	}
	relative = rooted.Relative()
	if _, exists, err := root.adapter.inspectExact(relative); err != nil {
		return false, err
	} else if exists {
		return false, fmt.Errorf("rooted path %s already exists", relative)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return false, fmt.Errorf("create exclusive rooted publication nonce: %w", err)
	}
	temporary := path.Join(
		path.Dir(relative),
		runtimeStagePrefix+hex.EncodeToString(random),
	)
	if err := root.adapter.writeFileExclusive(temporary, content, permission); err != nil {
		return false, err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_, _ = root.adapter.removeFileContentExact(
				temporary, content, int64(len(content)), nil,
			)
		}
	}()
	published, err := root.adapter.renameFileNoReplaceWithPublication(
		temporary, relative, afterPublication,
	)
	if published {
		removeTemporary = false
	}
	if err != nil {
		return published, err
	}
	file, _, err := root.adapter.openRegularFileExact(relative, os.O_RDONLY, 0, false)
	if err != nil {
		return true, err
	}
	defer file.Close()
	if err := root.verifyOwnedRegularFile(relative, file); err != nil {
		return true, err
	}
	return true, root.VerifyPath()
}

func (root *VerifiedRoot) EnsureDirectory(relative string, permission os.FileMode) error {
	if err := root.VerifyPath(); err != nil {
		return err
	}
	if _, _, err := root.adapter.makeDirectory(relative, permission); err != nil {
		return err
	}
	return root.VerifyPath()
}

func (root *VerifiedRoot) Sync() error {
	if err := root.VerifyPath(); err != nil {
		return err
	}
	if err := root.adapter.syncDirectory("."); err != nil {
		return err
	}
	return root.VerifyPath()
}

func (root *VerifiedRoot) openOwnedRegularFile(
	relative string,
	flags int,
	permission os.FileMode,
	create bool,
) (*os.File, bool, error) {
	if err := root.VerifyPath(); err != nil {
		return nil, false, err
	}
	file, created, err := root.adapter.openRegularFileExact(relative, flags, permission, create)
	if err != nil {
		return nil, false, err
	}
	if err := root.verifyOwnedRegularFile(relative, file); err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if err := root.VerifyPath(); err != nil {
		_ = file.Close()
		return nil, false, err
	}
	return file, created, nil
}

func (root *VerifiedRoot) verifyOwnedRegularFile(relative string, file *os.File) error {
	if root == nil || root.adapter == nil {
		return fmt.Errorf("verified root is closed")
	}
	if err := root.adapter.verifyOpenedFileExact(relative, file); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect %s root file %s: %w", root.role, relative, err)
	}
	identity, err := platformFileIdentity(info)
	if err != nil {
		return fmt.Errorf("identify %s root file %s: %w", root.role, relative, err)
	}
	if identity.Owner != root.identity.Owner {
		return fmt.Errorf(
			"%s root file %s owner %d does not match root owner %d",
			root.role, relative, identity.Owner, root.identity.Owner,
		)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf(
			"%s root file %s permissions %04o allow non-owner writes",
			root.role, relative, info.Mode().Perm(),
		)
	}
	links, err := platformFileLinkCount(info)
	if err != nil {
		return fmt.Errorf("inspect %s root file %s hard links: %w", root.role, relative, err)
	}
	if links != 1 {
		return fmt.Errorf("%s root file %s has %d hard links; exactly one is required", root.role, relative, links)
	}
	return nil
}

func (root *VerifiedRoot) ProbeDurability() error {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("create %s root capability nonce: %w", root.role, err)
	}
	probeDirectory := "runtime-capability-" + hex.EncodeToString(random)
	return root.probeDurabilityAt(probeDirectory)
}

func (root *VerifiedRoot) probeDurabilityAt(probeDirectory string) error {
	if err := root.VerifyPath(); err != nil {
		return err
	}
	rootedProbe, err := NewRootedPath(root.path, probeDirectory)
	if err != nil {
		return fmt.Errorf("validate %s root capability probe path: %w", root.role, err)
	}
	probeDirectory = rootedProbe.Relative()
	if path.Dir(probeDirectory) != "." {
		return fmt.Errorf("%s root capability probe must use one owned directory", root.role)
	}
	source := probeDirectory + "/source"
	linked := probeDirectory + "/linked"
	renamed := probeDirectory + "/renamed"
	content := []byte("feature-implement-root-capability\n")

	cleanup := func() error {
		var cleanupErrors []error
		for _, candidate := range []string{renamed, linked, source} {
			if _, err := root.adapter.removeFileContentExact(candidate, content, int64(len(content)), nil); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		if _, err := root.adapter.removeEmptyDirectoryExact(probeDirectory, nil); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, err)
		}
		return errors.Join(cleanupErrors...)
	}
	fail := func(capability string, capabilityErr error) error {
		cleanupErr := cleanup()
		if cleanupErr != nil {
			return fmt.Errorf(
				"%s root does not support required %s semantics: %w; clean probe: %v",
				root.role, capability, capabilityErr, cleanupErr,
			)
		}
		return fmt.Errorf("%s root does not support required %s semantics: %w", root.role, capability, capabilityErr)
	}

	created, _, err := root.adapter.makeDirectory(probeDirectory, 0o700)
	if err != nil || !created {
		if err == nil {
			err = fmt.Errorf("probe directory already exists")
		}
		return fail("secure creation", err)
	}
	if err := root.adapter.writeFileExclusive(source, content, 0o600); err != nil {
		return fail("exclusive file creation and synchronization", err)
	}
	file, _, err := root.adapter.openRegularFileExact(source, os.O_RDWR, 0o600, false)
	if err != nil {
		return fail("no-follow file opening", err)
	}
	if err := root.verifyOwnedRegularFile(source, file); err != nil {
		_ = file.Close()
		return fail("ownership and single-link verification", err)
	}
	info, statErr := file.Stat()
	if statErr == nil {
		var identity PlatformFileIdentity
		identity, statErr = platformFileIdentity(info)
		if statErr == nil && identity.Owner != root.identity.Owner {
			statErr = fmt.Errorf("probe owner %d does not match root owner %d", identity.Owner, root.identity.Owner)
		}
	}
	if statErr != nil {
		_ = file.Close()
		return fail("ownership verification", statErr)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return fail("advisory locking", err)
	}
	contender, _, err := root.adapter.openRegularFileExact(
		source, os.O_RDWR, 0, false,
	)
	if err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fail("competing advisory lock opening", err)
	}
	if err := root.verifyOwnedRegularFile(source, contender); err != nil {
		_ = contender.Close()
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fail("competing advisory lock ownership", err)
	}
	contenderLockErr := syscall.Flock(
		int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB,
	)
	if contenderLockErr == nil {
		contenderUnlockErr := syscall.Flock(int(contender.Fd()), syscall.LOCK_UN)
		contenderCloseErr := contender.Close()
		unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		closeErr := file.Close()
		return fail(
			"advisory lock exclusion",
			errors.Join(
				fmt.Errorf("an independently opened contender acquired the held lock"),
				contenderUnlockErr,
				contenderCloseErr,
				unlockErr,
				closeErr,
			),
		)
	}
	if !errors.Is(contenderLockErr, syscall.EWOULDBLOCK) &&
		!errors.Is(contenderLockErr, syscall.EAGAIN) {
		_ = contender.Close()
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return fail("competing advisory locking", contenderLockErr)
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if unlockErr != nil {
		_ = contender.Close()
		_ = file.Close()
		return fail("advisory unlocking", unlockErr)
	}
	if err := syscall.Flock(
		int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB,
	); err != nil {
		_ = contender.Close()
		_ = file.Close()
		return fail("advisory lock handoff", err)
	}
	contenderUnlockErr := syscall.Flock(int(contender.Fd()), syscall.LOCK_UN)
	contenderCloseErr := contender.Close()
	closeErr := file.Close()
	if contenderUnlockErr != nil {
		return fail("competing advisory unlocking", contenderUnlockErr)
	}
	if contenderCloseErr != nil {
		return fail("competing lock file closure", contenderCloseErr)
	}
	if closeErr != nil {
		return fail("file closure", closeErr)
	}
	if err := root.adapter.linkFileNoReplace(source, linked); err != nil {
		return fail("hard-link no-replace publication", err)
	}
	if err := root.adapter.renameFileNoReplace(linked, renamed); err != nil {
		return fail("rename no-replace publication", err)
	}
	same, err := root.adapter.sameFile(source, renamed)
	if err != nil || !same {
		if err == nil {
			err = fmt.Errorf("hard-link identity changed")
		}
		return fail("opened-object identity", err)
	}
	if err := root.adapter.syncDirectory(probeDirectory); err != nil {
		return fail("directory synchronization", err)
	}
	if err := cleanup(); err != nil {
		return fmt.Errorf("clean %s root capability probe: %w", root.role, err)
	}
	return root.VerifyPath()
}

type WorkspaceRootLayout struct {
	Plan                *VerifiedRoot
	Runtime             *VerifiedRoot
	Target              *VerifiedRoot
	GitCommon           *VerifiedRoot
	Worktree            *VerifiedRoot
	RegisteredWorktrees []*VerifiedRoot
}

func ValidateWorkspaceRootLayout(layout WorkspaceRootLayout) error {
	required := []struct {
		name string
		role RootRole
		root *VerifiedRoot
	}{
		{"plan", RootRolePlan, layout.Plan},
		{"runtime", RootRoleRuntime, layout.Runtime},
		{"target", RootRoleTarget, layout.Target},
		{"Git common directory", RootRoleGitCommon, layout.GitCommon},
		{"worktree", RootRoleWorktree, layout.Worktree},
	}
	for _, item := range required {
		if item.root == nil || item.root.Role() != item.role {
			return fmt.Errorf("workspace root layout requires a verified %s root", item.name)
		}
		if err := item.root.VerifyPath(); err != nil {
			return err
		}
	}
	for index, registered := range layout.RegisteredWorktrees {
		if registered == nil || registered.Role() != RootRoleRegisteredWorktree {
			return fmt.Errorf("registered worktree %d is not a verified registered-worktree root", index)
		}
		if err := registered.VerifyPath(); err != nil {
			return err
		}
	}

	all := []*VerifiedRoot{layout.Plan, layout.Runtime, layout.Target, layout.GitCommon, layout.Worktree}
	all = append(all, layout.RegisteredWorktrees...)
	for leftIndex := 0; leftIndex < len(all); leftIndex++ {
		for rightIndex := leftIndex + 1; rightIndex < len(all); rightIndex++ {
			left, right := all[leftIndex], all[rightIndex]
			if allowedWorkspaceRootOverlap(layout, left, right) {
				continue
			}
			if rootsOverlap(left, right) {
				return fmt.Errorf(
					"unsafe workspace root overlap: %s root %s and %s root %s",
					left.Role(), left.Path(), right.Role(), right.Path(),
				)
			}
		}
	}
	return nil
}

func allowedWorkspaceRootOverlap(layout WorkspaceRootLayout, left, right *VerifiedRoot) bool {
	if (left == layout.Target && right == layout.GitCommon) ||
		(right == layout.Target && left == layout.GitCommon) {
		target, common := layout.Target, layout.GitCommon
		return common.Path() == filepath.Join(target.Path(), ".git")
	}
	if left == layout.GitCommon && right.Role() == RootRoleRegisteredWorktree ||
		right == layout.GitCommon && left.Role() == RootRoleRegisteredWorktree {
		registered := right
		if right == layout.GitCommon {
			registered = left
		}
		return layout.GitCommon.Path() == filepath.Join(registered.Path(), ".git")
	}
	if left == layout.Target && right.Role() == RootRoleRegisteredWorktree ||
		right == layout.Target && left.Role() == RootRoleRegisteredWorktree {
		registered := right
		if right == layout.Target {
			registered = left
		}
		return registered.Path() == layout.Target.Path() &&
			registered.Identity() == layout.Target.Identity()
	}
	return false
}

func rootsOverlap(left, right *VerifiedRoot) bool {
	if left.Identity() == right.Identity() {
		return true
	}
	return pathContains(left.Path(), right.Path()) || pathContains(right.Path(), left.Path())
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." {
		return relative == "."
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
