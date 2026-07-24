package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	planIndexFileName          = "index"
	planIndexLockFileName      = "index.lock"
	planIndexSyncIntentName    = "feature-plan-index-sync.v1.json"
	planIndexPreviousFileName  = "feature-plan-index.previous.v1"
	planIndexSyncSchemaVersion = 1
	maxPlanIndexBytes          = 64 << 20
	maxPlanIndexIntentBytes    = 16 << 10
)

type planIndexSyncIntentWire struct {
	SchemaVersion int    `json:"schema_version"`
	TargetCommit  string `json:"target_commit"`
	PriorExists   bool   `json:"prior_exists"`
	PriorDigest   string `json:"prior_digest"`
	DesiredDigest string `json:"desired_digest"`
}

type planIndexSyncIntent struct {
	target        GitObjectID
	priorExists   bool
	priorDigest   Digest
	desiredDigest Digest
	content       []byte
}

type planIndexSnapshot struct {
	exists  bool
	content []byte
	digest  Digest
}

func (snapshot planIndexSnapshot) matches(exists bool, digest Digest) bool {
	if snapshot.exists != exists {
		return false
	}
	if !exists {
		return true
	}
	return snapshot.digest == digest
}

func (adapter planCheckpointGitAdapter) synchronizeIndex(
	ctx context.Context,
	commit planCheckpointCommit,
	fault PlanCheckpointFaultInjector,
) error {
	if err := adapter.recoverIndexSynchronization(ctx); err != nil {
		return err
	}
	current, err := adapter.inspectHead(ctx)
	if err != nil {
		return err
	}
	if current.id != commit.id {
		return fmt.Errorf("plan repository HEAD moved before index recovery")
	}
	matchesCommit, err := adapter.indexMatchesCommit(ctx, commit.id)
	if err != nil {
		return err
	}
	if matchesCommit {
		return nil
	}
	recoverable, err := adapter.indexRecoverableFromCommit(ctx, commit)
	if err != nil {
		return err
	}
	if !recoverable {
		return fmt.Errorf(
			"plan repository index contains staged changes; refusing destructive recovery",
		)
	}
	desired, err := adapter.renderPlanIndex(ctx, commit.id)
	if err != nil {
		return err
	}
	gitRoot := adapter.gitDirectory.root
	prior, err := readPlanIndexSnapshot(gitRoot, planIndexFileName)
	if err != nil {
		return err
	}
	if _, exists, err := gitRoot.adapter.inspectExact(planIndexSyncIntentName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("plan index synchronization intent appeared during recovery")
	}
	if _, exists, err := gitRoot.adapter.inspectExact(planIndexPreviousFileName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("plan index synchronization has an unexpected previous index")
	}
	if _, exists, err := gitRoot.adapter.inspectExact(planIndexLockFileName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("plan repository index is locked by another Git operation")
	}
	intent, err := newPlanIndexSyncIntent(commit.id, prior, desired)
	if err != nil {
		return err
	}
	if err := gitRoot.WriteExclusive(
		planIndexSyncIntentName,
		intent.content,
		0o600,
	); err != nil {
		return fmt.Errorf("publish plan index synchronization intent: %w", err)
	}
	if err := gitRoot.WriteExclusive(planIndexLockFileName, desired, 0o600); err != nil {
		cleanupErr := removePlanIndexIntent(gitRoot, intent)
		return errors.Join(
			fmt.Errorf("acquire plan Git index lock: %w", err),
			cleanupErr,
		)
	}
	lockedPrior, err := readPlanIndexSnapshot(gitRoot, planIndexFileName)
	if err != nil {
		return errors.Join(err, cleanupPlanIndexBeforeEffect(gitRoot, intent))
	}
	if !lockedPrior.matches(prior.exists, prior.digest) {
		return errors.Join(
			fmt.Errorf("plan repository index changed before its lock was acquired"),
			cleanupPlanIndexBeforeEffect(gitRoot, intent),
		)
	}
	matchesCommit, err = adapter.indexMatchesCommit(ctx, commit.id)
	if err != nil {
		return errors.Join(err, cleanupPlanIndexBeforeEffect(gitRoot, intent))
	}
	if matchesCommit {
		return cleanupPlanIndexBeforeEffect(gitRoot, intent)
	}
	recoverable, err = adapter.indexRecoverableFromCommit(ctx, commit)
	if err != nil {
		return errors.Join(err, cleanupPlanIndexBeforeEffect(gitRoot, intent))
	}
	if !recoverable {
		return errors.Join(
			fmt.Errorf(
				"plan repository index contains staged changes; refusing destructive recovery",
			),
			cleanupPlanIndexBeforeEffect(gitRoot, intent),
		)
	}
	if err := injectPlanCheckpointFault(
		fault,
		PlanCheckpointFaultAfterIndexLock,
	); err != nil {
		return errors.Join(err, cleanupPlanIndexBeforeEffect(gitRoot, intent))
	}
	current, err = adapter.inspectHead(ctx)
	if err != nil || current.id != commit.id {
		if err == nil {
			err = fmt.Errorf("plan repository HEAD moved before index publication")
		}
		return errors.Join(err, cleanupPlanIndexBeforeEffect(gitRoot, intent))
	}
	if prior.exists {
		if err := gitRoot.adapter.renameFileNoReplace(
			planIndexFileName,
			planIndexPreviousFileName,
		); err != nil {
			return err
		}
	}
	if err := injectPlanCheckpointFault(
		fault,
		PlanCheckpointFaultAfterIndexQuarantine,
	); err != nil {
		return err
	}
	current, err = adapter.inspectHead(ctx)
	if err != nil || current.id != commit.id {
		if err == nil {
			err = fmt.Errorf("plan repository HEAD moved during index publication")
		}
		recoveryErr := adapter.recoverIndexSynchronization(ctx)
		return errors.Join(err, recoveryErr)
	}
	if err := gitRoot.adapter.renameFileNoReplace(
		planIndexLockFileName,
		planIndexFileName,
	); err != nil {
		return err
	}
	if err := finishPlanIndexSynchronization(gitRoot, intent); err != nil {
		return err
	}
	return adapter.gitDirectory.Verify()
}

func (adapter planCheckpointGitAdapter) indexRecoverableFromCommit(
	ctx context.Context,
	commit planCheckpointCommit,
) (bool, error) {
	switch len(commit.parents) {
	case 0:
		return adapter.indexIsEmpty(ctx)
	case 1:
		return adapter.indexMatchesCommit(ctx, commit.parents[0])
	default:
		return false, fmt.Errorf(
			"plan checkpoint index recovery requires at most one parent",
		)
	}
}

func (adapter planCheckpointGitAdapter) planIndexSnapshotMatchesCommit(
	ctx context.Context,
	snapshot planIndexSnapshot,
	commit GitObjectID,
) (bool, error) {
	if commit.IsZero() {
		return false, fmt.Errorf("plan index comparison requires a commit")
	}
	indexPath, cleanup, err := materializePlanIndexSnapshot(snapshot)
	if err != nil {
		return false, err
	}
	defer cleanup()
	_, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{indexPath: indexPath},
		"diff-index",
		"--cached",
		"--quiet",
		rawPlanGitObject(commit),
		"--",
		":/",
	)
	if err != nil {
		return false, err
	}
	switch exitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf(
			"inspect plan repository index: Git exited with status %d: %s",
			exitCode,
			strings.TrimSpace(string(stderr)),
		)
	}
}

func (adapter planCheckpointGitAdapter) planIndexSnapshotIsEmpty(
	ctx context.Context,
	snapshot planIndexSnapshot,
) (bool, error) {
	if !snapshot.exists {
		return true, nil
	}
	indexPath, cleanup, err := materializePlanIndexSnapshot(snapshot)
	if err != nil {
		return false, err
	}
	defer cleanup()
	stdout, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{indexPath: indexPath},
		"ls-files",
		"--cached",
		"-z",
		"--",
		":/",
	)
	if err != nil {
		return false, err
	}
	if exitCode != 0 {
		return false, fmt.Errorf(
			"inspect initial plan index: Git exited with status %d: %s",
			exitCode,
			strings.TrimSpace(string(stderr)),
		)
	}
	return len(stdout) == 0, nil
}

func materializePlanIndexSnapshot(
	snapshot planIndexSnapshot,
) (string, func(), error) {
	file, err := os.CreateTemp("", "feature-plan-index-inspect-*")
	if err != nil {
		return "", nil, err
	}
	indexPath := file.Name()
	cleanup := func() { _ = os.Remove(indexPath) }
	if !snapshot.exists {
		if err := file.Close(); err != nil {
			cleanup()
			return "", nil, err
		}
		if err := os.Remove(indexPath); err != nil {
			return "", nil, err
		}
		return indexPath, cleanup, nil
	}
	if _, err := file.Write(snapshot.content); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return indexPath, cleanup, nil
}

func (adapter planCheckpointGitAdapter) renderPlanIndex(
	ctx context.Context,
	commit GitObjectID,
) ([]byte, error) {
	file, err := os.CreateTemp("", "feature-plan-index-sync-*")
	if err != nil {
		return nil, err
	}
	indexPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(indexPath)
		return nil, err
	}
	if err := os.Remove(indexPath); err != nil {
		return nil, err
	}
	defer os.Remove(indexPath)
	_, stderr, exitCode, err := adapter.run(
		ctx,
		planGitRunOptions{indexPath: indexPath},
		"read-tree",
		rawPlanGitObject(commit),
	)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf(
				"Git exited with status %d: %s",
				exitCode,
				strings.TrimSpace(string(stderr)),
			)
		}
		return nil, fmt.Errorf("render plan repository index: %w", err)
	}
	rendered, err := os.Open(indexPath)
	if err != nil {
		return nil, fmt.Errorf("open rendered plan repository index: %w", err)
	}
	defer rendered.Close()
	info, err := rendered.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxPlanIndexBytes {
		return nil, fmt.Errorf("rendered plan repository index has an invalid size")
	}
	content, err := io.ReadAll(io.LimitReader(rendered, maxPlanIndexBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > maxPlanIndexBytes {
		return nil, fmt.Errorf("rendered plan repository index has an invalid size")
	}
	return content, nil
}

func newPlanIndexSyncIntent(
	target GitObjectID,
	prior planIndexSnapshot,
	desired []byte,
) (planIndexSyncIntent, error) {
	if target.IsZero() || len(desired) == 0 {
		return planIndexSyncIntent{}, fmt.Errorf(
			"plan index synchronization requires a target and desired index",
		)
	}
	wire := planIndexSyncIntentWire{
		SchemaVersion: planIndexSyncSchemaVersion,
		TargetCommit:  target.String(),
		PriorExists:   prior.exists,
		DesiredDigest: DigestBytes(desired).String(),
	}
	if prior.exists {
		wire.PriorDigest = prior.digest.String()
	}
	content, err := json.Marshal(wire)
	if err != nil {
		return planIndexSyncIntent{}, err
	}
	content = append(content, '\n')
	if len(content) > maxPlanIndexIntentBytes {
		return planIndexSyncIntent{}, fmt.Errorf(
			"plan index synchronization intent is too large",
		)
	}
	return parsePlanIndexSyncIntent(content)
}

func parsePlanIndexSyncIntent(content []byte) (planIndexSyncIntent, error) {
	if len(content) == 0 || len(content) > maxPlanIndexIntentBytes {
		return planIndexSyncIntent{}, fmt.Errorf(
			"plan index synchronization intent has an invalid size",
		)
	}
	if err := rejectDuplicateJSONObjectKeys(content); err != nil {
		return planIndexSyncIntent{}, err
	}
	var wire planIndexSyncIntentWire
	if err := decodeStrictJSONRequired(content, &wire); err != nil {
		return planIndexSyncIntent{}, err
	}
	if wire.SchemaVersion != planIndexSyncSchemaVersion {
		return planIndexSyncIntent{}, fmt.Errorf(
			"plan index synchronization intent has unsupported schema_version %d",
			wire.SchemaVersion,
		)
	}
	target, err := ParseGitObjectID(wire.TargetCommit)
	if err != nil {
		return planIndexSyncIntent{}, err
	}
	desired, err := ParseDigest(wire.DesiredDigest)
	if err != nil {
		return planIndexSyncIntent{}, err
	}
	prior := Digest{}
	if wire.PriorExists {
		prior, err = ParseDigest(wire.PriorDigest)
		if err != nil {
			return planIndexSyncIntent{}, err
		}
	} else if wire.PriorDigest != "" {
		return planIndexSyncIntent{}, fmt.Errorf(
			"plan index synchronization intent has a digest for a missing prior index",
		)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return planIndexSyncIntent{}, err
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(canonical, content) {
		return planIndexSyncIntent{}, fmt.Errorf(
			"plan index synchronization intent is not canonical",
		)
	}
	return planIndexSyncIntent{
		target: target, priorExists: wire.PriorExists,
		priorDigest: prior, desiredDigest: desired,
		content: append([]byte(nil), content...),
	}, nil
}

func (adapter planCheckpointGitAdapter) recoverIndexSynchronization(
	ctx context.Context,
) error {
	if adapter.gitDirectory == nil {
		return fmt.Errorf("retained plan Git directory is required")
	}
	gitRoot := adapter.gitDirectory.root
	content, err := gitRoot.ReadBounded(
		planIndexSyncIntentName,
		maxPlanIndexIntentBytes,
	)
	if errors.Is(err, os.ErrNotExist) {
		if _, exists, inspectErr := gitRoot.adapter.inspectExact(
			planIndexPreviousFileName,
		); inspectErr != nil {
			return inspectErr
		} else if exists {
			return fmt.Errorf(
				"plan index previous file exists without a synchronization intent",
			)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read plan index synchronization intent: %w", err)
	}
	intent, err := parsePlanIndexSyncIntent(content)
	if err != nil {
		return fmt.Errorf("decode plan index synchronization intent: %w", err)
	}
	index, err := readPlanIndexSnapshot(gitRoot, planIndexFileName)
	if err != nil {
		return err
	}
	lock, err := readPlanIndexSnapshot(gitRoot, planIndexLockFileName)
	if err != nil {
		return err
	}
	previous, err := readPlanIndexSnapshot(gitRoot, planIndexPreviousFileName)
	if err != nil {
		return err
	}
	current, err := adapter.inspectHead(ctx)
	if err != nil {
		return err
	}
	targetIsHead := current.id == intent.target
	indexPrior := index.matches(intent.priorExists, intent.priorDigest)
	indexDesired := index.matches(true, intent.desiredDigest)
	lockDesired := lock.matches(true, intent.desiredDigest)
	previousPrior := previous.matches(intent.priorExists, intent.priorDigest)

	if targetIsHead {
		switch {
		case indexDesired && !previous.exists &&
			(!lock.exists || lockDesired):
			if lock.exists {
				if err := removePlanIndexHash(
					gitRoot,
					planIndexLockFileName,
					intent.desiredDigest,
				); err != nil {
					return err
				}
			}
			return removePlanIndexIntent(gitRoot, intent)
		case indexDesired && previousPrior && !lock.exists:
			return finishPlanIndexSynchronization(gitRoot, intent)
		case indexPrior && lockDesired && !previous.exists:
			if intent.priorExists {
				if err := gitRoot.adapter.renameFileNoReplace(
					planIndexFileName,
					planIndexPreviousFileName,
				); err != nil {
					return err
				}
			}
			if err := gitRoot.adapter.renameFileNoReplace(
				planIndexLockFileName,
				planIndexFileName,
			); err != nil {
				return err
			}
			return finishPlanIndexSynchronization(gitRoot, intent)
		case !index.exists && lockDesired && previousPrior:
			if err := gitRoot.adapter.renameFileNoReplace(
				planIndexLockFileName,
				planIndexFileName,
			); err != nil {
				return err
			}
			return finishPlanIndexSynchronization(gitRoot, intent)
		case indexPrior && !lock.exists && !previous.exists:
			return removePlanIndexIntent(gitRoot, intent)
		case previousPrior && !lock.exists && index.exists:
			return finishPlanIndexSynchronization(gitRoot, intent)
		default:
			return fmt.Errorf(
				"plan index synchronization recovery state is unsafe for current HEAD",
			)
		}
	}

	switch {
	case indexPrior && lockDesired && !previous.exists:
		if err := removePlanIndexHash(
			gitRoot,
			planIndexLockFileName,
			intent.desiredDigest,
		); err != nil {
			return err
		}
		return removePlanIndexIntent(gitRoot, intent)
	case indexPrior && !lock.exists && !previous.exists:
		return removePlanIndexIntent(gitRoot, intent)
	case !index.exists && lockDesired && previousPrior:
		if intent.priorExists {
			if err := gitRoot.adapter.renameFileNoReplace(
				planIndexPreviousFileName,
				planIndexFileName,
			); err != nil {
				return err
			}
		}
		if err := removePlanIndexHash(
			gitRoot,
			planIndexLockFileName,
			intent.desiredDigest,
		); err != nil {
			return err
		}
		return removePlanIndexIntent(gitRoot, intent)
	case indexDesired && !lock.exists &&
		(previousPrior || (!intent.priorExists && !previous.exists)):
		if err := gitRoot.adapter.renameFileNoReplace(
			planIndexFileName,
			planIndexLockFileName,
		); err != nil {
			return err
		}
		if intent.priorExists {
			if err := gitRoot.adapter.renameFileNoReplace(
				planIndexPreviousFileName,
				planIndexFileName,
			); err != nil {
				return err
			}
		}
		if err := removePlanIndexHash(
			gitRoot,
			planIndexLockFileName,
			intent.desiredDigest,
		); err != nil {
			return err
		}
		return removePlanIndexIntent(gitRoot, intent)
	case previousPrior && !lock.exists && index.exists:
		return finishPlanIndexSynchronization(gitRoot, intent)
	default:
		return fmt.Errorf(
			"plan index synchronization recovery state is unsafe after HEAD moved",
		)
	}
}

func readPlanIndexSnapshot(
	root *VerifiedRoot,
	relative string,
) (planIndexSnapshot, error) {
	info, exists, err := root.adapter.inspectExact(relative)
	if err != nil {
		return planIndexSnapshot{}, err
	}
	if !exists {
		return planIndexSnapshot{}, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return planIndexSnapshot{}, fmt.Errorf(
			"plan Git administrative path %s is not a regular file",
			relative,
		)
	}
	content, err := root.ReadBounded(relative, maxPlanIndexBytes)
	if err != nil {
		return planIndexSnapshot{}, err
	}
	return planIndexSnapshot{
		exists: true, content: content, digest: DigestBytes(content),
	}, nil
}

func cleanupPlanIndexBeforeEffect(
	root *VerifiedRoot,
	intent planIndexSyncIntent,
) error {
	lockErr := removePlanIndexHash(
		root,
		planIndexLockFileName,
		intent.desiredDigest,
	)
	intentErr := removePlanIndexIntent(root, intent)
	return errors.Join(lockErr, intentErr)
}

func finishPlanIndexSynchronization(
	root *VerifiedRoot,
	intent planIndexSyncIntent,
) error {
	var previousErr error
	if intent.priorExists {
		previousErr = removePlanIndexHash(
			root,
			planIndexPreviousFileName,
			intent.priorDigest,
		)
	}
	intentErr := removePlanIndexIntent(root, intent)
	return errors.Join(previousErr, intentErr)
}

func removePlanIndexHash(
	root *VerifiedRoot,
	relative string,
	expected Digest,
) error {
	removed, err := root.adapter.removeFileHashExact(
		relative,
		expected.String(),
		maxPlanIndexBytes,
		root.VerifyPath,
	)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("plan Git administrative file %s is missing", relative)
	}
	return nil
}

func removePlanIndexIntent(
	root *VerifiedRoot,
	intent planIndexSyncIntent,
) error {
	removed, err := root.adapter.removeFileContentExact(
		planIndexSyncIntentName,
		intent.content,
		int64(len(intent.content)),
		root.VerifyPath,
	)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("plan index synchronization intent is missing")
	}
	return nil
}
