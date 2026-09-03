package workspace

import (
	"fmt"
)

type GitFileMode string

const (
	GitModeAbsent     GitFileMode = "000000"
	GitModeRegular    GitFileMode = "100644"
	GitModeExecutable GitFileMode = "100755"
	GitModeSymlink    GitFileMode = "120000"
	GitModeSubmodule  GitFileMode = "160000"
)

func (mode GitFileMode) valid() bool {
	switch mode {
	case GitModeAbsent, GitModeRegular, GitModeExecutable, GitModeSymlink, GitModeSubmodule:
		return true
	default:
		return false
	}
}

type CommitPathChange struct {
	oldPath string
	newPath string
}

func NewCommitPathChange(oldPath, newPath string) (CommitPathChange, error) {
	if oldPath != "" {
		normalized, err := normalizeCommitPath(oldPath)
		if err != nil {
			return CommitPathChange{}, fmt.Errorf("old path: %w", err)
		}
		oldPath = normalized
	}
	if newPath != "" {
		normalized, err := normalizeCommitPath(newPath)
		if err != nil {
			return CommitPathChange{}, fmt.Errorf("new path: %w", err)
		}
		newPath = normalized
	}
	change := CommitPathChange{oldPath: oldPath, newPath: newPath}
	if err := change.validate(); err != nil {
		return CommitPathChange{}, err
	}
	return change, nil
}

func (change CommitPathChange) OldPath() string { return change.oldPath }
func (change CommitPathChange) NewPath() string { return change.newPath }

func (change CommitPathChange) validate() error {
	if change.oldPath == "" && change.newPath == "" {
		return fmt.Errorf("commit path change requires an old or new path")
	}
	if change.oldPath != "" {
		if _, err := normalizeCommitPath(change.oldPath); err != nil {
			return fmt.Errorf("old path: %w", err)
		}
	}
	if change.newPath != "" {
		if _, err := normalizeCommitPath(change.newPath); err != nil {
			return fmt.Errorf("new path: %w", err)
		}
	}
	return nil
}

func (policy CommitPathPolicy) ValidateChange(change CommitPathChange) error {
	if err := change.validate(); err != nil {
		return err
	}
	if change.oldPath != "" {
		if err := policy.Validate(change.oldPath); err != nil {
			return fmt.Errorf("old path: %w", err)
		}
	}
	if change.newPath != "" {
		if err := policy.Validate(change.newPath); err != nil {
			return fmt.Errorf("new path: %w", err)
		}
	}
	return nil
}

type CommitDiff struct {
	changes []CommitPathChange
}

func NewCommitDiff(changes []CommitPathChange) (CommitDiff, error) {
	if len(changes) == 0 {
		return CommitDiff{}, fmt.Errorf("commit diff must contain at least one change")
	}
	copyChanges := append([]CommitPathChange(nil), changes...)
	for _, change := range copyChanges {
		if err := change.validate(); err != nil {
			return CommitDiff{}, err
		}
	}
	return CommitDiff{changes: copyChanges}, nil
}

func (diff CommitDiff) Changes() []CommitPathChange {
	return append([]CommitPathChange(nil), diff.changes...)
}

func cloneCommitDiff(diff CommitDiff) CommitDiff {
	diff.changes = append([]CommitPathChange(nil), diff.changes...)
	return diff
}
