# Agent Rebase Hygiene

This guide is for agents refreshing workspace implementation branches while preserving the branch's own contribution. Treat refresh work as a controlled operation with evidence, backups, and verification, not as routine cleanup.

## Terms

- Local unpublished refresh: rebase an unpushed local attempt branch onto a newer workspace base or parent ref.
- Published branch refresh: update a branch that has a remote ref or may be used by another actor.
- Contribution: the file and patch changes introduced by the branch relative to its old base.
- Backup ref: a local branch created before any rebase or rewrite.
- Expected remote SHA: the exact remote branch head observed before a published rewrite.

## Preflight

Before any refresh:

1. Confirm the worktree is clean.

   ```sh
   git -C <worktree> status --short --branch
   ```

2. Record the current branch, old base, new base, and current head.

   ```sh
   branch="$(git -C <worktree> branch --show-current)"
   pre_head="$(git -C <worktree> rev-parse HEAD)"
   old_base="$(git -C <worktree> merge-base HEAD <current-base-ref>)"
   new_base="$(git -C <worktree> rev-parse <new-base-ref>)"
   ```

3. Record changed files and stable patch IDs for the branch contribution before the rebase.

   ```sh
   git -C <worktree> diff --name-status "$old_base"...HEAD
   git -C <worktree> log --reverse --format=%H "$old_base"..HEAD |
     while read commit; do git -C <worktree> show "$commit" | git patch-id --stable; done
   ```

4. Stop if the old base, new base, current branch, or branch publication state is ambiguous.

## Local Unpublished Refresh

Use local refresh only when the branch is unpublished, or when the operator has confirmed that no remote/shared branch must be updated.

Create the backup before any rebase:

```sh
backup="${branch}-backup-$(date -u +%Y%m%dT%H%M%SZ)"
git -C <worktree> branch "$backup" "$branch"
```

Refresh the branch onto the new base:

```sh
git -C <worktree> rebase --onto "$new_base" "$old_base" "$branch"
```

After a successful rebase, record:

- `old_base`
- `new_base`
- `pre_head`
- `post_head`
- `backup_ref`
- changed files before and after refresh
- stable patch IDs before and after refresh
- validation command results

Verify preservation:

```sh
post_head="$(git -C <worktree> rev-parse HEAD)"
git -C <worktree> diff --name-status "$new_base"...HEAD
git -C <worktree> log --reverse --format=%H "$new_base"..HEAD |
  while read commit; do git -C <worktree> show "$commit" | git patch-id --stable; done
git -C <worktree> range-diff "$old_base".."$backup" "$new_base"..HEAD
```

The refresh is acceptable only when the branch contribution is preserved. If changed files disappear, patch IDs do not map to equivalent changes, commits are lost, or validation fails, keep the backup and block the merge unit with `refresh_verification_failed`.

Do not delete backup refs automatically.

## Published Branch Refresh

Published branch refresh is an external write. Before rewriting the remote branch, agents must have explicit approval and a reserved external intent. The provider command performs the rewrite, and the intent result must be recorded immediately after the provider command reports its outcome.

Before planning the write, capture and validate the expected remote head:

```sh
expected_remote_sha="$(git ls-remote origin "refs/heads/$branch" | awk '{print $1}')"
if [ -z "$expected_remote_sha" ]; then
  echo "remote_branch_moved"
  exit 1
fi
```

After local preservation checks pass, the remote update must use force-with-lease against that exact expected SHA:

```sh
git -C <worktree> push \
  --force-with-lease="refs/heads/$branch:$expected_remote_sha" \
  origin "HEAD:$branch"
```

If the push rejects because the remote no longer equals `expected_remote_sha`, stop and block with `remote_branch_moved`. Do not fall back to a plain force push.

Agents must not run published rewrites directly, and must not use the existing workspace push provider for refresh rewrites. That provider is for ordinary pushes and does not include a force-with-lease boundary. Until `publish-refresh` is available, stop after local verification, keep the backup, and ask for operator direction so any published rewrite records approval, intent, the exact expected remote SHA, the force-with-lease command, and result evidence.

## Relation To rebase-up

Refresh hygiene is not the `rebase-up` workflow.

Use refresh hygiene for one workspace attempt branch that needs a newer base while preserving its own contribution.

Use `rebase-up` for an ordered stack of dependent branches where lower branch changes must propagate upward. The `rebase-up` workflow requires the full root-to-tip branch chain, user confirmation of that chain, backups for every rebased branch, per-branch verification, and no force-push. Do not use `refresh-branch` to walk a stack, and do not weaken the `rebase-up` confirmation or verification rules.

## Escalation Boundaries

Stop and ask for operator direction when:

- the worktree is dirty
- the branch publication state is unknown
- the branch is part of a stack and the ordered chain is not confirmed
- the rebase conflicts
- contribution preservation checks fail
- validation commands fail
- expected remote SHA is missing or changed before a published rewrite
- approval or external intent is missing for a published rewrite

The safe default is to leave the backup in place, record the blocking condition, and avoid any external write.
