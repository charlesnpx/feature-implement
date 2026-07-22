package workspace

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type ProviderIdentity struct {
	kind       ID
	repository string
}

func NewProviderIdentity(kind ID, repository string) (ProviderIdentity, error) {
	if kind.IsZero() {
		return ProviderIdentity{}, fmt.Errorf("provider kind is required")
	}
	repository = strings.TrimSpace(repository)
	if err := validateBoundedText("provider repository", repository, 2048); err != nil {
		return ProviderIdentity{}, err
	}
	return ProviderIdentity{kind: kind, repository: repository}, nil
}

func (identity ProviderIdentity) Kind() ID           { return identity.kind }
func (identity ProviderIdentity) Repository() string { return identity.repository }

type PlanReference struct {
	id     ID
	source string
}

func (reference PlanReference) ID() ID         { return reference.id }
func (reference PlanReference) Source() string { return reference.source }

type MergeUnitReference struct {
	planID      ID
	mergeUnitID ID
}

func NewMergeUnitReference(planID, mergeUnitID ID) (MergeUnitReference, error) {
	if planID.IsZero() || mergeUnitID.IsZero() {
		return MergeUnitReference{}, fmt.Errorf("merge unit reference requires plan and merge unit identifiers")
	}
	return MergeUnitReference{planID: planID, mergeUnitID: mergeUnitID}, nil
}

func (reference MergeUnitReference) PlanID() ID      { return reference.planID }
func (reference MergeUnitReference) MergeUnitID() ID { return reference.mergeUnitID }
func (reference MergeUnitReference) String() string {
	if reference.planID.IsZero() || reference.mergeUnitID.IsZero() {
		return ""
	}
	return reference.planID.String() + "/" + reference.mergeUnitID.String()
}

func (reference MergeUnitReference) key() string {
	return reference.planID.String() + "\x00" + reference.mergeUnitID.String()
}

type WorkspaceDependency struct {
	before MergeUnitReference
	after  MergeUnitReference
}

func (dependency WorkspaceDependency) Before() MergeUnitReference { return dependency.before }
func (dependency WorkspaceDependency) After() MergeUnitReference  { return dependency.after }

type AuthorityKind string

const (
	AuthorityGitBlob        AuthorityKind = "git_blob"
	AuthorityExternalDigest AuthorityKind = "external_digest"
)

func (kind AuthorityKind) valid() bool {
	return kind == AuthorityGitBlob || kind == AuthorityExternalDigest
}

type AuthorityReference struct {
	id       ID
	kind     AuthorityKind
	location string
}

func (reference AuthorityReference) ID() ID              { return reference.id }
func (reference AuthorityReference) Kind() AuthorityKind { return reference.kind }
func (reference AuthorityReference) Location() string    { return reference.location }

// WorkspaceManifest is the sole owner of workspace composition and discovery
// metadata. Its fields are private and collection accessors return copies.
type WorkspaceManifest struct {
	id               ID
	repositoryRoot   string
	repository       RepositoryIdentity
	provider         ProviderIdentity
	baseRef          string
	remote           string
	executionConfig  string
	plans            []PlanReference
	dependencies     []WorkspaceDependency
	authoritySources []AuthorityReference
}

func DecodeWorkspaceManifest(source []byte) (WorkspaceManifest, error) {
	var wire workspaceWire
	if err := decodeStrictV2("workspace manifest", source, &wire); err != nil {
		return WorkspaceManifest{}, err
	}
	return normalizeWorkspace(wire)
}

func normalizeWorkspace(wire workspaceWire) (WorkspaceManifest, error) {
	id, err := NewID(wire.ID)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("workspace id: %w", err)
	}
	repositoryRoot := filepath.Clean(strings.TrimSpace(wire.Repository.Root))
	if wire.Repository.Root == "" || !filepath.IsAbs(repositoryRoot) {
		return WorkspaceManifest{}, fmt.Errorf("repository.root must be an absolute path")
	}
	repository, err := NewRepositoryIdentity(wire.Repository.Identity)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	providerKind, err := NewID(wire.Provider.Kind)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("provider.kind: %w", err)
	}
	provider, err := NewProviderIdentity(providerKind, wire.Provider.Repository)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	if provider.repository == "local-only" {
		return WorkspaceManifest{}, fmt.Errorf("workspace v2 does not support local-only execution")
	}
	if provider.kind.String() != "github" {
		return WorkspaceManifest{}, fmt.Errorf("provider.kind %q is unsupported; workspace v2 supports github", provider.kind)
	}
	providerRepositoryParts := strings.Split(provider.repository, "/")
	if len(providerRepositoryParts) != 2 ||
		!validGitHubRepositoryComponent(providerRepositoryParts[0]) ||
		!validGitHubRepositoryComponent(providerRepositoryParts[1]) {
		return WorkspaceManifest{}, fmt.Errorf("provider.repository must be GitHub owner/repository")
	}
	baseRef, err := normalizeToken("base_ref", wire.BaseRef, 1024)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	remote, err := normalizeToken("remote", wire.Remote, 512)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	if remote == "local-only" {
		return WorkspaceManifest{}, fmt.Errorf("workspace v2 does not support local-only execution")
	}
	executionConfig, err := normalizeSourcePath(wire.ExecutionConfig)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("execution_config: %w", err)
	}
	if len(wire.Plans) == 0 {
		return WorkspaceManifest{}, fmt.Errorf("workspace requires at least one plan")
	}
	if wire.Dependencies == nil {
		return WorkspaceManifest{}, fmt.Errorf("workspace dependencies must be explicit, including an empty list")
	}
	if wire.AuthoritySources == nil {
		return WorkspaceManifest{}, fmt.Errorf("workspace authority_sources must be explicit, including an empty list")
	}

	plans := make([]PlanReference, 0, len(wire.Plans))
	planIDs := make(map[string]struct{}, len(wire.Plans))
	planSources := make(map[string]struct{}, len(wire.Plans))
	for index, item := range wire.Plans {
		planID, err := NewID(item.ID)
		if err != nil {
			return WorkspaceManifest{}, fmt.Errorf("plans[%d].id: %w", index, err)
		}
		sourcePath, err := normalizeSourcePath(item.Source)
		if err != nil {
			return WorkspaceManifest{}, fmt.Errorf("plans[%d].source: %w", index, err)
		}
		if _, exists := planIDs[planID.String()]; exists {
			return WorkspaceManifest{}, fmt.Errorf("duplicate workspace plan id %s", planID)
		}
		if _, exists := planSources[sourcePath]; exists {
			return WorkspaceManifest{}, fmt.Errorf("duplicate workspace plan source %s", sourcePath)
		}
		planIDs[planID.String()] = struct{}{}
		planSources[sourcePath] = struct{}{}
		plans = append(plans, PlanReference{id: planID, source: sourcePath})
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].id.String() < plans[j].id.String() })

	dependencies := make([]WorkspaceDependency, 0, len(*wire.Dependencies))
	dependencyKeys := make(map[string]struct{}, len(*wire.Dependencies))
	for index, item := range *wire.Dependencies {
		before, err := normalizeMergeUnitReference(item.Before, fmt.Sprintf("dependencies[%d].before", index), planIDs)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		after, err := normalizeMergeUnitReference(item.After, fmt.Sprintf("dependencies[%d].after", index), planIDs)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		if before.planID == after.planID {
			return WorkspaceManifest{}, fmt.Errorf("workspace dependency %s/%s -> %s/%s must cross plans", before.planID, before.mergeUnitID, after.planID, after.mergeUnitID)
		}
		key := before.key() + "\x00" + after.key()
		if _, exists := dependencyKeys[key]; exists {
			return WorkspaceManifest{}, fmt.Errorf("duplicate dependency %s/%s -> %s/%s", before.planID, before.mergeUnitID, after.planID, after.mergeUnitID)
		}
		dependencyKeys[key] = struct{}{}
		dependencies = append(dependencies, WorkspaceDependency{before: before, after: after})
	}
	sort.Slice(dependencies, func(i, j int) bool {
		left := dependencies[i].before.key() + "\x00" + dependencies[i].after.key()
		right := dependencies[j].before.key() + "\x00" + dependencies[j].after.key()
		return left < right
	})
	if err := validateWorkspaceDependencyDAG(dependencies); err != nil {
		return WorkspaceManifest{}, err
	}

	authorities := make([]AuthorityReference, 0, len(*wire.AuthoritySources))
	authorityIDs := make(map[string]struct{}, len(*wire.AuthoritySources))
	for index, item := range *wire.AuthoritySources {
		authorityID, err := NewID(item.ID)
		if err != nil {
			return WorkspaceManifest{}, fmt.Errorf("authority_sources[%d].id: %w", index, err)
		}
		kind := AuthorityKind(strings.TrimSpace(item.Kind))
		if !kind.valid() {
			return WorkspaceManifest{}, fmt.Errorf("authority source %s has unsupported kind %q", authorityID, item.Kind)
		}
		location := strings.TrimSpace(item.Location)
		if kind == AuthorityGitBlob {
			location, err = normalizeSourcePath(location)
		} else {
			err = validateBoundedText("external authority location", location, 4096)
		}
		if err != nil {
			return WorkspaceManifest{}, fmt.Errorf("authority source %s: %w", authorityID, err)
		}
		if _, exists := authorityIDs[authorityID.String()]; exists {
			return WorkspaceManifest{}, fmt.Errorf("duplicate authority source %s", authorityID)
		}
		authorityIDs[authorityID.String()] = struct{}{}
		authorities = append(authorities, AuthorityReference{id: authorityID, kind: kind, location: location})
	}
	sort.Slice(authorities, func(i, j int) bool { return authorities[i].id.String() < authorities[j].id.String() })

	return WorkspaceManifest{
		id: id, repositoryRoot: repositoryRoot, repository: repository, provider: provider,
		baseRef: baseRef, remote: remote, executionConfig: executionConfig,
		plans: plans, dependencies: dependencies, authoritySources: authorities,
	}, nil
}

func validGitHubRepositoryComponent(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func (manifest WorkspaceManifest) ID() ID                         { return manifest.id }
func (manifest WorkspaceManifest) RepositoryRoot() string         { return manifest.repositoryRoot }
func (manifest WorkspaceManifest) Repository() RepositoryIdentity { return manifest.repository }
func (manifest WorkspaceManifest) Provider() ProviderIdentity     { return manifest.provider }
func (manifest WorkspaceManifest) BaseRef() string                { return manifest.baseRef }
func (manifest WorkspaceManifest) Remote() string                 { return manifest.remote }
func (manifest WorkspaceManifest) ExecutionConfigSource() string  { return manifest.executionConfig }
func (manifest WorkspaceManifest) Plans() []PlanReference {
	return append([]PlanReference(nil), manifest.plans...)
}
func (manifest WorkspaceManifest) Dependencies() []WorkspaceDependency {
	return append([]WorkspaceDependency(nil), manifest.dependencies...)
}
func (manifest WorkspaceManifest) AuthoritySources() []AuthorityReference {
	return append([]AuthorityReference(nil), manifest.authoritySources...)
}

type Story struct {
	id             ID
	summary        string
	acceptance     []string
	implementation []string
	testing        []string
	dependencies   []ID
}

func (story Story) ID() ID                   { return story.id }
func (story Story) Summary() string          { return story.summary }
func (story Story) Acceptance() []string     { return append([]string(nil), story.acceptance...) }
func (story Story) Implementation() []string { return append([]string(nil), story.implementation...) }
func (story Story) Testing() []string        { return append([]string(nil), story.testing...) }
func (story Story) Dependencies() []ID       { return append([]ID(nil), story.dependencies...) }

type MergeUnit struct {
	id       ID
	name     string
	storyIDs []ID
}

func (unit MergeUnit) ID() ID         { return unit.id }
func (unit MergeUnit) Name() string   { return unit.name }
func (unit MergeUnit) StoryIDs() []ID { return append([]ID(nil), unit.storyIDs...) }

// Plan owns only plan-local semantics: stories, their dependencies, and merge
// unit composition. It has no repository, remote, execution policy, or runtime
// lifecycle fields.
type Plan struct {
	id         ID
	title      string
	stories    []Story
	mergeUnits []MergeUnit
}

func DecodePlan(source []byte) (Plan, error) {
	var wire planWire
	if err := decodeStrictV2("plan", source, &wire); err != nil {
		return Plan{}, err
	}
	return normalizePlan(wire)
}

func normalizePlan(wire planWire) (Plan, error) {
	id, err := NewID(wire.ID)
	if err != nil {
		return Plan{}, fmt.Errorf("plan id: %w", err)
	}
	title := strings.TrimSpace(wire.Title)
	if err := validateBoundedText("plan title", title, 1024); err != nil {
		return Plan{}, err
	}
	if len(wire.Stories) == 0 || len(wire.MergeUnits) == 0 {
		return Plan{}, fmt.Errorf("plan requires stories and merge_units")
	}

	stories := make([]Story, 0, len(wire.Stories))
	storyIDs := make(map[string]struct{}, len(wire.Stories))
	for index, item := range wire.Stories {
		storyID, err := NewID(item.ID)
		if err != nil {
			return Plan{}, fmt.Errorf("stories[%d].id: %w", index, err)
		}
		if _, exists := storyIDs[storyID.String()]; exists {
			return Plan{}, fmt.Errorf("duplicate story %s", storyID)
		}
		storyIDs[storyID.String()] = struct{}{}
		if item.Dependencies == nil {
			return Plan{}, fmt.Errorf("story %s dependencies must be explicit, including an empty list", storyID)
		}
		summary := strings.TrimSpace(item.Summary)
		if err := validateBoundedText("story summary", summary, 16*1024); err != nil {
			return Plan{}, fmt.Errorf("story %s: %w", storyID, err)
		}
		acceptance, err := normalizeNonEmptyTextList("acceptance", item.Acceptance)
		if err != nil {
			return Plan{}, fmt.Errorf("story %s: %w", storyID, err)
		}
		implementation, err := normalizeNonEmptyTextList("implementation", item.Implementation)
		if err != nil {
			return Plan{}, fmt.Errorf("story %s: %w", storyID, err)
		}
		testing, err := normalizeNonEmptyTextList("testing", item.Testing)
		if err != nil {
			return Plan{}, fmt.Errorf("story %s: %w", storyID, err)
		}
		dependencies := make([]ID, 0, len(*item.Dependencies))
		seenDependencies := make(map[string]struct{}, len(*item.Dependencies))
		for _, raw := range *item.Dependencies {
			dependency, err := NewID(raw)
			if err != nil {
				return Plan{}, fmt.Errorf("story %s dependency: %w", storyID, err)
			}
			if dependency == storyID {
				return Plan{}, fmt.Errorf("story %s cannot depend on itself", storyID)
			}
			if _, exists := seenDependencies[dependency.String()]; exists {
				return Plan{}, fmt.Errorf("story %s has duplicate dependency %s", storyID, dependency)
			}
			seenDependencies[dependency.String()] = struct{}{}
			dependencies = append(dependencies, dependency)
		}
		sort.Slice(dependencies, func(i, j int) bool { return dependencies[i].String() < dependencies[j].String() })
		stories = append(stories, Story{
			id: storyID, summary: summary, acceptance: acceptance,
			implementation: implementation, testing: testing, dependencies: dependencies,
		})
	}
	for _, story := range stories {
		for _, dependency := range story.dependencies {
			if _, exists := storyIDs[dependency.String()]; !exists {
				return Plan{}, fmt.Errorf("story %s depends on unknown story %s", story.id, dependency)
			}
		}
	}
	if err := validateStoryDAG(stories); err != nil {
		return Plan{}, err
	}

	units := make([]MergeUnit, 0, len(wire.MergeUnits))
	unitIDs := make(map[string]struct{}, len(wire.MergeUnits))
	assignedStories := make(map[string]string, len(stories))
	for index, item := range wire.MergeUnits {
		unitID, err := NewID(item.ID)
		if err != nil {
			return Plan{}, fmt.Errorf("merge_units[%d].id: %w", index, err)
		}
		if _, exists := unitIDs[unitID.String()]; exists {
			return Plan{}, fmt.Errorf("duplicate merge unit %s", unitID)
		}
		unitIDs[unitID.String()] = struct{}{}
		name := strings.TrimSpace(item.Name)
		if err := validateBoundedText("merge unit name", name, 1024); err != nil {
			return Plan{}, fmt.Errorf("merge unit %s: %w", unitID, err)
		}
		if len(item.StoryIDs) == 0 {
			return Plan{}, fmt.Errorf("merge unit %s requires story_ids", unitID)
		}
		unitStories := make([]ID, 0, len(item.StoryIDs))
		for _, raw := range item.StoryIDs {
			storyID, err := NewID(raw)
			if err != nil {
				return Plan{}, fmt.Errorf("merge unit %s story id: %w", unitID, err)
			}
			if _, exists := storyIDs[storyID.String()]; !exists {
				return Plan{}, fmt.Errorf("merge unit %s references unknown story %s", unitID, storyID)
			}
			if prior := assignedStories[storyID.String()]; prior != "" {
				return Plan{}, fmt.Errorf("story %s is assigned to both %s and %s", storyID, prior, unitID)
			}
			assignedStories[storyID.String()] = unitID.String()
			unitStories = append(unitStories, storyID)
		}
		units = append(units, MergeUnit{id: unitID, name: name, storyIDs: unitStories})
	}
	for storyID := range storyIDs {
		if assignedStories[storyID] == "" {
			return Plan{}, fmt.Errorf("story %s is not assigned to a merge unit", storyID)
		}
	}

	return Plan{id: id, title: title, stories: stories, mergeUnits: units}, nil
}

func (plan Plan) ID() ID                  { return plan.id }
func (plan Plan) Title() string           { return plan.title }
func (plan Plan) Stories() []Story        { return append([]Story(nil), plan.stories...) }
func (plan Plan) MergeUnits() []MergeUnit { return append([]MergeUnit(nil), plan.mergeUnits...) }

func normalizeSourcePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || strings.Contains(value, "\\") || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("source path is required and must use forward slashes")
	}
	clean := path.Clean(value)
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("source path %q must remain relative", value)
	}
	return clean, nil
}

func normalizeToken(name, value string, maxBytes int) (string, error) {
	value = strings.TrimSpace(value)
	if err := validateBoundedText(name, value, maxBytes); err != nil {
		return "", err
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("%s must not contain whitespace", name)
	}
	return value, nil
}

func normalizeNonEmptyTextList(name string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%s requires at least one item", name)
	}
	result := append([]string(nil), values...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
		if err := validateBoundedText(fmt.Sprintf("%s item %d", name, index), result[index], 64*1024); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func normalizeMergeUnitReference(wire mergeUnitReferenceWire, location string, planIDs map[string]struct{}) (MergeUnitReference, error) {
	planID, err := NewID(wire.PlanID)
	if err != nil {
		return MergeUnitReference{}, fmt.Errorf("%s.plan_id: %w", location, err)
	}
	mergeUnitID, err := NewID(wire.MergeUnitID)
	if err != nil {
		return MergeUnitReference{}, fmt.Errorf("%s.merge_unit_id: %w", location, err)
	}
	if _, exists := planIDs[planID.String()]; !exists {
		return MergeUnitReference{}, fmt.Errorf("%s references unknown plan %s", location, planID)
	}
	return MergeUnitReference{planID: planID, mergeUnitID: mergeUnitID}, nil
}

func validateWorkspaceDependencyDAG(dependencies []WorkspaceDependency) error {
	edges := make(map[string][]string, len(dependencies))
	idSet := make(map[string]struct{}, len(dependencies)*2)
	for _, dependency := range dependencies {
		before := dependency.before.key()
		after := dependency.after.key()
		idSet[before] = struct{}{}
		idSet[after] = struct{}{}
		edges[after] = append(edges[after], before)
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return validateDAG("workspace merge-unit", ids, edges)
}

func validateStoryDAG(stories []Story) error {
	ids := make([]string, 0, len(stories))
	edges := make(map[string][]string, len(stories))
	for _, story := range stories {
		ids = append(ids, story.id.String())
		for _, dependency := range story.dependencies {
			edges[story.id.String()] = append(edges[story.id.String()], dependency.String())
		}
	}
	return validateDAG("story", ids, edges)
}

func validateDAG(kind string, ids []string, edges map[string][]string) error {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(ids))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("%s dependency cycle includes %s", kind, id)
		case visited:
			return nil
		}
		state[id] = visiting
		for _, dependency := range edges[id] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
