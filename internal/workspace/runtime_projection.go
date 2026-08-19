package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	return verifyProjectionConformance(
		first, second, canonical, activeGeneration, expectedGeneration,
	)
}

func verifyProjectionConformance[State any](
	first State,
	second State,
	canonical func(State) ([]byte, error),
	activeGeneration func(State) Digest,
	expectedGeneration Digest,
) (Digest, error) {
	if canonical == nil || activeGeneration == nil || expectedGeneration.IsZero() {
		return Digest{}, fmt.Errorf("replay conformance requires constructors, reducer, canonicalizer, and active generation")
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

type RuntimeLocalTargetProjection struct {
	binding       LocalTargetBinding
	intentDigest  Digest
	intentRecord  uint64
	createdHead   GitObjectID
	createdRecord uint64
	headRecord    uint64
}

func (projection RuntimeLocalTargetProjection) Binding() LocalTargetBinding {
	return projection.binding
}
func (projection RuntimeLocalTargetProjection) IntentDigest() Digest {
	return projection.intentDigest
}
func (projection RuntimeLocalTargetProjection) IntentRecord() uint64 {
	return projection.intentRecord
}
func (projection RuntimeLocalTargetProjection) Created() bool {
	return projection.createdRecord != 0
}
func (projection RuntimeLocalTargetProjection) CreatedHead() GitObjectID {
	return projection.createdHead
}
func (projection RuntimeLocalTargetProjection) CreatedRecord() uint64 {
	return projection.createdRecord
}
func (projection RuntimeLocalTargetProjection) HeadRecord() uint64 {
	return projection.headRecord
}
func (projection RuntimeLocalTargetProjection) IsZero() bool {
	return projection.binding.IsZero()
}

type RuntimeWorkspaceCompletionProjection struct {
	featureRef   string
	featureHead  GitObjectID
	reportDigest Digest
	record       uint64
	eventDigest  Digest
}

func (completion RuntimeWorkspaceCompletionProjection) FeatureRef() string {
	return completion.featureRef
}
func (completion RuntimeWorkspaceCompletionProjection) FeatureHead() GitObjectID {
	return completion.featureHead
}
func (completion RuntimeWorkspaceCompletionProjection) ReportDigest() Digest {
	return completion.reportDigest
}
func (completion RuntimeWorkspaceCompletionProjection) Record() uint64 {
	return completion.record
}
func (completion RuntimeWorkspaceCompletionProjection) EventDigest() Digest {
	return completion.eventDigest
}

type WorkspaceRuntimeProjection struct {
	workspaceID                  ID
	activeGeneration             Digest
	planCheckpoint               Digest
	planCheckpointArtifactDigest Digest
	worktreeRoot                 WorkspaceWorktreeRootBinding
	localTarget                  RuntimeLocalTargetProjection
	recoveries                   []RuntimeRecoveryProjection
	attempts                     []RuntimeAttemptProjection
	completion                   *RuntimeWorkspaceCompletionProjection
}

func (projection WorkspaceRuntimeProjection) WorkspaceID() ID { return projection.workspaceID }
func (projection WorkspaceRuntimeProjection) ActiveGeneration() Digest {
	return projection.activeGeneration
}
func (projection WorkspaceRuntimeProjection) PlanCheckpoint() Digest {
	return projection.planCheckpoint
}
func (projection WorkspaceRuntimeProjection) PlanCheckpointArtifactDigest() Digest {
	return projection.planCheckpointArtifactDigest
}
func (projection WorkspaceRuntimeProjection) WorktreeRoot() WorkspaceWorktreeRootBinding {
	return projection.worktreeRoot
}
func (projection WorkspaceRuntimeProjection) LocalTarget() (
	RuntimeLocalTargetProjection,
	bool,
) {
	if projection.localTarget.IsZero() {
		return RuntimeLocalTargetProjection{}, false
	}
	return projection.localTarget, true
}
func (projection WorkspaceRuntimeProjection) Recoveries() []RuntimeRecoveryProjection {
	return append([]RuntimeRecoveryProjection(nil), projection.recoveries...)
}
func (projection WorkspaceRuntimeProjection) Completion() (
	RuntimeWorkspaceCompletionProjection,
	bool,
) {
	if projection.completion == nil {
		return RuntimeWorkspaceCompletionProjection{}, false
	}
	return *projection.completion, true
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
	if current.completion != nil {
		if _, recovery := record.event.(JournalTailRecoveredEvent); !recovery {
			return WorkspaceRuntimeProjection{}, fmt.Errorf(
				"workspace completion is final for local workflow mutations",
			)
		}
	}
	if !current.activeGeneration.IsZero() {
		target, hasTarget := current.LocalTarget()
		ready := hasTarget && target.Created()
		switch record.event.(type) {
		case FeatureRefCreationIntendedJournalEvent,
			FeatureRefCreatedJournalEvent,
			JournalTailRecoveredEvent:
			// Initialization and journal-tail recovery are the only transitions
			// admitted before durable feature-ref completion.
		default:
			if !ready {
				return WorkspaceRuntimeProjection{}, fmt.Errorf(
					"workspace runtime is not ready until feature_ref_created is durable",
				)
			}
		}
	}
	if !isIntegrationJournalEvent(record.event) {
		for _, attempt := range current.attempts {
			if attempt.integration != nil &&
				journalEventTargetsAttempt(record.event, attempt.attemptID) {
				return WorkspaceRuntimeProjection{}, fmt.Errorf(
					"attempt %s is frozen after durable integration intent",
					attempt.attemptID,
				)
			}
		}
	}
	switch event := record.event.(type) {
	case WorkspaceInitializedJournalEvent:
		if !current.activeGeneration.IsZero() || !current.localTarget.IsZero() ||
			len(current.attempts) != 0 {
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
		next.planCheckpointArtifactDigest = event.planCheckpointArtifactDigest
		next.worktreeRoot = event.worktreeRoot
	case FeatureRefCreationIntendedJournalEvent:
		if current.workspaceID != event.workspaceID ||
			current.activeGeneration != event.generation {
			return WorkspaceRuntimeProjection{}, fmt.Errorf(
				"feature-ref creation intent has stale workspace bindings",
			)
		}
		if !current.localTarget.IsZero() {
			return WorkspaceRuntimeProjection{}, fmt.Errorf(
				"feature-ref creation intent is already recorded",
			)
		}
		next.localTarget = RuntimeLocalTargetProjection{
			binding: event.binding, intentDigest: event.intentDigest,
			intentRecord: record.sequence,
		}
	case FeatureRefCreatedJournalEvent:
		if current.workspaceID != event.workspaceID ||
			current.activeGeneration != event.generation ||
			current.localTarget.IsZero() ||
			current.localTarget.createdRecord != 0 {
			return WorkspaceRuntimeProjection{}, fmt.Errorf(
				"feature-ref creation completion has stale or duplicate workspace bindings",
			)
		}
		target := current.localTarget
		if target.intentDigest != event.intentDigest ||
			target.binding.featureRef != event.featureRef ||
			target.binding.baseCommit != event.head {
			return WorkspaceRuntimeProjection{}, fmt.Errorf(
				"feature-ref creation completion does not match its exact intent",
			)
		}
		next.localTarget.createdHead = event.head
		next.localTarget.createdRecord = record.sequence
		next.localTarget.headRecord = record.sequence
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
	case MergeUnitIntegrationIntendedJournalEvent,
		MergeUnitIntegratedJournalEvent:
		if err := reduceIntegrationRuntime(
			current, &next, record,
		); err != nil {
			return WorkspaceRuntimeProjection{}, err
		}
	case WorkspaceCompletedJournalEvent:
		if err := reduceCompletionRuntime(
			current, &next, record, event,
		); err != nil {
			return WorkspaceRuntimeProjection{}, err
		}
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
		} else if isReviewJournalEvent(record.event) {
			// Review has its own definition-aware projection. The core attempt
			// runtime remains the Git and review-fix source of truth.
		} else {
			return WorkspaceRuntimeProjection{}, fmt.Errorf("unsupported runtime event %T", record.event)
		}
	}
	return next, nil
}

func journalEventTargetsAttempt(event WorkspaceJournalEvent, attemptID ID) bool {
	switch event := event.(type) {
	case ReviewHeadAdoptedJournalEvent:
		return event.attemptID == attemptID
	case ReviewInvocationReservedJournalEvent:
		return event.attemptID == attemptID
	case ReviewInvocationFailedJournalEvent:
		return event.attemptID == attemptID
	case ReviewFindingFixReservedJournalEvent:
		return event.attemptID == attemptID
	}
	bound, ok := event.(interface{ AttemptID() ID })
	return ok && bound.AttemptID() == attemptID
}

func requireReadyLocalTarget(runtime WorkspaceRuntimeProjection) error {
	target, ok := runtime.LocalTarget()
	if !ok || !target.Created() {
		return fmt.Errorf(
			"workspace runtime is not ready until feature_ref_created is durable",
		)
	}
	return nil
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
	if event.head != event.priorHead &&
		(attempt.commitProtocol != nil || attempt.reviewFixes != nil) {
		return fmt.Errorf("review head adoption is only allowed without durable commit protocols")
	}
	if event.head == event.priorHead {
		if attempt.commitProtocol != nil &&
			(attempt.commitProtocol.phase != CommitProtocolComplete ||
				attempt.commitProtocol.Head() != event.head) {
			return fmt.Errorf(
				"same-head adoption does not match the completed commit protocol",
			)
		}
		if attempt.reviewFixes != nil &&
			(!attempt.reviewFixes.Quiescent() ||
				attempt.reviewFixes.Head() != event.head) {
			return fmt.Errorf(
				"same-head adoption does not match completed review fixes",
			)
		}
	}
	updated := &next.attempts[index]
	updated.verifiedHead = event.head
	updated.inspectionDigest = event.snapshotDigest
	return nil
}

func cloneWorkspaceRuntime(source WorkspaceRuntimeProjection) WorkspaceRuntimeProjection {
	result := source
	result.recoveries = append([]RuntimeRecoveryProjection(nil), source.recoveries...)
	result.attempts = cloneRuntimeAttempts(source.attempts)
	if source.completion != nil {
		completion := *source.completion
		result.completion = &completion
	}
	return result
}

func canonicalWorkspaceRuntime(projection WorkspaceRuntimeProjection) ([]byte, error) {
	type recoveryJSON struct {
		Record        uint64 `json:"record"`
		Generation    string `json:"generation"`
		DiscardOffset int64  `json:"discard_offset"`
		DiscardSize   int64  `json:"discard_size"`
		DiscardDigest string `json:"discard_digest"`
		ResultingHead string `json:"resulting_head"`
	}
	type localTargetJSON struct {
		Binding       localTargetBindingWire `json:"binding"`
		BindingDigest string                 `json:"binding_digest"`
		IntentDigest  string                 `json:"intent_digest"`
		IntentRecord  uint64                 `json:"intent_record"`
		Created       bool                   `json:"created"`
		CreatedHead   string                 `json:"created_head,omitempty"`
		CreatedRecord uint64                 `json:"created_record,omitempty"`
		HeadRecord    uint64                 `json:"head_record,omitempty"`
	}
	type completionJSON struct {
		FeatureRef   string `json:"feature_ref"`
		FeatureHead  string `json:"feature_head"`
		ReportDigest string `json:"report_digest"`
		Record       uint64 `json:"record"`
		EventDigest  string `json:"event_digest"`
	}
	type runtimeJSON struct {
		SchemaVersion                int               `json:"schema_version"`
		WorkspaceID                  string            `json:"workspace_id"`
		ActiveGeneration             string            `json:"active_generation"`
		PlanCheckpoint               string            `json:"plan_checkpoint,omitempty"`
		PlanCheckpointArtifactDigest string            `json:"plan_checkpoint_artifact_digest,omitempty"`
		WorktreeRoot                 string            `json:"worktree_root"`
		LocalTarget                  *localTargetJSON  `json:"local_target,omitempty"`
		Recoveries                   []recoveryJSON    `json:"recoveries"`
		Attempts                     []json.RawMessage `json:"attempts"`
		Completion                   *completionJSON   `json:"completion,omitempty"`
	}
	value := runtimeJSON{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: projection.workspaceID.String(),
		ActiveGeneration:             projection.activeGeneration.String(),
		PlanCheckpoint:               projection.planCheckpoint.String(),
		PlanCheckpointArtifactDigest: projection.planCheckpointArtifactDigest.String(),
		WorktreeRoot:                 projection.worktreeRoot.Path(),
		Recoveries:                   make([]recoveryJSON, 0, len(projection.recoveries)),
		Attempts:                     make([]json.RawMessage, 0, len(projection.attempts)),
	}
	if !projection.localTarget.IsZero() {
		value.LocalTarget = &localTargetJSON{
			Binding: localTargetBindingToWire(
				projection.localTarget.binding,
			),
			BindingDigest: projection.localTarget.binding.digest.String(),
			IntentDigest:  projection.localTarget.intentDigest.String(),
			IntentRecord:  projection.localTarget.intentRecord,
			Created:       projection.localTarget.createdRecord != 0,
			CreatedHead:   projection.localTarget.createdHead.String(),
			CreatedRecord: projection.localTarget.createdRecord,
			HeadRecord:    projection.localTarget.headRecord,
		}
	}
	if projection.completion != nil {
		value.Completion = &completionJSON{
			FeatureRef:   projection.completion.featureRef,
			FeatureHead:  projection.completion.featureHead.String(),
			ReportDigest: projection.completion.reportDigest.String(),
			Record:       projection.completion.record,
			EventDigest:  projection.completion.eventDigest.String(),
		}
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
