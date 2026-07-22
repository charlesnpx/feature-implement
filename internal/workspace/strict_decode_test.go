package workspace_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestStrictWorkspaceDecoderRejectsNonV2AndAmbiguousYAML(t *testing.T) {
	fixture := newDefinitionFixture(t)
	valid := string(fixture.sources.Workspace.Bytes)
	tests := []struct {
		name    string
		source  string
		wantErr string
	}{
		{
			name:    "v1 artifact",
			source:  strings.Replace(valid, "schema_version: 2", "schema_version: 1", 1),
			wantErr: "v2 is required",
		},
		{
			name:    "duplicate field",
			source:  strings.Replace(valid, "id: example-workspace\n", "id: example-workspace\nid: duplicate\n", 1),
			wantErr: `duplicate field "id"`,
		},
		{
			name:    "unknown field",
			source:  strings.Replace(valid, "id: example-workspace\n", "id: example-workspace\nlegacy_status: active\n", 1),
			wantErr: "field legacy_status not found",
		},
		{
			name:    "nested unknown field",
			source:  strings.Replace(valid, "  kind: github\n", "  kind: github\n  token: forbidden\n", 1),
			wantErr: "field token not found",
		},
		{
			name:    "anchor",
			source:  strings.Replace(valid, "id: example-workspace", "id: &workspace example-workspace", 1),
			wantErr: "anchors and aliases are not supported",
		},
		{
			name:    "merge key",
			source:  strings.Replace(valid, "provider:\n  kind: github\n  repository: example/project", "provider:\n  <<: {kind: github, repository: example/project}", 1),
			wantErr: "YAML merge keys are not supported",
		},
		{
			name:    "timestamp scalar",
			source:  strings.Replace(valid, "remote: origin", "remote: 2026-07-21", 1),
			wantErr: `scalar tag "!!timestamp" is not supported`,
		},
		{
			name:    "null scalar",
			source:  strings.Replace(valid, "remote: origin", "remote: null", 1),
			wantErr: `scalar tag "!!null" is not supported`,
		},
		{
			name:    "boolean in string field",
			source:  strings.Replace(valid, "id: example-workspace", "id: true", 1),
			wantErr: "root.id must be a string",
		},
		{
			name:    "integer in string field",
			source:  strings.Replace(valid, "remote: origin", "remote: 7", 1),
			wantErr: "root.remote must be a string",
		},
		{
			name:    "local-only remote sentinel",
			source:  strings.Replace(valid, "remote: origin", "remote: local-only", 1),
			wantErr: "does not support local-only execution",
		},
		{
			name:    "local-only provider sentinel",
			source:  strings.Replace(valid, "repository: example/project", "repository: local-only", 1),
			wantErr: "does not support local-only execution",
		},
		{
			name:    "unsupported provider",
			source:  strings.Replace(valid, "kind: github", "kind: gitlab", 1),
			wantErr: "workspace v2 supports github",
		},
		{
			name:    "malformed github repository",
			source:  strings.Replace(valid, "repository: example/project", "repository: example/project/extra", 1),
			wantErr: "must be GitHub owner/repository",
		},
		{
			name:    "unsafe github repository",
			source:  strings.Replace(valid, "repository: example/project", "repository: example/project%2Fother", 1),
			wantErr: "must be GitHub owner/repository",
		},
		{
			name:    "noncanonical integer",
			source:  strings.Replace(valid, "schema_version: 2", "schema_version: 02", 1),
			wantErr: "must use unsigned decimal form",
		},
		{
			name:    "multiple documents",
			source:  valid + "---\nschema_version: 2\n",
			wantErr: "exactly one YAML document",
		},
		{
			name:    "implicit dependencies",
			source:  strings.Replace(valid, "dependencies: []\n", "", 1),
			wantErr: "dependencies must be explicit",
		},
		{
			name:    "implicit authorities",
			source:  strings.Split(valid, "authority_sources:")[0],
			wantErr: "authority_sources must be explicit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := workspace.DecodeWorkspaceManifest([]byte(test.source))
			assertErrorContains(t, err, test.wantErr)
		})
	}
}

func TestStrictDecodersBoundInputAndRejectV1PlanAuthority(t *testing.T) {
	tooLarge := bytes.Repeat([]byte{'x'}, workspace.MaxArtifactBytes+1)
	if _, err := workspace.DecodePlan(tooLarge); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized decode error = %v", err)
	}
	v1Plan := `schema_version: 1
id: legacy
title: Legacy
base_ref: main
remote: origin
stories: []
merge_units: []
`
	if _, err := workspace.DecodePlan([]byte(v1Plan)); err == nil || !strings.Contains(err.Error(), "v2 is required") {
		t.Fatalf("v1 decode error = %v", err)
	}
	v2WithWorkspaceAuthority := `schema_version: 2
id: misplaced-authority
title: Misplaced Authority
base_ref: main
stories: []
merge_units: []
`
	if _, err := workspace.DecodePlan([]byte(v2WithWorkspaceAuthority)); err == nil || !strings.Contains(err.Error(), "field base_ref not found") {
		t.Fatalf("misplaced plan authority error = %v", err)
	}
}

func TestExecutionPolicyRejectsImplicitOrWeakeningPrecedence(t *testing.T) {
	fixture := newDefinitionFixture(t)
	valid := string(fixture.sources.ExecutionConfig.Bytes)
	profileStart := strings.Index(valid, "  - id: standard\n")
	profileEnd := strings.Index(valid, "merge_units:\n")
	duplicateProfile := valid[:profileEnd] + valid[profileStart:profileEnd] + valid[profileEnd:]
	firstUnitStart := strings.Index(valid, "  - plan_id: alpha-plan\n")
	secondUnitStart := strings.Index(valid[firstUnitStart+1:], "  - plan_id: alpha-plan\n") + firstUnitStart + 1
	duplicateUnit := valid[:secondUnitStart] + valid[firstUnitStart:secondUnitStart] + valid[secondUnitStart:]
	tests := []struct {
		name    string
		source  string
		wantErr string
	}{
		{
			name:    "implicit field",
			source:  strings.Replace(valid, "  require_signed_receipts: true\n", "", 1),
			wantErr: "must explicitly define every policy field",
		},
		{
			name:    "legacy boolean spelling",
			source:  strings.Replace(valid, "  allow_write_network: false", "  allow_write_network: yes", 1),
			wantErr: "root.policy.allow_write_network must be a boolean",
		},
		{
			name:    "quoted boolean",
			source:  strings.Replace(valid, "  allow_write_network: false", `  allow_write_network: "false"`, 1),
			wantErr: "root.policy.allow_write_network must be a boolean",
		},
		{
			name:    "profile weakens maximum",
			source:  strings.Replace(valid, "      max_attempts: 4\n", "      max_attempts: 6\n", 1),
			wantErr: "profile standard policy weakens max_attempts",
		},
		{
			name:    "profile weakens requirement",
			source:  strings.Replace(valid, "      require_signed_receipts: true\n", "      require_signed_receipts: false\n", 1),
			wantErr: "profile standard policy weakens require_signed_receipts",
		},
		{
			name:    "unit weakens permission",
			source:  replaceNth(valid, "      allow_write_network: false", "      allow_write_network: true", 2),
			wantErr: "merge unit alpha-plan/unit-one policy weakens allow_write_network",
		},
		{
			name:    "unknown profile",
			source:  strings.Replace(valid, "    profile: standard\n", "    profile: missing\n", 1),
			wantErr: "references unknown profile missing",
		},
		{
			name:    "duplicate profile authority",
			source:  duplicateProfile,
			wantErr: "duplicate execution profile standard",
		},
		{
			name:    "duplicate merge-unit authority",
			source:  duplicateUnit,
			wantErr: "duplicate execution policy for merge unit alpha-plan/unit-one",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := workspace.DecodeExecutionConfig([]byte(test.source))
			assertErrorContains(t, err, test.wantErr)
		})
	}
}

func TestWorkspaceOwnsAndValidatesCrossPlanDependencies(t *testing.T) {
	root := t.TempDir()
	valid := `schema_version: 2
id: multi-plan
repository:
  root: ` + root + `
  identity: https://github.com/example/project.git
provider:
  kind: github
  repository: example/project
base_ref: feature/multi-plan
remote: origin
execution_config: config/execution.yaml
plans:
  - id: second-plan
    source: plans/second.yaml
  - id: first-plan
    source: plans/first.yaml
dependencies:
  - before:
      plan_id: first-plan
      merge_unit_id: first-unit
    after:
      plan_id: second-plan
      merge_unit_id: second-unit
authority_sources: []
`
	manifest, err := workspace.DecodeWorkspaceManifest([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Plans(); len(got) != 2 || got[0].ID().String() != "first-plan" || got[1].ID().String() != "second-plan" {
		t.Fatalf("normalized plan membership = %#v", got)
	}
	dependencies := manifest.Dependencies()
	if len(dependencies) != 1 ||
		dependencies[0].Before().PlanID().String() != "first-plan" || dependencies[0].Before().MergeUnitID().String() != "first-unit" ||
		dependencies[0].After().PlanID().String() != "second-plan" || dependencies[0].After().MergeUnitID().String() != "second-unit" {
		t.Fatalf("cross-plan dependencies = %#v", dependencies)
	}
	cycle := strings.Replace(valid, "authority_sources: []", "  - before:\n      plan_id: second-plan\n      merge_unit_id: second-unit\n    after:\n      plan_id: first-plan\n      merge_unit_id: first-unit\nauthority_sources: []", 1)
	if _, err := workspace.DecodeWorkspaceManifest([]byte(cycle)); err == nil || err.Error() != "workspace merge-unit dependency cycle includes first-plan\x00first-unit" {
		t.Fatalf("deterministic cycle error = %v", err)
	}
}

func TestDefinitionRejectsIncompleteOrContradictoryAuthority(t *testing.T) {
	fixture := newDefinitionFixture(t)
	tests := []struct {
		name    string
		mutate  func(*workspace.DefinitionSources)
		wantErr string
	}{
		{
			name: "missing merge unit policy",
			mutate: func(sources *workspace.DefinitionSources) {
				text := string(sources.ExecutionConfig.Bytes)
				marker := "  - plan_id: alpha-plan\n    merge_unit_id: unit-two\n"
				index := strings.Index(text, marker)
				sources.ExecutionConfig.Bytes = []byte(text[:index])
			},
			wantErr: "execution config covers 1 merge units; workspace plans require 2",
		},
		{
			name: "undeclared authority material",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.Authorities = append(sources.Authorities, workspace.AuthorityMaterial{
					ID: "extra-policy", Kind: workspace.AuthorityExternalDigest,
					Content: []byte("extra\n"), ExpectedSourceDigest: workspace.DigestBytes([]byte("extra\n")).String(),
				})
			},
			wantErr: "declares 2 authority sources but 3 materials were supplied",
		},
		{
			name: "duplicate authority material",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.Authorities = append(sources.Authorities, sources.Authorities[0])
			},
			wantErr: "duplicate authority material owner-policy",
		},
		{
			name: "external digest mismatch",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.Authorities[1].Content = []byte("changed\n")
			},
			wantErr: "content digest mismatch",
		},
		{
			name: "Git blob mismatch",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.Authorities[0].Content = []byte("changed\n")
			},
			wantErr: "content does not match pinned blob",
		},
		{
			name: "mixed Git object formats",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.Authorities[0].CommitObject = "sha256:" + strings.Repeat("1", 64)
			},
			wantErr: "commit and blob use different object formats",
		},
		{
			name: "wrong plan identity",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.Plans[0].Bytes = []byte(strings.Replace(string(sources.Plans[0].Bytes), "id: alpha-plan", "id: other-plan", 1))
			},
			wantErr: "declares id other-plan, expected alpha-plan",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := cloneDefinitionSources(fixture.sources)
			test.mutate(&sources)
			_, err := workspace.ValidateDefinition(sources)
			assertErrorContains(t, err, test.wantErr)
		})
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func replaceNth(source, old, replacement string, occurrence int) string {
	start := 0
	for current := 1; current <= occurrence; current++ {
		index := strings.Index(source[start:], old)
		if index < 0 {
			return source
		}
		index += start
		if current == occurrence {
			return source[:index] + replacement + source[index+len(old):]
		}
		start = index + len(old)
	}
	return source
}
