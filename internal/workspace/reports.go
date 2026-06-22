package workspace

import "sort"

const (
	ExternalIntentResultSourceUnresolved = "unresolved"
	ExternalIntentResultSourceOperator   = "operator"
	ExternalIntentResultSourceTool       = "tool"
	ExternalIntentResultSourcePolicy     = "policy"
	ExternalIntentResultSourceProvider   = "provider"

	ExternalIntentPurposeCompletion = "completion"
	ExternalIntentPurposeCleanup    = "cleanup"

	RecoveryActionRecoveredLease = "recovered_lease"
)

type WorkspaceBlockerGroup struct {
	Type           string                 `json:"type"`
	RequiredAction string                 `json:"required_action,omitempty"`
	Count          int                    `json:"count"`
	MergeUnits     []string               `json:"merge_units,omitempty"`
	Conditions     []WorkspaceBlockerView `json:"conditions,omitempty"`
}

type WorkspaceBlockerView struct {
	MergeUnitID    string `json:"merge_unit_id,omitempty"`
	Type           string `json:"type"`
	Resource       string `json:"resource"`
	AttemptID      string `json:"attempt_id,omitempty"`
	ContractID     string `json:"contract_id,omitempty"`
	ArtifactID     string `json:"artifact_id,omitempty"`
	IntentID       string `json:"intent_id,omitempty"`
	Action         string `json:"action,omitempty"`
	Target         string `json:"target,omitempty"`
	Status         string `json:"status,omitempty"`
	RequiredAction string `json:"required_action,omitempty"`
	EvidencePath   string `json:"evidence_path,omitempty"`
	Gate           string `json:"gate,omitempty"`
}

type ExternalIntentReport struct {
	IntentID       string `json:"intent_id"`
	MergeUnitID    string `json:"merge_unit_id"`
	AttemptID      string `json:"attempt_id"`
	Action         string `json:"action"`
	Purpose        string `json:"purpose"`
	Target         string `json:"target"`
	Status         string `json:"status"`
	ResultStatus   string `json:"result_status,omitempty"`
	ResultSource   string `json:"result_source"`
	Accepted       bool   `json:"accepted"`
	PolicyAccepted bool   `json:"policy_accepted,omitempty"`
	Operator       string `json:"operator,omitempty"`
	RequiredAction string `json:"required_action,omitempty"`
}

type RecoveryActionView struct {
	Type           string `json:"type"`
	MergeUnitID    string `json:"merge_unit_id,omitempty"`
	LeaseID        string `json:"lease_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	Status         string `json:"status"`
}

func workspaceBlockerGroups(view SchedulerView) []WorkspaceBlockerGroup {
	byKey := map[string]*WorkspaceBlockerGroup{}
	for _, unit := range view.MergeUnits {
		for _, condition := range unit.BlockingConditions {
			if condition.Type == "frozen_resource" {
				continue
			}
			addWorkspaceBlocker(byKey, unit.ID, condition)
		}
	}
	for _, entry := range view.MergeQueue {
		for _, condition := range entry.BlockingConditions {
			if condition.Type == "frozen_resource" {
				continue
			}
			addWorkspaceBlocker(byKey, entry.MergeUnitID, condition)
		}
	}
	for _, freeze := range view.FrozenResources {
		addWorkspaceBlocker(byKey, freeze.MergeUnitID, SchedulerBlockingCondition{
			Type:           "frozen_resource",
			Resource:       freeze.Resource,
			IntentID:       freeze.IntentID,
			AttemptID:      freeze.AttemptID,
			Action:         freeze.Action,
			Target:         freeze.Target,
			Status:         freeze.Status,
			RequiredAction: freeze.RequiredAction,
		})
	}
	groups := make([]WorkspaceBlockerGroup, 0, len(byKey))
	for _, group := range byKey {
		sort.Strings(group.MergeUnits)
		group.Count = len(group.Conditions)
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Type != groups[j].Type {
			return groups[i].Type < groups[j].Type
		}
		return groups[i].RequiredAction < groups[j].RequiredAction
	})
	return groups
}

func addWorkspaceBlocker(groups map[string]*WorkspaceBlockerGroup, mergeUnitID string, condition SchedulerBlockingCondition) {
	requiredAction := condition.RequiredAction
	if requiredAction == "" {
		requiredAction = defaultRequiredAction(condition.Type)
	}
	key := condition.Type + "\x00" + requiredAction
	group := groups[key]
	if group == nil {
		group = &WorkspaceBlockerGroup{
			Type:           condition.Type,
			RequiredAction: requiredAction,
		}
		groups[key] = group
	}
	if mergeUnitID != "" && !containsString(group.MergeUnits, mergeUnitID) {
		group.MergeUnits = append(group.MergeUnits, mergeUnitID)
	}
	for _, existing := range group.Conditions {
		if existing.MergeUnitID == mergeUnitID &&
			existing.Resource == condition.Resource &&
			existing.IntentID == condition.IntentID &&
			existing.AttemptID == condition.AttemptID &&
			existing.Gate == condition.Gate {
			return
		}
	}
	view := WorkspaceBlockerView{
		MergeUnitID:    mergeUnitID,
		Type:           condition.Type,
		Resource:       condition.Resource,
		AttemptID:      condition.AttemptID,
		ContractID:     condition.ContractID,
		ArtifactID:     condition.ArtifactID,
		IntentID:       condition.IntentID,
		Action:         condition.Action,
		Target:         condition.Target,
		Status:         condition.Status,
		RequiredAction: requiredAction,
		EvidencePath:   condition.EvidencePath,
		Gate:           condition.Gate,
	}
	group.Conditions = append(group.Conditions, view)
}

func defaultRequiredAction(conditionType string) string {
	switch conditionType {
	case "dependency":
		return "complete_dependencies"
	case "stale_contract":
		return "bind_contract"
	case "merge_approval", "stale_approval":
		return "grant_merge_approval"
	case "queue_position":
		return "wait_for_queue"
	default:
		return ""
	}
}

func externalIntentReports(events []JournalEvent, activeLeases map[string]activeLeaseSnapshot) ([]ExternalIntentReport, error) {
	snapshots, err := externalIntentSnapshots(events)
	if err != nil {
		return nil, err
	}
	reports := make([]ExternalIntentReport, 0, len(snapshots))
	for _, intent := range snapshots {
		status, requiredAction, frozen := intent.freezeState(activeLeases)
		report := ExternalIntentReport{
			IntentID:       intent.IntentID,
			MergeUnitID:    intent.MergeUnitID,
			AttemptID:      intent.AttemptID,
			Action:         intent.Action,
			Purpose:        externalIntentPurpose(intent.Action),
			Target:         intent.Target,
			Status:         intent.Status,
			ResultSource:   ExternalIntentResultSourceUnresolved,
			RequiredAction: requiredAction,
		}
		if frozen {
			report.Status = status
		}
		if intent.Result != nil {
			report.ResultStatus = intent.Result.Status
			report.ResultSource = externalIntentResultSource(*intent.Result)
			report.Accepted = intent.Result.Accepted
			report.PolicyAccepted = intent.Result.PolicyAccepted
			report.Operator = intent.Result.Operator
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].MergeUnitID != reports[j].MergeUnitID {
			return reports[i].MergeUnitID < reports[j].MergeUnitID
		}
		return reports[i].IntentID < reports[j].IntentID
	})
	return reports, nil
}

func externalIntentPurpose(action string) string {
	if action == ExternalActionRemoteDelete {
		return ExternalIntentPurposeCleanup
	}
	return ExternalIntentPurposeCompletion
}

func externalIntentResultSource(result ExternalIntentRecordedResultView) string {
	if result.Operator != "" || result.Status == ExternalResultReconciledByOperator {
		return ExternalIntentResultSourceOperator
	}
	if result.Status == ExternalResultSucceeded {
		return ExternalIntentResultSourceTool
	}
	if result.PolicyAccepted && result.Accepted {
		return ExternalIntentResultSourcePolicy
	}
	return ExternalIntentResultSourceProvider
}

func recoveryActionsFromLeases(recovered []RecoveredLeaseView) []RecoveryActionView {
	actions := make([]RecoveryActionView, 0, len(recovered))
	for _, lease := range recovered {
		actions = append(actions, RecoveryActionView{
			Type:           RecoveryActionRecoveredLease,
			MergeUnitID:    lease.MergeUnitID,
			LeaseID:        lease.LeaseID,
			AgentID:        lease.AgentID,
			LeaseExpiresAt: lease.LeaseExpiresAt,
			Status:         "recovered",
		})
	}
	return actions
}
