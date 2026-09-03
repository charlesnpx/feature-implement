package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// MaxWorkspaceLockBytes bounds the one canonical, committed definition lock.
const MaxWorkspaceLockBytes = MaxArtifactBytes

const workspaceLockPublicationLockName = WorkspaceLockFileName + ".publication.lock"

type WorkspaceLockWriteFaultPoint string

const (
	WorkspaceLockFaultAfterTemporarySync       WorkspaceLockWriteFaultPoint = "after_temporary_sync"
	WorkspaceLockFaultPublicationLockContended WorkspaceLockWriteFaultPoint = "publication_lock_contended"
)

type WorkspaceLockWriteOptions struct {
	FaultInjector func(WorkspaceLockWriteFaultPoint) error
}

type WorkspaceLockWriteResult struct {
	created bool
	updated bool
}

func (result WorkspaceLockWriteResult) Created() bool { return result.created }
func (result WorkspaceLockWriteResult) Updated() bool { return result.updated }

type workspaceLockArtifactWire struct {
	Kind         ArtifactKind `json:"kind"`
	ID           string       `json:"id"`
	Path         string       `json:"path"`
	SourceHash   string       `json:"source_hash"`
	SemanticHash string       `json:"semantic_hash"`
}

type workspaceLockWire struct {
	SchemaVersion int                         `json:"schema_version"`
	WorkspaceID   string                      `json:"workspace_id"`
	Generation    string                      `json:"generation"`
	Artifacts     []workspaceLockArtifactWire `json:"artifacts"`
}

// WorkspaceBundleLockBytes is the sole authoritative generated artifact for a
// validated workspace bundle. It binds the normalized definition, including
// every referenced source artifact, without projecting a per-plan file tree.
func WorkspaceBundleLockBytes(bundle WorkspaceBundle) ([]byte, error) {
	if bundle.definition.generation.IsZero() || bundle.root == "" {
		return nil, fmt.Errorf("validated workspace bundle is required")
	}
	artifacts := make([]workspaceLockArtifactWire, 0, len(bundle.definition.artifacts))
	for _, artifact := range bundle.definition.artifacts {
		artifacts = append(artifacts, workspaceLockArtifactWire{
			Kind:         artifact.kind,
			ID:           artifact.id.String(),
			Path:         artifact.path,
			SourceHash:   artifact.sourceHash.String(),
			SemanticHash: artifact.semanticHash.String(),
		})
	}
	content, err := json.Marshal(workspaceLockWire{
		SchemaVersion: 2,
		WorkspaceID:   bundle.definition.workspace.id.String(),
		Generation:    bundle.definition.generation.String(),
		Artifacts:     artifacts,
	})
	if err != nil {
		return nil, err
	}
	content = append(content, '\n')
	if len(content) > MaxWorkspaceLockBytes {
		return nil, fmt.Errorf("workspace lock exceeds %d bytes", MaxWorkspaceLockBytes)
	}
	return content, nil
}

// WriteWorkspaceBundleLock atomically installs the canonical lock. The prior
// lock is never replaced unless it is exactly the current committed lock. A
// crash before rename leaves that prior file intact and readable.
func WriteWorkspaceBundleLock(
	bundle WorkspaceBundle,
	options WorkspaceLockWriteOptions,
) (result WorkspaceLockWriteResult, resultErr error) {
	if err := bundle.VerifyRoot(); err != nil {
		return WorkspaceLockWriteResult{}, err
	}
	desired, err := WorkspaceBundleLockBytes(bundle)
	if err != nil {
		return WorkspaceLockWriteResult{}, err
	}
	root, err := OpenVerifiedRoot(RootRolePlan, bundle.root, false)
	if err != nil {
		return WorkspaceLockWriteResult{}, fmt.Errorf("open workspace lock root: %w", err)
	}
	defer root.Close()
	releasePublication, err := lockWorkspaceLockPublication(root, options.FaultInjector)
	if err != nil {
		return WorkspaceLockWriteResult{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, releasePublication())
	}()

	existing, exists, err := readWorkspaceLock(root)
	if err != nil {
		return WorkspaceLockWriteResult{}, err
	}
	if exists && bytes.Equal(existing, desired) {
		return WorkspaceLockWriteResult{}, nil
	}
	if exists {
		committed, err := committedWorkspaceLockBytes(context.Background(), bundle.root)
		if err != nil {
			return WorkspaceLockWriteResult{}, err
		}
		if !bytes.Equal(existing, committed) {
			return WorkspaceLockWriteResult{}, fmt.Errorf(
				"workspace lock differs unexpectedly from its committed value; regenerate from a clean plan checkout",
			)
		}
	}

	temporary, err := workspaceLockTemporaryName()
	if err != nil {
		return WorkspaceLockWriteResult{}, err
	}
	if err := root.adapter.writeFileExclusive(temporary, desired, 0o600); err != nil {
		return WorkspaceLockWriteResult{}, fmt.Errorf("write workspace lock temporary file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_, _ = root.adapter.removeFileContentExact(
				temporary, desired, int64(len(desired)), nil,
			)
		}
	}()
	if options.FaultInjector != nil {
		if err := options.FaultInjector(WorkspaceLockFaultAfterTemporarySync); err != nil {
			return WorkspaceLockWriteResult{}, fmt.Errorf("workspace lock fault after temporary sync: %w", err)
		}
	}
	if current, currentExists, readErr := readWorkspaceLock(root); readErr != nil {
		return WorkspaceLockWriteResult{}, readErr
	} else if currentExists != exists || (exists && !bytes.Equal(current, existing)) {
		return WorkspaceLockWriteResult{}, fmt.Errorf("workspace lock changed before atomic replacement")
	}
	if err := root.adapter.root.Rename(temporary, WorkspaceLockFileName); err != nil {
		return WorkspaceLockWriteResult{}, fmt.Errorf("atomically replace workspace lock: %w", err)
	}
	removeTemporary = false
	if err := root.adapter.syncDirectory("."); err != nil {
		return WorkspaceLockWriteResult{}, fmt.Errorf("synchronize workspace lock directory: %w", err)
	}
	stored, storedExists, err := readWorkspaceLock(root)
	if err != nil || !storedExists || !bytes.Equal(stored, desired) {
		if err == nil {
			err = errors.New("atomic workspace lock replacement did not retain expected bytes")
		}
		return WorkspaceLockWriteResult{}, err
	}
	return WorkspaceLockWriteResult{
		created: !exists, updated: exists,
	}, nil
}

func lockWorkspaceLockPublication(
	root *VerifiedRoot,
	faultInjector func(WorkspaceLockWriteFaultPoint) error,
) (func() error, error) {
	file, _, err := root.openOwnedRegularFile(
		workspaceLockPublicationLockName, os.O_RDWR, 0o600, true,
	)
	if err != nil {
		return nil, fmt.Errorf("open workspace lock publication lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire workspace lock publication lock: %w", err)
		}
		if faultInjector != nil {
			if faultErr := faultInjector(WorkspaceLockFaultPublicationLockContended); faultErr != nil {
				_ = file.Close()
				return nil, fmt.Errorf("workspace lock fault on publication-lock contention: %w", faultErr)
			}
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire workspace lock publication lock: %w", err)
		}
	}
	if err := root.verifyOwnedRegularFile(workspaceLockPublicationLockName, file); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("verify workspace lock publication lock: %w", err)
	}
	return func() error {
		verifyErr := root.verifyOwnedRegularFile(
			workspaceLockPublicationLockName, file,
		)
		unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		closeErr := file.Close()
		if verifyErr != nil {
			verifyErr = fmt.Errorf(
				"workspace lock publication lock was replaced before release: %w",
				verifyErr,
			)
		}
		if unlockErr != nil {
			unlockErr = fmt.Errorf("unlock workspace lock publication lock: %w", unlockErr)
		}
		if closeErr != nil {
			closeErr = fmt.Errorf("close workspace lock publication lock: %w", closeErr)
		}
		return errors.Join(verifyErr, unlockErr, closeErr)
	}, nil
}

func ReadWorkspaceBundleLock(bundle WorkspaceBundle) ([]byte, error) {
	if err := bundle.VerifyRoot(); err != nil {
		return nil, err
	}
	root, err := OpenVerifiedRoot(RootRolePlan, bundle.root, false)
	if err != nil {
		return nil, fmt.Errorf("open workspace lock root: %w", err)
	}
	defer root.Close()
	content, exists, err := readWorkspaceLock(root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("workspace lock %s is missing", WorkspaceLockFileName)
	}
	return content, nil
}

func readWorkspaceLock(root *VerifiedRoot) ([]byte, bool, error) {
	content, err := root.ReadBounded(WorkspaceLockFileName, MaxWorkspaceLockBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read workspace lock: %w", err)
	}
	if len(content) == 0 {
		return nil, false, fmt.Errorf("workspace lock is empty")
	}
	return content, true, nil
}

func committedWorkspaceLockBytes(ctx context.Context, root string) ([]byte, error) {
	head, err := committedPlanHead(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("read committed workspace lock authority: %w", err)
	}
	content, err := gitShowFile(ctx, root, head, WorkspaceLockFileName)
	if err != nil {
		return nil, fmt.Errorf("read committed workspace lock authority: %w", err)
	}
	if len(content) == 0 || len(content) > MaxWorkspaceLockBytes {
		return nil, fmt.Errorf("committed workspace lock has invalid size")
	}
	return content, nil
}

func workspaceLockTemporaryName() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate workspace lock temporary name: %w", err)
	}
	return WorkspaceLockFileName + ".tmp-" + hex.EncodeToString(nonce[:]), nil
}
