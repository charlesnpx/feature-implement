package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

type SchedulerUnitStatus string

const (
	SchedulerUnitBlocked         SchedulerUnitStatus = "blocked"
	SchedulerUnitReady           SchedulerUnitStatus = "ready"
	SchedulerUnitReserved        SchedulerUnitStatus = "reserved"
	SchedulerUnitMaterializing   SchedulerUnitStatus = "materializing"
	SchedulerUnitActive          SchedulerUnitStatus = "active"
	SchedulerUnitPaused          SchedulerUnitStatus = "paused"
	SchedulerUnitReviewExhausted SchedulerUnitStatus = "review_exhausted"
	SchedulerUnitCompleted       SchedulerUnitStatus = "completed"
)

type SchedulerUnitView struct {
	PlanID              string                  `json:"plan_id"`
	MergeUnitID         string                  `json:"merge_unit_id"`
	Status              SchedulerUnitStatus     `json:"status"`
	Generation          string                  `json:"generation"`
	LifecycleGeneration string                  `json:"lifecycle_generation,omitempty"`
	Dependencies        []string                `json:"dependencies"`
	Blockers            []string                `json:"blockers"`
	AttemptID           string                  `json:"attempt_id,omitempty"`
	AttemptNumber       uint64                  `json:"attempt_number,omitempty"`
	Branch              string                  `json:"branch,omitempty"`
	Worktree            string                  `json:"worktree,omitempty"`
	Head                string                  `json:"head,omitempty"`
	CompletionReceipt   string                  `json:"completion_receipt,omitempty"`
	BoundaryPending     bool                    `json:"boundary_pending"`
	BoundaryReason      string                  `json:"boundary_reason,omitempty"`
	PendingDirectives   []BoundaryDirectiveView `json:"pending_directives"`
}

type BoundaryDirectiveView struct {
	Kind            string   `json:"kind"`
	WorkspaceID     string   `json:"workspace_id"`
	Generation      string   `json:"generation"`
	AttemptID       string   `json:"attempt_id"`
	BoundaryID      string   `json:"boundary_id"`
	GoalID          string   `json:"goal_id"`
	GoalScope       string   `json:"goal_scope"`
	Head            string   `json:"head"`
	DirectiveDigest string   `json:"directive_digest"`
	IdempotencyKey  string   `json:"idempotency_key,omitempty"`
	Choices         []string `json:"choices,omitempty"`
}

type SchedulerView struct {
	SchemaVersion    int                 `json:"schema_version"`
	WorkspaceID      string              `json:"workspace_id"`
	ActiveGeneration string              `json:"active_generation"`
	JournalHead      string              `json:"journal_head"`
	Units            []SchedulerUnitView `json:"units"`
}

type GateStatus string

const (
	GatePending GateStatus = "pending"
	GatePassed  GateStatus = "passed"
	GateFailed  GateStatus = "failed"
)

type GateCheckView struct {
	Name       string     `json:"name"`
	Status     GateStatus `json:"status"`
	Generation string     `json:"generation"`
	Reason     string     `json:"reason"`
}

type UnitGateView struct {
	PlanID      string          `json:"plan_id"`
	MergeUnitID string          `json:"merge_unit_id"`
	AttemptID   string          `json:"attempt_id,omitempty"`
	Checks      []GateCheckView `json:"checks"`
	MergeReady  bool            `json:"merge_ready"`
}

type GateView struct {
	SchemaVersion    int            `json:"schema_version"`
	WorkspaceID      string         `json:"workspace_id"`
	ActiveGeneration string         `json:"active_generation"`
	JournalHead      string         `json:"journal_head"`
	Units            []UnitGateView `json:"units"`
}

type MergeQueueEntryView struct {
	Position    int    `json:"position"`
	PlanID      string `json:"plan_id"`
	MergeUnitID string `json:"merge_unit_id"`
	AttemptID   string `json:"attempt_id"`
	Generation  string `json:"generation"`
	Head        string `json:"head"`
}

type ProviderQueueEntryView struct {
	EntryID    string `json:"entry_id"`
	Generation string `json:"generation"`
	Resolved   bool   `json:"resolved"`
}

type MergeQueueView struct {
	SchemaVersion    int                      `json:"schema_version"`
	WorkspaceID      string                   `json:"workspace_id"`
	ActiveGeneration string                   `json:"active_generation"`
	JournalHead      string                   `json:"journal_head"`
	Ready            []MergeQueueEntryView    `json:"ready"`
	ProviderEntries  []ProviderQueueEntryView `json:"provider_entries"`
}

type ReceiptIndexEntry struct {
	Sequence   uint64 `json:"sequence"`
	Kind       string `json:"kind"`
	Generation string `json:"generation"`
	Digest     string `json:"digest"`
}

type ReceiptView struct {
	SchemaVersion int                 `json:"schema_version"`
	WorkspaceID   string              `json:"workspace_id"`
	JournalHead   string              `json:"journal_head"`
	Receipts      []ReceiptIndexEntry `json:"receipts"`
}

type CompletionEntryView struct {
	PlanID        string `json:"plan_id"`
	MergeUnitID   string `json:"merge_unit_id"`
	AttemptID     string `json:"attempt_id"`
	Generation    string `json:"generation"`
	Head          string `json:"head"`
	MergeCommit   string `json:"merge_commit"`
	FinalBaseHead string `json:"final_base_head"`
	ReceiptDigest string `json:"receipt_digest"`
}

type CompletionView struct {
	SchemaVersion int                   `json:"schema_version"`
	WorkspaceID   string                `json:"workspace_id"`
	JournalHead   string                `json:"journal_head"`
	Completed     []CompletionEntryView `json:"completed"`
}

type WorkspaceReport struct {
	SchemaVersion       int            `json:"schema_version"`
	WorkspaceID         string         `json:"workspace_id"`
	ActiveGeneration    string         `json:"active_generation"`
	JournalHead         string         `json:"journal_head"`
	CoreConformance     string         `json:"core_conformance"`
	ReviewConformance   string         `json:"review_conformance"`
	ProviderConformance string         `json:"provider_conformance"`
	Scheduler           SchedulerView  `json:"scheduler"`
	Gates               GateView       `json:"gates"`
	Queue               MergeQueueView `json:"queue"`
	Receipts            ReceiptView    `json:"receipts"`
	Completion          CompletionView `json:"completion"`
	ReportDigest        string         `json:"report_digest"`
}

func RebuildSchedulerView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (SchedulerView, error) {
	core, reviews, providers, _, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return SchedulerView{}, err
	}
	dependencies, references := definitionDependencyGraph(definition)
	attempts := latestAttemptsByMergeUnit(core)
	completions, err := completionsByMergeUnit(providers)
	if err != nil {
		return SchedulerView{}, err
	}
	completed := make(map[string]bool, len(completions))
	for key, receipt := range completions {
		attempt, exists := attempts[key]
		if exists && attempt.attemptID == receipt.attemptID && attemptCompletionBoundaryResolved(attempt) {
			completed[key] = true
		}
	}

	view := SchedulerView{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		ActiveGeneration: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
		Units: make([]SchedulerUnitView, 0, len(references)),
	}
	for _, reference := range references {
		key := reference.key()
		unit := SchedulerUnitView{
			PlanID: reference.planID.String(), MergeUnitID: reference.mergeUnitID.String(),
			Generation: definition.generation.String(), Dependencies: make([]string, 0, len(dependencies[key])),
			Blockers: []string{}, PendingDirectives: []BoundaryDirectiveView{},
		}
		for _, dependency := range dependencies[key] {
			unit.Dependencies = append(unit.Dependencies, dependency.String())
			if !completed[dependency.key()] {
				unit.Blockers = append(unit.Blockers, "dependency:"+dependency.String())
			}
		}
		if receipt, ok := completions[key]; ok && completed[key] {
			unit.Status = SchedulerUnitCompleted
			unit.LifecycleGeneration = receipt.Generation().String()
			unit.AttemptID = receipt.AttemptID().String()
			unit.Head = receipt.Head().String()
			unit.CompletionReceipt = receipt.Digest().String()
			unit.Blockers = []string{}
		} else if attempt, ok := attempts[key]; ok {
			receipt, completedByProvider := completions[key]
			unit.Status = schedulerStatusForAttempt(attempt)
			unit.LifecycleGeneration = attempt.generation.String()
			unit.AttemptID = attempt.attemptID.String()
			unit.AttemptNumber = attempt.attemptNumber
			unit.Branch = attempt.branch
			unit.Worktree = attempt.worktree
			unit.Head = attempt.verifiedHead.String()
			if completedByProvider {
				unit.CompletionReceipt = receipt.Digest().String()
				unit.LifecycleGeneration = receipt.Generation().String()
			}
			unit.BoundaryPending, unit.BoundaryReason, unit.PendingDirectives = attemptBoundaryStatus(core, attempt, completedByProvider)
			if state, exists := reviews.State(attempt.attemptID); exists {
				if _, exhausted := state.Exhaustion(); exhausted {
					unit.Status = SchedulerUnitReviewExhausted
				}
			}
		} else if len(unit.Blockers) == 0 {
			unit.Status = SchedulerUnitReady
		} else {
			unit.Status = SchedulerUnitBlocked
		}
		view.Units = append(view.Units, unit)
	}
	return view, nil
}

func RebuildGateView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (GateView, error) {
	core, reviews, providers, authorization, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return GateView{}, err
	}
	scheduler, err := RebuildSchedulerView(snapshot, definition)
	if err != nil {
		return GateView{}, err
	}
	attempts := latestAttemptsByMergeUnit(core)
	unitExecution := unitExecutionsByMergeUnit(definition)
	completed, err := completionsByMergeUnit(providers)
	if err != nil {
		return GateView{}, err
	}
	authorizationSafe := authorizationStateSafe(authorization.State())
	view := GateView{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		ActiveGeneration: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
		Units: make([]UnitGateView, 0, len(scheduler.Units)),
	}
	for _, scheduled := range scheduler.Units {
		key := scheduled.PlanID + "\x00" + scheduled.MergeUnitID
		unit := UnitGateView{PlanID: scheduled.PlanID, MergeUnitID: scheduled.MergeUnitID, Checks: []GateCheckView{}}
		dependencyGate := GateCheckView{Name: "dependencies", Generation: definition.generation.String()}
		if len(scheduled.Blockers) == 0 {
			dependencyGate.Status, dependencyGate.Reason = GatePassed, "all_dependencies_completed"
		} else {
			dependencyGate.Status, dependencyGate.Reason = GatePending, scheduled.Blockers[0]
		}
		unit.Checks = append(unit.Checks, dependencyGate)

		attempt, hasAttempt := attempts[key]
		if hasAttempt {
			unit.AttemptID = attempt.attemptID.String()
		}
		commitGate := GateCheckView{Name: "commit", Generation: definition.generation.String()}
		execution := unitExecution[key]
		_, commitConfigured := execution.CommitProtocol()
		switch {
		case !hasAttempt:
			commitGate.Status, commitGate.Reason = GatePending, "no_attempt"
		case !commitConfigured:
			commitGate.Status, commitGate.Reason = GatePassed, "not_configured"
		case attempt.commitProtocol != nil && attempt.commitProtocol.Phase() == CommitProtocolComplete:
			commitGate.Status, commitGate.Reason = GatePassed, "protocol_complete"
		default:
			commitGate.Status, commitGate.Reason = GatePending, "protocol_incomplete"
		}
		unit.Checks = append(unit.Checks, commitGate)

		reviewGate := GateCheckView{Name: "review", Generation: definition.generation.String()}
		_, reviewConfigured := execution.ReviewLoop()
		switch {
		case !hasAttempt:
			reviewGate.Status, reviewGate.Reason = GatePending, "no_attempt"
		case !reviewConfigured:
			reviewGate.Status, reviewGate.Reason = GatePassed, "not_configured"
		default:
			state, exists := reviews.State(attempt.attemptID)
			if exists && state.MergeReady() {
				reviewGate.Status, reviewGate.Reason = GatePassed, "all_profiles_confirmed"
			} else if exists {
				if _, exhausted := state.Exhaustion(); exhausted {
					reviewGate.Status, reviewGate.Reason = GateFailed, "review_exhausted"
				} else {
					reviewGate.Status, reviewGate.Reason = GatePending, "review_incomplete"
				}
			} else {
				reviewGate.Status, reviewGate.Reason = GatePending, "review_not_started"
			}
		}
		unit.Checks = append(unit.Checks, reviewGate)

		authorizationGate := GateCheckView{Name: "authorization_safety", Generation: definition.generation.String()}
		if authorizationSafe {
			authorizationGate.Status, authorizationGate.Reason = GatePassed, "safe"
		} else {
			authorizationGate.Status, authorizationGate.Reason = GateFailed, "unsafe_or_reconciliation_pending"
		}
		unit.Checks = append(unit.Checks, authorizationGate)

		completionGate := GateCheckView{Name: "provider_completion", Generation: definition.generation.String()}
		if receipt, exists := completed[key]; exists {
			completionGate.Status, completionGate.Reason = GatePassed, receipt.Digest().String()
		} else {
			completionGate.Status, completionGate.Reason = GatePending, "merge_not_verified"
		}
		unit.Checks = append(unit.Checks, completionGate)
		unit.MergeReady = dependencyGate.Status == GatePassed && commitGate.Status == GatePassed &&
			reviewGate.Status == GatePassed && authorizationGate.Status == GatePassed && completionGate.Status != GatePassed
		view.Units = append(view.Units, unit)
	}
	return view, nil
}

func RebuildMergeQueueView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (MergeQueueView, error) {
	core, _, providers, _, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return MergeQueueView{}, err
	}
	gates, err := RebuildGateView(snapshot, definition)
	if err != nil {
		return MergeQueueView{}, err
	}
	attempts := latestAttemptsByMergeUnit(core)
	view := MergeQueueView{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		ActiveGeneration: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
		Ready: []MergeQueueEntryView{}, ProviderEntries: []ProviderQueueEntryView{},
	}
	for _, unit := range gates.Units {
		if !unit.MergeReady {
			continue
		}
		key := unit.PlanID + "\x00" + unit.MergeUnitID
		attempt, exists := attempts[key]
		if !exists || attempt.generation != definition.generation {
			continue
		}
		view.Ready = append(view.Ready, MergeQueueEntryView{
			Position: len(view.Ready) + 1, PlanID: unit.PlanID, MergeUnitID: unit.MergeUnitID,
			AttemptID: attempt.attemptID.String(), Generation: attempt.generation.String(), Head: attempt.verifiedHead.String(),
		})
	}
	for _, entry := range providers.QueueEntryStates() {
		view.ProviderEntries = append(view.ProviderEntries, ProviderQueueEntryView{
			EntryID: entry.entryID.String(), Generation: entry.generation.String(), Resolved: entry.resolved,
		})
	}
	sort.Slice(view.ProviderEntries, func(i, j int) bool { return view.ProviderEntries[i].EntryID < view.ProviderEntries[j].EntryID })
	return view, nil
}

func RebuildReceiptView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (ReceiptView, error) {
	core, _, _, _, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return ReceiptView{}, err
	}
	view := ReceiptView{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		JournalHead: snapshot.head.String(), Receipts: []ReceiptIndexEntry{},
	}
	appendReceipt := func(record JournalRecord, kind string, digest Digest) {
		if digest.IsZero() {
			return
		}
		view.Receipts = append(view.Receipts, ReceiptIndexEntry{
			Sequence: record.sequence, Kind: kind, Generation: record.Generation().String(), Digest: digest.String(),
		})
	}
	for _, record := range snapshot.records {
		switch event := record.event.(type) {
		case AttemptOrchestrationAcknowledgedJournalEvent:
			appendReceipt(record, "goal_acknowledgment", event.receiptDigest)
		case AttemptOwnerResponseJournalEvent:
			appendReceipt(record, "owner_decision", event.receiptDigest)
		case AuthorizationGrantRecordedJournalEvent:
			appendReceipt(record, "standing_grant", event.grant.receiptDigest)
		case AuthorizationRevokedJournalEvent:
			appendReceipt(record, "revocation", event.revocation.receipt)
		case AuthorizationSafetyChangedJournalEvent:
			appendReceipt(record, "authorization_safety", event.receiptDigest)
		case GenerationActivatedJournalEvent:
			appendReceipt(record, "reconciliation", event.ownerReceiptDigest)
		case ReviewResultRecordedJournalEvent:
			appendReceipt(record, "review_evidence", event.receiptDigest)
		case ProviderResultRecordedJournalEvent:
			appendReceipt(record, "provider_result", event.result.digest)
		case ProviderCompletionVerifiedJournalEvent:
			appendReceipt(record, "provider_completion", event.receipt.digest)
		}
	}
	return view, nil
}

func RebuildCompletionView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (CompletionView, error) {
	core, _, providers, _, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return CompletionView{}, err
	}
	view := CompletionView{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		JournalHead: snapshot.head.String(), Completed: []CompletionEntryView{},
	}
	for _, receipt := range providers.CompletionReceipts() {
		view.Completed = append(view.Completed, CompletionEntryView{
			PlanID: receipt.mergeUnit.planID.String(), MergeUnitID: receipt.mergeUnit.mergeUnitID.String(),
			AttemptID: receipt.attemptID.String(), Generation: receipt.generation.String(), Head: receipt.head.String(),
			MergeCommit: receipt.mergeCommit.String(), FinalBaseHead: receipt.finalBaseHead.String(),
			ReceiptDigest: receipt.digest.String(),
		})
	}
	sort.Slice(view.Completed, func(i, j int) bool {
		left := view.Completed[i].PlanID + "\x00" + view.Completed[i].MergeUnitID
		right := view.Completed[j].PlanID + "\x00" + view.Completed[j].MergeUnitID
		return left < right
	})
	return view, nil
}

func RebuildWorkspaceReport(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (WorkspaceReport, error) {
	core, _, _, _, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	coreDigest, err := VerifyWorkspaceRuntimeConformance(snapshot, definition.generation)
	if err != nil {
		return WorkspaceReport{}, err
	}
	reviewDigest, err := VerifyReviewRuntimeConformance(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	providerDigest, err := VerifyProviderRuntimeConformance(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	scheduler, err := RebuildSchedulerView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	gates, err := RebuildGateView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	queue, err := RebuildMergeQueueView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	receipts, err := RebuildReceiptView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	completion, err := RebuildCompletionView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	report := WorkspaceReport{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		ActiveGeneration: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
		CoreConformance: coreDigest.String(), ReviewConformance: reviewDigest.String(), ProviderConformance: providerDigest.String(),
		Scheduler: scheduler, Gates: gates, Queue: queue, Receipts: receipts, Completion: completion,
	}
	canonical, err := json.Marshal(report)
	if err != nil {
		return WorkspaceReport{}, err
	}
	report.ReportDigest = DigestBytes(canonical).String()
	return report, nil
}

func RebuildReconciliationState(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (ReconciliationState, error) {
	core, reviews, providers, authorization, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return ReconciliationState{}, err
	}
	scheduler, err := RebuildSchedulerView(snapshot, definition)
	if err != nil {
		return ReconciliationState{}, err
	}
	mergeUnits := make([]MergeUnitRuntimeState, 0, len(scheduler.Units))
	for _, unit := range scheduler.Units {
		reference, err := referenceFromStrings(unit.PlanID, unit.MergeUnitID)
		if err != nil {
			return ReconciliationState{}, err
		}
		disposition := reconciliationDisposition(unit.Status)
		generation := Digest{}
		if disposition != MergeUnitFuture {
			generation, err = ParseDigest(unit.LifecycleGeneration)
			if err != nil {
				generation = definition.generation
			}
		}
		state, err := NewMergeUnitRuntimeState(reference, disposition, generation)
		if err != nil {
			return ReconciliationState{}, err
		}
		mergeUnits = append(mergeUnits, state)
	}
	reviewHistory, err := json.Marshal(reviewHistoryView(reviews))
	if err != nil {
		return ReconciliationState{}, err
	}
	authorizationHistory, err := canonicalAuthorizationHistory(authorization.State())
	if err != nil {
		return ReconciliationState{}, err
	}
	receipts, err := RebuildReceiptView(snapshot, definition)
	if err != nil {
		return ReconciliationState{}, err
	}
	receiptHistory, err := json.Marshal(receipts)
	if err != nil {
		return ReconciliationState{}, err
	}
	history, err := NewRuntimeHistoryBinding(
		DigestBytes(reviewHistory), DigestBytes(authorizationHistory), DigestBytes(receiptHistory),
	)
	if err != nil {
		return ReconciliationState{}, err
	}
	attemptBindings := core.AttemptGenerationBindings()
	completedAttempts := make(map[ID]struct{}, len(scheduler.Units))
	for _, unit := range scheduler.Units {
		if unit.Status == SchedulerUnitCompleted {
			attemptID, parseErr := NewID(unit.AttemptID)
			if parseErr != nil {
				return ReconciliationState{}, fmt.Errorf("completed scheduler attempt: %w", parseErr)
			}
			completedAttempts[attemptID] = struct{}{}
		}
	}
	for index := range attemptBindings {
		if _, completed := completedAttempts[attemptBindings[index].attemptID]; completed {
			attemptBindings[index].phase = AttemptCompleted
		}
	}
	return NewReconciliationState(
		snapshot, mergeUnits, attemptBindings, providers.ReconciliationStates(),
		providers.QueueEntryStates(), history,
	)
}

func attemptCompletionBoundaryResolved(attempt RuntimeAttemptProjection) bool {
	pending, _, _ := attemptBoundaryStatus(WorkspaceRuntimeProjection{workspaceID: ID{}}, attempt, true)
	return !pending
}

func attemptBoundaryStatus(
	core WorkspaceRuntimeProjection,
	attempt RuntimeAttemptProjection,
	completionRecorded bool,
) (bool, string, []BoundaryDirectiveView) {
	directives := []BoundaryDirectiveView{}
	boundary, exists := attempt.CurrentBoundary()
	if !exists {
		if completionRecorded {
			return true, "completion_boundary_not_recorded", directives
		}
		return false, "", directives
	}
	view := func(kind string, goal GoalBinding, idempotency Digest, choices []string) BoundaryDirectiveView {
		return BoundaryDirectiveView{
			Kind: kind, WorkspaceID: core.workspaceID.String(), Generation: attempt.generation.String(),
			AttemptID: attempt.attemptID.String(), BoundaryID: boundary.boundaryID.String(),
			GoalID: goal.id.String(), GoalScope: string(goal.scope), Head: boundary.head.String(),
			DirectiveDigest: boundary.directiveDigest.String(), IdempotencyKey: idempotency.String(), Choices: choices,
		}
	}
	if boundary.mode == AttemptBoundaryCompleteGoalAndWait && !boundary.goalCompletedOK {
		return true, "complete_goal_and_wait", []BoundaryDirectiveView{
			view("complete_goal_and_wait", boundary.goal, boundary.idempotencyKey, nil),
		}
	}
	if !boundary.ownerResponseOK {
		return true, "owner_gate", []BoundaryDirectiveView{
			view("owner_gate", boundary.goal, Digest{}, []string{string(OwnerBoundaryContinue)}),
		}
	}
	if boundary.mode == AttemptBoundaryCompleteGoalAndWait {
		if !boundary.nextGoalIntentOK {
			return true, "next_goal_intent_not_recorded", directives
		}
		if !boundary.nextGoalOK {
			return true, "create_next_goal", []BoundaryDirectiveView{
				view("create_next_goal", boundary.nextGoalIntent.goal, boundary.nextGoalIntent.idempotencyKey, nil),
			}
		}
	}
	return false, "", directives
}

func rebuildViewProjections(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
) (WorkspaceRuntimeProjection, ReviewRuntimeProjection, ProviderRuntimeProjection, AuthorizationRuntimeProjection, error) {
	core, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, err
	}
	if core.workspaceID != definition.workspace.id || core.activeGeneration != definition.generation {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{},
			fmt.Errorf("workspace report definition does not match active journal generation")
	}
	if err := requireReadyLocalTarget(core); err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, err
	}
	reviews, err := RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, err
	}
	providers, err := RebuildProviderRuntime(snapshot, definition)
	if err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, err
	}
	authorization, err := RebuildAuthorizationRuntime(snapshot, definition)
	if err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, ProviderRuntimeProjection{}, AuthorizationRuntimeProjection{}, err
	}
	return core, reviews, providers, authorization, nil
}

func definitionDependencyGraph(definition EffectiveWorkspaceDefinition) (map[string][]MergeUnitReference, []MergeUnitReference) {
	references := make([]MergeUnitReference, 0)
	dependencies := make(map[string][]MergeUnitReference)
	storyUnits := make(map[string]MergeUnitReference)
	for _, plan := range definition.plans {
		for _, unit := range plan.mergeUnits {
			reference := MergeUnitReference{planID: plan.id, mergeUnitID: unit.id}
			references = append(references, reference)
			for _, storyID := range unit.storyIDs {
				storyUnits[plan.id.String()+"\x00"+storyID.String()] = reference
			}
		}
	}
	for _, plan := range definition.plans {
		for _, story := range plan.stories {
			unit := storyUnits[plan.id.String()+"\x00"+story.id.String()]
			for _, dependencyID := range story.dependencies {
				dependency := storyUnits[plan.id.String()+"\x00"+dependencyID.String()]
				if dependency != unit {
					dependencies[unit.key()] = appendUniqueReference(dependencies[unit.key()], dependency)
				}
			}
		}
	}
	for _, dependency := range definition.workspace.dependencies {
		dependencies[dependency.after.key()] = appendUniqueReference(dependencies[dependency.after.key()], dependency.before)
	}
	for key := range dependencies {
		sort.Slice(dependencies[key], func(i, j int) bool { return dependencies[key][i].key() < dependencies[key][j].key() })
	}
	sort.Slice(references, func(i, j int) bool { return references[i].key() < references[j].key() })
	return dependencies, references
}

func appendUniqueReference(values []MergeUnitReference, value MergeUnitReference) []MergeUnitReference {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func latestAttemptsByMergeUnit(core WorkspaceRuntimeProjection) map[string]RuntimeAttemptProjection {
	result := make(map[string]RuntimeAttemptProjection)
	for _, attempt := range core.attempts {
		key := attempt.mergeUnit.key()
		current, exists := result[key]
		if !exists || attempt.attemptNumber > current.attemptNumber ||
			(attempt.attemptNumber == current.attemptNumber && attempt.reservationRecord > current.reservationRecord) {
			result[key] = cloneRuntimeAttempt(attempt)
		}
	}
	return result
}

func completionsByMergeUnit(providers ProviderRuntimeProjection) (map[string]ProviderCompletionReceipt, error) {
	result := make(map[string]ProviderCompletionReceipt)
	for _, receipt := range providers.completionReceipts {
		key := receipt.mergeUnit.key()
		if existing, exists := result[key]; exists && existing.digest != receipt.digest {
			return nil, fmt.Errorf("merge unit %s has multiple provider completion receipts", receipt.mergeUnit)
		}
		result[key] = receipt
	}
	return result, nil
}

func schedulerStatusForAttempt(attempt RuntimeAttemptProjection) SchedulerUnitStatus {
	switch attempt.phase {
	case AttemptReserved:
		return SchedulerUnitReserved
	case AttemptMaterializing:
		return SchedulerUnitMaterializing
	case AttemptPaused:
		return SchedulerUnitPaused
	case AttemptReviewExhausted:
		return SchedulerUnitReviewExhausted
	default:
		return SchedulerUnitActive
	}
}

func unitExecutionsByMergeUnit(definition EffectiveWorkspaceDefinition) map[string]UnitExecution {
	result := make(map[string]UnitExecution, len(definition.execution.mergeUnits))
	for _, unit := range definition.execution.mergeUnits {
		result[unit.planID.String()+"\x00"+unit.mergeUnitID.String()] = unit
	}
	return result
}

func authorizationStateSafe(state AuthorizationState) bool {
	safety := state.safety
	return !safety.gatesBlocked && !safety.reconciliationPending && !safety.driftDetected && !safety.ambiguousEffect &&
		len(state.obligations) == 0
}

func referenceFromStrings(planID, mergeUnitID string) (MergeUnitReference, error) {
	plan, err := NewID(planID)
	if err != nil {
		return MergeUnitReference{}, err
	}
	unit, err := NewID(mergeUnitID)
	if err != nil {
		return MergeUnitReference{}, err
	}
	return NewMergeUnitReference(plan, unit)
}

func reconciliationDisposition(status SchedulerUnitStatus) MergeUnitRuntimeDisposition {
	switch status {
	case SchedulerUnitReserved:
		return MergeUnitReserved
	case SchedulerUnitMaterializing:
		return MergeUnitMaterializing
	case SchedulerUnitActive:
		return MergeUnitActive
	case SchedulerUnitPaused:
		return MergeUnitPaused
	case SchedulerUnitReviewExhausted:
		return MergeUnitReviewExhausted
	case SchedulerUnitCompleted:
		return MergeUnitCompleted
	default:
		return MergeUnitFuture
	}
}

type reviewHistoryEntry struct {
	AttemptID             string `json:"attempt_id"`
	Generation            string `json:"generation"`
	RoundsUsed            uint16 `json:"rounds_used"`
	FixesUsed             uint16 `json:"fixes_used"`
	InfrastructureRetries uint16 `json:"infrastructure_retries"`
	Head                  string `json:"head"`
	MergeReady            bool   `json:"merge_ready"`
}

func reviewHistoryView(projection ReviewRuntimeProjection) []reviewHistoryEntry {
	result := make([]reviewHistoryEntry, 0, len(projection.states))
	for _, state := range projection.states {
		result = append(result, reviewHistoryEntry{
			AttemptID: state.attemptID.String(), Generation: state.generation.String(), RoundsUsed: state.RoundsUsed(),
			FixesUsed: state.FixesUsed(), InfrastructureRetries: state.InfrastructureRetriesUsed(),
			Head: state.head.String(), MergeReady: state.MergeReady(),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AttemptID < result[j].AttemptID })
	return result
}

func canonicalAuthorizationHistory(state AuthorizationState) ([]byte, error) {
	type authorizationHistory struct {
		WorkspaceID       string   `json:"workspace_id"`
		Generation        string   `json:"generation"`
		Epoch             uint64   `json:"epoch"`
		Safety            [4]bool  `json:"safety"`
		GrantIDs          []string `json:"grant_ids"`
		RevokedGrantIDs   []string `json:"revoked_grant_ids"`
		CompletedSegments []string `json:"completed_segments"`
		Obligations       []string `json:"obligations"`
	}
	value := authorizationHistory{
		WorkspaceID: state.workspaceID.String(), Generation: state.generation.String(), Epoch: state.epoch,
		Safety:   [4]bool{state.safety.gatesBlocked, state.safety.reconciliationPending, state.safety.driftDetected, state.safety.ambiguousEffect},
		GrantIDs: []string{}, RevokedGrantIDs: []string{}, CompletedSegments: []string{}, Obligations: []string{},
	}
	for _, grant := range state.grants {
		value.GrantIDs = append(value.GrantIDs, grant.grantID.String())
	}
	for _, revoked := range state.revokedGrantIDs {
		value.RevokedGrantIDs = append(value.RevokedGrantIDs, revoked.String())
	}
	for _, segment := range state.completedSegments {
		value.CompletedSegments = append(value.CompletedSegments, segment.String())
	}
	for _, obligation := range state.obligations {
		value.Obligations = append(value.Obligations, obligation.effectID.String()+":"+obligation.requestDigest.String())
	}
	sort.Strings(value.GrantIDs)
	sort.Strings(value.RevokedGrantIDs)
	sort.Strings(value.CompletedSegments)
	sort.Strings(value.Obligations)
	return json.Marshal(value)
}
