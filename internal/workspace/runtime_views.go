package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

type WorkspaceUnitBlocker struct {
	Kind           string     `json:"kind"`
	Reason         string     `json:"reason"`
	DependencySets [][]string `json:"dependency_sets,omitempty"`
}

type WorkspaceUnitState struct {
	PlanID            string                       `json:"plan_id"`
	MergeUnitID       string                       `json:"merge_unit_id"`
	Status            SchedulerUnitStatus          `json:"status"`
	Generation        string                       `json:"generation"`
	Dependencies      []string                     `json:"dependencies"`
	Blockers          []WorkspaceUnitBlocker       `json:"blockers"`
	AttemptID         string                       `json:"attempt_id,omitempty"`
	AttemptNumber     uint64                       `json:"attempt_number,omitempty"`
	Branch            string                       `json:"branch,omitempty"`
	Worktree          string                       `json:"worktree,omitempty"`
	Head              string                       `json:"head,omitempty"`
	BoundaryPending   bool                         `json:"boundary_pending"`
	BoundaryReason    string                       `json:"boundary_reason,omitempty"`
	PendingDirectives []WorkspaceBoundaryDirective `json:"pending_directives"`
}

type WorkspaceBoundaryDirective struct {
	Kind            string   `json:"kind"`
	BoundaryKind    string   `json:"boundary_kind"`
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

type WorkspaceSchedule struct {
	SchemaVersion int                  `json:"schema_version"`
	WorkspaceID   string               `json:"workspace_id"`
	Generation    string               `json:"generation"`
	JournalHead   string               `json:"journal_head"`
	Units         []WorkspaceUnitState `json:"units"`
}

type GateStatus string

const (
	GatePending GateStatus = "pending"
	GatePassed  GateStatus = "passed"
	GateFailed  GateStatus = "failed"
)

type WorkspaceGate struct {
	Name       string     `json:"name"`
	Status     GateStatus `json:"status"`
	Generation string     `json:"generation"`
	Reason     string     `json:"reason"`
}

type WorkspaceUnitGates struct {
	PlanID      string          `json:"plan_id"`
	MergeUnitID string          `json:"merge_unit_id"`
	AttemptID   string          `json:"attempt_id,omitempty"`
	Checks      []WorkspaceGate `json:"checks"`
	MergeReady  bool            `json:"merge_ready"`
}

type WorkspaceGates struct {
	SchemaVersion      int                  `json:"schema_version"`
	WorkspaceID        string               `json:"workspace_id"`
	Generation         string               `json:"generation"`
	JournalHead        string               `json:"journal_head"`
	Units              []WorkspaceUnitGates `json:"units"`
	Completion         WorkspaceGate        `json:"completion"`
	CompletionBlockers []string             `json:"completion_blockers"`
}

type WorkspaceWorkflow struct {
	WorkspaceID            string `json:"workspace_id"`
	Generation             string `json:"generation"`
	JournalHead            string `json:"journal_head"`
	PlanCheckpoint         string `json:"plan_checkpoint"`
	WorktreeRoot           string `json:"worktree_root"`
	ProjectionDigest       string `json:"projection_digest"`
	ReviewProjectionDigest string `json:"review_projection_digest"`
}

type WorkspaceTarget struct {
	Root             string `json:"root"`
	GitDirectory     string `json:"git_directory"`
	CommonDirectory  string `json:"common_directory"`
	RepositoryFormat uint64 `json:"repository_format"`
	ObjectFormat     string `json:"object_format"`
	LinkedWorktree   bool   `json:"linked_worktree"`
	BaseRef          string `json:"base_ref"`
	BaseCommit       string `json:"base_commit"`
	FeatureBranch    string `json:"feature_branch"`
	FeatureRef       string `json:"feature_ref"`
	FeatureHead      string `json:"feature_head"`
	BindingDigest    string `json:"binding_digest"`
	Ready            bool   `json:"ready"`
}

type WorkspaceAttempt struct {
	AttemptID         string                       `json:"attempt_id"`
	PlanID            string                       `json:"plan_id"`
	MergeUnitID       string                       `json:"merge_unit_id"`
	Generation        string                       `json:"generation"`
	AttemptNumber     uint64                       `json:"attempt_number"`
	Base              string                       `json:"base"`
	Branch            string                       `json:"branch"`
	Worktree          string                       `json:"worktree"`
	Phase             AttemptRuntimePhase          `json:"phase"`
	Head              string                       `json:"head,omitempty"`
	GoalID            string                       `json:"goal_id"`
	GoalScope         GoalScope                    `json:"goal_scope"`
	BoundaryPending   bool                         `json:"boundary_pending"`
	BoundaryReason    string                       `json:"boundary_reason,omitempty"`
	PendingDirectives []WorkspaceBoundaryDirective `json:"pending_directives"`
}

type WorkspaceReview struct {
	AttemptID             string `json:"attempt_id"`
	PlanID                string `json:"plan_id"`
	MergeUnitID           string `json:"merge_unit_id"`
	Generation            string `json:"generation"`
	Head                  string `json:"head"`
	Tree                  string `json:"tree"`
	Status                string `json:"status"`
	RoundsUsed            uint16 `json:"rounds_used"`
	FixesUsed             uint16 `json:"fixes_used"`
	InfrastructureRetries uint16 `json:"infrastructure_retries"`
	MergeReady            bool   `json:"merge_ready"`
}

type WorkspaceIntegrationUnit struct {
	PlanID      string `json:"plan_id"`
	MergeUnitID string `json:"merge_unit_id"`
	AttemptID   string `json:"attempt_id,omitempty"`
	Head        string `json:"head,omitempty"`
	Status      string `json:"status"`
}

type WorkspaceIntegration struct {
	Units []WorkspaceIntegrationUnit `json:"units"`
}

type WorkspaceDrift struct {
	Detected bool     `json:"detected"`
	Reasons  []string `json:"reasons"`
}

type WorkspaceCompletion struct {
	Complete     bool     `json:"complete"`
	Blockers     []string `json:"blockers"`
	ReportDigest string   `json:"report_digest,omitempty"`
}

// WorkspaceView is the single journal-derived operator view. Its nested
// sections retain the established wire layout while sharing one rebuild.
type WorkspaceView struct {
	SchemaVersion int                  `json:"schema_version"`
	Workflow      WorkspaceWorkflow    `json:"workflow"`
	Target        WorkspaceTarget      `json:"target"`
	Attempts      []WorkspaceAttempt   `json:"attempts"`
	Reviews       []WorkspaceReview    `json:"reviews"`
	Scheduler     WorkspaceSchedule    `json:"scheduler"`
	Gates         WorkspaceGates       `json:"gates"`
	Integration   WorkspaceIntegration `json:"integration"`
	Drift         WorkspaceDrift       `json:"drift"`
	Completion    WorkspaceCompletion  `json:"completion"`
	ReportDigest  string               `json:"report_digest"`

	snapshot JournalSnapshot
	runtime  WorkspaceRuntimeProjection
	reviews  ReviewRuntimeProjection
}

func RebuildWorkspaceView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (WorkspaceView, error) {
	core, reviews, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return WorkspaceView{}, err
	}
	coreDigest, err := VerifyWorkspaceRuntimeConformance(snapshot, definition.generation)
	if err != nil {
		return WorkspaceView{}, err
	}
	reviewDigest, err := VerifyReviewRuntimeConformance(snapshot, definition)
	if err != nil {
		return WorkspaceView{}, err
	}
	target, err := workspaceTargetView(core, definition)
	if err != nil {
		return WorkspaceView{}, err
	}

	dependencies, references := definitionDependencyGraph(definition)
	attemptsByUnit := latestAttemptsByMergeUnit(core)
	unitExecution := unitExecutionsByMergeUnit(definition)
	schedule := WorkspaceSchedule{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		Generation: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
		Units: make([]WorkspaceUnitState, 0, len(references)),
	}
	gates := WorkspaceGates{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		Generation: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
		Units: make([]WorkspaceUnitGates, 0, len(references)),
	}
	integration := WorkspaceIntegration{
		Units: make([]WorkspaceIntegrationUnit, 0, len(references)),
	}
	for _, reference := range references {
		key := reference.key()
		unit := WorkspaceUnitState{
			PlanID: reference.planID.String(), MergeUnitID: reference.mergeUnitID.String(),
			Generation:        definition.generation.String(),
			Dependencies:      make([]string, 0, len(dependencies[key])),
			Blockers:          []WorkspaceUnitBlocker{},
			PendingDirectives: []WorkspaceBoundaryDirective{},
		}
		unsatisfiedDependencySets := make([][]string, 0, len(dependencies[key]))
		for _, dependency := range dependencies[key] {
			dependencyID := dependency.String()
			unit.Dependencies = append(unit.Dependencies, dependencyID)
			dependencyAttempt, completed := attemptsByUnit[dependency.key()]
			if !completed || dependencyAttempt.phase != AttemptCompleted {
				unsatisfiedDependencySets = append(
					unsatisfiedDependencySets, []string{dependencyID},
				)
			}
		}
		if len(unsatisfiedDependencySets) != 0 {
			unit.Blockers = append(unit.Blockers, WorkspaceUnitBlocker{
				Kind:           "dependency_sets",
				Reason:         dependencySetReason(unsatisfiedDependencySets),
				DependencySets: cloneDependencySets(unsatisfiedDependencySets),
			})
		}

		attempt, hasAttempt := attemptsByUnit[key]
		if hasAttempt && attempt.phase.retryableTerminal() {
			hasAttempt = false
		}
		if hasAttempt {
			unit.Status = schedulerStatusForAttempt(attempt)
			unit.AttemptID = attempt.attemptID.String()
			unit.AttemptNumber = attempt.attemptNumber
			unit.Branch = attempt.branch
			unit.Worktree = attempt.worktree
			unit.Head = attempt.verifiedHead.String()
			unit.BoundaryPending, unit.BoundaryReason, unit.PendingDirectives = attemptBoundaryStatus(core, attempt)
			if attempt.phase == AttemptActive {
				if state, exists := reviews.State(attempt.attemptID); exists {
					if _, exhausted := state.Exhaustion(); exhausted {
						unit.Status = SchedulerUnitReviewExhausted
					}
				}
			}
		} else if len(unit.Blockers) == 0 {
			unit.Status = SchedulerUnitReady
		} else {
			unit.Status = SchedulerUnitBlocked
		}

		unitGates := WorkspaceUnitGates{
			PlanID: unit.PlanID, MergeUnitID: unit.MergeUnitID,
			Checks: []WorkspaceGate{},
		}
		dependencyGate := WorkspaceGate{
			Name: "dependencies", Generation: definition.generation.String(),
		}
		if len(unit.Blockers) == 0 {
			dependencyGate.Status, dependencyGate.Reason = GatePassed, "all_dependencies_completed"
		} else {
			dependencyGate.Status, dependencyGate.Reason = GatePending, unit.Blockers[0].Reason
		}
		unitGates.Checks = append(unitGates.Checks, dependencyGate)
		if hasAttempt {
			unitGates.AttemptID = attempt.attemptID.String()
		}

		execution := unitExecution[key]
		commitGate := WorkspaceGate{Name: "commit", Generation: definition.generation.String()}
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
		unitGates.Checks = append(unitGates.Checks, commitGate)

		reviewGate := WorkspaceGate{Name: "review", Generation: definition.generation.String()}
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
		unitGates.Checks = append(unitGates.Checks, reviewGate)

		integrationGate := WorkspaceGate{Name: "integration", Generation: definition.generation.String()}
		integrationStatus := "pending"
		if unit.Status == SchedulerUnitCompleted {
			integrationGate.Status, integrationGate.Reason = GatePassed, "merge_unit_integrated"
			integrationStatus = "integrated"
		} else {
			integrationGate.Status, integrationGate.Reason = GatePending, "not_integrated"
		}
		unitGates.Checks = append(unitGates.Checks, integrationGate)
		unitGates.MergeReady = dependencyGate.Status == GatePassed &&
			commitGate.Status == GatePassed && reviewGate.Status == GatePassed &&
			integrationGate.Status != GatePassed

		schedule.Units = append(schedule.Units, unit)
		gates.Units = append(gates.Units, unitGates)
		integration.Units = append(integration.Units, WorkspaceIntegrationUnit{
			PlanID: unit.PlanID, MergeUnitID: unit.MergeUnitID,
			AttemptID: unit.AttemptID, Head: unit.Head, Status: integrationStatus,
		})
	}

	completionBlockers, complete, completionDigest, err := workspaceCompletionViewState(
		snapshot, definition, reviews, core,
	)
	if err != nil {
		return WorkspaceView{}, err
	}
	completion := WorkspaceCompletion{
		Complete:     complete,
		Blockers:     completionBlockers,
		ReportDigest: completionDigest.String(),
	}
	gates.Completion = WorkspaceGate{
		Name: "completion", Generation: definition.generation.String(),
	}
	gates.CompletionBlockers = append([]string{}, completionBlockers...)
	switch {
	case complete:
		gates.Completion.Status, gates.Completion.Reason = GatePassed, "workspace_completed"
	case len(completionBlockers) == 0:
		gates.Completion.Status, gates.Completion.Reason = GatePending, "workspace_completion_not_recorded"
	default:
		if _, recorded := core.Completion(); recorded {
			gates.Completion.Status = GateFailed
		} else {
			gates.Completion.Status = GatePending
		}
		gates.Completion.Reason = completionBlockers[0]
	}

	view := WorkspaceView{
		SchemaVersion: JournalSchemaVersion,
		Workflow: WorkspaceWorkflow{
			WorkspaceID:            core.workspaceID.String(),
			Generation:             core.activeGeneration.String(),
			JournalHead:            snapshot.head.String(),
			PlanCheckpoint:         core.planCheckpoint.String(),
			WorktreeRoot:           core.worktreeRoot.Path(),
			ProjectionDigest:       coreDigest.String(),
			ReviewProjectionDigest: reviewDigest.String(),
		},
		Target:      target,
		Attempts:    workspaceAttemptViews(core, schedule),
		Reviews:     workspaceReviewViews(reviews),
		Scheduler:   schedule,
		Gates:       gates,
		Integration: integration,
		Drift:       WorkspaceDrift{Reasons: []string{}},
		Completion:  completion,
		snapshot:    snapshot,
		runtime:     core,
		reviews:     reviews,
	}
	if err := setWorkspaceViewDigest(&view); err != nil {
		return WorkspaceView{}, err
	}
	return view, nil
}

func ApplyWorkspaceIntegrationDrift(
	ctx context.Context,
	view *WorkspaceView,
	definition EffectiveWorkspaceDefinition,
	git IntegrationGitPort,
) error {
	if ctx == nil || view == nil || git == nil {
		return fmt.Errorf("workspace integration drift check requires context, view, and Git adapter")
	}
	assessment := assessWorkspaceCompletion(
		view.snapshot, definition, view.reviews, view.runtime,
	)
	if len(assessment.chain) == 0 {
		return nil
	}
	target, exists := view.runtime.LocalTarget()
	if !exists || !target.Created() {
		return nil
	}
	if err := git.VerifyCompletedIntegration(
		ctx, target.binding, assessment.chain,
	); err == nil {
		return nil
	} else {
		view.Drift.Detected = true
		view.Drift.Reasons = []string{err.Error()}
		view.Completion.Complete = false
		view.Completion.Blockers =
			sortedUniqueCompletionBlockers(
				append(
					view.Completion.Blockers,
					"git:completed_integration_drift",
				),
			)
		view.Gates.CompletionBlockers = append(
			[]string{},
			view.Completion.Blockers...,
		)
		if _, recorded := view.runtime.Completion(); recorded {
			view.Gates.Completion.Status = GateFailed
		} else {
			view.Gates.Completion.Status = GatePending
		}
		view.Gates.Completion.Reason =
			"git:completed_integration_drift"
	}
	if err := setWorkspaceViewDigest(view); err != nil {
		return err
	}
	return nil
}

func setWorkspaceViewDigest(view *WorkspaceView) error {
	if view == nil {
		return fmt.Errorf("workspace view is required")
	}
	view.ReportDigest = ""
	canonical, err := json.Marshal(view)
	if err != nil {
		return err
	}
	view.ReportDigest = DigestBytes(canonical).String()
	return nil
}

func dependencySetReason(sets [][]string) string {
	parts := make([]string, 0, len(sets))
	for _, providers := range sets {
		parts = append(parts, "["+strings.Join(providers, ", ")+"]")
	}
	return "unsatisfied dependency sets: " + strings.Join(parts, ", ")
}

func cloneDependencySets(source [][]string) [][]string {
	result := make([][]string, 0, len(source))
	for _, set := range source {
		result = append(result, append([]string(nil), set...))
	}
	return result
}

func workspaceUnitBlockerReasons(blockers []WorkspaceUnitBlocker) []string {
	result := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		if blocker.Reason != "" {
			result = append(result, blocker.Reason)
		}
	}
	return result
}

func workspaceTargetView(
	core WorkspaceRuntimeProjection,
	definition EffectiveWorkspaceDefinition,
) (WorkspaceTarget, error) {
	target, exists := core.LocalTarget()
	if !exists {
		configured := definition.workspace.target
		if configured.IsZero() {
			return WorkspaceTarget{}, fmt.Errorf(
				"workspace view requires a configured local target",
			)
		}
		return WorkspaceTarget{
			Root:          configured.Root(),
			BaseRef:       configured.BaseRef(),
			BaseCommit:    configured.BaseCommit().String(),
			FeatureBranch: configured.FeatureBranch(),
			FeatureRef:    configured.FeatureRef(),
			Ready:         false,
		}, nil
	}
	binding := target.Binding()
	featureHead := ""
	if target.Created() {
		featureHead = target.CreatedHead().String()
	}
	return WorkspaceTarget{
		Root:             binding.Root(),
		GitDirectory:     binding.GitDirectory(),
		CommonDirectory:  binding.CommonDirectory(),
		RepositoryFormat: binding.RepositoryFormat(),
		ObjectFormat:     string(binding.ObjectFormat()),
		LinkedWorktree:   binding.LinkedWorktree(),
		BaseRef:          binding.BaseRef(),
		BaseCommit:       binding.BaseCommit().String(),
		FeatureBranch:    binding.FeatureBranch(),
		FeatureRef:       binding.FeatureRef(),
		FeatureHead:      featureHead,
		BindingDigest:    binding.Digest().String(),
		Ready:            target.Created(),
	}, nil
}

func workspaceAttemptViews(
	core WorkspaceRuntimeProjection,
	scheduler WorkspaceSchedule,
) []WorkspaceAttempt {
	scheduled := make(map[string]WorkspaceUnitState, len(scheduler.Units))
	for _, unit := range scheduler.Units {
		if unit.AttemptID != "" {
			scheduled[unit.AttemptID] = unit
		}
	}
	result := make([]WorkspaceAttempt, 0, len(core.attempts))
	for _, attempt := range core.attempts {
		unit := scheduled[attempt.attemptID.String()]
		result = append(result, WorkspaceAttempt{
			AttemptID:         attempt.attemptID.String(),
			PlanID:            attempt.mergeUnit.planID.String(),
			MergeUnitID:       attempt.mergeUnit.mergeUnitID.String(),
			Generation:        attempt.generation.String(),
			AttemptNumber:     attempt.attemptNumber,
			Base:              attempt.base.String(),
			Branch:            attempt.branch,
			Worktree:          attempt.worktree,
			Phase:             attempt.phase,
			Head:              attempt.verifiedHead.String(),
			GoalID:            attempt.goal.id.String(),
			GoalScope:         attempt.goal.scope,
			BoundaryPending:   unit.BoundaryPending,
			BoundaryReason:    unit.BoundaryReason,
			PendingDirectives: append([]WorkspaceBoundaryDirective(nil), unit.PendingDirectives...),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].PlanID + "\x00" + result[i].MergeUnitID
		right := result[j].PlanID + "\x00" + result[j].MergeUnitID
		if left != right {
			return left < right
		}
		if result[i].AttemptNumber != result[j].AttemptNumber {
			return result[i].AttemptNumber < result[j].AttemptNumber
		}
		return result[i].AttemptID < result[j].AttemptID
	})
	return result
}

func workspaceReviewViews(
	reviews ReviewRuntimeProjection,
) []WorkspaceReview {
	states := reviews.States()
	result := make([]WorkspaceReview, 0, len(states))
	for _, state := range states {
		status := "active"
		if state.MergeReady() {
			status = "ready"
		} else if _, exhausted := state.Exhaustion(); exhausted {
			status = "exhausted"
		} else if _, pending := state.PendingFix(); pending {
			status = "fix_pending"
		}
		result = append(result, WorkspaceReview{
			AttemptID:             state.AttemptID().String(),
			PlanID:                state.MergeUnit().PlanID().String(),
			MergeUnitID:           state.MergeUnit().MergeUnitID().String(),
			Generation:            state.Generation().String(),
			Head:                  state.Head().String(),
			Tree:                  state.Tree().String(),
			Status:                status,
			RoundsUsed:            state.RoundsUsed(),
			FixesUsed:             state.FixesUsed(),
			InfrastructureRetries: state.InfrastructureRetriesUsed(),
			MergeReady:            state.MergeReady(),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].AttemptID < result[j].AttemptID
	})
	return result
}

func attemptBoundaryStatus(
	core WorkspaceRuntimeProjection,
	attempt RuntimeAttemptProjection,
) (bool, string, []WorkspaceBoundaryDirective) {
	directives := []WorkspaceBoundaryDirective{}
	boundary, exists := attempt.CurrentBoundary()
	if !exists {
		return false, "", directives
	}
	view := func(kind string, goal GoalBinding, idempotency Digest, choices []string) WorkspaceBoundaryDirective {
		return WorkspaceBoundaryDirective{
			Kind: kind, BoundaryKind: string(boundary.kind), WorkspaceID: core.workspaceID.String(), Generation: attempt.generation.String(),
			AttemptID: attempt.attemptID.String(), BoundaryID: boundary.boundaryID.String(),
			GoalID: goal.id.String(), GoalScope: string(goal.scope), Head: boundary.head.String(),
			DirectiveDigest: boundary.directiveDigest.String(), IdempotencyKey: idempotency.String(),
			Choices: append([]string{}, choices...),
		}
	}
	if boundary.checkpoint == AttemptCheckpointCompleteGoalAndWait && !boundary.goalCompletedOK {
		return true, "complete_goal_and_wait", []WorkspaceBoundaryDirective{
			view("complete_goal_and_wait", boundary.goal, boundary.idempotencyKey, nil),
		}
	}
	if !boundary.ownerResponseOK {
		return true, "owner_gate", []WorkspaceBoundaryDirective{
			view("owner_gate", boundary.goal, Digest{}, []string{string(OwnerBoundaryContinue)}),
		}
	}
	if boundary.checkpoint == AttemptCheckpointCompleteGoalAndWait {
		if !boundary.nextGoalIntentOK {
			return true, "next_goal_intent_not_recorded", directives
		}
		if !boundary.nextGoalOK {
			return true, "create_next_goal", []WorkspaceBoundaryDirective{
				view("create_next_goal", boundary.nextGoalIntent.goal, boundary.nextGoalIntent.idempotencyKey, nil),
			}
		}
	}
	return false, "", directives
}

func rebuildViewProjections(
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
) (WorkspaceRuntimeProjection, ReviewRuntimeProjection, error) {
	core, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, err
	}
	if core.workspaceID != definition.workspace.id || core.activeGeneration != definition.generation {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{},
			fmt.Errorf("workspace view definition does not match active journal generation")
	}
	reviews, err := RebuildReviewRuntime(snapshot, definition)
	if err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, err
	}
	return core, reviews, nil
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
	case AttemptSuperseded, AttemptFailed, AttemptAbandoned:
		return SchedulerUnitReady
	case AttemptCompleted:
		return SchedulerUnitCompleted
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
