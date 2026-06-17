package workspace

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultLeaseDuration = 30 * time.Minute

	EventLeaseGranted = "lease.granted"

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

	view, err := RebuildSchedulerView(opts.WorkspaceDir)
	if err != nil {
		return NextResult{}, err
	}
	events, err := readJournalEvents(EventsPath(opts.WorkspaceDir))
	if err != nil {
		return NextResult{}, err
	}
	activeLeases, err := activeLeaseMergeUnits(events, claimedAt)
	if err != nil {
		return NextResult{}, err
	}
	unitByID := schedulerUnitByID(view)
	for _, mergeUnitID := range view.Ready {
		if activeLeases[mergeUnitID] {
			continue
		}
		unit := unitByID[mergeUnitID]
		if unit == nil {
			return NextResult{}, fmt.Errorf("ready merge unit %s missing from scheduler view", mergeUnitID)
		}
		return claimReadyMergeUnit(opts, view, *unit, claimedAt, leaseDuration)
	}
	return NextResult{
		Status:       "none",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  view.WorkspaceID,
		BaseRef:      view.BaseRef,
	}, nil
}

func claimReadyMergeUnit(opts NextOptions, view SchedulerView, unit SchedulerMergeUnitView, claimedAt time.Time, leaseDuration time.Duration) (NextResult, error) {
	expiresAt := claimedAt.Add(leaseDuration).UTC().Format(time.RFC3339Nano)
	leaseID := fmt.Sprintf("%s:%s:%d", unit.ID, opts.AgentID, claimedAt.UTC().UnixNano())
	leaseResource := LeaseResource(unit.ID)
	mergeUnitResource := MergeUnitResource(unit.ID)
	revisions, err := ResourceRevisions(opts.WorkspaceDir)
	if err != nil {
		return NextResult{}, err
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
		ReadSet: map[string]int{
			leaseResource:     revisions[leaseResource],
			mergeUnitResource: revisions[mergeUnitResource],
		},
		WriteSet: []string{leaseResource},
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

func schedulerUnitByID(view SchedulerView) map[string]*SchedulerMergeUnitView {
	unitByID := map[string]*SchedulerMergeUnitView{}
	for i := range view.MergeUnits {
		unitByID[view.MergeUnits[i].ID] = &view.MergeUnits[i]
	}
	return unitByID
}

func activeLeaseMergeUnits(events []JournalEvent, now time.Time) (map[string]bool, error) {
	active := map[string]bool{}
	for _, event := range events {
		if event.Type != EventLeaseGranted {
			continue
		}
		mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
		if err != nil {
			return nil, err
		}
		expiresAtText, err := eventStringPayload(event, eventPayloadLeaseExpiresAtKey)
		if err != nil {
			return nil, err
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
		if err != nil {
			return nil, fmt.Errorf("scheduler event %s payload %s must be RFC3339Nano: %w", event.ID, eventPayloadLeaseExpiresAtKey, err)
		}
		if now.Before(expiresAt) {
			active[mergeUnitID] = true
		}
	}
	return active, nil
}
