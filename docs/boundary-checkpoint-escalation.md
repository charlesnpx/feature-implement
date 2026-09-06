# Boundary Checkpoints and Escalation

Status: Implemented.

This design record describes the boundary policy used by local attempts. It
keeps planned checkpoints and agent-raised exception stops explicit while
allowing units with no boundary to run unattended.

## Problem resolved

A planned checkpoint tells the runner where a pause is permitted. An
escalation lets the runner pause for a genuine runtime exception. The policy
keeps those two reasons distinct.

## Shipped schema

```yaml
merge_units:
  - plan_id: sample-plan
    merge_unit_id: first-contract
    profile: standard
    boundary:
      checkpoint: pause_only
      escalation: allowed         # allowed | forbidden
      serial_segment: serial-first-contract
```

`checkpoint` is the owner's planned gate. `escalation` is the agent's
permission to stop on its own.

### Semantics

| checkpoint | escalation | Behavior |
|---|---|---|
| `none` | `allowed` | Runs unit to unit without stopping. The agent may still stop if it hits something real. **This is the block-of-units default.** |
| `none` | `forbidden` | Cannot stop for any reason. The agent must finish or fail. For unattended and CI runs. |
| `pause_only` | `allowed` | Records a planned boundary and pauses the attempt. |
| `pause_only` | `forbidden` | Records the planned boundary and pauses; the agent may not raise its own stop. |

`checkpoint: none` makes an unattended block expressible.

## Boundary kind and reporting

Both arrive through `attempt pause`, whose input requires `kind` with no
default:

- **`checkpoint`** — legal only when `checkpoint != none`.
- **`escalation`** — legal only when `escalation: allowed`.

The recorded kind is carried on `AttemptBoundaryReachedJournalEvent`. A
paused attempt reports `boundary_pending: true`, uses that recorded kind for
`boundary_reason`, and emits one `pending_directives` item with
`kind: "boundary_pending"` and the same `boundary_kind`. An attempt without a
current boundary reports an empty `pending_directives` array.

At a boundary, the owner either resumes the attempt or abandons it. Resume
uses the recorded boundary goal; no owner-response or next-goal exchange is
required.

## Implemented enforcement

| Location | Shipped behavior |
|---|---|
| `internal/workspace/wire.go` | Merge-unit boundaries decode `checkpoint`, `escalation`, and unchanged `serial_segment`; profile boundaries decode `escalation` only. |
| `internal/workspace/policy.go` | `AttemptBoundaryPolicy` carries checkpoint, escalation, and serial segment; `ProfileBoundaryPolicy` carries escalation only. |
| `internal/workspace/attempt_lifecycle.go` | Pause rejects a checkpoint on `checkpoint: none` and an escalation on `escalation: forbidden` before appending. Resume uses the current boundary; abandon is the terminal local exit. |
| `internal/workspace/attempt_projection.go` | Boundary projection validates the recorded kind against the reserved policy and exposes a current boundary only while the attempt is paused. |
| Journal codec and events | The kind is serialized and reconstructed with the recorded boundary. |
| `internal/workspace/runtime_views.go` | Status and report project the recorded kind as the pending reason and boundary directive. |

## Profile narrowing

An execution profile may optionally declare `boundary` with `escalation` only.
`escalation` is an agent permission, so a merge unit may narrow `allowed` to
`forbidden` but may never widen `forbidden` to `allowed`.

Profiles do not declare a checkpoint; planned checkpoints remain on individual
merge units.

## Runner ordering and conditionality

The runner records a boundary only for a configured checkpoint other than
`none` or a genuine allowed escalation. It records that boundary before
`integrate merge-unit`, while the attempt is active. The owner then resumes or
abandons the paused attempt. A unit with no needed boundary proceeds directly
to integration.

Integration sets the attempt to `AttemptCompleted` and clears its lease:

```go
updated.phase = AttemptCompleted
updated.serialSegmentHeld = false
updated.leaseID = ID{}
```

`PauseAttempt` requires `AttemptActive`, so a boundary cannot be recorded
after integration.
