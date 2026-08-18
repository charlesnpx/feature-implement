package workspacecmd

import "github.com/charlesnpx/feature-implement/internal/workspace"

// ReportSchemas describes the complete local-only JSON report surface. Journal
// digests are corruption-detection values for local durable state; they do not
// authenticate an owner, reviewer, or other identity.
func ReportSchemas() map[string]any {
	text := func() map[string]any {
		return map[string]any{"type": "string"}
	}
	nonEmptyText := func() map[string]any {
		return map[string]any{"type": "string", "minLength": 1}
	}
	boolean := func() map[string]any {
		return map[string]any{"type": "boolean"}
	}
	integer := func(minimum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum}
	}
	enum := func(values ...string) map[string]any {
		return map[string]any{"enum": values}
	}
	array := func(items map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": items}
	}
	object := func(
		required []string,
		properties map[string]any,
	) map[string]any {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             required,
			"properties":           properties,
		}
	}

	directive := object(
		[]string{
			"kind", "boundary_kind", "workspace_id", "generation", "attempt_id",
			"boundary_id", "goal_id", "goal_scope", "head",
			"directive_digest",
		},
		map[string]any{
			"kind":             nonEmptyText(),
			"boundary_kind":    enum("checkpoint", "escalation"),
			"workspace_id":     nonEmptyText(),
			"generation":       nonEmptyText(),
			"attempt_id":       nonEmptyText(),
			"boundary_id":      nonEmptyText(),
			"goal_id":          nonEmptyText(),
			"goal_scope":       enum("merge_unit", "workspace"),
			"head":             nonEmptyText(),
			"directive_digest": nonEmptyText(),
			"idempotency_key":  text(),
			"choices":          array(nonEmptyText()),
		},
	)
	schedulerUnit := object(
		[]string{
			"plan_id", "merge_unit_id", "status", "generation",
			"dependencies", "blockers", "boundary_pending",
			"pending_directives",
		},
		map[string]any{
			"plan_id":       nonEmptyText(),
			"merge_unit_id": nonEmptyText(),
			"status": enum(
				"blocked", "ready", "reserved", "materializing",
				"active", "paused", "review_exhausted", "completed",
			),
			"generation":         nonEmptyText(),
			"dependencies":       array(nonEmptyText()),
			"blockers":           array(nonEmptyText()),
			"attempt_id":         nonEmptyText(),
			"attempt_number":     integer(1),
			"branch":             nonEmptyText(),
			"worktree":           nonEmptyText(),
			"head":               nonEmptyText(),
			"boundary_pending":   boolean(),
			"boundary_reason":    nonEmptyText(),
			"pending_directives": array(directive),
		},
	)
	scheduler := object(
		[]string{
			"schema_version", "workspace_id", "generation",
			"journal_head", "units",
		},
		map[string]any{
			"schema_version": map[string]any{"const": workspace.JournalSchemaVersion},
			"workspace_id":   nonEmptyText(),
			"generation":     nonEmptyText(),
			"journal_head":   nonEmptyText(),
			"units":          array(schedulerUnit),
		},
	)

	gateCheck := object(
		[]string{"name", "status", "generation", "reason"},
		map[string]any{
			"name":       enum("dependencies", "commit", "review", "integration", "completion"),
			"status":     enum("pending", "passed", "failed"),
			"generation": nonEmptyText(),
			"reason":     nonEmptyText(),
		},
	)
	gateUnit := object(
		[]string{"plan_id", "merge_unit_id", "checks", "merge_ready"},
		map[string]any{
			"plan_id":       nonEmptyText(),
			"merge_unit_id": nonEmptyText(),
			"attempt_id":    nonEmptyText(),
			"checks":        array(gateCheck),
			"merge_ready":   boolean(),
		},
	)
	gates := object(
		[]string{
			"schema_version", "workspace_id", "generation",
			"journal_head", "units", "completion",
			"completion_blockers",
		},
		map[string]any{
			"schema_version": map[string]any{"const": workspace.JournalSchemaVersion},
			"workspace_id":   nonEmptyText(),
			"generation":     nonEmptyText(),
			"journal_head":   nonEmptyText(),
			"units":          array(gateUnit),
			"completion":     gateCheck,
			"completion_blockers": array(
				nonEmptyText(),
			),
		},
	)

	workflow := object(
		[]string{
			"workspace_id", "generation", "journal_head",
			"plan_checkpoint", "worktree_root", "projection_digest",
			"review_projection_digest",
		},
		map[string]any{
			"workspace_id":             nonEmptyText(),
			"generation":               nonEmptyText(),
			"journal_head":             nonEmptyText(),
			"plan_checkpoint":          nonEmptyText(),
			"worktree_root":            nonEmptyText(),
			"projection_digest":        nonEmptyText(),
			"review_projection_digest": nonEmptyText(),
		},
	)
	target := object(
		[]string{
			"root", "git_directory", "common_directory",
			"repository_format", "object_format", "linked_worktree",
			"base_ref", "base_commit", "feature_branch", "feature_ref",
			"feature_head", "binding_digest", "ready",
		},
		map[string]any{
			"root":              nonEmptyText(),
			"git_directory":     text(),
			"common_directory":  text(),
			"repository_format": integer(0),
			"object_format":     enum("", "sha1", "sha256"),
			"linked_worktree":   boolean(),
			"base_ref":          nonEmptyText(),
			"base_commit":       nonEmptyText(),
			"feature_branch":    nonEmptyText(),
			"feature_ref":       nonEmptyText(),
			"feature_head":      text(),
			"binding_digest":    text(),
			"ready":             boolean(),
		},
	)
	attempt := object(
		[]string{
			"attempt_id", "plan_id", "merge_unit_id", "generation",
			"attempt_number", "base", "branch", "worktree", "phase",
			"goal_id", "goal_scope", "boundary_pending",
			"pending_directives",
		},
		map[string]any{
			"attempt_id":     nonEmptyText(),
			"plan_id":        nonEmptyText(),
			"merge_unit_id":  nonEmptyText(),
			"generation":     nonEmptyText(),
			"attempt_number": integer(1),
			"base":           nonEmptyText(),
			"branch":         nonEmptyText(),
			"worktree":       nonEmptyText(),
			"phase": enum(
				"reserved", "materializing", "active", "paused",
				"review_exhausted", "completed", "failed", "abandoned",
			),
			"head":               nonEmptyText(),
			"goal_id":            nonEmptyText(),
			"goal_scope":         enum("merge_unit", "workspace"),
			"boundary_pending":   boolean(),
			"boundary_reason":    nonEmptyText(),
			"pending_directives": array(directive),
		},
	)
	review := object(
		[]string{
			"attempt_id", "plan_id", "merge_unit_id", "generation",
			"head", "tree", "status", "rounds_used", "fixes_used",
			"infrastructure_retries", "merge_ready",
		},
		map[string]any{
			"attempt_id":             nonEmptyText(),
			"plan_id":                nonEmptyText(),
			"merge_unit_id":          nonEmptyText(),
			"generation":             nonEmptyText(),
			"head":                   nonEmptyText(),
			"tree":                   nonEmptyText(),
			"status":                 enum("active", "ready", "exhausted", "fix_pending"),
			"rounds_used":            integer(0),
			"fixes_used":             integer(0),
			"infrastructure_retries": integer(0),
			"merge_ready":            boolean(),
		},
	)
	integrationUnit := object(
		[]string{"plan_id", "merge_unit_id", "status"},
		map[string]any{
			"plan_id":       nonEmptyText(),
			"merge_unit_id": nonEmptyText(),
			"attempt_id":    nonEmptyText(),
			"head":          nonEmptyText(),
			"status":        enum("pending", "integrated"),
		},
	)
	integration := object(
		[]string{"units"},
		map[string]any{"units": array(integrationUnit)},
	)
	drift := object(
		[]string{"detected", "reasons"},
		map[string]any{
			"detected": boolean(),
			"reasons":  array(nonEmptyText()),
		},
	)
	completion := object(
		[]string{"complete", "blockers"},
		map[string]any{
			"complete":      boolean(),
			"blockers":      array(nonEmptyText()),
			"report_digest": nonEmptyText(),
		},
	)
	report := object(
		[]string{
			"schema_version", "workflow", "target", "attempts",
			"reviews", "scheduler", "gates", "integration", "drift",
			"completion", "report_digest",
		},
		map[string]any{
			"schema_version": map[string]any{"const": workspace.JournalSchemaVersion},
			"workflow":       workflow,
			"target":         target,
			"attempts":       array(attempt),
			"reviews":        array(review),
			"scheduler":      scheduler,
			"gates":          gates,
			"integration":    integration,
			"drift":          drift,
			"completion":     completion,
			"report_digest":  nonEmptyText(),
		},
	)
	return map[string]any{
		"$schema":        "https://json-schema.org/draft/2020-12/schema",
		"$comment":       "Owner and reviewer labels are descriptive metadata. Journal hashes detect local corruption; they are not authentication.",
		"schema_version": workspace.JournalSchemaVersion,
		"reports": map[string]any{
			"status":    report,
			"scheduler": scheduler,
			"gates":     gates,
			"report":    report,
		},
	}
}
