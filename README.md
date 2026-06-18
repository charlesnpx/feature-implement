# feature-implement

`feature-implement` provides a delegated `mise-en-place` skill and Go CLI for turning implementation scope into validated epic, feature, story, and merge-unit plans, then executing those plans through guarded branch and PR workflows.

It installs:

- `feature`, a self-contained Go CLI under `~/.local/bin`
- `$feature` and `$feature:implement` for Codex
- `/feature` and `/feature:implement` for Claude Code

## Install Contract

This repo follows the `mise-en-place` delegated installer contract. `install-skill.sh` is a thin wrapper around:

```sh
feature install-skills [--plan|--install|--uninstall] [--target claude|codex|tools|all] [--json] [--install-root <dir>]
```

The installer emits delegated JSON with `schema: 1`, `kind: delegated`, target file records, setup metadata, and SHA256 hashes for installed files. Go/self-contained tool installation is owned by the delegated installer through the `tools` target.

## Commands

```sh
feature plan example
feature plan schema --json
feature plan materialize --manifest feature.plan.yaml --json
feature validate <plan-dir> --write-lock --json
feature status <plan-dir> --json
feature implement next <plan-dir> --json
feature implement start <plan-dir> --merge-unit <id> --base-sha <sha> --write-state --json
feature implement commit <plan-dir> --merge-unit <id> --commit-sha <sha> --write-state --json
feature implement push <plan-dir> --merge-unit <id> --allow-push --write-state --json
feature implement open-pr <plan-dir> --merge-unit <id> --allow-open-pr --pr <number> --pr-url <url> --write-state --json
feature implement review <plan-dir> --merge-unit <id> --review-status passed --write-state --json
feature implement merge <plan-dir> --merge-unit <id> --allow-merge --merge-commit <sha> --write-state --json
feature implement cleanup <plan-dir> --merge-unit <id> --write-state --json
```

`feature validate` writes `feature.plan.lock.json`; implementation commands consume that validated snapshot rather than live-edited Markdown.
Lifecycle write steps use immutable lock transitions: each `--write-state` command reads the current lock, returns a new ordered merge-unit state snapshot, and writes that snapshot back to `feature.plan.lock.json`. Existing v1 lock files with map-shaped state are migrated on the next state write.

`feature:implement` creates one temporary worktree for the active merge unit under `<plan-dir>/worktrees/<merge-unit-id>`. After the PR is merged and the local checkout of the plan `base_ref` is updated, remove that worktree and record `feature implement cleanup ... --write-state`. Remote branch deletion is separate and still requires both merge policy allowance and explicit approval.

`feature implement ... --json` results for a selected merge unit include `story_progress_label`, such as `(Story 4/16)` or `(Stories 4-5/16)`, derived from the ordered stories in `feature.plan.lock.json`.

When `$feature` or `/feature` needs to draft the temporary `feature.plan.yaml`, it should stage that scratch manifest under the user-provided output folder, `~/tmp` when it exists, or the system temp directory. It should not write scratch manifests into the current repository root unless that repo root was explicitly supplied as the output folder.

## Direct Implement Versus Workspace Attempts

Use `feature implement` directly for the normal single-plan flow. It owns the active merge-unit lifecycle in `feature.plan.lock.json`, creates one temporary worktree for that merge unit, opens a PR against the plan `base_ref`, and records commit, PR, review, merge, and cleanup metadata in the plan lock.

Use `feature workspace` when several validated plans need to run on a shared long-running integration branch. A workspace has its own `feature.workspace.yaml`, `feature.workspace.lock.json`, scheduler view, and event journal. Workspace claims, leases, attempts, worktree commands, and lifecycle transitions are recorded in workspace state, not in the referenced plans' `feature.plan.lock.json` files.

Workspace-managed workers should claim a merge unit with `feature workspace next --claim`, start an attempt with `feature workspace attempt start`, create the planned branch/worktree from the workspace `base_ref`, and record local lifecycle movement with `feature workspace transition`. External writes such as push, PR creation, review, merge, and cleanup still belong to the guarded implementation workflow and should target the workspace integration branch.

## Manifest Contract

`$feature` and `/feature` create a `feature.plan.yaml` manifest, then `feature plan materialize` turns it into epic, feature, and story Markdown folders.

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

Materialized story Markdown includes Acceptance Criteria, Implementation Notes, and Testing Criteria sections.

Each merge unit requires `id` and `story_ids`. Use `allow_feature_level_pr: true` only when grouping multiple stories from the same feature.

For migration or phased-planning prompts, map phases to epics, workstreams/capability areas to features, and concrete implementation steps to stories.

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

Smoke test:

```sh
scratch_root="${HOME}/tmp"
if [ -d "$scratch_root" ]; then
  scratch_dir="$(mktemp -d "$scratch_root/feature-manifest-XXXXXX")"
else
  scratch_dir="$(mktemp -d)"
fi

manifest="$scratch_dir/feature.plan.yaml"
feature plan example > "$manifest"
plan_dir="$(feature plan materialize --manifest "$manifest")"
feature validate "$plan_dir" --write-lock
feature status "$plan_dir"
```

## Development

Run the full local check:

```sh
go test ./...
./install-skill.sh --plan --target all --json
stage="$(mktemp -d)"
./install-skill.sh --install --target all --json --install-root "$stage"
"$stage/.local/bin/feature" version
```

Optional local git smoke, which creates `.git` metadata only inside temp test directories:

```sh
FEATURE_WORKSPACE_LOCAL_GIT_SMOKE=1 go test ./internal/workspace -run TestLocalGitAttemptWorktreeSmoke -count=1
```
