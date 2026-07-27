package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

type ReviewRuntimeProjection struct {
	workspaceID      ID
	activeGeneration Digest
	states           []ReviewState
	core             WorkspaceRuntimeProjection
}

func (projection ReviewRuntimeProjection) WorkspaceID() ID { return projection.workspaceID }
func (projection ReviewRuntimeProjection) ActiveGeneration() Digest {
	return projection.activeGeneration
}
func (projection ReviewRuntimeProjection) States() []ReviewState {
	result := make([]ReviewState, 0, len(projection.states))
	for _, state := range projection.states {
		result = append(result, cloneReviewState(state))
	}
	return result
}
func (projection ReviewRuntimeProjection) State(attemptID ID) (ReviewState, bool) {
	for _, state := range projection.states {
		if state.attemptID == attemptID {
			return cloneReviewState(state), true
		}
	}
	return ReviewState{}, false
}

func RebuildReviewRuntime(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
) (ReviewRuntimeProjection, error) {
	projection, err := RebuildProjection(snapshot, ReviewRuntimeProjection{}, reduceReviewRuntime)
	if err != nil {
		return ReviewRuntimeProjection{}, err
	}
	if projection.workspaceID != definition.workspace.id || projection.activeGeneration != definition.generation {
		return ReviewRuntimeProjection{}, fmt.Errorf("review runtime does not match the effective workspace generation")
	}
	for _, state := range projection.states {
		if state.generation != definition.generation {
			continue
		}
		unit, err := executionForMergeUnit(definition.execution, state.mergeUnit)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		loop, configured := unit.ReviewLoop()
		if !configured || loop.digest != state.loop.digest {
			return ReviewRuntimeProjection{}, fmt.Errorf(
				"attempt %s review state does not match its effective configured loop",
				state.attemptID,
			)
		}
	}
	for _, attempt := range projection.core.attempts {
		if attempt.generation != definition.generation {
			continue
		}
		unit, err := executionForMergeUnit(definition.execution, attempt.mergeUnit)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		if _, configured := unit.ReviewLoop(); !configured {
			continue
		}
		index := reviewStateIndex(projection.states, attempt.attemptID)
		if index < 0 {
			if attempt.reviewFixes != nil {
				return ReviewRuntimeProjection{}, fmt.Errorf(
					"attempt %s has review-fix protocol state without a completed review round reservation",
					attempt.attemptID,
				)
			}
			continue
		}
		state := projection.states[index]
		used := uint16(0)
		if attempt.reviewFixes != nil {
			protocol, configured := unit.ReviewFixProtocol()
			if !configured || attempt.reviewFixes.generation != definition.generation ||
				attempt.reviewFixes.protocol.digest != protocol.digest ||
				attempt.reviewFixes.maximum != unit.policy.maxReviewFixes {
				return ReviewRuntimeProjection{}, fmt.Errorf(
					"attempt %s review-fix protocol state does not match effective review policy",
					attempt.attemptID,
				)
			}
			used = attempt.reviewFixes.Used()
		}
		if state.pendingFix == nil {
			if used != state.FixesUsed() {
				return ReviewRuntimeProjection{}, fmt.Errorf(
					"attempt %s has review-fix protocol progress without an application reservation",
					attempt.attemptID,
				)
			}
		} else if state.pendingFix.ordinal != state.FixesUsed()+1 ||
			(used != state.FixesUsed() && used != state.FixesUsed()+1) {
			return ReviewRuntimeProjection{}, fmt.Errorf(
				"attempt %s pending review fix does not match durable protocol progress",
				attempt.attemptID,
			)
		}
	}
	return projection, nil
}

func VerifyReviewRuntimeConformance(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
) (Digest, error) {
	digest, err := VerifyReplayConformance(
		snapshot,
		func() ReviewRuntimeProjection { return ReviewRuntimeProjection{} },
		reduceReviewRuntime,
		canonicalReviewRuntime,
		func(state ReviewRuntimeProjection) Digest { return state.activeGeneration },
		definition.generation,
	)
	if err != nil {
		return Digest{}, err
	}
	if _, err := RebuildReviewRuntime(snapshot, definition); err != nil {
		return Digest{}, err
	}
	return digest, nil
}

func reduceReviewRuntime(current ReviewRuntimeProjection, record JournalRecord) (ReviewRuntimeProjection, error) {
	next := cloneReviewRuntime(current)
	core, err := reduceWorkspaceRuntime(current.core, record)
	if err != nil {
		return ReviewRuntimeProjection{}, err
	}
	next.core = core
	next.workspaceID, next.activeGeneration = core.workspaceID, core.activeGeneration
	if isReviewFixJournalEvent(record.event) {
		if err := validateReviewFixJournalReservation(current, record.event); err != nil {
			return ReviewRuntimeProjection{}, err
		}
	}

	switch event := record.event.(type) {
	case ReviewHeadAdoptedJournalEvent:
		if reviewStateIndex(current.states, event.attemptID) >= 0 {
			return ReviewRuntimeProjection{}, fmt.Errorf("review head adoption cannot follow durable review state")
		}
	case ReviewRoundStartedJournalEvent:
		attempt, exists := current.core.Attempt(event.attemptID)
		if !exists || attempt.phase != AttemptActive || attempt.generation != event.generation ||
			attempt.mergeUnit != event.mergeUnit || attempt.verifiedHead != event.head ||
			current.workspaceID != event.workspaceID || current.activeGeneration != event.generation {
			return ReviewRuntimeProjection{}, fmt.Errorf("review round does not match an active exact-head attempt")
		}
		start, err := NewStartReviewRound(
			event.workspaceID, event.generation, event.attemptID, event.mergeUnit,
			event.loop, event.ordinal, event.head, event.tree,
		)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		index := reviewStateIndex(current.states, event.attemptID)
		state := ReviewState{}
		if index >= 0 {
			state = current.states[index]
		}
		state, err = ReduceReview(state, start)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		if index < 0 {
			next.states = append(next.states, state)
			sort.Slice(next.states, func(i, j int) bool { return next.states[i].attemptID.String() < next.states[j].attemptID.String() })
		} else {
			next.states[index] = state
		}
	case ReviewInvocationReservedJournalEvent:
		index := reviewStateIndex(current.states, event.attemptID)
		attempt, exists := current.core.Attempt(event.attemptID)
		if index < 0 || !exists || attempt.phase != AttemptActive ||
			attempt.verifiedHead != current.states[index].head || current.workspaceID != event.workspaceID ||
			current.activeGeneration != event.generation || current.states[index].loop.digest != event.loopDigest {
			return ReviewRuntimeProjection{}, fmt.Errorf("review invocation reservation does not match active exact-head review state")
		}
		domain, err := NewReserveReviewInvocation(
			event.reservation.request, event.reservation.reviewerInstance, event.reservation.idempotencyKey,
		)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		state, err := ReduceReview(current.states[index], domain)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		next.states[index] = state
	case ReviewInvocationFailedJournalEvent:
		index := reviewStateIndex(current.states, event.attemptID)
		attempt, exists := current.core.Attempt(event.attemptID)
		if index < 0 || !exists || attempt.phase != AttemptActive ||
			attempt.verifiedHead != current.states[index].head || current.workspaceID != event.workspaceID ||
			current.activeGeneration != event.generation || current.states[index].loop.digest != event.loopDigest {
			return ReviewRuntimeProjection{}, fmt.Errorf("review invocation failure does not match active exact-head review state")
		}
		domain, err := NewRecordReviewInvocationFailure(event.reservationDigest, event.failureDigest)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		state, err := ReduceReview(current.states[index], domain)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		next.states[index] = state
	case ReviewResultRecordedJournalEvent:
		index := reviewStateIndex(current.states, event.attemptID)
		attempt, exists := current.core.Attempt(event.attemptID)
		if index < 0 || !exists || attempt.phase != AttemptActive || attempt.verifiedHead != current.states[index].head ||
			current.workspaceID != event.workspaceID || current.activeGeneration != event.generation ||
			current.states[index].loop.digest != event.loopDigest {
			return ReviewRuntimeProjection{}, fmt.Errorf("review result does not match the active durable review state")
		}
		domain, err := NewRecordReviewResult(
			event.round, event.profileOrdinal, event.invocation, event.reservationDigest,
			event.result,
		)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		state, err := ReduceReview(current.states[index], domain)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		next.states[index] = state
	case ReviewFindingFixReservedJournalEvent:
		index := reviewStateIndex(current.states, event.attemptID)
		attempt, exists := current.core.Attempt(event.attemptID)
		if index < 0 || !exists || attempt.phase != AttemptActive ||
			attempt.verifiedHead != current.states[index].head || current.workspaceID != event.workspaceID ||
			current.activeGeneration != event.generation || current.states[index].loop.digest != event.loopDigest {
			return ReviewRuntimeProjection{}, fmt.Errorf("review finding-fix reservation does not match active exact-head review state")
		}
		usedFixes := uint16(0)
		if attempt.reviewFixes != nil {
			usedFixes = attempt.reviewFixes.Used()
		}
		if usedFixes != current.states[index].FixesUsed() {
			return ReviewRuntimeProjection{}, fmt.Errorf("review finding-fix reservation does not match applied fix counters")
		}
		domain, err := NewReserveReviewFindingFix(event.reservation)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		state, err := ReduceReview(current.states[index], domain)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		next.states[index] = state
	case ReviewFixAppliedJournalEvent:
		index := reviewStateIndex(current.states, event.attemptID)
		attempt, exists := current.core.Attempt(event.attemptID)
		if index < 0 || !exists || attempt.phase != AttemptActive || attempt.reviewFixes == nil ||
			attempt.verifiedHead != event.fix.head || current.workspaceID != event.workspaceID ||
			current.activeGeneration != event.generation || current.states[index].loop.digest != event.loopDigest {
			return ReviewRuntimeProjection{}, fmt.Errorf("review fix does not match the active attempt and durable review-fix protocol")
		}
		fixState := attempt.reviewFixes
		if fixState.Used() != event.fix.ordinal || !fixState.Quiescent() || len(fixState.fixes) == 0 {
			return ReviewRuntimeProjection{}, fmt.Errorf("review fix event does not match the completed review-fix ordinal")
		}
		commit := fixState.fixes[len(fixState.fixes)-1].commit
		if commit.commit != event.fix.head || commit.tree != event.fix.tree || commit.parent != event.fix.priorHead ||
			commit.evidence != event.fix.evidence {
			return ReviewRuntimeProjection{}, fmt.Errorf("review fix event does not match durable commit evidence")
		}
		state, err := ReduceReview(current.states[index], event.fix)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		next.states[index] = state
	}
	return next, nil
}

func validateReviewFixJournalReservation(
	current ReviewRuntimeProjection,
	event WorkspaceJournalEvent,
) error {
	var workspaceID, attemptID ID
	var generation Digest
	var ordinal uint16
	var parent GitObjectID
	switch value := event.(type) {
	case ReviewFixReservedJournalEvent:
		workspaceID, generation, attemptID, ordinal, parent =
			value.workspaceID, value.generation, value.attemptID, value.ordinal, value.parent
	case ReviewFixIntendedJournalEvent:
		workspaceID, generation, attemptID, ordinal, parent =
			value.workspaceID, value.generation, value.attemptID, value.ordinal, value.parent
	case ReviewFixCommitRecordedJournalEvent:
		workspaceID, generation, attemptID, ordinal, parent =
			value.workspaceID, value.generation, value.attemptID, value.ordinal, value.evidence.parent
	case ReviewFixCheckRecordedJournalEvent:
		workspaceID, generation, attemptID, ordinal =
			value.workspaceID, value.generation, value.attemptID, value.ordinal
	default:
		return fmt.Errorf("unsupported review-fix reservation transition %T", event)
	}
	index := reviewStateIndex(current.states, attemptID)
	if index < 0 {
		return nil
	}
	state := current.states[index]
	attempt, exists := current.core.Attempt(attemptID)
	if !exists || attempt.phase != AttemptActive || current.workspaceID != workspaceID ||
		current.activeGeneration != generation || state.workspaceID != workspaceID || state.generation != generation {
		return fmt.Errorf("review-fix protocol transition does not match the active review attempt")
	}
	reservation := state.pendingFix
	if reservation == nil {
		_, isRebasedCheck := event.(ReviewFixCheckRecordedJournalEvent)
		if isRebasedCheck && ordinal <= state.FixesUsed() && attempt.reviewFixes != nil &&
			attempt.reviewFixes.rebaseEpoch > 0 && attempt.reviewFixes.checkingFix >= 0 {
			return nil
		}
		return fmt.Errorf("review-fix protocol transition has no matching accepted-finding reservation")
	}
	if reservation.ordinal != ordinal || reservation.ordinal != state.FixesUsed()+1 {
		return fmt.Errorf("review-fix protocol transition has no matching accepted-finding reservation")
	}
	if !parent.IsZero() && parent != reservation.head {
		return fmt.Errorf("review-fix protocol transition does not match its exact reviewed parent")
	}
	return nil
}

func reviewStateIndex(states []ReviewState, attemptID ID) int {
	for index, state := range states {
		if state.attemptID == attemptID {
			return index
		}
	}
	return -1
}

func cloneReviewRuntime(source ReviewRuntimeProjection) ReviewRuntimeProjection {
	result := source
	result.states = make([]ReviewState, 0, len(source.states))
	for _, state := range source.states {
		result.states = append(result.states, cloneReviewState(state))
	}
	result.core = cloneWorkspaceRuntime(source.core)
	return result
}

func canonicalReviewRuntime(projection ReviewRuntimeProjection) ([]byte, error) {
	type resultJSON struct {
		Request     string `json:"request_digest"`
		Reservation string `json:"reservation_digest"`
		Result      string `json:"result_digest"`
	}
	type reservationJSON struct {
		Request        string `json:"request_digest"`
		Reviewer       string `json:"reviewer_instance"`
		IdempotencyKey string `json:"idempotency_key"`
		Digest         string `json:"digest"`
	}
	type failureJSON struct {
		Reservation string `json:"reservation_digest"`
		Failure     string `json:"failure_digest"`
	}
	type roundJSON struct {
		Ordinal      uint16            `json:"ordinal"`
		Head         string            `json:"head"`
		Tree         string            `json:"tree"`
		Reservations []reservationJSON `json:"reservations"`
		Failures     []failureJSON     `json:"failures"`
		Attempts     []resultJSON      `json:"attempts"`
		Results      []resultJSON      `json:"results"`
	}
	type fixJSON struct {
		Ordinal     uint16   `json:"ordinal"`
		Round       uint16   `json:"round"`
		Reservation string   `json:"reservation_digest"`
		PriorHead   string   `json:"prior_head"`
		PriorTree   string   `json:"prior_tree"`
		Head        string   `json:"head"`
		Tree        string   `json:"tree"`
		Evidence    string   `json:"evidence_digest"`
		Findings    []string `json:"finding_ids"`
	}
	type stateJSON struct {
		AttemptID   string      `json:"attempt_id"`
		PlanID      string      `json:"plan_id"`
		MergeUnitID string      `json:"merge_unit_id"`
		Generation  string      `json:"generation"`
		Loop        string      `json:"loop_digest"`
		Head        string      `json:"head"`
		Tree        string      `json:"tree"`
		Rounds      []roundJSON `json:"rounds"`
		Fixes       []fixJSON   `json:"fixes"`
		PendingFix  string      `json:"pending_fix_reservation,omitempty"`
		Exhaustion  string      `json:"exhaustion_directive,omitempty"`
	}
	type runtimeJSON struct {
		SchemaVersion    int         `json:"schema_version"`
		WorkspaceID      string      `json:"workspace_id"`
		ActiveGeneration string      `json:"active_generation"`
		States           []stateJSON `json:"states"`
	}
	value := runtimeJSON{
		SchemaVersion: 2, WorkspaceID: projection.workspaceID.String(),
		ActiveGeneration: projection.activeGeneration.String(), States: make([]stateJSON, 0, len(projection.states)),
	}
	for _, state := range projection.states {
		item := stateJSON{
			AttemptID: state.attemptID.String(), PlanID: state.mergeUnit.planID.String(),
			MergeUnitID: state.mergeUnit.mergeUnitID.String(), Generation: state.generation.String(),
			Loop: state.loop.digest.String(), Head: state.head.String(), Tree: state.tree.String(),
			Rounds: make([]roundJSON, 0, len(state.rounds)), Fixes: make([]fixJSON, 0, len(state.fixes)),
		}
		for _, round := range state.rounds {
			roundItem := roundJSON{
				Ordinal: round.ordinal, Head: round.head.String(), Tree: round.tree.String(),
				Reservations: make([]reservationJSON, 0, len(round.reservations)),
				Failures:     make([]failureJSON, 0, len(round.failures)),
				Attempts:     make([]resultJSON, 0, len(round.attempts)), Results: make([]resultJSON, 0, len(round.results)),
			}
			for _, reservation := range round.reservations {
				roundItem.Reservations = append(roundItem.Reservations, reservationJSON{
					Request: reservation.request.digest.String(), Reviewer: reservation.reviewerInstance.String(),
					IdempotencyKey: reservation.idempotencyKey.String(), Digest: reservation.digest.String(),
				})
			}
			for _, failure := range round.failures {
				roundItem.Failures = append(roundItem.Failures, failureJSON{
					Reservation: failure.reservationDigest.String(), Failure: failure.failureDigest.String(),
				})
			}
			for _, result := range round.attempts {
				roundItem.Attempts = append(roundItem.Attempts, resultJSON{
					Request: result.request.digest.String(), Reservation: result.reservationDigest.String(),
					Result: result.submission.digest.String(),
				})
			}
			for _, result := range round.results {
				roundItem.Results = append(roundItem.Results, resultJSON{
					Request: result.request.digest.String(), Reservation: result.reservationDigest.String(),
					Result: result.submission.digest.String(),
				})
			}
			item.Rounds = append(item.Rounds, roundItem)
		}
		for _, fix := range state.fixes {
			findingIDs := make([]string, 0, len(fix.findings))
			for _, finding := range fix.findings {
				findingIDs = append(findingIDs, finding.String())
			}
			item.Fixes = append(item.Fixes, fixJSON{
				Ordinal: fix.ordinal, Round: fix.round, Reservation: fix.reservationDigest.String(),
				PriorHead: fix.priorHead.String(), PriorTree: fix.priorTree.String(),
				Head: fix.head.String(), Tree: fix.tree.String(), Evidence: fix.evidence.String(), Findings: findingIDs,
			})
		}
		if state.pendingFix != nil {
			item.PendingFix = state.pendingFix.digest.String()
		}
		if state.exhaustion != nil {
			item.Exhaustion = state.exhaustion.digest.String()
		}
		value.States = append(value.States, item)
	}
	sort.Slice(value.States, func(i, j int) bool { return value.States[i].AttemptID < value.States[j].AttemptID })
	return json.Marshal(value)
}
