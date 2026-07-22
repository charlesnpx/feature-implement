---
name: feature
description: Explicit /feature invocation only. Create an implementation-ready workspace-v2 bundle with stories, merge units, execution policy, review policy, boundaries, and pinned authority inputs.
---

# Feature Planning

## Invocation guard

Proceed only when the user's current request contains a literal `/feature` invocation. If this skill was selected for another request, stop and ask the user to invoke `/feature` explicitly.

Create a strict schema-version-2 workspace bundle that `/feature:implement` can execute without a compatibility bridge.

## Workflow

1. Derive a stable slug-style `<workspace-id>`. Use the operator's output directory when supplied; otherwise use `~/tmp/feature-plans/<workspace-id>/` when `~/tmp` exists or an equivalent system-temporary location. Do not draft inside the implementation repository unless that location was explicitly requested.
2. Resolve the repository's absolute root, canonical Git identity, GitHub `owner/repository`, integration base, and remote with read-only Git queries. Do not require, clean, stash, reset, or otherwise change the primary checkout.
3. Create `feature.workspace.bundle.json`, `feature.workspace.yaml`, one or more `plans/*.yaml` files, and `config/execution.yaml`. Quote every YAML string scalar. Keep integers and booleans typed, and include every required empty list explicitly.
4. Give every story concrete acceptance, implementation, and testing criteria. Default to one merge unit per story. Group stories only when their dependency and review boundaries genuinely belong in one PR.
5. Assign every merge unit exactly one execution profile, effective policy, and explicit boundary. Default to `pause_only` with a stable serial segment. Add a review loop and matching review-fix protocol when governed review is required; derive the narrowest practical allowed and frozen path patterns from the repository and story scope.
6. Treat commit protocols as optional. Add one only when exact subjects, path constraints, ordered commits, or structured check checkpoints are part of the contract. Never invent a strict commit sequence merely to make the schema look complete.
7. Require operator-supplied, externally pinned public authority material for protected receipts. Reference it consistently from the descriptor and workspace manifest. Never generate a private signing key, fabricate a receipt, or weaken `require_signed_receipts` to avoid an unavailable control plane.
8. Run `feature workspace validate --bundle <bundle-dir> --json`. Fix strict decoding, coverage, dependency, and policy errors before review.
9. Run at most three plan-review iterations. Each iteration asks a fresh Claude subagent to review the source bundle for missing implementation detail, invalid dependencies, unsafe grouping, policy weakening, unusable path constraints, missing authority inputs, and direct CLI incompatibility.
10. After each review, apply fixes for its evidence-backed Critical and High findings and apply worthwhile Medium and Low fixes once, then validate again. Start another plan-review iteration only when that review reported at least one Critical or High finding and fewer than three iterations have run. If it reported no Critical or High findings, stop the review loop after applying worthwhile Medium and Low findings; do not start another plan-review iteration. If the third review still reports Critical or High findings, apply the supported fixes, validate, stop at the cap, and report that no subsequent clean review was obtained.
11. Run `feature workspace validate --bundle <bundle-dir> --write-locks --json`. Treat `generated/` as tool-owned immutable projections; never edit its locks or ownership inventory by hand.

If repository identity, provider identity, integration base, execution policy, or required authority material is ambiguous, stop for operator direction instead of inventing it.

Return the bundle directory, workspace ID, effective generation, ordered merge units, validation result, and any authority material the operator must still provide.

## Bundle contract

The descriptor is strict JSON:

```json
{
  "schema_version": 2,
  "workspace": "feature.workspace.yaml",
  "plans": ["plans/sample-plan.yaml"],
  "execution_config": "config/execution.yaml",
  "authorities": []
}
```

Every descriptor path is relative, non-hidden, outside `generated/`, uniquely owned by one source role, and rooted beneath the bundle. Authority entries use `git_blob` with repository/commit/blob pins or `external_digest` with an expected SHA256 source digest. Set `control_plane_authority` only to an authority ID present in that same descriptor; the descriptor and that selection are generation-bound.

The workspace manifest owns repository and composition authority:

```yaml
schema_version: 2
id: "sample-workspace"
repository:
  root: "/absolute/path/to/repository"
  identity: "https://github.com/example/project.git"
provider:
  kind: "github"
  repository: "example/project"
base_ref: "feature/sample-workspace"
remote: "origin"
execution_config: "config/execution.yaml"
plans:
  - id: "sample-plan"
    source: "plans/sample-plan.yaml"
dependencies: []
authority_sources: []
```

Each plan requires `schema_version: 2`, `id`, `title`, nonempty `stories`, and nonempty `merge_units`. Every story requires `id`, `summary`, nonempty `acceptance`, `implementation`, and `testing`, plus an explicit `dependencies` list. Every merge unit requires `id`, `name`, and `story_ids`.

Execution configuration requires a complete top-level policy, at least one execution profile, and exactly one merge-unit entry per planned merge unit. Every policy level explicitly defines:

- `require_passing_checks`
- `require_signed_receipts`
- `allow_write_network`
- `max_attempts`
- `max_review_rounds`
- `max_review_fixes`

Child policies may only narrow parent policy. Review profiles declare an ID, runner, and `retain` or `fresh_each_invocation` reviewer policy. A review loop lists profile IDs and a bounded infrastructure retry count. A review-fix protocol defines its subject prefix, body policy, allowed paths, frozen paths, and structured checks.

Use `feature workspace schema bundle --json`, `feature workspace schema requests --json`, and `feature workspace example` as the installed CLI references.
