---
name: feature
description: Break a scoped implementation task into agile-style epics, features, stories, and merge units, materialize a structured feature plan folder, review it with a subagent, apply useful findings, and validate the plan. Use when the user invokes /feature or asks to create a feature implementation plan.
argument-hint: "[--out <folder>] <scope/task>"
---

# Feature Planning

Create a feature implementation plan from the user's scope.

1. Parse the requested scope and optional output folder. If omitted, do not pass `--out-root`; let `feature plan materialize` use its default output root.
2. Draft `feature.plan.yaml` using the manifest contract below. Use stable slug-style IDs, ordered numbers, epics, features, stories, dependencies, and merge units.
3. Default to one merge unit per story. Use a feature-level merge unit only when all included stories are in the same feature and have no unresolved dependency on an outside story.
4. For migration or phased-planning prompts, map phases to epics, workstreams/capability areas to features, and concrete implementation steps to stories.
5. Run `feature plan materialize --manifest <manifest> --out-root <folder> --json` only when the user gave an output folder; otherwise run `feature plan materialize --manifest <manifest> --json`.
6. Spawn a Claude subagent to review hierarchy, story granularity, dependencies, merge units, missing caveats, and implementation order.
7. Apply useful review findings, then run `feature validate <plan-dir> --write-lock --json`.

Return the plan directory, validation status, and implementation order.

## Manifest Contract

Required top-level fields:

- `schema_version: 1`
- `id`
- `title`
- `epics`

Optional top-level fields:

- `output_name`: output folder name under the selected root.
- `base_ref`: implementation base branch, usually `main`.
- `remote`: implementation remote, usually `origin`.
- `merge_policy`: `auto_merge_allowed`, `delete_branch_allowed`, `require_passing_checks`.
- `merge_units`: explicit implementation/PR units. If omitted, validation creates one merge unit per story.

Each epic requires `id`, `number`, `name`, and at least one feature. Each feature requires `id`, `number`, `name`, and at least one story. Each story requires `id`, `number`, and `name`; use `summary`, `acceptance`, `implementation`, and `dependencies` when useful. Story dependencies must reference story IDs.

Each merge unit requires `id` and `story_ids`. Use `allow_feature_level_pr: true` only when grouping multiple stories from the same feature.

Use this valid manifest shape:

```yaml
schema_version: 1
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
            summary: Document systems, owners, data, dependencies, and risks.
            acceptance:
              - Current systems and owners are listed.
              - Migration risks and unknowns are captured.
            implementation:
              - Review existing docs, code paths, and operational runbooks.
          - id: story-target-plan
            number: 2
            name: Target Migration Plan
            summary: Produce sequencing, rollback approach, and validation gates.
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
```

For quick reference, `feature plan example` prints a valid manifest and `feature plan schema --json` prints the machine-readable schema.
