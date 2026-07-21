package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

type SourceArtifact struct {
	Path  string
	Bytes []byte
}

type DefinitionSources struct {
	Workspace       SourceArtifact
	Plans           []SourceArtifact
	ExecutionConfig SourceArtifact
	Authorities     []AuthorityMaterial
}

type ArtifactKind string

const (
	ArtifactWorkspace       ArtifactKind = "workspace"
	ArtifactPlan            ArtifactKind = "plan"
	ArtifactExecutionConfig ArtifactKind = "execution_config"
)

type NormalizedArtifact struct {
	kind         ArtifactKind
	id           ID
	path         string
	sourceHash   Digest
	semanticHash Digest
	canonical    []byte
}

func (artifact NormalizedArtifact) Kind() ArtifactKind   { return artifact.kind }
func (artifact NormalizedArtifact) ID() ID               { return artifact.id }
func (artifact NormalizedArtifact) Path() string         { return artifact.path }
func (artifact NormalizedArtifact) SourceHash() Digest   { return artifact.sourceHash }
func (artifact NormalizedArtifact) SemanticHash() Digest { return artifact.semanticHash }
func (artifact NormalizedArtifact) CanonicalBytes() []byte {
	return append([]byte(nil), artifact.canonical...)
}

// EffectiveWorkspaceDefinition is a content-addressed, immutable composition
// of all validated v2 authority. It is pure with respect to checkout state.
type EffectiveWorkspaceDefinition struct {
	workspace   WorkspaceManifest
	plans       []Plan
	execution   ExecutionConfig
	artifacts   []NormalizedArtifact
	authorities []AuthoritySnapshot
	generation  Digest
}

func ValidateDefinition(sources DefinitionSources) (EffectiveWorkspaceDefinition, error) {
	workspacePath, err := normalizeSourcePath(sources.Workspace.Path)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("workspace source path: %w", err)
	}
	workspaceSource := append([]byte(nil), sources.Workspace.Bytes...)
	workspace, err := DecodeWorkspaceManifest(workspaceSource)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}
	workspaceCanonical, err := canonicalWorkspaceBytes(workspace)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}
	artifacts := []NormalizedArtifact{newArtifact(
		ArtifactWorkspace, workspace.id, workspacePath, workspaceSource, workspaceCanonical,
	)}

	planSources := make(map[string][]byte, len(sources.Plans))
	for index, source := range sources.Plans {
		sourcePath, err := normalizeSourcePath(source.Path)
		if err != nil {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("plan source %d: %w", index, err)
		}
		if _, exists := planSources[sourcePath]; exists {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("duplicate plan input source %s", sourcePath)
		}
		planSources[sourcePath] = append([]byte(nil), source.Bytes...)
	}
	if len(planSources) != len(workspace.plans) {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("workspace declares %d plans but %d plan sources were supplied", len(workspace.plans), len(planSources))
	}
	plans := make([]Plan, 0, len(workspace.plans))
	for _, reference := range workspace.plans {
		source, exists := planSources[reference.source]
		if !exists {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("missing plan source %s for %s", reference.source, reference.id)
		}
		plan, err := DecodePlan(source)
		if err != nil {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("plan %s: %w", reference.id, err)
		}
		if plan.id != reference.id {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("plan source %s declares id %s, expected %s", reference.source, plan.id, reference.id)
		}
		canonical, err := canonicalPlanBytes(plan)
		if err != nil {
			return EffectiveWorkspaceDefinition{}, err
		}
		plans = append(plans, plan)
		artifacts = append(artifacts, newArtifact(ArtifactPlan, plan.id, reference.source, source, canonical))
	}
	if err := validateWorkspaceDependencyTargets(workspace, plans); err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}

	executionPath, err := normalizeSourcePath(sources.ExecutionConfig.Path)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("execution config source path: %w", err)
	}
	if executionPath != workspace.executionConfig {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("execution config input %s does not match workspace discovery path %s", executionPath, workspace.executionConfig)
	}
	executionSource := append([]byte(nil), sources.ExecutionConfig.Bytes...)
	execution, err := DecodeExecutionConfig(executionSource)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}
	if err := validateExecutionCoverage(plans, execution); err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}
	executionCanonical, err := canonicalExecutionBytes(execution)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}
	artifacts = append(artifacts, newArtifact(
		ArtifactExecutionConfig, workspace.id, executionPath, executionSource, executionCanonical,
	))

	materialByID := make(map[string]AuthorityMaterial, len(sources.Authorities))
	for _, material := range sources.Authorities {
		materialID, err := NewID(material.ID)
		if err != nil {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("authority material: %w", err)
		}
		if _, exists := materialByID[materialID.String()]; exists {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("duplicate authority material %s", materialID)
		}
		copyMaterial := material
		copyMaterial.Content = append([]byte(nil), material.Content...)
		materialByID[materialID.String()] = copyMaterial
	}
	if len(materialByID) != len(workspace.authoritySources) {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("workspace declares %d authority sources but %d materials were supplied", len(workspace.authoritySources), len(materialByID))
	}
	authorities := make([]AuthoritySnapshot, 0, len(workspace.authoritySources))
	for _, reference := range workspace.authoritySources {
		material, exists := materialByID[reference.id.String()]
		if !exists {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("missing authority material %s", reference.id)
		}
		snapshot, err := pinAuthority(reference, material)
		if err != nil {
			return EffectiveWorkspaceDefinition{}, err
		}
		authorities = append(authorities, snapshot)
	}

	sort.Slice(artifacts, func(i, j int) bool {
		left := string(artifacts[i].kind) + "\x00" + artifacts[i].id.String() + "\x00" + artifacts[i].path
		right := string(artifacts[j].kind) + "\x00" + artifacts[j].id.String() + "\x00" + artifacts[j].path
		return left < right
	})
	generationBytes, err := canonicalGenerationBytes(workspace.id, artifacts, authorities)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}

	return EffectiveWorkspaceDefinition{
		workspace: workspace, plans: append([]Plan(nil), plans...), execution: execution,
		artifacts: cloneArtifacts(artifacts), authorities: append([]AuthoritySnapshot(nil), authorities...),
		generation: DigestBytes(generationBytes),
	}, nil
}

func (definition EffectiveWorkspaceDefinition) Workspace() WorkspaceManifest {
	return definition.workspace
}
func (definition EffectiveWorkspaceDefinition) Plans() []Plan {
	return append([]Plan(nil), definition.plans...)
}
func (definition EffectiveWorkspaceDefinition) ExecutionConfig() ExecutionConfig {
	return definition.execution
}
func (definition EffectiveWorkspaceDefinition) Profiles() []ExecutionProfile {
	return definition.execution.Profiles()
}
func (definition EffectiveWorkspaceDefinition) Artifacts() []NormalizedArtifact {
	return cloneArtifacts(definition.artifacts)
}
func (definition EffectiveWorkspaceDefinition) AuthoritySnapshots() []AuthoritySnapshot {
	return append([]AuthoritySnapshot(nil), definition.authorities...)
}
func (definition EffectiveWorkspaceDefinition) Generation() Digest { return definition.generation }

func newArtifact(kind ArtifactKind, id ID, sourcePath string, source, canonical []byte) NormalizedArtifact {
	return NormalizedArtifact{
		kind: kind, id: id, path: sourcePath, sourceHash: DigestBytes(source),
		semanticHash: DigestBytes(canonical), canonical: append([]byte(nil), canonical...),
	}
}

func cloneArtifacts(source []NormalizedArtifact) []NormalizedArtifact {
	result := append([]NormalizedArtifact(nil), source...)
	for index := range result {
		result[index].canonical = append([]byte(nil), result[index].canonical...)
	}
	return result
}

func validateExecutionCoverage(plans []Plan, execution ExecutionConfig) error {
	expected := make(map[string]struct{})
	for _, plan := range plans {
		for _, unit := range plan.mergeUnits {
			expected[plan.id.String()+"\x00"+unit.id.String()] = struct{}{}
		}
	}
	if len(expected) != len(execution.mergeUnits) {
		return fmt.Errorf("execution config covers %d merge units; workspace plans require %d", len(execution.mergeUnits), len(expected))
	}
	for _, unit := range execution.mergeUnits {
		key := unit.planID.String() + "\x00" + unit.mergeUnitID.String()
		if _, exists := expected[key]; !exists {
			return fmt.Errorf("execution config references unknown merge unit %s/%s", unit.planID, unit.mergeUnitID)
		}
		delete(expected, key)
	}
	for key := range expected {
		return fmt.Errorf("execution config is missing merge unit %q", key)
	}
	return nil
}

func validateWorkspaceDependencyTargets(workspace WorkspaceManifest, plans []Plan) error {
	known := make(map[string]struct{})
	for _, plan := range plans {
		for _, unit := range plan.mergeUnits {
			known[MergeUnitReference{planID: plan.id, mergeUnitID: unit.id}.key()] = struct{}{}
		}
	}
	for _, dependency := range workspace.dependencies {
		for _, reference := range []MergeUnitReference{dependency.before, dependency.after} {
			if _, exists := known[reference.key()]; !exists {
				return fmt.Errorf("workspace dependency references unknown merge unit %s/%s", reference.planID, reference.mergeUnitID)
			}
		}
	}
	return nil
}

type canonicalWorkspace struct {
	SchemaVersion    int                            `json:"schema_version"`
	ID               string                         `json:"id"`
	Repository       canonicalRepository            `json:"repository"`
	Provider         canonicalProvider              `json:"provider"`
	BaseRef          string                         `json:"base_ref"`
	Remote           string                         `json:"remote"`
	ExecutionConfig  string                         `json:"execution_config"`
	Plans            []canonicalPlanReference       `json:"plans"`
	Dependencies     []canonicalWorkspaceDependency `json:"dependencies"`
	AuthoritySources []canonicalAuthorityReference  `json:"authority_sources"`
}

type canonicalRepository struct {
	Root     string `json:"root"`
	Identity string `json:"identity"`
}
type canonicalProvider struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
}
type canonicalPlanReference struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}
type canonicalMergeUnitReference struct {
	PlanID      string `json:"plan_id"`
	MergeUnitID string `json:"merge_unit_id"`
}
type canonicalWorkspaceDependency struct {
	Before canonicalMergeUnitReference `json:"before"`
	After  canonicalMergeUnitReference `json:"after"`
}
type canonicalAuthorityReference struct {
	ID       string        `json:"id"`
	Kind     AuthorityKind `json:"kind"`
	Location string        `json:"location"`
}

func canonicalWorkspaceBytes(workspace WorkspaceManifest) ([]byte, error) {
	value := canonicalWorkspace{
		SchemaVersion: 2, ID: workspace.id.String(),
		Repository: canonicalRepository{Root: workspace.repositoryRoot, Identity: workspace.repository.String()},
		Provider:   canonicalProvider{Kind: workspace.provider.kind.String(), Repository: workspace.provider.repository},
		BaseRef:    workspace.baseRef, Remote: workspace.remote, ExecutionConfig: workspace.executionConfig,
		Plans:            make([]canonicalPlanReference, 0, len(workspace.plans)),
		Dependencies:     make([]canonicalWorkspaceDependency, 0, len(workspace.dependencies)),
		AuthoritySources: make([]canonicalAuthorityReference, 0, len(workspace.authoritySources)),
	}
	for _, plan := range workspace.plans {
		value.Plans = append(value.Plans, canonicalPlanReference{ID: plan.id.String(), Source: plan.source})
	}
	for _, dependency := range workspace.dependencies {
		value.Dependencies = append(value.Dependencies, canonicalWorkspaceDependency{
			Before: canonicalMergeUnitReference{PlanID: dependency.before.planID.String(), MergeUnitID: dependency.before.mergeUnitID.String()},
			After:  canonicalMergeUnitReference{PlanID: dependency.after.planID.String(), MergeUnitID: dependency.after.mergeUnitID.String()},
		})
	}
	for _, authority := range workspace.authoritySources {
		value.AuthoritySources = append(value.AuthoritySources, canonicalAuthorityReference{ID: authority.id.String(), Kind: authority.kind, Location: authority.location})
	}
	return json.Marshal(value)
}

type canonicalPlan struct {
	SchemaVersion int                  `json:"schema_version"`
	ID            string               `json:"id"`
	Title         string               `json:"title"`
	Stories       []canonicalStory     `json:"stories"`
	MergeUnits    []canonicalMergeUnit `json:"merge_units"`
}
type canonicalStory struct {
	ID             string   `json:"id"`
	Summary        string   `json:"summary"`
	Acceptance     []string `json:"acceptance"`
	Implementation []string `json:"implementation"`
	Testing        []string `json:"testing"`
	Dependencies   []string `json:"dependencies"`
}
type canonicalMergeUnit struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	StoryIDs []string `json:"story_ids"`
}

func canonicalPlanBytes(plan Plan) ([]byte, error) {
	value := canonicalPlan{SchemaVersion: 2, ID: plan.id.String(), Title: plan.title}
	for _, story := range plan.stories {
		item := canonicalStory{
			ID: story.id.String(), Summary: story.summary,
			Acceptance:     append([]string(nil), story.acceptance...),
			Implementation: append([]string(nil), story.implementation...),
			Testing:        append([]string(nil), story.testing...),
		}
		for _, dependency := range story.dependencies {
			item.Dependencies = append(item.Dependencies, dependency.String())
		}
		value.Stories = append(value.Stories, item)
	}
	for _, unit := range plan.mergeUnits {
		item := canonicalMergeUnit{ID: unit.id.String(), Name: unit.name}
		for _, storyID := range unit.storyIDs {
			item.StoryIDs = append(item.StoryIDs, storyID.String())
		}
		value.MergeUnits = append(value.MergeUnits, item)
	}
	return json.Marshal(value)
}

type canonicalExecution struct {
	SchemaVersion int                      `json:"schema_version"`
	Policy        canonicalPolicy          `json:"policy"`
	Profiles      []canonicalProfile       `json:"profiles"`
	MergeUnits    []canonicalUnitExecution `json:"merge_units"`
}
type canonicalPolicy struct {
	RequirePassingChecks  bool   `json:"require_passing_checks"`
	RequireSignedReceipts bool   `json:"require_signed_receipts"`
	AllowWriteNetwork     bool   `json:"allow_write_network"`
	MaxAttempts           uint16 `json:"max_attempts"`
	MaxReviewRounds       uint16 `json:"max_review_rounds"`
	MaxReviewFixes        uint16 `json:"max_review_fixes"`
}
type canonicalProfile struct {
	ID     string          `json:"id"`
	Runner string          `json:"runner"`
	Policy canonicalPolicy `json:"policy"`
}
type canonicalUnitExecution struct {
	PlanID      string          `json:"plan_id"`
	MergeUnitID string          `json:"merge_unit_id"`
	Profile     string          `json:"profile"`
	Policy      canonicalPolicy `json:"policy"`
}

func canonicalExecutionBytes(config ExecutionConfig) ([]byte, error) {
	value := canonicalExecution{SchemaVersion: 2, Policy: canonicalizePolicy(config.policy)}
	for _, profile := range config.profiles {
		value.Profiles = append(value.Profiles, canonicalProfile{ID: profile.id.String(), Runner: profile.runner.String(), Policy: canonicalizePolicy(profile.policy)})
	}
	for _, unit := range config.mergeUnits {
		value.MergeUnits = append(value.MergeUnits, canonicalUnitExecution{
			PlanID: unit.planID.String(), MergeUnitID: unit.mergeUnitID.String(), Profile: unit.profileID.String(), Policy: canonicalizePolicy(unit.policy),
		})
	}
	return json.Marshal(value)
}

func canonicalizePolicy(policy ExecutionPolicy) canonicalPolicy {
	return canonicalPolicy{
		RequirePassingChecks: policy.requirePassingChecks, RequireSignedReceipts: policy.requireSignedReceipts,
		AllowWriteNetwork: policy.allowWriteNetwork, MaxAttempts: policy.maxAttempts,
		MaxReviewRounds: policy.maxReviewRounds, MaxReviewFixes: policy.maxReviewFixes,
	}
}

type canonicalAuthority struct {
	ID             string        `json:"id"`
	Kind           AuthorityKind `json:"kind"`
	Location       string        `json:"location"`
	SourceHash     string        `json:"source_hash"`
	GitRepository  string        `json:"git_repository,omitempty"`
	GitCommit      string        `json:"git_commit,omitempty"`
	GitBlob        string        `json:"git_blob,omitempty"`
	ExternalDigest string        `json:"external_digest,omitempty"`
}

func canonicalAuthorityBytes(snapshot AuthoritySnapshot) ([]byte, error) {
	value := canonicalAuthority{
		ID: snapshot.id.String(), Kind: snapshot.kind, Location: snapshot.location, SourceHash: snapshot.sourceHash.String(),
	}
	if snapshot.kind == AuthorityGitBlob {
		value.GitRepository = snapshot.gitPin.repository.String()
		value.GitCommit = snapshot.gitPin.commit.String()
		value.GitBlob = snapshot.gitPin.blob.String()
	} else {
		value.ExternalDigest = snapshot.externalPin.String()
	}
	return json.Marshal(value)
}

type canonicalGeneration struct {
	SchemaVersion int                          `json:"schema_version"`
	WorkspaceID   string                       `json:"workspace_id"`
	Artifacts     []canonicalArtifactIdentity  `json:"artifacts"`
	Authorities   []canonicalAuthorityIdentity `json:"authorities"`
}
type canonicalArtifactIdentity struct {
	Kind         ArtifactKind `json:"kind"`
	ID           string       `json:"id"`
	Path         string       `json:"path"`
	SourceHash   string       `json:"source_hash"`
	SemanticHash string       `json:"semantic_hash"`
}
type canonicalAuthorityIdentity struct {
	ID           string        `json:"id"`
	Kind         AuthorityKind `json:"kind"`
	SourceHash   string        `json:"source_hash"`
	SemanticHash string        `json:"semantic_hash"`
}

func canonicalGenerationBytes(workspaceID ID, artifacts []NormalizedArtifact, authorities []AuthoritySnapshot) ([]byte, error) {
	value := canonicalGeneration{SchemaVersion: 2, WorkspaceID: workspaceID.String()}
	for _, artifact := range artifacts {
		value.Artifacts = append(value.Artifacts, canonicalArtifactIdentity{
			Kind: artifact.kind, ID: artifact.id.String(), Path: artifact.path,
			SourceHash: artifact.sourceHash.String(), SemanticHash: artifact.semanticHash.String(),
		})
	}
	for _, authority := range authorities {
		value.Authorities = append(value.Authorities, canonicalAuthorityIdentity{
			ID: authority.id.String(), Kind: authority.kind,
			SourceHash: authority.sourceHash.String(), SemanticHash: authority.semanticHash.String(),
		})
	}
	return json.Marshal(value)
}
