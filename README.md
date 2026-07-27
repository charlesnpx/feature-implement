# feature-implement

`feature-implement` is a self-contained Go CLI with delegated Codex and Claude
skills for planning implementation work and executing it in a local Git
repository. Workspace v2 uses immutable source definitions, a typed append-only
journal, isolated attempt worktrees, exact-head review, and deterministic local
integration.

It installs:

- `feature` under `~/.local/bin`
- `$feature` and `$feature:implement` for Codex
- `/feature` and `/feature:implement` for Claude Code

## Install contract

This repository follows the `mise-en-place` delegated installer contract.
`install-skill.sh` is a thin wrapper around:

```sh
feature install-skills [--plan|--install|--uninstall] [--target claude|codex|tools|all] [--json] [--install-root <dir>]
```

The installer emits delegated JSON with `schema: 1`, `kind: delegated`, target
file records, setup metadata, and SHA256 hashes for installed files. Git is the
only runtime executable required by the installed workflow. The `tools` target
owns the self-contained Go binary.

## Command surfaces

The standalone version-one plan materializer remains available for planning
consumers:

```sh
feature plan example
feature plan schema --json
feature plan materialize --manifest feature.plan.yaml --out-root <dir> --json
feature validate <plan-dir> [--write-lock] --json
```

Workspace execution uses these version-two surfaces:

```text
feature plan checkpoint --root <bundle-root> --kind initial|revision|lock --input <file|-> [--json]

feature workspace schema bundle|requests|reports [--json]
feature workspace example
feature workspace validate --bundle <bundle-root> [--write-locks] [--json]
feature workspace init|recover --bundle <bundle-root> --workspace <runtime-root> --input <file|-> [--json]
feature workspace status|scheduler|gates|report --bundle <bundle-root> --workspace <runtime-root> [--json]

feature workspace attempt reserve|materialize|adopt-head|boundary|next-goal|acknowledge|owner-response|resume ...
feature workspace commit next ...
feature workspace review start|reserve|record|reserve-fix|apply-fix|record-fix|ready ...
feature workspace integrate merge-unit ...
feature workspace complete verify ...
```

Every mutation accepts exactly one strict schema-version-two JSON request
through `--input <file|->`. Unknown or duplicate fields, trailing JSON,
unsupported enum values, omitted required fields, and oversized inputs are
rejected before a transition is recorded. Use
`feature workspace schema requests --json` as the request reference.

## Workspace bundle

A bundle is an immutable set of source files rooted by
`feature.workspace.bundle.json`:

```text
sample-workspace/
├── feature.workspace.bundle.json
├── feature.workspace.yaml
├── plans/
│   └── sample-plan.yaml
├── config/
│   └── execution.yaml
└── generated/                  # tool-owned immutable lock projections
```

The descriptor contains only local source discovery:

```json
{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/sample-plan.yaml"],
  "execution_config": "config/execution.yaml"
}
```

Every descriptor path is relative, non-hidden, outside `generated/`, uniquely
owned by one source role, and rooted beneath the bundle. Source paths cannot
traverse symlinks or collide across roles.

The workspace manifest binds one local target repository, a stable fully
qualified base ref and exact base commit, an AI-selected feature branch, plan
membership, and cross-plan dependencies:

```yaml
schema_version: 2
id: sample-workspace
mode: local
repository:
  root: /absolute/path/to/repository
base_ref: refs/heads/main
base_commit: sha1:1111111111111111111111111111111111111111
feature_branch: feature/sample-workspace
execution_config: config/execution.yaml
plans:
  - id: sample-plan
    source: plans/sample-plan.yaml
dependencies: []
```

The pinned base commit must equal `base_ref` during validation and
initialization. Later movement of that ref is reported as drift; an active
workspace is never silently rebased. The primary checkout may be dirty and is
never cleaned, stashed, switched, or used as an attempt worktree.

Plan sources own stories, story dependencies, and merge-unit composition:

```yaml
schema_version: 2
id: sample-plan
title: Sample Plan
stories:
  - id: story-first-contract
    summary: Establish the first implementation contract.
    acceptance:
      - The contract is explicit and enforced.
    implementation:
      - Implement the bounded contract.
    testing:
      - Exercise success and rejection paths.
    dependencies: []
merge_units:
  - id: first-contract
    name: First Contract
    story_ids:
      - story-first-contract
```

Execution configuration assigns every merge unit exactly one profile, a policy
that may only narrow its parent, and an explicit boundary:

```yaml
schema_version: 2
policy:
  require_passing_checks: true
  allow_write_network: false
  max_attempts: 3
  max_review_rounds: 3
  max_review_fixes: 2
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
merge_units:
  - plan_id: sample-plan
    merge_unit_id: first-contract
    profile: standard
    boundary:
      mode: pause_only
      serial_segment: serial-first-contract
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
```

Commit protocols, review-fix protocols, and review loops are optional strict
schemas within each merge-unit entry. Without a commit protocol, ordinary
local commits are allowed. Without a configured review loop,
`attempt adopt-head` records the exact clean accepted head and tree before
integration.

Agent-driven broad review is capped at three iterations. Start another
iteration only when the preceding review found a Critical or High issue.
After a review with no Critical or High findings, apply worthwhile Medium and
Low fixes once, perform targeted confirmation, and stop the broad-review loop.

## Locks and runtime state

The bundle root is also the plan repository root. Record an `initial`
checkpoint before generated locks, use `revision` for an accepted source
change, and record a `lock` checkpoint after:

```sh
feature workspace validate --bundle "$bundle_root" --write-locks --json
```

The generated ownership inventory permits replacement only while each existing
generated file still matches its last generated hash. Modified projections,
hidden paths, symlink traversal, missing inventory, and unowned conflicts fail
closed. Do not edit `generated/` by hand.

Keep runtime state and attempt worktrees outside the source bundle and the
target repository. Initialization records the verified worktree root and the
exact plan lock checkpoint:

```json
{
  "schema_version": 2,
  "occurred_at": "2026-07-24T12:00:00Z",
  "worktree_root": "/absolute/path/to/attempt-worktrees"
}
```

Runtime state is append-only under `<runtime-root>/state/`. A runtime without
the local v3 format marker is rejected with a regeneration diagnostic; it is
not interpreted or migrated.

## Local execution

1. Run `recover`, then read `status`, `scheduler`, `gates`, and `report`.
2. Select a `ready` merge unit. Submit `attempt reserve` with its plan ID,
   merge-unit ID, next attempt number, and goal. The base, branch, and worktree
   root are derived from locked runtime state.
3. Submit `attempt materialize`. Work only in the returned worktree and branch.
4. Use `commit next` for a configured commit step. Otherwise make ordinary
   local commits and keep the worktree clean.
5. For configured review, use `review start`, `reserve`, `record`, bounded fix
   actions, and `ready`. Reviewer labels are descriptive local metadata, and
   every result binds the exact request, head, tree, and evidence.
6. Without configured review, submit `attempt adopt-head` for the exact clean
   descendant selected for integration.
7. Submit `integrate merge-unit`. Integration creates a deterministic
   two-parent local commit and compare-and-swap updates only the workspace-owned
   feature ref.
8. Record and resolve the attempt boundary and any returned local directives.
   Acknowledgements and owner responses bind the exact directive, goal, head,
   and idempotency inputs.
9. After every unit is integrated and every boundary is resolved, run
   `complete verify` to record local workspace completion.

Every mutation returns a fresh journal-derived report. Treat that report as the
source of truth instead of reconstructing state from remembered commands.

The journal hash chain and stored digests detect accidental corruption; they
do not authenticate people. Reviewer and owner labels are descriptive. Local
completion proves the recorded Git topology and workflow state only.

## Development

```sh
gofmt -w cmd/feature/*.go internal/install/*.go internal/plan/*.go internal/workspace/*.go internal/workspacecmd/*.go
go test -timeout=20m ./...
go test -shuffle=on -timeout=20m ./...
go test -race -timeout=30m ./internal/workspace ./internal/workspacecmd
go vet ./...
./install-skill.sh --plan --target all --json
stage="$(mktemp -d)"
./install-skill.sh --install --target all --json --install-root "$stage"
"$stage/.local/bin/feature" version
```

The exact-head CI baseline is reusable locally:

```sh
EXPECTED_HEAD_SHA="$(git rev-parse HEAD)" ./scripts/ci-baseline.sh all
./scripts/ci-baseline.sh normal
FEATURE_SHUFFLE_SEED=1700000000 ./scripts/ci-baseline.sh shuffle
./scripts/ci-baseline.sh race
./scripts/ci-baseline.sh vet
./scripts/ci-baseline.sh build
./scripts/ci-baseline.sh installer
./scripts/ci-baseline.sh diff
./scripts/ci-baseline.sh clean
```

The hosted pull-request workflow checks out the exact requested head, disables
persisted checkout state and dependency caching, and runs the same gates on
pinned Ubuntu and macOS runners. A shuffled run prints its seed for
reproduction.
