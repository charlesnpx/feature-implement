package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type MergeUnitRuntimeDisposition string

const (
	MergeUnitFuture          MergeUnitRuntimeDisposition = "future"
	MergeUnitReserved        MergeUnitRuntimeDisposition = "reserved"
	MergeUnitMaterializing   MergeUnitRuntimeDisposition = "materializing"
	MergeUnitActive          MergeUnitRuntimeDisposition = "active"
	MergeUnitPaused          MergeUnitRuntimeDisposition = "paused"
	MergeUnitReviewExhausted MergeUnitRuntimeDisposition = "review_exhausted"
	MergeUnitCompleted       MergeUnitRuntimeDisposition = "completed"
	MergeUnitFailed          MergeUnitRuntimeDisposition = "failed"
	MergeUnitAbandoned       MergeUnitRuntimeDisposition = "abandoned"
)

func (disposition MergeUnitRuntimeDisposition) valid() bool {
	switch disposition {
	case MergeUnitFuture, MergeUnitReserved, MergeUnitMaterializing, MergeUnitActive,
		MergeUnitPaused, MergeUnitReviewExhausted, MergeUnitCompleted, MergeUnitFailed, MergeUnitAbandoned:
		return true
	default:
		return false
	}
}

type MergeUnitRuntimeState struct {
	reference   MergeUnitReference
	disposition MergeUnitRuntimeDisposition
	generation  Digest
}

func NewMergeUnitRuntimeState(
	reference MergeUnitReference,
	disposition MergeUnitRuntimeDisposition,
	generation Digest,
) (MergeUnitRuntimeState, error) {
	if reference.planID.IsZero() || reference.mergeUnitID.IsZero() || !disposition.valid() {
		return MergeUnitRuntimeState{}, fmt.Errorf("merge unit runtime state requires a reference and valid disposition")
	}
	if disposition != MergeUnitFuture && generation.IsZero() {
		return MergeUnitRuntimeState{}, fmt.Errorf("non-future merge unit %s requires a generation binding", reference)
	}
	return MergeUnitRuntimeState{reference: reference, disposition: disposition, generation: generation}, nil
}

func (state MergeUnitRuntimeState) Reference() MergeUnitReference { return state.reference }
func (state MergeUnitRuntimeState) Disposition() MergeUnitRuntimeDisposition {
	return state.disposition
}
func (state MergeUnitRuntimeState) Generation() Digest { return state.generation }

type AttemptRuntimePhase string

const (
	AttemptReserved        AttemptRuntimePhase = "reserved"
	AttemptMaterializing   AttemptRuntimePhase = "materializing"
	AttemptActive          AttemptRuntimePhase = "active"
	AttemptPaused          AttemptRuntimePhase = "paused"
	AttemptReviewExhausted AttemptRuntimePhase = "review_exhausted"
	AttemptCompleted       AttemptRuntimePhase = "completed"
	AttemptFailed          AttemptRuntimePhase = "failed"
	AttemptAbandoned       AttemptRuntimePhase = "abandoned"
)

func (phase AttemptRuntimePhase) valid() bool {
	switch phase {
	case AttemptReserved, AttemptMaterializing, AttemptActive, AttemptPaused,
		AttemptReviewExhausted, AttemptCompleted, AttemptFailed, AttemptAbandoned:
		return true
	default:
		return false
	}
}

func (phase AttemptRuntimePhase) nonterminal() bool {
	return phase == AttemptReserved || phase == AttemptMaterializing || phase == AttemptActive ||
		phase == AttemptPaused || phase == AttemptReviewExhausted
}

type AttemptGenerationBinding struct {
	attemptID  ID
	mergeUnit  MergeUnitReference
	generation Digest
	phase      AttemptRuntimePhase
}

func NewAttemptGenerationBinding(
	attemptID ID,
	mergeUnit MergeUnitReference,
	generation Digest,
	phase AttemptRuntimePhase,
) (AttemptGenerationBinding, error) {
	if attemptID.IsZero() || mergeUnit.planID.IsZero() || mergeUnit.mergeUnitID.IsZero() || generation.IsZero() || !phase.valid() {
		return AttemptGenerationBinding{}, fmt.Errorf("attempt requires identity, merge unit, exact generation, and valid phase")
	}
	return AttemptGenerationBinding{attemptID: attemptID, mergeUnit: mergeUnit, generation: generation, phase: phase}, nil
}

func (attempt AttemptGenerationBinding) AttemptID() ID                 { return attempt.attemptID }
func (attempt AttemptGenerationBinding) MergeUnit() MergeUnitReference { return attempt.mergeUnit }
func (attempt AttemptGenerationBinding) Generation() Digest            { return attempt.generation }
func (attempt AttemptGenerationBinding) Phase() AttemptRuntimePhase    { return attempt.phase }

type ProviderIntentRuntimeState struct {
	intentID   ID
	generation Digest
	resolved   bool
}

func NewProviderIntentRuntimeState(intentID ID, generation Digest, resolved bool) (ProviderIntentRuntimeState, error) {
	if intentID.IsZero() || generation.IsZero() {
		return ProviderIntentRuntimeState{}, fmt.Errorf("provider intent requires identity and generation")
	}
	return ProviderIntentRuntimeState{intentID: intentID, generation: generation, resolved: resolved}, nil
}

func (intent ProviderIntentRuntimeState) IntentID() ID       { return intent.intentID }
func (intent ProviderIntentRuntimeState) Generation() Digest { return intent.generation }
func (intent ProviderIntentRuntimeState) Resolved() bool     { return intent.resolved }

type QueueEntryRuntimeState struct {
	entryID    ID
	generation Digest
	resolved   bool
}

func NewQueueEntryRuntimeState(entryID ID, generation Digest, resolved bool) (QueueEntryRuntimeState, error) {
	if entryID.IsZero() || generation.IsZero() {
		return QueueEntryRuntimeState{}, fmt.Errorf("queue entry requires identity and generation")
	}
	return QueueEntryRuntimeState{entryID: entryID, generation: generation, resolved: resolved}, nil
}

func (entry QueueEntryRuntimeState) EntryID() ID        { return entry.entryID }
func (entry QueueEntryRuntimeState) Generation() Digest { return entry.generation }
func (entry QueueEntryRuntimeState) Resolved() bool     { return entry.resolved }

// RuntimeHistoryBinding makes reconciliation preserve the independently
// projected budget, approval, and evidence histories across activation.
type RuntimeHistoryBinding struct {
	budgets   Digest
	approvals Digest
	evidence  Digest
}

func NewRuntimeHistoryBinding(budgets, approvals, evidence Digest) (RuntimeHistoryBinding, error) {
	if budgets.IsZero() || approvals.IsZero() || evidence.IsZero() {
		return RuntimeHistoryBinding{}, fmt.Errorf("runtime history requires budget, approval, and evidence digests")
	}
	return RuntimeHistoryBinding{budgets: budgets, approvals: approvals, evidence: evidence}, nil
}

func EmptyRuntimeHistoryBinding() RuntimeHistoryBinding {
	empty := DigestBytes(nil)
	return RuntimeHistoryBinding{budgets: empty, approvals: empty, evidence: empty}
}

func (history RuntimeHistoryBinding) BudgetDigest() Digest   { return history.budgets }
func (history RuntimeHistoryBinding) ApprovalDigest() Digest { return history.approvals }
func (history RuntimeHistoryBinding) EvidenceDigest() Digest { return history.evidence }

type ReconciliationState struct {
	journalHead JournalHeadBinding
	mergeUnits  []MergeUnitRuntimeState
	attempts    []AttemptGenerationBinding
	intents     []ProviderIntentRuntimeState
	queue       []QueueEntryRuntimeState
	history     RuntimeHistoryBinding
}

type JournalHeadBinding struct{ digest Digest }

func (binding JournalHeadBinding) Digest() Digest { return binding.digest }

func NewReconciliationState(
	snapshot JournalSnapshot,
	mergeUnits []MergeUnitRuntimeState,
	attempts []AttemptGenerationBinding,
	intents []ProviderIntentRuntimeState,
	queue []QueueEntryRuntimeState,
	history RuntimeHistoryBinding,
) (ReconciliationState, error) {
	if snapshot.head.IsZero() || history.budgets.IsZero() || history.approvals.IsZero() || history.evidence.IsZero() {
		return ReconciliationState{}, fmt.Errorf("reconciliation state requires journal and runtime history bindings")
	}
	state := ReconciliationState{
		journalHead: JournalHeadBinding{digest: snapshot.head},
		mergeUnits:  append([]MergeUnitRuntimeState(nil), mergeUnits...),
		attempts:    append([]AttemptGenerationBinding(nil), attempts...),
		intents:     append([]ProviderIntentRuntimeState(nil), intents...),
		queue:       append([]QueueEntryRuntimeState(nil), queue...), history: history,
	}
	if err := normalizeReconciliationState(&state); err != nil {
		return ReconciliationState{}, err
	}
	return state, nil
}

func (state ReconciliationState) JournalHead() JournalHeadBinding { return state.journalHead }
func (state ReconciliationState) MergeUnits() []MergeUnitRuntimeState {
	return append([]MergeUnitRuntimeState(nil), state.mergeUnits...)
}
func (state ReconciliationState) Attempts() []AttemptGenerationBinding {
	return append([]AttemptGenerationBinding(nil), state.attempts...)
}
func (state ReconciliationState) ProviderIntents() []ProviderIntentRuntimeState {
	return append([]ProviderIntentRuntimeState(nil), state.intents...)
}
func (state ReconciliationState) QueueEntries() []QueueEntryRuntimeState {
	return append([]QueueEntryRuntimeState(nil), state.queue...)
}
func (state ReconciliationState) History() RuntimeHistoryBinding { return state.history }

type ReconciliationPlan struct {
	workspaceID         ID
	activeGeneration    Digest
	candidateGeneration Digest
	journalHead         Digest
	stateDigest         Digest
	structuralDigest    Digest
	comparisonDigest    Digest
	changedMergeUnits   []MergeUnitReference
	workspaceRevision   uint64
	candidateRevision   uint64
}

type reconciliationComparisonWire struct {
	SchemaVersion       int                      `json:"schema_version"`
	WorkspaceID         string                   `json:"workspace_id"`
	ActiveGeneration    string                   `json:"active_generation"`
	CandidateGeneration string                   `json:"candidate_generation"`
	JournalHead         string                   `json:"journal_head"`
	StateDigest         string                   `json:"state_digest"`
	StructuralDigest    string                   `json:"structural_digest"`
	WorkspaceRevision   uint64                   `json:"workspace_revision"`
	CandidateRevision   uint64                   `json:"candidate_revision"`
	ChangedMergeUnits   []mergeUnitReferenceJSON `json:"changed_merge_units"`
}

type reconciliationPlanTokenWire struct {
	SchemaVersion    int                          `json:"schema_version"`
	ComparisonDigest string                       `json:"comparison_digest"`
	Comparison       reconciliationComparisonWire `json:"comparison"`
}

func (plan ReconciliationPlan) WorkspaceID() ID             { return plan.workspaceID }
func (plan ReconciliationPlan) ActiveGeneration() Digest    { return plan.activeGeneration }
func (plan ReconciliationPlan) CandidateGeneration() Digest { return plan.candidateGeneration }
func (plan ReconciliationPlan) JournalHead() Digest         { return plan.journalHead }
func (plan ReconciliationPlan) StateDigest() Digest         { return plan.stateDigest }
func (plan ReconciliationPlan) StructuralDigest() Digest    { return plan.structuralDigest }
func (plan ReconciliationPlan) ComparisonDigest() Digest    { return plan.comparisonDigest }
func (plan ReconciliationPlan) ChangedMergeUnits() []MergeUnitReference {
	return append([]MergeUnitReference(nil), plan.changedMergeUnits...)
}

func (plan ReconciliationPlan) TokenBytes() ([]byte, error) {
	comparison := comparisonWireFromPlan(plan)
	comparisonBytes, err := json.Marshal(comparison)
	if err != nil {
		return nil, err
	}
	if DigestBytes(comparisonBytes) != plan.comparisonDigest {
		return nil, fmt.Errorf("reconciliation plan comparison digest is invalid")
	}
	return json.Marshal(reconciliationPlanTokenWire{
		SchemaVersion: JournalSchemaVersion, ComparisonDigest: plan.comparisonDigest.String(), Comparison: comparison,
	})
}

func ParseReconciliationPlanToken(source []byte) (ReconciliationPlan, error) {
	if len(source) == 0 || len(source) > 64*1024 {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation token is empty or exceeds 65536 bytes")
	}
	var wire reconciliationPlanTokenWire
	if err := decodeStrictJSON(source, &wire); err != nil {
		return ReconciliationPlan{}, fmt.Errorf("decode reconciliation token: %w", err)
	}
	if wire.SchemaVersion != JournalSchemaVersion || wire.Comparison.SchemaVersion != JournalSchemaVersion {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation token schema_version must be %d", JournalSchemaVersion)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	if !bytes.Equal(canonical, source) {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation token is not canonical JSON")
	}
	comparisonDigest, err := ParseDigest(wire.ComparisonDigest)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	comparisonBytes, err := json.Marshal(wire.Comparison)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	if DigestBytes(comparisonBytes) != comparisonDigest {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation token comparison digest mismatch")
	}
	if wire.Comparison.ChangedMergeUnits == nil {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation token changed_merge_units must be explicit")
	}
	workspaceID, err := NewID(wire.Comparison.WorkspaceID)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	active, err := ParseDigest(wire.Comparison.ActiveGeneration)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	candidate, err := ParseDigest(wire.Comparison.CandidateGeneration)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	journalHead, err := ParseDigest(wire.Comparison.JournalHead)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	stateDigest, err := ParseDigest(wire.Comparison.StateDigest)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	structuralDigest, err := ParseDigest(wire.Comparison.StructuralDigest)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	changed := make([]MergeUnitReference, 0, len(wire.Comparison.ChangedMergeUnits))
	for _, value := range wire.Comparison.ChangedMergeUnits {
		planID, err := NewID(value.PlanID)
		if err != nil {
			return ReconciliationPlan{}, err
		}
		unitID, err := NewID(value.MergeUnitID)
		if err != nil {
			return ReconciliationPlan{}, err
		}
		reference, _ := NewMergeUnitReference(planID, unitID)
		changed = append(changed, reference)
	}
	originalChanged := append([]MergeUnitReference(nil), changed...)
	changed, err = normalizeMergeUnitReferences(changed)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	if len(changed) != len(wire.Comparison.ChangedMergeUnits) {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation token changed merge units are invalid")
	}
	for index := range changed {
		if changed[index] != originalChanged[index] {
			return ReconciliationPlan{}, fmt.Errorf("reconciliation token changed merge units must be sorted")
		}
	}
	plan := ReconciliationPlan{
		workspaceID: workspaceID, activeGeneration: active, candidateGeneration: candidate,
		journalHead: journalHead, stateDigest: stateDigest, structuralDigest: structuralDigest,
		comparisonDigest: comparisonDigest, changedMergeUnits: changed,
		workspaceRevision: wire.Comparison.WorkspaceRevision, candidateRevision: wire.Comparison.CandidateRevision,
	}
	if active == candidate {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation token must change the active generation")
	}
	return plan, nil
}

func DryRunReconciliation(
	active, candidate EffectiveWorkspaceDefinition,
	snapshot JournalSnapshot,
	state ReconciliationState,
) (ReconciliationPlan, error) {
	if active.generation.IsZero() || candidate.generation.IsZero() || active.generation == candidate.generation {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation requires distinct active and candidate definitions")
	}
	if state.journalHead.digest != snapshot.head {
		return ReconciliationPlan{}, fmt.Errorf("reconciliation state is stale for journal head %s", snapshot.head)
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	if runtime.workspaceID != active.workspace.id || runtime.workspaceID != candidate.workspace.id || runtime.activeGeneration != active.generation {
		return ReconciliationPlan{}, fmt.Errorf("definitions do not match the active journal workspace and generation")
	}
	if !runtime.HasActivatableCandidate(candidate.generation) {
		return ReconciliationPlan{}, fmt.Errorf("candidate generation %s is not durably staged and pending activation", candidate.generation)
	}
	scheduler, err := RebuildSchedulerView(snapshot, active)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	completedAttempts := make(map[ID]struct{}, len(scheduler.Units))
	for _, unit := range scheduler.Units {
		if unit.Status != SchedulerUnitCompleted {
			continue
		}
		attemptID, parseErr := NewID(unit.AttemptID)
		if parseErr != nil {
			return ReconciliationPlan{}, fmt.Errorf("completed scheduler attempt: %w", parseErr)
		}
		completedAttempts[attemptID] = struct{}{}
	}
	if err := validateReconciliationSafety(state, runtime, completedAttempts); err != nil {
		return ReconciliationPlan{}, err
	}
	activeStructure, err := canonicalDefinitionStructure(active)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	candidateStructure, err := canonicalDefinitionStructure(candidate)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	activeStructuralDigest := DigestBytes(activeStructure)
	if activeStructuralDigest != DigestBytes(candidateStructure) {
		return ReconciliationPlan{}, fmt.Errorf("candidate contains structural changes and requires a new workspace")
	}
	activeUnits, activeReferences, err := definitionUnitFingerprints(active)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	candidateUnits, candidateReferences, err := definitionUnitFingerprints(candidate)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	if len(activeUnits) != len(candidateUnits) {
		return ReconciliationPlan{}, fmt.Errorf("candidate merge unit topology changed and requires a new workspace")
	}
	changed := make([]MergeUnitReference, 0)
	for key, activeFingerprint := range activeUnits {
		candidateFingerprint, exists := candidateUnits[key]
		if !exists || candidateFingerprint != activeFingerprint {
			changed = append(changed, activeReferences[key])
		}
	}
	for key := range candidateUnits {
		if _, exists := activeUnits[key]; !exists {
			return ReconciliationPlan{}, fmt.Errorf("candidate introduces merge unit %s and requires a new workspace", candidateReferences[key])
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].key() < changed[j].key() })
	unitStates := make(map[string]MergeUnitRuntimeState, len(state.mergeUnits))
	for _, unit := range state.mergeUnits {
		if _, exists := activeUnits[unit.reference.key()]; !exists {
			return ReconciliationPlan{}, fmt.Errorf("runtime state references unknown merge unit %s", unit.reference)
		}
		unitStates[unit.reference.key()] = unit
	}
	attemptedUnits := make(map[string]AttemptGenerationBinding, len(state.attempts)+len(runtime.attempts))
	for _, attempt := range state.attempts {
		if _, exists := activeUnits[attempt.mergeUnit.key()]; !exists {
			return ReconciliationPlan{}, fmt.Errorf("attempt %s references unknown merge unit %s", attempt.attemptID, attempt.mergeUnit)
		}
		attemptedUnits[attempt.mergeUnit.key()] = attempt
	}
	for _, attempt := range runtime.AttemptGenerationBindings() {
		if _, exists := activeUnits[attempt.mergeUnit.key()]; !exists {
			return ReconciliationPlan{}, fmt.Errorf("journal attempt %s references unknown merge unit %s", attempt.attemptID, attempt.mergeUnit)
		}
		attemptedUnits[attempt.mergeUnit.key()] = attempt
	}
	for _, reference := range changed {
		if unit, exists := unitStates[reference.key()]; exists && unit.disposition != MergeUnitFuture {
			return ReconciliationPlan{}, fmt.Errorf(
				"candidate changes retrospective merge unit %s in state %s and requires a new workspace",
				reference, unit.disposition,
			)
		}
		if attempt, exists := attemptedUnits[reference.key()]; exists {
			return ReconciliationPlan{}, fmt.Errorf(
				"candidate changes retrospective merge unit %s with attempt %s and requires a new workspace",
				reference, attempt.attemptID,
			)
		}
	}
	stateBytes, err := canonicalReconciliationState(state)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	stateDigest := DigestBytes(stateBytes)
	workspaceResource := WorkspaceJournalResource(runtime.workspaceID)
	candidateResource := GenerationJournalResource(candidate.generation)
	comparisonWire := reconciliationComparisonWire{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: runtime.workspaceID.String(),
		ActiveGeneration: active.generation.String(), CandidateGeneration: candidate.generation.String(),
		JournalHead: snapshot.head.String(), StateDigest: stateDigest.String(),
		StructuralDigest:  activeStructuralDigest.String(),
		WorkspaceRevision: snapshot.Revision(workspaceResource), CandidateRevision: snapshot.Revision(candidateResource),
		ChangedMergeUnits: make([]mergeUnitReferenceJSON, 0, len(changed)),
	}
	for _, reference := range changed {
		comparisonWire.ChangedMergeUnits = append(comparisonWire.ChangedMergeUnits, mergeUnitReferenceJSON{
			PlanID: reference.planID.String(), MergeUnitID: reference.mergeUnitID.String(),
		})
	}
	comparisonBytes, err := json.Marshal(comparisonWire)
	if err != nil {
		return ReconciliationPlan{}, err
	}
	return ReconciliationPlan{
		workspaceID: runtime.workspaceID, activeGeneration: active.generation,
		candidateGeneration: candidate.generation, journalHead: snapshot.head,
		stateDigest: stateDigest, structuralDigest: activeStructuralDigest,
		comparisonDigest: DigestBytes(comparisonBytes), changedMergeUnits: changed,
		workspaceRevision: comparisonWire.WorkspaceRevision, candidateRevision: comparisonWire.CandidateRevision,
	}, nil
}

func comparisonWireFromPlan(plan ReconciliationPlan) reconciliationComparisonWire {
	value := reconciliationComparisonWire{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: plan.workspaceID.String(),
		ActiveGeneration: plan.activeGeneration.String(), CandidateGeneration: plan.candidateGeneration.String(),
		JournalHead: plan.journalHead.String(), StateDigest: plan.stateDigest.String(),
		StructuralDigest: plan.structuralDigest.String(), WorkspaceRevision: plan.workspaceRevision,
		CandidateRevision: plan.candidateRevision,
		ChangedMergeUnits: make([]mergeUnitReferenceJSON, 0, len(plan.changedMergeUnits)),
	}
	for _, reference := range plan.changedMergeUnits {
		value.ChangedMergeUnits = append(value.ChangedMergeUnits, mergeUnitReferenceJSON{
			PlanID: reference.planID.String(), MergeUnitID: reference.mergeUnitID.String(),
		})
	}
	return value
}

func ReconciliationControlPlaneBinding(
	active EffectiveWorkspaceDefinition,
	candidate EffectiveWorkspaceDefinition,
	plan ReconciliationPlan,
) (ControlPlaneBinding, error) {
	if active.workspace.id.IsZero() || candidate.workspace.id.IsZero() ||
		active.workspace.id != candidate.workspace.id || active.workspace.id != plan.workspaceID ||
		active.generation != plan.activeGeneration || candidate.generation != plan.candidateGeneration ||
		active.workspace.repository != candidate.workspace.repository || active.workspace.remote != candidate.workspace.remote {
		return ControlPlaneBinding{}, fmt.Errorf("reconciliation receipt definition does not match the dry-run plan")
	}
	return NewControlPlaneBinding(ControlPlaneBindingOptions{
		Kind: ControlPlaneReceiptReconciliation, WorkspaceID: plan.workspaceID,
		Generation: plan.candidateGeneration, RequestDigest: plan.comparisonDigest,
		Repository: active.workspace.repository, Remote: active.workspace.remote,
	})
}

func ActivateCandidateGeneration(
	ctx context.Context,
	journal *WorkspaceJournal,
	store *GenerationStore,
	active EffectiveWorkspaceDefinition,
	candidate EffectiveWorkspaceDefinition,
	plan ReconciliationPlan,
	state ReconciliationState,
	receipt ControlPlaneReceiptV2,
	verifier ControlPlaneVerifierPort,
	occurredAt time.Time,
) (JournalRecord, error) {
	if journal == nil || store == nil || journal.workspaceDir != store.workspaceDir || verifier == nil || occurredAt.IsZero() {
		return JournalRecord{}, fmt.Errorf("candidate activation requires journal, store, verifier, and occurrence time")
	}
	if plan.workspaceID.IsZero() || plan.activeGeneration.IsZero() || plan.candidateGeneration.IsZero() || plan.comparisonDigest.IsZero() {
		return JournalRecord{}, fmt.Errorf("candidate activation requires a complete dry-run plan")
	}
	if receipt.ReceiptDigest().IsZero() {
		return JournalRecord{}, fmt.Errorf("candidate activation requires an owner receipt")
	}
	snapshot, err := journal.ReadSnapshot()
	if err != nil {
		return JournalRecord{}, err
	}
	if snapshot.head != plan.journalHead {
		return JournalRecord{}, fmt.Errorf("stale reconciliation token: journal head changed")
	}
	stateBytes, err := canonicalReconciliationState(state)
	if err != nil {
		return JournalRecord{}, err
	}
	if state.journalHead.digest != snapshot.head || DigestBytes(stateBytes) != plan.stateDigest {
		return JournalRecord{}, fmt.Errorf("stale reconciliation token: runtime safety state changed")
	}
	runtime, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return JournalRecord{}, err
	}
	if runtime.workspaceID != plan.workspaceID || runtime.activeGeneration != plan.activeGeneration || !runtime.HasActivatableCandidate(plan.candidateGeneration) {
		return JournalRecord{}, fmt.Errorf("stale reconciliation token: generation state changed")
	}
	authorization, err := RebuildAuthorizationRuntime(snapshot, active)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("rebuild authorization before generation activation: %w", err)
	}
	if len(authorization.state.obligations) != 0 {
		return JournalRecord{}, fmt.Errorf("generation activation is blocked by dispatched effects awaiting reconciliation")
	}
	recomputed, err := DryRunReconciliation(active, candidate, snapshot, state)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("revalidate reconciliation plan: %w", err)
	}
	if recomputed.comparisonDigest != plan.comparisonDigest {
		return JournalRecord{}, fmt.Errorf("reconciliation token does not match the current definition comparison")
	}
	storedActive, err := store.Load(plan.activeGeneration)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("load active generation: %w", err)
	}
	if storedActive.workspaceID != plan.workspaceID {
		return JournalRecord{}, fmt.Errorf("active generation belongs to workspace %s", storedActive.workspaceID)
	}
	storedCandidate, err := store.Load(plan.candidateGeneration)
	if err != nil {
		return JournalRecord{}, fmt.Errorf("load candidate generation: %w", err)
	}
	if storedCandidate.workspaceID != plan.workspaceID {
		return JournalRecord{}, fmt.Errorf("candidate generation belongs to workspace %s", storedCandidate.workspaceID)
	}
	binding, err := ReconciliationControlPlaneBinding(active, candidate, plan)
	if err != nil {
		return JournalRecord{}, err
	}
	verification, err := NewControlPlaneVerification(binding)
	if err != nil {
		return JournalRecord{}, err
	}
	if err := verifier.Verify(ctx, verification, receipt); err != nil {
		return JournalRecord{}, fmt.Errorf("verify owner activation receipt: %w", err)
	}
	event, err := NewGenerationActivatedJournalEvent(
		plan.workspaceID, plan.activeGeneration, plan.candidateGeneration,
		plan.comparisonDigest, receipt.ReceiptDigest(), state.history, plan.changedMergeUnits,
	)
	if err != nil {
		return JournalRecord{}, err
	}
	workspaceResource := WorkspaceJournalResource(plan.workspaceID)
	candidateResource := GenerationJournalResource(plan.candidateGeneration)
	workspaceRevision, _ := NewJournalResourceRevision(workspaceResource, plan.workspaceRevision)
	candidateRevision, _ := NewJournalResourceRevision(candidateResource, plan.candidateRevision)
	appendRequest, err := newPrivilegedJournalAppend(
		event, occurredAt,
		[]JournalResourceRevision{workspaceRevision, candidateRevision},
		[]JournalResource{workspaceResource, candidateResource},
	)
	if err != nil {
		return JournalRecord{}, err
	}
	return journal.AppendIfHead(appendRequest, plan.journalHead)
}

func normalizeReconciliationState(state *ReconciliationState) error {
	seenUnits := make(map[string]struct{}, len(state.mergeUnits))
	for _, unit := range state.mergeUnits {
		if unit.reference.planID.IsZero() || unit.reference.mergeUnitID.IsZero() || !unit.disposition.valid() ||
			(unit.disposition != MergeUnitFuture && unit.generation.IsZero()) {
			return fmt.Errorf("invalid merge unit runtime state")
		}
		if _, exists := seenUnits[unit.reference.key()]; exists {
			return fmt.Errorf("duplicate merge unit runtime state %s", unit.reference)
		}
		seenUnits[unit.reference.key()] = struct{}{}
	}
	sort.Slice(state.mergeUnits, func(i, j int) bool { return state.mergeUnits[i].reference.key() < state.mergeUnits[j].reference.key() })
	seenAttempts := make(map[string]struct{}, len(state.attempts))
	for _, attempt := range state.attempts {
		if attempt.attemptID.IsZero() || attempt.mergeUnit.planID.IsZero() || attempt.mergeUnit.mergeUnitID.IsZero() ||
			attempt.generation.IsZero() || !attempt.phase.valid() {
			return fmt.Errorf("invalid attempt generation binding")
		}
		if _, exists := seenAttempts[attempt.attemptID.String()]; exists {
			return fmt.Errorf("duplicate attempt %s", attempt.attemptID)
		}
		seenAttempts[attempt.attemptID.String()] = struct{}{}
	}
	sort.Slice(state.attempts, func(i, j int) bool {
		return state.attempts[i].attemptID.String() < state.attempts[j].attemptID.String()
	})
	seenIntents := make(map[string]struct{}, len(state.intents))
	for _, intent := range state.intents {
		if intent.intentID.IsZero() || intent.generation.IsZero() {
			return fmt.Errorf("invalid provider intent runtime state")
		}
		if _, exists := seenIntents[intent.intentID.String()]; exists {
			return fmt.Errorf("duplicate provider intent %s", intent.intentID)
		}
		seenIntents[intent.intentID.String()] = struct{}{}
	}
	sort.Slice(state.intents, func(i, j int) bool { return state.intents[i].intentID.String() < state.intents[j].intentID.String() })
	seenQueue := make(map[string]struct{}, len(state.queue))
	for _, entry := range state.queue {
		if entry.entryID.IsZero() || entry.generation.IsZero() {
			return fmt.Errorf("invalid queue entry runtime state")
		}
		if _, exists := seenQueue[entry.entryID.String()]; exists {
			return fmt.Errorf("duplicate queue entry %s", entry.entryID)
		}
		seenQueue[entry.entryID.String()] = struct{}{}
	}
	sort.Slice(state.queue, func(i, j int) bool { return state.queue[i].entryID.String() < state.queue[j].entryID.String() })
	return nil
}

func validateReconciliationSafety(
	state ReconciliationState,
	runtime WorkspaceRuntimeProjection,
	completedAttempts map[ID]struct{},
) error {
	knownGenerations := runtime.generationHistory
	for _, unit := range state.mergeUnits {
		if unit.disposition != MergeUnitFuture && !containsDigest(knownGenerations, unit.generation) {
			return fmt.Errorf("merge unit %s is bound to unknown generation %s", unit.reference, unit.generation)
		}
		switch unit.disposition {
		case MergeUnitReserved, MergeUnitMaterializing, MergeUnitActive, MergeUnitPaused, MergeUnitReviewExhausted:
			return fmt.Errorf("reconciliation is blocked by merge unit %s in state %s", unit.reference, unit.disposition)
		}
	}
	for _, attempt := range state.attempts {
		if !containsDigest(knownGenerations, attempt.generation) {
			return fmt.Errorf("attempt %s is bound to unknown generation %s", attempt.attemptID, attempt.generation)
		}
		if attempt.phase.nonterminal() {
			return fmt.Errorf("reconciliation is blocked by nonterminal attempt %s in state %s", attempt.attemptID, attempt.phase)
		}
	}
	for _, attempt := range runtime.AttemptGenerationBindings() {
		if !containsDigest(knownGenerations, attempt.generation) {
			return fmt.Errorf("journal attempt %s is bound to unknown generation %s", attempt.attemptID, attempt.generation)
		}
		if attempt.phase.nonterminal() {
			if _, completed := completedAttempts[attempt.attemptID]; completed {
				continue
			}
			return fmt.Errorf("reconciliation is blocked by journal-projected nonterminal attempt %s in state %s", attempt.attemptID, attempt.phase)
		}
	}
	for _, intent := range state.intents {
		if !containsDigest(knownGenerations, intent.generation) {
			return fmt.Errorf("provider intent %s is bound to unknown generation %s", intent.intentID, intent.generation)
		}
		if !intent.resolved {
			return fmt.Errorf("reconciliation is blocked by unresolved provider intent %s", intent.intentID)
		}
	}
	for _, entry := range state.queue {
		if !containsDigest(knownGenerations, entry.generation) {
			return fmt.Errorf("queue entry %s is bound to unknown generation %s", entry.entryID, entry.generation)
		}
		if !entry.resolved {
			return fmt.Errorf("reconciliation is blocked by unresolved queue entry %s", entry.entryID)
		}
	}
	return nil
}

func canonicalReconciliationState(state ReconciliationState) ([]byte, error) {
	type unitJSON struct {
		PlanID      string                      `json:"plan_id"`
		MergeUnitID string                      `json:"merge_unit_id"`
		Disposition MergeUnitRuntimeDisposition `json:"disposition"`
		Generation  string                      `json:"generation"`
	}
	type attemptJSON struct {
		AttemptID   string              `json:"attempt_id"`
		PlanID      string              `json:"plan_id"`
		MergeUnitID string              `json:"merge_unit_id"`
		Generation  string              `json:"generation"`
		Phase       AttemptRuntimePhase `json:"phase"`
	}
	type externalJSON struct {
		ID         string `json:"id"`
		Generation string `json:"generation"`
		Resolved   bool   `json:"resolved"`
	}
	type stateJSON struct {
		SchemaVersion   int            `json:"schema_version"`
		JournalHead     string         `json:"journal_head"`
		MergeUnits      []unitJSON     `json:"merge_units"`
		Attempts        []attemptJSON  `json:"attempts"`
		Intents         []externalJSON `json:"provider_intents"`
		Queue           []externalJSON `json:"queue_entries"`
		BudgetHistory   string         `json:"budget_history"`
		ApprovalHistory string         `json:"approval_history"`
		EvidenceHistory string         `json:"evidence_history"`
	}
	value := stateJSON{
		SchemaVersion: JournalSchemaVersion, JournalHead: state.journalHead.digest.String(),
		MergeUnits: make([]unitJSON, 0, len(state.mergeUnits)), Attempts: make([]attemptJSON, 0, len(state.attempts)),
		Intents: make([]externalJSON, 0, len(state.intents)), Queue: make([]externalJSON, 0, len(state.queue)),
		BudgetHistory: state.history.budgets.String(), ApprovalHistory: state.history.approvals.String(),
		EvidenceHistory: state.history.evidence.String(),
	}
	for _, unit := range state.mergeUnits {
		value.MergeUnits = append(value.MergeUnits, unitJSON{
			PlanID: unit.reference.planID.String(), MergeUnitID: unit.reference.mergeUnitID.String(),
			Disposition: unit.disposition, Generation: unit.generation.String(),
		})
	}
	for _, attempt := range state.attempts {
		value.Attempts = append(value.Attempts, attemptJSON{
			AttemptID: attempt.attemptID.String(), PlanID: attempt.mergeUnit.planID.String(),
			MergeUnitID: attempt.mergeUnit.mergeUnitID.String(), Generation: attempt.generation.String(), Phase: attempt.phase,
		})
	}
	for _, intent := range state.intents {
		value.Intents = append(value.Intents, externalJSON{ID: intent.intentID.String(), Generation: intent.generation.String(), Resolved: intent.resolved})
	}
	for _, entry := range state.queue {
		value.Queue = append(value.Queue, externalJSON{ID: entry.entryID.String(), Generation: entry.generation.String(), Resolved: entry.resolved})
	}
	return json.Marshal(value)
}

func canonicalDefinitionStructure(definition EffectiveWorkspaceDefinition) ([]byte, error) {
	type authorityJSON struct {
		ID       string        `json:"id"`
		Kind     AuthorityKind `json:"kind"`
		Location string        `json:"location"`
	}
	type storyJSON struct {
		ID           string   `json:"id"`
		Dependencies []string `json:"dependencies"`
	}
	type unitJSON struct {
		ID       string   `json:"id"`
		StoryIDs []string `json:"story_ids"`
	}
	type planJSON struct {
		ID      string      `json:"id"`
		Source  string      `json:"source"`
		Stories []storyJSON `json:"stories"`
		Units   []unitJSON  `json:"merge_units"`
	}
	type structureJSON struct {
		SchemaVersion   int                            `json:"schema_version"`
		WorkspaceID     string                         `json:"workspace_id"`
		RepositoryRoot  string                         `json:"repository_root"`
		Repository      string                         `json:"repository"`
		ProviderKind    string                         `json:"provider_kind"`
		ProviderRepo    string                         `json:"provider_repository"`
		BaseRef         string                         `json:"base_ref"`
		Remote          string                         `json:"remote"`
		ExecutionConfig string                         `json:"execution_config"`
		Plans           []planJSON                     `json:"plans"`
		Dependencies    []canonicalWorkspaceDependency `json:"dependencies"`
		Authorities     []authorityJSON                `json:"authority_sources"`
	}
	planSources := make(map[string]string, len(definition.workspace.plans))
	for _, reference := range definition.workspace.plans {
		planSources[reference.id.String()] = reference.source
	}
	value := structureJSON{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: definition.workspace.id.String(),
		RepositoryRoot: definition.workspace.repositoryRoot, Repository: definition.workspace.repository.String(),
		ProviderKind: definition.workspace.provider.kind.String(), ProviderRepo: definition.workspace.provider.repository,
		BaseRef: definition.workspace.baseRef, Remote: definition.workspace.remote,
		ExecutionConfig: definition.workspace.executionConfig,
		Plans:           make([]planJSON, 0, len(definition.plans)),
		Dependencies:    make([]canonicalWorkspaceDependency, 0, len(definition.workspace.dependencies)),
		Authorities:     make([]authorityJSON, 0, len(definition.workspace.authoritySources)),
	}
	for _, plan := range definition.plans {
		item := planJSON{ID: plan.id.String(), Source: planSources[plan.id.String()]}
		for _, story := range plan.stories {
			dependencies := make([]string, 0, len(story.dependencies))
			for _, dependency := range story.dependencies {
				dependencies = append(dependencies, dependency.String())
			}
			item.Stories = append(item.Stories, storyJSON{ID: story.id.String(), Dependencies: dependencies})
		}
		for _, unit := range plan.mergeUnits {
			storyIDs := make([]string, 0, len(unit.storyIDs))
			for _, storyID := range unit.storyIDs {
				storyIDs = append(storyIDs, storyID.String())
			}
			item.Units = append(item.Units, unitJSON{ID: unit.id.String(), StoryIDs: storyIDs})
		}
		value.Plans = append(value.Plans, item)
	}
	for _, dependency := range definition.workspace.dependencies {
		value.Dependencies = append(value.Dependencies, canonicalWorkspaceDependency{
			Before: canonicalMergeUnitReference{PlanID: dependency.before.planID.String(), MergeUnitID: dependency.before.mergeUnitID.String()},
			After:  canonicalMergeUnitReference{PlanID: dependency.after.planID.String(), MergeUnitID: dependency.after.mergeUnitID.String()},
		})
	}
	for _, authority := range definition.workspace.authoritySources {
		value.Authorities = append(value.Authorities, authorityJSON{
			ID: authority.id.String(), Kind: authority.kind, Location: authority.location,
		})
	}
	return json.Marshal(value)
}

func definitionUnitFingerprints(definition EffectiveWorkspaceDefinition) (map[string]Digest, map[string]MergeUnitReference, error) {
	profiles := make(map[string]ExecutionProfile, len(definition.execution.profiles))
	for _, profile := range definition.execution.profiles {
		profiles[profile.id.String()] = profile
	}
	unitExecution := make(map[string]UnitExecution, len(definition.execution.mergeUnits))
	for _, unit := range definition.execution.mergeUnits {
		unitExecution[unit.planID.String()+"\x00"+unit.mergeUnitID.String()] = unit
	}
	authorities := make([]json.RawMessage, 0, len(definition.authorities))
	for _, authority := range definition.authorities {
		canonical, err := canonicalAuthorityBytes(authority)
		if err != nil {
			return nil, nil, err
		}
		authorities = append(authorities, json.RawMessage(canonical))
	}
	fingerprints := make(map[string]Digest)
	references := make(map[string]MergeUnitReference)
	for _, plan := range definition.plans {
		stories := make(map[string]Story, len(plan.stories))
		for _, story := range plan.stories {
			stories[story.id.String()] = story
		}
		for _, unit := range plan.mergeUnits {
			reference := MergeUnitReference{planID: plan.id, mergeUnitID: unit.id}
			execution, exists := unitExecution[reference.key()]
			if !exists {
				return nil, nil, fmt.Errorf("missing execution policy for %s", reference)
			}
			profile, exists := profiles[execution.profileID.String()]
			if !exists {
				return nil, nil, fmt.Errorf("missing execution profile %s", execution.profileID)
			}
			type unitFingerprintJSON struct {
				PlanID        string                 `json:"plan_id"`
				PlanTitle     string                 `json:"plan_title"`
				MergeUnitID   string                 `json:"merge_unit_id"`
				MergeUnitName string                 `json:"merge_unit_name"`
				Stories       []canonicalStory       `json:"stories"`
				BasePolicy    canonicalPolicy        `json:"base_policy"`
				Profile       canonicalProfile       `json:"profile"`
				Execution     canonicalUnitExecution `json:"execution"`
				Authorities   []json.RawMessage      `json:"authorities"`
			}
			value := unitFingerprintJSON{
				PlanID: plan.id.String(), PlanTitle: plan.title, MergeUnitID: unit.id.String(), MergeUnitName: unit.name,
				Stories: make([]canonicalStory, 0, len(unit.storyIDs)), BasePolicy: canonicalizePolicy(definition.execution.policy),
				Profile: canonicalProfile{ID: profile.id.String(), Runner: profile.runner.String(), Policy: canonicalizePolicy(profile.policy)},
				Execution: canonicalUnitExecution{
					PlanID: plan.id.String(), MergeUnitID: unit.id.String(), Profile: execution.profileID.String(),
					Policy: canonicalizePolicy(execution.policy), BoundaryMode: execution.boundary.mode,
					SerialSegment: execution.boundary.serialSegment.String(),
				},
				Authorities: append([]json.RawMessage(nil), authorities...),
			}
			for _, storyID := range unit.storyIDs {
				story := stories[storyID.String()]
				canonical := canonicalStory{
					ID: story.id.String(), Summary: story.summary,
					Acceptance:     append([]string(nil), story.acceptance...),
					Implementation: append([]string(nil), story.implementation...),
					Testing:        append([]string(nil), story.testing...),
				}
				for _, dependency := range story.dependencies {
					canonical.Dependencies = append(canonical.Dependencies, dependency.String())
				}
				value.Stories = append(value.Stories, canonical)
			}
			canonical, err := json.Marshal(value)
			if err != nil {
				return nil, nil, err
			}
			fingerprints[reference.key()] = DigestBytes(canonical)
			references[reference.key()] = reference
		}
	}
	return fingerprints, references, nil
}
