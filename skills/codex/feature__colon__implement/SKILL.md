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
2. Make ordinary local commits. When a commit protocol is configured, the
   workspace validates final base-to-head history: ordered checkpoints, exact
   messages, path constraints, and configured checks.
3. You may amend and reorganize intermediate commits before final-history
   validation. Keep the attempt worktree clean.
4. Configured checks run against an isolated materialization of the final
   accepted commit with repository hooks disabled and write-capable network
   denied. A host without a supported strict sandbox fails closed.

## Review

`feature-implement` records a review-gate fact; it neither conducts review nor
interprets a policy.

1. When a complete `review_gate` is configured, submit `review dispatch` after
   the attempt is clean. This records intent before it materializes a separate
   frozen copy at the exact head and tree.
2. Give the named adapter only that frozen copy and its opaque policy text, not
   the attempt worktree. A configured adapter may use a fresh Codex subagent
   according to its own policy; this workflow does not prescribe an iteration
   scheme.
3. After the adapter creates durable evidence, submit `review record` with its
   evidence digest. For a completed Witness run, use `review record-document`
   with its strict `review-report-v1` document instead; for Witness
   `failed_to_run`, use `review record` with the durable failure-evidence
   digest. The raw document is retained as the gate evidence.
4. Record exactly one terminal verdict: `satisfied`, `not_satisfied`, or
   `failed_to_run`. A failure to run is not a negative verdict and does not
   alter the attempt phase; use ordinary attempt lifecycle actions if the owner
   chooses to retry.
5. `review ready` only checks that a satisfied gate binds the exact current
   head and tree. It never runs an adapter. A changed artifact needs a fresh
   dispatch and terminal record.
6. Without a configured review gate, submit `attempt adopt-head` for the exact
   clean accepted head and tree.

Do not invent adapter evidence, terminal verdicts, or readiness.

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
   for the exact accepted head and tree. A configured review gate must be
   satisfied for that artifact; `review ready` can inspect the same binding,
   while review-optional work requires matching `attempt adopt-head`.
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
