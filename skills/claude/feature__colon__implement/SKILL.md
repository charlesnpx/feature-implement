---
name: "feature:implement"
description: Implement a validated feature plan created by /feature, processing epics, features, stories, and merge units through guarded branch, PR, review, merge, and resume steps. Use when the user invokes /feature:implement or asks to implement a feature plan folder.
argument-hint: "[plan-dir]"
---

# Feature Implementation

Implement a validated feature plan.

Preconditions:

- Use the obvious current plan folder only when conversation context identifies exactly one. Otherwise require the plan path.
- Read `feature.plan.lock.json`; if missing, run `feature validate <plan-dir> --write-lock --json`.
- External writes such as push, PR creation, merge, and branch deletion require explicit user approval.

Workflow:

1. Read each epic file, then each feature file, then each story file in the current merge unit.
2. Use `feature status <plan-dir> --json` and `feature implement next <plan-dir> --json` to identify the next merge unit.
3. Create an isolated worktree and branch for each merge unit. A merge unit defaults to one story.
4. Implement the story or merge unit, run repo checks, commit locally, then push/open PR only with explicit approval.
5. Spawn a Claude subagent to review the PR or branch diff. Assess findings and apply only useful fixes.
6. Commit and push accepted review fixes.
7. Merge/delete only when the plan permits it and the user explicitly approved it. Repeat runtime preflight before merge/delete.
8. Update local main after each merged unit and continue until all merge units are complete.

Use guarded CLI forms for write steps:

```sh
feature implement push <plan-dir> --merge-unit <id> --allow-push --json
feature implement open-pr <plan-dir> --merge-unit <id> --allow-open-pr --json
feature implement merge <plan-dir> --merge-unit <id> --allow-merge --allow-delete-branch --json
```
