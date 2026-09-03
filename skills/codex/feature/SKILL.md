---
name: feature
description: Explicit $feature invocation only. Create an implementation-ready local workspace-v2 bundle with stories, merge units, execution policy, review policy, and exact Git bindings.
---

# Feature Planning

## Invocation guard

Proceed only when the user's current request contains a literal `$feature`
invocation. If this skill was selected for another request, stop and ask the
user to invoke `$feature` explicitly.

Create a strict schema-version-two workspace bundle that `$feature:implement`
can execute locally.

## Workflow

1. Derive a stable slug-style `<workspace-id>`. Use the operator's output
   directory when supplied; otherwise use
   `~/tmp/feature-plans/<workspace-id>/` when `~/tmp` exists or an equivalent
   system-temporary location. Do not draft inside the implementation
   repository unless that location was explicitly requested.
2. Resolve the target repository's canonical absolute root, fully qualified
   base ref, exact base commit, and object format with read-only Git queries.
   Choose a meaningful `feature/<kebab-case-name>` branch. Do not clean, stash,
   reset, switch, or otherwise change the primary checkout.
3. Create `feature.workspace.bundle.json`, `feature.workspace.yaml`, one or
   more `plans/*.yaml` files, and `config/execution.yaml`. Quote YAML string
   scalars, keep integers and booleans typed, and include required empty lists.
4. Give every story concrete acceptance, implementation, and testing criteria.
   Default to one merge unit per story. Group stories only when their
   dependency and review boundaries genuinely belong together.
5. Assign every merge unit exactly one execution profile, effective policy,
   and explicit boundary. Default to `pause_only` with a stable serial segment.
   When governed review is required, add one whole optional `review_gate` block
   with `adapter`, `recipe`, and `policy_file`.
6. Treat commit protocols as optional. Add one only when exact subjects, path
   constraints, ordered commits, or structured check checkpoints are part of
   the contract.
7. Run `feature workspace validate --bundle <bundle-dir> --json`. Fix strict
   decoding, coverage, dependency, target-binding, and policy errors.
8. Run at most three plan-review iterations. Each iteration asks a fresh Codex
   subagent to review the source bundle for missing implementation detail,
   invalid dependencies, unsafe grouping, unusable path constraints, and
   mismatches with the installed CLI. Do not use a PR-review skill for this
   review.
9. Apply evidence-backed Critical and High fixes and worthwhile Medium and Low
   fixes once, then validate again. Start another broad review only when the
   preceding review reported a Critical or High finding. Stop after a review
   with no Critical or High findings or after the third iteration.
10. Run `feature workspace validate --bundle <bundle-dir> --write-locks --json`,
    commit the plan sources and generated locks in the bundle repository, and
    verify the plan repository is clean at the committed `HEAD`.

If the target root, base ref, exact base commit, feature branch, execution
policy, or story scope is materially ambiguous, stop for operator direction.

Return the bundle directory, workspace ID, effective generation, committed plan
`HEAD`, generated lock digest, ordered merge units, and validation result.

## Bundle contract

The descriptor is strict JSON:

```json
{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/sample-plan.yaml"],
  "execution_config": "config/execution.yaml"
}
```

Every descriptor path is relative, non-hidden, outside `generated/`, uniquely
owned by one source role, and rooted beneath the bundle.

The workspace manifest owns local target and composition bindings:

```yaml
schema_version: 2
id: "sample-workspace"
mode: "local"
repository:
  root: "/absolute/path/to/repository"
base_ref: "refs/heads/main"
base_commit: "sha1:1111111111111111111111111111111111111111"
feature_branch: "feature/sample-workspace"
execution_config: "config/execution.yaml"
plans:
  - id: "sample-plan"
    source: "plans/sample-plan.yaml"
dependencies: []
```

Each plan requires `schema_version: 2`, `id`, `title`, nonempty `stories`, and
nonempty `merge_units`. Every story requires `id`, `summary`, nonempty
`acceptance`, `implementation`, and `testing`, plus an explicit `dependencies`
list. Every merge unit requires `id`, `name`, and `story_ids`.

Every policy level explicitly defines:

- `require_passing_checks`
- `allow_write_network`
- `max_attempts`

Child policies may only narrow their parent. A merge unit either inherits the
complete root `review_gate` or replaces it with another complete block; partial
overrides are rejected.

Use `feature workspace schema bundle --json`,
`feature workspace schema requests --json`, and `feature workspace example` as
the installed CLI references.
