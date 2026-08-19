package workspace_test

import (
	"testing"
	"time"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func mustDefinition(t *testing.T, sources workspace.DefinitionSources) workspace.EffectiveWorkspaceDefinition {
	t.Helper()
	definition, err := workspace.ValidateDefinition(sources)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
