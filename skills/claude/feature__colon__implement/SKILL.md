---
name: "feature:implement"
description: Implement a validated feature plan created by /feature by synthesizing a feature workspace and processing epics, features, stories, and merge units through workspace-supervised branch, PR, review, merge, and recovery steps. Use when the user invokes /feature:implement or asks to implement a feature plan folder.
argument-hint: "[plan-dir]"
---

# Feature Implementation

Implement a validated feature plan through a single-plan feature workspace. The plan lock is source metadata; after workspace initialization, workspace state owns execution.

Preconditions:

- Use the obvious current plan folder only when conversation context identifies exactly one. Otherwise require the plan path.
- External writes such as push, PR creation, merge, and branch deletion require explicit user approval.
- Local git worktrees create git metadata under hidden paths. Get hidden-file approval before creating worktrees when the environment requires it.
- Stable repository skills use the `feature` command name. Local experimental installs may substitute another CLI name outside this repository.

Workspace initialization:

1. Read each epic file, then each feature file, then each story file relevant to the next work.
2. Read `feature.plan.lock.json`. If it is missing or stale, run `feature validate <plan-dir> --write-lock --json`, then read `feature.plan.lock.json` again.
3. Extract the plan lock `manifest_id`, `base_ref`, and `remote`. Stop with a clear error if `base_ref` or `remote` is empty; do not invent defaults for workspace execution.
4. Choose a `<workspace-dir>` beside or under the plan workspace area, then write `<workspace-dir>/feature.workspace.yaml` using the plan lock values:

```yaml
schema_version: 1
id: <safe-workspace-id>
repo: <implementation-repo-root>
base_ref: <base_ref from feature.plan.lock.json>
remote: <remote from feature.plan.lock.json>
plans:
  - id: <manifest_id from feature.plan.lock.json>
    path: <relative-or-absolute-plan-dir>
dependencies: []
```

5. Before initialization, compare the manifest `base_ref` and `remote` to `feature.plan.lock.json`. If either value does not match, stop and report the mismatch. `feature workspace init` performs the same validation against each referenced plan lock.
6. Run `feature workspace init --manifest <workspace-dir>/feature.workspace.yaml --write-lock --json`.
7. After workspace initialization, treat `feature.plan.lock.json` as read-only input. Do not hand-edit it and do not use direct plan lifecycle write-state commands for workspace-managed execution.

Recovery and claim checks:

1. Run `feature workspace status <workspace-dir> --json`.
2. Run `feature workspace recover <workspace-dir> --json`.
3. Re-run `feature workspace status <workspace-dir> --json` after recovery records actions, and resolve any blockers before claiming work.
4. Claim work with `feature workspace next <workspace-dir> --agent <id> --claim --json`.
5. Start the claimed attempt with `feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> --json`, then execute the returned worktree command before spawning or acting as the worker.

Workflow:

1. Keep supervisor state in workspace commands: status, recover, claim, attempt start, heartbeat, transition, gate, queue, and external intent commands.
2. Keep worker edits confined to the attempt worktree returned by the workspace command packet.
3. Run a PR review loop with a maximum of 10 fresh-review iterations. Spawn a Claude subagent to review the opened PR. Use branch-diff review only when PR creation is not approved.
4. Refresh the branch after the final implementation commit or review-fix commit, evaluate gates from that refreshed evidence to get the input hash, record tool-proven review, security, or test evidence with `feature workspace gate record`, rerun `feature workspace evaluate-gates` so the recorded output hash includes that evidence, and use those same base/head SHAs when entering the merge queue. Any later fix commit stales the refresh evidence; refresh again before gate evaluation, approval matching, or queue entry.
5. External writes remain approval-gated. Reserve and record workspace external intents for push, PR creation, merge, and cleanup actions so workspace state reflects the provider result before claiming completion.

```sh
feature validate <plan-dir> --write-lock --json
feature workspace init --manifest <workspace-dir>/feature.workspace.yaml --write-lock --json
feature workspace status <workspace-dir> --json
feature workspace recover <workspace-dir> --json
feature workspace next <workspace-dir> --agent <id> --claim --json
feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> --json
```

The plan lock is immutable source input after workspace initialization. Record lifecycle changes only through workspace state commands.
