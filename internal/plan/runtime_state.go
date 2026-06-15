package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
)

const (
	runtimeStateSchemaVersion = 2

	MergeUnitPending   = "pending"
	MergeUnitStarted   = "started"
	MergeUnitCommitted = "committed"
	MergeUnitPushed    = "pushed"
	MergeUnitPROpen    = "pr_open"
	MergeUnitReviewed  = "reviewed"
	MergeUnitMerged    = "merged"
	MergeUnitCleaned   = "cleaned"
)

func (s *RuntimeState) UnmarshalJSON(data []byte) error {
	var raw struct {
		SchemaVersion int             `json:"schema_version"`
		MergeUnits    json.RawMessage `json:"merge_units"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.SchemaVersion = raw.SchemaVersion
	if len(raw.MergeUnits) == 0 || bytes.Equal(raw.MergeUnits, []byte("null")) {
		return nil
	}
	trimmed := bytes.TrimSpace(raw.MergeUnits)
	if len(trimmed) == 0 {
		return nil
	}
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &s.MergeUnits); err != nil {
			return err
		}
	case '{':
		legacy := map[string]MergeUnitState{}
		if err := json.Unmarshal(trimmed, &legacy); err != nil {
			return err
		}
		for id, state := range legacy {
			if state.ID == "" {
				state.ID = id
			}
			legacy[id] = state
		}
		s.legacyMergeUnit = legacy
	default:
		return fmt.Errorf("state.merge_units must be array or object")
	}
	return nil
}

func normalizeLockState(lock Lock) Lock {
	next := cloneLock(lock)
	byID := map[string]MergeUnitState{}
	for _, state := range lock.State.MergeUnits {
		if state.ID != "" {
			byID[state.ID] = state
		}
	}
	for id, state := range lock.State.legacyMergeUnit {
		if state.ID == "" {
			state.ID = id
		}
		byID[id] = state
	}
	next.State = RuntimeState{SchemaVersion: runtimeStateSchemaVersion}
	for _, unit := range next.MergeUnits {
		state := byID[unit.ID]
		state.ID = unit.ID
		if state.Status == "" {
			state.Status = MergeUnitPending
		}
		next.State.MergeUnits = append(next.State.MergeUnits, state)
	}
	return next
}

func cloneLock(lock Lock) Lock {
	next := lock
	next.Epics = cloneEpics(lock.Epics)
	next.MergeUnits = cloneMergeUnits(lock.MergeUnits)
	next.Files = append([]PlanFile(nil), lock.Files...)
	next.State = RuntimeState{
		SchemaVersion: lock.State.SchemaVersion,
		MergeUnits:    cloneMergeUnitStates(lock.State.MergeUnits),
	}
	if lock.State.legacyMergeUnit != nil {
		next.State.legacyMergeUnit = map[string]MergeUnitState{}
		for id, state := range lock.State.legacyMergeUnit {
			next.State.legacyMergeUnit[id] = state
		}
	}
	return next
}

func cloneEpics(values []Epic) []Epic {
	out := append([]Epic(nil), values...)
	for i := range out {
		out[i].Constraints = append([]string(nil), out[i].Constraints...)
		out[i].Features = cloneFeatures(out[i].Features)
	}
	return out
}

func cloneFeatures(values []Feature) []Feature {
	out := append([]Feature(nil), values...)
	for i := range out {
		out[i].Constraints = append([]string(nil), out[i].Constraints...)
		out[i].Stories = cloneStories(out[i].Stories)
	}
	return out
}

func cloneStories(values []Story) []Story {
	out := append([]Story(nil), values...)
	for i := range out {
		out[i].Acceptance = append([]string(nil), out[i].Acceptance...)
		out[i].Implementation = append([]string(nil), out[i].Implementation...)
		out[i].Testing = append([]string(nil), out[i].Testing...)
		out[i].Dependencies = append([]string(nil), out[i].Dependencies...)
	}
	return out
}

func cloneMergeUnits(values []MergeUnit) []MergeUnit {
	out := append([]MergeUnit(nil), values...)
	for i := range out {
		out[i].StoryIDs = append([]string(nil), out[i].StoryIDs...)
	}
	return out
}

func cloneMergeUnitStates(values []MergeUnitState) []MergeUnitState {
	return append([]MergeUnitState(nil), values...)
}

func nextMergeUnitID(lock Lock) string {
	lock = normalizeLockState(lock)
	for _, state := range lock.State.MergeUnits {
		if state.Status != MergeUnitCleaned {
			return state.ID
		}
	}
	return ""
}

func mergeUnitState(lock Lock, id string) (MergeUnitState, bool) {
	lock = normalizeLockState(lock)
	for _, state := range lock.State.MergeUnits {
		if state.ID == id {
			return state, true
		}
	}
	return MergeUnitState{}, false
}

func transitionMergeUnit(lock Lock, id string, action string, mutate func(*MergeUnitState)) (Lock, MergeUnitState, error) {
	next, index, current, err := validateMergeUnitTransition(lock, id, action)
	if err != nil {
		return Lock{}, MergeUnitState{}, err
	}
	updated := current
	mutate(&updated)
	next.State.MergeUnits[index] = updated
	return next, updated, nil
}

func validateMergeUnitTransition(lock Lock, id string, action string) (Lock, int, MergeUnitState, error) {
	next := normalizeLockState(lock)
	index := -1
	for i, state := range next.State.MergeUnits {
		if state.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Lock{}, -1, MergeUnitState{}, fmt.Errorf("unknown merge unit: %s", id)
	}
	nextID := nextMergeUnitID(next)
	if id != nextID {
		return Lock{}, -1, MergeUnitState{}, fmt.Errorf("cannot %s merge unit %s before %s", action, id, nextID)
	}
	current := next.State.MergeUnits[index]
	if err := validateTransition(current.Status, action); err != nil {
		return Lock{}, -1, MergeUnitState{}, err
	}
	return next, index, current, nil
}

func validateTransition(current string, action string) error {
	if current == "" {
		current = MergeUnitPending
	}
	want := map[string]string{
		"start":   MergeUnitPending,
		"commit":  MergeUnitStarted,
		"push":    MergeUnitCommitted,
		"open-pr": MergeUnitPushed,
		"review":  MergeUnitPROpen,
		"merge":   MergeUnitReviewed,
		"cleanup": MergeUnitMerged,
	}[action]
	if want == "" {
		return fmt.Errorf("unsupported state transition action: %s", action)
	}
	if current != want {
		return fmt.Errorf("cannot %s merge unit from status %s; expected %s", action, current, want)
	}
	return nil
}

func defaultBranchName(lock Lock, unitID string) string {
	if lock.ManifestID == "" {
		return "feature/" + unitID
	}
	return "feature/" + lock.ManifestID + "/" + unitID
}

func defaultWorktreePath(planDir, unitID string) string {
	return filepath.Join(planDir, "worktrees", unitID)
}
