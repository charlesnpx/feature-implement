---
name: "feature:implement"
description: Explicit $feature:implement invocation only. Execute a validated local workspace-v2 bundle through isolated attempt worktrees, exact-head review, deterministic integration, and local completion.
---

# Feature Implementation

## Invocation guard

Proceed only when the user's current request contains a literal
`$feature:implement` invocation. If this skill was selected for another
request, stop and ask the user to invoke `$feature:implement` explicitly.

Execute one validated workspace-v2 bundle through its local journal.

## Preconditions

1. Use the obvious bundle only when context identifies exactly one; otherwise
	   require `<bundle-dir>`. Require its strict
	   `feature.workspace.bundle.json` and every referenced source.
2. Run `feature workspace validate --bundle <bundle-dir> --write-locks --json`.
	   Treat the source files and `generated/` projections as immutable for the
	   active generation. Require the bundle repository to be clean at a committed
	   `HEAD` containing the exact source and lock bytes.
3. Use a dedicated `<runtime-dir>` and `<worktree-root>` outside the primary
   checkout and target repository. Existing runtime state is authoritative. If
   it exists, run `recover`; otherwise initialize it with a strict request that
   contains the absolute worktree root.
4. Verify the configured local repository root, fully qualified base ref,
   pinned base commit, feature branch, and current Git object format. The
   primary checkout may be dirty and must not be cleaned, stashed, reset,
   switched, or used for implementation.
5. Read `feature workspace schema requests --json` before constructing
   mutations. Each mutation is one strict schema-version-two JSON value.

If the journal, active generation, target binding, committed plan `HEAD`, or Git
topology is missing, contradictory, or ambiguous, stop for operator direction.
Never edit `generated/`, `<runtime-dir>/state/`, or journal records by hand.

## Select and materialize a merge unit

1. Run `feature workspace recover`, then `status`, `scheduler`, `gates`, and
   `report`.
2. Select only a scheduler unit whose status is `ready`. Respect dependency
   order and the attempt budget.
3. Submit `attempt reserve` with the plan ID, merge-unit ID, next attempt
   number, and a stable merge-unit goal. The CLI derives the exact base,
   attempt identity, branch, and worktree from locked runtime state.
4. Submit `attempt materialize` for the returned attempt ID.
5. Verify the returned worktree and branch. Work only there.

Every successful mutation returns a fresh journal-derived report. Use it as the
source of truth.

## Implement and commit

1. Implement the merge unit's stories and testing criteria in the attempt
   worktree.
2. When a commit protocol is configured, stage only the next step's allowed
   changes and use `feature workspace commit next`. The workspace shell owns
   the exact commit and its structured checks.
3. Without a commit protocol, ordinary local commits are allowed. Keep the
   attempt worktree clean.
4. Configured checks run against an isolated materialization of the recorded
   commit with repository hooks disabled and write-capable network denied. A
   host without a supported strict sandbox fails closed.

## Review

Run a broad read-only review loop for every merge unit.

1. Treat each fresh broad audit as one review iteration and run at most three.
   Use a fresh Codex subagent with the exact base-to-head diff, read-only
   repository access, ephemeral scratch, disabled repository hooks, no
   write-capable network, and no external-write permission.
2. Apply evidence-backed Critical and High fixes and worthwhile Medium and Low
   fixes once. Targeted confirmation stays within the same iteration.
3. Start another broad audit only when the preceding audit reported a Critical
   or High finding. Stop after a review with no Critical or High findings or
   after the third iteration.
4. For a configured review loop, submit `review start`, reserve the exact
   invocation with `review reserve`, and submit the local result through
   `review record`. Preserve exact finding details, evidence digests, the
   descriptive reviewer label, request digest, head, tree, and isolation
   fields.
5. Use `review reserve-fix`, `apply-fix`, and `record-fix` for accepted
   findings. Seek `review ready` without exceeding configured budgets.
6. Without a configured review loop, apply accepted fixes with ordinary local
   commits, rerun validation, then submit `attempt adopt-head` for the exact
   clean accepted head and tree.

Do not invent findings, reviewer identity claims, isolation state, or
readiness.

## Integrate, resolve boundaries, and complete

1. Submit `integrate merge-unit` only for the exact accepted head and tree.
   Configured review requires matching `review ready`; review-optional work
   requires matching `attempt adopt-head`.
2. Integration creates a deterministic two-parent commit whose first parent is
   the expected feature head, second parent is the accepted attempt head, and
   tree is the accepted attempt tree. It compare-and-swap updates only the
   workspace-owned feature ref.
3. Submit `attempt boundary` with exact local evidence. Treat every returned
   directive as authoritative workflow state:

   - `complete_goal_and_wait`: complete the exact goal and submit
     `attempt acknowledge` with the returned directive and idempotency values.
   - `owner_gate`: obtain the listed owner choice and submit
     `attempt owner-response` with the exact boundary, directive, goal, and
     expected head.
   - `create_next_goal`: create exactly the returned goal, then submit its
     matching acknowledgement.

4. Call `attempt resume` only after every required acknowledgement and owner
   response is recorded. Do not resume a completed merge unit.
5. After all merge units are integrated, all boundaries are resolved, and no
   attempt or local effect remains active, run `complete verify`.

## Finish

Run `status`, `scheduler`, `gates`, and `report` again. Report the workspace ID,
generation, plan checkpoint, feature branch and head, completed merge units,
local completion digest, validation results, and any intentionally retained
attempt worktrees. Do not claim completion while a gate, directive, drift
condition, or recovery action remains unresolved.
