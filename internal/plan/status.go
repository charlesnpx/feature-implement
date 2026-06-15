package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type StatusResult struct {
	Status   string       `json:"status"`
	PlanDir  string       `json:"plan_dir"`
	LockPath string       `json:"lock_path,omitempty"`
	State    RuntimeState `json:"state,omitempty"`
}

func Status(planDir string) (StatusResult, error) {
	lockPath := filepath.Join(planDir, "feature.plan.lock.json")
	b, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return StatusResult{Status: "unvalidated", PlanDir: planDir}, nil
		}
		return StatusResult{}, err
	}
	var lock Lock
	if err := json.Unmarshal(b, &lock); err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Status: "validated", PlanDir: planDir, LockPath: lockPath, State: lock.State}, nil
}
