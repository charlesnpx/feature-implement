package workspace

import (
	"reflect"
	"sort"
	"testing"
)

type workspaceDAGFixture struct {
	Name          string
	Workspace     workspaceFixture
	ExpectedReady []string
	Blocked       map[string][]string
}

func TestWorkspaceDAGFixturesExpectedReadyUnits(t *testing.T) {
	tests := []struct {
		name  string
		build func(*testing.T) workspaceDAGFixture
	}{
		{name: "independent", build: newIndependentDAGFixture},
		{name: "chained", build: newChainedDAGFixture},
		{name: "blocked", build: newBlockedDAGFixture},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := tt.build(t)
			second := tt.build(t)

			firstReady := readyMergeUnits(t, first.Workspace)
			secondReady := readyMergeUnits(t, second.Workspace)
			if !reflect.DeepEqual(firstReady, first.ExpectedReady) {
				t.Fatalf("ready units = %+v, want %+v", firstReady, first.ExpectedReady)
			}
			if !reflect.DeepEqual(secondReady, firstReady) {
				t.Fatalf("fixture is not stable across runs: first=%+v second=%+v", firstReady, secondReady)
			}
			for unitID, wantDeps := range first.Blocked {
				gotDeps := mergeUnitDependencies(t, first.Workspace, unitID)
				if !reflect.DeepEqual(gotDeps, wantDeps) {
					t.Fatalf("%s deps = %+v, want %+v", unitID, gotDeps, wantDeps)
				}
			}
		})
	}
}

func newIndependentDAGFixture(t *testing.T) workspaceDAGFixture {
	t.Helper()
	return workspaceDAGFixture{
		Name: "independent",
		Workspace: newWorkspaceFixture(t, workspaceFixtureSpec{
			ID:      "workspace-independent",
			BaseRef: fixtureWorkspaceBaseRef,
			Plans: []workspaceFixturePlan{
				{ID: "foundation", StoryID: "story-a"},
				{ID: "sources", StoryID: "story-b"},
			},
		}),
		ExpectedReady: []string{"foundation:story-a", "sources:story-b"},
		Blocked:       map[string][]string{},
	}
}

func newChainedDAGFixture(t *testing.T) workspaceDAGFixture {
	t.Helper()
	return workspaceDAGFixture{
		Name: "chained",
		Workspace: newWorkspaceFixture(t, workspaceFixtureSpec{
			ID:      "workspace-chained",
			BaseRef: fixtureWorkspaceBaseRef,
			Plans: []workspaceFixturePlan{
				{ID: "foundation", StoryID: "story-a"},
				{ID: "sources", StoryID: "story-b"},
			},
			Dependencies: []WorkspaceDependency{
				{Before: "foundation:story-a", After: "sources:story-b"},
			},
		}),
		ExpectedReady: []string{"foundation:story-a"},
		Blocked: map[string][]string{
			"sources:story-b": {"foundation:story-a"},
		},
	}
}

func newBlockedDAGFixture(t *testing.T) workspaceDAGFixture {
	t.Helper()
	return workspaceDAGFixture{
		Name: "blocked",
		Workspace: newWorkspaceFixture(t, workspaceFixtureSpec{
			ID:      "workspace-blocked",
			BaseRef: fixtureWorkspaceBaseRef,
			Plans: []workspaceFixturePlan{
				{
					ID: "foundation",
					Stories: []workspaceFixtureStory{
						{ID: "story-a"},
						{ID: "story-c", Dependencies: []string{"story-a"}},
					},
				},
				{ID: "sources", StoryID: "story-b"},
			},
			Dependencies: []WorkspaceDependency{
				{Before: "foundation:story-c", After: "sources:story-b"},
			},
		}),
		ExpectedReady: []string{"foundation:story-a"},
		Blocked: map[string][]string{
			"foundation:story-c": {"foundation:story-a"},
			"sources:story-b":    {"foundation:story-c"},
		},
	}
}

func readyMergeUnits(t *testing.T, fixture workspaceFixture) []string {
	t.Helper()
	result, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate DAG fixture: %v", err)
	}
	ready := []string{}
	for _, unit := range result.Lock.MergeUnits {
		if len(unit.Dependencies) == 0 {
			ready = append(ready, unit.ID)
		}
	}
	sort.Strings(ready)
	return ready
}

func mergeUnitDependencies(t *testing.T, fixture workspaceFixture, unitID string) []string {
	t.Helper()
	result, err := Validate(ValidateOptions{WorkspaceDir: fixture.Dir, WriteLock: true})
	if err != nil {
		t.Fatalf("Validate DAG fixture: %v", err)
	}
	for _, unit := range result.Lock.MergeUnits {
		if unit.ID == unitID {
			return unit.Dependencies
		}
	}
	t.Fatalf("merge unit %s not found", unitID)
	return nil
}
