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
)

// MaxWorkspaceLockBytes bounds the one canonical, committed definition lock.
const MaxWorkspaceLockBytes = MaxArtifactBytes

type WorkspaceLockWriteFaultPoint string

const WorkspaceLockFaultAfterTemporarySync WorkspaceLockWriteFaultPoint = "after_temporary_sync"

type WorkspaceLockWriteOptions struct {
	FaultInjector func(WorkspaceLockWriteFaultPoint) error
}

type WorkspaceLockWriteResult struct {
	created bool
	updated bool
	digest  Digest
}

func (result WorkspaceLockWriteResult) Created() bool  { return result.created }
func (result WorkspaceLockWriteResult) Updated() bool  { return result.updated }
func (result WorkspaceLockWriteResult) Digest() Digest { return result.digest }

// WorkspaceBundleLockBytes is the sole authoritative generated artifact for a
// validated workspace bundle. It binds the normalized definition, including
// every referenced source artifact, without projecting a per-plan file tree.
func WorkspaceBundleLockBytes(bundle WorkspaceBundle) ([]byte, error) {
	if bundle.definition.generation.IsZero() || bundle.root == "" {
		return nil, fmt.Errorf("validated workspace bundle is required")
	}
	content, err := json.Marshal(ProjectWorkspaceLock(bundle.definition))
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
) (WorkspaceLockWriteResult, error) {
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

	existing, exists, err := readWorkspaceLock(root)
	if err != nil {
		return WorkspaceLockWriteResult{}, err
	}
	if exists && bytes.Equal(existing, desired) {
		return WorkspaceLockWriteResult{digest: DigestBytes(desired)}, nil
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
		created: !exists, updated: exists, digest: DigestBytes(desired),
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
