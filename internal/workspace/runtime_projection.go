package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

type ProjectionReducer[State any] func(State, JournalRecord) (State, error)

// RebuildProjection is the generic disposable-projection engine. Reducers are
// expected to be pure and return a new state for each immutable record.
func RebuildProjection[State any](snapshot JournalSnapshot, initial State, reduce ProjectionReducer[State]) (State, error) {
	if reduce == nil {
		return initial, fmt.Errorf("projection reducer is required")
	}
	state := initial
	for _, record := range snapshot.records {
		next, err := reduce(state, record)
		if err != nil {
			return initial, fmt.Errorf("project journal record %d: %w", record.sequence, err)
		}
		state = next
	}
	return state, nil
}

// VerifyReplayConformance rebuilds twice from fresh initial states, compares
// canonical bytes, and proves both projections bind the expected generation.
func VerifyReplayConformance[State any](
	snapshot JournalSnapshot,
	initial func() State,
	reduce ProjectionReducer[State],
	canonical func(State) ([]byte, error),
	activeGeneration func(State) Digest,
	expectedGeneration Digest,
) (Digest, error) {
	if initial == nil || reduce == nil || canonical == nil || activeGeneration == nil || expectedGeneration.IsZero() {
		return Digest{}, fmt.Errorf("replay conformance requires constructors, reducer, canonicalizer, and active generation")
	}
	first, err := RebuildProjection(snapshot, initial(), reduce)
	if err != nil {
		return Digest{}, err
	}
	second, err := RebuildProjection(snapshot, initial(), reduce)
	if err != nil {
		return Digest{}, err
	}
	firstBytes, err := canonical(first)
	if err != nil {
		return Digest{}, err
	}
	secondBytes, err := canonical(second)
	if err != nil {
		return Digest{}, err
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		return Digest{}, fmt.Errorf("projection replay is nondeterministic")
	}
	if activeGeneration(first) != expectedGeneration || activeGeneration(second) != expectedGeneration {
		return Digest{}, fmt.Errorf("projection replay does not bind active generation %s", expectedGeneration)
	}
	return DigestBytes(firstBytes), nil
}

type RuntimeCandidateProjection struct {
	generation Digest
	record     uint64
	recovered  bool
	activated  bool
}

func (candidate RuntimeCandidateProjection) Generation() Digest { return candidate.generation }
func (candidate RuntimeCandidateProjection) Record() uint64     { return candidate.record }
func (candidate RuntimeCandidateProjection) Recovered() bool    { return candidate.recovered }
func (candidate RuntimeCandidateProjection) Activated() bool    { return candidate.activated }

type RuntimeActivationProjection struct {
	prior             Digest
	active            Digest
	record            uint64
	comparisonDigest  Digest
	ownerReceipt      Digest
	history           RuntimeHistoryBinding
	changedMergeUnits []MergeUnitReference
}

func (activation RuntimeActivationProjection) PriorGeneration() Digest  { return activation.prior }
func (activation RuntimeActivationProjection) ActiveGeneration() Digest { return activation.active }
func (activation RuntimeActivationProjection) Record() uint64           { return activation.record }
func (activation RuntimeActivationProjection) ComparisonDigest() Digest {
	return activation.comparisonDigest
}
func (activation RuntimeActivationProjection) OwnerReceiptDigest() Digest {
	return activation.ownerReceipt
}
func (activation RuntimeActivationProjection) History() RuntimeHistoryBinding {
	return activation.history
}
func (activation RuntimeActivationProjection) ChangedMergeUnits() []MergeUnitReference {
	return append([]MergeUnitReference(nil), activation.changedMergeUnits...)
}

type RuntimeRecoveryProjection struct {
	record        uint64
	generation    Digest
	discardOffset int64
	discardSize   int64
	discardDigest Digest
	resultingHead Digest
}

func (recovery RuntimeRecoveryProjection) Record() uint64        { return recovery.record }
func (recovery RuntimeRecoveryProjection) Generation() Digest    { return recovery.generation }
func (recovery RuntimeRecoveryProjection) DiscardOffset() int64  { return recovery.discardOffset }
func (recovery RuntimeRecoveryProjection) DiscardSize() int64    { return recovery.discardSize }
func (recovery RuntimeRecoveryProjection) DiscardDigest() Digest { return recovery.discardDigest }
func (recovery RuntimeRecoveryProjection) ResultingHead() Digest { return recovery.resultingHead }

type WorkspaceRuntimeProjection struct {
	workspaceID       ID
	activeGeneration  Digest
	planCheckpoint    GitObjectID
	generationHistory []Digest
	candidates        []RuntimeCandidateProjection
	activations       []RuntimeActivationProjection
	recoveries        []RuntimeRecoveryProjection
	attempts          []RuntimeAttemptProjection
}

func (projection WorkspaceRuntimeProjection) WorkspaceID() ID { return projection.workspaceID }
func (projection WorkspaceRuntimeProjection) ActiveGeneration() Digest {
	return projection.activeGeneration
}
func (projection WorkspaceRuntimeProjection) PlanCheckpoint() GitObjectID {
	return projection.planCheckpoint
}
func (projection WorkspaceRuntimeProjection) GenerationHistory() []Digest {
	return append([]Digest(nil), projection.generationHistory...)
}
func (projection WorkspaceRuntimeProjection) Candidates() []RuntimeCandidateProjection {
	return append([]RuntimeCandidateProjection(nil), projection.candidates...)
}
func (projection WorkspaceRuntimeProjection) Activations() []RuntimeActivationProjection {
	result := append([]RuntimeActivationProjection(nil), projection.activations...)
	for index := range result {
		result[index].changedMergeUnits = append([]MergeUnitReference(nil), result[index].changedMergeUnits...)
	}
	return result
}
func (projection WorkspaceRuntimeProjection) Recoveries() []RuntimeRecoveryProjection {
	return append([]RuntimeRecoveryProjection(nil), projection.recoveries...)
}
func (projection WorkspaceRuntimeProjection) HasCandidate(generation Digest) bool {
	for _, candidate := range projection.candidates {
		if candidate.generation == generation {
			return true
		}
	}
	return false
}

func (projection WorkspaceRuntimeProjection) HasActivatableCandidate(generation Digest) bool {
	for _, candidate := range projection.candidates {
		if candidate.generation == generation && !candidate.activated {
			return true
		}
	}
	return false
}

func RebuildWorkspaceRuntime(snapshot JournalSnapshot) (WorkspaceRuntimeProjection, error) {
	return RebuildProjection(snapshot, WorkspaceRuntimeProjection{}, reduceWorkspaceRuntime)
}

func VerifyWorkspaceRuntimeConformance(snapshot JournalSnapshot, expectedGeneration Digest) (Digest, error) {
	return VerifyReplayConformance(
		snapshot,
		func() WorkspaceRuntimeProjection { return WorkspaceRuntimeProjection{} },
		reduceWorkspaceRuntime,
		canonicalWorkspaceRuntime,
		func(state WorkspaceRuntimeProjection) Digest { return state.activeGeneration },
		expectedGeneration,
	)
}

func reduceWorkspaceRuntime(current WorkspaceRuntimeProjection, record JournalRecord) (WorkspaceRuntimeProjection, error) {
	next := cloneWorkspaceRuntime(current)
	switch event := record.event.(type) {
	case WorkspaceInitializedJournalEvent:
		if !current.activeGeneration.IsZero() || len(current.generationHistory) != 0 ||
			len(current.candidates) != 0 || len(current.activations) != 0 || len(current.attempts) != 0 {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("workspace initialization must be the first and only initialization event")
		}
		if current.workspaceID.IsZero() {
			if record.sequence != 1 {
				return WorkspaceRuntimeProjection{}, fmt.Errorf("workspace initialization must be the first event unless bootstrap recovery was recorded")
			}
		} else {
			if current.workspaceID != event.workspaceID || len(current.recoveries) == 0 {
				return WorkspaceRuntimeProjection{}, fmt.Errorf("workspace initialization does not match bootstrap recovery")
			}
			for _, recovery := range current.recoveries {
				if recovery.generation != event.generation {
					return WorkspaceRuntimeProjection{}, fmt.Errorf("workspace initialization generation does not match bootstrap recovery")
				}
			}
		}
		next.workspaceID = event.workspaceID
		next.activeGeneration = event.generation
		next.planCheckpoint = event.planCheckpoint
		next.generationHistory = []Digest{event.generation}
	case CandidateGenerationStoredJournalEvent:
		if current.workspaceID != event.workspaceID || current.activeGeneration != event.activeGeneration {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("candidate generation is not based on the active workspace generation")
		}
		if current.HasCandidate(event.candidateGeneration) || containsDigest(current.generationHistory, event.candidateGeneration) {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("candidate generation %s is already recorded", event.candidateGeneration)
		}
		next.candidates = append(next.candidates, RuntimeCandidateProjection{
			generation: event.candidateGeneration, record: record.sequence, recovered: event.recovered,
		})
	case GenerationActivatedJournalEvent:
		if current.workspaceID != event.workspaceID || current.activeGeneration != event.priorGeneration {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("generation activation has a stale prior generation")
		}
		candidateIndex := -1
		for index, candidate := range current.candidates {
			if candidate.generation == event.activeGeneration && !candidate.activated {
				candidateIndex = index
				break
			}
		}
		if candidateIndex < 0 {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("generation activation references an unstaged candidate")
		}
		for _, attempt := range current.attempts {
			if attempt.phase.nonterminal() {
				return WorkspaceRuntimeProjection{}, fmt.Errorf("generation activation is blocked by nonterminal attempt %s", attempt.attemptID)
			}
		}
		next.candidates[candidateIndex].activated = true
		next.activeGeneration = event.activeGeneration
		next.generationHistory = append(next.generationHistory, event.activeGeneration)
		next.activations = append(next.activations, RuntimeActivationProjection{
			prior: event.priorGeneration, active: event.activeGeneration, record: record.sequence,
			comparisonDigest: event.comparisonDigest, ownerReceipt: event.ownerReceiptDigest,
			history: event.history, changedMergeUnits: append([]MergeUnitReference(nil), event.changedMergeUnits...),
		})
	case JournalTailRecoveredEvent:
		if current.activeGeneration.IsZero() {
			if current.workspaceID.IsZero() {
				if record.sequence != 1 {
					return WorkspaceRuntimeProjection{}, fmt.Errorf("bootstrap journal recovery must begin the journal")
				}
				next.workspaceID = event.workspaceID
			} else if current.workspaceID != event.workspaceID {
				return WorkspaceRuntimeProjection{}, fmt.Errorf("bootstrap journal recovery workspace does not match prior recovery")
			}
			for _, recovery := range current.recoveries {
				if recovery.generation != event.generation {
					return WorkspaceRuntimeProjection{}, fmt.Errorf("bootstrap journal recovery generation does not match prior recovery")
				}
			}
		} else if current.workspaceID != event.workspaceID || current.activeGeneration != event.generation {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("journal recovery generation does not match the active workspace")
		}
		if record.previousHash != event.resultingHead {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("journal recovery resulting head does not match its previous hash")
		}
		next.recoveries = append(next.recoveries, RuntimeRecoveryProjection{
			record: record.sequence, generation: event.generation,
			discardOffset: event.discardOffset, discardSize: event.discardSize,
			discardDigest: event.discardDigest, resultingHead: event.resultingHead,
		})
	default:
		if event, ok := record.event.(ReviewHeadAdoptedJournalEvent); ok {
			if err := reduceReviewHeadAdoption(current, &next, record, event); err != nil {
				return WorkspaceRuntimeProjection{}, err
			}
		} else if isAttemptJournalEvent(record.event) {
			if err := reduceAttemptRuntime(current, &next, record); err != nil {
				return WorkspaceRuntimeProjection{}, err
			}
		} else if isCommitJournalEvent(record.event) {
			if err := reduceCommitRuntime(current, &next, record); err != nil {
				return WorkspaceRuntimeProjection{}, err
			}
		} else if isAuthorizationJournalEvent(record.event) {
			// Authorization has its own definition-aware projection. The core
			// runtime deliberately does not reinterpret its protected payloads.
		} else if isReviewJournalEvent(record.event) {
			// Review has its own definition-aware projection. The core attempt
			// runtime remains the Git and review-fix source of truth.
		} else if isProviderJournalEvent(record.event) {
			// Provider effects and completion receipts have their own
			// definition-aware projection. The core attempt runtime never
			// reinterprets trusted broker evidence.
		} else {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("unsupported runtime event %T", record.event)
		}
	}
	return next, nil
}

func reduceReviewHeadAdoption(
	current WorkspaceRuntimeProjection,
	next *WorkspaceRuntimeProjection,
	record JournalRecord,
	event ReviewHeadAdoptedJournalEvent,
) error {
	if next == nil || current.workspaceID.IsZero() || current.activeGeneration.IsZero() {
		return fmt.Errorf("review head adoption requires an initialized workspace runtime")
	}
	if record.generation != current.activeGeneration {
		return fmt.Errorf("review head adoption generation is not active")
	}
	index, attempt, err := requireRuntimeAttempt(
		current, event.attemptID, event.workspaceID, event.generation,
	)
	if err != nil {
		return err
	}
	if attempt.phase != AttemptActive || attempt.mergeUnit != event.mergeUnit ||
		attempt.verifiedHead != event.priorHead {
		return fmt.Errorf("review head adoption does not match the active attempt and prior head")
	}
	if attempt.commitProtocol != nil || attempt.reviewFixes != nil {
		return fmt.Errorf("review head adoption is only allowed without durable commit protocols")
	}
	updated := &next.attempts[index]
	updated.verifiedHead = event.head
	updated.inspectionDigest = event.snapshotDigest
	return nil
}

func cloneWorkspaceRuntime(source WorkspaceRuntimeProjection) WorkspaceRuntimeProjection {
	result := source
	result.generationHistory = append([]Digest(nil), source.generationHistory...)
	result.candidates = append([]RuntimeCandidateProjection(nil), source.candidates...)
	result.activations = append([]RuntimeActivationProjection(nil), source.activations...)
	for index := range result.activations {
		result.activations[index].changedMergeUnits = append([]MergeUnitReference(nil), source.activations[index].changedMergeUnits...)
	}
	result.recoveries = append([]RuntimeRecoveryProjection(nil), source.recoveries...)
	result.attempts = cloneRuntimeAttempts(source.attempts)
	return result
}

func canonicalWorkspaceRuntime(projection WorkspaceRuntimeProjection) ([]byte, error) {
	type candidateJSON struct {
		Generation string `json:"generation"`
		Record     uint64 `json:"record"`
		Recovered  bool   `json:"recovered"`
		Activated  bool   `json:"activated"`
	}
	type activationJSON struct {
		Prior             string                   `json:"prior_generation"`
		Active            string                   `json:"active_generation"`
		Record            uint64                   `json:"record"`
		ComparisonDigest  string                   `json:"comparison_digest"`
		OwnerReceipt      string                   `json:"owner_receipt_digest"`
		BudgetHistory     string                   `json:"budget_history_digest"`
		ApprovalHistory   string                   `json:"approval_history_digest"`
		EvidenceHistory   string                   `json:"evidence_history_digest"`
		ChangedMergeUnits []mergeUnitReferenceJSON `json:"changed_merge_units"`
	}
	type recoveryJSON struct {
		Record        uint64 `json:"record"`
		Generation    string `json:"generation"`
		DiscardOffset int64  `json:"discard_offset"`
		DiscardSize   int64  `json:"discard_size"`
		DiscardDigest string `json:"discard_digest"`
		ResultingHead string `json:"resulting_head"`
	}
	type runtimeJSON struct {
		SchemaVersion     int               `json:"schema_version"`
		WorkspaceID       string            `json:"workspace_id"`
		ActiveGeneration  string            `json:"active_generation"`
		PlanCheckpoint    string            `json:"plan_checkpoint,omitempty"`
		GenerationHistory []string          `json:"generation_history"`
		Candidates        []candidateJSON   `json:"candidates"`
		Activations       []activationJSON  `json:"activations"`
		Recoveries        []recoveryJSON    `json:"recoveries"`
		Attempts          []json.RawMessage `json:"attempts"`
	}
	value := runtimeJSON{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: projection.workspaceID.String(),
		ActiveGeneration:  projection.activeGeneration.String(),
		PlanCheckpoint:    projection.planCheckpoint.String(),
		GenerationHistory: make([]string, 0, len(projection.generationHistory)),
		Candidates:        make([]candidateJSON, 0, len(projection.candidates)),
		Activations:       make([]activationJSON, 0, len(projection.activations)),
		Recoveries:        make([]recoveryJSON, 0, len(projection.recoveries)),
		Attempts:          make([]json.RawMessage, 0, len(projection.attempts)),
	}
	for _, generation := range projection.generationHistory {
		value.GenerationHistory = append(value.GenerationHistory, generation.String())
	}
	for _, candidate := range projection.candidates {
		value.Candidates = append(value.Candidates, candidateJSON{
			Generation: candidate.generation.String(), Record: candidate.record,
			Recovered: candidate.recovered, Activated: candidate.activated,
		})
	}
	for _, activation := range projection.activations {
		changed := make([]mergeUnitReferenceJSON, 0, len(activation.changedMergeUnits))
		for _, reference := range activation.changedMergeUnits {
			changed = append(changed, mergeUnitReferenceJSON{PlanID: reference.planID.String(), MergeUnitID: reference.mergeUnitID.String()})
		}
		value.Activations = append(value.Activations, activationJSON{
			Prior: activation.prior.String(), Active: activation.active.String(), Record: activation.record,
			ComparisonDigest: activation.comparisonDigest.String(), OwnerReceipt: activation.ownerReceipt.String(),
			BudgetHistory: activation.history.budgets.String(), ApprovalHistory: activation.history.approvals.String(),
			EvidenceHistory: activation.history.evidence.String(), ChangedMergeUnits: changed,
		})
	}
	for _, recovery := range projection.recoveries {
		value.Recoveries = append(value.Recoveries, recoveryJSON{
			Record: recovery.record, Generation: recovery.generation.String(),
			DiscardOffset: recovery.discardOffset, DiscardSize: recovery.discardSize,
			DiscardDigest: recovery.discardDigest.String(), ResultingHead: recovery.resultingHead.String(),
		})
	}
	for _, attempt := range projection.attempts {
		canonical, err := canonicalAttemptRuntime(attempt)
		if err != nil {
			return nil, err
		}
		value.Attempts = append(value.Attempts, canonical)
	}
	return json.Marshal(value)
}

func containsDigest(values []Digest, wanted Digest) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sortedCandidateGenerations(projection WorkspaceRuntimeProjection) []Digest {
	result := make([]Digest, 0, len(projection.candidates))
	for _, candidate := range projection.candidates {
		result = append(result, candidate.generation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}
