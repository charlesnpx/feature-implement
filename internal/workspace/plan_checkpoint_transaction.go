package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

const (
	planRepositoryTransactionLockName = "feature-plan-transaction.v1.lock"
	planRepositoryTransactionLockText = "feature-plan-transaction-lock:v1\n"
	planMainRefLockName               = "refs/heads/main.lock"
	planMainRefLockText               = "feature-plan-main-ref-lock:v1\n"
)

type planRepositoryTransaction struct {
	root *VerifiedRoot
	file *os.File
}

type planMainRefExclusion struct {
	root *VerifiedRoot
	file *os.File
}

func acquirePlanRepositoryTransaction(
	root *VerifiedRoot,
) (*planRepositoryTransaction, error) {
	if root == nil || root.Role() != RootRoleGitCommon {
		return nil, fmt.Errorf("verified plan Git directory is required")
	}
	content := []byte(planRepositoryTransactionLockText)
	if _, exists, err := root.adapter.inspectExact(
		planRepositoryTransactionLockName,
	); err != nil {
		return nil, err
	} else if !exists {
		if err := root.WriteExclusive(
			planRepositoryTransactionLockName,
			content,
			0o600,
		); err != nil {
			if _, appeared, inspectErr := root.adapter.inspectExact(
				planRepositoryTransactionLockName,
			); inspectErr != nil {
				return nil, errors.Join(err, inspectErr)
			} else if !appeared {
				return nil, err
			}
		}
	}
	file, err := openAndFlockPlanGitFile(
		root,
		planRepositoryTransactionLockName,
		content,
	)
	if err != nil {
		return nil, fmt.Errorf("acquire plan repository transaction: %w", err)
	}
	return &planRepositoryTransaction{root: root, file: file}, nil
}

func (transaction *planRepositoryTransaction) Close() error {
	if transaction == nil || transaction.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(
		int(transaction.file.Fd()),
		syscall.LOCK_UN,
	)
	closeErr := transaction.file.Close()
	transaction.file = nil
	return errors.Join(unlockErr, closeErr)
}

func acquirePlanMainRefExclusion(
	root *VerifiedRoot,
) (*planMainRefExclusion, error) {
	if root == nil || root.Role() != RootRoleGitCommon {
		return nil, fmt.Errorf("verified plan Git directory is required")
	}
	content := []byte(planMainRefLockText)
	if _, exists, err := root.adapter.inspectExact(planMainRefLockName); err != nil {
		return nil, err
	} else if !exists {
		if err := root.WriteExclusive(
			planMainRefLockName,
			content,
			0o600,
		); err != nil {
			if _, appeared, inspectErr := root.adapter.inspectExact(
				planMainRefLockName,
			); inspectErr != nil {
				return nil, errors.Join(err, inspectErr)
			} else if !appeared {
				return nil, err
			}
		}
	}
	file, err := openAndFlockPlanGitFile(
		root,
		planMainRefLockName,
		content,
	)
	if err != nil {
		return nil, fmt.Errorf("acquire plan main ref exclusion: %w", err)
	}
	return &planMainRefExclusion{root: root, file: file}, nil
}

func (exclusion *planMainRefExclusion) Close() error {
	if exclusion == nil || exclusion.file == nil {
		return nil
	}
	removed, removeErr := exclusion.root.adapter.removeFileContentExact(
		planMainRefLockName,
		[]byte(planMainRefLockText),
		int64(len(planMainRefLockText)),
		exclusion.root.VerifyPath,
	)
	if removeErr == nil && !removed {
		removeErr = fmt.Errorf("plan main ref exclusion is missing")
	}
	unlockErr := syscall.Flock(
		int(exclusion.file.Fd()),
		syscall.LOCK_UN,
	)
	closeErr := exclusion.file.Close()
	exclusion.file = nil
	return errors.Join(removeErr, unlockErr, closeErr)
}

func recoverAbandonedPlanMainRefExclusion(root *VerifiedRoot) error {
	content, err := root.ReadBounded(
		planMainRefLockName,
		int64(len(planMainRefLockText)),
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(content, []byte(planMainRefLockText)) {
		return fmt.Errorf(
			"plan repository main ref is locked by another Git operation",
		)
	}
	exclusion, err := acquirePlanMainRefExclusion(root)
	if err != nil {
		return err
	}
	return exclusion.Close()
}

func openAndFlockPlanGitFile(
	root *VerifiedRoot,
	relative string,
	expected []byte,
) (*os.File, error) {
	file, _, err := root.adapter.openRegularFileExact(
		relative,
		os.O_RDWR,
		0,
		false,
	)
	if err != nil {
		return nil, err
	}
	fail := func(lockErr error) (*os.File, error) {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, lockErr
	}
	if err := syscall.Flock(
		int(file.Fd()),
		syscall.LOCK_EX|syscall.LOCK_NB,
	); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) ||
			errors.Is(err, syscall.EAGAIN) {
			return fail(fmt.Errorf("plan repository transaction is active"))
		}
		return fail(err)
	}
	if err := root.verifyOwnedRegularFile(relative, file); err != nil {
		return fail(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	content, err := io.ReadAll(io.LimitReader(
		file,
		int64(len(expected))+1,
	))
	if err != nil {
		return fail(err)
	}
	if !bytes.Equal(content, expected) {
		return fail(fmt.Errorf("plan Git lock %s has unexpected content", relative))
	}
	return file, nil
}
