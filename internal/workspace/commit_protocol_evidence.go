package workspace

import (
	"bytes"
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

// StagedCommitInspection is an immutable snapshot of the exact index tree and
// raw Git changes the shell is allowed to turn into a commit. Unstaged and
// untracked files make the transaction ineligible instead of being silently
// left behind.
type StagedCommitInspection struct {
	head        GitObjectID
	indexTree   GitObjectID
	diff        CommitDiff
	unstaged    []string
	untracked   []string
	conflicted  []string
	stateDigest Digest
}

func NewStagedCommitInspection(
	head, indexTree GitObjectID,
	diff CommitDiff,
	unstaged, untracked, conflicted []string,
) (StagedCommitInspection, error) {
	if head.IsZero() || indexTree.IsZero() || diff.digest.IsZero() || head.Algorithm() != indexTree.Algorithm() {
		return StagedCommitInspection{}, fmt.Errorf("staged inspection requires algorithm-compatible head, index tree, and diff")
	}
	if commitDiffObjectAlgorithm(diff) != head.Algorithm() {
		return StagedCommitInspection{}, fmt.Errorf("staged inspection diff object format differs from the repository")
	}
	normalizePaths := func(label string, values []string) ([]string, error) {
		result := append([]string(nil), values...)
		for index, value := range result {
			normalized, err := normalizeCommitPath(value)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", label, index, err)
			}
			result[index] = normalized
		}
		sort.Strings(result)
		for index := 1; index < len(result); index++ {
			if result[index] == result[index-1] {
				return nil, fmt.Errorf("duplicate %s path %q", label, result[index])
			}
		}
		return result, nil
	}
	var err error
	unstaged, err = normalizePaths("unstaged", unstaged)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	untracked, err = normalizePaths("untracked", untracked)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	conflicted, err = normalizePaths("conflicted", conflicted)
	if err != nil {
		return StagedCommitInspection{}, err
	}
	type canonical struct {
		Head       string   `json:"head"`
		IndexTree  string   `json:"index_tree"`
		Diff       string   `json:"diff"`
		Unstaged   []string `json:"unstaged"`
		Untracked  []string `json:"untracked"`
		Conflicted []string `json:"conflicted"`
	}
	content, _ := json.Marshal(canonical{
		Head: head.String(), IndexTree: indexTree.String(), Diff: diff.digest.String(),
		Unstaged: unstaged, Untracked: untracked, Conflicted: conflicted,
	})
	return StagedCommitInspection{
		head: head, indexTree: indexTree, diff: cloneCommitDiff(diff),
		unstaged: unstaged, untracked: untracked, conflicted: conflicted,
		stateDigest: DigestBytes(content),
	}, nil
}

func (inspection StagedCommitInspection) Head() GitObjectID      { return inspection.head }
func (inspection StagedCommitInspection) IndexTree() GitObjectID { return inspection.indexTree }
func (inspection StagedCommitInspection) Diff() CommitDiff       { return cloneCommitDiff(inspection.diff) }
func (inspection StagedCommitInspection) Unstaged() []string {
	return append([]string(nil), inspection.unstaged...)
}
func (inspection StagedCommitInspection) Untracked() []string {
	return append([]string(nil), inspection.untracked...)
}
func (inspection StagedCommitInspection) Conflicted() []string {
	return append([]string(nil), inspection.conflicted...)
}
func (inspection StagedCommitInspection) StateDigest() Digest { return inspection.stateDigest }
func (inspection StagedCommitInspection) Eligible() bool {
	return !inspection.head.IsZero() && !inspection.indexTree.IsZero() && !inspection.diff.digest.IsZero() &&
		len(inspection.unstaged) == 0 && len(inspection.untracked) == 0 && len(inspection.conflicted) == 0
}

func (inspection StagedCommitInspection) Validate(step CommitStep, expectedParent GitObjectID) error {
	if !inspection.Eligible() {
		return fmt.Errorf("commit transaction requires a non-empty staged diff and no unstaged, untracked, or conflicted paths")
	}
	if inspection.head != expectedParent {
		return fmt.Errorf("staged inspection head %s does not match expected parent %s", inspection.head, expectedParent)
	}
	if inspection.head.Algorithm() != inspection.indexTree.Algorithm() {
		return fmt.Errorf("staged inspection mixes Git object algorithms")
	}
	for _, change := range inspection.diff.changes {
		if err := step.paths.ValidateChange(change); err != nil {
			return fmt.Errorf("commit step %s path policy: %w", step.id, err)
		}
	}
	return nil
}

type CommitObjectEvidence struct {
	generation Digest
	stepID     ID
	ordinal    uint16
	commit     GitObjectID
	parent     GitObjectID
	tree       GitObjectID
	subject    string
	body       string
	diff       CommitDiff
	pathPolicy Digest
	evidence   Digest
}

func NewCommitObjectEvidence(
	generation Digest,
	stepID ID,
	ordinal uint16,
	commit, parent, tree GitObjectID,
	subject, body string,
	diff CommitDiff,
	pathPolicy Digest,
) (CommitObjectEvidence, error) {
	evidence := CommitObjectEvidence{
		generation: generation, stepID: stepID, ordinal: ordinal,
		commit: commit, parent: parent, tree: tree, subject: subject, body: body,
		diff: cloneCommitDiff(diff), pathPolicy: pathPolicy,
	}
	if err := evidence.validate(); err != nil {
		return CommitObjectEvidence{}, err
	}
	content, _ := canonicalCommitObjectEvidence(evidence)
	evidence.evidence = DigestBytes(content)
	return evidence, nil
}

func (evidence CommitObjectEvidence) Generation() Digest       { return evidence.generation }
func (evidence CommitObjectEvidence) StepID() ID               { return evidence.stepID }
func (evidence CommitObjectEvidence) Ordinal() uint16          { return evidence.ordinal }
func (evidence CommitObjectEvidence) Commit() GitObjectID      { return evidence.commit }
func (evidence CommitObjectEvidence) Parent() GitObjectID      { return evidence.parent }
func (evidence CommitObjectEvidence) Tree() GitObjectID        { return evidence.tree }
func (evidence CommitObjectEvidence) Subject() string          { return evidence.subject }
func (evidence CommitObjectEvidence) Body() string             { return evidence.body }
func (evidence CommitObjectEvidence) Diff() CommitDiff         { return cloneCommitDiff(evidence.diff) }
func (evidence CommitObjectEvidence) PathPolicyDigest() Digest { return evidence.pathPolicy }
func (evidence CommitObjectEvidence) EvidenceDigest() Digest   { return evidence.evidence }

func (evidence CommitObjectEvidence) validate() error {
	if evidence.generation.IsZero() || evidence.stepID.IsZero() || evidence.ordinal == 0 ||
		evidence.commit.IsZero() || evidence.parent.IsZero() || evidence.tree.IsZero() ||
		evidence.diff.digest.IsZero() || evidence.pathPolicy.IsZero() {
		return fmt.Errorf("commit evidence requires generation, step, ordinal, objects, diff, and path policy")
	}
	if evidence.commit.Algorithm() != evidence.parent.Algorithm() || evidence.commit.Algorithm() != evidence.tree.Algorithm() {
		return fmt.Errorf("commit evidence mixes Git object algorithms")
	}
	if commitDiffObjectAlgorithm(evidence.diff) != evidence.commit.Algorithm() {
		return fmt.Errorf("commit evidence diff object format differs")
	}
	if err := validateCommitSubject(evidence.subject); err != nil {
		return err
	}
	return validateCommitBody(evidence.body)
}

func (evidence CommitObjectEvidence) ValidateStep(step CommitStep, generation Digest, ordinal uint16, parent GitObjectID) error {
	if err := evidence.validate(); err != nil {
		return err
	}
	if evidence.generation != generation || evidence.stepID != step.id || evidence.ordinal != ordinal || evidence.parent != parent {
		return fmt.Errorf("commit evidence does not match generation, step, ordinal, and first parent")
	}
	if evidence.pathPolicy != step.paths.digest {
		return fmt.Errorf("commit evidence path policy digest does not match step %s", step.id)
	}
	if err := step.message.Validate(evidence.subject, evidence.body); err != nil {
		return err
	}
	for _, change := range evidence.diff.changes {
		if err := step.paths.ValidateChange(change); err != nil {
			return err
		}
	}
	return nil
}

func canonicalCommitObjectEvidence(evidence CommitObjectEvidence) ([]byte, error) {
	if err := evidence.validate(); err != nil {
		return nil, err
	}
	type canonical struct {
		Generation string `json:"generation"`
		StepID     string `json:"step_id"`
		Ordinal    uint16 `json:"ordinal"`
		Commit     string `json:"commit"`
		Parent     string `json:"parent"`
		Tree       string `json:"tree"`
		Subject    string `json:"subject"`
		Body       string `json:"body"`
		Diff       string `json:"diff"`
		PathPolicy string `json:"path_policy"`
	}
	return json.Marshal(canonical{
		Generation: evidence.generation.String(), StepID: evidence.stepID.String(), Ordinal: evidence.ordinal,
		Commit: evidence.commit.String(), Parent: evidence.parent.String(), Tree: evidence.tree.String(),
		Subject: evidence.subject, Body: evidence.body, Diff: evidence.diff.digest.String(),
		PathPolicy: evidence.pathPolicy.String(),
	})
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
	credentialsAvailable bool
	repositoryHooks      bool
	writeNetwork         bool
	providerBroker       bool
	digest               Digest
}

func NewCheckIsolationProof(credentialsAvailable, repositoryHooks, writeNetwork, providerBroker bool) CheckIsolationProof {
	type canonical struct {
		CredentialsAvailable bool `json:"credentials_available"`
		RepositoryHooks      bool `json:"repository_hooks"`
		WriteNetwork         bool `json:"write_network"`
		ProviderBroker       bool `json:"provider_broker"`
	}
	content, _ := json.Marshal(canonical{credentialsAvailable, repositoryHooks, writeNetwork, providerBroker})
	return CheckIsolationProof{
		credentialsAvailable: credentialsAvailable, repositoryHooks: repositoryHooks,
		writeNetwork: writeNetwork, providerBroker: providerBroker, digest: DigestBytes(content),
	}
}

func StrictCheckIsolationProof() CheckIsolationProof {
	return NewCheckIsolationProof(false, false, false, false)
}

func (proof CheckIsolationProof) CredentialsAvailable() bool { return proof.credentialsAvailable }
func (proof CheckIsolationProof) RepositoryHooks() bool      { return proof.repositoryHooks }
func (proof CheckIsolationProof) WriteNetwork() bool         { return proof.writeNetwork }
func (proof CheckIsolationProof) ProviderBroker() bool       { return proof.providerBroker }
func (proof CheckIsolationProof) Digest() Digest             { return proof.digest }
func (proof CheckIsolationProof) Strict() bool {
	return !proof.digest.IsZero() && !proof.credentialsAvailable && !proof.repositoryHooks &&
		!proof.writeNetwork && !proof.providerBroker
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

type CheckOutcomeKind string

const (
	CheckOutcomePassed               CheckOutcomeKind = "passed"
	CheckOutcomeAssertionFailed      CheckOutcomeKind = "assertion_failed"
	CheckOutcomeDiagnosticFailed     CheckOutcomeKind = "diagnostic_failed"
	CheckOutcomeCompilationFailed    CheckOutcomeKind = "compilation_failed"
	CheckOutcomeSetupFailed          CheckOutcomeKind = "setup_failed"
	CheckOutcomeTimedOut             CheckOutcomeKind = "timed_out"
	CheckOutcomeSignaled             CheckOutcomeKind = "signaled"
	CheckOutcomeCrashed              CheckOutcomeKind = "crashed"
	CheckOutcomeMissingExecutable    CheckOutcomeKind = "missing_executable"
	CheckOutcomeMalformedOutput      CheckOutcomeKind = "malformed_output"
	CheckOutcomeInfrastructureFailed CheckOutcomeKind = "infrastructure_failed"
)

func (kind CheckOutcomeKind) valid() bool {
	switch kind {
	case CheckOutcomePassed, CheckOutcomeAssertionFailed, CheckOutcomeDiagnosticFailed,
		CheckOutcomeCompilationFailed, CheckOutcomeSetupFailed, CheckOutcomeTimedOut,
		CheckOutcomeSignaled, CheckOutcomeCrashed, CheckOutcomeMissingExecutable,
		CheckOutcomeMalformedOutput, CheckOutcomeInfrastructureFailed:
		return true
	default:
		return false
	}
}

type ParsedCheckOutcome struct {
	kind       CheckOutcomeKind
	identities []string
	digest     Digest
}

func NewParsedCheckOutcome(kind CheckOutcomeKind, identities []string) (ParsedCheckOutcome, error) {
	if !kind.valid() {
		return ParsedCheckOutcome{}, fmt.Errorf("unsupported check outcome %q", kind)
	}
	values := append([]string(nil), identities...)
	for index, value := range values {
		if value == "" || strings.TrimSpace(value) != value || len(value) > maxCheckFailureIDBytes ||
			strings.ContainsAny(value, "\x00\r\n") || !utf8.ValidString(value) {
			return ParsedCheckOutcome{}, fmt.Errorf("check outcome identity %d is invalid", index)
		}
	}
	sort.Strings(values)
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return ParsedCheckOutcome{}, fmt.Errorf("duplicate check outcome identity %q", values[index])
		}
	}
	if (kind == CheckOutcomeAssertionFailed || kind == CheckOutcomeDiagnosticFailed) != (len(values) > 0) {
		return ParsedCheckOutcome{}, fmt.Errorf("structured failure outcomes require identities and other outcomes forbid them")
	}
	content, _ := json.Marshal(struct {
		Kind       CheckOutcomeKind `json:"kind"`
		Identities []string         `json:"identities"`
	}{kind, values})
	return ParsedCheckOutcome{kind: kind, identities: values, digest: DigestBytes(content)}, nil
}

func (outcome ParsedCheckOutcome) Kind() CheckOutcomeKind { return outcome.kind }
func (outcome ParsedCheckOutcome) Identities() []string {
	return append([]string(nil), outcome.identities...)
}
func (outcome ParsedCheckOutcome) Digest() Digest { return outcome.digest }

func (expectation CheckExpectation) SatisfiedBy(outcome ParsedCheckOutcome) bool {
	if outcome.digest.IsZero() {
		return false
	}
	switch expectation.kind {
	case CheckExpectationPass:
		return outcome.kind == CheckOutcomePassed
	case CheckExpectationExpectedTestFailure:
		if outcome.kind != CheckOutcomeAssertionFailed && outcome.kind != CheckOutcomeDiagnosticFailed {
			return false
		}
		return equalStrings(expectation.failureIDs, outcome.identities)
	default:
		return false
	}
}

type CommitCheckEvidence struct {
	generation Digest
	stepID     ID
	checkID    ID
	commit     GitObjectID
	tree       GitObjectID
	diff       Digest
	runner     ID
	parser     CheckParserKind
	command    Digest
	output     Digest
	isolation  Digest
	outcome    ParsedCheckOutcome
	evidence   Digest
}

func NewCommitCheckEvidence(
	generation Digest,
	step CommitStep,
	check CommitCheck,
	commit CommitObjectEvidence,
	result CheckProcessResult,
	outcome ParsedCheckOutcome,
) (CommitCheckEvidence, error) {
	if generation.IsZero() || step.id.IsZero() || check.id.IsZero() || commit.commit.IsZero() ||
		result.output.IsZero() || outcome.digest.IsZero() {
		return CommitCheckEvidence{}, fmt.Errorf("check evidence requires complete immutable bindings")
	}
	if generation != commit.generation || step.id != commit.stepID || !result.isolation.Strict() {
		return CommitCheckEvidence{}, fmt.Errorf("check evidence generation, step, or isolation does not match")
	}
	if !check.expectation.SatisfiedBy(outcome) {
		return CommitCheckEvidence{}, fmt.Errorf("check %s outcome %s does not satisfy %s", check.id, outcome.kind, check.expectation.kind)
	}
	commandBytes, _ := json.Marshal(check.command.Values())
	evidence := CommitCheckEvidence{
		generation: generation, stepID: step.id, checkID: check.id,
		commit: commit.commit, tree: commit.tree, diff: commit.diff.digest,
		runner: check.runner, parser: check.parser, command: DigestBytes(commandBytes),
		output: result.output, isolation: result.isolation.digest,
		outcome: ParsedCheckOutcome{kind: outcome.kind, identities: outcome.Identities(), digest: outcome.digest},
	}
	content, _ := canonicalCommitCheckEvidence(evidence)
	evidence.evidence = DigestBytes(content)
	return evidence, nil
}

func (evidence CommitCheckEvidence) Generation() Digest      { return evidence.generation }
func (evidence CommitCheckEvidence) StepID() ID              { return evidence.stepID }
func (evidence CommitCheckEvidence) CheckID() ID             { return evidence.checkID }
func (evidence CommitCheckEvidence) Commit() GitObjectID     { return evidence.commit }
func (evidence CommitCheckEvidence) Tree() GitObjectID       { return evidence.tree }
func (evidence CommitCheckEvidence) DiffDigest() Digest      { return evidence.diff }
func (evidence CommitCheckEvidence) Runner() ID              { return evidence.runner }
func (evidence CommitCheckEvidence) Parser() CheckParserKind { return evidence.parser }
func (evidence CommitCheckEvidence) CommandDigest() Digest   { return evidence.command }
func (evidence CommitCheckEvidence) OutputDigest() Digest    { return evidence.output }
func (evidence CommitCheckEvidence) IsolationDigest() Digest { return evidence.isolation }
func (evidence CommitCheckEvidence) Outcome() ParsedCheckOutcome {
	return ParsedCheckOutcome{kind: evidence.outcome.kind, identities: evidence.outcome.Identities(), digest: evidence.outcome.digest}
}
func (evidence CommitCheckEvidence) EvidenceDigest() Digest { return evidence.evidence }

func (evidence CommitCheckEvidence) Validate(check CommitCheck, commit CommitObjectEvidence) error {
	if evidence.generation != commit.generation || evidence.stepID != commit.stepID ||
		evidence.checkID != check.id || evidence.commit != commit.commit || evidence.tree != commit.tree ||
		evidence.diff != commit.diff.digest || evidence.runner != check.runner || evidence.parser != check.parser {
		return fmt.Errorf("check evidence does not match check and commit bindings")
	}
	commandBytes, _ := json.Marshal(check.command.Values())
	if evidence.command != DigestBytes(commandBytes) || !check.expectation.SatisfiedBy(evidence.outcome) ||
		evidence.output.IsZero() || evidence.isolation != StrictCheckIsolationProof().digest || evidence.evidence.IsZero() {
		return fmt.Errorf("check evidence command, outcome, output, or isolation is invalid")
	}
	content, err := canonicalCommitCheckEvidence(evidence)
	if err != nil || DigestBytes(content) != evidence.evidence {
		return fmt.Errorf("check evidence digest does not match canonical bindings")
	}
	return nil
}

func canonicalCommitCheckEvidence(evidence CommitCheckEvidence) ([]byte, error) {
	if evidence.generation.IsZero() || evidence.stepID.IsZero() || evidence.checkID.IsZero() ||
		evidence.commit.IsZero() || evidence.tree.IsZero() || evidence.diff.IsZero() ||
		evidence.runner.IsZero() || !evidence.parser.valid() || evidence.command.IsZero() ||
		evidence.output.IsZero() || evidence.isolation.IsZero() || evidence.outcome.digest.IsZero() {
		return nil, fmt.Errorf("check evidence bindings are incomplete")
	}
	if evidence.isolation != StrictCheckIsolationProof().digest {
		return nil, fmt.Errorf("check evidence does not carry the strict isolation proof")
	}
	type canonical struct {
		Generation string          `json:"generation"`
		StepID     string          `json:"step_id"`
		CheckID    string          `json:"check_id"`
		Commit     string          `json:"commit"`
		Tree       string          `json:"tree"`
		Diff       string          `json:"diff"`
		Runner     string          `json:"runner"`
		Parser     CheckParserKind `json:"parser"`
		Command    string          `json:"command"`
		Output     string          `json:"output"`
		Isolation  string          `json:"isolation"`
		Outcome    string          `json:"outcome"`
	}
	return json.Marshal(canonical{
		Generation: evidence.generation.String(), StepID: evidence.stepID.String(), CheckID: evidence.checkID.String(),
		Commit: evidence.commit.String(), Tree: evidence.tree.String(), Diff: evidence.diff.String(),
		Runner: evidence.runner.String(), Parser: evidence.parser, Command: evidence.command.String(),
		Output: evidence.output.String(), Isolation: evidence.isolation.String(), Outcome: evidence.outcome.digest.String(),
	})
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

func equalStrings(left, right []string) bool {
	return len(left) == len(right) && bytes.Equal([]byte(strings.Join(left, "\x00")), []byte(strings.Join(right, "\x00")))
}
