package plan

const exampleManifestYAML = `schema_version: 1
id: sample-migration-plan
title: Sample Migration Plan
output_name: sample-migration-plan
base_ref: main
remote: origin
merge_policy:
  require_passing_checks: true
epics:
  - id: epic-discovery
    number: 1
    name: Discovery
    summary: Inventory the current state and migration constraints.
    constraints:
      - Keep production behavior stable while planning.
    features:
      - id: feature-inventory
        number: 1
        name: Inventory
        summary: Capture the systems, data, and workflows that must migrate.
        stories:
          - id: story-current-state
            number: 1
            name: Current State Inventory
            summary: Document the systems, owners, data, dependencies, and risks involved in the migration.
            acceptance:
              - Current systems and owners are listed.
              - Migration risks and unknowns are captured.
            implementation:
              - Review existing docs, code paths, and operational runbooks.
          - id: story-target-plan
            number: 2
            name: Target Migration Plan
            summary: Produce the target migration plan, sequencing, rollback approach, and validation gates.
            acceptance:
              - Target phases and success gates are defined.
              - Rollback and validation steps are documented.
            implementation:
              - Convert findings into an implementation-ready migration sequence.
            dependencies:
              - story-current-state
merge_units:
  - id: story-current-state
    name: Current State Inventory
    story_ids:
      - story-current-state
  - id: story-target-plan
    name: Target Migration Plan
    story_ids:
      - story-target-plan
`

func ExampleManifestYAML() string {
	return exampleManifestYAML
}

func ManifestSchema() map[string]any {
	stringArray := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                "feature.plan.yaml",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema_version", "id", "title", "epics"},
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "integer", "const": 1},
			"id":             map[string]any{"type": "string", "minLength": 1},
			"title":          map[string]any{"type": "string", "minLength": 1},
			"output_name":    map[string]any{"type": "string"},
			"base_ref":       map[string]any{"type": "string"},
			"remote":         map[string]any{"type": "string"},
			"merge_policy":   map[string]any{"$ref": "#/$defs/merge_policy"},
			"epics": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items":    map[string]any{"$ref": "#/$defs/epic"},
			},
			"merge_units": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "#/$defs/merge_unit"},
			},
		},
		"$defs": map[string]any{
			"merge_policy": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"auto_merge_allowed":     map[string]any{"type": "boolean"},
					"delete_branch_allowed":  map[string]any{"type": "boolean"},
					"require_passing_checks": map[string]any{"type": "boolean"},
				},
			},
			"epic": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"id", "number", "name", "features"},
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "minLength": 1},
					"number":      map[string]any{"type": "integer", "minimum": 1},
					"name":        map[string]any{"type": "string", "minLength": 1},
					"summary":     map[string]any{"type": "string"},
					"constraints": stringArray,
					"features": map[string]any{
						"type":     "array",
						"minItems": 1,
						"items":    map[string]any{"$ref": "#/$defs/feature"},
					},
				},
			},
			"feature": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"id", "number", "name", "stories"},
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "minLength": 1},
					"number":      map[string]any{"type": "integer", "minimum": 1},
					"name":        map[string]any{"type": "string", "minLength": 1},
					"summary":     map[string]any{"type": "string"},
					"constraints": stringArray,
					"stories": map[string]any{
						"type":     "array",
						"minItems": 1,
						"items":    map[string]any{"$ref": "#/$defs/story"},
					},
				},
			},
			"story": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"id", "number", "name"},
				"properties": map[string]any{
					"id":             map[string]any{"type": "string", "minLength": 1},
					"number":         map[string]any{"type": "integer", "minimum": 1},
					"name":           map[string]any{"type": "string", "minLength": 1},
					"summary":        map[string]any{"type": "string"},
					"acceptance":     stringArray,
					"implementation": stringArray,
					"dependencies":   stringArray,
				},
			},
			"merge_unit": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"id", "story_ids"},
				"properties": map[string]any{
					"id":                     map[string]any{"type": "string", "minLength": 1},
					"name":                   map[string]any{"type": "string"},
					"story_ids":              stringArray,
					"allow_feature_level_pr": map[string]any{"type": "boolean"},
				},
			},
		},
	}
}
