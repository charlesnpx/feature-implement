package workspace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	WorkspaceBundleFileName      = "feature.workspace.bundle.json"
	WorkspaceLockFileName        = "feature.workspace.lock.json"
	MaxWorkspaceBundleBytes      = 1 << 20
	WorkspaceBundleSchemaVersion = 2
)

type workspaceBundleWire struct {
	SchemaVersion   int      `json:"schema_version"`
	Workspace       string   `json:"workspace"`
	Plans           []string `json:"plans"`
	ExecutionConfig string   `json:"execution_config"`
}

// WorkspaceBundle is a validated, immutable set of local source bytes. The
// descriptor is discovery metadata incorporated into the effective generation.
type WorkspaceBundle struct {
	root             string
	descriptorDigest Digest
	sourcePaths      []string
	sourceFiles      map[string][]byte
	sources          DefinitionSources
	definition       EffectiveWorkspaceDefinition
}

func (bundle WorkspaceBundle) Root() string                             { return bundle.root }
func (bundle WorkspaceBundle) DescriptorDigest() Digest                 { return bundle.descriptorDigest }
func (bundle WorkspaceBundle) Definition() EffectiveWorkspaceDefinition { return bundle.definition }
func (bundle WorkspaceBundle) SourcePaths() []string {
	return append([]string(nil), bundle.sourcePaths...)
}
func (bundle WorkspaceBundle) Sources() DefinitionSources {
	return cloneDefinitionSources(bundle.sources)
}
func (bundle WorkspaceBundle) VerifyRoot() error {
	if bundle.root == "" {
		return fmt.Errorf("workspace bundle root is unavailable")
	}
	root, err := OpenVerifiedRoot(RootRolePlan, bundle.root, false)
	if err != nil {
		return fmt.Errorf("reopen workspace bundle root: %w", err)
	}
	defer root.Close()
	return root.VerifyPath()
}

// LoadWorkspaceBundle resolves a strict v2 bundle through the rooted
// filesystem adapter. It never follows a source path outside the bundle root
// and does not inspect or require a clean implementation checkout.
func LoadWorkspaceBundle(bundleRoot string) (WorkspaceBundle, error) {
	bundleRoot = filepath.Clean(strings.TrimSpace(bundleRoot))
	if !filepath.IsAbs(bundleRoot) {
		absolute, err := filepath.Abs(bundleRoot)
		if err != nil {
			return WorkspaceBundle{}, err
		}
		bundleRoot = absolute
	}
	bundleRoot, err := canonicalizeTrustedRootPath(bundleRoot)
	if err != nil {
		return WorkspaceBundle{}, err
	}
	if err := rejectReservedDerivedBundleRoot(bundleRoot); err != nil {
		return WorkspaceBundle{}, err
	}
	if err := rejectAncestorWorkspaceBundleRoot(bundleRoot); err != nil {
		return WorkspaceBundle{}, err
	}
	filesystem, err := OpenVerifiedRoot(RootRolePlan, bundleRoot, false)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("open workspace bundle: %w", err)
	}
	defer filesystem.Close()

	descriptor, err := readWorkspaceBundleFile(filesystem, WorkspaceBundleFileName, MaxWorkspaceBundleBytes)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("read %s: %w", WorkspaceBundleFileName, err)
	}
	var wire workspaceBundleWire
	if err := decodeStrictJSONRequired(descriptor, &wire); err != nil {
		return WorkspaceBundle{}, fmt.Errorf("decode %s: %w", WorkspaceBundleFileName, err)
	}
	if wire.SchemaVersion != WorkspaceBundleSchemaVersion {
		return WorkspaceBundle{}, fmt.Errorf("workspace bundle schema_version must be %d", WorkspaceBundleSchemaVersion)
	}
	workspacePath, err := normalizeBundleSourcePath("workspace", wire.Workspace)
	if err != nil {
		return WorkspaceBundle{}, err
	}
	executionPath, err := normalizeBundleSourcePath("execution_config", wire.ExecutionConfig)
	if err != nil {
		return WorkspaceBundle{}, err
	}
	sourceOwners := make(map[string]string)
	claimSourcePath := func(path, owner string) error {
		if prior, exists := sourceOwners[path]; exists {
			return fmt.Errorf("workspace bundle source path %s is claimed by both %s and %s", path, prior, owner)
		}
		sourceOwners[path] = owner
		return nil
	}
	if err := claimSourcePath(workspacePath, "workspace"); err != nil {
		return WorkspaceBundle{}, err
	}
	if err := claimSourcePath(executionPath, "execution_config"); err != nil {
		return WorkspaceBundle{}, err
	}
	workspaceBytes, err := readWorkspaceBundleFile(filesystem, workspacePath, MaxArtifactBytes)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("read workspace source %s: %w", workspacePath, err)
	}
	executionBytes, err := readWorkspaceBundleFile(filesystem, executionPath, MaxArtifactBytes)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("read execution config %s: %w", executionPath, err)
	}
	executionConfig, err := DecodeExecutionConfig(executionBytes)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("decode execution config %s: %w", executionPath, err)
	}
	policyPaths := reviewGatePolicyPaths(executionConfig)
	policies := make([]SourceArtifact, 0, len(policyPaths))
	for index, configuredPath := range policyPaths {
		policyPath, pathErr := normalizeBundleSourcePath(
			fmt.Sprintf("review_gate policy %d", index), configuredPath,
		)
		if pathErr != nil {
			return WorkspaceBundle{}, pathErr
		}
		if claimErr := claimSourcePath(policyPath, fmt.Sprintf("review_gate policy %d", index)); claimErr != nil {
			return WorkspaceBundle{}, claimErr
		}
		content, readErr := readWorkspaceBundleFile(filesystem, policyPath, MaxArtifactBytes)
		if readErr != nil {
			return WorkspaceBundle{}, fmt.Errorf("read review gate policy %s: %w", policyPath, readErr)
		}
		policies = append(policies, SourceArtifact{Path: policyPath, Bytes: content})
	}

	planPaths := make([]string, 0, len(wire.Plans))
	planSeen := make(map[string]struct{}, len(wire.Plans))
	for index, raw := range wire.Plans {
		path, err := normalizeBundleSourcePath(fmt.Sprintf("plans[%d]", index), raw)
		if err != nil {
			return WorkspaceBundle{}, err
		}
		if _, exists := planSeen[path]; exists {
			return WorkspaceBundle{}, fmt.Errorf("workspace bundle contains duplicate plan path %s", path)
		}
		if err := claimSourcePath(path, fmt.Sprintf("plans[%d]", index)); err != nil {
			return WorkspaceBundle{}, err
		}
		planSeen[path] = struct{}{}
		planPaths = append(planPaths, path)
	}
	sort.Strings(planPaths)
	plans := make([]SourceArtifact, 0, len(planPaths))
	for _, path := range planPaths {
		content, err := readWorkspaceBundleFile(filesystem, path, MaxArtifactBytes)
		if err != nil {
			return WorkspaceBundle{}, fmt.Errorf("read plan source %s: %w", path, err)
		}
		plans = append(plans, SourceArtifact{Path: path, Bytes: content})
	}

	sources := DefinitionSources{
		Workspace:       SourceArtifact{Path: workspacePath, Bytes: workspaceBytes},
		Plans:           plans,
		ExecutionConfig: SourceArtifact{Path: executionPath, Bytes: executionBytes},
		ReviewPolicies:  policies,
	}
	definition, err := ValidateDefinition(sources)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("validate workspace bundle: %w", err)
	}
	definition, err = bindWorkspaceBundleDefinition(
		definition, descriptor, workspacePath, planPaths, executionPath,
	)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("bind workspace bundle descriptor: %w", err)
	}
	if err := filesystem.VerifyPath(); err != nil {
		return WorkspaceBundle{}, fmt.Errorf("revalidate workspace bundle root: %w", err)
	}
	sourcePaths := make([]string, 0, len(sourceOwners)+1)
	sourcePaths = append(sourcePaths, WorkspaceBundleFileName)
	for sourcePath := range sourceOwners {
		sourcePaths = append(sourcePaths, sourcePath)
	}
	sort.Strings(sourcePaths)
	sourceFiles := make(map[string][]byte, len(sourcePaths))
	sourceFiles[WorkspaceBundleFileName] = append([]byte(nil), descriptor...)
	sourceFiles[workspacePath] = append([]byte(nil), workspaceBytes...)
	sourceFiles[executionPath] = append([]byte(nil), executionBytes...)
	for _, plan := range plans {
		sourceFiles[plan.Path] = append([]byte(nil), plan.Bytes...)
	}
	for _, policy := range policies {
		sourceFiles[policy.Path] = append([]byte(nil), policy.Bytes...)
	}
	return WorkspaceBundle{
		root:             filesystem.Path(),
		descriptorDigest: DigestBytes(descriptor),
		sourcePaths:      sourcePaths,
		sourceFiles:      sourceFiles,
		sources:          cloneDefinitionSources(sources), definition: definition,
	}, nil
}

func rejectReservedDerivedBundleRoot(bundleRoot string) error {
	base := filepath.Base(bundleRoot)
	for _, suffix := range []string{
		derivedRuntimeDirectorySuffix,
		derivedAttemptRootSuffix,
	} {
		if strings.HasSuffix(base, suffix) {
			return fmt.Errorf(
				"workspace bundle root basename %q ends in reserved derived suffix %q",
				base,
				suffix,
			)
		}
	}
	return nil
}

type canonicalWorkspaceBundle struct {
	SchemaVersion   int      `json:"schema_version"`
	Workspace       string   `json:"workspace"`
	Plans           []string `json:"plans"`
	ExecutionConfig string   `json:"execution_config"`
}

func bindWorkspaceBundleDefinition(
	definition EffectiveWorkspaceDefinition,
	descriptor []byte,
	workspacePath string,
	planPaths []string,
	executionPath string,
) (EffectiveWorkspaceDefinition, error) {
	canonical := canonicalWorkspaceBundle{
		SchemaVersion: WorkspaceBundleSchemaVersion,
		Workspace:     workspacePath, Plans: append([]string(nil), planPaths...),
		ExecutionConfig: executionPath,
	}
	canonicalBytes, err := json.Marshal(canonical)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}
	definition.artifacts = append(cloneArtifacts(definition.artifacts), newArtifact(
		ArtifactWorkspaceBundle, definition.workspace.id, WorkspaceBundleFileName, descriptor, canonicalBytes,
	))
	sort.Slice(definition.artifacts, func(i, j int) bool {
		left := string(definition.artifacts[i].kind) + "\x00" + definition.artifacts[i].id.String() + "\x00" + definition.artifacts[i].path
		right := string(definition.artifacts[j].kind) + "\x00" + definition.artifacts[j].id.String() + "\x00" + definition.artifacts[j].path
		return left < right
	})
	generationBytes, err := canonicalGenerationBytes(definition.workspace.id, definition.artifacts)
	if err != nil {
		return EffectiveWorkspaceDefinition{}, err
	}
	definition.generation = DigestBytes(generationBytes)
	return definition, nil
}

func normalizeBundleSourcePath(field, value string) (string, error) {
	path, err := normalizeSourcePath(value)
	if err != nil {
		return "", fmt.Errorf("workspace bundle %s: %w", field, err)
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(component, ".") {
			return "", fmt.Errorf("workspace bundle %s cannot reference hidden path %s", field, path)
		}
	}
	if path == WorkspaceBundleFileName || path == WorkspaceLockFileName {
		return "", fmt.Errorf("workspace bundle %s cannot reference its descriptor", field)
	}
	return path, nil
}

func readWorkspaceBundleFile(filesystem *VerifiedRoot, relative string, maximum int) ([]byte, error) {
	content, err := filesystem.ReadBounded(relative, int64(maximum))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("source is empty")
	}
	if len(content) > maximum {
		return nil, fmt.Errorf("source exceeds %d bytes", maximum)
	}
	return content, nil
}

func cloneDefinitionSources(source DefinitionSources) DefinitionSources {
	result := DefinitionSources{
		Workspace:       SourceArtifact{Path: source.Workspace.Path, Bytes: append([]byte(nil), source.Workspace.Bytes...)},
		ExecutionConfig: SourceArtifact{Path: source.ExecutionConfig.Path, Bytes: append([]byte(nil), source.ExecutionConfig.Bytes...)},
		Plans:           make([]SourceArtifact, 0, len(source.Plans)),
		ReviewPolicies:  make([]SourceArtifact, 0, len(source.ReviewPolicies)),
	}
	for _, plan := range source.Plans {
		result.Plans = append(result.Plans, SourceArtifact{Path: plan.Path, Bytes: append([]byte(nil), plan.Bytes...)})
	}
	for _, policy := range source.ReviewPolicies {
		result.ReviewPolicies = append(result.ReviewPolicies, SourceArtifact{Path: policy.Path, Bytes: append([]byte(nil), policy.Bytes...)})
	}
	return result
}

func WorkspaceBundleSchema() map[string]any {
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema_version", "workspace", "plans", "execution_config"},
		"properties": map[string]any{
			"schema_version":   map[string]any{"const": WorkspaceBundleSchemaVersion},
			"workspace":        map[string]any{"type": "string", "minLength": 1},
			"plans":            map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1}},
			"execution_config": map[string]any{"type": "string", "minLength": 1},
		},
	}
}
