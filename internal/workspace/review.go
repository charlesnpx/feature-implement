package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ReviewGateVerdict is the terminal fact recorded by an adapter. The local
// workflow records the fact and its artifact bindings; it does not interpret
// the policy that led to it.
type ReviewGateVerdict string

const (
	ReviewGateSatisfied    ReviewGateVerdict = "satisfied"
	ReviewGateNotSatisfied ReviewGateVerdict = "not_satisfied"
	ReviewGateFailedToRun  ReviewGateVerdict = "failed_to_run"
)

func (verdict ReviewGateVerdict) valid() bool {
	switch verdict {
	case ReviewGateSatisfied, ReviewGateNotSatisfied, ReviewGateFailedToRun:
		return true
	default:
		return false
	}
}

// ReviewGateDispatch is the durable intent that precedes handing a frozen
// tree to an adapter. Its digest identifies one repeatable request, including
// the policy bytes selected by the bundle.
type ReviewGateDispatch struct {
	workspaceID  ID
	generation   Digest
	attemptID    ID
	mergeUnit    MergeUnitReference
	adapter      ID
	recipe       ID
	policyDigest Digest
	head         GitObjectID
	tree         GitObjectID
	digest       Digest
}

type ReviewGateDispatchOptions struct {
	WorkspaceID  ID
	Generation   Digest
	AttemptID    ID
	MergeUnit    MergeUnitReference
	Adapter      ID
	Recipe       ID
	PolicyDigest Digest
	Head         GitObjectID
	Tree         GitObjectID
}

func NewReviewGateDispatch(options ReviewGateDispatchOptions) (ReviewGateDispatch, error) {
	dispatch := ReviewGateDispatch{
		workspaceID: options.WorkspaceID, generation: options.Generation,
		attemptID: options.AttemptID, mergeUnit: options.MergeUnit,
		adapter: options.Adapter, recipe: options.Recipe,
		policyDigest: options.PolicyDigest, head: options.Head, tree: options.Tree,
	}
	canonical, err := canonicalReviewGateDispatch(dispatch)
	if err != nil {
		return ReviewGateDispatch{}, err
	}
	dispatch.digest = DigestBytes(canonical)
	return dispatch, nil
}

func (dispatch ReviewGateDispatch) WorkspaceID() ID      { return dispatch.workspaceID }
func (dispatch ReviewGateDispatch) AttemptID() ID        { return dispatch.attemptID }
func (dispatch ReviewGateDispatch) Adapter() ID          { return dispatch.adapter }
func (dispatch ReviewGateDispatch) Recipe() ID           { return dispatch.recipe }
func (dispatch ReviewGateDispatch) PolicyDigest() Digest { return dispatch.policyDigest }
func (dispatch ReviewGateDispatch) Head() GitObjectID    { return dispatch.head }
func (dispatch ReviewGateDispatch) Tree() GitObjectID    { return dispatch.tree }
func (dispatch ReviewGateDispatch) Digest() Digest       { return dispatch.digest }

func canonicalReviewGateDispatch(dispatch ReviewGateDispatch) ([]byte, error) {
	if dispatch.workspaceID.IsZero() || dispatch.generation.IsZero() ||
		dispatch.attemptID.IsZero() || dispatch.mergeUnit.planID.IsZero() ||
		dispatch.mergeUnit.mergeUnitID.IsZero() || dispatch.adapter.IsZero() ||
		dispatch.recipe.IsZero() || dispatch.policyDigest.IsZero() ||
		dispatch.head.IsZero() || dispatch.tree.IsZero() ||
		dispatch.head.Algorithm() != dispatch.tree.Algorithm() {
		return nil, fmt.Errorf("review gate dispatch requires exact workspace, adapter, policy, head, and tree bindings")
	}
	return json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		WorkspaceID   string `json:"workspace_id"`
		Generation    string `json:"generation"`
		AttemptID     string `json:"attempt_id"`
		PlanID        string `json:"plan_id"`
		MergeUnitID   string `json:"merge_unit_id"`
		Adapter       string `json:"adapter"`
		Recipe        string `json:"recipe"`
		PolicyDigest  string `json:"policy_digest"`
		Head          string `json:"head"`
		Tree          string `json:"tree"`
	}{
		SchemaVersion: 2, WorkspaceID: dispatch.workspaceID.String(),
		Generation: dispatch.generation.String(), AttemptID: dispatch.attemptID.String(),
		PlanID: dispatch.mergeUnit.planID.String(), MergeUnitID: dispatch.mergeUnit.mergeUnitID.String(),
		Adapter: dispatch.adapter.String(), Recipe: dispatch.recipe.String(),
		PolicyDigest: dispatch.policyDigest.String(), Head: dispatch.head.String(), Tree: dispatch.tree.String(),
	})
}

// ReviewGateRecord is the terminal half of a dispatch. EvidenceDigest names
// the adapter-owned durable record; it is required for every outcome,
// including a failure to run.
type ReviewGateRecord struct {
	dispatchDigest Digest
	adapter        ID
	recipe         ID
	head           GitObjectID
	tree           GitObjectID
	verdict        ReviewGateVerdict
	evidenceDigest Digest
	policyDigest   Digest
	occurredAt     time.Time
	digest         Digest
}

type ReviewGateRecordOptions struct {
	Dispatch       ReviewGateDispatch
	Verdict        ReviewGateVerdict
	EvidenceDigest Digest
	OccurredAt     time.Time
}

func NewReviewGateRecord(options ReviewGateRecordOptions) (ReviewGateRecord, error) {
	record := ReviewGateRecord{
		dispatchDigest: options.Dispatch.digest, adapter: options.Dispatch.adapter,
		recipe: options.Dispatch.recipe, head: options.Dispatch.head, tree: options.Dispatch.tree,
		verdict: options.Verdict, evidenceDigest: options.EvidenceDigest,
		policyDigest: options.Dispatch.policyDigest, occurredAt: options.OccurredAt.UTC(),
	}
	canonical, err := canonicalReviewGateRecord(record)
	if err != nil {
		return ReviewGateRecord{}, err
	}
	record.digest = DigestBytes(canonical)
	return record, nil
}

func (record ReviewGateRecord) DispatchDigest() Digest { return record.dispatchDigest }
func (record ReviewGateRecord) Adapter() ID            { return record.adapter }
func (record ReviewGateRecord) Recipe() ID             { return record.recipe }
func (record ReviewGateRecord) Head() GitObjectID      { return record.head }
func (record ReviewGateRecord) Tree() GitObjectID      { return record.tree }
func (record ReviewGateRecord) Verdict() ReviewGateVerdict {
	return record.verdict
}
func (record ReviewGateRecord) EvidenceDigest() Digest { return record.evidenceDigest }
func (record ReviewGateRecord) PolicyDigest() Digest   { return record.policyDigest }
func (record ReviewGateRecord) OccurredAt() time.Time  { return record.occurredAt }
func (record ReviewGateRecord) Digest() Digest         { return record.digest }

func canonicalReviewGateRecord(record ReviewGateRecord) ([]byte, error) {
	if record.dispatchDigest.IsZero() || record.adapter.IsZero() || record.recipe.IsZero() ||
		record.head.IsZero() || record.tree.IsZero() ||
		record.head.Algorithm() != record.tree.Algorithm() || !record.verdict.valid() ||
		record.evidenceDigest.IsZero() || record.policyDigest.IsZero() || record.occurredAt.IsZero() ||
		record.occurredAt.Location() != time.UTC {
		return nil, fmt.Errorf("review gate record requires dispatch, adapter, exact artifact, verdict, evidence, policy, and occurrence bindings")
	}
	return json.Marshal(struct {
		SchemaVersion  int               `json:"schema_version"`
		DispatchDigest string            `json:"dispatch_digest"`
		Adapter        string            `json:"adapter"`
		Recipe         string            `json:"recipe"`
		Head           string            `json:"head"`
		Tree           string            `json:"tree"`
		Verdict        ReviewGateVerdict `json:"verdict"`
		EvidenceDigest string            `json:"evidence_digest"`
		PolicyDigest   string            `json:"policy_digest"`
		OccurredAt     string            `json:"occurred_at"`
	}{
		SchemaVersion: 2, DispatchDigest: record.dispatchDigest.String(),
		Adapter: record.adapter.String(), Recipe: record.recipe.String(),
		Head: record.head.String(), Tree: record.tree.String(), Verdict: record.verdict,
		EvidenceDigest: record.evidenceDigest.String(), PolicyDigest: record.policyDigest.String(),
		OccurredAt: record.occurredAt.Format(time.RFC3339Nano),
	})
}

// ReviewGateState is a projection of durable requests and terminal records for
// one attempt. It intentionally exposes no assessment details.
type ReviewGateState struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	mergeUnit   MergeUnitReference
	dispatches  []ReviewGateDispatch
	records     []ReviewGateRecord
}

func (state ReviewGateState) Generation() Digest            { return state.generation }
func (state ReviewGateState) AttemptID() ID                 { return state.attemptID }
func (state ReviewGateState) MergeUnit() MergeUnitReference { return state.mergeUnit }
func (state ReviewGateState) Dispatches() []ReviewGateDispatch {
	return append([]ReviewGateDispatch(nil), state.dispatches...)
}
func (state ReviewGateState) Records() []ReviewGateRecord {
	return append([]ReviewGateRecord(nil), state.records...)
}

func (state ReviewGateState) Dispatch(digest Digest) (ReviewGateDispatch, bool) {
	for _, dispatch := range state.dispatches {
		if dispatch.digest == digest {
			return dispatch, true
		}
	}
	return ReviewGateDispatch{}, false
}

func (state ReviewGateState) Record(dispatchDigest Digest) (ReviewGateRecord, bool) {
	for _, record := range state.records {
		if record.dispatchDigest == dispatchDigest {
			return record, true
		}
	}
	return ReviewGateRecord{}, false
}

func (state ReviewGateState) Pending() (ReviewGateDispatch, bool) {
	for index := len(state.dispatches) - 1; index >= 0; index-- {
		dispatch := state.dispatches[index]
		if _, recorded := state.Record(dispatch.digest); !recorded {
			return dispatch, true
		}
	}
	return ReviewGateDispatch{}, false
}

func (state ReviewGateState) Satisfied(
	config ReviewGateConfig, head, tree GitObjectID,
) (ReviewGateRecord, bool) {
	for index := len(state.records) - 1; index >= 0; index-- {
		record := state.records[index]
		if record.verdict != ReviewGateSatisfied || record.adapter != config.adapter ||
			record.recipe != config.recipe || record.policyDigest != config.policyDigest ||
			record.head != head || record.tree != tree {
			continue
		}
		dispatch, exists := state.Dispatch(record.dispatchDigest)
		if exists && dispatch.adapter == record.adapter && dispatch.recipe == record.recipe &&
			dispatch.policyDigest == record.policyDigest && dispatch.head == record.head && dispatch.tree == record.tree {
			return record, true
		}
	}
	return ReviewGateRecord{}, false
}

func cloneReviewGateState(state ReviewGateState) ReviewGateState {
	state.dispatches = append([]ReviewGateDispatch(nil), state.dispatches...)
	state.records = append([]ReviewGateRecord(nil), state.records...)
	return state
}

func sortReviewGateStates(states []ReviewGateState) {
	sort.Slice(states, func(i, j int) bool {
		return states[i].attemptID.String() < states[j].attemptID.String()
	})
}

// ReviewGateReadiness is the immutable evidence used by integration when the
// configured gate is satisfied against the exact accepted head and tree.
type ReviewGateReadiness struct {
	workspaceID ID
	generation  Digest
	attemptID   ID
	mergeUnit   MergeUnitReference
	head        GitObjectID
	tree        GitObjectID
	dispatch    Digest
	gateRecord  Digest
	digest      Digest
}

func newReviewGateReadiness(
	definition EffectiveWorkspaceDefinition,
	attempt RuntimeAttemptProjection,
	state ReviewGateState,
	config ReviewGateConfig,
	head, tree GitObjectID,
) (ReviewGateReadiness, error) {
	if attempt.attemptID.IsZero() || attempt.generation != definition.generation ||
		attempt.verifiedHead != head || !config.bound() {
		return ReviewGateReadiness{}, fmt.Errorf("review gate readiness requires an active configured exact attempt")
	}
	record, satisfied := state.Satisfied(config, head, tree)
	if !satisfied {
		return ReviewGateReadiness{}, fmt.Errorf("attempt %s has no satisfied review gate for the exact head and tree", attempt.attemptID)
	}
	readiness := ReviewGateReadiness{
		workspaceID: definition.workspace.id, generation: definition.generation,
		attemptID: attempt.attemptID, mergeUnit: attempt.mergeUnit, head: head, tree: tree,
		dispatch: record.dispatchDigest, gateRecord: record.digest,
	}
	canonical, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		WorkspaceID   string `json:"workspace_id"`
		Generation    string `json:"generation"`
		AttemptID     string `json:"attempt_id"`
		PlanID        string `json:"plan_id"`
		MergeUnitID   string `json:"merge_unit_id"`
		Head          string `json:"head"`
		Tree          string `json:"tree"`
		Dispatch      string `json:"dispatch_digest"`
		GateRecord    string `json:"gate_record_digest"`
	}{
		SchemaVersion: 2, WorkspaceID: readiness.workspaceID.String(), Generation: readiness.generation.String(),
		AttemptID: readiness.attemptID.String(), PlanID: readiness.mergeUnit.planID.String(),
		MergeUnitID: readiness.mergeUnit.mergeUnitID.String(), Head: readiness.head.String(), Tree: readiness.tree.String(),
		Dispatch: readiness.dispatch.String(), GateRecord: readiness.gateRecord.String(),
	})
	if err != nil {
		return ReviewGateReadiness{}, err
	}
	readiness.digest = DigestBytes(canonical)
	return readiness, nil
}

func (readiness ReviewGateReadiness) WorkspaceID() ID               { return readiness.workspaceID }
func (readiness ReviewGateReadiness) Generation() Digest            { return readiness.generation }
func (readiness ReviewGateReadiness) AttemptID() ID                 { return readiness.attemptID }
func (readiness ReviewGateReadiness) MergeUnit() MergeUnitReference { return readiness.mergeUnit }
func (readiness ReviewGateReadiness) Head() GitObjectID             { return readiness.head }
func (readiness ReviewGateReadiness) Tree() GitObjectID             { return readiness.tree }
func (readiness ReviewGateReadiness) DispatchDigest() Digest        { return readiness.dispatch }
func (readiness ReviewGateReadiness) GateRecordDigest() Digest      { return readiness.gateRecord }
func (readiness ReviewGateReadiness) Digest() Digest                { return readiness.digest }
