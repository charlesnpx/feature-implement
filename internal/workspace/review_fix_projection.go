package workspace

import (
	"encoding/json"
	"fmt"
)

func reduceReviewFixRuntime(
	current WorkspaceRuntimeProjection,
	next *WorkspaceRuntimeProjection,
	record JournalRecord,
) error {
	if next == nil || current.workspaceID.IsZero() || current.activeGeneration.IsZero() {
		return fmt.Errorf("review-fix events require an initialized workspace runtime")
	}
	if record.generation != current.activeGeneration {
		return fmt.Errorf("review-fix event generation is not active")
	}
	var workspaceID, attemptID ID
	var generation Digest
	switch event := record.event.(type) {
	case ReviewFixReservedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	case ReviewFixIntendedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	case ReviewFixCommitRecordedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	case ReviewFixCheckRecordedJournalEvent:
		workspaceID, generation, attemptID = event.workspaceID, event.generation, event.attemptID
	default:
		return fmt.Errorf("unsupported review-fix runtime event %T", record.event)
	}
	index, attempt, err := requireRuntimeAttempt(current, attemptID, workspaceID, generation)
	if err != nil {
		return err
	}
	if attempt.phase != AttemptActive {
		return fmt.Errorf("attempt %s must be active for review-fix transitions", attemptID)
	}
	updated := &next.attempts[index]
	switch event := record.event.(type) {
	case ReviewFixReservedJournalEvent:
		if attempt.commitProtocol != nil && attempt.commitProtocol.phase != CommitProtocolComplete {
			return fmt.Errorf("review-fix reservation requires a completed implementation protocol")
		}
		if event.parent != attempt.verifiedHead && (attempt.commitProtocol != nil || attempt.reviewFixes != nil) {
			return fmt.Errorf("review-fix reservation parent does not match the durable attempt head")
		}
		var state ReviewFixState
		if attempt.reviewFixes == nil {
			state, err = NewReviewFixState(event.generation, event.parent, event.protocol, event.maximum)
			if err != nil {
				return err
			}
		} else {
			state = cloneReviewFixState(*attempt.reviewFixes)
			if state.generation != event.generation || state.protocol.digest != event.protocol.digest ||
				state.maximum != event.maximum {
				return fmt.Errorf("review-fix reservation does not match the durable generation, protocol, and budget")
			}
		}
		transition, err := NewReserveReviewFix(event.ordinal, event.parent)
		if err != nil {
			return err
		}
		reduction, err := ReduceReviewFix(state, transition)
		if err != nil {
			return err
		}
		state = reduction.State()
		fixes := state.fixes
		if fixes[len(fixes)-1].reservationKey != event.reservationKey {
			return fmt.Errorf("review-fix reservation does not match its derived key")
		}
		updated.reviewFixes = &state
		updated.verifiedHead = event.parent
		return nil
	case ReviewFixIntendedJournalEvent:
		state, err := requireAttemptReviewFix(attempt, event.protocolDigest)
		if err != nil {
			return err
		}
		if state.Phase() != ReviewFixReserved || event.ordinal != state.Used() {
			return fmt.Errorf("review-fix intent has no current reservation")
		}
		fix := state.fixes[len(state.fixes)-1]
		step, err := state.protocol.Step(event.ordinal)
		if err != nil {
			return err
		}
		if event.stepID != step.id || event.parent != fix.parent || event.reservationKey != fix.reservationKey {
			return fmt.Errorf("review-fix intent does not match its reservation")
		}
		transition, err := newStageReviewFix(event.ordinal, event.inspection, event.body, attempt.commitProtocol)
		if err != nil {
			return err
		}
		reduction, err := ReduceReviewFix(state, transition)
		if err != nil {
			return err
		}
		effects := reduction.Effects()
		if len(effects) != 1 {
			return fmt.Errorf("review-fix intent did not produce one closed commit effect")
		}
		effect, ok := effects[0].(CreateConfiguredCommitEffect)
		if !ok || effect.idempotencyKey != event.idempotencyKey {
			return fmt.Errorf("review-fix intent effect does not match journal idempotency key")
		}
		state = reduction.State()
		updated.reviewFixes = &state
		return nil
	case ReviewFixCommitRecordedJournalEvent:
		state, err := requireAttemptReviewFix(attempt, event.protocolDigest)
		if err != nil {
			return err
		}
		if state.Phase() != ReviewFixAwaitingCommit || event.ordinal != state.Used() {
			return fmt.Errorf("review-fix commit record has no pending intent")
		}
		effects, err := PendingReviewFixEffects(state)
		if err != nil || len(effects) != 1 {
			return fmt.Errorf("load pending review-fix commit effect: %w", err)
		}
		effect, ok := effects[0].(CreateConfiguredCommitEffect)
		if !ok || effect.idempotencyKey != event.intentKey {
			return fmt.Errorf("review-fix commit record does not match durable intent")
		}
		transition, err := NewRecordReviewFixCommit(event.ordinal, event.evidence)
		if err != nil {
			return err
		}
		reduction, err := ReduceReviewFix(state, transition)
		if err != nil {
			return err
		}
		state = reduction.State()
		updated.reviewFixes = &state
		updated.verifiedHead = event.evidence.commit
		return nil
	case ReviewFixCheckRecordedJournalEvent:
		state, err := requireAttemptReviewFix(attempt, event.protocolDigest)
		if err != nil {
			return err
		}
		expectedOrdinal := state.Used()
		if state.checkingFix >= 0 {
			expectedOrdinal = uint16(state.checkingFix + 1)
		}
		if state.Phase() != ReviewFixAwaitingChecks || event.ordinal != expectedOrdinal {
			return fmt.Errorf("review-fix check record has no pending check")
		}
		effects, err := PendingReviewFixEffects(state)
		if err != nil || len(effects) != 1 {
			return fmt.Errorf("load pending review-fix check effect: %w", err)
		}
		effect, ok := effects[0].(RunConfiguredCheckEffect)
		if !ok || effect.stepOrdinal != event.ordinal || effect.checkOrdinal != event.checkOrdinal ||
			effect.idempotencyKey != event.idempotencyKey {
			return fmt.Errorf("review-fix check record does not match the next configured check")
		}
		transition, err := NewRecordReviewFixCheck(event.ordinal, event.evidence)
		if err != nil {
			return err
		}
		reduction, err := ReduceReviewFix(state, transition)
		if err != nil {
			return err
		}
		state = reduction.State()
		updated.reviewFixes = &state
		return nil
	default:
		return fmt.Errorf("unsupported review-fix runtime event %T", record.event)
	}
}

func requireAttemptReviewFix(
	attempt RuntimeAttemptProjection,
	protocolDigest Digest,
) (ReviewFixState, error) {
	if attempt.reviewFixes == nil {
		return ReviewFixState{}, fmt.Errorf("attempt %s has no review-fix runtime", attempt.attemptID)
	}
	state := cloneReviewFixState(*attempt.reviewFixes)
	if state.protocol.digest != protocolDigest {
		return ReviewFixState{}, fmt.Errorf("attempt review-fix protocol digest does not match event")
	}
	return state, nil
}

func canonicalReviewFixRuntime(state ReviewFixState) (json.RawMessage, error) {
	if err := validateReviewFixState(state); err != nil {
		return nil, err
	}
	type checkJSON struct {
		CheckID  string `json:"check_id"`
		Evidence string `json:"evidence"`
		Commit   string `json:"commit"`
		Outcome  string `json:"outcome"`
	}
	type fixJSON struct {
		Ordinal        uint16         `json:"ordinal"`
		Parent         string         `json:"parent"`
		Phase          ReviewFixPhase `json:"phase"`
		ReservationKey string         `json:"reservation_key"`
		IndexTree      string         `json:"index_tree,omitempty"`
		Diff           string         `json:"diff,omitempty"`
		StateDigest    string         `json:"state_digest,omitempty"`
		BodyDigest     string         `json:"body_digest,omitempty"`
		IntentKey      string         `json:"intent_key,omitempty"`
		Commit         string         `json:"commit,omitempty"`
		Tree           string         `json:"tree,omitempty"`
		Evidence       string         `json:"evidence,omitempty"`
		Checks         []checkJSON    `json:"checks"`
	}
	type runtimeJSON struct {
		Generation  string         `json:"generation"`
		Base        string         `json:"base"`
		Protocol    string         `json:"protocol"`
		Maximum     uint16         `json:"maximum"`
		Used        uint16         `json:"used"`
		Remaining   uint16         `json:"remaining"`
		Phase       ReviewFixPhase `json:"phase"`
		RebaseEpoch uint64         `json:"rebase_epoch"`
		CheckingFix uint16         `json:"checking_fix"`
		Fixes       []fixJSON      `json:"fixes"`
	}
	value := runtimeJSON{
		Generation: state.generation.String(), Base: state.base.String(), Protocol: state.protocol.digest.String(),
		Maximum: state.maximum, Used: state.Used(), Remaining: state.Remaining(), Phase: state.Phase(),
		RebaseEpoch: state.rebaseEpoch, Fixes: make([]fixJSON, 0, len(state.fixes)),
	}
	if state.checkingFix >= 0 {
		value.CheckingFix = uint16(state.checkingFix + 1)
	}
	for _, fix := range state.fixes {
		item := fixJSON{
			Ordinal: fix.ordinal, Parent: fix.parent.String(), Phase: fix.phase,
			ReservationKey: fix.reservationKey.String(), Checks: make([]checkJSON, 0, len(fix.checks)),
		}
		if !fix.inspection.stateDigest.IsZero() {
			item.IndexTree = fix.inspection.indexTree.String()
			item.Diff = fix.inspection.diff.digest.String()
			item.StateDigest = fix.inspection.stateDigest.String()
			item.BodyDigest = DigestBytes([]byte(fix.body)).String()
			item.IntentKey = fix.intentKey.String()
		}
		if !fix.commit.evidence.IsZero() {
			item.Commit = fix.commit.commit.String()
			item.Tree = fix.commit.tree.String()
			item.Evidence = fix.commit.evidence.String()
		}
		for _, check := range fix.checks {
			item.Checks = append(item.Checks, checkJSON{
				CheckID: check.checkID.String(), Evidence: check.evidence.String(),
				Commit: check.commit.String(), Outcome: check.outcome.digest.String(),
			})
		}
		value.Fixes = append(value.Fixes, item)
	}
	content, err := json.Marshal(value)
	return json.RawMessage(content), err
}
