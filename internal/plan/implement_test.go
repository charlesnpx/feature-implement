package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImplementLifecycleRecordsStateAndAdvances(t *testing.T) {
	planDir := materializeExamplePlan(t)

	start, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true, BaseSHA: "base-sha"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if start.Status != "recorded" || start.State.Status != MergeUnitStarted {
		t.Fatalf("start result = %+v", start)
	}
	if start.Worktree != filepath.Join(planDir, "worktrees", "story-current-state") {
		t.Fatalf("worktree = %q", start.Worktree)
	}
	if start.Branch != "feature/sample-migration-plan/story-current-state" {
		t.Fatalf("branch = %q", start.Branch)
	}
	if start.StoryProgressLabel != "(Story 1/2)" {
		t.Fatalf("story progress = %q", start.StoryProgressLabel)
	}
	wantStartCommand := "git worktree add -b feature/sample-migration-plan/story-current-state " + filepath.Join(planDir, "worktrees", "story-current-state") + " main"
	if len(start.Commands) != 1 || start.Commands[0] != wantStartCommand {
		t.Fatalf("start commands = %#v", start.Commands)
	}

	steps := []ImplementOptions{
		{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state", WriteState: true, CommitSHA: "commit-sha"},
		{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", WriteState: true, AllowPush: true},
		{PlanDir: planDir, Action: "open-pr", MergeUnit: "story-current-state", WriteState: true, AllowOpenPR: true, PRNumber: 42, PRURL: "https://example.test/pr/42"},
		{PlanDir: planDir, Action: "review", MergeUnit: "story-current-state", WriteState: true, ReviewStatus: "changes-applied"},
		{PlanDir: planDir, Action: "merge", MergeUnit: "story-current-state", WriteState: true, AllowMerge: true, MergeCommit: "merge-sha"},
	}
	for _, step := range steps {
		if _, err := Implement(step); err != nil {
			t.Fatalf("%s: %v", step.Action, err)
		}
	}
	beforeCleanup, err := Implement(ImplementOptions{PlanDir: planDir, Action: "next"})
	if err != nil {
		t.Fatalf("next before cleanup: %v", err)
	}
	if beforeCleanup.MergeUnit != "story-current-state" || beforeCleanup.State.Status != MergeUnitMerged {
		t.Fatalf("next before cleanup = %+v", beforeCleanup)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "cleanup", MergeUnit: "story-current-state", WriteState: true}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	next, err := Implement(ImplementOptions{PlanDir: planDir, Action: "next"})
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if next.MergeUnit != "story-target-plan" || next.State.Status != MergeUnitPending {
		t.Fatalf("next = %+v", next)
	}
	if next.StoryProgressLabel != "(Story 2/2)" {
		t.Fatalf("next story progress = %q", next.StoryProgressLabel)
	}

	lock := readTestLock(t, planDir)
	if lock.State.SchemaVersion != runtimeStateSchemaVersion {
		t.Fatalf("schema = %d", lock.State.SchemaVersion)
	}
	if lock.State.MergeUnits[0].Status != MergeUnitCleaned {
		t.Fatalf("first state = %+v", lock.State.MergeUnits[0])
	}
	if lock.State.MergeUnits[0].PRNumber != 42 || lock.State.MergeUnits[0].PRURL == "" || lock.State.MergeUnits[0].MergeCommit != "merge-sha" {
		t.Fatalf("state metadata missing: %+v", lock.State.MergeUnits[0])
	}
}

func TestImplementNextReportsMultiStoryProgress(t *testing.T) {
	planDir := t.TempDir()
	lock := Lock{
		SchemaVersion: 1,
		ManifestID:    "multi-story",
		Title:         "Multi Story",
		Epics: []Epic{{
			ID:     "epic-a",
			Number: 1,
			Name:   "Epic A",
			Features: []Feature{{
				ID:     "feature-a",
				Number: 1,
				Name:   "Feature A",
				Stories: []Story{
					{ID: "story-a", Number: 1, Name: "Story A"},
					{ID: "story-b", Number: 2, Name: "Story B"},
					{ID: "story-c", Number: 3, Name: "Story C"},
				},
			}},
		}},
		MergeUnits: []MergeUnit{
			{ID: "unit-ab", Name: "Unit AB", StoryIDs: []string{"story-a", "story-b"}, AllowFeatureLevelPR: true},
			{ID: "unit-c", Name: "Unit C", StoryIDs: []string{"story-c"}},
		},
	}
	if err := writeLock(planDir, lock); err != nil {
		t.Fatal(err)
	}

	next, err := Implement(ImplementOptions{PlanDir: planDir, Action: "next"})
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if next.MergeUnit != "unit-ab" {
		t.Fatalf("merge unit = %q", next.MergeUnit)
	}
	if next.StoryProgressLabel != "(Stories 1-2/3)" {
		t.Fatalf("story progress = %q", next.StoryProgressLabel)
	}
}

func TestImplementRejectsInvalidLifecycleTransitions(t *testing.T) {
	planDir := materializeExamplePlan(t)
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", WriteState: true, AllowPush: true}); err == nil {
		t.Fatalf("push before start/commit should fail")
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "merge", MergeUnit: "story-current-state", AllowMerge: true}); err == nil {
		t.Fatalf("planned merge before review should fail")
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true}); err == nil {
		t.Fatalf("start without base SHA should fail")
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-target-plan", WriteState: true, BaseSHA: "base-sha"}); err == nil {
		t.Fatalf("starting a later merge unit before the next unit should fail")
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true, BaseSHA: "base-sha"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state", WriteState: true}); err == nil {
		t.Fatalf("commit without commit SHA should fail")
	}
}

func TestImplementWriteStateRequiresLifecycleMetadata(t *testing.T) {
	planDir := materializeExamplePlan(t)
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true}); err == nil {
		t.Fatalf("start without base SHA should fail")
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true, BaseSHA: "base-sha"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state", WriteState: true, CommitSHA: "commit-sha"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", WriteState: true, AllowPush: true}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "open-pr", MergeUnit: "story-current-state", WriteState: true, AllowOpenPR: true, PRNumber: 42}); err == nil {
		t.Fatalf("open-pr without PR URL should fail")
	}
}

func TestImplementPlansConcretePushCommand(t *testing.T) {
	planDir := materializeExamplePlan(t)
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true, BaseSHA: "base-sha"}); err != nil {
		t.Fatalf("start: %v", err)
	}

	commitPlan, err := Implement(ImplementOptions{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state"})
	if err != nil {
		t.Fatalf("commit plan: %v", err)
	}
	worktree := filepath.Join(planDir, "worktrees", "story-current-state")
	wantCommitCommands := []string{
		"git -C " + worktree + " status --short",
		"git -C " + worktree + " add .",
		"git -C " + worktree + " commit",
	}
	if strings.Join(commitPlan.Commands, "\n") != strings.Join(wantCommitCommands, "\n") {
		t.Fatalf("commit commands = %#v", commitPlan.Commands)
	}

	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state", WriteState: true, CommitSHA: "commit-sha"}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	push, err := Implement(ImplementOptions{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", AllowPush: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	want := "git -C " + worktree + " push -u origin HEAD:feature/sample-migration-plan/story-current-state"
	if len(push.Commands) != 1 || push.Commands[0] != want {
		t.Fatalf("push commands = %#v", push.Commands)
	}

	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", WriteState: true, AllowPush: true}); err != nil {
		t.Fatalf("record push: %v", err)
	}
	openPR, err := Implement(ImplementOptions{PlanDir: planDir, Action: "open-pr", MergeUnit: "story-current-state", AllowOpenPR: true})
	if err != nil {
		t.Fatalf("open-pr: %v", err)
	}
	wantOpenPR := "cd " + worktree + " && gh pr create --base main --head feature/sample-migration-plan/story-current-state"
	if len(openPR.Commands) != 1 || openPR.Commands[0] != wantOpenPR {
		t.Fatalf("open-pr commands = %#v", openPR.Commands)
	}
}

func TestImplementQuotesWorktreeCommandsWithSpaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plans with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	planDir := materializeExamplePlanAt(t, root)

	start, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	wantWorktree := filepath.Join(planDir, "worktrees", "story-current-state")
	wantStartCommand := "git worktree add -b feature/sample-migration-plan/story-current-state '" + wantWorktree + "' main"
	if len(start.Commands) != 1 || start.Commands[0] != wantStartCommand {
		t.Fatalf("start commands = %#v", start.Commands)
	}

	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true, BaseSHA: "base-sha"}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	commitPlan, err := Implement(ImplementOptions{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state"})
	if err != nil {
		t.Fatalf("commit plan: %v", err)
	}
	wantCommitCommands := []string{
		"git -C '" + wantWorktree + "' status --short",
		"git -C '" + wantWorktree + "' add .",
		"git -C '" + wantWorktree + "' commit",
	}
	if strings.Join(commitPlan.Commands, "\n") != strings.Join(wantCommitCommands, "\n") {
		t.Fatalf("commit commands = %#v", commitPlan.Commands)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state", WriteState: true, CommitSHA: "commit-sha"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	pushPlan, err := Implement(ImplementOptions{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", AllowPush: true})
	if err != nil {
		t.Fatalf("push plan: %v", err)
	}
	wantPushCommand := "git -C '" + wantWorktree + "' push -u origin HEAD:feature/sample-migration-plan/story-current-state"
	if len(pushPlan.Commands) != 1 || pushPlan.Commands[0] != wantPushCommand {
		t.Fatalf("push commands = %#v", pushPlan.Commands)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", WriteState: true, AllowPush: true}); err != nil {
		t.Fatalf("push: %v", err)
	}
	openPRPlan, err := Implement(ImplementOptions{PlanDir: planDir, Action: "open-pr", MergeUnit: "story-current-state", AllowOpenPR: true})
	if err != nil {
		t.Fatalf("open-pr plan: %v", err)
	}
	wantOpenPRCommand := "cd '" + wantWorktree + "' && gh pr create --base main --head feature/sample-migration-plan/story-current-state"
	if len(openPRPlan.Commands) != 1 || openPRPlan.Commands[0] != wantOpenPRCommand {
		t.Fatalf("open-pr commands = %#v", openPRPlan.Commands)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "open-pr", MergeUnit: "story-current-state", WriteState: true, AllowOpenPR: true, PRNumber: 42, PRURL: "https://example.test/pr/42"}); err != nil {
		t.Fatalf("open-pr: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "review", MergeUnit: "story-current-state", WriteState: true, ReviewStatus: "passed"}); err != nil {
		t.Fatalf("review: %v", err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "merge", MergeUnit: "story-current-state", WriteState: true, AllowMerge: true, MergeCommit: "merge-sha"}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	cleanup, err := Implement(ImplementOptions{PlanDir: planDir, Action: "cleanup", MergeUnit: "story-current-state"})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	wantCleanupCommand := "git worktree remove '" + wantWorktree + "'"
	if len(cleanup.Commands) != 1 || cleanup.Commands[0] != wantCleanupCommand {
		t.Fatalf("cleanup commands = %#v", cleanup.Commands)
	}
}

func TestImplementPlansConcreteMergeCommand(t *testing.T) {
	planDir := materializeExamplePlan(t)
	steps := []ImplementOptions{
		{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true, BaseSHA: "base-sha"},
		{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state", WriteState: true, CommitSHA: "commit-sha"},
		{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", WriteState: true, AllowPush: true},
		{PlanDir: planDir, Action: "open-pr", MergeUnit: "story-current-state", WriteState: true, AllowOpenPR: true, PRNumber: 42, PRURL: "https://example.test/pr/42"},
		{PlanDir: planDir, Action: "review", MergeUnit: "story-current-state", WriteState: true, ReviewStatus: "passed"},
	}
	for _, step := range steps {
		if _, err := Implement(step); err != nil {
			t.Fatalf("%s: %v", step.Action, err)
		}
	}

	merge, err := Implement(ImplementOptions{PlanDir: planDir, Action: "merge", MergeUnit: "story-current-state", AllowMerge: true})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merge.Commands) != 1 || merge.Commands[0] != "gh pr merge https://example.test/pr/42 --merge" {
		t.Fatalf("merge commands = %#v", merge.Commands)
	}
}

func TestImplementRejectsOutOfOrderWritesForAlreadyStartedUnit(t *testing.T) {
	planDir := materializeExamplePlan(t)
	lock := readTestLock(t, planDir)
	lock.State.MergeUnits[1].Status = MergeUnitStarted
	lock.State.MergeUnits[1].Branch = "feature/sample-migration-plan/story-target-plan"
	lock.State.MergeUnits[1].Worktree = filepath.Join(planDir, "worktrees", "story-target-plan")
	lock.State.MergeUnits[1].BaseSHA = "base-sha"
	if err := writeLock(planDir, lock); err != nil {
		t.Fatal(err)
	}

	if _, err := Implement(ImplementOptions{PlanDir: planDir, Action: "commit", MergeUnit: "story-target-plan", WriteState: true, CommitSHA: "commit-sha"}); err == nil {
		t.Fatalf("commit on later started unit should fail while an earlier unit is pending")
	}
}

func TestDefaultWorktreePathDoesNotEscapePlanWorktrees(t *testing.T) {
	planDir := t.TempDir()

	worktree := defaultWorktreePath(planDir, "../outside")

	want := filepath.Join(planDir, "worktrees", "outside")
	if worktree != want {
		t.Fatalf("worktree = %q, want %q", worktree, want)
	}
	rel, err := filepath.Rel(filepath.Join(planDir, "worktrees"), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("worktree escaped plan worktrees root: %q", worktree)
	}
}

func TestImplementMergeAndLocalCleanupDoNotRequireDeleteBranchApproval(t *testing.T) {
	planDir := materializeExamplePlan(t)
	lock := readTestLock(t, planDir)
	lock.MergePolicy.DeleteBranchAllowed = true
	if err := writeLock(planDir, lock); err != nil {
		t.Fatal(err)
	}

	steps := []ImplementOptions{
		{PlanDir: planDir, Action: "start", MergeUnit: "story-current-state", WriteState: true, BaseSHA: "base-sha"},
		{PlanDir: planDir, Action: "commit", MergeUnit: "story-current-state", WriteState: true, CommitSHA: "commit-sha"},
		{PlanDir: planDir, Action: "push", MergeUnit: "story-current-state", WriteState: true, AllowPush: true},
		{PlanDir: planDir, Action: "open-pr", MergeUnit: "story-current-state", WriteState: true, AllowOpenPR: true, PRNumber: 42, PRURL: "https://example.test/pr/42"},
		{PlanDir: planDir, Action: "review", MergeUnit: "story-current-state", WriteState: true, ReviewStatus: "passed"},
		{PlanDir: planDir, Action: "merge", MergeUnit: "story-current-state", WriteState: true, AllowMerge: true, MergeCommit: "merge-sha"},
	}
	for _, step := range steps {
		if _, err := Implement(step); err != nil {
			t.Fatalf("%s without delete-branch approval: %v", step.Action, err)
		}
	}

	plannedCleanup, err := Implement(ImplementOptions{PlanDir: planDir, Action: "cleanup", MergeUnit: "story-current-state", AllowDeleteBranch: true})
	if err != nil {
		t.Fatalf("planned cleanup with delete-branch approval: %v", err)
	}
	wantWorktreeCommand := "git worktree remove " + filepath.Join(planDir, "worktrees", "story-current-state")
	wantDeleteCommand := "git push origin --delete feature/sample-migration-plan/story-current-state"
	if len(plannedCleanup.Commands) != 2 || plannedCleanup.Commands[0] != wantWorktreeCommand || plannedCleanup.Commands[1] != wantDeleteCommand {
		t.Fatalf("planned cleanup commands = %#v", plannedCleanup.Commands)
	}

	cleanup, err := Implement(ImplementOptions{PlanDir: planDir, Action: "cleanup", MergeUnit: "story-current-state", WriteState: true})
	if err != nil {
		t.Fatalf("cleanup without delete-branch approval: %v", err)
	}
	if len(cleanup.Commands) != 1 || cleanup.Commands[0] != wantWorktreeCommand {
		t.Fatalf("cleanup commands = %#v", cleanup.Commands)
	}
}

func TestTransitionMergeUnitDoesNotMutateInputLock(t *testing.T) {
	lock := normalizeLockState(Lock{
		ManifestID: "sample",
		MergeUnits: []MergeUnit{
			{ID: "unit-a", Name: "Unit A", StoryIDs: []string{"story-a"}},
		},
		State: RuntimeState{SchemaVersion: runtimeStateSchemaVersion, MergeUnits: []MergeUnitState{
			{ID: "unit-a", Status: MergeUnitPending},
		}},
	})
	updated, state, err := transitionMergeUnit(lock, "unit-a", "start", func(state *MergeUnitState) {
		state.Status = MergeUnitStarted
		state.Branch = "feature/unit-a"
	})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if state.Status != MergeUnitStarted || updated.State.MergeUnits[0].Status != MergeUnitStarted {
		t.Fatalf("updated = %+v state=%+v", updated, state)
	}
	if lock.State.MergeUnits[0].Status != MergeUnitPending || lock.State.MergeUnits[0].Branch != "" {
		t.Fatalf("input lock mutated: %+v", lock.State.MergeUnits[0])
	}
}

func TestImplementMigratesLegacyMapStateOnWrite(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, "feature.plan.lock.json")
	legacy := `{
  "schema_version": 1,
  "manifest_id": "legacy",
  "title": "Legacy",
  "merge_units": [
    {"id": "unit-a", "name": "Unit A", "story_ids": ["story-a"]},
    {"id": "unit-b", "name": "Unit B", "story_ids": ["story-b"]}
  ],
  "state": {
    "schema_version": 1,
    "merge_units": {
      "unit-a": {"status": "pending"},
      "unit-b": {"status": "pending"}
    }
  }
}`
	if err := os.WriteFile(lockPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Implement(ImplementOptions{PlanDir: root, Action: "start", MergeUnit: "unit-a", WriteState: true, BaseSHA: "base-sha"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	var raw map[string]any
	b, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	state := raw["state"].(map[string]any)
	if _, ok := state["merge_units"].([]any); !ok {
		t.Fatalf("merge_units should migrate to array: %T", state["merge_units"])
	}
}

func materializeExamplePlan(t *testing.T) string {
	t.Helper()
	return materializeExamplePlanAt(t, t.TempDir())
}

func materializeExamplePlanAt(t *testing.T, root string) string {
	t.Helper()
	manifestPath := filepath.Join(root, "feature.plan.yaml")
	if err := os.WriteFile(manifestPath, []byte(ExampleManifestYAML()), 0o644); err != nil {
		t.Fatal(err)
	}
	materialized, err := Materialize(MaterializeOptions{ManifestPath: manifestPath, OutRoot: root})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if _, err := Validate(ValidateOptions{PlanDir: materialized.PlanDir, WriteLock: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return materialized.PlanDir
}

func readTestLock(t *testing.T, planDir string) Lock {
	t.Helper()
	lock, err := readLock(planDir)
	if err != nil {
		t.Fatal(err)
	}
	return normalizeLockState(lock)
}
