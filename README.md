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
feature workspace abandon --bundle <bundle-root> --workspace <runtime-root> --input <file|-> [--json]
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

Execution configuration assigns every merge unit exactly one profile, an
execution policy that may only narrow its parent, and an explicit boundary:

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
    boundary:
      escalation: allowed
merge_units:
  - plan_id: sample-plan
    merge_unit_id: first-contract
    profile: standard
    boundary:
      checkpoint: pause_only
      escalation: allowed
      serial_segment: serial-first-contract
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
```

`checkpoint` is the owner's planned gate; `escalation` is the agent's
permission to stop on its own when something genuinely goes wrong. The four
workflow combinations are:

| checkpoint | escalation | behavior |
|---|---|---|
| `none` | `allowed` | Runs unit to unit without stopping; the agent may still stop if it hits something real. The block-of-units default. |
| `none` | `forbidden` | Cannot stop for any reason — finish or fail. For unattended and CI runs. |
| `pause_only` | `allowed` | Stops here, owner responds `continue`, and resumes on the same goal. |
| `complete_goal_and_wait` | `allowed` | Goal finishes here; after the owner responds, a next goal is reserved and acknowledged, and the attempt resumes on the new goal. |

An execution profile may optionally declare `boundary` with `escalation` only.
A merge unit may narrow `allowed` to `forbidden`, but never widen `forbidden`
to `allowed`. Profiles deliberately do not declare `checkpoint`, because
`pause_only` and `complete_goal_and_wait` do not order against each other.

Commit protocols, review-fix protocols, and review loops are optional strict
schemas within each merge-unit entry. Without a commit protocol, ordinary
local commits are allowed. Without a configured review loop,
`attempt adopt-head` records the exact clean accepted head and tree before
integration.

Agent-driven broad review is capped at three iterations. Start another
iteration only when the preceding review found a Critical or High issue.
After a review with no Critical or High findings, apply worthwhile Medium and
Low fixes once, perform targeted confirmation, and stop the broad-review loop.
`max_review_rounds` must be at least 2 for a fix budget to be usable, because a
fix is reconfirmed by a following review round.

## Locks and runtime state

The bundle root is also the plan repository root. Keep plan sources in ordinary
Git history. After an accepted source change, regenerate locks with:

```sh
feature workspace validate --bundle "$bundle_root" --write-locks --json
```

Commit both the plan sources and generated locks, and keep the plan repository
clean before initializing a runtime. `workspace init` verifies that the clean
plan `HEAD` contains the exact source and lock bytes, then derives the runtime
plan checkpoint artifact from that committed state.

The generated ownership inventory permits replacement only while each existing
generated file still matches its last generated hash. Modified projections,
hidden paths, symlink traversal, missing inventory, and unowned conflicts fail
closed. Do not edit `generated/` by hand.

Keep runtime state and attempt worktrees outside the source bundle and the
target repository. Initialization records the verified worktree root and the
derived plan checkpoint:

```json
{
  "schema_version": 2,
  "occurred_at": "2026-07-24T12:00:00Z",
  "worktree_root": "/absolute/path/to/attempt-worktrees"
}
```

Runtime state is append-only under `<runtime-root>/state/`. A runtime without
the local v5 format marker is rejected with a regeneration diagnostic; it is
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
7. Before integration, submit `attempt boundary` only when the merge unit
   configures a checkpoint other than `none`, or when the agent genuinely needs
   an allowed escalation. The request requires `kind`: use `checkpoint` for the
   configured gate and `escalation` for a genuine agent-raised stop. Record it
   while the attempt is active, before `integrate merge-unit`.
8. Resolve every returned local directive and resume the attempt when a boundary
   was recorded. If no boundary is needed, proceed directly. Only then submit
   `integrate merge-unit` for the exact accepted head and tree. Integration
   creates a deterministic two-parent local commit and compare-and-swap updates
   only the workspace-owned feature ref. Acknowledgements and owner responses
   bind the exact directive, goal, head, and idempotency inputs.
9. After every unit is integrated and every boundary is resolved, run
   `complete verify` to record local workspace completion.

If a runtime must be given up, submit `abandon` with a non-empty reason. It
ends local workflow mutations. When the runtime created the feature ref, the
ref keeps its exact head and is re-marked as released; delete or rename that
branch manually before using a fresh branch.

Every mutation returns a fresh journal-derived report. Treat that report as the
source of truth instead of reconstructing state from remembered commands.

The journal hash chain and stored digests detect accidental corruption; they
do not authenticate people. Reviewer and owner labels are descriptive. Local
completion proves the recorded Git topology and workflow state only.

## Public contract

### Operations and migration

Workspace v2 is a local-only execution model. Operators commit exact plan
sources and generated lock bytes in a clean plan repository, initialize a fresh
local v5 runtime, recover before each work cycle, and use journal-derived
reports as the source of truth. Earlier draft runtime state is intentionally not
migrated; a runtime without the local v5 marker must be regenerated from the
committed plan and current lock.

### Supported repository profile

The target must be a local non-bare Git repository with a complete object
database, SHA-1 or SHA-256 objects, ordinary ref and reflog storage, and no
active partial-clone, promisor, shallow, submodule, external object-storage,
repository-attribute, configured-filter, configured-signature-verifier, or
replacement-history profile. Linked worktrees are supported when their
administration and common directories remain exactly bound to the verified
target repository.

### Stable-base policy

The fully qualified `base_ref` must point to the exact `base_commit` during
validation and initialization. After initialization, movement of the base ref is
reported as drift only. The runtime never rebases active work, adopts a moved
base, or mutates the primary checkout to make the base match.

### Threat model

The implementation defends local state against malformed source bundles,
generated-file drift, journal tail corruption, stale compare-and-swap inputs,
symlink traversal, unsafe Git configuration, repository hooks, ambient helper
programs, and write-capable network use by configured checks. It does not
authenticate operators, reviewers, or owners; detect same-user replacement of
owned runtime files, locks, directories, Git admin data, or executables; or
provide cross-invocation hard-link insertion guarantees. Local completion is not
an external attestation.

### Deferred GitHub design

Any hosted-forge lifecycle is outside the v0.2 executable surface. It must be
introduced as a separate design with its own state, checks, and admission rules;
it cannot reinterpret local completion as hosted approval or release evidence.

### License and third-party notices

The project is distributed under the MIT license; see `LICENSE`. Runtime module
notices are listed in `THIRD_PARTY_NOTICES.md`. The repository does not vendor
third-party source or assets.

## Development

```sh
gofmt -w cmd/feature/*.go internal/install/*.go internal/plan/*.go internal/workspace/*.go internal/workspacecmd/*.go
go test -short -count=1 -p=1 -parallel=4 -timeout=10m ./...
go test -short -count=1 -p=1 -shuffle=on -parallel=4 -timeout=10m ./...
go test -short -count=1 -race -p=1 -parallel=4 -timeout=20m ./internal/workspace
go vet ./...
./install-skill.sh --plan --target all --json
stage="$(mktemp -d)"
./install-skill.sh --install --target all --json --install-root "$stage"
"$stage/.local/bin/feature" version
```

The exact-head CI baseline is reusable locally:

```sh
./scripts/ci-baseline.sh short-normal
FEATURE_SHUFFLE_SEED=1700000000 ./scripts/ci-baseline.sh short-shuffle
./scripts/ci-baseline.sh short-race
FEATURE_TEST_PARALLEL=2 ./scripts/ci-baseline.sh short-race # macOS

./scripts/ci-baseline.sh normal
FEATURE_SHUFFLE_SEED=1700000000 ./scripts/ci-baseline.sh shuffle
./scripts/ci-baseline.sh race
./scripts/ci-baseline.sh single-slot
FEATURE_SHUFFLE_SEED=1700000000 ./scripts/ci-baseline.sh shuffle-race
./scripts/ci-baseline.sh stress-concurrency

EXPECTED_HEAD_SHA="$(git rev-parse HEAD)" ./scripts/ci-baseline.sh all
./scripts/ci-baseline.sh vet
./scripts/ci-baseline.sh build
./scripts/ci-baseline.sh installer
./scripts/ci-baseline.sh diff
./scripts/ci-baseline.sh clean
```

All test profiles run one package binary at a time with `-p=1`. They permit four
parallel tests inside that binary by default; `FEATURE_TEST_PARALLEL` accepts a
validated value from one through four. Race profiles remain available for
operator-invoked local validation.

Testing has three tiers:

- Every pull-request workflow runs representative short coverage: normal tests on Linux
  and macOS, one Linux shuffle, and static checks on both platforms. The short
  suite retains the main local lifecycle, the two
  essential recovery states for each durable effect, core compare-and-swap
  races, representative rooted-filesystem and real-Git integration, and command
  and installer contracts.
- The full suite preserves every test and subtest. It runs automatically for
  changes under the workspace or workspace-command packages, the CI contracts
  and workflows, the baseline script, or the Go module files. It can also be
  dispatched for an exact commit SHA with the `full` profile.
- Stress validation is operator-invoked for an exact SHA with the `stress`
  profile. It runs three fixed shuffle seeds, single-slot compatibility, and
  repeated concurrency-sensitive scenarios. There is no scheduled stress
  workflow.

Every hosted job checks out the exact requested head, disables persisted
credentials and dependency caching, leaves token variables empty, and runs its
clean-tree check even after a preceding step fails. Shuffled runs print their
seed for reproduction.
