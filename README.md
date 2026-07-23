# feature-implement

`feature-implement` provides a self-contained Go CLI and delegated Codex and Claude skills for planning implementation work and executing it through immutable workspace-v2 definitions, a typed journal, isolated Git worktrees, protected review and authorization boundaries, and verified GitHub merge receipts.

It installs:

- `feature` under `~/.local/bin`
- `$feature` and `$feature:implement` for Codex
- `/feature` and `/feature:implement` for Claude Code

## Install contract

This repository follows the `mise-en-place` delegated installer contract. `install-skill.sh` is a thin wrapper around:

```sh
feature install-skills [--plan|--install|--uninstall] [--target claude|codex|tools|all] [--json] [--install-root <dir>]
```

The installer emits delegated JSON with `schema: 1`, `kind: delegated`, target file records, setup metadata, and SHA256 hashes for installed files. The `tools` target owns the self-contained Go binary.

## Command surfaces

The standalone version-one plan materializer remains available for planning-only consumers:

```sh
feature plan example
feature plan schema --json
feature plan materialize --manifest feature.plan.yaml --out-root <dir> --json
feature validate <plan-dir> [--write-lock] --json
```

Execution is workspace-v2 only. Direct `feature status` and `feature implement` lifecycle commands have been removed; plan locks no longer contain mutable runtime state.

```sh
feature workspace schema bundle --json
feature workspace schema requests --json
feature workspace example
feature workspace validate --bundle <bundle-dir> [--write-locks] --json
feature workspace init --bundle <bundle-dir> --workspace <runtime-dir> --input <request.json> --json
feature workspace status --bundle <bundle-dir> --workspace <runtime-dir> --json
feature workspace recover --bundle <bundle-dir> --workspace <runtime-dir> --input <request.json> --json
feature workspace scheduler|gates|queue|receipts|report --bundle <bundle-dir> --workspace <runtime-dir> --json
```

Mutation groups expose closed, typed subactions:

```text
reconcile  stage | plan | activate
attempt    reserve | materialize | boundary | next-goal | acknowledge | owner-response | resume
commit     next | rebase
review     start | reserve | record | reserve-fix | apply-fix | record-fix | ready
control    grant | revoke | safety | segment-complete | inspect-receipt
provider   reserve | preflight | dispatch | reconcile | abandon | authorize-pr
complete   verify
```

Every mutation accepts exactly one strict schema-version-2 JSON request through `--input <file|->`. Unknown fields, duplicate keys, trailing JSON, unsupported enum values, and oversized inputs fail before a transition is recorded. Use `feature workspace schema requests --json` as the request reference.

## Workspace bundle

A bundle is an immutable set of source files rooted by `feature.workspace.bundle.json`:

```text
sample-workspace/
├── feature.workspace.bundle.json
├── feature.workspace.yaml
├── plans/
│   └── sample-plan.yaml
├── config/
│   └── execution.yaml
├── authority/
│   └── owner-policy.json       # optional, externally pinned authority material
└── generated/                  # tool-owned immutable lock projections
```

The descriptor contains discovery and authority-selection bindings rather than runtime state. Its exact bytes, every referenced source byte, every authority pin, and the selected control-plane authority contribute to the effective generation. Source paths are relative, non-hidden, outside `generated/`, rooted beneath the bundle, and cannot traverse symlinks or collide across roles.

```json
{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/sample-plan.yaml"],
  "execution_config": "config/execution.yaml",
  "authorities": []
}
```

The workspace manifest owns repository, provider, base, remote, plan membership, cross-plan dependencies, and authority declarations. It never requires the primary checkout to be clean.

```yaml
schema_version: 2
id: sample-workspace
repository:
  root: /absolute/path/to/repository
  identity: https://github.com/example/project.git
provider:
  kind: github
  repository: example/project
base_ref: feature/sample-workspace
remote: origin
execution_config: config/execution.yaml
plans:
  - id: sample-plan
    source: plans/sample-plan.yaml
dependencies: []
authority_sources: []
```

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

Execution configuration assigns every merge unit exactly one effective profile, explicit boundary behavior, and policy that can only narrow its parent policy:

```yaml
schema_version: 2
policy:
  require_passing_checks: true
  require_signed_receipts: true
  allow_write_network: false
  max_attempts: 3
  max_review_rounds: 3
  max_review_fixes: 2
profiles:
  - id: standard
    runner: codex
    policy:
      require_passing_checks: true
      require_signed_receipts: true
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
      require_signed_receipts: true
      allow_write_network: false
      max_attempts: 3
      max_review_rounds: 3
      max_review_fixes: 2
```

Optional commit, review-fix, and review-loop protocols are strict schemas within each merge-unit execution entry. An absent commit protocol leaves normal local commits unconstrained; the first configured review atomically adopts the exact clean head. A configured commit protocol owns its commits and isolated checks.

Agent-driven broad review loops are capped at three iterations for the plan and for each merge unit. A new iteration starts only when the preceding review found a Critical or High issue. When a review has no Critical or High findings, worthwhile Medium and Low findings are still applied once, but another broad iteration is not started solely to re-review them.

`feature workspace validate --write-locks` synchronizes immutable projections under `<bundle>/generated/`. The ownership inventory permits replacement only when an existing generated file still matches its last-generated hash. Modified projections, hidden paths, symlink traversal, missing inventory, and unowned conflicts fail closed.

## Journal-backed execution

Keep runtime state separate from the source bundle, for example `<bundle>/runtime/`. Initialization writes the append-only journal, generation store, and disposable projections under `<runtime>/state/`.

1. Validate the bundle and initialize the runtime with a strict request containing `schema_version` and an RFC3339Nano `occurred_at` value.
2. Run `recover`, then read `status` or `scheduler`. Reserve only a `ready` merge unit against the exact integration-base Git object.
3. Materialize the reserved attempt. The CLI derives its flat branch and worktree identity and never modifies or cleans the primary checkout.
4. Work only in the returned attempt worktree. Use `commit next` for configured commit steps; otherwise make ordinary local commits. The first configured review adopts the exact clean descendant head; when no governed review is configured, submit `attempt adopt-head` before provider reservation. Record only receipt-backed governed-review results for their exact head and tree.
5. Treat every returned boundary directive as authoritative. Scheduler reports re-emit `boundary_pending`, `boundary_reason`, and `pending_directives`; complete goal acknowledgements and owner gates with matching signed control-plane receipts before resuming or creating another goal.
6. Obtain operator approval and a matching standing-grant receipt before provider writes. Reserve typed `push`, `open_pull_request`, or `merge` intents, then dispatch only through the trusted provider adapter. Ambiguous effects must be reconciled before further dispatch.
7. Verify completion independently. The completion receipt binds the branch, head, tree, PR checks and reviews, merge-commit parents and tree, integration topology, and final base head.
8. Record and resolve the final attempt boundary to release its lease and serial segment. A provider completion receipt alone does not complete the scheduler unit or unblock dependents; use the journal-derived report as the completion source of truth.

Provider results contain typed evidence and idempotency markers, never executable provider commands. The only provider intents are push, pull-request creation, and merge; completion always verifies a merge commit. GitHub observations bind only branch-protection-required check runs, commit-status contexts, and review decisions rather than treating optional activity as required. The primary checkout need not be clean.

## Protected receipts

Review evidence, standing grants, revocations, reconciliation activation, orchestration acknowledgements, and owner decisions require canonical signed control-plane receipts when their transition is protected. The bundle must pin the corresponding public authority material and identify it with `control_plane_authority`. Private signing keys are external to this repository and are never generated or stored by the CLI.

## Minimal smoke test

After creating the three source YAML files and descriptor above:

```sh
bundle_dir=/absolute/path/to/sample-workspace
runtime_dir="$bundle_dir/runtime"
request_file="$bundle_dir/init-request.json"

feature workspace validate --bundle "$bundle_dir" --write-locks --json
feature workspace init --bundle "$bundle_dir" --workspace "$runtime_dir" --input "$request_file" --json
feature workspace status --bundle "$bundle_dir" --workspace "$runtime_dir" --json
```

`init-request.json` contains one canonical request:

```json
{"schema_version":2,"occurred_at":"2026-07-22T12:00:00Z"}
```

## Development

```sh
gofmt -w cmd/feature/*.go internal/install/*.go internal/plan/*.go internal/workspace/*.go internal/workspacecmd/*.go
go test ./...
go test -shuffle=on ./...
go test -race ./internal/workspace ./internal/workspacecmd
go vet ./...
./install-skill.sh --plan --target all --json
stage="$(mktemp -d)"
./install-skill.sh --install --target all --json --install-root "$stage"
"$stage/.local/bin/feature" version
```

The exact-head CI baseline is also reusable locally. Set the exact commit expected
for the checkout, then run every Linux/macOS-compatible gate or an individual
gate:

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

The GitHub workflow checks out the pull request head SHA directly, disables
persisted checkout credentials and dependency caching, and runs the same gates
on pinned Ubuntu and macOS runners. A shuffled run prints its seed so it can be
reproduced with `FEATURE_SHUFFLE_SEED`.
