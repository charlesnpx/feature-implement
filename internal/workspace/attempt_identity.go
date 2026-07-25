package workspace

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const maxAttemptBranchBytes = 240

type AttemptIdentity struct {
	attemptID ID
	branch    string
}

func DeriveAttemptIdentity(
	workspaceID ID,
	generation Digest,
	mergeUnit MergeUnitReference,
	attemptNumber uint64,
	base GitObjectID,
) (AttemptIdentity, error) {
	attemptID, branch, err := deriveAttemptIdentity(
		workspaceID, generation, mergeUnit, attemptNumber, base,
	)
	if err != nil {
		return AttemptIdentity{}, err
	}
	return AttemptIdentity{attemptID: attemptID, branch: branch}, nil
}

func (identity AttemptIdentity) AttemptID() ID  { return identity.attemptID }
func (identity AttemptIdentity) Branch() string { return identity.branch }

func deriveAttemptIdentity(
	workspaceID ID,
	generation Digest,
	mergeUnit MergeUnitReference,
	attemptNumber uint64,
	base GitObjectID,
) (ID, string, error) {
	if workspaceID.IsZero() || generation.IsZero() ||
		mergeUnit.planID.IsZero() || mergeUnit.mergeUnitID.IsZero() ||
		attemptNumber == 0 || base.IsZero() {
		return ID{}, "", fmt.Errorf(
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
		return ID{}, "", err
	}
	suffix := "-a" + strconv.FormatUint(attemptNumber, 10) + "-" + digestHex[:12]
	slugBudget := maxAttemptBranchBytes - len("mu/") - len(suffix)
	if slugBudget < 1 {
		return ID{}, "", fmt.Errorf("attempt number leaves no branch identity budget")
	}
	slug := mergeUnit.planID.String() + "-" + mergeUnit.mergeUnitID.String()
	if len(slug) > slugBudget {
		slug = strings.TrimRight(slug[:slugBudget], "-.")
	}
	if slug == "" {
		return ID{}, "", fmt.Errorf("attempt branch identity is empty after bounding")
	}
	branch := "mu/" + slug + suffix
	if len(branch) > maxAttemptBranchBytes || strings.Count(branch, "/") != 1 {
		return ID{}, "", fmt.Errorf("attempt branch is not flat and length bounded")
	}
	return attemptID, branch, nil
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

type AttemptRefConflictKind string

const (
	AttemptRefExact      AttemptRefConflictKind = "exact"
	AttemptRefAncestor   AttemptRefConflictKind = "ancestor"
	AttemptRefDescendant AttemptRefConflictKind = "descendant"
)

type AttemptRefConflict struct {
	kind      AttemptRefConflictKind
	existing  string
	candidate string
}

func (conflict AttemptRefConflict) Error() string {
	return fmt.Sprintf(
		"attempt branch %q has a %s local ref conflict with %q",
		conflict.candidate, conflict.kind, conflict.existing,
	)
}

func (conflict AttemptRefConflict) Kind() AttemptRefConflictKind { return conflict.kind }
func (conflict AttemptRefConflict) Existing() string             { return conflict.existing }
func (conflict AttemptRefConflict) Candidate() string            { return conflict.candidate }

func CheckAttemptRefConflicts(candidate string, refs []string, allowExact bool) error {
	if err := validateAttemptBranchSyntax(candidate); err != nil {
		return err
	}
	refs = append([]string(nil), refs...)
	sort.Strings(refs)
	for _, raw := range refs {
		existing, err := normalizeHeadRef(raw)
		if err != nil {
			return fmt.Errorf("inspect local attempt refs: %w", err)
		}
		kind := AttemptRefConflictKind("")
		switch {
		case existing == candidate:
			if allowExact {
				continue
			}
			kind = AttemptRefExact
		case strings.HasPrefix(candidate, existing+"/"):
			kind = AttemptRefAncestor
		case strings.HasPrefix(existing, candidate+"/"):
			kind = AttemptRefDescendant
		}
		if kind != "" {
			return AttemptRefConflict{kind: kind, existing: existing, candidate: candidate}
		}
	}
	return nil
}

func normalizeHeadRef(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "refs/heads/")
	if err := validateAttemptBranchSyntax(value); err != nil {
		return "", err
	}
	return value, nil
}

func validateAttemptBranchSyntax(branch string) error {
	if branch == "" || len(branch) > maxAttemptBranchBytes || strings.TrimSpace(branch) != branch ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\\x00") {
		return fmt.Errorf("invalid attempt branch %q", branch)
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("invalid attempt branch %q", branch)
		}
	}
	return nil
}
