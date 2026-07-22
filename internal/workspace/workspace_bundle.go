package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	WorkspaceBundleFileName      = "feature.workspace.bundle.json"
	WorkspaceLockFileName        = "feature.workspace.lock.json"
	WorkspaceGeneratedDirectory  = "generated"
	MaxWorkspaceBundleBytes      = 1 << 20
	WorkspaceBundleSchemaVersion = 2
)

type workspaceBundleWire struct {
	SchemaVersion         int                            `json:"schema_version"`
	Workspace             string                         `json:"workspace"`
	Plans                 []string                       `json:"plans"`
	ExecutionConfig       string                         `json:"execution_config"`
	Authorities           []workspaceBundleAuthorityWire `json:"authorities"`
	ControlPlaneAuthority string                         `json:"control_plane_authority,omitempty"`
}

type workspaceBundleAuthorityWire struct {
	ID                   string        `json:"id"`
	Kind                 AuthorityKind `json:"kind"`
	ContentPath          string        `json:"content_path"`
	RepositoryIdentity   string        `json:"repository_identity,omitempty"`
	CommitObject         string        `json:"commit_object,omitempty"`
	BlobObject           string        `json:"blob_object,omitempty"`
	ExpectedSourceDigest string        `json:"expected_source_digest,omitempty"`
}

// WorkspaceBundle is a validated, immutable set of source bytes and authority
// pins. The descriptor is discovery metadata only; every authority-bearing
// value it contains is incorporated into the effective generation.
type WorkspaceBundle struct {
	root                  string
	descriptorDigest      Digest
	sources               DefinitionSources
	definition            EffectiveWorkspaceDefinition
	controlPlaneAuthority ID
}

func (bundle WorkspaceBundle) Root() string                             { return bundle.root }
func (bundle WorkspaceBundle) DescriptorDigest() Digest                 { return bundle.descriptorDigest }
func (bundle WorkspaceBundle) Definition() EffectiveWorkspaceDefinition { return bundle.definition }
func (bundle WorkspaceBundle) Sources() DefinitionSources {
	return cloneDefinitionSources(bundle.sources)
}
func (bundle WorkspaceBundle) ControlPlaneAuthorityID() ID { return bundle.controlPlaneAuthority }

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
	filesystem, err := OpenRootedFilesystemAdapter(bundleRoot)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("open workspace bundle: %w", err)
	}
	defer filesystem.Close()

	descriptor, err := readWorkspaceBundleFile(filesystem, WorkspaceBundleFileName, MaxWorkspaceBundleBytes)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("read %s: %w", WorkspaceBundleFileName, err)
	}
	var wire workspaceBundleWire
	if err := decodeStrictJSON(descriptor, &wire); err != nil {
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

	authorities := make([]AuthorityMaterial, 0, len(wire.Authorities))
	authoritySeen := make(map[string]struct{}, len(wire.Authorities))
	authorityContentPaths := make(map[string]string, len(wire.Authorities))
	for index, item := range wire.Authorities {
		id, err := NewID(item.ID)
		if err != nil {
			return WorkspaceBundle{}, fmt.Errorf("authorities[%d].id: %w", index, err)
		}
		if _, exists := authoritySeen[id.String()]; exists {
			return WorkspaceBundle{}, fmt.Errorf("workspace bundle contains duplicate authority %s", id)
		}
		authoritySeen[id.String()] = struct{}{}
		contentPath, err := normalizeBundleSourcePath(fmt.Sprintf("authorities[%d].content_path", index), item.ContentPath)
		if err != nil {
			return WorkspaceBundle{}, err
		}
		if err := claimSourcePath(contentPath, fmt.Sprintf("authorities[%d]", index)); err != nil {
			return WorkspaceBundle{}, err
		}
		authorityContentPaths[id.String()] = contentPath
		content, err := readWorkspaceBundleFile(filesystem, contentPath, MaxArtifactBytes)
		if err != nil {
			return WorkspaceBundle{}, fmt.Errorf("read authority %s content %s: %w", id, contentPath, err)
		}
		authorities = append(authorities, AuthorityMaterial{
			ID: id.String(), Kind: item.Kind, Content: content,
			RepositoryIdentity: item.RepositoryIdentity, CommitObject: item.CommitObject,
			BlobObject: item.BlobObject, ExpectedSourceDigest: item.ExpectedSourceDigest,
		})
	}
	sort.Slice(authorities, func(i, j int) bool { return authorities[i].ID < authorities[j].ID })
	controlPlaneAuthority := ID{}
	if strings.TrimSpace(wire.ControlPlaneAuthority) != "" {
		controlPlaneAuthority, err = NewID(wire.ControlPlaneAuthority)
		if err != nil {
			return WorkspaceBundle{}, fmt.Errorf("control_plane_authority: %w", err)
		}
		if _, exists := authoritySeen[controlPlaneAuthority.String()]; !exists {
			return WorkspaceBundle{}, fmt.Errorf("control_plane_authority %s is not present in authorities", controlPlaneAuthority)
		}
	}

	sources := DefinitionSources{
		Workspace:       SourceArtifact{Path: workspacePath, Bytes: workspaceBytes},
		Plans:           plans,
		ExecutionConfig: SourceArtifact{Path: executionPath, Bytes: executionBytes},
		Authorities:     authorities,
	}
	definition, err := ValidateDefinition(sources)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("validate workspace bundle: %w", err)
	}
	definition, err = bindWorkspaceBundleDefinition(
		definition, descriptor, workspacePath, planPaths, executionPath,
		authorityContentPaths, controlPlaneAuthority,
	)
	if err != nil {
		return WorkspaceBundle{}, fmt.Errorf("bind workspace bundle authority: %w", err)
	}
	return WorkspaceBundle{
		root: filesystem.Root(), descriptorDigest: DigestBytes(descriptor),
		sources: cloneDefinitionSources(sources), definition: definition,
		controlPlaneAuthority: controlPlaneAuthority,
	}, nil
}

type canonicalWorkspaceBundleAuthority struct {
	ID                   string        `json:"id"`
	Kind                 AuthorityKind `json:"kind"`
	ContentPath          string        `json:"content_path"`
	RepositoryIdentity   string        `json:"repository_identity,omitempty"`
	CommitObject         string        `json:"commit_object,omitempty"`
	BlobObject           string        `json:"blob_object,omitempty"`
	ExpectedSourceDigest string        `json:"expected_source_digest,omitempty"`
}

type canonicalWorkspaceBundle struct {
	SchemaVersion         int                                 `json:"schema_version"`
	Workspace             string                              `json:"workspace"`
	Plans                 []string                            `json:"plans"`
	ExecutionConfig       string                              `json:"execution_config"`
	Authorities           []canonicalWorkspaceBundleAuthority `json:"authorities"`
	ControlPlaneAuthority string                              `json:"control_plane_authority,omitempty"`
}

func bindWorkspaceBundleDefinition(
	definition EffectiveWorkspaceDefinition,
	descriptor []byte,
	workspacePath string,
	planPaths []string,
	executionPath string,
	authorityContentPaths map[string]string,
	controlPlaneAuthority ID,
) (EffectiveWorkspaceDefinition, error) {
	canonical := canonicalWorkspaceBundle{
		SchemaVersion: WorkspaceBundleSchemaVersion,
		Workspace:     workspacePath, Plans: append([]string(nil), planPaths...),
		ExecutionConfig: executionPath, Authorities: []canonicalWorkspaceBundleAuthority{},
		ControlPlaneAuthority: controlPlaneAuthority.String(),
	}
	for _, snapshot := range definition.authorities {
		contentPath, exists := authorityContentPaths[snapshot.id.String()]
		if !exists {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("authority %s has no descriptor content path", snapshot.id)
		}
		item := canonicalWorkspaceBundleAuthority{
			ID: snapshot.id.String(), Kind: snapshot.kind, ContentPath: contentPath,
		}
		if pin, ok := snapshot.GitPin(); ok {
			item.RepositoryIdentity = pin.Repository().String()
			item.CommitObject = pin.Commit().String()
			item.BlobObject = pin.Blob().String()
		} else if digest, ok := snapshot.ExternalDigest(); ok {
			item.ExpectedSourceDigest = digest.String()
		} else {
			return EffectiveWorkspaceDefinition{}, fmt.Errorf("authority %s has no canonical pin", snapshot.id)
		}
		canonical.Authorities = append(canonical.Authorities, item)
	}
	sort.Slice(canonical.Authorities, func(i, j int) bool {
		return canonical.Authorities[i].ID < canonical.Authorities[j].ID
	})
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
	generationBytes, err := canonicalGenerationBytes(definition.workspace.id, definition.artifacts, definition.authorities)
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
	if path == WorkspaceBundleFileName {
		return "", fmt.Errorf("workspace bundle %s cannot reference its descriptor", field)
	}
	if strings.Split(filepath.ToSlash(path), "/")[0] == WorkspaceGeneratedDirectory {
		return "", fmt.Errorf("workspace bundle %s cannot reference tool-owned generated path %s", field, path)
	}
	return path, nil
}

func readWorkspaceBundleFile(filesystem *RootedFilesystemAdapter, relative string, maximum int) ([]byte, error) {
	rooted, err := NewRootedPath(filesystem.Root(), relative)
	if err != nil {
		return nil, err
	}
	content, err := filesystem.ReadFile(context.Background(), rooted)
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
		Authorities:     make([]AuthorityMaterial, 0, len(source.Authorities)),
	}
	for _, plan := range source.Plans {
		result.Plans = append(result.Plans, SourceArtifact{Path: plan.Path, Bytes: append([]byte(nil), plan.Bytes...)})
	}
	for _, authority := range source.Authorities {
		copyAuthority := authority
		copyAuthority.Content = append([]byte(nil), authority.Content...)
		result.Authorities = append(result.Authorities, copyAuthority)
	}
	return result
}

func WorkspaceBundleSchema() map[string]any {
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema_version", "workspace", "plans", "execution_config", "authorities"},
		"properties": map[string]any{
			"schema_version":          map[string]any{"const": WorkspaceBundleSchemaVersion},
			"workspace":               map[string]any{"type": "string", "minLength": 1},
			"plans":                   map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1}},
			"execution_config":        map[string]any{"type": "string", "minLength": 1},
			"control_plane_authority": map[string]any{"type": "string", "minLength": 1},
			"authorities": map[string]any{
				"type": "array", "items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"id", "kind", "content_path"},
					"properties": map[string]any{
						"id":                     map[string]any{"type": "string", "minLength": 1},
						"kind":                   map[string]any{"enum": []string{string(AuthorityGitBlob), string(AuthorityExternalDigest)}},
						"content_path":           map[string]any{"type": "string", "minLength": 1},
						"repository_identity":    map[string]any{"type": "string"},
						"commit_object":          map[string]any{"type": "string"},
						"blob_object":            map[string]any{"type": "string"},
						"expected_source_digest": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

func WorkspaceBundleLockArtifacts(bundle WorkspaceBundle) ([]MaterializationArtifact, error) {
	if bundle.definition.generation.IsZero() || bundle.root == "" {
		return nil, fmt.Errorf("validated workspace bundle is required")
	}
	workspaceLock, err := json.Marshal(ProjectWorkspaceLock(bundle.definition))
	if err != nil {
		return nil, err
	}
	workspaceArtifact, err := NewMaterializationArtifact("workspace-lock", WorkspaceLockFileName, append(workspaceLock, '\n'))
	if err != nil {
		return nil, err
	}
	artifacts := []MaterializationArtifact{workspaceArtifact}
	for _, lock := range ProjectPlanLocks(bundle.definition) {
		content, err := json.Marshal(lock)
		if err != nil {
			return nil, err
		}
		path := filepath.ToSlash(filepath.Join("plans", lock.PlanID().String()+".lock.json"))
		artifact, err := NewMaterializationArtifact("plan-lock-"+lock.PlanID().String(), path, append(content, '\n'))
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}
