package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type ImplementOptions struct {
	PlanDir           string
	Action            string
	MergeUnit         string
	AllowPush         bool
	AllowOpenPR       bool
	AllowMerge        bool
	AllowDeleteBranch bool
	WriteState        bool
	Branch            string
	Worktree          string
	BaseSHA           string
	CommitSHA         string
	PRNumber          int
	PRURL             string
	ReviewStatus      string
	MergeCommit       string
}

type ImplementResult struct {
	Status             string          `json:"status"`
	Action             string          `json:"action"`
	MergeUnit          string          `json:"merge_unit,omitempty"`
	StoryProgressLabel string          `json:"story_progress_label,omitempty"`
	Branch             string          `json:"branch,omitempty"`
	Worktree           string          `json:"worktree,omitempty"`
	State              *MergeUnitState `json:"state,omitempty"`
	Commands           []string        `json:"commands,omitempty"`
	Message            string          `json:"message,omitempty"`
}

func Implement(opts ImplementOptions) (ImplementResult, error) {
	lock, err := readLock(opts.PlanDir)
	if err != nil {
		return ImplementResult{}, err
	}
	lock = normalizeLockState(lock)
	unitID := opts.MergeUnit
	if unitID == "" {
		unitID = nextMergeUnitID(lock)
	}
	if unitID == "" {
		return ImplementResult{Status: "complete", Action: opts.Action, Message: "no pending merge units"}, nil
	}
	if !hasUnit(lock, unitID) {
		return ImplementResult{}, fmt.Errorf("unknown merge unit: %s", unitID)
	}
	progressLabel := storyProgressLabel(lock, unitID)
	if opts.Action != "next" {
		var err error
		lock, _, _, err = validateMergeUnitTransition(lock, unitID, opts.Action)
		if err != nil {
			return ImplementResult{}, err
		}
	}
	switch opts.Action {
	case "next":
		state, _ := mergeUnitState(lock, unitID)
		return ImplementResult{Status: "ready", Action: opts.Action, MergeUnit: unitID, StoryProgressLabel: progressLabel, State: &state}, nil
	case "start":
		branch := firstNonBlank(opts.Branch, defaultBranchName(lock, unitID))
		worktree := firstNonBlank(opts.Worktree, defaultWorktreePath(opts.PlanDir, unitID))
		baseRef := firstNonBlank(lock.BaseRef, "main")
		result := ImplementResult{
			Status:             "planned",
			Action:             opts.Action,
			MergeUnit:          unitID,
			StoryProgressLabel: progressLabel,
			Branch:             branch,
			Worktree:           worktree,
			Commands:           []string{fmt.Sprintf("git worktree add -b %s %s %s", shellQuote(branch), shellQuote(worktree), shellQuote(baseRef))},
			Message:            "runtime preflight and worktree creation are required before implementation; use --write-state after the worktree exists",
		}
		if opts.WriteState {
			if strings.TrimSpace(opts.BaseSHA) == "" {
				return ImplementResult{}, fmt.Errorf("start --write-state requires --base-sha")
			}
			return writeTransition(opts.PlanDir, lock, unitID, opts.Action, result, func(state *MergeUnitState) {
				state.Status = MergeUnitStarted
				state.Branch = branch
				state.Worktree = worktree
				state.BaseSHA = opts.BaseSHA
			})
		}
		return result, nil
	case "commit":
		state, _ := mergeUnitState(lock, unitID)
		worktree := firstNonBlank(state.Worktree, opts.Worktree, defaultWorktreePath(opts.PlanDir, unitID))
		result := ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, StoryProgressLabel: progressLabel, Commands: []string{
			fmt.Sprintf("git -C %s status --short", shellQuote(worktree)),
			fmt.Sprintf("git -C %s add .", shellQuote(worktree)),
			fmt.Sprintf("git -C %s commit", shellQuote(worktree)),
		}}
		if opts.WriteState {
			if strings.TrimSpace(opts.CommitSHA) == "" {
				return ImplementResult{}, fmt.Errorf("commit --write-state requires --commit-sha")
			}
			return writeTransition(opts.PlanDir, lock, unitID, opts.Action, result, func(state *MergeUnitState) {
				state.Status = MergeUnitCommitted
				state.CommitSHA = opts.CommitSHA
			})
		}
		return result, nil
	case "push":
		if !opts.AllowPush {
			return ImplementResult{}, fmt.Errorf("push requires --allow-push")
		}
		state, _ := mergeUnitState(lock, unitID)
		worktree := firstNonBlank(state.Worktree, opts.Worktree, defaultWorktreePath(opts.PlanDir, unitID))
		branch := firstNonBlank(state.Branch, opts.Branch, defaultBranchName(lock, unitID))
		remote := firstNonBlank(lock.Remote, "origin")
		result := ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, StoryProgressLabel: progressLabel, Commands: []string{fmt.Sprintf("git -C %s push -u %s %s", shellQuote(worktree), shellQuote(remote), shellQuote("HEAD:"+branch))}}
		if opts.WriteState {
			return writeTransition(opts.PlanDir, lock, unitID, opts.Action, result, func(state *MergeUnitState) {
				state.Status = MergeUnitPushed
			})
		}
		return result, nil
	case "open-pr":
		if !opts.AllowOpenPR {
			return ImplementResult{}, fmt.Errorf("open-pr requires --allow-open-pr")
		}
		state, _ := mergeUnitState(lock, unitID)
		worktree := firstNonBlank(state.Worktree, opts.Worktree, defaultWorktreePath(opts.PlanDir, unitID))
		branch := firstNonBlank(state.Branch, opts.Branch, defaultBranchName(lock, unitID))
		baseRef := firstNonBlank(lock.BaseRef, "main")
		result := ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, StoryProgressLabel: progressLabel, Commands: []string{fmt.Sprintf("cd %s && gh pr create --base %s --head %s", shellQuote(worktree), shellQuote(baseRef), shellQuote(branch))}}
		if opts.WriteState {
			if opts.PRNumber <= 0 {
				return ImplementResult{}, fmt.Errorf("open-pr --write-state requires --pr")
			}
			if strings.TrimSpace(opts.PRURL) == "" {
				return ImplementResult{}, fmt.Errorf("open-pr --write-state requires --pr-url")
			}
			return writeTransition(opts.PlanDir, lock, unitID, opts.Action, result, func(state *MergeUnitState) {
				state.Status = MergeUnitPROpen
				state.PRNumber = opts.PRNumber
				state.PRURL = opts.PRURL
			})
		}
		return result, nil
	case "review":
		result := ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, StoryProgressLabel: progressLabel, Commands: []string{"spawn PR review subagent", "apply useful findings"}}
		if opts.WriteState {
			if opts.ReviewStatus != "passed" && opts.ReviewStatus != "changes-applied" {
				return ImplementResult{}, fmt.Errorf("review --write-state requires --review-status passed|changes-applied")
			}
			return writeTransition(opts.PlanDir, lock, unitID, opts.Action, result, func(state *MergeUnitState) {
				state.Status = MergeUnitReviewed
				state.ReviewStatus = opts.ReviewStatus
			})
		}
		return result, nil
	case "merge":
		if !opts.AllowMerge {
			return ImplementResult{}, fmt.Errorf("merge requires --allow-merge")
		}
		state, _ := mergeUnitState(lock, unitID)
		prNumber := firstPositive(opts.PRNumber, state.PRNumber)
		prTarget := firstNonBlank(state.PRURL)
		if prTarget == "" && prNumber > 0 {
			prTarget = fmt.Sprintf("%d", prNumber)
		}
		if prTarget == "" {
			return ImplementResult{}, fmt.Errorf("merge requires recorded PR number or URL; run open-pr --write-state first")
		}
		result := ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, StoryProgressLabel: progressLabel, Commands: []string{fmt.Sprintf("gh pr merge %s --merge", shellQuote(prTarget))}}
		if opts.WriteState {
			if strings.TrimSpace(opts.MergeCommit) == "" {
				return ImplementResult{}, fmt.Errorf("merge --write-state requires --merge-commit")
			}
			return writeTransition(opts.PlanDir, lock, unitID, opts.Action, result, func(state *MergeUnitState) {
				state.Status = MergeUnitMerged
				state.MergeStatus = "merged"
				state.MergeCommit = opts.MergeCommit
			})
		}
		return result, nil
	case "cleanup":
		state, _ := mergeUnitState(lock, unitID)
		worktree := firstNonBlank(state.Worktree, opts.Worktree, defaultWorktreePath(opts.PlanDir, unitID))
		branch := firstNonBlank(state.Branch, opts.Branch, defaultBranchName(lock, unitID))
		remote := firstNonBlank(lock.Remote, "origin")
		result := ImplementResult{Status: "planned", Action: opts.Action, MergeUnit: unitID, StoryProgressLabel: progressLabel, Commands: []string{fmt.Sprintf("git worktree remove %s", shellQuote(worktree))}}
		if lock.MergePolicy.DeleteBranchAllowed && opts.AllowDeleteBranch {
			result.Commands = append(result.Commands, fmt.Sprintf("git push %s --delete %s", shellQuote(remote), shellQuote(branch)))
		}
		if opts.WriteState {
			cleanupStatus := "worktree-removed"
			if lock.MergePolicy.DeleteBranchAllowed && opts.AllowDeleteBranch {
				cleanupStatus = "worktree-removed-branch-deleted"
			}
			return writeTransition(opts.PlanDir, lock, unitID, opts.Action, result, func(state *MergeUnitState) {
				state.Status = MergeUnitCleaned
				state.CleanupStatus = cleanupStatus
			})
		}
		return result, nil
	default:
		return ImplementResult{}, fmt.Errorf("unsupported implement action: %s", opts.Action)
	}
}

func writeTransition(planDir string, lock Lock, unitID string, action string, result ImplementResult, mutate func(*MergeUnitState)) (ImplementResult, error) {
	next, state, err := transitionMergeUnit(lock, unitID, action, mutate)
	if err != nil {
		return ImplementResult{}, err
	}
	if err := writeLock(planDir, next); err != nil {
		return ImplementResult{}, err
	}
	result.Status = "recorded"
	result.State = &state
	return result, nil
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

func writeLock(planDir string, lock Lock) error {
	return writeJSON(filepath.Join(planDir, "feature.plan.lock.json"), normalizeLockState(lock))
}

func hasUnit(lock Lock, id string) bool {
	for _, unit := range lock.MergeUnits {
		if unit.ID == id {
			return true
		}
	}
	return false
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func storyProgressLabel(lock Lock, unitID string) string {
	storyIndex := map[string]int{}
	total := 0
	for _, epic := range lock.Epics {
		for _, feature := range epic.Features {
			for _, story := range feature.Stories {
				total++
				storyIndex[story.ID] = total
			}
		}
	}
	if total == 0 {
		return ""
	}
	var indexes []int
	for _, unit := range lock.MergeUnits {
		if unit.ID != unitID {
			continue
		}
		for _, storyID := range unit.StoryIDs {
			if index := storyIndex[storyID]; index > 0 {
				indexes = append(indexes, index)
			}
		}
		break
	}
	if len(indexes) == 0 {
		return ""
	}
	sort.Ints(indexes)
	if len(indexes) == 1 {
		return fmt.Sprintf("(Story %d/%d)", indexes[0], total)
	}
	if contiguous(indexes) {
		return fmt.Sprintf("(Stories %d-%d/%d)", indexes[0], indexes[len(indexes)-1], total)
	}
	parts := make([]string, 0, len(indexes))
	for _, index := range indexes {
		parts = append(parts, strconv.Itoa(index))
	}
	return fmt.Sprintf("(Stories %s/%d)", strings.Join(parts, ","), total)
}

func contiguous(values []int) bool {
	for i := 1; i < len(values); i++ {
		if values[i] != values[i-1]+1 {
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '/' ||
			r == '.' ||
			r == '_' ||
			r == '-' ||
			r == ':' ||
			r == '@' ||
			r == '+')
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
