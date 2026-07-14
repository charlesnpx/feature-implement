---
name: feature
description: Explicit /feature invocation only. Create a concise, implementation-ready feature plan with epics, features, stories, and merge units only when the user's current request contains a literal /feature invocation.
---

# Feature Planning

## Invocation Guard

Proceed only when the user's current request contains a literal `/feature` invocation. If this skill was selected for any other planning request, stop and ask the user to invoke `/feature` explicitly.

Create a plan the existing `feature` CLI can materialize and validate.

## Workflow

1. Derive a stable slug-style `<plan-id>`. Draft the manifest outside the repository under `~/tmp` or the system temp directory, set its `output_name` to `<plan-id>`, and materialize the plan at `~/tmp/feature-plans/<plan-id>/`.
2. Give every story concrete acceptance, implementation, and testing criteria. Default to one merge unit per story; group stories only when they are in one feature and have no unresolved outside dependency.
3. Quote every YAML string scalar, including IDs, names, summaries, and list items. Leave integers and booleans typed.
4. Run `feature plan materialize --manifest <manifest> --out-root ~/tmp/feature-plans --json`, then `feature validate <plan-dir> --json`.
5. Ask a fresh reviewer, a Claude subagent, to review the materialized plan for missing implementation detail, invalid dependencies, unsuitable merge units, and direct CLI incompatibilities.
6. Apply only evidence-backed Critical or High findings. Critical/High means normal-flow failure, data loss, approval bypass, unintended external writes, or direct CLI incompatibility. Do not turn speculative edge cases into blockers.
7. After each accepted Critical/High finding, rematerialize, validate, and ask a fresh reviewer to review the updated plan. Repeat until a fresh review has no Critical or High findings; use no fixed iteration cap.
8. Apply worthwhile Medium or Low findings from that final review once, rematerialize, validate, then run `feature validate <plan-dir> --write-lock --json`.

If state is ambiguous, stop and ask the operator rather than inventing recovery steps.

Return the plan directory, validation result, and implementation order.

## Manifest Contract

Require top-level `schema_version: 1`, `id`, `title`, and `epics`. Support `output_name`, `base_ref`, `remote`, `merge_policy`, and explicit `merge_units`.

Require every epic and feature to have `id`, positive `number`, `name`, and at least one child. Require every story to have `id`, positive `number`, `name`, `summary`, `acceptance`, `implementation`, `testing`, and any story-ID `dependencies`. Require every merge unit to have `id` and `story_ids`; use `allow_feature_level_pr: true` only for a valid same-feature grouping.

```yaml
schema_version: 1
id: "sample-migration-plan"
title: "Sample Migration Plan"
output_name: "sample-migration-plan"
base_ref: "main"
remote: "origin"
merge_policy:
  require_passing_checks: true
epics:
  - id: "epic-discovery"
    number: 1
    name: "Discovery"
    features:
      - id: "feature-inventory"
        number: 1
        name: "Inventory"
        stories:
          - id: "story-current-state"
            number: 1
            name: "Current State Inventory"
            summary: "Inventory: document systems, owners, dependencies, and risks."
            acceptance:
              - "Current systems and owners are listed."
            implementation:
              - "Review existing code paths and operating guidance."
            testing:
              - "Verify the inventory covers owners, dependencies, and risks."
merge_units:
  - id: "story-current-state"
    name: "Current State Inventory"
    story_ids:
      - "story-current-state"
```

Use `feature plan example` and `feature plan schema --json` as the current CLI reference.
