---
name: "feature:implement"
description: Explicit /feature:implement invocation only. Execute a validated local workspace-v2 bundle through isolated attempt worktrees, exact-head review, deterministic integration, and local completion.
---

# Feature Implementation

## Invocation guard

Proceed only when the user's current request contains a literal
`/feature:implement` invocation. If this skill was selected for another
request, stop and ask the user to invoke `/feature:implement` explicitly.

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

## Start a merge unit

1. Run `feature workspace recover`, then `status`.
2. Select only a merge unit whose status is `ready`. Respect dependency
   order and the attempt budget.
3. Submit `attempt start` with the plan ID, merge-unit ID, next attempt
   number, and a stable merge-unit goal. The CLI derives the exact base,
   attempt identity, and detached worktree from locked runtime state.
4. Verify the returned detached worktree. Work only there; its history is
   scratch until an exact clean head is selected for integration.

Every successful mutation returns a fresh journal-derived workspace view. Use it as the
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
   Use a fresh Claude subagent with the exact base-to-head diff, read-only
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

## Integrate, pause when needed, and complete

1. Before integration, submit `attempt pause` with exact local evidence only
   when the merge unit configures a checkpoint other than `none`, or when the
   agent genuinely needs an allowed escalation. The request requires `kind`:
   use `checkpoint` for the configured gate and `escalation` for a genuine
   agent-raised stop. Record it while the attempt is active, before
   `integrate merge-unit`.
2. When a pause was recorded, call `attempt resume` once the same clean head is
   ready to continue. Do not resume a completed merge unit.
3. Use `attempt abandon` only to terminally exit a non-integrated attempt. It
   leaves the detached scratch directory intact for inspection.
4. After any required pause is resolved and the attempt is active again, or
   immediately when no boundary is needed, submit `integrate merge-unit` only
   for the exact accepted head and tree. Configured review requires matching
   `review ready`; review-optional work requires matching `attempt adopt-head`.
5. Integration creates a deterministic two-parent commit whose first parent is
   the expected feature head, second parent is the accepted attempt head, and
   tree is the accepted attempt tree. It compare-and-swap updates only the
   workspace-owned feature ref. The completed attempt cannot reach another
   `attempt pause`.
6. After all merge units are integrated, all pauses are resolved, and no
   attempt or local effect remains active, run `complete verify`.

## Finish

Run `status` again. Report the workspace ID,
generation, plan checkpoint, feature branch and head, completed merge units,
local completion digest, validation results, and any intentionally retained
attempt worktrees. Do not claim completion while a gate, directive, drift
condition, or recovery action remains unresolved.
