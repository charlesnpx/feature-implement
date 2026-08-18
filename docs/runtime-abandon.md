# Runtime Abandonment and Feature-Ref Release Marker

Status: Implemented.

This design record describes the explicit operation for ending a stalled,
dead, or superseded local workspace runtime without deleting its feature
branch.

## Problem resolved

A runtime records a creation intent whose digest appears in the feature ref's
first reflog message. That exact message prevents an unrelated runtime from
adopting the branch. Before this operation, a runtime that would never resume
left that marker behind indefinitely. A replacement runtime then could neither
create the configured ref nor prove that it owned the existing one.

The operation makes the decision explicit. It preserves the branch, its head,
and its reflog for operator inspection while recording that the runtime no
longer drives it in a separate private ref.

## Shipped schema

The command has no subaction:

```
feature workspace abandon --bundle <dir> --workspace <dir> --input <file|->
```

Its strict schema-version-two request is:

```json
{"schema_version":2,"occurred_at":"2026-08-18T15:04:05.000000000Z","reason":"superseded by generation sha256:..."}
```

`reason` is required, trimmed, valid UTF-8 text, and uses the existing
16 KiB free-text bound.

## Semantics

| Situation | Behavior |
|---|---|
| Ready runtime with an owned feature ref | Preflight its exact durable head and ownership marker, append one `workspace_abandoned` event, then create the private release-marker ref at that head with expected-absent compare-and-swap. |
| Creation intent exists but `feature_ref_created` does not | Append the abandonment event without a Git action. |
| Same reason submitted again | Verify the recorded abandonment and private release marker by its exact head and reflog message. If the marker is missing, recreate it at the recorded head; the feature ref is no longer checked. |
| Different reason submitted again | Refuse the request and retain the journal unchanged. |
| Runtime already completed | Refuse abandonment before changing the ref or journal. |
| Any later local workflow mutation | Refuse it because abandonment is final. Journal-tail recovery remains admitted. |

The release-marker ref is:

```
refs/feature-implement/released/<feature-branch>
```

It points at the runtime's durable owned head. Its creation reflog message is:

```
feature-implement feature-ref released <intent-digest>
```

The marker is valid only when both its head and newest reflog message exactly
match those recorded values. Before appending, the operation preflights the
feature ref at the durable head with its exact ownership marker. It appends the
abandonment event before creating the marker ref, so an append failure cannot
leave an orphaned marker without a durable explanation. If marker creation is
interrupted after the append, the recorded event identifies the exact marker to
recreate. Neither path updates the feature ref.

## Why release is not a feature-ref reflog entry

Git behavior is: “Git suppresses a reflog entry for a no-op update.” Updating
a ref from an object ID to that identical object ID does not append the release
message, including when `update-ref` is given `--create-reflog` and `-m`.
Therefore the feature ref cannot carry a reliable release entry without moving
or deleting it. The separate marker ref records the release while leaving both
the feature ref value and its reflog unchanged.

## Implemented enforcement

| Location | Shipped behavior |
|---|---|
| `internal/workspace/local_target_journal_events.go` | Defines `WorkspaceAbandonedJournalEvent`, validates the bounded reason, and binds the event to the configured feature ref and optional durable head. |
| `internal/workspace/local_target_journal_codec.go` | Canonically serializes and strictly reconstructs the abandonment event. |
| `internal/workspace/runtime_projection.go` | Projects abandonment, includes it in canonical replay bytes, admits it before feature-ref completion, and rejects later workflow mutations. |
| `internal/workspace/local_target_release.go` | Preflights the owned feature ref, records abandonment before the Git effect, and reconciles repeated requests from the recorded state. |
| `internal/workspace/local_target_git.go` and `internal/workspace/local_target_git_session.go` | Create and verify the private marker ref with an expected-absent transaction, exact head, and exact reflog message; preserve the feature ref; and identify released refs with an actionable message. |
| `internal/workspace/completion.go` and `internal/workspace/runtime_views.go` | Surface abandonment in reports and make it a completion blocker. |
| `internal/workspacecmd/` and `cmd/feature/main.go` | Decode the strict request, expose the command and schema, and return the refreshed report. |

## Why a marker ref is used rather than deleting the branch

The branch can contain integrated merge units or other local work that an
operator still needs. Deleting it would discard a useful frontier and make the
runtime operation destructive. The marker ref leaves the branch's object name,
value, and reflog unchanged while making the prior runtime's state unambiguous.

## Deliberate follow-up

A released feature ref is not automatically re-adoptable by a fresh runtime.
The release marker reserves that feature-branch name, so the operator must use
a new feature branch for a replacement runtime. Automatic re-adoption remains
outside this operation.
