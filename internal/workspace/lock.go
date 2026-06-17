package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	LockFileName      = "feature.workspace.lock.json"
	lockSchemaVersion = 1
	planLockFileName  = "feature.plan.lock.json"
)

type ValidateOptions struct {
	WorkspaceDir string
	WriteLock    bool
}

type ValidateResult struct {
	Status       string        `json:"status"`
	WorkspaceDir string        `json:"workspace_dir"`
	LockPath     string        `json:"lock_path,omitempty"`
	Lock         WorkspaceLock `json:"lock,omitempty"`
}

type WorkspaceLock struct {
	SchemaVersion int                 `json:"schema_version"`
	WorkspaceID   string              `json:"workspace_id"`
	Repo          string              `json:"repo"`
	BaseRef       string              `json:"base_ref"`
	Remote        string              `json:"remote"`
	Plans         []WorkspacePlanLock `json:"plans"`
}

type WorkspacePlanLock struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	LockPath string `json:"lock_path"`
	LockHash string `json:"lock_hash"`
}

func Validate(opts ValidateOptions) (ValidateResult, error) {
	workspaceDir := opts.WorkspaceDir
	if workspaceDir == "" {
		return ValidateResult{}, fmt.Errorf("workspace validate requires <workspace-dir>")
	}
	manifest, err := ReadManifest(filepath.Join(workspaceDir, ManifestFileName))
	if err != nil {
		return ValidateResult{}, err
	}
	lock, err := BuildLock(workspaceDir, manifest)
	if err != nil {
		return ValidateResult{}, err
	}
	lockPath := filepath.Join(workspaceDir, LockFileName)
	if opts.WriteLock {
		if err := writeStableJSON(lockPath, lock); err != nil {
			return ValidateResult{}, err
		}
		return ValidateResult{Status: "valid", WorkspaceDir: workspaceDir, LockPath: lockPath, Lock: lock}, nil
	}
	return ValidateResult{Status: "valid", WorkspaceDir: workspaceDir, Lock: lock}, nil
}

func BuildLock(workspaceDir string, manifest WorkspaceManifest) (WorkspaceLock, error) {
	lock := WorkspaceLock{
		SchemaVersion: lockSchemaVersion,
		WorkspaceID:   manifest.ID,
		Repo:          manifest.Repo,
		BaseRef:       manifest.BaseRef,
		Remote:        manifest.Remote,
	}
	for _, plan := range manifest.Plans {
		planDir := resolveWorkspacePath(workspaceDir, plan.Path)
		lockPath := filepath.Join(planDir, planLockFileName)
		hash, err := hashCanonicalJSONFile(lockPath)
		if err != nil {
			return WorkspaceLock{}, fmt.Errorf("plan %s lock: %w", plan.ID, err)
		}
		lock.Plans = append(lock.Plans, WorkspacePlanLock{
			ID:       plan.ID,
			Path:     plan.Path,
			LockPath: lockPath,
			LockHash: hash,
		})
	}
	return lock, nil
}

func resolveWorkspacePath(workspaceDir string, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workspaceDir, path))
}

func hashCanonicalJSONFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return "", fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func writeStableJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
