package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
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

type CommitChangeKind string

const (
	CommitChangeAdded       CommitChangeKind = "added"
	CommitChangeModified    CommitChangeKind = "modified"
	CommitChangeDeleted     CommitChangeKind = "deleted"
	CommitChangeRenamed     CommitChangeKind = "renamed"
	CommitChangeCopied      CommitChangeKind = "copied"
	CommitChangeTypeChanged CommitChangeKind = "type_changed"
)

func (kind CommitChangeKind) valid() bool {
	switch kind {
	case CommitChangeAdded, CommitChangeModified, CommitChangeDeleted, CommitChangeRenamed,
		CommitChangeCopied, CommitChangeTypeChanged:
		return true
	default:
		return false
	}
}

type CommitPathChange struct {
	kind      CommitChangeKind
	oldPath   string
	newPath   string
	oldMode   GitFileMode
	newMode   GitFileMode
	oldObject GitObjectID
	newObject GitObjectID
}

func NewCommitPathChange(
	kind CommitChangeKind,
	oldPath, newPath string,
	oldMode, newMode GitFileMode,
	oldObject, newObject GitObjectID,
) (CommitPathChange, error) {
	change := CommitPathChange{
		kind: kind, oldPath: oldPath, newPath: newPath, oldMode: oldMode, newMode: newMode,
		oldObject: oldObject, newObject: newObject,
	}
	if err := change.validate(); err != nil {
		return CommitPathChange{}, err
	}
	return change, nil
}

func (change CommitPathChange) Kind() CommitChangeKind { return change.kind }
func (change CommitPathChange) OldPath() string        { return change.oldPath }
func (change CommitPathChange) NewPath() string        { return change.newPath }
func (change CommitPathChange) OldMode() GitFileMode   { return change.oldMode }
func (change CommitPathChange) NewMode() GitFileMode   { return change.newMode }
func (change CommitPathChange) OldObject() GitObjectID { return change.oldObject }
func (change CommitPathChange) NewObject() GitObjectID { return change.newObject }

func (change CommitPathChange) validate() error {
	if !change.kind.valid() || !change.oldMode.valid() || !change.newMode.valid() {
		return fmt.Errorf("commit path change has unsupported kind or mode")
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
	if !change.oldObject.IsZero() && !change.newObject.IsZero() && change.oldObject.Algorithm() != change.newObject.Algorithm() {
		return fmt.Errorf("commit path change object algorithms differ")
	}
	switch change.kind {
	case CommitChangeAdded:
		if change.oldPath != "" || change.oldMode != GitModeAbsent || !change.oldObject.IsZero() ||
			change.newPath == "" || change.newMode == GitModeAbsent || change.newObject.IsZero() {
			return fmt.Errorf("added path requires only a new object")
		}
	case CommitChangeDeleted:
		if change.oldPath == "" || change.oldMode == GitModeAbsent || change.oldObject.IsZero() ||
			change.newPath != "" || change.newMode != GitModeAbsent || !change.newObject.IsZero() {
			return fmt.Errorf("deleted path requires only an old object")
		}
	case CommitChangeRenamed, CommitChangeCopied:
		if change.oldPath == "" || change.newPath == "" || change.oldPath == change.newPath ||
			change.oldMode == GitModeAbsent || change.newMode == GitModeAbsent ||
			change.oldObject.IsZero() || change.newObject.IsZero() {
			return fmt.Errorf("rename or copy requires distinct old and new objects and paths")
		}
	case CommitChangeModified, CommitChangeTypeChanged:
		if change.oldPath == "" || change.newPath != change.oldPath || change.oldMode == GitModeAbsent ||
			change.newMode == GitModeAbsent || change.oldObject.IsZero() || change.newObject.IsZero() {
			return fmt.Errorf("modified path requires old and new objects at one path")
		}
		if change.kind == CommitChangeTypeChanged && change.oldMode == change.newMode {
			return fmt.Errorf("type-changed path requires a mode change")
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
	if change.newPath != "" && change.newPath != change.oldPath {
		if err := policy.Validate(change.newPath); err != nil {
			return fmt.Errorf("new path: %w", err)
		}
	}
	return nil
}

type CommitDiff struct {
	changes []CommitPathChange
	digest  Digest
}

func NewCommitDiff(changes []CommitPathChange) (CommitDiff, error) {
	if len(changes) == 0 {
		return CommitDiff{}, fmt.Errorf("commit diff must contain at least one change")
	}
	copyChanges := append([]CommitPathChange(nil), changes...)
	var objectAlgorithm GitHashAlgorithm
	for _, change := range copyChanges {
		if err := change.validate(); err != nil {
			return CommitDiff{}, err
		}
		for _, object := range []GitObjectID{change.oldObject, change.newObject} {
			if object.IsZero() {
				continue
			}
			if objectAlgorithm == "" {
				objectAlgorithm = object.Algorithm()
			} else if object.Algorithm() != objectAlgorithm {
				return CommitDiff{}, fmt.Errorf("commit diff mixes Git object algorithms")
			}
		}
	}
	if objectAlgorithm == "" {
		return CommitDiff{}, fmt.Errorf("commit diff has no Git objects")
	}
	sort.Slice(copyChanges, func(i, j int) bool {
		left := string(copyChanges[i].kind) + "\x00" + copyChanges[i].oldPath + "\x00" + copyChanges[i].newPath
		right := string(copyChanges[j].kind) + "\x00" + copyChanges[j].oldPath + "\x00" + copyChanges[j].newPath
		return left < right
	})
	for index := 1; index < len(copyChanges); index++ {
		prior, current := copyChanges[index-1], copyChanges[index]
		if prior.kind == current.kind && prior.oldPath == current.oldPath && prior.newPath == current.newPath {
			return CommitDiff{}, fmt.Errorf("duplicate commit path change for %q and %q", current.oldPath, current.newPath)
		}
	}
	content, err := canonicalCommitChanges(copyChanges)
	if err != nil {
		return CommitDiff{}, err
	}
	if len(content) > MaxJournalRecordBytes/2 {
		return CommitDiff{}, fmt.Errorf("commit diff exceeds its durable journal footprint")
	}
	return CommitDiff{changes: copyChanges, digest: DigestBytes(content)}, nil
}

func (diff CommitDiff) Changes() []CommitPathChange {
	return append([]CommitPathChange(nil), diff.changes...)
}
func (diff CommitDiff) Digest() Digest { return diff.digest }

func canonicalCommitChanges(changes []CommitPathChange) ([]byte, error) {
	type changeJSON struct {
		Kind      CommitChangeKind `json:"kind"`
		OldPath   string           `json:"old_path,omitempty"`
		NewPath   string           `json:"new_path,omitempty"`
		OldMode   GitFileMode      `json:"old_mode"`
		NewMode   GitFileMode      `json:"new_mode"`
		OldObject string           `json:"old_object,omitempty"`
		NewObject string           `json:"new_object,omitempty"`
	}
	values := make([]changeJSON, 0, len(changes))
	for _, change := range changes {
		if err := change.validate(); err != nil {
			return nil, err
		}
		values = append(values, changeJSON{
			Kind: change.kind, OldPath: change.oldPath, NewPath: change.newPath,
			OldMode: change.oldMode, NewMode: change.newMode,
			OldObject: change.oldObject.String(), NewObject: change.newObject.String(),
		})
	}
	return json.Marshal(values)
}

type CheckTerminationKind string

const (
	CheckExited            CheckTerminationKind = "exited"
	CheckTimedOut          CheckTerminationKind = "timed_out"
	CheckSignaled          CheckTerminationKind = "signaled"
	CheckCrashed           CheckTerminationKind = "crashed"
	CheckMissingExecutable CheckTerminationKind = "missing_executable"
	CheckInfrastructure    CheckTerminationKind = "infrastructure_failure"
)

func (kind CheckTerminationKind) valid() bool {
	switch kind {
	case CheckExited, CheckTimedOut, CheckSignaled, CheckCrashed, CheckMissingExecutable, CheckInfrastructure:
		return true
	default:
		return false
	}
}

type CheckIsolationProof struct {
	repositoryHooks bool
	writeNetwork    bool
	digest          Digest
}

func NewCheckIsolationProof(repositoryHooks, writeNetwork bool) CheckIsolationProof {
	type canonical struct {
		RepositoryHooks bool `json:"repository_hooks"`
		WriteNetwork    bool `json:"write_network"`
	}
	content, _ := json.Marshal(canonical{repositoryHooks, writeNetwork})
	return CheckIsolationProof{
		repositoryHooks: repositoryHooks,
		writeNetwork:    writeNetwork, digest: DigestBytes(content),
	}
}

func StrictCheckIsolationProof() CheckIsolationProof {
	return NewCheckIsolationProof(false, false)
}

func (proof CheckIsolationProof) RepositoryHooks() bool { return proof.repositoryHooks }
func (proof CheckIsolationProof) WriteNetwork() bool    { return proof.writeNetwork }
func (proof CheckIsolationProof) Digest() Digest        { return proof.digest }
func (proof CheckIsolationProof) Strict() bool {
	return !proof.digest.IsZero() && !proof.repositoryHooks && !proof.writeNetwork
}

type CheckProcessResult struct {
	termination CheckTerminationKind
	exitCode    int
	signal      string
	stdout      []byte
	stderr      []byte
	isolation   CheckIsolationProof
	output      Digest
}

func NewCheckProcessResult(
	termination CheckTerminationKind,
	exitCode int,
	signal string,
	stdout, stderr []byte,
	isolation CheckIsolationProof,
) (CheckProcessResult, error) {
	if !termination.valid() || len(stdout) > maxAttemptGitOutputBytes || len(stderr) > maxAttemptGitOutputBytes ||
		!utf8.Valid(stdout) || !utf8.Valid(stderr) {
		return CheckProcessResult{}, fmt.Errorf("check process result is invalid or exceeds output bounds")
	}
	if termination == CheckExited {
		if exitCode < 0 || signal != "" {
			return CheckProcessResult{}, fmt.Errorf("exited check requires non-negative code and no signal")
		}
	} else if exitCode != -1 {
		return CheckProcessResult{}, fmt.Errorf("non-exited check requires exit code -1")
	}
	if termination == CheckSignaled && (signal == "" || strings.ContainsAny(signal, "\x00\r\n")) {
		return CheckProcessResult{}, fmt.Errorf("signaled check requires a canonical signal")
	}
	if termination != CheckSignaled && signal != "" {
		return CheckProcessResult{}, fmt.Errorf("only signaled checks can carry a signal")
	}
	if proof := isolation; proof.digest.IsZero() {
		return CheckProcessResult{}, fmt.Errorf("check process result requires an isolation proof")
	}
	type outputEnvelope struct {
		Termination CheckTerminationKind `json:"termination"`
		ExitCode    int                  `json:"exit_code"`
		Signal      string               `json:"signal,omitempty"`
		Stdout      []byte               `json:"stdout"`
		Stderr      []byte               `json:"stderr"`
	}
	content, _ := json.Marshal(outputEnvelope{termination, exitCode, signal, stdout, stderr})
	return CheckProcessResult{
		termination: termination, exitCode: exitCode, signal: signal,
		stdout: append([]byte(nil), stdout...), stderr: append([]byte(nil), stderr...),
		isolation: isolation, output: DigestBytes(content),
	}, nil
}

func (result CheckProcessResult) Termination() CheckTerminationKind { return result.termination }
func (result CheckProcessResult) ExitCode() int                     { return result.exitCode }
func (result CheckProcessResult) Signal() string                    { return result.signal }
func (result CheckProcessResult) Stdout() []byte                    { return append([]byte(nil), result.stdout...) }
func (result CheckProcessResult) Stderr() []byte                    { return append([]byte(nil), result.stderr...) }
func (result CheckProcessResult) Isolation() CheckIsolationProof    { return result.isolation }
func (result CheckProcessResult) OutputDigest() Digest              { return result.output }
func (result CheckProcessResult) Succeeded() bool {
	return result.termination == CheckExited && result.exitCode == 0
}

func cloneCommitDiff(diff CommitDiff) CommitDiff {
	diff.changes = append([]CommitPathChange(nil), diff.changes...)
	return diff
}

func commitDiffObjectAlgorithm(diff CommitDiff) GitHashAlgorithm {
	for _, change := range diff.changes {
		if !change.oldObject.IsZero() {
			return change.oldObject.Algorithm()
		}
		if !change.newObject.IsZero() {
			return change.newObject.Algorithm()
		}
	}
	return ""
}
