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
	case ReviewResultRecordedJournalEvent:
		index := reviewStateIndex(current.states, event.attemptID)
		attempt, exists := current.core.Attempt(event.attemptID)
		if index < 0 || !exists || attempt.phase != AttemptActive || attempt.verifiedHead != current.states[index].head ||
			current.workspaceID != event.workspaceID || current.activeGeneration != event.generation ||
			current.states[index].loop.digest != event.loopDigest {
			return ReviewRuntimeProjection{}, fmt.Errorf("review result does not match the active durable review state")
		}
		if reviewReceiptSeen(current.states, event.receiptDigest) {
			return ReviewRuntimeProjection{}, fmt.Errorf("control-plane review receipt %s was replayed", event.receiptDigest)
		}
		domain, err := NewRecordReviewResult(
			event.round, event.profileOrdinal, event.invocation, event.result, event.receiptDigest,
		)
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

func reviewReceiptSeen(states []ReviewState, receipt Digest) bool {
	for _, state := range states {
		for _, round := range state.rounds {
			for _, result := range round.attempts {
				if result.receiptDigest == receipt {
					return true
				}
			}
		}
	}
	return false
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
		Request string `json:"request_digest"`
		Result  string `json:"result_digest"`
		Receipt string `json:"receipt_digest"`
	}
	type roundJSON struct {
		Ordinal  uint16       `json:"ordinal"`
		Head     string       `json:"head"`
		Tree     string       `json:"tree"`
		Attempts []resultJSON `json:"attempts"`
		Results  []resultJSON `json:"results"`
	}
	type fixJSON struct {
		Ordinal   uint16   `json:"ordinal"`
		PriorHead string   `json:"prior_head"`
		PriorTree string   `json:"prior_tree"`
		Head      string   `json:"head"`
		Tree      string   `json:"tree"`
		Evidence  string   `json:"evidence_digest"`
		Findings  []string `json:"finding_ids"`
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
				Attempts: make([]resultJSON, 0, len(round.attempts)), Results: make([]resultJSON, 0, len(round.results)),
			}
			for _, result := range round.attempts {
				roundItem.Attempts = append(roundItem.Attempts, resultJSON{
					Request: result.request.digest.String(), Result: result.submission.digest.String(),
					Receipt: result.receiptDigest.String(),
				})
			}
			for _, result := range round.results {
				roundItem.Results = append(roundItem.Results, resultJSON{
					Request: result.request.digest.String(), Result: result.submission.digest.String(),
					Receipt: result.receiptDigest.String(),
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
				Ordinal: fix.ordinal, PriorHead: fix.priorHead.String(), PriorTree: fix.priorTree.String(),
				Head: fix.head.String(), Tree: fix.tree.String(), Evidence: fix.evidence.String(), Findings: findingIDs,
			})
		}
		if state.exhaustion != nil {
			item.Exhaustion = state.exhaustion.digest.String()
		}
		value.States = append(value.States, item)
	}
	sort.Slice(value.States, func(i, j int) bool { return value.States[i].AttemptID < value.States[j].AttemptID })
	return json.Marshal(value)
}
