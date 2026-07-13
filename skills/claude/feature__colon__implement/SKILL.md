---
name: "feature:implement"
description: Implement a validated feature plan with the existing guarded serial branch, PR, review, merge, and cleanup commands. Use when the user invokes /feature:implement or asks to implement a feature plan folder.
---

# Feature Implementation

Implement one merge unit at a time with the existing `feature implement` lifecycle.

## Workflow

1. Require an unambiguous `<plan-dir>`, read its materialized epic, feature, and current-story files, and require `feature.plan.lock.json`. Create it with `feature validate <plan-dir> --write-lock --json` only when it is missing.
2. Use `feature status <plan-dir> --json` and `feature implement next <plan-dir> --json` to select the merge unit. Include the returned `story_progress_label` in progress updates and PR text.
3. Run and verify each Git or GitHub operation first. Use `feature implement ... --write-state` only to record verified results: create the worktree, implement the assigned stories, run relevant checks, and make the commit before recording their state.
4. Before every external write, obtain explicit approval for that action. Push the branch and verify its remote SHA before recording `push`; create the PR and capture its number and URL before recording `open-pr`. Use a local branch-diff review only when PR creation is not approved.
5. Ask a fresh Claude subagent to review the current PR.
6. Apply only evidence-backed Critical or High findings. Critical/High means normal-flow failure, data loss, approval bypass, unintended external writes, or direct CLI incompatibility. Do not elevate speculative edge cases.
7. For each accepted Critical/High finding, run checks, make an additive commit, push it, and ask a fresh reviewer to inspect the updated PR. Repeat until a fresh review has no Critical or High findings; use no fixed iteration cap.
8. Apply worthwhile Medium or Low findings from that final review once, run checks, commit, and push. Record `review-status passed` when no findings were applied, or `review-status changes-applied` when the final Medium/Low fixes were committed.
9. When checks and policy allow it, merge the PR with explicit approval and verify the merge commit before recording it. Update local `main`, remove the worktree, then record cleanup. Delete the remote branch only when the plan permits it and the user approved deletion.

Stop for operator direction if plan or PR state is ambiguous. Do not invent state transitions or a separate recovery protocol.

```sh
git worktree add -b <branch> <worktree> <base-ref>
feature implement start <plan-dir> --merge-unit <id> --branch <branch> --worktree <plan-dir>/worktrees/<id> --base-sha <sha> --write-state --json
git -C <worktree> commit
feature implement commit <plan-dir> --merge-unit <id> --commit-sha <sha> --write-state --json
git -C <worktree> push -u <remote> HEAD:<branch>
feature implement push <plan-dir> --merge-unit <id> --allow-push --write-state --json
cd <worktree> && gh pr create --base <base-ref> --head <branch>
feature implement open-pr <plan-dir> --merge-unit <id> --allow-open-pr --pr <number> --pr-url <url> --write-state --json
feature implement review <plan-dir> --merge-unit <id> --review-status passed|changes-applied --write-state --json
gh pr merge <pr-number-or-url> --merge
feature implement merge <plan-dir> --merge-unit <id> --allow-merge --merge-commit <sha> --write-state --json
git worktree remove <worktree>
feature implement cleanup <plan-dir> --merge-unit <id> --write-state --json
```

Do not edit `feature.plan.lock.json` by hand.
