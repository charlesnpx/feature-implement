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
	attemptHistory   []reviewAttemptHistory
	core             WorkspaceRuntimeProjection
}

type reviewAttemptHistory struct {
	attemptID                 ID
	roundsUsed                uint16
	infrastructureRetriesUsed uint16
	reservations              []reviewAttemptReservation
}

type reviewAttemptReservation struct {
	profileID        ID
	reviewerInstance ID
	idempotencyKey   Digest
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
func (projection ReviewRuntimeProjection) RoundsUsed(attemptID ID) uint16 {
	if history, exists := projection.attemptHistoryFor(attemptID); exists {
		return history.roundsUsed
	}
	return 0
}

func (projection ReviewRuntimeProjection) InfrastructureRetriesUsed(attemptID ID) uint16 {
	if history, exists := projection.attemptHistoryFor(attemptID); exists {
		return history.infrastructureRetriesUsed
	}
	return 0
}

func (projection ReviewRuntimeProjection) attemptHistoryFor(attemptID ID) (reviewAttemptHistory, bool) {
	if index := reviewAttemptHistoryIndex(projection.attemptHistory, attemptID); index >= 0 {
		return cloneReviewAttemptHistory(projection.attemptHistory[index]), true
	}
	return reviewAttemptHistory{}, false
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
	switch event := record.event.(type) {
	case ReviewHeadAdoptedJournalEvent:
		if index := reviewStateIndex(current.states, event.attemptID); index >= 0 {
			// A review is valid only for its exact head and tree. An ordinary
			// follow-up commit replaces that head, so discard the old review state
			next.states = append(next.states[:index:index], next.states[index+1:]...)
		}
	case ReviewRoundStartedJournalEvent:
		attempt, exists := current.core.Attempt(event.attemptID)
		if !exists || attempt.phase != AttemptActive || attempt.generation != event.generation ||
			attempt.mergeUnit != event.mergeUnit || attempt.verifiedHead != event.head ||
			current.workspaceID != event.workspaceID || current.activeGeneration != event.generation {
			return ReviewRuntimeProjection{}, fmt.Errorf("review round does not match an active exact-head attempt")
		}
		if current.RoundsUsed(event.attemptID) >= event.loop.maxRounds {
			return ReviewRuntimeProjection{}, reviewRoundBudgetExhaustedError(event.loop.maxRounds)
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
		next.attemptHistory = incrementReviewRoundCount(next.attemptHistory, event.attemptID)
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
		if err := current.validateNewReviewInvocationReservation(
			event.attemptID, current.states[index].loop, event.reservation,
		); err != nil {
			return ReviewRuntimeProjection{}, err
		}
		state, err := ReduceReview(current.states[index], domain)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		next.states[index] = state
		next.attemptHistory = appendReviewInvocationReservation(
			next.attemptHistory, event.attemptID, event.reservation,
		)
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
		state.exhaustion = deriveReviewExhaustionAtUsage(
			state, current.RoundsUsed(event.attemptID),
			current.InfrastructureRetriesUsed(event.attemptID),
		)
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
		state.exhaustion = deriveReviewExhaustionAtUsage(
			state, current.RoundsUsed(event.attemptID),
			current.InfrastructureRetriesUsed(event.attemptID),
		)
		next.states[index] = state
	}
	return next, nil
}

func reviewStateIndex(states []ReviewState, attemptID ID) int {
	for index, state := range states {
		if state.attemptID == attemptID {
			return index
		}
	}
	return -1
}

func reviewAttemptHistoryIndex(history []reviewAttemptHistory, attemptID ID) int {
	for index, item := range history {
		if item.attemptID == attemptID {
			return index
		}
	}
	return -1
}

func incrementReviewRoundCount(
	history []reviewAttemptHistory, attemptID ID,
) []reviewAttemptHistory {
	if index := reviewAttemptHistoryIndex(history, attemptID); index >= 0 {
		history[index].roundsUsed++
		return history
	}
	return append(history, reviewAttemptHistory{attemptID: attemptID, roundsUsed: 1})
}

func appendReviewInvocationReservation(
	history []reviewAttemptHistory,
	attemptID ID,
	reservation ReviewInvocationReservation,
) []reviewAttemptHistory {
	index := reviewAttemptHistoryIndex(history, attemptID)
	if index < 0 {
		history = append(history, reviewAttemptHistory{attemptID: attemptID})
		index = len(history) - 1
	}
	history[index].reservations = append(
		history[index].reservations,
		reviewAttemptReservation{
			profileID:        reservation.request.profile.id,
			reviewerInstance: reservation.reviewerInstance,
			idempotencyKey:   reservation.idempotencyKey,
		},
	)
	if reservation.request.invocation > 1 {
		history[index].infrastructureRetriesUsed++
	}
	return history
}

func (projection ReviewRuntimeProjection) validateNewReviewInvocationReservation(
	attemptID ID,
	loop ReviewLoop,
	reservation ReviewInvocationReservation,
) error {
	history, _ := projection.attemptHistoryFor(attemptID)
	for _, prior := range history.reservations {
		if prior.idempotencyKey == reservation.idempotencyKey {
			return fmt.Errorf("review invocation idempotency key is already bound")
		}
	}
	if reservation.request.invocation > 1 &&
		history.infrastructureRetriesUsed >= loop.maxInfrastructureRetries {
		return reviewInfrastructureRetryBudgetExhaustedError(loop.maxInfrastructureRetries)
	}
	return validateReviewInstancePolicy(
		reservation.request.profile,
		reservation.reviewerInstance,
		reviewReviewerInstanceUsesFromHistory(history),
	)
}

func reviewReviewerInstanceUsesFromHistory(
	history reviewAttemptHistory,
) []reviewReviewerInstanceUse {
	uses := make([]reviewReviewerInstanceUse, 0, len(history.reservations))
	for _, reservation := range history.reservations {
		uses = append(uses, reviewReviewerInstanceUse{
			profileID: reservation.profileID, instance: reservation.reviewerInstance,
		})
	}
	return uses
}

func cloneReviewAttemptHistory(source reviewAttemptHistory) reviewAttemptHistory {
	source.reservations = append(
		[]reviewAttemptReservation(nil), source.reservations...,
	)
	return source
}

func cloneReviewRuntime(source ReviewRuntimeProjection) ReviewRuntimeProjection {
	result := source
	result.states = make([]ReviewState, 0, len(source.states))
	for _, state := range source.states {
		result.states = append(result.states, cloneReviewState(state))
	}
	result.attemptHistory = make([]reviewAttemptHistory, 0, len(source.attemptHistory))
	for _, history := range source.attemptHistory {
		result.attemptHistory = append(result.attemptHistory, cloneReviewAttemptHistory(history))
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
	type stateJSON struct {
		AttemptID   string      `json:"attempt_id"`
		PlanID      string      `json:"plan_id"`
		MergeUnitID string      `json:"merge_unit_id"`
		Generation  string      `json:"generation"`
		Loop        string      `json:"loop_digest"`
		Head        string      `json:"head"`
		Tree        string      `json:"tree"`
		Rounds      []roundJSON `json:"rounds"`
	}
	type attemptReservationJSON struct {
		Profile        string `json:"profile"`
		Reviewer       string `json:"reviewer_instance"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	type attemptHistoryJSON struct {
		AttemptID                 string                   `json:"attempt_id"`
		RoundsUsed                uint16                   `json:"rounds_used"`
		InfrastructureRetriesUsed uint16                   `json:"infrastructure_retries_used"`
		Reservations              []attemptReservationJSON `json:"reservations"`
	}
	type runtimeJSON struct {
		SchemaVersion    int                  `json:"schema_version"`
		WorkspaceID      string               `json:"workspace_id"`
		ActiveGeneration string               `json:"active_generation"`
		States           []stateJSON          `json:"states"`
		AttemptHistory   []attemptHistoryJSON `json:"attempt_history"`
	}
	value := runtimeJSON{
		SchemaVersion: 2, WorkspaceID: projection.workspaceID.String(),
		ActiveGeneration: projection.activeGeneration.String(), States: make([]stateJSON, 0, len(projection.states)),
		AttemptHistory: make([]attemptHistoryJSON, 0, len(projection.attemptHistory)),
	}
	for _, state := range projection.states {
		item := stateJSON{
			AttemptID: state.attemptID.String(), PlanID: state.mergeUnit.planID.String(),
			MergeUnitID: state.mergeUnit.mergeUnitID.String(), Generation: state.generation.String(),
			Loop: state.loop.digest.String(), Head: state.head.String(), Tree: state.tree.String(),
			Rounds: make([]roundJSON, 0, len(state.rounds)),
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
		value.States = append(value.States, item)
	}
	sort.Slice(value.States, func(i, j int) bool { return value.States[i].AttemptID < value.States[j].AttemptID })
	for _, history := range projection.attemptHistory {
		item := attemptHistoryJSON{
			AttemptID: history.attemptID.String(), RoundsUsed: history.roundsUsed,
			InfrastructureRetriesUsed: history.infrastructureRetriesUsed,
			Reservations:              make([]attemptReservationJSON, 0, len(history.reservations)),
		}
		for _, reservation := range history.reservations {
			item.Reservations = append(item.Reservations, attemptReservationJSON{
				Profile: reservation.profileID.String(), Reviewer: reservation.reviewerInstance.String(),
				IdempotencyKey: reservation.idempotencyKey.String(),
			})
		}
		value.AttemptHistory = append(value.AttemptHistory, item)
	}
	sort.Slice(value.AttemptHistory, func(i, j int) bool {
		return value.AttemptHistory[i].AttemptID < value.AttemptHistory[j].AttemptID
	})
	return json.Marshal(value)
}
