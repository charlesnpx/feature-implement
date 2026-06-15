package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ImplementOptions struct {
	PlanDir           string
	Action            string
	MergeUnit         string
	AllowPush         bool
	AllowOpenPR       bool
	AllowMerge        bool
	AllowDeleteBranch bool
}

type ImplementResult struct {
	Status    string   `json:"status"`
	Action    string   `json:"action"`
	MergeUnit string   `json:"merge_unit,omitempty"`
	Commands  []string `json:"commands,omitempty"`
	Message   string   `json:"message,omitempty"`
}

func Implement(opts ImplementOptions) (ImplementResult, error) {
	lock, err := readLock(opts.PlanDir)
	if err != nil {
		return ImplementResult{}, err
	}
	unitID := opts.MergeUnit
	if unitID == "" {
		unitID = nextPending(lock)
	}
	if unitID == "" {
		return ImplementResult{Status: "complete", Action: opts.Action, Message: "no pending merge units"}, nil
	}
	if !hasUnit(lock, unitID) {
		return ImplementResult{}, fmt.Errorf("unknown merge unit: %s", unitID)
	}
	switch opts.Action {
	case "next":
		return ImplementResult{Status: "ready", Action: opts.Action, MergeUnit: unitID}, nil
	case "start":
		return ImplementResult{
			Status:    "planned",
			Action:    opts.Action,
			MergeUnit: unitID,
			Commands:  []string{"git worktree add", "git checkout -b"},
			Message:   "runtime preflight and worktree creation are required before implementation",
		}, nil
	case "commit":
		return ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, Commands: []string{"git status --short", "git add", "git commit"}}, nil
	case "push":
		if !opts.AllowPush {
			return ImplementResult{}, fmt.Errorf("push requires --allow-push")
		}
		return ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, Commands: []string{"git push -u"}}, nil
	case "open-pr":
		if !opts.AllowOpenPR {
			return ImplementResult{}, fmt.Errorf("open-pr requires --allow-open-pr")
		}
		return ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, Commands: []string{"gh pr create"}}, nil
	case "merge":
		if !opts.AllowMerge {
			return ImplementResult{}, fmt.Errorf("merge requires --allow-merge")
		}
		if lock.MergePolicy.DeleteBranchAllowed && !opts.AllowDeleteBranch {
			return ImplementResult{}, fmt.Errorf("branch deletion requires --allow-delete-branch")
		}
		return ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, Commands: []string{"gh pr merge"}}, nil
	default:
		return ImplementResult{}, fmt.Errorf("unsupported implement action: %s", opts.Action)
	}
}

func readLock(planDir string) (Lock, error) {
	b, err := os.ReadFile(filepath.Join(planDir, "feature.plan.lock.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Lock{}, fmt.Errorf("validated lock is required; run feature validate <plan-dir> --write-lock")
		}
		return Lock{}, err
	}
	var lock Lock
	if err := json.Unmarshal(b, &lock); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

func nextPending(lock Lock) string {
	for _, unit := range lock.MergeUnits {
		state := lock.State.MergeUnits[unit.ID]
		if state.Status == "" || state.Status == "pending" {
			return unit.ID
		}
	}
	return ""
}

func hasUnit(lock Lock, id string) bool {
	for _, unit := range lock.MergeUnits {
		if unit.ID == id {
			return true
		}
	}
	return false
}
