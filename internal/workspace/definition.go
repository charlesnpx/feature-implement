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
}

type ArtifactKind string

const (
	ArtifactWorkspaceBundle ArtifactKind = "workspace_bundle"
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
// of all validated workspace inputs. It is pure with respect to checkout state.
type EffectiveWorkspaceDefinition struct {
	workspace  WorkspaceManifest
	plans      []Plan
	execution  ExecutionConfig
	artifacts  []NormalizedArtifact
	generation Digest
}

func ValidateDefinition(sources DefinitionSources) (EffectiveWorkspaceDefinition, error) {
	workspacePath, err := normalizeSourcePath(sources.Workspace.Path)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("workspace source path: %w", err)
	}
	artifactPaths := map[string]string{workspacePath: "workspace"}
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
		if prior, exists := artifactPaths[sourcePath]; exists {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("artifact source path %s is claimed by both %s and plan input %d", sourcePath, prior, index)
		}
		artifactPaths[sourcePath] = fmt.Sprintf("plan input %d", index)
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
	if err := validateWorkspaceMergeUnitGraph(workspace, plans); err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}

	executionPath, err := normalizeSourcePath(sources.ExecutionConfig.Path)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("execution config source path: %w", err)
	}
	if prior, exists := artifactPaths[executionPath]; exists {
		return EffectiveWorkspaceDefinition{}, fmt.Errorf("artifact source path %s is claimed by both %s and execution config", executionPath, prior)
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

	sort.Slice(artifacts, func(i, j int) bool {
		left := string(artifacts[i].kind) + "\x00" + artifacts[i].id.String() + "\x00" + artifacts[i].path
		right := string(artifacts[j].kind) + "\x00" + artifacts[j].id.String() + "\x00" + artifacts[j].path
		return left < right
	})
	generationBytes, err := canonicalGenerationBytes(workspace.id, artifacts)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}

	return EffectiveWorkspaceDefinition{
		workspace: workspace, plans: append([]Plan(nil), plans...), execution: execution,
		artifacts:  cloneArtifacts(artifacts),
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

func validateWorkspaceMergeUnitGraph(workspace WorkspaceManifest, plans []Plan) error {
	known := make(map[string]struct{})
	storyUnits := make(map[string]string)
	ids := make([]string, 0)
	edges := make(map[string][]string)
	for _, plan := range plans {
		for _, unit := range plan.mergeUnits {
			unitKey := MergeUnitReference{planID: plan.id, mergeUnitID: unit.id}.key()
			known[unitKey] = struct{}{}
			ids = append(ids, unitKey)
			for _, storyID := range unit.storyIDs {
				storyUnits[plan.id.String()+"\x00"+storyID.String()] = unitKey
			}
		}
	}
	for _, plan := range plans {
		for _, story := range plan.stories {
			unitKey := storyUnits[plan.id.String()+"\x00"+story.id.String()]
			for _, dependency := range story.dependencies {
				dependencyUnit := storyUnits[plan.id.String()+"\x00"+dependency.String()]
				if dependencyUnit != unitKey {
					edges[unitKey] = append(edges[unitKey], dependencyUnit)
				}
			}
		}
	}
	for _, dependency := range workspace.dependencies {
		for _, reference := range []MergeUnitReference{dependency.before, dependency.after} {
			if _, exists := known[reference.key()]; !exists {
				return fmt.Errorf("workspace dependency references unknown merge unit %s/%s", reference.planID, reference.mergeUnitID)
			}
		}
		edges[dependency.after.key()] = append(edges[dependency.after.key()], dependency.before.key())
	}
	sort.Strings(ids)
	for id := range edges {
		sort.Strings(edges[id])
	}
	return validateDAG("workspace merge-unit", ids, edges)
}

type canonicalWorkspace struct {
	SchemaVersion   int                            `json:"schema_version"`
	ID              string                         `json:"id"`
	Mode            WorkspaceMode                  `json:"mode"`
	Repository      canonicalRepository            `json:"repository"`
	BaseRef         string                         `json:"base_ref"`
	BaseCommit      string                         `json:"base_commit"`
	FeatureBranch   string                         `json:"feature_branch"`
	ExecutionConfig string                         `json:"execution_config"`
	Plans           []canonicalPlanReference       `json:"plans"`
	Dependencies    []canonicalWorkspaceDependency `json:"dependencies"`
}

type canonicalRepository struct {
	Root string `json:"root"`
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

func canonicalWorkspaceBytes(workspace WorkspaceManifest) ([]byte, error) {
	value := canonicalWorkspace{
		SchemaVersion: 2, ID: workspace.id.String(),
		Mode:       workspace.mode,
		Repository: canonicalRepository{Root: workspace.target.root},
		BaseRef:    workspace.target.baseRef, BaseCommit: workspace.target.baseCommit.String(),
		FeatureBranch:   workspace.target.featureBranch,
		ExecutionConfig: workspace.executionConfig,
		Plans:           make([]canonicalPlanReference, 0, len(workspace.plans)),
		Dependencies:    make([]canonicalWorkspaceDependency, 0, len(workspace.dependencies)),
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
	SchemaVersion  int                      `json:"schema_version"`
	Policy         canonicalPolicy          `json:"policy"`
	Profiles       []canonicalProfile       `json:"profiles"`
	ReviewProfiles []canonicalReviewProfile `json:"review_profiles,omitempty"`
	MergeUnits     []canonicalUnitExecution `json:"merge_units"`
}
type canonicalPolicy struct {
	RequirePassingChecks bool   `json:"require_passing_checks"`
	AllowWriteNetwork    bool   `json:"allow_write_network"`
	MaxAttempts          uint16 `json:"max_attempts"`
	MaxReviewRounds      uint16 `json:"max_review_rounds"`
}

type canonicalAttemptBoundaryPolicy struct {
	Checkpoint    AttemptCheckpointMode   `json:"checkpoint"`
	Escalation    AttemptEscalationPolicy `json:"escalation"`
	SerialSegment string                  `json:"serial_segment,omitempty"`
}

type canonicalProfileBoundaryPolicy struct {
	Escalation AttemptEscalationPolicy `json:"escalation"`
}

type canonicalProfile struct {
	ID       string                          `json:"id"`
	Runner   string                          `json:"runner"`
	Policy   canonicalPolicy                 `json:"policy"`
	Boundary *canonicalProfileBoundaryPolicy `json:"boundary,omitempty"`
}
type canonicalReviewProfile struct {
	ID             string               `json:"id"`
	Runner         string               `json:"runner"`
	ReviewerPolicy ReviewReviewerPolicy `json:"reviewer_policy"`
}
type canonicalUnitExecution struct {
	PlanID         string                         `json:"plan_id"`
	MergeUnitID    string                         `json:"merge_unit_id"`
	Profile        string                         `json:"profile"`
	Policy         canonicalPolicy                `json:"policy"`
	Boundary       canonicalAttemptBoundaryPolicy `json:"boundary"`
	CommitProtocol *canonicalCommitProtocol       `json:"commit_protocol,omitempty"`
	ReviewLoop     *canonicalReviewLoop           `json:"review_loop,omitempty"`
}

type canonicalReviewLoop struct {
	Profiles                 []string `json:"profiles"`
	MaxReviewRounds          uint16   `json:"max_review_rounds"`
	MaxInfrastructureRetries uint16   `json:"max_infrastructure_retries"`
}

type canonicalCommitProtocol struct {
	Steps []canonicalCommitStep `json:"steps"`
}

type canonicalCommitStep struct {
	ID           string                 `json:"id"`
	Subject      string                 `json:"subject"`
	BodyPolicy   CommitBodyPolicy       `json:"body_policy"`
	ExactBody    string                 `json:"exact_body,omitempty"`
	AllowedPaths []string               `json:"allowed_paths"`
	FrozenPaths  []string               `json:"frozen_paths"`
	Checks       []canonicalCommitCheck `json:"checks"`
}

type canonicalCommitCheck struct {
	ID      string   `json:"id"`
	Runner  string   `json:"runner"`
	Command []string `json:"command"`
}

func canonicalExecutionBytes(config ExecutionConfig) ([]byte, error) {
	value := canonicalExecution{SchemaVersion: 2, Policy: canonicalizePolicy(config.policy)}
	for _, profile := range config.profiles {
		canonicalProfile := canonicalProfile{
			ID: profile.id.String(), Runner: profile.runner.String(), Policy: canonicalizePolicy(profile.policy),
		}
		if profile.boundary != nil {
			boundary := canonicalizeProfileBoundaryPolicy(*profile.boundary)
			canonicalProfile.Boundary = &boundary
		}
		value.Profiles = append(value.Profiles, canonicalProfile)
	}
	for _, profile := range config.reviewProfiles {
		value.ReviewProfiles = append(value.ReviewProfiles, canonicalReviewProfile{
			ID: profile.id.String(), Runner: profile.runner.String(), ReviewerPolicy: profile.reviewerPolicy,
		})
	}
	for _, unit := range config.mergeUnits {
		canonicalUnit := canonicalUnitExecution{
			PlanID: unit.planID.String(), MergeUnitID: unit.mergeUnitID.String(), Profile: unit.profileID.String(),
			Policy: canonicalizePolicy(unit.policy), Boundary: canonicalizeAttemptBoundaryPolicy(unit.boundary),
		}
		if unit.commitProtocol != nil {
			protocol := canonicalizeCommitProtocol(*unit.commitProtocol)
			canonicalUnit.CommitProtocol = &protocol
		}
		if unit.reviewLoop != nil {
			loop := canonicalReviewLoop{
				Profiles:                 make([]string, 0, len(unit.reviewLoop.profiles)),
				MaxReviewRounds:          unit.reviewLoop.maxRounds,
				MaxInfrastructureRetries: unit.reviewLoop.maxInfrastructureRetries,
			}
			for _, profile := range unit.reviewLoop.profiles {
				loop.Profiles = append(loop.Profiles, profile.id.String())
			}
			canonicalUnit.ReviewLoop = &loop
		}
		value.MergeUnits = append(value.MergeUnits, canonicalUnit)
	}
	return json.Marshal(value)
}

func canonicalizeAttemptBoundaryPolicy(policy AttemptBoundaryPolicy) canonicalAttemptBoundaryPolicy {
	return canonicalAttemptBoundaryPolicy{
		Checkpoint: policy.checkpoint, Escalation: policy.escalation,
		SerialSegment: policy.serialSegment.String(),
	}
}

func canonicalizeProfileBoundaryPolicy(policy ProfileBoundaryPolicy) canonicalProfileBoundaryPolicy {
	return canonicalProfileBoundaryPolicy{Escalation: policy.escalation}
}

func canonicalizeCommitProtocol(protocol CommitProtocol) canonicalCommitProtocol {
	value := canonicalCommitProtocol{Steps: make([]canonicalCommitStep, 0, len(protocol.steps))}
	for _, step := range protocol.steps {
		value.Steps = append(value.Steps, canonicalizeCommitStep(step))
	}
	return value
}

func canonicalizeCommitStep(step CommitStep) canonicalCommitStep {
	allowed := make([]string, 0, len(step.paths.allowed))
	for _, pattern := range step.paths.allowed {
		allowed = append(allowed, pattern.value)
	}
	frozen := make([]string, 0, len(step.paths.frozen))
	for _, pattern := range step.paths.frozen {
		frozen = append(frozen, pattern.value)
	}
	return canonicalCommitStep{
		ID: step.id.String(), Subject: step.message.subject, BodyPolicy: step.message.body,
		ExactBody: step.message.exactBody, AllowedPaths: allowed, FrozenPaths: frozen,
		Checks: canonicalizeCommitChecks(step.checks),
	}
}

func canonicalizeCommitChecks(checks []CommitCheck) []canonicalCommitCheck {
	result := make([]canonicalCommitCheck, 0, len(checks))
	for _, check := range checks {
		result = append(result, canonicalCommitCheck{
			ID: check.id.String(), Runner: check.runner.String(), Command: check.command.Values(),
		})
	}
	return result
}

func canonicalizePolicy(policy ExecutionPolicy) canonicalPolicy {
	return canonicalPolicy{
		RequirePassingChecks: policy.requirePassingChecks,
		AllowWriteNetwork:    policy.allowWriteNetwork, MaxAttempts: policy.maxAttempts,
		MaxReviewRounds: policy.maxReviewRounds,
	}
}

type canonicalGeneration struct {
	SchemaVersion int                         `json:"schema_version"`
	WorkspaceID   string                      `json:"workspace_id"`
	Artifacts     []canonicalArtifactIdentity `json:"artifacts"`
}
type canonicalArtifactIdentity struct {
	Kind         ArtifactKind `json:"kind"`
	ID           string       `json:"id"`
	Path         string       `json:"path"`
	SourceHash   string       `json:"source_hash"`
	SemanticHash string       `json:"semantic_hash"`
}

func canonicalGenerationBytes(workspaceID ID, artifacts []NormalizedArtifact) ([]byte, error) {
	value := canonicalGeneration{
		SchemaVersion: 2, WorkspaceID: workspaceID.String(),
		Artifacts: []canonicalArtifactIdentity{},
	}
	for _, artifact := range artifacts {
		value.Artifacts = append(value.Artifacts, canonicalArtifactIdentity{
			Kind: artifact.kind, ID: artifact.id.String(), Path: artifact.path,
			SourceHash: artifact.sourceHash.String(), SemanticHash: artifact.semanticHash.String(),
		})
	}
	return json.Marshal(value)
}
