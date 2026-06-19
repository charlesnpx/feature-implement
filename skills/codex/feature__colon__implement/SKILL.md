---
name: "feature:implement"
description: Implement a validated feature plan created by $feature by synthesizing a feature workspace and processing epics, features, stories, and merge units through workspace-supervised branch, PR, review, merge, and recovery steps. Use when the user invokes $feature:implement or asks to implement a feature plan folder.
argument-hint: "[plan-dir]"
---

# Feature Implementation

Implement a validated feature plan through a single-plan feature workspace. The plan lock is source metadata; after workspace initialization, workspace state owns execution.

## Preconditions

- Use the obvious current plan folder only when conversation context identifies exactly one. Otherwise require the plan path.
- External writes such as push, PR creation, merge, and branch deletion require explicit user approval.
- Local git worktrees create git metadata under hidden paths. Get hidden-file approval before creating worktrees when the environment requires it.
- Stable repository skills use the `feature` command name. Local experimental installs may substitute another CLI name outside this repository.

## Workspace Initialization

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

## Supervisor Flow

1. Run `feature workspace status <workspace-dir> --json`, then `feature workspace recover <workspace-dir> --json`, then status again. Resolve blockers before claiming work.
2. Claim work with `feature workspace next <workspace-dir> --agent <id> --claim --json`.
3. Start the claimed attempt with `feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> --json`.
4. Treat the attempt-start JSON as the worker packet. It includes `workspace_id`, `repo`, `base_ref`, global `merge_unit_id`, `plan_id`, `plan_merge_unit_id`, `story_ids`, `dependencies`, `attempt_id`, `lease_id`, `branch`, `worktree`, `base_sha`, and `commands`.
5. Execute every returned `commands[]` worktree command before spawning or acting as the worker.
6. Record the local lifecycle start with `feature workspace transition ... --from pending --to in_progress --evidence worktree=<worktree> --json`.
7. Keep the lease alive with `feature workspace heartbeat <workspace-dir> --agent <id> --lease <id> --json` during long implementation, review, or external-write waits.
8. If work cannot continue, either release the lease before an attempt starts or abandon/fail the attempt with explicit evidence instead of leaving stale state for the next worker.

## Worker Flow

1. Work only in the `worktree` from the worker packet, and read only the packet's `plan_id`, `plan_merge_unit_id`, `story_ids`, and dependency context for the assigned implementation.
2. Implement the story, run repo checks, and commit locally on the packet `branch`.
3. Run a PR review loop with a maximum of 10 fresh-review iterations. For opened-PR reviews, check whether the active Codex Skills list includes `pr:review:no-file`; if it does, spawn a Codex subagent from the implementation worktree/repository path and instruct it to run `$pr:review:no-file <pr-number>` there. If it does not, spawn the current generic Codex PR-review subagent. Use branch-diff review only when PR creation is not approved.
4. After the final implementation commit or review-fix commit, refresh the branch with `feature workspace refresh-branch --local`. Include validation command results with `--command-result <command=status>`.
5. Run `feature workspace evaluate-gates` using the refreshed attempt, then record tool-proven review, security, or test evidence with `feature workspace gate record` using the returned input hash, head SHA, and base SHA.
6. Rerun `feature workspace evaluate-gates` after recording evidence. The queue, approval, and external-write steps must use the same refreshed head/base SHAs. Any later commit stales the refresh and gate evidence; refresh and evaluate again.
7. Enter the merge queue only after dependencies, contract checks, gates, approvals, and blockers are clear.

## External Writes

External writes remain explicitly approval-gated by the operator. Do not push, create PRs, merge, or delete remote branches unless the operator has approved that action for the exact branch or PR plus head/base SHAs.

After operator approval, record a scoped approval capability with `feature workspace approve grant` using the same action, branch or PR, head SHA, and base SHA that the provider command will use.

For each approved provider action, use `feature workspace external plan` and execute the returned commands in order: `approval_command`, `intent_command`, `provider_command`, then `result_command` after provider success. If the provider command fails and did not already record a result, record `failed_before_side_effect`, `failed_after_side_effect`, or `ambiguous` with `feature workspace external intent result`.

Use separate approvals and separate external intents for `push`, `open-pr`, `merge`, and `remote-delete`. Remote branch deletion must only be planned after accepted merge external intent evidence exists for the same attempt and matching head/base SHAs.

Complete the merge unit only after accepted external intent evidence proves the required provider writes are done. If the attempt entered the merge queue, completion requires accepted merge evidence:

```sh
feature workspace transition <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --from in_progress --to completed --evidence commit_sha=<head-sha> --evidence external_intent_ids=<intent-id>[,<intent-id>...] --json
```

Use failure transitions for abandoned work that should not be retried as-is:

```sh
feature workspace transition <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --from in_progress --to failed --evidence reason=<reason> --json
```

## Command Order

```sh
feature validate <plan-dir> --write-lock --json
feature workspace init --manifest <workspace-dir>/feature.workspace.yaml --write-lock --json
feature workspace status <workspace-dir> --json
feature workspace recover <workspace-dir> --json
feature workspace next <workspace-dir> --agent <id> --claim --json
feature workspace attempt start <workspace-dir> --merge-unit <id> --agent <id> --lease <id> --base-sha <sha> --json
feature workspace transition <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --from pending --to in_progress --evidence worktree=<worktree> --json
feature workspace heartbeat <workspace-dir> --agent <id> --lease <lease-id> --json
feature workspace refresh-branch <workspace-dir> --local --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --new-base <base-ref> --worktree <worktree> --command-result '<command>=passed' --json
feature workspace evaluate-gates <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --json
feature workspace gate record <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --gate test --status passed --input-hash <hash> --head-sha <head-sha> --base-sha <base-sha> --command '<command>' --summary '<summary>' --json
feature workspace evaluate-gates <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --json
feature workspace queue enter <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --branch <branch> --head-sha <head-sha> --base-sha <base-sha> --approval <approval-id> --json
feature workspace approve grant <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --action push --branch <branch> --head-sha <head-sha> --base-sha <base-sha> --expires-in <duration> --json
feature workspace external plan <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --approval <approval-id> --action push --branch <branch> --head-sha <head-sha> --base-sha <base-sha> --json
feature workspace external intent result <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --intent <intent-id> --status succeeded --details '<details>' --json
feature workspace transition <workspace-dir> --merge-unit <id> --attempt <attempt-id> --agent <id> --lease <lease-id> --from in_progress --to completed --evidence commit_sha=<head-sha> --evidence external_intent_ids=<intent-id>[,<intent-id>...] --json
```

The plan lock is immutable source input after workspace initialization. Record lifecycle changes only through workspace state commands.
