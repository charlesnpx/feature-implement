package workspace

import (
	"context"
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
	PlanID            string                  `json:"plan_id"`
	MergeUnitID       string                  `json:"merge_unit_id"`
	Status            SchedulerUnitStatus     `json:"status"`
	Generation        string                  `json:"generation"`
	Dependencies      []string                `json:"dependencies"`
	Blockers          []string                `json:"blockers"`
	AttemptID         string                  `json:"attempt_id,omitempty"`
	AttemptNumber     uint64                  `json:"attempt_number,omitempty"`
	Branch            string                  `json:"branch,omitempty"`
	Worktree          string                  `json:"worktree,omitempty"`
	Head              string                  `json:"head,omitempty"`
	BoundaryPending   bool                    `json:"boundary_pending"`
	BoundaryReason    string                  `json:"boundary_reason,omitempty"`
	PendingDirectives []BoundaryDirectiveView `json:"pending_directives"`
}

type BoundaryDirectiveView struct {
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

type SchedulerView struct {
	SchemaVersion int                 `json:"schema_version"`
	WorkspaceID   string              `json:"workspace_id"`
	Generation    string              `json:"generation"`
	JournalHead   string              `json:"journal_head"`
	Units         []SchedulerUnitView `json:"units"`
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
	SchemaVersion      int            `json:"schema_version"`
	WorkspaceID        string         `json:"workspace_id"`
	Generation         string         `json:"generation"`
	JournalHead        string         `json:"journal_head"`
	Units              []UnitGateView `json:"units"`
	Completion         GateCheckView  `json:"completion"`
	CompletionBlockers []string       `json:"completion_blockers"`
}

type WorkspaceWorkflowView struct {
	WorkspaceID            string `json:"workspace_id"`
	Generation             string `json:"generation"`
	JournalHead            string `json:"journal_head"`
	PlanCheckpoint         string `json:"plan_checkpoint"`
	WorktreeRoot           string `json:"worktree_root"`
	ProjectionDigest       string `json:"projection_digest"`
	ReviewProjectionDigest string `json:"review_projection_digest"`
}

type WorkspaceTargetView struct {
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

type WorkspaceAttemptView struct {
	AttemptID         string                  `json:"attempt_id"`
	PlanID            string                  `json:"plan_id"`
	MergeUnitID       string                  `json:"merge_unit_id"`
	Generation        string                  `json:"generation"`
	AttemptNumber     uint64                  `json:"attempt_number"`
	Base              string                  `json:"base"`
	Branch            string                  `json:"branch"`
	Worktree          string                  `json:"worktree"`
	Phase             AttemptRuntimePhase     `json:"phase"`
	Head              string                  `json:"head,omitempty"`
	GoalID            string                  `json:"goal_id"`
	GoalScope         GoalScope               `json:"goal_scope"`
	BoundaryPending   bool                    `json:"boundary_pending"`
	BoundaryReason    string                  `json:"boundary_reason,omitempty"`
	PendingDirectives []BoundaryDirectiveView `json:"pending_directives"`
}

type WorkspaceReviewView struct {
	AttemptID             string                `json:"attempt_id"`
	PlanID                string                `json:"plan_id"`
	MergeUnitID           string                `json:"merge_unit_id"`
	Generation            string                `json:"generation"`
	Head                  string                `json:"head"`
	Tree                  string                `json:"tree"`
	Status                string                `json:"status"`
	RoundsUsed            uint16                `json:"rounds_used"`
	FixesUsed             uint16                `json:"fixes_used"`
	InfrastructureRetries uint16                `json:"infrastructure_retries"`
	MergeReady            bool                  `json:"merge_ready"`
	Exhaustion            *ReviewExhaustionView `json:"exhaustion,omitempty"`
}

type ReviewExhaustionView struct {
	Reason                ReviewExhaustionReason        `json:"reason"`
	RoundsUsed            uint16                        `json:"rounds_used"`
	FixesUsed             uint16                        `json:"fixes_used"`
	InfrastructureRetries uint16                        `json:"infrastructure_retries"`
	Head                  string                        `json:"head"`
	Tree                  string                        `json:"tree"`
	Choices               []ReviewExhaustionOwnerChoice `json:"choices"`
}

type IntegrationUnitView struct {
	PlanID      string `json:"plan_id"`
	MergeUnitID string `json:"merge_unit_id"`
	AttemptID   string `json:"attempt_id,omitempty"`
	Head        string `json:"head,omitempty"`
	Status      string `json:"status"`
}

type IntegrationView struct {
	Units []IntegrationUnitView `json:"units"`
}

type DriftView struct {
	Detected bool     `json:"detected"`
	Reasons  []string `json:"reasons"`
}

type CompletionView struct {
	Complete     bool     `json:"complete"`
	Blockers     []string `json:"blockers"`
	ReportDigest string   `json:"report_digest,omitempty"`
}

type WorkspaceAbandonmentView struct {
	Reason      string `json:"reason"`
	FeatureRef  string `json:"feature_ref"`
	FeatureHead string `json:"feature_head,omitempty"`
	Record      uint64 `json:"record"`
	Released    bool   `json:"released"`
}

type WorkspaceReport struct {
	SchemaVersion int                       `json:"schema_version"`
	Workflow      WorkspaceWorkflowView     `json:"workflow"`
	Target        WorkspaceTargetView       `json:"target"`
	Attempts      []WorkspaceAttemptView    `json:"attempts"`
	Reviews       []WorkspaceReviewView     `json:"reviews"`
	Scheduler     SchedulerView             `json:"scheduler"`
	Gates         GateView                  `json:"gates"`
	Integration   IntegrationView           `json:"integration"`
	Drift         DriftView                 `json:"drift"`
	Completion    CompletionView            `json:"completion"`
	Abandonment   *WorkspaceAbandonmentView `json:"abandonment,omitempty"`
	ReportDigest  string                    `json:"report_digest"`
}

func RebuildSchedulerView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (SchedulerView, error) {
	core, reviews, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return SchedulerView{}, err
	}
	dependencies, references := definitionDependencyGraph(definition)
	attempts := latestAttemptsByMergeUnit(core)

	view := SchedulerView{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		Generation: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
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
			dependencyAttempt, completed := attempts[dependency.key()]
			if !completed ||
				dependencyAttempt.phase != AttemptCompleted {
				unit.Blockers = append(
					unit.Blockers,
					"dependency:"+dependency.String(),
				)
			}
		}
		attempt, hasAttempt := attempts[key]
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
				if state, exists := reviews.State(
					attempt.attemptID,
				); exists {
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
		view.Units = append(view.Units, unit)
	}
	return view, nil
}

func RebuildGateView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (GateView, error) {
	core, reviews, err := rebuildViewProjections(snapshot, definition)
	if err != nil {
		return GateView{}, err
	}
	scheduler, err := RebuildSchedulerView(snapshot, definition)
	if err != nil {
		return GateView{}, err
	}
	attempts := latestAttemptsByMergeUnit(core)
	unitExecution := unitExecutionsByMergeUnit(definition)
	view := GateView{
		SchemaVersion: JournalSchemaVersion, WorkspaceID: core.workspaceID.String(),
		Generation: core.activeGeneration.String(), JournalHead: snapshot.head.String(),
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
		if hasAttempt && attempt.phase.retryableTerminal() {
			hasAttempt = false
		}
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

		integrationGate := GateCheckView{Name: "integration", Generation: definition.generation.String()}
		if scheduled.Status == SchedulerUnitCompleted {
			integrationGate.Status, integrationGate.Reason = GatePassed, "merge_unit_integrated"
		} else {
			integrationGate.Status, integrationGate.Reason = GatePending, "not_integrated"
		}
		unit.Checks = append(unit.Checks, integrationGate)
		unit.MergeReady = dependencyGate.Status == GatePassed && commitGate.Status == GatePassed &&
			reviewGate.Status == GatePassed && integrationGate.Status != GatePassed
		view.Units = append(view.Units, unit)
	}
	blockers, complete, _, err := workspaceCompletionViewState(
		snapshot, definition, reviews, core,
	)
	if err != nil {
		return GateView{}, err
	}
	view.Completion = GateCheckView{
		Name:       "completion",
		Generation: definition.generation.String(),
	}
	view.CompletionBlockers = append(
		[]string{}, blockers...,
	)
	switch {
	case complete:
		view.Completion.Status = GatePassed
		view.Completion.Reason = "workspace_completed"
	case len(blockers) == 0:
		view.Completion.Status = GatePending
		view.Completion.Reason = "workspace_completion_not_recorded"
	default:
		if _, abandoned := core.Abandonment(); abandoned {
			view.Completion.Status = GateFailed
		} else if _, recorded := core.Completion(); recorded {
			view.Completion.Status = GateFailed
		} else {
			view.Completion.Status = GatePending
		}
		view.Completion.Reason = blockers[0]
	}
	return view, nil
}

func RebuildCompletionView(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (CompletionView, error) {
	core, reviews, err := rebuildViewProjections(
		snapshot, definition,
	)
	if err != nil {
		return CompletionView{}, err
	}
	blockers, complete, reportDigest, err :=
		workspaceCompletionViewState(
			snapshot, definition, reviews, core,
		)
	if err != nil {
		return CompletionView{}, err
	}
	return CompletionView{
		Complete:     complete,
		Blockers:     blockers,
		ReportDigest: reportDigest.String(),
	}, nil
}

func RebuildWorkspaceReport(snapshot JournalSnapshot, definition EffectiveWorkspaceDefinition) (WorkspaceReport, error) {
	core, reviews, err := rebuildViewProjections(snapshot, definition)
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
	scheduler, err := RebuildSchedulerView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	gates, err := RebuildGateView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	target, err := workspaceTargetView(core, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	attempts := workspaceAttemptViews(core, scheduler)
	reviewViews := workspaceReviewViews(reviews)
	integration := workspaceIntegrationView(scheduler)
	completion, err := RebuildCompletionView(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	report := WorkspaceReport{
		SchemaVersion: JournalSchemaVersion,
		Workflow: WorkspaceWorkflowView{
			WorkspaceID:            core.workspaceID.String(),
			Generation:             core.activeGeneration.String(),
			JournalHead:            snapshot.head.String(),
			PlanCheckpoint:         core.planCheckpoint.String(),
			WorktreeRoot:           core.worktreeRoot.Path(),
			ProjectionDigest:       coreDigest.String(),
			ReviewProjectionDigest: reviewDigest.String(),
		},
		Target:      target,
		Attempts:    attempts,
		Reviews:     reviewViews,
		Scheduler:   scheduler,
		Gates:       gates,
		Integration: integration,
		Drift:       DriftView{Reasons: []string{}},
		Completion:  completion,
	}
	if abandonment, exists := core.Abandonment(); exists {
		report.Abandonment = &WorkspaceAbandonmentView{
			Reason:      abandonment.Reason(),
			FeatureRef:  abandonment.FeatureRef(),
			FeatureHead: abandonment.FeatureHead().String(),
			Record:      abandonment.Record(),
			Released:    abandonment.Released(),
		}
	}
	if err := setWorkspaceReportDigest(&report); err != nil {
		return WorkspaceReport{}, err
	}
	return report, nil
}

func RebuildWorkspaceReportWithGit(
	ctx context.Context,
	snapshot JournalSnapshot,
	definition EffectiveWorkspaceDefinition,
	git IntegrationGitPort,
) (WorkspaceReport, error) {
	if ctx == nil || git == nil {
		return WorkspaceReport{}, fmt.Errorf(
			"Git-aware workspace report requires context and integration Git adapter",
		)
	}
	report, err := RebuildWorkspaceReport(snapshot, definition)
	if err != nil {
		return WorkspaceReport{}, err
	}
	core, reviews, err := rebuildViewProjections(
		snapshot, definition,
	)
	if err != nil {
		return WorkspaceReport{}, err
	}
	if _, abandoned := core.Abandonment(); abandoned {
		return report, nil
	}
	assessment := assessWorkspaceCompletion(
		snapshot, definition, reviews, core,
	)
	if len(assessment.chain) == 0 {
		return report, nil
	}
	target, exists := core.LocalTarget()
	if !exists || !target.Created() {
		return report, nil
	}
	if err := git.VerifyCompletedIntegration(
		ctx, target.binding, assessment.chain,
	); err == nil {
		return report, nil
	} else {
		report.Drift.Detected = true
		report.Drift.Reasons = []string{err.Error()}
		report.Completion.Complete = false
		report.Completion.Blockers =
			sortedUniqueCompletionBlockers(
				append(
					report.Completion.Blockers,
					"git:completed_integration_drift",
				),
			)
		report.Gates.CompletionBlockers = append(
			[]string{},
			report.Completion.Blockers...,
		)
		if _, recorded := core.Completion(); recorded {
			report.Gates.Completion.Status = GateFailed
		} else {
			report.Gates.Completion.Status = GatePending
		}
		report.Gates.Completion.Reason =
			"git:completed_integration_drift"
	}
	if err := setWorkspaceReportDigest(&report); err != nil {
		return WorkspaceReport{}, err
	}
	return report, nil
}

func setWorkspaceReportDigest(report *WorkspaceReport) error {
	if report == nil {
		return fmt.Errorf("workspace report is required")
	}
	report.ReportDigest = ""
	canonical, err := json.Marshal(report)
	if err != nil {
		return err
	}
	report.ReportDigest = DigestBytes(canonical).String()
	return nil
}

func workspaceTargetView(
	core WorkspaceRuntimeProjection,
	definition EffectiveWorkspaceDefinition,
) (WorkspaceTargetView, error) {
	target, exists := core.LocalTarget()
	if !exists {
		configured := definition.workspace.target
		if configured.IsZero() {
			return WorkspaceTargetView{}, fmt.Errorf(
				"workspace report requires a configured local target",
			)
		}
		return WorkspaceTargetView{
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
	return WorkspaceTargetView{
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
	scheduler SchedulerView,
) []WorkspaceAttemptView {
	scheduled := make(map[string]SchedulerUnitView, len(scheduler.Units))
	for _, unit := range scheduler.Units {
		if unit.AttemptID != "" {
			scheduled[unit.AttemptID] = unit
		}
	}
	result := make([]WorkspaceAttemptView, 0, len(core.attempts))
	for _, attempt := range core.attempts {
		unit := scheduled[attempt.attemptID.String()]
		result = append(result, WorkspaceAttemptView{
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
			PendingDirectives: append([]BoundaryDirectiveView(nil), unit.PendingDirectives...),
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
) []WorkspaceReviewView {
	states := reviews.States()
	result := make([]WorkspaceReviewView, 0, len(states))
	for _, state := range states {
		status := "active"
		if state.MergeReady() {
			status = "ready"
		} else if _, exhausted := state.Exhaustion(); exhausted {
			status = "exhausted"
		} else if _, pending := state.PendingFix(); pending {
			status = "fix_pending"
		}
		view := WorkspaceReviewView{
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
		}
		if exhaustion, exhausted := state.Exhaustion(); exhausted {
			view.Exhaustion = &ReviewExhaustionView{
				Reason:                exhaustion.Reason(),
				RoundsUsed:            exhaustion.RoundsUsed(),
				FixesUsed:             exhaustion.FixesUsed(),
				InfrastructureRetries: exhaustion.InfrastructureRetriesUsed(),
				Head:                  exhaustion.Head().String(),
				Tree:                  exhaustion.Tree().String(),
				Choices:               exhaustion.Choices(),
			}
		}
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].AttemptID < result[j].AttemptID
	})
	return result
}

func workspaceIntegrationView(
	scheduler SchedulerView,
) IntegrationView {
	view := IntegrationView{
		Units: make([]IntegrationUnitView, 0, len(scheduler.Units)),
	}
	for _, unit := range scheduler.Units {
		status := "pending"
		if unit.Status == SchedulerUnitCompleted {
			status = "integrated"
		}
		view.Units = append(view.Units, IntegrationUnitView{
			PlanID: unit.PlanID, MergeUnitID: unit.MergeUnitID,
			AttemptID: unit.AttemptID, Head: unit.Head, Status: status,
		})
	}
	return view
}

func attemptBoundaryStatus(
	core WorkspaceRuntimeProjection,
	attempt RuntimeAttemptProjection,
) (bool, string, []BoundaryDirectiveView) {
	directives := []BoundaryDirectiveView{}
	boundary, exists := attempt.CurrentBoundary()
	if !exists {
		return false, "", directives
	}
	view := func(kind string, goal GoalBinding, idempotency Digest, choices []string) BoundaryDirectiveView {
		return BoundaryDirectiveView{
			Kind: kind, BoundaryKind: string(boundary.kind), WorkspaceID: core.workspaceID.String(), Generation: attempt.generation.String(),
			AttemptID: attempt.attemptID.String(), BoundaryID: boundary.boundaryID.String(),
			GoalID: goal.id.String(), GoalScope: string(goal.scope), Head: boundary.head.String(),
			DirectiveDigest: boundary.directiveDigest.String(), IdempotencyKey: idempotency.String(),
			Choices: append([]string{}, choices...),
		}
	}
	if boundary.checkpoint == AttemptCheckpointCompleteGoalAndWait && !boundary.goalCompletedOK {
		return true, "complete_goal_and_wait", []BoundaryDirectiveView{
			view("complete_goal_and_wait", boundary.goal, boundary.idempotencyKey, nil),
		}
	}
	if !boundary.ownerResponseOK {
		return true, "owner_gate", []BoundaryDirectiveView{
			view("owner_gate", boundary.goal, Digest{}, []string{string(OwnerBoundaryContinue)}),
		}
	}
	if boundary.checkpoint == AttemptCheckpointCompleteGoalAndWait {
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
) (WorkspaceRuntimeProjection, ReviewRuntimeProjection, error) {
	core, err := RebuildWorkspaceRuntime(snapshot)
	if err != nil {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{}, err
	}
	if core.workspaceID != definition.workspace.id || core.activeGeneration != definition.generation {
		return WorkspaceRuntimeProjection{}, ReviewRuntimeProjection{},
			fmt.Errorf("workspace report definition does not match active journal generation")
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
