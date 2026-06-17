package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultLeaseDuration = 30 * time.Minute

	EventLeaseGranted   = "lease.granted"
	EventLeaseHeartbeat = "lease.heartbeat"
	EventLeaseReleased  = "lease.released"

	eventPayloadLeaseIDKey        = "lease_id"
	eventPayloadAgentIDKey        = "agent_id"
	eventPayloadLeaseExpiresAtKey = "lease_expires_at"
)

type NextOptions struct {
	WorkspaceDir  string
	AgentID       string
	Claim         bool
	LeaseDuration time.Duration
	Now           func() time.Time
}

type NextResult struct {
	Status         string `json:"status"`
	WorkspaceDir   string `json:"workspace_dir"`
	WorkspaceID    string `json:"workspace_id"`
	BaseRef        string `json:"base_ref"`
	MergeUnitID    string `json:"merge_unit_id,omitempty"`
	LeaseID        string `json:"lease_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	Lifecycle      string `json:"lifecycle,omitempty"`
}

type LeaseOptions struct {
	WorkspaceDir  string
	AgentID       string
	LeaseID       string
	LeaseDuration time.Duration
	Now           func() time.Time
}

type LeaseResult struct {
	Status         string `json:"status"`
	WorkspaceDir   string `json:"workspace_dir"`
	WorkspaceID    string `json:"workspace_id"`
	BaseRef        string `json:"base_ref"`
	MergeUnitID    string `json:"merge_unit_id"`
	LeaseID        string `json:"lease_id"`
	AgentID        string `json:"agent_id"`
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	Lifecycle      string `json:"lifecycle"`
}

type activeLeaseSnapshot struct {
	MergeUnitID    string
	LeaseID        string
	AgentID        string
	LeaseExpiresAt time.Time
}

func Next(opts NextOptions) (NextResult, error) {
	if opts.WorkspaceDir == "" {
		return NextResult{}, fmt.Errorf("workspace next requires <workspace-dir>")
	}
	if !opts.Claim {
		return NextResult{}, fmt.Errorf("workspace next currently requires --claim")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return NextResult{}, fmt.Errorf("workspace next --claim requires --agent")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	leaseDuration := opts.LeaseDuration
	if leaseDuration == 0 {
		leaseDuration = DefaultLeaseDuration
	}
	if leaseDuration < 0 {
		return NextResult{}, fmt.Errorf("lease duration must be non-negative")
	}
	claimedAt := now()

	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return NextResult{}, err
	}
	events, err := readJournalEvents(EventsPath(opts.WorkspaceDir))
	if err != nil {
		return NextResult{}, err
	}
	revisions, err := replayResourceRevisions(events)
	if err != nil {
		return NextResult{}, err
	}
	view, err := buildSchedulerViewAt(lock, events, claimedAt)
	if err != nil {
		return NextResult{}, err
	}
	unitByID := schedulerUnitByID(view)
	for _, mergeUnitID := range view.Ready {
		unit := unitByID[mergeUnitID]
		if unit == nil {
			return NextResult{}, fmt.Errorf("ready merge unit %s missing from scheduler view", mergeUnitID)
		}
		return claimReadyMergeUnit(opts, view, *unit, claimedAt, leaseDuration, revisions)
	}
	return NextResult{
		Status:       "none",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  view.WorkspaceID,
		BaseRef:      view.BaseRef,
	}, nil
}

func Heartbeat(opts LeaseOptions) (LeaseResult, error) {
	opts, observedAt, err := normalizeLeaseOptions("heartbeat", opts)
	if err != nil {
		return LeaseResult{}, err
	}
	leaseDuration := opts.LeaseDuration
	if leaseDuration == 0 {
		leaseDuration = DefaultLeaseDuration
	}
	if leaseDuration < 0 {
		return LeaseResult{}, fmt.Errorf("lease duration must be non-negative")
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, observedAt)
	if err != nil {
		return LeaseResult{}, err
	}
	lease, unit, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return LeaseResult{}, err
	}
	expiresAt := observedAt.Add(leaseDuration).UTC().Format(time.RFC3339Nano)
	if err := appendLeaseEvent(opts.WorkspaceDir, EventLeaseHeartbeat, lease, expiresAt, state.Revisions, observedAt); err != nil {
		return LeaseResult{}, err
	}
	return LeaseResult{
		Status:         "extended",
		WorkspaceDir:   opts.WorkspaceDir,
		WorkspaceID:    state.View.WorkspaceID,
		BaseRef:        state.View.BaseRef,
		MergeUnitID:    lease.MergeUnitID,
		LeaseID:        lease.LeaseID,
		AgentID:        lease.AgentID,
		LeaseExpiresAt: expiresAt,
		Lifecycle:      unit.Status,
	}, nil
}

func Release(opts LeaseOptions) (LeaseResult, error) {
	opts, observedAt, err := normalizeLeaseOptions("release", opts)
	if err != nil {
		return LeaseResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, observedAt)
	if err != nil {
		return LeaseResult{}, err
	}
	lease, unit, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return LeaseResult{}, err
	}
	if err := appendLeaseEvent(opts.WorkspaceDir, EventLeaseReleased, lease, "", state.Revisions, observedAt); err != nil {
		return LeaseResult{}, err
	}
	return LeaseResult{
		Status:       "released",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		MergeUnitID:  lease.MergeUnitID,
		LeaseID:      lease.LeaseID,
		AgentID:      lease.AgentID,
		Lifecycle:    unit.Status,
	}, nil
}

func claimReadyMergeUnit(opts NextOptions, view SchedulerView, unit SchedulerMergeUnitView, claimedAt time.Time, leaseDuration time.Duration, revisions map[string]int) (NextResult, error) {
	expiresAt := claimedAt.Add(leaseDuration).UTC().Format(time.RFC3339Nano)
	leaseID := fmt.Sprintf("%s:%s:%d", unit.ID, opts.AgentID, claimedAt.UTC().UnixNano())
	leaseResource := LeaseResource(unit.ID)
	mergeUnitResource := MergeUnitResource(unit.ID)
	readSet := map[string]int{
		leaseResource:     revisions[leaseResource],
		mergeUnitResource: revisions[mergeUnitResource],
	}
	for _, dependencyID := range unit.Dependencies {
		dependencyResource := MergeUnitResource(dependencyID)
		readSet[dependencyResource] = revisions[dependencyResource]
	}
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventLeaseGranted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:    unit.ID,
			eventPayloadLeaseIDKey:        leaseID,
			eventPayloadAgentIDKey:        opts.AgentID,
			eventPayloadLeaseExpiresAtKey: expiresAt,
		},
		ReadSet:  readSet,
		WriteSet: []string{leaseResource, mergeUnitResource},
		Now:      func() time.Time { return claimedAt },
	}); err != nil {
		return NextResult{}, err
	}
	return NextResult{
		Status:         "claimed",
		WorkspaceDir:   opts.WorkspaceDir,
		WorkspaceID:    view.WorkspaceID,
		BaseRef:        view.BaseRef,
		MergeUnitID:    unit.ID,
		LeaseID:        leaseID,
		AgentID:        opts.AgentID,
		LeaseExpiresAt: expiresAt,
		Lifecycle:      unit.Status,
	}, nil
}

type leaseOperationState struct {
	View         SchedulerView
	Revisions    map[string]int
	ActiveLeases map[string]activeLeaseSnapshot
	UnitByID     map[string]*SchedulerMergeUnitView
}

func normalizeLeaseOptions(action string, opts LeaseOptions) (LeaseOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return LeaseOptions{}, time.Time{}, fmt.Errorf("workspace %s requires <workspace-dir>", action)
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return LeaseOptions{}, time.Time{}, fmt.Errorf("workspace %s requires --agent", action)
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return LeaseOptions{}, time.Time{}, fmt.Errorf("workspace %s requires --lease", action)
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func loadLeaseOperationState(workspaceDir string, observedAt time.Time) (leaseOperationState, error) {
	lock, err := readWorkspaceLock(filepath.Join(workspaceDir, LockFileName))
	if err != nil {
		return leaseOperationState{}, err
	}
	events, err := readJournalEvents(EventsPath(workspaceDir))
	if err != nil {
		return leaseOperationState{}, err
	}
	revisions, err := replayResourceRevisions(events)
	if err != nil {
		return leaseOperationState{}, err
	}
	view, err := buildSchedulerViewAt(lock, events, observedAt)
	if err != nil {
		return leaseOperationState{}, err
	}
	activeLeases, err := activeLeaseSnapshots(events, observedAt)
	if err != nil {
		return leaseOperationState{}, err
	}
	return leaseOperationState{
		View:         view,
		Revisions:    revisions,
		ActiveLeases: activeLeases,
		UnitByID:     schedulerUnitByID(view),
	}, nil
}

func requireOwnedActiveLease(state leaseOperationState, leaseID string, agentID string) (activeLeaseSnapshot, SchedulerMergeUnitView, error) {
	for _, lease := range state.ActiveLeases {
		if lease.LeaseID != leaseID {
			continue
		}
		if lease.AgentID != agentID {
			return activeLeaseSnapshot{}, SchedulerMergeUnitView{}, fmt.Errorf("lease %s is owned by agent %s, not %s", leaseID, lease.AgentID, agentID)
		}
		unit := state.UnitByID[lease.MergeUnitID]
		if unit == nil {
			return activeLeaseSnapshot{}, SchedulerMergeUnitView{}, fmt.Errorf("lease %s references unknown merge unit %s", leaseID, lease.MergeUnitID)
		}
		return lease, *unit, nil
	}
	return activeLeaseSnapshot{}, SchedulerMergeUnitView{}, fmt.Errorf("active lease not found: %s", leaseID)
}

func appendLeaseEvent(workspaceDir string, eventType string, lease activeLeaseSnapshot, leaseExpiresAt string, revisions map[string]int, occurredAt time.Time) error {
	leaseResource := LeaseResource(lease.MergeUnitID)
	mergeUnitResource := MergeUnitResource(lease.MergeUnitID)
	payload := map[string]any{
		eventPayloadMergeUnitIDKey: lease.MergeUnitID,
		eventPayloadLeaseIDKey:     lease.LeaseID,
		eventPayloadAgentIDKey:     lease.AgentID,
	}
	if leaseExpiresAt != "" {
		payload[eventPayloadLeaseExpiresAtKey] = leaseExpiresAt
	}
	_, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         eventType,
		Payload:      payload,
		ReadSet: map[string]int{
			leaseResource:     revisions[leaseResource],
			mergeUnitResource: revisions[mergeUnitResource],
		},
		WriteSet: []string{leaseResource},
		Now:      func() time.Time { return occurredAt },
	})
	return err
}

func schedulerUnitByID(view SchedulerView) map[string]*SchedulerMergeUnitView {
	unitByID := map[string]*SchedulerMergeUnitView{}
	for i := range view.MergeUnits {
		unitByID[view.MergeUnits[i].ID] = &view.MergeUnits[i]
	}
	return unitByID
}

func activeLeaseSnapshots(events []JournalEvent, now time.Time) (map[string]activeLeaseSnapshot, error) {
	leases := map[string]activeLeaseSnapshot{}
	for _, event := range events {
		switch event.Type {
		case EventLeaseGranted, EventLeaseHeartbeat:
			lease, err := eventLeasePayload(event)
			if err != nil {
				return nil, err
			}
			leases[lease.MergeUnitID] = lease
		case EventLeaseReleased:
			lease, err := eventReleasedLeasePayload(event)
			if err != nil {
				return nil, err
			}
			current := leases[lease.MergeUnitID]
			if current.LeaseID == lease.LeaseID {
				delete(leases, lease.MergeUnitID)
			}
		default:
			continue
		}
	}
	active := map[string]activeLeaseSnapshot{}
	for mergeUnitID, lease := range leases {
		if now.Before(lease.LeaseExpiresAt) {
			active[mergeUnitID] = lease
		}
	}
	return active, nil
}

func eventLeasePayload(event JournalEvent) (activeLeaseSnapshot, error) {
	lease, err := eventReleasedLeasePayload(event)
	if err != nil {
		return activeLeaseSnapshot{}, err
	}
	expiresAtText, err := eventStringPayload(event, eventPayloadLeaseExpiresAtKey)
	if err != nil {
		return activeLeaseSnapshot{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
	if err != nil {
		return activeLeaseSnapshot{}, fmt.Errorf("scheduler event %s payload %s must be RFC3339Nano: %w", event.ID, eventPayloadLeaseExpiresAtKey, err)
	}
	lease.LeaseExpiresAt = expiresAt
	return lease, nil
}

func eventReleasedLeasePayload(event JournalEvent) (activeLeaseSnapshot, error) {
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return activeLeaseSnapshot{}, err
	}
	leaseID, err := eventStringPayload(event, eventPayloadLeaseIDKey)
	if err != nil {
		return activeLeaseSnapshot{}, err
	}
	agentID, err := eventStringPayload(event, eventPayloadAgentIDKey)
	if err != nil {
		return activeLeaseSnapshot{}, err
	}
	return activeLeaseSnapshot{
		MergeUnitID: mergeUnitID,
		LeaseID:     leaseID,
		AgentID:     agentID,
	}, nil
}
