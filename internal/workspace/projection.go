package workspace

import "encoding/json"

// ArtifactProjection is generated evidence about one validated source. It is
// not accepted by ValidateDefinition as input authority.
type ArtifactProjection struct {
	kind         ArtifactKind
	id           ID
	path         string
	sourceHash   Digest
	semanticHash Digest
}

func (projection ArtifactProjection) Kind() ArtifactKind   { return projection.kind }
func (projection ArtifactProjection) ID() ID               { return projection.id }
func (projection ArtifactProjection) Path() string         { return projection.path }
func (projection ArtifactProjection) SourceHash() Digest   { return projection.sourceHash }
func (projection ArtifactProjection) SemanticHash() Digest { return projection.semanticHash }

type AuthorityProjection struct {
	id           ID
	kind         AuthorityKind
	location     string
	sourceHash   Digest
	semanticHash Digest
}

func (projection AuthorityProjection) ID() ID               { return projection.id }
func (projection AuthorityProjection) Kind() AuthorityKind  { return projection.kind }
func (projection AuthorityProjection) Location() string     { return projection.location }
func (projection AuthorityProjection) SourceHash() Digest   { return projection.sourceHash }
func (projection AuthorityProjection) SemanticHash() Digest { return projection.semanticHash }

// WorkspaceLockProjection deliberately contains no runtime state. It is a
// disposable projection of an EffectiveWorkspaceDefinition.
type WorkspaceLockProjection struct {
	schemaVersion int
	workspaceID   ID
	generation    Digest
	artifacts     []ArtifactProjection
	authorities   []AuthorityProjection
}

func ProjectWorkspaceLock(definition EffectiveWorkspaceDefinition) WorkspaceLockProjection {
	artifacts := make([]ArtifactProjection, 0, len(definition.artifacts))
	for _, artifact := range definition.artifacts {
		artifacts = append(artifacts, ArtifactProjection{
			kind: artifact.kind, id: artifact.id, path: artifact.path,
			sourceHash: artifact.sourceHash, semanticHash: artifact.semanticHash,
		})
	}
	authorities := make([]AuthorityProjection, 0, len(definition.authorities))
	for _, authority := range definition.authorities {
		authorities = append(authorities, AuthorityProjection{
			id: authority.id, kind: authority.kind, location: authority.location,
			sourceHash: authority.sourceHash, semanticHash: authority.semanticHash,
		})
	}
	return WorkspaceLockProjection{
		schemaVersion: 2, workspaceID: definition.workspace.id,
		generation: definition.generation, artifacts: artifacts, authorities: authorities,
	}
}

func (projection WorkspaceLockProjection) SchemaVersion() int { return projection.schemaVersion }
func (projection WorkspaceLockProjection) WorkspaceID() ID    { return projection.workspaceID }
func (projection WorkspaceLockProjection) Generation() Digest { return projection.generation }
func (projection WorkspaceLockProjection) Artifacts() []ArtifactProjection {
	return append([]ArtifactProjection(nil), projection.artifacts...)
}
func (projection WorkspaceLockProjection) Authorities() []AuthorityProjection {
	return append([]AuthorityProjection(nil), projection.authorities...)
}

func (projection WorkspaceLockProjection) MarshalJSON() ([]byte, error) {
	type artifactJSON struct {
		Kind         ArtifactKind `json:"kind"`
		ID           string       `json:"id"`
		Path         string       `json:"path"`
		SourceHash   string       `json:"source_hash"`
		SemanticHash string       `json:"semantic_hash"`
	}
	type authorityJSON struct {
		ID           string        `json:"id"`
		Kind         AuthorityKind `json:"kind"`
		Location     string        `json:"location"`
		SourceHash   string        `json:"source_hash"`
		SemanticHash string        `json:"semantic_hash"`
	}
	type workspaceLockJSON struct {
		SchemaVersion int             `json:"schema_version"`
		WorkspaceID   string          `json:"workspace_id"`
		Generation    string          `json:"generation"`
		Artifacts     []artifactJSON  `json:"artifacts"`
		Authorities   []authorityJSON `json:"authorities"`
	}
	value := workspaceLockJSON{
		SchemaVersion: projection.schemaVersion,
		WorkspaceID:   projection.workspaceID.String(),
		Generation:    projection.generation.String(),
		Artifacts:     make([]artifactJSON, 0, len(projection.artifacts)),
		Authorities:   make([]authorityJSON, 0, len(projection.authorities)),
	}
	for _, artifact := range projection.artifacts {
		value.Artifacts = append(value.Artifacts, artifactJSON{
			Kind: artifact.kind, ID: artifact.id.String(), Path: artifact.path,
			SourceHash: artifact.sourceHash.String(), SemanticHash: artifact.semanticHash.String(),
		})
	}
	for _, authority := range projection.authorities {
		value.Authorities = append(value.Authorities, authorityJSON{
			ID: authority.id.String(), Kind: authority.kind, Location: authority.location,
			SourceHash: authority.sourceHash.String(), SemanticHash: authority.semanticHash.String(),
		})
	}
	return json.Marshal(value)
}

// PlanLockProjection contains source identity only: no base, remote, execution
// policy, approvals, or mutable lifecycle fields.
type PlanLockProjection struct {
	schemaVersion int
	planID        ID
	sourceHash    Digest
	semanticHash  Digest
	generation    Digest
}

func ProjectPlanLocks(definition EffectiveWorkspaceDefinition) []PlanLockProjection {
	result := make([]PlanLockProjection, 0, len(definition.plans))
	for _, plan := range definition.plans {
		for _, artifact := range definition.artifacts {
			if artifact.kind == ArtifactPlan && artifact.id == plan.id {
				result = append(result, PlanLockProjection{
					schemaVersion: 2, planID: plan.id, sourceHash: artifact.sourceHash,
					semanticHash: artifact.semanticHash, generation: definition.generation,
				})
				break
			}
		}
	}
	return result
}

func (projection PlanLockProjection) SchemaVersion() int   { return projection.schemaVersion }
func (projection PlanLockProjection) PlanID() ID           { return projection.planID }
func (projection PlanLockProjection) SourceHash() Digest   { return projection.sourceHash }
func (projection PlanLockProjection) SemanticHash() Digest { return projection.semanticHash }
func (projection PlanLockProjection) Generation() Digest   { return projection.generation }

func (projection PlanLockProjection) MarshalJSON() ([]byte, error) {
	type planLockJSON struct {
		SchemaVersion int    `json:"schema_version"`
		PlanID        string `json:"plan_id"`
		SourceHash    string `json:"source_hash"`
		SemanticHash  string `json:"semantic_hash"`
		Generation    string `json:"generation"`
	}
	return json.Marshal(planLockJSON{
		SchemaVersion: projection.schemaVersion,
		PlanID:        projection.planID.String(),
		SourceHash:    projection.sourceHash.String(),
		SemanticHash:  projection.semanticHash.String(),
		Generation:    projection.generation.String(),
	})
}
