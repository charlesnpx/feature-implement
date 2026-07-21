package workspace

import (
	"encoding/hex"
	"fmt"
)

type CommitProtocolPhase string

const (
	CommitProtocolReady          CommitProtocolPhase = "ready"
	CommitProtocolAwaitingCommit CommitProtocolPhase = "awaiting_commit"
	CommitProtocolAwaitingChecks CommitProtocolPhase = "awaiting_checks"
	CommitProtocolComplete       CommitProtocolPhase = "complete"
)

func (phase CommitProtocolPhase) valid() bool {
	switch phase {
	case CommitProtocolReady, CommitProtocolAwaitingCommit, CommitProtocolAwaitingChecks, CommitProtocolComplete:
		return true
	default:
		return false
	}
}

type CommitProtocolStepState struct {
	commit CommitObjectEvidence
	checks []CommitCheckEvidence
}

func (state CommitProtocolStepState) Commit() CommitObjectEvidence {
	return cloneCommitObjectEvidence(state.commit)
}
func (state CommitProtocolStepState) Checks() []CommitCheckEvidence {
	return cloneCommitCheckEvidence(state.checks)
}

type pendingCommitStep struct {
	inspection StagedCommitInspection
	body       string
}

type CommitProtocolState struct {
	generation   Digest
	base         GitObjectID
	protocol     CommitProtocol
	phase        CommitProtocolPhase
	steps        []CommitProtocolStepState
	pending      pendingCommitStep
	checkingStep int
	rebaseEpoch  uint64
}

func NewCommitProtocolState(generation Digest, base GitObjectID, protocol CommitProtocol) (CommitProtocolState, error) {
	if generation.IsZero() || base.IsZero() || protocol.digest.IsZero() || len(protocol.steps) == 0 {
		return CommitProtocolState{}, fmt.Errorf("commit protocol state requires generation, base, and protocol")
	}
	return CommitProtocolState{
		generation: generation, base: base, protocol: *cloneCommitProtocol(&protocol),
		phase: CommitProtocolReady, checkingStep: -1,
	}, nil
}

func (state CommitProtocolState) Generation() Digest { return state.generation }
func (state CommitProtocolState) Base() GitObjectID  { return state.base }
func (state CommitProtocolState) Protocol() CommitProtocol {
	return *cloneCommitProtocol(&state.protocol)
}
func (state CommitProtocolState) ProtocolDigest() Digest     { return state.protocol.digest }
func (state CommitProtocolState) Phase() CommitProtocolPhase { return state.phase }
func (state CommitProtocolState) RebaseEpoch() uint64        { return state.rebaseEpoch }
func (state CommitProtocolState) CompletedSteps() []CommitProtocolStepState {
	return cloneCommitStepStates(state.steps)
}
func (state CommitProtocolState) Head() GitObjectID {
	if len(state.steps) == 0 {
		return state.base
	}
	return state.steps[len(state.steps)-1].commit.commit
}

type CommitProtocolEvent interface {
	isCommitProtocolEvent()
}

type StageCommitStep struct {
	inspection StagedCommitInspection
	body       string
}

func NewStageCommitStep(inspection StagedCommitInspection, body string) (StageCommitStep, error) {
	if inspection.stateDigest.IsZero() {
		return StageCommitStep{}, fmt.Errorf("stage commit event requires an inspection")
	}
	if err := validateCommitBody(body); err != nil {
		return StageCommitStep{}, err
	}
	return StageCommitStep{inspection: cloneStagedCommitInspection(inspection), body: body}, nil
}

func (StageCommitStep) isCommitProtocolEvent() {}
func (event StageCommitStep) Inspection() StagedCommitInspection {
	return cloneStagedCommitInspection(event.inspection)
}
func (event StageCommitStep) Body() string { return event.body }

type RecordCommitStep struct {
	evidence CommitObjectEvidence
}

func NewRecordCommitStep(evidence CommitObjectEvidence) (RecordCommitStep, error) {
	if err := evidence.validate(); err != nil || evidence.evidence.IsZero() {
		if err == nil {
			err = fmt.Errorf("commit evidence digest is required")
		}
		return RecordCommitStep{}, err
	}
	return RecordCommitStep{evidence: cloneCommitObjectEvidence(evidence)}, nil
}

func (RecordCommitStep) isCommitProtocolEvent() {}
func (event RecordCommitStep) Evidence() CommitObjectEvidence {
	return cloneCommitObjectEvidence(event.evidence)
}

type RecordCommitCheck struct {
	evidence CommitCheckEvidence
}

func NewRecordCommitCheck(evidence CommitCheckEvidence) (RecordCommitCheck, error) {
	if evidence.evidence.IsZero() {
		return RecordCommitCheck{}, fmt.Errorf("check evidence digest is required")
	}
	return RecordCommitCheck{evidence: cloneOneCommitCheckEvidence(evidence)}, nil
}

func (RecordCommitCheck) isCommitProtocolEvent() {}
func (event RecordCommitCheck) Evidence() CommitCheckEvidence {
	return cloneOneCommitCheckEvidence(event.evidence)
}

type RemapRebasedCommits struct {
	base    GitObjectID
	commits []CommitObjectEvidence
}

func NewRemapRebasedCommits(base GitObjectID, commits []CommitObjectEvidence) (RemapRebasedCommits, error) {
	if base.IsZero() || len(commits) == 0 {
		return RemapRebasedCommits{}, fmt.Errorf("rebase remapping requires a new base and commits")
	}
	copyCommits := make([]CommitObjectEvidence, len(commits))
	for index, commit := range commits {
		if err := commit.validate(); err != nil || commit.evidence.IsZero() {
			if err == nil {
				err = fmt.Errorf("commit evidence digest is required")
			}
			return RemapRebasedCommits{}, fmt.Errorf("rebased commit %d: %w", index, err)
		}
		copyCommits[index] = cloneCommitObjectEvidence(commit)
	}
	return RemapRebasedCommits{base: base, commits: copyCommits}, nil
}

func (RemapRebasedCommits) isCommitProtocolEvent()  {}
func (event RemapRebasedCommits) Base() GitObjectID { return event.base }
func (event RemapRebasedCommits) Commits() []CommitObjectEvidence {
	result := make([]CommitObjectEvidence, len(event.commits))
	for index, commit := range event.commits {
		result[index] = cloneCommitObjectEvidence(commit)
	}
	return result
}

type CommitProtocolEffect interface {
	isCommitProtocolEffect()
}

type CreateConfiguredCommitEffect struct {
	generation     Digest
	protocol       Digest
	step           CommitStep
	ordinal        uint16
	parent         GitObjectID
	body           string
	inspection     StagedCommitInspection
	idempotencyKey Digest
}

func (CreateConfiguredCommitEffect) isCommitProtocolEffect()       {}
func (effect CreateConfiguredCommitEffect) Generation() Digest     { return effect.generation }
func (effect CreateConfiguredCommitEffect) ProtocolDigest() Digest { return effect.protocol }
func (effect CreateConfiguredCommitEffect) Step() CommitStep {
	return cloneCommitSteps([]CommitStep{effect.step})[0]
}
func (effect CreateConfiguredCommitEffect) Ordinal() uint16     { return effect.ordinal }
func (effect CreateConfiguredCommitEffect) Parent() GitObjectID { return effect.parent }
func (effect CreateConfiguredCommitEffect) Body() string        { return effect.body }
func (effect CreateConfiguredCommitEffect) Inspection() StagedCommitInspection {
	return cloneStagedCommitInspection(effect.inspection)
}
func (effect CreateConfiguredCommitEffect) IdempotencyKey() Digest { return effect.idempotencyKey }

type RunConfiguredCheckEffect struct {
	generation     Digest
	protocol       Digest
	step           CommitStep
	stepOrdinal    uint16
	check          CommitCheck
	checkOrdinal   uint16
	commit         CommitObjectEvidence
	idempotencyKey Digest
}

func (RunConfiguredCheckEffect) isCommitProtocolEffect()       {}
func (effect RunConfiguredCheckEffect) Generation() Digest     { return effect.generation }
func (effect RunConfiguredCheckEffect) ProtocolDigest() Digest { return effect.protocol }
func (effect RunConfiguredCheckEffect) Step() CommitStep {
	return cloneCommitSteps([]CommitStep{effect.step})[0]
}
func (effect RunConfiguredCheckEffect) StepOrdinal() uint16 { return effect.stepOrdinal }
func (effect RunConfiguredCheckEffect) Check() CommitCheck {
	return cloneCommitChecks([]CommitCheck{effect.check})[0]
}
func (effect RunConfiguredCheckEffect) CheckOrdinal() uint16 { return effect.checkOrdinal }
func (effect RunConfiguredCheckEffect) Commit() CommitObjectEvidence {
	return cloneCommitObjectEvidence(effect.commit)
}
func (effect RunConfiguredCheckEffect) IdempotencyKey() Digest { return effect.idempotencyKey }

type CommitProtocolCompletedEffect struct {
	generation Digest
	protocol   Digest
	head       GitObjectID
	evidence   Digest
}

func (CommitProtocolCompletedEffect) isCommitProtocolEffect()       {}
func (effect CommitProtocolCompletedEffect) Generation() Digest     { return effect.generation }
func (effect CommitProtocolCompletedEffect) ProtocolDigest() Digest { return effect.protocol }
func (effect CommitProtocolCompletedEffect) Head() GitObjectID      { return effect.head }
func (effect CommitProtocolCompletedEffect) EvidenceDigest() Digest { return effect.evidence }

type CommitProtocolReduction struct {
	state   CommitProtocolState
	effects []CommitProtocolEffect
}

func (reduction CommitProtocolReduction) State() CommitProtocolState {
	return cloneCommitProtocolState(reduction.state)
}
func (reduction CommitProtocolReduction) Effects() []CommitProtocolEffect {
	return append([]CommitProtocolEffect(nil), reduction.effects...)
}

// ReduceCommitProtocol is pure. It accepts only typed observations/evidence,
// owns exact ordering and budgets, and returns closed effects for the
// imperative shell. It never calls Git or a process runner.
func ReduceCommitProtocol(current CommitProtocolState, event CommitProtocolEvent) (CommitProtocolReduction, error) {
	if err := validateCommitProtocolState(current); err != nil {
		return CommitProtocolReduction{}, err
	}
	if event == nil {
		return CommitProtocolReduction{}, fmt.Errorf("commit protocol event is required")
	}
	next := cloneCommitProtocolState(current)
	var effects []CommitProtocolEffect
	switch value := event.(type) {
	case StageCommitStep:
		if current.phase != CommitProtocolReady || len(current.steps) >= len(current.protocol.steps) {
			return CommitProtocolReduction{}, fmt.Errorf("commit step can be staged only while the protocol is ready")
		}
		ordinal := uint16(len(current.steps) + 1)
		step := current.protocol.steps[ordinal-1]
		parent := current.Head()
		if err := value.inspection.Validate(step, parent); err != nil {
			return CommitProtocolReduction{}, err
		}
		body, err := step.message.ResolveBody(value.body)
		if err != nil {
			return CommitProtocolReduction{}, fmt.Errorf("commit step %s body: %w", step.id, err)
		}
		next.phase = CommitProtocolAwaitingCommit
		next.pending = pendingCommitStep{inspection: cloneStagedCommitInspection(value.inspection), body: body}
		effect, err := createCommitEffect(next, step, ordinal, parent)
		if err != nil {
			return CommitProtocolReduction{}, err
		}
		effects = []CommitProtocolEffect{effect}
	case RecordCommitStep:
		if current.phase != CommitProtocolAwaitingCommit || current.pending.inspection.stateDigest.IsZero() {
			return CommitProtocolReduction{}, fmt.Errorf("commit evidence requires a pending configured commit")
		}
		ordinal := uint16(len(current.steps) + 1)
		step := current.protocol.steps[ordinal-1]
		parent := current.Head()
		if err := value.evidence.ValidateStep(step, current.generation, ordinal, parent); err != nil {
			return CommitProtocolReduction{}, err
		}
		if value.evidence.tree != current.pending.inspection.indexTree ||
			value.evidence.diff.digest != current.pending.inspection.diff.digest ||
			value.evidence.body != current.pending.body {
			return CommitProtocolReduction{}, fmt.Errorf("created commit does not match the staged tree, diff, and resolved message")
		}
		next.steps = append(next.steps, CommitProtocolStepState{commit: cloneCommitObjectEvidence(value.evidence)})
		next.pending = pendingCommitStep{}
		next.checkingStep = -1
		effects = advanceAfterCommitOrCheck(&next, len(next.steps)-1)
	case RecordCommitCheck:
		if current.phase != CommitProtocolAwaitingChecks || current.checkingStep < 0 || current.checkingStep >= len(current.steps) {
			return CommitProtocolReduction{}, fmt.Errorf("check evidence requires an awaiting configured check")
		}
		stepIndex := current.checkingStep
		step := current.protocol.steps[stepIndex]
		stepState := current.steps[stepIndex]
		checkIndex := len(stepState.checks)
		if checkIndex >= len(step.checks) {
			return CommitProtocolReduction{}, fmt.Errorf("commit step %s has no pending check", step.id)
		}
		check := step.checks[checkIndex]
		if err := value.evidence.Validate(check, stepState.commit); err != nil {
			return CommitProtocolReduction{}, err
		}
		next.steps[stepIndex].checks = append(next.steps[stepIndex].checks, cloneOneCommitCheckEvidence(value.evidence))
		effects = advanceAfterCommitOrCheck(&next, stepIndex)
	case RemapRebasedCommits:
		if current.phase != CommitProtocolReady && current.phase != CommitProtocolComplete {
			return CommitProtocolReduction{}, fmt.Errorf("rebase remapping is blocked by an in-flight commit or check")
		}
		if len(value.commits) != len(current.steps) {
			return CommitProtocolReduction{}, fmt.Errorf("rebase remapping requires exactly %d completed commits", len(current.steps))
		}
		if value.base.Algorithm() != current.base.Algorithm() {
			return CommitProtocolReduction{}, fmt.Errorf("rebase changes the repository object format")
		}
		parent := value.base
		seen := make(map[string]struct{}, len(value.commits))
		changed := value.base != current.base
		for index, evidence := range value.commits {
			step := current.protocol.steps[index]
			if err := evidence.ValidateStep(step, current.generation, uint16(index+1), parent); err != nil {
				return CommitProtocolReduction{}, fmt.Errorf("rebased step %s: %w", step.id, err)
			}
			if evidence.diff.digest != current.steps[index].commit.diff.digest {
				return CommitProtocolReduction{}, fmt.Errorf("rebased step %s changed its canonical diff", step.id)
			}
			if evidence.subject != current.steps[index].commit.subject || evidence.body != current.steps[index].commit.body {
				return CommitProtocolReduction{}, fmt.Errorf("rebased step %s changed its commit message", step.id)
			}
			if _, exists := seen[evidence.commit.String()]; exists {
				return CommitProtocolReduction{}, fmt.Errorf("rebased sequence repeats commit %s", evidence.commit)
			}
			seen[evidence.commit.String()] = struct{}{}
			changed = changed || evidence.commit != current.steps[index].commit.commit || evidence.tree != current.steps[index].commit.tree
			parent = evidence.commit
		}
		if !changed {
			return CommitProtocolReduction{}, fmt.Errorf("rebase remapping did not change the base, commit, or tree sequence")
		}
		next.base = value.base
		next.rebaseEpoch++
		next.pending = pendingCommitStep{}
		next.checkingStep = -1
		for index, evidence := range value.commits {
			next.steps[index] = CommitProtocolStepState{commit: cloneCommitObjectEvidence(evidence)}
		}
		effects = scheduleFirstRebasedCheck(&next)
	default:
		return CommitProtocolReduction{}, fmt.Errorf("unsupported commit protocol event %T", event)
	}
	if err := validateCommitProtocolState(next); err != nil {
		return CommitProtocolReduction{}, fmt.Errorf("invalid commit protocol transition: %w", err)
	}
	return CommitProtocolReduction{state: cloneCommitProtocolState(next), effects: effects}, nil
}

func PendingCommitProtocolEffects(state CommitProtocolState) ([]CommitProtocolEffect, error) {
	if err := validateCommitProtocolState(state); err != nil {
		return nil, err
	}
	switch state.phase {
	case CommitProtocolAwaitingCommit:
		ordinal := uint16(len(state.steps) + 1)
		effect, err := createCommitEffect(state, state.protocol.steps[ordinal-1], ordinal, state.Head())
		if err != nil {
			return nil, err
		}
		return []CommitProtocolEffect{effect}, nil
	case CommitProtocolAwaitingChecks:
		stepIndex := state.checkingStep
		checkIndex := len(state.steps[stepIndex].checks)
		effect, err := createCheckEffect(state, stepIndex, checkIndex)
		if err != nil {
			return nil, err
		}
		return []CommitProtocolEffect{effect}, nil
	case CommitProtocolComplete:
		effect, err := completedProtocolEffect(state)
		if err != nil {
			return nil, err
		}
		return []CommitProtocolEffect{effect}, nil
	default:
		return nil, nil
	}
}

func advanceAfterCommitOrCheck(state *CommitProtocolState, stepIndex int) []CommitProtocolEffect {
	step := state.protocol.steps[stepIndex]
	if len(state.steps[stepIndex].checks) < len(step.checks) {
		state.phase = CommitProtocolAwaitingChecks
		state.checkingStep = stepIndex
		effect, _ := createCheckEffect(*state, stepIndex, len(state.steps[stepIndex].checks))
		return []CommitProtocolEffect{effect}
	}
	for index := stepIndex + 1; index < len(state.steps); index++ {
		if len(state.steps[index].checks) < len(state.protocol.steps[index].checks) {
			state.phase = CommitProtocolAwaitingChecks
			state.checkingStep = index
			effect, _ := createCheckEffect(*state, index, len(state.steps[index].checks))
			return []CommitProtocolEffect{effect}
		}
	}
	state.checkingStep = -1
	if len(state.steps) == len(state.protocol.steps) {
		state.phase = CommitProtocolComplete
		effect, _ := completedProtocolEffect(*state)
		return []CommitProtocolEffect{effect}
	}
	state.phase = CommitProtocolReady
	return nil
}

func scheduleFirstRebasedCheck(state *CommitProtocolState) []CommitProtocolEffect {
	for index, step := range state.protocol.steps[:len(state.steps)] {
		if len(step.checks) != 0 {
			state.phase = CommitProtocolAwaitingChecks
			state.checkingStep = index
			effect, _ := createCheckEffect(*state, index, 0)
			return []CommitProtocolEffect{effect}
		}
	}
	state.checkingStep = -1
	if len(state.steps) == len(state.protocol.steps) {
		state.phase = CommitProtocolComplete
		effect, _ := completedProtocolEffect(*state)
		return []CommitProtocolEffect{effect}
	}
	state.phase = CommitProtocolReady
	return nil
}

func createCommitEffect(state CommitProtocolState, step CommitStep, ordinal uint16, parent GitObjectID) (CreateConfiguredCommitEffect, error) {
	key, err := commitEffectIdempotencyKey(
		state.generation, state.protocol.digest, step.id, ordinal, parent,
		state.pending.inspection.stateDigest, state.pending.body, state.rebaseEpoch,
	)
	if err != nil {
		return CreateConfiguredCommitEffect{}, err
	}
	return CreateConfiguredCommitEffect{
		generation: state.generation, protocol: state.protocol.digest,
		step: cloneCommitSteps([]CommitStep{step})[0], ordinal: ordinal, parent: parent,
		body: state.pending.body, inspection: cloneStagedCommitInspection(state.pending.inspection),
		idempotencyKey: key,
	}, nil
}

func createCheckEffect(state CommitProtocolState, stepIndex, checkIndex int) (RunConfiguredCheckEffect, error) {
	if stepIndex < 0 || stepIndex >= len(state.steps) || checkIndex < 0 ||
		checkIndex >= len(state.protocol.steps[stepIndex].checks) {
		return RunConfiguredCheckEffect{}, fmt.Errorf("configured check effect index is invalid")
	}
	step := state.protocol.steps[stepIndex]
	check := step.checks[checkIndex]
	commit := state.steps[stepIndex].commit
	keyBytes := []byte(fmt.Sprintf(
		"commit_check_effect_v2\ngeneration=%s\nprotocol=%s\nstep=%s\nstep_ordinal=%d\ncheck=%s\ncheck_ordinal=%d\ncommit=%s\ntree=%s\ndiff=%s\nrunner=%s\nparser=%s\nrebase_epoch=%d\n",
		state.generation, state.protocol.digest, step.id, stepIndex+1, check.id, checkIndex+1,
		commit.commit, commit.tree, commit.diff.digest, check.runner, check.parser, state.rebaseEpoch,
	))
	return RunConfiguredCheckEffect{
		generation: state.generation, protocol: state.protocol.digest,
		step: cloneCommitSteps([]CommitStep{step})[0], stepOrdinal: uint16(stepIndex + 1),
		check: cloneCommitChecks([]CommitCheck{check})[0], checkOrdinal: uint16(checkIndex + 1),
		commit: cloneCommitObjectEvidence(commit), idempotencyKey: DigestBytes(keyBytes),
	}, nil
}

func completedProtocolEffect(state CommitProtocolState) (CommitProtocolCompletedEffect, error) {
	if len(state.steps) != len(state.protocol.steps) {
		return CommitProtocolCompletedEffect{}, fmt.Errorf("protocol completion requires every configured step")
	}
	bindings := []byte(fmt.Sprintf(
		"commit_protocol_complete_v2\ngeneration=%s\nprotocol=%s\nbase=%s\nhead=%s\nrebase_epoch=%d\n",
		state.generation, state.protocol.digest, state.base, state.Head(), state.rebaseEpoch,
	))
	for index, step := range state.steps {
		bindings = append(bindings, []byte(fmt.Sprintf("step_%d=%s\n", index+1, step.commit.evidence))...)
		for checkIndex, check := range step.checks {
			bindings = append(bindings, []byte(fmt.Sprintf("step_%d_check_%d=%s\n", index+1, checkIndex+1, check.evidence))...)
		}
	}
	return CommitProtocolCompletedEffect{
		generation: state.generation, protocol: state.protocol.digest,
		head: state.Head(), evidence: DigestBytes(bindings),
	}, nil
}

func commitEffectIdempotencyKey(
	generation, protocol Digest,
	step ID,
	ordinal uint16,
	parent GitObjectID,
	inspection Digest,
	body string,
	rebaseEpoch uint64,
) (Digest, error) {
	if generation.IsZero() || protocol.IsZero() || step.IsZero() || ordinal == 0 || parent.IsZero() || inspection.IsZero() {
		return Digest{}, fmt.Errorf("commit effect idempotency bindings are incomplete")
	}
	bodyDigest := DigestBytes([]byte(body))
	return DigestBytes([]byte(fmt.Sprintf(
		"commit_effect_v2\ngeneration=%s\nprotocol=%s\nstep=%s\nordinal=%d\nparent=%s\ninspection=%s\nbody=%s\nrebase_epoch=%d\n",
		generation, protocol, step, ordinal, parent, inspection, bodyDigest, rebaseEpoch,
	))), nil
}

func validateCommitProtocolState(state CommitProtocolState) error {
	if state.generation.IsZero() || state.base.IsZero() || state.protocol.digest.IsZero() ||
		!state.phase.valid() || len(state.protocol.steps) == 0 || len(state.steps) > len(state.protocol.steps) {
		return fmt.Errorf("commit protocol state is incomplete")
	}
	parent := state.base
	for index, stepState := range state.steps {
		step := state.protocol.steps[index]
		if err := stepState.commit.ValidateStep(step, state.generation, uint16(index+1), parent); err != nil {
			return fmt.Errorf("commit protocol step %d: %w", index+1, err)
		}
		if len(stepState.checks) > len(step.checks) {
			return fmt.Errorf("commit protocol step %s has too many check records", step.id)
		}
		for checkIndex, evidence := range stepState.checks {
			if err := evidence.Validate(step.checks[checkIndex], stepState.commit); err != nil {
				return fmt.Errorf("commit protocol step %s check %d: %w", step.id, checkIndex+1, err)
			}
		}
		parent = stepState.commit.commit
	}
	switch state.phase {
	case CommitProtocolReady:
		if len(state.steps) >= len(state.protocol.steps) || !state.pending.inspection.stateDigest.IsZero() || state.checkingStep != -1 {
			return fmt.Errorf("ready commit protocol has inconsistent pending state")
		}
		for index, step := range state.steps {
			if len(step.checks) != len(state.protocol.steps[index].checks) {
				return fmt.Errorf("ready commit protocol has incomplete checks")
			}
		}
	case CommitProtocolAwaitingCommit:
		if len(state.steps) >= len(state.protocol.steps) || state.pending.inspection.stateDigest.IsZero() || state.checkingStep != -1 {
			return fmt.Errorf("awaiting commit protocol has inconsistent pending state")
		}
	case CommitProtocolAwaitingChecks:
		if !state.pending.inspection.stateDigest.IsZero() || state.checkingStep < 0 || state.checkingStep >= len(state.steps) ||
			len(state.steps[state.checkingStep].checks) >= len(state.protocol.steps[state.checkingStep].checks) {
			return fmt.Errorf("awaiting checks protocol has inconsistent pending state")
		}
		for index := 0; index < state.checkingStep; index++ {
			if len(state.steps[index].checks) != len(state.protocol.steps[index].checks) {
				return fmt.Errorf("awaiting checks protocol skipped an earlier check")
			}
		}
	case CommitProtocolComplete:
		if len(state.steps) != len(state.protocol.steps) || !state.pending.inspection.stateDigest.IsZero() || state.checkingStep != -1 {
			return fmt.Errorf("completed commit protocol has inconsistent state")
		}
		for index, step := range state.steps {
			if len(step.checks) != len(state.protocol.steps[index].checks) {
				return fmt.Errorf("completed commit protocol has incomplete checks")
			}
		}
	}
	return nil
}

type ReviewFixBudget struct {
	protocol ReviewFixProtocol
	maximum  uint16
	used     uint16
}

func NewReviewFixBudget(protocol ReviewFixProtocol, maximum uint16) (ReviewFixBudget, error) {
	if protocol.digest.IsZero() || maximum == 0 {
		return ReviewFixBudget{}, fmt.Errorf("review-fix budget requires protocol and positive maximum")
	}
	return ReviewFixBudget{protocol: *cloneReviewFixProtocol(&protocol), maximum: maximum}, nil
}

func (budget ReviewFixBudget) Maximum() uint16   { return budget.maximum }
func (budget ReviewFixBudget) Used() uint16      { return budget.used }
func (budget ReviewFixBudget) Remaining() uint16 { return budget.maximum - budget.used }

func (budget ReviewFixBudget) ReserveNext() (ReviewFixBudget, CommitStep, error) {
	if budget.protocol.digest.IsZero() || budget.maximum == 0 || budget.used >= budget.maximum {
		return ReviewFixBudget{}, CommitStep{}, fmt.Errorf("review-fix budget is exhausted")
	}
	next := budget
	next.used++
	step, err := next.protocol.Step(next.used)
	if err != nil {
		return ReviewFixBudget{}, CommitStep{}, err
	}
	return next, step, nil
}

func cloneCommitProtocolState(state CommitProtocolState) CommitProtocolState {
	result := state
	result.protocol = *cloneCommitProtocol(&state.protocol)
	result.steps = cloneCommitStepStates(state.steps)
	result.pending.inspection = cloneStagedCommitInspection(state.pending.inspection)
	return result
}

func cloneCommitStepStates(states []CommitProtocolStepState) []CommitProtocolStepState {
	result := append([]CommitProtocolStepState(nil), states...)
	for index := range result {
		result[index].commit = cloneCommitObjectEvidence(result[index].commit)
		result[index].checks = cloneCommitCheckEvidence(result[index].checks)
	}
	return result
}

func cloneStagedCommitInspection(inspection StagedCommitInspection) StagedCommitInspection {
	inspection.diff = cloneCommitDiff(inspection.diff)
	inspection.unstaged = append([]string(nil), inspection.unstaged...)
	inspection.untracked = append([]string(nil), inspection.untracked...)
	inspection.conflicted = append([]string(nil), inspection.conflicted...)
	return inspection
}

func cloneCommitObjectEvidence(evidence CommitObjectEvidence) CommitObjectEvidence {
	evidence.diff = cloneCommitDiff(evidence.diff)
	return evidence
}

func cloneOneCommitCheckEvidence(evidence CommitCheckEvidence) CommitCheckEvidence {
	evidence.outcome.identities = evidence.outcome.Identities()
	return evidence
}

func cloneCommitCheckEvidence(evidence []CommitCheckEvidence) []CommitCheckEvidence {
	result := append([]CommitCheckEvidence(nil), evidence...)
	for index := range result {
		result[index] = cloneOneCommitCheckEvidence(result[index])
	}
	return result
}

func shortDigestID(prefix string, digest Digest) ID {
	if digest.IsZero() {
		return ID{}
	}
	id, _ := NewID(prefix + "-" + hex.EncodeToString(digest.Bytes()[:8]))
	return id
}
