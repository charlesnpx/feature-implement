package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type StatusResult struct {
	Status          string                     `json:"status"`
	WorkspaceDir    string                     `json:"workspace_dir"`
	LockPath        string                     `json:"lock_path"`
	ViewPath        string                     `json:"view_path"`
	WorkspaceID     string                     `json:"workspace_id"`
	BaseRef         string                     `json:"base_ref"`
	TotalMergeUnits int                        `json:"total_merge_units"`
	Counts          map[string]int             `json:"counts"`
	Ready           []string                   `json:"ready"`
	Blocked         []string                   `json:"blocked"`
	Blockers        []WorkspaceBlockerGroup    `json:"blockers,omitempty"`
	FrozenResources []ExternalIntentFreezeView `json:"frozen_resources,omitempty"`
	ExternalIntents []ExternalIntentReport     `json:"external_intents,omitempty"`
	MergeQueue      []MergeQueueEntryView      `json:"merge_queue,omitempty"`
	MergeUnits      []SchedulerMergeUnitView   `json:"merge_units"`
}

func Status(workspaceDir string) (StatusResult, error) {
	if workspaceDir == "" {
		return StatusResult{}, fmt.Errorf("workspace status requires <workspace-dir>")
	}
	lockPath := filepath.Join(workspaceDir, LockFileName)
	if _, err := os.Stat(lockPath); err != nil {
		if os.IsNotExist(err) {
			return StatusResult{}, fmt.Errorf("workspace lock missing: %s (run feature workspace validate <workspace-dir> --write-lock)", lockPath)
		}
		return StatusResult{}, err
	}

	observedAt := time.Now()
	view, err := rebuildSchedulerViewAt(workspaceDir, observedAt)
	if err != nil {
		return StatusResult{}, fmt.Errorf("workspace status: %w", err)
	}
	events, err := readJournalEvents(EventsPath(workspaceDir))
	if err != nil {
		return StatusResult{}, err
	}
	activeLeases, err := activeLeaseSnapshots(events, observedAt)
	if err != nil {
		return StatusResult{}, err
	}
	externalIntents, err := externalIntentReports(events, activeLeases)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{
		Status:          "ok",
		WorkspaceDir:    workspaceDir,
		LockPath:        lockPath,
		ViewPath:        SchedulerViewPath(workspaceDir),
		WorkspaceID:     view.WorkspaceID,
		BaseRef:         view.BaseRef,
		TotalMergeUnits: len(view.MergeUnits),
		Counts:          cloneCounts(view.Counts),
		Ready:           append([]string{}, view.Ready...),
		Blocked:         append([]string{}, view.Blocked...),
		Blockers:        workspaceBlockerGroups(view),
		FrozenResources: append([]ExternalIntentFreezeView{}, view.FrozenResources...),
		ExternalIntents: externalIntents,
		MergeQueue:      append([]MergeQueueEntryView{}, view.MergeQueue...),
		MergeUnits:      append([]SchedulerMergeUnitView{}, view.MergeUnits...),
	}, nil
}

func cloneCounts(counts map[string]int) map[string]int {
	cloned := map[string]int{}
	for key, value := range counts {
		cloned[key] = value
	}
	ensureLifecycleCounts(cloned)
	return cloned
}
