package workspace

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type AttemptIdentity struct {
	attemptID ID
}

func DeriveAttemptIdentity(
	workspaceID ID,
	generation Digest,
	mergeUnit MergeUnitReference,
	attemptNumber uint64,
	base GitObjectID,
) (AttemptIdentity, error) {
	attemptID, err := deriveAttemptIdentity(
		workspaceID, generation, mergeUnit, attemptNumber, base,
	)
	if err != nil {
		return AttemptIdentity{}, err
	}
	return AttemptIdentity{attemptID: attemptID}, nil
}

func (identity AttemptIdentity) AttemptID() ID { return identity.attemptID }

func deriveAttemptIdentity(
	workspaceID ID,
	generation Digest,
	mergeUnit MergeUnitReference,
	attemptNumber uint64,
	base GitObjectID,
) (ID, error) {
	if workspaceID.IsZero() || generation.IsZero() ||
		mergeUnit.planID.IsZero() || mergeUnit.mergeUnitID.IsZero() ||
		attemptNumber == 0 || base.IsZero() {
		return ID{}, fmt.Errorf(
			"attempt identity requires workspace, generation, merge unit, positive attempt, and base",
		)
	}
	baseSHA := hex.EncodeToString(base.Bytes())
	bindings := fmt.Sprintf(
		"workspace_id=%s\ngeneration=%s\nplan_id=%s\nmerge_unit_id=%s\nattempt=%d\nbase_sha=%s\n",
		workspaceID, generation, mergeUnit.planID, mergeUnit.mergeUnitID, attemptNumber, baseSHA,
	)
	digestHex := hex.EncodeToString(DigestBytes([]byte(bindings)).Bytes())
	attemptID, err := NewID("attempt-" + digestHex[:16])
	if err != nil {
		return ID{}, err
	}
	return attemptID, nil
}

func AttemptWorktreePath(root string, identity AttemptIdentity, mergeUnit MergeUnitReference, attemptNumber uint64) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) || identity.attemptID.IsZero() || mergeUnit.mergeUnitID.IsZero() || attemptNumber == 0 {
		return "", fmt.Errorf("attempt worktree path requires an absolute root and complete attempt identity")
	}
	digestSuffix := strings.TrimPrefix(identity.attemptID.String(), "attempt-")
	suffix := "-a" + strconv.FormatUint(attemptNumber, 10) + "-" + digestSuffix
	nameBudget := 180 - len(suffix)
	name := mergeUnit.mergeUnitID.String()
	if len(name) > nameBudget {
		name = strings.TrimRight(name[:nameBudget], "-.")
	}
	path := filepath.Clean(filepath.Join(root, name+suffix))
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("attempt worktree path escapes its root")
	}
	if err := validateBoundedText("attempt worktree", path, 4096); err != nil {
		return "", err
	}
	return path, nil
}
