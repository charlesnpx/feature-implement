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
feature workspace status --bundle <bundle-root> --workspace <runtime-root> [--json]

feature workspace attempt start|adopt-head|pause|resume|abandon ...
feature workspace review dispatch|record|record-document|ready ...
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
├── policies/
│   └── review.md
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
execution policy that may only narrow its parent, an explicit boundary, and an
optional review-gate adapter contract:

```yaml
schema_version: 2
policy:
  require_passing_checks: true
  allow_write_network: false
  max_attempts: 3
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      allow_write_network: false
      max_attempts: 3
    boundary:
      escalation: allowed
review_gate:
  adapter: natural-language
  recipe: default
  policy_file: policies/review.md
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
```

`checkpoint` is the owner's planned gate; `escalation` is the agent's
permission to stop on its own when something genuinely goes wrong. The four
workflow combinations are:

| checkpoint | escalation | behavior |
|---|---|---|
| `none` | `allowed` | Runs unit to unit without stopping; the agent may still stop if it hits something real. The block-of-units default. |
| `none` | `forbidden` | Cannot stop for any reason — finish or fail. For unattended and CI runs. |
| `pause_only` | `allowed` | Records a planned pause and resumes on the same goal. |
| `pause_only` | `forbidden` | Records the planned pause; the agent may not raise its own stop. Resumes on the same goal. |

An execution profile may optionally declare `boundary` with `escalation` only.
A merge unit may narrow `allowed` to `forbidden`, but never widen `forbidden`
to `allowed`. Profiles deliberately do not declare `checkpoint`; planned
checkpoints remain on individual merge units.

Commit protocols and review gates are optional strict schemas within each
merge-unit entry. A configured commit protocol validates the final clean
base-to-head history—ordered checkpoints, exact messages, path constraints,
and isolated checks that exit zero—before gate dispatch or integration.

A `review_gate` names `adapter`, `recipe`, and `policy_file` together. A merge
unit either inherits the complete root gate or names another complete gate; a
partial override is rejected. The policy file is ordinary bundle source text:
its exact bytes are digested, retained in the generation, and handed to the
adapter without interpretation by `feature-implement`. The natural-language
adapter is a normal implementation, not a fallback. Its policy can prescribe
two parallel passes (for example, general and economy), one initial pass, and
at most one additional pass when its own criteria call for one. That policy is
the adapter's concern, not local scheduling logic.

Each dispatch records intent before a frozen copy is materialized. Its terminal
record is exactly one of `satisfied`, `not_satisfied`, or `failed_to_run`, and
always carries an evidence digest. The latter two are terminal facts rather
than special scheduler paths. Without a configured review gate,
`attempt adopt-head` records the exact clean accepted head and tree before
integration.

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
the local v7 format marker is rejected with a regeneration diagnostic; it is
not interpreted or migrated.

## Local execution

1. Run `recover`, then read `status`.
2. Select a `ready` merge unit. Submit `attempt start` with its plan ID,
   merge-unit ID, next attempt number, and goal. The base and detached scratch
   worktree are derived from locked runtime state.
3. Work only in the returned detached worktree. Its working history is scratch
   until an exact clean head is selected for integration.
4. Make ordinary local commits and keep the worktree clean. When a
   `commit_protocol` is configured, the final base-to-head history is checked
   against its ordered checkpoints and configured checks before review or
   integration.
5. For a configured review gate, submit `review dispatch` after the attempt is
   clean. It records the request first and returns a separately materialized
   frozen copy plus the opaque policy text; give the adapter only that copy.
   When the adapter has a durable evidence record, submit `review record` with
   its digest, or use `review record-document` for a Witness
   `review-report-v1` document. The latter strictly validates and retains raw
   report bytes. A changed head or tree requires a fresh dispatch.
6. `review ready` is a read-only check for a satisfied gate against the exact
   current artifact. It does not conduct review. `not_satisfied` and
   `failed_to_run` remain distinguishable terminal facts; use ordinary owner
   decisions and attempt lifecycle actions rather than a review-specific loop.
7. Without a configured review gate, submit `attempt adopt-head` for the exact
   clean descendant selected for integration.
8. Before integration, submit `attempt pause` only when the merge unit
   configures a checkpoint other than `none`, or when the agent genuinely needs
   an allowed escalation. The request requires `kind`: use `checkpoint` for the
   configured planned stop and `escalation` for a genuine agent-raised stop. Record it
   while the attempt is active, before `integrate merge-unit`.
9. Resume the attempt directly when a pause was recorded. Use `attempt abandon`
   only to terminally exit a non-integrated attempt; it leaves its detached
   scratch directory intact for inspection. If no pause is needed, proceed
   directly. Only then submit `integrate merge-unit` for the exact accepted head and tree. Integration
   creates a deterministic two-parent local commit and compare-and-swap updates
   only the workspace-owned feature ref.
10. After every unit is integrated and every pause is resolved, run
   `complete verify` to record local workspace completion.

Every mutation returns a fresh journal-derived workspace view. Treat that view as the
source of truth instead of reconstructing state from remembered commands.

The journal hash chain and stored digests detect accidental corruption; they
do not authenticate people. Reviewer and owner labels are descriptive. Local
completion proves the recorded Git topology and workflow state only.

## Public contract

### Operations and migration

Workspace v2 is a local-only execution model. Operators commit exact plan
sources and generated lock bytes in a clean plan repository, initialize a fresh
local v7 runtime, recover before each work cycle, and use journal-derived
reports as the source of truth. Earlier draft runtime state is intentionally not
migrated; a runtime without the local v7 marker must be regenerated from the
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
