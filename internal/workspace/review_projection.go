package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ReviewRuntimeProjection is the journal-derived gate ledger paired with the
// ordinary workspace projection. It records adapter requests and terminal gate
// facts only.
type ReviewRuntimeProjection struct {
	workspaceID      ID
	activeGeneration Digest
	states           []ReviewGateState
	core             WorkspaceRuntimeProjection
}

func (projection ReviewRuntimeProjection) State(attemptID ID) (ReviewGateState, bool) {
	if index := reviewGateStateIndex(projection.states, attemptID); index >= 0 {
		return cloneReviewGateState(projection.states[index]), true
	}
	return ReviewGateState{}, false
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
		return ReviewRuntimeProjection{}, fmt.Errorf("review gate runtime does not match the effective workspace generation")
	}
	for _, state := range projection.states {
		if state.generation != definition.generation {
			return ReviewRuntimeProjection{}, fmt.Errorf("review gate state has an inactive generation")
		}
		unit, err := executionForMergeUnit(definition.execution, state.mergeUnit)
		if err != nil {
			return ReviewRuntimeProjection{}, err
		}
		config, configured := unit.ReviewGate()
		if !configured || !config.bound() {
			return ReviewRuntimeProjection{}, fmt.Errorf("attempt %s has gate state without a configured review gate", state.attemptID)
		}
		for _, dispatch := range state.dispatches {
			if !reviewGateDispatchMatchesConfig(dispatch, config) {
				return ReviewRuntimeProjection{}, fmt.Errorf("attempt %s gate dispatch does not match its effective configuration", state.attemptID)
			}
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
		// A head adoption changes what can satisfy readiness, but terminal facts
		// about older artifacts remain useful crash-forensics records.
	case ReviewGateDispatchedJournalEvent:
		dispatch := event.dispatch
		attempt, exists := current.core.Attempt(dispatch.attemptID)
		if !exists || attempt.phase != AttemptActive || attempt.generation != dispatch.generation ||
			attempt.mergeUnit != dispatch.mergeUnit || attempt.verifiedHead != dispatch.head ||
			current.workspaceID != dispatch.workspaceID || current.activeGeneration != dispatch.generation {
			return ReviewRuntimeProjection{}, fmt.Errorf("review gate dispatch does not match an active exact-head attempt")
		}
		index := reviewGateStateIndex(current.states, dispatch.attemptID)
		state := ReviewGateState{
			workspaceID: dispatch.workspaceID, generation: dispatch.generation,
			attemptID: dispatch.attemptID, mergeUnit: dispatch.mergeUnit,
		}
		if index >= 0 {
			state = cloneReviewGateState(current.states[index])
			if state.workspaceID != dispatch.workspaceID || state.generation != dispatch.generation ||
				state.mergeUnit != dispatch.mergeUnit {
				return ReviewRuntimeProjection{}, fmt.Errorf("review gate dispatch changes the attempt identity")
			}
		}
		if pending, exists := state.Pending(); exists {
			return ReviewRuntimeProjection{}, fmt.Errorf("review gate dispatch %s remains unresolved", pending.digest)
		}
		if _, exists := state.Dispatch(dispatch.digest); exists {
			return ReviewRuntimeProjection{}, fmt.Errorf("duplicate review gate dispatch %s", dispatch.digest)
		}
		state.dispatches = append(state.dispatches, dispatch)
		if index < 0 {
			next.states = append(next.states, state)
			sortReviewGateStates(next.states)
		} else {
			next.states[index] = state
		}
	case ReviewGateRecordedJournalEvent:
		dispatch, gateRecord := event.dispatch, event.record
		index := reviewGateStateIndex(current.states, dispatch.attemptID)
		if index < 0 {
			return ReviewRuntimeProjection{}, fmt.Errorf("review gate record has no prior dispatch")
		}
		state := cloneReviewGateState(current.states[index])
		storedDispatch, exists := state.Dispatch(dispatch.digest)
		if !exists || storedDispatch != dispatch {
			return ReviewRuntimeProjection{}, fmt.Errorf("review gate record does not match a durable dispatch")
		}
		if existing, exists := state.Record(dispatch.digest); exists {
			if existing == gateRecord {
				return ReviewRuntimeProjection{}, fmt.Errorf("duplicate review gate record %s", gateRecord.digest)
			}
			return ReviewRuntimeProjection{}, fmt.Errorf("review gate dispatch already has a different terminal record")
		}
		if gateRecord.occurredAt != record.occurredAt {
			return ReviewRuntimeProjection{}, fmt.Errorf("review gate record occurrence time does not match its journal record")
		}
		if err := validateReviewGateRecordDocumentContract(dispatch, gateRecord, event.document); err != nil {
			return ReviewRuntimeProjection{}, err
		}
		state.records = append(state.records, gateRecord)
		if event.document != nil {
			state.documentedDispatches = append(state.documentedDispatches, gateRecord.dispatchDigest)
		}
		next.states[index] = state
	}
	return next, nil
}

func reviewGateDispatchMatchesConfig(dispatch ReviewGateDispatch, config ReviewGateConfig) bool {
	return dispatch.adapter == config.adapter && dispatch.recipe == config.recipe &&
		dispatch.policyDigest == config.policyDigest
}

func reviewGateStateIndex(states []ReviewGateState, attemptID ID) int {
	for index, state := range states {
		if state.attemptID == attemptID {
			return index
		}
	}
	return -1
}

func cloneReviewRuntime(source ReviewRuntimeProjection) ReviewRuntimeProjection {
	result := source
	result.states = make([]ReviewGateState, 0, len(source.states))
	for _, state := range source.states {
		result.states = append(result.states, cloneReviewGateState(state))
	}
	result.core = cloneWorkspaceRuntime(source.core)
	return result
}

func canonicalReviewRuntime(projection ReviewRuntimeProjection) ([]byte, error) {
	type dispatchJSON struct {
		Digest string `json:"digest"`
	}
	type gateRecordJSON struct {
		Digest           string `json:"digest"`
		DocumentArtifact bool   `json:"document_artifact"`
	}
	type stateJSON struct {
		AttemptID   string           `json:"attempt_id"`
		PlanID      string           `json:"plan_id"`
		MergeUnitID string           `json:"merge_unit_id"`
		Generation  string           `json:"generation"`
		Dispatches  []dispatchJSON   `json:"dispatches"`
		Records     []gateRecordJSON `json:"records"`
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
			Dispatches: make([]dispatchJSON, 0, len(state.dispatches)),
			Records:    make([]gateRecordJSON, 0, len(state.records)),
		}
		for _, dispatch := range state.dispatches {
			item.Dispatches = append(item.Dispatches, dispatchJSON{Digest: dispatch.digest.String()})
		}
		for _, gateRecord := range state.records {
			item.Records = append(item.Records, gateRecordJSON{
				Digest: gateRecord.digest.String(), DocumentArtifact: state.hasDocumentArtifact(gateRecord.dispatchDigest),
			})
		}
		value.States = append(value.States, item)
	}
	sort.Slice(value.States, func(i, j int) bool { return value.States[i].AttemptID < value.States[j].AttemptID })
	return json.Marshal(value)
}
