package workspace

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// reconcilePreparedPlanLockInventory rolls back the durable worktree marker
// left by an interrupted lock checkpoint before any request-specific
// materialization changes occur. The committed HEAD inventory is the durable
// pre-lock state. Publishing it first makes a subsequent lock regeneration or
// retired-lock synchronization safe even if this process is interrupted
// again.
func (adapter planCheckpointGitAdapter) reconcilePreparedPlanLockInventory(
	ctx context.Context,
	root *VerifiedRoot,
	head planCheckpointCommit,
) error {
	if head.id.IsZero() ||
		(head.metadata.kind != PlanCheckpointInitial &&
			head.metadata.kind != PlanCheckpointRevision) {
		return nil
	}
	current, exists, err := currentInventoryBytes(root)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("initialized plan repository is missing its inventory")
	}
	committed, err := adapter.readCommitPath(
		ctx,
		head.id,
		PlanRepositoryInventoryFileName,
	)
	if err != nil {
		return err
	}
	if bytes.Equal(current, committed) {
		return nil
	}
	if err := verifyPreparedPlanLockInventory(root, committed, current); err != nil {
		return fmt.Errorf("plan repository inventory has uncheckpointed changes: %w", err)
	}
	observed, err := adapter.inspectHead(ctx)
	if err != nil {
		return err
	}
	if observed.id != head.id {
		return fmt.Errorf("plan repository HEAD moved before prepared lock recovery")
	}
	latest, exists, err := currentInventoryBytes(root)
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(latest, current) {
		return fmt.Errorf("prepared plan lock inventory changed before recovery")
	}
	if err := root.PublishReplaceable(
		PlanRepositoryInventoryFileName,
		committed,
		0o600,
		maxPlanRepositoryInventoryBytes,
		PublicationOptions{},
	); err != nil {
		return fmt.Errorf("recover prepared plan lock inventory: %w", err)
	}
	observed, err = adapter.inspectHead(ctx)
	if err != nil {
		return err
	}
	if observed.id != head.id {
		return fmt.Errorf("plan repository HEAD moved during prepared lock recovery")
	}
	return root.VerifyPath()
}

func verifyPreparedPlanLockInventory(
	root *VerifiedRoot,
	committedBytes []byte,
	preparedBytes []byte,
) error {
	committed, err := parsePlanRepositoryInventory(committedBytes)
	if err != nil {
		return fmt.Errorf("decode committed plan inventory: %w", err)
	}
	prepared, err := parsePlanRepositoryInventory(preparedBytes)
	if err != nil {
		return fmt.Errorf("decode prepared plan inventory: %w", err)
	}
	base := planInventoryBasePaths(committed)
	if !equalPlanRepositoryPaths(base, planInventoryBasePaths(prepared)) {
		return fmt.Errorf("prepared lock inventory changes committed source paths")
	}
	if !planInventoryHasLockPaths(prepared) {
		return fmt.Errorf("unexpected inventory is not a prepared lock inventory")
	}

	materializationBytes, err := root.adapter.readBounded(
		path.Join(WorkspaceGeneratedDirectory, MaterializationInventoryFileName),
		MaxMaterializationControlBytes,
	)
	if err != nil {
		return fmt.Errorf("read prepared lock materialization inventory: %w", err)
	}
	materialization, err := parseMaterializationInventory(materializationBytes)
	if err != nil {
		return err
	}
	if materialization.generatorVersion != PlanCheckpointGeneratorVersion {
		return fmt.Errorf(
			"prepared locks were not produced by %s",
			PlanCheckpointGeneratorVersion,
		)
	}
	if err := verifyPlanMaterializationControl(root, materialization); err != nil {
		return err
	}

	if isRetiredPlanLockInventory(materialization) {
		if err := verifyPreparedInventoryGeneratedPaths(prepared); err != nil {
			return err
		}
		return verifyRetiredPlanLockFilesAtRoot(root, materialization)
	}
	if len(materialization.artifacts) == 0 {
		return fmt.Errorf("prepared lock materialization contains no lock artifacts")
	}
	expected := append([]planRepositoryPathWire(nil), base...)
	for _, artifact := range materialization.artifacts {
		relative := path.Join(WorkspaceGeneratedDirectory, artifact.path)
		content, err := root.ReadBounded(relative, MaxMaterializationArtifactBytes)
		if err != nil {
			return fmt.Errorf("read prepared lock %s: %w", relative, err)
		}
		if DigestBytes(content) != artifact.lastGeneratedHash {
			return fmt.Errorf("prepared lock %s changed after generation", relative)
		}
		expected = append(expected, planRepositoryPathWire{
			Role: planRepositoryPathLock, Path: relative, Tracked: true,
		})
	}
	for _, relative := range planLockAdministrativePaths(materialization) {
		expected = append(expected, planRepositoryPathWire{
			Role: planRepositoryPathAdministrative, Path: relative,
		})
	}
	sortPlanRepositoryPaths(expected)
	if !equalPlanRepositoryPaths(expected, prepared.paths) {
		return fmt.Errorf("prepared lock inventory does not match its owned materialization")
	}
	return nil
}

func planInventoryBasePaths(
	inventory planRepositoryInventory,
) []planRepositoryPathWire {
	result := make([]planRepositoryPathWire, 0, len(inventory.paths))
	for _, item := range inventory.paths {
		if item.Role == planRepositoryPathInventory ||
			item.Role == planRepositoryPathSource {
			result = append(result, item)
		}
	}
	return result
}

func planInventoryHasLockPaths(inventory planRepositoryInventory) bool {
	for _, item := range inventory.paths {
		if item.Role == planRepositoryPathLock {
			return true
		}
	}
	return false
}

func verifyPreparedInventoryGeneratedPaths(
	inventory planRepositoryInventory,
) error {
	for _, item := range inventory.paths {
		if item.Role == planRepositoryPathInventory ||
			item.Role == planRepositoryPathSource {
			continue
		}
		if !strings.HasPrefix(
			item.Path,
			WorkspaceGeneratedDirectory+"/",
		) {
			return fmt.Errorf(
				"prepared lock inventory contains non-generated path %s",
				item.Path,
			)
		}
		switch item.Role {
		case planRepositoryPathLock, planRepositoryPathAdministrative:
		default:
			return fmt.Errorf(
				"prepared lock inventory path %s has unexpected role %s",
				item.Path,
				item.Role,
			)
		}
	}
	return nil
}

func isRetiredPlanLockInventory(inventory MaterializationInventory) bool {
	return len(inventory.artifacts) == 1 &&
		len(inventory.directories) == 0 &&
		inventory.artifacts[0].id == planRetiredLockArtifactID &&
		inventory.artifacts[0].path == planRetiredLockFileName
}

func verifyRetiredPlanLockFilesAtRoot(
	root *VerifiedRoot,
	inventory MaterializationInventory,
) error {
	artifact := inventory.artifacts[0]
	content, err := root.ReadBounded(
		path.Join(WorkspaceGeneratedDirectory, artifact.path),
		MaxMaterializationArtifactBytes,
	)
	if err != nil {
		return err
	}
	if DigestBytes(content) != artifact.lastGeneratedHash ||
		!bytes.Equal(content, []byte(planRetiredLockContent)) {
		return fmt.Errorf("retired generated lock sentinel changed")
	}
	return nil
}

func sortPlanRepositoryPaths(paths []planRepositoryPathWire) {
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Path != paths[j].Path {
			return paths[i].Path < paths[j].Path
		}
		return paths[i].Role < paths[j].Role
	})
}

func equalPlanRepositoryPaths(
	left []planRepositoryPathWire,
	right []planRepositoryPathWire,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
