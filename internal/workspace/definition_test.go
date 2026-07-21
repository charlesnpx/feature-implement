package workspace_test

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

type definitionFixture struct {
	sources workspace.DefinitionSources
	gitData []byte
	extData []byte
}

func newDefinitionFixture(t *testing.T) definitionFixture {
	t.Helper()
	repositoryRoot := t.TempDir()
	gitData := []byte("owner-policy: strict\n")
	externalData := []byte("organization-policy: strict\n")
	workspaceYAML := fmt.Sprintf(`schema_version: 2
id: example-workspace
repository:
  root: %s
  identity: https://github.com/example/project.git
provider:
  kind: github
  repository: example/project
base_ref: feature/example-workspace
remote: origin
execution_config: config/execution.yaml
plans:
  - id: alpha-plan
    source: plans/alpha.yaml
dependencies: []
authority_sources:
  - id: owner-policy
    kind: git_blob
    location: policy/owner.yaml
  - id: organization-policy
    kind: external_digest
    location: https://policy.example/v2
`, repositoryRoot)
	planYAML := `schema_version: 2
id: alpha-plan
title: Alpha Plan
stories:
  - id: story-one
    summary: Establish the first contract.
    acceptance:
      - The first contract is explicit.
    implementation:
      - Implement the first contract.
    testing:
      - Test the first contract.
    dependencies: []
  - id: story-two
    summary: Establish the dependent contract.
    acceptance:
      - The dependent contract is explicit.
    implementation:
      - Implement the dependent contract.
    testing:
      - Test the dependent contract.
    dependencies:
      - story-one
merge_units:
  - id: unit-one
    name: Unit One
    story_ids:
      - story-one
  - id: unit-two
    name: Unit Two
    story_ids:
      - story-two
`
	executionYAML := `schema_version: 2
policy:
  require_passing_checks: true
  require_signed_receipts: true
  allow_write_network: false
  max_attempts: 5
  max_review_rounds: 4
  max_review_fixes: 3
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 4
      max_review_rounds: 3
      max_review_fixes: 3
merge_units:
  - plan_id: alpha-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      mode: pause_only
      serial_segment: serial-alpha
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
  - plan_id: alpha-plan
    merge_unit_id: unit-two
    profile: standard
    boundary:
      mode: complete_goal_and_wait
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 2
`
	return definitionFixture{
		gitData: gitData,
		extData: externalData,
		sources: workspace.DefinitionSources{
			Workspace:       workspace.SourceArtifact{Path: "feature.workspace.yaml", Bytes: []byte(workspaceYAML)},
			Plans:           []workspace.SourceArtifact{{Path: "plans/alpha.yaml", Bytes: []byte(planYAML)}},
			ExecutionConfig: workspace.SourceArtifact{Path: "config/execution.yaml", Bytes: []byte(executionYAML)},
			Authorities: []workspace.AuthorityMaterial{
				{
					ID: "owner-policy", Kind: workspace.AuthorityGitBlob, Content: gitData,
					RepositoryIdentity: "https://github.com/example/policy.git",
					CommitObject:       "sha1:" + strings.Repeat("1", 40),
					BlobObject:         gitSHA1Blob(gitData),
				},
				{
					ID: "organization-policy", Kind: workspace.AuthorityExternalDigest, Content: externalData,
					ExpectedSourceDigest: workspace.DigestBytes(externalData).String(),
				},
			},
		},
	}
}

func TestValidateDefinitionBuildsContentAddressedEffectiveAuthority(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition, err := workspace.ValidateDefinition(fixture.sources)
	if err != nil {
		t.Fatalf("ValidateDefinition: %v", err)
	}
	if definition.Generation().IsZero() {
		t.Fatal("generation must be content-addressed")
	}
	manifest := definition.Workspace()
	if got := manifest.ID().String(); got != "example-workspace" {
		t.Fatalf("workspace id = %q", got)
	}
	if got := manifest.Repository().String(); got != "https://github.com/example/project.git" {
		t.Fatalf("repository identity = %q", got)
	}
	if got := manifest.Provider().Repository(); got != "example/project" {
		t.Fatalf("provider repository = %q", got)
	}
	if manifest.BaseRef() != "feature/example-workspace" || manifest.Remote() != "origin" {
		t.Fatalf("workspace authority missing base/remote: %#v", manifest)
	}
	if got := manifest.ExecutionConfigSource(); got != "config/execution.yaml" {
		t.Fatalf("execution config path = %q", got)
	}
	if got := len(definition.Plans()); got != 1 {
		t.Fatalf("plans = %d", got)
	}
	if got := len(definition.Profiles()); got != 1 {
		t.Fatalf("profiles = %d", got)
	}
	if got := len(definition.Artifacts()); got != 3 {
		t.Fatalf("artifacts = %d", got)
	}
	for _, artifact := range definition.Artifacts() {
		if artifact.SourceHash().IsZero() || artifact.SemanticHash().IsZero() || len(artifact.CanonicalBytes()) == 0 {
			t.Fatalf("artifact is not fully hashed: kind=%s path=%s", artifact.Kind(), artifact.Path())
		}
	}
	snapshots := definition.AuthoritySnapshots()
	if len(snapshots) != 2 {
		t.Fatalf("authority snapshots = %d", len(snapshots))
	}
	for _, snapshot := range snapshots {
		if snapshot.SourceHash().IsZero() || snapshot.SemanticHash().IsZero() {
			t.Fatalf("authority snapshot is not hashed: %s", snapshot.ID())
		}
		switch snapshot.Kind() {
		case workspace.AuthorityGitBlob:
			pin, ok := snapshot.GitPin()
			if !ok || pin.Blob().String() != gitSHA1Blob(fixture.gitData) {
				t.Fatalf("invalid Git authority pin: %#v", pin)
			}
		case workspace.AuthorityExternalDigest:
			digest, ok := snapshot.ExternalDigest()
			if !ok || digest != workspace.DigestBytes(fixture.extData) {
				t.Fatalf("invalid external authority pin: %s", digest)
			}
		default:
			t.Fatalf("unexpected authority kind %q", snapshot.Kind())
		}
	}

	workspaceLock := workspace.ProjectWorkspaceLock(definition)
	if workspaceLock.SchemaVersion() != 2 || workspaceLock.Generation() != definition.Generation() || len(workspaceLock.Artifacts()) != 3 || len(workspaceLock.Authorities()) != 2 {
		t.Fatalf("workspace projection = %#v", workspaceLock)
	}
	planLocks := workspace.ProjectPlanLocks(definition)
	if len(planLocks) != 1 || planLocks[0].PlanID().String() != "alpha-plan" || planLocks[0].Generation() != definition.Generation() {
		t.Fatalf("plan projections = %#v", planLocks)
	}
	workspaceLockJSON, err := json.Marshal(workspaceLock)
	if err != nil {
		t.Fatal(err)
	}
	planLockJSON, err := json.Marshal(planLocks[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range [][]byte{workspaceLockJSON, planLockJSON} {
		text := string(encoded)
		if !strings.Contains(text, definition.Generation().String()) || strings.Contains(text, "runtime") || strings.Contains(text, "status") || strings.Contains(text, "base_ref") || strings.Contains(text, "remote") {
			t.Fatalf("projection JSON has mutable or misplaced authority: %s", text)
		}
	}
}

func TestSourceAndSemanticHashesHaveDistinctContracts(t *testing.T) {
	fixture := newDefinitionFixture(t)
	first, err := workspace.ValidateDefinition(fixture.sources)
	if err != nil {
		t.Fatal(err)
	}
	secondSources := cloneDefinitionSources(fixture.sources)
	secondSources.Workspace.Bytes = []byte(strings.Replace(string(secondSources.Workspace.Bytes), "id: example-workspace\n", "id: example-workspace   \n", 1))
	second, err := workspace.ValidateDefinition(secondSources)
	if err != nil {
		t.Fatal(err)
	}
	firstWorkspace := artifactByKind(t, first, workspace.ArtifactWorkspace)
	secondWorkspace := artifactByKind(t, second, workspace.ArtifactWorkspace)
	if firstWorkspace.SourceHash() == secondWorkspace.SourceHash() {
		t.Fatal("raw source hash must change when source bytes change")
	}
	if firstWorkspace.SemanticHash() != secondWorkspace.SemanticHash() {
		t.Fatal("semantic hash must ignore insignificant YAML whitespace")
	}
	if first.Generation() == second.Generation() {
		t.Fatal("generation must bind exact source hashes")
	}
}

func TestNoReviewExecutionCanonicalFormPreservesPreReviewSemanticHash(t *testing.T) {
	fixture := newDefinitionFixture(t)
	definition, err := workspace.ValidateDefinition(fixture.sources)
	if err != nil {
		t.Fatal(err)
	}
	execution := artifactByKind(t, definition, workspace.ArtifactExecutionConfig)
	if strings.Contains(string(execution.CanonicalBytes()), "review_profiles") {
		t.Fatalf("no-review canonical execution gained a review_profiles field: %s", execution.CanonicalBytes())
	}
	const preReviewSemanticHash = "sha256:005977c9af3bfe559b2adf55ec1e15bac02989dadf09e305c5174c3fe9ebba6d"
	if got := execution.SemanticHash().String(); got != preReviewSemanticHash {
		t.Fatalf("no-review execution semantic hash = %s, want %s", got, preReviewSemanticHash)
	}
}

func TestAuthorityChangesCreateNewGeneration(t *testing.T) {
	fixture := newDefinitionFixture(t)
	first, err := workspace.ValidateDefinition(fixture.sources)
	if err != nil {
		t.Fatal(err)
	}

	contentChanged := cloneDefinitionSources(fixture.sources)
	contentChanged.Authorities[1].Content = []byte("organization-policy: stricter\n")
	contentChanged.Authorities[1].ExpectedSourceDigest = workspace.DigestBytes(contentChanged.Authorities[1].Content).String()
	second, err := workspace.ValidateDefinition(contentChanged)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation() == second.Generation() {
		t.Fatal("authority content change must create a new generation")
	}

	pinChanged := cloneDefinitionSources(fixture.sources)
	pinChanged.Authorities[0].CommitObject = "sha1:" + strings.Repeat("2", 40)
	third, err := workspace.ValidateDefinition(pinChanged)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation() == third.Generation() {
		t.Fatal("authority Git pin change must create a new generation")
	}
}

func TestEffectiveDefinitionBindsTypedCrossPlanMergeUnitDependencies(t *testing.T) {
	fixture := newDefinitionFixture(t)
	sources := cloneDefinitionSources(fixture.sources)
	workspaceText := string(sources.Workspace.Bytes)
	workspaceText = strings.Replace(workspaceText,
		"  - id: alpha-plan\n    source: plans/alpha.yaml\ndependencies: []",
		"  - id: alpha-plan\n    source: plans/alpha.yaml\n  - id: beta-plan\n    source: plans/beta.yaml\ndependencies:\n  - before:\n      plan_id: alpha-plan\n      merge_unit_id: unit-two\n    after:\n      plan_id: beta-plan\n      merge_unit_id: missing-unit",
		1,
	)
	sources.Workspace.Bytes = []byte(workspaceText)
	sources.Plans = append(sources.Plans, workspace.SourceArtifact{Path: "plans/beta.yaml", Bytes: []byte(`schema_version: 2
id: beta-plan
title: Beta Plan
stories:
  - id: beta-story
    summary: Implement the beta story.
    acceptance:
      - The beta story is complete.
    implementation:
      - Implement beta behavior.
    testing:
      - Test beta behavior.
    dependencies: []
merge_units:
  - id: beta-unit
    name: Beta Unit
    story_ids:
      - beta-story
`)})
	sources.ExecutionConfig.Bytes = append(sources.ExecutionConfig.Bytes, []byte(`  - plan_id: beta-plan
    merge_unit_id: beta-unit
    profile: standard
    boundary:
      mode: pause_only
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 2
`)...)
	if _, err := workspace.ValidateDefinition(sources); err == nil || !strings.Contains(err.Error(), "unknown merge unit beta-plan/missing-unit") {
		t.Fatalf("unknown cross-plan target error = %v", err)
	}
	sources.Workspace.Bytes = []byte(strings.Replace(string(sources.Workspace.Bytes), "merge_unit_id: missing-unit", "merge_unit_id: beta-unit", 1))
	definition, err := workspace.ValidateDefinition(sources)
	if err != nil {
		t.Fatalf("valid cross-plan definition: %v", err)
	}
	dependencies := definition.Workspace().Dependencies()
	if len(dependencies) != 1 || dependencies[0].Before().MergeUnitID().String() != "unit-two" || dependencies[0].After().MergeUnitID().String() != "beta-unit" {
		t.Fatalf("effective cross-plan dependency = %#v", dependencies)
	}
}

func TestEffectiveDefinitionRejectsCombinedCrossPlanCycle(t *testing.T) {
	fixture := newDefinitionFixture(t)
	sources := cloneDefinitionSources(fixture.sources)
	sources.Workspace.Bytes = []byte(strings.Replace(string(sources.Workspace.Bytes),
		"  - id: alpha-plan\n    source: plans/alpha.yaml\ndependencies: []",
		"  - id: alpha-plan\n    source: plans/alpha.yaml\n  - id: beta-plan\n    source: plans/beta.yaml\ndependencies:\n  - before:\n      plan_id: alpha-plan\n      merge_unit_id: unit-two\n    after:\n      plan_id: beta-plan\n      merge_unit_id: unit-one\n  - before:\n      plan_id: beta-plan\n      merge_unit_id: unit-two\n    after:\n      plan_id: alpha-plan\n      merge_unit_id: unit-one",
		1,
	))
	sources.Plans = append(sources.Plans, workspace.SourceArtifact{Path: "plans/beta.yaml", Bytes: []byte(strings.Replace(string(sources.Plans[0].Bytes), "id: alpha-plan", "id: beta-plan", 1))})
	sources.ExecutionConfig.Bytes = append(sources.ExecutionConfig.Bytes, []byte(`  - plan_id: beta-plan
    merge_unit_id: unit-one
    profile: standard
    boundary:
      mode: pause_only
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 2
  - plan_id: beta-plan
    merge_unit_id: unit-two
    profile: standard
    boundary:
      mode: pause_only
    policy:
      require_passing_checks: true
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 2
      max_review_rounds: 2
      max_review_fixes: 2
`)...)
	if _, err := workspace.ValidateDefinition(sources); err == nil || !strings.Contains(err.Error(), "workspace merge-unit dependency cycle") {
		t.Fatalf("combined cross-plan cycle error = %v", err)
	}
}

func TestEffectiveDefinitionRejectsCycleIntroducedByMergeUnitGrouping(t *testing.T) {
	fixture := newDefinitionFixture(t)
	sources := cloneDefinitionSources(fixture.sources)
	sources.Plans[0].Bytes = []byte(`schema_version: 2
id: alpha-plan
title: Grouped Plan
stories:
  - id: story-a-one
    summary: First story in unit A.
    acceptance: [Unit A is explicit.]
    implementation: [Implement unit A.]
    testing: [Test unit A.]
    dependencies: [story-b-one]
  - id: story-a-two
    summary: Second story in unit A.
    acceptance: [Unit A remains explicit.]
    implementation: [Implement the second part of unit A.]
    testing: [Test the second part of unit A.]
    dependencies: []
  - id: story-b-one
    summary: First story in unit B.
    acceptance: [Unit B is explicit.]
    implementation: [Implement unit B.]
    testing: [Test unit B.]
    dependencies: []
  - id: story-b-two
    summary: Second story in unit B.
    acceptance: [Unit B remains explicit.]
    implementation: [Implement the second part of unit B.]
    testing: [Test the second part of unit B.]
    dependencies: [story-a-two]
merge_units:
  - id: unit-one
    name: Unit A
    story_ids: [story-a-one, story-a-two]
  - id: unit-two
    name: Unit B
    story_ids: [story-b-one, story-b-two]
`)
	if _, err := workspace.ValidateDefinition(sources); err == nil || !strings.Contains(err.Error(), "workspace merge-unit dependency cycle") {
		t.Fatalf("grouped merge-unit cycle error = %v", err)
	}
}

func TestEffectiveDefinitionRejectsCrossRoleArtifactPathCollisions(t *testing.T) {
	fixture := newDefinitionFixture(t)
	tests := []struct {
		name   string
		mutate func(*workspace.DefinitionSources)
		roles  string
	}{
		{
			name: "workspace and plan",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.Plans[0].Path = sources.Workspace.Path
			},
			roles: "workspace and plan input 0",
		},
		{
			name: "plan and execution config",
			mutate: func(sources *workspace.DefinitionSources) {
				sources.ExecutionConfig.Path = sources.Plans[0].Path
			},
			roles: "plan input 0 and execution config",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := cloneDefinitionSources(fixture.sources)
			test.mutate(&sources)
			if _, err := workspace.ValidateDefinition(sources); err == nil || !strings.Contains(err.Error(), test.roles) {
				t.Fatalf("artifact collision error = %v", err)
			}
		})
	}
}

func TestValidationIsIndependentOfDirtyCheckout(t *testing.T) {
	fixture := newDefinitionFixture(t)
	first, err := workspace.ValidateDefinition(fixture.sources)
	if err != nil {
		t.Fatal(err)
	}
	dirtyPath := filepath.Join(first.Workspace().RepositoryRoot(), "unrelated-dirty-file.txt")
	if err := os.WriteFile(dirtyPath, []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := workspace.ValidateDefinition(fixture.sources)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation() != second.Generation() {
		t.Fatal("dirty checkout changed a definition built from exact supplied sources")
	}
}

func TestDefinitionDefensivelyCopiesNestedInputsAndOutputs(t *testing.T) {
	fixture := newDefinitionFixture(t)
	sources := cloneDefinitionSources(fixture.sources)
	definition, err := workspace.ValidateDefinition(sources)
	if err != nil {
		t.Fatal(err)
	}
	wantGeneration := definition.Generation()
	sources.Workspace.Bytes[0] = 'X'
	sources.Plans[0].Bytes[0] = 'X'
	sources.Authorities[0].Content[0] = 'X'
	artifacts := definition.Artifacts()
	canonical := artifacts[0].CanonicalBytes()
	canonical[0] = 'X'
	artifacts[0] = workspace.NormalizedArtifact{}
	plans := definition.Plans()
	plans[0] = workspace.Plan{}
	profiles := definition.Profiles()
	profiles[0] = workspace.ExecutionProfile{}
	snapshots := definition.AuthoritySnapshots()
	snapshots[0] = workspace.AuthoritySnapshot{}

	if definition.Generation() != wantGeneration || definition.Plans()[0].ID().String() != "alpha-plan" || len(definition.Artifacts()[0].CanonicalBytes()) == 0 {
		t.Fatal("caller mutation escaped a definition boundary")
	}
	story := definition.Plans()[0].Stories()[1]
	dependencies := story.Dependencies()
	dependencies[0] = workspace.MustID("changed")
	if got := story.Dependencies()[0].String(); got != "story-one" {
		t.Fatalf("nested story dependency alias escaped: %q", got)
	}
}

func TestV2PlanAndLockProjectionsCannotOwnRuntimeOrWorkspaceAuthority(t *testing.T) {
	assertTypeOmitsFields(t, reflect.TypeOf(workspace.Plan{}), "base", "remote", "policy", "runtime", "state", "status")
	assertTypeOmitsFields(t, reflect.TypeOf(workspace.PlanLockProjection{}), "base", "remote", "policy", "runtime", "state", "status")
	assertTypeOmitsFields(t, reflect.TypeOf(workspace.WorkspaceLockProjection{}), "runtime", "state", "status", "approval", "attempt")
}

func artifactByKind(t *testing.T, definition workspace.EffectiveWorkspaceDefinition, kind workspace.ArtifactKind) workspace.NormalizedArtifact {
	t.Helper()
	for _, artifact := range definition.Artifacts() {
		if artifact.Kind() == kind {
			return artifact
		}
	}
	t.Fatalf("missing artifact kind %s", kind)
	return workspace.NormalizedArtifact{}
}

func cloneDefinitionSources(source workspace.DefinitionSources) workspace.DefinitionSources {
	result := source
	result.Workspace.Bytes = append([]byte(nil), source.Workspace.Bytes...)
	result.ExecutionConfig.Bytes = append([]byte(nil), source.ExecutionConfig.Bytes...)
	result.Plans = append([]workspace.SourceArtifact(nil), source.Plans...)
	for index := range result.Plans {
		result.Plans[index].Bytes = append([]byte(nil), source.Plans[index].Bytes...)
	}
	result.Authorities = append([]workspace.AuthorityMaterial(nil), source.Authorities...)
	for index := range result.Authorities {
		result.Authorities[index].Content = append([]byte(nil), source.Authorities[index].Content...)
	}
	return result
}

func gitSHA1Blob(content []byte) string {
	digest := sha1.New()
	_, _ = fmt.Fprintf(digest, "blob %d%c", len(content), byte(0))
	_, _ = digest.Write(content)
	return fmt.Sprintf("sha1:%x", digest.Sum(nil))
}

func assertTypeOmitsFields(t *testing.T, typ reflect.Type, forbidden ...string) {
	t.Helper()
	for index := 0; index < typ.NumField(); index++ {
		name := strings.ToLower(typ.Field(index).Name)
		for _, fragment := range forbidden {
			if strings.Contains(name, fragment) {
				t.Fatalf("%s unexpectedly owns field %s", typ, typ.Field(index).Name)
			}
		}
	}
}
