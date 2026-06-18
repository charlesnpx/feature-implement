package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultLeaseDuration = 30 * time.Minute
	nextClaimMaxAttempts = 100

	EventLeaseGranted     = "lease.granted"
	EventLeaseHeartbeat   = "lease.heartbeat"
	EventLeaseReleased    = "lease.released"
	EventLeaseRecovered   = "lease.recovered"
	EventAttemptStarted   = "attempt.started"
	EventAttemptAbandoned = "attempt.abandoned"

	eventPayloadLeaseIDKey        = "lease_id"
	eventPayloadAgentIDKey        = "agent_id"
	eventPayloadLeaseExpiresAtKey = "lease_expires_at"
	eventPayloadAttemptIDKey      = "attempt_id"
	eventPayloadAttemptNumberKey  = "attempt_number"
	eventPayloadBranchKey         = "branch"
	eventPayloadWorktreeKey       = "worktree"
	eventPayloadBaseRefKey        = "base_ref"
	eventPayloadBaseSHAKey        = "base_sha"
	eventPayloadModeKey           = "mode"
	eventPayloadReasonKey         = "reason"
	eventPayloadStatusKey         = "status"
	eventPayloadFromKey           = "from"
	eventPayloadToKey             = "to"
	eventPayloadEvidenceKey       = "evidence"

	evidenceWorktreeKey  = "worktree"
	evidenceCommitSHAKey = "commit_sha"
	evidenceReasonKey    = "reason"

	attemptStatusActive    = "active"
	attemptStatusAbandoned = "abandoned"
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

type RecoverOptions struct {
	WorkspaceDir string
	Now          func() time.Time
}

type RecoverResult struct {
	Status         string               `json:"status"`
	WorkspaceDir   string               `json:"workspace_dir"`
	WorkspaceID    string               `json:"workspace_id"`
	BaseRef        string               `json:"base_ref"`
	ViewPath       string               `json:"view_path"`
	Recovered      []RecoveredLeaseView `json:"recovered"`
	RecoveredCount int                  `json:"recovered_count"`
	Ready          []string             `json:"ready"`
	Leased         []string             `json:"leased"`
	Counts         map[string]int       `json:"counts"`
}

type AttemptStartOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AgentID      string
	LeaseID      string
	BaseSHA      string
	Mode         string
	Now          func() time.Time
}

type AttemptAbandonOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	AgentID      string
	LeaseID      string
	Reason       string
	Now          func() time.Time
}

type TransitionOptions struct {
	WorkspaceDir string
	MergeUnitID  string
	AttemptID    string
	AgentID      string
	LeaseID      string
	From         string
	To           string
	Evidence     map[string]any
	Now          func() time.Time
}

type AttemptResult struct {
	Status        string   `json:"status"`
	WorkspaceDir  string   `json:"workspace_dir"`
	WorkspaceID   string   `json:"workspace_id"`
	BaseRef       string   `json:"base_ref"`
	MergeUnitID   string   `json:"merge_unit_id"`
	AttemptID     string   `json:"attempt_id"`
	AttemptNumber int      `json:"attempt_number"`
	AgentID       string   `json:"agent_id"`
	LeaseID       string   `json:"lease_id"`
	Branch        string   `json:"branch"`
	Worktree      string   `json:"worktree"`
	BaseSHA       string   `json:"base_sha"`
	Mode          string   `json:"mode"`
	Lifecycle     string   `json:"lifecycle"`
	Reason        string   `json:"reason,omitempty"`
	Commands      []string `json:"commands,omitempty"`
}

type TransitionResult struct {
	Status       string         `json:"status"`
	WorkspaceDir string         `json:"workspace_dir"`
	WorkspaceID  string         `json:"workspace_id"`
	BaseRef      string         `json:"base_ref"`
	MergeUnitID  string         `json:"merge_unit_id"`
	AttemptID    string         `json:"attempt_id"`
	AgentID      string         `json:"agent_id"`
	LeaseID      string         `json:"lease_id"`
	From         string         `json:"from"`
	To           string         `json:"to"`
	EventType    string         `json:"event_type"`
	Evidence     map[string]any `json:"evidence"`
}

type RecoveredLeaseView struct {
	MergeUnitID    string `json:"merge_unit_id"`
	LeaseID        string `json:"lease_id"`
	AgentID        string `json:"agent_id"`
	LeaseExpiresAt string `json:"lease_expires_at"`
}

type activeLeaseSnapshot struct {
	MergeUnitID    string
	LeaseID        string
	AgentID        string
	LeaseStartedAt time.Time
	LeaseExpiresAt time.Time
}

type attemptSnapshot struct {
	MergeUnitID   string
	AttemptID     string
	AttemptNumber int
	StartedAt     time.Time
	AgentID       string
	LeaseID       string
	Branch        string
	Worktree      string
	BaseRef       string
	BaseSHA       string
	Mode          string
	Status        string
	Reason        string
}

func Next(opts NextOptions) (NextResult, error) {
	opts, err := normalizeNextOptions(opts)
	if err != nil {
		return NextResult{}, err
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
	var lastRetryable error
	for attempt := 0; attempt < nextClaimMaxAttempts; attempt++ {
		result, err := nextOnce(opts, now(), leaseDuration)
		if err == nil {
			return result, nil
		}
		var retryable retryableClaimRaceError
		if !errors.As(err, &retryable) {
			return NextResult{}, err
		}
		lastRetryable = retryable.err
		delay := attempt + 1
		if delay > 10 {
			delay = 10
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	if lastRetryable != nil {
		return NextResult{}, fmt.Errorf("workspace next claim did not stabilize after %d attempts: %w", nextClaimMaxAttempts, lastRetryable)
	}
	return NextResult{}, fmt.Errorf("workspace next claim did not stabilize after %d attempts", nextClaimMaxAttempts)
}

func normalizeNextOptions(opts NextOptions) (NextOptions, error) {
	if opts.WorkspaceDir == "" {
		return NextOptions{}, fmt.Errorf("workspace next requires <workspace-dir>")
	}
	if !opts.Claim {
		return NextOptions{}, fmt.Errorf("workspace next currently requires --claim")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return NextOptions{}, fmt.Errorf("workspace next --claim requires --agent")
	}
	return opts, nil
}

func nextOnce(opts NextOptions, claimedAt time.Time, leaseDuration time.Duration) (NextResult, error) {
	lock, err := readWorkspaceLock(filepath.Join(opts.WorkspaceDir, LockFileName))
	if err != nil {
		return NextResult{}, err
	}
	events, err := readJournalEvents(EventsPath(opts.WorkspaceDir))
	if err != nil {
		return NextResult{}, err
	}
	claimedAt, err = observedAtAfterEvents(events, claimedAt)
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
		result, err := claimReadyMergeUnit(opts, view, *unit, claimedAt, leaseDuration, revisions)
		if err != nil && isRetryableClaimError(err) {
			return NextResult{}, retryableClaimRaceError{err: err}
		}
		return result, err
	}
	return NextResult{
		Status:       "none",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  view.WorkspaceID,
		BaseRef:      view.BaseRef,
	}, nil
}

type retryableClaimRaceError struct {
	err error
}

func (e retryableClaimRaceError) Error() string {
	return e.err.Error()
}

func (e retryableClaimRaceError) Unwrap() error {
	return e.err
}

func observedAtAfterEvents(events []JournalEvent, observedAt time.Time) (time.Time, error) {
	for _, event := range events {
		occurredAt, err := eventTimestamp(event)
		if err != nil {
			return time.Time{}, err
		}
		if observedAt.Before(occurredAt) {
			observedAt = occurredAt
		}
	}
	return observedAt, nil
}

func isRetryableClaimError(err error) bool {
	if err == nil {
		return false
	}
	var stale StaleResourceError
	if errors.As(err, &stale) {
		return true
	}
	return strings.Contains(err.Error(), "workspace journal lock is held")
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

func Recover(opts RecoverOptions) (RecoverResult, error) {
	if opts.WorkspaceDir == "" {
		return RecoverResult{}, fmt.Errorf("workspace recover requires <workspace-dir>")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	recoveredAt := now()
	state, err := loadLeaseOperationState(opts.WorkspaceDir, recoveredAt)
	if err != nil {
		return RecoverResult{}, err
	}
	expiredLeases, err := expiredLeaseSnapshots(state.Events, recoveredAt)
	if err != nil {
		return RecoverResult{}, err
	}
	recovered := []RecoveredLeaseView{}
	for _, lease := range expiredLeases {
		if err := appendLeaseEvent(opts.WorkspaceDir, EventLeaseRecovered, lease, "", state.Revisions, recoveredAt); err != nil {
			return RecoverResult{}, err
		}
		leaseResource := LeaseResource(lease.MergeUnitID)
		state.Revisions[leaseResource]++
		recovered = append(recovered, RecoveredLeaseView{
			MergeUnitID:    lease.MergeUnitID,
			LeaseID:        lease.LeaseID,
			AgentID:        lease.AgentID,
			LeaseExpiresAt: lease.LeaseExpiresAt.UTC().Format(time.RFC3339Nano),
		})
	}
	view, err := rebuildSchedulerViewAt(opts.WorkspaceDir, recoveredAt)
	if err != nil {
		return RecoverResult{}, err
	}
	status := "unchanged"
	if len(recovered) > 0 {
		status = "recovered"
	}
	return RecoverResult{
		Status:         status,
		WorkspaceDir:   opts.WorkspaceDir,
		WorkspaceID:    view.WorkspaceID,
		BaseRef:        view.BaseRef,
		ViewPath:       SchedulerViewPath(opts.WorkspaceDir),
		Recovered:      recovered,
		RecoveredCount: len(recovered),
		Ready:          append([]string{}, view.Ready...),
		Leased:         append([]string{}, view.Leased...),
		Counts:         cloneCounts(view.Counts),
	}, nil
}

func StartAttempt(opts AttemptStartOptions) (AttemptResult, error) {
	opts, startedAt, err := normalizeAttemptStartOptions(opts)
	if err != nil {
		return AttemptResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, startedAt)
	if err != nil {
		return AttemptResult{}, err
	}
	lease, unit, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return AttemptResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return AttemptResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return AttemptResult{}, err
	}
	nextNumber := nextAttemptNumber(attempts[opts.MergeUnitID])
	if current := currentAttempt(attempts[opts.MergeUnitID]); current != nil {
		return AttemptResult{}, fmt.Errorf("merge unit %s already has active attempt %s", opts.MergeUnitID, current.AttemptID)
	}
	attemptID := fmt.Sprintf("%s:attempt-%d", opts.MergeUnitID, nextNumber)
	branch := attemptBranchName(state.View.WorkspaceID, unit.PlanID, unit.MergeUnitID, nextNumber)
	worktree := attemptWorktreePath(opts.WorkspaceDir, state.View.WorkspaceID, unit.PlanID, unit.MergeUnitID, nextNumber)
	commands := attemptWorktreeCommands(branch, worktree, state.View.BaseRef)
	mergeUnitResource := MergeUnitResource(opts.MergeUnitID)
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventAttemptStarted,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey:   opts.MergeUnitID,
			eventPayloadAttemptIDKey:     attemptID,
			eventPayloadAttemptNumberKey: nextNumber,
			eventPayloadAgentIDKey:       opts.AgentID,
			eventPayloadLeaseIDKey:       opts.LeaseID,
			eventPayloadBranchKey:        branch,
			eventPayloadWorktreeKey:      worktree,
			eventPayloadBaseRefKey:       state.View.BaseRef,
			eventPayloadBaseSHAKey:       opts.BaseSHA,
			eventPayloadModeKey:          opts.Mode,
		},
		ReadSet: map[string]int{
			LeaseResource(opts.MergeUnitID): state.Revisions[LeaseResource(opts.MergeUnitID)],
			mergeUnitResource:               state.Revisions[mergeUnitResource],
		},
		WriteSet: []string{mergeUnitResource},
		Now:      func() time.Time { return startedAt },
	}); err != nil {
		return AttemptResult{}, err
	}
	return AttemptResult{
		Status:        "started",
		WorkspaceDir:  opts.WorkspaceDir,
		WorkspaceID:   state.View.WorkspaceID,
		BaseRef:       state.View.BaseRef,
		MergeUnitID:   opts.MergeUnitID,
		AttemptID:     attemptID,
		AttemptNumber: nextNumber,
		AgentID:       opts.AgentID,
		LeaseID:       opts.LeaseID,
		Branch:        branch,
		Worktree:      worktree,
		BaseSHA:       opts.BaseSHA,
		Mode:          opts.Mode,
		Lifecycle:     unit.Status,
		Commands:      commands,
	}, nil
}

func AbandonAttempt(opts AttemptAbandonOptions) (AttemptResult, error) {
	opts, abandonedAt, err := normalizeAttemptAbandonOptions(opts)
	if err != nil {
		return AttemptResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, abandonedAt)
	if err != nil {
		return AttemptResult{}, err
	}
	lease, unit, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return AttemptResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return AttemptResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return AttemptResult{}, err
	}
	current, err := requireCurrentAttempt(attempts, opts.MergeUnitID, opts.AttemptID)
	if err != nil {
		return AttemptResult{}, err
	}
	mergeUnitResource := MergeUnitResource(opts.MergeUnitID)
	if _, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: opts.WorkspaceDir,
		Type:         EventAttemptAbandoned,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: opts.MergeUnitID,
			eventPayloadAttemptIDKey:   opts.AttemptID,
			eventPayloadAgentIDKey:     opts.AgentID,
			eventPayloadLeaseIDKey:     opts.LeaseID,
			eventPayloadStatusKey:      attemptStatusAbandoned,
			eventPayloadReasonKey:      opts.Reason,
		},
		ReadSet: map[string]int{
			LeaseResource(opts.MergeUnitID): state.Revisions[LeaseResource(opts.MergeUnitID)],
			mergeUnitResource:               state.Revisions[mergeUnitResource],
		},
		WriteSet: []string{mergeUnitResource},
		Now:      func() time.Time { return abandonedAt },
	}); err != nil {
		return AttemptResult{}, err
	}
	return AttemptResult{
		Status:        attemptStatusAbandoned,
		WorkspaceDir:  opts.WorkspaceDir,
		WorkspaceID:   state.View.WorkspaceID,
		BaseRef:       state.View.BaseRef,
		MergeUnitID:   opts.MergeUnitID,
		AttemptID:     current.AttemptID,
		AttemptNumber: current.AttemptNumber,
		AgentID:       opts.AgentID,
		LeaseID:       opts.LeaseID,
		Branch:        current.Branch,
		Worktree:      current.Worktree,
		BaseSHA:       current.BaseSHA,
		Mode:          current.Mode,
		Lifecycle:     unit.Status,
		Reason:        opts.Reason,
	}, nil
}

func Transition(opts TransitionOptions) (TransitionResult, error) {
	opts, transitionedAt, err := normalizeTransitionOptions(opts)
	if err != nil {
		return TransitionResult{}, err
	}
	state, err := loadLeaseOperationState(opts.WorkspaceDir, transitionedAt)
	if err != nil {
		return TransitionResult{}, err
	}
	lease, unit, err := requireOwnedActiveLease(state, opts.LeaseID, opts.AgentID)
	if err != nil {
		return TransitionResult{}, err
	}
	if lease.MergeUnitID != opts.MergeUnitID {
		return TransitionResult{}, fmt.Errorf("lease %s is for merge unit %s, not %s", opts.LeaseID, lease.MergeUnitID, opts.MergeUnitID)
	}
	if unit.Status != opts.From {
		return TransitionResult{}, fmt.Errorf("merge unit %s lifecycle is %s, not %s", opts.MergeUnitID, unit.Status, opts.From)
	}
	attempts, err := attemptSnapshots(state.Events)
	if err != nil {
		return TransitionResult{}, err
	}
	current, err := requireCurrentAttemptAt(attempts, opts.MergeUnitID, opts.AttemptID, transitionedAt)
	if err != nil {
		return TransitionResult{}, err
	}
	if err := validateAttemptLeaseOwner(opts.AttemptID, current.AgentID, current.LeaseID, opts.AgentID, opts.LeaseID); err != nil {
		return TransitionResult{}, err
	}
	eventType, err := transitionEventType(opts.From, opts.To)
	if err != nil {
		return TransitionResult{}, err
	}
	evidence, err := normalizeTransitionEvidence(opts.From, opts.To, opts.Evidence, current)
	if err != nil {
		return TransitionResult{}, err
	}
	if err := appendTransitionEvent(opts.WorkspaceDir, opts, eventType, evidence, state.Revisions, transitionedAt); err != nil {
		return TransitionResult{}, err
	}
	return TransitionResult{
		Status:       "transitioned",
		WorkspaceDir: opts.WorkspaceDir,
		WorkspaceID:  state.View.WorkspaceID,
		BaseRef:      state.View.BaseRef,
		MergeUnitID:  opts.MergeUnitID,
		AttemptID:    opts.AttemptID,
		AgentID:      opts.AgentID,
		LeaseID:      opts.LeaseID,
		From:         opts.From,
		To:           opts.To,
		EventType:    eventType,
		Evidence:     evidence,
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
	for _, binding := range unit.ContractBindings {
		contractResource := ContractResource(binding.ContractID)
		bindingResource := ContractBindingResource(unit.ID, binding.ContractID, binding.ArtifactID)
		readSet[contractResource] = revisions[contractResource]
		readSet[bindingResource] = revisions[bindingResource]
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
	Events       []JournalEvent
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

func normalizeAttemptStartOptions(opts AttemptStartOptions) (AttemptStartOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return AttemptStartOptions{}, time.Time{}, fmt.Errorf("workspace attempt start requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return AttemptStartOptions{}, time.Time{}, fmt.Errorf("workspace attempt start requires --merge-unit")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return AttemptStartOptions{}, time.Time{}, fmt.Errorf("workspace attempt start requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return AttemptStartOptions{}, time.Time{}, fmt.Errorf("workspace attempt start requires --lease")
	}
	opts.BaseSHA = strings.TrimSpace(opts.BaseSHA)
	if opts.BaseSHA == "" {
		return AttemptStartOptions{}, time.Time{}, fmt.Errorf("workspace attempt start requires --base-sha")
	}
	opts.Mode = strings.TrimSpace(opts.Mode)
	if opts.Mode == "" {
		opts.Mode = "fresh-from-base"
	}
	if opts.Mode != "fresh-from-base" {
		return AttemptStartOptions{}, time.Time{}, fmt.Errorf("unsupported attempt start mode: %s", opts.Mode)
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func normalizeAttemptAbandonOptions(opts AttemptAbandonOptions) (AttemptAbandonOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return AttemptAbandonOptions{}, time.Time{}, fmt.Errorf("workspace attempt abandon requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return AttemptAbandonOptions{}, time.Time{}, fmt.Errorf("workspace attempt abandon requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return AttemptAbandonOptions{}, time.Time{}, fmt.Errorf("workspace attempt abandon requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return AttemptAbandonOptions{}, time.Time{}, fmt.Errorf("workspace attempt abandon requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return AttemptAbandonOptions{}, time.Time{}, fmt.Errorf("workspace attempt abandon requires --lease")
	}
	opts.Reason = strings.TrimSpace(opts.Reason)
	if opts.Reason == "" {
		return AttemptAbandonOptions{}, time.Time{}, fmt.Errorf("workspace attempt abandon requires --reason")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	return opts, now(), nil
}

func normalizeTransitionOptions(opts TransitionOptions) (TransitionOptions, time.Time, error) {
	if opts.WorkspaceDir == "" {
		return TransitionOptions{}, time.Time{}, fmt.Errorf("workspace transition requires <workspace-dir>")
	}
	opts.MergeUnitID = strings.TrimSpace(opts.MergeUnitID)
	if opts.MergeUnitID == "" {
		return TransitionOptions{}, time.Time{}, fmt.Errorf("workspace transition requires --merge-unit")
	}
	opts.AttemptID = strings.TrimSpace(opts.AttemptID)
	if opts.AttemptID == "" {
		return TransitionOptions{}, time.Time{}, fmt.Errorf("workspace transition requires --attempt")
	}
	opts.AgentID = strings.TrimSpace(opts.AgentID)
	if opts.AgentID == "" {
		return TransitionOptions{}, time.Time{}, fmt.Errorf("workspace transition requires --agent")
	}
	opts.LeaseID = strings.TrimSpace(opts.LeaseID)
	if opts.LeaseID == "" {
		return TransitionOptions{}, time.Time{}, fmt.Errorf("workspace transition requires --lease")
	}
	opts.From = strings.TrimSpace(opts.From)
	if opts.From == "" {
		return TransitionOptions{}, time.Time{}, fmt.Errorf("workspace transition requires --from")
	}
	opts.To = strings.TrimSpace(opts.To)
	if opts.To == "" {
		return TransitionOptions{}, time.Time{}, fmt.Errorf("workspace transition requires --to")
	}
	if opts.Evidence == nil {
		opts.Evidence = map[string]any{}
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
		Events:       events,
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

func requireCurrentAttempt(attempts map[string][]attemptSnapshot, mergeUnitID string, attemptID string) (attemptSnapshot, error) {
	current := currentAttempt(attempts[mergeUnitID])
	if current == nil {
		return attemptSnapshot{}, fmt.Errorf("merge unit %s has no active attempt", mergeUnitID)
	}
	if current.AttemptID != attemptID {
		return attemptSnapshot{}, fmt.Errorf("attempt %s is not current active attempt %s", attemptID, current.AttemptID)
	}
	return *current, nil
}

func requireCurrentAttemptAt(attempts map[string][]attemptSnapshot, mergeUnitID string, attemptID string, observedAt time.Time) (attemptSnapshot, error) {
	current, err := requireCurrentAttempt(attempts, mergeUnitID, attemptID)
	if err != nil {
		return attemptSnapshot{}, err
	}
	if observedAt.Before(current.StartedAt) {
		return attemptSnapshot{}, fmt.Errorf("attempt %s has not started yet", attemptID)
	}
	return current, nil
}

func validateAttemptLeaseOwner(attemptID string, attemptAgentID string, attemptLeaseID string, agentID string, leaseID string) error {
	if attemptLeaseID != leaseID {
		return fmt.Errorf("attempt %s was started under lease %s, not %s", attemptID, attemptLeaseID, leaseID)
	}
	if attemptAgentID != agentID {
		return fmt.Errorf("attempt %s is owned by agent %s, not %s", attemptID, attemptAgentID, agentID)
	}
	return nil
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

func appendTransitionEvent(workspaceDir string, opts TransitionOptions, eventType string, evidence map[string]any, revisions map[string]int, occurredAt time.Time) error {
	leaseResource := LeaseResource(opts.MergeUnitID)
	mergeUnitResource := MergeUnitResource(opts.MergeUnitID)
	_, err := AppendEvent(AppendEventOptions{
		WorkspaceDir: workspaceDir,
		Type:         eventType,
		Payload: map[string]any{
			eventPayloadMergeUnitIDKey: opts.MergeUnitID,
			eventPayloadAttemptIDKey:   opts.AttemptID,
			eventPayloadAgentIDKey:     opts.AgentID,
			eventPayloadLeaseIDKey:     opts.LeaseID,
			eventPayloadFromKey:        opts.From,
			eventPayloadToKey:          opts.To,
			eventPayloadEvidenceKey:    evidence,
		},
		ReadSet: map[string]int{
			leaseResource:     revisions[leaseResource],
			mergeUnitResource: revisions[mergeUnitResource],
		},
		WriteSet: []string{mergeUnitResource},
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
	leases, err := openLeaseSnapshots(events)
	if err != nil {
		return nil, err
	}
	active := map[string]activeLeaseSnapshot{}
	for mergeUnitID, lease := range leases {
		if !now.Before(lease.LeaseStartedAt) && now.Before(lease.LeaseExpiresAt) {
			active[mergeUnitID] = lease
		}
	}
	return active, nil
}

func expiredLeaseSnapshots(events []JournalEvent, now time.Time) ([]activeLeaseSnapshot, error) {
	leases, err := openLeaseSnapshots(events)
	if err != nil {
		return nil, err
	}
	expired := []activeLeaseSnapshot{}
	for _, lease := range leases {
		if !now.Before(lease.LeaseExpiresAt) {
			expired = append(expired, lease)
		}
	}
	sortLeaseSnapshots(expired)
	return expired, nil
}

func openLeaseSnapshots(events []JournalEvent) (map[string]activeLeaseSnapshot, error) {
	leases := map[string]activeLeaseSnapshot{}
	for _, event := range events {
		switch event.Type {
		case EventLeaseGranted, EventLeaseHeartbeat:
			lease, err := eventLeasePayload(event)
			if err != nil {
				return nil, err
			}
			leases[lease.MergeUnitID] = lease
		case EventLeaseReleased, EventLeaseRecovered:
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
	return leases, nil
}

func sortLeaseSnapshots(leases []activeLeaseSnapshot) {
	sort.Slice(leases, func(i, j int) bool {
		if leases[i].MergeUnitID != leases[j].MergeUnitID {
			return leases[i].MergeUnitID < leases[j].MergeUnitID
		}
		return leases[i].LeaseID < leases[j].LeaseID
	})
}

type attemptLocation struct {
	mergeUnitID string
	index       int
}

type attemptTracker struct {
	attempts map[string][]attemptSnapshot
	byID     map[string]attemptLocation
}

func newAttemptTracker() *attemptTracker {
	return &attemptTracker{
		attempts: map[string][]attemptSnapshot{},
		byID:     map[string]attemptLocation{},
	}
}

func attemptSnapshots(events []JournalEvent) (map[string][]attemptSnapshot, error) {
	tracker := newAttemptTracker()
	for _, event := range events {
		if err := tracker.Apply(event); err != nil {
			return nil, err
		}
	}
	return tracker.Snapshots(), nil
}

func (t *attemptTracker) Apply(event JournalEvent) error {
	switch event.Type {
	case EventAttemptStarted:
		return t.applyStarted(event)
	case EventAttemptAbandoned:
		return t.applyAbandoned(event)
	default:
		return nil
	}
}

func (t *attemptTracker) applyStarted(event JournalEvent) error {
	attempt, err := eventAttemptStartedPayload(event)
	if err != nil {
		return err
	}
	if _, exists := t.byID[attempt.AttemptID]; exists {
		return fmt.Errorf("scheduler event %s duplicates attempt %s", event.ID, attempt.AttemptID)
	}
	if current := t.Current(attempt.MergeUnitID); current != nil {
		return fmt.Errorf("scheduler event %s starts attempt %s while attempt %s is active", event.ID, attempt.AttemptID, current.AttemptID)
	}
	attempt.Status = attemptStatusActive
	t.attempts[attempt.MergeUnitID] = append(t.attempts[attempt.MergeUnitID], attempt)
	t.byID[attempt.AttemptID] = attemptLocation{mergeUnitID: attempt.MergeUnitID, index: len(t.attempts[attempt.MergeUnitID]) - 1}
	return nil
}

func (t *attemptTracker) applyAbandoned(event JournalEvent) error {
	abandoned, err := eventAttemptAbandonedPayload(event)
	if err != nil {
		return err
	}
	location, ok := t.byID[abandoned.AttemptID]
	if !ok || location.mergeUnitID != abandoned.MergeUnitID {
		return fmt.Errorf("scheduler event %s references unknown attempt %s", event.ID, abandoned.AttemptID)
	}
	current := t.Current(abandoned.MergeUnitID)
	if current == nil {
		return fmt.Errorf("scheduler event %s abandons attempt %s without an active attempt", event.ID, abandoned.AttemptID)
	}
	if current.AttemptID != abandoned.AttemptID {
		return fmt.Errorf("scheduler event %s abandons attempt %s but current active attempt is %s", event.ID, abandoned.AttemptID, current.AttemptID)
	}
	attempt := &t.attempts[location.mergeUnitID][location.index]
	attempt.Status = attemptStatusAbandoned
	attempt.Reason = abandoned.Reason
	return nil
}

func (t *attemptTracker) Current(mergeUnitID string) *attemptSnapshot {
	return currentAttempt(t.attempts[mergeUnitID])
}

func (t *attemptTracker) HasAny(mergeUnitID string) bool {
	return len(t.attempts[mergeUnitID]) > 0
}

func (t *attemptTracker) Snapshots() map[string][]attemptSnapshot {
	return t.attempts
}

func eventAttemptStartedPayload(event JournalEvent) (attemptSnapshot, error) {
	startedAt, err := eventTimestamp(event)
	if err != nil {
		return attemptSnapshot{}, err
	}
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	attemptNumber, err := eventIntPayload(event, eventPayloadAttemptNumberKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	agentID, err := eventStringPayload(event, eventPayloadAgentIDKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	leaseID, err := eventStringPayload(event, eventPayloadLeaseIDKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	branch, err := eventStringPayload(event, eventPayloadBranchKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	worktree, err := eventStringPayload(event, eventPayloadWorktreeKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	baseRef, err := eventStringPayload(event, eventPayloadBaseRefKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	baseSHA, err := eventStringPayload(event, eventPayloadBaseSHAKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	mode, err := eventStringPayload(event, eventPayloadModeKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	return attemptSnapshot{
		MergeUnitID:   mergeUnitID,
		AttemptID:     attemptID,
		AttemptNumber: attemptNumber,
		StartedAt:     startedAt,
		AgentID:       agentID,
		LeaseID:       leaseID,
		Branch:        branch,
		Worktree:      worktree,
		BaseRef:       baseRef,
		BaseSHA:       baseSHA,
		Mode:          mode,
	}, nil
}

func eventAttemptAbandonedPayload(event JournalEvent) (attemptSnapshot, error) {
	mergeUnitID, err := eventStringPayload(event, eventPayloadMergeUnitIDKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	attemptID, err := eventStringPayload(event, eventPayloadAttemptIDKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	status, err := eventStringPayload(event, eventPayloadStatusKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	if status != attemptStatusAbandoned {
		return attemptSnapshot{}, fmt.Errorf("scheduler event %s payload %s must be %q", event.ID, eventPayloadStatusKey, attemptStatusAbandoned)
	}
	reason, err := eventStringPayload(event, eventPayloadReasonKey)
	if err != nil {
		return attemptSnapshot{}, err
	}
	return attemptSnapshot{
		MergeUnitID: mergeUnitID,
		AttemptID:   attemptID,
		Status:      status,
		Reason:      reason,
	}, nil
}

func transitionEventType(from string, to string) (string, error) {
	switch {
	case from == MergeUnitPending && to == MergeUnitInProgress:
		return EventMergeUnitStarted, nil
	case from == MergeUnitInProgress && to == MergeUnitCompleted:
		return EventMergeUnitCompleted, nil
	case (from == MergeUnitPending || from == MergeUnitInProgress) && to == MergeUnitFailed:
		return EventMergeUnitFailed, nil
	default:
		return "", fmt.Errorf("unsupported workspace transition: %s -> %s", from, to)
	}
}

func normalizeTransitionEvidence(from string, to string, evidence map[string]any, attempt attemptSnapshot) (map[string]any, error) {
	normalized := map[string]any{}
	for key, value := range evidence {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("transition evidence key is required")
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("transition evidence %s must be a string", key)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, fmt.Errorf("transition evidence %s is required", key)
		}
		normalized[key] = text
	}
	switch to {
	case MergeUnitInProgress:
		worktree, err := requiredStringEvidence(normalized, evidenceWorktreeKey)
		if err != nil {
			return nil, err
		}
		if worktree != attempt.Worktree {
			return nil, fmt.Errorf("transition evidence worktree %s does not match current attempt worktree %s", worktree, attempt.Worktree)
		}
	case MergeUnitCompleted:
		if _, err := requiredStringEvidence(normalized, evidenceCommitSHAKey); err != nil {
			return nil, err
		}
	case MergeUnitFailed:
		if _, err := requiredStringEvidence(normalized, evidenceReasonKey); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported workspace transition: %s -> %s", from, to)
	}
	return normalized, nil
}

func requiredStringEvidence(evidence map[string]any, key string) (string, error) {
	value, ok := evidence[key]
	if !ok {
		return "", fmt.Errorf("transition evidence %s is required", key)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("transition evidence %s must be a string", key)
	}
	return text, nil
}

func eventTransitionPayload(event JournalEvent) (from string, to string, evidence map[string]any, ok bool, err error) {
	hasFrom := event.Payload[eventPayloadFromKey] != nil
	hasTo := event.Payload[eventPayloadToKey] != nil
	hasEvidence := event.Payload[eventPayloadEvidenceKey] != nil
	if !hasFrom && !hasTo && !hasEvidence {
		return "", "", nil, false, nil
	}
	from, err = eventStringPayload(event, eventPayloadFromKey)
	if err != nil {
		return "", "", nil, true, err
	}
	to, err = eventStringPayload(event, eventPayloadToKey)
	if err != nil {
		return "", "", nil, true, err
	}
	evidence, err = eventEvidencePayload(event)
	if err != nil {
		return "", "", nil, true, err
	}
	return from, to, evidence, true, nil
}

func eventEvidencePayload(event JournalEvent) (map[string]any, error) {
	value, ok := event.Payload[eventPayloadEvidenceKey]
	if !ok {
		return nil, fmt.Errorf("scheduler event %s missing payload %s", event.ID, eventPayloadEvidenceKey)
	}
	evidence, ok := value.(map[string]any)
	if !ok || len(evidence) == 0 {
		return nil, fmt.Errorf("scheduler event %s payload %s must be an object", event.ID, eventPayloadEvidenceKey)
	}
	return evidence, nil
}

func eventIntPayload(event JournalEvent, key string) (int, error) {
	value, ok := event.Payload[key]
	if !ok {
		return 0, fmt.Errorf("scheduler event %s missing payload %s", event.ID, key)
	}
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return typed, nil
		}
	case int64:
		if typed > 0 && typed <= int64(int(^uint(0)>>1)) {
			return int(typed), nil
		}
	case float64:
		asInt := int(typed)
		if typed > 0 && typed == float64(asInt) {
			return asInt, nil
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil && parsed > 0 && parsed <= int64(int(^uint(0)>>1)) {
			return int(parsed), nil
		}
	}
	return 0, fmt.Errorf("scheduler event %s payload %s must be a positive integer", event.ID, key)
}

func nextAttemptNumber(attempts []attemptSnapshot) int {
	next := 1
	for _, attempt := range attempts {
		if attempt.AttemptNumber >= next {
			next = attempt.AttemptNumber + 1
		}
	}
	return next
}

func currentAttempt(attempts []attemptSnapshot) *attemptSnapshot {
	var current *attemptSnapshot
	for i := range attempts {
		if attempts[i].Status != "active" {
			continue
		}
		if current == nil || attempts[i].AttemptNumber > current.AttemptNumber {
			current = &attempts[i]
		}
	}
	return current
}

func attemptBranchName(workspaceID string, planID string, mergeUnitID string, attemptNumber int) string {
	return fmt.Sprintf("feature/%s/%s/%s/attempt-%d", workspaceID, planID, mergeUnitID, attemptNumber)
}

func attemptWorktreePath(workspaceDir string, workspaceID string, planID string, mergeUnitID string, attemptNumber int) string {
	return filepath.Join(StateDir(workspaceDir), "worktrees", workspaceID, planID, mergeUnitID, fmt.Sprintf("attempt-%d", attemptNumber))
}

func attemptWorktreeCommands(branch string, worktree string, baseRef string) []string {
	return []string{fmt.Sprintf("git worktree add -b %s %s %s", shellQuote(branch), shellQuote(worktree), shellQuote(baseRef))}
}

func eventLeasePayload(event JournalEvent) (activeLeaseSnapshot, error) {
	lease, err := eventReleasedLeasePayload(event)
	if err != nil {
		return activeLeaseSnapshot{}, err
	}
	startedAt, err := eventTimestamp(event)
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
	lease.LeaseStartedAt = startedAt
	lease.LeaseExpiresAt = expiresAt
	return lease, nil
}

func eventTimestamp(event JournalEvent) (time.Time, error) {
	timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("scheduler event %s timestamp must be RFC3339Nano: %w", event.ID, err)
	}
	return timestamp, nil
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
