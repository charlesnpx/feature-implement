---
name: feature
description: Break a scoped implementation task into agile-style epics, features, stories, and merge units, materialize a structured feature plan folder, review it with a subagent, apply useful findings, and validate the plan. Use when the user invokes /feature or asks to create a feature implementation plan.
argument-hint: "[--out <folder>] <scope/task>"
---

# Feature Planning

Create a feature implementation plan from the user's scope.

1. Parse the requested scope and optional output folder.
2. Draft `feature.plan.yaml` with `schema_version: 1`, stable IDs, epics, features, stories, dependencies, and merge units.
3. Default to one merge unit per story. Use a feature-level merge unit only when all included stories are in the same feature and have no unresolved dependency on an outside story.
4. Run `feature plan materialize --manifest <manifest> --out-root <folder> --json`.
5. Spawn a Claude subagent to review hierarchy, story granularity, dependencies, merge units, missing caveats, and implementation order.
6. Apply useful review findings, then run `feature validate <plan-dir> --write-lock --json`.

Return the plan directory, validation status, and implementation order.
