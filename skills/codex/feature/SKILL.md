---
name: feature
description: Break a scoped implementation task into agile-style epics, features, stories, and merge units, materialize a structured feature plan folder, review it with a subagent, apply useful findings, and validate the plan. Use when the user invokes $feature or asks to create a feature implementation plan.
argument-hint: "[--out <folder>] <scope/task>"
---

# Feature Planning

Create a feature implementation plan from the user's scope.

## Workflow

1. Parse the requested scope and optional output folder. If omitted, do not pass `--out-root`; let `feature plan materialize` use its default output root.
2. Choose a scratch manifest staging root. Use the provided output folder when one was specified; otherwise use `~/tmp` if it exists; otherwise use the system temp directory. Never write the draft manifest in the current repo root unless the user explicitly supplied the repo root as the output folder.
3. Create a non-hidden scratch staging folder under that root, for example `<staging-root>/feature-manifest-<slug>/feature.plan.yaml`, and draft the manifest there using the contract below. Use stable slug-style IDs, ordered numbers, epics, features, stories, dependencies, and merge units.
4. Default to one merge unit per story. Use a feature-level merge unit only when all included stories are in the same feature and have no unresolved dependency on an outside story.
5. For migration or phased-planning prompts, map phases to epics, workstreams/capability areas to features, and concrete implementation steps to stories.
6. Run one of:

```sh
feature plan materialize --manifest <manifest> --out-root <folder> --json
feature plan materialize --manifest <manifest> --json
```

7. Spawn a fresh Codex subagent to review the generated folder. Ask it to check hierarchy, story granularity, dependencies, merge units, missing caveats, and implementation order. Do not use `pr:review:local:no-file` for this generated-folder review.
8. Run a plan review loop with a maximum of 10 fresh-review iterations. For each review with useful findings, apply selected edits to the scratch manifest, re-run `feature plan materialize` with the same manifest and output arguments so Markdown stays in sync, then spawn a fresh reviewer for the updated generated folder. Stop only when a fresh reviewer returns no findings worth addressing. If iteration 10 still has worthwhile findings, stop and report the remaining findings instead of validating.
9. After a clean review, run:

```sh
feature validate <plan-dir> --write-lock --json
```

Return the plan directory, validation status, and the key implementation order.

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

Each epic requires `id`, `number`, `name`, and at least one feature. Each feature requires `id`, `number`, `name`, and at least one story.

Every story must be implementation-ready and include:

- `id`, `number`, `name`, and `summary`
- `acceptance`: concrete acceptance criteria that define done behavior
- `implementation`: specific implementation notes detailed enough for a coding agent to act on
- `testing`: explicit test criteria, including unit/integration/manual checks as appropriate
- `dependencies` when the story depends on earlier story IDs

Story dependencies must reference story IDs.

Materialized story Markdown must include Acceptance Criteria, Implementation Notes, and Testing Criteria sections.

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
            testing:
              - Validate that the inventory covers systems, owners, dependencies, and risks.
          - id: story-target-plan
            number: 2
            name: Target Migration Plan
            summary: Produce sequencing, rollback approach, and validation gates.
            acceptance:
              - Target phases and success gates are defined.
              - Rollback and validation steps are documented.
            implementation:
              - Convert findings into an implementation-ready migration sequence.
            testing:
              - Review the plan against phase gates, rollback expectations, and validation steps.
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
