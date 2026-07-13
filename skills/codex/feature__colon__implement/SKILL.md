---
name: "feature:implement"
description: Implement a validated feature plan with the existing guarded serial branch, PR, review, merge, and cleanup commands. Use when the user invokes $feature:implement or asks to implement a feature plan folder.
---

# Feature Implementation

Implement one merge unit at a time with the existing `feature implement` lifecycle.

## Workflow

1. Require an unambiguous `<plan-dir>`, read its materialized epic, feature, and current-story files, and require `feature.plan.lock.json`. Create it with `feature validate <plan-dir> --write-lock --json` only when it is missing.
2. Use `feature status <plan-dir> --json` and `feature implement next <plan-dir> --json` to select the merge unit. Include the returned `story_progress_label` in progress updates and PR text.
3. Start one temporary worktree, implement the assigned stories, run relevant checks, commit, and record each lifecycle step through `feature implement ... --write-state`.
4. Before every external write, obtain explicit approval for that action. Push the branch, open a PR, and record its number and URL. Use a local branch-diff review only when PR creation is not approved.
5. Ask a fresh Codex subagent to review the current PR. Use `$pr:review:no-file <pr-number>` from the implementation worktree when that skill is available; otherwise use a generic PR-review subagent.
6. Apply only evidence-backed Critical or High findings. Critical/High means normal-flow failure, data loss, approval bypass, unintended external writes, or direct CLI incompatibility. Do not elevate speculative edge cases.
7. For each accepted Critical/High finding, run checks, make an additive commit, push it, and ask a fresh reviewer to inspect the updated PR. Repeat until a fresh review has no Critical or High findings; use no fixed iteration cap.
8. Apply worthwhile Medium or Low findings from that final review once, run checks, commit, and push. Record `review-status passed` when no findings were applied, or `review-status changes-applied` when the final Medium/Low fixes were committed.
9. When checks and policy allow it, merge the PR with explicit approval, record the merge commit, update local `main`, remove the worktree, and record cleanup. Delete the remote branch only when the plan permits it and the user approved deletion.

Stop for operator direction if plan or PR state is ambiguous. Do not invent state transitions or a separate recovery protocol.

```sh
feature implement start <plan-dir> --merge-unit <id> --branch <branch> --worktree <plan-dir>/worktrees/<id> --base-sha <sha> --write-state --json
feature implement commit <plan-dir> --merge-unit <id> --commit-sha <sha> --write-state --json
feature implement push <plan-dir> --merge-unit <id> --allow-push --write-state --json
feature implement open-pr <plan-dir> --merge-unit <id> --allow-open-pr --pr <number> --pr-url <url> --write-state --json
feature implement review <plan-dir> --merge-unit <id> --review-status passed|changes-applied --write-state --json
feature implement merge <plan-dir> --merge-unit <id> --allow-merge --merge-commit <sha> --write-state --json
feature implement cleanup <plan-dir> --merge-unit <id> --write-state --json
```

Do not edit `feature.plan.lock.json` by hand.
