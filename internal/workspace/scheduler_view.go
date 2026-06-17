package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	SchedulerViewFileName = "scheduler.view.json"

	MergeUnitPending    = "pending"
	MergeUnitInProgress = "in_progress"
	MergeUnitCompleted  = "completed"
	MergeUnitFailed     = "failed"

	EventWorkspaceCreated      = "workspace.created"
	EventWorkspaceValidated    = "workspace.validated"
	EventMergeUnitStarted      = "merge_unit.started"
	EventMergeUnitCompleted    = "merge_unit.completed"
	EventMergeUnitFailed       = "merge_unit.failed"
	eventPayloadMergeUnitIDKey = "merge_unit_id"
)

type SchedulerView struct {
	SchemaVersion int                      `json:"schema_version"`
	WorkspaceID   string                   `json:"workspace_id"`
	BaseRef       string                   `json:"base_ref"`
	MergeUnits    []SchedulerMergeUnitView `json:"merge_units"`
	Counts        map[string]int           `json:"counts"`
	Ready         []string                 `json:"ready"`
	Blocked       []string                 `json:"blocked"`
	Leased        []string                 `json:"leased"`
}

type SchedulerMergeUnitView struct {
	ID             string                `json:"id"`
	PlanID         string                `json:"plan_id"`
	MergeUnitID    string                `json:"merge_unit_id"`
	StoryIDs       []string              `json:"story_ids"`
	Dependencies   []string              `json:"dependencies,omitempty"`
	Status         string                `json:"status"`
	BlockedBy      []string              `json:"blocked_by,omitempty"`
	ActiveLease    *SchedulerLeaseView   `json:"active_lease,omitempty"`
	CurrentAttempt *SchedulerAttemptView `json:"current_attempt,omitempty"`
}

type SchedulerLeaseView struct {
	LeaseID        string `json:"lease_id"`
	AgentID        string `json:"agent_id"`
	LeaseExpiresAt string `json:"lease_expires_at"`
}

type SchedulerAttemptView struct {
	AttemptID     string `json:"attempt_id"`
	AttemptNumber int    `json:"attempt_number"`
	AgentID       string `json:"agent_id"`
	LeaseID       string `json:"lease_id"`
	Branch        string `json:"branch"`
	Worktree      string `json:"worktree"`
	BaseRef       string `json:"base_ref"`
	BaseSHA       string `json:"base_sha"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
}

func SchedulerViewPath(workspaceDir string) string {
	return filepath.Join(StateDir(workspaceDir), SchedulerViewFileName)
}

func RebuildSchedulerView(workspaceDir string) (SchedulerView, error) {
	return rebuildSchedulerViewAt(workspaceDir, time.Now())
}

func rebuildSchedulerViewAt(workspaceDir string, now time.Time) (SchedulerView, error) {
	lock, err := readWorkspaceLock(filepath.Join(workspaceDir, LockFileName))
	if err != nil {
		return SchedulerView{}, err
	}
	events, err := readJournalEvents(EventsPath(workspaceDir))
	if err != nil {
		return SchedulerView{}, err
	}
	view, err := buildSchedulerViewAt(lock, events, now)
	if err != nil {
		return SchedulerView{}, err
	}
	if err := os.MkdirAll(StateDir(workspaceDir), 0o755); err != nil {
		return SchedulerView{}, err
	}
	if err := writeStableJSON(SchedulerViewPath(workspaceDir), view); err != nil {
		return SchedulerView{}, err
	}
	return view, nil
}

func BuildSchedulerView(lock WorkspaceLock, events []JournalEvent) (SchedulerView, error) {
	return buildSchedulerViewAt(lock, events, time.Now())
}

func buildSchedulerViewAt(lock WorkspaceLock, events []JournalEvent, now time.Time) (SchedulerView, error) {
	view := SchedulerView{
		SchemaVersion: 1,
		WorkspaceID:   lock.WorkspaceID,
		BaseRef:       lock.BaseRef,
		Counts:        map[string]int{},
	}
	for _, unit := range lock.MergeUnits {
		view.MergeUnits = append(view.MergeUnits, SchedulerMergeUnitView{
			ID:           unit.ID,
			PlanID:       unit.PlanID,
			MergeUnitID:  unit.MergeUnitID,
			StoryIDs:     append([]string(nil), unit.StoryIDs...),
			Dependencies: append([]string(nil), unit.Dependencies...),
			Status:       MergeUnitPending,
		})
	}
	unitByID := map[string]*SchedulerMergeUnitView{}
	for i := range view.MergeUnits {
		unitByID[view.MergeUnits[i].ID] = &view.MergeUnits[i]
	}
	attempts := newAttemptTracker()
	for _, event := range events {
		if err := applySchedulerEvent(unitByID, attempts, event); err != nil {
			return SchedulerView{}, err
		}
	}
	activeLeases, err := activeLeaseSnapshots(events, now)
	if err != nil {
		return SchedulerView{}, err
	}
	for i := range view.MergeUnits {
		unit := &view.MergeUnits[i]
		if lease, ok := activeLeases[unit.ID]; ok {
			unit.ActiveLease = &SchedulerLeaseView{
				LeaseID:        lease.LeaseID,
				AgentID:        lease.AgentID,
				LeaseExpiresAt: lease.LeaseExpiresAt.UTC().Format(time.RFC3339Nano),
			}
			view.Leased = append(view.Leased, unit.ID)
		}
		if attempt := attempts.Current(unit.ID); attempt != nil {
			unit.CurrentAttempt = &SchedulerAttemptView{
				AttemptID:     attempt.AttemptID,
				AttemptNumber: attempt.AttemptNumber,
				AgentID:       attempt.AgentID,
				LeaseID:       attempt.LeaseID,
				Branch:        attempt.Branch,
				Worktree:      attempt.Worktree,
				BaseRef:       attempt.BaseRef,
				BaseSHA:       attempt.BaseSHA,
				Mode:          attempt.Mode,
				Status:        attempt.Status,
			}
		}
		view.Counts[unit.Status]++
		if unit.Status == MergeUnitPending {
			unit.BlockedBy = incompleteDependencies(unit.Dependencies, unitByID)
			if unit.ActiveLease != nil {
				continue
			}
			if len(unit.BlockedBy) == 0 {
				view.Ready = append(view.Ready, unit.ID)
			} else {
				view.Blocked = append(view.Blocked, unit.ID)
			}
		}
	}
	sort.Strings(view.Ready)
	sort.Strings(view.Blocked)
	sort.Strings(view.Leased)
	ensureLifecycleCounts(view.Counts)
	return view, nil
}

func applySchedulerEvent(unitByID map[string]*SchedulerMergeUnitView, attempts *attemptTracker, event JournalEvent) error {
	switch event.Type {
	case EventWorkspaceCreated, EventWorkspaceValidated:
		return nil
	case EventLeaseGranted, EventLeaseHeartbeat, EventLeaseReleased, EventLeaseRecovered, EventAttemptStarted, EventAttemptAbandoned:
		return attempts.Apply(event)
	case EventMergeUnitStarted:
		return updateMergeUnitStatus(unitByID, attempts, event, MergeUnitInProgress)
	case EventMergeUnitCompleted:
		return updateMergeUnitStatus(unitByID, attempts, event, MergeUnitCompleted)
	case EventMergeUnitFailed:
		return updateMergeUnitStatus(unitByID, attempts, event, MergeUnitFailed)
	default:
		return fmt.Errorf("unknown scheduler event type %q", event.Type)
	}
}

func updateMergeUnitStatus(unitByID map[string]*SchedulerMergeUnitView, attempts *attemptTracker, event JournalEvent, status string) error {
	unitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return err
	}
	unit := unitByID[unitID]
	if unit == nil {
		return fmt.Errorf("scheduler event %s references unknown merge unit %s", event.ID, unitID)
	}
	if err := validateCurrentAttemptForTransition(event, attempts, unitID); err != nil {
		return err
	}
	unit.Status = status
	return nil
}

func validateCurrentAttemptForTransition(event JournalEvent, attempts *attemptTracker, mergeUnitID string) error {
	if !attempts.HasAny(mergeUnitID) {
		if _, ok := event.Payload[eventPayloadAttemptIDKey]; ok {
			attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
			if err != nil {
				return err
			}
			return fmt.Errorf("scheduler event %s references unknown attempt %s", event.ID, attemptID)
		}
		return nil
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return err
	}
	current := attempts.Current(mergeUnitID)
	if current == nil {
		return fmt.Errorf("scheduler event %s cannot advance merge unit %s without an active attempt", event.ID, mergeUnitID)
	}
	if attemptID != current.AttemptID {
		return fmt.Errorf("scheduler event %s attempt %s is not current active attempt %s", event.ID, attemptID, current.AttemptID)
	}
	return nil
}

func eventStringPayload(event JournalEvent, key string) (string, error) {
	value, ok := event.Payload[key]
	if !ok {
		return "", fmt.Errorf("scheduler event %s missing payload %s", event.ID, key)
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return "", fmt.Errorf("scheduler event %s payload %s must be a string", event.ID, key)
	}
	return text, nil
}

func incompleteDependencies(dependencies []string, unitByID map[string]*SchedulerMergeUnitView) []string {
	blockedBy := []string{}
	for _, dependency := range dependencies {
		unit := unitByID[dependency]
		if unit == nil || unit.Status != MergeUnitCompleted {
			blockedBy = append(blockedBy, dependency)
		}
	}
	sort.Strings(blockedBy)
	return blockedBy
}

func ensureLifecycleCounts(counts map[string]int) {
	for _, status := range []string{MergeUnitPending, MergeUnitInProgress, MergeUnitCompleted, MergeUnitFailed} {
		if _, ok := counts[status]; !ok {
			counts[status] = 0
		}
	}
}

func readWorkspaceLock(path string) (WorkspaceLock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return WorkspaceLock{}, err
	}
	var lock WorkspaceLock
	if err := json.Unmarshal(b, &lock); err != nil {
		return WorkspaceLock{}, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return lock, nil
}
