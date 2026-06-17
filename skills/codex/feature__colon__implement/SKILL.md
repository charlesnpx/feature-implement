---
name: "feature:implement"
description: Implement a validated feature plan created by $feature, processing epics, features, stories, and merge units through guarded branch, PR, review, merge, and resume steps. Use when the user invokes $feature:implement or asks to implement a feature plan folder.
argument-hint: "[plan-dir]"
---

# Feature Implementation

Implement a validated feature plan.

## Preconditions

- Use the obvious current plan folder only when conversation context identifies exactly one. Otherwise require the plan path.
- Read `feature.plan.lock.json`; if missing, run `feature validate <plan-dir> --write-lock --json`.
- External writes such as push, PR creation, merge, and branch deletion require explicit user approval.
- Local git worktrees create git metadata under hidden paths. Get hidden-file approval before creating worktrees when the environment requires it.

## Workflow

1. Read each epic file, then each feature file, then each story file in the current merge unit.
2. Use `feature status <plan-dir> --json` and `feature implement next <plan-dir> --json` to identify the next merge unit.
3. Create one temporary isolated worktree for the active merge unit at `<plan-dir>/worktrees/<merge-unit-id>`, then record `feature implement start ... --write-state`.
4. Implement the story or merge unit, run repo checks, commit locally, then record `feature implement commit ... --commit-sha <sha> --write-state`.
5. Push the implementation branch, open a PR with a clear title and description, then record PR number/URL with `feature implement open-pr ... --write-state`.
6. For opened-PR reviews, check whether the active Codex Skills list includes `pr:review:no-file`. If it does, spawn a Codex subagent from the implementation worktree/repository path and instruct it to run `$pr:review:no-file <pr-number>` there to review the opened PR. If it does not, spawn the current generic Codex PR-review subagent. Use branch-diff review only when PR creation is not approved.
7. Run a PR review loop with a maximum of 10 fresh-review iterations. For each review with worthwhile findings, apply the selected fixes locally, run checks, and do not commit yet.
8. When using the generic Codex PR-review subagent, keep that reviewer agent alive; send it the addressed findings, changed file list, and relevant local diff; ask whether the patch resolves its specific concerns. Iterate locally until the reviewer confirms, or until you deliberately reject the concern with a concrete reason.
9. Commit and push the accepted fixes after they are confirmed by a generic reviewer or selected from a no-file review, then spawn a fresh subagent to review the updated PR using the same skill-selection rule. With `pr:review:no-file`, that fresh no-file review is the confirmation mechanism. Stop only when a fresh reviewer returns no findings worth addressing. If iteration 10 still has worthwhile findings, stop and report the remaining findings instead of merging.
10. Record review state with `feature implement review ... --review-status passed|changes-applied --write-state` only after the final reviewed branch has been pushed.
11. Merge only when checks and policy allow it. Record merge state with `feature implement merge ... --merge-commit <sha> --write-state`.
12. Update local main, remove the temporary worktree, then record `feature implement cleanup ... --write-state`. Delete the remote branch only when the plan permits it and the user explicitly approved it.
13. Confirm `feature implement next <plan-dir> --json` advances before continuing to the next merge unit.

Use these guarded CLI forms for write steps:

```sh
feature implement start <plan-dir> --merge-unit <id> --branch <branch> --worktree <plan-dir>/worktrees/<id> --base-sha <sha> --write-state --json
feature implement commit <plan-dir> --merge-unit <id> --commit-sha <sha> --write-state --json
feature implement push <plan-dir> --merge-unit <id> --allow-push --write-state --json
feature implement open-pr <plan-dir> --merge-unit <id> --allow-open-pr --pr <number> --pr-url <url> --write-state --json
feature implement review <plan-dir> --merge-unit <id> --review-status passed|changes-applied --write-state --json
feature implement merge <plan-dir> --merge-unit <id> --allow-merge --merge-commit <sha> --write-state --json
feature implement cleanup <plan-dir> --merge-unit <id> --write-state --json
```

The lock state is immutable and ordered. Do not edit `feature.plan.lock.json` by hand; always record lifecycle changes through `feature implement ... --write-state`.
