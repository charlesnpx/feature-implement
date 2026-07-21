package workspace

import (
	"encoding/json"
	"fmt"
)

func reduceCommitRuntime(
	current WorkspaceRuntimeProjection,
	next *WorkspaceRuntimeProjection,
	record JournalRecord,
) error {
	if next == nil || current.workspaceID.IsZero() || current.activeGeneration.IsZero() {
		return fmt.Errorf("commit protocol events require an initialized workspace runtime")
	}
	if record.generation != current.activeGeneration {
		return fmt.Errorf("commit protocol event generation is not active")
	}
	var workspaceID, attemptID ID
	var generation Digest
	switch event := record.event.(type) {
	case CommitProtocolStartedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	case CommitStepIntendedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	case CommitStepRecordedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	case CommitCheckRecordedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	case CommitProtocolRebasedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	default:
		return fmt.Errorf("unsupported commit runtime event %T", record.event)
	}
	index, attempt, err := requireRuntimeAttempt(current, attemptID, workspaceID, generation)
	if err != nil {
		return err
	}
	if attempt.phase != AttemptActive {
		return fmt.Errorf("attempt %s must be active for commit protocol transitions", attemptID)
	}
	updated := &next.attempts[index]
	switch event := record.event.(type) {
	case CommitProtocolStartedJournalEvent:
		if attempt.commitProtocol != nil {
			return fmt.Errorf("attempt %s already has a commit protocol runtime", attemptID)
		}
		if event.base != attempt.verifiedHead {
			return fmt.Errorf("commit protocol base %s does not match attempt head %s", event.base, attempt.verifiedHead)
		}
		state, err := NewCommitProtocolState(event.generation, event.base, event.protocol)
		if err != nil {
			return err
		}
		updated.commitProtocol = &state
		return nil
	case CommitStepIntendedJournalEvent:
		state, err := requireAttemptCommitProtocol(attempt, event.protocolDigest)
		if err != nil {
			return err
		}
		if state.phase != CommitProtocolReady || event.ordinal != uint16(len(state.steps)+1) ||
			event.stepID != state.protocol.steps[event.ordinal-1].id || event.parent != state.Head() ||
			event.rebaseEpoch != state.rebaseEpoch {
			return fmt.Errorf("commit step intent does not match the next configured step")
		}
		stage, err := NewStageCommitStep(event.inspection, event.body)
		if err != nil {
			return err
		}
		reduction, err := ReduceCommitProtocol(state, stage)
		if err != nil {
			return err
		}
		effects := reduction.Effects()
		if len(effects) != 1 {
			return fmt.Errorf("commit step intent did not produce one closed effect")
		}
		effect, ok := effects[0].(CreateConfiguredCommitEffect)
		if !ok || effect.idempotencyKey != event.idempotencyKey {
			return fmt.Errorf("commit step intent effect does not match journal idempotency key")
		}
		state = reduction.State()
		updated.commitProtocol = &state
		return nil
	case CommitStepRecordedJournalEvent:
		state, err := requireAttemptCommitProtocol(attempt, event.protocolDigest)
		if err != nil {
			return err
		}
		if state.phase != CommitProtocolAwaitingCommit {
			return fmt.Errorf("commit step record has no pending intent")
		}
		effects, err := PendingCommitProtocolEffects(state)
		if err != nil || len(effects) != 1 {
			return fmt.Errorf("load pending commit effect: %w", err)
		}
		effect, ok := effects[0].(CreateConfiguredCommitEffect)
		if !ok || effect.idempotencyKey != event.intentKey {
			return fmt.Errorf("commit step record does not match durable intent")
		}
		transition, err := NewRecordCommitStep(event.evidence)
		if err != nil {
			return err
		}
		reduction, err := ReduceCommitProtocol(state, transition)
		if err != nil {
			return err
		}
		state = reduction.State()
		updated.commitProtocol = &state
		updated.verifiedHead = event.evidence.commit
		return nil
	case CommitCheckRecordedJournalEvent:
		state, err := requireAttemptCommitProtocol(attempt, event.protocolDigest)
		if err != nil {
			return err
		}
		if state.phase != CommitProtocolAwaitingChecks {
			return fmt.Errorf("commit check record has no pending check")
		}
		effects, err := PendingCommitProtocolEffects(state)
		if err != nil || len(effects) != 1 {
			return fmt.Errorf("load pending check effect: %w", err)
		}
		effect, ok := effects[0].(RunConfiguredCheckEffect)
		if !ok || effect.stepOrdinal != event.stepOrdinal || effect.checkOrdinal != event.checkOrdinal ||
			effect.idempotencyKey != event.idempotencyKey {
			return fmt.Errorf("commit check record does not match the next configured check")
		}
		transition, err := NewRecordCommitCheck(event.evidence)
		if err != nil {
			return err
		}
		reduction, err := ReduceCommitProtocol(state, transition)
		if err != nil {
			return err
		}
		state = reduction.State()
		updated.commitProtocol = &state
		return nil
	case CommitProtocolRebasedJournalEvent:
		state, err := requireAttemptCommitProtocol(attempt, event.protocolDigest)
		if err != nil {
			return err
		}
		transition, err := NewRemapRebasedCommits(event.base, event.commits)
		if err != nil {
			return err
		}
		reduction, err := ReduceCommitProtocol(state, transition)
		if err != nil {
			return err
		}
		state = reduction.State()
		updated.commitProtocol = &state
		updated.verifiedHead = event.commits[len(event.commits)-1].commit
		return nil
	default:
		return fmt.Errorf("unsupported commit runtime event %T", record.event)
	}
}

func requireAttemptCommitProtocol(
	attempt RuntimeAttemptProjection,
	protocolDigest Digest,
) (CommitProtocolState, error) {
	if attempt.commitProtocol == nil {
		return CommitProtocolState{}, fmt.Errorf("attempt %s has no commit protocol runtime", attempt.attemptID)
	}
	state := cloneCommitProtocolState(*attempt.commitProtocol)
	if state.protocol.digest != protocolDigest {
		return CommitProtocolState{}, fmt.Errorf("attempt commit protocol digest does not match event")
	}
	return state, nil
}

func canonicalCommitProtocolRuntime(state CommitProtocolState) (json.RawMessage, error) {
	if err := validateCommitProtocolState(state); err != nil {
		return nil, err
	}
	type checkJSON struct {
		CheckID  string `json:"check_id"`
		Evidence string `json:"evidence"`
		Commit   string `json:"commit"`
		Outcome  string `json:"outcome"`
	}
	type stepJSON struct {
		StepID   string      `json:"step_id"`
		Commit   string      `json:"commit"`
		Tree     string      `json:"tree"`
		Diff     string      `json:"diff"`
		Evidence string      `json:"evidence"`
		Checks   []checkJSON `json:"checks"`
	}
	type pendingJSON struct {
		Head        string `json:"head"`
		IndexTree   string `json:"index_tree"`
		Diff        string `json:"diff"`
		StateDigest string `json:"state_digest"`
		BodyDigest  string `json:"body_digest"`
	}
	type runtimeJSON struct {
		Generation   string              `json:"generation"`
		Base         string              `json:"base"`
		Protocol     string              `json:"protocol"`
		Phase        CommitProtocolPhase `json:"phase"`
		RebaseEpoch  uint64              `json:"rebase_epoch"`
		CheckingStep int                 `json:"checking_step"`
		Steps        []stepJSON          `json:"steps"`
		Pending      *pendingJSON        `json:"pending,omitempty"`
	}
	value := runtimeJSON{
		Generation: state.generation.String(), Base: state.base.String(), Protocol: state.protocol.digest.String(),
		Phase: state.phase, RebaseEpoch: state.rebaseEpoch, CheckingStep: state.checkingStep,
		Steps: make([]stepJSON, 0, len(state.steps)),
	}
	if !state.pending.inspection.stateDigest.IsZero() {
		value.Pending = &pendingJSON{
			Head: state.pending.inspection.head.String(), IndexTree: state.pending.inspection.indexTree.String(),
			Diff: state.pending.inspection.diff.digest.String(), StateDigest: state.pending.inspection.stateDigest.String(),
			BodyDigest: DigestBytes([]byte(state.pending.body)).String(),
		}
	}
	for _, step := range state.steps {
		item := stepJSON{
			StepID: step.commit.stepID.String(), Commit: step.commit.commit.String(), Tree: step.commit.tree.String(),
			Diff: step.commit.diff.digest.String(), Evidence: step.commit.evidence.String(),
			Checks: make([]checkJSON, 0, len(step.checks)),
		}
		for _, check := range step.checks {
			item.Checks = append(item.Checks, checkJSON{
				CheckID: check.checkID.String(), Evidence: check.evidence.String(),
				Commit: check.commit.String(), Outcome: check.outcome.digest.String(),
			})
		}
		value.Steps = append(value.Steps, item)
	}
	content, err := json.Marshal(value)
	return json.RawMessage(content), err
}
